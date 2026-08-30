#!/usr/bin/env bash
# DBR1 disposable-cluster gate.
#
# This test intentionally exercises the harness in validation-only mode first:
# no external DSN is accepted, the compose file must pin PostgreSQL by the
# VERSIONS.lock digest, and the only host mapping is a Docker-assigned
# loopback port.  A full disposable run is performed by
# scripts/test-database-roles.ps1 when Docker is available.
set -Eeuo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
script="$repo_root/scripts/test-database-roles.ps1"
compose="$repo_root/tests/security/database-role-cluster.compose.yaml"
fixture="$repo_root/tests/security/fixtures/database-role-fixture.sql"

fail() {
  printf 'DBR1 SECURITY TEST FAIL: %s\n' "$1" >&2
  exit 1
}

[[ -f "$script" ]] || fail "缺少 scripts/test-database-roles.ps1"
[[ -f "$compose" ]] || fail "缺少 disposable compose 文件"
[[ -f "$fixture" ]] || fail "缺少 role fixture SQL"

grep -Eq 'postgres:18@sha256:[0-9a-f]{64}' "$compose" \
  || fail "compose 未钉住 postgres:18 RepoDigest"
grep -Fq '127.0.0.1::5432' "$compose" \
  || fail "compose 必须只发布 127.0.0.1::5432"
if grep -Eq '(^|[[:space:]])(0\.0\.0\.0|::):|127\.0\.0\.1:[0-9]+:5432' "$compose"; then
  fail "compose 含 wildcard 或固定 host 端口"
fi
if grep -Eiq 'external:[[:space:]]*true|network_mode:[[:space:]]*host|/var/lib/postgresql[^[:space:]]*:[^[:space:]]*' "$compose"; then
  fail "compose 不得复用外部网络或 bind-mounted 数据目录"
fi
grep -Fq 'PGHOST' "$script" || fail "脚本缺少内部连接构造守卫"
grep -Fq 'RequireLoopback' "$script" || fail "脚本未调用 pgdsn.RequireLoopback"
grep -Fq 'Validate' "$script" || fail "脚本未调用 pgdsn.Validate"
grep -Fq 'down --volumes --remove-orphans' "$script" \
  || fail "脚本缺少按项目销毁的 finally 路径"
grep -Fq 'com.docker.compose.project' "$script" \
  || fail "脚本未按本次 project label 复核资源"
grep -Fq 'server_version_num' "$script" || fail "脚本未验证 PostgreSQL major"

if command -v pwsh >/dev/null 2>&1; then
  # ValidationOnly must not create a project, connect to a database, or read a
  # caller-provided DSN.  It is also the fast path used by governance CI.
  pwsh -NoProfile -File "$script" -ValidateOnly -RepoRoot "$repo_root" \
    >/tmp/xm-dbr1-policy.out 2>/tmp/xm-dbr1-policy.err \
    || { cat /tmp/xm-dbr1-policy.err >&2; fail "ValidateOnly 失败"; }
  grep -Fq 'DBR1 VALIDATION PASS' /tmp/xm-dbr1-policy.out \
    || fail "ValidateOnly 未输出通过标记"
  rm -f /tmp/xm-dbr1-policy.out /tmp/xm-dbr1-policy.err
else
  echo 'DBR1 SECURITY TEST NOTICE: pwsh 不可用，跳过 ValidateOnly（Windows 门禁会执行）'
fi

echo 'database role disposable harness static-boundary checks passed'
