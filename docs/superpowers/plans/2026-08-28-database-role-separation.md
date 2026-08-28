# XM-C Database Role Separation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.
>
> **Approval gate:** This plan is an approval artifact, not implementation or deployment authorization.
> DBR1～DBR4 each require an explicit named approval, dedicated worktree/branch/PR, review, and human merge.
> Role/owner SQL, real credentials, staging execution, legacy lockdown, and production cutover each have a
> separate human STOP. Codex never executes live database changes, handles real password values, merges, or deploys.

**Goal:** Replace the shared PostgreSQL superuser/owner runtime with a verifiable migrator-owner plus
least-privilege API, worker, lifecycle, ops, and backup identities without changing business behavior.

**Architecture:** Grants attach to stable NOLOGIN capability roles; containers use independently rotating
LOGIN identities. `xm_migrator` alone owns platform/River objects, while machine-readable policy and a
fail-closed verifier prove current roles, memberships, owners, PUBLIC/default ACLs, positive capabilities,
and prohibited operations on disposable PostgreSQL 18 before any staging cutover.

**Tech Stack:** Go 1.27, PostgreSQL 18, pgx/sqlc, Docker Compose, Docker Secrets, SOPS/age, PowerShell,
Bash, existing `internal/platform/pgdsn` and `internal/platform/secrets`.

**Spec:** `docs/superpowers/specs/2026-08-28-database-role-separation-design.md`

## Global Constraints

- DBR0/DBR1 may be approved; all live cutover is currently NO-GO.
- Do not touch production, Keycloak, upstream systems, real passwords, or a non-loopback database.
- Do not push directly to `main`/`release/v0.1-launch`; never merge or deploy.
- One DBR slice = one owner, worktree, branch, PR, review, and Handoff.
- DBR1 connects only to a disposable loopback PostgreSQL 18 and drops random test roles as well as its DB.
- Role/owner/GRANT SQL is a Platform Lifecycle Operation and needs an exact separate migration approval.
- CredentialRef names are committed; values are human-configured and never enter repo/log/test output.
- Staging and production credentials are independent; staging approval never implies production approval.
- Never use broad `REASSIGN OWNED`; transfer only the approved target database/object manifest.
- Runtime roles never receive owner, superuser, CREATEROLE, CREATEDB, REPLICATION, BYPASSRLS, or grant options.
- PUBLIC loses database CONNECT/TEMP, public-schema access, and default routine EXECUTE as specified by the policy.
- Tables, sequences, schemas, routines, role memberships, and default privileges are all verified separately.
- API/worker behavior, Action/Query semantics, River, retention, audit chain, and RUNWAY evidence must not regress.
- `go fmt`, never bare `gofmt`; use `go test -p 1 ./...` on this Windows host.

## File Structure

### DBR1 — policy, verifier, disposable PG18

- Create: `contracts/database/role-policy.v1.json`
- Create: `internal/platform/dbroles/policy.go`
- Create: `internal/platform/dbroles/policy_test.go`
- Create: `internal/platform/dbroles/verifier.go`
- Create: `internal/platform/dbroles/verifier_integration_test.go`
- Create: `cmd/db-role-verify/main.go`
- Create: `cmd/db-role-verify/main_test.go`
- Create: `scripts/test-database-roles.ps1`
- Create: `tests/security/database-role-policy.test.sh`
- Modify: `.github/workflows/ci.yml`
- Modify: `scripts/check-governance.sh`
- Modify: `scripts/guard-governance-files.sh`
- Modify: `docs/modules/audit/DATA-MODEL.md`
- Modify: `docs/modules/ops/README.md`

### DBR2 — owner/migrator/lifecycle artifacts

- Create: `deploy/database/001_create_role_capabilities.sql`
- Create: `deploy/database/002_transfer_target_owners_and_grants.sql`
- Create: `deploy/database/003_inverse_runtime_grants.sql`
- Create: `deploy/database/verify-role-policy.ps1`
- Create: `tests/security/database-owner-manifest.test.sh`
- Create: `internal/platform/dbconn/config.go`
- Create: `internal/platform/dbconn/config_test.go`
- Modify: `cmd/platform-worker/database.go`
- Modify: `cmd/platform-worker/database_test.go`
- Modify: `cmd/platform-api/database.go`
- Modify: `cmd/platform-api/database_test.go`
- Modify: `deploy/docker/go.Dockerfile`
- Modify: `deploy/docker/migrate-entrypoint.sh`
- Modify: `deploy/compose/launch.yaml`
- Modify: `deploy/compose/.env.example`
- Create: `docs/runbooks/SWITCH-DATABASE-ROLES.md`

### DBR3 — API/worker cutover contract

- Modify: `deploy/compose/launch.yaml`
- Modify: `deploy/compose/.env.example`
- Modify: `cmd/platform-api/README.md`
- Modify: `cmd/platform-worker/README.md`
- Create: `tests/security/database-runtime-isolation.test.sh`
- Create: `internal/platform/httpapi/database_role_integration_test.go`
- Create: `internal/platform/jobs/database_role_integration_test.go`
- Modify: `docs/runbooks/SWITCH-DATABASE-ROLES.md`
- Modify: `docs/runbooks/LAUNCH.md`

### DBR4 — ops/backup, recovery, legacy lockdown

- Modify: `cmd/audit-verify/main.go`
- Create: `cmd/audit-verify/database_test.go`
- Modify: `cmd/platform-shadow/main.go`
- Create: `cmd/platform-shadow/database_test.go`
- Create: `deploy/docker/database-ops-entrypoint.sh`
- Modify: `deploy/docker/go.Dockerfile`
- Modify: `deploy/compose/launch.yaml`
- Modify: `deploy/compose/.env.example`
- Create: `tests/security/database-backup-restore.test.sh`
- Create: `tests/security/database-legacy-lockdown.test.sh`
- Modify: `docs/modules/audit/RUNBOOK.md`
- Modify: `docs/runbooks/secrets.md`
- Modify: `docs/runbooks/SWITCH-DATABASE-ROLES.md`

---

## DBR1 — Policy, Verifier, and Disposable PostgreSQL 18

### Task 1: Freeze the machine-readable policy

**Files:**
- Create: `contracts/database/role-policy.v1.json`
- Create: `internal/platform/dbroles/policy.go`
- Test: `internal/platform/dbroles/policy_test.go`

**Interfaces:**
- Consumes: design spec §§5–8.
- Produces: `dbroles.Policy`, `dbroles.RoleSpec`, `dbroles.ObjectGrant`, and exact policy version `1`.

- [ ] **Step 1: Write failing policy tests**

Assert exact capability roles, initial login memberships, role attributes, database/schema privileges,
all existing tables/sequences/routines, PUBLIC revocations, custom-schema default deny, River/public defaults,
and the RUNWAY conditional objects. Test these invariants explicitly:

```go
if policy.Roles["xm_api_runtime"].Login {
    t.Fatal("capability role must be NOLOGIN")
}
if grant := policy.Grant("audit", "audit_event", "xm_api_runtime");
    !slices.Equal(grant, []string{"INSERT", "SELECT"}) {
    t.Fatalf("audit_event API grant=%v", grant)
}
if policy.Allows("xm_worker_runtime", "action.action_run", "INSERT") {
    t.Fatal("worker must not write ActionRun")
}
```

- [ ] **Step 2: Confirm RED**

```powershell
go test -p 1 ./internal/platform/dbroles -run Policy -count=1
```

Expected: FAIL because the package/policy does not exist.

- [ ] **Step 3: Add JSON contract and typed loader**

The JSON includes:

```json
{
  "version": "1",
  "database": "xingmang",
  "owner": "xm_migrator",
  "capability_roles": [
    "xm_api_runtime",
    "xm_worker_runtime",
    "xm_lifecycle_runtime",
    "xm_ops_read",
    "xm_backup_read"
  ],
  "public_database_privileges": [],
  "public_schema_privileges": [],
  "custom_schema_default": "deny",
  "public_schema_contract": "river-only"
}
```

Object grants repeat the complete design matrix; no `ALL`, wildcard relation, or omitted privilege field.
Unknown role/object kind/privilege makes `LoadPolicy` fail.

- [ ] **Step 4: Run GREEN and commit DBR1 policy**

```powershell
go fmt ./internal/platform/dbroles
go test -p 1 ./internal/platform/dbroles -run Policy -count=1
git add contracts/database/role-policy.v1.json internal/platform/dbroles/policy.go `
  internal/platform/dbroles/policy_test.go
git commit -m "test(database): define least-privilege role policy"
```

### Task 2: Build a fail-closed catalog verifier

**Files:**
- Create: `internal/platform/dbroles/verifier.go`
- Create: `internal/platform/dbroles/verifier_integration_test.go`
- Create: `cmd/db-role-verify/main.go`
- Create: `cmd/db-role-verify/main_test.go`

**Interfaces:**
- Consumes: `dbroles.Policy`, pgx read-only catalog connection.
- Produces: deterministic violations and CLI exit `0=match`, `1=policy violation`, `2=config/connection error`.

- [ ] **Step 1: Write RED tests for every evidence family**

Create fixtures for: superuser runtime, owner runtime, wrong membership options, direct login grant, PUBLIC CONNECT,
schema CREATE, missing/extra table privilege, sequence privilege, routine EXECUTE, default ACL drift, unknown owner,
RUNWAY table present/absent, and blank application_name. Each violation has stable code and object identity, never DSN.

- [ ] **Step 2: Implement read-only catalog checks**

Query only `pg_roles`, `pg_auth_members`, `pg_database`, `pg_namespace`, `pg_class`, `pg_proc`,
`pg_default_acl`, `information_schema.role_*_grants`, `pg_stat_activity`, and `has_*_privilege`.
The verifier executes no CREATE/GRANT/ALTER/SET ROLE.

- [ ] **Step 3: Add CLI safety**

CLI accepts `DATABASE_URL`, uses `pgdsn.Validate`, scrubs logs, emits JSON/text without passwords, and verifies the
current database name. `--policy` defaults to the committed contract path; unknown policy version fails.

- [ ] **Step 4: Run GREEN and commit verifier**

```powershell
go fmt ./internal/platform/dbroles ./cmd/db-role-verify
go test -p 1 ./internal/platform/dbroles ./cmd/db-role-verify -count=1
git add internal/platform/dbroles cmd/db-role-verify
git commit -m "feat(database): verify role and grant policy"
```

### Task 3: Add the disposable PG18 harness and CI gate

**Files:**
- Create: `scripts/test-database-roles.ps1`
- Create: `tests/security/database-role-policy.test.sh`
- Modify: `.github/workflows/ci.yml`
- Modify: `scripts/check-governance.sh`
- Modify: `scripts/guard-governance-files.sh`

**Interfaces:**
- Consumes: admin DSN on loopback PostgreSQL 18 and DBR1 policy/verifier.
- Produces: random disposable database/roles, positive/negative permission evidence, complete cleanup.

- [ ] **Step 1: Write harness guard tests**

Assert rejection of non-loopback host, `?host=` override, non-PostgreSQL URL, target business DB, fixed role name,
and missing CREATEDB/CREATEROLE. Reuse `pgdsn.Validate` and `pgdsn.RequireLoopback`; string-prefix checks are forbidden.

- [ ] **Step 2: Implement random lifecycle and cleanup**

Names use one UUID suffix. `finally` always attempts:

1. terminate only matching test DB sessions;
2. drop only the exact random DB;
3. clear test-role membership/default ACL/owned entries;
4. drop every exact random login/capability role;
5. report all cleanup failures and exit nonzero.

- [ ] **Step 3: Prove positive and negative ACLs**

Run all design §12 assertions. For append-only, use separate probes:

- runtime role → SQLSTATE `42501` (ACL layer);
- DML-capable trigger/rule probe → statement accepted by ACL but row unchanged/stable trigger rejection.

- [ ] **Step 4: Wire CI without replacing existing integration DSN**

Keep `XM_TEST_DATABASE_URL` for existing owner-based setup tests. Add a separate role-policy step using the
loopback admin URL; run DBR verifier and negative probes after migrations. CI secrets remain test literals only.

- [ ] **Step 5: Add governance rules**

Guard the role policy/verifier/scripts. Fail when a new migration creates table/sequence/routine without an exact
grant clause or `no-runtime-access` marker; fail when ordinary business objects are created in public.

- [ ] **Step 6: Run DBR1 gates and commit**

```powershell
pwsh -File scripts/test-database-roles.ps1
bash tests/security/database-role-policy.test.sh
go test -p 1 ./internal/platform/dbroles ./cmd/db-role-verify -count=1
bash scripts/check-governance.sh
git add scripts/test-database-roles.ps1 tests/security/database-role-policy.test.sh `
  .github/workflows/ci.yml scripts/check-governance.sh scripts/guard-governance-files.sh
git commit -m "test(database): enforce role isolation on postgres 18"
```

### Task 4: Deliver DBR1 Handoff and STOP

Document current evidence, disposable DB/role names redacted to suffixes, full tests, policy violations found,
and state explicitly: no staging/production/real credential access. Open DBR1 PR and stop; approval does not permit DBR2.

---

## DBR2 — Migrator Owner and Lifecycle Foundation

### Task 5: Draft exact role/owner/ACL SQL, then STOP for migration approval

**Files:**
- Create: `deploy/database/001_create_role_capabilities.sql`
- Create: `deploy/database/002_transfer_target_owners_and_grants.sql`
- Create: `deploy/database/003_inverse_runtime_grants.sql`
- Create: `tests/security/database-owner-manifest.test.sh`

**Interfaces:**
- Consumes: merged DBR1 policy/verifier and latest target database inventory.
- Produces: exact lifecycle SQL review packet; no database application.

- [ ] **Step 1: Refresh refs and catalog evidence**

Fetch latest base, recalculate migration inventory, run #97 evidence and DBR1 verifier read-only, inventory every
database/schema/table/sequence/routine/default ACL. If unknown object/schema appears, stop and extend policy first.

- [ ] **Step 2: Generate explicit SQL**

`001` creates capability/login roles with PASSWORD NULL and minimum attributes. `002` lists explicit
`ALTER DATABASE/SCHEMA/TABLE/SEQUENCE/ROUTINE ... OWNER TO xm_migrator`, PUBLIC revocations,
object grants, column-level chain-root update, and defaults. It contains no REASSIGN OWNED, wildcard target DB,
password, production hostname, or DROP database. `003` restores only runtime grants for emergency rollback;
it does not restore shared superuser ownership.

- [ ] **Step 3: Produce approval packet**

Include exact SQL diff, before/after owner manifest, lock assessment, PUBLIC/default ACL diff, inverse-grant path,
backup/restore requirement, and DBR1 disposable evidence.

- [ ] **Step 4: STOP**

Do not apply to disposable/staging/production until the exact SQL receives separate migration approval. Any base
or inventory change invalidates approval and requires a refreshed packet.

### Task 6: Validate approved SQL only on disposable PG18

After approval, use DBR1 harness to apply the exact scripts. Run full policy, Action/audit rule, retention,
River, migration up/no-op, and inverse-grant tests. Prove `postgres/template*/xm0050_test` are untouched in a
fixture mirroring current shared ownership. Commit only after all evidence is green.

### Task 7: Add shared CredentialRef-based DB assembly

**Files:**
- Create: `internal/platform/dbconn/config.go`
- Create: `internal/platform/dbconn/config_test.go`
- Modify: API/worker database helpers and tests.

Implement one shared builder that validates effective host/query params, resolves `DATABASE_PASSWORD_REF`
through audited DockerSecretProvider, injects password only into pgx config, requires explicit user/application_name,
and has no env/file fallback. Preserve existing development-only behavior only where the approved contract says so.

Run RED/GREEN tests for inline/query/PGPASSWORD/passfile/host override, unknown ref, missing file, log redaction,
and exact `current_user`/application_name.

### Task 8: Package migrator/lifecycle identities and pause for credential approval

Update migrate image/entrypoint and compose so:

- platform + River migrations use `xm_migrator` secret only;
- bootstrap/tools use `xm_lifecycle_a` secret only;
- cluster admin secret is mounted only where human break-glass design allows;
- no service receives another role's secret;
- API/worker remain on legacy identity until DBR3.

Commit CredentialRef names only. **STOP:** human configures real secret values and role passwords; Codex never
reads or sets them. Compose config/static security tests can run with non-secret placeholders.

### Task 9: Prepare staging change packet and STOP

Write `SWITCH-DATABASE-ROLES.md` with preflight, backup/restore, exact image/compose checksums, SQL evidence,
migrator/lifecycle canary, verifier, inverse grants, and rollback. Do not execute. Staging needs its own approval.

---

## DBR3 — API and Worker Runtime Separation

### Task 10: Wire independent API/worker DSNs without behavior changes

Configure API `xm_api_a` / `application_name=platform-api` and worker `xm_worker_a` /
`application_name=platform-worker`, each with only its Docker secret. Do not alter endpoints, Action definitions,
job schedules, Query contracts, retention cutoffs, or business SQL.

Add tests that container inspect env/secret names do not cross, runtime DSNs have exact user/application_name,
and API cannot access River while worker cannot write action/audit_event.

### Task 11: Run restricted-role application integration tests

Using DBR1 harness:

- API: health/ready, registry/finance/alerts Query, one permitted L1 Action, ActionRun + audit event;
- worker: River migration/heartbeat, Sub2API/NewAPI sync fixtures, finance collection, R5 reconcile, retention;
- negative: all cross-role, owner DDL, PUBLIC and append-only assertions.

Run full Go gates with owner setup DSN plus explicit API/worker role DSNs; a skipped restricted-role test is failure.

### Task 12: Deliver DBR3 code PR and STOP before staging

Commit code/compose/runbook only. Handoff lists files, full gates, exact policy version, credentials not run,
and NO DEPLOY. Human merge does not trigger staging.

### Task 13: Staging canary and rolling cutover — HUMAN EXECUTION ONLY

Runbook order after separate approval:

1. backup + independent restore/audit verification;
2. apply approved DBR2 owner/ACL scripts;
3. migrate/lifecycle no-op canary;
4. one API replica, Query + L1 Action + audit;
5. one worker, River/sync/finance/alert/retention;
6. verifier and `pg_stat_activity` evidence;
7. remaining replicas;
8. soak while legacy identity stays available for rollback.

Codex may review evidence but does not execute these steps.

---

## DBR4 — Ops, Backup, Recovery, and Legacy Lockdown

### Task 14: Put audit-verify/platform-shadow behind ops CredentialRef

Refactor both commands to use shared dbconn + `xm_ops_a`, default transaction read-only, statement/lock/idle
timeouts, and `application_name=platform-ops`. Package a versioned ops tool image. Tests prove any DML fails and
errors/logs never expose DSN/password.

### Task 15: Add backup identity and real restore gate

Package pg_dump/restore tooling with `xm_backup_a`, read-only defaults and no runtime DML. Test on disposable PG18:

1. dump all platform/audit/River schemas and sequences;
2. verify checksum;
3. restore to a new random DB;
4. verify migration version/dirty, owner/ACL/default ACL, audit chain, RUNWAY rows if present;
5. run API/worker restricted-role smoke tests;
6. drop DB and random roles completely.

Backup failure or omitted object is a hard failure; backup secret/age key cannot share storage with DB backup.

### Task 16: Prove zero legacy runtime sessions and draft lockdown

After approved staging soak, collect current_user/application_name evidence. The lockdown script may revoke
legacy service use and rotate/remove shared compose secret, but must not DROP/RENAME/demote bootstrap superuser.
Produce break-glass and rollback procedure. **STOP** for separate legacy-lockdown approval.

### Task 17: Production approval packet and STOP

Require latest-base policy/verifier, staging soak, real restore exercise, image/compose digests, independent
production secrets, maintenance/rollback windows, and human owners. Staging evidence does not authorize production.
No production command appears as an automatic CI/CD step.

---

## RUNWAY and AUDIT Dependency Gates

### RUNWAY

Before RUNWAY live activation, verifier must cover current/history grants exactly. If RUNWAY migration lands first,
DBR2 owner/ACL manifest must discover both tables; if DBR lands first, RUNWAY migration must carry explicit grants.
Disposable development may proceed under its own approvals, but staging/production require DBR2+DBR3 evidence.

### AUDIT archive

DBR4 grants ops/backup SELECT only. No archive implementation may gain audit DELETE through this plan. Export,
chain-root, cold-store and any hot-row lifecycle remain a separate ADR/approval after restore proof.

## Per-Slice Verification

```powershell
go fmt ./...
go vet ./...
go test -p 1 ./...
pnpm --config.verify-deps-before-run=false -r run typecheck
pnpm --config.verify-deps-before-run=false -r run test
pnpm --config.verify-deps-before-run=false --filter ui-storybook run build
bash scripts/check-governance.sh
git diff --check
git status --short
```

DBR1/2/3/4 additionally run their disposable role/security scripts. Any skip is listed with reason and blocks
live approval. Windows linked worktrees must invoke governance with explicit WSL `GIT_DIR`/`GIT_WORK_TREE` so
migration immutability is not silently skipped.

## PR/Handoff Requirements

Every DBR PR includes:

- status/branch/commits/base;
- exact policy version and slice;
- files changed;
- tests run/full outcomes and not run/reason;
- role/owner/PUBLIC/default ACL before/after evidence;
- positive and negative capability matrix;
- CredentialRef names only, never values;
- backup/restore and rollback evidence where applicable;
- RUNWAY/AUDIT dependency state;
- risks/follow-ups;
- explicit `NO LIVE DB CHANGE`, `NO MERGE`, `NO DEPLOY` as applicable.

After DBR0 design PR review, ask the human owner whether DBR1 is authorized. Do not infer DBR1, migration,
credential, staging, lockdown, or production authority from documentation approval.
