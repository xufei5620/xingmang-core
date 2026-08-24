$ErrorActionPreference = 'Stop'

$gateSource = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'release-image-gate.ps1')
if ([regex]::Matches($gateSource, "'--timeout', '15m'").Count -lt 4) {
    throw 'release image gate does not apply the reviewed 15-minute Trivy timeout to database updates, scans and SBOM generation'
}
Set-StrictMode -Version Latest

$projectRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'release-image-gate-lib.ps1')

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
