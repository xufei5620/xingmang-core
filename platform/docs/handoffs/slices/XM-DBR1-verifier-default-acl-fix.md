sprint-section: 7.5

# XM-DBR1 verifier PG18 default-ACL follow-up

## status

READY-FOLLOWUP（待验收线审读并人工合入；纯离线只读修复，无需部署）

- branch: `ai/codex/XM-DBR1-verifier-default-acl-fix`
- worktree: `K:/星芒统一控制平台/wt-xmDBR1-default-acl-fix`
- base release: `release/v0.1-launch@eeebf955e877c049608805107d2e3553b35618ca`
- request: acceptance follow-up after DBR2 disposable PG18 verification found
  `pg_default_acl.defaclobjtype='T'` represents both type and domain families
  while the verifier emitted only `type`
- delivery commit: branch tip (the exact SHA is reported in the
  READY-FOLLOWUP line; acceptance should use `git rev-parse HEAD`)

## scope / summary

PostgreSQL exposes default ACLs for types and domains through the shared
`defaclobjtype='T'` catalog family. DBR1 policy v1 intentionally keeps `type`
and `domain` as separate logical families, so the previous projection produced
one family and made the verifier report a false `CATALOG_DEFAULT_ACL_MISSING`
for every domain family.

This follow-up adds a small pure projection helper used by
`ReadCatalogSnapshot`:

- `T` rows are materialized as both logical `type` and `domain` entries;
- the same grants are mirrored to both entries;
- the LEFT-JOIN empty-ACL sentinel still creates both family entries without
  inventing a privilege;
- existing `r`, `S`, `f`, and `d` mappings remain unchanged.

No SQL text, migrations, policy/event artifacts, role grants, DB connections,
runtime wiring, compose files, credentials, or release files were changed.

## files_changed

- `internal/platform/dbroles/verifier.go`
- `internal/platform/dbroles/verifier_test.go`
- `docs/handoffs/slices/XM-DBR1-verifier-default-acl-fix.md`

## implementation details

- `projectDefaultACLRow` centralizes the catalog-row projection and preserves
  the existing normalized map behavior.
- `defaultACLKinds("T")` returns `[]string{"type", "domain"}`; all other
  object-kind codes continue through `defaultACLKind`.
- Empty rows (`grantee=PUBLIC`, `privilege=''`) initialize both map entries but
  do not add a grant. This matches the existing `verifyDefaultACLs` semantics
  where empty role arrays are equivalent to absent empty grants.

## TDD / verification evidence

- Regression test: `TestDefaultACLProjectionExpandsPG18TypeFamilyAndEmptyRows`
  asserts both logical entries are created from an empty `T` row and that a
  worker `USAGE` grant is mirrored to both type and domain.
- `go fmt ./internal/platform/dbroles` — PASS.
- `go test -p 1 ./internal/platform/dbroles -count=1` — PASS.
- `go test -race -p 1 ./internal/platform/dbroles -count=1` — PASS.
- `go vet ./internal/platform/dbroles` — PASS.
- `go test -p 1 ./internal/platform/dbroles ./cmd/db-role-verify -count=1` — PASS.
- `go test -race -p 1 ./internal/platform/dbroles ./cmd/db-role-verify -count=1` — PASS.
- `go vet ./internal/platform/dbroles ./cmd/db-role-verify` — PASS.
- `go test -p 1 ./... -count=1` — PASS (all Go packages).
- `gitleaks.exe git --redact --no-banner --log-opts=eeebf955e877c049608805107d2e3553b35618ca..HEAD` — PASS (1 commit, no leaks).
- `GOVERNANCE_BASE_REF=eeebf955e877c049608805107d2e3553b35618ca
  GOVERNANCE_REQUIRE_BASE=1 D:/Git/bin/bash.exe scripts/check-governance.sh` —
  PASS.
- `git diff --check eeebf955e877c049608805107d2e3553b35618ca..HEAD` — PASS.
- Disposable PostgreSQL 18.6 evidence (random loopback port, container
  destroyed after the check): creating `xm_migrator` and a `core` schema, then
  running `ALTER DEFAULT PRIVILEGES FOR ROLE xm_migrator IN SCHEMA core GRANT
  USAGE ON TYPES TO xm_worker_runtime` returned
  `T|core|1` from `pg_default_acl`. This confirms the PG18 catalog shape that
  the projection handles; no shared/staging/production DSN was used.
- `git diff --check` — PASS before commit.

## intentionally_not_changed / not_run

- No migration or SQL/ACL operation was committed. The one disposable PG18
  check was catalog setup only and was fully torn down.
- No shared compose stack, staging/production database, real upstream,
  credentials, SecretProvider, worker/API runtime, or deployment was touched;
  this slice has no runtime effect and does not require `deploy-local.sh`.
- `contracts/database/*` and `docs/handoffs/ACCEPTANCE-LOG.md` remain
  unchanged; acceptance owns merge/release decisions.
- The repository's existing `scripts/test-database-roles.ps1` full-harness
  invocation was not used here because Windows PowerShell reports pre-existing
  parser/encoding errors around its later query assertions; the direct PG18
  catalog probe above is the scoped runtime evidence for this read-only fix.

## follow-ups / boundaries

1. Acceptance should rerun `db-role-verify` against the disposable DBR1 PG18
   harness and confirm the default-ACL count includes both logical type and
   domain families for each schema.
2. DBR2 SQL remains a separate slice; this follow-up does not add owner/ACL
   migrations or runtime DSN wiring.
3. If a future PostgreSQL catalog version introduces another combined
   `defaclobjtype`, add an explicit logical projection and regression test
   rather than silently dropping a policy family.

## acceptance handoff

`READY-FOLLOWUP ai/codex/XM-DBR1-verifier-default-acl-fix <commit>
docs/handoffs/slices/XM-DBR1-verifier-default-acl-fix.md`

Acceptance owns review, merge, and any release/deployment log entry. This
follow-up itself has no deployment step.
