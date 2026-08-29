# R2-15 Connector Source-Budget Implementation Plan

> **Approval artifact only.** Do not execute a slice until a human approves the named slice and
> dependencies. Each slice uses its own worktree/PR. Codex does not merge, connect real sources,
> configure credentials, change intervals or deploy.

**Goal:** ensure every Connector run has an explicit finite cost and every source scope has a
cluster-wide rate/concurrency authority, persisted next-due/backoff evidence and zero-request
fail-closed behavior.

**Spec:** `docs/superpowers/specs/2026-08-29-r2-15-connector-budget-design.md`

## Global Gates

- R215-0 is docs-only and authorizes no implementation/request.
- R215-1 has no migration/network/real credential and preserves current runtime behavior.
- R215-2 is harness-owned pinned disposable PG18 only；migration/DBR/policy exact diff separately approved.
- R215-3 integration starts one connector/capability per PR in shadow；shadow never adds a request.
- R215-4 real staging needs R2-10, source owner, explicit ceiling/window and rollback approval.
- No interval may become faster merely because code is deployed；policy starts at current or slower.
- Unknown policy/route/version/provider scope/budget backend is zero upstream requests.
- All retries/pages/bytes/rows/cost count toward one run hard total.
- No manual refresh/reset/edit UI；writes wait Foundation-B.
- Destructive usage pop waits R2-10 receipt + R2-15 and is absent from R215-1/2/3.
- R2-15 is incomplete until R215-4 accepted.

## Files by Slice

### R215-1 — static policy and pure guard

- Create: `contracts/connectors/budgets.v1.json`
- Create: `internal/platform/connector/budget_policy.go`
- Create: `internal/platform/connector/budget_policy_test.go`
- Create: `internal/platform/connector/budget.go`
- Create: `internal/platform/connector/budget_test.go`
- Create: `internal/platform/connector/limited_body.go`
- Create: `internal/platform/connector/limited_body_test.go`
- Create: `internal/platform/connector/backoff.go`
- Create: `internal/platform/connector/backoff_test.go`
- Modify: Connector packages to expose static route/capability specs only
- Create: `docs/evidence/TEMPLATE-connector-budget-inventory.md`
- Modify: `docs/modules/connector/README.md`

### R215-2 — PostgreSQL source authority/poll state

- Dynamically create: `db/migrations/${CONNECTOR_BUDGET_MIGRATION_ID}_connector_budget.up.sql`
- Dynamically create: `db/migrations/${CONNECTOR_BUDGET_MIGRATION_ID}_connector_budget.down.sql`
- Create/modify: `db/queries/connector_budget.sql`
- Regenerate: `internal/platform/connector/gen/*`
- Create: `internal/platform/connector/budget_store.go`
- Create: `internal/platform/connector/budget_store_test.go`
- Create: `internal/platform/connector/budget_store_integration_test.go`
- Create: `cmd/connector-budget-bootstrap/main.go`
- Create: `cmd/connector-budget-bootstrap/main_test.go`
- Modify after DBR CR: next database role policy version/grants tests
- Create: `tests/security/connector-budget-database-role.test.sh`
- Create: `docs/runbooks/CONNECTOR-SOURCE-BUDGET.md`

### R215-3 — connector/job integration and shadow evidence

- Modify one at a time: Sub2API, NewAPI, metering clients/tests
- Modify: `internal/platform/jobs/sub2api_sync.go`/tests
- Modify: `internal/platform/jobs/newapi_sync.go`/tests
- Modify: `internal/platform/jobs/cost_sync.go`/tests
- Modify: `internal/platform/connector/types.go`
- Modify: `cmd/platform-worker/config.go`/tests/main logging
- Modify: `internal/platform/ops` metric registry only after metric contract approval
- Optional after UI/data approval: read-only budget panel/tests

### R215-4 — staging packet

- Create: `docs/evidence/TEMPLATE-connector-source-budget-acceptance.md`
- Create: `docs/runbooks/SWITCH-CONNECTOR-BUDGET-ENFORCEMENT.md`
- Deployment/config changes only in a human-approved staging slice.

---

## 0. Preflight

- [ ] Verify exact worktree/base/branch/clean status and approvals.
- [ ] Fetch exact release tip and record Go/River/PG/image/contract versions.
- [ ] Generate current outbound route/capability/page/request maxima from source；manual list and code
      must match. Unknown callsite is STOP.
- [ ] Record real-source tests/credentials/network as `not_run` unless explicitly approved.
- [ ] Confirm R2-10 state；multi-replica enforcement is NO-GO without accepted ownership evidence.
- [ ] No source endpoint/body/header/cursor/credential value in evidence output.

## R215-1 — Static Policy and Pure Guard

### Task 1: Freeze exact capability/route inventory

```go
type RouteSpec struct { ConnectorType, Capability, RouteID, Method, PathTemplate string }
func RegisteredRouteSpecs() []RouteSpec
```

- [ ] RED tests compare policy to every current outbound callsite and target-version contract：

```go
func TestBudgetPolicyCoversEveryRegisteredRouteExactlyOnce(t *testing.T)
func TestCurrentSub2APINewAPIMeteringCapabilitiesHaveFiniteRunCaps(t *testing.T)
func TestV1BaseEqualsMinAndNeverAcceleratesCurrentCadence(t *testing.T)
func TestCountHeavyAndPaginationRoutesHaveExplicitCost(t *testing.T)
func TestUnknownRouteVersionProviderScopeAndWriteCapabilityFailClosed(t *testing.T)
func TestFutureCPAUsageIsDestructiveAndDisabled(t *testing.T)
```

- [ ] Expose static safe route specs without changing request behavior. No raw endpoint host/query.
- [ ] Freeze literal v1 policy with current-or-lower maxima. Exact current high caps require human
      review；do not infer approval from source constants.
- [ ] Strict loader rejects unknown fields, overflow, zero/unbounded cap, retry totals beyond hard
      run totals, unknown cost/cursor/coverage mode and duplicate route/capability.
- [ ] Commit contract/loader only. No network/migration/config.

### Task 2: Build pure BudgetRun state machine

```go
type RunCounters struct { Requests, Pages, Rows, Bytes, CostUnits, Attempts int64 }
type RunEvidence struct { Plan RunPlan; Counters RunCounters; OutcomeCode string; Coverage CoverageEvidence }
type Guard interface { BeforeRequest(context.Context, string, int64) error; ObserveResponse(string,int,int64,int64,*time.Duration) error; ObservePage(string,string,int64) error; Finish(RunOutcome) RunEvidence }
```

- [ ] RED tests at N-1/N/N+1 for every cap；checked arithmetic/negative input；first request included；
      retries/cost/body included；run deadline/context；cursor repeat/cycle；partial coverage/reason；
      finish once/idempotent evidence；zero request on preflight failure.
- [ ] One monotonic state machine；connector cannot reset counters between helper calls/pages.
- [ ] Stable budget errors have no URL/query/body/provider text. Evidence is deterministic/sorted.
- [ ] Pure fake authority injects allow/deny decisions；no DB yet.

### Task 3: Hard response body limiter

- [ ] RED: Content-Length over cap, chunked overflow, exact boundary, gzip/decompression growth,
      slow body/context cancel, close-on-overflow and decoder partial/error classification.
- [ ] Cap applies to decoded bytes actually consumed；compressed wire cap may be separately lower.
- [ ] Read limit is installed before JSON decoder and cannot be bypassed by helper/read-all path.
- [ ] Overflow never includes body prefix in error/log.

### Task 4: Backoff/Retry-After/Poll transition pure reference model

- [ ] RED for delta-seconds/HTTP-date/invalid/negative/over-cap Retry-After；DB/reference time；
      unavailable exponential；rate limit max(calc,header)；auth/not-supported suspend；bad response；
      partial；success changed/unchanged/recovery；deterministic jitter and overflow.
- [ ] Valid over-cap Retry-After enters manual suspend and never retries earlier at the cap.
- [ ] RetryPolicy max attempts includes first request and is clipped by run request/cost/time caps.
- [ ] Produce pure `NextPollState`；no ticker/sleep/DB/network.
- [ ] Commit R215-1 after full connector package tests/governance. Runtime unchanged.

## R215-2 — PostgreSQL Authority and Poll State

### Task 5: Draft exact migration and STOP

- [ ] Fetch release, require HEAD exact tip, compute dynamic next migration from fetched tree.
- [ ] Write true-PG RED tests before DDL：schema constraints, append-only history, policy revision 1,
      provider-scope current/history binding, bucket/lease/state, secure functions, DB-time, DBR grants, up/down/up.
- [ ] Draft exact spec §7/§8 schema/functions/queries and input-test bytes. Hash exact paths/base/tip/
      migration max/number. STOP for migration/security approval before apply/sqlc/runtime.
- [ ] After approval, apply only in harness-owned digest-pinned PG18 random project/volume/loopback
      port. External/admin DSN, wrong labels/digest/version/sentinel or Skip is fatal.

### Task 6: Implement atomic source reservation

```go
type Reservation struct { LeaseID, Fence string; Allowed bool; RetryAfter time.Duration; PolicyRevision int64; ExpiresAt time.Time }
type SourceBudgetAuthority interface { Reserve(context.Context, RunPlan, int /* attempt no */, string /* route ID */, int64 /* units */) (Reservation,error); Complete(context.Context,string /* lease */,string /* fence */,RunOutcome) error }
```

- [ ] RED true-PG：variable-unit burst/refill, two pools/replicas, provider-scope sharing, max
      concurrent, idempotent same run/attempt and conflicting reuse, lease expiry/crash,
      late/wrong-fence completion, DB restart/time
      rollback, timeout/lock/pool saturation, policy version and bounded cleanup.
- [ ] Binding lookup starts from `core.service.id` UUID, joins authoritative environment/type and
      derives scope；caller instance/scope mismatch,
      unknown generation/type and unapproved regroup fail. Regroup tests prove old leases drain and
      TAT/concurrency state cannot reset to a fresh burst.
- [ ] Post-response completion failure prevents another request while lease active；after expiry a
      repeated read is budgeted and classified `evidence_commit_failed`, never hidden as exactly-once.
- [ ] Go reference model and SQL share literal vectors：burst N admits exactly N unit-cost requests,
      one U-unit request equals sequential units, U>burst/overflow fail, retry-after rounds up.
- [ ] Fixed SECURITY DEFINER functions use `pg_catalog,connector,pg_temp`, fully qualified objects,
      no dynamic SQL, PUBLIC none；DBR verifier checks proconfig/owner/grants.
- [ ] API/worker has EXECUTE only；ops/backup safe view；no raw bucket/lease DML.
- [ ] Any DB error returns typed unavailable；call path tests assert zero secret resolve/HTTP.

### Task 7: Implement persisted PollState

- [ ] RED: due/not_due DB clock, restart persistence, success/unchanged/failure/rate-limit/auth/
      not-supported/partial transitions, policy drift, deterministic jitter, stale retry-after,
      credential-generation unchanged/incremented, concurrent tick and failure rollback.
- [ ] State update is one transaction with run outcome/lease completion；unknown commit retries by
      run ID and does not schedule duplicate source request.
- [ ] Tick reads/locks state；not_due returns before secret/network. Dynamic per-instance goroutine is absent.
- [ ] Scheduler tick remains cluster singleton through accepted R2-10 manifest/job ID.
- [ ] RED/ready gate requires policy scheduler tick exactly equals the accepted R2-10 effective
      manifest；mismatch stops before secret/source and requires both approvals to change.

### Task 8: Lifecycle bootstrap revision 1 and DBR

- [ ] CLI accepts committed policy path/hash, approved provider-scope bindings and the accepted
      signed R2-10 fleet/effective-job manifest；it proves all replicas share the scheduler tick and
      policy base equals current-or-slower. No caller-supplied arbitrary interval/SQL/table/rate override.
- [ ] Revision 1 insert-only；same bytes idempotent；different bytes/row exist STOP. Later changes
      require Action/history/CR, not lifecycle overwrite.
- [ ] DBR next policy version separately approved；positive EXECUTE/SELECT and 42501 direct DML/
      TRUNCATE/DDL/SET ROLE tests.
- [ ] Dedicated budget-pool config/application_name, acquire+query deadline and full-fleet
      `max_connections` reserve formula require load evidence；pool saturation stays zero-request fail closed.
- [ ] Full backup/restore proves policy/history/bucket/lease/poll state and safe recovery.
- [ ] Commit R215-2 and STOP. No connector integration/staging.

## R215-3 — One Connector at a Time, Shadow First

### Task 9: Extend safe Connector error metadata

- [ ] RED: Retry-After/status class/attempt/route ID preserved；body/header/URL/query/token absent；
      errors.Is/KindOf unchanged；serialization never leaks cause.
- [ ] Parse Retry-After with response Date/DB reference and accepted bound；invalid gets stable
      reason and valid over-bound values enter manual suspension, never downward capping.
- [ ] Freeze a positive non-secret credential generation in each real Connector registration/run
      plan. Missing/zero generation fails before secret resolve；only an approved increment clears
      auth suspension.

### Task 10: Integrate Sub2API in pure shadow

Shadow observes the requests current code would already make and computes counters/decisions；it
does not reserve/deny/sleep/add/retry or change interval.

- [ ] RED fake upstream exact requests/pages/rows/bytes/cost for every Sub2API capability, caps,
      repeated page, timeout/429/auth/partial and no-secret evidence.
- [ ] Route each request through one Run Guard; no helper creates a new guard/counter.
- [ ] Compare observed current maxima to approved policy；unexpected route/cost is alert/NO-GO.
- [ ] Commit Sub2API-only shadow. NewAPI/metering unchanged.

### Task 11: Integrate NewAPI and metering in separate PRs

- [ ] NewAPI tests include 50/10/20 page caps, count-heavy topup/error-rate weights, filtered log
      counts, full-body prohibition and partial coverage.
- [ ] Metering tests combine per-account/token calls under explicit provider scope and cost.
- [ ] Finance + platform sync sharing one provider scope is replayed offline to prove combined policy.
- [ ] Each PR has independent shadow evidence and no additional source requests.

### Task 12: Enforce only against fake/canary and add read-only evidence

- [ ] Wire DB authority only in disposable/fake mode. `shadow`/`enforce` config is strict；production
      rejects enforce until signed staging activation artifact exists.
- [ ] Fail-closed tests assert policy/DB/lease/not_due before SecretProvider/HTTP.
- [ ] Add metric/run evidence contract only after approval；unknown/malformed fail closed.
- [ ] Optional UI/data slice shows budget/next_due/counters/coverage/errors with no controls/Drawer.
- [ ] Stop before real staging.

## R215-4 — Human Staging Acceptance

### Task 13: Prepare staging packet

Require exact instance/provider-scope owner, source owner consent, current request baseline, hard
ceiling/window, Connector/policy/migration/DBR/R2-10 hashes, credentials configured by owner,
shadow evidence, fake fault tests, rollback and monitoring. No command runs in prior slices.

### Task 14: HUMAN low-volume sequence

1. backup/verify policy and rollback；
2. deploy shadow only；prove zero request delta and compare decisions；
3. enable enforcement for one low-cost capability/instance；
4. observe multiple base intervals；no faster polling；
5. test not_due/budget/concurrency using controlled canary, not production overload；
6. if owner approves, observe one natural/synthetic Retry-After path；
7. verify partial/source/freshness/UI/alerts and DB lease cleanup；
8. rollback on source SLO, unexpected request, auth, coverage, DB latency or evidence mismatch。

R2-15 is complete only after accepted evidence. Production and each additional capability remain
new approvals.

## Optional UI Slice

- filters before compact search；platform/instance/capability rows；
- cost class/policy/provider scope, base/current/min/max, last/next/circuit/reason；
- requests/pages/rows/bytes/cost vs cap, lease/concurrency and coverage/freshness/source；
- loading/live/empty/error/stale/permission/partial/malformed；
- no refresh-now/interval/reset/edit/disable/secret/cursor/raw endpoint/Drawer；
- responsive and keyboard tests。

## Rollback

- Disable enforce/adaptive mode and restore approved image/policy/interval；
- keep Retry-After and source hard caps honored during rollback；
- scale to one worker or retain R2-10 fencing；
- preserve policy/history/lease/poll evidence；do not truncate/reset to regain burst；
- mark R2-15 incomplete/degraded；
- production rollback uses a new change record。

## Final Requirement-to-Evidence Matrix

| Requirement | Evidence |
|---|---|
| every capability declares cost | exact route/capability inventory parity tests |
| request/page/row/byte/time limits | N-1/N/N+1 guard/body/pagination tests |
| cluster source rate/concurrency | two-pool PG reserve/lease/failure tests |
| adaptive poll/backoff | DB-time state transition/restart/Retry-After/jitter tests |
| single-flight | R2-10 job ownership + provider-scope authority + no manual path |
| zero-request fail closed | secret/HTTP spies on every policy/DB/lease/not_due failure |
| partial honesty | covered/expected/reason/source/freshness evidence tests |
| no high-frequency logs | route/cost inventory + minimal filtered/count-only tests |
| observability/UI | run evidence/metric/alert/read-only state tests |
| P0 completion | accepted owner-approved R215-4 staging/rollback packet |

Any missing, stale, indirect or skipped evidence leaves R2-15 incomplete.
