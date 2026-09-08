# XM-INV-DEAD-REQUEUE —— 把判死的 `source_ingest_events` 重新投回队列

- status: ready-for-review（未合入发布线，未 push，未 tag）
- branch: `ai/claude/XM-INV-DEAD-REQUEUE`
- base: `331777a`（RC103 发版材料）
- worktree: `K:/发票/wt-XM-INV-DEAD-REQUEUE`
- commits（2 条，按顺序）：
  - `61b6ac4` `feat(eligibility-repair): requeue dead source ingest events (XM-INV-DEAD-REQUEUE)`
    —— 实现、CLI 接线、集成测试、`docs/ELIGIBILITY-OPERATIONS.md` 新增一节
  - 本文（`docs(handoffs): XM-INV-DEAD-REQUEUE`）
- 新增 `invoice-eligibility-repair --kind=ingest-requeue-dead`，形状照抄
  `--kind=projection-requeue-dead`（XM-INV-PROJECTION-FAILURE-GRADING）。

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
会拿到 `domain.ErrConflict`，事件会把 8 次重试全部烧完再死一次。dry run 会打出来，
看到 `blocked` 就别投，先处理周期。

**顺带确认过的一条：** balances 流的 `invalidBalanceSnapshot` 判定用的是
`count(*) FILTER (WHERE sie.entity_type='balance_checkpoint')` 与
`scan_snapshot_row_count` 比较——这个计数与 `processing_status` 无关，重投改不动它。

---

## 改了什么

| 文件 | 内容 |
| --- | --- |
| `backend/internal/postgresstore/ingest_requeue_dead_repair.go`（新增，~420 行） | `RepairIngestRequeueDead` 及其输入/结果类型。文件头注释写明上面三个设计问题的结论与理由，照样板 `projection_requeue_dead_repair.go` 的写法。 |
| `backend/internal/postgresstore/ingest_requeue_dead_repair_integration_test.go`（新增） | 8 个集成测试 + fixture。 |
| `backend/cmd/eligibility-repair/main.go` | 新 `--kind=ingest-requeue-dead`、新 `--event` 收窄标志、`--account` 扩到这个 kind、`runIngestRequeueDead`、`printIngestRequeueDeadSummary`、包文档补一条。**新增：收窄标志用错 kind 时报错而不是静默忽略。** |
| `backend/cmd/eligibility-repair/main_test.go` | 既有 7 处 `run(...)` 调用补一个参数；新增空库冒烟测试与标志作用域测试；`TestRunApplyWithoutOperatorIDIsRejected` 加上新 kind。 |
| `docs/ELIGIBILITY-OPERATIONS.md` | 新增《Dead source ingest events》一节，与既有 `projection-requeue-dead` 一节同格式。 |

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
| `TestIngestRequeueDeadNarrowsByEventAndByAccount` | 两种收窄；`--account` 的冻结关联盲区；无关账号找到 0 条（不会退化成不收窄） |
| `TestIngestRequeueDeadApplyRequiresOperatorAndValidFilters` | apply 需要 operator UUID；畸形 `--event`/`--account` 被拒（而不是静默匹配 0 条）；被拒的调用没写任何东西 |
| `TestRunIngestRequeueDeadDryRunAgainstEmptyDatabaseReportsNothing` | CLI 接线（标志、密钥、迁移校验、store 调用） |
| `TestRunNarrowingFlagsRejectedForWrongKind` | 收窄标志作用域，**自带对照组**（实现该标志的 kind 必须仍然接受） |

### 变异验证（每次只改一个条件/取值，跑完整 8 个用例，记录红与绿）

| # | 变异（改条件/取值，不删代码） | 预期红 | 实测红 | 对照组（必须绿）| 实测 |
| --- | --- | --- | --- | --- | --- |
| A | `if !in.Apply {` → `if false {`（dry run 走 apply 路径并提交） | dry-run 用例 | `DryRunWritesNothing`、`NarrowsByEventAndByAccount`（后者的 dry run 会真的重投，导致后续收窄查不到——已预判） | 其余 6 个 | 全绿 |
| B | 审计 `object_id` 从 `candidate.EventID` 改成固定字符串 | 「每条一行审计」 | `ApplyRequeuesEveryRowWithItsOwnAudit` | 其余 7 个 | 全绿 |
| C | `attempt_count=0` → `attempt_count=attempt_count` | 可认领性 | `RequeuedEventIsClaimableAgain`（报 `requeued event was not claimable: []`）、`ApplyRequeuesEveryRowWithItsOwnAudit` | 其余 6 个 | 全绿 |
| D | **在 apply 路径里加进一条解冻 UPDATE**（缺席型断言的变异方向：引入被否定的行为） | 「不动冻结」 | `LeavesFreezesAndAccountStateUntouched`（`status="resolved" version=2`） | 其余 7 个 | 全绿 |
| E | dry run 返回裸 `candidate` 而不是填充过的 `row` | 「报出找到的是什么」 | `DryRunWritesNothing` | 其余 7 个 | 全绿 |

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
| 新用例 | `go test ./internal/postgresstore/ -run TestIngestRequeueDead -count=1` | 8/8 通过 |
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

- `scan_cycle: <id> cycle_status=<status>`
  - `published`：安全，重投不会动它（见设计问题 3）。**这 3 条预期是这个。**
  - `processing`：重投会把这个周期暂时吊住，直到事件再次终态化。可以做，但要知道
    这段时间该 stream 不发布水位线。
  - `blocked`：**别投**。事实会被 `verifyFactBatchContextTx` 拒收，事件会烧完 8 次
    重试再死一次。先处理周期。
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

## 交接注意事项

- 本切片**不含**根因修复。另一位 agent 在改「40001 不扣重试预算」，两者互不依赖，
  合入顺序无所谓；但**生产执行**顺序上，根因先上线会让这次重投一次成功的概率高很多。
- `docs/ELIGIBILITY-OPERATIONS.md` 新增的一节与本文的执行手册有意重复了一部分——
  运维平时看的是那份，这份是本次事件的完整记录。
- Go 源码注释全英文（与该包既有风格一致），中文只在本文与提交信息里。
