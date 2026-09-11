#!/usr/bin/env bash
set -Eeuo pipefail
project_root=${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}
preflight=$(sed -n '/^tag=$(sed /,/^for image in /{ /^for image in /!p; }' "$project_root/deploy/roll-forward.sh")
temporary=$(mktemp -d)
trap 'rm -rf -- "$temporary"' EXIT
printf 'INVOICE_IMAGE_TAG=0.1.0-rc110\n' >"$temporary/synthetic.env"
env_file="$temporary/synthetic.env"
failures=0
for mode in unset matching conflicting empty; do
  [[ -z "${IMAGE_TAG_CASE:-}" || "$IMAGE_TAG_CASE" == "$mode" ]] || continue
  set +e
  output=$(
    set -euo pipefail
    exec 2>&1
    unset INVOICE_IMAGE_TAG
    case "$mode" in matching) export INVOICE_IMAGE_TAG=0.1.0-rc110;; conflicting) export INVOICE_IMAGE_TAG=0.1.0-rc109;; empty) export INVOICE_IMAGE_TAG='';; esac
    eval "$preflight"
    bash -c 'printf "effective=%s\n" "${INVOICE_IMAGE_TAG:-missing}"'
  )
  actual=$?
  set -e
  if [[ "$mode" == unset || "$mode" == matching ]]; then
    if [[ "$actual" != 0 || "$output" != effective=0.1.0-rc110 ]]; then echo "FAIL $mode: tag not bound for Compose" >&2; failures=$((failures+1)); fi
  elif [[ "$actual" == 0 ]]; then echo "FAIL $mode: conflicting exported tag accepted" >&2; failures=$((failures+1)); fi
done
(( failures == 0 )) || exit 1
echo 'Roll-forward image-tag boundary passed.'
