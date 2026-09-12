"""Switch only reviewed public proxy configuration; never open TLS key files."""
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shlex
import stat
import time
import urllib.parse
import uuid

from lifecycle import OperatorError, require, plain_path, digest, read_public_json, atomic_json, utc, NAME, IMAGE_ID


def parse(value):
    lexer = shlex.shlex(value, posix=True, punctuation_chars='{};')
    lexer.whitespace_split = True
    tokens = []
    for token in lexer:
        tokens.extend(token if token and set(token) <= set('{};') else [token])
    cursor = iter(tokens)
    def block(nested=False):
        result, args = [], []
        for token in cursor:
            if token == '}':
                require(nested and not args, 'invalid public nginx block')
                return result
            if token in (';', '{'):
                require(bool(args), 'invalid public nginx directive')
                result.append((tuple(args), None if token == ';' else block(True)))
                args = []
            else:
                args.append(token)
        require(not nested and not args, 'incomplete public nginx configuration')
        return result
    try:
        return block()
    except ValueError:
        raise OperatorError('invalid public nginx configuration') from None


def normalize(nodes, *, proxy=False):
    result = []
    for args, children in nodes:
        head = ('proxy_pass', '<reviewed-loopback>') if proxy and args[0] == 'proxy_pass' else args
        result.append((head, None if children is None else normalize(children, proxy=proxy)))
    return result


def replace_reviewed_includes(nodes, pairs):
    """Only replace the exact reviewed include target at its original AST slot."""
    mapping = dict(pairs)
    seen = set()
    def visit(items):
        result = []
        for args, children in items:
            if args[0] == 'include' and len(args) == 2 and args[1] in mapping:
                seen.add(args[1])
                args = ('include', mapping[args[1]])
            result.append((args, None if children is None else visit(children)))
        return result
    result = visit(nodes)
    require(seen == set(mapping), 'each reviewed vhost must have an explicit include at its original main-config position')
    return result


def only_proxy_changes(old, new):
    require(normalize(parse(old), proxy=True) == normalize(parse(new), proxy=True),
            'candidate vhost changes more than reviewed proxy destinations')


def check_routes(value, origins, ports):
    servers = []
    def collect(nodes):
        for args, children in nodes:
            if args == ('server',) and children is not None:
                servers.append(children)
            elif children is not None:
                collect(children)
    collect(parse(value))
    for role, origin in origins.items():
        url = urllib.parse.urlsplit(origin)
        wanted_port = str(url.port or 443)
        matches = []
        for server in servers:
            names = [name for args, _ in server if args[0] == 'server_name' for name in args[1:]]
            listens = [args for args, _ in server if args[0] == 'listen']
            if url.hostname in names and any('ssl' in args and args[1].rsplit(':', 1)[-1] == wanted_port for args in listens):
                matches.append(server)
        require(len(matches) == 1, 'configured HTTPS origin has no unique managed nginx server')
        require(not any(args[0] == 'include' for args, _ in matches[0]),
                'managed server routes must be explicit; included route handlers require owner review')
        locations = []
        for args, children in matches[0]:
            if args[0] == 'location':
                require(children is not None and ((len(args) == 2 and args[1].startswith('/')) or
                        (len(args) == 3 and args[1] in ('=', '^~') and args[2].startswith('/'))),
                        'managed vhost requires explicit prefix/exact routes, not unreviewed regex routes')
                locations.append((args, children))
        def verify_proxy(selected):
            proxies = [args for args, _ in selected if args[0] == 'proxy_pass']
            require(proxies == [('proxy_pass', 'http://127.0.0.1:' + str(ports[role]))],
                    'nginx public route does not preserve its path to the selected new web port')
            require(not any(args[0] in ('return', 'rewrite', 'try_files', 'if', 'include', 'location') for args, _ in selected),
                    'nginx public route has an unreviewed handler override')
        for args, body in locations:
            # Check every more-specific API route and every proxied page, not
            # merely the sampled login/list URLs exercised by read-only smoke.
            path = args[-1]
            protected = any(path == prefix.rstrip('/') or path.startswith(prefix)
                            for prefix in ('/api/', '/invoice-api/', '/webhooks/', '/finance/'))
            if protected or any(head[0] in ('proxy_pass', 'location') for head, _ in body):
                verify_proxy(body)
        paths = ['/', '/readyz', '/healthz', '/invoice-api/v1/auth/session']
        if role == 'admin': paths += ['/api/v1/auth/login', '/finance', '/webhooks/']
        for path in paths:
            exact = [body for args, body in locations if args[1] == '=' and args[2] == path]
            prefixes = [(args[-1], body) for args, body in locations if args[1] != '=' and path.startswith(args[-1])]
            require(len(exact) <= 1 and (exact or prefixes), 'nginx public route is missing or ambiguous')
            if exact:
                selected = exact[0]
            else:
                longest = max(len(prefix) for prefix, _ in prefixes)
                choices = [body for prefix, body in prefixes if len(prefix) == longest]
                require(len(choices) == 1, 'nginx route prefix is ambiguous')
                selected = choices[0]
            verify_proxy(selected)


def expanded(raw):
    try:
        value = raw.decode('utf-8')
    except UnicodeError:
        raise OperatorError('nginx expanded configuration is not UTF-8') from None
    result = {}
    headers = list(re.finditer(r'^# configuration file (.+):\r?$', value, re.MULTILINE))
    require(headers, 'nginx -T did not expose its loaded public configuration')
    for index, match in enumerate(headers):
        path = match[1]
        require(path not in result, 'duplicate nginx configuration section')
        end = headers[index + 1].start() if index + 1 < len(headers) else len(value)
        result[path] = value[match.end():end].strip()
    return result


def compare_expanded(old, new, old_main, staged_main, pairs):
    require(old_main in old and staged_main in new, 'nginx main configuration was not loaded')
    old_vhosts, new_vhosts = set(), set()
    for before, after in pairs:
        require(before in old and after in new and before not in new and after not in old,
                'staged nginx configuration does not replace exactly the live vhost')
        only_proxy_changes(old[before], new[after])
        old_vhosts.add(before); new_vhosts.add(after)
    require(replace_reviewed_includes(parse(old[old_main]), pairs) == parse(new[staged_main]),
            'staged nginx main changes include scope, order or unreviewed behavior')
    original = {p: body for p, body in old.items() if p not in old_vhosts | {old_main}}
    candidate = {p: body for p, body in new.items() if p not in new_vhosts | {staged_main}}
    require(original == candidate, 'staged nginx configuration changes other sites or shared policy')


def atomic_public_file(path, body, metadata):
    path = plain_path(str(path))
    temporary = path.with_name(path.name + '.unified-' + uuid.uuid4().hex + '.tmp')
    descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, metadata['mode'])
    try:
        with os.fdopen(descriptor, 'wb') as stream:
            stream.write(body); stream.flush(); os.fsync(stream.fileno())
            if os.name != 'nt':
                os.fchmod(stream.fileno(), metadata['mode'])
                os.fchown(stream.fileno(), metadata['uid'], metadata['gid'])
        os.replace(temporary, path)
    finally:
        if temporary.exists(): temporary.unlink()  # Only this exact newly created public temp file.


class HostNginx:
    def __init__(self, driver, *, recovering=False):
        self.driver = driver
        self.config = driver.config.get('host_nginx')
        require(isinstance(self.config, dict) and set(self.config) == {'main_config', 'staged_main_config', 'vhosts', 'execution', 'web_ports'},
                'explicit host nginx switch and rollback configuration is required')
        self.main = plain_path(self.config['main_config'])
        self.staged = plain_path(self.config['staged_main_config'], exists=not recovering)
        require(self.main != self.staged, 'nginx staged main must be separate')
        self.smoke = read_public_json(driver.config['smoke_config'])
        from smoke import origin
        self.origins = {role: origin(self.smoke['origins'][role]) for role in ('admin', 'user')}
        self.ports = self.config['web_ports']
        require(set(self.ports) == {'admin', 'user'} and all(type(port) is int and 1 <= port <= 65535 for port in self.ports.values()) and len(set(self.ports.values())) == 2,
                'both distinct new web ports must be explicit')
        rows = self.config['vhosts']
        require(isinstance(rows, list) and 1 <= len(rows) <= 2, 'one or two exact managed vhost files are required')
        self.rows = []
        paths = {self.main, self.staged}
        for row in rows:
            require(set(row) == {'live', 'candidate', 'candidate_sha256'}, 'invalid managed vhost descriptor')
            before, after = plain_path(row['live']), plain_path(row['candidate'], exists=not recovering)
            require(before not in paths and after not in paths and before != after, 'nginx public input paths overlap')
            require(re.fullmatch(r'[a-f0-9]{64}', row['candidate_sha256']) and (recovering or digest(after) == row['candidate_sha256']), 'candidate vhost hash differs')
            paths.update((before, after)); self.rows.append((before, after))
        self.execution = self.config['execution']
        if driver.config['mode'] == 'local-synthetic':
            require(set(self.execution) == {'container', 'image_id', 'project', 'host_root', 'container_root'}, 'local nginx execution descriptor is incomplete')
            require(NAME.fullmatch(self.execution['container']) and NAME.fullmatch(self.execution['project']) and IMAGE_ID.fullmatch(self.execution['image_id']), 'local nginx identity is invalid')
            self.host_root = plain_path(self.execution['host_root'], directory=True)
            self.container_root = PurePosixPath(self.execution['container_root'])
            require(self.container_root.is_absolute() and '..' not in self.container_root.parts, 'local nginx container root must be absolute')
            require(all(p.is_relative_to(self.host_root) for p in paths), 'local nginx public files escape owned configuration directory')
        else:
            require(set(self.execution) == {'binary', 'prefix', 'unshare_binary'}, 'server nginx execution descriptor is incomplete')
            plain_path(self.execution['binary']); plain_path(self.execution['prefix'], directory=True)
            plain_path(self.execution['unshare_binary'])

    def runtime_path(self, path):
        return str(self.container_root / path.relative_to(self.host_root).as_posix()) if self.driver.config['mode'] == 'local-synthetic' else str(path)

    def command(self, name, main, *, reload=False):
        if self.driver.config['mode'] == 'local-synthetic':
            info = self.driver.command('host-nginx-identity', self.driver.docker + ['inspect', '--type', 'container', self.execution['container'], '--format', '{{json .}}'])
            value = json.loads(info.stdout)
            require(value.get('State', {}).get('Running') is True and value.get('Image') == self.execution['image_id'] and
                    value.get('Config', {}).get('Labels', {}).get('com.docker.compose.project') == self.execution['project'], 'local nginx is not the reviewed running task container')
            prefix = self.driver.docker + ['exec', self.execution['container'], 'nginx', '-p', str(self.container_root)]
            if not reload:
                prefix = self.driver.docker + ['exec', self.execution['container'], 'busybox', 'unshare', '-n', '--', 'nginx', '-p', str(self.container_root)]
        else:
            prefix = [self.execution['binary'], '-p', self.execution['prefix']]
            if not reload:
                prefix = [self.execution['unshare_binary'], '--net', '--', *prefix]
        args = ['-s', 'reload'] if reload else ['-T']  # -T includes the same syntax checks as -t.
        return self.driver.command(name, prefix + args + ['-c', self.runtime_path(main)])

    def preflight(self):
        projects = [p for p in self.driver.config['candidate']['projects'] if p['kind'] == 'unified']
        require(len(projects) == 1, 'host nginx requires exactly one verified unified web project')
        project = projects[0]
        services = [name for name, row in project['services'].items() if row['role'] == 'web']
        require(len(services) == 1, 'host nginx requires exactly one manifest-bound web service')
        resolved = json.loads(self.driver.compose(project, 'host-nginx-web-bindings', ['config', '--format', 'json']).stdout)
        bindings = resolved['services'][services[0]].get('ports', [])
        for role, target in (('admin', 80), ('user', 8081)):
            matches = [row for row in bindings if row.get('target') == target]
            require(len(matches) == 1 and matches[0].get('host_ip') == '127.0.0.1' and matches[0].get('protocol', 'tcp') == 'tcp' and
                    str(matches[0].get('published')) == str(self.ports[role]), 'nginx upstream does not match the actual loopback web binding')
        old = expanded(self.command('host-nginx-current-test', self.main).stdout)
        new = expanded(self.command('host-nginx-candidate-test', self.staged).stdout)
        pairs = [(self.runtime_path(a), self.runtime_path(b)) for a, b in self.rows]
        compare_expanded(old, new, self.runtime_path(self.main), self.runtime_path(self.staged), pairs)
        for before, after in self.rows:
            require(old[self.runtime_path(before)] == before.read_text('utf-8').strip() and new[self.runtime_path(after)] == after.read_text('utf-8').strip(),
                    'nginx runtime mapping differs from reviewed host public files')
        check_routes('\n'.join(new[b] for a, b in pairs), self.origins, self.ports)
        return {'status': 'PASS', 'exit_code': 0, 'utc_end': utc(), 'public_network_access': False, 'nginx_test_network': 'fresh-isolated-network-namespace', 'main_sha256': digest(self.main),
                'staged_main_sha256': digest(self.staged), 'vhosts': [{'live': str(a), 'original_sha256': digest(a), 'candidate': str(b), 'candidate_sha256': digest(b)} for a, b in self.rows]}

    def snapshot(self):
        proof = self.preflight()
        folder = self.driver.output / 'host-nginx-original'
        folder.mkdir(parents=True, exist_ok=False)
        for index, row in enumerate(proof['vhosts']):
            path = plain_path(row['live'])
            body = path.read_bytes()
            require(hashlib.sha256(body).hexdigest() == row['original_sha256'], 'nginx original changed before snapshot')
            target = folder / (str(index) + '.conf')
            with target.open('xb') as stream: stream.write(body)
            info = path.stat()
            row.update(backup=str(target), mode=stat.S_IMODE(info.st_mode), uid=info.st_uid, gid=info.st_gid)
        return proof

    def apply(self, snapshot, *, rollback=False):
        require(snapshot.get('main_sha256') == digest(self.main), 'nginx main changed after snapshot')
        expected = [(str(a), str(b)) for a, b in self.rows]
        require([(r['live'], r['candidate']) for r in snapshot['vhosts']] == expected, 'nginx snapshot belongs to a different vhost plan')
        prepared = []
        for row in snapshot['vhosts']:
            actual = digest(row['live'])
            require(actual in (row['original_sha256'], row['candidate_sha256']), 'nginx vhost was changed outside the operation')
            source = plain_path(row['backup'] if rollback else row['candidate'])
            wanted = row['original_sha256'] if rollback else row['candidate_sha256']
            require(digest(source) == wanted, 'nginx recovery or candidate input changed')
            prepared.append((row, source.read_bytes(), wanted))
        for row, body, wanted in prepared:
            atomic_public_file(row['live'], body, row)
            require(digest(row['live']) == wanted, 'nginx public install hash mismatch')
        self.command('host-nginx-restored-test' if rollback else 'host-nginx-switched-test', self.main)
        self.command('host-nginx-restore-reload' if rollback else 'host-nginx-switch-reload', self.main, reload=True)
        if rollback:
            self.probe_restored()

    def probe_restored(self):
        from smoke import Client, SmokeFailure, json_object
        # Runtime verification is after rollback, not part of offline preflight.
        for role, origin in self.origins.items():
            client = Client(origin, self.smoke.get('ca_file'), [], self.smoke.get('connect_to', {}).get(role))
            deadline = time.monotonic() + 30
            while True:
                try:
                    body, media = client.raw('GET', '/readyz')
                    require(media == 'application/json', 'restored public readiness is not JSON')
                    payload = json_object(body)
                    # Original platform 3800a8b and invoice 277063c both return
                    # status=ready. Invoice alone may include contained-dead
                    # diagnostics; preserve that original successful outcome.
                    allowed = {'status', 'degraded'} if role == 'user' else {'status'}
                    require(payload.get('status') == 'ready' and set(payload) <= allowed,
                            'restored public readiness does not report the original successful verdict')
                    if 'degraded' in payload:
                        names = payload['degraded']
                        require(isinstance(names, list) and bool(names) and all(isinstance(name, str) and re.fullmatch(r'[a-z][a-z_]{2,63}', name) for name in names),
                                'restored invoice readiness has invalid contained diagnostics')
                    break
                except (OperatorError, SmokeFailure):
                    require(time.monotonic() < deadline, 'restored public nginx readiness failed')
                    time.sleep(1)
        atomic_json(self.driver.output / 'host-nginx-rollback-probe.json', {'status': 'PASS', 'exit_code': 0, 'utc_end': utc(), 'origins': self.origins})
