# XM-INV-BINDING-SKEW（L0）—— 认领绑定先看时间合法性，5 分钟判据收成唯一定义

- status: ready-for-review（合入 RC105 候选分支；未 push，未 tag）
- branch: `ai/claude/XM-INV-BINDING-SKEW`（提交 `1895935` + 审稿修复 `ff64b48`）
- base: `d28bb8b`（= RC104 生产提交 `14bf7e7` + 手册落差说明 + 分层设计文档）
- worktree: `K:/发票/wt-XM-INV-BINDING-SKEW`；专用测试库 `invoice_test_bindskew`
- 设计依据：`docs/handoffs/XM-INV-STRANDED-EVENTS-DESIGN.md` 的 L0 一节
- 产出方式：ultracode 工作流，8 个 Opus 5 代理（2 侦察 / 1 实现 / 2 对抗审稿 / 1 修复 / 2 复审），
  两轮复审均 `pass`、两位审稿人均愿签字；主控者另行亲自重跑了审稿人的关键变异。

## 它修的是什么

RC104（2026-09-08 已部署）的 CLAIM-BINDING 让认领优先用代理最新投递的
(event_id, batch_id, scan_cycle_id) 映射，但**只看周期状态、不看时间**；而
`validateFactMetadata` 的 5 分钟规则会拒绝水位晚于 `observed_at + 5min` 的批次。
生产实证（2026-09-08）：客户 2222 的两条 usage 死信重投后，认领选中 published
周期里 09:28:14 的批次，事件 `observed_at` 是 07:08:33，立刻报
`source fact event time/watermark is invalid`，八次判死回到原点；同时写off工具的
`ReplayBlocked` 只看周期状态，坚持说「可重投」——**两个工具互相推诿**，
readyz 继续 503，6 个客户继续开不了票。

更要紧的是它是**会复发的**：任何绑定前就有停放事件的客户，一绑定唤醒就会走进
同一条链。这是 RC104 埋下的新雷，与 09-07 的原始事故（40001 + 周期被放弃）无关。

## 改法（最终形态，含审稿修复）

| 处 | 改动 |
|---|---|
| `consumption.go` | `factClockSkewTolerance = 5 * time.Minute` 唯一定义；`factClockSkewToleranceSQL` 从它渲染（故意渲染成 `interval '300 seconds'`，让手抄的 `'5 minutes'` 字面量被测试抓住）；**比较式**抽成唯一函数 `factWatermarkOutsideClockSkew(observedAt, watermarkAt)`；`validateFactMetadata` 改用它；`factMetadataTimeInvalidMessage` 抽成常量供工具逐字引用；`factClockSkewStreams` 记录**哪些流的写入方真的应用这条规则**（usage / credits / balances；payments 的 `ObserveFundingLot` 不调 `validateFactMetadata`） |
| `source_sync.go` | `claimBindingSelect` 由 `const` 改 `var`，臂 1 从**过滤**改成**三级优先**：`CASE WHEN b.scan_ceiling_at <= sie.observed_at + <渲染片段> THEN 1 ELSE 2 END AS preference`，first_batch_id 回落为 3，`ORDER BY preference, b.sequence DESC` |
| `ingest_requeue_dead_repair.go` | `ReplayBlocked` 补第 4 例：所选绑定的 ceiling 超出容忍（调用同一个 `factWatermarkOutsideClockSkew`），**仅对 `factClockSkewStreams` 中的流生效**；批次 `scan_ceiling_at` 为 NULL 也判 blocked（NULL 读成「没问题」是闸失效的典型形状） |
| `ingest_unreplayable_acknowledge.go` / `cmd/eligibility-repair/main.go` | 复用同一解析器，`--kind` 帮助串补上 `ingest-acknowledge-unreplayable` |
| `docs/ELIGIBILITY-OPERATIONS.md` | 改写「绑定 = first_batch_id」的旧表述为现行三级规则；拒绝理由从两条补齐到四条；写清 payments 永不被报为时钟偏移受阻；按实体类型区分写off方向（usage 缺失 = 少算，保守；credits/退款缺失 = 多算，危险） |

### 为什么是「排序」不是「过滤」（审稿的致命发现）

第一版把时间过滤对四条 v3 流一律生效。但 payments 的写入路径**没有 5 分钟规则**：
一条 payments 死信若首批次落在 blocked 周期、唯一救援是晚到的 published 重投，
RC104 能救回来，第一版反而把它排除、退回 blocked 首批次——**比 RC104 还差**。
改成「时间合法 > 状态合法 > 首批次」的三级优先后：payments 恢复 RC104 行为；
usage/credits/balances 若没有时间合法的绑定，会拿到一个晚到绑定 → 运行时必拒 →
而修复工具用同一函数判出 `ReplayBlocked=true` → `ingest-acknowledge-unreplayable`
放行写掉。两个工具从此说同一句话。

### 「哪些流有这条规则」是手列的，但被发现出来的测试钉住

`factClockSkewStreams` 是一份副本（真身是各 Observe* 是否调 `validateFactMetadata`）。
`TestFactClockSkewToleranceAppliesToExactlyTheStreamsItNames`（`fact_clock_skew_tolerance_test.go:246-267`）
对**每一条经济流**驱动一次真实写入探针，断言「实际是否被规则拒绝」==「名单里是否有它」，
并拒绝名单里出现非经济流。名单漂了会红，不是靠人记得同步。

## 测试（新增 5 个文件 / 修改 1 个，28 个用例 + 子臂）

- T1 `TestParkedFactWokenByBindingLandsOnItsOwnScanCycleBinding`（application 包）：
  首送 observed=now-30m 落 C1（published），C2（published，ceiling=now）重投同一 event_id；
  唤醒后认领选 C1 批次，穿 claim → verify → validateFactMetadata → INSERT 四段，事实落地。
  **旧实现下红**（选 C2）。
- T2 `TestClaimBindingTakesTheNewestOfSeveralValidBindings` 等 ClaimBinding* 系列（含 credits 每周期重投变体）。
- T3a `TestFactClockSkewToleranceIsTheOnlyIntervalLiteralOnTheClaimPath`：claimBindingSelect 文本含渲染片段且**不含任何其他 interval 字面量**；
  T3b `TestFactClockSkewToleranceMatchesEveryFactTableCheckConstraint`：`pg_get_constraintdef` 读三张事实表 CHECK，
  与同一服务器上由渲染片段 deparse 出的参照约束逐字比对；
  T3c `TestFactClockSkewToleranceIsWhatValidateFactMetadataApplies`：边界表（恰好 5m 通过、+1µs 拒绝）。
- T4 `TestIngestRequeueDeadBlocksAnEventWhoseOnlyLaterBindingIsTooLate`、
  `TestIngestRequeueDeadBlocksAFirstBindingBeyondTheClockSkewTolerance`（含「恰好一个容忍仍放行」子臂）、
  `TestIngestRequeueDeadKeepsAPaymentsRescueTheRuntimeAccepts`（payments 不被误伤）、
  `TestIngestRequeueDeadRequeuesAnEventRescuedByALaterValidBinding`。

### 变异验证（实现者逐条跑并还原；两位审稿人在隔离副本里各自重跑；主控者亲自重跑第 ⑩ 条）

| # | 变异 | 变红的 | 对照保持绿的 |
|---|---|---|---|
| ① | 删臂 1 的时间判据（= 第一版之前的实现） | T1、ClaimBindingRejects…、T3a、T4-later-too-late | T3b、T3c、payments 用例、其余全部 |
| ② | validateFactMetadata 改回硬编码 5m 且常量改 45m | T1、ClaimBindingRejects…、T3b、T3c | T4 两条、T3a |
| ③ | 整个删掉臂 1（= 回退 RC104 的 CLAIM-BINDING） | ClaimBinding* 三条、T3a、Requeues…Rescued… | T1、T4 两条、T3b |
| ④ | 臂 1 比较方向翻转 | T1、ClaimBindingRejects…、T3a、T4-later-too-late | 其余 |
| ⑤ | 渲染片段换成手写 `interval '5 minutes'`（行为等价） | **只有 T3a** | 其余全部 |
| ⑥ | 渲染变量改成漂了一秒的手写 `'299 seconds'` | T3a、T3b | 其余 |
| ⑦ | 在渲染片段旁边多加一条冗余的手写 5 分钟判据 | T3a | 其余 |
| ⑧ | 常量改 4 分钟 | T3b | T3a、T3c、T1、T4 |
| ⑨ | 渲染成 `make_interval(secs=>300)` | T3b、T3a | — |
| ⑩ | **工具那行改成手写比较 + 边界翻转**（审稿人的原实验） | `…BeyondTheClockSkewTolerance/a_first_binding_exactly_one_tolerance_ahead_is_still_carried` | 其余 |
| ⑪ | 排序退回成过滤（CASE 塌成 1，WHERE 加回过滤） | `TestIngestRequeueDeadKeepsAPaymentsRescueTheRuntimeAccepts` | 其余 |

第 ⑩ 条是关键：第一版下这条变异**全绿**（审稿人证明「取值」单一化了但「比较式」没有），
修复后红。第 ⑪ 条证明 payments 的保护是活的。

## 门禁

- 定向：28 个用例 + 子臂全 PASS（`postgresstore` 30.2s / `application` 4.6s），CRLF 归一后复跑仍绿。
- 后端全量 `go test -p 1 -count=1 ./...`（专用库 `invoice_test_bindskew`，八个代理变量 `env -u`）：见 RELEASE-RC105.md。
- `go vet ./...`：见 RELEASE-RC105.md。
- `scripts/check-no-secrets.ps1`：exit 0。
- 三个新文件用 LF 副本单独做 gofmt 校验：干净。

## not_run / risks / follow_ups

- **未修的根因仍在**：对账放弃在途周期 → 孤儿（设计文档 L1–L4）。本片只堵 RC104 的新雷、并让写off工具能正确认出不可重投；孤儿的产生与「一个账号拖住全体」的放大面分别是 L3 与 L1。
- 复审剩两条 minor：① 排序谓词用 `b.scan_ceiling_at`（而不是 `b.stream_watermark_at`）只有字面量测试钉着，全仓夹具一律 `StreamWatermarkAt==ScanCeilingAt`，没有行为锚——若两列语义分家会静默；② `ingest_requeue_dead_repair.go:492-496` 的总结句仍说「运行时检查的一切事先可知」，payments 那半的措辞未更新。
- 若 L2（重投记录自己的 observed_at）落地，臂 1 的谓词与外层查询要**一起**改成 `COALESCE(m.observed_at, sie.observed_at)`（源码注释已标）。
- `observed_at` 首送冻结是本片前提（`source_ingest_events` 首写后不在重投时改写）；审稿两方均未质疑，但它靠 CommitSourceBatch 的写法成立，属外部事实。
