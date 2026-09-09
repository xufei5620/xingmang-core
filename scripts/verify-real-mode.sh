#!/usr/bin/env bash
# XM-REAL0-d：本机真实模式切换后的只读验证清单。
# 只读取本地栈的探针、指标和 Worker 日志；不会 source .env、打印凭据或请求真实上游。
set -Eeuo pipefail
umask 077

test_gate_value="${XM_VERIFY_REAL_MODE_TEST_MODE:-0}"
docker_override="${XM_VERIFY_REAL_MODE_DOCKER_BIN:-}"
curl_override="${XM_VERIFY_REAL_MODE_CURL_BIN:-}"
python_override="${XM_VERIFY_REAL_MODE_PYTHON_BIN:-}"
base_override="${XM_VERIFY_REAL_MODE_BASE_URL:-}"
worker_override="${XM_VERIFY_REAL_MODE_WORKER_SERVICE:-}"
project=xingmang-launch
unset DOCKER_HOST DOCKER_CONTEXT DOCKER_CONFIG COMPOSE_PROJECT_NAME COMPOSE_FILE COMPOSE_PROFILES COMPOSE_ENV_FILES \
  GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_CONFIG GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_SSH_COMMAND GIT_ASKPASS \
  BASH_ENV ENV LD_PRELOAD LD_LIBRARY_PATH DYLD_INSERT_LIBRARIES NODE_OPTIONS PYTHONPATH RUBYOPT PERL5OPT CDPATH

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
default_repo="$(cd -- "$script_dir/.." && pwd -P)"
repo_path="$default_repo"
compose_file=
env_file=
base_url=http://127.0.0.1:8088
worker_service=platform-worker
mode=real
platforms=all
probe_attempts=12
test_mode=0

usage() {
  cat <<'USAGE'
用法: scripts/verify-real-mode.sh [选项]
  --repo PATH                 Git checkout
  --compose-file PATH        launch.yaml
  --env-file PATH             .env
  --mode staging|real         期望 Worker 模式（默认 real）
  --platform LIST             sub2api,newapi 或 all
  --base-url URL              loopback Web 地址（默认 8088）
  --worker-service NAME       Worker 服务名（默认 platform-worker）
  --probe-attempts N          探针重试次数 1..30
  --test-mode                 仅允许 XM_VERIFY_REAL_MODE_TEST_MODE=1
USAGE
}

die() { echo "VERIFY REAL MODE FAIL: $*" >&2; exit 1; }

normalize_path() {
  local value="$1"
  if command -v cygpath >/dev/null 2>&1 && [[ "$value" =~ ^[A-Za-z]:[\\/].* ]]; then
    value="$(cygpath -u -- "$value")"
  fi
  printf '%s' "$value"
}

validate_path() {
  local label="$1" value="$2"
  [ -n "$value" ] || die "$label 不能为空"
  case "$value" in /*|[A-Za-z]:[\\/]*) ;; *) die "$label 必须是绝对路径" ;; esac
  case "/$value/" in */../*|*/./*) die "$label 不得包含 . 或 .. 路径段" ;; esac
}

resolve_binary() {
  local label="$1" value="$2" resolved
  if [[ "$value" == */* ]]; then
    value="$(normalize_path "$value")"
    validate_path "$label" "$value"
    [ -x "$value" ] && [ ! -L "$value" ] || die "$label 不可执行"
    printf '%s' "$value"; return
  fi
  resolved="$(type -P -- "$value" 2>/dev/null || true)"
  [ -n "$resolved" ] && [ -x "$resolved" ] || die "找不到 $label: $value"
  printf '%s' "$resolved"
}

read_env_value() {
  local key="$1" line lhs rhs
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in [[:space:]]*|""|\#*) continue ;; esac
    lhs="${line%%=*}"
    [ "$lhs" = "$key" ] || continue
    rhs="${line#*=}"
    rhs="${rhs%$'\r'}"
    case "$rhs" in
      \"*\") rhs="${rhs:1:${#rhs}-2}" ;;
      \\'*\\') rhs="${rhs:1:${#rhs}-2}" ;;
    esac
    printf '%s' "$rhs"; return 0
  done < "$env_file"
  return 1
}

parse_uint() {
  case "$1" in ''|*[!0-9]*) return 1 ;; esac
  [ "$1" -ge 1 ] && [ "$1" -le 30 ]
}

parse_platforms() {
  local raw="$1" part
  [ -n "$raw" ] || die "platform 不能为空"
  [ "$raw" = all ] && { printf 'sub2api newapi'; return; }
  local -a out=()
  IFS=',' read -r -a parts <<< "$raw"
  for part in "${parts[@]}"; do
    part="${part//[[:space:]]/}"
    case "$part" in sub2api|newapi) ;; *) die "不支持的平台: $part" ;; esac
    case " ${out[*]} " in *" $part "*) ;; *) out+=("$part") ;; esac
  done
  printf '%s' "${out[*]}"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --repo) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; repo_path="$2"; shift 2 ;;
    --repo=*) repo_path="${1#*=}"; shift ;;
    --compose-file) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; compose_file="$2"; shift 2 ;;
    --compose-file=*) compose_file="${1#*=}"; shift ;;
    --env-file) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; env_file="$2"; shift 2 ;;
    --env-file=*) env_file="${1#*=}"; shift ;;
    --mode) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; mode="$2"; shift 2 ;;
    --mode=*) mode="${1#*=}"; shift ;;
    --platform) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; platforms="$2"; shift 2 ;;
    --platform=*) platforms="${1#*=}"; shift ;;
    --base-url) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; base_url="$2"; shift 2 ;;
    --base-url=*) base_url="${1#*=}"; shift ;;
    --worker-service) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; worker_service="$2"; shift 2 ;;
    --worker-service=*) worker_service="${1#*=}"; shift ;;
    --probe-attempts) [ "$#" -ge 2 ] || { usage >&2; exit 2; }; probe_attempts="$2"; shift 2 ;;
    --probe-attempts=*) probe_attempts="${1#*=}"; shift ;;
    --test-mode) test_mode=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die "未知参数: $1" ;;
  esac
done

if [ "$test_mode" -eq 1 ]; then
  [ "$test_gate_value" = 1 ] || die "--test-mode 需要 XM_VERIFY_REAL_MODE_TEST_MODE=1"
  docker_value="${docker_override:-docker}"
  curl_value="${curl_override:-curl}"
  python_value="${python_override:-python3}"
  [ -n "$base_override" ] && base_url="$base_override" || true
  [ -n "$worker_override" ] && worker_service="$worker_override" || true
else
  docker_value=docker; curl_value=curl; python_value=python3
fi
case "$mode" in staging|real) ;; *) die "mode 只能是 staging 或 real" ;; esac
parse_uint "$probe_attempts" || die "probe-attempts 必须是 1..30"
platform_list="$(parse_platforms "$platforms")"

repo_path="$(normalize_path "$repo_path")"
validate_path repo "$repo_path"
[ -d "$repo_path" ] && [ ! -L "$repo_path" ] || die "repo 不存在或是符号链接"
repo_path="$(cd -- "$repo_path" && pwd -P)"
compose_file="${compose_file:-$repo_path/deploy/compose/launch.yaml}"
env_file="${env_file:-$repo_path/deploy/compose/.env}"
compose_file="$(normalize_path "$compose_file")"
env_file="$(normalize_path "$env_file")"
validate_path compose-file "$compose_file"; validate_path env-file "$env_file"
[ -f "$compose_file" ] && [ ! -L "$compose_file" ] || die "compose-file 不存在或是符号链接"
[ -f "$env_file" ] && [ ! -L "$env_file" ] || die "env-file 不存在或是符号链接"
environment="$(read_env_value ENVIRONMENT 2>/dev/null || true)"
environment="${environment//[[:space:]]/}"
[ "$environment" = staging ] || [ "$environment" = production ] || die "ENVIRONMENT 必须是 staging 或 production"
base_url="${base_url%/}"
[[ "$base_url" =~ ^https?://(127\.0\.0\.1|localhost):[0-9]{1,5}$ ]] || die "base-url 只允许 loopback"
docker_bin="$(resolve_binary docker "$docker_value")"
curl_bin="$(resolve_binary curl "$curl_value")"
python_bin="$(resolve_binary python "$python_value")"
git_root="$(git -C "$repo_path" rev-parse --show-toplevel 2>/dev/null || true)"
[ -n "$git_root" ] || die "repo 不是 Git checkout"
git_root="$(cd -- "$git_root" && pwd -P)"
[ "$git_root" = "$repo_path" ] || die "Git checkout 根目录无法验证"

tmp="$(mktemp -d)"
trap 'rm -rf -- "$tmp"' EXIT
compose() {
  ( cd -- "$repo_path"; COMPOSE_FILE="$compose_file" COMPOSE_PROJECT_NAME="$project" COMPOSE_ENV_FILES="$env_file" "$docker_bin" compose "$@" )
}
started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
worker_id="$(compose ps -q --status running "$worker_service" 2>"$tmp/docker.err" || true)"
[ -n "$worker_id" ] || die "Worker 未运行"
# 以容器真正的 StartedAt 作为日志边界，避免脚本启动前已经产生的旧日志
# 被误认为本轮切换证据。测试替身没有 inspect 时回落到脚本时间边界。
worker_started_at="$("$docker_bin" inspect -f '{{.State.StartedAt}}' "$worker_id" 2>"$tmp/docker-inspect.err" || true)"
if ! [[ "$worker_started_at" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2} ]]; then
  worker_started_at="$started_at"
fi

curl_common=(--noproxy '*' -sS --connect-timeout 5 --max-time 15)
if [ "$environment" != production ] || [ "$test_mode" -eq 1 ]; then
  # connector.manage 是 GET /api/v1/connectors/config 的读权限（XM-CRED0：
  # 与写 Action connector.config.set@1 同一个 scope，见 router.go 的注释——
  # 能看出「哪条通道还是 fake」本身就是敏感信息，不下放到 ops.read）。
  curl_common+=(-H 'X-Dev-Principal-ID: dev-operator' -H 'X-Dev-Principal-Type: HUMAN' -H 'X-Dev-Scopes: ops.read,registry.read,finance.read,platform.users.read,connector.manage')
fi
probe() {
  local name="$1" path="$2" out="$tmp/$1.json" code attempt
  for ((attempt=1; attempt<=probe_attempts; attempt++)); do
    code="$("$curl_bin" "${curl_common[@]}" -o "$out" -w '%{http_code}' "$base_url$path" 2>"$tmp/curl.err" || true)"
    [ "$code" = 200 ] && { printf '%s' "$code"; return; }
    [ "$attempt" -lt "$probe_attempts" ] && sleep 1
  done
  die "$name probe failed (status=$code)"
}
healthz="$(probe healthz /healthz)"
readyz="$(probe readyz /readyz)"
services="$(probe services /api/v1/services)"
metrics="$(probe metrics /api/v1/metrics)"
alerts="$(probe alerts /api/v1/alerts)"
# core.connector_config 是 XM-CRED0 的接入模式来源，worker 每轮动态读取,
# 切换不重启容器——所以它是判定「平台当前实际在哪个模式」唯一权威的地方。
# 旧版本本脚本只信 Worker 启动日志里的 {platform}_mode 字段（进程起来那一刻
# 的快照），后台切换模式后不重启容器就检测不出来。
connectors_config="$(probe connectors_config /api/v1/connectors/config)"
compose logs --no-color --since "$worker_started_at" "$worker_service" >"$tmp/worker.log" 2>"$tmp/docker-logs.err" || die "无法读取 Worker 日志"
[ -s "$tmp/worker.log" ] || die "Worker 日志为空"

VERIFY_ENVIRONMENT="$environment" VERIFY_MODE="$mode" VERIFY_PLATFORMS="$platform_list" \
  "$python_bin" - "$tmp/metrics.json" "$tmp/worker.log" "$tmp/connectors_config.json" <<'PY'
import json, os, sys
metrics_path, worker_path, connectors_config_path = sys.argv[1:4]
environment = os.environ["VERIFY_ENVIRONMENT"]
requested_mode = os.environ["VERIFY_MODE"]
platforms = os.environ["VERIFY_PLATFORMS"].split()
worker_mode = "fake" if requested_mode == "staging" else "real"

def fail(message):
    print(message, file=sys.stderr)
    raise SystemExit(1)
try:
    with open(metrics_path, encoding="utf-8") as stream:
        metric_doc = json.load(stream)
    with open(worker_path, encoding="utf-8") as stream:
        worker_lines = []
        for raw in stream:
            line = raw.strip()
            # docker compose logs normally prefixes each line with
            # `service-n | `; the test fixture contains bare JSON lines.
            if " | " in line:
                line = line.split(" | ", 1)[1].strip()
            if not line.startswith("{"):
                continue
            try:
                worker_lines.append(json.loads(line))
            except json.JSONDecodeError:
                continue
    if not worker_lines:
        fail("Worker 日志中没有可解析事件")
except (OSError, json.JSONDecodeError):
    fail("无法解析本轮验证数据")
# worker_started 只回答「这条采集链路开没开、来源标识是什么」。
#
# 它**不再**参与模式判定（XM-OPS-TRUTH）：那一行里的 {platform}_mode_default
# 是环境变量给的缺省，后台热切换模式不重启容器它永远不变，与 --mode 期望
# 什么本来就没关系。生产 env 缺省恰好一直是 fake，所以旧版这条判定在
# --mode real 时本该一直红——它没红，只是因为没人在生产上跑过。
starts = {}
for row in worker_lines:
    if row.get("event") != "worker_started" or row.get("environment") != environment:
        continue
    for platform in platforms:
        source = row.get(f"{platform}_source")
        if row.get(f"{platform}_sync_enabled") is True and source:
            starts[platform] = source
missing_starts = [p for p in platforms if p not in starts]
if missing_starts:
    fail("worker sync disabled or source missing: " + ",".join(missing_starts))

# 生效模式的判定改到这里：connector_config_applied 是 worker **每轮**用
# jobs.ResolveEffectiveMode 解析出来、并且真的拿去建客户端的那一份配置
# （ACCEPTANCE-LOG :82/:86 记的「需改为读 connector_config_applied」）。
applied = {}
for row in worker_lines:
    if row.get("event") != "connector_config_applied" or row.get("environment") != environment:
        continue
    platform = row.get("platform")
    if platform in platforms:
        applied[platform] = row
missing_applied = [p for p in platforms if p not in applied]
if missing_applied:
    fail("missing connector_config_applied: " + ",".join(missing_applied))
applied_mismatch = []
for platform in platforms:
    row = applied[platform]
    if row.get("mode") != worker_mode:
        applied_mismatch.append(f"{platform}={row.get('mode')}")
    elif requested_mode == "real" and row.get("config_source") != "database":
        # real 必须来自后台那张表，不能是「env 缺省恰好也是 real」。
        applied_mismatch.append(f"{platform}=source:{row.get('config_source')}")
if applied_mismatch:
    fail(f"effective mode mismatch (expected {worker_mode}): " + ",".join(applied_mismatch))

# core.connector_config 校验：这是本轮真正生效的模式来源（XM-CRED0，worker
# 每轮读取、切换不重启），与上面 worker 启动日志的快照是两件事——一次热切换
# 之后启动日志不会变，只有这张表会变。GET /connectors/config 只返回**存在**
# 的行；没有行的平台按 credentials.Store.ListConnectorConfigs 的既定口径
# 视为 fake（"没在后台配过"是正常状态，不是错误）。
try:
    with open(connectors_config_path, encoding="utf-8") as stream:
        connector_config_doc = json.load(stream)
except (OSError, json.JSONDecodeError):
    fail("connector config response invalid")
connector_config_items = connector_config_doc.get("items")
if not isinstance(connector_config_items, list):
    fail("connector config response invalid")
db_mode_by_platform = {}
for row in connector_config_items:
    if not isinstance(row, dict):
        continue
    platform, row_mode = row.get("platform"), row.get("mode")
    if platform in ("sub2api", "newapi") and row_mode in ("fake", "real"):
        db_mode_by_platform[platform] = row_mode
db_mode_mismatch = []
for platform in platforms:
    # 没有行时**不猜**（XM-OPS-TRUTH）：旧版这里写 .get(platform, "fake")，
    # 是「没有行就是 fake」那套猜法的第四份拷贝。它只在「worker 的 env 缺省
    # 恰好也是 fake」时答对，靠的是别处的事实。生效模式已经由上面的
    # connector_config_applied 判定过了，这里只校验**存在的**行。
    actual = db_mode_by_platform.get(platform)
    if actual is not None and actual != worker_mode:
        db_mode_mismatch.append(f"{platform}={actual}")
if db_mode_mismatch:
    fail(f"core.connector_config mode mismatch (expected {worker_mode}): " + ",".join(db_mode_mismatch))
connector_config_summary = ",".join(f"{p}:{db_mode_by_platform.get(p, 'none')}" for p in platforms)

if requested_mode == "real":
    demo = [p for p, source in starts.items() if source.endswith("-staging")]
    if demo:
        fail("demo source: " + ",".join(demo))
for platform in platforms:
    jobs = [r for r in worker_lines if r.get("event") == "job_completed" and r.get("job_kind") == f"{platform}_sync" and r.get("environment") == environment]
    if not jobs:
        fail(f"missing worker job: {platform}_sync")
    latest = jobs[-1]
    if latest.get("success") is not True or latest.get("metrics_failed") != 0:
        fail(f"worker {platform}_sync metrics_failed={latest.get('metrics_failed')}")
    if latest.get("source") != starts[platform]:
        fail(f"worker source mismatch: {platform}")
metric_field = "metric_" + "key"
required_suffixes = {
    "sub2api": [".users.total", ".users.balance", ".revenue.daily", ".cost.daily", ".channels.balance", ".channels.status"],
    "newapi": [".users.total", ".recharge.daily", ".subscription.daily", ".channels.status", ".models.usage"],
}
items = metric_doc.get("items")
if not isinstance(items, list):
    fail("metrics response invalid")
by_key = {item.get(metric_field): item for item in items if isinstance(item, dict)}
for platform in platforms:
    required = [platform + suffix for suffix in required_suffixes[platform]]
    missing = [key for key in required if key not in by_key]
    if missing:
        fail("missing metric keys: " + ",".join(missing))
    for key in required:
        item = by_key[key]
        if item.get("environment") != environment or item.get("source") != starts[platform]:
            if requested_mode == "real":
                fail(f"demo source or metric source/environment mismatch: {key}")
            fail(f"metric source/environment mismatch: {key}")
        freshness = item.get("freshness")
        if not item.get("watermark") or not isinstance(freshness, dict):
            fail(f"metric freshness missing: {key}")
        state, partial = freshness.get("state"), freshness.get("is_partial")
        if state not in ("fresh","partial"):
            fail(f"metric freshness state invalid: {key}")
        if (state == "partial") != (partial is True):
            fail(f"partial freshness mismatch: {key}")
        if not freshness.get("observed_at"):
            fail(f"metric observed_at missing: {key}")
        if freshness.get("last_error_code") not in ("", None):
            fail(f"metric last_error_code present: {key}")
print(f"verified_platforms={','.join(platforms)} metrics_checked={sum(len(required_suffixes[p]) for p in platforms)} connector_config_mode={connector_config_summary}")
PY

# connector_config_mode 是本轮从 core.connector_config 读到、并已通过上面
# db_mode_mismatch 校验的逐平台真实模式；Python 已经把它打在上面那一行,
# 这里不重复解析同一份 JSON，只是把这句话说清楚：**这一行不是回显 --mode
# 参数**，是脚本独立核实过的结论。
echo "VERIFY REAL MODE PASS: environment=$environment mode=$mode healthz=$healthz readyz=$readyz smoke=services:$services,metrics:$metrics,alerts:$alerts,connectors_config:$connectors_config finance.rows_written=deferred worker=running"
