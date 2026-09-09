param(
  [string]$Root = (Resolve-Path (Join-Path $PSScriptRoot '..'))
)

$progressDir = Join-Path $Root 'tools/local-progress'
$progressPath = Join-Path $progressDir 'progress.json'
$ledgerPath = Join-Path $Root '.superpowers/local-completion/progress.md'

function GitText([string[]]$Arguments) {
  $value = & git -C $Root @Arguments 2>$null
  if ($LASTEXITCODE -ne 0) { return '' }
  return ($value -join "`n").Trim()
}

$branch = GitText @('branch', '--show-current')
$head = GitText @('rev-parse', '--short', 'HEAD')
$commitCount = [int](GitText @('rev-list', '--count', 'HEAD'))
$statusText = GitText @('status', '--short')
$statusLines = @($statusText -split "`n" | Where-Object { $_.Trim() })
$changedFiles = @($statusLines | ForEach-Object { if ($_.Length -gt 3) { $_.Substring(3).Trim() } } | Select-Object -First 24)
$now = (Get-Date).ToUniversalTime().ToString('o')

$phase = '本地项目收尾'
$next = '继续安全可实现的运营切片，并保持全量回归为绿色。'
if (Test-Path -LiteralPath $ledgerPath) {
  $ledger = Get-Content -LiteralPath $ledgerPath -Raw
  if ($ledger -match 'Runway / user-read local completion') { $phase = 'Runway / 用户读取收尾' }
  if ($ledger -match 'Next:\s*(.+)') { $next = $Matches[1].Trim() }
}

$payload = [ordered]@{
  phase = $phase; status = 'working'; statusLabel = '持续推进'; statusTone = ''
  progress = if ($changedFiles.Count -eq 0) { 100 } else { 76 }
  progressLabel = if ($changedFiles.Count -eq 0) { '工作树干净，等待下一片' } else { "检测到 $($changedFiles.Count) 个本地变更" }
  updatedAt = $now; branch = $branch; head = $head; commits = $commitCount
  checks = [ordered]@{ backend = '请查看最近回归'; frontend = '请查看最近回归'; storybook = '待运行'; governance = '待运行' }
  next = $next; timeline = @(); changedFiles = $changedFiles
}
$payload | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $progressPath -Encoding UTF8
