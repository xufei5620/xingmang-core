# Metric Downsampling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 保留至少 90 天原始指标样本，并交付可验证的 UTC 日粒度降采样、冷热历史查询与“聚合完成后才删除”的生命周期。

**Architecture:** 版本化 15-key policy 决定 integer/fixed-point/JSON/status 语义；独立 rollup identity 扫描 `NOT EXISTS receipt` 的 raw，并在同一事务写 daily、append-only receipt 与 success state。ID 只排序，不作提交水位。Legacy hours 保持 raw-only；range API opt-in raw/day/auto；删除逐行依赖 receipt 与独立硬门。

**Tech Stack:** Go 1.27、PostgreSQL 18、pgx/sqlc、River、React/TypeScript/Vitest、PowerShell/Git Bash；不新增数据库扩展。

**Spec:** `docs/superpowers/specs/2026-08-28-metric-downsampling-design.md`

## Global Constraints

- DS0 只交付 docs，不是实现授权。
- DS1/DS2 仅在 policy 与 exact migration diff 获批后，于 pinned disposable PostgreSQL 18 条件 GO；不得连接 staging/production。
- DS3 API/UI、DBR policy、River 多副本、staging backfill、raw DELETE 全部 NO-GO，分别另批。
- legacy `hours=1..168`、raw item、alerts consecutive-failure 语义不得回归。
- raw 至少保留 90 天；`XM_METRIC_RAW_DELETE_ENABLED` 默认 false，且独立于 downsampling enabled。
- money/count/fixed-point 全程整数；failed value 不进 numeric；partial 不冒充 full；当前 15-key snapshot 的 sum 全部为 null；两个 invoice key 未获 CR-0002 冻结即 STOP。
- v1 日桶固定 UTC；auto 固定当前 UTC 日 raw、此前完整日 daily。
- unknown key/policy/version fail closed；cadence/coverage unknown 如实返回 null，但不能替代逐行 receipt 删除证明。
- River unique 不是最终锁；per-stream state row lock + receipt unique key 必须存在；禁止 max-ID/`SKIP LOCKED` 生成水位。
- migration 号从实施时 fresh base 动态取得，exact diff 单独审批；一字节漂移即 STOP。
- 每片独立 worktree/branch/commit/PR；Codex 不 merge、不 deploy、不配置真实凭据。
- 每片门禁：`go fmt ./...`、`go vet ./...`、`go test -p 1 ./...`、`pnpm -r run typecheck`、`pnpm -r run test`、`pnpm --filter ui-storybook run build`、`bash scripts/check-governance.sh`。

## File Structure

### DS1 — policy/schema/pure accumulator

- Create: `contracts/ops/metric-rollup-policy.v1.json`
- Create: `internal/platform/ops/rollup_policy.go`
- Create: `internal/platform/ops/rollup_policy_test.go`
- Create: `internal/platform/ops/rollup.go`
- Create: `internal/platform/ops/rollup_test.go`
- Dynamically create: `db/migrations/${METRIC_DOWNSAMPLING_MIGRATION_ID}_metric_downsampling.up.sql`
- Dynamically create: `db/migrations/${METRIC_DOWNSAMPLING_MIGRATION_ID}_metric_downsampling.down.sql`
- Modify: `db/queries/ops.sql`
- Modify: `internal/platform/ops/store.go`
- Modify: `internal/platform/ops/store_test.go`
- Modify: `internal/platform/jobs/sub2api_sync.go`
- Modify: `internal/platform/jobs/newapi_sync.go`
- Modify: `internal/platform/jobs/cost_sync.go`
- Modify: corresponding job tests/config wiring（写入 effective cadence）
- Regenerate: `internal/platform/ops/gen/*`
- Create: `internal/platform/ops/rollup_schema_integration_test.go`
- Modify: `docs/modules/ops/DATA-MODEL.md`
- Modify: `docs/modules/ops/README.md`

### DS2 — receipt exactly-once store/manual CLI/disabled independent worker

- Create: `internal/platform/ops/rollup_store.go`
- Create: `internal/platform/ops/rollup_store_test.go`
- Create: `internal/platform/ops/rollup_store_integration_test.go`
- Create: `cmd/metric-rollup/main.go`
- Create: `cmd/metric-rollup/main_test.go`
- Create: `cmd/metric-rollup-worker/main.go`
- Create: `cmd/metric-rollup-worker/config.go`
- Create: `cmd/metric-rollup-worker/config_test.go`
- Create: `internal/platform/jobs/metric_rollup.go`
- Create: `internal/platform/jobs/metric_rollup_test.go`
- Modify: `internal/platform/jobs/client.go`
- Modify: `deploy/compose/.env.example`
- Create: `docs/evidence/TEMPLATE-metric-rollup-backfill.md`
- Create: `docs/runbooks/METRIC-ROLLUP.md`

### DS3 — Query/API + legacy UI honesty（range UI explicitly out；NO-GO）

- Create: `internal/platform/ops/history.go`
- Create: `internal/platform/ops/history_test.go`
- Create: `internal/platform/ops/history_integration_test.go`
- Modify: `db/queries/ops.sql`
- Modify: `internal/platform/httpapi/metrics_history.go`
- Modify: `internal/platform/httpapi/metrics_history_test.go`
- Modify: `cmd/platform-api/main.go`
- Modify: `web/apps/admin-web/src/api/platform.ts`
- Modify: `web/apps/admin-web/src/api/platform.test.ts`
- Modify: `web/apps/admin-web/src/lib/metrics.ts`
- Modify: `web/apps/admin-web/src/lib/metrics.test.ts`
- Modify: `web/apps/admin-web/src/components/MetricSparkline.tsx`
- Create: `web/apps/admin-web/src/components/MetricSparkline.test.tsx`
- Modify: `docs/modules/httpapi/README.md`

### DS4 — receipt-protected aggregate/prune/DBR/backup/staging（NO-GO）

- Modify: `internal/platform/ops/rollup_store.go`
- Modify: `internal/platform/ops/rollup_store_integration_test.go`
- Modify: `internal/platform/jobs/retention.go`
- Modify: `internal/platform/jobs/retention_test.go`
- Modify: `internal/platform/jobs/retention_integration_test.go`
- Modify: `internal/platform/jobs/client.go`
- Modify: `cmd/metric-rollup-worker/config.go`
- Modify: `cmd/metric-rollup-worker/config_test.go`
- Create: `cmd/metric-rollup-worker/README.md`
- Modify: `cmd/platform-worker/config.go`
- Modify: `cmd/platform-worker/config_test.go`
- Modify: `cmd/platform-worker/README.md`
- Create after approved DBR CR: next version after approved `contracts/database/role-policy.v*.json`
- Create after approved migration/DBR CR: dynamically numbered metric-rollup grants artifact
- Create: `tests/security/metric-rollup-database-roles.test.sh`
- Create: `tests/security/metric-rollup-backup-restore.test.sh`
- Modify: `.github/workflows/ci.yml`（pinned digest + dedicated disposable sentinel DB gate）
- Modify: `docs/runbooks/METRIC-ROLLUP.md`
- Create: `docs/evidence/TEMPLATE-metric-rollup-activation.md`

---

## 0. 固定 preflight

- [ ] **Step 1: Verify exact worktree/base**

```powershell
git rev-parse --show-toplevel
git branch --show-current
git rev-parse HEAD
git status --short --branch
```

Expected: 当前片独立 worktree、fresh release base、工作区干净；SHA 写入 Task/PR。

- [ ] **Step 2: Verify approval scope**

DS1 需 CR-0002 + 15-key exact policy/UTC/schema 批准；DS2 需 DS1 merged；DS3 需 API contract；
DS4 需 DBR/R2-10/backup/staging/delete 独立批准。缺任一项写 `BLOCKED:` 并 STOP。

- [ ] **Step 3: Capture evidence, not estimates**

记录 base、迁移最大号、15-key registry、PG image digest。staging size/cadence 只有获批
只读连接才采；否则写 `not_run`，不得复用 README 的旧 5-key 估算。

---

## DS1 — Policy, Schema, Pure Accumulator（conditional GO; disposable PG only）

### Task 1: Freeze metric-rollup policy v1

**Files:**
- Create: `contracts/ops/metric-rollup-policy.v1.json`
- Create: `internal/platform/ops/rollup_policy.go`
- Test: `internal/platform/ops/rollup_policy_test.go`

**Interfaces:**

```go
type ValueKind string
const ( ValueGauge ValueKind="gauge"; ValueDailySnapshot ValueKind="daily_snapshot"; ValueAdditiveDelta ValueKind="additive_delta"; ValueDocumentStatus ValueKind="document_status" )
type PrimaryKind string
const ( PrimaryCount PrimaryKind="count"; PrimaryMoneyMinor PrimaryKind="money_minor"; PrimaryFixedPoint PrimaryKind="fixed_point"; PrimaryNone PrimaryKind="none" )
type RollupPolicy struct {
    Version int16
    MetricKey string
    ValueKind ValueKind
    PrimaryKind PrimaryKind
    PrimaryJSONPointer, CurrencyJSONPointer, BusinessDayJSONPointer string
    Unit string
    Scale int64
    SumMode, BucketTimezone string
    ExpectedIntervalSeconds *int32
    FullValueRequired bool
    PartialValueAllowed bool
}
func LoadRollupPolicies([]byte) (map[string]RollupPolicy, string, error)
func RollupPolicyFor(map[string]RollupPolicy, string) (RollupPolicy, error)
```

- [ ] **Step 1: Write RED tests**

```go
func TestPolicyCoversExactlyRegisteredMetrics(t *testing.T)
func TestPolicyFreezesFifteenMetricKindsAndPointers(t *testing.T)
func TestInvoicePoliciesRequireFrozenCR0002(t *testing.T)
func TestPolicyForbidsSumForAllV1Snapshots(t *testing.T)
func TestPolicyRequiresUTCAndPositiveIntegerScale(t *testing.T)
func TestPolicyRejectsUnknownKeyVersionKindPointerAndFloatScale(t *testing.T)
func TestMoneyPolicyRequiresCurrencyPointer(t *testing.T)
func TestPolicyRequiresFullValueAndPartialValueFlags(t *testing.T)
```

- [ ] **Step 2: Confirm RED**

Run: `go test -p 1 ./internal/platform/ops -run 'TestPolicy|TestInvoicePolicies|TestMoneyPolicy' -count=1 -v`

Expected: FAIL because contract/loader do not exist.

- [ ] **Step 3: Implement literal contract and strict loader**

Copy spec §4.1 exactly（15 keys）. Reject duplicate/unknown/missing keys/fields, invalid
pointer/scale, non-UTC, missing full/partial flags, and additive sum on current snapshots.
Invoice CR evidence absent => loader/gate fails. Hash canonical policy bytes.

- [ ] **Step 4: Run GREEN and commit**

```powershell
go fmt ./internal/platform/ops
go test -p 1 ./internal/platform/ops -run 'TestPolicy|TestInvoicePolicies|TestMoneyPolicy' -count=1 -v
git add contracts/ops/metric-rollup-policy.v1.json internal/platform/ops/rollup_policy.go internal/platform/ops/rollup_policy_test.go
git commit -m "test(ops): freeze metric rollup policy"
```

STOP if any metric owner disputes kind, pointer, currency/day or sum semantics.

### Task 2: Draft schema migration and exact-diff STOP

**Files:**
- Create: `db/migrations/${METRIC_DOWNSAMPLING_MIGRATION_ID}_metric_downsampling.up.sql`
- Create: `db/migrations/${METRIC_DOWNSAMPLING_MIGRATION_ID}_metric_downsampling.down.sql`
- Modify: `db/queries/ops.sql`
- Regenerate: `internal/platform/ops/gen/*`
- Test: `internal/platform/ops/rollup_schema_integration_test.go`
- Modify: `internal/platform/ops/store.go`
- Modify: `internal/platform/ops/store_test.go`
- Modify: `internal/platform/jobs/sub2api_sync.go` and tests
- Modify: `internal/platform/jobs/newapi_sync.go` and tests
- Modify: `internal/platform/jobs/cost_sync.go` and tests

- [ ] **Step 1: Resolve fresh migration number**

```powershell
git fetch --no-tags origin 'refs/heads/release/v0.1-launch:refs/remotes/origin/release/v0.1-launch'
$releaseTip = git rev-parse origin/release/v0.1-launch
if ((git rev-parse HEAD) -ne $releaseTip) { throw 'STOP: DS1 must start from the exact fetched release tip' }
$paths = git ls-tree -r --name-only $releaseTip -- db/migrations
$numbers = $paths | ForEach-Object {
  if ($_ -match '^db/migrations/(?<number>\d+)_.*\.up\.sql$') { [int]$Matches['number'] }
}
$migrationMax = [int](($numbers | Measure-Object -Maximum).Maximum)
$env:METRIC_DOWNSAMPLING_MIGRATION_ID = '{0:D6}' -f ($migrationMax + 1)
Write-Output "release_tip=$releaseTip migration_max=$migrationMax next=$env:METRIC_DOWNSAMPLING_MIGRATION_ID"
```

- [ ] **Step 2: Write schema RED tests before DDL**

```go
func TestRollupSchemaHasRawPolicyAndRetentionIndex(t *testing.T)
func TestDailySchemaRejectsNonUTCInvalidCountsMixedMoneyAndForbiddenSum(t *testing.T)
func TestDailyUniqueKeySeparatesSourceAndPolicyVersion(t *testing.T)
func TestReceiptIsAppendOnlyAndUniquePerRollupSample(t *testing.T)
func TestStateIsPerEnvironmentMetricAndHasNoScalarDeleteFrontier(t *testing.T)
func TestMigrationPreservesExistingRawRowsAndHistoryQuery(t *testing.T)
func TestPeriodicWritesPersistExplicitPolicyVersionAndEffectiveCadence(t *testing.T)
func TestCadenceChangeAndLegacyNullRemainDistinguishable(t *testing.T)
func TestEveryConfiguredPeriodicWriterPropagatesItsEffectiveCadence(t *testing.T)
```

Run: `go test -tags=rolluppg -p 1 ./internal/platform/ops ./internal/platform/jobs -run 'TestRollupSchema|TestDailySchema|TestReceipt|TestState|TestMigrationPreserves|TestPeriodicWrites|TestCadenceChange|TestEveryConfiguredPeriodicWriter' -count=1 -v`

Expected: FAIL because raw additions/daily/receipt/state do not exist.

- [ ] **Step 3: Draft exact DDL/query bytes and STOP**

Add only spec §6 schema（raw metadata + daily + receipt + per-stream state）. Compute
`git diff --binary -- db/migrations db/queries/ops.sql | git hash-object --stdin`.
Record base, exact paths and digest. Do not apply/sqlc/edit runtime before human approves exact bytes.
Base/number/one-byte change invalidates approval.

- [ ] **Step 4: Apply approved bytes to pinned disposable PG18**

```powershell
go tool sqlc generate
git diff --exit-code -- sqlc.yaml
go test -tags=rolluppg -p 1 ./internal/platform/ops ./internal/platform/jobs -run 'TestRollupSchema|TestDailySchema|TestReceipt|TestState|TestMigrationPreserves|TestPeriodicWrites|TestCadenceChange|TestEveryConfiguredPeriodicWriter' -count=1 -v
```

The harness must create and exclusively own a random Compose project/container/network/volume
using the approved PostgreSQL 18 image digest, publish only a random `127.0.0.1` port, derive the
internal DSN from matching project/container labels, and destroy the whole project/volume in
`finally`. It must Fatal（never Skip）before any SQL unless label, RepoDigest, PG major 18, fresh
catalog, database-comment sentinel and migration version/dirty all match；external/admin DSNs,
fixed ports and `0.0.0.0` are rejected. Update Observation/Store and every enabled periodic writer to pass the
committed policy version plus the interval actually used by that writer into each raw INSERT;
never rely on the DDL DEFAULT. A changed interval affects only later rows. Expected: existing raw
unchanged with cadence null；new raw explicitly populated；daily/receipt/state empty；all constraints green.

- [ ] **Step 5: Commit exact schema slice and STOP**

Fetch release again and require its exact tip, migration max/number and approved diff digest to
match the Step 3 approval; `merge-base` alone is insufficient. Any movement means discard the
numbered files and restart from a fresh worktree. Stage the exact two migration files, query/gen,
writer code/tests, schema test and ops docs. Stop for migration/security review；do not start DS2
from unmerged DS1.

### Task 3: Build pure integer accumulator

**Files:** `internal/platform/ops/rollup.go`, `internal/platform/ops/rollup_test.go`.

**Interfaces:**

```go
type RawRollupSample struct {
    ID int64
    MetricKey, Source, Environment string
    ObservedAt *time.Time
    SyncedAt time.Time
    Status SyncStatus
    IsPartial bool
    Watermark, LastErrorCode string
    Value map[string]any
    PolicyVersion int16
    ExpectedIntervalSeconds *int32
}
type DailyAccumulator struct {
    Environment, MetricKey, Source string
    BucketDay time.Time
    PolicyVersion int16
    FirstNumeric, LastNumeric, MinNumeric, MaxNumeric, SumNumeric *big.Int
    NumericCount, SampleCount, FullSuccessCount, PartialSuccessCount, FailedCount int64
    ExpectedSlotCount, CoveredSlotCount *int64
    FirstFullValue, LastFullValue, LastPartialValue map[string]any
    MinSampleID, MaxSampleID int64
}
func NewDailyAccumulator(RollupPolicy, RawRollupSample) (DailyAccumulator, error)
func (DailyAccumulator) Merge(RollupPolicy, []RawRollupSample) (DailyAccumulator, error)
```

- [ ] **Step 1: Write RED tests**

```go
func TestAccumulatorUsesSyncedAtUTCDayAndIDTieBreak(t *testing.T)
func TestFailedOldValueNeverEntersNumeric(t *testing.T)
func TestPartialSuccessStaysSeparateFromFull(t *testing.T)
func TestSnapshotFirstLastMinMaxButNeverSum(t *testing.T)
func TestMoneyMixedCurrencyOmitsNumeric(t *testing.T)
func TestIntegerAboveTwoToThe53AndFixedPointStayExact(t *testing.T)
func TestDocumentKeepsRepresentativeJSONWithoutArrayAggregation(t *testing.T)
func TestCoverageUsesSlotsAndUnknownCadenceReturnsNull(t *testing.T)
func TestAccumulatorRejectsDuplicateOrNonIncreasingSampleID(t *testing.T)
```

- [ ] **Step 2: Confirm RED then implement exact integers**

Run targeted tests. Parse with `json.Number`/decimal integer; never Float64. Use
`(synced_at,id)` first/last, disjoint quality counts, sorted errors and exact source/policy/day.

- [ ] **Step 3: Run deterministic GREEN and DS1 gates**

Run targeted tests twice, then global gates. Evidence records policy hash, migration digest and
disposable PG identity. **STOP:** no DS2/API/staging/DBR/delete authorization.

---

## DS2 — Exactly-once Store, CLI, Disabled Worker（conditional GO; disposable PG only）

### Task 4: Implement receipt-backed, state-locked RollupBatch

**Files:**
- Create: `internal/platform/ops/rollup_store.go`
- Test: `internal/platform/ops/rollup_store_test.go`
- Test: `internal/platform/ops/rollup_store_integration_test.go`
- Modify: `db/queries/ops.sql`
- Regenerate: `internal/platform/ops/gen/*`

**Interfaces:**

```go
type RollupBatchResult struct {
    SamplesRead, DailyRowsTouched, ReceiptsInserted, LateSamples int64
    HighestReceiptSampleID int64 // diagnostic only; never a scan/delete frontier
}
type RollupStream struct { RollupName, Environment, MetricKey string }
type RollupState struct {
    RollupName, Environment, MetricKey, PolicyHash, Status, LastErrorCode string
    ReceiptCount, HighestReceiptSampleID int64
    LastCompletedBucketEnd, LastRunAt, LastSuccessAt, LastFailureAt *time.Time
}
func (s *Store) RollupBatch(context.Context, RollupStream, int32) (RollupBatchResult, error)
func (s *Store) RollupState(context.Context, RollupStream) (RollupState, error)
func (s *Store) RecordRollupFailure(context.Context, RollupStream, time.Time, string) error
```

- [ ] **Step 1: Write RED tests**

```go
func TestRollupBatchLocksStateAndScansOnlyRowsWithoutReceipt(t *testing.T)
func TestLowerIDCommittedAfterHigherIDIsProcessedNextRound(t *testing.T)
func TestDailyReceiptAndSuccessStateRollbackTogether(t *testing.T)
func TestRetryAfterAmbiguousCommitDoesNotDoubleCount(t *testing.T)
func TestConcurrentRollupsCannotBothAdvanceWatermark(t *testing.T)
func TestConcurrentRollupsSerializePerStreamAndReceiptIsUnique(t *testing.T)
func TestLateSampleUpdatesOldUTCDay(t *testing.T)
func TestUnknownPolicyWritesNoDailyReceiptOrSuccessState(t *testing.T)
func TestPolicyVersionChangeCreatesSeparateAccumulator(t *testing.T)
func TestBackfillAllUnreceiptedRowsMatchesRawReference(t *testing.T)
func TestFailureRecordUsesSeparateTransactionAndCannotOverwriteLaterSuccess(t *testing.T)
```

- [ ] **Step 2: Implement receipt scan and one success transaction**

`SELECT state FOR UPDATE` -> select this stream's raw rows for which no matching receipt exists,
ordered by ID with a hard batch limit -> merge/validate daily -> upsert daily -> insert one
append-only receipt per exact raw row -> update success state -> COMMIT. The input query must not
use `id > scalar_frontier` or `SKIP LOCKED`; ID is ordering/diagnostic only. Keep every DELETE
absent from DS2. On rollback/error, record a bounded stable error code in a separate small
transaction only if it cannot overwrite a later `last_success_at`; that transaction never writes
daily/receipt counts or deletion eligibility.

- [ ] **Step 3: Run true PG fault/concurrency tests**

Run: `go test -tags=rolluppg -p 1 ./internal/platform/ops -run 'TestRollupBatch|TestLowerID|TestDailyReceipt|TestRetry|TestConcurrent|TestLate|TestUnknownPolicy|TestPolicyVersion|TestBackfill|TestFailureRecord' -count=1 -v`

Reuse the DS1 harness-owned pinned cluster; no caller-provided admin DSN is accepted. The low-ID
test holds transaction A after identity allocation, commits higher-ID transaction B,
runs rollup, then commits A and proves the next round creates A's receipt. Test prelude must Fatal
rather than Skip unless the pinned disposable PG18 version/digest/sentinel/migration checks pass.
Expected: shared/business DB rejected and all fault boundaries leave no split daily/receipt/state.

### Task 5: Add constrained CLI and a disabled independent rollup worker

**Files:** `cmd/metric-rollup/main.go`, `cmd/metric-rollup/main_test.go`,
`cmd/metric-rollup-worker/main.go`, `cmd/metric-rollup-worker/config.go`,
`cmd/metric-rollup-worker/config_test.go`, `internal/platform/jobs/metric_rollup.go`,
`internal/platform/jobs/metric_rollup_test.go`, `internal/platform/jobs/client.go`,
`deploy/compose/.env.example`,
`docs/runbooks/METRIC-ROLLUP.md`, `docs/evidence/TEMPLATE-metric-rollup-backfill.md`.

```text
metric-rollup plan --policy <committed-policy-path>
metric-rollup run --policy <committed-policy-path> --expected-policy-hash <64hex>
metric-rollup verify --policy <committed-policy-path>
```

- [ ] **Step 1: Write RED tests**

```go
func TestMetricRollupDisabledByDefault(t *testing.T)
func TestCLIRejectsUnknownPolicyHashAndArbitrarySQL(t *testing.T)
func TestWorkerUsesMaintenanceQueueStableUniquenessAndStateLock(t *testing.T)
func TestWorkerUsesIndependentRollupPoolAndCredentialRef(t *testing.T)
func TestCollectorCredentialCannotStartRollupWorker(t *testing.T)
func TestOutputContainsNoValueJSONMoneyOrDSN(t *testing.T)
```

- [ ] **Step 2: Implement no-delete plan/run/verify**

CLI has no table/SQL/range override. `XM_METRIC_DOWNSAMPLING_ENABLED` defaults false. The
independent process owns a small separate pool and `XM_METRIC_ROLLUP_DATABASE_*` CredentialRef;
it must not reuse the collector/platform-worker identity. Before R2-10, production periodic is
not registered and compose contains no enabled rollup service；manual disposable execution only.

- [ ] **Step 3: Backfill, restore, and STOP**

Backfill disposable raw, verify reference parity, rerun no-op, add late samples, run concurrent
workers, dump/restore, verify again. Commit DS2. **STOP:** no API/compose activation/DBR/staging/delete.

---

## DS3 — Hot/Cold Query, HTTP, UI（NO-GO until contract approval）

### Task 6: Implement raw/day/auto repository

**Files:** `internal/platform/ops/history.go`, `internal/platform/ops/history_test.go`,
`internal/platform/ops/history_integration_test.go`, `db/queries/ops.sql`, `internal/platform/ops/gen/*`.

**Interfaces:**

```go
type Resolution string
const (
    ResolutionRaw Resolution = "raw"
    ResolutionDay Resolution = "day"
    ResolutionAuto Resolution = "auto"
)
type HistoryRequest struct {
    Environment, MetricKey string
    From, To time.Time
    Resolution Resolution
    Limit int32
    Cursor string
}
type HistoryPoint struct {
    Resolution Resolution
    SortAt time.Time
    Raw *Observation
    Day *DailyAccumulator
}
type CoverageSegment struct { From, To time.Time; Resolution Resolution; Complete bool }
type HistoryCoverage struct {
    Complete bool
    RequestedFrom, RequestedTo, EffectiveFrom, EffectiveTo time.Time
    RawAvailableFrom, RawAvailableTo, DailyAvailableFrom, DailyAvailableTo *time.Time
    Segments []CoverageSegment
    Sources []string
    PolicyVersions []int16
    PolicyHashes []string
    ReceiptCount, HighestReceiptSampleID int64
    ReceiptVerifiedAt *time.Time
    SampleCount, FullSuccessCount, PartialSuccessCount, FailedCount int64
    UnknownReasons []string
}
type HistoryPage struct {
    Items []HistoryPoint
    Coverage HistoryCoverage
    Truncated bool
    Limit int32
    NextCursor string
}
type HistoryQuery interface { ListHistory(context.Context, HistoryRequest) (HistoryPage, error) }
```

- [ ] **Step 1: Write repository RED tests**

```go
func TestLegacyHoursUsesRawOnly(t *testing.T)
func TestDayUsesDailyAndAutoUsesCompleteDaysThenCurrentUTCDayRaw(t *testing.T)
func TestMixedBoundaryHasNoOverlapOrGapAtUTCMidnight(t *testing.T)
func TestMergeHasDeterministicGlobalOrderAndLimit(t *testing.T)
func TestCursorBindsEnvironmentMetricRangeResolutionAndPolicyHash(t *testing.T)
func TestCursorUsesStrictOpenTupleAndRejectsReplayAcrossPolicy(t *testing.T)
func TestRequiredTierFailureReturnsNoPartialPage(t *testing.T)
func TestCoverageReportsAvailabilitySegmentsSourcesPoliciesReceiptsAndQuality(t *testing.T)
func TestAlertsConsecutiveFailuresStillUsesRawListSamples(t *testing.T)
```

- [ ] **Step 2: Confirm RED**

Run: `go test -p 1 ./internal/platform/ops ./internal/platform/alerts -run 'TestLegacyHours|TestDayUses|TestMixedBoundary|TestMerge|TestCursor|TestRequiredTier|TestCoverage|TestAlertsConsecutive' -count=1 -v`

- [ ] **Step 3: Implement explicit raw/day types and merge**

Do not represent daily as `ops.Observation`；callers must not invoke Freshness on aggregates.
For auto, freeze `boundary = start of the current UTC day`: only complete days strictly before
the boundary come from daily, and `[boundary,effective_to)` comes from raw. Read required tiers,
merge by the spec tuple, apply one global latest-point budget, and return ascending. The opaque
cursor encodes the oldest tuple in the page and the next page uses a strict open comparison;
environment/metric/range/resolution/policy mismatch fails closed. Unknown/lagging required daily
state fails day/auto explicitly；legacy raw stays available.

- [ ] **Step 4: Run PG parity and STOP**

At an evaluation time such as `2026-08-28T15:20Z`, prove 25/26/27 use daily and only
28 00:00–15:20 uses raw, with no overlap/gap. Also prove legacy raw parity, source/policy
changes, same-time IDs, empty ranges, strict cursor replay rejection and the 1000-point boundary.
Stop for product/backend contract review；DS3 remains NO-GO.

### Task 7: Extend HTTP and close frontend page/source gaps

**Files:** `internal/platform/httpapi/metrics_history.go`,
`internal/platform/httpapi/metrics_history_test.go`, `cmd/platform-api/main.go`,
`web/apps/admin-web/src/api/platform.ts`, `web/apps/admin-web/src/api/platform.test.ts`,
`web/apps/admin-web/src/lib/metrics.ts`, `web/apps/admin-web/src/lib/metrics.test.ts`,
`web/apps/admin-web/src/components/MetricSparkline.tsx`,
`web/apps/admin-web/src/components/MetricSparkline.test.tsx`, `docs/modules/httpapi/README.md`.

- [ ] **Step 1: Write HTTP RED tests**

```go
func TestHistoryLegacyHoursResponseRemainsCompatible(t *testing.T)
func TestHistoryRejectsHoursWithRangeAndInvalidResolutionCursor(t *testing.T)
func TestHistoryRangeReturnsDiscriminatedItemsCursorAndCoverage(t *testing.T)
func TestHistoryNeverDropsSourceTruncatedLimitOrCoverage(t *testing.T)
func TestHistoryCoverageIncludesAvailabilityPoliciesReceiptVerificationQualityAndReasons(t *testing.T)
func TestHistoryScopesEnvironmentAndKnownMetric(t *testing.T)
```

- [ ] **Step 2: Write frontend RED tests**

```typescript
it("returns the full history page instead of dropping truncated and limit")
it("types and preserves source on every raw and daily point")
it("orders raw points by synced_at then id and does not claim observed_at ordering")
it("renders truncated or incomplete coverage visibly")
it("marks source and policy version transitions")
it("keeps legacy 24h and 168h sparkline behavior")
```

- [ ] **Step 3: Implement additive opt-in contract**

Legacy hours remains raw-only. `from/to/resolution` is mutually exclusive with hours. New items
are discriminated raw/day；daily shows quality/coverage, never synthetic freshness.
`listMetricHistory` returns `MetricHistoryPage`, not `items` alone.

This slice intentionally does **not** add a range/resolution page, date control or cursor UX.
It delivers the backend/API contract plus honest legacy sparkline handling only. A browsable
history page and its paging controls require a separately approved UI slice after DS3 evidence.

- [ ] **Step 4: Run backend/frontend gates and STOP**

Commit only after product/backend/UI contract approval and all gates. Merge does not authorize
staging data, DBR, worker activation or deletion.

---

## DS4 — Aggregate-and-Prune, DBR, Backup, Staging（NO-GO）

### Task 8: Replace direct prune with RollupAndPruneBatch

**Files:** `internal/platform/ops/rollup_store.go`,
`internal/platform/ops/rollup_store_integration_test.go`, `internal/platform/jobs/retention.go`,
`internal/platform/jobs/retention_test.go`, `internal/platform/jobs/retention_integration_test.go`,
`internal/platform/jobs/client.go`, `cmd/metric-rollup-worker/config.go`,
`cmd/metric-rollup-worker/config_test.go`, `cmd/metric-rollup-worker/README.md`,
`cmd/platform-worker/config.go`, `cmd/platform-worker/config_test.go`,
`cmd/platform-worker/README.md`（旧进程删除 raw 的路径必须被移除）.

**Interface:**

```go
type RollupPruneResult struct {
    RollupBatchResult
    RawRowsDeleted, ReceiptsMatched int64
    HasMoreEligible bool
    Cutoff time.Time
}
func (s *Store) RollupAndPruneBatch(context.Context, time.Time, int32, int32) (RollupPruneResult, error)
```

- [ ] **Step 1: Write deletion RED tests**

```go
func TestPruneNeverDeletesRawWithoutExactReceiptDailyAndPolicyProof(t *testing.T)
func TestPruneFindsAndAggregatesLowIDLateCommitBeforeDeleting(t *testing.T)
func TestPruneRollbackRestoresDailyReceiptStateAndRawAtEveryBoundary(t *testing.T)
func TestPruneSeparatesUnknownCoverageFromReceiptedDeletionProof(t *testing.T)
func TestPruneDeletesZeroOnUnknownPolicyOrReceiptStateRegression(t *testing.T)
func TestPruneBacklogProbeDoesNotInferEmptyFromShortOrLockedBatch(t *testing.T)
func TestRawDeleteKillSwitchDefaultsFalseAndIsIndependent(t *testing.T)
func TestRetentionLogsReceiptLagAggregateAndDeleteCounts(t *testing.T)
func TestRetentionStillNeverTouchesAuditOrUnresolvedAlerts(t *testing.T)
```

- [ ] **Step 2: Implement same-transaction aggregate-before-delete**

Hold the per-stream state row lock, perform the same missing-receipt aggregation, then delete only
rows older than the cutoff for which an exact append-only receipt points to the matching daily
row and policy hash/version. Do not use a scalar ID frontier or `SKIP LOCKED` to create deletion
eligibility. Cadence/coverage may remain honestly unknown for legacy rows, but a row-level receipt
plus daily/policy proof is the separate deletion fact. Use an explicit `EXISTS eligible raw`
backlog probe; a short batch never means empty. Any unknown policy or receipt/daily/state mismatch
returns failure and zero raw deletes；old direct prune is unreachable.
Remove `SamplePruner`/raw-delete wiring from the existing platform-worker retention process；that
process keeps only its separately approved resolved-alert cleanup. Raw aggregation/deletion is
owned solely by the independently credentialed metric-rollup-worker.

- [ ] **Step 3: Fault-inject on true PG**

Create >90d/boundary/fresh rows, low-ID late commits, cadence-null legacy rows and concurrent runs.
Fail after daily upsert, receipt insert, success-state update and delete；each rollback must restore
daily/receipt/state/raw. Compare daily to raw reference and prove unreceipted rows never disappear.

### Task 9: Add exact DBR and backup gates

**Files:** the next version after the latest approved `contracts/database/role-policy.v*.json`,
the dynamically numbered approved metric-rollup grants artifact,
`tests/security/metric-rollup-database-roles.test.sh`,
`tests/security/metric-rollup-backup-restore.test.sh`,
`docs/evidence/TEMPLATE-metric-rollup-activation.md`, `docs/runbooks/METRIC-ROLLUP.md`.

- [ ] **Step 1: STOP for approved DBR version and CR**

Resolve the latest approved DBR merge SHA and current policy version, then publish a new policy
version through an independent CR/exact diff. Unknown version/role/object fails closed. Do not
edit an earlier policy in place, edit an unmerged DBR path, or fold permissions into lifecycle/admin.

- [ ] **Step 2: Prove real-role grants**

```text
collector: raw INSERT yes；raw UPDATE/DELETE no
API: raw/daily/receipt/state SELECT yes；all DML no
independent rollup worker: raw SELECT/DELETE + daily/receipt/state SELECT/INSERT/UPDATE as exact table policy allows；receipt UPDATE/DELETE and raw UPDATE/TRUNCATE no
collector/platform-worker identity: no daily/receipt/state DML and no raw DELETE
backup: raw/daily/receipt/state/identity-sequence SELECT yes；DML no
```

Denied operations must return SQLSTATE 42501.

- [ ] **Step 3: Run full backup/restore drill**

Use separate source-backup, target-migrator and one-time target-restore identities. Dump one
consistent snapshot containing raw/daily/receipt/state/identity sequence plus committed policy
bytes/checksum; run exact migrations on a fresh pinned PG18 target; restore data/sequence with
the one-time identity and revoke it. Prove next sequence value is strictly greater than raw max ID,
then compare receipt counts/daily/raw/history/cursor and run no-op plus low-ID-late-commit rollups.
Tests must Fatal rather than Skip unless PG18 version, pinned image digest, disposable sentinel and
exact migration version are verified; CI supplies the DS-approved pinned digest rather than a tag.

- [ ] **Step 4: Prepare staging packet and STOP**

Require exact image/compose/migration/policy hashes, DS1–DS3 exact-head CI, R2-10, DBR,
backfill lock/WAL/size evidence, kill switches, monitoring, rollback and human window.
No staging command is executed by this task.

### Task 10: Human staging soak and final delete gate

**HUMAN EXECUTION ONLY** after separate staging approval:

1. restore-tested backup；
2. exact migration with raw delete false；
3. backfill and daily/raw parity；
4. read-only day/auto canary against legacy；
5. soak at least one retention interval with zero policy/receipt-state/coverage incidents；
6. second explicit approval for `XM_METRIC_RAW_DELETE_ENABLED=true`；
7. one deletion batch canary, Query/backup/lag verification, then bounded continuation；
8. production remains a further approval.

Any mismatch, skipped PG test, missing restore, stale policy hash, rollup lag, source/coverage loss
or unexpected lock/WAL means NO-GO and delete remains false.

---

## Final Requirement-to-Evidence Audit

| Requirement | Evidence |
|---|---|
| 15-key policy + invoice gate | literal contract + exact registry/pointer/sum/CR tests |
| integer/fixed-point | >2^53/int64/numeric tests + no-float search |
| failed/partial | old-value exclusion + disjoint count tests |
| UTC day | midnight/leap/same-time ID tests |
| daily/receipt/state schema | migration constraints/index/owner/ACL evidence |
| exactly-once/late | retry/concurrency/fault/backfill/late tests |
| aggregate-before-delete | same-tx daily/receipt/state/raw rollback + row-level receipt proof + kill switch |
| Query/API | legacy parity + mixed no-gap + cursor/limit/truncated/coverage |
| frontend | legacy sparkline source/truncation/full-page mapping honesty；range UI remains separate |
| River/R2-10 | uniqueness + DB lock + merged cluster ownership evidence |
| DBR | exact positive/42501 negative matrix |
| backup/rollback | fresh-cluster restore + raw-off rollback evidence |
| staging/delete | soak + second explicit approval + canary evidence |

Any missing/stale/indirect/skipped evidence leaves that phase incomplete. “Daily rows exist”
never implies “raw can be deleted.”
