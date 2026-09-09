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

# Set-TrivyDbMetadataDownloadedAt stamps a real DownloadedAt onto a trivy-db/
# trivy-java-db metadata.json exactly the way Trivy's own downloader does
# (github.com/aquasecurity/trivy/pkg/db.(*Client).updateDownloadedAt,
# confirmed via `strings` on the pinned 0.74.0 binary) after it finishes
# downloading and extracting a database -- a step this script did not
# reproduce before, and the root cause of the incident this fix responds to.
#
# The upstream OCI-published metadata.json (both trivy-db and trivy-java-db)
# ships DownloadedAt as Go's zero time.Time value, "0001-01-01T00:00:00Z"
# (confirmed directly: the raw file extracted from the real trivy-db:2 OCI
# layer at the time of this fix reads exactly `{"Version":2,"NextUpdate":
# "...","UpdatedAt":"...","DownloadedAt":"0001-01-01T00:00:00Z"}`) -- that
# field only has meaning to whichever client actually performs a real
# download, so upstream cannot pre-populate it. Left uncorrected, a cache
# this script seeds from that raw file is still fully valid and scannable
# (`--skip-db-update --skip-java-db-update --offline-scan` against it
# succeeds normally -- confirmed directly), but Trivy's own freshness check
# treats the zero DownloadedAt as a sign the local database "may be
# corrupted": running the exact command scripts/release-image-gate.ps1 runs
# every release (`trivy image --download-db-only`, no skip flags) against
# such a cache prints `Trivy DB may be corrupted and will be re-downloaded`
# and unconditionally redownloads over the network, even though nothing is
# actually wrong with the content -- reproduced directly, verbatim log line
# included, against a cache this script seeded. That unwanted redownload,
# inside the gate's own container with no proxy configured there, is what
# hung until timeout and lost release RC72's first attempt.
#
# Stamping a real DownloadedAt makes that same `--download-db-only` command
# a silent, sub-second no-op instead (confirmed directly: 0.72s, zero log
# output, exit 0) -- the "gate normally finds an already-fresh cache and
# does not need to download anything" contract this script's own doc
# comment already promises.
function Set-TrivyDbMetadataDownloadedAt {
    param(
        [Parameter(Mandatory)][string]$JsonText,
        [Parameter(Mandatory)][DateTimeOffset]$DownloadedAt
    )
    $metadata = $JsonText | ConvertFrom-Json -DateKind String
    # Go's time.Time JSON marshaling emits RFC 3339 with a fractional-second
    # component (e.g. "2026-09-02T18:38:21.557688944Z", confirmed directly
    # against Trivy's own downloader's output) -- .NET's round-trip "o"
    # format on a UTC DateTime produces the same shape and Trivy's JSON
    # decoder (encoding/json into a time.Time) parses any valid RFC 3339
    # timestamp regardless of fractional-second digit count, so exact
    # trailing-digit equivalence is not required, only RFC 3339 validity.
    $metadata | Add-Member -MemberType NoteProperty -Name DownloadedAt -Value $DownloadedAt.UtcDateTime.ToString('o') -Force
    return ($metadata | ConvertTo-Json -Compress)
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
    # GNU tar (Git for Windows' own tar.exe, which resolves ahead of
    # Windows' bundled bsdtar on PATH in a typical dev shell here) treats a
    # bare single-letter drive prefix like "C:" or "K:" in an archive or
    # destination path as a remote "host:file" reference and tries to rsh
    # to a host literally named "C" -- reproduced directly: `tar (child):
    # Cannot connect to C: resolve failed`, `gzip: stdin: unexpected end of
    # file`. Windows' own bsdtar (libarchive-based) has no such remote-
    # archive feature, needs no special-casing, and errors out immediately
    # if given the GNU-only --force-local flag ("Option --force-local is
    # not supported") -- so detect which one actually resolved via its own
    # --version banner rather than assuming either flavor.
    $tarVersionText = (& $tarCommand.Source --version 2>&1 | Out-String)
    $tarArguments = [Collections.Generic.List[string]]::new()
    if ($tarVersionText -match '(?i)GNU tar') {
        $tarArguments.Add('--force-local')
    }
    # Forward slashes, not backslashes: same rationale as New-
    # TrivyCacheCurlConfig's own comment -- GNU tar's argument handling
    # mangles a literal Windows backslash path (verified directly: a
    # `-C "C:\Users\...\out"` destination came back re-escaped as
    # "C\:\\Users\...", "Cannot open: No such file or directory"). Forward
    # slashes are accepted identically by both tar flavors and sidestep it.
    $tarArguments.AddRange([string[]]@('-xzf', $ArchivePath.Replace('\', '/'), '-C', $DestinationDirectory.Replace('\', '/')))
    & $tarCommand.Source @tarArguments
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

# --- staging + mandatory post-seed self-check ---------------------------------
#
# scripts/refresh-trivy-cache.ps1 never writes a refreshed component
# straight into the live cache volume any more: it first builds the would-
# be new state in a disposable staging volume (this run's refreshed
# component(s) layered onto a clone of whatever is currently live, so the
# self-check below sees exactly the cache a real release gate run would),
# proves that staged state actually works, and only then replaces the
# corresponding live component(s) -- so a bad seed (this fix's own root
# cause included) is caught here, against a throwaway volume, instead of
# reaching the shared cache the release gate depends on.

# Copy-TrivyCacheVolumeToVolume clones every file currently in SourceVolume
# into DestinationVolume (first emptied, dotfiles included) via a throwaway
# SeedImage container, then ensures a "tmp" directory exists at the
# destination's root. That last part matters even though this script's own
# seeding never uses it: the self-checks below run Trivy's real `--download-
# *-only` commands against the staged volume, and Trivy 0.74 downloads into
# $TMPDIR (defaulting to the container's own /tmp) before moving the result
# into --cache-dir -- when --cache-dir is a mounted volume, that move
# crosses filesystems and can silently fail, leaving no db/java-db directory
# behind while still logging a success message. Pointing TMPDIR at a
# directory already on the same volume avoids that entirely (found and
# fixed independently while reseeding the real cache during this same
# investigation).
function Copy-TrivyCacheVolumeToVolume {
    param(
        [Parameter(Mandatory)][string]$SourceVolume,
        [Parameter(Mandatory)][string]$DestinationVolume,
        [Parameter(Mandatory)][string]$SeedImage
    )
    $script = 'set -e; find /dest -mindepth 1 -delete; ' +
        'if [ -n "$(ls -A /source 2>/dev/null)" ]; then cp -a /source/. /dest/.; fi; ' +
        'mkdir -p /dest/tmp'
    $dockerArguments = @('run', '--rm', '-v', "${SourceVolume}:/source:ro", '-v', "${DestinationVolume}:/dest", $SeedImage, 'sh', '-c', $script)
    $output = & docker @dockerArguments 2>&1
    if ($LASTEXITCODE -ne 0) {
        $joined = $output -join "`n"
        throw "cloning volume $SourceVolume into $DestinationVolume failed (docker exit $LASTEXITCODE): $joined"
    }
}

# Invoke-TrivyCacheOfflineScanSelfCheck is the mandatory post-seed check:
# proves the staged cache is one Trivy can actually scan with, not merely
# one this script believes it wrote correctly. Mirrors the exact flags
# scripts/release-image-gate.ps1 uses for its real image scans (--skip-db-
# update --skip-java-db-update --no-progress), plus --offline-scan so
# nothing here can reach the network, against ScanImageReference (a small
# image already present in the local Docker image store -- this needs -v
# /var/run/docker.sock:/var/run/docker.sock for Trivy to read it, exactly
# like release-image-gate.ps1's own Get-TrivyArguments). Throws with
# Trivy's full output on any non-zero exit.
function Invoke-TrivyCacheOfflineScanSelfCheck {
    param(
        [Parameter(Mandatory)][string]$Volume,
        [Parameter(Mandatory)][string]$TrivyImage,
        [Parameter(Mandatory)][string]$ScanImageReference
    )
    $dockerArguments = @(
        'run', '--rm',
        '-v', '/var/run/docker.sock:/var/run/docker.sock',
        '-v', "${Volume}:/root/.cache/trivy",
        $TrivyImage,
        'image', '--skip-db-update', '--skip-java-db-update', '--offline-scan', '--no-progress', $ScanImageReference
    )
    $output = & docker @dockerArguments 2>&1
    $joined = $output -join "`n"
    if ($LASTEXITCODE -ne 0) {
        throw "post-seed self-check (offline scan of $ScanImageReference) failed (trivy exit $LASTEXITCODE) -- refusing to replace the live Trivy cache; output:`n$joined"
    }
    return $joined
}

# Invoke-TrivyCacheFreshnessSelfCheck goes one check further than the
# offline-scan self-check above, which this fix's own investigation proved
# does NOT by itself catch a regressed/missing DownloadedAt stamp: a cache
# whose DownloadedAt is still Go's zero value scans successfully under
# --skip-db-update (confirmed directly against a pre-fix seeded cache), so
# that check alone would not have caught the actual defect behind this
# incident. This one runs the literal command release-image-gate.ps1 runs
# every release (`image --download-db-only` / `--download-java-db-only`,
# no skip flags) against the staged volume and fails if Trivy's own
# freshness check ever decides the cache "may be corrupted" -- the exact
# warning this investigation reproduced verbatim against the pre-fix
# seeded cache, immediately before it unconditionally redownloads over the
# network (which, inside the gate's own container with no proxy
# configured there, is what actually hung until timeout and lost release
# RC72's first attempt).
#
# Bounded by a short TimeoutSeconds: when DownloadedAt is stamped
# correctly this is a silent, sub-second no-op (confirmed directly: 0.72s,
# zero log output, exit 0), so there is no legitimate reason for it to
# need long enough to complete an actual fresh download; a real
# corruption should be caught by the warning text or the timeout,
# whichever comes first, rather than let this self-check hang on the same
# flaky-proxy network path that caused the original incident. ProxyUrl is
# passed through only so a genuine regression fails fast on the warning
# text rather than stalling for lack of network access; the no-op path
# never touches the network at all.
function Invoke-TrivyCacheFreshnessSelfCheck {
    param(
        [Parameter(Mandatory)][string]$Volume,
        [Parameter(Mandatory)][string]$TrivyImage,
        [Parameter(Mandatory)][bool]$IncludeJavaDb,
        [AllowEmptyString()][string]$ProxyUrl = '',
        [int]$TimeoutSeconds = 45
    )
    # release-image-gate.ps1 runs these as two separate `trivy image`
    # invocations, never combined -- reproduced directly why: Trivy 0.74
    # itself rejects the combination outright ("flag error: unable to
    # convert flags to options: --download-db-only and --download-java-db-
    # only options can not be specified both"), found only by actually
    # running this self-check end to end against a real db+java-db volume.
    $downloadFlagsPerCall = [Collections.Generic.List[string]]::new()
    $downloadFlagsPerCall.Add('--download-db-only')
    if ($IncludeJavaDb) { $downloadFlagsPerCall.Add('--download-java-db-only') }

    $allOutput = [Collections.Generic.List[string]]::new()
    foreach ($downloadFlag in $downloadFlagsPerCall) {
        $dockerArguments = [Collections.Generic.List[string]]::new()
        $dockerArguments.AddRange([string[]]@('run', '--rm'))
        if (-not [string]::IsNullOrWhiteSpace($ProxyUrl)) {
            $dockerArguments.AddRange([string[]]@('-e', "HTTPS_PROXY=$ProxyUrl", '-e', "HTTP_PROXY=$ProxyUrl"))
        }
        $dockerArguments.AddRange([string[]]@('-e', 'TMPDIR=/root/.cache/trivy/tmp'))
        $dockerArguments.AddRange([string[]]@('-v', "${Volume}:/root/.cache/trivy", $TrivyImage))
        $dockerArguments.AddRange([string[]]@('--cache-dir', '/root/.cache/trivy', 'image', '--timeout', "${TimeoutSeconds}s", '--no-progress', $downloadFlag))
        $output = & docker @dockerArguments 2>&1
        $joined = $output -join "`n"
        $allOutput.Add("$downloadFlag`:`n$joined")
        if ($LASTEXITCODE -ne 0 -or $joined -match '(?i)may be corrupted') {
            $combined = $allOutput -join "`n---`n"
            throw "post-seed self-check (freshness) failed -- Trivy's own '$downloadFlag' does not recognize this seeded cache as fresh (docker exit $LASTEXITCODE); refusing to replace the live Trivy cache; output:`n$combined"
        }
    }
    return ($allOutput -join "`n---`n")
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
