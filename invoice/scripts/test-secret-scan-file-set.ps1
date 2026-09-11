[CmdletBinding()]
param([string]$Scanner=(Join-Path $PSScriptRoot 'check-no-secrets.ps1'),[ValidateSet('All','tracked-ignored','hidden','untracked-ignored','ordinary')][string]$Case='All')
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
$cases=if($Case -eq 'All'){@('ordinary','tracked-ignored','hidden','untracked-ignored')}else{@($Case)}
foreach($item in $cases){
    $root=Join-Path ([IO.Path]::GetTempPath()) ('invoice-scanner-files-'+[guid]::NewGuid().ToString('N'))
    $null=New-Item -ItemType Directory -Path (Join-Path $root 'scripts'),(Join-Path $root 'generated')
    [IO.File]::WriteAllBytes((Join-Path $root 'scripts/check-no-secrets.ps1'),[IO.File]::ReadAllBytes($Scanner))
    [IO.File]::WriteAllText((Join-Path $root '.gitignore'),"generated/`n")
    & git -C $root init --quiet --template=
    if($LASTEXITCODE -ne 0){throw 'Cannot create Git fixture'}
    $name=if($item -in @('tracked-ignored','untracked-ignored')){'generated/中文 marker.txt'}elseif($item -eq 'hidden'){'.hidden-marker.txt'}else{'ordinary.txt'}
    $marker=('github_'+'pat_')+('0'*24)
    [IO.File]::WriteAllText((Join-Path $root $name),$marker,[Text.UTF8Encoding]::new($false))
    if($item -eq 'tracked-ignored'){
        & git -C $root add -f -- $name
        if($LASTEXITCODE -ne 0){throw 'Cannot track ignored fixture file'}
    }
    $output=@(& pwsh -NoProfile -File (Join-Path $root 'scripts/check-no-secrets.ps1') 2>&1)
    $exit=$LASTEXITCODE
    if($item -eq 'untracked-ignored'){
        if($exit -ne 0){throw 'Scanner incorrectly read ignored untracked data outside release file set'}
    } elseif($exit -ne 1 -or ($output -join "`n") -notmatch 'possible secret material detected'){
        throw "Release file-set scan missed $item marker"
    }
    Write-Host "PASS file-set $item"
}
Write-Host 'Exact release file-set checks passed.'
$global:LASTEXITCODE=0
