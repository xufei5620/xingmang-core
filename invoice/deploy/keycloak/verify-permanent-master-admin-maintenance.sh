#!/usr/bin/env bash
set -Eeuo pipefail

# Dynamic fail-closed tests for the interactive maintenance wrapper. A copied
# wrapper is path-rebound to root-owned disposable fixtures; no production path
# or service is touched.

umask 077

readonly VERIFY_NAME='verify-permanent-master-admin-maintenance'
readonly SOURCE_WRAPPER="$(cd -- "$(dirname -- "$0")" && pwd)/run-permanent-master-admin-maintenance.sh"
readonly -a EXPECTED_SCENARIOS=(
  operator-failure
  nginx-test-failure
  nginx-reload-failure
  preflight-eof
  frozen-gate-timeout
  operator-eof
  operator-timeout
  restored-gate-timeout
  interactive-success
)

fail() {
  printf '%s: %s\n' "$VERIFY_NAME" "$*" >&2
  exit 1
}

for command_name in awk base64 bash chmod cmp cp find grep mkdir mktemp python3 rm sha256sum ssh-keygen stat wc; do
  command -v "$command_name" >/dev/null 2>&1 || fail "required command is unavailable: $command_name"
done

(( EUID == 0 )) || fail 'dynamic verifier must run as root in a disposable Linux or WSL environment'
[[ -f "$SOURCE_WRAPPER" && ! -L "$SOURCE_WRAPPER" && -s "$SOURCE_WRAPPER" ]] || fail 'maintenance wrapper is missing'
bash -n "$SOURCE_WRAPPER" || fail 'maintenance wrapper failed bash syntax validation'

for required in \
  "readonly ALLOWLIST='/www/server/panel/vhost/nginx/access/auth-admin.solov.cc.allow.conf'" \
  "readonly NGINX='/www/server/nginx/sbin/nginx'" \
  "readonly GLOBAL_LOCK='/run/lock/solov-keycloak-master-admin-maintenance.lock'" \
  'original-allowlist.conf.age' \
  'ALLOWLIST-ORIGINAL.sha256' \
  'allow 127.0.0.1;' \
  'allow ::1;' \
  'deny all;' \
  'timeout --foreground --signal=TERM' \
  '2>"$TMP_DIR/operator.stderr" | tee "$TMP_DIR/operator.stdout"' \
  'KEYCLOAK_ADMIN_WRITE_FREEZE_CONFIRMED=YES' \
  'trap cleanup EXIT' \
  'maintenance_gate;stage=preflight-approved-source' \
  'maintenance_gate;stage=frozen-approved-source' \
  'maintenance_gate;stage=restored-approved-source' \
  'approved_source_probe":"passed"'; do
  grep -Fq "$required" "$SOURCE_WRAPPER" || fail "maintenance wrapper contract is missing: $required"
done

if grep -Eni '(set[[:space:]]+-x|eval[[:space:]]|(^|[[:space:]])(bash|sh)[[:space:]]+-c|authorization:|bearer[[:space:]]|password=|token=|secret=|target[_-]?email|smtp[_-]?(host|user|password))' "$SOURCE_WRAPPER" >/dev/null; then
  fail 'wrapper failed command-injection or secret-material scan'
fi

SANDBOX="$(mktemp -d /root/keycloak-maintenance-verify.XXXXXX)"
cleanup() { rm -rf -- "$SANDBOX"; }
trap cleanup EXIT HUP INT TERM

RELEASE_ROOT="$SANDBOX/release"
PROJECT_ROOT="$RELEASE_ROOT/source/invoice"
WRAPPER="$PROJECT_ROOT/deploy/keycloak/run-permanent-master-admin-maintenance.sh"
OPERATOR="$PROJECT_ROOT/deploy/keycloak/invite-permanent-master-admin.sh"
ALLOWLIST="$SANDBOX/nginx/access/auth-admin.solov.cc.allow.conf"
NGINX="$SANDBOX/nginx/sbin/nginx"
RECORD_ROOT="$SANDBOX/records"
TRUST_DIR="$SANDBOX/trust"
STATE_DIR="$SANDBOX/state"
BIN_DIR="$SANDBOX/bin"
LOCK_FILE="/run/lock/solov-keycloak-master-admin-maintenance-verify.lock"

mkdir -p -- "$(dirname -- "$WRAPPER")" "$(dirname -- "$ALLOWLIST")" "$(dirname -- "$NGINX")" "$RECORD_ROOT" "$TRUST_DIR" "$STATE_DIR" "$BIN_DIR"
find "$SANDBOX" -type d -exec chmod 0700 {} +
cp -- "$SOURCE_WRAPPER" "$WRAPPER"
python3 - "$WRAPPER" "$ALLOWLIST" "$NGINX" "$LOCK_FILE" "$RECORD_ROOT" "$TRUST_DIR/release-tree-allowed-signers" <<'PY'
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
text = path.read_text(encoding="utf-8")
replacements = {
    "readonly ALLOWLIST='/www/server/panel/vhost/nginx/access/auth-admin.solov.cc.allow.conf'": f"readonly ALLOWLIST='{sys.argv[2]}'",
    "readonly NGINX='/www/server/nginx/sbin/nginx'": f"readonly NGINX='{sys.argv[3]}'",
    "readonly GLOBAL_LOCK='/run/lock/solov-keycloak-master-admin-maintenance.lock'": f"readonly GLOBAL_LOCK='{sys.argv[4]}'",
    "readonly RECORD_ROOT_EXPECTED='/root/invoice-system/keycloak-backups'": f"readonly RECORD_ROOT_EXPECTED='{sys.argv[5]}'",
    "readonly RELEASE_ALLOWED_SIGNERS_FILE='/root/invoice-system/trust/release-tree-allowed-signers'": f"readonly RELEASE_ALLOWED_SIGNERS_FILE='{sys.argv[6]}'",
    "readonly GATE_TIMEOUT_SECONDS='300'": "readonly GATE_TIMEOUT_SECONDS='1'",
    "readonly OPERATOR_TIMEOUT_SECONDS='1800'": "readonly OPERATOR_TIMEOUT_SECONDS='2'",
}
for old, new in replacements.items():
    if text.count(old) != 1:
        raise SystemExit(f"fixed contract replacement count was not one: {old}")
    text = text.replace(old, new)
path.write_text(text, encoding="utf-8", newline="\n")
PY
chmod 0700 "$WRAPPER"
bash -n "$WRAPPER"

cat >"$OPERATOR" <<EOF
#!/usr/bin/env bash
set -Eeuo pipefail
state='$STATE_DIR'
if [[ -f "\$state/operator-fail" ]]; then exit 42; fi
printf '%s\n' 'backup_ready;record=/root/non-identifying-record'
if [[ -f "\$state/operator-timeout" ]]; then sleep 10; fi
IFS= read -r acknowledgement || exit 43
[[ "\$acknowledgement" == continue ]] || exit 44
printf '%s\n' '{"status":"ok","operation":"permanent-master-admin-invitation","backup_restore":"passed","invitation":"dispatched","master_smtp_restored":true}'
EOF
chmod 0700 "$OPERATOR"

cat >"$NGINX" <<EOF
#!/usr/bin/env bash
set -Eeuo pipefail
state='$STATE_DIR'
allowlist='$ALLOWLIST'
if [[ "\${1:-}" == -t ]]; then
  if [[ -f "\$state/fail-test-once" ]]; then rm -f "\$state/fail-test-once"; exit 51; fi
  exit 0
fi
if [[ "\${1:-}" == -s && "\${2:-}" == reload ]]; then
  if [[ -f "\$state/fail-reload-once" ]]; then rm -f "\$state/fail-reload-once"; exit 52; fi
  if cmp -s <(printf '%s\n' 'allow 127.0.0.1;' 'allow ::1;' 'deny all;') "\$allowlist"; then
    printf '%s\n' frozen >"\$state/runtime"
  else
    printf '%s\n' restored >"\$state/runtime"
  fi
  exit 0
fi
exit 53
EOF
chmod 0700 "$NGINX"

cat >"$BIN_DIR/age" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
mode='' output='' input=''
while (( $# > 0 )); do
  case "$1" in
    --encrypt) mode=encrypt; shift ;;
    --decrypt) mode=decrypt; shift ;;
    --recipients-file|--identity) shift 2 ;;
    --output) output="$2"; shift 2 ;;
    *) input="$1"; shift ;;
  esac
done
[[ -n "$mode" && -n "$output" && -f "$input" ]]
if [[ "$mode" == encrypt ]]; then
  base64 <"$input" >"$output"
else
  base64 -d <"$input" >"$output"
fi
EOF
chmod 0700 "$BIN_DIR/age"

printf '%s\n' 'allow 192.0.2.10/32;' 'allow ::1/128;' 'deny all;' >"$ALLOWLIST"
chmod 0600 "$ALLOWLIST"
cp -- "$ALLOWLIST" "$SANDBOX/original.expected"
chmod 0600 "$SANDBOX/original.expected"
printf '%s\n' restored >"$STATE_DIR/runtime"

ssh-keygen -q -t ed25519 -N '' -f "$TRUST_DIR/release-key"
release_public="$(<"$TRUST_DIR/release-key.pub")"
printf '%s namespaces="solov-invoice-release-v1" %s\n' 'invoice-release@solov.cc' "$release_public" >"$TRUST_DIR/release-tree-allowed-signers"
chmod 0600 "$TRUST_DIR/release-key" "$TRUST_DIR/release-key.pub" "$TRUST_DIR/release-tree-allowed-signers"
(
  cd "$RELEASE_ROOT"
  sha256sum source/invoice/deploy/keycloak/invite-permanent-master-admin.sh source/invoice/deploy/keycloak/run-permanent-master-admin-maintenance.sh >RELEASE-TREE.sha256
)
chmod 0600 "$RELEASE_ROOT/RELEASE-TREE.sha256"
ssh-keygen -Y sign -q -f "$TRUST_DIR/release-key" -n solov-invoice-release-v1 "$RELEASE_ROOT/RELEASE-TREE.sha256" >/dev/null
chmod 0600 "$RELEASE_ROOT/RELEASE-TREE.sha256.sig"

ssh-keygen -q -t ed25519 -N '' -f "$TRUST_DIR/backup-key"
backup_public="$(<"$TRUST_DIR/backup-key.pub")"
printf '%s namespaces="solov-invoice-backup-v1" %s\n' invoice-backup "$backup_public" >"$TRUST_DIR/backup-allowed-signers"
chmod 0600 "$TRUST_DIR/backup-key" "$TRUST_DIR/backup-key.pub" "$TRUST_DIR/backup-allowed-signers"

printf '%s\n' fixture-identity >"$TRUST_DIR/age-identity"
printf '%s\n' fixture-recipient >"$TRUST_DIR/age-recipients"
chmod 0600 "$TRUST_DIR/age-identity" "$TRUST_DIR/age-recipients"

reset_scenario() {
  rm -f -- "$STATE_DIR"/operator-* "$STATE_DIR"/fail-*
  cp -- "$SANDBOX/original.expected" "$ALLOWLIST"
  chmod 0600 "$ALLOWLIST"
  printf '%s\n' restored >"$STATE_DIR/runtime"
}

declare -A KNOWN_RECORDS=()
mark_scenario_record() {
  local scenario="$1" record_dir new_record='' new_count=0
  while IFS= read -r record_dir; do
    if [[ -z "${KNOWN_RECORDS[$record_dir]:-}" ]]; then
      new_record="$record_dir"
      new_count=$((new_count+1))
    fi
  done < <(find "$RECORD_ROOT" -mindepth 1 -maxdepth 1 -type d -name 'keycloak-master-admin-maintenance-*' -print)
  (( new_count == 1 )) || fail "$scenario created an unexpected number of recovery records"
  printf '%s\n' "$scenario" >"$new_record/.scenario-marker"
  chmod 0600 "$new_record/.scenario-marker"
  KNOWN_RECORDS["$new_record"]=1
}

assert_restored() {
  local scenario="$1"
  cmp -s -- "$SANDBOX/original.expected" "$ALLOWLIST" || fail "$scenario did not restore the exact original allowlist"
  [[ "$(stat -c '%u:%g:%a' -- "$ALLOWLIST")" == '0:0:600' ]] || fail "$scenario restored unsafe allowlist permissions"
  [[ "$(<"$STATE_DIR/runtime")" == restored ]] || fail "$scenario did not reactivate the restored Nginx policy"
}

run_wrapper() {
  PATH="$BIN_DIR:$PATH" \
  AGE_RECIPIENT_FILE="$TRUST_DIR/age-recipients" \
  AGE_IDENTITY_FILE="$TRUST_DIR/age-identity" \
  BACKUP_SIGNING_KEY_FILE="$TRUST_DIR/backup-key" \
  BACKUP_ALLOWED_SIGNERS_FILE="$TRUST_DIR/backup-allowed-signers" \
    "$WRAPPER"
}

expect_failure() {
  local scenario="$1" input_mode="$2"
  set +e
  case "$input_mode" in
    preflight-only)
      printf '%s\n' confirm-preflight-auth-admin-reachable | run_wrapper >"$SANDBOX/$scenario.stdout" 2>"$SANDBOX/$scenario.stderr"
      ;;
    through-freeze)
      printf '%s\n' confirm-preflight-auth-admin-reachable confirm-frozen-auth-admin-master-403 | run_wrapper >"$SANDBOX/$scenario.stdout" 2>"$SANDBOX/$scenario.stderr"
      ;;
    frozen-timeout)
      { printf '%s\n' confirm-preflight-auth-admin-reachable; sleep 2; } | run_wrapper >"$SANDBOX/$scenario.stdout" 2>"$SANDBOX/$scenario.stderr"
      ;;
    restored-timeout)
      { printf '%s\n' confirm-preflight-auth-admin-reachable confirm-frozen-auth-admin-master-403 continue; sleep 2; } | run_wrapper >"$SANDBOX/$scenario.stdout" 2>"$SANDBOX/$scenario.stderr"
      ;;
    no-input)
      run_wrapper </dev/null >"$SANDBOX/$scenario.stdout" 2>"$SANDBOX/$scenario.stderr"
      ;;
    *) fail 'unknown verifier input mode' ;;
  esac
  local rc=$?
  set -e
  (( rc != 0 )) || fail "$scenario unexpectedly succeeded"
  if grep -Fq '"status":"ok","operation":"permanent-master-admin-maintenance"' "$SANDBOX/$scenario.stdout"; then
    fail "$scenario emitted the wrapper success record"
  fi
  assert_restored "$scenario"
  mark_scenario_record "$scenario"
}

reset_scenario
touch "$STATE_DIR/operator-fail"
expect_failure operator-failure through-freeze

reset_scenario
touch "$STATE_DIR/fail-test-once"
expect_failure nginx-test-failure preflight-only

reset_scenario
touch "$STATE_DIR/fail-reload-once"
expect_failure nginx-reload-failure preflight-only

reset_scenario
expect_failure preflight-eof no-input

reset_scenario
expect_failure frozen-gate-timeout frozen-timeout

reset_scenario
expect_failure operator-eof through-freeze

reset_scenario
touch "$STATE_DIR/operator-timeout"
expect_failure operator-timeout through-freeze

reset_scenario
expect_failure restored-gate-timeout restored-timeout

reset_scenario
printf '%s\n' \
  confirm-preflight-auth-admin-reachable \
  confirm-frozen-auth-admin-master-403 \
  continue \
  confirm-restored-auth-admin-master-non403 \
  | run_wrapper >"$SANDBOX/success.stdout" 2>"$SANDBOX/success.stderr" || fail 'interactive success scenario failed'
assert_restored interactive-success
mark_scenario_record interactive-success
for sentinel in \
  'maintenance_gate;stage=preflight-approved-source;reply=confirm-preflight-auth-admin-reachable' \
  'maintenance_gate;stage=frozen-approved-source;reply=confirm-frozen-auth-admin-master-403' \
  'backup_ready;record=/root/non-identifying-record' \
  'maintenance_gate;stage=restored-approved-source;reply=confirm-restored-auth-admin-master-non403'; do
  grep -Fxq "$sentinel" "$SANDBOX/success.stdout" || fail "interactive stream omitted: $sentinel"
done
grep -Fq '"status":"ok","operation":"permanent-master-admin-maintenance"' "$SANDBOX/success.stdout" ||
  fail 'interactive success omitted the fixed wrapper success record'

record_count="$(find "$RECORD_ROOT" -mindepth 1 -maxdepth 1 -type d -name 'keycloak-master-admin-maintenance-*' | wc -l)"
expected_record_count="${#EXPECTED_SCENARIOS[@]}"
(( record_count == expected_record_count )) || fail 'dynamic scenario and encrypted recovery record counts differ'
for scenario in "${EXPECTED_SCENARIOS[@]}"; do
  scenario_count="$(grep -R -l -x -F --include='.scenario-marker' "$scenario" "$RECORD_ROOT" | wc -l)"
  (( scenario_count == 1 )) || fail "$scenario does not map to exactly one recovery record"
done
if grep -R -F '192.0.2.10' "$RECORD_ROOT" >/dev/null 2>&1; then
  fail 'an administrator address leaked into persistent evidence'
fi
while IFS= read -r record_dir; do
  [[ "$(stat -c '%u:%g:%a' -- "$record_dir")" == '0:0:700' ]] || fail 'recovery record directory permissions are unsafe'
  [[ "$(stat -c '%u:%g:%a' -- "$record_dir/original-allowlist.conf.age")" == '0:0:600' ]] || fail 'encrypted recovery copy permissions are unsafe'
  ssh-keygen -Y verify -f "$TRUST_DIR/backup-allowed-signers" -I invoice-backup -n solov-invoice-backup-v1 \
    -s "$record_dir/ALLOWLIST-ORIGINAL.sha256.sig" <"$record_dir/ALLOWLIST-ORIGINAL.sha256" >/dev/null 2>&1 ||
    fail 'encrypted recovery record signature verification failed'
done < <(find "$RECORD_ROOT" -mindepth 1 -maxdepth 1 -type d -name 'keycloak-master-admin-maintenance-*' -print)

printf '%s\n' 'Interactive approved-source gates, operator ACK streaming, failure timeouts and exact restoration tests passed.'
