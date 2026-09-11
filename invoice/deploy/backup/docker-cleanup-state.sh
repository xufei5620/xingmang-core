#!/usr/bin/env bash

# Return 0 only when the named resource is absent and the Docker daemon is
# independently reachable, 1 when it still exists, and 2 when absence cannot
# be distinguished from a daemon/permission failure.
docker_resource_absent() {
  local kind=${1:?docker resource kind is required}
  local name=${2:?docker resource name is required}
  [[ "$kind" == container || "$kind" == network ]] || return 2
  if docker "$kind" inspect "$name" >/dev/null 2>&1; then
    return 1
  fi
  docker info >/dev/null 2>&1 || return 2
  # A reachable daemon does not make a denied/failed inspect mean not-found.
  # Require a successful inventory and compare the complete resource name.
  local names observed
  if [[ "$kind" == container ]]; then
    names=$(docker container ls --all --format '{{.Names}}') || return 2
  else
    names=$(docker network ls --format '{{.Name}}') || return 2
  fi
  while IFS= read -r observed; do
    [[ "$observed" != "$name" ]] || return 1
  done <<<"$names"
  return 0
}

remove_docker_resource_strict() {
  local kind=${1:?docker resource kind is required}
  local name=${2:?docker resource name is required}
  local state
  local attempt
  [[ "$kind" == container || "$kind" == network ]] || return 2
  for attempt in 1 2 3 4 5; do
    if [[ "$kind" == container ]]; then
      docker rm --force "$name" >/dev/null 2>&1 || true
    else
      docker network rm "$name" >/dev/null 2>&1 || true
    fi
    if docker_resource_absent "$kind" "$name"; then
      return 0
    else
      state=$?
    fi
    (( state == 2 )) && return 2
    sleep 1
  done
  return 1
}
