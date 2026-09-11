[CmdletBinding()]
param([string]$Scanner=(Join-Path $PSScriptRoot 'check-no-secrets.ps1'),[int[]]$ErrorCodes=@(2,-1073741819))
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest
$root=Join-Path ([IO.Path]::GetTempPath()) ('invoice-scanner-exit-'+[guid]::NewGuid().ToString('N'))
$scripts=Join-Path $root 'scripts';$null=New-Item -ItemType Directory -Path $scripts
[IO.File]::WriteAllBytes((Join-Path $scripts 'check-no-secrets.ps1'),[IO.File]::ReadAllBytes($Scanner))
& git -C $root init --quiet --template=
if($LASTEXITCODE -ne 0){throw 'Cannot create isolated scanner fixture'}
[IO.File]::WriteAllText((Join-Path $root 'fixture.txt'),'inert source fixture')
$wrapper=@'
param([string]$Scanner,[int]$Code)
$ErrorActionPreference='Stop'
$global:FixtureExit=$Code
function global:rg {
    & $env:ComSpec /d /c ('exit /b '+$global:FixtureExit)
}
try { & $Scanner } catch { Write-Host $_.Exception.Message; exit 1 }
exit 0
'@
$wrapperPath=Join-Path $root 'native-error-probe.ps1'
[IO.File]::WriteAllText($wrapperPath,$wrapper,[Text.UTF8Encoding]::new($false))
foreach($code in @($ErrorCodes)+@(1)){
    $output=@(& pwsh -NoProfile -File $wrapperPath -Scanner (Join-Path $scripts 'check-no-secrets.ps1') -Code $code 2>&1)
    $exit=$LASTEXITCODE
    if($code -eq 1){
        if($exit -ne 0){throw 'Documented rg no-match exit 1 was rejected'}
    } elseif($exit -ne 1 -or ($output -join "`n") -notmatch 'secret scan failed to execute'){
        throw "Scanner failed to reject native status $code at the scanner error boundary"
    }
    Write-Host "PASS scanner status $code"
}
Write-Host 'Scanner native exit contracts passed.'
$global:LASTEXITCODE=0
