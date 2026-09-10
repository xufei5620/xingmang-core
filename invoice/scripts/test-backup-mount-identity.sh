#!/usr/bin/env bash
set -Eeuo pipefail
project_root=${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}
temporary=$(mktemp -d)
trap 'rm -rf -- "$temporary"' EXIT
backup="$project_root/deploy/backup/backup.sh"
services=(sub2api-payments sub2api-identities sub2api-usage sub2api-credits sub2api-balances newapi-payments newapi-identities newapi-usage newapi-credits newapi-balances)
cases=(valid stopped canonical-input canonical-mount document-mismatch mount-missing mount-duplicate mount-type mount-path-missing inspect-failure container-missing container-duplicate container-query-failure service-label config-label label-query-failure)
for service in "${services[@]}"; do
  cases+=("state-$service")
  [[ "$service" == *-identities ]] || cases+=("cutover-$service")
done
failures=0
for mode in "${cases[@]}"; do
  [[ -z "${BACKUP_MOUNT_CASE:-}" || "$BACKUP_MOUNT_CASE" == "$mode" ]] || continue
  sandbox="$temporary/$mode"
  mkdir -p "$sandbox/current/cutover/sub2api" "$sandbox/current/cutover/newapi" "$sandbox/obsolete/cutover/sub2api" "$sandbox/obsolete/cutover/newapi"
  for service in "${services[@]}"; do mkdir -p "$sandbox/current/$service" "$sandbox/obsolete/$service"; done
  : >"$sandbox/prod.yml"; : >"$sandbox/sources.yml"; : >"$sandbox/other-project.yml"
  cat >"$sandbox/fake-docker.sh" <<'FAKE'
docker() {
  if [[ "$1" == compose ]]; then
    local service=${*: -1}
    if [[ "$*" == *'ps --status running -q'* ]]; then
      [[ "$mode" != stopped ]] || return 0
    elif [[ "$*" == *'ps -a -q'* ]]; then
      printf 'container:%s\n' "$service" >>"$sandbox/calls"
      [[ "$mode" != container-query-failure ]] || return 73
      [[ "$mode" != container-missing ]] || return 0
      if [[ "$mode" == container-duplicate ]]; then printf 'cid-extra\n'; fi
    else
      echo 'unexpected Compose operation' >&2; return 99
    fi
    printf 'cid-%s\n' "$service"
    return 0
  fi
  [[ "$1" == inspect && "$2" == --format ]] || { echo 'unexpected Docker operation' >&2; return 99; }
  local template=$3 service=${4#cid-} destination value
  if [[ "$template" == *Config.Healthcheck* ]]; then printf 'healthy\n'; return 0; fi
  if [[ "$template" == *com.docker.compose.service* ]]; then
    [[ "$mode" != label-query-failure ]] || return 74
    local observed_service=$service config=$source_compose_file
    [[ "$service" != api ]] || config=$prod_compose_file
    [[ "$mode" != service-label ]] || observed_service=other-service
    [[ "$mode" != config-label ]] || config=$sandbox/other-project.yml
    printf '%s|%s\n' "$observed_service" "$config"
    return 0
  fi
  [[ "$template" == *'.Mounts'* ]] || { echo 'unexpected Docker inspect format' >&2; return 99; }
  [[ "$mode" != inspect-failure ]] || return 75
  if [[ "$template" == *'"/data/documents"'* ]]; then
    destination=/data/documents; value='volume|invoice-system-prod_invoice_document_data'
    [[ "$mode" != document-mismatch ]] || value='volume|obsolete_document_data'
  elif [[ "$template" == *'"/cutover"'* ]]; then
    destination=/cutover; value="bind|$sandbox/current/cutover/${service%%-*}"
    [[ "$mode" != "cutover-$service" ]] || value="bind|$sandbox/obsolete/cutover/${service%%-*}"
  elif [[ "$template" == *'"/state"'* ]]; then
    destination=/state; value="bind|$sandbox/current/$service"
    [[ "$mode" != "state-$service" ]] || value="bind|$sandbox/obsolete/$service"
    [[ "$mode" != canonical-mount ]] || value="bind|$sandbox/current/./$service"
    [[ "$mode" != mount-path-missing ]] || value="bind|$sandbox/missing"
  else
    echo 'unexpected mount destination' >&2; return 99
  fi
  printf 'mount:%s:%s\n' "$service" "$destination" >>"$sandbox/calls"
  [[ "$mode" != mount-missing ]] || return 0
  [[ "$mode" != mount-type ]] || value="tmpfs|${value#*|}"
  printf '%s\n' "$value"
  [[ "$mode" != mount-duplicate ]] || printf '%s\n' "$value"
  return 0
}
FAKE
  {
    printf 'set -euo pipefail\nmode=%q\nsandbox=%q\n' "$mode" "$sandbox"
    printf 'source %q\n' "$sandbox/fake-docker.sh"
    printf 'SOURCE_STATE_ROOT="$sandbox/current"\nSOURCE_CUTOVER_ROOT="$sandbox/current/cutover"\n'
    printf '[[ "$mode" != canonical-input ]] || SOURCE_STATE_ROOT="$sandbox/current/../current"\n'
    sed -n '/^state_root=$(cd /,/^test -f "$BACKUP_SIGNING_KEY_FILE"/{ /^test -f "$BACKUP_SIGNING_KEY_FILE"/!p; }' "$backup"
    printf '[[ "$SOURCE_STATE_ROOT" == "$state_root" && "$SOURCE_CUTOVER_ROOT" == "$cutover_root" ]] || { echo "backup resource identity is not canonical" >&2; exit 1; }\n'
    printf 'prod_compose_file="$sandbox/prod.yml"\nsource_compose_file="$sandbox/sources.yml"\n'
    printf 'prod_compose=(docker compose --env-file "$sandbox/approved.env" -f "$prod_compose_file")\nsource_compose=(docker compose --env-file "$sandbox/approved.env" -f "$source_compose_file")\n'
    sed -n '/^source_services=/p' "$backup"
    sed -n '/^document_volume=/,/^record_running_service() {/{ /^record_running_service() {/q; p; }' "$backup"
    sed -n '/^record_running_service() {/,/^verify_services_running() {/{ /^verify_services_running() {/!p; }' "$backup"
    sed -n '/^# Freeze every writer/,/^resume_required=true/{ /^resume_required=true/!p; }' "$backup"
    printf 'echo BOUNDARY-ACCEPTED\n'
  } >"$sandbox/probe.sh"
  # No production preamble, key material, write-freeze, archive or resume call is executed.
  if (export work_dir="$sandbox"; bash "$sandbox/probe.sh") >"$sandbox/output" 2>&1; then actual=0; else actual=$?; fi
  case "$mode" in valid|stopped|canonical-input|canonical-mount) expected=0 ;; *) expected=1 ;; esac
  if [[ "$expected" == 0 ]]; then
    if [[ "$actual" != 0 ]] || ! grep -qx BOUNDARY-ACCEPTED "$sandbox/output"; then
      echo "FAIL $mode: legal resource identity rejected (exit=$actual)" >&2; cat "$sandbox/output" >&2; failures=$((failures+1)); continue
    fi
    for service in "${services[@]}"; do
      if ! grep -qx "mount:$service:/state" "$sandbox/calls"; then echo "FAIL $mode: /state identity unchecked for $service" >&2; failures=$((failures+1)); fi
      if [[ "$service" != *-identities ]] && ! grep -qx "mount:$service:/cutover" "$sandbox/calls"; then echo "FAIL $mode: /cutover identity unchecked for $service" >&2; failures=$((failures+1)); fi
    done
    if ! grep -qx 'mount:api:/data/documents' "$sandbox/calls"; then echo "FAIL $mode: document identity unchecked" >&2; failures=$((failures+1)); fi
  elif [[ "$actual" == 0 ]]; then
    echo "FAIL $mode: unsafe resource identity accepted" >&2; failures=$((failures+1))
  elif ! grep -Eq 'backup (resource|mount|container) identity' "$sandbox/output"; then
    echo "FAIL $mode: rejected for an unrelated reason" >&2; cat "$sandbox/output" >&2; failures=$((failures+1))
  fi
done
for mode in docs-valid docs-missing-state docs-missing-cutover; do
  [[ -z "${BACKUP_MOUNT_CASE:-}" || "$BACKUP_MOUNT_CASE" == "$mode" ]] || continue
  sandbox="$temporary/$mode"
  mkdir -p "$sandbox"
  {
    printf 'set -euo pipefail\nexpected_state=%q\n' "$sandbox/current"
    printf 'SOURCE_STATE_ROOT=$expected_state\nSOURCE_CUTOVER_ROOT=$expected_state/cutover\n'
    [[ "$mode" != docs-missing-state ]] || printf 'unset SOURCE_STATE_ROOT\n'
    [[ "$mode" != docs-missing-cutover ]] || printf 'unset SOURCE_CUTOVER_ROOT\n'
    printf 'bash() { [[ "$*" == deploy/backup/backup.sh ]] || exit 97; [[ "$SOURCE_STATE_ROOT" == "$expected_state" && "$SOURCE_CUTOVER_ROOT" == "$expected_state/cutover" ]] || { echo DOC-RESOURCE-MISMATCH >&2; exit 1; }; echo DOC-BOUNDARY-ACCEPTED; }\n'
    # Execute this finding's real generation assignments and consumer boundary.
    # Other release/key prerequisites belong to their own command-contract gates.
    sed 's/\r$//' "$project_root/docs/PRODUCTION-RUNBOOK.md" | sed -n '/^BACKUP_DIR=\/root\/invoice-system\/backups \\/,/^  bash deploy\/backup\/backup.sh/{ /^SOURCE_STATE_ROOT=/p; /^SOURCE_CUTOVER_ROOT=/p; /^  bash deploy\/backup\/backup.sh/{ p; q; }; }'
  } >"$sandbox/probe.sh"
  if bash "$sandbox/probe.sh" >"$sandbox/output" 2>&1; then actual=0; else actual=$?; fi
  if [[ "$mode" == docs-valid ]]; then
    if [[ "$actual" != 0 ]] || ! grep -qx DOC-BOUNDARY-ACCEPTED "$sandbox/output"; then echo "FAIL $mode: reviewed generation not forwarded" >&2; cat "$sandbox/output" >&2; failures=$((failures+1)); fi
  elif [[ "$actual" == 0 ]] || ! grep -Eq 'SOURCE_(STATE|CUTOVER)_ROOT:' "$sandbox/output"; then
    echo "FAIL $mode: missing reviewed resource not rejected by the documented command" >&2; cat "$sandbox/output" >&2; failures=$((failures+1))
  fi
done
(( failures == 0 )) || exit 1
echo 'Backup resource identity cases passed.'
