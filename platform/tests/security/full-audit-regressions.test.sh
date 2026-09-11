#!/usr/bin/env bash
# Explicit synthetic-only regressions added by the full audit. Both ci-local.sh
# and the CI security job already execute tests/security/*.test.sh.
set -euo pipefail
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
cd "$root"

# These suites must use this checkout and their own fresh synthetic fixtures.
unset AUDIT_SOURCE RUNBOOK_FIXTURE_ROOT PYTHONOPTIMIZE
export XM_DEV_WTDB_E2E=0
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
export GIT_ALLOW_PROTOCOL=file GIT_TERMINAL_PROMPT=0
for tool in bash python python3 pwsh wsl.exe; do
  unset -f "$tool" 2>/dev/null || true
done

windows=0
case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*) windows=1 ;; esac
if [ "$windows" -eq 1 ]; then
  python=python
  required=(python pwsh wsl.exe cygpath)
else
  python=python3
  required=(python3 pwsh bash jq go git)
fi
for tool in "${required[@]}"; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "FULL AUDIT NORMAL FAIL: missing required tool $tool" >&2
    exit 2
  }
done

run_python() {
  "$python" -X utf8 "$1" || return $?
}

run_shell() {
  if [ "$windows" -eq 1 ]; then
    # WSL only executes tests whose Git/Docker/SQL activity is confined to their
    # independent Linux fixtures; it never queries this Windows worktree's Git.
    local native linux_path
    native="$(cygpath -am "$root/$1")" || return $?
    [[ "$native" =~ ^[A-Za-z]:/ ]] || {
      echo "FULL AUDIT NORMAL FAIL: WSL fixture requires a drive path" >&2
      return 2
    }
    local drive="${native:0:1}"
    linux_path="/mnt/${drive,,}${native:2}"
    MSYS2_ARG_CONV_EXCL='*' wsl.exe --exec env -u AUDIT_SOURCE -u RUNBOOK_FIXTURE_ROOT -u PYTHONOPTIMIZE       GIT_OPTIONAL_LOCKS=0 GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null       GIT_ALLOW_PROTOCOL=file GIT_TERMINAL_PROMPT=0 LC_ALL=C.UTF-8       XM_DEV_WTDB_E2E=0 bash "$linux_path" || return $?
  else
    bash "$1" || return $?
  fi
}

run_powershell() {
  local script="$root/$1"
  if [ "$windows" -eq 1 ]; then
    script="$(cygpath -am "$script")" || return $?
  fi
  pwsh -NoLogo -NoProfile -File "$script" || return $?
}

python_tests=(
  tests/runbooks/archive_fixture_env_test.py
  tests/runbooks/shadow_exit_test.py
  tests/runbooks/evidence_output_test.py
  tests/runbooks/real_restart_overlay_test.py
  tests/runbooks/reqlog_permission_doc_test.py
  tests/runbooks/reqlog_container_doc_test.py
  tests/runbooks/evidence_secret_root_test.py
  tests/runbooks/reqlog_shape_claim_test.py
  tests/runbooks/connector_mode_doc_test.py
  tests/runbooks/current_entry_doc_test.py
  tests/runbooks/deploy_port_doc_test.py
  tests/security/governance-preserves-source.test.py
)
shell_tests=(
  tests/deploy/full-audit-pop01.test.sh
  tests/deploy/full-audit-pop02.test.sh
  tests/deploy/full-audit-pop03.test.sh
  tests/deploy/full-audit-pop04.test.sh
  tests/deploy/full-audit-pop05.test.sh
  tests/deploy/full-audit-pop06.test.sh
  tests/deploy/full-audit-pop07.test.sh
  tests/deploy/full-audit-pop08.test.sh
  tests/deploy/full-audit-pop10.test.sh
  tests/deploy/full-audit-pop11.test.sh
  tests/deploy/full-audit-f3.test.sh
  scripts/dev/test-worktree-testdb.sh
  tests/deploy/deploy-local.test.sh
  tests/deploy/deploy0-a.test.sh
  tests/deploy/deploy0-b.test.sh
)
powershell_tests=(
  scripts/test-database-roles.test.ps1
  scripts/test-runway-threshold-db.test.ps1
)

for test_file in "${python_tests[@]}" "${shell_tests[@]}" "${powershell_tests[@]}"; do
  [ -f "$test_file" ] || {
    echo "FULL AUDIT NORMAL FAIL: missing regression $test_file" >&2
    exit 2
  }
done
for test_file in "${python_tests[@]}"; do
  echo "FULL AUDIT NORMAL: $test_file"
  run_python "$test_file" || exit $?
done
for test_file in "${shell_tests[@]}"; do
  echo "FULL AUDIT NORMAL: $test_file"
  run_shell "$test_file" || exit $?
done
for test_file in "${powershell_tests[@]}"; do
  echo "FULL AUDIT NORMAL: $test_file"
  run_powershell "$test_file" || exit $?
done
echo "FULL AUDIT NORMAL PASS: ${#python_tests[@]} Python, ${#shell_tests[@]} shell, ${#powershell_tests[@]} PowerShell regressions"
