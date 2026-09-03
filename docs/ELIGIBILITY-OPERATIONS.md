# Consumption eligibility operations

This runbook covers operational freezes created by the signed consumption
ledger. It does not create a manual amount override.

## Administrator queue

`GET /api/v1/admin/eligibility-freezes` is protected by the existing
administrator role, fresh MFA step-up, CSRF policy and administrator IP
allowlist. It supports a maximum page size of 100 and keyset pagination with
`before_opened_at` plus `before_id`. Filters are `status=open|resolved|all`,
the compiled freeze-reason whitelist, a source-instance UUID and (CR-0007) an
exact-match `external_user_id`, which may be combined with the source-instance
filter to disambiguate across platforms that could otherwise reuse the same
external ID.

The response contains invoice-side IDs, source label/type, optional
funding-lot ID, freeze reason/status, timestamps, CAS version and (CR-0007,
reversing this endpoint's prior posture) the upstream platform's own
`external_user_id`, plain and unmasked. It still never returns source
cursors, trigger object IDs, revision or configuration hashes, evidence
references, notes or ciphertext, and it still never returns the invoice
system's own internal external-account row ID. The plain external user ID is
not a new PII exposure: the platform console's own user detail page already
shows the identical numeric ID in the clear, and the self-service
`listSourceAccounts` endpoint already treats it as administrator-visible
(there, masked). See `docs/change-requests/CR-0007-invoice-admin-freeze-queue-operability.md`
for why the queue previously omitted it (an 83-record backlog was otherwise
unidentifiable without querying the database directly) and why exposing it
plain does not cross a new trust boundary.

## Safe resolution

`POST /api/v1/admin/eligibility-freezes/{id}/resolve` requires:

```json
{
  "version": 1,
  "evidence_reference": "internal-ticket-or-document-reference",
  "note": "why the signed ledger and latest reconciliation now agree"
}
```

Evidence and note are trimmed, bounded, hashed independently and encrypted
with domain-separated AAD before entering PostgreSQL. Audit records contain
only state hashes and the evidence SHA-256. Concurrent resolution uses the
freeze version and account advisory lock; an identical retry is idempotent and
changed evidence conflicts.

The operation refuses resolution unless all of these are true in one
serializable transaction:

- all five source streams pass the existing runtime, freshness, projection,
  pending/dead-event and watermark checks;
- the latest finalized reconciliation checkpoint has a latest evaluation of
  `matched` or `positive_classified_non_cash`;
- the account has no existing eligibility projection job;
- there is no open `SOURCE_REFUND` freeze, refund-frozen lot, open refund case
  or request in `refund_attention`;
- the target is not `SOURCE_REFUND` and the administrator is not the affected
  invoice user.

Resolving one row leaves the account frozen while another open freeze exists.
After the last row closes, the account becomes `active` and a defensive
reprojection job is queued. Submit/issue SQL also requires that no projection
job exists, so entitlement cannot be consumed until that job commits. Refund
and red-letter exposure must be closed through the dedicated refund workflow;
this endpoint cannot bypass it.

None of the above judgment conditions, their order, or the role/CSRF/IP
access control around this endpoint changed for CR-0007. What changed is only
which error code a rejection reports. Previously all four rejection reasons
below were indistinguishable on the wire (409 `CONFLICT`, or 503
`SOURCE_SYNC_UNAVAILABLE` for the first one); each now has its own code so the
admin console can show an operator a specific, correctly-actionable reason
instead of a generic "refresh and retry":

- one or more of the five source streams are not fresh enough: 503
  `ELIGIBILITY_SOURCE_STALE`;
- an eligibility projection job is still pending for the account: 409
  `ELIGIBILITY_PROJECTION_PENDING`;
- the account has open refund exposure (an open `SOURCE_REFUND` freeze --
  including the target row itself -- a refund-frozen lot, an open refund
  case, or a request in `refund_attention`): 409 `ELIGIBILITY_REFUND_EXPOSED`;
- the latest finalized reconciliation checkpoint's evaluation is neither
  `matched` nor `positive_classified_non_cash` (including "no evaluation
  recorded yet"): 409 `ELIGIBILITY_EVALUATION_UNMATCHED`.

A stale CAS `version` (including the self-freeze/non-admin-actor guard) still
reports the unchanged generic 409 `CONFLICT` -- "refresh and retry" remains
the accurate advice there, unlike for the four reasons above.

## 管理员账本视图（CR-0009，XM-INV-CR0009-LEDGER-VIEW）

CR-0009（`docs/change-requests/CR-0009-invoice-admin-user-ledger-view.md`）把
运营复核的聚合单位从"按冻结记录逐行看"换成"按用户账号看账本"，最终确定了
XM-INV-USER-LEDGER-QUERY（design 3(E)）的查询契约，取代其原有的提案路由
`GET /api/v1/admin/eligibility-ledger`（该路由从未上线生产，见
`docs/handoffs/XM-INV-USER-LEDGER-QUERY.md`的替换说明）。两个新端点与
`eligibility-freezes`同一套准入（管理员角色 + IP 白名单，纯 `GET`，无需
MFA 或 CSRF），全程只读，从不读取 `audit_events`——`block_reason`引用的每一
个事实都来自明文列或本节下方列出的既有表。

### 列表：`GET /api/v1/admin/accounts/ledger`

分页 keyset：默认按 `invoiceable_now_minor` 降序，游标为
`before_invoiceable_minor`+`before_id`（两者必须同时提供或同时省略）；
`sort=block_state`时改为按 block_state 优先级排序（`frozen_manual_review`→
`not_invoiceable_pending_reconciliation`→`below_threshold`→`invoiceable`，
最需要处理的排最前，这是实现层面的选择，CR-0009 本身未规定具体方向），
tie-break 为 id 降序，此时游标只需要 `before_id`（`before_invoiceable_minor`
被忽略）。过滤：`external_user_id`（精确匹配）、`source_instance_id`（精确
匹配）。邮箱前缀搜索未实现——用户邮箱经 securefields 加密存储，没有可搜索的
明文列，CR-0009 本身也把这条列为"若无明文列可搜索列则省略并记录偏差"的
条件分支，这里记录为该条件确实成立后的省略，不是遗漏。

```json
{
  "items": [{
    "external_account_id": "1a2b...", "source_type": "sub2api",
    "external_user_id": "1147", "policy_start_at": "2026-09-01T00:00:00+08:00",
    "recharges_since_start_count": 3, "recharges_since_start_minor": 128000,
    "consumed_since_start_minor": 96000, "invoiceable_now_minor": 31800,
    "issued_minor": 0, "threshold_reached": true,
    "block_state": "invoiceable", "last_checkpoint_at": "2026-09-03T06:25:11Z"
  }],
  "has_more": false, "next_before_invoiceable_minor": null, "next_before_id": null
}
```

- `recharges_since_start_minor`/`_count` sum/count `verified_cash_minor` over
  `WALLET_CASH`/`SUBSCRIPTION_CASH` funding lots with `completed_at>=`账号自身
  `cutover_at`（design 3(E) 的逐字公式），**且要求
  `verification_state='verified'`**——这是对 XM-INV-USER-LEDGER-QUERY 原有
  查询的一处修正（那个版本省略了这个过滤，见其自己的 handoff），改为与
  design 第 2 节的完整公式和本文件其余每一处触及现金 lot 的公式一致。
  `cutover_at` 读的是当前值：XM-INV-ELIG-POLICY-START-ANCHOR（design 3(D)）
  落地前，遗留账号的 `cutover_at` 仍可能是首个检查点时间而非真正策略起点。
- `consumed_since_start_minor`/`invoiceable_now_minor`/`issued_minor` 与用户
  自助摘要（"User summary"一节）的 `consumed_minor`/`available_minor`/
  `issued_minor` 是同一份公式（同样的过滤条件），不重新推导，SQL 文本是有意
  的重复；因为 `WALLET_CASH`/`SUBSCRIPTION_CASH` 这两种 `eligibility_kind`
  本就被 `enforce_funding_lot_invoice_policy`（迁移 0016）强制要求
  `completed_at>=`策略起点，这三个字段不需要也没有额外的 `>=cutover_at`
  过滤（与 `recharges_since_start_minor` 不同，后者需要，见上）。
- `threshold_reached` 复用真实的、管理员可配置的最低起票金额
  （`ledger.MinimumRequestMinor()`），不是硬编码常量。

### 详情：`GET /api/v1/admin/accounts/{external_account_id}/ledger`

列表字段基础上追加 `opening_balance_units`（期初余额，`{service_units,
unit_code}`，即账号自己的 `cutover_balance_units`/`unit_code`，沿用"服务
单位 + unit_code"表达，不假装非现金单位是人民币）、`recharges_since_start[]`
（`funding_lot_id`/`completed_at`/`amount_minor`/`eligibility_kind`/
`refund_frozen`，按 `completed_at` 升序，含退款冻结中的 lot——这类现金
确实到账了，只是暂不可开票）、`consumption_timeline`（按 Asia/Shanghai
日历日聚合的每日消耗，来自 `consumption_allocations` 关联 `source_usage_events`
与 `funding_lots`——现有表已支持，不需要新聚合表；"按检查点"聚合是另一个
可行选项，本实现选了"按天"，更符合运营按日核对的习惯）、`block_reason`
（详见下）、`last_reconciled_at`（最近一次评估为 `matched` 的时间，区别于
`last_checkpoint_at`——后者是最近一次收到任何检查点/结转证明的时间，可能
本身就是一次不匹配）。未知账号（不存在或格式不合法）返回 404，沿用既有
错误信封约定。

### `block_state`（四态，只读组合既有信号，不新增判定条件）

- `frozen_manual_review`：存在 `status='open'` 的 `eligibility_freezes`
  行，或者 `eligibility_status='frozen'`（即便查不到对应的开放冻结行——
  按构造不应发生，但不假设）。XM-INV-ELIG-AUTO-RECONCILE 与
  XM-INV-ELIG-QUEUE-NARROW 上线后，任何新产生的开放冻结都已经是design 3(C)
  "保留清单"里的人工复核原因（`SOURCE_REFUND`/精确的
  `LATE_FINALIZED_EVENT`/`SOURCE_GAP`/六种数据完整性原因），所以判定不再
  按 `freeze_reason` 白名单过滤——本任务的实现仍把这份白名单记录成一个带
  中文说明的 Go 常量（`accountLedgerFreezeReasonDescriptions`，
  `httpapi/accounts_ledger.go`），既满足"保留白名单以备查"的要求，也用来
  给 `block_reason` 生成具体的中文原因描述，包括两个历史遗留原因
  （`UNKNOWN_NEGATIVE_BALANCE`/`USAGE_EXCEEDS_LEDGER`——若队列收窄迁移工具
  还没在生产跑过，这两种旧冻结行仍可能存在）。
- `not_invoiceable_pending_reconciliation`：`eligibility_status`正是该值，
  或者存在 `eligibility_projection_jobs` 行（该表只在排队中/处理中/失败时
  才有行，成功后无条件删除，见 XM-INV-ELIG-AUTO-RECONCILE 的说明），或者
  最近一次余额评估状态不在 `matched`/`positive_classified_non_cash`/
  `positive_blip_ignored` 之内——**含"从未有过任何评估记录"**（此时判定为
  "不在白名单内"，与 `ResolveEligibilityFreeze`
  自己"未记录评估视为不匹配"的既有先例一致，不是本次新发明的读法）。
- `below_threshold`：以上都不成立，但 `invoiceable_now_minor` 低于当前
  配置的最低起票金额。
- `invoiceable`：以上都不成立。

### `block_reason`：具体中文原因，绝不是"数据异常"这类泛泛表述

只在详情端点返回（列表端点没有这个字段），`block_state=invoiceable`时为
`null`。时间一律换算为 Asia/Shanghai 展示；凡是已知为人民币分的金额（可
开票额、起票门槛、被红冲 lot 的已开票额）都换算成"元"两位小数（纯整数
运算，不经过浮点）；但对账检查点/结转证明的差额数字**不**换算成"元"——
`unit_code`（例如测试夹具里的 `SUB2_BALANCE_1E8`）是上游来源自己的记账
单位，这个系统里从未有过一个全局、已验证的"服务单位→人民币"换算系数
（`funding_lot_consumption_state`里的换算是逐笔 lot 各自计算的，不是一个
常数），编一个换算系数会是编造的精度——与"不假装非现金单位是人民币"这条
既有原则（用户摘要的 `legacy_noninvoiceable`/`noncash`）一致。这条是对
任务简报"金额一律按元两位小数"这句话的一处刻意收窄读法，记录在
`docs/handoffs/XM-INV-CR0009-LEDGER-VIEW.md`。

## "资格冻结"页签收窄（CR-0009 前端，纯客户端过滤，服务端契约不变）

`GET /api/v1/admin/eligibility-freezes`本身的查询参数、响应形状、解冻流程
（`POST .../resolve`）完全不变——CR-0009 明确"这是前端过滤条件收窄，不是
服务端契约变化"。默认只展示`isMechanicalReconciliationFreeze`判定为假的
记录（`web/src/lib/workflow.ts`）：隐藏
`UNKNOWN_NEGATIVE_BALANCE`/`USAGE_EXCEEDS_LEDGER`原因的记录，以及
`scope='account'`（泛化、非精确红冲）的`LATE_FINALIZED_EVENT`记录——这三类
在 XM-INV-ELIG-AUTO-RECONCILE/XM-INV-ELIG-QUEUE-NARROW 上线后都已经不会
再被新建，仍然出现的只可能是那两个切片上线前、或队列收窄迁移工具尚未在
生产跑过时遗留的历史行。页面右上角有一个"显示全部"复选框可以关闭这条
默认过滤，解冻流程本身不受影响（用户账本页签本身完全只读，不提供解冻
入口）。

## User summary

`GET /api/v1/user/eligibility-summary` returns one item per connected source:

- claimable `available_minor`, finalized `consumed_minor`, paid but unconsumed
  `unconsumed_minor`, `reserved_minor` and `issued_minor`, all explicitly CNY;
- `legacy_noninvoiceable` and `noncash` as `{service_units, unit_code}` rather
  than pretending quota/balance units are yuan;
- a closed reason list: `READY`, `BINDING_NOT_VERIFIED`, `ACCOUNT_FROZEN`,
  `PENDING_RECONCILIATION`, `PROJECTION_PENDING`, `SOURCE_NOT_READY`, or
  `NO_CONSUMED_CASH`.

Available CNY is reported as zero while binding, source health, freeze,
pending reconciliation or reprojection prevents a new invoice request.
Historical consumed/issued values remain visible for reconciliation.

## Automatic reconciliation (`not_invoiceable_pending_reconciliation`)

XM-INV-ELIG-AUTO-RECONCILE (2026-09-03) downgraded two freeze reasons that
were purely about reconciliation *timing*, not genuine data problems, from
the manual admin queue to an account-local, self-clearing state. Neither ever
creates an `eligibility_freezes` row, so neither ever appears in the
administrator queue above and neither is affected by "safe resolution":

- **A negative or otherwise unreconciled balance difference**
  (`UNKNOWN_NEGATIVE_BALANCE`, historically a freeze reason and still used as
  such for other trigger shapes -- see below). The account's
  `eligibility_status` becomes `not_invoiceable_pending_reconciliation` and
  `source_account_eligibility_state` carries five plaintext columns
  describing why: `pending_reconciliation_reason`,
  `pending_reconciliation_trigger_type`/`_trigger_id`,
  `pending_reconciliation_detail` (a human-readable sentence) and
  `pending_reconciliation_since`. The account auto-returns to `active` once
  two consecutive real balance-evidence items (a reconciliation checkpoint or
  carry-forward proof) evaluate `matched` -- a single reconciliation is not
  enough, to avoid masking a still-real gap with one lucky match. A genuinely
  frozen account (any other, real open freeze) is never downgraded into this
  state -- frozen always takes priority, and this state and `frozen` can
  never coexist on one account row.
- **Usage exceeding the ledger** (`USAGE_EXCEEDS_LEDGER`). This no longer
  freezes the account at all -- the invoiceable amount was always capped at
  what actually got allocated into a cash pool, so the unallocated overage
  was never invoiceable in the first place. It is recorded instead, on the
  same account row: `non_invoiceable_overage_units` and
  `non_invoiceable_overage_usage_event_id`, cleared automatically the next
  time reprojection no longer finds a shortfall. `USAGE_EXCEEDS_LEDGER`
  remains a valid `freeze_reason` value (for historical rows), but no code
  path opens a new freeze for it any more.

Both states are invisible to `/readyz` and to
`EligibilityProjectionHealth` by construction: entering or exiting either one
happens inside the same eligibility-projection job that, on success,
unconditionally deletes its own `eligibility_projection_jobs` row regardless
of which status the account ends up in -- there is no separate queue entry
for either state to get stuck in.

## Manual queue narrowing (XM-INV-ELIG-QUEUE-NARROW, 2026-09-03)

Two changes, together with the automatic reconciliation above, narrowed the
administrator freeze queue down to what genuinely needs a human: refund/
red-letter exposure (`SOURCE_REFUND`), a self-heal failure on a data gap
(`SOURCE_GAP`), a precise, funding-lot-scoped red-reversal
(`LATE_FINALIZED_EVENT` with a `funding_lot_id`), and the six data-integrity/
migration-period reasons (`EVENT_PAYLOAD_DRIFT`, `UNIT_MISMATCH`,
`AMBIGUOUS_EVENT_ORDER`, `STREAM_WATERMARK_REGRESSION`, `EVENT_DEAD`,
`POLICY_ANCHOR_BLOCKED`), which are unaffected by this slice.

- **A late-arriving fact no longer freezes the whole account by default.**
  Previously, any fact (a usage or credit event) observed after the account's
  own `finalized_through` watermark had already passed it froze the entire
  account (`LATE_FINALIZED_EVENT`, no `funding_lot_id`) purely as a defensive
  measure, regardless of whether the resulting reprojection actually caused
  any problem. That generalized freeze is gone: the late fact still triggers
  a full reprojection (`eligibility.late_fact.reprojected` audit event, no
  freeze), and reprojection's own, unchanged, precise check still opens a
  `funding_lot`-scoped `LATE_FINALIZED_EVENT` freeze exactly when a late fact
  genuinely causes a red-reversal -- an already-issued invoice's recognized
  amount dropping below what was issued on that lot. That real, still-manual
  case is untouched.
- **`invoice-eligibility-repair --kind=queue-narrow`** migrates every
  still-open freeze this slice and XM-INV-ELIG-AUTO-RECONCILE made obsolete:
  `UNKNOWN_NEGATIVE_BALANCE`, `USAGE_EXCEEDS_LEDGER`, and a
  `LATE_FINALIZED_EVENT` freeze with no `funding_lot_id` (the generalized
  form removed above). Each matching freeze is resolved with the fixed note
  "由 XM-INV-ELIG-SIMPLIFY 迁移自动解除", using the same column shape the
  interactive resolve endpoint writes. For the first two reasons, the tool
  does not simply clear the account: it rebuilds `not_invoiceable_pending_reconciliation`
  from the account's latest real balance evaluation if that evaluation still
  shows a negative difference (so an account still genuinely unreconciled is
  never misreported "resolved"), and reprojects so the usage-overage columns
  reflect the current projection. An account returns to `active` only when no
  open freeze remains at all, through the same rules the interactive resolve
  endpoint uses -- never forced. Unlike the other three `eligibility-repair`
  kinds, this one processes one account per transaction rather than the whole
  run in one transaction: a single account's own data inconsistency is
  reported as a per-account error and does not block any other account in
  the same run.

  ```
  # dry run (default) -- reports what would change, writes nothing
  /app/bin/invoice-eligibility-repair \
    --database-url-file=/run/secrets/invoice-db-url \
    --field-keyring-file=/run/secrets/field-keyring.json \
    --kind=queue-narrow

  # apply -- requires an approving operator id
  /app/bin/invoice-eligibility-repair \
    --database-url-file=/run/secrets/invoice-db-url \
    --field-keyring-file=/run/secrets/field-keyring.json \
    --kind=queue-narrow --apply --operator-id=<admin-uuid>
  ```
