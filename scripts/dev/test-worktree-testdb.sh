#!/usr/bin/env bash
# XM-DEV-WTDB0 的测试。
#
# 单元部分（默认跑）只测纯函数（sanitize_name / derive_db_name / build_db_url）
# 与参数解析（parse_args），全部在独立子进程里 source 被测脚本后调用，不连接
# 任何数据库、不需要 Docker/Postgres/Go 在场。
#
# 真库往返部分（建库→迁移→列库→删库）默认跳过，需要显式
# `XM_DEV_WTDB_E2E=1 bash scripts/dev/test-worktree-testdb.sh` 才会跑，
# 会针对本机 invoice-test-pg 容器创建并删除当前 worktree 对应的
# xm_test_<worktree 目录名> 库（跑完清理，即使中途失败也会清理）。
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
script="$repo_root/scripts/dev/worktree-testdb.sh"

[ -f "$script" ] || { echo "TEST HARNESS FAIL: 找不到 $script" >&2; exit 2; }

tmp="$(mktemp -d)"
[ -n "$tmp" ] && [ -d "$tmp" ] || { echo "TEST HARNESS FAIL: mktemp 失败" >&2; exit 2; }
trap 'rm -rf "$tmp"' EXIT
fail=0

ok() { printf 'ok - %s\n' "$1"; }
bad() { printf 'not ok - %s\n' "$1" >&2; fail=1; }

expect_success() {
  local name="$1"; shift
  if "$@" >"$tmp/stdout" 2>"$tmp/stderr"; then
    ok "$name"
  else
    bad "$name (exit=$?)"
    sed 's/^/  /' "$tmp/stderr" >&2 || true
  fi
}

expect_failure() {
  local name="$1"; shift
  if "$@" >"$tmp/stdout" 2>"$tmp/stderr"; then
    bad "$name (unexpected success)"
  else
    ok "$name"
  fi
}

assert_text() {
  local name="$1" expected="$2" path="$3"
  if [ ! -f "$path" ]; then
    bad "$name (missing file $path)"
  elif grep -Fq -- "$expected" "$path"; then ok "$name"; else
    bad "$name (missing '$expected')"
    sed 's/^/  /' "$path" >&2 || true
  fi
}

assert_eq() {
  local name="$1" expected="$2" actual="$3"
  if [ "$expected" = "$actual" ]; then
    ok "$name"
  else
    bad "$name (expected='$expected' actual='$actual')"
  fi
}

assert_match() {
  local name="$1" pattern="$2" actual="$3"
  if [[ "$actual" =~ $pattern ]]; then
    ok "$name"
  else
    bad "$name (actual='$actual' 不匹配 /$pattern/)"
  fi
}

# call_fn：在独立子进程里 source 被测脚本后直接调用一个纯函数，打印其结果。
# 用子进程隔离被测脚本顶部的 `set -Eeuo pipefail`，不让它泄漏进本测试脚本
# 自己的控制流（本文件故意不开 -e，靠显式 if/退出码判断，参照
# tests/deploy/deploy0-a.test.sh 的既有约定）。
call_fn() {
  bash -c 'source -- "$1"; shift; "$@"' _ "$script" "$@"
}

# call_parse_args：同样在独立子进程里调用 parse_args，把结果变量打印成一行
# 方便断言；失败（die）时该子进程直接以非零退出，调用方按普通命令退出码判断。
call_parse_args() {
  bash -c '
    source -- "$1"; shift
    parse_args "$@"
    printf "action=%s print_url=%s pg_url_override=%s\n" "$action" "$print_url" "$pg_url_override"
  ' _ "$script" "$@"
}

###############################################################################
# 语法与可 source 性
###############################################################################
expect_success "worktree-testdb.sh 语法检查" bash -n "$script"

expect_success "source 本身不执行 main（不需要数据库）" bash -c '
  source -- "$1"
  [ -z "${dbname:-}" ] && [ -z "${worktree_root:-}" ] && [ -z "${pg_admin_url:-}" ]
' _ "$script"

###############################################################################
# sanitize_name：规整成 [a-z0-9_]，合并连续非法字符，去首尾下划线，空串兜底
###############################################################################
out="$(call_fn sanitize_name "wt-wtdb")"
assert_eq "sanitize_name 处理短横线" "wt_wtdb" "$out"

out="$(call_fn sanitize_name "WT-Foo.Bar")"
assert_eq "sanitize_name 处理大写与多个非法字符" "wt_foo_bar" "$out"

out="$(call_fn sanitize_name "___lead_trail___")"
assert_eq "sanitize_name 去除首尾下划线" "lead_trail" "$out"

out="$(call_fn sanitize_name "a---b")"
assert_eq "sanitize_name 合并连续非法字符为单个下划线" "a_b" "$out"

out="$(call_fn sanitize_name "")"
assert_match "sanitize_name 空串退化为 wt+8位哈希" '^wt[0-9a-f]{8}$' "$out"

out="$(call_fn sanitize_name "测试")"
assert_match "sanitize_name 纯非 ASCII 退化为 wt+8位哈希" '^wt[0-9a-f]{8}$' "$out"

out1="$(call_fn sanitize_name "")"
out2="$(call_fn sanitize_name "")"
assert_eq "sanitize_name 兜底哈希确定性（同输入同输出）" "$out1" "$out2"

###############################################################################
# derive_db_name：xm_test_ 前缀，取路径 basename，长度不超过 63
###############################################################################
out="$(call_fn derive_db_name "/some/path/wt-wtdb")"
assert_eq "derive_db_name 取路径 basename 并加前缀" "xm_test_wt_wtdb" "$out"

out="$(call_fn derive_db_name "wt-wtdb")"
assert_eq "derive_db_name 接受纯名字（非路径）" "xm_test_wt_wtdb" "$out"

long_name="$(printf 'a%.0s' $(seq 1 80))"
out="$(call_fn derive_db_name "$long_name")"
assert_match "derive_db_name 超长名字截断到 63 字符以内" '^xm_test_a+_[0-9a-f]{8}$' "$out"
assert_eq "derive_db_name 超长名字精确等于 63 字符" "63" "${#out}"

long_a="$(printf 'a%.0s' $(seq 1 80))"
long_b="$(printf 'a%.0s' $(seq 1 81))"
out_a="$(call_fn derive_db_name "$long_a")"
out_b="$(call_fn derive_db_name "$long_b")"
if [ "$out_a" != "$out_b" ]; then
  ok "derive_db_name 两个不同的超长名字截断后不撞名"
else
  bad "derive_db_name 两个不同的超长名字截断后不撞名 (都是 $out_a)"
fi

###############################################################################
# build_db_url：替换 dbname，保留 scheme/authority/query
###############################################################################
out="$(call_fn build_db_url "postgres://postgres:test@127.0.0.1:55432/postgres?sslmode=disable" "xm_test_foo")"
assert_eq "build_db_url 保留 query 并替换 dbname" \
  "postgres://postgres:test@127.0.0.1:55432/xm_test_foo?sslmode=disable" "$out"

out="$(call_fn build_db_url "postgres://u@h:1/d" "xm_test_bar")"
assert_eq "build_db_url 无 query 时同样替换 dbname" "postgres://u@h:1/xm_test_bar" "$out"

out="$(call_fn build_db_url "postgresql://u:p@h:2/old" "xm_test_baz")"
assert_eq "build_db_url 支持 postgresql:// scheme" "postgresql://u:p@h:2/xm_test_baz" "$out"

expect_failure "build_db_url 拒绝非 postgres(ql):// 连接串" call_fn build_db_url "mysql://h/d" "xm_test_x"

###############################################################################
# parse_args：默认值、各 flag、互斥校验、未知参数
###############################################################################
out="$(call_parse_args)"
assert_eq "parse_args 默认动作为 ensure" "action=ensure print_url=0 pg_url_override=" "$out"

out="$(call_parse_args --print-url)"
assert_eq "parse_args 识别 --print-url" "action=ensure print_url=1 pg_url_override=" "$out"

out="$(call_parse_args --drop)"
assert_eq "parse_args 识别 --drop" "action=drop print_url=0 pg_url_override=" "$out"

out="$(call_parse_args --list)"
assert_eq "parse_args 识别 --list" "action=list print_url=0 pg_url_override=" "$out"

out="$(call_parse_args --pg-url postgres://h/d)"
assert_eq "parse_args 识别 --pg-url VALUE 形式" "action=ensure print_url=0 pg_url_override=postgres://h/d" "$out"

out="$(call_parse_args --pg-url=postgres://h2/d2)"
assert_eq "parse_args 识别 --pg-url=VALUE 形式" "action=ensure print_url=0 pg_url_override=postgres://h2/d2" "$out"

expect_failure "parse_args 拒绝 --drop 与 --list 同时出现" call_parse_args --drop --list
expect_failure "parse_args 拒绝 --list 与 --drop 同时出现（反序）" call_parse_args --list --drop
expect_failure "parse_args 拒绝 --print-url 与 --drop 同时出现" call_parse_args --print-url --drop
expect_failure "parse_args 拒绝 --print-url 与 --list 同时出现" call_parse_args --print-url --list
expect_failure "parse_args 拒绝未知参数" call_parse_args --bogus
expect_failure "parse_args 拒绝 --pg-url 缺值" call_parse_args --pg-url
expect_failure "parse_args 拒绝非 postgres(ql):// 的 --pg-url" call_parse_args --pg-url mysql://h/d

expect_success "--help 不需要数据库即可退出 0" bash "$script" --help
assert_text "--help 输出包含 --drop" "--drop" "$tmp/stdout"
assert_text "--help 输出包含 --list" "--list" "$tmp/stdout"
assert_text "--help 输出包含 --pg-url" "--pg-url" "$tmp/stdout"

expect_success "-h 是 --help 的别名" bash "$script" -h

###############################################################################
# 无数据库场景下的 fail-fast：覆盖到不可达目标时应清楚报错，而不是裸调用栈
###############################################################################
expect_failure "--pg-url 指向不可达目标时清楚失败" \
  bash "$script" --pg-url postgres://postgres:test@127.0.0.1:1/nope --print-url
assert_text "失败信息带统一前缀" "WORKTREE-TESTDB FAIL" "$tmp/stderr"

if [ "$fail" -eq 0 ]; then
  printf 'WORKTREE-TESTDB-UNIT-TEST-OK\n'
fi

###############################################################################
# 真库往返（默认跳过）：XM_DEV_WTDB_E2E=1 才跑，针对本机 invoice-test-pg。
###############################################################################
if [ "${XM_DEV_WTDB_E2E:-0}" = "1" ]; then
  expected_db="$(call_fn derive_db_name "$(git -C "$repo_root" rev-parse --show-toplevel)")"
  echo "E2E: 当前 worktree 对应库名 = $expected_db" >&2

  cleanup_e2e() { bash "$script" --drop >/dev/null 2>&1 || true; }
  trap 'cleanup_e2e; rm -rf "$tmp"' EXIT

  # 先确保起点干净，不依赖此前手工调试留下的状态。
  cleanup_e2e

  expect_success "E2E: --print-url 建库并灌迁移" bash "$script" --print-url
  printed_url="$(cat "$tmp/stdout")"
  assert_match "E2E: 打印的是 postgres 连接串" '^postgres://' "$printed_url"
  case "$printed_url" in
    */"$expected_db"'?'*|*/"$expected_db")
      ok "E2E: 打印的连接串指向 $expected_db" ;;
    *)
      bad "E2E: 打印的连接串指向 $expected_db (actual=$printed_url)" ;;
  esac

  expect_success "E2E: 默认动作输出 export 语句" bash "$script"
  assert_text "E2E: export 语句包含 XM_TEST_DATABASE_URL" "export XM_TEST_DATABASE_URL=" "$tmp/stdout"
  assert_text "E2E: export 语句指向本 worktree 的库" "/$expected_db" "$tmp/stdout"

  expect_success "E2E: 已存在时重复 ensure 仍成功（幂等）" bash "$script" --print-url

  expect_success "E2E: --list 能看到本 worktree 的库" bash "$script" --list
  assert_text "E2E: --list 输出包含本 worktree 的库名" "$expected_db" "$tmp/stdout"

  expect_success "E2E: --drop 删除本 worktree 的库" bash "$script" --drop

  expect_success "E2E: 删除后 --list 命令本身仍成功执行" bash "$script" --list
  if grep -Fq -- "$expected_db" "$tmp/stdout"; then
    bad "E2E: 删除后 --list 不再包含该库 (仍出现在输出中)"
  else
    ok "E2E: 删除后 --list 不再包含该库"
  fi

  expect_success "E2E: 对不存在的库重复 --drop 仍成功（幂等）" bash "$script" --drop

  if [ "$fail" -eq 0 ]; then
    printf 'WORKTREE-TESTDB-E2E-TEST-OK\n'
  fi
else
  echo "跳过真库往返测试（设置 XM_DEV_WTDB_E2E=1 以启用，需要本机 invoice-test-pg 容器）" >&2
fi

exit "$fail"
