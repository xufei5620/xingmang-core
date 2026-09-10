[CmdletBinding()]
param(
    [string]$LibraryPath = (Join-Path $PSScriptRoot 'release-image-gate-lib.ps1'),
    [string]$CaseId = '*',
    [string]$DockerfilePath = (Join-Path $PSScriptRoot '../deploy/keycloak/Dockerfile')
)
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
. $LibraryPath
$base='quay.io/keycloak/keycloak:26.7.2@sha256:9d1f1b2b7261ff53c66cb1092dfcdc34a5fb77e81f9e6a6e75b8b6a795de8067'
$builder='registry.access.redhat.com/ubi9/openjdk-21@sha256:cc8a30e9181b0135e6657ca3b824d7b32e4c7f6a664769ef641d4f7031339564'
$legacy="FROM $base AS builder"+[char]10+"FROM $base"+[char]10
$rebuilt="FROM $builder AS source-build"+[char]10+$legacy
$failures=[Collections.Generic.List[string]]::new()
function Check-RebuildCase([string]$Id,[scriptblock]$Action){
    if($CaseId -ne '*' -and $CaseId -cne $Id){return}
    try{& $Action;Write-Host "PASS $Id"}catch{$failures.Add($Id+': '+$_.Exception.Message);Write-Host "FAIL $Id"}
}
function Require-Rejected([string]$Text){
    $rejected=$false
    try{Assert-KeycloakDockerfileLiteralBasePins -DockerfileText $Text -ExpectedBaseReference $base|Out-Null}catch{$rejected=$true}
    if(-not $rejected){throw 'Untrusted source-build recipe was accepted.'}
}
Check-RebuildCase 'reviewed-source-stage-accepted' {
    Assert-KeycloakDockerfileLiteralBasePins -DockerfileText $rebuilt -ExpectedBaseReference $base|Out-Null
}
Check-RebuildCase 'legacy-binary-only-layout-rejected' {Require-Rejected $legacy}
Check-RebuildCase 'unreviewed-source-builder-rejected' {Require-Rejected ($rebuilt.Replace($builder,'ubuntu:latest'))}
Check-RebuildCase 'source-builder-digest-drift-rejected' {Require-Rejected ($rebuilt.Replace('cc8a30e9181b0135e6657ca3b824d7b32e4c7f6a664769ef641d4f7031339564',('a'*64)))}
Check-RebuildCase 'source-builder-order-rejected' {Require-Rejected ($legacy+"FROM $builder AS source-build"+[char]10)}
Check-RebuildCase 'runtime-base-drift-rejected' {Require-Rejected ($rebuilt.Replace($base,'quay.io/keycloak/keycloak:26.7.3@sha256:'+('b'*64)))}
Check-RebuildCase 'source-build-libicu-pin-before-maven' {
    $text=[IO.File]::ReadAllText($DockerfilePath).Replace("`r`n","`n").Replace("`r","`n")
    $stages=[regex]::Matches($text,'(?m)^[\t ]*FROM[\t ]+')
    if($stages.Count -lt 2){throw 'Cannot locate the Keycloak source-build stage.'}
    $source=$text.Substring($stages[0].Index,$stages[1].Index-$stages[0].Index)
    $active=(($source -split "`n" | Where-Object {$_ -notmatch '^\s*#'}) -join "`n") -replace '\\\n[\t ]*',' '
    $pin='libicu-67.1-10.el9_6.x86_64'
    $install=[regex]::Match($active,'(?m)^[\t ]*RUN[\t ]+microdnf[\t ]+install[\t ]+-y[\t ]+([^&\r\n]+)')
    if(-not $install.Success -or ($install.Groups[1].Value.Trim() -split '\s+') -cnotcontains $pin){
        throw 'Keycloak source-build must install the exact libicu-67.1-10.el9_6.x86_64 prerequisite.'
    }
    $assertion=[regex]::Match($active,'(?m)(?:^[\t ]*RUN|&&)[\t ]+rpm[\t ]+-q[\t ]+'+[regex]::Escape($pin)+'(?=[\t ]*(?:&&|$))')
    $maven=[regex]::Match($active,'(?m)(?:^[\t ]*RUN|&&)[\t ]+mvn(?:[\t ]|$)')
    if(-not $assertion.Success -or -not $maven.Success -or $assertion.Index -le $install.Index -or $assertion.Index -ge $maven.Index){
        throw 'Keycloak source-build must assert the exact libicu package with rpm -q after installation and before Maven.'
    }
}
if($failures.Count){throw ('Keycloak source-build contracts failed: '+($failures -join '; '))}
Write-Host 'Keycloak source-build stage contracts passed.'
