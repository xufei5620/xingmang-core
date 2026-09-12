"""Original orchestration consumers with deterministic external command boundaries."""
import copy
import json
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import lifecycle
import restore


def config():
    def project(kind,name,services):
        return {'kind':kind,'name':name,'services':{s:{'role':s} for s in services}}
    old=[project(k,'old-'+k,[k+'-postgres']) for k in ('platform','invoice','idp')]
    candidate=[project('unified','new-unified',['platform-api','ingest-proxy']),project('sources','new-sources',sorted(lifecycle.STREAM_ROLES))]
    jobs=[{'project':'new-unified','service':s} for s in ('migrate','invoice-migrate','invoice-permissions')]
    return {'candidate':{'projects':candidate,'jobs':jobs,'migration_digest':'same'},
            'previous':{'projects':old,'permission_jobs':[{'project':'old-invoice','service':'permissions'}],'migration_digest':'same'},'backups':{}}


class CandidateStartJobsTests(unittest.TestCase):
    def test_dry_run_describes_the_actual_nginx_switch_and_restore(self):
        cutover=lifecycle.cutover(None,dry_run=True);rollback=lifecycle.rollback(None,{},dry_run=True)
        self.assertEqual(cutover['steps'][-2:],['switch_nginx','smoke'])
        self.assertIn('restore_nginx',cutover['on_failure'])
        self.assertEqual(rollback.get('steps'),['stop_new','restore_permissions','start_old','check_old','restore_nginx'])

    def test_valid_jobs_execute_only_their_assigned_project_side(self):
        with tempfile.TemporaryDirectory() as tmp:
            driver=object.__new__(lifecycle.DockerDriver);driver.config=config();driver.state=Path(tmp);calls=[]
            driver.compose=lambda p,n,a:calls.append((p['name'],a[-1]))
            driver.migrate_and_permissions()
            driver.jobs([{'project':'new-unified','service':'verify-invoice-restore'}],'verify-frozen-snapshot')
            driver.jobs(driver.config['previous']['permission_jobs'],'restore-permissions')
            self.assertEqual(calls,[('new-unified',s) for s in ('migrate','invoice-migrate','invoice-permissions','verify-invoice-restore')]+[('old-invoice','permissions')])

    def test_sources_start_before_strict_api_health_wait(self):
        driver=object.__new__(lifecycle.DockerDriver);driver.config=config();calls=[];state={'sources':False}
        output=tempfile.TemporaryDirectory();self.addCleanup(output.cleanup);driver.output=Path(output.name)
        def compose(project,name,args):
            calls.append((project['kind'],args))
            if '--wait' in args:lifecycle.require(state['sources'],'strict API readiness needs fresh source heartbeats')
            if project['kind']=='sources':state['sources']=True
        driver.compose=compose
        try:driver.start_new()
        except lifecycle.OperatorError:self.fail('unified waited for strict source readiness before launching sources')
        self.assertEqual([kind for kind,args in calls[:2]],['unified','sources'])
        self.assertTrue(all('--wait' not in args for _,args in calls[:2]))
        self.assertEqual([kind for kind,args in calls[2:]],['unified','sources'])
        self.assertTrue(all('--wait' in args and '--no-recreate' in args for _,args in calls[2:]))

    def test_unhealthy_candidate_after_both_starts_still_fails(self):
        driver=object.__new__(lifecycle.DockerDriver);driver.config=config();started=[]
        output=tempfile.TemporaryDirectory();self.addCleanup(output.cleanup);driver.output=Path(output.name)
        def compose(project,name,args):
            if '--wait' in args:raise lifecycle.OperatorError('strict health failed')
            started.append(project['kind'])
        driver.compose=compose
        with self.assertRaises(lifecycle.OperatorError):driver.start_new()
        self.assertEqual(started,['unified','sources'])

    def test_migrations_reject_old_or_source_project_before_any_job(self):
        with tempfile.TemporaryDirectory() as tmp:
            for target in ('old-platform','new-sources'):
                for index in range(3):
                    driver=object.__new__(lifecycle.DockerDriver);driver.config=config();driver.state=Path(tmp);calls=[]
                    driver.config['candidate']['jobs'][index]['project']=target
                    driver.compose=lambda *args:calls.append(args)
                    with self.subTest(target=target,index=index),self.assertRaises(lifecycle.OperatorError):driver.migrate_and_permissions()
                    self.assertEqual(calls,[], 'validate the whole dispatch list before its first job')

    def test_frozen_job_misrouting_rejects_before_driver_or_restore_is_created(self):
        with tempfile.TemporaryDirectory() as tmp:
            base=config();projects=copy.deepcopy(base['candidate']['projects'])
            for p in projects:p['name']='xm-rehearsal-'+p['kind']
            value={'owner_id':'a'*32,'projects':projects,'jobs':[{'project':'xm-rehearsal-unified','service':r['service']} for r in base['candidate']['jobs']],
                   'ready_url':'unused','smoke_config':'unused','tools_image':'sha256:'+'a'*64,'databases':{},
                   'verification_jobs':[{'project':'xm-rehearsal-unified','service':'verify-invoice-restore'}],
                   'shred_binary':'unused','temporary_identity_paths':[],'archive_tmpfs_bytes':134217728,
                   'volumes':{k:'xm-rehearsal-'+k for k in ('platform_database','invoice_database','documents','source_state','invoice_metadata','platform_metadata')}}
            for field in ('jobs','verification_jobs'):
                current=copy.deepcopy(value);current[field][-1]['project']='old-invoice'
                driver=SimpleNamespace(config={**base,'rehearsal':current},state=Path(tmp),output=Path(tmp))
                with self.subTest(field=field),patch.object(restore,'DockerDriver',side_effect=lifecycle.OperatorError('external boundary')) as constructor:
                    with self.assertRaises(lifecycle.OperatorError):restore.rehearsal_driver(driver)
                    constructor.assert_not_called()

    def test_rollback_permission_job_cannot_run_on_candidate_before_old_database_up(self):
        with tempfile.TemporaryDirectory() as tmp:
            driver=object.__new__(lifecycle.DockerDriver);driver.config=config();driver.state=Path(tmp);calls=[]
            driver.config['previous']['permission_jobs'][0]['project']='new-unified'
            snap={'old_input_snapshot':{},'containers':[],'ledger_hashes':{},'platform_permissions':[]}
            (Path(tmp)/'deployment-record.json').write_text(json.dumps({'snapshot':snap}))
            driver.verify_original_plan=lambda *args:None;driver.compose=lambda *args:calls.append(args)
            driver.ledger_snapshot=lambda _:{};driver.platform_permissions_snapshot=lambda _:[]
            with patch.object(lifecycle,'require_old_inputs',return_value={}),patch.object(lifecycle,'require_snapshot_files'):
                with self.assertRaises(lifecycle.OperatorError):driver.restore_permissions()
            self.assertEqual(calls,[])


if __name__=='__main__':unittest.main()
