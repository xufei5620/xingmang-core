"""Go timestamp validation must work on Python 3.10 without changing uploads."""
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
sys.path.insert(0, str(Path(__file__).resolve().parent))
import preview_smoke
import test_preview_write_smoke as write_smoke
import test_smoke


class TimestampCompatibilityTests(unittest.TestCase):
    setUp = test_smoke.ExecutionTests.setUp
    execute_preview = write_smoke.PreviewContractTests.execute_preview

    def assert_complete_round_trip(self, stamp):
        self.config['mode'] = 'server-rehearsal'
        result, fixture = self.execute_preview(issued_at=stamp)
        self.assertEqual(result['status'], 'PASS', (stamp, result.get('failure_code'), result.get('failure_type')))
        self.assertEqual(tuple(s['name'] for s in result['steps']), preview_smoke.REQUIRED)
        self.assertEqual(result['financial_coverage'], 'full-write')
        self.assertEqual(result['document_coverage'], 'scan-upload-both-downloads')
        self.assertEqual(fixture.uploaded_issued_at, stamp.encode('ascii'), 'server timestamp changed in multipart upload')
        downloads = [role for role, method, path in fixture.calls
                     if method == 'GET' and path.endswith('/document') and role != 'new']
        self.assertEqual(downloads, ['sub', 'admin'])
        self.assertEqual(sum(path.endswith('/logout') for _, _, path in fixture.calls), 3)

    def test_go_nanosecond_timestamp_completes_unchanged_upload_and_downloads(self):
        self.assert_complete_round_trip('2026-09-12T01:02:03.123456789Z')

    def test_all_zero_to_nine_fraction_lengths_and_offsets_complete_unchanged(self):
        for digits in range(10):
            fraction = '.' + '123456789'[:digits] if digits else ''
            for offset in ('Z', '+00:00', '-00:00', '+08:00', '-05:30', '+23:59'):
                stamp = '2026-09-12T01:02:03' + fraction + offset
                with self.subTest(stamp=stamp):
                    self.assert_complete_round_trip(stamp)

    def test_fraction_trailing_zeros_and_leap_day_remain_byte_exact(self):
        for stamp in ('2024-02-29T23:59:59.120000000Z',
                      '2024-02-29T23:59:59.000000000+08:00',
                      '2026-09-12T01:02:03.123456001-03:30'):
            with self.subTest(stamp=stamp):
                self.assert_complete_round_trip(stamp)

    def test_malformed_timestamp_never_uploads_and_revokes_sessions(self):
        invalid = (
            '2026-09-12T01:02:03', '2026-09-12', '2026-09-12 01:02:03Z',
            '2026-9-12T01:02:03Z', '2026-09-12t01:02:03z',
            '2026-09-12T01:02:03.Z', '2026-09-12T01:02:03.1234567890Z',
            '2026-09-12T01:02:03,123Z', '2026-09-12T01:02:03+24:00',
            '2026-09-12T01:02:03+00:60', '2026-09-12T01:02:03+01:02:03',
            '2026-09-12T01:02:03+0800', '2026-09-12T01:02:03Z\n',
            '2026-09-12T01:02:03Z\r\nInjected: value',
            '0000-01-01T00:00:00Z', '2025-02-29T00:00:00Z',
            '2026-04-31T00:00:00Z', '2026-13-01T00:00:00Z',
            '2026-09-12T24:00:00Z', '2026-09-12T01:60:00Z',
            '2026-09-12T01:02:60Z', '２０２６-09-12T01:02:03Z',
        )
        for stamp in invalid:
            with self.subTest(stamp=stamp):
                result, fixture = self.execute_preview(issued_at=stamp)
                self.assertEqual(result['status'], 'FAIL')
                self.assertEqual(result['failure_code'], 'SERVER_ISSUED_TIME_INVALID')
                self.assertFalse(any(path.endswith('/documents/upload') for _, _, path in fixture.calls))
                self.assertEqual(sum(path.endswith('/logout') for _, _, path in fixture.calls), 3)
                self.assertEqual(result['steps'][-1]['name'], 'sessions.revoked')

    def test_missing_timestamp_never_uploads_and_revokes_sessions(self):
        for stamp in (None, '', 123, True, [], {}):
            with self.subTest(stamp=stamp):
                result, fixture = self.execute_preview(issued_at=stamp)
                self.assertEqual(result['status'], 'FAIL')
                self.assertEqual(result['failure_code'], 'SERVER_ISSUED_TIME_MISSING')
                self.assertFalse(any(path.endswith('/documents/upload') for _, _, path in fixture.calls))
                self.assertEqual(sum(path.endswith('/logout') for _, _, path in fixture.calls), 3)


if __name__ == '__main__':
    unittest.main()
