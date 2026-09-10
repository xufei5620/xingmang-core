#!/usr/bin/env bash
set -Eeuo pipefail

# This script changes Docker network membership. Run it only in the approved
# production change window, from the release root, after the read-only
# preflight has proved the planned CIDRs do not overlap.
env_file=${PRODUCTION_ENV_FILE:-deploy/.env.production}
test -s "$env_file"

read_setting() {
  local key=$1 value count
  count=$(grep -c -E "^${key}=" "$env_file" || true)
  [[ "$count" == 1 ]] || { echo "expected exactly one ${key} in ${env_file}" >&2; exit 2; }
  value=$(grep -E "^${key}=" "$env_file" | cut -d= -f2-)
  [[ -n "$value" && "$value" != *$'\r'* && "$value" != *$'\n'* ]] || {
    echo "invalid ${key}" >&2; exit 2;
  }
  printf '%s' "$value"
}

sub2_network=$(read_setting SUB2API_PROJECTION_NETWORK)
sub2_subnet=$(read_setting SUB2API_PROJECTION_SUBNET)
newapi_network=$(read_setting NEWAPI_PROJECTION_NETWORK)
newapi_subnet=$(read_setting NEWAPI_PROJECTION_SUBNET)

for name in "$sub2_network" "$newapi_network"; do
  [[ "$name" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$ ]] || { echo "unsafe network name: $name" >&2; exit 2; }
done
for subnet in "$sub2_subnet" "$newapi_subnet"; do
  [[ "$subnet" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}/[0-9]{1,2}$ ]] || { echo "unsafe projection subnet: $subnet" >&2; exit 2; }
done

ensure_network() {
  local name=$1 subnet=$2
  if docker network inspect "$name" >/dev/null 2>&1; then
    local internal actual
    internal=$(docker network inspect --format '{{.Internal}}' "$name")
    actual=$(docker network inspect --format '{{range .IPAM.Config}}{{.Subnet}}{{end}}' "$name")
    [[ "$internal" == true && "$actual" == "$subnet" ]] || {
      echo "existing network ${name} is not the approved internal ${subnet}" >&2
      exit 1
    }
    return
  fi
  docker network create --internal --subnet "$subnet" \
    --label com.solov.invoice.projection=true "$name" >/dev/null
}

ensure_attachment() {
  local network=$1 container=$2 alias=$3
  docker container inspect "$container" >/dev/null
  if docker network inspect --format '{{range $id,$c := .Containers}}{{if eq $c.Name "'"$container"'"}}present{{end}}{{end}}' "$network" | grep -qx present; then
    local aliases
    aliases=$(docker container inspect --format '{{range (index .NetworkSettings.Networks "'"$network"'").Aliases}}{{println .}}{{end}}' "$container")
    grep -Fxq "$alias" <<< "$aliases" || {
        echo "${container} is attached to ${network} without required alias ${alias}; stop and review manually" >&2
        exit 1
      }
    return
  fi
  docker network connect --alias "$alias" "$network" "$container"
}

validate_members() {
  local network=$1 source=$2 database=$3 member
  while IFS= read -r member; do
    [[ -z "$member" ]] && continue
    case "$member" in
      "$database") ;;
      invoice-source-agents-prod-${source}-payments-[0-9]*|invoice-source-agents-prod-${source}-identities-[0-9]*|invoice-source-agents-prod-${source}-usage-[0-9]*|invoice-source-agents-prod-${source}-credits-[0-9]*|invoice-source-agents-prod-${source}-balances-[0-9]*|invoice-source-agents-prod-${source}-cutover-init-[0-9]*) ;;
      *) echo "unexpected container ${member} on ${network}" >&2; exit 1 ;;
    esac
  done < <(docker network inspect --format '{{range .Containers}}{{println .Name}}{{end}}' "$network")
}

ensure_network "$sub2_network" "$sub2_subnet"
ensure_network "$newapi_network" "$newapi_subnet"
ensure_attachment "$sub2_network" sub2api-mig-postgres sub2api-projection-db
ensure_attachment "$newapi_network" postgres newapi-projection-db
validate_members "$sub2_network" sub2api sub2api-mig-postgres
validate_members "$newapi_network" newapi postgres

printf 'projection networks ready: %s=%s, %s=%s\n' \
  "$sub2_network" "$sub2_subnet" "$newapi_network" "$newapi_subnet"
