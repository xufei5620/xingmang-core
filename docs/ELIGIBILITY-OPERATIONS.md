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
