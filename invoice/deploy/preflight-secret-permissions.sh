#!/usr/bin/env bash
set -Eeuo pipefail

: "${SECRETS_DIR:?set SECRETS_DIR}"
: "${POSTGRES_UID:?set POSTGRES_UID after checking the pinned image with id postgres}"
test -d "$SECRETS_DIR"
parent_mode=$(stat -c '%a' "$SECRETS_DIR")
parent_owner=$(stat -c '%u' "$SECRETS_DIR")
[[ "$parent_owner" == "0" && "$parent_mode" == "700" ]] || {
  echo "SECRETS_DIR must be root-owned mode 0700" >&2
  exit 1
}

check_file() {
  expected_uid="$1"
  expected_mode="$2"
  name="$3"
  path="$SECRETS_DIR/$name"
  test -f "$path" && test ! -L "$path" && test -s "$path" || { echo "required path must be a nonempty regular file without symlinks" >&2; exit 1; }
  actual_uid=$(stat -c '%u' "$path")
  actual_mode=$(stat -c '%a' "$path")
  [[ "$actual_uid" == "$expected_uid" && "$actual_mode" == "$expected_mode" ]] || {
    echo "unsafe secret ownership/mode: $path got $actual_uid:$actual_mode want $expected_uid:$expected_mode" >&2
    exit 1
  }
}

check_group_file() {
  expected_uid="$1"
  expected_gid="$2"
  expected_mode="$3"
  name="$4"
  path="$SECRETS_DIR/$name"
  test -f "$path" && test ! -L "$path" && test -s "$path" || { echo "required path must be a nonempty regular file without symlinks" >&2; exit 1; }
  actual_uid=$(stat -c '%u' "$path")
  actual_gid=$(stat -c '%g' "$path")
  actual_mode=$(stat -c '%a' "$path")
  [[ "$actual_uid" == "$expected_uid" && "$actual_gid" == "$expected_gid" && "$actual_mode" == "$expected_mode" ]] || {
    echo "unsafe shared secret ownership/mode: $path got $actual_uid:$actual_gid:$actual_mode want $expected_uid:$expected_gid:$expected_mode" >&2
    exit 1
  }
}

api_private=(
  invoice_owner_database_url invoice_app_database_url invoice_field_keyring.json
  invoice_session_binding_key invoice_admin_break_glass_cidrs invoice_oidc_client_secret
)
for name in "${api_private[@]}"; do check_file 10001 400 "$name"; done

# The API UID10001 and scanner UID10002 both receive supplementary/primary
# GID10000. qpdf cannot replace this root-owned, read-only mounted capability;
# the shared socket volume contains no credential material.
check_group_file 0 10000 440 invoice_pdf_scanner_capability

postgres_private=(invoice_owner_db_password invoice_app_db_password keycloak_owner_db_password)
for name in "${postgres_private[@]}"; do check_file "$POSTGRES_UID" 400 "$name"; done

source_private=(
  sub2api_payments_reader_database_url sub2api_identities_reader_database_url
  sub2api_usage_reader_database_url sub2api_credits_reader_database_url sub2api_balances_reader_database_url
  newapi_payments_reader_database_url newapi_identities_reader_database_url
  newapi_usage_reader_database_url newapi_credits_reader_database_url newapi_balances_reader_database_url
  sub2api_payments_spool_key sub2api_identities_spool_key
  sub2api_usage_spool_key sub2api_credits_spool_key sub2api_balances_spool_key
  newapi_payments_spool_key newapi_identities_spool_key
  newapi_usage_spool_key newapi_credits_spool_key newapi_balances_spool_key
  sub2api_payments_signing_key.pem sub2api_identities_signing_key.pem
  sub2api_usage_signing_key.pem sub2api_credits_signing_key.pem sub2api_balances_signing_key.pem
  newapi_payments_signing_key.pem newapi_identities_signing_key.pem
  newapi_usage_signing_key.pem newapi_credits_signing_key.pem newapi_balances_signing_key.pem
  sub2api_cutover_key newapi_cutover_key
  sub2api_balance_snapshot_key newapi_balance_snapshot_key
  sub2api-agent_key.pem newapi-agent_key.pem
)
for name in "${source_private[@]}"; do check_file 65532 400 "$name"; done

for name in sub2api-agent_cert.pem newapi-agent_cert.pem source_agent_ca.pem ingest_server_cert.pem; do
  check_file 0 444 "$name"
done
check_file 0 400 ingest_server_key.pem

# Read by postgres UID during one-time init and Keycloak UID1000 at runtime.
# The root-owned 0700 parent blocks host traversal; Compose mounts only this
# file into the two single-purpose containers.
check_file 0 444 keycloak_app_db_password
if [[ -e "$SECRETS_DIR/keycloak_bootstrap_admin_password" ]]; then
  check_file 1000 400 keycloak_bootstrap_admin_password
fi

printf 'host secret ownership/mode preflight passed\n'

if [[ "${CHECK_CONTAINER_READABILITY:-false}" == "true" ]]; then
  : "${PRODUCTION_ENV_FILE:?set PRODUCTION_ENV_FILE for container checks}"
  project_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
  compose=(docker compose --env-file "$PRODUCTION_ENV_FILE" -f "$project_root/deploy/docker-compose.prod.yml")
  "${compose[@]}" run --rm --no-deps --entrypoint /bin/sh api -ec '
    for file in invoice_app_database_url invoice_field_keyring invoice_session_binding_key invoice_admin_break_glass_cidrs invoice_oidc_client_secret; do
      test -r "/run/secrets/$file"
      mode=$(stat -c %a "/run/secrets/$file")
      test "$mode" = 400
    done
    test -r /run/secrets/invoice_pdf_scanner_capability
    test "$(stat -c %a /run/secrets/invoice_pdf_scanner_capability)" = 440'
  "${compose[@]}" run --rm --no-deps --entrypoint /bin/sh pdf-scanner -ec '
    test -r /run/secrets/invoice_pdf_scanner_capability
    test "$(stat -c %a /run/secrets/invoice_pdf_scanner_capability)" = 440
    test ! -e /run/secrets/invoice_app_database_url
    test ! -e /run/secrets/invoice_field_keyring'
  "${compose[@]}" --profile tools run --rm --no-deps --entrypoint /bin/sh migrate -ec '
    test -r /run/secrets/invoice_owner_database_url
    test "$(stat -c %a /run/secrets/invoice_owner_database_url)" = 400'
  "${compose[@]}" --profile tools run --rm --no-deps --entrypoint /bin/sh permissions -ec '
    test "$(id -u)" = 10001
    test -r /run/secrets/invoice_owner_database_url
    test "$(stat -c %a /run/secrets/invoice_owner_database_url)" = 400'
  printf 'API/tools in-container secret readability preflight passed\n'
fi
