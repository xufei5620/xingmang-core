#!/usr/bin/env bash
# 安全测试：decrypt-secrets.sh 的 scope/name 必须按 CredentialRef 字符集校验，
# 拒绝路径穿越（红队发现 XM-R001 / Issue #15）。
# 用法: bash tests/security/decrypt-secrets-path.test.sh
set -uo pipefail
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
script="$repo_root/deploy/scripts/decrypt-secrets.sh"
fail=0
err() { echo "SECURITY TEST FAIL: $*" >&2; fail=1; }

command -v python3 >/dev/null || { echo "SKIP: 无 python3"; exit 0; }
python3 -c 'import yaml' 2>/dev/null || { echo "SKIP: 无 pyyaml"; exit 0; }

# 提取脚本中的 python 解析块（heredoc PY ... PY），单独喂恶意 YAML 验证。
extract_py() { sed -n "/<<'PY'/,/^PY$/p" "$script" | sed "1d;\$d"; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
py="$work/parse.py"
extract_py > "$py"
out_root="$work/out"
mkdir -p "$out_root"

run_case() {
  local desc="$1" yaml="$2"
  local rc
  printf '%s' "$yaml" | python3 "$py" "$out_root" 3<&0 >/dev/null 2>&1
  rc=$?
  if [ "$rc" -eq 0 ]; then
    err "$desc：非法 scope/name 被接受（应非零退出）"
  fi
  # 无论退出码如何，out_root 之外不得出现文件
  if find "$work" -mindepth 1 -maxdepth 1 ! -name out ! -name parse.py | grep -q .; then
    err "$desc：写出了 out_root 之外的文件"
    find "$work" -mindepth 1 -maxdepth 1 ! -name out ! -name parse.py >&2
  fi
}

run_case "scope 路径穿越" '../../xm-pwn:
  hijack: test-value
'
run_case "name 路径穿越" 'legit-scope:
  ../../xm-pwn-name: test-value
'
run_case "scope 绝对路径" '/etc:
  passwd: test-value
'
run_case "scope 大写非法字符" 'UPPER_scope:
  name: test-value
'
run_case "name 含斜杠" 'scope:
  sub/dir: test-value
'

# 合法输入必须成功且落在正确位置
printf 'sub2api-prod:\n  read-only-admin: test-value-ok\n' | python3 "$py" "$out_root" 3<&0 >/dev/null 2>&1 \
  || err "合法输入被拒绝"
[ -f "$out_root/sub2api-prod/read-only-admin" ] || err "合法输入未写到 <root>/<scope>/<name>"

[ "$fail" -eq 0 ] && echo "SECURITY-TEST-OK"
exit $fail
