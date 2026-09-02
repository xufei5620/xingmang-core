# XM-INV-PROOF-CONTENTION: settlement lock contention / livelock fix

- **status:** fully implemented and self-tested locally — all five required changes landed, full
  backend suite green (`go test -p 1 -count=1 ./...`, every package), `go vet` clean.
- **branch:** `ai/claude/XM-INV-PROOF-CONTENTION` (based on `ai/claude/XM-INV-AUTOLOGIN` at
  `2b92cbf`, the RC68 line), worktree `K:/发票/wt-XM-INV-CONTENTION`.
- **commits:** five, in dependency order (oldest first):
  1. `9b2f9b3` fix(sourceingest): attribute EVENT_DEAD freezes via the application layer's
     resolved account (requirement 5)
  2. `1f30367` fix(sourceingest): non-blocking per-account lock in the worker, isolate per-claim
     failures (requirements 2's worker half + requirement 3)
  3. `03e1a9c` fix(ledger): exponential backoff for BALANCE_PROOF_PENDING, honored by the requeue
     upserts (requirement 1)
  4. `8f4f44e` fix(ledger): defer the per-account advisory lock to the projection job's write
     phase (requirement 2's lock-discipline half)
  5. `cc5f230` perf(ledger): set-based balance carry-forward proof evaluation (requirement 4)

  Requirements were implemented in an order chosen for testability (2's worker half + 3 needed to
  land together to write one contention-reproduction test; 4 was done last since its own tests
  are the most expensive to run repeatedly), not the order listed in the task. No schema changes
  — every fix is application/query-level, using existing `eligibility_projection_jobs` and
  `source_ingest_events` columns.

## Incident recap (for context; see the team lead's brief for the full production timeline)

A projection job for one sub2api account (external_account_id `40bd883d…`) retried every 30s for
500+ attempts with `last_error_code='BALANCE_PROOF_PENDING'`, each attempt re-running the
per-visibility balance carry-forward proof loop (up to ~1,150 checkpoints, ~42ms each) while
holding the account's advisory lock the whole time. The source-projection worker's own attempt to
process that account's events blocked on the same lock and eventually hit
`canceling statement due to lock timeout (SQLSTATE 55P03)`, which aborted the worker's entire
batch (the old `RunOnce` returned on the first per-claim failure), stranding the rest of the batch
in `processing` for the full 10-minute lease. The stream's scan cycle never completed, its
watermark never advanced, and the proof stayed pending — a livelock that only broke when the job
was manually paused with a fake lease.

## The five changes

### 1. Exponential backoff for BALANCE_PROOF_PENDING, honored by the requeue upserts

**Backoff rule** (documented in code at `consumption.go`'s
`balanceProofPendingBackoffCapSeconds`/`balanceProofPendingRequeueResetWindow` constants): the
`ProcessEligibilityProjectionJobs` requeue on `errBalanceCarryForwardProofPending` now sets
`next_attempt_at = now + LEAST(600, 30 * 2^(attempt_count-1))` seconds, keyed off the job's own
`attempt_count` (already incremented by the claim step before this runs): **30s, 60s, 120s, 240s,
480s, then capped at 600s (10 min) from the 6th attempt on.** `attempt_count` itself is untouched
by this change — it still only increments in the claim step, so counting stays honest.

**Requeue-preserving rule** (the "at most once per N minutes" the team lead asked for, with the
exact rule spelled out): both `ON CONFLICT(external_account_id) DO UPDATE` upserts that fire when
a new fact is observed for an account (`finalizeSourceAccountsTx`, source-wide, fired once all
four of a source's stream watermarks are present; and the one inside `ObserveBalanceCheckpoint`
for a checkpoint at/before `finalized_through`) used to reset `next_attempt_at=now()`
unconditionally. That is what actually caused the livelock in production: since facts kept
streaming in for the contended account while its proof stayed pending, every one of them re-armed
the job immediately, collapsing the backoff back to sub-second retries in practice. The rule is
now:

```
next_attempt_at = CASE
    WHEN status='queued' AND last_error_code='BALANCE_PROOF_PENDING'
         AND next_attempt_at <= now() + 5 minutes   -- balanceProofPendingRequeueResetWindow
    THEN next_attempt_at        -- unchanged: already due soon, leave it alone
    ELSE now()                  -- pull forward: this is a genuinely new opportunity to retry
END
```

In words: a new fact pulls a `BALANCE_PROOF_PENDING` job's schedule forward to "now" only when it
is currently more than 5 minutes away from its next attempt; an already-shallow backoff (due
within 5 minutes) is left alone, and a job not currently in this state keeps the pre-existing
immediate-requeue behavior untouched. This bounds how often a burst of facts can re-arm a deeply
backed-off job to strictly less often than the backoff itself grows past the 5-minute mark again
(which only happens after further real, counted failures) — it cannot re-arm on every single fact
the way the old unconditional reset did, while still being responsive to genuinely new
information once the account has been waiting a while.

`status`/`lease_token`/`lease_expires_at` in both upserts keep their pre-existing unconditional
reset — only `next_attempt_at`'s formula changed. Both `ON CONFLICT DO UPDATE` sites carry the
byte-for-byte identical `CASE` expression (verified with `grep -n -A7 "next_attempt_at=CASE"`).

**Tests:** `TestBalanceProofPendingBackoffGrowsExponentiallyPerAttempt` (drives seven real
attempts through `ProcessEligibilityProjectionJobs` against an account whose proof is
deterministically pending — a usage fact with no covering published balances cycle anywhere near
it — and asserts the exact 30/60/120/240/480/600/600s sequence and honest `attempt_count`),
`TestBalanceProofPendingRequeuePreservesDeepBackoffButPullsInShallowOne` (calls
`finalizeSourceAccountsTx` directly in its own transaction, standing in for "a new fact was
observed," and proves a shallow ~30s backoff is left untouched while a synthetic 10-minute-deep
one is pulled forward to ~now — both without touching `attempt_count`).

### 2. Lock discipline: short holds in the projection job, non-blocking in the worker

**Worker side** (`ObserveBalanceCheckpoint`, and `observeEligibilityFact` — the function shared by
`ObserveUsageEvent`/`ObserveCreditEvent`): both now call `pg_try_advisory_xact_lock` instead of
the blocking `pg_advisory_xact_lock`. A busy account (`false` returned) surfaces as the new
`domain.ErrAccountLockBusy` sentinel instead of blocking. These are the *only* two call sites for
the per-account lock (namespace 43) reachable from the source-projection worker — confirmed by
`grep` for `.ObserveUsageEvent(`/`.ObserveCreditEvent(`/`.ObserveBalanceCheckpoint(`: every
production call site is in `application/source_processor.go`'s `process*Event` functions.

The application layer's `RunOnce` treats `ErrAccountLockBusy` as routine contention: it calls the
new `Store.MarkSourceEventBusy` (reuses the existing `queued` status — no schema change — sets
`next_attempt_at = now+15s`, credits `attempt_count` back by 1 exactly like the pre-existing
`waiting_dependency` path does, so a run of busy-reschedules never spends the event's real
attempt/retry budget) instead of erroring.

**Projection job side** (`processEligibilityProjectionJob`): the per-account advisory lock used to
be acquired first thing, before the reanchor check and `ensureBalanceCarryForwardProofTx`'s
evaluation, and held for the whole transaction. It (and the account's row lock, via
`getEligibilityAccountTx`'s `lock` parameter) is now acquired only once, **immediately before the
write phase** (`reprojectEligibilityTx` on), after a successful (non-pending) proof evaluation.
The initial account fetch (and the re-fetch after a reanchor) uses `getEligibilityAccountTx(...,
lock=false)`: reanchor is a one-time, per-account transition and the proof evaluation is
read-mostly, and the job-claim `SKIP LOCKED` handoff in `ProcessEligibilityProjectionJobs` already
guarantees only one `processEligibilityProjectionJob` execution per account runs at a time, so
neither step needs the advisory lock to exclude another instance of itself; `SERIALIZABLE`
isolation still catches any genuine conflict with a concurrent writer (e.g. a worker-side freeze)
at commit, and that job's own backoff retries it. A job whose proof turns out pending now returns
*before ever touching the lock at all*.

**Tests:**
`TestSourceProjectionWorkerSkipsAccountLockedByProjectionJobWithoutAbortingBatch` (reproduces the
incident directly: a raw, held-open transaction simulates a long-running projection job's write
phase holding account A's lock; `RunOnce` is called on a batch containing both account A's event
and account B's; asserts `RunOnce` returns within 10s with no error, account A is
busy-rescheduled without spending its attempt budget, and account B's unrelated claim reaches its
own terminal state in the same call — see requirement 3 below for why that second assertion
matters) and `TestProcessEligibilityProjectionJobEvaluatesProofWithoutTheAdvisoryLock` (the other
direction: an account's lock is held externally throughout, and a job whose proof is
deterministically pending for that account is proven to complete promptly regardless, since it
never needs the lock at all).

### 3. Worker fault isolation

`SourceEventProcessor.RunOnce` used to `return` immediately the moment any single claim's
bookkeeping call (`MarkSourceEventWaitingDependency`/`MarkSourceEventFailed`/
`MarkSourceEventProcessed`) failed, aborting the rest of the batch. Every remaining claim in that
batch was left in `processing_status='processing'` holding its lease for the full 10-minute
`sourceEventLease` with no worker able to touch it in the meantime — this is what turned one
lock-timeout into a stream-wide stall.

Every claim now runs to its own terminal outcome unconditionally; an isolated bookkeeping failure
increments a local `isolated` counter and records the last such error, but the loop always
proceeds to the next claim. If any claims were isolated, `RunOnce` returns a summary error at the
end (`"source-projection run summary: claimed=%d processed=%d isolated_failures=%d; last
isolated error: %w"`) instead of erroring immediately — this still surfaces through
`cmd/api/runtime.go`'s existing `slog.Error("background worker failed", ...)` log line (counts
included in the message), which is where the `application` package's convention already puts all
of this worker's logging; no new logging call was added to `application` itself. `processed`
still only counts claims that reached a real terminal state (including busy-reschedule), matching
its pre-existing meaning used by the runtime harness to decide whether to loop again immediately.

**Test:** covered together with requirement 2's contention-reproduction test above (account B's
claim reaching its own outcome in the same `RunOnce` call as account A's busy-reschedule is
exactly "the rest of the batch proceeds").

### 4. Proof loop efficiency: set-based evaluation

`ensureBalanceCarryForwardProofTx` used to issue up to two queries per entry of its
`visibilities` list (one checking for real-checkpoint coverage, one falling back to delta-carry
coverage, each parameterized by that specific visibility). Both are "smallest candidate ceiling
>= a threshold" lookups against candidate sets that don't actually depend on which visibility
triggered them. The rewrite prefetches each candidate set exactly once — real checkpoints tied to
their own arrival cycle (unbounded above, exactly like the original), and every published
delta-carry cycle in the window with its prior-checkpoint delta and `has_real_checkpoint` flag
precomputed per row — then walks the (already sorted, deduplicated) `visibilities` list against
both prefetched lists with an ordinary ascending two-pointer merge. This is valid for the same
reason a merge-join is valid on two sorted inputs: `visibilities` is processed in strictly
ascending order, and each prefetch list is fetched pre-sorted by ceiling, so a forward-only
pointer into each correctly finds "smallest candidate >= current visibility" at every step,
identically to the original per-call queries. The preference order (real checkpoint beats
delta-carry regardless of which has the smaller ceiling) and every write (idempotent
`balance_carry_forward_proofs` inserts, `ON CONFLICT DO NOTHING` with the same integrity check on
conflict) are unchanged.

**Equivalence:** every existing carry-forward integration fixture passes unchanged —
`TestBalanceDeltaCarryForward{FreezesUnchangedActualMismatch,MatchedNetFactsFinalize,
MissingProofFailsWithoutAdvancing,UsesLatestLowerSequenceActualAtSameAsOf,
RejectsUnmappedFundingVisibility,UsesMappedFundingVisibility}`,
`TestBalanceEvidenceEvaluatesCarryBeforeLaterRealCheckpoint`, plus every policy-anchor and
consumption integration test that exercises `ProcessEligibilityProjectionJobs`. None of their
fixtures or assertions were touched.

**EXPLAIN ANALYZE comparison** (new test
`TestBalanceCarryForwardProofScalesToOneThousandCheckpoints`, 1,000 usage-fact visibilities, each
covered by its own tiny published balances cycle so the jump-ahead skip never applies — the exact
worst case the incident hit):

| | calls needed for 1,000 visibilities | cost per call (EXPLAIN ANALYZE) |
|---|---|---|
| **Old** per-visibility query (one representative call, bound to the 500th visibility) | up to 1,000 | Execution Time **51.776 ms**, Buffers: shared hit=26427 |
| **New** prefetch query (one call, unbounded across the whole window) | 1 | Execution Time **79.120 ms**, Buffers: shared hit=38515 |

The new query's single execution (79ms) already costs less than *two* calls of the old query
(2×51.8ms≈104ms) would have — and the old code needed up to 1,000 of them in the worst case
(≈51.8 seconds), which is what actually produced the incident's "tens of seconds per attempt."
The full end-to-end `ProcessEligibilityProjectionJobs` call for this fixture (prefetch + both
`reprojectEligibilityTx` passes + evidence evaluation + finalize, i.e. everything, not just the
proof) completed in **6.46s** for all 1,000 visibilities — faster than the old proof loop *alone*
would have been. Full plans (including the nested-loop/index-scan structure on both sides) are in
the test's own `-v` log output:
```
go test ./internal/postgresstore/... -run TestBalanceCarryForwardProofScalesToOneThousandCheckpoints -v -count=1
```
This test is part of the permanent suite (not a throwaway script) so the comparison stays
reproducible; it costs about 39s of the suite's total runtime, almost all of it spent seeding the
1,000 cycles through the real `CommitSourceBatch`/publish pipeline for realism, not in the
queries under measurement.

### 5. EVENT_DEAD attribution gap (design XM-INV-POLICY-ANCHOR 2.5's documented follow-up)

`MarkSourceEventFailed`'s EVENT_DEAD freeze could only attribute a dead event to an account by
joining its `payload_hash` against an already-persisted domain fact
(`source_usage_events`/`source_credit_events`/`balance_reconciliation_checkpoints`). An event
whose every attempt was rejected before any such INSERT (e.g. a deterministic validation failure)
left nothing to correlate — even though the application layer had already decrypted the payload
and resolved the account on that very attempt.

`processUsageEvent`/`processCreditEvent`/`processBalanceCheckpoint` now wrap their returned error
with the account they'd already resolved (`wrapWithAccountHint`, a small unexported
`error`-implementing type with `Unwrap()`, so the existing `*sourceDependencyWait`
`errors.As` check in `RunOnce` still works through it unchanged). `RunOnce` unwraps this hint only
on the path that would otherwise call `MarkSourceEventFailed`, and passes it through as a new
`hintAccountID` parameter. `MarkSourceEventFailed`'s dead-status branch tries the existing
`source_revision_hash` correlation first (unchanged, still preferred when it finds a match, since
it points at a real persisted fact) and falls back to the hint only when that correlation finds
nothing and a hint was supplied — pointing `trigger_object_type`/`trigger_object_id` at the
ingest event itself (`claim.EntityType`/`claim.EventID`) since there is no persisted fact to
reference. Decrypt failures (payload never parsed at all) are unaffected — no hint is ever
available in that case, so behavior there is unchanged, matching "keep the existing behavior when
the payload cannot be decrypted."

**Test:** `TestDeadUsageEventWithoutPersistedFactStillFreezesViaApplicationLayerAccountHint` — a
usage fact with a `unit_code` that never matches the source's expected unit is rejected by
`observeEligibilityFact`'s `domain.ErrConflict` check on every attempt, before any
`source_usage_events` row is ever written (asserted directly). Drives one real attempt, then
fast-forwards `attempt_count` to 7 via direct SQL (avoiding seven redundant identical attempts)
and drives the 8th for real; asserts the event reaches `dead`, an `EVENT_DEAD` freeze exists for
the correct account with `trigger_object_type='usage_event'` and `trigger_object_id` equal to the
event's own id, and — the actually-motivating production effect — that the stream's scan cycle
subsequently publishes (`tryPublishEconomicScanCyclesTx`'s completeness query treats a
dead-with-open-freeze event as complete).

## Files changed

- `backend/internal/postgresstore/consumption.go` — backoff constants/formula and requeue-upsert
  `CASE` (both sites), `processEligibilityProjectionJob`'s lock restructure, `ObserveBalanceCheckpoint`'s
  try-lock, `observeEligibilityFact`'s try-lock, `ensureBalanceCarryForwardProofTx`'s set-based
  rewrite and updated doc comments.
- `backend/internal/postgresstore/source_sync.go` — `MarkSourceEventFailed`'s new `hintAccountID`
  parameter and dead-branch fallback, new `MarkSourceEventBusy`.
- `backend/internal/application/source_processor.go` — `deadEventAccountHint`/`wrapWithAccountHint`,
  the three `process*Event` functions now capture and forward the resolved account, `RunOnce`'s
  fault-isolation rewrite and `ErrAccountLockBusy` handling.
- `backend/internal/domain/types.go` — new `ErrAccountLockBusy` sentinel.
- `backend/internal/postgresstore/proof_contention_backoff_integration_test.go` (new) — requirement
  1's two tests plus the shared always-pending fixture, and requirement 2's
  evaluate-without-the-lock test (reuses the same fixture).
- `backend/internal/postgresstore/proof_contention_scale_integration_test.go` (new) — requirement
  4's 1,000-checkpoint scale/EXPLAIN test.
- `backend/internal/application/service_integration_test.go` — requirement 5's dead-event test and
  requirement 2/3's contention-reproduction test.

**Not touched:** any migration file (no schema changes were needed); `release/`; `scripts/`;
`RELEASE-READINESS.md`; `docs/PRODUCTION-RUNBOOK.md`; `docs/IMAGE-SCAN-REVIEW.md`;
`docs/superpowers/plans/`; any RC version identity; `contracts/`; anything under
`internal/postgresstore` unrelated to this slice's five call sites.

## Tests run

From `backend/`, with `GOFLAGS=-buildvcs=false`, the eight proxy variables unset, and
`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_contention?sslmode=disable`
(a dedicated database created in the already-running `invoice-test-pg` container, separate from
the shared `invoice_test` other agents use):

```
go build ./...                 # exit 0
go vet ./...                   # exit 0
go test -p 1 -count=1 ./...    # all 20 testable packages ok, ~110s total
```

Full package list, all passing: `cmd/api`, `cmd/bootstrap-settings`, `cmd/bootstrap-sources`,
`cmd/keygen`, `cmd/migrate`, `cmd/mtlsgen`, `cmd/oidc-preflight`, `cmd/pdf-policy-check`,
`internal/adminsettings`, `internal/application` (15.7s), `internal/auth`, `internal/backuparchive`,
`internal/backupverify`, `internal/document`, `internal/domain`, `internal/httpapi`,
`internal/ledger`, `internal/mailer`, `internal/migrate`, `internal/oidcretention`,
`internal/pdfscanner`, `internal/postgresstore` (77.0s — includes the new ~39s 1,000-checkpoint
scale test), `internal/securefields`, `internal/sourceingest`.

New tests added by this slice (11 total, all passing individually and as part of the full run,
multiple times across this session):
- `TestDeadUsageEventWithoutPersistedFactStillFreezesViaApplicationLayerAccountHint` (application)
- `TestSourceProjectionWorkerSkipsAccountLockedByProjectionJobWithoutAbortingBatch` (application)
- `TestBalanceProofPendingBackoffGrowsExponentiallyPerAttempt` (postgresstore)
- `TestBalanceProofPendingRequeuePreservesDeepBackoffButPullsInShallowOne` (postgresstore)
- `TestProcessEligibilityProjectionJobEvaluatesProofWithoutTheAdvisoryLock` (postgresstore)
- `TestBalanceCarryForwardProofScalesToOneThousandCheckpoints` (postgresstore)

`gofmt`: `"$(go env GOROOT)/bin/gofmt" -d <touched files>` shows whole-file `@@ -1,N +1,N @@`
diffs (same line count before/after) on every file edited via the `Edit` tool — this is the
known CRLF-vs-LF false alarm documented for this repo (Windows checkout stores `.go` files as
CRLF; `gofmt` emits LF; a real formatting problem would show a *different* line count, not just
different line endings). The two new files (written directly, LF-native) show zero diff. Did not
run `gofmt -w` anywhere, repo-wide or otherwise, per the task's instruction.

## Not run

- Anything requiring a server/production connection or a release/deploy step — out of scope for
  this worktree/task, explicitly excluded (no release ceremony, no image gate, no
  release-identity files touched).
- A dedicated test exercising the *second* requeue-upsert site (inside `ObserveBalanceCheckpoint`)
  end-to-end through the full checkpoint-observation pipeline — its SQL is verified
  byte-for-byte identical to the site that *is* tested directly
  (`finalizeSourceAccountsTx`, see requirement 1's test), and driving it through
  `ObserveBalanceCheckpoint` would require substantially more fixture (batch-context validation,
  scan-cycle-event mapping) for the same `CASE` expression already covered. Flagged as a
  follow-up, not blocking.

## Risks / things to sign off on

1. **The lock-discipline restructure (requirement 2) relies on an invariant, not just on
   `SERIALIZABLE` isolation:** that the job-claim `SKIP LOCKED` handoff in
   `ProcessEligibilityProjectionJobs` already guarantees only one `processEligibilityProjectionJob`
   execution per account runs at a time. This was true before this change too (nothing in this
   slice touches the claim mechanism), but the *consequence* of relying on it is new: reanchor and
   the proof evaluation no longer hold a row/advisory lock during their read, where before they
   did. If that invariant were ever violated (e.g. a future change lets the same account's job be
   claimed twice concurrently), the failure mode would be a `SERIALIZABLE` conflict (40001,
   retried by the job's own backoff) rather than silent corruption — but it's worth the team
   lead's explicit confirmation that this reliance is acceptable, since it's a slightly different
   correctness argument than "the lock makes it impossible."
2. **`MarkSourceEventBusy` reuses the existing `'queued'` status** rather than adding a new one
   (avoiding a schema/CHECK-constraint change) — a busy-rescheduled event is briefly
   indistinguishable in the `processing_status` column from a normal queued retry; `processing_error='ACCOUNT_LOCK_BUSY'`
   is the only marker. This seemed the right tradeoff given the task's strong preference against
   schema changes, but flagging it explicitly in case a future dashboard/alert wants to
   distinguish "busy" from "queued" more visibly than that.
3. **The requeue-preserving upsert rule's 5-minute reset window is a new tunable
   (`balanceProofPendingRequeueResetWindow`)** with no existing precedent to match against — it's
   a reasoned choice (roughly the midpoint of the backoff schedule: long enough that a burst of
   facts can't re-arm a deep backoff on every single one, short enough that once contention
   clears, a genuinely new checkpoint can still pull a badly-backed-off job forward within a few
   minutes rather than waiting out the full 10-minute cap) but it's a judgment call, not a value
   derived from a hard constraint. Worth the team lead's sign-off, and easy to tune (one constant)
   if production experience suggests otherwise.
4. No new float amounts, no logged/persisted secrets, no `contracts/` changes, no admin-OIDC
   changes, no schema/migration changes, no touch to any file outside this slice's stated scope —
   checked.

## Follow-ups (recommended, not blocking this delivery)

1. A dedicated test for the second requeue-upsert site (inside `ObserveBalanceCheckpoint`) through
   its full real pipeline, per "Not run" above — low value given the verified-identical SQL, but
   would close the loop completely.
2. Design XM-INV-POLICY-ANCHOR 2.5's *remaining* gap (documented in that slice's own handoff,
   unchanged by this one): a dead event whose payload could not be decrypted at all still cannot
   be attributed to any account — structural, since nothing in the system can identify the
   account of an event it never successfully parsed.
3. Consider whether `balanceProofPendingRequeueResetWindow` (5 minutes) and the backoff cap (10
   minutes) should be operator-configurable rather than compile-time constants, if production
   experience with this incident class suggests different values are needed per source or per
   account tier.
