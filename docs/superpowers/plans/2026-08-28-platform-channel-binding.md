# Platform Channel Binding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **APPROVAL GATE:** This plan is not executable until a human approves every item in
> `docs/superpowers/specs/2026-08-28-platform-channel-binding-design.md` §15. MAP0 authorizes
> documentation review only, not implementation.

**Goal:** Restore the prototype's one-row-per-managed-channel grain using an explicit temporal binding to finance upstream accounts, without duplicate revenue or repeated shared-account totals.

**Architecture:** Connector observations remain channel identity and health truth; `finance.platform_channel_binding` stores only human-confirmed attribution. MAP1 supplies a complete directory, MAP2 adds temporal binding and Actions, MAP3 builds a fail-closed read projection, and MAP4 switches the UI while the existing account-grain endpoint remains compatible.

**Tech Stack:** Go 1.27, PostgreSQL 18, sqlc, Chi, React 19, TypeScript, TanStack Query/Table, Vitest, pnpm.

**Spec:** `docs/superpowers/specs/2026-08-28-platform-channel-binding-design.md`

## Global Constraints

- Human approval of all seven spec §15 items is required before creating MAP1.
- In particular, MAP1 requires one explicit approval covering both **Sub2API read v2** and the
  **NewAPI directory-completeness v2 envelope**; NewAPI row semantics remain v1. Approval of only
  one half is no approval, and MAP1 must not start.
- This plan assumes MI-1 approves **single observed instance, fail closed** for MAP1~MAP4.
- If human review chooses true multi-instance metrics, revise the spec and this plan first.
- Each MAP slice starts from the then-current `origin/release/v0.1-launch` in its own worktree/branch/PR.
- Reads use Query; binding writes use Action; migrations remain Platform Lifecycle Operations.
- No upstream source changes, production access, Keycloak mutation, plaintext credentials, or direct third-party writes.
- `finance.platform_channel_binding.manage` is absent from every default role.
- `/api/v1/finance/channels/summary` keeps its account-grain response through MAP4.
- Binding changes never rewrite historical `finance.profit_daily`.
- Channel identity gating uses independent directory completeness evidence; coverage/error-rate
  partial never blocks binding by itself.
- Historical economics match binding history by complete business-day interval; active binding is
  never applied retroactively, and an intra-day rebind makes that day fail closed.
- `external_channel_id` is trimmed before lookup/lock and must equal `btrim(...)` in the database.
- Money stays integer scale-6; ratios stay Decimal strings; unknown stays null, never zero.
- MAP2 resolves the migration number dynamically; it never assumes `000013` is free.
- Every slice runs targeted red/green tests and all repository gates before PR creation.
- Codex may push and create a PR after green verification, but never merges it.

## Slice dependency

```text
approved MAP0
  -> MAP1 complete channel directory
  -> MAP2 temporal binding + candidates + Actions
  -> MAP3 channel-grain read projection
  -> MAP4 prototype-grain UI
```

---

### Task 1: MAP1 — Complete managed-channel inventory

**Files:**
- Create: `contracts/connectors/sub2api.read.v2.md`
- Create: `contracts/connectors/newapi.channel-directory.v2.md`
- Create: `connectors/sub2api/channel_directory.go`
- Create: `connectors/sub2api/channel_directory_test.go`
- Create: `connectors/sub2api/contracttest/v2_suite.go`
- Modify: `connectors/sub2api/contract.go`
- Modify: `connectors/sub2api/client.go`
- Modify: `connectors/sub2api/upstream.go`
- Modify: `connectors/sub2api/fake.go`
- Modify: `connectors/sub2api/contract_test.go`
- Modify: `connectors/sub2api/client_contract_test.go`
- Create: `connectors/newapi/channel_directory.go`
- Create: `connectors/newapi/channel_directory_test.go`
- Modify: `connectors/newapi/contract.go`
- Modify: `connectors/newapi/client.go`
- Modify: `connectors/newapi/upstream.go`
- Modify: `connectors/newapi/fake.go`
- Modify: `connectors/newapi/client_contract_test.go`
- Modify: `internal/platform/jobs/sub2api_sync.go`
- Modify: `internal/platform/jobs/sub2api_sync_test.go`
- Modify: `internal/platform/jobs/newapi_sync.go`
- Modify: `internal/platform/jobs/newapi_sync_test.go`
- Modify: `internal/platform/jobs/client.go`
- Create: `internal/platform/jobs/channel_directory_factory_test.go`
- Modify: `internal/platform/ops/freshness.go`
- Modify: `internal/platform/ops/metrickeys_test.go`
- Modify: `docs/modules/connector/README.md`

**Interfaces:**
- Consumes: Sub2API v1 `Snapshot`, the read-only `/api/v1/admin/accounts` client, and `ops.Observation`.
- Produces:

```go
const (
    ContractVersionV1 = "1"
    ContractVersionV2 = "2"
    MetricChannelsStatus = "sub2api.channels.status"
)

type ManagedChannel struct {
    Snapshot
    ChannelID         string
    Name              string
    Status            string
    BalanceMinorUnits *int64
    Currency          string
}

type DirectoryCompleteness struct {
    Complete bool
    Truncated bool
    ReportedCount *int64
    FetchedCount int64
    Evidence string
}

type ManagedChannelDirectory struct {
    Snapshot
    Completeness DirectoryCompleteness
    CoveragePartial bool
    Items []ManagedChannel
}

type ReadClientV2 interface {
    ReadClient
    ChannelDirectory(context.Context) (ManagedChannelDirectory, error)
}

func ToChannelDirectoryObservation(
    now time.Time, instanceID, environment string, directory ManagedChannelDirectory,
) ops.Observation
```

NewAPI keeps `ChannelStatus` and `ReadClient.Channels()` v1 row semantics. Its own directory-v2 type
duplicates the completeness fields above and wraps `[]ChannelStatus`; connectors do not import a
shared contract type. `newapi/contract.go` adds the same V1/V2 constants and a v2 interface embedding
v1; existing `newapi.channels.read` capability and row fields do not change. This exposes
reported/fetched/truncated independently from v1 `IsPartial`.
Both `connectors/*/NewClient`, both jobs factory types/constructors, both worker option/field types,
and `jobs.NewClient` are statically v2 end-to-end; legacy arrays are pure adapters from the v2
directory. Runtime type assertions and “try v2, fall back to v1” are forbidden.

- [ ] **Step 1: Create MAP1 only after MAP0 approval**

```powershell
git fetch origin
git worktree add K:/星芒统一控制平台/wt-xmC-MAP1 `
  -b ai/codex/XM-C-MAP1-channel-directory origin/release/v0.1-launch
```

Verify a clean worktree and that its HEAD is the latest release head.

- [ ] **Step 2: Freeze the v2 contract before code**

The contracts state: every Sub2API `/admin/accounts` item with a nonblank ID yields one row; missing
quota yields `BalanceMinorUnits=nil`; NewAPI reuses v1 `ChannelStatus` rows; both directory envelopes
carry independent complete/truncated/reported/fetched/evidence; field coverage partial is separate.
Sub2 v1 `ChannelBalances()` remains, v2 adds `sub2api.channels.read`, and neither v2 adds a write
method or credential-shaped field.

- [ ] **Step 3: Write failing v2 contract tests**

```go
func TestManagedChannelBalanceIsNullable(t *testing.T) {
    field, ok := reflect.TypeOf(sub2api.ManagedChannel{}).FieldByName("BalanceMinorUnits")
    if !ok || field.Type != reflect.TypeOf((*int64)(nil)) {
        t.Fatal("BalanceMinorUnits must be *int64")
    }
}

func TestV2DirectoryKeepsNoQuotaAccount(t *testing.T) {
    rows := directoryFixture(t, `[
      {"id":1,"name":"metered","status":"active","quota_limit":10,"quota_used":2},
      {"id":2,"name":"subscription","status":"active","quota_limit":null,"quota_used":null}
    ]`)
    if len(rows) != 2 || rows[1].BalanceMinorUnits != nil {
        t.Fatalf("directory=%+v", rows)
    }
}

func TestNewAPICoveragePartialDoesNotMeanDirectoryIncomplete(t *testing.T) {
    got := newAPIDirectoryFixture(t, reported(80), fetched(80), errorRatesMeasured(40))
    if !got.Completeness.Complete || !got.CoveragePartial {
        t.Fatalf("directory=%+v", got)
    }
}

func TestReportedCountUnknownDiffersFromExplicitZero(t *testing.T) {
    unknown := directoryCompleteness(nil, 0, naturalEnd())
    zero := directoryCompleteness(ptr(int64(0)), 0, reportedEnd())
    if unknown.ReportedCount != nil || zero.ReportedCount == nil || *zero.ReportedCount != 0 {
        t.Fatalf("unknown=%+v zero=%+v", unknown, zero)
    }
    if !unknown.Complete || !zero.Complete || unknown.Evidence == zero.Evidence {
        t.Fatalf("unknown and explicit zero need distinct completeness evidence")
    }
}
```

- [ ] **Step 4: Run the focused test and confirm red**

```powershell
go test ./connectors/sub2api/... ./connectors/newapi/... `
  -run 'ManagedChannel|V2Directory|DirectoryIncomplete|CoveragePartial' -count=1
```

Expected: FAIL because the v2 types/decoder do not exist.

- [ ] **Step 5: Implement one complete fetch plus a legacy projection**

Fetch `/admin/accounts` once into `[]ManagedChannel`. Derive the existing v1 list only from rows
whose balance is non-nil:

```go
func legacyBalances(rows []ManagedChannel) []ChannelBalance {
    out := make([]ChannelBalance, 0, len(rows))
    for _, row := range rows {
        if row.BalanceMinorUnits == nil { continue }
        out = append(out, ChannelBalance{
            Snapshot: row.Snapshot, ChannelID: row.ChannelID, ChannelName: row.Name,
            BalanceMinorUnits: *row.BalanceMinorUnits, Currency: row.Currency,
            TokenValid: strings.EqualFold(row.Status, "active"),
        })
    }
    return out
}
```

Trim IDs and reject blank IDs; keep status opaque; never decode credentials or account `extra`.

- [ ] **Step 6: Emit the full metric and retain v1**

`sub2api.channels.status` contains every channel row. Omit `balance_minor_units` when nil. Both
systems emit `inventory_completeness` separately from `coverage_partial`; NewAPI v1 `IsPartial`
never gates the directory. `sub2api_sync` writes new and legacy metrics from one fetch. The Sub2
factory returned to the Worker is statically `ReadClientV2`; Fake/real clients implement it and the
Worker contains no optional type assertion. Legacy v1 still omits nil-quota rows and records skipped
count + `IsPartial=true` without changing v2 completeness.

Change `connectors/sub2api.NewClient` and `connectors/newapi.NewClient` to return their v2 interfaces;
change `Sub2APIClientFactory`, `NewAPIClientFactory`, both factory constructors, worker option/field
types, and `jobs.NewClient` wiring to the same static types. `LegacyChannelBalances` and
`LegacyChannelStatuses` are pure adapters used to preserve v1 outputs from the one directory fetch.

- [ ] **Step 7: Run MAP1 verification**

```powershell
go test ./connectors/sub2api/... -count=1
go test ./connectors/newapi/... -count=1
go test ./internal/platform/jobs -run 'Sub2API|NewAPI' -count=1
go test ./internal/platform/ops -count=1
go fmt ./connectors/sub2api/... ./connectors/newapi/... ./internal/platform/jobs/... `
  ./internal/platform/ops/... ./cmd/platform-worker/...
go vet ./...
go test -p 1 ./...
bash scripts/check-governance.sh
```

Expected: all commands exit 0; v1 output remains compatible and v2 retains no-quota accounts.

- [ ] **Step 8: Commit and open MAP1 without merging**

```powershell
git add contracts/connectors/sub2api.read.v2.md `
  contracts/connectors/newapi.channel-directory.v2.md connectors/sub2api connectors/newapi `
  internal/platform/jobs/sub2api_sync.go internal/platform/jobs/sub2api_sync_test.go `
  internal/platform/jobs/newapi_sync.go internal/platform/jobs/newapi_sync_test.go `
  internal/platform/jobs/client.go internal/platform/jobs/channel_directory_factory_test.go `
  internal/platform/ops/freshness.go internal/platform/ops/metrickeys_test.go `
  docs/modules/connector/README.md
git commit -m "feat(connectors): add complete Sub2API channel directory"
```

The PR Handoff states that MAP2 waits for human merge; NewAPI row semantics remain v1 while only
the independent directory-completeness envelope is v2.

---

### Task 2: MAP2 — Temporal binding, four-state candidates, Actions, and Query

**Files:**
- Create dynamically: `db/migrations/$migrationNumber` + `_finance_platform_channel_binding.up.sql`
- Create dynamically: matching `.down.sql`
- Modify: `db/queries/finance.sql`
- Modify generated: `internal/platform/finance/gen/finance.sql.go`
- Modify generated: `internal/platform/finance/gen/models.go`
- Create: `internal/platform/finance/channel_binding.go`
- Create: `internal/platform/finance/channel_binding_test.go`
- Create: `internal/platform/finance/channel_binding_store.go`
- Create: `internal/platform/finance/channel_binding_store_integration_test.go`
- Create: `internal/platform/finance/channel_candidate.go`
- Create: `internal/platform/finance/channel_candidate_test.go`
- Modify: `internal/platform/finance/actions.go`
- Modify: `internal/platform/finance/actions_test.go`
- Modify: `internal/platform/finance/actions_integration_test.go`
- Modify: `internal/platform/finance/permissions.go`
- Modify: `internal/platform/action/errors.go`
- Modify: `internal/platform/action/errors_test.go`
- Create: `contracts/actions/finance.platform_channel_binding.set.v1.json`
- Create: `contracts/actions/finance.platform_channel_binding.remove.v1.json`
- Create: `internal/platform/httpapi/channel_bindings.go`
- Create: `internal/platform/httpapi/channel_bindings_test.go`
- Modify: `internal/platform/httpapi/response.go`
- Modify: `internal/platform/httpapi/response_test.go`
- Modify: `internal/platform/httpapi/router.go`
- Modify: `cmd/platform-api/main.go`
- Modify: `internal/platform/oidcauth/resolver_test.go`
- Modify: `docs/modules/finance/README.md`
- Modify: `docs/modules/httpapi/PERMISSIONS.md`

**Interfaces:**

```go
const ScopePlatformChannelBindingManage = "finance.platform_channel_binding.manage"
const ActionPlatformChannelBindingSet = "finance.platform_channel_binding.set"
const ActionPlatformChannelBindingRemove = "finance.platform_channel_binding.remove"

type ChannelRef struct {
    ServiceID uuid.UUID
    ExternalChannelID string
}

type CandidateState string
const (
    CandidateUnmapped CandidateState = "unmapped"
    CandidateProposed CandidateState = "candidate"
    CandidateConflict CandidateState = "conflict"
    CandidateOrphan CandidateState = "orphan"
)
```

- [ ] **Step 1: Create MAP2 from the human-merged MAP1 head**

Use worktree `K:/星芒统一控制平台/wt-xmC-MAP2` and branch
`ai/codex/XM-C-MAP2-channel-binding`; verify the MAP1 merge is an ancestor.

- [ ] **Step 2: Resolve the next migration number immediately before editing**

```powershell
$numbers = Get-ChildItem db/migrations -Filter '*.up.sql' |
  ForEach-Object { if ($_.Name -match '^(\d{6})_') { [int]$Matches[1] } }
$migrationNumber = '{0:D6}' -f ((($numbers | Measure-Object -Maximum).Maximum) + 1)
Get-ChildItem db/migrations -Filter "$migrationNumber*"
```

Expected: the last command returns no file. Use that value for both migration filenames; do not
replace it with a number copied from this plan.

- [ ] **Step 3: Write failing domain and integration tests**

Cover trimmed canonical IDs (`"1"` and `" 1 "` cannot coexist), four exact candidate states,
candidate evidence sufficiency, same-target idempotency, the complete expected-ID matrix, concurrent
first create/rebind, overlapping historical intervals rejected, many channels per upstream accepted,
and both cross-environment FK violations rejected.

- [ ] **Step 4: Confirm the red state**

```powershell
go test ./internal/platform/finance -run 'ChannelRef|Binding|Candidate' -count=1
```

Expected: FAIL because the domain/schema are absent.

- [ ] **Step 5: Apply the exact schema from spec §6**

The migration includes the table, `external_channel_id = btrim(external_channel_id)`, interval checks,
`provenance` enum check, composite service/account environment FKs, active partial unique index,
start unique index, upstream history index, and the two supporting composite candidate keys. It
inserts no confirmed binding.

- [ ] **Step 6: Add six sqlc queries and regenerate**

```text
InsertPlatformChannelBinding :one
GetActivePlatformChannelBinding :one
AcquirePlatformChannelBindingLock :exec
LockActivePlatformChannelBinding :one
ClosePlatformChannelBinding :one
ListOverlappingPlatformChannelBindings :many
ListActivePlatformChannelBindingsByService :many
ListPlatformChannelBindingHistory :many
```

Run `sqlc generate`; only the checked-in generated finance files may change.

- [ ] **Step 7: Implement transactional optimistic concurrency**

```go
type SetBindingInput struct {
    Environment string
    Channel ChannelRef
    UpstreamAccountID uuid.UUID
    ExpectedBindingID *uuid.UUID
    Provenance string
    Reason string
    CreatedBy string
}

func (s *ChannelBindingStore) Set(ctx context.Context, in SetBindingInput) (PlatformChannelBinding, bool, error)
func (s *ChannelBindingStore) Remove(ctx context.Context, environment string, ref ChannelRef, expected uuid.UUID, reason, actor string) error
```

`bool` means changed. Normalize before acquiring
`pg_advisory_xact_lock(hashtextextended(service_id::text || chr(31) || external_id,0))`; under that lock,
reject overlapping `[valid_from,valid_to)` history before close/insert. Same target + matching expected
ID is an audited no-op; stale/omitted expected ID is `ErrBindingConflict`; unique-index races map to
the same stable error. No-active + nonempty expected ID is also conflict.

- [ ] **Step 8: Implement the deterministic candidate evaluator**

```go
func EvaluateBindingCandidates(
    inventory InventorySnapshot,
    confirmed []PlatformChannelBinding,
    evidence []TokenMapEvidence,
) []ChannelCandidate
```

It returns `unmapped/candidate/conflict/orphan` in ChannelRef order plus
`EvidenceStatus=sufficient|insufficient|conflicting`. Token-map evidence can propose a candidate only
when that environment has exactly one active service of `ua.system_type` and its service type matches
the requested ChannelRef. Zero services yields orphan + insufficient `no_active_service`; multiple
services yields conflict + `ambiguous_service`; type mismatch, platform mismatch, or multiple targets
is also conflict. Directory incomplete/stale/source mismatch sets
`InventoryUnknown` and cannot create orphan; coverage partial alone does not.

- [ ] **Step 9: Add L1 HUMAN-only Action contracts and handlers**

Both contracts are version 1, require the new scope, declare `risk_level: L1`, `human_only: true`,
and `compensation_mode: MANUAL`. Set makes `expected_binding_id` optional-but-non-null; remove requires
it. Environment comes only from Principal. Handlers verify latest complete directory, service/account
environment, service state/type, current expected ID, and token evidence; they never mutate token
maps, `platform_id`, ledger rows, or third-party state. Set result always includes the current/new
`binding_id`; rebind also returns `previous_binding_id`; remove returns `removed_binding_id`.

- [ ] **Step 10: Add the binding Query and default-deny proof**

```http
GET /api/v1/finance/platform-channel-bindings?service_id=<uuid>&include_history=false&limit=50&cursor=...
```

Require `finance.read`; freeze the spec §10 response, sorting, history order, cursor, and 1..200
limit. Add `CONFLICT`→409 and `PRECONDITION_FAILED`→412 to Action/HTTP errors and verify every error
row in spec §10.4. Extend `TestDefaultRoleScopeMapIsConservative` to assert the new manage scope
appears in neither default staff nor default admin.

- [ ] **Step 11: Run MAP2 verification**

```powershell
go test ./internal/platform/finance -run 'Binding|Candidate' -count=1
go test ./internal/platform/httpapi -run ChannelBinding -count=1
go test ./internal/platform/oidcauth -run DefaultRoleScopeMapIsConservative -count=1
go fmt ./internal/platform/finance/... ./internal/platform/httpapi/... ./cmd/platform-api/...
go vet ./...
go test -p 1 ./...
bash scripts/check-governance.sh
```

Expected: all exit 0, including the repository's approved finance database integration recipe.

- [ ] **Step 12: Commit and open MAP2 without merging**

Stage the dynamically named migration explicitly and only this task's files. Commit:

```powershell
git commit -m "feat(finance): add temporal platform channel bindings"
```

The Handoff records the actual migration number, approval reference, default-deny scope evidence,
and that no production candidate was auto-confirmed.

---

### Task 3: MAP3 — Channel-grain projection with fail-closed economics

**Files:**
- Create: `internal/platform/finance/channel_projection.go`
- Create: `internal/platform/finance/channel_projection_test.go`
- Modify: `db/queries/finance.sql`
- Modify generated: `internal/platform/finance/gen/finance.sql.go`
- Create: `internal/platform/finance/channel_projection_store.go`
- Create: `internal/platform/finance/channel_projection_store_integration_test.go`
- Create: `internal/platform/httpapi/platform_channels.go`
- Create: `internal/platform/httpapi/platform_channels_test.go`
- Modify: `internal/platform/httpapi/router.go`
- Modify: `cmd/platform-api/main.go`
- Modify: `docs/modules/finance/README.md`
- Modify: `docs/modules/httpapi/README.md`

**Interfaces:**

```go
type ChannelProjectionQuery struct {
    Environment string
    ServiceID uuid.UUID
    From time.Time
    To time.Time
}

type ChannelEconomicsConflict string
const (
    ConflictDuplicateRevenue ChannelEconomicsConflict = "duplicate_revenue"
    ConflictUpstreamMismatch ChannelEconomicsConflict = "upstream_mismatch"
)

func (s *ChannelProjectionStore) List(
    context.Context, ChannelProjectionQuery,
) ([]PlatformChannelProjection, error)
```

- [ ] **Step 1: Create MAP3 from the human-merged MAP2 head**

Use worktree `K:/星芒统一控制平台/wt-xmC-MAP3` and branch
`ai/codex/XM-C-MAP3-channel-projection`; verify MAP2 is an ancestor.

- [ ] **Step 2: Write failing pure projector tests**

```go
func TestProjectionFailsClosedOnDuplicateRevenue(t *testing.T) {
    got := ProjectChannel(channelFixture(), bindingFixture(), []ProfitFact{
        {Day: day, RevenueMinor: ptr(int64(100)), UpstreamAccountID: accountA},
        {Day: day, RevenueMinor: ptr(int64(100)), UpstreamAccountID: accountA},
    }, nil)
    if got.Conflict != ConflictDuplicateRevenue || got.Economics != nil {
        t.Fatalf("projection=%+v; duplicate revenue must fail closed", got)
    }
}

func TestSharedRunwayTotalsDistinctAccounts(t *testing.T) {
    page := ProjectPage(twoChannelsBoundTo(accountA), oneRunway(accountA))
    if len(page.Items) != 2 || page.RunwayCoverage.Total != 1 {
        t.Fatalf("items=%d coverage=%+v", len(page.Items), page.RunwayCoverage)
    }
}
```

Add parallel assertions for an unmapped inventory row, one revenue with multiple token costs,
upstream mismatch, mixed currencies, missing sides, and a historical rebind.

Fixtures include two channels sharing one upstream, multiple token costs with one account revenue,
two known revenue rows for one channel/day, mixed currencies, missing sides, a full-day historical
binding, no historical binding, NULL/wrong platform, timezone disagreement, and an intra-day rebind.

- [ ] **Step 3: Confirm red before implementation**

```powershell
go test ./internal/platform/finance -run 'Projection|DuplicateRevenue|SharedRunway' -count=1
```

Expected: FAIL because the projector does not exist.

- [ ] **Step 4: Add the raw ledger Query**

In one `REPEATABLE READ READ ONLY` transaction, return ledger rows and binding history rather than
using active binding or a revenue SUM:

```sql
WHERE ua.environment = sqlc.arg(environment)
  AND pd.account_id = sqlc.arg(external_channel_id)
  AND pd.business_day BETWEEN sqlc.arg(from_day) AND sqlc.arg(to_day)
ORDER BY pd.business_day, pd.upstream_account_id, pd.token_id
```

Select upstream account, business day, token ID, nullable revenue/cost, currency, source, and both
observed timestamps plus `platform_id` and `business_day_tz`. Partition the raw set with all binding
histories for that external ID: rows fully matching another ChannelRef are excluded from this one;
NULL stays unattributed; a non-NULL value matching no historical ChannelRef is platform mismatch.
Regenerate sqlc and inspect nullable types.

- [ ] **Step 5: Select the binding that covered the full business day**

Convert each day through its one frozen `business_day_tz` to `[day_start,next_day_start)`. Exactly
one history interval must cover the full day and every ledger row must match its target. No interval
or NULL platform is `unattributed_history`; wrong platform/account, a gap, multiple timezones, or an
intra-day rebind returns explicit conflict and null economics. Never use current active binding,
observed timestamps, or updated_at to rewrite/split a daily fact.

- [ ] **Step 6: Implement exact per-day de-duplication**

```go
switch knownRevenueRows {
case 0:
    revenue = nil
case 1:
    revenue = onlyKnownRevenue
default:
    conflict = ConflictDuplicateRevenue
    revenue, cost, profit, margin = nil, nil, nil, nil
}
```

Require every ledger row's upstream account to equal the confirmed binding. Sum costs only when
all cost rows are known and currency is singular. Never use `SUM(DISTINCT revenue_minor)`.

- [ ] **Step 7: Join health/model without inventing data**

Sub2API uses v2 status and nullable balance; success, latency, model names, verified count, and
assurance remain null. NewAPI uses v1 enabled/error-rate/latency/model-count; model names and
assurance remain null. Inventory completeness and field coverage partial remain separate. A NewAPI
`bad_response` from illegal balance is a page-level `EXECUTION_FAILED`/502 with no rows; no per-row
invalid balance state is invented.

- [ ] **Step 8: Attach account-level runway and distinct totals**

Load upstream summaries once and index by account. Channel rows may repeat a runway reference, but:

```go
sharedCount := activeBindingCountByUpstream[accountID]
coverageTotal := len(distinctMeteredUpstreamAccountIDs)
```

Balances, most-urgent runway, and coverage are computed over distinct account IDs. Subscription
accounts remain `not_applicable` and are excluded from the denominator.

- [ ] **Step 9: Add the two-scope Query and single-instance gate**

```http
GET /api/v1/platforms/{platform}/channels?service_id=<uuid>&from=...&to=...&limit=50&cursor=...
```

Require both `ops.read` and `finance.read`. If service ID is omitted, resolve only when exactly one
active service of that type exists; zero is `not_connected`, multiple is `ambiguous_service`.
Observation source must equal `service.instance_id`. Implement spec §10 sorting, cursor, 1..200
limit, 92-day window, response objects, and error codes exactly.

- [ ] **Step 10: Protect the legacy endpoint**

Add a regression test proving `/api/v1/finance/channels/summary` still returns upstream-account UUID
rows with its existing schema. C001 totals continue using it.

- [ ] **Step 11: Run MAP3 verification**

```powershell
go test ./internal/platform/finance `
  -run 'Projection|DuplicateRevenue|SharedRunway|HistoricalBinding|IntraDayRebind|Unattributed' -count=1
go test ./internal/platform/httpapi -run 'PlatformChannels|SummaryCompatibility' -count=1
go fmt ./internal/platform/finance/... ./internal/platform/httpapi/... ./cmd/platform-api/...
go vet ./...
go test -p 1 ./...
bash scripts/check-governance.sh
```

Expected: all exit 0; duplicate/mismatch fixtures return null finance cells and explicit conflicts.

- [ ] **Step 12: Commit and open MAP3 without merging**

```powershell
git add db/queries/finance.sql internal/platform/finance/gen `
  internal/platform/finance/channel_projection* `
  internal/platform/httpapi/platform_channels* internal/platform/httpapi/router.go `
  cmd/platform-api/main.go docs/modules/finance/README.md docs/modules/httpapi/README.md
git commit -m "feat(finance): project economics by managed channel"
```

The Handoff states that Collector write semantics were unchanged and reports detected legacy
conflicts as blockers rather than repaired data.

---

### Task 4: MAP4 — Restore the prototype channel-row UI

**Files:**
- Create: `web/apps/admin-web/src/api/platformChannels.ts`
- Create: `web/apps/admin-web/src/api/platformChannels.test.ts`
- Modify: `web/apps/admin-web/src/lib/channelTable.ts`
- Modify: `web/apps/admin-web/src/lib/channelTable.test.ts`
- Modify: `web/apps/admin-web/src/lib/metrics.ts`
- Modify: `web/apps/admin-web/src/lib/metrics.test.ts`
- Modify: `web/apps/admin-web/src/components/ChannelTable.tsx`
- Modify: `web/apps/admin-web/src/components/ChannelTableColumns.tsx`
- Modify: `web/apps/admin-web/src/components/ChannelScopeNote.tsx`
- Modify: `web/apps/admin-web/src/components/ChannelsPanel.tsx`
- Modify: `web/apps/admin-web/src/components/ChannelsPanel.test.tsx`
- Modify: `web/apps/admin-web/src/components/NewApiChannelsPanel.tsx`
- Modify: `web/apps/admin-web/src/router.test.tsx`

**Interfaces:**

```ts
export interface ChannelRef {
  serviceId: string;
  externalChannelId: string;
}

export type CandidateState = "unmapped" | "candidate" | "conflict" | "orphan";
export type CandidateEvidenceStatus = "sufficient" | "insufficient" | "conflicting";

export interface PlatformChannelRow {
  channelRef: ChannelRef;
  name: string;
  binding: { id: string; upstreamAccountId: string } | null;
  candidateState: CandidateState | null;
  candidateEvidenceStatus: CandidateEvidenceStatus;
  economics: ChannelEconomics | null;
  health: ChannelHealth | null;
  runway: SharedRunway | null;
}
```

- [ ] **Step 1: Create MAP4 from the human-merged MAP3 head**

Use worktree `K:/星芒统一控制平台/wt-xmC-MAP4` and branch
`ai/codex/XM-C-MAP4-channel-table-grain`.

- [ ] **Step 2: Write failing API mapping tests**

Test the frozen response schema, nullable money, all candidate/evidence states, conflicts, shared
count, inventory complete versus coverage partial, and NewAPI's unconfigured versus known-zero
balance. A fixture with two channels sharing one upstream must produce two frontend rows. A 502
`EXECUTION_FAILED` fixture must produce page error with no stale/per-row fallback. Separate fixtures
assert `reported_count:null` remains unknown while `reported_count:0` remains explicit zero.

- [ ] **Step 3: Confirm API tests are red**

```powershell
pnpm --config.verify-deps-before-run=false --filter admin-web test -- platformChannels.test.ts
```

Expected: FAIL because the API module is absent.

- [ ] **Step 4: Implement API mapping without financial arithmetic**

Map backend strings/nulls, pagination cursor, and error codes into typed rows/PageState. Do not
calculate revenue, cost, profit, margin, coverage, or runway in TypeScript; only format server
results. Do not invent an invalid-balance row state: NewAPI malformed balance is page-level error
until a separately approved v2 contract says otherwise.

- [ ] **Step 5: Switch row identity to ChannelRef**

```ts
export function channelRowKey(row: PlatformChannelRow): string {
  return `${row.channelRef.serviceId}:${row.channelRef.externalChannelId}`;
}
```

Never key by upstream account ID. Views and filters keep unmapped/conflict rows visible.

- [ ] **Step 6: Write failing component and C001 regression tests**

Assert two shared-account channels remain two rows; unmapped shows `未映射` rather than zero;
candidate/conflict/orphan are explicit; shared runway says `共享余额 · 共 2 渠道`; Sub2API
unsupported success/model cells say `未接入 · M1.5`; NewAPI v1 health retains freshness. Also assert
C001 totals still call the legacy account-grain endpoint and never sum channel rows. Directory
incomplete disables binding UI; coverage partial only marks affected health fields. A NewAPI
`EXECUTION_FAILED` response renders the page error state and zero channel rows.

- [ ] **Step 7: Confirm component tests are red**

```powershell
pnpm --config.verify-deps-before-run=false --filter admin-web test -- ChannelsPanel.test.tsx
```

Expected: the XM-0052 account-grain implementation fails the new row-grain assertions.

- [ ] **Step 8: Update shared and platform-specific UI**

Use `DataTableV2`, `PageState`, `FreshnessBadge`, semantic design tokens, and
`formatScaledMinorUnits`. The scope note says one row equals one managed platform channel and shared
upstream balance is a reference, not an amount to sum.

- [ ] **Step 9: Run frontend and governance gates**

```powershell
pnpm --config.verify-deps-before-run=false --filter admin-web test -- `
  platformChannels.test.ts channelTable.test.ts ChannelsPanel.test.tsx router.test.tsx
pnpm --config.verify-deps-before-run=false -r run typecheck
pnpm --config.verify-deps-before-run=false -r run test
pnpm --config.verify-deps-before-run=false --filter ui-storybook run build
bash scripts/check-governance.sh
```

Expected: all exit 0.

- [ ] **Step 10: Perform proportional browser verification**

When Docker/browser is available, rebuild only the web service and verify both platforms at 1024px
and desktop widths: body width does not overflow, table container owns horizontal scroll, row count
equals Connector inventory, shared upstream rows do not duplicate page totals. If unavailable, record
the exact not-run reason and make no visual-success claim.

- [ ] **Step 11: Commit and open MAP4 without merging**

```powershell
git add web/apps/admin-web/src/api/platformChannels* `
  web/apps/admin-web/src/lib/channelTable* web/apps/admin-web/src/lib/metrics* `
  web/apps/admin-web/src/components web/apps/admin-web/src/router.test.tsx
git commit -m "feat(admin): restore managed-channel row grain"
```

The Handoff includes the prototype grid-to-source-state mapping, test counts, visual evidence or
not-run reason, remaining M1.5 unknowns, and C001 compatibility proof.

---

## Completion audit after MAP4

Before requesting human merge, prove all of the following with current evidence:

1. No duplicate active/canonical ChannelRef exists; cross-environment rows are structurally rejected.
2. Advisory-lock concurrency and overlap tests preserve non-overlapping history.
3. All four candidate states, service/type evidence sufficiency, and inventory unknown behavior pass.
4. Directory completeness and coverage partial are independent; New v1 IsPartial never gates identity.
5. Sub2 v2 real/fake factory and Worker wiring are static; legacy nil-quota partial remains compatible.
6. Query/Action schemas, sorting, pagination, include_history, expected-ID matrix, result IDs, and
   frozen error codes match the spec.
7. The new manage scope is absent from both default roles.
8. Full-day historical binding selection passes; intra-day rebind, gaps, NULL/wrong platform,
   wrong upstream, and timezone conflicts return null + evidence without using active binding.
9. Duplicate revenue and upstream mismatch produce null + explicit conflict.
10. Shared runway totals use distinct upstream accounts.
11. NewAPI invalid balance produces page-level 502/PageState error, not a made-up row state.
12. The legacy summary response and C001 totals are unchanged.
13. No binding Action changed token maps, `platform_id`, historical ledger, or third-party state.
14. Governance, secret-scan, backend, and frontend CI are all green.
15. Codex has not merged or deployed the PR.

Any failed item leaves the slice in progress; a partial green set does not prove the design complete.
