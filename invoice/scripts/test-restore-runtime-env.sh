#!/usr/bin/env bash
set -Eeuo pipefail
project_root=${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}
arguments=$(sed -n '/^  docker_args=(docker run/,/^  "${docker_args\[@\]}"/p' "$project_root/deploy/backup/restore-drill.sh")
temporary=/fixture/restore
state_dir=/fixture/state
key_copy=/fixture/key
SOURCE_SPOOL_KEY_ROOT=/fixture/offline
source_agent_image=fixture-agent
expected_source=fixture-source
expected_stream=payments
eligibility_start_at=2026-09-01T00:00:00+08:00
SUB2API_RUNTIME_VERSION=approved-sub
NEWAPI_RUNTIME_VERSION=approved-new
SUB2API_BALANCES_SIGNING_KEY_ID=fixture-sub-id
NEWAPI_BALANCES_SIGNING_KEY_ID=fixture-new-id
install() { :; }; chown() { :; }; find() { :; }; test() { :; }
docker() { printf '%s\n' "$@"; }
failures=0
for source_type in sub2api newapi; do
  for mode in explicit fallback; do
    label=$source_type-$mode
    [[ -z "${RESTORE_RUNTIME_CASE:-}" || "$RESTORE_RUNTIME_CASE" == "$label" ]] || continue
    unset SUB2API_CUTOVER_RUNTIME_VERSION NEWAPI_CUTOVER_RUNTIME_VERSION
    if [[ "$mode" == explicit ]]; then SUB2API_CUTOVER_RUNTIME_VERSION=sealed-sub; NEWAPI_CUTOVER_RUNTIME_VERSION=sealed-new; fi
    if [[ "$source_type" == sub2api ]]; then expected_runtime=approved-sub; expected_cutover=${SUB2API_CUTOVER_RUNTIME_VERSION:-$expected_runtime}; else expected_runtime=approved-new; expected_cutover=${NEWAPI_CUTOVER_RUNTIME_VERSION:-$expected_runtime}; fi
    output=$(eval "$arguments")
    for pair in "SOURCE_RUNTIME_VERSION=$expected_runtime" "SOURCE_CUTOVER_RUNTIME_VERSION=$expected_cutover"; do
      if ! grep -Fx "$pair" <<<"$output" >/dev/null; then
        echo "FAIL $label: missing $pair" >&2; failures=$((failures+1))
      fi
    done
  done
done
(( failures == 0 )) || exit 1
echo 'Restore runtime environment cases passed.'
