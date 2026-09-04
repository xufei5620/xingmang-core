[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$ReleaseDirectory,

    [switch]$RequireTransferReady,

    [string]$SignedReleaseTag = ''
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$projectRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'release-image-gate-lib.ps1')
Assert-ReleasePowerShellRuntime | Out-Null

if (-not $RequireTransferReady -and -not [string]::IsNullOrWhiteSpace($SignedReleaseTag)) {
    throw 'SignedReleaseTag is valid only with RequireTransferReady'
}
$signedTagCommit = ''
if ($RequireTransferReady) {
    $signedTagRef = Get-StrictSignedReleaseTagRef -SignedReleaseTag $SignedReleaseTag
    $peeledSignedTagRef = "$signedTagRef^{}"
    $tagObjectType = (& git -C $projectRoot cat-file -t $signedTagRef 2>$null | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $tagObjectType -cne 'tag') {
        throw 'strict transfer requires the supplied RC92 name to resolve to an annotated tag object'
    }
    & git -C $projectRoot verify-tag $signedTagRef *> $null
    if ($LASTEXITCODE -ne 0) {
        throw 'strict transfer requires a valid signature on the supplied RC92 tag'
    }
    $signedTagCommit = (& git -C $projectRoot rev-parse --verify $peeledSignedTagRef 2>$null | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $signedTagCommit -notmatch '^[0-9a-f]{40}$') {
        throw 'strict transfer could not peel the supplied signed RC92 tag to a commit'
    }
    $peeledObjectType = (& git -C $projectRoot cat-file -t $peeledSignedTagRef 2>$null | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $peeledObjectType -cne 'commit') {
        throw 'strict transfer signed RC92 tag did not peel to a commit object'
    }
}

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
if ($RequireTransferReady) {
    Assert-StrictReleaseDirectoryName -ReleaseDirectory $releaseRoot -ExpectedReleaseName '0.1.0-rc92' | Out-Null
}

Assert-Sha256Sums -ReleaseDirectory $releaseRoot | Out-Null
$manifestPath = Join-Path $releaseRoot 'release-manifest.json'
if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) { throw 'release-manifest.json is missing' }
$manifestJson = Get-Content -Raw -LiteralPath $manifestPath
$manifest = ConvertFrom-ReleaseJson -JsonText $manifestJson
if ([string]$manifest.schemaVersion -cne 'solov.invoice.release-image-gate/v1') {
    throw 'unsupported release image gate manifest schema'
}
if ($RequireTransferReady) {
    Assert-TransferReadyManifest -Manifest $manifest -ExpectedGitHead $signedTagCommit | Out-Null
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
    [string]$manifest.tools.readinessIndexOperatorGateSha256 -cne (Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-source-readiness-index-operator.ps1')) -or
    [string]$manifest.tools.readinessIndexOperatorSha256 -cne (Get-FileSha256Lower -Path (Join-Path $projectRoot 'deploy\postgres\apply-source-readiness-index-concurrently.sh')) -or
    [string]$manifest.tools.readinessIndexVerifierSha256 -cne (Get-FileSha256Lower -Path (Join-Path $projectRoot 'deploy\postgres\verify-source-readiness-index.sh')) -or
    [string]$manifest.tools.keycloakRuntimeVerifierSha256 -cne (Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-keycloak-runtime.ps1')) -or
    [string]$manifest.tools.keycloakProvisioningVerifierSha256 -cne (Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-keycloak-provisioning.ps1')) -or
    [string]$manifest.tools.keycloakProvisionerSha256 -cne (Get-FileSha256Lower -Path (Join-Path $projectRoot 'deploy\keycloak\provision-solov-realm.sh')) -or
    [string]$manifest.tools.nginxVerifierSha256 -cne (Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-nginx-configs.ps1')) -or
    [string]$manifest.tools.webSecurityHeadersVerifierSha256 -cne (Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-web-security-headers.ps1'))) {
    throw 'release gate/verifier source changed after artifact generation'
}
Assert-ExactReleaseManifestPolicy -Manifest $manifest | Out-Null

$currentFingerprints = [ordered]@{
    backend = Get-ContextFingerprint -Root (Join-Path $projectRoot 'backend') -ExcludedDirectoryNames @('bin', 'coverage')
    web = Get-ContextFingerprint -Root (Join-Path $projectRoot 'web') -ExcludedDirectoryNames @('node_modules', 'dist', 'coverage', '.playwright-cli')
    agents = Get-ContextFingerprint -Root (Join-Path $projectRoot 'agents') -ExcludedDirectoryNames @('bin', 'coverage')
    keycloak = Get-ContextFingerprint -Root (Join-Path $projectRoot 'deploy\keycloak')
    postgres = Get-ContextFingerprint -Root (Join-Path $projectRoot 'deploy\postgres')
    clamav = Get-ContextFingerprint -Root (Join-Path $projectRoot 'deploy\clamav')
    ingestProxy = Get-ContextFingerprint -Root (Join-Path $projectRoot 'deploy\ingest-proxy')
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
if ($names.Count -ne $requiredNames.Count) { throw 'release manifest does not contain the exact RC92 image inventory' }
foreach ($name in $requiredNames) {
    if ($name -notin $names) { throw "release manifest is missing required image $name" }
}

$releaseImageTag = Get-CommonReleaseImageTag -ImageRecords $records -IdPMode $idpMode
$expectedRepositories = [ordered]@{
    api = 'invoice-system-api'
    'pdf-scanner' = 'invoice-system-pdf-scanner'
    tools = 'invoice-system-tools'
    web = 'invoice-system-web'
    'source-agent' = 'invoice-source-agent'
    'postgres-runtime' = 'invoice-postgres'
    'clamav-runtime' = 'invoice-clamav'
    'ingest-proxy' = 'invoice-ingest-proxy'
}
if ($idpMode -ceq 'keycloak') { $expectedRepositories.keycloak = 'invoice-keycloak' }
$expectedDerivedBases = [ordered]@{
    'postgres-runtime' = 'postgres:18.6-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
    'clamav-runtime' = 'clamav/clamav:1.4.5@sha256:4de20bd9ab45a4b763c5412b769217ef5082572ebc8a63aff1a77943419e5dd8'
    'ingest-proxy' = 'nginx:1.30-alpine@sha256:97d490c12ba55b4946b01546d1c3ed324e8d41ab1c9fcb2a616aa470620e5b46'
}
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
        $renderedIDP = ConvertFrom-ReleaseJson -JsonText $renderedIDPText
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
    Assert-LinuxAmd64Platform -Platform ([string]$record.platform) | Out-Null
    $currentPlatform = (& docker image inspect ([string]$record.reference) --format '{{.Os}}/{{.Architecture}}' 2>$null | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $currentPlatform -cne [string]$record.platform) {
        throw 'current image platform does not match the manifest-bound linux/amd64 platform'
    }
    Assert-LinuxAmd64Platform -Platform $currentPlatform | Out-Null
    $expectedRepository = [string]$expectedRepositories[[string]$record.name]
    if ([string]::IsNullOrWhiteSpace($expectedRepository) -or
        [string]$record.reference -cne "${expectedRepository}:$releaseImageTag" -or
        [string]$record.kind -cne 'built' -or
        [string]$record.acquisition -cne 'built-from-source') {
        throw "RC92 image is not an exact locally built common-tag record: $($record.name)"
    }
    if ($expectedDerivedBases.Contains([string]$record.name) -and
        [string]$record.baseReference -cne [string]$expectedDerivedBases[[string]$record.name]) {
        throw "RC92 derived image base reference drifted: $($record.name)"
    }
    if ([string]$record.name -notin @('postgres-runtime', 'keycloak')) {
        if ($record.vulnerabilities.total -ne 0 -or [string]$record.policyStatus -cne 'approved') {
            throw "required zero-finding image is not approved: $($record.name)"
        }
    }
}

$postgres = @($records | Where-Object name -eq 'postgres-runtime')
if ($postgres.Count -ne 1) { throw 'release manifest must have exactly one PostgreSQL record' }
if ($postgres[0].vulnerabilities.total -ne 0 -or
    [string]$postgres[0].policyStatus -cne 'approved' -or
    $null -ne $postgres[0].exception) {
    throw 'RC92 PostgreSQL image must have zero HIGH/CRITICAL findings and no exception'
}

$ingest = @($records | Where-Object name -eq 'ingest-proxy')
$web = @($records | Where-Object name -eq 'web')
if ($ingest.Count -ne 1 -or $web.Count -ne 1) { throw 'release manifest must have exact ingest and web records' }
$nginxConfigurationPath = Resolve-ReleaseArtifactPath -ReleaseDirectory $releaseRoot -RelativePath ([string]$ingest[0].nginxConfiguration.path)
if ([string]$ingest[0].nginxConfiguration.status -cne 'passed' -or
    [string]$ingest[0].nginxConfiguration.reference -cne [string]$ingest[0].reference -or
    [string]$ingest[0].nginxConfiguration.imageId -cne [string]$ingest[0].imageId -or
    [string]$ingest[0].nginxConfiguration.scriptSha256 -cne (Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-nginx-configs.ps1')) -or
    (Get-FileSha256Lower -Path $nginxConfigurationPath) -cne [string]$ingest[0].nginxConfiguration.sha256 -or
    (Get-Content -Raw -LiteralPath $nginxConfigurationPath) -notmatch '(?m)^Public, split-admin OIDC and mTLS Nginx configurations passed syntax and security gates\.\s*$') {
    throw 'derived ingest Nginx configuration proof is missing or stale'
}
$webHeadersPath = Resolve-ReleaseArtifactPath -ReleaseDirectory $releaseRoot -RelativePath ([string]$web[0].webSecurityHeaders.path)
if ([string]$web[0].webSecurityHeaders.status -cne 'passed' -or
    [string]$web[0].webSecurityHeaders.reference -cne [string]$web[0].reference -or
    [string]$web[0].webSecurityHeaders.imageId -cne [string]$web[0].imageId -or
    [string]$web[0].webSecurityHeaders.scriptSha256 -cne (Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-web-security-headers.ps1')) -or
    (Get-FileSha256Lower -Path $webHeadersPath) -cne [string]$web[0].webSecurityHeaders.sha256 -or
    (Get-Content -Raw -LiteralPath $webHeadersPath) -notmatch '(?m)^Web root, immutable asset and admin security headers verified with Nginx\.\s*$') {
    throw 'derived web security-header proof is missing or stale'
}

$keycloak = @($records | Where-Object name -eq 'keycloak')
switch ($idpMode) {
    'keycloak' {
        $expectedKeycloakBase = 'quay.io/keycloak/keycloak:26.7.2@sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067'
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
                [string]$keycloak[0].runtimeSmoke.postgresReference -cne [string]$postgres[0].reference -or
                [string]$keycloak[0].runtimeSmoke.postgresImageId -cne [string]$postgres[0].imageId -or
                [string]$keycloak[0].runtimeSmoke.probeReference -cne [string]$ingest[0].reference -or
                [string]$keycloak[0].runtimeSmoke.probeImageId -cne [string]$ingest[0].imageId -or
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
                [string]$keycloak[0].realmProvisioning.postgresReference -cne [string]$postgres[0].reference -or
                [string]$keycloak[0].realmProvisioning.postgresImageId -cne [string]$postgres[0].imageId -or
                (Get-FileSha256Lower -Path $provisioningPath) -cne [string]$keycloak[0].realmProvisioning.sha256 -or
                [string]$keycloak[0].realmProvisioning.scriptSha256 -cne (Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-keycloak-provisioning.ps1')) -or
                [string]$keycloak[0].realmProvisioning.provisionerSha256 -cne (Get-FileSha256Lower -Path (Join-Path $projectRoot 'deploy\keycloak\provision-solov-realm.sh')) -or
                (Get-Content -Raw -LiteralPath $provisioningPath) -notmatch '(?m)^Disposable Keycloak 26\.7\.2 realm provisioning, output allowlist and one-secret publication passed\.\s*$') {
                throw 'Keycloak realm provisioning proof is missing or stale'
            }
        }
        if ($keycloak[0].vulnerabilities.total -eq 0) {
            if ([string]$keycloak[0].policyStatus -ceq 'failed') {
                if ([string]$manifest.idp.status -cne 'image_rejected') { throw 'failed Keycloak runtime smoke was not fail-closed' }
            } elseif ([string]$keycloak[0].policyStatus -cne 'approved' -or -not $runtimeSmokePassed -or -not $realmProvisioningPassed -or
                [string]$manifest.idp.status -cne 'image_approved_pending_canary' -or
                [string]$manifest.idp.productionCanary -cne 'pending') {
                throw 'zero-finding Keycloak image did not remain pending the production canary'
            }
        } elseif ($keycloak[0].vulnerabilities.total -eq 1) {
            $keycloakReportPath = Resolve-ReleaseArtifactPath -ReleaseDirectory $releaseRoot -RelativePath ([string]$keycloak[0].vulnerabilityReport.path)
            $keycloakReport = ConvertFrom-ReleaseJson -JsonText (Get-Content -Raw -LiteralPath $keycloakReportPath)
            $keycloakSummary = Assert-TrivyReportBinding -Report $keycloakReport -ExpectedImageId ([string]$keycloak[0].imageId)
            Assert-KeycloakVendorRejectedCveScope -ImageReference ([string]$keycloak[0].reference) -ImageId ([string]$keycloak[0].imageId) -BaseReference ([string]$keycloak[0].baseReference) -Summary $keycloakSummary -ExpectedBaseReference $expectedKeycloakBase | Out-Null
            $expectedKeycloakTarget = "$([string]$keycloak[0].reference) (redhat 9.8)"
            $expectedRationale = 'Red Hat officially rejected the CVE; AWS records that it affects Oracle proprietary bundled libpng while OpenJDK distributions using system libpng are not affected.'
            $expectedReviewedAt = '2026-08-31T00:00:00Z'
            $expectedReviewDueAt = '2026-09-30T00:00:00Z'
            if ([string]$keycloak[0].policyStatus -ceq 'failed') {
                if ([string]$manifest.idp.status -cne 'image_rejected') { throw 'failed Keycloak runtime smoke was not fail-closed' }
            } elseif ([string]$keycloak[0].policyStatus -cne 'approved-by-exact-vendor-rejection' -or -not $runtimeSmokePassed -or -not $realmProvisioningPassed -or
                [string]$keycloak[0].exception.scope -cne 'exact-keycloak-26.7.2-base-derived-image-and-single-finding-only' -or
                [string]$keycloak[0].exception.baseReference -cne $expectedKeycloakBase -or
                [string]$keycloak[0].exception.derivedImageId -cne [string]$keycloak[0].imageId -or
                [string]$keycloak[0].exception.target -cne $expectedKeycloakTarget -or
                [string]$keycloak[0].exception.vulnerabilityId -cne 'CVE-2026-22020' -or
                [string]$keycloak[0].exception.package -cne 'java-21-openjdk-headless' -or
                [string]$keycloak[0].exception.installedVersion -cne '1:21.0.12.1.1-1.2.el9' -or
                [string]$keycloak[0].exception.fixedVersion -cne '' -or
                [string]$keycloak[0].exception.trivyClass -cne 'os-pkgs' -or
                [string]$keycloak[0].exception.trivyType -cne 'redhat' -or
                [string]$keycloak[0].exception.severity -cne 'HIGH' -or
                [string]$keycloak[0].exception.trivyStatus -cne 'affected' -or
                [string]$keycloak[0].exception.disposition -cne 'vendor_rejected_not_affected' -or
                [string]$keycloak[0].exception.redHatEvidence -cne 'https://bugzilla.redhat.com/show_bug.cgi?id=2460045#c11' -or
                [string]$keycloak[0].exception.awsEvidence -cne 'https://explore.alas.aws.amazon.com/CVE-2026-22020.html' -or
                [string]$keycloak[0].exception.rationale -cne $expectedRationale -or
                [string]$keycloak[0].exception.reviewedAt -cne $expectedReviewedAt -or
                [string]$keycloak[0].exception.reviewDueAt -cne $expectedReviewDueAt -or
                [string]$keycloak[0].exception.runtimePruningProof -cne [string]$keycloak[0].runtimeProof.path -or
                [string]$manifest.idp.status -cne 'image_approved_pending_canary' -or
                [string]$manifest.idp.productionCanary -cne 'pending') {
                throw 'Keycloak vendor-rejection exception or pending canary binding is stale'
            }
            else {
                Assert-ExceptionReviewContract -Rationale ([string]$keycloak[0].exception.rationale) -ReviewedAt ([string]$keycloak[0].exception.reviewedAt) -ReviewDueAt ([string]$keycloak[0].exception.reviewDueAt) -CurrentTime ([DateTimeOffset]::UtcNow.ToString("yyyy-MM-dd'T'HH:mm:ss'Z'", [Globalization.CultureInfo]::InvariantCulture)) | Out-Null
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
