"""P1-11: a configured HEAD alone cannot attest the Python actually executed."""
import hashlib
import importlib
import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import lifecycle


class RuntimeBindingTests(unittest.TestCase):
    def test_self_consistent_inventory_cannot_omit_private_endpoint_authority(self):
        m = importlib.import_module('operator_source')
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            entries = []
            for name in sorted(m.REQUIRED - {'deploy/rehearsal-unified/rehearsal_endpoint.py'}):
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                body = b'# staged public source\n'
                path.write_bytes(body)
                entries.append({'path': name, 'mode': '100644', 'blob': m.git_blob(body)})
            source = {'gitHead': 'a' * 40, 'gitDirty': False, 'entries': entries,
                      'inventorySha256': hashlib.sha256(json.dumps(entries, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode()).hexdigest()}
            with self.assertRaises(lifecycle.OperatorError):
                m.verify(root, source)

    def test_actual_artifact_preflight_rejects_unattested_operator_sources(self):
        with tempfile.TemporaryDirectory() as tmp:
            manifest = Path(tmp) / 'manifest.json'
            manifest.write_text(json.dumps({'source': {'gitHead': 'a' * 40, 'gitDirty': False, 'entries': [], 'inventorySha256': hashlib.sha256(b'[]').hexdigest()}, 'images': []}))
            driver = object.__new__(lifecycle.DockerDriver)
            driver.config = {'candidate': {'head': 'a' * 40, 'manifest': str(manifest), 'manifest_sha256': lifecycle.digest(manifest)}}
            driver.output = Path(tmp) / 'evidence'
            driver.verify_local_engine = lambda: None
            driver.projects = lambda side: []
            with self.assertRaises(lifecycle.OperatorError):
                driver.artifact_preflight()

    def test_staged_source_binding_works_without_git_and_rejects_edited_engine(self):
        path = Path(__file__).resolve().parents[1] / 'operator_source.py'
        self.assertTrue(path.is_file(), 'operator source binding is absent')
        m = importlib.import_module('operator_source')
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            entries = []
            for name in sorted(m.REQUIRED):
                p = root / name; p.parent.mkdir(parents=True, exist_ok=True)
                body = b'# staged public source\n'
                p.write_bytes(body.replace(b'\n', b'\r\n'))
                entries.append({'path': name, 'mode': '100644', 'blob': hashlib.sha1(b'blob ' + str(len(body)).encode() + b'\0' + body).hexdigest()})
            source = {'gitHead': 'a' * 40, 'gitDirty': False, 'entries': entries,
                      'inventorySha256': hashlib.sha256(json.dumps(entries, sort_keys=True, separators=(',', ':'), ensure_ascii=False).encode()).hexdigest()}
            proof = m.verify(root, source)
            self.assertEqual(proof['head'], source['gitHead'])
            self.assertEqual(len(proof['files']), len(entries))
            self.assertFalse((root / '.git').exists())
            with self.assertRaises(lifecycle.OperatorError):
                m.verify(root, {**source, 'inventorySha256': 'f' * 64})
            p = root / 'deploy/rehearsal-unified/restore.py'
            p.write_bytes(p.read_bytes() + b'# modified after previous qualification\n')
            with self.assertRaises(lifecycle.OperatorError):
                m.verify(root, source)

    def test_durable_deployment_record_contains_actual_source_proof(self):
        with tempfile.TemporaryDirectory() as tmp:
            driver = object.__new__(lifecycle.DockerDriver)
            driver.config = {'mode': 'local-synthetic', 'candidate': {'head':'a'*40,'manifest_sha256':'b'*64}}
            driver.output = Path(tmp)/'run'; driver.state = Path(tmp)/'state'
            driver.operator_source = {'head':'a'*40,'files':[{'path':'public-source.py','sha256':'c'*64}]}
            driver.record({'status':'COMMITTED','snapshot':{'original':'kept'}})
            record = json.loads((driver.state/'deployment-record.json').read_text())
            self.assertEqual(record.get('actual_operator_source'),driver.operator_source)


if __name__ == '__main__': unittest.main()
