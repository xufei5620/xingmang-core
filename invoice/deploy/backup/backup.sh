#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

: "${BACKUP_DIR:?set BACKUP_DIR to a dedicated backup filesystem}"
: "${AGE_RECIPIENT_FILE:?set AGE_RECIPIENT_FILE to a public age recipients file}"
: "${SOURCE_STATE_ROOT:?set SOURCE_STATE_ROOT to the parent of all ten source state directories}"
: "${SOURCE_CUTOVER_ROOT:?set SOURCE_CUTOVER_ROOT to SOURCE_STATE_ROOT/cutover}"
: "${BACKUP_SIGNING_KEY_FILE:?temporarily mount the offline Ed25519 backup signing private key}"
: "${BACKUP_ALLOWED_SIGNERS_FILE:?set the offline-reviewed OpenSSH allowed_signers file}"
: "${INVOICE_IMAGE_TAG:?set the exact reviewed invoice release tag}"
[[ "${BACKUP_QUIESCE_CONFIRMED:-}" == "YES" ]] || {
  echo 'set BACKUP_QUIESCE_CONFIRMED=YES after scheduling the write-freeze window' >&2
  exit 2
}
backup_schema_mode=${BACKUP_SCHEMA_MODE:-post-0011}
[[ "$backup_schema_mode" == 'pre-0011' || "$backup_schema_mode" == 'post-0011' ]] || {
  echo 'BACKUP_SCHEMA_MODE must be pre-0011 or post-0011' >&2
  exit 2
}

for command in age docker sha256sum flock tar find ssh-keygen stat cmp; do command -v "$command" >/dev/null; done
test -s "$AGE_RECIPIENT_FILE"
test -d "$SOURCE_STATE_ROOT"
test -d "$SOURCE_CUTOVER_ROOT"
state_root=$(cd "$SOURCE_STATE_ROOT" && pwd -P)
cutover_root=$(cd "$SOURCE_CUTOVER_ROOT" && pwd -P)
[[ "$cutover_root" == "$state_root/cutover" ]] || { echo 'SOURCE_CUTOVER_ROOT must resolve to SOURCE_STATE_ROOT/cutover for atomic source-state backup' >&2; exit 1; }
test -f "$BACKUP_SIGNING_KEY_FILE" && test ! -L "$BACKUP_SIGNING_KEY_FILE" && test -s "$BACKUP_SIGNING_KEY_FILE" || { echo "required path must be a nonempty regular file without symlinks" >&2; exit 1; }
test -f "$BACKUP_ALLOWED_SIGNERS_FILE" && test ! -L "$BACKUP_ALLOWED_SIGNERS_FILE" && test -s "$BACKUP_ALLOWED_SIGNERS_FILE" || { echo "required path must be a nonempty regular file without symlinks" >&2; exit 1; }
(( $(stat -c '%s' "$BACKUP_SIGNING_KEY_FILE") <= 65536 ))
(( $(stat -c '%s' "$BACKUP_ALLOWED_SIGNERS_FILE") <= 65536 ))
signing_mode=$(stat -c '%a' "$BACKUP_SIGNING_KEY_FILE")
signing_owner=$(stat -c '%u' "$BACKUP_SIGNING_KEY_FILE")
[[ "$signing_owner" == "$(id -u)" && ("$signing_mode" == "400" || "$signing_mode" == "600") ]] || {
  echo 'backup signing private key must be owned by the backup operator and mode 0400/0600' >&2
  exit 1
}
signing_public=$(ssh-keygen -y -f "$BACKUP_SIGNING_KEY_FILE")
[[ "$signing_public" == ssh-ed25519\ * ]] || {
  echo 'backup signing key must be Ed25519' >&2
  exit 1
}
validate_allowed_signers() {
  local count=0
  local line
  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ -z "$line" || "$line" =~ ^[[:space:]]*# ]] && continue
    [[ "$line" =~ ^invoice-backup[[:space:]]+namespaces=\"solov-invoice-backup-v1\"[[:space:]]+ssh-ed25519[[:space:]]+[A-Za-z0-9+/=]+([[:space:]].*)?$ ]] || {
      echo 'allowed_signers must contain only namespace-bound invoice-backup Ed25519 keys' >&2
      return 1
    }
    count=$((count+1))
  done <"$BACKUP_ALLOWED_SIGNERS_FILE"
  (( count > 0 ))
}
validate_allowed_signers
mkdir -p -- "$BACKUP_DIR"
test -d "$BACKUP_DIR"
backup_directory=$(cd "$BACKUP_DIR" && pwd -P)
signing_directory=$(cd "$(dirname "$BACKUP_SIGNING_KEY_FILE")" && pwd -P)
[[ "$signing_directory" != "$backup_directory" && "$signing_directory" != "$backup_directory"/* ]] || {
  echo 'backup signing private key must not be stored under BACKUP_DIR' >&2
  exit 1
}

project_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
invoice_postgres_image="invoice-postgres:$INVOICE_IMAGE_TAG"
invoice_ingest_proxy_image="invoice-ingest-proxy:$INVOICE_IMAGE_TAG"
docker image inspect "$invoice_ingest_proxy_image" "$invoice_postgres_image" >/dev/null
prod_compose_file="$project_root/deploy/docker-compose.prod.yml"
source_compose_file="$project_root/deploy/docker-compose.sources.yml"
compose_env=()
if [[ -n "${PRODUCTION_ENV_FILE:-}" ]]; then
  test -s "$PRODUCTION_ENV_FILE"
  compose_env=(--env-file "$PRODUCTION_ENV_FILE")
fi
prod_compose=(docker compose "${compose_env[@]}" -f "$prod_compose_file")
source_compose=(docker compose "${compose_env[@]}" -f "$source_compose_file")
source_services=(sub2api-payments sub2api-identities sub2api-usage sub2api-credits sub2api-balances newapi-payments newapi-identities newapi-usage newapi-credits newapi-balances)
source_directories=(sub2api-payments sub2api-identities sub2api-usage sub2api-credits sub2api-balances newapi-payments newapi-identities newapi-usage newapi-credits newapi-balances)
source_archive_entries=("${source_directories[@]}" cutover)

capture_migration_state() {
  local output=$1
  "${prod_compose[@]}" exec -T postgres psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice -At -F '|' \
    -c "SELECT name,checksum FROM schema_migrations ORDER BY name" >"$output"
}

timestamp=$(date -u +%Y%m%dT%H%M%SZ)
prefix="$BACKUP_DIR/invoice-$timestamp"
test ! -e "$prefix.postgres.dump.age"
lock_file="$BACKUP_DIR/.invoice-backup.lock"
exec 9>"$lock_file"
flock -n 9 || { echo 'another invoice backup is running' >&2; exit 1; }
work_dir=$(mktemp -d "$BACKUP_DIR/.invoice-backup-work.XXXXXX")
chmod 0700 "$work_dir"

database_tmp="$prefix.postgres.dump.age.part"
documents_tmp="$prefix.documents.tar.age.part"
source_state_tmp="$prefix.source-state.tar.age.part"
metadata_tmp="$prefix.metadata.tar.age.part"
keycloak_tmp="$prefix.keycloak.dump.age.part"
manifest_tmp="$prefix.sha256.part"
signature_tmp="$manifest_tmp.sig"
database_final="${database_tmp%.part}"
documents_final="${documents_tmp%.part}"
source_state_final="${source_state_tmp%.part}"
metadata_final="${metadata_tmp%.part}"
keycloak_final="${keycloak_tmp%.part}"
manifest_final="${manifest_tmp%.part}"
signature_final="$manifest_final.sig"
backup_signature_namespace=solov-invoice-backup-v1
backup_signer_identity=invoice-backup
document_volume=${INVOICE_DOCUMENT_VOLUME:-invoice-system-prod_invoice_document_data}
resume_required=false
backup_published=false
resume_source_services=()
resume_prod_services=()
resume_source_wait_services=()
resume_prod_wait_services=()
resume_source_running_only_services=()
resume_prod_running_only_services=()
service_health_before_file="$work_dir/service-health-before.tsv"
printf 'compose\tservice\thealth_state\n' >"$service_health_before_file"

record_running_service() {
  local compose_scope=$1
  local service=$2
  local container_output
  local -a container_ids=()
  if [[ "$compose_scope" == 'source' ]]; then
    if ! container_output=$("${source_compose[@]}" ps --status running -q "$service"); then
      echo "cannot inspect pre-backup source service state: $service" >&2
      return 1
    fi
  elif [[ "$compose_scope" == 'prod' ]]; then
    if ! container_output=$("${prod_compose[@]}" ps --status running -q "$service"); then
      echo "cannot inspect pre-backup production service state: $service" >&2
      return 1
    fi
  else
    echo "unknown Compose scope while recording service state: $compose_scope" >&2
    return 1
  fi
  if [[ -z "$container_output" ]]; then
    printf '%s\t%s\tnot-running\n' "$compose_scope" "$service" >>"$service_health_before_file"
    return 0
  fi
  mapfile -t container_ids <<<"$container_output"
  (( ${#container_ids[@]} > 0 )) || return 0
  (( ${#container_ids[@]} == 1 )) || {
    echo "expected exactly one running container for $compose_scope service $service" >&2
    return 1
  }

  local health_state
  if ! health_state=$(docker inspect --format '{{if .Config.Healthcheck}}{{if .State.Health}}{{.State.Health.Status}}{{else}}starting{{end}}{{else}}none{{end}}' "${container_ids[0]}"); then
    echo "cannot inspect pre-backup health state for $compose_scope service $service" >&2
    return 1
  fi
  case "$health_state" in
    healthy|none|starting|unhealthy) ;;
    *)
      echo "unsupported pre-backup health state for $compose_scope service $service: $health_state" >&2
      return 1
      ;;
  esac
  printf '%s\t%s\t%s\n' "$compose_scope" "$service" "$health_state" >>"$service_health_before_file"

  if [[ "$compose_scope" == 'source' ]]; then
    resume_source_services+=("$service")
    if [[ "$health_state" == 'healthy' ]]; then
      resume_source_wait_services+=("$service")
    else
      resume_source_running_only_services+=("$service")
    fi
  else
    resume_prod_services+=("$service")
    if [[ "$health_state" == 'healthy' ]]; then
      resume_prod_wait_services+=("$service")
    else
      resume_prod_running_only_services+=("$service")
    fi
  fi
}

verify_services_running() {
  local compose_scope=$1
  shift
  (( $# > 0 )) || return 0
  local deadline=$((SECONDS+60))
  local service
  local all_running
  local container_output
  local -a container_ids=()
  while :; do
    all_running=true
    for service in "$@"; do
      container_ids=()
      if [[ "$compose_scope" == 'source' ]]; then
        if ! container_output=$("${source_compose[@]}" ps --status running -q "$service"); then
          echo "cannot inspect restored source service state: $service" >&2
          return 1
        fi
      elif [[ "$compose_scope" == 'prod' ]]; then
        if ! container_output=$("${prod_compose[@]}" ps --status running -q "$service"); then
          echo "cannot inspect restored production service state: $service" >&2
          return 1
        fi
      else
        echo "unknown Compose scope while checking restored service: $compose_scope" >&2
        return 1
      fi
      if [[ -n "$container_output" ]]; then
        mapfile -t container_ids <<<"$container_output"
      fi
      if (( ${#container_ids[@]} != 1 )); then
        all_running=false
      fi
    done
    $all_running && return 0
    if (( SECONDS >= deadline )); then
      echo "one or more previously running $compose_scope services did not return to running state: $*" >&2
      return 1
    fi
    sleep 1
  done
}

resume_services() {
  if (( ${#resume_source_services[@]} > 0 )); then
    "${source_compose[@]}" up -d --pull never --no-deps "${resume_source_services[@]}" || return 1
  fi
  if (( ${#resume_prod_services[@]} > 0 )); then
    "${prod_compose[@]}" up -d --pull never --no-deps "${resume_prod_services[@]}" || return 1
  fi
  if (( ${#resume_source_wait_services[@]} > 0 )); then
    "${source_compose[@]}" up -d --pull never --no-deps --wait --wait-timeout 180 "${resume_source_wait_services[@]}" || return 1
  fi
  if (( ${#resume_prod_wait_services[@]} > 0 )); then
    "${prod_compose[@]}" up -d --pull never --no-deps --wait --wait-timeout 180 "${resume_prod_wait_services[@]}" || return 1
  fi
  verify_services_running source "${resume_source_running_only_services[@]}" || return 1
  verify_services_running prod "${resume_prod_running_only_services[@]}" || return 1
  return 0
}

cleanup() {
  status=$?
  trap - EXIT
  # Cleanup is a recovery boundary. Never let errexit or a failed removal skip
  # the attempt to return every previously running service to its prior state.
  set +e
  if $resume_required; then
    resume_services
    resume_status=$?
    resume_required=false
    if (( resume_status != 0 )); then
      echo 'CRITICAL: backup failed and one or more quiesced services did not restart' >&2
      status=1
    fi
  fi

  cleanup_failed=false
  rm -f -- "$database_tmp" "$documents_tmp" "$source_state_tmp" "$metadata_tmp" "$keycloak_tmp" "$manifest_tmp" "$signature_tmp" || cleanup_failed=true
  if ! $backup_published; then
    # This timestamp prefix is unique and was checked absent before the
    # freeze. Never leave unsigned ciphertext components that look like a
    # restorable set after signing or pre-publication failure.
    rm -f -- "$database_final" "$documents_final" "$source_state_final" \
      "$metadata_final" "$keycloak_final" "$manifest_final" "$signature_final" || cleanup_failed=true
  fi
  rm -rf -- "$work_dir" || cleanup_failed=true
  if $cleanup_failed; then
    echo 'CRITICAL: backup cleanup left temporary or unpublished files behind' >&2
    status=1
  fi
  exit "$status"
}
trap cleanup EXIT

for directory in "${source_directories[@]}"; do
  state_dir="$SOURCE_STATE_ROOT/$directory"
  test -d "$state_dir"
  test -s "$state_dir/state.json"
  [[ "$directory" == *-identities ]] && test -s "$state_dir/reconcile.json"
  if find "$state_dir" -type l -print -quit | grep -q .; then
    echo "source state directory contains a symlink: $state_dir" >&2
    exit 1
  fi
done
for source in sub2api newapi; do
  test -s "$SOURCE_CUTOVER_ROOT/$source/manifest.enc"
  test -s "$SOURCE_CUTOVER_ROOT/$source/baseline.enc"
  grep -Fq '"algorithm":"AES-256-GCM"' "$SOURCE_CUTOVER_ROOT/$source/manifest.enc"
  grep -Fq '"algorithm":"AES-256-GCM"' "$SOURCE_CUTOVER_ROOT/$source/baseline.enc"
done

# Freeze every writer before taking any component snapshot. The whole source
# directory is archived, including pending spool, inventory, reconciliation and
# lock metadata introduced by future compatible agent versions.
for service in "${source_services[@]}"; do
  record_running_service source "$service"
done
for service in api ingest-proxy; do
  record_running_service prod "$service"
done
resume_required=true
"${prod_compose[@]}" stop -t 30 ingest-proxy api
"${source_compose[@]}" stop -t 30 "${source_services[@]}"
for directory in "${source_directories[@]}"; do
  state_dir="$SOURCE_STATE_ROOT/$directory"
  if find "$state_dir" -maxdepth 1 -type f -name '*.lock' -print -quit | grep -q .; then
    echo "source agent left a stale lock after stop: $state_dir" >&2
    exit 1
  fi
done

schema_mode_state=$("${prod_compose[@]}" exec -T postgres psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice -At -F '|' \
  -c "SELECT to_regclass('invoice_eligibility_policy') IS NULL,(SELECT count(*) FROM schema_migrations WHERE name='0011_invoice_eligibility_policy.sql')")
if [[ "$backup_schema_mode" == 'pre-0011' ]]; then
  [[ "$schema_mode_state" == 't|0' ]] || { echo 'pre-0011 backup mode does not match the invoice database schema' >&2; exit 1; }
else
  [[ "$schema_mode_state" == 'f|1' ]] || { echo 'post-0011 backup mode does not match the invoice database schema' >&2; exit 1; }
fi
migration_state_before="$work_dir/.schema-migrations-before"
migration_state_after="$work_dir/.schema-migrations-after"
capture_migration_state "$migration_state_before"

"${prod_compose[@]}" exec -T postgres \
  pg_dump -U invoice_owner -d invoice --format=custom --no-owner --no-acl \
  | age -R "$AGE_RECIPIENT_FILE" -o "$database_tmp"
test -s "$database_tmp"
mv -- "$database_tmp" "$database_final"

docker run --pull never --rm --read-only --network none \
  --mount "type=volume,src=$document_volume,dst=/data,readonly" \
  "$invoice_ingest_proxy_image" \
  tar -C /data -cf - . \
  | age -R "$AGE_RECIPIENT_FILE" -o "$documents_tmp"
test -s "$documents_tmp"
mv -- "$documents_tmp" "$documents_final"

docker run --pull never --rm --read-only --network none \
  --mount "type=bind,src=$SOURCE_STATE_ROOT,dst=/state,readonly" \
  "$invoice_ingest_proxy_image" \
  tar -C /state -cf - "${source_archive_entries[@]}" \
  | age -R "$AGE_RECIPIENT_FILE" -o "$source_state_tmp"
test -s "$source_state_tmp"
mv -- "$source_state_tmp" "$source_state_final"

"${prod_compose[@]}" exec -T postgres psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice \
  -c "COPY (SELECT name,checksum,applied_at FROM schema_migrations ORDER BY name) TO STDOUT WITH CSV HEADER" \
  >"$work_dir/schema-migrations.csv"
"${prod_compose[@]}" exec -T postgres psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice \
  -c "COPY (SELECT id,invoice_request_id,object_key,object_version,sha256,size_bytes,mime_type FROM invoice_documents ORDER BY id) TO STDOUT WITH CSV HEADER" \
  >"$work_dir/invoice-documents.csv"
"${prod_compose[@]}" exec -T postgres psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice \
  -c "COPY (SELECT si.id,si.source_type,si.runtime_version,sis.stream_id,sis.sequence,COALESCE(sis.last_batch_hash,'') FROM source_instances si LEFT JOIN source_ingest_state sis ON sis.source_instance_id=si.id ORDER BY si.id,sis.stream_id) TO STDOUT WITH CSV HEADER" \
  >"$work_dir/source-receiver-state.csv"
printf '%s\n' "$backup_schema_mode" >"$work_dir/backup-schema-mode.txt"
if [[ "$backup_schema_mode" == 'post-0011' ]]; then
  "${prod_compose[@]}" exec -T postgres psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice \
    -c "COPY (SELECT singleton_id,to_char(eligibility_start_at AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS eligibility_start_utc,display_timezone,require_payment_at_or_after,require_usage_at_or_after,policy_version FROM invoice_eligibility_policy ORDER BY singleton_id) TO STDOUT WITH CSV HEADER" \
    >"$work_dir/invoice-eligibility-policy.csv"
  eligibility_policy=$("${prod_compose[@]}" exec -T postgres psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice -At -F '|' \
    -c "SELECT to_char(eligibility_start_at AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'),display_timezone,require_payment_at_or_after,require_usage_at_or_after,policy_version FROM invoice_eligibility_policy WHERE singleton_id=1")
  [[ "$eligibility_policy" == '2026-08-31T16:00:00Z|Asia/Shanghai|t|t|1' ]] || {
    echo 'immutable invoice eligibility policy mismatch; backup publication refused' >&2
    exit 1
  }
else
  pre_policy_state=$("${prod_compose[@]}" exec -T postgres psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice -At -F '|' \
    -c "SELECT to_regclass('invoice_eligibility_policy') IS NULL,(SELECT count(*) FROM schema_migrations WHERE name='0011_invoice_eligibility_policy.sql')")
  [[ "$pre_policy_state" == 't|0' ]] || {
    echo 'pre-0011 backup mode does not match the invoice database schema' >&2
    exit 1
  }
  printf 'schema_mode\npre-0011\n' >"$work_dir/invoice-eligibility-policy.csv"
fi
capture_migration_state "$migration_state_after"
cmp "$migration_state_before" "$migration_state_after" || {
  echo 'invoice migration state changed during the backup snapshot; publication refused' >&2
  exit 1
}
rm -f -- "$migration_state_before" "$migration_state_after"

cat >"$work_dir/backup-info.txt" <<INFO
snapshot_utc=$timestamp
schema_mode=$backup_schema_mode
write_freeze=api,ingest-proxy,${source_services[*]}
document_volume=$document_volume
source_state_directories=${source_directories[*]}
invoice_image_tag=${INVOICE_IMAGE_TAG:-unknown}
source_agent_image_tag=${INVOICE_IMAGE_TAG:-unknown}
eligibility_start_at=$([[ "$backup_schema_mode" == post-0011 ]] && printf '%s' '2026-09-01T00:00:00+08:00' || printf '%s' 'not-applied')
eligibility_policy_version=$([[ "$backup_schema_mode" == post-0011 ]] && printf '%s' '1' || printf '%s' 'not-applied')
INFO
cp -- "$source_compose_file" "$work_dir/docker-compose.sources.yml"
for metadata_file in "${SOURCE_INSTANCES_CONFIG_FILE:-}" "${SOURCE_TRUST_CONFIG_FILE:-}" "${RELEASE_METADATA_FILE:-}"; do
  if [[ -n "$metadata_file" ]]; then
    test -s "$metadata_file"
    cp -- "$metadata_file" "$work_dir/$(basename "$metadata_file")"
  fi
done
(
  cd "$work_dir"
  sha256sum -- * >metadata-files.sha256
  tar -cf - .
) | age -R "$AGE_RECIPIENT_FILE" -o "$metadata_tmp"
test -s "$metadata_tmp"
mv -- "$metadata_tmp" "$metadata_final"

backup_files=("$database_final" "$documents_final" "$source_state_final" "$metadata_final")
if [[ "${BACKUP_LOCAL_KEYCLOAK:-false}" == "true" ]]; then
  idp_compose="$project_root/deploy/docker-compose.idp.yml"
  idp=(docker compose "${compose_env[@]}" -f "$idp_compose")
  test -n "$("${idp[@]}" ps -q keycloak-postgres)"
  "${idp[@]}" exec -T keycloak-postgres \
    pg_dump -U keycloak_owner -d keycloak --format=custom --no-owner --no-acl \
    | age -R "$AGE_RECIPIENT_FILE" -o "$keycloak_tmp"
  test -s "$keycloak_tmp"
  mv -- "$keycloak_tmp" "$keycloak_final"
  backup_files+=("$keycloak_final")
fi

(
  cd "$BACKUP_DIR"
  basenames=()
  for file in "${backup_files[@]}"; do basenames+=("$(basename "$file")"); done
  sha256sum "${basenames[@]}" >"$(basename "$manifest_tmp")"
)
ssh-keygen -Y sign -f "$BACKUP_SIGNING_KEY_FILE" -n "$backup_signature_namespace" "$manifest_tmp"
test -s "$signature_tmp"
ssh-keygen -Y verify -f "$BACKUP_ALLOWED_SIGNERS_FILE" -I "$backup_signer_identity" \
  -n "$backup_signature_namespace" -s "$signature_tmp" <"$manifest_tmp"
mv -- "$manifest_tmp" "$manifest_final"
mv -- "$signature_tmp" "$signature_final"
backup_published=true
sync -f "$BACKUP_DIR" 2>/dev/null || sync

resume_services
resume_required=false
rm -rf -- "$work_dir"
trap - EXIT
printf 'consistent encrypted backup complete: %s\n' "$prefix"
printf 'signed manifest: %s\n' "$signature_final"
printf 'Back up FIELD_KEYRING_FILE, age private identity, ten spool keys, two cutover keys and two balance-snapshot keys separately/offline; none is copied by this script.\n'
