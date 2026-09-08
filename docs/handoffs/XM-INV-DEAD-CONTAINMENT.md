# XM-INV-DEAD-CONTAINMENT

- **status**: 已实现、定向测试全绿、变异 22 条全部按预期变红；**未合入、未推送、未部署**
- **branch**: `ai/claude/XM-INV-DEAD-CONTAINMENT`
- **base**: `996bc53`（RC105 生产提交 `4fa39a4f` + 两份设计文档）
- **commit**: 分支上唯一一个提交，即 `ai/claude/XM-INV-DEAD-CONTAINMENT` 的 tip（`git log -1`）。
  这里不写死哈希：把哈希写进被它自己提交的文件里，哈希就永远是上一次的。
- **迁移**: `backend/migrations/0032_eligibility_freezes_open_revision_index.sql`（只加索引）

## summary

2026-09-07 事故的放大机制是一句话：`source_ingest_events.processing_status='dead'`
计数 >0 就让整条流 `Ready=false`，而 `ListFundingLots` / `ListUserEligibilitySummaries`
的五流 AND 与 `assertSourceFreshTx` 都直接读 `stream.Ready`。一个账号的 3 条死信
因此让同来源 19 张额度、6 个用户 30 小时开不了票。

本片把「死信」分成两类：

- **已兜住**：该事件的 `payload_hash` 等于某张 `status='open'` 的
  `eligibility_freezes.source_revision_hash`。有账号被冻结兜着，损失面就是那一个
  账号，其余客户照常开票。流级 `Ready` 保持 true，原因里报
  `EVENTS_DEAD_CONTAINED`（非致命）。
- **未兜住**：没有任何 open 冻结指向它。行为一字不变——`EVENTS_DEAD` 致命、
  五流 AND 拒绝、`/readyz` 503。

readyz 不因此说谎：Ready 且 Contained>0 时 200 的 body 带
`degraded: ["source_ingest_dead_events_contained"]`，日志每 5 分钟一条 Warn。

配套两道闸：

1. **解冻关卡**。冻结是死信唯一的兜底，所以在对应事件离开 `dead` 之前不得解冻，
   否则一次「顺手清理冻结队列」就把放大面从另一扇门重新打开。返回
   `409 ELIGIBILITY_DEAD_EVENT_UNREPAIRED`。
2. **结转证明挂起（A2）**。兜住让 balances 周期得以发布，而资格投影每 2 秒一轮，
   会抢在运维重投之前写下不可变的「本周期余额未变」假证明；0014 的
   `reject_real_checkpoint_after_carry_forward` 触发器此后永久拒绝真检查点。所以
   本账号本周期存在「被兜住的 dead/failed balance_checkpoint」时，证明改为等待
   （`BALANCE_PROOF_PENDING`），重投或核销后自动继续。

## files_changed

### backend
| 文件 | 改动 |
| --- | --- |
| `internal/postgresstore/source_sync.go` | `eventsDeadContainedReason` 常量；`nonFatalStreamHealthReasons` 增项 + 导出 `NonFatalStreamHealthReasons()`（返回副本）；新增唯一判据渲染函数 `sourceEventContainedByOpenFreezeSQL` / `sourceDeadEventCountColumnsSQL` / `sourceDeadEventFlagColumnsSQL`；四个健康面全部改用渲染；`SourceIngestHealth.DeadContained`、`SourceStreamHealth.ContainedDeadEvents`；`evaluateSourceStreamHealth` 改用 uncontained 差值 + 坏报告钳位；导出薄包装 `EvaluateSourceStreamHealth` |
| `internal/postgresstore/consumption.go` | `tryPublishEconomicScanCyclesTx` 的冻结关联改用同一渲染判据，并由 LEFT JOIN 改 EXISTS；结转证明查询 B 增列 `has_stranded_checkpoint` + 归并循环挂起（A2） |
| `internal/postgresstore/eligibility_operations.go` | `eligibilityFreezeBlockingEventStatuses`（渲染成 SQL 数组）与 `eligibilityFreezeDeadEventGuardQuery`（单一查询文本）；`ResolveEligibilityFreeze` 新增解冻关卡 |
| `internal/domain/types.go` | `ErrEligibilityDeadEventUnrepaired` |
| `internal/httpapi/readiness.go` | `ReadinessOutcome` + `publishableDegraded()`（复用既有闭合词汇正则） |
| `internal/httpapi/server.go` | `/readyz` 成功路径带 `degraded`；`Readiness` 签名改为返回 outcome；新增 409 `ELIGIBILITY_DEAD_EVENT_UNREPAIRED` case（排在泛型 `ErrInvalidState` 之前） |
| `cmd/api/readiness.go` | `readinessDegradedSourceIngestDeadContained`；`evaluate` 返回 outcome；probe 增 `containedDead` |
| `cmd/api/runtime.go` | 两个 allowed-reason 集合改为从 `postgresstore.NonFatalStreamHealthReasons()` 派生；流级/ingest 级闸改用差值 + 自洽检查 + B4 诚实闸；`containedDeadWarner`（Warn，5 分钟节流） |
| `migrations/0032_...sql` | `eligibility_freezes(source_revision_hash) WHERE status='open' AND source_revision_hash IS NOT NULL` |

### 测试（新增）
`internal/postgresstore/dead_containment_health_integration_test.go`、
`dead_event_unfreeze_guard_integration_test.go`、
`dead_containment_cycle_integration_test.go`、
`dead_containment_carry_forward_integration_test.go`、
`internal/application/dead_containment_integration_test.go`

### 测试（扩展）
`internal/postgresstore/source_sync_test.go`、`internal/domain/types_test.go`、
`internal/httpapi/readiness_test.go`、`internal/httpapi/server_test.go`、
`internal/httpapi/eligibility_operations_test.go`、`cmd/api/readiness_test.go`、
`cmd/api/main_test.go`

### 前端 / 部署 / 文档
`web/src/types.ts`、`web/src/lib/http-api.ts`、`web/src/lib/mock-api.ts`、
`web/src/App.tsx`、`deploy/postgres/verify-source-readiness-index.sh`、
`docs/PRODUCTION-RUNBOOK.md`、`docs/ELIGIBILITY-OPERATIONS.md`、
`docs/SOURCE-SYNC-PROTOCOL.md`、`docs/CONFIGURATION.md`

## tests_run

```
cd K:/发票/wt-XM-INV-L1L2/backend && env -u HTTP_PROXY ... \
  INVOICE_TEST_DATABASE_URL="postgres://postgres:test@127.0.0.1:55432/invoice_test_l1l2?sslmode=disable" \
  go test -p 1 -count=1 -run 'ContainedDead|DeadEventContainment|ResolveEligibilityFreeze|UnfreezeGuard|
  TwoOpenFreezes|StrandedCheckpoint|EveryDeadEventCount|NonFatalStreamHealthReasons|Readyz|Readiness|
  ShouldWarnContainedDead|EligibilityResolutionSentinels|HandleDomainErrorMapsEligibility|
  ScanCycleFrozenDeadEvent|SourceReadiness|SourceHealthQuery|SourceRuntimeReadiness|EconomicHeartbeat|
  EconomicWatermarkStale|BalanceDeltaCarryForward|BalanceEvidenceEvaluatesCarry|EligibilityFreezeAdminPage' \
  ./internal/... ./cmd/...
```
exit 0（application / domain / httpapi / migrate / postgresstore / cmd/api 全 ok）。

`gofmt`（LF 副本）对本片改动的 20 个 Go 文件全部干净，唯一例外
`internal/domain/types.go` 在 HEAD 上就已经不干净（CJK 常量对齐，与本片无关，
已核实 `git show HEAD:` 版本同样 dirty，故未顺手改）。

## not_run

- **全量 `go test ./...`**：按派工由主控者跑。
- **前端类型检查/构建**：该工作树没有 `web/node_modules`，无法跑 `tsc`/`vite build`。
  前端改动逐行核对过：`SourceStreamHealth` 新增必填字段 `containedDeadEvents`，
  唯一的其他构造点 `web/src/lib/mock-api.ts` 已同步补 0；HTTP 解码用 `?? 0` 两处，
  服务端旧版本不会解出 `undefined`。**合入前需要跑一次前端构建。**
- **`deploy/postgres/verify-source-readiness-index.sh`**：需要真实生产库，未运行。
- 生产只读普查（见 risks 第 1 条）。

## 我替负责人做的决定（owner_decisions_taken）

三条 `needs_owner=true` 的决定，理由与取舍如下。若不同意，前两条改动都局限在
一处，回滚成本很低。

### 1. A2 选 (a)：结转证明挂起，而不是 (b) 承认永久缺口

选 (a)，并与本片同刀合入（`consumption.go` 查询 B 增列 + 归并循环挂起）。

- (b) 的出口实际上不存在：这一形状下周期是 published、哈希匹配、时钟在容差内，
  `ingestRequeueDeadReplayBindingTx` 判 `ReplayBlocked=false`，于是
  `AcknowledgeUnreplayableIngestEvent` 会拒绝写掉。要让 (b) 可用就得放宽那道护栏，
  而它的注释明写放宽方向是「让运维写掉运行时本会接受的事实」——过度开票方向。
- 真正的杀伤是竞态而不是「不可重投」：投影 worker 每 2 秒一轮，必然赶在人工重投
  之前落下不可变假证明，之后 0014 的触发器永久拒绝真检查点。也就是说 (b) 会把
  「一条本可重投的余额事实被永久丢掉」变成默认结局。
- (a) 的代价此前被高估：`EligibilityProjectionHealth` 的 `OldestPending` FILTER
  明确排除 `BALANCE_PROOF_PENDING` 行，所以不会把 readyz 拖到 503，只进
  ProofPending 计数 + 60 分钟后一条 Warn。真实代价是被冻结账号解冻前必须先让事件
  落地或核销（`ErrEligibilityProjectionPending`）——有名字、有出口，已写进
  `docs/ELIGIBILITY-OPERATIONS.md`。
- 这是账本核心（不可变证明 + 触发器），改动 8 行 + 一个 EXISTS 子查询，测试见
  `TestStrandedCheckpointHoldsTheCarryForwardProofInsteadOfFakingIt`（含控制臂、
  钥匙臂、跨账号负向臂）。

### 2. 解冻关卡只挡 `dead`，不挡「未 processed」

派工说明写的是「对应事件 processed 之前不得解冻」，两份审稿的签字条件写的是
「凡仍对应 `processing_status='dead'` 的事件不得解冻」，设计文档 §L3-A1 也把更宽
的版本划给 L3。本片按审稿条件实现。

理由：关卡存在的唯一目的是「不让一次解冻把已兜住的死信变回未兜住」，只有 `dead`
能做到这件事。挡到 queued/failed/processing 会造出无出口的形状——被重投的事件若
转 `waiting_dependency` 或 `parked_identity` 就再也不会 processed，冻结永久解不掉；
而本仓库明令禁止「不给恢复路径的 fail-closed 闩」。`dead` 天然有两把钥匙。

状态集合抽成 `eligibilityFreezeBlockingEventStatuses`（今天只有一项），注释点名
L3 会来改这里。变异 M15（提前扩到 `waiting_dependency`）会让
`a waiting event does not block; only a dead one does` 变红——这条用例就是这个
偏离的锚点。

### 3. 解冻关卡按 `source_instance_id` 收窄

审稿给的 SQL 原文没有 source 条件，但 A3 要求 EXPLAIN 走
`source_ingest_events_readiness_active_idx`；该索引以 `source_instance_id` 开头，
不带它就没有 index cond 边界。`ResolveEligibilityFreeze` 在冻结查找时已经拿到
`sourceID`，直接用。

语义代价：健康面的兜住判据不带 source，关卡带，两者不再严格互逆。安全性来自
冻结账号经 `source_account_eligibility_state` 唯一属于一个来源，且写入端的
`source_revision_hash` 与 `payload_hash` 来自同一次 claim。

**这一条查出了一个测试缺陷，值得单独记：** 我最初把关卡的 SQL 在 EXPLAIN 测试里
**重抄了一遍**，于是变异 M22（删掉 source 收窄）第一次跑是绿的——测的是副本。
现在查询抽成 `eligibilityFreezeDeadEventGuardQuery`，测试 EXPLAIN 的是同一个字符串。
同时发现「计划里出现该索引名」这个断言太弱：夹具里部分索引本来就很小，规划器无论
如何都会走它。断言改为「最后一条 `Index Cond:` 必须含 `source_instance_id`」，M22
这才变红。

## 变异表（22 条，全部逐条：改 → 跑 → 记录 → 还原 → 复跑确认绿）

驱动脚本会在每条之后把文件按原字节还原，并在全部跑完后复跑基线确认 exit 0。

| # | 变异 | 预期 | 实际变红的测试 |
| --- | --- | --- | --- |
| M1 | `evaluateSourceStreamHealth` 致命分支写回 `item.DeadEvents > 0` | 红 | `ContainedDeadIsNonFatal`(a)(e)、`DeadEventContainmentIsConsultedByEveryHealthSurface`、`ContainedDeadEventLeavesOtherAccountsInvoiceable`、`ReadyzAcceptsContainedDead` 四个子测试 |
| M2 | `nonFatalStreamHealthReasons` 去掉 `EVENTS_DEAD_CONTAINED` | 红 | `ContainedDeadIsNonFatal`(a)、`NonFatalStreamHealthReasonsIsTheOneList`、`ReadyzAcceptsContainedDead` 四子测试 |
| M3 | 不追加 `EVENTS_DEAD_CONTAINED` 原因（Ready 仍放宽） | 红 | `ContainedDeadIsNonFatal`(a)(c)(e)(f)、四面测试、`ReadyzAcceptsContainedDead`(α)(η) |
| M4 | 去掉坏报告钳位（裸减法） | 红 | `ContainedDeadIsNonFatal` 的 `a self-contradicting report...` |
| M5 | `SourceHealth` 面的 contained 计数去掉兜住谓词 | 红 | 四面测试（**mixed 阶段**）、`EveryDeadEventCountIsRenderedFromOneDefinition` |
| M6 | `assertSourceFreshTx` 面回退成纯 dead 计数 | 红 | 四面测试、解冻关卡 6 个子测试、发现型测试 |
| M7 | 共享判据去掉 `ef.status='open'` | 红 | 四面测试（**resolved 阶段**）、两账号隔离测试 |
| M8 | `validateSourceIngestRuntimeReadiness` 保留 `health.Dead > 0` | 红 | `ReadyzAcceptsContainedDead` 的 ε 与 η |
| M9 | 同上判反成 `health.DeadContained > 0` | 红 | ε、η **以及** `ReadinessProbeNamesTheCheckThatFailed/source ingest dead events` |
| M10 | 同上过宽（有兜住就算全兜住） | 红 | **只有** `ReadinessProbeNamesTheCheckThatFailed/partly contained ingest dead events still fail closed` |
| M11 | 去掉 B4 诚实闸子句 | 红 | **只有** `ReadyzAcceptsContainedDead/ready with contained dead but no reason naming it is rejected` |
| M12 | `readySourceStreamAllowedReasons` 改回手写字面量 | 红 | α、ζ、η |
| M13 | 删掉解冻关卡 | 红 | 解冻关卡 4 个子测试 |
| M14 | 关卡加 `AND ef.freeze_reason='EVENT_DEAD'` | 红 | 主臂、`EVENT_PAYLOAD_DRIFT` 变体、核销放行臂 |
| M15 | 阻塞状态集合提前扩到 `waiting_dependency`（L3 的改动） | 红 | `a waiting event does not block; only a dead one does` |
| M16 | `tryPublish` 改回 LEFT JOIN | 红 | `TwoOpenFreezesOnOnePayload...` 两臂（checkpoint + manifest） |
| M17 | 去掉 A2 的 stranded 挂起 | 红 | `StrandedCheckpointHolds.../a stranded checkpoint holds the proof...` |
| M18 | 在非测试源码里手写一处 dead FILTER | 红 | `EveryDeadEventCountIsRenderedFromOneDefinition` |
| M19 | `evaluate()` 成功后不填 `Degraded` | 红 | `ReadyzAcceptsContainedDead/the probe reports ready with the contained condition named` |
| M20 | httpapi 发布 degraded 时不过闭合词汇正则 | 红 | `ReadyzPublishesDegradedNames.../a name that fails the pattern is dropped` |
| M21 | readiness 子查询脱离活动状态边界 | 红 | `ContainedDeadFilterKeepsReadinessOnTheActivePartialIndex`、`SourceReadinessQueryIsBoundedToActivePartialIndex` |
| M22 | 关卡去掉 `source_instance_id` 收窄 | 红 | `UnfreezeGuardStaysOnTheActiveIngestIndex` |

**M9 / M10 是审稿 B5 归因修正的直接验证**：判反只被 `{Dead:1,DeadContained:1}→nil`
那条抓到，过宽只被 `{Dead:2,DeadContained:1}` 那条抓到。两条测试各守一半，
提案原文说的「T4 第一条能抓判反」不成立，缺一不可。

**M5 / M7 是 B5 另一半的验证**：删掉某一面的谓词只有混合形状（Dead=2/Contained=1）
的第一阶段红；去掉 `ef.status='open'` 只有 resolved 阶段红。两段分工不同，
都保留。

## risks / follow_ups

1. **【上线前必答，阻塞】生产那 3 条死信是否都 contained=true？** 任一为 false，
   本片对它完全不生效（仍 `EVENTS_DEAD` 致命、仍 503、仍全实例不可用），且 09-07
   的推断链有误。只读查询：
   ```sql
   SELECT sie.stream_id, sie.event_id,
          EXISTS(SELECT 1 FROM eligibility_freezes ef
                 WHERE ef.status='open' AND ef.source_revision_hash=sie.payload_hash) AS contained
   FROM source_ingest_events sie WHERE sie.processing_status='dead';
   ```
2. **「未兜住」是全有或全无**。一次事故产生的孤儿里只要有一条拿不到归因，整条流回到
   `EVENTS_DEAD` 致命。`wrapWithAccountHint` 只有三处（usage / credits /
   balance_checkpoint），payments、identities、cutover_manifest、解密/JSON 失败都
   没有。L3-A3 才补充值/订阅两臂。所以「下一次复发能否受益」取决于孤儿的实体类型
   分布，不是确定的。
3. **本片不碰 `EVENTS_PENDING`**。判死前那约 40 分钟的 pending 窗口对流级 Ready 仍是
   致命的，`SourceHealth`/`assertSourceFreshTx` 也没有 busy 宽限。09-07 事故
   07:08→08:32 那段所有客户被拒的现象，本片一点都不解决（属 L4）。RUNBOOK 已写明
   重投一条兜住的死信仍会带来一次约 40 分钟全实例停摆。
4. **前端未构建**（无 `node_modules`）。另外 `App.tsx` 的 reasons 列表原先只在
   `!item.ready` 时渲染——Ready=true + 兜住时标签一次都不会出现在页面上，任何
   「标签正确显示」的断言都会恒真。渲染条件已改为
   `!item.ready || item.containedDeadEvents > 0`，但没有前端测试守它；建议合入后
   人工看一眼后台来源健康页。
5. **解冻关卡在 SERIALIZABLE 事务里读 `source_ingest_events`**，会加 SIREAD 谓词锁。
   走索引时是行/页级（已由 EXPLAIN 断言守住）；并发写者只有两个修复工具，概率极低
   但不是零。40001 在 Resolve 的 HTTP 路径会落到 `handleDomainError` 的 default
   分支变成未分类 500。提案里「新增 40001 面：无」这句过于绝对。
6. **`verify-source-readiness-index.sh` 的内嵌查询副本**已按 A7 重新同步（此前落后
   三处改动：`classified` 子查询与 `busy_within_grace`、扫描周期联接、cutover
   manifest 子查询——也就是说它此前每次绿都什么也没证明）。但它仍然是一份手抄副本；
   函数注释里写明了两条规矩，以及真正的门禁在
   `source_readiness_integration_test.go`。**follow-up：让脚本从 Go 源码抽取查询
   文本，或加一条比对测试。**
7. **迁移编号**：本工作树 0032 未被占用，但仓库有多条 `ai/claude` 分支在飞，
   合入前需再核一次（记忆条目：迁移版本号是指针不是集合）。本地测试库若跑过其他
   分支迁移，处置是重建 `invoice_test_l1l2` 而不是 goto。
8. **`SourceIngestHealth()`**（`source_sync.go` 的 store 方法）不在生产 readyz 路径
   上，只被测试与一个测试专用包装调用。它仍然改了（发现型测试强制同源），风险为零。
9. **`internal/domain/types.go` 在 HEAD 上就 gofmt 不干净**（CJK 常量对齐）。本片
   没有顺手修——那是无关改动。若要修，应单独一刀。
10. **L2/L3 的接口点**：`eligibilityFreezeBlockingEventStatuses` 是 L3-A1 唯一需要
    改的地方；`sourceEventContainedByOpenFreezeSQL` 是「兜住」的唯一定义，任何要
    改判据的片子改这一处即可，四个健康面与周期发布会一起跟上。

## 提交

提交消息与 trailer 见 `git log -1`。本片未推送 GitHub、未部署、未连接生产库。
