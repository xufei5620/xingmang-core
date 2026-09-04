# XM-INV-OVERAGE-CARRY-FORWARD: charge an overdraw to the next top-up instead of writing it off

- **status:** implemented on the release branch, gated locally (backend
  build/vet, full `go test -p 1 -count=1 ./...` against real PostgreSQL). Not
  yet released. Shadow evaluation is **required** before it ships: this changes
  what the evaluator allocates.
- **branch:** `ai/claude/XM-INV-AUTOLOGIN` (the release line).
- **found in production**, 2026-09-04, while verifying XM-INV-CATCHUP-RELEASE.
- **product decision** (owner, 2026-09-04): carry the overdraw forward and
  invoice it, rather than writing it off. The owner confirmed the upstream
  behaviour directly: "用户的负余额会在下次充值的时候被抵扣掉". After the
  production evidence below, the owner narrowed it further: settle a carried
  debt from cash only.

## Symptom

Account `acdcdce9` sat in `not_invoiceable_pending_reconciliation` and could
not leave. Its evaluation history:

| classification | count | window |
| --- | --- | --- |
| `matched` | 183 | 2026-09-01 12:14:31 → 2026-09-02 07:54:36 |
| `negative_frozen` | 380 | from 2026-09-02 07:58:53 onward |

Every one of the 380 negative differences was **the same number**:
`-1361800`. The account's own `non_invoiceable_overage_units` column read
`1361800`.

## Root cause

Both sources bill as they go and let a request overdraw. At 2026-09-02
07:58:53 this account's balance went to zero mid-request, and the source
carried it 1361800 units negative. The user's next top-up settled that debt
before anything else, which is how the source works.

`buildEligibilityProjectionTx` did not. Its allocation loop walked the facts in
time order, allocated each usage event against the pools that existed at that
instant, and **dropped whatever it could not cover**:

```go
if remaining.Sign() > 0 && projection.ShortfallUsage == "" {
    projection.ShortfallUsage = fact.UsageID
    projection.ShortfallUnits = new(big.Int).Set(remaining)
}
```

`ShortfallUnits` was recorded on the account row and then never used again.
The units themselves were gone. So from that moment on:

- `ExpectedBalance` was permanently 1361800 units higher than the source's,
  because the source had deducted those units and the projection had not.
- Every checkpoint after it computed `difference = balance - ExpectedBalance`
  = `-1361800`, took the `case -1:` branch, and called
  `enterPendingReconciliationTx(UNKNOWN_NEGATIVE_BALANCE)`.
- `enterPendingReconciliationTx` resets
  `pending_reconciliation_consecutive_matches` to 0 on every entry, and
  `advancePendingReconciliationMatchTx` needs `pendingReconciliationExitMatches`
  consecutive matches to leave. With a constant offset there is never a single
  match, so the self-clearing state could never clear.
- The 1361800 units were never invoiced, even though the next top-up's cash is
  what paid for them.

The account was both permanently blocked and permanently under-invoiced, and
nothing logged an error at any point.

## Why this is everyone's problem, not one account's

Per the product owner, essentially every user tops up small amounts, spends to
zero, and tops up again. Every single burn-to-zero cycle overdraws by whatever
the last request cost and mints one of these permanent offsets.

At the time of writing, of 8 bound accounts: 4 have already hit a zero
balance, and 2 already carry a recorded overage. The second one
(`40bd883d`, 300000 units) is still `active` only because it has not topped up
since — it would have been parked on its next top-up.

## Fix

The allocation loop keeps unallocated usage in a `carried` queue instead of
dropping it, and drains that queue, oldest debt first, whenever a **cash**
top-up arrives. A usage event can therefore be paid in instalments, so
allocation order is tracked per usage event (`consumption_allocations` is
`UNIQUE (usage_event_id, allocation_order)`) rather than restarting at each
visit.

**Only cash settles a debt.** Non-cash pools never drain one, and a usage fact
that is not invoice-eligible is never carried at all, because cash is the one
thing that could settle it and it may not touch cash. The reason is in the next
section.

`ShortfallUsage`/`ShortfallUnits` now describe what is *still* unallocated at
the end of the window. An overdraw a later cash top-up settled is no longer an
overage at all, because its units were charged to that top-up's lot.

Consequences, all intended:

- `ExpectedBalance` tracks the source again across a burn-to-zero cycle, so the
  account keeps reconciling and never enters the pending state for this reason.
- The overdrawn units land on the lot whose cash actually covered them, with a
  `cash_minor_delta`, so they are invoiced.
- Nothing else about the loop changed: allocation still goes non-cash first
  then cash, a lot's `consumed_service_units` still cannot exceed its own
  `cash_service_units`, and every invariant `enforce_lot_consumption_mirror()`
  checks still holds, because the carry-forward path runs through the same
  `cumulativeCashRound` call as the ordinary path.

## Why only cash, and what that changes about the blast radius

The first draft drained a debt against whatever pool arrived next, cash or not.
Production data showed that is wrong, and the product owner's account of how
the sources are operated explained why.

Two accounts carry a recorded overage. They are not the same case:

| | `acdcdce9` (user 12) | `40bd883d` (user 34) |
| --- | --- | --- |
| status | `not_invoiceable_pending_reconciliation` | `active` |
| overage | 1361800 | 300000 |
| latest checkpoint difference | −1361800 | **0** |
| pools | six real cash top-ups, ¥70.00 | one ¥5 top-up, plus four synthesized `UNKNOWN_POSITIVE` credits totalling 50,288,881,378 units |

Account 12's difference equals its overage exactly: the source did deduct the
overdraw, and the next top-up settled it. Carrying it forward against cash puts
the expected balance back on the source's number.

Account 34 reconciles perfectly today, which is the evidence that the source
did **not** deduct its overdraw. It is the team's own test account: a ¥5 real
top-up, quota added by an administrator (which leaves no payment record, so the
evaluator synthesizes `UNKNOWN_POSITIVE` credits to explain the balance it
cannot attribute), and a deliberately inflated per-user rate multiplier used to
burn quota quickly. Draining its debt into those synthesized credits would have
lowered its expected balance by 300000 and moved a reconciling account off zero
for nothing.

Restricting settlement to cash is not a hedge, it is the correct rule: we carry
a debt forward only when real money covers it. When a gift, a `REBATE`, or a
synthesized credit is what cleared the negative balance upstream, the
consumption is not invoiceable anyway, so writing it off — the behaviour that
predates this slice — is already the right answer.

The blast radius is therefore exactly one account: `acdcdce9`. Every other
account's projection is byte-identical, because `carried` only becomes non-empty
where the old code recorded an overage, and of the two accounts that did, only
one has cash arriving after the overdraw that is not already fully consumed.

## Tests

`backend/internal/postgresstore/overage_carry_forward_integration_test.go`,
against a real database:

- `TestOverdrawnUsageIsChargedToTheNextTopUp` — 500 units funded, 800 spent,
  500 funded again. Asserts `ExpectedBalance` 200, no shortfall, the usage
  split 500/300 across the two lots with distinct allocation orders, and a
  30000-minor cash delta on the second lot. **Verified red before the fix**:
  `expected balance 500, want 200`.
- `TestOverdrawnUsageIsNotSettledByNonCashCredit` — same shape, but a
  non-cash `REBATE` credit arrives instead of a second cash top-up. Asserts
  the credit stays whole, the debt stays outstanding, and no allocation lands
  on a credit. **Verified red against the first draft**: `expected balance
  200, want 500`.
- `TestOverdrawnUsageWithNoLaterTopUpStaysAnOverage` — same shape without the
  second top-up. Asserts the 300 units stay unallocated and keep being
  reported as the overage. Carry forward is not forgiveness.

Full `internal/postgresstore` suite green (181s), full backend suite green.

## What about the snapshot racing the usage stream?

The obvious worry, raised by the product owner: both sources bill continuously,
so a balance snapshot and the usage stream can never be perfectly aligned, and
a checkpoint could show the source lower than the projection simply because
usage it had already applied had not reached us yet. The evaluator has no
tolerance for a negative difference -- `case -1:` parks the account on the
first one it sees -- so such a race would park accounts at random.

It cannot happen, and the reason is structural rather than lucky.
`finalizeSourceAccountsTx` only ever projects up to

```
requested_through = GREATEST(cutover_at, min(all four stream watermarks)
                             - finalization_delay_seconds)
```

with `finalization_delay_seconds` at 900 in production. A checkpoint at `T` is
therefore not evaluated until every one of the four streams has a watermark of
at least `T + 15 minutes`, by which point every usage event with
`event_time <= T` has certainly been delivered.

The production evidence agrees. If the race happened, the negative differences
would be scattered values, each one the size of whatever was in flight. All 380
of this account's were the same number. There was not a single transient one.

So no separate slice is needed for it. The asymmetry between the two branches
(a positive difference gets a boundary rule and a defer-and-confirm blip
mechanism; a negative one gets neither) is not an oversight: a positive
difference comes from a *source-side* ordering artifact -- the snapshot taken
before the instant's own debit was applied -- which the delay above cannot
prevent, whereas a late usage fact on our side is exactly what the delay is
for.
