"""Real file/CLI and original rollback consumers; no Docker or credential fixtures."""
import copy
import contextlib
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import lifecycle


class OldInputTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.old = self.root/'old-release'; self.old.mkdir()
        self.new = self.root/'new-release'; self.new.mkdir()
        self.state = self.root/'state'; self.state.mkdir()
        self.env = self.old/'runtime.env'; self.env.write_bytes(b'PUBLIC_FIXTURE=original\r\n')
        self.projects = []
        for kind, roles in lifecycle.OLD_ROLES.items():
            directory = self.old/kind; directory.mkdir()
            compose = directory/'compose.json'
            services = {role: {'role':role,'image_id':'sha256:'+'a'*64} for role in sorted(roles)}
            compose.write_text(json.dumps({'services':{role:{'image':'sha256:'+'a'*64} for role in roles}}))
            self.projects.append({'kind':kind,'name':'old-'+kind,'compose_files':[str(compose)],
                                  'env_file':str(self.env),'services':services})
        self.config = {'mode':'local-synthetic','state_root':str(self.state),
                       'candidate':{'projects':[{'compose_files':[str(self.new/'compose.json')]}],
                                    'migration_digest':'same'},
                       'previous':{'projects':self.projects,'migration_digest':'same','permission_jobs':[{'project':'old-invoice','service':'permissions'}]}}
        self.proof = self.root/'old-inputs.json'

    def capture(self):
        self.assertTrue(hasattr(lifecycle, 'capture_old_inputs'), 'old inputs have no pre-stage preservation binding')
        descriptor = lifecycle.capture_old_inputs(self.config, [str(self.old)], str(self.new), str(self.proof))
        self.config['previous']['input_snapshot'] = descriptor
        return descriptor

    def test_capture_retains_original_bytes_and_never_parses_or_copies_environment(self):
        before = self.env.read_bytes()
        original_read = Path.read_text
        def public_only(path, *args, **kwargs):
            if path == self.env: raise AssertionError('environment must only be streamed to SHA')
            return original_read(path, *args, **kwargs)
        with patch.object(Path,'read_text',public_only): self.capture()
        self.assertEqual(self.env.read_bytes(), before)
        proof = json.loads(self.proof.read_text())
        self.assertEqual(proof['status'], 'INPUTS_CAPTURED_NOT_RUNTIME_VERIFIED')
        self.assertNotIn('PUBLIC_FIXTURE', self.proof.read_text())
        self.assertEqual(proof['projects'], self.projects)
        self.assertEqual(set(p.name for p in self.root.iterdir()), {'old-release','new-release','state','old-inputs.json'})
        with self.assertRaises(lifecycle.OperatorError):
            lifecycle.capture_old_inputs(self.config,[str(self.old)],str(self.new),str(self.proof))

    def test_new_retired_template_does_not_replace_rollback_input_or_relative_base(self):
        self.capture()
        template = self.new/'platform/deploy/compose/launch.yaml'; template.parent.mkdir(parents=True)
        template.write_text('services: {}\nretired-independent-topology: retired\n')
        driver = object.__new__(lifecycle.DockerDriver)
        driver.config=self.config; driver.state=self.state; driver.docker=['docker']; calls=[]
        driver.verify_original_plan=lambda rows: None
        driver.compose=lambda project,name,args: calls.append((project,name,args))
        driver.jobs=lambda *args: None
        driver.ledger_snapshot=lambda side:{}; driver.platform_permissions_snapshot=lambda side:[]
        snapshot={'project_files':[], 'containers':[], 'ledger_hashes':{}, 'platform_permissions':[],
                  'old_input_snapshot':self.config['previous']['input_snapshot']}
        lifecycle.atomic_json(self.state/'deployment-record.json', {'snapshot':snapshot})
        with patch.object(lifecycle,'__file__',str(self.new/'deploy/rehearsal-unified/lifecycle.py')):
            driver.restore_permissions()
            driver.start_old()
        self.assertEqual([name for _,name,_ in calls[:3]],
                         ['restore-old-database-platform','restore-old-database-invoice','restore-old-database-idp'])
        for project,_,_ in calls:
            argv=driver.compose_argv(project, [])
            self.assertEqual(Path(argv[argv.index('-f')+1]).parent, self.old/project['kind'])
            self.assertEqual(argv[argv.index('--env-file')+1], str(self.env))
        original=self.env.read_bytes(); self.env.write_bytes(b'PUBLIC_FIXTURE=drift\n'); calls.clear()
        with patch.object(lifecycle,'__file__',str(self.new/'deploy/rehearsal-unified/lifecycle.py')):
            with self.assertRaises(lifecycle.OperatorError): driver.restore_permissions()
        self.assertEqual(calls, [], 'changed old env must reject before the first old up')
        self.env.write_bytes(original)
        self.proof.write_text(self.proof.read_text()+' ')
        with self.assertRaises(lifecycle.OperatorError): lifecycle.require_snapshot_files(snapshot)

    def test_retired_old_template_and_overlapping_stage_are_rejected_without_output(self):
        self.assertTrue(hasattr(lifecycle,'capture_old_inputs'))
        for stage in (self.old, self.old/'new', self.root):
            with self.subTest(stage=str(stage)), self.assertRaises(lifecycle.OperatorError):
                lifecycle.capture_old_inputs(self.config,[str(self.old)],str(stage),str(self.proof))
            self.assertFalse(self.proof.exists())
        Path(self.projects[0]['compose_files'][0]).write_text('services: {}\nretired-independent-topology: retired\n')
        with self.assertRaises(lifecycle.OperatorError): self.capture()
        self.assertFalse(self.proof.exists())

    def test_empty_replacement_without_retirement_marker_is_not_an_original_base(self):
        self.assertTrue(hasattr(lifecycle,'capture_old_inputs'))
        base=Path(self.projects[0]['compose_files'][0])
        for body in ('{}','{"services":{}}'):
            base.write_text(body)
            with self.subTest(body=body), self.assertRaises(lifecycle.OperatorError): self.capture()
            self.assertFalse(self.proof.exists())
        base=base.with_suffix('.yaml');self.projects[0]['compose_files']=[str(base)]
        base.write_text('services: {}\n')
        with self.assertRaises(lifecycle.OperatorError): self.capture()
        self.assertFalse(self.proof.exists())

    def test_actual_capture_cli_keeps_source_bytes_and_returns_only_public_descriptor(self):
        config=self.root/'plan.json';config.write_text(json.dumps(self.config))
        command=[sys.executable,'-X','utf8','-B',str(Path(lifecycle.__file__).with_name('capture-old-inputs.py')),
                 '--config',str(config),'--retained-root',str(self.old),'--new-source-root',str(self.new),'--output',str(self.proof)]
        result=subprocess.run(command,capture_output=True)
        self.assertEqual(result.returncode,0,result.stderr.decode())
        self.assertEqual(json.loads(result.stdout)['input_snapshot']['sha256'],lifecycle.digest(self.proof))
        self.assertNotIn(b'PUBLIC_FIXTURE',result.stdout+result.stderr)
        before=self.proof.read_bytes();again=subprocess.run(command,capture_output=True)
        self.assertEqual(again.returncode,1)
        self.assertEqual(self.proof.read_bytes(),before)

    def test_preflight_checks_binding_before_external_commands(self):
        self.capture(); self.env.write_bytes(b'PUBLIC_FIXTURE=changed\n')
        calls=[]; driver=object.__new__(lifecycle.DockerDriver); driver.config=self.config
        driver.artifact_preflight=lambda:calls.append('artifact')
        driver.inventory=lambda _: (_ for _ in ()).throw(lifecycle.OperatorError('external boundary'))
        with patch.object(lifecycle,'__file__',str(self.new/'deploy/rehearsal-unified/lifecycle.py')):
            with self.assertRaises(lifecycle.OperatorError): driver.preflight()
        self.assertEqual(calls, [], 'input preservation must be checked before Docker consumption')

    def test_snapshot_cannot_rebaseline_drift_that_happens_during_inventory(self):
        self.capture();driver=object.__new__(lifecycle.DockerDriver);driver.config=self.config
        driver.ledger_snapshot=lambda _:{};driver.platform_permissions_snapshot=lambda _:[]
        def inventory(side):
            self.env.write_bytes(b'PUBLIC_FIXTURE=changed-during-inventory\n')
            return [{'role':role} for roles in lifecycle.OLD_ROLES.values() for role in roles]
        driver.inventory=inventory
        with patch.object(lifecycle,'__file__',str(self.new/'deploy/rehearsal-unified/lifecycle.py')):
            with self.assertRaises(lifecycle.OperatorError):driver.snapshot()

    def test_runtime_original_compose_labels_are_checked_against_selected_files(self):
        project=self.projects[0]; service=next(iter(project['services']))
        project=copy.deepcopy(project); project['services']={service:project['services'][service]}
        state={'project':project['name'],'service':service,'image':'sha256:'+'a'*64,
               'running':True,'exit_code':0,'health':'healthy','ports':{},'mounts':[],
               'compose_files':','.join(project['compose_files']), 'working_dir':str(self.old/'platform')}
        driver=object.__new__(lifecycle.DockerDriver); driver.config=self.config; driver.docker=['docker']
        driver.projects=lambda side:[project]
        driver.compose=lambda p,n,a:subprocess.CompletedProcess([],0,(service if '--services' in a else 'container-id').encode(),b'')
        driver.command=lambda *args:subprocess.CompletedProcess([],0,json.dumps(state).encode(),b'')
        rows=driver.inventory('previous')
        self.assertEqual(rows[0].get('compose_provenance'),
                         {'config_files':state['compose_files'],'working_dir':state['working_dir']})
        for key,value in [('compose_files',str(self.new/'empty.json')),('working_dir',str(self.new)),
                          ('compose_files',''),('working_dir',None)]:
            before=state[key];state[key]=value
            with self.subTest(key=key,value=value), self.assertRaises(lifecycle.OperatorError):
                driver.inventory('previous')
            state[key]=before


if __name__=='__main__':unittest.main()
