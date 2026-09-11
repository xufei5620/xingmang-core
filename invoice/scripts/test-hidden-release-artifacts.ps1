[CmdletBinding()]
param([string]$Library=(Join-Path $PSScriptRoot 'release-image-gate-lib.ps1'),[ValidateSet('All','writer','verifier','hidden-directory','visible')][string]$Case='All')
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
. $Library
$root=Join-Path ([IO.Path]::GetTempPath()) ('invoice-hidden-artifact-'+[guid]::NewGuid().ToString('N'))
$null=New-Item -ItemType Directory -Path $root
$visible=Join-Path $root 'visible.txt';$hidden=Join-Path $root '.hidden-proof.json'
[IO.File]::WriteAllText($visible,'visible synthetic artifact')
[IO.File]::WriteAllText($hidden,'synthetic artifact version one')
if($IsWindows){[IO.File]::SetAttributes($hidden,[IO.FileAttributes]::Hidden)}
$null=Write-Sha256Sums -ReleaseDirectory $root
if($Case -in @('All','writer')){
    if(-not [IO.File]::ReadAllText((Join-Path $root 'SHA256SUMS')).Contains('.hidden-proof.json')){throw 'Checksum writer omitted hidden artifact'}
    Write-Host 'PASS checksum writer includes hidden artifact'
}
if($Case -in @('All','verifier')){
    # Supply a complete checksum set independently of the writer under test.
    $sums=(Get-FileSha256Lower $visible)+'  visible.txt'+"`n"+(Get-FileSha256Lower $hidden)+'  .hidden-proof.json'+"`n"
    [IO.File]::WriteAllText((Join-Path $root 'SHA256SUMS'),$sums)
    Assert-Sha256Sums -ReleaseDirectory $root | Out-Null
    $stream=[IO.File]::Open($hidden,[IO.FileMode]::Open,[IO.FileAccess]::Write)
    try{$stream.SetLength(0);$bytes=[Text.Encoding]::UTF8.GetBytes('modified synthetic artifact');$stream.Write($bytes,0,$bytes.Length)}finally{$stream.Dispose()}
    $caught=$null;try{Assert-Sha256Sums -ReleaseDirectory $root | Out-Null}catch{$caught=$_}
    if($null -eq $caught -or $caught.Exception.Message -notmatch 'checksum mismatch'){throw 'Hidden artifact tamper was not rejected'}
    Write-Host 'PASS checksum verifier binds hidden artifact content'
}
if($Case -in @('All','hidden-directory')){
    $hiddenDirectory=Join-Path $root '.hidden-dir';$null=New-Item -ItemType Directory -Path $hiddenDirectory
    [IO.File]::WriteAllText((Join-Path $hiddenDirectory 'nested.txt'),'nested synthetic artifact')
    if($IsWindows){[IO.File]::SetAttributes($hiddenDirectory,[IO.FileAttributes]::Hidden)}
    $null=Write-Sha256Sums -ReleaseDirectory $root
    if(-not [IO.File]::ReadAllText((Join-Path $root 'SHA256SUMS')).Contains('.hidden-dir/nested.txt')){throw 'Checksum writer omitted hidden directory contents'}
    Assert-Sha256Sums -ReleaseDirectory $root | Out-Null
    Write-Host 'PASS nested hidden artifact covered'
}
if($Case -in @('All','visible')){
    $null=Write-Sha256Sums -ReleaseDirectory $root
    [IO.File]::WriteAllText($visible,'changed ordinary fixture')
    $caught=$null;try{Assert-Sha256Sums -ReleaseDirectory $root | Out-Null}catch{$caught=$_}
    if($null -eq $caught -or $caught.Exception.Message -notmatch 'checksum mismatch'){throw 'Visible artifact tamper was not rejected'}
    Write-Host 'PASS visible artifact remains protected'
}
Write-Host 'Hidden release artifact contracts passed.'
$global:LASTEXITCODE=0
