"""The real version stdout envelope is input evidence, never rewritten."""
import hashlib
import unittest
from test_preflight import Driver, p


OBSERVED=(
    '2026-09-13T02:19:57.389+0800\tINFO\tstdlog\t'
    'Sub2API 0.2.4 (commit: 5de5e2bed035d43591a2e10e51f420ef6a84eb98, built: 2026-09-09T06:53:25Z)\t'
    '{"service": "sub2api", "env": "bootstrap", "legacy_stdlog": true}\n'
).encode()


class SubVersionOutputTests(unittest.TestCase):
    def run_source(self,raw):
        driver=Driver();driver.replies['source-sub2api-version']=raw
        h={'sources':dict(sub2api='fixture-sub',sub2api_db='fixture-sub-db',newapi='fixture-new',newapi_db='fixture-new-db'),
           'expected_sub2_version':'0.2.4','expected_newapi_tag':'v1.0.0-rc.25'}
        value=p.sources(driver,h,False)
        self.assertEqual(driver.replies['source-sub2api-version'],raw)
        self.assertEqual(driver.calls[-1],('source-sub2api-version',driver.docker+['exec','fixture-sub','/app/sub2api','-version']))
        return value

    def test_actual_native_log_stdout_passes_without_rewriting_command_or_bytes(self):
        self.assertEqual(hashlib.sha256(OBSERVED).hexdigest(),'378bfabbf7375a97231be2ba529ee3b33f606555ccf56a0b8e2642dda418a12c')
        self.assertEqual(self.run_source(OBSERVED)['sub2api_version'],'0.2.4')

    def test_legacy_plain_output_remains_supported(self):
        for raw in (b'Sub2API 0.2.4',b'Sub2API 0.2.4 (fixture)\n',OBSERVED.split(b'\t')[3]+b'\r\n'):
            with self.subTest(raw=raw):self.assertEqual(self.run_source(raw)['sub2api_version'],'0.2.4')

    def test_wrong_version_and_ambiguous_records_are_rejected(self):
        for raw in (OBSERVED.replace(b'0.2.4',b'0.2.40'),OBSERVED.replace(b'0.2.4',b'0.2.5'),
                    OBSERVED+OBSERVED,OBSERVED+b'Sub2API 0.2.4\n',b'Sub2API 0.2.4\n'+OBSERVED,
                    b'prefix Sub2API 0.2.4\n',b''):
            with self.subTest(raw=raw),self.assertRaises(p.PreflightError):self.run_source(raw)

    def test_unknown_logger_level_or_forged_envelope_are_rejected(self):
        for raw in (OBSERVED.replace(b'\tstdlog\t',b'\tunknown\t'),OBSERVED.replace(b'\tINFO\t',b'\tDEBUG\t'),
                    OBSERVED.replace(b'"service": "sub2api"',b'"service": "other"'),
                    OBSERVED.replace(b'"env": "bootstrap"',b'"env": "production"'),
                    OBSERVED.replace(b'"legacy_stdlog": true',b'"legacy_stdlog": 1'),
                    OBSERVED.replace(b'"legacy_stdlog": true',b'"legacy_stdlog": false'),
                    OBSERVED.replace(b'"service": "sub2api"',b'"service": "sub2api", "extra": "fake"'),
                    OBSERVED.replace(b'\t{',b'  {'),OBSERVED.replace(b'Sub2API 0.2.4',b'message: Sub2API 0.2.4')):
            with self.subTest(raw=raw),self.assertRaises(p.PreflightError):self.run_source(raw)

    def test_duplicate_metadata_keys_bad_timestamps_and_native_failure_reject(self):
        for raw in (OBSERVED.replace(b'"service": "sub2api"',b'"service": "other", "service": "sub2api"'),
                    OBSERVED.replace(b'2026-09-13T02:19:57.389+0800',b'2026-99-13T02:19:57.389+0800'),
                    OBSERVED.replace(b'2026-09-13T02:19:57.389+0800',b'not-a-timestamp'),
                    OBSERVED.replace(b'(commit:',b'(claimed-commit:'), (1,OBSERVED)):
            with self.subTest(raw=raw),self.assertRaises(p.PreflightError):self.run_source(raw)


if __name__=='__main__':unittest.main()
