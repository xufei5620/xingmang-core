#!/usr/bin/env bash
set -Eeuo pipefail
project_root=${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}
temporary=$(mktemp -d)
trap 'rm -rf -- "$temporary"' EXIT
printf 'synthetic public marker\n' >"$temporary/regular"
: >"$temporary/empty"
mkdir "$temporary/directory"
failures=0
cases=0
# The symlink metadata result is simulated so Git Bash's copy-on-ln behavior
# cannot turn that test into a regular-file positive. No secret is opened.
test() {
  if [[ "${1:-}" == '!' && "${2:-}" == -L ]]; then [[ "$kind" != symlink ]]; return; fi
  command test "$@"
}
# Unified backup/cutover/rollback input guards run in test-unified-operations.py.
for relative in deploy/preflight-secret-permissions.sh \
  deploy/backup/restore-drill.sh deploy/rehearsal/shadow-eval.sh; do
  ordinal=0
  while IFS= read -r guard; do
    [[ "$guard" == *'test -f '* && "$guard" == *'test ! -L '* && "$guard" == *'test -s '* ]] || continue
    ordinal=$((ordinal+1))
    for kind in regular directory symlink empty; do
      label="$relative:$ordinal:$kind"
      [[ -z "${FILE_GUARD_CASE:-}" || "$FILE_GUARD_CASE" == "$label" ]] || continue
      cases=$((cases+1))
      value="$temporary/$kind"
      [[ "$kind" != symlink ]] || value="$temporary/regular"
      path=$value; file=$value; capacity_validator=$value; cleanup_state_helper=$value; balance_history_rehearsal=$value
      BACKUP_SIGNING_KEY_FILE=$value; BACKUP_ALLOWED_SIGNERS_FILE=$value; KEYCLOAK_BACKUP=$value; AGE_IDENTITY_FILE=$value
      set +e
      (set -euo pipefail; eval "$guard"$'\necho guard-accepted') >"$temporary/output" 2>&1
      status=$?
      set -e
      if [[ "$kind" == regular && "$status" != 0 ]] || [[ "$kind" != regular && "$status" == 0 ]]; then
        echo "FAIL $label: exit=$status" >&2; failures=$((failures+1))
      fi
    done
  done <"$project_root/$relative"
done
(( cases > 0 )) || { echo 'FAIL no file guards exercised' >&2; exit 1; }
(( failures == 0 )) || exit 1
printf 'Deployment file guards passed: %s cases\n' "$cases"
