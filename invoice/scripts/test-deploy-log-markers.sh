#!/usr/bin/env bash
# Exercise only the real docker-log predicates; Docker is always a local fake.
set -Eeuo pipefail
project_root=${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}
filter=${DEPLOY_MARKER_CASE:-}
container=fixture-postgres
restore_container=fixture-postgres
marker=''
mode=''
docker() {
  [[ "${1:-}" == logs ]] || return 99
  [[ "$mode" == absent ]] || printf '%s\n' "$marker"
  for ((i=0; i<20000; i++)); do printf 'synthetic follow-up log %0100d\n' "$i"; done
  [[ "$mode" != producer-failure ]] || return 73
}
failures=0
cases=0
# Unified lifecycle predicates are exercised by test-unified-operations.py.
# These retained offline restore/shadow helpers still require producer-exit guards.
for relative in deploy/backup/restore-drill.sh deploy/rehearsal/shadow-eval.sh; do
  ordinal=0
  while IFS= read -r line; do
    [[ "$line" == *'docker logs'*'| grep '* ]] || continue
    case "$line" in
      *"'invoice API listening'"*) marker='invoice API listening' ;;
      *"'PostgreSQL init process complete; ready for start up.'"*) marker='PostgreSQL init process complete; ready for start up.' ;;
      *) continue ;;
    esac
    ordinal=$((ordinal+1))
    pipeline="docker logs${line#*docker logs}"
    pipeline=${pipeline%\; do}
    pipeline=${pipeline%\ \|\| \{*}
    pipeline=${pipeline%\ \&\& break\; sleep*}
    for mode in present absent producer-failure; do
      label="$relative:$ordinal:$mode"
      [[ -z "$filter" || "$label" == "$filter" ]] || continue
      cases=$((cases+1))
      if (eval "$pipeline") >/dev/null 2>&1; then actual=0; else actual=$?; fi
      if [[ "$mode" == present && "$actual" != 0 ]] || [[ "$mode" != present && "$actual" == 0 ]]; then
        printf 'FAIL %s: unexpected exit %s\n' "$label" "$actual" >&2
        failures=$((failures+1))
      fi
    done
  done <"$project_root/$relative"
done
(( cases > 0 )) || { echo 'FAIL no log-marker predicates exercised' >&2; exit 1; }
(( failures == 0 )) || exit 1
printf 'Deployment log-marker cases passed: %s\n' "$cases"
