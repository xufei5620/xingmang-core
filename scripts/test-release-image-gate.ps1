$ErrorActionPreference = 'Stop'

$gateSource = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'release-image-gate.ps1')
if ([regex]::Matches($gateSource, "'--timeout', '15m'").Count -lt 4) {
    throw 'release image gate does not apply the reviewed 15-minute Trivy timeout to database updates, scans and SBOM generation'
}
Set-StrictMode -Version Latest

$projectRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'release-image-gate-lib.ps1')

$idpCompose = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\docker-compose.idp.yml')
$artifactVerifier = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify-release-image-artifacts.ps1')
$sourceVerifier = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify.ps1')
$readinessIndexGate = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify-source-readiness-index-operator.ps1')
$keycloakProvisioningVerifier = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify-keycloak-provisioning.ps1')
if (-not $idpCompose.Contains('    image: invoice-keycloak:${INVOICE_IMAGE_TAG:?set the exact reviewed invoice release tag}') -or
    $idpCompose.Contains('    image: invoice-keycloak:26.7.2') -or
    -not $artifactVerifier.Contains('Get-CommonReleaseImageTag -ImageRecords $records -IdPMode $idpMode') -or
    -not $artifactVerifier.Contains('production Keycloak Compose does not use the manifest-bound release image tag') -or
    -not $sourceVerifier.Contains("Test-OrdinalStringEqual -Actual `$idpBaseObject.services.keycloak.image -Expected 'invoice-keycloak:verification-build'") -or
    -not $sourceVerifier.Contains("Test-OrdinalStringEqual -Actual `$idpBaseObject.services.keycloak.pull_policy -Expected 'never'")) {
    throw 'Keycloak production Compose can escape the common manifest-bound release image tag'
}
if (-not $sourceVerifier.Contains("& (Join-Path `$PSScriptRoot 'verify-source-readiness-index-operator.ps1')", [StringComparison]::Ordinal) -or
    -not $gateSource.Contains('readinessIndexOperatorGateSha256 = Get-FileSha256Lower', [StringComparison]::Ordinal) -or
    -not $gateSource.Contains('readinessIndexOperatorSha256 = Get-FileSha256Lower', [StringComparison]::Ordinal) -or
    -not $gateSource.Contains('readinessIndexVerifierSha256 = Get-FileSha256Lower', [StringComparison]::Ordinal) -or
    -not $artifactVerifier.Contains('manifest.tools.readinessIndexOperatorGateSha256', [StringComparison]::Ordinal) -or
    -not $artifactVerifier.Contains('manifest.tools.readinessIndexOperatorSha256', [StringComparison]::Ordinal) -or
    -not $artifactVerifier.Contains('manifest.tools.readinessIndexVerifierSha256', [StringComparison]::Ordinal) -or
    -not $readinessIndexGate.Contains('readiness query execution exceeded the 2000ms hard limit', [StringComparison]::Ordinal)) {
    throw 'RC39 readiness-index operator/verifier escaped the source or release artifact gates'
}
if ($keycloakProvisioningVerifier -notmatch 'tr -d ''\\r\\n''' -or
    $keycloakProvisioningVerifier -notmatch 'test "\$\(wc -l <"\$tmp"\)" -eq 1' -or
    $keycloakProvisioningVerifier -notmatch '/run/test-secrets/admin_auth_header' -or
    $keycloakProvisioningVerifier -notmatch 'curl --header @/run/test-secrets/admin_auth_header' -or
    $keycloakProvisioningVerifier -notmatch 'http://keycloak:8080/admin/realms/' -or
    $keycloakProvisioningVerifier -notmatch 'http://keycloak:8080/realms/master/\.well-known/openid-configuration' -or
    $keycloakProvisioningVerifier -notmatch 'rm -f "\$password_request"' -or
    $keycloakProvisioningVerifier -notmatch '\^\[A-Za-z0-9_-\]\+\\\.\[A-Za-z0-9_-\]\+\\\.\[A-Za-z0-9_-\]\+\$' -or
    $keycloakProvisioningVerifier -notmatch 'test "\$\{#token\}" -ge 64' -or
    $keycloakProvisioningVerifier -notmatch 'test "\$\{#token\}" -le 262144' -or
    $keycloakProvisioningVerifier -notmatch '\$Path\.Contains\(''\.\.''' -or
    $keycloakProvisioningVerifier -notmatch '\(\?i\)%2f\|%5c' -or
    $keycloakProvisioningVerifier -notmatch 'Admin GET JSON root is not an object or array' -or
    $keycloakProvisioningVerifier -match 'http://127\.0\.0\.1:\$Port/admin/realms/' -or
    $keycloakProvisioningVerifier -match '--publish') {
    throw 'Keycloak provisioning verification can expose credentials or depend on Docker host-port NAT for Admin REST'
}

$validFixtureJWT = ('a' * 32) + '.' + ('b' * 32) + '.' + ('c' * 32)
$multilineFixtureJWT = $validFixtureJWT + "`n" + 'header = "X-Probe: injected"'
$fixtureJWTPattern = '^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$'
if ($validFixtureJWT -cnotmatch $fixtureJWTPattern -or
    @($validFixtureJWT -split "`n").Count -ne 1 -or
    @($multilineFixtureJWT -split "`n").Count -eq 1) {
    throw 'Keycloak fixture JWT single-line validation contract is incomplete'
}

if (-not (Test-OrdinalStringEqual -Actual 'invoice-keycloak:verification-build' -Expected 'invoice-keycloak:verification-build') -or
    (Test-OrdinalStringEqual -Actual 'invoice-keycloak:Verification-build' -Expected 'invoice-keycloak:verification-build') -or
    (Test-OrdinalStringEqual -Actual 'Never' -Expected 'never')) {
    throw 'production image or pull-policy comparison is not ordinal and case-sensitive'
}

$matchingTagRecords = @(
    [pscustomobject]@{ name = 'api'; reference = 'invoice-system-api:fixture' }
    [pscustomobject]@{ name = 'pdf-scanner'; reference = 'invoice-system-pdf-scanner:fixture' }
    [pscustomobject]@{ name = 'tools'; reference = 'invoice-system-tools:fixture' }
    [pscustomobject]@{ name = 'web'; reference = 'invoice-system-web:fixture' }
    [pscustomobject]@{ name = 'source-agent'; reference = 'invoice-source-agent:fixture' }
)
if ((Get-CommonReleaseImageTag -ImageRecords $matchingTagRecords -IdPMode external-managed) -cne 'fixture' -or
    (Get-CommonReleaseImageTag -ImageRecords $matchingTagRecords -IdPMode none) -cne 'fixture') {
    throw 'non-Keycloak IdP modes no longer accept the five common application release images'
}
$keycloakRecords = @($matchingTagRecords) + [pscustomobject]@{ name = 'keycloak'; reference = 'invoice-keycloak:fixture' }
if ((Get-CommonReleaseImageTag -ImageRecords $keycloakRecords -IdPMode keycloak) -cne 'fixture') {
    throw 'matching Keycloak release image tag was rejected'
}
$keycloakRecords[-1].reference = 'invoice-keycloak:different'
$rejected = $false
try { Get-CommonReleaseImageTag -ImageRecords $keycloakRecords -IdPMode keycloak | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'mismatched Keycloak release image tag was accepted' }
$keycloakRecords[-1].reference = 'invoice-keycloak:Fixture'
$rejected = $false
try { Get-CommonReleaseImageTag -ImageRecords $keycloakRecords -IdPMode keycloak | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'case-mismatched Keycloak release image tag was accepted' }

$productionRunbookLines = Get-Content -LiteralPath (Join-Path $projectRoot 'docs\PRODUCTION-RUNBOOK.md')
foreach ($line in $productionRunbookLines) {
    if ($line -match '\bup -d\b' -and $line -notmatch '--no-build') {
        throw "production Compose command can rebuild outside the release gate: $line"
    }
    if ($line -match '\brun --rm\b' -and $line -notmatch '^docker run\b' -and $line -notmatch '--pull never') {
        throw "production Compose one-shot command can pull outside the release gate: $line"
    }
}
$productionComposeText = @(
    Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\docker-compose.prod.yml')
    Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\docker-compose.sources.yml')
    Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\docker-compose.idp.yml')
) -join "`n"
if ($productionComposeText -match '(?m)^\s+build:\s*$') {
    throw 'production Compose retains a local build directive outside the release gate'
}

$backendDockerfile = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'backend\Dockerfile')
$webDockerfile = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'web\Dockerfile')
$keycloakDockerfile = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\keycloak\Dockerfile')
$postgresDockerfilePath = Join-Path $projectRoot 'deploy\postgres\Dockerfile'
$postgresGoModPath = Join-Path $projectRoot 'deploy\postgres\gosu-build\go.mod'
$postgresGoSumPath = Join-Path $projectRoot 'deploy\postgres\gosu-build\go.sum'
$clamavDockerfilePath = Join-Path $projectRoot 'deploy\clamav\Dockerfile'
$ingestDockerfilePath = Join-Path $projectRoot 'deploy\ingest-proxy\Dockerfile'
if (-not (Test-Path -LiteralPath $postgresDockerfilePath) -or
    -not (Test-Path -LiteralPath $postgresGoModPath) -or
    -not (Test-Path -LiteralPath $postgresGoSumPath) -or
    -not (Test-Path -LiteralPath $clamavDockerfilePath) -or
    -not (Test-Path -LiteralPath $ingestDockerfilePath)) {
    throw 'RC49 derived PostgreSQL, ClamAV, and ingest runtime image sources are missing'
}

$postgresDockerfile = Get-Content -Raw -LiteralPath $postgresDockerfilePath
$postgresGoMod = Get-Content -Raw -LiteralPath $postgresGoModPath
$postgresGoSum = Get-Content -Raw -LiteralPath $postgresGoSumPath
$clamavDockerfile = Get-Content -Raw -LiteralPath $clamavDockerfilePath
$ingestDockerfile = Get-Content -Raw -LiteralPath $ingestDockerfilePath
$fixedAlpinePackages = 'libcrypto3=3.5.8-r0 libssl3=3.5.8-r0'
$goBuilderReference = 'golang:1.25.13-alpine@sha256:1e0126852075c9c60731c8ba49088448b91f63e2aed97ca9d1a9791622a05946'
$postgresBaseReference = 'postgres:18.6-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
$clamavBaseReference = 'clamav/clamav:1.4.5@sha256:4de20bd9ab45a4b763c5412b769217ef5082572ebc8a63aff1a77943419e5dd8'
$nginxBaseReference = 'nginx:1.30-alpine@sha256:97d490c12ba55b4946b01546d1c3ed324e8d41ab1c9fcb2a616aa470620e5b46'
$keycloakBaseReference = 'quay.io/keycloak/keycloak:26.7.2@sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067'
$gosuModuleVersion = 'v0.0.0-20250923190938-6456aaa0f3c8'
$gosuExpectedVersion = '1.19 (go1.25.13 on linux/amd64; gc)'
if (-not $backendDockerfile.Contains($fixedAlpinePackages) -or
    -not $webDockerfile.Contains($fixedAlpinePackages) -or
    -not $postgresDockerfile.Contains($fixedAlpinePackages) -or
    -not $clamavDockerfile.Contains($fixedAlpinePackages) -or
    -not $ingestDockerfile.Contains($fixedAlpinePackages)) {
    throw 'RC49 runtime Alpine stages do not pin both fixed OpenSSL packages'
}
if (-not $postgresDockerfile.Contains($goBuilderReference) -or
    -not $postgresDockerfile.Contains($postgresBaseReference) -or
    -not $postgresGoMod.Contains($gosuModuleVersion) -or
    -not $postgresDockerfile.Contains('CGO_ENABLED=0') -or
    -not $postgresDockerfile.Contains('-mod=readonly') -or
    -not $postgresDockerfile.Contains('-trimpath') -or
    -not $postgresDockerfile.Contains('-buildvcs=false') -or
    -not $postgresDockerfile.Contains($gosuExpectedVersion) -or
    -not $postgresDockerfile.Contains('test "$TARGETOS" = "linux"') -or
    -not $postgresDockerfile.Contains('test "$TARGETARCH" = "amd64"') -or
    $postgresDockerfile.Contains('go mod download -mod=readonly')) {
    throw 'RC49 PostgreSQL gosu rebuild is not pinned, platform-gated, and read-only at build time'
}
if (-not $postgresGoMod.Contains('module invoice.local/gosu-build') -or
    -not $postgresGoMod.Contains('go 1.25.0') -or
    -not $postgresGoMod.Contains("github.com/tianon/gosu $gosuModuleVersion") -or
    -not $postgresGoMod.Contains('github.com/moby/sys/user v0.1.0 // indirect') -or
    -not $postgresGoMod.Contains('golang.org/x/sys v0.1.0 // indirect')) {
    throw 'RC49 PostgreSQL gosu module contract drifted'
}
foreach ($moduleSum in @(
    'github.com/moby/sys/user v0.1.0 h1:WmZ93f5Ux6het5iituh9x2zAG7NFY9Aqi49jjE1PaQg=',
    'github.com/moby/sys/user v0.1.0/go.mod h1:fKJhFOnsCN6xZ5gSfbM6zaHGgDJMrqt9/reuj4T7MmU=',
    'github.com/tianon/gosu v0.0.0-20250923190938-6456aaa0f3c8 h1:HIpXk5mGBQGfOqcaBbRT4Vnss8NPICnMGlD5xTlPBdQ=',
    'github.com/tianon/gosu v0.0.0-20250923190938-6456aaa0f3c8/go.mod h1:SwhRwWsO6iqXZN9CpIaU9CnOrUqpWDINW16KaaSqnrU=',
    'golang.org/x/sys v0.1.0 h1:kunALQeHf1/185U1i0GOB/fy1IPRDDpuoOOqRReG57U=',
    'golang.org/x/sys v0.1.0/go.mod h1:oPkhp1MJrh7nUepCBck5+mAzfO9JrbApNNgaTdGDITg='
)) {
    if (-not $postgresGoSum.Contains($moduleSum)) {
        throw "RC49 PostgreSQL gosu module sum is missing: $moduleSum"
    }
}
if (-not $clamavDockerfile.Contains($clamavBaseReference) -or
    -not $clamavDockerfile.Contains('ClamAV 1.4.5') -or
    -not $ingestDockerfile.Contains($nginxBaseReference) -or
    -not $ingestDockerfile.Contains('nginx/1.30.4') -or
    -not $keycloakDockerfile.Contains($keycloakBaseReference) -or
    [regex]::Matches($keycloakDockerfile, [regex]::Escape($keycloakBaseReference)).Count -ne 2) {
    throw 'RC49 derived runtime or both Keycloak stages are not pinned to reviewed bases'
}

$localPostgresImage = 'invoice-postgres:${INVOICE_IMAGE_TAG:?set the exact reviewed invoice release tag}'
$localClamavImage = 'invoice-clamav:${INVOICE_IMAGE_TAG:?set the exact reviewed invoice release tag}'
$localIngestImage = 'invoice-ingest-proxy:${INVOICE_IMAGE_TAG:?set the exact reviewed invoice release tag}'
if ([regex]::Matches($productionComposeText, [regex]::Escape($localPostgresImage)).Count -ne 3 -or
    [regex]::Matches($productionComposeText, [regex]::Escape($localClamavImage)).Count -ne 1 -or
    [regex]::Matches($productionComposeText, [regex]::Escape($localIngestImage)).Count -ne 1 -or
    $productionComposeText -match '(?m)^\s+image:\s+postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2\s*$' -or
    $productionComposeText -match '(?m)^\s+image:\s+clamav/clamav:1\.4\.5@sha256:4de20bd9ab45a4b763c5412b769217ef5082572ebc8a63aff1a77943419e5dd8\s*$' -or
    $productionComposeText -match '(?m)^\s+image:\s+nginx:1\.30-alpine@sha256:97d490c12ba55b4946b01546d1c3ed324e8d41ab1c9fcb2a616aa470620e5b46\s*$') {
    throw 'RC49 production Compose retains external PostgreSQL, ClamAV, or ingest runtime image references'
}
foreach ($composeService in @(
    @{ Text = $productionComposeText; Service = 'postgres' },
    @{ Text = $productionComposeText; Service = 'permissions' },
    @{ Text = $idpCompose; Service = 'keycloak-postgres' },
    @{ Text = $productionComposeText; Service = 'clamav' },
    @{ Text = $productionComposeText; Service = 'ingest-proxy' }
)) {
    if ($composeService.Text -notmatch "(?ms)^  $([regex]::Escape($composeService.Service)):\r?\n(?:(?!^  [A-Za-z0-9_-]+:).)*^    pull_policy: never\r?$") {
        throw "RC49 $($composeService.Service) Compose service can pull outside the reviewed release image set"
    }
}

$bridgeMatrixVerifier = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'agents\scripts\verify-bridge-postgres-matrix.ps1')
if ($bridgeMatrixVerifier -notmatch '\$maxAttempts = 5' -or
    $bridgeMatrixVerifier -notmatch 'for \(\$attempt = 1; \$attempt -le \$maxAttempts; \$attempt\+\+\)' -or
    $bridgeMatrixVerifier -notmatch 'Test-BridgeMatrixTransientHostPortFailure' -or
    $bridgeMatrixVerifier -notmatch 'retrying the unchanged test suite in a fresh container' -or
    $bridgeMatrixVerifier -notmatch 'Complete-BridgeMatrixAttempt') {
    throw 'Bridge PostgreSQL matrix retry can hide deterministic failures or is no longer tightly bounded'
}
$bridgeRetryPolicy = Join-Path $projectRoot 'agents\scripts\bridge-matrix-retry-policy.ps1'
. $bridgeRetryPolicy
$transientFixture = 'bridge_integration_test.go:233: failed to connect to user=postgres database=bridge_test: 127.0.0.1:54321: dial tcp 127.0.0.1:54321: connectex: connection refused'
$mixedDeterministicFixture = "assertion mismatch`nserver closed the connection unexpectedly"
$authenticationFixture = 'bridge_integration_test.go:233: failed to connect to user=postgres database=bridge_test: 127.0.0.1:54321: FATAL: password authentication failed'
$databaseFixture = 'bridge_integration_test.go:233: failed to connect to user=postgres database=missing: 127.0.0.1:54321: FATAL: database does not exist'
$mixedLoopbackFixture = "bridge_integration_test.go:120: assertion mismatch: got 1 want 0`nbridge_integration_test.go:233: failed to connect: dial tcp 127.0.0.1:54321: connectex: connection refused"
if (-not (Test-BridgeMatrixTransientHostPortFailure -Text $transientFixture) -or
    (Test-BridgeMatrixTransientHostPortFailure -Text $mixedDeterministicFixture) -or
    (Test-BridgeMatrixTransientHostPortFailure -Text $authenticationFixture) -or
    (Test-BridgeMatrixTransientHostPortFailure -Text $databaseFixture) -or
    (Test-BridgeMatrixTransientHostPortFailure -Text $mixedLoopbackFixture) -or
    (Test-BridgeMatrixTransientHostPortFailure -Text 'server closed the connection unexpectedly')) {
    throw 'Bridge PostgreSQL matrix transient classifier escaped the explicit loopback host-port scope'
}
$cleanupRejected = $false
try { Complete-BridgeMatrixAttempt -ContainerName fixture -CleanupExitCode 1 -AttemptError $null } catch { $cleanupRejected = $_.Exception.Message -match 'cleanup.*failed' }
if (-not $cleanupRejected) { throw 'Bridge PostgreSQL matrix cleanup failure was not fail-closed' }
$combinedRejected = $false
try { Complete-BridgeMatrixAttempt -ContainerName fixture -CleanupExitCode 1 -AttemptError ([Exception]::new('deterministic assertion')) } catch {
    $combinedRejected = $_.Exception.Message -match 'deterministic assertion' -and $_.Exception.Message -match 'cleanup.*failed'
}
if (-not $combinedRejected) { throw 'Bridge PostgreSQL matrix cleanup failure hid the original attempt error' }

$fixtureRoot = Join-Path $PSScriptRoot 'fixtures\release-image-gate'
$imageID = 'sha256:' + ('a' * 64)
$trivyTimestamp = ConvertFrom-TrivyDatabaseTimestamp -Timestamp '2026-08-20 19:46:52.822388238 +0000 UTC'
if ($trivyTimestamp.ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ss.fffffffZ') -cne '2026-08-20T19:46:52.8223882Z') {
    throw 'Trivy Go-style nanosecond database timestamp was not parsed deterministically'
}
$goodReport = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'good-vulnerability-report.json') | ConvertFrom-Json
$goodBom = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'good-sbom.json') | ConvertFrom-Json

$summary = Assert-TrivyReportBinding -Report $goodReport -ExpectedImageId $imageID
if ($summary.Total -ne 0 -or $summary.High -ne 0 -or $summary.Critical -ne 0) {
    throw 'zero-finding fixture was not interpreted as clean'
}
Assert-CycloneDxBinding -Bom $goodBom -ExpectedImageId $imageID | Out-Null

$badReport = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'good-vulnerability-report.json') | ConvertFrom-Json
$badReport.Metadata.ImageID = 'sha256:' + ('b' * 64)
$rejected = $false
try { Assert-TrivyReportBinding -Report $badReport -ExpectedImageId $imageID | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'stale Trivy ImageID fixture was accepted' }

$badBom = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'good-sbom.json') | ConvertFrom-Json
$badBom.metadata.component.properties[0].value = 'sha256:' + ('b' * 64)
$rejected = $false
try { Assert-CycloneDxBinding -Bom $badBom -ExpectedImageId $imageID | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'stale CycloneDX ImageID fixture was accepted' }

$postgresReference = 'postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
$postgresID = 'sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
$postgresReport = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'postgres-gosu-vulnerability-report.json') | ConvertFrom-Json
$postgresSummary = Assert-TrivyReportBinding -Report $postgresReport -ExpectedImageId $postgresID
$rejected = $false
try { Assert-PostgresGosuFindingScope -ImageReference $postgresReference -ImageId $postgresID -Summary $postgresSummary -ExpectedReference $postgresReference -ExpectedImageId $postgresID | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'PostgreSQL exception accepted the RC48 gosu finding with a fixed version' }

function Assert-PostgresFixtureRejected {
    param(
        [Parameter(Mandatory)][scriptblock]$Mutate,
        [Parameter(Mandatory)][string]$Message
    )

    $report = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'postgres-gosu-vulnerability-report.json') | ConvertFrom-Json
    & $Mutate $report
    $summary = Get-TrivyFindingSummary -Report $report
    $rejected = $false
    try { Assert-PostgresGosuFindingScope -ImageReference $postgresReference -ImageId $postgresID -Summary $summary -ExpectedReference $postgresReference -ExpectedImageId $postgresID | Out-Null } catch { $rejected = $true }
    if (-not $rejected) { throw $Message }
}

Assert-PostgresFixtureRejected -Mutate { param($report) $report.Results[0].Vulnerabilities[0].VulnerabilityID = 'CVE-DRIFT' } -Message 'PostgreSQL exception generalized beyond exact CVE'
Assert-PostgresFixtureRejected -Mutate { param($report) $report.Results[0].Class = 'os-pkgs' } -Message 'PostgreSQL exception generalized beyond exact class'
Assert-PostgresFixtureRejected -Mutate { param($report) $report.Results[0].Type = 'library' } -Message 'PostgreSQL exception generalized beyond exact type'
Assert-PostgresFixtureRejected -Mutate { param($report) $report.Results[0].Vulnerabilities[0].InstalledVersion = 'v1.24.5' } -Message 'PostgreSQL exception generalized beyond exact installed version'
Assert-PostgresFixtureRejected -Mutate { param($report) $report.Results[0].Vulnerabilities[0] | Add-Member -NotePropertyName Status -NotePropertyValue 'not_affected' } -Message 'PostgreSQL exception generalized beyond explicitly reviewed no-fix status'
Assert-PostgresFixtureRejected -Mutate { param($report) $report.Results[0].Vulnerabilities[0].FixedVersion = 'v1.24.8' } -Message 'PostgreSQL exception generalized beyond fixed-version drift'

$rejected = $false
try { Assert-PostgresGosuFindingScope -ImageReference 'postgres:latest' -ImageId $postgresID -Summary $postgresSummary -ExpectedReference $postgresReference -ExpectedImageId $postgresID | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'PostgreSQL exception generalized beyond exact digest' }

$keycloakBase = 'quay.io/keycloak/keycloak:26.7.2@sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067'
$keycloakID = 'sha256:' + ('c' * 64)
$keycloakReport = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'keycloak-vendor-rejected-cve-report.json') | ConvertFrom-Json
$keycloakSummary = Assert-TrivyReportBinding -Report $keycloakReport -ExpectedImageId $keycloakID
Assert-KeycloakVendorRejectedCveScope -ImageReference 'invoice-keycloak:fixture' -ImageId $keycloakID -BaseReference $keycloakBase -Summary $keycloakSummary -ExpectedBaseReference $keycloakBase | Out-Null

function Assert-KeycloakFixtureRejected {
    param(
        [Parameter(Mandatory)][scriptblock]$Mutate,
        [Parameter(Mandatory)][string]$Message
    )

    $report = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'keycloak-vendor-rejected-cve-report.json') | ConvertFrom-Json
    & $Mutate $report
    $summary = Get-TrivyFindingSummary -Report $report
    $rejected = $false
    try { Assert-KeycloakVendorRejectedCveScope -ImageReference 'invoice-keycloak:fixture' -ImageId $keycloakID -BaseReference $keycloakBase -Summary $summary -ExpectedBaseReference $keycloakBase | Out-Null } catch { $rejected = $true }
    if (-not $rejected) { throw $Message }
}

$rejected = $false
try { Assert-KeycloakVendorRejectedCveScope -ImageReference 'invoice-keycloak:fixture' -ImageId ('sha256:' + ('d' * 64)) -BaseReference $keycloakBase -Summary $keycloakSummary -ExpectedBaseReference $keycloakBase | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'Keycloak vendor-rejection exception generalized beyond the derived image ID' }
$rejected = $false
try { Assert-KeycloakVendorRejectedCveScope -ImageReference 'invoice-keycloak:fixture' -ImageId $keycloakID -BaseReference 'quay.io/keycloak/keycloak:latest' -Summary $keycloakSummary -ExpectedBaseReference $keycloakBase | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'Keycloak vendor-rejection exception generalized beyond exact base digest' }
Assert-KeycloakFixtureRejected -Mutate { param($report) $report.Results[0].Vulnerabilities[0].VulnerabilityID = 'CVE-DRIFT' } -Message 'Keycloak vendor-rejection exception generalized beyond exact CVE'
Assert-KeycloakFixtureRejected -Mutate { param($report) $report.Results[0].Vulnerabilities[0].PkgName = 'different-package' } -Message 'Keycloak vendor-rejection exception generalized beyond exact package'
Assert-KeycloakFixtureRejected -Mutate { param($report) $report.Results[0].Vulnerabilities[0].InstalledVersion = '1:21.0.12.0.8-1.2.el9' } -Message 'Keycloak vendor-rejection exception generalized beyond exact installed version'
Assert-KeycloakFixtureRejected -Mutate { param($report) $report.Results[0].Vulnerabilities[0].FixedVersion = '1:21.0.12.2.1-1.2.el9' } -Message 'Keycloak vendor-rejection exception generalized beyond fixed-version drift'
Assert-KeycloakFixtureRejected -Mutate { param($report) $report.Results[0].Vulnerabilities[0].Severity = 'CRITICAL' } -Message 'Keycloak vendor-rejection exception generalized beyond exact severity'
Assert-KeycloakFixtureRejected -Mutate { param($report) $report.Results[0].Vulnerabilities[0].Status = 'not_affected' } -Message 'Keycloak vendor-rejection exception generalized beyond exact status'
Assert-KeycloakFixtureRejected -Mutate { param($report) $report.Results[0].Class = 'lang-pkgs' } -Message 'Keycloak vendor-rejection exception generalized beyond exact class'
Assert-KeycloakFixtureRejected -Mutate { param($report) $report.Results[0].Type = 'library' } -Message 'Keycloak vendor-rejection exception generalized beyond exact type'

$reviewedAt = '2026-08-31T00:00:00Z'
$reviewDueAt = '2026-09-30T00:00:00Z'
$reviewNow = '2026-08-31T12:00:00Z'
$reviewRationale = 'Red Hat rejected the CVE for the retained OpenJDK system-libpng runtime.'
Assert-ExceptionReviewContract -Rationale $reviewRationale -ReviewedAt $reviewedAt -ReviewDueAt $reviewDueAt -CurrentTime $reviewNow | Out-Null

function Assert-ExceptionReviewRejected {
    param(
        [string]$Rationale = $reviewRationale,
        [string]$ReviewedAt = $reviewedAt,
        [string]$ReviewDueAt = $reviewDueAt,
        [string]$CurrentTime = $reviewNow,
        [Parameter(Mandatory)][string]$Message
    )

    $rejected = $false
    try { Assert-ExceptionReviewContract -Rationale $Rationale -ReviewedAt $ReviewedAt -ReviewDueAt $ReviewDueAt -CurrentTime $CurrentTime | Out-Null } catch { $rejected = $true }
    if (-not $rejected) { throw $Message }
}

Assert-ExceptionReviewRejected -Rationale '' -Message 'exception review accepted missing rationale'
Assert-ExceptionReviewRejected -ReviewedAt '' -Message 'exception review accepted missing reviewedAt'
Assert-ExceptionReviewRejected -ReviewDueAt '' -Message 'exception review accepted missing reviewDueAt'
Assert-ExceptionReviewRejected -ReviewedAt '2026/08/31 00:00:00' -Message 'exception review accepted invalid reviewedAt timestamp'
Assert-ExceptionReviewRejected -ReviewDueAt 'not-a-timestamp' -Message 'exception review accepted invalid reviewDueAt timestamp'
Assert-ExceptionReviewRejected -ReviewDueAt $reviewedAt -Message 'exception review accepted due-at equal to reviewed-at'
Assert-ExceptionReviewRejected -ReviewDueAt '2026-08-31T00:00:01Z' -CurrentTime $reviewNow -Message 'exception review accepted an expired due date'
Assert-ExceptionReviewRejected -CurrentTime '2026-08-30T23:59:59Z' -Message 'exception review accepted a current time before the review window'

$fixtureDBTime = [DateTimeOffset]::UtcNow.ToString('yyyy-MM-dd HH:mm:ss +0000') + ' UTC'
$versionProof = "Go: go1.25.7`nScanner: govulncheck@v1.7.0`nDB: https://vuln.go.dev`nDB updated: $fixtureDBTime`n"
$binaryProof = "=== Symbol Results ===`n`nNo vulnerabilities found.`n`nYour code is affected by 0 vulnerabilities.`n"
Assert-GovulncheckBinaryProof -ProofText $binaryProof -VersionText $versionProof | Out-Null
$weekendDBTime = [DateTimeOffset]::UtcNow.AddHours(-95).ToString('yyyy-MM-dd HH:mm:ss +0000') + ' UTC'
$weekendVersionProof = $versionProof -replace '(?m)^DB updated:.+$', "DB updated: $weekendDBTime"
Assert-GovulncheckBinaryProof -ProofText $binaryProof -VersionText $weekendVersionProof | Out-Null
$staleDBTime = [DateTimeOffset]::UtcNow.AddHours(-97).ToString('yyyy-MM-dd HH:mm:ss +0000') + ' UTC'
$staleVersionProof = $versionProof -replace '(?m)^DB updated:.+$', "DB updated: $staleDBTime"
$rejected = $false
try { Assert-GovulncheckBinaryProof -ProofText $binaryProof -VersionText $staleVersionProof | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'stale govulncheck database fixture exceeded the 96-hour release window' }
$rejected = $false
try { Assert-GovulncheckBinaryProof -ProofText ($binaryProof -replace '0 vulnerabilities', '1 vulnerability') -VersionText $versionProof | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'called-vulnerability govulncheck fixture was accepted' }

$rejected = $false
try { Resolve-ReleaseArtifactPath -ReleaseDirectory $projectRoot -RelativePath '../outside' | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'release artifact traversal fixture was accepted' }
$rejected = $false
try { Resolve-ReleaseArtifactPath -ReleaseDirectory $projectRoot -RelativePath 'C:/outside' | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'absolute release artifact fixture was accepted' }

$keycloakRejectedDecision = Get-IdpGateDecision -Mode keycloak -KeycloakImageApproved $false
$keycloakApprovedDecision = Get-IdpGateDecision -Mode keycloak -KeycloakImageApproved $true
$externalDecision = Get-IdpGateDecision -Mode external-managed
$noneDecision = Get-IdpGateDecision -Mode none
if ($keycloakRejectedDecision.Status -cne 'image_rejected' -or
    $keycloakRejectedDecision.BlockReason -cne 'self_hosted_keycloak_image_failed' -or
    $keycloakApprovedDecision.Status -cne 'image_approved_pending_canary' -or
    $keycloakApprovedDecision.BlockReason -cne 'idp_self_hosted_pending_canary' -or
    $externalDecision.Status -cne 'external_pending_canary' -or
    $externalDecision.ProductionCanary -cne 'pending' -or
    $noneDecision.Status -cne 'not_included') {
    throw 'IdP fail-closed decision fixture drifted'
}
$gateSource = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'release-image-gate.ps1')
$artifactVerifierSource = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify-release-image-artifacts.ps1')
if ($gateSource -notmatch '(?s)\[string\]\s*\$IdPMode\s*=\s*''keycloak''' -or
    $gateSource -notmatch "Policy\s+'keycloak-26\.7\.2-exact-vendor-rejection'" -or
    $gateSource -notmatch [regex]::Escape('quay.io/keycloak/keycloak:26.7.2@sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067')) {
    throw 'release image gate no longer defaults to exact Keycloak 26.7.2 scan mode'
}
if ($gateSource -match '(?m)verify\.ps1.*-SkipPostgres') {
    throw 'release image gate bypasses the full PostgreSQL verification gate'
}
if ($gateSource -notmatch "@\('build', '--pull', '--provenance=false'") {
    throw 'release image builds no longer disable nondeterministic BuildKit provenance wrappers'
}
if ($artifactVerifierSource -match 'approved-by-exact-binary-exception' -or
    $artifactVerifierSource -notmatch '\[int\]\$postgres\[0\]\.vulnerabilities\.total\s*-ne\s*0' -or
    $artifactVerifierSource -notmatch '\[string\]\$postgres\[0\]\.policyStatus\s*-cne\s*''approved''' -or
    $artifactVerifierSource -notmatch '\$null\s*-ne\s*\$postgres\[0\]\.exception') {
    throw 'RC49 artifact verification still permits a PostgreSQL vulnerability exception'
}

Write-Host 'Release image gate offline/static fixtures passed.'
$global:LASTEXITCODE = 0
