"""Expired server D proves the real business refusal, never fictitious issuance."""
import copy
import hashlib
import json
from pathlib import Path
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import lifecycle
import preview_smoke
import rehearsal_seed
import smoke
import test_smoke
from test_preview_write_smoke import WriteFixture
from test_readiness_phases import full_report

# Real userFundingLotDTOs/AvailableMinor output for one validated wallet;
# only the source-health response overlay differs between these fixtures.
DTO_FIXTURES = {name: json.loads((Path(__file__).parent / 'fixtures' / (name + '-wallet.json')).read_bytes())
                for name in ('source-unavailable', 'active')}


class ExpiredFixture(WriteFixture):
    def __init__(self, fault=None):
        super().__init__(fault)
        self.posted = False
        self.final_lot_read = False
        self.report = full_report(expired=('source_watermark_expired',))

    class Client(WriteFixture.Client):
        def call(self, method, path, payload=None, expected=(200,)):
            f = self.fixture
            if path == '/readyz?report=full':
                f.calls.append((self.role, method, path))
                value = copy.deepcopy(f.report)
                if f.fault == 'missing_check': value['invoice']['checks'].pop('database')
                if f.fault == 'unevaluated': value['invoice']['checks']['database']['status'] = 'not_evaluated'
                if f.fault == 'platform_down': value['modules']['platform']['ready'] = False
                if f.fault == 'future': value['invoice']['source_freshness']['reasons'] = ['source_watermark_future']
                return value
            if path == '/invoice-api/v1/user/funding-lots':
                value = super().call(method, path, payload, expected)
                if self.role == 'sub':
                    lot = value['items'][0]
                    lot.clear()
                    lot.update(copy.deepcopy(DTO_FIXTURES['source-unavailable']))
                    if f.posted:
                        f.final_lot_read = True
                        if f.fault and f.fault.startswith('recovered'):
                            lot.clear()
                            lot.update(copy.deepcopy(DTO_FIXTURES['active']))
                            if f.fault == 'recovered_bad_available': lot['available_minor'] = 100001
                            if f.fault == 'recovered_changed_amount': lot['consumed_cash_minor'] = 99999
                            if f.fault == 'recovered_changed_identity': lot['id'] = test_smoke.NEW_LOT
                            if f.fault == 'recovered_changed_verification': lot['verification'] = 'unverified'
                            if f.fault == 'recovered_unknown_status': lot['eligibility_status'] = 'unknown'
                            if f.fault == 'recovered_bad_reason': lot['reason_code'] = 'SOURCE_NOT_READY'
                            if f.fault == 'recovered_bool_available': lot['available_minor'] = True
                            if f.fault == 'recovered_type_changed': lot['refund_frozen'] = 0
                            if f.fault == 'recovered_unknown_field': lot['unexpected'] = 'private-test-marker'
                    if f.fault == 'wrong_reason': value['items'][0]['reason_code'] = 'LEDGER_FROZEN'
                    if f.fault == 'available': value['items'][0]['available_minor'] = 100000
                return value
            if path == '/invoice-api/v1/user/invoice-requests' and method == 'GET':
                f.calls.append((self.role, method, path))
                return {'items': [{'id': test_smoke.REQUEST, 'source_instance_id': test_smoke.SOURCE, 'source_type': 'sub2api'}] if f.fault == 'new_request' and f.posted and self.role == 'sub' else [], 'has_more': False}
            if path == '/invoice-api/v1/user/invoice-requests' and method == 'POST':
                f.calls.append((self.role, method, path)); f.posted = True
                status = 201 if f.fault == 'accepted' else 503
                smoke.require(status in expected, 'HTTP_STATUS_UNEXPECTED')
                return {'error': {'code': 'OTHER_UNAVAILABLE' if f.fault == 'wrong_503' else 'SOURCE_SYNC_UNAVAILABLE'}}
            return super().call(method, path, payload, expected)


class ExpiredPreviewTests(unittest.TestCase):
    setUp = test_smoke.ExecutionTests.setUp

    def execute_expired(self, fault=None):
        self.config['mode'] = 'server-rehearsal'
        fixture = ExpiredFixture(fault)
        proof = {'owner_id': 'a'*32, 'head': 'b'*40, 'manifest_sha256': 'c'*64, 'runtime_containers': 18}
        output = Path(self.temp.name) / ('output-' + str(len(list(Path(self.temp.name).iterdir())))); output.mkdir()
        budget = lifecycle.ReadinessBudget(output)
        budget.observe(200, json.dumps(full_report(expired=() if fault == 'baseline_fresh' else ('source_watermark_expired',))).encode())
        cfg = {'mode': 'server-rehearsal', 'candidate': {'head': proof['head'], 'manifest_sha256': proof['manifest_sha256']}, 'rehearsal': {'owner_id': proof['owner_id']}}
        driver = SimpleNamespace(config=cfg, output=output, readiness_budget=budget, artifact_preflight=lambda: None)
        seed = rehearsal_seed.Seed.__new__(rehearsal_seed.Seed)
        seed.driver = driver; seed.seeded = True
        snapshot = {'users': {k: {'invoice_requests': 0} for k in ('sub2api', 'newapi')},
                    'lots': {'sub2api': {'id': test_smoke.LOT, 'source_instance_id': test_smoke.SOURCE, 'consumed_cash_minor': 100000,
                            'reserved_minor': 0, 'issued_minor': 0, 'verified_cash_minor': 100000,
                            'verification': 'verified', 'refund_frozen': False, 'eligibility_kind': 'WALLET_CASH'}}}
        snapshot['lots']['newapi'] = {**snapshot['lots']['sub2api'], 'id': test_smoke.NEW_LOT, 'source_instance_id': test_smoke.NEW_SOURCE}
        self.snapshots = []
        def read_snapshot(source_id, lot_id):
            self.assertEqual((source_id, lot_id), (test_smoke.SOURCE, test_smoke.LOT))
            value = copy.deepcopy(snapshot)
            if fault == 'db_changed' and self.snapshots: value['users']['sub2api']['invoice_requests'] = 1
            if fault == 'amount_changed' and self.snapshots: value['lots']['sub2api']['reserved_minor'] = 1
            if fault == 'new_amount_changed' and self.snapshots: value['lots']['newapi']['reserved_minor'] = 1
            if fixture.final_lot_read:
                if fault == 'get_changes_finance': value['lots']['sub2api']['reserved_minor'] = 1
                if fault == 'get_changes_new_finance': value['lots']['newapi']['issued_minor'] = 1
                if fault == 'get_changes_request': value['users']['sub2api']['invoice_requests'] = 1
            self.snapshots.append(value)
            return value
        seed.blocked_invoice_snapshot = read_snapshot
        driver.rehearsal_seed = seed
        if fault == 'budget_tamper':
            budget.value['elapsed_seconds'] = 301
            budget.value['first_accepted_monotonic'] = budget.value['start_monotonic'] + 301
            budget.value['end_monotonic'] = budget.value['first_accepted_monotonic']
            budget.value['observations'][0]['accepted_monotonic'] = budget.value['first_accepted_monotonic']
            budget.save()
        if fault == 'raw_tamper':
            path = output/'candidate-readiness-1.response'; path.write_bytes(path.read_bytes() + b'\n')
        if fault == 'head_changed': cfg['candidate']['head'] = 'd'*40
        permit = preview_smoke.FrozenPermit(preview_smoke._CAPABILITY, self.config, proof)
        permit._driver = None if fault == 'no_attestation' else driver
        if fault == 'local_mode': self.config['mode'] = 'local-synthetic'; permit.configuration_sha256 = preview_smoke.config_hash(self.config)
        with patch.object(preview_smoke, 'PreviewClient', side_effect=lambda *a, **kw: fixture.factory(*a)):
            result = smoke.run(self.config, _preview=permit)
        return result, fixture

    def test_expired_server_proves_503_and_no_invoice_without_claiming_upload(self):
        result, fixture = self.execute_expired()
        self.assertEqual(result['status'], 'PASS', result.get('failure_code'))
        self.assertEqual(result['financial_coverage'], 'blocked-write')
        self.assertEqual(result['document_coverage'], 'not_exercised_source_expired')
        self.assertEqual(tuple(s['name'] for s in result['steps']), preview_smoke.BLOCKED_REQUIRED)
        self.assertEqual(len(self.snapshots), 3)
        self.assertTrue(fixture.posted)
        self.assertFalse(any('/documents/' in p or p.endswith('/review') for _, _, p in fixture.calls))

    def test_expiry_never_accepts_bad_scope_report_money_refusal_or_changed_invoice(self):
        for fault in ('missing_check', 'unevaluated', 'platform_down', 'future', 'wrong_reason', 'available',
                      'accepted', 'wrong_503', 'new_request', 'db_changed', 'amount_changed', 'no_attestation',
                      'budget_tamper', 'raw_tamper', 'head_changed', 'local_mode', 'new_amount_changed'):
            with self.subTest(fault=fault):
                result, fixture = self.execute_expired(fault)
                self.assertEqual(result['status'], 'FAIL', fault)
                self.assertFalse(any('/documents/' in p for _, _, p in fixture.calls), fault)

    def test_fresh_accepted_base_can_expire_before_the_smoke(self):
        result, fixture = self.execute_expired('baseline_fresh')
        self.assertEqual(result['status'], 'PASS', result.get('failure_code'))
        self.assertEqual(result['financial_coverage'], 'blocked-write')
        self.assertTrue(fixture.posted)

    def test_source_recovery_after_refusal_preserves_financial_proof(self):
        result, fixture = self.execute_expired('recovered')
        self.assertEqual(result['status'], 'PASS', result.get('failure_code'))
        self.assertEqual(len(self.snapshots), 3)
        self.assertTrue(fixture.final_lot_read)
        self.assertEqual(result['submission_refusal'], {'http_status': 503, 'code': 'SOURCE_SYNC_UNAVAILABLE'})
        self.assertFalse(result['financial_writes_completed'])
        comparison = result['funding_lot_comparison']
        self.assertEqual(comparison['changed_fields'], ['available_minor', 'eligibility_status', 'reason_code'])
        self.assertTrue(comparison['stable_fields_unchanged'])
        self.assertEqual(comparison['after_projection'], {'eligibility_status': 'active',
            'reason_code': '<absent>', 'available_minor': 100000})
        self.assertTrue(result['financial_snapshot_checks']['after_final_lot_read'])
        self.assertEqual(result['steps'][-1]['name'], 'sessions.revoked')
        self.assertFalse(any('/documents/' in p for _, _, p in fixture.calls))

    def test_final_get_cannot_hide_financial_or_request_changes(self):
        for fault in ('get_changes_finance', 'get_changes_new_finance', 'get_changes_request'):
            with self.subTest(fault=fault):
                result, fixture = self.execute_expired(fault)
                self.assertEqual(result['status'], 'FAIL', fault)
                self.assertTrue(fixture.final_lot_read)
                self.assertEqual(result['failure_code'], 'EXPIRED_PREVIEW_FINANCIAL_STATE_CHANGED_AFTER_READ')
                self.assertFalse(result['financial_snapshot_checks']['after_final_lot_read'])

    def test_recovery_cannot_hide_invalid_projection_identity_or_financial_fields(self):
        for fault in ('recovered_bad_available', 'recovered_changed_amount', 'recovered_changed_identity',
                      'recovered_changed_verification', 'recovered_unknown_status', 'recovered_bad_reason',
                      'recovered_bool_available', 'recovered_type_changed', 'recovered_unknown_field'):
            with self.subTest(fault=fault):
                result, fixture = self.execute_expired(fault)
                self.assertEqual(result['status'], 'FAIL', fault)
                self.assertEqual(result['steps'][-1]['name'], 'sessions.revoked')
                self.assertNotIn('private-test-marker', json.dumps(result))
                if fault == 'recovered_unknown_field':
                    self.assertTrue(result['funding_lot_comparison']['unknown_fields_changed'])
                    self.assertNotIn('unexpected', result['funding_lot_comparison']['changed_fields'])


if __name__ == '__main__': unittest.main()
