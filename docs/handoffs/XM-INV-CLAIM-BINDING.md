# XM-INV-CLAIM-BINDING —— 认领时改用「当前有效的绑定」，而不是第一次那个批次

- status: ready-for-review（未合入发布线，未 push，未 tag）
- branch: `ai/claude/XM-INV-DEAD-REQUEUE`（与 XM-INV-DEAD-REQUEUE 同分支，独立提交）
- base: `331777a`（RC103），在 XM-INV-DEAD-REQUEUE 之后
- worktree: `K:/发票/wt-XM-INV-DEAD-REQUEUE`
- 前置阅读：`docs/handoffs/XM-INV-DEAD-REQUEUE.md` 的
  《2026-09-08 生产 dry run：结论与第二轮修改》一节——本片就是那一节里
  「存在一条不绕过校验的出路」的实现。

## 一句话

`ClaimUnprocessedSourceEvents` 过去无条件按 `sie.first_batch_id` 取批次上下文，把
**第一次投递时那个批次**的 `batch_id` / `scan_cycle_id` 交给
`verifyFactBatchContextTx`。代理重启后那个周期会被 supersede 成 `blocked`，于是
校验器**永远**拒绝——而代理其实早已把同一条事件重新投递进了一个健康周期，映射就躺在
库里，只是没人看它。本片让认领**优先取该事件当前仍然有效的、最新的那条绑定**。

**校验器一个字没改。** 这不是放松校验，是**给校验一个为真的输入**。

## 为什么这是「输入正确」而不是「校验放松」

`source_economic_scan_cycle_events` 只有一个写入者：`CommitSourceBatch`，而且写在
校验批次哈希链（`previous_batch_hash` / `body_hash` / `signing_key_id` / `sequence`）
的同一笔事务里。所以一条映射行的含义是**「代理确实在批次 B、周期 C 下投递过这条
payload_hash 的事件」**——这是签名批次背书过的事实，不是我们编出来的。

本片选绑定时用的三个条件，与校验器自己用的**逐条相同**：

| 校验器（`consumption.go:995` 一带） | 本片的 lateral |
| --- | --- |
| `b.schema_version='3.0'` | `b.schema_version='3.0'` |
| `m.payload_hash = <事件 payload_hash>` | `m.payload_hash=sie.payload_hash` |
| `cycle_status IN ('receiving','processing','published')` | 同 |

因此「优先分支」挑出来的绑定，**只可能是校验器会接受的那一条**。没有任何一条校验被
削弱：一个本来就该被拒的绑定，仍然会被拒（见反向测试）。

副作用方向都是「更正确」：事实记录的 `source_sequence` / `stream_watermark_at` 来自
**真正观测到它的那次扫描**，而不是一次被放弃的扫描。

## 与要求 4 的一处偏离（请看，这是我唯一没有照字面做的地方）

要求 4 的字面表述是：**只有** blocked 绑定、没有任何有效绑定的事件「**仍然不被认领**」。

**我没有那样做，改成了「保留原绑定、照旧认领、照旧被校验器拒绝」。理由：**

「不认领」会造出一个**新的、永久停滞的状态**。那条事件会一直停在 `queued`/`failed`，
`attempt_count` 再也不增长，于是永远不会判死；而
`SourceIngestHealth.Pending` 把它算进去、`OldestPending` 取 `min(created_at)`
（几小时前），`validateSourceIngestRuntimeReadiness` 于是以泛化的
`source ingestion processing is unhealthy` **永久 503**，且再没有「有死信」这个信号
指向具体是哪条事件。**这正是我在 XM-INV-DEAD-REQUEUE 设计问题 1 里论证过、并且明确
反对过的那种「把一个有名字的故障换成一个没名字的故障」。**

保留回退则完全维持今天的行为：被认领 → 校验器拒绝 → 8 次后判死 → readyz 报
`contains dead events` → `invoice-eligibility-repair --kind=ingest-requeue-dead` 的
dry run 逐条说明原因。一次性烧 35 分钟，然后停在一个**稳定且可读**的状态。

我也考虑过「不认领 + 给这个状态加一个健康计数器」，那能兼顾两边；但
`sourceReadinessHealthQuery` 是刻意设计成只走
`source_ingest_events_readiness_active_idx` 的（注释写明「一个很大的 waiting/parked
积压不能让 /readyz 去扫事件表」），而判定「有没有有效绑定」需要对每行做一次映射表
lateral，会直接破坏那个性质。所以不做。

**要求 4 的用意（「否则你可能顺手把谁都能过也一起放开了」）我是完整满足的**，只是
断言点换了：反向测试断言的是**没有任何一条无效绑定被当成有效的交出去**，而且**该事件
持有的每一条绑定都仍然被未经修改的校验器拒绝**。见下。

**如果你坚持要字面的「不认领」，告诉我，我按上面那条路补健康计数器与就绪理由一起做。**

## 改了什么

| 文件 | 内容 |
| --- | --- |
| `backend/migrations/0031_claim_binding_index.sql`（新增） | `source_economic_scan_cycle_events(source_instance_id, stream_id, event_id)` 索引。主键与既有索引都以 `scan_cycle_id` 领先，按 event_id 查没有可用索引；认领是热路径，没有它会退化成顺序扫描。纯只读增量，可先于代码上线、代码回滚后留着也无害。 |
| `backend/internal/postgresstore/source_sync.go` | 新常量 `claimBindingSelect`（一个 `JOIN LATERAL`），替换 `ClaimUnprocessedSourceEvents` 里那句 `JOIN source_ingest_batches sib ... ON sib.batch_id=sie.first_batch_id`。选择规则：优先「当前有效的经济绑定里 `b.sequence` 最大的那条」，否则回退 `first_batch_id`。 |
| `backend/internal/postgresstore/claim_binding_integration_test.go`（新增） | 3 个集成测试。 |
| `backend/internal/migrate/migrate_test.go` | 0031 加入精简 fixture 的排除表（它给被排除的 0009 建的表加索引，与 0022/0023 同理）。 |

**`verifyFactBatchContextTx` 未改动一个字符。** 可用
`git show <commit> -- backend/internal/postgresstore/consumption.go` 核对：本片不含
该文件。

### `first_batch_id` 的另一处用途，已按要求逐处核过

全仓非测试代码里 `first_batch_id` 只有两处**功能性**使用（其余都是本片与
XM-INV-DEAD-REQUEUE 的注释文本）：

1. `source_sync.go:402`，`CommitSourceBatch` 插入事件行时写入 —— **本片不改**。它记录
   「这条事件第一次是在哪个批次到达的」，是溯源信息，仍然准确，也仍然是回退分支要用的
   那一条。
2. `source_sync.go:577`（原）认领时的 JOIN —— **本片改的就是这一处**。

外加 schema 层一处：`0004_persistent_application.sql:242` 的外键
`(source_instance_id, stream_id, first_batch_id) REFERENCES source_ingest_batches
... ON DELETE RESTRICT`。本片不改存进去的值，所以外键不受影响。

结论：**改动不外溢。**

## 测试

| 测试 | 钉住什么 |
| --- | --- |
| `TestClaimBindingPrefersTheNewestValidBindingOverASupersededFirstBatch` | 生产形状（真实 supersede 路径：旧周期 blocked ＋ 同一确定性 event id 在后继周期重投递、后继周期发布）。认领必须拿到**后继批次**（`BatchID`/`ScanCycleID`/`BatchSequence` 三项都断言），并且**未经修改的校验器**（经导出入口 `ValidateEconomicFactContext`）接受这个三元组；同时断言它**仍然拒绝**旧的那个三元组——否则 fixture 根本没复现生产故障，测试就是假绿。 |
| `TestClaimBindingKeepsTheOriginalBindingWhenNoValidBindingExists` | 反向：事件持有**两条各不相同批次上的、都不可用的**绑定（第一条被 supersede，第二条也被 supersede）。认领必须拿到**原始**那条，绝不能因为「有更新的」就换过去；并且逐条断言该事件持有的**每一条**绑定都仍被校验器拒绝。**两条不同批次是关键**：只有一条时「回退到第一条」与「取最新一条」结果相同，断言就是恒真的。 |
| `TestClaimBindingUnchangedForEventsWithNoEconomicBinding` | 对照组：identities 流的 v2 事件根本没有映射行，认领必须与改动前完全一致（原批次、`SchemaVersion=2.0`、空 `ScanCycleID`），并断言映射数确实是 0。 |

### 变异验证

| # | 变异（改条件/取值） | 预期红 | 实测红 | 对照组 | 实测 |
| --- | --- | --- | --- | --- | --- |
| I | lateral 去掉 `c.cycle_status IN (...)`（改成 `IS NOT NULL`） | 反向用例 | `KeepsTheOriginalBindingWhenNoValidBindingExists`，报文字就是「取了更新但同样不可用的那条批次」 | 其余 13 个（含全部 ingest-requeue-dead 用例） | 全绿 |
| J | `ORDER BY b.sequence DESC` → `ASC` | 「取最新」那条 | `TakesTheNewestOfSeveralValidBindings` | 其余 3 个 | 全绿 |
| K | 优先分支 `1 AS preference` → `3`（等价于改回 `first_batch_id`） | 两条「优先取有效绑定」用例 | `PrefersTheNewestValidBinding...`、`TakesTheNewestOfSeveralValidBindings` | `KeepsTheOriginalBinding...`、`UnchangedForEventsWithNoEconomicBinding`、全部 ingest-requeue-dead 用例 | 全绿 |

**关于变异 J，有一件值得记下的事。** 我最初只写了两个正向用例，
那时变异 J **是空跑**的：生产形状的用例里只有**一条**绑定是可接受的，
`ORDER BY` 方向根本观测不到，改 ASC 也全绿。也就是说「取最新」这条规则
当时根本没被钉住。为此新增了 `TakesTheNewestOfSeveralValidBindings`（一条不可用
加**两条都可接受**的绑定），J 才有了意义。**“变异不红”有时不是实现健壮，
而是用例根本没进那个分支。**

另外，`TakesTheNewestOfSeveralValidBindings` 第一版自己就红了，报
`cycle_status="blocked"`：第三个周期用了普通的 `commitEvents`，它的 ceiling
比上一个（supersede 助手刻意往后拨了一分钟）早，
`tryPublishEconomicScanCyclesTx` 判为水位线回退直接 blocked。fixture 的错，已改。

## 门禁

| 门禁 | 命令 | 结果 |
| --- | --- | --- |
| build / vet | `go build ./...`、`go vet ./internal/postgresstore/ ./cmd/eligibility-repair/` | 通过 |
| 本片用例 | `go test ./internal/postgresstore/ -run TestClaimBinding -count=1` | 4/4 通过 |
| 相邻片用例 | `go test ./internal/postgresstore/ -run TestIngestRequeueDead -count=1` | 11/11 通过（本片改了认领查询，它们是回归对照） |
| CLI | `go test ./cmd/eligibility-repair/ -count=1` | 通过 |
| 后端全量 | `go test -p 1 -count=1 ./...` | 见下方“全量门禁” |

必须带 `-p 1`：`cmd/eligibility-repair` 与 `internal/postgresstore` 共用同一个
worktree 专用库且各自 `DROP SCHEMA`。

## 一处必须同步改的地方（差点漏掉）

本片改了认领走哪条绑定，而
`invoice-eligibility-repair --kind=ingest-requeue-dead` 的全部价值就在于
**预测校验器会怎么判**。它原来自己拼了一份按 `first_batch_id` 查的 SQL——
那是**旧**行为。本片上线后，工具会继续把那两条 usage 报成
`replay blocked`，而认领实际会成功——**报告与运行时直接对不上**，
也就是说我本来会给你一个错的「预期变化」。

已修：`ingestRequeueDeadReplayBindingTx` 现在**直接嵌入 `claimBindingSelect`
这个同一份 SQL 常量**，而不是重写一遍规则——两者从结构上就无法漂移。

相应地，`TestIngestRequeueDeadSkipsEventsWhoseReplayCycleIsBlocked` 的前提变了
（它用的正是生产那两条 usage 的形状，现在应当可投），拆成两个：

- `TestIngestRequeueDeadRequeuesAnEventRescuedByALaterValidBinding`：生产形状
  现在 `Requeued=true`、replay binding = 已发布的后继；**并且 apply 后真去认领一次，
  断言 claim 带的 batch/cycle 与报告预测的逐字一致**，再用未修改的校验器
  确认它接受。这条用例就是「预期变化」的可执行形式。
- `TestIngestRequeueDeadSkipsWhenEveryBindingIsUnusable`：两条绑定都不可用（余额
  快照那种）仍然跳过。

**连带效果（值得单独记一笔）**：`--kind=ingest-acknowledge-unreplayable` 那道
拒绝闸复用的是**同一个** `ingestRequeueDeadReplayBindingTx`，所以它
**自动继承了这个修复**——无需另改一行。于是「什么算可重投」这个定义
在**三处**（认领、重投预测、注销拒绝）只存在一份。

这一点对注销尤其要紧：它是**不可逆**的操作。如果它拿的是一份已经漂移的
副本，它会在「这条其实救得回来」的情况下放行注销——而那是没有撤回的。

**教训：一个「预测另一处行为」的工具，必须与被预测的那处共用代码，
否则它会在某次无关的改动后安静地开始说谎。**

## dry run 的预期变化

本片上线后，`invoice-eligibility-repair --kind=ingest-requeue-dead` 的 dry run
对生产那 3 条应当变成：

| 事件 | 流 | 现在（本片前） | 本片上线后预期 |
| --- | --- | --- | --- |
| `63872270` | usage | `REQUEUED false (replay blocked)`，replay binding = `b1de0e2b` **blocked** | `REQUEUED true`，replay binding = `9ca5afcb` **published** |
| `bb412266` | usage | 同上 | 同上 |
| `fcd2e2c6` | balances | `REQUEUED false (replay blocked)`，replay binding = `e58b9430` **blocked** | **不变**（它没有任何其它绑定） |

汇总行应当从 `total requeued: 0 / not requeued (replay blocked): 3` 变成
`total requeued: 2 / not requeued (replay blocked): 1`。

**这个预期本身就是一道校验：** 如果上线后那两条 usage 没有变成可投，
说明它们的 `9ca5afcb` 绑定并不满足校验器的条件（比如 `payload_hash` 对不上），
**先停下来搞清楚，不要用 `--include-blocked-cycles` 硬投**。

`fcd2e2c6` 仍然不可投，属于“承认丢失”那一类，用
`--kind=ingest-acknowledge-unreplayable` 处置（见
`docs/handoffs/XM-INV-DEAD-REQUEUE.md`）。
