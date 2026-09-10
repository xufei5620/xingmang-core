[CmdletBinding()]
param(
    [string]$ProvenanceLibrary=(Join-Path $PSScriptRoot 'release-image-gate-lib.ps1'),
    [string]$UpstreamScript=(Join-Path $PSScriptRoot 'check-upstream-integrity.ps1'),
    [ValidateSet('All','invoice','platform','pinned')][string]$Case='All',
    [string]$FixtureRoot=(Join-Path ([IO.Path]::GetTempPath()) ('invoice-git-state-'+[guid]::NewGuid().ToString('N')))
)
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
. $ProvenanceLibrary
$null=New-Item -ItemType Directory -Path $FixtureRoot -ErrorAction Stop
function Invoke-TestGit([string[]]$Arguments){
    $result=@(& git -C $FixtureRoot @Arguments 2>&1)
    if($LASTEXITCODE -ne 0){throw ('Fixture Git failed: '+($result -join ' '))}
    return ($result -join "`n").Trim()
}
$null=New-Item -ItemType Directory -Path (Join-Path $FixtureRoot 'invoice'),(Join-Path $FixtureRoot 'platform')
[IO.File]::WriteAllText((Join-Path $FixtureRoot 'invoice/tracked.txt'),'tracked fixture')
[IO.File]::WriteAllText((Join-Path $FixtureRoot 'platform/tracked.txt'),'tracked fixture')
$null=Invoke-TestGit @('init','--quiet','--template=')
$null=Invoke-TestGit @('add','--','invoice','platform')
$null=Invoke-TestGit @('-c','user.name=Fixture','-c','user.email=fixture@example.invalid','-c','commit.gpgSign=false','-c','core.hooksPath=NUL','commit','--quiet','-m','fixture')
$null=Invoke-TestGit @('config','--local','status.showUntrackedFiles','no')
$expectedHead=Invoke-TestGit @('rev-parse','HEAD')
$failed=[Collections.Generic.List[string]]::new()
if($Case -in @('All','platform')){
    [IO.File]::WriteAllText((Join-Path $FixtureRoot 'platform/untracked.txt'),'untracked platform fixture')
    $state=Get-ReleaseGitProvenance -RepositoryRoot (Join-Path $FixtureRoot 'invoice')
    if($state.GitHead -cne $expectedHead -or $state.GitDirty){$failed.Add('platform: unrelated untracked changes affected invoice provenance')}
    else {Write-Host 'PASS platform: unrelated untracked changes remain outside invoice scope'}
}
if($Case -in @('All','invoice')){
    [IO.File]::WriteAllText((Join-Path $FixtureRoot 'invoice/untracked.txt'),'untracked invoice fixture')
    $state=Get-ReleaseGitProvenance -RepositoryRoot (Join-Path $FixtureRoot 'invoice')
    if($state.GitHead -cne $expectedHead -or -not $state.GitDirty){$failed.Add('invoice: hidden-untracked configuration suppressed dirty source')}
    else {Write-Host 'PASS invoice: untracked source detected despite local Git configuration'}
}
if($Case -in @('All','pinned')){
    [IO.File]::WriteAllText((Join-Path $FixtureRoot 'pinned-untracked.txt'),'untracked pin fixture')
    $tokens=$null;$errors=$null
    $ast=[Management.Automation.Language.Parser]::ParseFile($UpstreamScript,[ref]$tokens,[ref]$errors)
    if($errors.Count){throw 'Upstream script syntax error'}
    foreach($definition in $ast.EndBlock.Statements | Where-Object {$_ -is [Management.Automation.Language.FunctionDefinitionAst]}){
        . ([scriptblock]::Create($definition.Extent.Text))
    }
    $loop=@($ast.EndBlock.Statements | Where-Object {$_ -is [Management.Automation.Language.ForEachStatementAst] -and $_.Variable.VariablePath.UserPath -ceq 'upstream'})
    if($loop.Count -ne 1){throw 'Expected one actual upstream-check loop'}
    $upstreams=@(@{Name='synthetic-pinned';Path=$FixtureRoot;Head=$expectedHead})
    $caught=$null
    try { & ([scriptblock]::Create($loop[0].Extent.Text)) } catch {$caught=$_}
    if($null -eq $caught -or $caught.Exception.Message -notmatch 'working tree is not clean'){$failed.Add('pinned: untracked source was not rejected by the actual pin check')}
    else {Write-Host 'PASS pinned: untracked source rejected by the actual pin check'}
}
if($failed.Count){throw ($failed -join "`n")}
Write-Host 'Configured untracked-file checks passed.'
$global:LASTEXITCODE=0
