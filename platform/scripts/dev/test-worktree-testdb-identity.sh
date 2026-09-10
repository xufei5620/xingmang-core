#!/usr/bin/env bash
# Local identity and fake SQL boundary only; never open a database connection.
set -Eeuo pipefail
script="$(cd "$(dirname "$0")" && pwd)/worktree-testdb.sh"
source "$script"
fail=0
check_distinct() {
  local label="$1" left="$2" right="$3"
  if [ "$(derive_db_name "$left")" = "$(derive_db_name "$right")" ]; then
    echo "FAIL: distinct worktree identities collided ($label)" >&2; fail=1
  fi
}
check_distinct same_basename /synthetic/team-a/core /synthetic/team-b/core
check_distinct punctuation /synthetic/team/wt-a /synthetic/team/wt_a
check_distinct case /synthetic/team/Feature /synthetic/team/feature
check_distinct non_ascii_prefix /synthetic/team/甲-core /synthetic/team/乙-core
if [ "$(derive_db_name /synthetic/team/core)" = xm_test_core ]; then
  echo 'FAIL: legacy basename-only database must remain unselected' >&2; fail=1
fi
if [ "$(derive_db_name /synthetic/team/./core)" != "$(derive_db_name /synthetic/team/core)" ]; then
  echo 'FAIL: equivalent cleaned worktree paths must retain one identity' >&2; fail=1
fi
# Exercise the actual drop action with a fake SQL boundary. No executable can
# connect to a DB; capture only SQL targets derived from two worktree identities.
psql_exec() { printf '%s\n' "$1" >&3; }
drop_for() ( dbname="$(derive_db_name "$1")"; cmd_drop 3>&1 2>/dev/null; )
left="$(drop_for /synthetic/team-a/core)"
right="$(drop_for /synthetic/team-b/core)"
if [ "$left" = "$right" ] || [[ "$left" == *'"xm_test_core"'* ]]; then
  echo 'FAIL: drop SQL must target this full-path identity and preserve legacy database' >&2; fail=1
fi
[ "$fail" -ne 0 ] || echo WORKTREE-TESTDB-IDENTITY-OK
exit "$fail"
