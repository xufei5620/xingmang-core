# XM-C Runway Threshold Settings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Approval gate:** This plan is an approval artifact, not implementation authorization. Do not execute any task until a human explicitly authorizes the named C3 slice. C3c additionally requires the approved and merged Foundation-B / XM-0030 controls. Every slice uses its own worktree, branch, PR, review, and human merge.

**Goal:** Replace startup-only runway env thresholds with an environment-scoped, versioned PostgreSQL truth source; unify summary/R5 boundaries; add read/preview surfaces and a Foundation-B-gated L2 management UI.

**Architecture:** Finance owns a current configuration row plus append-only history and exposes a snapshot provider. API requests and worker evaluation rounds each read one DB snapshot and reuse it throughout that unit of work. Alerts owns R5 impact comparison, while all writes use `finance.runway_threshold.set@1`; the UI lives inline under `/alerts?sub=rules` and `/settings` only links there.

**Tech Stack:** Go 1.27, PostgreSQL/pgx/sqlc, chi, River, React 19, TypeScript, TanStack Query, Vitest, existing `@xingmang/ui-*` packages.

**Spec:** `docs/superpowers/specs/2026-08-28-runway-threshold-settings-design.md`

## Global Constraints

- Approval of the spec/plan does not authorize implementation; obtain explicit authorization per C3a–C3d.
- Do not push directly to `main` or `release/v0.1-launch`; do not merge any PR.
- Use one task owner, worktree, branch, and PR per C3 slice.
- All reads are Query; the only write is Action `finance.runway_threshold.set@1`.
- C3c is wholly blocked until Foundation-B is approved and merged: do not create/register its Action contract, scope, Handler, or client before that gate.
- New scope is exactly `finance.runway_threshold.manage`; do not add it to default roles, Keycloak mappings, AI identities, or frontend `DEFAULT_SCOPES`.
- Query and preview use existing `finance.read`.
- Runtime truth is PostgreSQL by environment; env is bootstrap input only and is never a post-cutover fallback.
- Enforce `0 < critical < warning < serious` in Go, Action validation, Store, and DB CHECK constraints.
- Classification is inclusive: `<= critical`, `<= warning`, `<= serious`; `Classify` returns `(level,error)` and never converts invalid thresholds to critical; serious never creates an R5 notification.
- DB missing/unavailable is fail closed: no default fallback and no reconcile from an empty finding set.
- Add trusted codes `RUNWAY_CONFIG_UNAVAILABLE`→503 and `REVISION_CONFLICT`→409; Kernel only preserves safe `*action.Error`, and arbitrary causes stay opaque.
- Bootstrap current/history in one transaction and ship the command inside the versioned migrate lifecycle image/tools service.
- History rejects UPDATE/DELETE/TRUNCATE; tests use a disposable migrated database and minimum non-superuser roles, never TRUNCATE cleanup.
- API request and worker evaluation round each use exactly one threshold snapshot.
- UI uses inline/full-page/dialog only; never a right-side Drawer.
- Do not hardcode a migration prefix. Compute `${MIGRATION_PREFIX}` from the implementation worktree after latest approved dependencies are present.
- After generating the exact dynamically numbered schema diff, stop for explicit migration approval before DB application, sqlc generation, or Go implementation.
- Use Action schema type `int`, not JSON Schema `integer`.
- Use `go fmt`, never bare `gofmt` on this Windows host.
- Credentials remain CredentialRef-only; this feature introduces no credential fields.

## File Structure

### C3a — DB truth source, bootstrap, current/history Query

- Create: `db/migrations/${MIGRATION_PREFIX}_finance_runway_threshold_config.up.sql`
- Create: `db/migrations/${MIGRATION_PREFIX}_finance_runway_threshold_config.down.sql`
- Modify: `db/queries/finance.sql`
- Regenerate: `internal/platform/finance/gen/db.go`
- Regenerate: `internal/platform/finance/gen/models.go`
- Regenerate: `internal/platform/finance/gen/finance.sql.go`
- Create: `internal/platform/finance/runway_config.go`
- Create: `internal/platform/finance/runway_config_store_integration_test.go`
- Create: `scripts/test-runway-threshold-db.ps1`
- Create: `cmd/runway-threshold-bootstrap/main.go`
- Create: `cmd/runway-threshold-bootstrap/main_test.go`
- Modify: `deploy/docker/go.Dockerfile`
- Modify: `deploy/compose/launch.yaml`
- Create: `tests/security/runway-bootstrap-lifecycle.test.sh`
- Create: `tests/security/runway-threshold-db-roles.test.sh`
- Create: `internal/platform/httpapi/finance_runway_config.go`
- Create: `internal/platform/httpapi/finance_runway_config_test.go`
- Modify: `internal/platform/action/errors.go`
- Modify: `internal/platform/action/errors_test.go`
- Modify: `internal/platform/httpapi/response.go`
- Modify: `internal/platform/httpapi/response_test.go`
- Modify: `internal/platform/httpapi/router.go`
- Modify: `cmd/platform-api/main.go`
- Modify: `docs/modules/finance/README.md`

### C3b — classifier, R5, preview, per-request/per-round snapshots

- Modify: `internal/platform/finance/runway.go`
- Modify: `internal/platform/finance/runway_test.go`
- Modify: `internal/platform/finance/summary_store.go`
- Modify: `internal/platform/finance/summary_store_integration_test.go`
- Modify: `internal/platform/alerts/rules.go`
- Modify: `internal/platform/alerts/runway_rule_test.go`
- Modify: `internal/platform/alerts/reconcile_test.go`
- Modify: `internal/platform/alerts/store_integration_test.go`
- Create: `internal/platform/alerts/runway_preview.go`
- Create: `internal/platform/alerts/runway_preview_test.go`
- Modify: `internal/platform/httpapi/finance_summary.go`
- Modify: `internal/platform/httpapi/finance_summary_test.go`
- Create: `internal/platform/httpapi/finance_runway_preview.go`
- Create: `internal/platform/httpapi/finance_runway_preview_test.go`
- Modify: `internal/platform/httpapi/router.go`
- Modify: `internal/platform/jobs/client.go`
- Modify: `internal/platform/jobs/alert_evaluate_test.go`
- Modify: `cmd/platform-worker/config.go`
- Modify: `cmd/platform-worker/config_test.go`
- Modify: `cmd/platform-api/config.go`
- Modify: `cmd/platform-api/config_test.go`
- Modify: `cmd/platform-api/main.go`
- Modify: `docs/modules/alerts/README.md`

### C3c — L2 Action and explicit scope

- Create: `contracts/actions/finance.runway_threshold.set.v1.json`
- Modify: `internal/platform/finance/permissions.go`
- Modify: `internal/platform/finance/runway_config.go`
- Modify: `internal/platform/finance/runway_config_store_integration_test.go`
- Create: `internal/platform/finance/runway_actions.go`
- Create: `internal/platform/finance/runway_actions_test.go`
- Create: `internal/platform/finance/runway_actions_integration_test.go`
- Modify: `internal/platform/action/errors.go`
- Modify: `internal/platform/action/errors_test.go`
- Modify: `internal/platform/action/kernel.go`
- Modify: `internal/platform/action/kernel_test.go`
- Modify: `internal/platform/httpapi/response.go`
- Modify: `internal/platform/httpapi/response_test.go`
- Modify: `cmd/platform-api/main.go`
- Modify: `internal/platform/httpapi/actions_test.go`
- Modify: `docs/modules/action/README.md`
- Modify: `docs/modules/finance/README.md`

### C3d — alerts rules UI and rollout closure

- Create: `web/apps/admin-web/src/api/runwayThresholds.ts` (C3a/b read/preview exports first; Action export only after C3c)
- Create: `web/apps/admin-web/src/api/runwayThresholds.test.ts`
- Create: `web/apps/admin-web/src/components/RunwayThresholdRulePanel.tsx`
- Create: `web/apps/admin-web/src/components/RunwayThresholdRulePanel.test.tsx`
- Create: `web/apps/admin-web/src/pages/AlertRulesPage.tsx`
- Create: `web/apps/admin-web/src/pages/AlertRulesPage.test.tsx`
- Modify: `web/apps/admin-web/src/pages/AlertsPage.tsx`
- Modify: `web/apps/admin-web/src/pages/SettingsPage.tsx`
- Modify: `web/apps/admin-web/src/api/config.ts`
- Modify: `web/apps/admin-web/src/router.test.tsx`
- Modify: `deploy/compose/launch.yaml`
- Modify: `deploy/compose/.env.example`
- Create: `docs/runbooks/SWITCH-RUNWAY-THRESHOLDS-TO-DB.md`

---

## C3a — DB Truth Source, Bootstrap, and Read Queries

### Task 1: Establish the implementation baseline and calculate the migration prefix

**Files:**
- Inspect: `db/migrations/`
- Inspect: `.git`
- No file is modified in this task.

**Interfaces:**
- Consumes: latest `origin/release/v0.1-launch`, PR #103 state, and XM-B003 state.
- Produces: shell variable `${MIGRATION_PREFIX}` used by Task 2.

- [ ] **Step 1: Verify the C3a worktree and fetch current refs**

Run in PowerShell from the dedicated C3a worktree:

```powershell
git rev-parse --show-toplevel
git branch --show-current
git status --short
git fetch --prune origin
gh pr view 103 --json number,state,headRefName,baseRefName,url
git branch -a | Select-String 'XM-B003'
```

Expected: the worktree is on the exact branch named by the approved C3a Task Spec and is clean. Fetch must succeed. If fetch fails, stop; do not calculate a number from stale refs.

- [ ] **Step 2: Put all approved migration dependencies into the worktree before numbering**

Rebase or recreate the C3a branch from the latest approved base. If PR #103 or XM-B003 is an explicit dependency and is not merged, stop for dependency direction; do not cherry-pick an unapproved branch merely to reserve a number.

- [ ] **Step 3: Calculate and validate the next prefix**

```powershell
$migrationNumbers = Get-ChildItem -LiteralPath 'db\migrations' -File |
  ForEach-Object {
    if ($_.Name -match '^(?<number>\d{6})_.*\.(up|down)\.sql$') {
      [int]$Matches.number
    }
  }

if (-not $migrationNumbers) { throw 'No migration files found' }

$MIGRATION_PREFIX = ((($migrationNumbers | Measure-Object -Maximum).Maximum) + 1).ToString('000000')
$existing = Get-ChildItem -LiteralPath 'db\migrations' -File -Filter "$MIGRATION_PREFIX`_*"
if ($existing) { throw "Migration prefix collision: $MIGRATION_PREFIX" }
$MIGRATION_PREFIX
```

Expected: one six-digit value not present in the directory. Record it in the C3a PR description, not in this design branch.

- [ ] **Step 4: Re-run the collision check immediately before the C3a commit and after every base refresh**

Expected: the pair remains unique. If another accepted migration has taken the prefix, rename both unmerged C3a files and regenerate evidence.

### Task 2: Draft the dynamically numbered schema, then stop for migration approval

**Files:**
- Create: `db/migrations/${MIGRATION_PREFIX}_finance_runway_threshold_config.up.sql`
- Create: `db/migrations/${MIGRATION_PREFIX}_finance_runway_threshold_config.down.sql`

**Interfaces:**
- Consumes: `${MIGRATION_PREFIX}` from Task 1 and existing `core.environment(id)`.
- Produces: an exact migration review artifact only; no DB application or generated Go code before approval.

- [ ] **Step 1: Create the migration pair**

The up migration must implement this exact shape:

```sql
CREATE TABLE finance.runway_threshold_config (
    environment   text PRIMARY KEY REFERENCES core.environment (id) ON DELETE RESTRICT,
    critical_days integer NOT NULL CHECK (critical_days > 0),
    warning_days  integer NOT NULL CHECK (warning_days > 0),
    serious_days  integer NOT NULL CHECK (serious_days > 0),
    revision      bigint NOT NULL CHECK (revision > 0),
    updated_at    timestamptz NOT NULL,
    updated_by    text NOT NULL CHECK (btrim(updated_by) <> ''),
    reason        text NOT NULL CHECK (btrim(reason) <> ''),
    request_id    text NOT NULL CHECK (btrim(request_id) <> ''),
    CONSTRAINT runway_threshold_ordered
        CHECK (critical_days < warning_days AND warning_days < serious_days)
);

CREATE TABLE finance.runway_threshold_history (
    environment   text NOT NULL REFERENCES core.environment (id) ON DELETE RESTRICT,
    revision      bigint NOT NULL CHECK (revision > 0),
    critical_days integer NOT NULL CHECK (critical_days > 0),
    warning_days  integer NOT NULL CHECK (warning_days > 0),
    serious_days  integer NOT NULL CHECK (serious_days > 0),
    changed_at    timestamptz NOT NULL,
    changed_by    text NOT NULL CHECK (btrim(changed_by) <> ''),
    reason        text NOT NULL CHECK (btrim(reason) <> ''),
    request_id    text NOT NULL CHECK (btrim(request_id) <> ''),
    change_source text NOT NULL CHECK (change_source IN ('bootstrap', 'action')),
    PRIMARY KEY (environment, revision),
    CONSTRAINT runway_threshold_history_ordered
        CHECK (critical_days < warning_days AND warning_days < serious_days)
);
```

Add an index on `(environment, revision DESC)` and two trigger paths backed by a function that raises a
stable exception: row-level `BEFORE UPDATE OR DELETE`, plus statement-level `BEFORE TRUNCATE`. The down
migration drops both triggers, trigger function, history table, then current table in that order.

- [ ] **Step 2: Produce the migration approval packet without touching a database**

Run:

```powershell
git diff --check -- `
  "db/migrations/$($MIGRATION_PREFIX)_finance_runway_threshold_config.up.sql" `
  "db/migrations/$($MIGRATION_PREFIX)_finance_runway_threshold_config.down.sql"
git diff -- `
  "db/migrations/$($MIGRATION_PREFIX)_finance_runway_threshold_config.up.sql" `
  "db/migrations/$($MIGRATION_PREFIX)_finance_runway_threshold_config.down.sql"
```

The review packet includes the exact prefix calculation, PR #103/XM-B003 state, table/trigger diff, lock
assessment (`CREATE TABLE` only; no rewrite of existing finance tables), down order, and production
forward-fix statement.

- [ ] **Step 3: STOP for explicit migration approval**

Do not apply the migration to any database, run sqlc, or write Store/Query/bootstrap code. The human must
approve this exact numbered diff; approval of RUNWAY0 does not satisfy this gate. If the base changes while
waiting, recompute the prefix, regenerate the pair, and request approval again.

### Task 3: Verify the approved schema and generate queries

**Files:**
- Modify: `db/queries/finance.sql`
- Regenerate: `internal/platform/finance/gen/db.go`
- Regenerate: `internal/platform/finance/gen/models.go`
- Regenerate: `internal/platform/finance/gen/finance.sql.go`
- Create: `internal/platform/finance/runway_config_store_integration_test.go`
- Create: `scripts/test-runway-threshold-db.ps1`
- Create: `tests/security/runway-threshold-db-roles.test.sh`

**Interfaces:**
- Consumes: human-approved migration pair from Task 2.
- Produces: verified current/history constraints and sqlc methods used by `RunwayThresholdStore`.

- [ ] **Step 1: Add the disposable database harness**

`scripts/test-runway-threshold-db.ps1` requires `XM_TEST_DATABASE_ADMIN_URL`, creates a random database
named `xm_runway_test_<uuid>`, applies all migrations, creates non-superuser test roles for platform-api,
worker, and lifecycle capabilities, runs the requested Go/security tests, terminates connections, and drops
the whole database in `finally`. It must refuse an admin URL whose target database is not a known test/admin
database. It never TRUNCATEs history or disables triggers.

- [ ] **Step 2: Write schema/role integration tests**

`TestRunwayThresholdTablesEnforceOrderAndHistoryIsAppendOnly` inserts one valid history row through the
lifecycle test role, then uses the platform-api role to attempt UPDATE, DELETE, and TRUNCATE. Each must fail; SELECT
still succeeds. A 10/5/20 current insert must fail the order CHECK. The worker role must SELECT current and
must fail every DML statement. Teardown is database drop only.

```go
for name, statement := range map[string]string{
    "update":   `UPDATE finance.runway_threshold_history SET reason='rewritten'`,
    "delete":   `DELETE FROM finance.runway_threshold_history`,
    "truncate": `TRUNCATE finance.runway_threshold_history`,
} {
    t.Run(name, func(t *testing.T) {
        if _, err := apiRolePool.Exec(ctx, statement); err == nil {
            t.Fatalf("%s history must be rejected", name)
        }
    })
}
```

- [ ] **Step 3: Apply the approved migration only to the disposable DB and run RED/GREEN schema checks**

First run the schema test against a disposable DB created from the pre-migration base and confirm the table
missing failure. Then run the harness with the approved migration included and require all CHECK/trigger/role
assertions to pass.

- [ ] **Step 4: Add sqlc queries**

Add named queries for:

Add named queries for:

```sql
-- name: GetRunwayThresholdConfig :one
SELECT * FROM finance.runway_threshold_config WHERE environment = $1;

-- name: InsertRunwayThresholdBootstrap :one
INSERT INTO finance.runway_threshold_config (
    environment, critical_days, warning_days, serious_days,
    revision, updated_at, updated_by, reason, request_id
) VALUES (
    sqlc.arg(environment), sqlc.arg(critical_days), sqlc.arg(warning_days),
    sqlc.arg(serious_days), 1, sqlc.arg(updated_at), sqlc.arg(updated_by),
    sqlc.arg(reason), sqlc.arg(request_id)
)
ON CONFLICT (environment) DO NOTHING
RETURNING *;

-- name: UpdateRunwayThresholdConfigAtRevision :one
UPDATE finance.runway_threshold_config SET
    critical_days = sqlc.arg(critical_days),
    warning_days = sqlc.arg(warning_days),
    serious_days = sqlc.arg(serious_days),
    revision = revision + 1,
    updated_at = sqlc.arg(updated_at),
    updated_by = sqlc.arg(updated_by),
    reason = sqlc.arg(reason),
    request_id = sqlc.arg(request_id)
WHERE environment = sqlc.arg(environment)
  AND revision = sqlc.arg(expected_revision)
RETURNING *;

-- name: InsertRunwayThresholdHistory :one
INSERT INTO finance.runway_threshold_history (
    environment, revision, critical_days, warning_days, serious_days,
    changed_at, changed_by, reason, request_id, change_source
) VALUES (
    sqlc.arg(environment), sqlc.arg(revision), sqlc.arg(critical_days),
    sqlc.arg(warning_days), sqlc.arg(serious_days), sqlc.arg(changed_at),
    sqlc.arg(changed_by), sqlc.arg(reason), sqlc.arg(request_id),
    sqlc.arg(change_source)
) RETURNING *;

-- name: ListRunwayThresholdHistory :many
SELECT * FROM finance.runway_threshold_history
WHERE environment = sqlc.arg(environment)
  AND (sqlc.arg(before_revision)::bigint = 0 OR revision < sqlc.arg(before_revision))
ORDER BY revision DESC
LIMIT sqlc.arg(result_limit);
```

Bootstrap conflict inspection uses `GetRunwayThresholdConfig` after an `ON CONFLICT DO NOTHING`; do not overwrite an existing row.

- [ ] **Step 5: Regenerate sqlc output**

Run:

```powershell
sqlc generate
```

Expected: only finance generated files change in addition to the query file.

- [ ] **Step 6: Run the approved migration/role tests GREEN**

```powershell
pwsh -File scripts/test-runway-threshold-db.ps1 `
  -GoTest './internal/platform/finance' `
  -Run 'TestRunwayThresholdTablesEnforceOrderAndHistoryIsAppendOnly'
bash tests/security/runway-threshold-db-roles.test.sh
```

Expected: PASS.

- [ ] **Step 7: Commit the approved C3a schema/query evidence**

```powershell
git add -- "db/migrations/$($MIGRATION_PREFIX)_finance_runway_threshold_config.up.sql" `
  "db/migrations/$($MIGRATION_PREFIX)_finance_runway_threshold_config.down.sql" `
  db/queries/finance.sql internal/platform/finance/gen `
  internal/platform/finance/runway_config_store_integration_test.go `
  scripts/test-runway-threshold-db.ps1 `
  tests/security/runway-threshold-db-roles.test.sh
git commit -m "feat(finance): add versioned runway threshold schema"
```

### Task 4: Implement the snapshot Store and transactional lifecycle bootstrap

**Files:**
- Create: `internal/platform/finance/runway_config.go`
- Modify: `internal/platform/finance/runway_config_store_integration_test.go`
- Create: `cmd/runway-threshold-bootstrap/main.go`
- Create: `cmd/runway-threshold-bootstrap/main_test.go`
- Modify: `deploy/docker/go.Dockerfile`
- Modify: `deploy/compose/launch.yaml`
- Create: `tests/security/runway-bootstrap-lifecycle.test.sh`

**Interfaces:**
- Consumes: sqlc methods from Task 3 and `ParseRunwayThresholds`.
- Produces:
  - `RunwayThresholdSnapshot`
  - `RunwayThresholdProvider.Current(ctx, environment)`
  - `RunwayThresholdStore.Bootstrap(ctx, input)`
  - `RunwayThresholdStore.ListHistory(ctx, query)`

- [ ] **Step 1: Write failing Store tests**

Cover these exact cases:

| Test | Setup | Required assertion |
|---|---|---|
| `TestRunwayThresholdStoreBootstrapIsIdempotent` | call Bootstrap twice with 5/10/20 | both return revision 1; history count stays 1 |
| `TestRunwayThresholdStoreBootstrapRefusesDifferentExistingValues` | bootstrap 5/10/20, retry 4/9/18 | error is `ErrRunwayBootstrapConflict`; current remains 5/10/20 |
| `TestRunwayThresholdStoreBootstrapHistoryFailureRollsBackCurrent` | disposable DB trigger rejects staging history INSERT | Bootstrap errors; staging current/history both have zero rows |
| `TestRunwayThresholdStoreCurrentMissingFailsClosed` | fresh disposable DB, development row absent | error is `ErrRunwayConfigUnavailable`; no default snapshot is returned |

The first test should contain the concrete idempotency assertion:

```go
first, err := store.Bootstrap(ctx, bootstrapInput("production", thresholds(5, 10, 20)))
if err != nil {
    t.Fatal(err)
}
second, err := store.Bootstrap(ctx, bootstrapInput("production", thresholds(5, 10, 20)))
if err != nil {
    t.Fatal(err)
}
if first.Revision != 1 || second.Revision != 1 || first.Thresholds != second.Thresholds {
    t.Fatalf("bootstrap must be idempotent: first=%+v second=%+v", first, second)
}
```

- [ ] **Step 2: Run tests and confirm RED**

```powershell
go test -p 1 ./internal/platform/finance -run 'TestRunwayThresholdStore' -count=1
```

Expected: FAIL because Store types do not exist.

- [ ] **Step 3: Implement the domain types and provider**

Use these exact public interfaces:

```go
type RunwayThresholdSnapshot struct {
    Environment string
    Thresholds  RunwayThresholds
    Revision    int64
    Source      string
    UpdatedAt   time.Time
    UpdatedBy   string
    Reason      string
    RequestID   string
}

type RunwayThresholdProvider interface {
    Current(context.Context, string) (RunwayThresholdSnapshot, error)
}

type BootstrapRunwayThresholdInput struct {
    Environment string
    Thresholds  RunwayThresholds
    Actor        string
    Reason       string
    RequestID    string
}

```

`Current` maps no row and database errors to a wrapped `ErrRunwayConfigUnavailable`; it never returns
defaults. `Bootstrap` opens one pgx transaction, checks/inserts current, inserts history, and commits only
after both succeed. Do not add the general `Set` method in C3a; that write implementation belongs wholly to
post-Foundation-B C3c.

The history failure test installs a disposable-DB-only trigger that raises for staging history INSERT, calls
Bootstrap, then asserts both tables have zero staging rows before dropping the entire database. It must not
TRUNCATE either table or disable the production append-only triggers.

- [ ] **Step 4: Implement the versioned bootstrap command**

The command reads exactly:

```go
environment := strings.TrimSpace(os.Getenv("ENVIRONMENT"))
thresholds, err := finance.ParseRunwayThresholds(
    os.Getenv("XM_FINANCE_RUNWAY_WARN_DAYS"),
    os.Getenv("XM_FINANCE_RUNWAY_CRIT_DAYS"),
)
```

It resolves the database through the repository CredentialRef path, then calls `Bootstrap` with actor
`platform-lifecycle:runway-threshold-bootstrap`, reason `import existing runway environment thresholds`, and
a generated request ID. It also implements `version`, which prints build version/commit without opening the
database. Tests inject env lookup and Store interface; they prove invalid env fails before DB access,
history failure rolls back current, and conflict never overwrites.

- [ ] **Step 5: Put bootstrap in the versioned lifecycle image and tools service**

Add `./cmd/runway-threshold-bootstrap` to the builder output in `deploy/docker/go.Dockerfile`; copy the
binary into the existing `migrate` target. Add a compose service named `runway-threshold-bootstrap` with:

```yaml
profiles: ["tools"]
image: xingmang/migrate:${BUILD_VERSION:-staging}
entrypoint: ["/usr/local/bin/runway-threshold-bootstrap"]
command: ["up"]
```

It uses the same build target/digest as `migrate`, depends on successful migration, and receives DB
CredentialRef plus bootstrap-only ENVIRONMENT/WARN/CRIT variables. It is never part of normal `up`.

`tests/security/runway-bootstrap-lifecycle.test.sh` fails unless the Docker builder compiles the binary, the
migrate target copies it, the service uses the same versioned image and `tools` profile, and runtime API/worker
services do not invoke it.

- [ ] **Step 6: Run Store, command, image-contract tests GREEN**

```powershell
pwsh -File scripts/test-runway-threshold-db.ps1 `
  -GoTest './internal/platform/finance' `
  -Run 'TestRunwayThresholdStore'
go test -p 1 ./cmd/runway-threshold-bootstrap -count=1
bash tests/security/runway-bootstrap-lifecycle.test.sh
docker build -f deploy/docker/go.Dockerfile --target migrate `
  --build-arg BUILD_VERSION=runway-test --build-arg BUILD_COMMIT=runway-test `
  -t xingmang/migrate:runway-test .
docker run --rm --entrypoint /usr/local/bin/runway-threshold-bootstrap `
  xingmang/migrate:runway-test version
docker compose -p xingmang-runway-plan -f deploy/compose/launch.yaml `
  --env-file deploy/compose/.env.example --profile tools config --quiet
```

Expected: PASS.

- [ ] **Step 7: Commit C3a Store/bootstrap lifecycle delivery**

```powershell
git add internal/platform/finance/runway_config.go `
  internal/platform/finance/runway_config_store_integration_test.go `
  cmd/runway-threshold-bootstrap deploy/docker/go.Dockerfile `
  deploy/compose/launch.yaml tests/security/runway-bootstrap-lifecycle.test.sh
git commit -m "feat(finance): add runway threshold store and bootstrap"
```

### Task 5: Add current/history Query endpoints and safe 503 mapping

**Files:**
- Create: `internal/platform/httpapi/finance_runway_config.go`
- Create: `internal/platform/httpapi/finance_runway_config_test.go`
- Modify: `internal/platform/action/errors.go`
- Modify: `internal/platform/action/errors_test.go`
- Modify: `internal/platform/httpapi/response.go`
- Modify: `internal/platform/httpapi/response_test.go`
- Modify: `internal/platform/httpapi/router.go`
- Modify: `cmd/platform-api/main.go`
- Modify: `docs/modules/finance/README.md`

**Interfaces:**
- Consumes: `RunwayThresholdProvider` and Store history query from Task 4.
- Produces:
  - `GET /api/v1/finance/runway-thresholds`
  - `GET /api/v1/finance/runway-thresholds/history`

- [ ] **Step 1: Write failing HTTP tests**

Tests must assert:

| Test | Request | Required assertion |
|---|---|---|
| `TestGetRunwayThresholdsReturnsDBRevisionAndSource` | `GET /finance/runway-thresholds` with `finance.read` | 200; exact 5/10/20, revision 7, source database |
| `TestGetRunwayThresholdsRequiresFinanceRead` | same request without scope | 403 names `finance.read`; provider call count is zero |
| `TestGetRunwayThresholdsRejectsCrossEnvironment` | staging Principal requests production | 403; no production snapshot is read |
| `TestGetRunwayThresholdsMissingRowReturnsUnavailableNotDefaults` | provider returns `ErrRunwayConfigUnavailable` | 503 with stable code; body has no synthesized thresholds |
| `TestListRunwayThresholdHistoryIsBoundedAndDescending` | `limit=2` over revisions 1,2,3 | returns 3,2 with `has_more=true`; invalid/oversized limits rejected or capped per handler contract |
| `TestWriteErrorMapsRunwayUnavailableToOpaque503` | wrapped cause contains DSN/IP/table/constraint | 503 + safe message; none of the cause fragments appear |
| `TestWriteErrorUnknownErrorRemainsOpaque500` | ordinary error text says `RUNWAY_CONFIG_UNAVAILABLE` | still INTERNAL 500; string imitation is not trusted |

Use an actual JSON assertion for the current endpoint:

```go
if rec.Code != http.StatusOK {
    t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
}
var got runwayThresholdResponse
if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
    t.Fatal(err)
}
if got.Revision != 7 || got.Source != "database" ||
    got.CriticalDays != 5 || got.WarningDays != 10 || got.SeriousDays != 20 {
    t.Fatalf("unexpected snapshot: %+v", got)
}
```

The unavailable test must assert HTTP 503 and stable code `RUNWAY_CONFIG_UNAVAILABLE`, and must assert the response does not contain `5`, `10`, or `20` as a synthesized configuration.

- [ ] **Step 2: Run tests and confirm RED**

```powershell
go test -p 1 ./internal/platform/httpapi -run 'RunwayThreshold' -count=1
```

Expected: FAIL because routes and handlers do not exist.

- [ ] **Step 3: Implement DTOs and handlers**

Add `action.CodeRunwayConfigUnavailable = "RUNWAY_CONFIG_UNAVAILABLE"` and map it to HTTP 503. The
handler maps `finance.ErrRunwayConfigUnavailable` to:

```go
action.NewError(
    action.CodeRunwayConfigUnavailable,
    "可用天数阈值配置暂不可用",
    err,
)
```

The current response uses flat snake_case fields from the spec. History uses `{items, has_more}` with a
maximum page size of 100. Resolve environment through the existing principal helper; never accept a
handler-local fallback. Do not expose `err.Error()` or special-case an arbitrary string.

- [ ] **Step 4: Wire dependencies and routes**

Add a read interface to `httpapi.Deps`, construct one `finance.NewRunwayThresholdStore(pool, nil)` in `cmd/platform-api/main.go`, and mount both routes behind `RequireScope(finance.ScopeRead)`.

- [ ] **Step 5: Run focused and C3a package tests**

```powershell
go test -p 1 ./internal/platform/finance ./internal/platform/httpapi ./cmd/runway-threshold-bootstrap ./cmd/platform-api -count=1
```

Expected: PASS.

- [ ] **Step 6: Update finance module documentation and commit**

Document DB ownership, bootstrap, error behavior, Query shapes, and the fact that execution remains absent.

```powershell
git add internal/platform/httpapi/finance_runway_config.go `
  internal/platform/httpapi/finance_runway_config_test.go `
  internal/platform/action/errors.go internal/platform/action/errors_test.go `
  internal/platform/httpapi/response.go internal/platform/httpapi/response_test.go `
  internal/platform/httpapi/router.go cmd/platform-api/main.go `
  docs/modules/finance/README.md
git commit -m "feat(api): expose runway threshold queries"
```

---

## C3b — One Classifier, R5 Preview, and Runtime Snapshots

### Task 6: Replace split boundary logic with one error-returning inclusive classifier

**Files:**
- Modify: `internal/platform/finance/runway.go`
- Modify: `internal/platform/finance/runway_test.go`
- Modify: `internal/platform/finance/summary_store.go`
- Modify: `internal/platform/finance/summary_store_integration_test.go`
- Modify: `internal/platform/alerts/rules.go`
- Modify: `internal/platform/alerts/runway_rule_test.go`

**Interfaces:**
- Consumes: existing `RunwayThresholds`.
- Produces: `RunwayThresholds.Classify(days int) (RunwayLevel, error)` and
  `ComputeRunway(RunwayInput) (Runway, error)`, used by summary, preview, and R5.

- [ ] **Step 1: Rewrite boundary tests to the approved inclusive matrix**

```go
func TestRunwayThresholdsClassifyInclusiveBoundaries(t *testing.T) {
    thresholds := finance.RunwayThresholds{CriticalDays: 5, WarningDays: 10, SeriousDays: 20}
    cases := map[int]finance.RunwayLevel{
        4: finance.RunwayCritical,
        5: finance.RunwayCritical,
        6: finance.RunwayWarning,
        10: finance.RunwayWarning,
        11: finance.RunwaySerious,
        20: finance.RunwaySerious,
        21: finance.RunwayHealthy,
    }
    for days, want := range cases {
        got, err := thresholds.Classify(days)
        if err != nil {
            t.Fatalf("%d days: %v", days, err)
        }
        if got != want {
            t.Fatalf("%d days: got %s want %s", days, got, want)
        }
    }
}

func TestRunwayThresholdsClassifyRejectsInvalidConfig(t *testing.T) {
    bad := finance.RunwayThresholds{CriticalDays: 10, WarningDays: 5, SeriousDays: 20}
    got, err := bad.Classify(3)
    if err == nil || got != "" {
        t.Fatalf("invalid thresholds must return empty level + error, got=%q err=%v", got, err)
    }
}
```

Update R5 tests so 5 is critical, 10 is warning, 11/20/21 do not create Findings.

- [ ] **Step 2: Run tests and confirm RED at 5/10**

```powershell
go test -p 1 ./internal/platform/finance ./internal/platform/alerts `
  -run 'RunwayThresholdsClassifyInclusiveBoundaries|RunwayAlertBands|RunwayAlertSilentAboveThreshold' -count=1
```

Expected: FAIL under the current `<` evaluator behavior and missing exported classifier.

- [ ] **Step 3: Implement one classifier and consume it everywhere**

```go
func (t RunwayThresholds) Classify(days int) (RunwayLevel, error) {
    if err := t.Validate(); err != nil {
        return "", err
    }
    switch {
    case days <= t.CriticalDays:
        return RunwayCritical, nil
    case days <= t.WarningDays:
        return RunwayWarning, nil
    case days <= t.SeriousDays:
        return RunwaySerious, nil
    default:
        return RunwayHealthy, nil
    }
}
```

Change `ComputeRunway` to return `(Runway, error)` and propagate `Classify` errors. Update every call site found
by `rg -n 'ComputeRunway\('`; no call may ignore the error or restore default thresholds. R5 switches on a
successful level: critical/warning produce one Finding; serious/healthy produce none. Delete duplicated
integer comparisons and `runwayInputs`/RuleConfig invalid→default fallbacks.

- [ ] **Step 4: Run package tests GREEN**

```powershell
go test -p 1 ./internal/platform/finance ./internal/platform/alerts -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the semantic correction separately**

```powershell
git add internal/platform/finance/runway.go internal/platform/finance/runway_test.go `
  internal/platform/finance/summary_store.go `
  internal/platform/finance/summary_store_integration_test.go `
  internal/platform/alerts/rules.go internal/platform/alerts/runway_rule_test.go
git commit -m "fix(alerts): unify inclusive runway threshold boundaries"
```

### Task 7: Add complete R5 impact preview

**Files:**
- Create: `internal/platform/alerts/runway_preview.go`
- Create: `internal/platform/alerts/runway_preview_test.go`
- Create: `internal/platform/httpapi/finance_runway_preview.go`
- Create: `internal/platform/httpapi/finance_runway_preview_test.go`
- Modify: `internal/platform/httpapi/router.go`

**Interfaces:**
- Consumes: current/proposed thresholds, `[]finance.UpstreamRunway`, and active R5 alerts.
- Produces: `RunwayImpactPreview` and preview Query endpoint.

- [ ] **Step 1: Write pure preview tests**

Use these transition constants and cases:

```go
const (
    RunwayWouldOpen       RunwayImpactTransition = "would_open"
    RunwayWouldEscalate   RunwayImpactTransition = "would_escalate"
    RunwayWouldDeescalate RunwayImpactTransition = "would_deescalate"
    RunwayWouldResolve    RunwayImpactTransition = "would_resolve"
    RunwayUnchanged       RunwayImpactTransition = "unchanged"
    RunwayCurrentInconsistent RunwayImpactTransition = "current_inconsistent"
)
```

Tests must cover every row of the spec transition table: open, warning→critical escalation,
critical→warning de-escalation, resolve, unchanged actionable, unchanged non-actionable, missing active alert,
unexpected active alert (including unknown/subscription), severity mismatch, and duplicate active alert.
Inconsistency takes precedence over proposed impact. Also prove serious never opens an alert, preview does not
mutate inputs, and output ordering is stable by transition then account ID.

- [ ] **Step 2: Run tests and confirm RED**

```powershell
go test -p 1 ./internal/platform/alerts -run RunwayImpactPreview -count=1
```

Expected: FAIL because preview types do not exist.

- [ ] **Step 3: Implement the pure comparator**

The comparator calls error-returning `current.Classify(days)` and `proposed.Classify(days)` and propagates any
error. Alert activity is derived from critical/warning only. It compares current classifier state with the
actual active R5 rows before predicting proposed impact. It never calls Store/Reconciler and never invents days
for unknown items.

- [ ] **Step 4: Write failing HTTP preview tests**

Assert positive/strictly ordered query params, `finance.read`, environment isolation, page cap, top-level
`evaluation_at`, per-object `observed_at`, current revision, coverage, all six transition counts, consistency
reasons, and zero writes to config/action/audit stores. Set the two times differently and assert they are not
mapped into each other's fields.

- [ ] **Step 5: Implement and mount preview Query**

Mount:

```text
GET /api/v1/finance/runway-thresholds/preview
```

The handler records `evaluation_at` once, reads the current config snapshot once, obtains current runways and
active R5 alerts, runs the pure comparator, and returns changed or inconsistent items plus counts. Each item
uses the balance evidence `observed_at`; the top-level time is never copied into it. It does not bind or execute
an Action.

- [ ] **Step 6: Run focused tests GREEN and commit**

```powershell
go test -p 1 ./internal/platform/alerts ./internal/platform/httpapi -run 'Runway.*Preview' -count=1
git add internal/platform/alerts/runway_preview.go `
  internal/platform/alerts/runway_preview_test.go `
  internal/platform/httpapi/finance_runway_preview.go `
  internal/platform/httpapi/finance_runway_preview_test.go `
  internal/platform/httpapi/router.go
git commit -m "feat(alerts): preview runway threshold impact"
```

### Task 8: Make API and worker consume one DB snapshot per unit of work and persist revision evidence

**Files:**
- Modify: `internal/platform/finance/summary_store.go`
- Modify: `internal/platform/httpapi/finance_summary.go`
- Modify: `internal/platform/httpapi/finance_summary_test.go`
- Modify: `internal/platform/alerts/rules.go`
- Modify: `internal/platform/alerts/runway_rule_test.go`
- Modify: `internal/platform/jobs/client.go`
- Modify: `internal/platform/jobs/alert_evaluate_test.go`
- Modify: `cmd/platform-api/config.go`
- Modify: `cmd/platform-api/config_test.go`
- Modify: `cmd/platform-api/main.go`
- Modify: `cmd/platform-worker/config.go`
- Modify: `cmd/platform-worker/config_test.go`
- Modify: `docs/modules/alerts/README.md`

**Interfaces:**
- Consumes: `finance.RunwayThresholdProvider` from C3a.
- Produces: one snapshot per HTTP request and one snapshot per Evaluator round.

- [ ] **Step 1: Add failing call-count and failure tests**

Use a fake provider with an atomic call counter. Tests must assert:

| Test | Provider behavior | Required assertion |
|---|---|---|
| `TestUpstreamSummaryReadsOneThresholdSnapshotPerRequest` | counter returns revision 8 | one HTTP request increments counter exactly once |
| `TestUpstreamSummaryUsesSameRevisionForRowsAndResponse` | provider would return a different value on call 2 | only revision 8/its thresholds appear in both calculations and response |
| `TestUpstreamSummaryConfigFailureDoesNotReturnDefaults` | `Current` errors | endpoint returns unavailable; 5/10/20 are not synthesized |
| `TestEvaluatorReadsOneThresholdSnapshotPerRound` | counter returns revision 8 | one Evaluate increments counter once regardless of account count |
| `TestEvaluatorConfigFailureDoesNotReconcileOrResolve` | provider errors | worker returns error; Store Upsert/Resolve call counts remain zero |
| `TestNextEvaluationRoundObservesNewRevision` | first call revision 8, second revision 9 | two Evaluate calls classify with 8 then 9 |
| `TestRunwayFindingCarriesThresholdRevisionAndValues` | revision 8 / 5/10/20 | Finding.Detail contains all four stable key/value pairs |
| `TestReconcilerPersistsRunwayThresholdEvidence` | reconcile that Finding | stored alert detail contains exact revision and three values |

The call-count assertion is exact:

```go
_, err := handlerResultForSummary(t, provider, lister)
if err != nil {
    t.Fatal(err)
}
if got := provider.Calls(); got != 1 {
    t.Fatalf("threshold provider calls=%d want 1", got)
}
```

- [ ] **Step 2: Run tests and confirm RED**

```powershell
go test -p 1 ./internal/platform/httpapi ./internal/platform/alerts ./internal/platform/jobs `
  -run 'ThresholdSnapshot|ConfigFailure|NewRevision' -count=1
```

Expected: FAIL because API/worker still hold startup config.

- [ ] **Step 3: Replace static API dependency with provider**

Remove `Deps.FinanceRunwayThresholds`; add `Deps.FinanceRunwayThresholds finance.RunwayThresholdProvider`. In each summary request call `Current` exactly once, pass `snapshot.Thresholds` to `SummaryStore`, and serialize the same snapshot fields into `runway_thresholds`.

- [ ] **Step 4: Replace worker startup thresholds with provider**

Remove `jobs.Config.AlertRunwayThresholds` and both cmd env parsers. Inject the DB-backed provider into alerts Evaluator. `Evaluate` reads one snapshot before requesting runways; an error aborts the round before Reconciler can treat it as empty findings.

When R5 emits a Finding, append this exact stable fragment to detail:

```text
threshold_revision=8; critical_days=5; warning_days=10; serious_days=20
```

Use values from the same per-round snapshot. Evaluator tests assert generation; Reconciler/Store tests assert
the detail is persisted unchanged on insert and touch. Do not put updated_by/reason or DB errors in detail.

- [ ] **Step 5: Preserve env parsing only in bootstrap**

Search:

```powershell
rg -n 'XM_FINANCE_RUNWAY_(WARN|CRIT)_DAYS|ParseRunwayThresholds' cmd internal deploy
```

Expected after this code task: the two env names remain in bootstrap/deployment documentation only; runtime API/worker config no longer reads them.

- [ ] **Step 6: Run C3b backend tests GREEN**

```powershell
go test -p 1 ./internal/platform/finance ./internal/platform/alerts `
  ./internal/platform/httpapi ./internal/platform/jobs ./cmd/platform-api ./cmd/platform-worker -count=1
```

Expected: PASS.

- [ ] **Step 7: Update alerts docs and commit**

Document inclusive boundaries, serious/no-notification, threshold revision in R5 detail, per-round snapshot, and failure behavior.

```powershell
git add internal/platform/finance/summary_store.go `
  internal/platform/httpapi/finance_summary.go internal/platform/httpapi/finance_summary_test.go `
  internal/platform/alerts/rules.go internal/platform/alerts/runway_rule_test.go `
  internal/platform/alerts/reconcile_test.go internal/platform/alerts/store_integration_test.go `
  internal/platform/jobs/client.go internal/platform/jobs/alert_evaluate_test.go `
  cmd/platform-api/config.go cmd/platform-api/config_test.go cmd/platform-api/main.go `
  cmd/platform-worker/config.go cmd/platform-worker/config_test.go `
  docs/modules/alerts/README.md
git commit -m "feat(runtime): load runway thresholds per request and evaluation"
```

---

## C3c — Foundation-B-Gated L2 Action

### Task 9: Implement the complete L2 Action only after Foundation-B is merged

**Files:**
- Create: `contracts/actions/finance.runway_threshold.set.v1.json`
- Modify: `internal/platform/finance/permissions.go`
- Modify: `internal/platform/finance/runway_config.go`
- Modify: `internal/platform/finance/runway_config_store_integration_test.go`
- Create: `internal/platform/finance/runway_actions.go`
- Create: `internal/platform/finance/runway_actions_test.go`
- Create: `internal/platform/finance/runway_actions_integration_test.go`
- Modify: `internal/platform/action/errors.go`
- Modify: `internal/platform/action/errors_test.go`
- Modify: `internal/platform/action/kernel.go`
- Modify: `internal/platform/action/kernel_test.go`
- Modify: `internal/platform/httpapi/response.go`
- Modify: `internal/platform/httpapi/response_test.go`
- Modify: `cmd/platform-api/main.go`
- Modify: `internal/platform/httpapi/actions_test.go`
- Modify: `docs/modules/action/README.md`
- Modify: `docs/modules/finance/README.md`

**Interfaces:**
- Consumes: merged XM-0030 approval/execute controls and C3a Store current/history.
- Produces: `RunwayThresholdStore.Set`, trusted 409 error propagation, registered
  `finance.runway_threshold.set@1`, and new scope constant.

- [ ] **Step 1: STOP unless Foundation-B evidence exists**

Run:

```powershell
gh pr list --state open --search 'XM-0030 in:title' --json number,title,state,url
rg -n '状态:设计稿,待产品负责人拍板|RequiresAdvancedControls' `
  docs/superpowers/plans/2026-08-27-xm-0030-action-advanced-controls-design.md `
  internal/platform/action/risk.go
```

Required evidence is explicit human approval, merged Action Advanced Controls on the C3c target base, and green
tests proving L2 approval/execute/idempotency. If any evidence is absent, stop with **zero C3c file changes**:
do not create the contract/scope/definition/Handler and do not pre-register a fail-closed Action.

- [ ] **Step 2: Write failing domain, error-model, definition, and Handler tests**

Add these exact cases:

| Test | Required assertion |
|---|---|
| `TestSetRunwayThresholdInputRejectsNonPositiveRevision` | 0/-1 rejected before SQL |
| `TestSetRunwayThresholdInputRejectsBlankReason` | `""` and whitespace rejected before SQL |
| `TestRunwayThresholdStoreSetRequiresExpectedRevision` | stale revision returns `ErrRevisionConflict`; no history row |
| `TestRunwayThresholdStoreSetWritesCurrentAndHistoryAtomically` | exact next revision appears in both tables |
| `TestKernelPreservesTrustedRevisionConflict` | wrapped `*action.Error` keeps code in result/run/audit; safe message only |
| `TestKernelDoesNotTrustRevisionConflictText` | ordinary error containing that text becomes `EXECUTION_FAILED` |
| `TestStatusForRevisionConflict` | trusted code maps to 409 |
| `TestRunwayThresholdActionRequiresHumanAndScope` | denied before Store call |
| `TestRunwayThresholdActionRejectsInvalidSemanticParamsBeforeStore` | bad order, revision<=0, blank reason all make zero Store calls |
| `TestRunwayThresholdActionRecordsEvidenceAndConfirmsWrite` | before/after/reason/resource and readback are exact |

HTTP tests place a DSN, IP, table name, constraint name, and actual current revision inside the wrapped cause;
none may appear in the response body.

- [ ] **Step 3: Implement domain Set validation and atomic write**

Add `SetRunwayThresholdInput` in `runway_config.go`. Its `Validate` trims reason and requires
`ExpectedRevision > 0`, valid environment/actor/request ID, and valid strict thresholds. `Store.Set` validates
again, then in one pgx transaction performs revision-guarded current UPDATE and history INSERT. A zero-row
UPDATE returns `ErrRevisionConflict`; history failure rolls back current. After commit, read back and compare
the exact values and `expected+1` revision.

- [ ] **Step 4: Add trusted revision conflict error handling**

Add `action.CodeRevisionConflict = "REVISION_CONFLICT"`; `StatusForCode` maps it to 409. Update Kernel Handler
error handling so only `errors.As(err, &actionErr)` where `actionErr` is `*action.Error` preserves trusted code/message in ActionRun/audit and
the returned error. Every ordinary error remains `EXECUTION_FAILED`; cause text is only logged.

The Handler maps `finance.ErrRevisionConflict` to:

```go
action.NewError(
    action.CodeRevisionConflict,
    "阈值配置已更新，请刷新后重新预览",
    err,
)
```

- [ ] **Step 5: Add the exact scope, Action definition, and contract**

Assert exact ID/version, `action.L2`, HUMAN-only, all explicit environments, permission
`finance.runway_threshold.manage`, and Action schema fields with `FieldInt`/`FieldString`. The contract body is:

```json
{
  "id": "finance.runway_threshold.set",
  "version": "1",
  "risk_level": "L2",
  "permission": "finance.runway_threshold.manage",
  "principal_types": ["HUMAN"],
  "environments": ["development", "staging", "production"],
  "idempotency": "REQUIRED_FOUNDATION_B",
  "write_confirmation": "REQUIRED_FOUNDATION_B",
  "approval": {"required": true},
  "compensation_mode": "MANUAL",
  "params": {
    "critical_days": {"type": "int", "required": true},
    "warning_days": {"type": "int", "required": true},
    "serious_days": {"type": "int", "required": true},
    "expected_revision": {"type": "int", "required": true},
    "reason": {"type": "string", "required": true}
  }
}
```

- [ ] **Step 6: Implement the Handler with semantic prechecks**

Use `action.IntParam` for the four integers, trim `action.StringParam(reason)`, and reject nonpositive revision,
blank reason, or invalid thresholds before `Store.Current`/`Set`. Take actor/environment from Principal context;
never accept environment in params. Record resource `finance.runway_threshold_config`, resource ID environment,
before/after snapshots, and trimmed reason. Return approval/run IDs and verified old/new revisions.

- [ ] **Step 7: Prove the new scope is not granted by default**

Search and test:

```powershell
rg -n 'finance\.runway_threshold\.manage' web/apps/admin-web/src/api/config.ts `
  internal/platform/oidcauth deploy
```

Expected: no default role or `DEFAULT_SCOPES` occurrence. The string may exist only in the Action/permission/UI missing-scope copy.

- [ ] **Step 8: Add real DB + approved-kernel integration coverage**

Using the disposable DB harness, submit an L2 request, obtain approval under the merged XM-0030 policy, execute
once, retry to prove one-run idempotency, verify current/history revision increment, and check Action audit
before/after. Inject history INSERT failure and write-confirmation mismatch; neither may report success.

- [ ] **Step 9: Run C3c tests GREEN and commit**

```powershell
go test -p 1 ./internal/platform/finance ./internal/platform/action `
  ./internal/platform/httpapi ./cmd/platform-api -count=1
git add contracts/actions/finance.runway_threshold.set.v1.json `
  internal/platform/finance/permissions.go internal/platform/finance/runway_config.go `
  internal/platform/finance/runway_config_store_integration_test.go `
  internal/platform/finance/runway_actions.go `
  internal/platform/finance/runway_actions_test.go `
  internal/platform/finance/runway_actions_integration_test.go `
  internal/platform/action/errors.go internal/platform/action/errors_test.go `
  internal/platform/action/kernel.go internal/platform/action/kernel_test.go `
  internal/platform/httpapi/response.go internal/platform/httpapi/response_test.go `
  internal/platform/httpapi/actions_test.go cmd/platform-api/main.go `
  docs/modules/action/README.md docs/modules/finance/README.md
git commit -m "feat(finance): add approved runway threshold action"
```

---

## C3d — Inline Rules UI and Deployment Closure

### Task 10: Add typed frontend current/history/preview clients

**Files:**
- Create: `web/apps/admin-web/src/api/runwayThresholds.ts`
- Create: `web/apps/admin-web/src/api/runwayThresholds.test.ts`

**Interfaces:**
- Consumes: C3a/b Query contracts only.
- Produces: typed current/history/preview functions and query keys; no Action/scope export before C3c.

- [ ] **Step 1: Write failing client tests**

Assert exact paths, snake_case mapping, environment query behavior, AbortSignal forwarding, all six transition
values, consistency reason, top-level `evaluation_at`, and per-object `observed_at`. Assert the module exports
no submit helper or manage-permission constant while C3c is absent.

- [ ] **Step 2: Run tests and confirm RED**

```powershell
pnpm --config.verify-deps-before-run=false --filter admin-web test -- `
  src/api/runwayThresholds.test.ts
```

Expected: FAIL because the module does not exist.

- [ ] **Step 3: Implement exact frontend types**

```ts
export interface RunwayThresholdSnapshot {
  environment: string;
  criticalDays: number;
  warningDays: number;
  seriousDays: number;
  revision: number;
  source: "database";
  updatedAt: string;
  updatedBy: string;
  reason: string;
}

export interface RunwayThresholdDraft {
  criticalDays: number;
  warningDays: number;
  seriousDays: number;
}

export type RunwayImpactTransition =
  | "would_open"
  | "would_escalate"
  | "would_deescalate"
  | "would_resolve"
  | "unchanged"
  | "current_inconsistent";

export interface RunwayImpactItem {
  accountId: string;
  observedAt: string | null;
  transition: RunwayImpactTransition;
  consistencyReason: string | null;
}

export interface RunwayImpactPreview {
  evaluationAt: string;
  items: RunwayImpactItem[];
}

export const RUNWAY_THRESHOLD_QUERY = "finance-runway-threshold";
export const RUNWAY_THRESHOLD_HISTORY_QUERY = "finance-runway-threshold-history";
export const RUNWAY_THRESHOLD_PREVIEW_QUERY = "finance-runway-threshold-preview";
```

Expose only `getRunwayThresholds`, `listRunwayThresholdHistory`, and `previewRunwayThresholds`. Preview types
must model `evaluationAt` separately from each item's `observedAt`, and include
`current_inconsistent`/`consistencyReason`. Do not introduce the manage scope string yet.

- [ ] **Step 4: Run client tests GREEN and commit**

```powershell
pnpm --config.verify-deps-before-run=false --filter admin-web test -- `
  src/api/runwayThresholds.test.ts
git add web/apps/admin-web/src/api/runwayThresholds.ts `
  web/apps/admin-web/src/api/runwayThresholds.test.ts
git commit -m "feat(admin): add runway threshold api client"
```

### Task 11: Build the inline rules experience and Settings link

**Files:**
- Create: `web/apps/admin-web/src/components/RunwayThresholdRulePanel.tsx`
- Create: `web/apps/admin-web/src/components/RunwayThresholdRulePanel.test.tsx`
- Create: `web/apps/admin-web/src/pages/AlertRulesPage.tsx`
- Create: `web/apps/admin-web/src/pages/AlertRulesPage.test.tsx`
- Modify: `web/apps/admin-web/src/pages/AlertsPage.tsx`
- Modify: `web/apps/admin-web/src/pages/SettingsPage.tsx`
- Modify: `web/apps/admin-web/src/router.test.tsx`

**Interfaces:**
- Consumes: frontend clients from Task 10, Action catalog state, and current Principal scopes.
- Produces: `/alerts?sub=rules` inline editor/preview/history and `/settings` governance link.

- [ ] **Step 1: Write failing UI tests for placement and shape**

Tests must cover this exact matrix:

| Test | Interaction | Required assertion |
|---|---|---|
| rules placement | render `/alerts?sub=rules`, then `/settings` | thresholds appear only on rules route; Settings has one link |
| no Drawer | render rule editor and confirmation | no complementary/right-drawer landmark or Drawer import; confirmation is a dialog |
| serious copy | load 5/10/20 | text says serious is display-only and creates no R5 notification |
| client validation | change critical to 10 while warning is 10; click preview | inline strict-order error; preview client not called |
| evidence order | resolve preview | evaluation time/coverage render before table; each row shows its own observation time |
| current inconsistency | return missing/unexpected/severity mismatch rows | dedicated warning and consistency reason; not counted as proposed effect |
| C3c absent gate | Action catalog has no `finance.runway_threshold.set@1` | no execute request/helper; fixed Foundation-B/C3c gate shown |

The strict-order test includes a concrete no-request assertion:

```tsx
fireEvent.change(screen.getByLabelText("Critical 天数"), { target: { value: "10" } });
fireEvent.change(screen.getByLabelText("Warning 天数"), { target: { value: "10" } });
fireEvent.click(screen.getByRole("button", { name: "预览影响" }));
expect(await screen.findByText("必须满足 0 < critical < warning < serious")).not.toBeNull();
expect(previewRunwayThresholds).not.toHaveBeenCalled();
```

Add a static source assertion that neither new component imports or renders a Drawer.

- [ ] **Step 2: Run tests and confirm RED**

```powershell
pnpm --config.verify-deps-before-run=false --filter admin-web test -- `
  src/components/RunwayThresholdRulePanel.test.tsx `
  src/pages/AlertRulesPage.test.tsx src/router.test.tsx
```

Expected: FAIL because the page/components do not exist.

- [ ] **Step 3: Implement IA-v3 subpage routing**

`AlertsPage` reads `sub` from the URL. `sub=alerts` retains the existing active/all control inside that subpage. `sub=rules` renders `AlertRulesPage`. Other final-IA subpages remain honest `PageState unavailable` until their own slices; do not fabricate incidents/notifications/silence lists.

- [ ] **Step 4: Implement the inline panel and dialog flow**

The panel always shows current values, revision/source/time, serious explanation, and history. Editing and
preview are inline. “预览影响” renders all transition counts, a dedicated current-inconsistent warning,
top-level evaluation time, and each object's observation time. With C3c absent there is no confirmation Dialog
or write button—only the fixed Foundation-B/C3c gate. No right-side Drawer is used.

- [ ] **Step 5: Implement the pre-C3c hard gate**

- `finance.read` controls Query visibility through service responses.
- Action catalog absence shows “Foundation-B / C3c 尚未开放”.
- No manage scope is read or assumed, no execute client exists, and no POST is emitted.

- [ ] **Step 6: Make Settings a link, not a second editor**

Replace the stale “告警规则与静默尚未实现” settings card with a governance entry linking to `/alerts?sub=rules`. Assert Settings contains no critical/warning/serious inputs.

- [ ] **Step 7: Run UI tests, typecheck, and commit**

```powershell
pnpm --config.verify-deps-before-run=false --filter admin-web test
pnpm --config.verify-deps-before-run=false --filter admin-web run typecheck
git add web/apps/admin-web/src/components/RunwayThresholdRulePanel.tsx `
  web/apps/admin-web/src/components/RunwayThresholdRulePanel.test.tsx `
  web/apps/admin-web/src/pages/AlertRulesPage.tsx `
  web/apps/admin-web/src/pages/AlertRulesPage.test.tsx `
  web/apps/admin-web/src/pages/AlertsPage.tsx `
  web/apps/admin-web/src/pages/SettingsPage.tsx `
  web/apps/admin-web/src/router.test.tsx
git commit -m "feat(admin): add gated runway threshold rules ui"
```

Expected: tests/typecheck PASS.

### Task 12: Extend the rules UI with approved Action submission after C3c

**Files:**
- Modify: `web/apps/admin-web/src/api/runwayThresholds.ts`
- Modify: `web/apps/admin-web/src/api/runwayThresholds.test.ts`
- Modify: `web/apps/admin-web/src/components/RunwayThresholdRulePanel.tsx`
- Modify: `web/apps/admin-web/src/components/RunwayThresholdRulePanel.test.tsx`
- Modify: `web/apps/admin-web/src/api/config.ts`

**Interfaces:**
- Consumes: merged C3c Action/catalog and Foundation-B approval API.
- Produces: submit-approval client/dialog, scope gate, and verified-success state.

- [ ] **Step 1: Stop unless C3c and Foundation-B are merged on the target base**

If the Action catalog does not contain exact ID/version/risk/scope, leave Task 11's fixed gate in place and make
no Task 12 changes.

- [ ] **Step 2: Write failing client/UI tests**

| Test | Required assertion |
|---|---|
| Action request | exact generic Action endpoint, request ID, params include three ints/positive expected revision/trimmed reason |
| blank reason/revision | whitespace reason and revision 0/-1 produce no POST |
| missing scope | submit disabled and exact manage scope named |
| centered confirmation | Dialog repeats proposed values, revision, `evaluation_at`, impact/inconsistency counts; no Drawer |
| 403/409/503 | safe message displayed; draft and last preview retained; no root-cause fragment rendered |
| approval response | UI says “已提交审批”, never “已生效” |
| verified success | green state only after current + summary refetch match expected next revision and all three values |

- [ ] **Step 3: Add the post-C3c client and scope constant**

Only now export:

```ts
export const RUNWAY_THRESHOLD_MANAGE_PERMISSION = "finance.runway_threshold.manage";
export async function submitRunwayThresholdAction(input: {
  criticalDays: number;
  warningDays: number;
  seriousDays: number;
  expectedRevision: number;
  reason: string;
}): Promise<ActionRun>;
```

Trim reason and require `expectedRevision > 0` before building params. Do not add the permission to
`DEFAULT_SCOPES`; add a test that fails if it appears there.

- [ ] **Step 4: Implement the centered approval Dialog and write verification**

The submit button says “提交审批”. Keep inputs/preview on every error. A 409 forces current refetch and
re-preview. After approval execution, refetch both current config and upstream summary; only exact next revision
and exact three values produce “写后验证通过”.

- [ ] **Step 5: Run tests/typecheck and commit**

```powershell
pnpm --config.verify-deps-before-run=false --filter admin-web test -- `
  src/api/runwayThresholds.test.ts `
  src/components/RunwayThresholdRulePanel.test.tsx
pnpm --config.verify-deps-before-run=false --filter admin-web run typecheck
git add web/apps/admin-web/src/api/runwayThresholds.ts `
  web/apps/admin-web/src/api/runwayThresholds.test.ts `
  web/apps/admin-web/src/components/RunwayThresholdRulePanel.tsx `
  web/apps/admin-web/src/components/RunwayThresholdRulePanel.test.tsx `
  web/apps/admin-web/src/api/config.ts
git commit -m "feat(admin): submit approved runway threshold changes"
```

### Task 13: Cut over deployment and retire runtime env authority

**Files:**
- Modify: `deploy/compose/launch.yaml`
- Modify: `deploy/compose/.env.example`
- Create: `docs/runbooks/SWITCH-RUNWAY-THRESHOLDS-TO-DB.md`
- Modify: `docs/modules/finance/README.md`
- Modify: `docs/modules/alerts/README.md`

**Interfaces:**
- Consumes: C3a bootstrap binary and C3a/b DB-only runtime.
- Produces: repeatable preflight/bootstrap/verify/cutover/rollback procedure.

- [ ] **Step 1: Write the runbook before changing compose**

The runbook must contain these ordered gates:

1. verify the exact migration diff has its separate approval and production DB roles are non-superuser with the
   approved role-split evidence; otherwise stop;
2. back up DB and record current API/worker image digests, migrate image digest, and the exact released
   `launch.yaml`/env-file checksums;
3. record current WARN/CRIT values in the restricted change record; they are not credentials, but must not be
   lost because old binaries require them for rollback;
4. apply the approved dynamically numbered migration through the versioned migrate image;
5. prove bootstrap binary version/commit matches the migrate image:

   ```powershell
   docker compose -p xingmang-launch -f deploy/compose/launch.yaml `
     --env-file deploy/compose/.env --profile tools `
     run --rm runway-threshold-bootstrap version
   ```

   Then run once per environment:

   ```powershell
   docker compose -p xingmang-launch -f deploy/compose/launch.yaml `
     --env-file deploy/compose/.env --profile tools `
     run --rm runway-threshold-bootstrap up
   ```
6. Query current and history; require revision 1, exact effective values, one history row, and no partial row;
7. start one upgraded API replica and verify summary revision/three values;
8. start one upgraded worker, wait one evaluation interval, and verify R5 detail/log uses the same revision/three
   values;
9. roll remaining replicas only after equality evidence;
10. remove runtime env wiring in the final compose revision while retaining bootstrap-only values/service;
11. execute a staged rollback rehearsal using the procedure below before production approval.

Rollback is one atomic operational decision, not “use old image” alone:

1. stop upgraded API/worker replicas;
2. restore the previous released `launch.yaml` (or previous signed compose artifact) and previous image digests;
3. restore the recorded `XM_FINANCE_RUNWAY_WARN_DAYS` and `XM_FINANCE_RUNWAY_CRIT_DAYS` values in the
   gitignored env file and verify old compose passes them to **both** API and worker;
4. start one old API replica and assert summary回显 equals the recorded env-derived thresholds;
5. start one old worker, wait one evaluation interval, and assert its configured R5 warning/critical values equal
   the same recorded values;
6. only then roll remaining old replicas and declare rollback complete;
7. retain current/history tables and rows; old binaries ignore them. Never run production down migration or
   delete history.

- [ ] **Step 2: Add a deployment contract test/search**

After cutover, this command must only find the old variables in the bootstrap command and historical/runbook explanation:

```powershell
rg -n 'XM_FINANCE_RUNWAY_(WARN|CRIT)_DAYS' cmd internal deploy docs
```

It must not find the names under `cmd/platform-api`, `cmd/platform-worker`, or runtime API/worker environment
blocks in `launch.yaml`; it must still find them under the tools-profile bootstrap service and runbook.

- [ ] **Step 3: Remove the two runtime env entries**

Remove them from API/worker environment blocks and mark them bootstrap/rollback-only in `.env.example`. Keep
the tools-profile bootstrap service wired to them. Do not add a runtime `RUNWAY_CONFIG_FALLBACK` switch.

- [ ] **Step 4: Run compose and docs verification**

```powershell
docker compose -p xingmang-launch -f deploy/compose/launch.yaml `
  --env-file deploy/compose/.env.example --profile tools config --quiet
bash scripts/check-governance.sh
```

Expected: compose config and governance checks PASS. On a Windows linked worktree whose `.git` pointer WSL cannot resolve, invoke the script with explicit WSL `GIT_DIR` and `GIT_WORK_TREE`; do not treat “skipped migration check” as a pass.

- [ ] **Step 5: Commit deployment closure**

```powershell
git add deploy/compose/launch.yaml deploy/compose/.env.example `
  docs/runbooks/SWITCH-RUNWAY-THRESHOLDS-TO-DB.md `
  docs/modules/finance/README.md docs/modules/alerts/README.md
git commit -m "docs(runway): add threshold database cutover runbook"
```

---

## Per-Slice Verification Before PR

Run only after the authorized slice implementation is complete; a skipped command must be listed in Handoff with a concrete reason.

```powershell
go fmt ./...
go vet ./...
go test -p 1 ./...
pnpm --config.verify-deps-before-run=false -r run typecheck
pnpm --config.verify-deps-before-run=false -r run test
pnpm --config.verify-deps-before-run=false --filter ui-storybook run build
bash scripts/check-governance.sh
git status --short
git diff --check
```

For C3a/C3c, use `scripts/test-runway-threshold-db.ps1` with a disposable database; never add history to shared
TRUNCATE cleanup. Verify migration up/down, trigger/role denials, bootstrap rollback on history failure, and
Action atomicity. For C3d, rebuild staging containers and capture desktop/narrow rules pages for read/preview,
current inconsistency, C3c-absent gate, and—only after C3c—no-scope, 409/503, approval, and verified success.
Run the old-compose + restored-env rollback rehearsal and attach API/worker equality evidence.

## PR/Handoff Requirements

Every C3 PR must include:

- status / branch / commits;
- exact spec section and C3 slice;
- files changed;
- tests run and full outcomes;
- tests not run and why;
- migration prefix derivation evidence when applicable;
- current/preview/Action contract examples;
- scope and default-role search evidence;
- risks, rollback/compensation, and follow-ups;
- explicit statement that Codex did not merge or deploy.

After the design PR containing this plan is reviewed, ask the human owner which authorized C3 slice to start. Do not infer authorization from document approval.
