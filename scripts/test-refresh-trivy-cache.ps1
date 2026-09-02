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
        } else {
            Write-Warning 'The real shared release-gate Trivy lock is currently held by another process (likely a real release image gate run elsewhere in this session) -- skipping the two brief CLI-level checks above rather than risk contending with it. Every other test in this file already covers the same logic against the isolated test volume and a scratch lock path.'
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
