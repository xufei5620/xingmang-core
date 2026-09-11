[CmdletBinding()]
param(
    [string]$FixtureRoot = (Join-Path ([IO.Path]::GetTempPath()) 'invoice-release-path-tests'),
    [string]$CaseId = '*',
    [string]$ScriptsRoot = $PSScriptRoot,
    [string]$UnifiedScriptsRoot = (Join-Path (Split-Path -Parent (Split-Path -Parent $ScriptsRoot)) 'scripts')
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$runRoot = Join-Path $FixtureRoot ([guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $runRoot
. (Join-Path $ScriptsRoot 'release-image-gate-lib.ps1')
$pwsh = (Get-Process -Id $PID).Path
$failures = [Collections.Generic.List[string]]::new()
$cases = 0

function New-RedirectDirectory([string]$Path, [string]$Target) {
    $kind = if ($IsWindows) { 'Junction' } else { 'SymbolicLink' }
    New-Item -ItemType $kind -Path $Path -Target $Target | Out-Null
}

function Get-FixtureInventory([string]$Root) {
    $records = @(Get-ChildItem -LiteralPath $Root -Recurse -Force | Sort-Object FullName | ForEach-Object {
        $hash = ''
        if (-not $_.PSIsContainer -and -not ($_.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
            $hash = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash
        }
        [pscustomobject]@{ Path=$_.FullName; Attributes=[string]$_.Attributes; Hash=$hash }
    })
    return ConvertTo-Json -InputObject $records -Depth 4 -Compress
}

function Invoke-RetiredEntry([string]$Path, [string]$ReleaseDirectory) {
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $pwsh
    $start.UseShellExecute = $false
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($argument in @('-NoProfile','-NonInteractive','-File',$Path,'-ReleaseDirectory',$ReleaseDirectory,'-ReleaseName','fixture')) {
        $start.ArgumentList.Add($argument)
    }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $start
    $process.Start() | Out-Null
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()
    $process.WaitForExit()
    $stdout.GetAwaiter().GetResult() | Out-Null
    $stderr.GetAwaiter().GetResult() | Out-Null
    return $process.ExitCode
}

foreach ($entry in @('helper','producer','verifier')) {
    $modes = @('normal','parent','grandparent','leaf')
    if ($entry -ne 'helper') { $modes += @('parent-missing','nonempty') }
    foreach ($mode in $modes) {
        $id = "$entry-$mode"
        if ($CaseId -ne '*' -and $CaseId -cne $id) { continue }
        $cases++
        try {
            $caseRoot = Join-Path $runRoot $id
            $projectRoot = Join-Path $caseRoot 'project'
            $outside = Join-Path $caseRoot 'outside'
            $null = New-Item -ItemType Directory -Path $projectRoot,$outside
            $releaseParent = Join-Path $projectRoot 'release'
            $ReleaseDirectory = Join-Path $releaseParent 'candidate'
            if ($mode -in @('parent','parent-missing')) {
                New-RedirectDirectory $releaseParent $outside
                if ($mode -eq 'parent-missing') { $ReleaseDirectory = Join-Path $releaseParent 'missing/deeper/candidate' }
            } elseif ($mode -eq 'grandparent') {
                $alias = Join-Path $caseRoot 'alias'
                New-RedirectDirectory $alias $outside
                $projectRoot = Join-Path $alias 'project'
                $releaseParent = Join-Path $projectRoot 'release'
                $ReleaseDirectory = Join-Path $releaseParent 'candidate'
                $null = New-Item -ItemType Directory -Path $releaseParent
            } else {
                $null = New-Item -ItemType Directory -Path $releaseParent
            }
            if ($mode -eq 'leaf') {
                New-RedirectDirectory $ReleaseDirectory $outside
            } elseif (($entry -ne 'producer' -and $mode -ne 'parent-missing') -or $mode -eq 'nonempty') {
                $null = New-Item -ItemType Directory -Path $ReleaseDirectory
            }
            if ($mode -eq 'nonempty') {
                Set-Content -LiteralPath (Join-Path $ReleaseDirectory 'kept.txt') -Value 'preserve fixture data'
            }
            $before = Get-FixtureInventory $caseRoot
            if ($entry -eq 'helper') {
                $caught = $null
                try { Assert-NoReleaseReparsePoints -ReleaseDirectory $ReleaseDirectory | Out-Null } catch { $caught = $_ }
                if ($mode -eq 'normal') {
                    if ($null -ne $caught) { throw "ordinary release directory rejected: $($caught.Exception.Message)" }
                } elseif ($null -eq $caught -or $caught.Exception.Message -notmatch 'symlink/reparse point') {
                    throw "unsafe helper path was not rejected: $mode"
                }
            } else {
                $name = if ($entry -eq 'producer') { 'release-image-gate.ps1' } else { 'verify-release-image-artifacts.ps1' }
                $code = Invoke-RetiredEntry (Join-Path $ScriptsRoot $name) $ReleaseDirectory
                if ($code -ne 64) { throw "retired entry must actually return 64 for every input; got $code" }
            }
            if ((Get-FixtureInventory $caseRoot) -cne $before) { throw 'entry changed fixture/output files before refusal' }
            Write-Host "PASS $id"
        } catch {
            $failures.Add("${id}: $($_.Exception.Message)")
            Write-Host "FAIL ${id}: $($_.Exception.Message)"
        }
    }
}
if ($cases -eq 0) { throw "no matching case: $CaseId" }
if ($failures.Count -gt 0) { throw "Release path boundary failures:`n$($failures -join "`n")" }
Write-Host "Legacy helper/retired entry cases passed: $cases; retained fixtures: $runRoot"

# The active build/verify/artifact consumers must retain the same refusal using
# real OS junctions/symlinks. Never replace this with source-string inspection.
$python = (Get-Command python -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
& $python -X utf8 (Join-Path $UnifiedScriptsRoot 'tests/test_unified_paths.py')
if ($LASTEXITCODE -ne 0) { throw 'unified build/verify/artifact path boundary tests failed' }
$global:LASTEXITCODE = 0
