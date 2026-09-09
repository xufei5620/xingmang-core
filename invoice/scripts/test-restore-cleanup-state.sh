#!/usr/bin/env bash
set -Eeuo pipefail

project_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
temporary=$(mktemp -d)
trap 'rm -rf -- "$temporary"' EXIT
state_file="$temporary/resource-present"

cat >"$temporary/docker" <<'FAKE'
#!/usr/bin/env bash
set -u
mode=${FAKE_DOCKER_MODE:?}
state_file=${FAKE_DOCKER_STATE_FILE:?}
if [[ "${1:-}" == info ]]; then
  [[ "$mode" != unknown ]]
  exit
fi
if [[ "${1:-}" == container && "${2:-}" == inspect ]] ||
   [[ "${1:-}" == network && "${2:-}" == inspect ]]; then
  case "$mode" in
    absent) exit 1 ;;
    unknown) exit 2 ;;
    present|persistent) exit 0 ;;
    removable) [[ -e "$state_file" ]]; exit ;;
  esac
fi
if [[ "${1:-}" == rm ]] || [[ "${1:-}" == network && "${2:-}" == rm ]]; then
  [[ "$mode" == removable ]] && rm -f -- "$state_file"
  [[ "$mode" != unknown ]]
  exit
fi
exit 2
FAKE
chmod 0700 "$temporary/docker"
export PATH="$temporary:$PATH"
export FAKE_DOCKER_STATE_FILE="$state_file"
# shellcheck source=deploy/backup/docker-cleanup-state.sh
source "$project_root/deploy/backup/docker-cleanup-state.sh"

expect_status() {
  local want=$1
  shift
  local got
  set +e
  "$@"
  got=$?
  set -e
  [[ "$got" -eq "$want" ]] || {
    echo "unexpected cleanup state: got=$got want=$want command=$*" >&2
    exit 1
  }
}

FAKE_DOCKER_MODE=absent expect_status 0 docker_resource_absent container test-container
FAKE_DOCKER_MODE=absent expect_status 0 docker_resource_absent network test-network
FAKE_DOCKER_MODE=unknown expect_status 2 docker_resource_absent container test-container
FAKE_DOCKER_MODE=present expect_status 1 docker_resource_absent network test-network
FAKE_DOCKER_MODE=persistent expect_status 1 remove_docker_resource_strict container test-container
touch "$state_file"
FAKE_DOCKER_MODE=removable expect_status 0 remove_docker_resource_strict network test-network
[[ ! -e "$state_file" ]]
FAKE_DOCKER_MODE=unknown expect_status 2 remove_docker_resource_strict container test-container

printf '%s\n' 'Restore Docker cleanup three-state contract passed.'
