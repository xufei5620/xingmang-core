"""N-1: only the verified frozen-copy path can exercise invoice writes."""
from contextlib import ExitStack
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import copy
import hashlib
import json

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import restore
import smoke
import preview_smoke
import test_smoke
import lifecycle


class OrchestrationTests(unittest.TestCase):
    def test_rehearsal_runs_write_path_after_restore_and_before_cleanup(self):
        with tempfile.TemporaryDirectory() as tmp, ExitStack() as stack:
            root = Path(tmp); events = []
            config = {'mode':'local-synthetic', 'candidate':{'head':'a'*40,
                'manifest_sha256':'b'*64, 'manifest':'public-manifest.json'},
                'backups':{}, 'approvals':{'max_age_hours':24}}
            driver = SimpleNamespace(config=config, state=root/'state', output=root/'run',
                docker=['docker'], operator_source={'head':'a'*40,'files':[]},
                artifact_preflight=lambda:None,
                command=lambda *a:subprocess.CompletedProcess([],0,stdout=str(16*1024**3).encode()),
                start_new_databases=lambda:events.append('databases'), jobs=lambda *a:None,
                migrate_and_permissions=lambda:events.append('migrate'),
                start_new=lambda:events.append('start'), check_new=lambda:events.append('ready'),
                smoke=lambda:events.append('readonly'),
                preview_smoke=lambda *a:events.append('write-preview'), inventory=lambda _:[])
            value={'archive_tmpfs_bytes':1024,'tools_image':'tools',
                'verification_jobs':[{'service':'verify-invoice-restore'}]}
            stack.enter_context(patch.object(restore,'rehearsal_driver',return_value=(driver,value)))
            for name in ('invalidate_receipt','validate_frozen_mounts','create_frozen_volumes',
                         'extract_archive','inherited_preflight_pass','stage_temporary_identities',
                         'prepare_source_verification_networks'):
                stack.enter_context(patch.object(restore,name,return_value=None))
            stack.enter_context(patch('preflight.run',return_value={}))
            stack.enter_context(patch.object(restore,'verify_backups',return_value={}))
            stack.enter_context(patch.object(restore,'read_public_json',return_value={'images':[{'name':'invoice-tools','imageId':'tools'}]}))
            stack.enter_context(patch.object(restore,'restore_database',side_effect=lambda *_:events.append('restore')))
            stack.enter_context(patch.object(restore,'verify_snapshot_metadata',return_value={}))
            stack.enter_context(patch.object(restore,'verify_frozen_source_state',return_value=[]))
            stack.enter_context(patch.object(restore,'cleanup',side_effect=lambda *_:events.append('cleanup')))
            stack.enter_context(patch.object(restore,'cleanup_temporary_identities',return_value={'temporary_identities_shredded':0,'original_identities_preserved':True}))
            result=restore.rehearse(driver)
            self.assertEqual(result['status'],'PASS')
            self.assertIn('write-preview',events,'a readonly smoke cannot substitute for the required invoice write round trip')
            self.assertLess(events.index('ready'),events.index('write-preview'))
            self.assertLess(events.index('write-preview'),events.index('cleanup'))
            self.assertNotIn('readonly',events)


class WriteFixture(test_smoke.ProtocolFixture):
    class Client(test_smoke.ProtocolFixture.Client):
        def call(self, method, path, payload=None, expected=(200,)):
            f = self.fixture
            if method == 'POST' and path == '/invoice-api/v1/user/invoice-requests':
                f.calls.append((self.role,method,path))
                f.request={'id':test_smoke.REQUEST,'source_instance_id':test_smoke.SOURCE,
                    'source_type':'sub2api','amount_minor':20000,'status':'pending_review','version':1,
                    'allocations':[{'funding_lot_id':test_smoke.LOT,'amount_minor':20000}]}
                return copy.deepcopy(f.request)
            transitions={'/review':'approved','/begin-manual-issue':'manual_issuing','/confirm-manual-issue':'issued_awaiting_document'}
            suffix=next((s for s in transitions if path.endswith(s)),None)
            if method=='POST' and suffix:
                f.calls.append((self.role,method,path))
                if f.fault!='stale_version': f.request['version']+=1
                f.request.update(status=transitions[suffix],updated_at='2026-09-12T01:02:03.123456Z')
                return copy.deepcopy(f.request)
            return super().call(method,path,payload,expected)

        def raw(self, method, path, payload=None, expected=(200,), content_type='application/json'):
            f=self.fixture
            if path.endswith('/documents/upload'):
                f.calls.append((self.role,method,path))
                pdf=preview_smoke.synthetic_pdf()
                assert pdf in payload and 'multipart/form-data' in content_type
                f.request['version']+=1;f.request['status']='issued'
                return json.dumps({'request':f.request,'document':{'id':test_smoke.DOC,
                    'request_id':test_smoke.REQUEST,'scan_status':'infected' if f.fault=='scanner' else 'clean',
                    'sha256':hashlib.sha256(pdf).hexdigest()}}).encode(),'application/json'
            if path.endswith('/document'):
                f.calls.append((self.role,method,path))
                body=preview_smoke.synthetic_pdf()
                if f.fault=='download':body+=b'changed'
                return body,'application/pdf'
            return super().raw(method,path,payload,expected,content_type)


class PreviewContractTests(unittest.TestCase):
    setUp=test_smoke.ExecutionTests.setUp

    def execute_preview(self, fault=None):
        fixture=WriteFixture(fault)
        permit=preview_smoke.FrozenPermit(preview_smoke._CAPABILITY,self.config,{'unit_fixture':True})
        with patch.object(preview_smoke,'PreviewClient',side_effect=lambda *a,**kw:fixture.factory(*a)):
            result=smoke.run(self.config,_preview=permit)
        return result,fixture

    def test_round_trip_has_real_transitions_scanning_and_both_hash_checked_downloads(self):
        result,fixture=self.execute_preview()
        self.assertEqual(result['status'],'PASS',result.get('failure_code'))
        self.assertEqual(tuple(s['name'] for s in result['steps']),preview_smoke.REQUIRED)
        self.assertTrue(result['financial_writes_permitted'])
        self.assertEqual(result['evidence_kind'],'actual-frozen-preview-smoke')
        for suffix in ('/review','/begin-manual-issue','/confirm-manual-issue','/documents/upload'):
            self.assertTrue(any(role=='admin' and method=='POST' and path.endswith(suffix) for role,method,path in fixture.calls))
        downloads=[role for role,method,path in fixture.calls if method=='GET' and path.endswith('/document') and role!='new']
        self.assertEqual(downloads,['sub','admin'])
        self.assertEqual(result['steps'][-1]['name'],'sessions.revoked')

    def test_bad_scans_hashes_transitions_or_wallet_cannot_pass_and_sessions_revoke(self):
        for failure in ('scanner','download','stale_version','subscription_only','unconsumed_wallet','inflated_wallet_available'):
            with self.subTest(failure=failure):
                result,fixture=self.execute_preview(failure)
                self.assertEqual(result['status'],'FAIL')
                self.assertEqual(result['steps'][-1]['name'],'sessions.revoked')
                self.assertEqual(sum(path.endswith('/logout') for _,_,path in fixture.calls),3)

    def test_production_cannot_mint_permission_and_config_changes_invalidate_it(self):
        with self.assertRaisesRegex(smoke.SmokeFailure,'VERIFIED_FROZEN'):
            preview_smoke.FrozenPermit(object(),self.config,{})
        production=copy.deepcopy(self.config);production['mode']='production'
        with self.assertRaisesRegex(smoke.SmokeFailure,'PRODUCTION_WRITES_FORBIDDEN'):
            preview_smoke.FrozenPermit(preview_smoke._CAPABILITY,production,{})
        permit=preview_smoke.FrozenPermit(preview_smoke._CAPABILITY,self.config,{})
        with self.assertRaisesRegex(smoke.SmokeFailure,'PREVIEW_CONFIGURATION_CHANGED'):permit.check_config(production)

    def test_permission_only_targets_the_request_created_in_this_preview(self):
        permit=preview_smoke.FrozenPermit(preview_smoke._CAPABILITY,self.config,{})
        self.assertTrue(permit.allowed('POST','/invoice-api/v1/user/invoice-requests'))
        permit.request_id=test_smoke.REQUEST
        self.assertFalse(permit.allowed('POST','/invoice-api/v1/user/invoice-requests'))
        prefix='/invoice-api/v1/admin/invoice-requests/'+test_smoke.REQUEST
        self.assertTrue(permit.allowed('POST',prefix+'/review'))
        for method,path in [('DELETE',prefix),('POST',prefix+'/review?x=1'),('POST',prefix.replace(test_smoke.REQUEST,test_smoke.LOT)+'/review')]:
            self.assertFalse(permit.allowed(method,path))

    def test_switch_smoke_ignores_attempted_write_settings(self):
        self.config.update(preview=True,financial_writes_permitted=True)
        fixture=WriteFixture()
        with patch.object(smoke,'Client',fixture.factory):result=smoke.run(self.config)
        self.assertEqual(result['status'],'PASS')
        self.assertFalse(result['financial_writes_permitted'])
        self.assertFalse(any(method=='POST' and '/invoice-requests' in path for _,method,path in fixture.calls))


class FrozenGuardTests(unittest.TestCase):
    setUp=test_smoke.ExecutionTests.setUp

    def guards(self, stack):
        self.config['connect_to']={k:{'address':'127.0.0.1','port':p} for k,p in [('admin',19444),('user',19443)]}
        projects=[{'name':'xm-rehearsal-frozen-'+k,'kind':k} for k in ('unified','sources')]
        cfg={'mode':'local-synthetic','candidate':{'projects':projects,'ready_url':'http://127.0.0.1:19000/readyz','head':'a'*40,'manifest_sha256':'b'*64},
            'rehearsal':{'projects':projects,'ready_url':'http://127.0.0.1:19000/readyz','smoke_config':'preview.json',
                'volumes':{'database':'xm-rehearsal-frozen-db'},'owner_id':'1'*32},'smoke_config':'preview.json'}
        deployment={'candidate':{'projects':[{'name':'candidate'}]},'previous':{'projects':[{'name':'old'}]},'smoke_config':'live.json'}
        driver=SimpleNamespace(config=cfg,artifact_preflight=lambda:None,inventory=lambda _: [{}]*18)
        live={'origins':{'admin':'https://console.example.com','user':'https://invoice.example.com'}}
        stack.enter_context(patch.object(preview_smoke,'read_public_json',side_effect=lambda p:self.config if p=='preview.json' else live))
        doubles={name:stack.enter_context(patch.object(restore,name,return_value={})) for name in
                 ('validate_frozen_mounts','volume_metadata','require_owned_volume','verify_project_resource_owners')}
        return driver,deployment,doubles

    def test_actual_resource_guards_are_mandatory_before_issuing_capability(self):
        with ExitStack() as stack:
            driver,live,doubles=self.guards(stack)
            permit,config=preview_smoke.verified_permit(driver,live)
            self.assertEqual(permit.proof['runtime_containers'],18)
            self.assertIs(config,self.config)
            for mock in doubles.values():mock.assert_called_once()
        for name in ('validate_frozen_mounts','require_owned_volume','verify_project_resource_owners'):
            with self.subTest(name=name),ExitStack() as stack:
                driver,live,doubles=self.guards(stack)
                doubles[name].side_effect=lifecycle.OperatorError('unowned or writable original input')
                with self.assertRaises(lifecycle.OperatorError):preview_smoke.verified_permit(driver,live)

    def test_production_original_projects_or_live_ports_cannot_get_write_permission(self):
        for bad in ('production','project','endpoint','missing_runtime'):
            with self.subTest(bad=bad),ExitStack() as stack:
                driver,live,_=self.guards(stack)
                if bad=='production':driver.config['mode']='production'
                if bad=='project':live['candidate']['projects']=[{'name':driver.config['candidate']['projects'][0]['name']}]
                if bad=='endpoint':self.config['connect_to']['admin']['port']=443
                if bad=='missing_runtime':driver.inventory=lambda _:[]
                with self.assertRaises(smoke.SmokeFailure):preview_smoke.verified_permit(driver,live)


if __name__ == '__main__': unittest.main()
