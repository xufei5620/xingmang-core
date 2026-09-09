# XM-INV-DEAD-CONTAINMENT

- **status**: 已实现 + 已处理一轮对抗审稿（1 fatal / 5 major，全部修掉）；定向测试
  全绿、变异 22+13=35 条全部按预期变红；**未合入、未推送、未部署**
- **branch**: `ai/claude/XM-INV-DEAD-CONTAINMENT`
- **base**: `996bc53`（RC105 生产提交 `4fa39a4f` + 两份设计文档）
- **commit**: 分支上两个提交：第一刀是实现，第二刀是审稿修复（`61f5ca5` 之上追加，
  未 amend、未 rebase）。tip 见 `git log -1`——这里不写死哈希：把哈希写进被它自己
  提交的文件里，哈希就永远是上一次的。
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

### 审稿修复轮追加（第二刀）
| 文件 | 改动 |
| --- | --- |
| `internal/postgresstore/source_sync.go` | `sourceEventContainedByOpenFreezeSQL` 收变长 `extraFreezePredicates`（每条必须约束 `ef.` 别名，否则 panic）；注释列出全部渲染消费者与唯一的形状外读者 |
| `internal/postgresstore/eligibility_operations.go` | 关卡查询改为事件驱动 + 渲染判据；抽出 `assertNoBlockingDeadEventForFreezeTx`，长论证搬到 helper 上（五扇门共用一份解释） |
| `internal/postgresstore/balance_anchor_repair.go` | `applyBalanceAnchorFreezeResolution` 调用关卡 |
| `internal/postgresstore/balance_blip_repair.go` | `applyBalanceBlipFreezeResolution` 增 `sourceInstanceID` 形参并调用关卡；调用点传 `c.sourceInstanceID` |
| `internal/postgresstore/queue_narrow_repair.go` | `applyQueueNarrowFreezeResolution` 增 `sourceInstanceID` 形参并调用关卡 |
| `internal/postgresstore/eligibility_repair.go` | 重投循环挪到两个解冻循环之前；`applyPreAnchorFreezeResolution` 调用关卡 |
| `internal/postgresstore/consumption.go` | `has_stranded_checkpoint` 改为渲染判据 + 增「无人认领也算」分支 |
| `internal/postgresstore/ingest_requeue_dead_repair.go` | `--account` 过滤改用渲染判据（顺带补回 `IS NOT NULL`） |
| `internal/postgresstore/containment_discovery_test.go`（新） | 三条发现型规则：dead FILTER 单一来源、兜住关联单一来源、每扇解冻门必过关卡。各带匹配器自测 + 正控制 + 扫描量下限，扫描范围是整个 `backend/` |
| `internal/postgresstore/verify_script_query_sync_test.go`（新） | shell 副本与 `sourceReadinessHealthQuery` 逐字比对 |
| `internal/postgresstore/dead_event_unfreeze_guard_repairs_integration_test.go`（新） | 四扇修复门各一条「拒绝 → 写掉事件 → 成功」 |
| `internal/postgresstore/dead_containment_carry_forward_integration_test.go` | 增两臂：裸 SQL 解冻后仍挂起、修复工具拒绝而不是释放等待 |
| `internal/postgresstore/source_sync_test.go` | 旧的逐行发现型测试删除（被上面那条替代），留一段说明它是怎么被打穿的 |
| `deploy/postgres/verify-source-readiness-index.sh` | 注释从「两条规矩」改为指向那条比对测试，并写明不要重排 heredoc 标记 |
| `docs/ELIGIBILITY-OPERATIONS.md` | 「One door does not pass through this guard」→ 五扇门的表 + 「让表诚实的是那条测试」；结转等待增加第三种情形（无人认领） |

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

审稿修复轮追加跑过（同样的 `-p 1`、同一个测试库）：

```
go test -p 1 -count=1 -run 'ContainedDead|DeadEventContainment|ResolveEligibilityFreeze|
  EveryDeadEventCount|ContainmentCorrelation|EveryFreezeResolution|EveryRepairTool|
  StrandedCheckpoint|VerifyScriptPlans|UnfreezeGuard|RepairPreAnchor|RepairBalanceAnchor|
  RepairBalanceBlip|QueueNarrow|IngestRequeueDead|SourceReadiness|ScanCycle|CarryForward|
  BalanceBlip|BalanceEvidence' ./internal/postgresstore/          # ok 143.9s
go test -p 1 -count=1 -run 'ContainedDead|Readyz|Readiness|DeadEvent|Eligibility' \
  ./internal/application/ ./internal/httpapi/ ./internal/domain/ ./cmd/api/   # 四个包全 ok
```

四个修复工具原有的 15 条测试（`RepairPreAnchor*`/`RepairBalanceAnchor*`/
`RepairBalanceBlip*`/`QueueNarrow*`）在加关卡与调顺序之后全部仍绿——这是本轮
「没有把修复工具改坏」的主要证据。

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

## 审稿修复轮（第二刀提交，在 `61f5ca5` 之上追加）

一轮对抗审稿给了 1 条 fatal、5 条 major。六条全部成立，全部修掉；没有一条是靠
「不成立」打发的。

### F1（fatal）解冻关卡只装在五扇门里的一扇

审稿实测复现了后果：`seedCarryForwardFixture` + 一条 dead `balance_checkpoint` +
一张 SOURCE_GAP 冻结，跑 `anchor-balance` 修复工具，冻结被解掉、事件仍 dead、
`ensureBalanceCarryForwardProofTx` 从 pending 变成写入 proofs=1——一条**本来完全
可重投**的余额事实就此被 0014 的触发器永久拒绝。而 L1 让这件事更容易发生，不是更难：
以前流级 fail-closed 会逼运维先修事件，现在部署是绿的，一次例行修复就够了。

修法：把关卡抽成 `assertNoBlockingDeadEventForFreezeTx`，**五扇门全部调用**——
admin 解冻端点、`preanchor-usage`、`anchor-balance`、`balance-blip`、`queue-narrow`。
`preanchor-usage` 原本靠「同事务里先解冻后重投」自证安全，现在把重投挪到两个解冻循环
之前，让它也真的过关卡。**豁免清单为零**，这一点由发现型测试
`TestEveryFreezeResolutionPassesTheDeadEventGuard` 保证：它读 `backend/` 下每一个
非测试 Go 源文件，任何声明只要写了 `UPDATE eligibility_freezes ... SET status='resolved'`
而没有调用那个 helper 就红。文档 `ELIGIBILITY-OPERATIONS.md` 的「One door does not
pass through this guard」整段改写成五扇门的表，并写明「让这张表诚实的不是表本身，
是那条测试」。

顺带做了 F1 的第二道防线（见下），因为「关卡挡住了所有代码路径」和「结转证明不会被
写坏」是两个命题，不该只有一个证明。

### F2 / F6（major）「兜住」判据被手抄了三份

`ef.status='open' AND ef.source_revision_hash IS NOT NULL AND ef.source_revision_hash=<行>.payload_hash`
在提交里出现三次：渲染函数一份，解冻关卡一份，A2 的 `has_stranded_checkpoint` 一份。
审稿把 A2 那份的 `status='open'` 改成 `IN ('open','resolved')` 跑了 64 秒的定向测试，
**全绿**——提交消息里「判据只有一条、只写一处」当时是假的。

修法：`sourceEventContainedByOpenFreezeSQL(alias, extraFreezePredicates...)` 收窄条件
作为参数（`ef.id=$1` / `ef.external_account_id=$1`），四份手抄全部改为渲染——解冻关卡、
`has_stranded_checkpoint`，外加审稿没点名但形状相同的
`ingestRequeueDeadCandidates` 的 `--account` 过滤（它此前还漏了 `IS NOT NULL`）。
解冻关卡因此从「冻结驱动 JOIN 事件」改写成「事件驱动 + EXISTS」，索引路径不变
（EXPLAIN 断言仍然要求 `source_instance_id` 是 index cond）。

`ingestRequeueDeadOpenFreezesTx` 是唯一没有改的读者，并且写明了理由：它把哈希作为
绑定参数传入、返回行而不是布尔，是「值对列」不是「列对列」，**在形状上**就落在规则
之外，不是豁免。

### F3（major）发现型测试自己是恒真的

上一刀那条 `TestEveryDeadEventCountIsRenderedFromOneDefinition` 是逐行
`strings.Contains("FILTER (WHERE")` + `strings.Contains("processing_status='dead'")`。
审稿加了第五个不看兜住的健康面，三种写法都没红：FILTER 跨两行、`=` 两侧加空格、
`IN ('dead')`。也就是说这条守则的覆盖面是「和现有四处写法碰巧一样」的语法巧合。

修法：改成按**声明**扫描（`go/ast` 解析，取每个顶层声明的源码切片），先把空白规范化，
再用正则匹配运算符与引号变体；并且给每条规则配三样东西——① 自测：把审稿那三种写法
（以及另外三种）钉在测试里，匹配器认不出就 fatal；② 正控制：匹配器必须认得**渲染函数
实际产出的字符串**（渲染函数自己是拼接出来的，源码里并不包含那个字面量，所以这是唯一
有意义的控制）；③ 扫描量下限。扫描范围也从「本包目录」扩到整个 `backend/`。

### F4（major）verify 脚本的手抄副本只是被重新同步了

A7 只要求「标注它已过期」，上一刀把它手工同步了——审稿说得对：一份刚同步过的副本比
一份明显过期的副本更危险，因为它被信任，而产生前三代漂移的机制一点没动。

修法：`TestVerifyScriptPlansTheQueryTheServiceActuallyRuns` 读脚本里的 heredoc，
规范化后与 `sourceReadinessHealthQuery` 逐字比较。规范化只抹两处**写明的**差异
（`public.` 前缀、Go 侧的 SQL 注释）加排版；空格只在「两侧至少一侧不是标识符字符」时
才删，所以它不可能把两个标识符粘成一个、也就不可能让两条不同的查询比成相等。失败时
打印第一处分歧的上下文。

### F5（major，与 F1 同源）另一位审稿人的同一条 + 文档的一句断言

同 F1。文档那句「anything else that resolves freezes programmatically has to
establish the same ordering for itself」是断言，不是约束——现在是约束了。

### 顺带补上的第二道防线（A2 的语义修正）

结转证明的等待此前完全依赖「本账号有一张 open 冻结」来做账号归属。冻结一旦在事件仍
dead 时被解掉（哪怕是绕过所有代码路径的裸 SQL），等待就消失，两秒后假证明落库。

`has_stranded_checkpoint` 因此加了第二个分支：**没有任何 open 冻结认领**的搁浅检查点
也算本账号的。理由写在 SQL 注释里——此时没有任何东西能说明它是谁的，而「假定不是我的」
正是丢事实的那个假定；而且未兜住的死信本来就让整个来源致命，这一等不额外花费任何东西。
别的账号的冻结仍然不拖住本账号（那是两个分支同时为假），有独立用例钉住。

## 变异表（35 条：第一刀 22 条 + 审稿修复轮 13 条）

每一条都是：改 → 只跑相关定向测试 → 记录红/绿 → 按原字节还原 → 复跑确认绿。

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

### 审稿修复轮的 13 条（M23–M35）

| # | 变异 | 预期 | 实际变红的测试 |
| --- | --- | --- | --- |
| M23 | `anchor-balance` 去掉关卡调用 | 红 | `EveryFreezeResolutionPassesTheDeadEventGuard`、`EveryRepairToolRefuses.../balance anchor repair` |
| M24 | `balance-blip` 去掉关卡调用 | 红 | 同上两条（blip 子测试） |
| M25 | `queue-narrow` 去掉关卡调用 | 红 | 发现型、`.../queue narrow repair`、**以及**结转证明的 `a repair run refuses instead of releasing the wait` |
| M26 | `preanchor-usage` 去掉关卡调用 | 红 | 发现型、`.../pre-anchor usage repair` |
| M27 | `preanchor-usage` 把重投挪回两个解冻循环**之后**（保留关卡） | 红 | `RepairPreAnchorUsageEligibilityApplyResolvesRequeuesAndReactivates`——顺序是承重的，既有 happy-path 测试直接抓到 |
| M28 | `has_stranded_checkpoint` 去掉「无人认领也算」分支 | 红 | **只有** `a freeze resolved out from under a still-dead checkpoint still holds the proof`；「别的账号的冻结」臂仍绿 |
| M29 | `has_stranded_checkpoint` 去掉 `ef.external_account_id=$1` 收窄 | 红 | **只有** `another account's freeze does not hold this account's proof`；M28 那臂仍绿（两臂互为对照） |
| M30 | 解冻关卡改回手抄兜住谓词（行为完全等价） | 红 | `ContainmentCorrelationIsWrittenInOnePlace` |
| M31 | 新增第五个健康面，三种写法（FILTER 跨行 / `= 'dead'` / `IN ('dead')`）各一 | 红 | `EveryDeadEventCountIsRenderedFromOneDefinition`——这三种正是审稿用来打穿旧检查器的写法 |
| M32 | 改 `sourceReadinessHealthQuery` 的 ORDER BY，不动 shell 副本 | 红 | `VerifyScriptPlansTheQueryTheServiceActuallyRuns`，并打印第一处分歧 |
| M33 | 新增第六扇解冻门（带表别名、跨行、无关卡） | 红 | `EveryFreezeResolutionPassesTheDeadEventGuard` |
| M34 | 关卡加 `AND ef.freeze_reason='EVENT_DEAD'`（重构后重跑 M14） | 红 | 解冻关卡 3 个子测试 + 四个修复工具子测试全红 |
| M35 | 关卡去掉 `source_instance_id` 收窄（重构后重跑 M22） | 红 | `UnfreezeGuardStaysOnTheActiveIngestIndex` 的 index-cond 断言 |

**M28 / M29 是一对**：它们证明结转等待的两个分支各自承担不同的判断，删掉任何一个都
只让另一个的用例失守。**M25 顺带证明了防线是两道**：关卡被拆掉之后，
`proofs==0` 那条断言仍然成立（第二分支接住了），失败的是「修复工具应该拒绝」那条。

**M27 值得单独看一眼**：它说明「先重投后解冻」不是为了绕开关卡，而正是关卡要求的顺序；
把顺序改回去，既有的 happy-path 测试立刻红——不需要为它新写断言。

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
6. ~~**`verify-source-readiness-index.sh` 的内嵌查询副本**~~ **已闭环**（审稿 F4）。
   它此前落后三处改动，每次绿都什么也没证明；第一刀手工同步了它，第二刀加了
   `TestVerifyScriptPlansTheQueryTheServiceActuallyRuns` 把两份逐字钉住。残余脆弱点
   只剩一处并已写进脚本注释：比对测试靠 `emit_readiness_query() {` /
   `  cat <<'SQL'` / 收尾的 `SQL` 三个标记定位副本，重排这三行会让它找不到——找不到
   时它 fatal，不会假绿。
7. **迁移编号**：本工作树 0032 未被占用，但仓库有多条 `ai/claude` 分支在飞，
   合入前需再核一次（记忆条目：迁移版本号是指针不是集合）。本地测试库若跑过其他
   分支迁移，处置是重建 `invoice_test_l1l2` 而不是 goto。
8. **`SourceIngestHealth()`**（`source_sync.go` 的 store 方法）不在生产 readyz 路径
   上，只被测试与一个测试专用包装调用。它仍然改了（发现型测试强制同源），风险为零。
9. **`internal/domain/types.go` 在 HEAD 上就 gofmt 不干净**（CJK 常量对齐）。本片
   没有顺手修——那是无关改动。若要修，应单独一刀。
10. **L2/L3 的接口点**：`eligibilityFreezeBlockingEventStatuses` 是 L3-A1 唯一需要
    改的地方；`sourceEventContainedByOpenFreezeSQL` 是「兜住」的唯一定义，任何要
    改判据的片子改这一处即可，四个健康面、周期发布、结转等待、解冻关卡与重投工具
    的账号过滤会一起跟上。**L3 注意**：把阻塞状态集合扩到「未 processed」时，五扇
    解冻门会一起收紧——`preanchor-usage` 的重投目标只有 usage/credit，扩集合前要先
    确认它重投之后的落点（`waiting_dependency` / `parked_identity` 都不是
    `processed`）不会把那扇门变成永久拒绝。
11. **修复工具新增了一种失败模式**：四个工具现在都可能因为「有事件还 dead」而拒绝。
    出口是明确的（重投或写掉那条事件后重跑），文档写了；但运维第一次撞上时看到的是
    一次 abort，不是一条跳过。`queue-narrow` 是唯一按账号隔离的（那一个账号进
    `Errors`，其余照修），另外三个是整轮 abort、一字未写——与它们既有的
    「predicate 不匹配就整轮 abort」契约一致。
12. **发现型规则的扫描范围是 `backend/` 下的 Go 源码**。SQL 迁移里如果将来出现
    直接 UPDATE 冻结状态的语句，这三条规则看不见它。今天没有这种迁移（`grep` 过），
    但这是规则的真实边界，写在这里而不是假装它覆盖一切。

## 收尾轮（第三刀，`80d5774` + 合并 `f8386f8`）

### 本轮改了什么

复审两轮之后行为修复已被确认，剩下的 2 条 major 都指向同一件事：**撑住行为修复
的那道发现型闸，自己有静默出口**。复审在 `backend/internal/postgresstore/` 下种了
第六扇门（非测试 `.go`、不调关卡），四种写法里三种没红，第四种靠一行注释混过去。

1. **`freezeResolutionPattern` 收紧。** 从
   `UPDATE eligibility_freezes(?: \w+)? SET status='resolved'` 改成
   `(?i)UPDATE eligibility_freezes(?: (?:AS )?\w+)? SET (?:[^;]{0,160}?, ?)?\bstatus ?= ?'resolved'`。
   四段分别对应大小写、`AS 别名`（旧的 `(?: \w+)?` 只吃一个词，吃不下 `AS ef`）、
   SET 列序调换、`=` 两侧空格。`\b` 让 `prior_status='resolved'` 不算解冻——下划线
   是单词字符，那里没有边界，而 `ef.status` 有。四种逃逸写法连同「小写 + 别名 +
   列序」三者叠加的那种全部进了自测数组，另配三条「不该命中」的反向桩。

2. **关卡命中判据不再看源码文本。** 新增 `declCallsFunction`，走 `go/ast` 找
   `*ast.CallExpr`；注释、SQL 字符串、同名标识符（`...Note`）都不再算调用。
   同一处修法也用在 `TestContainmentCorrelationIsWrittenInOnePlace` 的 `callers`
   计数上——`consumption.go` 的查询注释里就写着那个函数名，旧写法会让「还有人调用」
   这条空转检查在最后一个真调用消失之后仍然满足。另外扫描时统一把 Go 注释抹成空格
   （`blankComments`），任何规则都不必自己记得忽略注释；SQL 字符串里的 `--` 不是
   Go 注释，不受影响。新增 `TestDeclCallsFunctionIgnoresCommentsAndStrings`，
   六条反向桩 + 五条正向桩 + 两条边界桩钉住它。

3. **dead 聚合的判据从「拼法」换成「判据本身」。** `deadStatusTestSource` 覆盖
   `::text` 强转、反写 `'dead'=col`、`= ANY(ARRAY[...])`、以及 IN 列表里 `'dead'`
   不在首位；聚合外壳除 `FILTER` 外补上 `sum(CASE WHEN ... THEN 1 ELSE 0 END)`。

### 第 3 条是一次有意的规则改动，不是纯收紧

把匹配面拓宽之后，`consumption.go` 的 `tryPublishEconomicScanCyclesTx` 被照出来了：
它手写了一个带 `processing_status IN ('failed','dead')` 的聚合（数「未完成事件」），
**但紧接着就调 `sourceEventContainedByOpenFreezeSQL` 把已兜住的死信排除掉**。
它是对的，只是旧正则根本看不见它——也就是说「这条规则今天没报警」有一部分是靠
看不见，不是靠没问题。

它需要的列形状与两列渲染函数不同，硬塞进 `sourceDeadEventCountColumnsSQL` 只会更糟。
所以规则改成它真正要守的那句话：**dead 聚合必须在同一段表达式里问一次 containment**。
渲染函数是标准做法、四个健康面都走它；周期发布这种需要别的列形状的，内联问一次
也算数。allowance 的绳子只有 240 字符，并且两侧都配了桩：同一个 `FILTER` 里问过就
放行，隔得远（桩里隔了约 460 字符）就照样报——避免退化成「函数体里问过一次就整体
豁免」。测试同步改名
`TestEveryDeadEventCountIsRenderedFromOneDefinition` →
`TestEveryDeadEventAggregateConsultsContainment`，`source_sync.go` 里引用它的注释
一并改。

### 变异表（13 条：M36–M48）

做法与前两刀一致：在 `backend/internal/postgresstore/` 下种一个非测试 `.go`
文件当第六扇门 / 手写健康面，跑三条树级规则，跑完删掉。这一轮**每条都在修复前后
各跑一次**（用 `git show HEAD:` 取出修复前的测试文件换进去跑，再换回来），所以
「修后红」不是孤证，「修前绿」是同一条变异在同一棵树上的实测。

| # | 变异 | 修前 | 修后 | 修后变红的测试 |
| --- | --- | --- | --- | --- |
| M36 | `SET status = 'resolved'`（`=` 两侧空格） | 绿 | 红 | `EveryFreezeResolutionPassesTheDeadEventGuard` |
| M37 | `AS zz SET status='resolved'`（显式别名） | 绿 | 红 | 同上 |
| M38 | `SET resolved_at=now(), status='resolved'`（列序调换） | 绿 | 红 | 同上 |
| M39 | `update ... set status='resolved'`（全小写） | 绿 | 红 | 同上 |
| M40 | 一字不差的 UPDATE + 关卡名**只**出现在注释里 | 绿 | 红 | 同上（这条是 `declCallsFunction` 的树级证据） |
| M41 | 对照：同上但真的调关卡 | 绿 | 绿 | —— |
| M42 | `processing_status::text='dead'` | 绿 | 红 | `EveryDeadEventAggregateConsultsContainment` |
| M43 | `'dead'=sie.processing_status`（反写） | 绿 | 红 | 同上 |
| M44 | `= ANY(ARRAY['dead'])` | 绿 | 红 | 同上 |
| M45 | `sum(CASE WHEN ...='dead' THEN 1 ELSE 0 END)` | 绿 | 红 | 同上 |
| M46 | `IN ('failed','dead')`（`'dead'` 不在首位） | 绿 | 红 | 同上 |
| M47 | 对照：dead 聚合 + 同段内联 containment | **红** | 绿 | —— |
| M48 | 对照：不种任何东西 | 绿 | 绿 | —— |

**M47 修前红、修后绿是第 3 条规则改动本身，不是回归。** 旧规则说「除渲染函数外
不许出现 dead FILTER」，所以一个内联问过 containment 的聚合在旧规则下也是错的；
新规则说「必须问 containment」，它就合法了。这一格是这次规则改动唯一的行为差异，
特意留在表里而不是把对照组换成一个两边都绿的。

**M41 / M48 是两条不该变红的对照**，都确认没红——否则「目标变异红了」可能只是
种进去的文件本身编译不过。

### 合并 RC106 的取舍

派工预判 `web/src/App.tsx`、`web/src/lib/http-api.ts`、`web/src/lib/mock-api.ts`、
`web/src/types.ts`、`docs/PRODUCTION-RUNBOOK.md` 五处冲突。**实际零冲突标记，
git 全部自动合上。** 文本无冲突不等于语义正确，所以逐个核对了两侧都在：

| 文件 | RC106 这一侧 | L1 这一侧 | 取舍 |
| --- | --- | --- | --- |
| `web/src/types.ts` | lot / summary 的资格状态与 reason 换成契约生成的联合类型 | `SourceStreamHealth.containedDeadEvents` | 两处在不同 interface、不相邻，都保留 |
| `web/src/lib/http-api.ts` | 重写 `mapLot` / `mapEligibilitySummary` / `mapEligibilityFreeze`，加形状校验与未知值降级 | 改的是 `mapSourceHealth`（可选字段 `contained_dead_events`、`?? 0`、指纹数组多带一位） | 都保留；三处 L1 标记与 RC106 的 `eligibility-wire.generated` 引用都实测在树上 |
| `web/src/App.tsx` | 资格摘要面板、来源账号面板 | 同步流健康表：`EVENTS_DEAD_CONTAINED` 文案、「已兜住 N」、渲染条件放宽成 `!item.ready \|\| containedDeadEvents > 0` | 都保留 |
| `web/src/lib/mock-api.ts` | 另一个 fixture | `containedDeadEvents: 0` | 都保留 |
| `docs/PRODUCTION-RUNBOOK.md` | rc105→rc106 发布身份替换 | L1 这一侧没动过这个文件 | 单边，直接取 RC106 |

**专门查过的一件事**：RC106 新增了 `web/src/lib/source-labels.ts`，如果它把
`sourceReasonLabels` 从 `App.tsx` 挪了过去，L1 那条 `EVENTS_DEAD_CONTAINED` 文案
就会落进一份死副本——文本照样无冲突，页面上永远不出现。实测 RC106 没有挪，
`sourceReasonLabels` 仍在 `App.tsx:5845`，L1 的文案在 `:5853`，渲染条件在 `:6157`。

**契约（`contracts/invoice-eligibility-wire.v1.json`）本轮不需要改。** L1 唯一新增
的枚举值是 `EVENTS_DEAD_CONTAINED`，它是**同步流就绪原因**，不是 lot 资格状态、
不是冻结原因、也不是摘要 reason。该契约管的是 `lot_eligibility_status` /
`lot_reason_code` / `summary_status` / `summary_reason` 四族，就绪原因不在其中；
`backend/internal/eligibilitywire` 的探针也只扫 `eligibility_status` 与
`reason_code`。判断依据是实跑门禁而不是读代码推断：
`go test ./internal/eligibilitywire/... ./internal/httpapi/...` 全绿。
`web/src/lib/eligibility-wire.generated.ts` 未重新生成，因为契约未变。

### 门禁（全部实测，UTC）

| 门禁 | 命令 | 开始 | 结束 | 耗时 | 结果 |
| --- | --- | --- | --- | --- | --- |
| 前端依赖 | `npm ci`（本工作树原本没有 `node_modules`） | 05:42:00 | 05:42:08 | 8s | 57 包，0 漏洞 |
| 后端静态 | `go vet ./...` | 05:44:07 | 05:44:08 | 1s | exit 0 |
| 后端全量 | `env -u <八个代理变量> INVOICE_TEST_DATABASE_URL=...invoice_test_l1merge go test -p 1 -count=1 ./...` | 05:44:16 | 05:51:33 | 7m17s | exit 0，30 包 ok + 5 包无用例，零 FAIL |
| 前端类型 | `npm run typecheck` | 05:51:51 | 05:51:53 | 2s | exit 0 |
| 前端用例 | `npm test -- --run` | 05:51:53 | 05:51:55 | 2s | 20 文件 / 330 例全绿 |
| 密钥扫描 | `pwsh -NoProfile -File scripts/check-no-secrets.ps1` | 05:52:00 | 05:52:01 | 1s | exit 0 |

后端全量里耗时靠前的包（`go test` 自报，单位秒）：

| 包 | 耗时 |
| --- | --- |
| `internal/postgresstore` | 317.521 |
| `internal/application` | 27.686 |
| `internal/testdb` | 25.105 |
| `cmd/eligibility-repair` | 14.732 |
| `internal/auth` | 10.096 |
| `internal/migrate` | 7.442 |
| `internal/oidcretention` | 4.767 |
| `internal/adminsettings` | 3.906 |
| `cmd/identity-migrate` | 1.926 |
| `internal/backupverify` | 1.121 |

测试库 `invoice_test_l1merge` 是本轮新建的（`docker exec invoice-test-pg psql -U postgres
-c "CREATE DATABASE invoice_test_l1merge"`），没有复用跑过别的分支迁移的库。

### 本轮对上面 risks / follow_ups 的更新

- **第 4 条「前端未构建」已闭环一半。** 本工作树跑了 `npm ci`，typecheck 与 vitest
  都实测全绿。但「没有前端测试守 `EVENTS_DEAD_CONTAINED` 的渲染条件」这半条**仍然
  成立**：本轮没有新增前端用例（不在派工范围内），那条 `!item.ready ||
  item.containedDeadEvents > 0` 至今只有代码注释在说明它为什么必要。合入后仍建议
  人工看一眼后台来源健康页，或单独补一条组件用例——注意它是「元素应当出现」型断言，
  别写成缺席型。
- **第 7 条「迁移编号」已闭环。** 同事在 L2 分支上发现 `0032` 漏进迁移排除表，
  提交 `3d95168` 属于 L1 范围，本轮已 cherry-pick 进来（`b39a123`）。
  `TestConsumptionMigrationClosesPreCutoverReservationsAndPreservesIssuedExposure`
  故意排除 `0009` 及其依赖，而 `0032` 在 `0009` 建的 `eligibility_freezes` 上建索引，
  `CREATE INDEX IF NOT EXISTS` 不救场（它只抑制「索引已存在」，不抑制「表不存在」）。
- **第 12 条「扫描范围是 `backend/` 下的 Go 源码」仍然成立，边界更清楚了一点。**
  本轮补上的是「注释与字符串不算数」，扫描范围没有变：SQL 迁移里若将来出现直接
  UPDATE 冻结状态的语句，这三条规则依然看不见它。
- **新增一条边界（本轮引入，写在这里而不是假装没有）**：dead 聚合的 containment
  allowance 是按**归一化文本里 240 字符的窗口**判定的，不是按「同一个 SQL 括号」。
  归一化后的声明文本是 SQL 与 Go 拼接混在一起的，读不出精确的括号配对。方向是安全
  的一侧——问得太远会**报错**而不是被忽略——但如果将来有人写了一个很长的 `FILTER`、
  containment 调用落在 240 字符之外，他会看到一条需要重排而不是需要修 bug 的红。

## 提交

提交消息与 trailer 见 `git log`。本片未推送 GitHub、未部署、未连接生产库。
收尾轮三个提交：`b39a123`（cherry-pick 0032 排除表）、`80d5774`（发现型闸的
四个静默出口）、`f8386f8`（合并 RC106）。
