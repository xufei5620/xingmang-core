<#!
.SYNOPSIS
  Create a random, disposable PostgreSQL 18 cluster, apply migrations, and run
  the DS1 schema integration tests.  The harness never accepts a caller DSN and
  always destroys its own Compose project and volume in finally.
#>
[CmdletBinding()]
param(
  [string]$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).ProviderPath,
  [switch]$ValidateOnly
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Sanitize([string]$Text) {
  if ($null -eq $Text) { return '' }
  if ($script:password) { $Text = $Text.Replace($script:password, '<redacted>') }
  return [regex]::Replace($Text, '(?i)(postgres(?:ql)?://)[^\s"'']+', '$1<redacted>')
}
function Fail([string]$Message) { throw "DS1 PG HARNESS FAIL: $(Sanitize $Message)" }
function Native([string[]]$NativeArguments, [switch]$AllowFailure) {
  $out = @(& docker @NativeArguments 2>&1)
  $code = $LASTEXITCODE
  if (-not $AllowFailure -and $code -ne 0) { Fail (($out -join "`n").Trim()) }
  return [pscustomobject]@{ Code = $code; Output = $out }
}
function Join-Output($Output) { return (($Output | ForEach-Object { [string]$_ }) -join "`n") }
function NativeOutput([string[]]$NativeArguments) { return (Native $NativeArguments).Output }

$compose = Join-Path $RepoRoot 'tests/security/metric-rollup-cluster.compose.yaml'
$migration = Join-Path $RepoRoot 'db/migrations/000019_metric_downsampling.up.sql'
$lock = Join-Path $RepoRoot 'VERSIONS.lock'
if (-not (Test-Path -LiteralPath $compose) -or -not (Test-Path -LiteralPath $migration)) { Fail 'required harness files are missing' }
$composeText = Get-Content -LiteralPath $compose -Raw -Encoding UTF8
$lockText = Get-Content -LiteralPath $lock -Raw -Encoding UTF8
$digest = [regex]::Match($lockText, '(?m)^\s*postgres\.digest\s*=\s*(sha256:[0-9a-f]{64})\s*$')
if (-not $digest.Success -or $composeText -notmatch [regex]::Escape("postgres:18@$($digest.Groups[1].Value)")) { Fail 'compose image does not match VERSIONS.lock' }
if ($composeText -notmatch '127\.0\.0\.1::5432') { Fail 'compose must publish loopback ephemeral port' }
if ($composeText -match '(?i)0\.0\.0\.0|network_mode:\s*host|external:\s*true') { Fail 'unsafe compose boundary' }

if ($ValidateOnly) {
  Write-Output 'DS1 PG HARNESS VALIDATION PASS'
  exit 0
}

foreach ($name in @('XM_ROLLUP_TEST_DATABASE_URL','XM_TEST_DATABASE_URL','DATABASE_URL','PGPASSWORD','DOCKER_HOST','COMPOSE_FILE','COMPOSE_PROJECT_NAME')) {
  if (-not [string]::IsNullOrEmpty([Environment]::GetEnvironmentVariable($name))) { Fail "external override $name is forbidden" }
}
$runId = 'ds1-' + [guid]::NewGuid().ToString('N')
$project = 'xm-ds1-' + $runId.Substring(4, 12)
$database = 'xm_rollup_' + $runId.Substring(4, 12)
$volume = $project + '_data'
$temp = Join-Path ([IO.Path]::GetTempPath()) $runId
New-Item -ItemType Directory -Path $temp -Force | Out-Null
$passwordFile = Join-Path $temp 'postgres.password'
$password = [guid]::NewGuid().ToString('N') + [guid]::NewGuid().ToString('N')
[IO.File]::WriteAllText($passwordFile, $password)
$env:DS1_RUN_ID = $runId
$env:DS1_DATABASE = $database
$env:DS1_VOLUME_NAME = $volume
$env:DS1_PASSWORD_FILE = $passwordFile
$composeArgs = @('compose','-p',$project,'-f',$compose)
$started = $false
try {
  Native ($composeArgs + @('config','--quiet')) | Out-Null
  $started = $true
  Native ($composeArgs + @('up','-d','--wait','postgres')) | Out-Null
  $portText = Join-Output (NativeOutput ($composeArgs + @('port','postgres','5432')))
  $match = [regex]::Match($portText.Trim(), '(?m)^\s*(?<host>[^:\s]+):(?<port>[0-9]+)\s*$')
  if (-not $match.Success -or $match.Groups['host'].Value -ne '127.0.0.1') { Fail 'discovered port is not loopback' }
  $port = [int]$match.Groups['port'].Value
  if ($port -le 1024 -or $port -eq 5432) { Fail "port is not ephemeral: $port" }
  $serverVersion = (Join-Output (NativeOutput ($composeArgs + @('exec','-T','postgres','psql','-U','postgres','-d',$database,'-At','-c',"SELECT (current_setting('server_version_num')::int / 10000);")))).Trim()
  if ($serverVersion -ne '18') { Fail "server_version_num major is not 18: $serverVersion" }
  $containerId = (Join-Output (NativeOutput ($composeArgs + @('ps','-q','postgres')))).Trim()
  if ($containerId -notmatch '^[0-9a-f]{12,64}$') { Fail 'postgres container id is missing' }
  $projectLabel = (Join-Output (NativeOutput @('inspect','--format','{{index .Config.Labels "com.docker.compose.project"}}',$containerId))).Trim()
  $serviceLabel = (Join-Output (NativeOutput @('inspect','--format','{{index .Config.Labels "com.docker.compose.service"}}',$containerId))).Trim()
  $runLabel = (Join-Output (NativeOutput @('inspect','--format','{{index .Config.Labels "com.xingmang.ds1.run-id"}}',$containerId))).Trim()
  $managedLabel = (Join-Output (NativeOutput @('inspect','--format','{{index .Config.Labels "com.xingmang.ds1.managed"}}',$containerId))).Trim()
  if ($projectLabel -ne $project -or $serviceLabel -ne 'postgres' -or $runLabel -ne $runId -or $managedLabel -ne 'true') { Fail 'container/project/run labels do not match this harness' }
  $imageID = (Join-Output (NativeOutput @('inspect','--format','{{.Image}}',$containerId))).Trim()
  $repoDigests = Join-Output (NativeOutput @('image','inspect','--format','{{json .RepoDigests}}',$imageID))
  if ($repoDigests -notmatch [regex]::Escape($digest.Groups[1].Value)) { Fail 'actual postgres RepoDigest differs from VERSIONS.lock' }
  $mountName = (Join-Output (NativeOutput @('inspect','--format','{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql"}}{{.Name}}{{end}}{{end}}',$containerId))).Trim()
  if ($mountName -ne $volume) { Fail 'postgres data mount is not this run volume' }
  $volumeLabels = Join-Output (NativeOutput @('volume','inspect','--format','{{json .Labels}}',$volume))
  if ($volumeLabels -notmatch [regex]::Escape($runId) -or $volumeLabels -notmatch 'com.xingmang.ds1.managed') { Fail 'volume labels do not match this harness' }
  $freshCatalog = (Join-Output (NativeOutput ($composeArgs + @('exec','-T','postgres','psql','-U','postgres','-d',$database,'-At','-c',"SELECT count(*) FROM pg_namespace WHERE nspname IN ('core','action','audit','ops','alerts','finance');")))).Trim()
  if ($freshCatalog -ne '0') { Fail "fresh catalog check failed: $freshCatalog" }
  $dsn = "postgres://postgres:$password@127.0.0.1`:$port/${database}?sslmode=disable&application_name=ds1-harness"
  $env:XM_DATABASE_URL = $dsn
  # Apply the complete committed migration chain through the lifecycle command.
  Push-Location $RepoRoot
  try { & go run ./cmd/migrate -database $dsn up; if ($LASTEXITCODE -ne 0) { Fail 'platform migrations failed' } }
  finally { Pop-Location }
  # The DS1 gate must see the newly-created metric_observation_daily table,
  # not merely a successful migration process exit.
  $dailyTable = (Join-Output (NativeOutput ($composeArgs + @('exec','-T','postgres','psql','-U','postgres','-d',$database,'-At','-c',"SELECT to_regclass('ops.metric_observation_daily');")))).Trim()
  if ($dailyTable -ne 'ops.metric_observation_daily') { Fail "metric_observation_daily is missing after migrations: $dailyTable" }
  $env:XM_ROLLUP_TEST_DATABASE_URL = $dsn
  $env:XM_ROLLUP_EXPECTED_DATABASE = $database
  $env:XM_ROLLUP_EXPECTED_SENTINEL = 'xingmang-metric-rollup-disposable'
  # Mark the database after migrations, then run the tagged schema gate.
  $commentSql = "COMMENT ON DATABASE `"$database`" IS 'xingmang-metric-rollup-disposable';"
  & docker @($composeArgs + @('exec','-T','postgres','psql','-U','postgres','-d',$database,'-v','ON_ERROR_STOP=1','-c',$commentSql)) | Out-Null
  $sentinel = (Join-Output (NativeOutput ($composeArgs + @('exec','-T','postgres','psql','-U','postgres','-d',$database,'-At','-c',"SELECT COALESCE((SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname=current_database()), '');")))).Trim()
  if ($sentinel -ne 'xingmang-metric-rollup-disposable') { Fail 'database comment sentinel mismatch' }
  Push-Location $RepoRoot
  try { & go test -tags=rolluppg -p 1 ./internal/platform/ops -run 'Test(RollupSchema|DailySchema|DailyUnique|Receipt|State|Migration|Periodic|Cadence|EveryConfigured)' -count=1 -v; if ($LASTEXITCODE -ne 0) { Fail 'DS1 PG schema tests failed' } }
  finally { Pop-Location }
  Write-Output "DS1 PG HARNESS PASS: project=$project port=$port database=$database"
}
finally {
  Remove-Item Env:XM_DATABASE_URL -ErrorAction SilentlyContinue
  Remove-Item Env:XM_ROLLUP_TEST_DATABASE_URL -ErrorAction SilentlyContinue
  Remove-Item Env:XM_ROLLUP_EXPECTED_DATABASE -ErrorAction SilentlyContinue
  Remove-Item Env:XM_ROLLUP_EXPECTED_SENTINEL -ErrorAction SilentlyContinue
  if ($started) {
    Native ($composeArgs + @('down','--volumes','--remove-orphans')) -AllowFailure | Out-Null
    $leftContainers = (Join-Output (NativeOutput @('ps','-aq','--filter',"label=com.docker.compose.project=$project"))).Trim()
    $leftVolumes = (Join-Output (NativeOutput @('volume','ls','-q','--filter',"label=com.xingmang.ds1.run-id=$runId"))).Trim()
    if ($leftContainers -or $leftVolumes) { Fail 'disposable project/volume remains after teardown' }
  }
  Remove-Item Env:DS1_RUN_ID -ErrorAction SilentlyContinue
  Remove-Item Env:DS1_DATABASE -ErrorAction SilentlyContinue
  Remove-Item Env:DS1_VOLUME_NAME -ErrorAction SilentlyContinue
  Remove-Item Env:DS1_PASSWORD_FILE -ErrorAction SilentlyContinue
  if (Test-Path -LiteralPath $temp) { Remove-Item -LiteralPath $temp -Recurse -Force -ErrorAction SilentlyContinue }
}
