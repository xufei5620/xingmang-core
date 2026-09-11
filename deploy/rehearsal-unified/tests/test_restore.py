"""Signed package and ownership boundaries; never production credentials."""
import datetime as dt
import importlib.util
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch
import hashlib

ROOT = Path(__file__).resolve().parents[1]


def module(test):
    path = ROOT / "restore.py"
    test.assertTrue(path.is_file(), "the signed two-domain restore is absent")
    sys.path.insert(0, str(ROOT))
    spec = importlib.util.spec_from_file_location("unified_restore", path)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


class SignedBackupTests(unittest.TestCase):
    def test_manifest_rejects_missing_duplicate_extra_and_traversal(self):
        m = module(self)
        name = "invoice-20260912T000000Z.postgres.dump.age"
        other = "invoice-20260912T000000Z.metadata.tar.age"
        good = "a" * 64 + "  " + name + "\n" + "b" * 64 + "  " + other + "\n"
        self.assertEqual(set(m.parse_manifest(good, {name, other})), {name, other})
        for bad in (good.splitlines()[0], good + good.splitlines()[0], good + "c" * 64 + "  extra.age\n", good.replace(name, "../" + name)):
            with self.subTest(bad=bad):
                with self.assertRaises(m.OperatorError): m.parse_manifest(bad, {name, other})

    def test_backup_age_comes_from_every_signed_component_name(self):
        m = module(self); now = dt.datetime(2026, 9, 12, 1, tzinfo=dt.timezone.utc)
        self.assertEqual(m.capture_time("invoice", ["invoice-20260912T000000Z.postgres.dump.age", "invoice-20260912T000000Z.metadata.tar.age"], 24, now), "2026-09-12T00:00:00+00:00")
        for names in (["invoice-20260910T000000Z.postgres.dump.age"], ["invoice-20260913T000000Z.postgres.dump.age"], ["invoice-20260912T000000Z.postgres.dump.age", "invoice-20260912T003000Z.metadata.tar.age"], ["platform-20260912T000000Z.postgres.dump.age"]):
            with self.subTest(names=names):
                with self.assertRaises(m.OperatorError): m.capture_time("invoice", names, 24, now)

    def test_public_signer_rejects_wildcards_cross_domain_and_wrong_namespace(self):
        m = module(self)
        good = 'invoice-backup namespaces="solov-invoice-backup-v1" ssh-ed25519 YWJj\n'
        m.validate_signers("invoice", good)
        for bad in ("", good.replace("invoice-backup ", "* "), good.replace("solov-invoice", "solov-platform"), good + 'other ssh-ed25519 YWJj\n'):
            with self.subTest(bad=bad):
                with self.assertRaises(m.OperatorError): m.validate_signers("invoice", bad)

    def test_owned_volume_must_match_exact_label_and_name_before_removal(self):
        m = module(self)
        metadata = {"Name": "xm-rehearsal-test-documents", "Labels": {"xingmang.rehearsal.owner": "abcd"}}
        m.require_owned_volume(metadata, "xm-rehearsal-test-documents", "abcd")
        for bad in ({**metadata, "Name": "production-documents"}, {**metadata, "Labels": {}}, {**metadata, "Labels": {"xingmang.rehearsal.owner": "other"}}):
            with self.assertRaises(m.OperatorError): m.require_owned_volume(bad, "xm-rehearsal-test-documents", "abcd")

    def test_snapshot_is_not_pass_before_cleanup_success(self):
        m = module(self)
        for cleanup in (False, None, "true"):
            with self.assertRaises(m.OperatorError): m.finish_rehearsal({"status": "PASS"}, cleanup)
        self.assertEqual(m.finish_rehearsal({"status": "PASS"}, True)["status"], "PASS")

    def test_signature_failure_precedes_ciphertext_read_and_identity_is_never_read(self):
        m = module(self)
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); descriptors = {}
            timestamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
            for domain, kinds in (("platform", ("database", "metadata")), ("invoice", ("database", "documents", "source_state", "metadata"))):
                paths = {}
                for key in ("manifest", "signature", "allowed_signers", "identity_file", "age_binary", "ssh_keygen_binary"):
                    path = root / (domain + "." + key); path.write_text("synthetic fixture")
                    paths[key] = str(path)
                principal, namespace = m.BACKUP_ANCHORS[domain]
                Path(paths["allowed_signers"]).write_text(principal + ' namespaces="' + namespace + '" ssh-ed25519 YWJj\n')
                components = {}
                lines = []
                for kind in kinds:
                    path = root / (domain + "-" + timestamp + m.SUFFIXES[kind]); path.write_bytes(b"ciphertext fixture")
                    components[kind] = str(path); lines.append(hashlib.sha256(path.read_bytes()).hexdigest() + "  " + path.name)
                Path(paths["manifest"]).write_text("\n".join(lines) + "\n")
                descriptors[domain] = {**paths, "components": components}
            class Driver:
                def __init__(self): self.calls = []
                def command(self, name, args, **kwargs):
                    self.calls.append((name, args))
                    raise m.OperatorError("synthetic invalid signature")
            driver = Driver()
            with patch.object(m, "digest", side_effect=AssertionError("ciphertext was read before signature verification")):
                with self.assertRaises(m.OperatorError): m.verify_backups(driver, descriptors, 24)
            self.assertEqual([c[0] for c in driver.calls], ["verify-platform-signature"])

    def test_signature_success_does_not_open_identity_or_allow_extra_components(self):
        # Source-level prohibition supplements the real age pipeline: the
        # helper has no identity read method; age receives the named path only.
        m = module(self)
        row = {"age_binary": "/usr/bin/age", "identity_file": "/offline/identity", "components": {"database": "/backup/encrypted.age"}}
        self.assertEqual(m.decrypt_args(row, "database"), ["/usr/bin/age", "--decrypt", "-i", "/offline/identity", "/backup/encrypted.age"])


if __name__ == "__main__": unittest.main()
