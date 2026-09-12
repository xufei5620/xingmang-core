"""Real CLI/record consumers with in-memory external deployment boundaries only."""
import contextlib
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import lifecycle


def configuration(root):
    public = root / "public.json"
    public.write_text("{}", encoding="utf-8")
    def project(kind, roles, side):
        return {"kind": kind, "name": "test-" + side + "-" + kind,
                "compose_files": [str(public)], "env_file": str(public),
                "services": {role: {"role": role, "image_id": "sha256:" + "a" * 64} for role in roles}}
    return {"schema": "xingmang.unified.operator/v1", "mode": "local-synthetic",
            "state_root": str(root / "state"),
            "docker": {"binary": str(public), "context": "test-local", "config_dir": str(root)},
            "candidate": {"head": "b" * 40, "manifest": str(public), "manifest_sha256": "c" * 64,
                          "migration_digest": "d" * 64, "databases": {}, "ready_url": "unused", "jobs": [],
                          "projects": [project("unified", {"platform-api"}, "new"),
                                       project("sources", lifecycle.STREAM_ROLES, "new")]},
            "previous": {"projects": [project(kind, roles, "old") for kind, roles in lifecycle.OLD_ROLES.items()],
                         "migration_digest": "d" * 64, "ready_urls": {}, "permission_jobs": [], "databases": {}},
            "backups": {"invoice": {}, "platform": {}}, "approvals": {},
            "smoke_config": str(public), "rehearsal": {}, "host_preflight": {}}


def deployment_boundary(events, topology):
    class Driver(lifecycle.DockerDriver):
        def verify_local_engine(self): events.append("engine")
        def preflight(self):
            events.append("preflight")
            lifecycle.require(topology["old"], "original services are stopped")
        def precheck_new(self): events.append("precheck_new")
        def snapshot(self):
            return {"containers": [{"role": role, "image_id": "sha256:" + "a" * 64}
                                   for roles in lifecycle.OLD_ROLES.values() for role in sorted(roles)]}
        def stop_old(self): events.append("stop_old"); topology["old"] = False
        def start_new_databases(self): events.append("new_databases")
        def migrate_and_permissions(self): events.append("migrate")
        def start_new(self): events.append("start_new"); topology["new"] = True
        def check_new(self): lifecycle.require(topology["new"], "new stopped")
        def smoke(self): events.append("smoke")
        def stop_new(self): events.append("stop_new"); topology["new"] = False
        def restore_permissions(self): events.append("permissions")
        def start_old(self): events.append("start_old"); topology["old"] = True
        def check_old(self, snapshot):
            lifecycle.require(topology["old"] and not topology["new"] and len(snapshot["containers"]) == 22,
                              "original topology/snapshot not restored")
    return Driver


class RepeatCutoverTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.config = configuration(self.root)
        self.config_path = self.root / "operator.json"
        self.config_path.write_text(json.dumps(self.config), encoding="utf-8")
        self.record = self.root / "state" / "deployment-record.json"
        self.events = []
        self.topology = {"old": True, "new": False}
        self.driver = deployment_boundary(self.events, self.topology)

    def invoke(self, operation):
        args = ["lifecycle.py", operation]
        if operation == "cutover": args.append(self.config["candidate"]["head"])
        args += ["--config", str(self.config_path)]
        with patch.object(lifecycle, "DockerDriver", self.driver), patch.object(sys, "argv", args), \
                contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            return lifecycle.main()

    def test_repeat_cli_preserves_committed_bytes_and_supported_rollback(self):
        self.assertEqual(self.invoke("cutover"), 0)
        committed = self.record.read_bytes()
        self.assertEqual(json.loads(committed)["status"], "COMMITTED")
        self.events.clear()
        self.assertEqual(self.invoke("cutover"), 1)
        self.assertEqual(self.record.read_bytes(), committed)
        self.assertEqual(self.events, [], "repeat must reject before any external operation")
        histories = [json.loads(p.read_text()) for p in self.record.parent.glob("cutover-*/history/*.json")]
        self.assertTrue(any(row["status"] == "CUTOVER_REJECTED" for row in histories))
        self.assertEqual(self.invoke("rollback"), 0)
        self.assertEqual(json.loads(self.record.read_bytes())["snapshot"], json.loads(committed)["snapshot"])
        self.assertEqual(self.topology, {"old": True, "new": False})
        self.assertEqual(self.invoke("cutover"), 0, "explicit completed rollback allows a fully checked new cutover")

    def test_prefreeze_failure_cannot_replace_any_durable_snapshot(self):
        self.assertEqual(self.invoke("cutover"), 0)
        committed = self.record.read_bytes()
        driver = self.driver(self.config, record_root=self.record.parent / "direct-failed-attempt")
        with self.assertRaises(lifecycle.OperatorError): lifecycle.cutover(driver)
        self.assertEqual(self.record.read_bytes(), committed)
        history = list((driver.output / "history").glob("*.json"))
        self.assertEqual(len(history), 1)
        self.assertEqual(json.loads(history[0].read_text())["status"], "PREFLIGHT_FAILED")

    def test_incomplete_or_unknown_recovery_state_rejects_before_external_calls(self):
        self.assertEqual(self.invoke("cutover"), 0)
        for status in ("PREPARED", "ROLLING_BACK", "ROLLBACK_FAILED", "UNKNOWN"):
            with self.subTest(status=status):
                value = json.loads(self.record.read_text()); value["status"] = status
                lifecycle.atomic_json(self.record, value)
                before = self.record.read_bytes(); self.events.clear()
                self.assertEqual(self.invoke("cutover"), 1)
                self.assertEqual(self.record.read_bytes(), before)
                self.assertEqual(self.events, [])


if __name__ == "__main__": unittest.main()
