sprint-section: 7.5

# XM-DBR1 verifier empty-ACL routine follow-up

## status

READY-FOLLOWUP（待验收线审读并人工合入；纯离线只读修复，无需部署）

- branch: `ai/codex/XM-DBR1-verifier-routine-null-fix`
- worktree: `K:/星芒统一控制平台/wt-xmDBR1-verifier-routine-null-fix`
- base release: `release/v0.1-launch@517408f92c42d147e9f8f0028f909ecf8c2f1ca5`
- request: DBR2 disposable PG18 rerun found a NULL `privilege_type` scan
  result for routines whose ACL is explicitly empty; fix the read-only
  verifier and add regression coverage without changing permission semantics
- delivery commit: branch tip (exact SHA in READY-FOLLOWUP line)

## scope / summary

`catalogRoutinesSQL` uses a `LEFT JOIN LATERAL aclexplode(...)`. When a
routine has an explicit empty ACL (`proacl = '{}'`), PostgreSQL returns the
left-join sentinel with `ax.privilege_type IS NULL`. Scanning that value into a
Go `string` failed the complete catalog read before verification could run.

The projection now uses `COALESCE(ax.privilege_type, '')` for routines. The
same mechanically equivalent normalization is applied to relation and type
ACL projections, which use the identical nullable left-join shape; the scanner
already treats an empty privilege as “no grant”, so no permission meaning
changes. The relation allowlist also explicitly includes the `audit` schema;
this preserves the policy's audit table inventory instead of silently omitting
those objects. PUBLIC principal handling and all named-role checks are
unchanged.

No migrations, policy/state-event artifacts, role operations, database
connections in application code, runtime wiring, compose files, credentials,
or release files were changed.

## files_changed

- `internal/platform/dbroles/verifier.go`
- `internal/platform/dbroles/verifier_test.go`
- `docs/handoffs/slices/XM-DBR1-verifier-routine-null-fix.md`

## implementation / safety

- `catalogRelationsSQL`, `catalogRoutinesSQL`, and `catalogTypesSQL` now
  normalize nullable `ax.privilege_type` to the existing empty-string sentinel.
- `CatalogQueryAllowlist` remains SELECT-only and continues to pass
  `pgreadonly.AssertSelectOnly` before execution.
- The scanner still skips empty `(grantee, privilege)` rows, preserving the
  prior ACL semantics while preventing a NULL-to-string scan error.

## TDD / verification evidence

- RED: before the change,
  `go test -p 1 ./internal/platform/dbroles -run
  TestRoutineCatalogQueryNormalizesEmptyACLPrivilegeRows -count=1` failed,
  reporting the routine query's raw `ax.privilege_type` projection.
- `go fmt ./internal/platform/dbroles` — PASS.
- `go test -p 1 ./internal/platform/dbroles -count=1` — PASS.
- `go test -race -p 1 ./internal/platform/dbroles -count=1` — PASS.
- `go vet ./internal/platform/dbroles` — PASS.
- `go test -p 1 ./internal/platform/dbroles ./cmd/db-role-verify -count=1` — PASS.
- `go test -race -p 1 ./internal/platform/dbroles ./cmd/db-role-verify -count=1` — PASS.
- `go vet ./internal/platform/dbroles ./cmd/db-role-verify` — PASS.
- `go test -p 1 ./... -count=1` — PASS (all Go packages).
- `pwsh -NoProfile -ExecutionPolicy Bypass -File scripts/test-database-roles.ps1` —
  PASS (`DBR1 HARNESS PASS`, PostgreSQL 18, random project/loopback port,
  positive+negative probes; harness destroyed its container/volume).
- `gitleaks.exe git --redact --no-banner --log-opts=517408f92c42d147e9f8f0028f909ecf8c2f1ca5..HEAD` — PASS after commit.
- `GOVERNANCE_BASE_REF=517408f92c42d147e9f8f0028f909ecf8c2f1ca5
  GOVERNANCE_REQUIRE_BASE=1 D:/Git/bin/bash.exe scripts/check-governance.sh` —
  PASS.
- `git diff --check 517408f92c42d147e9f8f0028f909ecf8c2f1ca5..HEAD` — PASS.
- Disposable PostgreSQL 18.6 probe (random loopback port, destroyed after
  the check): created `xm_migrator`, an owned `core` schema, and
  `core.empty_acl()`, then revoked all function privileges from both PUBLIC
  and the owner so `pg_proc.proacl = '{}'`. The exact routine query returned
  `core|<oid>|empty_acl()|xm_migrator|PUBLIC|` (a trailing empty privilege)
  without a scan/query error, proving the NULL sentinel is normalized.
- `TestRelationCatalogQueryIncludesAuditSchema` pins the complete relation
  schema allowlist, including `audit`, and prevents a recurrence of the
  `CATALOG_OBJECT_MISSING` drift seen in the DBR2 verifier rerun.

## intentionally_not_changed / not_run

- No SQL migration or `CREATE/ALTER/GRANT/REVOKE/SET ROLE` was committed; the
  disposable probe's catalog setup was torn down completely.
- No shared/staging/production database, real credentials, upstream, worker,
  API runtime, compose stack, or deployment was touched. This is a read-only
  verifier fix and does not require `deploy-local.sh`.
- Windows PowerShell 5's legacy parser reports encoding/quoting errors in the
  harness script; the supported PowerShell 7 (`pwsh`) invocation above passed.
- `contracts/database/*` and `docs/handoffs/ACCEPTANCE-LOG.md` remain
  unchanged; acceptance owns merge/release decisions.

## follow-ups / boundaries

1. Acceptance should rerun the DBR2 disposable PG18 verifier after this fix and
   confirm empty ACL rows for relation/type/routine objects no longer abort the
   snapshot read.
2. Keep `COALESCE(ax.privilege_type, '')` on any future left-join ACL query, or
   use an explicit nullable scanner; never scan an unguarded nullable catalog
   field into a non-nullable Go string.
3. DBR2 SQL and DBR3 runtime/DSN changes remain separate slices.

## acceptance handoff

`READY-FOLLOWUP ai/codex/XM-DBR1-verifier-routine-null-fix <commit>
docs/handoffs/slices/XM-DBR1-verifier-routine-null-fix.md`

Acceptance owns review, merge, and any release/deployment log entry. This
follow-up itself has no deployment step.
