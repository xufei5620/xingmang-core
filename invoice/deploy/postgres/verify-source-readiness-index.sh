#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
export LC_ALL=C

readonly INDEX_NAME='source_ingest_events_readiness_active_idx'
readonly TABLE_NAME='source_ingest_events'
readonly DATABASE_NAME='invoice'
readonly DATABASE_USER='invoice_owner'

die() {
  printf 'source readiness index verification failed: %s\n' "$*" >&2
  exit 1
}

[[ "$(id -u)" == '0' ]] || die 'run as root'
: "${PRODUCTION_ENV_FILE:?set PRODUCTION_ENV_FILE to the reviewed production environment file}"
: "${EXPECTED_API_IMAGE_ID:?set EXPECTED_API_IMAGE_ID to the exact old API image ID}"
: "${EXPECTED_VERIFIER_SHA256:?set EXPECTED_VERIFIER_SHA256 to the reviewed verifier SHA-256}"
: "${VERIFICATION_RECORD_DIR:?set VERIFICATION_RECORD_DIR to a new root-only evidence directory}"

[[ "$EXPECTED_API_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || die 'EXPECTED_API_IMAGE_ID is not a sha256 image ID'
[[ "$EXPECTED_VERIFIER_SHA256" =~ ^[0-9a-f]{64}$ ]] || die 'EXPECTED_VERIFIER_SHA256 is not a lowercase SHA-256'

for command_name in awk basename cat chmod cmp cut date dirname docker find grep id mkdir realpath sha256sum sort stat timeout xargs; do
  command -v "$command_name" >/dev/null || die "required command is unavailable: $command_name"
done

readonly SCRIPT_PATH="$(realpath -- "${BASH_SOURCE[0]}")"
[[ -f "$SCRIPT_PATH" && ! -L "$SCRIPT_PATH" ]] || die 'verifier must be a regular non-symlink file'
[[ "$(stat -c '%u:%g:%a' "$SCRIPT_PATH")" == '0:0:700' ]] || die 'installed verifier must be root:root mode 0700'
readonly ACTUAL_VERIFIER_SHA256="$(sha256sum "$SCRIPT_PATH" | cut -d' ' -f1)"
[[ "$ACTUAL_VERIFIER_SHA256" == "$EXPECTED_VERIFIER_SHA256" ]] || die 'installed verifier hash differs from the reviewed hash'

check_root_config_file() {
  local path=$1
  local label=$2
  [[ -f "$path" && ! -L "$path" && -s "$path" ]] || die "$label must be a nonempty regular non-symlink file"
  [[ "$(stat -c '%u:%g' "$path")" == '0:0' ]] || die "$label must be root-owned"
  local mode
  mode=$(stat -c '%a' "$path")
  (( (8#$mode & 8#022) == 0 )) || die "$label must not be group/other writable"
  (( $(stat -c '%s' "$path") <= 1048576 )) || die "$label is unexpectedly large"
}

check_root_directory() {
  local path=$1
  local label=$2
  [[ -d "$path" && ! -L "$path" ]] || die "$label must be a regular directory"
  [[ "$(stat -c '%u:%g:%a' "$path")" == '0:0:700' ]] || die "$label must be root:root mode 0700"
}

check_root_config_file "$PRODUCTION_ENV_FILE" 'production environment file'
readonly PROJECT_ROOT="$(cd "$(dirname "$SCRIPT_PATH")/../.." && pwd -P)"
readonly COMPOSE_FILE="$PROJECT_ROOT/deploy/docker-compose.prod.yml"
check_root_config_file "$COMPOSE_FILE" 'production Compose file'

readonly RECORD_PARENT="$(dirname "$VERIFICATION_RECORD_DIR")"
check_root_directory "$RECORD_PARENT" 'verification record parent'
[[ ! -e "$VERIFICATION_RECORD_DIR" ]] || die 'verification record directory already exists'
mkdir -m 0700 -- "$VERIFICATION_RECORD_DIR"
[[ "$(realpath -- "$VERIFICATION_RECORD_DIR")" == "$(realpath -- "$RECORD_PARENT")/$(basename "$VERIFICATION_RECORD_DIR")" ]] ||
  die 'verification record directory did not resolve beneath its expected parent'

readonly -a COMPOSE=(docker compose --env-file "$PRODUCTION_ENV_FILE" -f "$COMPOSE_FILE")

psql_readonly() {
  "${COMPOSE[@]}" exec -T \
    -e PGOPTIONS='-c default_transaction_read_only=on -c statement_timeout=5s -c lock_timeout=3s' \
    postgres psql -X -q -v ON_ERROR_STOP=1 -U "$DATABASE_USER" -d "$DATABASE_NAME" "$@"
}

capture_schema_migrations() {
  local output=$1
  psql_readonly -At -F '|' -c \
    "SELECT name,checksum,to_char(applied_at AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"') FROM public.schema_migrations ORDER BY name" \
    >"$output"
}

capture_api_state() {
  local output=$1
  local container_output
  local -a ids=()
  container_output=$("${COMPOSE[@]}" ps --status running -q api) || die 'cannot inspect old API container'
  [[ -n "$container_output" ]] && mapfile -t ids <<<"$container_output"
  (( ${#ids[@]} == 1 )) || die 'expected exactly one running old API container'
  local container_id=${ids[0]}
  local state
  state=$(docker inspect --format '{{.State.Running}}|{{.Image}}|{{.State.StartedAt}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$container_id") ||
    die 'cannot inspect old API container state'
  local running image_id started_at docker_health
  IFS='|' read -r running image_id started_at docker_health <<<"$state"
  [[ "$running" == 'true' ]] || die 'old API container is not running'
  [[ "$image_id" == "$EXPECTED_API_IMAGE_ID" ]] || die 'old API container image differs from EXPECTED_API_IMAGE_ID'
  local health_body
  health_body=$(timeout 10s docker exec "$container_id" wget -q -T 5 -O - http://127.0.0.1:8088/healthz) ||
    die 'old API /healthz did not return successfully'
  grep -Fq '"status":"ok"' <<<"$health_body" || die 'old API /healthz body is not healthy'
  grep -Fq '"source_mode":"agent"' <<<"$health_body" || die 'old API source mode drifted'
  grep -Fq '"auth_mode":"oidc"' <<<"$health_body" || die 'old API auth mode drifted'
  printf 'container_id=%s\nimage_id=%s\nstarted_at=%s\ndocker_health=%s\nhealthz_status=ok\n' \
    "$container_id" "$image_id" "$started_at" "$docker_health" >"$output"
}

assert_same_api_identity() {
  local before=$1
  local after=$2
  local before_identity after_identity
  before_identity=$(grep -E '^(container_id|image_id|started_at)=' "$before")
  after_identity=$(grep -E '^(container_id|image_id|started_at)=' "$after")
  [[ "$before_identity" == "$after_identity" ]] ||
    die 'old API container identity, image, or start time changed during verification'
}

readonly TABLE_ASSERTION_SQL="
SELECT CASE WHEN
  c.oid='public.${TABLE_NAME}'::pg_catalog.regclass
  AND n.nspname='public'
  AND c.relname='${TABLE_NAME}'
  AND c.relkind='r'
  AND c.relpersistence='p'
  AND pg_catalog.pg_get_userbyid(c.relowner)='invoice_owner'
  AND (SELECT count(*) FROM pg_catalog.pg_attribute a
       WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
         AND a.attname IN ('source_instance_id','stream_id','processing_status','created_at'))=4
  AND EXISTS (SELECT 1 FROM pg_catalog.pg_attribute a WHERE a.attrelid=c.oid AND a.attname='source_instance_id' AND a.atttypid='uuid'::pg_catalog.regtype AND a.attnotnull AND a.attgenerated='' AND a.attidentity='')
  AND EXISTS (SELECT 1 FROM pg_catalog.pg_attribute a WHERE a.attrelid=c.oid AND a.attname='stream_id' AND a.atttypid='text'::pg_catalog.regtype AND a.attnotnull AND a.attgenerated='' AND a.attidentity='')
  AND EXISTS (SELECT 1 FROM pg_catalog.pg_attribute a WHERE a.attrelid=c.oid AND a.attname='processing_status' AND a.atttypid='text'::pg_catalog.regtype AND a.attnotnull AND a.attgenerated='' AND a.attidentity='')
  AND EXISTS (SELECT 1 FROM pg_catalog.pg_attribute a WHERE a.attrelid=c.oid AND a.attname='created_at' AND a.atttypid='timestamp with time zone'::pg_catalog.regtype AND a.attnotnull AND a.attgenerated='' AND a.attidentity='')
THEN 'exact' ELSE 'mismatch' END
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
WHERE c.oid='public.${TABLE_NAME}'::pg_catalog.regclass"

readonly INDEX_ASSERTION_SQL="
SELECT CASE WHEN count(*)=1 THEN 'exact' ELSE 'mismatch' END
FROM pg_catalog.pg_index i
JOIN pg_catalog.pg_class idx ON idx.oid=i.indexrelid
JOIN pg_catalog.pg_namespace idx_ns ON idx_ns.oid=idx.relnamespace
JOIN pg_catalog.pg_am am ON am.oid=idx.relam
JOIN pg_catalog.pg_class tbl ON tbl.oid=i.indrelid
JOIN pg_catalog.pg_namespace tbl_ns ON tbl_ns.oid=tbl.relnamespace
WHERE idx.relname='${INDEX_NAME}'
  AND idx_ns.nspname='public'
  AND idx.relkind='i'
  AND idx.relpersistence='p'
  AND idx.reltablespace=0
  AND idx.reloptions IS NULL
  AND idx.relowner=tbl.relowner
  AND am.amname='btree'
  AND tbl.relname='${TABLE_NAME}'
  AND tbl_ns.nspname='public'
  AND tbl.relkind='r'
  AND i.indrelid='public.${TABLE_NAME}'::pg_catalog.regclass
  AND i.indnatts=4
  AND i.indnkeyatts=4
  AND i.indexprs IS NULL
  AND pg_catalog.pg_get_indexdef(idx.oid,1,true)='source_instance_id'
  AND pg_catalog.pg_get_indexdef(idx.oid,2,true)='stream_id'
  AND pg_catalog.pg_get_indexdef(idx.oid,3,true)='processing_status'
  AND pg_catalog.pg_get_indexdef(idx.oid,4,true)='created_at'
  AND i.indpred IS NOT NULL
  AND pg_catalog.regexp_replace(pg_catalog.pg_get_expr(i.indpred,i.indrelid,false),'\\s+','','g')='(processing_status=ANY(ARRAY[''queued''::text,''failed''::text,''processing''::text,''dead''::text]))'
  AND NOT i.indisunique
  AND NOT i.indisprimary
  AND NOT i.indisexclusion
  AND NOT i.indisclustered
  AND NOT i.indisreplident
  AND i.indisvalid
  AND i.indisready
  AND i.indislive"

capture_index_catalog() {
  local output=$1
  psql_readonly -At -F '|' -c "
SELECT idx_ns.nspname,idx.relname,am.amname,tbl_ns.nspname,tbl.relname,
       i.indnatts,i.indnkeyatts,i.indexprs IS NULL,
       pg_catalog.pg_get_indexdef(idx.oid),pg_catalog.pg_get_expr(i.indpred,i.indrelid,false),
       i.indisunique,i.indisprimary,i.indisexclusion,i.indisclustered,i.indisreplident,
       i.indisvalid,i.indisready,i.indislive,idx.reltablespace,COALESCE(array_to_string(idx.reloptions,','),'')
FROM pg_catalog.pg_class idx
JOIN pg_catalog.pg_namespace idx_ns ON idx_ns.oid=idx.relnamespace
LEFT JOIN pg_catalog.pg_index i ON i.indexrelid=idx.oid
LEFT JOIN pg_catalog.pg_am am ON am.oid=idx.relam
LEFT JOIN pg_catalog.pg_class tbl ON tbl.oid=i.indrelid
LEFT JOIN pg_catalog.pg_namespace tbl_ns ON tbl_ns.oid=tbl.relnamespace
WHERE idx_ns.nspname='public' AND idx.relname='${INDEX_NAME}'" >"$output"
}

# emit_readiness_query is a hand-maintained copy of
# backend/internal/postgresstore/source_sync.go's sourceReadinessHealthQuery.
# A copy of a query is a gate that can quietly stop testing the thing it
# names: this copy had already fallen behind by three separate changes (the
# `classified` subquery and its busy_within_grace column, the scan-cycle join,
# and the cutover-manifest subquery), so every green run of this script was
# proving an index path for a query the service no longer runs.
#
# Re-synchronised on 2026-09-08 for XM-INV-DEAD-CONTAINMENT, which adds the
# dead_contained flag -- and then pinned, because re-synchronising a copy by
# hand fixes the symptom and leaves the mechanism. A freshly-synced copy is
# more dangerous than a visibly stale one: it is trusted, and the next edit
# re-orphans it silently.
#
# TestVerifyScriptPlansTheQueryTheServiceActuallyRuns
# (backend/internal/postgresstore/verify_script_query_sync_test.go) now reads
# the heredoc below, normalises it, and requires it to equal
# sourceReadinessHealthQuery. Editing either one without the other fails in the
# same commit that causes the drift, and prints the first divergence. Do not
# reformat the markers `emit_readiness_query() {` / `  cat <<'SQL'` / the
# closing `SQL` line: the pin locates the copy by them.
#
# This script is still not the only gate. TestSourceReadinessHealthIgnoresParked\
# BacklogAndUsesPartialIndex and TestContainedDeadFilterKeepsReadinessOnThe\
# ActivePartialIndex, in source_readiness_integration_test.go, EXPLAIN the live
# query text. This script exists to check the *production* planner against
# production statistics, which no test can do.
#
# Differences from the Go source that are intentional and not drift: the
# `public.` schema qualifications, and the absence of Go's string
# concatenation (the transient-requeue marker array and the contained-dead
# predicate are rendered here literally).
emit_readiness_query() {
  cat <<'SQL'
WITH active_event_health AS MATERIALIZED (
  SELECT source_instance_id,stream_id,
    count(*) FILTER (WHERE processing_status IN ('queued','failed','processing') AND NOT busy_within_grace) AS pending_events,
    count(*) FILTER (WHERE is_dead) AS dead_events,
    count(*) FILTER (WHERE dead_contained) AS dead_events_contained,
    COALESCE(min(created_at) FILTER (WHERE processing_status IN ('queued','failed','processing') AND NOT busy_within_grace),'epoch'::timestamptz) AS oldest_pending
  FROM (
    SELECT sie.source_instance_id,sie.stream_id,sie.processing_status,sie.created_at,
      sie.processing_status='dead' AS is_dead,
      sie.processing_status='dead' AND EXISTS (SELECT 1 FROM public.eligibility_freezes ef WHERE ef.status='open' AND ef.source_revision_hash IS NOT NULL AND ef.source_revision_hash=sie.payload_hash) AS dead_contained,
      COALESCE(sie.processing_status='queued' AND COALESCE(sie.processing_error,'')=ANY(ARRAY['ACCOUNT_LOCK_BUSY','SERIALIZATION_BUSY'])
       AND sie.updated_at>=now()-interval '10 minutes',false) AS busy_within_grace
    FROM public.source_ingest_events sie
    WHERE sie.processing_status IN ('queued','failed','processing','dead')
  ) classified
  GROUP BY source_instance_id,stream_id
), ingest_health AS (
  SELECT COALESCE(sum(pending_events),0)::bigint AS pending_events,
    COALESCE(sum(dead_events),0)::bigint AS dead_events,
    COALESCE(sum(dead_events_contained),0)::bigint AS dead_events_contained,
    COALESCE(min(oldest_pending) FILTER (WHERE pending_events>0),'epoch'::timestamptz) AS oldest_pending
  FROM active_event_health
)
SELECT si.id,si.source_type,si.name,si.enabled,required.stream_id,
  COALESCE(sis.sequence,0),si.runtime_version,COALESCE((SELECT scm.source_runtime_version FROM public.source_cutover_manifests scm WHERE scm.source_instance_id=si.id ORDER BY scm.cutover_at DESC LIMIT 1),''),
  COALESCE(sis.source_runtime_version,''),COALESCE(sis.source_agent_version,''),COALESCE(sis.projection_status,'unknown'),
  COALESCE(sis.last_accepted_at,'epoch'::timestamptz),
  COALESCE(sis.last_nonempty_batch_at,'epoch'::timestamptz),
  COALESCE(sew.watermark_at,'epoch'::timestamptz),
  COALESCE(aeh.pending_events,0),COALESCE(aeh.dead_events,0),COALESCE(aeh.dead_events_contained,0),
  ingest.pending_events,ingest.dead_events,ingest.dead_events_contained,ingest.oldest_pending,
  COALESCE(sesc.updated_at,'epoch'::timestamptz)
FROM public.source_instances si
CROSS JOIN (VALUES ('payments'::text),('identities'::text),('usage'::text),('credits'::text),('balances'::text)) required(stream_id)
LEFT JOIN public.source_ingest_state sis ON sis.source_instance_id=si.id AND sis.stream_id=required.stream_id
LEFT JOIN public.source_economic_stream_watermarks sew ON sew.source_instance_id=si.id AND sew.stream_kind=required.stream_id
LEFT JOIN active_event_health aeh ON aeh.source_instance_id=si.id AND aeh.stream_id=required.stream_id
CROSS JOIN ingest_health ingest
LEFT JOIN public.source_economic_scan_cycles sesc ON sesc.source_instance_id=si.id AND sesc.stream_id=required.stream_id
  AND sesc.cycle_status IN ('receiving','processing')
ORDER BY si.source_type,si.id,required.stream_id
SQL
}

assert_plan() {
  local plan_file=$1
  grep -Fq "$INDEX_NAME" "$plan_file" || die "query plan does not use $INDEX_NAME"
  if grep -Eiq '(^|[[:space:]])(Parallel[[:space:]]+)?Seq Scan on (public\.)?source_ingest_events([[:space:]]|$)' "$plan_file"; then
    die 'query plan contains a source_ingest_events sequential scan'
  fi
}

readonly START_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
readonly START_SECONDS=$SECONDS
capture_api_state "$VERIFICATION_RECORD_DIR/api-before.env"
capture_schema_migrations "$VERIFICATION_RECORD_DIR/schema-migrations-before.tsv"

readonly SERVER_STATE="$(psql_readonly -At -F '|' -c "SELECT current_database(),current_user,current_setting('server_version_num')::integer/10000,current_setting('default_transaction_read_only')")"
[[ "$SERVER_STATE" == 'invoice|invoice_owner|18|on' ]] || die "unexpected PostgreSQL verifier session: $SERVER_STATE"
printf '%s\n' "$SERVER_STATE" >"$VERIFICATION_RECORD_DIR/postgres-session.txt"

readonly TABLE_STATE="$(psql_readonly -At -c "$TABLE_ASSERTION_SQL")"
[[ "$TABLE_STATE" == 'exact' ]] || die 'public.source_ingest_events table contract mismatch'
printf '%s\n' "$TABLE_STATE" >"$VERIFICATION_RECORD_DIR/table-contract.txt"

capture_index_catalog "$VERIFICATION_RECORD_DIR/index-catalog.tsv"
readonly INDEX_STATE="$(psql_readonly -At -c "$INDEX_ASSERTION_SQL")"
[[ "$INDEX_STATE" == 'exact' ]] || die 'same-name index is absent, invalid, not ready/live, or definition-mismatched'
printf '%s\n' "$INDEX_STATE" >"$VERIFICATION_RECORD_DIR/index-contract.txt"

{
  printf '%s\n' 'EXPLAIN (COSTS OFF, VERBOSE, SETTINGS, FORMAT TEXT)'
  emit_readiness_query
  printf ';\n'
} | timeout 15s "${COMPOSE[@]}" exec -T \
      -e PGOPTIONS='-c default_transaction_read_only=on -c statement_timeout=5s -c lock_timeout=3s' \
      postgres psql -X -q -v ON_ERROR_STOP=1 -U "$DATABASE_USER" -d "$DATABASE_NAME" -At \
      >"$VERIFICATION_RECORD_DIR/explain.txt"
assert_plan "$VERIFICATION_RECORD_DIR/explain.txt"

readonly EXPLAIN_ANALYZE_START_SECONDS=$SECONDS
{
  printf '%s\n' 'EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, VERBOSE, SETTINGS, SUMMARY, FORMAT TEXT)'
  emit_readiness_query
  printf ';\n'
} | timeout 10s "${COMPOSE[@]}" exec -T \
      -e PGOPTIONS='-c default_transaction_read_only=on -c statement_timeout=2500ms -c lock_timeout=3s' \
      postgres psql -X -q -v ON_ERROR_STOP=1 -U "$DATABASE_USER" -d "$DATABASE_NAME" -At \
      >"$VERIFICATION_RECORD_DIR/explain-analyze-buffers.txt"
readonly EXPLAIN_ANALYZE_DURATION_SECONDS=$((SECONDS-EXPLAIN_ANALYZE_START_SECONDS))
assert_plan "$VERIFICATION_RECORD_DIR/explain-analyze-buffers.txt"
mapfile -t execution_times_ms < <(
  awk '$1=="Execution" && $2=="Time:" && $4=="ms" { print $3 }' \
    "$VERIFICATION_RECORD_DIR/explain-analyze-buffers.txt"
)
(( ${#execution_times_ms[@]} == 1 )) || die 'EXPLAIN ANALYZE did not emit exactly one PostgreSQL Execution Time'
readonly READINESS_EXECUTION_TIME_MS="${execution_times_ms[0]}"
[[ "$READINESS_EXECUTION_TIME_MS" =~ ^[0-9]+([.][0-9]+)?$ ]] || die 'PostgreSQL Execution Time is not a nonnegative millisecond value'
awk -v milliseconds="$READINESS_EXECUTION_TIME_MS" 'BEGIN { exit !(milliseconds <= 2000.0) }' ||
  die "readiness query execution exceeded the 2000ms hard limit: ${READINESS_EXECUTION_TIME_MS}ms"
printf 'postgres_execution_time_ms=%s\nhard_limit_ms=2000\nstatement_timeout_ms=2500\nouter_timeout_seconds=10\n' \
  "$READINESS_EXECUTION_TIME_MS" >"$VERIFICATION_RECORD_DIR/explain-analyze-timing.env"

capture_schema_migrations "$VERIFICATION_RECORD_DIR/schema-migrations-after.tsv"
cmp -s "$VERIFICATION_RECORD_DIR/schema-migrations-before.tsv" "$VERIFICATION_RECORD_DIR/schema-migrations-after.tsv" ||
  die 'schema_migrations changed during read-only verification'
capture_api_state "$VERIFICATION_RECORD_DIR/api-after.env"
assert_same_api_identity "$VERIFICATION_RECORD_DIR/api-before.env" "$VERIFICATION_RECORD_DIR/api-after.env"

readonly FINISH_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
readonly DURATION_SECONDS=$((SECONDS-START_SECONDS))
readonly COMPOSE_SHA256="$(sha256sum "$COMPOSE_FILE" | cut -d' ' -f1)"
readonly ENV_SHA256="$(sha256sum "$PRODUCTION_ENV_FILE" | cut -d' ' -f1)"
readonly MIGRATIONS_SHA256="$(sha256sum "$VERIFICATION_RECORD_DIR/schema-migrations-after.tsv" | cut -d' ' -f1)"
cat >"$VERIFICATION_RECORD_DIR/result.env" <<RESULT
status=passed
operation=rc39_source_readiness_index_readonly_verification
started_utc=$START_UTC
finished_utc=$FINISH_UTC
duration_seconds=$DURATION_SECONDS
explain_analyze_duration_seconds=$EXPLAIN_ANALYZE_DURATION_SECONDS
readiness_execution_time_ms=$READINESS_EXECUTION_TIME_MS
readiness_execution_hard_limit_ms=2000
verifier_sha256=$ACTUAL_VERIFIER_SHA256
compose_sha256=$COMPOSE_SHA256
production_env_sha256=$ENV_SHA256
schema_migrations_sha256=$MIGRATIONS_SHA256
database_read_only=true
source_ingest_events_seq_scan=false
index_contract=exact
old_api_liveness=passed
RESULT

(
  cd "$VERIFICATION_RECORD_DIR"
  find . -type f ! -name 'RC39-SOURCE-READINESS-INDEX-VERIFY.sha256' -printf '%P\0' |
    sort -z | xargs -0 sha256sum -- >RC39-SOURCE-READINESS-INDEX-VERIFY.sha256
)
chmod -R go-rwx -- "$VERIFICATION_RECORD_DIR"
printf 'source readiness index read-only verification passed; signing input: %s\n' \
  "$VERIFICATION_RECORD_DIR/RC39-SOURCE-READINESS-INDEX-VERIFY.sha256"
