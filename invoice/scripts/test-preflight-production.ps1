param(
    [string]$TargetScript = (Join-Path $PSScriptRoot 'preflight-production.ps1'),
    [string]$OnlyCase = ''
)
$ErrorActionPreference = 'Stop'
$pwsh = (Get-Process -Id $PID).Path
$sandbox = Join-Path ([IO.Path]::GetTempPath()) ('invoice-preflight-tests-' + [guid]::NewGuid().ToString('N'))
[void][IO.Directory]::CreateDirectory($sandbox)
$copy = Join-Path $sandbox 'preflight-production.ps1'
Copy-Item -LiteralPath $TargetScript -Destination $copy
# The integrity prerequisite is a sibling of this exact script copy. It cannot
# discover or invoke a real upstream tree from the synthetic test directory.
[IO.File]::WriteAllText((Join-Path $sandbox 'check-upstream-integrity.ps1'), '$global:LASTEXITCODE=0')
$cases = @(
    @{ Name='valid'; Exit=0; Message='Read-only production preflight passed' },
    @{ Name='df-empty'; Exit=1; Message='invalid host disk metadata' },
    @{ Name='df-one-row'; Exit=1; Message='invalid host disk metadata' },
    @{ Name='df-header'; Exit=1; Message='invalid host disk metadata' },
    @{ Name='df-malformed'; Exit=1; Message='invalid host disk metadata' },
    @{ Name='df-number'; Exit=1; Message='invalid host disk metadata' },
    @{ Name='df-percentage'; Exit=1; Message='invalid host disk metadata' },
    @{ Name='df-mount'; Exit=1; Message='invalid host disk metadata' },
    @{ Name='df-full'; Exit=1; Message='above 80% use' },
    @{ Name='df-79'; Exit=0; Message='Read-only production preflight passed' },
    @{ Name='net-empty'; Exit=1; Message='invalid Docker network metadata' },
    @{ Name='net-malformed'; Exit=1; Message='invalid Docker network metadata' },
    @{ Name='net-name'; Exit=1; Message='invalid Docker network metadata' },
    @{ Name='net-internal'; Exit=1; Message='invalid Docker network metadata' },
    @{ Name='net-cidr'; Exit=1; Message='invalid Docker network metadata' },
    @{ Name='net-prefix'; Exit=1; Message='invalid Docker network metadata' },
    @{ Name='net-ipv6-prefix'; Exit=1; Message='invalid Docker network metadata' },
    @{ Name='net-empty-entry'; Exit=1; Message='invalid Docker network metadata' },
    @{ Name='net-overlap'; Exit=1; Message='overlaps Docker network' },
    @{ Name='net-builtins'; Exit=0; Message='Read-only production preflight passed' },
    @{ Name='net-ipv6'; Exit=0; Message='Read-only production preflight passed' },
    @{ Name='net-approved'; Exit=0; Message='Read-only production preflight passed' },
    @{ Name='version-prefix'; Exit=1; Message='New API image drift' },
    @{ Name='version-wrong'; Exit=1; Message='New API image drift' },
    @{ Name='version-digest'; Exit=0; Message='Read-only production preflight passed' },
    @{ Name='version-registry-port'; Exit=0; Message='Read-only production preflight passed' },
    @{ Name='version-status'; Exit=1; Message='New API image drift' },
    @{ Name='version-no-tag'; Exit=1; Message='New API image drift' },
    @{ Name='version-case'; Exit=1; Message='New API image drift' },
    @{ Name='version-bad-digest'; Exit=1; Message='New API image drift' }
)
if ($OnlyCase) { $cases = @($cases | Where-Object Name -eq $OnlyCase) }
if ($cases.Count -eq 0) { throw 'no preflight fixture cases selected' }
$failures = 0
foreach ($case in $cases) {
    $output = (& $pwsh -NoLogo -NoProfile -NonInteractive -File (Join-Path $PSScriptRoot 'test-support/preflight-production-case.ps1') -TargetScript $copy -Case $case.Name 2>&1 | Out-String)
    $code = $LASTEXITCODE
    if ($code -ne $case.Exit -or -not $output.Contains($case.Message)) {
        $failures++
        Write-Host "FAIL $($case.Name): expected exit $($case.Exit) / $($case.Message); actual exit $code"
        Write-Host $output
    } else { Write-Host "PASS $($case.Name): exit $code" }
}
if ($failures) { throw "$failures preflight fixture assertions failed; fixtures retained at $sandbox" }
Write-Host "All $($cases.Count) preflight fixture cases passed; no real external requests."
