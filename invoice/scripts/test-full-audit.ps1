[CmdletBinding()]
param(
    [string]$FixtureRoot = (Join-Path ([IO.Path]::GetTempPath()) ('invoice-full-audit-' + [Guid]::NewGuid().ToString('N')))
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$projectRoot = Split-Path -Parent $PSScriptRoot
$FixtureRoot = [IO.Path]::GetFullPath($FixtureRoot)
$pwsh = (Get-Process -Id $PID).Path
$pythonNames = if ($IsWindows) { @('python', 'python3') } else { @('python3', 'python') }
$python = @($pythonNames | ForEach-Object { Get-Command $_ -CommandType Application -ErrorAction SilentlyContinue } | Select-Object -First 1)
if ($python.Count -eq 0) { throw 'Python 3 is required for full-audit regressions' }
$pythonPath = $python[0].Source
if ($IsWindows) {
    # Resolve Git Bash from the actual Git installation, not a personal drive.
    $gitDirectory = Split-Path -Parent (Get-Command git -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
    $bashCandidates = @((Join-Path $gitDirectory '../bin/bash.exe'), (Join-Path $gitDirectory 'bash.exe'))
    $bashPath = @($bashCandidates | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } | Select-Object -First 1)
    if ($bashPath.Count -eq 0) { throw 'Git Bash is required for Windows full-audit regressions' }
    $bashPath = [IO.Path]::GetFullPath($bashPath[0])
} else {
    $bashPath = (Get-Command bash -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
}
foreach ($directory in @('', 'signature', 'release-env', 'migration-gate', 'repair-kinds', 'repair-exits')) {
    [IO.Directory]::CreateDirectory((Join-Path $FixtureRoot $directory)) | Out-Null
}
# Explicit coverage: only inert/local regressions, never maintenance entrypoints.
$checks = @(
    @{ File='tests/test_full_audit_wiring.py'; Arguments=@() }
    @{ File='test-git-root-boundaries.ps1'; Arguments=@() }
    @{ File='test-release-path-boundaries.ps1'; Arguments=@() }
    @{ File='test-unified-operations.py'; Arguments=@() }
    @{ File='test-trivy-cache-safety.ps1'; Arguments=@('-Case', 'image-parameters') }
    @{ File='test-trivy-cache-safety.ps1'; Arguments=@('-Case', 'lock-errors') }
    @{ File='test-trivy-cache-safety.ps1'; Arguments=@('-Case', 'resume-identity') }
    @{ File='test-trivy-cache-safety.ps1'; Arguments=@('-Case', 'unchanged-cache') }
    @{ File='test-trivy-cache-safety.ps1'; Arguments=@('-Case', 'shared-cache-lock') }
    @{ File='test-register-trivy-refresh-behavior.ps1'; Arguments=@('-Case', 'legacy-suite') }
    @{ File='test-register-trivy-refresh-behavior.ps1'; Arguments=@('-Case', 'meta') }
    @{ File='tests/test_projection_attachment.py'; Arguments=@() }
    @{ File='tests/test_capture_failures.py'; Arguments=@() }
    @{ File='tests/test_collection_producers.py'; Arguments=@() }
    @{ File='tests/test_index_finalizer.py'; Arguments=@() }
    @{ File='tests/test_shell_syntax_coverage.py'; Arguments=@() }
    @{ File='test-runbook-release-signature.ps1'; Arguments=@('-FixtureRoot', (Join-Path $FixtureRoot 'signature')) }
    @{ File='test-runbook-command-contracts.py'; Arguments=@('--case', 'release-env', '--fixture-root', (Join-Path $FixtureRoot 'release-env')) }
    @{ File='test-runbook-command-contracts.py'; Arguments=@('--case', 'migration-gate', '--fixture-root', (Join-Path $FixtureRoot 'migration-gate')) }
    @{ File='test-runbook-repair-contracts.py'; Arguments=@('--case', 'kinds', '--fixture-root', (Join-Path $FixtureRoot 'repair-kinds')) }
    @{ File='test-runbook-repair-contracts.py'; Arguments=@('--case', 'exit-codes', '--fixture-root', (Join-Path $FixtureRoot 'repair-exits')) }
    @{ File='test-runbook-maintenance-contracts.py'; Arguments=@('--case', 'blocked-event') }
    @{ File='test-runbook-maintenance-contracts.py'; Arguments=@('--case', 'shadow-order') }
)
$previousEnvironment = @{}
foreach ($name in @('PATH', 'TEST_BASH', 'RUNBOOK_TEST_BASH', 'PYTHONOPTIMIZE')) {
    $previousEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
Push-Location $projectRoot
try {
    $env:PATH = (Split-Path -Parent $bashPath) + [IO.Path]::PathSeparator + $env:PATH
    $env:TEST_BASH = $bashPath
    $env:RUNBOOK_TEST_BASH = $bashPath
    [Environment]::SetEnvironmentVariable('PYTHONOPTIMIZE', $null, 'Process')
    foreach ($check in $checks) {
        $path = Join-Path $PSScriptRoot $check.File
        $arguments = $check.Arguments
        $started = [DateTimeOffset]::UtcNow
        Write-Host "AUDIT-START $($started.ToString('o')) $($check.File) $($arguments -join ' ')"
        if ($path.EndsWith('.ps1', [StringComparison]::Ordinal)) {
            & $pwsh -NoProfile -NonInteractive -File $path @arguments
        } else {
            & $pythonPath -X utf8 $path @arguments
        }
        $code = $LASTEXITCODE
        Write-Host "AUDIT-END $([DateTimeOffset]::UtcNow.ToString('o')) exit=$code $($check.File)"
        if ($code -ne 0) { throw "full-audit child failed: $($check.File) (exit $code)" }
    }
} finally {
    Pop-Location
    foreach ($name in $previousEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $previousEnvironment[$name], 'Process')
    }
}
Write-Host 'All full-audit regression gates passed.'
