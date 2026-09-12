"""A single startup deadline, including real blocking command/HTTP boundaries."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import lifecycle
from test_candidate_start_jobs import config
from test_operator import good_readiness, FakeDriver


def driver(root):
    value = object.__new__(lifecycle.DockerDriver)
    value.config = config(); value.output = Path(root); value.env = os.environ.copy(); value.sequence = 0
    value.config['candidate']['ready_url'] = 'http://127.0.0.1:1/readyz'
    return value


class ReadinessBudgetTests(unittest.TestCase):
    def test_actual_start_deadline_still_reaches_existing_automatic_rollback(self):
        with tempfile.TemporaryDirectory() as root:
            d = FakeDriver(); d.config = config(); d.output = Path(root); d.env = os.environ.copy(); d.sequence = 0
            d.projects = lambda side: d.config[side]['projects']
            d.command = lambda *args, **kwargs: lifecycle.DockerDriver.command(d, *args, **kwargs)
            d.compose = lambda project, name, args: d.command(name, [sys.executable, '-c', 'import time; time.sleep(3)'])
            d.start_new = lambda: lifecycle.DockerDriver.start_new(d)
            with patch.object(lifecycle, 'READINESS_BUDGET_SECONDS', 0.2):
                with self.assertRaises(lifecycle.OperatorError): lifecycle.cutover(d)
            self.assertFalse(d.readiness_budget.active)
            self.assertEqual(d.calls[-5:], ['stop_new', 'restore_permissions', 'start_old', 'check_old', 'restore_nginx'])
            self.assertEqual(d.records[-1]['status'], 'ROLLED_BACK')
            self.assertEqual(d.records[-1]['exit_code'], 1)

    def test_two_projects_share_one_remaining_budget_and_no_reset_at_check(self):
        with tempfile.TemporaryDirectory() as root:
            d = driver(root); clock = [100.0]; calls = []
            def compose(project, name, args):
                calls.append((name, args)); clock[0] += 60
            d.compose = compose; d.inventory = lambda side: clock.__setitem__(0, clock[0] + 61)
            d.http_ready = lambda *args, **kwargs: good_readiness()
            with patch.object(lifecycle.time, 'monotonic', side_effect=lambda: clock[0]):
                d.start_new()
                waits = [int(args[args.index('--wait-timeout') + 1]) for _, args in calls if '--wait' in args]
                self.assertEqual(waits, [180, 120])
                with self.assertRaisesRegex(lifecycle.OperatorError, '300 second'):
                    d.check_new()
            record = json.loads((Path(root) / 'candidate-readiness.json').read_text())
            self.assertEqual(record['status'], 'DEADLINE_EXCEEDED')
            self.assertEqual(record['elapsed_seconds'], 301)

    def test_start_command_is_actually_killed_and_cleanup_has_no_expired_deadline(self):
        with tempfile.TemporaryDirectory() as root:
            d = driver(root)
            d.compose = lambda project, name, args: d.command(name, [sys.executable, '-c', 'import time; time.sleep(3)'])
            started = time.monotonic()
            with patch.object(lifecycle, 'READINESS_BUDGET_SECONDS', 0.2):
                with self.assertRaises(lifecycle.OperatorError): d.start_new()
            self.assertLess(time.monotonic() - started, 2)
            cleanup = d.command('cleanup-owned-fixture', [sys.executable, '-c', 'print("cleaned")'])
            self.assertEqual(cleanup.returncode, 0)
            events = [json.loads(p.read_text()) for p in (Path(root) / 'events').glob('*.json')]
            timed = next(row for row in events if row['operation'] == 'start-new-unified')
            self.assertEqual(timed['exit_code'], 124)
            self.assertTrue(timed['timed_out'])

    def test_true_http_probe_records_original_bytes_and_first_observation(self):
        raw = json.dumps({**good_readiness(), 'invoice': {'ready': True}}, separators=(',', ':')).encode() + b'\n'
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200); self.end_headers(); self.wfile.write(raw)
            def log_message(self, *args): pass
        server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True); thread.start()
        try:
            with tempfile.TemporaryDirectory() as root:
                d = driver(root); d.compose = lambda *args: None; d.inventory = lambda _: []
                d.config['candidate']['ready_url'] = f'http://127.0.0.1:{server.server_port}/readyz'
                d.start_new(); d.check_new()
                record = json.loads((Path(root) / 'candidate-readiness.json').read_text())
                self.assertEqual(record['status'], 'READY')
                self.assertLess(record['elapsed_seconds'], 300)
                self.assertEqual(record['invoice_latches'], 11)
                self.assertEqual(Path(record['observations'][0]['response_path']).read_bytes(), raw)
                self.assertEqual(record['first_all_ready_utc'], record['observations'][0]['observed_utc'])
                self.assertEqual(record['first_all_ready_monotonic'], record['observations'][0]['observed_monotonic'])
        finally:
            server.shutdown(); server.server_close(); thread.join()

    def test_slow_drip_http_cannot_extend_total_budget(self):
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200); self.end_headers()
                try:
                    for _ in range(30): self.wfile.write(b' '); self.wfile.flush(); time.sleep(0.1)
                except OSError: pass
            def log_message(self, *args): pass
        server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True); thread.start()
        try:
            with tempfile.TemporaryDirectory() as root:
                d = driver(root); d.compose = lambda *args: None; d.inventory = lambda _: []
                d.config['candidate']['ready_url'] = f'http://127.0.0.1:{server.server_port}/readyz'
                started = time.monotonic()
                with patch.object(lifecycle, 'READINESS_BUDGET_SECONDS', 0.4):
                    d.start_new()
                    with self.assertRaises(lifecycle.OperatorError): d.check_new()
                self.assertLess(time.monotonic() - started, 2)
                self.assertEqual(json.loads((Path(root) / 'candidate-readiness.json').read_text())['status'], 'DEADLINE_EXCEEDED')
        finally:
            server.shutdown(); server.server_close(); thread.join()

    def test_nominal_budget_is_fixed_and_ready_report_cannot_omit_original_latches(self):
        self.assertEqual(lifecycle.READINESS_BUDGET_SECONDS, 300)
        with tempfile.TemporaryDirectory() as root:
            d = driver(root); d.compose = lambda *args: None; d.inventory = lambda _: []
            with patch.object(lifecycle, 'READINESS_BUDGET_SECONDS', 0.15):
                d.start_new()
                d.command = lambda *args, **kwargs: subprocess.CompletedProcess([], 0, b'200\n' + json.dumps(good_readiness()).encode(), b'')
                with self.assertRaises(lifecycle.OperatorError): d.check_new()
            self.assertNotEqual(json.loads((Path(root) / 'candidate-readiness.json').read_text())['status'], 'READY')


if __name__ == '__main__': unittest.main()
