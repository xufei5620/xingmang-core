#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
  cat <<'USAGE'
Usage:
  invoke-upstream-projection-maintenance.sh \
    --source newapi|sub2api \
    --mode audit|apply|install-source|install-economic|rollback-bridge|upgrade-preflight|cutover-quiescence-preflight \
    --container NAME --database NAME --user NAME \
    [--boundary-state detached|bridge-v4] \
    [--ack-source-agents-stopped --ack-backup-verified] \
    [--ack-upstream-app-stopped]

The SQL is streamed through stdin to psql inside the explicitly named running
database container. No DSN, password, or SQL file is copied into the container.
USAGE
}

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 2
}

source_name=''
mode='audit'
container_name=''
database_name=''
database_user=''
boundary_state='detached'
ack_agents='false'
ack_backup='false'
ack_upstream='false'

while (($#)); do
  case "$1" in
    --source)
      (($# >= 2)) || die '--source requires a value'
      source_name=$2
      shift 2
      ;;
    --mode)
      (($# >= 2)) || die '--mode requires a value'
      mode=$2
      shift 2
      ;;
    --container)
      (($# >= 2)) || die '--container requires a value'
      container_name=$2
      shift 2
      ;;
    --database)
      (($# >= 2)) || die '--database requires a value'
      database_name=$2
      shift 2
      ;;
    --user)
      (($# >= 2)) || die '--user requires a value'
      database_user=$2
      shift 2
      ;;
    --boundary-state)
      (($# >= 2)) || die '--boundary-state requires a value'
      boundary_state=$2
      shift 2
      ;;
    --ack-source-agents-stopped)
      ack_agents='true'
      shift
      ;;
    --ack-backup-verified)
      ack_backup='true'
      shift
      ;;
    --ack-upstream-app-stopped)
      ack_upstream='true'
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

[[ $source_name == 'newapi' || $source_name == 'sub2api' ]] ||
  die '--source must be newapi or sub2api'
[[ $mode == 'audit' || $mode == 'apply' || $mode == 'install-source' ||
   $mode == 'install-economic' || $mode == 'rollback-bridge' ||
   $mode == 'upgrade-preflight' || $mode == 'cutover-quiescence-preflight' ]] ||
  die 'unsupported --mode'
[[ $boundary_state == 'detached' || $boundary_state == 'bridge-v4' ]] ||
  die '--boundary-state must be detached or bridge-v4'

for required_name in container_name database_name database_user; do
  [[ -n ${!required_name} ]] || die "--${required_name//_/-} is required"
  [[ ${!required_name} =~ ^[A-Za-z0-9_.-]+$ ]] ||
    die "--${required_name//_/-} contains an unsafe character"
done

if [[ $mode == 'apply' || $mode == 'install-source' || $mode == 'install-economic' || $mode == 'rollback-bridge' ]]; then
  [[ $ack_agents == 'true' ]] || die "$mode requires --ack-source-agents-stopped"
  [[ $ack_backup == 'true' ]] || die "$mode requires --ack-backup-verified"
fi
if [[ $mode == 'cutover-quiescence-preflight' ]]; then
  [[ $ack_upstream == 'true' ]] || die 'cutover quiescence requires --ack-upstream-app-stopped'
fi

command -v docker >/dev/null 2>&1 || die 'docker is not installed'

script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repository_root=$(CDPATH='' cd -- "$script_dir/.." && pwd -P)
case "$mode" in
  upgrade-preflight) sql_path="$repository_root/contracts/upstream-upgrade-preflight.postgresql.sql" ;;
  cutover-quiescence-preflight) sql_path="$repository_root/contracts/source-cutover-quiescence-preflight.postgresql.sql" ;;
  install-source) sql_path="$repository_root/contracts/${source_name}-source-projection-grants.postgresql.sql" ;;
  install-economic) sql_path="$repository_root/contracts/${source_name}-economic-projection-grants.postgresql.sql" ;;
  rollback-bridge) sql_path="$repository_root/contracts/${source_name}-bridge-v4-rollback.postgresql.sql" ;;
  *) sql_path="$repository_root/contracts/projection-reconcile.postgresql.sql" ;;
esac
[[ -f $sql_path && ! -L $sql_path ]] || die "reviewed SQL contract is missing or linked: $sql_path"

running=$(docker inspect --type container --format '{{.State.Running}}' "$container_name" 2>/dev/null) ||
  die "container does not exist: $container_name"
[[ $running == 'true' ]] || die "container is not running: $container_name"

docker exec "$container_name" psql --version >/dev/null 2>&1 ||
  die "container does not provide psql: $container_name"

# Prove the explicit mapping before streaming the contract. The source-specific
# application-table fingerprint is independently checked again inside SQL.
actual_identity=$(
  docker exec \
    --env "PGAPPNAME=invoice-${source_name}-${mode}-identity" \
    "$container_name" \
    psql -X --no-password --tuples-only --no-align \
      --username "$database_user" --dbname "$database_name" \
      --command "SELECT current_database() || '|' || current_user"
) || die 'unable to verify explicit container/database/user mapping'
actual_identity=${actual_identity//$'\r'/}
actual_identity=${actual_identity//$'\n'/}
[[ $actual_identity == "${database_name}|${database_user}" ]] ||
  die 'container/database/user identity does not match the explicit mapping'

psql_arguments=(
  -X
  --no-password
  --set=ON_ERROR_STOP=1
  --set=VERBOSITY=terse
  --set="invoice_source=${source_name}"
)
if [[ $mode == 'upgrade-preflight' ]]; then
  psql_arguments+=(--set="boundary_state=${boundary_state}")
elif [[ $mode == 'audit' || $mode == 'apply' ]]; then
  apply_value='0'
  [[ $mode == 'apply' ]] && apply_value='1'
  psql_arguments+=(--set="reconcile_apply=${apply_value}")
fi
psql_arguments+=(--file=-)

printf 'Running %s for %s in explicitly verified container %s / database %s.\n' \
  "$mode" "$source_name" "$container_name" "$database_name"

docker exec --interactive \
  --env "PGAPPNAME=invoice-${source_name}-${mode}" \
  "$container_name" \
  psql "${psql_arguments[@]}" \
    --username "$database_user" --dbname "$database_name" \
  < "$sql_path"

printf '%s passed for %s.\n' "$mode" "$source_name"
