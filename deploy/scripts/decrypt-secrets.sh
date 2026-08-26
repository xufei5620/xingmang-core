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
rm -rf "$out_root" && mkdir -p "$out_root"

# 加密 YAML 结构约定（两层：scope → name → 值）:
#   sub2api-prod:
#     read-only-admin: "<值>"
#   alerting:
#     telegram-primary: "<值>"
sops -d "$enc_file" | python3 - "$out_root" <<'PY'
import sys, os, yaml
out_root = sys.argv[1]
data = yaml.safe_load(sys.stdin) or {}
for scope, entries in data.items():
    if not isinstance(entries, dict):
        raise SystemExit(f"scope {scope!r} 下必须是 name→值 映射")
    os.makedirs(os.path.join(out_root, scope), mode=0o700, exist_ok=True)
    for name, value in entries.items():
        p = os.path.join(out_root, scope, name)
        with open(p, "w", encoding="utf-8") as f:
            f.write(str(value))
        os.chmod(p, 0o600)
print(f"decrypted -> {out_root}")
PY
