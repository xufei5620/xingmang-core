# XM-INV-ELIG-AUTO-RECONCILE: automatic reconciliation state and usage-overage recording (design section 3(A)/(B))

- **status:** implemented and self-tested locally; **merged with the parallel XM-INV-BLIP-SOFTFAIL
  hotfix** (see "Merge with XM-INV-BLIP-SOFTFAIL" below); full backend suite green after the merge
  (`go test -p 1 -count=1 ./...`), `go vet` clean, `go build` clean, `gofmt` clean (staged-blob
  method).
- **branch:** `ai/claude/XM-INV-ELIG-AUTO-RECONCILE` (based on `ai/claude/XM-INV-AUTOLOGIN` at
  `0177e72`, then merged with `ai/claude/XM-INV-AUTOLOGIN`'s later head at `17945dc` -- see below),
  worktree `K:/发票/wt-XM-INV-AUTO-RECONCILE`.
- **spec:** `docs/superpowers/specs/2026-09-03-xm-inv-eligibility-simplification-design.md`,
  sections 3(A)/3(B), the parts of section 5 (test matrix) and section 6 (slice 1) that belong to
  this slice. Acceptance-line rulings applied (2026-09-03): the six data-integrity freeze reasons
  stay manual (unchanged, not touched by this slice); N=2 consecutive `matched` evaluations for
  auto-exit; the state columns are laid out (`pending_reconciliation_reason` etc.) so a later
  slice's ledger query can express "frozen AND pending reconciliation underneath" if ever needed
  (not implemented here -- see Follow-ups).

## Merge with XM-INV-BLIP-SOFTFAIL

A parallel hotfix, `XM-INV-BLIP-SOFTFAIL` (commits `2ef613b`/`7b35a87`/`e9ac4e2`, merged into
`ai/claude/XM-INV-AUTOLOGIN` ahead of a routine `chore(release): advance release identity to RC76`
at `17945dc`), landed on the same file, `consumption.go`, while this slice was in progress -- it
touches `evaluatePendingBalanceEvidenceTx`'s confirm branch (a non-reconciling blip confirmation now
softfails and rebaselines, capped at 3 consecutive occurrences, instead of returning a hard error)
and adds `countConsecutiveRebaselinedBlipsTx`. Per instruction, `ai/claude/XM-INV-AUTOLOGIN`'s head
(`17945dc`) was merged into this branch (`git merge`, commit `0bc77c8`) before reporting.

**The merge was clean -- no conflicts.** The two slices' edits to `evaluatePendingBalanceEvidenceTx`
occupy disjoint regions: this slice's own changes are (a) the two `UNKNOWN_NEGATIVE_BALANCE`
branches (`item.balanceNegative` and `difference.Sign()==-1`), replacing `freezeEligibilityTx` calls
with `enterPendingReconciliationTx`, and (b) the `writeEvaluation` closure's tail (an added
`advancePendingReconciliationMatchTx` call after a `matched` write); the hotfix's own changes are
entirely inside the `pending != nil` confirm/disconfirm block (a different branch of the same
function) and a new function appended at the file's end. Verified by reading the merged function in
full (not just trusting a clean `git merge` exit code): both behaviors are intact, and the hotfix's
own new `writeEvaluation(item, "matched", ...)` call (in its confirm-success path) now correctly
also triggers this slice's `advancePendingReconciliationMatchTx` -- a real, matched piece of evidence
is exactly the forward-progress signal this slice's auto-exit counter is supposed to count,
regardless of which code path produced it. `ProcessEligibilityProjectionJobs` (the per-account batch
isolation loop) is untouched by either slice, confirmed by diffing it against the merge base.

Re-ran the full backend suite after the merge (`go test -p 1 -count=1 ./...`, `go vet`, `go build`,
`gofmt` staged-blob check, `gitleaks`) -- all clean, including both slices' own tests together
(`TestBalanceBlipSoftfail*`, `TestPendingReconciliation*`, `TestUsageOverage*`,
`TestBalanceBlip*`/`TestBalanceDeltaCarryForward*`). Two full-suite runs each hit a handful of
unrelated packages failing on raw connection errors (Postgres pool, ClamAV, OIDC discovery mock) from
apparent environment/Docker resource contention with other concurrently-running agents in this
sandbox -- confirmed transient by immediately re-running exactly the packages that failed, which then
passed cleanly; no assertion failures were ever observed, only connection-level errors, and never in
the same package twice in a row.

### Hard rule compliance (RC75-derived, per the team lead's brief)

- **Every evaluator branch returns a defined outcome.** Audited every new function added by this
  slice (`enterPendingReconciliationTx`, `advancePendingReconciliationMatchTx`,
  `recordUsageOverageTx`): none of them ever return a synthetic business-logic error -- every
  returned error is a genuine DB/query failure (`tx.Exec`/`tx.QueryRow`/`writeAudit`), and every
  "nothing to do" case (see "Undefined situations" below) is an explicit, documented `return nil`,
  not a fallthrough or a swallowed condition. The one pre-existing synthetic business-logic error in
  this function (`evaluatePendingBalanceEvidenceTx`'s old "did not reconcile" hard error) was already
  removed by the merged hotfix, not by this slice.
- **A batch-isolation test exists for this slice's own additions.**
  `TestPendingReconciliationBatchStillProcessesHealthyAccount` (added after the merge, in the same
  commit as this handoff update) proves a `ProcessEligibilityProjectionJobs` batch containing one
  account entering `not_invoiceable_pending_reconciliation` alongside a separate, ordinary healthy
  account processes both to completion (`processed==2`, no error, no leftover job rows) -- mirroring
  `XM-INV-BLIP-SOFTFAIL`'s own `TestBalanceBlipSoftfailBatchStillProcessesHealthyAccount`. Since this
  slice's code never actually errors (see above), the test's role is to prove that claim empirically
  at the batch level rather than to recover from an injected failure.
- **Undefined situations encountered, and the outcome given to each** (this slice's own code only --
  see `docs/handoffs/XM-INV-BLIP-SOFTFAIL.md` for that slice's own list):
  1. *A negative balance difference arrives while the account is already `frozen`* --
     `enterPendingReconciliationTx`'s `WHERE ... eligibility_status<>'frozen'` guard matches zero
     rows; outcome: no-op for the five pending-reconciliation columns and the account status (frozen
     takes priority, per the design), but reservations are still invalidated (matching
     `freezeEligibilityTx`'s own unconditional behavior) and the checkpoint/proof's own
     `evaluation_status` is still recorded `negative_frozen` by the normal `writeEvaluation` call at
     the end of the loop iteration -- the evidence is never silently dropped, only the account-level
     handling is a no-op. Tested by `TestPendingReconciliationDoesNotDowngradeAFrozenAccount`.
  2. *A `matched` evaluation is written for an account that is not currently
     `not_invoiceable_pending_reconciliation`* (the overwhelming majority of `matched` writes, for
     ordinary `active` accounts) -- `advancePendingReconciliationMatchTx`'s claiming `UPDATE ...
     RETURNING` affects zero rows, surfaced as `pgx.ErrNoRows`; outcome: explicit `return nil`, a
     documented no-op, not an error.
  3. *The consecutive-match count reaches the exit threshold, but a separate open
     `eligibility_freezes` row exists for the account* -- outcome: do not exit (leave
     `eligibility_status` and the already-incremented `pending_reconciliation_consecutive_matches` as
     they are; write no exit audit event). Tested by `TestPendingReconciliationOpenFreezeBlocksAutoExit`.
     This state (an open freeze coexisting with `eligibility_status<>'frozen'`) is reachable via
     `freezeRefundedLotTx`'s `SOURCE_REFUND` path, which never touches `eligibility_status` -- not a
     purely synthetic edge case, though the test itself constructs it via a direct SQL insert to
     isolate the guard (see Risks item 2 below).
  4. *`recordUsageOverageTx` is called with no shortfall (`usageID==""`) and the account already has
     no overage recorded* -- outcome: no write, no audit event (the `WHERE ... IS NOT NULL` guard
     matches zero rows). Deliberate: `reprojectEligibilityTx` runs on every projection job for every
     account, so an unconditional audit write here would spam the log on every routine, unchanged
     reprojection.
  5. *`recordUsageOverageTx` is called with the same shortfall usage event and the same unit amount
     already recorded* (an unchanged shortfall across repeated reprojections of the same window) --
     outcome: no write, no audit event, via the same `IS DISTINCT FROM` guard, for the same reason as
     4.
  6. *An account re-enters `not_invoiceable_pending_reconciliation` while already in that state* (a
     second negative item arrives before the first has auto-exited) -- outcome (a deliberate design
     choice, not a forced one): `reason`/`trigger_type`/`trigger_id`/`detail` refresh to the new
     evidence and `consecutive_matches` resets to zero (the previous streak, if any, evidently did
     not resolve the real gap), but `pending_reconciliation_since` is preserved from the *original*
     entry (via a `CASE WHEN eligibility_status='not_invoiceable_pending_reconciliation' THEN
     pending_reconciliation_since ELSE now() END`) so an operator reading it sees how long the
     account has genuinely been unreconciled, not merely since the latest negative item. Not
     separately tested by a dedicated test in this slice -- flagged here as a real, but not
     independently verified, behavior.
  7. *The blip-rebaseline cap's `SOURCE_GAP` freeze (the merged hotfix's own escalation) fires for an
     account that is currently `not_invoiceable_pending_reconciliation` from an unrelated earlier
     negative checkpoint* -- outcome: well-defined by construction, not specially handled.
     `freezeEligibilityTx` (this slice's own modification) unconditionally clears the five
     pending-reconciliation columns whenever it freezes an account, regardless of the account's prior
     status, so the CHECK-constraint pairing is always satisfied and the account correctly ends up
     `frozen` with the pending-reconciliation columns cleared. Not separately tested by a dedicated
     cross-slice test in this slice; verified by code inspection of the merged function, not by a
     fixture exercising both code paths together in one batch.

## Summary

Implements design section 3(A) (a negative or otherwise unreconciled balance difference downgrades
the account to a new, self-clearing `eligibility_status`, `not_invoiceable_pending_reconciliation`,
instead of opening a manual `eligibility_freezes` row) and section 3(B) (usage exceeding every
available non-cash/cash pool no longer freezes the account -- the unallocated overage is recorded
on the account row instead, cleared automatically the next time reprojection finds no shortfall).

### (A) `not_invoiceable_pending_reconciliation`

- New migration `0020_eligibility_auto_reconcile.sql` extends `source_account_eligibility_state`
  with `pending_reconciliation_reason/trigger_type/trigger_id/detail/since` and
  `pending_reconciliation_consecutive_matches`, plus a pairing `CHECK` (all five populated iff the
  status equals the new value, else all `NULL`/`0`) mirroring `eligibility_freezes`'s own
  open/resolved pairing check.
- New `enterPendingReconciliationTx` (`consumption.go`) replaces every `freezeEligibilityTx(...,
  "UNKNOWN_NEGATIVE_BALANCE", ...)` call site (both `ObserveBalanceCheckpoint` bootstrap branches,
  and both negative-difference branches in `evaluatePendingBalanceEvidenceTx`): writes the five
  columns, sets the new status, still invalidates reservations, but never writes an
  `eligibility_freezes` row. **Frozen priority:** a no-op (besides the always-run reservation
  invalidation) when the account is already `frozen` -- never downgrades a real, distinct freeze.
  `evaluation_status` on the checkpoint/proof itself stays `negative_frozen` unchanged (that column
  classifies the evidence, independent of how the account is handled).
- New `advancePendingReconciliationMatchTx`, invoked from `evaluatePendingBalanceEvidenceTx`'s
  `writeEvaluation` closure whenever it just wrote a `matched` status: increments
  `pending_reconciliation_consecutive_matches`; at 2 (`pendingReconciliationExitMatches`) *and* no
  open `eligibility_freezes` row (reusing `ResolveEligibilityFreeze`'s own
  `count(*)...status='open'` guard for identical semantics), clears all five columns and returns the
  account to `active`, writing `eligibility.pending_reconciliation.exited`. A non-matched real
  evaluation (`negative_frozen`) already resets the counter to zero as part of
  `enterPendingReconciliationTx`'s own unconditional reset; a non-matched, non-negative evaluation
  (`source_gap_frozen`/`positive_classified_non_cash`/`positive_blip_ignored`) leaves it untouched.
- `freezeEligibilityTx` now also clears the five `pending_reconciliation_*` columns whenever it
  freezes an account, satisfying the new pairing `CHECK` and keeping the two states mutually
  exclusive on one row (an account *can* independently carry an open `eligibility_freezes` row while
  `eligibility_status` is not `'frozen'` -- `freezeRefundedLotTx`'s `SOURCE_REFUND` path never
  touches `eligibility_status` -- which is exactly the scenario the auto-exit open-freeze guard
  above protects against).
- Display: `internal/httpapi/user_dto.go` (`reasonCode="LEDGER_PENDING_RECONCILIATION"`) and
  `internal/application/service.go` (`reasons=append(reasons,"PENDING_RECONCILIATION")`) each gained
  one new branch, distinct from `LEDGER_FROZEN`/`ACCOUNT_FROZEN`.
- `/readyz`/`EligibilityProjectionHealth`: no code change needed or made -- confirmed by inspection
  and by `TestPendingReconciliationDoesNotStickReadiness` that `processEligibilityProjectionJob`
  unconditionally deletes its own `eligibility_projection_jobs` row on success regardless of which
  `eligibility_status` the account ends up in, so this state can never leave a row for
  `EligibilityProjectionHealth` (which only reads that table) to see.

### (B) `USAGE_EXCEEDS_LEDGER` downgrade

- Same migration adds `non_invoiceable_overage_units NUMERIC(78,0)` and
  `non_invoiceable_overage_usage_event_id UUID REFERENCES source_usage_events(id)`, paired by a
  `CHECK` (both `NULL` or both non-`NULL`) -- independent of `eligibility_status`, since this tracks
  a fact about the projection, not the account's own admission state.
- `eligibilityProjection` gained a `ShortfallUnits *big.Int` field (set alongside the pre-existing
  `ShortfallUsage` string in both places `buildEligibilityProjectionExcludingUsageTx` records a
  shortfall) so the overage amount, not just the usage event id, is available to record.
- `reprojectEligibilityTx` no longer calls `freezeEligibilityTx(..., "USAGE_EXCEEDS_LEDGER", ...)`;
  it calls new `recordUsageOverageTx` unconditionally every reprojection: sets both columns (and
  writes `eligibility.usage.overage_recorded`) when `ShortfallUsage!=""`, clears both (and writes
  `eligibility.usage.overage_cleared`) otherwise -- each branch is a no-op (no write, no audit) when
  the stored value already matches, so a routine reprojection with an unchanged overage does not
  spam the audit log. `freeze_reason` keeps the `USAGE_EXCEEDS_LEDGER` enum value (historical rows,
  and the next slice's queue-narrowing repair tool will reference it) -- only the code path that
  opened a *new* freeze for it is removed.
- `funding_lots.consumed_cash_minor` was always capped at `verified_cash_minor` by
  `buildEligibilityProjectionTx`'s own cash-pool allocation (the overage was never allocated into
  any cash pool, freeze or no freeze) -- unchanged, verified directly in
  `TestUsageOverageRecordedWithoutFreezingAccount`.

### Not in this slice (explicitly out of scope, per the team lead's brief)

- Manual-review freeze reasons (refund, red-flush, the six data-integrity reasons, `SOURCE_GAP`
  after self-heal failure) are unchanged. `ResolveEligibilityFreeze` semantics are unchanged.
- Existing *open* `eligibility_freezes` rows (from before this slice, for `UNKNOWN_NEGATIVE_BALANCE`
  or `USAGE_EXCEEDS_LEDGER`) are **not** retroactively resolved here -- that is slice 2's
  (`XM-INV-ELIG-QUEUE-NARROW`) `eligibility-repair --kind=queue-narrow` migration tool, per design
  section 3(C)/6. This slice only changes which code paths open a *new* freeze for these two
  reasons going forward.

## Files changed

- `backend/migrations/0020_eligibility_auto_reconcile.sql` (new) -- see Summary above.
- `backend/internal/postgresstore/consumption.go` -- `eligibilityProjection.ShortfallUnits` (new
  field, set at both existing `ShortfallUsage` assignment sites); `reprojectEligibilityTx`'s
  `USAGE_EXCEEDS_LEDGER` freeze replaced with a `recordUsageOverageTx` call; `freezeEligibilityTx`'s
  `UPDATE` also clears the five `pending_reconciliation_*` columns; new
  `pendingReconciliationExitMatches` constant, `enterPendingReconciliationTx`,
  `advancePendingReconciliationMatchTx`, `recordUsageOverageTx`; `writeEvaluation`'s closure in
  `evaluatePendingBalanceEvidenceTx` now calls `advancePendingReconciliationMatchTx` after a
  `matched` write; both `UNKNOWN_NEGATIVE_BALANCE` branches in that same function's main loop, and
  both `ObserveBalanceCheckpoint` bootstrap branches, now call `enterPendingReconciliationTx` instead
  of `freezeEligibilityTx`.
- `backend/internal/httpapi/user_dto.go` -- one new `reasonCode` branch
  (`LEDGER_PENDING_RECONCILIATION`).
- `backend/internal/application/service.go` -- one new `reasons` branch
  (`PENDING_RECONCILIATION`).
- `backend/internal/postgresstore/eligibility_auto_reconcile_integration_test.go` (new) -- this
  slice's own fixtures and tests, see Tests below.
- `backend/internal/postgresstore/balance_carry_forward_integration_test.go` --
  `TestBalanceDeltaCarryForwardFreezesUnchangedActualMismatch` corrected (not a regression -- the
  fix exposed a latent test gap, see below).
- `backend/internal/postgresstore/consumption_integration_test.go` --
  `TestMissingUsageIsCaughtByLowerBalanceCheckpointWithoutIncreasingEligibility`'s final assertion
  corrected (same reason).
- `backend/internal/migrate/migrate_test.go` --
  `TestConsumptionMigrationClosesPreCutoverReservationsAndPreservesIssuedExposure` now also excludes
  `0020_eligibility_auto_reconcile.sql` from its deliberately-reduced migration set (it `ALTER`s
  `source_account_eligibility_state`, created by the already-excluded `0009`, same reason `0016` is
  excluded there).
- `docs/ELIGIBILITY-OPERATIONS.md` -- new closed-reason-list entry (`PENDING_RECONCILIATION`) and a
  new "Automatic reconciliation" section documenting both new states for operators.
- `docs/handoffs/XM-INV-ELIG-AUTO-RECONCILE.md` -- this file.
- `backend/internal/postgresstore/eligibility_auto_reconcile_integration_test.go` -- (post-merge)
  `TestPendingReconciliationBatchStillProcessesHealthyAccount` added, per the team lead's hard rule
  -- see "Merge with XM-INV-BLIP-SOFTFAIL" above.

**Not touched by this slice's own three commits:** `backend/Dockerfile` (no new binary, no
build-stage change needed); `cmd/api/runtime.go` (no readyz change needed -- see Summary);
`scripts/release-image-gate-lib.ps1`; `test-release-image-gate.ps1`;
`verify-release-image-artifacts.ps1`; `RELEASE-READINESS.md`; `docs/PRODUCTION-RUNBOOK.md`;
`docs/IMAGE-SCAN-REVIEW.md`; `docs/superpowers/plans/`; any RC version identity; `contracts/`;
`web/`; no tags created; `eligibility_operations.go`'s `eligibilityFreezeReasons` map (unchanged --
it validates the admin queue's own filter, a distinct concept from `pending_reconciliation_reason`'s
value domain). The merge with `ai/claude/XM-INV-AUTOLOGIN` (instructed, not this slice's own
authorship) did bring in a routine RC76 release-identity bump touching several of the
release-identity files above -- see Risks item 5.

## Pre-existing tests corrected (not a regression -- the fix exposed a latent test gap)

Both were verified to fail with their *original* assertions against the fixed evaluator (confirming
the fix, not a fixture change, caused the difference) before updating:

1. **`TestBalanceDeltaCarryForwardFreezesUnchangedActualMismatch`**
   (`balance_carry_forward_integration_test.go`). A carry-forward proof's unchanged-actual negative
   mismatch used to freeze the account (`UNKNOWN_NEGATIVE_BALANCE`, one open freeze,
   `HasOpenFreeze=true`). Now downgrades to `not_invoiceable_pending_reconciliation` (no freeze row,
   `HasOpenFreeze=false`). The test's name predates this slice and was kept unrenamed (matching
   `XM-INV-ANCHOR-BALANCE`/`XM-INV-BALANCE-BLIP`'s own precedent of correcting assertions in place);
   its own comment now explains why. Added assertions on the five new pending-reconciliation columns
   (reason/trigger/detail/since/consecutive-matches) for full coverage of the new behavior, not just
   the status flip.
2. **`TestMissingUsageIsCaughtByLowerBalanceCheckpointWithoutIncreasingEligibility`**
   (`consumption_integration_test.go`). A checkpoint reporting less balance than the ledger expects
   used to freeze the lot's account (`EligibilityStatus="frozen"`). Now
   `"not_invoiceable_pending_reconciliation"`. Still fails closed (`AvailableMinor()==0` unchanged --
   submit/issue still require `eligibility_status='active'`).

## Tests

New tests, all in `backend/internal/postgresstore/eligibility_auto_reconcile_integration_test.go`:

**(A):**

- `TestPendingReconciliationSingleMatchDoesNotExit` -- a single matched evaluation after entering
  increments the counter to 1 but does not exit.
- `TestPendingReconciliationTwoConsecutiveMatchesExit` -- two consecutive matched evaluations exit to
  `active` with all five columns cleared and the exit audit event written; the first match alone is
  checked not to have exited yet before the second is added (not merely "eventually exits").
- `TestPendingReconciliationOpenFreezeBlocksAutoExit` -- an unrelated open `eligibility_freezes` row
  (constructed directly via SQL, matching this package's own precedent for this "account has both an
  unrelated open freeze and this issue" shape -- see the test's own doc comment for why this is a
  real, reachable combination via `freezeRefundedLotTx`'s `SOURCE_REFUND` path, not merely a
  synthetic edge case) blocks auto-exit even once the match count reaches 2.
- `TestPendingReconciliationDoesNotDowngradeAFrozenAccount` -- an account already frozen for a real,
  distinct reason stays frozen when a negative balance difference arrives; the evidence is still
  recorded `negative_frozen` (honest classification), but the account-level columns are never
  touched.
- `TestPendingReconciliationDoesNotStickReadiness` -- through the real
  `ProcessEligibilityProjectionJobs` entrypoint (not a direct evaluator call): after the account
  enters the new state, its `eligibility_projection_jobs` row is gone (deleted unconditionally on
  success) and `EligibilityProjectionHealth` shows nothing stuck.
- `TestPendingReconciliationBatchStillProcessesHealthyAccount` (added post-merge, per the team
  lead's hard rule) -- a batch containing one account entering the new state alongside a wholly
  separate, ordinary healthy account processes both to completion in one
  `ProcessEligibilityProjectionJobs` call.

**(B):**

- `TestUsageOverageRecordedWithoutFreezingAccount` -- usage exceeding a 100-unit paid lot by 50 units
  records the overage on the account row, keeps the account `active`, opens no freeze, and leaves the
  lot's `consumed_cash_minor` capped at its full `verified_cash_minor` (100_000, no more).
- `TestUsageOverageClearsOnceLedgerCatchesUp` -- a second payment lot giving the same usage fact
  enough cash to be fully allocated clears both overage columns on the next reprojection and writes
  the clearing audit event. Calls `reprojectEligibilityTx` directly (not through
  `ProcessEligibilityProjectionJobs`) -- see the test's own doc comment: that entrypoint's
  `finalized_through` watermark makes a *second* payment lot dated before an already-advanced
  watermark trip the pre-existing, unrelated `LATE_FINALIZED_EVENT` freeze
  (`applyFundingObservationEligibilityTx`), which is not what this test is targeting.

Existing tests re-run unmodified (regression check) as part of the full package runs below,
including every test this package already had for `evaluatePendingBalanceEvidenceTx`,
`ObserveBalanceCheckpoint`, `reprojectEligibilityTx`, `ProcessEligibilityProjectionJobs`, and
`ResolveEligibilityFreeze` -- all still pass with this slice's changes.

## Gate results

From `backend/`, with the eight proxy variables unset and
`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_autorec?sslmode=disable`
(a dedicated database created for this task in the already-running `invoice-test-pg` container,
separate from other agents' databases; never `invoice_test_release`), **run after the merge with
`ai/claude/XM-INV-AUTOLOGIN`'s `17945dc` head**:

```
go build ./...                 # exit 0
go vet ./...                   # exit 0
go test -p 1 -count=1 ./...    # ok, all packages (multiple full runs, all green)
```

Ran the full suite four times across this task (twice before the merge, twice after). Every run that
failed anything failed only on raw connection errors in packages unrelated to eligibility code --
Postgres pool dial failures (`internal/application`, `internal/postgresstore`'s own
`identity_migrate`-adjacent fixtures), ClamAV dial failures (`internal/document`), and OIDC-discovery
mock-server failures (`internal/auth`) -- never an assertion failure, and never in the same package
on two consecutive runs; immediately re-running exactly the failed packages always came back clean.
Consistent with environment/Docker resource contention from other agents running concurrently in this
sandbox, not a regression. The final post-merge full run (with the batch-isolation test included) was
completely clean on the first try, `internal/auth` included.

`"$(go env GOROOT)/bin/gofmt" -l` against the **staged git blob** of every file this slice touches
or adds (`git show ":<path>"`, matching this machine's documented CRLF-checkout `gofmt` false-positive
handling): clean on every file, including after the merge and the batch-isolation test addition.

`gitleaks git --no-banner --log-opts="0177e72..HEAD" .`: clean, scanning all six commits in this
branch's range ahead of the merge base (`2ef613b`/`7b35a87`/`e9ac4e2`/`17945dc` from the merged-in
hotfix, plus this slice's own three commits).

## Not run

- `scripts/verify.ps1` in full -- spans frontend, Docker release-image gates, and Keycloak/Nginx
  verification, none of which this backend-only slice touches. Its backend-relevant lines are
  covered in spirit by the gates above, run without `-race` (not requested for this task).
- Anything requiring a server/production connection -- explicitly out of scope, matching every prior
  sibling slice's own precedent.

## Risks / things to sign off on

1. **`pending_reconciliation_reason`'s value domain mirrors `eligibility_freezes.freeze_reason`'s
   full 11-value `CHECK`** (per the design doc's "同 freeze_reason 取值" wording), even though this
   slice's own code only ever writes `'UNKNOWN_NEGATIVE_BALANCE'` to it. Chosen for forward
   compatibility with the design's own framing rather than a narrower one-value `CHECK`, at the cost
   of technically allowing a value no current code path would ever write. Flagging in case a
   narrower constraint was intended.
2. **The auto-exit open-freeze guard (`advancePendingReconciliationMatchTx`) is defensive**: by
   construction (`enterPendingReconciliationTx`'s own frozen-priority guard,
   `eligibility_status<>'frozen'`), an account cannot normally reach
   `not_invoiceable_pending_reconciliation` while `eligibility_status='frozen'` -- but it *can*
   independently carry an open `eligibility_freezes` row without `eligibility_status` being
   `'frozen'`, since `freezeRefundedLotTx` (the `SOURCE_REFUND` path) never touches
   `eligibility_status` at all. `TestPendingReconciliationOpenFreezeBlocksAutoExit` constructs this
   via a direct SQL insert rather than reproducing a full `SOURCE_REFUND` fixture, to isolate the
   guard itself; the underlying reachability claim (a real, distinct open freeze coexisting with this
   state) rests on `freezeRefundedLotTx`'s documented behavior, not on a fixture that drives it
   end-to-end. Flagging as a real, but not full-path-verified, coverage boundary.
3. **`recordUsageOverageTx`'s FK to `source_usage_events(id)` is a plain (non-deferred) `REFERENCES`.**
   Safe given the only real call site (`reprojectEligibilityTx`, after `buildEligibilityProjectionTx`
   has already read the usage event's own row from a previously-committed insert) always references
   an already-existing, already-committed row -- verified by construction, not by a specific
   concurrency test.
4. **`FreezeReason == "USAGE_EXCEEDS_LEDGER"` and `"UNKNOWN_NEGATIVE_BALANCE"` remain valid
   `eligibility_freezes.freeze_reason` enum values** (historical rows, and slice 2's repair tool will
   need to reference them) even though no code path in this slice opens a *new* freeze for either.
   Confirmed by grep: the only remaining call sites creating `eligibility_freezes` rows for these two
   reasons are the sibling repair tools' own direct-SQL test fixtures (unrelated, pre-existing,
   untouched by this slice) and `eligibility_operations_integration_test.go`'s
   `ResolveEligibilityFreeze` fixture (a hand-inserted admin-queue row, unrelated real code path).
5. No new float amounts, no logged/persisted secrets, no `contracts/` changes, no admin-OIDC changes,
   no tags created, no touch to `backend/Dockerfile`, no touch to any file outside this slice's own
   stated scope by this slice's own three commits -- checked. **Exception, from the merge, not this
   slice:** the merge brought in `ai/claude/XM-INV-AUTOLOGIN`'s own routine `chore(release): advance
   release identity to RC76` commit (`17945dc`), which does touch the six release-identity files,
   `RELEASE-READINESS.md`, `docs/PRODUCTION-RUNBOOK.md`, `docs/IMAGE-SCAN-REVIEW.md`, and
   `scripts/release-image-gate*`/`test-release-image-gate.ps1`/`verify-release-image-artifacts.ps1`
   -- not authored by this slice, merged in as an ordinary consequence of merging the instructed
   branch head, not cherry-picked or specifically requested.

## Follow-ups (recommended, not blocking this delivery)

1. **Section 7 open question 3** (design doc): whether the future user-ledger query (`XM-INV-ELIG-USER-LEDGER-QUERY`,
   slice 4) should expose a boolean "frozen AND pending-reconciliation-underneath" bit. Not
   implemented here (deliberately out of scope, per the team lead's own framing of it as a later
   slice's decision) -- but risk 2 above documents that the two states cannot coexist as designed, so
   if a future slice ever wants to surface "this frozen account also has an unreconciled balance
   difference sitting underneath," it will need either a new column pair or to keep classifying
   incoming negative differences even while frozen (not done today: `enterPendingReconciliationTx`
   no-ops entirely, writing nothing, when the account is already frozen).
2. Slice 2 (`XM-INV-ELIG-QUEUE-NARROW`)'s `eligibility-repair --kind=queue-narrow` migration tool
   still needs to be built to resolve the *existing* open `UNKNOWN_NEGATIVE_BALANCE`/
   `USAGE_EXCEEDS_LEDGER` freezes created before this slice shipped, per design section 3(C) item 3 --
   explicitly not attempted here.
3. Consider whether production has any existing open `UNKNOWN_NEGATIVE_BALANCE`/`USAGE_EXCEEDS_LEDGER`
   freezes right now that would benefit from this slice landing before slice 2's repair tool is
   ready -- no production access for this task to check.
