"""P0-2: production smoke is read-only apart from revocable login sessions."""
import copy
import http.cookiejar
import json
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
sys.path.insert(0, str(Path(__file__).resolve().parent))
import smoke
import test_smoke
from test_operator import FakeDriver, module


class ProductionSmokeTests(unittest.TestCase):
    setUp = test_smoke.ExecutionTests.setUp
    execute = test_smoke.ExecutionTests.execute
    def test_real_configured_https_origins_use_normal_certificate_validation(self):
        self.config["mode"] = "production"
        self.config["origins"] = {"admin": "https://console.example.com", "user": "https://invoice.example.com"}
        self.config.pop("ca_file")
        smoke.validate_config(self.config)
        self.assertEqual(smoke.origin("https://invoice.example.com"), "https://invoice.example.com")
        with patch.object(smoke.ssl, "create_default_context", wraps=smoke.ssl.create_default_context) as context:
            smoke.Client(self.config["origins"]["admin"], None, [])
        self.assertEqual(context.call_args.kwargs.get("cafile"), None)

    def test_financial_mutations_rejected_before_transport(self):
        client = smoke.Client.__new__(smoke.Client)
        client.base, client.records, client.csrf = "https://invoice.example.com", [], "x" * 43
        with patch.object(smoke.urllib.request, "Request") as request:
            for method, path in (("POST", "/invoice-api/v1/user/invoice-requests"),
                                 ("POST", "/invoice-api/v1/admin/invoice-requests/id/review"),
                                 ("POST", "/invoice-api/v1/admin/invoice-requests/id/documents/upload"),
                                 ("DELETE", "/invoice-api/v1/user/profiles/id"),
                                 ("POST", "/invoice-api/v1/auth/logout?redirect=evil")):
                with self.subTest(method=method, path=path), self.assertRaisesRegex(smoke.SmokeFailure, "SMOKE_WRITE_FORBIDDEN"):
                    client.raw(method, path, b"{}")
            request.assert_not_called()

    def test_success_does_not_create_invoice_and_revokes_all_three_sessions(self):
        result, fixture = self.execute()
        self.assertEqual(result["status"], "PASS", result.get("failure_code"))
        forbidden = [c for c in fixture.calls if c[1] != "GET" and "/invoice-requests" in c[2]]
        self.assertEqual(forbidden, [], "smoke must never create, approve or upload invoices")
        for role, path in (("sub", "/invoice-api/v1/auth/logout"), ("new", "/invoice-api/v1/auth/logout"), ("admin", "/api/v1/auth/logout")):
            self.assertIn((role, "POST", path), fixture.calls)

    def test_failed_read_probe_still_revokes_login_sessions(self):
        result, fixture = self.execute("legacy_active")
        self.assertEqual(result["status"], "FAIL")
        for role, path in (("sub", "/invoice-api/v1/auth/logout"), ("new", "/invoice-api/v1/auth/logout"), ("admin", "/api/v1/auth/logout")):
            self.assertIn((role, "POST", path), fixture.calls)

    def test_logout_replays_original_cookie_to_prove_server_revocation(self):
        client = smoke.Client.__new__(smoke.Client)
        client.cookies = http.cookiejar.CookieJar()
        cookie = http.cookiejar.Cookie(0, "xm_session", "unit-session", None, False, "console.example.com", False, False, "/", True, True, None, True, None, None, {})
        client.cookies.set_cookie(cookie)
        calls = []
        def raw(method, path, *args, **kwargs):
            calls.append((method, [c.value for c in client.cookies]))
            if method == "POST":
                client.cookies.clear()
                return (b'{"ok":true}', "application/json", 200) if kwargs.get("with_status") else (b'{"ok":true}', "application/json")
            return b'{"authenticated":true}', "application/json"
        client.raw = raw
        with self.assertRaisesRegex(smoke.SmokeFailure, "SESSION_STILL_AUTHENTICATED"):
            client.revoke("staff")
        self.assertEqual(calls, [("POST", ["unit-session"]), ("GET", ["unit-session"])])
        self.assertEqual(list(client.cookies), [])

    def test_loopback_transport_keeps_configured_tls_hostname(self):
        target = {"address": "127.0.0.1", "port": 18443}
        for bad in ({**target, "address": "192.0.2.1"}, {**target, "port": True}, {**target, "port": 0}):
            with self.assertRaises(smoke.InputFailure):
                smoke.loopback_target(bad)
        from unittest.mock import Mock
        context = Mock()
        connection = smoke.LoopbackHTTPSConnection("console.example.com", target=smoke.loopback_target(target), context=context)
        with patch.object(smoke.socket, "create_connection") as connect:
            connection.connect()
        self.assertEqual(connect.call_args.args[0], ("127.0.0.1", 18443))
        self.assertEqual(context.wrap_socket.call_args.kwargs["server_hostname"], "console.example.com")


class CandidatePreviewTests(unittest.TestCase):
    def test_production_preview_uses_fresh_restore_not_a_previous_pass_receipt(self):
        import restore
        from test_preview_mode_binding import preview_configuration
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            config = preview_configuration(root)
            config["host_preflight"] = {"required_env_keys": {"candidate": {}}}
            config["rehearsal"]["host_preflight"] = {"required_env_keys": {"xm-rehearsal-preview": {}}}
            driver = types.SimpleNamespace(config=config, state=root / "old-state", output=root / "current-run")
            passed = {"status": "PASS", "exit_code": 0, "cleanup_complete": True, "source_head": config["candidate"]["head"], "manifest_sha256": config["candidate"]["manifest_sha256"]}
            with patch.object(restore, "DockerDriver") as constructor, patch.object(restore, "rehearse", return_value=passed) as execute:
                self.assertEqual(restore.preview_candidate(driver), passed)
                execute.assert_called_once_with(constructor.return_value)
                actual = constructor.call_args.args[0]
                self.assertEqual(actual["mode"], "server-rehearsal")
                self.assertEqual(actual["host_preflight"], config["rehearsal"]["host_preflight"])
                self.assertNotEqual(actual["state_root"], config["state_root"])
                self.assertEqual(config["mode"], "production")
                driver.output = root / "second-run"
                execute.return_value = {**passed, "cleanup_complete": False}
                with self.assertRaises(restore.OperatorError):
                    restore.preview_candidate(driver)
            config["rehearsal"]["projects"] = [{"name": config["previous"]["projects"][0]["name"]}]
            with self.assertRaises(restore.OperatorError):
                restore.preview_candidate(driver)

    def test_preview_is_really_invoked_before_old_stop(self):
        m = module(self)
        driver = FakeDriver()
        driver.precheck_new = lambda: driver.step("precheck_new")
        m.cutover(driver)
        self.assertIn("precheck_new", driver.calls)
        self.assertLess(driver.calls.index("precheck_new"), driver.calls.index("stop_old"))

    def test_preview_failure_never_stops_old_stack(self):
        m = module(self)
        driver = FakeDriver("precheck_new")
        driver.precheck_new = lambda: driver.step("precheck_new")
        with self.assertRaises(m.OperatorError):
            m.cutover(driver)
        self.assertNotIn("stop_old", driver.calls)
        self.assertNotIn("start_new", driver.calls)


if __name__ == "__main__":
    unittest.main()
