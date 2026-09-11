$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot

Push-Location $projectRoot
try {
    $candidateOutput = @(& git ls-files -z --cached --others --exclude-standard)
    $enumerationExit = $LASTEXITCODE
    $candidateFiles = @(($candidateOutput -join "`n").Split([char]0) | Where-Object { $_ -ne '' } | Sort-Object -Unique)
    if ($enumerationExit -ne 0 -or $candidateFiles.Count -eq 0) {
        throw 'cannot enumerate release files for secret scanning'
    }
    $forbiddenNames = @($candidateFiles | Where-Object {
        $_ -match '(^|[/\\])\.env\.production$' -or
        $_ -match '\.(pem|key|p12|pfx)$' -or
        $_ -match '(^|[/\\])secrets?([/\\]|$)'
    })
    if ($forbiddenNames.Count -ne 0) {
        throw "secret-like files are present in the release set: $($forbiddenNames -join ', ')"
    }

    $patterns = @(
        '-----BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY-----',
        'AKIA[0-9A-Z]{16}',
        'ASIA[0-9A-Z]{16}',
        'github_pat_[A-Za-z0-9_]{20,}',
        'gh[pousr]_[A-Za-z0-9]{30,}',
        'xox[baprs]-[A-Za-z0-9-]{20,}',
        'sk_live_[A-Za-z0-9]{16,}',
        'sk-[A-Za-z0-9]{32,}',
        'eyJ[A-Za-z0-9_-]{16,}\.eyJ[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}'
    )
    $findings = @()
    foreach ($pattern in $patterns) {
        # Git defines the release set, including force-added ignored files.
        # Explicit bounded batches preserve whitespace without scanning ignored
        # untracked runtime data or allowing a second set of ignore rules.
        for ($offset = 0; $offset -lt $candidateFiles.Count; $offset += 32) {
            $last = [Math]::Min($offset + 31, $candidateFiles.Count - 1)
            $batch = @($candidateFiles[$offset..$last] | ForEach-Object { './' + $_ })
            $matches = @(rg -l -I --pcre2 --no-ignore --hidden -- $pattern @batch 2>$null)
            if ($LASTEXITCODE -notin @(0, 1)) { throw 'secret scan failed to execute' }
            $findings += $matches
        }
    }
    $findings = @($findings | Sort-Object -Unique)
    if ($findings.Count -ne 0) {
        throw "possible secret material detected in: $($findings -join ', ')"
    }
} finally {
    Pop-Location
}

Write-Host 'Release file names and contents passed the secret-material gate.'
$global:LASTEXITCODE = 0
