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

## Per-account ledger (`GET /api/v1/admin/eligibility-ledger`)

XM-INV-USER-LEDGER-QUERY (design doc
`2026-09-03-xm-inv-eligibility-simplification-design.md`, section 3(E)) adds a
second, read-only admin endpoint: one row per external account, for the
per-user account-ledger view CR-0009 builds on the platform side. This route
path is a proposal -- CR-0009 finalizes it. It is protected by the same
administrator role and IP allowlist as `eligibility-freezes` above (no MFA
step-up or CSRF token is required -- it is a pure `GET`, unlike
`eligibility-freezes/{id}/resolve`). It never reads `audit_events`: every
field comes from a plaintext column already on `source_account_eligibility_state`,
`funding_lots`, or `eligibility_freezes`.

Pagination is keyset, `before_id` plus `limit` (default and max 100, same
mechanics as every other admin list in this file -- `WHERE id<before_id
ORDER BY id DESC`), scoped to the external account's own id; there is no
per-item `id` field in the response, only the page envelope's opaque
`next_before_id`. An optional `external_user_id` query parameter does an
exact-match lookup, identical in posture to `eligibility-freezes`'s own
filter of the same name.

Each item:

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
  "block_detail": "balance_checkpoint ckpt-xxx at 2026-09-03T10:00:00Z reported balance -120, expected 380 (difference -500)",
  "block_since": "2026-09-03T10:00:12Z"
}
```

- `recharged_since_policy_start_minor` sums `verified_cash_minor` across every
  `WALLET_CASH`/`SUBSCRIPTION_CASH` funding lot with `completed_at` on or
  after the account's own `cutover_at` -- the design doc's own formula
  (inclusive `>=`), independent of verification/refund-freeze state. This
  reads `cutover_at` as it stands today: XM-INV-ELIG-POLICY-START-ANCHOR
  (design section 3(D), a later, independent slice) is what makes `cutover_at`
  always equal the policy start; until that slice lands, a legacy-bootstrapped
  account's `cutover_at` can still be its first-observed-checkpoint time
  instead, and this field reads whatever value is on the row either way.
- `consumed_minor`/`invoiceable_minor` use the identical formula the user
  summary's own `consumed_minor`/`available_minor` above already uses
  (same filters: CNY, `WALLET_CASH`/`SUBSCRIPTION_CASH`, verified lots;
  `invoiceable_minor` additionally excludes refund-frozen lots and nets out
  reserved/issued amounts) -- not re-derived, the SQL text is a deliberate,
  documented duplicate of that same formula.
- `threshold_minor`/`threshold_reached` reuse the real, admin-configurable
  minimum invoice amount (the same value that gates actual submission), not a
  hardcoded constant. `threshold_reached` is a plain numeric comparison --
  it can be `true` even while the account is blocked, exactly as the example
  above shows.
- `block_reason`/`block_detail`/`block_since` are `null` unless
  `eligibility_status` is `not_invoiceable_pending_reconciliation` (read
  directly from that state's own five plaintext columns, see "Automatic
  reconciliation" below) or `frozen` (read from the latest open
  `eligibility_freezes` row instead: `freeze_reason` becomes `block_reason`,
  `block_since` is that row's `opened_at`, and `block_detail` is composed
  from its `trigger_object_type`/`trigger_object_id` -- there is no stored
  human-readable sentence for a freeze the way there is for pending
  reconciliation). A `frozen` account with no open `eligibility_freezes` row
  (should not happen by construction, but not assumed) still returns a row,
  with all three left `null` rather than erroring. Recorded usage overage
  (`non_invoiceable_overage_*`) never surfaces here at all -- it does not
  block the account, and design section 3(E) has no field for it.

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
