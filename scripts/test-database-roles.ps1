<#!
.SYNOPSIS
  Creates, verifies, and destroys one disposable PostgreSQL 18 role-policy
  cluster.  This script never accepts an administrator DSN and never targets
  the shared xingmang-launch stack.

.DESCRIPTION
  DBR1 is a transport/permission harness only.  Compose infrastructure names
  and the database name are random per invocation; role and object names in the
  fixture remain the fixed policy names.  The cluster is always torn down in a
  finally block, including after a failed probe.

  -ValidateOnly runs all static guards without Docker.  A full run owns its
  random Compose project from creation through label-verified destruction.
#>
[CmdletBinding()]
param(
  [string]$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).ProviderPath,
  [string]$ComposeFile = '',
  [string]$FixtureFile = '',
  [switch]$ValidateOnly
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Fail([string]$Message) {
  throw "DBR1 FAIL: $Message"
}

function Resolve-RepoFile([string]$Path, [string]$DefaultRelative) {
  if ([string]::IsNullOrWhiteSpace($Path)) {
    $Path = Join-Path $RepoRoot $DefaultRelative
  } elseif (-not [IO.Path]::IsPathRooted($Path)) {
    $Path = Join-Path $RepoRoot $Path
  }
  try { $resolved = (Resolve-Path -LiteralPath $Path -ErrorAction Stop).ProviderPath }
  catch { Fail "文件不存在: $Path" }
  $root = [IO.Path]::GetFullPath($RepoRoot).TrimEnd([IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar)
  $full = [IO.Path]::GetFullPath($resolved)
  $prefix = $root + [IO.Path]::DirectorySeparatorChar
  if (-not $full.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) {
    Fail "文件必须位于 RepoRoot 内: $full"
  }
  return $full
}

function Join-NativeOutput($Output) {
  return (($Output | ForEach-Object { [string]$_ }) -join "`n")
}

function Sanitize([string]$Text, [string]$Secret = '') {
  if ($null -eq $Text) { return '' }
  if ($Secret) { $Text = $Text.Replace($Secret, '<redacted>') }
  # Do not let an accidental native error echo a credential-bearing URL.
  return [regex]::Replace($Text, '(?i)(postgres(?:ql)?://)[^\s"'']+', '$1<redacted>')
}

function Invoke-NativeResult([string]$Tool, [string[]]$Arguments) {
  $output = @(& $Tool @Arguments 2>&1)
  $code = $LASTEXITCODE
  return [pscustomobject]@{ ExitCode = $code; Output = $output }
}

function Invoke-NativeChecked([string]$Tool, [string[]]$Arguments, [string]$Label, [string]$Secret = '') {
  $result = Invoke-NativeResult $Tool $Arguments
  if ($result.ExitCode -ne 0) {
    $details = Sanitize (Join-NativeOutput $result.Output) $Secret
    if ($details.Length -gt 1200) { $details = $details.Substring(0, 1200) }
    if ($details) { Fail "$Label 失败(exit=$($result.ExitCode)): $details" }
    Fail "$Label 失败(exit=$($result.ExitCode))"
  }
  return @($result.Output)
}

function Assert-ExternalInputsAbsent {
  # These values are deliberately rejected rather than overridden.  Accepting
  # a caller DSN would make it possible to point the harness at staging or
  # production while the output still looked like a disposable test.
  $forbidden = @(
    'DATABASE_URL', 'XM_TEST_DATABASE_URL', 'XM_TEST_DATABASE_ADMIN_URL',
    'DBR_DATABASE_URL', 'DBR_ADMIN_DSN', 'XM_DATABASE_DSN',
    'PGSERVICE', 'PGSERVICEFILE', 'PGPASSFILE', 'PGPASSWORD',
    'DOCKER_HOST'
  )
  foreach ($name in $forbidden) {
    $value = [Environment]::GetEnvironmentVariable($name)
    if (-not [string]::IsNullOrEmpty($value)) {
      Fail "拒绝外部连接/凭据环境变量 $name；DBR1 只接受本次内部构造的 loopback DSN"
    }
  }
  $composeOverrides = @('COMPOSE_FILE', 'COMPOSE_PROJECT_NAME', 'COMPOSE_PROFILES')
  foreach ($name in $composeOverrides) {
    $value = [Environment]::GetEnvironmentVariable($name)
    if (-not [string]::IsNullOrEmpty($value)) {
      Fail "拒绝 Compose 覆盖环境变量 $name；项目与文件必须由 harness 固定"
    }
  }
  if ($env:DOCKER_CONTEXT -and $env:DOCKER_CONTEXT -ne 'default') {
    Fail '拒绝非 default Docker context；请在本机 Docker daemon 上运行 disposable harness'
  }
}

function Read-ImagePin([string]$LockFile) {
  $text = Get-Content -LiteralPath $LockFile -Raw -Encoding UTF8
  $imageMatch = [regex]::Match($text, '(?m)^\s*postgres\.image\s*=\s*(\S+)\s*$')
  $digestMatch = [regex]::Match($text, '(?m)^\s*postgres\.digest\s*=\s*(sha256:[0-9a-f]{64})\s*$')
  if (-not $imageMatch.Success -or -not $digestMatch.Success) {
    Fail 'VERSIONS.lock 缺少精确 postgres.image/postgres.digest'
  }
  if ($imageMatch.Groups[1].Value -ne 'docker.io/library/postgres:18') {
    Fail "VERSIONS.lock postgres.image 非 PostgreSQL 18: $($imageMatch.Groups[1].Value)"
  }
  return [pscustomobject]@{
    Image = $imageMatch.Groups[1].Value
    Digest = $digestMatch.Groups[1].Value
    Reference = "$($imageMatch.Groups[1].Value)@$($digestMatch.Groups[1].Value)"
  }
}

function Validate-ComposeStatic([string]$Path, $Pin) {
  $text = Get-Content -LiteralPath $Path -Raw -Encoding UTF8
  $expected = [regex]::Escape($Pin.Reference)
  $imageCount = ([regex]::Matches($text, "(?m)^\s*image:\s*$expected\s*$")).Count
  if ($imageCount -ne 1) { Fail "compose 必须恰好使用锁定镜像 $($Pin.Reference)" }
  if (([regex]::Matches($text, '127\.0\.0\.1::5432')).Count -ne 1) {
    Fail 'compose 的 postgres 唯一 host 映射必须是 127.0.0.1::5432'
  }
  if ($text -match '(?m)^\s*-\s*["'']?(?:0\.0\.0\.0|::):') {
    Fail 'compose 禁止 wildcard host bind (0.0.0.0/::)'
  }
  if ($text -match '(?m)127\.0\.0\.1:\d+:5432') {
    Fail 'compose 禁止固定 host 端口'
  }
  if ($text -match '(?im)^\s*(?:network_mode\s*:\s*host|external\s*:\s*true)') {
    Fail 'compose 禁止 host network 或 external network/volume'
  }
  if ($text -match '(?m)^\s*-\s*(?:[A-Za-z]:[\\/]|/|~)[^\r\n]*:/var/lib/postgresql(?:/|\s|$)') {
    Fail 'compose 禁止把宿主路径 bind mount 到 PostgreSQL 数据目录'
  }
  if ($text -match '(?im)postgres(?::\d+(?:\.\d+)*)?\s*(?:$|[\s#])') {
    # The exact image line above is allowed; this guard catches an additional
    # tag-only image declaration anywhere else in the file.
    $tagOnly = [regex]::Matches($text, '(?im)image:\s*[^\s#]+postgres[^\s#]*') |
      Where-Object { $_.Value -notmatch '@sha256:[0-9a-f]{64}' }
    if (@($tagOnly).Count -gt 0) { Fail 'compose 含未钉 digest 的 postgres 镜像' }
  }
  foreach ($required in @('DBR_RUN_ID', 'DBR_DATABASE', 'DBR_VOLUME_NAME', 'DBR_PASSWORD_FILE', 'DBR_FIXTURE_FILE',
                           'com.docker.compose.project', 'com.xingmang.dbr1.run-id')) {
    # com.docker.compose.project is injected by Compose; the other markers are
    # explicit in the file or script and checked below.
    if ($required -notin @('com.docker.compose.project') -and $text -notmatch [regex]::Escape($required)) {
      Fail "compose 缺少 disposable marker $required"
    }
  }
  if ($text -match '(?im)\b(?:DATABASE_URL|DBR_ADMIN_DSN)\b') {
    Fail 'disposable compose 不得接受外部 DATABASE_URL/管理员 DSN'
  }
}

function Validate-FixtureStatic([string]$Path) {
  $text = Get-Content -LiteralPath $Path -Raw -Encoding UTF8
  # Guard SQL tokens in executable text, not explanatory comments.
  $code = [regex]::Replace($text, '(?m)--[^\r\n]*', '')
  $code = [regex]::Replace($code, '(?s)/\*.*?\*/', '')
  foreach ($needle in @(
    'CREATE ROLE xm_api_runtime NOLOGIN',
    'CREATE ROLE xm_worker_runtime NOLOGIN',
    'CREATE ROLE xm_lifecycle_runtime NOLOGIN',
    'CREATE ROLE xm_ops_read NOLOGIN',
    'CREATE ROLE xm_backup_read NOLOGIN',
    'GRANT xm_api_runtime TO xm_api_a WITH INHERIT TRUE, SET FALSE, ADMIN FALSE',
    'GRANT xm_worker_runtime TO xm_worker_a WITH INHERIT TRUE, SET FALSE, ADMIN FALSE',
    'ALTER DATABASE %I OWNER TO xm_migrator',
    'REVOKE CONNECT, TEMPORARY ON DATABASE %I FROM PUBLIC',
    'CREATE RULE action_run_no_delete',
    'CREATE RULE audit_event_no_delete',
    'chain_root_guard',
    'river_job_state',
    'ALTER DEFAULT PRIVILEGES'
  )) {
    if ($text -notmatch [regex]::Escape($needle)) { Fail "fixture 缺少必需的角色/ACL 断言: $needle" }
  }
  if ($code -match '(?im)\bREASSIGN\s+OWNED\b|\bDROP\s+DATABASE\b') {
    Fail 'fixture 禁止 REASSIGN OWNED/DROP DATABASE'
  }
  if ($text -match '(?im)PASSWORD\s+[''"`][^''"`]+[''"`]') {
    Fail 'fixture 不得包含明文密码字面量'
  }
}

$RepoRoot = [IO.Path]::GetFullPath($RepoRoot)
if (-not (Test-Path -LiteralPath $RepoRoot -PathType Container)) { Fail "RepoRoot 不存在: $RepoRoot" }
$lockFile = Join-Path $RepoRoot 'VERSIONS.lock'
if (-not (Test-Path -LiteralPath $lockFile -PathType Leaf)) { Fail '缺少 VERSIONS.lock' }
$composePath = Resolve-RepoFile $ComposeFile 'tests/security/database-role-cluster.compose.yaml'
$fixturePath = Resolve-RepoFile $FixtureFile 'tests/security/fixtures/database-role-fixture.sql'
$pin = Read-ImagePin $lockFile
Validate-ComposeStatic $composePath $pin
Validate-FixtureStatic $fixturePath
Assert-ExternalInputsAbsent

if ($ValidateOnly) {
  Write-Output "DBR1 VALIDATION PASS: image=$($pin.Reference) binding=127.0.0.1::5432 fixture=guarded"
  exit 0
}

foreach ($tool in @('docker', 'go')) {
  if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) { Fail "缺少命令: $tool" }
}
$composeVersion = Invoke-NativeChecked 'docker' @('compose', 'version') 'Docker Compose version'
$null = $composeVersion

$runId = ([guid]::NewGuid().ToString('N')).Substring(0, 20)
$project = "xm-dbr1-$runId"
$database = "dbr1_$runId"
$volumeName = "xm-dbr1-data-$runId"
$pgHost = '127.0.0.1' # PGHOST is a constant; caller host overrides are rejected.
if ($project.Length -gt 63 -or $database.Length -gt 63 -or $volumeName.Length -gt 255) {
  Fail '随机 infrastructure 名称超出 Docker/PostgreSQL 限制'
}
$tempRoot = Join-Path ([IO.Path]::GetTempPath()) "xm-dbr1-$runId"
$secretPath = Join-Path $tempRoot 'postgres.password'
$composeEnvPath = Join-Path $tempRoot 'compose.env'
New-Item -ItemType Directory -Path $tempRoot -Force | Out-Null
$passwordBytes = New-Object byte[] 24
[Security.Cryptography.RandomNumberGenerator]::Fill($passwordBytes)
$password = [BitConverter]::ToString($passwordBytes).Replace('-', '').ToLowerInvariant()
[IO.File]::WriteAllText($secretPath, $password, [Text.UTF8Encoding]::new($false))
$envLines = @(
  "DBR_RUN_ID=$runId",
  "DBR_DATABASE=$database",
  "DBR_VOLUME_NAME=$volumeName",
  "DBR_PASSWORD_FILE=$secretPath",
  "DBR_FIXTURE_FILE=$fixturePath"
)
[IO.File]::WriteAllText($composeEnvPath, (($envLines -join "`n") + "`n"), [Text.UTF8Encoding]::new($false))

$composeBase = @('--project-name', $project, '--file', $composePath, '--env-file', $composeEnvPath)
$started = $false
$discoveredPort = 0
$teardownError = $null
$runStartedAt = [DateTimeOffset]::UtcNow

function Invoke-Compose([string[]]$Arguments, [string]$Label, [switch]$AllowFailure) {
  $result = Invoke-NativeResult 'docker' (@('compose') + $composeBase + $Arguments)
  if (-not $AllowFailure -and $result.ExitCode -ne 0) {
    $details = Sanitize (Join-NativeOutput $result.Output) $password
    if ($details.Length -gt 1200) { $details = $details.Substring(0, 1200) }
    if ($details) { Fail "$Label 失败(exit=$($result.ExitCode)): $details" }
    Fail "$Label 失败(exit=$($result.ExitCode))"
  }
  return $result
}

function Invoke-Psql([string]$Role, [string]$Sql, [switch]$AllowFailure) {
  if ($Role -notmatch '^[a-z_][a-z0-9_]{0,62}$') { Fail "非法 fixture role: $Role" }
  $args = @('exec', '--no-TTY', '--env', "PGPASSWORD=$password", 'postgres',
            'psql', '--no-psqlrc', '-X', '-v', 'ON_ERROR_STOP=1', '-v', 'VERBOSITY=verbose',
            '-h', $pgHost, '-U', $Role, '-d', $database, '-At', '-c', $Sql)
  $result = Invoke-Compose $args "psql($Role)" -AllowFailure:$AllowFailure
  if (-not $AllowFailure -and $result.ExitCode -ne 0) { Fail "psql($Role) failed" }
  return $result
}

function Invoke-PsqlFixture {
  $args = @('exec', '--no-TTY', '--env', "PGPASSWORD=$password", 'postgres',
            'psql', '--no-psqlrc', '-X', '-v', 'ON_ERROR_STOP=1', '-h', '127.0.0.1',
            '-U', 'postgres', '-d', $database, '-f', '/run/dbr1/fixture.sql')
  $result = Invoke-NativeResult 'docker' (@('compose') + $composeBase + $args)
  if ($result.ExitCode -ne 0) {
    $details = Sanitize (Join-NativeOutput $result.Output) $password
    if ($details.Length -gt 1200) { $details = $details.Substring(0, 1200) }
    if ($details) { Fail "fixture 执行失败(exit=$($result.ExitCode)): $details" }
    Fail "fixture 执行失败(exit=$($result.ExitCode))"
  }
}

function Assert-Query([string]$Role, [string]$Sql, [string]$Expected, [string]$Label) {
  $result = Invoke-Psql $Role $Sql
  $actual = (Join-NativeOutput $result.Output).Trim()
  if ($actual -ne $Expected) { Fail "${Label}: expected [$Expected], got [$actual]" }
}

function Assert-Denied([string]$Role, [string]$Sql, [string]$Label, [string[]]$Codes = @('42501', '55000')) {
  $result = Invoke-Psql $Role $Sql -AllowFailure
  if ($result.ExitCode -eq 0) {
    # PostgreSQL reports a GRANT without grant option as a successful command
    # plus warning 01007 (no privileges were granted).  Treat that as an ACL
    # denial only after checking the warning; a silent privilege change is not
    # accepted.
    if ((Join-NativeOutput $result.Output) -match '(?i)no privileges were granted') { return }
    $diagResult = Invoke-Psql $Role "SELECT current_user || ':' || session_user;" -AllowFailure
    $diagnostic = (Join-NativeOutput $diagResult.Output).Trim()
    $notice = (Join-NativeOutput $result.Output).Trim()
    if ($notice.Length -gt 300) { $notice = $notice.Substring(0, 300) }
    Fail "${Label} 未被拒绝 (role=$Role, current=$diagnostic, output=$notice)"
  }
  $text = Join-NativeOutput $result.Output
  $matched = $false
  foreach ($code in $Codes) { if ($text -match [regex]::Escape($code)) { $matched = $true } }
  if (-not $matched -and $text -notmatch '(?i)permission denied|must be owner|not permitted|immutable|read-only transaction') {
    $safe = Sanitize $text $password
    if ($safe.Length -gt 600) { $safe = $safe.Substring(0, 600) }
    Fail "${Label} 返回了非预期错误: $safe"
  }
}

function Assert-NoMutation([string]$Role, [string]$MutationSql, [string]$BeforeSql, [string]$Expected, [string]$Label) {
  $before = (Join-NativeOutput (Invoke-Psql 'postgres' $BeforeSql).Output).Trim()
  Invoke-Psql $Role $MutationSql -AllowFailure | Out-Null
  $after = (Join-NativeOutput (Invoke-Psql 'postgres' $BeforeSql).Output).Trim()
  if ($before -ne $Expected -or $after -ne $Expected) {
    Fail "${Label}: append-only row changed (before=$before after=$after)"
  }
}

try {
  # Compose config is rendered before any resource is created.  The generated
  # env file is the only source of infrastructure interpolation.
  Invoke-Compose @('config', '--quiet') 'Compose config' | Out-Null

  # A random project must never already own a resource.  Any collision is a
  # hard stop rather than an invitation to clean up someone else's container.
  foreach ($probe in @(
    @('ps', '-aq', '--filter', "label=com.docker.compose.project=$project"),
    @('network', 'ls', '-q', '--filter', "label=com.docker.compose.project=$project"),
    @('volume', 'ls', '-q', '--filter', "label=com.docker.compose.project=$project"),
    @('ps', '-aq', '--filter', "label=com.xingmang.dbr1.run-id=$runId"),
    @('volume', 'ls', '-q', '--filter', "label=com.xingmang.dbr1.run-id=$runId")
  )) {
    $kind = $probe[0]
    $result = Invoke-NativeChecked 'docker' $probe "pre-existing $kind label check"
    if ((Join-NativeOutput $result).Trim()) { Fail "随机 project/run-id 已有资源，拒绝复用" }
  }

  # Mark the project as owned before invoking Docker.  Even a partially
  # created service must be torn down if `up` exits non-zero.
  $started = $true
  $up = Invoke-Compose @('up', '-d', '--wait', 'postgres') 'start disposable postgres'
  $null = $up

  $portOutput = Invoke-Compose @('port', 'postgres', '5432') 'discover assigned postgres port'
  $portText = (Join-NativeOutput $portOutput.Output).Trim()
  $portMatch = [regex]::Match($portText, '(?m)^\s*(?<host>[^:\s]+):(?<port>[0-9]+)\s*$')
  if (-not $portMatch.Success) { Fail 'docker compose port 未返回 host:port' }
  if ($portMatch.Groups['host'].Value -ne '127.0.0.1') { Fail 'discovered postgres host 不是 127.0.0.1' }
  $discoveredPort = [int]$portMatch.Groups['port'].Value
  if ($discoveredPort -le 1024 -or $discoveredPort -eq 5432 -or $discoveredPort -gt 65535) {
    Fail "discovered postgres host port 非随机 ephemeral port: $discoveredPort"
  }

  $containerOutput = Invoke-Compose @('ps', '-q', 'postgres') 'discover postgres container'
  $containers = @((Join-NativeOutput $containerOutput.Output).Trim() -split "`r?`n" | Where-Object { $_ })
  if ($containers.Count -ne 1) { Fail "postgres service container 数量应为 1，实际 $($containers.Count)" }
  $containerId = $containers[0]
  $projectLabel = (Invoke-NativeChecked 'docker' @('inspect', '--format', '{{index .Config.Labels "com.docker.compose.project"}}', $containerId) 'container project label').Trim()
  $serviceLabel = (Invoke-NativeChecked 'docker' @('inspect', '--format', '{{index .Config.Labels "com.docker.compose.service"}}', $containerId) 'container service label').Trim()
  $runLabel = (Invoke-NativeChecked 'docker' @('inspect', '--format', '{{index .Config.Labels "com.xingmang.dbr1.run-id"}}', $containerId) 'container run label').Trim()
  if ($projectLabel -ne $project -or $serviceLabel -ne 'postgres' -or $runLabel -ne $runId) {
    Fail 'postgres container Compose/project/run labels 不匹配本次运行'
  }
  $stateJson = Join-NativeOutput (Invoke-NativeChecked 'docker' @('inspect', '--format', '{{json .State}}', $containerId) 'container state')
  $state = $stateJson | ConvertFrom-Json
  if (-not $state.Running -or ($state.Health -and $state.Health.Status -ne 'healthy')) {
    Fail 'postgres 容器未处于 running/healthy 状态'
  }

  $imageId = (Invoke-NativeChecked 'docker' @('inspect', '--format', '{{.Image}}', $containerId) 'container image id').Trim()
  $digestJson = Join-NativeOutput (Invoke-NativeChecked 'docker' @('image', 'inspect', '--format', '{{json .RepoDigests}}', $imageId) 'postgres RepoDigest')
  $repoDigests = $digestJson | ConvertFrom-Json
  $digestRegex = '@' + [regex]::Escape($pin.Digest) + '$'
  $digestMatched = @($repoDigests | Where-Object { [string]$_ -match $digestRegex }).Count -gt 0
  if (-not $digestMatched) { Fail "postgres RepoDigest 与 VERSIONS.lock 不一致" }

  # Inspect mounts: exactly one Compose-managed volume at the PG18 parent
  # directory; no host path or unrelated volume may be used.
  $inspectJson = Join-NativeOutput (Invoke-NativeChecked 'docker' @('inspect', '--format', '{{json .}}', $containerId) 'container metadata')
  $inspect = $inspectJson | ConvertFrom-Json
  $pgMounts = @($inspect.Mounts | Where-Object { $_.Destination -eq '/var/lib/postgresql' })
  if ($pgMounts.Count -ne 1 -or $pgMounts[0].Type -ne 'volume' -or $pgMounts[0].Name -ne $volumeName) {
    Fail 'postgres 数据目录不是本次随机命名的专用 volume'
  }
  $volumeJson = Join-NativeOutput (Invoke-NativeChecked 'docker' @('volume', 'inspect', $volumeName) 'volume metadata')
  $volume = @($volumeJson | ConvertFrom-Json)[0]
  if ($volume.Labels.'com.xingmang.dbr1.run-id' -ne $runId -or $volume.Labels.'com.xingmang.dbr1.managed' -ne 'true') {
    Fail 'volume run-id/managed label 不匹配'
  }
  try {
    $volumeCreatedAt = [DateTimeOffset]::Parse([string]$volume.CreatedAt)
    # Docker Desktop may report its daemon clock in the host's local offset
    # while PowerShell uses UTC.  The random volume name + preflight label
    # check is the authoritative freshness proof; retain a generous clock
    # sanity bound so a stale/reused volume still fails closed.
    $ageHours = [math]::Abs(($volumeCreatedAt.UtcDateTime - $runStartedAt.UtcDateTime).TotalHours)
    if ($ageHours -gt 24) {
      Fail "volume 创建时间明显早于本次 harness 运行，拒绝复用旧 cluster (created=$volumeCreatedAt run=$runStartedAt)"
    }
  } catch [FormatException] {
    Fail '无法解析 volume 创建时间，拒绝继续'
  }

  # Construct the DSN only after the port is discovered.  It deliberately has
  # no password and no host/query override.  pgdsn-validate calls the platform
  # pgdsn.Validate and pgdsn.RequireLoopback guards; PGPASSWORD is not present
  # in this parent process while validation runs.
  $dsn = "postgres://postgres@127.0.0.1:${discoveredPort}/${database}?sslmode=disable&application_name=dbr1-harness"
  $oldDsn = [Environment]::GetEnvironmentVariable('DBR_INTERNAL_DSN', 'Process')
  try {
    $env:DBR_INTERNAL_DSN = $dsn
    $validation = Invoke-NativeChecked 'go' @('run', './cmd/pgdsn-validate', '--dsn-env', 'DBR_INTERNAL_DSN', '--require-loopback', '--require-managed-password') 'internal DSN loopback/managed-password validation'
    $null = $validation
  } finally {
    if ($null -eq $oldDsn) { Remove-Item Env:DBR_INTERNAL_DSN -ErrorAction SilentlyContinue }
    else { $env:DBR_INTERNAL_DSN = $oldDsn }
  }
  if ($dsn -match '(?i)password=|@[^/]+:[^@]+@') { Fail '内部 DSN 含密码或 query host override' }

  Assert-Query 'postgres' "SELECT (current_setting('server_version_num')::int / 10000)::text;" '18' 'PostgreSQL server_version_num major'
  Assert-Query 'postgres' 'SELECT current_database();' $database 'current database fingerprint'
  Assert-Query 'postgres' "SELECT count(*) FROM pg_roles WHERE rolname LIKE 'xm_%';" '0' 'fresh role catalog'
  Assert-Query 'postgres' "SELECT count(*) FROM pg_namespace WHERE nspname IN ('core','action','audit','ops','alerts','finance');" '0' 'fresh platform schemas'

  Invoke-PsqlFixture
  # Set one anonymous per-run password for fixture login identities.  The
  # value is generated in memory and never printed or committed.
  foreach ($role in @('xm_migrator', 'xm_api_a', 'xm_worker_a', 'xm_lifecycle_a', 'xm_ops_a', 'xm_backup_a')) {
    Invoke-Psql 'postgres' "ALTER ROLE $role PASSWORD '$password';" | Out-Null
  }

  # Role attributes and memberships.
  Assert-Query 'postgres' "SELECT count(*) FROM pg_roles WHERE rolname LIKE 'xm_%' AND (rolsuper OR rolcreaterole OR rolcreatedb OR rolreplication OR rolbypassrls);" '0' 'runtime role attributes'
  Assert-Query 'postgres' "SELECT count(*) FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.roleid JOIN pg_roles u ON u.oid=m.member WHERE r.rolname LIKE 'xm_%' AND u.rolname LIKE 'xm_%';" '5' 'capability memberships'
  Assert-Query 'postgres' "SELECT has_database_privilege('public', current_database(), 'CONNECT');" 'f' 'PUBLIC database CONNECT revoked'
  Assert-Query 'postgres' "SELECT has_schema_privilege('public', 'public', 'USAGE');" 'f' 'PUBLIC schema USAGE revoked'
  Assert-Query 'postgres' "SELECT has_type_privilege('public', 'public.river_job_state', 'USAGE');" 'f' 'PUBLIC enum USAGE revoked'
  Assert-Query 'xm_ops_a' 'SHOW default_transaction_read_only;' 'on' 'ops read-only transaction'
  Assert-Query 'xm_backup_a' 'SHOW default_transaction_read_only;' 'on' 'backup read-only transaction'

  # Positive API/worker/lifecycle/ops/backup probes.
  Assert-Query 'xm_api_a' 'SELECT count(*) FROM core.environment;' '2' 'API environment SELECT'
  Invoke-Psql 'xm_api_a' "INSERT INTO action.action_run (id, action_id, principal_id, environment, status) VALUES ('00000000-0000-0000-0000-000000000010', 'platform.dbr1.probe', 'dbr1', 'staging', 'succeeded');" | Out-Null
  Invoke-Psql 'xm_api_a' "INSERT INTO audit.audit_event (id, sequence, principal_id, environment, result, prev_hash, event_hash) VALUES ('00000000-0000-0000-0000-000000000011', 1, 'dbr1', 'staging', 'succeeded', repeat('b',64), repeat('c',64));" | Out-Null
  Invoke-Psql 'xm_worker_a' "INSERT INTO ops.metric_observation_sample (metric_key, environment, status) VALUES ('dbr1.worker', 'staging', 'ok');" | Out-Null
  Invoke-Psql 'xm_worker_a' "UPDATE ops.metric_observation SET status='ok' WHERE metric_key='dbr1.fixture';" | Out-Null
  Invoke-Psql 'xm_worker_a' "INSERT INTO public.river_job (state, kind) VALUES ('available', 'dbr1.probe');" | Out-Null
  Assert-Query 'xm_worker_a' "SELECT has_type_privilege('xm_worker_a', 'public.river_job_state', 'USAGE');" 't' 'worker enum USAGE'
  Invoke-Psql 'xm_worker_a' "UPDATE audit.chain_root SET exported_at=now(), export_target='dbr1' WHERE id='00000000-0000-0000-0000-000000000001';" | Out-Null
  Assert-Query 'xm_lifecycle_a' 'SELECT count(*) FROM core.environment;' '2' 'lifecycle registry SELECT'
  Assert-Query 'xm_ops_a' 'SELECT count(*) FROM audit.audit_event;' '1' 'ops audit SELECT'
  Assert-Query 'xm_backup_a' 'SELECT count(*) FROM finance.profit_daily;' '0' 'backup finance SELECT'
  Assert-Query 'xm_backup_a' 'SELECT count(*) FROM ops.metric_observation_sample;' '1' 'backup sample SELECT'
  Assert-Query 'xm_backup_a' "SELECT has_sequence_privilege('xm_backup_a', 'ops.metric_observation_sample_id_seq', 'SELECT');" 't' 'backup sequence SELECT'
  Assert-Query 'postgres' "SELECT has_table_privilege('xm_api_a', 'core.environment', 'SELECT WITH GRANT OPTION');" 'f' 'API grant option absent'
  Assert-Query 'postgres' "SELECT pg_get_userbyid(c.relowner) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='core' AND c.relname='environment';" 'xm_migrator' 'fixture table owner'

  # ACL-layer negative probes (SQLSTATE 42501) and role escalation denial.
  foreach ($role in @('xm_api_a', 'xm_worker_a', 'xm_lifecycle_a', 'xm_ops_a', 'xm_backup_a')) {
    Assert-Denied $role 'CREATE TABLE public.dbr1_forbidden (id integer);' "$role CREATE"
    Assert-Denied $role 'CREATE ROLE dbr1_forbidden;' "$role CREATEROLE"
    # Plain GRANT may legally return 0 with a warning when no grant option is
    # held.  WITH GRANT OPTION forces PostgreSQL's ACL denial (42501), making
    # this probe fail closed instead of treating a warning/no-op as success.
    Assert-Denied $role 'GRANT SELECT ON core.environment TO xm_worker_a WITH GRANT OPTION;' "$role GRANT"
  }
  # Rewrite rules intentionally turn append-only UPDATE/DELETE into a no-op;
  # prove the second defense by checking the row remains byte-for-byte stable.
  Assert-NoMutation 'xm_api_a' "UPDATE action.action_run SET status='failed';" "SELECT status FROM action.action_run WHERE id='00000000-0000-0000-0000-000000000010';" 'succeeded' 'API action_run append-only UPDATE'
  Assert-NoMutation 'xm_api_a' 'DELETE FROM audit.audit_event;' 'SELECT count(*) FROM audit.audit_event;' '1' 'API audit_event append-only DELETE'
  Assert-NoMutation 'xm_worker_a' "UPDATE audit.audit_event SET result='failed';" "SELECT result FROM audit.audit_event WHERE id='00000000-0000-0000-0000-000000000011';" 'succeeded' 'worker audit_event append-only UPDATE'
  Assert-Denied 'xm_api_a' "UPDATE core.environment SET description='forbidden';" 'API core UPDATE ACL'
  Assert-Denied 'xm_api_a' 'DELETE FROM core.environment;' 'API core DELETE ACL'
  Assert-Denied 'xm_worker_a' 'TRUNCATE ops.metric_observation_sample;' 'worker sample TRUNCATE'
  Assert-Denied 'xm_worker_a' "SELECT setval('ops.metric_observation_sample_id_seq', 99);" 'worker sequence setval'
  Assert-Denied 'xm_api_a' 'SELECT * FROM public.river_job;' 'API River SELECT'
  Assert-Denied 'xm_worker_a' "INSERT INTO action.action_run (id, action_id, principal_id, environment, status) VALUES ('00000000-0000-0000-0000-000000000012', 'platform.dbr1.probe', 'dbr1', 'staging', 'succeeded');" 'worker action_run INSERT'
  Assert-Denied 'xm_api_a' 'SET ROLE xm_migrator;' 'API SET ROLE migrator'
  Assert-Denied 'xm_api_a' "UPDATE audit.chain_root SET root_hash=repeat('d',64) WHERE id='00000000-0000-0000-0000-000000000001';" 'chain_root protected column' @('42501')
  Assert-Denied 'xm_api_a' 'TRUNCATE audit.chain_root;' 'API chain_root TRUNCATE'

  # The owner can reach the table, so the immutable trigger is tested
  # separately from the runtime ACL.  Only the two export columns may change.
  Assert-Denied 'xm_migrator' "UPDATE audit.chain_root SET root_hash=repeat('e',64) WHERE id='00000000-0000-0000-0000-000000000001';" 'chain_root trigger protected column' @('55000')
  Invoke-Psql 'xm_migrator' "UPDATE audit.chain_root SET exported_at=now(), export_target='owner-probe' WHERE id='00000000-0000-0000-0000-000000000001';" | Out-Null
  Assert-Query 'postgres' "SELECT export_target FROM audit.chain_root WHERE id='00000000-0000-0000-0000-000000000001';" 'owner-probe' 'chain_root export update'
  Assert-Query 'postgres' 'SELECT count(*) FROM action.action_run;' '1' 'append-only action row unchanged'
  Assert-Query 'postgres' 'SELECT count(*) FROM audit.audit_event;' '1' 'append-only audit row unchanged'

  Write-Output "DBR1 HARNESS PASS: postgres=18 project=$project port=$discoveredPort probes=positive+negative"
} catch {
  $message = Sanitize $_.Exception.Message $password
  Write-Error $message
  throw
} finally {
  if ($started) {
    try {
      # Equivalent command (kept explicit for review):
      # docker compose -p <random-project> down --volumes --remove-orphans
      $down = Invoke-Compose @('down', '--volumes', '--remove-orphans') 'destroy disposable project' -AllowFailure
      if ($down.ExitCode -ne 0) { $teardownError = "compose down exit=$($down.ExitCode)" }
    } catch { $teardownError = $_.Exception.Message }
    try {
      foreach ($pair in @(
        @('container', @('ps', '-aq', '--filter', "label=com.docker.compose.project=$project")),
        @('network', @('network', 'ls', '-q', '--filter', "label=com.docker.compose.project=$project")),
        @('volume', @('volume', 'ls', '-q', '--filter', "label=com.docker.compose.project=$project")),
        @('run-container', @('container', 'ls', '-aq', '--filter', "label=com.xingmang.dbr1.run-id=$runId")),
        @('run-volume', @('volume', 'ls', '-q', '--filter', "label=com.xingmang.dbr1.run-id=$runId"))
      )) {
        $remaining = Join-NativeOutput (Invoke-NativeChecked 'docker' $pair[1] "post-teardown $($pair[0]) enumeration")
        if ($remaining.Trim()) { $teardownError = "post-teardown $($pair[0]) resource remains" }
      }
    } catch { $teardownError = $_.Exception.Message }
    if ($discoveredPort -gt 0) {
      try {
        $tcp = [Net.Sockets.TcpClient]::new()
        $async = $tcp.BeginConnect('127.0.0.1', $discoveredPort, $null, $null)
        $connected = $async.AsyncWaitHandle.WaitOne(750) -and $tcp.Connected
        $tcp.Close()
        if ($connected) { $teardownError = "post-teardown port $discoveredPort still accepts connections" }
      } catch { }
    }
  }
  Remove-Item Env:DBR_INTERNAL_DSN -ErrorAction SilentlyContinue
  if (Test-Path -LiteralPath $tempRoot) {
    Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
  }
  if ($teardownError) { Fail $teardownError }
}
