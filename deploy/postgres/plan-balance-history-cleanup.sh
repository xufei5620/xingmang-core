#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
export LC_ALL=C

die() { printf 'balance history cleanup plan failed: %s\n' "$*" >&2; exit 1; }
[[ "$(id -u)" == 0 ]] || die 'run as root'
: "${PRODUCTION_ENV_FILE:?set reviewed production env file}"
: "${EXPECTED_SQL_SHA256:?set reviewed cleanup SQL hash}"
: "${RECORD_ROOT:?set root-only evidence root}"
[[ "$EXPECTED_SQL_SHA256" =~ ^[0-9a-f]{64}$ ]] || die 'EXPECTED_SQL_SHA256 is invalid'
script_path=$(realpath -- "${BASH_SOURCE[0]}")
script_dir=$(dirname "$script_path")
sql_path="$script_dir/balance-history-cleanup.sql"
[[ "$(sha256sum "$sql_path" | cut -d' ' -f1)" == "$EXPECTED_SQL_SHA256" ]] || die 'cleanup SQL hash mismatch'
[[ -d "$RECORD_ROOT" && ! -L "$RECORD_ROOT" && "$(stat -c '%u:%g:%a' "$RECORD_ROOT")" == 0:0:700 ]] || die 'RECORD_ROOT must be root:root mode 0700'
project_root=$(cd "$script_dir/../.." && pwd -P)
compose_file="$project_root/deploy/docker-compose.prod.yml"
compose=(docker compose --env-file "$PRODUCTION_ENV_FILE" -f "$compose_file")
source_compose=(docker compose --env-file "$PRODUCTION_ENV_FILE" -f "$project_root/deploy/docker-compose.sources.yml")
running=$("${compose[@]}" ps --status running --services) || die 'cannot inspect production freeze'
[[ "$(sort <<<"$running")" == postgres ]] || die 'plan requires postgres-only production service state'
source_running=$("${source_compose[@]}" ps --status running -q) || die 'cannot inspect source-agent freeze'
[[ -z "$source_running" ]] || die 'plan requires every source agent stopped'
record_dir="$RECORD_ROOT/balance-history-plan-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -m 0700 -- "$record_dir"
other_sessions=$(timeout 120s "${compose[@]}" exec -T \
  -e PGOPTIONS='-c default_transaction_read_only=on -c statement_timeout=30s' postgres \
  psql -X -q -At -v ON_ERROR_STOP=1 -U invoice_owner -d invoice \
  -c "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()")
[[ "$other_sessions" == 0 ]] || die 'plan requires no other invoice database sessions'
scratch=''
cleanup() {
  local status=$?
  set +e
  if [[ -n "$scratch" && "$scratch" =~ ^/tmp/solov-balance-history-plan\.[A-Za-z0-9]+$ ]]; then
    timeout 120s "${compose[@]}" exec -T postgres rm -rf -- "$scratch" >/dev/null 2>&1 || status=1
    timeout 120s "${compose[@]}" exec -T postgres /bin/sh -c 'if [ -e "$1" ]; then exit 10; else exit 0; fi' sh "$scratch" >/dev/null 2>&1
    [[ $? == 0 ]] || status=1
  fi
  if (( status != 0 )); then
    printf '%s\n' cleanup_status=failed >"$record_dir/cleanup-status.env" 2>/dev/null || true
    ( cd "$record_dir" && find . -type f ! -name BALANCE-HISTORY-PLAN.sha256 -printf '%P\0' |
        sort -z | xargs -0 sha256sum -- >BALANCE-HISTORY-PLAN.sha256 ) 2>/dev/null || true
  fi
  exit "$status"
}
trap cleanup EXIT
scratch=$(timeout 120s "${compose[@]}" exec -T postgres mktemp -d /tmp/solov-balance-history-plan.XXXXXXXX)
keep_rows="$scratch/keep.rows"
purge_rows="$scratch/purge.rows"
timeout 120s "${compose[@]}" exec -T postgres /bin/sh -ceu 'umask 077; : >"$1"; : >"$2"' sh "$keep_rows" "$purge_rows"
timeout --foreground --kill-after=30s 7500s "${compose[@]}" exec -T \
  -e PGOPTIONS='-c default_transaction_read_only=on -c statement_timeout=2h -c lock_timeout=5s' postgres \
  psql -X -q -v ON_ERROR_STOP=1 -U invoice_owner -d invoice \
  -v apply_cleanup=false -v keep_rows_path="$keep_rows" -v purge_rows_path="$purge_rows" -f - \
  <"$sql_path" >"$record_dir/plan.stdout" 2>"$record_dir/plan.stderr"
timeout 120s "${compose[@]}" exec -T postgres sha256sum "$keep_rows" | cut -d' ' -f1 >"$record_dir/keep.sha256"
timeout 120s "${compose[@]}" exec -T postgres sha256sum "$purge_rows" | cut -d' ' -f1 >"$record_dir/purge.sha256"
for tuple in T=1879297 G=2747 K=5494 D=1873803; do grep -Fx "$tuple" "$record_dir/plan.stdout" >/dev/null || die "plan tuple missing: $tuple"; done
grep -Fx 'min_group=79|max_group=771' "$record_dir/plan.stdout" >/dev/null || die 'plan group-size tuple mismatch'
grep -Fx 'cutover_anchors=2738|post_cutover_anchors=9|baseline_time_mismatch=0' "$record_dir/plan.stdout" >/dev/null || die 'plan first-anchor tuple mismatch'
grep -Fx 'latest_actual_anchors=2747|unexpected_last=0' "$record_dir/plan.stdout" >/dev/null || die 'plan latest-actual tuple mismatch'
( cd "$record_dir" && find . -type f ! -name BALANCE-HISTORY-PLAN.sha256 -printf '%P\0' |
    sort -z | xargs -0 sha256sum -- >BALANCE-HISTORY-PLAN.sha256 )
chmod -R go-rwx -- "$record_dir"
printf 'balance history cleanup read-only plan passed: %s\n' "$record_dir/BALANCE-HISTORY-PLAN.sha256"
