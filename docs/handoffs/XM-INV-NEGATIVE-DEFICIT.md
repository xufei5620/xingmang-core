# XM-INV-NEGATIVE-DEFICIT: a negative balance carries its magnitude; an explained overdraw is matched

- **status:** implemented on the release branch; agents suite green, the seven
  new backend integration tests green against the local test database, full
  backend suite running as this handoff is written (result recorded in the
  ACCEPTANCE-LOG entry). Not released. **Shadow evaluation is required** before
  it ships (evaluator change), and the bridge SQL must be re-installed on both
  source databases before agents built from this commit start (see "Rollout").
- **branch:** `ai/claude/XM-INV-AUTOLOGIN` (the release line), worktree
  `K:/发票/wt-XM-INV-AUTOLOGIN`.
- **design (frozen):** `docs/superpowers/specs/2026-09-06-xm-inv-negative-deficit-design.md`
  -- product owner's decision 2026-09-06: "B+C".
- **prerequisite handoffs read first:** `XM-INV-OVERAGE-CARRY-FORWARD.md`
  (the carried cash debt this slice compares against),
  `XM-INV-ELIG-AUTO-RECONCILE.md` (the self-clearing pending state).

## Symptom

Account `acdcdce9` (user 12) cycles "spend to zero -> overdraw -> pending ->
top up -> exit -> spend to zero". The bridge reported a negative balance as
`balance_service_units="0"` + `balance_negative=true` with no magnitude; the
evaluator parked any negative report in `not_invoiceable_pending_reconciliation`
unconditionally; leaving needs two consecutive matched **new** checkpoints, and
an unchanged balance produces none, so only the next top-up could unpark it.
Per the owner, essentially every user tops up small amounts and burns to zero,
so every cycle went through the pending state.

## What changed

**Bridge (our `invoice_bridge` schema on the source databases, not source
code):** both `balances_v4('rows')` outputs gain
`deficit_service_units` (`GREATEST(-balance,0)` at the same 1e8 scale;
`GREATEST(-quota,0)` for New API). Function names, the `contract`/`health`
operations, `projection_contract` and `configuration_hash` are unchanged, so
no cutover manifest is invalidated and running agents ignore the extra key.
`check-db` pins each routine body by `sha256(prosrc)`; both balances constants
are repinned (`agents/cmd/source-agent-prod/main.go`,
`expectedBridgeRoutineHash`) and a new test recomputes all ten from
`contracts/` (`bridge_routine_hash_test.go`), so a future SQL edit without a
repin fails in CI. Extraction rule, verified against the pre-change constants:
everything between `AS $bridge$` and the closing `$bridge$;`, leading newline
included.

**Agent:** `BalanceSnapshotRow.DeficitServiceUnits` (`omitempty`, so a
baseline sealed before the field keeps its content hash -- tested), the
shared `balanceRowRecordDefinition` with the fourth column, `balanceRowDeficit`
(NULL = bridge not re-installed = fail closed; magnitude must agree with the
flag and with zero units), the delta rule now treats a moved deficit as a
change (-3 to -5 units publishes a checkpoint), the checkpoint payload and
`validateRecord` carry and check the field, `inspect-pending` is unchanged
(it never printed payloads).

**API and ledger:** payload field `deficit_service_units` (optional; both
sides decode with `DisallowUnknownFields`, so the api restarts before the
agents -- roll-forward already orders it that way), migration
`0026_balance_checkpoint_deficit.sql` (nullable `deficit_service_units` on
`balance_reconciliation_checkpoints` and `balance_carry_forward_proofs`,
non-negative and `(deficit>0)=balance_negative` checks, the carry-forward
contract trigger now also requires `prior_deficit IS DISTINCT FROM
NEW.deficit_service_units` to be false), the three checkpoint inserts, the
carry-forward candidate/insert path and the evaluation items query carry it.

**Evaluator:** `eligibilityProjection.UnallocatedUnits` is new -- every unit
of usage the projection could not charge to any pool by the end of the
window (carried cash debts plus non-invoice-eligible shortfalls, all of them;
`ShortfallUnits`/the overage columns still name only the oldest). In
`evaluatePendingBalanceEvidenceTx`, a negative item with a known deficit
computes `difference = (-deficit) - (ExpectedBalance - UnallocatedUnits)`:
zero is `matched` (no pending entry; a pending account counts a consecutive
match), anything else is `negative_frozen` with the signed numbers in the
detail ("reported balance -X, expected -Y (difference D)"). An unknown deficit
(NULL: evidence sealed before the bridge reported it, or a bridge that has
not been re-installed) keeps the previous treatment, with "unknown magnitude"
in the detail. A smaller-than-expected deficit deliberately does not
synthesise an `UNKNOWN_POSITIVE` credit: such a credit is non-cash and would
never settle a carried debt, so the gap would persist; the account is parked
visibly instead.

## Tests

- `agents/sourceagent/balance_deficit_test.go`: NULL-deficit fail-closed;
  flag/units/magnitude invariants; deficit move counts as a change; a negative
  account is never retired; a legacy baseline without the field keeps its
  content hash and still validates; inconsistent rows rejected; the projected
  payload carries the field and strict-decodes.
- `agents/cmd/source-agent-prod/bridge_routine_hash_test.go`: the ten pinned
  routine hashes equal sha256 of the reviewed contract bodies.
- `backend/internal/postgresstore/negative_deficit_integration_test.go` (real
  PostgreSQL): explained deficit -> matched, active, no pending audit, overage
  unchanged; larger deficit -> negative_frozen/-10 with signed detail; smaller
  deficit -> negative_frozen/10; unknown deficit -> negative_frozen "unknown
  magnitude"; `ObserveBalanceCheckpoint` rejects flag/deficit contradictions
  and malformed values; a carry-forward proof restates the prior deficit, is
  evaluated with it (stale -50 against an expected -60 parks the account) and
  the contract trigger refuses a proof that drops it; **account isolation**: a
  second account in the same source keeps byte-identical eligibility state and
  evaluations while the first account's explained negative is evaluated.
- Corrected fixture: `TestBalanceDeltaEmitsFirstPostCutoverAndOnlyRealChanges`
  flipped `BalanceNegative=true` on a row with 75 units -- a shape the database
  has always rejected (`CHECK (NOT balance_negative OR balance_service_units=0)`)
  and the agent now rejects too. The row is now `0` units, negative, deficit
  75, which preserves the test's intent (the row changed) and exercises the
  new field.

## Rollout

1. Owner runs the maintenance wrapper `install-economic` for `sub2api` and
   `newapi` (cluster superuser; see PRODUCTION-RUNBOOK section 7). Running
   agents are unaffected.
2. Ordinary RC: migration 0026 -> api -> agents. Shadow evaluation before the
   roll-forward (evaluator change).
3. Canary: account 12's next negative checkpoint evaluates `matched`; no new
   `negative_frozen` rows with "unknown magnitude" once the agents are on this
   build.
4. Rollback: the previous release directory (api and agents together -- the
   old api also rejects unknown payload fields, so agents must not stay on
   this build against an old api). The bridge's extra column is harmless to
   old agents and needs no rollback.

## Follow-ups

- Historical `negative_frozen` rows are immutable facts; account 12 exits the
  pending state on its next two evidence items (the first explained negative
  checkpoint plus the post-top-up one), not retroactively. A repair command
  was considered and not built: the natural cycle clears it within one
  top-up.
- The admin ledger view shows the evaluation detail string; it does not yet
  surface `deficit_service_units` as its own column.
