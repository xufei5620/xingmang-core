#!/usr/bin/env bash
set -Eeuo pipefail

# Create exactly one named, permanent Keycloak master-realm administrator and
# dispatch Keycloak's required-action invitation. This is deliberately a
# create-only operator: an existing target is inspected but never repaired,
# merged, re-invited or granted additional privileges.
#
# Bootstrap password, bearer token and SMTP configuration exist only in a
# root-owned /dev/shm directory. The target email necessarily persists in the
# newly created Keycloak user; operator-generated evidence/artifacts contain no
# plaintext PII, SMTP configuration or authorization material.

umask 077

readonly OPERATOR_NAME='invite-permanent-master-admin'
readonly SOURCE_REALM='solov'
readonly MASTER_REALM='master'
readonly SOURCE_ROLE='invoice-admin'
readonly MANAGEMENT_CLIENT='realm-management'
readonly MANAGEMENT_ROLE='admin'
readonly CONTRACT_VERSION='solov-permanent-master-admin-v1'
readonly REQUIRED_ACTIONS='["VERIFY_EMAIL","UPDATE_PASSWORD","CONFIGURE_TOTP"]'
readonly ACTION_LIFESPAN='900'
readonly BACKUP_NAMESPACE='solov-invoice-backup-v1'
readonly BACKUP_SIGNER='invoice-backup'
readonly EXPECTED_POSTGRES_IMAGE='postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2'
readonly FIXED_SUCCESS='{"status":"ok","operation":"permanent-master-admin-invitation","backup_restore":"passed","invitation":"dispatched","master_smtp_restored":true}'
readonly EXPECTED_SOURCE_TAG='v0.1.0-rc38-signed'
readonly RELEASE_SIGNATURE_NAMESPACE='solov-invoice-release-v1'
readonly RELEASE_SIGNER_IDENTITY='invoice-release@solov.cc'
readonly RELEASE_ALLOWED_SIGNERS_FILE='/root/invoice-system/trust/release-tree-allowed-signers'
readonly OFFSITE_ALLOWED_SIGNERS_FILE='/root/invoice-system/trust/offsite-allowed-signers'
readonly OFFSITE_SIGNATURE_NAMESPACE='solov-invoice-offsite-v1'
readonly OFFSITE_SIGNER_IDENTITY='invoice-offsite'
readonly GLOBAL_LOCK_FILE='/run/lock/solov-keycloak-permanent-master-admin.lock'
readonly ADMIN_FREEZE_ALLOWLIST='/www/server/panel/vhost/nginx/access/auth-admin.solov.cc.allow.conf'
readonly BACKUP_ROOT_EXPECTED='/root/invoice-system/keycloak-backups'
readonly KEYCLOAK_CONTAINER='invoice-keycloak-prod-keycloak-1'

die() {
  printf '%s: %s\n' "$OPERATOR_NAME" "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command is unavailable: $1"
}

(( EUID == 0 )) || die 'must run as root'

require_command sha256sum
require_command awk
require_command realpath
require_command dirname
require_command ssh-keygen
require_command stat
require_command tr
require_command sed
require_command grep
require_command wc

readonly OPERATOR_PATH="$(realpath -e -- "$0")"
readonly PROJECT_ROOT="$(realpath -e -- "$(dirname -- "$OPERATOR_PATH")/../..")"
case "$PROJECT_ROOT" in
  */source/invoice) readonly RELEASE_ROOT="$(realpath -e -- "$PROJECT_ROOT/../..")" SOURCE_RELATIVE_ROOT=source/invoice ;;
  */source) readonly RELEASE_ROOT="$(realpath -e -- "$PROJECT_ROOT/..")" SOURCE_RELATIVE_ROOT=source ;;
  *) die 'operator must run from an installed immutable release tree' ;;
esac
[[ "$PROJECT_ROOT" == "$RELEASE_ROOT/$SOURCE_RELATIVE_ROOT" ]] || die 'operator must run from an installed immutable release tree'
[[ ! -d "$RELEASE_ROOT/source/deploy" || ! -d "$RELEASE_ROOT/source/invoice/deploy" ]] || die 'ambiguous installed invoice deploy roots'
readonly SOURCE_COMMIT_FILE="$RELEASE_ROOT/SOURCE_COMMIT"
readonly SOURCE_TAG_FILE="$RELEASE_ROOT/SOURCE_TAG"
readonly KEYCLOAK_IMAGE_FILE="$RELEASE_ROOT/KEYCLOAK_IMAGE"
readonly SMTP_TRANSPORT_FILE="$RELEASE_ROOT/SMTP_TRANSPORT"
readonly RELEASE_BINDING_MANIFEST="$RELEASE_ROOT/RELEASE-TREE.sha256"
readonly RELEASE_BINDING_SIGNATURE="$RELEASE_ROOT/RELEASE-TREE.sha256.sig"

for release_file in "$SOURCE_COMMIT_FILE" "$SOURCE_TAG_FILE" "$KEYCLOAK_IMAGE_FILE" "$SMTP_TRANSPORT_FILE" "$RELEASE_BINDING_MANIFEST" "$RELEASE_BINDING_SIGNATURE" "$RELEASE_ALLOWED_SIGNERS_FILE"; do
  [[ -f "$release_file" && ! -L "$release_file" && -s "$release_file" ]] || die 'installed release binding is incomplete'
  [[ "$(stat -c '%u:%g:%a' -- "$release_file")" == '0:0:600' ]] || die 'installed release binding files must be root-owned mode 0600'
done
ssh-keygen -Y verify -f "$RELEASE_ALLOWED_SIGNERS_FILE" -I "$RELEASE_SIGNER_IDENTITY" \
  -n "$RELEASE_SIGNATURE_NAMESPACE" -s "$RELEASE_BINDING_SIGNATURE" <"$RELEASE_BINDING_MANIFEST" >/dev/null ||
  die 'installed release-tree signature verification failed'

release_signers=0
while IFS= read -r release_signer_line || [[ -n "$release_signer_line" ]]; do
  [[ -z "$release_signer_line" || "$release_signer_line" =~ ^[[:space:]]*# ]] && continue
  [[ "$release_signer_line" =~ ^invoice-release@solov\.cc[[:space:]]+namespaces=\"solov-invoice-release-v1\"[[:space:]]+ssh-ed25519[[:space:]]+[A-Za-z0-9+/=]+([[:space:]].*)?$ ]] ||
    die 'release allowed_signers contains an unexpected principal or namespace'
  release_signers=$((release_signers+1))
done <"$RELEASE_ALLOWED_SIGNERS_FILE"
(( release_signers > 0 )) || die 'release allowed_signers contains no trusted release key'

release_manifest_entries=0
declare -A release_manifest_seen=()
while IFS= read -r release_line || [[ -n "$release_line" ]]; do
  [[ "$release_line" =~ ^[0-9a-f]{64}\ \ (SOURCE_COMMIT|SOURCE_TAG|KEYCLOAK_IMAGE|SMTP_TRANSPORT|${SOURCE_RELATIVE_ROOT}/deploy/keycloak/(invite-permanent-master-admin|run-permanent-master-admin-maintenance)\.sh)$ ]] ||
    die 'installed release-tree manifest syntax is invalid'
  release_name="${BASH_REMATCH[1]}"
  [[ -z "${release_manifest_seen[$release_name]:-}" ]] || die 'installed release-tree manifest contains a duplicate'
  release_manifest_seen[$release_name]=1
  release_manifest_entries=$((release_manifest_entries+1))
done <"$RELEASE_BINDING_MANIFEST"
[[ "$release_manifest_entries" == 6 && -n "${release_manifest_seen[SOURCE_COMMIT]:-}" && -n "${release_manifest_seen[SOURCE_TAG]:-}" && -n "${release_manifest_seen[KEYCLOAK_IMAGE]:-}" && -n "${release_manifest_seen[SMTP_TRANSPORT]:-}" && -n "${release_manifest_seen[$SOURCE_RELATIVE_ROOT/deploy/keycloak/invite-permanent-master-admin.sh]:-}" && -n "${release_manifest_seen[$SOURCE_RELATIVE_ROOT/deploy/keycloak/run-permanent-master-admin-maintenance.sh]:-}" ]] ||
  die 'installed release-tree manifest does not bind the exact source/operation tuple'
(
  cd "$RELEASE_ROOT"
  sha256sum -c RELEASE-TREE.sha256 >/dev/null
) || die 'installed release tree does not match its signed manifest'

SOURCE_COMMIT="$(tr -d '\r\n' <"$SOURCE_COMMIT_FILE")"
SOURCE_TAG="$(tr -d '\r\n' <"$SOURCE_TAG_FILE")"
[[ "$SOURCE_COMMIT" =~ ^[0-9a-f]{40}$ && "$SOURCE_COMMIT" != '0000000000000000000000000000000000000000' ]] || die 'installed source commit is invalid'
[[ "$SOURCE_TAG" == "$EXPECTED_SOURCE_TAG" ]] || die 'installed source tag is not the exact RC38 signed tag'
ACTUAL_OPERATOR_SHA256="$(sha256sum "$OPERATOR_PATH" | awk '{print $1}')"
KEYCLOAK_CONFIG_IMAGE="$(sed -n 's/^config_image=//p' "$KEYCLOAK_IMAGE_FILE")"
KEYCLOAK_IMAGE_ID="$(sed -n 's/^image_id=//p' "$KEYCLOAK_IMAGE_FILE")"
[[ "$(wc -l <"$KEYCLOAK_IMAGE_FILE")" == 2 && "$(grep -Ec '^(config_image|image_id)=' "$KEYCLOAK_IMAGE_FILE")" == 2 ]] ||
  die 'signed Keycloak image binding must contain exactly two fields'
[[ "$KEYCLOAK_CONFIG_IMAGE" == 'invoice-keycloak:0.1.0-rc38' ]] || die 'signed Keycloak config image is not RC38'
[[ "$KEYCLOAK_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || die 'signed Keycloak immutable image ID is invalid'
SMTP_HOST_SHA256="$(sed -n 's/^host_sha256=//p' "$SMTP_TRANSPORT_FILE")"
[[ "$(wc -l <"$SMTP_TRANSPORT_FILE")" == 4 && "$(grep -Ec '^(host_sha256|port|ssl|starttls)=' "$SMTP_TRANSPORT_FILE")" == 4 ]] ||
  die 'signed SMTP transport binding must contain exactly four fields'
[[ "$SMTP_HOST_SHA256" =~ ^[0-9a-f]{64}$ ]] || die 'signed SMTP host hash is invalid'
grep -Fxq 'port=465' "$SMTP_TRANSPORT_FILE" && grep -Fxq 'ssl=true' "$SMTP_TRANSPORT_FILE" && grep -Fxq 'starttls=false' "$SMTP_TRANSPORT_FILE" ||
  die 'signed SMTP transport is not the approved SSL 465 contract'

for command_name in age basename chmod cmp cp curl date docker findmnt flock grep id jq mkdir mktemp mv openssl rm sleep sync tail; do
  require_command "$command_name"
done

: "${AGE_RECIPIENT_FILE:?set AGE_RECIPIENT_FILE to the approved age recipients file}"
: "${AGE_IDENTITY_FILE:?temporarily mount the matching offline age identity for the restore drill}"
: "${BACKUP_SIGNING_KEY_FILE:?temporarily mount the offline Ed25519 backup signing key}"
: "${BACKUP_ALLOWED_SIGNERS_FILE:?set the reviewed namespace-bound allowed_signers file}"
: "${KEYCLOAK_BOOTSTRAP_PASSWORD_FILE:?set KEYCLOAK_BOOTSTRAP_PASSWORD_FILE}"
[[ "${KEYCLOAK_ADMIN_WRITE_FREEZE_CONFIRMED:-}" == 'YES' ]] ||
  die 'set KEYCLOAK_ADMIN_WRITE_FREEZE_CONFIRMED=YES only after activating the loopback-only admin write freeze'

readonly ADMIN_BASE_URL="${KEYCLOAK_ADMIN_BASE_URL:-http://127.0.0.1:${KEYCLOAK_ADMIN_HTTP_PORT:-58181}}"
readonly PUBLIC_HOST="${KEYCLOAK_PUBLIC_HOST:-auth.solov.cc}"
readonly ADMIN_HOST="${KEYCLOAK_ADMIN_HOST:-auth-admin.solov.cc}"
readonly BOOTSTRAP_USERNAME="${KEYCLOAK_BOOTSTRAP_USERNAME:-invoice-bootstrap-admin}"
readonly PERMANENT_USERNAME="${KEYCLOAK_PERMANENT_MASTER_ADMIN_USERNAME:-invoice-permanent-master-admin}"
readonly DB_CONTAINER="${KEYCLOAK_POSTGRES_CONTAINER:-invoice-keycloak-prod-keycloak-postgres-1}"

[[ "$ADMIN_BASE_URL" =~ ^http://127\.0\.0\.1:([1-9][0-9]{0,4})$ ]] ||
  die 'Admin REST URL must be an explicit HTTP loopback endpoint'
(( 10#${BASH_REMATCH[1]} <= 65535 )) || die 'Admin REST port is invalid'
[[ "$PUBLIC_HOST" == 'auth.solov.cc' && "$ADMIN_HOST" == 'auth-admin.solov.cc' ]] ||
  die 'Keycloak production hostname contract drifted'
[[ "$BOOTSTRAP_USERNAME" =~ ^[A-Za-z0-9._@-]{3,128}$ ]] || die 'bootstrap username is invalid'
[[ "$PERMANENT_USERNAME" =~ ^[A-Za-z][A-Za-z0-9._-]{7,63}$ ]] || die 'permanent administrator username is invalid'
[[ "$PERMANENT_USERNAME" != "$BOOTSTRAP_USERNAME" ]] || die 'permanent administrator must use a different username'
[[ "$DB_CONTAINER" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$ ]] || die 'Keycloak PostgreSQL container name is invalid'

check_regular_file() {
  local path="$1"
  local label="$2"
  [[ -f "$path" && ! -L "$path" && -s "$path" ]] || die "$label must be a non-empty regular non-symlink file"
  (( $(stat -c '%s' -- "$path") <= 262144 )) || die "$label is unexpectedly large"
}

check_private_file() {
  local path="$1"
  local label="$2"
  check_regular_file "$path" "$label"
  local owner mode
  owner="$(stat -c '%u' -- "$path")"
  mode="$(stat -c '%a' -- "$path")"
  [[ "$owner" == 0 && ("$mode" == 400 || "$mode" == 600) ]] ||
    die "$label must be root-owned mode 0400 or 0600"
}

check_root_config_file() {
  local path="$1"
  local label="$2"
  check_regular_file "$path" "$label"
  [[ "$(stat -c '%u:%g:%a' -- "$path")" == '0:0:600' ]] ||
    die "$label must be root-owned mode 0600"
}

check_root_config_file "$AGE_RECIPIENT_FILE" 'age recipients file'
check_private_file "$AGE_IDENTITY_FILE" 'age identity file'
check_private_file "$BACKUP_SIGNING_KEY_FILE" 'backup signing key'
check_root_config_file "$BACKUP_ALLOWED_SIGNERS_FILE" 'backup allowed_signers file'
check_root_config_file "$OFFSITE_ALLOWED_SIGNERS_FILE" 'offsite allowed_signers file'
offsite_signers=0
while IFS= read -r offsite_signer_line || [[ -n "$offsite_signer_line" ]]; do
  [[ -z "$offsite_signer_line" || "$offsite_signer_line" =~ ^[[:space:]]*# ]] && continue
  [[ "$offsite_signer_line" =~ ^invoice-offsite[[:space:]]+namespaces=\"solov-invoice-offsite-v1\"[[:space:]]+ssh-ed25519[[:space:]]+[A-Za-z0-9+/=]+([[:space:]].*)?$ ]] ||
    die 'offsite allowed_signers contains an unexpected principal or namespace'
  offsite_signers=$((offsite_signers+1))
done <"$OFFSITE_ALLOWED_SIGNERS_FILE"
(( offsite_signers > 0 )) || die 'offsite allowed_signers contains no trusted offsite key'
check_regular_file "$KEYCLOAK_BOOTSTRAP_PASSWORD_FILE" 'bootstrap password file'
[[ "$(stat -c '%u:%a' -- "$KEYCLOAK_BOOTSTRAP_PASSWORD_FILE")" == '1000:400' ]] ||
  die 'bootstrap password file must retain the exact Keycloak uid 1000 mode 0400 contract'

jq -eRs 'test("^[A-Za-z0-9_+/=-]{32,256}\\r?\\n?$")' "$KEYCLOAK_BOOTSTRAP_PASSWORD_FILE" >/dev/null 2>&1 ||
  die 'bootstrap password file format is invalid'

signing_public="$(ssh-keygen -y -f "$BACKUP_SIGNING_KEY_FILE")" || die 'cannot read backup signing public key'
[[ "$signing_public" == ssh-ed25519\ * ]] || die 'backup signing key must be Ed25519'
unset signing_public

validate_allowed_signers() {
  local count=0 line
  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ -z "$line" || "$line" =~ ^[[:space:]]*# ]] && continue
    [[ "$line" =~ ^invoice-backup[[:space:]]+namespaces=\"solov-invoice-backup-v1\"[[:space:]]+ssh-ed25519[[:space:]]+[A-Za-z0-9+/=]+([[:space:]].*)?$ ]] ||
      return 1
    count=$((count+1))
  done <"$BACKUP_ALLOWED_SIGNERS_FILE"
  (( count > 0 ))
}
validate_allowed_signers || die 'backup allowed_signers policy is invalid'

[[ -d "$BACKUP_ROOT_EXPECTED" && ! -L "$BACKUP_ROOT_EXPECTED" ]] || die 'fixed Keycloak backup root must already be a real directory'
readonly BACKUP_ROOT="$(realpath -e -- "$BACKUP_ROOT_EXPECTED")"
[[ "$BACKUP_ROOT" == "$BACKUP_ROOT_EXPECTED" && "$(stat -c '%u:%a' -- "$BACKUP_ROOT")" == '0:700' ]] || die 'fixed Keycloak backup root must be root-owned mode 0700'
backup_filesystem="$(findmnt -n -T "$BACKUP_ROOT" -o FSTYPE)" || die 'cannot identify Keycloak backup filesystem'
case "$backup_filesystem" in tmpfs|ramfs|overlay) die 'Keycloak backup root is on an ephemeral filesystem' ;; esac
unset backup_filesystem
readonly AGE_IDENTITY_REAL="$(realpath -e -- "$AGE_IDENTITY_FILE")"
readonly SIGNING_KEY_REAL="$(realpath -e -- "$BACKUP_SIGNING_KEY_FILE")"
readonly SIGNING_DIRECTORY="$(realpath -e -- "$(dirname -- "$BACKUP_SIGNING_KEY_FILE")")"
[[ "$SIGNING_DIRECTORY" != "$BACKUP_ROOT" && "$SIGNING_DIRECTORY" != "$BACKUP_ROOT"/* ]] ||
  die 'backup signing key must not be stored under the fixed backup root'
[[ "$AGE_IDENTITY_REAL" != "$BACKUP_ROOT"/* && "$SIGNING_KEY_REAL" != "$BACKUP_ROOT"/* ]] ||
  die 'age identity and backup signing key must not be stored under the fixed backup root'

[[ -d /run/lock && ! -L /run/lock && "$(stat -c '%u:%g' /run/lock)" == '0:0' ]] ||
  die '/run/lock must be a real root-owned directory'
exec 9>"$GLOBAL_LOCK_FILE"
chmod 0600 "$GLOBAL_LOCK_FILE"
[[ "$(stat -c '%u:%g:%a' "$GLOBAL_LOCK_FILE")" == '0:0:600' ]] || die 'global operator lock permissions are unsafe'
flock -n 9 || die 'another permanent administrator invitation is running'

[[ -d /dev/shm && ! -L /dev/shm ]] || die '/dev/shm is required'
TMP_DIR="$(mktemp -d /dev/shm/keycloak-master-admin-invite.XXXXXX)"
[[ "$(stat -c '%u:%g:%a' -- "$TMP_DIR")" == '0:0:700' ]] || die 'ephemeral directory permissions are unsafe'
OPERATION_NONCE="$(openssl rand -hex 32)" || die 'cannot generate operation nonce'
[[ "$OPERATION_NONCE" =~ ^[0-9a-f]{64}$ ]] || die 'operation nonce generation failed'

[[ -f "$ADMIN_FREEZE_ALLOWLIST" && ! -L "$ADMIN_FREEZE_ALLOWLIST" ]] || die 'loopback-only administrator freeze allowlist is missing'
[[ "$(stat -c '%u:%g:%a' "$ADMIN_FREEZE_ALLOWLIST")" == '0:0:600' ]] ||
  die 'administrator freeze allowlist must be root-owned mode 0600'
printf '%s\n' 'allow 127.0.0.1;' 'allow ::1;' 'deny all;' >"$TMP_DIR/expected-admin-freeze-allowlist.conf"
chmod 0600 "$TMP_DIR/expected-admin-freeze-allowlist.conf"
cmp -s "$TMP_DIR/expected-admin-freeze-allowlist.conf" "$ADMIN_FREEZE_ALLOWLIST" ||
  die 'administrator routes are not frozen to loopback-only access'

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
RECORD_DIR="$BACKUP_ROOT/keycloak-master-admin-invite-$timestamp"
[[ ! -e "$RECORD_DIR" && ! -L "$RECORD_DIR" ]] || die 'backup record already exists'
mkdir -m 0700 -- "$RECORD_DIR"
[[ "$(stat -c '%u:%g:%a' -- "$RECORD_DIR")" == '0:0:700' ]] || die 'backup record directory permissions are unsafe'

AUTH_HEADER="$TMP_DIR/admin-auth.header"
BOOTSTRAP_PASSWORD_REQUEST_FILE="$TMP_DIR/bootstrap-password"
HTTP_BODY=''
HTTP_HEADERS=''
HTTP_STATUS=''
REQUEST_COUNTER=0
RESTORE_CONTAINER=''
USER_ID=''
USER_MUTATION_ATTEMPTED=false
SMTP_CHANGED=false
BACKUP_PUBLISHED=false
RESULT_PUBLISHED=false

tr -d '\r\n' <"$KEYCLOAK_BOOTSTRAP_PASSWORD_FILE" >"$BOOTSTRAP_PASSWORD_REQUEST_FILE"
chmod 0600 "$BOOTSTRAP_PASSWORD_REQUEST_FILE"

public_curl_args=(
  --silent --show-error --noproxy '*' --connect-timeout 5 --max-time 30
  --max-filesize 1048576 --proto '=http' --proto-redir '=http'
  --header "Host: $PUBLIC_HOST"
  --header "X-Forwarded-Host: $PUBLIC_HOST"
  --header 'X-Forwarded-Proto: https'
  --header 'X-Forwarded-Port: 443'
)

admin_curl_args=(
  --silent --show-error --noproxy '*' --connect-timeout 5 --max-time 30
  --max-filesize 1048576 --proto '=http' --proto-redir '=http'
  --header "Host: $ADMIN_HOST"
  --header "X-Forwarded-Host: $ADMIN_HOST"
  --header 'X-Forwarded-Proto: https'
  --header 'X-Forwarded-Port: 443'
)

refresh_admin_header_soft() {
  local response="$TMP_DIR/token-response.json" status
  rm -f -- "$response" "$AUTH_HEADER"
  status="$(curl "${public_curl_args[@]}" \
    --output "$response" --write-out '%{http_code}' \
    --request POST --header 'Content-Type: application/x-www-form-urlencoded' \
    --data-urlencode 'grant_type=password' --data-urlencode 'client_id=admin-cli' \
    --data-urlencode "username=$BOOTSTRAP_USERNAME" \
    --data-urlencode "password@$BOOTSTRAP_PASSWORD_REQUEST_FILE" \
    "$ADMIN_BASE_URL/realms/master/protocol/openid-connect/token")" || return 1
  [[ "$status" == 200 ]] || return 1
  jq -e 'type == "object" and (.access_token | type == "string" and length >= 64 and length <= 262144)' "$response" >/dev/null 2>&1 || return 1
  {
    printf 'Authorization: Bearer '
    jq -jr '.access_token' "$response"
    printf '\n'
  } >"$AUTH_HEADER"
  chmod 0600 "$AUTH_HEADER"
  rm -f -- "$response"
}

admin_request_once_soft() {
  local method="$1" path="$2" payload="$3" response="$4" headers="$5"
  local -a args
  [[ "$path" == /admin/realms* && "$path" != *$'\n'* && "$path" != *$'\r'* && "$path" != *'..'* ]] || return 1
  args=(
    "${admin_curl_args[@]}" --header "@$AUTH_HEADER" --header 'Accept: application/json'
    --output "$response" --dump-header "$headers" --write-out '%{http_code}' --request "$method"
  )
  if [[ -n "$payload" ]]; then
    [[ -f "$payload" && ! -L "$payload" ]] || return 1
    args+=(--header 'Content-Type: application/json' --data-binary "@$payload")
  fi
  curl "${args[@]}" "$ADMIN_BASE_URL$path"
}

admin_request_soft() {
  local method="$1" path="$2" payload="${3:-}" expected="${4:-200}"
  local status attempt
  ((REQUEST_COUNTER+=1))
  HTTP_BODY="$TMP_DIR/response.$REQUEST_COUNTER.json"
  HTTP_HEADERS="$TMP_DIR/response.$REQUEST_COUNTER.headers"
  for attempt in 1 2; do
    : >"$HTTP_BODY"
    : >"$HTTP_HEADERS"
    status="$(admin_request_once_soft "$method" "$path" "$payload" "$HTTP_BODY" "$HTTP_HEADERS")" || return 1
    if [[ "$status" == 401 && "$attempt" == 1 ]]; then
      refresh_admin_header_soft || return 1
      continue
    fi
    HTTP_STATUS="$status"
    break
  done
  case " $expected " in
    *" $HTTP_STATUS "*) return 0 ;;
    *) return 1 ;;
  esac
}

admin_request() {
  admin_request_soft "$@" || die 'Admin REST operation failed'
}

assert_json() {
  jq -e . "$1" >/dev/null 2>&1 || die 'Admin REST returned malformed JSON'
}

assert_uuid() {
  [[ "$1" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || die "$2 is not a canonical UUID"
}

db_query_to_file() {
  local sql="$1" output="$2"
  docker exec "$DB_CONTAINER" psql -X -v ON_ERROR_STOP=1 -U keycloak_owner -d keycloak -At -c "$sql" >"$output"
  chmod 0600 "$output"
}

query_realm_smtp() {
  local realm="$1" output="$2"
  [[ "$realm" == "$SOURCE_REALM" || "$realm" == "$MASTER_REALM" ]] || return 1
  db_query_to_file "SELECT COALESCE(jsonb_object_agg(c.name,c.value ORDER BY c.name),'{}'::jsonb)::text FROM realm r LEFT JOIN realm_smtp_config c ON c.realm_id=r.id WHERE r.name='$realm' GROUP BY r.id" "$output"
  jq -S -c 'if type == "object" then . else error("not object") end' "$output" >"$output.canonical"
  chmod 0600 "$output.canonical"
  mv -f -- "$output.canonical" "$output"
}

canonicalize_master_realm() {
  local input="$1" output="$2"
  jq -S -c 'if type == "object" and .realm == "master" then del(.smtpServer) else error("invalid master realm") end' \
    "$input" >"$output" || return 1
  chmod 0600 "$output"
}

capture_master_state_soft() {
  local realm_output="$1" canonical_output="$2" smtp_output="$3"
  admin_request_soft GET '/admin/realms/master' '' 200 || return 1
  cp "$HTTP_BODY" "$realm_output" || return 1
  chmod 0600 "$realm_output"
  canonicalize_master_realm "$realm_output" "$canonical_output" || return 1
  query_realm_smtp "$MASTER_REALM" "$smtp_output"
}

verify_master_state_soft() {
  local expected_canonical="$1" expected_smtp="$2" prefix="$3"
  capture_master_state_soft "$TMP_DIR/$prefix.realm.json" "$TMP_DIR/$prefix.canonical.json" "$TMP_DIR/$prefix.smtp.json" || return 1
  cmp -s "$expected_canonical" "$TMP_DIR/$prefix.canonical.json" &&
    cmp -s "$expected_smtp" "$TMP_DIR/$prefix.smtp.json"
}

restore_master_smtp_soft() {
  local current_realm="$TMP_DIR/master-restore-current.realm.json"
  local current_canonical="$TMP_DIR/master-restore-current.canonical.json"
  local current_smtp="$TMP_DIR/master-restore-current.smtp.json"
  local restore_payload="$TMP_DIR/master-restore-payload.json"
  local drifted=false
  [[ -s "$TMP_DIR/master-realm-original.canonical.json" && -s "$TMP_DIR/master-smtp-original.json" && -s "$TMP_DIR/source-smtp.json" ]] || return 1
  capture_master_state_soft "$current_realm" "$current_canonical" "$current_smtp" || return 1
  cmp -s "$TMP_DIR/master-realm-original.canonical.json" "$current_canonical" || drifted=true
  if cmp -s "$TMP_DIR/master-smtp-original.json" "$current_smtp"; then
    SMTP_CHANGED=false
    $drifted && return 2
    return 0
  fi
  cmp -s "$TMP_DIR/source-smtp.json" "$current_smtp" || return 1
  jq '.smtpServer={}' "$current_realm" >"$restore_payload" || return 1
  chmod 0600 "$restore_payload"
  admin_request_soft PUT '/admin/realms/master' "$restore_payload" 204 || return 1
  verify_master_state_soft "$current_canonical" "$TMP_DIR/master-smtp-original.json" 'master-restore-verified' || return 1
  SMTP_CHANGED=false
  $drifted && return 2
  return 0
}

delete_created_user_soft() {
  [[ -n "$USER_ID" ]] || return 1
  admin_request_soft DELETE "/admin/realms/master/users/$USER_ID" '' 204 || return 1
  admin_request_soft GET "/admin/realms/master/users/$USER_ID" '' 404
}

locate_created_user_soft() {
  local response
  admin_request_soft GET "/admin/realms/master/users?username=$PERMANENT_USERNAME&exact=true&first=0&max=2" '' 200 || return 1
  response="$HTTP_BODY"
  jq -e --arg username "$PERMANENT_USERNAME" --slurpfile target "$TMP_DIR/target.json" --arg contract "$CONTRACT_VERSION" --arg nonce "$OPERATION_NONCE" '
    type == "array" and length == 1 and
    .[0].username == $username and .[0].email == $target[0].email and
    .[0].attributes["solov.permanent-master-admin.contract"] == [$contract] and
    .[0].attributes["solov.permanent-master-admin.operation-nonce"] == [$nonce]
  ' "$response" >/dev/null 2>&1 || return 1
  USER_ID="$(jq -r '.[0].id' "$response")"
  [[ "$USER_ID" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]]
}

cleanup() {
  local rc=$? rollback_failed=false
  trap - EXIT HUP INT TERM
  set +e
  if (( rc != 0 )) && $USER_MUTATION_ATTEMPTED; then
    if [[ -z "$USER_ID" ]]; then
      locate_created_user_soft || rollback_failed=true
    fi
    if [[ -n "$USER_ID" ]]; then
      delete_created_user_soft || rollback_failed=true
    fi
    USER_MUTATION_ATTEMPTED=false
  fi
  if $SMTP_CHANGED; then
    restore_master_smtp_soft || rollback_failed=true
    SMTP_CHANGED=false
  fi
  if [[ -n "$RESTORE_CONTAINER" ]]; then
    docker rm --force "$RESTORE_CONTAINER" >/dev/null 2>&1 || rollback_failed=true
  fi
  rm -rf -- "$TMP_DIR" || rollback_failed=true
  if ! $BACKUP_PUBLISHED; then
    rm -rf -- "$RECORD_DIR" || rollback_failed=true
  elif ! $RESULT_PUBLISHED; then
    rm -f -- "$RECORD_DIR/invite-result.txt" "$RECORD_DIR/KEYCLOAK-PERMANENT-ADMIN-INVITE.sha256" "$RECORD_DIR/KEYCLOAK-PERMANENT-ADMIN-INVITE.sha256.sig" || rollback_failed=true
  fi
  if $rollback_failed; then
    printf '%s: CRITICAL rollback incomplete; keep bootstrap retirement blocked and inspect root-only state\n' "$OPERATOR_NAME" >&2
    rc=1
  fi
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM

sign_and_verify() {
  local manifest="$1"
  ssh-keygen -Y sign -q -f "$BACKUP_SIGNING_KEY_FILE" -n "$BACKUP_NAMESPACE" "$manifest" >/dev/null
  ssh-keygen -Y verify -f "$BACKUP_ALLOWED_SIGNERS_FILE" -I "$BACKUP_SIGNER" -n "$BACKUP_NAMESPACE" -s "$manifest.sig" <"$manifest" >/dev/null
}

sync_persistent_evidence() {
  local file
  for file in "$@"; do
    [[ -f "$file" && ! -L "$file" && -s "$file" ]] || return 1
    sync -f "$file" || return 1
  done
  sync -f "$RECORD_DIR" || return 1
  sync -f "$BACKUP_ROOT" || return 1
}

# Gate 1: produce a complete encrypted Keycloak database backup, restore it in
# a networkless disposable PostgreSQL container and sign the proof before the
# first Admin REST mutation.
[[ "$(docker inspect "$DB_CONTAINER" -f '{{.State.Running}}')" == true ]] || die 'Keycloak PostgreSQL is not running'
postgres_image="$(docker inspect "$DB_CONTAINER" -f '{{.Config.Image}}')"
[[ "$postgres_image" == "$EXPECTED_POSTGRES_IMAGE" ]] || die 'Keycloak PostgreSQL image is not the reviewed digest'

encrypted_backup="$RECORD_DIR/keycloak.postgres.dump.age"
docker exec "$DB_CONTAINER" pg_dump -U keycloak_owner -d keycloak --format=custom --no-owner --no-acl \
  | age -R "$AGE_RECIPIENT_FILE" -o "$encrypted_backup"
[[ -s "$encrypted_backup" ]] || die 'encrypted Keycloak backup is empty'
chmod 0600 "$encrypted_backup"

RESTORE_CONTAINER="keycloak-master-admin-restore-${timestamp,,}-$$"
docker run --detach --pull never --name "$RESTORE_CONTAINER" --network none \
  --env POSTGRES_PASSWORD=restore-drill-only --env POSTGRES_DB=keycloak \
  --tmpfs /var/lib/postgresql:rw,nosuid,nodev,size=2g \
  "$postgres_image" >/dev/null
restore_deadline=$((SECONDS+180))
until docker exec "$RESTORE_CONTAINER" pg_isready -U postgres -d keycloak >/dev/null 2>&1; do
  (( SECONDS < restore_deadline )) || die 'networkless restore PostgreSQL did not become ready'
  sleep 1
done
age --decrypt -i "$AGE_IDENTITY_FILE" "$encrypted_backup" \
  | docker exec -i "$RESTORE_CONTAINER" pg_restore -U postgres -d keycloak --no-owner --no-acl --exit-on-error
restore_state="$(docker exec "$RESTORE_CONTAINER" psql -X -v ON_ERROR_STOP=1 -U postgres -d keycloak -At -F '|' -c \
  "SELECT count(*) FILTER (WHERE name='master'),count(*) FILTER (WHERE name='solov'),count(*) FILTER (WHERE name='solov' AND EXISTS(SELECT 1 FROM realm_smtp_config c WHERE c.realm_id=realm.id)) FROM realm")"
[[ "$restore_state" == '1|1|1' ]] || die 'networkless restored database failed the realm/SMTP invariant'
unset restore_state
docker rm --force "$RESTORE_CONTAINER" >/dev/null
RESTORE_CONTAINER=''

printf '%s\n' \
  "source_commit=$SOURCE_COMMIT" \
  "source_tag=$SOURCE_TAG" \
  "operator_sha256=$ACTUAL_OPERATOR_SHA256" \
  'backup_kind=keycloak_full_database' \
  'encryption=age' \
  'restore_network=none' \
  'restore_status=passed' \
  'master_realm=present' \
  'source_realm=present' \
  'source_realm_smtp_config=present' \
  'recipient_data=not_recorded' \
  'secret_data=not_recorded' \
  >"$RECORD_DIR/restore-proof.txt"
chmod 0600 "$RECORD_DIR/restore-proof.txt"
(
  cd "$RECORD_DIR"
  sha256sum keycloak.postgres.dump.age restore-proof.txt >KEYCLOAK-PREINVITE-BACKUP.sha256
)
chmod 0600 "$RECORD_DIR/KEYCLOAK-PREINVITE-BACKUP.sha256"
sign_and_verify "$RECORD_DIR/KEYCLOAK-PREINVITE-BACKUP.sha256"
chmod 0600 "$RECORD_DIR/KEYCLOAK-PREINVITE-BACKUP.sha256.sig"
sync_persistent_evidence \
  "$encrypted_backup" "$RECORD_DIR/restore-proof.txt" \
  "$RECORD_DIR/KEYCLOAK-PREINVITE-BACKUP.sha256" "$RECORD_DIR/KEYCLOAK-PREINVITE-BACKUP.sha256.sig" ||
  die 'signed pre-invitation backup did not reach persistent storage'
BACKUP_PUBLISHED=true

# Two-phase off-site durability gate. The operator exposes only the root-only
# record identifier, then blocks without acquiring an Admin token. A separate
# workstation must download and verify the encrypted set, sign the exact
# manifest hash under the independent off-site namespace and upload the ACK.
record_id="$(basename "$RECORD_DIR")"
backup_manifest_hash="$(sha256sum "$RECORD_DIR/KEYCLOAK-PREINVITE-BACKUP.sha256" | awk '{print $1}')"
printf 'backup_ready;record=%s\n' "$RECORD_DIR"
continue_word=''
IFS= read -r -t 900 continue_word || die 'off-site backup acknowledgement timed out or stdin closed'
[[ "$continue_word" == 'continue' ]] || die 'off-site backup acknowledgement continuation was not exact'
offsite_ack="$RECORD_DIR/OFFSITE-ACK"
offsite_ack_signature="$RECORD_DIR/OFFSITE-ACK.sig"
for ack_file in "$offsite_ack" "$offsite_ack_signature"; do
  [[ -f "$ack_file" && ! -L "$ack_file" && -s "$ack_file" && "$(stat -c '%u:%g:%a' "$ack_file")" == '0:0:600' ]] ||
    die 'off-site acknowledgement files are missing or unsafe'
done
ssh-keygen -Y verify -f "$OFFSITE_ALLOWED_SIGNERS_FILE" -I "$OFFSITE_SIGNER_IDENTITY" \
  -n "$OFFSITE_SIGNATURE_NAMESPACE" -s "$offsite_ack_signature" <"$offsite_ack" >/dev/null ||
  die 'off-site acknowledgement signature verification failed'
jq -eRn --arg record "$record_id" --arg manifest "$backup_manifest_hash" '
  [inputs] as $lines |
  ($lines | length == 3) and
  ($lines[0] == ("record_id=" + $record)) and
  ($lines[1] == ("backup_manifest_sha256=" + $manifest)) and
  ($lines[2] | test("^verified_at_utc=[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$"))
' <"$offsite_ack" >/dev/null 2>&1 || die 'off-site acknowledgement content is invalid'
sync_persistent_evidence "$offsite_ack" "$offsite_ack_signature" || die 'off-site acknowledgement did not reach persistent storage'
unset backup_manifest_hash continue_word

# Gate 2: derive the only eligible invitation target from the existing solov
# invoice-admin role. No email is accepted from argv, environment or stdin.
[[ "$(docker inspect "$KEYCLOAK_CONTAINER" -f '{{.State.Running}}')" == true ]] || die 'Keycloak runtime is not running'
[[ "$(docker inspect "$KEYCLOAK_CONTAINER" -f '{{.State.Health.Status}}')" == healthy ]] || die 'Keycloak runtime is not healthy'
[[ "$(docker inspect "$KEYCLOAK_CONTAINER" -f '{{.Config.Image}}')" == "$KEYCLOAK_CONFIG_IMAGE" ]] || die 'running Keycloak config image differs from signed release binding'
[[ "$(docker inspect "$KEYCLOAK_CONTAINER" -f '{{.Image}}')" == "$KEYCLOAK_IMAGE_ID" ]] || die 'running Keycloak immutable image differs from signed release binding'
[[ "$(docker image inspect "$KEYCLOAK_CONFIG_IMAGE" -f '{{.Id}}')" == "$KEYCLOAK_IMAGE_ID" ]] || die 'local Keycloak tag differs from signed release binding'
docker inspect "$KEYCLOAK_CONTAINER" -f '{{json .Config.Env}}' >"$TMP_DIR/keycloak-runtime-env.json"
chmod 0600 "$TMP_DIR/keycloak-runtime-env.json"
jq -e '
  type == "array" and
  ([.[] | select(. == "KC_TLS_HOSTNAME_VERIFIER=DEFAULT")] | length == 1) and
  ([.[] | select(. == "KC_TRUSTSTORE_KUBERNETES_ENABLED=false")] | length == 1)
' "$TMP_DIR/keycloak-runtime-env.json" >/dev/null 2>&1 || die 'running Keycloak TLS/truststore environment differs from signed runtime contract'
refresh_admin_header_soft || die 'bootstrap token acquisition failed'
admin_request GET '/admin/realms/master'
assert_json "$HTTP_BODY"
jq -e '
  .realm == "master" and .enabled == true and
  .adminEventsDetailsEnabled == false and
  ((.attributes.frontendUrl // "") == "") and
  ((.smtpServer // {}) | length == 0)
' "$HTTP_BODY" >/dev/null 2>&1 || die 'master realm frontend or SMTP baseline is unsafe'
cp "$HTTP_BODY" "$TMP_DIR/master-realm-original.json"
chmod 0600 "$TMP_DIR/master-realm-original.json"
canonicalize_master_realm "$TMP_DIR/master-realm-original.json" "$TMP_DIR/master-realm-original.canonical.json" ||
  die 'master realm baseline canonicalization failed'

admin_request GET '/admin/realms/solov/roles/invoice-admin/users?first=0&max=2'
assert_json "$HTTP_BODY"
jq -e '
  type == "array" and length == 1 and
  .[0].enabled == true and .[0].emailVerified == true and
  (.[0].id | type == "string" and test("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")) and
  (.[0].email | type == "string" and length >= 3 and length <= 320 and
    test("^[A-Za-z0-9.!#$%&'\''*+/=?^_`{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$") and
    (test("[\\u0000-\\u001f\\u007f]") | not))
' "$HTTP_BODY" >/dev/null 2>&1 || die 'solov must contain exactly one enabled verified invoice administrator'
jq -c '.[0] | {email:.email}' "$HTTP_BODY" >"$TMP_DIR/target.json"
chmod 0600 "$TMP_DIR/target.json"

# Gate 3: derive SMTP only from the existing Keycloak realm_smtp_config table.
query_realm_smtp "$SOURCE_REALM" "$TMP_DIR/source-smtp.json"
jq -e '
  type == "object" and
  ((keys - ["auth","authType","debug","envelopeFrom","from","fromDisplayName","host","password","port","replyTo","replyToDisplayName","ssl","starttls","user"]) | length == 0) and
  (["auth","from","host","password","port","ssl","starttls","user"] - keys | length == 0) and
  (.host | type == "string" and length >= 1 and length <= 253) and
  .port == "465" and .ssl == "true" and .starttls == "false" and
  (.from | type == "string" and length >= 3 and length <= 320 and contains("@")) and
  .auth == "true" and (.user | type == "string" and length >= 1 and length <= 320) and
  (.password | type == "string" and length >= 1 and length <= 4096 and . != "**********") and
  ((.authType // "basic") == "basic") and ((.debug // "false") == "false") and
  all(to_entries[]; (.key | test("^[A-Za-z][A-Za-z0-9._-]{0,63}$")) and
    (.value | type == "string" and length <= 4096 and (test("[\\u0000-\\u001f\\u007f]") | not)))
' "$TMP_DIR/source-smtp.json" >/dev/null 2>&1 || die 'source realm SMTP configuration is incomplete or unsafe'
observed_smtp_host_sha256="$(jq -jr '.host' "$TMP_DIR/source-smtp.json" | sha256sum | awk '{print $1}')"
[[ "$observed_smtp_host_sha256" == "$SMTP_HOST_SHA256" ]] || die 'source realm SMTP host differs from signed release binding'
unset observed_smtp_host_sha256
query_realm_smtp "$MASTER_REALM" "$TMP_DIR/master-smtp-original.json"
jq -e 'type == "object" and length == 0' "$TMP_DIR/master-smtp-original.json" >/dev/null 2>&1 ||
  die 'master realm SMTP must be empty before invitation'

# Resolve the exact master realm-management/admin client role.
admin_request GET '/admin/realms/master/clients?clientId=realm-management&search=true&first=0&max=2'
assert_json "$HTTP_BODY"
management_client_id="$(jq -er '[.[] | select(.clientId == "realm-management")] | if length == 1 then .[0].id else error("not unique") end' "$HTTP_BODY" 2>/dev/null)" ||
  die 'realm-management client lookup is not unique'
assert_uuid "$management_client_id" 'realm-management client ID'
admin_request GET "/admin/realms/master/clients/$management_client_id/roles/admin"
assert_json "$HTTP_BODY"
jq -e 'type == "object" and .name == "admin" and (.id | type == "string") and .clientRole == true' "$HTTP_BODY" >/dev/null 2>&1 ||
  die 'realm-management admin role is unavailable'
cp "$HTTP_BODY" "$TMP_DIR/management-admin-role.json"
chmod 0600 "$TMP_DIR/management-admin-role.json"

# Create-only preflight. An existing username is classified strictly, then
# refused even when it matches; this operator never repairs or re-invites it.
admin_request GET "/admin/realms/master/users?username=$PERMANENT_USERNAME&exact=true&first=0&max=2"
assert_json "$HTTP_BODY"
cp "$HTTP_BODY" "$TMP_DIR/existing-user-query.json"
chmod 0600 "$TMP_DIR/existing-user-query.json"
existing_count="$(jq 'length' "$HTTP_BODY")"
[[ "$existing_count" == 0 || "$existing_count" == 1 ]] || die 'permanent administrator username lookup is not unique'
if [[ "$existing_count" == 1 ]]; then
  existing_id="$(jq -r '.[0].id' "$HTTP_BODY")"
  assert_uuid "$existing_id" 'existing permanent administrator ID'
  admin_request GET "/admin/realms/master/users/$existing_id/role-mappings/clients/$management_client_id"
  roles_file="$HTTP_BODY"
  assert_json "$roles_file"
  admin_request GET "/admin/realms/master/users/$existing_id/credentials"
  credentials_file="$HTTP_BODY"
  assert_json "$credentials_file"
  existing_identity_matches=false
  existing_pending_matches=false
  existing_enrolled_matches=false
  if jq -e --arg username "$PERMANENT_USERNAME" --slurpfile target "$TMP_DIR/target.json" --arg contract "$CONTRACT_VERSION" '
      .[0].username == $username and .[0].enabled == true and .[0].email == $target[0].email and
      .[0].attributes["solov.permanent-master-admin.contract"] == [$contract] and
      (.[0].attributes["solov.permanent-master-admin.operation-nonce"] | type == "array" and length == 1 and (.[0] | test("^[0-9a-f]{64}$")))
    ' "$TMP_DIR/existing-user-query.json" >/dev/null 2>&1 &&
    jq -e 'type == "array" and length == 1 and .[0].name == "admin" and .[0].clientRole == true' "$roles_file" >/dev/null 2>&1; then
    existing_identity_matches=true
  fi
  if $existing_identity_matches &&
    jq -e --argjson actions "$REQUIRED_ACTIONS" '.[0].emailVerified == false and ((.[0].requiredActions // []) | sort) == ($actions | sort)' "$TMP_DIR/existing-user-query.json" >/dev/null 2>&1 &&
    jq -e 'type == "array" and length == 0' "$credentials_file" >/dev/null 2>&1; then
    existing_pending_matches=true
  fi
  if $existing_identity_matches &&
    jq -e '.[0].emailVerified == true and ((.[0].requiredActions // []) | length == 0)' "$TMP_DIR/existing-user-query.json" >/dev/null 2>&1 &&
    jq -e 'type == "array" and any(.[]; .type == "password") and any(.[]; .type == "otp")' "$credentials_file" >/dev/null 2>&1; then
    existing_enrolled_matches=true
  fi
  if $existing_pending_matches || $existing_enrolled_matches; then
    die 'matching permanent administrator already exists; create-only invitation refused'
  fi
  die 'existing permanent administrator identity is inconsistent; manual inspection required'
fi

user_payload="$TMP_DIR/create-user.json"
jq -n --arg username "$PERMANENT_USERNAME" --slurpfile target "$TMP_DIR/target.json" --arg contract "$CONTRACT_VERSION" --arg nonce "$OPERATION_NONCE" --argjson actions "$REQUIRED_ACTIONS" '
  {
    username:$username,
    enabled:true,
    emailVerified:false,
    email:$target[0].email,
    requiredActions:$actions,
    attributes:{
      "solov.permanent-master-admin.contract":[$contract],
      "solov.permanent-master-admin.operation-nonce":[$nonce]
    }
  }
' >"$user_payload"
chmod 0600 "$user_payload"
USER_MUTATION_ATTEMPTED=true
admin_request POST '/admin/realms/master/users' "$user_payload" 201
location_header="$(grep -i '^location:' "$HTTP_HEADERS" | tail -n 1 | tr -d '\r' | sed 's/^[^:]*:[[:space:]]*//')"
USER_ID="${location_header##*/}"
assert_uuid "$USER_ID" 'created permanent administrator ID'
unset location_header

role_payload="$TMP_DIR/admin-role-mapping.json"
jq -s '.' "$TMP_DIR/management-admin-role.json" >"$role_payload"
chmod 0600 "$role_payload"
admin_request POST "/admin/realms/master/users/$USER_ID/role-mappings/clients/$management_client_id" "$role_payload" 204

admin_request GET "/admin/realms/master/users/$USER_ID"
assert_json "$HTTP_BODY"
jq -e --arg username "$PERMANENT_USERNAME" --slurpfile target "$TMP_DIR/target.json" --arg contract "$CONTRACT_VERSION" --arg nonce "$OPERATION_NONCE" --argjson actions "$REQUIRED_ACTIONS" '
  .username == $username and .enabled == true and .emailVerified == false and
  .email == $target[0].email and (.requiredActions | sort) == ($actions | sort) and
  .attributes["solov.permanent-master-admin.contract"] == [$contract] and
  .attributes["solov.permanent-master-admin.operation-nonce"] == [$nonce]
' "$HTTP_BODY" >/dev/null 2>&1 || die 'created permanent administrator contract did not persist'
admin_request GET "/admin/realms/master/users/$USER_ID/credentials"
assert_json "$HTTP_BODY"
jq -e 'type == "array" and length == 0' "$HTTP_BODY" >/dev/null 2>&1 || die 'new permanent administrator unexpectedly has credentials'
admin_request GET "/admin/realms/master/users/$USER_ID/role-mappings/clients/$management_client_id"
assert_json "$HTTP_BODY"
jq -e 'type == "array" and length == 1 and .[0].name == "admin" and .[0].clientRole == true' "$HTTP_BODY" >/dev/null 2>&1 ||
  die 'new permanent administrator role mapping is not exact'

# Configure master SMTP only for the invitation transaction, verify the exact
# database representation, dispatch all required actions in one email, then
# restore the previous master SMTP map before publishing success evidence.
verify_master_state_soft "$TMP_DIR/master-realm-original.canonical.json" "$TMP_DIR/master-smtp-original.json" 'master-pre-temporary-put' ||
  die 'master realm changed before temporary SMTP PUT; maintenance window is invalid'
jq --slurpfile smtp "$TMP_DIR/source-smtp.json" '.smtpServer=$smtp[0]' \
  "$TMP_DIR/master-pre-temporary-put.realm.json" >"$TMP_DIR/master-realm-temporary.json"
chmod 0600 "$TMP_DIR/master-realm-temporary.json"
SMTP_CHANGED=true
admin_request PUT '/admin/realms/master' "$TMP_DIR/master-realm-temporary.json" 204
verify_master_state_soft "$TMP_DIR/master-realm-original.canonical.json" "$TMP_DIR/source-smtp.json" 'master-post-temporary-put' ||
  die 'temporary master realm state differs from the exact baseline plus approved SMTP'

actions_payload="$TMP_DIR/required-actions.json"
printf '%s\n' "$REQUIRED_ACTIONS" >"$actions_payload"
chmod 0600 "$actions_payload"
admin_request PUT "/admin/realms/master/users/$USER_ID/execute-actions-email?lifespan=$ACTION_LIFESPAN" "$actions_payload" 204

verify_master_state_soft "$TMP_DIR/master-realm-original.canonical.json" "$TMP_DIR/source-smtp.json" 'master-pre-restore' ||
  die 'master realm changed before SMTP restoration; concurrent administration detected'
restore_status=0
restore_master_smtp_soft || restore_status=$?
[[ "$restore_status" == 0 ]] || die 'master SMTP restoration failed or preserved a concurrent non-SMTP change'

printf '%s\n' \
  "source_commit=$SOURCE_COMMIT" \
  "source_tag=$SOURCE_TAG" \
  "operator_sha256=$ACTUAL_OPERATOR_SHA256" \
  'operation=permanent_master_admin_invitation' \
  'status=dispatched' \
  'identity_source=unique_enabled_verified_source_invoice_admin' \
  'master_user=create_only' \
  'master_username=different_from_bootstrap' \
  'role=realm_management_admin' \
  'required_actions=verify_email_update_password_configure_totp' \
  'action_token_lifespan_seconds=900' \
  'smtp_source=source_realm_smtp_config' \
  'master_smtp_restored=true' \
  'recipient_data=not_recorded' \
  'domain_data=not_recorded' \
  'secret_data=not_recorded' \
  'bootstrap_retirement=still_blocked_pending_browser_enrollment_and_fresh_admin_canary' \
  >"$RECORD_DIR/invite-result.txt"
chmod 0600 "$RECORD_DIR/invite-result.txt"
(
  cd "$RECORD_DIR"
  sha256sum KEYCLOAK-PREINVITE-BACKUP.sha256 KEYCLOAK-PREINVITE-BACKUP.sha256.sig OFFSITE-ACK OFFSITE-ACK.sig invite-result.txt \
    >KEYCLOAK-PERMANENT-ADMIN-INVITE.sha256
)
chmod 0600 "$RECORD_DIR/KEYCLOAK-PERMANENT-ADMIN-INVITE.sha256"
sign_and_verify "$RECORD_DIR/KEYCLOAK-PERMANENT-ADMIN-INVITE.sha256"
chmod 0600 "$RECORD_DIR/KEYCLOAK-PERMANENT-ADMIN-INVITE.sha256.sig"
sync_persistent_evidence \
  "$RECORD_DIR/invite-result.txt" "$RECORD_DIR/KEYCLOAK-PERMANENT-ADMIN-INVITE.sha256" \
  "$RECORD_DIR/KEYCLOAK-PERMANENT-ADMIN-INVITE.sha256.sig" ||
  die 'signed invitation result did not reach persistent storage'
RESULT_PUBLISHED=true
USER_MUTATION_ATTEMPTED=false

printf '%s\n' "$FIXED_SUCCESS"
