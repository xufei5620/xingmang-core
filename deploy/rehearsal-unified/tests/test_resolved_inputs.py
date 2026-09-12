"""Resolved deployment input boundaries; not a real container rehearsal."""
import copy
import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
import lifecycle
import restore


class ResolvedInputTests(unittest.TestCase):
    def test_real_start_commands_wait_for_existing_healthchecks(self):
        driver = object.__new__(lifecycle.DockerDriver)
        previous = [{"kind": kind, "name": kind, "services": {kind: {"role": kind}}} for kind in ("idp", "platform", "invoice", "sources")]
        candidate = [{"kind": kind, "name": kind, "services": {kind: {"role": kind}}} for kind in ("unified", "sources")]
        driver.projects = lambda side: candidate if side == "candidate" else previous
        calls = []; driver.compose = lambda p,n,args: calls.append(args)
        driver.start_new(); driver.start_old()
        self.assertEqual(len(calls), 8)
        for call in calls[:2]: self.assertNotIn("--wait", call)
        for call in calls[2:]: self.assertIn("--wait", call)

    def test_in_place_cutover_binds_data_sources_not_only_equal_ledgers(self):
        self.assertTrue(hasattr(lifecycle, "require_original_data"))
        original = [{"role": role, "mounts": [{"type": "volume", "target": target, "source": source}]} for role,target,source in
                    (("platform-postgres", "/var/lib/postgresql", "/vol/platform"), ("invoice-postgres", "/var/lib/postgresql", "/vol/invoice"),
                     ("invoice-api", "/data/documents", "/vol/documents"), ("platform-api", "/run/xm/secrets", "/vol/secrets"))]
        original += [{"role": role, "mounts": [{"type": "volume", "target": "/state", "source": "/vol/"+role}]} for role in lifecycle.STREAM_ROLES]
        candidate = copy.deepcopy(original)
        candidate[2]["role"] = "platform-api"
        lifecycle.require_original_data(original, candidate)
        for index in range(len(candidate)):
            bad = copy.deepcopy(candidate); bad[index]["mounts"][0]["source"] += "-copy"
            with self.assertRaises(lifecycle.OperatorError): lifecycle.require_original_data(original, bad)
        with self.assertRaises(lifecycle.OperatorError): lifecycle.require_original_data(original, candidate[:-1])

    def test_final_compose_image_must_match_the_reviewed_runtime(self):
        self.assertTrue(hasattr(lifecycle, "require_resolved_images"))
        project = {"services": {"api": {"image_id": "sha256:" + "a" * 64}}}
        resolved = {"services": {"api": {"image": "reviewed:tag"}}}
        seen = []
        lookup = lambda reference: seen.append(reference) or "sha256:" + "a" * 64
        lifecycle.require_resolved_images(project, resolved, lookup)
        self.assertEqual(seen, ["reviewed:tag"])
        for value, identity in (({}, "a"), ({"services": {}}, "a"), (resolved, "b")):
            with self.assertRaises(lifecycle.OperatorError):
                lifecycle.require_resolved_images(project, value, lambda _: "sha256:" + identity * 64)

    def test_frozen_networks_and_ports_cannot_reach_external_or_host_targets(self):
        self.assertTrue(hasattr(restore, "require_isolated_networks"))
        good = {"networks": {"a": {"name": "xm-rehearsal-a", "internal": True}},
                "services": {"api": {"networks": {"a": {}}, "ports": [{"host_ip": "127.0.0.1", "published": "58090", "target": 80}]},
                             "scanner": {"network_mode": "none"}}}
        restore.require_isolated_networks(good, lambda _: {"Internal": True})
        external = copy.deepcopy(good); external["networks"]["a"] = {"name": "xm-rehearsal-a", "external": True}
        restore.require_isolated_networks(external, lambda _: {"Internal": True})
        for mutate in (lambda x: x["networks"]["a"].update(internal=False),
                       lambda x: x["services"]["api"].update(network_mode="host"),
                       lambda x: x["services"]["api"]["ports"][0].update(host_ip="0.0.0.0"),
                       lambda x: x["services"]["api"].update(networks={"missing": {}})):
            bad = copy.deepcopy(good); mutate(bad)
            with self.assertRaises(lifecycle.OperatorError): restore.require_isolated_networks(bad, lambda _: {"Internal": True})
        with self.assertRaises(lifecycle.OperatorError): restore.require_isolated_networks(external, lambda _: {"Internal": False})

    def test_a_new_rehearsal_invalidates_old_pass_without_erasing_history(self):
        self.assertTrue(hasattr(restore, "invalidate_receipt"))
        with tempfile.TemporaryDirectory() as tmp:
            base = Path(tmp); receipt = base / "rehearsal-pass.json"
            original = b'{"status":"PASS","source_head":"old"}\n'; receipt.write_bytes(original)
            driver = type("Driver", (), {"state": base, "output": base / "new-run"})()
            restore.invalidate_receipt(driver)
            self.assertNotEqual(json.loads(receipt.read_text())["status"], "PASS")
            self.assertEqual((driver.output / "prior-rehearsal-receipt.json").read_bytes(), original)


if __name__ == "__main__": unittest.main()
