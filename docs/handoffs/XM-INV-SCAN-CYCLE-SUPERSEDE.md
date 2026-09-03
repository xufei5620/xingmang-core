# XM-INV-SCAN-CYCLE-SUPERSEDE: self-heal a stale active scan cycle left by an agent restart

- **status:** implemented and self-tested locally; gates below. Not merged, not pushed, not tagged.
- **branch:** `ai/claude/XM-INV-SCAN-CYCLE-SUPERSEDE` (based on `ai/claude/XM-INV-AUTOLOGIN` at
  `d608d3b`, which contains XM-INV-AGENT-RESTART-GRACE), worktree `K:/发票/wt-XM-INV-CYCLE-SUPERSEDE`.
- **commits (2, in order):**
  - `ae0d338` feat(postgresstore): self-heal a stale active scan cycle in CommitSourceBatch
    (core fix, migration 0022, the service.go wiring, and the new integration tests -- see
    "Deviation from the brief" below for why these landed in one commit instead of several)
  - `a6015bd` test(migrate): exclude the new 0022 migration from the pre-ledger fixture

## Production incident (verified 2026-09-03 09:02Z)

After the RC78 roll-forward, the Sub2API usage stream wedged. Sequence of events:

1. The pre-deploy backup quiesce restarted the 0.3.0 source-agent, which opened scan cycle
   `c272b5a4` (`cycle_status='receiving'`, sequences 106054-106231, last update 08:58:21Z).
2. The roll-forward then started the 0.3.1 agent. Its rolling-window reconcile logic
   (XM-INV-AGENT-RESTART-GRACE part B, `shouldAbandonLegacyReconcileCycle` in
   `agents/sourceagent/economics_db.go`) correctly recognized `c272b5a4` as a legacy in-flight
   cycle from a pre-upgrade binary and abandoned it *client-side*, starting a brand-new
   `scan_cycle_id` from its rolling-window baseline.
3. Nothing server-side ever closed the orphaned `c272b5a4` row. Every batch of the new cycle
   hit `backend/internal/postgresstore/source_sync.go`'s `CommitSourceBatch` (around what was
   then line 310-355, before this fix), whose `INSERT ... ON CONFLICT` into
   `source_economic_scan_cycles` raced the partial unique index
   `source_economic_one_active_scan_cycle` (one cycle per `(source_instance_id,stream_id)` with
   `cycle_status IN ('receiving','processing')`, migration 0009) and surfaced as a Postgres
   `unique_violation`, mapped to `domain.ErrScanCycleBusy` (HTTP 503).
4. Per XM-INV-CYCLE-BACKOFF, the agent correctly treats 503 as transient backpressure and
   retries the same batch forever, honoring `Retry-After`. That is the right behavior for a
   *genuinely* still-active cycle (see `docs/PRODUCTION-RUNBOOK.md` section 7's existing
   `SOURCE_SCAN_CYCLE_BUSY` writeup) -- but `c272b5a4` was not still active, and nothing on
   either side ever marked it terminal. The stream stayed wedged until an operator manually ran
   `UPDATE source_economic_scan_cycles SET cycle_status='blocked' ...` by hand.

Root cause, precisely: **CommitSourceBatch had no way to distinguish "a different scan cycle id
is racing a genuinely active cycle" (the case the one-active-cycle constraint and
`ErrScanCycleBusy` correctly exist for) from "a different scan cycle id is racing an
abandoned cycle nothing will ever finish"** -- both looked identical from the constraint's point
of view: a second row for the same `(source_instance_id,stream_id)` still in
`receiving`/`processing`.

## Design

`supersedeStaleActiveScanCycleTx` (`backend/internal/postgresstore/source_sync.go`) runs inside
`CommitSourceBatch`'s existing transaction -- which already holds the per-`(source,stream)`
advisory xact lock taken at the top of `CommitSourceBatch` before this function is ever called, so
no concurrent `CommitSourceBatch` call for the same stream can race this decision -- immediately
before the `INSERT INTO source_economic_scan_cycles` for schema v3 batches:

- **No active cycle at all**, or the active cycle's id equals the incoming batch's
  `ScanCycleID` (a continuation batch of the same cycle): returns `nil` immediately, before any
  staleness check. The caller's `INSERT` proceeds exactly as before this feature existed.
- **A different active cycle id that is still fresh**: its `updated_at` is within the caller's
  `StaleActiveScanCycleMaxAge` of `Now`, using the *existing*
  `economicRescanActivityWithinWindow` helper (XM-INV-AGENT-RESTART-GRACE part A) unmodified --
  the exact same "does this still look like a live rescan" test the readiness endpoint already
  uses to grant `/readyz` grace. Returns `domain.ErrScanCycleBusy` directly (a deterministic,
  proactive check -- not a reliance on the `INSERT`'s `unique_violation` mapping, though that
  mapping is left in place as defense in depth for any case this proactive check does not cover).
  This preserves the pre-existing comment's documented race exactly: "a second, different
  scan_cycle_id racing an already-processing cycle ... is an expected, temporary rejection".
- **A different active cycle id that has gone stale** (no `updated_at` movement within that same
  window): `UPDATE`s the stale row to `cycle_status='blocked'`,
  `superseded_by_scan_cycle_id=<new cycle id>`, `supersede_reason=<fixed descriptive string>`,
  records a `source.scan_cycle.superseded` audit event (before/after payloads carry both cycle
  ids, the old cycle's `first_sequence`/`last_sequence`, and the new batch's `sequence`), then
  returns `nil` so the caller's `INSERT` proceeds. By the time that `INSERT` runs, the partial
  unique index no longer has a conflicting row (the `UPDATE` already moved the old row out of
  `cycle_status IN ('receiving','processing')`), so it succeeds without exercising the
  `unique_violation` path at all.

**Why reuse `EconomicRescanActivityMaxAge` rather than a new constant.** The brief asked for the
staleness window to be "derived from the same freshness policy constants source_sync.go already
uses for readiness ... as a named constant, never a magic number". `SourceFreshnessPolicy`'s
`EconomicRescanActivityMaxAge` (computed in `backend/cmd/api/runtime.go` as
`economicRescanActivityPollWindows*SOURCE_POLL_INTERVAL + SOURCE_ECONOMIC_SAFETY_DELAY +
economicRescanProcessingTailAllowance`, all three already named constants/config there) already
answers exactly this question -- "does this active-status scan-cycle row's `updated_at` still
look like a live, progressing rescan, or has it stopped" -- for the readiness endpoint. A cycle
the readiness endpoint would no longer grant grace to (because it looks stalled) is exactly the
cycle this feature should supersede, so `CommitSourceBatch` reuses the *same* duration end to end:
`Service.CommitVerifiedSourceBatch` (`backend/internal/application/service.go`) passes
`Now: s.now()` and `StaleActiveScanCycleMaxAge: s.sourceEconomicRescanActivityMaxAge` -- the exact
field the existing `sourceFreshnessPolicy()` also reads -- into the new `SourceBatchInput` fields.
No new environment variable, no new constant duplicating the same arithmetic, and both call sites
can never disagree about what "still active" means.

**Zero-value safety.** `SourceBatchInput.Now`/`StaleActiveScanCycleMaxAge` are new, optional
fields on an existing struct -- every pre-existing call site (20 across the test suite plus
`service.go`) that constructs a `SourceBatchInput{...}` with named fields and does not set these
two keeps compiling and keeps its exact pre-existing behavior (`in.Now.IsZero() ||
in.StaleActiveScanCycleMaxAge<=0` short-circuits straight to `domain.ErrScanCycleBusy` for a
different active cycle id), the same "zero disables the grace" convention
`EconomicRescanActivityMaxAge` itself already established.

**Migration 0022** adds `superseded_by_scan_cycle_id UUID` and `supersede_reason TEXT` to
`source_economic_scan_cycles`, plus:
- a pairing `CHECK` (both null or both set together),
- a `CHECK` that a set `superseded_by_scan_cycle_id` implies `cycle_status='blocked'`,
- a self-referencing `FOREIGN KEY (source_instance_id,stream_id,superseded_by_scan_cycle_id)
  REFERENCES source_economic_scan_cycles(source_instance_id,stream_id,scan_cycle_id) DEFERRABLE
  INITIALLY DEFERRED`. It must be deferred: within the same transaction, the old row's `UPDATE`
  (which sets this column to the *new* cycle's id) runs before the new cycle's row is `INSERT`ed,
  so the reference would not resolve yet if checked immediately -- deferring to `COMMIT` lets both
  statements land in their natural order.

Neither new column is in `enforce_scan_cycle_trust_immutability`'s guarded column list (migration
0009), so the existing `BEFORE UPDATE` trigger on `source_economic_scan_cycles` does not need any
change to allow this `UPDATE`.

**Migration numbering.** The next free slot was 0021, but the brief flagged that another slice may
independently be adding its own `0021`, so this migration is named
`0022_scan_cycle_supersede.sql` instead. `backend/internal/migrate`'s runner
(`backend/internal/migrate/migrate.go`) applies `*.sql` files in lexical filename order and tracks
each one individually by its full filename plus checksum in `schema_migrations` -- there is no
requirement that numeric prefixes be contiguous, so this file does not depend on whatever 0021
turns out to contain, in either direction.

## Confirmed, not changed

- **Events already committed under a superseded cycle remain intact.** The supersede path only
  `UPDATE`s `cycle_status`/`superseded_by_scan_cycle_id`/`supersede_reason`/`updated_at` on the
  scan-cycle row itself; it never touches `source_economic_scan_cycle_events` (the event-to-cycle
  mapping, `FOREIGN KEY ... ON DELETE RESTRICT` back to the scan cycle) or
  `source_ingest_events` (the encrypted event rows). Proven in
  `TestCommitSourceBatchSupersedesStaleActiveScanCycle`: the old cycle's mapping row count and its
  event's `processing_status` are asserted unchanged after the supersede.
- **A blocked cycle never publishes an economic watermark.** `tryPublishEconomicScanCyclesTx`
  (`backend/internal/postgresstore/consumption.go`, read-only for this slice per the brief) only
  ever selects `WHERE cycle_status='processing'` -- `'blocked'` was already structurally excluded
  before this feature existed; nothing needed to change there. Proven in the same test: after the
  superseded cycle's event is marked processed and `tryPublishEconomicScanCyclesTx` is called
  directly against it, `source_economic_stream_watermarks` gains no row and the cycle's own
  `cycle_status` stays `'blocked'`.
- **Two streams stay isolated.** The same test opens an independent active cycle on a second
  stream (`payments`) of the same source and asserts it is untouched by superseding the first
  stream's (`usage`) cycle -- both the `SELECT ... FOR UPDATE` inside
  `supersedeStaleActiveScanCycleTx` and the `UPDATE` it issues are scoped by
  `source_instance_id`+`stream_id`, matching the partial unique index's own scope.
- **Every branch returns a defined result; per-stream failures never affect other streams.**
  `supersedeStaleActiveScanCycleTx` has exactly four exits (no active cycle, same-id continuation,
  fresh-and-busy, stale-and-superseded) plus real Postgres errors propagated as `error` values (no
  panics, no silent swallows); it is scoped to one `(source_instance_id,stream_id)` row per call
  and never reads or writes another stream's rows.

## Deviation from the brief, flagged explicitly

The brief's commit-granularity instruction ("commit in small logical commits") was followed
imperfectly: commit `ae0d338` bundles the core `source_sync.go` fix, migration 0022, the
`service.go` wiring, and the new `source_sync_integration_test.go` tests together, rather than as
separate commits. This happened because `git add` was run for a `gofmt` verification step earlier
in the session (staging `service.go` and the test file to check their committed-blob formatting,
per this repository's documented CRLF/`gofmt` quirk) and those files were still staged when the
first commit was made; by the time this was noticed, splitting the already-made commit would have
required `git reset`/re-staging maneuvers on a branch nobody else was working from, which felt like
more risk than benefit for a purely cosmetic reordering. The commit's contents are correct and
complete regardless of the grouping. No other deviations from the brief.

## Files changed

- `backend/internal/postgresstore/source_sync.go` -- `SourceBatchInput.Now`/
  `StaleActiveScanCycleMaxAge`, `scanCycleSupersededAction`/`scanCycleSupersedeReason` constants,
  `supersedeStaleActiveScanCycleTx`, called from `CommitSourceBatch` immediately before the
  `source_economic_scan_cycles` `INSERT` for schema v3 batches.
- `backend/migrations/0022_scan_cycle_supersede.sql` (new) -- `superseded_by_scan_cycle_id`/
  `supersede_reason` columns, pairing/status/self-referencing-FK constraints on
  `source_economic_scan_cycles`.
- `backend/internal/application/service.go` -- `CommitVerifiedSourceBatch` now passes
  `Now: s.now()` and `StaleActiveScanCycleMaxAge: s.sourceEconomicRescanActivityMaxAge` into the
  `SourceBatchInput` it builds.
- `backend/internal/postgresstore/source_sync_integration_test.go` (extended) -- new
  `scanCycleRow`/`readScanCycleRowForTest`/`newV3SupersedeBatchInput`/`provisionV3SupersedeSource`
  helpers, plus three new tests:
  `TestCommitSourceBatchSupersedesStaleActiveScanCycle` (stale different-id cycle superseded, new
  cycle accepted, audit row written, events intact, blocked-cycle-never-publishes, two-stream
  isolation -- all in one fixture to share setup), `TestCommitSourceBatchKeepsFreshActiveScanCycleBusy`
  (fresh different-id cycle keeps returning `domain.ErrScanCycleBusy`, old row untouched),
  `TestCommitSourceBatchContinuationOfActiveScanCycleIsUnaffectedBySupersedeGrace` (same-id
  continuation succeeds unaffected even with the grace fully enabled and the cycle far past any
  staleness window).
- `backend/internal/migrate/migrate_test.go` -- `TestConsumptionMigrationClosesPreCutoverReservationsAndPreservesIssuedExposure`'s
  reduced-schema fixture (which deliberately excludes migration 0009 and everything that alters
  tables 0009 creates) now also excludes the new 0022, for the same reason 0016/0020 are already
  excluded there.
- `docs/PRODUCTION-RUNBOOK.md` -- new paragraph in section 7 (immediately after the existing
  `SOURCE_SCAN_CYCLE_BUSY` writeup) describing the incident this fixes, what the operator now sees
  (the same transient 503 retries, then self-recovery instead of a required manual `UPDATE`), and
  where to look if a stream is still wedged past the grace window.
- `docs/handoffs/XM-INV-SCAN-CYCLE-SUPERSEDE.md` -- this document.

## Gates

All commands run from `K:/发票/wt-XM-INV-CYCLE-SUPERSEDE/backend` (or the worktree root for
gitleaks), with `HTTP(S)_PROXY`/`ALL_PROXY`/`NO_PROXY` (and lowercase variants) unset before every
Go command per the documented Windows toolchain quirk, and
`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_supersede?sslmode=disable`
(a dedicated database, created via `docker exec invoice-test-pg psql -U postgres -c "CREATE
DATABASE invoice_test_supersede;"` against the already-running `invoice-test-pg` container on
`127.0.0.1:55432` -- no `psql` binary is on `PATH` in this environment) for every DB-backed test.

| Gate | Command | Result |
| --- | --- | --- |
| build | `go build ./...` | exit 0 |
| vet | `go vet ./...` | exit 0 |
| gofmt | `"$(go env GOROOT)/bin/gofmt" -l` on the staged (`git cat-file -p :<path>`) blob of every changed file, per the repository's documented CRLF-checkout gofmt quirk | clean (one real misalignment found and fixed in the new `scanCycleRow` struct before this was clean) |
| full suite | `go test -p 1 -count=1 ./...` | `ok` all packages, two full runs, both 100% green on the first attempt (no flakes observed, so no isolated rerun was needed) |
| gitleaks | `gitleaks git --no-banner --log-opts="d608d3b..HEAD" .` | `no leaks found`, 2 commits scanned |

## What was not verified

- **No production/staging deployment or roll-forward rehearsal.** This is a worktree-only change,
  not pushed, tagged, or merged.
- **The exact production incident's specific cycle id (`c272b5a4`) and sequence range
  (106054-106231)** were not replayed byte-for-byte against a seeded fixture; the new tests use
  synthetic cycle ids and sequences that exercise the same structural scenario (a stale different
  active cycle id) rather than reproducing the incident's exact data.
- **Concurrent-agent load** (multiple source-agent processes or retries racing the same stream
  during the exact moment of supersede) was not separately load-tested beyond the existing
  per-`(source,stream)` advisory-lock serialization this fix relies on for correctness, which is
  the same serialization every other `CommitSourceBatch` invariant in this file already depends on.
