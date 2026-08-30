sprint-section: 7.5

# XM-DBR1 verifier PUBLIC ACL follow-up

## status

READY-FOLLOWUP（待验收线审读并人工合入；纯离线只读修复，无需部署）

- branch: `ai/codex/XM-DBR1-verifier-public-fix`
- worktree: `K:/星芒统一控制平台/wt-xmDBR1-verifier-public-fix`
- base release: `release/v0.1-launch@05ab0cb97f6a7d570cd404ff438f80e8d5babdd1`
- request: `2026-08-30T11:51Z REJECT-REQUEST` in
  `docs/handoffs/ACCEPTANCE-LOG.md` (fix the DBR1 catalog verifier's
  `PUBLIC` pseudo-role failure and add regression coverage)
- delivery commit: branch tip (the exact SHA is reported in the READY-FOLLOWUP
  line; acceptance should use `git rev-parse HEAD`)

## scope / summary

The DBR1 read-only verifier previously passed the string `PUBLIC` to
`has_database_privilege` and `has_schema_privilege`. PostgreSQL treats PUBLIC
as an ACL principal (OID 0), not as a role name for those helper signatures,
so the catalog scan failed with `role "PUBLIC" does not exist`.

This follow-up now:

- reads database and `public`-schema PUBLIC grants with
  `aclexplode(COALESCE(..., acldefault(...)))` and `ax.grantee = 0`;
- keeps the named capability-role `has_*_privilege` branches for effective
  role checks, and appends ACL-OID UNION branches so object-level PUBLIC grants
  remain visible to `VerifySnapshot` rather than being silently omitted;
- adds static query/allowlist regression assertions and a snapshot regression
  proving an injected PUBLIC object grant is reported as privilege drift;
- makes rotation transition validation deterministic by using
  `loadPolicyUnchecked` inside the pure helper. `ValidateTransition` already
  validates both policies against its explicit UTC `now`; calling `LoadPolicy`
  there incorrectly consulted wall-clock time and could skip closure checks
  after a fixture deadline.

No policy contract, state-event history, migration, role grant, connection,
runtime wiring, compose file, credential, or release file was changed.

## files_changed

- `internal/platform/dbroles/verifier.go`
- `internal/platform/dbroles/verifier_test.go`
- `internal/platform/dbroles/transition.go`
- `internal/platform/dbroles/transition_test.go`
- `docs/handoffs/slices/XM-DBR1-verifier-public-fix.md`

## query safety / semantics

- `catalogPublicDatabasePrivilegesSQL` and
  `catalogPublicSchemaPrivilegesSQL` are SELECT-only ACL-OID projections.
- `catalogSchemasSQL` and `catalogDatabaseACLsSQL` retain named-role
  effective-privilege rows and add a SELECT-only `UNION ALL` branch filtered by
  `ax.grantee = 0`, projecting the principal as `PUBLIC` for the existing
  snapshot shape.
- No `has_*_privilege('PUBLIC', ...)` call remains. `CatalogQueryAllowlist` is
  still checked with `pgreadonly.AssertSelectOnly` before any query executes.

## TDD / verification evidence

- RED: before the SQL change,
  `go test -p 1 ./internal/platform/dbroles -run
  TestPublicPrivilegeQueriesDoNotPassPublicPseudoRoleToPrivilegeFunctions
  -count=1` failed because `catalogPublicDatabasePrivilegesSQL` contained
  `has_database_privilege('PUBLIC', ...)`.
- GREEN: `go fmt ./internal/platform/dbroles` — PASS.
- `go test -p 1 ./internal/platform/dbroles -count=1` — PASS.
- `go test -race -p 1 ./internal/platform/dbroles -count=1` — PASS.
- `go vet ./internal/platform/dbroles` — PASS.
- `go test -p 1 ./... -count=1` — PASS (all Go packages).
- `go test -race -p 1 ./... -count=1` — PASS (all Go packages).
- `go vet ./...` — PASS.
- `GOVERNANCE_BASE_REF=7346a2a29396066a1a5c9cd953e3fabf3af901de
  GOVERNANCE_REQUIRE_BASE=1 D:/Git/bin/bash.exe scripts/check-governance.sh` —
  PASS. The base was an ancestor; release was rebased to
  `05ab0cb97f6a7d570cd404ff438f80e8d5babdd1` before final handoff.
- `git diff --check` — PASS before commit.
- A disposable PostgreSQL 18.6 container (random project/loopback port,
  destroyed after the check) executed the exact ACL projections. After
  `REVOKE ALL ... FROM PUBLIC`, then injecting `GRANT CREATE ON SCHEMA public
  TO PUBLIC` and `GRANT TEMPORARY ON DATABASE xingmang TO PUBLIC`, the queries
  returned `public_schema_privileges=CREATE` and
  `public_database_privileges=TEMPORARY`; no role-does-not-exist error occurred.
  The container was removed and no shared/staging/production DSN was used.

## intentionally_not_changed / not_run

- No SQL migration or `CREATE/ALTER/GRANT/REVOKE/SET ROLE` was committed; the
  disposable SQL check above was the only database activity.
- No shared compose stack, staging/production database, real upstream,
  credentials, SecretProvider, worker/API runtime, or deployment was touched;
  this slice has no runtime effect and therefore requires no `deploy-local.sh`.
- `contracts/database/*` and `ACCEPTANCE-LOG.md` remain unchanged; acceptance
  owns the merge/release decision.

## follow-ups / boundaries

1. Acceptance should rerun the verifier against its disposable DBR harness and
   inspect both PUBLIC summary fields and object-level schema/database rows.
2. DBR2 SQL remains a separate branch/slice; DBR3 DSN/compose/runtime wiring is
   explicitly out of scope until its local-cutover approval.
3. A future verifier query change must preserve the OID-0 PUBLIC branch and
   the SELECT-only allowlist; never reintroduce a `PUBLIC` role argument to
   `has_*_privilege`.

## acceptance handoff

`READY-FOLLOWUP ai/codex/XM-DBR1-verifier-public-fix <commit>
docs/handoffs/slices/XM-DBR1-verifier-public-fix.md`

Acceptance owns review, merge, and any release/deployment log entry. This
follow-up itself has no deployment step.
