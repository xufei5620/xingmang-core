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
$keycloakProvisioningVerifier = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'verify-keycloak-provisioning.ps1')
if (-not $idpCompose.Contains('    image: invoice-keycloak:${INVOICE_IMAGE_TAG:?set the exact reviewed invoice release tag}') -or
    $idpCompose.Contains('    image: invoice-keycloak:26.7.2') -or
    -not $artifactVerifier.Contains('Get-CommonReleaseImageTag -ImageRecords $records -IdPMode $idpMode') -or
    -not $artifactVerifier.Contains('production Keycloak Compose does not use the manifest-bound release image tag') -or
    -not $sourceVerifier.Contains("Test-OrdinalStringEqual -Actual `$idpBaseObject.services.keycloak.image -Expected 'invoice-keycloak:verification-build'") -or
    -not $sourceVerifier.Contains("Test-OrdinalStringEqual -Actual `$idpBaseObject.services.keycloak.pull_policy -Expected 'never'")) {
    throw 'Keycloak production Compose can escape the common manifest-bound release image tag'
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

$bridgeMatrixVerifier = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'agents\scripts\verify-bridge-postgres-matrix.ps1')
if ($bridgeMatrixVerifier -notmatch 'for \(\$attempt = 1; \$attempt -le 3; \$attempt\+\+\)' -or
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
Assert-PostgresGosuFindingScope -ImageReference $postgresReference -ImageId $postgresID -Summary $postgresSummary -ExpectedReference $postgresReference -ExpectedImageId $postgresID | Out-Null

$badPostgresReport = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'postgres-gosu-vulnerability-report.json') | ConvertFrom-Json
$badPostgresReport.Results[0].Target = 'usr/local/bin/other'
$badPostgresSummary = Get-TrivyFindingSummary -Report $badPostgresReport
$rejected = $false
try { Assert-PostgresGosuFindingScope -ImageReference $postgresReference -ImageId $postgresID -Summary $badPostgresSummary -ExpectedReference $postgresReference -ExpectedImageId $postgresID | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'PostgreSQL exception generalized beyond exact gosu target'
}

$rejected = $false
try { Assert-PostgresGosuFindingScope -ImageReference 'postgres:latest' -ImageId $postgresID -Summary $postgresSummary -ExpectedReference $postgresReference -ExpectedImageId $postgresID | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'PostgreSQL exception generalized beyond exact digest' }

$keycloakBase = 'quay.io/keycloak/keycloak:26.7.2@sha256:6efbadc00f0ed0237610becf11f4101b9c3ad8edf08a5b70c97aa4154ed436ec'
$keycloakID = 'sha256:' + ('c' * 64)
$keycloakReport = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'keycloak-vendor-rejected-cve-report.json') | ConvertFrom-Json
$keycloakSummary = Assert-TrivyReportBinding -Report $keycloakReport -ExpectedImageId $keycloakID
Assert-KeycloakVendorRejectedCveScope -ImageReference 'invoice-keycloak:fixture' -ImageId $keycloakID -BaseReference $keycloakBase -Summary $keycloakSummary -ExpectedBaseReference $keycloakBase | Out-Null
$badKeycloakReport = Get-Content -Raw -LiteralPath (Join-Path $fixtureRoot 'keycloak-vendor-rejected-cve-report.json') | ConvertFrom-Json
$badKeycloakReport.Results[0].Vulnerabilities[0].PkgName = 'different-package'
$badKeycloakSummary = Get-TrivyFindingSummary -Report $badKeycloakReport
$rejected = $false
try { Assert-KeycloakVendorRejectedCveScope -ImageReference 'invoice-keycloak:fixture' -ImageId $keycloakID -BaseReference $keycloakBase -Summary $badKeycloakSummary -ExpectedBaseReference $keycloakBase | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'Keycloak vendor-rejection exception generalized beyond exact package' }
$rejected = $false
try { Assert-KeycloakVendorRejectedCveScope -ImageReference 'invoice-keycloak:fixture' -ImageId $keycloakID -BaseReference 'quay.io/keycloak/keycloak:latest' -Summary $keycloakSummary -ExpectedBaseReference $keycloakBase | Out-Null } catch { $rejected = $true }
if (-not $rejected) { throw 'Keycloak vendor-rejection exception generalized beyond exact base digest' }

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
if ($gateSource -notmatch '(?s)\[string\]\s*\$IdPMode\s*=\s*''keycloak''' -or
    $gateSource -notmatch "Policy\s+'keycloak-26\.7\.2-exact-vendor-rejection'" -or
    $gateSource -notmatch [regex]::Escape('quay.io/keycloak/keycloak:26.7.2@sha256:6efbadc00f0ed0237610becf11f4101b9c3ad8edf08a5b70c97aa4154ed436ec')) {
    throw 'release image gate no longer defaults to exact Keycloak 26.7.2 scan mode'
}
if ($gateSource -match '(?m)verify\.ps1.*-SkipPostgres') {
    throw 'release image gate bypasses the full PostgreSQL verification gate'
}
if ($gateSource -notmatch "@\('build', '--pull', '--provenance=false'") {
    throw 'release image builds no longer disable nondeterministic BuildKit provenance wrappers'
}

Write-Host 'Release image gate offline/static fixtures passed.'
$global:LASTEXITCODE = 0
