# XM-INV-SHADOW-EVAL-VACUOUS: the shadow evaluation has never exercised a projection

- **status:** implemented and verified: RC92 rehearsal 7 of 8 (whale proof-pending by structure), follow-ups implemented the same day, **RC93 rehearsal 8 of 8 with `pending_accounts` null** (2026-09-05, report `rehearsals/20260905T010341Z-3004051`). Filed 2026-09-04 from the RC88 rehearsal.
- **branch:** ai/claude/XM-INV-AUTOLOGIN.
- **found in production rehearsal**, 2026-09-04, while gating XM-INV-OVERAGE-CARRY-FORWARD.

## Symptom

`deploy/rehearsal/shadow-eval.sh` returned verdict `ready` for RC88, the first
release whose whole point is a change to what the evaluator allocates. The
report:

```
rounds run:   1 / 200
queue drained: true
before {matched: 2534, negative_frozen: 12, positive_blip_ignored: 220,
        positive_classified_non_cash: 10, source_gap_frozen: 12}
after  {matched: 2534, negative_frozen: 12, positive_blip_ignored: 220,
        positive_classified_non_cash: 10, source_gap_frozen: 12}
account status changes: 0
```

Before and after are identical. Not one projection ran. The candidate
evaluator was never invoked against a single account, so the `ready` verdict
says only that the tools image starts and the report serialises.

## Root cause

`eligibility-shadow` drains whatever is already in
`eligibility_projection_jobs` and reports the delta. It never enqueues
anything. Production's queue is normally empty — that is the healthy steady
state — so a backup restored from a healthy production has nothing to drain,
and the run is a no-op by construction.

This is not specific to RC88. The same "one round, drained" shape appears in
the RC78 and RC79 acceptance entries, which are the other releases that
carried evaluator or migration changes. The gate has been passing without ever
doing the thing it exists to do.

## Fix

Give the rehearsal a way to force a full reprojection of every account before
the drain. `deploy/rehearsal/shadow-eval.sh` already has the restored
container addressable by name at exactly the right moment — between its
`invoice-migrate` step and the `invoice-eligibility-shadow` run, where it
already issues `docker exec "$container" psql` twice to diff
`schema_migrations`. One statement there is enough:

```sql
INSERT INTO eligibility_projection_jobs(
    external_account_id, requested_through, status, next_attempt_at)
SELECT external_account_id, finalized_through, 'queued', now() - interval '1 second'
FROM source_account_eligibility_state
ON CONFLICT (external_account_id) DO NOTHING;
```

Reprojecting at an account's own `finalized_through` is an existing, supported
call shape, not a new one: the late-fact path in `observeEligibilityFactTx`
calls `reprojectEligibilityTx(ctx, tx, accountID, account.FinalizedThrough,
actor)` for exactly this purpose, and the job processor does not short-circuit
when the window has not advanced.

Gate it behind an explicit flag (`--reproject-all`, or
`SHADOW_EVAL_REPROJECT_ALL=1`) so the plain run keeps its current meaning, and
make the release plan require the flag whenever the release touches the
evaluator, the projection, or a migration that feeds either.

## Also worth reporting

The report carries `before`/`after` account status and evaluation counts, but
no per-account numbers, so it cannot show that an account's expected balance
moved to a particular value — the thing an allocation change actually needs to
demonstrate. Consider adding per-account `expected_service_units` and
`consumed_cash_minor` totals to `Snapshot`, so a reviewer can diff the
quantities the change is about rather than only their classifications.

## Acceptance

1. With the flag set, a rehearsal against a healthy production backup runs
   more than one round and reports a non-zero number of accounts projected.
2. Running it against RC88's own candidate reproduces, in the restored copy,
   the production prediction recorded in RC88's plan: `acdcdce9`'s expected
   balance moves to 812998180 and `40bd883d` does not move.
3. Without the flag, the run behaves exactly as it does today.
4. The verdict still fails closed on a new freeze-reason category or a
   projection error, unchanged.

## What was built (2026-09-05)

The fix is three things, not one. Enqueueing alone would have left the report
looking exactly as it does today.

**1. `postgresstore.EnqueueEligibilityShadowReprojection`** queues one job per
account at that account's own `finalized_through`, `ON CONFLICT DO NOTHING`.
Verified by reading the worker rather than trusting this document's earlier
claim: `processEligibilityProjectionJob` raises `requested_through` to
`finalized_through` when it is below, never lowers it, and then calls
`reprojectEligibilityTx` unconditionally — there is no short-circuit for a
window that did not advance. Its closing `UPDATE` is
`finalized_through=GREATEST(finalized_through,requested)`, so replaying an
account at its own boundary cannot move that boundary in either direction.

**2. The report now carries `accounts_projected`, and the verdict fails
closed on it.** This is the part the original write-up missed, and it matters
more than the enqueue. `ProcessEligibilityProjectionJobs` returns how many
accounts it processed; `run()` was discarding that value. Production has eight
accounts, and the batch limit is twenty-five — so a complete, genuine
rehearsal of this system reports `rounds_run: 1, queue_drained: true`, which
is **the same shape as the vacuous runs**. Round count cannot distinguish
them; only the account count can. When `--reproject-all` was passed and
`accounts_projected` is zero, the verdict is now `not_ready`, checked *before*
the freeze comparison — because "no new freeze reasons" is trivially true of a
projection that never ran, and attributing that to the candidate would send a
reader chasing a regression that is not there.

The condition is taught to both verdict implementations, Go and bash. They are
computed independently and compared at rehearsal time, so a condition only one
side knows would surface as a tooling failure rather than the blocked release
it should be.

**3. Per-account quantities in the snapshot** — `projection_version`,
`finalized_through`, `non_invoiceable_overage_units`, and the funding-lot
totals (cap, consumed cash, reserved, issued). RC88 is the worked example: it
claimed one account's expected balance would move by a specific amount, and
the report it was gated on could not have shown that even if the projection
had run, because it carried no quantity at all. `projection_version` doubles
as the per-account counterpart to `accounts_projected`: an account whose
version did not move was not reprojected.

### What the pre-existing tests caught that reading the code did not

The first draft scanned `non_invoiceable_overage_units::text` straight into a
string. That column is nullable and **NULL is the ordinary case — six of the
eight production accounts carry no overage** — so the snapshot query would
have failed on the first row, and the rehearsal this slice exists to repair
would have exited 1 before taking its baseline. It is now
`COALESCE(...,'0')`, which is unambiguous rather than merely convenient:
migration 0020's CHECK is `IS NULL OR > 0`, so a stored value can never be
zero and "0" can only mean "none".

Credit where it belongs: the two tests that caught this were the *existing*
`TestEligibilityShadowSnapshotAccountsAndFreezes` and
`...EvaluationsKeepsLatestPerCheckpoint`, not anything added by this slice.
The failure mode was contained — `EligibilityShadowSnapshot` has exactly two
callers, both in `cmd/eligibility-shadow`, so nothing in the request or worker
path touches it, and the rehearsal fails closed (exit 1 blocks the tag) rather
than reporting a wrong verdict. The lesson is not that it nearly shipped. It
is that this is code which only runs during a release, so a defect in it stays
invisible until the next release, at the most expensive possible moment to
debug — on the server, with the age identity mounted. Reading the SQL was not
an adequate substitute for running it.

Two of the new tests also asserted hard-coded row counts that encoded the
shared fixture's own bookkeeping rather than the behaviour; they now derive
the expectation from `source_account_eligibility_state` itself, and the
"leaves existing jobs untouched" test reads the row back before the enqueue
instead of predicting what the seed helper wrote.

## Still open

- **Nothing has run against a real backup yet.** Every claim above is from
  unit and integration tests plus reading the worker. Acceptance items 1 and 2
  below need one rehearsal against a signed production backup, which needs the
  age identity; that is a user step and is not done.
- The release plan template must require `--reproject-all` for any release
  touching the evaluator, the projection, or a migration feeding either. Until
  that line exists in the template, this is a capability, not a gate.
- `XM-INV-CATCHUP-BURST-BACKPRESSURE` remains untouched.

## Real-backup run, 2026-09-05

Ran the modified rehearsal scripts against backup `invoice-20260904T184003Z`
with the **rc91** tools image, deliberately without `--reproject-all` — the
flag lives in the tool binary, and rc91's image predates it. Report:
`/root/invoice-system/rehearsals/20260904T203559Z-1390425/`.

Verified:

- The restore → verify → migrate → drain → report chain still works with the
  edited scripts. Exit 0.
- **The two verdict implementations agree on a real report**, not just on
  fixtures: `verdict=ready (independently recomputed; tool process exit was
  0)`. The new `shadow_eval_run_was_vacuous` does not misfire on a report that
  carries neither `reproject_all_requested` nor `accounts_projected` — both
  keys confirmed absent, both scalars read empty, condition correctly false.
  Reports already on disk from earlier rehearsals stay readable.
- Migration 0025 applies cleanly to a restored production backup.

And it captured the defect itself, in full:

```
accounts before: 8   after: 8
before == after : True
rounds_run: 1    queue_drained: True
matched 3251 / negative_frozen 429 / positive_blip_ignored 220 /
positive_classified_non_cash 10 / source_gap_frozen 8   -- identical either side
```

That is the exact report shape RC78, RC79 and RC88 were gated on. Eight
accounts, none projected, verdict "ready".

**Not verified, and this is the whole point of the slice:** the enqueue and
the vacuity block. Both live in the tool binary, so they need an image built
from this commit.

### Acceptance for RC92

Add to RC92's Task 3, as an explicit one-off verification of this gate rather
than a routine step:

- [ ] Run the shadow evaluation with `--reproject-all` against the newest
      signed backup. Require `accounts_enqueued` == `accounts_projected` == the
      row count of `source_account_eligibility_state` (8 at the time of
      writing). If `accounts_projected` is 0 the verdict must be `not_ready`
      with exit 3 — that outcome is the gate working, not the release failing,
      and it means this slice did not do its job.

## Verified on RC92, 2026-09-05

The rehearsal ran with `--reproject-all` against `invoice-20260904T184003Z`
using the rc92 tools image (report `rehearsals/20260904T224617Z-2176602`):
`accounts_enqueued = accounts_projected = 7` of 8 accounts, `projection_version`
moved on all seven, every `consumed_cash_minor` and overage identical before
and after (this release carries no evaluator change, so identical is the
correct answer — and the first time the harness could give it), verdict
`ready` in both implementations. Acceptance items 1 and 3 are met; item 2
(reproducing RC88's prediction) needs a backup that predates RC88 and is
deferred.

The eighth account, the whale `40bd883d`, was not projected, and the reason
is structural rather than a defect: the backup already held its queued job
(`ON CONFLICT DO NOTHING` left it alone, as designed), the drain claimed it,
and the carry-forward proof returned `BALANCE_PROOF_PENDING` because a frozen
copy never publishes the balances cycle that would cover the window's tail.
A continuously-consuming account is always in that state at backup time. The
report shows it (`after_projection_health.proof_pending = 1`) instead of
hiding it.

Two follow-ups, both small:

1. **Name the proof-pending accounts in the report.** `after_projection_health`
   gives a count; the per-account section should say which accounts did not
   move and why (`BALANCE_PROOF_PENDING` from the job row's `last_error_code`),
   so "7 of 8" is self-explaining without a database query.
2. **Cap `requested_through` at the covered visibility.** With `--reproject-all`,
   enqueue each account at the latest instant the copy's published balances
   cycles actually cover, not at its `finalized_through`. That lets the whale
   — the account whose reprojection matters most — be exercised on a copy.
   `EnqueueEligibilityShadowReprojection` already computes per account; the
   cap is one more subquery, and the existing integration tests pin the rest.

### Follow-ups implemented, 2026-09-05

1. `pending_accounts` in the report: every non-dead job row left when the
   drain stopped, with status, `last_error_code`, attempt count and
   `requested_through`. The bash summary prints the count; the verdict
   ignores it (a proof-pending job is not a projection error).
2. `--reproject-all` now retargets a pre-existing non-dead job to the
   account's own `finalized_through` (status queued, backoff and lease
   cleared) instead of leaving it as captured. A dead job is still left
   alone. Pinned by `TestEnqueueEligibilityShadowReprojectionRetargetsAQueuedJobToTheAccountsOwnBoundary`.

Expected on the next rehearsal: `accounts_enqueued = accounts_projected = 8`,
`pending_accounts` null. If the whale still lands in `pending_accounts`, the
reason will be in the row.
