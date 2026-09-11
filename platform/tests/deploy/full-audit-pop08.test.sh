#!/usr/bin/env bash
# POP-08: mirror validation prefix; no repository access or push.
set -Eeuo pipefail
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
script="${AUDIT_SOURCE:-$root/deploy/scripts/mirror-github.sh}"
work="$(mktemp -d)"
sed '/^\[ -d "$repo_path" \]/,$d' "$script" > "$work/prefix.sh"
printf '\necho REACHED_REPOSITORY_BOUNDARY\n' >> "$work/prefix.sh"
fail=0
for mode in default explicit; do
 args=(--dry-run); [ "$mode" = default ] || args+=(--git-bin /usr/bin/git)
 if ! bash -p "$work/prefix.sh" "${args[@]}" > "$work/$mode.log" 2>&1 || ! grep -q REACHED_REPOSITORY_BOUNDARY "$work/$mode.log"; then
  echo "FAIL POP-08: $mode system Git rejected" >&2; fail=1
 fi
done
printf '#!/bin/sh\nexit 0\n' > "$work/git"; chmod +x "$work/git"
if bash -p "$work/prefix.sh" --dry-run --git-bin "$work/git" > "$work/untrusted.log" 2>&1; then
 echo 'FAIL POP-08: non-system executable accepted' >&2; fail=1
fi
[ "$fail" = 0 ] || exit 1
echo 'PASS POP-08: default and explicit system Git reach repository boundary'
