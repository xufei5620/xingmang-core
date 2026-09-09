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

  XM-INV-NEGATIVE-DEFICIT (2026-09-06) narrowed what counts as "unreconciled"
  for a **negative** upstream balance. The bridge now reports the magnitude
  of a negative balance (`deficit_service_units`) beside the flag, and the
  evaluator compares it with every unit of usage the projection could not
  charge to any pool by that moment (the carried cash debts plus
  non-invoice-eligible shortfalls, summed; `non_invoiceable_overage_units`
  still names only the oldest of them). Equal
  magnitudes evaluate `matched`: the account burned to zero and its last
  request overdrew by exactly that much, which is the ledger and the source
  agreeing, so the account stays `active` (and a pending account counts one
  consecutive match). A `negative_frozen` evaluation therefore now has three
  possible readings, and `pending_reconciliation_detail` says which:
  "reported balance -X, expected -Y (difference D)" is a genuine gap in
  either direction; "reported a negative balance of unknown magnitude" is
  evidence sealed before the bridge learned to report deficits (or a bridge
  that has not had `install-economic` re-run); and the account-bootstrap
  wording is an opening negative balance, which no projection can explain.
- **Usage exceeding the ledger** (`USAGE_EXCEEDS_LEDGER`). This no longer
  freezes the account at all -- the invoiceable amount was always capped at
  what actually got allocated into a cash pool, so the unallocated overage
  was never invoiceable in the first place. It is recorded instead, on the
  same account row: `non_invoiceable_overage_units` and
  `non_invoiceable_overage_usage_event_id`, cleared automatically the next
  time reprojection no longer finds a shortfall. `USAGE_EXCEEDS_LEDGER`
  remains a valid `freeze_reason` value (for historical rows), but no code
  path opens a new freeze for it any more.

Neither state can get *stuck* in `/readyz` or `EligibilityProjectionHealth`:
entering or exiting either one happens inside the same eligibility-projection
job that, on success, unconditionally deletes its own
`eligibility_projection_jobs` row regardless of which status the account ends
up in -- there is no separate queue entry for either state to sit in.

Since XM-INV-PENDING-RECON (2026-09-09) they are no longer entirely invisible
there, and the difference matters when reading a health snapshot. A
`not_invoiceable_pending_reconciliation` account that is one matched
evaluation short of auto-exit gets a projection job enqueued by each
finalization pass that publishes a balances cycle it could still derive
evidence from, and `invoice-eligibility-repair --kind=pending-reevaluate`
enqueues one on demand. So `Queued`/`OldestPending` can show 1 or 2 for a few
seconds. The worker takes 25 jobs every two seconds and
`eligibility_projection_stuck` only fires after 15 minutes, so a healthy
system clears these long before readiness notices. A pending-reconciliation
account sitting in `Queued` for minutes is a real signal, not the expected
state.

### Idle re-evaluation (XM-INV-PENDING-RECON, 2026-09-09)

Auto-exit needs two consecutive matched evaluations of real balance evidence,
and an account with nothing happening to it produces neither kind: the source
agent emits a checkpoint only when the balance, negative flag or deficit
changes, and a carry-forward proof is derived only at the visibility instant
of a real fact. A production account sat one match short for days while every
finalization pass advanced `finalized_through` past the published balances
cycles a proof could still have come from.

Now, when such an account is one match short and the window carries no facts
at all, the projection job derives one carry-forward proof from the newest
published balances cycle that can still take one, and the ordinary evaluator
judges it. Nothing about the exit rule changes: the proof restates the latest
real checkpoint verbatim (migration 0026's trigger enforces that, deficit
included), it is one evidence item, and it counts as one. The acceptance line
accepted on 2026-09-09 that for an idle account the second of the two matches
is a restatement of the first.

Two things suppress the derivation entirely, and both are deliberate: an open
`eligibility_freezes` row (the exit would be blocked by the freeze guard
anyway, and a proof is immutable once written), and a `balance_checkpoint`
event that is dead or failed in that same cycle (XM-INV-DEAD-CONTAINMENT --
writing a proof there would make migration 0014 refuse the real checkpoint
forever). The audit row for an idle derivation carries
`idle_reevaluation: true`.

**`invoice-eligibility-repair --kind=pending-reevaluate`** is the on-demand
form, for when an account has not cleared on its own. It asks the projection
worker to look at that one account again, now, by enqueueing its projection
job -- and that is all it writes. No proof, no evaluation, no
`eligibility_status`: the worker re-derives and re-evaluates through the
ordinary path, so the two-consecutive-matches exit rule is untouched by it.
Its `--apply` writes exactly one job row and one
`eligibility.pending_reconciliation.reevaluation_requested` audit event.

`--account` is required and names exactly one account. The dry run is the
diagnosis and is usually the whole answer: the account's state and streak,
open freezes, its job row, evidence already waiting for the evaluator, which
published cycle an idle derivation would take, and this run's own recomputed
verdict for the evidence it would produce -- computed from the ledger, never
read back from a stored evaluation row. Checks printed as `STOP` refuse the
apply: there is no eligibility state row for the account at all, the account
is not pending, an open freeze would block the exit anyway, the job is
`processing` or `dead` (`dead` is `--kind=projection-requeue-dead`'s decision,
and this tool never revives one), the requeue window is empty, no cycle inside
the window can be derived from, the one that can already carries a proof or a
stranded checkpoint, the checkpoint to be restated has no magnitude, or the
operator is the account's own invoice user. `docs/PRODUCTION-RUNBOOK.md` has
the full table with what to do about each.

The command is a `docker run` against the tools image -- there is no compose
service for this binary. `docs/PRODUCTION-RUNBOOK.md` defines the
`invoice_eligibility_repair` shell function used below (image tag, the
`invoice-system-prod_invoice_db` network, and the two read-only secret
mounts); copy that block first.

```bash
# dry run (default) -- the diagnosis, writes nothing
invoice_eligibility_repair --kind=pending-reevaluate --account=<external-account-uuid>

# apply -- requires an approving operator id, and refuses if any check says STOP
invoice_eligibility_repair --kind=pending-reevaluate --account=<external-account-uuid> \
  --apply --operator-id=<admin-uuid>
```

Read the report's `requeue window` line before applying even when nothing says
`STOP`. The apply asks for the window a finalization pass would ask for --
min(the source's four stream watermarks) minus the account's
`finalization_delay_seconds`, and never lowers a window an existing job row
already asks for -- and the derivation only happens inside it. When `newest
published` is above that window the account is just waiting for the
finalization delay to pass, and the automatic path will reach it anyway.

> The older snippets in this file (queue-narrow, policy-start-reanchor,
> projection-requeue-dead, ingest-requeue-dead,
> ingest-acknowledge-unreplayable) still show `/app/bin/...` with
> `/run/secrets/invoice-db-url` and `field-keyring.json`. Those paths do not
> exist: the binary is at `/usr/local/bin/invoice-eligibility-repair`, and the
> compose secrets are `invoice_owner_database_url` and
> `invoice_field_keyring`. Use the runbook's form for every kind until those
> snippets are corrected.

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

## Policy-start anchoring (XM-INV-ELIG-POLICY-START-ANCHOR, 2026-09-03)

XM-INV-ELIG-POLICY-START-ANCHOR (design XM-INV-ELIG-SIMPLIFY section 3(D))
changed how a `POLICY_ANCHOR` account's `cutover_at` is set: it is now
unconditionally the global invoice policy start (`invoice_eligibility_policy.
eligibility_start_at`), not the triggering reconciliation checkpoint's own
`as_of`. `cutover_balance_units` is derived by unwinding that checkpoint's
observed balance backward across the window using whatever credit/usage
facts are already persisted at bootstrap time, and a second, derived
reconciliation checkpoint row is inserted at the policy start so migration
0016/0021's anchor-checkpoint validation still holds. This closes a dead
zone between the policy start and an account's first observed checkpoint:
facts, and cash payments, dated inside that window are now persisted and
projected normally instead of being permanently excluded.

Every account bootstrapped by the fixed code already gets this correctly.
The three accounts bootstrapped by the pre-fix code (`40bd883d-...`,
`98cce4c8-...`, `6706ea6a-...`) still carry the old, later `cutover_at` and
need a one-time re-anchor.

**`invoice-eligibility-repair --kind=policy-start-reanchor`** re-anchors
those accounts. For each `POLICY_ANCHOR` account whose `cutover_at` is still
after the policy start, it checks for a verified, non-refund-frozen
`WALLET_CASH` funding lot with `completed_at` in the window between the
policy start and the account's current `cutover_at` -- exactly the
predicate `buildEligibilityProjectionTx`'s own cash-pool query uses, so this
precisely predicts whether re-anchoring changes anything:

- **Zero such lots (expected for all three accounts today):** a deliberate,
  reported no-op in both dry-run and apply -- nothing is written. Moving
  `cutover_at` back with no invoiceable amount to gain is pure downside
  risk; the usage/credit facts that fell in this window before the fix are
  already unrecoverable (never persisted), so only a real cash payment
  makes re-anchoring worth doing at all.
- **A real in-window lot found (unexpected -- production has been verified
  to have none as of this writing):** apply inserts the derived
  reconciliation checkpoint, moves `cutover_at`/`cutover_balance_units` via
  the guarded UPDATE migration 0021 adds, resets and rebuilds the account's
  consumption state, and queues a projection job so the balance evaluator
  picks up the derived checkpoint on its next run. Any resulting negative
  difference or usage overage flows through the existing automatic
  reconciliation/overage recording above, never a freeze.
- **A live invoice reservation, issuance or refund-review exists on any of
  the account's funding lots:** reported `Blocked`, left completely
  untouched in either mode (mirrors design XM-INV-POLICY-ANCHOR 2.4's own
  guard in `reanchorLegacyEligibilityAccountTx` -- resetting consumption
  state under real invoice exposure would corrupt accounting already
  depending on it). Re-run the tool later once the exposure clears.

Like `--kind=queue-narrow`, this processes one account per transaction, not
the whole run in one transaction.

```
# dry run (default) -- reports what would change, writes nothing
/app/bin/invoice-eligibility-repair \
  --database-url-file=/run/secrets/invoice-db-url \
  --field-keyring-file=/run/secrets/field-keyring.json \
  --kind=policy-start-reanchor

# apply -- requires an approving operator id
/app/bin/invoice-eligibility-repair \
  --database-url-file=/run/secrets/invoice-db-url \
  --field-keyring-file=/run/secrets/field-keyring.json \
  --kind=policy-start-reanchor --apply --operator-id=<admin-uuid>
```

Production sequence: apply migration 0021 first (it must be live before the
repair runs, since the repair's own guarded UPDATE depends on the sibling
GUC it adds), then dry-run, then owner-approved apply. Expect an
all-no-op dry-run report for the three known accounts unless a real
in-window payment has appeared since this was last checked.

## Eligibility-projection failure grading (XM-INV-PROJECTION-FAILURE-GRADING, 2026-09-03)

Before this slice, `ProcessEligibilityProjectionJobs` marked any per-account
error other than a pending balance proof `status='failed'` immediately --
no attempt counter, no backoff, no terminal grade -- and
`EligibilityProjectionHealth`/`eligibilityProjectionReady` turned `/readyz`
503 the instant any row carried that status. A single account hitting a
transient error (a serialization failure, a momentary database error, a
one-off evaluator bug) took the whole API not-ready until a human
intervened. This is the same production incident mechanism as RC75's
balance-blip loop (`docs/handoffs/XM-INV-BLIP-SOFTFAIL.md`), now fixed at
its structural root rather than only for that one bug class.

`eligibility_projection_jobs` (migration 0023) gained the same retry/dead
shape `source_ingest_events` already has via `MarkSourceEventFailed`:

- `attempts` (new column, distinct from the pre-existing `attempt_count`,
  which the worker's claim step increments on every claim regardless of
  outcome) counts consecutive per-account processing errors. A balance-proof-
  pending outcome (`errBalanceCarryForwardProofPending`) is a separate,
  unrelated backoff and never spends an attempt.
- Below 8 consecutive failures (`projectionFailureDeadThreshold`,
  mirroring `source_ingest_events`' own `attempt_count>=8` threshold), the
  job stays `status='queued'` with an exponential backoff -- 30s doubling,
  capped at 30 minutes (`projectionFailureBackoffBaseSeconds`/
  `projectionFailureBackoffCapSeconds`) -- and `last_error`/`last_error_code`
  record what happened.
- At the threshold the job becomes a terminal `status='dead'` and an audit
  event (`eligibility.projection.dead`, carrying the account id, `attempts`
  and the last error) is written. `status='failed'` stays a valid value for
  compatibility with any row already in that status at deploy time (the
  worker's claim query still picks those up and grades them going forward)
  but the code no longer produces it.
- A successful run still deletes the job row outright, unchanged from
  before -- the account's next error, if any, starts a fresh `attempts=0`
  row.
- A new fact for a dead account (a usage/credit/checkpoint observation, or
  the source-projection worker's own bulk requeue) never silently revives
  it -- only `requested_through` still advances, so the account's full
  backlog is picked up the moment it *is* requeued. The only way a dead job
  returns to the queue is `invoice-eligibility-repair
  --kind=projection-requeue-dead` below.

`EligibilityProjectionHealth` (surfaced at
`GET /api/v1/admin/source-health`'s `eligibility_projection` object,
alongside the five source streams that endpoint already reports) exposes:

- `Dead` (renamed from `Failed`) -- `status='dead'` count. This is the only
  eligibility-projection condition `eligibilityProjectionReady` treats as
  not-ready.
- `Retrying` -- `status='queued' AND attempts>0` count. Purely
  informational; never affects readiness by itself. It can overlap with
  `ProofPending` for a job that failed before and later separately hit a
  proof-pending outcome (attempts is not reset by that outcome) -- both
  counts then legitimately include that one row.
- `OldestPending` (the 15-minute stuck-job budget) excludes a
  failure-grading job whose own backoff has not elapsed yet, the same
  treatment `BALANCE_PROOF_PENDING` jobs and live-lease `processing` rows
  already got (XM-INV-READY-PENDING/XM-INV-READY-LEASE) -- a job legitimately
  waiting out its own backoff is not stuck.

**`invoice-eligibility-repair --kind=projection-requeue-dead`** lists every
dead job (optionally narrowed with `--account`) with its previous attempt
count, last error code/text and how long it has been dead; apply resets
`attempts=0`, `status='queued'`, `next_attempt_at=now()` and writes an audit
event (`eligibility.projection.requeued`). Like `--kind=queue-narrow`, this
processes one account per transaction, so one account's own conflict never
blocks any other account in the same run.

```
# dry run (default) -- reports what would change, writes nothing
/app/bin/invoice-eligibility-repair \
  --database-url-file=/run/secrets/invoice-db-url \
  --field-keyring-file=/run/secrets/field-keyring.json \
  --kind=projection-requeue-dead

# apply -- requires an approving operator id
/app/bin/invoice-eligibility-repair \
  --database-url-file=/run/secrets/invoice-db-url \
  --field-keyring-file=/run/secrets/field-keyring.json \
  --kind=projection-requeue-dead --apply --operator-id=<admin-uuid>

# narrow to one account
/app/bin/invoice-eligibility-repair \
  --database-url-file=/run/secrets/invoice-db-url \
  --field-keyring-file=/run/secrets/field-keyring.json \
  --kind=projection-requeue-dead --account=<external-account-uuid> \
  --apply --operator-id=<admin-uuid>
```

Before requeuing a dead job, check `last_error`/`last_error_code` in the
dry-run report -- requeuing an account whose underlying data problem was
never actually fixed just spends another 8 attempts (roughly 63 minutes of
backoff) before it goes dead again.

## Dead source ingest events (XM-INV-DEAD-REQUEUE, 2026-09-08)

`source_ingest_events` has the same dead-letter grade one layer further up:
`MarkSourceEventFailed` marks a row `processing_status='dead'` on its eighth
consecutive attempt, `SourceIngestHealth.Dead>0` fails `/readyz`, and (when
the account can be correlated) `MarkSourceEventFailed` also opens an
`EVENT_DEAD` freeze so the account stops being invoiceable while one of its
facts is missing.

A dead ingest row still carries its original encrypted payload, so the fact
it was carrying is recoverable -- but only by putting the row back on the
queue, which nothing does automatically. `ClaimUnprocessedSourceEvents`
filters `attempt_count < 8`, so a dead row is invisible to the worker for
good.

**`invoice-eligibility-repair --kind=ingest-requeue-dead`** lists every dead
ingest row (optionally narrowed with `--event`, or with `--account`) with its
attempt count, error, when it was created and how long it has been dead, the
economic scan cycles it belongs to *and their current status*, and the open
freezes correlated to its payload hash. Apply resets
`processing_status='queued'`, `attempt_count=0`, `next_attempt_at=now()`,
clears the lease and the stale `processing_error`, and writes one
`source_ingest_event.repair_requeued` audit event per row. One transaction
per event, so one row's conflict never blocks another row in the same run.

**It refuses, by default, to requeue an event whose replay cannot succeed.**
A replay is verified against one exact `(event_id, batch_id, scan_cycle_id)`
triple -- the one `ClaimUnprocessedSourceEvents` would resolve. Since
XM-INV-CLAIM-BINDING that is no longer `first_batch_id`: the claim takes the
event's newest economic binding whose cycle is
`receiving`/`processing`/`published`, and since XM-INV-BINDING-SKEW it prefers,
among those, one whose batch `scan_ceiling_at` is within five minutes
(`factClockSkewTolerance`) of the event's own `observed_at`. Only when the
event has no valid binding at all does it fall back to `first_batch_id`. The
tool resolves that same triple by embedding the claim's own SQL, calls it the
**replay binding**, and prints it on its own line.

Four things make a replay hopeless, and the tool reports each with the name of
the function that would refuse it:

- the binding carries no scan-cycle mapping (`verifyFactBatchContextTx`,
  `ErrForbidden`);
- its `payload_hash` does not match the event's (`verifyFactBatchContextTx`,
  `ErrConflict`);
- its cycle is not `receiving`/`processing`/`published` -- usually `blocked`
  (`verifyFactBatchContextTx`);
- its batch `scan_ceiling_at` runs more than five minutes past the event's
  `observed_at`, so `validateFactMetadata` refuses the fact with
  `source fact event time/watermark is invalid` *before* the verifier is
  reached at all. That reason quotes the message verbatim, so it can be
  grepped against the projection log. It applies on `usage`, `credits` and
  `balances`; a `payments` event is written by `ObserveFundingLot`, which
  applies no clock rule to a fact, and is never reported this way.

Such an event is listed as `REQUEUED false (replay blocked)` with a
`NOT REQUEUED:` line naming the reason, and counted under
`not requeued (replay blocked)` rather than `total requeued`.
`--include-blocked-cycles` overrides that; it is for deliberately reproducing
the failure, never a fix.

An event can be mapped to several cycles: agent event ids are deterministic
(`agents/sourceagent/batch.go`), so a rescan re-delivers the identical event
id and `CommitSourceBatch` adds a fresh mapping under the new cycle while
leaving the existing ingest row -- `dead` included -- untouched. Every mapping
is printed, labelled `replay binding` or `other binding, NOT used by replay`.
**A healthy cycle among the other bindings does not make the event
replayable**; only the replay binding governs. This is not hypothetical: the
2026-09-08 production dry run showed exactly that pair, and reading the
healthy sibling as the verdict would have been wrong.

It deliberately changes nothing else:

- **Freezes are left open.** The freeze is what lets
  `tryPublishEconomicScanCyclesTx` treat a dead event as complete; resolving
  it here would leave a dead-again event with no freeze, which holds its scan
  cycle open and, through `source_economic_one_active_scan_cycle`, wedges the
  stream. Resolve the freeze afterwards through the normal admin path, which
  refuses to run until the account has no projection job left and its latest
  finalized balance evidence evaluates `matched` -- that gate is exactly what
  makes "requeue first, resolve second" the only valid order.
- **Scan cycles are read, never written.** An already-`published` replay
  binding is safe: that cycle is never re-evaluated, and
  `verifyFactBatchContextTx` still accepts a fact whose cycle has published,
  so the fact lands as a late fact and reprojects. A binding still in
  `processing` *does* send that cycle back to incomplete until the requeued
  event terminates, in both directions -- read the `replay_binding` line
  before applying. Anything else is the refusal above.
- **`created_at` is left alone.** Readiness ages a pending event from
  `created_at`, so a long-dead row requeued here is immediately "old":
  `/readyz` stays 503 with a *different* reason (`source ingestion processing
  is unhealthy` instead of `contains dead events`) until the event actually
  reaches `processed`. That is accurate, not a regression.

```
# dry run (default) -- reports what would change, writes nothing
/app/bin/invoice-eligibility-repair \
  --database-url-file=/run/secrets/invoice-db-url \
  --field-keyring-file=/run/secrets/field-keyring.json \
  --kind=ingest-requeue-dead

# apply one specific, individually reviewed event
/app/bin/invoice-eligibility-repair \
  --database-url-file=/run/secrets/invoice-db-url \
  --field-keyring-file=/run/secrets/field-keyring.json \
  --kind=ingest-requeue-dead --event=<ingest-event-uuid> \
  --apply --operator-id=<admin-uuid>

# narrow to one account (correlated through that account's open freezes)
/app/bin/invoice-eligibility-repair \
  --database-url-file=/run/secrets/invoice-db-url \
  --field-keyring-file=/run/secrets/field-keyring.json \
  --kind=ingest-requeue-dead --account=<external-account-uuid> \
  --apply --operator-id=<admin-uuid>
```

`--account` correlates through open `eligibility_freezes` rows whose
`source_revision_hash` equals the event's `payload_hash` -- the only account
attribution the ingest layer has, since the payload is ciphertext. A dead
event whose account was never frozen is invisible to `--account`; run
unnarrowed, or use `--event`, to reach it.

As with `projection-requeue-dead`: read the reported `processing_error`
first. If the underlying cause is not fixed, the row spends eight fresh
attempts (five minutes apart, so roughly 35 minutes) and goes dead again --
reusing the same still-open freeze rather than opening a second one, so the
system lands back exactly where it started. Re-running the tool later is
safe and idempotent.

### When the replay binding is blocked, there is nothing to run

A `blocked` replay binding is almost always a superseded cycle: an agent
restart abandoned an in-flight scan, `supersedeStaleActiveScanCycleTx` marked
the abandoned cycle `blocked`, and any of its events that had not finished are
pinned to it for as long as they have no other binding that is valid in both
status and time. (Before XM-INV-CLAIM-BINDING they were pinned to it forever,
because `first_batch_id` never moves. That is no longer the rule; the paragraph
above says what the rule is.)

There is no supported repair for that state, and none should be invented:

- **The mapping table cannot be edited.** `source_economic_scan_cycle_events`
  has exactly one writer, `CommitSourceBatch`, inside the transaction that
  verifies the batch's hash chain and signing key. A hand-written row asserts
  a delivery that never happened. On the `balances` stream it is also
  destructive: `tryPublishEconomicScanCyclesTx` requires the cycle's
  `balance_checkpoint` mapping count to equal the agent-signed
  `scan_snapshot_row_count`, so one extra row blocks the cycle and opens a
  `SOURCE_GAP` freeze on *every* account of that source.
- **A blocked cycle cannot be revived.** No code path moves `cycle_status`
  out of `blocked`, and un-blocking one would make it active again, colliding
  with `source_economic_one_active_scan_cycle` against the live cycle and
  wedging the stream -- the very failure the supersede machinery exists to
  prevent. `superseded_by_scan_cycle_id` is a forensic breadcrumb, never read
  by any code.
- **The supported recovery is upstream, through the agent.** Because event ids
  are deterministic, a rescan that reads the same upstream record re-delivers
  the same event id into a healthy cycle and creates a valid mapping. That
  already works for the mapping. Since XM-INV-CLAIM-BINDING the claim path
  follows it too: `ClaimUnprocessedSourceEvents` prefers the event's newest
  *currently valid* binding over `first_batch_id`, so an event whose evidence
  is already on file is replayable again. Only an event with **no** valid
  binding at all is genuinely stuck.

  **"Currently valid" includes time.** Since XM-INV-BINDING-SKEW the claim
  prefers a re-delivery whose batch `scan_ceiling_at` is within
  `factClockSkewTolerance` (5 minutes) of the event's `observed_at`. That
  column is frozen at the event's *first* delivery and never rewritten, and on
  `usage`/`credits`/`balances` `validateFactMetadata` rejects a wider gap
  outright -- before the fact-context verifier is even reached -- so a late
  binding is not a rescue there, it is eight guaranteed refusals. Until
  XM-INV-OBSERVED-AT-PER-BINDING (L2) lets a re-delivery carry its own
  observation, this recovery works for a prompt agent restart and **not** for
  an event parked for hours: a rescan that arrives more than five minutes after
  the event was first observed does not rescue a usage, credit or balance
  event. For those, `ingest-requeue-dead` reports `ReplayBlocked=true` naming
  `validateFactMetadata`.

  It is a preference and not a filter, which matters on `payments`:
  `ObserveFundingLot` applies no clock rule to a fact, so a late re-delivery
  really does rescue a payment or refund, and the claim still selects it when
  the event has nothing better. A `payments` event is never reported as
  clock-skew blocked.

  **Before writing one of those off, read the entity type.** A missing `usage`
  fact under-states what the customer consumed -- conservative, it cannot
  over-invoice. A missing `credits`, refund or `balances` fact runs the other
  way: it over-states what the customer paid for, which is the over-invoicing
  direction, and needs the owner's sign-off rather than an operator's judgement
  (design `XM-INV-STRANDED-EVENTS-DESIGN.md` §L3-A7). And an event blocked
  *only* by clock skew is exactly the class L2 exists to make replayable again:
  acknowledging it sets `processing_status='processed'` and no later rescan
  will ever pick it up, so a write-off today irreversibly discards a fact L2
  would have recovered. Prefer waiting for L2 unless `/readyz` is latched and
  the event is holding customers out. See
  `docs/handoffs/XM-INV-DEAD-REQUEUE.md`,
  `docs/handoffs/XM-INV-CLAIM-BINDING.md` and
  `docs/handoffs/XM-INV-STRANDED-EVENTS-DESIGN.md`.

### Acknowledging a fact that can never be replayed

Resolving the account's freeze does **not** clear `/readyz`:
`validateSourceIngestRuntimeReadiness` fails first on `Dead > 0`, and a
permanently unreplayable row stays `dead` forever. Deleting it is blocked by
the `source_economic_scan_cycle_events` foreign key (`ON DELETE RESTRICT`) and
would destroy the evidence of what was lost.

**`invoice-eligibility-repair --kind=ingest-acknowledge-unreplayable`** gives
such a row a non-dead terminal state without asserting anything untrue. It
uses exactly the column shape `RequeueSourceDependency` already uses for facts
that will never be applied (`PRE_POLICY_SKIPPED`):
`processing_status='processed'` with `processed_at` set, lease and dependency
columns cleared, and a marker in `processing_error` -- here
`UNREPLAYABLE_BINDING`. **It does not claim the fact was applied; it records
that it never will be.** Nothing is written to `source_usage_events`,
`source_credit_events` or `balance_reconciliation_checkpoints`, and no
watermark or scan cycle moves.

Guardrails, because this writes off customer data:

- `--event` is **required**. There is no bulk mode: each acknowledgement is a
  separate admission that one specific fact is gone.
- Default dry run; `--apply` requires `--operator-id`.
- One audit event (`source_ingest_event.unreplayable_acknowledged`) per
  acknowledged row, carrying the previous state, the reason the replay is
  impossible, and `fact_applied: false`.
- It **refuses** an event that still has a binding the verifier would accept,
  and points at `--kind=ingest-requeue-dead` instead. A replayable event has a
  repair; it does not have a write-off.
- Open freezes are untouched: closing the event is not the same judgment as
  declaring the account clean.

```
# dry run (default) -- reports the event and why it is unreplayable
/app/bin/invoice-eligibility-repair \
  --database-url-file=/run/secrets/invoice-db-url \
  --field-keyring-file=/run/secrets/field-keyring.json \
  --kind=ingest-acknowledge-unreplayable --event=<ingest-event-uuid>

# apply -- one reviewed event, with a named operator
/app/bin/invoice-eligibility-repair \
  --database-url-file=/run/secrets/invoice-db-url \
  --field-keyring-file=/run/secrets/field-keyring.json \
  --kind=ingest-acknowledge-unreplayable --event=<ingest-event-uuid> \
  --apply --operator-id=<admin-uuid>
```

Afterwards, resolve the account's freeze through the normal admin path once
its projection queue is empty and its *latest* finalized balance evidence
evaluates `matched`. That gate reads only the latest evidence item, so a
permanently missing older checkpoint does not block reopening the account.

### Repair before unfreeze is now a rule, not an ordering accident

XM-INV-DEAD-CONTAINMENT added an explicit precondition to
`POST /api/v1/admin/eligibility-freezes/{id}/resolve`:

> While any `source_ingest_events` row on the freeze's own source instance is
> `processing_status='dead'` and carries the freeze's `source_revision_hash` as
> its `payload_hash`, the freeze cannot be resolved. The API answers
> `409 ELIGIBILITY_DEAD_EVENT_UNREPAIRED` with the message *"a dead source
> event still correlates to this freeze; requeue or acknowledge it first"*.

This used to hold by accident. A dead event made every stream not-ready, so the
five-stream freshness gate rejected the resolution first, with
`503 ELIGIBILITY_SOURCE_STALE`. Containment removes that side effect on purpose
— which is exactly why the rule now has to be stated: an open freeze is the
only thing keeping a dead event from taking the whole source instance away from
every other account again, and one tidy-up resolution would undo it.

The guard is deliberately narrow. It matches on `source_revision_hash` alone,
**not** on `freeze_reason` — an `EVENT_PAYLOAD_DRIFT` freeze and an
`EVENT_DEAD` freeze can both be open for one payload, and resolving either one
equally removes containment. And it blocks only on `dead`, not on every
not-yet-processed status: a requeued event that lands in `waiting_dependency`
or `parked_identity` may never become `processed`, and a freeze that could
never be resolved again is a worse failure than the one being prevented. A dead
event always has two exits, both of which end the refusal:

- `invoice-eligibility-repair --kind=ingest-requeue-dead` — the event replays
  and reaches `processed`. **Read the warning in
  `docs/PRODUCTION-RUNBOOK.md` first: requeueing opens an
  `EVENTS_PENDING` window of up to ~40 minutes during which the entire source
  instance is unavailable to every account.**
- `invoice-eligibility-repair --kind=ingest-acknowledge-unreplayable` — the
  event is written off as `processed` / `UNREPLAYABLE_BINDING`, with no pending
  window. It refuses events that could still replay.

**Every door passes through this guard — there is no exemption list.** Five
pieces of code write `eligibility_freezes.status='resolved'`, and all five ask
the same question first:

| Door | What it resolves | What a refusal does |
| --- | --- | --- |
| the admin freeze queue (`POST /api/v1/admin/eligibility-freezes/{id}/resolve`) | one freeze | `409 ELIGIBILITY_DEAD_EVENT_UNREPAIRED` |
| `invoice-eligibility-repair --kind=preanchor-usage` | `SOURCE_GAP` on usage/credit plus the correlated `EVENT_DEAD` freezes | aborts the run, nothing written |
| `invoice-eligibility-repair --kind=anchor-balance` | `SOURCE_GAP` on `balance_checkpoint` / `balance_carry_forward_proof` | aborts the run, nothing written |
| `invoice-eligibility-repair --kind=balance-blip` | `UNKNOWN_NEGATIVE_BALANCE` | aborts the run, nothing written |
| `invoice-eligibility-repair --kind=queue-narrow` | `UNKNOWN_NEGATIVE_BALANCE`, `USAGE_EXCEEDS_LEDGER`, lot-less `LATE_FINALIZED_EVENT` | that one account is reported in the run's error list; every other account still repairs |

Three of the four repair tools resolve freezes that carry a
`source_revision_hash` and never read `source_ingest_events` at all, so without
this guard a routine migration run un-contained a dead event with no error and
no symptom — until the source instance went unavailable to everyone again, or,
for a `balance_checkpoint`, until a real balance fact had been permanently
refused (see the next section). `queue-narrow` is the widest: one run resolves
every matching freeze on every candidate account.

`preanchor-usage` is the one door that already established the ordering by
itself, since it requeues the correlated events in the same transaction. It
still asks, and it has to: it only requeues dead/failed `usage_event` /
`credit_event` rows, so a correlated dead event of any other entity type is one
it cannot repair and must not resolve around. Its requeue now runs ahead of its
resolutions so the guard sees the repaired events.

An earlier version of this section named one exemption and asserted the other
doors were safe. That survey was done by reading, and it missed three tools.
What keeps this list honest is not the table above but
`TestEveryFreezeResolutionPassesTheDeadEventGuard`, which reads every Go source
under `backend/` and fails on any declaration that resolves a freeze without
calling the shared guard. A sixth door is allowed to exist; it is not allowed
to skip the question.

### A contained `balance_checkpoint` parks the account's projection

While a dead `balance_checkpoint` event is contained by an open freeze, that
account's eligibility projection job stops with
`last_error_code='BALANCE_PROOF_PENDING'` instead of writing a carry-forward
proof for the cycle the checkpoint belongs to.

This is intended. Containment lets the balances scan cycle publish, and the
projection worker runs every two seconds — so without the wait it would write
an immutable "the balance did not change in this cycle" proof within seconds,
and migration 0014's `reject_real_checkpoint_after_carry_forward` trigger would
then refuse the real checkpoint permanently. A replayable balance fact would be
lost as a side effect of a decision about something else.

What an operator sees, and what to do:

- `/readyz` is unaffected: the `eligibility_projection_stuck` check excludes
  proof-pending jobs by construction. After an hour the api log carries
  `msg="invoice eligibility projection has balance-proof-pending jobs waiting a
  long time"`.
- The parked job also means `ResolveEligibilityFreeze` returns
  `409 ELIGIBILITY_PROJECTION_PENDING` for that account. This is a second gate,
  not a deadlock: repair the event (either exit above), the wait ends, the
  proof is written, the job completes, and the freeze becomes resolvable.

Note that the two "contained" judgments differ on purpose. Stream health asks
only whether *some* open freeze holds the payload — deliberately account-blind,
because the question is whether anyone is accountable at all. The carry-forward
wait asks whether *this account* has one, because the proof it is about to
write is per account and per cycle. Both are pinned by tests; changing either
without the other will turn one of them red.

There is a third case, and it is the one that made this a data-loss risk rather
than an inconvenience: a stranded checkpoint that **no** open freeze answers
for. Nothing then says whose checkpoint it is, and assuming it is not this
account's is the assumption that loses the fact — so the proof waits for every
account on that source until the event is repaired. This costs nothing that is
not already lost: an uncontained dead event holds the whole source instance
fatal anyway. Reaching this state requires a freeze to have been resolved out
from under a still-dead event, which every door above now refuses; the wait is
the second line, not the first. A freeze belonging to a *different* account is
still not this account's problem, and still does not hold its proof.
