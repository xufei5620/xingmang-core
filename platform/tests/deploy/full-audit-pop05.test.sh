#!/usr/bin/env bash
# POP-05: execute only installer argument/path preflight, never install commands.
set -Eeuo pipefail
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
script="${AUDIT_SOURCE:-$root/deploy/scripts/install-git-server.sh}"
work="$(mktemp -d)"; mkdir "$work/git" "$work/ci"
sed '/^echo "Git server install plan:/,$d' "$script" > "$work/prefix.sh"
printf '\necho VALIDATED\n' >> "$work/prefix.sh"
run() { bash -c 'id() { [ "${1:-}" != -u ] || { echo 0; return; }; command id "$@"; }; source "$1" "${@:2}"' _ "$work/prefix.sh" "$@"; }
if ! run --repo "$work/git/repo.git" --ci-dir "$work/ci" --dry-run > "$work/legal.log" 2>&1; then
 echo 'FAIL POP-05: legal separated targets rejected' >&2; exit 1
fi
for kind in ci-alias repo-parent; do
 repo="$work/git/repo.git"; ci=//
 [ "$kind" = ci-alias ] || { repo=/xingmang.git; ci="$work/ci"; }
 if run --repo "$repo" --ci-dir "$ci" --dry-run > "$work/$kind.log" 2>&1; then
   echo "FAIL POP-05: unsafe $kind accepted" >&2; exit 1
 fi
done
echo 'PASS POP-05: unsafe root targets rejected before installation'
echo "fixture=$work"
