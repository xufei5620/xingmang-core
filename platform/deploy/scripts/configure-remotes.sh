#!/bin/bash -p
# 配置服务器 origin 与 GitHub 镜像 remote（XM-C-DEPLOY0-c）。
#
# 本脚本只修改当前 checkout 的 .git/config；不会 push、rename 分支或读取凭据。
# 实际写入必须显式 --confirm CONFIGURE-REMOTES。发现未知 remote 时 fail closed。
set -Eeuo pipefail
umask 077
unset BASH_ENV ENV LD_PRELOAD LD_LIBRARY_PATH DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH \
  NODE_OPTIONS PYTHONPATH RUBYOPT PERL5OPT CDPATH \
  GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_COMMON_DIR \
  GIT_CONFIG GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_SSH_COMMAND GIT_PROXY_COMMAND \
  GIT_ASKPASS DOCKER_HOST DOCKER_CONTEXT DOCKER_CONFIG COMPOSE_PROJECT_NAME COMPOSE_FILE \
  COMPOSE_PROFILES COMPOSE_ENV_FILES COMPOSE_PATH_SEPARATOR
export GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0 HOME=/nonexistent \
  XDG_CONFIG_HOME=/nonexistent TMPDIR=/tmp
if [ -x /usr/bin/git ]; then
  export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
else
  export PATH=/mingw64/bin:/usr/bin:/bin
fi

usage() {
  cat >&2 <<'USAGE'
用法:
  configure-remotes.sh --server-url URL --github-url URL [选项]

选项:
  --repo PATH              Git checkout（默认当前目录）
  --server-url URL         服务器 bare repo（默认 /srv/git/xingmang-platform.git）
  --github-url URL         GitHub 镜像 URL（默认 git@github.com:xufei5620/xingmang-platform.git）
  --confirm TOKEN          实际写入必须为 CONFIGURE-REMOTES
  --test-mode              仅本地测试；需 XM_DEPLOY_TEST_MODE=1
  --dry-run                只打印计划，不修改 .git/config
  -h, --help
USAGE
}

repo_path="$(pwd -P)"
server_url="/srv/git/xingmang-platform.git"
github_url="git@github.com:xufei5620/xingmang-platform.git"
confirm_token=""
test_mode=0
dry_run=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --repo) [ "$#" -ge 2 ] || { usage; exit 2; }; repo_path="$2"; shift 2 ;;
    --repo=*) repo_path="$(printf '%s' "$1" | cut -d= -f2-)"; shift ;;
    --server-url) [ "$#" -ge 2 ] || { usage; exit 2; }; server_url="$2"; shift 2 ;;
    --server-url=*) server_url="$(printf '%s' "$1" | cut -d= -f2-)"; shift ;;
    --github-url) [ "$#" -ge 2 ] || { usage; exit 2; }; github_url="$2"; shift 2 ;;
    --github-url=*) github_url="$(printf '%s' "$1" | cut -d= -f2-)"; shift ;;
    --confirm) [ "$#" -ge 2 ] || { usage; exit 2; }; confirm_token="$2"; shift 2 ;;
    --confirm=*) confirm_token="$(printf '%s' "$1" | cut -d= -f2-)"; shift ;;
    --test-mode) test_mode=1; shift ;;
    --dry-run) dry_run=1; shift ;;
    -h|--help) usage >&1; exit 0 ;;
    *) echo "REMOTES FAIL: 未知参数 $1" >&2; usage; exit 2 ;;
  esac
done

validate_path() {
  local label="$1" value="$2"
  [ -n "$value" ] || { echo "REMOTES FAIL: $label 不能为空" >&2; return 1; }
  case "$value" in /*) ;; *) echo "REMOTES FAIL: $label 必须是绝对路径" >&2; return 1 ;; esac
  case "$value" in *[!A-Za-z0-9_./-]*) echo "REMOTES FAIL: $label 含非法字符" >&2; return 1 ;; esac
  case "/$value/" in */../*|*/./*) echo "REMOTES FAIL: $label 不得包含 . 或 .. 路径段" >&2; return 1 ;; esac
  [ "$value" != "/" ] || { echo "REMOTES FAIL: $label 不能是根目录" >&2; return 1; }
}

validate_remote_url() {
  local label="$1" value="$2"
  [ -n "$value" ] || { echo "REMOTES FAIL: $label 不能为空" >&2; return 1; }
  case "$value" in *[[:space:]]*|*'?'*|*'#'*) echo "REMOTES FAIL: $label 含空白/query/fragment" >&2; return 1 ;; esac
  case "$value" in
    /srv/git/*.git|/tmp/*.git|file:///srv/git/*.git|file:///tmp/*.git) ;;
    [A-Za-z]:/*.git|file://[A-Za-z]:/*.git)
      [ "$test_mode" -eq 1 ] || { echo "REMOTES FAIL: 生产不得使用 Windows 本地路径" >&2; return 1; } ;;
    git@github.com:*|git@fiberstate:/srv/git/*.git|ssh://gitci@fiberstate/srv/git/*.git|https://github.com/*) ;;
    *) echo "REMOTES FAIL: $label 主机/路径不在允许形态" >&2; return 1 ;;
  esac
  case "$value" in https://*'@'*) echo "REMOTES FAIL: $label 不得含 userinfo" >&2; return 1 ;; esac
}

normalize_local_url() {
  local value="$1"
  case "$value" in
    file://*) value="${value#file://}" ;;
    [A-Za-z]:/*)
      if command -v cygpath >/dev/null 2>&1; then value="$(cygpath -u "$value")"; fi
      ;;
  esac
  printf '%s' "$value"
}

validate_path repo "$repo_path" || exit 1
[ -d "$repo_path" ] && [ ! -L "$repo_path" ] || { echo "REMOTES FAIL: repo 不是目录或是符号链接" >&2; exit 1; }
[ "$(readlink -f -- "$repo_path" 2>/dev/null || true)" = "$repo_path" ] || {
  echo "REMOTES FAIL: repo 路径解析后越界或不可验证" >&2; exit 1;
}
git -C "$repo_path" rev-parse --show-toplevel >/dev/null 2>&1 || {
  echo "REMOTES FAIL: repo 不是 Git checkout" >&2; exit 1;
}
validate_remote_url server-url "$server_url" || exit 1
validate_remote_url github-url "$github_url" || exit 1
case "$github_url" in
  git@github.com:xufei5620/xingmang-platform.git|ssh://git@github.com/xufei5620/xingmang-platform.git|https://github.com/xufei5620/xingmang-platform.git) ;;
  *) echo "REMOTES FAIL: github-url 必须是官方仓库 xufei5620/xingmang-platform.git" >&2; exit 1 ;;
esac

if [ "$test_mode" -eq 1 ]; then
  test_mode_env="$(printenv XM_DEPLOY_TEST_MODE 2>/dev/null || true)"
  [ "$test_mode_env" = "1" ] || { echo "REMOTES FAIL: --test-mode 需要 XM_DEPLOY_TEST_MODE=1" >&2; exit 1; }
else
  [ "$repo_path" = "/srv/deploy/xingmang-platform" ] || {
    echo "REMOTES FAIL: 生产 checkout 不在受控路径（测试请显式 --test-mode）" >&2; exit 1;
  }
  [ "$server_url" = "/srv/git/xingmang-platform.git" ] || {
    echo "REMOTES FAIL: 生产 server-url 必须是安装器路径" >&2; exit 1;
  }
fi

origin_url="$(git -C "$repo_path" remote get-url origin 2>/dev/null || true)"
github_existing="$(git -C "$repo_path" remote get-url github 2>/dev/null || true)"
origin_urls="$(git -C "$repo_path" remote get-url --all origin 2>/dev/null || true)"
github_urls="$(git -C "$repo_path" remote get-url --all github 2>/dev/null || true)"
[ -z "$origin_urls" ] || [ "$(printf '%s\n' "$origin_urls" | wc -l)" -eq 1 ] || {
  echo "REMOTES FAIL: origin 配置了多个 URL，拒绝猜测" >&2; exit 1;
}
[ -z "$github_urls" ] || [ "$(printf '%s\n' "$github_urls" | wc -l)" -eq 1 ] || {
  echo "REMOTES FAIL: github 配置了多个 URL，拒绝猜测" >&2; exit 1;
}
rewrite_rules="$(git -C "$repo_path" config --local --get-regexp '^url\..*' 2>/dev/null | grep -Ei '\.(insteadof|pushinsteadof)( |$)' || true)"
[ -z "$rewrite_rules" ] || { echo "REMOTES FAIL: 检测到 url.* 重写规则，先人工核对" >&2; exit 1; }
remote_list="$(git -C "$repo_path" remote 2>/dev/null)" || {
  echo "REMOTES FAIL: 无法读取 remote 列表" >&2; exit 1;
}
while IFS= read -r remote_name; do
  case "$remote_name" in
    ""|origin|github) ;;
    *) echo "REMOTES FAIL: 发现未登记 remote $remote_name，拒绝静默保留/覆盖" >&2; exit 1 ;;
  esac
done <<< "$remote_list"
origin_pushurl="$(git -C "$repo_path" config --get-all remote.origin.pushurl 2>/dev/null || true)"
github_pushurl="$(git -C "$repo_path" config --get-all remote.github.pushurl 2>/dev/null || true)"
[ -z "$origin_pushurl" ] || { echo "REMOTES FAIL: origin pushurl 已存在，先人工核对" >&2; exit 1; }
[ -z "$github_pushurl" ] || { echo "REMOTES FAIL: github pushurl 已存在，先人工核对" >&2; exit 1; }
[ -z "$origin_url" ] || validate_remote_url origin-url "$origin_url"
[ -z "$github_existing" ] || validate_remote_url github-existing-url "$github_existing"

server_file_url="file://$server_url"
server_compare="$(normalize_local_url "$server_url")"
origin_compare="$(normalize_local_url "$origin_url")"
if [ -n "$origin_url" ] && [ "$origin_compare" != "$server_compare" ]; then
  case "$origin_url" in
    git@github.com:xufei5620/xingmang-platform.git|ssh://git@github.com/xufei5620/xingmang-platform.git|https://github.com/xufei5620/xingmang-platform.git) ;;
    *) echo "REMOTES FAIL: origin 不是已登记的官方 GitHub 镜像，拒绝迁移" >&2; exit 1 ;;
  esac
fi
if [ -n "$github_existing" ] && [ "$github_existing" != "$github_url" ]; then
  echo "REMOTES FAIL: 已有 github remote URL 不匹配，拒绝覆盖" >&2
  exit 1
fi
if [ -n "$origin_url" ] && [ "$origin_compare" != "$server_compare" ]; then
  case "$origin_url" in
    git@github.com:*|https://github.com/*)
      [ -z "$github_existing" ] || { echo "REMOTES FAIL: origin 是 GitHub 但 github 已占用不同配置" >&2; exit 1; }
      ;;
    *)
      echo "REMOTES FAIL: origin 指向未知地址，拒绝静默替换" >&2
      exit 1
      ;;
  esac
fi

git_dir="$(git -C "$repo_path" rev-parse --git-dir 2>/dev/null || true)"
case "$git_dir" in
  /*) ;;
  *) git_dir="$(cd -- "$repo_path/$git_dir" 2>/dev/null && pwd -P)" ;;
esac
[ -n "$git_dir" ] && [ -d "$git_dir" ] && [ ! -L "$git_dir" ] || {
  echo "REMOTES FAIL: 无法解析 Git 配置目录" >&2; exit 1;
}
lock_dir="$git_dir/xm-remotes.lock"
[ ! -e "$lock_dir" ] || [ ! -L "$lock_dir" ] || { echo "REMOTES FAIL: remotes 锁路径是符号链接" >&2; exit 75; }

if [ "$dry_run" -eq 1 ]; then
  echo "DRY-RUN: origin=$server_url github=$github_url repo=$repo_path"
  [ -n "$origin_url" ] && echo "DRY-RUN: existing-origin=$origin_url"
  exit 0
fi
[ "$confirm_token" = "CONFIGURE-REMOTES" ] || {
  echo "REMOTES FAIL: 实际写入必须提供 --confirm CONFIGURE-REMOTES" >&2; exit 1;
}
mkdir -- "$lock_dir" || { echo "REMOTES FAIL: 无法取得 remotes 锁" >&2; exit 75; }
config_backup=""
backup_ready=0
write_committed=0
cleanup() {
  rc="$?"
  rollback_failed=0
  if [ "$backup_ready" -eq 1 ] && [ "$write_committed" -eq 0 ]; then
    if ! cp -- "$config_backup" "$config_path" 2>/dev/null; then
      rollback_failed=1
      echo "REMOTES CRITICAL: 回滚 .git/config 失败，备份保留在 $config_backup" >&2
    fi
  fi
  if [ "$rollback_failed" -eq 0 ] && [ "$backup_ready" -eq 1 ]; then
    rm -f -- "$config_backup" 2>/dev/null || true
  fi
  if [ "$backup_ready" -eq 0 ] && [ -n "$config_backup" ]; then
    echo "REMOTES CRITICAL: 无法建立 .git/config 备份，备份文件保留在 $config_backup" >&2
    [ "$rc" -eq 0 ] && rc=1
  fi
  rmdir -- "$lock_dir" 2>/dev/null || true
  [ "$rollback_failed" -eq 0 ] || [ "$rc" -ne 0 ] || rc=1
  exit "$rc"
}
trap cleanup EXIT
config_path="$(git -C "$repo_path" rev-parse --git-path config 2>/dev/null || true)"
case "$config_path" in /*) ;; *) config_path="$repo_path/$config_path" ;; esac
[ -f "$config_path" ] && [ ! -L "$config_path" ] || { echo "REMOTES FAIL: Git config 不可备份" >&2; exit 1; }
config_backup="$(mktemp -- "$git_dir/.xm-remotes-config.XXXXXX")" || exit 1
chmod 0600 "$config_backup"
cp -- "$config_path" "$config_backup"
backup_ready=1

if [ -n "$origin_url" ] && [ "$origin_compare" != "$server_compare" ]; then
  # 先复制旧 GitHub URL，再改 origin；不要 `remote rename`，否则 Git 会把
  # branch.*.remote=origin 一并改成 github，后续 fetch 会继续读镜像而非服务器。
  if ! git -C "$repo_path" remote get-url github >/dev/null 2>&1; then
    git -C "$repo_path" remote add github "$origin_url"
  fi
fi
if git -C "$repo_path" remote get-url origin >/dev/null 2>&1; then
  git -C "$repo_path" remote set-url origin "$server_url"
else
  git -C "$repo_path" remote add origin "$server_url"
fi
if git -C "$repo_path" remote get-url github >/dev/null 2>&1; then
  git -C "$repo_path" remote set-url github "$github_url"
else
  git -C "$repo_path" remote add github "$github_url"
fi

[ "$(normalize_local_url "$(git -C "$repo_path" remote get-url origin)")" = "$server_compare" ] || {
  echo "REMOTES FAIL: origin 写后核对失败" >&2; exit 1;
}
[ "$(git -C "$repo_path" remote get-url github)" = "$github_url" ] || {
  echo "REMOTES FAIL: github 写后核对失败" >&2; exit 1;
}
write_committed=1
echo "REMOTES PASS: origin=$server_url github=$github_url"
