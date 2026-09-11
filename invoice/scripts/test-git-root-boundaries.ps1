[CmdletBinding()]
param(
    [string]$FixtureRoot = (Join-Path ([IO.Path]::GetTempPath()) 'invoice-git-root-tests'),
    [string]$CaseId = '*',
    [string]$SourceRoot = (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)),
    [string]$Bash = 'D:\Git\bin\bash.exe'
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$runRoot = Join-Path $FixtureRoot ([guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $runRoot
$failures = [Collections.Generic.List[string]]::new()
$cases = 0
function Invoke-GitFixture {
    param([string]$Path, [string[]]$Arguments)
    $lines = @(& git -C $Path @Arguments 2>&1)
    if ($LASTEXITCODE -ne 0) { throw "fixture Git failed: $($Arguments -join ' ')" }
    return ($lines -join "`n").Trim()
}
function New-RootFixture {
    param([string]$Name)
    $root = Join-Path $runRoot $Name
    $null = New-Item -ItemType Directory -Path (Join-Path $root 'nested')
    Set-Content -LiteralPath (Join-Path $root 'nested/kept.txt') -Value 'fixture'
    $null = Invoke-GitFixture $root @('init', '--initial-branch=main')
    $null = Invoke-GitFixture $root @('add', '--', '.')
    $null = Invoke-GitFixture $root @('-c','user.name=Fixture','-c','user.email=fixture@example.invalid','-c','commit.gpgsign=false','-c','core.hooksPath=','commit','-m','fixture')
    return $root
}
function Test-Case {
    param([string]$Id, [scriptblock]$Action)
    if ($CaseId -ne '*' -and $Id -cne $CaseId) { return }
    $script:cases++
    try { & $Action; Write-Host "PASS $Id" }
    catch { $script:failures.Add("${Id}: $($_.Exception.Message)"); Write-Host "FAIL ${Id}: $($_.Exception.Message)" }
}
function Assert-ProcessResult {
    param([string]$Id, [int]$Exit, [object[]]$Output, [bool]$Accepted)
    if ($Accepted) {
        if ($Exit -ne 0) { throw "legal Git root rejected ($Id): $($Output -join ' ')" }
    } elseif ($Exit -eq 0 -or ($Output -join ' ') -notmatch 'actual Git top-level') {
        throw "internal directory was not rejected by Git root guard ($Id), exit=${Exit}: $($Output -join ' ')"
    }
}
$pinPaths = @('sub2api-upstream-v0.1.157','_research/sub2api-contract-v0.1.179','_research/new-api-agent','_research/new-api-local')
foreach ($mode in @('root','linked','internal','junction-root','junction-internal','parent-junction-root')) {
    Test-Case "pinned-$mode" {
        $root = New-RootFixture "pinned-$mode-repo"
        $head = Invoke-GitFixture $root @('rev-parse','HEAD')
        $pinned = Join-Path $runRoot "pinned-$mode"
        $null = New-Item -ItemType Directory -Path (Join-Path $pinned '_research')
        foreach ($relative in $pinPaths) {
            $destination = Join-Path $pinned $relative
            if ($mode -in @('root','parent-junction-root')) {
                $null = Invoke-GitFixture $runRoot @('clone','--local','--no-hardlinks',$root,$destination)
            } elseif ($mode -eq 'linked') {
                $null = Invoke-GitFixture $root @('worktree','add','--detach',$destination,'HEAD')
            } elseif ($mode -eq 'internal') {
                $null = New-Item -ItemType Directory -Path $destination
            } else {
                $target = if ($mode -eq 'junction-root') { $root } else { Join-Path $root 'nested' }
                $null = New-Item -ItemType Junction -Path $destination -Target $target
            }
        }
        if ($mode -eq 'internal') {
            $null = Invoke-GitFixture $pinned @('init','--initial-branch=main')
            # An empty commit keeps the four ordinary internal directories clean.
            $null = Invoke-GitFixture $pinned @('-c','user.name=Fixture','-c','user.email=fixture@example.invalid','-c','commit.gpgsign=false','-c','core.hooksPath=','commit','--allow-empty','-m','fixture')
            $head = Invoke-GitFixture $pinned @('rev-parse','HEAD')
        }
        if ($mode -eq 'parent-junction-root') {
            $alias = Join-Path $runRoot 'pinned-parent-alias'
            $null = New-Item -ItemType Junction -Path $alias -Target $pinned
            $pinned = $alias
        }
        # Execute the full production gate with only the immutable pin identities
        # replaced by synthetic local commits; never access actual upstream trees.
        $source = Get-Content -Raw -LiteralPath (Join-Path $SourceRoot 'invoice/scripts/check-upstream-integrity.ps1')
        $source = [regex]::Replace($source, "Head = '[0-9a-f]{40}'", "Head = '$head'")
        $scriptPath = Join-Path $runRoot "pinned-$mode.ps1"
        [IO.File]::WriteAllText($scriptPath, $source)
        $previous = $env:INVOICE_UPSTREAM_ROOT
        try {
            $env:INVOICE_UPSTREAM_ROOT = $pinned
            $output = @(& pwsh -NoProfile -File $scriptPath 2>&1)
            $exit = $LASTEXITCODE
        } finally { $env:INVOICE_UPSTREAM_ROOT = $previous }
        Assert-ProcessResult "pinned-$mode" $exit $output ($mode -notin @('internal','junction-internal'))
    }
}
foreach ($entry in @('configure-remotes','mirror-github','deploy','promote')) {
    foreach ($mode in @('root','linked','internal')) {
        Test-Case "$entry-$mode" {
            $root = New-RootFixture "$entry-$mode-repo"
            $inputRoot = $root
            if ($mode -eq 'internal') { $inputRoot = Join-Path $root 'nested' }
            if ($mode -eq 'linked') {
                $inputRoot = Join-Path $runRoot "$entry-linked-tree"
                $null = Invoke-GitFixture $root @('worktree','add','--detach',$inputRoot,'HEAD')
            }
            $source = (Get-Content -Raw -LiteralPath (Join-Path $SourceRoot "platform/deploy/scripts/$entry.sh")).Replace("`r`n","`n")
            $variable = if ($entry -eq 'promote') { 'checkout_path' } else { 'repo_path' }
            $startMarker = '[ -d "$' + $variable + '" ] &&'
            $start = $source.IndexOf($startMarker, [StringComparison]::Ordinal)
            $endMarker = switch ($entry) {
                'configure-remotes' { 'validate_remote_url server-url' }
                'mirror-github' { 'remote_url=' }
                default { 'git -C "$' + $variable + '" rev-parse --is-shallow-repository' }
            }
            $end = $source.IndexOf($endMarker, $start, [StringComparison]::Ordinal)
            if ($start -lt 0 -or $end -le $start) { throw "cannot isolate actual $entry input guard" }
            # This exact contiguous production block stops before any remote,
            # secret, deployment, audit-log or other write operations.
            $block = $source.Substring($start, $end - $start)
            $unixPath = (& $Bash -c 'cygpath -u "$1"' -- $inputRoot).Trim()
            $harness = "set -Eeuo pipefail`ndie() { echo `"`$*`" >&2; return 1; }`n$variable='$unixPath'`ngit_bin=git`n" + $block
            $scriptPath = Join-Path $runRoot "$entry-$mode.sh"
            [IO.File]::WriteAllText($scriptPath, $harness)
            $output = @(& $Bash $scriptPath 2>&1)
            $exit = $LASTEXITCODE
            Assert-ProcessResult "$entry-$mode" $exit $output ($mode -ne 'internal')
        }
    }
}
if ($cases -eq 0) { throw "no matching case: $CaseId" }
if ($failures.Count -gt 0) { throw "Git root boundary failures:`n$($failures -join "`n")" }
Write-Host "Git root boundary cases passed: $cases; retained fixtures: $runRoot"
$global:LASTEXITCODE = 0
