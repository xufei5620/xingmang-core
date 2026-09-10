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
} else { throw "unknown safety case: $Case" }
Write-Host "TRIVY-SAFETY-PASS: $Case"
