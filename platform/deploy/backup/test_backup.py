import importlib.util
from pathlib import Path
import unittest
import tempfile
import json
import subprocess
from unittest.mock import patch

spec=importlib.util.spec_from_file_location('platform_backup',Path(__file__).with_name('backup.py'))
backup=importlib.util.module_from_spec(spec);spec.loader.exec_module(backup)

class WriterRecoveryTests(unittest.TestCase):
    def test_partial_stop_failure_restores_only_original_running_writers(self):
        class Driver:
            before={'platform-api':{'running':True},'platform-worker':{'running':False}}
            restored=None
            def preflight(self):return self.before
            def arm(self,before):pass
            def stop_writers(self,before):raise backup.BackupError('simulated partial stop')
            def restore_writers(self,before):self.restored=before
            def record_result(self,status,**kwargs):pass
            def disarm(self):pass
        driver=Driver()
        with self.assertRaises(backup.BackupError):backup.perform_backup(driver)
        self.assertEqual(driver.restored,driver.before)

    def test_each_post_freeze_failure_restores_before_any_publication(self):
        class Driver:
            def __init__(self,fail):self.fail=fail;self.events=[];self.before={'platform-api':{'running':True},'platform-worker':{'running':False}}
            def step(self,name):
                self.events.append(name)
                if name==self.fail:raise backup.BackupError(name)
            def preflight(self):return self.before
            def arm(self,before):self.step('arm')
            def stop_writers(self,before):self.step('stop')
            def metadata(self,phase):self.step('metadata-'+phase);return {'ledger':b'same'}
            def dump_database(self):self.step('dump')
            def require_stopped(self):self.step('frozen')
            def encrypt_metadata(self,files):self.step('metadata-encrypt')
            def sign_components(self):self.step('sign')
            def restore_writers(self,before):self.events.append('restore');self.restored=before
            def disarm(self):self.events.append('disarm')
            def publish(self):self.events.append('publish');return {}
            def record_result(self,*args,**kwargs):pass
        for fail in ('stop','metadata-before','dump','metadata-after','metadata-encrypt','sign'):
            with self.subTest(fail=fail):
                driver=Driver(fail)
                with self.assertRaises(backup.BackupError):backup.perform_backup(driver)
                self.assertEqual(driver.restored,driver.before)
                self.assertNotIn('publish',driver.events)
        driver=Driver(None);backup.perform_backup(driver)
        self.assertLess(driver.events.index('restore'),driver.events.index('publish'))

    def test_actual_restore_method_does_not_start_previously_stopped_worker(self):
        driver=backup.BackupDriver.__new__(backup.BackupDriver)
        driver.config={'writers':{'platform-api':{'container_id':'api'},'platform-worker':{'container_id':'worker'}}};driver.docker=[]
        states={'platform-api':False,'platform-worker':False};started=[]
        def inspect(service,row,**kwargs):return {'running':states[service],'health':'none'}
        def command(name,args):
            service=name.removeprefix('restore-');started.append(service)
            self.assertEqual(service,'platform-api');states[service]=True
        driver.inspect=inspect;driver.command=command
        with tempfile.TemporaryDirectory() as temp:
            driver.output=Path(temp)
            driver.restore_writers({'platform-api':{'running':True,'health':'none'},'platform-worker':{'running':False,'health':'none'}})
        self.assertEqual(started,['platform-api']);self.assertFalse(states['platform-worker'])

    def test_restoration_failure_keeps_journal_and_never_publishes(self):
        class Driver:
            disarmed=False;published=False;status=None
            def preflight(self):return {}
            def arm(self,value):pass
            def stop_writers(self,value):raise backup.BackupError('failed freeze')
            def restore_writers(self,value):raise backup.BackupError('failed restore')
            def disarm(self):self.disarmed=True
            def publish(self):self.published=True
            def record_result(self,status,**kwargs):self.status=status
        driver=Driver()
        with self.assertRaisesRegex(backup.BackupError,'WRITER_RESTORATION_FAILED'):backup.perform_backup(driver)
        self.assertFalse(driver.disarmed or driver.published);self.assertEqual(driver.status,'RESTORE_FAILED')

    def test_full_evidence_disk_does_not_prevent_actual_start_attempts(self):
        driver=backup.BackupDriver.__new__(backup.BackupDriver)
        driver.config={'writers':{service:{'container_id':service} for service in backup.WRITERS}}
        driver.docker=[];driver.events=[];driver.env={};driver.journal_io_failed=False
        state={service:False for service in backup.WRITERS};started=[]
        def inspect(service,row,**kwargs):
            driver.command('inspect-'+service,['inspect',service])
            return {'running':state[service],'health':'none'}
        def execute(args,**kwargs):
            if args[0]=='start':state[args[1]]=True;started.append(args[1])
            return subprocess.CompletedProcess(args,0,b'',b'')
        driver.inspect=inspect
        with tempfile.TemporaryDirectory() as temp:
            driver.output=Path(temp)
            with patch.object(backup,'write_json',side_effect=OSError('synthetic disk full')),patch.object(backup.subprocess,'run',side_effect=execute):
                driver.restore_writers({service:{'running':True,'health':'none'} for service in backup.WRITERS})
        self.assertEqual(set(started),set(backup.WRITERS));self.assertTrue(all(state.values()));self.assertTrue(driver.journal_io_failed)

    def test_changed_active_before_does_not_override_original_snapshot(self):
        with tempfile.TemporaryDirectory() as temp:
            driver=backup.BackupDriver.__new__(backup.BackupDriver)
            driver.base=Path(temp);driver.active=driver.base/'.platform-backup-active.json';driver.config_sha='configured'
            driver.config={'mode':'local-synthetic','writers':{service:{'container_id':service,'image_id':'image'} for service in backup.WRITERS}}
            driver.start_utc=backup.utc();driver.journal_io_failed=False
            folder=driver.base/'.platform-backup-runs'/'original';folder.mkdir(parents=True)
            before={service:{'running':service=='platform-api','health':'none','container_id':service,'image_id':'image'} for service in backup.WRITERS}
            backup.write_json(folder/'writer-state-before.json',before)
            journal={'schema':'xingmang.platform-backup-active/v1','config_sha256':'configured','output':str(folder),
                'before':json.loads(json.dumps(before)),'before_file_sha256':backup.file_sha(folder/'writer-state-before.json')}
            journal['before']['platform-worker']['running']=True
            backup.write_json(driver.active,journal)
            with patch.object(driver,'restore_writers') as restore:
                with self.assertRaisesRegex(backup.BackupError,'RECOVERY_BEFORE_SNAPSHOT_CHANGED'):driver.recover()
                restore.assert_not_called()

    def test_recovery_directory_disk_full_still_attempts_original_writer_set(self):
        with tempfile.TemporaryDirectory() as temp:
            driver=backup.BackupDriver.__new__(backup.BackupDriver)
            driver.base=Path(temp);driver.active=driver.base/'.platform-backup-active.json';driver.config_sha='configured'
            driver.config={'mode':'local-synthetic','writers':{service:{'container_id':service,'image_id':'image'} for service in backup.WRITERS}}
            driver.start_utc=backup.utc();driver.journal_io_failed=False
            folder=driver.base/'.platform-backup-runs'/'original';folder.mkdir(parents=True)
            before={service:{'running':service=='platform-api','health':'none','container_id':service,'image_id':'image'} for service in backup.WRITERS}
            backup.write_json(folder/'writer-state-before.json',before)
            journal={'schema':'xingmang.platform-backup-active/v1','config_sha256':'configured','output':str(folder),
                'before':before,'before_file_sha256':backup.file_sha(folder/'writer-state-before.json')}
            backup.write_json(driver.active,journal)
            original=driver.active.read_bytes()
            with patch.object(Path,'mkdir',side_effect=OSError('synthetic disk full')),patch.object(driver,'restore_writers') as restore:
                with self.assertRaisesRegex(backup.BackupError,'RECOVERY_LOG_WRITE_FAILED'):driver.recover()
                restore.assert_called_once_with(before)
            self.assertEqual(driver.active.read_bytes(),original)

if __name__=='__main__':unittest.main()
