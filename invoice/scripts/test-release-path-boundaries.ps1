[CmdletBinding()]
param(
    [string]$FixtureRoot = (Join-Path ([IO.Path]::GetTempPath()) 'invoice-release-path-tests'),
    [string]$CaseId = '*',
    [string]$ScriptsRoot = $PSScriptRoot
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$runRoot = Join-Path $FixtureRoot ([guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $runRoot
. (Join-Path $ScriptsRoot 'release-image-gate-lib.ps1')
$failures = [Collections.Generic.List[string]]::new()
$cases = 0
foreach ($entry in @('helper','producer','verifier')) {
    $modes = @('normal','parent','grandparent','leaf')
    if ($entry -eq 'producer') { $modes += @('parent-missing','nonempty') }
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
                $null = New-Item -ItemType Junction -Path $releaseParent -Target $outside
                if ($mode -eq 'parent-missing') { $ReleaseDirectory = Join-Path $releaseParent 'missing/deeper/candidate' }
            } elseif ($mode -eq 'grandparent') {
                $alias = Join-Path $caseRoot 'alias'
                $null = New-Item -ItemType Junction -Path $alias -Target $outside
                $projectRoot = Join-Path $alias 'project'
                $releaseParent = Join-Path $projectRoot 'release'
                $ReleaseDirectory = Join-Path $releaseParent 'candidate'
                $null = New-Item -ItemType Directory -Path $releaseParent
            } else {
                $null = New-Item -ItemType Directory -Path $releaseParent
            }
            if ($mode -eq 'leaf') {
                $null = New-Item -ItemType Junction -Path $ReleaseDirectory -Target $outside
            } elseif ($entry -ne 'producer' -or $mode -eq 'nonempty') {
                $null = New-Item -ItemType Directory -Path $ReleaseDirectory
            }
            if ($mode -eq 'nonempty') {
                Set-Content -LiteralPath (Join-Path $ReleaseDirectory 'kept.txt') -Value 'preserve fixture data'
            }
            $before = @(Get-ChildItem -LiteralPath $outside -Recurse -Force | ForEach-Object FullName)
            $ReleaseName = 'fixture'
            $IdPMode = 'none'
            $RequireTransferReady = $false
            $caught = $null
            try {
                if ($entry -eq 'helper') {
                    Assert-NoReleaseReparsePoints -ReleaseDirectory $ReleaseDirectory | Out-Null
                } else {
                    $name = if ($entry -eq 'producer') { 'release-image-gate.ps1' } else { 'verify-release-image-artifacts.ps1' }
                    $source = Get-Content -Raw -LiteralPath (Join-Path $ScriptsRoot $name)
                    $startMarker = if ($entry -eq 'producer') { 'if ([string]::IsNullOrWhiteSpace($ReleaseDirectory)) {' } else { 'if (-not [IO.Path]::IsPathFullyQualified($ReleaseDirectory)) {' }
                    $endMarker = if ($entry -eq 'producer') { '$lockPath = ' } else { 'Assert-Sha256Sums -ReleaseDirectory' }
                    $start = $source.IndexOf($startMarker, [StringComparison]::Ordinal)
                    $end = $source.IndexOf($endMarker, $start, [StringComparison]::Ordinal)
                    if ($start -lt 0 -or $end -le $start) { throw 'unable to isolate production directory boundary' }
                    # Exact production directory block: stop before lock/Docker
                    # for producer and before artifact reads for verifier.
                    & ([scriptblock]::Create($source.Substring($start, $end - $start)))
                }
            } catch { $caught = $_ }
            $after = @(Get-ChildItem -LiteralPath $outside -Recurse -Force | ForEach-Object FullName)
            $outsideChanged = ($before -join "`n") -cne ($after -join "`n")
            if ($mode -eq 'normal') {
                if ($null -ne $caught) { throw "ordinary release directory rejected: $($caught.Exception.Message)" }
                if ($entry -eq 'producer' -and -not (Test-Path -LiteralPath (Join-Path $ReleaseDirectory 'logs') -PathType Container)) {
                    throw 'ordinary release output was not created'
                }
            } elseif ($mode -eq 'nonempty') {
                if ($null -eq $caught -or $caught.Exception.Message -notmatch 'non-empty release directory' -or
                    (Get-Content -Raw -LiteralPath (Join-Path $ReleaseDirectory 'kept.txt')).Trim() -cne 'preserve fixture data' -or
                    @(Get-ChildItem -LiteralPath $ReleaseDirectory -Force).Count -ne 1) {
                    throw 'nonempty release directory was not rejected intact before writes'
                }
            } elseif ($null -eq $caught -or $caught.Exception.Message -notmatch 'symlink/reparse point' -or $outsideChanged) {
                throw "unsafe release path was not rejected before writes: entry=$entry mode=$mode caught=$($null -ne $caught) outsideChanged=$outsideChanged"
            }
            Write-Host "PASS $id"
        } catch {
            $failures.Add("${id}: $($_.Exception.Message)")
            Write-Host "FAIL ${id}: $($_.Exception.Message)"
        }
    }
}
if ($cases -eq 0) { throw "no matching case: $CaseId" }
if ($failures.Count -gt 0) { throw "Release path boundary failures:`n$($failures -join "`n")" }
Write-Host "Release path boundary cases passed: $cases; retained fixtures: $runRoot"
$global:LASTEXITCODE = 0
