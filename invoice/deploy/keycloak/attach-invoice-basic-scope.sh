#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

readonly expected_source_commit="${EXPECTED_SOURCE_COMMIT:?set EXPECTED_SOURCE_COMMIT from the verified signed release tag}"
readonly expected_source_tag="${EXPECTED_SOURCE_TAG:?set EXPECTED_SOURCE_TAG from the verified signed release tag}"
readonly expected_operator_sha256="${EXPECTED_OPERATOR_SHA256:?set EXPECTED_OPERATOR_SHA256 from the verified signed release tag}"
readonly keycloak_container='invoice-keycloak-prod-keycloak-1'
readonly postgres_container='invoice-keycloak-prod-keycloak-postgres-1'
readonly admin_password_file='/root/invoice-system/secrets/keycloak_bootstrap_admin_password'
readonly recipient='/root/invoice-system/incoming/rc22/backup-age-recipient'
readonly identity='/root/invoice-system/incoming/rc22/backup-age-identity'
readonly signing_key='/root/invoice-system/incoming/rc22/backup-signing-key'
readonly allowed_signers='/root/invoice-system/incoming/rc22/backup-allowed-signers'
readonly signature_namespace='solov-invoice-backup-v1'
readonly signer_identity='invoice-backup'

[[ "$expected_source_commit" =~ ^[0-9a-f]{40}$ ]]
[[ "$expected_source_tag" == 'v0.1.0-rc34-signed' ]]
[[ "$expected_operator_sha256" =~ ^[0-9a-f]{64}$ ]]
command -v sha256sum >/dev/null 2>&1
command -v awk >/dev/null 2>&1
actual_operator_sha256=$(sha256sum "$0" | awk '{print $1}')
[[ "$actual_operator_sha256" == "$expected_operator_sha256" ]] || {
  printf 'operator script does not match the verified signed release\n' >&2
  exit 1
}

for command_name in age awk chmod curl date docker grep install jq mktemp rm sha256sum sleep ssh-keygen stat; do
  command -v "$command_name" >/dev/null 2>&1 || {
    printf 'required command is unavailable: %s\n' "$command_name" >&2
    exit 1
  }
done

for secret_file in "$admin_password_file" "$recipient" "$identity" "$signing_key" "$allowed_signers"; do
  [[ -f "$secret_file" && ! -L "$secret_file" && -s "$secret_file" ]] || {
    printf 'required protected file is unavailable: %s\n' "$secret_file" >&2
    exit 1
  }
done
[[ "$(stat -c '%u:%a' "$admin_password_file")" == '1000:400' ]] || {
  printf 'Keycloak bootstrap password has unsafe ownership or mode\n' >&2
  exit 1
}
for private_file in "$identity" "$signing_key"; do
  private_owner=$(stat -c '%u' "$private_file")
  private_mode=$(stat -c '%a' "$private_file")
  [[ "$private_owner" == 0 && ("$private_mode" == 400 || "$private_mode" == 600) ]] || {
    printf 'protected private file has unsafe ownership or mode: %s\n' "$private_file" >&2
    exit 1
  }
done

timestamp=$(date -u +%Y%m%dT%H%M%SZ)
backup_dir="/root/invoice-system/keycloak-backups/invoice-auth-time-scope-$timestamp"
record_dir="/root/invoice-system/deployment-records/invoice-auth-time-scope-$timestamp"
install -d -m 0700 "$backup_dir" "$record_dir"
install -m 0600 "$0" "$record_dir/operator-script.sh"

tmp=''
restore_container=''
restore_volume=''
mutation_attempted=false
rollback_failed=false

verify_original_scope_set() {
  local output_file="$1"
  curl -fsS -A 'invoice-auth-time-scope/1' -H @"$tmp/admin.headers" \
    "http://127.0.0.1:58181/admin/realms/solov/clients/$client_uuid/default-client-scopes" \
    >"$output_file"
  jq -e '([.[].name] | sort) == ["email","profile","solov-token-contract"]' \
    "$output_file" >/dev/null 2>&1
}

cleanup() {
  local status=$?
  trap - EXIT
  set +e
  if (( status != 0 )) && [[ "$mutation_attempted" == true && -n "$tmp" && -s "$tmp/admin.headers" && -n "${client_uuid:-}" && -n "${basic_scope_id:-}" ]]; then
    current_scope_code=$(curl -sS -o "$tmp/default-scopes-rollback-check.json" -w '%{http_code}' \
      -A 'invoice-auth-time-scope/1' -H @"$tmp/admin.headers" \
      "http://127.0.0.1:58181/admin/realms/solov/clients/$client_uuid/default-client-scopes")
    if [[ "$current_scope_code" != 200 ]]; then
      rollback_failed=true
    elif jq -e --arg id "$basic_scope_id" 'any(.[]; .id == $id)' "$tmp/default-scopes-rollback-check.json" >/dev/null 2>&1; then
      rollback_code=$(curl -sS -o "$tmp/rollback-body" -w '%{http_code}' \
        -A 'invoice-auth-time-scope/1' -H @"$tmp/admin.headers" -X DELETE \
        "http://127.0.0.1:58181/admin/realms/solov/clients/$client_uuid/default-client-scopes/$basic_scope_id")
      [[ "$rollback_code" == 204 ]] || rollback_failed=true
    fi
    if [[ "$rollback_failed" == true ]] || ! verify_original_scope_set "$tmp/default-scopes-rollback.json"; then
      rollback_failed=true
      printf 'automatic scope rollback failed; use the signed backup and operator record\n' >&2
    fi
  fi
  if [[ -n "$restore_container" ]]; then
    docker rm --force "$restore_container" >/dev/null 2>&1 || true
  fi
  if [[ -n "$restore_volume" ]]; then
    docker volume rm "$restore_volume" >/dev/null 2>&1 || true
  fi
  if [[ -n "$tmp" ]]; then
    rm -rf -- "$tmp"
  fi
  if [[ "$rollback_failed" == true ]]; then
    status=1
  fi
  exit "$status"
}
trap cleanup EXIT

[[ "$(docker inspect -f '{{.State.Health.Status}}' "$keycloak_container")" == healthy ]]
[[ "$(docker inspect -f '{{.State.Health.Status}}' "$postgres_container")" == healthy ]]

# Capture a full, encrypted, transactionally consistent Keycloak database
# backup and prove it restores into an isolated PostgreSQL 18 instance before
# changing the client-scope association.
docker exec "$postgres_container" pg_dump -U keycloak_owner -d keycloak \
  --format=custom --no-owner --no-acl \
  | age -R "$recipient" -o "$backup_dir/keycloak.postgres.dump.age"
[[ -s "$backup_dir/keycloak.postgres.dump.age" ]]

postgres_image=$(docker inspect -f '{{.Config.Image}}' "$postgres_container")
[[ "$postgres_image" == postgres:18-alpine@sha256:d3e1620b530c944afa6e887d22eb899824da68e19c52024bf98f5220c88a65b2 ]]
restore_container="invoice-kc-auth-time-restore-${timestamp,,}"
restore_volume="invoice-kc-auth-time-restore-${timestamp,,}"
docker volume create "$restore_volume" >/dev/null
docker run --detach --name "$restore_container" --network none \
  --env POSTGRES_PASSWORD=restore-drill-only --env POSTGRES_DB=keycloak_restore \
  --volume "$restore_volume:/var/lib/postgresql" "$postgres_image" >/dev/null
deadline=$((SECONDS+120))
until docker logs "$restore_container" 2>&1 | grep -F 'PostgreSQL init process complete; ready for start up.' >/dev/null; do
  (( SECONDS < deadline )) || {
    printf 'isolated restore PostgreSQL did not become ready\n' >&2
    exit 1
  }
  sleep 1
done
for _ in 1 2 3; do
  docker exec "$restore_container" pg_isready -U postgres -d keycloak_restore >/dev/null
  sleep 1
done
age --decrypt -i "$identity" "$backup_dir/keycloak.postgres.dump.age" \
  | docker exec -i "$restore_container" pg_restore -U postgres -d keycloak_restore \
      --no-owner --no-acl --exit-on-error
[[ "$(docker exec "$restore_container" psql -X -U postgres -d keycloak_restore -Atc "select count(*) from realm where name='solov'")" == 1 ]]
[[ "$(docker exec "$restore_container" psql -X -U postgres -d keycloak_restore -Atc "select count(*) from client c join realm r on r.id=c.realm_id where r.name='solov' and c.client_id='invoice-web'")" == 1 ]]
restored_default_scopes=$(docker exec "$restore_container" psql -X -U postgres -d keycloak_restore -Atc \
  "select string_agg(cs.name,',' order by cs.name) from client c join realm r on r.id=c.realm_id join client_scope_client csc on csc.client_id=c.id join client_scope cs on cs.id=csc.scope_id where r.name='solov' and c.client_id='invoice-web' and csc.default_scope")
[[ "$restored_default_scopes" == 'email,profile,solov-token-contract' ]]
[[ "$(docker exec "$restore_container" psql -X -U postgres -d keycloak_restore -Atc "select count(*) from client c join realm r on r.id=c.realm_id join client_scope_client csc on csc.client_id=c.id where r.name='solov' and c.client_id='invoice-web' and not csc.default_scope")" == 0 ]]
docker rm --force "$restore_container" >/dev/null
restore_container=''
docker volume rm "$restore_volume" >/dev/null
restore_volume=''

tmp=$(mktemp -d /dev/shm/invoice-auth-time-scope.XXXXXX)
chmod 0700 "$tmp"
tr -d '\r\n' <"$admin_password_file" >"$tmp/admin-password"
curl -fsS -A 'invoice-auth-time-scope/1' -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode 'grant_type=password' --data-urlencode 'client_id=admin-cli' \
  --data-urlencode 'username=invoice-bootstrap-admin' \
  --data-urlencode "password@$tmp/admin-password" \
  http://127.0.0.1:58180/realms/master/protocol/openid-connect/token \
  >"$tmp/admin-token.json"
jq -er '.access_token' "$tmp/admin-token.json" >"$tmp/admin-token"
printf 'Authorization: Bearer %s\n' "$(<"$tmp/admin-token")" >"$tmp/admin.headers"
chmod 0600 "$tmp"/*

curl -fsS -A 'invoice-auth-time-scope/1' -H @"$tmp/admin.headers" \
  'http://127.0.0.1:58181/admin/realms/solov/clients?clientId=invoice-web&exact=true' \
  >"$tmp/invoice-clients.json"
jq -e 'length == 1 and .[0].clientId == "invoice-web"' "$tmp/invoice-clients.json" >/dev/null 2>&1
client_uuid=$(jq -er '.[0].id' "$tmp/invoice-clients.json")
[[ "$client_uuid" =~ ^[0-9a-fA-F-]{36}$ ]]

curl -fsS -A 'invoice-auth-time-scope/1' -H @"$tmp/admin.headers" \
  'http://127.0.0.1:58181/admin/realms/solov/client-scopes' \
  >"$tmp/client-scopes.json"
jq -e '[.[] | select(.name == "basic" and .protocol == "openid-connect")] | length == 1' \
  "$tmp/client-scopes.json" >/dev/null 2>&1
basic_scope_id=$(jq -er '.[] | select(.name == "basic" and .protocol == "openid-connect") | .id' "$tmp/client-scopes.json")
[[ "$basic_scope_id" =~ ^[0-9a-fA-F-]{36}$ ]]

curl -fsS -A 'invoice-auth-time-scope/1' -H @"$tmp/admin.headers" \
  "http://127.0.0.1:58181/admin/realms/solov/client-scopes/$basic_scope_id/protocol-mappers/models" \
  >"$tmp/basic-mappers.json"
jq -e '[.[] | select(
  .name == "auth_time" and
  .protocolMapper == "oidc-usersessionmodel-note-mapper" and
  .config["user.session.note"] == "AUTH_TIME" and
  .config["claim.name"] == "auth_time" and
  .config["jsonType.label"] == "long" and
  .config["id.token.claim"] == "true"
)] | length == 1' "$tmp/basic-mappers.json" >/dev/null 2>&1

verify_original_scope_set "$tmp/default-scopes-before.json"
curl -fsS -A 'invoice-auth-time-scope/1' -H @"$tmp/admin.headers" \
  "http://127.0.0.1:58181/admin/realms/solov/clients/$client_uuid/optional-client-scopes" \
  >"$tmp/optional-scopes-before.json"
jq -e 'length == 0' "$tmp/optional-scopes-before.json" >/dev/null 2>&1

jq -r 'sort_by(.name)[] | [.name,.protocol] | @tsv' "$tmp/default-scopes-before.json" \
  >"$backup_dir/invoice-web-default-scopes-before.tsv"
jq -r '.[] | select(.name == "auth_time") | [.name,.protocolMapper,.config["user.session.note"],.config["claim.name"],.config["jsonType.label"],.config["id.token.claim"]] | @tsv' \
  "$tmp/basic-mappers.json" >"$backup_dir/basic-auth-time-mapper.tsv"
printf 'source_commit=%s\nsource_tag=%s\noperator_sha256=%s\nkeycloak_image=%s\n' \
  "$expected_source_commit" "$expected_source_tag" "$actual_operator_sha256" \
  "$(docker inspect -f '{{.Config.Image}}' "$keycloak_container")" \
  >"$backup_dir/runtime.txt"
(
  cd "$backup_dir"
  sha256sum basic-auth-time-mapper.tsv invoice-web-default-scopes-before.tsv keycloak.postgres.dump.age runtime.txt \
    >KEYCLOAK-AUTH-TIME-BACKUP.sha256
  ssh-keygen -Y sign -q -f "$signing_key" -n "$signature_namespace" KEYCLOAK-AUTH-TIME-BACKUP.sha256
  ssh-keygen -Y verify -f "$allowed_signers" -I "$signer_identity" -n "$signature_namespace" \
    -s KEYCLOAK-AUTH-TIME-BACKUP.sha256.sig <KEYCLOAK-AUTH-TIME-BACKUP.sha256 >/dev/null
)

mutation_attempted=true
put_code=$(curl -sS -o "$tmp/scope-put-body" -w '%{http_code}' \
  -A 'invoice-auth-time-scope/1' -H @"$tmp/admin.headers" -X PUT \
  "http://127.0.0.1:58181/admin/realms/solov/clients/$client_uuid/default-client-scopes/$basic_scope_id")
[[ "$put_code" == 204 ]]

curl -fsS -A 'invoice-auth-time-scope/1' -H @"$tmp/admin.headers" \
  "http://127.0.0.1:58181/admin/realms/solov/clients/$client_uuid/default-client-scopes" \
  >"$tmp/default-scopes-after.json"
jq -e '([.[].name] | sort) == ["basic","email","profile","solov-token-contract"]' \
  "$tmp/default-scopes-after.json" >/dev/null 2>&1
curl -fsS -A 'invoice-auth-time-scope/1' -H @"$tmp/admin.headers" \
  "http://127.0.0.1:58181/admin/realms/solov/clients/$client_uuid/optional-client-scopes" \
  >"$tmp/optional-scopes-after.json"
jq -e 'length == 0' "$tmp/optional-scopes-after.json" >/dev/null 2>&1
[[ "$(docker inspect -f '{{.State.Health.Status}}' "$keycloak_container")" == healthy ]]
curl -fsS -A 'invoice-auth-time-scope/1' \
  http://127.0.0.1:58180/realms/solov/.well-known/openid-configuration \
  >"$tmp/discovery.json"
jq -e '.issuer == "https://auth.solov.cc/realms/solov" and (.acr_values_supported | index("urn:solov:loa:2") != null)' \
  "$tmp/discovery.json" >/dev/null 2>&1

jq -r 'sort_by(.name)[] | [.name,.protocol] | @tsv' "$tmp/default-scopes-after.json" \
  >"$record_dir/invoice-web-default-scopes-after.tsv"
printf 'source_commit=%s\nsource_tag=%s\noperator_sha256=%s\nchange=attach built-in basic as invoice-web default scope\nauth_time_mapper_configuration=verified\nreal_admin_step_up_canary=pending\nbootstrap_admin_retirement=pending_permanent_keycloak_admin_canary\nkeycloak_health=healthy\nrollback=DELETE the basic default-scope association or restore the signed encrypted database backup\nbackup_dir=%s\n' \
  "$expected_source_commit" "$expected_source_tag" "$actual_operator_sha256" "$backup_dir" >"$record_dir/result.txt"
chmod 0600 "$backup_dir"/* "$record_dir"/*
(
  cd "$record_dir"
  sha256sum invoice-web-default-scopes-after.tsv operator-script.sh result.txt \
    >KEYCLOAK-AUTH-TIME-DEPLOYMENT.sha256
  ssh-keygen -Y sign -q -f "$signing_key" -n "$signature_namespace" KEYCLOAK-AUTH-TIME-DEPLOYMENT.sha256
  ssh-keygen -Y verify -f "$allowed_signers" -I "$signer_identity" -n "$signature_namespace" \
    -s KEYCLOAK-AUTH-TIME-DEPLOYMENT.sha256.sig <KEYCLOAK-AUTH-TIME-DEPLOYMENT.sha256 >/dev/null
)

mutation_attempted=false
printf 'invoice_auth_time_scope=attached;backup=%s;record=%s;signature=verified\n' "$backup_dir" "$record_dir"
