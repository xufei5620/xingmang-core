import copy
import datetime as dt
import hashlib
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import sys
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import smoke


SOURCE = "10000000-0000-0000-0000-000000000001"
LOT = "20000000-0000-0000-0000-000000000001"
USER = "30000000-0000-0000-0000-000000000001"
REQUEST = "40000000-0000-0000-0000-000000000001"
NEW_SOURCE = "10000000-0000-0000-0000-000000000002"
NEW_LOT = "20000000-0000-0000-0000-000000000002"
NEW_USER = "30000000-0000-0000-0000-000000000002"
DOC = "50000000-0000-0000-0000-000000000001"


class ContractTests(unittest.TestCase):
    def test_ready_requires_every_module_and_invoice_verdict_true(self):
        base = {"invoice_ready": True, "modules": {k: {"ready": True} for k in ("platform", "invoice_sources", "invoice_projection")}}
        smoke.check_ready(base)
        for key in base["modules"]:
            for bad in (False, "true", 1, None):
                value = copy.deepcopy(base)
                value["modules"][key]["ready"] = bad
                with self.subTest(key=key, bad=bad), self.assertRaises(smoke.SmokeFailure):
                    smoke.check_ready(value)
        for bad in (False, "true", 1, None):
            with self.subTest(invoice_ready=bad), self.assertRaises(smoke.SmokeFailure):
                smoke.check_ready({**base, "invoice_ready": bad})

    def test_source_isolation_cannot_pass_empty_or_other_source(self):
        valid = [{"id": LOT, "source_instance_id": SOURCE, "source": "sub2api"}]
        smoke.check_scope(valid, SOURCE, "sub2api")
        for value in ([], [{**valid[0], "source_instance_id": "other"}], [{**valid[0], "source": "newapi"}], valid * 2):
            with self.subTest(value=value), self.assertRaises(smoke.SmokeFailure):
                smoke.check_scope(value, SOURCE, "sub2api")

    def test_existing_profile_requires_owner_verified_and_exact_receiver(self):
        base = {"id": LOT, "principal_id": USER, "email_verified": True, "email": "synthetic@example.test"}
        smoke.check_profile(base, USER, "synthetic@example.test")
        for delta in ({"principal_id": "other"}, {"email_verified": False}, {"email_verified": "true"}, {"email": "other@example.test"}):
            with self.subTest(delta=delta), self.assertRaises(smoke.SmokeFailure):
                smoke.check_profile({**base, **delta}, USER, "synthetic@example.test")

    def test_submitted_request_preserves_exact_amount_source_allocation_and_version(self):
        base = {"id": REQUEST, "source_instance_id": SOURCE, "source_type": "sub2api", "amount_minor": 20000, "status": "pending_review", "version": 2, "allocations": [{"funding_lot_id": LOT, "amount_minor": 20000}]}
        smoke.check_request(base, SOURCE, 20000, LOT, "pending_review", 1)
        for delta in ({"id": "bad"}, {"source_instance_id": "other"}, {"source_type": "newapi"}, {"amount_minor": 19999}, {"version": 1}, {"version": True}, {"status": "issued"}, {"allocations": []}, {"allocations": [{"funding_lot_id": LOT, "amount_minor": 1}]}):
            with self.subTest(delta=delta), self.assertRaises(smoke.SmokeFailure):
                smoke.check_request({**base, **delta}, SOURCE, 20000, LOT, "pending_review", 1)

    def test_staff_requires_actual_admin_session_fresh_mfa_and_csrf(self):
        base = {"authenticated": True, "admin_step_up_required": False, "csrf_token": "x" * 43, "user": {"id": USER, "role": "admin"}}
        smoke.check_staff(base)
        for delta in ({"authenticated": False}, {"authenticated": "true"}, {"admin_step_up_required": True}, {"admin_step_up_required": 0}, {"csrf_token": ""}, {"user": {"id": USER, "role": "user"}}):
            with self.subTest(delta=delta), self.assertRaises(smoke.SmokeFailure):
                smoke.check_staff({**base, **delta})

    def test_download_requires_exact_pdf_bytes(self):
        pdf = b"%PDF-1.4\nsynthetic\n%%EOF\n"
        smoke.check_download(pdf, pdf)
        for bad in (b"", b"<html>login</html>", pdf + b"changed"):
            with self.subTest(length=len(bad)), self.assertRaises(smoke.SmokeFailure):
                smoke.check_download(bad, pdf)

    def test_totp_rfc_vector_not_recovery_code(self):
        self.assertEqual(smoke.totp("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", 59), "287082")

    def test_origins_cannot_send_credentials_outside_loopback_or_over_http(self):
        self.assertEqual(smoke.origin("https://localhost:18443"), "https://localhost:18443")
        for value in ("http://localhost:18443", "https://api.example:443", "https://localhost.example:443", "https://user:pass@localhost:443", "https://localhost:443/path", "https://127.0.0.1:443?x=1", "https://localhost:443/#fragment", "https://localhost", "https://localhost:443\n"):
            with self.subTest(value=value), self.assertRaises(smoke.InputFailure):
                smoke.origin(value)

    def test_duplicate_json_keys_are_rejected(self):
        with self.assertRaises(smoke.SmokeFailure):
            smoke.json_object(b'{"invoice_ready":false,"invoice_ready":true}')

    def test_transport_checks_actual_http_status_and_does_not_log_payload(self):
        class Response(io.BytesIO):
            code = 200
            headers = {"Content-Type": "application/json"}
        class Opener:
            def open(self, request, timeout):
                self.request = request
                return Response(b'{"ok":true}')
        client = smoke.Client.__new__(smoke.Client)
        client.base, client.csrf, client.records, client.opener = "https://localhost:443", "x" * 43, [], Opener()
        with self.assertRaises(smoke.SmokeFailure):
            client.denied("POST", "/invoice-api/v1/auth/console-assertion", {"password": "synthetic-private-value"}, (404,))
        self.assertEqual(client.records[0]["status"], 200)
        self.assertNotIn("synthetic-private-value", json.dumps(client.records))
        headers = dict((key.lower(), value) for key, value in client.opener.request.header_items())
        self.assertEqual(headers["origin"], client.base)
        self.assertEqual(headers["x-csrf-token"], "x" * 43)
        self.assertEqual(headers["x-requested-with"], "xingmang")

    def test_redirects_never_forward_credentials(self):
        with self.assertRaises(smoke.SmokeFailure):
            smoke.NoRedirect().redirect_request(None, None, 302, "", {}, "https://outside.invalid/")


class ProtocolFixture:
    """In-memory HTTP-boundary unit fixture, never deployment evidence."""
    def __init__(self, fault=None):
        self.fault = fault
        self.clients = []
        self.calls = []
        self.version = 0
        self.request = None
        self.ready_calls = 0

    def factory(self, base, ca_file, records):
        client = self.Client(self, ("admin", "sub", "new")[len(self.clients)])
        self.clients.append(client)
        return client

    class Client:
        def __init__(self, fixture, role):
            self.fixture, self.role, self.csrf = fixture, role, ""

        def denied(self, method, path, payload=None, expected=(401, 403, 404)):
            self.fixture.calls.append((self.role, method, path))
            retired = (method, path) in smoke.OLD_ROUTES
            status = 404 if retired else 403
            if self.fixture.fault == "legacy_active" and retired:
                status = 200
            if self.fixture.fault == "cross_access" and self.role == "new" and "/invoice-requests/" in path:
                status = 200
            if self.fixture.fault == "customer_admin" and self.role == "sub" and "/admin/" in path:
                status = 200
            smoke.require(status in expected, "HTTP_STATUS_UNEXPECTED")

        def call(self, method, path, payload=None, expected=(200,)):
            f = self.fixture
            f.calls.append((self.role, method, path))
            if path == "/readyz":
                f.ready_calls += 1
                if f.fault == "interrupt_final" and f.ready_calls == 2:
                    raise KeyboardInterrupt("synthetic interruption")
                ready = {"invoice_ready": True, "modules": {k: {"ready": True} for k in ("platform", "invoice_sources", "invoice_projection")}}
                if f.fault == "projection_down":
                    ready["modules"]["invoice_projection"]["ready"] = False
                if f.fault == "final_not_ready" and f.ready_calls == 2:
                    ready["invoice_ready"] = False
                return ready
            if path == "/invoice-api/v1/auth/platform-login":
                return {"ok": f.fault != "source_login_pending"}
            if path == "/invoice-api/v1/auth/session":
                user = USER if self.role == "sub" else NEW_USER
                if f.fault == "same_principal":
                    user = USER
                return {"authenticated": True, "csrf_token": "u" * 43, "user": {"id": user, "role": "user", "platform": "sub2api" if self.role == "sub" else "newapi"}}
            if path == "/invoice-api/v1/user/funding-lots":
                is_sub = self.role == "sub"
                lot = {"id": LOT if is_sub else NEW_LOT, "source_instance_id": SOURCE if is_sub else NEW_SOURCE, "source": "sub2api" if is_sub else "newapi", "eligibility_kind": "wallet", "verification": "verified", "refund_frozen": False, "eligibility_status": "active", "consumed_cash_minor": 100000, "available_minor": 100000}
                if f.fault == "source_crossed" and not is_sub:
                    lot["source_instance_id"] = SOURCE
                if f.fault == "empty_new" and not is_sub:
                    return {"items": []}
                if f.fault == "subscription_only":
                    lot["eligibility_kind"] = "subscription"
                if f.fault == "unconsumed_wallet":
                    lot["consumed_cash_minor"] = 0
                if f.fault == "inflated_wallet_available":
                    lot["consumed_cash_minor"] = 19999
                return {"items": [lot]}
            if path == "/invoice-api/v1/user/profiles":
                return {"items": [{"id": LOT, "principal_id": USER, "email_verified": f.fault != "unverified_profile", "email": "synthetic@example.test"}]}
            if path == "/api/v1/auth/login":
                return {"requires_totp": f.fault != "totp_bypassed", "temp_token": "private-test-token"}
            if path == "/api/v1/auth/login/totp":
                return {"requires_totp": False, "totp_enrolled": True, "must_change_password": False, "must_enroll_totp": False, "username": "fixture-staff", "roles": ["admin"]}
            if path == "/invoice-api/v1/auth/staff-session":
                return {"authenticated": True, "admin_step_up_required": f.fault == "mfa_stale", "csrf_token": "s" * 43, "user": {"id": USER, "role": "admin"}}
            if path == "/invoice-api/v1/admin/invoice-requests":
                return {"items": []}
            if path == "/invoice-api/v1/user/invoice-requests":
                f.request = {"id": REQUEST, "source_instance_id": SOURCE, "source_type": "sub2api", "amount_minor": 20000, "status": "pending_review", "version": 1, "allocations": [{"funding_lot_id": LOT, "amount_minor": 20000}], "updated_at": "2026-09-12T00:00:00.123456Z"}
                if f.fault == "amount_shrunk":
                    f.request["amount_minor"] = 19999
                return copy.deepcopy(f.request)
            suffix = path.rsplit("/", 1)[-1]
            states = {"review": "approved", "begin-manual-issue": "manual_issuing", "confirm-manual-issue": "issued_awaiting_document"}
            if suffix in states:
                f.request["status"] = states[suffix]
                if f.fault != "version_stale":
                    f.request["version"] += 1
                return copy.deepcopy(f.request)
            raise AssertionError("unexpected protocol operation")

        def raw(self, method, path, payload=None, expected=(200,), content_type="application/json"):
            f = self.fixture
            f.calls.append((self.role, method, path))
            if path.startswith("/finance"):
                return b"<html>local management shell</html>", "text/html"
            if path.endswith("/documents/upload"):
                f.request.update(status="issued", version=f.request["version"] + 1)
                document = {"id": DOC, "request_id": REQUEST, "scan_status": "clean", "sha256": hashlib.sha256(smoke.synthetic_pdf()).hexdigest()}
                if f.fault == "scan_failed":
                    document["scan_status"] = "infected"
                return json.dumps({"request": f.request, "document": document}).encode(), "application/json"
            if path.endswith("/document"):
                pdf = smoke.synthetic_pdf()
                if f.fault == "download_changed":
                    pdf += b"changed"
                return pdf, "application/pdf"
            raise AssertionError("unexpected raw operation")


class ExecutionTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        root = Path(self.temp.name)
        def sample(name, value):
            p = root / name
            p.write_text(value, encoding="utf-8")
            p.chmod(0o600)
            return str(p)
        password = sample("password", "synthetic-test-password")
        ca = sample("public-ca.pem", "placeholder for mocked transport")
        self.config = {"schema": smoke.SCHEMA, "mode": "local-synthetic", "origins": {"admin": "https://localhost:18443", "user": "https://localhost:18444"}, "ca_file": ca,
            "credentials": {"staff": {"username": "fixture-staff", "password_file": password, "totp_file": sample("totp", "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")}, "sub2api": {"identifier": "synthetic@example.test", "password_file": password, "source_id": SOURCE, "existing_profile_id": LOT, "expected_email_file": sample("email", "synthetic@example.test")}, "newapi": {"identifier": "new-fixture", "password_file": password, "source_id": NEW_SOURCE}}, "expected": {"request_amount_minor": 20000, "minimum_available_minor": 20000}}

    def execute(self, fault=None):
        fixture = ProtocolFixture(fault)
        with patch.object(smoke, "Client", fixture.factory):
            result = smoke.run(self.config)
        return result, fixture

    def test_protocol_success_runs_all_required_operations(self):
        result, fixture = self.execute()
        self.assertEqual(result["status"], "PASS", result.get("failure_code"))
        self.assertEqual(result["exit_code"], 0)
        self.assertEqual(tuple(x["name"] for x in result["steps"]), smoke.REQUIRED)
        self.assertEqual(result["document_sha256"], hashlib.sha256(smoke.synthetic_pdf()).hexdigest())
        for operation in (("admin", "POST", "/api/v1/auth/login/totp"), ("sub", "POST", "/invoice-api/v1/user/invoice-requests"), ("admin", "POST", "/invoice-api/v1/admin/invoice-requests/" + REQUEST + "/documents/upload")):
            self.assertIn(operation, fixture.calls)
        serialized = json.dumps(result)
        for private in ("synthetic-test-password", "private-test-token", "synthetic@example.test", "GEZDGNBV"):
            self.assertNotIn(private, serialized)

    def test_each_critical_failed_boundary_prevents_pass(self):
        for fault in ("projection_down", "source_login_pending", "same_principal", "source_crossed", "empty_new", "subscription_only", "unconsumed_wallet", "inflated_wallet_available", "unverified_profile", "totp_bypassed", "mfa_stale", "legacy_active", "cross_access", "customer_admin", "amount_shrunk", "version_stale", "scan_failed", "download_changed", "final_not_ready"):
            with self.subTest(fault=fault):
                result, _ = self.execute(fault)
                self.assertEqual(result["status"], "FAIL")
                self.assertEqual(result["exit_code"], 1)
                self.assertTrue(result["failure_code"])

    def test_missing_credentials_fail_before_http_with_input_exit(self):
        self.config["credentials"]["staff"]["password_file"] += ".missing"
        with patch.object(smoke, "Client") as client:
            result = smoke.run(self.config)
        self.assertEqual((result["status"], result["exit_code"]), ("FAIL", 2))
        client.assert_not_called()

    def test_interruption_after_business_steps_cannot_emit_success(self):
        fixture = ProtocolFixture("interrupt_final")
        with patch.object(smoke, "Client", fixture.factory), self.assertRaises(KeyboardInterrupt) as caught:
            smoke.run(self.config)
        result = caught.exception.smoke_result
        self.assertEqual((result["status"], result["exit_code"]), ("FAIL", 1))
        self.assertEqual(result["steps"][-1]["status"], "FAIL")

    def test_cli_failure_output_overwrites_no_pass_and_contains_no_private_data(self):
        config_path = Path(self.temp.name) / "config.json"
        output = Path(self.temp.name) / "smoke.json"
        config_path.write_text(json.dumps(self.config), encoding="utf-8")
        output.write_text('{"status":"PASS","exit_code":0}', encoding="utf-8")
        fixture = ProtocolFixture("interrupt_final")
        with patch.object(smoke, "Client", fixture.factory), self.assertRaises(KeyboardInterrupt):
            smoke.main(["--config", str(config_path), "--output", str(output)])
        result = json.loads(output.read_text("utf-8"))
        self.assertEqual((result["status"], result["exit_code"]), ("FAIL", 1))

    def test_configuration_cannot_remove_fixed_retired_route_checks(self):
        self.config["expected"]["old_routes"] = []
        result, fixture = self.execute("legacy_active")
        self.assertEqual(result["status"], "FAIL")
        self.assertIn(("admin", *smoke.OLD_ROUTES[0]), fixture.calls)


if __name__ == "__main__":
    unittest.main()
