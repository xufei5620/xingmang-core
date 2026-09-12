"""Public nginx inputs and lifecycle consumers; no production daemon is touched."""
import importlib
import hashlib
import json
from pathlib import Path
import sys
import tempfile
import unittest
import types
from unittest.mock import patch
from test_operator import FakeDriver, module

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))


def nginx(test):
    test.assertTrue((Path(__file__).resolve().parents[1] / 'host_nginx.py').is_file(), 'host nginx is absent from the actual operator')
    return importlib.import_module('host_nginx')


def vhost(port=8088, api_port=None):
    api = '' if api_port is None else 'location /api/ { proxy_pass http://127.0.0.1:' + str(api_port) + '; }'
    return 'server { listen 443 ssl; server_name console.example.com; ssl_certificate /public/cert; ssl_certificate_key /private/key; location / { proxy_pass http://127.0.0.1:' + str(port) + '; } ' + api + ' }'


class HostRouteTests(unittest.TestCase):
    def test_more_specific_and_nested_proxy_routes_cannot_keep_the_old_api(self):
        m = nginx(self)
        for directive in (
            'location /invoice-api/v1/admin/invoice-requests/ { proxy_pass http://127.0.0.1:58088; }',
            'location = /invoice-api/v1/admin/invoice-requests/id/issue { proxy_pass http://127.0.0.1:58088; }',
            'location ^~ /api/v1/staff/ { proxy_pass http://127.0.0.1:58088; }',
            'location /other-page/ { proxy_pass http://127.0.0.1:58088; }',
            'location /invoice-api/v1/admin/ { return 503; }',
        ):
            with self.subTest(directive=directive):
                candidate = vhost()[:-1] + directive + ' }'
                with self.assertRaises(m.OperatorError):
                    m.check_routes(candidate, {'admin': 'https://console.example.com'}, {'admin': 8088})
        nested = vhost().replace('location / {', 'location / { location /invoice-api/ { proxy_pass http://127.0.0.1:58088; }')
        with self.assertRaises(m.OperatorError):
            m.check_routes(nested, {'admin': 'https://console.example.com'}, {'admin': 8088})

    def test_specific_new_web_routes_and_unchanged_denials_remain_valid(self):
        m = nginx(self)
        candidate = vhost()[:-1] + ('location /invoice-api/v1/admin/invoice-requests/ { proxy_pass http://127.0.0.1:8088; } '
                                  'location = /private { return 403; } }')
        m.check_routes(candidate, {'admin': 'https://console.example.com'}, {'admin': 8088})

    def test_regex_and_unexamined_server_includes_remain_fail_closed(self):
        m = nginx(self)
        for extra in ('location ~ /invoice-api/ { proxy_pass http://127.0.0.1:8088; }',
                      'include /unexamined/server-routes.conf;'):
            with self.subTest(extra=extra), self.assertRaises(m.OperatorError):
                m.check_routes(vhost()[:-1] + extra + ' }', {'admin': 'https://console.example.com'}, {'admin': 8088})

    def test_actual_api_override_cannot_stay_on_old_backend_port(self):
        m = nginx(self)
        origins = {'admin': 'https://console.example.com'}
        m.check_routes(vhost(), origins, {'admin': 8088})
        for value in (vhost(api_port=58088), vhost().replace('127.0.0.1', '192.0.2.1'), vhost().replace('console.example.com', 'other.example.com')):
            with self.subTest(value=value), self.assertRaises(m.OperatorError):
                m.check_routes(value, origins, {'admin': 8088})

    def test_candidate_cannot_silently_remove_original_tls_security(self):
        m = nginx(self)
        m.only_proxy_changes(vhost(58088), vhost(8088))
        with self.assertRaises(m.OperatorError):
            m.only_proxy_changes(vhost(58088), vhost().replace('ssl_certificate_key /private/key;', ''))

    def test_expanded_nginx_tree_proves_candidate_loaded_and_other_sites_unchanged(self):
        m = nginx(self)
        old = {'/nginx/main.conf': 'events {} http { include /sites/app.conf; include /sites/other.conf; }', '/sites/app.conf': vhost(58088), '/sites/other.conf': 'server { listen 80; server_name unrelated.example.com; return 204; }'}
        new = {'/staged/main.conf': 'events {} http { include /staged/app.conf; include /sites/other.conf; }', '/staged/app.conf': vhost(), '/sites/other.conf': old['/sites/other.conf']}
        m.compare_expanded(old, new, '/nginx/main.conf', '/staged/main.conf', [('/sites/app.conf', '/staged/app.conf')])
        for changed in ({**new, '/sites/app.conf': old['/sites/app.conf']}, {**new, '/sites/other.conf': 'server { return 200; }'}):
            with self.assertRaises(m.OperatorError):
                m.compare_expanded(old, changed, '/nginx/main.conf', '/staged/main.conf', [('/sites/app.conf', '/staged/app.conf')])

    def test_include_scope_order_and_unreviewed_replacement_are_preserved(self):
        m = nginx(self)
        old = {'/main': 'events {} http { include /headers; include /live; server { listen 81; } }',
               '/headers': 'add_header X-Frame-Options DENY always;', '/live': vhost(58088)}
        new = {'/stage': 'events {} http { include /headers; include /candidate; server { listen 81; } }',
               '/headers': old['/headers'], '/candidate': vhost()}
        m.compare_expanded(old, new, '/main', '/stage', [('/live', '/candidate')])
        for main in (
            'events {} http { include /candidate; server { listen 81; include /headers; } }',
            'events {} http { include /candidate; include /headers; server { listen 81; } }',
        ):
            with self.subTest(main=main), self.assertRaises(m.OperatorError):
                m.compare_expanded(old, {**new, '/stage': main}, '/main', '/stage', [('/live', '/candidate')])
        wildcard = {**old, '/main': 'events {} http { include /*; server { listen 81; } }'}
        with self.assertRaises(m.OperatorError):
            m.compare_expanded(wildcard, new, '/main', '/stage', [('/live', '/candidate')])


class HostLifecycleTests(unittest.TestCase):
    def test_switch_is_before_public_smoke_and_rollback_restores_old_route(self):
        m = module(self)
        driver = FakeDriver()
        driver.switch_nginx = lambda snapshot: driver.step('switch_nginx')
        driver.restore_nginx = lambda snapshot: driver.step('restore_nginx')
        m.cutover(driver)
        self.assertIn('switch_nginx', driver.calls)
        self.assertLess(driver.calls.index('check_new'), driver.calls.index('switch_nginx'))
        self.assertLess(driver.calls.index('switch_nginx'), driver.calls.index('smoke'))
        m.rollback(driver, {'actual_invoice_containers': 18})
        self.assertEqual(driver.calls[-1], 'restore_nginx')

    def test_reload_failure_drives_actual_rollback_consumer(self):
        m = module(self)
        driver = FakeDriver('switch_nginx')
        driver.switch_nginx = lambda snapshot: driver.step('switch_nginx')
        driver.restore_nginx = lambda snapshot: driver.step('restore_nginx')
        with self.assertRaises(m.OperatorError):
            m.cutover(driver)
        self.assertEqual(driver.calls[-5:], ['stop_new', 'restore_permissions', 'start_old', 'check_old', 'restore_nginx'])
        self.assertEqual(driver.records[-1]['status'], 'ROLLED_BACK')


class PublicFileConsumerTests(unittest.TestCase):
    def setUp(self):
        self.m = nginx(self)
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.before = self.root / 'live.conf'
        self.after = self.root / 'candidate.conf'
        self.main = self.root / 'nginx.conf'
        self.staged = self.root / 'staged.conf'
        self.before.write_text(vhost(58088) + '\n' + vhost(58089).replace('console.example.com', 'invoice.example.com'), newline='\n')
        self.after.write_text(vhost(8088) + '\n' + vhost(58090).replace('console.example.com', 'invoice.example.com'), newline='\n')
        # Match the simulated nginx -T runtime path byte-for-byte; escaping
        # backslashes matters on this portable Windows test host.
        self.main.write_text('events {} http { include ' + json.dumps(str(self.before)) + '; }')
        self.staged.write_text('events {} http { include ' + json.dumps(str(self.after)) + '; }')
        self.original = self.before.read_bytes()
        smoke = self.root / 'smoke.json'
        smoke.write_text(json.dumps({'origins': {'admin': 'https://console.example.com', 'user': 'https://invoice.example.com'}}))
        config = {'mode': 'server-rehearsal', 'smoke_config': str(smoke), 'candidate': {'projects': [{'kind': 'unified', 'services': {'web': {'role': 'web'}}}]}, 'host_nginx': {
            'main_config': str(self.main), 'staged_main_config': str(self.staged), 'vhosts': [{'live': str(self.before), 'candidate': str(self.after), 'candidate_sha256': hashlib.sha256(self.after.read_bytes()).hexdigest()}],
            'web_ports': {'admin': 8088, 'user': 58090}, 'execution': {'binary': sys.executable, 'prefix': str(self.root), 'unshare_binary': sys.executable}}}
        self.calls = []
        self.fail_reload = False
        self.resolved_ports = [{'target': 80, 'published': '8088', 'host_ip': '127.0.0.1', 'protocol': 'tcp'}, {'target': 8081, 'published': '58090', 'host_ip': '127.0.0.1', 'protocol': 'tcp'}]
        def command(name, argv):
            self.calls.append((name, argv))
            if '-s' in argv:
                if self.fail_reload:
                    self.fail_reload = False
                    raise self.m.OperatorError('synthetic actual reload failure')
                return types.SimpleNamespace(stdout=b'', returncode=0)
            primary = Path(argv[-1])
            nodes = self.m.parse(primary.read_text())
            target = Path(nodes[1][1][0][0][1])
            raw = '\n'.join('# configuration file ' + str(p) + ':\n' + p.read_text() + '\n' for p in (primary, target))
            return types.SimpleNamespace(stdout=raw.encode(), returncode=0)
        def compose(*args):
            return types.SimpleNamespace(stdout=json.dumps({'services': {'web': {'ports': self.resolved_ports}}}).encode(), returncode=0)
        self.driver = types.SimpleNamespace(config=config, output=self.root / 'evidence', command=command, compose=compose)

    def test_preflight_cannot_accept_ports_not_bound_to_actual_new_web(self):
        host = self.m.HostNginx(self.driver)
        host.preflight()
        self.resolved_ports[0]['published'] = '9999'
        with self.assertRaises(self.m.OperatorError):
            host.preflight()

    def test_snapshot_install_failed_reload_and_original_byte_restore(self):
        host = self.m.HostNginx(self.driver)
        snapshot = host.snapshot()
        self.assertEqual(self.before.read_bytes(), self.original, 'preflight/snapshot must not switch live ingress')
        self.assertTrue(all('--net' in argv for name, argv in self.calls))
        self.fail_reload = True
        with self.assertRaises(self.m.OperatorError):
            host.apply(snapshot)
        self.assertEqual(self.before.read_bytes(), self.after.read_bytes())
        self.after.write_text('tampered candidate must not block original recovery')
        recovering = self.m.HostNginx(self.driver, recovering=True)
        with patch.object(recovering, 'probe_restored') as probe:
            recovering.apply(snapshot, rollback=True)
            probe.assert_called_once()
        self.assertEqual(self.before.read_bytes(), self.original)
        self.assertIn('host-nginx-restore-reload', [name for name, _ in self.calls])

    def test_unknown_live_edit_is_preserved_during_rollback(self):
        host = self.m.HostNginx(self.driver)
        snapshot = host.snapshot()
        self.before.write_text('unrelated operator edit')
        with self.assertRaises(self.m.OperatorError):
            host.apply(snapshot, rollback=True)
        self.assertEqual(self.before.read_text(), 'unrelated operator edit')

    def test_staged_syntax_failure_never_writes_live_bytes_or_reloads(self):
        host = self.m.HostNginx(self.driver)
        snapshot = host.snapshot()
        original_info = self.before.stat()
        command = self.driver.command
        self.calls.clear()
        def fail_test(name, argv):
            if name in ('host-nginx-switch-staged-test', 'host-nginx-switched-test'):
                self.calls.append((name, argv))
                raise self.m.OperatorError('synthetic nginx rejects staged syntax')
            return command(name, argv)
        self.driver.command = fail_test
        with patch.object(self.m, 'atomic_public_file', wraps=self.m.atomic_public_file) as install:
            with self.assertRaisesRegex(self.m.OperatorError, 'rejects staged syntax'):
                host.apply(snapshot)
            self.assertEqual(self.before.read_bytes(), self.original)
            self.assertEqual(self.before.stat().st_mode, original_info.st_mode)
            self.assertEqual(self.before.stat().st_mtime_ns, original_info.st_mtime_ns)
            install.assert_not_called()
        self.assertIn('host-nginx-switch-staged-test', [name for name, _ in self.calls])
        self.assertFalse(any('-s' in argv for _, argv in self.calls))

    def test_success_tests_exact_staged_bytes_before_install_and_tests_live_before_reload(self):
        host = self.m.HostNginx(self.driver)
        snapshot = host.snapshot()
        command, install = self.driver.command, self.m.atomic_public_file
        events = []
        def observe_command(name, argv):
            events.append(name)
            if name == 'host-nginx-switch-staged-test':
                self.assertEqual(self.before.read_bytes(), self.original)
                primary = Path(argv[-1])
                self.assertNotIn(primary, (self.main, self.staged))
                target = Path(self.m.parse(primary.read_text())[1][1][0][0][1])
                self.assertEqual(target.read_bytes(), self.after.read_bytes())
                self.assertIn('--net', argv)
            if name == 'host-nginx-switched-test':
                self.assertEqual(self.before.read_bytes(), self.after.read_bytes())
            return command(name, argv)
        def observe_install(path, body, metadata):
            events.append('install')
            return install(path, body, metadata)
        self.driver.command = observe_command
        with patch.object(self.m, 'atomic_public_file', side_effect=observe_install):
            host.apply(snapshot)
        self.assertEqual(events, ['host-nginx-switch-current-test', 'host-nginx-switch-staged-test',
                                  'install', 'host-nginx-switched-test', 'host-nginx-switch-reload'])
        self.assertEqual(list(self.root.glob('unified-nginx-*')), [])

    def test_edits_during_staged_test_are_rejected_before_any_live_install(self):
        host = self.m.HostNginx(self.driver)
        snapshot = host.snapshot()
        command = self.driver.command
        originals = {p: p.read_bytes() for p in (self.after, self.main, self.staged, self.before)}
        for kind in ('candidate', 'main', 'staged-main', 'live', 'temporary-main', 'temporary-vhost', 'same-byte-replacement'):
            with self.subTest(kind=kind):
                def edit_after_test(name, argv):
                    result = command(name, argv)
                    if name == 'host-nginx-switch-staged-test':
                        primary = Path(argv[-1])
                        target = {'candidate': self.after, 'main': self.main, 'staged-main': self.staged,
                                  'live': self.before, 'temporary-main': primary,
                                  'temporary-vhost': Path(self.m.parse(primary.read_text())[1][1][0][0][1]),
                                  'same-byte-replacement': self.after}[kind]
                        if kind == 'same-byte-replacement':
                            alternate = self.root / 'replacement.conf'
                            alternate.write_bytes(target.read_bytes())
                            alternate.replace(target)
                        else:
                            target.write_bytes(target.read_bytes() + b'\n# unrelated concurrent edit\n')
                    return result
                self.driver.command = edit_after_test
                with patch.object(self.m, 'atomic_public_file', wraps=self.m.atomic_public_file) as install:
                    with self.assertRaisesRegex(self.m.OperatorError, 'changed during'):
                        host.apply(snapshot)
                    install.assert_not_called()
                expected = self.original + b'\n# unrelated concurrent edit\n' if kind == 'live' else self.original
                self.assertEqual(self.before.read_bytes(), expected)
                self.assertEqual(list(self.root.glob('unified-nginx-*')), [])
                for path, body in originals.items(): path.write_bytes(body)

    def test_source_changed_between_read_and_hash_is_rejected(self):
        host = self.m.HostNginx(self.driver)
        snapshot = host.snapshot()
        read_bytes = Path.read_bytes
        changed = False
        def concurrent_read(path):
            nonlocal changed
            value = read_bytes(path)
            if path == self.after and not changed:
                changed = True
                path.write_bytes(value + b'\n# edit during read\n')
            return value
        with patch.object(Path, 'read_bytes', concurrent_read), \
             patch.object(self.m, 'atomic_public_file', wraps=self.m.atomic_public_file) as install:
            with self.assertRaisesRegex(self.m.OperatorError, 'changed while being read'):
                host.apply(snapshot)
            install.assert_not_called()
        self.assertEqual(self.before.read_bytes(), self.original)

    def test_configured_staged_main_changed_after_snapshot_is_rejected(self):
        host = self.m.HostNginx(self.driver)
        snapshot = host.snapshot()
        self.staged.write_bytes(self.staged.read_bytes() + b'\n# different reviewed input\n')
        with self.assertRaisesRegex(self.m.OperatorError, 'staged main changed after snapshot'):
            host.apply(snapshot)
        self.assertEqual(self.before.read_bytes(), self.original)

    def test_rollback_stages_backup_when_candidate_and_configured_stage_are_missing(self):
        host = self.m.HostNginx(self.driver)
        snapshot = host.snapshot()
        host.apply(snapshot)
        candidate = self.before.read_bytes()
        self.after.unlink(); self.staged.unlink()
        recovering = self.m.HostNginx(self.driver, recovering=True)
        command = self.driver.command
        def inspect_restore(name, argv):
            if name == 'host-nginx-restore-staged-test':
                self.assertEqual(self.before.read_bytes(), candidate)
                target = Path(self.m.parse(Path(argv[-1]).read_text())[1][1][0][0][1])
                self.assertEqual(target.read_bytes(), self.original)
            return command(name, argv)
        self.driver.command = inspect_restore
        with patch.object(recovering, 'probe_restored') as probe:
            recovering.apply(snapshot, rollback=True)
            probe.assert_called_once()
        self.assertEqual(self.before.read_bytes(), self.original)
        self.assertIn('host-nginx-restore-staged-test', [name for name, _ in self.calls])

    def test_restore_staged_syntax_failure_does_not_modify_live_or_reload(self):
        host = self.m.HostNginx(self.driver)
        snapshot = host.snapshot()
        host.apply(snapshot)
        installed = self.before.read_bytes()
        self.calls.clear()
        command = self.driver.command
        def reject_restore(name, argv):
            if name == 'host-nginx-restore-staged-test':
                raise self.m.OperatorError('synthetic restored syntax rejected')
            return command(name, argv)
        self.driver.command = reject_restore
        with patch.object(self.m, 'atomic_public_file', wraps=self.m.atomic_public_file) as install, \
             patch.object(host, 'probe_restored') as probe:
            with self.assertRaisesRegex(self.m.OperatorError, 'restored syntax rejected'):
                host.apply(snapshot, rollback=True)
            install.assert_not_called(); probe.assert_not_called()
        self.assertEqual(self.before.read_bytes(), installed)
        self.assertFalse(any('-s' in argv for _, argv in self.calls))

    def test_unrepresentable_temporary_main_cleans_only_its_owned_files(self):
        host = self.m.HostNginx(self.driver)
        snapshot = host.snapshot()
        with patch.object(self.m, 'staged_main', side_effect=self.m.OperatorError('include layout rejected')):
            with self.assertRaisesRegex(self.m.OperatorError, 'include layout rejected'):
                host.apply(snapshot)
        self.assertEqual(self.before.read_bytes(), self.original)
        self.assertEqual(list(self.root.glob('unified-nginx-*')), [])

    def test_rollback_probe_requires_the_original_ready_response_not_any_json(self):
        import smoke
        host = self.m.HostNginx(self.driver)
        for body in (b'{}', b'{"status":"unavailable"}', b'{"ready":false,"invoice_ready":false}',
                     b'{"status":"ready","error":"dependency"}', b'{"status":"ready","status":"unavailable"}'):
            client = types.SimpleNamespace(raw=lambda *a, **kw: (body, 'application/json'))
            with self.subTest(body=body), patch.object(smoke, 'Client', return_value=client), \
                 patch.object(self.m.time, 'monotonic', side_effect=[0, 31]), self.assertRaises(self.m.OperatorError):
                host.probe_restored()
            self.assertFalse((self.driver.output / 'host-nginx-rollback-probe.json').exists())

    def test_rollback_probe_accepts_original_platform_and_invoice_contained_ready(self):
        import smoke
        host = self.m.HostNginx(self.driver)
        def client(origin, *a):
            body = b'{"status":"ready"}' if 'console' in origin else b'{"status":"ready","degraded":["source_ingest_dead_events_contained"]}'
            return types.SimpleNamespace(raw=lambda *a, **kw: (body, 'application/json'))
        with patch.object(smoke, 'Client', side_effect=client):
            host.probe_restored()
        proof = json.loads((self.driver.output / 'host-nginx-rollback-probe.json').read_text())
        self.assertEqual(proof['status'], 'PASS')
        self.assertEqual(proof['exit_code'], 0)


if __name__ == '__main__': unittest.main()
