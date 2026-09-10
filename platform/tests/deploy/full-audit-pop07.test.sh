#!/usr/bin/env bash
# POP-07: env parsing and smoke-route selection only; no production lifecycle.
set -Eeuo pipefail
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
script="${AUDIT_SOURCE:-$root/deploy/scripts/deploy-local.sh}"
work="$(mktemp -d)"
for fn in trim read_env_value; do sed -n "/^$fn() {$/,/^}$/p" "$script"; done > "$work/functions.sh"
sed -n '/^auth_mode_local=0$/,/^if \[ "$auth_mode_local" -eq 1 \]; then$/p' "$script" | sed '$d' > "$work/select.sh"
fail=0
while IFS='|' read -r name environment wanted line; do
 printf '%s\n' "$line" > "$work/input.env"
 got="$(env_file="$work/input.env"; expected_environment="$environment"; source "$work/functions.sh"; source "$work/select.sh"; printf '%s' "$auth_mode_local")"
 if [ "$got" != "$wanted" ]; then echo "FAIL POP-07: $name selected $got expected $wanted" >&2; fail=1; fi
done <<'CASES'
bare|staging|1|XM_AUTH_MODE=local
quoted|staging|1|XM_AUTH_MODE="local"
comment|staging|1|XM_AUTH_MODE=local # fixture
production-default|production|1|
staging-default|staging|0|
explicit-oidc|production|0|XM_AUTH_MODE=oidc
CASES
[ "$fail" = 0 ] || exit 1
echo 'PASS POP-07: smoke follows parsed auth mode and existing defaults'
echo "fixture=$work"
