"""Render the complete unified Compose contract using synthetic values only."""
import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile


def require(value, message):
    if not value:
        raise ValueError(message)


def render(root):
    documents = [root / 'deploy/unified' / name for name in ('compose.json', 'sources.json')]
    text = '\n'.join(path.read_text(encoding='utf-8') for path in documents)
    values = {name: 'synthetic' for name in re.findall(r'\$\{([A-Z0-9_]+):\?', text)}
    values.update(UNIFIED_IMAGE_TAG='verification-build', INVOICE_PROXY_SUBNET='172.30.201.0/24',
        INVOICE_PROXY_GATEWAY_IP='172.30.201.1', UNIFIED_WEB_PROXY_IP='172.30.201.2',
        UNIFIED_WEB_PROXY_CIDR='172.30.201.2/32', INVOICE_DB_SUBNET='172.30.202.0/24',
        INVOICE_APP_SUBNET='172.30.203.0/24', CLAMAV_EGRESS_SUBNET='172.30.204.0/24',
        INVOICE_INGEST_SUBNET='172.30.205.0/24', INVOICE_INGEST_DYNAMIC_RANGE='172.30.205.128/25',
        INVOICE_INGEST_PROXY_IP='172.30.205.2', INVOICE_INGEST_PROXY_CIDR='172.30.205.2/32',
        PLATFORM_EGRESS_SUBNET='172.30.206.0/24', INVOICE_STAFF_ORIGIN='https://staff.example.invalid',
        PUBLIC_ORIGIN='https://invoice.example.invalid', ELIGIBILITY_START_AT='2026-09-01T00:00:00+08:00',
        SECRETS_DIR='/synthetic/not-mounted/secrets', SOURCE_TRUST_CONFIG_FILE='/synthetic/trust.json',
        SOURCE_STATE_ROOT='/synthetic/state', SOURCE_CUTOVER_ROOT='/synthetic/cutover',
        SOURCE_INSTANCES_CONFIG_FILE='/synthetic/instances.json', ADMIN_SETTINGS_BOOTSTRAP_FILE='/synthetic/settings.json',
        SUB2API_SOURCE_ID='10000000-0000-4000-8000-000000000001', NEWAPI_SOURCE_ID='10000000-0000-4000-8000-000000000002')
    environment = {key: os.environ[key] for key in ('PATH', 'SystemRoot', 'TEMP', 'TMP', 'USERPROFILE', 'HOME', 'APPDATA', 'LOCALAPPDATA', 'DOCKER_CONFIG', 'DOCKER_CONTEXT') if key in os.environ}
    docker = shutil.which('docker')
    require(docker, 'Docker Compose is required')
    result = []
    with tempfile.TemporaryDirectory(prefix='invoice-unified-contract-') as temp:
        config = Path(temp) / 'synthetic.env'
        config.write_text(''.join(key + '=' + value + '\n' for key, value in values.items()), encoding='utf-8')
        for document in documents:
            command = [docker, 'compose', '--profile', 'tools', '--profile', 'cutover', '--env-file', str(config), '-f', str(document),
                       'config', '--no-env-resolution', '--no-path-resolution', '--format', 'json']
            process = subprocess.run(command, env=environment, capture_output=True, text=True, encoding='utf-8')
            require(process.returncode == 0, 'Compose render failed: ' + process.stderr)
            result.append(json.loads(process.stdout))
    return *result, values


def validate(root, production, sources, values):
    services = production['services']
    api = services['platform-api']
    env = api['environment']
    require(env['APP_ENV'] == 'production' and env['ENVIRONMENT'] == 'production' and env['AUTH_MODE'] == 'session' and env['XM_AUTH_MODE'] == 'local', 'unified production/local session modes drifted')
    require(env['LISTEN_ADDR'] == '0.0.0.0:8080' and 'HTTP_ADDR' not in env, 'more than one API listener configured')
    require(env['DATABASE_URL_FILE'] == '/run/secrets/invoice_app_database_url', 'invoice owner/runtime boundary drifted')
    for name, service in services.items():
        require(not re.search('oidc|keycloak', name, re.I), 'retired IdP service present')
        require(not any(re.search('OIDC|CONSOLE_ASSERTION', key) for key in service.get('environment', {})), 'interactive IdP configuration reintroduced')
    require(env['ELIGIBILITY_START_AT'] == services['invoice-migrate']['environment']['ELIGIBILITY_START_AT'] == '2026-09-01T00:00:00+08:00', 'immutable eligibility start drifted')
    for key, expected in [('SOURCE_ECONOMIC_HEARTBEAT_MAX_STALENESS', '5m'), ('SOURCE_ECONOMIC_WATERMARK_MAX_STALENESS', '15m'), ('SOURCE_ECONOMIC_SAFETY_DELAY', '5m'), ('SOURCE_POLL_INTERVAL', '1m')]:
        require(env[key] == expected, 'economic timing contract drifted: ' + key)
    proxy = services['ingest-proxy']['networks']['invoice_ingest']
    require(env['INGEST_PROXY_CIDRS'] == values['INVOICE_INGEST_PROXY_CIDR'] and proxy['ipv4_address'] == values['INVOICE_INGEST_PROXY_IP'] and 'invoice-ingest.internal' in proxy['aliases'], 'ingestion exact proxy IP/alias drifted')
    require(services['ingest-proxy']['depends_on']['platform-api']['condition'] == 'service_started', 'ingress readiness circular dependency')
    web_ip = services['web']['networks']['invoice_proxy']['ipv4_address']
    require(env['TRUSTED_PROXY_CIDRS'] == web_ip + '/32' and web_ip == values['UNIFIED_WEB_PROXY_IP'], 'same-origin web proxy is not an exact host route')
    require(api['networks']['invoice_proxy']['gw_priority'] == 1, 'API default gateway drifted')
    for name, key, internal in [('invoice_proxy', 'INVOICE_PROXY_SUBNET', False), ('invoice_db', 'INVOICE_DB_SUBNET', True), ('invoice_app', 'INVOICE_APP_SUBNET', True), ('clamav_egress', 'CLAMAV_EGRESS_SUBNET', False), ('invoice_ingest', 'INVOICE_INGEST_SUBNET', True)]:
        network = production['networks'][name]
        require(network['ipam']['config'][0]['subnet'] == values[key] and network.get('internal', False) is internal, 'network boundary drifted: ' + name)
    require(production['networks']['invoice_proxy']['ipam']['config'][0]['gateway'] == values['INVOICE_PROXY_GATEWAY_IP'], 'proxy gateway drifted')
    scanner = services['pdf-scanner']
    require(scanner['network_mode'] == 'none' and scanner['read_only'] is True and 'ALL' in scanner['cap_drop'] and 'no-new-privileges:true' in scanner['security_opt'], 'PDF scanner sandbox drifted')
    require(int(scanner['mem_limit']) == 268435456 and scanner['pids_limit'] == 32 and scanner['cpus'] == 0.5 and scanner.get('healthcheck'), 'PDF scanner resource budget drifted')
    secrets = lambda service: {item['source'] for item in service.get('secrets', [])}
    require(secrets(scanner) == {'invoice_pdf_scanner_capability'}, 'scanner has excessive secrets')
    mounts = [mount for mount in scanner['volumes'] if mount['target'] == '/scanner']
    require(len(scanner['volumes']) == len(mounts) == 1 and mounts[0]['source'] == 'invoice_pdf_scanner_socket', 'scanner socket ownership drifted')
    api_socket = [mount for mount in api['volumes'] if mount['target'] == '/scanner']
    require(len(api_socket) == 1 and api_socket[0]['source'] == 'invoice_pdf_scanner_socket' and api_socket[0]['read_only'] is True and '10000' in api['group_add'], 'API scanner mount is not read-only shared group')
    require('invoice_pdf_scanner_capability' in secrets(api) and 'invoice_owner_database_url' not in secrets(api), 'API scanner/owner credential boundary drifted')
    for name in ('clamav', 'pdf-scanner', 'postgres'):
        require(api['depends_on'][name]['condition'] == 'service_healthy', 'scanner readiness is not mandatory: ' + name)
    require(services['invoice-migrate']['depends_on']['invoice-postgres']['condition'] == 'service_healthy' and api['depends_on']['invoice-migrate']['condition'] == 'service_completed_successfully', 'invoice database health and completed migration must precede API startup')
    require(api['depends_on']['invoice-permissions']['condition'] == 'service_completed_successfully' and services['invoice-permissions']['depends_on']['invoice-migrate']['condition'] == 'service_completed_successfully', 'migration and runtime permission sequence drifted')
    clam = services['clamav']
    require(int(clam['mem_limit']) == 4294967296 and clam['pids_limit'] == 256 and clam['networks']['clamav_egress']['gw_priority'] == 1, 'ClamAV resources/egress drifted')
    require(clam['healthcheck']['test'] == ['CMD', '/bin/sh', '/usr/local/bin/invoice-clamav-healthcheck'] and clam['environment']['CLAMAV_DATABASE_ROOT'] == '/var/lib/clamav' and clam['environment']['CLAMAV_MAX_SIGNATURE_AGE'] == '48h', 'ClamAV signature health contract drifted')
    health = [mount for mount in clam['volumes'] if mount['target'] == '/usr/local/bin/invoice-clamav-healthcheck']
    require(len(health) == 1 and health[0]['read_only'] is True, 'ClamAV health script must be read-only')
    permissions = services['invoice-permissions']
    require(permissions['user'] == '10001:10001' and secrets(permissions) == {'invoice_owner_database_url'} and 'ALL' in permissions['cap_drop'] and 'no-new-privileges:true' in permissions['security_opt'], 'isolated permission job credential/UID contract drifted')
    pins = {'postgres': 'docker.io/library/postgres:18@sha256:7341002d2b8c7c5bdd7542a671a95b36196c0b5b888daf454ae4fc33ba5346d7', 'invoice-postgres': 'postgres:18.6-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'}
    for name, pin in pins.items():
        require(services[name]['image'] == pin, 'official database pin changed: ' + name)
        data = [mount for mount in services[name]['volumes'] if mount['type'] == 'volume']
        require(len(data) == 1 and data[0]['target'] == '/var/lib/postgresql', 'PG18 parent volume contract drifted')
    require(permissions['image'] == pins['invoice-postgres'], 'permissions must use the same official invoice PostgreSQL image')
    definitions = json.loads((root / 'deploy/unified/images.json').read_text(encoding='utf-8'))
    allowed = {item['reference'] if item['kind'] == 'pinned' else item['repository'] + ':verification-build' for item in definitions}
    for name, service in {**services, **sources['services']}.items():
        require(service['image'] in allowed and service['pull_policy'] == 'never' and 'build' not in service, 'service escaped complete prebuilt image inventory: ' + name)
    trust = json.loads((root / 'invoice/deploy/source-trust.example.json').read_text(encoding='utf-8'))
    for source in ('sub2api', 'newapi'):
        trusted = [entry for entry in trust['sources'] if entry['source_type'] == source]
        require(len(trusted) == 1, 'source trust mapping is ambiguous')
        for stream in ('payments', 'identities', 'usage', 'credits', 'balances'):
            name = source + '-' + stream
            service = sources['services'][name]
            source_env = service['environment']
            require(source_env['SOURCE_TYPE'] == source and source_env['SOURCE_STATE_STREAM'] == stream and source_env['INGESTION_ALLOWED_CIDRS'] == values['INVOICE_INGEST_PROXY_CIDR'], 'source stream identity or ingress pin drifted: ' + name)
            require(source_env['ELIGIBILITY_START_AT'] == env['ELIGIBILITY_START_AT'] and source_env['SOURCE_POLL_INTERVAL'] == '1m' and service.get('healthcheck'), 'source policy/poll/health drifted: ' + name)
            require(source_env['SOURCE_ECONOMIC_SAFETY_DELAY'] == '5m', 'source economic horizon drifted: ' + name)
            if stream == 'identities':
                require(source_env['SOURCE_SCHEMA_VERSION'] == '2.0' and source_env['SOURCE_RECONCILE_FILE'] == '/state/reconcile.json', 'identity reconciliation V2 contract drifted')
            else:
                require(source_env['SOURCE_SCHEMA_VERSION'] == '3.0' and not source_env.get('SOURCE_RECONCILE_FILE') and source_env['SOURCE_CUTOVER_MANIFEST_FILE'] == '/cutover/manifest.enc', 'economic cutover V3 contract drifted')
            require(source_env['SOURCE_SIGNING_KEY_ID'] in trusted[0]['streams'][stream]['signing_keys'], 'source signing key ID not in public trust declaration')
        require(sources['services'][source + '-cutover-init']['environment']['ELIGIBILITY_START_AT'] == env['ELIGIBILITY_START_AT'], 'cutover policy boundary drifted')
    validate_build_boundaries(root)


def validate_build_boundaries(root):
    backend = (root/'invoice/backend/Dockerfile').read_text(encoding='utf-8')
    platform = (root/'platform/deploy/docker/go.Dockerfile').read_text(encoding='utf-8')
    web = (root/'platform/deploy/docker/web.Dockerfile').read_text(encoding='utf-8')
    agents = (root/'invoice/agents/Dockerfile.production').read_text(encoding='utf-8')
    go = 'golang:1.27.0-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc'
    require('FROM '+go+' AS build' in backend and 'FROM '+go+' AS builder' in platform, 'unified Go builders drifted from reviewed module-compatible version')
    require(len(re.findall(r'(?m)^FROM alpine:3\.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS (?:api-base|scanner-base)$', backend)) == 2, 'invoice tools/scanner bases drifted')
    require('FROM scanner-base AS scanner' in backend and 'FROM api-base AS api' not in backend, 'separate legacy API or missing scanner stage')
    require(not re.search(r'(?m)^\s*(?:RUN|COPY|ADD)\b[^\n]*(?:qpdf|invoice-pdf-scanner)', platform), 'unified API acquired document parsing executable')
    require('FROM node:24.13.0-alpine@sha256:cd6fb7efa6490f039f3471a189214d5f548c11df1ff9e5b181aa49e22c14383e AS builder' in web and 'FROM nginx:1.28-alpine AS web' in web, 'unified web bases drifted from the existing platform contract')
    require('ARG GO_IMAGE=golang:1.25.13-alpine@sha256:1e0126852075c9c60731c8ba49088448b91f63e2aed97ca9d1a9791622a05946' in agents and 'ARG ALPINE_IMAGE=alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40' in agents, 'source agent builder bases drifted')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--project-root', type=Path, default=Path(__file__).resolve().parents[2])
    args = parser.parse_args()
    validate(args.project_root, *render(args.project_root))
    print('Unified production/source Compose: all retained security, policy, image, scanner and stream contracts passed.')


if __name__ == '__main__':
    main()
