"""Process-to-service environment wiring; no Compose daemon or secrets needed."""
import contextlib
import io
import json
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

class UnifiedJSONEnvironmentTest(unittest.TestCase):
    def check(self, transform=lambda doc: None, raw=None):
        with tempfile.TemporaryDirectory(prefix='unified-compose-env-') as tmp:
            root=Path(tmp)/'platform'
            for service in ('platform-api', 'platform-worker'):
                p=root/'cmd'/service/'main.go'; p.parent.mkdir(parents=True)
                p.write_text('package main\nfunc main() { os.Getenv("XM_SMS_MODE"); os.Getenv("XM_SMS_PROVIDERS") }\n')
            invoice=root.parent/'invoice/backend/service/runtime.go'
            invoice.parent.mkdir(parents=True)
            invoice.write_text('package service\n', encoding='utf-8')
            doc={"services":{name:{"environment":{"XM_SMS_MODE":"${XM_SMS_MODE:-fake}"}} for name in ('platform-api','platform-worker')}}
            transform(doc)
            compose=root/'compose.json'; compose.write_text(raw if raw is not None else json.dumps(doc))
            original=checker.ROOT,checker.COMPOSE
            try:
                checker.ROOT,checker.COMPOSE=root,compose
                with contextlib.redirect_stderr(io.StringIO()): return checker.main()
            finally: checker.ROOT,checker.COMPOSE=original

    def test_legal_unified_json(self): self.assertEqual(self.check(),0)
    def test_api_cannot_borrow_worker_environment(self):
        self.assertEqual(self.check(lambda d:d['services']['platform-api']['environment'].pop('XM_SMS_MODE')),1)
    def test_worker_cannot_borrow_api_environment(self):
        self.assertEqual(self.check(lambda d:d['services']['platform-worker']['environment'].pop('XM_SMS_MODE')),1)
    def test_retired_in_json_is_rejected(self):
        self.assertEqual(self.check(lambda d:d['services']['platform-api']['environment'].update(XM_SMS_PROVIDERS='bad')),1)
    def test_non_explicit_or_inherited_environment_is_rejected(self):
        for key,value in [('extends','base'),('env_file','private.env'),('<<',{'environment':{'XM_SMS_MODE':'fake'}})]:
            with self.subTest(key=key):
                self.assertEqual(self.check(lambda d:d['services']['platform-api'].update({key:value})),1)
        for bad in (None,['XM_SMS_MODE=fake'],{'XM_SMS_MODE':None},{'XM_SMS_MODE':{'secret':'file'}}):
            with self.subTest(bad=bad):
                self.assertEqual(self.check(lambda d:d['services']['platform-api'].update(environment=bad)),1)
    def test_duplicate_json_keys_are_rejected(self):
        self.assertEqual(self.check(raw='{"services":{"platform-api":{"environment":{"XM_SMS_MODE":"x","XM_SMS_MODE":"y"}},"platform-worker":{"environment":{"XM_SMS_MODE":"x"}}}}'),1)


class LinkedInvoiceEnvironmentTest(unittest.TestCase):
    def check(self, source, api=(), worker=(), remove_source=False):
        with tempfile.TemporaryDirectory(prefix='linked-invoice-env-') as tmp:
            root=Path(tmp)/'platform'
            for service in ('platform-api', 'platform-worker'):
                path=root/'cmd'/service/'main.go'
                path.parent.mkdir(parents=True)
                path.write_text('package main\n', encoding='utf-8')
            invoice=root.parent/'invoice/backend/service/runtime.go'
            if not remove_source:
                invoice.parent.mkdir(parents=True)
                invoice.write_text('package service\n'+source, encoding='utf-8')
                (invoice.parent/'runtime_test.go').write_text('os.Getenv("TEST_ONLY")', encoding='utf-8')
            compose=root/'compose.json'
            compose.write_text(json.dumps({'services':{
                'platform-api':{'environment':{key:'' for key in api}},
                'platform-worker':{'environment':{key:'' for key in worker}},
            }}), encoding='utf-8')
            original=checker.ROOT,checker.COMPOSE
            try:
                checker.ROOT,checker.COMPOSE=root,compose
                stderr=io.StringIO()
                with contextlib.redirect_stderr(stderr): code=checker.main()
                return code,stderr.getvalue()
            finally: checker.ROOT,checker.COMPOSE=original

    def test_non_xm_invoice_variable_cannot_be_omitted(self):
        code, error=self.check('os.Getenv("SOURCE_MODE")')
        self.assertEqual(code,1)
        self.assertIn('SOURCE_MODE',error)
        self.assertIn('invoice/backend/service/runtime.go',error)
    def test_variable_must_be_in_embedded_api_not_worker(self):
        self.assertEqual(self.check('os.Getenv("FIELD_KEYRING_FILE")',worker=['FIELD_KEYRING_FILE'])[0],1)
    def test_all_invoice_literal_read_helpers_are_covered(self):
        for call in ('env','csvEnv','boundedIntEnv','boundedInt64Env','boundedDurationEnv'):
            with self.subTest(call=call):
                self.assertEqual(self.check(call+'("INVOICE_SETTING", "fallback")')[0],1)
                self.assertEqual(self.check(call+'("INVOICE_SETTING", "fallback")',api=['INVOICE_SETTING'])[0],0)
    def test_missing_linked_source_is_not_a_success(self):
        self.assertEqual(self.check('',remove_source=True)[0],1)
    def test_linked_test_files_do_not_require_runtime_configuration(self):
        self.assertEqual(self.check('os.Getenv("AUTH_MODE")',api=['AUTH_MODE'])[0],0)
    def test_mock_only_ip_settings_stay_out_of_production_compose(self):
        for key in ('ADMIN_IP_ALLOWLIST','ADMIN_BOOTSTRAP_IP_ALLOWLIST'):
            with self.subTest(key=key):
                source='csvEnv("'+key+'", "127.0.0.1/32")'
                self.assertEqual(self.check(source)[0],0)
                self.assertEqual(self.check(source,api=[key])[0],1)
    def test_comments_are_not_environment_readers(self):
        self.assertEqual(self.check('// os.Getenv("OLD_EXAMPLE")\n/* env("BLOCK_EXAMPLE", "") */')[0],0)

if __name__ == '__main__': unittest.main()
