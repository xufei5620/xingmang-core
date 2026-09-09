#!/bin/bash
set -euo pipefail

validate_keycloak_proxy_trusted_addresses() {
  local value="${KC_PROXY_TRUSTED_ADDRESSES:-}"
  local octet
  local canonical=""

  [[ "$value" =~ ^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})/32$ ]] || {
    echo "KC_PROXY_TRUSTED_ADDRESSES must be exactly one canonical IPv4 /32" >&2
    return 1
  }

  for octet in "${BASH_REMATCH[@]:1:4}"; do
    ((10#$octet <= 255)) || {
      echo "KC_PROXY_TRUSTED_ADDRESSES contains an invalid IPv4 octet" >&2
      return 1
    }
    if [[ -n "$canonical" ]]; then canonical+="."; fi
    canonical+="$((10#$octet))"
  done
  canonical+="/32"

  [[ "$value" == "$canonical" ]] || {
    echo "KC_PROXY_TRUSTED_ADDRESSES must use canonical IPv4 notation" >&2
    return 1
  }

  [[ "${KEYCLOAK_EDGE_GATEWAY:-}" == "${canonical%/32}" ]] || {
    echo "KC_PROXY_TRUSTED_ADDRESSES must equal KEYCLOAK_EDGE_GATEWAY/32" >&2
    return 1
  }
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  validate_keycloak_proxy_trusted_addresses
fi
