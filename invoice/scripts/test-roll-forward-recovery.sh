#!/usr/bin/env bash
set -Eeuo pipefail
project_root=${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}
recovery=$(sed -n '/^restart_ingest_proxy() {/,/^docker restart invoice-system-prod-api-1/{ /^docker restart invoice-system-prod-api-1/!p; }' "$project_root/deploy/roll-forward.sh")
failures=0
for spec in normal-ok:0:0 normal-failed:42:42 trap-ok:0:17 trap-failed:42:17; do
  IFS=: read -r label restart_exit expected <<<"$spec"
  [[ -z "${RECOVERY_CASE:-}" || "$RECOVERY_CASE" == "$label" ]] || continue
  set +e
  output=$(
    set -euo pipefail
    exec 2>&1
    docker() { [[ "$*" == 'restart invoice-system-prod-ingest-proxy-1' ]] || return 99; echo FAKE_PROXY_ATTEMPT >&2; return "$restart_exit"; }
    sleep() { :; }
    eval "$recovery"
    if [[ "$label" == trap-* ]]; then exit 17; fi
    restart_ingest_proxy
    trap - EXIT
    echo NORMAL_CONTINUED
  )
  # Capture diagnostics too, without executing any Docker binary.
  actual=$?
  set -e
  if [[ "$actual" != "$expected" ]]; then
    echo "FAIL $label: exit=$actual expected=$expected" >&2; failures=$((failures+1))
  fi
  if [[ "$output" != *FAKE_PROXY_ATTEMPT* ]]; then
    echo "FAIL $label: recovery was not attempted" >&2; failures=$((failures+1))
  fi
done
(( failures == 0 )) || exit 1
echo 'Roll-forward recovery exit cases passed.'
