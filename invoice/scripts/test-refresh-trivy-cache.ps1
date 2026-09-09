$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# Safety note for whoever reviews or reruns this file: scripts/refresh-
# trivy-cache.ps1 takes the exact same shared release\.trivy-0.74.release-
# gate.lock file scripts/release-image-gate.ps1 does, and this repo
# routinely has other concurrent agents/worktrees that may be running a
# real release gate at any time. Every functional test below (digest
# verification, extraction, the newer-cache guard, seeding round-trips)
# therefore calls the library functions directly against an isolated,
# disposable test-only Docker volume -- never scripts/refresh-trivy-
# cache.ps1 itself -- because the lock only lives in that top-level script,
# not in any library function. The two places this file does invoke the
# real CLI script are both pre-arranged to already be "up to date" before
# the call (see Test-RealLockIsFree/pre-seeding below), so each holds the
# real shared lock only for one quick manifest fetch, never a real
# download -- and both are skipped outright, not failed, if the real lock
# is already held by someone else when the test starts.

$projectRoot = Split-Path -Parent $PSScriptRoot
$libPath = Join-Path $PSScriptRoot 'refresh-trivy-cache-lib.ps1'
$scriptPath = Join-Path $PSScriptRoot 'refresh-trivy-cache.ps1'
. $libPath

function Assert-NoParseErrors {
    param([Parameter(Mandatory)][string]$Path)
    $tokens = $null
    $parseErrors = $null
    [System.Management.Automation.Language.Parser]::ParseFile($Path, [ref]$tokens, [ref]$parseErrors) | Out-Null
    if ($parseErrors.Count -ne 0) {
        throw "$Path has $($parseErrors.Count) parse error(s): $(($parseErrors | ForEach-Object { $_.Message }) -join '; ')"
    }
}

Assert-NoParseErrors -Path $libPath
Assert-NoParseErrors -Path $scriptPath
Write-Host 'Parse check passed for refresh-trivy-cache.ps1 and refresh-trivy-cache-lib.ps1.'

if ($PSVersionTable.PSVersion -lt [version]'7.5.0') {
    throw "this test suite itself requires PowerShell 7.5+ (same as refresh-trivy-cache.ps1); got $($PSVersionTable.PSVersion)"
}
if (-not (Assert-RefreshTrivyCachePowerShellRuntime)) {
    throw 'Assert-RefreshTrivyCachePowerShellRuntime did not return $true on this supported runtime'
}
Write-Host "Assert-RefreshTrivyCachePowerShellRuntime passed on PowerShell $($PSVersionTable.PSVersion)."

# --- Get-RangedDownloadPlan --------------------------------------------------
foreach ($fixture in @(
    [pscustomobject]@{ Total = 1000L; Parts = 24 },
    [pscustomobject]@{ Total = 115599257L; Parts = 24 },  # the real trivy-db layer size
    [pscustomobject]@{ Total = 10L; Parts = 24 },          # fewer bytes than requested parts
    [pscustomobject]@{ Total = 1L; Parts = 24 }
)) {
    $plan = @(Get-RangedDownloadPlan -TotalSize $fixture.Total -PartCount $fixture.Parts)
    if ($plan.Count -lt 1 -or $plan.Count -gt $fixture.Parts) {
        throw "plan for Total=$($fixture.Total) Parts=$($fixture.Parts) has $($plan.Count) entries"
    }
    $coveredBytes = 0L
    for ($i = 0; $i -lt $plan.Count; $i++) {
        if ($plan[$i].Index -ne $i) { throw "plan entry $i has Index=$($plan[$i].Index)" }
        if ($plan[$i].Start -lt 0 -or $plan[$i].End -lt $plan[$i].Start) { throw "plan entry $i has an invalid range $($plan[$i].Start)-$($plan[$i].End)" }
        if ($i -gt 0 -and $plan[$i].Start -ne ($plan[$i - 1].End + 1)) { throw "plan entry $i does not start immediately after entry $($i-1) ends (gap or overlap)" }
        $coveredBytes += $plan[$i].Length
    }
    if ($plan[0].Start -ne 0) { throw 'plan does not start at byte 0' }
    if ($plan[-1].End -ne ($fixture.Total - 1)) { throw "plan does not end at the last byte (got $($plan[-1].End), want $($fixture.Total - 1))" }
    if ($coveredBytes -ne $fixture.Total) { throw "plan covers $coveredBytes bytes, want $($fixture.Total)" }
}
$rejectedZero = $false
try { Get-RangedDownloadPlan -TotalSize 0 -PartCount 24 | Out-Null } catch { $rejectedZero = $true }
if (-not $rejectedZero) { throw 'Get-RangedDownloadPlan accepted a zero TotalSize' }
Write-Host 'Get-RangedDownloadPlan fixtures passed (full coverage, no gaps/overlaps, degenerate small sizes).'

# --- New-TrivyCacheCurlConfig -------------------------------------------------
$configPlan = @(Get-RangedDownloadPlan -TotalSize 1000L -PartCount 4)
$configPartPaths = @(0..3 | ForEach-Object { "C:\scratch\part$_.tmp" })
$configText = New-TrivyCacheCurlConfig -Plan $configPlan -Url 'https://example.invalid/blob' -PartPaths $configPartPaths -BearerToken 'tok123' -ProxyUrl 'http://127.0.0.1:10808'
if (([regex]::Matches($configText, '--next')).Count -ne ($configPlan.Count - 1)) {
    throw "curl config has the wrong number of --next separators: $configText"
}
# The registry blob endpoint answers with a 302 to a separate signed storage
# URL rather than the content itself (verified directly against the real
# registry: without "location", every ranged part came back as the same
# 140-byte redirect body, not real content). "location" must be repeated in
# every --next block, same as the other per-transfer options.
if (([regex]::Matches($configText, '(?m)^location$')).Count -ne 4) {
    throw "curl config does not repeat 'location' in every block -- ranged blob downloads would silently save 302 redirect bodies instead of content`n$configText"
}
foreach ($i in 0..3) {
    if (-not $configText.Contains("range = `"$($configPlan[$i].Start)-$($configPlan[$i].End)`"", [StringComparison]::Ordinal)) {
        throw "curl config is missing the expected range for part $i`n$configText"
    }
    # Forward slashes, not the literal backslash path: see New-
    # TrivyCacheCurlConfig's own comment -- curl's -K config file mangles a
    # literal Windows backslash path via C-style escape processing, so the
    # generated config deliberately normalizes to forward slashes.
    if (-not $configText.Contains("output = `"$($configPartPaths[$i].Replace('\', '/'))`"", [StringComparison]::Ordinal)) {
        throw "curl config is missing the expected (forward-slash-normalized) output path for part $i`n$configText"
    }
    if ($configText.Contains("output = `"$($configPartPaths[$i])`"", [StringComparison]::Ordinal)) {
        throw "curl config contains a literal backslash output path for part $i -- this is exactly the shape that silently writes no file`n$configText"
    }
}
if (([regex]::Matches($configText, [regex]::Escape('header = "Authorization: Bearer tok123"'))).Count -ne 4) {
    throw "curl config does not repeat the auth header in every block (required since --next resets per-transfer options)`n$configText"
}
if (([regex]::Matches($configText, [regex]::Escape('proxy = "http://127.0.0.1:10808"'))).Count -ne 4) {
    throw "curl config does not repeat the proxy in every block`n$configText"
}
$noProxyConfig = New-TrivyCacheCurlConfig -Plan $configPlan -Url 'https://example.invalid/blob' -PartPaths $configPartPaths -BearerToken 'tok123' -ProxyUrl ''
if ($noProxyConfig.Contains('proxy =', [StringComparison]::Ordinal)) {
    throw 'curl config included a proxy directive when ProxyUrl was empty'
}
Write-Host 'New-TrivyCacheCurlConfig produces one correctly-ranged, fully self-contained block per part.'

# --- Get-Sha256HexOfFile / Assert-OciDigestMatches ---------------------------
$hashScratch = Join-Path ([IO.Path]::GetTempPath()) "trivy-cache-test-hash-$([Guid]::NewGuid().ToString('N').Substring(0,8)).bin"
[IO.File]::WriteAllBytes($hashScratch, [Text.Encoding]::UTF8.GetBytes('hello trivy cache refresh test'))
try {
    $hex = Get-Sha256HexOfFile -Path $hashScratch
    if ($hex -cnotmatch '^[0-9a-f]{64}$') { throw "Get-Sha256HexOfFile returned an unexpected shape: $hex" }
    Assert-OciDigestMatches -ActualHex $hex -ExpectedDigest "sha256:$hex" -Description 'test file' | Out-Null
    Assert-OciDigestMatches -ActualHex $hex.ToUpperInvariant() -ExpectedDigest "sha256:$hex" -Description 'test file (case-insensitive)' | Out-Null
    $rejectedMismatch = $false
    try { Assert-OciDigestMatches -ActualHex $hex -ExpectedDigest 'sha256:0000000000000000000000000000000000000000000000000000000000000000' -Description 'test file' | Out-Null } catch { $rejectedMismatch = $true }
    if (-not $rejectedMismatch) { throw 'Assert-OciDigestMatches accepted a mismatched digest' }
    $rejectedShape = $false
    try { Assert-OciDigestMatches -ActualHex $hex -ExpectedDigest 'md5:abc' -Description 'test file' | Out-Null } catch { $rejectedShape = $true }
    if (-not $rejectedShape) { throw 'Assert-OciDigestMatches accepted a non-sha256 digest shape' }
} finally {
    Remove-Item -LiteralPath $hashScratch -Force -ErrorAction SilentlyContinue
}
Write-Host 'Get-Sha256HexOfFile / Assert-OciDigestMatches fixtures passed.'

# --- Get-OciManifestSingleLayer -----------------------------------------------
$validManifest = [pscustomobject]@{
    schemaVersion = 2
    layers        = @([pscustomobject]@{ mediaType = 'application/vnd.aquasec.trivy.db.layer.v1.tar+gzip'; digest = 'sha256:00c4d228cfa375be76c87280a6bec4c3c69be60405baf5f696edd1bf9d3522a4'; size = 115599257 })
}
$validLayer = Get-OciManifestSingleLayer -Manifest $validManifest
if ($validLayer.Digest -cne 'sha256:00c4d228cfa375be76c87280a6bec4c3c69be60405baf5f696edd1bf9d3522a4' -or $validLayer.Size -ne 115599257) {
    throw "Get-OciManifestSingleLayer did not extract the expected layer: $($validLayer | ConvertTo-Json -Compress)"
}
foreach ($invalidManifest in @(
    [pscustomobject]@{ schemaVersion = 1; layers = @($validManifest.layers[0]) },
    [pscustomobject]@{ schemaVersion = 2; layers = @() },
    [pscustomobject]@{ schemaVersion = 2; layers = @($validManifest.layers[0], $validManifest.layers[0]) },
    [pscustomobject]@{ schemaVersion = 2; layers = @([pscustomobject]@{ mediaType = 'x'; digest = 'not-a-digest'; size = 5 }) },
    [pscustomobject]@{ schemaVersion = 2; layers = @([pscustomobject]@{ mediaType = 'x'; digest = 'sha256:00c4d228cfa375be76c87280a6bec4c3c69be60405baf5f696edd1bf9d3522a4'; size = 0 }) }
)) {
    $rejected = $false
    try { Get-OciManifestSingleLayer -Manifest $invalidManifest | Out-Null } catch { $rejected = $true }
    if (-not $rejected) { throw "Get-OciManifestSingleLayer accepted an invalid manifest: $($invalidManifest | ConvertTo-Json -Compress)" }
}
Write-Host 'Get-OciManifestSingleLayer fixtures passed (valid single-layer manifest, four invalid shapes all rejected).'

# --- Read-TrivyDbMetadataText / Test-CandidateMetadataIsAcceptable ------------
$validMetadataText = '{"Version":2,"NextUpdate":"2026-09-03T00:20:12Z","UpdatedAt":"2026-09-02T00:20:12Z"}'
$parsedMetadata = Read-TrivyDbMetadataText -JsonText $validMetadataText
if ($parsedMetadata.UpdatedAt -cne '2026-09-02T00:20:12Z' -or $parsedMetadata.NextUpdate -cne '2026-09-03T00:20:12Z') {
    throw "Read-TrivyDbMetadataText did not round-trip the expected fields: $($parsedMetadata | ConvertTo-Json -Compress)"
}
foreach ($invalidMetadataText in @('{"Version":2}', '{"UpdatedAt":"not-a-date","NextUpdate":"2026-09-03T00:20:12Z"}', '{}')) {
    $rejected = $false
    try { Read-TrivyDbMetadataText -JsonText $invalidMetadataText | Out-Null } catch { $rejected = $true }
    if (-not $rejected) { throw "Read-TrivyDbMetadataText accepted invalid metadata: $invalidMetadataText" }
}
if (-not (Test-CandidateMetadataIsAcceptable -CandidateUpdatedAt '2026-09-02T00:00:00Z' -CurrentUpdatedAt $null)) {
    throw 'Test-CandidateMetadataIsAcceptable rejected a candidate when nothing is currently seeded'
}
if (-not (Test-CandidateMetadataIsAcceptable -CandidateUpdatedAt '2026-09-02T00:00:00Z' -CurrentUpdatedAt '2026-09-01T00:00:00Z')) {
    throw 'Test-CandidateMetadataIsAcceptable rejected a strictly newer candidate'
}
if (-not (Test-CandidateMetadataIsAcceptable -CandidateUpdatedAt '2026-09-02T00:00:00Z' -CurrentUpdatedAt '2026-09-02T00:00:00Z')) {
    throw 'Test-CandidateMetadataIsAcceptable rejected an equal-age candidate'
}
if (Test-CandidateMetadataIsAcceptable -CandidateUpdatedAt '2026-09-01T00:00:00Z' -CurrentUpdatedAt '2026-09-02T00:00:00Z') {
    throw 'Test-CandidateMetadataIsAcceptable accepted a strictly older candidate -- this is the "refuse to replace a cache newer than the download" guard'
}
Write-Host 'Read-TrivyDbMetadataText and Test-CandidateMetadataIsAcceptable fixtures passed, including the newer-cache refusal.'

# --- Set-TrivyDbMetadataDownloadedAt ------------------------------------------
# The upstream OCI-published metadata.json ships DownloadedAt as Go's zero
# time value ("0001-01-01T00:00:00Z") -- confirmed directly against the
# real trivy-db:2 artifact while investigating this fix. Trivy's own
# `--download-db-only` (no skip flags -- exactly what release-image-
# gate.ps1 runs every release) treats that zero value as a sign the local
# database "may be corrupted" and unconditionally redownloads, even though
# the database content itself is perfectly valid -- reproduced verbatim,
# including the exact log line, against a cache this script seeded before
# this fix. That is the actual root cause this fix responds to, so the
# function that corrects it gets its own fixture, independent of live
# network (the live section below additionally proves this against the
# real, currently-published upstream artifact).
$zeroDownloadedAtMetadataText = '{"Version":2,"NextUpdate":"2026-09-03T00:20:12Z","UpdatedAt":"2026-09-02T00:20:12Z","DownloadedAt":"0001-01-01T00:00:00Z"}'
$stampedDownloadedAt = [DateTimeOffset]::Parse('2026-09-02T18:30:00.1234567Z')
$stampedMetadataText = Set-TrivyDbMetadataDownloadedAt -JsonText $zeroDownloadedAtMetadataText -DownloadedAt $stampedDownloadedAt
$stampedMetadata = $stampedMetadataText | ConvertFrom-Json -DateKind String
if ([DateTimeOffset]::Parse([string]$stampedMetadata.DownloadedAt) -ne $stampedDownloadedAt) {
    throw "Set-TrivyDbMetadataDownloadedAt did not stamp the expected DownloadedAt: $stampedMetadataText"
}
if ([string]$stampedMetadata.UpdatedAt -cne '2026-09-02T00:20:12Z' -or [string]$stampedMetadata.NextUpdate -cne '2026-09-03T00:20:12Z') {
    throw "Set-TrivyDbMetadataDownloadedAt altered UpdatedAt/NextUpdate, which must pass through unchanged: $stampedMetadataText"
}
Read-TrivyDbMetadataText -JsonText $stampedMetadataText | Out-Null # must still validate; DownloadedAt was never part of that contract
# Also confirm it adds the field when absent entirely (defensive: in case a
# future upstream shape omits DownloadedAt rather than zero-valuing it),
# not only when overwriting an existing zero value.
$missingDownloadedAtMetadataText = '{"Version":2,"NextUpdate":"2026-09-03T00:20:12Z","UpdatedAt":"2026-09-02T00:20:12Z"}'
$stampedFromMissingText = Set-TrivyDbMetadataDownloadedAt -JsonText $missingDownloadedAtMetadataText -DownloadedAt $stampedDownloadedAt
$stampedFromMissing = $stampedFromMissingText | ConvertFrom-Json -DateKind String
if ([DateTimeOffset]::Parse([string]$stampedFromMissing.DownloadedAt) -ne $stampedDownloadedAt) {
    throw "Set-TrivyDbMetadataDownloadedAt did not add DownloadedAt when the field was absent entirely: $stampedFromMissingText"
}
Write-Host 'Set-TrivyDbMetadataDownloadedAt stamps DownloadedAt (overwriting an existing zero value, or adding it when absent) without disturbing UpdatedAt/NextUpdate.'

# --- Get-BlobDownloadPlanExcludingCompleteParts (resumability) ---------------
$resumeScratch = Join-Path ([IO.Path]::GetTempPath()) "trivy-cache-test-resume-$([Guid]::NewGuid().ToString('N').Substring(0,8))"
New-Item -ItemType Directory -Path $resumeScratch -Force | Out-Null
try {
    $resumePlan = @(Get-RangedDownloadPlan -TotalSize 300L -PartCount 3)
    $resumePartPaths = @(0..2 | ForEach-Object { Join-Path $resumeScratch "part$_.tmp" })
    [IO.File]::WriteAllBytes($resumePartPaths[0], [byte[]]::new($resumePlan[0].Length)) # complete
    [IO.File]::WriteAllBytes($resumePartPaths[1], [byte[]]::new(5))                    # short/interrupted
    # part 2 missing entirely
    $pending = @(Get-BlobDownloadPlanExcludingCompleteParts -FullPlan $resumePlan -PartPaths $resumePartPaths)
    if (@(Compare-Object -ReferenceObject @(1, 2) -DifferenceObject $pending -SyncWindow 0).Count -ne 0) {
        throw "expected parts 1 and 2 pending (0 already complete), got: $($pending -join ',')"
    }
} finally {
    Remove-Item -LiteralPath $resumeScratch -Recurse -Force -ErrorAction SilentlyContinue
}
Write-Host 'Get-BlobDownloadPlanExcludingCompleteParts correctly identifies only missing/short parts as pending.'

# --- lock helpers (always against a scratch project root, never the real one) -
$lockScratchRoot = Join-Path ([IO.Path]::GetTempPath()) "trivy-cache-test-lock-$([Guid]::NewGuid().ToString('N').Substring(0,8))"
New-Item -ItemType Directory -Path $lockScratchRoot -Force | Out-Null
try {
    $scratchLockPath = Get-TrivyReleaseGateLockPath -ProjectRoot $lockScratchRoot
    if ($scratchLockPath -cne (Join-Path $lockScratchRoot 'release\.trivy-0.74.release-gate.lock')) {
        throw "Get-TrivyReleaseGateLockPath returned an unexpected path: $scratchLockPath"
    }
    $firstLock = Enter-TrivyReleaseGateLock -LockPath $scratchLockPath
    try {
        $contentionRejected = $false
        try { (Enter-TrivyReleaseGateLock -LockPath $scratchLockPath).Dispose() } catch { $contentionRejected = $_.Exception.Message.Contains('already using the shared Trivy cache lock', [StringComparison]::Ordinal) }
        if (-not $contentionRejected) { throw 'a second Enter-TrivyReleaseGateLock on the same path did not fail with the expected contention message' }
    } finally {
        $firstLock.Dispose()
    }
    # released -- must be acquirable again
    (Enter-TrivyReleaseGateLock -LockPath $scratchLockPath).Dispose()
} finally {
    Remove-Item -LiteralPath $lockScratchRoot -Recurse -Force -ErrorAction SilentlyContinue
}
Write-Host 'Get-TrivyReleaseGateLockPath / Enter-TrivyReleaseGateLock fixtures passed (path shape, contention, release, reacquisition) -- all against a scratch project root, never the real shared lock.'

# --- network + docker checks -------------------------------------------------
$networkAvailable = $false
$dockerAvailable = ($null -ne (Get-Command docker -CommandType Application -ErrorAction SilentlyContinue))
if ($dockerAvailable) {
    & docker info *> $null
    $dockerAvailable = ($LASTEXITCODE -eq 0)
}
$proxyUrl = if (-not [string]::IsNullOrWhiteSpace($env:HTTPS_PROXY)) { $env:HTTPS_PROXY } elseif (-not [string]::IsNullOrWhiteSpace($env:HTTP_PROXY)) { $env:HTTP_PROXY } else { 'http://127.0.0.1:10808' }
$realDbToken = $null
$realDbManifest = $null
$realDbLayer = $null
try {
    $realDbToken = Get-OciRegistryToken -Registry 'mirror.gcr.io' -Repository 'aquasec/trivy-db' -ProxyUrl $proxyUrl
    $realDbManifest = Get-OciManifest -Registry 'mirror.gcr.io' -Repository 'aquasec/trivy-db' -Tag '2' -BearerToken $realDbToken -ProxyUrl $proxyUrl
    $realDbLayer = Get-OciManifestSingleLayer -Manifest $realDbManifest
    $networkAvailable = $true
    Write-Host "Live registry check reached mirror.gcr.io/aquasec/trivy-db:2 -- current layer digest $($realDbLayer.Digest), $([Math]::Round($realDbLayer.Size / 1MB, 1)) MiB."
} catch {
    Write-Warning "Could not reach mirror.gcr.io (network/proxy unavailable in this environment): $_"
    Write-Warning 'Skipping every live network/docker test below; the fixtures above already cover all pure logic.'
}

if ($networkAvailable) {
    # Also confirm the java-db repository/tag resolve (manifest only -- it is
    # ~900 MiB and this test does not download it) and that the mediaType
    # this script asserts on both matches reality.
    $javaToken = Get-OciRegistryToken -Registry 'mirror.gcr.io' -Repository 'aquasec/trivy-java-db' -ProxyUrl $proxyUrl
    $javaManifest = Get-OciManifest -Registry 'mirror.gcr.io' -Repository 'aquasec/trivy-java-db' -Tag '1' -BearerToken $javaToken -ProxyUrl $proxyUrl
    $javaLayer = Get-OciManifestSingleLayer -Manifest $javaManifest
    if ($javaLayer.MediaType -cne 'application/vnd.aquasec.trivy.javadb.layer.v1.tar+gzip') {
        throw "trivy-java-db layer mediaType changed upstream: $($javaLayer.MediaType) -- refresh-trivy-cache.ps1's ExpectedMediaType needs updating"
    }
    if ($realDbLayer.MediaType -cne 'application/vnd.aquasec.trivy.db.layer.v1.tar+gzip') {
        throw "trivy-db layer mediaType changed upstream: $($realDbLayer.MediaType) -- refresh-trivy-cache.ps1's ExpectedMediaType needs updating"
    }
    Write-Host "Live registry check also confirmed mirror.gcr.io/aquasec/trivy-java-db:1's manifest and mediaType (not downloaded -- $([Math]::Round($javaLayer.Size / 1MB, 1)) MiB, out of scope for this test's bandwidth)."
}

if ($networkAvailable -and $dockerAvailable) {
    $testVolume = "invoice-release-gate-trivy-0-74-0-testonly-$([Guid]::NewGuid().ToString('N').Substring(0,8))"
    $workScratch = Join-Path ([IO.Path]::GetTempPath()) "trivy-cache-test-work-$([Guid]::NewGuid().ToString('N').Substring(0,8))"
    New-Item -ItemType Directory -Path $workScratch -Force | Out-Null
    $seedImage = 'postgres:18.6-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
    # Must match release-image-gate.ps1's own $trivyImage pin and refresh-
    # trivy-cache.ps1's own -TrivyImage default exactly.
    $trivyImageForTest = 'ghcr.io/aquasecurity/trivy:0.74.0@sha256:62b1e65e8869bc4b4c6aa4fa2b21595256c7c2f6018a9d9ad61caf87187c1969'

    try {
        New-TrivyCacheVolumeIfMissing -Volume $testVolume
        if (-not (Test-DockerVolumeExists -Volume $testVolume)) { throw 'New-TrivyCacheVolumeIfMissing did not create a volume Test-DockerVolumeExists can see' }

        $freshState = Get-TrivyCacheVolumeComponentState -Volume $testVolume -SubPath 'db' -SeedImage $seedImage
        if ($null -ne $freshState.Digest -or $null -ne $freshState.MetadataText) {
            throw "a brand new volume reported non-empty state: $($freshState | ConvertTo-Json -Compress)"
        }
        Write-Host 'Get-TrivyCacheVolumeComponentState correctly reports no state on a fresh, never-seeded volume.'

        # --- full real download, digest verify, extract, seed, for trivy-db ---
        $blobUrl = "https://mirror.gcr.io/v2/aquasec/trivy-db/blobs/$($realDbLayer.Digest)"
        $partsDirectory = Join-Path $workScratch 'parts'
        $blobPath = Join-Path $workScratch 'blob.tar.gz'
        $downloadSw = [System.Diagnostics.Stopwatch]::StartNew()
        Invoke-TrivyCacheRangedDownload -Url $blobUrl -Size $realDbLayer.Size -PartCount 24 `
            -BearerToken $realDbToken -ProxyUrl $proxyUrl -PartsDirectory $partsDirectory -DestinationPath $blobPath | Out-Null
        $downloadSw.Stop()
        $downloadedHex = Get-Sha256HexOfFile -Path $blobPath
        Assert-OciDigestMatches -ActualHex $downloadedHex -ExpectedDigest $realDbLayer.Digest -Description 'live trivy-db download' | Out-Null
        Write-Host "Real 24-way parallel ranged download of the current trivy-db layer ($([Math]::Round($realDbLayer.Size / 1MB, 1)) MiB) completed and digest-verified in $([Math]::Round($downloadSw.Elapsed.TotalSeconds, 1))s."

        # Resumability: delete the last part and re-run -- only that one part
        # should be re-fetched, and the reassembled blob must still verify.
        $lastPartPath = Get-ChildItem -LiteralPath $partsDirectory -Filter 'part*.tmp' | Sort-Object Name | Select-Object -Last 1
        Remove-Item -LiteralPath $lastPartPath.FullName -Force
        Remove-Item -LiteralPath $blobPath -Force
        Invoke-TrivyCacheRangedDownload -Url $blobUrl -Size $realDbLayer.Size -PartCount 24 `
            -BearerToken $realDbToken -ProxyUrl $proxyUrl -PartsDirectory $partsDirectory -DestinationPath $blobPath | Out-Null
        $resumedHex = Get-Sha256HexOfFile -Path $blobPath
        Assert-OciDigestMatches -ActualHex $resumedHex -ExpectedDigest $realDbLayer.Digest -Description 'resumed live trivy-db download' | Out-Null
        Write-Host 'Deleting one downloaded part and rerunning correctly re-fetched only that part and still verified.'

        $extractDirectory = Join-Path $workScratch 'extracted'
        Expand-TrivyCacheTarGz -ArchivePath $blobPath -DestinationDirectory $extractDirectory | Out-Null
        foreach ($expectedFile in @('trivy.db', 'metadata.json')) {
            if (-not (Test-Path -LiteralPath (Join-Path $extractDirectory $expectedFile) -PathType Leaf)) {
                throw "extracted trivy-db archive is missing $expectedFile -- Trivy's real cache layout may have changed"
            }
        }
        $realMetadataText = Get-Content -Raw -LiteralPath (Join-Path $extractDirectory 'metadata.json')
        $realMetadata = Read-TrivyDbMetadataText -JsonText $realMetadataText
        Write-Host "Extracted archive has exactly the expected trivy.db + metadata.json; real UpdatedAt=$($realMetadata.UpdatedAt) NextUpdate=$($realMetadata.NextUpdate)."

        Publish-TrivyCacheComponentToVolume -Volume $testVolume -SubPath 'db' -LocalSourceDirectory $extractDirectory `
            -Digest $realDbLayer.Digest -SeedImage $seedImage -FileNames @('trivy.db', 'metadata.json')
        $seededState = Get-TrivyCacheVolumeComponentState -Volume $testVolume -SubPath 'db' -SeedImage $seedImage
        if ($seededState.Digest -cne $realDbLayer.Digest) {
            throw "seeded volume digest sidecar does not round-trip: got $($seededState.Digest), want $($realDbLayer.Digest)"
        }
        $seededMetadata = Read-TrivyDbMetadataText -JsonText $seededState.MetadataText
        if ($seededMetadata.UpdatedAt -cne $realMetadata.UpdatedAt) {
            throw "seeded volume metadata.json does not round-trip UpdatedAt: got $($seededMetadata.UpdatedAt), want $($realMetadata.UpdatedAt)"
        }
        Write-Host 'Publish-TrivyCacheComponentToVolume seeded the real trivy-db content; digest sidecar and metadata.json both round-trip correctly.'

        # --- newer-cache guard, using synthetic data (no extra download) ------
        $fakeNewerScratch = Join-Path $workScratch 'fake-newer'
        New-Item -ItemType Directory -Path $fakeNewerScratch -Force | Out-Null
        Set-Content -LiteralPath (Join-Path $fakeNewerScratch 'trivy.db') -Value 'not a real db, only used to prove the newer-guard' -Encoding utf8
        Set-Content -LiteralPath (Join-Path $fakeNewerScratch 'metadata.json') -Value '{"Version":2,"UpdatedAt":"2099-01-01T00:00:00Z","NextUpdate":"2099-01-02T00:00:00Z"}' -Encoding utf8
        $fakeDigest = 'sha256:' + ('f' * 64)
        Publish-TrivyCacheComponentToVolume -Volume $testVolume -SubPath 'db' -LocalSourceDirectory $fakeNewerScratch `
            -Digest $fakeDigest -SeedImage $seedImage -FileNames @('trivy.db', 'metadata.json')
        $afterFakeSeedState = Get-TrivyCacheVolumeComponentState -Volume $testVolume -SubPath 'db' -SeedImage $seedImage
        $afterFakeSeedMetadata = Read-TrivyDbMetadataText -JsonText $afterFakeSeedState.MetadataText
        if ($afterFakeSeedMetadata.UpdatedAt -cne '2099-01-01T00:00:00Z') {
            throw 'seeding a synthetic far-future UpdatedAt did not take effect -- cannot exercise the newer-guard'
        }
        # Now: is the REAL (much older) download acceptable against this
        # synthetic far-future "currently seeded" state? It must not be.
        $guardAccepts = Test-CandidateMetadataIsAcceptable -CandidateUpdatedAt $realMetadata.UpdatedAt -CurrentUpdatedAt $afterFakeSeedMetadata.UpdatedAt
        if ($guardAccepts) {
            throw 'Test-CandidateMetadataIsAcceptable accepted a real, older download over a synthetic far-future currently-seeded UpdatedAt'
        }
        Write-Host 'Newer-cache guard correctly refuses to accept a real (older) download over a synthetic far-future currently-seeded metadata.json.'
        # Restore the real, correct content for the rest of this test.
        Publish-TrivyCacheComponentToVolume -Volume $testVolume -SubPath 'db' -LocalSourceDirectory $extractDirectory `
            -Digest $realDbLayer.Digest -SeedImage $seedImage -FileNames @('trivy.db', 'metadata.json')

        # --- staging + mandatory post-seed self-check (same real download
        # above, no extra network cost for the trivy.db content itself) -----
        $rawMetadataParsed = $realMetadataText | ConvertFrom-Json -DateKind String
        if ([string]$rawMetadataParsed.DownloadedAt -cne '0001-01-01T00:00:00Z') {
            Write-Warning "the live trivy-db:2 upstream artifact's raw DownloadedAt is '$($rawMetadataParsed.DownloadedAt)', not the Go zero value this fix was written against -- upstream may have changed. Set-TrivyDbMetadataDownloadedAt is applied unconditionally by refresh-trivy-cache.ps1 regardless, so this is informational, not fatal, here."
        } else {
            Write-Host "Confirmed live: the real trivy-db:2 upstream artifact still ships DownloadedAt as Go's zero value -- exactly the precondition this fix corrects."
        }

        # Correct the metadata in place (mutating $extractDirectory directly
        # rather than copying the 1.3GB trivy.db elsewhere) and re-publish,
        # exactly as refresh-trivy-cache.ps1 itself now does before ever
        # seeding anything.
        $correctedMetadataText = Set-TrivyDbMetadataDownloadedAt -JsonText $realMetadataText -DownloadedAt ([DateTimeOffset]::UtcNow)
        $correctedMetadata = Read-TrivyDbMetadataText -JsonText $correctedMetadataText
        if ($correctedMetadata.UpdatedAt -cne $realMetadata.UpdatedAt) {
            throw 'Set-TrivyDbMetadataDownloadedAt altered UpdatedAt on the real downloaded metadata.json'
        }
        [IO.File]::WriteAllText((Join-Path $extractDirectory 'metadata.json'), $correctedMetadataText, [Text.UTF8Encoding]::new($false))
        Publish-TrivyCacheComponentToVolume -Volume $testVolume -SubPath 'db' -LocalSourceDirectory $extractDirectory `
            -Digest $realDbLayer.Digest -SeedImage $seedImage -FileNames @('trivy.db', 'metadata.json')

        $stagingTestVolume = "$testVolume-staging-test"
        $rawExtractForRegressionTest = $null
        try {
            New-TrivyCacheVolumeIfMissing -Volume $stagingTestVolume
            Copy-TrivyCacheVolumeToVolume -SourceVolume $testVolume -DestinationVolume $stagingTestVolume -SeedImage $seedImage
            $clonedState = Get-TrivyCacheVolumeComponentState -Volume $stagingTestVolume -SubPath 'db' -SeedImage $seedImage
            if ($clonedState.Digest -cne $realDbLayer.Digest) {
                throw "Copy-TrivyCacheVolumeToVolume did not clone the db component's digest sidecar correctly: got $($clonedState.Digest)"
            }
            $clonedMetadata = Read-TrivyDbMetadataText -JsonText $clonedState.MetadataText
            if ($clonedMetadata.UpdatedAt -cne $realMetadata.UpdatedAt) {
                throw 'Copy-TrivyCacheVolumeToVolume did not clone metadata.json correctly'
            }
            $tmpDirCheck = (& docker run --rm -v "${stagingTestVolume}:/cache:ro" $seedImage sh -c 'if [ -d /cache/tmp ]; then echo TMPDIR_OK; fi' 2>&1) -join "`n"
            if (-not $tmpDirCheck.Contains('TMPDIR_OK')) {
                throw 'Copy-TrivyCacheVolumeToVolume did not create the tmp/ directory Invoke-TrivyCacheFreshnessSelfCheck points TMPDIR at (needed so a real redownload, if the freshness check ever triggers one, does not hit the cross-filesystem TMPDIR move bug found while investigating this incident)'
            }
            Write-Host 'Copy-TrivyCacheVolumeToVolume clones the digest sidecar and metadata.json correctly and creates the tmp/ directory the freshness self-check depends on.'

            Invoke-TrivyCacheOfflineScanSelfCheck -Volume $stagingTestVolume -TrivyImage $trivyImageForTest -ScanImageReference $seedImage | Out-Null
            Write-Host 'Invoke-TrivyCacheOfflineScanSelfCheck passes against a correctly-staged (DownloadedAt-stamped) volume.'

            Invoke-TrivyCacheFreshnessSelfCheck -Volume $stagingTestVolume -TrivyImage $trivyImageForTest -IncludeJavaDb $false -ProxyUrl $proxyUrl | Out-Null
            Write-Host 'Invoke-TrivyCacheFreshnessSelfCheck passes (fast, no-op) against a correctly-staged (DownloadedAt-stamped) volume.'

            # Prove the freshness check earns its place: reseed staging with
            # the RAW (zero-DownloadedAt) upstream metadata and confirm (a)
            # the offline-scan check alone still PASSES (documenting exactly
            # why it is not sufficient by itself -- this is what the manual
            # incident repro also saw) and (b) the freshness check now fails
            # on Trivy's real "may be corrupted" signal.
            $rawExtractForRegressionTest = Join-Path $workScratch 'raw-metadata-regression'
            New-Item -ItemType Directory -Path $rawExtractForRegressionTest -Force | Out-Null
            Copy-Item -LiteralPath (Join-Path $extractDirectory 'trivy.db') -Destination (Join-Path $rawExtractForRegressionTest 'trivy.db')
            Set-Content -LiteralPath (Join-Path $rawExtractForRegressionTest 'metadata.json') -Value $realMetadataText -NoNewline -Encoding utf8
            Publish-TrivyCacheComponentToVolume -Volume $stagingTestVolume -SubPath 'db' -LocalSourceDirectory $rawExtractForRegressionTest `
                -Digest $realDbLayer.Digest -SeedImage $seedImage -FileNames @('trivy.db', 'metadata.json')

            Invoke-TrivyCacheOfflineScanSelfCheck -Volume $stagingTestVolume -TrivyImage $trivyImageForTest -ScanImageReference $seedImage | Out-Null
            Write-Host 'Confirmed: the offline-scan self-check alone PASSES even against a raw zero-DownloadedAt cache -- exactly why this fix also added the freshness self-check below.'

            $regressionCaught = $false
            try {
                Invoke-TrivyCacheFreshnessSelfCheck -Volume $stagingTestVolume -TrivyImage $trivyImageForTest -IncludeJavaDb $false -ProxyUrl $proxyUrl -TimeoutSeconds 8 | Out-Null
            } catch {
                $regressionCaught = $_.Exception.Message.Contains('may be corrupted', [StringComparison]::OrdinalIgnoreCase) -or
                    $_.Exception.Message.Contains('does not recognize this seeded cache as fresh', [StringComparison]::OrdinalIgnoreCase)
                if (-not $regressionCaught) { throw }
            }
            if (-not $regressionCaught) {
                throw 'Invoke-TrivyCacheFreshnessSelfCheck did not fail against a raw zero-DownloadedAt cache -- it should catch the exact regression class that caused this incident'
            }
            Write-Host "Invoke-TrivyCacheFreshnessSelfCheck correctly fails against a raw zero-DownloadedAt cache, reproducing (and this time catching) the incident's root cause."
        } finally {
            if (Test-DockerVolumeExists -Volume $stagingTestVolume) {
                & docker volume rm --force $stagingTestVolume *> $null
            }
            if ($rawExtractForRegressionTest) {
                Remove-Item -LiteralPath $rawExtractForRegressionTest -Recurse -Force -ErrorAction SilentlyContinue
            }
        }

        # Restore the real, DownloadedAt-corrected content once more so the
        # CLI end-to-end checks below (which reuse $testVolume) see the
        # correct shape too.
        Publish-TrivyCacheComponentToVolume -Volume $testVolume -SubPath 'db' -LocalSourceDirectory $extractDirectory `
            -Digest $realDbLayer.Digest -SeedImage $seedImage -FileNames @('trivy.db', 'metadata.json')

        # --- CLI end-to-end: pre-seeded-current volume, -WhatIf, fast path ----
        # The test volume is already seeded with the exact current upstream
        # digest, so this CLI call takes the "already up to date" branch: one
        # quick manifest fetch, no download, and (being -WhatIf as well) no
        # write -- bounding how long it holds the real shared release-gate
        # lock to roughly the time of one HTTPS request.
        $lockPath = Get-TrivyReleaseGateLockPath -ProjectRoot $projectRoot
        $lockCurrentlyFree = $true
        try { (Enter-TrivyReleaseGateLock -LockPath $lockPath).Dispose() } catch { $lockCurrentlyFree = $false }

        if ($lockCurrentlyFree) {
            $whatIfOutput = & pwsh -NoProfile -NonInteractive -File $scriptPath -TrivyCacheVolume $testVolume -SkipJavaDb -ProxyUrl $proxyUrl -WhatIf 2>&1 | Out-String
            if ($LASTEXITCODE -ne 0) {
                throw "refresh-trivy-cache.ps1 -WhatIf against an already-current test volume failed (exit $LASTEXITCODE):`n$whatIfOutput"
            }
            if (-not $whatIfOutput.Contains('already up to date', [StringComparison]::OrdinalIgnoreCase)) {
                throw "refresh-trivy-cache.ps1 -WhatIf did not report the already-up-to-date fast path:`n$whatIfOutput"
            }
            Write-Host 'refresh-trivy-cache.ps1 -WhatIf CLI path (pre-seeded, already-current volume) took the fast, no-download, no-write branch and exited 0.'

            # And the true idempotent path (no -WhatIf, but nothing to do):
            # since the volume is already current, this must not attempt any
            # network transfer beyond one manifest fetch, and must exit 0.
            $idempotentOutput = & pwsh -NoProfile -NonInteractive -File $scriptPath -TrivyCacheVolume $testVolume -SkipJavaDb -ProxyUrl $proxyUrl 2>&1 | Out-String
            if ($LASTEXITCODE -ne 0) {
                throw "refresh-trivy-cache.ps1 against an already-current test volume failed (exit $LASTEXITCODE):`n$idempotentOutput"
            }
            if (-not $idempotentOutput.Contains('unchanged', [StringComparison]::OrdinalIgnoreCase)) {
                throw "refresh-trivy-cache.ps1's idempotent run did not report Action=unchanged:`n$idempotentOutput"
            }
            if (-not ($idempotentOutput -match 'UpdatedAt:\s+\S')) {
                throw "refresh-trivy-cache.ps1 did not print the DB UpdatedAt/NextUpdate summary:`n$idempotentOutput"
            }
            Write-Host 'refresh-trivy-cache.ps1 (no -WhatIf) against the same already-current volume is a true no-op (Action=unchanged) and prints UpdatedAt/NextUpdate, proving the idempotent-daily-run contract end to end.'

            # --- per-run logging + latest.json + pruning (XM-INV-TRIVY-REFRESH-LOG) ---
            # A dedicated -WorkDirectory (never the real logs\trivy-cache-
            # refresh) so this fixture's 35 synthetic pre-existing run logs,
            # and the pruning they trigger, never touch real run history.
            $loggingWorkDirectory = Join-Path $workScratch 'logging-fixture'
            $loggingRunsDirectory = Join-Path $loggingWorkDirectory 'runs'
            New-Item -ItemType Directory -Path $loggingRunsDirectory -Force | Out-Null
            for ($i = 0; $i -lt 35; $i++) {
                $fakeRunLogPath = Join-Path $loggingRunsDirectory "19700101T000000Z-fake$i.log"
                Set-Content -LiteralPath $fakeRunLogPath -Value "pre-existing run log $i, only used to exercise pruning" -Encoding utf8
                (Get-Item -LiteralPath $fakeRunLogPath).LastWriteTimeUtc = [DateTime]::new(1970, 1, 1, 0, 0, 0, [DateTimeKind]::Utc).AddSeconds($i)
            }

            $loggingOutput = & pwsh -NoProfile -NonInteractive -File $scriptPath -TrivyCacheVolume $testVolume -SkipJavaDb -ProxyUrl $proxyUrl -WorkDirectory $loggingWorkDirectory 2>&1 | Out-String
            if ($LASTEXITCODE -ne 0) {
                throw "refresh-trivy-cache.ps1 against an already-current test volume (custom -WorkDirectory, logging fixture) failed (exit $LASTEXITCODE):`n$loggingOutput"
            }

            $survivingRunLogs = @(Get-ChildItem -LiteralPath $loggingRunsDirectory -Filter '*.log' -File)
            if ($survivingRunLogs.Count -ne 30) {
                throw "expected exactly 30 run logs to survive pruning (35 pre-seeded + 1 new = 36, keep the last 30), got $($survivingRunLogs.Count)"
            }
            $newestRunLog = $survivingRunLogs | Sort-Object LastWriteTimeUtc -Descending | Select-Object -First 1
            if ($newestRunLog.Name -notmatch '^\d{8}T\d{6}Z-\d+\.log$') {
                throw "the newest run log's name does not match the documented <UTC stamp>-<pid>.log shape: $($newestRunLog.Name)"
            }
            $newestRunLogContent = Get-Content -Raw -LiteralPath $newestRunLog.FullName
            foreach ($expectedFragment in @('Run started (UTC):', 'Run finished (UTC):', 'Exit code:          0', 'already up to date')) {
                if (-not $newestRunLogContent.Contains($expectedFragment, [StringComparison]::Ordinal)) {
                    throw "the newest run log is missing an expected fragment '$expectedFragment':`n$newestRunLogContent"
                }
            }
            if ($newestRunLogContent.Contains('Error:', [StringComparison]::Ordinal)) {
                throw "a successful run's log must not contain an Error: line:`n$newestRunLogContent"
            }
            Write-Host 'refresh-trivy-cache.ps1 writes a per-run transcript log (start/end time, exit code, the console summary) under runs\<UTC stamp>-<pid>.log and prunes older run logs down to the last 30.'

            $loggingLatestJsonPath = Join-Path $loggingRunsDirectory 'latest.json'
            if (-not (Test-Path -LiteralPath $loggingLatestJsonPath -PathType Leaf)) {
                throw "refresh-trivy-cache.ps1 did not write latest.json to $loggingLatestJsonPath"
            }
            $latestSummary = Get-Content -Raw -LiteralPath $loggingLatestJsonPath | ConvertFrom-Json
            if ($latestSummary.exit_code -ne 0) { throw "latest.json exit_code should be 0 for a successful run, got $($latestSummary.exit_code)" }
            if ($latestSummary.action_db -cne 'unchanged') { throw "latest.json action_db should be 'unchanged' for this already-current volume, got '$($latestSummary.action_db)'" }
            if ($null -ne $latestSummary.action_java_db) { throw "latest.json action_java_db should be null (this run used -SkipJavaDb), got '$($latestSummary.action_java_db)'" }
            if ($null -ne $latestSummary.error) { throw "latest.json error should be null for a successful run, got '$($latestSummary.error)'" }
            foreach ($requiredField in @('started_at', 'finished_at')) {
                if ([string]::IsNullOrWhiteSpace($latestSummary.$requiredField)) { throw "latest.json is missing or has an empty $requiredField" }
            }
            Write-Host 'latest.json correctly summarizes a successful run: exit_code=0, action_db=unchanged, action_java_db=null, error=null.'
        } else {
            Write-Warning 'The real shared release-gate Trivy lock is currently held by another process (likely a real release image gate run elsewhere in this session) -- skipping the CLI-level checks above rather than risk contending with it. Every other test in this file already covers the same logic against the isolated test volume and a scratch lock path.'
        }

        # --- exit code 75: release gate holds the shared lock, skip without touching the volume ---
        # A fresh attempt at the real shared lock (independent of
        # $lockCurrentlyFree above, which only reflects its state before the
        # CLI checks that just ran): this fixture needs to hold the lock
        # itself for the duration of one child CLI invocation, so it cannot
        # reuse that earlier, momentary check.
        $heldLock = $null
        try { $heldLock = Enter-TrivyReleaseGateLock -LockPath (Get-TrivyReleaseGateLockPath -ProjectRoot $projectRoot) } catch { $heldLock = $null }
        if ($null -eq $heldLock) {
            Write-Warning 'The real shared release-gate Trivy lock is currently held by another process -- skipping the exit-75 (gate holds the cache volume) CLI fixture rather than risk contending with it.'
        } else {
            try {
                $lockContentionWorkDirectory = Join-Path $workScratch 'lock-contention-fixture'
                $lockContentionOutput = & pwsh -NoProfile -NonInteractive -File $scriptPath -TrivyCacheVolume $testVolume -SkipJavaDb -ProxyUrl $proxyUrl -WorkDirectory $lockContentionWorkDirectory 2>&1 | Out-String
                if ($LASTEXITCODE -ne 75) {
                    throw "refresh-trivy-cache.ps1 did not exit 75 while this test process held the shared release-gate lock (exit $LASTEXITCODE):`n$lockContentionOutput"
                }
                if (-not $lockContentionOutput.Contains('gate holds the cache volume; skipped', [StringComparison]::Ordinal)) {
                    throw "refresh-trivy-cache.ps1's lock-contention output did not contain the documented skip message:`n$lockContentionOutput"
                }

                $stateAfterLockContention = Get-TrivyCacheVolumeComponentState -Volume $testVolume -SubPath 'db' -SeedImage $seedImage
                if ($stateAfterLockContention.Digest -cne $realDbLayer.Digest) {
                    throw 'the live test volume state changed even though the run should have exited on lock contention before ever touching it'
                }

                $lockContentionLatestJsonPath = Join-Path $lockContentionWorkDirectory 'runs\latest.json'
                $lockContentionLatest = Get-Content -Raw -LiteralPath $lockContentionLatestJsonPath | ConvertFrom-Json
                if ($lockContentionLatest.exit_code -ne 75) { throw "lock-contention latest.json exit_code should be 75, got $($lockContentionLatest.exit_code)" }
                if ($lockContentionLatest.error -cnotmatch 'gate holds the cache volume') { throw "lock-contention latest.json error field does not mention the gate holding the cache volume: '$($lockContentionLatest.error)'" }
                if ($null -ne $lockContentionLatest.action_db -or $null -ne $lockContentionLatest.action_java_db) {
                    throw "lock-contention latest.json should report both actions as null (nothing was ever attempted), got action_db='$($lockContentionLatest.action_db)' action_java_db='$($lockContentionLatest.action_java_db)'"
                }

                $lockContentionRunLog = Get-ChildItem -LiteralPath (Join-Path $lockContentionWorkDirectory 'runs') -Filter '*.log' -File | Select-Object -First 1
                $lockContentionLogContent = Get-Content -Raw -LiteralPath $lockContentionRunLog.FullName
                if (-not $lockContentionLogContent.Contains('Exit code:          75', [StringComparison]::Ordinal)) {
                    throw "lock-contention run log does not record exit code 75:`n$lockContentionLogContent"
                }
                Write-Host 'refresh-trivy-cache.ps1 exits 75 with a clear message and an untouched cache volume when the release image gate (or a concurrent refresh) already holds the shared Trivy cache lock; recorded correctly in both the run log and latest.json.'
            } finally {
                $heldLock.Dispose()
            }
        }
    } finally {
        if (Test-DockerVolumeExists -Volume $testVolume) {
            & docker volume rm --force $testVolume *> $null
        }
        Remove-Item -LiteralPath $workScratch -Recurse -Force -ErrorAction SilentlyContinue
    }
} else {
    Write-Warning 'Skipping all Docker-volume-based live fixtures (network or docker unavailable in this environment).'
}

Write-Host 'All refresh-trivy-cache fixtures passed.'
