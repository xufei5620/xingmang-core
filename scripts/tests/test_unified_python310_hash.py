"""Hash compatibility is byte-identical without Python 3.11 file_digest."""
import hashlib
import importlib.util
import io
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec=importlib.util.spec_from_file_location('unified_hash_compat',Path(__file__).resolve().parents[1]/'unified-service.py')
gate=importlib.util.module_from_spec(spec);spec.loader.exec_module(gate)


class HashCompatibilityTests(unittest.TestCase):
    def test_old_new_sha256_are_identical_including_multiple_chunks(self):
        for data in (b'',b'abc',bytes(range(256))*8193+b'final partial block'):
            with self.subTest(size=len(data)),tempfile.TemporaryDirectory() as folder:
                path=Path(folder)/'public-vector.bin';path.write_bytes(data)
                legacy=getattr(hashlib,'file_digest',None)
                old=legacy(io.BytesIO(data),'sha256').hexdigest() if legacy else hashlib.sha256(data).hexdigest()
                with patch.object(hashlib,'file_digest',create=True,side_effect=AttributeError('Python 3.10 has no file_digest')):
                    self.assertEqual(gate.file_hash(path),old)
                    self.assertEqual(gate.stream_hash(io.BytesIO(data)),old)


if __name__=='__main__':unittest.main()
