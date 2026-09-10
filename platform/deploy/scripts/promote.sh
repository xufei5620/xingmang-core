#!/bin/bash -p
# 把已经通过 release CI 的提交晋级到 main（XM-C-DEPLOY0-b）。
#
# main 的保护由 bare-repo pre-receive hook 执行。本脚本只通过 git push 触发
# hook，先以原子方式写入严格的 sha=<40hex> marker；不调用 update-ref/强推，
# 不接受未经人工确认的生产晋级。
set -Eeuo pipefail
umask 077

# 晋级必须使用本机登记的 bare repo 与 Git 配置；调用者不能用环境变量把
# git/ssh 重定向到另一台服务器或注入全局配置。
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY \
  GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_COMMON_DIR GIT_CONFIG GIT_CONFIG_GLOBAL \
  GIT_CONFIG_SYSTEM GIT_SSH_COMMAND GIT_PROXY_COMMAND GIT_ASKPASS \
  DOCKER_HOST DOCKER_CONTEXT DOCKER_CONFIG COMPOSE_PROJECT_NAME COMPOSE_FILE \
  COMPOSE_PROFILES COMPOSE_ENV_FILES COMPOSE_PATH_SEPARATOR
unset BASH_ENV ENV LD_PRELOAD LD_LIBRARY_PATH DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH \
  NODE_OPTIONS PYTHONPATH RUBYOPT PERL5OPT CDPATH
export GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0 HOME=/nonexistent \
  XDG_CONFIG_HOME=/nonexistent DOCKER_CONFIG=/nonexistent

usage() {
  cat >&2 <<'USAGE'
用法:
  promote.sh --confirm PROMOTE-PRODUCTION --reason "..." [选项]

选项:
  --repo PATH               服务器 bare repository（默认 /srv/git/xingmang-platform.git）
  --checkout PATH           受控部署 checkout（默认 /srv/deploy/xingmang-platform）
  --remote NAME             Git remote（默认 origin）
  --source-ref REF          仅允许 refs/heads/release/v0.1-launch
  --status-dir PATH         CI 状态目录（默认 /srv/ci）
  --audit-log PATH          晋级审计文件（默认 /srv/audit/xingmang-deploy.log）
  --confirm TOKEN           必须为 PROMOTE-PRODUCTION
  --reason TEXT             审计原因（必填，禁止凭据关键词/换行）
  --notify-hook PATH        可选受控通知适配器（只接收脱敏 key=value stdin）
  --test-mode               仅本地测试；需 XM_DEPLOY_TEST_MODE=1，禁止使用 /srv 路径
  --dry-run                 只验证计划，不写 marker、不 push、不写审计
  -h, --help
USAGE
}

repo_path="/srv/git/xingmang-platform.git"
checkout_path="/srv/deploy/xingmang-platform"
remote_name="origin"
source_ref="refs/heads/release/v0.1-launch"
status_dir="/srv/ci"
audit_log="/srv/audit/xingmang-deploy.log"
confirm_token=""
reason=""
dry_run=0
test_mode=0
notify_hook=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --repo) [ "$#" -ge 2 ] || { usage; exit 2; }; repo_path="$2"; shift 2 ;;
    --repo=*) repo_path="$(printf '%s' "$1" | cut -d= -f2-)"; shift ;;
    --checkout) [ "$#" -ge 2 ] || { usage; exit 2; }; checkout_path="$2"; shift 2 ;;
    --checkout=*) checkout_path="$(printf '%s' "$1" | cut -d= -f2-)"; shift ;;
    --remote) [ "$#" -ge 2 ] || { usage; exit 2; }; remote_name="$2"; shift 2 ;;
    --remote=*) remote_name="$(printf '%s' "$1" | cut -d= -f2-)"; shift ;;
    --source-ref) [ "$#" -ge 2 ] || { usage; exit 2; }; source_ref="$2"; shift 2 ;;
    --source-ref=*) source_ref="$(printf '%s' "$1" | cut -d= -f2-)"; shift ;;
    --status-dir) [ "$#" -ge 2 ] || { usage; exit 2; }; status_dir="$2"; shift 2 ;;
    --status-dir=*) status_dir="$(printf '%s' "$1" | cut -d= -f2-)"; shift ;;
    --audit-log) [ "$#" -ge 2 ] || { usage; exit 2; }; audit_log="$2"; shift 2 ;;
    --audit-log=*) audit_log="$(printf '%s' "$1" | cut -d= -f2-)"; shift ;;
    --confirm) [ "$#" -ge 2 ] || { usage; exit 2; }; confirm_token="$2"; shift 2 ;;
    --confirm=*) confirm_token="$(printf '%s' "$1" | cut -d= -f2-)"; shift ;;
    --reason) [ "$#" -ge 2 ] || { usage; exit 2; }; reason="$2"; shift 2 ;;
    --reason=*) reason="$(printf '%s' "$1" | cut -d= -f2-)"; shift ;;
    --notify-hook) [ "$#" -ge 2 ] || { usage; exit 2; }; notify_hook="$2"; shift 2 ;;
    --notify-hook=*) notify_hook="$(printf '%s' "$1" | cut -d= -f2-)"; shift ;;
    --dry-run) dry_run=1; shift ;;
    --test-mode) test_mode=1; shift ;;
    -h|--help) usage >&1; exit 0 ;;
    *) echo "PROMOTE FAIL: 未知参数 $1" >&2; usage; exit 2 ;;
  esac
done

validate_path() {
  local label="$1" value="$2"
  [ -n "$value" ] || { echo "PROMOTE FAIL: $label 不能为空" >&2; return 1; }
  case "$value" in /*) ;; *) echo "PROMOTE FAIL: $label 必须是绝对路径" >&2; return 1 ;; esac
  case "$value" in *[!A-Za-z0-9_./-]*) echo "PROMOTE FAIL: $label 含非法字符" >&2; return 1 ;; esac
  case "/$value/" in */../*|*/./*) echo "PROMOTE FAIL: $label 不得包含 . 或 .. 路径段" >&2; return 1 ;; esac
  [ "$value" != "/" ] || { echo "PROMOTE FAIL: $label 不能是根目录" >&2; return 1; }
}
validate_path repo "$repo_path" || exit 1
validate_path checkout "$checkout_path" || exit 1
validate_path status-dir "$status_dir" || exit 1
validate_path audit-log "$audit_log" || exit 1
[[ "$remote_name" =~ ^[A-Za-z0-9._/-]+$ ]] || { echo "PROMOTE FAIL: remote 非法" >&2; exit 1; }
if [ "$test_mode" -eq 1 ]; then
  [ "${XM_DEPLOY_TEST_MODE:-0}" = "1" ] || { echo "PROMOTE FAIL: --test-mode 需要 XM_DEPLOY_TEST_MODE=1" >&2; exit 1; }
  for test_path in "$repo_path" "$checkout_path" "$status_dir" "$audit_log"; do
    protected_path="$(readlink -m -- "$test_path" 2>/dev/null)" || exit 1
    case "$protected_path" in
      /srv|/srv/*) echo "PROMOTE FAIL: test-mode 禁止使用 /srv 路径" >&2; exit 1 ;;
    esac
  done
fi
if [ "$test_mode" -eq 0 ]; then
  export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
fi
if [ "$test_mode" -eq 0 ]; then
  [ "$repo_path" = "/srv/git/xingmang-platform.git" ] || { echo "PROMOTE FAIL: repo 不在受控服务器路径" >&2; exit 1; }
  [ "$checkout_path" = "/srv/deploy/xingmang-platform" ] || { echo "PROMOTE FAIL: checkout 不在受控服务器路径" >&2; exit 1; }
  [ "$status_dir" = "/srv/ci" ] || { echo "PROMOTE FAIL: status-dir 不在受控 CI 路径" >&2; exit 1; }
  case "$audit_log" in /srv/audit/*) ;; *) echo "PROMOTE FAIL: audit-log 不在受控审计路径" >&2; exit 1 ;; esac
  [ "$remote_name" = "origin" ] || { echo "PROMOTE FAIL: remote 只能是 origin" >&2; exit 1; }
  if [ -n "$notify_hook" ]; then
    [ "$notify_hook" = "/usr/local/libexec/xingmang-deploy-notify" ] || {
      echo "PROMOTE FAIL: 生产通知适配器路径不可覆盖" >&2; exit 1;
    }
  fi
fi
[ "$source_ref" = "refs/heads/release/v0.1-launch" ] || {
  echo "PROMOTE FAIL: source-ref 只能是 refs/heads/release/v0.1-launch" >&2; exit 1;
}
if [ "$dry_run" -eq 0 ]; then
  [ "$confirm_token" = "PROMOTE-PRODUCTION" ] || {
    echo "PROMOTE FAIL: 必须提供 --confirm PROMOTE-PRODUCTION" >&2; exit 1;
  }
fi
printf '%s\n' "$reason" | LC_ALL=C grep -Eq '^[A-Za-z0-9][A-Za-z0-9 ._:/,@+_-]{0,199}$' || {
  echo "PROMOTE FAIL: reason 必填且只能包含有限 ASCII 字符（<=200）" >&2; exit 1;
}
reason_lower="$(printf '%s' "$reason" | tr '[:upper:]' '[:lower:]')"
case "$reason_lower" in
  *password*|*passwd*|*token*|*secret*|*credential*|*dsn*|*begin*)
    echo "PROMOTE FAIL: reason 不能包含凭据/密钥内容" >&2; exit 1 ;;
esac
reason_audit="${reason// /_}"

actor="$(id -un 2>/dev/null || true)"
[[ "$actor" =~ ^[A-Za-z0-9._@-]+$ ]] || actor="unknown"
[ -d "$repo_path" ] && [ ! -L "$repo_path" ] && [ -f "$repo_path/HEAD" ] || {
  echo "PROMOTE FAIL: repo 必须是可验证的 bare repository" >&2; exit 1;
}
resolved_repo="$(readlink -f -- "$repo_path" 2>/dev/null || true)"
[ "$resolved_repo" = "$repo_path" ] || { echo "PROMOTE FAIL: repo 路径解析后越界或不可验证" >&2; exit 1; }
git --git-dir="$repo_path" rev-parse --is-bare-repository 2>/dev/null | grep -qx true || {
  echo "PROMOTE FAIL: repo 不是 bare repository" >&2; exit 1;
}
[ -d "$checkout_path" ] && [ ! -L "$checkout_path" ] || {
  echo "PROMOTE FAIL: checkout 不存在或是符号链接" >&2; exit 1;
}
resolved_checkout="$(readlink -f -- "$checkout_path" 2>/dev/null || true)"
[ "$resolved_checkout" = "$checkout_path" ] || { echo "PROMOTE FAIL: checkout 路径解析后越界或不可验证" >&2; exit 1; }
checkout_top="$(git -C "$checkout_path" rev-parse --show-toplevel 2>/dev/null)" || {
  echo "PROMOTE FAIL: checkout 不是 Git checkout" >&2; exit 1;
}
[ "$(cd -- "$checkout_top" 2>/dev/null && pwd -P)" = "$resolved_checkout" ] || {
  echo "PROMOTE FAIL: checkout must be the actual Git top-level" >&2; exit 1;
}
git -C "$checkout_path" rev-parse --is-shallow-repository 2>/dev/null | grep -qx false || {
  echo "PROMOTE FAIL: 拒绝在 shallow checkout 晋级" >&2; exit 1;
}
remote_url="$(git -C "$checkout_path" remote get-url "$remote_name" 2>/dev/null || true)"
[ -n "$remote_url" ] || { echo "PROMOTE FAIL: Git remote 不存在或没有 URL: $remote_name" >&2; exit 1; }
push_urls="$(git -C "$checkout_path" config --get-all "remote.$remote_name.pushurl" 2>/dev/null || true)"
if [ -n "$push_urls" ] && [ "$test_mode" -eq 0 ]; then
  echo "PROMOTE FAIL: 生产 remote 不允许单独 pushurl 重定向" >&2
  exit 1
fi
case "$remote_url" in
  http://*|https://*|ssh://*)
    remote_authority="${remote_url#*://}"
    case "$remote_authority" in *'@'*) echo "PROMOTE FAIL: Git remote URL 不得包含 userinfo" >&2; exit 1 ;; esac
    ;;
  *' '*|*$'\n'*) echo "PROMOTE FAIL: Git remote URL 含空白" >&2; exit 1 ;;
esac
expected_remote="$repo_path"
case "$remote_url" in
  file://*) remote_compare="${remote_url#file://}" ;;
  [A-Za-z]:/*) remote_compare="$(cygpath -u "$remote_url" 2>/dev/null || printf '%s' "$remote_url")" ;;
  *) remote_compare="$remote_url" ;;
esac
if command -v cygpath >/dev/null 2>&1; then
  expected_remote="$(cygpath -u "$expected_remote" 2>/dev/null || printf '%s' "$expected_remote")"
fi
[ -n "$notify_hook" ] && {
  validate_path notify-hook "$notify_hook" || exit 1
  [ -x "$notify_hook" ] && [ ! -L "$notify_hook" ] || {
    echo "PROMOTE FAIL: notify-hook 不可执行或是符号链接" >&2; exit 1;
  }
  notify_mode="$(stat -c '%a' "$notify_hook" 2>/dev/null || true)"
  if [ "$test_mode" -eq 0 ] && [[ "$notify_mode" =~ ^[0-7]+$ ]] && (( 8#$notify_mode & 022 )); then
    echo "PROMOTE FAIL: 生产 notify-hook 不得可被组/其他用户写入" >&2; exit 1;
  fi
}
[ "$remote_compare" = "$expected_remote" ] || {
  echo "PROMOTE FAIL: Git remote 未指向指定 bare repo" >&2; exit 1;
}
if [ -n "$push_urls" ]; then
  while IFS= read -r push_url; do
    [ -n "$push_url" ] || continue
    case "$push_url" in file://*) push_compare="${push_url#file://}" ;; [A-Za-z]:/*) push_compare="$(cygpath -u "$push_url" 2>/dev/null || printf '%s' "$push_url")" ;; *) push_compare="$push_url" ;; esac
    [ "$push_compare" = "$expected_remote" ] || {
      echo "PROMOTE FAIL: pushurl 未指向指定 bare repo" >&2; exit 1;
    }
  done <<< "$push_urls"
fi
hook_path="$repo_path/hooks/pre-receive"
[ -f "$hook_path" ] && [ ! -L "$hook_path" ] && [ -x "$hook_path" ] || {
  echo "PROMOTE FAIL: bare repo 缺少可信 pre-receive hook" >&2; exit 1;
}
git --git-dir="$repo_path" config --get receive.denyNonFastForwards | grep -qx true || {
  echo "PROMOTE FAIL: bare repo 未启用 denyNonFastForwards" >&2; exit 1;
}
git --git-dir="$repo_path" config --get receive.denyDeletes | grep -qx true || {
  echo "PROMOTE FAIL: bare repo 未启用 denyDeletes" >&2; exit 1;
}
trusted_hook="$checkout_path/deploy/git-hooks/pre-receive"
[ -f "$trusted_hook" ] && [ ! -L "$trusted_hook" ] && [ -x "$trusted_hook" ] || {
  echo "PROMOTE FAIL: checkout 缺少版本化 pre-receive hook" >&2; exit 1;
}
command -v sha256sum >/dev/null 2>&1 || { echo "PROMOTE FAIL: 缺少 sha256sum" >&2; exit 1; }
# Git for Windows 可能按 core.autocrlf 检出 CRLF；校验前只规范行尾，
# 仍逐字校验脚本内容，避免本地验证因换行格式产生假阴性。
hook_sha="$(sed 's/\r$//' "$hook_path" | sha256sum | awk '{print $1}')"
trusted_hook_sha="$(sed 's/\r$//' "$trusted_hook" | sha256sum | awk '{print $1}')"
[ "$hook_sha" = "$trusted_hook_sha" ] || {
  echo "PROMOTE FAIL: bare hook 与版本化可信 hook 哈希不一致" >&2; exit 1;
}

marker_path="$(git --git-dir="$repo_path" config --get xm.receive.promoteMarker 2>/dev/null || true)"
[ -n "$marker_path" ] || marker_path="$repo_path/xm-promote.marker"
if [[ "$marker_path" =~ ^[A-Za-z]:/ ]] && command -v cygpath >/dev/null 2>&1; then
  marker_path="$(cygpath -u "$marker_path")"
fi
validate_path marker "$marker_path" || exit 1
case "$marker_path" in "$repo_path"/*) ;; *) echo "PROMOTE FAIL: marker 必须位于 bare repo 内" >&2; exit 1 ;; esac
marker_parent="$(dirname -- "$marker_path")"
[ -d "$marker_parent" ] && [ ! -L "$marker_parent" ] || {
  echo "PROMOTE FAIL: marker 父目录不存在或是符号链接" >&2; exit 1;
}
resolved_marker_parent="$(readlink -f -- "$marker_parent" 2>/dev/null || true)"
[ "$resolved_marker_parent" = "$marker_parent" ] || {
  echo "PROMOTE FAIL: marker 父目录解析后越界或不可验证" >&2; exit 1;
}

source_sha=""
marker_lock=""
marker_lock_owned=0
audit_written=0

cleanup_marker_lock() {
  local file expected
  if [ "$marker_lock_owned" -eq 1 ]; then
    # Only remove this invocation's two files. A replaced/unknown lock is retained.
    [ -d "$marker_lock" ] && [ ! -L "$marker_lock" ] || return 0
    for file in pid sha; do
      expected="$source_sha"
      [ "$file" != pid ] || expected="$$"
      if [ -e "$marker_lock/$file" ] || [ -L "$marker_lock/$file" ]; then
        [ -f "$marker_lock/$file" ] && [ ! -L "$marker_lock/$file" ] &&
          [ "$(cat -- "$marker_lock/$file" 2>/dev/null)" = "$expected" ] || return 0
      fi
    done
    rm -f -- "$marker_lock/pid" "$marker_lock/sha" 2>/dev/null || return 0
    rmdir -- "$marker_lock" 2>/dev/null || return 0
    marker_lock_owned=0
  fi
}
trap cleanup_marker_lock EXIT

if [ "$dry_run" -eq 0 ]; then
  audit_parent="$(dirname -- "$audit_log")"
  mkdir -p -- "$status_dir" "$audit_parent"
  [ ! -L "$status_dir" ] && [ ! -L "$audit_parent" ] || {
    echo "PROMOTE FAIL: status/audit 父目录不能是符号链接" >&2; exit 1;
  }
  resolved_status="$(readlink -f -- "$status_dir" 2>/dev/null || true)"
  resolved_audit_parent="$(readlink -f -- "$audit_parent" 2>/dev/null || true)"
  [ "$resolved_status" = "$status_dir" ] && [ "$resolved_audit_parent" = "$audit_parent" ] || {
    echo "PROMOTE FAIL: status/audit 路径解析后越界或不可验证" >&2; exit 1;
  }
fi

append_audit() {
  local result="$1" sha="$2" lock tmp mode previous_hash canonical_line entry_hash
  [ "$dry_run" -eq 0 ] || return 0
  [ ! -L "$audit_log" ] || { echo "PROMOTE FAIL: audit-log 不能是符号链接" >&2; return 1; }
  if [ -e "$audit_log" ]; then
    mode="$(stat -c '%a' "$audit_log" 2>/dev/null || true)"
    if [[ "$mode" =~ ^[0-7]+$ ]] && (( 8#$mode & 077 )); then
      echo "PROMOTE FAIL: audit-log 必须是 owner-only" >&2; return 1
    fi
    if [ -s "$audit_log" ]; then
      audit_last_byte="$(tail -c 1 "$audit_log" 2>/dev/null | od -An -t x1 | tr -d ' \r\n')"
      [ "$audit_last_byte" = "0a" ] || { echo "PROMOTE FAIL: audit-log 尾部必须以换行结束" >&2; return 1; }
      audit_last_line="$(tail -n 1 "$audit_log")"
      printf '%s\n' "$audit_last_line" | LC_ALL=C grep -Eq '(^| )entry_sha256=[0-9a-fA-F]{64}$' || {
        echo "PROMOTE FAIL: audit-log 末行缺少有效 entry_sha256" >&2; return 1;
      }
      audit_entry="${audit_last_line##* entry_sha256=}"
      audit_canonical="${audit_last_line% entry_sha256=*}"
      [ "$(printf '%s\n' "$audit_canonical" | sha256sum | awk '{print $1}')" = "$audit_entry" ] || {
        echo "PROMOTE FAIL: audit-log 末行 entry_sha256 校验失败" >&2; return 1;
      }
      printf '%s\n' "$audit_canonical" | LC_ALL=C grep -Eq '(^| )prev_sha256=(GENESIS|[0-9a-fA-F]{64})$' || {
        echo "PROMOTE FAIL: audit-log 末行 prev_sha256 格式非法" >&2; return 1;
      }
    fi
  else
    : > "$audit_log" || return 1
    chmod 0600 "$audit_log" || return 1
  fi
  lock="$audit_log.lock"
  if ! mkdir -- "$lock" 2>/dev/null; then
    echo "PROMOTE FAIL: audit-log 正在被另一个晋级占用" >&2; return 1
  fi
  resolved_audit="$(readlink -f -- "$audit_log" 2>/dev/null || true)"
  [ "$resolved_audit" = "$audit_log" ] || {
    rmdir "$lock" 2>/dev/null || true
    echo "PROMOTE FAIL: audit-log 解析后越界或是符号链接" >&2; return 1;
  }
  tmp="$(mktemp -- "$audit_log.tmp.XXXXXX")" || { rmdir "$lock" 2>/dev/null || true; return 1; }
  previous_hash="GENESIS"
  if [ -s "$audit_log" ]; then
    previous_hash="$(tail -n 1 "$audit_log" | sha256sum | awk '{print $1}')"
  fi
  canonical_line="$(printf 'ts=%s actor=%s environment=production source_ref=%s target_ref=refs/heads/main sha=%s result=%s reason=%s prev_sha256=%s' \
    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$actor" "$source_ref" "$sha" "$result" "$reason_audit" "$previous_hash")"
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
  if ! env -i PATH=/usr/bin:/bin HOME=/nonexistent LANG=C LC_ALL=C \
    "$notify_hook" < <(printf 'environment=production\nsource_ref=%s\ntarget_ref=refs/heads/main\nsha=%s\nresult=%s\n' \
      "$source_ref" "$sha" "$result"); then
    echo "PROMOTE WARN: 通知适配器失败（晋级结果不回滚）" >&2
  fi
}

read_green_status() {
  local file="$status_dir/$source_sha.status" last
  [ -f "$file" ] && [ ! -L "$file" ] || { echo "PROMOTE FAIL: CI status 缺失/符号链接" >&2; return 1; }
  [ "$(readlink -f -- "$file" 2>/dev/null || true)" = "$file" ] || {
    echo "PROMOTE FAIL: CI status 解析后越界或不可验证" >&2; return 1;
  }
  awk 'NR == 1 && $0 == "green" { ok=1; next } { bad=1 } END { exit !(ok && !bad) }' "$file" || {
    echo "PROMOTE FAIL: CI status 不是严格单行 green" >&2; return 1;
  }
  last="$(tail -c 1 "$file" 2>/dev/null | od -An -t x1 | tr -d ' \r\n')"
  [ "$last" = "0a" ] || { echo "PROMOTE FAIL: CI status 必须以换行结束" >&2; return 1; }
}

write_marker() {
  local tmp_marker
  [ ! -L "$marker_path" ] || { echo "PROMOTE FAIL: marker 是符号链接" >&2; return 1; }
  if [ -e "$marker_path" ]; then
    marker_mode="$(stat -c '%a' "$marker_path" 2>/dev/null || true)"
    if [[ "$marker_mode" =~ ^[0-7]+$ ]] && (( 8#$marker_mode & 077 )); then
      echo "PROMOTE FAIL: marker 必须是 owner-only" >&2; return 1
    fi
    awk -v expected="sha=$source_sha" 'NR == 1 && $0 == expected { ok=1; next } { bad=1 } END { exit !(ok && !bad) }' "$marker_path" || {
      echo "PROMOTE FAIL: 已有 marker 指向不同提交" >&2; return 1;
    }
    marker_last="$(tail -c 1 "$marker_path" 2>/dev/null | od -An -t x1 | tr -d ' \r\n')"
    [ "$marker_last" = "0a" ] || { echo "PROMOTE FAIL: 已有 marker 未以换行结束" >&2; return 1; }
    return 0
  fi
  tmp_marker="$(mktemp -- "$marker_path.tmp.XXXXXX")" || return 1
  printf 'sha=%s\n' "$source_sha" > "$tmp_marker" || { rm -f -- "$tmp_marker"; return 1; }
  chmod 0600 "$tmp_marker" || { rm -f -- "$tmp_marker"; return 1; }
  mv -f -- "$tmp_marker" "$marker_path"
}

consume_marker_if_matching() {
  [ -e "$marker_path" ] || return 0
  [ ! -L "$marker_path" ] || { echo "PROMOTE FAIL: marker 变成符号链接" >&2; return 1; }
  awk -v expected="sha=$source_sha" 'NR == 1 && $0 == expected { ok=1; next } { bad=1 } END { exit !(ok && !bad) }' "$marker_path" || {
    echo "PROMOTE FAIL: marker 内容与晋级 SHA 不一致" >&2; return 1;
  }
  marker_last="$(tail -c 1 "$marker_path" 2>/dev/null | od -An -t x1 | tr -d ' \r\n')"
  [ "$marker_last" = "0a" ] || { echo "PROMOTE FAIL: marker 未以换行结束" >&2; return 1; }
  marker_mode="$(stat -c '%a' "$marker_path" 2>/dev/null || true)"
  if [[ "$marker_mode" =~ ^[0-7]+$ ]] && (( 8#$marker_mode & 077 )); then
    echo "PROMOTE FAIL: marker 必须是 owner-only" >&2; return 1
  fi
  rm -f -- "$marker_path"
}

main() {
  if [ "$dry_run" -eq 1 ]; then
    echo "DRY-RUN: source=$source_ref target=refs/heads/main repo=$repo_path"
    return 0
  fi
  git -C "$checkout_path" diff --quiet --exit-code || return 1
  git -C "$checkout_path" diff --cached --quiet --exit-code || return 1
  worktree_status=""
  if ! worktree_status="$(git -C "$checkout_path" status --porcelain --untracked-files=all 2>/dev/null)"; then
    return 1
  fi
  [ -z "$worktree_status" ] || return 1
  git -C "$checkout_path" fetch --no-tags "$remote_name" "$source_ref" >/dev/null 2>&1 || return 1
  source_sha="$(git -C "$checkout_path" rev-parse FETCH_HEAD 2>/dev/null)" || return 1
  [[ "$source_sha" =~ ^[0-9a-fA-F]{40}$ ]] || return 1
  [ "$(git -C "$checkout_path" cat-file -t "$source_sha" 2>/dev/null)" = "commit" ] || return 1
  [ "$(git --git-dir="$repo_path" rev-parse "$source_ref" 2>/dev/null)" = "$source_sha" ] || return 1
  read_green_status || return 1
  old_sha="$(git --git-dir="$repo_path" rev-parse refs/heads/main 2>/dev/null)" || return 1
  [[ "$old_sha" =~ ^[0-9a-fA-F]{40}$ ]] || return 1
  git --git-dir="$repo_path" merge-base --is-ancestor "$old_sha" "$source_sha" || {
    echo "PROMOTE FAIL: source 不是 main 的快进后代" >&2; return 1;
  }

  # pre-receive 自己会短暂占用 `$marker_path.lock`；脚本不能抢同一个锁，
  # 否则 hook 会把这次合法晋级误判成并发 receive。脚本使用独立锁，
  # 让 marker 的 hook 锁仍由 hook 独占。
  marker_lock="$marker_path.promote.lock"
  if ! mkdir -- "$marker_lock" 2>/dev/null; then
    echo "PROMOTE FAIL: marker 正在被另一个晋级占用" >&2
    return 75
  fi
  marker_lock_owned=1
  # 安装器开启 xm.receive.requirePromoteLock 时，hook 会检查这两个文件：
  # PID 必须仍对应本进程的 promote.sh，SHA 必须与 marker/main 目标一致。
  printf '%s\n' "$$" > "$marker_lock/pid" || return 1
  printf '%s\n' "$source_sha" > "$marker_lock/sha" || return 1
  chmod 0600 "$marker_lock/pid" "$marker_lock/sha" || return 1
  write_marker || return 1
  # 只通过 push 触发 bare-repo pre-receive；不使用 update-ref/force。
  git -C "$checkout_path" push --no-follow-tags "$remote_name" \
    "$source_sha:refs/heads/main" >/dev/null 2>&1 || {
      # push 失败时只清理仍然匹配本次 SHA 的 marker；若 hook 已消费则不猜测。
      consume_marker_if_matching || true
      return 1
    }
  [ "$(git --git-dir="$repo_path" rev-parse refs/heads/main 2>/dev/null)" = "$source_sha" ] || return 1
  consume_marker_if_matching || return 1
  cleanup_marker_lock
  return 0
}

if main; then
  if [ "$dry_run" -eq 0 ]; then
    append_audit green "$source_sha" || exit 1
    notify_result green "$source_sha"
  fi
  if [ -n "$source_sha" ]; then display_sha="$source_sha"; else display_sha="dry-run"; fi
  echo "PROMOTE PASS: source_sha=$display_sha → refs/heads/main"
  exit 0
else
  rc=$?
fi
if [ "$dry_run" -eq 0 ] && [ -n "$source_sha" ] && [ "$audit_written" -eq 0 ]; then
  append_audit red "$source_sha" || true
  notify_result red "$source_sha"
fi
if [ -n "$source_sha" ]; then display_sha="$source_sha"; else display_sha="unknown"; fi
echo "PROMOTE FAIL: source_sha=$display_sha（未绕过 receive hook）" >&2
exit "$rc"
