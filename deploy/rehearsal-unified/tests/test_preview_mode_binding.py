"""Exercise production preview through the real driver/config/file validators."""
import copy
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
sys.path.insert(0, str(Path(__file__).resolve().parent))
import lifecycle
import restore
from test_repeat_cutover import configuration


def preview_configuration(root, mode="production"):
    config = configuration(root)
    config["mode"] = mode
    config["previous"]["input_snapshot"] = lifecycle.capture_old_inputs(
        config, [str(root / "old-release")], str(Path(lifecycle.__file__).absolute().parents[2]),
        str(root / "reviewed-inputs.json"))
    preview_mode = "local-synthetic" if mode == "local-synthetic" else "server-rehearsal"
    smoke_path = root / "preview-smoke.json"
    smoke_path.write_text(json.dumps({
        "schema": "xingmang.unified.public-smoke/v1", "mode": preview_mode,
        "origins": {"admin": "https://console.example.com", "user": "https://invoice.example.com"},
        "connect_to": {key: {"address": "127.0.0.1", "port": port}
                       for key, port in (("admin", 18443), ("user", 18444))}}))
    projects = copy.deepcopy(config["candidate"]["projects"])
    for project in projects:
        project["name"] = "xm-rehearsal-preview-" + project["kind"]
    config["rehearsal"] = {"projects": projects, "smoke_config": str(smoke_path),
                           "temporary_identity_paths": [], "seed": {}, "host_preflight": {}}
    return config


class PreviewModeBindingTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.config = preview_configuration(self.root)
        self.driver = lifecycle.DockerDriver(self.config, record_root=self.root / "current-run")
        self.passed = {"status": "PASS", "exit_code": 0, "cleanup_complete": True,
                       "source_head": self.config["candidate"]["head"],
                       "manifest_sha256": self.config["candidate"]["manifest_sha256"]}

    def test_production_preview_constructs_real_driver_with_separate_bound_inputs(self):
        original_config = copy.deepcopy(self.config)
        original_path = Path(self.config["previous"]["input_snapshot"]["path"])
        original_bytes = original_path.read_bytes()
        with patch.object(restore, "rehearse", return_value=self.passed) as execute:
            self.assertEqual(restore.preview_candidate(self.driver), self.passed)
        preview = execute.call_args.args[0]
        self.assertIs(type(preview), lifecycle.DockerDriver)
        self.assertEqual(preview.config["mode"], "server-rehearsal")
        derived = preview.config["previous"]["input_snapshot"]
        self.assertNotEqual(derived["path"], str(original_path))
        self.assertTrue(Path(derived["path"]).is_relative_to(preview.output))
        lifecycle.require_old_inputs(preview.config)
        original = json.loads(original_bytes)
        observed = lifecycle.read_public_json(derived["path"])
        self.assertEqual(observed["mode"], "server-rehearsal")
        for key in ("schema", "status", "new_source_root", "retained_roots", "projects", "files"):
            self.assertEqual(observed[key], original[key], key)
        self.assertEqual(self.config, original_config)
        self.assertEqual(original_path.read_bytes(), original_bytes)

    def test_changed_original_input_cannot_be_rebaselined_by_preview(self):
        old_env = Path(self.config["previous"]["projects"][0]["env_file"])
        old_env.write_bytes(b"PUBLIC_FIXTURE=changed-after-review\n")
        with patch.object(restore, "rehearse") as execute:
            with self.assertRaisesRegex(lifecycle.OperatorError, "retained original Compose or environment bytes changed"):
                restore.preview_candidate(self.driver)
            execute.assert_not_called()
        self.assertFalse((self.driver.output / "candidate-precheck").exists())

    def test_reviewed_snapshot_mode_is_not_silently_rewritten(self):
        descriptor = self.config["previous"]["input_snapshot"]
        path = Path(descriptor["path"])
        proof = lifecycle.read_public_json(path)
        proof["mode"] = "server-rehearsal"
        path.write_text(json.dumps(proof))
        descriptor["sha256"] = lifecycle.digest(path)
        with patch.object(restore, "rehearse") as execute:
            with self.assertRaisesRegex(lifecycle.OperatorError, "old input snapshot does not bind the original projects"):
                restore.preview_candidate(self.driver)
            execute.assert_not_called()
        self.assertFalse((self.driver.output / "candidate-precheck").exists())

    def test_same_mode_preview_retains_its_existing_binding(self):
        for mode in ("local-synthetic", "server-rehearsal"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                config = preview_configuration(root, mode)
                original = copy.deepcopy(config)
                driver = lifecycle.DockerDriver(config, record_root=root / "current-run")
                with patch.object(restore, "rehearse", return_value=self.passed) as execute:
                    restore.preview_candidate(driver)
                preview = execute.call_args.args[0]
                self.assertIs(type(preview), lifecycle.DockerDriver)
                self.assertEqual(preview.config["previous"]["input_snapshot"],
                                 config["previous"]["input_snapshot"])
                lifecycle.require_old_inputs(preview.config)
                self.assertEqual(config, original)

    def test_capture_cannot_expand_the_reviewed_retention_roots(self):
        capture = restore.capture_old_inputs
        extra_root = self.root / "not-reviewed"
        extra_root.mkdir()
        def changed_capture(*args):
            binding = capture(*args)
            path = Path(binding["path"])
            captured = lifecycle.read_public_json(path)
            captured["retained_roots"].append(str(extra_root))
            path.write_text(json.dumps(captured))
            return {"path": str(path), "sha256": lifecycle.digest(path)}
        with patch.object(restore, "capture_old_inputs", side_effect=changed_capture), \
                patch.object(restore, "rehearse") as execute:
            with self.assertRaisesRegex(lifecycle.OperatorError, "preview input snapshot differs"):
                restore.preview_candidate(self.driver)
            execute.assert_not_called()

    def test_production_snapshot_is_rechecked_after_preview_capture(self):
        capture = restore.capture_old_inputs
        original_path = Path(self.config["previous"]["input_snapshot"]["path"])
        def changed_capture(*args):
            binding = capture(*args)
            original_path.write_bytes(original_path.read_bytes() + b" ")
            return binding
        with patch.object(restore, "capture_old_inputs", side_effect=changed_capture), \
                patch.object(restore, "rehearse") as execute:
            with self.assertRaisesRegex(lifecycle.OperatorError, "old input preservation snapshot changed"):
                restore.preview_candidate(self.driver)
            execute.assert_not_called()

    def test_temporary_input_drift_during_capture_cannot_be_accepted(self):
        capture = restore.capture_old_inputs
        old_env = Path(self.config["previous"]["projects"][0]["env_file"])
        before = old_env.read_bytes()
        def changed_capture(*args):
            old_env.write_bytes(b"PUBLIC_FIXTURE=transient-during-capture\n")
            try:
                return capture(*args)
            finally:
                old_env.write_bytes(before)
        with patch.object(restore, "capture_old_inputs", side_effect=changed_capture), \
                patch.object(restore, "rehearse") as execute:
            with self.assertRaisesRegex(lifecycle.OperatorError, "preview input snapshot differs"):
                restore.preview_candidate(self.driver)
            execute.assert_not_called()

    def test_repeated_preview_cannot_overwrite_the_existing_evidence(self):
        with patch.object(restore, "rehearse", return_value=self.passed) as execute:
            restore.preview_candidate(self.driver)
            path = Path(execute.call_args.args[0].config["previous"]["input_snapshot"]["path"])
            before = path.read_bytes()
            execute.reset_mock()
            with self.assertRaisesRegex(lifecycle.OperatorError, "candidate preview evidence directory already exists"):
                restore.preview_candidate(self.driver)
            execute.assert_not_called()
            self.assertEqual(path.read_bytes(), before)


if __name__ == "__main__":
    unittest.main()
