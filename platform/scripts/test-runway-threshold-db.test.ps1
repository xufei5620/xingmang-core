$ErrorActionPreference = 'Stop'
$previous = $env:XM_TEST_DATABASE_ADMIN_URL
$previousTest = $env:XM_TEST_DATABASE_URL
$previousIsolated = $env:XM_RUNWAY_TEST_ISOLATED
$script = Join-Path $PSScriptRoot 'test-runway-threshold-db.ps1'
function Expect-NativeRejection([string]$Pattern) {
  $output = & pwsh -NoProfile -File $script 2>&1
  $code = $LASTEXITCODE
  if ($code -ne 1 -or ($output -join "`n") -notmatch $Pattern) {
    throw "runway guard did not reject for $Pattern (exit=$code)"
  }
}
function Expect-HarnessRejection([string]$Mode, [string]$Pattern) {
  $global:runwayHarnessTestMode = $Mode
  $rejected = $false
  try { & $script } catch {
    if ($_.Exception.Message -notmatch $Pattern) { throw }
    $rejected = $true
  }
  if (-not $rejected) { throw "runway harness accepted $Mode" }
}
try {
  Remove-Item Env:XM_TEST_DATABASE_ADMIN_URL -ErrorAction SilentlyContinue
  Remove-Item Env:XM_TEST_DATABASE_URL -ErrorAction SilentlyContinue
  $env:XM_RUNWAY_TEST_ISOLATED = '1'
  Expect-NativeRejection 'XM_TEST_DATABASE_ADMIN_URL'
  $env:XM_TEST_DATABASE_ADMIN_URL = 'postgres://u@127.0.0.1:65432/xm_admin'
  Expect-NativeRejection 'XM_TEST_DATABASE_URL'
  $env:XM_TEST_DATABASE_URL = 'postgres://u@127.0.0.1:65432/xm_test'
  # Every Go call is local synthetic output; neither validation nor a DB test runs.
  function go {
    $global:LASTEXITCODE = 0
    if ($args[0] -ne 'test') { return 'ok' }
    switch ($global:runwayHarnessTestMode) {
      'empty' { 'testing: warning: no tests to run'; 'PASS' }
      'skip' { '--- PASS: TestControl (0.00s)'; '--- SKIP: TestFixture (0.00s)' }
      'failed' { $global:LASTEXITCODE = 7; '--- PASS: TestControl (0.00s)'; 'FAIL' }
      default { '=== RUN   TestFixture'; '--- PASS: TestFixture (0.00s)'; 'PASS' }
    }
  }
  Expect-HarnessRejection 'empty' 'no tests passed'
  Expect-HarnessRejection 'skip' 'skipped'
  Expect-HarnessRejection 'failed' 'exit code 7'
  $global:runwayHarnessTestMode = 'passed'
  & $script
} finally {
  if ($null -eq $previous) { Remove-Item Env:XM_TEST_DATABASE_ADMIN_URL -ErrorAction SilentlyContinue } else { $env:XM_TEST_DATABASE_ADMIN_URL = $previous }
  if ($null -eq $previousTest) { Remove-Item Env:XM_TEST_DATABASE_URL -ErrorAction SilentlyContinue } else { $env:XM_TEST_DATABASE_URL = $previousTest }
  if ($null -eq $previousIsolated) { Remove-Item Env:XM_RUNWAY_TEST_ISOLATED -ErrorAction SilentlyContinue } else { $env:XM_RUNWAY_TEST_ISOLATED = $previousIsolated }
}
Write-Host 'runway DB harness guard test passed'
