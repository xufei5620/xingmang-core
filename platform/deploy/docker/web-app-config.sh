#!/bin/sh
# Unified console uses the platform's own staff sessions only.
set -eu
test "${XM_WEB_AUTH_MODE:-local}" = local || {
  echo 'unified web requires XM_WEB_AUTH_MODE=local; external IdP login is retired' >&2
  exit 64
}
# The public host proxy is one exact bridge host, never a network-wide trust rule.
proxy_cidr="${UNIFIED_HOST_PROXY_CIDR:-}"
case "$proxy_cidr" in
  ''|*[!0-9./]*) echo 'one exact host proxy IPv4 /32 is required' >&2; exit 64 ;;
esac
printf '%s\n' "$proxy_cidr" | awk -F '[./]' '
  NF != 5 || $5 != 32 { exit 1 }
  { for (i=1; i<=4; i++) if ($i == "" || $i < 0 || $i > 255) exit 1 }
  $1 == 0 || $1 >= 224 { exit 1 }
' || { echo 'one exact host proxy IPv4 /32 is required' >&2; exit 64; }
trust_out="${XM_WEB_TRUSTED_PROXY_CONFIG_PATH:-/etc/nginx/runtime/trusted-proxy.conf}"
printf 'set_real_ip_from %s;\nreal_ip_header X-Forwarded-For;\nreal_ip_recursive on;\n' "$proxy_cidr" > "$trust_out.tmp.$$"
mv "$trust_out.tmp.$$" "$trust_out"
out="${XM_WEB_APP_CONFIG_PATH:-/etc/nginx/runtime/app-config.js}"
tmp="$out.tmp.$$"
printf 'window.__XM_CONFIG__ = {"authMode":"local"};\n' > "$tmp"
mv "$tmp" "$out"
