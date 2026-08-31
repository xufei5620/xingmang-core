[CmdletBinding()]
param(
    [ValidatePattern('^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$')]
    [string]$ReleaseName = '0.1.0-rc1',

    [ValidatePattern('^[0-9A-Za-z_][0-9A-Za-z_.-]{0,127}$')]
    [string]$ImageTag = 'release-candidate',

    [ValidatePattern('^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$')]
    [string]$SourceAgentVersion = '0.3.0',

    [ValidateSet('keycloak', 'none', 'external-managed')]
    [string]$IdPMode = 'keycloak',

    [string]$ReleaseDirectory = '',

    [ValidatePattern('^[0-9A-Za-z][0-9A-Za-z_.-]{0,127}$')]
    [string]$TrivyCacheVolume = 'invoice-release-gate-trivy-0-74-0'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$projectRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'release-image-gate-lib.ps1')

$trivyImage = 'ghcr.io/aquasecurity/trivy:0.74.0@sha256:62b1e65e8869bc4b4c6aa4fa2b21595256c7c2f6018a9d9ad61caf87187c1969'
$postgresBaseReference = 'postgres:18.6-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
$clamavBaseReference = 'clamav/clamav:1.4.5@sha256:4de20bd9ab45a4b763c5412b769217ef5082572ebc8a63aff1a77943419e5dd8'
$nginxBaseReference = 'nginx:1.30-alpine@sha256:97d490c12ba55b4946b01546d1c3ed324e8d41ab1c9fcb2a616aa470620e5b46'
$keycloakBaseReference = 'quay.io/keycloak/keycloak:26.7.2@sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067'
$keycloakExceptionRationale = 'Red Hat officially rejected the CVE; AWS records that it affects Oracle proprietary bundled libpng while OpenJDK distributions using system libpng are not affected.'
$keycloakExceptionReviewedAt = '2026-08-31T00:00:00Z'
$keycloakExceptionReviewDueAt = '2026-09-30T00:00:00Z'

function Write-Utf8NoBom {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][AllowEmptyString()][string]$Text)
    [IO.File]::WriteAllText($Path, $Text, [Text.UTF8Encoding]::new($false))
}

function Invoke-DockerLogged {
    param(
        [Parameter(Mandatory)][string[]]$Arguments,
        [Parameter(Mandatory)][string]$LogPath,
        [Parameter(Mandatory)][string]$Description
    )
    Write-Host "==> $Description"
    & docker @Arguments *> $LogPath
    if ($LASTEXITCODE -ne 0) {
        throw "$Description failed; see $(Get-ReleaseRelativePath -BasePath $releaseRoot -Path $LogPath)"
    }
}

function Ensure-PinnedImage {
    param(
        [Parameter(Mandatory)][string]$Reference,
        [Parameter(Mandatory)][string]$LogPath,
        [Parameter(Mandatory)][string]$Description
    )
    if ($Reference -notmatch '@sha256:[0-9a-f]{64}$') {
        throw "local registry fallback is forbidden for non-digest reference $Reference"
    }
    Write-Host "==> $Description"
    & docker pull $Reference *> $LogPath
    if ($LASTEXITCODE -eq 0) {
        return [pscustomobject]@{ Status = 'registry-refreshed'; ImageId = Get-RequiredImageId -Reference $Reference }
    }
    try {
        $localID = Get-RequiredImageId -Reference $Reference
    } catch {
        throw "$Description failed and the exact digest is not available locally; see $(Get-ReleaseRelativePath -BasePath $releaseRoot -Path $LogPath)"
    }
    [IO.File]::AppendAllText($LogPath, "`nREGISTRY_REFRESH_FAILED_EXACT_LOCAL_DIGEST_VERIFIED $localID`n", [Text.UTF8Encoding]::new($false))
    Write-Host "    registry unavailable; exact local digest verified as $localID"
    return [pscustomobject]@{ Status = 'registry-refresh-failed-exact-local-digest-verified'; ImageId = $localID }
}

function Invoke-DockerTextCapture {
    param(
        [Parameter(Mandatory)][string[]]$Arguments,
        [Parameter(Mandatory)][string]$OutputPath,
        [Parameter(Mandatory)][string]$ErrorLogPath,
        [Parameter(Mandatory)][string]$Description
    )
    Write-Host "==> $Description"
    $text = (& docker @Arguments 2> $ErrorLogPath | Out-String)
    $exitCode = $LASTEXITCODE
    Write-Utf8NoBom -Path $OutputPath -Text $text
    if ($exitCode -ne 0) {
        throw "$Description failed; see $(Get-ReleaseRelativePath -BasePath $releaseRoot -Path $ErrorLogPath)"
    }
}

function Get-ImageMetadata {
    param([Parameter(Mandatory)][string]$Reference)

    $raw = (& docker image inspect $Reference --format '{{json .}}' 2>$null | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($raw)) {
        throw "cannot inspect image $Reference"
    }
    $data = $raw | ConvertFrom-Json
    if ([string]$data.Id -notmatch '^sha256:[0-9a-f]{64}$') {
        throw "image $Reference has no immutable image ID"
    }
    return [pscustomobject]@{
        Id = [string]$data.Id
        Os = [string]$data.Os
        Architecture = [string]$data.Architecture
        Created = [string]$data.Created
    }
}

function Get-TrivyArguments {
    param([Parameter(Mandatory)][string[]]$Command)
    return @(
        'run', '--rm',
        '-v', '/var/run/docker.sock:/var/run/docker.sock',
        '-v', "${TrivyCacheVolume}:/root/.cache/trivy",
        $trivyImage
    ) + $Command
}

function Assert-TrivyDatabaseFreshness {
    param([Parameter(Mandatory)][string]$VersionText)

    if ($VersionText -notmatch '(?m)^Version:\s+0\.74\.0\s*$') {
        throw 'release gate did not execute exact Trivy 0.74.0'
    }
    $matches = [regex]::Matches($VersionText, '(?m)^\s+UpdatedAt:\s+(.+?)\s*$')
    if ($matches.Count -ne 2) {
        throw 'Trivy version evidence does not contain both vulnerability and Java database timestamps'
    }
    $now = [DateTimeOffset]::UtcNow
    $maximumAges = @([TimeSpan]::FromHours(48), [TimeSpan]::FromHours(96))
    for ($i = 0; $i -lt 2; $i++) {
        $rawTimestamp = $matches[$i].Groups[1].Value
        $updated = ConvertFrom-TrivyDatabaseTimestamp -Timestamp $rawTimestamp
        $age = $now - $updated.ToUniversalTime()
        if ($age -lt [TimeSpan]::FromMinutes(-5) -or $age -gt $maximumAges[$i]) {
            throw "Trivy database $i timestamp is outside the allowed freshness window"
        }
    }
}

function New-ImageDefinition {
    param(
        [string]$Name,
        [string]$ArtifactName,
        [string]$Reference,
        [string]$Kind,
        [string]$Policy,
        [string]$Context = '',
        [string]$Dockerfile = '',
        [string]$Target = '',
        [string[]]$BuildArguments = @(),
        [string]$BaseReference = ''
    )
    return [pscustomobject]@{
        Name = $Name
        ArtifactName = $ArtifactName
        Reference = $Reference
        Kind = $Kind
        Policy = $Policy
        Context = $Context
        Dockerfile = $Dockerfile
        Target = $Target
        BuildArguments = $BuildArguments
        BaseReference = $BaseReference
    }
}

if ([string]::IsNullOrWhiteSpace($ReleaseDirectory)) {
    $stamp = [DateTimeOffset]::UtcNow.ToString('yyyyMMddTHHmmssZ')
    $ReleaseDirectory = Join-Path $projectRoot "release\image-gate-$ReleaseName-$stamp"
} elseif (-not [IO.Path]::IsPathFullyQualified($ReleaseDirectory)) {
    $ReleaseDirectory = Join-Path $projectRoot $ReleaseDirectory
}
$releaseRoot = [IO.Path]::GetFullPath($ReleaseDirectory)
$allowedReleaseRoot = [IO.Path]::GetFullPath((Join-Path $projectRoot 'release')).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
if ($releaseRoot.TrimEnd([IO.Path]::DirectorySeparatorChar).Equals($allowedReleaseRoot.TrimEnd([IO.Path]::DirectorySeparatorChar), [StringComparison]::OrdinalIgnoreCase) -or
    -not ($releaseRoot + [IO.Path]::DirectorySeparatorChar).StartsWith($allowedReleaseRoot, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'ReleaseDirectory must be a child of the project release directory'
}
if (Test-Path -LiteralPath $releaseRoot) {
    if (((Get-Item -LiteralPath $releaseRoot -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw 'ReleaseDirectory cannot be a symlink/reparse point'
    }
    if (@(Get-ChildItem -LiteralPath $releaseRoot -Force).Count -ne 0) {
        throw "refusing to overwrite non-empty release directory: $releaseRoot"
    }
} else {
    New-Item -ItemType Directory -Path $releaseRoot | Out-Null
}
foreach ($name in @('build', 'logs', 'proof', 'reports', 'sbom')) {
    New-Item -ItemType Directory -Path (Join-Path $releaseRoot $name) | Out-Null
}
Write-Host "Release output: $releaseRoot"
Write-Host "IdP mode: $IdPMode (default keycloak mode must pass its image scan and remains blocked pending the production canary)"

$lockPath = Join-Path (Split-Path -Parent $releaseRoot) '.trivy-0.74.release-gate.lock'
try {
    $trivyLock = [IO.File]::Open($lockPath, [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
} catch {
    throw 'another release image gate is already using the shared Trivy cache'
}

try {
    Push-Location $projectRoot
    try {
        $sourceFingerprints = [ordered]@{
            backend = Get-ContextFingerprint -Root (Join-Path $projectRoot 'backend') -ExcludedDirectoryNames @('bin', 'coverage')
            web = Get-ContextFingerprint -Root (Join-Path $projectRoot 'web') -ExcludedDirectoryNames @('node_modules', 'dist', 'coverage', '.playwright-cli')
            agents = Get-ContextFingerprint -Root (Join-Path $projectRoot 'agents') -ExcludedDirectoryNames @('bin', 'coverage')
            keycloak = Get-ContextFingerprint -Root (Join-Path $projectRoot 'deploy\keycloak')
            postgres = Get-ContextFingerprint -Root (Join-Path $projectRoot 'deploy\postgres')
            clamav = Get-ContextFingerprint -Root (Join-Path $projectRoot 'deploy\clamav')
            ingestProxy = Get-ContextFingerprint -Root (Join-Path $projectRoot 'deploy\ingest-proxy')
            deployment = Get-ContextFingerprint -Root (Join-Path $projectRoot 'deploy')
        }

        $verificationLog = Join-Path $releaseRoot 'logs\source-verification.log'
        Write-Host '==> Running source, test, Compose and upstream-integrity gates'
        # Run the existing project verifier in an isolated PowerShell process.
        # This gate intentionally uses StrictMode; the legacy verifier should
        # not inherit that caller setting and change behavior by invocation path.
        & pwsh -NoProfile -File (Join-Path $PSScriptRoot 'verify.ps1') *>&1 | Tee-Object -FilePath $verificationLog
        if ($LASTEXITCODE -ne 0) { throw 'source verification gate failed' }

        Invoke-DockerLogged -Arguments @('volume', 'create', $TrivyCacheVolume) -LogPath (Join-Path $releaseRoot 'logs\trivy-cache.log') -Description 'Ensuring isolated Trivy cache volume exists'
        $trivyAcquisition = Ensure-PinnedImage -Reference $trivyImage -LogPath (Join-Path $releaseRoot 'logs\trivy-tool-pull.log') -Description 'Refreshing exact Trivy 0.74.0 tool digest'
        $trivyToolID = Get-RequiredImageId -Reference $trivyImage
        if ($trivyAcquisition.ImageId -cne $trivyToolID) { throw 'Trivy tool ID changed after acquisition' }
        Invoke-DockerLogged -Arguments (Get-TrivyArguments -Command @('image', '--timeout', '15m', '--download-db-only', '--no-progress')) -LogPath (Join-Path $releaseRoot 'logs\trivy-db-update.log') -Description 'Updating Trivy vulnerability database'
        Invoke-DockerLogged -Arguments (Get-TrivyArguments -Command @('image', '--timeout', '15m', '--download-java-db-only', '--no-progress')) -LogPath (Join-Path $releaseRoot 'logs\trivy-java-db-update.log') -Description 'Updating Trivy Java database'
        $trivyVersionPath = Join-Path $releaseRoot 'proof\trivy-version.txt'
        Invoke-DockerTextCapture -Arguments (Get-TrivyArguments -Command @('--version')) -OutputPath $trivyVersionPath -ErrorLogPath (Join-Path $releaseRoot 'logs\trivy-version.log') -Description 'Recording Trivy and database versions'
        $trivyVersionText = Get-Content -Raw -LiteralPath $trivyVersionPath
        Assert-TrivyDatabaseFreshness -VersionText $trivyVersionText

        $definitions = [Collections.Generic.List[object]]::new()
        $definitions.Add((New-ImageDefinition -Name 'api' -ArtifactName 'invoice-system-api' -Reference "invoice-system-api:$ImageTag" -Kind 'built' -Policy 'zero-findings' -Context 'backend' -Dockerfile 'backend/Dockerfile' -Target 'api'))
        $definitions.Add((New-ImageDefinition -Name 'pdf-scanner' -ArtifactName 'invoice-system-pdf-scanner' -Reference "invoice-system-pdf-scanner:$ImageTag" -Kind 'built' -Policy 'zero-findings' -Context 'backend' -Dockerfile 'backend/Dockerfile' -Target 'scanner'))
        $definitions.Add((New-ImageDefinition -Name 'tools' -ArtifactName 'invoice-system-tools' -Reference "invoice-system-tools:$ImageTag" -Kind 'built' -Policy 'zero-findings' -Context 'backend' -Dockerfile 'backend/Dockerfile' -Target 'tools'))
        $definitions.Add((New-ImageDefinition -Name 'web' -ArtifactName 'invoice-system-web' -Reference "invoice-system-web:$ImageTag" -Kind 'built' -Policy 'zero-findings' -Context 'web' -Dockerfile 'web/Dockerfile'))
        $definitions.Add((New-ImageDefinition -Name 'source-agent' -ArtifactName 'invoice-source-agent' -Reference "invoice-source-agent:$ImageTag" -Kind 'built' -Policy 'zero-findings' -Context 'agents' -Dockerfile 'agents/Dockerfile.production' -Target 'production' -BuildArguments @('SOURCE_AGENT_VERSION=' + $SourceAgentVersion)))
        $definitions.Add((New-ImageDefinition -Name 'postgres-runtime' -ArtifactName 'invoice-postgres' -Reference "invoice-postgres:$ImageTag" -Kind 'built' -Policy 'zero-findings' -Context 'postgres' -Dockerfile 'deploy/postgres/Dockerfile' -BuildArguments @('POSTGRES_BASE_IMAGE=' + $postgresBaseReference) -BaseReference $postgresBaseReference))
        $definitions.Add((New-ImageDefinition -Name 'clamav-runtime' -ArtifactName 'invoice-clamav' -Reference "invoice-clamav:$ImageTag" -Kind 'built' -Policy 'zero-findings' -Context 'clamav' -Dockerfile 'deploy/clamav/Dockerfile' -BuildArguments @('CLAMAV_BASE_IMAGE=' + $clamavBaseReference) -BaseReference $clamavBaseReference))
        $definitions.Add((New-ImageDefinition -Name 'ingest-proxy' -ArtifactName 'invoice-ingest-proxy' -Reference "invoice-ingest-proxy:$ImageTag" -Kind 'built' -Policy 'zero-findings' -Context 'ingest-proxy' -Dockerfile 'deploy/ingest-proxy/Dockerfile' -BuildArguments @('NGINX_BASE_IMAGE=' + $nginxBaseReference) -BaseReference $nginxBaseReference))
        if ($IdPMode -ceq 'keycloak') {
            $definitions.Add((New-ImageDefinition -Name 'keycloak' -ArtifactName 'invoice-keycloak' -Reference "invoice-keycloak:$ImageTag" -Kind 'built' -Policy 'keycloak-26.7.2-exact-vendor-rejection' -Context 'keycloak' -Dockerfile 'deploy/keycloak/Dockerfile' -BaseReference $keycloakBaseReference))
        }

        $imageAcquisition = @{}
        foreach ($definition in $definitions) {
            if ($definition.Kind -in @('built', 'built-idp')) {
                # BuildKit's default provenance attestation creates a fresh
                # manifest-list ID on every local build. The release gate emits
                # its own manifest/SBOM evidence, so disable that nondeterministic
                # wrapper and bind the tag directly to the reproducible image.
                $arguments = @('build', '--pull', '--platform', 'linux/amd64', '--provenance=false', '--file', (Join-Path $projectRoot $definition.Dockerfile), '--tag', $definition.Reference)
                if (-not [string]::IsNullOrWhiteSpace($definition.Target)) { $arguments += @('--target', $definition.Target) }
                foreach ($buildArgument in $definition.BuildArguments) { $arguments += @('--build-arg', $buildArgument) }
                $contextPath = switch ($definition.Context) {
                    'backend' { Join-Path $projectRoot 'backend' }
                    'web' { Join-Path $projectRoot 'web' }
                    'agents' { Join-Path $projectRoot 'agents' }
                    'keycloak' { Join-Path $projectRoot 'deploy\keycloak' }
                    'postgres' { Join-Path $projectRoot 'deploy\postgres' }
                    'clamav' { Join-Path $projectRoot 'deploy\clamav' }
                    'ingest-proxy' { Join-Path $projectRoot 'deploy\ingest-proxy' }
                    default { throw "unknown build context $($definition.Context)" }
                }
                $arguments += $contextPath
                Invoke-DockerLogged -Arguments $arguments -LogPath (Join-Path $releaseRoot "build\$($definition.ArtifactName).log") -Description "Building $($definition.Reference)"
            } else {
                $imageAcquisition[$definition.Name] = Ensure-PinnedImage -Reference $definition.Reference -LogPath (Join-Path $releaseRoot "build\$($definition.ArtifactName).pull.log") -Description "Refreshing $($definition.Reference)"
            }
        }

        $imageMetadata = @{}
        foreach ($definition in $definitions) {
            $metadata = Get-ImageMetadata -Reference $definition.Reference
            Assert-LinuxAmd64Platform -Platform "$($metadata.Os)/$($metadata.Architecture)" | Out-Null
            $imageMetadata[$definition.Name] = $metadata
            Write-Host "    $($definition.Name): $($metadata.Id)"
        }

        $imageRecords = [Collections.Generic.List[object]]::new()
        $applicationFailures = [Collections.Generic.List[string]]::new()
        foreach ($definition in $definitions) {
            $metadata = $imageMetadata[$definition.Name]
            $reportRelative = "reports/$($definition.ArtifactName).trivy.json"
            $sbomRelative = "sbom/$($definition.ArtifactName).cdx.json"
            $reportPath = Join-Path $releaseRoot ($reportRelative -replace '/', [IO.Path]::DirectorySeparatorChar)
            $sbomPath = Join-Path $releaseRoot ($sbomRelative -replace '/', [IO.Path]::DirectorySeparatorChar)
            $reportLog = Join-Path $releaseRoot "logs\$($definition.ArtifactName).trivy.log"
            $sbomLog = Join-Path $releaseRoot "logs\$($definition.ArtifactName).sbom.log"

            $scanCommand = @('image', '--timeout', '15m', '--skip-db-update', '--skip-java-db-update', '--no-progress', '--scanners', 'vuln', '--severity', 'HIGH,CRITICAL', '--format', 'json', $definition.Reference)
            Invoke-DockerTextCapture -Arguments (Get-TrivyArguments -Command $scanCommand) -OutputPath $reportPath -ErrorLogPath $reportLog -Description "Scanning $($definition.Reference) (serial HIGH/CRITICAL policy)"
            $report = Get-Content -Raw -LiteralPath $reportPath | ConvertFrom-Json
            $summary = Assert-TrivyReportBinding -Report $report -ExpectedImageId $metadata.Id

            $sbomCommand = @('image', '--timeout', '15m', '--skip-db-update', '--skip-java-db-update', '--no-progress', '--scanners', 'vuln', '--format', 'cyclonedx', $definition.Reference)
            Invoke-DockerTextCapture -Arguments (Get-TrivyArguments -Command $sbomCommand) -OutputPath $sbomPath -ErrorLogPath $sbomLog -Description "Generating CycloneDX SBOM for $($definition.Reference)"
            $bom = Get-Content -Raw -LiteralPath $sbomPath | ConvertFrom-Json
            Assert-CycloneDxBinding -Bom $bom -ExpectedImageId $metadata.Id | Out-Null

            $policyStatus = 'approved'
            $policyReason = 'zero_high_or_critical_findings'
            $exception = $null
            $runtimeProof = $null
            if ($definition.Policy -ceq 'zero-findings' -and $summary.Total -ne 0) {
                $policyStatus = 'failed'
                $policyReason = 'high_or_critical_findings'
            } elseif ($definition.Policy -ceq 'keycloak-26.7.2-exact-vendor-rejection') {
                try {
                    $pruningPath = Join-Path $releaseRoot 'proof\keycloak-runtime-pruning.txt'
                    $pruningCommand = "test ! -e /opt/keycloak/bin/client && ! find /opt/keycloak -type f -name 'com.microsoft.sqlserver.mssql-jdbc-*.jar' -print -quit | grep -q . && printf 'bin/client=absent\nmssql-driver=absent\n'"
                    Invoke-DockerTextCapture -Arguments @('run', '--rm', '--entrypoint', '/bin/bash', $definition.Reference, '-ec', $pruningCommand) -OutputPath $pruningPath -ErrorLogPath (Join-Path $releaseRoot 'logs\keycloak-runtime-pruning.log') -Description 'Proving Keycloak admin CLI and unused MSSQL driver are absent from the derived image'
                    $pruningText = (Get-Content -Raw -LiteralPath $pruningPath).Replace("`r`n", "`n").Trim()
                    if ($pruningText -cne "bin/client=absent`nmssql-driver=absent") {
                        throw 'Keycloak runtime pruning proof output is not exact'
                    }
                    $runtimeProof = [ordered]@{
                        type = 'keycloak-runtime-pruning'
                        path = 'proof/keycloak-runtime-pruning.txt'
                        sha256 = Get-FileSha256Lower -Path $pruningPath
                        imageId = $metadata.Id
                        assertions = @('bin/client=absent', 'mssql-driver=absent')
                    }
                    if ($summary.Total -eq 0) {
                        $policyStatus = 'approved'
                        $policyReason = 'zero_high_or_critical_findings'
                    } else {
                        Assert-KeycloakVendorRejectedCveScope -ImageReference $definition.Reference -ImageId $metadata.Id -BaseReference $definition.BaseReference -Summary $summary -ExpectedBaseReference $keycloakBaseReference | Out-Null
                        Assert-ExceptionReviewContract -Rationale $keycloakExceptionRationale -ReviewedAt $keycloakExceptionReviewedAt -ReviewDueAt $keycloakExceptionReviewDueAt -CurrentTime ([DateTimeOffset]::UtcNow.ToString("yyyy-MM-dd'T'HH:mm:ss'Z'", [Globalization.CultureInfo]::InvariantCulture)) | Out-Null
                        $policyStatus = 'approved-by-exact-vendor-rejection'
                        $policyReason = 'exact_redhat_openjdk_libpng_false_positive_only'
                        $exception = [ordered]@{
                            scope = 'exact-keycloak-26.7.2-base-derived-image-and-single-finding-only'
                            baseReference = $keycloakBaseReference
                            derivedImageId = $metadata.Id
                            target = "$($definition.Reference) (redhat 9.8)"
                            vulnerabilityId = 'CVE-2026-22020'
                            package = 'java-21-openjdk-headless'
                            installedVersion = '1:21.0.12.1.1-1.2.el9'
                            fixedVersion = ''
                            trivyClass = 'os-pkgs'
                            trivyType = 'redhat'
                            trivyStatus = 'affected'
                            severity = 'HIGH'
                            disposition = 'vendor_rejected_not_affected'
                            redHatEvidence = 'https://bugzilla.redhat.com/show_bug.cgi?id=2460045#c11'
                            awsEvidence = 'https://explore.alas.aws.amazon.com/CVE-2026-22020.html'
                            rationale = $keycloakExceptionRationale
                            reviewedAt = $keycloakExceptionReviewedAt
                            reviewDueAt = $keycloakExceptionReviewDueAt
                            runtimePruningProof = 'proof/keycloak-runtime-pruning.txt'
                        }
                    }
                } catch {
                    $policyStatus = 'failed'
                    $policyReason = 'keycloak_exact_exception_or_pruning_proof_failed: ' + $_.Exception.Message
                }
            }

            if ($policyStatus -in @('failed', 'rejected') -and $definition.Name -ne 'keycloak') {
                $applicationFailures.Add("$($definition.Name):$policyReason")
            }
            $imageRecords.Add([ordered]@{
                name = $definition.Name
                kind = $definition.Kind
                reference = $definition.Reference
                imageId = $metadata.Id
                platform = "$($metadata.Os)/$($metadata.Architecture)"
                created = $metadata.Created
                acquisition = if ($definition.Kind -eq 'pinned-external') { $imageAcquisition[$definition.Name].Status } else { 'built-from-source' }
                policy = $definition.Policy
                policyStatus = $policyStatus
                policyReason = $policyReason
                baseReference = if ([string]::IsNullOrWhiteSpace($definition.BaseReference)) { $null } else { $definition.BaseReference }
                vulnerabilities = [ordered]@{
                    high = $summary.High
                    critical = $summary.Critical
                    total = $summary.Total
                }
                vulnerabilityReport = [ordered]@{
                    path = $reportRelative
                    sha256 = Get-FileSha256Lower -Path $reportPath
                }
                sbom = [ordered]@{
                    format = 'CycloneDX'
                    specVersion = [string]$bom.specVersion
                    path = $sbomRelative
                    sha256 = Get-FileSha256Lower -Path $sbomPath
                    imageId = $metadata.Id
                }
                exception = $exception
                runtimeProof = $runtimeProof
                runtimeSmoke = $null
                realmProvisioning = $null
                nginxConfiguration = $null
                webSecurityHeaders = $null
            })
        }

        $postgresRuntimeRecords = @($imageRecords | Where-Object { $_.name -ceq 'postgres-runtime' })
        $ingestRuntimeRecords = @($imageRecords | Where-Object { $_.name -ceq 'ingest-proxy' })
        $webRuntimeRecords = @($imageRecords | Where-Object { $_.name -ceq 'web' })
        if ($postgresRuntimeRecords.Count -ne 1 -or $ingestRuntimeRecords.Count -ne 1 -or $webRuntimeRecords.Count -ne 1) {
            throw 'release gate did not produce exact PostgreSQL, ingest-proxy, and web image records'
        }
        $postgresRuntimeRecord = $postgresRuntimeRecords[0]
        $ingestRuntimeRecord = $ingestRuntimeRecords[0]
        $webRuntimeRecord = $webRuntimeRecords[0]

        $nginxConfigurationPath = Join-Path $releaseRoot 'proof\ingest-nginx-configuration.txt'
        $nginxConfigurationErrorPath = Join-Path $releaseRoot 'logs\ingest-nginx-configuration.log'
        Write-Host '==> Verifying edge/ingest Nginx configuration with the exact derived ingest image'
        $nginxConfigurationText = (& pwsh -NoProfile -File (Join-Path $PSScriptRoot 'verify-nginx-configs.ps1') -Image ([string]$ingestRuntimeRecord.reference) -ExpectedImageID ([string]$ingestRuntimeRecord.imageId) 2> $nginxConfigurationErrorPath | Out-String)
        $nginxConfigurationExit = $LASTEXITCODE
        Write-Utf8NoBom -Path $nginxConfigurationPath -Text $nginxConfigurationText
        $ingestRuntimeRecord['nginxConfiguration'] = [ordered]@{
            status = if ($nginxConfigurationExit -eq 0 -and $nginxConfigurationText -match '(?m)^Public, split-admin OIDC and mTLS Nginx configurations passed syntax and security gates\.\s*$') { 'passed' } else { 'failed' }
            path = 'proof/ingest-nginx-configuration.txt'
            sha256 = Get-FileSha256Lower -Path $nginxConfigurationPath
            script = 'scripts/verify-nginx-configs.ps1'
            scriptSha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-nginx-configs.ps1')
            reference = [string]$ingestRuntimeRecord.reference
            imageId = [string]$ingestRuntimeRecord.imageId
        }
        if ([string]$ingestRuntimeRecord.nginxConfiguration.status -cne 'passed') {
            $ingestRuntimeRecord['policyStatus'] = 'failed'
            $ingestRuntimeRecord['policyReason'] = 'derived_ingest_nginx_configuration_failed'
            $applicationFailures.Add('ingest-proxy:derived_ingest_nginx_configuration_failed')
        }

        $webHeadersPath = Join-Path $releaseRoot 'proof\web-security-headers.txt'
        $webHeadersErrorPath = Join-Path $releaseRoot 'logs\web-security-headers.log'
        Write-Host '==> Verifying security headers with the exact derived web image'
        $webHeadersText = (& pwsh -NoProfile -File (Join-Path $PSScriptRoot 'verify-web-security-headers.ps1') -Image ([string]$webRuntimeRecord.reference) -ExpectedImageID ([string]$webRuntimeRecord.imageId) 2> $webHeadersErrorPath | Out-String)
        $webHeadersExit = $LASTEXITCODE
        Write-Utf8NoBom -Path $webHeadersPath -Text $webHeadersText
        $webRuntimeRecord['webSecurityHeaders'] = [ordered]@{
            status = if ($webHeadersExit -eq 0 -and $webHeadersText -match '(?m)^Web root and immutable asset security headers verified with Nginx\.\s*$') { 'passed' } else { 'failed' }
            path = 'proof/web-security-headers.txt'
            sha256 = Get-FileSha256Lower -Path $webHeadersPath
            script = 'scripts/verify-web-security-headers.ps1'
            scriptSha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-web-security-headers.ps1')
            reference = [string]$webRuntimeRecord.reference
            imageId = [string]$webRuntimeRecord.imageId
        }
        if ([string]$webRuntimeRecord.webSecurityHeaders.status -cne 'passed') {
            $webRuntimeRecord['policyStatus'] = 'failed'
            $webRuntimeRecord['policyReason'] = 'derived_web_security_headers_failed'
            $applicationFailures.Add('web:derived_web_security_headers_failed')
        }

        if ($IdPMode -ceq 'keycloak') {
            $keycloakRuntimeRecords = @($imageRecords | Where-Object { $_.name -ceq 'keycloak' })
            if ($keycloakRuntimeRecords.Count -ne 1) { throw 'Keycloak mode did not produce exactly one image record' }
            $keycloakRuntimeRecord = $keycloakRuntimeRecords[0]
            if ($keycloakRuntimeRecord.policyStatus -in @('approved', 'approved-by-exact-vendor-rejection') -and
                [string]$postgresRuntimeRecord.policyStatus -ceq 'approved' -and
                [string]$ingestRuntimeRecord.policyStatus -ceq 'approved') {
                $runtimeSmokePath = Join-Path $releaseRoot 'proof\keycloak-runtime-smoke.txt'
                $runtimeSmokeErrorPath = Join-Path $releaseRoot 'logs\keycloak-runtime-smoke.log'
                Write-Host '==> Running isolated Keycloak/PostgreSQL health and OIDC discovery smoke'
                $runtimeSmokeText = (& pwsh -NoProfile -File (Join-Path $PSScriptRoot 'verify-keycloak-runtime.ps1') -Image ([string]$keycloakRuntimeRecord.reference) -ExpectedImageID ([string]$keycloakRuntimeRecord.imageId) -PostgresImage ([string]$postgresRuntimeRecord.reference) -ExpectedPostgresImageID ([string]$postgresRuntimeRecord.imageId) -ProbeImage ([string]$ingestRuntimeRecord.reference) -ExpectedProbeImageID ([string]$ingestRuntimeRecord.imageId) 2> $runtimeSmokeErrorPath | Out-String)
                $runtimeSmokeExit = $LASTEXITCODE
                Write-Utf8NoBom -Path $runtimeSmokePath -Text $runtimeSmokeText
                if ($runtimeSmokeExit -eq 0 -and $runtimeSmokeText -match '(?m)^Hardened Keycloak runtime, PostgreSQL schema, health and OIDC discovery smoke passed\.\s*$') {
                    $keycloakRuntimeRecord['runtimeSmoke'] = [ordered]@{
                        status = 'passed'
                        path = 'proof/keycloak-runtime-smoke.txt'
                        sha256 = Get-FileSha256Lower -Path $runtimeSmokePath
                        script = 'scripts/verify-keycloak-runtime.ps1'
                        scriptSha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-keycloak-runtime.ps1')
                        imageId = [string]$keycloakRuntimeRecord.imageId
                        postgresReference = [string]$postgresRuntimeRecord.reference
                        postgresImageId = [string]$postgresRuntimeRecord.imageId
                        probeReference = [string]$ingestRuntimeRecord.reference
                        probeImageId = [string]$ingestRuntimeRecord.imageId
                        assertions = @('postgresql18-schema', 'keycloak26.7.2-startup', 'health-ready', 'oidc-discovery', 'rp-logout', 'backchannel-logout', 'session-logout', 'RS256', 'no-linkage-errors')
                    }
                } else {
                    $keycloakRuntimeRecord['policyStatus'] = 'failed'
                    $keycloakRuntimeRecord['policyReason'] = 'keycloak_runtime_oidc_smoke_failed'
                    $keycloakRuntimeRecord['runtimeSmoke'] = [ordered]@{
                        status = 'failed'
                        path = 'proof/keycloak-runtime-smoke.txt'
                        sha256 = Get-FileSha256Lower -Path $runtimeSmokePath
                        script = 'scripts/verify-keycloak-runtime.ps1'
                        scriptSha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-keycloak-runtime.ps1')
                        imageId = [string]$keycloakRuntimeRecord.imageId
                        postgresReference = [string]$postgresRuntimeRecord.reference
                        postgresImageId = [string]$postgresRuntimeRecord.imageId
                        probeReference = [string]$ingestRuntimeRecord.reference
                        probeImageId = [string]$ingestRuntimeRecord.imageId
                    }
                }
                if ([string]$keycloakRuntimeRecord.runtimeSmoke.status -ceq 'passed') {
                    $provisioningPath = Join-Path $releaseRoot 'proof\keycloak-realm-provisioning.txt'
                    $provisioningErrorPath = Join-Path $releaseRoot 'logs\keycloak-realm-provisioning.log'
                    Write-Host '==> Running isolated Keycloak realm/client/LoA2 provisioning contract'
                    $provisioningText = (& pwsh -NoProfile -File (Join-Path $PSScriptRoot 'verify-keycloak-provisioning.ps1') -KeycloakImage ([string]$keycloakRuntimeRecord.reference) -ExpectedKeycloakImageID ([string]$keycloakRuntimeRecord.imageId) -PostgresImage ([string]$postgresRuntimeRecord.reference) -ExpectedPostgresImageID ([string]$postgresRuntimeRecord.imageId) 2> $provisioningErrorPath | Out-String)
                    $provisioningExit = $LASTEXITCODE
                    Write-Utf8NoBom -Path $provisioningPath -Text $provisioningText
                    if ($provisioningExit -eq 0 -and $provisioningText -match '(?m)^Disposable Keycloak 26\.7\.2 realm provisioning, output allowlist and one-secret publication passed\.\s*$') {
                        $keycloakRuntimeRecord['realmProvisioning'] = [ordered]@{
                            status = 'passed'
                            path = 'proof/keycloak-realm-provisioning.txt'
                            sha256 = Get-FileSha256Lower -Path $provisioningPath
                            script = 'scripts/verify-keycloak-provisioning.ps1'
                            scriptSha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-keycloak-provisioning.ps1')
                            provisionerSha256 = Get-FileSha256Lower -Path (Join-Path $projectRoot 'deploy\keycloak\provision-solov-realm.sh')
                            imageId = [string]$keycloakRuntimeRecord.imageId
                            postgresReference = [string]$postgresRuntimeRecord.reference
                            postgresImageId = [string]$postgresRuntimeRecord.imageId
                            assertions = @('realm-policy', 'default-user-role', 'loa1-loa2', 'roles-acr-amr-mappers', 'invoice-auth-time-scope', 'four-clients', 'no-offline-scope', 'disabled-desktop', 'single-secret-0400', 'fixed-output', 'existing-realm-refusal', 'log-redaction')
                        }
                    } else {
                        $keycloakRuntimeRecord['policyStatus'] = 'failed'
                        $keycloakRuntimeRecord['policyReason'] = 'keycloak_realm_provisioning_contract_failed'
                        $keycloakRuntimeRecord['realmProvisioning'] = [ordered]@{
                            status = 'failed'
                            path = 'proof/keycloak-realm-provisioning.txt'
                            sha256 = Get-FileSha256Lower -Path $provisioningPath
                            script = 'scripts/verify-keycloak-provisioning.ps1'
                            scriptSha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-keycloak-provisioning.ps1')
                            provisionerSha256 = Get-FileSha256Lower -Path (Join-Path $projectRoot 'deploy\keycloak\provision-solov-realm.sh')
                            imageId = [string]$keycloakRuntimeRecord.imageId
                            postgresReference = [string]$postgresRuntimeRecord.reference
                            postgresImageId = [string]$postgresRuntimeRecord.imageId
                        }
                    }
                }
            } elseif ($keycloakRuntimeRecord.policyStatus -in @('approved', 'approved-by-exact-vendor-rejection')) {
                $keycloakRuntimeRecord['policyStatus'] = 'failed'
                $keycloakRuntimeRecord['policyReason'] = 'keycloak_runtime_dependency_image_failed'
            }
        }

        foreach ($definition in $definitions) {
            $currentID = Get-RequiredImageId -Reference $definition.Reference
            if ($currentID -cne $imageMetadata[$definition.Name].Id) {
                throw "image tag/reference changed during gate: $($definition.Reference)"
            }
        }
        $endingFingerprints = [ordered]@{
            backend = Get-ContextFingerprint -Root (Join-Path $projectRoot 'backend') -ExcludedDirectoryNames @('bin', 'coverage')
            web = Get-ContextFingerprint -Root (Join-Path $projectRoot 'web') -ExcludedDirectoryNames @('node_modules', 'dist', 'coverage', '.playwright-cli')
            agents = Get-ContextFingerprint -Root (Join-Path $projectRoot 'agents') -ExcludedDirectoryNames @('bin', 'coverage')
            keycloak = Get-ContextFingerprint -Root (Join-Path $projectRoot 'deploy\keycloak')
            postgres = Get-ContextFingerprint -Root (Join-Path $projectRoot 'deploy\postgres')
            clamav = Get-ContextFingerprint -Root (Join-Path $projectRoot 'deploy\clamav')
            ingestProxy = Get-ContextFingerprint -Root (Join-Path $projectRoot 'deploy\ingest-proxy')
            deployment = Get-ContextFingerprint -Root (Join-Path $projectRoot 'deploy')
        }
        foreach ($name in $sourceFingerprints.Keys) {
            if ($sourceFingerprints[$name] -cne $endingFingerprints[$name]) {
                throw "build context changed while release gate was running: $name"
            }
        }

        $keycloakImageApproved = $false
        if ($IdPMode -ceq 'keycloak') {
            $keycloakRecords = @($imageRecords | Where-Object { $_.name -ceq 'keycloak' })
            $keycloakImageApproved = $keycloakRecords.Count -eq 1 -and $keycloakRecords[0].policyStatus -in @('approved', 'approved-by-exact-vendor-rejection')
        }
        $idpDecision = Get-IdpGateDecision -Mode $IdPMode -KeycloakImageApproved $keycloakImageApproved

        $gitHead = $null
        $headOutput = (& git rev-parse --verify HEAD 2>$null | Out-String).Trim()
        if ($LASTEXITCODE -eq 0 -and $headOutput -match '^[0-9a-f]{40}$') { $gitHead = $headOutput }
        $gitDirty = @(& git status --porcelain=v1).Count -ne 0
        $productionReasons = [Collections.Generic.List[string]]::new()
        foreach ($failure in $applicationFailures) { $productionReasons.Add($failure) }
        $productionReasons.Add($idpDecision.BlockReason)
        if ($null -eq $gitHead) { $productionReasons.Add('source_git_head_missing') }
        if ($gitDirty) { $productionReasons.Add('source_worktree_dirty') }
        $applicationStatus = if ($applicationFailures.Count -eq 0) { 'passed' } else { 'failed' }
        $productionStatus = if ($productionReasons.Count -eq 0) { 'approved' } else { 'blocked' }

        $manifest = [ordered]@{
            schemaVersion = 'solov.invoice.release-image-gate/v1'
            releaseName = $ReleaseName
            generatedAt = [DateTimeOffset]::UtcNow.ToString('o')
            policy = [ordered]@{
                trivySeverities = @('HIGH', 'CRITICAL')
                ignoreUnfixed = $false
                ignoredVulnerabilities = @()
                trivyExecution = 'serial'
                staleImageArtifacts = 'fail'
                postgresException = $null
                keycloakException = 'exact 26.7.2 base plus derived image ID plus exact single Red Hat rejected CVE tuple plus runtime pruning proof only'
            }
            tools = [ordered]@{
                script = 'scripts/release-image-gate.ps1'
                scriptSha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'release-image-gate.ps1')
                librarySha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'release-image-gate-lib.ps1')
                verifierSha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-release-image-artifacts.ps1')
                sourceVerifierSha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify.ps1')
                readinessIndexOperatorGateSha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-source-readiness-index-operator.ps1')
                readinessIndexOperatorSha256 = Get-FileSha256Lower -Path (Join-Path $projectRoot 'deploy\postgres\apply-source-readiness-index-concurrently.sh')
                readinessIndexVerifierSha256 = Get-FileSha256Lower -Path (Join-Path $projectRoot 'deploy\postgres\verify-source-readiness-index.sh')
                keycloakRuntimeVerifierSha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-keycloak-runtime.ps1')
                keycloakProvisioningVerifierSha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-keycloak-provisioning.ps1')
                keycloakProvisionerSha256 = Get-FileSha256Lower -Path (Join-Path $projectRoot 'deploy\keycloak\provision-solov-realm.sh')
                nginxVerifierSha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-nginx-configs.ps1')
                webSecurityHeadersVerifierSha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-web-security-headers.ps1')
                trivyReference = $trivyImage
                trivyImageId = $trivyToolID
                trivyAcquisition = $trivyAcquisition.Status
                trivyVersionEvidence = 'proof/trivy-version.txt'
            }
            source = [ordered]@{
                gitHead = $gitHead
                gitDirty = $gitDirty
                contextFingerprints = $sourceFingerprints
                verificationLog = 'logs/source-verification.log'
            }
            idp = [ordered]@{
                mode = $IdPMode
                status = $idpDecision.Status
                productionCanary = $idpDecision.ProductionCanary
                note = $idpDecision.Note
                imageGate = if ($IdPMode -ceq 'keycloak') { if ($keycloakImageApproved) { 'passed' } else { 'failed' } } else { 'not_applicable' }
            }
            images = @($imageRecords)
            decisions = [ordered]@{
                applicationImageGate = $applicationStatus
                productionLaunch = $productionStatus
                reasons = @($productionReasons)
            }
        }
        $manifestPath = Join-Path $releaseRoot 'release-manifest.json'
        Write-Utf8NoBom -Path $manifestPath -Text (($manifest | ConvertTo-Json -Depth 20) + "`n")

        $readme = @"
# $ReleaseName image-gate artifacts

Generated by `scripts/release-image-gate.ps1` using exact Trivy 0.74.0.
`release-manifest.json` is the machine-readable source of truth. Every Trivy
report and CycloneDX SBOM is bound to the immutable image ID in that manifest.
`SHA256SUMS` covers every generated artifact except itself.

IdP mode: `$IdPMode`
Application image gate: `$applicationStatus`
Production launch: `$productionStatus`

PostgreSQL has no RC50 exception path. Its locally built linux/amd64 release
image must have zero HIGH/CRITICAL findings and retain a null exception record.
The Keycloak exception, when present, retains the Trivy finding and is bound to
the exact 26.7.2 base digest, current derived image ID, one CVE/package/version
tuple and proof that the admin CLI and unused MSSQL driver are absent.
The same immutable Keycloak image must also pass the disposable realm/client,
LoA2, mapper, output-redaction and one-secret-publication contract.
"@
        Write-Utf8NoBom -Path (Join-Path $releaseRoot 'README.md') -Text ($readme + "`n")

        $generatedManifest = Get-Content -Raw -LiteralPath $manifestPath | ConvertFrom-Json
        foreach ($record in @($generatedManifest.images)) {
            Assert-GeneratedArtifactBinding -ReleaseDirectory $releaseRoot -ImageRecord $record | Out-Null
        }
        Write-Sha256Sums -ReleaseDirectory $releaseRoot | Out-Null
        Assert-Sha256Sums -ReleaseDirectory $releaseRoot | Out-Null
        & (Join-Path $PSScriptRoot 'verify-release-image-artifacts.ps1') -ReleaseDirectory $releaseRoot
        if ($LASTEXITCODE -ne 0) { throw 'independent release artifact verification failed' }

        Write-Host "Release manifest: $manifestPath"
        Write-Host "Application image gate: $applicationStatus"
        Write-Host "Production launch: $productionStatus"
        if ($productionStatus -ne 'approved') {
            throw "release remains blocked: $($productionReasons -join '; ')"
        }
        Write-Host 'Release image gate passed.'
    } finally {
        Pop-Location
    }
} finally {
    $trivyLock.Dispose()
}

$global:LASTEXITCODE = 0
