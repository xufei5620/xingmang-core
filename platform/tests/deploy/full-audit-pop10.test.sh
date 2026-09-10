#!/usr/bin/env bash
# POP-10: parameter checks only, stop before filesystem/deployment checks.
set -Eeuo pipefail
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
script="${AUDIT_SOURCE:-$root/deploy/scripts/promote.sh}"
work="$(mktemp -d)"
sed '/^if \[ "$test_mode" -eq 0 \]; then$/,$d' "$script" > "$work/prefix.sh"
args=(--test-mode --repo "$work/repo.git" --checkout "$work/checkout" --status-dir "$work/status" --audit-log "$work/audit")
if ! XM_DEPLOY_TEST_MODE=1 bash "$work/prefix.sh" "${args[@]}"; then echo 'FAIL POP-10: isolated paths rejected' >&2; exit 1; fi
fail=0
for flag in repo checkout status-dir audit-log; do
 for protected in /srv/fixture //srv/fixture; do
  if XM_DEPLOY_TEST_MODE=1 bash "$work/prefix.sh" "${args[@]}" "--$flag" "$protected" > "$work/rejection.log" 2>&1; then
   echo "FAIL POP-10: protected $flag $protected accepted" >&2; fail=1
  fi
 done
done
[ "$fail" = 0 ] || exit 1
echo 'PASS POP-10: all test-mode targets exclude protected paths'
