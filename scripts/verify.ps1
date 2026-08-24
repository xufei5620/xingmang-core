param(
    [switch]$SkipPostgres
)

$ErrorActionPreference = 'Stop'

$projectRoot = Split-Path -Parent $PSScriptRoot

function Test-OrdinalStringEqual {
    param(
        [AllowNull()]$Actual,
        [AllowNull()]$Expected
    )
    return [string]::Equals([string]$Actual, [string]$Expected, [StringComparison]::Ordinal)
}

& (Join-Path $PSScriptRoot 'check-no-secrets.ps1')
if ($LASTEXITCODE -ne 0) { throw 'secret-material gate failed' }

& (Join-Path $PSScriptRoot 'test-release-image-gate.ps1')
if ($LASTEXITCODE -ne 0) { throw 'release image gate static fixtures failed' }

if (-not (Get-Command bash -ErrorAction SilentlyContinue)) {
    throw 'bash is required to run the ClamAV deployment healthcheck tests'
}
Push-Location $projectRoot
try {
    bash scripts/test-clamav-healthcheck.sh
    if ($LASTEXITCODE -ne 0) { throw 'ClamAV deployment healthcheck tests failed' }
    bash scripts/test-preserve-source-reader-roles.sh
    if ($LASTEXITCODE -ne 0) { throw 'source reader role-verifier envelope tests failed' }
} finally {
    Pop-Location
}

Push-Location (Join-Path $projectRoot 'backend')
try {
    go test -race ./...
    if ($LASTEXITCODE -ne 0) { throw 'backend tests failed' }
    go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'backend vet failed' }
} finally {
    Pop-Location
}

Push-Location (Join-Path $projectRoot 'web')
try {
    npm test
    if ($LASTEXITCODE -ne 0) { throw 'frontend automated tests failed' }
    npm run typecheck
    if ($LASTEXITCODE -ne 0) { throw 'frontend typecheck failed' }
    npm run build
    if ($LASTEXITCODE -ne 0) { throw 'frontend build failed' }
    npm audit --audit-level=moderate
    if ($LASTEXITCODE -ne 0) { throw 'frontend dependency audit failed' }

    $previousMode = $env:VITE_API_MODE
    $env:VITE_API_MODE = 'http'
    npm run build
    if ($LASTEXITCODE -ne 0) { throw 'frontend HTTP-mode build failed' }
    $mockHeaders = @(rg -a -n 'X-Mock-User-ID|X-Mock-Role' dist 2>$null)
    if ($mockHeaders.Count -ne 0) { throw 'production HTTP bundle contains mock identity headers' }
    if ($null -eq $previousMode) { Remove-Item Env:VITE_API_MODE -ErrorAction SilentlyContinue } else { $env:VITE_API_MODE = $previousMode }
} finally {
    Pop-Location
}

& (Join-Path $PSScriptRoot 'verify-web-security-headers.ps1')
if ($LASTEXITCODE -ne 0) { throw 'frontend Nginx security-header verification failed' }

& (Join-Path $PSScriptRoot 'verify-nginx-configs.ps1')
if ($LASTEXITCODE -ne 0) { throw 'edge Nginx configuration verification failed' }

& (Join-Path $PSScriptRoot 'verify-backup-signature.ps1')
if ($LASTEXITCODE -ne 0) { throw 'backup signature verification failed' }

Push-Location (Join-Path $projectRoot 'agents')
try {
    go test -race ./...
    if ($LASTEXITCODE -ne 0) { throw 'source-agent tests failed' }
    go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'source-agent vet failed' }
} finally {
    Pop-Location
}

docker compose -f (Join-Path $projectRoot 'deploy\docker-compose.dev.yml') config --quiet
if ($LASTEXITCODE -ne 0) { throw 'docker compose validation failed' }

if (Get-Command bash -ErrorAction SilentlyContinue) {
    Push-Location $projectRoot
    try {
        $shellScripts = @(rg --files deploy scripts | Where-Object { $_ -like '*.sh' })
        $scriptsForBash = @($shellScripts | ForEach-Object { $_.Replace('\', '/') })
        bash -n @scriptsForBash
        if ($LASTEXITCODE -ne 0) { throw 'production shell syntax validation failed' }

        bash deploy/validate-keycloak-admin-allowlist.sh deploy/nginx/auth-admin.solov.cc.allow.conf.example
        if ($LASTEXITCODE -ne 0) { throw 'Keycloak admin allowlist example failed exact-host validation' }

        bash -c "KEYCLOAK_EDGE_GATEWAY='172.30.254.1' KC_PROXY_TRUSTED_ADDRESSES='172.30.254.1/32' bash deploy/keycloak/validate-proxy-trust.sh"
        if ($LASTEXITCODE -ne 0) { throw 'exact Keycloak proxy /32 was rejected' }
        foreach ($invalidProxyTrust in @(
            '', '172.30.254.1', '172.30.254.0/29', '172.30.254.1/32,127.0.0.1/32',
            '0172.30.254.1/32', '999.30.254.1/32', '::1/128'
        )) {
            bash -c "KEYCLOAK_EDGE_GATEWAY='172.30.254.1' KC_PROXY_TRUSTED_ADDRESSES='$invalidProxyTrust' bash deploy/keycloak/validate-proxy-trust.sh" 2>$null
            if ($LASTEXITCODE -eq 0) {
                throw "unsafe Keycloak proxy trust value was accepted: $invalidProxyTrust"
            }
        }
        bash -c "KEYCLOAK_EDGE_GATEWAY='172.30.254.2' KC_PROXY_TRUSTED_ADDRESSES='172.30.254.1/32' bash deploy/keycloak/validate-proxy-trust.sh" 2>$null
        if ($LASTEXITCODE -eq 0) {
            throw 'Keycloak proxy trust validator accepted a gateway mismatch'
        }
    } finally {
        Pop-Location
    }
} else {
    throw 'bash is required to validate production shell scripts'
}

$sub2SourceContract = Get-Content -Raw (Join-Path $projectRoot 'contracts\sub2api-source-projection-grants.postgresql.sql')
$newAPISourceContract = Get-Content -Raw (Join-Path $projectRoot 'contracts\newapi-source-projection-grants.postgresql.sql')
if ($sub2SourceContract -notmatch 'ALTER ROLE invoice_sub2api_payments_reader\s+NOLOGIN[\s\S]*?CONNECTION LIMIT 0;[\s\S]*?REVOKE CONNECT' -or
    $newAPISourceContract -notmatch 'ALTER ROLE invoice_newapi_payments_reader\s+NOLOGIN[\s\S]*?CONNECTION LIMIT 0;[\s\S]*?REVOKE CONNECT') {
    throw 'unused V2 payment compatibility roles must remain credential-free NOLOGIN holders'
}

$productionEnv = @{
    INVOICE_IMAGE_TAG = 'verification-build'
    SECRETS_DIR = (Join-Path $projectRoot 'deploy')
    ADMIN_SETTINGS_BOOTSTRAP_FILE = (Join-Path $projectRoot 'deploy\admin-settings.bootstrap.example.json')
    SOURCE_TRUST_CONFIG_FILE = (Join-Path $projectRoot 'deploy\source-trust.example.json')
    SOURCE_INSTANCES_CONFIG_FILE = (Join-Path $projectRoot 'deploy\source-instances.example.json')
    INVOICE_EDGE_SUBNET = '172.30.239.0/28'
    INVOICE_DB_SUBNET = '172.30.240.0/28'
    INVOICE_APP_SUBNET = '172.30.241.0/28'
    CLAMAV_EGRESS_SUBNET = '172.30.242.0/28'
    OIDC_PREFLIGHT_EGRESS_SUBNET = '172.30.243.0/28'
    KEYCLOAK_HTTP_PORT = '58180'
    KEYCLOAK_ADMIN_HTTP_PORT = '58181'
    KEYCLOAK_DB_SUBNET = '172.30.244.0/28'
    KEYCLOAK_EDGE_SUBNET = '172.30.254.0/29'
    KEYCLOAK_EDGE_GATEWAY = '172.30.254.1'
    KC_PROXY_TRUSTED_ADDRESSES = '172.30.254.1/32'
    TRUSTED_PROXY_CIDRS = '172.30.250.1/32'
    INVOICE_PROXY_SUBNET = '172.30.250.0/28'
    INVOICE_PROXY_GATEWAY_IP = '172.30.250.1'
    INVOICE_INGEST_SUBNET = '172.30.251.0/28'
    INVOICE_INGEST_DYNAMIC_RANGE = '172.30.251.0/29'
    INVOICE_INGEST_PROXY_IP = '172.30.251.14'
    INVOICE_INGEST_PROXY_CIDR = '172.30.251.14/32'
    OIDC_ISSUER_URL = 'https://auth.solov.cc/realms/solov'
    OIDC_REQUIRED_ADMIN_ACR = 'urn:solov:loa:2'
    OIDC_ALLOWED_ENDPOINT_HOSTS = 'auth.solov.cc'
    OIDC_ALLOWED_SIGNING_ALGS = 'RS256'
    OIDC_LOGOUT_TOKEN_MAX_AGE = '10m'
    OIDC_MAX_HTTP_RESPONSE_BYTES = '1048576'
    OIDC_PREFLIGHT_TIMEOUT = '30s'
    CLAMAV_MAX_SIGNATURE_AGE = '48h'
    SOURCE_AGENT_VERSION = '0.3.0'
    SOURCE_STATE_ROOT = (Join-Path $projectRoot 'deploy')
    SOURCE_CUTOVER_ROOT = (Join-Path $projectRoot 'deploy\cutover')
    SUB2API_SOURCE_ID = '10000000-0000-4000-8000-000000000001'
    NEWAPI_SOURCE_ID = '10000000-0000-4000-8000-000000000002'
    SUB2API_RUNTIME_VERSION = '0.1.179'
    NEWAPI_RUNTIME_VERSION = 'v1.0.0-rc.25'
    SUB2API_OIDC_PROVIDER_KEY = 'https://auth.solov.cc/realms/solov'
    SUB2API_PROJECTION_NETWORK = 'invoice-sub2api-projection'
    NEWAPI_PROJECTION_NETWORK = 'invoice-newapi-projection'
}
$previousProductionEnv = @{}
try {
    foreach ($entry in $productionEnv.GetEnumerator()) {
        $previousProductionEnv[$entry.Key] = [Environment]::GetEnvironmentVariable($entry.Key, 'Process')
        [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process')
    }
    docker compose -f (Join-Path $projectRoot 'deploy\docker-compose.prod.yml') config --quiet
    if ($LASTEXITCODE -ne 0) { throw 'production docker compose validation failed' }
    $renderedProduction = docker compose -f (Join-Path $projectRoot 'deploy\docker-compose.prod.yml') config --format json | ConvertFrom-Json
    if ($LASTEXITCODE -ne 0) { throw 'cannot inspect rendered production compose' }
    $proxyNetwork = $renderedProduction.services.'ingest-proxy'.networks.invoice_ingest
    if ($renderedProduction.services.api.environment.INGEST_PROXY_CIDRS -ne $productionEnv.INVOICE_INGEST_PROXY_CIDR -or
        $proxyNetwork.ipv4_address -ne $productionEnv.INVOICE_INGEST_PROXY_IP -or
        @($proxyNetwork.aliases) -notcontains 'invoice-ingest.internal' -or
        $renderedProduction.services.'ingest-proxy'.depends_on.api.condition -ne 'service_started') {
        throw 'production ingestion trust is not pinned to the dedicated proxy IP and alias'
    }
    $invoiceProxyIPAM = @($renderedProduction.networks.invoice_proxy.ipam.config)[0]
    if ($invoiceProxyIPAM.subnet -ne $productionEnv.INVOICE_PROXY_SUBNET -or
        $invoiceProxyIPAM.gateway -ne $productionEnv.INVOICE_PROXY_GATEWAY_IP -or
        [int]$renderedProduction.services.api.networks.invoice_proxy.gw_priority -ne 1 -or
        $renderedProduction.services.api.environment.TRUSTED_PROXY_CIDRS -ne "$($invoiceProxyIPAM.gateway)/32" -or
        $renderedProduction.services.api.environment.OIDC_LOGOUT_TOKEN_MAX_AGE -ne $productionEnv.OIDC_LOGOUT_TOKEN_MAX_AGE -or
        $renderedProduction.services.api.environment.OIDC_MAX_HTTP_RESPONSE_BYTES -ne $productionEnv.OIDC_MAX_HTTP_RESPONSE_BYTES) {
        throw 'invoice API trusted proxy/default gateway does not equal the exact configured bridge gateway /32'
    }
    foreach ($network in @(
        @{ Name = 'invoice_edge'; Env = 'INVOICE_EDGE_SUBNET'; Internal = $false },
        @{ Name = 'invoice_db'; Env = 'INVOICE_DB_SUBNET'; Internal = $true },
        @{ Name = 'invoice_app'; Env = 'INVOICE_APP_SUBNET'; Internal = $true },
        @{ Name = 'clamav_egress'; Env = 'CLAMAV_EGRESS_SUBNET'; Internal = $false }
    )) {
        $renderedNetwork = $renderedProduction.networks.($network.Name)
        $ipam = @($renderedNetwork.ipam.config)[0]
        if ($ipam.subnet -ne $productionEnv[$network.Env] -or [bool]$renderedNetwork.internal -ne $network.Internal) {
            throw "$($network.Name) does not use its exact reviewed subnet/internal mode"
        }
    }
    if ($renderedProduction.services.clamav.image -ne 'clamav/clamav:1.4.5@sha256:4de20bd9ab45a4b763c5412b769217ef5082572ebc8a63aff1a77943419e5dd8' -or
        [int]$renderedProduction.services.clamav.networks.clamav_egress.gw_priority -ne 1 -or
        $renderedProduction.networks.invoice_app.internal -ne $true -or
        $renderedProduction.networks.clamav_egress.internal -eq $true -or
        [int64]$renderedProduction.services.clamav.mem_limit -ne 4294967296 -or
        [int]$renderedProduction.services.clamav.pids_limit -ne 256) {
        throw 'ClamAV digest, resources, default egress route or internal application boundary drifted'
    }
    $clamAVHealthTest = @($renderedProduction.services.clamav.healthcheck.test)
    $clamAVHealthScriptMount = @($renderedProduction.services.clamav.volumes | Where-Object { $_.target -eq '/usr/local/bin/invoice-clamav-healthcheck' })
    if (($clamAVHealthTest -join '|') -cne 'CMD|/bin/sh|/usr/local/bin/invoice-clamav-healthcheck' -or
        $renderedProduction.services.clamav.environment.CLAMAV_DATABASE_ROOT -cne '/var/lib/clamav' -or
        $renderedProduction.services.clamav.environment.CLAMAV_MAX_SIGNATURE_AGE -cne $productionEnv.CLAMAV_MAX_SIGNATURE_AGE -or
        $clamAVHealthScriptMount.Count -ne 1 -or
        $clamAVHealthScriptMount[0].read_only -ne $true -or
        $renderedProduction.services.api.depends_on.clamav.condition -cne 'service_healthy' -or
        $renderedProduction.services.api.depends_on.postgres.condition -cne 'service_healthy' -or
        $renderedProduction.services.api.depends_on.'pdf-scanner'.condition -cne 'service_healthy') {
        throw 'API startup is not gated by the reviewed ClamAV/database/scanner health contract'
    }
    $expectedReleaseImages = @{
        api = 'invoice-system-api:verification-build'
        web = 'invoice-system-web:verification-build'
        'pdf-scanner' = 'invoice-system-pdf-scanner:verification-build'
    }
    foreach ($serviceName in $expectedReleaseImages.Keys) {
        $releaseService = $renderedProduction.services.$serviceName
        if (-not (Test-OrdinalStringEqual -Actual $releaseService.image -Expected $expectedReleaseImages[$serviceName]) -or
            -not (Test-OrdinalStringEqual -Actual $releaseService.pull_policy -Expected 'never') -or
            $releaseService.PSObject.Properties.Name -contains 'build') {
            throw "production service $serviceName escaped the exact prebuilt-only INVOICE_IMAGE_TAG"
        }
    }
    $pdfScanner = $renderedProduction.services.'pdf-scanner'
    $scannerSecrets = @($pdfScanner.secrets | ForEach-Object { $_.source })
    $scannerVolumes = @($pdfScanner.volumes)
    $apiScannerVolume = @($renderedProduction.services.api.volumes | Where-Object { $_.target -eq '/scanner' })
    $apiSecrets = @($renderedProduction.services.api.secrets | ForEach-Object { $_.source })
    if ($pdfScanner.image -notlike 'invoice-system-pdf-scanner:*' -or
        $pdfScanner.network_mode -ne 'none' -or
        $pdfScanner.read_only -ne $true -or
        @($pdfScanner.cap_drop) -notcontains 'ALL' -or
        @($pdfScanner.security_opt) -notcontains 'no-new-privileges:true' -or
        [int64]$pdfScanner.mem_limit -ne 268435456 -or
        [int]$pdfScanner.pids_limit -ne 32 -or
        [double]$pdfScanner.cpus -ne 0.5 -or
        $null -eq $pdfScanner.healthcheck) {
        throw 'isolated PDF scanner image/network/resource hardening drifted'
    }
    if ($scannerSecrets.Count -ne 1 -or $scannerSecrets[0] -ne 'invoice_pdf_scanner_capability' -or
        $scannerVolumes.Count -ne 1 -or $scannerVolumes[0].source -ne 'invoice_pdf_scanner_socket' -or $scannerVolumes[0].target -ne '/scanner' -or
        $apiScannerVolume.Count -ne 1 -or $apiScannerVolume[0].source -ne 'invoice_pdf_scanner_socket' -or $apiScannerVolume[0].read_only -ne $true -or
        $apiSecrets -notcontains 'invoice_pdf_scanner_capability' -or
        @($renderedProduction.services.api.group_add) -notcontains '10000' -or
        $renderedProduction.services.api.depends_on.'pdf-scanner'.condition -ne 'service_healthy') {
        throw 'PDF scanner capability/socket boundary is not least-privilege and fail-closed'
    }
    $backendDockerfile = Get-Content -Raw (Join-Path $projectRoot 'backend\Dockerfile')
    $apiStageOffset = $backendDockerfile.LastIndexOf('FROM api-base AS api', [StringComparison]::Ordinal)
    if ($apiStageOffset -lt 0 -or
        -not $backendDockerfile.Contains('FROM golang:1.25.13-alpine@sha256:1e0126852075c9c60731c8ba49088448b91f63e2aed97ca9d1a9791622a05946 AS build') -or
        [regex]::Matches($backendDockerfile, '(?m)^FROM alpine:3\.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS (?:api-base|scanner-base)$').Count -ne 2 -or
        $backendDockerfile.Substring($apiStageOffset) -match '(?m)apk add.*qpdf|/usr/bin/qpdf|invoice-pdf-scanner' -or
        $backendDockerfile -notmatch '(?m)^FROM scanner-base AS scanner$') {
        throw 'backend build bases or minimal API/qpdf image boundary drifted'
    }
    $webDockerfile = Get-Content -Raw (Join-Path $projectRoot 'web\Dockerfile')
    $agentDockerfile = Get-Content -Raw (Join-Path $projectRoot 'agents\Dockerfile.production')
    if (-not $webDockerfile.Contains('FROM node:24-alpine@sha256:d32cdf619f63fe0471182d08996dd516c6275bb5fd31ae06e55a570bd9e1ad43 AS build') -or
        -not $webDockerfile.Contains('FROM nginx:1.30-alpine@sha256:97d490c12ba55b4946b01546d1c3ed324e8d41ab1c9fcb2a616aa470620e5b46') -or
        -not $agentDockerfile.Contains('ARG GO_IMAGE=golang:1.25.13-alpine@sha256:1e0126852075c9c60731c8ba49088448b91f63e2aed97ca9d1a9791622a05946') -or
        -not $agentDockerfile.Contains('ARG ALPINE_IMAGE=alpine:3.23@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40')) {
        throw 'web or source-agent build bases are not pinned to reviewed digests'
    }
    $renderedProductionTools = docker compose --profile tools -f (Join-Path $projectRoot 'deploy\docker-compose.prod.yml') config --format json | ConvertFrom-Json
    if ($LASTEXITCODE -ne 0) { throw 'cannot inspect rendered production tools compose' }
    foreach ($service in @('migrate', 'bootstrap-settings', 'bootstrap-sources', 'document-gc', 'oidc-logout-retention', 'oidc-preflight')) {
        $toolService = $renderedProductionTools.services.$service
        if (-not (Test-OrdinalStringEqual -Actual $toolService.image -Expected 'invoice-system-tools:verification-build') -or
            -not (Test-OrdinalStringEqual -Actual $toolService.pull_policy -Expected 'never') -or
            $toolService.PSObject.Properties.Name -contains 'build') {
            throw "$service does not use the isolated prebuilt-only tools image with the exact reviewed release tag"
        }
    }
    $permissionsService = $renderedProductionTools.services.permissions
    if ([string]$permissionsService.user -ne '10001:10001' -or
        @($permissionsService.secrets | ForEach-Object { $_.source }) -notcontains 'invoice_owner_database_url' -or
        @($permissionsService.cap_drop) -notcontains 'ALL' -or
        @($permissionsService.security_opt) -notcontains 'no-new-privileges:true') {
        throw 'permissions service cannot read its 0400 owner DSN as the isolated tools UID'
    }
    $oidcPreflight = $renderedProductionTools.services.'oidc-preflight'
    $oidcPreflightNetworks = @($oidcPreflight.networks.PSObject.Properties.Name)
    $oidcPreflightNetwork = $renderedProductionTools.networks.oidc_preflight_egress
    $oidcPreflightIPAM = @($oidcPreflightNetwork.ipam.config)[0]
    if ($oidcPreflight.PSObject.Properties.Name -contains 'secrets' -or
        $oidcPreflight.PSObject.Properties.Name -contains 'volumes' -or
        $oidcPreflight.PSObject.Properties.Name -contains 'ports' -or
        $oidcPreflightNetworks.Count -ne 1 -or $oidcPreflightNetworks[0] -ne 'oidc_preflight_egress' -or
        $oidcPreflightIPAM.subnet -ne $productionEnv.OIDC_PREFLIGHT_EGRESS_SUBNET -or
        [bool]$oidcPreflightNetwork.internal -ne $false -or
        @($oidcPreflight.entrypoint) -notcontains '/usr/local/bin/invoice-oidc-preflight' -or
        $oidcPreflight.read_only -ne $true -or
        @($oidcPreflight.cap_drop) -notcontains 'ALL' -or
        @($oidcPreflight.security_opt) -notcontains 'no-new-privileges:true' -or
        [int64]$oidcPreflight.mem_limit -ne 134217728 -or
        [int]$oidcPreflight.pids_limit -ne 32 -or
        [double]$oidcPreflight.cpus -ne 0.25 -or
        $oidcPreflight.environment.OIDC_ISSUER_URL -ne $productionEnv.OIDC_ISSUER_URL -or
        $oidcPreflight.environment.OIDC_ALLOWED_ENDPOINT_HOSTS -ne $productionEnv.OIDC_ALLOWED_ENDPOINT_HOSTS -or
        $oidcPreflight.environment.OIDC_ALLOWED_SIGNING_ALGS -ne $productionEnv.OIDC_ALLOWED_SIGNING_ALGS -or
        $oidcPreflight.environment.OIDC_ALLOWED_PRIVATE_ENDPOINT_IPS -ne $renderedProduction.services.api.environment.OIDC_ALLOWED_PRIVATE_ENDPOINT_IPS -or
        $oidcPreflight.environment.OIDC_MAX_HTTP_RESPONSE_BYTES -ne $productionEnv.OIDC_MAX_HTTP_RESPONSE_BYTES -or
        $oidcPreflight.environment.OIDC_PREFLIGHT_TIMEOUT -ne $productionEnv.OIDC_PREFLIGHT_TIMEOUT) {
        throw 'OIDC provider preflight is not a secret-free, exact-policy tools-only gate'
    }
    if (-not $backendDockerfile.Contains('/out/invoice-oidc-preflight ./cmd/oidc-preflight') -or
        -not $backendDockerfile.Contains('/out/invoice-oidc-preflight /usr/local/bin/')) {
        throw 'OIDC provider preflight binary is missing from the production tools image'
    }
    docker compose -f (Join-Path $projectRoot 'deploy\docker-compose.idp.yml') config --quiet
    if ($LASTEXITCODE -ne 0) { throw 'Keycloak docker compose validation failed' }
    $renderedIDPBase = (docker compose -f (Join-Path $projectRoot 'deploy\docker-compose.idp.yml') config --format json | Out-String)
    if ($LASTEXITCODE -ne 0) { throw 'cannot inspect rendered base Keycloak compose' }
    if ($renderedIDPBase -match 'KC_BOOTSTRAP_ADMIN|keycloak_bootstrap_admin_password') {
        throw 'base Keycloak compose permanently mounts bootstrap administration credentials'
    }
    $idpBaseObject = $renderedIDPBase | ConvertFrom-Json
    $expectedPostgresImage = 'postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
    if ($renderedProduction.services.postgres.image -ne $expectedPostgresImage -or
        $renderedProductionTools.services.permissions.image -ne $expectedPostgresImage -or
        $idpBaseObject.services.'keycloak-postgres'.image -ne $expectedPostgresImage) {
        throw 'PostgreSQL runtime/tool images are not pinned to the reviewed digest'
    }
    $invoicePGData = @($renderedProduction.services.postgres.volumes | Where-Object { $_.source -eq 'invoice_postgres_data' })
    $keycloakPGData = @($idpBaseObject.services.'keycloak-postgres'.volumes | Where-Object { $_.source -eq 'keycloak_postgres_data' })
    if ($invoicePGData.Count -ne 1 -or $invoicePGData[0].target -ne '/var/lib/postgresql' -or
        $keycloakPGData.Count -ne 1 -or $keycloakPGData[0].target -ne '/var/lib/postgresql') {
        throw 'PostgreSQL 18 data volumes must mount the version-aware /var/lib/postgresql parent'
    }
    $keycloakSecretSources = @($idpBaseObject.services.keycloak.secrets | ForEach-Object { $_.source })
    if ($idpBaseObject.services.keycloak.environment.KC_DB_USERNAME -ne 'keycloak_app' -or
        $keycloakSecretSources -contains 'keycloak_owner_db_password' -or
        $keycloakSecretSources -notcontains 'keycloak_app_db_password' -or
        $idpBaseObject.services.'keycloak-postgres'.environment.POSTGRES_USER -ne 'keycloak_owner') {
        throw 'Keycloak runtime database role is not isolated from the PostgreSQL owner'
    }
    $expectedKeycloakBase = 'quay.io/keycloak/keycloak:26.7.2@sha256:6efbadc00f0ed0237610becf11f4101b9c3ad8edf08a5b70c97aa4154ed436ec'
    $keycloakDockerfile = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\keycloak\Dockerfile')
    if (-not $keycloakDockerfile.Contains("ARG KEYCLOAK_BASE_IMAGE=$expectedKeycloakBase") -or
        [regex]::Matches($keycloakDockerfile, '(?m)^FROM \$\{KEYCLOAK_BASE_IMAGE\}(?: AS builder)?$').Count -ne 2 -or
        -not (Test-OrdinalStringEqual -Actual $idpBaseObject.services.keycloak.image -Expected 'invoice-keycloak:verification-build') -or
        -not (Test-OrdinalStringEqual -Actual $idpBaseObject.services.keycloak.pull_policy -Expected 'never') -or
        $idpBaseObject.services.keycloak.PSObject.Properties.Name -contains 'build' -or
        [regex]::Matches($keycloakDockerfile, '(?m)^(?:RUN|\s*&&) rm -rf /opt/keycloak/bin/client \\$').Count -ne 2 -or
        [regex]::Matches($keycloakDockerfile, '(?m)^\s*&& rm -f /opt/keycloak/lib/lib/main/com\.microsoft\.sqlserver\.mssql-jdbc-\*\.jar \\$').Count -ne 2 -or
        [regex]::Matches($keycloakDockerfile, '(?m)^\s*&& test ! -e /opt/keycloak/bin/client \\$').Count -ne 2) {
        throw 'Keycloak build is not pinned to exact 26.7.2 or does not prune admin CLI/MSSQL artifacts in both stages'
    }
    $keycloakEnvironment = $idpBaseObject.services.keycloak.environment
    if ($keycloakEnvironment.KC_HOSTNAME -ne 'https://auth.solov.cc' -or
        $keycloakEnvironment.KC_HOSTNAME_ADMIN -ne 'https://auth-admin.solov.cc' -or
        $keycloakEnvironment.KC_PROXY_HEADERS -ne 'xforwarded' -or
        $keycloakEnvironment.KC_PROXY_TRUSTED_ADDRESSES -ne $productionEnv.KC_PROXY_TRUSTED_ADDRESSES -or
        $keycloakEnvironment.KEYCLOAK_EDGE_GATEWAY -ne $productionEnv.KEYCLOAK_EDGE_GATEWAY -or
        $keycloakEnvironment.JAVA_OPTS_KC_HEAP -ne '-XX:MinHeapFreeRatio=10 -XX:MaxHeapFreeRatio=30 -XX:MaxRAMPercentage=65') {
        throw 'Keycloak hostname or exact proxy trust policy drifted'
    }
    $keycloakPublishedPorts = @(
        $idpBaseObject.services.keycloak.ports |
            ForEach-Object { "$($_.host_ip):$($_.published):$($_.target)" }
    )
    if ($keycloakPublishedPorts -notcontains '127.0.0.1:58180:8080' -or
        $keycloakPublishedPorts -notcontains '127.0.0.1:58181:8080') {
        throw 'Keycloak public/admin listeners are not split across loopback-only host ports'
    }
    $keycloakEdgeIPAM = @($idpBaseObject.networks.keycloak_edge.ipam.config)[0]
    $keycloakDBIPAM = @($idpBaseObject.networks.keycloak_db.ipam.config)[0]
    if ($keycloakEdgeIPAM.subnet -ne $productionEnv.KEYCLOAK_EDGE_SUBNET -or
        $keycloakEdgeIPAM.gateway -ne $productionEnv.KEYCLOAK_EDGE_GATEWAY -or
        $keycloakDBIPAM.subnet -ne $productionEnv.KEYCLOAK_DB_SUBNET -or
        $idpBaseObject.networks.keycloak_db.internal -ne $true -or
        [int]$idpBaseObject.services.keycloak.networks.keycloak_edge.gw_priority -ne 1 -or
        $productionEnv.KC_PROXY_TRUSTED_ADDRESSES -ne "$($keycloakEdgeIPAM.gateway)/32") {
        throw 'Keycloak proxy trust/default gateway does not equal the exact configured edge gateway /32'
    }
    $idpBootstrapOverride = Join-Path $projectRoot 'deploy\docker-compose.idp.bootstrap.yml'
    docker compose -f (Join-Path $projectRoot 'deploy\docker-compose.idp.yml') -f $idpBootstrapOverride config --quiet
    if ($LASTEXITCODE -ne 0) { throw 'one-time Keycloak bootstrap compose validation failed' }
    $renderedIDPBootstrap = (docker compose -f (Join-Path $projectRoot 'deploy\docker-compose.idp.yml') -f $idpBootstrapOverride config --format json | Out-String)
    if ($LASTEXITCODE -ne 0 -or $renderedIDPBootstrap -notmatch 'KC_BOOTSTRAP_ADMIN_USERNAME' -or $renderedIDPBootstrap -notmatch 'keycloak_bootstrap_admin_password') {
        throw 'one-time Keycloak bootstrap override does not mount both username and secret'
    }
    docker compose -f (Join-Path $projectRoot 'deploy\docker-compose.sources.yml') config --quiet
    if ($LASTEXITCODE -ne 0) { throw 'source-agent docker compose validation failed' }
    $renderedSources = docker compose -f (Join-Path $projectRoot 'deploy\docker-compose.sources.yml') config --format json | ConvertFrom-Json
    if ($LASTEXITCODE -ne 0) { throw 'cannot inspect rendered source-agent compose' }
    $economicServices = @('sub2api-payments','sub2api-usage','sub2api-credits','sub2api-balances','newapi-payments','newapi-usage','newapi-credits','newapi-balances')
    $identityServices = @('sub2api-identities','newapi-identities')
    foreach ($service in ($economicServices + $identityServices)) {
        $sourceService = $renderedSources.services.$service
        if (-not (Test-OrdinalStringEqual -Actual $sourceService.image -Expected 'invoice-source-agent:verification-build') -or
            -not (Test-OrdinalStringEqual -Actual $sourceService.pull_policy -Expected 'never') -or
            $sourceService.PSObject.Properties.Name -contains 'build') {
            throw "$service escaped the common prebuilt-only release image tag"
        }
        if ($renderedSources.services.$service.environment.INGESTION_ALLOWED_CIDRS -ne $productionEnv.INVOICE_INGEST_PROXY_CIDR) {
            throw "$service does not pin ingestion DNS to the proxy /32"
        }
        if ($null -eq $renderedSources.services.$service.healthcheck) { throw "$service is missing its local healthcheck" }
        if ($service -in $identityServices -and ($renderedSources.services.$service.environment.SOURCE_SCHEMA_VERSION -ne '2.0' -or $renderedSources.services.$service.environment.SOURCE_RECONCILE_FILE -ne '/state/reconcile.json')) { throw "$service is missing V2 durable reconciliation" }
        if ($service -in $economicServices -and ($renderedSources.services.$service.environment.SOURCE_SCHEMA_VERSION -ne '3.0' -or $null -ne $renderedSources.services.$service.environment.SOURCE_RECONCILE_FILE -or $renderedSources.services.$service.environment.SOURCE_CUTOVER_MANIFEST_FILE -ne '/cutover/manifest.enc')) { throw "$service V3 cutover/state contract drifted" }
    }
    $renderedSourceCutover = docker compose --profile cutover -f (Join-Path $projectRoot 'deploy\docker-compose.sources.yml') config --format json | ConvertFrom-Json
    if ($LASTEXITCODE -ne 0) { throw 'cannot inspect rendered source-agent cutover compose' }
    foreach ($service in @('sub2api-cutover-init', 'newapi-cutover-init')) {
        $cutoverService = $renderedSourceCutover.services.$service
        if (-not (Test-OrdinalStringEqual -Actual $cutoverService.image -Expected 'invoice-source-agent:verification-build') -or
            -not (Test-OrdinalStringEqual -Actual $cutoverService.pull_policy -Expected 'never') -or
            $cutoverService.PSObject.Properties.Name -contains 'build') {
            throw "$service escaped the common prebuilt-only release image tag"
        }
    }
    $trustExample = Get-Content -Raw (Join-Path $projectRoot 'deploy\source-trust.example.json') | ConvertFrom-Json
    $serviceTrust = @{
        'sub2api-payments' = @('sub2api','payments')
        'sub2api-identities' = @('sub2api','identities')
        'newapi-payments' = @('newapi','payments')
        'newapi-identities' = @('newapi','identities')
        'sub2api-usage' = @('sub2api','usage')
        'sub2api-credits' = @('sub2api','credits')
        'sub2api-balances' = @('sub2api','balances')
        'newapi-usage' = @('newapi','usage')
        'newapi-credits' = @('newapi','credits')
        'newapi-balances' = @('newapi','balances')
    }
    foreach ($service in $serviceTrust.Keys) {
        $sourceType, $stream = $serviceTrust[$service]
        $source = @($trustExample.sources | Where-Object { $_.source_type -eq $sourceType })
        $keyId = [string]$renderedSources.services.$service.environment.SOURCE_SIGNING_KEY_ID
        $streamTrust = $source[0].streams.$stream
        if ($source.Count -ne 1 -or $null -eq $streamTrust -or
            $keyId -notin @($streamTrust.signing_keys.PSObject.Properties.Name)) {
            throw "$service signing key ID $keyId does not match source-trust.example.json"
        }
    }
} finally {
    foreach ($entry in $previousProductionEnv.GetEnumerator()) {
        [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, 'Process')
    }
}

Get-Content -Raw (Join-Path $projectRoot 'contracts\source-agent-batch.v1.schema.json') | ConvertFrom-Json | Out-Null
Get-Content -Raw (Join-Path $projectRoot 'contracts\examples\source-agent-batch.mock.json') | ConvertFrom-Json | Out-Null
$v2Schema = Get-Content -Raw (Join-Path $projectRoot 'contracts\source-agent-batch.v2.schema.json') | ConvertFrom-Json
$v2Example = Get-Content -Raw (Join-Path $projectRoot 'contracts\examples\source-agent-batch.v2.newapi.json') | ConvertFrom-Json
$v2Signature = Get-Content -Raw (Join-Path $projectRoot 'contracts\examples\source-agent-signature.v2.json') | ConvertFrom-Json
$v2Required = @($v2Schema.required)
foreach ($field in @('source_instance_id','stream_id','source_type','projection_status')) {
    if ($field -notin $v2Required) { throw "v2 schema does not require $field" }
}
if ($v2Schema.additionalProperties -ne $false -or
    (@($v2Schema.properties.source_type.enum) -join ',') -ne 'sub2api,newapi' -or
    (@($v2Schema.'$defs'.streamId.enum) -join ',') -ne 'payments,identities' -or
    (@($v2Schema.properties.projection_status.enum) -join ',') -ne 'healthy,blocked') {
    throw 'v2 schema production identity or projection health contract drifted'
}
if ($v2Example.source_instance_id -notmatch '^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$' -or
    $v2Example.stream_id -notin @('payments','identities') -or
    $v2Example.source_type -notin @('sub2api','newapi') -or
    $v2Example.projection_status -notin @('healthy','blocked') -or
    $v2Signature.batch_file -ne 'source-agent-batch.v2.newapi.json') {
    throw 'v2 signed example is outside the production schema contract'
}

$v3Schema = Get-Content -Raw (Join-Path $projectRoot 'contracts\source-agent-batch.v3.schema.json') | ConvertFrom-Json
$v3Example = Get-Content -Raw (Join-Path $projectRoot 'contracts\examples\source-agent-batch.v3.sub2api-usage.json') | ConvertFrom-Json
$v3Required = @($v3Schema.required)
foreach ($field in @('stream_watermark_at','source_cursor','scan_ceiling_at','scan_ceiling_cursor','scan_cycle_id','scan_complete')) {
    if ($field -notin $v3Required) { throw "v3 schema does not require $field" }
}
if ($v3Schema.additionalProperties -ne $false -or (@($v3Schema.properties.stream_id.enum) -join ',') -ne 'payments,usage,credits,balances' -or
    $v3Schema.'$defs'.paymentOrderPayload.additionalProperties -ne $false -or
    $v3Schema.'$defs'.paymentCandidatePayload.additionalProperties -ne $false -or
    $v3Schema.'$defs'.paymentAdjustmentPayload.additionalProperties -ne $false) {
    throw 'v3 economic stream or strict payment schema drifted'
}
if ($v3Example.schema_version -ne '3.0' -or $v3Example.stream_id -ne 'usage' -or $v3Example.scan_complete -ne $false -or $v3Example.records[0].entity_type -ne 'usage_event') { throw 'v3 fixed example drifted' }

if (-not $SkipPostgres) {
    & (Join-Path $PSScriptRoot 'test-upstream-projection-maintenance.ps1') `
        -PostgresImage 'postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
    if ($LASTEXITCODE -ne 0) { throw 'upstream projection maintenance adversarial tests failed' }

    & (Join-Path $projectRoot 'agents\scripts\verify-bridge-postgres-matrix.ps1')
    if ($LASTEXITCODE -ne 0) { throw 'PostgreSQL 15/18 source Bridge V4 matrix failed' }

    & (Join-Path $PSScriptRoot 'verify-postgres.ps1')
    if ($LASTEXITCODE -ne 0) { throw 'isolated PostgreSQL verification failed' }
}

& (Join-Path $PSScriptRoot 'check-upstream-integrity.ps1')
if ($LASTEXITCODE -ne 0) { throw 'upstream integrity check failed' }

Write-Host 'All local verification gates passed.'
