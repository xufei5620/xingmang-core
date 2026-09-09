# XM-INV-PENDING-RECON —— 待对平账号的闲置重评、分类器有符号口径与按需重评工具（实现交接）

- 分支：`ai/claude/XM-INV-PENDING-RECON-IMPL`，基线 `b3ded69`（RC107 生产提交，L1 XM-INV-DEAD-CONTAINMENT 已上线）
- 设计稿：`ai/claude/XM-INV-PENDING-RECON` 分支上的 `docs/handoffs/XM-INV-PENDING-RECON-DESIGN.md`
- 无迁移。仅 API 镜像改动，代理与桥接一字未动，Sub2API/NewAPI 上游源码未动
- 提交（按设计 §6 的四段拆分）：

| 段 | 内容 | 哈希 |
| --- | --- | --- |
| ① | C1 + C2 闲置重评 | `7bc8b80` |
| ② | C5 有符号期望口径 | `e36fba5` |
| ③ | C4 未知量级证据不重置连击 | `a14bcdf` |
| ④ | C3 新 repair kind + C6 文档 | `bba9ac0` |

---

## 1. §7 决策记录（负责人 2026-09-09 拍板）

| # | 决策 | 落地位置 |
| --- | --- | --- |
| D1 | (a) 接受闲置账号的第二次 matched 是复述 | `ensureBalanceCarryForwardProofTx` 闲置分支的注释里写明了这条取舍；`docs/ELIGIBILITY-OPERATIONS.md` 的「闲置重评」小节对运维复述一遍 |
| D2 | (a) C1 只对 `consecutive_matches >= N−1` 的账号触发 | `finalizeSourceAccountsTx` 第五个 OR 分支，阈值以 `$4 = pendingReconciliationExitMatches-1` 传入，常量仍只有一处 |
| D3 | (a) C4 做，独立提交 | 提交 `a14bcdf`，可整体剔除而不影响 C1/C2/C5 |
| D4 | (a) C5 现在做 | 提交 `e36fba5` |
| D5 | (a) 新 kind 语义为 `pending-reevaluate`（排队 + 诊断，不绕判据） | `backend/internal/postgresstore/pending_reevaluate_repair.go`；读者 3 的方案 (b)（人替代第二次匹配）明确不实现，函数头注释写明它需要验收线重裁 |
| D6 | L1 已随 RC107 上线，本切片直接建在其上 | C2 的闲置分支把 XM-INV-DEAD-CONTAINMENT A2 的死信 hold 排在自己之前，见下节 |
| D7 | XM-INV-ADMIN-CREDITS 立项、另一片做 | 本切片不动桥接契约、不重钉哈希 |
| D8 | (a) C2 允许包含下界（ceiling == finalized_through） | 候选查询本来就是 `scan_ceiling_at>=finalized_through`；`pending-reevaluate` 排的窗口 `requested_through=finalized_through` 正是靠它才够得着，用例 `TestPendingReevaluateApplyOnTheCeilingBoundaryDerivesEvidence` 钉死 |
| D9 | (a) 部署后先等 C1 自动处理 | 运行手册写明：报告的 `in requeue window: false` 就是「等下一次 finalize」，此时 apply 不改变任何东西 |

## 2. 实际改了什么

### C1 `finalizeSourceAccountsTx`（`backend/internal/postgresstore/consumption.go`）

`targets` 多带出 `eligibility_status` 与 `pending_reconciliation_consecutive_matches`，
`changed` 增加第五个 OR 分支：pending、连击 ≥ N−1、且窗口内有已发布 balances 周期时排一条投影
作业。作业行存在，末尾那条空推进 UPDATE 的 `NOT EXISTS (jobs)` 就跳过该账号，周期不会被烧掉。
`held`/SKIP LOCKED 与 ON CONFLICT 的 dead/processing 保护 CASE 一字未改。

### C2 `ensureBalanceCarryForwardProofTx`（同文件）

`visibilities` 为空且账号处于 pending 时，从最新一个「还能承载证明」的已发布 balances 周期派生
一张结转证明。**分支内的先后顺序是这一段最要紧的地方**：

1. 从新到旧挑第一个 `hasRealCheckpoint=false` 的候选周期；
2. 该周期若带 `hasStrandedCheckpoint`（XM-INV-DEAD-CONTAINMENT A2），返回
   `errBalanceCarryForwardProofPending` —— 死信 hold 赢，闲置派生不得绕过它；
3. 该账号若有 open 冻结，不派生（返回 nil）；
4. 派生。

第 2 步在第 3 步之前是刻意的：两个守卫都「不派生」，但后果不同。死信 hold 会让作业带
`BALANCE_PROOF_PENDING` 重排、`finalized_through` 不动，周期得以为那条待重投的检查点保留；
冻结守卫返回 nil，作业正常结束、`finalized_through` 推过该周期。用例
`TestIdlePendingDerivationWaitsForAStrandedCheckpointBeforeTheFreezeGuard` 就是靠这个差别把顺序
钉死的，变异 M5 把死信 hold 挪到冻结守卫之后即红。

证明的 INSERT + 一致性核对 + 审计抽成 `insertBalanceCarryForwardProofTx`，事实驱动与闲置两条
路径共用；闲置路径的审计 after 多一个 `idle_reevaluation: true`。`carryCandidate` 随之提到包级。

### C5 `signedExpectedUnits`（同文件）

新 helper，四处比较全部改走它：初判、防抖确认重建、边界规则、负向分支。`case −1` 的 detail
也改印有符号期望，三个数字自洽。评估行 `expected_service_units` 仍写无符号 `ExpectedBalance`
（列有 `>=0` CHECK）。

### C4 `enterPendingReconciliationTx`（同文件）

新增 `preserveStreak` 参数。负向分支按 `item.deficitText == nil` 传入；其余四个调用点传 `false`，
行为不变。为真且原状态已是 pending 时，连击列保持、且不写第二条 `entered`。原状态用
`SELECT ... FOR UPDATE` 在 UPDATE 之前读出来判断（`RowsAffected` 只能区分 frozen 与否，
`RETURNING (xmax=0)` 在 HOT update 下不可靠）。

### C3 `pending-reevaluate`

`backend/internal/postgresstore/pending_reevaluate_repair.go` +
`backend/cmd/eligibility-repair/main.go` 的常量、白名单、`--account` 必填校验、分派与打印。
SERIALIZABLE + 与 `processEligibilityProjectionJob` 同一把
`pg_advisory_xact_lock(hashtextextended($1,43))`。apply 只写一条作业行与一条审计。

排队语句抽成 `requeuePendingReevaluateJobTx`，它的 `WHERE eligibility_projection_jobs.status<>'dead'`
是第二层防线：报告的作业检查会先拒绝 dead，所以正常流程里这个子句永远轮不到它决定什么。
为了让它不是「没人验证过的第二道闸」，直接给它写了一条用例
（`TestPendingReevaluateRequeueStatementRefusesADeadRowOnItsOwn`）绕开检查层调用它，变异 M18
删掉子句即红。

## 3. 变异验证记录

每条断言都做了「改坏 → 实测红 → 还原」。还原后全量重跑绿。

| 变异 | 改动 | 预期红 | 实测 |
| --- | --- | --- | --- |
| M1 | C1 阈值 `>=$4` 改 `>$4` | 用例 a | 红（未排作业） |
| M2 | C1 阈值参数 `N−1` 改 `0` | 用例 b | 红（零连击也排了作业） |
| M3 | C2 open 冻结守卫短路 | 用例 g | 红（多派生一张证明） |
| M4 | C2 `hasRealCheckpoint` 跳过短路 | 用例 h | 红（0014 trigger 拒绝） |
| M5 | C2 死信 hold 挪到冻结守卫之后 | 顺序用例 | 红（processed=1，周期被烧） |
| M6 | 闲置审计不带 `idle_reevaluation` | 用例 a | 红（after_hash 不符） |
| M7 | 闲置分支整体短路 | 用例 a | 红（无证明） |
| M8 | 共用写入器丢掉复述的 deficit | 闲置用例 + `TestCarryForwardProofRestatesTheDeficit` | 两条同时红（证明共用一个写入器） |
| M9 | `signedExpectedUnits` 不再减 UnallocatedUnits | 四处比较对应的四条用例 | 四条全红 |
| M10 | 只回退初判 | 初判/边界/RC75 三条 | 三条红，负向分支用例绿 |
| M11 | 只回退边界规则 | 边界用例 | 只有它红 |
| M12 | 只回退确认重建 | 确认重建用例 | 只有它红（第一版用例区分不出来，已补 U'>0 的场景，见 §5） |
| M13 | 只回退负向分支 | 负向 deficit 用例 | 只有它红 |
| M14 | 删 `writeEvaluation` 的 `status=="matched"` 闸 | 计数用例 | 红 |
| M15 | C4 调用点强制 `preserveStreak=false` | 再进入用例 | 红（连击清零） |
| M16 | 去掉重复 `entered` 抑制 | 再进入用例 | 红（entered 变 2） |
| M17 | 连击无条件保留 | 「有量级且不符」对照用例 | 红（对照不是恒真） |
| M18 | 删排队语句的 `status<>'dead'` 子句 | 语句级用例 | 红（dead 被复活） |
| M19 | 自利守卫短路 | 自利用例 | 红 |
| M20 | `Blocked()` 看不见 blocker | 五条拒绝用例 | 全红 |
| M21 | 报告重算不再走共用 helper | dry-run 用例 | 红（期望 0 而非 −50） |
| M22 | 被拒绝的 apply 退回 dry-run 横幅 | 横幅格式用例 | 红 |

M18 的第一版（只跑 `TestPendingReevaluateRefusesADeadJob`）**是绿的**，因为检查层先拒绝、语句
根本没执行到。这属于「闸恰好没被触发」而不是「闸有效」，所以把语句抽成函数并单测它；记在这里
是因为下一个人重构这段时很容易再掉进去。

## 4. 门禁（实测 UTC 起止与耗时）

全部为 2026-09-09 的实测值，不是估计。

| 门禁 | 开始 (UTC) | 结束 (UTC) | 耗时 |
| --- | --- | --- | --- |
| `go vet ./...`（backend，最后一次） | 10:05:40 | 10:05:41 | 1s |
| `go test ./internal/postgresstore/`（① C1+C2 后） | 09:05:43 | 09:10:54 | 5m11s |
| `go test ./internal/postgresstore/`（② C5 后） | 09:15:36 | 09:20:59 | 5m23s |
| `go test ./internal/postgresstore/`（③ C4 后） | 09:42:01 | 09:47:36 | 5m35s |
| `go test ./cmd/eligibility-repair/`（④） | — | — | 19.1s |
| **`go test -p 1 -count=1 ./...`（backend 全量，四段合入后）** | **10:15:20** | **10:23:17** | **7m57s** |
| `go vet ./...` + `go test ./...`（agents 模块） | 10:23:31 | 10:23:36 | 5s |
| `pwsh -NoProfile -File scripts/check-no-secrets.ps1` | 10:23:36 | 10:23:37 | 1s，exit 0 |

全量里最重的一包是 `internal/postgresstore` 354.5s，其余各包合计约 90s。

集成测试用专用库 `invoice_test_pendrecon`
（`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_pendrecon?sslmode=disable`），
包列表放在 flag 之前，八个代理变量全部 `env -u`。

**没跑的门禁，以及原因：**

- `cd web && npm run typecheck && npm test -- --run`：本切片一个前端文件都没改
  （`git diff --name-only b3ded69 -- web/` 为空）。
- `scripts/verify.ps1` / `scripts/verify-postgres.ps1`：容器化的发布门禁，属于发布环节，
  由主控者在打 RC 时跑。
- **影子评估**：C5 属于评估器改动，按 XM-INV-ELIG-POLICY-START-ANCHOR 的纪律，
  `deploy/rehearsal/shadow-eval.sh --reproject-all --reevaluate-evidence` **发布前必做**，
  本切片没有跑（需要生产备份与 age 身份，不在实现范围内）。这不是可选项，见 §6 第 9 条。

顺带一条实现现场的坑：`gofmt -w internal/postgresstore/` 会把整包文件的 CRLF 改成 LF，
`git status` 于是显示 78 个文件被改。仓库 `core.autocrlf=true`，所以 `git diff` 对这些文件是
空的、提交内容不受影响，但工作树会脏一大片。已用 `git checkout -- backend/internal/postgresstore/`
还原；下次只对自己动过的文件跑 `gofmt -w`。

## 5. 偏离设计稿的地方（都是实现当下发现、当下记的）

1. **专用库名**：设计 §5.1 A13 写 `invoice_test_pendingrecon`，派工写 `invoice_test_pendrecon`。
   按派工执行。
2. **C5 让 XM-INV-BLIP-SOFTFAIL 的复现形状消失了**，设计稿没有预见这一条。签名之后
   `E − U` 就是 `credits + cash − usage` 的精确记账，合成一笔恰等于差值的额度必然把重建对平到
   0 —— 只要那笔额度真的写进去了。RC75 那个形状（合成额度落在造成欠账的用量之前）因此现在
   干净自愈，两条既有 softfail 用例的旧期望其实编码的是那个下限 bug。处置：
   - `TestBalanceBlipConfirmationThatDoesNotReconcileIsSoftfailedNotErrored` 改名为
     `...OnAFlooredLedgerReconcilesUnderTheSignedComparison`，首要断言仍是「绝不返回错误」，
     结果改成自愈，注释写明旧期望是什么、为什么变；
   - 新增 `TestBalanceBlipConfirmationThatCannotSynthesiseIsSoftfailedNotErrored`，用「合成被
     `ON CONFLICT DO NOTHING` 挡住」这条仍然可达的路径覆盖 softfail 分支本身；
   - 上限升级用例改挂同一条路径，升级逻辑一字未改。
   **softfail 与 `balanceBlipRebaselineCap` 的代码一律没动**，它们仍是兜底。但要如实说：签名口径
   之后，除了「合成写不进去」这一条，我没能找到别的可达路径。是否就此把它们判为 dead code，
   是主控者/验收线的决定，不是我的。
3. **C3 报告第 6 项的重算时刻**：设计写 `buildEligibilityProjectionTx(account, prior.as_of)`，
   实现用的是目标周期的天花板（= 派生出来的证明自己的 `as_of`，也是评估器实际会用的时刻）。
   闲置账号两者结果相同；非闲置时天花板更准。
4. **C3 报告多了一条 `in requeue window`**：设计的报告第 5 项说「最新 published balances 周期
   （ceiling ≥ finalized_through）」，但本工具排的作业窗口是 `requested_through = finalized_through`，
   够得着的只有 ceiling **等于** finalized_through 的那一个。不写这条，报告会显示一个工具其实
   到不了的周期，操作者 apply 之后看不到任何变化却以为工具坏了。加了这一行并在运行手册里点名。
5. **CLI 输出语言**：横幅与定宽表沿用同仓库其它 kind 的英文（设计 §3 C3 明确要求「同
   `main.go:449-472` 风格」），检查项的解释文字用中文。
6. **`TestRunApplyWithoutOperatorIDIsRejected` 的枚举**：设计 A12 说补到 9 种。实际改成
   per-kind 带 filters 的表，给 `ingest-acknowledge-unreplayable` 和 `pending-reevaluate` 各自
   补上它们必填的窄化参数 —— 否则这两种会因为「缺 `--event`/`--account`」而先报错，用例看着绿
   但根本没测到 operator-id 那道闸。
7. **被拒绝的 apply 单独一个横幅**（设计没写，是实现时发现的输出正确性问题）。兄弟 kind 的
   横幅只有 `DRY RUN (nothing was changed)` 与 `APPLIED` 两种，而本 kind 会拒绝 apply；沿用
   两种横幅的话，操作者打了 `--apply` 却看到 `DRY RUN`，最合理的推断是「参数没生效、再打一遍」，
   而不是去读 STOP 行。所以结果里加了 `ApplyRequested`，横幅第三种为
   `REFUSED (--apply was requested; a check below said STOP, nothing was changed)`。
   变异 M22 把它退回 dry-run 横幅即红。

## 6. 设计稿 §8 未证实前提的处置

**已在代码里证实：**

- 第 7 条（`audit_events.action` 是否有 CHECK 枚举）：**没有**。`0001_init.sql:178` 是裸
  `TEXT NOT NULL`，全库对 `eligibility_freezes` 之外没有针对 action 的 CHECK
  （`pg_constraint` 实测）。新 action 名不需要迁移。`auditActionPattern` 只约束
  `RecordAdminOperationalAudit`/`RecordMaintenanceAudit` 两个导出入口，`writeAudit` 不过它。
- 第 14 条（`coveredVisibility` 的语义）：确认它初始化为 `account.FinalizedThrough`，并且闲置
  分支必须放在合并循环之外 —— `visibilities` 为空时循环根本不进，所以闲置分支既没有「绕过
  `After` 判断」的问题，也必须自己重做死信检查（这就是 §2 里那个顺序的由来）。
- `sourceEventContainedByOpenFreezeSQL` 与 C2 的候选查询同源渲染，C3 报告里的死信判断复用同一
  个片段，两处不会漂开。

**仍需主控者在生产核实（属于生产数据类，我不猜）：**

1. 生产 balances 周期发布节奏与「每 20 分钟空推进」的驱动（决定用户 12 多久能自动退出）。
2. 用户 34 当前是否仍有 `status='dead'` 的作业行；若是，先跑 `--kind=projection-requeue-dead`。
3. 用户 34 在 09-06 17:15–17:38 的负余额评估为何不 matched（残差 D 的大小），以及 C5 的合成量
   到底会是多少。
4. 用户 34 的 09-02 四条 UNKNOWN_POSITIVE 各自走的路径（影响后续 admin-credits 切片的去重）。
5. 用户 12 在 09-06 之后是否有停放/死信的余额 ingest 事件 —— 若有，C2 的死信 hold 会让它的作业
   进入 `BALANCE_PROOF_PENDING` 重排而不是派生，这是对的，但要先处理那条死信。
6. 0026 由哪个 RC 上线、`install-economic` 的执行时间（12 的 09-06 11:29 检查点归因）。
9. 生产中还有多少 `UnallocatedUnits>0` 且上游余额为正的账号 —— C5 会改判它们，**发布前的影子
   评估必须逐账号 diff**（评估器改动纪律，本切片没有跑影子评估，那是发布环节的事）。
15. 用户 34 加余额的上游操作到底走的哪个入口。

第 8、10、11、12、13 条与本切片的实现无关，未处置。

## 7. 部署后预期观测

- **用户 12（C1+C2）**：下一次 balances 发布且 `requested_through` 越过新周期天花板后，依次出现
  `eligibility.balance_carry_forward.derived{idle_reevaluation:true}` →
  `eligibility.balance_carry_forward.evaluated{status:matched}` →
  `eligibility.pending_reconciliation.exited{consecutive_matches:2}`，状态转 `active`。
  若久未退出，先跑 `--kind=pending-reevaluate --account=<12>` 的 dry-run 看报告，
  特别是「目标周期」与 `in requeue window` 两行，再决定要不要 apply。
- **用户 34（C5）**：接下来两张真实检查点。若出现 `eligibility.balance_blip.rebaselined`，
  说明重建没有对平 —— 按 §5 第 2 条，签名口径之后这只可能是「合成额度写不进去」，
  停下来看 detail，不要用工具清状态。
- **readyz**：`Queued` 偶发 1–2、秒级清零；不应出现 `eligibility_projection_stuck`。
  `docs/ELIGIBILITY-OPERATIONS.md` 已经把「pending 对 readyz 结构上不可见」这句改掉了。
- **回滚**：重新部署上一版镜像即可（无迁移）。新代码写下的证明/评估/退出/合成额度在旧代码下
  都是合法行；副作用是 12 不再自动重评（已退出者保持 active），34 会被旧的无符号口径重新判回
  pending。
