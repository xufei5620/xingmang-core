#!/usr/bin/env bash
# Static boundary checks for the DS1 disposable PostgreSQL harness.
set -Eeuo pipefail

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
compose="$root/tests/security/metric-rollup-cluster.compose.yaml"
script="$root/scripts/test-metric-rollup.ps1"

fail() { printf 'DS1 PG HARNESS TEST FAIL: %s\n' "$1" >&2; exit 1; }
[[ -f "$compose" ]] || fail "missing disposable compose"
[[ -f "$script" ]] || fail "missing harness script"
grep -Eq 'postgres:18@sha256:[0-9a-f]{64}' "$compose" || fail "postgres image is not digest pinned"
grep -Fq '127.0.0.1::5432' "$compose" || fail "port must be loopback and ephemeral"
if grep -Eiq '0\.0\.0\.0|network_mode:[[:space:]]*host|external:[[:space:]]*true|/var/lib/postgresql[^[:space:]]*:' "$compose"; then
  fail "compose exposes a wildcard/external/data bind"
fi
grep -Fq 'server_version_num' "$script" || fail "harness does not verify PG major"
grep -Fq 'shobj_description' "$script" || fail "harness does not verify database sentinel"
grep -Fq 'down' "$script" || fail "harness has no teardown"
grep -Fq 'metric_observation_daily' "$script" || fail "harness does not apply platform migrations"
echo 'metric rollup disposable PostgreSQL static-boundary checks passed'
