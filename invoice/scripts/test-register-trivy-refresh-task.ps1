$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# Builder fixtures plus executable behavior behind local scheduler fakes.
# The wrapper is tested against a synthetic refresh sentinel, never the real
# refresh script or the Task Scheduler service.

$scriptsRoot = $PSScriptRoot
. (Join-Path $scriptsRoot 'register-trivy-refresh-task-lib.ps1')

Assert-RegisterTrivyRefreshTaskPowerShellRuntime | Out-Null

function Assert-ThrowsLike {
    param(
        [Parameter(Mandatory)][scriptblock]$Action,
        [Parameter(Mandatory)][string]$ExpectedMessagePattern,
        [Parameter(Mandatory)][string]$FailureMessage
    )

    $caught = $null
    try { & $Action } catch { $caught = $_ }
    if ($null -eq $caught -or $caught.Exception.Message -notmatch $ExpectedMessagePattern) {
        $actual = if ($null -eq $caught) { '<no exception>' } else { $caught.Exception.Message }
        throw "$FailureMessage (actual: $actual)"
    }
}

# --- Get-InvoiceTrivyRefreshTaskArguments ------------------------------------

$refreshScriptPathFixture = 'C:\repo\scripts\refresh-trivy-cache.ps1'
$expectedArguments = '-NoProfile -ExecutionPolicy Bypass -File "C:\repo\scripts\refresh-trivy-cache.ps1"'
$actualArguments = Get-InvoiceTrivyRefreshTaskArguments -RefreshScriptPath $refreshScriptPathFixture
if ($actualArguments -cne $expectedArguments) {
    throw "InvoiceTrivyCacheRefresh task arguments do not match: actual='$actualArguments' expected='$expectedArguments'"
}
if ($actualArguments -match '(?i)password|secret|token|bearer') {
    throw 'InvoiceTrivyCacheRefresh task arguments must never embed a credential or secret'
}
Assert-ThrowsLike `
    -Action { Get-InvoiceTrivyRefreshTaskArguments -RefreshScriptPath 'scripts\refresh-trivy-cache.ps1' | Out-Null } `
    -ExpectedMessagePattern 'fully qualified path' `
    -FailureMessage 'task arguments builder accepted a relative RefreshScriptPath'

# --- New-InvoiceTrivyRefreshTaskDefinition -----------------------------------

$pwshPathFixture = 'C:\Program Files\PowerShell\7\pwsh.exe'
$workingDirectoryFixture = 'C:\repo'
$userIdFixture = 'CONTOSO\invoice-operator'

$definition = New-InvoiceTrivyRefreshTaskDefinition `
    -PwshPath $pwshPathFixture `
    -RefreshScriptPath $refreshScriptPathFixture `
    -WorkingDirectory $workingDirectoryFixture `
    -UserId $userIdFixture `
    -StartTime '05:30'

if ($definition.Action.Execute -cne $pwshPathFixture) {
    throw "task action Execute does not match: actual='$($definition.Action.Execute)' expected='$pwshPathFixture'"
}
if ($definition.Action.Arguments -cne $expectedArguments) {
    throw "task action Arguments does not match: actual='$($definition.Action.Arguments)' expected='$expectedArguments'"
}
if ($definition.Action.WorkingDirectory -cne $workingDirectoryFixture) {
    throw "task action WorkingDirectory does not match: actual='$($definition.Action.WorkingDirectory)' expected='$workingDirectoryFixture'"
}

if ($definition.Trigger.CimClass.CimClassName -cne 'MSFT_TaskDailyTrigger') {
    throw "task trigger is not a daily trigger: $($definition.Trigger.CimClass.CimClassName)"
}
if ($definition.Trigger.DaysInterval -ne 1) {
    throw "task trigger does not recur every day: DaysInterval=$($definition.Trigger.DaysInterval)"
}
if ($definition.Trigger.Enabled -ne $true) {
    throw 'task trigger is not enabled'
}
$actualStartTimeOfDay = Get-LocalTimeOfDayFromTaskTriggerStartBoundary -StartBoundary $definition.Trigger.StartBoundary
if ($actualStartTimeOfDay -cne '05:30') {
    throw "task trigger does not fire at 05:30 local time: actual local time of day = $actualStartTimeOfDay"
}

if ($definition.Settings.StartWhenAvailable -ne $true) {
    throw 'task settings do not retry a missed run when the machine becomes available (StartWhenAvailable)'
}
# RestartCount/RestartInterval: Task Scheduler's own SettingsSet CimInstance
# reports RestartCount as an integer and RestartInterval as an ISO-8601
# duration string (e.g. "PT30M") -- confirmed directly in this environment,
# not a TimeSpan/DateTime -- so round-trip it back via XmlConvert (the same
# BCL type Task Scheduler's own duration strings are defined against) to
# assert the actual interval rather than string-matching one representation.
if ($definition.Settings.RestartCount -ne 3) {
    throw "task settings do not retry 3 times by default (RestartCount): actual=$($definition.Settings.RestartCount)"
}
$defaultRestartInterval = [Xml.XmlConvert]::ToTimeSpan($definition.Settings.RestartInterval)
if ($defaultRestartInterval -ne (New-TimeSpan -Minutes 30)) {
    throw "task settings do not retry 30 minutes apart by default (RestartInterval): actual=$($definition.Settings.RestartInterval)"
}

if ($definition.Principal.UserId -cne $userIdFixture) {
    throw "task principal UserId does not match: actual='$($definition.Principal.UserId)' expected='$userIdFixture'"
}
if ($definition.Principal.LogonType -ne 'S4U') {
    throw "task principal LogonType is not S4U (runs without a stored password whether or not the user is logged on): actual=$($definition.Principal.LogonType)"
}
if ($definition.Principal.RunLevel -ne 'Limited') {
    throw "task principal RunLevel is not Limited (the task must not request elevation): actual=$($definition.Principal.RunLevel)"
}

# A default StartTime is documented and reviewed as 05:30; confirm the
# parameter default itself, not just an explicitly passed value.
$defaultStartTimeDefinition = New-InvoiceTrivyRefreshTaskDefinition `
    -PwshPath $pwshPathFixture `
    -RefreshScriptPath $refreshScriptPathFixture `
    -WorkingDirectory $workingDirectoryFixture `
    -UserId $userIdFixture
$defaultStartTimeOfDay = Get-LocalTimeOfDayFromTaskTriggerStartBoundary -StartBoundary $defaultStartTimeDefinition.Trigger.StartBoundary
if ($defaultStartTimeOfDay -cne '05:30') {
    throw "InvoiceTrivyCacheRefresh's default start time is not 05:30 local time: actual=$defaultStartTimeOfDay"
}

# A different StartTime propagates all the way through to the trigger.
$eveningDefinition = New-InvoiceTrivyRefreshTaskDefinition `
    -PwshPath $pwshPathFixture `
    -RefreshScriptPath $refreshScriptPathFixture `
    -WorkingDirectory $workingDirectoryFixture `
    -UserId $userIdFixture `
    -StartTime '23:15'
$eveningStartTimeOfDay = Get-LocalTimeOfDayFromTaskTriggerStartBoundary -StartBoundary $eveningDefinition.Trigger.StartBoundary
if ($eveningStartTimeOfDay -cne '23:15') {
    throw "a non-default StartTime did not propagate to the task trigger: actual=$eveningStartTimeOfDay"
}

# A different RestartCount/RestartInterval propagates all the way through
# to the settings (a skipped/failed run's own retry policy).
$customRestartDefinition = New-InvoiceTrivyRefreshTaskDefinition `
    -PwshPath $pwshPathFixture `
    -RefreshScriptPath $refreshScriptPathFixture `
    -WorkingDirectory $workingDirectoryFixture `
    -UserId $userIdFixture `
    -RestartCount 5 `
    -RestartInterval (New-TimeSpan -Minutes 15)
if ($customRestartDefinition.Settings.RestartCount -ne 5) {
    throw "a non-default RestartCount did not propagate to the task settings: actual=$($customRestartDefinition.Settings.RestartCount)"
}
$customRestartInterval = [Xml.XmlConvert]::ToTimeSpan($customRestartDefinition.Settings.RestartInterval)
if ($customRestartInterval -ne (New-TimeSpan -Minutes 15)) {
    throw "a non-default RestartInterval did not propagate to the task settings: actual=$($customRestartDefinition.Settings.RestartInterval)"
}
Assert-ThrowsLike `
    -Action {
        New-InvoiceTrivyRefreshTaskDefinition `
            -PwshPath $pwshPathFixture `
            -RefreshScriptPath $refreshScriptPathFixture `
            -WorkingDirectory $workingDirectoryFixture `
            -UserId $userIdFixture `
            -RestartInterval ([TimeSpan]::Zero) | Out-Null
    } `
    -ExpectedMessagePattern 'RestartInterval must be a positive timespan' `
    -FailureMessage 'task definition accepted a zero RestartInterval'
foreach ($invalidRestartCount in @(-1, 11)) {
    Assert-ThrowsLike `
        -Action {
            New-InvoiceTrivyRefreshTaskDefinition `
                -PwshPath $pwshPathFixture `
                -RefreshScriptPath $refreshScriptPathFixture `
                -WorkingDirectory $workingDirectoryFixture `
                -UserId $userIdFixture `
                -RestartCount $invalidRestartCount | Out-Null
        } `
        -ExpectedMessagePattern 'Cannot validate argument|RestartCount' `
        -FailureMessage "task definition accepted an out-of-range RestartCount: $invalidRestartCount"
}

foreach ($invalidStartTime in @('5:30', '25:00', '05:60', '05-30', '0530', '')) {
    Assert-ThrowsLike `
        -Action {
            New-InvoiceTrivyRefreshTaskDefinition `
                -PwshPath $pwshPathFixture `
                -RefreshScriptPath $refreshScriptPathFixture `
                -WorkingDirectory $workingDirectoryFixture `
                -UserId $userIdFixture `
                -StartTime $invalidStartTime | Out-Null
        } `
        -ExpectedMessagePattern 'Cannot validate argument|StartTime' `
        -FailureMessage "task definition accepted an invalid StartTime: '$invalidStartTime'"
}

Assert-ThrowsLike `
    -Action {
        New-InvoiceTrivyRefreshTaskDefinition `
            -PwshPath 'pwsh.exe' `
            -RefreshScriptPath $refreshScriptPathFixture `
            -WorkingDirectory $workingDirectoryFixture `
            -UserId $userIdFixture | Out-Null
    } `
    -ExpectedMessagePattern 'PwshPath must be a fully qualified path' `
    -FailureMessage 'task definition accepted a non-fully-qualified PwshPath'
Assert-ThrowsLike `
    -Action {
        New-InvoiceTrivyRefreshTaskDefinition `
            -PwshPath $pwshPathFixture `
            -RefreshScriptPath $refreshScriptPathFixture `
            -WorkingDirectory 'repo' `
            -UserId $userIdFixture | Out-Null
    } `
    -ExpectedMessagePattern 'WorkingDirectory must be a fully qualified path' `
    -FailureMessage 'task definition accepted a non-fully-qualified WorkingDirectory'

# --- wiring: the main script never embeds a secret and never runs the refresh itself ---

$registerScriptSource = Get-Content -Raw -LiteralPath (Join-Path $scriptsRoot 'register-trivy-refresh-task.ps1')
if ($registerScriptSource -match '(?i)-Password\b|Get-Credential|ConvertTo-SecureString|\[PSCredential\]|\bSecureString\b') {
    throw 'register-trivy-refresh-task.ps1 must not collect or embed a credential'
}
if (-not $registerScriptSource.Contains('SupportsShouldProcess', [StringComparison]::Ordinal)) {
    throw 'register-trivy-refresh-task.ps1 does not support -WhatIf/-Confirm'
}
if ($registerScriptSource -match '(?m)^\s*&\s*.*refresh-trivy-cache\.ps1' -or
    $registerScriptSource -match '(?m)^\s*pwsh\b.*refresh-trivy-cache\.ps1' -or
    $registerScriptSource -match 'Start-Process\b.*refresh-trivy-cache\.ps1') {
    throw 'register-trivy-refresh-task.ps1 must only schedule refresh-trivy-cache.ps1, never invoke it directly'
}
if (-not $registerScriptSource.Contains('-RunLevel', [StringComparison]::Ordinal) -and
    -not $registerScriptSource.Contains('New-InvoiceTrivyRefreshTaskDefinition', [StringComparison]::Ordinal)) {
    throw 'register-trivy-refresh-task.ps1 does not build its task principal through the reviewed builder'
}
if ($registerScriptSource -notmatch "TaskName\s*=\s*'InvoiceTrivyCacheRefresh'") {
    throw 'register-trivy-refresh-task.ps1 does not default TaskName to InvoiceTrivyCacheRefresh'
}
if (-not $registerScriptSource.Contains('Get-ScheduledTask -TaskName $TaskName -TaskPath $TaskPath -ErrorAction SilentlyContinue', [StringComparison]::Ordinal) -or
    -not $registerScriptSource.Contains('Register-ScheduledTask', [StringComparison]::Ordinal) -or
    -not $registerScriptSource.Contains('Set-ScheduledTask', [StringComparison]::Ordinal)) {
    throw 'register-trivy-refresh-task.ps1 does not register when absent and update in place when the task already exists'
}

& (Join-Path $scriptsRoot 'test-register-trivy-refresh-behavior.ps1') -Case wiring

Write-Host 'All register-trivy-refresh-task fixtures passed.'
