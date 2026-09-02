<#!
.SYNOPSIS
  scripts/dev/worktree-testdb.sh 的瘦包装（经 Git Bash 转发），行为与 bash
  版本逐字一致——本文件不重新实现任何数据库/命名逻辑，只负责找到 bash.exe、
  把参数原样转发、把标准输出/标准错误/退出码原样透传。

.DESCRIPTION
  每个 worktree 独立测试库工具的 PowerShell 入口，见
  docs/runbooks/GIT-WORKFLOW.md 的"每个 worktree 独立测试库"一节。

  默认动作打印的 `export XM_TEST_DATABASE_URL=...` 是 POSIX shell 语法，
  PowerShell 下没有对应写法，请改用 -PrintUrl（转发为 --print-url）后自行
  赋值给 $env:，例如：

    $env:XM_TEST_DATABASE_URL = & scripts\dev\worktree-testdb.ps1 -PrintUrl

.PARAMETER Drop
  删除当前 worktree 专属测试库（转发 --drop）。
.PARAMETER ListDatabases
  列出所有 xm_test_* 测试库及大小（转发 --list）。
.PARAMETER PrintUrl
  仅打印连接串本身，不带 export 前缀（转发 --print-url）。
.PARAMETER PgUrl
  覆盖管理连接，默认本地 invoice-test-pg 容器（转发 --pg-url）。
.PARAMETER Rest
  未识别的参数原样转发给 bash 脚本（例如 -h / --help）。

.EXAMPLE
  scripts\dev\worktree-testdb.ps1
  建库（如不存在）+ 灌迁移，打印 `export XM_TEST_DATABASE_URL=...`。

.EXAMPLE
  $env:XM_TEST_DATABASE_URL = & scripts\dev\worktree-testdb.ps1 -PrintUrl

.EXAMPLE
  scripts\dev\worktree-testdb.ps1 -ListDatabases
  scripts\dev\worktree-testdb.ps1 -Drop
#>
[CmdletBinding()]
param(
  [switch]$Drop,
  [switch]$ListDatabases,
  [switch]$PrintUrl,
  [string]$PgUrl = '',
  [Parameter(ValueFromRemainingArguments = $true)]
  [string[]]$Rest = @()
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Fail([string]$Message) {
  Write-Error "WORKTREE-TESTDB FAIL: $Message"
  exit 1
}

if ($Drop -and $ListDatabases) {
  Fail '-Drop 不能与 -ListDatabases 同时使用'
}

$shScript = Join-Path $PSScriptRoot 'worktree-testdb.sh'
if (-not (Test-Path -LiteralPath $shScript)) {
  Fail "找不到配套脚本: $shScript"
}

# 找 Git Bash：**不能**信 PATH 上裸的 bash.exe——Windows 10/11 装了 WSL 时
# `C:\Windows\System32\bash.exe`（WSL 启动器）几乎总是排在 PATH 更前面，
# 会无声地换成 WSL 的 bash 去跑（实测：WSL 把带中文的 Windows 路径翻译错，
# 报 "No such file or directory"，且报错信息本身还夹着一段不相关的 WSL
# localhost 转发提示，非常误导）。改为从已知可靠的 git.exe 反推 Git for
# Windows 安装根目录（标准布局 <root>\cmd\git.exe 与 <root>\bin\bash.exe
# 同级），再补注册表与标准安装路径兜底；全部找不到就清楚报错退出，
# 绝不静默回落到 PATH 上的 bash.exe。
function Find-GitBash {
  $candidates = New-Object System.Collections.Generic.List[string]

  $gitCmd = Get-Command 'git.exe' -ErrorAction SilentlyContinue
  if ($gitCmd) {
    $gitRoot = Split-Path -Parent (Split-Path -Parent $gitCmd.Source)
    $candidates.Add((Join-Path $gitRoot 'bin\bash.exe'))
  }

  try {
    $installPath = (Get-ItemProperty -LiteralPath 'HKLM:\SOFTWARE\GitForWindows' -ErrorAction Stop).InstallPath
    if ($installPath) { $candidates.Add((Join-Path $installPath 'bin\bash.exe')) }
  } catch {}

  $candidates.Add((Join-Path $env:ProgramFiles 'Git\bin\bash.exe'))
  if (${env:ProgramFiles(x86)}) {
    $candidates.Add((Join-Path ${env:ProgramFiles(x86)} 'Git\bin\bash.exe'))
  }

  foreach ($candidate in $candidates) {
    if ($candidate -and (Test-Path -LiteralPath $candidate)) { return $candidate }
  }
  return $null
}

$bashPath = Find-GitBash
if (-not $bashPath) {
  Fail '找不到 Git Bash(bash.exe)；请确认 Git for Windows 已安装（PATH 上的 bash.exe 若来自 WSL 不会被采用）'
}

$forwarded = New-Object System.Collections.Generic.List[string]
if ($Drop) { $forwarded.Add('--drop') }
if ($ListDatabases) { $forwarded.Add('--list') }
if ($PrintUrl) { $forwarded.Add('--print-url') }
if ($PgUrl) {
  $forwarded.Add('--pg-url')
  $forwarded.Add($PgUrl)
}
foreach ($arg in $Rest) { $forwarded.Add($arg) }

& $bashPath $shScript @forwarded
exit $LASTEXITCODE
