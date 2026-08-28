# R2-15 Connector 采集预算、自适应轮询与源站保护设计

> **状态：P0 审批设计，不是实现授权。** 本文冻结每个 Connector capability 的
> request/page/row/byte/time/cost/concurrency 预算、cluster-wide reservation、轮询状态、
> backoff/jitter、partial 证据与 staging 验收。它不授权迁移、真实凭据、加快采集、
> usage pop、staging/production 请求或外部写。

## 1. 结论

推荐四层预算：

1. versioned `ConnectorBudgetPolicyV1`：每 capability/route 的静态成本与硬上限；
2. per-run `BudgetGuard`：页/行/字节/请求/墙钟/重复 cursor 硬闸；
3. PostgreSQL `SourceBudgetAuthority`：跨副本/跨 job 的 provider-scope cost token +
   concurrent lease，DB 故障时 fail closed，不打上游；
4. `PollState`：DB-time next_due、Retry-After、failure backoff、deterministic jitter 和
   capability coverage。River tick 只检查 due；不 due 就不触碰凭据/上游。

现有 300s interval、分页上限和 read-only transport 是局部护栏，不是预算系统。R2-15
只有在 R215-4 的 staging shadow/enforcement/rollback 证据通过后才满足。

阶段裁决：

| 阶段 | 结论 |
|---|---|
| R215-0 本设计 | 可审批 |
| R215-1 static policy + pure guard/transport caps | 条件 GO；无迁移/真实请求 |
| R215-2 DB authority/poll state | NO-GO；migration/DBR exact diff另批；disposable PG only |
| R215-3 current connectors shadow/integration | NO-GO；contract/credential/fleet另批 |
| R215-4 staging source-protection acceptance | NO-GO；上游 owner同意/低量窗口/rollback |
| production | NO-GO；staging新证据后再批 |

## 2. 当前事实与缺口

### 2.1 已有护栏

- `ReadOnlyTransport` 只允许 GET/HEAD、exact host allowlist、无 allowlist fail closed；
- redirect 永久拒绝；HTTP client 有总 timeout；
- Connector error 已稳定分类 auth/rate_limited/unavailable/bad_response 等；
- jobs 由 River 周期任务登记，生产 RunID 为空且有 ByPeriod uniqueness；
- Sub2API 用户 1000/页、最多 20 页；账号 200/页、最多 10 页；
- NewAPI 列表 100/页；用户最多 50 页、渠道 10 页、充值 20 页；
- NewAPI 错误率每轮最多 40 渠道、每渠道两次日志 COUNT；
- probe 已尽量 page_size=1；超页时标 partial；
- 429 可分类，部分测试已有 Retry-After header。

### 2.2 缺口

- cost/page/route 常量散落，无法回答“一轮最坏打多少请求/字节/COUNT”；
- 没有统一 response-body byte cap、row cap、cursor stall/cycle guard；
- 没有 cluster-wide provider concurrency/rate authority；finance 与平台同步可打同一源站；
- 只有固定 interval，无 next_due、失败代次、Retry-After 持久态或 deterministic jitter；
- River job unique 防重复 JobRow，但一个 Job Work 内仍可高成本翻页；
- 不同 capability/instance/host 共享预算的 provider scope 未定义；
- auth/not-supported/bad-response/rate-limit/partial 的 backoff 语义未冻结；
- DB/预算故障时没有“绝不请求上游”的机械断言；
- UI只显示数据新鲜度，不显示请求/页/字节/预算/next due/退避原因；
- 没有受控源站/fake load staging 证据。

## 3. 术语

- **capability**：如 `newapi.users.snapshot`，不是 URL；
- **route ID**：versioned contract 中的稳定路由名，不是 raw URL；
- **provider scope**：预算共享边界，例如同一 CPA/NewAPI/Sub2API source account/host group；
- **cost unit**：相对源站代价整数，不是钱；一次昂贵 COUNT 可大于 1；
- **run budget**：一次 capability run 的硬上限；
- **source budget**：跨 run/replica/job 的 rate/concurrency；
- **tick**：River scheduler 唤醒；不等于一定发上游请求；
- **due**：DB authority 判定当前可运行；
- **partial**：硬预算前拿到一部分事实；不冒充 complete；
- **attempt**：一次实际 HTTP request，包括 retry。

## 4. ConnectorBudgetPolicyV1

```go
type RouteCost struct {
    RouteID, Method, PathTemplate string
    CostUnits, MaxResponseBytes, MaxRows int64
}
type RetryPolicy struct {
    MaxAttempts int
    InitialBackoffMS, MaxBackoffMS, MaxRetryAfterMS int64
    JitterPPM int32
    RetryKinds []string
}
type PollPolicy struct {
    SchedulerTickSeconds, BaseIntervalSeconds int64
    MinIntervalSeconds, MaxIntervalSeconds int64
    UnchangedStepsBeforeSlowdown, SuccessStepsBeforeRecovery int
}
type CapabilityBudget struct {
    ConnectorType, Capability, ProviderScopeRule, CostClass string
    MinConnectorVersion, MaxConnectorVersion string
    Routes []RouteCost
    MaxRequests, MaxPages, MaxRows, MaxBytes, MaxCostUnits int64
    MaxRunMillis, MaxConcurrentSource int64
    CursorMode, SingleFlightKey, CoveragePolicy string
    Retry RetryPolicy
    Poll PollPolicy
}
```

Strict rules：

- every actual outbound route belongs to exactly one capability/route ID；unknown route is denied；
- method/path/host remain ADR-004/018 exact facts；budget never expands allowlist；
- all counts/bytes/time/cost/concurrency positive and checked for overflow；
- v1 `min_interval_seconds == base_interval_seconds` and base is current-or-slower；adaptive logic
  may slow toward max or recover toward base, never poll faster than the pre-approved base；
- max attempts includes first request；retry cannot make run exceed any hard total；
- Retry-After within the accepted bound is persisted with DB time and not bypassed by restart；a
  valid over-bound value opens manual suspension rather than being capped downward；
- capability max is at most source policy max；
- page/row/byte/run/cost limits are independent；hitting one stops further requests；
- cursor/page must advance；repeat/cycle/out-of-order triggers partial/error；
- cost class is one of probe/light/page/count-heavy/destructive-consume/write；
- destructive-consume and write are denied in v1 unless their separate gates are present；
- unknown Connector version/policy revision/provider scope fails closed；
- real credential generation must come from an approved registry/config contract；no inferred
  default. If that contract is absent, auth-suspension recovery integration remains blocked；
- policy bytes immutable by version；changes publish next version/history/approval。

## 5. Current capability inventory baseline

R215-1 must generate a literal inventory from current code and target contracts. Current maxima are
evidence, not automatic approval：

| example capability | current code behavior to capture |
|---|---|
| Sub2API health/version | one minimal probe each |
| Sub2API users balance | up to 20 × 1000 rows |
| Sub2API accounts | up to 10 × 200 rows |
| NewAPI users | up to 50 × 100 rows |
| NewAPI channels | up to 10 × 100 rows |
| NewAPI recharge day | up to 20 × 100 rows, upstream repeats unbounded COUNT per page |
| NewAPI channel error rate | up to 40 channels × 2 COUNT requests |
| NewAPI logs/status probe | minimal filtered count；never log bodies |
| metering collectors | per registered account/token endpoint set |
| future CPA usage pop | destructive consume；disabled pending R2-10/R2-15 receipt |

Policy review may lower these maxima. A lower cap produces explicit partial coverage, not silent drop。

## 6. BudgetRun and hard enforcement

```go
type RunPlan struct {
    Environment, ServiceID, ServiceInstanceID, ProviderScope, Capability string
    PolicyVersion int
    CredentialGeneration int64
    RunID, ScheduledSlot string
}
type BudgetRun interface {
    BeforeRequest(context.Context, RouteID, int64 /* cost units */) error
    ObserveResponse(RouteID, status int, bytes, rows int64, retryAfter *time.Duration) error
    ObservePage(cursorIn, cursorOut string, rows int64) error
    Finish(outcome RunOutcome) RunEvidence
}
```

Enforcement occurs before secret resolution/request creation：

1. resolve the authoritative `core.service.id` from environment/type/instance and validate
   policy/capability/route/provider binding；
2. acquire source reservation/lease；
3. only then resolve CredentialRef and build request；
4. wrap body reader with hard byte limit；
5. connector reports rows/page/cursor；
6. every retry reacquires/consumes cost and total attempt budget；
7. release/complete lease and persist safe outcome/next_due；
8. zero request on any budget/policy/DB/lease failure。

Transport still independently rejects writes/hosts/redirects. Budget is an extra deny layer。

Stable run codes：`not_due`, `source_budget_wait`, `source_concurrency_full`, `run_request_limit`,
`run_page_limit`, `run_row_limit`, `run_byte_limit`, `run_cost_limit`, `run_deadline`,
`cursor_stalled`, `cursor_cycle`, `response_too_large`, `rate_limited`, `auth_suspended`,
`version_unsupported`, `budget_backend_unavailable`, `partial_budget_exhausted`, `complete`。

## 7. SourceBudgetAuthority

Provider scope is explicit registry/policy data, not inferred by hostname substring. Several Connector
instances may intentionally share one scope；wrong/unknown scope is STOP。

```text
connector.provider_scope_binding_current
  service_id uuid PRIMARY KEY REFERENCES core.service(id) ON DELETE RESTRICT,
  provider_scope, generation, policy_version, updated_at, change_ref

connector.provider_scope_binding_history (append-only)
```

Scope ID is non-secret ASCII ID, not hostname/account/key. Initial binding is lifecycle revision 1；
later regrouping is a separately approved Action/history change. It must drain old leases and carry
forward a conservative budget state so moving an instance cannot regain burst：quiesce the instance,
wait its old leases complete/expire, leave the shared old bucket unchanged, and initialize a new
scope bucket as empty or with a later approved TAT (never a full bucket). Authority joins the
`service_id` to `core.service` and requires its environment/service_type match the authenticated
run plan；caller-supplied instance/scope strings are evidence only and mismatch fails before reservation。

PostgreSQL authority uses DB time and fixed functions：

```text
connector.budget_policy_current
  environment, provider_scope, revision,
  refill_units_per_minute, burst_units, max_concurrent,
  lease_ttl_seconds, updated_at, change_ref

connector.budget_policy_history (append-only)

connector.budget_bucket
  environment, provider_scope, revision,
  tat_us, last_seen_us

connector.budget_lease
  lease_id, environment, provider_scope, capability, run_id, attempt_no, route_id,
  reserved_units, fence, acquired_at, expires_at, completed_at, outcome_code,
  UNIQUE(environment,provider_scope,run_id,attempt_no)
```

- `reserve_source_budget(environment, service_id, connector_type, capability, run_id, attempt_no, route_id, units)` joins the approved registry/binding and atomically enforces variable-unit
  GCRA/token budget + active unexpired lease count；returns lease/fence/retry-after；
- same `(run_id,attempt_no)` + same route/units retries return the original reservation without
  consuming twice；different facts are `reservation_conflict`；
- `complete_source_budget(lease_id, fence, outcome)` is idempotent and cannot release another attempt；
- reserved rate units are not refunded when later secret/build/transport fails；only concurrency is
  released. Conservative accounting prevents reserve/cancel loops from manufacturing burst；
- if completion/poll-state persistence fails after an HTTP response, the job records
  `evidence_commit_failed` and first reconciles the original lease on retry；it cannot issue a second
  request while that lease is active. After expiry a read may repeat (at-least-once) but remains
  budgeted/visible；external writes are not permitted by this design；
- expired lease is reclaimable only by DB time；late completion cannot corrupt a new lease；
- lease ID/fence is random/monotonic evidence, not user input；
- DB/SQL/timeout/pool failure is `budget_backend_unavailable` and no upstream request；
- API/worker gets EXECUTE only, no direct table DML/DDL/TRUNCATE；PUBLIC none；
- policy change uses lifecycle insert revision 1 only；later change requires Action/history/CR；
- rate/concurrency policy is source protection, not a promise of upstream quota。

Variable-unit GCRA is frozen with checked integer microseconds：

```text
interval_us = ceil(60_000_000 / refill_units_per_minute)
capacity_us = burst_units * interval_us
effective_now_us = max(db_now_us, last_seen_us)
candidate_tat = max(previous_tat, effective_now_us) + requested_units * interval_us
allow iff 1 <= requested_units <= burst_units
         and candidate_tat <= effective_now_us + capacity_us
on allow: tat = candidate_tat
on deny:  tat unchanged；retry_after = ceil((candidate_tat-(effective_now_us+capacity_us))/1s)
```

The SQL uses one `clock_timestamp()` and `effective_now=max(db_now,last_seen)` to avoid clock rollback
granting extra units. A multi-unit request is equivalent to U sequential unit reservations at one
DB instant；golden vectors prove burst N admits exactly N units. All multiply/add/ceil operations
reject overflow；requested units greater than burst are policy errors, not an infinite wait。
Allow and deny both advance `last_seen_us` to effective time so sustained denied traffic cannot let
an idle-TTL cleanup reset the bucket and regain burst；deny never advances TAT. Cleanup is bounded,
environment-scoped and never removes active leases。

PostgreSQL is recommended because it already coordinates River/DBR and traffic is low. Redis/Valkey
is reconsidered only after measured DB/WAL/lock thresholds；not introduced speculatively。

Budget authority uses a dedicated small pgx pool (`application_name=platform-worker-connector-budget`,
min_conns=0) and an approved acquire+query deadline. Fleet connection budget includes every worker
replica, normal job pools, API, migration and reserved admin capacity；`max_conns × replicas` cannot
be chosen locally. Pool saturation/timeout is fail closed and must not block the normal pool by
holding its connection. Exact max/deadline come from R215-2 load evidence, not hardcoded here。

## 8. PollState and adaptive schedule

```text
connector.poll_state
  environment, service_instance_id, provider_scope, capability,
  policy_version, policy_hash, credential_generation,
  next_due_at, current_interval_seconds,
  consecutive_success, consecutive_unchanged, consecutive_failure,
  retry_after_until, circuit_state,
  last_attempt_at, last_success_at, last_outcome_code,
  last_observed_watermark_hash, last_coverage_complete,
  updated_at
```

River runs one cluster-owned **scheduler tick** at policy tick interval. Tick transaction locks state,
uses `clock_timestamp()` and either returns `not_due` without secret/upstream or enqueues/executes one
run with exact policy revision. Dynamic per-instance goroutines/tickers are forbidden。

`scheduler_tick_seconds` must equal the corresponding accepted R2-10 EffectiveJobManifest interval.
Changing it is both an R2-15 policy change and an R2-10 manifest/quiesce change；neither side may
silently override the other。

Transition rules：

- success+changed：move interval toward base after configured recovery steps；
- success+unchanged：after N steps, increase toward max；
- unavailable/timeout：exponential toward max；
- rate_limited：within the accepted bound use `max(calculated_backoff, sanitized Retry-After)`；
  a valid Retry-After above policy max opens manual-suspend instead of capping downward and retrying early；
- auth：open circuit/long suspend until an explicitly approved non-secret credential generation
  increases；same ref/name with unchanged generation cannot silently clear it. Human reset is absent；
- not_supported/version drift：suspend until capability/version evidence changes；
- bad_response：bounded backoff + alert, never fast retry；
- budget exhausted/concurrency：next due from authority, not counted as upstream failure；
- partial：persist coverage and conservative interval；not treated as complete unchanged；
- manual reset/write is absent until Foundation-B。

Jitter is deterministic from SHA-256 of cluster/instance/capability/slot/policy, bounded by policy
PPM, and never shortens below min or Retry-After. No secret required, no random restart burst。

## 9. Retry-After and safe error evidence

Extend Connector error metadata with sanitized fields only：kind, route ID, HTTP status class,
retry-after duration, attempt, budget code. Never upstream body/header/token/URL/query。

- parse delta-seconds and HTTP-date against DB/response Date evidence；invalid ignored + code；a valid
  value above accepted max suspends automatic retry and records only duration class/code；
- 401/403/423 auth not retried；429 only within total budget；5xx/network per policy；
- every attempt consumes request/cost/bytes/time；
- retry sleeps are context-cancellable and cannot outlive run deadline/lease；
- River retry of whole job sees persisted PollState/Retry-After and does not immediately re-hit source；
- successful partial response is not retried into a full-scan storm。

## 10. Pagination, cursor and body safety

- max response bytes enforced while streaming/decoding；overflow closes body and marks partial/error；
- connector reports decoded rows and pages；policy cap checked before next request；
- repeated page/cursor, cycle, non-advancing or impossible total/pages stops；
- raw cursor is never logged/metric; persisted incremental cursor requires separate encryption/privacy
  contract. v1 current page-based clients persist only safe watermark hash/evidence；
- expensive COUNT routes get higher cost units；page_size does not imply cheap query；
- full log bodies remain forbidden；count-only endpoints use minimal filtered query；
- partial output includes covered/expected pages/rows/capabilities and budget reason。

## 11. Single-flight and R2-10

- in-process `singleflight` key is exact environment/service_instance/capability/policy；
- scheduled cluster singleton comes from accepted R2-10 job ownership；
- DB SourceBudget prevents different job types/replicas from exceeding shared provider scope；
- manual/UI refresh is absent；future request becomes an approved River job/idempotency key；
- worker crash may repeat an upstream read after lease expiry；attempt is visible and still budgeted；
- no claim of exactly-once external read。

## 12. Evidence and observability

`ConnectorRunEvidenceV1`：

```text
environment, instance_id, provider_scope, capability,
policy version/hash, scheduled_slot, run_id,
due/started/finished timestamps,
requests/pages/rows/bytes/cost_units/attempts,
source lease/concurrency facts,
interval/next_due/retry_after/circuit/outcome,
coverage complete/covered/expected/reasons,
source/observed_at/watermark hash,
budget backend latency/error class
```

No credential, body, raw URL/query/cursor, user data or secret ref value。

Low-card metrics：runs/outcomes, request/cost units, bytes, partial, rate-limited, budget denied,
concurrency, next_due lag, backend latency/error, source lease expiry, cursor stall, policy revision。

Alerts：budget backend unavailable；rate-limit sustained；auth suspended；cursor stall；partial sustained；
unexpected high requests/cost/bytes；poll state overdue；duplicate cluster run (R2-10)；source policy drift。

## 13. Read-only UI

Platform connection/operations page may show：

- capability/cost class/policy revision/provider scope；
- base/current/min/max interval, last attempt/success, next due, circuit/backoff reason；
- request/page/row/byte/cost totals vs caps；source concurrent/lease status；
- complete/partial coverage, source/freshness/watermark；
- rate-limit/auth/version/budget errors and evidence link。

No “refresh now”, faster interval, reset circuit, edit budget or disable guard button；no Drawer；no raw
endpoint/cursor/key。Writes wait Foundation-B + preview/approval/readback。

## 14. Verification matrix

### Pure policy/guard

- exact current capability/route inventory；unknown route denied；
- every request/page/row/byte/cost/deadline boundary and overflow；
- retries included in totals；cursor stall/cycle；body overflow；partial evidence；
- cost-heavy COUNT weighted；destructive/write denied；
- deterministic jitter/backoff/Retry-After/circuit transitions；
- zero secret resolution/HTTP call on any preflight/budget failure。

### PostgreSQL authority

- harness-owned digest-pinned PG18, no external admin DSN/Skip；
- concurrent variable-unit reserve across two pools；burst/refill/DB clock；
- max concurrency, crash/expiry/late completion/fence/idempotent complete；
- DB restart/timeout/lock/pool saturation fail closed；
- policy revision/history/unknown scope；PUBLIC/runtime DML denial；
- poll state due/not_due/transitions/restart persistence；
- cleanup bounded and no active lease deletion；
- DBR positive/42501 negative and backup/restore。

### Connector integration

- fake upstream counts exact requests/pages/bytes/rows/cost；
- current Sub2API/NewAPI/metering routes all covered；
- over cap produces honest partial/no next request；
- 429/Retry-After, 5xx, auth, bad response, timeout, slow body, repeated page/cursor；
- finance and sync sharing provider scope obey combined rate/concurrency；
- two worker replicas with R2-10 create one run；manual path absent；
- logs/metrics/evidence contain no header/body/cursor/credential。

### Staging

- upstream owner-approved low-volume window and hard ceiling；
- shadow computes decisions without increasing requests；
- compare current vs policy requests/latency/coverage；
- enforce one capability/instance, observe multiple base intervals；
- rate-limit/backoff/failure injection only against controlled fake/canary endpoint unless owner agrees；
- rollback to old fixed interval with one worker replica and explicit “R2-15 not satisfied” state；
- zero skipped tests, source SLO breach or unexplained request。

## 15. Rollout/rollback

1. approve R215 policy/inventory；
2. implement pure guard with existing behavior shadowed；
3. approve exact migration/DBR/source policy/poll-state diff；
4. prove disposable PG and fake upstream；
5. integrate one Connector in shadow：same requests, decisions only；
6. stage enforced low-volume capability with owner approval；
7. soak/backoff/rollback；
8. expand capability-by-capability；production separate。

Rollback disables enforcement/adaptive state and restores approved prior interval/image, but first
scales worker to one replica or keeps R2-10 fencing. It does not clear policy/history/leases, ignore
Retry-After, increase interval frequency, bypass budget, or claim source protection remains solved。

## 16. 分片

| 片 | 交付 | 门禁 |
|---|---|---|
| R215-0 | 本设计/计划 | docs only |
| R215-1 | static policy + pure BudgetRun/body/page/cursor guard + current route inventory | 人工批准；no migration/network |
| R215-2 | PG source authority/poll state/functions/DBR/true PG tests | exact migration/DBR approval；disposable only |
| R215-3 | current connector/job integration + shadow evidence + read-only UI contract | Connector/metric/API approvals；no staging |
| R215-4 | staging one-capability enforce/soak/rollback | R2-10 + owner/staging approval；production NO-GO |

## 17. 审批请求

1. four-layer policy/run/source/poll model and PostgreSQL authority；
2. provider scope explicit mapping and cost units；
3. per-capability request/page/row/byte/time/cost/concurrency caps；
4. adaptive transitions, deterministic jitter and persisted Retry-After；
5. fail-closed zero-request behavior on policy/DB/budget failure；
6. R2-10 single owner, no manual refresh and no exactly-once external-read claim；
7. evidence/UI fields and no write controls；
8. R215-1/2/3/4 separate approvals, R2-15 only after R215-4 accepted。
