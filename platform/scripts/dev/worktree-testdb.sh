#!/usr/bin/env bash
# XM-DEV-WTDB0：每个 git worktree 独立的 Postgres 测试库。
#
# 背景：多个 worktree/agent 长期共用同一个 xm_test 库，2026-09-02/03 已经真实
# 撞过一次——credentials 包的 DB 集成测试往 core.connector_config 写
# (sub2api/newapi, staging) 这两行且不清理，与 jobs 包的同库测试撞了唯一键
# （XM-DBTEST-FIX0 记录的三处夹具漂移之一）。本脚本让每个 worktree 拿到自己
# 独立的 xm_test_<worktree 目录名> 库，从根上消除这类"跑测顺序/并发敏感"的
# 耦合，而不是逐个包打补丁。
#
# 用法（默认动作）：
#   scripts/dev/worktree-testdb.sh              # 建库（如不存在）+ 灌迁移，
#                                                # 打印 export XM_TEST_DATABASE_URL=...
#   eval "$(scripts/dev/worktree-testdb.sh)"     # 直接令当前 shell 生效
#   export XM_TEST_DATABASE_URL=$(scripts/dev/worktree-testdb.sh --print-url)
#   scripts/dev/worktree-testdb.sh --list        # 列出所有 xm_test_* 库及大小
#   scripts/dev/worktree-testdb.sh --drop        # 删除当前 worktree 的专属库
#
# 约定：本脚本对外只有两种输出通道——诊断/进度信息一律写 stderr；stdout 只在
# 默认动作（含 --print-url）时输出那一行连接串/export 语句，--list 时输出查询
# 结果表格。这样 `export X=$(... --print-url)` 之类的捕获不会被进度信息污染。
#
# 可被 source：本文件把纯函数（sanitize_name / derive_db_name / build_db_url）
# 和参数解析（parse_args）都写成不依赖数据库的普通函数，配合文件末尾的
# BASH_SOURCE 判断——`source` 本文件只加载函数，不会执行 main、不需要
# Postgres/Docker/Go 在场。scripts/dev/test-worktree-testdb.sh 靠这个做无库
# 单元测试。
set -Eeuo pipefail

# ---------------------------------------------------------------------------
# 纯函数：不触碰任何外部状态，可在测试里直接 source 后调用。
# ---------------------------------------------------------------------------

# sanitize_name：把任意字符串规整成 [a-z0-9_]，连续非法字符合并成单个下划线，
# 去掉首尾下划线；规整后为空（比如原串全是非 ASCII 字符）时退化成
# "wt" + 原串 sha256 前 8 位十六进制，保证一定有区分度。
sanitize_name() {
  local raw="$1" out
  out="$(printf '%s' "$raw" | tr '[:upper:]' '[:lower:]')"
  out="$(printf '%s' "$out" | LC_ALL=C sed -E 's/[^a-z0-9]+/_/g')"
  out="$(printf '%s' "$out" | sed -E 's/^_+//; s/_+$//')"
  if [ -z "$out" ]; then
    out="wt$(printf '%s' "$raw" | sha256sum | cut -c1-8)"
  fi
  printf '%s' "$out"
}

# derive_db_name：worktree 目录路径（或直接是名字）-> xm_test_ 前缀的库名，
# 总长度不超过 63（Postgres 标识符上限）。超长时截断 sanitize 后的名字并补
# 一段短哈希消歧——只截断不消歧会让两个不同的长 worktree 名悄悄撞成同一个
# 库名，正好违背"每个 worktree 独立测试库"这件事本身。
derive_db_name() {
  local raw="$1" base sanitized prefix full max hash keep
  base="$(basename -- "$raw")"
  sanitized="$(sanitize_name "$base")"
  prefix="xm_test_"
  full="${prefix}${sanitized}"
  max=63
  if [ "${#full}" -le "$max" ]; then
    printf '%s' "$full"
    return 0
  fi
  hash="$(printf '%s' "$sanitized" | sha256sum | cut -c1-8)"
  keep=$((max - ${#prefix} - ${#hash} - 1))
  printf '%s%s_%s' "$prefix" "${sanitized:0:$keep}" "$hash"
}

# build_db_url：把一个 postgres(ql):// 连接串的 dbname 换成指定值，保留
# scheme/userinfo/host/port 与 ?query 部分。
build_db_url() {
  local base="$1" dbname="$2" scheme rest authority path_and_query query=""
  case "$base" in
    postgres://*) scheme="postgres://"; rest="${base#postgres://}" ;;
    postgresql://*) scheme="postgresql://"; rest="${base#postgresql://}" ;;
    *) die "连接串必须以 postgres:// 或 postgresql:// 开头：$base" ;;
  esac
  authority="${rest%%/*}"
  case "$rest" in
    */*) path_and_query="${rest#*/}" ;;
    *) path_and_query="" ;;
  esac
  case "$path_and_query" in
    *\?*) query="?${path_and_query#*\?}" ;;
  esac
  printf '%s%s/%s%s' "$scheme" "$authority" "$dbname" "$query"
}

usage() {
  cat <<'USAGE'
用法: scripts/dev/worktree-testdb.sh [选项]
  （默认）确保当前 worktree 专属测试库存在并已迁移到最新版本，
          输出一行 `export XM_TEST_DATABASE_URL=...`
  --print-url        仅打印连接串本身（不带 export 前缀），便于 $(...) 捕获
  --drop             删除当前 worktree 专属测试库（如存在，含终止其活动连接）
  --list             列出所有 xm_test_* 测试库及大小
  --pg-url URL       覆盖管理连接（默认本地 invoice-test-pg 容器的 postgres 库）
  -h, --help         显示本帮助
USAGE
}

die() { echo "WORKTREE-TESTDB FAIL: $*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# 参数解析：只改全局变量，不碰数据库/Docker/Go——可在子进程里单独 source
# 后调用来测试，不需要真的连接任何东西。
# ---------------------------------------------------------------------------
action=ensure          # ensure|drop|list
print_url=0
pg_url_override=

parse_args() {
  action=ensure
  print_url=0
  pg_url_override=
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --drop)
        [ "$action" = ensure ] || die "--drop 不能与 --list 同时使用"
        action=drop; shift ;;
      --list)
        [ "$action" = ensure ] || die "--list 不能与 --drop 同时使用"
        action=list; shift ;;
      --print-url)
        print_url=1; shift ;;
      --pg-url)
        [ "$#" -ge 2 ] || die "--pg-url 需要一个值"
        pg_url_override="$2"; shift 2 ;;
      --pg-url=*)
        pg_url_override="${1#*=}"; shift ;;
      -h|--help)
        usage; exit 0 ;;
      *)
        usage >&2; die "未知参数: $1" ;;
    esac
  done
  case "$pg_url_override" in
    ""|postgres://*|postgresql://*) ;;
    *) die "--pg-url 必须以 postgres:// 或 postgresql:// 开头" ;;
  esac
  if [ "$print_url" -eq 1 ] && [ "$action" != ensure ]; then
    die "--print-url 只能用于默认动作，不能与 --drop/--list 同时使用"
  fi
}

# ---------------------------------------------------------------------------
# 外部世界：git / docker / psql / go。main() 之后才会触达这些。
# ---------------------------------------------------------------------------
docker_bin="${XM_WORKTREE_TESTDB_DOCKER_BIN:-docker}"
psql_bin="${XM_WORKTREE_TESTDB_PSQL_BIN:-psql}"
go_bin="${XM_WORKTREE_TESTDB_GO_BIN:-go}"
pg_container="${XM_WORKTREE_TESTDB_CONTAINER:-invoice-test-pg}"
default_pg_admin_url="postgres://postgres:test@127.0.0.1:55432/postgres?sslmode=disable"

require_bin() {
  local label="$1" value="$2"
  command -v -- "$value" >/dev/null 2>&1 || die "找不到可执行文件: $label（$value）"
}

find_worktree_root() {
  local root
  root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
  [ -n "$root" ] || die "当前目录不在任何 Git 工作树内"
  printf '%s' "$root"
}

# resolve_pg_mode：没有显式 --pg-url 覆盖、且 invoice-test-pg 容器在跑，就走
# docker exec（配方与团队现有做法逐字一致）；否则要求本地 PATH 上有 psql，
# 直接用连接串连接（--pg-url 覆盖场景下，继续用固定容器名的 docker exec
# 已经文不对题——覆盖连接就是在说"目标不是这个容器"）。
pg_mode=local
resolve_pg_mode() {
  if [ -z "$pg_url_override" ] && command -v "$docker_bin" >/dev/null 2>&1 \
     && [ "$("$docker_bin" inspect -f '{{.State.Running}}' "$pg_container" 2>/dev/null || true)" = "true" ]; then
    pg_mode=docker
    require_bin docker "$docker_bin"
    return 0
  fi
  pg_mode=local
  require_bin psql "$psql_bin"
}

# run_psql：$1 是 psql 输出模式标志（-c 用于语句/表格输出，-tAc 用于取标量），
# $2 是 SQL 文本。
run_psql() {
  local flag="$1" sql="$2"
  if [ "$pg_mode" = docker ]; then
    "$docker_bin" exec "$pg_container" psql -U postgres -v ON_ERROR_STOP=1 "$flag" "$sql"
  else
    "$psql_bin" "$pg_admin_url" -v ON_ERROR_STOP=1 "$flag" "$sql"
  fi
}

psql_exec() {
  local sql="$1" out rc
  out="$(run_psql -c "$sql" 2>&1)"; rc=$?
  [ "$rc" -eq 0 ] || die "Postgres 操作失败（模式=$pg_mode, exit=$rc）：$out"
  [ -z "$out" ] || printf '%s\n' "$out"
}

psql_scalar() {
  local sql="$1" out rc
  out="$(run_psql -tAc "$sql" 2>&1)"; rc=$?
  [ "$rc" -eq 0 ] || die "Postgres 查询失败（模式=$pg_mode, exit=$rc）：$out"
  printf '%s' "$out" | tr -d '[:space:]'
}

ping_admin() {
  local v
  v="$(psql_scalar 'SELECT 1;')"
  [ "$v" = "1" ] || die "无法连接 Postgres（模式=$pg_mode）：请确认 $pg_container 容器在跑，或本地 Postgres 在 ${pg_admin_url} 可达"
}

# ---------------------------------------------------------------------------
# 动作
# ---------------------------------------------------------------------------
cmd_ensure() {
  local exists migrate_url
  exists="$(psql_scalar "SELECT 1 FROM pg_database WHERE datname = '$dbname';")"
  if [ "$exists" = "1" ]; then
    echo "WORKTREE-TESTDB: 数据库 $dbname 已存在，跳过创建" >&2
  else
    psql_exec "CREATE DATABASE \"$dbname\";" >/dev/null
    echo "WORKTREE-TESTDB: 已创建数据库 $dbname" >&2
  fi
  migrate_url="$(build_db_url "$pg_admin_url" "$dbname")"
  require_bin go "$go_bin"
  # 迁移只连本机已发布端口的真实 Postgres，不需要外部网络；这里仍然主动
  # unset 本机常见的转发代理变量，避免个别机器上代理抖动影响 go run 编译
  # cmd/migrate 时的模块解析（纯防御性操作，模块已在缓存时完全没有副作用）。
  ( cd "$worktree_root" && env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy \
      -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy \
      "$go_bin" run ./cmd/migrate -database "$migrate_url" -path db/migrations up ) \
    || die "迁移失败：$dbname"
  if [ "$print_url" -eq 1 ]; then
    printf '%s\n' "$migrate_url"
  else
    printf 'export XM_TEST_DATABASE_URL=%s\n' "$migrate_url"
  fi
}

cmd_drop() {
  psql_exec "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$dbname' AND pid <> pg_backend_pid();" >/dev/null
  psql_exec "DROP DATABASE IF EXISTS \"$dbname\";" >/dev/null
  echo "WORKTREE-TESTDB: 已删除数据库 $dbname（如存在）" >&2
}

cmd_list() {
  psql_exec "SELECT datname AS database, pg_size_pretty(pg_database_size(datname)) AS size FROM pg_database WHERE datname LIKE 'xm_test_%' ORDER BY datname;"
}

main() {
  parse_args "$@"
  pg_admin_url="${pg_url_override:-$default_pg_admin_url}"
  worktree_root="$(find_worktree_root)"
  dbname="$(derive_db_name "$worktree_root")"
  case "$dbname" in
    xm_test_*) ;;
    *) die "内部错误：派生的数据库名缺少 xm_test_ 前缀：$dbname" ;;
  esac
  resolve_pg_mode
  ping_admin
  case "$action" in
    ensure) cmd_ensure ;;
    drop) cmd_drop ;;
    list) cmd_list ;;
  esac
}

if [ "${BASH_SOURCE[0]:-$0}" = "${0}" ]; then
  main "$@"
fi
