#!/usr/bin/env bash
# F3: actual dry-run branch block only; no Git/Docker/HTTP operation.
set -Eeuo pipefail
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
script="${AUDIT_SOURCE:-$root/deploy/scripts/deploy-local.sh}"
work="$(mktemp -d)"
sed -n '/^if \[ "$dry_run" -eq 1 \]; then$/,/^fi$/p' "$script" > "$work/dry.sh"
run() (
 die() { echo "$*" >&2; exit 1; }
 dry_run=1; current_branch="$1"; current_sha=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
 expected_sha="$current_sha"; web_url=http://127.0.0.1:8088; build_services=(web)
 source "$work/dry.sh"
)
fail=0
if ! run release/v0.1-launch > "$work/release.log" 2>&1 || ! grep -Fq 'branch=release/v0.1-launch' "$work/release.log"; then
 echo 'FAIL F3: valid release dry-run failed' >&2; fail=1
fi
for branch in feature ''; do
 label="${branch:-detached}"
 if run "$branch" > "$work/$label.log" 2>&1 || ! grep -Fq "actual=$label" "$work/$label.log"; then
  echo "FAIL F3: wrong branch $label not rejected/reported" >&2; fail=1
 fi
done
[ "$fail" = 0 ] || exit 1
echo 'PASS F3: release admitted and actual wrong/detached branches rejected'
