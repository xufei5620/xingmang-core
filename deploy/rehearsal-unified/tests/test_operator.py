"""Operator state-machine boundaries; these tests are not Docker rehearsal evidence."""
import copy
import datetime as dt
import importlib.util
from pathlib import Path
import tempfile
import unittest
import os
import sys
import json


SCRIPT = Path(__file__).resolve().parents[1] / "lifecycle.py"


def module(test):
    test.assertTrue(SCRIPT.is_file(), "the unified deploy/rollback operator is not implemented")
    spec = importlib.util.spec_from_file_location("unified_operator", SCRIPT)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


def good_readiness():
    return {"status": "ready", "invoice_ready": True, "modules": {
        key: {"ready": True, "status": "ready"}
        for key in ("platform", "invoice_sources", "invoice_projection")}}


def old_projects():
    def project(kind, roles):
        return {"kind": kind, "name": "synthetic-old-" + kind,
                "services": {role: {"role": role, "image_id": "sha256:" + "a" * 64} for role in roles}}
    return [project("platform", ["platform-api", "platform-worker", "platform-web", "platform-postgres"]),
            project("invoice", ["invoice-api", "invoice-web", "invoice-postgres", "pdf-scanner", "clamav", "ingest-proxy"]),
            project("sources", [kind + "-" + stream for kind in ("sub2api", "newapi")
                                for stream in ("payments", "identities", "usage", "credits", "balances")]),
            project("idp", ["keycloak", "keycloak-postgres"])]


class BoundaryTests(unittest.TestCase):
    def test_only_verified_old_exact_loopback_bindings_can_hold_takeover_ports(self):
        m = module(self)
        row = {"ports": {"80/tcp": [{"HostIp": "127.0.0.1", "HostPort": "8088"}]}}
        self.assertEqual(m.approved_loopback_ports([row], {8088, 58090}), {8088})
        for value in ([{ "ports": {"80/tcp": [{"HostIp": "0.0.0.0", "HostPort": "8088"}]}}], [row, row]):
            with self.assertRaises(m.OperatorError): m.approved_loopback_ports(value, {8088})

    def test_readiness_requires_all_modules_and_original_invoice_latch(self):
        m = module(self)
        m.require_ready(good_readiness())
        for label in ("platform", "invoice_sources", "invoice_projection", "invoice_ready"):
            with self.subTest(label=label):
                value = good_readiness()
                if label == "invoice_ready":
                    value[label] = False
                else:
                    value["modules"][label] = {"ready": False, "status": "not_evaluated"}
                with self.assertRaises(m.OperatorError):
                    m.require_ready(value)

    def test_readiness_missing_or_string_boolean_is_not_ready(self):
        m = module(self)
        for value in ({}, {"invoice_ready": True}, {**good_readiness(), "invoice_ready": "true"}):
            with self.subTest(value=value):
                with self.assertRaises(m.OperatorError):
                    m.require_ready(value)

    def test_rollback_requires_the_actual_eighteen_invoice_containers(self):
        m = module(self)
        m.validate_previous(old_projects())
        for kind, removed in (("idp", "keycloak"), ("sources", "newapi-balances"), ("invoice", "invoice-api")):
            with self.subTest(kind=kind):
                projects = old_projects()
                del next(p for p in projects if p["kind"] == kind)["services"][removed]
                with self.assertRaises(m.OperatorError):
                    m.validate_previous(projects)

    def test_old_role_cannot_be_duplicated_under_a_different_service(self):
        m = module(self)
        projects = old_projects()
        projects[1]["services"]["spare-api"] = projects[1]["services"]["invoice-api"].copy()
        with self.assertRaises(m.OperatorError):
            m.validate_previous(projects)

    def test_signed_backup_domain_anchors_are_fixed_and_distinct(self):
        m = module(self)
        self.assertEqual(m.BACKUP_ANCHORS["invoice"], ("invoice-backup", "solov-invoice-backup-v1"))
        self.assertEqual(m.BACKUP_ANCHORS["platform"], ("platform-backup", "solov-platform-backup-v1"))

    def test_approval_evidence_must_have_recent_timezone_aware_completion(self):
        m = module(self)
        self.assertTrue(hasattr(m, "require_recent_record"), "approval evidence has no freshness validation")
        now = dt.datetime(2026, 9, 12, 1, tzinfo=dt.timezone.utc)
        m.require_recent_record({"end_utc": "2026-09-12T00:30:00+00:00"}, 24, "end_utc", now=now)
        for stamp in (None, "2026-09-12T00:30:00", "2026-09-12T02:00:00+00:00", "2026-09-10T00:30:00+00:00"):
            with self.subTest(stamp=stamp):
                with self.assertRaises(m.OperatorError): m.require_recent_record({"end_utc": stamp}, 24, "end_utc", now=now)


class FakeDriver:
    def __init__(self, fail=None):
        self.fail = fail
        self.calls = []
        self.records = []

    def step(self, name):
        self.calls.append(name)
        if name == self.fail:
            raise RuntimeError("synthetic operation failed")

    def preflight(self): self.step("preflight")
    def precheck_new(self): self.step("precheck_new")
    def snapshot(self): self.step("snapshot"); return {"actual_invoice_containers": 18}
    def stop_old(self): self.step("stop_old")
    def start_new_databases(self): self.step("start_new_databases")
    def migrate_and_permissions(self): self.step("migrate_and_permissions")
    def start_new(self): self.step("start_new")
    def check_new(self): self.step("check_new")
    def switch_nginx(self, snapshot): self.step("switch_nginx")
    def smoke(self): self.step("smoke")
    def stop_new(self): self.step("stop_new")
    def restore_permissions(self): self.step("restore_permissions")
    def start_old(self): self.step("start_old")
    def check_old(self, snapshot): self.step("check_old"); assert snapshot["actual_invoice_containers"] == 18
    def restore_nginx(self, snapshot): self.step("restore_nginx")
    def record(self, value): self.records.append(copy.deepcopy(value))


class StateMachineTests(unittest.TestCase):
    def test_plaintext_pipeline_requires_both_actual_processes_to_succeed(self):
        m = module(self)
        with tempfile.TemporaryDirectory() as tmp:
            driver = object.__new__(m.DockerDriver)
            driver.env = dict(os.environ); driver.output = Path(tmp); driver.sequence = 0
            with self.assertRaises(m.OperatorError):
                driver.pipeline("producer-failure", [sys.executable, "-c", "raise SystemExit(7)"], [sys.executable, "-c", "import sys; sys.stdin.buffer.read()"])
            event = json.loads(next((Path(tmp) / "events").glob("*.json")).read_text())
            self.assertEqual(event["producer_exit_code"], 7)
            self.assertEqual(event["consumer_exit_code"], 0)
            self.assertEqual(event["exit_code"], 1)

    def test_success_orders_freeze_migrations_and_all_verification(self):
        m = module(self); driver = FakeDriver()
        result = m.cutover(driver)
        self.assertEqual(result["status"], "COMMITTED")
        self.assertEqual(driver.calls, ["preflight", "precheck_new", "snapshot", "stop_old", "start_new_databases", "migrate_and_permissions", "start_new", "check_new", "switch_nginx", "smoke"])

    def test_preflight_failure_never_stops_old_services(self):
        m = module(self); driver = FakeDriver("preflight")
        with self.assertRaises(m.OperatorError): m.cutover(driver)
        self.assertEqual(driver.calls, ["preflight"])
        self.assertNotEqual(driver.records[-1]["status"], "COMMITTED")

    def test_every_post_freeze_failure_runs_real_rollback_and_returns_failure(self):
        m = module(self)
        for failure in ("stop_old", "start_new_databases", "migrate_and_permissions", "start_new", "check_new", "switch_nginx", "smoke"):
            with self.subTest(failure=failure):
                driver = FakeDriver(failure)
                with self.assertRaises(m.OperatorError): m.cutover(driver)
                self.assertEqual(driver.calls[-5:], ["stop_new", "restore_permissions", "start_old", "check_old", "restore_nginx"])
                self.assertEqual(driver.records[-1]["status"], "ROLLED_BACK")
                self.assertEqual(driver.records[-1]["exit_code"], 1)

    def test_failed_stop_new_never_starts_old_writers_and_cannot_pass(self):
        m = module(self); driver = FakeDriver("stop_new")
        with self.assertRaises(m.OperatorError): m.rollback(driver, {"actual_invoice_containers": 18})
        self.assertNotIn("start_old", driver.calls)
        self.assertEqual(driver.records[-1]["status"], "ROLLBACK_FAILED")

    def test_old_verification_failure_is_not_success(self):
        m = module(self); driver = FakeDriver("check_old")
        with self.assertRaises(m.OperatorError): m.rollback(driver, {"actual_invoice_containers": 18})
        self.assertEqual(driver.records[-1]["status"], "ROLLBACK_FAILED")

    def test_dry_run_does_not_invoke_a_write_or_generate_pass(self):
        m = module(self); driver = FakeDriver()
        result = m.cutover(driver, dry_run=True)
        self.assertEqual(driver.calls, [])
        self.assertEqual(result["status"], "DRY_RUN")
        self.assertFalse(result["executed"])
        self.assertNotIn("COMMITTED", [r.get("status") for r in driver.records])


if __name__ == "__main__":
    unittest.main()
