Set-StrictMode -Version Latest

# Pure helpers behind scripts/register-trivy-refresh-task.ps1, split into
# their own library file (mirroring release-image-gate.ps1's own split from
# release-image-gate-lib.ps1, and refresh-trivy-cache.ps1's from refresh-
# trivy-cache-lib.ps1) so scripts/test-register-trivy-refresh-task.ps1 can
# dot-source and exercise the task-definition builder directly, without ever
# calling Get-ScheduledTask/Register-ScheduledTask/Set-ScheduledTask or
# otherwise touching the real Task Scheduler service.
#
# New-ScheduledTaskAction/-Trigger/-SettingsSet/-Principal (used below) are
# themselves pure, local object constructors -- confirmed directly in this
# environment: none of them opens a CIM session against the Task Scheduler
# service or persists anything; only Get-/Register-/Set-/Unregister-
# ScheduledTask do that. That is what makes this file safe to dot-source and
# call from a static test.

function Assert-RegisterTrivyRefreshTaskPowerShellRuntime {
    if ($PSVersionTable.PSVersion -lt [version]'7.0.0') {
        throw "scripts/register-trivy-refresh-task.ps1 requires PowerShell 7 or newer; got $($PSVersionTable.PSVersion)"
    }
    return $true
}

# Get-InvoiceTrivyRefreshTaskArguments builds the exact command-line
# arguments the registered task passes to pwsh: -NoProfile (a scheduled,
# non-interactive run has no reason to load anyone's profile script),
# -ExecutionPolicy Bypass (likewise no reason to inherit whatever execution
# policy happens to be configured machine- or user-wide), -File plus the
# fully qualified path to scripts/refresh-trivy-cache.ps1. No credential or
# secret of any kind is ever part of this string, and refresh-trivy-
# cache.ps1 is invoked with no arguments of its own -- this script only
# schedules that refresh, it never runs it.
function Get-InvoiceTrivyRefreshTaskArguments {
    param([Parameter(Mandatory)][string]$RefreshScriptPath)
    if (-not [IO.Path]::IsPathFullyQualified($RefreshScriptPath)) {
        throw "RefreshScriptPath must be a fully qualified path: $RefreshScriptPath"
    }
    return "-NoProfile -ExecutionPolicy Bypass -File `"$RefreshScriptPath`""
}

# New-InvoiceTrivyRefreshTaskDefinition builds the four pieces Register-
# ScheduledTask/Set-ScheduledTask need (Action/Trigger/Settings/Principal)
# for the InvoiceTrivyCacheRefresh task, entirely from explicit parameters --
# no ambient script state and no call to Get-ScheduledTask/Register-
# ScheduledTask/Set-ScheduledTask, so a caller (the test file included) can
# construct and inspect the exact task shape without ever touching a real
# Scheduled Task.
#
# LogonType S4U (rather than plain Interactive) is deliberate: the task
# fires once daily at StartTime, an hour nobody is likely to be interactively
# logged in on a workstation, and S4U is the standard Windows mechanism for a
# per-user task that must run whether or not that user is logged on, without
# Task Scheduler ever storing -- or this script ever collecting or embedding
# -- a password. It does require the account to hold the "Log on as a batch
# job" user right (secpol.msc -> Local Policies -> User Rights Assignment),
# which an interactive Windows account normally already has; if Task
# Scheduler's own history shows the task never firing, that right is the
# first thing to check.
#
# RunLevel Limited (not Highest) is also deliberate: refresh-trivy-cache.ps1
# only needs a normal user token to reach curl, tar, and Docker Desktop's
# named pipe, so this task never requests elevation.
#
# RestartCount/RestartInterval (default 3 times, 30 minutes apart) is Task
# Scheduler's own built-in retry: it fires whenever the action's process
# exits non-zero -- which now includes exit 75, refresh-trivy-cache.ps1's
# "the release image gate holds the shared cache volume; skipped" outcome
# (see that script's own .DESCRIPTION) -- so a run skipped or genuinely
# failed once still gets refreshed later the same day, on top of
# StartWhenAvailable already covering the machine being off/asleep at
# StartTime.
function New-InvoiceTrivyRefreshTaskDefinition {
    param(
        [Parameter(Mandatory)][string]$PwshPath,
        [Parameter(Mandatory)][string]$RefreshScriptPath,
        [Parameter(Mandatory)][string]$WorkingDirectory,
        [Parameter(Mandatory)][string]$UserId,

        [ValidatePattern('^([01]\d|2[0-3]):[0-5]\d$')]
        [string]$StartTime = '05:30',

        [ValidateRange(0, 10)]
        [int]$RestartCount = 3,

        [ValidateNotNull()]
        [TimeSpan]$RestartInterval = (New-TimeSpan -Minutes 30)
    )
    if (-not [IO.Path]::IsPathFullyQualified($PwshPath)) {
        throw "PwshPath must be a fully qualified path: $PwshPath"
    }
    if (-not [IO.Path]::IsPathFullyQualified($WorkingDirectory)) {
        throw "WorkingDirectory must be a fully qualified path: $WorkingDirectory"
    }
    if ($RestartInterval -le [TimeSpan]::Zero) {
        throw "RestartInterval must be a positive timespan, got $RestartInterval"
    }

    $taskArguments = Get-InvoiceTrivyRefreshTaskArguments -RefreshScriptPath $RefreshScriptPath
    $action = New-ScheduledTaskAction -Execute $PwshPath -Argument $taskArguments -WorkingDirectory $WorkingDirectory
    $trigger = New-ScheduledTaskTrigger -Daily -At $StartTime
    $settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -RestartCount $RestartCount -RestartInterval $RestartInterval
    $principal = New-ScheduledTaskPrincipal -UserId $UserId -LogonType S4U -RunLevel Limited

    return [pscustomobject]@{
        Action    = $action
        Trigger   = $trigger
        Settings  = $settings
        Principal = $principal
        StartTime = $StartTime
    }
}

# Get-LocalTimeOfDayFromTaskTriggerStartBoundary parses a ScheduledTaskTrigger's
# StartBoundary (a UTC ISO-8601 string, e.g. "2026-09-02T21:30:00Z" --
# confirmed directly: the CimInstance New-ScheduledTaskTrigger returns
# carries StartBoundary as a plain System.String in that shape, not a
# DateTime/DateTimeOffset) back into the "HH:mm" local wall-clock time it
# represents, so a caller can assert the configured start time regardless of
# the machine's timezone or the date the check happens to run on (-Daily -At
# anchors StartBoundary to today's or tomorrow's date at construction time,
# which this helper deliberately ignores).
function Get-LocalTimeOfDayFromTaskTriggerStartBoundary {
    param([Parameter(Mandatory)][string]$StartBoundary)
    $parsed = [DateTimeOffset]::Parse($StartBoundary, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::None)
    return $parsed.ToLocalTime().ToString('HH:mm', [Globalization.CultureInfo]::InvariantCulture)
}
