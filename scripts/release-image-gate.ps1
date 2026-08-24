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
$postgresReference = 'postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
$postgresImageID = 'sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
$postgresGosuSha256 = '52c8749d0142edd234e9d6bd5237dff2d81e71f43537e2f4f66f75dd4b243dd0'
$clamavReference = 'clamav/clamav:1.4.5@sha256:4de20bd9ab45a4b763c5412b769217ef5082572ebc8a63aff1a77943419e5dd8'
$nginxReference = 'nginx:1.30-alpine@sha256:97d490c12ba55b4946b01546d1c3ed324e8d41ab1c9fcb2a616aa470620e5b46'
$keycloakBaseReference = 'quay.io/keycloak/keycloak:26.7.2@sha256:6efbadc00f0ed0237610becf11f4101b9c3ad8edf08a5b70c97aa4154ed436ec'

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

function Invoke-GoTextCapture {
    param(
        [Parameter(Mandatory)][string[]]$Arguments,
        [Parameter(Mandatory)][string]$OutputPath,
        [Parameter(Mandatory)][string]$ErrorLogPath,
        [Parameter(Mandatory)][string]$Description
    )
    Write-Host "==> $Description"
    $text = (& go @Arguments 2> $ErrorLogPath | Out-String)
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
        $definitions.Add((New-ImageDefinition -Name 'postgres-runtime' -ArtifactName 'postgres-runtime' -Reference $postgresReference -Kind 'pinned-external' -Policy 'exact-postgres-gosu-exception'))
        $definitions.Add((New-ImageDefinition -Name 'clamav-runtime' -ArtifactName 'clamav-runtime' -Reference $clamavReference -Kind 'pinned-external' -Policy 'zero-findings'))
        $definitions.Add((New-ImageDefinition -Name 'ingest-proxy' -ArtifactName 'nginx-ingest-proxy' -Reference $nginxReference -Kind 'pinned-external' -Policy 'zero-findings'))
        if ($IdPMode -ceq 'keycloak') {
            $definitions.Add((New-ImageDefinition -Name 'keycloak' -ArtifactName 'invoice-keycloak' -Reference "invoice-keycloak:$ImageTag" -Kind 'built-idp' -Policy 'keycloak-26.7.2-exact-vendor-rejection' -Context 'keycloak' -Dockerfile 'deploy/keycloak/Dockerfile' -BuildArguments @('KEYCLOAK_BASE_IMAGE=' + $keycloakBaseReference) -BaseReference $keycloakBaseReference))
        }

        $imageAcquisition = @{}
        foreach ($definition in $definitions) {
            if ($definition.Kind -in @('built', 'built-idp')) {
                # BuildKit's default provenance attestation creates a fresh
                # manifest-list ID on every local build. The release gate emits
                # its own manifest/SBOM evidence, so disable that nondeterministic
                # wrapper and bind the tag directly to the reproducible image.
                $arguments = @('build', '--pull', '--provenance=false', '--file', (Join-Path $projectRoot $definition.Dockerfile), '--tag', $definition.Reference)
                if (-not [string]::IsNullOrWhiteSpace($definition.Target)) { $arguments += @('--target', $definition.Target) }
                foreach ($buildArgument in $definition.BuildArguments) { $arguments += @('--build-arg', $buildArgument) }
                $contextPath = switch ($definition.Context) {
                    'backend' { Join-Path $projectRoot 'backend' }
                    'web' { Join-Path $projectRoot 'web' }
                    'agents' { Join-Path $projectRoot 'agents' }
                    'keycloak' { Join-Path $projectRoot 'deploy\keycloak' }
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
            $imageMetadata[$definition.Name] = $metadata
            Write-Host "    $($definition.Name): $($metadata.Id)"
        }
        if ($imageMetadata['postgres-runtime'].Id -cne $postgresImageID -or
            $imageMetadata['postgres-runtime'].Os -cne 'linux' -or
            $imageMetadata['postgres-runtime'].Architecture -cne 'amd64') {
            throw 'PostgreSQL exception image ID/platform does not match the reviewed linux/amd64 image'
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
                        $policyStatus = 'approved-by-exact-vendor-rejection'
                        $policyReason = 'exact_redhat_openjdk_libpng_false_positive_only'
                        $exception = [ordered]@{
                            scope = 'exact-keycloak-26.7.2-base-derived-image-and-single-finding-only'
                            baseReference = $keycloakBaseReference
                            derivedImageId = $metadata.Id
                            target = "$($definition.Reference) (redhat 9.8)"
                            vulnerabilityId = 'CVE-2026-22020'
                            package = 'java-21-openjdk-headless'
                            installedVersion = '1:21.0.12.0.8-1.2.el9'
                            fixedVersion = ''
                            trivyStatus = 'affected'
                            severity = 'HIGH'
                            disposition = 'vendor_rejected_not_affected'
                            redHatEvidence = 'https://bugzilla.redhat.com/show_bug.cgi?id=2460045#c11'
                            awsEvidence = 'https://explore.alas.aws.amazon.com/CVE-2026-22020.html'
                            rationale = 'Red Hat officially rejected the CVE; AWS records that it affects Oracle proprietary bundled libpng while OpenJDK distributions using system libpng are not affected.'
                            runtimePruningProof = 'proof/keycloak-runtime-pruning.txt'
                        }
                    }
                } catch {
                    $policyStatus = 'failed'
                    $policyReason = 'keycloak_exact_exception_or_pruning_proof_failed: ' + $_.Exception.Message
                }
            } elseif ($definition.Policy -ceq 'exact-postgres-gosu-exception') {
                if ($summary.Total -eq 0) {
                    $policyReason = 'zero_high_or_critical_findings_exception_unused'
                } else {
                    try {
                        Assert-PostgresGosuFindingScope -ImageReference $definition.Reference -ImageId $metadata.Id -Summary $summary -ExpectedReference $postgresReference -ExpectedImageId $postgresImageID | Out-Null
                        $gosuPath = Join-Path $releaseRoot 'proof\postgres-gosu-linux-amd64'
                        $containerName = 'invoice-release-gosu-' + [guid]::NewGuid().ToString('N')
                        try {
                            & docker create --name $containerName $postgresReference | Out-Null
                            if ($LASTEXITCODE -ne 0) { throw 'cannot create exact PostgreSQL proof container' }
                            & docker cp "${containerName}:/usr/local/bin/gosu" $gosuPath | Out-Null
                            if ($LASTEXITCODE -ne 0) { throw 'cannot extract exact gosu binary' }
                        } finally {
                            & docker rm -f $containerName 2>$null | Out-Null
                        }
                        $gosuHash = Get-FileSha256Lower -Path $gosuPath
                        if ($gosuHash -cne $postgresGosuSha256) { throw 'exact PostgreSQL image produced an unexpected gosu binary hash' }
                        $gosuVersionPath = Join-Path $releaseRoot 'proof\postgres-gosu-version.txt'
                        Invoke-DockerTextCapture -Arguments @('run', '--rm', '--entrypoint', '/usr/local/bin/gosu', $postgresReference, '--version') -OutputPath $gosuVersionPath -ErrorLogPath (Join-Path $releaseRoot 'logs\postgres-gosu-version.log') -Description 'Recording exact gosu binary version'
                        $gosuVersionText = (Get-Content -Raw -LiteralPath $gosuVersionPath).Trim()
                        if ($gosuVersionText -cne '1.19 (go1.24.6 on linux/amd64; gc)') { throw 'gosu binary version/platform is outside the reviewed exception' }

                        $govulnVersionPath = Join-Path $releaseRoot 'proof\postgres-gosu.govulncheck-version.txt'
                        $govulnProofPath = Join-Path $releaseRoot 'proof\postgres-gosu.govulncheck.txt'
                        $moduleCache = (& go env GOMODCACHE | Out-String).Trim()
                        if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($moduleCache)) { throw 'cannot resolve Go module cache for pinned govulncheck tool' }
                        $moduleVersionRoot = Join-Path $moduleCache 'cache\download\golang.org\x\vuln\@v'
                        $moduleArtifacts = [ordered]@{
                            'v1.7.0.zip' = 'a14bf913551ac09f00ae0e903c1b358713f71af911d7ddacc3fab8ce5c149a26'
                            'v1.7.0.mod' = '11910998cc62879ae98db88cdeb9c1c1180a50c62a86e76af37b0685a237fb2a'
                            'v1.7.0.info' = 'a26cff1a3b2b4b2b2c2266fb5de651bce912ff61388f4cc54be585a472233037'
                        }
                        $moduleEvidenceLines = [Collections.Generic.List[string]]::new()
                        foreach ($artifactName in $moduleArtifacts.Keys) {
                            $artifactPath = Join-Path $moduleVersionRoot $artifactName
                            $artifactHash = Get-FileSha256Lower -Path $artifactPath
                            if ($artifactHash -cne $moduleArtifacts[$artifactName]) { throw "pinned govulncheck module cache hash mismatch: $artifactName" }
                            $moduleEvidenceLines.Add("$artifactHash  $artifactName")
                        }
                        $zipHash = (Get-Content -Raw -LiteralPath (Join-Path $moduleVersionRoot 'v1.7.0.ziphash')).Trim()
                        if ($zipHash -cne 'h1:4MQBuhmXbz2uepNJrf3v+aaZLGDqw1JluwYboegA1qg=') { throw 'pinned govulncheck Go module sum mismatch' }
                        $moduleEvidenceLines.Add("$zipHash  v1.7.0.ziphash")
                        $moduleEvidencePath = Join-Path $releaseRoot 'proof\govulncheck-module-cache.txt'
                        Write-Utf8NoBom -Path $moduleEvidencePath -Text (($moduleEvidenceLines -join "`n") + "`n")

                        $previousGoProxy = $env:GOPROXY
                        $previousGoSumDB = $env:GOSUMDB
                        try {
                            $localProxy = (Join-Path $moduleCache 'cache\download').Replace('\', '/')
                            $env:GOPROXY = 'file:///' + $localProxy
                            $env:GOSUMDB = 'sum.golang.org'
                            Invoke-GoTextCapture -Arguments @('run', 'golang.org/x/vuln/cmd/govulncheck@v1.7.0', '-version') -OutputPath $govulnVersionPath -ErrorLogPath (Join-Path $releaseRoot 'logs\govulncheck-version.log') -Description 'Recording exact govulncheck version and database from verified local module cache'
                            Invoke-GoTextCapture -Arguments @('run', 'golang.org/x/vuln/cmd/govulncheck@v1.7.0', '-mode=binary', $gosuPath) -OutputPath $govulnProofPath -ErrorLogPath (Join-Path $releaseRoot 'logs\postgres-gosu.govulncheck.log') -Description 'Checking called vulnerabilities in exact gosu binary'
                        } finally {
                            $env:GOPROXY = $previousGoProxy
                            $env:GOSUMDB = $previousGoSumDB
                        }
                        Assert-GovulncheckBinaryProof -ProofText (Get-Content -Raw -LiteralPath $govulnProofPath) -VersionText (Get-Content -Raw -LiteralPath $govulnVersionPath) | Out-Null
                        $policyStatus = 'approved-by-exact-binary-exception'
                        $policyReason = 'exact_digest_gosu_only_zero_called_vulnerabilities'
                        $exception = [ordered]@{
                            scope = 'exact-postgres-gosu-linux-amd64-only'
                            imageReference = $postgresReference
                            imageId = $postgresImageID
                            binaryPath = 'usr/local/bin/gosu'
                            binarySha256 = $gosuHash
                            binaryVersion = $gosuVersionText
                            govulncheckVersion = 'v1.7.0'
                            govulncheckProof = 'proof/postgres-gosu.govulncheck.txt'
                            govulncheckVersionEvidence = 'proof/postgres-gosu.govulncheck-version.txt'
                            govulncheckModuleEvidence = 'proof/govulncheck-module-cache.txt'
                            govulncheckModuleZipSha256 = $moduleArtifacts['v1.7.0.zip']
                        }
                    } catch {
                        $policyStatus = 'failed'
                        $policyReason = 'postgres_exception_proof_failed: ' + $_.Exception.Message
                    }
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
            })
        }

        if ($IdPMode -ceq 'keycloak') {
            $keycloakRuntimeRecords = @($imageRecords | Where-Object { $_.name -ceq 'keycloak' })
            if ($keycloakRuntimeRecords.Count -ne 1) { throw 'Keycloak mode did not produce exactly one image record' }
            $keycloakRuntimeRecord = $keycloakRuntimeRecords[0]
            if ($keycloakRuntimeRecord.policyStatus -in @('approved', 'approved-by-exact-vendor-rejection')) {
                $runtimeSmokePath = Join-Path $releaseRoot 'proof\keycloak-runtime-smoke.txt'
                $runtimeSmokeErrorPath = Join-Path $releaseRoot 'logs\keycloak-runtime-smoke.log'
                Write-Host '==> Running isolated Keycloak/PostgreSQL health and OIDC discovery smoke'
                $runtimeSmokeText = (& pwsh -NoProfile -File (Join-Path $PSScriptRoot 'verify-keycloak-runtime.ps1') -Image ([string]$keycloakRuntimeRecord.reference) 2> $runtimeSmokeErrorPath | Out-String)
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
                    }
                }
                if ([string]$keycloakRuntimeRecord.runtimeSmoke.status -ceq 'passed') {
                    $provisioningPath = Join-Path $releaseRoot 'proof\keycloak-realm-provisioning.txt'
                    $provisioningErrorPath = Join-Path $releaseRoot 'logs\keycloak-realm-provisioning.log'
                    Write-Host '==> Running isolated Keycloak realm/client/LoA2 provisioning contract'
                    $provisioningText = (& pwsh -NoProfile -File (Join-Path $PSScriptRoot 'verify-keycloak-provisioning.ps1') -KeycloakImage ([string]$keycloakRuntimeRecord.reference) -ExpectedKeycloakImageID ([string]$keycloakRuntimeRecord.imageId) 2> $provisioningErrorPath | Out-String)
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
                            assertions = @('realm-policy', 'default-user-role', 'loa1-loa2', 'roles-acr-amr-mappers', 'four-clients', 'no-offline-scope', 'disabled-desktop', 'single-secret-0400', 'fixed-output', 'existing-realm-refusal', 'log-redaction')
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
                        }
                    }
                }
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
                postgresException = 'exact digest plus exact gosu sha256 plus govulncheck v1.7.0 binary proof only'
                keycloakException = 'exact 26.7.2 base plus derived image ID plus exact single Red Hat rejected CVE tuple plus runtime pruning proof only'
            }
            tools = [ordered]@{
                script = 'scripts/release-image-gate.ps1'
                scriptSha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'release-image-gate.ps1')
                librarySha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'release-image-gate-lib.ps1')
                verifierSha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-release-image-artifacts.ps1')
                sourceVerifierSha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify.ps1')
                keycloakRuntimeVerifierSha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-keycloak-runtime.ps1')
                keycloakProvisioningVerifierSha256 = Get-FileSha256Lower -Path (Join-Path $PSScriptRoot 'verify-keycloak-provisioning.ps1')
                keycloakProvisionerSha256 = Get-FileSha256Lower -Path (Join-Path $projectRoot 'deploy\keycloak\provision-solov-realm.sh')
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

The PostgreSQL exception, when present, applies only to the exact pinned
linux/amd64 image, exact gosu SHA-256 and its retained govulncheck v1.7.0
binary proof. It cannot be reused for another image, binary or finding target.
The Keycloak exception, when present, retains the Trivy finding and is bound to
the exact 26.7.2 base digest, current derived image ID, one CVE/package/version
tuple and proof that the admin CLI and unused MSSQL driver are absent.
The same immutable Keycloak image must also pass the disposable realm/client,
LoA2, mapper, output-redaction and one-secret-publication contract.
"@
        Write-Utf8NoBom -Path (Join-Path $releaseRoot 'README.md') -Text ($readme + "`n")

        foreach ($record in $imageRecords) {
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
