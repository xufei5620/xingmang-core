# CR-0009：开票管理端"用户账本"视图（按用户看充值、消耗、可开票与阻断原因）

> 状态：**proposed**。优先级 P1（操作员当前唯一的日常工作视图，冻结队列本身已不足以支撑复核工作）。
> 依据：验收线 2026-09-03 复核生产资格冻结队列时的直接发现（待处理记录从
> CR-0007 立单时的 83 条增长到 241 条，全部不可操作——见"背景/问题"）；产品负责人
> 同日就"发票可开金额"给出的书面规则（见下）。

## 发起方
验收线（生产只读复核开票资格冻结队列时发现）。

## 接收方
开票线（invoice-system，全部改动落在这里）；平台线（xingmang-platform）不涉及——
`InvoiceConsolePanel.tsx`/`EmbeddedConsoleFrame.tsx` 只是 iframe 承载壳，不解析、不
展示任何开票业务数据（CR-0005"明确不变"），本 CR 不改变这条边界。

## 目标资源
- 新增管理端只读查询：`GET /api/v1/admin/accounts/ledger`（列表）与
  `GET /api/v1/admin/accounts/{external_account_id}/ledger`（详情），落在
  `backend/internal/httpapi/operations.go`（同 `listEligibilityFreezes`/
  `listUserEligibilitySummary` 一类只读 handler）与对应的
  `backend/internal/postgresstore` 只读查询函数。
- 复用（不改变判定语义，只新增只读读法）：`invoice_eligibility_policy` 单例
  （`eligibility_start_at`）、`funding_lots`（`verified_cash_minor`/
  `consumed_cash_minor`/`issued_minor`/`reserved_minor`/`eligibility_kind`/
  `eligibility_cutover_at`/`completed_at`/`refund_frozen`）、
  `funding_lot_consumption_state`、`consumption_allocations`、
  `source_account_eligibility_state`（`eligibility_status`）、
  `eligibility_projection_jobs`、`balance_reconciliation_checkpoints`/
  `balance_checkpoint_evaluations`、`eligibility_freezes`、
  `external_accounts.external_user_id`（CR-0007 已选出）、
  `adminsettings.MinimumRequestMinor`。
- 开票管理端前端：`web/src/App.tsx` 新增"用户账本"页签与详情抽屉，
  `web/src/lib/http-api.ts` 新增映射，`web/src/types.ts` 新增类型；既有
  "资格冻结"页签保留但收窄（见"变更范围"）。
- 文档：`docs/ELIGIBILITY-OPERATIONS.md` 新增对应章节。

## 背景/问题
CR-0007 解决的是"冻结队列每一行认不认得出是谁、拒绝原因看不看得懂"，当时的
83 条待处理记录是该改动的直接动因，已随 RC73 上线（`external_user_id` 列、
按 ID 过滤、四类可区分错误码）。验收线 2026-09-03 再次复核生产队列时发现：
即使每一行都认得出账号，队列还是从 83 条涨到了 241 条，操作员依旧**无法对
任何一条记录采取行动**——不是看不懂某一行，而是"一行冻结记录"从来就不是
操作员需要回答的问题的单位。操作员要回答的是"这个用户现在能不能开票、不能
的话具体差什么、什么时候能补上"，这是一个**按用户聚合**的问题；队列把同一
账号的多条历史冻结（例如 `SOURCE_GAP` 类的机制性重复冻结，见
`docs/handoffs/ACCEPTANCE-LOG.md` 2026-09-03"开票冻结队列复核"条目）和真正
需要人工复核的条目混在同一张按行罗列的表里，看不出"这个用户"这条主线。

产品负责人 2026-09-03 就"发票可开金额"给出书面规则，本 CR 是把这条规则第一次
变成一个操作员看得到的界面：可开票金额 = 2026-09-01 00:00（Asia/Shanghai）起
的现金充值中已消耗的部分，旧余额优先消耗（先进先出），最低起开 200 元
（`minimum_request_minor=20000`）；负值或未对账状态是"暂不可开票"，不是需要
人工复核的问题；用量超出账本只封顶可开票金额，不产生额外阻断。

按这条规则，"资格冻结"队列里相当一部分记录（尤其是等下一次对账周期即可
自愈的 `SOURCE_GAP`/负值检查点）本就不该占用人工复核的注意力——是"还没到
时间"，不是"卡住了"。本 CR 不改变任何判定条件本身（`SOURCE_GAP` 持续增长的
机制性根因由另一只读调研任务追踪，与本 CR 并行起草的
`docs/superpowers/specs/2026-09-03-xm-inv-eligibility-simplification-design.md`
负责厘清"人工复核子集"的确切定义），只改变操作员看到的**聚合单位**：把
"按行看队列"换成"按用户看账本"，冻结队列收窄为其中真正需要人工复核的子集。

## 变更范围

**开票线（全部）：**

1. 新增管理端账本 Query：`GET /api/v1/admin/accounts/ledger`（分页列表，支持
   按 `external_user_id`/邮箱前缀模糊搜索、按 `source_instance_id` 过滤、按
   `invoiceable_now_minor`/`block_state` 排序，字段见"契约变化"精简版，不含
   完整充值/消耗明细）；`GET /api/v1/admin/accounts/{external_account_id}/ledger`
   （单账号详情，含完整充值列表与消耗时间线）。两者均只读，权限、CSRF、IP
   白名单与既有 `/admin/eligibility-freezes` 同一套（`s.require("admin", ...)`）。
2. 后端聚合逻辑：以 `invoice_eligibility_policy.eligibility_start_at`（今天是
   固定单例 2026-09-01T00:00:00+08:00）为起点，对该账号下
   `eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH')` 且
   `verification_state='verified'` 的 `funding_lots` 按 `completed_at` 升序
   聚合"起点后充值"；可开票金额取聚合后的
   `verified_cash_minor - issued_minor - reserved_minor` 与
   `MinimumRequestMinor` 比较得到 `threshold_reached`。这与
   `ListUserEligibilitySummaries`（用户自助"可开票摘要"）算的是同一件事的
   管理员视角，本 CR 倾向复用其内部计算、换成按 `external_account_id` 查询、
   返回更细明细，是否抽共享函数由实现切片决定。
3. `block_state` 与 `block_reason`（只读组合既有信号，不新增判定条件）：
   `frozen_manual_review`（有 `status='open'` 的 `eligibility_freezes`，且
   `freeze_reason` 不属于"等下一次对账自愈"的机制性原因，白名单待与"消歧
   设计"文档共同确定）；`not_invoiceable_pending_reconciliation`（无需人工
   复核的冻结，但 `eligibility_projection_jobs` 有未清任务，或最近一次
   `balance_checkpoint_evaluations.evaluation_status` 不是 `matched`/
   `positive_classified_non_cash`）；`below_threshold`（无冻结、投影已清、
   对账通过，但 `invoiceable_now_minor < MinimumRequestMinor`）；
   `invoiceable`（以上都不满足）。`block_reason` 是四态之一对应的人类可读
   中文原文，命名具体事实（时间、差额、下一次预期检查点），例如"2026-09-03
   06:25 上游余额 315.33 元比账本 315.37 元少 0.04 元，等待下一次核对"；不得
   使用"数据异常"这类不点名具体事实的泛泛表述。
4. 前端新增"用户账本"页签（`web/src/App.tsx`，紧邻既有"资格冻结"页签）：
   列表（搜索框、`invoiceable_now_minor` 排序、`block_state` 徽章列）、详情
   抽屉（充值列表、消耗时间线、可开票/已开票金额、`block_reason` 原文、
   `last_checkpoint_at`/`last_reconciled_at`）；复用既有分页/搜索/抽屉组件与
   `friendlyError()` 错误呈现纪律，不新建一套 UI 基础设施。
5. "资格冻结"页签收窄：过滤条件默认只显示 `block_state=frozen_manual_review`
   的账号级冻结，不再展示等对账自愈的机制性记录；页签本身（列、按钮、解冻
   流程）不删除，仍是唯一能实际调用 `POST .../resolve` 的入口，"用户账本"
   页签只读、不提供解冻操作。这一收窄与并行起草的消歧设计文档共享同一个
   "人工复核子集"定义，实现以该文档定稿为准；若交付顺序不一致，可推迟到
   定稿后再做，不阻塞本 CR 其余部分先行交付。
6. 文档：`docs/ELIGIBILITY-OPERATIONS.md` 新增"管理员账本视图"一节，说明
   两个新端点、`block_state` 四态与"资格冻结"页签收窄后的范围。

**平台线：无改动**——`InvoiceConsolePanel.tsx` 三个嵌入入口原样承载新页签，
不需要感知其存在（同 CR-0007 先例：新增页签是 iframe 内部路由，不涉及
`EmbeddedConsoleFrame` 承载壳本身）。

## 契约变化

`GET /api/v1/admin/accounts/ledger` 响应项（列表，精简）：

```json
{
  "items": [
    {
      "external_account_id": "1a2b...", "source_type": "sub2api",
      "external_user_id": "1147", "policy_start_at": "2026-09-01T00:00:00+08:00",
      "recharges_since_start_count": 3, "recharges_since_start_minor": 128000,
      "consumed_since_start_minor": 96000, "invoiceable_now_minor": 31800,
      "issued_minor": 0, "threshold_reached": true,
      "block_state": "invoiceable", "last_checkpoint_at": "2026-09-03T06:25:11Z"
    }
  ],
  "has_more": false, "next_before_invoiceable_minor": null, "next_before_id": null
}
```

`GET /api/v1/admin/accounts/{external_account_id}/ledger` 响应项（详情，在
列表字段基础上追加）：

```json
{
  "external_account_id": "1a2b...", "source_type": "sub2api", "external_user_id": "1147",
  "policy_start_at": "2026-09-01T00:00:00+08:00",
  "opening_balance_units": {"service_units": "48200", "unit_code": "USD_MICRO"},
  "recharges_since_start": [
    {"funding_lot_id": "f9e8...", "completed_at": "2026-09-02T03:11:00Z",
     "amount_minor": 50000, "eligibility_kind": "WALLET_CASH", "refund_frozen": false}
  ],
  "consumed_since_start_minor": 96000, "invoiceable_now_minor": 31800,
  "issued_minor": 0, "threshold_reached": true,
  "block_state": "not_invoiceable_pending_reconciliation",
  "block_reason": "2026-09-03 06:25 上游余额 315.33 元比账本 315.37 元少 0.04 元，等待下一次核对",
  "last_checkpoint_at": "2026-09-03T06:25:11Z", "last_reconciled_at": "2026-09-02T18:04:02Z"
}
```

金额字段全部 minor unit（分）整数，禁止浮点；`opening_balance_units`/用量类
字段沿用既有"服务单位 + `unit_code`"表达（同 `ListUserEligibilitySummaries` 的
`legacy_noninvoiceable`/`noncash`），不假装非现金单位是人民币。所有时间戳
UTC ISO-8601，前端按 Asia/Shanghai 展示（与 `invoice_eligibility_policy.
display_timezone` 一致）。

`GET /api/v1/admin/eligibility-freezes`：查询参数与响应形状不变（CR-0007
定义的形状是本 CR 的地基，不是修改对象）；前端默认过滤条件收窄（"变更范围"
第 5 条）不是服务端契约变化。

## 兼容性/安全
- 两个新端点与既有 `/admin/eligibility-freezes` 同一套准入（管理员角色、IP
  白名单、CSRF 策略）；不新增写操作，不改变任何资格判定条件、顺序或
  `ResolveEligibilityFreeze` 的准入逻辑，不改变既有表的触发器与不可变性约束。
- `external_user_id` 明文展示沿用 CR-0007 已确立的先例（管理员可见、不构成
  新 PII 类别），不重复论证；不新增邮箱/用户名等 CR-0007 已明确排除的字段。
- 金额一律 minor unit 整数；时间戳存储 UTC，展示层固定 Asia/Shanghai，与
  `invoice_eligibility_policy` 既有约束一致。

## 验收标准
1. 对任意一个有过充值记录的账号，管理员在"用户账本"页签（无需 SQL）即可
   回答：2026-09-01 起充值多少、消耗多少、当前可开票多少。
2. 若该账号当前不可开票，`block_state`/`block_reason` 给出四态之一与对应
   的具体事实（不是"数据异常"这类不点名的泛泛表述），且与直接查库核对的
   实际触发条件一致。
3. 搜索（用户 ID/邮箱前缀）与排序（按可开票金额）结果与直接查库核对一致。
4. "资格冻结"页签收窄后只展示 `block_state=frozen_manual_review` 的账号级
   记录，既有解冻流程（CR-0007 交付的四类错误码、`external_user_id` 列、
   过滤）行为不回归。
5. 三个嵌入入口与独立入口下"用户账本"页签渲染一致，平台线无需任何改动。

## 优先级
P1——这是操作员当前唯一的日常工作视图；CR-0007 解决的"看得懂"没有解决
"从哪个单位看"，241 条不可操作记录的现状证明后者才是真正的阻塞点。

## 状态
proposed。

## 明确不变
- 不改变 `invoice_eligibility_policy`、任何资格判定触发器或
  `ResolveEligibilityFreeze` 的判定条件、顺序与安全边界。
- 不新增任何写操作；两个新端点全程只读聚合既有表。
- 队列与解除接口既有的角色、CSRF、IP 白名单访问控制不变；"资格冻结"页签
  的解冻能力保留，不迁移到"用户账本"页签。
- 平台侧 iframe"不认识开票业务内容"的既定原则（CR-0005"明确不变"）不变：
  本 CR 全部改动在开票系统内部，平台线不需要感知新页签的存在。
- 金额单位、时区展示纪律（minor unit、Asia/Shanghai）不变。

## 回滚
本 CR 全部改动为新增只读端点、新增前端页签、既有页签的默认前端过滤条件
调整，没有数据迁移、没有新表新列。回滚 = 停用两个新端点、隐藏"用户账本"
页签、把"资格冻结"页签的默认过滤条件还原为不收窄——均为纯前端/路由层面
的回退，不涉及数据修复。

## 确认
- 平台线：验收线，2026-09-03（按运营复核发现起草，无平台侧改动）。
- 开票线：验收线，2026-09-03（按运营复核发现起草）。
- 产品负责人：待确认（含"资格冻结"页签收窄的具体人工复核子集定义，与并行
  起草的消歧设计文档共同确认）。

## 执行记录
- 2026-09-03 立单（设计阶段，未派发实现切片）。
