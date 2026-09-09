sprint-section: 7.5

# XM-DBR1 verifier routine identity follow-up

## status

READY-FOLLOWUP（待验收线审读并人工合入；纯离线只读修复，无需部署）

- branch: `ai/codex/XM-DBR1-verifier-routine-identity-fix`
- worktree: `K:/星芒统一控制平台/wt-xmDBR1-verifier-routine-identity-fix`
- base release: `release/v0.1-launch@4d8fd2a490dd3816f57e4cd83fd93643dba3f2c3`
- request: DBR2 disposable PG18 verification showed
  `pg_get_function_identity_arguments` includes parameter names (for example
  `bitmask bit, state river_job_state`) while the approved policy identity is
  type-only (`bit, river_job_state`)
- delivery commit: branch tip (exact SHA in READY-FOLLOWUP line)

## scope / summary

The read-only routine catalog projection now derives a deterministic,
type-only identity from `pg_proc.proargtypes`:

- `unnest(p.proargtypes::oid[]) WITH ORDINALITY` preserves input argument order;
- `format_type(arg_oid, NULL)` emits canonical type names without parameter
  names or typmods;
- `string_agg(..., ', ' ORDER BY ord)` joins overload signatures, and an empty
  `COALESCE` produces `routine()` for zero-argument functions.

This makes the PG18 River routine resolve to the policy key
`river_job_state_in_bitmask(bit, river_job_state)` without changing policy
artifacts or privilege semantics. The existing empty-ACL `COALESCE` and all
schema allowlists (including `audit`) remain intact.

No migration, policy/state-event contract, role operation, runtime wiring,
compose file, credential, or release file was changed.

## files_changed

- `internal/platform/dbroles/verifier.go`
- `internal/platform/dbroles/verifier_test.go`
- `docs/handoffs/slices/XM-DBR1-verifier-routine-identity-fix.md`

## query safety / compatibility

- `catalogRoutinesSQL` remains a single SELECT in `CatalogQueryAllowlist` and
  passes `pgreadonly.AssertSelectOnly`.
- The type-only subquery reads `pg_proc.proargtypes`/`format_type` only; it does
  not execute or mutate routines.
- Existing ACL owner/grantee/privilege columns and empty-privilege sentinel are
  unchanged.

## TDD / verification evidence

- RED: before implementation,
  `go test -p 1 ./internal/platform/dbroles -run
  TestRoutineCatalogQueryUsesTypeOnlyIdentityArguments -count=1` failed because
  the query called `pg_get_function_identity_arguments`.
- `go fmt ./internal/platform/dbroles` — PASS.
- `go test -p 1 ./internal/platform/dbroles ./cmd/db-role-verify -count=1` — PASS.
- `go test -race -p 1 ./internal/platform/dbroles ./cmd/db-role-verify -count=1` — PASS.
- `go vet ./internal/platform/dbroles ./cmd/db-role-verify` — PASS.
- `go test -p 1 ./... -count=1` — PASS (all Go packages).
- Disposable PostgreSQL 18.6 probe (random loopback port, destroyed after the
  check): created `river_job_state` and
  `river_job_state_in_bitmask(bitmask BIT(8), state river_job_state)`. The exact
  projection returned `river_job_state_in_bitmask(bit, river_job_state)` for
  both ACL rows, with no parameter names and no query error.
- DBR2-style disposable verifier: in a separate temporary evidence worktree,
  loaded the DBR2-sql policy inventory, applied platform migrations through
  `000019`, all River up migrations, and DBR2 SQL `001/002/003` to a random
  PostgreSQL 18 container. Running this branch's `cmd/db-role-verify` with the
  password-free loopback DSN and `--now 2026-08-30T10:30:00Z` printed
  `DB ROLE POLICY MATCH` and exited `0`. The evidence worktree and container
  were removed immediately afterward; no DBR2 files were committed here.
- `pwsh -NoProfile -ExecutionPolicy Bypass -File scripts/test-database-roles.ps1`
  — PASS (`DBR1 HARNESS PASS`, PostgreSQL 18, random project/loopback port,
  positive+negative probes; harness destroyed its resources).
- `gitleaks.exe git --redact --no-banner --log-opts=4d8fd2a490dd3816f57e4cd83fd93643dba3f2c3..HEAD` — PASS.
- `GOVERNANCE_BASE_REF=4d8fd2a490dd3816f57e4cd83fd93643dba3f2c3
  GOVERNANCE_REQUIRE_BASE=1 D:/Git/bin/bash.exe scripts/check-governance.sh` —
  PASS.
- `git diff --check 4d8fd2a490dd3816f57e4cd83fd93643dba3f2c3..HEAD` — PASS.

## intentionally_not_changed / not_run

- No SQL migration or `CREATE/ALTER/GRANT/REVOKE/SET ROLE` was committed; the
  PG18 probe was disposable and fully torn down.
- No shared/staging/production database, real credentials, upstream, worker,
  API runtime, compose stack, or deployment was touched; this slice has no
  runtime effect and does not require `deploy-local.sh`.
- `contracts/database/*` and `docs/handoffs/ACCEPTANCE-LOG.md` remain unchanged;
  acceptance owns merge/release decisions.

## follow-ups / boundaries

1. Acceptance should rerun `db-role-verify` on the DBR2 PG18 catalog and verify
   the River routine is neither missing nor unknown after type-only projection.
2. Keep policy routine identities type-only; if overload canonicalization needs
   variadic/OUT-argument semantics later, add an explicit contract version and
   regression rather than reverting to parameter-name-dependent output.
3. DBR2 SQL and DBR3 runtime/DSN work remain separate slices.

## acceptance handoff

`READY-FOLLOWUP ai/codex/XM-DBR1-verifier-routine-identity-fix <commit>
docs/handoffs/slices/XM-DBR1-verifier-routine-identity-fix.md`

Acceptance owns review, merge, and any release/deployment log entry. This
follow-up itself has no deployment step.
