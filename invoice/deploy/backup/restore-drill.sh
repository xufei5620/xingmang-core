#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

: "${DATABASE_BACKUP:?set DATABASE_BACKUP to an encrypted .postgres.dump.age file}"
: "${DOCUMENT_BACKUP:?set DOCUMENT_BACKUP to an encrypted .documents.tar.age file}"
: "${SOURCE_STATE_BACKUP:?set SOURCE_STATE_BACKUP to an encrypted .source-state.tar.age file}"
: "${METADATA_BACKUP:?set METADATA_BACKUP to an encrypted .metadata.tar.age file}"
: "${BACKUP_MANIFEST:?set BACKUP_MANIFEST to the matching .sha256 file}"
: "${BACKUP_SIGNATURE:?set BACKUP_SIGNATURE to the matching OpenSSH .sha256.sig file}"
: "${BACKUP_ALLOWED_SIGNERS_FILE:?set the offline-reviewed OpenSSH allowed_signers file}"
: "${AGE_IDENTITY_FILE:?set AGE_IDENTITY_FILE to the offline restore identity}"
: "${FIELD_KEYRING_FILE:?set FIELD_KEYRING_FILE to the offline field keyring copy}"
: "${SOURCE_SPOOL_KEY_ROOT:?set SOURCE_SPOOL_KEY_ROOT to the offline directory containing all source state encryption keys}"
: "${SUB2API_SOURCE_ID:?set restored Sub2API source UUID}"
: "${NEWAPI_SOURCE_ID:?set restored New API source UUID}"
: "${SUB2API_RUNTIME_VERSION:?set the exact restored Sub2API runtime}"
: "${NEWAPI_RUNTIME_VERSION:?set the exact restored New API runtime}"
: "${SUB2API_BALANCES_SIGNING_KEY_ID:?set the cutover manifest signing key id}"
: "${NEWAPI_BALANCES_SIGNING_KEY_ID:?set the cutover manifest signing key id}"
: "${INVOICE_IMAGE_TAG:?set the exact reviewed invoice release tag}"

eligibility_start_at='2026-09-01T00:00:00+08:00'
eligibility_start_utc='2026-08-31T16:00:00Z'
restore_schema_mode=${RESTORE_SCHEMA_MODE:-post-0011}
restore_postgres_tmpfs_size=${RESTORE_POSTGRES_TMPFS_SIZE-16g}
restore_balance_history_rehearsal=${RESTORE_BALANCE_HISTORY_CLEANUP_REHEARSAL-NO}
[[ "$restore_schema_mode" == 'pre-0011' || "$restore_schema_mode" == 'post-0011' ]] || {
  echo 'RESTORE_SCHEMA_MODE must be pre-0011 or post-0011' >&2
  exit 2
}
[[ "$restore_balance_history_rehearsal" == NO || "$restore_balance_history_rehearsal" == YES ]] || {
  echo 'RESTORE_BALANCE_HISTORY_CLEANUP_REHEARSAL must be YES or NO' >&2
  exit 2
}
if [[ "$restore_balance_history_rehearsal" == YES && "$restore_postgres_tmpfs_size" != 16g ]]; then
  echo 'balance history cleanup rehearsal requires RESTORE_POSTGRES_TMPFS_SIZE=16g' >&2
  exit 2
fi
if [[ "$restore_balance_history_rehearsal" == YES ]]; then
  : "${BALANCE_HISTORY_REHEARSAL_RECORD_ROOT:?set fixed root-only rehearsal evidence root}"
  [[ -d "$BALANCE_HISTORY_REHEARSAL_RECORD_ROOT" && ! -L "$BALANCE_HISTORY_REHEARSAL_RECORD_ROOT" &&
     "$(stat -c '%u:%g:%a' "$BALANCE_HISTORY_REHEARSAL_RECORD_ROOT")" == 0:0:700 ]] || {
    echo 'BALANCE_HISTORY_REHEARSAL_RECORD_ROOT must be root:root mode 0700' >&2
    exit 2
  }
fi

for command in age awk docker sha256sum tar find cmp grep ssh-keygen stat; do command -v "$command" >/dev/null; done
restore_postgres_image="invoice-postgres:$INVOICE_IMAGE_TAG"
docker image inspect "$restore_postgres_image" >/dev/null
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
capacity_validator="$script_dir/validate-restore-postgres-capacity.sh"
test -f "$capacity_validator" && test ! -L "$capacity_validator" && test -s "$capacity_validator" || { echo "required path must be a nonempty regular file without symlinks" >&2; exit 1; }
cleanup_state_helper="$script_dir/docker-cleanup-state.sh"
test -f "$cleanup_state_helper" && test ! -L "$cleanup_state_helper" && test -s "$cleanup_state_helper" || { echo "required path must be a nonempty regular file without symlinks" >&2; exit 1; }
# shellcheck source=deploy/backup/docker-cleanup-state.sh
source "$cleanup_state_helper"
balance_history_rehearsal="$script_dir/../postgres/rehearse-balance-history-cleanup.sh"
test -f "$balance_history_rehearsal" && test ! -L "$balance_history_rehearsal" && test -s "$balance_history_rehearsal" || { echo "required path must be a nonempty regular file without symlinks" >&2; exit 1; }
if [[ "$restore_balance_history_rehearsal" == YES ]]; then
  [[ "$(stat -c '%u:%g:%a' "$balance_history_rehearsal")" == 0:0:700 ]] || {
    echo 'installed balance history rehearsal must be root:root mode 0700' >&2
    exit 2
  }
  [[ "$(stat -c '%u:%g:%a' "$script_dir/../postgres/balance-history-cleanup.sql")" == 0:0:600 ]] || {
    echo 'installed balance history cleanup SQL must be root:root mode 0600' >&2
    exit 2
  }
fi
host_available_bytes=$(awk '/^MemAvailable:/ { printf "%.0f\n",$2*1024; found=1 } END { if (!found) exit 1 }' /proc/meminfo)
docker_total_bytes=$(docker info --format '{{.MemTotal}}')
restore_postgres_tmpfs_bytes=$(bash "$capacity_validator" "$restore_postgres_tmpfs_size" \
  "$host_available_bytes" "$docker_total_bytes")
[[ "$restore_postgres_tmpfs_bytes" =~ ^[1-9][0-9]*$ ]]
for file in "$DATABASE_BACKUP" "$DOCUMENT_BACKUP" "$SOURCE_STATE_BACKUP" "$METADATA_BACKUP" "$BACKUP_MANIFEST" "$BACKUP_SIGNATURE" "$BACKUP_ALLOWED_SIGNERS_FILE" "$AGE_IDENTITY_FILE" "$FIELD_KEYRING_FILE"; do
  test -f "$file" && test ! -L "$file" && test -s "$file" || { echo "required path must be a nonempty regular file without symlinks" >&2; exit 1; }
done
(( $(stat -c '%s' "$BACKUP_MANIFEST") <= 65536 ))
(( $(stat -c '%s' "$BACKUP_SIGNATURE") <= 16384 ))
(( $(stat -c '%s' "$BACKUP_ALLOWED_SIGNERS_FILE") <= 65536 ))
if [[ -n "${KEYCLOAK_BACKUP:-}" ]]; then
  test -f "$KEYCLOAK_BACKUP" && test ! -L "$KEYCLOAK_BACKUP" && test -s "$KEYCLOAK_BACKUP" || { echo "required path must be a nonempty regular file without symlinks" >&2; exit 1; }
fi
for key in sub2api_payments_spool_key sub2api_identities_spool_key sub2api_usage_spool_key sub2api_credits_spool_key sub2api_balances_spool_key newapi_payments_spool_key newapi_identities_spool_key newapi_usage_spool_key newapi_credits_spool_key newapi_balances_spool_key sub2api_cutover_key newapi_cutover_key sub2api_balance_snapshot_key newapi_balance_snapshot_key; do
  test -s "$SOURCE_SPOOL_KEY_ROOT/$key"
done

backup_signature_namespace=solov-invoice-backup-v1
backup_signer_identity=invoice-backup
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
# Authenticity is checked before checksums and, critically, before age sees any
# attacker-controlled ciphertext.
ssh-keygen -Y verify -f "$BACKUP_ALLOWED_SIGNERS_FILE" -I "$backup_signer_identity" \
  -n "$backup_signature_namespace" -s "$BACKUP_SIGNATURE" <"$BACKUP_MANIFEST"

manifest_dir=$(cd "$(dirname "$BACKUP_MANIFEST")" && pwd)
declare -A expected_manifest_files=()
restore_components=("$DATABASE_BACKUP" "$DOCUMENT_BACKUP" "$SOURCE_STATE_BACKUP" "$METADATA_BACKUP")
if [[ -n "${KEYCLOAK_BACKUP:-}" ]]; then restore_components+=("$KEYCLOAK_BACKUP"); fi
for file in "${restore_components[@]}"; do
  file_dir=$(cd "$(dirname "$file")" && pwd)
  [[ "$file_dir" == "$manifest_dir" ]] || {
    echo "every signed backup component must be in the manifest directory" >&2
    exit 1
  }
  filename=$(basename "$file")
  [[ "$filename" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$ ]] || {
    echo "unsafe backup component filename: $filename" >&2
    exit 1
  }
  expected_manifest_files[$filename]=1
done
declare -A observed_manifest_files=()
manifest_entries=0
while IFS= read -r line || [[ -n "$line" ]]; do
  [[ "$line" =~ ^[0-9a-f]{64}\ \ ([A-Za-z0-9][A-Za-z0-9._-]{0,254})$ ]] || {
    echo 'signed manifest has invalid syntax' >&2
    exit 1
  }
  filename=${BASH_REMATCH[1]}
  [[ -n "${expected_manifest_files[$filename]:-}" && -z "${observed_manifest_files[$filename]:-}" ]] || {
    echo "signed manifest contains an unexpected or duplicate component: $filename" >&2
    exit 1
  }
  observed_manifest_files[$filename]=1
  manifest_entries=$((manifest_entries+1))
done <"$BACKUP_MANIFEST"
[[ "$manifest_entries" -eq "${#expected_manifest_files[@]}" ]] || {
  echo 'signed manifest does not contain the exact restore component set' >&2
  exit 1
}
(
  cd "$manifest_dir"
  sha256sum -c "$(basename "$BACKUP_MANIFEST")"
)

suffix=$(date -u +%s)-$$
container="invoice-restore-drill-$suffix"
network="invoice-restore-drill-$suffix"
temporary=$(mktemp -d)
started=false
network_created=false
cleanup() {
  local original_status=$?
  local cleanup_failed=false
  set +e
  if $started; then
    remove_docker_resource_strict container "$container" || cleanup_failed=true
  fi
  if $network_created; then
    remove_docker_resource_strict network "$network" || cleanup_failed=true
  fi
  rm -rf -- "$temporary" || cleanup_failed=true
  [[ -e "$temporary" ]] && cleanup_failed=true
  if $cleanup_failed; then
    echo 'CRITICAL: restore drill cleanup left a container, network, or temporary plaintext behind' >&2
    exit 1
  fi
  exit "$original_status"
}
trap cleanup EXIT

age --decrypt -i "$AGE_IDENTITY_FILE" -o "$temporary/documents.tar" "$DOCUMENT_BACKUP"
age --decrypt -i "$AGE_IDENTITY_FILE" -o "$temporary/source-state.tar" "$SOURCE_STATE_BACKUP"
age --decrypt -i "$AGE_IDENTITY_FILE" -o "$temporary/metadata.tar" "$METADATA_BACKUP"
if [[ -n "${INVOICE_TOOLS_IMAGE:-}" ]]; then
  invoice_tools_image=$INVOICE_TOOLS_IMAGE
else
  : "${INVOICE_IMAGE_TAG:?set the exact reviewed invoice release tag or INVOICE_TOOLS_IMAGE}"
  invoice_tools_image="invoice-system-tools:$INVOICE_IMAGE_TAG"
fi
database_verify_image=$invoice_tools_image
if [[ "$restore_schema_mode" == 'pre-0011' ]]; then
  : "${PRE_0011_TOOLS_IMAGE:?set the exact RC17 tools image for pre-0011 migration verification}"
  database_verify_image=$PRE_0011_TOOLS_IMAGE
fi
validate_tar() {
  local archive="$1"
  local max_total="$2"
  local max_file="$3"
  docker run --pull never --rm --read-only --network none --user "$(id -u):$(id -g)" \
    --cap-drop ALL --security-opt no-new-privileges:true \
    --pids-limit 16 --memory 128m --cpus 0.25 \
    --mount "type=bind,src=$archive,dst=/archive.tar,readonly" \
    --entrypoint /usr/local/bin/invoice-archive-verify "$invoice_tools_image" \
    --archive /archive.tar --max-entries 100000 \
    --max-total-bytes "$max_total" --max-file-bytes "$max_file"
}
validate_tar "$temporary/documents.tar" "${RESTORE_MAX_DOCUMENT_BYTES:-107374182400}" 33554432
validate_tar "$temporary/source-state.tar" 4294967296 1073741824
validate_tar "$temporary/metadata.tar" 134217728 67108864

mkdir -p "$temporary/documents" "$temporary/source-state" "$temporary/metadata"
tar --no-same-owner --no-same-permissions --no-xattrs --no-acls --no-selinux -xf "$temporary/documents.tar" -C "$temporary/documents"
tar --no-same-owner --no-same-permissions --no-xattrs --no-acls --no-selinux -xf "$temporary/source-state.tar" -C "$temporary/source-state"
tar --no-same-owner --no-same-permissions --no-xattrs --no-acls --no-selinux -xf "$temporary/metadata.tar" -C "$temporary/metadata"
if find "$temporary/documents" "$temporary/source-state" "$temporary/metadata" -type l -print -quit | grep -q .; then
  echo 'restored archive contains a symlink' >&2
  exit 1
fi
(
  cd "$temporary/metadata"
  sha256sum -c metadata-files.sha256
)
test -f "$temporary/metadata/backup-schema-mode.txt"
metadata_schema_mode=$(<"$temporary/metadata/backup-schema-mode.txt")
[[ "$metadata_schema_mode" == "$restore_schema_mode" ]] || {
  echo 'requested restore schema mode does not match signed backup metadata' >&2
  exit 1
}

source_directories=(sub2api-payments sub2api-identities sub2api-usage sub2api-credits sub2api-balances newapi-payments newapi-identities newapi-usage newapi-credits newapi-balances)
source_agent_image=${SOURCE_AGENT_IMAGE:-invoice-source-agent:${INVOICE_IMAGE_TAG:?set the exact reviewed invoice release tag or SOURCE_AGENT_IMAGE}}
for directory in "${source_directories[@]}"; do
  state_dir="$temporary/source-state/$directory"
  test -d "$state_dir"
  test -s "$state_dir/state.json"
  expected_source="$SUB2API_SOURCE_ID"
  [[ "$directory" == newapi-* ]] && expected_source="$NEWAPI_SOURCE_ID"
  expected_stream=${directory#*-}
  grep -Fq "\"source_id\":\"$expected_source\"" "$state_dir/state.json"
  grep -Fq "\"stream_id\":\"$expected_stream\"" "$state_dir/state.json"
  if [[ -e "$state_dir/pending.enc" ]]; then
    test -s "$state_dir/pending.enc"
    grep -Fq '"algorithm":"AES-256-GCM"' "$state_dir/pending.enc"
  fi
  source_type=sub2api
  [[ "$directory" == newapi-* ]] && source_type=newapi
  key_name=${source_type}_${expected_stream}_spool_key
  key_copy="$temporary/$directory-spool-key"
  install -m 0400 "$SOURCE_SPOOL_KEY_ROOT/$key_name" "$key_copy"
  chown -R 65532:65532 "$state_dir" "$key_copy"
  find "$state_dir" -type d -exec chmod 0700 {} +
  find "$state_dir" -type f -exec chmod 0600 {} +
  docker_args=(docker run --pull never --rm --read-only --network none --user 65532:65532
    --cap-drop ALL --security-opt no-new-privileges:true \
    --mount "type=bind,src=$state_dir,dst=/state,readonly" \
    --mount "type=bind,src=$key_copy,dst=/run/secrets/spool-key,readonly" \
    --env "SOURCE_ID=$expected_source" --env "SOURCE_TYPE=$source_type" \
    --env "SOURCE_STATE_STREAM=$expected_stream" \
    --env SOURCE_STATE_FILE=/state/state.json \
    --env SOURCE_SPOOL_FILE=/state/pending.enc \
    --env SOURCE_SPOOL_KEY_FILE=/run/secrets/spool-key)
  if [[ "$expected_stream" == identities ]]; then
    docker_args+=(--env SOURCE_SCHEMA_VERSION=2.0 --env SOURCE_RECONCILE_FILE=/state/reconcile.json --env SOURCE_RECONCILE_MISS_THRESHOLD=3)
  else
    runtime_version=$SUB2API_RUNTIME_VERSION
    balances_signing_key_id=$SUB2API_BALANCES_SIGNING_KEY_ID
    [[ "$source_type" == newapi ]] && runtime_version=$NEWAPI_RUNTIME_VERSION && balances_signing_key_id=$NEWAPI_BALANCES_SIGNING_KEY_ID
    if [[ "$source_type" == newapi ]]; then
      cutover_runtime_version=${NEWAPI_CUTOVER_RUNTIME_VERSION:-$runtime_version}
    else
      cutover_runtime_version=${SUB2API_CUTOVER_RUNTIME_VERSION:-$runtime_version}
    fi
    cutover_key_copy="$temporary/$source_type-cutover-key"
    install -m 0400 "$SOURCE_SPOOL_KEY_ROOT/${source_type}_cutover_key" "$cutover_key_copy"
    chown 65532:65532 "$cutover_key_copy"
    cutover_dir="$temporary/source-state/cutover/$source_type"
    test -s "$cutover_dir/manifest.enc" && test -s "$cutover_dir/baseline.enc"
    chown -R 65532:65532 "$cutover_dir"
    find "$cutover_dir" -type d -exec chmod 0700 {} +
    find "$cutover_dir" -type f -exec chmod 0600 {} +
    docker_args+=(--env SOURCE_SCHEMA_VERSION=3.0 --env "SOURCE_RUNTIME_VERSION=$runtime_version"
      --env "SOURCE_CUTOVER_RUNTIME_VERSION=$cutover_runtime_version"
      --env "ELIGIBILITY_START_AT=$eligibility_start_at"
      --env "SOURCE_SIGNING_KEY_ID=$balances_signing_key_id"
      --env SOURCE_CUTOVER_MANIFEST_FILE=/cutover/manifest.enc
      --env SOURCE_CUTOVER_KEY_FILE=/run/secrets/cutover-key
      --mount "type=bind,src=$cutover_dir,dst=/cutover,readonly"
      --mount "type=bind,src=$cutover_key_copy,dst=/run/secrets/cutover-key,readonly")
    if [[ "$expected_stream" == balances ]]; then
      snapshot_key_copy="$temporary/$source_type-balance-snapshot-key"
      install -m 0400 "$SOURCE_SPOOL_KEY_ROOT/${source_type}_balance_snapshot_key" "$snapshot_key_copy"
      chown 65532:65532 "$snapshot_key_copy"
      docker_args+=(--env SOURCE_BALANCE_BASELINE_FILE=/cutover/baseline.enc
        --env SOURCE_BALANCE_SNAPSHOT_FILE=/state/balance-current.enc
        --env SOURCE_BALANCE_SNAPSHOT_KEY_FILE=/run/secrets/balance-snapshot-key
        --mount "type=bind,src=$snapshot_key_copy,dst=/run/secrets/balance-snapshot-key,readonly")
    fi
  fi
  docker_args+=(--entrypoint /source-agent-prod "$source_agent_image" check-state)
  "${docker_args[@]}"
done

if docker network inspect "$network" >/dev/null 2>&1 ||
   docker container inspect "$container" >/dev/null 2>&1; then
  echo 'restore drill Docker resource name collision' >&2
  exit 1
fi
network_created=true
docker network create "$network" >/dev/null
started=true
docker run --pull never --detach --rm --name "$container" --network "$network" --network-alias postgres \
  --env POSTGRES_PASSWORD=restore-drill-only \
  --env POSTGRES_DB=invoice \
  --tmpfs "/var/lib/postgresql:rw,nosuid,nodev,size=$restore_postgres_tmpfs_size" \
  "$restore_postgres_image" >/dev/null

deadline=$((SECONDS+60))
until docker exec "$container" pg_isready -U postgres -d invoice >/dev/null 2>&1; do
  (( SECONDS < deadline )) || { echo 'restore PostgreSQL did not become ready' >&2; exit 1; }
  sleep 1
done

# The official image briefly runs a bootstrap postmaster, stops it, then starts
# the final server. A single pg_isready can hit that transient instance and
# race pg_restore into the restart window. Require the entrypoint's init-complete
# marker plus three consecutive final-server readiness checks.
until docker logs "$container" 2>&1 | grep -F 'PostgreSQL init process complete; ready for start up.' >/dev/null; do
  (( SECONDS < deadline )) || { echo 'restore PostgreSQL initialization did not complete' >&2; exit 1; }
  sleep 1
done
for _ in 1 2 3; do
  docker exec "$container" pg_isready -U postgres -d invoice >/dev/null 2>&1 || {
    echo 'restore PostgreSQL final server did not remain ready' >&2
    exit 1
  }
  sleep 1
done

age --decrypt -i "$AGE_IDENTITY_FILE" "$DATABASE_BACKUP" \
  | docker exec -i "$container" pg_restore -U postgres -d invoice --no-owner --no-acl --exit-on-error

table_count=$(docker exec "$container" psql -U postgres -d invoice -Atc \
  "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'")
test "$table_count" -ge 20

docker exec "$container" psql -X -v ON_ERROR_STOP=1 -U postgres -d invoice \
  -c "COPY (SELECT name,checksum,applied_at FROM schema_migrations ORDER BY name) TO STDOUT WITH CSV HEADER" \
  >"$temporary/restored-schema-migrations.csv"
docker exec "$container" psql -X -v ON_ERROR_STOP=1 -U postgres -d invoice \
  -c "COPY (SELECT id,invoice_request_id,object_key,object_version,sha256,size_bytes,mime_type FROM invoice_documents ORDER BY id) TO STDOUT WITH CSV HEADER" \
  >"$temporary/restored-invoice-documents.csv"
docker exec "$container" psql -X -v ON_ERROR_STOP=1 -U postgres -d invoice \
  -c "COPY (SELECT si.id,si.source_type,si.runtime_version,sis.stream_id,sis.sequence,COALESCE(sis.last_batch_hash,'') FROM source_instances si LEFT JOIN source_ingest_state sis ON sis.source_instance_id=si.id ORDER BY si.id,sis.stream_id) TO STDOUT WITH CSV HEADER" \
  >"$temporary/restored-source-receiver-state.csv"
cmp "$temporary/metadata/schema-migrations.csv" "$temporary/restored-schema-migrations.csv"
cmp "$temporary/metadata/invoice-documents.csv" "$temporary/restored-invoice-documents.csv"
cmp "$temporary/metadata/source-receiver-state.csv" "$temporary/restored-source-receiver-state.csv"
if [[ "$restore_schema_mode" == 'post-0011' ]]; then
  docker exec "$container" psql -X -v ON_ERROR_STOP=1 -U postgres -d invoice \
    -c "COPY (SELECT singleton_id,to_char(eligibility_start_at AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"') AS eligibility_start_utc,display_timezone,require_payment_at_or_after,require_usage_at_or_after,policy_version FROM invoice_eligibility_policy ORDER BY singleton_id) TO STDOUT WITH CSV HEADER" \
    >"$temporary/restored-invoice-eligibility-policy.csv"
  cmp "$temporary/metadata/invoice-eligibility-policy.csv" "$temporary/restored-invoice-eligibility-policy.csv"
  restored_policy=$(docker exec "$container" psql -X -v ON_ERROR_STOP=1 -U postgres -d invoice -At -F '|' -c \
    "SELECT to_char(eligibility_start_at AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'),display_timezone,require_payment_at_or_after,require_usage_at_or_after,policy_version FROM invoice_eligibility_policy WHERE singleton_id=1")
  [[ "$restored_policy" == "$eligibility_start_utc|Asia/Shanghai|t|t|1" ]] || {
    echo 'restored immutable invoice eligibility policy mismatch' >&2
    exit 1
  }
else
  [[ "$(<"$temporary/metadata/invoice-eligibility-policy.csv")" == $'schema_mode\npre-0011' ]] || {
    echo 'signed pre-0011 eligibility policy marker is invalid' >&2
    exit 1
  }
  restored_pre_policy=$(docker exec "$container" psql -X -v ON_ERROR_STOP=1 -U postgres -d invoice -At -F '|' -c \
    "SELECT to_regclass('invoice_eligibility_policy') IS NULL,(SELECT count(*) FROM schema_migrations WHERE name='0011_invoice_eligibility_policy.sql')")
  [[ "$restored_pre_policy" == 't|0' ]] || {
    echo 'restored database is not the signed pre-0011 schema' >&2
    exit 1
  }
fi

install -m 0400 "$FIELD_KEYRING_FILE" "$temporary/field-keyring.json"
printf '%s\n' 'postgres://postgres:restore-drill-only@postgres:5432/invoice?sslmode=disable' >"$temporary/database-url"
chmod 0400 "$temporary/database-url"
chown -R 10001:10001 "$temporary/documents" "$temporary/field-keyring.json" "$temporary/database-url"
docker run --pull never --rm --read-only --network "$network" --user 10001:10001 \
  --cap-drop ALL --security-opt no-new-privileges:true \
  --mount "type=bind,src=$temporary/documents,dst=/restore/documents,readonly" \
  --mount "type=bind,src=$temporary/field-keyring.json,dst=/run/secrets/field-keyring.json,readonly" \
  --mount "type=bind,src=$temporary/database-url,dst=/run/secrets/database-url,readonly" \
  --entrypoint /usr/local/bin/invoice-backup-verify "$database_verify_image" \
  --database-url-file /run/secrets/database-url \
  --document-root /restore/documents \
  --field-keyring-file /run/secrets/field-keyring.json \
  --migrations-dir /app/migrations \
  --sample "${DOCUMENT_DECRYPT_SAMPLE:-10}"

if [[ -n "${KEYCLOAK_BACKUP:-}" ]]; then
  test -s "$KEYCLOAK_BACKUP"
  docker exec "$container" createdb -U postgres keycloak_restore
  age --decrypt -i "$AGE_IDENTITY_FILE" "$KEYCLOAK_BACKUP" \
    | docker exec -i "$container" pg_restore -U postgres -d keycloak_restore --no-owner --no-acl --exit-on-error
  keycloak_tables=$(docker exec "$container" psql -U postgres -d keycloak_restore -Atc \
    "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'")
  test "$keycloak_tables" -gt 20
fi

if [[ "$restore_balance_history_rehearsal" == YES ]]; then
  rehearsal_evidence="$BALANCE_HISTORY_REHEARSAL_RECORD_ROOT/rehearsal-$suffix"
  mkdir -m 0700 -- "$rehearsal_evidence"
  "$balance_history_rehearsal" "$container" "$rehearsal_evidence"
fi

printf 'restore drill passed: public_tables=%s; source_states=%s; database/document/source metadata matched; encrypted document samples verified\n' "$table_count" "${#source_directories[@]}"
