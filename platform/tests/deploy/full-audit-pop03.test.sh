#!/usr/bin/env bash
# Exercise only the real lock cleanup function against newly-created files.
set -Eeuo pipefail
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
script="${AUDIT_SOURCE:-$root/deploy/scripts/promote.sh}"
work="$(mktemp -d)"
sed -n '/^cleanup_marker_lock() {$/,/^}$/p' "$script" > "$work/function.sh"
source "$work/function.sh"
source_sha=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
marker_lock="$work/owned.lock"; mkdir "$marker_lock"
marker_lock_owned=1
printf '%s\n' "$$" > "$marker_lock/pid"
printf '%s\n' "$source_sha" > "$marker_lock/sha"
cleanup_marker_lock
if [ -e "$marker_lock" ]; then echo 'FAIL POP-03: owned lock remains' >&2; exit 1; fi
marker_lock="$work/foreign.lock"; mkdir "$marker_lock"
printf '%s\n' "$(( $$ + 100000 ))" > "$marker_lock/pid"
printf '%s\n' "$source_sha" > "$marker_lock/sha"
marker_lock_owned=1
cleanup_marker_lock
if [ ! -s "$marker_lock/pid" ] || [ ! -s "$marker_lock/sha" ]; then echo 'FAIL POP-03: foreign lock was changed' >&2; exit 1; fi
echo 'PASS POP-03: owned files released, foreign files retained'
echo "fixture=$work"
