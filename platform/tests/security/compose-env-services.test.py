"""Process-to-service environment wiring; no Compose daemon or secrets needed."""
import contextlib
import io
from pathlib import Path
import tempfile
import unittest
import types

ROOT = Path(__file__).resolve().parents[2]
path = ROOT/'scripts/check-compose-env.py'
checker = types.ModuleType('compose_env')
checker.__file__ = str(path)
exec(compile(path.read_bytes(), str(path), 'exec'), checker.__dict__)

class ServiceEnvironmentTest(unittest.TestCase):
    def check(self, api='      XM_SMS_MODE: fake\n', worker='      XM_SMS_MODE: fake\n',
              api_head='    environment:\n', remove_source=False):
        with tempfile.TemporaryDirectory(prefix='compose-env-') as tmp:
            root = Path(tmp)
            for service in ('platform-api', 'platform-worker'):
                p = root/'cmd'/service/'main.go'
                p.parent.mkdir(parents=True)
                p.write_text('package main\nfunc main() { os.Getenv("XM_SMS_MODE"); os.Getenv("XM_SMS_PROVIDERS") }\n', encoding='utf-8')
            if remove_source:
                (root/'cmd/platform-api/main.go').unlink()
                (root/'cmd/platform-api').rmdir()
            compose = root/'launch.yaml'
            compose.write_text('services:\n  platform-api:\n'+api_head+api+
                               '  platform-worker:\n    environment:\n'+worker, encoding='utf-8')
            original = checker.ROOT, checker.COMPOSE
            try:
                checker.ROOT, checker.COMPOSE = root, compose
                with contextlib.redirect_stderr(io.StringIO()):
                    return checker.main()
            finally:
                checker.ROOT, checker.COMPOSE = original

    def test_legal(self): self.assertEqual(self.check(), 0)
    def test_api_missing(self): self.assertEqual(self.check(api='      XM_OTHER: x\n'), 1)
    def test_worker_missing(self): self.assertEqual(self.check(worker='      XM_OTHER: x\n'), 1)
    def test_wrong_mapping(self): self.assertEqual(self.check(api_head='    labels:\n'), 1)
    def test_comment_not_wiring(self): self.assertEqual(self.check(api='      # XM_SMS_MODE: fake\n'), 1)
    def test_retired_in_worker(self): self.assertEqual(self.check(worker='      XM_SMS_MODE: fake\n      XM_SMS_PROVIDERS: bad\n'), 1)
    def test_unsupported_shape(self): self.assertEqual(self.check(api_head='    environment: {XM_SMS_MODE: fake}\n', api=''), 1)
    def test_missing_source(self): self.assertEqual(self.check(remove_source=True), 1)

if __name__ == '__main__': unittest.main()
