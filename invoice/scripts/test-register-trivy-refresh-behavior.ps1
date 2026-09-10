[CmdletBinding()]
param(
    [string]$Case = 'wiring',
    [string]$SourceDirectory = $PSScriptRoot,
    [string]$FixtureRoot = (Join-Path ([IO.Path]::GetTempPath()) ('invoice-trivy-registration-' + [Guid]::NewGuid().ToString('N')))
)
$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest

# These constructors and service boundaries are local fakes. No scheduler API,
# refresh script, Docker or network process can be reached by this test.
function New-ScheduledTaskAction { [CmdletBinding()] param($Execute,$Argument,$WorkingDirectory) [pscustomobject]@{Execute=$Execute;Arguments=$Argument;WorkingDirectory=$WorkingDirectory} }
function New-ScheduledTaskTrigger { [CmdletBinding()] param([switch]$Daily,[datetime]$At) [pscustomobject]@{CimClass=[pscustomobject]@{CimClassName='MSFT_TaskDailyTrigger'};DaysInterval=1;Enabled=$true;StartBoundary=([DateTimeOffset]$At).ToUniversalTime().ToString('o')} }
function New-ScheduledTaskSettingsSet { [CmdletBinding()] param([switch]$StartWhenAvailable,[int]$RestartCount,[TimeSpan]$RestartInterval) [pscustomobject]@{StartWhenAvailable=[bool]$StartWhenAvailable;RestartCount=$RestartCount;RestartInterval=[Xml.XmlConvert]::ToString($RestartInterval)} }
function New-ScheduledTaskPrincipal { [CmdletBinding()] param($UserId,$LogonType,$RunLevel) [pscustomobject]@{UserId=$UserId;LogonType=$LogonType;RunLevel=$RunLevel} }
function Unregister-ScheduledTask { throw 'registration-wiring: unregistration forbidden' }
function Start-ScheduledTask { throw 'registration-wiring: triggering forbidden' }
function docker { throw 'registration-wiring: Docker forbidden' }
function curl.exe { throw 'registration-wiring: network forbidden' }
if($Case -eq 'constructors'){return}
if($Case -eq 'legacy-suite'){
    & (Join-Path $SourceDirectory 'test-register-trivy-refresh-task.ps1')
    return
}
[IO.Directory]::CreateDirectory($FixtureRoot)|Out-Null
if($Case -eq 'meta'){
    $mutations=@(
        @('branch','if ($null -eq $existingTask) {','if ($null -ne $existingTask) {'),
        @('whatif',"if (`$PSCmdlet.ShouldProcess(`$taskFullName, 'Register daily Trivy cache refresh Scheduled Task')) {",'if ($true) {'),
        @('invoke','$definition = New-InvoiceTrivyRefreshTaskDefinition',('& $refreshScriptPath' + "`n" + '$definition = New-InvoiceTrivyRefreshTaskDefinition'))
    )
    $survivors=@()
    foreach($mutation in $mutations){
        $dir=Join-Path $FixtureRoot $mutation[0];[IO.Directory]::CreateDirectory($dir)|Out-Null
        foreach($file in @('register-trivy-refresh-task.ps1','register-trivy-refresh-task-lib.ps1','test-register-trivy-refresh-task.ps1','test-register-trivy-refresh-behavior.ps1')){Copy-Item -LiteralPath (Join-Path $SourceDirectory $file) -Destination (Join-Path $dir $file)}
        $path=Join-Path $dir 'register-trivy-refresh-task.ps1';$text=[IO.File]::ReadAllText($path)
        if(-not $text.Contains($mutation[1])){throw 'registration-meta: mutation target missing'}
        [IO.File]::WriteAllText($path,$text.Replace($mutation[1],$mutation[2]))
        $output=& pwsh -NoProfile -NonInteractive -File (Join-Path $dir 'test-register-trivy-refresh-behavior.ps1') -Case legacy-suite -SourceDirectory $dir -FixtureRoot (Join-Path $dir 'fixture') 2>&1
        $code=$LASTEXITCODE;($output|Out-String)|Set-Content -LiteralPath (Join-Path $dir 'test.log')
        if($code -eq 0){$survivors+=$mutation[0]}
        elseif(($output|Out-String) -notmatch 'registration-wiring:'){throw "registration-meta: $($mutation[0]) failed outside the intended behavior assertions"}
    }
    if($survivors.Count){throw "registration-meta: existing tests missed behavioral mutants: $($survivors -join ',')"}
    Write-Host 'REGISTRATION-META-PASS';return
}
if($Case -eq 'lookup'){
    foreach($scenario in @('denied','unavailable','unexpected-notfound','terminating','notfound','register-fails','update-fails')){
        & {
            param($scenario,$wrapper,$root)
            $calls=[Collections.Generic.List[string]]::new()
            function Get-ScheduledTask {
                [CmdletBinding()] param($TaskName,$TaskPath)
                switch($scenario){
                    'denied'{Write-Error 'fixture lookup denied' -Category PermissionDenied;return}
                    'unavailable'{Write-Error 'fixture lookup unavailable' -Category ResourceUnavailable;return}
                    'unexpected-notfound'{Write-Error 'fixture unrelated missing resource' -Category ObjectNotFound -ErrorId UnrelatedResource;return}
                    'terminating'{throw 'fixture provider failure'}
                    'notfound'{Write-Error 'fixture task does not exist' -Category ObjectNotFound -ErrorId CmdletizationQuery_NotFound_TaskName;return}
                    'update-fails'{[pscustomobject]@{TaskName=$TaskName;TaskPath=$TaskPath};return}
                }
            }
            function Register-ScheduledTask {
                [CmdletBinding()] param($TaskName,$TaskPath,$Action,$Trigger,$Settings,$Principal,$Description,[switch]$Force)
                $calls.Add('Register')
                if($Force){throw 'registration-lookup: creation must not force overwrite'}
                if($scenario -eq 'register-fails'){Write-Error 'fixture register failed' -Category WriteError}
            }
            function Set-ScheduledTask {
                [CmdletBinding()] param($TaskName,$TaskPath,$Action,$Trigger,$Settings,$Principal)
                $calls.Add('Set');Write-Error 'fixture update failed' -Category WriteError
            }
            $scripts=Join-Path $root 'scripts';[IO.Directory]::CreateDirectory($scripts)|Out-Null
            [IO.File]::WriteAllText((Join-Path $scripts 'refresh-trivy-cache.ps1'),"throw 'registration-lookup: refresh invocation forbidden'")
            $failure=$null
            try{& $wrapper -RepoRoot $root -UserId 'fixture\operator' -Confirm:$false | Out-Null}catch{$failure=$_}
            if($scenario -eq 'notfound'){
                if($failure -or ($calls -join ',') -cne 'Register'){throw "registration-lookup: recognized task absence did not create safely: $failure"}
            } elseif($scenario -in @('register-fails','update-fails')){
                if($null -eq $failure -or $calls.Count -ne 1){throw "registration-lookup: $scenario did not propagate the service write failure"}
            } elseif($null -eq $failure -or $calls.Count -ne 0){throw "registration-lookup: $scenario was treated as task absence or caused a write"}
        } $scenario (Join-Path $SourceDirectory 'register-trivy-refresh-task.ps1') (Join-Path $FixtureRoot $scenario)
    }
    Write-Host 'REGISTRATION-BEHAVIOR-PASS: lookup';return
}
if($Case -ne 'wiring'){throw "unknown registration case: $Case"}
$wrapper=Join-Path $SourceDirectory 'register-trivy-refresh-task.ps1'
foreach($scenario in @('absent','existing','whatif-absent','whatif-existing')){
    & {
        param($scenario,$wrapper,$root)
        $calls=[Collections.Generic.List[string]]::new()
        function Get-ScheduledTask { [CmdletBinding()] param($TaskName,$TaskPath) if($scenario -like '*existing'){[pscustomobject]@{TaskName=$TaskName;TaskPath=$TaskPath}} }
        function Register-ScheduledTask { [CmdletBinding()] param($TaskName,$TaskPath,$Action,$Trigger,$Settings,$Principal,$Description,[switch]$Force) $calls.Add('Register') }
        function Set-ScheduledTask { [CmdletBinding()] param($TaskName,$TaskPath,$Action,$Trigger,$Settings,$Principal) $calls.Add('Set') }
        $scripts=Join-Path $root 'scripts';[IO.Directory]::CreateDirectory($scripts)|Out-Null
        [IO.File]::WriteAllText((Join-Path $scripts 'refresh-trivy-cache.ps1'), "throw 'registration-wiring: refresh must never be invoked'")
        $failure=$null
        try {& $wrapper -RepoRoot $root -UserId 'fixture\operator' -WhatIf:($scenario -like 'whatif-*') -Confirm:$false | Out-Null}catch{$failure=$_}
        if($failure){throw "registration-wiring: $scenario failed or invoked refresh: $($failure.Exception.Message)"}
        $expected=if($scenario -like 'whatif-*'){''}elseif($scenario -eq 'absent'){'Register'}else{'Set'}
        if(($calls -join ',') -cne $expected){throw "registration-wiring: $scenario expected '$expected' but observed '$($calls -join ',')'"}
    } $scenario $wrapper (Join-Path $FixtureRoot $scenario)
}
Write-Host 'REGISTRATION-BEHAVIOR-PASS: wiring'
