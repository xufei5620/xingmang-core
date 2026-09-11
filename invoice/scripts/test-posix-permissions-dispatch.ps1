$ErrorActionPreference = 'Stop'
$helperSource = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'test-posix-permissions.ps1') -Raw
$tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$fixture = Join-Path $tempRoot ('invoice permission dispatcher ' + [guid]::NewGuid().ToString('N'))
$fixture = [IO.Path]::GetFullPath($fixture)
if (-not $fixture.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase)) { throw 'fixture escaped temp root' }
$null = New-Item -ItemType Directory -Path $fixture
$helper = Join-Path $fixture 'dispatcher.ps1'
$targets = @('scripts/test-preserve-source-reader-roles.sh', 'deploy/keycloak/verify-permanent-master-admin-maintenance.sh')
$dispatchCalls = [Collections.Generic.List[object]]::new()
$dispatchExit = 0
function wsl.exe {
    $dispatchCalls.Add([pscustomobject]@{ Tool = 'wsl.exe'; Arguments = @($args) })
    $global:LASTEXITCODE = $dispatchExit
}
function bash {
    $dispatchCalls.Add([pscustomobject]@{ Tool = 'bash'; Arguments = @($args) })
    $global:LASTEXITCODE = $dispatchExit
}
function Assert-Route([string]$Tool, [string[]]$Arguments) {
    if ($dispatchCalls.Count -ne 1) { throw 'dispatcher must invoke exactly one synthetic child' }
    $call = $dispatchCalls[0]
    if ($call.Tool -ne $Tool -or ($call.Arguments -join "`n") -cne ($Arguments -join "`n")) {
        throw "dispatcher selected incorrect $Tool arguments"
    }
}
function Expect-Rejected([scriptblock]$Action, [string]$Pattern) {
    $caught = $false
    try { & $Action } catch {
        if ($_.Exception.Message -notmatch $Pattern) { throw }
        $caught = $true
    }
    if (-not $caught) { throw 'dispatcher swallowed a rejection' }
}
try {
    foreach ($target in @($targets) + @('scripts/forbidden-maintenance.sh')) {
        $path = Join-Path $fixture $target
        $null = New-Item -ItemType Directory -Path (Split-Path -Parent $path) -Force
        [IO.File]::WriteAllText($path, '# inert fixture; never executed')
    }
    # Windows exercises both branches; on Linux the native Bash route applies.
    $modes = if ($IsWindows) { @($true, $false) } else { @($false) }
    foreach ($windowsRoute in $modes) {
        $platformLiteral = if ($windowsRoute) { '$true' } else { '$false' }
        [IO.File]::WriteAllText($helper, $helperSource.Replace('if ($IsWindows)', "if ($platformLiteral)"))
        foreach ($target in $targets) {
            $dispatchCalls.Clear()
            $dispatchExit = 0
            & $helper -RelativeTestPath $target -ProjectRoot $fixture
            $native = [IO.Path]::GetFullPath((Join-Path $fixture $target))
            if ($windowsRoute) {
                $path = $native.Replace('\', '/')
                $linux = '/mnt/' + $path.Substring(0, 1).ToLowerInvariant() + $path.Substring(2)
                $expected = @('--exec', 'env', 'TMPDIR=/tmp', 'bash', $linux)
                if ($target -eq $targets[1]) { $expected = @('--user', 'root') + $expected }
                Assert-Route 'wsl.exe' $expected
            } else {
                Assert-Route 'bash' @($native)
            }
            foreach ($nativeExit in @(23, -1073741819)) {
                $dispatchCalls.Clear()
                $dispatchExit = $nativeExit
                Expect-Rejected { & $helper -RelativeTestPath $target -ProjectRoot $fixture } "POSIX permission fixture failed:.*exit $nativeExit"
                if ($dispatchCalls.Count -ne 1) { throw 'failure case did not reach the synthetic child' }
            }
        }
        $dispatchExit = 0
        $dispatchCalls.Clear()
        Expect-Rejected { & $helper -RelativeTestPath 'scripts/forbidden-maintenance.sh' -ProjectRoot $fixture } 'validate|validation|验证'
        if ($dispatchCalls.Count -ne 0) { throw 'unapproved fixture invoked a child' }
        Expect-Rejected { & $helper -RelativeTestPath $targets[0] -ProjectRoot (Join-Path $fixture 'missing') } 'fixture is missing'
        if ($dispatchCalls.Count -ne 0) { throw 'missing fixture invoked a child' }
    }
} finally {
    $resolvedFixture = [IO.Path]::GetFullPath($fixture)
    if (-not $resolvedFixture.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -or
        [IO.Path]::GetFileName($resolvedFixture) -notlike 'invoice permission dispatcher *') {
        throw 'refusing to remove an unexpected dispatcher fixture'
    }
    Remove-Item -LiteralPath $resolvedFixture -Recurse -Force
}
$global:LASTEXITCODE = 0
Write-Host 'POSIX permission dispatcher allowlist, platform routes and child failure tests passed.'
