import copy
import datetime as dt
import hashlib
import json
import unittest
import test_preflight as fixture_module

CF, p = fixture_module.CF, fixture_module.p


class OfflineCloudflareTests(unittest.TestCase):
    def setUp(self):
        fixture=fixture_module.PreflightTests();fixture.setUp();self.addCleanup(fixture.doCleanups)
        self.cfg,self.driver,self.root=fixture.cfg,fixture.driver,fixture.root

    def run_guard(self): return p.run(self.driver,self.cfg)

    def test_reviewed_local_bytes_are_consumed_without_any_network_command(self):
        result=self.run_guard()
        self.assertEqual(result['status'],'PASS',result)
        proof=result['evidence']['cloudflare']
        self.assertEqual(proof['scope'],'reviewed-offline')
        self.assertIs(proof['current_live_verified'],False)
        self.assertTrue(proof['server_freshness_action'])
        self.assertEqual(proof['document_sha256'],self.cfg['host_preflight']['cloudflare_review']['sha256'])
        self.assertEqual(proof['cidr_count'],3)
        self.assertFalse(any(any(arg.startswith(('http://','https://')) for arg in argv) or argv[0] in ('curl','wget')
                             for _,argv in self.driver.calls))

    def test_missing_or_changed_artifact_fails_before_external_commands(self):
        path=self.root/'cf.json';raw=path.read_bytes()
        for value in (b'{}',raw+b' ',None):
            if value is None:path.unlink()
            else:path.write_bytes(value)
            self.driver.calls.clear()
            result=self.run_guard()
            self.assertEqual(result['status'],'FAIL',result)
            self.assertEqual(self.driver.calls,[])

    def test_review_identity_scope_url_and_time_must_be_explicit_and_valid(self):
        h=self.cfg['host_preflight']; original=copy.deepcopy(h['cloudflare_review'])
        now=dt.datetime.now(dt.timezone.utc)
        changes=[('sha256','0'*64),('reviewed_by',''),('scope','synthetic'),('source_url','https://else.invalid'),
                 ('reviewed_at',now.replace(tzinfo=None).isoformat()),('reviewed_at',(now+dt.timedelta(minutes=1)).isoformat()),
                 ('expires_at',(now-dt.timedelta(minutes=1)).isoformat())]
        for key,value in changes:
            with self.subTest(key=key,value=value):
                h['cloudflare_review']={**original,key:value}
                self.driver.calls.clear(); self.assertEqual(self.run_guard()['status'],'FAIL')
                self.assertEqual(self.driver.calls,[])

    def test_complete_artifact_cannot_be_reduced_to_match_an_incomplete_configuration(self):
        # The reviewed SHA is independent of the later supplied config/body.
        value=copy.deepcopy(CF);value['result']['ipv4_cidrs'].pop()
        (self.root/'cf.json').write_text(json.dumps(value),encoding='utf-8')
        self.assertEqual(self.run_guard()['status'],'FAIL')


if __name__=='__main__':unittest.main()
