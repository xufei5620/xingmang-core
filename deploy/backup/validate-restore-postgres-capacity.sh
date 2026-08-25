#!/usr/bin/env bash
set -Eeuo pipefail

size=${1:-}
host_available_bytes=${2:-}
docker_total_bytes=${3:-}

[[ "$size" =~ ^([89]|[12][0-9]|3[0-2])g$ ]] || {
  echo 'RESTORE_POSTGRES_TMPFS_SIZE must be an integer 8g..32g using a lowercase g suffix' >&2
  exit 2
}
size_gib=${BASH_REMATCH[1]}
[[ "$host_available_bytes" =~ ^[1-9][0-9]*$ ]] || {
  echo 'host MemAvailable is unavailable or invalid' >&2
  exit 2
}
[[ "$docker_total_bytes" =~ ^[1-9][0-9]*$ ]] || {
  echo 'Docker daemon memory total is unavailable or invalid' >&2
  exit 2
}

gib=$((1024*1024*1024))
tmpfs_bytes=$((size_gib*gib))
reserve_bytes=$((4*gib))
required_bytes=$((tmpfs_bytes+reserve_bytes))

(( host_available_bytes >= required_bytes )) || {
  echo "restore drill requires at least ${required_bytes} host-available bytes for ${size} tmpfs plus 4g reserve" >&2
  exit 1
}
(( docker_total_bytes >= required_bytes )) || {
  echo "restore drill requires Docker memory >= ${required_bytes} bytes for ${size} tmpfs plus 4g reserve" >&2
  exit 1
}

printf '%s\n' "$tmpfs_bytes"
