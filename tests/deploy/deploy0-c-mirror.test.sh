#!/bin/bash -p
# XM-C-DEPLOY0-c：GitHub 镜像 helper 的本地可重复测试。
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
mirror_script="$repo_root/deploy/scripts/mirror-github.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail=0

ok() { printf 'ok - %s\n' "$1"; }
bad() { printf 'not ok - %s\n' "$1" >&2; fail=1; }
expect_success() {
  local name="$1"; shift
  if "$@" >"$tmp/stdout" 2>"$tmp/stderr"; then ok "$name"; else
    bad "$name (exit=$?)"; sed 's/^/  /' "$tmp/stderr" >&2 || true
  fi
}
expect_failure() {
  local name="$1"; shift
  if "$@" >"$tmp/stdout" 2>"$tmp/stderr"; then bad "$name (unexpected success)"; else ok "$name"; fi
}
assert_text() {
  local name="$1" expected="$2" path="$3"
  if [ -f "$path" ] && grep -Fq -- "$expected" "$path"; then ok "$name"; else bad "$name"; fi
}
assert_not_text() {
  local name="$1" forbidden="$2" path="$3"
  if [ -f "$path" ] && grep -Fq -- "$forbidden" "$path"; then bad "$name"; else ok "$name"; fi
}
trace_count() {
  local needle="$1" path="$2"
  if [ -f "$path" ]; then grep -Fc -- "$needle" "$path" 2>/dev/null || true; else printf '0'; fi
}
write_executable() {
  local path="$1" body="$2"
  mkdir -p "$(dirname "$path")"
  printf '%s\n' "$body" > "$path"
  chmod +x "$path"
}
setup_repo() {
  local repo="$1" origin="$2" github="$3" github_url="$4"
  "$real_git" init --bare "$origin" >/dev/null 2>&1
  "$real_git" init --bare "$github" >/dev/null 2>&1
  "$real_git" init "$repo" >/dev/null 2>&1
  "$real_git" -C "$repo" config user.email test@example.invalid
  "$real_git" -C "$repo" config user.name deploy-test
  printf 'seed\n' > "$repo/README"
  "$real_git" -C "$repo" add README
  "$real_git" -C "$repo" commit -m seed >/dev/null 2>&1
  "$real_git" -C "$repo" remote add origin "$origin"
  "$real_git" -C "$repo" remote add github "$github_url"
}

if [ ! -x "$mirror_script" ]; then
  bad "mirror-github.sh 存在且可执行"
else
  ok "mirror-github.sh 存在且可执行"
  if bash -n "$mirror_script"; then ok "mirror-github.sh shell 语法"; else bad "mirror-github.sh shell 语法"; fi
fi

real_git="$(command -v git)"
fake_bin="$tmp/bin"
mkdir -p "$fake_bin"
fake_git="$fake_bin/git"
trace="$tmp/git.trace"
write_executable "$fake_git" '#!/usr/bin/env bash
set -Eeuo pipefail
printf "%s\n" "$*" >> "${D0C_TRACE:?}"
repo_prefix=()
if [ "${1:-}" = "-C" ]; then
  repo_prefix=(-C "$2")
  shift 2
fi
case "${1:-}" in
  push)
    [ "${D0C_PUSH_FAIL:-0}" = 1 ] && exit 27
    exit 0
    ;;
esac
exec "${REAL_GIT:?}" "${repo_prefix[@]}" "$@"
'

success_repo="$tmp/success-repo"
success_origin="$tmp/success-origin.git"
success_github="$tmp/success-github.git"
setup_repo "$success_repo" "$success_origin" "$success_github" "$success_github"
expect_success "默认 github remote 执行一次镜像" env REAL_GIT="$real_git" D0C_TRACE="$trace" XM_DEPLOY_TEST_MODE=1 \
  "$mirror_script" --test-mode --repo "$success_repo" --git-bin "$fake_git" --reason accepted
assert_text "命令形状包含 remote get-url github" "remote get-url github" "$trace"
assert_text "命令形状包含 push --mirror github" "push --mirror github" "$trace"
if [ "$(trace_count 'push --mirror github' "$trace")" = "1" ]; then ok "push 只执行一次"; else bad "push 只执行一次"; fi
assert_not_text "不触碰 origin" "origin" "$trace"

dry_repo="$tmp/dry-repo"
dry_origin="$tmp/dry-origin.git"
dry_github="$tmp/dry-github.git"
dry_trace="$tmp/dry.trace"
setup_repo "$dry_repo" "$dry_origin" "$dry_github" "$dry_github"
expect_success "dry-run 不执行 push" env REAL_GIT="$real_git" D0C_TRACE="$dry_trace" XM_DEPLOY_TEST_MODE=1 \
  "$mirror_script" --test-mode --repo "$dry_repo" --git-bin "$fake_git" --dry-run
assert_text "dry-run 仍会校验 github remote" "remote get-url github" "$dry_trace"
if [ "$(trace_count 'push --mirror github' "$dry_trace")" = "0" ]; then ok "dry-run 未 push"; else bad "dry-run 未 push"; fi

fail_repo="$tmp/fail-repo"
fail_origin="$tmp/fail-origin.git"
fail_github="$tmp/fail-github.git"
fail_trace="$tmp/fail.trace"
setup_repo "$fail_repo" "$fail_origin" "$fail_github" "$fail_github"
expect_failure "push 失败时返回非零" env REAL_GIT="$real_git" D0C_TRACE="$fail_trace" D0C_PUSH_FAIL=1 XM_DEPLOY_TEST_MODE=1 \
  "$mirror_script" --test-mode --repo "$fail_repo" --git-bin "$fake_git" --reason failed
if [ "$(trace_count 'push --mirror github' "$fail_trace")" = "1" ]; then ok "push 失败不重试"; else bad "push 失败不重试"; fi

blocked_repo="$tmp/blocked-repo"
blocked_origin="$tmp/blocked-origin.git"
blocked_github="$tmp/blocked-github.git"
setup_repo "$blocked_repo" "$blocked_origin" "$blocked_github" "$blocked_github"
expect_failure "拒绝非 github remote 名" env REAL_GIT="$real_git" D0C_TRACE="$tmp/blocked.trace" XM_DEPLOY_TEST_MODE=1 \
  "$mirror_script" --test-mode --repo "$blocked_repo" --git-bin "$fake_git" --remote origin --reason blocked
if [ ! -s "$tmp/blocked.trace" ]; then ok "非 github remote 未进入 git"; else bad "非 github remote 未进入 git"; fi

secret_repo="$tmp/secret-repo"
secret_origin="$tmp/secret-origin.git"
secret_github="$tmp/secret-github.git"
secret_trace="$tmp/secret.trace"
setup_repo "$secret_repo" "$secret_origin" "$secret_github" 'https://user:secret-token@github.example.invalid/org/repo.git'
expect_failure "含凭据的 remote URL 被拒绝" env REAL_GIT="$real_git" D0C_TRACE="$secret_trace" XM_DEPLOY_TEST_MODE=1 \
  "$mirror_script" --test-mode --repo "$secret_repo" --git-bin "$fake_git" --dry-run
assert_not_text "失败输出不泄露凭据" 'secret-token' "$tmp/stderr"
if [ "$(trace_count 'push --mirror github' "$secret_trace")" = "0" ]; then ok "凭据 URL 在 push 前被拦截"; else bad "凭据 URL 在 push 前被拦截"; fi

[ "$fail" -eq 0 ] && echo "DEPLOY0-C-MIRROR-TEST-OK"
exit "$fail"
