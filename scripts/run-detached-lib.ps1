Set-StrictMode -Version Latest

# Pure helpers behind scripts/run-detached.ps1. Split into their own library
# file (mirroring scripts/release-image-gate-lib.ps1's split from
# release-image-gate.ps1) so scripts/test-run-detached.ps1 can dot-source and
# exercise them directly without running a real detached process for every
# case.

function Assert-DetachedRunPowerShellRuntime {
    if ($PSVersionTable.PSVersion -lt [version]'7.5.0') {
        throw "scripts/run-detached.ps1 requires PowerShell 7.5 or newer for ConvertFrom-Json -DateKind String; got $($PSVersionTable.PSVersion)"
    }
    return $true
}

if (-not ('DetachedRunNative.Handles' -as [type])) {
    Add-Type -Namespace DetachedRunNative -Name Handles -MemberDefinition @'
[System.Runtime.InteropServices.DllImport("kernel32.dll", SetLastError = true)]
public static extern bool SetHandleInformation(System.IntPtr hObject, uint dwMask, uint dwFlags);
[System.Runtime.InteropServices.DllImport("kernel32.dll", SetLastError = true)]
public static extern System.IntPtr GetStdHandle(int nStdHandle);
'@
}

# Disable-CurrentProcessStandardHandleInheritance works around a Windows
# handle-inheritance trap that otherwise defeats "detached" entirely: any
# process this one spawns with redirected output (New-DetachedRun's own
# Start-Process call, below, redirects the wrapper's stdout/stderr) is
# created with bInheritHandles=true -- a detail Start-Process/Process.Start
# always requests once any stream is redirected, with no way to turn it off
# from the public API. bInheritHandles=true duplicates *every* inheritable
# handle currently open in this process into the child, not just the new
# ones just created for it -- including this process's own stdin/stdout/
# stderr, if whatever launched this process redirected them (a calling
# tool capturing this script's output, or another Start-Process -Wait).
# Concretely: the detached wrapper process then holds a second, duplicate
# write handle to this process's own stdout pipe, so whatever is reading
# that pipe never sees end-of-stream -- and so never considers this process
# "done" -- until the *wrapper* (which can legitimately run far longer)
# also exits, silently defeating the entire "return almost immediately"
# point of this script. Measured directly while building this script: a
# caller that -Wait's on this process's exit returned in ~1.2s after this
# fix, versus ~15-17s (matching a deliberately long-sleeping grandchild)
# without it.
#
# The fix is to mark this process's own std handles non-inheritable before
# spawning the child that must not inherit them. This only affects what
# future children of *this* process inherit; it does not close the handles
# or affect this process's own ability to read or write them.
function Disable-CurrentProcessStandardHandleInheritance {
    $stdHandleIds = @(-10, -11, -12) # STD_INPUT/OUTPUT/ERROR_HANDLE
    $handleFlagInherit = 1
    foreach ($stdHandleId in $stdHandleIds) {
        try {
            $handle = [DetachedRunNative.Handles]::GetStdHandle($stdHandleId)
            if ($handle -ne [IntPtr]::Zero -and $handle -ne [IntPtr]-1) {
                [DetachedRunNative.Handles]::SetHandleInformation($handle, $handleFlagInherit, 0) | Out-Null
            }
        } catch {
            Write-Warning "could not mark standard handle $stdHandleId non-inheritable: $_"
        }
    }
}

function ConvertTo-DetachedRunSafeName {
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Name)

    $lowered = $Name.ToLowerInvariant()
    $chars = foreach ($ch in $lowered.ToCharArray()) {
        if (($ch -ge 'a' -and $ch -le 'z') -or ($ch -ge '0' -and $ch -le '9') -or $ch -eq '-' -or $ch -eq '_') { $ch } else { '-' }
    }
    $joined = -join $chars
    while ($joined.Contains('--')) { $joined = $joined.Replace('--', '-') }
    $joined = $joined.Trim('-')
    if ([string]::IsNullOrEmpty($joined)) { return 'run' }
    return $joined
}

function New-DetachedRunId {
    param(
        [Parameter(Mandatory)][AllowEmptyString()][string]$RunName,
        [Parameter(Mandatory)][string]$ScriptPath,
        [string]$Stamp = '',
        [string]$RandomSuffix = ''
    )
    if ([string]::IsNullOrEmpty($Stamp)) {
        $Stamp = [DateTimeOffset]::UtcNow.ToString('yyyyMMddTHHmmssZ')
    }
    $label = if (-not [string]::IsNullOrWhiteSpace($RunName)) {
        ConvertTo-DetachedRunSafeName -Name $RunName
    } else {
        ConvertTo-DetachedRunSafeName -Name ([IO.Path]::GetFileNameWithoutExtension($ScriptPath))
    }
    if ([string]::IsNullOrEmpty($RandomSuffix)) {
        $RandomSuffix = -join ((1..4) | ForEach-Object { '{0:x}' -f (Get-Random -Maximum 16) })
    }
    return "$label-$Stamp-$RandomSuffix"
}

function ConvertTo-SingleQuotedLiteral {
    param([Parameter(Mandatory)][AllowEmptyString()][string]$Value)
    return "'" + $Value.Replace("'", "''") + "'"
}

function ConvertTo-PowerShellArgumentArrayLiteral {
    param([string[]]$Values = @())
    if ($null -eq $Values -or $Values.Count -eq 0) { return '@()' }
    $quoted = $Values | ForEach-Object { ConvertTo-SingleQuotedLiteral -Value $_ }
    return '@(' + ($quoted -join ', ') + ')'
}

# New-DetachedRunWrapperScriptText builds the actual .ps1 text that will run
# inside the hidden detached process. It:
#   1. sets the console code page to UTF-8 (chcp 65001) and .NET's console
#      and pipeline output encodings to UTF-8 -- a hidden/detached console on
#      this machine's Windows locale otherwise defaults to the OEM/GBK code
#      page, which mangles UTF-8 byte output from native tools (git, docker)
#      that assume a UTF-8 console;
#   2. runs the target script with *> so every output stream (success,
#      error, warning, verbose, debug, information) lands in one transcript
#      file, the same convention release-image-gate.ps1 already uses
#      (Invoke-DockerLogged's "*> $LogPath") for logging a nested command's
#      full output reliably -- more robust here than Start-Transcript, which
#      is known to drop native-command output in some non-interactive hosts;
#   3. always writes an exit code file as its last action, from a try/
#      finally, so a caller can tell success from failure (and tell "still
#      running" from "crashed before writing anything") without attaching to
#      the process itself.
#
# $LASTEXITCODE is explicitly cleared before invoking the target script so a
# target that neither calls exit N nor runs a native command (leaving
# $LASTEXITCODE at whatever a previous command, e.g. chcp.com, set it to)
# falls back to $? instead of reporting a stale, unrelated code.
function New-DetachedRunWrapperScriptText {
    param(
        [Parameter(Mandatory)][string]$ScriptPath,
        [string[]]$ScriptArguments = @(),
        [Parameter(Mandatory)][string]$WorkingDirectory,
        [Parameter(Mandatory)][string]$TranscriptPath,
        [Parameter(Mandatory)][string]$ExitCodePath
    )

    $template = @'
$ErrorActionPreference = 'Stop'
try { & chcp.com 65001 | Out-Null } catch {}
try {
    [Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
    [Console]::InputEncoding = [Text.UTF8Encoding]::new($false)
} catch {}
$OutputEncoding = [Text.UTF8Encoding]::new($false)

Set-Location -LiteralPath __WORKING_DIRECTORY__

$targetScript = __SCRIPT_PATH__
$targetArguments = __SCRIPT_ARGUMENTS__
$exitCode = 1
try {
    $global:LASTEXITCODE = $null
    & $targetScript @targetArguments *> __TRANSCRIPT_PATH__
    $exitCode = $LASTEXITCODE
    if ($null -eq $exitCode) { $exitCode = if ($?) { 0 } else { 1 } }
} catch {
    ($_ | Out-String) | Out-File -LiteralPath __TRANSCRIPT_PATH__ -Append -Encoding utf8
    $exitCode = 1
} finally {
    [IO.File]::WriteAllText(__EXIT_CODE_PATH__, [string]$exitCode, [Text.UTF8Encoding]::new($false))
}
exit $exitCode
'@

    $template = $template.Replace('__WORKING_DIRECTORY__', (ConvertTo-SingleQuotedLiteral -Value $WorkingDirectory))
    $template = $template.Replace('__SCRIPT_PATH__', (ConvertTo-SingleQuotedLiteral -Value $ScriptPath))
    $template = $template.Replace('__SCRIPT_ARGUMENTS__', (ConvertTo-PowerShellArgumentArrayLiteral -Values $ScriptArguments))
    $template = $template.Replace('__TRANSCRIPT_PATH__', (ConvertTo-SingleQuotedLiteral -Value $TranscriptPath))
    $template = $template.Replace('__EXIT_CODE_PATH__', (ConvertTo-SingleQuotedLiteral -Value $ExitCodePath))
    return $template
}

# New-DetachedRun launches the wrapper as a genuinely separate, hidden Win32
# process (Start-Process, not & or Invoke-Expression) with its stdout/stderr
# redirected to files rather than inherited -- so the tool call that invokes
# this script returns almost immediately once the process is created,
# instead of blocking on it. That is what actually survives a sandbox that
# kills whatever is still in the foreground when a tool call's own timeout
# elapses: after this function returns, there is nothing left running in the
# foreground for the sandbox to kill.
#
# The transcript/exit-code files created by the wrapper (New-
# DetachedRunWrapperScriptText, above) are the primary evidence of what the
# target script did; stdout.log/stderr.log here are a narrower safety net
# for a catastrophic failure in the wrapper itself (e.g. an execution-policy
# refusal) that happens before the wrapper's own try/finally can run.
function New-DetachedRun {
    [CmdletBinding(SupportsShouldProcess)]
    param(
        [Parameter(Mandatory)][string]$ScriptPath,
        [string[]]$ScriptArguments = @(),
        [Parameter(Mandatory)][string]$WorkingDirectory,
        [Parameter(Mandatory)][string]$RunDirectory
    )

    if (-not (Test-Path -LiteralPath $ScriptPath -PathType Leaf)) {
        throw "script not found: $ScriptPath"
    }
    if (-not (Test-Path -LiteralPath $WorkingDirectory -PathType Container)) {
        throw "working directory not found: $WorkingDirectory"
    }
    $pwshCommand = Get-Command -Name pwsh -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -eq $pwshCommand) {
        throw 'pwsh executable not found on PATH'
    }
    $pwshPath = $pwshCommand.Source

    if (-not $PSCmdlet.ShouldProcess("$ScriptPath $($ScriptArguments -join ' ')", 'Start detached, hidden pwsh process')) {
        return $null
    }

    New-Item -ItemType Directory -Path $RunDirectory -Force | Out-Null
    $transcriptPath = Join-Path $RunDirectory 'transcript.log'
    $exitCodePath = Join-Path $RunDirectory 'exitcode.txt'
    $wrapperPath = Join-Path $RunDirectory 'wrapper.ps1'
    $stdoutPath = Join-Path $RunDirectory 'stdout.log'
    $stderrPath = Join-Path $RunDirectory 'stderr.log'
    $metadataPath = Join-Path $RunDirectory 'run.json'

    $wrapperText = New-DetachedRunWrapperScriptText -ScriptPath $ScriptPath -ScriptArguments $ScriptArguments `
        -WorkingDirectory $WorkingDirectory -TranscriptPath $transcriptPath -ExitCodePath $exitCodePath
    # UTF-8 WITH a BOM, deliberately, unlike this repo's usual Write-Utf8NoBom
    # artifacts: this file can embed non-ASCII paths (e.g. this repo's own
    # "K:\发票\..." worktrees), and an unmarked UTF-8 file is exactly the
    # ambiguous-encoding shape that lets a GBK-locale console misread it --
    # the same class of bug this script exists to route around. A BOM makes
    # pwsh's file-encoding detection unambiguous regardless of the system
    # codepage, for this one generated launcher file.
    [IO.File]::WriteAllText($wrapperPath, $wrapperText, [Text.UTF8Encoding]::new($true))

    # See Disable-CurrentProcessStandardHandleInheritance's own comment: this
    # must run immediately before the redirected Start-Process call below, or
    # a caller waiting on *this* process's own output/exit can silently block
    # until the wrapper -- which can legitimately run far longer -- exits.
    Disable-CurrentProcessStandardHandleInheritance

    $startArguments = @('-NoProfile', '-NonInteractive', '-ExecutionPolicy', 'Bypass', '-File', $wrapperPath)
    $process = Start-Process -FilePath $pwshPath -ArgumentList $startArguments -WorkingDirectory $RunDirectory `
        -WindowStyle Hidden -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath -PassThru

    $processStartTime = ''
    try { $processStartTime = $process.StartTime.ToUniversalTime().ToString('o') } catch {}

    $metadata = [ordered]@{
        runDirectory     = $RunDirectory
        scriptPath       = $ScriptPath
        scriptArguments  = @($ScriptArguments)
        workingDirectory = $WorkingDirectory
        startedAt        = [DateTimeOffset]::UtcNow.ToString('o')
        processId        = $process.Id
        processStartTime = $processStartTime
        wrapperPath      = $wrapperPath
        transcriptPath   = $transcriptPath
        exitCodePath     = $exitCodePath
        stdoutPath       = $stdoutPath
        stderrPath       = $stderrPath
    }
    [IO.File]::WriteAllText($metadataPath, ($metadata | ConvertTo-Json -Depth 5), [Text.UTF8Encoding]::new($false))

    return [pscustomobject]@{
        ProcessId    = $process.Id
        RunDirectory = $RunDirectory
    }
}

# Get-DetachedRunStatus inspects a run directory written by New-DetachedRun
# and reports one of:
#   Completed -- exitcode.txt exists; ExitCode is populated.
#   Running   -- no exitcode.txt yet, and a live process matches both the
#                recorded process ID and start time (guarding against the
#                OS having recycled that PID onto an unrelated process since
#                the run started).
#   Unknown   -- no exitcode.txt, and no matching live process (e.g. it was
#                killed with Stop-Process -Force, bypassing the wrapper's
#                own finally block) -- or exitcode.txt exists but is not a
#                parseable integer.
function Get-DetachedRunStatus {
    param([Parameter(Mandatory)][string]$RunDirectory)

    $runDirectory = [IO.Path]::GetFullPath($RunDirectory)
    $metadataPath = Join-Path $runDirectory 'run.json'
    if (-not (Test-Path -LiteralPath $metadataPath -PathType Leaf)) {
        throw "not a detached run directory (missing run.json): $runDirectory"
    }
    # -DateKind String: ConvertFrom-Json otherwise auto-detects
    # processStartTime's ISO-8601 shape and silently converts it to a
    # [DateTime] (losing the recorded UTC/'Z' marker in the process), which
    # corrupts the start-time comparison below. release-image-gate-lib.ps1's
    # ConvertFrom-ReleaseJson works around the exact same PowerShell
    # behavior the same way.
    $metadata = Get-Content -Raw -LiteralPath $metadataPath | ConvertFrom-Json -DateKind String

    $exitCodePath = Join-Path $runDirectory 'exitcode.txt'
    if (Test-Path -LiteralPath $exitCodePath -PathType Leaf) {
        $exitCodeText = (Get-Content -Raw -LiteralPath $exitCodePath).Trim()
        $parsedExitCode = 0
        if ([int]::TryParse($exitCodeText, [ref]$parsedExitCode)) {
            return [pscustomobject]@{ State = 'Completed'; ExitCode = $parsedExitCode; RunDirectory = $runDirectory; Metadata = $metadata }
        }
        return [pscustomobject]@{ State = 'Unknown'; ExitCode = $null; RunDirectory = $runDirectory; Metadata = $metadata }
    }

    $processId = [int]$metadata.processId
    $process = Get-Process -Id $processId -ErrorAction SilentlyContinue
    $stillRunning = $false
    if ($null -ne $process) {
        $stillRunning = $true
        if ($metadata.PSObject.Properties['processStartTime'] -and -not [string]::IsNullOrEmpty($metadata.processStartTime)) {
            try {
                $recordedStart = [DateTimeOffset]::Parse($metadata.processStartTime)
                $actualStart = [DateTimeOffset]$process.StartTime.ToUniversalTime()
                $stillRunning = [Math]::Abs(($recordedStart - $actualStart).TotalSeconds) -lt 2
            } catch {
                # Could not verify start time (e.g. access denied); fall back
                # to the PID-only check already established above.
            }
        }
    }
    if ($stillRunning) {
        return [pscustomobject]@{ State = 'Running'; ExitCode = $null; RunDirectory = $runDirectory; Metadata = $metadata }
    }
    return [pscustomobject]@{ State = 'Unknown'; ExitCode = $null; RunDirectory = $runDirectory; Metadata = $metadata }
}

# Wait-DetachedRun polls Get-DetachedRunStatus until the run leaves the
# Running state or TimeoutSeconds elapses, whichever comes first. Each poll
# call is short (well under any sandbox's own per-tool-call timeout); the
# caller is expected to invoke this from a separate, later tool call if one
# poll's TimeoutSeconds is not enough, rather than this function blocking
# for an unbounded time itself.
function Wait-DetachedRun {
    param(
        [Parameter(Mandatory)][string]$RunDirectory,
        [int]$TimeoutSeconds = 3600,
        [int]$PollIntervalSeconds = 5
    )
    $deadline = [DateTimeOffset]::UtcNow.AddSeconds($TimeoutSeconds)
    while ($true) {
        $status = Get-DetachedRunStatus -RunDirectory $RunDirectory
        if ($status.State -ne 'Running') {
            return $status
        }
        $remainingSeconds = ($deadline - [DateTimeOffset]::UtcNow).TotalSeconds
        if ($remainingSeconds -le 0) {
            return Get-DetachedRunStatus -RunDirectory $RunDirectory
        }
        $sleepSeconds = [Math]::Max(1, [Math]::Min($PollIntervalSeconds, [Math]::Ceiling($remainingSeconds)))
        Start-Sleep -Seconds $sleepSeconds
    }
}
