"""Audit I/O cannot prevent recovery; native or ownership errors still must."""
import errno
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch

sys.path.insert(0,str(Path(__file__).resolve().parents[1]))
import lifecycle
import restore


class RecoveryAuditTests(unittest.TestCase):
    def cutover_with_full_audit(self, root, *, failed_native=None):
        class Driver:
            def __init__(self):
                self.calls=[];self.log_full=False;self.output=root/'events-root';self.state=root/'state';self.sequence=0;self.env={}
                self.config={'candidate':{'head':'a'*40,'manifest_sha256':'b'*64},'mode':'production'}
            def preflight(self):pass
            def precheck_new(self):pass
            def snapshot(self):return {'original':'snapshot'}
            def record(self,value):return lifecycle.DockerDriver.record(self,value)
            def stop_old(self):self.calls.append('stop_old')
            def start_new_databases(self):pass
            def migrate_and_permissions(self):pass
            def start_new(self):pass
            def check_new(self):
                self.original_record=(self.state/'deployment-record.json').read_bytes()
                self.log_full=True
                raise lifecycle.OperatorError('synthetic failed new stack')
            def recover(self,name):
                self.calls.append(name)
                lifecycle.DockerDriver.command(self,name,['synthetic-'+name])
            def stop_new(self):self.recover('stop_new')
            def restore_permissions(self):self.recover('restore_permissions')
            def start_old(self):self.recover('start_old')
            def check_old(self,snapshot):self.recover('check_old')
            def restore_nginx(self,snapshot):self.recover('restore_nginx')
        driver=Driver();atomic=lifecycle.atomic_json
        def save(path,value):
            if driver.log_full:raise OSError(errno.ENOSPC,'synthetic audit full')
            return atomic(path,value)
        def native(args,**kwargs):
            return subprocess.CompletedProcess(args,1 if args==['synthetic-'+str(failed_native)] else 0,b'',b'')
        with patch.object(lifecycle,'atomic_json',side_effect=save),patch.object(lifecycle.subprocess,'run',side_effect=native):
            with self.assertRaises(lifecycle.OperatorError):lifecycle.cutover(driver)
        self.assertEqual((driver.state/'deployment-record.json').read_bytes(),driver.original_record)
        self.assertFalse(getattr(driver,'_recovering_audit',False))
        return driver

    def test_real_initial_record_and_command_IO_failures_do_not_stop_recovery(self):
        with tempfile.TemporaryDirectory() as temp:
            driver=self.cutover_with_full_audit(Path(temp))
            self.assertEqual(driver.calls[-5:],['stop_new','restore_permissions','start_old','check_old','restore_nginx'])
            self.assertTrue(driver._recovery_audit_errors)

    def test_real_native_stop_failure_still_forbids_starting_old_writers(self):
        with tempfile.TemporaryDirectory() as temp:
            driver=self.cutover_with_full_audit(Path(temp),failed_native='stop_new')
            self.assertIn('stop_new',driver.calls)
            self.assertNotIn('restore_permissions',driver.calls)
            self.assertNotIn('start_old',driver.calls)

    def test_non_recovery_command_audit_failure_remains_fatal(self):
        with tempfile.TemporaryDirectory() as temp:
            driver=object.__new__(lifecycle.DockerDriver);driver.output=Path(temp);driver.sequence=0;driver.env={}
            with patch.object(lifecycle.subprocess,'run',return_value=subprocess.CompletedProcess([],0,b'',b'')),patch.object(lifecycle,'atomic_json',side_effect=OSError(errno.ENOSPC,'audit full')):
                with self.assertRaises(OSError):driver.command('ordinary-operation',['synthetic-command'])

    def test_outer_identity_driver_finishes_both_owned_copies_despite_event_IO_failure(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);state=root/'state';output=root/'output';state.mkdir();output.mkdir();owner='a'*32
            originals={name:root/(name+'.original-empty-fixture') for name in ('platform','invoice')}
            staged={name:state/'tmpfs'/owner/(name+'.age-identity') for name in originals}
            for path in (*originals.values(),*staged.values()):path.parent.mkdir(parents=True,exist_ok=True);path.touch()
            driver=object.__new__(lifecycle.DockerDriver);driver.state=state;driver.output=output;driver.sequence=0;driver.env={}
            driver.config={'backups':{name:{'identity_file':str(path)} for name,path in originals.items()}}
            value={'owner_id':owner,'temporary_identity_paths':[str(p) for p in staged.values()],'shred_binary':sys.executable}
            (output/'temporary-identity-ownership.json').write_text(json.dumps({'owner_id':owner,'paths':[str(staged[name]) for name in lifecycle.BACKUP_ANCHORS]}))
            native=[]
            def shred(args,**kwargs):
                native.append(args);Path(args[-1]).unlink()
                return subprocess.CompletedProcess(args,0,b'',b'')
            with patch.object(lifecycle.subprocess,'run',side_effect=shred),patch.object(lifecycle,'atomic_json',side_effect=OSError(errno.ENOSPC,'synthetic audit full')):
                result=restore.cleanup_temporary_identities(driver,value)
            self.assertEqual(len(native),2)
            self.assertEqual(result['temporary_identities_shredded'],2)
            self.assertFalse(any(path.exists() for path in staged.values()))
            self.assertTrue(all(path.exists() for path in originals.values()))
            self.assertFalse(result['audit_complete'])
            self.assertTrue(result['audit_errors'])
            self.assertFalse(getattr(driver,'_recovering_audit',False))

    def test_D_cleanup_executes_owned_native_commands_but_reports_incomplete_audit(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);owner='a'*32;volume='xm-rehearsal-data';native_calls=[]
            project={'name':'xm-rehearsal-project','kind':'unified','services':{}}
            trial=object.__new__(lifecycle.DockerDriver);trial.output=root;trial.sequence=0;trial.env={};trial.docker=['docker']
            trial.config={'candidate':{'projects':[project]}};trial.verify_local_engine=lambda:None
            trial.compose=lambda p,n,a:trial.command(n,['compose',*a])
            driver=SimpleNamespace(state=root,output=root)
            value={'owner_id':owner,'volumes':{'database':volume}}
            journal={'owner_id':owner,'volumes':value['volumes'],'projects':[project['name']]}
            removed=[False]
            def native(args,**kwargs):
                native_calls.append(args)
                if args[:3]==['docker','volume','rm']:removed[0]=True
                if args[:3]==['docker','volume','inspect']:
                    if removed[0]:return subprocess.CompletedProcess(args,1,b'',b'')
                    return subprocess.CompletedProcess(args,0,json.dumps([{'Name':volume,'Labels':{'xingmang.rehearsal.owner':owner}}]).encode(),b'')
                return subprocess.CompletedProcess(args,0,b'',b'')
            full=OSError(errno.ENOSPC,'synthetic audit full')
            with patch.object(restore,'rehearsal_driver',return_value=(trial,value)),patch.object(restore,'read_public_json',return_value=journal),patch.object(restore,'verify_project_resource_owners'),patch.object(restore,'cleanup_temporary_identities',return_value={'temporary_identities_shredded':2,'original_identities_preserved':True}),patch.object(lifecycle.subprocess,'run',side_effect=native),patch.object(lifecycle,'atomic_json',side_effect=full),patch.object(restore,'atomic_json',side_effect=full):
                result=restore.cleanup(driver)
            self.assertTrue(result['cleanup_complete'])
            self.assertEqual(result['exit_code'],1)
            self.assertFalse(result['audit_complete'])
            self.assertTrue(result['audit_errors'])
            self.assertTrue(removed[0])
            self.assertFalse(getattr(trial,'_recovering_audit',False))


if __name__=='__main__':unittest.main()
