#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
export LC_ALL=C

die() { printf 'balance history cleanup rehearsal failed: %s\n' "$*" >&2; exit 1; }
[[ $# == 2 ]] || die 'usage: rehearse-balance-history-cleanup.sh <restore-container> <evidence-dir>'
container=$1
evidence_dir=$2
[[ "$container" =~ ^invoice-restore-drill-[0-9]+-[0-9]+$ ]] || die 'unexpected restored PostgreSQL container name'
[[ -d "$evidence_dir" && ! -L "$evidence_dir" ]] || die 'evidence directory must already exist'
: "${EXPECTED_BALANCE_HISTORY_KEEP_SHA256:?set reviewed keep digest}"
: "${EXPECTED_BALANCE_HISTORY_PURGE_SHA256:?set reviewed purge digest}"
: "${EXPECTED_BALANCE_HISTORY_SQL_SHA256:?set reviewed cleanup SQL hash}"
: "${EXPECTED_BALANCE_HISTORY_REHEARSAL_SHA256:?set reviewed rehearsal script hash}"
for value in "$EXPECTED_BALANCE_HISTORY_KEEP_SHA256" "$EXPECTED_BALANCE_HISTORY_PURGE_SHA256" "$EXPECTED_BALANCE_HISTORY_SQL_SHA256" "$EXPECTED_BALANCE_HISTORY_REHEARSAL_SHA256"; do
  [[ "$value" =~ ^[0-9a-f]{64}$ ]] || die 'reviewed digests must be lowercase SHA-256'
done
script_path=$(realpath -- "${BASH_SOURCE[0]}")
script_dir=$(dirname "$script_path")
sql_path="$script_dir/balance-history-cleanup.sql"
[[ -f "$sql_path" && ! -L "$sql_path" ]] || die 'tracked cleanup SQL is missing'
[[ "$(sha256sum "$script_path" | cut -d' ' -f1)" == "$EXPECTED_BALANCE_HISTORY_REHEARSAL_SHA256" ]] || die 'rehearsal script hash mismatch'
[[ "$(sha256sum "$sql_path" | cut -d' ' -f1)" == "$EXPECTED_BALANCE_HISTORY_SQL_SHA256" ]] || die 'cleanup SQL hash mismatch'
schema_state=$(docker exec "$container" psql -X -q -At -U postgres -d invoice -c "SELECT count(*) FROM public.schema_migrations WHERE name='0014_balance_carry_forward_proof.sql'")
[[ "$schema_state" == 1 ]] || die 'cleanup rehearsal requires a post-0014 restored backup'

scratch=''
cleanup() {
  local status=$?
  set +e
  if [[ -n "$scratch" && "$scratch" =~ ^/tmp/solov-balance-history-rehearsal\.[A-Za-z0-9]+$ ]]; then
    docker exec "$container" rm -rf -- "$scratch" >/dev/null 2>&1 || status=1
    docker exec "$container" /bin/sh -c 'if [ -e "$1" ]; then exit 10; else exit 0; fi' sh "$scratch" >/dev/null 2>&1
    [[ $? == 0 ]] || status=1
  fi
  if (( status != 0 )) && [[ -e "$evidence_dir/metrics.env" ]]; then
    sed -i 's/^status=.*/status=failed/' "$evidence_dir/metrics.env" 2>/dev/null || true
    ( cd "$evidence_dir" && find . -type f ! -name BALANCE-HISTORY-REHEARSAL.sha256 -printf '%P\0' |
        sort -z | xargs -0 sha256sum -- >BALANCE-HISTORY-REHEARSAL.sha256 ) 2>/dev/null || true
  fi
  exit "$status"
}
trap cleanup EXIT

lookup_name=source_economic_scan_cycle_events_event_fk_idx
named=$(docker exec "$container" psql -X -q -At -U postgres -d invoice -c "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='$lookup_name'")
if [[ "$named" == 0 ]]; then
  timeout --foreground --kill-after=30s 7500s docker exec "$container" psql -X -q -v ON_ERROR_STOP=1 -U postgres -d invoice \
    -c "CREATE INDEX CONCURRENTLY $lookup_name ON public.source_economic_scan_cycle_events(source_instance_id,stream_id,event_id)"
fi
exact=$(docker exec "$container" psql -X -q -At -U postgres -d invoice -c "SELECT count(*) FROM pg_index i JOIN pg_class idx ON idx.oid=i.indexrelid JOIN pg_namespace n ON n.oid=idx.relnamespace JOIN pg_am am ON am.oid=idx.relam WHERE n.nspname='public' AND idx.relname='$lookup_name' AND am.amname='btree' AND i.indrelid='public.source_economic_scan_cycle_events'::regclass AND i.indnatts=3 AND i.indnkeyatts=3 AND i.indexprs IS NULL AND i.indpred IS NULL AND pg_get_indexdef(idx.oid,1,true)='source_instance_id' AND pg_get_indexdef(idx.oid,2,true)='stream_id' AND pg_get_indexdef(idx.oid,3,true)='event_id' AND NOT i.indisunique AND NOT i.indisprimary AND NOT i.indisexclusion AND i.indisvalid AND i.indisready AND i.indislive")
[[ "$exact" == 1 ]] || die 'rehearsal FK lookup index is not exact'

scratch=$(docker exec "$container" mktemp -d /tmp/solov-balance-history-rehearsal.XXXXXXXX)
[[ "$scratch" =~ ^/tmp/solov-balance-history-rehearsal\.[A-Za-z0-9]+$ ]] || die 'unsafe rehearsal scratch path'
docker exec "$container" chmod 0700 "$scratch"
[[ "$(docker exec "$container" stat -c '%u:%g:%a' "$scratch")" == '0:0:700' ]] || die 'rehearsal scratch directory mode mismatch'
keep_rows="$scratch/keep.rows"
purge_rows="$scratch/purge.rows"
docker exec "$container" /bin/sh -ceu 'umask 077; : >"$1"; : >"$2"' sh "$keep_rows" "$purge_rows"
for scratch_file in "$keep_rows" "$purge_rows"; do
  [[ "$(docker exec "$container" stat -c '%u:%g:%a' "$scratch_file")" == '0:0:600' ]] || die 'rehearsal scratch file mode mismatch'
  docker exec "$container" test -f "$scratch_file" && ! docker exec "$container" test -L "$scratch_file" || die 'rehearsal scratch file type mismatch'
done
keep_inode=$(docker exec "$container" stat -Lc '%d:%i' "$keep_rows")
purge_inode=$(docker exec "$container" stat -Lc '%d:%i' "$purge_rows")
wal_before=$(docker exec "$container" psql -X -q -At -U postgres -d invoice -c 'SELECT pg_current_wal_lsn()')
start=$SECONDS
timeout --foreground --kill-after=30s 7500s docker exec -i "$container" \
  psql -X -q -v ON_ERROR_STOP=1 -U postgres -d invoice \
  -v apply_cleanup=true \
  -v expected_keep_sha256="$EXPECTED_BALANCE_HISTORY_KEEP_SHA256" \
  -v expected_purge_sha256="$EXPECTED_BALANCE_HISTORY_PURGE_SHA256" \
  -v keep_rows_path="$keep_rows" -v purge_rows_path="$purge_rows" -f - \
  <"$sql_path" >"$evidence_dir/delete.stdout" 2>"$evidence_dir/delete.stderr"
[[ "$keep_inode" == "$(docker exec "$container" stat -Lc '%d:%i' "$keep_rows")" &&
   "$purge_inode" == "$(docker exec "$container" stat -Lc '%d:%i' "$purge_rows")" ]] || die 'rehearsal scratch inode changed'
wal_after=$(docker exec "$container" psql -X -q -At -U postgres -d invoice -c 'SELECT pg_current_wal_lsn()')
wal_bytes=$(docker exec "$container" psql -X -q -At -U postgres -d invoice -c "SELECT pg_wal_lsn_diff('$wal_after','$wal_before')::numeric(30,0)")
docker exec "$container" sha256sum "$keep_rows" | cut -d' ' -f1 >"$evidence_dir/keep.sha256"
docker exec "$container" sha256sum "$purge_rows" | cut -d' ' -f1 >"$evidence_dir/purge.sha256"
[[ "$(<"$evidence_dir/keep.sha256")" == "$EXPECTED_BALANCE_HISTORY_KEEP_SHA256" ]]
[[ "$(<"$evidence_dir/purge.sha256")" == "$EXPECTED_BALANCE_HISTORY_PURGE_SHA256" ]]
printf 'status=passed\nT=1879297\nG=2747\nK=5494\nD=1873803\nduration_seconds=%s\nwal_bytes=%s\nwal_before=%s\nwal_after=%s\ncleanup_sql_sha256=%s\nrehearsal_sha256=%s\nbackup_manifest_sha256=%s\nbackup_signature_sha256=%s\nkeep_sha256=%s\npurge_sha256=%s\n' \
  "$((SECONDS-start))" "$wal_bytes" "$wal_before" "$wal_after" "$EXPECTED_BALANCE_HISTORY_SQL_SHA256" \
  "$EXPECTED_BALANCE_HISTORY_REHEARSAL_SHA256" "$(sha256sum "$BACKUP_MANIFEST" | cut -d' ' -f1)" \
  "$(sha256sum "$BACKUP_SIGNATURE" | cut -d' ' -f1)" "$EXPECTED_BALANCE_HISTORY_KEEP_SHA256" \
  "$EXPECTED_BALANCE_HISTORY_PURGE_SHA256" \
  >"$evidence_dir/metrics.env"
timeout --foreground --kill-after=30s 7500s docker exec "$container" env PGOPTIONS='-c statement_timeout=2h -c lock_timeout=5s' \
  psql -X -q -v ON_ERROR_STOP=1 -U postgres -d invoice \
  -c 'VACUUM (ANALYZE) public.source_economic_scan_cycle_events' \
  -c 'VACUUM (ANALYZE) public.source_ingest_events' >"$evidence_dir/vacuum.stdout" 2>"$evidence_dir/vacuum.stderr"
printf 'vacuum_status=passed\ncompleted_epoch=%s\n' "$(date -u +%s)" >>"$evidence_dir/metrics.env"
( cd "$evidence_dir" && find . -type f ! -name BALANCE-HISTORY-REHEARSAL.sha256 -printf '%P\0' |
    sort -z | xargs -0 sha256sum -- >BALANCE-HISTORY-REHEARSAL.sha256 )
printf '%s\n' 'balance history cleanup rehearsal passed on restored PostgreSQL copy'
