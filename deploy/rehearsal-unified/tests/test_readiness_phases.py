"""The authorized base/freshness phases retain the original cutover rollback."""
from pathlib import Path
import copy
import json
import os
import subprocess
import tempfile
import time
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import lifecycle
from test_operator import FakeDriver
from test_readiness_budget import driver as budget_driver

LATCHES = ('database', 'admin_settings', 'invoice_issuer', 'clamav_daemon',
           'clamav_signatures', 'pdf_scanner', 'source_health_query', 'source_ingest',
           'eligibility_health_query', 'eligibility_projection', 'source_streams')


def full_report(*, expired=(), legacy_ready=None):
    fresh = not expired
    legacy_ready = fresh if legacy_ready is None else legacy_ready
    invoice = {'ready': legacy_ready, 'checks': {name: {'status': 'ready'} for name in LATCHES},
               'source_non_freshness_status': 'ready',
               'source_freshness': {'status': 'ready' if fresh else 'not_ready', 'reasons': list(expired)}}
    if not legacy_ready:
        invoice['check'] = 'source_streams'
        invoice['checks']['source_streams']['status'] = 'not_ready'
    return {'report_schema': 'xingmang.readiness-evaluation/v1',
            'status': 'ready' if legacy_ready else 'unavailable',
            'platform_ready': True, 'invoice_ready': legacy_ready, 'invoice': invoice,
            'modules': {'platform': {'ready': True, 'status': 'ready'},
                        'invoice_sources': {'ready': legacy_ready, 'status': 'ready' if legacy_ready else 'not_ready'},
                        'invoice_projection': {'ready': True, 'status': 'ready'}}}


class ReadinessPolicyTests(unittest.TestCase):
    def test_base_accepts_only_explicit_heartbeat_and_watermark_expiry(self):
        for reasons in ((), ('source_heartbeat_expired',), ('source_watermark_expired',),
                        ('source_heartbeat_expired', 'source_watermark_expired')):
            with self.subTest(reasons=reasons):
                result = lifecycle.require_readiness_report(full_report(expired=reasons), require_freshness=False)
                self.assertEqual(result['expected_source_expiry'], list(reasons))

    def test_base_rejects_future_missing_unknown_and_inconsistent_reason_reports(self):
        for reasons in (('source_heartbeat_future',), ('source_watermark_future',),
                        ('source_heartbeat_missing',), ('source_watermark_missing',),
                        ('STREAM_STALE',), ('source_database_expired',),
                        ('source_heartbeat_expired', 'source_heartbeat_expired')):
            with self.subTest(reasons=reasons), self.assertRaises(lifecycle.OperatorError):
                lifecycle.require_readiness_report(full_report(expired=reasons), require_freshness=False)
        payload = full_report(expired=('source_heartbeat_expired',))
        payload['invoice']['source_freshness']['reasons'] = []
        with self.assertRaises(lifecycle.OperatorError):
            lifecycle.require_readiness_report(payload, require_freshness=False)

    def test_expiry_never_hides_missing_or_unevaluated_base_latches_or_modules(self):
        base = full_report(expired=('source_watermark_expired',))
        for name in LATCHES:
            states = ('not_evaluated', 'unknown') + (('not_ready',) if name != 'source_streams' else ())
            for state in states:
                payload = copy.deepcopy(base); payload['invoice']['checks'][name]['status'] = state
                with self.subTest(latch=name,state=state), self.assertRaises(lifecycle.OperatorError):
                    lifecycle.require_readiness_report(payload, require_freshness=False)
            payload = copy.deepcopy(base); del payload['invoice']['checks'][name]
            with self.subTest(missing=name), self.assertRaises(lifecycle.OperatorError):
                lifecycle.require_readiness_report(payload, require_freshness=False)
        for name in ('platform','invoice_sources','invoice_projection'):
            payload = copy.deepcopy(base); payload['modules'][name]['status'] = 'not_evaluated'
            with self.subTest(module=name), self.assertRaises(lifecycle.OperatorError):
                lifecycle.require_readiness_report(payload, require_freshness=False)

    def test_expiry_cannot_hide_nonfreshness_dead_or_unknown_schema(self):
        for mutate in (lambda p:p['invoice'].update(source_non_freshness_status='not_ready'),
                       lambda p:p['invoice'].update(check='source_stream_dead_events'),
                       lambda p:p.update(report_schema='unknown'),
                       lambda p:p['invoice']['checks'].update(extra={'status':'ready'})):
            payload = full_report(expired=('source_heartbeat_expired',)); mutate(payload)
            with self.assertRaises(lifecycle.OperatorError):
                lifecycle.require_readiness_report(payload, require_freshness=False)

    def test_full_freshness_does_not_accept_original_rescan_grace_readiness(self):
        payload = full_report(expired=('source_watermark_expired',), legacy_ready=True)
        lifecycle.require_readiness_report(payload, require_freshness=False)
        with self.assertRaises(lifecycle.OperatorError):
            lifecycle.require_readiness_report(payload, require_freshness=True)
        lifecycle.require_readiness_report(full_report(), require_freshness=True)


class PhasedDriver(FakeDriver):
    def wait_source_freshness(self): self.step('wait_source_freshness')


class ReadinessPhaseFlowTests(unittest.TestCase):
    def test_phase_audit_write_failure_cannot_block_original_rollback(self):
        with tempfile.TemporaryDirectory() as root:
            driver=PhasedDriver('check_new')
            driver.readiness_budget=lifecycle.ReadinessBudget(Path(root))
            with patch.object(driver.readiness_budget,'save',side_effect=OSError('synthetic audit full')):
                with self.assertRaises(lifecycle.OperatorError):lifecycle.cutover(driver)
            self.assertEqual(driver.calls[-5:],['stop_new','restore_permissions','start_old','check_old','restore_nginx'])
            self.assertFalse(driver.readiness_budget.active)

    def test_E_may_switch_after_base_but_commits_only_after_freshness_and_smoke(self):
        driver = PhasedDriver()
        result = lifecycle.cutover(driver)
        self.assertEqual(result['status'], 'COMMITTED')
        self.assertEqual(driver.calls[-4:], ['check_new', 'switch_nginx', 'wait_source_freshness', 'smoke'])

    def test_E_freshness_failure_restores_original_stack_and_nginx_without_retry(self):
        driver = PhasedDriver('wait_source_freshness')
        with self.assertRaises(lifecycle.OperatorError):
            lifecycle.cutover(driver)
        self.assertEqual(driver.calls.count('switch_nginx'), 1)
        self.assertEqual(driver.calls.count('wait_source_freshness'), 1)
        self.assertNotIn('smoke', driver.calls)
        self.assertEqual(driver.calls[-5:], ['stop_new', 'restore_permissions', 'start_old', 'check_old', 'restore_nginx'])
        self.assertEqual(driver.records[-1]['status'], 'ROLLED_BACK')
        self.assertEqual(driver.records[-1]['exit_code'], 1)


class ReadinessPhaseBudgetTests(unittest.TestCase):
    def test_mode_never_authorizes_an_unattested_private_ready_target(self):
        with tempfile.TemporaryDirectory() as root:
            driver=budget_driver(root)
            for mode in ('production','server-rehearsal','local-synthetic'):
                driver.config['mode']=mode
                with self.subTest(mode=mode), self.assertRaises(lifecycle.OperatorError):
                    driver.http_ready('http://192.168.240.34:8081/readyz',all_modules=True)

    def test_frozen_identity_drift_after_HTTP_is_fatal_without_retry_or_acceptance(self):
        with tempfile.TemporaryDirectory() as root:
            driver=budget_driver(root);driver.compose=lambda *args:None;driver.inventory=lambda *args,**kwargs:[]
            driver.start_new();driver.config['candidate']['ready_url']='http://192.168.240.34:8081/readyz';calls=[]
            class Scope:
                def __init__(self):self.calls=0
                def assert_target(self, actual, url):
                    self.calls+=1
                    if self.calls==3:raise lifecycle.OperatorError('synthetic frozen identity changed')
            scope=Scope();driver._frozen_ready_endpoint=scope
            def probe(*args,**kwargs):
                calls.append(args)
                return subprocess.CompletedProcess([],0,b'200\n'+json.dumps(full_report()).encode(),b'')
            driver.command=probe
            with self.assertRaisesRegex(lifecycle.OperatorError,'frozen identity changed'):driver.check_new()
            self.assertEqual(len(calls),1)
            self.assertEqual(scope.calls,3)
            self.assertFalse(driver.readiness_budget.active)
            self.assertEqual(driver.readiness_budget.value['observations'],[])

    def test_phase_elapsed_includes_final_container_health_attestation(self):
        with tempfile.TemporaryDirectory() as root:
            clock=[0.0]
            with patch.object(lifecycle.time,'monotonic',side_effect=lambda:clock[0]):
                budget=lifecycle.ReadinessBudget(Path(root),phase='source_freshness')
                clock[0]=10.0
                budget.observe(200,json.dumps(full_report()).encode(),before_accept=lambda:clock.__setitem__(0,20.0))
                self.assertEqual(budget.value['elapsed_seconds'],20.0)
                self.assertEqual(budget.value['observations'][0]['observed_monotonic'],10.0)
                self.assertEqual(budget.value['observations'][0]['accepted_monotonic'],20.0)

    def test_health_deferral_is_only_the_known_running_API_in_its_base_phase(self):
        with tempfile.TemporaryDirectory() as root:
            driver=budget_driver(root); image='sha256:'+'a'*64
            project={'name':'candidate-unified','kind':'unified','services':{
                'api':{'role':'platform-api','image_id':image},'clamav':{'role':'clamav','image_id':image}}}
            driver.config['candidate']['projects']=[project];driver.docker=['docker']
            health={'api':'unhealthy','clamav':'healthy'}
            def compose(project,name,args):
                body=b'api\nclamav\n' if '--services' in args else args[-1].encode()
                return subprocess.CompletedProcess(args,0,body,b'')
            def command(name,args):
                service=args[2]
                body={'image':image,'running':True,'exit_code':0,'health':health[service],
                      'project':project['name'],'service':service,'ports':{},'mounts':[]}
                return subprocess.CompletedProcess(args,0,json.dumps(body).encode(),b'')
            driver.compose=compose;driver.command=command
            with self.assertRaises(lifecycle.OperatorError):driver.inventory('candidate',allow_deferred_invoice_health=True)
            driver.readiness_budget=lifecycle.ReadinessBudget(Path(root))
            self.assertEqual(len(driver.inventory('candidate',allow_deferred_invoice_health=True)),2)
            with self.assertRaises(lifecycle.OperatorError):driver.inventory('candidate')
            health['clamav']='unhealthy'
            with self.assertRaises(lifecycle.OperatorError):driver.inventory('candidate',allow_deferred_invoice_health=True)

    def test_source_deadline_starts_once_before_connect_and_is_not_reset_after_base(self):
        with tempfile.TemporaryDirectory() as root:
            driver = budget_driver(root); clock = [100.0]; trigger = []
            def compose(project, name, args):
                if name == 'start-new-sources':
                    trigger.append(driver.source_freshness_budget.started)
                    self.assertEqual(driver.source_freshness_budget.value['trigger'], 'source_collectors_start_requested')
                clock[0] += 50
            driver.compose = compose
            driver.inventory = lambda side, **kwargs: []
            with patch.object(lifecycle.time, 'monotonic', side_effect=lambda: clock[0]):
                driver.start_new()
                self.assertEqual(trigger, [150.0])
                deadline = driver.source_freshness_budget.deadline
                driver.readiness_budget.observe(200, json.dumps(full_report(expired=('source_heartbeat_expired',))).encode())
                self.assertEqual(driver.readiness_budget.value['status'], 'BASE_READY')
                self.assertEqual(deadline, 1050.0)
                driver.http_ready = lambda *args, **kwargs: driver.source_freshness_budget.remaining()
                clock[0] = deadline + 0.01
                with self.assertRaises(lifecycle.ReadinessTimeout): driver.wait_source_freshness()
                self.assertEqual(driver.source_freshness_budget.deadline, deadline)
                self.assertFalse(driver.source_freshness_budget.active)
                with self.assertRaises(lifecycle.OperatorError): driver.wait_source_freshness()

    def test_rehearsal_has_no_freshness_extension_and_does_not_wait_on_strict_API_health(self):
        with tempfile.TemporaryDirectory() as root:
            driver = budget_driver(root); driver.config['mode'] = 'server-rehearsal'; calls=[]
            driver.compose = lambda project, name, args: calls.append((name,args))
            driver.start_new(rehearsal=True)
            self.assertFalse(hasattr(driver, 'source_freshness_budget'))
            self.assertEqual(driver.readiness_budget.value['budget_seconds'], 300)
            wait = next(args for name,args in calls if name=='wait-new-unified')
            self.assertNotIn('platform-api', wait)
            self.assertIn('ingest-proxy', wait)

    def test_HTTP_failure_never_becomes_a_valid_expired_source_sample(self):
        for status in (301, 500, 503):
            with self.subTest(status=status), tempfile.TemporaryDirectory() as root:
                budget = lifecycle.ReadinessBudget(Path(root))
                with self.assertRaises(lifecycle.OperatorError):
                    budget.observe(status, json.dumps(full_report(expired=('source_watermark_expired',))).encode())
                self.assertTrue(budget.active)
                self.assertNotIn('first_accepted_utc', budget.value)

    def test_freshness_deadline_kills_actual_probe_and_does_not_block_cleanup(self):
        with tempfile.TemporaryDirectory() as root:
            driver = budget_driver(root); driver.compose=lambda *args:None; driver.inventory=lambda *args,**kwargs:[]
            with patch.object(lifecycle, 'SOURCE_FRESHNESS_BUDGET_SECONDS', 0.2), patch.object(lifecycle, 'READINESS_PROBE', 'import time; time.sleep(3)'):
                driver.start_new()
                driver.readiness_budget.observe(200,json.dumps(full_report(expired=('source_heartbeat_expired',))).encode())
                began=time.monotonic()
                with self.assertRaises(lifecycle.ReadinessTimeout):driver.wait_source_freshness()
                self.assertLess(time.monotonic()-began,2)
            result=driver.command('owned-cleanup-fixture',[sys.executable,'-c','print("cleaned")'])
            self.assertEqual(result.returncode,0)
            events=[json.loads(p.read_text()) for p in (Path(root)/'events').glob('*.json')]
            failure=next(event for event in events if event['operation']=='source-freshness-probe')
            self.assertEqual(failure['exit_code'],124)
            self.assertTrue(failure['timed_out'])
            self.assertEqual(driver.source_freshness_budget.value['status'],'DEADLINE_EXCEEDED')


if __name__ == '__main__': unittest.main()
