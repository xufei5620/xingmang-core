#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
grep -q 'runway-threshold-bootstrap' "$root/deploy/docker/go.Dockerfile"
grep -q 'runway-threshold-bootstrap:' "$root/deploy/compose/launch.yaml"
grep -q 'profiles: \["tools"\]' "$root/deploy/compose/launch.yaml"
echo 'runway bootstrap lifecycle static wiring checks passed (disposable DB execution is not implemented)'
