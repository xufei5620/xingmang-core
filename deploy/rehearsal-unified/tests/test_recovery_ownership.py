"""Independent-review regressions: reject drift before writes and foreign cleanup."""
import copy
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import lifecycle
import restore


class RecoveryOwnershipTests(unittest.TestCase):
    def test_file_backed_compose_secrets_are_part_of_original_mount_binding(self):
        project={"name":"old","kind":"invoice","services":{"api":{"role":"invoice-api"}}}
        resolved={"services":{"api":{"secrets":[{"source":"app","target":"app-dsn"}]}},"secrets":{"app":{"file":"/private/app-dsn"}}}
        driver=object.__new__(lifecycle.DockerDriver);driver.projects=lambda _: [project]
        driver.compose=lambda *a:subprocess.CompletedProcess([],0,json.dumps(resolved).encode(),b'')
        rows=driver.resolved_mount_inventory("previous")
        expected=[{"type":"bind","source":"/private/app-dsn","target":"/run/secrets/app-dsn","rw":False}]
        self.assertEqual(rows[0]['mounts'],expected)
        resolved['secrets']['app']={'environment':'SECRET_VALUE'}
        with self.assertRaises(lifecycle.OperatorError):driver.resolved_mount_inventory("previous")

    def test_rollback_checks_frozen_bytes_before_any_old_up(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp); file=path/"old.json"; file.write_text("changed")
            snap={"project_files":[{"env_file":str(file),"env_sha256":"0"*64,"compose":[]}],"containers":[],"ledger_hashes":{},"platform_permissions":[]}
            (path/"deployment-record.json").write_text(json.dumps({"snapshot":snap}))
            driver=object.__new__(lifecycle.DockerDriver);driver.state=path
            driver.config={"candidate":{"migration_digest":"same"},"previous":{"migration_digest":"same","permission_jobs":[]}}
            driver.verify_original_plan=lambda _: None
            driver.projects=lambda _: [{"kind":k,"services":{"postgres":{"role":r}}} for k,r in (("platform","platform-postgres"),("invoice","invoice-postgres"),("idp","keycloak-postgres"))]
            writes=[];driver.compose=lambda *args: writes.append(args);driver.jobs=lambda *args: None
            driver.ledger_snapshot=lambda _: {};driver.platform_permissions_snapshot=lambda _: []
            with self.assertRaises(lifecycle.OperatorError): driver.restore_permissions()
            self.assertEqual(writes,[])

    def test_foreign_cleanup_owner_is_rejected_before_compose_down(self):
        with tempfile.TemporaryDirectory() as tmp:
            state=Path(tmp);project={"name":"xm-rehearsal-foreign","kind":"unified"};value={"owner_id":"a"*32,"volumes":{},"temporary_identity_paths":[]}
            (state/"rehearsal-ownership.json").write_text(json.dumps({**value,"projects":[project["name"]]}))
            writes=[]
            def command(name,argv,**kwargs):
                body=json.dumps({"com.docker.compose.project":project["name"],"xingmang.rehearsal.owner":"foreign"}).encode() if 'inspect' in argv else b'foreign-id\n'
                return subprocess.CompletedProcess(argv,0,body,b'')
            trial=SimpleNamespace(docker=['docker'],projects=lambda _: [project],command=command,verify_local_engine=lambda:None,compose=lambda *args:writes.append(args))
            driver=SimpleNamespace(state=state,config={"backups":{}})
            with patch.object(restore,"rehearsal_driver",return_value=(trial,value)):
                with self.assertRaises(lifecycle.OperatorError): restore.cleanup(driver)
            self.assertEqual(writes,[])

    def test_changed_old_input_is_rejected_before_any_restart(self):
        self.assertTrue(hasattr(lifecycle, "require_snapshot_files"))
        with tempfile.TemporaryDirectory() as tmp:
            file = Path(tmp)/"old.json"; file.write_bytes(b'original')
            snap = {"project_files": [{"name":"old", "env_file":str(file), "env_sha256":hashlib.sha256(b'original').hexdigest(),
                                     "compose":[{"path":str(file),"sha256":hashlib.sha256(b'original').hexdigest()}]}]}
            lifecycle.require_snapshot_files(snap)
            file.write_bytes(b'changed')
            with self.assertRaises(lifecycle.OperatorError): lifecycle.require_snapshot_files(snap)

    def test_original_resolved_mounts_include_readonly_and_every_service(self):
        self.assertTrue(hasattr(lifecycle, "require_mount_inventory"))
        row = {"project":"old", "service":"api", "role":"invoice-api", "mounts":[{"type":"volume","source":"/original","target":"/data","rw":True}]}
        lifecycle.require_mount_inventory([row], [copy.deepcopy(row)])
        for field,value in (("source","/different"),("rw",False),("target","/other")):
            bad=copy.deepcopy(row);bad["mounts"][0][field]=value
            with self.assertRaises(lifecycle.OperatorError): lifecycle.require_mount_inventory([row],[bad])
        with self.assertRaises(lifecycle.OperatorError): lifecycle.require_mount_inventory([row],[])

    def test_existing_project_prevents_creating_any_new_volume(self):
        self.assertTrue(hasattr(restore, "assert_project_resources_absent"))
        project={"name":"xm-rehearsal-occupied", "kind":"unified"}
        calls=[]
        def command(name,argv,**kwargs):
            calls.append(argv)
            return subprocess.CompletedProcess(argv,0,b'foreign-container\n',b'')
        driver=SimpleNamespace(docker=['docker'],projects=lambda _: [project],command=command)
        with self.assertRaises(lifecycle.OperatorError): restore.assert_project_resources_absent(driver)
        self.assertTrue(calls)
        self.assertFalse(any('create' in call or 'rm' in call or 'down' in call for call in calls))

    def test_cleanup_rejects_other_container_or_network_owner(self):
        self.assertTrue(hasattr(restore, "require_resource_owner"))
        good={"com.docker.compose.project":"xm-rehearsal-owned", "xingmang.rehearsal.owner":"a"*32}
        restore.require_resource_owner(good,"xm-rehearsal-owned","a"*32)
        for bad in ({}, {**good,"xingmang.rehearsal.owner":"b"*32},{**good,"com.docker.compose.project":"other"}):
            with self.assertRaises(lifecycle.OperatorError): restore.require_resource_owner(bad,"xm-rehearsal-owned","a"*32)


if __name__ == "__main__": unittest.main()
