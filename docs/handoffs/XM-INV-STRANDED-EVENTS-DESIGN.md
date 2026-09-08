# XM-INV-STRANDED-EVENTS —— 「孤儿事件 → 全员停摆」分层修复设计

- status: design（负责人尚未拍板；未实现）
- 产出方式：2026-09-08 ultracode 工作流，16 个代理：3 读者 / 4 独立角度提案 / 8 对抗审稿（每案数据完整性 + 复发运维两视角）/ 1 综合
- 审稿结果：DRAIN-HOLD 与 HANDOVER 被数据完整性视角推翻；DEAD-CONTAINMENT 两票愿签字；CYCLE-WAIT 未被推翻但 12 条条件待修
- 依据 worktree `K:/发票/wt-XM-INV-RC104` HEAD da0e3df；每个行号引用均由综合者只读复核

---

# 开票系统「孤儿事件 → 全员停摆」分层修复计划

（依据：worktree `K:/发票/wt-XM-INV-RC104`，HEAD da0e3df，分支 ai/claude/XM-INV-RC104；三份读者地图 + 四个方案及八份审稿；我另外只读核对了下文引用的每一处行号。）

## 0. 大白话

系统把上游数据一批一批搬过来，每批装在一个「箱子」（扫描周期）里，箱子里每一条都记完账才能封箱。这次一个新客户绑定后，系统一口气放出他 6842 条历史用量去记账，把记账队伍堵住了；恰好有一个箱子超过 37 分钟没封上，下一个箱子按规则把它「作废」了。作废箱子里没记完的 3 条数据成了「孤儿」：每次重试都必然失败，失败 8 次就被判「死」。系统有一条硬规矩——只要有一条「死」数据，这个来源下的**所有**客户都不能开票——这就是那 25 小时。上周的 RC104 修好了「堵车时误判失败」，但没碰「作废 → 孤儿 → 判死 → 全员停摆」这条链；而且 RC104 新加的「优先用最新一次投递」还埋了一颗新雷：任何等着绑定的老数据，一旦客户绑定就会因为「投递时间对不上」被判死——所以「更多用户绑定」在今天的代码下**会出事**。要修三样东西：第一，挑投递记录时先看时间对不对（几小时的活，顺带能把现在卡住的 3 条合法清掉，让那 6 个用户恢复）；第二，「死」数据只冻结它自己的账号，不再拖住别人；第三，让重新投递真的能把孤儿救回来，而不是判死。修完之后仍然会有孤儿产生（只是不再致命）、仍有几类数据只能人工核销，还有一件四个方案都没碰的事：每次大客户绑定补数的那几十分钟，整个来源仍然会停摆。

**直接回答「如果更多用户绑定怎么办」**：今天（RC104）再绑一个有停放数据的客户，大概率复发同类闩死，原因见 L0；L0 上线后这条路堵住；但补数期间的整来源停摆（L4）要另外立项。

## 1. 分层计划表

| 层 | 防住什么 | 防不住什么 | 改动量 | 依赖 | 顺序 | 必需？ |
|---|---|---|---|---|---|---|
| **L0 XM-INV-BINDING-SKEW**<br>挑投递记录时先看时间合法性（从 HANDOVER 方案 B/C 与三份审稿的共同条件中抽出） | ① RC104 埋的雷：停放（parked）事件被后续周期重投过（credits 流每分钟重扫全量，`agents/sourceagent/economics_db.go:291-296`）→ 客户绑定唤醒 → 认领选到最新绑定（`source_sync.go:646-648` 只看周期状态，不看时间）→ 5 分钟规则必败（`consumption.go:322-323`）→ 判死闩死。② 两个修复工具互相推诿：现有 2 条 usage 死信可被合法写掉 → Dead 归零 → readyz 解闩 → 6 个用户恢复 | 孤儿的产生；首批次落在 blocked 周期的孤儿仍判死；放大面 | **S**：3 个文件约 40 行 + 4 个测试，无迁移，半天 | RC104 已在生产 | **1** | **必需，立即** |
| **L1 XM-INV-DEAD-CONTAINMENT**<br>死信只冻自己账号，流级只对「无人认领」的死信 fail-closed | 已被冻结兜住的死信不再让同来源其他客户 `source_unavailable` / 提交被拒（五流 AND 在 `service.go:611-625`）；readyz 200 但带 degraded；解冻关卡防止「顺手解冻」把放大面重新打开 | 无法归因的死信（cutover manifest、解密失败、未加 hint 的路径）仍闩死；判死前 ~40 分钟 pending 窗口；孤儿产生 | **M**：约 250 行 + 9 组测试 + 1 条只加索引的迁移 + 前端 15 行 + 文档，2-3 天 | L0（审稿 B 列为硬前置） | **2** | **必需（止血）** |
| **L2 XM-INV-OBSERVED-AT-PER-BINDING**<br>重投递记录自己的观测时间（HANDOVER 方案 B 部分独立成片） | 手册写的「唯一正规恢复 = 代理重扫」（`docs/ELIGIBILITY-OPERATIONS.md:645-650`）今天根本走不通：`observed_at` 首写冻结（`source_sync.go:424-431`），映射行不带观测时间（`:437-443`），任何晚于首送 5 分钟的重投都过不了。本层让重投绑定通过 → 孤儿在下次重扫后能落地（usage 6h 对账窗内；credits 每分钟） | 对账窗外的旧行、balances 检查点（结构上永不重投） | **S-M**：1 条加可空列迁移（无 FK，无回填）+ 映射 INSERT 写列 + 认领 COALESCE + 工具自动继承 + 4 测试，1-1.5 天 | L0（复用同一时间判据常量） | **3** | **必需（让「治本」路径真的存在）** |
| **L3 XM-INV-CYCLE-WAIT**<br>「绑定周期已 blocked」不判死、退预算、进等待态、只冻本账号、重投自动唤醒 | 孤儿第一次被拒就进等待，不烧 8×5 分钟，不进 Dead/Pending；重投到达自动唤醒（配 L2 落地）；失败有名字（今天 blocked/哈希/水位/单位不符全是同一个裸 `ErrConflict`，`consumption.go:1024-1027`；RunOnce 只分三档，`source_processor.go:205-262`） | 孤儿产生；无 hint 事件；永不重投的孤儿（等待到人工核销）；账号解冻仍需 admin MFA | **M+**：约 300 行 + 放宽 CHECK 迁移 + 8 测试（含 1 个确定性并发用例）+ 审稿 12 条条件，3-4 天 | L0、L2 **同一发布**；L1 的解冻关卡 | **4** | **建议（第二阶段）**：把 L1+L2 的「每个孤儿两步人工」变成「一步」，并消掉 40 分钟烧预算 |
| **L4（待立项）**<br>补数期间的 pending 不挡无关账号 | 每次绑定补数的整来源停摆：`EVENTS_PENDING` 在流级 Ready 是致命的（`source_sync.go:1119-1131`，非致命集合只有 `ECONOMIC_RESCAN_ACTIVE` `:1071`），且五流闸不带 busy 宽限；6842 条 ≈ 45 分钟全员开不了票——这与本次死信无关，是每次绑定都会发生的 | — | 未设计，估 M-L | 需与 `runtime.go:770-775` 记录的「不可逆操作用更严策略」意图对账 | 5 | **建议立项**，四个方案都未覆盖 |
| L5 DRAIN-HOLD 后端半边（只作废空闲周期 + 有名字的作废理由） | 排空中的周期不被作废 → 少产生孤儿 | 卡死的周期会把整条流拖到人来为止 | M（跨代理+后端） | L0 | 后议 | **锦上添花**，见 §4 |

## 2. 各层验收条件（= 审稿 conditions）与「变异必红」测试

### L0 XM-INV-BINDING-SKEW
范围：`consumption.go` 新增 `factClockSkewTolerance = 5m` 并渲染成 SQL 片段，`validateFactMetadata` 改用它；`source_sync.go:646` 臂 1 加 `AND b.scan_ceiling_at <= sie.observed_at + <5m>`；`ingest_requeue_dead_repair.go:515-527` 的 ReplayBlocked 加第 4 例（ceiling > observed+5m，只可能在臂 2 出现）。

验收条件（来自 DRAIN-HOLD 审稿 A 条件 1、CONTAINMENT 审稿 B 条件 1、HANDOVER 审稿 A 残余风险）：
- 5 分钟判据物理上只剩一处 Go 定义；三张事实表的 DDL CHECK（0009:327/361/442）用测试对照，不再各写一份。
- 上线前负责人跑只读普查：有 ≥1 条 published 周期映射且状态为 parked_identity 的事件数量（这是「存量弹药」；L0 后无害，但数量说明离复发多近）。
- 修复工具与运行时对「可重投」定义一致：`ingest-requeue-dead` 对现有 2 条 usage 报 ReplayBlocked=true，`ingest-acknowledge-unreplayable` 放行。

变异必红：
1. **停放后唤醒**：事件首送 observed=now-30m 落 C1（published），C2（published，ceiling=now）重投同一 event_id；唤醒后认领必须选 C1 批次，`validateFactMetadata` 通过，事实落地。**旧实现下此测试红**（选 C2 → "source fact event time/watermark is invalid"）。变异：删掉臂 1 的时间过滤 → 红。
2. **credits 变体**：同上但每周期都重投（模拟 credits 归零重扫，5 个周期）→ 仍选首批次。变异同上。
3. **常量对照**：`pg_get_constraintdef` 三张事实表 CHECK == 渲染常量；claimBindingSelect 文本含渲染片段。变异：常量改 4m → 红；SQL 手写 '5 minutes' 字面量 → 红。
4. **工具一致**：死事件首批次在 blocked 周期 + published 周期有晚 2h 的重投绑定 → requeue dry-run ReplayBlocked=true、acknowledge dry-run Acknowledged=true。变异：删过滤 → ReplayBlocked=false → 红。

上线后生产动作（负责人签字项）：dry-run 确认 → `ingest-acknowledge-unreplayable --apply` 写掉 3 条 → Dead=0 → readyz 200 → 6 用户恢复。写掉 2222 的 2 条 usage 是**少算**他的消费（保守方向，不会多开票）；2222 本人的 3 个冻结另走 admin 流程。

### L1 XM-INV-DEAD-CONTAINMENT（审稿两方均 would_ship=true）
验收条件（合并两份审稿）：
- **A1/B3** 上线前负责人跑方案给的只读查询，确认 3 条死信都有 open 冻结兜住；任一 false 先按 L0 写掉。
- **B1** L0 已合入（否则解冻关卡是一把没有钥匙的锁）。
- **A2** 「已兜住的 balance_checkpoint 死信落在已发布周期 → 结转证明写入不可变假证明」二选一并写进手册：(a) `ensureBalanceCarryForwardProofTx` 把「本周期映射到该账号 open 冻结所指死信」视为已有检查点，返回 pending；或 (b) 明文承认该账号该周期余额不可重投、修 acknowledge 护栏。删除方案里「账本不会失真」的绝对表述。
- **A3** 解冻关卡查询走 `source_ingest_events_readiness_active_idx`，T6 加 EXPLAIN 断言；不在 `source_ingest_events` 建新索引。
- **A4** degraded 节流日志用 Warn 级（与 `eligibilityProofPendingWarner` 一致），否则永久触发 ` ERROR ` 盯守。
- **B4** 保留 `runtime.go:762` 诚实闸：Ready=true 且 Contained>0 ⇒ Reasons 必含 `EVENTS_DEAD_CONTAINED`。
- **B2** RUNBOOK 新增行不得直接指向 requeue-dead；写明「重投一条兜住的死信 = 约 40 分钟 EVENTS_PENDING 全实例不可开票」。
- **A5** 文档补 `RepairPreAnchorUsageEligibility` 直接 resolved 那扇门。
- **A6** 负向用例：另一账号同 payload_hash 的冻结当前语义也算兜住，用断言钉死并注释依赖内容哈希唯一性。
- **A7/A8** 标注 `deploy/postgres/verify-source-readiness-index.sh:192-221` 查询副本已过期；修正对 `consumption.go:474` 的误引；合入前核对迁移编号（当前最新 0031，多分支在飞）。
- **B5** 修正测试变异归因：T4 第一条改用 `{Dead:1,DeadContained:1}→nil`；T2 写明是「冻结 resolved 后翻回致命」那段让变异变红。

变异必红（至少两条）：
1. **两账号隔离**（真库）：同一来源 A/B 两账号，A 的一条 usage 经 RunOnce 真正判死并经 hint 冻结；断言 B 的 ListFundingLots 非 source_unavailable、Submit(B) 通过 assertSourceFreshTx，A 被额度谓词挡住。**旧实现下 B 三处全不可用 → 红**。变异：删 assertSourceFreshTx 的兜住谓词 → Submit(B) 红；把 A 的冻结手动 resolved → B 必须重新不可用（判定是活的，若改成缓存列则红）。
2. **解冻关卡**：EVENT_DEAD 冻结 + 同 hash dead 事件 → Resolve 返回 `ErrEligibilityDeadEventUnrepaired`；仅 EVENT_PAYLOAD_DRIFT（同 hash）也被拒；acknowledge 后不再是该哨兵。变异：删关卡 → 红；关卡只查 freeze_reason='EVENT_DEAD' → DRIFT 变体红。
3. **四个健康面同源**：每个面单独断言 Contained=1/Ready=true；变异任一面去掉谓词 → 只有「resolved 后翻回致命」那段红（按 B5 归因）。

### L2 XM-INV-OBSERVED-AT-PER-BINDING
范围：迁移 0032（或下一空号）给 `source_economic_scan_cycle_events` 加可空列 `observed_at`（无 FK、无回填，元数据级）；CommitSourceBatch 映射 INSERT 写 `event.ObservedAt`；认领臂 1 SELECT/WHERE 与主查询改用 `COALESCE(m.observed_at, sie.observed_at)`；修复工具因逐字嵌入同一 SQL 自动继承。

验收条件（从 HANDOVER 两份审稿中与 B 部分相关的抽出）：
- 只加可空列，不加 FK/CHECK（避免全表锁）；先应用迁移再上二进制；回滚二进制不需要回滚迁移。
- 落地事实的 `observed_at` 取重投观测、`stream_watermark_at` 取重投批次 ceiling：可见时间「只晚不错」，写进设计说明；不得让事实水位钉在从未发布的周期上。
- 映射表若日后出现第三个写者（或回填）不写 observed_at 会回退到首送观测——写进注释。
- 与 L0 共用同一常量。

变异必红：
1. **重投带自己的观测时间**：首送 observed=now-30m 落 C1；C1 被 supersede（blocked）；C2 重投 E 且 ObservedAt=now，C2 published。断言 claim 选 C2 批次、`claim.ObservedAt==now`、事实真的经 ObserveUsageEvent 落地（穿 claim→verify→validateFactMetadata→INSERT 四段）。**旧实现下红**（选 C2 但 observed 冻结 → 5 分钟规则败）。变异：映射 INSERT 不写 observed_at → NULL → 回退首送 → 过滤排除 C2 → 认领回落 blocked 首批次 → 红；主查询改回 `sie.observed_at` → 红。
2. **历史行兼容**：测试 UPDATE 把 (C2,E) 的 observed_at 置 NULL 模拟旧行 → 认领不得选它（回落首批次）。变异：COALESCE 改成只读 m.observed_at → NULL 比较恒假 → 行为变化 → 红。

### L3 XM-INV-CYCLE-WAIT（审稿两方 refuted=false，would_ship=false，条件可修）
验收条件（合并两份审稿，共 12 条）：
- **A1** `ResolveEligibilityFreeze` 护栏：`trigger_object_type='source_ingest_event'` 的冻结，对应事件必须 processed（含 UNREPLAYABLE_BINDING 写掉）才可解；同护栏覆盖 EVENT_DEAD-hint 冻结。**与 L1 的解冻关卡做成同一个护栏。** 方案第五节出口 (4) 改为「先让事件落地/核销，再解冻」。
- **A2/B1** 并发形状重做：方案第四节交错 (c) 的推理在 READ COMMITTED 下不成立（UPDATE 只锁快照里满足 WHERE 的行，不等锁、不进 EvalPlanQual）。修法：Mark 先冻结（取 eas 锁）再取 sie 行锁，全局锁序 eas < cycle < sie；CommitSourceBatch 把 `:413-416` 的 SELECT 扩成同时读 processing_status，预读为 processing/waiting 时先按主键 `FOR UPDATE` 再条件唤醒。T4b 判据改为「CommitSourceBatch 后端在 sie 行锁上等待」，并在两连接夹具下证明无 40P01。
- **A2 补** 唤醒条件用周期 upsert `RETURNING cycle_status ∈ {receiving,processing,published}`，不用批次自报的 ProjectionStatus。
- **A3** `processPaymentOrder` / `processSubscriptionPurchase` 的 ObserveFundingLot 错误包 `wrapWithAccountHint`（verifyFactBatchContextTx 有五个调用点，方案只数了四个）；T8 加充值/订阅两个臂。
- **A4** acknowledge 的 UPDATE 谓词（`:147`）与审计 before（`:158-160`）同步放宽到 waiting/source_scan_cycle，状态集合抽成一处。
- **A5** Mark 冻结前 `pg_try_advisory_xact_lock(...,43)`，拿不到退回 `MarkSourceEventBusy(15s)`，不阻塞到 lock_timeout；加与投影作业并发的确定性用例。
- **A6/B2** 与 L2 同一发布；发布门禁加端到端用例：09-07 形状（首送 observed、重投水位晚 >5 分钟）跑到 processed 且 readyz 全程 200。
- **A7** 方向陈述改正并写进手册：usage 缺失 = 低估（保守）；credits/退款缺失 = 高估（危险）；核销/解冻前按实体类型区分。
- **B3** 可见面同片：`awaiting_redelivery` 接到前端状态卡片；告警规则「等待 >6h」或「open 冻结 trigger='source_ingest_event' >6h」；改写 RUNBOOK:1856 的 100,000 阈值。
- **B5** T6 夹具显式断言 `bootstrap_kind='POLICY_ANCHOR'`（否则变异 (2) 恒绿）；加「Mark 失败后重认领收敛、attempt_count 不爬升」用例。
- **B6** 回滚手册去掉手写 SQL：加 wake 模式或明确「等 12h 兜底清空后再回滚」。
- **B7** eligibility-repair 加 dry-run 清单：触发事件已 processed 的 open SOURCE_GAP(source_ingest_event) 冻结。

变异必红：
1. **分级只对周期状态生效**（表驱动）：主臂 supersede 后 RunOnce → waiting/attempt=0/SOURCE_GAP 冻结一行/账号 frozen；控制臂 b（改账号 manifest hash 触发 verifyFactTrustTx 的 ErrConflict）→ 仍 failed/PROJECTION_FAILED/attempts=1。变异：RunOnce 分支改成 `errors.Is(processErr, domain.ErrConflict)` → 控制臂 b 变 waiting → 红；删 freeze 调用 → 红；不退 attempt → 红。
2. **就绪与其他账号不受影响**：Pending=0、Dead=0、流 Ready=true、assertSourceFreshTx nil、B 账号 active。变异：Mark 写成 'failed' → 红；Mark 对全源冻结 → B frozen → 红。
3. **并发收敛**（按 A2 重写）：Mark 持 sie 行锁时 CommitSourceBatch 在该行等待；最终 queued，两条审计各一。变异：删预锁 → 最终 waiting 且映射在活动周期 → 红。

## 3. 谁阻止复发、谁止血、谁治本

- **单独上就能阻止「这次事故」再来一遍**（3 条孤儿 → 判死 → 闩死 → 6 用户 24 小时）：**L1**（前提 L0）。孤儿仍死，但只冻 2222，其他人照常，readyz 200 degraded。L3 也能，且更彻底（根本不判死），但 L3 没有 L2 时孤儿会永远等待。
- **止血（限制影响面）**：**L1**。
- **治本**：必须诚实说——真正「不产生孤儿」的两个候选（DRAIN-HOLD、HANDOVER）都被审稿打回，需要重设计（§4）。本计划的「治本」定义为「孤儿能自己回家」：**L2**（重投能落地）+ **L3**（孤儿等待而非判死，重投自动唤醒）。周期仍会被作废，但作废不再意味着数据永久丢失或全员停摆。
- **L0** 既是 L1/L2/L3 的前置，也单独堵住 RC104 埋的新雷；它是唯一能在几小时内让当前 6 个用户合法恢复的动作。

## 4. 我们不做什么、为什么

- **不做 DRAIN-HOLD（代理侧等排空）**：审稿 A 判 fatal 的那条其实是 L0 要修的 RC104 隐患，修完后剩下的价值是「排空中的周期不被作废」；但它的 DRAIN_STALLED/DRAIN_EXCEEDED 自动作废等于机器替人核销数据（worker 停 40 分钟就丢一批可恢复的用量），只留 IDLE 则卡住的周期会把整条流拖到人来为止；另有 lastReconcile 被重放页误记、waiting_dependency 被判停滞、前 37 分钟无头无日志等问题。跨代理+后端 M 级，收益在 L3 落地后大幅缩水。留作 L5 后议。
- **不做 HANDOVER 主体（把映射行搬到后继周期）**：核心 SQL 返回的是批次的周期而非映射行的周期（`source_sync.go:637`），交接行在运行时是死行；修正后 claim 语义变化触及六个消费点；payments「迁全部行」会把无冻结的死支付事件带进活周期造成级联停摆；映射表加 FK/CHECK 全表锁；「骑手」事件让后继周期停在 processing → 5 分钟心跳闩 → 滚动停摆；balances 的 D 必须强制。需要重设计。**只收割它的 B 部分成为 L2**。
- **不改代理的放弃判据** `shouldAbandonLegacyReconcileCycle`（`economics_db.go:191`，任务描述的 :174 是其注释起点）：它是一次性的（`ReconcileWindowBounded` 只在 `:306` 置 true、无处置回），按代码 09-07 大概率没有触发，真实链是「processing 周期被投影拖过 37 分钟 → 被下一个增量周期作废」；本计划对作废的成因不敏感。需要用 09-07 代理日志核实，但不阻塞任何一层。
- **不改上游 Sub2API/NewAPI**：硬约束，且所有层都不需要。
- **不新增 freeze_reason、不新开自清态入口**：前者要改六处枚举；后者是验收线 2026-09-03 对设计 3(A) 的裁定范围，L3 复用 SOURCE_GAP + 独一无二的 trigger_object_type。
- **默认不做 balances 检查点自动退役（HANDOVER 的 D）**：不可逆写掉，需负责人单独签字；默认保持人工 `ingest-acknowledge-unreplayable`。
- **不手写 SQL 改库**：包括回滚步骤。

## 5. 残余风险（四层全部上线后仍防不住）

1. **每次绑定补数，整个来源停摆几十分钟**（L4 未设计）。EVENTS_PENDING 在流级是致命的，五流闸无宽限；6842 条 ≈ 45 分钟。这与死信无关，是每次绑定都会发生的。
2. **周期仍会被 37 分钟作废，孤儿仍会产生**；只是变成「一个账号冻结 + 等待重投」而非全员停摆。
3. **无法归因的死信仍闩死 readyz 与全实例**：cutover manifest、解密/JSON 失败、以及 L3 A3 补 hint 之前的充值/订阅路径。
4. **永不重投的孤儿**：balances 检查点（event id 折入快照哈希）、对账窗口外的旧 usage 行——只能人工核销，账号在此之前一直冻结。
5. **每个孤儿一条冻结，逐条 admin MFA 解冻**；一次作废几十个账号的周期就是几十次。
6. **告警面转移**：从「readyz 503、容器 unhealthy」变成「冻结队列 / Warn 日志 / degraded 字段」。若无人看，单账号可以静默很久。L1/L3 的告警条件是防线，不是保证。
7. **前提未经我核实**（我不能连生产库）：3 条死信是否都有 open 冻结；07:02:28/09:28:14 是 `scan_ceiling_at` 还是 `stream_watermark_at`（校验用的是前者，`source_processor.go:669`）；09-07 代理日志是否真有 legacy 放弃；迁移编号 0032 是否已被其他在飞分支占用。
8. **并发守卫来自 Postgres 行锁而非本代码**，可挂栅栏的注入点有限；L3 的并发用例只能覆盖设计好的交错。
9. **写掉 2222 的 2 条 usage** 是少算他的消费（保守），但他本人需要 re-anchor 才能恢复开票。

## 6. 第一片建议

**切片 ID：XM-INV-BINDING-SKEW**（= L0）

范围（一个人几小时，含变异验证）：
1. `backend/internal/postgresstore/consumption.go`：新增 `factClockSkewTolerance = 5 * time.Minute` 与渲染成 SQL 的 `factClockSkewToleranceSQL`；`validateFactMetadata`（:322-323）改用常量。
2. `backend/internal/postgresstore/source_sync.go`：`claimBindingSelect` 从 const 改为渲染变量，臂 1 WHERE（:646 之后）加 `AND b.scan_ceiling_at <= sie.observed_at + <factClockSkewToleranceSQL>`。
3. `backend/internal/postgresstore/ingest_requeue_dead_repair.go`：`ingestRequeueDeadReplayBindingTx` 多 Scan `sib.scan_ceiling_at`/`sie.observed_at`，switch 加第 4 例并在 reason 里点名 `validateFactMetadata`。
4. 四个测试（§2 L0 所列），其中第 1 条在旧实现下必须先跑一次确认是红的。
5. `docs/ELIGIBILITY-OPERATIONS.md:645-650` 加一句：重投绑定要通过时间合法性才算「当前有效」；在 L2 落地前，晚于首送 5 分钟的重投不会被选中。

不做：不加迁移、不改前端、不动 tryPublish、不动 RunOnce 分级。

上线后当天：`ingest-requeue-dead` dry-run → 确认 2 条 usage ReplayBlocked=true → `ingest-acknowledge-unreplayable --apply` 三条（负责人签字：写掉 2222 的 2 条 usage，方向保守）→ Dead=0 → readyz 200 → 6 个用户恢复。同时请负责人跑 §2 L0 的只读普查，数出存量「停放且带多个 published 映射」的事件，作为 L1 排期紧迫度的依据。