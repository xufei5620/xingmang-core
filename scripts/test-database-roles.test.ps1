<#!
DBR1 harness guard tests.  These invoke only -ValidateOnly, so they never
create a Docker project or connect to a database.  Keep the negative cases here
in lockstep with the fail-closed input boundary in test-database-roles.ps1.
#>
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$script = Join-Path $PSScriptRoot 'test-database-roles.ps1'
$repo = (Resolve-Path (Join-Path $PSScriptRoot '..')).ProviderPath

function Expect-Rejected([scriptblock]$Action, [string]$Label) {
  $rejected = $false
  try { & $Action } catch { $rejected = $true }
  if (-not $rejected) { throw "DBR1 guard test failed: $Label was accepted" }
}

$saved = @{}
foreach ($name in @('DATABASE_URL', 'XM_TEST_DATABASE_URL', 'XM_TEST_DATABASE_ADMIN_URL',
                    'DBR_ADMIN_DSN', 'DOCKER_HOST', 'DOCKER_CONTEXT')) {
  $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
  Remove-Item "Env:$name" -ErrorAction SilentlyContinue
}
$temp = Join-Path ([IO.Path]::GetTempPath()) ("xm-dbr1-guard-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $temp -Force | Out-Null
try {
  & $script -ValidateOnly -RepoRoot $repo | Out-Null

  $env:DATABASE_URL = 'postgres://admin:secret@example.invalid/production'
  Expect-Rejected { & $script -ValidateOnly -RepoRoot $repo } 'external DATABASE_URL'
  Remove-Item Env:DATABASE_URL -ErrorAction SilentlyContinue

  $env:DBR_ADMIN_DSN = 'postgres://admin@example.invalid/production'
  Expect-Rejected { & $script -ValidateOnly -RepoRoot $repo } 'external DBR_ADMIN_DSN'
  Remove-Item Env:DBR_ADMIN_DSN -ErrorAction SilentlyContinue

  $sourceCompose = Join-Path $repo 'tests/security/database-role-cluster.compose.yaml'
  $badPort = Join-Path $temp 'bad-port.yaml'
  (Get-Content -LiteralPath $sourceCompose -Raw).Replace('127.0.0.1::5432', '0.0.0.0:5432:5432') |
    Set-Content -LiteralPath $badPort -Encoding utf8
  Expect-Rejected { & $script -ValidateOnly -RepoRoot $repo -ComposeFile $badPort } 'wildcard/fixed port mapping'

  $badDigest = Join-Path $temp 'bad-digest.yaml'
  (Get-Content -LiteralPath $sourceCompose -Raw).Replace('@sha256:7341002d2b8c7c5bdd7542a671a95b36196c0b5b888daf454ae4fc33ba5346d7', '') |
    Set-Content -LiteralPath $badDigest -Encoding utf8
  Expect-Rejected { & $script -ValidateOnly -RepoRoot $repo -ComposeFile $badDigest } 'tag-only postgres image'

  $env:DOCKER_HOST = 'tcp://production.example.invalid:2375'
  Expect-Rejected { & $script -ValidateOnly -RepoRoot $repo } 'remote Docker host'
  Remove-Item Env:DOCKER_HOST -ErrorAction SilentlyContinue

  $env:PGHOST = 'production.example.invalid'
  Expect-Rejected { & $script -ValidateOnly -RepoRoot $repo } 'libpq PGHOST override'
  Remove-Item Env:PGHOST -ErrorAction SilentlyContinue

  Write-Output 'database role harness guard tests passed'
} finally {
  Remove-Item -LiteralPath $temp -Recurse -Force -ErrorAction SilentlyContinue
  foreach ($name in $saved.Keys) {
    if ($null -eq $saved[$name]) { Remove-Item "Env:$name" -ErrorAction SilentlyContinue }
    else { Set-Item "Env:$name" $saved[$name] }
  }
}
