param(
  [string]$GoTest = './internal/platform/finance',
  [string]$Run = 'TestRunwayThresholdStore'
)

# This is an external-DSN scaffold, not an automatic database lifecycle.  It
# deliberately fails closed when either DSN/isolated guard is missing so a
# skipped integration test cannot be reported as a green database gate.
$ErrorActionPreference = 'Stop'
if (-not $env:XM_TEST_DATABASE_ADMIN_URL) {
  throw 'XM_TEST_DATABASE_ADMIN_URL is required for the disposable DB harness'
}
if (-not $env:XM_TEST_DATABASE_URL) {
  throw 'XM_TEST_DATABASE_URL is required; refusing to run tests that would otherwise skip the database checks'
}
if ($env:XM_RUNWAY_TEST_ISOLATED -ne '1') {
  throw 'STOP: set XM_RUNWAY_TEST_ISOLATED=1 only for a disposable database; shared DB cleanup is forbidden'
}

go run ./cmd/pgdsn-validate --dsn-env XM_TEST_DATABASE_ADMIN_URL --require-loopback
if ($LASTEXITCODE -ne 0) { throw 'test database DSN guard rejected the admin URL' }
go run ./cmd/pgdsn-validate --dsn-env XM_TEST_DATABASE_URL --require-loopback
if ($LASTEXITCODE -ne 0) { throw 'test database DSN guard rejected the test URL' }

Write-Host 'Runway threshold DB harness scaffold: external disposable DSNs accepted; full migration/role lifecycle is not implemented.'
$output = & go test -v -p 1 $GoTest -run $Run -count=1 2>&1
$goExit = $LASTEXITCODE
$output | Write-Host
if ($goExit -ne 0) { throw "go test failed with exit code $goExit" }
if ($output -match '(?m)^\s*--- SKIP:') {
  throw 'database test run skipped; refusing to report a green harness'
}
