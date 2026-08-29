#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
migration="$root/db/migrations/000017_finance_runway_threshold_config.up.sql"
test -f "$migration"
grep -q "runway_threshold_ordered" "$migration"
grep -q "ERRCODE = '55000'" "$migration"
grep -q "BEFORE TRUNCATE" "$migration"
if grep -Eq 'GRANT .*history|REVOKE .*history' "$migration"; then
  echo 'role grants belong to the separately approved DB-role slice' >&2
  exit 1
fi
echo 'runway threshold schema static-boundary checks passed (ACL integration is not implemented)'
