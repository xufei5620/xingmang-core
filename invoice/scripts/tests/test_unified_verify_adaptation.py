"""Exercise retained unified verify boundaries with local synthetic mutations."""
import argparse
import copy
import importlib.util
from pathlib import Path
import subprocess
import shutil
import sys
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parents[1]
ROOT = Path(__file__).resolve().parents[3]


def load(name):
    spec=importlib.util.spec_from_file_location(name.replace('-','_'),SCRIPTS/(name+'.py'))
    module=importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


contracts=load('verify-unified-contracts')
nginx=load('verify-unified-nginx')


class ComposeBoundaries(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.production,cls.sources,cls.values=contracts.render(ROOT)

    def test_real_synthetic_compose_preserves_all_applicable_guards(self):
        contracts.validate(ROOT,self.production,self.sources,self.values)

    def test_unsafe_configuration_mutations_are_rejected(self):
        mutations=[
            ('policy',lambda p,s:p['services']['platform-api']['environment'].__setitem__('ELIGIBILITY_START_AT','2025-01-01T00:00:00Z')),
            ('freshness',lambda p,s:p['services']['platform-api']['environment'].__setitem__('SOURCE_ECONOMIC_WATERMARK_MAX_STALENESS','1h')),
            ('broad-proxy',lambda p,s:p['services']['platform-api']['environment'].__setitem__('TRUSTED_PROXY_CIDRS','172.30.201.0/24')),
            ('owner-credential',lambda p,s:p['services']['platform-api']['secrets'].append({'source':'invoice_owner_database_url'})),
            ('db-health',lambda p,s:p['services']['invoice-migrate']['depends_on']['invoice-postgres'].__setitem__('condition','service_started')),
            ('scanner-network',lambda p,s:p['services']['pdf-scanner'].__setitem__('network_mode','bridge')),
            ('scanner-writable',lambda p,s:p['services']['pdf-scanner'].__setitem__('read_only',False)),
            ('scanner-secret',lambda p,s:p['services']['pdf-scanner']['secrets'].append({'source':'invoice_owner_database_url'})),
            ('scanner-resource',lambda p,s:p['services']['pdf-scanner'].__setitem__('pids_limit',256)),
            ('clam-signature',lambda p,s:p['services']['clamav']['environment'].__setitem__('CLAMAV_MAX_SIGNATURE_AGE','240h')),
            ('db-network',lambda p,s:p['networks']['invoice_db'].__setitem__('internal',False)),
            ('runtime-permissions',lambda p,s:p['services']['invoice-permissions'].__setitem__('user','0:0')),
            ('floating-image',lambda p,s:p['services']['web'].__setitem__('image','web:latest')),
            ('source-ingress',lambda p,s:s['services']['sub2api-payments']['environment'].__setitem__('INGESTION_ALLOWED_CIDRS','0.0.0.0/0')),
            ('source-reconcile',lambda p,s:s['services']['newapi-identities']['environment'].__setitem__('SOURCE_RECONCILE_FILE','')),
            ('source-cutover',lambda p,s:s['services']['sub2api-usage']['environment'].__setitem__('SOURCE_CUTOVER_MANIFEST_FILE','')),
            ('source-signing-id',lambda p,s:s['services']['newapi-credits']['environment'].__setitem__('SOURCE_SIGNING_KEY_ID','unknown')),
        ]
        for name,mutate in mutations:
            with self.subTest(name=name):
                prod,sources=copy.deepcopy(self.production),copy.deepcopy(self.sources)
                mutate(prod,sources)
                with self.assertRaises(ValueError):
                    contracts.validate(ROOT,prod,sources,self.values)


class NginxBoundaries(unittest.TestCase):
    def test_current_routing_and_each_buffering_regression(self):
        source,_=nginx.validate_source(ROOT)
        for directive in ('proxy_request_buffering off;','proxy_buffering off;'):
            for occurrence in (0,1):
                with self.subTest(directive=directive,origin=occurrence),tempfile.TemporaryDirectory() as folder:
                    pieces=source.split(directive)
                    self.assertEqual(len(pieces),3 if directive.startswith('proxy_buffering') else 4)
                    # Request buffering also appears in the platform webhook route.
                    index=occurrence if directive.startswith('proxy_buffering') else occurrence*2
                    position=sum(len(piece)+len(directive) for piece in pieces[:index])+len(pieces[index])
                    mutated=source[:position]+source[position+len(directive):]
                    path=Path(folder)/'nginx.conf';path.write_text(mutated,encoding='utf-8')
                    with self.assertRaisesRegex(ValueError,'buffering'):
                        nginx.validate_source(ROOT,path)

    def test_exact_headers_reject_missing_duplicate_redirect_and_wrong_csp(self):
        headers='HTTP/1.1 200 OK\r\nX-Content-Type-Options: nosniff\r\nX-Frame-Options: DENY\r\nReferrer-Policy: no-referrer\r\nContent-Security-Policy: '+nginx.CSP+'\r\n'
        nginx.verify_headers(headers,200,True)
        for raw in (headers.replace('X-Content-Type-Options: nosniff\r\n',''),
                    headers+'X-Content-Type-Options: nosniff\r\n',headers.replace('200 OK','302 Found'),
                    headers.replace("frame-ancestors 'none'","frame-ancestors *")):
            with self.subTest(raw=raw),self.assertRaises(ValueError):
                nginx.verify_headers(raw,200,True)


class OperationsDispatcher(unittest.TestCase):
    def test_complete_suite_and_all_failure_modes(self):
        for case in ('pass','missing-preflight','failed-test','skipped-test'):
            with self.subTest(case=case),tempfile.TemporaryDirectory(prefix='unified-operations-tests-') as folder:
                root=Path(folder);tests=root/'deploy/rehearsal-unified/tests';tests.mkdir(parents=True)
                for name in ('operator','restore','smoke','preflight'):
                    if case=='missing-preflight' and name=='preflight':continue
                    body='self.assertTrue(True)'
                    if name=='preflight' and case=='failed-test':body='self.fail("synthetic guard failed")'
                    if name=='preflight' and case=='skipped-test':body='self.skipTest("synthetic incomplete guard")'
                    (tests/f'test_{name}.py').write_text('import unittest\nclass Guard(unittest.TestCase):\n def test_guard(self): '+body+'\n',encoding='utf-8')
                dispatcher=root/'invoice/scripts/test-unified-operations.py';dispatcher.parent.mkdir(parents=True)
                shutil.copyfile(SCRIPTS/'test-unified-operations.py',dispatcher)
                result=subprocess.run([sys.executable,'-X','utf8',str(dispatcher)],capture_output=True,text=True)
                self.assertEqual(result.returncode==0,case=='pass',result.stderr)


if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('--project-root',type=Path,default=ROOT)
    args,rest=parser.parse_known_args();ROOT=args.project_root.resolve()
    unittest.main(argv=[sys.argv[0],*rest],verbosity=2)
