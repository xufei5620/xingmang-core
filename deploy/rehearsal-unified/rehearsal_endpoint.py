"""Issue readiness authority only for one observed, owned frozen web endpoint."""
import hashlib
import ipaddress
import json
import re
from urllib.parse import urlsplit

from lifecycle import IMAGE_ID, NAME, OperatorError, plain_path, read_public_json, require


_ISSUER = object()
_ID = re.compile(r'[a-f0-9]{64}\Z')
_HEAD = re.compile(r'[a-f0-9]{40}\Z')
_OWNER = re.compile(r'[a-f0-9]{32}\Z')
_PRIVATE = tuple(ipaddress.ip_network(value) for value in ('10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16'))
_VOLUMES = {'platform_database', 'invoice_database', 'documents', 'source_state', 'invoice_metadata', 'platform_metadata'}
_CONTAINER = ('{"id":{{json .Id}},"image":{{json .Image}},"running":{{json .State.Running}},'
              '"project":{{json (index .Config.Labels "com.docker.compose.project")}},'
              '"service":{{json (index .Config.Labels "com.docker.compose.service")}},'
              '"owner":{{json (index .Config.Labels "xingmang.rehearsal.owner")}},'
              '"networks":{{json .NetworkSettings.Networks}}}')
_NETWORK = ('{"id":{{json .Id}},"name":{{json .Name}},"driver":{{json .Driver}},"internal":{{json .Internal}},'
            '"project":{{json (index .Labels "com.docker.compose.project")}},'
            '"owner":{{json (index .Labels "xingmang.rehearsal.owner")}},'
            '"ipam":{{json .IPAM.Config}},"containers":{{json .Containers}}}')


def _base_target(url):
    require(isinstance(url, str), 'frozen readiness URL is invalid')
    value = urlsplit(url)
    address = ipaddress.IPv4Address(value.hostname)
    require(any(address in network for network in _PRIVATE) and not address.is_loopback,
            'frozen readiness must use its observed private IPv4 address')
    require(value.port == 8081 and url == 'http://' + str(address) + ':8081/readyz',
            'frozen readiness requires exact HTTP port 8081 and /readyz')
    return str(address)


def _binding(driver, value):
    """Validate the actual creation journal before querying any Docker object."""
    require(isinstance(value, dict), 'frozen readiness value is missing')
    candidate = driver.config['candidate']
    projects, previous = candidate['projects'], driver.config['previous']['projects']
    require(driver.config.get('mode') in ('local-synthetic', 'server-rehearsal', 'production'), 'unknown readiness mode')
    require(isinstance(projects, list) and len(projects) == 2 and projects == value['projects'] and
            {p['kind'] for p in projects} == {'unified', 'sources'}, 'readiness candidate is not the frozen project plan')
    names = [p['name'] for p in projects]
    require(len(set(names)) == 2 and all(isinstance(name, str) and NAME.fullmatch(name) and name.startswith('xm-rehearsal-') for name in names),
            'readiness projects must be explicitly owned rehearsal projects')
    require(isinstance(previous, list) and previous and not set(names).intersection(p['name'] for p in previous),
            'frozen readiness must be disjoint from original production projects')
    head, owner = candidate['head'], value['owner_id']
    require(isinstance(head, str) and _HEAD.fullmatch(head) and isinstance(owner, str) and _OWNER.fullmatch(owner),
            'frozen readiness source or owner is invalid')
    volumes = value['volumes']
    require(isinstance(volumes, dict) and _VOLUMES <= set(volumes) and
            len(set(volumes.values())) == len(volumes) and
            all(isinstance(name, str) and NAME.fullmatch(name) and name.startswith('xm-rehearsal-') for name in volumes.values()),
            'frozen readiness volume ownership is incomplete')
    state = plain_path(str(driver.state), directory=True)
    invocation = plain_path(str(driver.output), directory=True)
    journal = read_public_json(state / 'rehearsal-ownership.json')
    require(journal == {'schema': 'xingmang.rehearsal.ownership/v1', 'owner_id': owner, 'invocation': str(driver.output),
                        'volumes': volumes, 'projects': names, 'source_head': head},
            'frozen readiness lacks the matching current ownership journal')
    require(candidate['ready_url'] == value['ready_url'], 'frozen readiness URL differs from the creation plan')
    address = _base_target(candidate['ready_url'])
    web = [(project, service, row) for project in projects for service, row in project['services'].items() if row.get('role') == 'web']
    require(len(web) == 1 and web[0][0]['kind'] == 'unified', 'frozen readiness requires one unified web role')
    project, service, entry = web[0]
    require(NAME.fullmatch(service) and isinstance(entry.get('image_id'), str) and IMAGE_ID.fullmatch(entry['image_id']),
            'frozen readiness web image must be immutable')
    context = {'value': value, 'candidate': candidate, 'previous': previous, 'state': str(state), 'invocation': str(invocation),
               'mode': driver.config['mode'], 'docker': driver.docker}
    fingerprint = hashlib.sha256(json.dumps(context, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    return fingerprint, project, service, entry['image_id'], owner, address


def _json(result):
    require(result.returncode == 0 and isinstance(result.stdout, bytes) and len(result.stdout) <= 8 * 1024 * 1024,
            'frozen readiness metadata command failed')
    value = json.loads(result.stdout.decode('utf-8'))
    require(isinstance(value, dict), 'frozen readiness metadata is not an object')
    return value


def _observe(driver, value):
    binding, project, service, image, owner, address = _binding(driver, value)
    resolved = _json(driver.compose(project, 'frozen-ready-compose', ['config', '--format', 'json']))
    web = resolved['services'][service]
    attachments = web.get('networks')
    require(web.get('image') == image and not web.get('network_mode') and isinstance(attachments, dict) and len(attachments) == 1,
            'frozen web must use one explicit network and its reviewed immutable image')
    key, planned_endpoint = next(iter(attachments.items()))
    require(isinstance(planned_endpoint, dict) and planned_endpoint.get('ipv4_address') == address,
            'frozen readiness IP is not the native resolved Compose static address')
    planned = resolved['networks'][key]
    name = planned['name']
    require(isinstance(name, str) and NAME.fullmatch(name) and name.startswith('xm-rehearsal-') and
            planned.get('internal') is True and not planned.get('external') and planned.get('driver', 'bridge') == 'bridge',
            'frozen web network is not a private owned bridge declaration')
    plans = planned['ipam']['config']
    require(isinstance(plans, list) and len(plans) == 1, 'frozen web requires one reviewed IPv4 subnet')
    subnet = ipaddress.IPv4Network(plans[0]['subnet'], strict=True)
    gateway = ipaddress.IPv4Address(plans[0]['gateway'])
    ip = ipaddress.IPv4Address(address)
    require(ip in subnet and gateway in subnet and ip not in (subnet.network_address, subnet.broadcast_address, gateway),
            'frozen web static IP is outside its usable reviewed subnet')
    ids_result = driver.compose(project, 'frozen-ready-container-id', ['ps', '--all', '-q', service])
    require(ids_result.returncode == 0 and isinstance(ids_result.stdout, bytes), 'frozen web container lookup failed')
    ids = ids_result.stdout.decode('ascii').split()
    require(len(ids) == 1 and _ID.fullmatch(ids[0]), 'frozen web requires exactly one actual container ID')
    cid = ids[0]
    container = _json(driver.command('frozen-ready-container', driver.docker + ['inspect', '--type', 'container', cid, '--format', _CONTAINER]))
    require(container.get('id') == cid and container.get('image') == image and container.get('running') is True and
            container.get('project') == project['name'] and container.get('service') == service and container.get('owner') == owner,
            'frozen web container identity or owner changed')
    actual = container.get('networks')
    require(isinstance(actual, dict) and set(actual) == {name}, 'frozen web has an unexpected network attachment')
    attachment = actual[name]
    network_id, endpoint_id = attachment['NetworkID'], attachment['EndpointID']
    require(isinstance(network_id, str) and _ID.fullmatch(network_id) and isinstance(endpoint_id, str) and _ID.fullmatch(endpoint_id) and
            attachment.get('IPAddress') == address and attachment.get('IPPrefixLen') == subnet.prefixlen,
            'frozen web live network ID, endpoint or IP differs')
    network = _json(driver.command('frozen-ready-network', driver.docker + ['network', 'inspect', network_id, '--format', _NETWORK]))
    require(network.get('id') == network_id and network.get('name') == name and network.get('driver') == 'bridge' and network.get('internal') is True and
            network.get('owner') == owner and network.get('project') == project['name'],
            'frozen web live network is not the exact owned internal bridge')
    ipam = network.get('ipam')
    require(isinstance(ipam, list) and len(ipam) == 1 and ipam[0].get('Subnet') == str(subnet) and
            ipam[0].get('Gateway') == str(gateway) and (ipam[0].get('IPRange') or '') == (plans[0].get('ip_range') or ''),
            'frozen web native network IPAM differs from resolved Compose')
    members = network.get('containers')
    require(isinstance(members, dict) and cid in members, 'frozen web is absent from the actual network')
    member = members[cid]
    require(member.get('EndpointID') == endpoint_id and member.get('IPv4Address') == address + '/' + str(subnet.prefixlen) and
            sum(row.get('IPv4Address', '').split('/')[0] == address for row in members.values()) == 1,
            'frozen web actual network endpoint does not uniquely own the static IP')
    identity = (cid, image, project['name'], service, owner, name, network_id, endpoint_id, address, str(subnet), str(gateway), plans[0].get('ip_range') or '')
    return binding, identity


class _FrozenReadyEndpoint:
    __slots__ = ('_issuer', '_driver', '_value', '_binding', '_identity', '_url')

    def __init__(self, issuer, driver, value, binding, identity):
        require(issuer is _ISSUER, 'frozen readiness capability cannot be reconstructed from data')
        self._issuer, self._driver, self._value = issuer, driver, value
        self._binding, self._identity, self._url = binding, identity, value['ready_url']

    def assert_target(self, driver, url):
        require(getattr(self, '_issuer', None) is _ISSUER and driver is self._driver and
                getattr(driver, '_frozen_ready_endpoint', None) is self, 'frozen readiness capability belongs to another driver')
        require(url in (self._url, self._url + '?report=full'), 'frozen readiness request is outside its exact target')
        try:
            require(_binding(driver, self._value)[0] == self._binding, 'frozen readiness invocation or plan changed')
            binding, identity = _observe(driver, self._value)
            require(binding == self._binding and identity == self._identity, 'frozen readiness endpoint changed after attestation')
        except (KeyError, TypeError, ValueError, AttributeError, OSError):
            raise OperatorError('frozen readiness metadata cannot be verified') from None
        return url


def attest_frozen_ready_endpoint(driver, value):
    """Return None for the existing loopback path; otherwise attest native state.

    Only the controlled restore caller stores the returned capability on
    driver._frozen_ready_endpoint, after start_new and before check_new.
    """
    try:
        url = value['ready_url']
        require(isinstance(url, str), 'frozen readiness URL is invalid')
        host = urlsplit(url).hostname
        if host == 'localhost':
            return None
        try:
            if ipaddress.ip_address(host).is_loopback:
                return None
        except ValueError:
            pass
        _base_target(url)
        binding, identity = _observe(driver, value)
        return _FrozenReadyEndpoint(_ISSUER, driver, value, binding, identity)
    except (KeyError, TypeError, ValueError, AttributeError, OSError):
        raise OperatorError('frozen readiness metadata cannot be verified') from None
