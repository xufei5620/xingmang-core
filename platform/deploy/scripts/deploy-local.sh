#!/usr/bin/env bash
# 星芒统一控制平台本地 staging 部署入口。
#
# 本脚本只管理本机的 xingmang-launch Compose 项目：
#   fetch/校验 release → config → build → postgres+migrate
#   → runway threshold bootstrap → 演示 bootstrap → API/worker/web
#   → /healthz /readyz → services / metrics / alerts 烟测
#
# 它与服务器上的 deploy.sh 有意分开：本地没有生产晋级、服务器 bare repo
# 或生产审计闸门，但仍然拒绝跨项目、跨环境和脏工作树操作。
#
# 本脚本的烟测只确认三个端点能拿到 200——不核实 Sub2API/NewAPI 各自
# 处于 real 还是 fake（那需要读 core.connector_config，与这里的
# dev-header 冒烟身份是不同的鉴权面，见 scripts/verify-real-mode.sh 顶部
# 注释）。部署完成后如果要确认「哪个平台现在真的在用真实上游」，另外手动跑：
#   scripts/verify-real-mode.sh --mode real --platform sub2api,newapi
# 该脚本会额外校验 GET /api/v1/connectors/config 与 Worker 日志两者是否
# 与期望模式一致（XM-OPS-TAILS0）。
#
# 自我更新安全性（XM-DEPLOY-SELFUPDATE0）：本脚本会 fetch/checkout/
# fast-forward 自己所在的 checkout（见下方 git 段落），这意味着运行中
# 脚本自身的磁盘字节可能被自己触发的 git 操作改写。为避免 bash 在脚本
# 执行到一半时按旧文件偏移量读到新文件内容（语法错误或错误步骤），全部
# 脚本主体被包在单个 main() 函数里、只在文件最后一行调用——bash 必须先
# 读完整个函数定义（含其后的全部逻辑）才能开始执行，因此运行中途改写
# 磁盘文件不影响本次已经在跑的进程。self-update 成功把 checkout 快进到
# 新 SHA 后，本脚本会以同样的参数 exec 一次刚更新的自身（用环境变量
# XM_DEPLOY_LOCAL_REEXEC=1 防止再次触发，仅内部使用，不供调用方设置），
# 确保真正部署的是新版本脚本而不是旧版本已解析在内存里的逻辑。
#
# 退出码 3：本地 release checkout 落后/分叉于 upstream 且无法自动
# fast-forward（fetch 已成功但 git merge --ff-only 失败，说明本地有
# upstream 没有的提交）。此时脚本会打印精确指令并停止，不猜测、不强推、
# 不部署一个状态不确定的 checkout；按提示手动执行
# `git fetch origin release/v0.1-launch && git merge --ff-only FETCH_HEAD`
# 核实/解决分叉后重新运行本脚本。fetch 本身失败（网络/镜像不可达）不算
# 这个退出码——那种情况仍走既有的「有精确匹配 SHA 才放行」fail-closed 路径
# （exit 1），因为无法确认 upstream 状态和"确认落后"不是一回事。
# cpa-observations 等待预算（XM-DEPLOY-CPAWAIT0）：production file 模式下，
# 该阶段等待新部署对应的四条同一 generation 的成功 CPA 观测
# （cpa.requests.daily/cpa.cost.daily/cpa.keys.usage/cpa.accounts.health）落库。
# 等待预算 = worker 自己的 cpa_sync 周期（同一个 XM_CPA_SYNC_INTERVAL、同一
# 解析规则和默认值，见 cmd/platform-worker/config.go 与
# internal/platform/jobs/cpa_sync.go 的 DefaultCPASyncInterval）+60s 安全边际，
# 不再复用其它探针共享的 probe_attempts×2s（约 24s）窗口——worker 容器刚
# 重建时，即使周期任务的 RunOnStart 会让它一启动就尝试同步一次，也没有一个
# 可靠的亚分钟级上界能保证这次尝试已经落库完成；唯一确定的上界是「最迟不超过
# 下一个正常周期节拍」。团队交接记录：这个窗口过短曾让两次生产部署在观测值
# 已经正确产生之后才被误判失败，详见本片 Handoff
# docs/handoffs/slices/XM-DEPLOY-CPAWAIT0.md。每 10s 轮询一次，至多每 60s
# 打印一行 cpa-observations=waiting 进度；预算耗尽仍未匹配时，失败行会把
# 最后一次观测到的状态元组和期望元组一起打出来，方便判断是旧 generation
# 还在、部分到齐还是完全没有。等待循环本身（parse_duration_seconds /
# cpa_observation_wait）定义在 main() 之外——纯函数、无副作用，方便
# tests/deploy/deploy-local.test.sh 直接 source 后单独调用验证，不需要经过
# Docker/Git/生产 root 前置检查；这不影响自我更新安全性
# （XM-DEPLOY-SELFUPDATE0）的「整份文件先解析完再执行」保证，因为它们和 main()
# 一样，都要等到文件读完、bash 到达文件最后一行才会真正被调用。
set -Eeuo pipefail
umask 077

# CPA_SYNC_DEFAULT_INTERVAL_SECONDS 镜像 internal/platform/jobs/cpa_sync.go 的
# DefaultCPASyncInterval（300s）——worker 在 XM_CPA_SYNC_INTERVAL 未设置时使用
# 的同一个默认值。cpa-observations 阶段据此推算等待预算；两处常量万一分叉，
# 唯一后果是等待预算不再精确匹配 worker 的真实周期（仍然安全，因为还加了
# 60s 安全边际），不是新的正确性风险。
CPA_SYNC_DEFAULT_INTERVAL_SECONDS=300

# 把 cmd/platform-worker/config.go 用 time.ParseDuration 解析
# XM_CPA_SYNC_INTERVAL 时会接受的字符串（如"300s"、"5m"、"1h30m"）转换成整数秒，
# 向上取整；只识别整数量值 + ns/us/ms/s/m/h 单位的序列——本仓库
# deploy/compose/.env.example 与两份 compose 文件里全部 *_INTERVAL 配置都是
# 这种形状，不支持 Go 允许但这里从未配过的小数量值。解析失败时返回非零，
# 调用方回退到 CPA_SYNC_DEFAULT_INTERVAL_SECONDS。
parse_duration_seconds() {
  local remaining="$1" total=0 unit number
  [ -n "$remaining" ] || return 1
  while [ -n "$remaining" ]; do
    [[ "$remaining" =~ ^([0-9]+)(ns|us|ms|s|m|h) ]] || return 1
    number="${BASH_REMATCH[1]}"
    unit="${BASH_REMATCH[2]}"
    remaining="${remaining:${#BASH_REMATCH[0]}}"
    case "$unit" in
      h) total=$((total + number * 3600)) ;;
      m) total=$((total + number * 60)) ;;
      s) total=$((total + number)) ;;
      ns|us|ms) : ;;
    esac
  done
  [ "$total" -gt 0 ] || return 1
  printf '%s' "$total"
}

# 格式化 cpa-observations 阶段的等待预算公告行（XM-DEPLOY-CPAWAIT0）。纯字符串
# 拼接，不重新解析 XM_CPA_SYNC_INTERVAL——main() 已经算出 interval_seconds，
# 这里只做 interval_seconds+60=budget 这一步并格式化输出。单独抽成函数是为了
# 让 tests/deploy/deploy-local.test.sh 能在不牵动 Docker/Git/生产 root 检查的
# 情况下，直接断言"budget 随 interval 变化"这条公式本身。
cpa_observation_budget_line() {
  local interval_seconds="$1" poll_seconds="$2" expected_generation="$3"
  echo "cpa-observations=budget budget=$((interval_seconds + 60))s interval=${interval_seconds}s poll=${poll_seconds}s expected_generation=$expected_generation"
}

# cpa-observations 阶段的等待循环（XM-DEPLOY-CPAWAIT0）。生产路径下由 main()
# 在 cpa-observations 阶段以同名局部变量 run_compose/POSTGRES_USER/
# POSTGRES_DB（bash 动态作用域：调用时可见，不需要额外传参）调用，行为与
# 内联在 main() 里完全一致。测试直接 source 本文件后自行定义同名的
# run_compose 假函数、POSTGRES_USER/POSTGRES_DB 变量与 sleep 函数来驱动它，
# 不牵动 Docker/Git/生产 root 检查。
#
# 每 poll_seconds 轮询一次 run_compose exec 出来的 Postgres 状态元组，直到
# 等于 expected_state 或用满 budget_seconds；至多每 60 秒打印一行等待进度
# （cpa-observations=waiting elapsed=…s expected_generation=…）。返回 0 表示
# 已匹配，1 表示预算耗尽；调用方从 CPA_OBSERVATION_LAST_STATE 读最后一次
# 观测到的元组去拼失败信息（bash 函数返回不了字符串，这里沿用 request_json()
# 把结果放进调用方看得到的地方这个既有做法，只是用变量而不是 $tmp_dir 文件
# ——这个值不需要跨进程，没必要落盘）。
cpa_observation_wait() {
  local expected_generation="$1" expected_state="$2" budget_seconds="$3" poll_seconds="$4"
  local elapsed=0 next_progress=0
  CPA_OBSERVATION_LAST_STATE=""
  while :; do
    CPA_OBSERVATION_LAST_STATE="$(run_compose exec -T postgres psql -X -qAt -F '|' \
      -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "
        SELECT count(*), count(DISTINCT watermark), min(watermark),
               count(*) FILTER (WHERE status='ok'),
               count(*) FILTER (WHERE observed_at IS NULL),
               count(*) FILTER (
                 WHERE metric_key='cpa.accounts.health'
                   AND COALESCE(value_json->>'run_at','') <> ''
               )
        FROM ops.metric_observation
        WHERE environment='production'
          AND metric_key IN (
            'cpa.requests.daily','cpa.cost.daily','cpa.keys.usage','cpa.accounts.health'
          )" 2>/dev/null || true)"
    if [ "$CPA_OBSERVATION_LAST_STATE" = "$expected_state" ]; then
      echo "cpa-observations=ok generation=$expected_generation elapsed=${elapsed}s"
      return 0
    fi
    if [ "$elapsed" -ge "$next_progress" ]; then
      echo "cpa-observations=waiting elapsed=${elapsed}s expected_generation=$expected_generation"
      next_progress=$((elapsed + 60))
    fi
    [ "$elapsed" -ge "$budget_seconds" ] && return 1
    sleep "$poll_seconds"
    elapsed=$((elapsed + poll_seconds))
  done
}

main() {
original_args=("$@")

# 先保存仅供契约测试使用的覆盖项，再清掉所有可能参与 Compose 插值的
# 调用者环境变量。Compose 的 shell 环境优先级高于 --env-file，不能让
# 一个遗留的 ENVIRONMENT/WEB_PORT/XM_* 把 staging 部署改到别处。
test_gate_value="${XM_DEPLOY_LOCAL_TEST_MODE:-0}"
skip_git_value="${XM_DEPLOY_LOCAL_SKIP_GIT:-0}"
repo_override_value="${XM_DEPLOY_LOCAL_REPO:-}"
env_file_override_value="${XM_DEPLOY_LOCAL_ENV_FILE:-}"
compose_file_override_value="${XM_DEPLOY_LOCAL_COMPOSE_FILE:-}"
web_url_override_value="${XM_DEPLOY_LOCAL_WEB_URL:-}"
docker_override_value="${XM_DEPLOY_LOCAL_DOCKER_BIN:-}"
curl_override_value="${XM_DEPLOY_LOCAL_CURL_BIN:-}"

# 不让调用者通过环境变量把 Docker、Compose 或 Git 重定向到别的控制面。
unset DOCKER_HOST DOCKER_CONTEXT DOCKER_CONFIG COMPOSE_PROJECT_NAME COMPOSE_FILE \
  COMPOSE_PROFILES COMPOSE_ENV_FILES COMPOSE_PATH_SEPARATOR \
  GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY \
  GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_CONFIG \
  GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_SSH_COMMAND GIT_PROXY_COMMAND \
  GIT_ASKPASS BASH_ENV ENV LD_PRELOAD LD_LIBRARY_PATH DYLD_INSERT_LIBRARIES \
  DYLD_LIBRARY_PATH NODE_OPTIONS PYTHONPATH RUBYOPT PERL5OPT CDPATH
for variable in ENVIRONMENT WEB_BIND WEB_PORT BUILD_VERSION BUILD_COMMIT \
  POSTGRES_DB POSTGRES_USER DATABASE_URL DATABASE_PASSWORD DATABASE_PASSWORD_REF \
  PGPASSWORD REQUEST_TIMEOUT HEARTBEAT_INTERVAL; do
  unset "$variable"
done
while IFS= read -r variable; do
  [ -n "$variable" ] && unset "$variable"
done < <(compgen -v | grep -E '^(XM_|POSTGRES_|DATABASE_|BUILD_|WEB_|COMPOSE_|DOCKER_)')
export GIT_TERMINAL_PROMPT=0

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# The checkout may contain only the platform sparse subtree. Git owns
# the checkout root; Compose and lifecycle assets belong to platform.
default_repo="$(git -C "$script_dir" rev-parse --show-toplevel 2>/dev/null || true)"
self_path="$script_dir/$(basename -- "${BASH_SOURCE[0]}")"
EXIT_CHECKOUT_BEHIND=3

usage() {
  cat <<'USAGE'
用法:
  deploy/scripts/deploy-local.sh [选项]

说明:
  只部署本机 staging 栈：Compose 项目 xingmang-launch，Web 端口 8088。
  默认从 origin 刷新 release/v0.1-launch，并要求工作树干净。

选项:
  --sha SHA                 期望部署的 release 提交（40 位 SHA，可选）
  --services LIST           构建服务，逗号分隔；默认 migrate,platform-api,platform-worker,web
  --service NAME            追加一个构建服务（可重复）
  --probe-attempts N        每个探针最多重试次数（默认 12，范围 1..30）
  --repo PATH               本地 Git checkout（默认脚本所在仓库）
  --env-file PATH           Compose .env（默认 <repo>/platform/deploy/compose/.env）
  --compose-file PATH       Compose 定义（默认 <repo>/platform/deploy/compose/launch.yaml）
  --override-file PATH      叠加的环境覆盖文件（仅允许 <repo>/platform/deploy/compose 下 server-staging.yaml / server-prod.yaml）
  --web-url URL             Web loopback 地址（默认 http://127.0.0.1:8088）
  --no-fetch                仅允许显式 test-mode，用于离线契约测试
  --test-mode               仅允许 XM_DEPLOY_LOCAL_TEST_MODE=1
  --dry-run                 只校验并打印计划，不调用 Docker/Git 写操作
  -h, --help

安全边界:
  项目名、环境、Compose 栈和 smoke 身份均固定；脚本不会 down、删卷、reset
  或自动回滚。失败会保留容器与卷供排查。
USAGE
}

die() {
  echo "DEPLOY LOCAL FAIL: $*" >&2
  exit 1
}

# 专用于「fetch 成功但本地 checkout 无法 fast-forward」——见文件头部对
# 退出码 3 的说明。与 die() 分开是为了给这一类失败一个可脚本化区分的
# 退出码，而不是和其它任意失败共用 exit 1。
die_checkout_behind() {
  echo "DEPLOY LOCAL FAIL: $*" >&2
  exit "$EXIT_CHECKOUT_BEHIND"
}

trim() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

normalize_path() {
  local value="$1"
  if command -v cygpath >/dev/null 2>&1 && [[ "$value" =~ ^[A-Za-z]:[\\/].* ]]; then
    value="$(cygpath -u -- "$value")"
  fi
  printf '%s' "$value"
}

validate_abs_path() {
  local label="$1" value="$2"
  [[ "$value" = /* ]] || die "$label 必须是绝对路径"
  [[ "$value" != "/" ]] || die "$label 不能是根目录"
  case "/$value/" in
    */../*|*/./*) die "$label 不得包含 . 或 .. 路径段" ;;
  esac
}

read_env_value() {
  local key="$1" line lhs rhs
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line%$'\r'}"
    lhs="${line%%=*}"
    [ "$lhs" != "$line" ] || continue
    lhs="$(trim "$lhs")"
    [ "$lhs" = "$key" ] || continue
    rhs="$(trim "${line#*=}")"
    rhs="$(printf '%s\n' "$rhs" | sed 's/[[:space:]]#.*$//')"
    rhs="$(trim "$rhs")"
    case "$rhs" in
      \"*\") rhs="${rhs:1:${#rhs}-2}" ;;
      \'*\') rhs="${rhs:1:${#rhs}-2}" ;;
    esac
    printf '%s' "$rhs"
    return 0
  done < "$env_file"
  return 1
}

resolve_binary() {
  local label="$1" value="$2" resolved
  if [[ "$value" == */* ]]; then
    value="$(normalize_path "$value")"
    validate_abs_path "$label" "$value"
    [ -x "$value" ] && [ ! -L "$value" ] || die "$label 不可执行或是符号链接: $value"
    printf '%s' "$value"
    return 0
  fi
  resolved="$(type -P -- "$value" 2>/dev/null || true)"
  [ -n "$resolved" ] && [ -x "$resolved" ] || die "找不到 $label: $value"
  printf '%s' "$resolved"
}

parse_uint() {
  case "$1" in ''|*[!0-9]*) return 1 ;; esac
  [ "$1" -ge 1 ] && [ "$1" -le 30 ]
}

allowed_service() {
  case "$1" in migrate|platform-api|platform-worker|web) return 0 ;; *) return 1 ;; esac
}

build_services=()
add_service() {
  local service="$1" existing
  allowed_service "$service" || die "不允许构建服务: $service"
  for existing in "${build_services[@]}"; do
    [ "$existing" = "$service" ] && return 0
  done
  build_services+=("$service")
}

parse_service_list() {
  local list="$1" item
  [ -n "$list" ] || die "services 列表不能为空"
  IFS=',' read -r -a items <<< "$list"
  [ "${#items[@]}" -gt 0 ] || die "services 列表不能为空"
  for item in "${items[@]}"; do
    item="$(trim "$item")"
    [ -n "$item" ] || die "services 列表含空服务名"
    add_service "$item"
  done
}

expected_sha=""
services_seen=0
probe_attempts=12
repo_path="$default_repo"
env_file=""
compose_file=""
override_file=""
web_url="http://127.0.0.1:8088"
dry_run=0
test_mode=0
no_fetch=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --sha)
      [ "$#" -ge 2 ] || { usage >&2; exit 2; }
      expected_sha="$2"; shift 2 ;;
    --sha=*) expected_sha="${1#*=}"; shift ;;
    --services)
      [ "$#" -ge 2 ] || { usage >&2; exit 2; }
      [ "$services_seen" -eq 0 ] || die "--services 不能与其它服务选择混用"
      build_services=(); parse_service_list "$2"; services_seen=1; shift 2 ;;
    --services=*)
      [ "$services_seen" -eq 0 ] || die "--services 不能与其它服务选择混用"
      build_services=(); parse_service_list "${1#*=}"; services_seen=1; shift ;;
    --service)
      [ "$#" -ge 2 ] || { usage >&2; exit 2; }
      [ "$services_seen" -eq 0 ] || die "--service 不能与 --services 混用"
      add_service "$2"; shift 2 ;;
    --service=*)
      [ "$services_seen" -eq 0 ] || die "--service 不能与 --services 混用"
      add_service "${1#*=}"; shift ;;
    --probe-attempts)
      [ "$#" -ge 2 ] || { usage >&2; exit 2; }
      probe_attempts="$2"; shift 2 ;;
    --probe-attempts=*) probe_attempts="${1#*=}"; shift ;;
    --repo)
      [ "$#" -ge 2 ] || { usage >&2; exit 2; }
      repo_path="$2"; shift 2 ;;
    --repo=*) repo_path="${1#*=}"; shift ;;
    --env-file)
      [ "$#" -ge 2 ] || { usage >&2; exit 2; }
      env_file="$2"; shift 2 ;;
    --env-file=*) env_file="${1#*=}"; shift ;;
    --compose-file)
      [ "$#" -ge 2 ] || { usage >&2; exit 2; }
      compose_file="$2"; shift 2 ;;
    --compose-file=*) compose_file="${1#*=}"; shift ;;
    --override-file)
      [ "$#" -ge 2 ] || { usage >&2; exit 2; }
      override_file="$2"; shift 2 ;;
    --override-file=*) override_file="${1#*=}"; shift ;;
    --web-url)
      [ "$#" -ge 2 ] || { usage >&2; exit 2; }
      web_url="$2"; shift 2 ;;
    --web-url=*) web_url="${1#*=}"; shift ;;
    --no-fetch) no_fetch=1; shift ;;
    --test-mode) test_mode=1; shift ;;
    --dry-run) dry_run=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die "未知参数: $1" ;;
  esac
done

if [ "$test_mode" -eq 1 ]; then
  [ "$test_gate_value" = "1" ] || die "--test-mode 需要 XM_DEPLOY_LOCAL_TEST_MODE=1"
else
  [ "$no_fetch" -eq 0 ] || die "--no-fetch 仅允许 test-mode"
  [ "$skip_git_value" != "1" ] || die "跳过 Git 仅允许 test-mode"
fi

if [ "$services_seen" -eq 0 ]; then
  add_service migrate
  add_service platform-api
  add_service platform-worker
  add_service web
fi

[[ "$expected_sha" = "" || "$expected_sha" =~ ^[0-9a-fA-F]{40}$ ]] || die "sha 必须是 40 位十六进制"
parse_uint "$probe_attempts" || die "probe-attempts 必须是 1..30"

if [ "$test_mode" -eq 1 ]; then
  [ -n "$repo_override_value" ] && [ "$repo_path" = "$default_repo" ] && repo_path="$repo_override_value" || true
  [ -n "$env_file_override_value" ] && [ "$env_file" = "" ] && env_file="$env_file_override_value" || true
  [ -n "$compose_file_override_value" ] && [ "$compose_file" = "" ] && compose_file="$compose_file_override_value" || true
  [ -n "$web_url_override_value" ] && [ "$web_url" = "http://127.0.0.1:8088" ] && web_url="$web_url_override_value" || true
fi

repo_path="$(normalize_path "$repo_path")"
validate_abs_path repo "$repo_path"
[ -d "$repo_path" ] && [ ! -L "$repo_path" ] || die "repo 不存在或是符号链接"
repo_path="$(cd -- "$repo_path" && pwd -P)"
project_path="$repo_path/platform"

[ -n "$compose_file" ] || compose_file="$project_path/deploy/compose/launch.yaml"
[ -n "$env_file" ] || env_file="$project_path/deploy/compose/.env"
compose_file="$(normalize_path "$compose_file")"
env_file="$(normalize_path "$env_file")"
validate_abs_path compose-file "$compose_file"
validate_abs_path env-file "$env_file"
[ -f "$compose_file" ] && [ ! -L "$compose_file" ] || die "compose-file 不存在或是符号链接"
[ -f "$env_file" ] && [ ! -L "$env_file" ] || die "env-file 不存在或是符号链接"
if ! duplicate_env_keys="$(awk -F= '/^[[:space:]]*[A-Za-z_][A-Za-z0-9_]*[[:space:]]*=/ { key=$1; sub(/^[[:space:]]*/, "", key); sub(/[[:space:]]*$/, "", key); count[key]++ } END { for (key in count) if (count[key] > 1) print key }' "$env_file")"; then
  die "无法解析 env-file"
fi
[ -z "$duplicate_env_keys" ] || die "env-file 含重复配置键"

if [ -n "$override_file" ]; then
  override_file="$(normalize_path "$override_file")"
  validate_abs_path override-file "$override_file"
  [ -f "$override_file" ] && [ ! -L "$override_file" ] || die "override-file 不存在或是符号链接"
  case "$(basename -- "$override_file")" in
    server-staging.yaml|server-prod.yaml) ;;
    *) die "override-file 只允许 server-staging.yaml 或 server-prod.yaml" ;;
  esac
  [ "$(dirname -- "$override_file")" = "$(dirname -- "$compose_file")" ] || die "override-file 必须与 compose-file 同目录"
fi
# 目标环境由覆盖文件决定：只有 server-prod.yaml 才允许（且要求）production。
# 这是唯一的生产闸门——没有覆盖文件的运行永远是 staging，.env 写了 production 也过不去。
expected_environment=staging
if [ -n "$override_file" ] && [ "$(basename -- "$override_file")" = "server-prod.yaml" ]; then
  expected_environment=production
fi
if [ "$test_mode" -eq 0 ]; then
  [ "$compose_file" = "$project_path/deploy/compose/launch.yaml" ] || die "compose-file 必须使用仓库内 launch.yaml"
  [ "$env_file" = "$project_path/deploy/compose/.env" ] || die "env-file 必须使用仓库内 .env"
fi

# Compose .env 只能提供应用配置，不能偷偷改变 Docker/Compose/Git 控制面。
if grep -Eq '^[[:space:]]*(COMPOSE_[A-Za-z0-9_]*|DOCKER_[A-Za-z0-9_]*|GIT_[A-Za-z0-9_]*)[[:space:]]*=' "$env_file"; then
  die "env-file 不得注入 Docker/Compose/Git 控制变量"
fi

environment_value="$(read_env_value ENVIRONMENT 2>/dev/null || true)"
environment_value="$(trim "$environment_value")"
if [ "$expected_environment" = "production" ]; then
  [ "$environment_value" = "production" ] || die "server-prod.yaml 覆盖要求 env-file 显式 ENVIRONMENT=production"
else
  [ -z "$environment_value" ] || [ "$environment_value" = "staging" ] || die "本地脚本只允许 ENVIRONMENT=staging（生产请带 --override-file <绝对路径>/server-prod.yaml）"
fi
web_bind_value="$(read_env_value WEB_BIND 2>/dev/null || true)"
web_bind_value="$(trim "$web_bind_value")"
[ -z "$web_bind_value" ] || [ "$web_bind_value" = "127.0.0.1" ] || die "本地 Web 只允许绑定 127.0.0.1"
web_port_value="$(read_env_value WEB_PORT 2>/dev/null || true)"
web_port_value="$(trim "$web_port_value")"
[ -z "$web_port_value" ] || [ "$web_port_value" = "8088" ] || die "本地 Web 端口必须是 8088"

web_url="${web_url%/}"
[[ "$web_url" =~ ^https?://(127\.0\.0\.1|localhost):[0-9]{1,5}$ ]] || die "web-url 只允许 loopback + 数字端口"
if [ "$test_mode" -eq 0 ]; then
  [ "$web_url" = "http://127.0.0.1:8088" ] || die "正式本地部署的 Web 地址固定为 http://127.0.0.1:8088"
fi

docker_value="docker"
curl_value="curl"
if [ "$test_mode" -eq 1 ]; then
  docker_value="${docker_override_value:-docker}"
  curl_value="${curl_override_value:-curl}"
fi
docker_bin="$(resolve_binary docker "$docker_value")"
curl_bin="$(resolve_binary curl "$curl_value")"

git_root="$(git -C "$repo_path" rev-parse --show-toplevel 2>/dev/null || true)"
[ -n "$git_root" ] || die "repo 不是 Git checkout"
git_root="$(cd -- "$git_root" && pwd -P)"
[ "$git_root" = "$repo_path" ] || die "Git checkout 根目录无法验证"
if [ "$test_mode" -eq 0 ]; then
  [ "$(basename -- "$repo_path")" = "xingmang-platform" ] || die "正式本地部署 checkout 名称必须是 xingmang-platform"
  origin_url="$(git -C "$repo_path" remote get-url origin 2>/dev/null || true)"
  case "$origin_url" in
    https://github.com/xufei5620/xingmang-platform.git|git@github.com:xufei5620/xingmang-platform.git|ssh://git@github.com/xufei5620/xingmang-platform.git|/srv/git/xingmang-platform.git|file:///srv/git/xingmang-platform.git|git@fiberstate:/srv/git/xingmang-platform.git|ssh://gitci@fiberstate/srv/git/xingmang-platform.git) ;;
    *) die "正式本地部署 origin 不是已登记的星芒仓库" ;;
  esac
fi

if ! worktree_status="$(git -C "$repo_path" status --porcelain --untracked-files=all 2>/dev/null)"; then
  die "无法读取 Git 工作树状态"
fi
[ -z "$worktree_status" ] || die "工作树不干净；不会 stash、clean 或覆盖本地改动"
current_branch="$(git -C "$repo_path" branch --show-current 2>/dev/null || true)"

current_sha="$(git -C "$repo_path" rev-parse HEAD 2>/dev/null || true)"
[[ "$current_sha" =~ ^[0-9a-fA-F]{40}$ ]] || die "无法解析当前 HEAD"
pre_fetch_branch="$current_branch"
pre_fetch_sha="$current_sha"

if [ "$dry_run" -eq 1 ]; then
  [ "$current_branch" = release/v0.1-launch ] || die "dry-run 需要 release/v0.1-launch；actual=${current_branch:-detached}"
  [ -z "$expected_sha" ] || [ "${current_sha,,}" = "${expected_sha,,}" ] || die "dry-run 的 sha 与当前 release 不一致"
  echo "DEPLOY LOCAL DRY-RUN"
  echo "project=xingmang-launch web=$web_url branch=$current_branch sha=${expected_sha:-$current_sha}"
  echo "services=$(IFS=,; echo "${build_services[*]}") bootstrap=enabled smoke=services,metrics,alerts"
  exit 0
fi

skip_git=0
if [ "$test_mode" -eq 1 ] && { [ "$skip_git_value" = "1" ] || [ "$no_fetch" -eq 1 ]; }; then
  skip_git=1
fi

fetch_ok=0
if [ "$skip_git" -eq 0 ]; then
  if ! git -C "$repo_path" fetch --no-tags origin refs/heads/release/v0.1-launch >/dev/null 2>&1; then
    # 本地中心过渡期 origin 可能仍是暂时不可达的 GitHub 镜像。只有在
    # 调用方明确给出 MERGED SHA 且该 SHA 已经是干净 release HEAD 时才放行；
    # 不给 SHA 或本地版本不一致仍 fail-closed，绝不猜测要部署哪个版本。
    if [ "$pre_fetch_branch" = "release/v0.1-launch" ] && [ -n "$expected_sha" ] && [ "${pre_fetch_sha,,}" = "${expected_sha,,}" ]; then
      echo "git-fetch=unavailable local-sha-verified" >&2
    else
      die "git fetch release/v0.1-launch 失败"
    fi
  else
    fetch_ok=1
  fi
  if [ "$fetch_ok" -eq 1 ]; then
    git -C "$repo_path" checkout --quiet release/v0.1-launch || die "checkout release/v0.1-launch 失败"
  fi
fi

if [ "$skip_git" -eq 1 ]; then
  :
else
  if ! worktree_status="$(git -C "$repo_path" status --porcelain --untracked-files=all 2>/dev/null)"; then
    die "checkout 后无法读取 Git 工作树状态"
  fi
  [ -z "$worktree_status" ] || die "checkout 后工作树不干净"
fi

current_sha="$(git -C "$repo_path" rev-parse HEAD 2>/dev/null || true)"
[[ "$current_sha" =~ ^[0-9a-fA-F]{40}$ ]] || die "无法解析 release HEAD"
if [ -n "$expected_sha" ] && [ "${current_sha,,}" != "${expected_sha,,}" ]; then
  if [ "$fetch_ok" -eq 1 ]; then
    fetched_sha="$(git -C "$repo_path" rev-parse FETCH_HEAD 2>/dev/null || true)"
    if [ "${fetched_sha,,}" = "${expected_sha,,}" ]; then
      git -C "$repo_path" merge --ff-only FETCH_HEAD >/dev/null 2>&1 || die_checkout_behind "release 无法快进到指定 sha（本地有 upstream 没有的提交）；请执行 git fetch origin release/v0.1-launch && git merge --ff-only FETCH_HEAD 核实/解决分叉后重新运行本脚本"
      current_sha="$(git -C "$repo_path" rev-parse HEAD 2>/dev/null || true)"
    fi
  fi
  [ "${current_sha,,}" = "${expected_sha,,}" ] || die "当前 release HEAD 与指定 sha 不一致"
elif [ -z "$expected_sha" ] && [ "$fetch_ok" -eq 1 ]; then
  fetched_sha="$(git -C "$repo_path" rev-parse FETCH_HEAD 2>/dev/null || true)"
  [ -n "$fetched_sha" ] || die "无法解析 fetch 后的 release SHA"
  if [ "${current_sha,,}" != "${fetched_sha,,}" ]; then
    git -C "$repo_path" merge --ff-only FETCH_HEAD >/dev/null 2>&1 || die_checkout_behind "release 无法快进到 fetch 后的 SHA（本地有 upstream 没有的提交）；请执行 git fetch origin release/v0.1-launch && git merge --ff-only FETCH_HEAD 核实/解决分叉后重新运行本脚本"
    current_sha="$(git -C "$repo_path" rev-parse HEAD 2>/dev/null || true)"
  fi
fi
target_sha="$current_sha"

# 自我更新 re-exec：只有真的做了 git 写操作（非 skip_git）且 checkout 内容
# 确实前进了（target_sha != 本次运行开始时的 SHA）才考虑；只有「被更新的
# checkout 就是本脚本自己所在的那份」时 re-exec 才有意义——--repo 指向
# 别的 checkout 时，本进程当前执行的字节从未被这次 git 操作动过，继续用
# 已解析在内存里的逻辑即可。用 XM_DEPLOY_LOCAL_REEXEC 保证最多 re-exec 一次。
if [ "$skip_git" -eq 0 ] && [ "$target_sha" != "$pre_fetch_sha" ]; then
  self_in_repo="$project_path/deploy/scripts/$(basename -- "$self_path")"
  if [ "$self_in_repo" = "$self_path" ]; then
    if [ "${XM_DEPLOY_LOCAL_REEXEC:-0}" != "1" ]; then
      echo "self-update=applied from=$pre_fetch_sha to=$target_sha action=reexec"
      # 顶部的调用者环境清理会 unset 所有 XM_/DOCKER_/... 前缀变量（防止
      # 遗留变量偷偷重定向正式部署），这里必须把契约测试用到的覆盖项显式
      # 传回子进程，否则 re-exec 出去的第二个进程会以为自己没收到这些覆盖。
      # 生产场景这些值本就是空/默认，显式传递不改变任何正式部署行为。
      XM_DEPLOY_LOCAL_REEXEC=1 \
      XM_DEPLOY_LOCAL_TEST_MODE="$test_gate_value" \
      XM_DEPLOY_LOCAL_SKIP_GIT="$skip_git_value" \
      XM_DEPLOY_LOCAL_REPO="$repo_override_value" \
      XM_DEPLOY_LOCAL_ENV_FILE="$env_file_override_value" \
      XM_DEPLOY_LOCAL_COMPOSE_FILE="$compose_file_override_value" \
      XM_DEPLOY_LOCAL_WEB_URL="$web_url_override_value" \
      XM_DEPLOY_LOCAL_DOCKER_BIN="$docker_override_value" \
      XM_DEPLOY_LOCAL_CURL_BIN="$curl_override_value" \
      exec "${BASH:-bash}" "$self_path" "${original_args[@]}"
      die "self-update: 无法 exec 刚更新的脚本 $self_path"
    else
      echo "self-update=skipped reason=already-reexeced from=$pre_fetch_sha to=$target_sha" >&2
    fi
  else
    echo "self-update=skipped reason=repo-not-self from=$pre_fetch_sha to=$target_sha" >&2
  fi
fi

# Compose 只允许使用本机 staging 的受控插值；这些值覆盖 .env/调用者可能
# 留下的同名变量，数据库口令本身仍只由 --env-file 提供，绝不在 shell 中回显。
export ENVIRONMENT="$expected_environment" WEB_BIND=127.0.0.1 WEB_PORT=8088
export POSTGRES_DB=xingmang POSTGRES_USER=xingmang
export BUILD_VERSION="$expected_environment" BUILD_COMMIT="$target_sha"

tmp_dir="$(mktemp -d)" || die "无法创建部署临时目录"
cpa_timer_recovery_needed=0
cleanup() {
  if [ "$cpa_timer_recovery_needed" -eq 1 ]; then
    # The newly installed producer and first generation were independently
    # verified. If a later app/smoke gate fails, keep refresh alive instead of
    # stranding a previously healthy timer in the stopped state.
    systemctl enable --now xingmang-cpa-snapshot.timer >/dev/null 2>&1 || true
  fi
  rm -rf -- "$tmp_dir"
}
trap cleanup EXIT
docker_config="$tmp_dir/docker-config"
mkdir -p -- "$docker_config"
export DOCKER_CONFIG="$docker_config"

# 不传 --project-directory：Compose 应以 launch.yaml 所在的
# deploy/compose 目录解析 build.context=../..。显式把项目目录设成仓库根会在
# Windows Docker Desktop 上把上下文错误解析成盘符根目录（例如 G:\deploy）。
compose_args=(compose --project-name xingmang-launch --file "$compose_file")
[ -n "$override_file" ] && compose_args+=(--file "$override_file")
compose_args+=(--env-file "$env_file")
run_compose() { "$docker_bin" "${compose_args[@]}" "$@"; }

request_json() {
  local label="$1" url="$2" pattern="$3" response_file error_file status attempt
  response_file="$tmp_dir/$label.response"
  error_file="$tmp_dir/$label.error"
  shift 3
  for attempt in $(seq 1 "$probe_attempts"); do
    : > "$response_file"
    : > "$error_file"
    status="$("$curl_bin" --fail --silent --show-error --connect-timeout 2 --max-time 5 \
      --proto '=http,https' --noproxy '*' -o "$response_file" -w '%{http_code}' "$@" "$url" 2>"$error_file" || true)"
    if [ "$status" = "200" ] && grep -Eq "$pattern" "$response_file"; then
      echo "$label=200 attempt=$attempt"
      return 0
    fi
    [ "$attempt" -lt "$probe_attempts" ] && sleep 1
  done
  echo "$label=fail status=${status:-000} attempts=$probe_attempts" >&2
  return 1
}

smoke_headers=(
  --header 'X-Dev-Principal-ID: dev-operator'
  --header 'X-Dev-Principal-Type: HUMAN'
  --header 'X-Dev-Scopes: registry.read,ops.read'
)

phase="docker-preflight"
"$docker_bin" version --format '{{.Server.Version}}' >/dev/null 2>&1 || die "$phase: Docker Engine 不可用"
"$docker_bin" compose version >/dev/null 2>&1 || die "$phase: Docker Compose 不可用"
context_name="$("$docker_bin" context show 2>/dev/null || true)"
case "$context_name" in
  default|desktop-linux) ;;
  *) die "$phase: Docker context 非本机" ;;
esac
context_endpoint="$("$docker_bin" context inspect --format '{{.Endpoints.docker.Host}}' "$context_name" 2>/dev/null || true)"
case "$context_endpoint" in
  ""|unix:///var/run/docker.sock|npipe:////./pipe/docker_engine|npipe:////./pipe/dockerDesktopLinuxEngine) ;;
  *) die "$phase: Docker context endpoint 非本机" ;;
esac

phase="stack-preflight"
run_compose ps -a >/dev/null 2>&1 || die "$phase: 无法读取 xingmang-launch 栈状态"

phase="compose-config"
run_compose config --quiet >/dev/null 2>&1 || die "$phase: Compose 配置无效"

phase="baseline"
existing_stack="$(run_compose ps -q 2>/dev/null || true)"
if [ -n "$existing_stack" ]; then
  baseline_ok=1
  request_json baseline-healthz "$web_url/healthz" '"status"[[:space:]]*:[[:space:]]*"ok"' >/dev/null 2>&1 || baseline_ok=0
  request_json baseline-readyz "$web_url/readyz" '"status"[[:space:]]*:[[:space:]]*"ready"' >/dev/null 2>&1 || baseline_ok=0
  request_json baseline-services "$web_url/api/v1/services" '"items"[[:space:]]*:' "${smoke_headers[@]}" >/dev/null 2>&1 || baseline_ok=0
  request_json baseline-metrics "$web_url/api/v1/metrics" '"items"[[:space:]]*:' "${smoke_headers[@]}" >/dev/null 2>&1 || baseline_ok=0
  request_json baseline-alerts "$web_url/api/v1/alerts" '"items"[[:space:]]*:' "${smoke_headers[@]}" >/dev/null 2>&1 || baseline_ok=0
  if [ "$baseline_ok" -eq 1 ]; then
    echo "baseline=healthy"
  else
    echo "baseline=not-ready (will reconcile after build)"
  fi
else
  echo "baseline=absent"
fi
cpa_mode_value="$(read_env_value XM_CPA_MODE 2>/dev/null || true)"
cpa_mode_value="$(trim "$cpa_mode_value")"
cpa_sync_enabled_value="$(read_env_value XM_CPA_SYNC_ENABLED 2>/dev/null || true)"
cpa_sync_enabled_value="$(trim "$cpa_sync_enabled_value")"
cpa_sync_enabled_value="${cpa_sync_enabled_value,,}"
cpa_sync_disabled=0
case "$cpa_sync_enabled_value" in 0|f|false) cpa_sync_disabled=1 ;; esac
if [ "$expected_environment" = "production" ]; then
  [ "${EUID:-$(id -u)}" -eq 0 ] || die "cpa-snapshot: production bind preparation requires root"
  command -v install >/dev/null 2>&1 || die "cpa-snapshot: install utility is unavailable"
  cpa_published_dir=/var/lib/xingmang/cpa-snapshot/published
  if [ -e "$cpa_published_dir" ]; then
    [ -d "$cpa_published_dir" ] && [ ! -L "$cpa_published_dir" ] \
      || die "cpa-snapshot: published bind source is not a real directory"
  else
    install -d -o root -g 10001 -m 0750 "$cpa_published_dir" \
      || die "cpa-snapshot: cannot create fixed published bind source"
  fi
fi
if [ "$expected_environment" = "production" ] && [ "$cpa_mode_value" = "file" ]; then
  # CPA file mode requires migrate in build service set: the host binary must
  # come from this exact target_sha, never an older cached migrate image.
  migrate_selected=0
  for selected_service in "${build_services[@]}"; do
    [ "$selected_service" = "migrate" ] && migrate_selected=1
  done
  [ "$migrate_selected" -eq 1 ] || die "build: CPA file mode requires migrate in build service set"
  [ "${EUID:-$(id -u)}" -eq 0 ] || die "cpa-snapshot: production file mode requires root"
  cpa_source=/root/cpa-stack/cpam-data/usage.sqlite
  cpa_source_dir=/root/cpa-stack/cpam-data
  [ -d "$cpa_source_dir" ] && [ ! -L "$cpa_source_dir" ] && [ -w "$cpa_source_dir" ] \
    || die "cpa-snapshot: active source directory is missing, symlinked, or cannot coordinate SHM"
  [ -f "$cpa_source" ] && [ ! -L "$cpa_source" ] && [ -s "$cpa_source" ] \
    || die "cpa-snapshot: active source database is not a non-empty regular file"
  command -v systemctl >/dev/null 2>&1 || die "cpa-snapshot: systemd is unavailable"
fi
phase="build"
run_compose build --pull=false "${build_services[@]}" >/dev/null 2>&1 || die "$phase: 构建失败"

phase="up-infra"
# 先只启动 PostgreSQL 与 migrate。阈值 current/history 必须在 API/worker
# 启动前由同一版本化 lifecycle 镜像导入，否则新进程会在切换窗口内读取
# 空配置并把告警评估置于不可用状态。一次性服务不使用 `up --wait`，
# 由后续显式命令和 HTTP 探针判断结果。
run_compose up -d --remove-orphans postgres migrate >/dev/null 2>&1 || die "$phase: postgres/migrate 启动失败"

phase="runway-bootstrap"
runway_bootstrap_log="$tmp_dir/runway-threshold-bootstrap.log"
runway_bootstrap_ok=0
for runway_bootstrap_attempt in $(seq 1 "$probe_attempts"); do
  # 000017 的运行时阈值必须由同一版本化 lifecycle 镜像导入；显式启用
  # tools profile，禁止退回宿主 go run 或旧版 psql 种子。服务自身依赖
  # migrate 成功完成，重复执行由 current/history 事务保证幂等。
  if run_compose --profile tools run --rm runway-threshold-bootstrap up >"$runway_bootstrap_log" 2>&1; then
    runway_bootstrap_ok=1
    break
  fi
  [ "$runway_bootstrap_attempt" -lt "$probe_attempts" ] && sleep 1
done
[ "$runway_bootstrap_ok" -eq 1 ] || die "$phase: 阈值 bootstrap 失败（容器与数据卷已保留）"
runway_bootstrap_summary="$(grep -E '^environment=.*revision=.*critical_days=.*warning_days=.*serious_days=' "$runway_bootstrap_log" | tail -n 1 || true)"
[ -n "$runway_bootstrap_summary" ] || runway_bootstrap_summary="completed"
echo "runway-bootstrap=ok summary=$runway_bootstrap_summary"

phase="bootstrap"
bootstrap_log="$tmp_dir/bootstrap.log"
bootstrap_ok=0
# 生产覆盖文件把演示登记放在 staging profile 下：服务清单里确实没有 bootstrap 时跳过，
# 不伪造演示数据；清单读不到时按原行为执行（失败仍会 die）。
compose_services="$(run_compose config --services 2>/dev/null || true)"
if [ -n "$compose_services" ] && ! printf '%s
' "$compose_services" | grep -qx bootstrap; then
  bootstrap_ok=2
fi
[ "$bootstrap_ok" -eq 2 ] || for bootstrap_attempt in $(seq 1 "$probe_attempts"); do
  # 保留 Compose 的 depends_on 链，让 migrate 成功后才会运行 bootstrap；
  # 不用 --no-deps 绕过迁移闸门。该命令本身仍由 bootstrap SQL 的
  # ON CONFLICT DO NOTHING 保证可重跑。
  if run_compose run --rm bootstrap >"$bootstrap_log" 2>&1; then
    bootstrap_ok=1
    break
  fi
  [ "$bootstrap_attempt" -lt "$probe_attempts" ] && sleep 1
done
if [ "$bootstrap_ok" -eq 2 ]; then
  echo "bootstrap=skipped reason=service-not-in-profile"
else
  [ "$bootstrap_ok" -eq 1 ] || die "$phase: 幂等演示登记失败（容器与数据卷已保留）"
  bootstrap_summary="$(grep -E '^(INSERT|UPDATE|DELETE|COMMIT)' "$bootstrap_log" | tr '\n' ' ' | sed 's/[[:space:]]\+/ /g' | sed 's/[[:space:]]*$//' || true)"
  [ -n "$bootstrap_summary" ] || bootstrap_summary="completed"
  echo "bootstrap=ok summary=$bootstrap_summary"
fi

phase="cpa-snapshot"
if [ "$expected_environment" = "production" ] && [ "$cpa_mode_value" = "file" ]; then
  [ "${EUID:-$(id -u)}" -eq 0 ] || die "$phase: production file mode requires root lifecycle installation"
  snapshot_binary="$tmp_dir/cpa-snapshot"
  # migrate is the exact image just built from target_sha and has completed
  # before runway-bootstrap succeeds. Extract only the static lifecycle tool;
  # no Go/sqlite development package is installed on the host.
  run_compose cp migrate:/usr/local/bin/cpa-snapshot "$snapshot_binary" >/dev/null 2>&1 \
    || die "$phase: cannot extract the commit-bound snapshot binary"
  chmod 0700 "$snapshot_binary" || die "$phase: cannot protect extracted snapshot binary"
  snapshot_install_log="$tmp_dir/cpa-snapshot-install.log"
  if ! bash "$project_path/deploy/scripts/install-cpa-snapshot.sh" "$snapshot_binary" "$target_sha" >"$snapshot_install_log" 2>&1; then
    die "$phase: initial snapshot/install failed; API/worker were not started"
  fi
  snapshot_summary="$(grep -F 'CPA SNAPSHOT INSTALL PASS:' "$snapshot_install_log" | tail -n 1 || true)"
  [ -n "$snapshot_summary" ] || die "$phase: installer returned without verified evidence"
  cpa_timer_recovery_needed=1
  echo "cpa-snapshot=ok summary=$snapshot_summary"
else
  echo "cpa-snapshot=skipped environment=$expected_environment mode=${cpa_mode_value:-off}"
fi

phase="cpa-consumer-preflight"
if [ "$expected_environment" = "production" ] && [ "$cpa_mode_value" = "file" ]; then
  /opt/xingmang/cpa-snapshot/current/cpa-snapshot verify >/dev/null 2>&1 \
    || die "$phase: host published snapshot verification failed"
  run_compose run --rm --no-deps --entrypoint sh platform-worker -eu -c '
    test -r /var/lib/xm/cpa/usage.sqlite
    test ! -w /var/lib/xm/cpa/usage.sqlite
    test ! -e /var/lib/xm/cpa/usage.sqlite-wal
    test ! -e /var/lib/xm/cpa/usage.sqlite-shm
    test ! -e /var/lib/xm/cpa/usage.sqlite-journal
  ' >/dev/null 2>&1 || die "$phase: new worker image cannot consume the standalone read-only snapshot"
  echo "cpa-consumer-preflight=verified"
fi

enable_cpa_snapshot_timer() {
  if [ "$expected_environment" = "production" ] && [ "$cpa_mode_value" = "file" ]; then
    phase="cpa-snapshot-timer"
    systemctl enable --now xingmang-cpa-snapshot.timer >/dev/null 2>&1 \
      || die "$phase: cannot enable verified snapshot refresh"
    systemctl is-enabled --quiet xingmang-cpa-snapshot.timer \
      || die "$phase: timer is not enabled"
    systemctl is-active --quiet xingmang-cpa-snapshot.timer \
      || die "$phase: timer is not active"
    cpa_timer_recovery_needed=0
    echo "cpa-snapshot-timer=enabled"
  fi
}

phase="up-app"
# 只有阈值与演示数据都完成后才启动 API/worker/web，确保运行时切换
# 从第一轮请求起就是 DB-backed。这里不使用 --remove-orphans，避免
# 按服务启动时误删仍需保留的 migrate/bootstrap 容器证据。
run_compose up -d platform-api platform-worker web >/dev/null 2>&1 || die "$phase: API/worker/web 启动失败"

phase="worker"
worker_container_id="$(run_compose ps -q --status running platform-worker 2>/dev/null || true)"
[ -n "$worker_container_id" ] || die "$phase: platform-worker 未处于 running 状态"

phase="cpa-consumer"
if [ "$expected_environment" = "production" ] && [ "$cpa_mode_value" = "file" ]; then
  /opt/xingmang/cpa-snapshot/current/cpa-snapshot verify >/dev/null 2>&1 \
    || die "$phase: host published snapshot verification failed after app start"
  for cpa_service in platform-api platform-worker; do
    cpa_container_id="$(run_compose ps -q --status running "$cpa_service" 2>/dev/null || true)"
    [ -n "$cpa_container_id" ] || die "$phase: $cpa_service is not running"
    cpa_runtime_mount="$("$docker_bin" inspect "$cpa_container_id" --format '{{range .Mounts}}{{if eq .Destination "/var/lib/xm/cpa"}}{{printf "%s|%t" .Source .RW}}{{end}}{{end}}' 2>/dev/null || true)"
    [ "$cpa_runtime_mount" = "/var/lib/xingmang/cpa-snapshot/published|false" ] \
      || die "$phase: $cpa_service is not bound read-only to the fixed published directory"
    run_compose exec -T "$cpa_service" sh -eu -c '
      test -r /var/lib/xm/cpa/usage.sqlite
      test ! -w /var/lib/xm/cpa/usage.sqlite
      test ! -e /var/lib/xm/cpa/usage.sqlite-wal
      test ! -e /var/lib/xm/cpa/usage.sqlite-shm
      test ! -e /var/lib/xm/cpa/usage.sqlite-journal
    ' >/dev/null 2>&1 || die "$phase: $cpa_service cannot consume the standalone read-only snapshot"
  done
  echo "cpa-consumer=verified services=platform-api,platform-worker"
fi

phase="cpa-observations"
if [ "$expected_environment" = "production" ] && [ "$cpa_mode_value" = "file" ] && [ "$cpa_sync_disabled" -eq 0 ]; then
  cpa_verify_json="$(/opt/xingmang/cpa-snapshot/current/cpa-snapshot verify 2>/dev/null)" \
    || die "$phase: cannot read published generation"
  cpa_expected_generation="$(printf '%s\n' "$cpa_verify_json" | sed -n 's/.*"generation":"\([0-9a-f]\{32\}\)".*/\1/p')"
  [[ "$cpa_expected_generation" =~ ^[0-9a-f]{32}$ ]] \
    || die "$phase: published generation is malformed"

  # 等待预算见文件头部「cpa-observations 等待预算」说明；这里不复用
  # probe_attempts（那是给其它探针共享的重试次数，cpa-observations 改用
  # 独立的时间预算，不受 --probe-attempts 影响）。
  cpa_sync_interval_raw="$(read_env_value XM_CPA_SYNC_INTERVAL 2>/dev/null || true)"
  cpa_sync_interval_raw="$(trim "$cpa_sync_interval_raw")"
  cpa_sync_interval_seconds="$(parse_duration_seconds "$cpa_sync_interval_raw" 2>/dev/null || true)"
  [[ "$cpa_sync_interval_seconds" =~ ^[0-9]+$ ]] && [ "$cpa_sync_interval_seconds" -gt 0 ] \
    || cpa_sync_interval_seconds="$CPA_SYNC_DEFAULT_INTERVAL_SECONDS"
  cpa_observation_budget_seconds=$((cpa_sync_interval_seconds + 60))
  cpa_observation_poll_seconds=10
  cpa_observation_budget_line "$cpa_sync_interval_seconds" "$cpa_observation_poll_seconds" "$cpa_expected_generation"

  cpa_observation_expected_state="4|1|$cpa_expected_generation|4|0|1"
  if cpa_observation_wait "$cpa_expected_generation" "$cpa_observation_expected_state" \
      "$cpa_observation_budget_seconds" "$cpa_observation_poll_seconds"; then
    cpa_observation_ok=1
  else
    cpa_observation_ok=0
  fi
  [ "$cpa_observation_ok" -eq 1 ] \
    || die "$phase: four same-generation successful CPA observations with run_at were not produced (expected=$cpa_observation_expected_state observed=${CPA_OBSERVATION_LAST_STATE:-<none>})"
elif [ "$expected_environment" = "production" ] && [ "$cpa_mode_value" = "file" ]; then
  echo "cpa-observations=skipped reason=XM_CPA_SYNC_ENABLED=false"
fi

phase="probes"
request_json healthz "$web_url/healthz" '"status"[[:space:]]*:[[:space:]]*"ok"' || die "$phase: /healthz 失败"
request_json readyz "$web_url/readyz" '"status"[[:space:]]*:[[:space:]]*"ready"' || die "$phase: /readyz 失败"

phase="smoke"
# XM-LOGIN：local 登录模式下开发头被拒是正确行为，烟测改为验证鉴权闸门本身（未登录必须 401/403）。
auth_mode_local=0
auth_mode_value="$(read_env_value XM_AUTH_MODE 2>/dev/null || true)"
# server-prod.yaml already defaults empty/unset auth mode to local.
if [ -z "$auth_mode_value" ] && [ "$expected_environment" = production ]; then
  auth_mode_value=local
fi
[ "$auth_mode_value" != local ] || auth_mode_local=1
if [ "$auth_mode_local" -eq 1 ]; then
  gate_status="$("$curl_bin" -sS --noproxy '*' --max-time 15 -o /dev/null -w '%{http_code}' "$web_url/api/v1/auth/me" 2>/dev/null || true)"
  case "$gate_status" in
    401|403) echo "auth-gate=$gate_status attempt=1" ;;
    *) die "$phase: local 鉴权闸门异常（/api/v1/auth/me 返回 ${gate_status:-无响应}，应为 401/403）" ;;
  esac
  echo "worker-log=$( [ -n "$worker_container_id" ] && echo available || echo unavailable )"
  enable_cpa_snapshot_timer
  echo "DEPLOY LOCAL PASS: sha=$target_sha project=xingmang-launch healthz=200 readyz=200 smoke=auth-gate:$gate_status"
  exit 0
fi
request_json services "$web_url/api/v1/services" '"items"[[:space:]]*:' "${smoke_headers[@]}" || die "$phase: services 失败"
if ! grep -Eq '"instance_id"[[:space:]]*:[[:space:]]*"sub2api-staging"' "$tmp_dir/services.response"; then
  die "$phase: staging 演示登记缺失"
fi
request_json metrics "$web_url/api/v1/metrics" '"items"[[:space:]]*:' "${smoke_headers[@]}" || die "$phase: metrics 失败"
request_json alerts "$web_url/api/v1/alerts" '"items"[[:space:]]*:' "${smoke_headers[@]}" || die "$phase: alerts 失败"

# 日志只作为证据摘要，不把完整 worker 输出或任何环境变量回显到终端。
worker_log="$tmp_dir/worker.log"
if run_compose logs --no-color --since 5m --tail 30 platform-worker >"$worker_log" 2>&1; then
  worker_lines="$(grep -E 'heartbeat|sync|collector|finance|alert|worker' "$worker_log" | tail -n 1 || true)"
  [ -n "$worker_lines" ] && echo "worker-log=available" || echo "worker-log=available-no-matching-line"
else
  echo "worker-log=unavailable (non-blocking)"
fi

enable_cpa_snapshot_timer
echo "DEPLOY LOCAL PASS: sha=$target_sha project=xingmang-launch healthz=200 readyz=200 smoke=services:200,metrics:200,alerts:200"
}

if [ "${BASH_SOURCE[0]}" = "${0}" ]; then
  main "$@"
fi
