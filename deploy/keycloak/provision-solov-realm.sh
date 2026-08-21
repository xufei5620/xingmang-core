#!/usr/bin/env bash
set -Eeuo pipefail

# One-time, fail-closed provisioning for the production SoloV realm.
#
# The hardened Keycloak runtime intentionally contains no kcadm client. This
# script talks only to the loopback-published Admin REST listener with
# curl/jq. Bootstrap credentials and bearer tokens never enter argv, stdout or
# the persistent filesystem. Re-running against an existing realm is an error;
# operators must inspect and explicitly remove a failed partial realm instead
# of accepting an implicit merge.

umask 077

readonly REALM_NAME="solov"
readonly CONTRACT_SCOPE="solov-token-contract"
readonly TOP_FLOW="solov-browser-step-up"
readonly AUTH_FLOW="solov-auth"
readonly LOA1_FLOW="solov-loa1"
readonly LOA2_FLOW="solov-loa2"
readonly FIXED_SUCCESS='{"status":"ok","realm":"solov","clients":4,"desktop_enabled":false}'

die() {
  printf 'provision-solov-realm: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command is unavailable: $1"
}

for command_name in curl jq openssl mktemp stat realpath dirname basename chmod chown ln rm tr; do
  require_command "$command_name"
done

(( EUID == 0 )) || die 'must run as root'

readonly TEST_MODE="${KEYCLOAK_PROVISION_TEST_MODE:-0}"
readonly ADMIN_BASE_URL="${KEYCLOAK_ADMIN_BASE_URL:-http://127.0.0.1:${KEYCLOAK_ADMIN_HTTP_PORT:-58181}}"
readonly PUBLIC_HOST="${KEYCLOAK_PUBLIC_HOST:-auth.solov.cc}"
readonly ADMIN_HOST="${KEYCLOAK_ADMIN_HOST:-auth-admin.solov.cc}"
readonly BOOTSTRAP_USERNAME="${KEYCLOAK_BOOTSTRAP_USERNAME:-invoice-bootstrap-admin}"
readonly BOOTSTRAP_PASSWORD_FILE="${KEYCLOAK_BOOTSTRAP_PASSWORD_FILE:?set KEYCLOAK_BOOTSTRAP_PASSWORD_FILE}"
readonly INVOICE_SECRET_FILE="${INVOICE_OIDC_CLIENT_SECRET_FILE:?set INVOICE_OIDC_CLIENT_SECRET_FILE}"
readonly SECRET_UID="${INVOICE_OIDC_CLIENT_SECRET_UID:-10001}"
readonly SECRET_GID="${INVOICE_OIDC_CLIENT_SECRET_GID:-10001}"

[[ "$TEST_MODE" == 0 || "$TEST_MODE" == 1 ]] || die 'KEYCLOAK_PROVISION_TEST_MODE must be 0 or 1'
if [[ "$TEST_MODE" == 0 ]]; then
  [[ "$ADMIN_BASE_URL" =~ ^http://127\.0\.0\.1:([1-9][0-9]{0,4})$ ]] ||
    die 'production Admin REST URL must be an HTTP loopback host and explicit port'
  (( 10#${BASH_REMATCH[1]} <= 65535 )) || die 'Keycloak Admin REST port is invalid'
  [[ "$PUBLIC_HOST" == 'auth.solov.cc' ]] || die 'production public Keycloak host must be auth.solov.cc'
  [[ "$ADMIN_HOST" == 'auth-admin.solov.cc' ]] || die 'production admin Keycloak host must be auth-admin.solov.cc'
else
  [[ "$ADMIN_BASE_URL" =~ ^http://[A-Za-z0-9.-]+:([1-9][0-9]{0,4})$ ]] ||
    die 'test Admin REST URL must be an explicit HTTP host and port'
  (( 10#${BASH_REMATCH[1]} <= 65535 )) || die 'test Keycloak Admin REST port is invalid'
  [[ "$PUBLIC_HOST" =~ ^[A-Za-z0-9.-]+(:[1-9][0-9]{0,4})?$ ]] || die 'test public Host header is invalid'
  [[ "$ADMIN_HOST" =~ ^[A-Za-z0-9.-]+(:[1-9][0-9]{0,4})?$ ]] || die 'test admin Host header is invalid'
fi

[[ "$BOOTSTRAP_USERNAME" =~ ^[A-Za-z0-9._@-]{3,128}$ ]] || die 'bootstrap username is invalid'
[[ "$SECRET_UID" =~ ^[0-9]+$ && "$SECRET_GID" =~ ^[0-9]+$ ]] || die 'secret UID/GID must be numeric'
(( 10#$SECRET_UID > 0 && 10#$SECRET_GID > 0 )) || die 'secret UID/GID must be non-root'

[[ -f "$BOOTSTRAP_PASSWORD_FILE" && ! -L "$BOOTSTRAP_PASSWORD_FILE" && -r "$BOOTSTRAP_PASSWORD_FILE" ]] ||
  die 'bootstrap password must be a readable regular non-symlink file'
[[ "$(stat -c '%a' -- "$BOOTSTRAP_PASSWORD_FILE")" == 400 ]] || die 'bootstrap password file mode must be 0400'
jq -eRs 'test("^[A-Za-z0-9_+/=-]{32,256}\\r?\\n?$")' \
  "$BOOTSTRAP_PASSWORD_FILE" >/dev/null 2>&1 || die 'bootstrap password file has an invalid format'

[[ "$INVOICE_SECRET_FILE" == /* ]] || die 'invoice OIDC client secret path must be absolute'
readonly SECRET_PARENT="$(dirname -- "$INVOICE_SECRET_FILE")"
readonly SECRET_BASENAME="$(basename -- "$INVOICE_SECRET_FILE")"
[[ "$SECRET_BASENAME" != '.' && "$SECRET_BASENAME" != '..' && "$SECRET_BASENAME" != */* && -n "$SECRET_BASENAME" ]] ||
  die 'invoice OIDC client secret basename is invalid'
[[ -d "$SECRET_PARENT" && ! -L "$SECRET_PARENT" ]] || die 'invoice OIDC secret parent must be a real directory'
readonly SECRET_PARENT_REAL="$(realpath -e -- "$SECRET_PARENT")"
[[ "$SECRET_PARENT_REAL/$SECRET_BASENAME" == "$INVOICE_SECRET_FILE" ]] ||
  die 'invoice OIDC secret path must be canonical'
[[ "$(stat -c '%u' -- "$SECRET_PARENT")" == 0 && "$(stat -c '%a' -- "$SECRET_PARENT")" == 700 ]] ||
  die 'invoice OIDC secret parent must be root-owned mode 0700'
[[ ! -e "$INVOICE_SECRET_FILE" && ! -L "$INVOICE_SECRET_FILE" ]] ||
  die 'invoice OIDC client secret already exists'

openssl version >/dev/null 2>&1 || die 'OpenSSL runtime check failed'

TMP_DIR=''
PUBLISHED_SECRET_TMP=''
cleanup() {
  local rc=$?
  set +e
  [[ -z "$PUBLISHED_SECRET_TMP" ]] || rm -f -- "$PUBLISHED_SECRET_TMP"
  [[ -z "$TMP_DIR" ]] || rm -rf -- "$TMP_DIR"
  trap - EXIT HUP INT TERM
  exit "$rc"
}
trap cleanup EXIT HUP INT TERM

[[ -d /dev/shm && ! -L /dev/shm ]] || die '/dev/shm is required for ephemeral authorization material'
TMP_DIR="$(mktemp -d /dev/shm/solov-keycloak-provision.XXXXXX)"
[[ "$(stat -c '%u:%g:%a' -- "$TMP_DIR")" == "0:0:700" ]] || die 'ephemeral directory permissions are unsafe'

AUTH_HEADER="$TMP_DIR/admin-auth.header"
BOOTSTRAP_PASSWORD_REQUEST_FILE="$TMP_DIR/bootstrap-password"
tr -d '\r\n' <"$BOOTSTRAP_PASSWORD_FILE" >"$BOOTSTRAP_PASSWORD_REQUEST_FILE"
chmod 0400 "$BOOTSTRAP_PASSWORD_REQUEST_FILE"
REQUEST_COUNTER=0
HTTP_STATUS=''
HTTP_BODY=''
HTTP_HEADERS=''

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

refresh_admin_header() {
  local response="$TMP_DIR/token-response.json"
  local status
  rm -f -- "$response" "$AUTH_HEADER"
  if ! status="$(curl "${public_curl_args[@]}" \
      --output "$response" --write-out '%{http_code}' \
      --request POST \
      --header 'Content-Type: application/x-www-form-urlencoded' \
      --data-urlencode 'grant_type=password' \
      --data-urlencode 'client_id=admin-cli' \
      --data-urlencode "username=$BOOTSTRAP_USERNAME" \
      --data-urlencode "password@$BOOTSTRAP_PASSWORD_REQUEST_FILE" \
      "$ADMIN_BASE_URL/realms/master/protocol/openid-connect/token")"; then
    rm -f -- "$response"
    die 'bootstrap token endpoint transport failed'
  fi
  [[ "$status" == 200 ]] || {
    rm -f -- "$response"
    die "bootstrap token endpoint returned HTTP $status"
  }
  jq -e 'type == "object" and (.access_token | type == "string" and length >= 64 and length <= 262144)' \
    "$response" >/dev/null 2>&1 || {
      rm -f -- "$response"
      die 'bootstrap token response is invalid'
    }
  {
    printf 'Authorization: Bearer '
    jq -jr '.access_token' "$response"
    printf '\n'
  } >"$AUTH_HEADER"
  chmod 0600 "$AUTH_HEADER"
  rm -f -- "$response"
}

admin_request_once() {
  local method="$1"
  local path="$2"
  local payload="$3"
  local response="$4"
  local headers="$5"
  local -a args
  [[ "$path" == /admin/realms* && "$path" != *$'\n'* && "$path" != *$'\r'* ]] || die 'unsafe Admin REST path'
  args=(
    "${admin_curl_args[@]}"
    --header "@$AUTH_HEADER"
    --header 'Accept: application/json'
    --output "$response"
    --dump-header "$headers"
    --write-out '%{http_code}'
    --request "$method"
  )
  if [[ -n "$payload" ]]; then
    [[ -f "$payload" && ! -L "$payload" ]] || die 'Admin REST payload is unavailable'
    args+=(--header 'Content-Type: application/json' --data-binary "@$payload")
  fi
  curl "${args[@]}" "$ADMIN_BASE_URL$path"
}

admin_request() {
  local method="$1"
  local path="$2"
  local payload="${3:-}"
  local expected="${4:-200}"
  local status
  local attempt
  ((REQUEST_COUNTER+=1))
  HTTP_BODY="$TMP_DIR/response.$REQUEST_COUNTER.json"
  HTTP_HEADERS="$TMP_DIR/response.$REQUEST_COUNTER.headers"
  for attempt in 1 2; do
    : >"$HTTP_BODY"
    : >"$HTTP_HEADERS"
    if ! status="$(admin_request_once "$method" "$path" "$payload" "$HTTP_BODY" "$HTTP_HEADERS")"; then
      die "Admin REST transport failed for $method ${path%%\?*}"
    fi
    if [[ "$status" == 401 && "$attempt" == 1 ]]; then
      refresh_admin_header
      continue
    fi
    HTTP_STATUS="$status"
    break
  done
  case " $expected " in
    *" $HTTP_STATUS "*) ;;
    *) die "Admin REST returned HTTP $HTTP_STATUS for $method ${path%%\?*}" ;;
  esac
}

payload_file() {
  local name="$1"
  printf '%s/%s.json' "$TMP_DIR" "$name"
}

assert_json() {
  local file="$1"
  jq -e . "$file" >/dev/null 2>&1 || die 'Admin REST returned malformed JSON'
}

assert_uuid() {
  [[ "$1" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] ||
    die "$2 is not a canonical UUID"
}

lookup_client_id() {
  local client_id="$1"
  admin_request GET "/admin/realms/$REALM_NAME/clients?clientId=$client_id&search=true&first=0&max=2"
  assert_json "$HTTP_BODY"
  local id
  id="$(jq -er --arg client "$client_id" \
    '[.[] | select(.clientId == $client)] | if length == 1 then .[0].id else error("not unique") end' \
    "$HTTP_BODY" 2>/dev/null)" || die "client lookup is not unique: $client_id"
  assert_uuid "$id" "client $client_id ID"
  printf '%s' "$id"
}

lookup_scope_id() {
  local scope_name="$1"
  admin_request GET "/admin/realms/$REALM_NAME/client-scopes"
  assert_json "$HTTP_BODY"
  local id
  id="$(jq -er --arg scope "$scope_name" \
    '[.[] | select(.name == $scope)] | if length == 1 then .[0].id else error("not unique") end' \
    "$HTTP_BODY" 2>/dev/null)" || die "client scope lookup is not unique: $scope_name"
  assert_uuid "$id" "client scope $scope_name ID"
  printf '%s' "$id"
}

lookup_role_representation() {
  local role_name="$1"
  local destination="$2"
  admin_request GET "/admin/realms/$REALM_NAME/roles/$role_name"
  assert_json "$HTTP_BODY"
  jq -e --arg role "$role_name" \
    'select(type == "object" and .name == $role and (.id | type == "string"))' \
    "$HTTP_BODY" >"$destination" 2>/dev/null || die "realm role lookup failed: $role_name"
  local id
  id="$(jq -r '.id' "$destination")"
  assert_uuid "$id" "realm role $role_name ID"
}

flow_executions() {
  local flow_alias="$1"
  admin_request GET "/admin/realms/$REALM_NAME/authentication/flows/$flow_alias/executions"
  assert_json "$HTTP_BODY"
  jq -e 'type == "array"' "$HTTP_BODY" >/dev/null 2>&1 || die "flow executions are invalid: $flow_alias"
}

lookup_provider_execution() {
  local flow_alias="$1"
  local provider_id="$2"
  flow_executions "$flow_alias"
  local id
  id="$(jq -er --arg provider "$provider_id" \
    '[.[] | select(.level == 0 and ((.authenticationFlow // false) == false) and .providerId == $provider)] |
     if length == 1 then .[0].id else error("not unique") end' "$HTTP_BODY" 2>/dev/null)" ||
    die "execution lookup is not unique: $flow_alias/$provider_id"
  assert_uuid "$id" "execution $flow_alias/$provider_id ID"
  printf '%s' "$id"
}

lookup_subflow_execution() {
  local parent_alias="$1"
  local subflow_alias="$2"
  flow_executions "$parent_alias"
  local id
  id="$(jq -er --arg alias "$subflow_alias" \
    '[.[] | select(.level == 0 and .authenticationFlow == true and .displayName == $alias)] |
     if length == 1 then .[0].id else error("not unique") end' "$HTTP_BODY" 2>/dev/null)" ||
    die "sub-flow execution lookup is not unique: $parent_alias/$subflow_alias"
  assert_uuid "$id" "sub-flow execution $parent_alias/$subflow_alias ID"
  printf '%s' "$id"
}

set_requirement() {
  local flow_alias="$1"
  local execution_id="$2"
  local requirement="$3"
  local payload
  [[ "$requirement" == REQUIRED || "$requirement" == CONDITIONAL || "$requirement" == ALTERNATIVE || "$requirement" == DISABLED ]] ||
    die 'invalid execution requirement'
  flow_executions "$flow_alias"
  payload="$(payload_file "requirement-$REQUEST_COUNTER")"
  jq -e --arg id "$execution_id" --arg requirement "$requirement" \
    '[.[] | select(.id == $id)] |
     if length == 1 then .[0] + {requirement: $requirement} else error("not unique") end' \
    "$HTTP_BODY" >"$payload" 2>/dev/null || die "cannot prepare execution requirement: $flow_alias"
  admin_request PUT "/admin/realms/$REALM_NAME/authentication/flows/$flow_alias/executions" "$payload" 204
  flow_executions "$flow_alias"
  jq -e --arg id "$execution_id" --arg requirement "$requirement" \
    '[.[] | select(.id == $id and .requirement == $requirement)] | length == 1' \
    "$HTTP_BODY" >/dev/null 2>&1 || die "execution requirement did not persist: $flow_alias"
}

add_execution() {
  local flow_alias="$1"
  local provider_id="$2"
  local priority="$3"
  local requirement="$4"
  local payload id
  payload="$(payload_file "add-execution-$REQUEST_COUNTER")"
  jq -n --arg provider "$provider_id" --argjson priority "$priority" \
    '{provider:$provider,priority:$priority}' >"$payload"
  admin_request POST "/admin/realms/$REALM_NAME/authentication/flows/$flow_alias/executions/execution" "$payload" 201
  id="$(lookup_provider_execution "$flow_alias" "$provider_id")"
  set_requirement "$flow_alias" "$id" "$requirement"
  printf '%s' "$id"
}

add_subflow() {
  local parent_alias="$1"
  local subflow_alias="$2"
  local priority="$3"
  local requirement="$4"
  local description="$5"
  local payload id
  payload="$(payload_file "add-subflow-$REQUEST_COUNTER")"
  jq -n --arg alias "$subflow_alias" --arg description "$description" --argjson priority "$priority" \
    '{alias:$alias,type:"basic-flow",provider:"",priority:$priority,description:$description}' >"$payload"
  admin_request POST "/admin/realms/$REALM_NAME/authentication/flows/$parent_alias/executions/flow" "$payload" 201
  id="$(lookup_subflow_execution "$parent_alias" "$subflow_alias")"
  set_requirement "$parent_alias" "$id" "$requirement"
  printf '%s' "$id"
}

attach_execution_config() {
  local execution_id="$1"
  local alias="$2"
  local config_json="$3"
  local payload
  payload="$(payload_file "execution-config-$REQUEST_COUNTER")"
  jq -n --arg alias "$alias" --argjson config "$config_json" '{alias:$alias,config:$config}' >"$payload"
  admin_request POST "/admin/realms/$REALM_NAME/authentication/executions/$execution_id/config" "$payload" 201
}

refresh_admin_header

# Refuse both an existing successful realm and a failed partial previous run.
admin_request GET "/admin/realms/$REALM_NAME" '' '200 404'
[[ "$HTTP_STATUS" == 404 ]] || die "realm already exists: $REALM_NAME"

realm_payload="$(payload_file realm)"
cat >"$realm_payload" <<'JSON'
{
  "realm":"solov",
  "displayName":"SoloV 统一登录",
  "enabled":true,
  "sslRequired":"external",
  "registrationAllowed":false,
  "registrationEmailAsUsername":true,
  "rememberMe":false,
  "verifyEmail":false,
  "loginWithEmailAllowed":true,
  "duplicateEmailsAllowed":false,
  "resetPasswordAllowed":true,
  "editUsernameAllowed":false,
  "defaultSignatureAlgorithm":"RS256",
  "revokeRefreshToken":true,
  "refreshTokenMaxReuse":0,
  "accessTokenLifespan":300,
  "accessTokenLifespanForImplicitFlow":300,
  "ssoSessionIdleTimeout":1800,
  "ssoSessionMaxLifespan":28800,
  "clientSessionIdleTimeout":1800,
  "clientSessionMaxLifespan":28800,
  "accessCodeLifespan":60,
  "accessCodeLifespanUserAction":300,
  "accessCodeLifespanLogin":300,
  "actionTokenGeneratedByAdminLifespan":900,
  "actionTokenGeneratedByUserLifespan":300,
  "passwordPolicy":"hashAlgorithm(argon2) and length(14) and digits(1) and lowerCase(1) and upperCase(1) and specialChars(1) and notUsername and notEmail and passwordHistory(5)",
  "otpPolicyType":"totp",
  "otpPolicyAlgorithm":"HmacSHA1",
  "otpPolicyDigits":6,
  "otpPolicyLookAheadWindow":1,
  "otpPolicyPeriod":30,
  "otpPolicyCodeReusable":false,
  "bruteForceProtected":true,
  "permanentLockout":false,
  "maxTemporaryLockouts":0,
  "bruteForceStrategy":"MULTIPLE",
  "maxFailureWaitSeconds":900,
  "minimumQuickLoginWaitSeconds":60,
  "waitIncrementSeconds":60,
  "quickLoginCheckMilliSeconds":1000,
  "maxDeltaTimeSeconds":43200,
  "failureFactor":5,
  "maxSecondaryAuthFailures":100,
  "eventsEnabled":true,
  "eventsExpiration":7776000,
  "eventsListeners":[],
  "adminEventsEnabled":true,
  "adminEventsDetailsEnabled":false,
  "attributes":{"acr.loa.map":"{\"urn:solov:loa:2\":2}"}
}
JSON
admin_request POST '/admin/realms' "$realm_payload" 201
admin_request GET "/admin/realms/$REALM_NAME"
assert_json "$HTTP_BODY"
jq -e '
  .realm == "solov" and .enabled == true and .sslRequired == "external" and
  .registrationAllowed == false and .verifyEmail == false and
  .defaultSignatureAlgorithm == "RS256" and .accessTokenLifespan == 300 and
  .ssoSessionIdleTimeout == 1800 and .ssoSessionMaxLifespan == 28800 and
  .bruteForceProtected == true and .permanentLockout == false and
  .attributes["acr.loa.map"] == "{\"urn:solov:loa:2\":2}"
' "$HTTP_BODY" >/dev/null 2>&1 || die 'realm security policy did not persist'

invoice_user_role_payload="$(payload_file invoice-user-role)"
invoice_admin_role_payload="$(payload_file invoice-admin-role)"
cat >"$invoice_user_role_payload" <<'JSON'
{"name":"invoice-user","description":"SoloV invoice user"}
JSON
cat >"$invoice_admin_role_payload" <<'JSON'
{"name":"invoice-admin","description":"SoloV finance invoice administrator"}
JSON
admin_request POST "/admin/realms/$REALM_NAME/roles" "$invoice_user_role_payload" 201
admin_request POST "/admin/realms/$REALM_NAME/roles" "$invoice_admin_role_payload" 201

invoice_user_role_rep="$(payload_file invoice-user-role-representation)"
invoice_admin_role_rep="$(payload_file invoice-admin-role-representation)"
lookup_role_representation invoice-user "$invoice_user_role_rep"
lookup_role_representation invoice-admin "$invoice_admin_role_rep"

default_role_rep="$(payload_file default-role-representation)"
lookup_role_representation default-roles-solov "$default_role_rep"
default_role_id="$(jq -r '.id' "$default_role_rep")"
default_composite_payload="$(payload_file default-role-composite)"
jq -s '.' "$invoice_user_role_rep" >"$default_composite_payload"
admin_request POST "/admin/realms/$REALM_NAME/roles-by-id/$default_role_id/composites" "$default_composite_payload" 204
admin_request GET "/admin/realms/$REALM_NAME/roles-by-id/$default_role_id/composites"
assert_json "$HTTP_BODY"
jq -e '
  ([.[] | select(.name == "invoice-user")] | length == 1) and
  ([.[] | select(.name == "invoice-admin")] | length == 0)
' "$HTTP_BODY" >/dev/null 2>&1 || die 'default role composite is unsafe'

top_flow_payload="$(payload_file top-flow)"
cat >"$top_flow_payload" <<'JSON'
{"alias":"solov-browser-step-up","description":"Password LoA1 and password plus TOTP LoA2","providerId":"basic-flow","topLevel":true,"builtIn":false}
JSON
admin_request POST "/admin/realms/$REALM_NAME/authentication/flows" "$top_flow_payload" 201

cookie_execution="$(add_execution "$TOP_FLOW" auth-cookie 10 ALTERNATIVE)"
add_subflow "$TOP_FLOW" "$AUTH_FLOW" 20 ALTERNATIVE 'SoloV authentication levels' >/dev/null
add_subflow "$AUTH_FLOW" "$LOA1_FLOW" 10 CONDITIONAL 'Password authentication level 1' >/dev/null
add_subflow "$AUTH_FLOW" "$LOA2_FLOW" 20 CONDITIONAL 'TOTP step-up authentication level 2' >/dev/null

loa1_condition="$(add_execution "$LOA1_FLOW" conditional-level-of-authentication 10 REQUIRED)"
password_execution="$(add_execution "$LOA1_FLOW" auth-username-password-form 20 REQUIRED)"
loa2_condition="$(add_execution "$LOA2_FLOW" conditional-level-of-authentication 10 REQUIRED)"
otp_execution="$(add_execution "$LOA2_FLOW" auth-otp-form 20 REQUIRED)"

attach_execution_config "$loa1_condition" solov-loa1-condition '{"loa-condition-level":"1","loa-max-age":"28800"}'
attach_execution_config "$password_execution" solov-password-amr '{"default.reference.value":"pwd","default.reference.maxAge":"28800"}'
attach_execution_config "$loa2_condition" solov-loa2-condition '{"loa-condition-level":"2","loa-max-age":"0"}'
attach_execution_config "$otp_execution" solov-otp-amr '{"default.reference.value":"otp","default.reference.maxAge":"600"}'

bind_flow_payload="$(payload_file bind-browser-flow)"
cat >"$bind_flow_payload" <<'JSON'
{"realm":"solov","browserFlow":"solov-browser-step-up"}
JSON
admin_request PUT "/admin/realms/$REALM_NAME" "$bind_flow_payload" 204
admin_request GET "/admin/realms/$REALM_NAME"
jq -e '.browserFlow == "solov-browser-step-up"' "$HTTP_BODY" >/dev/null 2>&1 || die 'custom browser flow was not bound'

scope_payload="$(payload_file token-contract-scope)"
cat >"$scope_payload" <<'JSON'
{
  "name":"solov-token-contract",
  "description":"SoloV roles, ACR and AMR token contract",
  "protocol":"openid-connect",
  "attributes":{"include.in.token.scope":"false","display.on.consent.screen":"false"},
  "protocolMappers":[
    {
      "name":"roles","protocol":"openid-connect","protocolMapper":"oidc-usermodel-realm-role-mapper","consentRequired":false,
      "config":{"usermodel.realmRoleMapping.rolePrefix":"","multivalued":"true","claim.name":"roles","jsonType.label":"String","access.token.claim":"true","id.token.claim":"true","userinfo.token.claim":"false"}
    },
    {
      "name":"acr","protocol":"openid-connect","protocolMapper":"oidc-acr-mapper","consentRequired":false,
      "config":{"access.token.claim":"true","id.token.claim":"true"}
    },
    {
      "name":"amr","protocol":"openid-connect","protocolMapper":"oidc-amr-mapper","consentRequired":false,
      "config":{"access.token.claim":"true","id.token.claim":"true"}
    }
  ]
}
JSON
admin_request POST "/admin/realms/$REALM_NAME/client-scopes" "$scope_payload" 201
contract_scope_id="$(lookup_scope_id "$CONTRACT_SCOPE")"
profile_scope_id="$(lookup_scope_id profile)"
email_scope_id="$(lookup_scope_id email)"
admin_request GET "/admin/realms/$REALM_NAME/client-scopes/$contract_scope_id/protocol-mappers/models"
assert_json "$HTTP_BODY"
jq -e '
  length == 3 and
  ([.[] | select(.name == "roles" and .protocolMapper == "oidc-usermodel-realm-role-mapper" and .config["claim.name"] == "roles" and .config.multivalued == "true" and .config["id.token.claim"] == "true" and .config["access.token.claim"] == "true")] | length == 1) and
  ([.[] | select(.name == "acr" and .protocolMapper == "oidc-acr-mapper")] | length == 1) and
  ([.[] | select(.name == "amr" and .protocolMapper == "oidc-amr-mapper")] | length == 1)
' "$HTTP_BODY" >/dev/null 2>&1 || die 'token contract protocol mappers are invalid'

invoice_client_payload="$(payload_file invoice-web-client)"
cat >"$invoice_client_payload" <<'JSON'
{
  "clientId":"invoice-web","name":"SoloV 发票中心","enabled":true,"protocol":"openid-connect","bearerOnly":false,
  "publicClient":false,"clientAuthenticatorType":"client-secret","standardFlowEnabled":true,"implicitFlowEnabled":false,
  "directAccessGrantsEnabled":false,"serviceAccountsEnabled":false,"authorizationServicesEnabled":false,"consentRequired":false,
  "frontchannelLogout":false,"fullScopeAllowed":false,
  "redirectUris":["https://invoice.solov.cc/api/v1/auth/callback"],"webOrigins":[],
  "defaultClientScopes":["profile","email","solov-token-contract"],"optionalClientScopes":[],
  "attributes":{
    "pkce.code.challenge.method":"S256",
    "post.logout.redirect.uris":"https://invoice.solov.cc/",
    "backchannel.logout.url":"https://invoice.solov.cc/api/v1/auth/backchannel-logout",
    "backchannel.logout.session.required":"true",
    "backchannel.logout.revoke.offline.tokens":"false",
    "logout.confirmation.enabled":"false",
    "id.token.signed.response.alg":"RS256",
    "access.token.signed.response.alg":"RS256",
    "oauth2.device.authorization.grant.enabled":"false",
    "oidc.ciba.grant.enabled":"false"
  }
}
JSON

sub2api_client_payload="$(payload_file sub2api-client)"
cat >"$sub2api_client_payload" <<'JSON'
{
  "clientId":"sub2api","name":"SoloV Sub2API","enabled":true,"protocol":"openid-connect","bearerOnly":false,
  "publicClient":false,"clientAuthenticatorType":"client-secret","standardFlowEnabled":true,"implicitFlowEnabled":false,
  "directAccessGrantsEnabled":false,"serviceAccountsEnabled":false,"authorizationServicesEnabled":false,"consentRequired":false,
  "frontchannelLogout":false,"fullScopeAllowed":false,
  "redirectUris":["https://api.solov.cc/api/v1/auth/oauth/oidc/callback"],"webOrigins":[],
  "defaultClientScopes":["profile","email","solov-token-contract"],"optionalClientScopes":[],
  "attributes":{"pkce.code.challenge.method":"S256","id.token.signed.response.alg":"RS256","access.token.signed.response.alg":"RS256","oauth2.device.authorization.grant.enabled":"false","oidc.ciba.grant.enabled":"false"}
}
JSON

newapi_client_payload="$(payload_file newapi-client)"
cat >"$newapi_client_payload" <<'JSON'
{
  "clientId":"newapi","name":"SoloV New API","enabled":true,"protocol":"openid-connect","bearerOnly":false,
  "publicClient":false,"clientAuthenticatorType":"client-secret","standardFlowEnabled":true,"implicitFlowEnabled":false,
  "directAccessGrantsEnabled":false,"serviceAccountsEnabled":false,"authorizationServicesEnabled":false,"consentRequired":false,
  "frontchannelLogout":false,"fullScopeAllowed":false,
  "redirectUris":["https://xm.solov.cc/oauth/solov-sso"],"webOrigins":[],
  "defaultClientScopes":["profile","email","solov-token-contract"],"optionalClientScopes":[],
  "attributes":{"id.token.signed.response.alg":"RS256","access.token.signed.response.alg":"RS256","oauth2.device.authorization.grant.enabled":"false","oidc.ciba.grant.enabled":"false"}
}
JSON

desktop_client_payload="$(payload_file invoice-desktop-client)"
cat >"$desktop_client_payload" <<'JSON'
{
  "clientId":"invoice-desktop","name":"SoloV Invoice Desktop","enabled":false,"protocol":"openid-connect","bearerOnly":false,
  "publicClient":true,"clientAuthenticatorType":"none","standardFlowEnabled":true,"implicitFlowEnabled":false,
  "directAccessGrantsEnabled":false,"serviceAccountsEnabled":false,"authorizationServicesEnabled":false,"consentRequired":false,
  "frontchannelLogout":false,"fullScopeAllowed":false,"redirectUris":[],"webOrigins":[],
  "defaultClientScopes":["profile","email","solov-token-contract"],"optionalClientScopes":[],
  "attributes":{"pkce.code.challenge.method":"S256","id.token.signed.response.alg":"RS256","access.token.signed.response.alg":"RS256","oauth2.device.authorization.grant.enabled":"false","oidc.ciba.grant.enabled":"false"}
}
JSON

for client_payload in "$invoice_client_payload" "$sub2api_client_payload" "$newapi_client_payload" "$desktop_client_payload"; do
  admin_request POST "/admin/realms/$REALM_NAME/clients" "$client_payload" 201
done

invoice_client_id="$(lookup_client_id invoice-web)"
sub2api_client_id="$(lookup_client_id sub2api)"
newapi_client_id="$(lookup_client_id newapi)"
desktop_client_id="$(lookup_client_id invoice-desktop)"

reconcile_client_scopes() {
  local client_uuid="$1"
  local scope_id scope_name
  admin_request GET "/admin/realms/$REALM_NAME/clients/$client_uuid/default-client-scopes"
  assert_json "$HTTP_BODY"
  while IFS=$'\t' read -r scope_id scope_name; do
    [[ -z "$scope_id" ]] && continue
    case "$scope_name" in
      profile|email|"$CONTRACT_SCOPE") ;;
      *)
        assert_uuid "$scope_id" 'default client scope ID'
        admin_request DELETE "/admin/realms/$REALM_NAME/clients/$client_uuid/default-client-scopes/$scope_id" '' 204
        ;;
    esac
  done < <(jq -r '.[] | [.id,.name] | @tsv' "$HTTP_BODY")

  admin_request GET "/admin/realms/$REALM_NAME/clients/$client_uuid/default-client-scopes"
  for required_scope_id in "$profile_scope_id" "$email_scope_id" "$contract_scope_id"; do
    if ! jq -e --arg id "$required_scope_id" 'any(.[]; .id == $id)' "$HTTP_BODY" >/dev/null 2>&1; then
      admin_request PUT "/admin/realms/$REALM_NAME/clients/$client_uuid/default-client-scopes/$required_scope_id" '' 204
    fi
  done

  admin_request GET "/admin/realms/$REALM_NAME/clients/$client_uuid/optional-client-scopes"
  assert_json "$HTTP_BODY"
  while IFS= read -r scope_id; do
    [[ -z "$scope_id" ]] && continue
    assert_uuid "$scope_id" 'optional client scope ID'
    admin_request DELETE "/admin/realms/$REALM_NAME/clients/$client_uuid/optional-client-scopes/$scope_id" '' 204
  done < <(jq -r '.[].id' "$HTTP_BODY")

  admin_request GET "/admin/realms/$REALM_NAME/clients/$client_uuid/default-client-scopes"
  jq -e --arg contract "$CONTRACT_SCOPE" \
    '([.[].name] | sort) == (["email","profile",$contract] | sort)' "$HTTP_BODY" >/dev/null 2>&1 ||
    die 'exact default client-scope set did not persist'
  admin_request GET "/admin/realms/$REALM_NAME/clients/$client_uuid/optional-client-scopes"
  jq -e 'length == 0' "$HTTP_BODY" >/dev/null 2>&1 || die 'optional/offline client scopes remain attached'
}

for client_uuid in "$invoice_client_id" "$sub2api_client_id" "$newapi_client_id" "$desktop_client_id"; do
  reconcile_client_scopes "$client_uuid"
done

invoice_user_only_payload="$(payload_file invoice-user-only-scope)"
invoice_admin_scope_payload="$(payload_file invoice-admin-scope)"
jq -s '.' "$invoice_user_role_rep" >"$invoice_user_only_payload"
jq -s '.' "$invoice_user_role_rep" "$invoice_admin_role_rep" >"$invoice_admin_scope_payload"
admin_request POST "/admin/realms/$REALM_NAME/clients/$invoice_client_id/scope-mappings/realm" "$invoice_admin_scope_payload" 204
for client_uuid in "$sub2api_client_id" "$newapi_client_id" "$desktop_client_id"; do
  admin_request POST "/admin/realms/$REALM_NAME/clients/$client_uuid/scope-mappings/realm" "$invoice_user_only_payload" 204
done

verify_role_scope() {
  local client_uuid="$1"
  local expect_admin="$2"
  admin_request GET "/admin/realms/$REALM_NAME/clients/$client_uuid/scope-mappings/realm"
  assert_json "$HTTP_BODY"
  jq -e --argjson expect_admin "$expect_admin" '
    ([.[] | select(.name == "invoice-user")] | length == 1) and
    (([.[] | select(.name == "invoice-admin")] | length == 1) == $expect_admin) and
    ([.[] | select(.name != "invoice-user" and .name != "invoice-admin")] | length == 0)
  ' "$HTTP_BODY" >/dev/null 2>&1 || die 'client role scope mapping is not least privilege'
}
verify_role_scope "$invoice_client_id" true
verify_role_scope "$sub2api_client_id" false
verify_role_scope "$newapi_client_id" false
verify_role_scope "$desktop_client_id" false

verify_client() {
  local client_uuid="$1"
  local client_id="$2"
  admin_request GET "/admin/realms/$REALM_NAME/clients/$client_uuid"
  assert_json "$HTTP_BODY"
  jq -e --arg client "$client_id" '
    .clientId == $client and .protocol == "openid-connect" and
    .standardFlowEnabled == true and .implicitFlowEnabled == false and
    .directAccessGrantsEnabled == false and .serviceAccountsEnabled == false and
    .frontchannelLogout == false and .fullScopeAllowed == false and
    ((.webOrigins // []) | length == 0)
  ' "$HTTP_BODY" >/dev/null 2>&1 || die "client baseline is unsafe: $client_id"
}
verify_client "$invoice_client_id" invoice-web
verify_client "$sub2api_client_id" sub2api
verify_client "$newapi_client_id" newapi
verify_client "$desktop_client_id" invoice-desktop

admin_request GET "/admin/realms/$REALM_NAME/clients/$invoice_client_id"
jq -e '
  .enabled == true and .publicClient == false and
  .redirectUris == ["https://invoice.solov.cc/api/v1/auth/callback"] and
  .attributes["pkce.code.challenge.method"] == "S256" and
  .attributes["post.logout.redirect.uris"] == "https://invoice.solov.cc/" and
  .attributes["backchannel.logout.url"] == "https://invoice.solov.cc/api/v1/auth/backchannel-logout" and
  .attributes["backchannel.logout.session.required"] == "true" and
  .attributes["backchannel.logout.revoke.offline.tokens"] == "false" and
  .attributes["logout.confirmation.enabled"] == "false"
' "$HTTP_BODY" >/dev/null 2>&1 || die 'invoice-web OIDC/logout contract is invalid'

admin_request GET "/admin/realms/$REALM_NAME/clients/$sub2api_client_id"
jq -e '
  .enabled == true and .publicClient == false and
  .redirectUris == ["https://api.solov.cc/api/v1/auth/oauth/oidc/callback"] and
  .attributes["pkce.code.challenge.method"] == "S256"
' "$HTTP_BODY" >/dev/null 2>&1 || die 'Sub2API OIDC contract is invalid'

admin_request GET "/admin/realms/$REALM_NAME/clients/$newapi_client_id"
jq -e '
  .enabled == true and .publicClient == false and
  .redirectUris == ["https://xm.solov.cc/oauth/solov-sso"] and
  (.attributes["pkce.code.challenge.method"] == null)
' "$HTTP_BODY" >/dev/null 2>&1 || die 'New API OIDC contract is invalid'

admin_request GET "/admin/realms/$REALM_NAME/clients/$desktop_client_id"
jq -e '
  .enabled == false and .publicClient == true and .clientAuthenticatorType == "none" and
  ((.redirectUris // []) | length == 0) and ((.webOrigins // []) | length == 0) and
  .attributes["pkce.code.challenge.method"] == "S256"
' "$HTTP_BODY" >/dev/null 2>&1 || die 'disabled desktop OIDC contract is invalid'

# Assert the exact flow shape and attached configuration IDs after all writes.
flow_executions "$TOP_FLOW"
jq -e --arg auth "$AUTH_FLOW" '
  ([.[] | select(.level == 0)] | length == 2) and
  ([.[] | select(.level == 0 and .providerId == "auth-cookie" and .requirement == "ALTERNATIVE")] | length == 1) and
  ([.[] | select(.level == 0 and .authenticationFlow == true and .displayName == $auth and .requirement == "ALTERNATIVE")] | length == 1)
' "$HTTP_BODY" >/dev/null 2>&1 || die 'top-level browser flow shape is invalid'
flow_executions "$AUTH_FLOW"
jq -e --arg loa1 "$LOA1_FLOW" --arg loa2 "$LOA2_FLOW" '
  ([.[] | select(.level == 0)] | length == 2) and
  ([.[] | select(.level == 0 and .displayName == $loa1 and .requirement == "CONDITIONAL")] | length == 1) and
  ([.[] | select(.level == 0 and .displayName == $loa2 and .requirement == "CONDITIONAL")] | length == 1)
' "$HTTP_BODY" >/dev/null 2>&1 || die 'LoA sub-flow shape is invalid'
flow_executions "$LOA1_FLOW"
jq -e '
  length == 2 and
  ([.[] | select(.providerId == "conditional-level-of-authentication" and .requirement == "REQUIRED" and (.authenticationConfig | type == "string"))] | length == 1) and
  ([.[] | select(.providerId == "auth-username-password-form" and .requirement == "REQUIRED" and (.authenticationConfig | type == "string"))] | length == 1)
' "$HTTP_BODY" >/dev/null 2>&1 || die 'LoA1 execution/config shape is invalid'
flow_executions "$LOA2_FLOW"
jq -e '
  length == 2 and
  ([.[] | select(.providerId == "conditional-level-of-authentication" and .requirement == "REQUIRED" and (.authenticationConfig | type == "string"))] | length == 1) and
  ([.[] | select(.providerId == "auth-otp-form" and .requirement == "REQUIRED" and (.authenticationConfig | type == "string"))] | length == 1)
' "$HTTP_BODY" >/dev/null 2>&1 || die 'LoA2 execution/config shape is invalid'

# This is deliberately the only client-secret endpoint called by the script.
# Sub2API/New API secrets remain visible only through the restricted Admin
# Console and are never written or printed by this provisioner.
admin_request GET "/admin/realms/$REALM_NAME/clients/$invoice_client_id/client-secret"
assert_json "$HTTP_BODY"
jq -e 'type == "object" and (.value | type == "string" and length >= 32 and length <= 256 and test("^[A-Za-z0-9_-]+$"))' \
  "$HTTP_BODY" >/dev/null 2>&1 || die 'invoice-web client secret response is invalid'
PUBLISHED_SECRET_TMP="$(mktemp --tmpdir="$SECRET_PARENT" .invoice-oidc-client-secret.XXXXXX)"
jq -jr '.value' "$HTTP_BODY" >"$PUBLISHED_SECRET_TMP"
chmod 0400 "$PUBLISHED_SECRET_TMP"
chown "$SECRET_UID:$SECRET_GID" "$PUBLISHED_SECRET_TMP"
[[ "$(stat -c '%u:%g:%a' -- "$PUBLISHED_SECRET_TMP")" == "$SECRET_UID:$SECRET_GID:400" ]] ||
  die 'invoice-web client secret staging permissions are unsafe'
# link(2) publishes without replacement and fails if another process won the
# destination race. The temporary link is then removed.
ln -- "$PUBLISHED_SECRET_TMP" "$INVOICE_SECRET_FILE" || die 'atomic invoice-web client secret publication failed'
rm -f -- "$PUBLISHED_SECRET_TMP"
PUBLISHED_SECRET_TMP=''
[[ -f "$INVOICE_SECRET_FILE" && ! -L "$INVOICE_SECRET_FILE" && "$(stat -c '%u:%g:%a' -- "$INVOICE_SECRET_FILE")" == "$SECRET_UID:$SECRET_GID:400" ]] ||
  die 'published invoice-web client secret permissions are unsafe'

printf '%s\n' "$FIXED_SUCCESS"
