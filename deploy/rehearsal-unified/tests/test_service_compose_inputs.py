"""Exact per-service original Compose provenance; no Docker or server fixtures."""
import copy
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch

import test_old_input_preservation as old_input_tests
import lifecycle


class MixedComposeTests(unittest.TestCase):
    def setUp(self):
        old_input_tests.OldInputTests.setUp(self)
        self.project = self.projects[0]
        self.base = self.project['compose_files'][0]
        self.overlay = self.old/'platform/server-prod.json'
        self.overlay.write_text(json.dumps({'services': {'platform-api': {'image': 'sha256:'+'a'*64}}}))
        self.project['compose_files'] = [self.base, str(self.overlay)]
        self.project['service_inputs'] = {
            service: {'compose_files': [self.base] if entry['role'] == 'platform-postgres' else [self.base, str(self.overlay)],
                      'working_dir': str(Path(self.base).parent)}
            for service, entry in self.project['services'].items()}

    def capture(self):
        return old_input_tests.OldInputTests.capture(self)

    def test_capture_binds_each_complete_service_list_and_every_file(self):
        descriptor = self.capture()
        self.assertEqual(json.loads(self.proof.read_text())['projects'], self.projects)
        files = {row['path']: row['sha256'] for row in json.loads(self.proof.read_text())['files']}
        self.assertEqual(files[str(self.overlay)], lifecycle.digest(self.overlay))
        self.assertEqual(files[self.base], lifecycle.digest(self.base))
        with patch.object(lifecycle, '__file__', str(self.new/'deploy/rehearsal-unified/lifecycle.py')):
            self.assertEqual(lifecycle.require_old_inputs(self.config), descriptor)

    def test_each_runtime_service_accepts_only_its_full_original_labels(self):
        for service, original in self.project['service_inputs'].items():
            state = {'service': service, 'compose_files': ','.join(original['compose_files']),
                     'working_dir': original['working_dir']}
            lifecycle.require_original_compose_labels(self.project, state)
            altered = dict(state, compose_files=','.join([self.base, str(self.overlay)] if service == 'platform-postgres' else [self.base]))
            with self.subTest(service=service), self.assertRaises(lifecycle.OperatorError):
                lifecycle.require_original_compose_labels(self.project, altered)

    def driver(self):
        value = object.__new__(lifecycle.DockerDriver)
        value.config = self.config; value.docker = ['docker']; value.state = self.state
        return value

    def source_root(self):
        return patch.object(lifecycle, '__file__', str(self.new/'deploy/rehearsal-unified/lifecycle.py'))

    def test_missing_extra_unknown_service_and_invalid_working_directory_are_rejected(self):
        original = copy.deepcopy(self.project['service_inputs'])
        variants = [None, {}, {k:v for k,v in original.items() if k != 'platform-postgres'},
                    {**original, 'migrate': copy.deepcopy(original['platform-api'])}]
        for value in variants:
            with self.subTest(value=value), self.assertRaises(lifecycle.OperatorError):
                lifecycle.original_runtime_groups({**self.project, 'service_inputs':value})
        for update in ({'working_dir':str(self.old)}, {'compose_files':[]},
                       {'compose_files':[self.base,self.base]}, {'compose_files':[{}]},
                       {'unexpected':'field'}):
            changed = copy.deepcopy(self.project)
            changed['service_inputs']['platform-postgres'].update(update)
            with self.subTest(update=update), self.assertRaises(lifecycle.OperatorError):
                lifecycle.preserved_input_files([changed])
        with self.assertRaises(lifecycle.OperatorError):
            lifecycle.original_service_project(self.project, 'migrate')

    def test_order_and_directory_labels_are_exact_not_subset_or_prefix(self):
        state={'service':'platform-api','compose_files':','.join([self.base,str(self.overlay)]),
               'working_dir':str(self.overlay.parent)}
        lifecycle.require_original_compose_labels(self.project,state)
        for change in ({'compose_files':','.join([str(self.overlay),self.base])},
                       {'working_dir':str(self.old)}, {'service':'migrate'}, {'working_dir':None}):
            with self.subTest(change=change), self.assertRaises(lifecycle.OperatorError):
                lifecycle.require_original_compose_labels(self.project,{**state,**change})

    def test_service_only_file_is_preserved_contained_and_cannot_drift(self):
        self.project['compose_files']=[self.base]
        descriptor=self.capture()
        self.assertIn(str(self.overlay),[row['path'] for row in json.loads(self.proof.read_text())['files']])
        before=self.overlay.read_bytes();self.overlay.write_bytes(before+b'\n')
        with self.source_root(), self.assertRaises(lifecycle.OperatorError):lifecycle.require_old_inputs(self.config)
        self.overlay.write_bytes(before)
        outside=self.new/'foreign.json';outside.write_text('{"services":{"api":{}}}')
        self.project['service_inputs']['platform-api']['compose_files']=[str(outside)]
        self.project['service_inputs']['platform-api']['working_dir']=str(self.new)
        with self.assertRaises(lifecycle.OperatorError):
            lifecycle.require_retained_layout(self.config,[str(self.old)],str(self.new),str(self.proof))
        self.assertEqual(descriptor['sha256'],lifecycle.digest(self.proof))

    def test_durable_snapshot_rejects_any_per_service_provenance_edit(self):
        self.capture()
        snapshot={'old_input_snapshot':self.config['previous']['input_snapshot'],
                  'project_files':lifecycle.snapshot_project_files(self.projects)}
        lifecycle.require_snapshot_files(snapshot)
        for key,value in [('working_dir',str(self.old)),('compose',[]),('unknown',True)]:
            changed=copy.deepcopy(snapshot)
            changed['project_files'][0]['service_inputs']['platform-postgres'][key]=value
            with self.subTest(key=key), self.assertRaises(lifecycle.OperatorError):lifecycle.require_snapshot_files(changed)
        changed=copy.deepcopy(snapshot);changed['project_files'][0]['service_inputs']['migrate']={}
        with self.assertRaises(lifecycle.OperatorError):lifecycle.require_snapshot_files(changed)
        body=self.proof.read_bytes();self.proof.write_bytes(body+b' ')
        with self.assertRaises(lifecycle.OperatorError):lifecycle.require_snapshot_files(snapshot)

    def test_json_object_key_order_does_not_change_the_same_snapshot_binding(self):
        pg_overlay=self.old/'platform/pg-original.json';pg_overlay.write_text('{"services":{"platform-postgres":{}}}')
        self.project['compose_files']=[self.base]
        self.project['service_inputs']['platform-postgres']['compose_files']=[self.base,str(pg_overlay)]
        self.capture()
        self.project['services']={'platform-postgres':self.project['services']['platform-postgres'],
                                  **{k:v for k,v in self.project['services'].items() if k!='platform-postgres'}}
        snapshot={'old_input_snapshot':self.config['previous']['input_snapshot'],
                  'project_files':lifecycle.snapshot_project_files(self.projects)}
        try:lifecycle.require_snapshot_files(snapshot)
        except lifecycle.OperatorError as error:self.fail('JSON key order must preserve the same binding: '+str(error))

    def test_stop_and_restart_use_exact_inputs_and_only_the_group_services(self):
        self.capture();driver=self.driver();calls=[]
        driver.command=lambda name,argv,**kw:(calls.append((name,argv)) or subprocess.CompletedProcess(argv,0,b'',b''))
        with self.source_root(), patch('preflight.check_ports'):
            driver.stop_old();driver.start_old()
        for phase in ('stop-old-platform','restore-old-platform'):
            selected=[(n,a) for n,a in calls if n.startswith(phase)]
            self.assertEqual(len(selected),2)
            seen=[]
            for _,argv in selected:
                files=[argv[i+1] for i,x in enumerate(argv) if x=='-f']
                self.assertIn('--project-directory',argv)
                self.assertEqual(argv[argv.index('--project-directory')+1],str(Path(self.base).parent))
                self.assertEqual(argv[argv.index('--env-file')+1],str(self.env))
                names=[s for s in self.project['services'] if s in argv]
                self.assertTrue(names)
                for name in names:self.assertEqual(files,self.project['service_inputs'][name]['compose_files'])
                if phase.startswith('restore'):
                    self.assertIn('--no-deps',argv);self.assertEqual(argv[argv.index('--pull')+1],'never')
                seen.extend(names)
            self.assertCountEqual(seen,self.project['services'])
        checks=[a for n,a in calls if n.startswith('verify-stopped-')]
        self.assertEqual(len(checks),4)
        self.assertTrue(all(a[-4:]==['ps','--status','running','-q'] for a in checks))

    def test_input_drift_rejects_before_first_stop_or_restart(self):
        self.capture();driver=self.driver();calls=[]
        driver.command=lambda *a,**k:calls.append(a)
        self.overlay.write_bytes(self.overlay.read_bytes()+b'\n')
        for operation in (driver.stop_old,driver.start_old):
            with self.source_root(),self.assertRaises(lifecycle.OperatorError):operation()
            self.assertEqual(calls,[])

    def test_images_and_mounts_only_use_their_own_service_input_group(self):
        driver=self.driver();driver.projects=lambda side:[self.project]
        original=[{'project':self.project['name'],'service':s,'role':row['role'],
                   'mounts':[{'type':'bind','source':'/retained/'+s,'target':'/data','rw':False}]}
                  for s,row in self.project['services'].items()]
        bad_api=False
        def compose(project,name,args):
            services={s:{'image':'sha256:'+'a'*64,'volumes':[{'type':'bind','source':'/retained/'+s,'target':'/data','read_only':True}]}
                      for s in self.project['services']}
            # The overlay changes PG: applying it to the base-only PG is wrong.
            if str(self.overlay) in project['compose_files']:
                services['platform-postgres']={'image':'sha256:'+'b'*64,'volumes':[{'type':'bind','source':'/wrong-pg','target':'/data'}]}
            if bad_api:services['platform-api']['image']='sha256:'+'b'*64
            return subprocess.CompletedProcess([],0,json.dumps({'services':services}).encode(),b'')
        driver.compose=compose
        driver.command=lambda name,args:subprocess.CompletedProcess([],0,args[-3].encode(),b'')
        driver.verify_original_plan(original)
        bad_api=True
        with self.assertRaises(lifecycle.OperatorError):driver.verify_original_plan(original)

    def test_inventory_keeps_whole_project_guard_and_uses_service_labels(self):
        driver=self.driver();driver.projects=lambda side:[self.project];calls=[];extra_running=False;bad_label=False
        def compose(project,name,args):
            calls.append((name,project,args))
            if '--services' in args:
                body=' '.join([*self.project['services'],*(['unreviewed-running'] if extra_running else [])])
            else:body=args[-1]
            return subprocess.CompletedProcess([],0,body.encode(),b'')
        def command(name,args):
            service=args[2];selected=self.project['service_inputs'][service]
            state={'project':self.project['name'],'service':service,'image':'sha256:'+'a'*64,
                   'running':True,'exit_code':0,'health':'healthy','ports':{},'mounts':[],
                   'compose_files':','.join(self.project['compose_files'] if bad_label else selected['compose_files']),
                   'working_dir':selected['working_dir']}
            return subprocess.CompletedProcess([],0,json.dumps(state).encode(),b'')
        driver.compose=compose;driver.command=command
        rows=driver.inventory('previous');self.assertEqual(len(rows),4)
        for name,project,args in calls:
            if name.startswith('container-id-'):
                self.assertEqual(project['compose_files'],self.project['service_inputs'][args[-1]]['compose_files'])
        extra_running=True
        with self.assertRaises(lifecycle.OperatorError):driver.inventory('previous')
        extra_running=False;bad_label=True
        with self.assertRaises(lifecycle.OperatorError):driver.inventory('previous')

    def test_database_reads_and_recovery_use_pg_inputs_and_default_permission_job(self):
        self.capture();driver=self.driver();calls=[]
        self.config['previous']['databases']={domain:{'project':'old-'+domain,'service':domain+'-postgres','owner':'database-owner','database':domain}
                                            for domain in ('platform','invoice')}
        driver.compose=lambda project,name,args:(calls.append((project,name,args)) or subprocess.CompletedProcess([],0,b'header\nvalue\n',b''))
        driver.ledger_snapshot('previous');driver.platform_permissions_snapshot('previous')
        selected=[project for project,name,args in calls if project['kind']=='platform']
        self.assertEqual(len(selected),7)
        self.assertTrue(all(project['compose_files']==[self.base] for project in selected))
        calls.clear();driver.verify_original_plan=lambda rows:None
        driver.ledger_snapshot=lambda side:{};driver.platform_permissions_snapshot=lambda side:[]
        snap={'old_input_snapshot':self.config['previous']['input_snapshot'],'project_files':lifecycle.snapshot_project_files(self.projects),
              'containers':[],'ledger_hashes':{},'platform_permissions':[]}
        lifecycle.atomic_json(self.state/'deployment-record.json',{'snapshot':snap})
        with self.source_root():driver.restore_permissions()
        pg=[(project,args) for project,name,args in calls if name=='restore-old-database-platform']
        self.assertEqual(len(pg),1);self.assertEqual(pg[0][0]['compose_files'],[self.base])
        self.assertEqual(pg[0][1][-1],'platform-postgres');self.assertIn('--no-deps',pg[0][1])
        # A stopped profiled job is not one of the 22 runtime services. It has
        # an explicit project default, not an invented runtime provenance row.
        calls.clear()
        driver.jobs([{'project':self.project['name'],'service':'permissions'}],'restore-permissions')
        self.assertEqual(calls[0][0]['compose_files'],[self.base,str(self.overlay)])
        self.assertEqual(calls[0][2],['run','--rm','--no-deps','--pull','never','permissions'])
        calls.clear()
        driver.jobs([{'project':self.project['name'],'service':'platform-postgres'}],'restore-permissions')
        self.assertEqual(calls[0][0]['compose_files'],[self.base], 'a runtime-targeted old job must also use its exact source')


if __name__ == '__main__': unittest.main()
