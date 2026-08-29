#!/bin/bash -p
# 将当前受控仓库镜像到 GitHub remote（XM-C-DEPLOY0-c）。
#
# 只允许名为 github 的 remote；脚本不会修改 origin，也不会自动重试。
set -Eeuo pipefail
umask 077

unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY \
  GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_CONFIG GIT_CONFIG_GLOBAL \
  GIT_CONFIG_SYSTEM GIT_SSH_COMMAND GIT_PROXY_COMMAND GIT_ASKPASS \
  DOCKER_HOST DOCKER_CONTEXT DOCKER_CONFIG COMPOSE_PROJECT_NAME COMPOSE_FILE \
  COMPOSE_PROFILES COMPOSE_ENV_FILES COMPOSE_PATH_SEPARATOR
unset BASH_ENV ENV LD_PRELOAD LD_LIBRARY_PATH DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH \
  NODE_OPTIONS PYTHONPATH RUBYOPT PERL5OPT CDPATH
export GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0 HOME=/nonexistent \
  XDG_CONFIG_HOME=/nonexistent DOCKER_CONFIG=/nonexistent
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

usage() {
  cat >&2 <<'USAGE'
用法:
  mirror-github.sh [选项]

选项:
  --repo PATH               受控 Git checkout（默认 /srv/deploy/xingmang-platform）
  --remote NAME             GitHub remote（只能是 github）
  --git-bin PATH            git 可执行文件（默认 PATH 中的 git）
  --dry-run                 只验证，不执行 git push --mirror github
  -h, --help
USAGE
}

repo_path="/srv/deploy/xingmang-platform"
remote_name="github"
git_bin="git"
dry_run=0

die() {
  echo "MIRROR FAIL: $*" >&2
  exit 1
}

validate_path() {
  local label="$1" value="$2"
  [ -n "$value" ] || die "$label 不能为空"
  case "$value" in /*) ;; *) die "$label 必须是绝对路径" ;; esac
  case "$value" in *[!A-Za-z0-9_./-]*) die "$label 含非法字符" ;; esac
  case "/$value/" in */../*|*/./*) die "$label 不得包含 . 或 .. 路径段" ;; esac
  [ "$value" != "/" ] || die "$label 不能是根目录"
}

validate_remote_url() {
  local url="$1" authority=""
  case "$url" in
    *$'\n'*|*$'\r'*|*' '*) die "remote URL 含空白" ;;
  esac
  case "$url" in
    *://*)
      authority="${url#*://}"
      authority="${authority%%/*}"
      case "$authority" in
        *'@'*) die "remote URL 不得包含凭据" ;;
      esac
      ;;
  esac
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --repo) [ "$#" -ge 2 ] || { usage; exit 2; }; repo_path="$2"; shift 2 ;;
    --repo=*) repo_path="${1#--repo=}"; shift ;;
    --remote) [ "$#" -ge 2 ] || { usage; exit 2; }; remote_name="$2"; shift 2 ;;
    --remote=*) remote_name="${1#--remote=}"; shift ;;
    --git-bin) [ "$#" -ge 2 ] || { usage; exit 2; }; git_bin="$2"; shift 2 ;;
    --git-bin=*) git_bin="${1#--git-bin=}"; shift ;;
    --dry-run) dry_run=1; shift ;;
    -h|--help) usage >&1; exit 0 ;;
    *) die "未知参数 $1" ;;
  esac
done

validate_path repo "$repo_path"
case "$remote_name" in
  github) ;;
  *) die "remote 只能是 github" ;;
esac
case "$git_bin" in
  */*) validate_path git-bin "$git_bin"; [ -x "$git_bin" ] || die "git-bin 不可执行" ;;
  *) command -v "$git_bin" >/dev/null 2>&1 || die "git-bin 不可执行" ;;
esac

[ -d "$repo_path" ] && [ ! -L "$repo_path" ] || die "repo 不存在或是符号链接"
resolved_repo="$(readlink -f -- "$repo_path" 2>/dev/null || true)"
[ "$resolved_repo" = "$repo_path" ] || die "repo 路径解析后越界或不可验证"

"$git_bin" -C "$repo_path" rev-parse --is-inside-work-tree >/dev/null 2>&1 || \
  die "repo 不是 Git checkout"

remote_url="$("$git_bin" -C "$repo_path" remote get-url "$remote_name" 2>/dev/null || true)"
[ -n "$remote_url" ] || die "github remote 不存在或没有 URL"
validate_remote_url "$remote_url"

if [ "$dry_run" -eq 1 ]; then
  echo "MIRROR OK: dry-run, 未执行 git push --mirror github"
  exit 0
fi

push_log="$(mktemp)"
cleanup() {
  rm -f -- "$push_log" 2>/dev/null || true
}
trap cleanup EXIT

if "$git_bin" -C "$repo_path" push --mirror "$remote_name" >"$push_log" 2>&1; then
  echo "MIRROR OK: 已执行 git push --mirror github"
  exit 0
else
  status=$?
  echo "MIRROR FAIL: git push --mirror github 失败（exit=$status）" >&2
  exit "$status"
fi
