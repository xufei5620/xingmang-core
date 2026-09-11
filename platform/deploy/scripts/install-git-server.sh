#!/bin/bash -p
# 安装自托管 Git 裸仓库、receive hooks 与 CI 目录。
# 只接受显式绝对路径；实际写入必须传 --confirm。脚本不接受任何凭据参数。
set -Eeuo pipefail
umask 077
if [ -x /usr/bin/git ]; then
  export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
fi
unset BASH_ENV ENV LD_PRELOAD LD_LIBRARY_PATH DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH \
  NODE_OPTIONS PYTHONPATH RUBYOPT PERL5OPT CDPATH

usage() {
  cat >&2 <<'USAGE'
用法: install-git-server.sh --repo /srv/git/xingmang-platform.git --ci-dir /srv/ci --confirm
      install-git-server.sh --repo /srv/git/xingmang-platform.git --ci-dir /srv/ci --dry-run
选项: --repo PATH, --ci-dir PATH, --git-user NAME(默认 gitci), --confirm, --dry-run
USAGE
}

repo_path=""
ci_dir=""
git_user="gitci"
confirmed=0
dry_run=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --repo) [ "$#" -ge 2 ] || { usage; exit 2; }; repo_path="$2"; shift 2 ;;
    --ci-dir) [ "$#" -ge 2 ] || { usage; exit 2; }; ci_dir="$2"; shift 2 ;;
    --git-user) [ "$#" -ge 2 ] || { usage; exit 2; }; git_user="$2"; shift 2 ;;
    --confirm|--yes) confirmed=1; shift ;;
    --dry-run) dry_run=1; shift ;;
    -h|--help) usage >&1; exit 0 ;;
    *) echo "INSTALL FAIL: 未知参数" >&2; usage; exit 2 ;;
  esac
done

if ! command -v id >/dev/null 2>&1 || [ "$(id -u)" != "0" ]; then
  echo "INSTALL FAIL: 必须由 root 执行" >&2
  exit 1
fi

validate_path() {
  local label="$1" value="$2"
  [ -n "$value" ] || { echo "INSTALL FAIL: $label 不能为空" >&2; return 1; }
  case "$value" in
    /*) ;;
    *) echo "INSTALL FAIL: $label 必须是绝对路径" >&2; return 1 ;;
  esac
  case "$value" in
    *[!A-Za-z0-9_./-]*) echo "INSTALL FAIL: $label 含非法字符" >&2; return 1 ;;
  esac
  case "/$value/" in
    */../*|*/./*) echo "INSTALL FAIL: $label 不得包含 . 或 .. 路径段" >&2; return 1 ;;
  esac
  [ "$value" != "/" ] || { echo "INSTALL FAIL: $label 不能是根目录" >&2; return 1; }
  # Resolve existing ancestors before any install/chown, including symlink aliases.
  local resolved
  resolved="$(readlink -m -- "$value" 2>/dev/null)" || {
    echo "INSTALL FAIL: $label 无法规范化" >&2; return 1;
  }
  [ "$resolved" = "$value" ] || {
    echo "INSTALL FAIL: $label 必须使用无别名的规范路径" >&2; return 1;
  }
}

validate_path repo "$repo_path"
validate_path ci-dir "$ci_dir"
case "$repo_path" in
  *.git) ;;
  *) echo "INSTALL FAIL: --repo 必须以 .git 结尾" >&2; exit 1 ;;
esac
[ "$repo_path" != "$ci_dir" ] || { echo "INSTALL FAIL: repo 与 ci-dir 必须不同" >&2; exit 1; }
[[ "$git_user" =~ ^[a-z_][a-z0-9_-]*$ ]] || { echo "INSTALL FAIL: --git-user 非法" >&2; exit 1; }
case "$git_user" in
  root|daemon|nobody) echo "INSTALL FAIL: 禁止使用高风险系统用户" >&2; exit 1 ;;
esac
repo_parent="$(dirname -- "$repo_path")"
validate_path repo-parent "$repo_parent"
case "$ci_dir/" in
  "$repo_parent/"* ) echo "INSTALL FAIL: ci-dir 不能位于仓库父目录内" >&2; exit 1 ;;
esac
case "$repo_parent/" in
  "$ci_dir/"* ) echo "INSTALL FAIL: 仓库不能位于 ci-dir 目录内" >&2; exit 1 ;;
esac

if [ "$dry_run" -eq 0 ] && [ "$confirmed" -ne 1 ]; then
  echo "INSTALL FAIL: 实际安装必须显式传入 --confirm" >&2
  exit 1
fi

echo "Git server install plan: repo=$repo_path ci-dir=$ci_dir user=$git_user"
if [ "$dry_run" -eq 1 ]; then
  echo "DRY-RUN: 不创建用户、目录或 hook"
  exit 0
fi

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || { echo "INSTALL FAIL: 缺少系统命令 $1" >&2; return 1; }
}
for cmd in id getent useradd usermod install git chown chmod mv sha256sum stat; do
  need_cmd "$cmd"
done

if ! id -u "$git_user" >/dev/null 2>&1; then
  useradd --system --create-home --shell /usr/sbin/nologin "$git_user"
fi
git_uid="$(id -u "$git_user" 2>/dev/null || true)"
[ -n "$git_uid" ] && [ "$git_uid" != "0" ] || {
  echo "INSTALL FAIL: git 用户不能是 root" >&2
  exit 1
}
git_group="$(id -gn "$git_user" 2>/dev/null || true)"
[ -n "$git_group" ] || { echo "INSTALL FAIL: 无法解析系统用户组" >&2; exit 1; }
getent group docker >/dev/null 2>&1 || {
  echo "INSTALL FAIL: 系统不存在 docker 组，请先配置 Docker" >&2
  exit 1
}
usermod -aG docker "$git_user"

install -d -m 0750 -o "$git_user" -g "$git_group" "$repo_parent" "$ci_dir"
[ ! -L "$repo_parent" ] || { echo "INSTALL FAIL: repo 父目录不能是符号链接" >&2; exit 1; }
[ ! -L "$ci_dir" ] || { echo "INSTALL FAIL: ci-dir 不能是符号链接" >&2; exit 1; }
if [ ! -d "$repo_path" ]; then
  git init --bare "$repo_path" >/dev/null
elif [ -L "$repo_path" ] || [ ! -f "$repo_path/HEAD" ] || [ ! -d "$repo_path/hooks" ]; then
  echo "INSTALL FAIL: 已存在路径但不是完整裸仓库" >&2
  exit 1
fi

hook_source="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../git-hooks" && pwd -P)"
ci_script_source="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../../scripts" && pwd -P)/ci-local.sh"
[ -f "$ci_script_source" ] && [ -x "$ci_script_source" ] || {
  echo "INSTALL FAIL: 缺少可执行的可信 CI 脚本" >&2
  exit 1
}
for hook in pre-receive post-receive; do
  [ -f "$hook_source/$hook" ] || { echo "INSTALL FAIL: 缺少 hook $hook" >&2; exit 1; }
  [ ! -L "$hook_source/$hook" ] || { echo "INSTALL FAIL: hook 源不能是符号链接: $hook" >&2; exit 1; }
  [ -x "$hook_source/$hook" ] || { echo "INSTALL FAIL: hook 不可执行: $hook" >&2; exit 1; }
  hook_tmp="$repo_path/hooks/.${hook}.tmp.$$"
  install -m 0750 -o "$git_user" -g "$git_group" "$hook_source/$hook" "$hook_tmp"
  mv -f -- "$hook_tmp" "$repo_path/hooks/$hook"
done

install -d -m 0750 -o "$git_user" -g "$git_group" "$ci_dir/work"
ci_script_target="$ci_dir/ci-local.sh"
install -m 0750 -o "$git_user" -g "$git_group" "$ci_script_source" "$ci_script_target"
ci_script_sha256="$(sha256sum -- "$ci_script_source" | awk '{print $1}')"
[[ "$ci_script_sha256" =~ ^[0-9a-fA-F]{64}$ ]] || {
  echo "INSTALL FAIL: 无法计算可信 CI 脚本 SHA-256" >&2
  exit 1
}
git --git-dir="$repo_path" config receive.denyNonFastForwards true
git --git-dir="$repo_path" config receive.denyDeletes true
git --git-dir="$repo_path" config xm.ci.statusDir "$ci_dir"
git --git-dir="$repo_path" config xm.ci.workDir "$ci_dir/work"
git --git-dir="$repo_path" config xm.ci.script "$ci_script_target"
git --git-dir="$repo_path" config xm.ci.scriptSha256 "$ci_script_sha256"
git --git-dir="$repo_path" config xm.ci.baseRef "origin/main"
git --git-dir="$repo_path" config xm.ci.requireBase true
git --git-dir="$repo_path" config xm.receive.promoteMarker "$repo_path/xm-promote.marker"
# main 晋级必须由 promote.sh 持有独立授权锁；pre-receive 会校验锁内 PID
# 仍是 promote.sh，避免另一个本地推送者抢先消费 marker。
git --git-dir="$repo_path" config xm.receive.requirePromoteLock true
# 不对整个 ci-dir 做递归 chown：它可能包含已有日志/状态或管理员文件。
# 只接管裸仓库与本安装创建的工作目录/可信脚本，避免误伤目录外的资产。
chown -R "$git_user:$git_group" "$repo_path"
chown "$git_user:$git_group" "$ci_dir" "$ci_dir/work" "$ci_script_target"
chmod 0750 "$repo_path" "$ci_dir" "$ci_dir/work" "$ci_script_target"

echo "INSTALL PASS: 裸仓库与 hooks 已幂等安装；未接收或输出任何凭据"
