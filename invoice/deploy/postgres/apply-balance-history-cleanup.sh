#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
export LC_ALL=C

die() { printf 'balance history cleanup operator failed: %s\n' "$*" >&2; exit 1; }

[[ "$(id -u)" == 0 ]] || die 'run as root'
: "${BALANCE_HISTORY_CLEANUP_CONFIRMED:?set BALANCE_HISTORY_CLEANUP_CONFIRMED=YES after signed approval}"
: "${WRITE_FREEZE_CONFIRMED:?set WRITE_FREEZE_CONFIRMED=YES after every writer is stopped}"
: "${PRODUCTION_ENV_FILE:?set reviewed production env file}"
: "${EXPECTED_OPERATOR_SHA256:?set signed operator SHA-256}"
: "${EXPECTED_SQL_SHA256:?set signed cleanup SQL SHA-256}"
: "${EXPECTED_SCHEMA_MIGRATIONS_SHA256:?set reviewed schema_migrations SHA-256}"
: "${EXPECTED_KEEP_SHA256:?set reviewed canonical keep-set SHA-256}"
: "${EXPECTED_PURGE_SHA256:?set reviewed canonical purge-set SHA-256}"
: "${WRITE_FREEZE_STARTED_EPOCH:?set reviewed write-freeze start epoch}"
: "${BACKUP_MANIFEST:?set signed full-backup manifest}"
: "${BACKUP_SIGNATURE:?set full-backup manifest signature}"
: "${BACKUP_ALLOWED_SIGNERS_FILE:?set reviewed backup allowed_signers file}"
: "${RESTORE_REHEARSAL_MANIFEST:?set signed 16g restore/rehearsal evidence manifest}"
: "${PLAN_EVIDENCE_MANIFEST:?set signed read-only plan evidence manifest}"
: "${OFFSITE_ACK_FILE:?set signed off-site backup ACK evidence}"
: "${EXPECTED_BACKUP_MANIFEST_SHA256:?set reviewed backup-manifest SHA-256}"
: "${EXPECTED_RESTORE_REHEARSAL_SHA256:?set reviewed restore/rehearsal SHA-256}"
: "${EXPECTED_PLAN_EVIDENCE_SHA256:?set reviewed plan-evidence SHA-256}"
: "${EXPECTED_OFFSITE_ACK_SHA256:?set reviewed off-site ACK SHA-256}"
: "${RECORD_ROOT:?set fixed root-only evidence root}"
[[ "$BALANCE_HISTORY_CLEANUP_CONFIRMED" == YES && "$WRITE_FREEZE_CONFIRMED" == YES ]] ||
  die 'both cleanup and write-freeze confirmations must equal YES'
for value in "$EXPECTED_OPERATOR_SHA256" "$EXPECTED_SQL_SHA256" "$EXPECTED_SCHEMA_MIGRATIONS_SHA256" "$EXPECTED_KEEP_SHA256" "$EXPECTED_PURGE_SHA256"; do
  [[ "$value" =~ ^[0-9a-f]{64}$ ]] || die 'every reviewed hash must be lowercase SHA-256'
done
for value in "$EXPECTED_BACKUP_MANIFEST_SHA256" "$EXPECTED_RESTORE_REHEARSAL_SHA256" "$EXPECTED_PLAN_EVIDENCE_SHA256" "$EXPECTED_OFFSITE_ACK_SHA256"; do
  [[ "$value" =~ ^[0-9a-f]{64}$ ]] || die 'recovery evidence hashes must be lowercase SHA-256'
done
[[ "$WRITE_FREEZE_STARTED_EPOCH" =~ ^[1-9][0-9]{9}$ ]] || die 'WRITE_FREEZE_STARTED_EPOCH is invalid'
for command_name in awk chmod cmp cut date df docker find flock grep id mkdir realpath rm sed sha256sum sort ssh-keygen stat sync timeout xargs; do
  command -v "$command_name" >/dev/null || die "required command unavailable: $command_name"
done

readonly SCRIPT_PATH="$(realpath -- "${BASH_SOURCE[0]}")"
readonly SCRIPT_DIR="$(dirname "$SCRIPT_PATH")"
readonly SQL_PATH="$SCRIPT_DIR/balance-history-cleanup.sql"
[[ -f "$SCRIPT_PATH" && ! -L "$SCRIPT_PATH" && "$(stat -c '%u:%g:%a' "$SCRIPT_PATH")" == 0:0:700 ]] ||
  die 'installed operator must be root:root mode 0700'
[[ -f "$SQL_PATH" && ! -L "$SQL_PATH" && "$(stat -c '%u:%g:%a' "$SQL_PATH")" == 0:0:600 ]] ||
  die 'installed cleanup SQL must be root:root mode 0600'
readonly ACTUAL_OPERATOR_SHA256="$(sha256sum "$SCRIPT_PATH" | cut -d' ' -f1)"
readonly ACTUAL_SQL_SHA256="$(sha256sum "$SQL_PATH" | cut -d' ' -f1)"
[[ "$ACTUAL_OPERATOR_SHA256" == "$EXPECTED_OPERATOR_SHA256" ]] || die 'operator hash mismatch'
[[ "$ACTUAL_SQL_SHA256" == "$EXPECTED_SQL_SHA256" ]] || die 'cleanup SQL hash mismatch'

check_root_file() {
  local path=$1 label=$2 mode
  [[ -f "$path" && ! -L "$path" && -s "$path" && "$(stat -c '%u:%g' "$path")" == 0:0 ]] ||
    die "$label must be a nonempty root-owned regular non-symlink file"
  mode=$(stat -c '%a' "$path")
  (( (8#$mode & 8#022) == 0 )) || die "$label must not be group/other writable"
}
check_root_directory() {
  [[ -d "$1" && ! -L "$1" && "$(stat -c '%u:%g:%a' "$1")" == 0:0:700 ]] ||
    die "$2 must be root:root mode 0700"
}
check_root_file "$PRODUCTION_ENV_FILE" 'production environment file'
check_root_directory "$RECORD_ROOT" 'evidence root'
check_root_file "$BACKUP_MANIFEST" 'signed backup manifest'
check_root_file "$BACKUP_SIGNATURE" 'backup manifest signature'
check_root_file "$BACKUP_ALLOWED_SIGNERS_FILE" 'backup allowed_signers file'
check_root_file "$RESTORE_REHEARSAL_MANIFEST" 'restore/rehearsal evidence manifest'
check_root_file "$PLAN_EVIDENCE_MANIFEST" 'read-only plan evidence manifest'
check_root_file "$OFFSITE_ACK_FILE" 'off-site ACK evidence'
[[ "$(sha256sum "$BACKUP_MANIFEST" | cut -d' ' -f1)" == "$EXPECTED_BACKUP_MANIFEST_SHA256" ]] || die 'backup manifest hash mismatch'
[[ "$(sha256sum "$RESTORE_REHEARSAL_MANIFEST" | cut -d' ' -f1)" == "$EXPECTED_RESTORE_REHEARSAL_SHA256" ]] || die 'restore/rehearsal manifest hash mismatch'
[[ "$(sha256sum "$PLAN_EVIDENCE_MANIFEST" | cut -d' ' -f1)" == "$EXPECTED_PLAN_EVIDENCE_SHA256" ]] || die 'plan evidence manifest hash mismatch'
[[ "$(sha256sum "$OFFSITE_ACK_FILE" | cut -d' ' -f1)" == "$EXPECTED_OFFSITE_ACK_SHA256" ]] || die 'off-site ACK hash mismatch'
ssh-keygen -Y verify -f "$BACKUP_ALLOWED_SIGNERS_FILE" -I invoice-backup \
  -n solov-invoice-backup-v1 -s "$BACKUP_SIGNATURE" <"$BACKUP_MANIFEST" >/dev/null || die 'backup manifest signature verification failed'
(
  cd "$(dirname "$BACKUP_MANIFEST")"
  sha256sum -c "$(basename "$BACKUP_MANIFEST")" >/dev/null
) || die 'signed backup manifest component verification failed'
rehearsal_dir=$(cd "$(dirname "$RESTORE_REHEARSAL_MANIFEST")" && pwd -P)
(
  cd "$rehearsal_dir"
  sha256sum -c "$(basename "$RESTORE_REHEARSAL_MANIFEST")" >/dev/null
) || die 'restore/rehearsal manifest component verification failed'
plan_dir=$(cd "$(dirname "$PLAN_EVIDENCE_MANIFEST")" && pwd -P)
(
  cd "$plan_dir"
  sha256sum -c "$(basename "$PLAN_EVIDENCE_MANIFEST")" >/dev/null
) || die 'plan evidence manifest component verification failed'
check_root_file "$plan_dir/keep.sha256" 'plan keep digest'
check_root_file "$plan_dir/purge.sha256" 'plan purge digest'
[[ "$(<"$plan_dir/keep.sha256")" == "$EXPECTED_KEEP_SHA256" &&
   "$(<"$plan_dir/purge.sha256")" == "$EXPECTED_PURGE_SHA256" ]] || die 'plan set digests mismatch'
check_root_file "$rehearsal_dir/metrics.env" 'rehearsal metrics'
check_root_file "$rehearsal_dir/keep.sha256" 'rehearsal keep digest'
check_root_file "$rehearsal_dir/purge.sha256" 'rehearsal purge digest'
[[ "$(<"$rehearsal_dir/keep.sha256")" == "$EXPECTED_KEEP_SHA256" &&
   "$(<"$rehearsal_dir/purge.sha256")" == "$EXPECTED_PURGE_SHA256" ]] || die 'rehearsal set digests mismatch'
for expected_line in status=passed T=1879297 G=2747 K=5494 D=1873803 \
  "cleanup_sql_sha256=$EXPECTED_SQL_SHA256" "backup_manifest_sha256=$EXPECTED_BACKUP_MANIFEST_SHA256" \
  "keep_sha256=$EXPECTED_KEEP_SHA256" "purge_sha256=$EXPECTED_PURGE_SHA256" vacuum_status=passed; do
  grep -Fx "$expected_line" "$rehearsal_dir/metrics.env" >/dev/null || die "rehearsal metrics missing: $expected_line"
done
completed_epoch=$(grep -E '^completed_epoch=[1-9][0-9]{9}$' "$rehearsal_dir/metrics.env" | cut -d= -f2)
[[ "$completed_epoch" =~ ^[1-9][0-9]{9}$ ]] || die 'rehearsal completion epoch is missing'
now_epoch=$(date -u +%s)
for evidence_file in "$BACKUP_MANIFEST" "$PLAN_EVIDENCE_MANIFEST" "$RESTORE_REHEARSAL_MANIFEST" "$OFFSITE_ACK_FILE"; do
  evidence_epoch=$(stat -c '%Y' "$evidence_file")
  (( evidence_epoch >= WRITE_FREEZE_STARTED_EPOCH && now_epoch-evidence_epoch <= 7200 && now_epoch >= evidence_epoch )) ||
    die 'backup/restore/off-site evidence is stale or predates the write freeze'
done
(( completed_epoch >= WRITE_FREEZE_STARTED_EPOCH && now_epoch-completed_epoch <= 7200 && now_epoch >= completed_epoch )) ||
  die 'rehearsal evidence is stale or predates the write freeze'
grep -Fx "backup_manifest_sha256=$EXPECTED_BACKUP_MANIFEST_SHA256" "$OFFSITE_ACK_FILE" >/dev/null ||
  die 'off-site ACK does not bind the approved backup manifest'
ack_epoch=$(grep -E '^ack_epoch=[1-9][0-9]{9}$' "$OFFSITE_ACK_FILE" | cut -d= -f2)
[[ "$ack_epoch" =~ ^[1-9][0-9]{9}$ ]] || die 'off-site ACK epoch is missing'
(( ack_epoch >= WRITE_FREEZE_STARTED_EPOCH && now_epoch-ack_epoch <= 7200 && now_epoch >= ack_epoch )) ||
  die 'off-site ACK is stale or predates the write freeze'

readonly PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd -P)"
readonly PROD_COMPOSE_FILE="$PROJECT_ROOT/deploy/docker-compose.prod.yml"
readonly SOURCE_COMPOSE_FILE="$PROJECT_ROOT/deploy/docker-compose.sources.yml"
check_root_file "$PROD_COMPOSE_FILE" 'production Compose file'
check_root_file "$SOURCE_COMPOSE_FILE" 'source Compose file'
readonly -a PROD_COMPOSE=(docker compose --env-file "$PRODUCTION_ENV_FILE" -f "$PROD_COMPOSE_FILE")
readonly -a SOURCE_COMPOSE=(docker compose --env-file "$PRODUCTION_ENV_FILE" -f "$SOURCE_COMPOSE_FILE")

readonly LOCK_PARENT=/run/lock
readonly LOCK_DIRECTORY="$LOCK_PARENT/solov-invoice-balance-history-cleanup"
readonly LOCK_FILE="$LOCK_DIRECTORY/operator.lock"
[[ -d "$LOCK_PARENT" && ! -L "$LOCK_PARENT" && "$(stat -c '%u:%g' "$LOCK_PARENT")" == 0:0 ]] ||
  die '/run/lock must be a root-owned regular directory'
lock_parent_mode=$(stat -c '%a' "$LOCK_PARENT")
(( (8#$lock_parent_mode & 8#002) == 0 || (8#$lock_parent_mode & 8#1000) != 0 )) ||
  die '/run/lock must not be world-writable unless sticky'
if [[ ! -e "$LOCK_DIRECTORY" && ! -L "$LOCK_DIRECTORY" ]]; then mkdir -m 0700 -- "$LOCK_DIRECTORY"; fi
[[ -d "$LOCK_DIRECTORY" && ! -L "$LOCK_DIRECTORY" && "$(stat -c '%u:%g:%a' "$LOCK_DIRECTORY")" == 0:0:700 ]] ||
  die 'dedicated lock directory must be root:root mode 0700'
[[ "$(realpath -- "$LOCK_DIRECTORY")" == "$LOCK_DIRECTORY" ]] || die 'lock directory path drifted'
if [[ ! -e "$LOCK_FILE" && ! -L "$LOCK_FILE" ]]; then ( set -o noclobber; : >"$LOCK_FILE" ); fi
[[ -f "$LOCK_FILE" && ! -L "$LOCK_FILE" && "$(stat -c '%u:%g:%a' "$LOCK_FILE")" == 0:0:600 ]] ||
  die 'lock file must be root:root mode 0600'
exec 9<>"$LOCK_FILE"
readonly LOCK_INODE="$(stat -Lc '%d:%i' "$LOCK_FILE")"
[[ "$LOCK_INODE" == "$(stat -Lc '%d:%i' "/proc/$$/fd/9")" ]] || die 'lock FD/path inode mismatch'
flock -n 9 || die 'another balance history cleanup is running'
[[ "$LOCK_INODE" == "$(stat -Lc '%d:%i' "$LOCK_FILE")" ]] || die 'lock inode changed after flock'

readonly TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
readonly RECORD_DIR="$RECORD_ROOT/balance-history-cleanup-$TIMESTAMP"
mkdir -m 0700 -- "$RECORD_DIR"
readonly START_SECONDS=$SECONDS
readonly START_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
OPERATION_STATUS=failed
FAILURE_LINE=none

SCRATCH_DIR=''
DIGEST_ROWS=''
KEEP_DIGEST_ROWS=''
postgres_exec() { timeout --foreground --kill-after=10s 120s "${PROD_COMPOSE[@]}" exec -T postgres "$@"; }
psql_owner() { postgres_exec psql -X -q -v ON_ERROR_STOP=1 -U invoice_owner -d invoice "$@"; }
psql_readonly() {
  timeout --foreground --kill-after=10s 120s "${PROD_COMPOSE[@]}" exec -T -e PGOPTIONS='-c default_transaction_read_only=on -c statement_timeout=30s -c lock_timeout=3s' \
    postgres psql -X -q -v ON_ERROR_STOP=1 -U invoice_owner -d invoice "$@"
}
remove_digest_files() {
  [[ -z "$SCRATCH_DIR" ]] && return 0
  [[ "$SCRATCH_DIR" =~ ^/tmp/solov-balance-history-cleanup\.[A-Za-z0-9]+$ ]] || return 1
  postgres_exec rm -rf -- "$SCRATCH_DIR" >/dev/null 2>&1 || return 2
  postgres_exec /bin/sh -c 'if [ -e "$1" ]; then exit 10; else exit 0; fi' sh "$SCRATCH_DIR" >/dev/null 2>&1
  local state=$?
  (( state == 0 )) && return 0
  (( state == 10 )) && return 1
  return 2
}
finalize_record() {
  local status=$?
  trap - EXIT ERR
  set +e
  if ! remove_digest_files; then
    status=1
    OPERATION_STATUS=failed
  fi
  cat >"$RECORD_DIR/result.env" <<RESULT
status=$OPERATION_STATUS
started_utc=$START_UTC
finished_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)
duration_seconds=$((SECONDS-START_SECONDS))
failure_line=$FAILURE_LINE
operator_sha256=$ACTUAL_OPERATOR_SHA256
cleanup_sql_sha256=$ACTUAL_SQL_SHA256
schema_migrations_sha256=$EXPECTED_SCHEMA_MIGRATIONS_SHA256
purge_sha256=$EXPECTED_PURGE_SHA256
keep_sha256=$EXPECTED_KEEP_SHA256
backup_manifest_sha256=$EXPECTED_BACKUP_MANIFEST_SHA256
restore_rehearsal_sha256=$EXPECTED_RESTORE_REHEARSAL_SHA256
plan_evidence_sha256=$EXPECTED_PLAN_EVIDENCE_SHA256
offsite_ack_sha256=$EXPECTED_OFFSITE_ACK_SHA256
write_freeze_started_epoch=$WRITE_FREEZE_STARTED_EPOCH
schema_migrations_written_by_operator=false
sub2api_newapi_touched=false
RESULT
  if [[ $? != 0 ]]; then status=1; OPERATION_STATUS=failed; fi
  ( cd "$RECORD_DIR" && find . -type f ! -name BALANCE-HISTORY-CLEANUP.sha256 -printf '%P\0' |
      sort -z | xargs -0 sha256sum -- >BALANCE-HISTORY-CLEANUP.sha256 ) || { status=1; OPERATION_STATUS=failed; }
  chmod -R go-rwx -- "$RECORD_DIR" || { status=1; OPERATION_STATUS=failed; }
  sync -f "$RECORD_ROOT" 2>/dev/null || sync || { status=1; OPERATION_STATUS=failed; }
  if (( status != 0 )); then
    OPERATION_STATUS=failed
    sed -i 's/^status=.*/status=failed/' "$RECORD_DIR/result.env" 2>/dev/null || true
    ( cd "$RECORD_DIR" && find . -type f ! -name BALANCE-HISTORY-CLEANUP.sha256 -printf '%P\0' |
        sort -z | xargs -0 sha256sum -- >BALANCE-HISTORY-CLEANUP.sha256 ) 2>/dev/null || true
  fi
  exit "$status"
}
trap 'FAILURE_LINE=$LINENO' ERR
trap finalize_record EXIT

SCRATCH_DIR=$(postgres_exec mktemp -d /tmp/solov-balance-history-cleanup.XXXXXXXX)
[[ "$SCRATCH_DIR" =~ ^/tmp/solov-balance-history-cleanup\.[A-Za-z0-9]+$ ]] || die 'unsafe digest scratch path'
[[ "$(postgres_exec stat -c '%u:%g:%a' "$SCRATCH_DIR")" == '0:0:700' ]] || die 'digest scratch must be root:root mode 0700'
[[ "$(postgres_exec realpath -- "$SCRATCH_DIR")" == "$SCRATCH_DIR" ]] || die 'digest scratch path drifted'
DIGEST_ROWS="$SCRATCH_DIR/purge.rows"
KEEP_DIGEST_ROWS="$SCRATCH_DIR/keep.rows"

# Full write freeze: only PostgreSQL may remain running in the invoice stack;
# all ten source agents and every API/ingest/maintenance writer must be stopped.
running_prod_output=$("${PROD_COMPOSE[@]}" ps --status running --services) || die 'cannot inspect production service freeze'
mapfile -t running_prod < <(sort <<<"$running_prod_output")
[[ "${running_prod[*]}" == postgres ]] || die 'write freeze requires postgres to be the only running production service'
source_running_output=$("${SOURCE_COMPOSE[@]}" ps --status running -q) || die 'cannot inspect source-agent freeze'
[[ -z "$source_running_output" ]] || die 'all source agents must be stopped'
readonly OTHER_SESSIONS="$(psql_readonly -At -c "SELECT count(*) FROM pg_catalog.pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()")"
[[ "$OTHER_SESSIONS" == 0 ]] || die 'unexpected invoice database sessions remain during write freeze'
readonly ECONOMIC_ZERO_STATE="$(psql_readonly -At -F '|' -c "
SELECT (SELECT count(*) FROM public.external_accounts),
       (SELECT count(*) FROM public.external_account_binding_proofs),
       (SELECT count(*) FROM public.source_account_eligibility_state),
       (SELECT count(*) FROM public.balance_reconciliation_checkpoints),
       (SELECT count(*) FROM public.balance_checkpoint_evaluations),
       (SELECT count(*) FROM public.balance_carry_forward_proofs),
       (SELECT count(*) FROM public.balance_carry_forward_evaluations),
       (SELECT count(*) FROM public.eligibility_projection_jobs)")"
[[ "$ECONOMIC_ZERO_STATE" == '0|0|0|0|0|0|0|0' ]] || die "pre-policy identity/economic state must be empty: $ECONOMIC_ZERO_STATE"
readonly RELATION_BYTES="$(psql_readonly -At -c "SELECT pg_total_relation_size('public.source_ingest_events'::regclass)+pg_total_relation_size('public.source_economic_scan_cycle_events'::regclass)")"
readonly WAL_BYTES_BEFORE="$(psql_readonly -At -c "SELECT COALESCE(sum(size),0) FROM pg_ls_waldir()")"
readonly DATA_DIRECTORY="$(psql_readonly -At -c 'SHOW data_directory')"
[[ "$DATA_DIRECTORY" == /var/lib/postgresql/* && "$DATA_DIRECTORY" != *'..'* ]] || die "unexpected PostgreSQL 18 data_directory: $DATA_DIRECTORY"
readonly DATA_DIRECTORY_REAL="$(postgres_exec realpath -- "$DATA_DIRECTORY")"
[[ "$DATA_DIRECTORY_REAL" == "$DATA_DIRECTORY" ]] || die 'PostgreSQL data_directory resolves through an unexpected path'
readonly PGDATA_AVAILABLE_BYTES="$(postgres_exec df -PB1 "$DATA_DIRECTORY_REAL" | awk 'NR==2 {print $4}')"
readonly CONTAINER_TMP_AVAILABLE_BYTES="$(postgres_exec df -PB1 /tmp | awk 'NR==2 {print $4}')"
readonly RECORD_AVAILABLE_BYTES="$(df -PB1 "$RECORD_ROOT" | awk 'NR==2 {print $4}')"
for value in "$RELATION_BYTES" "$WAL_BYTES_BEFORE" "$PGDATA_AVAILABLE_BYTES" "$CONTAINER_TMP_AVAILABLE_BYTES" "$RECORD_AVAILABLE_BYTES"; do
  [[ "$value" =~ ^[0-9]+$ ]] || die 'capacity evidence is unavailable'
done
required_pgdata_bytes=$((2*RELATION_BYTES+2*WAL_BYTES_BEFORE))
(( required_pgdata_bytes < 20*1024*1024*1024 )) && required_pgdata_bytes=$((20*1024*1024*1024))
(( PGDATA_AVAILABLE_BYTES >= required_pgdata_bytes )) || die 'PostgreSQL volume lacks cleanup/WAL headroom'
(( CONTAINER_TMP_AVAILABLE_BYTES >= 5*1024*1024*1024 && RECORD_AVAILABLE_BYTES >= 5*1024*1024*1024 )) ||
  die 'PostgreSQL-container /tmp or evidence root lacks 5g free space'
readonly REPLICATION_STATE="$(psql_readonly -At -F '|' -c "
SELECT current_setting('archive_mode'),
       CASE WHEN current_setting('archive_mode')='off' THEN true
            ELSE COALESCE(last_failed_time<=last_archived_time,false) END,
       (SELECT count(*) FROM pg_replication_slots),
       (SELECT count(*) FROM pg_stat_replication)
FROM pg_stat_archiver")"
[[ "$REPLICATION_STATE" == 'off|t|0|0' || "$REPLICATION_STATE" == 'on|t|0|0' ]] ||
  die "archive/slot/replication gate is not clean: $REPLICATION_STATE"
printf 'data_directory=%s\nrelation_bytes=%s\nwal_bytes=%s\npgdata_available_bytes=%s\ncontainer_tmp_available_bytes=%s\nrecord_available_bytes=%s\nrequired_pgdata_bytes=%s\nreplication_state=%s\n' \
  "$DATA_DIRECTORY_REAL" "$RELATION_BYTES" "$WAL_BYTES_BEFORE" "$PGDATA_AVAILABLE_BYTES" "$CONTAINER_TMP_AVAILABLE_BYTES" \
  "$RECORD_AVAILABLE_BYTES" "$required_pgdata_bytes" "$REPLICATION_STATE" >"$RECORD_DIR/capacity-before.env"
psql_readonly -At -F '|' -c "SELECT name,checksum FROM public.schema_migrations ORDER BY name" >"$RECORD_DIR/schema-migrations-before.tsv"
[[ "$(sha256sum "$RECORD_DIR/schema-migrations-before.tsv" | cut -d' ' -f1)" == "$EXPECTED_SCHEMA_MIGRATIONS_SHA256" ]] ||
  die 'schema_migrations differs from reviewed maintenance input'
readonly TARGET_JOIN_SQL="
FROM public.source_ingest_events event
JOIN public.source_economic_scan_cycle_events mapped
  ON mapped.source_instance_id=event.source_instance_id AND mapped.stream_id=event.stream_id AND mapped.event_id=event.event_id
JOIN public.source_economic_scan_cycles cycle
  ON cycle.source_instance_id=mapped.source_instance_id AND cycle.stream_id=mapped.stream_id AND cycle.scan_cycle_id=mapped.scan_cycle_id
JOIN public.source_ingest_batches batch
  ON batch.source_instance_id=mapped.source_instance_id AND batch.stream_id=mapped.stream_id
 AND batch.batch_id=mapped.batch_id AND batch.scan_cycle_id=mapped.scan_cycle_id
JOIN public.source_cutover_manifests manifest ON manifest.source_instance_id=event.source_instance_id
WHERE event.stream_id='balances' AND event.entity_type='balance_checkpoint' AND event.operation='upsert'
AND event.processing_status='parked_identity' AND event.dependency_kind='source_external_account'
AND event.dependency_key_hmac IS NOT NULL AND mapped.payload_hash=event.payload_hash
AND cycle.cycle_status='published' AND batch.schema_version='3.0'
AND cycle.scan_ceiling_at<'2026-09-01T00:00:00+08:00'::timestamptz"
readonly TARGET_COUNT_SQL="SELECT count(*) $TARGET_JOIN_SQL"
readonly ACTIVE_EVENT_COUNT="$(psql_readonly -At -c "SELECT count(*) FILTER (WHERE processing_status IN ('queued','processing','failed','dead')) FROM public.source_ingest_events")"
[[ "$ACTIVE_EVENT_COUNT" == 0 ]] || die "active/dead source events remain during freeze: $ACTIVE_EVENT_COUNT"
readonly TARGET_COUNT_BEFORE="$(psql_readonly -At -c "$TARGET_COUNT_SQL")"
readonly PARKED_TOTAL_BEFORE="$(psql_readonly -At -c "SELECT count(*) FROM public.source_ingest_events WHERE processing_status='parked_identity'")"
# Frozen RC45 prestate: 1,879,297 approved balance-history targets plus
# 25,044 non-target parked payment/usage/credit events.  Removing the exact
# 1,873,803 purge set must therefore leave 30,538 parked rows.
readonly EXPECTED_PARKED_PRESTATE=1904341
readonly EXPECTED_PARKED_POSTSTATE=30538
CLEANUP_STATE=unknown
case "$TARGET_COUNT_BEFORE" in
  1879297)
    [[ "$PARKED_TOTAL_BEFORE" == "$EXPECTED_PARKED_PRESTATE" ]] || die "prestate parked total mismatch: $PARKED_TOTAL_BEFORE"
    CLEANUP_STATE=prestate
    printf '%s\n' prestate >"$RECORD_DIR/cleanup-state-before.txt"
    ;;
  5494)
    [[ "$PARKED_TOTAL_BEFORE" == "$EXPECTED_PARKED_POSTSTATE" ]] || die "poststate parked total mismatch: $PARKED_TOTAL_BEFORE"
    CLEANUP_STATE=poststate
    readonly POST_GROUP_STATE="$(psql_readonly -At -F '|' -c "SELECT count(*),min(group_rows),max(group_rows) FROM (SELECT count(*) group_rows $TARGET_JOIN_SQL GROUP BY event.source_instance_id,event.dependency_key_hmac) grouped")"
    [[ "$POST_GROUP_STATE" == '2747|2|2' ]] || die "poststate group tuple mismatch: $POST_GROUP_STATE"
    readonly POST_KEEP_DIGEST="$({ psql_readonly -At -c "COPY (SELECT event.source_instance_id::text||'|'||event.event_id::text $TARGET_JOIN_SQL ORDER BY event.source_instance_id,event.event_id) TO STDOUT"; } | sha256sum | cut -d' ' -f1)"
    [[ "$POST_KEEP_DIGEST" == "$EXPECTED_KEEP_SHA256" ]] || die 'poststate keep digest mismatch'
    printf 'state=exact-poststate\ntarget=5494\ngroups=2747\nkeep_sha256=%s\n' "$POST_KEEP_DIGEST" >"$RECORD_DIR/cleanup-state-before.txt"
    ;;
  *)
    die "balance history cleanup state is neither exact prestate nor exact poststate: target=$TARGET_COUNT_BEFORE"
    ;;
esac

capture_guard_counts() {
  local output=$1
  psql_readonly -At -F '|' -c "
SELECT 'source_ingest_events',count(*) FROM public.source_ingest_events UNION ALL
SELECT 'source_economic_scan_cycle_events',count(*) FROM public.source_economic_scan_cycle_events UNION ALL
SELECT 'source_ingest_batches',count(*) FROM public.source_ingest_batches UNION ALL
SELECT 'source_economic_scan_cycles',count(*) FROM public.source_economic_scan_cycles UNION ALL
SELECT 'source_economic_stream_watermarks',count(*) FROM public.source_economic_stream_watermarks UNION ALL
SELECT 'source_cutover_manifests',count(*) FROM public.source_cutover_manifests UNION ALL
SELECT 'audit_events',count(*) FROM public.audit_events UNION ALL
SELECT 'schema_migrations',count(*) FROM public.schema_migrations ORDER BY 1" >"$output"
}
capture_guard_counts "$RECORD_DIR/counts-before.tsv"
psql_readonly -At -F '|' -c "SELECT processing_status,count(*) FROM public.source_ingest_events GROUP BY processing_status ORDER BY processing_status" >"$RECORD_DIR/status-before.tsv"

readonly LOOKUP_INDEX=source_economic_scan_cycle_events_event_fk_idx
readonly LOOKUP_CREATE="CREATE INDEX CONCURRENTLY $LOOKUP_INDEX ON public.source_economic_scan_cycle_events USING btree (source_instance_id, stream_id, event_id)"
lookup_index_state() {
  psql_readonly -At -c "SELECT CASE WHEN count(*)=1 THEN 'exact' ELSE 'mismatch' END
FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class idx ON idx.oid=i.indexrelid
JOIN pg_catalog.pg_namespace ns ON ns.oid=idx.relnamespace JOIN pg_catalog.pg_am am ON am.oid=idx.relam
WHERE ns.nspname='public' AND idx.relname='$LOOKUP_INDEX' AND am.amname='btree'
AND idx.relkind='i' AND idx.relpersistence='p'
AND i.indrelid='public.source_economic_scan_cycle_events'::regclass AND i.indnatts=3 AND i.indnkeyatts=3
AND i.indexprs IS NULL AND i.indpred IS NULL
AND pg_get_indexdef(idx.oid,1,true)='source_instance_id'
AND pg_get_indexdef(idx.oid,2,true)='stream_id' AND pg_get_indexdef(idx.oid,3,true)='event_id'
AND NOT i.indisunique AND NOT i.indisprimary AND NOT i.indisexclusion
AND i.indisvalid AND i.indisready AND i.indislive"
}
readonly NAMED_LOOKUP_COUNT="$(psql_readonly -At -c "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='$LOOKUP_INDEX'")"
if [[ "$NAMED_LOOKUP_COUNT" == 0 ]]; then
  printf '%s;\n' "SET statement_timeout='2h'" "SET lock_timeout='5s'" "$LOOKUP_CREATE" |
    timeout --foreground --kill-after=30s 7500s "${PROD_COMPOSE[@]}" exec -T postgres \
      psql -X -q -v ON_ERROR_STOP=1 -U invoice_owner -d invoice \
      >"$RECORD_DIR/create-lookup-index.stdout" 2>"$RECORD_DIR/create-lookup-index.stderr"
else
  [[ "$NAMED_LOOKUP_COUNT" == 1 ]] || die 'lookup index name is ambiguous'
fi
[[ "$(lookup_index_state)" == exact ]] || die 'exact valid/ready/live FK lookup index is required'
readonly DELETE_CATALOG_STATE="$(psql_readonly -At -c "
SELECT CASE WHEN
  (SELECT count(*) FROM pg_constraint c WHERE c.contype='f'
   AND c.conrelid='public.source_economic_scan_cycle_events'::regclass
   AND c.confrelid='public.source_ingest_events'::regclass
   AND c.conkey=ARRAY[1,2,4]::smallint[] AND c.confkey=ARRAY[1,2,3]::smallint[]
   AND c.confdeltype='r')=1
  AND (SELECT count(*) FROM pg_constraint c WHERE c.contype='f'
       AND c.confrelid='public.source_ingest_events'::regclass)=1
  AND NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.contype='f'
                  AND c.confrelid='public.source_economic_scan_cycle_events'::regclass)
  AND NOT EXISTS (SELECT 1 FROM pg_trigger t WHERE t.tgrelid IN (
       'public.source_ingest_events'::regclass,'public.source_economic_scan_cycle_events'::regclass)
       AND NOT t.tgisinternal AND (t.tgtype & 8)=8)
THEN 'exact' ELSE 'mismatch' END")"
[[ "$DELETE_CATALOG_STATE" == exact ]] || die 'FK/delete-trigger catalog contract mismatch'
printf '%s\n' "$DELETE_CATALOG_STATE" >"$RECORD_DIR/delete-catalog-contract.txt"

postgres_exec /bin/sh -ceu 'umask 077; : >"$1"; : >"$2"' sh \
  "$KEEP_DIGEST_ROWS" "$DIGEST_ROWS"
for scratch_file in "$KEEP_DIGEST_ROWS" "$DIGEST_ROWS"; do
  [[ "$(postgres_exec stat -c '%u:%g:%a' "$scratch_file")" == '0:0:600' ]] || die 'digest scratch file must be root:root mode 0600'
  postgres_exec test -f "$scratch_file" && ! postgres_exec test -L "$scratch_file" || die 'digest scratch file must be regular and non-symlink'
done
readonly KEEP_ROWS_INODE="$(postgres_exec stat -Lc '%d:%i' "$KEEP_DIGEST_ROWS")"
readonly PURGE_ROWS_INODE="$(postgres_exec stat -Lc '%d:%i' "$DIGEST_ROWS")"
readonly WAL_BEFORE="$(psql_readonly -At -c 'SELECT pg_current_wal_lsn()')"
if [[ "$CLEANUP_STATE" == prestate ]]; then
  readonly DELETE_START_SECONDS=$SECONDS
  timeout --foreground --kill-after=30s 7500s "${PROD_COMPOSE[@]}" exec -T postgres \
    psql -X -q -v ON_ERROR_STOP=1 -U invoice_owner -d invoice \
    -v apply_cleanup=true \
    -v expected_keep_sha256="$EXPECTED_KEEP_SHA256" -v expected_purge_sha256="$EXPECTED_PURGE_SHA256" \
    -v keep_rows_path="$KEEP_DIGEST_ROWS" -v purge_rows_path="$DIGEST_ROWS" -f - \
    <"$SQL_PATH" >"$RECORD_DIR/delete.stdout" 2>"$RECORD_DIR/delete.stderr"
  readonly DELETE_DURATION_SECONDS=$((SECONDS-DELETE_START_SECONDS))
  readonly WAL_AFTER_DELETE="$(psql_readonly -At -c 'SELECT pg_current_wal_lsn()')"
  readonly DELETE_WAL_BYTES="$(psql_readonly -At -c "SELECT pg_wal_lsn_diff('$WAL_AFTER_DELETE','$WAL_BEFORE')::numeric(30,0)")"
  [[ "$KEEP_ROWS_INODE" == "$(postgres_exec stat -Lc '%d:%i' "$KEEP_DIGEST_ROWS")" &&
     "$PURGE_ROWS_INODE" == "$(postgres_exec stat -Lc '%d:%i' "$DIGEST_ROWS")" ]] || die 'digest scratch inode changed'
  postgres_exec sha256sum "$DIGEST_ROWS" | cut -d' ' -f1 >"$RECORD_DIR/purge.sha256"
  postgres_exec sha256sum "$KEEP_DIGEST_ROWS" | cut -d' ' -f1 >"$RECORD_DIR/keep.sha256"
  [[ "$(<"$RECORD_DIR/purge.sha256")" == "$EXPECTED_PURGE_SHA256" ]] || die 'post-transaction purge digest mismatch'
  [[ "$(<"$RECORD_DIR/keep.sha256")" == "$EXPECTED_KEEP_SHA256" ]] || die 'post-transaction keep digest mismatch'
  printf 'state=prestate-delete\nduration_seconds=%s\nwal_bytes=%s\nwal_before=%s\nwal_after_delete=%s\n' \
    "$DELETE_DURATION_SECONDS" "$DELETE_WAL_BYTES" "$WAL_BEFORE" "$WAL_AFTER_DELETE" >"$RECORD_DIR/delete-metrics.env"
else
  printf '%s\n' "$EXPECTED_PURGE_SHA256" >"$RECORD_DIR/purge.sha256"
  printf '%s\n' "$EXPECTED_KEEP_SHA256" >"$RECORD_DIR/keep.sha256"
  printf 'state=poststate-reconciliation\nduration_seconds=0\nwal_bytes=0\nwal_before=%s\nwal_after_delete=%s\n' \
    "$WAL_BEFORE" "$WAL_BEFORE" >"$RECORD_DIR/delete-metrics.env"
fi

readonly VACUUM_START_SECONDS=$SECONDS
"${PROD_COMPOSE[@]}" exec -T \
  -e PGOPTIONS='-c statement_timeout=2h -c lock_timeout=5s' postgres \
  psql -X -q -v ON_ERROR_STOP=1 -U invoice_owner -d invoice \
  -c 'VACUUM (ANALYZE) public.source_economic_scan_cycle_events' \
  -c 'VACUUM (ANALYZE) public.source_ingest_events' \
  >"$RECORD_DIR/vacuum.stdout" 2>"$RECORD_DIR/vacuum.stderr"
printf 'duration_seconds=%s\nwal_after_vacuum=%s\n' "$((SECONDS-VACUUM_START_SECONDS))" \
  "$(psql_readonly -At -c 'SELECT pg_current_wal_lsn()')" >"$RECORD_DIR/vacuum-metrics.env"

capture_guard_counts "$RECORD_DIR/counts-after.tsv"
psql_readonly -At -F '|' -c "SELECT name,checksum FROM public.schema_migrations ORDER BY name" >"$RECORD_DIR/schema-migrations-after.tsv"
cmp -s "$RECORD_DIR/schema-migrations-before.tsv" "$RECORD_DIR/schema-migrations-after.tsv" || die 'schema_migrations changed during cleanup'
psql_readonly -At -F '|' -c "SELECT processing_status,count(*) FROM public.source_ingest_events GROUP BY processing_status ORDER BY processing_status" >"$RECORD_DIR/status-after.tsv"
readonly FINAL_FREEZE_STATE="$(psql_readonly -At -c "SELECT count(*) FILTER (WHERE processing_status IN ('queued','processing','failed','dead')) FROM public.source_ingest_events")|$(psql_readonly -At -c "$TARGET_COUNT_SQL")"
[[ "$FINAL_FREEZE_STATE" == '0|5494' ]] || die "post-cleanup source event/target tuple mismatch: $FINAL_FREEZE_STATE"
readonly PARKED_TOTAL_AFTER="$(psql_readonly -At -c "SELECT count(*) FROM public.source_ingest_events WHERE processing_status='parked_identity'")"
[[ "$PARKED_TOTAL_AFTER" == "$EXPECTED_PARKED_POSTSTATE" ]] || die "post-cleanup parked total mismatch: $PARKED_TOTAL_AFTER"

# Only the two target tables may differ, and by exactly D rows each.
before_events=$(grep '^source_ingest_events|' "$RECORD_DIR/counts-before.tsv" | cut -d'|' -f2)
after_events=$(grep '^source_ingest_events|' "$RECORD_DIR/counts-after.tsv" | cut -d'|' -f2)
before_mappings=$(grep '^source_economic_scan_cycle_events|' "$RECORD_DIR/counts-before.tsv" | cut -d'|' -f2)
after_mappings=$(grep '^source_economic_scan_cycle_events|' "$RECORD_DIR/counts-after.tsv" | cut -d'|' -f2)
if [[ "$CLEANUP_STATE" == prestate ]]; then
  [[ "$((before_events-after_events))" == 1873803 && "$((before_mappings-after_mappings))" == 1873803 ]] ||
    die 'target table count deltas differ from D=1873803'
else
  [[ "$before_events" == "$after_events" && "$before_mappings" == "$after_mappings" ]] ||
    die 'poststate reconciliation observed unexpected target-table drift'
fi
grep -Ev '^(source_ingest_events|source_economic_scan_cycle_events)\|' "$RECORD_DIR/counts-before.tsv" >"$RECORD_DIR/non-target-before.tsv"
grep -Ev '^(source_ingest_events|source_economic_scan_cycle_events)\|' "$RECORD_DIR/counts-after.tsv" >"$RECORD_DIR/non-target-after.tsv"
cmp -s "$RECORD_DIR/non-target-before.tsv" "$RECORD_DIR/non-target-after.tsv" || die 'non-target counts changed'

OPERATION_STATUS=passed
FAILURE_LINE=none
printf 'balance history cleanup passed; sign evidence manifest: %s\n' "$RECORD_DIR/BALANCE-HISTORY-CLEANUP.sha256"
