$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$projectRoot = Split-Path -Parent $PSScriptRoot
$libPath = Join-Path $PSScriptRoot 'run-detached-lib.ps1'
$scriptPath = Join-Path $PSScriptRoot 'run-detached.ps1'
. $libPath

function Assert-NoParseErrors {
    param([Parameter(Mandatory)][string]$Path)
    $tokens = $null
    $parseErrors = $null
    [System.Management.Automation.Language.Parser]::ParseFile($Path, [ref]$tokens, [ref]$parseErrors) | Out-Null
    if ($parseErrors.Count -ne 0) {
        throw "$Path has $($parseErrors.Count) parse error(s): $(($parseErrors | ForEach-Object { $_.Message }) -join '; ')"
    }
}

function Assert-TextParsesAsPowerShell {
    param([Parameter(Mandatory)][string]$Text, [Parameter(Mandatory)][string]$Label)
    $tokens = $null
    $parseErrors = $null
    [System.Management.Automation.Language.Parser]::ParseInput($Text, [ref]$tokens, [ref]$parseErrors) | Out-Null
    if ($parseErrors.Count -ne 0) {
        throw "$Label produced text with $($parseErrors.Count) parse error(s): $(($parseErrors | ForEach-Object { $_.Message }) -join '; ')`n---`n$Text"
    }
}

# --- static parse checks (both scripts must be syntactically valid) -------
Assert-NoParseErrors -Path $libPath
Assert-NoParseErrors -Path $scriptPath
Write-Host 'Parse check passed for run-detached.ps1 and run-detached-lib.ps1.'

if ($PSVersionTable.PSVersion -lt [version]'7.5.0') {
    throw "this test suite itself requires PowerShell 7.5+ (same as run-detached.ps1); got $($PSVersionTable.PSVersion)"
}
if (-not (Assert-DetachedRunPowerShellRuntime)) {
    throw 'Assert-DetachedRunPowerShellRuntime did not return $true on this supported runtime'
}
Write-Host "Assert-DetachedRunPowerShellRuntime passed on PowerShell $($PSVersionTable.PSVersion)."

# --- ConvertTo-DetachedRunSafeName -----------------------------------------
$safeNameFixtures = @(
    [pscustomobject]@{ In = 'refresh-trivy-cache'; Want = 'refresh-trivy-cache' },
    [pscustomobject]@{ In = 'Release Image Gate!!'; Want = 'release-image-gate' },
    [pscustomobject]@{ In = '---'; Want = 'run' },
    [pscustomobject]@{ In = ''; Want = 'run' },
    [pscustomobject]@{ In = 'a__b--c'; Want = 'a__b-c' }
)
foreach ($fixture in $safeNameFixtures) {
    $got = ConvertTo-DetachedRunSafeName -Name $fixture.In
    if ($got -cne $fixture.Want) {
        throw "ConvertTo-DetachedRunSafeName('$($fixture.In)') = '$got', want '$($fixture.Want)'"
    }
}
Write-Host 'ConvertTo-DetachedRunSafeName fixtures passed.'

# --- ConvertTo-SingleQuotedLiteral / ConvertTo-PowerShellArgumentArrayLiteral
# These generate the text of a *new* PowerShell script (New-
# DetachedRunWrapperScriptText embeds a real filesystem path and the target
# script's own arguments this way), so the strongest proof of correctness is
# a round trip through the real PowerShell parser and evaluator: generate a
# literal for a value containing every character that would be dangerous to
# get wrong (a single quote, a backtick, a dollar sign, a semicolon, a double
# quote), evaluate the generated text, and confirm the original value comes
# back byte for byte.
$trickyValues = @(
    "plain",
    "has a space",
    "single'quote",
    'G:\xingmang\09-wt\发票-toolchain-fixture with 空格 and a''quote',
    'semi;colon$var`backtick"quote'
)
foreach ($value in $trickyValues) {
    $literal = ConvertTo-SingleQuotedLiteral -Value $value
    Assert-TextParsesAsPowerShell -Text $literal -Label "ConvertTo-SingleQuotedLiteral('$value')"
    $roundTripped = [scriptblock]::Create($literal).Invoke()
    if ($roundTripped -cne $value) {
        throw "ConvertTo-SingleQuotedLiteral round trip failed: got '$roundTripped', want '$value'"
    }
}
Write-Host 'ConvertTo-SingleQuotedLiteral round-trips every tricky character.'

$arrayLiteralEmpty = ConvertTo-PowerShellArgumentArrayLiteral -Values @()
if ($arrayLiteralEmpty -cne '@()') {
    throw "ConvertTo-PowerShellArgumentArrayLiteral(@()) = '$arrayLiteralEmpty', want '@()'"
}
$arrayLiteral = ConvertTo-PowerShellArgumentArrayLiteral -Values @('-ReleaseName', "it's-rc68", 'plain')
Assert-TextParsesAsPowerShell -Text "`$x = $arrayLiteral" -Label 'ConvertTo-PowerShellArgumentArrayLiteral'
$roundTrippedArray = [scriptblock]::Create($arrayLiteral).Invoke()
if (@(Compare-Object -ReferenceObject @('-ReleaseName', "it's-rc68", 'plain') -DifferenceObject @($roundTrippedArray) -SyncWindow 0).Count -ne 0) {
    throw "ConvertTo-PowerShellArgumentArrayLiteral round trip failed: got $($roundTrippedArray -join ', ')"
}
Write-Host 'ConvertTo-PowerShellArgumentArrayLiteral round-trips correctly, including an embedded single quote.'

# --- New-DetachedRunWrapperScriptText --------------------------------------
$wrapperText = New-DetachedRunWrapperScriptText `
    -ScriptPath 'G:\xingmang\09-wt\发票-toolchain-fixture\scripts\release-image-gate.ps1' `
    -ScriptArguments @('-ReleaseName', "it's-rc68", '-IdPMode', 'keycloak') `
    -WorkingDirectory 'G:\xingmang\09-wt\发票-toolchain-fixture' `
    -TranscriptPath 'G:\xingmang\09-wt\发票-toolchain-fixture\logs\detached-runs\example\transcript.log' `
    -ExitCodePath 'G:\xingmang\09-wt\发票-toolchain-fixture\logs\detached-runs\example\exitcode.txt'
Assert-TextParsesAsPowerShell -Text $wrapperText -Label 'New-DetachedRunWrapperScriptText'
foreach ($required in @('chcp.com 65001', '[Console]::OutputEncoding', '$OutputEncoding', 'exitcode.txt', 'transcript.log', "it''s-rc68")) {
    if (-not $wrapperText.Contains($required, [StringComparison]::Ordinal)) {
        throw "generated wrapper script is missing expected content: $required"
    }
}
Write-Host 'New-DetachedRunWrapperScriptText produces a parseable wrapper with UTF-8 setup, transcript and exit-code capture.'

# --- Get-DetachedRunStatus ---------------------------------------------------
$scratchRoot = Join-Path $projectRoot "logs\test-run-detached-scratch-$([Guid]::NewGuid().ToString('N').Substring(0,8))"
New-Item -ItemType Directory -Path $scratchRoot -Force | Out-Null

try {
    function New-FixtureRunDirectory {
        param([Parameter(Mandatory)][string]$Name, [Parameter(Mandatory)][hashtable]$Metadata)
        $dir = Join-Path $scratchRoot $Name
        New-Item -ItemType Directory -Path $dir -Force | Out-Null
        [IO.File]::WriteAllText((Join-Path $dir 'run.json'), ($Metadata | ConvertTo-Json -Depth 5), [Text.UTF8Encoding]::new($false))
        return $dir
    }

    $completedDir = New-FixtureRunDirectory -Name 'completed' -Metadata @{ processId = 999999; processStartTime = '' }
    [IO.File]::WriteAllText((Join-Path $completedDir 'exitcode.txt'), '3', [Text.UTF8Encoding]::new($false))
    $completedStatus = Get-DetachedRunStatus -RunDirectory $completedDir
    if ($completedStatus.State -cne 'Completed' -or $completedStatus.ExitCode -ne 3) {
        throw "Completed fixture: got State=$($completedStatus.State) ExitCode=$($completedStatus.ExitCode)"
    }

    $malformedDir = New-FixtureRunDirectory -Name 'malformed-exitcode' -Metadata @{ processId = 999999; processStartTime = '' }
    [IO.File]::WriteAllText((Join-Path $malformedDir 'exitcode.txt'), 'not-a-number', [Text.UTF8Encoding]::new($false))
    $malformedStatus = Get-DetachedRunStatus -RunDirectory $malformedDir
    if ($malformedStatus.State -cne 'Unknown') {
        throw "Malformed exit code fixture: got State=$($malformedStatus.State), want Unknown"
    }

    $selfStartTime = (Get-Process -Id $PID).StartTime.ToUniversalTime().ToString('o')
    $runningDir = New-FixtureRunDirectory -Name 'running' -Metadata @{ processId = $PID; processStartTime = $selfStartTime }
    $runningStatus = Get-DetachedRunStatus -RunDirectory $runningDir
    if ($runningStatus.State -cne 'Running') {
        throw "Running fixture (this test's own live process): got State=$($runningStatus.State), want Running"
    }

    $mismatchedStartDir = New-FixtureRunDirectory -Name 'mismatched-start-time' -Metadata @{ processId = $PID; processStartTime = '2000-01-01T00:00:00Z' }
    $mismatchedStatus = Get-DetachedRunStatus -RunDirectory $mismatchedStartDir
    if ($mismatchedStatus.State -cne 'Unknown') {
        throw "Mismatched start-time fixture (live PID, wrong recorded start time -- guards against PID reuse): got State=$($mismatchedStatus.State), want Unknown"
    }

    # A short-lived process that has already exited by the time we check: a
    # PID Get-Process cannot find, with no exitcode.txt written -- this is
    # what "killed externally, bypassing the wrapper's own finally" looks
    # like from the outside.
    $shortLived = Start-Process -FilePath (Get-Command pwsh).Source -ArgumentList @('-NoProfile', '-NonInteractive', '-Command', 'exit 0') -WindowStyle Hidden -PassThru
    $shortLived.WaitForExit(10000) | Out-Null
    $exitedDir = New-FixtureRunDirectory -Name 'exited-without-file' -Metadata @{ processId = $shortLived.Id; processStartTime = '' }
    $exitedStatus = Get-DetachedRunStatus -RunDirectory $exitedDir
    if ($exitedStatus.State -cne 'Unknown') {
        throw "Exited-without-exitcode-file fixture: got State=$($exitedStatus.State), want Unknown"
    }

    $missingRunJsonRejected = $false
    try {
        Get-DetachedRunStatus -RunDirectory $scratchRoot | Out-Null
    } catch {
        $missingRunJsonRejected = $true
    }
    if (-not $missingRunJsonRejected) {
        throw 'Get-DetachedRunStatus did not reject a directory with no run.json'
    }
    Write-Host 'Get-DetachedRunStatus fixtures passed (Completed, malformed exit code, Running, PID-reuse guard, externally-killed, missing run.json).'

    # --- Wait-DetachedRun: timeout path -------------------------------------
    $neverCompletesDir = New-FixtureRunDirectory -Name 'never-completes' -Metadata @{ processId = $PID; processStartTime = $selfStartTime }
    $waitStart = [DateTimeOffset]::UtcNow
    $waited = Wait-DetachedRun -RunDirectory $neverCompletesDir -TimeoutSeconds 2 -PollIntervalSeconds 1
    $waitedSeconds = ([DateTimeOffset]::UtcNow - $waitStart).TotalSeconds
    if ($waited.State -cne 'Running') {
        throw "Wait-DetachedRun timeout fixture: got State=$($waited.State), want Running"
    }
    if ($waitedSeconds -lt 1.5) {
        throw "Wait-DetachedRun returned suspiciously fast ($waitedSeconds s) for a 2s timeout -- it may not actually be polling"
    }
    Write-Host "Wait-DetachedRun correctly reports Running after its timeout elapses (waited ${waitedSeconds}s)."

    # --- New-DetachedRun: -WhatIf starts nothing -----------------------------
    $whatIfRunDirectory = Join-Path $scratchRoot 'whatif-run'
    $whatIfResult = New-DetachedRun -ScriptPath $scriptPath -ScriptArguments @() -WorkingDirectory $projectRoot -RunDirectory $whatIfRunDirectory -WhatIf
    if ($null -ne $whatIfResult) {
        throw 'New-DetachedRun -WhatIf returned a result instead of $null'
    }
    if (Test-Path -LiteralPath $whatIfRunDirectory) {
        throw '-WhatIf created a run directory; it must not launch or leave anything behind'
    }
    Write-Host 'New-DetachedRun -WhatIf starts no process and creates no run directory.'

    # --- End-to-end: real detached dummy scripts -----------------------------
    $dummyDirectory = Join-Path $scratchRoot 'dummy-scripts'
    New-Item -ItemType Directory -Path $dummyDirectory -Force | Out-Null
    $dummyScripts = @{
        FastSuccess = Join-Path $dummyDirectory 'fast-success.ps1'
        Exit42      = Join-Path $dummyDirectory 'exit42.ps1'
        Throws      = Join-Path $dummyDirectory 'throws.ps1'
        Slow        = Join-Path $dummyDirectory 'slow.ps1'
    }
    Set-Content -LiteralPath $dummyScripts.FastSuccess -Value "Write-Output 'FAST_SUCCESS_MARKER'`nexit 0`n" -Encoding utf8
    # Mirrors release-image-gate.ps1's own real "image approved pending
    # canary" outcome -- the exact case that motivated this script (see its
    # doc comment) and the one manually verified against a raw & call before
    # this wrapper design was written.
    Set-Content -LiteralPath $dummyScripts.Exit42 -Value "Write-Output 'EXIT42_MARKER'`nexit 42`n" -Encoding utf8
    Set-Content -LiteralPath $dummyScripts.Throws -Value "Write-Output 'BEFORE_THROW_MARKER'`nthrow 'boom-marker-text'`n" -Encoding utf8
    Set-Content -LiteralPath $dummyScripts.Slow -Value "Write-Output 'SLOW_START_MARKER'`nStart-Sleep -Seconds 120`nexit 0`n" -Encoding utf8

    function Test-DetachedRunOutcome {
        param(
            [Parameter(Mandatory)][string]$Label,
            [Parameter(Mandatory)][string]$DummyScriptPath,
            [Parameter(Mandatory)][int]$WantExitCode,
            [Parameter(Mandatory)][string]$WantTranscriptMarker
        )
        $runDirectory = Join-Path $scratchRoot ("run-" + [Guid]::NewGuid().ToString('N').Substring(0, 8))
        $started = New-DetachedRun -ScriptPath $DummyScriptPath -WorkingDirectory $dummyDirectory -RunDirectory $runDirectory
        if ($null -eq $started -or $started.ProcessId -le 0) {
            throw "${Label}: New-DetachedRun did not report a process id"
        }
        $final = Wait-DetachedRun -RunDirectory $runDirectory -TimeoutSeconds 60 -PollIntervalSeconds 1
        if ($final.State -cne 'Completed') {
            throw "${Label}: run did not complete within 60s (State=$($final.State))"
        }
        if ($final.ExitCode -ne $WantExitCode) {
            throw "${Label}: exit code=$($final.ExitCode), want $WantExitCode"
        }
        $transcript = Get-Content -Raw -LiteralPath (Join-Path $runDirectory 'transcript.log')
        if (-not $transcript.Contains($WantTranscriptMarker, [StringComparison]::Ordinal)) {
            throw "${Label}: transcript does not contain expected marker '$WantTranscriptMarker'`n---`n$transcript"
        }
    }

    Test-DetachedRunOutcome -Label 'fast success' -DummyScriptPath $dummyScripts.FastSuccess -WantExitCode 0 -WantTranscriptMarker 'FAST_SUCCESS_MARKER'
    Test-DetachedRunOutcome -Label 'exit 42 (release-image-gate.ps1 pending-canary shape)' -DummyScriptPath $dummyScripts.Exit42 -WantExitCode 42 -WantTranscriptMarker 'EXIT42_MARKER'
    Test-DetachedRunOutcome -Label 'terminating error' -DummyScriptPath $dummyScripts.Throws -WantExitCode 1 -WantTranscriptMarker 'boom-marker-text'
    Write-Host 'End-to-end detached runs (fast success, exit 42, terminating error) all captured the correct exit code and transcript.'

    # --- End-to-end via the real CLI (run-detached.ps1 as a subprocess) -----
    function Invoke-RunDetachedCli {
        param([Parameter(Mandatory)][string[]]$CliArguments)
        $output = & pwsh -NoProfile -NonInteractive -File $scriptPath @CliArguments 2>&1 | Out-String
        return [pscustomobject]@{ ExitCode = $LASTEXITCODE; Output = $output }
    }

    $cliRunsDirectory = Join-Path $scratchRoot 'cli-runs'
    $cliWaitResult = Invoke-RunDetachedCli -CliArguments @(
        '-ScriptPath', $dummyScripts.FastSuccess,
        '-WorkingDirectory', $dummyDirectory,
        '-RunsDirectory', $cliRunsDirectory,
        '-RunName', 'cli-fast',
        '-Wait', '-TimeoutSeconds', '60', '-PollIntervalSeconds', '1'
    )
    if ($cliWaitResult.ExitCode -ne 0) {
        throw "CLI -Wait fixture: exit code=$($cliWaitResult.ExitCode), want 0`n$($cliWaitResult.Output)"
    }
    if (-not $cliWaitResult.Output.Contains('Detached run completed with exit code 0', [StringComparison]::Ordinal)) {
        throw "CLI -Wait fixture: unexpected output`n$($cliWaitResult.Output)"
    }
    Write-Host 'run-detached.ps1 -Wait CLI path completed with the correct propagated exit code.'

    $cliWhatIfResult = Invoke-RunDetachedCli -CliArguments @(
        '-ScriptPath', $dummyScripts.FastSuccess,
        '-WorkingDirectory', $dummyDirectory,
        '-RunsDirectory', $cliRunsDirectory,
        '-RunName', 'cli-whatif',
        '-WhatIf'
    )
    if ($cliWhatIfResult.ExitCode -ne 0) {
        throw "CLI -WhatIf fixture: exit code=$($cliWhatIfResult.ExitCode), want 0`n$($cliWhatIfResult.Output)"
    }
    if (@(Get-ChildItem -LiteralPath $cliRunsDirectory -Directory -Filter 'cli-whatif-*').Count -ne 0) {
        throw 'CLI -WhatIf fixture: a run directory was created despite -WhatIf'
    }
    Write-Host 'run-detached.ps1 -WhatIf CLI path starts nothing and creates no run directory.'

    $cliStartResult = Invoke-RunDetachedCli -CliArguments @(
        '-ScriptPath', $dummyScripts.Slow,
        '-WorkingDirectory', $dummyDirectory,
        '-RunsDirectory', $cliRunsDirectory,
        '-RunName', 'cli-slow'
    )
    if ($cliStartResult.ExitCode -ne 0) {
        throw "CLI start-without-wait fixture: exit code=$($cliStartResult.ExitCode), want 0`n$($cliStartResult.Output)"
    }
    $slowRunDirectory = (@($cliStartResult.Output -split "`r?`n") | Where-Object { $_ -like (Join-Path $cliRunsDirectory 'cli-slow-*') } | Select-Object -Last 1)
    if ([string]::IsNullOrWhiteSpace($slowRunDirectory) -or -not (Test-Path -LiteralPath $slowRunDirectory)) {
        throw "CLI start-without-wait fixture: could not find the printed run directory in output`n$($cliStartResult.Output)"
    }
    try {
        $attachResult = Invoke-RunDetachedCli -CliArguments @('-AttachRunDirectory', $slowRunDirectory, '-Wait', '-TimeoutSeconds', '2', '-PollIntervalSeconds', '1')
        if ($attachResult.ExitCode -ne 4) {
            throw "CLI -AttachRunDirectory -Wait timeout fixture: exit code=$($attachResult.ExitCode), want the still-running sentinel 4`n$($attachResult.Output)"
        }
        if (-not $attachResult.Output.Contains('still running', [StringComparison]::OrdinalIgnoreCase)) {
            throw "CLI -AttachRunDirectory -Wait timeout fixture: unexpected output`n$($attachResult.Output)"
        }
    } finally {
        $slowStatus = Get-DetachedRunStatus -RunDirectory $slowRunDirectory
        if ($slowStatus.State -eq 'Running') {
            Stop-Process -Id ([int]$slowStatus.Metadata.processId) -Force -ErrorAction SilentlyContinue
        }
    }
    Write-Host 'run-detached.ps1 start-without-wait, then -AttachRunDirectory -Wait against a still-running process, reports the still-running sentinel exit code 4.'
} finally {
    Remove-Item -LiteralPath $scratchRoot -Recurse -Force -ErrorAction SilentlyContinue
}

Write-Host 'All run-detached fixtures passed.'
