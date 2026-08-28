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
- The Action is L2, HUMAN-only, and fail closed with `ADVANCED_CONTROLS_REQUIRED` until Foundation-B is approved and merged.
- New scope is exactly `finance.runway_threshold.manage`; do not add it to default roles, Keycloak mappings, AI identities, or frontend `DEFAULT_SCOPES`.
- Query and preview use existing `finance.read`.
- Runtime truth is PostgreSQL by environment; env is bootstrap input only and is never a post-cutover fallback.
- Enforce `0 < critical < warning < serious` in Go, Action validation, Store, and DB CHECK constraints.
- Classification is inclusive: `<= critical`, `<= warning`, `<= serious`; serious never creates an R5 notification.
- DB missing/unavailable is fail closed: no default fallback and no reconcile from an empty finding set.
- API request and worker evaluation round each use exactly one threshold snapshot.
- UI uses inline/full-page/dialog only; never a right-side Drawer.
- Do not hardcode a migration prefix. Compute `${MIGRATION_PREFIX}` from the implementation worktree after latest approved dependencies are present.
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
- Modify: `internal/platform/finance/store_integration_test.go`
- Create: `cmd/runway-threshold-bootstrap/main.go`
- Create: `cmd/runway-threshold-bootstrap/main_test.go`
- Create: `internal/platform/httpapi/finance_runway_config.go`
- Create: `internal/platform/httpapi/finance_runway_config_test.go`
- Modify: `internal/platform/httpapi/router.go`
- Modify: `cmd/platform-api/main.go`
- Modify: `docs/modules/finance/README.md`

### C3b — classifier, R5, preview, per-request/per-round snapshots

- Modify: `internal/platform/finance/runway.go`
- Modify: `internal/platform/finance/runway_test.go`
- Modify: `internal/platform/finance/summary_store.go`
- Modify: `internal/platform/alerts/rules.go`
- Modify: `internal/platform/alerts/runway_rule_test.go`
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
- Create: `internal/platform/finance/runway_actions.go`
- Create: `internal/platform/finance/runway_actions_test.go`
- Create: `internal/platform/finance/runway_actions_integration_test.go`
- Modify: `cmd/platform-api/main.go`
- Modify: `internal/platform/httpapi/actions_test.go`
- Modify: `docs/modules/action/README.md`
- Modify: `docs/modules/finance/README.md`

### C3d — alerts rules UI and rollout closure

- Create: `web/apps/admin-web/src/api/runwayThresholds.ts`
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

### Task 2: Add current/history schema with database invariants

**Files:**
- Create: `db/migrations/${MIGRATION_PREFIX}_finance_runway_threshold_config.up.sql`
- Create: `db/migrations/${MIGRATION_PREFIX}_finance_runway_threshold_config.down.sql`
- Modify: `db/queries/finance.sql`
- Regenerate: `internal/platform/finance/gen/db.go`
- Regenerate: `internal/platform/finance/gen/models.go`
- Regenerate: `internal/platform/finance/gen/finance.sql.go`
- Test: `internal/platform/finance/runway_config_store_integration_test.go`
- Modify: `internal/platform/finance/store_integration_test.go`

**Interfaces:**
- Consumes: `${MIGRATION_PREFIX}` from Task 1 and existing `core.environment(id)`.
- Produces: current row, append-only history, and sqlc methods used by `RunwayThresholdStore`.

- [ ] **Step 1: Write the failing integration test for schema constraints and history immutability**

Create `internal/platform/finance/runway_config_store_integration_test.go` in package `finance_test` and use the existing `testPool(t)` helper:

```go
func TestRunwayThresholdTablesEnforceOrderAndHistoryIsAppendOnly(t *testing.T) {
    pool := testPool(t)
    ctx := context.Background()

    _, err := pool.Exec(ctx, `
        INSERT INTO finance.runway_threshold_config (
            environment, critical_days, warning_days, serious_days,
            revision, updated_at, updated_by, reason, request_id
        ) VALUES ('production', 10, 5, 20, 1, now(), 'itest', 'bad order', 'req-bad')`)
    if err == nil {
        t.Fatal("critical >= warning must be rejected by the database")
    }

    _, err = pool.Exec(ctx, `
        INSERT INTO finance.runway_threshold_history (
            environment, revision, critical_days, warning_days, serious_days,
            changed_at, changed_by, reason, request_id, change_source
        ) VALUES (
            'production', 1, 5, 10, 20,
            now(), 'itest', 'bootstrap', 'req-history', 'bootstrap'
        )`)
    if err != nil {
        t.Fatalf("insert history fixture: %v", err)
    }

    _, err = pool.Exec(ctx, `
        UPDATE finance.runway_threshold_history SET reason='rewritten'
        WHERE environment='production' AND revision=1`)
    if err == nil {
        t.Fatal("history UPDATE must be rejected")
    }
}
```

- [ ] **Step 2: Run the test and confirm RED**

Run:

```powershell
go test -p 1 ./internal/platform/finance -run TestRunwayThresholdTablesEnforceOrderAndHistoryIsAppendOnly -count=1
```

Expected: FAIL because the tables do not exist.

- [ ] **Step 3: Create the migration pair**

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

Add a trigger function that raises on UPDATE or DELETE of history, plus an index on
`(environment, revision DESC)`. The down migration drops the trigger, trigger function, history table, then current table in that order.

- [ ] **Step 4: Add sqlc queries**

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

Extend `testPool(t)` so its single TRUNCATE statement begins with
`finance.runway_threshold_history, finance.runway_threshold_config, ...`; current/history must not leak between integration tests.

- [ ] **Step 5: Regenerate sqlc output**

Run:

```powershell
sqlc generate
```

Expected: only finance generated files change in addition to the query file.

- [ ] **Step 6: Run the migration/store test GREEN**

Run with the repository integration-test database configured:

```powershell
go test -p 1 ./internal/platform/finance -run TestRunwayThresholdTablesEnforceOrderAndHistoryIsAppendOnly -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit C3a schema**

```powershell
git add -- "db/migrations/$($MIGRATION_PREFIX)_finance_runway_threshold_config.up.sql" `
  "db/migrations/$($MIGRATION_PREFIX)_finance_runway_threshold_config.down.sql" `
  db/queries/finance.sql internal/platform/finance/gen `
  internal/platform/finance/runway_config_store_integration_test.go `
  internal/platform/finance/store_integration_test.go
git commit -m "feat(finance): add versioned runway threshold schema"
```

### Task 3: Implement the snapshot Store and lifecycle bootstrap

**Files:**
- Create: `internal/platform/finance/runway_config.go`
- Modify: `internal/platform/finance/runway_config_store_integration_test.go`
- Create: `cmd/runway-threshold-bootstrap/main.go`
- Create: `cmd/runway-threshold-bootstrap/main_test.go`

**Interfaces:**
- Consumes: sqlc methods from Task 2 and `ParseRunwayThresholds`.
- Produces:
  - `RunwayThresholdSnapshot`
  - `RunwayThresholdProvider.Current(ctx, environment)`
  - `RunwayThresholdStore.Bootstrap(ctx, input)`
  - `RunwayThresholdStore.Set(ctx, input)` for C3c
  - `RunwayThresholdStore.ListHistory(ctx, query)`

- [ ] **Step 1: Write failing Store tests**

Cover these exact cases:

| Test | Setup | Required assertion |
|---|---|---|
| `TestRunwayThresholdStoreBootstrapIsIdempotent` | call Bootstrap twice with 5/10/20 | both return revision 1; history count stays 1 |
| `TestRunwayThresholdStoreBootstrapRefusesDifferentExistingValues` | bootstrap 5/10/20, retry 4/9/18 | error is `ErrRunwayBootstrapConflict`; current remains 5/10/20 |
| `TestRunwayThresholdStoreSetRequiresExpectedRevision` | current revision 2, Set expects 1 | error is `ErrRevisionConflict`; no revision 3 history |
| `TestRunwayThresholdStoreSetWritesCurrentAndHistoryAtomically` | current revision 1, Set 4/9/18 | current and history both contain revision 2 and the exact values |
| `TestRunwayThresholdStoreCurrentMissingFailsClosed` | truncate current/history | error is `ErrRunwayConfigUnavailable`; no default snapshot is returned |

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

type SetRunwayThresholdInput struct {
    Environment      string
    Thresholds       RunwayThresholds
    ExpectedRevision int64
    Actor             string
    Reason            string
    RequestID         string
}
```

`Current` maps no row and database errors to a wrapped `ErrRunwayConfigUnavailable`; it never returns defaults. `Set` uses one pgx transaction for current update plus history insert and reads back after commit for exact write confirmation.

- [ ] **Step 4: Implement the versioned bootstrap command**

The command reads exactly:

```go
environment := strings.TrimSpace(os.Getenv("ENVIRONMENT"))
thresholds, err := finance.ParseRunwayThresholds(
    os.Getenv("XM_FINANCE_RUNWAY_WARN_DAYS"),
    os.Getenv("XM_FINANCE_RUNWAY_CRIT_DAYS"),
)
```

It resolves the database through the repository CredentialRef path, then calls `Bootstrap` with actor `platform-lifecycle:runway-threshold-bootstrap`, reason `import existing runway environment thresholds`, and a generated request ID. Tests inject env lookup and Store interface; they must prove invalid env fails before DB access and conflict never overwrites.

- [ ] **Step 5: Run Store and command tests GREEN**

```powershell
go test -p 1 ./internal/platform/finance ./cmd/runway-threshold-bootstrap -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit C3a Store/bootstrap**

```powershell
git add internal/platform/finance/runway_config.go `
  internal/platform/finance/runway_config_store_integration_test.go `
  cmd/runway-threshold-bootstrap
git commit -m "feat(finance): add runway threshold store and bootstrap"
```

### Task 4: Add current/history Query endpoints

**Files:**
- Create: `internal/platform/httpapi/finance_runway_config.go`
- Create: `internal/platform/httpapi/finance_runway_config_test.go`
- Modify: `internal/platform/httpapi/router.go`
- Modify: `cmd/platform-api/main.go`
- Modify: `docs/modules/finance/README.md`

**Interfaces:**
- Consumes: `RunwayThresholdProvider` and Store history query from Task 3.
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

The current response uses flat snake_case fields from the spec. History uses `{items, has_more}` with a maximum page size of 100. Resolve environment through the existing principal helper; never accept a handler-local fallback.

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
  internal/platform/httpapi/router.go cmd/platform-api/main.go `
  docs/modules/finance/README.md
git commit -m "feat(api): expose runway threshold queries"
```

---

## C3b — One Classifier, R5 Preview, and Runtime Snapshots

### Task 5: Replace split boundary logic with one inclusive classifier

**Files:**
- Modify: `internal/platform/finance/runway.go`
- Modify: `internal/platform/finance/runway_test.go`
- Modify: `internal/platform/alerts/rules.go`
- Modify: `internal/platform/alerts/runway_rule_test.go`

**Interfaces:**
- Consumes: existing `RunwayThresholds`.
- Produces: `RunwayThresholds.Classify(days int) RunwayLevel`, used by compute, preview, and R5.

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
        if got := thresholds.Classify(days); got != want {
            t.Fatalf("%d days: got %s want %s", days, got, want)
        }
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
func (t RunwayThresholds) Classify(days int) RunwayLevel {
    if err := t.Validate(); err != nil {
        return RunwayCritical
    }
    switch {
    case days <= t.CriticalDays:
        return RunwayCritical
    case days <= t.WarningDays:
        return RunwayWarning
    case days <= t.SeriousDays:
        return RunwaySerious
    default:
        return RunwayHealthy
    }
}
```

`ComputeRunway` calls `Classify`. R5 switches on the returned level: critical/warning produce one Finding; serious/healthy produce none. Delete duplicated integer comparisons from alerts.

- [ ] **Step 4: Run package tests GREEN**

```powershell
go test -p 1 ./internal/platform/finance ./internal/platform/alerts -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit the semantic correction separately**

```powershell
git add internal/platform/finance/runway.go internal/platform/finance/runway_test.go `
  internal/platform/alerts/rules.go internal/platform/alerts/runway_rule_test.go
git commit -m "fix(alerts): unify inclusive runway threshold boundaries"
```

### Task 6: Add pure R5 impact preview

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
)
```

Tests must prove unknown days and subscription accounts only affect coverage, serious never opens an alert, preview does not mutate inputs, and output ordering is stable by transition then account ID.

- [ ] **Step 2: Run tests and confirm RED**

```powershell
go test -p 1 ./internal/platform/alerts -run RunwayImpactPreview -count=1
```

Expected: FAIL because preview types do not exist.

- [ ] **Step 3: Implement the pure comparator**

The comparator calls `current.Classify(days)` and `proposed.Classify(days)`. Alert activity is derived from critical/warning only. It never calls Store or Reconciler and never invents days for unknown items.

- [ ] **Step 4: Write failing HTTP preview tests**

Assert positive/strictly ordered query params, `finance.read`, environment isolation, page cap, `observed_at`, current revision, coverage, transition counts, and zero writes to config/action/audit stores.

- [ ] **Step 5: Implement and mount preview Query**

Mount:

```text
GET /api/v1/finance/runway-thresholds/preview
```

The handler reads the current config snapshot once, obtains current runways and active R5 alerts, runs the pure comparator, and returns only changed items plus counts. It does not bind or execute an Action.

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

### Task 7: Make API and worker consume one DB snapshot per unit of work

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
  internal/platform/jobs/client.go internal/platform/jobs/alert_evaluate_test.go `
  cmd/platform-api/config.go cmd/platform-api/config_test.go cmd/platform-api/main.go `
  cmd/platform-worker/config.go cmd/platform-worker/config_test.go `
  docs/modules/alerts/README.md
git commit -m "feat(runtime): load runway thresholds per request and evaluation"
```

---

## C3c — Foundation-B-Gated L2 Action

### Task 8: Register the L2 Action and prove the pre-F-B gate

**Files:**
- Create: `contracts/actions/finance.runway_threshold.set.v1.json`
- Modify: `internal/platform/finance/permissions.go`
- Create: `internal/platform/finance/runway_actions.go`
- Create: `internal/platform/finance/runway_actions_test.go`
- Modify: `cmd/platform-api/main.go`
- Modify: `internal/platform/httpapi/actions_test.go`

**Interfaces:**
- Consumes: `RunwayThresholdStore.Set` from C3a and existing Action registry/kernel.
- Produces: registered `finance.runway_threshold.set@1` definition and new scope constant.

- [ ] **Step 1: Confirm the external gate before touching code**

Run:

```powershell
gh pr list --state open --search 'XM-0030 in:title' --json number,title,state,url
rg -n '状态:设计稿,待产品负责人拍板|RequiresAdvancedControls' `
  docs/superpowers/plans/2026-08-27-xm-0030-action-advanced-controls-design.md `
  internal/platform/action/risk.go
```

If Foundation-B is not explicitly approved and merged, this task may only register/test the fail-closed definition; do not make execution succeed.

- [ ] **Step 2: Write failing definition tests**

Assert exact ID/version, `action.L2`, HUMAN-only, all explicit environments, permission `finance.runway_threshold.manage`, and Action schema fields with `FieldInt`/`FieldString`.

- [ ] **Step 3: Add the scope and contract**

The contract body must contain:

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

- [ ] **Step 4: Prove pre-F-B execution is denied**

Use the real kernel with a HUMAN Principal holding the new scope. Assert execute returns `ADVANCED_CONTROLS_REQUIRED`, Store `Set` call count remains zero, and a failed ActionRun/audit attempt is recorded.

- [ ] **Step 5: Prove the new scope is not granted by default**

Search and test:

```powershell
rg -n 'finance\.runway_threshold\.manage' web/apps/admin-web/src/api/config.ts `
  internal/platform/oidcauth deploy
```

Expected: no default role or `DEFAULT_SCOPES` occurrence. The string may exist only in the Action/permission/UI missing-scope copy.

- [ ] **Step 6: Run gate tests GREEN and commit the gated definition**

```powershell
go test -p 1 ./internal/platform/finance ./internal/platform/action `
  ./internal/platform/httpapi ./cmd/platform-api -run 'RunwayThreshold|AdvancedControls' -count=1
git add contracts/actions/finance.runway_threshold.set.v1.json `
  internal/platform/finance/permissions.go `
  internal/platform/finance/runway_actions.go `
  internal/platform/finance/runway_actions_test.go `
  cmd/platform-api/main.go internal/platform/httpapi/actions_test.go
git commit -m "feat(finance): register gated runway threshold action"
```

### Task 9: Enable execution only after Foundation-B is approved and merged

**Files:**
- Modify: `internal/platform/finance/runway_actions.go`
- Modify: `internal/platform/finance/runway_actions_test.go`
- Create: `internal/platform/finance/runway_actions_integration_test.go`
- Modify: `docs/modules/action/README.md`
- Modify: `docs/modules/finance/README.md`

**Interfaces:**
- Consumes: approved XM-0030 approval request/decision/execute API and `RunwayThresholdStore.Set`.
- Produces: expected-revision L2 execution with domain write confirmation and Action audit metadata.

- [ ] **Step 1: Stop unless Foundation-B evidence exists**

Required evidence is an approved design decision, merged Action Advanced Controls code on the target base, and green tests proving L2 approval/execute. An open design document or registered L2 definition is insufficient.

- [ ] **Step 2: Write failing handler tests**

Assert:

| Test | Input | Required assertion |
|---|---|---|
| `TestRunwayThresholdActionRequiresHuman` | MACHINE Principal with manage scope | denied before Store call |
| `TestRunwayThresholdActionRequiresManageScope` | HUMAN without manage scope | `PERMISSION_DENIED` names exact scope |
| `TestRunwayThresholdActionRejectsNonIncreasingValues` | 10/5/20 or 5/5/20 | invalid params; Store call count zero |
| `TestRunwayThresholdActionRejectsStaleExpectedRevision` | expected 3/current 4 | conflict; current/history unchanged |
| `TestRunwayThresholdActionRecordsReasonBeforeAfterAndResource` | valid 4/9/18 | audit resource is environment and before/after/reason are exact |
| `TestRunwayThresholdActionReadsBackExactNextRevision` | expected 4 | result/current are revision 5 and exact thresholds |
| `TestRunwayThresholdActionWriteConfirmationFailureIsNotSuccess` | fake Store returns mismatched readback | Action run is failed; UI-safe error contains no DB details |

At minimum, implement the no-Store-call invariant explicitly:

```go
_, err := kernel.Execute(machineContext, validThresholdRequest())
if !action.IsCode(err, action.CodePrincipalTypeNotAllowed) {
    t.Fatalf("got %v", err)
}
if got := store.SetCalls(); got != 0 {
    t.Fatalf("Store.Set calls=%d want 0", got)
}
```

- [ ] **Step 3: Implement the handler**

Use `action.IntParam` for all four integer params and `action.StringParam` for reason. Take actor/environment from Principal context. Call:

```go
before, err := store.Current(ctx, p.Environment)
after, err := store.Set(ctx, finance.SetRunwayThresholdInput{
    Environment:      p.Environment,
    Thresholds:       proposed,
    ExpectedRevision: int64(expectedRevision),
    Actor:             p.ID,
    Reason:            reason,
    RequestID:         requestID,
})
```

Record resource type `finance.runway_threshold_config`, resource ID environment, before/after snapshots, and trimmed reason. Never accept `environment` in params.

- [ ] **Step 4: Add real DB + approved-kernel integration test**

The test submits an L2 request, obtains approval according to the merged XM-0030 policy, executes once, retries execution to prove one-run idempotency, verifies current revision increment and one history row, and checks Action audit before/after.

- [ ] **Step 5: Run C3c tests GREEN**

```powershell
go test -p 1 ./internal/platform/action ./internal/platform/finance `
  ./internal/platform/httpapi ./cmd/platform-api -count=1
```

Expected: PASS with Foundation-B target base.

- [ ] **Step 6: Update docs and commit**

```powershell
git add internal/platform/finance/runway_actions.go `
  internal/platform/finance/runway_actions_test.go `
  internal/platform/finance/runway_actions_integration_test.go `
  docs/modules/action/README.md docs/modules/finance/README.md
git commit -m "feat(finance): execute approved runway threshold changes"
```

---

## C3d — Inline Rules UI and Deployment Closure

### Task 10: Add typed frontend Query/preview/Action clients

**Files:**
- Create: `web/apps/admin-web/src/api/runwayThresholds.ts`
- Create: `web/apps/admin-web/src/api/runwayThresholds.test.ts`
- Modify: `web/apps/admin-web/src/api/config.ts`

**Interfaces:**
- Consumes: C3a/b HTTP contracts and C3c generic Action endpoint.
- Produces: typed query functions, query keys, preview mapping, and submit helper.

- [ ] **Step 1: Write failing client tests**

Assert exact paths, snake_case mapping, environment query behavior, AbortSignal forwarding, Action request body, and request ID header. Also assert `DEFAULT_SCOPES` does not include `finance.runway_threshold.manage`.

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

export const RUNWAY_THRESHOLD_MANAGE_PERMISSION = "finance.runway_threshold.manage";
export const RUNWAY_THRESHOLD_QUERY = "finance-runway-threshold";
export const RUNWAY_THRESHOLD_HISTORY_QUERY = "finance-runway-threshold-history";
export const RUNWAY_THRESHOLD_PREVIEW_QUERY = "finance-runway-threshold-preview";
```

Expose `getRunwayThresholds`, `listRunwayThresholdHistory`, `previewRunwayThresholds`, and `submitRunwayThresholdAction`. Do not add the permission to `DEFAULT_SCOPES`.

- [ ] **Step 4: Run client tests GREEN and commit**

```powershell
pnpm --config.verify-deps-before-run=false --filter admin-web test -- `
  src/api/runwayThresholds.test.ts
git add web/apps/admin-web/src/api/runwayThresholds.ts `
  web/apps/admin-web/src/api/runwayThresholds.test.ts `
  web/apps/admin-web/src/api/config.ts
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
| evidence order | resolve preview | observed time and coverage render before affected-object table |
| error retention | return 403, 409, then 503 from submit | draft fields and last preview remain visible after each error |
| missing scope | omit manage scope | submit disabled and missing scope named |
| F-B gate | include manage scope but catalog reports L2 unavailable | submit disabled and `ADVANCED_CONTROLS_REQUIRED` explained |
| verified success | submit approval, then return mismatched current before matching current | no green success until current+summary values/revision match |

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

The panel always shows current values, revision/source/time, serious explanation, and history. Editing is inline. “预览影响” fetches preview and renders counts/table inline. A centered confirmation Dialog repeats the proposed values, current revision, observation time, and impact counts. The write button says “提交审批”. No right-side Drawer is used.

- [ ] **Step 5: Implement permission and Foundation-B gates**

- `finance.read` controls Query visibility through service responses.
- Missing `finance.runway_threshold.manage` disables submission and names the missing scope.
- Presence of the scope does not imply F-B availability; Action catalog/risk state controls the second gate.
- A successful submission displays approval request status, not “配置已生效”.
- Only a later current+summary refetch with matching revision/values may display “写后验证通过”.

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

### Task 12: Cut over deployment and retire runtime env authority

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

1. back up DB and record current API/worker image digests;
2. read current WARN/CRIT env without printing secrets (these values are non-secret);
3. apply the dynamically numbered migration;
4. run `runway-threshold-bootstrap` once per environment;
5. Query current config and history; require revision 1 and exact effective values;
6. start one upgraded API replica, verify summary threshold revision/value;
7. start one upgraded worker, wait one evaluation interval, verify logs use the same revision;
8. roll remaining replicas only after equality evidence;
9. remove runtime env wiring in the final compose revision;
10. rollback by restoring old images while retaining DB rows; never delete history.

- [ ] **Step 2: Add a deployment contract test/search**

After cutover, this command must only find the old variables in the bootstrap command and historical/runbook explanation:

```powershell
rg -n 'XM_FINANCE_RUNWAY_(WARN|CRIT)_DAYS' cmd internal deploy docs
```

It must not find the names under `cmd/platform-api`, `cmd/platform-worker`, or runtime service environment blocks in `launch.yaml`.

- [ ] **Step 3: Remove the two runtime env entries**

Remove them from API/worker environment blocks and mark them bootstrap-only in `.env.example` until the rollout is complete. Do not add a runtime `RUNWAY_CONFIG_FALLBACK` switch.

- [ ] **Step 4: Run compose and docs verification**

```powershell
docker compose -p xingmang-launch -f deploy/compose/launch.yaml `
  --env-file deploy/compose/.env.example config --quiet
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

For C3a/C3c, also run the PostgreSQL integration tests with `XM_TEST_DATABASE_URL` and verify migration up/down on a disposable database. For C3d, rebuild the relevant staging containers and capture the rules page at desktop and narrow viewport, including no-scope, F-B-gated, preview, 409 conflict, and verified-success states.

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
