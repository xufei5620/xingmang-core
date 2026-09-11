#!/usr/bin/env bash
# 将 SOPS/age 加密的凭据包解密为 FileProvider 嵌套布局（<root>/<scope>/<name>）。
# 用法: decrypt-secrets.sh <environment>   （development|staging|production）
# 前置: 安装 sops 与 age；SOPS_AGE_KEY_FILE 指向离线私钥。
# 本脚本是 Foundation-A 骨架：deploy/secrets/<env>.enc.yaml 存在后即可使用。
set -euo pipefail

env_name="${1:?用法: decrypt-secrets.sh <environment>}"
case "$env_name" in development|staging|production) ;; *)
  echo "非法 environment: $env_name" >&2; exit 1 ;;
esac

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
enc_file="$repo_root/deploy/secrets/${env_name}.enc.yaml"
out_root="$repo_root/var/secrets/${env_name}"

[ -f "$enc_file" ] || { echo "缺少加密文件: $enc_file" >&2; exit 1; }
command -v sops >/dev/null || { echo "需要安装 sops" >&2; exit 1; }
python3 -c 'import yaml' 2>/dev/null || { echo "需要 python3 + pyyaml（pip install pyyaml）" >&2; exit 1; }

umask 077
out_parent="$(dirname -- "$out_root")"
mkdir -p -- "$out_parent"
[ ! -L "$out_root" ] && { [ ! -e "$out_root" ] || [ -d "$out_root" ]; } || {
  echo "解密目标必须是普通目录" >&2; exit 1;
}
lock_dir="$out_parent/.${env_name}.decrypt.lock"
mkdir -- "$lock_dir" 2>/dev/null || { echo "同一环境正在解密，停止" >&2; exit 75; }
stage=""
cleanup() {
  local rc="$?"
  if [ -n "$stage" ]; then
    if [ -d "$stage/previous" ] && [ ! -e "$out_root" ]; then
      if ! mv -T -- "$stage/previous" "$out_root"; then
        echo "恢复原输出失败；原目录保留在 $stage/previous" >&2
        rmdir -- "$lock_dir" 2>/dev/null || true
        exit 1
      fi
    fi
    # Do not discard the previous directory after an interrupted publication.
    if [ "$rc" -ne 0 ] && [ -d "$stage/previous" ]; then
      echo "原输出备份保留在 $stage/previous" >&2
    else
      rm -rf -- "$stage"
    fi
  fi
  rmdir -- "$lock_dir" 2>/dev/null || true
  exit "$rc"
}
trap cleanup EXIT
stage="$(mktemp -d -- "$out_parent/.${env_name}.staging.XXXXXX")"
mkdir -- "$stage/output"
sops -d "$enc_file" > "$stage/payload.yaml"

# 加密 YAML 结构约定（两层：scope → name → 值）:
#   sub2api-prod:
#     read-only-admin: "<值>"
#   alerting:
#     telegram-primary: "<值>"
python3 - "$stage/output" 3< "$stage/payload.yaml" <<'PY'
import os
import re
import sys

import yaml

# 与 CredentialRef 同一字符集（internal/platform/secrets/ref.go、
# contracts/connectors/credential-ref.v1.md）。防止被投毒的加密包
# 借 scope/name 做路径穿越（红队发现 XM-R001 / Issue #15）。
PART = re.compile(r"^[a-z0-9][a-z0-9-]{0,63}$")

out_root = os.path.realpath(sys.argv[1])
with os.fdopen(3, "r", encoding="utf-8") as payload:
    data = yaml.safe_load(payload) or {}
if not isinstance(data, dict):
    raise SystemExit("加密包顶层必须是 scope→(name→值) 映射")

for scope, entries in data.items():
    if not isinstance(scope, str) or not PART.match(scope):
        raise SystemExit(f"非法 scope {scope!r}：须匹配 ^[a-z0-9][a-z0-9-]{{0,63}}$")
    if not isinstance(entries, dict):
        raise SystemExit(f"scope {scope!r} 下必须是 name→值 映射")
    scope_dir = os.path.join(out_root, scope)
    os.makedirs(scope_dir, mode=0o700, exist_ok=True)
    for name, value in entries.items():
        if not isinstance(name, str) or not PART.match(name):
            raise SystemExit(f"非法 name {name!r}（scope {scope!r}）：须匹配 ^[a-z0-9][a-z0-9-]{{0,63}}$")
        p = os.path.join(scope_dir, name)
        # 双保险：即便正则被绕过，也拒绝落在 out_root 之外的路径
        if os.path.commonpath([out_root, os.path.realpath(os.path.dirname(p))]) != out_root:
            raise SystemExit(f"拒绝写出 out_root 之外的路径: {p}")
        with open(p, "w", encoding="utf-8") as f:
            f.write(str(value))
        os.chmod(p, 0o600)

PY

# All entries were validated and written before touching the previous generation.
if [ -d "$out_root" ]; then mv -T -- "$out_root" "$stage/previous"; fi
mv -T -- "$stage/output" "$out_root"
echo "decrypted -> $out_root"
