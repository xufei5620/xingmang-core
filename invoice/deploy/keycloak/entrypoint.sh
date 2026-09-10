#!/bin/bash
set -euo pipefail

# The reverse proxy reaches this container through one Docker bridge gateway.
# Trusting a subnet or a list would let another peer supply forged forwarding
# headers, so production accepts one canonical IPv4 host route only.
source /opt/keycloak/bin/invoice-validate-proxy-trust.sh
validate_keycloak_proxy_trusted_addresses

read_secret() {
  local path="$1"
  test -f "$path" && test -s "$path" || return 1
  local value
  value=$(cat "$path") || return $?
  case "$value" in
    *$'\n'*|*$'\r'*|'') echo "invalid secret file: $path" >&2; exit 1 ;;
  esac
  printf '%s' "$value"
}

KC_DB_PASSWORD="$(read_secret /run/secrets/keycloak_app_db_password)" || exit $?
export KC_DB_PASSWORD

bootstrap_secret=/run/secrets/keycloak_bootstrap_admin_password
if [[ -e "$bootstrap_secret" ]]; then
  : "${KC_BOOTSTRAP_ADMIN_USERNAME:?bootstrap override must set KC_BOOTSTRAP_ADMIN_USERNAME}"
  KC_BOOTSTRAP_ADMIN_PASSWORD="$(read_secret "$bootstrap_secret")" || exit $?
  export KC_BOOTSTRAP_ADMIN_PASSWORD
else
  unset KC_BOOTSTRAP_ADMIN_USERNAME KC_BOOTSTRAP_ADMIN_PASSWORD
fi

exec /opt/keycloak/bin/kc.sh start --optimized "$@"
