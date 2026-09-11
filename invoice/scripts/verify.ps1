param()

$ErrorActionPreference = 'Stop'

$projectRoot = Split-Path -Parent $PSScriptRoot
$monorepoRoot = Split-Path -Parent $projectRoot
$python = (Get-Command python -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
$postgresCompatibilityFixtureImage = 'postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
. (Join-Path $PSScriptRoot 'release-image-gate-lib.ps1')

function Test-OrdinalStringEqual {
    param(
        [AllowNull()]$Actual,
        [AllowNull()]$Expected
    )
    return [string]::Equals([string]$Actual, [string]$Expected, [StringComparison]::Ordinal)
}

& (Join-Path $PSScriptRoot 'check-no-secrets.ps1')
if ($LASTEXITCODE -ne 0) { throw 'secret-material gate failed' }

& (Join-Path $PSScriptRoot 'test-secret-scan-native-exits.ps1')
if ($LASTEXITCODE -ne 0) { throw 'secret scanner native exit fixtures failed' }

& (Join-Path $PSScriptRoot 'test-secret-scan-file-set.ps1')
if ($LASTEXITCODE -ne 0) { throw 'secret scanner file-set fixtures failed' }

& (Join-Path $PSScriptRoot 'verify-source-readiness-index-operator.ps1')
if ($LASTEXITCODE -ne 0) { throw 'RC39 concurrent readiness-index operator/verifier gate failed' }

& (Join-Path $PSScriptRoot 'verify-balance-history-cleanup-operator.ps1')
if ($LASTEXITCODE -ne 0) { throw 'balance history cleanup operator/rehearsal gate failed' }

& $python -X utf8 (Join-Path $monorepoRoot 'scripts/unified-service.py') check
if ($LASTEXITCODE -ne 0) { throw 'unified release source/image/topology contract fixtures failed' }

& (Join-Path $PSScriptRoot 'test-untracked-git-state.ps1')
if ($LASTEXITCODE -ne 0) { throw 'configured untracked Git state fixtures failed' }

& (Join-Path $PSScriptRoot 'test-hidden-release-artifacts.ps1')
if ($LASTEXITCODE -ne 0) { throw 'hidden release artifact fixtures failed' }

$auditPwsh = (Get-Process -Id $PID).Path
& $auditPwsh -NoProfile -NonInteractive -File (Join-Path $PSScriptRoot 'test-full-audit.ps1')
if ($LASTEXITCODE -ne 0) { throw 'full-audit regression gate failed' }

& $python -X utf8 (Join-Path $PSScriptRoot 'tests/test_unified_verify_adaptation.py') --project-root $monorepoRoot
if ($LASTEXITCODE -ne 0) { throw 'unified verify fail-closed regression tests failed' }

& (Join-Path $PSScriptRoot 'test-verify-postgres.ps1')
if ($LASTEXITCODE -ne 0) { throw 'PostgreSQL 15 container-network static fixtures failed' }

if (-not (Get-Command bash -ErrorAction SilentlyContinue)) {
    throw 'bash is required to run the ClamAV deployment healthcheck tests'
}
Push-Location $projectRoot
try {
    bash scripts/test-deploy-log-markers.sh
    if ($LASTEXITCODE -ne 0) { throw 'deployment log-marker boundary tests failed' }
    bash scripts/test-restore-runtime-env.sh
    if ($LASTEXITCODE -ne 0) { throw 'restore runtime environment tests failed' }
    bash scripts/test-restore-cleanup-state.sh
    if ($LASTEXITCODE -ne 0) { throw 'restore resource-state tests failed' }
    bash deploy/rehearsal/test-shadow-eval.sh
    if ($LASTEXITCODE -ne 0) { throw 'shadow-eval final-decision tests failed' }
    bash scripts/test-deploy-file-guards.sh
    if ($LASTEXITCODE -ne 0) { throw 'deployment file-type guard tests failed' }
    bash scripts/test-clamav-healthcheck.sh
    if ($LASTEXITCODE -ne 0) { throw 'ClamAV deployment healthcheck tests failed' }
    & (Join-Path $PSScriptRoot 'test-posix-permissions-dispatch.ps1')
    if ($LASTEXITCODE -ne 0) { throw 'POSIX permission dispatcher fixtures failed' }
    & (Join-Path $PSScriptRoot 'test-posix-permissions.ps1') -RelativeTestPath 'scripts/test-preserve-source-reader-roles.sh' -ProjectRoot $projectRoot
    if ($LASTEXITCODE -ne 0) { throw 'source reader role-verifier envelope tests failed' }
} finally {
    Pop-Location
}

Push-Location (Join-Path $projectRoot 'backend')
try {
    go test -race -p 1 ./...
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
    if ($LASTEXITCODE -ne 0) { throw 'frontend dependency audit failed; this full gate has no audit waiver' }

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

# Build the native admin host as well: the Nginx gate must inspect fresh assets
# from both origins, never a previous build or a separately served iframe SPA.
Push-Location (Join-Path $monorepoRoot 'platform')
try {
    pnpm --filter admin-web build
    if ($LASTEXITCODE -ne 0) { throw 'native administration frontend build failed' }
} finally { Pop-Location }

& (Join-Path $PSScriptRoot 'verify-backup-signature.ps1')
if ($LASTEXITCODE -ne 0) { throw 'retained restore signature, namespace and tamper verification failed' }

& $python -X utf8 (Join-Path $PSScriptRoot 'verify-unified-nginx.py') --project-root $monorepoRoot
if ($LASTEXITCODE -ne 0) { throw 'unified Nginx syntax, same-origin routes, mTLS and exact security headers failed' }

& (Join-Path $PSScriptRoot 'verify-restore-capacity.ps1')
if ($LASTEXITCODE -ne 0) { throw 'restore PostgreSQL capacity verification failed' }

# Retired IdP routes and native staff identity are tested by the full backend/web
# suites above and the unified host package below, not by starting a retired IdP.
Push-Location (Join-Path $monorepoRoot 'platform')
try {
    go test -race -p 1 ./internal/platform/invoicehost ./cmd/platform-api
    if ($LASTEXITCODE -ne 0) { throw 'unified employee resolver, rejected legacy routes and modular readiness tests failed' }
} finally { Pop-Location }

Push-Location (Join-Path $projectRoot 'agents')
try {
    go test -race -p 1 ./...
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
        $unifiedShellScripts = @(
            '../deploy/rehearsal-unified/rehearse.sh',
            '../deploy/rehearsal-unified/cleanup.sh',
            '../deploy/unified/cutover.sh',
            '../deploy/unified/rollback.sh'
        )
        $scriptsForBash = @($shellScripts | ForEach-Object { $_.Replace('\', '/') }) + $unifiedShellScripts
        foreach ($shellScript in $scriptsForBash) {
            bash -n $shellScript
            if ($LASTEXITCODE -ne 0) { throw "production shell syntax validation failed: $shellScript" }
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

# Unified replacement for the former production/IdP/source Compose matrix.
# The verifier renders only synthetic configuration and preserves scanner, role,
# stream, image, volume, immutable-policy and proxy boundary assertions.
& $python -X utf8 (Join-Path $PSScriptRoot 'verify-unified-contracts.py') --project-root $monorepoRoot
if ($LASTEXITCODE -ne 0) { throw 'unified production/source contract verification failed' }

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

& (Join-Path $PSScriptRoot 'verify-source-agent-contracts.ps1') -ProjectRoot $projectRoot
if ($LASTEXITCODE -ne 0) { throw 'source-agent contract verification failed' }
& (Join-Path $PSScriptRoot 'test-source-agent-contracts.ps1') -ProjectRoot $projectRoot
if ($LASTEXITCODE -ne 0) { throw 'source-agent contract fixtures failed' }

& (Join-Path $PSScriptRoot 'test-upstream-projection-maintenance.ps1') `
    -PostgresImage $postgresCompatibilityFixtureImage
if ($LASTEXITCODE -ne 0) { throw 'upstream projection maintenance adversarial tests failed' }

& (Join-Path $PSScriptRoot 'verify-postgres.ps1') -PostgresImage $postgresCompatibilityFixtureImage
if ($LASTEXITCODE -ne 0) { throw 'isolated PostgreSQL 15/18 verification failed' }

& (Join-Path $PSScriptRoot 'check-upstream-integrity.ps1')
if ($LASTEXITCODE -ne 0) { throw 'upstream integrity check failed' }

Write-Host 'All local verification gates passed.'
