#!/usr/bin/env bash
# shadow-eval.sh -- XM-INV-SHADOW-EVAL release rehearsal.
#
# Restores a signed production database backup into a throwaway, isolated
# PostgreSQL container, brings its schema up to the candidate tools image's
# own migration set (the same invoice-migrate step
# deploy/roll-forward.sh always runs before anything else against real
# production -- the restored backup is normally still at the *running*
# release's migration set, one or more steps behind the candidate under
# rehearsal), runs the candidate's eligibility-projection worker against
# that restored-and-migrated copy until its queue drains (or --max-rounds
# is reached), and reports the delta in open freezes/errors versus the
# state immediately after restore-and-migrate. It never touches the running
# production Compose projects (invoice-system-prod_*, or the source-agent/idp
# projects): everything it starts is a bare, unnamed-project `docker
# network`/`docker run` pair on its own network, torn down before this
# script exits.
#
# Usage:
#   BACKUP_DIR=/root/invoice-system/backups \
#   BACKUP_ALLOWED_SIGNERS_FILE=/root/invoice-system/config/backup-allowed-signers \
#   AGE_IDENTITY_FILE=/offline/backup-age-identity.txt \
#     bash deploy/rehearsal/shadow-eval.sh --image-tag 0.1.0-rc77 \
#       [--backup invoice-20260903T011358Z] [--max-rounds 200] [--batch-limit 25]
#
# Exit codes:
#   0  the candidate is ready: no new open freeze-reason category and no
#      projection error versus the snapshot taken immediately after restore.
#   3  the candidate regressed: a new freeze-reason category or a
#      projection error appeared. The written report explains which.
#   2  usage error (bad flag, missing/invalid env, image not loaded).
#   1  any other execution failure (decrypt/restore/signature/Docker).
set -Eeuo pipefail
umask 077

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=deploy/backup/docker-cleanup-state.sh
source "$script_dir/../backup/docker-cleanup-state.sh"
# shellcheck source=deploy/rehearsal/shadow-eval-lib.sh
source "$script_dir/shadow-eval-lib.sh"
capacity_validator="$script_dir/../backup/validate-restore-postgres-capacity.sh"
test -f "$capacity_validator" && test ! -L "$capacity_validator" && test -s "$capacity_validator"

backup_name=""
image_tag=""
max_rounds=200
batch_limit=25
# XM-INV-SHADOW-EVAL-VACUOUS: off by default, so a plain run keeps exactly
# the meaning it has today. Any release that changes the evaluator, the
# projection, or a migration feeding either must pass it -- without it the
# candidate evaluator is never invoked against a single account, because a
# healthy production's job queue is empty and there is nothing to drain.
reproject_all=0
# XM-INV-CATCHUP-BURST-BACKPRESSURE fix 3: 0 keeps the single-pass evidence
# evaluation; N bounds it. The differential rehearsal runs one backup twice,
# once with each, and diffs the per-account quantities in the two reports.
evidence_batch_limit=0
# Rehearsal-only: clear the copy's evaluations first so the evidence pass has
# a pile to work through. Meaningless without --reproject-all; the tool
# refuses the combination and refuses a non-superuser session.
reevaluate_evidence=0
# Forward-only reproduction of the 2026-09-04 catch-up burst on a backup taken
# while the account was still excluded from finalization: --release-catchup
# replays the RC87 post-deploy repair on the copy, --finalization-window asks
# for the window a finalization pass would have requested. Both rehearsal-only;
# the tool refuses them outside a superuser session on a restored copy.
release_catchup=""
finalization_window=0
# Overall context budget handed to the tool's own --timeout; the default is
# its own (25m). A three-day catch-up replayed unbounded needs more.
tool_timeout=""
# --finalization-window-lag: derive the finalization window from watermarks
# this much earlier (a frontier window's carry-forward proof cannot close on a
# frozen copy; about 1h leaves a catch-up window whole but provable).
finalization_window_lag=""
# --finalization-window-provable: cut each window at the latest balances cycle
# ceiling by which every fact inside it had been seen (the proof closes there).
finalization_window_provable=0
tmpfs_size=${RESTORE_POSTGRES_TMPFS_SIZE:-16g}

usage() {
  cat >&2 <<'USAGE'
usage: shadow-eval.sh --image-tag <0.1.0-rcNN> [--backup <invoice-TIMESTAMP>]
                       [--max-rounds N] [--batch-limit N] [--reproject-all]
                       [--evidence-batch-limit N] [--reevaluate-evidence]
                       [--release-catchup <account-id[,account-id...]>] [--finalization-window]
                       [--timeout <Nm>]
                       [--finalization-window-lag <Nm|Nh>]
                       [--finalization-window-provable]
USAGE
}

while (( $# > 0 )); do
  case "$1" in
    --backup)
      (( $# >= 2 )) || { echo '--backup requires a value' >&2; exit 2; }
      backup_name=$2; shift 2 ;;
    --image-tag)
      (( $# >= 2 )) || { echo '--image-tag requires a value' >&2; exit 2; }
      image_tag=$2; shift 2 ;;
    --max-rounds)
      (( $# >= 2 )) || { echo '--max-rounds requires a value' >&2; exit 2; }
      max_rounds=$2; shift 2 ;;
    --batch-limit)
      (( $# >= 2 )) || { echo '--batch-limit requires a value' >&2; exit 2; }
      batch_limit=$2; shift 2 ;;
    --reproject-all)
      reproject_all=1; shift ;;
    --evidence-batch-limit)
      (( $# >= 2 )) || { echo '--evidence-batch-limit requires a value' >&2; exit 2; }
      evidence_batch_limit=$2; shift 2 ;;
    --reevaluate-evidence)
      reevaluate_evidence=1; shift ;;
    --release-catchup)
      (( $# >= 2 )) || { echo '--release-catchup requires a value' >&2; exit 2; }
      release_catchup=$2; shift 2 ;;
    --finalization-window)
      finalization_window=1; shift ;;
    --timeout)
      (( $# >= 2 )) || { echo '--timeout requires a value' >&2; exit 2; }
      tool_timeout=$2; shift 2 ;;
    --finalization-window-lag)
      (( $# >= 2 )) || { echo '--finalization-window-lag requires a value' >&2; exit 2; }
      finalization_window_lag=$2; shift 2 ;;
    --finalization-window-provable)
      finalization_window_provable=1; shift ;;
    -h|--help)
      usage; exit 0 ;;
    *)
      echo "unknown argument: $1" >&2; usage; exit 2 ;;
  esac
done

[[ "$image_tag" =~ ^0\.1\.0-rc[0-9]+$ ]] || { echo "--image-tag must look like 0.1.0-rcNN, got '$image_tag'" >&2; exit 2; }
[[ "$max_rounds" =~ ^[1-9][0-9]{0,3}$ ]] || { echo '--max-rounds must be an integer 1-9999' >&2; exit 2; }
[[ "$batch_limit" =~ ^[1-9][0-9]?$ ]] || { echo '--batch-limit must be an integer 1-99' >&2; exit 2; }
[[ "$evidence_batch_limit" =~ ^[0-9]{1,5}$ ]] || { echo '--evidence-batch-limit must be an integer 0-99999' >&2; exit 2; }
[[ -z "$release_catchup" || "$release_catchup" =~ ^[0-9a-f-]{36}(,[0-9a-f-]{36})*$ ]] || { echo '--release-catchup must be one or more comma-separated account uuids' >&2; exit 2; }
if (( finalization_window )) && ! (( reproject_all )); then echo '--finalization-window requires --reproject-all' >&2; exit 2; fi
[[ -z "$tool_timeout" || "$tool_timeout" =~ ^[1-9][0-9]{0,2}m$ ]] || { echo '--timeout must look like 90m' >&2; exit 2; }
[[ -z "$finalization_window_lag" || "$finalization_window_lag" =~ ^[1-9][0-9]{0,2}[mh]$ ]] || { echo '--finalization-window-lag must look like 1h or 30m' >&2; exit 2; }
if [[ -n "$finalization_window_lag" ]] && ! (( finalization_window )); then echo '--finalization-window-lag requires --finalization-window' >&2; exit 2; fi
if (( finalization_window_provable )) && ! (( finalization_window )); then echo '--finalization-window-provable requires --finalization-window' >&2; exit 2; fi
# An explicit --backup's shape is pure input validation and belongs with the
# other flag checks above -- before any environment or tool-availability
# check below -- so a typo'd backup name fails immediately regardless of
# what is or is not installed/configured on the host.
[[ -z "$backup_name" || "$backup_name" =~ ^invoice-[0-9]{8}T[0-9]{6}Z$ ]] || { echo "--backup must look like invoice-TIMESTAMP, got '$backup_name'" >&2; exit 2; }

: "${BACKUP_DIR:?set BACKUP_DIR to the signed backup directory}"
: "${BACKUP_ALLOWED_SIGNERS_FILE:?set the offline-reviewed OpenSSH allowed_signers file}"
: "${AGE_IDENTITY_FILE:?set AGE_IDENTITY_FILE to the offline restore identity}"
rehearsal_root=${REHEARSAL_ROOT:-/root/invoice-system/rehearsals}

for command in age docker sha256sum ssh-keygen stat grep awk comm sort mktemp; do command -v "$command" >/dev/null; done
test -d "$BACKUP_DIR"
test -f "$BACKUP_ALLOWED_SIGNERS_FILE" && test ! -L "$BACKUP_ALLOWED_SIGNERS_FILE" && test -s "$BACKUP_ALLOWED_SIGNERS_FILE"
(( $(stat -c '%s' "$BACKUP_ALLOWED_SIGNERS_FILE") <= 65536 ))
test -f "$AGE_IDENTITY_FILE" && test ! -L "$AGE_IDENTITY_FILE" && test -s "$AGE_IDENTITY_FILE"

tools_image="invoice-system-tools:$image_tag"
postgres_image="invoice-postgres:$image_tag"
docker image inspect "$tools_image" "$postgres_image" >/dev/null

# resolve_tools_container_ids prints "<uid> <gid>" for the numeric user the
# tools image's invoice-eligibility-shadow entrypoint actually runs as, via
# shadow_eval_parse_config_user (shadow-eval-lib.sh; statically tested
# there) when `Config.User` is already numeric -- backend/Dockerfile's tools
# stage currently pins this literally (`USER 10001:10001`), so this is the
# common case and starts no container just to answer the question -- or by
# asking the image itself, via `id`, running as its own configured default
# user, when `Config.User` is empty (root) or a name. The database-url
# secret file below must be readable by exactly this uid: this container
# runs `--read-only` as a non-root user, so a file merely readable by the
# host's root (this script's own user) is not readable inside it -- a real
# production run hit exactly this ("permission denied" reading
# /run/secrets/database-url) before this resolution step existed.
resolve_tools_container_ids() {
  local image=$1
  local config_user parsed uid gid
  config_user=$(docker image inspect --format '{{.Config.User}}' "$image")
  if parsed=$(shadow_eval_parse_config_user "$config_user"); then
    read -r uid gid <<<"$parsed"
  else
    uid=$(docker run --pull never --rm --entrypoint id "$image" -u) || return 1
    gid=$(docker run --pull never --rm --entrypoint id "$image" -g) || return 1
    [[ "$uid" =~ ^[0-9]+$ && "$gid" =~ ^[0-9]+$ ]] || return 1
  fi
  printf '%s %s\n' "$uid" "$gid"
}
read -r tools_uid tools_gid < <(resolve_tools_container_ids "$tools_image") || {
  echo "could not resolve the numeric user the tools image runs as" >&2
  exit 1
}
[[ "$tools_uid" != 0 && "$tools_gid" != 0 ]] || {
  echo "refusing to prepare a rehearsal secret for a tools image that runs as root" >&2
  exit 1
}

if [[ -z "$backup_name" ]]; then
  latest_signature=$(ls -t "$BACKUP_DIR"/invoice-*.sha256.sig 2>/dev/null | head -1 || true)
  [[ -n "$latest_signature" ]] || { echo "no signed backup found under $BACKUP_DIR" >&2; exit 2; }
  backup_name=$(basename "$latest_signature")
  backup_name=${backup_name%.sha256.sig}
  # The resolved name still gets the identical shape check the explicit
  # --backup case already passed above -- a signed-looking file that
  # somehow does not match the expected naming convention must not be used.
  [[ "$backup_name" =~ ^invoice-[0-9]{8}T[0-9]{6}Z$ ]] || { echo "resolved latest backup has an unexpected name: '$backup_name'" >&2; exit 1; }
fi

database_backup="$BACKUP_DIR/$backup_name.postgres.dump.age"
manifest_file="$BACKUP_DIR/$backup_name.sha256"
signature_file="$manifest_file.sig"
for file in "$database_backup" "$manifest_file" "$signature_file"; do
  test -f "$file" && test ! -L "$file" && test -s "$file"
done
(( $(stat -c '%s' "$manifest_file") <= 65536 ))
(( $(stat -c '%s' "$signature_file") <= 16384 ))

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

# Authenticity before anything else, exactly as restore-drill.sh does: verify
# the signed manifest before trusting any checksum line in it or decrypting
# any attacker-controlled ciphertext.
ssh-keygen -Y verify -f "$BACKUP_ALLOWED_SIGNERS_FILE" -I "$backup_signer_identity" \
  -n "$backup_signature_namespace" -s "$signature_file" <"$manifest_file"

# This rehearsal only needs the database component -- eligibility projection
# never touches the document volume or source-state archives -- so it checks
# only the database backup's line in the signed manifest, not every
# component restore-drill.sh's full backup-integrity check requires. It does
# not certify the backup as a whole; the standard restore drill (run
# separately, see docs/PRODUCTION-RUNBOOK.md section 11) still owns that.
database_backup_name=$(basename "$database_backup")
[[ "$database_backup_name" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$ ]] || { echo "unsafe backup component filename: $database_backup_name" >&2; exit 1; }
manifest_matches=$(grep -c -E "^[0-9a-f]{64}  $(printf '%s' "$database_backup_name" | sed 's/[.[\*^$]/\\&/g')\$" "$manifest_file" || true)
[[ "$manifest_matches" == 1 ]] || { echo "signed manifest has $manifest_matches (want exactly 1) entries for $database_backup_name" >&2; exit 1; }
manifest_line=$(grep -E "^[0-9a-f]{64}  $(printf '%s' "$database_backup_name" | sed 's/[.[\*^$]/\\&/g')\$" "$manifest_file")
(cd "$BACKUP_DIR" && printf '%s\n' "$manifest_line" | sha256sum -c -) >/dev/null

host_available_bytes=$(awk '/^MemAvailable:/ { printf "%.0f\n",$2*1024; found=1 } END { if (!found) exit 1 }' /proc/meminfo)
docker_total_bytes=$(docker info --format '{{.MemTotal}}')
restore_postgres_tmpfs_bytes=$(bash "$capacity_validator" "$tmpfs_size" "$host_available_bytes" "$docker_total_bytes")
[[ "$restore_postgres_tmpfs_bytes" =~ ^[1-9][0-9]*$ ]]

stamp=$(date -u +%Y%m%dT%H%M%SZ)-$$
network="invoice-shadow-$stamp"
container="invoice-shadow-pg-$stamp"
rehearsal_dir="$rehearsal_root/$stamp"
work_dir=$(mktemp -d)
work_mounted=false
network_created=false
started=false
report_json="$rehearsal_dir/shadow-eval.json"
summary_txt="$rehearsal_dir/shadow-eval-summary.txt"
tool_log="$rehearsal_dir/eligibility-shadow.log"
migrate_log="$rehearsal_dir/invoice-migrate.log"

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
  if [[ -f "$work_dir/database-url" ]]; then
    shred -u "$work_dir/database-url" 2>/dev/null || rm -f "$work_dir/database-url" || cleanup_failed=true
  fi
  if $work_mounted; then
    umount "$work_dir" 2>/dev/null || cleanup_failed=true
  fi
  rm -rf -- "$work_dir" || cleanup_failed=true
  [[ -e "$work_dir" ]] && cleanup_failed=true
  if $cleanup_failed; then
    echo 'CRITICAL: shadow-eval cleanup left a container, network, or temporary key material behind' >&2
    exit 1
  fi
  exit "$original_status"
}
trap cleanup EXIT

# The work directory holds only a throwaway restore-only database credential
# (a fixed, non-production password, matching restore-drill.sh's
# "restore-drill-only" convention) -- never the decrypted database dump
# itself, which is streamed directly from `age` into `pg_restore` over a
# pipe and never touches disk. It is still mounted as its own small tmpfs
# and shredded before removal, matching the task's key-material-handling
# requirement.
if mount -t tmpfs -o size=16m,mode=0700,nosuid,nodev,noexec tmpfs "$work_dir" 2>/dev/null; then
  work_mounted=true
else
  echo "warning: could not mount a dedicated tmpfs for the rehearsal work directory; continuing on $work_dir" >&2
fi
# Owned by root, group-owned by the tools container's own gid, traversable
# by that group alone (0710: rwx for root, --x for the group, nothing for
# anyone else) -- not the file's own readability (that is the 0400 chown
# below, and is what the container process actually needs), but defense in
# depth against any other host-side user or process browsing in here while
# the rehearsal runs.
chown 0:"$tools_gid" "$work_dir"
chmod 0710 "$work_dir"

mkdir -p -- "$rehearsal_dir"

if docker network inspect "$network" >/dev/null 2>&1 || docker container inspect "$container" >/dev/null 2>&1; then
  echo 'shadow-eval Docker resource name collision' >&2
  exit 1
fi
network_created=true
docker network create "$network" >/dev/null

started=true
docker run --pull never --detach --rm --name "$container" --network "$network" --network-alias postgres \
  --env POSTGRES_PASSWORD=restore-drill-only \
  --env POSTGRES_DB=invoice \
  --tmpfs "/var/lib/postgresql:rw,nosuid,nodev,size=$tmpfs_size" \
  "$postgres_image" >/dev/null

deadline=$((SECONDS+60))
until docker exec "$container" pg_isready -U postgres -d invoice >/dev/null 2>&1; do
  (( SECONDS < deadline )) || { echo 'shadow-eval PostgreSQL did not become ready' >&2; exit 1; }
  sleep 1
done
# See restore-drill.sh's identical comment: a single pg_isready can hit the
# official image's transient bootstrap postmaster before its restart into
# the final server. Require the init-complete marker plus three consecutive
# final-server readiness checks.
until docker logs "$container" 2>&1 | grep -Fq 'PostgreSQL init process complete; ready for start up.'; do
  (( SECONDS < deadline )) || { echo 'shadow-eval PostgreSQL initialization did not complete' >&2; exit 1; }
  sleep 1
done
for _ in 1 2 3; do
  docker exec "$container" pg_isready -U postgres -d invoice >/dev/null 2>&1 || {
    echo 'shadow-eval PostgreSQL final server did not remain ready' >&2
    exit 1
  }
  sleep 1
done

age --decrypt -i "$AGE_IDENTITY_FILE" "$database_backup" \
  | docker exec -i "$container" pg_restore -U postgres -d invoice --no-owner --no-acl --exit-on-error

printf '%s\n' 'postgres://postgres:restore-drill-only@postgres:5432/invoice?sslmode=disable' >"$work_dir/database-url"
# Owned by the tools container's own numeric user (resolved above) and
# readable by no one else -- the container runs as this uid, non-root,
# --read-only, so the file must be readable by exactly this uid or nothing
# else in the container can open it. Shared by both the invoice-migrate
# step below and the invoice-eligibility-shadow run after it.
chown "$tools_uid:$tools_gid" "$work_dir/database-url"
chmod 0400 "$work_dir/database-url"

# write_tooling_failure_marker <reason> <exit_code> <log_file>
# Overwrites report_json with an explicit, self-describing failure marker
# instead of leaving an empty or partial file behind -- shared by both
# possible tooling failures below (the migrate step and the
# eligibility-shadow step). shadow_eval_report_is_valid (shadow-eval-lib.sh)
# recognizes this exact shape and refuses to compute a ready/not_ready
# verdict from it, so a tooling/execution failure can never be silently
# folded into either.
write_tooling_failure_marker() {
  local reason=$1 exit_code=$2 log_file=$3
  printf '{\n  "tooling_failure": true,\n  "reason": "%s",\n  "tool_exit_code": %d,\n  "log_file": "%s"\n}\n' \
    "$reason" "$exit_code" "$log_file" >"$report_json"
}

# The restored backup is at whatever migration set the running production
# release left it (e.g. through 0019), while the candidate tools image's
# invoice-eligibility-shadow requires the candidate's own set (e.g. through
# 0020) -- exactly the mismatch a real production run hit
# ("required migration 0020_eligibility_auto_reconcile.sql is not
# applied"). deploy/roll-forward.sh always runs invoice-migrate before
# anything else against real production; this rehearsal must do the same
# against the restored copy, so the verdict below is explicitly "candidate
# schema + candidate evaluator against production data", not merely
# "candidate evaluator against whatever schema the backup happened to be
# taken at".
schema_migrations_before=$(docker exec "$container" psql -X -v ON_ERROR_STOP=1 -U postgres -d invoice -At \
  -c "SELECT name FROM schema_migrations ORDER BY name")

mapfile -t migrate_docker_args < <(shadow_eval_migrate_docker_args "$network" "$tools_image" "$work_dir/database-url")
set +e
docker run "${migrate_docker_args[@]}" >"$migrate_log" 2>&1
migrate_exit=$?
set -e
if (( migrate_exit != 0 )); then
  write_tooling_failure_marker "candidate invoice-migrate failed" "$migrate_exit" "$migrate_log"
  echo "candidate invoice-migrate failed (exit $migrate_exit); see $migrate_log" >&2
  cat "$migrate_log" >&2 || true
  exit 1
fi

schema_migrations_after=$(docker exec "$container" psql -X -v ON_ERROR_STOP=1 -U postgres -d invoice -At \
  -c "SELECT name FROM schema_migrations ORDER BY name")
migrations_applied_csv=$(comm -13 <(printf '%s\n' "$schema_migrations_before") <(printf '%s\n' "$schema_migrations_after") | paste -sd ',' -)

# An explicit array, so the flag is either present as one whole argument
# or absent entirely -- never an empty string, which the tool would reject
# as a positional argument (it accepts none).
reproject_all_args=()
if (( reproject_all )); then reproject_all_args=(--reproject-all); fi
evidence_batch_limit_args=(--evidence-batch-limit "$evidence_batch_limit")
reevaluate_evidence_args=()
if (( reevaluate_evidence )); then reevaluate_evidence_args=(--reevaluate-evidence); fi
release_catchup_args=()
if [[ -n "$release_catchup" ]]; then release_catchup_args=(--release-catchup "$release_catchup"); fi
finalization_window_args=()
if (( finalization_window )); then finalization_window_args=(--finalization-window); fi
timeout_args=()
if [[ -n "$tool_timeout" ]]; then timeout_args=(--timeout "$tool_timeout"); fi
finalization_window_lag_args=()
if [[ -n "$finalization_window_lag" ]]; then finalization_window_lag_args=(--finalization-window-lag "$finalization_window_lag"); fi
finalization_window_provable_args=()
if (( finalization_window_provable )); then finalization_window_provable_args=(--finalization-window-provable); fi

set +e
docker run --pull never --rm --network "$network" --read-only \
  --cap-drop ALL --security-opt no-new-privileges:true \
  --mount "type=bind,src=$work_dir/database-url,dst=/run/secrets/database-url,readonly" \
  --entrypoint /usr/local/bin/invoice-eligibility-shadow "$tools_image" \
  --database-url-file /run/secrets/database-url \
  --migrations-dir /app/migrations \
  --max-rounds "$max_rounds" --batch-limit "$batch_limit" \
  --backup-label "$backup_name" --candidate-image-tag "$image_tag" \
  --migrations-applied "$migrations_applied_csv" \
  "${reproject_all_args[@]}" "${evidence_batch_limit_args[@]}" "${reevaluate_evidence_args[@]}" \
  "${release_catchup_args[@]}" "${finalization_window_args[@]}" "${finalization_window_lag_args[@]}" "${finalization_window_provable_args[@]}" "${timeout_args[@]}" \
  >"$report_json" 2>"$tool_log"
tool_exit=$?
set -e

if [[ ! -s "$report_json" ]]; then
  # The tools container never got far enough to print anything (a real
  # production run hit exactly this: a "read database credential:
  # permission denied" error before the permission fix above). `>"$report_json"`
  # already created an empty file the instant the docker run command
  # started, regardless of what it wrote -- leaving that empty file behind
  # is a confusing, silently-misleading artifact (it looks like *some*
  # report exists). write_tooling_failure_marker overwrites it with an
  # explicit, valid, self-describing failure marker instead.
  write_tooling_failure_marker "eligibility-shadow produced no report" "$tool_exit" "$tool_log"
  echo "eligibility-shadow produced no report (exit $tool_exit); see $tool_log" >&2
  cat "$tool_log" >&2 || true
  exit 1
fi
# A lightweight structural sanity check (this repo avoids a jq dependency
# here, see shadow-eval-lib.sh's header comment) -- the tool always prints
# exactly one JSON object via json.MarshalIndent, so the first and last
# non-blank characters must be the object braces.
first_char=$(grep -m1 -v '^[[:space:]]*$' "$report_json" | cut -c1)
last_char=$(tail -n 5 "$report_json" | tr -d '[:space:]' | tail -c1)
[[ "$first_char" == '{' && "$last_char" == '}' ]] || {
  echo "eligibility-shadow report does not look like a JSON object: $report_json" >&2
  exit 1
}
# Belt and suspenders on top of the emptiness check above: anything that is
# valid JSON but still not a genuine report (missing "verdict", or somehow
# itself carrying a tooling_failure marker) must also never reach the
# verdict logic below.
shadow_eval_report_is_valid "$report_json" || {
  echo "eligibility-shadow report is not a valid report (execution failure, not a verdict): $report_json" >&2
  cat "$tool_log" >&2 || true
  exit 1
}

shadow_eval_human_summary "$report_json" >"$summary_txt"
cat "$summary_txt"

set +e
verdict=$(shadow_eval_verdict_exit_code "$report_json")
recomputed_exit=$?
set -e

# Defense in depth (matching this repo's usual double-checked gates, e.g.
# scripts/verify.ps1): the two verdicts are computed by independent
# implementations from the same published JSON. They should always agree;
# if they do not, treat it as a rehearsal-tooling bug and fail loudly rather
# than silently trusting either one.
tool_verdict=$(_shadow_eval_scalar "$report_json" verdict)
if [[ "$verdict" != "$tool_verdict" ]]; then
  echo "shadow-eval: bash-recomputed verdict ($verdict) disagrees with the tool's own verdict ($tool_verdict) -- treating this as a rehearsal-tooling failure" >&2
  exit 1
fi

echo "shadow-eval: report written to $report_json"
echo "shadow-eval: summary written to $summary_txt"
echo "shadow-eval: verdict=$verdict (independently recomputed; tool process exit was $tool_exit)"
exit "$recomputed_exit"
