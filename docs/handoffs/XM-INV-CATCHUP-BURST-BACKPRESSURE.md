# XM-INV-CATCHUP-BURST-BACKPRESSURE: chunk a released account's first projection

- **status:** open, not started. Filed 2026-09-04 from the RC87 canary.
- **branch:** none yet.
- **found in production**, 2026-09-04, while verifying the XM-INV-CATCHUP-RELEASE fix.

## Symptom

Production readiness answered 503 for roughly seventeen minutes, 04:00Z to
about 04:17Z, with zero error lines and zero reconcile errors. The RC87
30-minute canary recorded 17 non-200 probes and did not pass.

## What happened

From 03:58:22Z to 04:14:03Z one batch on the sub2api `balances` stream
(sequence 30350) was deferred every minute:

```
WARN source batch commit deferred: active scan cycle busy status=503
  stream_id=balances error="stream already has an active scan cycle"
```

Two lock timeouts sit inside that window:

```
ERROR background worker failed worker=source-projection
  error="mark source dependency wait (stream=balances event=eba17030):
  canceling statement due to lock timeout (SQLSTATE 55P03)"
ERROR source batch commit rejected stream_id=credits sequence=12759
  status=409 error="canceling statement due to lock timeout (SQLSTATE 55P03)"
```

A stream that cannot commit its batch stops advancing its watermark, and the
readiness freshness gate turns 503 on a stale watermark.

## Root cause

The trigger was releasing account `acdcdce9-c7f4-4cb4-9a02-ce527849a440` from
the catch-up exclusion that XM-INV-CATCHUP-RELEASE fixed. That account had
been excluded from finalization since 2026-09-01 12:14:31, so its first
projection after release had three days of economic facts to replay in one
pass. The audit shows the whole replay landing as a single
`eligibility.projection.rebuilt` at 04:16:42Z with 378
`eligibility.pending_reconciliation.entered` rows behind it.

That transaction holds the rows the ingest path needs in order to mark a
dependency wait, long enough for `lock_timeout` to fire on the ingest side.
The balances stream lost the race; credits lost it once too.

Nothing was wrong with the fix. The hole is that a catch-up release has no
backpressure: however much backlog an account accumulated, its first
projection tries to evaluate all of it inside one transaction, on the same
rows the live ingest path is using.

## Why it matters beyond this one account

It self-resolved here, and no data was lost. But the shape recurs whenever an
account is released after a long exclusion, and the blast radius is a global
readiness outage rather than one account's numbers. The bigger the backlog,
the longer the stall. A larger account, or several released at once, would
stall the ingest path proportionally longer.

## Proposed fix

Chunk the projection for an account whose `finalized_through` is far behind
`now()`: process a bounded time window per transaction (an hour, or a bounded
count of checkpoints), commit, and re-enqueue until caught up. The projection
job queue already supports re-enqueue and backoff, so this is a loop bound
rather than new machinery.

Two things to preserve while doing it:

- The evaluation must stay ordered. Checkpoints are evaluated against the
  facts visible at their own `as_of`, so windows have to advance forward and
  never overlap.
- Each chunk must leave a consistent state. `finalized_through` advances only
  to the end of the window actually committed, so an interrupted catch-up
  resumes rather than restarts.

## Acceptance

1. An account released with a multi-day backlog catches up across several
   committed chunks rather than one transaction, verifiable from the audit
   trail showing more than one `eligibility.projection.rebuilt`.
2. During that catch-up, no ingest stream watermark goes stale and readiness
   stays 200 throughout, on a real database with the ingest path running.
3. The final `finalized_through`, `consumed_cash_minor`, and evaluation set
   are identical to what the single-pass implementation produces for the same
   input. This is the regression that matters: chunking must not change any
   eligibility decision.
