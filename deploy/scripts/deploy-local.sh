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
set -Eeuo pipefail
umask 077

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
default_repo="$(cd -- "$script_dir/../.." && pwd -P)"

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
  --env-file PATH           Compose .env（默认 <repo>/deploy/compose/.env）
  --compose-file PATH       Compose 定义（默认 <repo>/deploy/compose/launch.yaml）
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

[ -n "$compose_file" ] || compose_file="$repo_path/deploy/compose/launch.yaml"
[ -n "$env_file" ] || env_file="$repo_path/deploy/compose/.env"
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

if [ "$test_mode" -eq 0 ]; then
  [ "$compose_file" = "$repo_path/deploy/compose/launch.yaml" ] || die "compose-file 必须使用仓库内 launch.yaml"
  [ "$env_file" = "$repo_path/deploy/compose/.env" ] || die "env-file 必须使用仓库内 .env"
fi

# Compose .env 只能提供应用配置，不能偷偷改变 Docker/Compose/Git 控制面。
if grep -Eq '^[[:space:]]*(COMPOSE_[A-Za-z0-9_]*|DOCKER_[A-Za-z0-9_]*|GIT_[A-Za-z0-9_]*)[[:space:]]*=' "$env_file"; then
  die "env-file 不得注入 Docker/Compose/Git 控制变量"
fi

environment_value="$(read_env_value ENVIRONMENT 2>/dev/null || true)"
environment_value="$(trim "$environment_value")"
[ -z "$environment_value" ] || [ "$environment_value" = "staging" ] || die "本地脚本只允许 ENVIRONMENT=staging"
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
  [ -z "$expected_sha" ] || [ "${current_sha,,}" = "${expected_sha,,}" ] || die "dry-run 的 sha 与当前 release 不一致"
  echo "DEPLOY LOCAL DRY-RUN"
  echo "project=xingmang-launch web=$web_url branch=release/v0.1-launch sha=${expected_sha:-$current_sha}"
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
      git -C "$repo_path" merge --ff-only FETCH_HEAD >/dev/null 2>&1 || die "release 无法快进到指定 sha"
      current_sha="$(git -C "$repo_path" rev-parse HEAD 2>/dev/null || true)"
    fi
  fi
  [ "${current_sha,,}" = "${expected_sha,,}" ] || die "当前 release HEAD 与指定 sha 不一致"
elif [ -z "$expected_sha" ] && [ "$fetch_ok" -eq 1 ]; then
  fetched_sha="$(git -C "$repo_path" rev-parse FETCH_HEAD 2>/dev/null || true)"
  [ -n "$fetched_sha" ] || die "无法解析 fetch 后的 release SHA"
  if [ "${current_sha,,}" != "${fetched_sha,,}" ]; then
    git -C "$repo_path" merge --ff-only FETCH_HEAD >/dev/null 2>&1 || die "release 无法快进到 fetch 后的 SHA"
    current_sha="$(git -C "$repo_path" rev-parse HEAD 2>/dev/null || true)"
  fi
fi
target_sha="$current_sha"

# Compose 只允许使用本机 staging 的受控插值；这些值覆盖 .env/调用者可能
# 留下的同名变量，数据库口令本身仍只由 --env-file 提供，绝不在 shell 中回显。
export ENVIRONMENT=staging WEB_BIND=127.0.0.1 WEB_PORT=8088
export POSTGRES_DB=xingmang POSTGRES_USER=xingmang
export BUILD_VERSION=staging BUILD_COMMIT="$target_sha"

tmp_dir="$(mktemp -d)" || die "无法创建部署临时目录"
cleanup() { rm -rf -- "$tmp_dir"; }
trap cleanup EXIT
docker_config="$tmp_dir/docker-config"
mkdir -p -- "$docker_config"
export DOCKER_CONFIG="$docker_config"

# 不传 --project-directory：Compose 应以 launch.yaml 所在的
# deploy/compose 目录解析 build.context=../..。显式把项目目录设成仓库根会在
# Windows Docker Desktop 上把上下文错误解析成盘符根目录（例如 K:\deploy）。
compose_args=(compose --project-name xingmang-launch --file "$compose_file" --env-file "$env_file")
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
for bootstrap_attempt in $(seq 1 "$probe_attempts"); do
  # 保留 Compose 的 depends_on 链，让 migrate 成功后才会运行 bootstrap；
  # 不用 --no-deps 绕过迁移闸门。该命令本身仍由 bootstrap SQL 的
  # ON CONFLICT DO NOTHING 保证可重跑。
  if run_compose run --rm bootstrap >"$bootstrap_log" 2>&1; then
    bootstrap_ok=1
    break
  fi
  [ "$bootstrap_attempt" -lt "$probe_attempts" ] && sleep 1
done
[ "$bootstrap_ok" -eq 1 ] || die "$phase: 幂等演示登记失败（容器与数据卷已保留）"
bootstrap_summary="$(grep -E '^(INSERT|UPDATE|DELETE|COMMIT)' "$bootstrap_log" | tr '\n' ' ' | sed 's/[[:space:]]\+/ /g' | sed 's/[[:space:]]*$//' || true)"
[ -n "$bootstrap_summary" ] || bootstrap_summary="completed"
echo "bootstrap=ok summary=$bootstrap_summary"

phase="up-app"
# 只有阈值与演示数据都完成后才启动 API/worker/web，确保运行时切换
# 从第一轮请求起就是 DB-backed。这里不使用 --remove-orphans，避免
# 按服务启动时误删仍需保留的 migrate/bootstrap 容器证据。
run_compose up -d platform-api platform-worker web >/dev/null 2>&1 || die "$phase: API/worker/web 启动失败"

phase="worker"
worker_container_id="$(run_compose ps -q --status running platform-worker 2>/dev/null || true)"
[ -n "$worker_container_id" ] || die "$phase: platform-worker 未处于 running 状态"

phase="probes"
request_json healthz "$web_url/healthz" '"status"[[:space:]]*:[[:space:]]*"ok"' || die "$phase: /healthz 失败"
request_json readyz "$web_url/readyz" '"status"[[:space:]]*:[[:space:]]*"ready"' || die "$phase: /readyz 失败"

phase="smoke"
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

echo "DEPLOY LOCAL PASS: sha=$target_sha project=xingmang-launch healthz=200 readyz=200 smoke=services:200,metrics:200,alerts:200"
