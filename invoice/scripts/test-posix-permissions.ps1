param(
    [Parameter(Mandatory)]
    [ValidateSet('scripts/test-preserve-source-reader-roles.sh', 'deploy/keycloak/verify-permanent-master-admin-maintenance.sh')]
    [string]$RelativeTestPath,
    [string]$ProjectRoot = (Split-Path -Parent $PSScriptRoot)
)

$ErrorActionPreference = 'Stop'
$testPath = [IO.Path]::GetFullPath((Join-Path $ProjectRoot $RelativeTestPath))
if (-not (Test-Path -LiteralPath $testPath -PathType Leaf)) {
    throw "POSIX permission fixture is missing: $RelativeTestPath"
}
if ($IsWindows) {
    # NTFS/Git Bash cannot supply the Linux owner and mode contracts under test.
    # Only these disposable fixtures run in local WSL; no maintenance entry does.
    $nativePath = $testPath.Replace('\', '/')
    if ($nativePath -notmatch '^([A-Za-z]):(/.+)$') {
        throw 'POSIX permission fixtures require a Windows drive path for WSL'
    }
    $linuxPath = '/mnt/' + $Matches[1].ToLowerInvariant() + $Matches[2]
    $wslArguments = @('--exec', 'env', 'TMPDIR=/tmp', 'bash', $linuxPath)
    if ($RelativeTestPath -eq 'deploy/keycloak/verify-permanent-master-admin-maintenance.sh') {
        $wslArguments = @('--user', 'root') + $wslArguments
    }
    & wsl.exe @wslArguments
} else {
    & bash $testPath
}
$fixtureExitCode = $LASTEXITCODE
if ($fixtureExitCode -ne 0) {
    throw "POSIX permission fixture failed: $RelativeTestPath (exit $fixtureExitCode)"
}
