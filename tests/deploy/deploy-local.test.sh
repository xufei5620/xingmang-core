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
# XM-DEPLOY-SELFUPDATE0：仅供「运行中途脚本文件被改写」回归测试使用。
# 在 build 这一步把目标路径的脚本文件替换成明显损坏的内容，模拟脚本
# 运行期间被别的 git/部署操作重写；只做一次，不影响其它测试。
if [ -n "${FAKE_DOCKER_CORRUPT_SCRIPT_PATH:-}" ] && [ ! -e "${FAKE_DOCKER_CORRUPT_SCRIPT_PATH}.done" ]; then
  case " $* " in
    *" build "*)
      printf "#!/usr/bin/env bash\necho SELFUPDATE-CORRUPTION-MARKER\nif [ 1 -eq 1 ]; then\n  echo unterminated-on-purpose\n" > "$FAKE_DOCKER_CORRUPT_SCRIPT_PATH"
      : > "${FAKE_DOCKER_CORRUPT_SCRIPT_PATH}.done"
      ;;
  esac
fi
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

expect_failure "override-file 只允许 server-staging/server-prod" env PATH="$fake_bin:$PATH" DEPLOY_LOCAL_TRACE="$trace"   "$deploy_script" --repo "$fixture" --env-file "$fixture/deploy/compose/.env"   --compose-file "$fixture/deploy/compose/launch.yaml" --override-file "$fixture/deploy/compose/launch.yaml"   --sha "$fixture_sha" --probe-attempts 1
grep -q -- '--override-file' "$deploy_script" && ok "脚本提供 --override-file" || bad "脚本缺少 --override-file"

# 生产闸门：只有 server-prod.yaml 覆盖才允许（且要求）ENVIRONMENT=production。
cp "$repo_root/deploy/compose/server-prod.yaml" "$fixture/deploy/compose/server-prod.yaml"
printf 'ENVIRONMENT=production
POSTGRES_DB=xingmang
POSTGRES_USER=xingmang
DATABASE_PASSWORD=test-only.invalid
' > "$tmp/env.prod"
git -C "$fixture" add deploy/compose/server-prod.yaml
git -C "$fixture" commit -qm prod-override
fixture_sha="$(git -C "$fixture" rev-parse HEAD)"
expect_success "server-prod 覆盖接受 ENVIRONMENT=production" env "${common_env[@]}"   "$deploy_script" --test-mode --dry-run --repo "$fixture" --env-file "$tmp/env.prod"   --compose-file "$fixture/deploy/compose/launch.yaml" --override-file "$fixture/deploy/compose/server-prod.yaml" --sha "$fixture_sha"
expect_failure "不带 server-prod 覆盖时拒绝 ENVIRONMENT=production" env "${common_env[@]}"   "$deploy_script" --test-mode --dry-run --repo "$fixture" --env-file "$tmp/env.prod"   --compose-file "$fixture/deploy/compose/launch.yaml" --sha "$fixture_sha"
expect_failure "server-prod 覆盖要求 env-file 显式 production" env "${common_env[@]}"   "$deploy_script" --test-mode --dry-run --repo "$fixture" --env-file "$fixture/deploy/compose/.env"   --compose-file "$fixture/deploy/compose/launch.yaml" --override-file "$fixture/deploy/compose/server-prod.yaml" --sha "$fixture_sha"
grep -q 'export ENVIRONMENT="$expected_environment"' "$deploy_script" && ok "Compose 插值环境随覆盖文件" || bad "Compose 插值环境仍钉死 staging"
grep -q 'bootstrap=skipped reason=service-not-in-profile' "$deploy_script" && ok "bootstrap 按 profile 存在性跳过" || bad "bootstrap 未按 profile 跳过"

# ============================================================
# XM-DEPLOY-SELFUPDATE0：脚本自我更新期间的安全性回归测试
# ============================================================

# (a) 运行中途脚本文件被改写（模拟自身触发的 git 操作或并发 fetch 重写了
# 磁盘上的脚本），不应该让本次已经在跑的进程读到新旧混杂的字节。main()
# 包裹把整份脚本预先解析完，运行中途改写磁盘应对本次执行完全没有影响。
under_test="$tmp/deploy-local-under-test.sh"
cp "$deploy_script" "$under_test"
chmod +x "$under_test"
: > "$trace"
expect_success "运行中途脚本文件被改写仍完成本次部署" env "${common_env[@]}" \
  FAKE_DOCKER_CORRUPT_SCRIPT_PATH="$under_test" \
  "$under_test" --test-mode --repo "$fixture" --env-file "$fixture/deploy/compose/.env" \
  --compose-file "$fixture/deploy/compose/launch.yaml" --probe-attempts 1
assert_text "改写后仍输出本地部署通过" 'DEPLOY LOCAL PASS' "$tmp/stdout"
assert_text "改写后仍继续到 up" ' up -d platform-api platform-worker web' "$trace"
assert_text "改写后仍继续到探针" '/healthz' "$trace"
assert_text "改写后仍继续到烟测" '/api/v1/services' "$trace"
if [ -e "${under_test}.done" ] && grep -Fq 'SELFUPDATE-CORRUPTION-MARKER' "$under_test"; then
  ok "运行期间脚本文件确实被改写（验证测试本身有效）"
else
  bad "运行期间脚本文件确实被改写（验证测试本身有效）"
fi
if bash -n "$under_test" >/dev/null 2>&1; then
  bad "改写后的字节本身是损坏的（反证：不是因为没改坏才通过）"
else
  ok "改写后的字节本身是损坏的（反证：不是因为没改坏才通过）"
fi

# (b) fetch 成功但本地 checkout 无法 fast-forward（真正分叉，不是单纯落后）
# 时必须以文档化的退出码 3 停止，并给出精确的人工修复指令，不猜测、不强推。
# 复用 $fixture：它此刻已经领先 $remote 一个未推送的本地提交（prod-override
# 及之后的改动）；这里让 $remote 独立再推进一个不同的提交，制造真正分叉。
selfupdate_divergent_seed="$tmp/selfupdate-divergent-seed"
git clone -q "$remote" "$selfupdate_divergent_seed"
git -C "$selfupdate_divergent_seed" config user.email deploy-local-test@example.invalid
git -C "$selfupdate_divergent_seed" config user.name deploy-local-test
printf 'remote-divergent\n' > "$selfupdate_divergent_seed/remote-divergent-marker"
git -C "$selfupdate_divergent_seed" add remote-divergent-marker
git -C "$selfupdate_divergent_seed" commit -qm remote-divergent
git -C "$selfupdate_divergent_seed" push -q origin HEAD:refs/heads/release/v0.1-launch

: > "$trace"
behind_stdout="$tmp/behind.stdout"
behind_stderr="$tmp/behind.stderr"
env XM_DEPLOY_LOCAL_TEST_MODE=1 XM_DEPLOY_LOCAL_DOCKER_BIN="$fake_bin/docker" XM_DEPLOY_LOCAL_CURL_BIN="$fake_bin/curl" DEPLOY_LOCAL_TRACE="$trace" \
  "$deploy_script" --test-mode --repo "$fixture" --env-file "$fixture/deploy/compose/.env" \
  --compose-file "$fixture/deploy/compose/launch.yaml" --probe-attempts 1 \
  >"$behind_stdout" 2>"$behind_stderr"
behind_rc=$?
if [ "$behind_rc" -eq 3 ]; then
  ok "checkout 分叉时退出码是文档化的 3"
else
  bad "checkout 分叉时退出码是文档化的 3（实际 $behind_rc）"
fi
assert_text "分叉失败信息带 DEPLOY LOCAL FAIL 前缀" 'DEPLOY LOCAL FAIL' "$behind_stderr"
assert_text "分叉失败信息给出精确的手动修复指令" 'git merge --ff-only' "$behind_stderr"
if grep -Fq '/healthz' "$trace" || grep -Fq ' build ' "$trace"; then
  bad "分叉失败未继续到 build/探针"
else
  ok "分叉失败未继续到 build/探针"
fi
# 分叉状态到此测试为止；后续没有测试再复用 $fixture 的 git 历史。

# (c) self-update 成功快进、且被更新的 checkout 就是脚本自己所在的那份时，
# 脚本必须以同样的参数 exec 一次刚更新的自身，让新版本的逻辑真正生效——
# 而不是继续用旧版本已经解析在内存里的代码跑完部署。构造一个「自带
# deploy-local.sh」的独立 checkout，让 --repo 的默认值（脚本自身所在目录）
# 与被 self-update 改写的目录重合。
selfupdate_repo="$tmp/selfupdate-checkout"
mkdir -p "$selfupdate_repo/deploy/compose" "$selfupdate_repo/deploy/scripts"
cp "$repo_root/deploy/compose/launch.yaml" "$selfupdate_repo/deploy/compose/launch.yaml"
printf 'ENVIRONMENT=staging\nPOSTGRES_DB=xingmang\nPOSTGRES_USER=xingmang\nDATABASE_PASSWORD=test-only.invalid\n' > "$selfupdate_repo/deploy/compose/.env"
cp "$deploy_script" "$selfupdate_repo/deploy/scripts/deploy-local.sh"
chmod +x "$selfupdate_repo/deploy/scripts/deploy-local.sh"
git -C "$selfupdate_repo" init -q
git -C "$selfupdate_repo" config user.email test@example.invalid
git -C "$selfupdate_repo" config user.name deploy-local-test
git -C "$selfupdate_repo" add deploy
git -C "$selfupdate_repo" commit -qm seed-v1
git -C "$selfupdate_repo" branch -M release/v0.1-launch

selfupdate_remote="$tmp/selfupdate-remote.git"
git init --bare -q "$selfupdate_remote"
git -C "$selfupdate_repo" remote add origin "$selfupdate_remote"
git -C "$selfupdate_repo" push -q origin release/v0.1-launch:refs/heads/release/v0.1-launch
git --git-dir="$selfupdate_remote" symbolic-ref HEAD refs/heads/release/v0.1-launch

selfupdate_v2_seed="$tmp/selfupdate-v2-seed"
git clone -q --branch release/v0.1-launch "$selfupdate_remote" "$selfupdate_v2_seed"
git -C "$selfupdate_v2_seed" config user.email deploy-local-test@example.invalid
git -C "$selfupdate_v2_seed" config user.name deploy-local-test
sed -i '/^main() {$/a echo SELFUPDATE-TEST-V2-MARKER' "$selfupdate_v2_seed/deploy/scripts/deploy-local.sh"
if grep -Fq 'SELFUPDATE-TEST-V2-MARKER' "$selfupdate_v2_seed/deploy/scripts/deploy-local.sh"; then
  ok "re-exec 测试的 v2 脚本已注入可观测标记"
else
  bad "re-exec 测试的 v2 脚本已注入可观测标记"
fi
git -C "$selfupdate_v2_seed" add deploy/scripts/deploy-local.sh
git -C "$selfupdate_v2_seed" commit -qm v2-marker
git -C "$selfupdate_v2_seed" push -q origin HEAD:refs/heads/release/v0.1-launch

: > "$trace"
expect_success "self-update 成功后 re-exec 到刚更新的脚本" env XM_DEPLOY_LOCAL_TEST_MODE=1 XM_DEPLOY_LOCAL_DOCKER_BIN="$fake_bin/docker" XM_DEPLOY_LOCAL_CURL_BIN="$fake_bin/curl" DEPLOY_LOCAL_TRACE="$trace" \
  "$selfupdate_repo/deploy/scripts/deploy-local.sh" --test-mode --probe-attempts 1
assert_text "输出记录 self-update 已应用" 'self-update=applied' "$tmp/stdout"
assert_text "re-exec 后的新版本脚本真的执行了" 'SELFUPDATE-TEST-V2-MARKER' "$tmp/stdout"
assert_text "re-exec 后仍完成部署" 'DEPLOY LOCAL PASS' "$tmp/stdout"
build_calls="$(grep -c ' build ' "$trace" || true)"
if [ "$build_calls" = "1" ]; then
  ok "re-exec 未导致部署步骤重复执行"
else
  bad "re-exec 未导致部署步骤重复执行（build 出现 ${build_calls} 次）"
fi
if grep -Fq 'SELFUPDATE-TEST-V2-MARKER' "$selfupdate_repo/deploy/scripts/deploy-local.sh"; then
  ok "checkout 磁盘上的脚本已快进到 v2"
else
  bad "checkout 磁盘上的脚本已快进到 v2"
fi

[ "$fail" -eq 0 ] && echo "DEPLOY-LOCAL-TEST-OK"
exit "$fail"
