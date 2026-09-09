#!/usr/bin/env bash
# AUD2 runtime wiring boundary checks.
#
# This slice intentionally ships a manual-only worker seam and a separate,
# loopback MinIO fixture. The checks prevent an accidental scheduler/production
# activation from hiding in Compose or the frozen job manifest.
set -Eeuo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
compose="$repo_root/deploy/compose/archive.yaml"
contract="$repo_root/contracts/jobs/audit-archive-manual.v1.json"

fail() { printf 'AUD2 ARCHIVE RUNTIME TEST FAIL: %s\n' "$1" >&2; exit 1; }

[[ -f "$compose" ]] || fail "missing deploy/compose/archive.yaml"
[[ -f "$contract" ]] || fail "missing manual archive contract"

grep -Fq 'name: xingmang-archive' "$compose" || fail "compose project must be xingmang-archive"
grep -Eq 'docker\.io/minio/minio:[^[:space:]@]+@sha256:[0-9a-f]{64}' "$compose" \
  || fail "MinIO image must use an immutable digest"
lock_image="$(awk -F'= *' '/^minio\.image[[:space:]]*=/{print $2; exit}' "$repo_root/VERSIONS.lock")"
lock_digest="$(awk -F'= *' '/^minio\.digest[[:space:]]*=/{print $2; exit}' "$repo_root/VERSIONS.lock")"
[[ -n "$lock_image" && -n "$lock_digest" ]] || fail "VERSIONS.lock lacks MinIO image/digest"
grep -Fq "image: ${lock_image}@${lock_digest}" "$compose" \
  || fail "archive compose MinIO reference differs from VERSIONS.lock"
grep -Fq '127.0.0.1::9000' "$compose" || fail "MinIO host mapping must be loopback + ephemeral"
if grep -Eq '(^|[[:space:]])(0\.0\.0\.0|::):|127\.0\.0\.1:[0-9]+:9000' "$compose"; then
  fail "archive compose contains wildcard or fixed host port"
fi
if grep -Eiq 'external:[[:space:]]*true|network_mode:[[:space:]]*host' "$compose"; then
  fail "archive compose must own an isolated network and volume"
fi
grep -Fq 'com.xingmang.archive.fixture' "$compose" || fail "fixture marker missing"
grep -Fq 'production: "false"' "$compose" || fail "production=false marker missing"
grep -Fq 'secret://archive/minio-kms' "$compose" || fail "KMS CredentialRef missing"
grep -Fq 'secret://archive/minio-runtime' "$compose" || fail "object-writer CredentialRef missing"
grep -Fq 'secret://archive/minio-qualification' "$compose" || fail "qualification CredentialRef missing"
grep -Fq 'secret://archive/security-sink' "$compose" || fail "security sink CredentialRef missing"
if grep -Eiq '(^|[=:])[[:space:]]*(latest|changeme|password|secret|token)[[:space:]]*$' "$compose"; then
  fail "compose contains a credential-like literal"
fi
if grep -Eq '^  postgres:' "$compose"; then
  fail "archive project must not include platform PostgreSQL"
fi

grep -Fq '"scheduler_registered": false' "$contract" || fail "contract must freeze scheduler_registered=false"
grep -Fq '"default_enabled": false' "$contract" || fail "contract must freeze default_enabled=false"
grep -Fq '"mode": "manual"' "$contract" || fail "contract must freeze manual mode"
grep -Fq 'secret://archive/minio-kms' "$contract" || fail "contract KMS ref missing"
grep -Fq 'secret://archive/minio-runtime' "$contract" || fail "contract object-writer ref missing"
grep -Fq 'secret://archive/minio-qualification' "$contract" || fail "contract qualification ref missing"
grep -Fq 'secret://archive/security-sink' "$contract" || fail "contract security sink ref missing"

printf 'audit archive runtime wiring static-boundary checks passed\n'
