#!/bin/bash -p
# XM-UNIFIED: retired independent production entry.
printf '%s\n' 'Independent deployment is retired. Use the monorepo scripts/unified-service.py check/build. Production cutover is not validated or authorized.' >&2
exit 64
# 星芒统一控制平台受控部署入口（XM-C-DEPLOY0-b）。
#
# 只接受 staging/prod 两档。脚本先读取 receive hook 产生的 exact SHA 状态，
# 再以固定顺序 fetch → checkout → compose config/build/up → health/ready 探针；
# 任一步失败即停止并写失败审计，不自动回滚、不执行任意命令。
set -Eeuo pipefail
umask 077

# 不继承调用者可能注入的 Git/Docker/Compose 控制面；凭据只由 Compose
# 的受控 env-file/secret provider 提供，不能被 DOCKER_HOST 或 GIT_CONFIG 重定向。
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY \
  GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_CONFIG GIT_CONFIG_GLOBAL \
  GIT_CONFIG_SYSTEM GIT_SSH_COMMAND GIT_PROXY_COMMAND GIT_ASKPASS \
  DOCKER_HOST DOCKER_CONTEXT DOCKER_CONFIG COMPOSE_PROJECT_NAME COMPOSE_FILE \
  COMPOSE_PROFILES COMPOSE_ENV_FILES COMPOSE_PATH_SEPARATOR
unset BASH_ENV ENV LD_PRELOAD LD_LIBRARY_PATH DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH \
  NODE_OPTIONS PYTHONPATH RUBYOPT PERL5OPT CDPATH
# 让 Docker/Git 不读取操作者 home 下的 context、credential helper 或全局配置。
export GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0 HOME=/nonexistent \
  XDG_CONFIG_HOME=/nonexistent DOCKER_CONFIG=/nonexistent

usage() {
  cat >&2 <<'USAGE'
用法:
  deploy.sh staging [选项]
  deploy.sh prod --confirm DEPLOY-PRODUCTION --reason "..." [选项]

选项:
  --env ENV                 staging 或 prod（可替代位置参数）
  --repo PATH               受控部署 checkout（默认 /srv/deploy/xingmang-platform）
  --remote NAME             Git remote（默认 origin）
  --status-dir PATH         CI 状态目录（默认 /srv/ci）
  --audit-log PATH          发布审计文件（默认 /srv/audit/xingmang-deploy.log）
  --env-file PATH           Compose env 文件（默认 checkout/deploy/compose/.env）
  --compose-file PATH       基础 Compose 文件（默认 launch.yaml）
  --override-file PATH      环境覆盖文件（默认 server-{staging,prod}.yaml）
  --project NAME            Compose 项目名（默认 xingmang-staging/xingmang-prod）
  --docker-bin PATH         Docker 可执行文件（默认 PATH 中的 docker）
  --curl-bin PATH           curl 可执行文件（默认 PATH 中的 curl）
  --health-url URL          活性探针（默认 loopback web /healthz）
  --ready-url URL            就绪探针（默认 loopback web /readyz）
  --probe-attempts N        每个探针最多重试次数（默认 12，1..30）
  --confirm TOKEN            production 二次确认（必须为 DEPLOY-PRODUCTION）
  --reason TEXT              审计原因（必填，禁止凭据关键词/换行）
  --notify-hook PATH         可选受控通知适配器（只接收脱敏 key=value stdin）
  --test-mode                仅本地测试；需 XM_DEPLOY_TEST_MODE=1，且禁止 prod
  --dry-run                 只做验证与计划输出，不 fetch/checkout/compose/写审计
  -h, --help
USAGE
}

env_name=""
repo_path="/srv/deploy/xingmang-platform"
remote_name="origin"
status_dir="/srv/ci"
audit_log="/srv/audit/xingmang-deploy.log"
env_file=""
compose_file=""
override_file=""
project_name=""
docker_bin="docker"
curl_bin="curl"
health_url=""
ready_url=""
probe_attempts=12
confirm_token=""
reason=""
dry_run=0
test_mode=0
notify_hook=""

die() {
  echo "DEPLOY FAIL: $*" >&2
  return 1
}

parse_uint() {
  case "$1" in
    ''|*[!0-9]*) return 1 ;;
  esac
  [ "$1" -ge 1 ] && [ "$1" -le 30 ]
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    staging|prod)
      [ -z "$env_name" ] || { usage; exit 2; }
      env_name="$1"
      shift
      ;;
    --env)
      [ "$#" -ge 2 ] || { usage; exit 2; }
      env_name="$2"
      shift 2
      ;;
    --env=*) env_name="${1#--env=}"; shift ;;
    --repo|--checkout)
      [ "$#" -ge 2 ] || { usage; exit 2; }
      repo_path="$2"
      shift 2
      ;;
    --repo=*|--checkout=*) repo_path="${1#*=}"; shift ;;
    --remote) [ "$#" -ge 2 ] || { usage; exit 2; }; remote_name="$2"; shift 2 ;;
    --remote=*) remote_name="${1#*=}"; shift ;;
    --status-dir) [ "$#" -ge 2 ] || { usage; exit 2; }; status_dir="$2"; shift 2 ;;
    --status-dir=*) status_dir="${1#*=}"; shift ;;
    --audit-log) [ "$#" -ge 2 ] || { usage; exit 2; }; audit_log="$2"; shift 2 ;;
    --audit-log=*) audit_log="${1#*=}"; shift ;;
    --env-file) [ "$#" -ge 2 ] || { usage; exit 2; }; env_file="$2"; shift 2 ;;
    --env-file=*) env_file="${1#*=}"; shift ;;
    --compose-file) [ "$#" -ge 2 ] || { usage; exit 2; }; compose_file="$2"; shift 2 ;;
    --compose-file=*) compose_file="${1#*=}"; shift ;;
    --override-file) [ "$#" -ge 2 ] || { usage; exit 2; }; override_file="$2"; shift 2 ;;
    --override-file=*) override_file="${1#*=}"; shift ;;
    --project) [ "$#" -ge 2 ] || { usage; exit 2; }; project_name="$2"; shift 2 ;;
    --project=*) project_name="${1#*=}"; shift ;;
    --docker-bin) [ "$#" -ge 2 ] || { usage; exit 2; }; docker_bin="$2"; shift 2 ;;
    --docker-bin=*) docker_bin="${1#*=}"; shift ;;
    --curl-bin) [ "$#" -ge 2 ] || { usage; exit 2; }; curl_bin="$2"; shift 2 ;;
    --curl-bin=*) curl_bin="${1#*=}"; shift ;;
    --health-url) [ "$#" -ge 2 ] || { usage; exit 2; }; health_url="$2"; shift 2 ;;
    --health-url=*) health_url="${1#*=}"; shift ;;
    --ready-url) [ "$#" -ge 2 ] || { usage; exit 2; }; ready_url="$2"; shift 2 ;;
    --ready-url=*) ready_url="${1#*=}"; shift ;;
    --probe-attempts) [ "$#" -ge 2 ] || { usage; exit 2; }; probe_attempts="$2"; shift 2 ;;
    --probe-attempts=*) probe_attempts="${1#*=}"; shift ;;
    --confirm) [ "$#" -ge 2 ] || { usage; exit 2; }; confirm_token="$2"; shift 2 ;;
    --confirm=*) confirm_token="${1#*=}"; shift ;;
    --reason) [ "$#" -ge 2 ] || { usage; exit 2; }; reason="$2"; shift 2 ;;
    --reason=*) reason="${1#*=}"; shift ;;
    --notify-hook) [ "$#" -ge 2 ] || { usage; exit 2; }; notify_hook="$2"; shift 2 ;;
    --notify-hook=*) notify_hook="${1#*=}"; shift ;;
    --dry-run) dry_run=1; shift ;;
    --test-mode) test_mode=1; shift ;;
    -h|--help) usage >&1; exit 0 ;;
    *) echo "DEPLOY FAIL: 未知参数 $1" >&2; usage; exit 2 ;;
  esac
done

[ -n "$env_name" ] || { usage; exit 2; }
case "$env_name" in staging|prod) ;; *) die "环境只能是 staging 或 prod"; exit 1 ;; esac
if [ "$test_mode" -eq 1 ]; then
  [ "${XM_DEPLOY_TEST_MODE:-0}" = "1" ] || { die "--test-mode 需要 XM_DEPLOY_TEST_MODE=1"; exit 1; }
  [ "$env_name" != "prod" ] || { die "test-mode 禁止用于 prod"; exit 1; }
fi
# 生产不使用调用者 PATH；test-mode 才允许测试夹具注入绝对 binary。
if [ "$test_mode" -eq 0 ]; then
  export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
fi
parse_uint "$probe_attempts" || { die "probe-attempts 必须是 1..30"; exit 1; }

validate_path() {
  local label="$1" value="$2"
  [ -n "$value" ] || { die "$label 不能为空"; return 1; }
  case "$value" in /*) ;; *) die "$label 必须是绝对路径"; return 1 ;; esac
  case "$value" in *[!A-Za-z0-9_./-]*) die "$label 含非法字符"; return 1 ;; esac
  case "/$value/" in */../*|*/./*) die "$label 不得包含 . 或 .. 路径段"; return 1 ;; esac
  [ "$value" != "/" ] || { die "$label 不能是根目录"; return 1; }
}

validate_path repo "$repo_path" || exit 1
validate_path status-dir "$status_dir" || exit 1
validate_path audit-log "$audit_log" || exit 1
[ "$remote_name" != "" ] && [[ "$remote_name" =~ ^[A-Za-z0-9._/-]+$ ]] || { die "remote 非法"; exit 1; }
[[ "$project_name" == "" || "$project_name" =~ ^[a-z0-9][a-z0-9_-]{0,62}$ ]] || { die "project 非法"; exit 1; }

# 生产不能通过 CLI 换到另一棵仓库、状态目录或 Compose；staging 也只允许
# 安装约定的服务器根目录。临时目录只能经显式 test-mode（且不可用于 prod）。
if [ "$test_mode" -eq 0 ]; then
  case "$repo_path" in /srv/deploy/xingmang-platform) ;; *) die "repo 不在受控部署路径"; exit 1 ;; esac
  case "$status_dir" in /srv/ci) ;; *) die "status-dir 不在受控 CI 路径"; exit 1 ;; esac
  case "$audit_log" in /srv/audit/*) ;; *) die "audit-log 不在受控审计路径"; exit 1 ;; esac
  [ "$remote_name" = "origin" ] || { die "remote 只能是 origin"; exit 1; }
  if [ -n "$notify_hook" ]; then
    [ "$notify_hook" = "/usr/local/libexec/xingmang-deploy-notify" ] || {
      die "生产通知适配器路径不可覆盖"; exit 1;
    }
  fi
  [ -z "$project_name" ] || {
    if [ "$env_name" = "staging" ]; then [ "$project_name" = "xingmang-staging" ] || { die "staging project 不可覆盖"; exit 1; }
    else [ "$project_name" = "xingmang-prod" ] || { die "prod project 不可覆盖"; exit 1; }
    fi
  }
fi

validate_reason() {
  [ -n "$reason" ] || { die "--reason 必填"; return 1; }
  printf '%s\n' "$reason" | LC_ALL=C grep -Eq '^[A-Za-z0-9][A-Za-z0-9 ._:/,@+_-]{0,199}$' || {
    die "reason 只能包含有限 ASCII 字符且不超过 200 字节"; return 1;
  }
  case "${reason,,}" in
    *password*|*passwd*|*token*|*secret*|*credential*|*dsn*|*begin*)
      die "reason 不能包含凭据/密钥内容"; return 1 ;;
  esac
}

actor="$(id -un 2>/dev/null || true)"
[[ "$actor" =~ ^[A-Za-z0-9._@-]+$ ]] || actor="unknown"
if [ "$env_name" = "prod" ] && [ "$dry_run" -eq 0 ]; then
  [ "$confirm_token" = "DEPLOY-PRODUCTION" ] || {
    die "production 必须提供 --confirm DEPLOY-PRODUCTION"; exit 1;
  }
fi
validate_reason || exit 1
reason_audit="${reason// /_}"

if [ -z "$project_name" ]; then
  if [ "$env_name" = "staging" ]; then project_name="xingmang-staging"; else project_name="xingmang-prod"; fi
fi

project_path="$repo_path"
asset_prefix=""
if [ -d "$repo_path/platform/deploy/compose" ]; then
  project_path="$repo_path/platform"
  asset_prefix="platform/"
fi

case "$env_name" in
  staging)
    branch_name="release/v0.1-launch"
    ref_name="refs/heads/release/v0.1-launch"
    default_port=18088
    [ -n "$override_file" ] || override_file="$project_path/deploy/compose/server-staging.yaml"
    ;;
  prod)
    branch_name="main"
    ref_name="refs/heads/main"
    default_port=18089
    [ -n "$override_file" ] || override_file="$project_path/deploy/compose/server-prod.yaml"
    ;;
esac
[ -n "$compose_file" ] || compose_file="$project_path/deploy/compose/launch.yaml"
[ -n "$env_file" ] || {
  if [ -f "$project_path/deploy/compose/.env" ]; then env_file="$project_path/deploy/compose/.env"; fi
}
[ -n "$health_url" ] || health_url="http://127.0.0.1:$default_port/healthz"
[ -n "$ready_url" ] || ready_url="http://127.0.0.1:$default_port/readyz"

if [ "$test_mode" -eq 0 ]; then
  [ "$compose_file" = "$project_path/deploy/compose/launch.yaml" ] || {
    die "compose-file 只能使用仓库内 launch.yaml"; exit 1;
  }
  expected_override="$project_path/deploy/compose/server-$env_name.yaml"
  [ "$override_file" = "$expected_override" ] || {
    die "override-file 只能使用当前环境的服务器覆盖"; exit 1;
  }
  if [ -n "$env_file" ]; then
    [ "$env_file" = "$project_path/deploy/compose/.env" ] || {
      die "env-file 只能使用受控 Compose .env"; exit 1;
    }
  else
    die "服务器部署必须显式存在 deploy/compose/.env"; exit 1
  fi
  [ "$health_url" = "http://127.0.0.1:$default_port/healthz" ] || {
    die "生产/服务器 health-url 不可覆盖"; exit 1;
  }
  [ "$ready_url" = "http://127.0.0.1:$default_port/readyz" ] || {
    die "生产/服务器 ready-url 不可覆盖"; exit 1;
  }
fi

validate_file_path() {
  local label="$1" value="$2" must_exist="$3"
  validate_path "$label" "$value" || return 1
  case "$value" in "$repo_path"/*) ;; *) die "$label 必须位于 checkout 内"; return 1 ;; esac
  if [ "$must_exist" -eq 1 ]; then
    [ -f "$value" ] && [ ! -L "$value" ] || { die "$label 不存在或是符号链接"; return 1; }
  fi
}

validate_file_path compose-file "$compose_file" 1 || exit 1
validate_file_path override-file "$override_file" 1 || exit 1
if [ -n "$env_file" ]; then
  validate_path env-file "$env_file" || exit 1
  [ -f "$env_file" ] && [ ! -L "$env_file" ] || { die "env-file 不存在或是符号链接"; exit 1; }
  if grep -Eq '^[[:space:]]*(COMPOSE_[A-Za-z0-9_]*|DOCKER_HOST|DOCKER_CONTEXT|DOCKER_CONFIG|GIT_[A-Za-z0-9_]*)=' "$env_file"; then
    die "env-file 不得注入 Docker/Compose/Git 控制变量"; exit 1
  fi
fi

validate_probe_url() {
  local label="$1" value="$2" path="$3"
  [[ "$value" =~ ^https?://(127\.0\.0\.1|localhost):[0-9]{1,5}/(healthz|readyz)$ ]] || {
    die "$label 只允许 loopback 主机、数字端口和固定 $path 路径"; return 1;
  }
  [ "${value##*/}" = "${path#/}" ] || { die "$label 路径必须是 $path"; return 1; }
  case "$value" in *'?'*|*'#'*|*'@'*) die "$label 不得带 query/fragment/userinfo"; return 1 ;; esac
}
validate_probe_url health-url "$health_url" /healthz || exit 1
validate_probe_url ready-url "$ready_url" /readyz || exit 1

resolve_binary() {
  local label="$1" value="$2" resolved
  if [[ "$value" == */* ]]; then
    validate_path "$label" "$value" || return 1
    [ -x "$value" ] && [ ! -L "$value" ] || { die "$label 不可执行或是符号链接"; return 1; }
    printf '%s\n' "$value"
  else
    resolved="$(command -v "$value" 2>/dev/null || true)"
    [ -n "$resolved" ] || { die "找不到 $label: $value"; return 1; }
    printf '%s\n' "$resolved"
  fi
}
docker_bin="$(resolve_binary docker "$docker_bin")" || exit 1
curl_bin="$(resolve_binary curl "$curl_bin")" || exit 1

if [ "$test_mode" -eq 0 ]; then
  case "$docker_bin" in /usr/bin/docker|/usr/local/bin/docker) ;; *) die "生产 Docker 路径不在受控系统目录"; exit 1 ;; esac
  case "$curl_bin" in /usr/bin/curl|/usr/local/bin/curl) ;; *) die "生产 curl 路径不在受控系统目录"; exit 1 ;; esac
fi
if [ -n "$notify_hook" ]; then
  validate_path notify-hook "$notify_hook" || exit 1
  [ -x "$notify_hook" ] && [ ! -L "$notify_hook" ] || {
    die "notify-hook 不可执行或是符号链接"; exit 1;
  }
  notify_mode="$(stat -c '%a' "$notify_hook" 2>/dev/null || true)"
  if [ "$test_mode" -eq 0 ] && [[ "$notify_mode" =~ ^[0-7]+$ ]] && (( 8#$notify_mode & 022 )); then
    die "生产 notify-hook 不得可被组/其他用户写入"; exit 1
  fi
fi

[ -d "$repo_path" ] && [ ! -L "$repo_path" ] || { die "repo checkout 不存在或是符号链接"; exit 1; }
resolved_repo="$(readlink -f -- "$repo_path" 2>/dev/null || true)"
[ "$resolved_repo" = "$repo_path" ] || { die "repo 路径解析后越界或不可验证"; exit 1; }
repo_top="$(git -C "$repo_path" rev-parse --show-toplevel 2>/dev/null)" || { die "repo 不是 Git checkout"; exit 1; }
[ "$(cd -- "$repo_top" 2>/dev/null && pwd -P)" = "$resolved_repo" ] || { die "repo must be the actual Git top-level"; exit 1; }
git -C "$repo_path" rev-parse --is-shallow-repository 2>/dev/null | grep -qx false || {
  die "拒绝在 shallow checkout 部署"; exit 1;
}
remote_url="$(git -C "$repo_path" remote get-url "$remote_name" 2>/dev/null || true)"
[ -n "$remote_url" ] || { die "Git remote 不存在或没有 URL: $remote_name"; exit 1; }
case "$remote_url" in
  http://*|https://*|ssh://*)
    remote_authority="${remote_url#*://}"
    case "$remote_authority" in *'@'*) die "Git remote URL 不得包含 userinfo"; exit 1 ;; esac
    ;;
  *' '*|*$'\n'*) die "Git remote URL 含空白"; exit 1 ;;
esac
if [ "$test_mode" -eq 0 ]; then
  expected_remote="/srv/git/xingmang-platform.git"
  case "$remote_url" in
    "$expected_remote"|file://"$expected_remote") ;;
    *) die "Git remote 不是安装器登记的服务器 bare repo"; exit 1 ;;
  esac
fi
if [ -n "$env_file" ]; then
  mode="$(stat -c '%a' "$env_file" 2>/dev/null || true)"
  if [[ "$mode" =~ ^[0-7]+$ ]] && (( 8#$mode & 077 )); then
    die "env-file 必须是 owner-only（建议 0600）"; exit 1
  fi
fi

compose_args=(compose --project-name "$project_name" --project-directory "$(dirname -- "$compose_file")"
  --file "$compose_file" --file "$override_file")
[ -n "$env_file" ] && compose_args+=(--env-file "$env_file")
[ "$env_name" = "staging" ] && compose_args+=(--profile staging)
run_compose() { "$docker_bin" "${compose_args[@]}" "$@"; }

target_sha=""
lock_dir=""
lock_owned=0
audit_written=0

cleanup_deploy_lock() {
  # 正常失败也必须允许人工修复后重试；SIGKILL/主机掉电不会执行 EXIT trap，
  # 因而仍会留下锁并 fail-closed，需按 runbook 人工核对后清理。
  if [ "$lock_owned" -eq 1 ]; then
    rmdir -- "$lock_dir" 2>/dev/null || true
    lock_owned=0
  fi
}
trap cleanup_deploy_lock EXIT

if [ "$dry_run" -eq 0 ]; then
  audit_parent="$(dirname -- "$audit_log")"
  mkdir -p -- "$status_dir" "$audit_parent"
  [ ! -L "$status_dir" ] && [ ! -L "$audit_parent" ] || { die "status/audit 父目录不能是符号链接"; exit 1; }
  resolved_status="$(readlink -f -- "$status_dir" 2>/dev/null || true)"
  resolved_audit_parent="$(readlink -f -- "$audit_parent" 2>/dev/null || true)"
  [ "$resolved_status" = "$status_dir" ] && [ "$resolved_audit_parent" = "$audit_parent" ] || {
    die "status/audit 路径解析后越界或不可验证"; exit 1;
  }
fi

append_audit() {
  local result="$1" sha="$2" lock tmp mode previous_hash canonical_line entry_hash
  [ "$dry_run" -eq 0 ] || return 0
  [ ! -L "$audit_log" ] || { echo "DEPLOY FAIL: audit-log 不能是符号链接" >&2; return 1; }
  if [ -e "$audit_log" ]; then
    mode="$(stat -c '%a' "$audit_log" 2>/dev/null || true)"
    if [[ "$mode" =~ ^[0-7]+$ ]] && (( 8#$mode & 077 )); then
      echo "DEPLOY FAIL: audit-log 必须是 owner-only" >&2; return 1
    fi
    if [ -s "$audit_log" ]; then
      audit_last_byte="$(tail -c 1 "$audit_log" 2>/dev/null | od -An -t x1 | tr -d ' \r\n')"
      [ "$audit_last_byte" = "0a" ] || { echo "DEPLOY FAIL: audit-log 尾部必须以换行结束" >&2; return 1; }
      audit_last_line="$(tail -n 1 "$audit_log")"
      printf '%s\n' "$audit_last_line" | LC_ALL=C grep -Eq '(^| )entry_sha256=[0-9a-fA-F]{64}$' || {
        echo "DEPLOY FAIL: audit-log 末行缺少有效 entry_sha256" >&2; return 1;
      }
      audit_entry="${audit_last_line##* entry_sha256=}"
      audit_canonical="${audit_last_line% entry_sha256=*}"
      [ "$(printf '%s\n' "$audit_canonical" | sha256sum | awk '{print $1}')" = "$audit_entry" ] || {
        echo "DEPLOY FAIL: audit-log 末行 entry_sha256 校验失败" >&2; return 1;
      }
      printf '%s\n' "$audit_canonical" | LC_ALL=C grep -Eq '(^| )prev_sha256=(GENESIS|[0-9a-fA-F]{64})$' || {
        echo "DEPLOY FAIL: audit-log 末行 prev_sha256 格式非法" >&2; return 1;
      }
    fi
  else
    : > "$audit_log" || return 1
    chmod 0600 "$audit_log" || return 1
  fi
  lock="$audit_log.lock"
  if ! mkdir -- "$lock" 2>/dev/null; then
    echo "DEPLOY FAIL: audit-log 正在被另一个发布占用" >&2; return 1
  fi
  resolved_audit="$(readlink -f -- "$audit_log" 2>/dev/null || true)"
  [ "$resolved_audit" = "$audit_log" ] || {
    rmdir "$lock" 2>/dev/null || true
    echo "DEPLOY FAIL: audit-log 解析后越界或是符号链接" >&2; return 1;
  }
  tmp="$(mktemp -- "$audit_log.tmp.XXXXXX")" || { rmdir "$lock" 2>/dev/null || true; return 1; }
  previous_hash="GENESIS"
  if [ -s "$audit_log" ]; then
    previous_hash="$(tail -n 1 "$audit_log" | sha256sum | awk '{print $1}')"
  fi
  canonical_line="$(printf 'ts=%s actor=%s environment=%s ref=%s sha=%s project=%s result=%s reason=%s prev_sha256=%s' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$actor" "$env_name" "$ref_name" "$sha" \
    "$project_name" "$result" "$reason_audit" "$previous_hash")"
  entry_hash="$(printf '%s\n' "$canonical_line" | sha256sum | awk '{print $1}')"
  printf '%s entry_sha256=%s\n' "$canonical_line" "$entry_hash" > "$tmp" || {
      rm -f -- "$tmp"; rmdir "$lock" 2>/dev/null || true; return 1;
    }
  cat "$tmp" >> "$audit_log" || {
    rm -f -- "$tmp"; rmdir "$lock" 2>/dev/null || true; return 1;
  }
  rm -f -- "$tmp"
  rmdir "$lock" 2>/dev/null || true
  audit_written=1
}

notify_result() {
  local result="$1" sha="$2"
  [ -n "$notify_hook" ] || return 0
  # 适配器不继承任何调用者环境或凭据；它应自行通过 CredentialRef 取
  # Telegram/Webhook 口令。失败只告警，不把已经完成的部署改写成失败。
  if ! env -i PATH=/usr/bin:/bin HOME=/nonexistent LANG=C LC_ALL=C \
    "$notify_hook" < <(printf 'environment=%s\nref=%s\nsha=%s\nresult=%s\n' \
      "$env_name" "$ref_name" "$sha" "$result"); then
    echo "DEPLOY WARN: 通知适配器失败（部署结果不回滚）" >&2
  fi
}

read_green_status() {
  local file="$status_dir/$target_sha.status" last
  [ -f "$file" ] && [ ! -L "$file" ] || { echo "CI status 缺失或是符号链接: $file" >&2; return 1; }
  [ "$(readlink -f -- "$file" 2>/dev/null || true)" = "$file" ] || {
    echo "CI status 解析后越界或不可验证: $file" >&2; return 1;
  }
  # 只接受严格单行 green，并要求以换行结束；多余字段/CR 都拒绝。
  awk 'NR == 1 && $0 == "green" { ok=1; next } { bad=1 } END { exit !(ok && !bad) }' "$file" || {
    echo "CI status 不是严格单行 green: $file" >&2; return 1;
  }
  last="$(tail -c 1 "$file" 2>/dev/null | od -An -t x1 | tr -d ' \r\n')"
  [ "$last" = "0a" ] || { echo "CI status 必须以换行结束: $file" >&2; return 1; }
}

probe() {
  local label="$1" url="$2" attempt
  for attempt in $(seq 1 "$probe_attempts"); do
    if "$curl_bin" --fail --silent --show-error --connect-timeout 2 --max-time 5 \
      --proto '=http,https' --output /dev/null "$url" >/dev/null 2>&1; then
      echo "probe=$label result=ok attempt=$attempt"
      return 0
    fi
    [ "$attempt" -lt "$probe_attempts" ] && sleep 1
  done
  echo "probe=$label result=fail attempts=$probe_attempts" >&2
  return 1
}

main() {
  if [ "$dry_run" -eq 1 ]; then
    echo "DRY-RUN: environment=$env_name ref=$ref_name project=$project_name"
    echo "DRY-RUN: checkout=$repo_path compose=$compose_file override=$override_file"
    return 0
  fi

  [ -n "$reason" ] || return 1
  # The checkout and FETCH_HEAD are shared across environments and candidates.
  # Use its Git metadata directory (also works for linked worktrees), not a SHA lock.
  lock_dir="$(git -C "$repo_path" rev-parse --path-format=absolute --git-path xm-deploy.lock)" || return 1
  [ -n "$lock_dir" ] || return 1
  if ! mkdir -- "$lock_dir" 2>/dev/null; then
    echo "DEPLOY FAIL: checkout 正在被另一个部署占用（锁未取得）" >&2
    return 75
  fi
  lock_owned=1
  git -C "$repo_path" diff --quiet --exit-code || return 1
  git -C "$repo_path" diff --cached --quiet --exit-code || return 1
  worktree_status=""
  if ! worktree_status="$(git -C "$repo_path" status --porcelain --untracked-files=all 2>/dev/null)"; then
    return 1
  fi
  [ -z "$worktree_status" ] || return 1

  # 用 exact ref fetch，随后只信 FETCH_HEAD/remote tracking 的 commit 对象。
  git -C "$repo_path" fetch --no-tags "$remote_name" "$ref_name" >/dev/null 2>&1 || return 1
  target_sha="$(git -C "$repo_path" rev-parse FETCH_HEAD 2>/dev/null)" || return 1
  [[ "$target_sha" =~ ^[0-9a-fA-F]{40}$ ]] || return 1
  [ "$(git -C "$repo_path" cat-file -t "$target_sha" 2>/dev/null)" = "commit" ] || return 1
  remote_sha="$(git -C "$repo_path" rev-parse "$remote_name/$branch_name" 2>/dev/null || true)"
  [ -z "$remote_sha" ] || [ "${remote_sha,,}" = "${target_sha,,}" ] || return 1

  # Compose 文件必须是目标提交中的受版本控制普通文件，不能用 untracked
  # 或仓库外文件替换部署定义。
  compose_rel="${compose_file#"$repo_path/"}"
  override_rel="${override_file#"$repo_path/"}"
  case "$compose_rel" in "${asset_prefix}deploy/compose/"*) ;; *) return 1 ;; esac
  case "$override_rel" in "${asset_prefix}deploy/compose/"*) ;; *) return 1 ;; esac
  [ "$compose_rel" != "$compose_file" ] || return 1
  [ "$override_rel" != "$override_file" ] || return 1
  git -C "$repo_path" cat-file -e "$target_sha:$compose_rel" || return 1
  git -C "$repo_path" cat-file -e "$target_sha:$override_rel" || return 1
  [ "$(git -C "$repo_path" cat-file -t "$target_sha:$compose_rel" 2>/dev/null)" = blob ] || return 1
  [ "$(git -C "$repo_path" cat-file -t "$target_sha:$override_rel" 2>/dev/null)" = blob ] || return 1

  read_green_status || return 1

  git -C "$repo_path" checkout --detach "$target_sha" >/dev/null 2>&1 || return 1
  worktree_status=""
  if ! worktree_status="$(git -C "$repo_path" status --porcelain --untracked-files=all 2>/dev/null)"; then
    return 1
  fi
  [ -z "$worktree_status" ] || return 1
  export BUILD_COMMIT="$target_sha"
  export BUILD_VERSION="$env_name-${target_sha:0:12}"

  run_compose config --quiet >/dev/null 2>&1 || return 1
  run_compose build --pull=false >/dev/null 2>&1 || return 1
  run_compose up -d --remove-orphans --wait >/dev/null 2>&1 || return 1
  probe health "$health_url" || return 1
  probe ready "$ready_url" || return 1
  return 0
}

if main; then
  if [ "$dry_run" -eq 0 ]; then
    append_audit green "$target_sha" || exit 1
    notify_result green "$target_sha"
  fi
  echo "DEPLOY PASS: environment=$env_name sha=${target_sha:-dry-run}"
  exit 0
else
  rc=$?
fi
if [ "$dry_run" -eq 0 ] && [ -n "$target_sha" ] && [ "$audit_written" -eq 0 ]; then
  append_audit red "$target_sha" || true
  notify_result red "$target_sha"
fi
echo "DEPLOY FAIL: environment=$env_name sha=${target_sha:-unknown}（未自动回滚）" >&2
exit "$rc"
