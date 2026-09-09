[CmdletBinding(SupportsShouldProcess, ConfirmImpact = 'Medium')]
param(
    [ValidateNotNullOrEmpty()]
    [string]$RepoRoot = (Split-Path -Parent $PSScriptRoot),

    [ValidatePattern('^[0-9A-Za-z][0-9A-Za-z _.-]{0,127}$')]
    [string]$TaskName = 'InvoiceTrivyCacheRefresh',

    [ValidateNotNullOrEmpty()]
    [string]$TaskPath = '\',

    [ValidatePattern('^([01]\d|2[0-3]):[0-5]\d$')]
    [string]$StartTime = '05:30',

    [ValidateNotNullOrEmpty()]
    [string]$UserId = "$env:USERDOMAIN\$env:USERNAME",

    # How many times, and how far apart, Task Scheduler retries this task
    # the same day after it exits non-zero -- including exit 75 (refresh-
    # trivy-cache.ps1 skipped because the release image gate held the
    # shared cache volume). See register-trivy-refresh-task-lib.ps1's own
    # comment on New-InvoiceTrivyRefreshTaskDefinition for the full reasoning.
    [ValidateRange(0, 10)]
    [int]$RestartCount = 3,

    [ValidateNotNull()]
    [TimeSpan]$RestartInterval = (New-TimeSpan -Minutes 30)
)

<#
.SYNOPSIS
Registers, or updates in place, a Windows Scheduled Task named
InvoiceTrivyCacheRefresh that runs scripts/refresh-trivy-cache.ps1 once
daily, so the release gate's shared invoice-release-gate-trivy-0-74-0 cache
volume stays warm without a person running the refresh by hand.

.DESCRIPTION
This script only registers/updates the schedule; it never runs the refresh
itself. It embeds no secret: the task's LogonType is S4U (see New-
InvoiceTrivyRefreshTaskDefinition in register-trivy-refresh-task-lib.ps1 for
why), which lets the task run daily whether or not UserId is interactively
logged in, without Task Scheduler ever storing a password. RunLevel is
Limited (not Highest) -- refresh-trivy-cache.ps1 needs no elevation.

Idempotent and safe to rerun: if InvoiceTrivyCacheRefresh does not exist yet
under TaskPath, it is registered; if it already exists, its action, trigger,
settings and principal are updated in place with Set-ScheduledTask rather
than being unregistered and recreated, so Task Scheduler's own run history
for the task is preserved.

.PARAMETER RepoRoot
Repository root containing scripts\refresh-trivy-cache.ps1. Defaults to this
script's own repo root (the parent of the scripts\ directory it lives in).

.PARAMETER RestartCount
.PARAMETER RestartInterval
How many times (default 3), and how far apart (default 30 minutes), Task
Scheduler retries the task the same day after refresh-trivy-cache.ps1 exits
non-zero -- including exit 75, which means the release image gate held the
shared cache volume and the run was skipped rather than failed.

.PARAMETER WhatIf
Prints the task definition (action, trigger, settings, principal) without
registering, updating, or otherwise changing anything.

.EXAMPLE
pwsh -NoProfile -File scripts\register-trivy-refresh-task.ps1
# Registers (or updates in place) InvoiceTrivyCacheRefresh: daily at 05:30
# local time, as the current user, running refresh-trivy-cache.ps1 from
# this script's own repo root.

.EXAMPLE
pwsh -NoProfile -File scripts\register-trivy-refresh-task.ps1 -WhatIf
# Prints the task definition without registering or changing anything.

.EXAMPLE
pwsh -NoProfile -File scripts\register-trivy-refresh-task.ps1 -Confirm:$false
# Registers/updates without an interactive confirmation prompt.
#>

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

. (Join-Path $PSScriptRoot 'register-trivy-refresh-task-lib.ps1')
Assert-RegisterTrivyRefreshTaskPowerShellRuntime | Out-Null

if ($null -eq (Get-Command -Name Get-ScheduledTask -ErrorAction SilentlyContinue)) {
    throw 'the ScheduledTasks module/cmdlets are not available on this system'
}

$pwshCommand = Get-Command -Name pwsh -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
if ($null -eq $pwshCommand) {
    throw 'pwsh executable not found on PATH'
}

$repoRootFullPath = (Resolve-Path -LiteralPath $RepoRoot).ProviderPath
$refreshScriptPath = Join-Path $repoRootFullPath 'scripts\refresh-trivy-cache.ps1'
if (-not (Test-Path -LiteralPath $refreshScriptPath -PathType Leaf)) {
    throw "refresh-trivy-cache.ps1 not found under RepoRoot: $refreshScriptPath"
}

$definition = New-InvoiceTrivyRefreshTaskDefinition `
    -PwshPath $pwshCommand.Source `
    -RefreshScriptPath $refreshScriptPath `
    -WorkingDirectory $repoRootFullPath `
    -UserId $UserId `
    -StartTime $StartTime `
    -RestartCount $RestartCount `
    -RestartInterval $RestartInterval

$taskFullName = "$TaskPath$TaskName"
Write-Host "Task definition for '$taskFullName':"
Write-Host "  Execute:          $($definition.Action.Execute)"
Write-Host "  Arguments:        $($definition.Action.Arguments)"
Write-Host "  WorkingDirectory: $($definition.Action.WorkingDirectory)"
Write-Host "  Trigger:          daily at $StartTime local time (recurrence DaysInterval=$($definition.Trigger.DaysInterval))"
Write-Host "  Principal:        $($definition.Principal.UserId), LogonType=$($definition.Principal.LogonType), RunLevel=$($definition.Principal.RunLevel)"
Write-Host "  Settings:         StartWhenAvailable=$($definition.Settings.StartWhenAvailable), RestartCount=$($definition.Settings.RestartCount), RestartInterval=$($definition.Settings.RestartInterval)"

$existingTask = Get-ScheduledTask -TaskName $TaskName -TaskPath $TaskPath -ErrorAction SilentlyContinue
$taskDescription = 'Refreshes the invoice-release-gate-trivy-0-74-0 Docker volume via scripts/refresh-trivy-cache.ps1. Registered by scripts/register-trivy-refresh-task.ps1; see docs/PRODUCTION-RUNBOOK.md.'

if ($null -eq $existingTask) {
    if ($PSCmdlet.ShouldProcess($taskFullName, 'Register daily Trivy cache refresh Scheduled Task')) {
        Register-ScheduledTask -TaskName $TaskName -TaskPath $TaskPath `
            -Action $definition.Action -Trigger $definition.Trigger -Settings $definition.Settings -Principal $definition.Principal `
            -Description $taskDescription -Force `
            | Out-Null
        Write-Host "Registered Scheduled Task '$taskFullName'."
    }
} else {
    if ($PSCmdlet.ShouldProcess($taskFullName, 'Update daily Trivy cache refresh Scheduled Task in place')) {
        Set-ScheduledTask -TaskName $TaskName -TaskPath $TaskPath `
            -Action $definition.Action -Trigger $definition.Trigger -Settings $definition.Settings -Principal $definition.Principal `
            | Out-Null
        Write-Host "Updated Scheduled Task '$taskFullName' in place."
    }
}

$global:LASTEXITCODE = 0
