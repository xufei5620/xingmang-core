"""Only an actual owned frozen web endpoint can authorize private readiness HTTP."""
import copy
import importlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import lifecycle

try:
    endpoint = importlib.import_module('rehearsal_endpoint')
except ModuleNotFoundError as error:
    if error.name != 'rehearsal_endpoint':
        raise
    endpoint = None

OWNER = 'a' * 32
HEAD = 'b' * 40
CID = 'c' * 64
NETWORK_ID = 'd' * 64
ENDPOINT_ID = 'e' * 64
IMAGE = 'sha256:' + 'f' * 64
IP = '192.168.240.34'
URL = 'http://' + IP + ':8081/readyz'
PROJECT = 'xm-rehearsal-test-unified'
NETWORK = PROJECT + '-invoice_proxy'


class Driver:
    def __init__(self, root):
        self.state = root / 'state'
        self.output = root / 'invocation'
        self.state.mkdir()
        self.output.mkdir()
        self.docker = ['docker', '--context', 'owned-local']
        projects = [
            {'kind': 'unified', 'name': PROJECT, 'compose_files': [str(root / 'd.json'), str(self.output / 'ownership.json')],
             'env_file': str(root / 'empty.env'), 'services': {'web': {'role': 'web', 'image_id': IMAGE}}},
            {'kind': 'sources', 'name': 'xm-rehearsal-test-sources', 'compose_files': [str(root / 'sources.json')],
             'env_file': str(root / 'empty.env'), 'services': {'stream': {'role': 'sub2api-usage', 'image_id': IMAGE}}},
        ]
        self.value = {'owner_id': OWNER, 'projects': projects, 'ready_url': URL,
                      'volumes': {name: 'xm-rehearsal-test-' + name for name in
                                  ('platform_database', 'invoice_database', 'documents', 'source_state', 'invoice_metadata', 'platform_metadata')}}
        self.config = {'mode': 'server-rehearsal', 'candidate': {'head': HEAD, 'projects': projects, 'ready_url': URL},
                       'previous': {'projects': [{'name': 'production-platform'}, {'name': 'production-invoice'}]},
                       'rehearsal': {'projects': copy.deepcopy(projects)}}
        # The original rehearsal config need not contain generated ownership or
        # seed overlays; the controlled restore's returned value does.
        self.config['rehearsal']['projects'][0]['compose_files'].pop()
        self.resolved = {'services': {'web': {'image': IMAGE, 'networks': {'invoice_proxy': {'ipv4_address': IP}}}},
                         'networks': {'invoice_proxy': {'name': NETWORK, 'internal': True, 'driver': 'bridge',
                             'ipam': {'config': [{'subnet': '192.168.240.32/27', 'gateway': '192.168.240.33', 'ip_range': '192.168.240.48/28'}]}}}}
        self.ids = CID + '\n'
        self.container = {'id': CID, 'image': IMAGE, 'running': True, 'project': PROJECT, 'service': 'web', 'owner': OWNER,
                          'networks': {NETWORK: {'NetworkID': NETWORK_ID, 'EndpointID': ENDPOINT_ID,
                              'IPAddress': IP, 'IPPrefixLen': 27, 'Gateway': ''}}}
        self.network = {'id': NETWORK_ID, 'name': NETWORK, 'driver': 'bridge', 'internal': True,
                        'project': PROJECT, 'owner': OWNER,
                        'ipam': [{'Subnet': '192.168.240.32/27', 'Gateway': '192.168.240.33', 'IPRange': '192.168.240.48/28'}],
                        'containers': {CID: {'EndpointID': ENDPOINT_ID, 'IPv4Address': IP + '/27'}}}
        self.calls = []
        self.write_journal()

    def write_journal(self, **changes):
        value = {'schema': 'xingmang.rehearsal.ownership/v1', 'owner_id': OWNER, 'invocation': str(self.output),
                 'source_head': HEAD, 'projects': [p['name'] for p in self.value['projects']], 'volumes': self.value['volumes']}
        value.update(changes)
        (self.state / 'rehearsal-ownership.json').write_text(json.dumps(value), encoding='utf-8')

    def projects(self, side):
        return self.config[side]['projects']

    def compose(self, project, name, args, **kwargs):
        self.calls.append(('compose', project['name'], args))
        if args == ['config', '--format', 'json']:
            raw = json.dumps(self.resolved).encode()
        elif args == ['ps', '--all', '-q', 'web']:
            raw = self.ids.encode()
        else:
            raise AssertionError('unexpected Compose command: ' + repr(args))
        return subprocess.CompletedProcess(args, 0, raw, b'')

    def command(self, name, args, **kwargs):
        self.calls.append(('docker', name, args))
        prefix = len(self.docker)
        if args[prefix:prefix + 3] == ['inspect', '--type', 'container']:
            assert args[prefix + 3] == CID
            raw = self.container
        elif args[prefix:prefix + 2] == ['network', 'inspect']:
            assert args[prefix + 2] == NETWORK_ID
            raw = self.network
        else:
            raise AssertionError('non-read-only or unexpected Docker command')
        return subprocess.CompletedProcess(args, 0, json.dumps(raw).encode(), b'')


class FrozenReadyEndpointTests(unittest.TestCase):
    def setUp(self):
        self.assertIsNotNone(endpoint, 'the frozen endpoint implementation is missing')
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.driver = Driver(Path(self.temp.name))

    def issue(self):
        handle = endpoint.attest_frozen_ready_endpoint(self.driver, self.driver.value)
        self.driver._frozen_ready_endpoint = handle
        return handle

    def test_owned_web_succeeds_for_both_exact_paths_and_each_valid_mode(self):
        for mode in ('server-rehearsal', 'production', 'local-synthetic'):
            with self.subTest(mode=mode):
                self.driver.config['mode'] = mode
                handle = self.issue()
                before = len(self.driver.calls)
                self.assertEqual(handle.assert_target(self.driver, URL), URL)
                self.assertEqual(handle.assert_target(self.driver, URL + '?report=full'), URL + '?report=full')
                inspections = self.driver.calls[before:]
                self.assertGreaterEqual(sum(row[0] == 'docker' for row in inspections), 4)
                with self.assertRaises(TypeError):
                    json.dumps(handle)

    def test_loopback_retains_the_existing_path_without_requiring_a_journal(self):
        (self.driver.state / 'rehearsal-ownership.json').unlink()
        for value in ('http://127.0.0.1:58098/readyz', 'http://localhost:58098/readyz', 'http://[::1]:58098/readyz'):
            self.driver.value['ready_url'] = value
            self.driver.config['candidate']['ready_url'] = value
            self.assertIsNone(endpoint.attest_frozen_ready_endpoint(self.driver, self.driver.value))
        self.assertEqual(self.driver.calls, [])

    def test_mode_or_an_unproven_private_url_does_not_authorize_any_docker_lookup(self):
        (self.driver.state / 'rehearsal-ownership.json').unlink()
        with self.assertRaises(lifecycle.OperatorError):
            self.issue()
        self.assertEqual(self.driver.calls, [])
        self.driver.write_journal()
        self.driver.config['mode'] = 'frozen-trusted'
        with self.assertRaises(lifecycle.OperatorError):
            self.issue()
        self.assertEqual(self.driver.calls, [])

    def test_foreign_or_incomplete_ownership_journal_is_rejected_before_commands(self):
        for key, value in [('schema', 'invented'), ('owner_id', '0' * 32), ('invocation', '/previous-run'),
                           ('source_head', '0' * 40), ('projects', ['production-platform']), ('volumes', {})]:
            with self.subTest(key=key):
                self.driver.write_journal(**{key: value})
                with self.assertRaises(lifecycle.OperatorError):
                    self.issue()
                self.assertEqual(self.driver.calls, [])

    def test_candidate_must_match_the_frozen_value_and_be_disjoint_from_originals(self):
        original = copy.deepcopy(self.driver.config['candidate']['projects'])
        for bad in ([{**original[0], 'name': 'production-platform'}, original[1]], original[:1]):
            self.driver.config['candidate']['projects'] = bad
            with self.assertRaises(lifecycle.OperatorError):
                self.issue()
        self.driver.config['candidate']['projects'] = self.driver.value['projects']
        self.driver.config['previous']['projects'].append({'name': PROJECT})
        with self.assertRaises(lifecycle.OperatorError):
            self.issue()
        self.assertEqual(self.driver.calls, [])

    def test_exact_url_is_required_even_for_other_private_or_loopback_addresses(self):
        handle = self.issue()
        for wrong in (URL.replace(IP, '192.168.240.35'), URL.replace(IP, '127.0.0.1'), URL.replace('8081', '8080'),
                      URL.replace('/readyz', '/healthz'), URL.replace('http:', 'https:'), URL + '?report=brief',
                      URL + '?report=full&extra=1', URL + '#fragment', URL.replace(IP, 'user@' + IP)):
            with self.subTest(url=wrong), self.assertRaises(lifecycle.OperatorError):
                handle.assert_target(self.driver, wrong)

    def test_attestation_rejects_nonprivate_addresses_and_noncanonical_target_forms(self):
        for wrong in (URL.replace(IP, '8.8.8.8'), URL.replace(IP, '169.254.1.2'), URL.replace(IP, '0.0.0.0'),
                      URL.replace(IP, '[fd00::1]'), URL + '?report=full', URL + '#', URL.replace('8081', '08081')):
            with self.subTest(url=wrong):
                self.driver.value['ready_url'] = wrong
                self.driver.config['candidate']['ready_url'] = wrong
                with self.assertRaises(lifecycle.OperatorError):
                    self.issue()
        self.assertEqual(self.driver.calls, [])

    def test_native_compose_must_pin_the_one_owned_network_and_static_web_address(self):
        original = copy.deepcopy(self.driver.resolved)
        changes = [lambda v: v['services']['web']['networks']['invoice_proxy'].pop('ipv4_address'),
                   lambda v: v['services']['web']['networks']['invoice_proxy'].update(ipv4_address='192.168.240.35'),
                   lambda v: v['services']['web'].update(image='sha256:' + '0' * 64),
                   lambda v: v['services']['web']['networks'].update(extra={}),
                   lambda v: v['networks']['invoice_proxy'].update(internal=False),
                   lambda v: v['networks']['invoice_proxy'].update(external=True)]
        for change in changes:
            self.driver.resolved = copy.deepcopy(original)
            change(self.driver.resolved)
            with self.subTest(change=change), self.assertRaises(lifecycle.OperatorError):
                self.issue()

    def test_container_id_image_labels_running_state_and_networks_are_all_bound(self):
        original = copy.deepcopy(self.driver.container)
        for key, wrong in [('id', '0' * 64), ('image', 'sha256:' + '0' * 64), ('project', 'production-platform'),
                           ('service', 'other-web'), ('owner', '0' * 32), ('running', False)]:
            self.driver.container = {**copy.deepcopy(original), key: wrong}
            with self.subTest(key=key), self.assertRaises(lifecycle.OperatorError):
                self.issue()
        self.driver.container = copy.deepcopy(original)
        self.driver.container['networks']['production-extra'] = {}
        with self.assertRaises(lifecycle.OperatorError):
            self.issue()
        self.driver.container = original
        self.driver.ids = CID + '\n' + '0' * 64 + '\n'
        with self.assertRaises(lifecycle.OperatorError):
            self.issue()

    def test_capability_rechecks_live_network_and_endpoint_before_every_request(self):
        original = copy.deepcopy(self.driver.network)
        changes = [lambda v: v.update(internal=False), lambda v: v.update(id='0' * 64),
                   lambda v: v.update(owner='0' * 32), lambda v: v.update(project='production-platform'),
                   lambda v: v.update(name='different-network'), lambda v: v['containers'].clear(),
                   lambda v: v['containers'][CID].update(EndpointID='0' * 64),
                   lambda v: v['containers'][CID].update(IPv4Address='192.168.240.35/27')]
        for change in changes:
            self.driver.network = copy.deepcopy(original)
            handle = self.issue()
            change(self.driver.network)
            with self.subTest(change=change), self.assertRaises(lifecycle.OperatorError):
                handle.assert_target(self.driver, URL + '?report=full')

    def test_capability_rechecks_container_and_resolved_ip_instead_of_caching_authority(self):
        handle = self.issue()
        self.driver.container['image'] = 'sha256:' + '0' * 64
        with self.assertRaises(lifecycle.OperatorError):
            handle.assert_target(self.driver, URL)
        self.driver.container['image'] = IMAGE
        self.driver.container['networks'][NETWORK]['IPAddress'] = '192.168.240.35'
        with self.assertRaises(lifecycle.OperatorError):
            handle.assert_target(self.driver, URL)
        self.driver.container['networks'][NETWORK]['IPAddress'] = IP
        self.driver.resolved['services']['web']['networks']['invoice_proxy']['ipv4_address'] = '192.168.240.35'
        with self.assertRaises(lifecycle.OperatorError):
            handle.assert_target(self.driver, URL)

    def test_capability_cannot_move_to_another_driver_or_follow_context_or_journal_drift(self):
        handle = self.issue()
        other = copy.copy(self.driver)
        other._frozen_ready_endpoint = handle
        with self.assertRaises(lifecycle.OperatorError):
            handle.assert_target(other, URL)
        self.driver.output = self.driver.output / 'another-invocation'
        with self.assertRaises(lifecycle.OperatorError):
            handle.assert_target(self.driver, URL)
        self.driver.output = self.driver.output.parent
        self.driver.write_journal(source_head='0' * 40)
        with self.assertRaises(lifecycle.OperatorError):
            handle.assert_target(self.driver, URL)
        self.driver.write_journal()
        self.driver.value['extra_unreviewed_input'] = True
        with self.assertRaises(lifecycle.OperatorError):
            handle.assert_target(self.driver, URL)


if __name__ == '__main__':
    unittest.main()
