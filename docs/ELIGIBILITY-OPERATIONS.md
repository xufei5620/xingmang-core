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

## User summary

`GET /api/v1/user/eligibility-summary` returns one item per connected source:

- claimable `available_minor`, finalized `consumed_minor`, paid but unconsumed
  `unconsumed_minor`, `reserved_minor` and `issued_minor`, all explicitly CNY;
- `legacy_noninvoiceable` and `noncash` as `{service_units, unit_code}` rather
  than pretending quota/balance units are yuan;
- a closed reason list: `READY`, `BINDING_NOT_VERIFIED`, `ACCOUNT_FROZEN`,
  `PROJECTION_PENDING`, `SOURCE_NOT_READY`, or `NO_CONSUMED_CASH`.

Available CNY is reported as zero while binding, source health, freeze or
reprojection prevents a new invoice request. Historical consumed/issued values
remain visible for reconciliation.
