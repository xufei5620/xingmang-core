Set-StrictMode -Version Latest

# Pure and network/docker helpers behind scripts/refresh-trivy-cache.ps1.
# Split into their own library file (mirroring scripts/release-image-gate-
# lib.ps1's split from release-image-gate.ps1) so scripts/test-refresh-
# trivy-cache.ps1 can dot-source and exercise the pure pieces directly.

function Assert-RefreshTrivyCachePowerShellRuntime {
    if ($PSVersionTable.PSVersion -lt [version]'7.5.0') {
        throw "scripts/refresh-trivy-cache.ps1 requires PowerShell 7.5 or newer for ConvertFrom-Json -DateKind String; got $($PSVersionTable.PSVersion)"
    }
    return $true
}

# --- pure helpers -----------------------------------------------------------

# Get-RangedDownloadPlan splits a TotalSize-byte resource into up to
# PartCount contiguous, non-overlapping byte ranges (HTTP Range semantics:
# inclusive Start/End). Never returns a zero-length range or more parts than
# TotalSize has bytes, so a small resource degrades gracefully to fewer,
# larger parts instead of empty ones.
function Get-RangedDownloadPlan {
    param(
        [Parameter(Mandatory)][long]$TotalSize,
        [Parameter(Mandatory)][int]$PartCount
    )
    if ($TotalSize -le 0) {
        throw "TotalSize must be positive, got $TotalSize"
    }
    if ($PartCount -lt 1) {
        throw "PartCount must be at least 1, got $PartCount"
    }
    $effectivePartCount = [Math]::Min($PartCount, $TotalSize)
    $plan = [Collections.Generic.List[object]]::new()
    for ($i = 0; $i -lt $effectivePartCount; $i++) {
        $start = [long]([Math]::Floor($TotalSize * $i / $effectivePartCount))
        $endExclusive = [long]([Math]::Floor($TotalSize * ($i + 1) / $effectivePartCount))
        $end = $endExclusive - 1
        $plan.Add([pscustomobject]@{
            Index  = $i
            Start  = $start
            End    = $end
            Length = $end - $start + 1
        })
    }
    return $plan
}

# New-TrivyCacheCurlConfig builds the text of a curl "-K" config file that
# downloads every entry in Plan (from Get-RangedDownloadPlan) as its own
# ranged request against Url, in parallel (the caller invokes curl with
# --parallel --parallel-max), each into its own PartPaths[i] file. Every
# option that must apply to a specific ranged request (auth header, proxy,
# retry policy, the range itself) is repeated inside each "--next"-delimited
# block rather than left to rely on ambiguous carry-over from the top of the
# file, since curl's own docs do not guarantee which per-transfer options
# survive a --next boundary.
function New-TrivyCacheCurlConfig {
    param(
        [Parameter(Mandatory)][object[]]$Plan,
        [Parameter(Mandatory)][string]$Url,
        [Parameter(Mandatory)][string[]]$PartPaths,
        [Parameter(Mandatory)][string]$BearerToken,
        [AllowEmptyString()][string]$ProxyUrl = ''
    )
    if ($Plan.Count -ne $PartPaths.Count) {
        throw "Plan has $($Plan.Count) entries but PartPaths has $($PartPaths.Count)"
    }
    $lines = [Collections.Generic.List[string]]::new()
    for ($i = 0; $i -lt $Plan.Count; $i++) {
        if ($i -gt 0) { $lines.Add('--next') }
        $lines.Add('fail')
        $lines.Add('silent')
        $lines.Add('show-error')
        # OCI registry blob endpoints (this one included) commonly answer a
        # blob GET with a 302 to a separate signed storage URL rather than
        # the content itself; without following it, curl saves the tiny
        # redirect body as if it were the blob (verified directly: every
        # part came back exactly 140 bytes, all identical redirect HTML).
        # Curl does not resend Authorization across a cross-host redirect by
        # default, which is the correct, safe behavior here -- the signed
        # storage URL carries its own auth in the URL and does not need it.
        $lines.Add('location')
        $lines.Add('retry = "5"')
        $lines.Add('retry-all-errors')
        $lines.Add('connect-timeout = "20"')
        if (-not [string]::IsNullOrWhiteSpace($ProxyUrl)) {
            $lines.Add("proxy = `"$ProxyUrl`"")
        }
        $lines.Add("header = `"Authorization: Bearer $BearerToken`"")
        $lines.Add("range = `"$($Plan[$i].Start)-$($Plan[$i].End)`"")
        # Forward slashes, not backslashes: curl's own -K/--config file
        # format applies C-style backslash escape processing inside double-
        # quoted values (\t, \n, \\, ...), which silently mangles a literal
        # Windows path like C:\Users\...\part0.tmp into something that
        # writes no file at all (verified directly: curl exits 0 and
        # produces nothing). Windows file APIs, including curl's own -o,
        # accept forward slashes exactly as well as backslashes, so
        # normalizing here sidesteps the escaping problem entirely rather
        # than trying to double every backslash.
        $lines.Add("output = `"$($PartPaths[$i].Replace('\', '/'))`"")
        $lines.Add("url = `"$Url`"")
    }
    return ($lines -join "`n") + "`n"
}

function Get-Sha256HexOfFile {
    param([Parameter(Mandatory)][string]$Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

# Assert-OciDigestMatches compares a locally computed SHA-256 hex digest
# against an OCI "sha256:<hex>" reference digest, case-insensitively (OCI
# digests are conventionally lower case, but comparing case-insensitively
# costs nothing and avoids a false mismatch on a differently-cased manifest).
function Assert-OciDigestMatches {
    param(
        [Parameter(Mandatory)][string]$ActualHex,
        [Parameter(Mandatory)][string]$ExpectedDigest,
        [Parameter(Mandatory)][string]$Description
    )
    if ($ExpectedDigest -notmatch '^sha256:([0-9a-fA-F]{64})$') {
        throw "$Description has an invalid OCI digest shape: $ExpectedDigest"
    }
    $expectedHex = $Matches[1].ToLowerInvariant()
    if ($ActualHex.ToLowerInvariant() -ne $expectedHex) {
        throw "$Description digest mismatch: downloaded content hashes to sha256:$($ActualHex.ToLowerInvariant()), manifest declares $ExpectedDigest"
    }
    return $true
}

# Get-OciManifestSingleLayer validates the shape this repo's Trivy DB/Java DB
# OCI artifacts actually have (schemaVersion 2, exactly one layer) and
# returns that layer's mediaType/digest/size. Failing closed on any other
# shape (zero or multiple layers, wrong schema version) means a future
# upstream repackaging is a loud, immediate error here rather than a script
# that silently downloads only the first of several layers.
function Get-OciManifestSingleLayer {
    param([Parameter(Mandatory)]$Manifest)

    if ([int]$Manifest.schemaVersion -ne 2) {
        throw "unsupported OCI manifest schemaVersion: $($Manifest.schemaVersion)"
    }
    $layers = @($Manifest.layers)
    if ($layers.Count -ne 1) {
        throw "expected exactly one manifest layer, found $($layers.Count)"
    }
    $layer = $layers[0]
    if ([string]$layer.digest -notmatch '^sha256:[0-9a-fA-F]{64}$') {
        throw "manifest layer has no valid sha256 digest: $($layer.digest)"
    }
    if ([long]$layer.size -le 0) {
        throw "manifest layer has a non-positive size: $($layer.size)"
    }
    return [pscustomobject]@{
        MediaType = [string]$layer.mediaType
        Digest    = [string]$layer.digest
        Size      = [long]$layer.size
    }
}

# Read-TrivyDbMetadataText parses one of Trivy's own db/metadata.json or
# java-db/metadata.json files (identical shape) and requires the two fields
# release-image-gate.ps1's own Assert-TrivyDatabaseFreshness reads from
# `trivy --version` output (UpdatedAt, NextUpdate) to actually be present
# and parseable, so a malformed or unexpectedly-shaped metadata.json fails
# here -- before ever reaching the cache volume -- rather than surfacing as a
# confusing failure deep inside a later real release-image-gate.ps1 run.
function Read-TrivyDbMetadataText {
    param([Parameter(Mandatory)][string]$JsonText)

    $metadata = $JsonText | ConvertFrom-Json -DateKind String
    foreach ($required in @('UpdatedAt', 'NextUpdate')) {
        $property = $metadata.PSObject.Properties[$required]
        if ($null -eq $property -or [string]::IsNullOrWhiteSpace([string]$property.Value)) {
            throw "Trivy db metadata.json is missing required field: $required"
        }
        [void][DateTimeOffset]::Parse([string]$property.Value) # throws if unparseable
    }
    return [pscustomobject]@{
        UpdatedAt  = [string]$metadata.UpdatedAt
        NextUpdate = [string]$metadata.NextUpdate
    }
}

# Test-CandidateMetadataIsAcceptable implements "refuse to replace a cache
# newer than the download": a freshly downloaded database is only allowed to
# replace what is already seeded if it is not older. CurrentUpdatedAt of
# $null means nothing is seeded yet (first run), which always accepts.
function Test-CandidateMetadataIsAcceptable {
    param(
        [Parameter(Mandatory)][string]$CandidateUpdatedAt,
        [AllowNull()][string]$CurrentUpdatedAt
    )
    if ([string]::IsNullOrWhiteSpace($CurrentUpdatedAt)) {
        return $true
    }
    $candidate = [DateTimeOffset]::Parse($CandidateUpdatedAt)
    $current = [DateTimeOffset]::Parse($CurrentUpdatedAt)
    return $candidate -ge $current
}

# --- network helpers ---------------------------------------------------------

function Invoke-TrivyCacheCurl {
    param(
        [Parameter(Mandatory)][string[]]$Arguments,
        [string]$Description = 'curl'
    )
    $output = & curl.exe @Arguments 2>&1
    $joinedOutput = $output -join "`n"
    if ($LASTEXITCODE -ne 0) {
        throw "$Description failed (curl exit $LASTEXITCODE): $joinedOutput"
    }
    return $joinedOutput
}

# Get-OciRegistryToken fetches an anonymous pull-scoped bearer token, the
# same two-step OCI/Docker Registry v2 auth flow `docker pull` itself uses
# for an anonymous public repository.
function Get-OciRegistryToken {
    param(
        [Parameter(Mandatory)][string]$Registry,
        [Parameter(Mandatory)][string]$Repository,
        [AllowEmptyString()][string]$ProxyUrl = ''
    )
    $curlArguments = [Collections.Generic.List[string]]::new()
    $curlArguments.AddRange([string[]]@('--fail', '--silent', '--show-error', '--retry', '5', '--retry-all-errors', '--connect-timeout', '20'))
    if (-not [string]::IsNullOrWhiteSpace($ProxyUrl)) { $curlArguments.AddRange([string[]]@('--proxy', $ProxyUrl)) }
    $tokenUrl = "https://$Registry/v2/token?service=$Registry&scope=repository:${Repository}:pull"
    $curlArguments.Add($tokenUrl)
    $responseText = Invoke-TrivyCacheCurl -Arguments $curlArguments -Description "fetch registry token for $Repository"
    $response = $responseText | ConvertFrom-Json -DateKind String
    if ([string]::IsNullOrWhiteSpace([string]$response.token)) {
        throw "registry token response for $Repository had no token field"
    }
    return [string]$response.token
}

function Get-OciManifest {
    param(
        [Parameter(Mandatory)][string]$Registry,
        [Parameter(Mandatory)][string]$Repository,
        [Parameter(Mandatory)][string]$Tag,
        [Parameter(Mandatory)][string]$BearerToken,
        [AllowEmptyString()][string]$ProxyUrl = ''
    )
    $curlArguments = [Collections.Generic.List[string]]::new()
    $curlArguments.AddRange([string[]]@('--fail', '--silent', '--show-error', '--retry', '5', '--retry-all-errors', '--connect-timeout', '20'))
    if (-not [string]::IsNullOrWhiteSpace($ProxyUrl)) { $curlArguments.AddRange([string[]]@('--proxy', $ProxyUrl)) }
    $curlArguments.AddRange([string[]]@('-H', "Authorization: Bearer $BearerToken"))
    $curlArguments.AddRange([string[]]@('-H', 'Accept: application/vnd.oci.image.manifest.v1+json'))
    $curlArguments.Add("https://$Registry/v2/$Repository/manifests/$Tag")
    $manifestText = Invoke-TrivyCacheCurl -Arguments $curlArguments -Description "fetch manifest for ${Repository}:${Tag}"
    return $manifestText | ConvertFrom-Json -DateKind String
}

# Get-BlobDownloadPlanExcludingCompleteParts skips any part file that already
# exists on disk with exactly its expected length -- the resumability
# contract: a previous run's fully-downloaded parts are never re-fetched,
# only missing or short (interrupted) parts are.
function Get-BlobDownloadPlanExcludingCompleteParts {
    param(
        [Parameter(Mandatory)][object[]]$FullPlan,
        [Parameter(Mandatory)][string[]]$PartPaths
    )
    $pending = [Collections.Generic.List[int]]::new()
    for ($i = 0; $i -lt $FullPlan.Count; $i++) {
        $path = $PartPaths[$i]
        $isComplete = (Test-Path -LiteralPath $path -PathType Leaf) -and
            ((Get-Item -LiteralPath $path).Length -eq $FullPlan[$i].Length)
        if (-not $isComplete) {
            $pending.Add($i)
        }
    }
    return @($pending)
}

# Invoke-TrivyCacheRangedDownload performs the "24-way ranged, resumable,
# parallel curl download": splits Size bytes of Url into PartCount ranges,
# skips any part already fully present in PartsDirectory from a prior
# interrupted attempt, downloads the rest in one parallel curl invocation,
# then concatenates every part (in order) into DestinationPath.
function Invoke-TrivyCacheRangedDownload {
    param(
        [Parameter(Mandatory)][string]$Url,
        [Parameter(Mandatory)][long]$Size,
        [Parameter(Mandatory)][int]$PartCount,
        [Parameter(Mandatory)][string]$BearerToken,
        [AllowEmptyString()][string]$ProxyUrl = '',
        [Parameter(Mandatory)][string]$PartsDirectory,
        [Parameter(Mandatory)][string]$DestinationPath
    )
    New-Item -ItemType Directory -Path $PartsDirectory -Force | Out-Null
    # @(...) at every call site here is deliberate, not decorative: a
    # PowerShell function's return value collapses from an array to a bare
    # scalar when it happens to contain exactly one element (e.g. a single
    # pending part to resume), which then breaks .Count/indexing downstream
    # under Set-StrictMode -- reproduced directly while building this script.
    $plan = @(Get-RangedDownloadPlan -TotalSize $Size -PartCount $PartCount)
    $partPaths = @($plan | ForEach-Object { Join-Path $PartsDirectory ('part{0:d3}of{1:d3}.tmp' -f $_.Index, $plan.Count) })

    $pendingIndexes = @(Get-BlobDownloadPlanExcludingCompleteParts -FullPlan $plan -PartPaths $partPaths)
    if ($pendingIndexes.Count -gt 0) {
        $pendingPlan = @($pendingIndexes | ForEach-Object { $plan[$_] })
        $pendingPartPaths = @($pendingIndexes | ForEach-Object { $partPaths[$_] })
        Write-Host "    downloading $($pendingPlan.Count)/$($plan.Count) part(s) ($([Math]::Round($Size / 1MB, 1)) MiB total, $PartCount-way parallel ranged)"
        $configText = New-TrivyCacheCurlConfig -Plan $pendingPlan -Url $Url -PartPaths $pendingPartPaths -BearerToken $BearerToken -ProxyUrl $ProxyUrl
        $configPath = Join-Path $PartsDirectory 'curl-config.txt'
        [IO.File]::WriteAllText($configPath, $configText, [Text.UTF8Encoding]::new($false))
        $parallelMax = [Math]::Max(1, [Math]::Min($PartCount, $pendingPlan.Count))
        & curl.exe --parallel --parallel-max $parallelMax --parallel-immediate -K $configPath
        if ($LASTEXITCODE -ne 0) {
            throw "parallel ranged download failed (curl exit $LASTEXITCODE); rerun to resume -- completed parts are kept"
        }
    } else {
        Write-Host "    all $($plan.Count) part(s) already downloaded from a previous run; skipping network transfer"
    }

    foreach ($i in 0..($plan.Count - 1)) {
        $actualLength = (Get-Item -LiteralPath $partPaths[$i]).Length
        if ($actualLength -ne $plan[$i].Length) {
            throw "part $i is $actualLength bytes, expected $($plan[$i].Length) -- rerun to resume"
        }
    }

    if (Test-Path -LiteralPath $DestinationPath) { Remove-Item -LiteralPath $DestinationPath -Force }
    $destinationStream = [IO.File]::Create($DestinationPath)
    try {
        foreach ($partPath in $partPaths) {
            $partStream = [IO.File]::OpenRead($partPath)
            try { $partStream.CopyTo($destinationStream) } finally { $partStream.Dispose() }
        }
    } finally {
        $destinationStream.Dispose()
    }
    return $DestinationPath
}

# --- extraction ---------------------------------------------------------------

function Expand-TrivyCacheTarGz {
    param(
        [Parameter(Mandatory)][string]$ArchivePath,
        [Parameter(Mandatory)][string]$DestinationDirectory
    )
    if (Test-Path -LiteralPath $DestinationDirectory) {
        Remove-Item -LiteralPath $DestinationDirectory -Recurse -Force
    }
    New-Item -ItemType Directory -Path $DestinationDirectory -Force | Out-Null
    $tarCommand = Get-Command -Name tar -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -eq $tarCommand) {
        throw 'tar executable not found on PATH (needed to extract the downloaded .tar.gz)'
    }
    & $tarCommand.Source -xzf $ArchivePath -C $DestinationDirectory
    if ($LASTEXITCODE -ne 0) {
        throw "tar extraction of $ArchivePath failed with exit $LASTEXITCODE"
    }
    return $DestinationDirectory
}

# --- docker volume helpers ----------------------------------------------------

function Assert-SafeTrivyCacheSubPath {
    param([Parameter(Mandatory)][string]$SubPath)
    if ($SubPath -notmatch '^[0-9A-Za-z][0-9A-Za-z_-]{0,63}$') {
        throw "unsafe Trivy cache sub-path: $SubPath"
    }
}

function Assert-SafeOciDigestForShell {
    param([Parameter(Mandatory)][string]$Digest)
    if ($Digest -notmatch '^sha256:[0-9a-fA-F]{64}$') {
        throw "refusing to embed a non-digest-shaped value into a container command: $Digest"
    }
}

function New-TrivyCacheVolumeIfMissing {
    param([Parameter(Mandatory)][string]$Volume)
    & docker volume create $Volume | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "docker volume create $Volume failed with exit $LASTEXITCODE"
    }
}

function Test-DockerVolumeExists {
    param([Parameter(Mandatory)][string]$Volume)
    & docker volume inspect $Volume *> $null
    return $LASTEXITCODE -eq 0
}

# Get-TrivyCacheVolumeComponentState reads back what Publish-
# TrivyCacheComponentToVolume last wrote for one component (SubPath "db" or
# "java-db"): the OCI layer digest it was seeded from (a small sidecar file,
# not part of Trivy's own format) and the raw metadata.json text, if
# present. Both are $null on a volume that has never been seeded for this
# component (a brand new volume, or a component this script has not been
# asked to refresh yet).
function Get-TrivyCacheVolumeComponentState {
    param(
        [Parameter(Mandatory)][string]$Volume,
        [Parameter(Mandatory)][string]$SubPath,
        [Parameter(Mandatory)][string]$SeedImage
    )
    Assert-SafeTrivyCacheSubPath -SubPath $SubPath
    $script = "if [ -f /cache/$SubPath/.source-digest ]; then echo DIGEST_BEGIN; cat /cache/$SubPath/.source-digest; echo; echo DIGEST_END; fi; " +
        "if [ -f /cache/$SubPath/metadata.json ]; then echo METADATA_BEGIN; cat /cache/$SubPath/metadata.json; echo; echo METADATA_END; fi; exit 0"
    $dockerArguments = @('run', '--rm', '-v', "${Volume}:/cache:ro", $SeedImage, 'sh', '-c', $script)
    $output = & docker @dockerArguments 2>&1
    if ($LASTEXITCODE -ne 0) {
        $joined = $output -join "`n"
        throw "reading current $SubPath state from volume $Volume failed (docker exit $LASTEXITCODE): $joined"
    }
    $text = $output -join "`n"
    $digest = $null
    if ($text -match '(?s)DIGEST_BEGIN\r?\n(.*?)\r?\nDIGEST_END') { $digest = $Matches[1].Trim() }
    $metadataText = $null
    if ($text -match '(?s)METADATA_BEGIN\r?\n(.*?)\r?\nMETADATA_END') { $metadataText = $Matches[1].Trim() }
    return [pscustomobject]@{ Digest = $digest; MetadataText = $metadataText }
}

# Publish-TrivyCacheComponentToVolume copies FileNames from
# LocalSourceDirectory into the volume's /cache/<SubPath>, tagged with the
# OCI layer Digest they came from (read back by Get-
# TrivyCacheVolumeComponentState on a later run). It builds the replacement
# directory alongside the live one first and only then swaps it into place
# (rename, not an in-place overwrite of individual files), so a scan
# concurrently reading the volume never observes a half-written mix of old
# and new files -- the caller is still expected to hold the shared release-
# gate Trivy lock for the duration, since a rename is not a substitute for
# mutual exclusion against a scan that opens files by path over the whole
# run, only against torn *individual file* reads.
function Publish-TrivyCacheComponentToVolume {
    param(
        [Parameter(Mandatory)][string]$Volume,
        [Parameter(Mandatory)][string]$SubPath,
        [Parameter(Mandatory)][string]$LocalSourceDirectory,
        [Parameter(Mandatory)][string]$Digest,
        [Parameter(Mandatory)][string]$SeedImage,
        [Parameter(Mandatory)][string[]]$FileNames
    )
    Assert-SafeTrivyCacheSubPath -SubPath $SubPath
    Assert-SafeOciDigestForShell -Digest $Digest
    foreach ($fileName in $FileNames) {
        if ($fileName -notmatch '^[0-9A-Za-z][0-9A-Za-z_.-]{0,127}$') {
            throw "unsafe file name for container copy: $fileName"
        }
        if (-not (Test-Path -LiteralPath (Join-Path $LocalSourceDirectory $fileName) -PathType Leaf)) {
            throw "expected extracted file not found: $(Join-Path $LocalSourceDirectory $fileName)"
        }
    }
    $copySteps = ($FileNames | ForEach-Object { "cp /source/$_ /cache/$SubPath.new/$_" }) -join ' && '
    $script = "set -e; rm -rf /cache/$SubPath.new; mkdir -p /cache/$SubPath.new; $copySteps && " +
        "printf '%s' '$Digest' > /cache/$SubPath.new/.source-digest && " +
        "rm -rf /cache/$SubPath.old && " +
        "if [ -d /cache/$SubPath ]; then mv /cache/$SubPath /cache/$SubPath.old; fi && " +
        "mv /cache/$SubPath.new /cache/$SubPath && " +
        "rm -rf /cache/$SubPath.old"
    $dockerArguments = @('run', '--rm', '-v', "${Volume}:/cache", '-v', "${LocalSourceDirectory}:/source:ro", $SeedImage, 'sh', '-c', $script)
    $output = & docker @dockerArguments 2>&1
    if ($LASTEXITCODE -ne 0) {
        $joined = $output -join "`n"
        throw "seeding $SubPath into volume $Volume failed (docker exit $LASTEXITCODE): $joined"
    }
}

# --- shared release-gate lock -------------------------------------------------

# Get-TrivyReleaseGateLockPath must equal release-image-gate.ps1's own
# $lockPath exactly (Join-Path (Split-Path -Parent $releaseRoot)
# '.trivy-0.74.release-gate.lock', where $releaseRoot is always a child of
# <projectRoot>\release), so this script and a concurrently running release
# image gate take the same lock and cannot race the same shared cache volume.
function Get-TrivyReleaseGateLockPath {
    param([Parameter(Mandatory)][string]$ProjectRoot)
    return Join-Path $ProjectRoot 'release\.trivy-0.74.release-gate.lock'
}

function Enter-TrivyReleaseGateLock {
    param([Parameter(Mandatory)][string]$LockPath)
    New-Item -ItemType Directory -Path (Split-Path -Parent $LockPath) -Force | Out-Null
    try {
        return [IO.File]::Open($LockPath, [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
    } catch {
        throw 'another process (a release image gate run, or a concurrent Trivy cache refresh) is already using the shared Trivy cache lock'
    }
}
