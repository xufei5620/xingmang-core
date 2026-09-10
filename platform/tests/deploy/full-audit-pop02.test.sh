#!/usr/bin/env bash
# POP-02: synthetic receive object directory; no push or network.
set -Eeuo pipefail
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
source_hook="${AUDIT_SOURCE:-$root/deploy/git-hooks/pre-receive}"
work="$(mktemp -d)"
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_ALLOW_PROTOCOL=file
bare="$work/receive.git"
git init --bare -q "$bare"
mkdir "$bare/objects/incoming-fixture"
quarantine="$bare/objects/incoming-fixture"
export GIT_AUTHOR_NAME=fixture GIT_AUTHOR_EMAIL=fixture@example.invalid
export GIT_COMMITTER_NAME=fixture GIT_COMMITTER_EMAIL=fixture@example.invalid
object_env=(env "GIT_OBJECT_DIRECTORY=$quarantine" "GIT_ALTERNATE_OBJECT_DIRECTORIES=$bare/objects")
tree="$(printf '' | "${object_env[@]}" git --git-dir="$bare" mktree)"
commit="$(printf 'synthetic receive\n' | "${object_env[@]}" git --git-dir="$bare" commit-tree "$tree")"
zero=0000000000000000000000000000000000000000
if git --git-dir="$bare" cat-file -e "$commit" 2>/dev/null; then echo 'fixture error: new object is not quarantined' >&2; exit 2; fi
if ! (cd "$bare"; printf '%s %s refs/heads/fixture\n' "$zero" "$commit" | "${object_env[@]}" env "GIT_QUARANTINE_PATH=$quarantine" bash -p "$source_hook") > "$work/valid.log" 2>&1; then
  echo 'FAIL POP-02: valid quarantined commit was rejected' >&2
  cat "$work/valid.log" >&2
  exit 1
fi
echo 'PASS POP-02: quarantined commit accepted'
echo "fixture=$work"
