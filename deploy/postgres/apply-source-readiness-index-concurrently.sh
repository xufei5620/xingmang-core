#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
export LC_ALL=C

readonly INDEX_NAME='source_ingest_events_readiness_active_idx'
readonly TABLE_NAME='source_ingest_events'
readonly DATABASE_NAME='invoice'
readonly DATABASE_USER='invoice_owner'
readonly CREATE_INDEX_SQL="CREATE INDEX CONCURRENTLY source_ingest_events_readiness_active_idx ON public.source_ingest_events USING btree (source_instance_id, stream_id, processing_status, created_at) WHERE processing_status IN ('queued','failed','processing','dead')"

die() {
  printf 'RC39 source readiness index operator failed: %s\n' "$*" >&2
  exit 1
}

[[ "$(id -u)" == '0' ]] || die 'run as root'
: "${SOURCE_READINESS_INDEX_CONFIRMED:?set SOURCE_READINESS_INDEX_CONFIRMED=YES after reviewing this one-time production operation}"
[[ "$SOURCE_READINESS_INDEX_CONFIRMED" == 'YES' ]] || die 'SOURCE_READINESS_INDEX_CONFIRMED must equal YES'
: "${PRODUCTION_ENV_FILE:?set PRODUCTION_ENV_FILE to the reviewed production environment file}"
: "${EXPECTED_API_IMAGE_ID:?set EXPECTED_API_IMAGE_ID to the exact old API image ID}"
: "${EXPECTED_OPERATOR_SHA256:?set EXPECTED_OPERATOR_SHA256 to the reviewed operator SHA-256}"
: "${EXPECTED_VERIFIER_SHA256:?set EXPECTED_VERIFIER_SHA256 to the reviewed verifier SHA-256}"
: "${RECORD_ROOT:?set RECORD_ROOT to the fixed root-only deployment-record directory}"

[[ "$EXPECTED_API_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || die 'EXPECTED_API_IMAGE_ID is not a sha256 image ID'
[[ "$EXPECTED_OPERATOR_SHA256" =~ ^[0-9a-f]{64}$ ]] || die 'EXPECTED_OPERATOR_SHA256 is not a lowercase SHA-256'
[[ "$EXPECTED_VERIFIER_SHA256" =~ ^[0-9a-f]{64}$ ]] || die 'EXPECTED_VERIFIER_SHA256 is not a lowercase SHA-256'

for command_name in basename chmod cmp cut date dirname docker find flock grep id mkdir realpath sha256sum sort stat sync timeout xargs; do
  command -v "$command_name" >/dev/null || die "required command is unavailable: $command_name"
done

readonly SCRIPT_PATH="$(realpath -- "${BASH_SOURCE[0]}")"
readonly SCRIPT_DIR="$(dirname "$SCRIPT_PATH")"
readonly VERIFIER_PATH="$SCRIPT_DIR/verify-source-readiness-index.sh"
[[ -f "$SCRIPT_PATH" && ! -L "$SCRIPT_PATH" ]] || die 'operator must be a regular non-symlink file'
[[ "$(stat -c '%u:%g:%a' "$SCRIPT_PATH")" == '0:0:700' ]] || die 'installed operator must be root:root mode 0700'
[[ -f "$VERIFIER_PATH" && ! -L "$VERIFIER_PATH" ]] || die 'verifier must be installed beside the operator'
[[ "$(stat -c '%u:%g:%a' "$VERIFIER_PATH")" == '0:0:700' ]] || die 'installed verifier must be root:root mode 0700'
readonly ACTUAL_OPERATOR_SHA256="$(sha256sum "$SCRIPT_PATH" | cut -d' ' -f1)"
readonly ACTUAL_VERIFIER_SHA256="$(sha256sum "$VERIFIER_PATH" | cut -d' ' -f1)"
[[ "$ACTUAL_OPERATOR_SHA256" == "$EXPECTED_OPERATOR_SHA256" ]] || die 'installed operator hash differs from the reviewed hash'
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

lock_parent_mode_is_safe() {
  local mode=$1
  [[ "$mode" =~ ^[0-7]{3,4}$ ]] || return 1
  (( (8#$mode & 8#002) == 0 || (8#$mode & 8#1000) != 0 ))
}

check_root_config_file "$PRODUCTION_ENV_FILE" 'production environment file'
check_root_directory "$RECORD_ROOT" 'deployment record root'
readonly PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd -P)"
readonly COMPOSE_FILE="$PROJECT_ROOT/deploy/docker-compose.prod.yml"
check_root_config_file "$COMPOSE_FILE" 'production Compose file'

readonly -a COMPOSE=(docker compose --env-file "$PRODUCTION_ENV_FILE" -f "$COMPOSE_FILE")
readonly LOCK_PARENT='/run/lock'
readonly LOCK_DIRECTORY="$LOCK_PARENT/solov-invoice-source-readiness-index"
readonly LOCK_FILE="$LOCK_DIRECTORY/operator.lock"
[[ -d "$LOCK_PARENT" && ! -L "$LOCK_PARENT" ]] || die '/run/lock must be a regular non-symlink directory'
[[ "$(stat -c '%u:%g' "$LOCK_PARENT")" == '0:0' ]] || die '/run/lock must be root-owned'
lock_parent_mode=$(stat -c '%a' "$LOCK_PARENT")
lock_parent_mode_is_safe "$lock_parent_mode" ||
  die '/run/lock must not be world-writable unless the sticky bit is set'
if [[ ! -e "$LOCK_DIRECTORY" && ! -L "$LOCK_DIRECTORY" ]]; then
  mkdir -m 0700 -- "$LOCK_DIRECTORY" || die 'cannot create the dedicated lock directory'
fi
[[ -d "$LOCK_DIRECTORY" && ! -L "$LOCK_DIRECTORY" ]] || die 'dedicated lock path must be a regular non-symlink directory'
[[ "$(stat -c '%u:%g:%a' "$LOCK_DIRECTORY")" == '0:0:700' ]] || die 'dedicated lock directory must be root:root mode 0700'
[[ "$(realpath -- "$LOCK_DIRECTORY")" == "$LOCK_DIRECTORY" ]] || die 'dedicated lock directory resolved through an unexpected path'
if [[ ! -e "$LOCK_FILE" && ! -L "$LOCK_FILE" ]]; then
  ( set -o noclobber; : >"$LOCK_FILE" ) 2>/dev/null || die 'cannot safely create the lock file'
fi
[[ -f "$LOCK_FILE" && ! -L "$LOCK_FILE" ]] || die 'lock file must be a regular non-symlink file'
[[ "$(stat -c '%u:%g:%a' "$LOCK_FILE")" == '0:0:600' ]] || die 'lock file must be root:root mode 0600'
exec 9<>"$LOCK_FILE"
readonly LOCK_PATH_DEVICE_INODE="$(stat -Lc '%d:%i' "$LOCK_FILE")"
readonly LOCK_FD_DEVICE_INODE="$(stat -Lc '%d:%i' "/proc/$$/fd/9")"
[[ "$LOCK_PATH_DEVICE_INODE" == "$LOCK_FD_DEVICE_INODE" ]] || die 'opened lock descriptor does not match the reviewed lock path'
flock -n 9 || die 'another source readiness index operation is running'
[[ -f "$LOCK_FILE" && ! -L "$LOCK_FILE" ]] || die 'lock path changed after flock'
[[ "$(stat -Lc '%d:%i' "$LOCK_FILE")" == "$LOCK_FD_DEVICE_INODE" ]] || die 'lock path inode changed after flock'

readonly TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
readonly RECORD_DIR="$RECORD_ROOT/rc39-source-readiness-index-$TIMESTAMP"
[[ ! -e "$RECORD_DIR" ]] || die 'timestamped deployment record already exists'
mkdir -m 0700 -- "$RECORD_DIR"
readonly START_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
readonly START_SECONDS=$SECONDS
OPERATION_STATUS='failed'
INDEX_ACTION='not-attempted'
FAILURE_LINE='none'

psql_owner() {
  "${COMPOSE[@]}" exec -T postgres psql -X -q -v ON_ERROR_STOP=1 -U "$DATABASE_USER" -d "$DATABASE_NAME" "$@"
}

psql_readonly() {
  "${COMPOSE[@]}" exec -T \
    -e PGOPTIONS='-c default_transaction_read_only=on -c statement_timeout=15s -c lock_timeout=3s' \
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
    die 'old API container identity, image, or start time changed during index prebuild'
}

capture_index_catalog() {
  local output=$1
  psql_readonly -At -F '|' -c "
SELECT idx_ns.nspname,idx.relname,COALESCE(am.amname,''),COALESCE(tbl_ns.nspname,''),COALESCE(tbl.relname,''),
       COALESCE(i.indnatts,0),COALESCE(i.indnkeyatts,0),i.indexprs IS NULL,
       pg_catalog.pg_get_indexdef(idx.oid),COALESCE(pg_catalog.pg_get_expr(i.indpred,i.indrelid,false),''),
       COALESCE(i.indisunique,false),COALESCE(i.indisprimary,false),COALESCE(i.indisexclusion,false),
       COALESCE(i.indisclustered,false),COALESCE(i.indisreplident,false),
       COALESCE(i.indisvalid,false),COALESCE(i.indisready,false),COALESCE(i.indislive,false),
       idx.reltablespace,COALESCE(array_to_string(idx.reloptions,','),'')
FROM pg_catalog.pg_class idx
JOIN pg_catalog.pg_namespace idx_ns ON idx_ns.oid=idx.relnamespace
LEFT JOIN pg_catalog.pg_index i ON i.indexrelid=idx.oid
LEFT JOIN pg_catalog.pg_am am ON am.oid=idx.relam
LEFT JOIN pg_catalog.pg_class tbl ON tbl.oid=i.indrelid
LEFT JOIN pg_catalog.pg_namespace tbl_ns ON tbl_ns.oid=tbl.relnamespace
WHERE idx_ns.nspname='public' AND idx.relname='${INDEX_NAME}'" >"$output"
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

index_contract_state() {
  local named_count
  named_count=$(psql_readonly -At -c "SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname='${INDEX_NAME}'")
  if [[ "$named_count" == '0' ]]; then
    printf '%s\n' 'absent'
    return
  fi
  [[ "$named_count" == '1' ]] || { printf '%s\n' 'mismatch'; return; }
  psql_readonly -At -c "$INDEX_ASSERTION_SQL"
}

finalize_record() {
  local status=$?
  trap - EXIT ERR
  set +e
  local finish_utc duration_seconds compose_hash env_hash migration_hash
  finish_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  duration_seconds=$((SECONDS-START_SECONDS))
  compose_hash=$(sha256sum "$COMPOSE_FILE" | cut -d' ' -f1)
  env_hash=$(sha256sum "$PRODUCTION_ENV_FILE" | cut -d' ' -f1)
  migration_hash='unavailable'
  [[ -s "$RECORD_DIR/schema-migrations-after.tsv" ]] && migration_hash=$(sha256sum "$RECORD_DIR/schema-migrations-after.tsv" | cut -d' ' -f1)
  cat >"$RECORD_DIR/result.env" <<RESULT
status=$OPERATION_STATUS
operation=rc39_source_readiness_index_concurrent_prebuild
started_utc=$START_UTC
finished_utc=$finish_utc
duration_seconds=$duration_seconds
failure_line=$FAILURE_LINE
index_action=$INDEX_ACTION
operator_sha256=$ACTUAL_OPERATOR_SHA256
verifier_sha256=$ACTUAL_VERIFIER_SHA256
compose_sha256=$compose_hash
production_env_sha256=$env_hash
schema_migrations_sha256=$migration_hash
schema_migrations_written_by_operator=false
sub2api_newapi_touched=false
RESULT
  (
    cd "$RECORD_DIR" || exit 1
    find . -type f ! -name 'RC39-SOURCE-READINESS-INDEX.sha256' -printf '%P\0' |
      sort -z | xargs -0 sha256sum -- >RC39-SOURCE-READINESS-INDEX.sha256
  )
  chmod -R go-rwx -- "$RECORD_DIR"
  sync -f "$RECORD_ROOT" 2>/dev/null || sync
  if (( status != 0 )); then
    printf 'operator failed closed; inspect and sign the failure evidence if retained: %s\n' "$RECORD_DIR" >&2
  fi
  exit "$status"
}

record_error_line() {
  FAILURE_LINE=$1
}
trap 'record_error_line "$LINENO"' ERR
trap finalize_record EXIT

printf 'operator_sha256=%s\nverifier_sha256=%s\ncreate_index_sql_sha256=%s\n' \
  "$ACTUAL_OPERATOR_SHA256" "$ACTUAL_VERIFIER_SHA256" \
  "$(printf '%s' "$CREATE_INDEX_SQL" | sha256sum | cut -d' ' -f1)" >"$RECORD_DIR/operator-inputs.env"

capture_api_state "$RECORD_DIR/api-before.env"
capture_schema_migrations "$RECORD_DIR/schema-migrations-before.tsv"
readonly MIGRATION_0013_BEFORE="$(psql_readonly -At -c "SELECT count(*) FROM public.schema_migrations WHERE name='0013_source_readiness_active_index.sql'")"
[[ "$MIGRATION_0013_BEFORE" == '0' ]] || die 'migration 0013 is already recorded; concurrent prebuild must run before the transactional migration'

readonly SERVER_STATE="$(psql_readonly -At -F '|' -c "SELECT current_database(),current_user,current_setting('server_version_num')::integer/10000,current_setting('default_transaction_read_only')")"
[[ "$SERVER_STATE" == 'invoice|invoice_owner|18|on' ]] || die "unexpected PostgreSQL operator preflight session: $SERVER_STATE"
printf '%s\n' "$SERVER_STATE" >"$RECORD_DIR/postgres-session.txt"

readonly TABLE_STATE="$(psql_readonly -At -c "$TABLE_ASSERTION_SQL")"
[[ "$TABLE_STATE" == 'exact' ]] || die 'public.source_ingest_events table contract mismatch'
printf '%s\n' "$TABLE_STATE" >"$RECORD_DIR/table-contract.txt"

capture_index_catalog "$RECORD_DIR/index-before.tsv"
readonly INDEX_STATE_BEFORE="$(index_contract_state)"
printf '%s\n' "$INDEX_STATE_BEFORE" >"$RECORD_DIR/index-state-before.txt"
case "$INDEX_STATE_BEFORE" in
  absent)
    INDEX_ACTION='create-concurrently'
    readonly INDEX_BUILD_START_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    readonly INDEX_BUILD_START_SECONDS=$SECONDS
    {
      printf '%s\n' "SET statement_timeout='2h';"
      printf '%s\n' "SET lock_timeout='5s';"
      printf '%s;\n' "$CREATE_INDEX_SQL"
    } | timeout 7500s "${COMPOSE[@]}" exec -T postgres \
        psql -X -q -v ON_ERROR_STOP=1 -U "$DATABASE_USER" -d "$DATABASE_NAME" \
        >"$RECORD_DIR/create-index.stdout" 2>"$RECORD_DIR/create-index.stderr"
    readonly INDEX_BUILD_FINISH_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    readonly INDEX_BUILD_DURATION_SECONDS=$((SECONDS-INDEX_BUILD_START_SECONDS))
    printf 'started_utc=%s\nfinished_utc=%s\nduration_seconds=%s\n' \
      "$INDEX_BUILD_START_UTC" "$INDEX_BUILD_FINISH_UTC" "$INDEX_BUILD_DURATION_SECONDS" \
      >"$RECORD_DIR/create-index-timing.env"
    ;;
  exact)
    INDEX_ACTION='already-exact'
    ;;
  *)
    die 'same-name index exists but is wrong, invalid, not ready, or not live; operator will not drop or repair it automatically'
    ;;
esac

capture_index_catalog "$RECORD_DIR/index-after-create.tsv"
readonly INDEX_STATE_AFTER="$(index_contract_state)"
[[ "$INDEX_STATE_AFTER" == 'exact' ]] || die 'concurrent index build did not produce the exact valid/ready/live catalog contract'
printf '%s\n' "$INDEX_STATE_AFTER" >"$RECORD_DIR/index-state-after.txt"

readonly ANALYZE_START_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
readonly ANALYZE_START_SECONDS=$SECONDS
timeout 1800s "${COMPOSE[@]}" exec -T postgres \
  psql -X -q -v ON_ERROR_STOP=1 -U "$DATABASE_USER" -d "$DATABASE_NAME" \
  -c "SET statement_timeout='20m'; SET lock_timeout='5s'; ANALYZE public.source_ingest_events" \
  >"$RECORD_DIR/analyze.stdout" 2>"$RECORD_DIR/analyze.stderr"
readonly ANALYZE_FINISH_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
readonly ANALYZE_DURATION_SECONDS=$((SECONDS-ANALYZE_START_SECONDS))
printf 'started_utc=%s\nfinished_utc=%s\nduration_seconds=%s\n' \
  "$ANALYZE_START_UTC" "$ANALYZE_FINISH_UTC" "$ANALYZE_DURATION_SECONDS" \
  >"$RECORD_DIR/analyze-timing.env"

EXPECTED_API_IMAGE_ID="$EXPECTED_API_IMAGE_ID" \
EXPECTED_VERIFIER_SHA256="$EXPECTED_VERIFIER_SHA256" \
PRODUCTION_ENV_FILE="$PRODUCTION_ENV_FILE" \
VERIFICATION_RECORD_DIR="$RECORD_DIR/post-create-readonly-verification" \
  "$VERIFIER_PATH"

capture_schema_migrations "$RECORD_DIR/schema-migrations-after.tsv"
cmp -s "$RECORD_DIR/schema-migrations-before.tsv" "$RECORD_DIR/schema-migrations-after.tsv" ||
  die 'schema_migrations changed during the concurrent prebuild operation'
readonly MIGRATION_0013_AFTER="$(psql_readonly -At -c "SELECT count(*) FROM public.schema_migrations WHERE name='0013_source_readiness_active_index.sql'")"
[[ "$MIGRATION_0013_AFTER" == '0' ]] || die 'operator must not record migration 0013'

capture_api_state "$RECORD_DIR/api-after.env"
assert_same_api_identity "$RECORD_DIR/api-before.env" "$RECORD_DIR/api-after.env"

OPERATION_STATUS='passed'
FAILURE_LINE='none'
printf 'RC39 concurrent readiness index prebuild passed; signing input will be: %s\n' \
  "$RECORD_DIR/RC39-SOURCE-READINESS-INDEX.sha256"
