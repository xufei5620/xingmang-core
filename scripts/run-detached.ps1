[CmdletBinding(SupportsShouldProcess, DefaultParameterSetName = 'Start')]
param(
    # Path to the PowerShell script to run detached, e.g. a release ceremony
    # step such as scripts/release-image-gate.ps1.
    [Parameter(Mandatory, ParameterSetName = 'Start', Position = 0)]
    [ValidateNotNullOrEmpty()]
    [string]$ScriptPath,

    # Arguments forwarded to ScriptPath, e.g.
    # @('-ReleaseName', '0.1.0-rc68', '-IdPMode', 'keycloak').
    [Parameter(ParameterSetName = 'Start')]
    [string[]]$ScriptArguments = @(),

    [Parameter(ParameterSetName = 'Start')]
    [ValidateNotNullOrEmpty()]
    [string]$WorkingDirectory = (Get-Location).ProviderPath,

    # A short label folded into the run directory's name; defaults to
    # ScriptPath's own base name.
    [Parameter(ParameterSetName = 'Start')]
    [AllowEmptyString()]
    [string]$RunName = '',

    # Parent directory for run directories; defaults to logs\detached-runs
    # under the project root (this script's grandparent directory).
    [Parameter(ParameterSetName = 'Start')]
    [AllowEmptyString()]
    [string]$RunsDirectory = '',

    # Instead of starting a new run, poll/report on one already started by a
    # previous -Start invocation (e.g. from an earlier, separate tool call).
    [Parameter(Mandatory, ParameterSetName = 'Attach')]
    [ValidateNotNullOrEmpty()]
    [string]$AttachRunDirectory,

    # Poll until the run completes or TimeoutSeconds elapses, instead of
    # returning immediately after starting/attaching. Keep this well under
    # whatever timeout the calling tool itself enforces -- for a run
    # expected to take longer than that, omit -Wait, then call this script
    # again later with -AttachRunDirectory (optionally with -Wait) to check
    # on it; each such call is its own short-lived foreground command.
    [switch]$Wait,

    [ValidateRange(1, 86400)]
    [int]$TimeoutSeconds = 3600,

    [ValidateRange(1, 300)]
    [int]$PollIntervalSeconds = 5
)

<#
.SYNOPSIS
Runs a PowerShell script in a hidden, detached process that outlives the
calling tool invocation, with UTF-8 console encodings, a full-output
transcript log, and an exit-code file -- then optionally polls it.

.DESCRIPTION
Two problems bit the RC68 release ceremony (see docs/PRODUCTION-RUNBOOK.md,
next to the image-gate block, for the full incident note):

  1. The interactive tool sandbox kills whatever is still in the foreground
     of a tool call after about 10 minutes, but a release image gate run
     (build + Trivy scan + SBOM for nine images) legitimately takes longer.
  2. A hidden/non-interactive console on this machine's Windows locale
     defaults to the OEM/GBK code page, which mangles UTF-8 byte output from
     native tools (git, docker) that assume a UTF-8 console -- e.g. git
     printing a path containing this repo's own "发票" directory name.

This script launches ScriptPath as a separate, hidden Win32 process
(Start-Process, not & or Invoke-Expression) with output redirected to files
rather than inherited, so this tool call itself returns almost immediately
once the process exists -- there is nothing left in the foreground for a
10-minute sandbox timeout to kill. The launched process's very first actions
are `chcp 65001` and setting .NET's console/output encodings to UTF-8,
before anything else runs.

.PARAMETER Wait
Poll the run to completion instead of returning immediately. Keep
TimeoutSeconds well under whatever timeout the calling tool itself enforces;
for a run expected to take longer than one call's budget, omit -Wait and
issue separate, later -AttachRunDirectory calls instead (each one a short,
independent foreground command).

.EXAMPLE
pwsh -NoProfile -File scripts\run-detached.ps1 `
  -ScriptPath scripts\release-image-gate.ps1 `
  -ScriptArguments @('-ReleaseName', '0.1.0-rc68', '-ImageTag', '0.1.0-rc68', '-IdPMode', 'keycloak')
# Prints the run directory and returns in well under a second.

.EXAMPLE
pwsh -NoProfile -File scripts\run-detached.ps1 -AttachRunDirectory 'logs\detached-runs\release-image-gate-...' -Wait -TimeoutSeconds 540
# Polls an already-started run for up to nine minutes from this one call;
# call again the same way if it is still running.
#>

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$projectRoot = Split-Path -Parent $PSScriptRoot
. (Join-Path $PSScriptRoot 'run-detached-lib.ps1')
Assert-DetachedRunPowerShellRuntime | Out-Null

if ($PSCmdlet.ParameterSetName -eq 'Attach') {
    if (-not (Test-Path -LiteralPath $AttachRunDirectory -PathType Container)) {
        throw "run directory does not exist: $AttachRunDirectory"
    }
    $runDirectory = (Resolve-Path -LiteralPath $AttachRunDirectory).ProviderPath
} else {
    if ([string]::IsNullOrWhiteSpace($RunsDirectory)) {
        $RunsDirectory = Join-Path $projectRoot 'logs\detached-runs'
    } elseif (-not [IO.Path]::IsPathFullyQualified($RunsDirectory)) {
        $RunsDirectory = Join-Path $projectRoot $RunsDirectory
    }
    if (-not (Test-Path -LiteralPath $RunsDirectory)) {
        New-Item -ItemType Directory -Path $RunsDirectory -Force | Out-Null
    }

    $scriptFullPath = (Resolve-Path -LiteralPath $ScriptPath).ProviderPath
    $workingDirectoryFullPath = (Resolve-Path -LiteralPath $WorkingDirectory).ProviderPath
    $runId = New-DetachedRunId -RunName $RunName -ScriptPath $scriptFullPath
    $runDirectory = Join-Path $RunsDirectory $runId
    if (Test-Path -LiteralPath $runDirectory) {
        throw "run directory already exists: $runDirectory"
    }

    $started = New-DetachedRun -ScriptPath $scriptFullPath -ScriptArguments $ScriptArguments `
        -WorkingDirectory $workingDirectoryFullPath -RunDirectory $runDirectory -WhatIf:$WhatIfPreference

    if ($null -eq $started) {
        # -WhatIf: New-DetachedRun's own ShouldProcess reported what it would
        # do and started nothing; there is no run directory to report on.
        exit 0
    }
    Write-Host "Detached run started: $runDirectory"
    Write-Host "  pid: $($started.ProcessId)"
    Write-Host "  transcript: $(Join-Path $runDirectory 'transcript.log')"
}

if (-not $Wait) {
    Write-Host $runDirectory
    exit 0
}

$final = Wait-DetachedRun -RunDirectory $runDirectory -TimeoutSeconds $TimeoutSeconds -PollIntervalSeconds $PollIntervalSeconds
switch ($final.State) {
    'Completed' {
        Write-Host "Detached run completed with exit code $($final.ExitCode): $runDirectory"
        exit $final.ExitCode
    }
    'Running' {
        Write-Host "Detached run is still running after a ${TimeoutSeconds}s wait: $runDirectory"
        Write-Host "Check on it again with: pwsh -NoProfile -File scripts\run-detached.ps1 -AttachRunDirectory `"$runDirectory`" -Wait"
        # A distinct, documented sentinel -- not 0 (success) and not a code
        # the target script itself is likely to produce -- so a caller's own
        # automation can tell "still running" apart from either outcome.
        exit 4
    }
    default {
        throw "detached run ended in an unexpected state '$($final.State)' with no exit code recorded: $runDirectory (check stdout.log/stderr.log there for a wrapper-level failure)"
    }
}
