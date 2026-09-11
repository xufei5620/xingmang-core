"""Check unified Nginx configuration and actual local-only HTTP responses."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import uuid

IMAGE = 'nginx:1.28-alpine@sha256:a8b39bd9cf0f83869a2162827a0caf6137ddf759d50a171451b335cecc87d236'
CSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"


def require(condition, message):
    if not condition:
        raise ValueError(message)


def validate_source(root, config=None):
    nginx = (config or root / 'deploy/unified/nginx.conf').read_text(encoding='utf-8')
    routes = re.findall(r'location /invoice-api/v1/\s*\{([^}]+)\}', nginx)
    require(len(routes) == 2, 'both public origins must expose the unified invoice namespace')
    for number, route in enumerate(routes):
        require('proxy_request_buffering off;' in route, f'unified invoice route {number}: PDF upload disk-capable buffering is not disabled')
        require('proxy_buffering off;' in route, f'unified invoice route {number}: PDF download disk-capable buffering is not disabled')
        require('proxy_next_upstream off;' in route and 'client_max_body_size 24m;' in route, 'PDF retry or request-size boundary drifted')
    require(nginx.count('location = /readyz { proxy_pass http://$unified_api; }') == 2, 'exact unified readyz must not be masked by SPA fallback')
    ingress = (root / 'invoice/deploy/nginx/ingest-mtls.conf').read_text(encoding='utf-8')
    require(re.search(r'resolver\s+127\.0\.0\.11\s', ingress) and 'proxy_pass http://$ingest_upstream:8080;' in ingress, 'ingestion must resolve the unified upstream per request')
    require('ssl_verify_client on;' in ingress and 'location / { return 404; }' in ingress, 'mTLS ingestion must fail closed outside its batch route')
    return nginx, ingress


def verify_headers(raw, expected_status, customer):
    statuses = re.findall(r'(?m)^\s*HTTP/\d(?:\.\d)?\s+(\d{3})\b', raw)
    require(statuses == [str(expected_status)], 'HTTP response is missing, redirected or ambiguous: ' + str(statuses))
    expected = {'X-Content-Type-Options': 'nosniff', 'X-Frame-Options': 'DENY', 'Referrer-Policy': 'no-referrer'}
    if customer:
        expected['Content-Security-Policy'] = CSP
    for name, value in expected.items():
        actual = re.findall(r'(?im)^\s*' + re.escape(name) + r':\s*([^\r\n]+)', raw)
        require([item.strip() for item in actual] == [value], 'missing/duplicate/wrong exact response header: ' + name)


def http_headers(docker, name, port, path):
    # BusyBox wget stops printing headers on an HTTP error. Read the actual wire
    # response with nc so 404 security headers get the same exact checks as 200.
    request = f'GET {path} HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n'
    result = subprocess.run(docker + ['exec', '-i', name, 'nc', '-w', '5', '127.0.0.1', str(port)],
                            input=request.encode('ascii'), capture_output=True)
    require(result.returncode == 0, 'local HTTP connection failed: ' + result.stderr.decode('utf-8', errors='replace'))
    header, separator, _ = result.stdout.partition(b'\r\n\r\n')
    require(separator, 'local HTTP response has no header terminator')
    raw = header.decode('iso-8859-1')
    print(json.dumps({'url': f'http://127.0.0.1:{port}'+path, 'headers':raw}), flush=True)
    return raw


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--project-root', type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument('--image', default=IMAGE)
    parser.add_argument('--admin-dist', type=Path)
    parser.add_argument('--invoice-dist', type=Path)
    parser.add_argument('--nginx-config', type=Path, help='Explicit local configuration for an isolated regression probe')
    parser.add_argument('--docker-context', default=os.environ.get('DOCKER_CONTEXT', 'desktop-linux' if os.name == 'nt' else 'default'))
    args = parser.parse_args()
    root = args.project_root.resolve()
    admin_dist = args.admin_dist or root/'platform/web/apps/admin-web/dist'
    invoice_dist = args.invoice_dist or root/'invoice/web/dist'
    nginx_config = args.nginx_config or root/'deploy/unified/nginx.conf'
    _, ingress = validate_source(root, nginx_config)
    spec = importlib.util.spec_from_file_location('unified_gate', root / 'scripts/unified-service.py')
    gate = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(gate)
    docker = gate.docker_command(args.docker_context)
    image_id = gate.run(docker + ['image', 'inspect', '--format', '{{.Id}}', args.image]).decode().strip()
    name = 'xm-invoice-verify-nginx-' + uuid.uuid4().hex[:12]
    created_network = created_container = False
    with tempfile.TemporaryDirectory(prefix='invoice-unified-nginx-') as temporary:
        folder = Path(temporary)
        try:
            gate.run(['go', 'run', './cmd/mtlsgen', '--out-dir', str(folder/'pki'), '--server-name', 'invoice-ingest.internal', '--clients', 'sub2api-agent,newapi-agent'], cwd=root/'invoice/backend')
            (folder/'trust.conf').write_text('set_real_ip_from 127.0.0.1/32;\nreal_ip_header X-Forwarded-For;\nreal_ip_recursive on;\n', encoding='utf-8')
            (folder/'app-config.js').write_text('window.__XM_CONFIG__={"authMode":"local"};\n', encoding='utf-8')
            (folder/'stub.conf').write_text('server { listen 8080; location / { default_type application/json; return 200 \'{"uri":"$request_uri","client":"$http_cf_connecting_ip","mock":"$http_x_mock_role","mtls":"$http_x_invoice_mtls_verified"}\'; } }\n', encoding='utf-8')
            ingress = ingress.replace('/run/secrets/ingest_server_cert', '/test/pki/ingest_server_cert.pem').replace('/run/secrets/ingest_server_key', '/test/pki/ingest_server_key.pem').replace('/run/secrets/source_agent_ca', '/test/pki/source_agent_ca.pem')
            (folder/'ingest.conf').write_text(ingress, encoding='utf-8')
            mounts = []
            for source, target in [(nginx_config, '/etc/nginx/conf.d/default.conf'),
                (admin_dist, '/usr/share/nginx/html'), (invoice_dist, '/usr/share/nginx/invoice'),
                (folder/'trust.conf', '/etc/nginx/runtime/trusted-proxy.conf'), (folder/'app-config.js', '/etc/nginx/runtime/app-config.js'),
                (folder/'stub.conf', '/etc/nginx/conf.d/zz-synthetic.conf'), (folder, '/test')]:
                require(source.exists(), 'required built frontend or test source is missing: ' + str(source))
                mounts += ['--mount', 'type=bind,source=' + str(source.resolve()) + ',target=' + target + ',readonly']
            gate.run(docker + ['network', 'create', '--internal', '--label', 'xingmang.local-probe='+name, name]); created_network = True
            gate.run(docker + ['run', '--detach', '--pull', 'never', '--name', name, '--label', 'xingmang.local-probe='+name, '--network', name,
                '--network-alias', 'platform-api', '--read-only', '--tmpfs', '/var/cache/nginx', '--tmpfs', '/var/run', *mounts,
                '--entrypoint', 'nginx', image_id, '-g', 'daemon off;']); created_container = True
            for config in ('/etc/nginx/nginx.conf', '/test/ingest.conf'):
                gate.run(docker + ['exec', name, 'nginx', '-t', '-c', config])
            for port, dist, customer in [(80, admin_dist, False), (8081, invoice_dist, True)]:
                assets = sorted(path for path in (dist/'assets').iterdir() if path.suffix in ('.js', '.css'))
                require(assets, 'built SPA has no static asset')
                paths = ['/', '/index.html', '/assets/'+assets[0].name, '/orders' if customer else '/finance']
                if not customer:
                    paths.append('/app-config.js')
                for path in paths:
                    verify_headers(http_headers(docker, name, port, path), 200, customer)
                for path in ['/internal/v1/source-batches', '/metrics'] + (['/api/v1/auth/login', '/admin', '/embed/'] if customer else []):
                    verify_headers(http_headers(docker, name, port, path), 404, customer)
            probe = docker + ['exec', name, 'wget', '-q', '-O', '-', '-T', '5']
            body = json.loads(gate.run(probe + ['--header=X-Forwarded-For: 198.51.100.77', '--header=CF-Connecting-IP: 203.0.113.99', '--header=X-Mock-Role: admin', '--header=X-Invoice-mTLS-Verified: SUCCESS', 'http://127.0.0.1:8081/invoice-api/v1/synthetic?source=sub2api']).decode())
            require(body == {'uri':'/invoice-api/v1/synthetic?source=sub2api','client':'198.51.100.77','mock':'','mtls':''}, 'unified proxy forwarded spoofed identity or changed the API URI')
            print(json.dumps({'exitCode':0, 'imageId':image_id, 'scope':'synthetic container-loopback; both built SPA roots, exact headers, mTLS syntax and routing'}))
        finally:
            errors = []
            if created_container and subprocess.run(docker+['rm','--force',name], capture_output=True).returncode != 0:
                errors.append('container')
            if created_network and subprocess.run(docker+['network','rm',name], capture_output=True).returncode != 0:
                errors.append('network')
            require(not errors, 'temporary Nginx cleanup failed: ' + ','.join(errors))


if __name__ == '__main__':
    main()
