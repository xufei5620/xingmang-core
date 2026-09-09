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
| M28 | 去掉「有未评估真实检查点就不派生」守卫 | B1 两条用例 | 红（一次观测放行了账号） |
| M29 | 去掉派生自身的连击门槛 | M3 用例 | 红（连击 0 也派生） |
| M30 | 派生回退到更旧的周期（旧 continue） | 直调派生的用例 | 红（回填到真实检查点之下） |
| M31 | 账本文案退回「预期 %s」 | 文案用例 | 红 |
| M32 | 被拒绝的 apply 不再返回哨兵错误 | CLI 退出码用例 | 红 |
| M33 | 报告的连击闸从 STOP 降成 NOTE | 连击门槛用例 | 红 |
| M34 | 未评估计数去掉锚前下界 | 锚前用例 | 红（owed=1，闲置派生被永久关死） |
| M35 | 未评估计数改用窗口上界 | 「最新证据还不可终局」用例 | 红（账号被放出去） |
| M36 | 加一个既不进清单也不进手册的 blocker | 发现式同源用例 | 红 |
| M37 | softfail 回滚去掉 `inserted` 守卫（删不属于自己的额度） | FK 复现用例 | 红，**报出与影子评估一模一样的错误串**（SQLSTATE 23001） |
| M38 | 派生候选查询去掉窗口上界 | 零宽窗口用例 | 红（零宽也派生了） |

M26 单独短路守卫是**绿**的：正常路径下预测与实写永远相等，守卫不决定任何事。它的价值是把
漂移变成一条清楚的错误信息而不是一次静默的错误提交；真正钉住这条性质的是用例里对作业行
`requested_through` 的断言（配对变异 M26 即红）。记在这里是因为「单独短路是绿的」这件事本身
容易被下一个人误读成「守卫没用」。

M30 的第一版走公共路径**是绿的**：B1 的守卫先拦住，B2 根本轮不到。而 B1 之后 B2 也确实没有
可达场景了——真实检查点一旦被评估，账号要么 matched 退出、要么非 matched 把连击清零，两条路
都不会再进闲置派生。所以 B2 是一道**今天不可达**的第二层防线，按与 `status<>'dead'` 同样的办法
处理：直接构造候选列表调用 `deriveIdlePendingCarryForwardProofTx`，让它自己有一条用例（并配
「最新候选可用则确实派生」的对照）。

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
| **第一轮复审修复后** `go vet ./...` | 11:51:42 | 11:51:42 | <1s |
| **第一轮复审修复后** `go test -p 1 -count=1 ./...`（backend 全量） | **11:51:42** | **11:59:34** | **7m52s，exit 0，30 包全 ok** |
| **第一轮复审修复后** agents 模块 vet + test | 11:59:56 | 11:59:59 | 3s |
| **第一轮复审修复后** `check-no-secrets.ps1` | 11:59:59 | 12:00:00 | 1s，exit 0 |
| **交付前最后一次** `go vet ./...` | 12:05:35 | 12:05:36 | 1s |
| **交付前最后一次** `go test -p 1 -count=1 ./...`（backend 全量） | **12:05:36** | **12:13:12** | **7m36s，exit 0，30 包全 ok** |
| **交付前最后一次** `check-no-secrets.ps1` | 12:13:12 | 12:13:12 | exit 0 |
| **终审 major 1 修复后** `go vet ./...` | 12:36:31 | 12:36:32 | 1s |
| **终审 major 1 修复后** `go test -p 1 -count=1 ./...`（backend 全量） | **12:36:32** | **12:44:09** | **7m37s，exit 0，30 包全 ok** |
| **终审 major 1 修复后** `check-no-secrets.ps1` | 12:44:09 | 12:44:10 | exit 0 |
| **终审 major 2 + 3 minor 修复后** `go vet ./...` | 12:56:10 | 12:56:12 | 2s |
| **终审 major 2 + 3 minor 修复后** `go test -p 1 -count=1 ./...`（backend 全量） | **12:56:12** | **13:03:33** | **7m21s，exit 0，30 包全 ok** |
| **终审 major 2 + 3 minor 修复后** `check-no-secrets.ps1` | 13:03:33 | 13:03:34 | exit 0 |
| **RC108 影子复盘修复后** `go vet ./...` | 14:08:42 | 14:08:43 | 1s |
| **RC108 影子复盘修复后** `go test -p 1 -count=1 ./...`（backend 全量） | **14:08:43** | **14:17:37** | **8m54s，exit 0，30 包全 ok** |
| **RC108 影子复盘修复后** `check-no-secrets.ps1` | 14:17:37 | 14:17:38 | exit 0 |

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
    --finalization-window --finalization-window-provable
```

> **不要加 `--finalization-window-lag 1h`。** 上一版本节推荐过它，RC108 的影子评估就是
> 因此判 not_ready 的——这是我写错的，改正见 §8.7。lag 是给「落后好几天」的追赶窗口用的；
> 12 与 34 的 finalized_through 只落后水位约 20 分钟，减去 15 分钟延迟再减 1 小时就落到
> finalized_through 之下，`GREATEST` 把窗口压成零宽，什么也派生不了。

每个参数为什么在这里：

| 参数 | 理由 |
| --- | --- |
| `--reproject-all` | 另外两个开关都要求它；也是「这次真的跑过账号」的前提（`AccountsProjected==0` 直接判 not_ready） |
| `--reevaluate-evidence` | 唯一能让 C5 的新分类器重判既有检查点的开关；不给它，C5 在影子上根本没被执行 |
| `--finalization-window` | 给 C1/C2 一个非零窗口；不给它，闲置派生一次都不会发生 |
| ~~`--finalization-window-lag 1h`~~ | **删掉**。见 §8.7：对落后仅 20 分钟的账号，1h 的 lag 直接把窗口压成零宽。只有当账号落后天级（追赶窗口）时才考虑它，且取值必须小于「水位 − 延迟 − finalized_through」 |
| `--finalization-window-provable` | 把每个窗口砍到「窗口内每条事实都已被看见」的那个已发布 balances 天花板，正是 `ensureBalanceCarryForwardProofTx` 需要的 |

**判据一（自动，必要不充分）**：`verdict=ready`、退出码 0。但要知道它只检查三件事
（`eligibility-shadow/report.go` 的 `EvaluateReadiness`）：跑过账号、没有**新出现的冻结原因
类别**、没有投影错误。它**不**比较 `eligibility_status`，所以 pending→active 不会让它变红；
它也**不**会发现「SOURCE_GAP 落到了一个原本没有 SOURCE_GAP 的账号上」，只要这个类别在基线里
已经存在。自动判据是必要条件，不是充分条件。

**判据二（人工，逐账号 diff —— 这才是通过判据）**：verdict 只比较冻结原因类别，评估状态的
变化它根本看不见，所以通过与否由这一步决定。对 `shadow-eval.json` 逐账号列出 before/after 的
`eligibility_status`、连击、证明数，每一处差异都要能由 `signedExpectedUnits` 或闲置派生解释
并被评审。生产共 12 个账号，基线 active 9、frozen 1（2222）、
`not_invoiceable_pending_reconciliation` 2（用户 12、34）。看 `after.accounts[]`，
**必须恰好只有下面这些变化**：

| 账号 | 预期 | 依据 |
| --- | --- | --- |
| 用户 12 `acdcdce9-c7f4-4cb4-9a02-ce527849a440` | 出现一条 `idle_reevaluation:true` 的结转证明，其评估 `matched`，连击 1→2，状态 → `active` | C1+C2；它 09-06 11:29:30Z 之后无检查点，连击停在 1 |
| 用户 34 `40bd883d-26fa-4938-b8c8-0f51c8b88686` | 正向检查点在有符号口径下改判：合成一条 `UNKNOWN_POSITIVE`（量级 ≈ 09-06 17:18 那笔加款 + 当时 deficit），随后 `matched`；状态 → `active`，或至少连击 ≥1 | C5 |
| 其余 9 个 active | 状态仍 `active`，不新增 open 冻结 | C5 对 `UnallocatedUnits=0` 的账号逐字节不变；有欠账的账号只会被判得更宽松，不会更严 |
| 2222（frozen） | 仍 `frozen`，冻结原因不变 | 本切片不碰冻结路径 |

预期会变的账号要在跑之前先列出来，不要跑完再补。用户 12、34 已点名；此外任何
`UnallocatedUnits>0` 且上游最新余额为正的账号都可能被 C5 改判，清单从副本上取：

```bash
# 在影子副本上跑（不是生产）：非现金/现金池余额、未分配用量、最新一张检查点的余额
docker exec -i <shadow-postgres> psql -X -v ON_ERROR_STOP=1 -U invoice_owner -d invoice -At -F '|' -c \
"SELECT eas.external_account_id, eas.eligibility_status, eas.non_invoiceable_overage_units,
        c.balance_service_units, c.balance_negative, c.as_of
 FROM source_account_eligibility_state eas
 LEFT JOIN LATERAL (
   SELECT balance_service_units, balance_negative, as_of
   FROM balance_reconciliation_checkpoints
   WHERE external_account_id=eas.external_account_id AND checkpoint_kind='reconciliation'
   ORDER BY as_of DESC LIMIT 1) c ON true
 WHERE COALESCE(eas.non_invoiceable_overage_units,0) > 0
 ORDER BY eas.external_account_id"
```

`non_invoiceable_overage_units` 是账号行上记着的未分配用量（`recordUsageOverageTx` 写的），它
大于 0 且最新检查点余额为正的账号，就是 C5 会改判的那一批。跑之前把这个清单贴进发布记录，
跑完逐个对照。

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
4. 用户 34 的合成额度（主控者 2026-09-09 12:45Z 只读核实）——**不是设计稿说的「09-02 四条」，
   而是 09-01 三条 + 09-02 一条**：
   | event_time | 单位 | ≈ 元当量 | created_at |
   | --- | ---: | ---: | --- |
   | 09-01 14:51:09 | 10,144,440,689 | 101.44 | 09-01 21:10:52 |
   | 09-01 15:05:17 | 10,105,221,407 | 101.05 | 09-01 21:10:52 |
   | 09-01 15:16:17 | 39,219,282 | 0.39 | 09-01 21:10:52 |
   | 09-02 18:27:18 | 30,000,000,000 | 300.00（管理员加款） | 09-02 18:54:45 |

   前三条 `created_at` 相同，是 identity_catchup 于 09-01 14:49 之后首轮重建的同一次
   `projection.rebuilt`。四条都是 `external_event_id` 前缀 `unknown-positive:`、无
   `causal_domain`、无 funding_lot；作为对照，PRE_POLICY 额度带
   `causal_domain=payment_orders`。

   **09-06 17:18 那次管理员加约 1000 没有对应的合成行**——账号在 17:38 直接进了 pending
   （连击 0）。这与根因分析一致：正向分支被延后、永不确认，所以什么也没合成。

   参考（用户 12）：08-24 切点 UNKNOWN_POSITIVE 1,998,791,280（created 09-03 09:19）、
   四条 PRE_POLICY（payment_orders）、08-31 16:00 policy-start UNKNOWN_POSITIVE 2,994,240
   （created 09-03 11:58）。
5. 用户 12（`acdcdce9-c7f4-4cb4-9a02-ce527849a440`）：最后一张真实检查点 09-06 11:29:30Z
   （余额 0 负、deficit 3,610,140），此后无检查点；pending 自 09-04 10:03:06Z，连击 1。
   全库 `source_ingest_events` **无 failed/dead**。→ 没有死信 hold 要先处理，C2 的闲置派生
   对它是畅通的。
6. 0026 于 09-06 11:28:57Z 打上，比 12 的最后一张检查点早 33 秒——所以那张带 deficit；
   0032 于 09-09 08:26:54Z。
9. 近似：每账号最新检查点余额为正的 8 个、非正 4 个；全库资格状态 active 9、frozen 1（2222）、
   pending 2（12、34）。真值仍要靠影子评估逐账号 diff，判据见 §4.1。
15. 用户 34 的加款走 Sub2API 管理员加余额（`redeem_codes` type=`admin_balance`），已核。

**主控者 2026-09-09 12:35Z 的第二批生产只读实测（机器时钟）：**

- 12 个账号**全部** `bootstrap_kind=POLICY_ANCHOR`；每个账号都恰有一张没有评估行的
  `checkpoint_kind='cutover'` 检查点，as_of 2026-08-24 23:49:51Z（切点），在各自 cutover_at 之前。
  （注意：本切片的未评估计数带 `checkpoint_kind='reconciliation'` 过滤，这张不在计数内——
  「全员被关死」的推论不成立，见 §8.5。）
- 用户 12（cutover_at 08-31 16:00Z）：锚后 241 matched（最后一张 09-06 11:29:30Z 判 matched）、
  416 negative_frozen、1 positive_classified_non_cash；锚前 212 positive_blip_ignored、
  2 negative_frozen、1 positive_classified_non_cash——**这些锚前检查点是有评估行的**（锚迁移之前
  评估的）；无评估行的只有那张 cutover。
- 用户 34（cutover_at 08-31 17:20:03Z）：锚前 1027 matched + 1 张 cutover 无评估；锚后 2407
  matched、2195 positive_blip_ignored、2 negative_frozen、5 positive_classified_non_cash，另有
  3 张 12:31–12:34Z 的最新检查点无评估行（正向延后中，属预期）。
- 账号 1113/1147/2092/2222 还有更多锚前无评估行的检查点（1147 有 2、2092 有 5、2222 有 23）；
  **它们的 `checkpoint_kind` 待确认**，这决定本切片的下界修复在今天是否真被触发（§8.5）。

另外一条与手册相关：`balance_reconciliation_checkpoints.reconciliation_status` 生产上**全是**
`pending_finalization`，评估结果不在这张表。手册的确认 SQL 已改为查
`balance_checkpoint_evaluations` / `balance_carry_forward_evaluations`。

**仍未核实：** 第 8、10、11、12、13 条与本切片的实现无关，未处置。

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
- **两条路径（主控者 2026-09-09 复审给出，已核对代码成立）**：
  - **用户 12** 在新代码下应是分钟级退出：连击 1 ≥ 门槛 1 → balances 每分钟发布一次触发 C1 排队
    → 窗口内无用量/额度/现金 lot，visibilities 为空进闲置分支 → 五道闸（连击、欠判定、最新候选、
    死信、冻结）全过 → 派生一张复述 deficit 3,610,140 那张检查点的证明 → matched → 连击 2 →
    `active`。前提是它名下没有**锚前且未评估的 reconciliation 检查点**（见 §8.5 的存疑点）。
  - **用户 34 根本进不了闲置分支**：它每分钟都有用量，visibilities 永远非空。它的出路是 C5——
    正向确认合成额度 → matched → 连击 1 → 再一张 matched → `active`。今天对 34 跑 C3 多半会被
    `cycle_has_real_checkpoint` 或 `unevaluated_checkpoints` 拒绝：**答案对，但理由不是连击 0**，
    看报告时别把这两条读成「连击不够」。
- **readyz**：`Queued` 偶发 1–2、秒级清零；不应出现 `eligibility_projection_stuck`。
  `docs/ELIGIBILITY-OPERATIONS.md` 已经把「pending 对 readyz 结构上不可见」这句改掉了。
- **回滚**：重新部署上一版镜像即可（无迁移）。新代码写下的证明/评估/退出/合成额度在旧代码下
  都是合法行；副作用是 12 不再自动重评（已退出者保持 active），34 会被旧的无符号口径重新判回
  pending。

---

## 8. 风险与 follow_up（第一轮复审之后）

### 8.1 softfail 与 rebaseline 上限基本是死代码，但保留

`signedExpectedUnits` 之后 `E − U ≡ Σcredits + Σcash − Σusage` 是精确记账，插入一笔等于延后
差值的额度必然让重建对平到 0——**只要那笔额度真的进了投影**。复审员与我各自独立复核，一共列出
三条仍然可达的路径：

- **A** 合成被 `ON CONFLICT DO NOTHING` 挡住（该项的合成事件 id 上已有行，通常是早先部分修复
  留下的）。已构造成用例
  `TestBalanceBlipConfirmationThatCannotSynthesiseIsSoftfailedNotErrored`。
- **B** 重建时命中 `AmbiguousAt` 提前返回，`confirmDifference` 变成整个余额。很窄：歧义通常已经
  先把账号冻住了。未构造用例。
- **C** 理论上 legacy 账号 `anchor_floor=-∞` 时合成额度被投影的 `event_time` 条件排除。未构造出
  实例。

**处置：代码一律不删。** 理由：C5 把大量原本判 `case −1` 的账号推进了正向防抖路径，而 B1 又
说明防抖对闲置账号会恒真——softfail 与 `balanceBlipRebaselineCap` 是最后还在兜「重建不对平」
的东西，正是新形状最需要它们的时候。

本 RC 的最小可见性动作：`eligibility.balance_blip.rebaselined` 从「一条审计行」变成有人看得见
的信号——手册的部署后观测里给了一条可粘贴 SQL（按 action 查最近 24h），并写明**出现即停**，
不要用工具清状态。

**follow_up（不在本 RC）**：把这条审计提成告警或就绪面板项；给 B、C 两条路径构造用例，或者
判定它们不可达并据此决定是否删除 softfail 分支。

### 8.2 finalize 里那条 CTE 的实测代价（M4）

复审建议把 C1 的相关子查询提成一次性 `max(scan_ceiling_at)` CTE，理由是它跑在
`tryPublishEconomicScanCyclesTx` 已持锁、`lock_timeout=5s` 的那段，而
`source_economic_scan_cycles` 没有能服务「按天花板取该流已发布周期」的索引。已按 (b) 改。

**但实测结果与预期相反，如实记下。** 造 50 万行周期（其中 balances 12.5 万）后 `EXPLAIN
(ANALYZE, BUFFERS)`：

| 形状 | 计划 | buffers | 执行 |
| --- | --- | ---: | ---: |
| 旧：逐账号相关 EXISTS | Nested Loop Semi Join + **Materialize**，内层 Bitmap Index Scan 走主键前缀，`loops=1` | 1312 | 11.9 ms |
| 新：一次性 `max()` CTE | InitPlan + Parallel Seq Scan | 9730 | 29.6 ms |

也就是说：旧形状在这个计划里**并没有**逐账号重复求值（规划器加了 Materialize），而新形状因为
`max()` 必须扫完全部已发布 balances 周期，反而更慢。

仍然选新形状，理由是「一次」这件事从**规划器的选择**变成了**结构上的保证**：Materialize 是代价
模型决定的，统计信息一变就可能退化成每账号一次；InitPlan 按构造只算一次。29.6 ms 在 5s 预算里
是安全的，且不随 pending 账号数增长。顺带 m6 那条「AND 各项按书写顺序求值」的假设也随之消失。

两者都随周期历史线性增长，谁都没有上界。**真正的修法是那条部分索引
`(source_instance_id, stream_id, scan_ceiling_at) WHERE cycle_status='published'`，它需要迁移，
本 RC 不带 → follow_up。** 届时可以把谓词收回到「窗口内存在」的精确形式。

顺带一提，现在的谓词只测下界（「最新已发布周期高于 finalized_through」），它是原谓词的**超集**，
不会漏账号，只会在「所有已发布周期都还高于 requested_through」时多排几条作业；那些作业派生
不到东西、照常推进 finalized_through，代价是停在 N−1 的那几个账号每轮多一行。

### 8.3 minor 9 没有留后门

「重算结果不是 matched 就拒绝 apply」做成了硬 blocker，没有 `--allow-unmatched`。复审给了两个
选项，选严的那个：派生不可逆（证明不可变，0014 之后该周期的真实检查点被永久拒绝），而重算已经
说了它不会对平——为一个已知不会成立的结果永久烧掉一个周期，没有值得留的口子。真需要时是等一条
真实事实，不是等一个操作者。若后续认为过严，加 flag 是个小改动，但要连同「谁批准、审计里怎么
记」一起设计。

### 8.4 第二轮终审 major 1：报告必须复刻派生的连击闸

报告把闲置派生的取周期与重算都复刻了，唯独漏掉派生自己的第一道闸——M3 新加的
「连击 ≥ N−1」。原来连击只在「状态」那一项的 detail 里顺带印一下，不是 blocker。

能走到的场景：账号 pending、连击 0（早先某张检查点判不平把它清零），随后账本被一次
不产生新事实的人工修复改正（policy-start-reanchor、balance-anchor 这类），操作者跑
`pending-reevaluate`——其余检查全绿，报告说会派生并 matched，apply 之后 worker 到连击闸
前停下，什么都没发生。又是「APPLIED 却空转」，只是这次卡在连击而不是窗口。

修法：报告新增 `streak_below_idle_threshold` 检查，与派生读**同一个常量**
`pendingReconciliationIdleMinMatches`，不足即 STOP；STOP 表同步（`runbook_sync` 用例会逼
着补）。手册与运维文档都写明这种账号该怎么办：**没有工具能替它推进连击，只有一次真实的
matched 评估可以**，等下一张真实检查点。变异 M33 把它降成 NOTE 即红。

### 8.5 终审 major 2：未评估计数的两个界，以及一处我不同意的判断

`countUnevaluatedBalanceEvidenceTx` 原来自带一份「评估器选活儿」的副本，少了两个界。
下界（`as_of >= anchor_floor`）**必须补**：XM-INV-PREANCHOR-BALANCE 把 POLICY_ANCHOR 账号
锚前的证据永久排除在评估器取值范围之外，它永远拿不到评估行，于是少了这个界的计数对这类账号
永远 > 0——两个调用方都以「计数为 0」为闸，所以闲置派生被永久关死、C3 永久拒绝，文案还写着
「worker 自己会处理」，而它永远不会。已把评估器的 evidence_floor 与两条谓词抽成常量，计数与
评估器现在共用同一段文本。

**上界（`as_of <= 窗口末端`）不能加，复审说它「无害、只偏保守」这一条我不同意，并有实测。**
加上之后 `TestIdleDerivationDoesNotReleaseAnAccountWhoseNewestEvidenceIsUnfinalizable` 立刻
变红：那条对不上的新检查点正好被 finalization_delay 挡在窗口之上，用窗口去数就数不到它，B1 的
闸失效，账号在「最新上游观测未评估且不符」的状态下被放出去——正是 B2 的持久形态。**「评估器还
欠不欠判定」与「本轮会判什么」是两个问题**，闸只能用前者。

现在计数返回三个数：`Owed`（锚前之上、无评估行，不设窗口上界——闸用这个）、
`InWindowCheckpoints/InWindowProofs`（本轮窗口内，报告用来说明 worker 这轮会处理什么）、
`PreAnchor`（永远不会被评估的那批，报告单独说明，并指向 `--kind=policy-start-reanchor`，
不再拿「worker 会处理」搪塞）。变异 M34（去掉下界）与 M35（改用窗口上界）各自变红。

**另一处要说清楚：主控者转述的「全员性」前提与代码对不上。** 生产实测说每个账号都有一张无评估行
的 `checkpoint_kind='cutover'` 检查点，并据此判断计数对全员恒 >0。但计数（改前改后都）带
`checkpoint_kind='reconciliation'` 过滤，**cutover 那张根本不在计数里**。所以「全员被关死」这个
结论不成立；真正会踩到的是**锚前且未评估的 reconciliation 检查点**。主控者给的数据里，账号
1147（2 条）、2092（5 条）、2222（23 条）「还有更多锚前无评估行的检查点」——**这些行的
`checkpoint_kind` 需要再确认一次**：若是 reconciliation，它们就是真实受害者；若也是 cutover，
那么本条在今天的生产上没有实际触发，修复仍然正确（防的是明天）。我没有生产只读权限，无法自己核。
用例两种形状都造了：cutover 那张（不计入）与锚前 reconciliation 那张（改后不计入）。

### 8.6 follow_up：ADMIN-CREDITS 切片必须带去重

当桥接把 `redeem_codes.type='admin_balance'` 放行、真实管理员加款作为额度事件进账本之后，
用户 34 已有的四条 `UNKNOWN_POSITIVE` 合成额度会与真实加款**重复计数**（§6 第 4 条的实测值）：

| 合成行 event_time | 单位 | ≈ 元 | 对应的真实加款 |
| --- | ---: | ---: | --- |
| 09-01 14:51:09 | 10,144,440,689 | 101.44 | 待 ADMIN-CREDITS 核对 |
| 09-01 15:05:17 | 10,105,221,407 | 101.05 | 待 ADMIN-CREDITS 核对 |
| 09-01 15:16:17 | 39,219,282 | 0.39 | 待 ADMIN-CREDITS 核对 |
| 09-02 18:27:18 | 30,000,000,000 | 300.00 | 管理员加款 300 元 |

09-01 三笔合计约 203 元，09-02 一笔 300 元。**再加上本切片 C5 在生产会新合成的那一笔**
（≈ 09-06 17:18 的加款额 + 当时 deficit），一共五条要处置。

去重只有两条路，ADMIN-CREDITS 必须在设计里选一条并写进它自己的交接单：

1. **按金额 + 时间窗匹配**后删除合成行。0019 的 GUC 门只允许删 `UNKNOWN_POSITIVE`
   （`0019_balance_blip_repair.sql`），所以删得掉；难点是匹配规则要能解释 09-01 那三条
   （同一次重建里连着写的三笔，与上游事件不是一对一）。
2. **退役合成行**：不删，改为在投影里排除（需要新列或新 credit_kind，等于迁移）。

无论选哪条，判据是同一个：处置前后账号的 `ExpectedBalance − UnallocatedUnits` 不变，
且 `funding_lots` 的现金口径一分不动（合成额度从来不进现金池，不产生 `cash_minor_delta`）。

---

## 9. RC108 影子评估 not_ready 的两个根因（2026-09-09 复盘）

影子评估（备份 `invoice-20260909T080659Z`、tools 0.1.0-rc108）判 not_ready，报告
`H:\temp\claude\rc108-shadow-eval.json`：`verdict_reason` 是「the candidate produced
projection errors」，一条 round error，用户 12 未退出。两件事互相独立。

### 9.1 用户 34 的 PROJECTION_FAILED：softfail 回滚删了不属于它的额度（**代码缺陷，已修**）

```
ERROR: update or delete on table "source_credit_events" violates RESTRICT setting of
foreign key constraint "consumption_allocations_credit_event_id_fkey" (SQLSTATE 23001)
```

**机制已在本地逐字复现**（`TestBlipRollbackNeverDeletesACreditItDidNotWrite`，变异 M37 把
守卫去掉后报出**一模一样的错误串**）：

1. `--reevaluate-evidence` 清了评估行，但**没有**删旧的合成额度（这正是我 §4.1 列的伪象 1）；
2. 重判到同一张检查点，防抖确认要合成——`external_event_id` 与历史合成同键，
   `ON CONFLICT DO NOTHING` 把它挡住（§8.1 路径 A）；
3. 于是 `confirmDifference ≠ 0`，走 softfail 回滚；
4. 回滚按 `external_event_id` 去 `DELETE`，删到的是**那条旧额度**——它已被
   `consumption_allocations` 引用（34 的四条合计 8,336 条分配），FK 是 RESTRICT；
5. 整个账号的投影作业失败，且每次重试都失败，直到失败分级把它打成 dead。

回答复审的三问：

**(a) 生产（不重判旧检查点）能否到达？** 能，但需要一次「删掉评估行」的人工修复做前提。
新证据的 `external_event_id` 是新的，不会撞键；只有当某一项的评估行被删掉、该项重新变成待
评估时，才会再次尝试用同一个键合成。会删评估行的是
`RepairBalanceAnchorEligibility`（`--kind=balance-anchor`）。所以：**没有修复介入时到不了；
跑过 balance-anchor 之后就到得了**，而那正是 34 这类账号将来可能要跑的。**C5 本身不改
`external_event_id` 的构成**（仍是 `unknown-positive:` + 该项的 external_event_id），所以
C5 没有引入新的撞键面，但它把更多账号推进正向确认路径，等于放大了这条路径的曝光。

**(b) 「暂定额度没有分配引用」在什么情况下不成立？** 只要那条额度不是本事务刚写的。同一事务
内不会：确认重建走的是 `buildEligibilityProjectionTx`（纯内存），不写
`consumption_allocations`；作业里的 `reprojectEligibilityTx` 跑在评估**之前**，那时额度还不
存在。**不成立的情形是额度更老**——它经历过至少一轮 `reprojectEligibilityTx`，分配已经写下。

**修法**：`synthesizeUnknownPositive` 现在返回是否真的插入了；回滚只在 `inserted` 时执行。
「撤销暂定额度」的语义本来就是「撤销本事务刚写的那一条」，而不是「删掉这个键上的任何行」。
不删也不影响别的：重建已经证明带着那条额度账本对不平（否则不会进这个分支），延后项无论如何
都记为 `positive_blip_ignored`；审计里新增 `tentative_credit_written` 字段说明当次有没有真的
写过。

**(c) 影子怎么跑才不撞伪象**：这次的答案是**两条都做，但优先级不同**。
- 本片已修的是代码缺陷，修完之后即使撞上伪象也只是「合成没发生、判 ignored」，不会再让作业
  失败。**这是主修法。**
- 伪象本身仍在：`--reevaluate-evidence` 只清评估行，不清合成额度与其分配，所以重放出来的
  合成结果与生产不会一一对应。建议在 rehearsal 步骤里补一条「同时清掉
  `credit_kind='UNKNOWN_POSITIVE'` 的合成额度及其 `consumption_allocations`」——但那要动
  `eligibility-shadow`，**不在本片范围**，列为 follow_up。在它落地之前，判据里要写明：34 的
  合成额度数量在影子上与生产不可比。

### 9.2 用户 12 没退出：窗口被 `--finalization-window-lag 1h` 压成零宽（**我的跑法写错了，不是代码缺陷**）

报告里两个 pending 账号的 `requested_through` 都**恰等于**各自的 `finalized_through`
（`2026-09-09T07:46:06.055197Z`）。这是零宽窗口，`(finalized_through, requested]` 里不可能有
任何周期，闲置派生因此没有候选。

算术（`EnqueueEligibilityShadowFinalizationWindow`）：
`bound = GREATEST(finalized_through, cutover_at, min(水位) − 延迟 − lag)`。
备份时刻 08:06:59Z，`finalized_through` 07:46:06，延迟 900s，lag 3600s →
`08:06 − 15min − 60min = 06:51 < 07:46` → `bound = finalized_through`；`provable` 再在
`(finalized_through, bound]` 这个空区间里找不到天花板，`COALESCE` 回落到
`finalized_through`。**lag 是给落后天级的追赶窗口用的，12 只落后约 20 分钟，1h 的 lag 直接
把窗口清零。§4.1 里推荐 1h 是我写错的，已改。**

**连击不是原因，已在本地证伪**（`TestRehearsalReplayKeepsTheStreakAndOnlyTheWindowDecides`）：
清掉全部评估行再重放，连击回到重放前的同一个值——重放是确定性的，结束在同一项上就结束在同一个
连击上。12 的最后一张证据（09-06 11:29:30Z）生产判 matched，重放后仍是 matched，所以
`cm=1 ≥ 门槛 1`，M3 的闸不会挡它。同一条用例接着证明：零宽窗口 → 一张证明也不派生、状态不动；
换成一次真实 finalize 会请求的窗口 → 派生发生、账号退出 `active`。变异 M38（去掉派生候选查询
的窗口上界）让「零宽不派生」那条断言变红，说明它不是恒真。

**是影子专有还是生产也会？影子专有。** 生产的 finalize 用
`GREATEST(cutover_at, min(水位) − 延迟)`，没有 lag、没有 provable 裁剪；水位每分钟随周期发布
前移，所以窗口对活着的源恒为正宽。12 在生产上的路径仍是 §7 写的那条。

**重跑的判据**：按 §4.1 改正后的命令（去掉 lag）跑，然后确认
`pending_accounts[].requested_through` **严格大于**对应账号的 `finalized_through`——这一条要
在看 diff 之前先看，它为零宽时整份报告对 C1/C2 没有信息量。
