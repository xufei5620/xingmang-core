"""Exercise the real resolved-Compose preflight, without starting Docker."""
import copy
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import preflight


class RolePolicyPreflightTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.record = Path(self.temp.name) / "mfa.json"
        self.roles = {"invoice-reviewer": ["finance.read"], "staff": ["registry.read"]}
        self.proof = {
            "admin_role": "invoice-reviewer", "role_scope_map": self.roles,
            "admin_role_total": 2, "invoice_admin_total": 2,
            "invoice_admin_totp_registered": 2, "invoice_admin_login_ready_total": 2,
        }
        self.env = {"ADMIN_ROLE": "invoice-reviewer", "XM_AUTH_ROLE_SCOPES": json.dumps(self.roles)}
        self.snapshot = Path(self.temp.name) / "platform.json"
        person = {"roles": ["invoice-reviewer"], "finance_read": True, "totp_registered": True,
                  "disabled": False, "locked": False, "must_change_password": False, "must_enroll_totp": False}
        self.rows = {"schema": "xingmang.identity-audit.platform/v2", "database": "test", "transaction_read_only": "on",
                     **copy.deepcopy(self.proof), "finance_read_total": 2, "finance_read_totp_registered": 2,
                     "finance_read_enabled_total": 2, "finance_read_enabled_totp_registered": 2,
                     "staff": [{"id": str(i), **copy.deepcopy(person)} for i in range(2)]}
        self.config = {"candidate": {"projects": [{"kind": "unified", "name": "test-unified"}]},
                       "approvals": {"mfa_query_record": str(self.record)}}
        self.host = {"required_env_keys": {"test-unified": {"platform-api": ["ADMIN_ROLE"]}}}
        self.calls = []

    def run_preflight(self):
        self.snapshot.write_text(json.dumps(self.rows), encoding="utf-8")
        self.record.write_text(json.dumps({"role_policy": self.proof, "platform_snapshot": str(self.snapshot),
            "platform_snapshot_sha256": hashlib.sha256(self.snapshot.read_bytes()).hexdigest()}), encoding="utf-8")
        owner = self
        class Driver:
            def compose(self, project, name, argv):
                owner.calls.append(argv)
                return subprocess.CompletedProcess(argv, 0, json.dumps({"services": {
                    "platform-api": {"environment": owner.env}}}).encode(), b"")
        return preflight.environment(Driver(), self.config, self.host)

    def test_exact_explicit_role_and_scope_are_accepted(self):
        self.run_preflight()
        self.assertEqual(self.calls, [["--profile", "*", "config", "--format", "json"]])

    def test_missing_blank_or_scope_only_role_fails(self):
        for value in ["other", "INVOICE-REVIEWER", " invoice-reviewer ", "finance.read"]:
            with self.subTest(value=value):
                self.env["ADMIN_ROLE"] = value
                with self.assertRaises(preflight.PreflightError): self.run_preflight()

    def test_implicit_default_or_ambiguous_map_fails(self):
        for value in [None, "", "{}", '{"invoice-reviewer":["finance.read"],"invoice-reviewer":["finance.read"]}',
                      '{"invoice-reviewer":["finance.read"]," invoice-reviewer ":["finance.read"]}']:
            with self.subTest(value=value):
                self.env["XM_AUTH_ROLE_SCOPES"] = value
                with self.assertRaises(preflight.PreflightError): self.run_preflight()

    def test_role_without_finance_or_wrong_case_fails(self):
        for scopes in [["registry.read"], ["Finance.Read"]]:
            with self.subTest(scopes=scopes):
                self.env["XM_AUTH_ROLE_SCOPES"] = json.dumps({"invoice-reviewer": scopes})
                with self.assertRaises(preflight.PreflightError): self.run_preflight()

    def test_census_must_bind_the_actual_role_and_entire_map(self):
        for changed in [{"admin_role": "admin"}, {"role_scope_map": {"invoice-reviewer": ["finance.read"]}}]:
            with self.subTest(changed=changed):
                before = copy.deepcopy(self.proof)
                self.proof.update(changed)
                with self.assertRaises(preflight.PreflightError): self.run_preflight()
                self.proof = before
        self.env['XM_AUTH_ROLE_SCOPES'] = json.dumps({**self.roles, 'staff': ['registry.read', 'ops.read']})
        with self.assertRaises(preflight.PreflightError): self.run_preflight()

    def test_absent_joint_role_totp_or_login_ready_census_fails(self):
        for name in ["admin_role_total", "invoice_admin_total", "invoice_admin_totp_registered", "invoice_admin_login_ready_total"]:
            for value in [0, True, -1]:
                with self.subTest(name=name, value=value):
                    self.proof[name] = value
                    with self.assertRaises(preflight.PreflightError): self.run_preflight()
                    self.proof[name] = 2
        self.proof["invoice_admin_totp_registered"] = 1
        with self.assertRaises(preflight.PreflightError): self.run_preflight()

    def test_runtime_scope_normalization_and_duplicate_scopes_preserved(self):
        self.env["XM_AUTH_ROLE_SCOPES"] = '{" invoice-reviewer ":[" finance.read ","finance.read"],"staff":["registry.read"]}'
        self.run_preflight()

    def test_scope_only_or_whitespace_role_rows_cannot_back_a_forged_count(self):
        for roles in [["staff"], [" invoice-reviewer "], ["INVOICE-REVIEWER"]]:
            with self.subTest(roles=roles):
                self.rows['staff'][0]['roles'] = roles
                with self.assertRaises(preflight.PreflightError): self.run_preflight()

    def test_unregistered_or_login_blocked_rows_cannot_back_a_forged_count(self):
        for flag in ['totp_registered', 'disabled', 'locked', 'must_change_password', 'must_enroll_totp']:
            with self.subTest(flag=flag):
                original = self.rows['staff'][0][flag]
                self.rows['staff'][0][flag] = not original
                with self.assertRaises(preflight.PreflightError): self.run_preflight()
                self.rows['staff'][0][flag] = original

    def test_honest_census_with_partial_totp_still_fails(self):
        self.rows['staff'][0]['totp_registered'] = False
        self.rows.update(invoice_admin_totp_registered=1, invoice_admin_login_ready_total=1,
                         finance_read_totp_registered=1, finance_read_enabled_totp_registered=1)
        self.proof.update(invoice_admin_totp_registered=1, invoice_admin_login_ready_total=1)
        with self.assertRaises(preflight.PreflightError): self.run_preflight()

    def test_prefix_validation_matches_runtime_without_case_folding_role_membership(self):
        self.env['XM_AUTH_ROLE_SCOPES'] = json.dumps({**self.roles, 'Registry.reader': ['finance.read']})
        with self.assertRaises(preflight.PreflightError): self.run_preflight()

    def test_snapshot_summary_cannot_disagree_with_its_own_rows(self):
        self.rows['invoice_admin_total'] = 3
        with self.assertRaises(preflight.PreflightError): self.run_preflight()


if __name__ == "__main__": unittest.main()
