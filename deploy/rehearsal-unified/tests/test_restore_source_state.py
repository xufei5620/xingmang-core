"""All ten restored native source states must verify before runtime startup."""
from contextlib import ExitStack
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import restore
import lifecycle

class SourceStateRestoreTests(unittest.TestCase):
    def test_native_ten_stream_checks_are_fixed_and_complete(self):
        self.assertTrue(hasattr(restore,'verify_frozen_source_state'))
        calls=[]
        project={'kind':'sources','services':{role:{'role':role} for role in lifecycle.STREAM_ROLES}}
        driver=SimpleNamespace(projects=lambda _:[project],compose=lambda p,name,args:calls.append(args) or subprocess.CompletedProcess(args,0))
        rows=restore.verify_frozen_source_state(driver)
        self.assertEqual({r['role'] for r in rows},lifecycle.STREAM_ROLES)
        self.assertEqual(len(calls),10)
        for argv in calls:
            self.assertEqual(argv,['run','--rm','--no-deps','--pull','never','--entrypoint','/source-agent-prod',argv[-2],'check-state'])

    def test_missing_role_and_native_failure_cannot_pass(self):
        self.assertTrue(hasattr(restore,'verify_frozen_source_state'))
        project={'kind':'sources','services':{role:{'role':role} for role in lifecycle.STREAM_ROLES}}
        calls=[]
        driver=SimpleNamespace(projects=lambda _:[project],compose=lambda *a:calls.append(a) or subprocess.CompletedProcess([],1))
        with self.assertRaises(lifecycle.OperatorError):restore.verify_frozen_source_state(driver)
        self.assertEqual(len(calls),1)
        project['services'].pop(next(iter(project['services'])))
        calls.clear()
        with self.assertRaises(lifecycle.OperatorError):restore.verify_frozen_source_state(driver)
        self.assertEqual(calls,[])

    def test_orchestrator_calls_source_verification_before_starting_collectors(self):
        self.assertTrue(hasattr(restore,'prepare_source_verification_networks'))
        # Only external resources are doubled. Run the actual orchestration and
        # the actual ten-stream verifier, including every possible failed stream.
        for failed_index in (None,*range(10),'stage','network','cleanup'):
            with self.subTest(failed_index=failed_index),tempfile.TemporaryDirectory() as tmp,ExitStack() as stack:
                root=Path(tmp);events=[];checks=[]
                project={'kind':'sources','services':{role:{'role':role} for role in lifecycle.STREAM_ROLES}}
                main={'kind':'unified','services':{'api':{'role':'platform-api'}}}
                def compose(_project,_name,args):
                    if args[0]=='create':
                        self.assertIs(_project,main)
                        self.assertEqual(args,['create','--no-build','--no-recreate','--pull','never','api'])
                        events.append('create_stopped_api')
                        return subprocess.CompletedProcess(args,1 if failed_index=='network' else 0)
                    self.assertIn('create_stopped_api',events,'external ingest network must exist before native checks')
                    events.append('check:'+args[-2]);checks.append(args)
                    return subprocess.CompletedProcess(args,1 if len(checks)-1==failed_index else 0)
                config={'mode':'local-synthetic','candidate':{'head':'a'*40,'manifest_sha256':'b'*64,'manifest':'public-manifest.json'},'backups':{},'approvals':{'max_age_hours':24}}
                driver=SimpleNamespace(config=config,state=root/'state',output=root/'run',docker=['docker'],
                    operator_source={'head':'a'*40,'files':[]},
                    artifact_preflight=lambda:None,command=lambda *a:subprocess.CompletedProcess([],0,stdout=str(16*1024**3).encode()),
                    projects=lambda _:[main,project],compose=compose,start_new_databases=lambda:events.append('databases'),jobs=lambda *a:None,
                    migrate_and_permissions=lambda:events.append('migrate'),start_new=lambda:events.append('start_new'),
                    check_new=lambda:None,smoke=lambda:None,inventory=lambda _:[])
                value={'archive_tmpfs_bytes':1024,'tools_image':'tools','verification_jobs':[{'service':'verify-invoice-restore'}]}
                stack.enter_context(patch.object(restore,'rehearsal_driver',return_value=(driver,value)))
                for name in ('invalidate_receipt','validate_frozen_mounts','create_frozen_volumes','extract_archive','inherited_preflight_pass'):
                    stack.enter_context(patch.object(restore,name,return_value=None))
                stack.enter_context(patch('preflight.run',return_value={}))
                stack.enter_context(patch.object(restore,'verify_backups',return_value={}))
                stack.enter_context(patch.object(restore,'read_public_json',return_value={'images':[{'name':'invoice-tools','imageId':'tools'}]}))
                stack.enter_context(patch.object(restore,'restore_database',side_effect=lambda _d,_v,domain:events.append('restore:'+domain)))
                stack.enter_context(patch.object(restore,'verify_snapshot_metadata',return_value={}))
                def cleanup(_):
                    events.append('cleanup')
                    if failed_index=='cleanup':raise lifecycle.OperatorError('resource cleanup failed')
                stack.enter_context(patch.object(restore,'cleanup',side_effect=cleanup))
                stack.enter_context(patch.object(restore,'cleanup_temporary_identities',side_effect=lambda *_:events.append('identity_cleanup') or {'temporary_identities_shredded':0,'original_identities_preserved':True}))
                if failed_index=='stage':
                    stack.enter_context(patch.object(restore,'stage_temporary_identities',side_effect=lifecycle.OperatorError('copy failed after cleanup was armed')))
                if failed_index is None:
                    result=restore.rehearse(driver)
                    self.assertEqual(result['status'],'PASS')
                    self.assertEqual(result['actual_operator_source'],driver.operator_source)
                    self.assertEqual(len(result['restored_source_state']),10)
                    self.assertEqual(len(checks),10)
                    self.assertLess(events.index('restore:invoice'),events.index('check:'+checks[0][-2]))
                    self.assertLess(events.index('restore:invoice'),events.index('create_stopped_api'))
                    self.assertLess(events.index('check:'+checks[-1][-2]),events.index('start_new'))
                else:
                    with self.assertRaises(lifecycle.OperatorError):restore.rehearse(driver)
                    self.assertEqual(len(checks),0 if failed_index in ('stage','network') else 10 if failed_index=='cleanup' else failed_index+1)
                    if failed_index!='cleanup':self.assertNotIn('start_new',events)
                self.assertEqual(events[-2:],['cleanup','identity_cleanup'])

if __name__=='__main__':unittest.main()
