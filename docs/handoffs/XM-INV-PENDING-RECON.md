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
| 追加 | 交接单补门禁实测时间 | `62b280f` |
| 追加 | 被拒绝 apply 的横幅 | `1e08a76` |
| 追加 | **第一轮复审修复**（重评窗口、报告同源、STOP 表同源、手册 SQL 与路径） | `41e65d8` |

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
| D8 | (a) C2 允许包含下界（ceiling == finalized_through） | 候选查询是 `scan_ceiling_at>=finalized_through`，下界包含。**注意**：第一轮复审后 `pending-reevaluate` 排的窗口不再是 `finalized_through`（那让派生在生产不可达，见 §5 第 8 条），所以本决策对该工具已不再是「够不够得着」的关键；它仍决定 C1 排队后 worker 的候选下界 |
| D9 | (a) 部署后先等 C1 自动处理 | 运行手册写明：报告的 `newest published` 高于 `requeue window` 时，最新周期还压在 finalization_delay 里，自动路径本来就会取到它 |

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
| M23 | 排队窗口退回 `finalized_through`（生产不可达那版） | 生产形状用例 | 红（预测/实写一致性守卫先报出来） |
| M24 | 报告忽略既有作业行更宽的窗口 | 既有作业行用例 | 红（报告说 finalized_through，实际是更宽的） |
| M25 | 目标周期不再受窗口约束 | 生产形状用例 | 红（选到窗口外的周期） |
| M26 | 预测/实写一致性守卫短路 **+** 同时把写入退回窄窗口 | 生产形状用例 | 红（作业行 requested_through 断言） |
| M27 | 加一个没有手册行的 blocker | 手册同源用例 | 红（11 行 vs 12 个 blocker） |

M26 单独短路守卫是**绿**的：正常路径下预测与实写永远相等，守卫不决定任何事。它的价值是把
漂移变成一条清楚的错误信息而不是一次静默的错误提交；真正钉住这条性质的是用例里对作业行
`requested_through` 的断言（配对变异 M26 即红）。记在这里是因为「单独短路是绿的」这件事本身
容易被下一个人误读成「守卫没用」。

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
- **影子评估**：C5 属于评估器改动，发布前必做；本切片没有跑（需要生产备份与 age 身份，
  不在实现范围内）。**怎么跑、怎么判见 §4.1，照那节执行。**

### 4.1 RC108 影子评估：命令、预期 diff、判据

第一轮复审指出的三件事都成立，先摆出来，因为它们决定了这一节为什么是这个样子：

1. `--reproject-all` 单独对 C5 是空转。评估器只挑 `NOT EXISTS(evaluation)` 的证据，生产快照
   里每条检查点都已经有评估行，所以只重放投影不会让新分类器判任何东西。
2. `--reevaluate-evidence` 才会清掉评估行让新分类器重判，但手册对它的既有判据是「重做出来的
   状态必须与生产一致」，而 C5 的全部目的就是改变其中一部分状态。照那条判据判，本次必然
   「不一致」。所以**本次的判据不是「一致」，是「变化恰好等于下面的清单」**。
3. C1/C2 的闲置派生需要窗口。冻结副本上没有任何东西发布水位，`--reproject-all` 只按各账号
   自己的 `finalized_through` 排作业（窗口宽度为零），派生不了。要 `--finalization-window`
   才会按「一次 finalize 会请求的窗口」排队。

命令（服务器上，镜像 load 之后、`roll-forward.sh` 之前）：

```bash
BACKUP_DIR=/root/invoice-system/backups \
BACKUP_ALLOWED_SIGNERS_FILE=/root/invoice-system/config/backup-allowed-signers \
AGE_IDENTITY_FILE=/dev/shm/rbk/backup-age-identity.txt \
  bash deploy/rehearsal/shadow-eval.sh --image-tag 0.1.0-rc108 \
    --reproject-all \
    --reevaluate-evidence \
    --finalization-window --finalization-window-lag 1h --finalization-window-provable
```

每个参数为什么在这里：

| 参数 | 理由 |
| --- | --- |
| `--reproject-all` | 另外两个开关都要求它；也是「这次真的跑过账号」的前提（`AccountsProjected==0` 直接判 not_ready） |
| `--reevaluate-evidence` | 唯一能让 C5 的新分类器重判既有检查点的开关；不给它，C5 在影子上根本没被执行 |
| `--finalization-window` | 给 C1/C2 一个非零窗口；不给它，闲置派生一次都不会发生 |
| `--finalization-window-lag 1h` | 冻结副本上贴着前沿的事实其水位晚于窗口末端，结转证明在那里永远 pend（生产靠下一次更宽的窗口收敛）；1h 是脚本注释自己给的经验值 |
| `--finalization-window-provable` | 把每个窗口砍到「窗口内每条事实都已被看见」的那个已发布 balances 天花板，正是 `ensureBalanceCarryForwardProofTx` 需要的 |

**判据一（自动）**：`verdict=ready`、退出码 0。但要知道它只检查三件事
（`eligibility-shadow/report.go` 的 `EvaluateReadiness`）：跑过账号、没有**新出现的冻结原因
类别**、没有投影错误。它**不**比较 `eligibility_status`，所以 pending→active 不会让它变红；
它也**不**会发现「SOURCE_GAP 落到了一个原本没有 SOURCE_GAP 的账号上」，只要这个类别在基线里
已经存在。自动判据是必要条件，不是充分条件。

**判据二（人工，逐账号 diff）**：生产共 12 个账号，基线 active 9、frozen 1（2222）、
`not_invoiceable_pending_reconciliation` 2（用户 12、34）。看 `after.accounts[]`，
**必须恰好只有下面这些变化**：

| 账号 | 预期 | 依据 |
| --- | --- | --- |
| 用户 12 `acdcdce9-c7f4-4cb4-9a02-ce527849a440` | 出现一条 `idle_reevaluation:true` 的结转证明，其评估 `matched`，连击 1→2，状态 → `active` | C1+C2；它 09-06 11:29:30Z 之后无检查点，连击停在 1 |
| 用户 34 `40bd883d-26fa-4938-b8c8-0f51c8b88686` | 正向检查点在有符号口径下改判：合成一条 `UNKNOWN_POSITIVE`（量级 ≈ 09-06 17:18 那笔加款 + 当时 deficit），随后 `matched`；状态 → `active`，或至少连击 ≥1 | C5 |
| 其余 9 个 active | 状态仍 `active`，不新增 open 冻结 | C5 对 `UnallocatedUnits=0` 的账号逐字节不变；有欠账的账号只会被判得更宽松，不会更严 |
| 2222（frozen） | 仍 `frozen`，冻结原因不变 | 本切片不碰冻结路径 |

**判据三**：清单之外的任何变化都是 not ready——某个 active 账号进了 pending 或 frozen、某个
账号多出 `SOURCE_GAP`、`eligibility.balance_blip.rebaselined` 出现在用户 34 以外的账号上、
合成额度出现在预期之外的账号上——停下来查，不要发布。

两个已知的影子专有伪象，看到不要当回归：

- `--reevaluate-evidence` 是否同时删除副本上已合成的 `UNKNOWN_POSITIVE` 额度，我没有证实
  （设计 §8 第 8 条）。若不删，用户 34 在影子上会出现「旧合成 + 新合成」两条，生产不会。
  跑之前先在副本上数一次 `source_credit_events WHERE credit_kind='UNKNOWN_POSITIVE'`，跑完
  再数，差值对不上就是这个伪象。
- 快照不带 `pending_reconciliation_consecutive_matches`（`eligibility_shadow_report.go`），
  连击要另外用 psql 在副本上读。

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
4. ~~**C3 报告多了一条 `in requeue window`**~~ —— **本条已被下面第 8 条取代，不要再照它读。**
   当时的想法是：设计的报告第 5 项说「最新 published balances 周期（ceiling ≥
   finalized_through）」，而工具排的窗口只够得着 ceiling 等于 finalized_through 的那一个，
   所以加一行 `in requeue window` 提示操作者「这一个到不了，等 finalize」。第一轮复审证明
   前提本身就是错的：窗口不该是 `finalized_through`。该字段与该行已删除，现在报告印的是
   `requeue window`（apply 实际会写进作业行的那个窗口）。
5. **CLI 输出语言**：横幅与定宽表沿用同仓库其它 kind 的英文（设计 §3 C3 明确要求「同
   `main.go:449-472` 风格」），检查项的解释文字用中文。
6. **`TestRunApplyWithoutOperatorIDIsRejected` 的枚举**：设计 A12 说补到 9 种。实际改成
   per-kind 带 filters 的表，给 `ingest-acknowledge-unreplayable` 和 `pending-reevaluate` 各自
   补上它们必填的窄化参数 —— 否则这两种会因为「缺 `--event`/`--account`」而先报错，用例看着绿
   但根本没测到 operator-id 那道闸。
8. **C3 的排队窗口重做**（第一轮复审 major 1+2，最要紧的一条）。原实现把
   `requested_through` 写成 `finalized_through`，报告也按 `finalized_through` 算窗口。两个后果：
   - 生产上 `finalized_through = min(四条流水位) − finalization_delay_seconds`（秒级、每次
     finalize 都在动），**不可能**正好落在某个周期天花板上，所以派生窗口宽度为零，apply 的
     派生路径在生产**不可达**：横幅 APPLIED、accounts affected: 1、审计照落、报告印着预期
     matched，而证明数、状态、连击一个都没动。唯一证明它「能派生」的用例是手工把
     `finalized_through` 写到天花板上的——**用例造出了生产不存在的形状**。
   - `ON CONFLICT DO UPDATE` 不写 `requested_through`，已有作业行保留它更宽的窗口。于是报告
     说「applying now changes nothing」，apply 之后 worker 用旧的宽窗口派生、评估 matched、
     账号退出「对账中」变 active——被告知「不会有变化」的操作把一个账号变成可开票。
   现在：排队按 **finalize 自己的口径**写窗口（`GREATEST(finalized_through, cutover_at,
   min(四条水位) − 延迟)`，四条水位齐了才算），既有行取 `GREATEST` 只升不降；报告用**同一个
   SQL 片段**（`pendingReevaluateWindowSQL`）预测同一个值；目标周期改成「窗口内、按
   `ensureBalanceCarryForwardProofTx` 自己的规则（从新往旧、跳过已带真实检查点的）」选。
   apply 还会把实写值与预测值比一次，不等就拒绝提交——报告描述的操作和实际执行的操作必须是
   同一个。用例换成生产形状：两个已发布周期、`finalized_through` 在两者之下、窗口落在两者
   之间，谁都不在边界上。
9. **报告新增 `requeue window` / `newest published` / `skipped` 三行**，并删掉会误导的
   `in requeue window`。窗口行排在周期行之前是刻意的：周期只有「在这个窗口里」才有意义。
10. **STOP 表与代码同源**（复审 minor 7）。原表 8 行、代码 10 个 blocker，漏了「没有资格状态
    行」和「窗口之后没有已发布周期」——后者恰是生产常态。现在每个 blocker 有一个 ASCII
    `Code`，`PendingReevaluateBlockerCodes` 是唯一清单，`TestPendingReevaluateBlockersMatchThe
    RunbookTable` 读手册数表格行、与清单长度比对（找不到表头就 fail，不会 0==0 恒真）。
11. **手册补两段只读 SQL**（复审 major 6）：按上游 id 查 `external_accounts.id`、查资格状态四列
    确认账号有没有离开「对账中」，并点名**别**去查
    `balance_reconciliation_checkpoints.reconciliation_status`（生产上全是
    `pending_finalization`），评估结果在两张 evaluations 表里。同时指向管理端
    `GET /api/v1/admin/accounts`。
12. **ELIGIBILITY-OPERATIONS.md 里我新增的可粘贴命令改成手册的 `docker run` 形状**（复审
    major 3）。原来抄了同文件既有片段的 `/app/bin/invoice-eligibility-repair`、
    `/run/secrets/invoice-db-url`、`field-keyring.json`——三个路径都不存在。**同文件里兄弟
    kind 的既有片段仍是错的**（我只改了自己新增的那两段，另加一段提示指向手册）：那是既有
    问题，是否一并修由主控者决定。
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

**主控者已在生产只读核实（2026-09-09 11:10Z），结论抄录如下：**

1. balances 周期每个源约 **1 分钟**发布一次（10:36–10:39 四分钟内 8 个 published 周期），
   空推进由 finalize 驱动。→ 设计稿「每 20 分钟」的估计偏慢，用户 12 在 C1 下应该在分钟级
   而不是 40 分钟内拿到闲置证据。**这条数字直接推翻了 C3 的原实现**，见 §5 第 8 条。
2. 用户 34 的投影作业是 `queued`（attempt 2），不是 dead；2222 是 `queued`（attempt 1）；
   全库**没有** dead 作业。→ **不必**先跑 `--kind=projection-requeue-dead`。
3. 用户 34（`40bd883d-26fa-4938-b8c8-0f51c8b88686`）时间线：09-06 17:14:30 余额 35,951,468
   → 17:15:32 余额 0 负、deficit 25,900,972 → 17:16:40 deficit 51,228,592 → 17:18:48 余额
   99,948,771,408（管理员加约 1000 元当量）→ 之后每分钟一张检查点持续消费，09-09 10:39Z
   余额 84,084,681,007。pending 自 09-06 17:38:06Z，reason `UNKNOWN_NEGATIVE_BALANCE`，
   连击 0。→ **C5 的合成量 ≈ 加款额 + 当时 deficit**，不是设计稿推测的 999.49+D。
4. 用户 34 的 09-02 四条 UNKNOWN_POSITIVE 各自路径：仍未核（留给 admin-credits 切片的去重）。
5. 用户 12（`acdcdce9-c7f4-4cb4-9a02-ce527849a440`）：最后一张真实检查点 09-06 11:29:30Z
   （余额 0 负、deficit 3,610,140），此后无检查点；pending 自 09-04 10:03:06Z，连击 1。
   全库 `source_ingest_events` **无 failed/dead**。→ 没有死信 hold 要先处理，C2 的闲置派生
   对它是畅通的。
6. 0026 于 09-06 11:28:57Z 打上，比 12 的最后一张检查点早 33 秒——所以那张带 deficit；
   0032 于 09-09 08:26:54Z。
9. 近似：每账号最新检查点余额为正的 8 个、非正 4 个；全库资格状态 active 9、frozen 1（2222）、
   pending 2（12、34）。真值仍要靠影子评估逐账号 diff，判据见 §4.1。
15. 用户 34 的加款走 Sub2API 管理员加余额（`redeem_codes` type=`admin_balance`），已核。

另外一条与手册相关：`balance_reconciliation_checkpoints.reconciliation_status` 生产上**全是**
`pending_finalization`，评估结果不在这张表。手册的确认 SQL 已改为查
`balance_checkpoint_evaluations` / `balance_carry_forward_evaluations`。

**仍未核实：** 第 4 条（34 的四条旧合成额度各走哪条路径）；第 8、10、11、12、13 条与本切片的
实现无关，未处置。

## 7. 部署后预期观测

- **用户 12（C1+C2）**：下一次 balances 发布且 `requested_through` 越过新周期天花板后，依次出现
  `eligibility.balance_carry_forward.derived{idle_reevaluation:true}` →
  `eligibility.balance_carry_forward.evaluated{status:matched}` →
  `eligibility.pending_reconciliation.exited{consecutive_matches:2}`，状态转 `active`。
  若久未退出，先跑 `--kind=pending-reevaluate --account=<12>` 的 dry-run 看报告，
  特别是 `requeue window`、`target cycle`、`newest published` 三行，再决定要不要 apply。
- **用户 34（C5）**：接下来两张真实检查点。若出现 `eligibility.balance_blip.rebaselined`，
  说明重建没有对平 —— 按 §5 第 2 条，签名口径之后这只可能是「合成额度写不进去」，
  停下来看 detail，不要用工具清状态。
- **readyz**：`Queued` 偶发 1–2、秒级清零；不应出现 `eligibility_projection_stuck`。
  `docs/ELIGIBILITY-OPERATIONS.md` 已经把「pending 对 readyz 结构上不可见」这句改掉了。
- **回滚**：重新部署上一版镜像即可（无迁移）。新代码写下的证明/评估/退出/合成额度在旧代码下
  都是合法行；副作用是 12 不再自动重评（已退出者保持 active），34 会被旧的无符号口径重新判回
  pending。
