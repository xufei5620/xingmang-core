# XM-INV-CATCHUP-BURST-BACKPRESSURE: chunk a released account's first projection

- **status:** fixes 1, 1b, 2 implemented and deployed (RC92/RC93, 2026-09-05); fix 3 implemented behind `ELIGIBILITY_EVIDENCE_BATCH_LIMIT` (default off) with an in-repo equivalence test, awaiting the server-side differential rehearsal before it is switched on. Root cause revised 2026-09-05 after a copy-run and a code trace; the original proposal below does not address it. Filed 2026-09-04 from the RC87 canary.
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

## Revision, 2026-09-05: what the evidence actually shows

Everything above was written on the night of the incident from log excerpts,
without reading the projection code. A reproduction on a restored copy of the
pre-incident backup (`invoice-20260904T033226Z`, rc87 images, account released
and enqueued exactly as in production) plus a trace of the code and the
surviving production rows changes the diagnosis in four places.

### 1. The proposed fix cannot bound the transaction

`reprojectEligibilityTx` (consumption.go:2216) deletes every
`consumption_allocations` row for the account and rebuilds from scratch, and
`buildEligibilityProjectionExcludingUsageTx` (1829) reads every fact from
`cutover_at`, not from `finalized_through`. A "bounded time window per
transaction" therefore replays the whole history up to the window's end on
every chunk; the last chunk is the single-pass replay. "A loop bound rather
than new machinery" is false: bounding by window requires an incremental
projection, which is new machinery.

What does scale with a bound is the evidence pass: `evaluatePendingBalanceEvidenceTx`
(3704) calls `buildEligibilityProjectionTx` once per pending checkpoint, and
once more when confirming a deferred positive. The night's transaction
evaluated 379 checkpoints in one pass (379 `pending_reconciliation.entered`
audit rows, one timestamp). Chunking by checkpoint count, with the allocation
replay left whole, is the only variant of the original idea that reduces
per-transaction work. It remains a second-order fix; see section 4.

### 2. The timeline was wrong, and so was the cause of the 503

Hard rows, not log excerpts:

| source | fact |
| --- | --- |
| `rc87-watch.log` | readyz 503 from 03:45:21 to 03:54:22, 200 briefly, 503 again 04:00:23 to about 04:14 |
| `source_economic_scan_cycles`, balances | ceiling 03:44:27 published 03:54:31 (10 min late); ceiling 03:45:39 published 04:14:40 (29 min late); ceiling 03:55:37 published 04:15:10. Every other stream published on time |
| `audit_events`, acdcdce9 | checkpoint observed 03:54:37 and 04:15:10, seconds after each late publish |
| `audit_events`, all accounts | two projections committed in the window: whale `40bd883d` at 04:16:38, `acdcdce9` at 04:16:42 |
| `source_ingest_events`, balances | two events parked as WAITING_DEPENDENCY after retries, at exactly 03:54:31 (attempt 1) and 04:14:40 (attempt 2), the late-publish instants |

The stall began at 03:44, fourteen minutes before the "03:58:22 first
deferral" the original write-up anchored on. And readiness did not go 503
because a watermark aged past 15 minutes: `runtime.go:711` marks a stream
not-ready the moment it has any pending event (`item.PendingEvents != 0`),
which is why the 503 appeared within a minute of the first stuck event. The
freshness gate is a later, separate trigger that never got the chance to fire.

### 3. The lock chain is not the one described

The projection job path locks no scan-cycle or batch row at all (verified by
enumerating every FOR UPDATE and UPDATE in consumption.go and mapping each to
its function). What it does hold for its whole SERIALIZABLE transaction:

- its own `eligibility_projection_jobs` row: FOR UPDATE at 3208, taken before
  anything else;
- the account's `source_account_eligibility_state` row: FOR UPDATE OF eas via
  `getEligibilityAccountTx(..., true)` inside `reprojectEligibilityTx`;
- every WALLET_CASH lot: FOR UPDATE OF fl,flcs at 1881;
- the per-account advisory lock for the write phase (3260).

Two ingest-side steps run into those under the role's `lock_timeout='5s'`
(deploy/postgres/010-invoice-roles.sh:18):

- Cycle publication. `tryPublishEconomicScanCyclesTx` holds FOR UPDATE on the
  stream's cycle rows (651) and the watermark row (743), marks the cycle
  published (780), and then calls `finalizeSourceAccountsTx` (789), whose
  `INSERT ... ON CONFLICT (external_account_id) DO UPDATE` on
  `eligibility_projection_jobs` (566-591) must lock the conflicting row. If
  that account's job is mid-transaction, the publish waits 5 s and dies with
  55P03, taking the whole publish, and the event mark that wrapped it, down
  with it. That is the 04:04:44 "mark source dependency wait ... lock timeout"
  line verbatim.
- Checkpoint observation. `ObserveBalanceCheckpoint` takes the advisory lock
  with `pg_try_advisory_xact_lock` (1047; non-blocking, ACCOUNT_LOCK_BUSY,
  15 s requeue) and then `getEligibilityAccountTx(..., true)` at two sites, a
  blocking row lock on the state row the projection holds. Either way the
  account's own checkpoint event cannot reach a terminal state while its
  projection runs; a non-terminal event keeps the cycle from publishing
  (tryPublish requires every event processed or parked) and, per section 2,
  flips readiness to 503 by itself.

The asymmetry in section 2 follows: finalization runs on every stream's
publish, but only the balances stream carries an event for the locked
account that must complete before its cycle can publish. The other three
streams published on schedule all night.

### 4. Fix direction, re-ranked

1. Decouple publication from the job row. `finalizeSourceAccountsTx` should
   not wait on an account whose job is processing: exclude those accounts
   from `changed` (their `requested_through` is recomputed from the
   watermarks on the next pass, so nothing is lost), or run finalization after
   the publish transaction commits so a wait there can never hold cycle rows.
   Small, local, and it removes the global blast radius regardless of how long
   any projection takes.
2. Stop a stuck checkpoint from flipping readiness. `ObserveBalanceCheckpoint`
   blocking on the state row is the second coupling; whether the answer is a
   try-lock there too, or readiness treating ACCOUNT_LOCK_BUSY as benign the
   way it treats ECONOMIC_RESCAN_ACTIVE, needs a design pass. The second
   option must not hide a genuinely stuck stream.
3. Bound the evidence pass by checkpoint count. Still worth doing for the
   300 s single-statement cliff (a job that exceeds it fails and restarts the
   whole replay under the same locks: the "36 retries" case in the code's own
   comments) and to shorten holds. Validate with the differential run the
   shadow-eval fix now makes possible: single-pass vs chunked on two restores
   of the same backup, per-account quantities diffed. Not before 1 and 2.

### The copy-run itself

The reproduction ran (report `rehearsals/20260904T212236Z-1678684`) and
stopped before the replay: the job was claimed and immediately requeued with
BALANCE_PROOF_PENDING, and the drain loop read "nothing claimable" as
"drained". Reconstruction from production shows why: the window's latest fact
visibility is 03:12:03 (the watermark of the batch that delivered a fact timed
at or before 03:11:42), and the latest published balances ceiling with a real
checkpoint at the backup instant is 03:11:00. No cover, so the proof waits for
a cycle that a frozen copy will never publish. In production that wait ended
when the balances stream finally published at 04:14:40, which is also why the
heavy evaluation committed at 04:16:42, about 90 s later, rather than after an
18-minute transaction. A second copy-run with `requested_through` chosen inside
the covered range would give the per-checkpoint replay cost; it is optional now
that the mechanism is established from rows rather than timing.

Note for the drain loop: "nothing claimable" because of backoff is
indistinguishable from "drained" in the report, the same shape as
XM-INV-SHADOW-EVAL-VACUOUS one layer down. Worth a `requeued_with_backoff`
count in the report.

## Implemented, 2026-09-05: fix 1, finalization no longer waits on a running job

`finalizeSourceAccountsTx` now locks, with `FOR UPDATE SKIP LOCKED`, only the
job rows it can take without waiting, and enqueues (a) those accounts and (b)
accounts with no job row at all. An account whose row is held by a running
projection is left out of the pass. Nothing is lost: `requested_through` is
recomputed from the stream watermarks on every pass, the running job publishes
`finalized_through` when it commits, and the next pass -- at most one poll
interval later -- enqueues whatever window is still open. The second
statement's own `NOT EXISTS (job)` guard already kept it from advancing
`finalized_through` under a live job, so the two halves stay consistent.

Pinned by `TestFinalizeSourceAccountsSkipsAJobRowHeldByARunningProjection`:
one account's processing row held `FOR UPDATE` by another connection, a
second account with an equally live window, finalization under
`lock_timeout='2s'`. On the previous code the pass dies with 55P03 after
2.01 s, the production publication failure verbatim. On this code it returns
in ~1 s, enqueues the free account, leaves the held row byte-identical (status,
lease, requested_through) and `finalized_through` unmoved, and reclaims the row
on the first pass after release.

Deliberately unchanged:

- The observe-path enqueue (`ObserveBalanceCheckpoint`'s late-fact upsert and
  its siblings) still waits. Skipping there would orphan a fact at or below an
  already-finalized boundary, because finalization's `changed` window can
  never see it again. Its wait is bounded by the same 5 s and retried by the
  event's own attempt budget; that is the correct failure mode for it.
- The job still takes its own row lock before the carry-forward proof phase.
  Moving that lock after the proof (fix 1b) would shrink every hold to the
  write phase; it reorders the hot path and needs the account-isolation tests
  the evaluator discipline requires. Not done here.
- Fix 2 (a stuck checkpoint event flipping readiness) and fix 3 (bounding the
  evidence pass by checkpoint count) are untouched.

Release note: this changes one statement in the finalization path. No
migration, no evaluator or allocation change, so the shadow evaluation is
skipped by rule; the differential run is not applicable.

## Implemented, 2026-09-05: fix 1b, the proof phase runs before the row lock

`processEligibilityProjectionJob` is now two halves.

- `prepareEligibilityProjectionJob` (lock-free): reads the job's window and
  the account without locking either, runs `ensureBalanceCarryForwardProofTx`
  in its own short transaction and commits. A proof-pending job therefore
  never holds its row at all; the caller's backoff requeue is unchanged. An
  account still awaiting its one-time legacy re-anchor skips this half, since
  re-anchoring changes the fields the proof reads.
- `completeEligibilityProjectionJob` (SERIALIZABLE, locked): exactly the old
  transaction from the `FOR UPDATE` read on. The proof is re-run only when the
  window differs from the one proved (its rows already exist, so that is
  cheap). If a finalization pass raised the row's `requested_through` while
  the lock-free half ran, the job clamps to the proved window and
  `finishEligibilityProjectionJobRowTx` requeues the row -- lease cleared, due
  now, window kept -- instead of deleting it, so the remainder is processed
  with its own proof rather than dropped.

Because the row is now reachable while a job is live, both projection-job
upserts (`finalizeSourceAccountsTx` and the observe-path enqueue) treat a
`processing` row like a `dead` one for everything except `requested_through`:
status, lease and `next_attempt_at` are left alone. Without that, a fact
arriving every minute would strip a running whale job of its lease every
minute and it could never finish. Reclaiming a crashed worker's row was never
this upsert's job; the claim step's lease-expiry rule does it.

Pinned by three tests in `finalize_skips_running_job_integration_test.go`:
the merge-only rule on a live processing row (window may rise, status/lease/
next_attempt unchanged); the row finish for an exact window (deleted) and a
raised one (requeued with the window kept); and the fix-1 skip test, whose
final assertion was corrected from "reclaimed to queued" to "claim intact".

Still open: fix 2 (a stuck checkpoint event flipping readiness by itself) and
fix 3 (bounding the evidence pass by checkpoint count, to be validated with
the differential rehearsal now that the whale can be exercised on a copy).

## Implemented, 2026-09-05: fix 2, a busy checkpoint event no longer flips readiness by itself

Readiness counted every `queued`/`failed`/`processing` ingest event as pending
and went 503 the moment one existed (`runtime.go`: `item.PendingEvents != 0`).
An event requeued with `ACCOUNT_LOCK_BUSY` is one that found its account's
projection in the write phase and will retry in fifteen seconds; on
2026-09-04 that, not a stale watermark, turned readiness off at 03:45.

`sourceReadinessHealthQuery` now leaves a `queued` event with
`processing_error='ACCOUNT_LOCK_BUSY'` out of `pending_events` (and
`oldest_pending`) while its `updated_at` is within
`accountLockBusyReadinessGrace` (10 minutes), and counts it again after that.
The bound is deliberately shorter than `SOURCE_ECONOMIC_WATERMARK_MAX_STALENESS`
(15 minutes), so a stream genuinely stuck behind a lock still fails readiness
through the freshness gate: this stops a short, expected wait from
masquerading as an outage, nothing more. A plain queued event is pending at
once, as before.

Pinned by `TestSourceReadinessHealthGivesAnAccountLockBusyEventABoundedGrace`:
busy now → not pending, ready; busy for eleven minutes → pending, not ready;
the same event without the busy marker → pending at once.

With fixes 1, 1b and 2 in, the three couplings the 2026-09-04 stall ran on are
each addressed at their own layer: publication no longer waits on a job row,
a job holds its row only for the write phase, and a short busy wait is not an
outage. Fix 3 (bounding the evidence pass by checkpoint count) remains, and
should be validated with the differential rehearsal before it ships.

## Implemented behind a knob, 2026-09-05: fix 3, the evidence pass bounded by item count

`Store.SetEvidenceBatchLimit(n)` (api: `ELIGIBILITY_EVIDENCE_BATCH_LIMIT`,
default `0` = unbounded; rehearsal: `--evidence-batch-limit N`, recorded in
the report as `evidence_batch_limit`). With a limit, `completeEligibilityProjectionJob`
cuts the window at the as_of of the limit-th pending balance-evidence item
(`evidenceBatchBoundaryTx`, selecting items exactly as the evaluation pass
does -- same anchor floor, both sources, same order -- and keeping a shared
instant whole), never below `finalized_through`, and everything else runs as
before; the row finish from fix 1b requeues the remainder because the row
still asks for more than was published. A window at or below the proved one
needs no second proof.

Why a boundary cannot change a decision, and what pins it:

- An item deferred at the end of a pass is written nowhere -- the loop's
  `continue` skips its evaluation and nothing after the loop writes it -- so
  the next pass re-evaluates it from the same durable facts as its first
  item. The consecutive-match counter lives in
  `source_account_eligibility_state`. Nothing cross-item is held in memory
  across a boundary.
- `TestEvidenceBatchLimitChunksTheEvidencePassWithoutChangingItsResult`: an
  anchored account with three checkpoints, driven with limit 1, drains in at
  least three jobs, each chunk landing on a checkpoint's as_of, and ends with
  the same evaluation rows in the same order, the same `finalized_through`
  and the same consumed cash as a single pass on a fresh copy.
- `TestEvidenceBatchBoundaryCutsAtTheLimitThItemAndKeepsASharedInstantWhole`
  pins the boundary arithmetic, including the shared-instant and
  already-evaluated cases.

Two things the tests taught that the design had not said:

- Pending items include carry-forward proofs the lock-free half inserts, and
  those can predate `finalized_through` (the cutover cycle's, for one).
  Cutting below the published boundary would evaluate them and publish
  nothing, so the boundary is floored at `finalized_through` and such items
  ride along with the first chunk that advances.
- The anchor checkpoint is evaluated at observation as well as by the pass,
  so evaluation rows outnumber checkpoints; the equivalence is between the
  two modes' rows, not against the checkpoint count.

Done (2026-09-05, RC98): the forward-only differential on the incident's own
backup passed -- `invoice-20260904T033226Z`, `--release-catchup` for `acdcdce9`,
`--finalization-window --finalization-window-provable`, `--reproject-all` twice with
`--evidence-batch-limit 0` and `25`: both `ready`, the released account replayed its
three days once in a single pass and once in 22 chunks, `finalized_through`,
`consumed_cash_minor`, every other published quantity and `evaluations_by_status`
identical (see the RC98 plan's execution record). Switching it on turned out to
need a release after all: `docker-compose.prod.yml` lists the api's environment
explicitly and never passed `ELIGIBILITY_EVIDENCE_BATCH_LIMIT` through -- RC94's
canary noted the variable absent from the container and read that as "off",
which it was, but no value in the release env could have turned it on. RC99
adds the passthrough (`${ELIGIBILITY_EVIDENCE_BATCH_LIMIT:-0}`), documents the
variable, and is rolled forward with `ELIGIBILITY_EVIDENCE_BATCH_LIMIT=25` set in
its release env, under the usual backup and 30-minute canary with the variable
confirmed inside the api container. Until that roll-forward the api runs with
`0`, which is byte-for-byte the previous behaviour.

### Fix 3 hardening, same day

Two gaps the first cut left, both found by asking what the equivalence test
had not exercised:

- **Progress.** The boundary used to count every pending item at or below
  the window and floor the cut at `finalized_through`. An item deferred at
  the end of a pass is unwritten and sits exactly at the published boundary,
  so the next pass could cut on it again — deferred, unwritten, published at
  the same instant, requeued — forever. The boundary now counts only items
  strictly after `finalized_through`, so every chunk advances past at least
  one new item; the deferred item rides along and the chunk's first new item
  confirms or disconfirms it, exactly as the single pass would.
- **The deferral path at a boundary was untested**: the constant-balance
  fixture evaluated everything as `matched`.
  `TestEvidenceBatchLimitCarriesADeferredPositiveAcrossTheChunkBoundary`
  reports 500/600/600/600, drives the queue with a bound of one, and requires
  the same rows as a single pass — `[positive_classified_non_cash matched
  positive_classified_non_cash matched matched]` in both modes, four bounded
  jobs, no spin.

And the rehearsal side: a restored copy already carries every evaluation
production has made, so the differential pair was going to compare two runs
that evaluated nothing. `--reevaluate-evidence` (see the shadow-eval handoff)
clears the copy's evaluations first -- which turned out not to be enough,
and the rewind that followed it turned out to be unsound; see the next
section.

### Differential rehearsal, take three: the rewind was unsound (2026-09-05, RC95)

RC94's pair compared two identical runs: on a current backup every evaluation
already exists and the cleared ones lie below `finalized_through`, which the
bound counts from, so the bounded run had nothing to chunk. RC95 made
`--reevaluate-evidence` also rewind every POLICY_ANCHOR account's
`finalized_through` to its cutover so the cleared pile would sit in front of
the boundary. The rewind engaged (4 of 8 accounts) and so did the bound, and
on the two rewound accounts without cash the bounded replay finished with
quantities identical to the single pass -- but on the two cash accounts every
chunk failed at commit: `wallet consumption state lacks matching usage
allocations` (SQLSTATE P0001, `consumption_allocations_mirror_guard`, migration
0011). A chunk's rebuild deletes all of the account's `consumption_allocations`
and re-inserts only those for usage up to its boundary, while the lot loader
takes only lots with `completed_at <= through`; a lot funded after the first
boundary kept the consumption state the copy carried from production and now
had no allocations.

That is not a defect of the bound. A published boundary behind lots that
already carry consumption is a state production never enters:
`completeEligibilityProjectionJob` clamps `requested` up to `finalized_through`,
`finalized_through` only moves by `GREATEST`, and a lot's consumption state is
written only by a projection whose window covered it, after which the
boundary covers it too. The re-anchor migration, the one legitimate backward
move, zeroes allocations, lot states and `consumed_cash_minor` in the same
transaction -- and even that reset would not make the rewind sound, because
`acdcdce9` has an issued invoice and a partial replay from cutover computes
consumption below `reserved+issued`, which the lot update refuses. Forward-only
chunking keeps every invariant by construction; a rewind cannot be the
instrument that proves it.

RC96 therefore removes the rewind (`--reevaluate-evidence` clears evaluations
and nothing else; `accounts_rewound` is gone from the report) and fixes the
other half of the instrument: `--reproject-all` used to overwrite a captured
job's `requested_through` with `finalized_through`, which on a backlog-era
backup erased exactly the window the differential needs -- it now raises and
never lowers. The acceptance moves to a forward-only replay of the incident
itself. Backups taken after the burst carry no backlog -- on
`invoice-20260904T065429Z` every account's `finalized_through` already stood at
06:27-06:33Z and a real-queue pair projected nothing -- because the backlog
window existed only between the RC87 post-deploy repair (~03:58Z) and the
replay that landed at 04:16:42Z. The pre-repair backup
`invoice-20260904T033226Z` is the incident's true starting state, so RC96 adds
two rehearsal-only operations that replay what happened next on the copy:
`--release-catchup <account>` clears `catchup_key_hmac` exactly as the repair did,
and `--finalization-window` queues every finalization target through the
window a finalization pass would have requested at the copy's last
watermarks (`GREATEST(cutover, min stream watermark - finalization delay)`,
never below `finalized_through`, never lowering a captured window). Both refuse
a non-superuser session. The bound's engagement (`projection_version` strictly
greater in the bounded report for the released account) is an explicit
acceptance criterion. RC95 was not rolled forward; production stayed on RC94
with the bound off.

### Take four: the instrument works, the frontier proof does not close on a frozen copy (2026-09-05, RC96)

RC96's pair on `invoice-20260904T033226Z` with `--release-catchup acdcdce9` and
`--finalization-window`: the release cleared the key (`accounts_released` 1), all
eight accounts were queued through the window finalization would have
requested (03:11:42Z, the minimum watermark minus the 900 s delay), six
projected and both runs came back `ready` -- but the released account never
replayed. Its job sat in `BALANCE_PROOF_PENDING` (as did `40bd883d`'s), because
`ensureBalanceCarryForwardProofTx` covers each fact's visibility (the stream
watermark it was ingested under) with a balances checkpoint or a published
balances cycle whose ceiling lies inside the window, and the facts nearest
the frontier were ingested under watermarks later than the window end. In
production the next finalization pass widens the window and the proof
closes; on a frozen copy there is no next pass. RC97 adds
`--finalization-window-lag`: the window a finalization pass would have requested
that much earlier, so an hour of lag leaves the three-day catch-up window
whole but provable. RC97 also restores the `os.Exit` that RC96's
`--reevaluate-evidence`-without-`--reproject-all` check had lost (it logged and
went on). Neither RC96 nor RC97 changes anything production runs.

### Take five: the last seconds of the window (2026-09-05, RC97)

RC97's pair with an hour of lag left `acdcdce9` `BALANCE_PROOF_PENDING` again, at
window 02:11:42Z. A read-only probe of the restored copy (no evaluator, just
SQL) showed the lag reasoning was wrong: the account's 1,399 usage facts
across the three days were ingested continuously (visibility lag p50 1 min,
p90 3 min, max 22 min), 550 reconciliation checkpoints sat unevaluated,
balances cycles closed every ~65 s. What blocks the proof is the last slice
of any window: the facts just before the window end are seen a few seconds
to minutes later, and `ensureBalanceCarryForwardProofTx` needs a balances cycle
ceiling inside the window at or after that visibility. RC96's window
(03:11:42Z) held a fact seen at 03:12:03Z -- impossible; RC97's (02:11:42Z)
held one seen at 02:11:25Z and the cycles closed at 02:10:45Z and 02:11:50Z --
none in the seventeen seconds that mattered. Production asks again a minute
later with a wider window and one of the next requests lands; a frozen copy
asks once. The probe also showed where the proof does close: the latest
balances cycle ceiling by which every fact before it had been seen was
03:06:41Z below the un-lagged window and 01:50:44Z below the lagged one, each
with its final ingest batch and a prior checkpoint. RC98 adds
`--finalization-window-provable`, which cuts each account's window to exactly
that ceiling (usage and credit facts by `stream_watermark_at`, cash lots by their
payments cycle ceiling -- the proof's own visibility set); the lag flag
stays but is no longer the instrument. Neither RC97 nor RC98 changes
anything production runs.
