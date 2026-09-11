#!/usr/bin/env bash
# XM-C-DEPLOY0-a 的可重复、无服务器测试。
#
# 这些测试只在临时目录中模拟 receive hook、CI 执行器和安装命令；不会连接
# 真实服务器、不会修改系统用户，也不会推送到任何远端。
set -uo pipefail
umask 077

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
ci_script="$repo_root/scripts/ci-local.sh"
pre_hook="$repo_root/deploy/git-hooks/pre-receive"
post_hook="$repo_root/deploy/git-hooks/post-receive"
installer="$repo_root/deploy/scripts/install-git-server.sh"

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

assert_file() {
  local name="$1" path="$2"
  [ -e "$path" ] && ok "$name" || bad "$name (missing $path)"
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

assert_not_text() {
  local name="$1" forbidden="$2" path="$3"
  if [ ! -f "$path" ]; then
    bad "$name (missing file $path)"
  elif grep -Fq -- "$forbidden" "$path"; then
    bad "$name (found '$forbidden')"
  else
    ok "$name"
  fi
}

write_executable() {
  local path="$1" body="$2"
  mkdir -p "$(dirname "$path")"
  printf '%s\n' "$body" > "$path"
  chmod +x "$path"
}

###############################################################################
# ci-local.sh: all four gates are ordered, reproducible, and fail closed.
###############################################################################
gate_bin="$tmp/gates"
mkdir -p "$gate_bin"
gate_log="$tmp/gates.log"
trace_log="$tmp/runner-order.log"
for gate in governance secret-scan backend frontend; do
  printf -v gate_body '#!/usr/bin/env bash\nprintf "%%s\\n" "%s" >> "${CI_LOCAL_TRACE_LOG}"\n' "$gate"
  write_executable "$gate_bin/$gate" "$gate_body"
done

expect_success "ci-local 四门禁按固定顺序运行" env \
  CI_LOCAL_GATE_LOG="$gate_log" \
  CI_LOCAL_TRACE_LOG="$trace_log" \
  CI_LOCAL_ALLOW_NO_GIT=1 \
  CI_LOCAL_ALLOW_OVERRIDES=1 \
  CI_LOCAL_GOVERNANCE_CMD="$gate_bin/governance" \
  CI_LOCAL_SECRET_SCAN_CMD="$gate_bin/secret-scan" \
  CI_LOCAL_BACKEND_CMD="$gate_bin/backend" \
  CI_LOCAL_FRONTEND_CMD="$gate_bin/frontend" \
  bash "$ci_script"

if [ "$(tr '\n' ' ' < "$trace_log" 2>/dev/null)" = "governance secret-scan backend frontend " ]; then
  ok "ci-local 门禁顺序证据"
else
  bad "ci-local 门禁顺序证据"
fi

expect_failure "ci-local 缺少门禁命令时 fail-closed" env \
  CI_LOCAL_ALLOW_NO_GIT=1 \
  CI_LOCAL_ALLOW_OVERRIDES=1 \
  CI_LOCAL_GOVERNANCE_CMD="$gate_bin/governance" \
  CI_LOCAL_SECRET_SCAN_CMD="$tmp/does-not-exist" \
  CI_LOCAL_BACKEND_CMD="$gate_bin/backend" \
  CI_LOCAL_FRONTEND_CMD="$gate_bin/frontend" \
  bash "$ci_script"

for broken_gate in governance secret-scan backend frontend; do
  broken_cmd="$gate_bin/${broken_gate}-fail"
  write_executable "$broken_cmd" '#!/usr/bin/env bash
printf "broken\n" >> "${CI_LOCAL_TRACE_LOG}"
exit 17
'
  case "$broken_gate" in
    governance)
      gov_cmd="$broken_cmd"; sec_cmd="$gate_bin/secret-scan"; back_cmd="$gate_bin/backend"; front_cmd="$gate_bin/frontend" ;;
    secret-scan)
      gov_cmd="$gate_bin/governance"; sec_cmd="$broken_cmd"; back_cmd="$gate_bin/backend"; front_cmd="$gate_bin/frontend" ;;
    backend)
      gov_cmd="$gate_bin/governance"; sec_cmd="$gate_bin/secret-scan"; back_cmd="$broken_cmd"; front_cmd="$gate_bin/frontend" ;;
    frontend)
      gov_cmd="$gate_bin/governance"; sec_cmd="$gate_bin/secret-scan"; back_cmd="$gate_bin/backend"; front_cmd="$broken_cmd" ;;
  esac
  expect_failure "ci-local $broken_gate 门禁失败不假绿" env \
    CI_LOCAL_ALLOW_NO_GIT=1 \
    CI_LOCAL_ALLOW_OVERRIDES=1 \
    CI_LOCAL_GOVERNANCE_CMD="$gov_cmd" \
    CI_LOCAL_SECRET_SCAN_CMD="$sec_cmd" \
    CI_LOCAL_BACKEND_CMD="$back_cmd" \
    CI_LOCAL_FRONTEND_CMD="$front_cmd" \
    CI_LOCAL_TRACE_LOG="$trace_log" \
    bash "$ci_script"
done

expect_failure "ci-local 默认拒绝没有 Git 元数据的目录" env \
  CI_LOCAL_ROOT="$tmp" \
  bash "$ci_script"

###############################################################################
# receive hooks: use a real temporary bare repository and real commit graph.
###############################################################################
source_repo="$tmp/source"
bare_repo="$tmp/repository.git"
git init -q "$source_repo"
git -C "$source_repo" config user.email test@example.invalid
git -C "$source_repo" config user.name deploy0-test
printf 'one\n' > "$source_repo/file.txt"
git -C "$source_repo" add file.txt
git -C "$source_repo" commit -qm initial
commit_one="$(git -C "$source_repo" rev-parse HEAD)"
printf 'two\n' >> "$source_repo/file.txt"
git -C "$source_repo" commit -qam second
commit_two="$(git -C "$source_repo" rev-parse HEAD)"
git init -q --bare "$bare_repo"
git -C "$source_repo" push -q "$bare_repo" "$commit_one:refs/heads/seed"
git --git-dir="$bare_repo" update-ref refs/heads/main "$commit_one"
git -C "$source_repo" push -q "$bare_repo" "$commit_two:refs/heads/candidate"

run_pre() {
  local line="$1"
  (cd "$bare_repo" && printf '%s\n' "$line" | \
    PRE_RECEIVE_PROMOTE_MARKER="$tmp/promote.marker" \
    PRE_RECEIVE_ALLOW_MARKER_OVERRIDE=1 \
    bash "$pre_hook")
}

zero=0000000000000000000000000000000000000000
expect_success "pre-receive 接受新分支创建" run_pre \
  "$zero $commit_one refs/heads/release/v0.1-launch"
assert_not_text "pre-receive 非 main 不伪称 promote" "promote-authorized main" "$tmp/stdout"
expect_failure "pre-receive 拒绝删除分支" run_pre \
  "$commit_one $zero refs/heads/release/v0.1-launch"
expect_failure "pre-receive 拒绝非快进更新" run_pre \
  "$commit_two $commit_one refs/heads/candidate"
expect_failure "pre-receive main 没有 promote 标记时拒绝" run_pre \
  "$commit_one $commit_two refs/heads/main"

printf 'sha=%s\n' "$commit_two" > "$tmp/promote.marker"
expect_success "pre-receive main 只接受匹配 promote 标记" run_pre \
  "$commit_one $commit_two refs/heads/main"
[ ! -e "$tmp/promote.marker" ] && ok "pre-receive 成功后消费 promote 标记" || bad "pre-receive 成功后消费 promote 标记"

# 安装器写入的 Git config 是生产路径；pre-receive 不应只认测试环境变量。
printf 'three\n' >> "$source_repo/file.txt"
git -C "$source_repo" commit -qam third
commit_three="$(git -C "$source_repo" rev-parse HEAD)"
git -C "$source_repo" push -q "$bare_repo" "$commit_three:refs/heads/candidate-three"
configured_marker="$bare_repo/configured-promote.marker"
git --git-dir="$bare_repo" config xm.receive.promoteMarker "$configured_marker"
# 旧 marker-only 测试显式标注 legacy/test 配置；真实安装器会写 true，
# 未登记该配置的服务器由 hook 默认拒绝晋级。
git --git-dir="$bare_repo" config xm.receive.requirePromoteLock false
printf 'sha=%s\n' "$commit_three" > "$configured_marker"
run_pre_config() {
  local line="$1"
  (cd "$bare_repo" && printf '%s\n' "$line" | env -u PRE_RECEIVE_PROMOTE_MARKER \
    PRE_RECEIVE_ALLOW_MARKER_OVERRIDE=1 bash "$pre_hook")
}
expect_success "pre-receive 读取安装器写入的 marker 配置" run_pre_config \
  "$commit_one $commit_three refs/heads/main"
[ ! -e "$configured_marker" ] && ok "配置 marker 成功后被消费" || bad "配置 marker 成功后被消费"
printf 'sha=%s\nextra=not-allowed\n' "$commit_three" > "$configured_marker"
expect_failure "pre-receive 拒绝含额外字段的 marker" run_pre_config \
  "$commit_one $commit_three refs/heads/main"
[ -e "$configured_marker" ] && ok "非法 marker 失败时不被消费" || bad "非法 marker 失败时不被消费"
printf 'sha=%s\n' "$commit_three" > "$configured_marker"
expect_failure "pre-receive 拒绝过期 old SHA 并保留 marker" run_pre_config \
  "$commit_two $commit_three refs/heads/main"
[ -e "$configured_marker" ] && ok "过期推送失败时不消费 marker" || bad "过期推送失败时不消费 marker"
rm -f -- "$configured_marker"

ci_executor="$tmp/ci-executor"
write_executable "$ci_executor" '#!/usr/bin/env bash
printf "executor sha=%s\n" "${2:-unknown}"
printf "git-head=%s\n" "$(git -C "$1" rev-parse HEAD)"
env
'
status_dir="$tmp/status"
work_dir="$tmp/work"
run_post() {
  local line="$1"
  (cd "$bare_repo" && printf '%s\n' "$line" | \
    POST_RECEIVE_STATUS_DIR="$status_dir" \
    POST_RECEIVE_WORK_DIR="$work_dir" \
    POST_RECEIVE_RELEASE_REF='refs/heads/release/v0.1-launch' \
    POST_RECEIVE_CI_COMMAND="$ci_executor" \
    POST_RECEIVE_ALLOW_CUSTOM_EXECUTOR=1 \
    POST_RECEIVE_EXECUTOR_PATH='/usr/bin:/bin:/mingw64/bin' \
    SECRET_SENTINEL='should-not-cross-boundary' \
    bash "$post_hook")
}

expect_success "post-receive 仅对 release 分支触发 CI" run_post \
  "$zero $commit_one refs/heads/release/v0.1-launch"
assert_file "post-receive 写入 status" "$status_dir/$commit_one.status"
assert_text "post-receive status 为 green" "green" "$status_dir/$commit_one.status"
assert_file "post-receive 写入 CI 日志" "$status_dir/$commit_one.log"
assert_text "post-receive 日志包含执行器输出" "executor sha=$commit_one" "$status_dir/$commit_one.log"
assert_text "post-receive CI checkout 保留 Git 元数据" "git-head=$commit_one" "$status_dir/$commit_one.log"

before_logs="$(find "$status_dir" -maxdepth 1 -name '*.log' -type f 2>/dev/null | wc -l | tr -d ' ')"
expect_success "post-receive 忽略非 release 分支" run_post \
  "$commit_one $commit_two refs/heads/candidate"
after_logs="$(find "$status_dir" -maxdepth 1 -name '*.log' -type f 2>/dev/null | wc -l | tr -d ' ')"
[ "$before_logs" = "$after_logs" ] && ok "post-receive 非 release 不执行 CI" || bad "post-receive 非 release 不执行 CI"
assert_not_text "post-receive 自定义执行器不继承敏感环境" "should-not-cross-boundary" "$status_dir/$commit_one.log"

failure_executor="$tmp/failure-executor"
write_executable "$failure_executor" '#!/usr/bin/env bash
printf "controlled executor failure\n"
exit 23
'
expect_failure "post-receive CI 失败返回非零" env \
  POST_RECEIVE_STATUS_DIR="$status_dir/failure" \
  POST_RECEIVE_WORK_DIR="$work_dir/failure" \
  POST_RECEIVE_RELEASE_REF='refs/heads/release/v0.1-launch' \
  POST_RECEIVE_CI_COMMAND="$failure_executor" \
  POST_RECEIVE_ALLOW_CUSTOM_EXECUTOR=1 \
  POST_RECEIVE_EXECUTOR_PATH='/usr/bin:/bin:/mingw64/bin' \
  bash -c 'cd "$1" && printf "%s\n" "$2 $3 refs/heads/release/v0.1-launch" | bash "$4"' _ \
  "$bare_repo" "$commit_one" "$commit_two" "$post_hook"
assert_text "post-receive 失败状态为 red" "red" "$status_dir/failure/$commit_two.status"
assert_text "post-receive 失败日志保留执行器输出" "controlled executor failure" "$status_dir/failure/$commit_two.log"
assert_text "post-receive 失败日志保留退出码" "ci_exit=23" "$status_dir/failure/$commit_two.log"
assert_not_text "post-receive 失败日志没有完成标记" "ci_finished=" "$status_dir/failure/$commit_two.log"
[ -z "$(find "$work_dir/failure" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ] && ok "post-receive 失败后清理 checkout" || bad "post-receive 失败后清理 checkout"

slow_executor="$tmp/slow-executor"
write_executable "$slow_executor" '#!/usr/bin/env bash
sleep 1
printf "slow executor sha=%s\n" "${2:-unknown}"
'
concurrent_status="$tmp/status-concurrent"
concurrent_work="$tmp/work-concurrent"
concurrent_line="$zero $commit_two refs/heads/release/v0.1-launch"
(
  cd "$bare_repo" && printf '%s\n' "$concurrent_line" | \
    POST_RECEIVE_STATUS_DIR="$concurrent_status" \
    POST_RECEIVE_WORK_DIR="$concurrent_work" \
    POST_RECEIVE_RELEASE_REF='refs/heads/release/v0.1-launch' \
    POST_RECEIVE_CI_COMMAND="$slow_executor" \
    POST_RECEIVE_ALLOW_CUSTOM_EXECUTOR=1 \
    POST_RECEIVE_EXECUTOR_PATH='/usr/bin:/bin:/mingw64/bin' \
    bash "$post_hook" >"$tmp/concurrent-1.out" 2>"$tmp/concurrent-1.err"
) & concurrent_pid1=$!
sleep 0.1
(
  cd "$bare_repo" && printf '%s\n' "$concurrent_line" | \
    POST_RECEIVE_STATUS_DIR="$concurrent_status" \
    POST_RECEIVE_WORK_DIR="$concurrent_work" \
    POST_RECEIVE_RELEASE_REF='refs/heads/release/v0.1-launch' \
    POST_RECEIVE_CI_COMMAND="$slow_executor" \
    POST_RECEIVE_ALLOW_CUSTOM_EXECUTOR=1 \
    POST_RECEIVE_EXECUTOR_PATH='/usr/bin:/bin:/mingw64/bin' \
    bash "$post_hook" >"$tmp/concurrent-2.out" 2>"$tmp/concurrent-2.err"
) & concurrent_pid2=$!
wait "$concurrent_pid1"; concurrent_rc1=$?
wait "$concurrent_pid2"; concurrent_rc2=$?
if { [ "$concurrent_rc1" -eq 0 ] && [ "$concurrent_rc2" -ne 0 ]; } || { [ "$concurrent_rc2" -eq 0 ] && [ "$concurrent_rc1" -ne 0 ]; }; then
  ok "post-receive 同一 SHA 并发时只有一个执行器获锁"
else
  bad "post-receive 同一 SHA 并发时只有一个执行器获锁 (rc1=$concurrent_rc1 rc2=$concurrent_rc2)"
fi
assert_text "post-receive 并发最终状态为 green" "green" "$concurrent_status/$commit_two.status"
[ -z "$(find "$concurrent_work" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ] && ok "post-receive 并发后清理 checkout" || bad "post-receive 并发后清理 checkout"

# Docker 分支必须挂载服务器登记的可信 CI 脚本，而不是执行提交中的同名文件。
trusted_ci="$tmp/trusted-ci-local.sh"
cp "$ci_script" "$trusted_ci"
chmod +x "$trusted_ci"
trusted_hash="$(sha256sum -- "$trusted_ci" | awk '{print $1}')"
git --git-dir="$bare_repo" config xm.ci.script "$trusted_ci"
git --git-dir="$bare_repo" config xm.ci.scriptSha256 "$trusted_hash"
fake_docker="$tmp/fake-docker"
write_executable "$fake_docker" '#!/usr/bin/env bash
printf "%s\n" "$*" > "$DOCKER_ARGS_PATH"
'
docker_args_path="$tmp/docker-args"
docker_status="$tmp/status-docker"
docker_work="$tmp/work-docker"
expect_success "post-receive Docker CI 使用可信脚本配置" env \
  POST_RECEIVE_STATUS_DIR="$docker_status" \
  POST_RECEIVE_WORK_DIR="$docker_work" \
  POST_RECEIVE_RELEASE_REF='refs/heads/release/v0.1-launch' \
  POST_RECEIVE_DOCKER_BIN="$fake_docker" \
  DOCKER_ARGS_PATH="$docker_args_path" \
  bash -c 'cd "$1" && printf "%s\n" "$2 $3 refs/heads/release/v0.1-launch" | bash "$4"' _ \
  "$bare_repo" "$zero" "$commit_one" "$post_hook"
assert_text "Docker CI 状态为 green" "green" "$docker_status/$commit_one.status"
assert_text "Docker CI 使用固定镜像拉取策略" "--pull=never" "$tmp/docker-args"
assert_text "Docker CI 默认无网络" "--network none" "$tmp/docker-args"
assert_text "Docker CI 挂载可信脚本" "$trusted_ci:/opt/xm-ci-local.sh:ro" "$tmp/docker-args"
assert_text "Docker CI 强制完整治理基线" "GOVERNANCE_REQUIRE_BASE=1" "$tmp/docker-args"
assert_not_text "Docker CI 不执行提交内脚本路径" "bash scripts/ci-local.sh" "$tmp/docker-args"

bad_hash_status="$tmp/status-docker-bad-hash"
git --git-dir="$bare_repo" config xm.ci.scriptSha256 "$(printf '0%.0s' {1..64})"
expect_failure "post-receive 拒绝可信 CI 脚本哈希不匹配" env \
  POST_RECEIVE_STATUS_DIR="$bad_hash_status" \
  POST_RECEIVE_WORK_DIR="$tmp/work-docker-bad-hash" \
  POST_RECEIVE_RELEASE_REF='refs/heads/release/v0.1-launch' \
  POST_RECEIVE_DOCKER_BIN="$fake_docker" \
  DOCKER_ARGS_PATH="$tmp/bad-hash-docker-args" \
  bash -c 'cd "$1" && printf "%s\n" "$2 $3 refs/heads/release/v0.1-launch" | bash "$4"' _ \
  "$bare_repo" "$zero" "$commit_one" "$post_hook"
assert_text "哈希负例到达目标校验" '可信 CI 脚本哈希不匹配' "$bad_hash_status/$commit_one.log"
if [ ! -e "$tmp/bad-hash-docker-args" ]; then ok "坏哈希未启动 Docker"; else bad "坏哈希未启动 Docker"; fi

expect_failure "post-receive 拒绝含空格的状态目录" env \
  POST_RECEIVE_STATUS_DIR="$tmp/status bad" \
  POST_RECEIVE_WORK_DIR="$tmp/work-bad" \
  bash -c 'cd "$1" && printf "\n" | bash "$2"' _ "$bare_repo" "$post_hook"
expect_failure "post-receive 拒绝含 .. 的工作目录" env \
  POST_RECEIVE_STATUS_DIR="$tmp/status-dot" \
  POST_RECEIVE_WORK_DIR="$tmp/work/../escape" \
  bash -c 'cd "$1" && printf "\n" | bash "$2"' _ "$bare_repo" "$post_hook"
expect_failure "post-receive 没有显式测试开关时拒绝自定义执行器" env \
  POST_RECEIVE_STATUS_DIR="$tmp/status-no-override" \
  POST_RECEIVE_WORK_DIR="$tmp/work-no-override" \
  POST_RECEIVE_CI_COMMAND="$ci_executor" \
  bash -c 'cd "$1" && printf "%s\n" "$2 $3 refs/heads/release/v0.1-launch" | bash "$4"' _ \
  "$bare_repo" "$commit_one" "$commit_two" "$post_hook"

###############################################################################
# installer: root, absolute paths, confirmation, idempotent dry-run contract.
###############################################################################
fake_bin="$tmp/fake-bin"
mkdir -p "$fake_bin"
write_executable "$fake_bin/id" '#!/usr/bin/env bash
if [ "${1:-}" = "-u" ]; then printf "0\n"; else /usr/bin/id "$@"; fi
'

expect_failure "安装脚本拒绝相对路径" env PATH="$fake_bin:$PATH" \
  bash "$installer" --repo relative.git --ci-dir /srv/ci --dry-run
expect_failure "安装脚本没有显式确认时拒绝执行" env PATH="$fake_bin:$PATH" \
  bash "$installer" --repo /srv/git/xingmang-platform.git --ci-dir /srv/ci

expect_success "安装脚本 dry-run 接受绝对路径并不输出凭据" env PATH="$fake_bin:$PATH" \
  bash "$installer" --repo /srv/git/xingmang-platform.git --ci-dir /srv/ci --dry-run
assert_not_text "安装脚本输出不含 token" "TOKEN" "$tmp/stdout"
assert_not_text "安装脚本输出不含 password" "PASSWORD" "$tmp/stdout"
if grep -Eq '^---(POST|PRE)' "$installer"; then
  bad "安装脚本不应拼接 receive hook 源码"
else
  ok "安装脚本内容边界完整"
fi
expect_success "安装脚本实际调用可信 CI 与禁止删除配置" python3 "$repo_root/tests/deploy/installer-config.test.py"

if [ "$fail" -eq 0 ]; then
  printf 'DEPLOY0-A-TEST-OK\n'
fi
exit "$fail"
