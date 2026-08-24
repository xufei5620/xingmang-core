#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
  cat <<'USAGE'
Usage:
  preserve-source-reader-roles.sh --mode export|restore \
    --source newapi|sub2api --container NAME --database NAME --user NAME \
    --file /absolute/root-only/path.sql

  preserve-source-reader-roles.sh --mode validate \
    --source newapi|sub2api --file /absolute/root-only/path.sql

Export writes only the six existing reader role definitions and their SCRAM
verifiers to a new mode-0600 file. Restore streams that file to psql inside the
explicit database container. The file is sensitive and must be encrypted or
removed immediately after the bridge is installed and all five DSNs pass.
USAGE
}

die() { printf 'ERROR: %s\n' "$*" >&2; exit 2; }

validate_private_directory() {
  local directory=$1 resolved owner mode
  [[ -d $directory && ! -L $directory ]] || die 'role-verifier parent directory is missing or linked'
  resolved=$(realpath -e -- "$directory") || die 'cannot resolve role-verifier parent directory'
  [[ $resolved == "$directory" ]] || die 'role-verifier parent path must be canonical and contain no symlink'
  owner=$(stat -c '%u' "$directory") || die 'cannot inspect role-verifier parent owner'
  mode=$(stat -c '%a' "$directory") || die 'cannot inspect role-verifier parent mode'
  [[ $owner == "$(id -u)" ]] || die 'role-verifier parent must be owned by the current operator'
  (( (8#$mode & 022) == 0 )) || die 'role-verifier parent must not be group/world writable'
}

declare -A restored_verifiers=()
compatibility_role=''

load_and_validate_envelope() {
  local source=$1 file=$2 file_mode current_uid file_uid parent
  local -a role_lines
  local line role_name login_state connection_limit verifier extra

  [[ -f $file && ! -L $file ]] || die 'role-verifier file is missing, non-regular or linked'
  file_mode=$(stat -c '%a' "$file") || die 'cannot inspect role-verifier file mode'
  [[ $file_mode == 600 || $file_mode == 400 ]] || die 'role-verifier file mode must be 0600 or 0400'
  current_uid=$(id -u)
  file_uid=$(stat -c '%u' "$file") || die 'cannot inspect role-verifier file owner'
  [[ $file_uid == "$current_uid" ]] || die 'role-verifier file must be owned by the current operator'
  parent=$(dirname -- "$file")
  validate_private_directory "$parent"

  mapfile -t role_lines <"$file"
  [[ ${#role_lines[@]} -eq 7 ]] || die 'role-verifier envelope must contain exactly seven lines'
  [[ ${role_lines[0]} == "invoice-reader-role-verifiers-v1|$source" ]] ||
    die 'role-verifier envelope header/source mismatch'
  restored_verifiers=()
  compatibility_role="invoice_${source}_payments_reader"
  for line in "${role_lines[@]:1}"; do
    IFS='|' read -r role_name login_state connection_limit verifier extra <<<"$line"
    [[ -z ${extra:-} && -n $role_name && -z ${restored_verifiers[$role_name]+present} ]] ||
      die 'role-verifier envelope contains a duplicate or malformed field'
    case "$role_name" in
      "invoice_${source}_balances_reader"|"invoice_${source}_credits_reader"|\
      "invoice_${source}_identities_reader"|"invoice_${source}_payments_v3_reader"|\
      "invoice_${source}_usage_reader")
        [[ $login_state == LOGIN && $connection_limit == 2 ]] || die 'active reader role attributes are invalid'
        [[ $verifier =~ ^SCRAM-SHA-256\$[1-9][0-9]*:[A-Za-z0-9+/=]+\$[A-Za-z0-9+/=]+:[A-Za-z0-9+/=]+$ ]] ||
          die 'active reader SCRAM verifier shape is invalid'
        ;;
      "$compatibility_role")
        [[ $login_state == NOLOGIN && $connection_limit == 0 && $verifier == - ]] ||
          die 'compatibility reader role attributes are invalid'
        ;;
      *) die 'role-verifier envelope contains an unapproved role' ;;
    esac
    restored_verifiers[$role_name]=$verifier
  done
  [[ ${#restored_verifiers[@]} -eq 6 ]] || die 'role-verifier envelope role set is incomplete'
}

mode=''
source_name=''
container_name=''
database_name=''
database_user=''
role_file=''

while (($#)); do
  case "$1" in
    --mode) (($# >= 2)) || die '--mode requires a value'; mode=$2; shift 2 ;;
    --source) (($# >= 2)) || die '--source requires a value'; source_name=$2; shift 2 ;;
    --container) (($# >= 2)) || die '--container requires a value'; container_name=$2; shift 2 ;;
    --database) (($# >= 2)) || die '--database requires a value'; database_name=$2; shift 2 ;;
    --user) (($# >= 2)) || die '--user requires a value'; database_user=$2; shift 2 ;;
    --file) (($# >= 2)) || die '--file requires a value'; role_file=$2; shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ $mode == export || $mode == restore || $mode == validate ]] || die '--mode must be export, restore or validate'
[[ $source_name == newapi || $source_name == sub2api ]] || die '--source must be newapi or sub2api'
[[ $role_file == /* && $role_file != *$'\n'* && $role_file != *$'\r'* ]] ||
  die '--file must be an absolute single-line path'
if [[ $mode == validate ]]; then
  load_and_validate_envelope "$source_name" "$role_file"
  printf 'Role-verifier envelope is valid for %s.\n' "$source_name"
  exit 0
fi
for required_name in container_name database_name database_user; do
  [[ -n ${!required_name} && ${!required_name} =~ ^[A-Za-z0-9_.-]+$ ]] ||
    die "--${required_name//_/-} is missing or unsafe"
done
command -v docker >/dev/null 2>&1 || die 'docker is not installed'

running=$(docker inspect --type container --format '{{.State.Running}}' "$container_name" 2>/dev/null) ||
  die "container does not exist: $container_name"
[[ $running == true ]] || die "container is not running: $container_name"
docker exec "$container_name" psql --version >/dev/null 2>&1 || die 'database container has no psql'

executor_ok=$(
  docker exec "$container_name" psql -X --no-password -Atq \
    --username "$database_user" --dbname "$database_name" \
    --command "SELECT r.rolsuper AND r.oid=d.datdba FROM pg_roles r JOIN pg_database d ON d.datname=current_database() WHERE r.rolname=current_user"
) || die 'cannot verify database executor'
executor_ok=${executor_ok//$'\r'/}
executor_ok=${executor_ok//$'\n'/}
[[ $executor_ok == t ]] || die 'operation requires the cluster superuser that owns the current database'

if [[ $mode == export ]]; then
  [[ ! -e $role_file && ! -L $role_file ]] || die 'export file already exists or is linked'
  parent=$(dirname -- "$role_file")
  validate_private_directory "$parent"
  umask 077
  temporary=$(mktemp --tmpdir="$parent" '.invoice-reader-roles.XXXXXXXX')
  cleanup() { [[ -n ${temporary:-} && -e $temporary ]] && rm -f -- "$temporary"; }
  trap cleanup EXIT HUP INT TERM

  docker exec --interactive "$container_name" psql -X --no-password -Atq \
    --set=ON_ERROR_STOP=1 --set="invoice_source=$source_name" \
    --username "$database_user" --dbname "$database_name" >"$temporary" <<'SQL'
\set ON_ERROR_STOP on
\o /dev/null
SELECT set_config('invoice.preserve.source', :'invoice_source', false);
DO $audit$
DECLARE source_name text := current_setting('invoice.preserve.source');
prefix text := 'invoice_'||source_name||'_';
expected text[] := ARRAY[
  prefix||'payments_reader',prefix||'identities_reader',prefix||'payments_v3_reader',
  prefix||'usage_reader',prefix||'credits_reader',prefix||'balances_reader'];
BEGIN
  IF (SELECT count(*) FROM pg_authid WHERE rolname=ANY(expected))<>6
     OR EXISTS (
       SELECT 1 FROM pg_authid WHERE rolname=ANY(expected) AND
         (rolsuper OR rolcreatedb OR rolcreaterole OR rolinherit OR rolreplication OR rolbypassrls)
     ) OR EXISTS (
       SELECT 1 FROM pg_authid WHERE rolname=prefix||'payments_reader'
         AND (rolcanlogin OR rolconnlimit<>0 OR rolpassword IS NOT NULL)
     ) OR EXISTS (
       SELECT 1 FROM pg_authid WHERE rolname=ANY(expected[2:6]) AND
         (NOT rolcanlogin OR rolconnlimit<>2 OR rolpassword IS NULL OR rolpassword NOT LIKE 'SCRAM-SHA-256$%')
     ) OR EXISTS (
       SELECT 1 FROM pg_auth_members membership
       JOIN pg_roles role_row ON role_row.oid=membership.member OR role_row.oid=membership.roleid
       WHERE role_row.rolname=ANY(expected)
     ) THEN
    RAISE EXCEPTION 'reader role inventory is not safe to preserve';
  END IF;
END $audit$;
\o
SELECT 'invoice-reader-role-verifiers-v1|'||current_setting('invoice.preserve.source');
SELECT rolname||'|'||CASE WHEN rolcanlogin THEN 'LOGIN|2|'||rolpassword ELSE 'NOLOGIN|0|-' END
FROM pg_authid
WHERE rolname LIKE 'invoice_'||current_setting('invoice.preserve.source')||'\_%' ESCAPE '\'
  AND rolname IN (
    'invoice_'||current_setting('invoice.preserve.source')||'_payments_reader',
    'invoice_'||current_setting('invoice.preserve.source')||'_identities_reader',
    'invoice_'||current_setting('invoice.preserve.source')||'_payments_v3_reader',
    'invoice_'||current_setting('invoice.preserve.source')||'_usage_reader',
    'invoice_'||current_setting('invoice.preserve.source')||'_credits_reader',
    'invoice_'||current_setting('invoice.preserve.source')||'_balances_reader')
ORDER BY rolname;
SQL
  chmod 0600 "$temporary"
  lines=$(wc -l <"$temporary")
  [[ $lines -eq 7 ]] || die "preserved role file has unexpected line count: $lines"
  mv -- "$temporary" "$role_file"
  temporary=''
  trap - EXIT HUP INT TERM
  printf 'Preserved %s reader role verifiers in a new mode-0600 file.\n' "$source_name"
  exit 0
fi

# Read once, then validate and reconstruct SQL from allowlisted fields. The
# sensitive file is never passed to psql, so psql metacommands or arbitrary SQL
# cannot cross this boundary even if the file was tampered with.
load_and_validate_envelope "$source_name" "$role_file"

role_count=$(
  docker exec "$container_name" psql -X --no-password -Atq \
    --username "$database_user" --dbname "$database_name" \
    --command "SELECT count(*) FROM pg_roles WHERE rolname LIKE 'invoice_${source_name}\\_%' ESCAPE '\\'"
) || die 'cannot inspect pre-restore role inventory'
role_count=${role_count//$'\r'/}
role_count=${role_count//$'\n'/}
[[ $role_count == 0 ]] || die "restore requires zero existing invoice_${source_name}_ roles"

restore_sql='BEGIN;'
for role_name in \
  "invoice_${source_name}_balances_reader" "invoice_${source_name}_credits_reader" \
  "invoice_${source_name}_identities_reader" "invoice_${source_name}_payments_reader" \
  "invoice_${source_name}_payments_v3_reader" "invoice_${source_name}_usage_reader"; do
  verifier=${restored_verifiers[$role_name]}
  if [[ $role_name == "$compatibility_role" ]]; then
    restore_sql+=$'\n'"CREATE ROLE $role_name NOLOGIN CONNECTION LIMIT 0 NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS;"
  else
    restore_sql+=$'\n'"CREATE ROLE $role_name LOGIN PASSWORD '$verifier' CONNECTION LIMIT 2 NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS;"
  fi
done
restore_sql+=$'\nCOMMIT;\n'

printf '%s' "$restore_sql" | docker exec --interactive "$container_name" psql -X --no-password \
  --set=ON_ERROR_STOP=1 --username "$database_user" --dbname "$database_name" \
  --file=-
unset restore_sql verifier restored_verifiers
printf 'Restored %s reader roles. Apply Bridge V4 and run all five check-db canaries now.\n' "$source_name"
