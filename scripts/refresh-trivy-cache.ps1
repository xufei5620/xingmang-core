[CmdletBinding(SupportsShouldProcess)]
param(
    [ValidatePattern('^[0-9A-Za-z][0-9A-Za-z_.-]{0,127}$')]
    [string]$TrivyCacheVolume = 'invoice-release-gate-trivy-0-74-0',

    [ValidateNotNullOrEmpty()]
    [string]$TrivyDbRegistry = 'mirror.gcr.io',
    [ValidateNotNullOrEmpty()]
    [string]$TrivyDbRepository = 'aquasec/trivy-db',
    [ValidateNotNullOrEmpty()]
    [string]$TrivyDbTag = '2',

    [ValidateNotNullOrEmpty()]
    [string]$TrivyJavaDbRegistry = 'mirror.gcr.io',
    [ValidateNotNullOrEmpty()]
    [string]$TrivyJavaDbRepository = 'aquasec/trivy-java-db',
    [ValidateNotNullOrEmpty()]
    [string]$TrivyJavaDbTag = '1',

    # The Java DB is ~900 MiB, versus ~110 MiB for the vulnerability DB --
    # skip it for a faster refresh when a caller knows the release gate run
    # it is preparing for will not scan anything needing it, or for local
    # testing.
    [switch]$SkipJavaDb,

    [AllowEmptyString()]
    [string]$ProxyUrl = $(
        if (-not [string]::IsNullOrWhiteSpace($env:HTTPS_PROXY)) { $env:HTTPS_PROXY }
        elseif (-not [string]::IsNullOrWhiteSpace($env:HTTP_PROXY)) { $env:HTTP_PROXY }
        else { 'http://127.0.0.1:10808' }
    ),

    [ValidateRange(1, 64)]
    [int]$ParallelDownloads = 24,

    # Reuses the exact, already-reviewed PostgreSQL base image pin
    # scripts/release-image-gate.ps1 itself builds from ($postgresBaseReference)
    # as the "throwaway container" that copies extracted files into the cache
    # volume, rather than introducing and having to track a second pinned
    # utility image just for this.
    [ValidatePattern('^[0-9A-Za-z][0-9A-Za-z._/-]*@sha256:[0-9a-f]{64}$')]
    [string]$SeedImage = 'postgres:18.6-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2',

    # Must match scripts/release-image-gate.ps1's own $trivyImage pin exactly
    # -- the mandatory post-seed self-check below runs this same Trivy
    # build against a staging volume, so it needs to be the same version
    # the real release gate will actually use.
    [ValidatePattern('^[0-9A-Za-z][0-9A-Za-z._/-]*@sha256:[0-9a-f]{64}$')]
    [string]$TrivyImage = 'ghcr.io/aquasecurity/trivy:0.74.0@sha256:62b1e65e8869bc4b4c6aa4fa2b21595256c7c2f6018a9d9ad61caf87187c1969',

    # A small image already present in the local Docker image store, scanned
    # by the mandatory post-seed self-check below. Defaults to -SeedImage
    # itself (already required, already pinned by digest, already present)
    # rather than introduce a second pinned-image dependency just for this.
    [ValidatePattern('^[0-9A-Za-z][0-9A-Za-z._/-]*@sha256:[0-9a-f]{64}$')]
    [string]$SelfCheckImageReference = $SeedImage,

    [AllowEmptyString()]
    [string]$WorkDirectory = '',

    # Overrides "refuse to replace a cache newer than the download" -- only
    # for the rare case an operator has deliberately confirmed the older
    # download is what they actually want seeded.
    [switch]$Force
)

<#
.SYNOPSIS
Seeds or refreshes the release-gate Trivy cache volume the way today's
manual recipe does: a 24-way ranged, resumable, parallel curl download of
the trivy-db and trivy-java-db OCI artifacts, an OCI digest check before any
of it is trusted, and a throwaway pinned-PostgreSQL-Alpine container to copy
the verified files into the shared Docker volume.

.DESCRIPTION
scripts/release-image-gate.ps1 updates its own Trivy databases by running
`trivy image --download-db-only`/`--download-java-db-only` inside its own
container, once, synchronously, as part of every release gate run. That
download can be slow or fail outright through this machine's local proxy,
and repeating it on every gate run is wasteful when the upstream database
usually has not changed since the last run. This script instead refreshes
the same shared cache volume (`-TrivyCacheVolume`, matching release-image-
gate.ps1's own default and parameter) directly against the OCI registry,
on its own schedule (e.g. once daily via Task Scheduler or cron), so a
release gate run normally finds an already-fresh cache and does not need to
download anything itself.

Idempotent and safe to run daily: each component (trivy-db, trivy-java-db)
records the exact OCI layer digest it was seeded from in the volume; if the
upstream digest has not changed since last time, this script does no
network transfer and no reseed at all. When the digest *has* changed, the
newly downloaded database's own UpdatedAt is compared against whatever is
currently seeded, and this script refuses to replace a newer cache with an
older download (pass -Force to override deliberately).

Before anything reaches the live cache volume, each refreshed component's
metadata.json is corrected to carry a real DownloadedAt (Trivy's own
downloader stamps this after a successful download; the upstream OCI
artifact ships it as Go's zero time value, which is otherwise
indistinguishable from "never really downloaded" to Trivy's own freshness
check -- see refresh-trivy-cache-lib.ps1's Set-TrivyDbMetadataDownloadedAt
for the full story), and the complete would-be new cache state (this run's
refreshed component(s) layered onto a clone of whatever is currently live)
is built in a disposable staging volume and proven to actually work --
both a real offline vulnerability scan and Trivy's own `--download-*-only`
freshness check must succeed against it -- before any live component is
replaced. A failed self-check leaves the live volume completely untouched.

Takes the exact same shared lock release-image-gate.ps1 does
(release\.trivy-0.74.release-gate.lock) for the whole run, so this script
and a concurrently running release gate can never race the same volume;
like release-image-gate.ps1, it fails fast rather than waiting if that lock
is already held.

.EXAMPLE
pwsh -NoProfile -File scripts\refresh-trivy-cache.ps1
# Refreshes both databases if the upstream digest changed, prints each
# component's UpdatedAt/NextUpdate, and exits 0. Safe to schedule daily.

.EXAMPLE
pwsh -NoProfile -File scripts\refresh-trivy-cache.ps1 -WhatIf
# Reports what would be downloaded/seeded without changing anything.
#>

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$projectRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'refresh-trivy-cache-lib.ps1')
Assert-RefreshTrivyCachePowerShellRuntime | Out-Null

if ([string]::IsNullOrWhiteSpace($WorkDirectory)) {
    $WorkDirectory = Join-Path $projectRoot 'logs\trivy-cache-refresh'
} elseif (-not [IO.Path]::IsPathFullyQualified($WorkDirectory)) {
    $WorkDirectory = Join-Path $projectRoot $WorkDirectory
}
New-Item -ItemType Directory -Path $WorkDirectory -Force | Out-Null

if ($null -eq (Get-Command docker -CommandType Application -ErrorAction SilentlyContinue)) {
    throw 'docker executable not found on PATH'
}
if ($null -eq (Get-Command curl.exe -CommandType Application -ErrorAction SilentlyContinue)) {
    throw 'curl.exe executable not found on PATH'
}
& docker info *> $null
if ($LASTEXITCODE -ne 0) {
    throw 'docker is not available (daemon not running, or this user cannot reach it)'
}

$lockPath = Get-TrivyReleaseGateLockPath -ProjectRoot $projectRoot
Write-Host "Acquiring shared Trivy cache lock: $lockPath"
$lock = Enter-TrivyReleaseGateLock -LockPath $lockPath
try {
    $volumeExists = Test-DockerVolumeExists -Volume $TrivyCacheVolume
    if (-not $volumeExists) {
        if ($PSCmdlet.ShouldProcess($TrivyCacheVolume, 'Create Trivy cache volume')) {
            New-TrivyCacheVolumeIfMissing -Volume $TrivyCacheVolume
            $volumeExists = $true
        }
    }

    $components = [Collections.Generic.List[object]]::new()
    $components.Add([pscustomobject]@{
        Name              = 'trivy-db'
        SubPath           = 'db'
        Registry          = $TrivyDbRegistry
        Repository        = $TrivyDbRepository
        Tag               = $TrivyDbTag
        FileNames         = @('trivy.db', 'metadata.json')
        ExpectedMediaType = 'application/vnd.aquasec.trivy.db.layer.v1.tar+gzip'
    })
    if (-not $SkipJavaDb) {
        $components.Add([pscustomobject]@{
            Name              = 'trivy-java-db'
            SubPath           = 'java-db'
            Registry          = $TrivyJavaDbRegistry
            Repository        = $TrivyJavaDbRepository
            Tag               = $TrivyJavaDbTag
            FileNames         = @('trivy-java.db', 'metadata.json')
            ExpectedMediaType = 'application/vnd.aquasec.trivy.javadb.layer.v1.tar+gzip'
        })
    }

    $summaries = [Collections.Generic.List[object]]::new()
    $pendingComponents = [Collections.Generic.List[object]]::new()
    foreach ($component in $components) {
        Write-Host "==> $($component.Name) ($($component.Registry)/$($component.Repository):$($component.Tag))"
        $componentWorkDirectory = Join-Path $WorkDirectory $component.Name
        New-Item -ItemType Directory -Path $componentWorkDirectory -Force | Out-Null

        $token = Get-OciRegistryToken -Registry $component.Registry -Repository $component.Repository -ProxyUrl $ProxyUrl
        $manifest = Get-OciManifest -Registry $component.Registry -Repository $component.Repository -Tag $component.Tag -BearerToken $token -ProxyUrl $ProxyUrl
        $layer = Get-OciManifestSingleLayer -Manifest $manifest
        if ($layer.MediaType -cne $component.ExpectedMediaType) {
            throw "$($component.Name): unexpected layer mediaType $($layer.MediaType), expected $($component.ExpectedMediaType)"
        }
        Write-Host "    upstream layer digest: $($layer.Digest) ($([Math]::Round($layer.Size / 1MB, 1)) MiB)"

        $currentState = if ($volumeExists) {
            Get-TrivyCacheVolumeComponentState -Volume $TrivyCacheVolume -SubPath $component.SubPath -SeedImage $SeedImage
        } else {
            [pscustomobject]@{ Digest = $null; MetadataText = $null }
        }

        if ($currentState.Digest -ceq $layer.Digest) {
            Write-Host '    already up to date (seeded digest matches upstream); skipping download and reseed'
            $metadata = Read-TrivyDbMetadataText -JsonText $currentState.MetadataText
            $summaries.Add([pscustomobject]@{ Name = $component.Name; UpdatedAt = $metadata.UpdatedAt; NextUpdate = $metadata.NextUpdate; Action = 'unchanged' })
            continue
        }

        $blobUrl = "https://$($component.Registry)/v2/$($component.Repository)/blobs/$($layer.Digest)"
        $partsDirectory = Join-Path $componentWorkDirectory 'parts'
        $blobPath = Join-Path $componentWorkDirectory 'blob.tar.gz'
        $blobAlreadyVerified = $false
        if ((Test-Path -LiteralPath $blobPath -PathType Leaf) -and ((Get-Item -LiteralPath $blobPath).Length -eq $layer.Size)) {
            $existingHex = Get-Sha256HexOfFile -Path $blobPath
            $blobAlreadyVerified = ("sha256:$existingHex" -ceq $layer.Digest)
        }
        if ($blobAlreadyVerified) {
            Write-Host "    reusing a previously downloaded, digest-verified blob: $blobPath"
        } else {
            Invoke-TrivyCacheRangedDownload -Url $blobUrl -Size $layer.Size -PartCount $ParallelDownloads `
                -BearerToken $token -ProxyUrl $ProxyUrl -PartsDirectory $partsDirectory -DestinationPath $blobPath | Out-Null
            $actualHex = Get-Sha256HexOfFile -Path $blobPath
            Assert-OciDigestMatches -ActualHex $actualHex -ExpectedDigest $layer.Digest -Description "$($component.Name) blob" | Out-Null
            Write-Host "    digest verified: $($layer.Digest)"
            Remove-Item -LiteralPath $partsDirectory -Recurse -Force -ErrorAction SilentlyContinue
        }

        $extractDirectory = Join-Path $componentWorkDirectory 'extracted'
        Expand-TrivyCacheTarGz -ArchivePath $blobPath -DestinationDirectory $extractDirectory | Out-Null
        foreach ($fileName in $component.FileNames) {
            if (-not (Test-Path -LiteralPath (Join-Path $extractDirectory $fileName) -PathType Leaf)) {
                throw "$($component.Name): extracted archive is missing expected file $fileName"
            }
        }
        $metadataPath = Join-Path $extractDirectory 'metadata.json'
        $candidateMetadataText = Get-Content -Raw -LiteralPath $metadataPath
        $candidateMetadata = Read-TrivyDbMetadataText -JsonText $candidateMetadataText

        $currentMetadata = $null
        if (-not [string]::IsNullOrWhiteSpace($currentState.MetadataText)) {
            $currentMetadata = Read-TrivyDbMetadataText -JsonText $currentState.MetadataText
        }
        $currentUpdatedAt = if ($currentMetadata) { $currentMetadata.UpdatedAt } else { $null }
        $acceptable = Test-CandidateMetadataIsAcceptable -CandidateUpdatedAt $candidateMetadata.UpdatedAt -CurrentUpdatedAt $currentUpdatedAt
        if (-not $acceptable) {
            if (-not $Force) {
                throw "$($component.Name): refusing to replace a currently seeded database newer than this download (current UpdatedAt=$currentUpdatedAt, download UpdatedAt=$($candidateMetadata.UpdatedAt)); rerun with -Force to override deliberately"
            }
            Write-Warning "$($component.Name): -Force overriding the newer-cache-than-download guard (current UpdatedAt=$currentUpdatedAt, download UpdatedAt=$($candidateMetadata.UpdatedAt))"
        }

        # Stamp a real DownloadedAt exactly as Trivy's own downloader does,
        # before this content is ever seeded anywhere (staging included) --
        # see Set-TrivyDbMetadataDownloadedAt's own doc comment for why the
        # raw upstream file (DownloadedAt still Go's zero time value) is not
        # safe to seed as-is.
        $downloadedAtMetadataText = Set-TrivyDbMetadataDownloadedAt -JsonText $candidateMetadataText -DownloadedAt ([DateTimeOffset]::UtcNow)
        [IO.File]::WriteAllText($metadataPath, $downloadedAtMetadataText, [Text.UTF8Encoding]::new($false))

        $pendingComponents.Add([pscustomobject]@{
            Component        = $component
            Digest           = $layer.Digest
            ExtractDirectory = $extractDirectory
            UpdatedAt        = $candidateMetadata.UpdatedAt
            NextUpdate       = $candidateMetadata.NextUpdate
        })
    }

    if ($pendingComponents.Count -gt 0) {
        $pendingNames = ($pendingComponents | ForEach-Object { $_.Component.Name }) -join ', '
        if ($PSCmdlet.ShouldProcess($TrivyCacheVolume, "Stage, self-check, then seed: $pendingNames")) {
            $stagingVolume = "$TrivyCacheVolume-staging-$([Guid]::NewGuid().ToString('N').Substring(0, 8))"
            Write-Host "==> Building staging volume $stagingVolume (live clone + this run's refreshed component(s)) for the mandatory post-seed self-check"
            try {
                New-TrivyCacheVolumeIfMissing -Volume $stagingVolume
                Copy-TrivyCacheVolumeToVolume -SourceVolume $TrivyCacheVolume -DestinationVolume $stagingVolume -SeedImage $SeedImage

                foreach ($pending in $pendingComponents) {
                    Publish-TrivyCacheComponentToVolume -Volume $stagingVolume -SubPath $pending.Component.SubPath `
                        -LocalSourceDirectory $pending.ExtractDirectory -Digest $pending.Digest -SeedImage $SeedImage -FileNames $pending.Component.FileNames
                }

                Write-Host "    self-check 1/2: offline vulnerability scan of $SelfCheckImageReference against $stagingVolume"
                Invoke-TrivyCacheOfflineScanSelfCheck -Volume $stagingVolume -TrivyImage $TrivyImage -ScanImageReference $SelfCheckImageReference | Out-Null
                Write-Host '    self-check 1/2 passed'

                $stagedJavaDbState = Get-TrivyCacheVolumeComponentState -Volume $stagingVolume -SubPath 'java-db' -SeedImage $SeedImage
                $includeJavaDbInFreshnessCheck = -not [string]::IsNullOrWhiteSpace($stagedJavaDbState.Digest)
                Write-Host "    self-check 2/2: Trivy's own --download-db-only$(if ($includeJavaDbInFreshnessCheck) { '/--download-java-db-only' }) freshness check against $stagingVolume"
                Invoke-TrivyCacheFreshnessSelfCheck -Volume $stagingVolume -TrivyImage $TrivyImage -IncludeJavaDb $includeJavaDbInFreshnessCheck -ProxyUrl $ProxyUrl | Out-Null
                Write-Host '    self-check 2/2 passed'

                foreach ($pending in $pendingComponents) {
                    Publish-TrivyCacheComponentToVolume -Volume $TrivyCacheVolume -SubPath $pending.Component.SubPath `
                        -LocalSourceDirectory $pending.ExtractDirectory -Digest $pending.Digest -SeedImage $SeedImage -FileNames $pending.Component.FileNames
                    Write-Host "    seeded $($pending.Component.Name) into $TrivyCacheVolume/$($pending.Component.SubPath)"
                    $summaries.Add([pscustomobject]@{ Name = $pending.Component.Name; UpdatedAt = $pending.UpdatedAt; NextUpdate = $pending.NextUpdate; Action = 'refreshed' })
                }
            } finally {
                if (Test-DockerVolumeExists -Volume $stagingVolume) {
                    & docker volume rm --force $stagingVolume *> $null
                }
            }
        } else {
            foreach ($pending in $pendingComponents) {
                $summaries.Add([pscustomobject]@{ Name = $pending.Component.Name; UpdatedAt = $pending.UpdatedAt; NextUpdate = $pending.NextUpdate; Action = 'would-refresh (-WhatIf)' })
            }
        }
    }

    Write-Host ''
    Write-Host 'Trivy cache refresh summary:'
    foreach ($summary in $summaries) {
        Write-Host "  $($summary.Name): $($summary.Action)"
        Write-Host "    UpdatedAt:  $($summary.UpdatedAt)"
        Write-Host "    NextUpdate: $($summary.NextUpdate)"
    }
} finally {
    $lock.Dispose()
}

$global:LASTEXITCODE = 0
