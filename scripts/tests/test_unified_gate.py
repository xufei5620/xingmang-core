"""Behavior tests use disposable Git repositories and synthetic build records only."""
import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import tarfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("unified_service", ROOT / "scripts/unified-service.py")
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)


class UnifiedSourceGateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / "repo"
        self.root.mkdir()
        for name in ("platform/main.go", "invoice/backend/main.go", ".dockerignore", "deploy/unified/images.json", "scripts/unified-service.py"):
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("synthetic baseline\n", encoding="utf-8")
        self.git("init", "-q")
        self.git("add", ".")
        self.git("-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-qm", "fixture")

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.root), *args], stderr=subprocess.STDOUT)

    def test_dirty_in_either_domain_or_root_blocks_build(self):
        for name in ("platform/main.go", "invoice/backend/main.go", ".dockerignore", "deploy/unified/images.json", "scripts/unified-service.py"):
            path = self.root / name
            original = path.read_bytes()
            path.write_bytes(original + b"changed\n")
            with self.assertRaisesRegex(gate.GateError, "uncommitted"):
                gate.source_provenance(self.root, require_clean=True)
            path.write_bytes(original)
        gate.source_provenance(self.root, require_clean=True)

    def test_committed_changes_in_each_tree_invalidate_previous_record(self):
        for name in ("platform/main.go", "invoice/backend/main.go", ".dockerignore"):
            before = gate.source_provenance(self.root, require_clean=True)
            with (self.root / name).open("a", encoding="utf-8") as stream:
                stream.write("new fact\n")
            self.git("add", name)
            self.git("-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "commit", "-qm", "next")
            after = gate.source_provenance(self.root, require_clean=True)
            self.assertNotEqual(before["inventorySha256"], after["inventorySha256"])
            with self.assertRaisesRegex(gate.GateError, "source"):
                gate.assert_source_matches(before, after)

    def test_untracked_source_is_not_silently_excluded(self):
        for name in ("platform/new.go", "invoice/backend/new.go"):
            path = self.root / name
            path.write_text("untracked", encoding="utf-8")
            with self.assertRaisesRegex(gate.GateError, "uncommitted"):
                gate.source_provenance(self.root, require_clean=True)
            path.unlink()

    def test_sensitive_context_entries_are_never_opened(self):
        for name in ("platform/deploy/.env", "invoice/keys/private.pem", "invoice/deploy/secrets/keyring.json", "platform/runtime/live.db", "logs/private.log"):
            self.assertFalse(gate.context_path_allowed(name), name)
        for name in ("platform/internal/platform/credentials/store.go", "platform/internal/platform/secrets/provider.go", "invoice/backend/internal/fieldcrypto/keyring.go"):
            self.assertTrue(gate.context_path_allowed(name), name)

    def test_remote_daemons_rejected_before_build(self):
        for endpoint in ("ssh://production", "tcp://127.0.0.1:2375", "tcp://10.0.0.1:2376", "npipe:////remote/pipe/docker_engine"):
            with self.assertRaisesRegex(gate.GateError, "local Docker"):
                gate.assert_local_endpoint(endpoint)
        gate.assert_local_endpoint("npipe:////./pipe/dockerDesktopLinuxEngine")

    def test_snapshot_rejects_symlink_before_opening_target(self):
        # Git mode can be constructed without Windows symlink privilege or target access.
        with self.assertRaisesRegex(gate.GateError, "symlink"):
            gate.validate_context_entry("platform/link.go", "120000")


class BaseImageEvidenceTests(unittest.TestCase):
    def test_records_actual_buildkit_digests_not_only_dockerfile_tags(self):
        metadata={'buildx.build.provenance':{'materials':[
            {'uri':'pkg:docker/alpine@3.22?platform=linux%2Famd64','digest':{'sha256':'a'*64}},
            {'uri':'pkg:docker/golang@1.27.0-alpine','digest':{'sha256':'b'*64}},
        ]}}
        self.assertEqual(gate.base_image_evidence(metadata),[
            {'uri':'pkg:docker/alpine@3.22?platform=linux%2Famd64','digest':'sha256:'+'a'*64},
            {'uri':'pkg:docker/golang@1.27.0-alpine','digest':'sha256:'+'b'*64},
        ])
    def test_missing_or_malformed_base_evidence_fails(self):
        for bad in ({}, {'buildx.build.provenance':{}},
                    {'buildx.build.provenance':{'materials':[{'uri':'pkg:docker/alpine','digest':{'sha256':'wrong'}}]}}):
            with self.subTest(metadata=bad),self.assertRaises(gate.GateError): gate.base_image_evidence(bad)


class UnifiedArtifactGateTests(unittest.TestCase):
    def test_exact_image_inventory_rejects_missing_duplicate_or_legacy_api(self):
        definitions = json.loads((ROOT / "deploy/unified/images.json").read_text("utf-8"))
        records = [{"name": item["name"], "imageId": "sha256:" + "a" * 64} for item in definitions]
        gate.assert_image_inventory(records, definitions)
        for bad in (records[:-1], records + [records[0]], records + [{"name": "invoice-api", "imageId": "sha256:" + "a" * 64}]):
            with self.assertRaisesRegex(gate.GateError, "inventory"):
                gate.assert_image_inventory(bad, definitions)

    def test_old_or_production_approved_manifest_is_rejected(self):
        for doc in ({"schema": "solov.invoice.release-image-gate/v1"}, {"schema": gate.SCHEMA, "productionReady": True}):
            with self.assertRaises(gate.GateError):
                gate.assert_manifest_kind(doc)
        gate.assert_manifest_kind({"schema": gate.SCHEMA, "productionReady": False})

    def test_archive_path_cannot_escape_output_directory(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            for bad in ("../outside.tar", str(root.parent / "outside.tar"), "images/../../outside.tar"):
                with self.assertRaisesRegex(gate.GateError, "artifact path"):
                    gate.artifact_path(root, bad)

    def test_archive_replacement_fails_hash_binding(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "synthetic-image.tar"
            path.write_bytes(b"original synthetic archive")
            expected = hashlib.sha256(path.read_bytes()).hexdigest()
            gate.assert_file_hash(path, expected)
            path.write_bytes(b"replacement archive")
            with self.assertRaisesRegex(gate.GateError, "hash"):
                gate.assert_file_hash(path, expected)

    def test_saved_archive_is_bound_to_the_actual_image_config_and_tag(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "image.tar"
            config = b'{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}'
            image_id = "sha256:" + hashlib.sha256(config).hexdigest()
            manifest = json.dumps([{"Config": "config.json", "RepoTags": ["test/unified:local"], "Layers": []}]).encode()
            with tarfile.open(path, "w") as archive:
                for name, body in (("manifest.json", manifest), ("config.json", config)):
                    info = tarfile.TarInfo(name)
                    info.size = len(body)
                    archive.addfile(info, io.BytesIO(body))
            gate.assert_archive_image(path, "test/unified:local", image_id)
            for reference, digest in (("test/other:local", image_id), ("test/unified:local", "sha256:" + "0" * 64)):
                with self.assertRaisesRegex(gate.GateError, "archive image"):
                    gate.assert_archive_image(path, reference, digest)


if __name__ == "__main__":
    unittest.main()
