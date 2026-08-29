#!/usr/bin/env bash
# deploy-local.sh 的本地契约测试。
#
# 这里不碰真实 Docker 栈：用显式 test-mode 的 fake docker/curl 验证命令边界，
# 真实栈验证由 deploy-local.sh 的 staging 运行记录在对应 Handoff 中承担。
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd -P)"
deploy_script="$repo_root/deploy/scripts/deploy-local.sh"
tmp="$(mktemp -d)"
trap 'rm -rf -- "$tmp"' EXIT
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
  if [ -f "$path" ] && grep -Fq -- "$expected" "$path"; then ok "$name"; else bad "$name"; fi
}

assert_not_text() {
  local name="$1" forbidden="$2" path="$3"
  if [ -f "$path" ] && grep -Fq -- "$forbidden" "$path"; then bad "$name"; else ok "$name"; fi
}

write_executable() {
  local path="$1" body="$2"
  mkdir -p -- "$(dirname "$path")"
  printf '%s\n' "$body" > "$path"
  chmod +x "$path"
}

if [ ! -x "$deploy_script" ]; then
  bad "deploy-local.sh 存在且可执行"
  printf 'DEPLOY-LOCAL-TEST-FAILED\n'
  exit "$fail"
fi
ok "deploy-local.sh 存在且可执行"
if bash -n "$deploy_script"; then ok "deploy-local.sh shell 语法"; else bad "deploy-local.sh shell 语法"; fi

if BASH_ENV="$tmp/evil-bash-env" "$deploy_script" --help >/dev/null 2>&1; then
  ok "help 可调用"
else
  bad "help 可调用"
fi

fixture="$tmp/xingmang-platform"
mkdir -p "$fixture/deploy/compose"
cp "$repo_root/deploy/compose/launch.yaml" "$fixture/deploy/compose/launch.yaml"
printf 'ENVIRONMENT=staging\nPOSTGRES_DB=xingmang\nPOSTGRES_USER=xingmang\nDATABASE_PASSWORD=test-only.invalid\n' > "$fixture/deploy/compose/.env"
git -C "$fixture" init -q
git -C "$fixture" config user.email test@example.invalid
git -C "$fixture" config user.name deploy-local-test
git -C "$fixture" add deploy/compose
git -C "$fixture" commit -qm seed
git -C "$fixture" branch -M release/v0.1-launch
git -C "$fixture" remote add origin https://github.com/xufei5620/xingmang-platform.git
fixture_sha="$(git -C "$fixture" rev-parse HEAD)"

trace="$tmp/trace"
fake_bin="$tmp/bin"
mkdir -p "$fake_bin"

write_executable "$fake_bin/docker" '#!/usr/bin/env bash
set -u
printf "docker %s\n" "$*" >> "${DEPLOY_LOCAL_TRACE:?}"
if [ "${FAKE_REQUIRE_LOCAL_ENV:-0}" = "1" ]; then
  [ "${ENVIRONMENT:-}" = "staging" ] || exit 41
  [ "${WEB_BIND:-}" = "127.0.0.1" ] || exit 41
  [ "${WEB_PORT:-}" = "8088" ] || exit 41
  [ "${BUILD_VERSION:-}" = "staging" ] || exit 41
  [ -n "${BUILD_COMMIT:-}" ] || exit 41
  [ -z "${XM_SUB2API_MODE:-}" ] || exit 41
  [ -z "${DATABASE_PASSWORD:-}" ] || exit 41
fi
if [ "${FAKE_REQUIRE_DOCKER_CONFIG:-0}" = "1" ]; then
  case "${DOCKER_CONFIG:-}" in /tmp/*|/var/tmp/*) ;; *) exit 42 ;; esac
fi
case " $* " in
 *" context show "*) printf "default\n"; exit 0 ;;
 *" context inspect "*) printf "npipe:////./pipe/docker_engine\n"; exit 0 ;;
 *" ps -q --status running platform-worker "*)
    [ "${FAKE_DOCKER_WORKER_DOWN:-0}" = "1" ] || printf "worker-container-id\n"
    exit 0 ;;
  *" ps -q "*)
    [ "${FAKE_DOCKER_BASELINE:-0}" = "1" ] && printf "existing-stack-id\n"
    exit 0 ;;
esac
case " $* " in
  *" version "*) printf "29.7.2\n"; exit 0 ;;
  *" info "*) printf "Server Version: 29.7.2\n"; exit 0 ;;
  *" run "*" runway-threshold-bootstrap "*)
    if [ "${FAKE_DOCKER_FAIL_RUNWAY_BOOTSTRAP:-0}" = "1" ]; then exit 18; fi
    printf "environment=staging revision=1 critical_days=5 warning_days=10 serious_days=20\n"; exit 0 ;;
  *" run "*" bootstrap "*)
    if [ "${FAKE_DOCKER_FAIL_BOOTSTRAP:-0}" = "1" ]; then exit 17; fi
    printf "INSERT 0 0\nCOMMIT\n"; exit 0 ;;
  *" compose "*" logs "*) printf "worker heartbeat\n"; exit 0 ;;
  *) exit 0 ;;
esac
'

write_executable "$fake_bin/curl" '#!/usr/bin/env bash
set -u
printf "curl %s\n" "$*" >> "${DEPLOY_LOCAL_TRACE:?}"
if [ "${FAKE_REQUIRE_NOPROXY:-0}" = "1" ]; then
  case " $* " in *" --noproxy "*|*" --noproxy="*) ;; *) exit 43 ;; esac
fi
out=""
url=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-o" ]; then out="$arg"; fi
  case "$arg" in http://*|https://*) url="$arg" ;; esac
  prev="$arg"
done
if [ "${FAKE_CURL_FAIL:-0}" = "1" ]; then exit 7; fi
body="{\"items\":[{\"instance_id\":\"sub2api-staging\"}]}"
case "$url" in
  */healthz) body="{\"status\":\"ok\"}" ;;
  */readyz) body="{\"status\":\"ready\"}" ;;
esac
if [[ "$url" == */api/v1/services ]] && [ "${FAKE_CURL_NO_SEED:-0}" = "1" ]; then
  body="{\"items\":[]}"
fi
if [ -n "$out" ]; then printf "%s\n" "$body" > "$out"; else printf "%s\n" "$body"; fi
printf "200"
exit 0
'

common_env=(
  "XM_DEPLOY_LOCAL_TEST_MODE=1"
  "XM_DEPLOY_LOCAL_SKIP_GIT=1"
  "XM_DEPLOY_LOCAL_DOCKER_BIN=$fake_bin/docker"
  "XM_DEPLOY_LOCAL_CURL_BIN=$fake_bin/curl"
  "DEPLOY_LOCAL_TRACE=$trace"
)

: > "$trace"
expect_success "test-mode 执行完整本地部署链" env "${common_env[@]}" ENVIRONMENT=production WEB_BIND=0.0.0.0 WEB_PORT=9999 BUILD_VERSION=evil BUILD_COMMIT=evil XM_SUB2API_MODE=real DATABASE_PASSWORD=DO_NOT_LEAK FAKE_REQUIRE_LOCAL_ENV=1 FAKE_REQUIRE_DOCKER_CONFIG=1 FAKE_REQUIRE_NOPROXY=1 \
  "$deploy_script" --test-mode --repo "$fixture" --env-file "$fixture/deploy/compose/.env" \
  --compose-file "$fixture/deploy/compose/launch.yaml" --sha "$fixture_sha" \
  --probe-attempts 1

assert_text "输出含本地部署通过" 'DEPLOY LOCAL PASS' "$tmp/stdout"
assert_text "Docker context endpoint 已校验" 'context inspect' "$trace"
assert_text "config 在 build 前" ' config ' "$trace"
assert_not_text "Compose 不覆盖项目目录解析" '--project-directory' "$trace"
assert_text "build 被执行" ' build ' "$trace"
assert_text "up 被执行" ' up ' "$trace"
assert_not_text "up 不让一次性 bootstrap 触发 wait 假失败" ' up -d --remove-orphans --wait' "$trace"
assert_text "runway 阈值 bootstrap 在栈上执行" 'runway-threshold-bootstrap' "$trace"
assert_text "bootstrap 幂等登记被执行" ' bootstrap' "$trace"
assert_not_text "bootstrap 不绕过迁移依赖" '--no-deps bootstrap' "$trace"
runway_line="$(grep -n 'runway-threshold-bootstrap' "$trace" | head -n 1 | cut -d: -f1)"
app_up_line="$(grep -n ' up -d platform-api platform-worker web' "$trace" | head -n 1 | cut -d: -f1)"
if [ -n "$runway_line" ] && [ -n "$app_up_line" ] && [ "$runway_line" -lt "$app_up_line" ]; then
  ok "阈值 bootstrap 在 API/worker 全栈启动前"
else
  bad "阈值 bootstrap 在 API/worker 全栈启动前"
fi
assert_text "healthz 被探测" '/healthz' "$trace"
assert_text "readyz 被探测" '/readyz' "$trace"
assert_text "services 烟测被执行" '/api/v1/services' "$trace"
assert_text "metrics 烟测被执行" '/api/v1/metrics' "$trace"
assert_text "alerts 烟测被执行" '/api/v1/alerts' "$trace"
assert_text "烟测带 registry.read" 'registry.read' "$trace"
assert_text "烟测带 ops.read" 'ops.read' "$trace"

: > "$trace"
expect_success "dry-run 不调用 Docker/Git" env "${common_env[@]}" \
  "$deploy_script" --test-mode --dry-run --repo "$fixture" --env-file "$fixture/deploy/compose/.env" \
  --compose-file "$fixture/deploy/compose/launch.yaml" --sha "$fixture_sha"
if [ ! -s "$trace" ]; then ok "dry-run 无外部命令副作用"; else bad "dry-run 无外部命令副作用"; fi

printf 'dirty\n' > "$fixture/dirty.txt"
expect_failure "脏工作树拒绝部署" env "${common_env[@]}" \
  "$deploy_script" --test-mode --repo "$fixture" --env-file "$fixture/deploy/compose/.env" \
  --compose-file "$fixture/deploy/compose/launch.yaml" --sha "$fixture_sha" --probe-attempts 1
rm -f -- "$fixture/dirty.txt"

: > "$trace"
expect_failure "bootstrap 失败立即停止" env "${common_env[@]}" FAKE_DOCKER_FAIL_BOOTSTRAP=1 \
  "$deploy_script" --test-mode --repo "$fixture" --env-file "$fixture/deploy/compose/.env" \
  --compose-file "$fixture/deploy/compose/launch.yaml" --sha "$fixture_sha" --probe-attempts 1
if grep -Fq '/healthz' "$trace" || grep -Fq '/api/v1/services' "$trace"; then
  bad "bootstrap 失败未继续探针/烟测"
else
  ok "bootstrap 失败未继续探针/烟测"
fi

: > "$trace"
expect_failure "runway 阈值 bootstrap 失败立即停止" env "${common_env[@]}" FAKE_DOCKER_FAIL_RUNWAY_BOOTSTRAP=1 \
  "$deploy_script" --test-mode --repo "$fixture" --env-file "$fixture/deploy/compose/.env" \
  --compose-file "$fixture/deploy/compose/launch.yaml" --sha "$fixture_sha" --probe-attempts 1
if grep -Fq ' up -d platform-api platform-worker web' "$trace" || grep -Fq '/healthz' "$trace"; then
  bad "runway bootstrap 失败仍启动 API/worker 或探针"
else
  ok "runway bootstrap 失败阻止 API/worker 与探针"
fi

# 过渡期镜像远端可能不可达；指定的 MERGED SHA 已在本地 release HEAD 时，
# 仍可安全部署本地精确版本。其它 Git 操作委托给真实 git，只有 fetch 模拟失败。
real_git="$(command -v git)"
write_executable "$fake_bin/git" "#!/usr/bin/env bash
for arg in \"\$@\"; do
  [ \"\$arg\" = fetch ] && exit 128
done
exec \"$real_git\" \"\$@\"
"
: > "$trace"
expect_success "本地精确 SHA 不被不可达镜像阻断" env PATH="$fake_bin:$PATH" DEPLOY_LOCAL_TRACE="$trace" \
  "$deploy_script" --repo "$fixture" --env-file "$fixture/deploy/compose/.env" \
  --compose-file "$fixture/deploy/compose/launch.yaml" --sha "$fixture_sha" --probe-attempts 1
assert_text "镜像不可达被明确标注" 'git-fetch=unavailable local-sha-verified' "$tmp/stderr"

# fetch 成功时必须从 feature checkout 切到 release，并快进到 FETCH_HEAD，不能
# 因为调用方没有传 --sha 就部署旧的本地 release。
remote="$tmp/remote.git"
remote_seed="$tmp/remote-seed"
git init --bare -q "$remote"
git -C "$fixture" remote set-url origin "$remote"
git -C "$fixture" push -q origin release/v0.1-launch:refs/heads/release/v0.1-launch
git --git-dir="$remote" symbolic-ref HEAD refs/heads/release/v0.1-launch
git clone -q --branch release/v0.1-launch "$remote" "$remote_seed"
git -C "$remote_seed" config user.email deploy-local-test@example.invalid
git -C "$remote_seed" config user.name deploy-local-test
printf 'remote-new\n' > "$remote_seed/remote-marker"
git -C "$remote_seed" add remote-marker
git -C "$remote_seed" commit -qm remote-new
git -C "$remote_seed" push -q origin HEAD:refs/heads/release/v0.1-launch
remote_sha="$("$real_git" -C "$remote_seed" rev-parse HEAD 2>/dev/null || git -C "$remote_seed" rev-parse HEAD)"
git -C "$fixture" checkout -qb feature
: > "$trace"
expect_success "fetch 后切换 release 并快进到最新远端" env XM_DEPLOY_LOCAL_TEST_MODE=1 XM_DEPLOY_LOCAL_SKIP_GIT=0 XM_DEPLOY_LOCAL_DOCKER_BIN="$fake_bin/docker" XM_DEPLOY_LOCAL_CURL_BIN="$fake_bin/curl" DEPLOY_LOCAL_TRACE="$trace" "$deploy_script" --test-mode --repo "$fixture" --env-file "$fixture/deploy/compose/.env" --compose-file "$fixture/deploy/compose/launch.yaml" --probe-attempts 1
assert_text "部署目标为 fetch 后的 release SHA" "sha=$remote_sha" "$tmp/stdout"
fixture_sha="$remote_sha"
git -C "$fixture" checkout -q release/v0.1-launch

expect_failure "正式模式固定 Web 端口" env PATH="$fake_bin:$PATH" DEPLOY_LOCAL_TRACE="$trace" \
  "$deploy_script" --repo "$fixture" --env-file "$fixture/deploy/compose/.env" \
  --compose-file "$fixture/deploy/compose/launch.yaml" --web-url http://127.0.0.1:9999 \
  --sha "$fixture_sha" --probe-attempts 1

: > "$trace"
expect_failure "缺少 staging 演示登记时 smoke 失败" env "${common_env[@]}" FAKE_CURL_NO_SEED=1 \
  "$deploy_script" --test-mode --repo "$fixture" --env-file "$fixture/deploy/compose/.env" \
  --compose-file "$fixture/deploy/compose/launch.yaml" --sha "$fixture_sha" --probe-attempts 1
if grep -Fq '/api/v1/metrics' "$trace" || grep -Fq '/api/v1/alerts' "$trace"; then
  bad "services seed 缺失未阻止后续 smoke"
else
  ok "services seed 缺失阻止后续 smoke"
fi

: > "$trace"
expect_success "worker 运行状态被纳入部署验证" env "${common_env[@]}" \
  "$deploy_script" --test-mode --repo "$fixture" --env-file "$fixture/deploy/compose/.env" \
  --compose-file "$fixture/deploy/compose/launch.yaml" --sha "$fixture_sha" --probe-attempts 1
assert_text "部署后确认 worker 仍运行" 'ps -q --status running platform-worker' "$trace"

: > "$trace"
expect_success "已有栈先完成健康与 smoke 基线" env "${common_env[@]}" FAKE_DOCKER_BASELINE=1 \
  "$deploy_script" --test-mode --repo "$fixture" --env-file "$fixture/deploy/compose/.env" \
  --compose-file "$fixture/deploy/compose/launch.yaml" --sha "$fixture_sha" --probe-attempts 1
build_line="$(grep -n ' build ' "$trace" | head -n 1 | cut -d: -f1)"
baseline_health_line="$(grep -n '/healthz' "$trace" | head -n 1 | cut -d: -f1)"
baseline_services_line="$(grep -n '/api/v1/services' "$trace" | head -n 1 | cut -d: -f1)"
if [ -n "$build_line" ] && [ -n "$baseline_health_line" ] && [ "$baseline_health_line" -lt "$build_line" ]; then
  ok "基线 healthz 在 build 前"
else
  bad "基线 healthz 在 build 前"
fi
if [ -n "$build_line" ] && [ -n "$baseline_services_line" ] && [ "$baseline_services_line" -lt "$build_line" ]; then
  ok "基线 services smoke 在 build 前"
else
  bad "基线 services smoke 在 build 前"
fi

: > "$trace"
expect_failure "worker 已退出时部署失败" env "${common_env[@]}" FAKE_DOCKER_WORKER_DOWN=1 \
  "$deploy_script" --test-mode --repo "$fixture" --env-file "$fixture/deploy/compose/.env" \
  --compose-file "$fixture/deploy/compose/launch.yaml" --sha "$fixture_sha" --probe-attempts 1
if grep -Fq '/healthz' "$trace" || grep -Fq '/api/v1/services' "$trace"; then
  bad "worker 退出未阻止后续探针/烟测"
else
  ok "worker 退出阻止后续阶段"
fi

assert_not_text "脚本不含 down -v" 'down -v' "$deploy_script"
assert_not_text "脚本不含 reset --hard" 'reset --hard' "$deploy_script"

[ "$fail" -eq 0 ] && echo "DEPLOY-LOCAL-TEST-OK"
exit "$fail"
