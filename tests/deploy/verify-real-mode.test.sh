#!/usr/bin/env bash
# scripts/verify-real-mode.sh 的离线契约测试。
#
# 这里使用显式 test-mode 的 fake curl/docker 和脱敏 fixture；不会触碰本机
# xingmang-launch 栈，也不会读取真实 .env。真实栈证据由 XM-REAL0-d Handoff
# 在凭据到位后补记。
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd -P)"
verify_script="$repo_root/scripts/verify-real-mode.sh"
fixture_source="$repo_root/tests/fixtures/real-mode"
tmp="$(mktemp -d)"
fixture_root="$tmp/fixtures"
mkdir -p "$fixture_root"
cp "$fixture_source"/worker-*.log "$fixture_root/"
trap 'rm -rf -- "$tmp"' EXIT
fail=0

# 指标样本在测试运行时生成，不把 `metric_key":"<名字>` 形状提交到仓库：
# 这样 gitleaks 的 generic-api-key 规则不会把脱敏指标名误判成凭据，同时
# 仍然让脚本验证与真实 /api/v1/metrics 相同的 JSON 字段。
VERIFY_FIXTURE_ROOT="$fixture_root" "${PYTHON_BIN:-python3}" - <<'PY'
import json
import os
from pathlib import Path

root = Path(os.environ["VERIFY_FIXTURE_ROOT"])
root.mkdir(parents=True, exist_ok=True)
field = "metric_" + "key"
suffixes = {
    "sub2api": ["users.total", "users.balance", "revenue.daily", "cost.daily", "channels.balance", "channels.status"],
    "newapi": ["users.total", "recharge.daily", "subscription.daily", "channels.status", "models.usage"],
}

def document(source, partial=False, omit=None, inconsistent=False):
    items = []
    for platform, names in suffixes.items():
        for suffix in names:
            name = platform + "." + suffix
            if name == omit:
                continue
            is_partial = partial and name.endswith("subscription.daily")
            state = "partial" if is_partial else "fresh"
            if inconsistent and name == "sub2api.users.total":
                is_partial, state = True, "fresh"
            item_source = source[platform] if isinstance(source, dict) else source
            items.append({field: name, "source": item_source, "environment": "staging",
                          "watermark": "wm-" + name.replace(".", "-"), "value": {},
                          "freshness": {"state": state, "is_partial": is_partial,
                                        "observed_at": "2026-08-30T00:00:00Z",
                                        "last_success": "2026-08-30T00:00:00Z",
                                        "last_error_code": ""}})
    return {"items": items}

for filename, body in {
    "staging-metrics.json": document({"sub2api": "sub2api-staging", "newapi": "newapi-staging"}),
    "real-metrics.json": document({"sub2api": "sub2api-prod", "newapi": "newapi-prod"}, partial=True),
    "missing-metric.json": document("sub2api-staging", omit="sub2api.users.balance"),
    "inconsistent-partial.json": document("sub2api-staging", inconsistent=True),
}.items():
    (root / filename).write_text(json.dumps(body, separators=(",", ":")) + "\n", encoding="utf-8")

# core.connector_config 的 GET /connectors/config 脱敏 fixture（XM-OPS-TAILS0）：
# 形状对齐 internal/platform/httpapi/credentials.go 的 connectorConfigItem。
# 「没有这一行」用省略该平台条目表示，与 credentials.Store.ListConnectorConfigs
# 「没有行的平台不在结果里」的真实行为一致，而不是发明一个第三态。
def connector_config_document(modes):
    items = []
    for platform, mode in modes.items():
        items.append({
            "platform": platform, "mode": mode,
            "endpoint": "https://upstream.invalid" if mode == "real" else "",
            "target_allowlist": ["upstream.invalid"] if mode == "real" else [],
            "credential_ref": f"secret://{platform}/token" if mode == "real" else "",
            "version": 1, "updated_at": "2026-08-30T00:00:00Z", "updated_by": "fixture",
        })
    return {"items": items}

for filename, modes in {
    "connector-config-staging.json": {"sub2api": "fake", "newapi": "fake"},
    "connector-config-real.json": {"sub2api": "real", "newapi": "real"},
    "connector-config-unconfigured.json": {},
    "connector-config-stale-real.json": {"sub2api": "fake", "newapi": "real"},
}.items():
    (root / filename).write_text(
        json.dumps(connector_config_document(modes), separators=(",", ":")) + "\n", encoding="utf-8")
PY

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

if [ ! -x "$verify_script" ]; then
  bad "verify-real-mode.sh 存在且可执行"
  printf 'VERIFY-REAL-MODE-TEST-FAILED\n'
  exit "$fail"
fi
ok "verify-real-mode.sh 存在且可执行"
if bash -n "$verify_script"; then ok "verify-real-mode.sh shell 语法"; else bad "verify-real-mode.sh shell 语法"; fi

# 先用最小 fixture checkout 验证 test-mode 的路径边界；生产模式不允许这些
# 替换变量绕过固定的仓库 / Compose 配置。
fixture_repo="$tmp/xingmang-platform"
mkdir -p "$fixture_repo/deploy/compose"
cp "$repo_root/deploy/compose/launch.yaml" "$fixture_repo/deploy/compose/launch.yaml"
printf 'ENVIRONMENT=staging\nPOSTGRES_DB=xingmang\nPOSTGRES_USER=xingmang\nDATABASE_PASSWORD=test-only.invalid\n' > "$fixture_repo/deploy/compose/.env"
git -C "$fixture_repo" init -q
git -C "$fixture_repo" config user.email verify-real-mode@example.invalid
git -C "$fixture_repo" config user.name verify-real-mode-test
git -C "$fixture_repo" add deploy/compose
git -C "$fixture_repo" commit -qm seed
git -C "$fixture_repo" branch -M release/v0.1-launch

fake_bin="$tmp/bin"
mkdir -p "$fake_bin"

cat > "$fake_bin/curl" <<'EOF'
#!/usr/bin/env bash
set -u
out=""
url=""
prev=""
for arg in "$@"; do
  [ "$prev" = "-o" ] && out="$arg"
  case "$arg" in http://*|https://*) url="$arg" ;; esac
  prev="$arg"
done
if [ "${VERIFY_FAKE_CURL_FAIL:-0}" = "1" ]; then
  [ -n "$out" ] && printf '%s\n' '{"error":{"code":"fixture_unavailable"}}' > "$out"
  printf '503'
  exit 0
fi
body='{"items":[]}'
case "$url" in
  */healthz) body='{"status":"ok"}' ;;
  */readyz) body='{"status":"ready"}' ;;
  */api/v1/services) body='{"items":[{"service_type":"sub2api","instance_id":"sub2api-staging","environment":"staging","status":"active"}]}' ;;
  */api/v1/metrics) body="$(cat "${VERIFY_METRICS_FIXTURE:?}")" ;;
  */api/v1/alerts) body='{"items":[]}' ;;
  */api/v1/connectors/config) body="$(cat "${VERIFY_CONNECTOR_CONFIG_FIXTURE:?}")" ;;
esac
[ -n "$out" ] && printf '%s\n' "$body" > "$out" || printf '%s\n' "$body"
printf '200'
exit 0
EOF
chmod +x "$fake_bin/curl"

cat > "$fake_bin/docker" <<'EOF'
#!/usr/bin/env bash
set -u
case " $* " in
  *" context show "*) printf 'default\n'; exit 0 ;;
  *" context inspect "*) printf 'npipe:////./pipe/docker_engine\n'; exit 0 ;;
  *" compose version "*) printf 'Docker Compose version v5.3.1\n'; exit 0 ;;
  *" version "*) printf '29.7.2\n'; exit 0 ;;
  *" compose logs "*) cat "${VERIFY_WORKER_LOG_FIXTURE:?}"; exit 0 ;;
  *" compose ps "*) printf 'worker-container\n'; exit 0 ;;
esac
exit 0
EOF
chmod +x "$fake_bin/docker"

common=(
  "XM_VERIFY_REAL_MODE_TEST_MODE=1"
  "XM_VERIFY_REAL_MODE_DOCKER_BIN=$fake_bin/docker"
  "XM_VERIFY_REAL_MODE_CURL_BIN=$fake_bin/curl"
  "XM_VERIFY_REAL_MODE_PYTHON_BIN=${PYTHON_BIN:-python3}"
  "VERIFY_WORKER_LOG_FIXTURE=$fixture_root/worker-staging.log"
  "VERIFY_METRICS_FIXTURE=$fixture_root/staging-metrics.json"
  "VERIFY_CONNECTOR_CONFIG_FIXTURE=$fixture_root/connector-config-staging.json"
)

expect_success "staging 正向 fixture 通过三 smoke、指标和 worker 日志" \
  env "${common[@]}" \
  "$verify_script" --test-mode --repo "$fixture_repo" \
  --compose-file "$fixture_repo/deploy/compose/launch.yaml" \
  --env-file "$fixture_repo/deploy/compose/.env" --mode staging --platform all \
  --probe-attempts 1
assert_text "正向输出含 healthz" 'healthz=200' "$tmp/stdout"
assert_text "正向输出含三 smoke" 'smoke=services:200,metrics:200,alerts:200' "$tmp/stdout"
assert_text "正向输出声明 finance rows deferred" 'finance.rows_written=deferred' "$tmp/stdout"
assert_text "正向输出声明 connector_config 逐平台模式" 'connector_config_mode=sub2api:fake,newapi:fake' "$tmp/stdout"
assert_not_text "正向输出不回显 fixture token" 'fixture-token-SHOULD-NOT-ECHO' "$tmp/stdout"
assert_not_text "正向错误输出不回显 fixture token" 'fixture-token-SHOULD-NOT-ECHO' "$tmp/stderr"

expect_success "real 正向 fixture 接受非演示来源与合法 partial" \
  env "${common[@]}" VERIFY_WORKER_LOG_FIXTURE="$fixture_root/worker-real.log" VERIFY_METRICS_FIXTURE="$fixture_root/real-metrics.json" \
  VERIFY_CONNECTOR_CONFIG_FIXTURE="$fixture_root/connector-config-real.json" \
  "$verify_script" --test-mode --repo "$fixture_repo" \
  --compose-file "$fixture_repo/deploy/compose/launch.yaml" \
  --env-file "$fixture_repo/deploy/compose/.env" --mode real --platform sub2api,newapi \
  --probe-attempts 1
assert_text "real 输出标记 real" 'mode=real' "$tmp/stdout"
assert_text "real 输出声明 connector_config 逐平台模式" 'connector_config_mode=sub2api:real,newapi:real' "$tmp/stdout"

expect_failure "real 拒绝演示 source" \
  env "${common[@]}" VERIFY_WORKER_LOG_FIXTURE="$fixture_root/worker-real.log" \
  VERIFY_CONNECTOR_CONFIG_FIXTURE="$fixture_root/connector-config-real.json" \
  "$verify_script" --test-mode --repo "$fixture_repo" \
  --compose-file "$fixture_repo/deploy/compose/launch.yaml" \
  --env-file "$fixture_repo/deploy/compose/.env" --mode real --platform sub2api \
  --probe-attempts 1
assert_text "演示 source 失败原因不泄漏响应正文" 'demo source' "$tmp/stderr"

expect_failure "core.connector_config 模式与请求模式不一致时被拒（热切换未被察觉）" \
  env "${common[@]}" VERIFY_WORKER_LOG_FIXTURE="$fixture_root/worker-real.log" VERIFY_METRICS_FIXTURE="$fixture_root/real-metrics.json" \
  VERIFY_CONNECTOR_CONFIG_FIXTURE="$fixture_root/connector-config-stale-real.json" \
  "$verify_script" --test-mode --repo "$fixture_repo" \
  --compose-file "$fixture_repo/deploy/compose/launch.yaml" \
  --env-file "$fixture_repo/deploy/compose/.env" --mode real --platform sub2api,newapi \
  --probe-attempts 1
assert_text "connector_config 不一致失败明确指出 mismatch" 'core.connector_config mode mismatch' "$tmp/stderr"
assert_text "connector_config 不一致失败点名具体平台" 'sub2api=fake' "$tmp/stderr"

expect_success "core.connector_config 里没有该平台的行按 fake 处理（未配置=正常状态）" \
  env "${common[@]}" VERIFY_CONNECTOR_CONFIG_FIXTURE="$fixture_root/connector-config-unconfigured.json" \
  "$verify_script" --test-mode --repo "$fixture_repo" \
  --compose-file "$fixture_repo/deploy/compose/launch.yaml" \
  --env-file "$fixture_repo/deploy/compose/.env" --mode staging --platform sub2api \
  --probe-attempts 1
assert_text "未配置平台按 fake 汇报" 'connector_config_mode=sub2api:fake' "$tmp/stdout"

expect_failure "缺 key 的 metrics fixture 被拒" \
  env "${common[@]}" VERIFY_METRICS_FIXTURE="$fixture_root/missing-metric.json" \
  "$verify_script" --test-mode --repo "$fixture_repo" \
  --compose-file "$fixture_repo/deploy/compose/launch.yaml" \
  --env-file "$fixture_repo/deploy/compose/.env" --mode staging --platform sub2api \
  --probe-attempts 1
assert_text "缺 key 失败明确指出 missing" 'missing metric keys' "$tmp/stderr"

expect_failure "partial 与 freshness state 不一致时被拒" \
  env "${common[@]}" VERIFY_METRICS_FIXTURE="$fixture_root/inconsistent-partial.json" \
  "$verify_script" --test-mode --repo "$fixture_repo" \
  --compose-file "$fixture_repo/deploy/compose/launch.yaml" \
  --env-file "$fixture_repo/deploy/compose/.env" --mode staging --platform sub2api \
  --probe-attempts 1
assert_text "partial 失败明确指出 freshness" 'partial' "$tmp/stderr"

expect_failure "worker metrics_failed 非零时被拒" \
  env "${common[@]}" VERIFY_WORKER_LOG_FIXTURE="$fixture_root/worker-failed.log" \
  "$verify_script" --test-mode --repo "$fixture_repo" \
  --compose-file "$fixture_repo/deploy/compose/launch.yaml" \
  --env-file "$fixture_repo/deploy/compose/.env" --mode staging --platform sub2api \
  --probe-attempts 1
assert_text "worker 失败明确指出 metrics_failed" 'metrics_failed' "$tmp/stderr"

expect_failure "HTTP 探针失败时不继续指标校验" \
  env "${common[@]}" VERIFY_FAKE_CURL_FAIL=1 \
  "$verify_script" --test-mode --repo "$fixture_repo" \
  --compose-file "$fixture_repo/deploy/compose/launch.yaml" \
  --env-file "$fixture_repo/deploy/compose/.env" --mode staging --platform sub2api \
  --probe-attempts 1
if grep -Fq '/api/v1/metrics' "$tmp/stderr"; then
  bad "探针失败后未暴露响应正文"
else
  ok "探针失败后未暴露响应正文"
fi

printf 'VERIFY-REAL-MODE-TEST-%s\n' "$([ "$fail" -eq 0 ] && printf OK || printf FAILED)"
exit "$fail"
