#!/usr/bin/env bash
# POP-04: only main()/cleanup are loaded, with fake Git/Compose/probes.
set -Eeuo pipefail
root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
script="${AUDIT_SOURCE:-$root/deploy/scripts/deploy.sh}"
work="$(mktemp -d)"; mkdir -p "$work/checkout/.git" "$work/status"
sed -n '/^main() {$/,/^}$/p' "$script" > "$work/main.sh"
sed -n '/^cleanup_deploy_lock() {$/,/^}$/p' "$script" > "$work/cleanup.sh"
run_deploy() (
  source "$work/main.sh"; source "$work/cleanup.sh"
  asset_prefix=""
  dry_run=0 reason=fixture repo_path="$work/checkout" status_dir="$work/status"
  env_name=staging remote_name=origin ref_name=refs/heads/release/v0.1-launch branch_name=release/v0.1-launch
  compose_file="$repo_path/deploy/compose/launch.yaml"; override_file="$repo_path/deploy/compose/server-staging.yaml"
  health_url=fixture ready_url=fixture lock_dir='' lock_owned=0 target_sha=''
  FAKE_SHA="$1"; role="$2"
  git() {
    printf '%s %s\n' "$role" "$*" >> "$work/git.trace"
    case " $* " in
      *' --git-path '*) printf '%s/.git/%s\n' "$repo_path" "${@: -1}" ;;
      *' status '*|*' diff '*|*' fetch '*) return 0 ;;
      *' rev-parse '*) printf '%s\n' "$FAKE_SHA" ;;
      *' cat-file -t '*':deploy/'*) printf 'blob\n' ;;
      *' cat-file -t '*) printf 'commit\n' ;;
      *' cat-file -e '*) return 0 ;;
      *' checkout '*) printf '%s\n' "$FAKE_SHA" > "$work/actual-head" ;;
      *) echo 'unexpected fixture git call' >&2; return 98 ;;
    esac
  }
  read_green_status() { return 0; }
  probe() { return 0; }
  run_compose() {
    if [ "$role" = first ] && [ "$1" = config ]; then
      : > "$work/blocked"
      for i in $(seq 1 200); do [ ! -e "$work/release" ] || break; sleep 0.05; done
    fi
    printf '%s %s %s\n' "$role" "$BUILD_COMMIT" "$(cat "$work/actual-head")" >> "$work/compose.trace"
  }
  trap cleanup_deploy_lock EXIT
  if main; then exit 0; else exit "$?"; fi
)
run_deploy aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa first > "$work/first.log" 2>&1 & first_pid=$!
for i in $(seq 1 200); do [ ! -e "$work/blocked" ] || break; sleep 0.05; done
[ -e "$work/blocked" ] || { echo 'fixture did not reach Compose' >&2; exit 2; }
set +e
run_deploy bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb second > "$work/second.log" 2>&1
second_rc=$?
: > "$work/release"
wait "$first_pid"; first_rc=$?
set -e
if [ "$second_rc" != 75 ] || grep -Eq '^second .* fetch ' "$work/git.trace"; then
  echo 'FAIL POP-04: second invocation reached shared Git before rejection' >&2; exit 1
fi
if [ "$first_rc" != 0 ] || [ -d "$work/checkout/.git/xm-deploy.lock" ]; then
  echo 'FAIL POP-04: completed invocation retained its lock' >&2; exit 1
fi
if ! awk '$2 != $3 { exit 1 }' "$work/compose.trace"; then echo 'FAIL POP-04: build source identity changed' >&2; exit 1; fi
echo 'PASS POP-04: shared checkout serialized and released'
echo "fixture=$work"
