# XM-INV-SHADOW-EVAL-VACUOUS: the shadow evaluation has never exercised a projection

- **status:** open, not started. Filed 2026-09-04 from the RC88 rehearsal.
- **branch:** none yet.
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
