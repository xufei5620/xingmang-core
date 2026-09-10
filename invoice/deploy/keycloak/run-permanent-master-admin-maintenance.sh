#!/usr/bin/env bash
set -Eeuo pipefail

# Freeze Keycloak master-realm administration to loopback while the signed,
# create-only permanent-administrator operator runs. The real allowlist is
# encrypted and signed before the freeze, then restored atomically on every
# exit path. Approved-source checks are interactive because the production
# administrator browser is the only available independent non-loopback canary.

umask 077

readonly WRAPPER_NAME='run-permanent-master-admin-maintenance'
readonly ALLOWLIST='/www/server/panel/vhost/nginx/access/auth-admin.solov.cc.allow.conf'
readonly NGINX='/www/server/nginx/sbin/nginx'
readonly GLOBAL_LOCK='/run/lock/solov-keycloak-master-admin-maintenance.lock'
readonly RECORD_ROOT_EXPECTED='/root/invoice-system/keycloak-backups'
readonly RELEASE_ALLOWED_SIGNERS_FILE='/root/invoice-system/trust/release-tree-allowed-signers'
readonly RELEASE_NAMESPACE='solov-invoice-release-v1'
readonly RELEASE_SIGNER='invoice-release@solov.cc'
readonly BACKUP_NAMESPACE='solov-invoice-backup-v1'
readonly BACKUP_SIGNER='invoice-backup'
readonly GATE_TIMEOUT_SECONDS='300'
readonly OPERATOR_TIMEOUT_SECONDS='1800'
readonly OPERATOR_SUCCESS='{"status":"ok","operation":"permanent-master-admin-invitation","backup_restore":"passed","invitation":"dispatched","master_smtp_restored":true}'
readonly FIXED_SUCCESS='{"status":"ok","operation":"permanent-master-admin-maintenance","freeze":"verified","invitation":"dispatched","allowlist_restore":"verified","approved_source_probe":"passed"}'

die() {
  printf '%s: %s\n' "$WRAPPER_NAME" "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command is unavailable: $1"
}

(( EUID == 0 )) || die 'must run as root'
(( $# == 0 )) || die 'this wrapper accepts no arguments'

for command_name in age awk chmod chown cmp cp date dirname env findmnt flock grep mktemp mv python3 realpath rm sha256sum sleep ssh-keygen stat sync tail tee timeout tr; do
  require_command "$command_name"
done

check_regular_file() {
  local path="$1" label="$2"
  [[ -f "$path" && ! -L "$path" && -s "$path" ]] || die "$label must be a non-empty regular non-symlink file"
  (( $(stat -c '%s' -- "$path") <= 262144 )) || die "$label is unexpectedly large"
}

check_root_0600() {
  local path="$1" label="$2"
  check_regular_file "$path" "$label"
  [[ "$(stat -c '%u:%g:%a' -- "$path")" == '0:0:600' ]] || die "$label must be root-owned mode 0600"
}

check_root_private() {
  local path="$1" label="$2"
  check_regular_file "$path" "$label"
  [[ "$(stat -c '%u:%a' -- "$path")" == '0:400' || "$(stat -c '%u:%a' -- "$path")" == '0:600' ]] ||
    die "$label must be root-owned mode 0400 or 0600"
}

check_root_executable() {
  local path="$1" label="$2" mode
  check_regular_file "$path" "$label"
  [[ -x "$path" && "$(stat -c '%u:%g' -- "$path")" == '0:0' ]] || die "$label ownership or execute permission is unsafe"
  mode="$(stat -c '%a' -- "$path")"
  [[ "$mode" =~ ^[0-7]?[0-7][0145][0145]$ ]] || die "$label must not be group-writable or world-writable"
}

allowlist_is_safe() {
  local path="$1"
  [[ -f "$path" && ! -L "$path" && -s "$path" ]] || return 1
  (( $(stat -c '%s' -- "$path") <= 262144 )) || return 1
  [[ "$(stat -c '%u:%g:%a' -- "$path")" == '0:0:600' ]] || return 1
  python3 - "$path" <<'PY' >/dev/null
import ipaddress
import pathlib
import re
import sys

try:
    lines = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8").splitlines()
except (OSError, UnicodeError) as exc:
    raise SystemExit("invalid") from exc

rules = []
for raw in lines:
    line = raw.split("#", 1)[0].strip()
    if line:
        rules.append(line)

if not rules or rules[-1] != "deny all;" or rules.count("deny all;") != 1:
    raise SystemExit("invalid")

seen = set()
non_loopback = False
for rule in rules[:-1]:
    match = re.fullmatch(r"allow\s+([^;\s]+);", rule)
    if not match:
        raise SystemExit("invalid")
    if "/" not in match.group(1):
        raise SystemExit("invalid")
    try:
        network = ipaddress.ip_network(match.group(1), strict=True)
    except ValueError as exc:
        raise SystemExit("invalid") from exc
    if network.prefixlen != network.max_prefixlen:
        raise SystemExit("invalid")
    canonical = str(network)
    if match.group(1) != canonical:
        raise SystemExit("invalid")
    if canonical in seen:
        raise SystemExit("invalid")
    seen.add(canonical)
    if not network.network_address.is_loopback:
        non_loopback = True

if not seen or not non_loopback:
    raise SystemExit("invalid")
PY
}

readonly WRAPPER_PATH="$(realpath -e -- "$0")"
readonly PROJECT_ROOT="$(realpath -e -- "$(dirname -- "$WRAPPER_PATH")/../..")"
case "$PROJECT_ROOT" in
  */source/invoice) readonly RELEASE_ROOT="$(realpath -e -- "$PROJECT_ROOT/../..")" SOURCE_RELATIVE_ROOT=source/invoice ;;
  */source) readonly RELEASE_ROOT="$(realpath -e -- "$PROJECT_ROOT/..")" SOURCE_RELATIVE_ROOT=source ;;
  *) die 'wrapper must run from an installed immutable release tree' ;;
esac
[[ "$PROJECT_ROOT" == "$RELEASE_ROOT/$SOURCE_RELATIVE_ROOT" ]] || die 'wrapper must run from an installed immutable release tree'
[[ ! -d "$RELEASE_ROOT/source/deploy" || ! -d "$RELEASE_ROOT/source/invoice/deploy" ]] || die 'ambiguous installed invoice deploy roots'
readonly OPERATOR="$PROJECT_ROOT/deploy/keycloak/invite-permanent-master-admin.sh"
readonly RELEASE_MANIFEST="$RELEASE_ROOT/RELEASE-TREE.sha256"
readonly RELEASE_SIGNATURE="$RELEASE_ROOT/RELEASE-TREE.sha256.sig"

check_root_executable "$WRAPPER_PATH" 'maintenance wrapper'
check_root_executable "$OPERATOR" 'permanent administrator operator'
check_root_executable "$NGINX" 'fixed Nginx binary'
[[ "$(realpath -e -- "$OPERATOR")" == "$OPERATOR" ]] || die 'operator path is not canonical'
[[ "$(realpath -e -- "$NGINX")" == "$NGINX" ]] || die 'fixed Nginx binary path is not canonical'
check_root_0600 "$RELEASE_MANIFEST" 'release-tree manifest'
check_root_0600 "$RELEASE_SIGNATURE" 'release-tree signature'
check_root_0600 "$RELEASE_ALLOWED_SIGNERS_FILE" 'release allowed_signers file'

ssh-keygen -Y verify -f "$RELEASE_ALLOWED_SIGNERS_FILE" -I "$RELEASE_SIGNER" -n "$RELEASE_NAMESPACE" \
  -s "$RELEASE_SIGNATURE" <"$RELEASE_MANIFEST" >/dev/null 2>&1 || die 'installed release-tree signature verification failed'

verify_release_file() {
  local relative="$1" actual="$2" line expected observed
  line="$(grep -E "^[0-9a-f]{64}  ${relative//./\\.}$" "$RELEASE_MANIFEST" || true)"
  [[ "$(printf '%s\n' "$line" | grep -Ec "^[0-9a-f]{64}  ${relative//./\\.}$")" == 1 ]] || return 1
  expected="${line%%  *}"
  observed="$(sha256sum -- "$actual" | awk '{print $1}')"
  [[ "$observed" == "$expected" ]]
}

verify_release_file "$SOURCE_RELATIVE_ROOT/deploy/keycloak/invite-permanent-master-admin.sh" "$OPERATOR" ||
  die 'signed release manifest does not bind the exact operator'
verify_release_file "$SOURCE_RELATIVE_ROOT/deploy/keycloak/run-permanent-master-admin-maintenance.sh" "$WRAPPER_PATH" ||
  die 'signed release manifest does not bind the exact maintenance wrapper'

allowlist_is_safe "$ALLOWLIST" || die 'administrator allowlist is unsafe or contains invalid rules'
[[ "$(realpath -e -- "$ALLOWLIST")" == "$ALLOWLIST" ]] || die 'administrator allowlist path is not canonical'

: "${AGE_RECIPIENT_FILE:?set AGE_RECIPIENT_FILE to the approved age recipients file}"
: "${AGE_IDENTITY_FILE:?temporarily mount the matching offline age identity for restoration}"
: "${BACKUP_SIGNING_KEY_FILE:?temporarily mount the offline Ed25519 backup signing key}"
: "${BACKUP_ALLOWED_SIGNERS_FILE:?set BACKUP_ALLOWED_SIGNERS_FILE to the reviewed namespace-bound allowed_signers file}"
check_root_0600 "$AGE_RECIPIENT_FILE" 'age recipients file'
check_root_private "$AGE_IDENTITY_FILE" 'age identity file'
check_root_private "$BACKUP_SIGNING_KEY_FILE" 'backup signing key'
check_root_0600 "$BACKUP_ALLOWED_SIGNERS_FILE" 'backup allowed_signers file'

signing_public="$(ssh-keygen -y -f "$BACKUP_SIGNING_KEY_FILE" 2>/dev/null)" || die 'cannot read backup signing public key'
[[ "$signing_public" == ssh-ed25519\ * ]] || die 'backup signing key must be Ed25519'
unset signing_public

backup_signers=0
while IFS= read -r signer_line || [[ -n "$signer_line" ]]; do
  [[ -z "$signer_line" || "$signer_line" =~ ^[[:space:]]*# ]] && continue
  [[ "$signer_line" =~ ^invoice-backup[[:space:]]+namespaces=\"solov-invoice-backup-v1\"[[:space:]]+ssh-ed25519[[:space:]]+[A-Za-z0-9+/=]+([[:space:]].*)?$ ]] ||
    die 'backup allowed_signers policy is invalid'
  backup_signers=$((backup_signers+1))
done <"$BACKUP_ALLOWED_SIGNERS_FILE"
(( backup_signers > 0 )) || die 'backup allowed_signers contains no trusted key'
unset backup_signers signer_line

[[ -d "$RECORD_ROOT_EXPECTED" && ! -L "$RECORD_ROOT_EXPECTED" ]] || die 'fixed recovery root must already be a real directory'
readonly RECORD_ROOT="$(realpath -e -- "$RECORD_ROOT_EXPECTED")"
[[ "$RECORD_ROOT" == "$RECORD_ROOT_EXPECTED" && "$(stat -c '%u:%g:%a' -- "$RECORD_ROOT")" == '0:0:700' ]] ||
  die 'fixed recovery root must be root-owned mode 0700'
record_filesystem="$(findmnt -n -T "$RECORD_ROOT" -o FSTYPE)" || die 'cannot identify recovery filesystem'
case "$record_filesystem" in tmpfs|ramfs|overlay) die 'fixed recovery root is on ephemeral storage' ;; esac
unset record_filesystem

[[ -d /run/lock && ! -L /run/lock && "$(stat -c '%u:%g' -- /run/lock)" == '0:0' ]] || die '/run/lock must be a real root-owned directory'
exec 8>"$GLOBAL_LOCK"
chmod 0600 "$GLOBAL_LOCK"
[[ "$(stat -c '%u:%g:%a' -- "$GLOBAL_LOCK")" == '0:0:600' ]] || die 'maintenance lock permissions are unsafe'
flock -n 8 || die 'another permanent administrator maintenance operation is running'

[[ -d /dev/shm && ! -L /dev/shm && "$(stat -c '%u:%g' -- /dev/shm)" == '0:0' ]] || die '/dev/shm is unavailable or unsafe'
TMP_DIR="$(mktemp -d /dev/shm/keycloak-master-admin-maintenance.XXXXXX)"
[[ "$(stat -c '%u:%g:%a' -- "$TMP_DIR")" == '0:0:700' ]] || die 'ephemeral working directory permissions are unsafe'

RECORD_DIR="$(mktemp -d "$RECORD_ROOT/keycloak-master-admin-maintenance-$(date -u +%Y%m%dT%H%M%SZ).XXXXXX")"
[[ "$(stat -c '%u:%g:%a' -- "$RECORD_DIR")" == '0:0:700' ]] || die 'recovery record directory permissions are unsafe'
readonly ORIGINAL="$TMP_DIR/original-allowlist.conf"
readonly ORIGINAL_FROM_RECORD="$TMP_DIR/original-from-record.conf"
readonly ENCRYPTED_ORIGINAL="$RECORD_DIR/original-allowlist.conf.age"
readonly ORIGINAL_MANIFEST="$RECORD_DIR/ALLOWLIST-ORIGINAL.sha256"

cp -- "$ALLOWLIST" "$ORIGINAL"
chown 0:0 "$ORIGINAL"
chmod 0600 "$ORIGINAL"
allowlist_is_safe "$ORIGINAL" || die 'ephemeral original allowlist copy is invalid'
cmp -s -- "$ALLOWLIST" "$ORIGINAL" || die 'administrator allowlist changed while it was copied'

age --encrypt --recipients-file "$AGE_RECIPIENT_FILE" --output "$ENCRYPTED_ORIGINAL" "$ORIGINAL" >/dev/null 2>&1 ||
  die 'administrator allowlist recovery encryption failed'
chmod 0600 "$ENCRYPTED_ORIGINAL"
age --decrypt --identity "$AGE_IDENTITY_FILE" --output "$ORIGINAL_FROM_RECORD" "$ENCRYPTED_ORIGINAL" >/dev/null 2>&1 ||
  die 'encrypted administrator allowlist recovery drill failed'
chmod 0600 "$ORIGINAL_FROM_RECORD"
allowlist_is_safe "$ORIGINAL_FROM_RECORD" || die 'decrypted recovery allowlist is invalid'
cmp -s -- "$ORIGINAL" "$ORIGINAL_FROM_RECORD" || die 'encrypted recovery allowlist differs from the exact original'
(
  cd "$RECORD_DIR"
  sha256sum -- original-allowlist.conf.age >ALLOWLIST-ORIGINAL.sha256
)
chmod 0600 "$ORIGINAL_MANIFEST"
ssh-keygen -Y sign -q -f "$BACKUP_SIGNING_KEY_FILE" -n "$BACKUP_NAMESPACE" "$ORIGINAL_MANIFEST" >/dev/null 2>&1 ||
  die 'recovery manifest signing failed'
chmod 0600 "$ORIGINAL_MANIFEST.sig"
ssh-keygen -Y verify -f "$BACKUP_ALLOWED_SIGNERS_FILE" -I "$BACKUP_SIGNER" -n "$BACKUP_NAMESPACE" \
  -s "$ORIGINAL_MANIFEST.sig" <"$ORIGINAL_MANIFEST" >/dev/null 2>&1 || die 'recovery manifest signature verification failed'
for persistent_file in "$ENCRYPTED_ORIGINAL" "$ORIGINAL_MANIFEST" "$ORIGINAL_MANIFEST.sig"; do
  sync -f "$persistent_file" || die 'signed encrypted recovery copy did not reach persistent storage'
done
sync -f "$RECORD_DIR" || die 'recovery record directory did not reach persistent storage'
sync -f "$RECORD_ROOT" || die 'recovery root directory did not reach persistent storage'

printf '%s\n' 'allow 127.0.0.1;' 'allow ::1;' 'deny all;' >"$TMP_DIR/freeze.conf"
chmod 0600 "$TMP_DIR/freeze.conf"

FREEZE_STARTED=false
RESTORE_COMPLETE=false

atomic_replace_allowlist() {
  local source="$1" replacement
  replacement="$(mktemp "$(dirname -- "$ALLOWLIST")/.auth-admin.solov.cc.allow.conf.XXXXXX")" || return 1
  if ! cp -- "$source" "$replacement" ||
     ! chown 0:0 "$replacement" ||
     ! chmod 0600 "$replacement" ||
     ! sync -f "$replacement" ||
     ! mv -fT -- "$replacement" "$ALLOWLIST"; then
    rm -f -- "$replacement"
    return 1
  fi
  sync -f "$ALLOWLIST" || return 1
  sync -f "$(dirname -- "$ALLOWLIST")" || return 1
}

nginx_test_reload() {
  "$NGINX" -t >/dev/null 2>&1 && "$NGINX" -s reload >/dev/null 2>&1
}

restore_allowlist() {
  local attempt original="$ORIGINAL_FROM_RECORD"
  allowlist_is_safe "$ORIGINAL_FROM_RECORD" || return 1
  for attempt in 1 2 3; do
    atomic_replace_allowlist "$ORIGINAL_FROM_RECORD" || { sleep 1; continue; }
    allowlist_is_safe "$ALLOWLIST" || { sleep 1; continue; }
    cmp -s "$original" "$ALLOWLIST" || { sleep 1; continue; }
    nginx_test_reload || { sleep 1; continue; }
    RESTORE_COMPLETE=true
    return 0
  done
  return 1
}

wait_for_gate() {
  local sentinel="$1" expected="$2" reply=''
  printf '%s\n' "$sentinel"
  IFS= read -r -t "$GATE_TIMEOUT_SECONDS" reply || return 1
  [[ "$reply" == "$expected" ]]
}

cleanup() {
  local rc=$? recovery_failed=false
  trap - EXIT HUP INT TERM
  set +e
  if $FREEZE_STARTED && ! $RESTORE_COMPLETE; then
    restore_allowlist || recovery_failed=true
  fi
  rm -rf -- "$TMP_DIR"
  if $recovery_failed; then
    printf '%s: CRITICAL exact allowlist restoration or Nginx reload failed\n' "$WRAPPER_NAME" >&2
    rc=1
  fi
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM

wait_for_gate \
  'maintenance_gate;stage=preflight-approved-source;reply=confirm-preflight-auth-admin-reachable' \
  'confirm-preflight-auth-admin-reachable' || die 'preflight approved-source confirmation timed out, closed or mismatched'
cmp -s -- "$ALLOWLIST" "$ORIGINAL_FROM_RECORD" || die 'administrator allowlist changed before maintenance'

FREEZE_STARTED=true
atomic_replace_allowlist "$TMP_DIR/freeze.conf" || die 'cannot atomically install the administrator freeze'
[[ "$(stat -c '%u:%g:%a' -- "$ALLOWLIST")" == '0:0:600' ]] || die 'administrator freeze permissions are unsafe'
cmp -s -- "$TMP_DIR/freeze.conf" "$ALLOWLIST" || die 'administrator freeze content verification failed'
nginx_test_reload || die 'Nginx rejected or failed to activate the administrator freeze'

wait_for_gate \
  'maintenance_gate;stage=frozen-approved-source;reply=confirm-frozen-auth-admin-master-403' \
  'confirm-frozen-auth-admin-master-403' || die 'frozen-route approved-source confirmation timed out, closed or mismatched'

# The operator publishes backup_ready and waits for the off-site ACK on this
# same stdin/stdout stream. A closed or abandoned SSH session is bounded by the
# foreground timeout and reaches the restoration trap.
set +e
timeout --foreground --signal=TERM --kill-after=5s "${OPERATOR_TIMEOUT_SECONDS}s" \
  env KEYCLOAK_ADMIN_WRITE_FREEZE_CONFIRMED=YES "$OPERATOR" \
  2>"$TMP_DIR/operator.stderr" | tee "$TMP_DIR/operator.stdout"
operator_pipeline_status=("${PIPESTATUS[@]}")
set -e
operator_rc="${operator_pipeline_status[0]}"
tee_rc="${operator_pipeline_status[1]}"
operator_output=''
if (( operator_rc == 0 && tee_rc == 0 )) && [[ -f "$TMP_DIR/operator.stdout" && ! -L "$TMP_DIR/operator.stdout" ]] &&
   (( $(stat -c '%s' -- "$TMP_DIR/operator.stdout") <= 4096 )); then
  operator_output="$(tail -n 1 "$TMP_DIR/operator.stdout" | tr -d '\r\n')"
fi
rm -f -- "$TMP_DIR/operator.stdout" "$TMP_DIR/operator.stderr"

# The invitation clock starts only after the operator's off-site ACK and mail
# dispatch. Restore and reload before any result publication or user canary.
restore_allowlist || die 'exact allowlist restoration or Nginx reload failed'
(( operator_rc == 0 && tee_rc == 0 )) || die 'signed operator failed after exact allowlist restoration'
[[ "$operator_output" == "$OPERATOR_SUCCESS" ]] || die 'signed operator returned unexpected output after exact allowlist restoration'

wait_for_gate \
  'maintenance_gate;stage=restored-approved-source;reply=confirm-restored-auth-admin-master-non403' \
  'confirm-restored-auth-admin-master-non403' || die 'restored approved-source confirmation timed out, closed or mismatched'

printf '%s\n' \
  'operation=permanent_master_admin_maintenance' \
  'preflight_approved_source=passed' \
  'freeze_external_denial=passed' \
  'signed_operator=passed' \
  'allowlist_restore=passed' \
  'restored_approved_source=passed' \
  'sensitive_data=not_recorded' \
  >"$RECORD_DIR/maintenance-result.txt"
chmod 0600 "$RECORD_DIR/maintenance-result.txt"
(
  cd "$RECORD_DIR"
  sha256sum -- ALLOWLIST-ORIGINAL.sha256 ALLOWLIST-ORIGINAL.sha256.sig maintenance-result.txt >ALLOWLIST-MAINTENANCE.sha256
)
chmod 0600 "$RECORD_DIR/ALLOWLIST-MAINTENANCE.sha256"
ssh-keygen -Y sign -q -f "$BACKUP_SIGNING_KEY_FILE" -n "$BACKUP_NAMESPACE" "$RECORD_DIR/ALLOWLIST-MAINTENANCE.sha256" >/dev/null 2>&1 ||
  die 'maintenance result signing failed after exact allowlist restoration'
chmod 0600 "$RECORD_DIR/ALLOWLIST-MAINTENANCE.sha256.sig"
ssh-keygen -Y verify -f "$BACKUP_ALLOWED_SIGNERS_FILE" -I "$BACKUP_SIGNER" -n "$BACKUP_NAMESPACE" \
  -s "$RECORD_DIR/ALLOWLIST-MAINTENANCE.sha256.sig" <"$RECORD_DIR/ALLOWLIST-MAINTENANCE.sha256" >/dev/null 2>&1 ||
  die 'maintenance result signature verification failed after exact allowlist restoration'
for persistent_file in "$RECORD_DIR/maintenance-result.txt" "$RECORD_DIR/ALLOWLIST-MAINTENANCE.sha256" "$RECORD_DIR/ALLOWLIST-MAINTENANCE.sha256.sig"; do
  sync -f "$persistent_file" || die 'maintenance result did not reach persistent storage after exact allowlist restoration'
done
sync -f "$RECORD_DIR" || die 'maintenance result directory did not reach persistent storage'

printf '%s\n' "$FIXED_SUCCESS"
