[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$ReleaseDirectory
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$projectRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'release-image-gate-lib.ps1')

if (-not [IO.Path]::IsPathFullyQualified($ReleaseDirectory)) {
    $ReleaseDirectory = Join-Path $projectRoot $ReleaseDirectory
}
$releaseRoot = [IO.Path]::GetFullPath($ReleaseDirectory)
$allowedRoot = [IO.Path]::GetFullPath((Join-Path $projectRoot 'release')).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
if ($releaseRoot.TrimEnd([IO.Path]::DirectorySeparatorChar).Equals($allowedRoot.TrimEnd([IO.Path]::DirectorySeparatorChar), [StringComparison]::OrdinalIgnoreCase) -or
    -not ($releaseRoot + [IO.Path]::DirectorySeparatorChar).StartsWith($allowedRoot, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'release artifact directory must remain below the project release directory'
}
if (((Get-Item -LiteralPath $releaseRoot -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
    throw 'release artifact directory cannot be a symlink/reparse point'
}

Assert-Sha256Sums -ReleaseDirectory $releaseRoot | Out-Null
$manifestPath = Join-Path $releaseRoot 'release-manifest.json'
if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) { throw 'release-manifest.json is missing' }
$manifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
if ([string]$manifest.schemaVersion -cne 'solov.invoice.release-image-gate/v1') {
    throw 'unsupported release image gate manifest schema'
}
if ([string]$manifest.tools.trivyReference -cne 'ghcr.io/aquasecurity/trivy:0.74.0@sha256:62b1e65e8869bc4b4c6aa4fa2b21595256c7c2f6018a9d9ad61caf87187c1969') {
    throw 'release manifest was not generated with the reviewed Trivy tool digest'
}
if ([string]$manifest.tools.trivyAcquisition -notin @('registry-refreshed', 'registry-refresh-failed-exact-local-digest-verified')) {
    throw 'Trivy tool acquisition was neither registry-refreshed nor exact-local-digest verified'
}
if ([string]$manifest.tools.scriptSha256 -cne (Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'release-image-gate.ps1')) -or
    [string]$manifest.tools.librarySha256 -cne (Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'release-image-gate-lib.ps1')) -or
    [string]$manifest.tools.verifierSha256 -cne (Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-release-image-artifacts.ps1')) -or
    [string]$manifest.tools.sourceVerifierSha256 -cne (Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify.ps1')) -or
    [string]$manifest.tools.keycloakRuntimeVerifierSha256 -cne (Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-keycloak-runtime.ps1')) -or
    [string]$manifest.tools.keycloakProvisioningVerifierSha256 -cne (Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-keycloak-provisioning.ps1')) -or
    [string]$manifest.tools.keycloakProvisionerSha256 -cne (Get-FileSha256Lower -Path (Join-Path $projectRoot 'deploy\keycloak\provision-solov-realm.sh'))) {
    throw 'release gate/verifier source changed after artifact generation'
}
if ((@($manifest.policy.trivySeverities) -join ',') -cne 'HIGH,CRITICAL' -or
    $manifest.policy.ignoreUnfixed -ne $false -or
    @($manifest.policy.ignoredVulnerabilities).Count -ne 0 -or
    [string]$manifest.policy.trivyExecution -cne 'serial' -or
    [string]$manifest.policy.staleImageArtifacts -cne 'fail') {
    throw 'release manifest weakens the reviewed vulnerability/staleness policy'
}

$currentFingerprints = [ordered]@{
    backend = Get-ContextFingerprint -Root (Join-Path $projectRoot 'backend') -ExcludedDirectoryNames @('bin', 'coverage')
    web = Get-ContextFingerprint -Root (Join-Path $projectRoot 'web') -ExcludedDirectoryNames @('node_modules', 'dist', 'coverage', '.playwright-cli')
    agents = Get-ContextFingerprint -Root (Join-Path $projectRoot 'agents') -ExcludedDirectoryNames @('bin', 'coverage')
    keycloak = Get-ContextFingerprint -Root (Join-Path $projectRoot 'deploy\keycloak')
    deployment = Get-ContextFingerprint -Root (Join-Path $projectRoot 'deploy')
}
foreach ($name in $currentFingerprints.Keys) {
    $recordedProperty = $manifest.source.contextFingerprints.PSObject.Properties[$name]
    if ($null -eq $recordedProperty -or [string]$recordedProperty.Value -cne $currentFingerprints[$name]) {
        throw "release image/deployment source fingerprint is stale: $name"
    }
}

$idpMode = [string]$manifest.idp.mode
$requiredNames = @('api', 'pdf-scanner', 'tools', 'web', 'source-agent', 'postgres-runtime', 'clamav-runtime', 'ingest-proxy')
if ($idpMode -ceq 'keycloak') { $requiredNames += 'keycloak' }
$records = @($manifest.images)
$names = @($records | ForEach-Object { [string]$_.name })
if (@($names | Sort-Object -Unique).Count -ne $names.Count) { throw 'release manifest has duplicate image names' }
foreach ($name in $requiredNames) {
    if ($name -notin $names) { throw "release manifest is missing required image $name" }
}

$releaseImageTag = Get-CommonReleaseImageTag -ImageRecords $records -IdPMode $idpMode
$idpCompose = Get-Content -Raw -LiteralPath (Join-Path $projectRoot 'deploy\docker-compose.idp.yml')
if ($idpMode -ceq 'keycloak' -and
    -not $idpCompose.Contains('    image: invoice-keycloak:${INVOICE_IMAGE_TAG:?set the exact reviewed invoice release tag}')) {
    throw 'production Keycloak Compose does not use the manifest-bound release image tag'
}
if ($idpMode -ceq 'keycloak') {
    $previousImageTag = $env:INVOICE_IMAGE_TAG
    try {
        $env:INVOICE_IMAGE_TAG = $releaseImageTag
        $renderedIDPText = docker compose `
            --env-file (Join-Path $projectRoot 'deploy\.env.production.example') `
            -f (Join-Path $projectRoot 'deploy\docker-compose.idp.yml') config --format json | Out-String
        if ($LASTEXITCODE -ne 0) { throw 'cannot render production Keycloak Compose for release binding verification' }
        $renderedIDP = $renderedIDPText | ConvertFrom-Json
    } finally {
        if ($null -eq $previousImageTag) {
            Remove-Item Env:INVOICE_IMAGE_TAG -ErrorAction SilentlyContinue
        } else {
            $env:INVOICE_IMAGE_TAG = $previousImageTag
        }
    }
    if ([string]$renderedIDP.services.keycloak.image -cne "invoice-keycloak:$releaseImageTag") {
        throw 'rendered production Keycloak image does not match the manifest-bound release tag'
    }
}

foreach ($record in $records) {
    Assert-GeneratedArtifactBinding -ReleaseDirectory $releaseRoot -ImageRecord $record | Out-Null
    $currentID = Get-RequiredImageId -Reference ([string]$record.reference)
    if ($currentID -cne [string]$record.imageId) {
        throw "stale release artifacts: current $($record.reference) resolves to $currentID instead of $($record.imageId)"
    }
    if ([string]$record.name -notin @('postgres-runtime', 'keycloak')) {
        if ([int]$record.vulnerabilities.total -ne 0 -or [string]$record.policyStatus -cne 'approved') {
            throw "required zero-finding image is not approved: $($record.name)"
        }
    }
    if ([string]$record.kind -ceq 'pinned-external' -and
        [string]$record.acquisition -notin @('registry-refreshed', 'registry-refresh-failed-exact-local-digest-verified')) {
        throw "pinned external image acquisition is not exact-digest verified: $($record.name)"
    }
}

$postgres = @($records | Where-Object name -eq 'postgres-runtime')
if ($postgres.Count -ne 1) { throw 'release manifest must have exactly one PostgreSQL record' }
$expectedPostgresReference = 'postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
$expectedPostgresID = 'sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
if ([int]$postgres[0].vulnerabilities.total -gt 0) {
    if ([string]$postgres[0].policyStatus -cne 'approved-by-exact-binary-exception' -or
        [string]$postgres[0].reference -cne $expectedPostgresReference -or
        [string]$postgres[0].imageId -cne $expectedPostgresID -or
        [string]$postgres[0].exception.imageReference -cne $expectedPostgresReference -or
        [string]$postgres[0].exception.imageId -cne $expectedPostgresID -or
        [string]$postgres[0].exception.binarySha256 -cne '52c8749d0142edd234e9d6bd5237dff2d81e71f43537e2f4f66f75dd4b243dd0' -or
        [string]$postgres[0].exception.govulncheckModuleZipSha256 -cne 'a14bf913551ac09f00ae0e903c1b358713f71af911d7ddacc3fab8ce5c149a26') {
        throw 'PostgreSQL exception escaped its exact digest/binary scope'
    }
    $binaryPath = Join-Path $releaseRoot 'proof\postgres-gosu-linux-amd64'
    if ((Get-FileSha256Lower -Path $binaryPath) -cne [string]$postgres[0].exception.binarySha256) {
        throw 'retained gosu binary does not match the exact exception proof'
    }
    Assert-GovulncheckBinaryProof `
        -ProofText (Get-Content -Raw -LiteralPath (Join-Path $releaseRoot 'proof\postgres-gosu.govulncheck.txt')) `
        -VersionText (Get-Content -Raw -LiteralPath (Join-Path $releaseRoot 'proof\postgres-gosu.govulncheck-version.txt')) | Out-Null
    $moduleEvidencePath = Resolve-ReleaseArtifactPath -ReleaseDirectory $releaseRoot -RelativePath ([string]$postgres[0].exception.govulncheckModuleEvidence)
    $moduleEvidence = Get-Content -Raw -LiteralPath $moduleEvidencePath
    if ($moduleEvidence -notmatch '(?m)^a14bf913551ac09f00ae0e903c1b358713f71af911d7ddacc3fab8ce5c149a26  v1\.7\.0\.zip$' -or
        $moduleEvidence -notmatch '(?m)^h1:4MQBuhmXbz2uepNJrf3v\+aaZLGDqw1JluwYboegA1qg=  v1\.7\.0\.ziphash$') {
        throw 'govulncheck module-cache supply-chain evidence is missing or stale'
    }
} elseif ([string]$postgres[0].policyStatus -cne 'approved') {
    throw 'zero-finding PostgreSQL image was not approved normally'
}

$keycloak = @($records | Where-Object name -eq 'keycloak')
switch ($idpMode) {
    'keycloak' {
        $expectedKeycloakBase = 'quay.io/keycloak/keycloak:26.7.2@sha256:6efbadc00f0ed0237610becf11f4101b9c3ad8edf08a5b70c97aa4154ed436ec'
        if ($keycloak.Count -ne 1 -or [string]$keycloak[0].policy -cne 'keycloak-26.7.2-exact-vendor-rejection' -or
            [string]$keycloak[0].baseReference -cne $expectedKeycloakBase -or
            [string]$manifest.decisions.productionLaunch -cne 'blocked') {
            throw 'Keycloak mode was silently skipped, drifted from the exact 26.7.2 base, or bypassed the production canary'
        }
        $pruningPath = Resolve-ReleaseArtifactPath -ReleaseDirectory $releaseRoot -RelativePath ([string]$keycloak[0].runtimeProof.path)
        if ([string]$keycloak[0].runtimeProof.type -cne 'keycloak-runtime-pruning' -or
            [string]$keycloak[0].runtimeProof.imageId -cne [string]$keycloak[0].imageId -or
            (Get-FileSha256Lower -Path $pruningPath) -cne [string]$keycloak[0].runtimeProof.sha256 -or
            ((Get-Content -Raw -LiteralPath $pruningPath).Replace("`r`n", "`n").Trim()) -cne "bin/client=absent`nmssql-driver=absent") {
            throw 'Keycloak runtime pruning proof is missing, stale or malformed'
        }
        $runtimeSmokePassed = $null -ne $keycloak[0].runtimeSmoke -and [string]$keycloak[0].runtimeSmoke.status -ceq 'passed'
        if ($runtimeSmokePassed) {
            $smokePath = Resolve-ReleaseArtifactPath -ReleaseDirectory $releaseRoot -RelativePath ([string]$keycloak[0].runtimeSmoke.path)
            if ([string]$keycloak[0].runtimeSmoke.imageId -cne [string]$keycloak[0].imageId -or
                (Get-FileSha256Lower -Path $smokePath) -cne [string]$keycloak[0].runtimeSmoke.sha256 -or
                [string]$keycloak[0].runtimeSmoke.scriptSha256 -cne (Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-keycloak-runtime.ps1')) -or
                (Get-Content -Raw -LiteralPath $smokePath) -notmatch '(?m)^Hardened Keycloak runtime, PostgreSQL schema, health and OIDC discovery smoke passed\.\s*$') {
                throw 'Keycloak runtime OIDC smoke proof is missing or stale'
            }
        }
        $realmProvisioningPassed = $null -ne $keycloak[0].realmProvisioning -and [string]$keycloak[0].realmProvisioning.status -ceq 'passed'
        if ($realmProvisioningPassed) {
            $provisioningPath = Resolve-ReleaseArtifactPath -ReleaseDirectory $releaseRoot -RelativePath ([string]$keycloak[0].realmProvisioning.path)
            if ([string]$keycloak[0].realmProvisioning.imageId -cne [string]$keycloak[0].imageId -or
                (Get-FileSha256Lower -Path $provisioningPath) -cne [string]$keycloak[0].realmProvisioning.sha256 -or
                [string]$keycloak[0].realmProvisioning.scriptSha256 -cne (Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-keycloak-provisioning.ps1')) -or
                [string]$keycloak[0].realmProvisioning.provisionerSha256 -cne (Get-FileSha256Lower -Path (Join-Path $projectRoot 'deploy\keycloak\provision-solov-realm.sh')) -or
                (Get-Content -Raw -LiteralPath $provisioningPath) -notmatch '(?m)^Disposable Keycloak 26\.7\.2 realm provisioning, output allowlist and one-secret publication passed\.\s*$') {
                throw 'Keycloak realm provisioning proof is missing or stale'
            }
        }
        if ([int]$keycloak[0].vulnerabilities.total -eq 0) {
            if ([string]$keycloak[0].policyStatus -ceq 'failed') {
                if ([string]$manifest.idp.status -cne 'image_rejected') { throw 'failed Keycloak runtime smoke was not fail-closed' }
            } elseif ([string]$keycloak[0].policyStatus -cne 'approved' -or -not $runtimeSmokePassed -or -not $realmProvisioningPassed -or
                [string]$manifest.idp.status -cne 'image_approved_pending_canary' -or
                [string]$manifest.idp.productionCanary -cne 'pending') {
                throw 'zero-finding Keycloak image did not remain pending the production canary'
            }
        } elseif ([int]$keycloak[0].vulnerabilities.total -eq 1) {
            $keycloakReportPath = Resolve-ReleaseArtifactPath -ReleaseDirectory $releaseRoot -RelativePath ([string]$keycloak[0].vulnerabilityReport.path)
            $keycloakReport = Get-Content -Raw -LiteralPath $keycloakReportPath | ConvertFrom-Json
            $keycloakSummary = Assert-TrivyReportBinding -Report $keycloakReport -ExpectedImageId ([string]$keycloak[0].imageId)
            Assert-KeycloakVendorRejectedCveScope -ImageReference ([string]$keycloak[0].reference) -ImageId ([string]$keycloak[0].imageId) -BaseReference ([string]$keycloak[0].baseReference) -Summary $keycloakSummary -ExpectedBaseReference $expectedKeycloakBase | Out-Null
            if ([string]$keycloak[0].policyStatus -ceq 'failed') {
                if ([string]$manifest.idp.status -cne 'image_rejected') { throw 'failed Keycloak runtime smoke was not fail-closed' }
            } elseif ([string]$keycloak[0].policyStatus -cne 'approved-by-exact-vendor-rejection' -or -not $runtimeSmokePassed -or -not $realmProvisioningPassed -or
                [string]$keycloak[0].exception.derivedImageId -cne [string]$keycloak[0].imageId -or
                [string]$keycloak[0].exception.vulnerabilityId -cne 'CVE-2026-22020' -or
                [string]$keycloak[0].exception.package -cne 'java-21-openjdk-headless' -or
                [string]$keycloak[0].exception.installedVersion -cne '1:21.0.12.0.8-1.2.el9' -or
                [string]$keycloak[0].exception.redHatEvidence -cne 'https://bugzilla.redhat.com/show_bug.cgi?id=2460045#c11' -or
                [string]$keycloak[0].exception.awsEvidence -cne 'https://explore.alas.aws.amazon.com/CVE-2026-22020.html' -or
                [string]$manifest.idp.status -cne 'image_approved_pending_canary' -or
                [string]$manifest.idp.productionCanary -cne 'pending') {
                throw 'Keycloak vendor-rejection exception or pending canary binding is stale'
            }
        } elseif ([string]$keycloak[0].policyStatus -cne 'failed' -or [string]$manifest.idp.status -cne 'image_rejected') {
            throw 'Keycloak findings outside the single exact vendor-rejection scope were incorrectly approved'
        }
    }
    'external-managed' {
        if ($keycloak.Count -ne 0 -or [string]$manifest.idp.status -cne 'external_pending_canary' -or
            [string]$manifest.idp.productionCanary -cne 'pending' -or [string]$manifest.decisions.productionLaunch -cne 'blocked') {
            throw 'external managed IdP mode does not explicitly preserve the pending production canary'
        }
    }
    'none' {
        if ($keycloak.Count -ne 0 -or [string]$manifest.idp.status -cne 'not_included' -or
            [string]$manifest.decisions.productionLaunch -cne 'blocked') {
            throw 'IdP none mode was silently treated as production-ready'
        }
    }
    default { throw "unsupported IdP mode in release manifest: $idpMode" }
}

Write-Host 'Release artifacts, immutable image bindings and IdP decision are internally consistent.'
$global:LASTEXITCODE = 0
