#!/bin/bash -p
# D0-c remote 配置 helper 的无网络测试。
set -uo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
script="$root/deploy/scripts/configure-remotes.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail=0
ok() { echo "ok - $1"; }
bad() { echo "not ok - $1" >&2; fail=1; }
expect_success() { local n="$1"; shift; if "$@" >/dev/null 2>"$tmp/err"; then ok "$n"; else bad "$n"; sed 's/^/  /' "$tmp/err" >&2; fi; }
expect_failure() { local n="$1"; shift; if "$@" >/dev/null 2>&1; then bad "$n"; else ok "$n"; fi; }

git init -q "$tmp/repo"
git -C "$tmp/repo" config user.email test@example.invalid
git -C "$tmp/repo" config user.name remote-test
printf 'x\n' > "$tmp/repo/README"
git -C "$tmp/repo" add README
git -C "$tmp/repo" commit -qm init
git -C "$tmp/repo" remote add origin git@github.com:xufei5620/xingmang-platform.git
git -C "$tmp/repo" config branch.main.remote origin
git -C "$tmp/repo" config branch.main.merge refs/heads/main

expect_success "remote helper dry-run 不改配置" env XM_DEPLOY_TEST_MODE=1 "$script" --test-mode --repo "$tmp/repo" --server-url "$tmp/server.git" --github-url git@github.com:xufei5620/xingmang-platform.git --dry-run
if [ "$(git -C "$tmp/repo" remote get-url origin)" = "git@github.com:xufei5620/xingmang-platform.git" ] && ! git -C "$tmp/repo" remote get-url github >/dev/null 2>&1; then ok "dry-run 配置保持原状"; else bad "dry-run 配置保持原状"; fi

git -C "$tmp/repo" remote add upstream git@github.com:example/other.git
expect_failure "remote helper 拒绝未登记 remote" env XM_DEPLOY_TEST_MODE=1 "$script" --test-mode --repo "$tmp/repo" --server-url "$tmp/server.git" --github-url git@github.com:xufei5620/xingmang-platform.git --dry-run
git -C "$tmp/repo" remote remove upstream

git -C "$tmp/repo" remote add github git@github.com:example/wrong.git
expect_failure "remote helper 拒绝不匹配 github URL" env XM_DEPLOY_TEST_MODE=1 "$script" --test-mode --repo "$tmp/repo" --server-url "$tmp/server.git" --github-url git@github.com:xufei5620/xingmang-platform.git --dry-run
git -C "$tmp/repo" remote remove github
git -C "$tmp/repo" config remote.origin.pushurl git@github.com:example/push-only.git
expect_failure "remote helper 拒绝隐藏 origin pushurl" env XM_DEPLOY_TEST_MODE=1 "$script" --test-mode --repo "$tmp/repo" --server-url "$tmp/server.git" --github-url git@github.com:xufei5620/xingmang-platform.git --dry-run
git -C "$tmp/repo" config --unset-all remote.origin.pushurl

expect_failure "remote helper 无确认拒绝写入" env XM_DEPLOY_TEST_MODE=1 "$script" --test-mode --repo "$tmp/repo" --server-url "$tmp/server.git" --github-url git@github.com:xufei5620/xingmang-platform.git
expect_success "remote helper 确认后切换 origin 并添加 github" env XM_DEPLOY_TEST_MODE=1 "$script" --test-mode --repo "$tmp/repo" --server-url "$tmp/server.git" --github-url git@github.com:xufei5620/xingmang-platform.git --confirm CONFIGURE-REMOTES
server_expected="$(cygpath -m "$tmp/server.git" 2>/dev/null || printf '%s' "$tmp/server.git")"
if [ "$(git -C "$tmp/repo" remote get-url origin)" = "$server_expected" ] && [ "$(git -C "$tmp/repo" remote get-url github)" = "git@github.com:xufei5620/xingmang-platform.git" ]; then ok "remote 最终映射正确"; else bad "remote 最终映射正确"; fi
if [ "$(git -C "$tmp/repo" config --get branch.main.remote)" = origin ]; then ok "分支跟踪仍指向服务器 origin"; else bad "分支跟踪仍指向服务器 origin"; fi
expect_success "remote helper 重跑保持幂等" env XM_DEPLOY_TEST_MODE=1 "$script" --test-mode --repo "$tmp/repo" --server-url "$tmp/server.git" --github-url git@github.com:xufei5620/xingmang-platform.git --confirm CONFIGURE-REMOTES
if git -C "$tmp/repo" config --get-all remote.origin.pushurl >/dev/null 2>&1 || git -C "$tmp/repo" config --get-all remote.github.pushurl >/dev/null 2>&1; then bad "remote 没有隐藏 pushurl"; else ok "remote 没有隐藏 pushurl"; fi

[ "$fail" -eq 0 ] && echo "DEPLOY0-C-REMOTES-TEST-OK"
exit "$fail"
