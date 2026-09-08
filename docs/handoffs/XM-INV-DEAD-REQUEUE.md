# XM-INV-DEAD-REQUEUE —— 把判死的 `source_ingest_events` 重新投回队列

- status: ready-for-review（未合入发布线，未 push，未 tag）
- branch: `ai/claude/XM-INV-DEAD-REQUEUE`
- base: `331777a`（RC103 发版材料）
- worktree: `K:/发票/wt-XM-INV-DEAD-REQUEUE`
- commits（按顺序）：
  - `61b6ac4` `feat(eligibility-repair): requeue dead source ingest events (XM-INV-DEAD-REQUEUE)`
    —— 实现、CLI 接线、集成测试、`docs/ELIGIBILITY-OPERATIONS.md` 新增一节
  - `fb48fb3` `docs(handoffs): XM-INV-DEAD-REQUEUE`
  - 第三条 —— 报告缺陷修复（判据内建）与恢复可行性调查，见
    《2026-09-08 生产 dry run：结论与第二轮修改》
- 新增 `invoice-eligibility-repair --kind=ingest-requeue-dead`，形状照抄
  `--kind=projection-requeue-dead`（XM-INV-PROJECTION-FAILURE-GRADING）。

> **结论先行（2026-09-08 生产 dry run 之后）**：那 3 条**现在一条都不能投**，
> 工具已经改成自己拒绝而不是让人对着手册判断。**2 条用量事件在证据层面并没有丢**
> （它们已经有一条合法的、指向已发布周期的绑定），卡住它们的是认领路径钉死在
> `first_batch_id`；**1 条余额快照没有任何合法绑定，只能承认丢失**。详见文末那一节。

## summary

生产 `source_ingest_events` 里有 3 条 `processing_status='dead'`：一个已验证、
已绑定的客户在 2026-09-07 07:08:28 完成绑定，系统补历史数据时头几条撞上序列化冲突，
8 次重试耗尽后判死。死的是 **2 条用量记录 + 1 条余额快照**，内容在
`source_usage_events` 与 `balance_reconciliation_checkpoints` 里都不存在——是真没
进账。原始 payload 还加密躺在那 3 行里，所以重投能把数据找回来。

系统已经为这 3 条各开了一张 `EVENT_DEAD` 资格冻结（`status='open'`），在洞补上前
拒绝给这个客户开票；`readyz` 因 `SourceIngestHealth.Dead > 0` 已 503 二十多小时。

**这个切片只做工具，不碰生产。** 拿它对生产跑的是别人，且必须先跑 dry run 给
负责人过目——见文末《给执行者：生产上怎么跑这 3 条》。

**根因不在本切片范围内。** 另一位 agent 正在修「40001 不再扣重试预算」；本工具在
「已修」和「未修」两种情况下都成立，两种情况的差别只体现在重投后多久会再死一次，
见设计问题 1。

---

## 三个设计问题的结论与依据

### 问题 1：`attempt_count` 怎么处理？→ **必须归零**

**先纠正一个前提。** 简报里的二选一是「归零 = 能再撞 8 次 / 不归零 = 一进来就又死」。
不归零的真实后果比「又死」更糟：**它根本不会被取走**。

`ClaimUnprocessedSourceEvents`（`backend/internal/postgresstore/source_sync.go:578`）
的认领条件是：

```sql
(sie.processing_status IN ('queued','failed') AND sie.attempt_count < 8 AND sie.next_attempt_at <= $1)
```

`attempt_count < 8` 是硬过滤。一条 `attempt_count=8` 却被翻成 `'queued'` 的行，worker
永远看不见它，于是：

1. 它永久停在 `queued`，永远不会终态化；
2. `SourceIngestHealth.Pending`（`source_sync.go:893`）和就绪查询
   （`sourceReadinessHealthQuery`，`source_sync.go:1005`）都把它算作 pending，
   `OldestPending` 取的是 `min(created_at)`——那是 20 多小时前；
3. `validateSourceIngestRuntimeReadiness`（`backend/cmd/api/runtime.go:789`）于是
   以 `source ingestion processing is unhealthy` 继续 503，
   **而且 `Dead` 已经归零，再也没有「有死信」这个信号告诉运维为什么**。

这比放着不动严格更差：把一个有名字的故障换成一个没名字的故障。

这不是推理，是变异验证测出来的（下文变异 C）：把 `attempt_count=0` 改成
`attempt_count=attempt_count` 后，
`TestIngestRequeueDeadRequeuedEventIsClaimableAgain` 报
`requeued event was not claimable: []`——认领结果是**空列表**，不是「取走后又死」。

所以 0 是唯一能让「重投」这个词有意义的取值。它也和仓库里**两处既有先例**一致：

- `RepairPreAnchorUsageEligibility`（`eligibility_repair.go:~230`）对同一张表的重投
  就是 `attempt_count=0`；
- 样板 `RepairProjectionRequeueDead` 对 `eligibility_projection_jobs` 是 `attempts=0`。

**根因未修时归零安全吗？安全，而且有界。** 认领会把它加到 1；
`SourceEventProcessor.RunOnce`（`backend/internal/application/source_processor.go:181`）
失败时固定退避 5 分钟；8 次约 35 分钟后 `MarkSourceEventFailed` 重新判死。
关键点是**不会留下更差的残骸**：`freezeEligibilityTx` 的
`ON CONFLICT(external_account_id,freeze_reason,trigger_object_type,trigger_object_id) WHERE status='open'`
（`consumption.go:2385`）会**复用那张还开着的冻结**，不会开第二张。所以最坏情况是
35 分钟后回到今天这个状态，多一串审计记录，工具随时可以在根因修好后再跑一次
（幂等，见 `TestIngestRequeueDeadApplyRequeuesEveryRowWithItsOwnAudit` 的二次运行断言）。

**否掉的两个替代方案：**

- *置成 7（「再给一次机会」）*：一次无关的瞬时错误——甚至一次租约过期后的重新认领
  （认领步骤无条件 `attempt_count+1`）——就把它打回死信。没有余量。
- *抬高 worker 的认领阈值*：那是改所有事件的全局行为，超出修复工具的职责，而且等于
  把死信这一等级本身废掉。

### 问题 2：要不要动那 3 张 `EVENT_DEAD` 冻结？→ **不要动。而且它不会变成孤儿**

结论与倾向一致，但真正的理由不是「冻结是独立业务判断」这条软理由，而是下面两条硬的。

**（a）解冻会把「重投失败」这个情况变得比现在更糟。**
`tryPublishEconomicScanCyclesTx` 判周期完整性的过滤条件是
（`backend/internal/postgresstore/consumption.go:713`）：

```sql
count(*) FILTER (
    WHERE sie.processing_status NOT IN ('processed','parked_identity')
      AND NOT (sie.processing_status IN ('failed','dead') AND ef.id IS NOT NULL)
)
```

即「dead 且有开着的冻结」＝视为完整。冻结正是让一条死事件不再吊住扫描周期的东西。
如果工具在重投的同时解了冻，而重投又失败了，就会出现**「死了但没有冻结」**——这条
事件会无限期把它的周期压在 `processing`，通过
`source_economic_one_active_scan_cycle`（迁移 0009 的部分唯一索引）让该 stream 的
**每一个新批次**都拿到 `ErrScanCycleBusy`，整条流水线卡死。那正是
`XM-INV-SCAN-CYCLE-SUPERSEDE` 记录的那一类生产事故。

**保留冻结，等于让「再死一次」这条路径退回到今天的状态，而不是更坏的状态。**

**（b）「重投成功后冻结会不会变成永远无人处理的孤儿」——不会，而且顺序是被系统强制的。**

冻结留在原地就是一张普通的 `status='open'` 行：`GET /api/v1/admin/eligibility-freezes`
（`httpapi/server.go:210`）照常列出它，`POST .../{id}/resolve` 照常能处置它
（迁移 0010 还专门建了 `eligibility_freezes_admin_page_idx` 给这个页面）。没有任何
东西会自动解它——那是刻意的，解冻是带 MFA 与加密处置说明的人工判断（09-02 有先例）。

更关键的是：`ResolveEligibilityFreeze`（`eligibility_operations.go:218`）有两道前置
关卡，**恰好使「先重投、后解冻」成为唯一合法顺序**：

```go
EXISTS(SELECT 1 FROM eligibility_projection_jobs j WHERE j.external_account_id=$1)
    → domain.ErrEligibilityProjectionPending
// 以及：该账号 as_of <= finalized_through 的最新一条余额证据
if evaluation != "matched" && evaluation != "positive_classified_non_cash"
    → domain.ErrEligibilityEvaluationUnmatched
```

也就是说，**今天**（洞还在、那条余额快照还没进账）想去解这张冻结，要么被
`ErrEligibilityEvaluationUnmatched` 挡下，要么是拿着一份缺了证据的旧评估去宣布账号
干净——顺序反了。重投成功之后，缺的快照落库、被评估，管理员才有真东西可依据。
所以冻结不是被遗忘的孤儿，是一个**被系统排好队的待办**。

补充：解掉该账号最后一张开着的冻结时，`ResolveEligibilityFreeze` 自己会把
`eligibility_status` 改回 `active` 并补一个投影任务——工具不需要、也不应该代劳。

工具因此做了一件事：**把冻结读出来打印**（freeze id、账号、reason），让操作者在动手前
就看见「补完数据之后还有谁压着这个账号」。测试
`TestIngestRequeueDeadLeavesFreezesAndAccountStateUntouched` 把「不动」钉死，并做了
变异验证（变异 D）。

### 问题 3：重投会不会破坏扫描周期的完整性判定？→ **查清了。已发布的周期不受影响；未发布的会被暂时吊住，且双向自愈**

分三种 `cycle_status` 讲，工具的 dry run 会把每条事件所属的每个周期及其状态打出来，
因为这一行决定了这次重投属于哪一种。

**`published`（生产上这 3 条最可能的情况）：安全，什么都不会变得不自洽。**

1. **已发布的周期不会被重新评估。** `tryPublishEconomicScanCyclesTx`
   （`consumption.go:684`）只 `SELECT ... WHERE cycle_status='processing' AND
   final_sequence IS NOT NULL`。
2. **没有任何路径能把 `published` 改回去。** 全仓 `cycle_status=` 的写入只有四处：
   两处写 `'blocked'`、一处写 `'published'`、以及 `CommitSourceBatch` 的
   `ON CONFLICT DO UPDATE`（`source_sync.go:352`）。最后那一处的 CASE 确实可能写
   `'processing'`，但它的 `WHERE` 带着
   `source_economic_scan_cycles.final_sequence IS NULL`，而一个已发布的周期必然
   `final_sequence IS NOT NULL`（发布的前提），所以它永远匹配不到已发布的行。
3. **重投后的事实仍然收得进来。** `verifyFactBatchContextTx`（`consumption.go:1025`）
   显式接受 `cycle_status IN ('receiving','processing','published')`——这是设计好的，
   不是巧合。
4. **它会作为「迟到事实」落库，而系统本来就有这条路径。**
   `observeEligibilityFact` 里 `late := !in.EventTime.After(account.FinalizedThrough)`
   为真时走 `reprojectEligibilityTx` 重算（`consumption.go:1828`）；
   XM-INV-ELIG-QUEUE-NARROW 已经把「迟到事实一律冻结整个账号」那条粗暴防御删掉了，
   现在只有真的造成红冲（已开票金额会下降）才开一张精确的、按 funding_lot 收窄的冻结。

   测试 `TestIngestRequeueDeadDoesNotReopenAPublishedScanCycle` 钉住 1 和 2：重投后
   周期仍是 `published`，再跑一次发布 pass 也仍是 `published`，且
   `source_economic_stream_watermarks` 的 `watermark_at`/`source_sequence` 一个字节
   都没动。

**`processing`（周期还没发布）：会被暂时吊住，这是正确的，而且自愈。**
`'queued'` 不在 `('failed','dead')` 里，所以那条豁免不再适用，事件重新计入
`incomplete`，周期停止发布，直到事件再次终态化（成功 → 发布；再死一次 → 冻结还在 →
又算完整 → 发布）。测试
`TestIngestRequeueDeadHoldsAnUnpublishedScanCycleUntilTheEventTerminates` 把两个方向
都跑了一遍。**这是本工具唯一一个「重投本身会让别的东西暂时变差」的情形**，所以 dry run
必须打印 `cycle_status`，运维要在动手前看这一行。

**`blocked`：重投是徒劳的。** `verifyFactBatchContextTx` 只接受那三个状态，`blocked`
会拿到 `domain.ErrConflict`，事件会把 8 次重试全部烧完再死一次。

> **2026-09-08 修订（生产实测后）**：第一版只把这一条写进文档、让人自己对照，这是错的
> ——工具现在自己拒绝，见《2026-09-08 生产 dry run》。同时纠正这一节当初的一处
> **不够精确**：这里说的「周期」不是「事件关联到的任意周期」，而是**经由
> `first_batch_id` 到达的那一个**（`ReplayBinding`）。一条事件可以同时关联到多个周期，
> 而只有 replay binding 上的状态决定成败。生产上那两条 usage 就是
> 「blocked（replay binding）＋ published（其它绑定）」，按旧写法很容易读反。

**顺带确认过的一条：** balances 流的 `invalidBalanceSnapshot` 判定用的是
`count(*) FILTER (WHERE sie.entity_type='balance_checkpoint')` 与
`scan_snapshot_row_count` 比较——这个计数与 `processing_status` 无关，重投改不动它。

---

## 2026-09-08 生产 dry run：结论与第二轮修改

产品负责人在生产上跑了 dry run。**结果是一条都不能投**，而且暴露了报告的一处缺陷。

### 生产实况

| 事件 | 流 | `first_batch_id` 所绑定的周期 | 状态 |
|---|---|---|---|
| `fcd2e2c6` | balances | `e58b9430` | **blocked** |
| `63872270` | usage | `b1de0e2b` | **blocked** |
| `bb412266` | usage | `b1de0e2b` | **blocked** |

两条 usage 在报告里还各列了一个 `9ca5afcb` = `published`，但那是**别的批次**下的关联。

### 缺陷：判据打印了，结论却要人自己去对照手册

第一版的 dry run **正确打印了 `cycle_status=blocked`**，可同一行的 `REQUEUED` 仍是
`true`，末尾还汇总「total requeued: 3」。手册在报告之外，报告在人眼前——等于把判据
交出去，让人自己得出相反的结论。而且它把**所有**关联周期平铺列出，没说哪一个才算数，
于是「有一个 published」看起来像是好消息。

**已修，判据现在由代码执行：**

1. 工具自己解析出**唯一算数的那条绑定**（`ReplayBinding`），复刻运行时的真实路径：
   `ClaimUnprocessedSourceEvents` 按 `sie.first_batch_id` 连 `source_ingest_batches`，
   把该批次的 `batch_id` / `scan_cycle_id` 放上 claim，应用层原样传下去，
   `verifyFactBatchContextTx` 按 **(event_id, batch_id, scan_cycle_id) 三元组**精确查。
   工具查的是同一个三元组，并且把 `schema_version='3.0'`、`m.payload_hash` 相等、
   `cycle_status ∈ {receiving, processing, published}` 这三项校验逐条复刻。
2. 判定不通过时：`REQUEUED false (replay blocked)`、一行
   `NOT REQUEUED: <原因，含周期 id 与 verifyFactBatchContextTx 字样>`，
   汇总行拆成 `total requeued` 与 `not requeued (replay blocked)`。
3. **apply 默认跳过这类**，行一个字都不改、审计一条都不写；要投必须显式加
   `--include-blocked-cycles`（帮助文本里写明它「只用于故意复现失败，不是修复手段」）。
4. 其余关联仍然打印，但逐条标注 `replay binding` / `other binding, NOT used by replay`
   ——`9ca5afcb=published` 那种「看起来是好消息」的行不会再被误读。

`TestIngestRequeueDeadSkipsEventsWhoseReplayCycleIsBlocked` 用**真实的 supersede 路径**
把生产这一形状完整复现了（旧周期被 supersede 成 blocked ＋ 同一个确定性 event id 在
后继周期里被重投递、后继周期发布），并顺带钉死了一条事实：**重投递不会复活死行**——
断言 `before.status == "dead"`，通过。

### 三个问题的调查结论

#### 1. 能不能把事件重新绑定到后继周期？——**不能，而且不该**

- **`source_economic_scan_cycle_events` 只有一个写入者**：`CommitSourceBatch`
  （`source_sync.go:413`；全仓其余 10 处引用全是读）。它在那笔事务里校验批次哈希链
  （`previous_batch_hash` / `body_hash` / `signing_key_id` / `sequence`）。手写一行映射
  等于在没有任何这些证据的情况下断言「代理在那个批次里投递过这条事件」——就是伪造来源。
- **对 balances 还额外具有破坏性**：`tryPublishEconomicScanCyclesTx` 会把周期内
  `entity_type='balance_checkpoint'` 的映射条数与代理签名的 `scan_snapshot_row_count`
  比对，不等即 `invalidBalanceSnapshot` → 周期置 `blocked`、
  `source_ingest_state.projection_status='blocked'`、并对**该来源下的每一个账号**
  调 `freezeSourceAccountsTx(SOURCE_GAP)`。手插一行会冻住全部 8 个账号。
- **supersede 时不迁移未完成事件，是刻意的吗？** 严格讲：
  `supersedeStaleActiveScanCycleTx` 的文件头注释**只字未提**遗留事件，所以它不是一条
  被明确记录下来的决定。但服务端迁移在设计上本来就不成立（上面两条），而且**存在一条
  正规路径**：代理的 event id 是确定性的
  （`agents/sourceagent/batch.go:264`，`deterministicUUID(source, entity, externalID, operation, payloadHash)`），
  所以代理重扫时会把同一个 event id 重新投递进新周期，映射由真实入库路径合法产生。
  **两条 usage 事件的 `9ca5afcb` 关联就是这么来的。**
  所以：**缺口不在 supersede，在最后一公里**——`CommitSourceBatch` 遇到已存在的行
  只跳过、不复活（对 dead 行也一样），而 `ClaimUnprocessedSourceEvents` 又钉死在
  `first_batch_id`。两者相加，一条「证据已经重新到位」的事件仍然取不走。
- 顺带确认：`superseded_by_scan_cycle_id` 全仓**没有任何代码读它**（只在
  `source_sync.go:533` 写、迁移 0022 里定义），它纯粹是给人看的痕迹。想「跟着后继走」
  没有现成机制可用，得新造。

#### 2. blocked 周期本身有没有合法的复活/重开路径？——**没有，也不该有**

`cycle_status` 的全部写入点：`'published'`（仅由 `tryPublishEconomicScanCyclesTx`
从 `'processing'` 改）、`'blocked'`（快照不自洽、水位线回退、supersede 三处）、
以及 `CommitSourceBatch` 的 `ON CONFLICT DO UPDATE`。最后那处的 CASE 虽能写
`'processing'`，但 `WHERE` 带 `final_sequence IS NULL` 且要求 `last_sequence+1` 严格衔接
——需要**代理**继续用那个它已经放弃的 cycle id 从下一个 sequence 接着发，正是
「客户端已放弃」这件事排除掉的可能。**没有任何面向运维的路径。**

而且不只是不成立，是有害：把 `e58b9430` / `b1de0e2b` 解除 blocked 会让它们重新变成
active 周期，与当前 receiving/processing 的周期在
`source_economic_one_active_scan_cycle` 上撞车，直接卡死整条 stream——正是 supersede
机制存在的理由。

#### 3. 这三条是不是永久丢了？——**要分开说**

**两条 usage 事件：证据没丢，丢的是取用路径。**
它们已经带着一条**真实的、代理签名批次投递产生的**映射，指向一个 `published` 周期。
`verifyFactBatchContextTx` 本来就接受 `published`。也就是说：这条事实并非「无法验证」，
系统里存着「代理确实在批次 B、周期 `9ca5afcb` 下投递过这条 payload_hash 完全相同的
事件」的证据。挡住它的只是 `ClaimUnprocessedSourceEvents` 取的是**第一个**批次绑定。

**因此存在一条不绕过校验的出路**（供负责人决策，我没有动手）：让认领在该事件**当前
仍然有效的**绑定里取一条（自然的选择是批次 sequence 最大的那条，即代理关于这条事件
的最新陈述），而不是永远取第一条。这**不是**放宽 `verifyFactBatchContextTx`——它原封
不动地照跑，只是拿到了一个**为真**的三元组。附带影响都指向「更正确」而非更松：
`SourceSequence` 会取较晚批次的序号（对一条迟到事实本就该如此）、
`StreamWatermarkAt` 取较晚的 `scan_ceiling_at`（那次扫描确实观测到了它）。
`first_batch_id` 全仓只有两处用途（`CommitSourceBatch` 的 INSERT、认领的 JOIN），
改动面很小。

**但这是 worker 核心契约的改动，属于另一个切片，需要负责人拍板 + 独立设计与测试，
我没有在本切片里做。**

**一条余额快照：只有一条指向 blocked 周期的绑定，承认丢失。**
（**这一条的前提来自生产 dry run 的输出，我没有生产访问权限**——执行前请用下面这条
查询再确认一次，它把三条事件的全部绑定和「哪一条是 replay binding」一起列出来。若
`fcd2e2c6` 意外也有一条状态可接受的其它绑定，那它就和两条 usage 同属一类，结论要改。）

```sql
SELECT sie.event_id, sie.entity_type,
       m.scan_cycle_id, c.cycle_status, m.batch_id,
       (m.batch_id = sie.first_batch_id) AS is_replay_binding
FROM source_ingest_events sie
JOIN source_economic_scan_cycle_events m
  ON m.source_instance_id=sie.source_instance_id AND m.stream_id=sie.stream_id
 AND m.event_id=sie.event_id
JOIN source_economic_scan_cycles c
  ON c.source_instance_id=m.source_instance_id AND c.stream_id=m.stream_id
 AND c.scan_cycle_id=m.scan_cycle_id
WHERE sie.processing_status='dead'
ORDER BY sie.event_id, is_replay_binding DESC;
```

它没有被重投递过，而且**结构上也不会有**：`deterministicUUID` 把 `payloadHash` 算进
event id，余额快照的 payload 含该时刻的余额与 `as_of`，之后任何一次扫描产生的都是
**另一个 event id**（会作为新事件正常入库——该账号后来的 1663 条快照就是这么进来的）。
所以这一条只能靠伪造映射才能 replay，而那是禁止的。

**「丢失」的准确含义，以及它并不阻塞客户：** 丢的是**一个历史时点的对账证据**，不是
当前余额状态。而 `ResolveEligibilityFreeze` 的证据关卡取的是
`as_of <= finalized_through` 里**最新的那一条**（`ORDER BY as_of DESC ... LIMIT 1`），
不是缺的那一条——所以这条快照永久缺失**不会**阻止解冻或让账号恢复可开票。

**需要负责人决定的两件事：**

1. 那两条 usage 事件要不要救（＝要不要开「认领改走当前有效绑定」这个切片）。
   不救的话，这个客户的账里会永久少两条用量记录——金额影响需要另行核算。
2. 余额快照（以及第 1 条若决定不救，那两条 usage）按丢失处理之后，**readyz 怎么办**。
   这里有一个必须点破的地方：**「承认丢失」本身不会让 readyz 恢复。**
   `validateSourceIngestRuntimeReadiness` 的第一条就是 `health.Dead > 0 → 503`，而
   `Dead` 数的就是 `processing_status='dead'`。只要这几行还是 `dead`，readyz 就一直
   503——处置冻结、让账号恢复可开票，都不改变这一点。要恢复，必须让这些行走到一个
   **非 dead 的终态**，而当前没有任何合法手段做到（重投会被拒、删除被
   `source_economic_scan_cycle_events` 的 `ON DELETE RESTRICT` 挡住，而且会毁掉证据）。

   **仓库里有一条可以照抄的先例**：`RequeueSourceDependency`
   （`source_sync.go:842` 一带）对「早于策略起点、永远不会被采用」的事件，就是把它们
   置成 `processing_status='processed'` 且 `processing_error='PRE_POLICY_SKIPPED'`
   ——不声称事实被采用了，只记录它永远不会被采用。按同样的形状加一个运维可执行的
   「不可重投，登记后终结」处置（例如 `processing_error='UNREPLAYABLE_BINDING'` ＋
   逐条审计），既能让 readyz 恢复，又不伪造任何来源；副作用也都是好的方向：
   `completeEligibilityCatchupTx` 数的是 `processing_status<>'processed'`，终结后
   卡住的 catch-up 会被释放，而涉及的周期本就是 blocked / 已发布，都不会被重新评估。

   **已经实现（2026-09-08，负责人批准）**：`--kind=ingest-acknowledge-unreplayable`，
   见下一节。**代码已落分支；对生产执行仍需产品负责人点头。**

## 处置：`--kind=ingest-acknowledge-unreplayable`（已实现）

给一条**永远无法重投**的死事件一个非 dead 的终态，不伪造任何东西。

### 形状：照抄 `PRE_POLICY_SKIPPED`，不另设计

`RequeueSourceDependency`（`source_sync.go`）对「早于策略起点、永远不会被采用」
的事件写的是：

```sql
SET processing_status='processed',processing_error='PRE_POLICY_SKIPPED',
    processed_at=now(),lease_token=NULL,lease_expires_at=NULL,
    dependency_kind=NULL,dependency_key_hmac=NULL,updated_at=now()
```

本处置逐字用同一个形状，只把标记换成 `UNREPLAYABLE_BINDING`。
**评审的人只需要判断「一致」，不需要判断一个新想法。**
它**不声称事实被应用了，它记录的是「它永远不会被应用」**——这两件事的区别
就是这个方案能成立的全部原因。

### 护栏

| 要求 | 实现 |
| --- | --- |
| 独立 kind，不混进重投 | `kindIngestAcknowledgeUnreplayable`，自己的 store 函数与汇总输出 |
| 默认 dry run，`--apply` 要 `--operator-id` | 同其他 kind；dry run 靠 `defer tx.Rollback` |
| 必须 `--event`，不接受批量 | CLI 层与 store 层**各拒一次**；`--account` 对这个 kind 直接报错 |
| 逐条审计 | `source_ingest_event.unreplayable_acknowledged`，object_id = event_id，after 载荷带 `fact_applied: false` 与不可重投的具体原因 |
| 不能变成「什么都能标 processed」 | **拒绝任何仍有有效绑定的事件**，复用同一个 `ingestRequeueDeadReplayBindingTx` 判定，错误文本指向 `--kind=ingest-requeue-dead` |
| 只处置死事件 | 谓词里钉死 `processing_status='dead'`，且 UPDATE 重新断言一次 |

冻结同样不动。

### 测试与变异验证

| 测试 | 钉住什么 |
| --- | --- |
| `TestAcknowledgeUnreplayableDryRunWritesNothing` | dry run 报出判定，但行与审计都没动 |
| `TestAcknowledgeUnreplayableClosesTheEventWithoutApplyingItsFact` | PRE_POLICY_SKIPPED 列形状逐列对比；**三张事实表里都没有这个 payload 的行**（缺席断言）；`SourceIngestHealth.Dead` 从 1 归 0 且 `Pending` 不变；恰好 1 行审计；冻结仍 open；重跑被拒且不写第二条审计 |
| `TestAcknowledgeUnreplayableRefusesAReplayableEvent` | 有效绑定的事件被拒，行未被修改，错误文本指向重投工具 |
| `TestAcknowledgeUnreplayableRequiresAnEventAndAnOperator` | 6 种缺参/错参全部拒绝，且一个字都没写 |
| `TestRunAcknowledgeUnreplayableRequiresAnEvent` | CLI 层：缺 `--event` 失败关闭；`--account` 不能当替代品 |

| # | 变异 | 预期红 | 实测红 | 对照组 | 实测 |
| --- | --- | --- | --- | --- | --- |
| L | **在 apply 路径里加进一条 `source_usage_events` 插入**（缺席断言的变异方向：引入被否定的行为） | 「不写事实」那条 | `ClosesTheEventWithoutApplyingItsFact`，报 `1 fact rows exist for this payload` | 其余 3 个 | 全绿 |
| M | `if !row.ReplayBlocked` → `if false`（去掉护栏） | 拒绝可重投事件那条 | `RefusesAReplayableEvent` | 其余 3 个 | 全绿 |
| N | `if !in.Apply` → `if false`（dry run 走 apply） | dry-run 那条 | `DryRunWritesNothing` | 其余 3 个 | 全绿 |

**变异 L 第一次又是假红，和变异 D 同一个坑：** 我又用 perl 注入带 `$1/$2/$3`
占位符的 SQL，perl 把它们当成捕获组吃掉了，结果是语法错。目标用例确实红了，
**但红在 `syntax error` 上，不是红在「出现了事实行」上**——只看「有红就算数」的话
这次变异就白做了。改用 Edit 写入不带占位符的 SQL 后才拿到真信号。
**同一个坑踩两次，记在这里：向被测代码注入 SQL 时不要用 perl。**

---

### 我特意没做的

- 没有为了让它能投而放宽 `verifyFactBatchContextTx` 的任何一项校验。
- 没有改数据模型、没有写 `source_economic_scan_cycle_events`、没有动 `cycle_status`。
- 没有连生产、没有部署。

---

## 改了什么

| 文件 | 内容 |
| --- | --- |
| `backend/internal/postgresstore/ingest_requeue_dead_repair.go`（新增，~520 行） | `RepairIngestRequeueDead` 及其输入/结果类型；`ingestRequeueDeadReplayBindingTx`（复刻 `verifyFactBatchContextTx` 的判据）。文件头注释写明三个设计问题的结论与理由，照样板 `projection_requeue_dead_repair.go` 的写法。 |
| `backend/internal/postgresstore/ingest_requeue_dead_repair_integration_test.go`（新增） | 11 个集成测试 + fixture（含用真实 supersede 路径复现生产形状的 `commitSupersedingCycle`）。 |
| `backend/cmd/eligibility-repair/main.go` | 新 `--kind=ingest-requeue-dead`、`--event` 收窄、`--include-blocked-cycles` 覆盖开关、`--account` 扩到这个 kind、`runIngestRequeueDead`、`printIngestRequeueDeadSummary`、包文档补一条。**收窄/覆盖标志用错 kind 时报错而不是静默忽略。** 顺手把 `run` 的三个可选标志收进 `repairFilters` 结构体（原本已经是 9 个位置参数、其中三个同类型相邻，容易写反）。 |
| `backend/cmd/eligibility-repair/main_test.go` | 既有 7 处 `run(...)` 调用改用 `repairFilters{}`；新增空库冒烟测试与标志作用域测试（自带对照组）；`TestRunApplyWithoutOperatorIDIsRejected` 加上新 kind。 |
| `docs/ELIGIBILITY-OPERATIONS.md` | 新增《Dead source ingest events》一节；含 replay binding 判据、`--include-blocked-cycles` 说明，以及《When the replay binding is blocked, there is nothing to run》。 |

无迁移。无 `Dockerfile` 改动（`invoice-eligibility-repair` 已经从 `./cmd/eligibility-repair` 构建）。
无上游（Sub2API/NewAPI）改动。

### 行为契约

apply 时对每一条命中的行，在**它自己的事务**里执行：

```sql
UPDATE source_ingest_events SET processing_status='queued',attempt_count=0,
    processing_error=NULL,next_attempt_at=now(),lease_token=NULL,lease_expires_at=NULL,
    dependency_kind=NULL,dependency_key_hmac=NULL,updated_at=now()
WHERE source_instance_id=$1 AND stream_id=$2 AND event_id=$3 AND processing_status='dead'
```

外加一条审计 `source_ingest_event.repair_requeued` / `source_ingest_event` / `<event_id>`
（复用 `RepairPreAnchorUsageEligibility` 的动作名与对象形状，只靠 payload 里的
`repair_tool` 区分），然后提交。dry run 在这一切之前返回，靠 `defer tx.Rollback` 保证
一个字都不写。

**刻意不动的三样：**
`eligibility_freezes`、`source_economic_scan_cycles`（理由见设计问题 2、3），以及
**`created_at`**。第三样值得单独说：就绪判定用 `created_at` 给 pending 事件计龄，所以
一条躺了 20 小时的行一旦重投就立刻「超龄」，`/readyz` 会**继续 503，但换一个理由**
（`source ingestion processing is unhealthy`，不再是 `contains dead events`），直到
事件真的走到 `processed`。改写 `created_at` 能让这个指标好看，但那是在伪造事件到达
时间——不做。执行手册里对这一点有专门提醒，别把它当成回归。

`catchup_key_hmac` 也不动，但工具会打印它是否存在：`completeEligibilityCatchupTx`
（`consumption.go:838`）数的是 `processing_status<>'processed'`，一条带 catch-up key 的
死行会**永久**卡住该 key 的释放，而
`source_account_eligibility_state.catchup_key_hmac` 一旦不清，账号就被
`tryPublishEconomicScanCyclesTx` 的 targets 过滤（`eas.catchup_key_hmac IS NULL`）
永久排除在 finalization 之外。重投是唯一能解开它的动作，所以这条信息要给操作者看见。

### 收窄

- `--event=<uuid>`：精确到一条 ingest 事件。**推荐生产用这个**：不依赖任何关联，
  对「已逐条核对过的 3 条」是最安全的形式。
- `--account=<uuid>`：通过「开着的 `eligibility_freezes.source_revision_hash` =
  事件 `payload_hash`」关联（`MarkSourceEventFailed` 的死信分支和
  `RepairPreAnchorUsageEligibility` 用的是同一条关联）。**已知盲区**：ingest 层没有
  账号列（payload 是密文），所以一条从未开出冻结的死事件对 `--account` 不可见——这正是
  `MarkSourceEventFailed` 注释里说的那个结构性缺口。不收窄或用 `--event` 才看得到。
  `TestIngestRequeueDeadNarrowsByEventAndByAccount` 把这个盲区当作规格钉住了。

---

## 测试与变异验证

新增 8 个集成测试（`internal/postgresstore`）+ 2 个 CLI 测试。全部通过。

| 测试 | 钉住什么 |
| --- | --- |
| `TestIngestRequeueDeadDryRunWritesNothing` | dry run 报出全部「找到的是什么」（attempts / error / created_at / dead_since / 扫描周期及状态 / 开着的冻结），且 ingest 行逐列不变、零审计行、冻结未动 |
| `TestIngestRequeueDeadApplyRequeuesEveryRowWithItsOwnAudit` | 生产同形状（2 usage + 1 balance_checkpoint）；每行 queued/0/error 清空/租约清空/next_attempt_at 到期；`created_at` 未被改写；**每条各 1 行审计**；二次运行是 no-op 且不写第二条审计 |
| `TestIngestRequeueDeadRequeuedEventIsClaimableAgain` | 重投前 `ClaimUnprocessedSourceEvents` 取不到；重投后取得到，`Attempt==1`，带可用租约与 payload，行变 `processing/1` |
| `TestIngestRequeueDeadLeavesFreezesAndAccountStateUntouched` | 冻结仍 open、`resolution_version` 仍 1、四个处置列仍 NULL、账号仍 `frozen`、零条 `eligibility.freeze.resolved` 审计 |
| `TestIngestRequeueDeadDoesNotReopenAPublishedScanCycle` | 已发布周期在重投后及再跑一次发布 pass 后仍 `published`，水位线一个字节没动 |
| `TestIngestRequeueDeadHoldsAnUnpublishedScanCycleUntilTheEventTerminates` | 未发布周期在重投后确实停止发布；事件 processed 后立刻发布 |
| `TestIngestRequeueDeadSkipsEventsWhoseReplayCycleIsBlocked` | **用真实 supersede 路径复现生产形状**（旧周期被 supersede 成 blocked，同一个确定性 event id 在后继周期重投递、后继周期发布）；重投递**不会复活死行**；dry run 与 apply 两种模式下都 `Requeued=false`、`TotalBlockedSkipped=1`、行与审计均未动；两条绑定都报出且只有 blocked 那条标为 `ReplayBinding`；`--include-blocked-cycles` 确实能强投 |
| `TestIngestRequeueDeadBlocksAnEventWithNoReplayBinding` | 另一条拒绝分支：v3 经济事件的 `first_batch_id` 上根本没有周期映射（`verifyFactBatchContextTx` 的 `ErrForbidden`），同样跳过 |
| `TestIngestRequeueDeadNarrowsByEventAndByAccount` | 两种收窄；`--account` 的冻结关联盲区；无关账号找到 0 条（不会退化成不收窄） |
| `TestIngestRequeueDeadBlocksAnEventWhoseBindingPayloadHashDiverges` | 第三条拒绝分支（`m.payload_hash` 不等）。**是全量门禁抓出来的**：这条最初塞在上一个用例末尾，而那个用例的死事件没有冻结、周期永远不发布，于是第二个周期撞上 `source_economic_one_active_scan_cycle`。拆成独立 fixture 后通过 |
| `TestIngestRequeueDeadApplyRequiresOperatorAndValidFilters` | apply 需要 operator UUID；畸形 `--event`/`--account` 被拒（而不是静默匹配 0 条）；被拒的调用没写任何东西 |
| `TestRunIngestRequeueDeadDryRunAgainstEmptyDatabaseReportsNothing` | CLI 接线（标志、密钥、迁移校验、store 调用） |
| `TestRunNarrowingFlagsRejectedForWrongKind` | 收窄/覆盖标志作用域，**自带对照组**（实现该标志的 kind 必须仍然接受） |

### 变异验证（每次只改一个条件/取值，跑完整用例集，记录红与绿）

| # | 变异（改条件/取值，不删代码） | 预期红 | 实测红 | 对照组（必须绿）| 实测 |
| --- | --- | --- | --- | --- | --- |
| A | `if !in.Apply {` → `if false {`（dry run 走 apply 路径并提交） | dry-run 用例 | `DryRunWritesNothing`、`NarrowsByEventAndByAccount`（后者的 dry run 会真的重投，导致后续收窄查不到——已预判） | 其余 6 个 | 全绿 |
| B | 审计 `object_id` 从 `candidate.EventID` 改成固定字符串 | 「每条一行审计」 | `ApplyRequeuesEveryRowWithItsOwnAudit` | 其余 7 个 | 全绿 |
| C | `attempt_count=0` → `attempt_count=attempt_count` | 可认领性 | `RequeuedEventIsClaimableAgain`（报 `requeued event was not claimable: []`）、`ApplyRequeuesEveryRowWithItsOwnAudit` | 其余 6 个 | 全绿 |
| D | **在 apply 路径里加进一条解冻 UPDATE**（缺席型断言的变异方向：引入被否定的行为） | 「不动冻结」 | `LeavesFreezesAndAccountStateUntouched`（`status="resolved" version=2`） | 其余 7 个 | 全绿 |
| E | dry run 返回裸 `candidate` 而不是填充过的 `row` | 「报出找到的是什么」 | `DryRunWritesNothing` | 其余 7 个 | 全绿 |
| F | `if row.ReplayBlocked && !in.IncludeBlockedCycles` → `if false`（永不跳过） | 两条 blocked 用例 | `SkipsEventsWhoseReplayCycleIsBlocked`、`BlocksAnEventWithNoReplayBinding` | 其余用例 | 全绿 |
| G | `ingestReplayableCycleStatuses` 加上 `"blocked"` | 只有 blocked-cycle 那条 | `SkipsEventsWhoseReplayCycleIsBlocked` | 其余用例（含 `BlocksAnEventWithNoReplayBinding`，它走的是另一条分支） | 全绿 |
| H | 绑定查询 `m.batch_id=b.batch_id` → `<>`（＝不再跟随 `first_batch_id`） | 「只有 replay binding 算数」 | `SkipsEventsWhoseReplayCycleIsBlocked` 报 **`Requeued=true for an event whose replay is refused`**——正是第一版那个缺陷；另有 3 条单绑定用例因反向失去绑定而红（已预判） | `LeavesFreezes…`、`DoesNotReopen…`、`Narrows…`、`BlocksAnEventWithNoReplayBinding`、`ApplyRequires…` | 全绿 |

变异 G 与 F 的对照很说明问题：G 只让 `SkipsEventsWhoseReplayCycleIsBlocked` 变红而
`BlocksAnEventWithNoReplayBinding` 保持绿，说明这两条用例确实钉的是两条不同的分支
（周期状态不可接受 vs 绑定根本不存在），不是同一条断言写了两遍。
变异 H 的失败信息**逐字**是第一版报告的缺陷，这是「只有 replay binding 算数」这条
判据确实在起作用的最直接证据。

关于变异 D 的一段插曲，值得记下来：**第一次写这个变异时它「假红」了。**
我用 perl 注入 SQL，`$2::uuid` 里的 `$2` 被 perl 当成捕获组吃掉，变成
`resolved_by=::uuid`，语法错。结果是**另外三个用例红了，而目标用例
`LeavesFreezesAndAccountStateUntouched` 反而绿**——因为解冻语句根本没执行成功，
per-event 错误隔离把它记成了每条事件的错误。如果当时只看「有红就算数」就会得出
完全相反的结论。重写成不带参数占位符的字面量 SQL 后才拿到正确结果（恰好也顺带
证明了 per-event 错误隔离是活的）。

变异结束后已把实现文件从备份逐字节还原（`diff` 无输出），并重跑确认全绿。

### 门禁

| 门禁 | 命令 | 结果 |
| --- | --- | --- |
| build | `go build ./...` | 通过 |
| vet | `go vet ./internal/postgresstore/ ./cmd/eligibility-repair/` | 通过 |
| 新用例 | `go test ./internal/postgresstore/ -run TestIngestRequeueDead -count=1` | 11/11 通过 |
| CLI 用例 | `go test ./cmd/eligibility-repair/ -count=1` | 通过 |
| 后端全量 | `go test -p 1 -count=1 ./...`（带 `INVOICE_TEST_DATABASE_URL`） | **EXIT=0，无一条 FAIL**（`postgresstore` 232.9s，`cmd/eligibility-repair` 15.9s，`testdb` 55.5s） |
| gofmt | `gofmt -l`（两个新文件） | 干净。整包列出的是全仓 CRLF 问题（`.gitattributes` 未覆盖 `*.go`），与本切片无关 |

集成测试库：本机 `invoice-test-pg` 容器（`127.0.0.1:55432`），
`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test?sslmode=disable`
——`internal/testdb` 会自动改写成本 worktree 专用库，不与其他 worktree 抢。

**必须带 `-p 1`。** `cmd/eligibility-repair` 与 `internal/postgresstore` 都用
`testdb.URL(t)`，解析到**同一个** worktree 专用库，各自又都会
`DROP SCHEMA public CASCADE`。并行跑这两个包会互相踩，报
`relation "external_accounts" does not exist`——我第一次省掉 `-p 1` 时就踩到了，
补上就绿。`internal/testdb` 只解决跨 worktree 的竞争，不解决同一 worktree 内跨包的，
这也正是仓库既定命令一直写着 `-p 1` 的原因。这是既有性质，不是本切片引入的。

**没有做的：** 任何生产连接、部署、`git push`。前端未改动，因此未跑前端门禁。

---

## 给执行者：生产上怎么跑这 3 条

这一节写给不了解来龙去脉的人。**按顺序做，不要跳步。**

### 0. 先确认前提

```sh
# 在开票主机、发布线检出目录下
docker compose --env-file "$PRODUCTION_ENV_FILE" -f deploy/docker-compose.prod.yml \
  exec -T postgres psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice -At -F '|' -c "
SELECT source_instance_id, stream_id, event_id, entity_type, attempt_count,
       processing_error, created_at, updated_at,
       catchup_key_hmac IS NOT NULL AS has_catchup_key
FROM source_ingest_events
WHERE processing_status='dead'
ORDER BY created_at, event_id;"
```

- 应当**正好 3 行**。多于 3 行说明期间又死了别的，先停下来问负责人——本工具默认
  不收窄会把所有死行一起投，那不是这次的授权范围。
- 记下这 3 个 `event_id`，后面每一条单独跑。

镜像里必须已经有本切片的二进制。确认办法：

```sh
docker compose --env-file "$PRODUCTION_ENV_FILE" -f deploy/docker-compose.prod.yml \
  exec -T api /app/bin/invoice-eligibility-repair --help 2>&1 | grep -c "ingest-requeue-dead"
```

输出 0 表示跑的还是旧镜像，**停下**，本工具还没上线。

### 1. dry run（不带 `--apply`，一个字都不会写）

先整体看一遍：

```sh
docker compose --env-file "$PRODUCTION_ENV_FILE" -f deploy/docker-compose.prod.yml \
  exec -T api /app/bin/invoice-eligibility-repair \
  --database-url-file=/run/secrets/invoice-db-url \
  --field-keyring-file=/run/secrets/field-keyring.json \
  --kind=ingest-requeue-dead
```

**把完整输出保存下来交给负责人过目。** 这份输出是这次修复唯一一份人类可读的「修复前
状态」记录——审计表里只存 before/after 的哈希，不存原文。

输出里逐条要看的三行：

- `replay_binding: cycle=<id> cycle_status=<status> batch=<id>`
  **只有这一行算数**，它是 `verifyFactBatchContextTx` 会拿到的那个三元组。
  下面的 `scan_cycle:` 列表里凡是标着 `other binding, NOT used by replay` 的，
  再健康也不代表这条事件能投。
  - `published`：安全，重投不会动它（见设计问题 3）。
  - `processing`：重投会把这个周期暂时吊住，直到事件再次终态化。可以做，但要知道
    这段时间该 stream 不发布水位线。
  - 其它（`blocked`、或根本没有绑定）：**工具会自己拒绝**，这一行会跟着一条
    `NOT REQUEUED: ...`，汇总计入 `not requeued (replay blocked)`。
    **不要用 `--include-blocked-cycles` 去强投**——那只会烧掉 8 次重试再死一次。
    2026-09-08 生产上这 3 条全部落在这一类；该怎么办见上面那一节。
- `open_freeze: <id> account=<uuid> reason=EVENT_DEAD (left open on purpose)`
  记下这 3 个 freeze id 和账号 id，第 4 步要用。
- `processing_error: ...`
  如果显示的是序列化冲突类错误，且**根因修复（另一位 agent 的 40001 不扣预算）还没
  上线**，重投很可能 35 分钟后再死一次。可以做（无害、可重复），但要向负责人说明
  这是「试一次」而不是「一定成」。理想顺序是根因修复先上线。

### 2. apply（逐条，一条一条来）

对第 0 步记下的每一个 `event_id` 分别执行，中间检查一次：

```sh
docker compose --env-file "$PRODUCTION_ENV_FILE" -f deploy/docker-compose.prod.yml \
  exec -T api /app/bin/invoice-eligibility-repair \
  --database-url-file=/run/secrets/invoice-db-url \
  --field-keyring-file=/run/secrets/field-keyring.json \
  --kind=ingest-requeue-dead \
  --event=<第 N 个 event_id> \
  --apply --operator-id=<审批管理员的 admin UUID>
```

`--operator-id` 必须是真实管理员 UUID（会写进审计的 actor）。工具在没有它时拒绝
apply。

失败也不要紧：工具幂等。一条已经重投过的行不再是 `dead`，重跑就是找不到、什么都不做。
如果输出里出现 `EVENT ERRORS`（比如序列化冲突），那一条没有被改动，直接对它重跑即可。

### 3. 跑完之后看什么

**（a）`/readyz` 不会立刻变绿——这不是回归。**
重投把 `Dead` 清零，但那 3 行立刻变成 `pending`，而就绪判定用 `created_at` 计龄
（20 多小时前），所以：

| 阶段 | `/readyz` | 理由文本 |
| --- | --- | --- |
| 现在（修复前） | 503 | `source ingestion contains dead events` |
| 重投后、处理完成前 | **仍然 503** | `source ingestion processing is unhealthy` |
| 3 条都 `processed` 之后 | 200 | —— |
| 若 8 次重试后又死了 | 503 | 回到 `contains dead events` |

**理由文本从 `contains dead events` 变成 `processing is unhealthy`，是重投生效的第一个
信号。** 顺利的话 worker 下一个 tick（秒级）就取走，几十秒内应当看到下面的查询清零。

```sh
# 三条应当在一两分钟内全部变成 processed
docker compose --env-file "$PRODUCTION_ENV_FILE" -f deploy/docker-compose.prod.yml \
  exec -T postgres psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice -At -F '|' -c "
SELECT event_id, processing_status, attempt_count, processing_error
FROM source_ingest_events
WHERE event_id IN ('<id1>','<id2>','<id3>');"
```

如果一直停在 `failed`/`queued` 且 `attempt_count` 每 5 分钟涨 1，就是根因还没修好，
约 35 分钟后会回到 `dead`。这时**不要反复重投**，去等根因修复上线。

**（b）数据是否真的进账了——这才是本次修复的目的。**

```sh
docker compose --env-file "$PRODUCTION_ENV_FILE" -f deploy/docker-compose.prod.yml \
  exec -T postgres psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice -At -F '|' -c "
-- 用 dry run 里打印的 payload_hash 对，逐条确认事实已落库
SELECT 'usage', external_usage_id, event_time, source_revision_hash
FROM source_usage_events WHERE source_revision_hash IN ('<hash1>','<hash2>','<hash3>')
UNION ALL
SELECT 'checkpoint', checkpoint_id, as_of, source_revision_hash
FROM balance_reconciliation_checkpoints WHERE source_revision_hash IN ('<hash1>','<hash2>','<hash3>');"
```

预期：3 条 `payload_hash` 各对应一行。**这一条查不到就等于没修好**，事件走到
`processed` 但事实没落库要立即回报（可能走了 pre-anchor/pre-policy 跳过分支）。

顺便看一眼审计（每条一行）：

```sql
SELECT object_id, actor_id, created_at FROM audit_events
WHERE action='source_ingest_event.repair_requeued' ORDER BY created_at DESC LIMIT 10;
```

**（c）冻结怎么后续处置——工具没动，也不该动。**

3 张 `EVENT_DEAD` 冻结仍然 `open`，账号仍然 `frozen`，**客户仍然开不了票**。这是对的：
补数据和「宣布这个账号干净」是两件事，后者是带 MFA 与加密处置说明的人工判断
（09-02 有先例）。

处置路径是后台的资格冻结队列（`GET /api/v1/admin/eligibility-freezes` →
`POST /api/v1/admin/eligibility-freezes/{id}/resolve`），**但要等两个条件**，否则
接口自己会拒绝：

1. 该账号的 `eligibility_projection_jobs` 行必须已经清空（重投触发的投影任务跑完会
   自删），否则报 `ErrEligibilityProjectionPending`；
2. 该账号最新一条 `as_of <= finalized_through` 的余额证据评估必须是 `matched`
   （或 `positive_classified_non_cash`），否则报 `ErrEligibilityEvaluationUnmatched`。

```sh
docker compose --env-file "$PRODUCTION_ENV_FILE" -f deploy/docker-compose.prod.yml \
  exec -T postgres psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice -At -F '|' -c "
SELECT (SELECT count(*) FROM eligibility_projection_jobs WHERE external_account_id='<account>') AS jobs_left,
       (SELECT count(*) FROM eligibility_freezes WHERE external_account_id='<account>' AND status='open') AS open_freezes,
       (SELECT eligibility_status FROM source_account_eligibility_state WHERE external_account_id='<account>') AS status;"
```

`jobs_left=0` 之后再去后台解冻。解掉**最后一张**开着的冻结时，系统会自动把账号改回
`active` 并补一个投影任务——不需要人工干预。

### 4. 出了问题怎么办 / 怎么回退

- 本工具只写 `source_ingest_events` 的状态列，**不删除、不改写任何事实数据、不动冻结、
  不动扫描周期**。最坏情况是那 3 行再烧 8 次重试后回到 `dead` + 原来那张冻结，
  即今天的状态。
- 没有「反向工具」，也不需要：把行手工改回 `dead` 只会重现今天的故障，没有意义。
- 如果重投后事件**成功了但结果不对**（例如落库的金额与预期不符），那不是本工具的问题
  域，走冻结/账本的既有处置路径，并保留第 1 步那份 dry run 输出作为修复前快照。

---

## 生产执行顺序（2026-09-08 定稿）

1. **上 XM-INV-CLAIM-BINDING**（含迁移 0031）。
2. **重跑 dry run**，确认那两条 usage 已从
   `NOT REQUEUED (replay blocked)` 变成可投；若没变，**停下来**，不要用
   `--include-blocked-cycles` 硬投。
3. **重投那两条 usage**（`--kind=ingest-requeue-dead --event=<id> --apply`）。
4. **核对事实真的进账了**——负责人加的这一道，必须做：

   ```sql
   SELECT external_usage_id, event_time, service_units, source_revision_hash
   FROM source_usage_events
   WHERE source_revision_hash IN ('<hash1>','<hash2>');
   ```

   预期两条 payload_hash 各对应一行。**「从队列里没了」和「数据进去了」是两回事**：
   事件走到 `processed` 只说明它不再占着队列，它也可能是走了
   pre-anchor / pre-policy 的跳过分支（那两条分支只写审计行，**不**写事实行）。
   查不到就立即回报，不要往下走。
5. **等投影排空**（该账号 `eligibility_projection_jobs` 为 0 行）。
6. **走后台正规界面处置三张冻结**（带 MFA 与加密处置说明）。
7. **最后才处置余额快照那一条**：
   `--kind=ingest-acknowledge-unreplayable --event=<id>`，先 dry run 给产品负责人过目，
   点头后再 `--apply`。这一步之后 `Dead` 归零，readyz 才能绿。

第 7 步**必须在最后**：它是一个不可逆的「承认这条数据没了」。只要前面几步还有
任何一步没走完，就不要做它。

---

## 交接注意事项

- 本切片**不含**根因修复。另一位 agent 在改「40001 不扣重试预算」，两者互不依赖，
  合入顺序无所谓；但**生产执行**顺序上，根因先上线会让这次重投一次成功的概率高很多。
- `docs/ELIGIBILITY-OPERATIONS.md` 新增的一节与本文的执行手册有意重复了一部分——
  运维平时看的是那份，这份是本次事件的完整记录。
- Go 源码注释全英文（与该包既有风格一致），中文只在本文与提交信息里。
