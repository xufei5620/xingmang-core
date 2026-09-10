[CmdletBinding()]
param(
    [string]$Case = 'image-parameters',
    [string]$SourceDirectory = $PSScriptRoot,
    [string]$FixtureRoot = (Join-Path ([IO.Path]::GetTempPath()) ('invoice-trivy-safety-' + [Guid]::NewGuid().ToString('N')))
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
[IO.Directory]::CreateDirectory($FixtureRoot) | Out-Null
. (Join-Path $SourceDirectory 'refresh-trivy-cache-lib.ps1')

if ($Case -eq 'image-parameters') {
    $tokens = $null; $errors = $null
    $ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $SourceDirectory 'refresh-trivy-cache.ps1'), [ref]$tokens, [ref]$errors)
    if ($errors.Count) { throw 'image-parameters: source parse failure' }
    # Execute only the real parameter binder; never the refresh script body.
    $binder = [scriptblock]::Create('[CmdletBinding()]' + "`n" + $ast.ParamBlock.Extent.Text + "`n" + "'bound'")
    foreach ($name in @('SeedImage', 'TrivyImage', 'SelfCheckImageReference')) {
        $defaultName = if ($name -eq 'SelfCheckImageReference') { 'SeedImage' } else { $name }
        $parameter = $ast.ParamBlock.Parameters | Where-Object { $_.Name.VariablePath.UserPath -eq $defaultName }
        $pin = $parameter.DefaultValue.SafeGetValue()
        $values = @{ ProxyUrl = ''; $name = $pin }
        try { $bound = & $binder @values } catch { throw "image-parameters: explicit $name default was rejected: $($_.Exception.Message)" }
        if ($bound -cne 'bound') { throw "image-parameters: $name binder did not finish" }
        foreach ($invalid in @('postgres:18.6-alpine', ('postgres@sha256:' + ('a' * 63)), ('postgres;echo x@sha256:' + ('a' * 64)))) {
            $values[$name] = $invalid; $rejected = $false
            try { & $binder @values | Out-Null } catch { $rejected = $true }
            if (-not $rejected) { throw "image-parameters: $name accepted an invalid or mutable image reference" }
        }
    }
    # Registry ports and untagged digest references remain legitimate.
    foreach ($pin in @(('localhost:5000/team/postgres:18.6-alpine@sha256:' + ('a' * 64)), ('ghcr.io/team/trivy@sha256:' + ('a' * 64)))) {
        & $binder -ProxyUrl '' -TrivyImage $pin | Out-Null
    }
} elseif ($Case -eq 'lock-errors') {
    $badPath = Join-Path $FixtureRoot 'lock-is-directory'
    [IO.Directory]::CreateDirectory($badPath) | Out-Null
    $badError = $null
    try { (Enter-TrivyReleaseGateLock -LockPath $badPath).Dispose() } catch { $badError = $_.Exception }
    if ($null -eq $badError -or $badError.Message.Contains('already using the shared Trivy cache lock')) {
        throw 'lock-errors: directory failure was lost or reported as contention'
    }
    $path = Join-Path $FixtureRoot 'shared.lock'
    $first = Enter-TrivyReleaseGateLock -LockPath $path
    try {
        $contention = $null
        try { (Enter-TrivyReleaseGateLock -LockPath $path).Dispose() } catch { $contention = $_.Exception }
        if ($null -eq $contention -or $contention.Data['TrivyCacheLockContention'] -ne $true) {
            throw 'lock-errors: real sharing violation lacks the typed contention marker'
        }
    } finally { $first.Dispose() }
    (Enter-TrivyReleaseGateLock -LockPath $path).Dispose()
    $tokens = $null; $errors = $null
    $ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $SourceDirectory 'refresh-trivy-cache.ps1'), [ref]$tokens, [ref]$errors)
    $clause = $ast.Find({ param($node) $node -is [Management.Automation.Language.CatchClauseAst] -and $node.Extent.Text.Contains('$exitCode = 75') }, $true)
    $classify = [scriptblock]::Create('param($Failure) $exitCode=0; try { throw $Failure } ' + $clause.Extent.Text + '; $exitCode')
    if ((& $classify $contention) -ne 75) { throw 'lock-errors: actual CLI catch did not classify true contention as 75' }
    $fakeText = [IO.IOException]::new('already using the shared Trivy cache lock: unrelated IO failure')
    $rejected = $false
    try { & $classify $fakeText | Out-Null } catch { $rejected = $true }
    if (-not $rejected) { throw 'lock-errors: CLI trusted message text instead of typed contention' }
} elseif ($Case -eq 'resume-identity') {
    $script:downloadCalls = 0
    $script:downloadBytes = [Text.Encoding]::UTF8.GetBytes('BBBBBBBBBBBB')
    function curl.exe {
        $script:downloadCalls++
        $config = [IO.File]::ReadAllText($args[-1])
        foreach ($block in ($config -split '(?m)^--next\r?$')) {
            $range = [regex]::Match($block, 'range = "(\d+)-(\d+)"')
            $output = [regex]::Match($block, 'output = "([^"]+)"').Groups[1].Value
            [IO.File]::WriteAllBytes($output, [byte[]]$script:downloadBytes[[int]$range.Groups[1].Value..[int]$range.Groups[2].Value])
        }
        $global:LASTEXITCODE = 0
    }
    $parts = Join-Path $FixtureRoot 'parts'
    [IO.Directory]::CreateDirectory($parts) | Out-Null
    0..2 | ForEach-Object { [IO.File]::WriteAllText((Join-Path $parts ('part{0:d3}of003.tmp' -f $_)), 'AAAA') }
    $digest = 'sha256:' + [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($script:downloadBytes)).ToLowerInvariant()
    $arguments = @{Url="https://example.invalid/blobs/$digest";Size=12;PartCount=3;BearerToken='fixture';ProxyUrl='';PartsDirectory=$parts;DestinationPath=(Join-Path $FixtureRoot 'blob')}
    if ((Get-Command Invoke-TrivyCacheRangedDownload).Parameters.ContainsKey('ExpectedDigest')) { $arguments.ExpectedDigest = $digest }
    # Use the actual download+digest path, with only curl replaced by a fixture.
    Invoke-TrivyCacheRangedDownload @arguments | Out-Null
    if ([IO.File]::ReadAllText($arguments.DestinationPath) -cne 'BBBBBBBBBBBB' -or $script:downloadCalls -ne 1) {
        throw 'resume-identity: old same-size parts prevented the new digest download'
    }
    if ([IO.File]::ReadAllText((Join-Path $parts 'part000of003.tmp')) -cne 'AAAA') { throw 'resume-identity: legacy parts were altered' }
    $before = $script:downloadCalls
    Invoke-TrivyCacheRangedDownload @arguments | Out-Null
    if ($script:downloadCalls -ne $before) { throw 'resume-identity: valid same-identity parts were not resumed' }
    $badParts = Join-Path $FixtureRoot 'bad-parts'
    $arguments.PartsDirectory = $badParts
    $arguments.DestinationPath = Join-Path $FixtureRoot 'recovered-blob'
    $script:downloadBytes = [Text.Encoding]::UTF8.GetBytes('CCCCCCCCCCCC')
    # Seed an interrupted attempt before the call under test. A snapshot taken
    # after rejection would miss a catch block that deletes every failed part.
    $completedParts = @(Get-ChildItem -LiteralPath $parts -Recurse -File -Filter '*.tmp' | Where-Object { $_.DirectoryName -cne $parts })
    if ($completedParts.Count -ne 3) { throw 'resume-identity: retention fixture must start with three complete parts' }
    foreach ($part in $completedParts) {
        $target = Join-Path $badParts ([IO.Path]::GetRelativePath($parts, $part.FullName))
        [IO.Directory]::CreateDirectory([IO.Path]::GetDirectoryName($target)) | Out-Null
        [IO.File]::WriteAllText($target, 'CCCC')
    }
    function Get-RejectedPartSnapshot {
        param([string]$Root)
        @(Get-ChildItem -LiteralPath $Root -Recurse -File -Filter '*.tmp' | Sort-Object FullName | ForEach-Object {
            [pscustomobject]@{ Path = [IO.Path]::GetRelativePath($Root, $_.FullName); Hash = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash }
        })
    }
    $retained = @(Get-RejectedPartSnapshot -Root $badParts)
    if ($retained.Count -ne 3) { throw 'resume-identity: retention snapshot must be nonempty before rejection' }
    $rejected = $false
    try { Invoke-TrivyCacheRangedDownload @arguments | Out-Null } catch { $rejected = $_.Exception.Message -match 'digest mismatch' }
    if (-not $rejected) { throw 'resume-identity: same-length corrupt resumed parts were accepted' }
    $afterRejection = @(Get-RejectedPartSnapshot -Root $badParts)
    if ($afterRejection.Count -ne $retained.Count -or @(Compare-Object -ReferenceObject $retained -DifferenceObject $afterRejection -Property Path, Hash).Count) {
        throw 'resume-identity: rejected part paths or hashes changed during rejection'
    }
    $script:downloadBytes = [Text.Encoding]::UTF8.GetBytes('BBBBBBBBBBBB')
    try { Invoke-TrivyCacheRangedDownload @arguments | Out-Null } catch { throw "resume-identity: digest-failed parts were reused on retry: $($_.Exception.Message)" }
    if ([IO.File]::ReadAllText($arguments.DestinationPath) -cne 'BBBBBBBBBBBB' -or $script:downloadCalls -ne ($before + 1)) {
        throw 'resume-identity: digest-failed parts were reused on retry'
    }
    foreach ($part in $retained) {
        $path = Join-Path $badParts $part.Path
        if (-not (Test-Path -LiteralPath $path -PathType Leaf) -or (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -cne $part.Hash) {
            throw 'resume-identity: rejected part paths or hashes changed during retry'
        }
    }
    $tokens = $null; $errors = $null
    $ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $SourceDirectory 'refresh-trivy-cache.ps1'), [ref]$tokens, [ref]$errors)
    $call = $ast.Find({ param($node) $node -is [Management.Automation.Language.CommandAst] -and $node.GetCommandName() -eq 'Invoke-TrivyCacheRangedDownload' }, $true)
    & {
        param($callText)
        function Invoke-TrivyCacheRangedDownload { param($Url,$Size,$PartCount,$BearerToken,$ProxyUrl,$PartsDirectory,$DestinationPath,$ExpectedDigest) if ($ExpectedDigest -cne ('sha256:' + ('a' * 64))) { throw 'resume-identity: CLI omitted the immutable download identity' } }
        $blobUrl='https://example.invalid/blob'; $layer=[pscustomobject]@{Size=12;Digest=('sha256:'+('a'*64))}; $ParallelDownloads=3
        $token='fixture'; $ProxyUrl=''; $partsDirectory='fixture'; $blobPath='fixture'
        & ([scriptblock]::Create($callText)) | Out-Null
    } $call.Extent.Text
} else { throw "unknown safety case: $Case" }
Write-Host "TRIVY-SAFETY-PASS: $Case"
