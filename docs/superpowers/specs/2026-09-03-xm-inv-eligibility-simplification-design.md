# XM-INV-ELIG-SIMPLIFY：可开票判定简化与策略起点锚定

**状态：** 设计中（本文档为纯文档产出，不含代码改动）。
**产品负责人决策日期：** 2026-09-03。
**前置阅读：** `docs/superpowers/specs/2026-09-02-invoice-policy-anchor-design.md`（尤其 2.1、2.4、
2.6、2.7、2.8）、`docs/handoffs/XM-INV-PREANCHOR-USAGE.md`、`docs/handoffs/XM-INV-ANCHOR-BALANCE.md`、
`docs/handoffs/XM-INV-BALANCE-BLIP.md`、`docs/handoffs/XM-INV-FREEZE-QUEUE-UX.md`、
`docs/handoffs/XM-INV-READY-PENDING.md`、`docs/ELIGIBILITY-OPERATIONS.md`。

## 1. 目标与不变量

产品负责人 2026-09-03 定下六条规则（复述要点，编号后续引用一致）：

1. 可开票金额只由 2026-09-01 00:00（Asia/Shanghai，即 `invoice_eligibility_policy.eligibility_start_at`
   的 UTC 值 2026-08-31T16:00Z）之后的充值决定；充值必须先被消费才可开票；消费"先进先出"——9/1 前的
   期初余额先于任何 9/1 后的充值被消费。9/1 前的充值永远不进入本系统（运营线下处理）。
2. 起票门槛 200 元（`minimum_request_minor=20000`，已实现，不变）。
3. 负余额或未对平的账本状态，只需让用户"暂时不可开票"，不建人工队列，等下一批检查点自动对平后自动
   解除。
4. 消费超出账本不冻结账户：只有超出部分不可开票（可开票额本就封顶于已消费的 9/1 后充值）。
5. 人工队列只保留真正需要人处理的：退款/冲正（`SOURCE_REFUND`、退款敞口）、红冲、自愈失败的数据缺口
   （`SOURCE_GAP`）。
6. 每个已锚定账号都要从 2026-09-01 00:00 起计入全部充值，即使账号之后才被首次观测到：在策略起点锚
   定、倒推期初余额，把起点到首个检查点之间的充值/消费纳入账本；今日生产没有账号在该窗口内有真实充值
   （已验证），故这是前瞻修复，但对已存在的三个 `POLICY_ANCHOR` 账号（`40bd883d...`、`98cce4c8...`、
   `6706ea6a...`）需要设计一次重新锚定迁移，窗口内无事实时必须是空操作。
7. 运营需要按用户的账本视图（UI 属于平台侧 CR-0009），本设计只定义发票后端为此暴露的查询。

**不变量：** 三张事实表（`source_credit_events`/`source_usage_events`/
`balance_reconciliation_checkpoints`）仍不可变，除已有的窄范围 GUC 门控修复例外（2.7、2.8）；本设计
的"自动降级"只改账户状态列与评估分支，不篡改历史事实。`eligibility_status='active'` 仍是唯一允许提交
发票申请的状态（`requests.go`/`store.go` 不变）。可开票金额永远封顶于已消费的现金池，这个封顶关系
（非现金池先于现金池被消费）不变，本设计只改变"要不要冻结账户"，不改变"这笔钱算不算钱"。

## 2. 现状

`eligibility_freezes.freeze_reason` 现有 11 种（迁移 0009/0016）：`UNKNOWN_NEGATIVE_BALANCE`、
`LATE_FINALIZED_EVENT`、`AMBIGUOUS_EVENT_ORDER`、`EVENT_PAYLOAD_DRIFT`、`UNIT_MISMATCH`、
`USAGE_EXCEEDS_LEDGER`、`STREAM_WATERMARK_REGRESSION`、`SOURCE_GAP`、`SOURCE_REFUND`、`EVENT_DEAD`、
`POLICY_ANCHOR_BLOCKED`。每种都经 `freezeEligibilityTx`（`postgresstore/consumption.go:1939`）落一行
`eligibility_freezes`、把账户 `eligibility_status` 置 `frozen`、作废全部预留，且必须走
`ResolveEligibilityFreeze`（人工、MFA、证据哈希）解除——这正是要收窄的"人工队列"。

两处与本设计直接相关：`evaluatePendingBalanceEvidenceTx`（consumption.go:3090）里
`difference<0`（或 `balance_negative=true`）一律冻结 `UNKNOWN_NEGATIVE_BALANCE`——2.7/2.8 已让"正差
额"有充分自愈机会（信任区间 + 边界规则 + 延迟确认），"负差额"至今没有自愈路径，一次性冻结整户。
`reprojectEligibilityTx`（consumption.go:1840）算出 `projection.ShortfallUsage`（用量在非现金池+现金
池耗尽后仍有剩余）时无条件冻结 `USAGE_EXCEEDS_LEDGER`；但可开票额本就只由已分配进现金池的部分决定，
超出部分从未被计入任何发票，冻结账户对此没有额外保护，只有额外伤害（作废全部预留，包括与超额无关、
早已对平的现金）。

**人工队列真实构成：** `SOURCE_REFUND` 恒人工；`LATE_FINALIZED_EVENT` 有两个触发点——
`observeEligibilityFact` 里"任何迟到事实都冻结整户"（宽泛防御）与 `reprojectEligibilityTx` 里"某笔
已开票金额超过重推消费额"（`markLotIssuedAttentionTx`，真正的红冲，只能靠人补红字发票）；`SOURCE_GAP`
有两个触发点——遗留账户重放历史缺失前序事实（真实缺口）与信任区间查询找不到任何起点的兜底（2.7 后，
`POLICY_ANCHOR` 账户恒有自己的 `cutover_at` 作信任起点，这条路径只对遗留账户里连
`checkpoint_kind='cutover'` 行都没有的极端情形触发，现状文档明确"生产不存在，只能靠手写 SQL 构
造"）；其余六种（`EVENT_PAYLOAD_DRIFT`/`UNIT_MISMATCH`/`AMBIGUOUS_EVENT_ORDER`/
`STREAM_WATERMARK_REGRESSION`/`EVENT_DEAD`/`POLICY_ANCHOR_BLOCKED`）都是数据完整性/迁移期异常，不是
"对平时机"问题——本设计默认不动（第 7 节确认）。

`audit_events`（迁移 0001）只存 `before_hash`/`after_hash`（`stateHash` 对 JSON 取 SHA-256，
`records.go:346`），从不落明文。"人类可读的具体触发事实"（规则 7）不可能靠读审计日志拼出来，必须在
账户状态表上开新的明文列——这是 3.5 节查询契约的直接约束。

## 3. 设计

### (A) 自动阻断状态 `NOT_INVOICEABLE_PENDING_RECONCILIATION`

**选择：复用 `eligibility_status` 列，新增枚举值 `not_invoiceable_pending_reconciliation`**，不另开
并行状态表。理由：该列已是提交发票申请的唯一依据（`=active` 放行），新值天然落入"非 active 不放行"
分支；只需要给"非 active 原因展示"（`httpapi/user_dto.go:138`、`application/service.go:1155`、
`docs/ELIGIBILITY-OPERATIONS.md` 的封闭原因列表）各加一个分支。

**新增列**（`source_account_eligibility_state`）：`pending_reconciliation_reason TEXT`（同
`freeze_reason` 取值）、`pending_reconciliation_trigger_type/id TEXT`、`pending_reconciliation_detail
TEXT`（人类可读一句话）、`pending_reconciliation_since TIMESTAMPTZ`、
`pending_reconciliation_consecutive_matches INTEGER NOT NULL DEFAULT 0`；配对 CHECK：非该状态时全
NULL/0（与 `eligibility_freezes` 现有 open/resolved 配对 CHECK 同手法）。

**何时进入。** 新增 `enterPendingReconciliationTx`，替换 `ObserveBalanceCheckpoint` 两个 bootstrap
分支与 `evaluatePendingBalanceEvidenceTx` 里共三处 `UNKNOWN_NEGATIVE_BALANCE` 的 `freezeEligibilityTx`
调用：写五列、置状态、仍调用 `invalidateAccountReservationsTx`，但**不**写 `eligibility_freezes`
行——这是它不进人工队列的原因。**唯一例外：** 账户已是 `frozen`（存在别的真实开放冻结）时不降级覆盖，
`frozen` 优先。

**何时自动退出。** 计数不挂 `eligibility_projection_jobs`（该表每次任务成功都无条件 `DELETE`，见
`processEligibilityProjectionJob:2680`，不适合跨批次计数），挂在账户行自身：`writeEvaluation` 每次真
实证据（检查点/结转证明）评为 `matched` 时，若账户处于待对平状态，计数加一；评为 `negative_frozen` 时
清零并刷新 `detail`；评为 `positive_classified_non_cash`/`positive_blip_ignored`/`source_gap_frozen`
不影响计数。达到 N=2（建议值：两次连续对平比一次更能排除偶然）且账户无任何开放 `eligibility_freezes`
（复用 `ResolveEligibilityFreeze` 已有的 `count(*)... status='open'` 判断，两处语义保持一致）时，五列
清空、状态回 `active`。

**审计与 readyz。** 进入/退出各写一条审计事件（只落哈希，明文靠新增列）。`processEligibilityProjectionJob`
每次结束都无条件删除自己的任务行，与账户最终落在哪个状态无关，所以待对平状态不会在任务队列滞留，
`EligibilityProjectionHealth`（喂给 readyz）完全看不到它、不需要改；`validateSourceIngestRuntimeReadiness`
同理只看五条源流健康度。一个账户可以"待对平"数小时甚至数天，readyz 全程 200——有意为之：这是账户级、
非阻塞信号。

**迁移：** 扩展 `eligibility_status` CHECK、新增五列及配对 CHECK。写作本文档时下一可用编号是 `0020`，
但历史上（`XM-INV-BALANCE-BLIP` 撞上并发占用的 `0017`）多次出现编号在实现时已被占用，实现者需以当时
`backend/migrations/` 目录为准。

### (B) `USAGE_EXCEEDS_LEDGER` 降级：不冻结，只记录超额

`reprojectEligibilityTx` 不再对 `ShortfallUsage` 调用 `freezeEligibilityTx`。新增
`non_invoiceable_overage_units NUMERIC(78,0)` 与 `non_invoiceable_overage_usage_event_id UUID
REFERENCES source_usage_events(id)`（同一次 0020 迁移，配对 CHECK：同时 NULL 或同时非 NULL），每次重
投影后：`ShortfallUsage!=""` 则写入该用量事件 id 与未分配的 `service_units`，否则清空。写审计事件
`eligibility.usage.overage_recorded`/`.cleared`（只落哈希）。账户状态、预留、已开票金额不受影响——
`buildEligibilityProjectionTx` 本就只把分配成功的部分算进 `ExpectedBalance`，超额部分从未进入现金池
分配，这个封顶是既有代码路径，只需删除"发现超额就冻结"这一步。`freeze_reason` 枚举保留
`USAGE_EXCEEDS_LEDGER`（历史数据、第 3.3 节迁移工具需引用），只是不再有代码路径主动创建新的这一原因
的冻结。

### (C) 人工队列收窄

**保留清单：** `SOURCE_REFUND`；`LATE_FINALIZED_EVENT` 仅限 `reprojectEligibilityTx` 里
`lot.RoundedMinor<lot.IssuedMinor` 的红冲分支；`SOURCE_GAP`。**明确不变（见第 7 节）：**
`EVENT_PAYLOAD_DRIFT`、`UNIT_MISMATCH`、`AMBIGUOUS_EVENT_ORDER`、`STREAM_WATERMARK_REGRESSION`、
`EVENT_DEAD`、`POLICY_ANCHOR_BLOCKED`——规则 5 原文没有点名它们，且它们是数据完整性/迁移期异常而非
对平时机问题，贸然自动解除有真实数据风险。

**改动：**
1. `UNKNOWN_NEGATIVE_BALANCE`、`USAGE_EXCEEDS_LEDGER`：见 (A)/(B)，不再创建新的 open 冻结。
2. `observeEligibilityFact` 里泛化的 `LATE_FINALIZED_EVENT`（无 `funding_lot_id`，触发对象是事实本
   身）不再冻结——仍调用既有的 `reprojectEligibilityTx`（会在真正命中红冲条件时自己开一个更精确、带
   `funding_lot_id` 的冻结），只是去掉"不管有没有真超发都先冻整户"的额外防御层。审计事件改为
   `eligibility.late_fact.reprojected`（不冻结）。
3. **既有开放冻结迁移：** 新增 `eligibility-repair --kind=queue-narrow`（复用现有 CLI）。对每条仍 open
   且 `freeze_reason IN ('UNKNOWN_NEGATIVE_BALANCE','USAGE_EXCEEDS_LEDGER')` 或
   `freeze_reason='LATE_FINALIZED_EVENT' AND funding_lot_id IS NULL` 的记录：用与
   `ResolveEligibilityFreeze` 相同列形状写 resolve（固定说明"由 XM-INV-ELIG-SIMPLIFY 迁移自动解除"），
   并对前两种额外触发 (A)/(B) 的新记录路径——用当前最新一次评估结果重建 `pending_reconciliation_*`/
   `non_invoiceable_overage_*` 的初始值，而非直接清空（否则此刻仍真实负余额的账户会被误报"已解决"）。
   dry-run/apply 两态，与既有三个修复模式同一套确认流程。

### (D) 策略起点锚定：从"首个检查点"改为"策略起点"

**两处死区（都在"策略起点"与"首个检查点"之间）：** 2.1 让 `POLICY_ANCHOR` 账户的 `cutover_at` 等于
**首个观测到的检查点自身的 `as_of`**，而非 `policy.StartAt`。用量/信用事实：2.2 只丢弃
`observed_at<PolicyStartAt` 的事实，但 2.6（`observeEligibilityFact`）对 `POLICY_ANCHOR` 账户的判断
是 `!in.EventTime.After(account.CutoverAt)`——发生在"首个检查点"之前（哪怕晚于策略起点）的事实一律
跳过且**不落库**（`XM-INV-PREANCHOR-USAGE` 事故里 106 个账户的窗口内用量正是这样被不可恢复地丢弃
的）。现金充值：`buildEligibilityProjectionTx` 的付款查询条件是 `fl.completed_at>account.CutoverAt`，
一笔落在"起点到首个检查点"之间的真实充值会被永久排除在投影之外——这是规则 6 要修的洞。

**修法：`cutover_at` 统一改为恒等于 `policy.StartAt`。** `ObserveBalanceCheckpoint` 的
`POLICY_ANCHOR` bootstrap 分支不再把 `cutover_at` 设为触发检查点的 `as_of`，而设为
`policy.StartAt`；`cutover_balance_units` 按公式倒推：`首个观测检查点余额 −
credits(策略起点..检查点as_of) + usage(策略起点..检查点as_of)`（取窗口内此刻已落库的事实）。2.2/2.6
的跳过边界同步从"账户自己的 `cutover_at`"改为全局常量 `policy.StartAt`——窗口内事实此后正常持久化、
正常参与投影，不再有死区。

迁移 0016 现有校验（锚点必须精确匹配一行真实检查点）不应削弱：bootstrap 时**额外插入一行**
`checkpoint_kind='reconciliation'`、`as_of=policy.StartAt`、`balance_service_units=`倒推值，借用触发
它的真实检查点的溯源列（`source_sequence`/`cursor`/`watermark`/`revision`/`observed_at`）——与
2.7/2.8 合成 `UNKNOWN_POSITIVE` 信用"借用触发事实溯源列"同一手法：推导而非观测出来的期初余额，但推导
过程完全来自已观测事实。沿用 2.0 的 `DEFERRABLE CONSTRAINT TRIGGER` 延后到 COMMIT 检查，不需要新的
延迟机制。

**边界情况：** 首条检查点早于策略起点——不适用（2.1 原有"直接跳过、不 bootstrap"不变）。账号在策略
起点后才注册——倒推公式的窗口起点仍是全局 `policy.StartAt`，不因账号何时被观测到而变化。

**对既有三账号的 re-anchor（新增 `eligibility-repair --kind=policy-start-reanchor`）：** dry-run 查询
窗口 `[policy.StartAt, 当前cutover_at)` 内是否存在已落库的 `funding_lots`（用量/信用事实在 2.6 修复
前已被丢弃且不可恢复，dry-run 只能核对充值这一类，即产品负责人已验证的那类）。零笔则报告"符合预期的
空操作"且**不修改任何行**（时间戳往回挪但数值不变，只增风险无收益）；非零笔（不预期）才需人工确认后
`--apply`：插入派生检查点 + 用迁移 0016 的 guarded UPDATE 机制改 `cutover_at`/`cutover_balance_units`
+ 按 2.4 套路清空 `consumption_allocations`/重置消费状态/重新投影。0016 现有 GUC 只放行
`legacy→POLICY_ANCHOR`，本设计新增姊妹 GUC（如 `invoice.policy_anchor_start_reanchor`）放行
`POLICY_ANCHOR→POLICY_ANCHOR`（同类型内部重锚），避免与 2.4 的语义/审计动作混用。

**与 (A) 的交互：** re-anchor 后的重投影可能产生新的负差额/超额用量，应走 (A)/(B) 的自动降级而非老的
整户冻结——本切片必须晚于 (A)/(B) 上线。

### (E) 用户账本查询契约（供 CR-0009 消费）

新增只读端点，沿用 `/api/v1/admin/eligibility-freezes` 同一套管理员角色 + IP 白名单（返回明文
`external_user_id`，安全 posture 与该端点一致）。每个 external account 一行：

```json
{
  "source_instance_id": "...", "source_type": "sub2api", "source_name": "...",
  "external_user_id": "34",
  "recharged_since_policy_start_minor": 500000,
  "consumed_minor": 320000,
  "invoiceable_minor": 180000,
  "threshold_minor": 20000,
  "threshold_reached": true,
  "eligibility_status": "not_invoiceable_pending_reconciliation",
  "block_reason": "UNKNOWN_NEGATIVE_BALANCE",
  "block_detail": "结算点 ckpt-xxx 于 2026-09-03T10:00:00Z 上报余额 -120，预期 380",
  "block_since": "2026-09-03T10:00:12Z"
}
```

`recharged_since_policy_start_minor`：(D) 后 `cutover_at` 恒为策略起点，即
`completed_at>=account.CutoverAt` 的 `WALLET_CASH`/`SUBSCRIPTION_CASH` `verified_cash_minor` 之和。
`invoiceable_minor` 同 `EligibilitySummary.AvailableMinor` 公式。`block_reason`/`block_detail`/
`block_since` 直接读 (A)/(B) 新增的明文列；`eligibility_status='frozen'` 时改读最新一条 open
`eligibility_freezes` 的 `freeze_reason`/`trigger_object_type/id`，同样不经审计日志。分页沿用既有
keyset 惯例（`before_id` + `limit`，最大 100）。具体路由路径由平台侧 CR-0009 最终确定。

## 4. 与既有安全边界的关系

放弃的"宁可错冻"有三处，各有明确兜底：**负余额不再整户冻结**——可开票额始终封顶于已消费的 9/1 后
现金充值，负余额期间 `invoiceable_minor` 本就是 0 或极小，唯一风险是体验上晚知道几分钟，不是多开出
钱。**用量超账本不再冻结**——超额部分从未进入现金池分配，没有钱从这条路径流出。**人工队列收窄**——
200 元门槛与退款敞口拦截（`SOURCE_REFUND`/`refund_frozen`/`refund_cases`/`refund_attention`）完全保留，
这是真正能把钱错误开出去的路径，一分未动；红冲同样完全保留。

## 5. 测试矩阵

- **(A)：** 单次对平不退出、连续两次对平后退出且不早退出；有别的 open 冻结时不因计数达标而误退出；
  `frozen` 优先、不被新状态降级覆盖；readyz/`EligibilityProjectionHealth` 在待对平状态下无回归。
- **(B)：** 超额不冻结、账户保持 `active`、超额记录列正确写入/清空；既有断言按新行为改写（先证明旧
  代码通过、新代码上失败，再改断言，与 `XM-INV-ANCHOR-BALANCE` 同方法论）。
- **(C)：** 迁移工具对三类历史冻结 dry-run/apply 均正确；红冲分支、`SOURCE_REFUND`、`SOURCE_GAP`、
  六种保留原因完全不受迁移工具影响（各用一个未受影响账户验证）。
- **(D)：** 三账号 dry-run 窗口内零充值时报告空操作且不写任何行；合成一个窗口内有真实充值的账户验证
  倒推公式与派生检查点精确性；边界情况（首条检查点早于起点、账号起点后才注册）各一个测试；姊妹 GUC
  的触发器场景（无 GUC 拒绝、GUC 但 `OLD.bootstrap_kind` 不符拒绝、正常路径接受）。
- **(E)：** 字段正确性、分页、权限（非管理员拒绝）、`block_reason`/`block_detail` 在 (A)/(B)/`frozen`
  三种状态下分别取数正确。

## 6. 分片建议

| 顺序 | 切片 | 内容 | 依赖 |
|---|---|---|---|
| 1 | `XM-INV-ELIG-AUTO-RECONCILE` | (A) 自动阻断状态 + (B) 用量降级 | 无（同为"评估器软化"，测试重叠度高，合并降低中间态风险） |
| 2 | `XM-INV-ELIG-QUEUE-NARROW` | (C) 人工队列收窄 + 既有冻结迁移工具 | 依赖 1（收窄前需先有新的自动记录路径接住） |
| 3 | `XM-INV-ELIG-POLICY-START-ANCHOR` | (D) 策略起点锚定 + 三账号 re-anchor 工具 | 依赖 1（re-anchor 后重投影需要自动降级兜底） |
| 4 | `XM-INV-ELIG-USER-LEDGER-QUERY` | (E) 用户账本查询契约 | 依赖 1（`block_reason` 明文列）与 3（"起点以来充值"口径） |

四个切片均可独立上线（各自单独一个 RC；写作本文档时最近已知标签为 `RC74`，实际编号以合入时的发布计
数器为准）。切片 2 与 3 之间无先后依赖、可并行，但都须晚于切片 1。

## 7. 待产品负责人确认的剩余问题

1. 六种数据完整性类冻结原因（`EVENT_PAYLOAD_DRIFT`/`UNIT_MISMATCH`/`AMBIGUOUS_EVENT_ORDER`/
   `STREAM_WATERMARK_REGRESSION`/`EVENT_DEAD`/`POLICY_ANCHOR_BLOCKED`）本设计默认保持人工——是否确认
   这一读法，还是希望其中某几种也一并收窄？
2. (A) 的自动退出阈值 N=2 是本设计的建议值，是否需要拍板一个具体数字？
3. `enterPendingReconciliationTx` 在账户已 `frozen` 时不降级覆盖——运营在冻结队列里看到的账户，此刻
   也可能同时存在未对平的余额差额但不单独展示（只能通过 (E) 看到 `block_reason` 仍是真实冻结原因）；
   是否需要 (E) 响应里补一个"是否同时存在待对平信号"的布尔位？
