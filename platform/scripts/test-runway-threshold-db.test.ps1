$ErrorActionPreference = 'Stop'
$previous = $env:XM_TEST_DATABASE_ADMIN_URL
$previousTest = $env:XM_TEST_DATABASE_URL
$previousIsolated = $env:XM_RUNWAY_TEST_ISOLATED
try {
  Remove-Item Env:XM_TEST_DATABASE_ADMIN_URL -ErrorAction SilentlyContinue
  Remove-Item Env:XM_TEST_DATABASE_URL -ErrorAction SilentlyContinue
  $env:XM_RUNWAY_TEST_ISOLATED = '1'
  try { pwsh -NoProfile -File $PSScriptRoot/test-runway-threshold-db.ps1 } catch { if ($_.Exception.Message -notmatch 'XM_TEST_DATABASE_ADMIN_URL') { throw } }
  $env:XM_TEST_DATABASE_ADMIN_URL = 'postgres://u@127.0.0.1:65432/xm_admin'
  try { pwsh -NoProfile -File $PSScriptRoot/test-runway-threshold-db.ps1 } catch { if ($_.Exception.Message -notmatch 'XM_TEST_DATABASE_URL') { throw } }
} finally {
  if ($null -eq $previous) { Remove-Item Env:XM_TEST_DATABASE_ADMIN_URL -ErrorAction SilentlyContinue } else { $env:XM_TEST_DATABASE_ADMIN_URL = $previous }
  if ($null -eq $previousTest) { Remove-Item Env:XM_TEST_DATABASE_URL -ErrorAction SilentlyContinue } else { $env:XM_TEST_DATABASE_URL = $previousTest }
  if ($null -eq $previousIsolated) { Remove-Item Env:XM_RUNWAY_TEST_ISOLATED -ErrorAction SilentlyContinue } else { $env:XM_RUNWAY_TEST_ISOLATED = $previousIsolated }
}
Write-Host 'runway DB harness guard test passed'
