# XM-INV-POLICY-ANCHOR: anchor ledgers at the invoice policy start

- **status:** partially implemented, self-tested locally. A schema conflict
  (found before writing any code, reported to the team lead before
  continuing) blocks design sections 2.1's core bootstrap mechanism and all
  of 2.4 -- both require writing `bootstrap_kind='POLICY_ANCHOR'`, a value
  the current schema's CHECK constraint and trigger do not allow, and which
  cannot be added without a migration (forbidden for this slice). Everything
  else in the design that does not depend on a new enum value is implemented:
  2.1's "ignore a pre-policy checkpoint" half, 2.2 in full, 2.3 (test only,
  no code change needed), and 2.5's "an already-frozen account's failed/dead
  event doesn't hold the cycle" half. See **Risks** below for the full
  conflict detail and exact citations.
- **branch:** `ai/claude/XM-INV-POLICY-ANCHOR` (based on
  `ai/claude/XM-INV-AUTOLOGIN` at `41edb5c`), worktree
  `K:/发票/wt-XM-INV-ANCHOR`.
- **commit:** see `git log` on this branch -- one commit carrying the code,
  test, and this handoff doc together.

## Summary

The design (`docs/superpowers/specs/2026-09-02-invoice-policy-anchor-design.md`)
has five sections. What actually shipped, and why, per section:

**2.1 (postgresstore/consumption.go, `ObserveBalanceCheckpoint`) -- half
implemented.** The reconciliation-checkpoint-with-no-eligibility-state branch
now fetches `invoice_eligibility_policy.eligibility_start_at` and, when the
account's manifest cutover predates the policy start (the invariant every
non-fixture manifest satisfies -- enforced by both
`RegisterCutoverManifest`'s own check and the `enforce_source_manifest_projection_contract`
trigger) **and** the checkpoint's `as_of` is also before the policy start, it
acknowledges the checkpoint (audits `eligibility.pre_policy_checkpoint.skipped`,
commits, returns nil) instead of returning `domain.ErrSourceUnavailable` to
wait on the signed cutover row. This applies to both baseline and
non-baseline accounts (design bullet 3: "if [a non-baseline account's first
checkpoint] is not [already post-policy], the same anchor rule applies").

**Not implemented: the actual policy-anchor bootstrap** (a checkpoint dated
at/after the policy start, for an account with no state, should bootstrap
directly with `cutover_at := checkpoint.as_of`, `bootstrap_kind :=
'POLICY_ANCHOR'`). `source_account_eligibility_state`'s
`..._bootstrap_kind_check` CHECK constraint (migration `0011`) only allows
`'SIGNED_CUTOVER'`/`'POST_CUTOVER_REPLAY'`, and the
`enforce_account_eligibility_cutover_contract` trigger (same migration) has
no validation branch for a third kind either (it would silently accept any
`cutover_at`/`cutover_balance_units` for an unrecognized `bootstrap_kind`,
relying entirely on the CHECK constraint to reject the value first). Neither
can be extended without a migration. **Unchanged:** a baseline member's
checkpoint at/after the policy start still returns `ErrSourceUnavailable`
(waits/parks) exactly as before this slice.

**2.2 (postgresstore/source_sync.go, `RequeueSourceDependency`) --
implemented in full.** For an `invoice_oidc_user`/`source_external_account`
wake specifically, before releasing the parked backlog, a first `UPDATE`
bulk-marks parked (`waiting_dependency`/`parked_identity`) `usage_event`/
`balance_checkpoint` entities with `observed_at` before the policy start as
`processing_status='processed', processing_error='PRE_POLICY_SKIPPED'`
(`processed_at=now()`, matching the table's own `processed <=> processed_at
NOT NULL` constraint). The existing release `UPDATE` then runs as before
over whatever the first update didn't touch -- payments, credits, identities,
manifests, and any post-policy usage/balance events are released normally,
untouched by the new branch. Both updates now run inside one transaction
(the function previously used a single bare `s.pool.Exec`).

**2.3 (postgresstore/consumption.go, `buildEligibilityProjectionTx`) -- no
code change, per the design ("no query change required"); added the
assertion-style test.** `TestEligibilityProjectionNeverAllocatesPreAnchorUsageFact`
hand-inserts a usage fact before `account.CutoverAt` directly into
`source_usage_events` (bypassing the normal ingestion path, which would
freeze on `SOURCE_GAP` instead of persisting such a row) and confirms
`buildEligibilityProjectionTx`'s existing `event_time>account.CutoverAt`
window excludes it from every allocation, while a fact just after the anchor
is allocated normally. The account uses the existing `POST_CUTOVER_REPLAY`
bootstrap as a stand-in for a policy-anchored account (see 2.1's blocker
above) -- the windowing behavior under test depends only on
`account.CutoverAt`, not on `bootstrap_kind`.

**2.4 (one-time re-anchoring of already-bootstrapped accounts) -- not
implemented at all.** This section's entire mechanism is rewriting
`bootstrap_kind` to `'POLICY_ANCHOR'`, `cutover_at` to the first post-policy
reconciliation checkpoint, deleting and reprojecting
`consumption_allocations`. Blocked by the same schema conflict as 2.1's
bootstrap half. I did not touch `processEligibilityProjectionJob` (the
mount point the design and the task brief both named) at all.

**2.5 (postgresstore/consumption.go, `tryPublishEconomicScanCyclesTx`) --
half implemented.** The scan-cycle completeness query now left-joins
`eligibility_freezes` on `ef.source_revision_hash=sie.payload_hash AND
ef.status='open'` and excludes a `failed`/`dead` event from the "incomplete"
count only when a matching open freeze exists. The join key is
`source_revision_hash`, not the ingest event's own UUID: every
`freezeEligibilityTx` call site already sets the freeze's
`source_revision_hash` to the triggering fact's revision hash, which is the
exact same value stored as the ingest event's `payload_hash` (both trace
back to `claim.PayloadHash`) -- there is no ingest-layer column identifying
which account/fact an encrypted, still-parked event belongs to, so this is
the only correlation available without decrypting payloads inside
`postgresstore` (a much larger change I did not consider in scope). A
`failed`/`dead` event with no matching freeze (pure transient exhaustion)
still holds the cycle, unchanged.

**Not implemented: auto-freezing a `dead` event that has no freeze, with
reason `EVENT_DEAD`.** `eligibility_freezes.freeze_reason`'s CHECK constraint
(migration `0009`) does not include `'EVENT_DEAD'` and no later migration
adds it. Without this half, the production incident described in the
design's Problem section 1 (a single dead event with no prior freeze holding
scan cycle `5b5c26bc` in `processing`) is **not** fixed by this slice --
only the case where the frozen state already exists (e.g. from an
independent payload-drift/conflict freeze the processor already does today)
is now excluded from holding the cycle.

## Files changed

- `backend/internal/postgresstore/consumption.go` -- `tryPublishEconomicScanCyclesTx`
  (2.5 half); `ObserveBalanceCheckpoint`'s reconciliation-with-no-state branch
  (2.1 half).
- `backend/internal/postgresstore/source_sync.go` -- `RequeueSourceDependency`
  rewritten to run in a transaction with the new pre-policy skip branch (2.2).
- `backend/internal/postgresstore/policy_anchor_integration_test.go` -- new
  file, 4 tests (listed under Tests run below).
- `backend/internal/application/service_integration_test.go` -- renamed and
  rewrote `TestBaselineMemberReconciliationWaitsWithoutConsumingRetryBudget`
  to `TestBaselineMemberPrePolicyCheckpointIgnoredWithoutConsumingRetryBudget`:
  moved the fixture's checkpoint `as_of` from after the policy start to
  strictly between the manifest cutover and the policy start (so it exercises
  2.1's new ignore branch instead of the old RC62 wait), and updated the
  first assertion block accordingly (`status == "processed"`, dropped the
  now-inapplicable `dependency_kind`/`attempt_count` checks). The cycle-not-held,
  second-`RunOnce`-processes-zero, and no-eligibility-state assertions were
  kept unchanged -- all three still hold under the new outcome, exactly as
  instructed.

**Not touched:** any migration file, `release/`, `scripts/`,
`RELEASE-READINESS.md`, `docs/PRODUCTION-RUNBOOK.md`,
`docs/IMAGE-SCAN-REVIEW.md`, `docs/superpowers/plans/`, any RC version
identity. No new dependencies, no `contracts/` changes.

## Tests run

From `backend/`, with `GOFLAGS=-buildvcs=false` and
`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test?sslmode=disable`
(the `invoice-test-pg` container was already running):

```
go build ./...                 # exit 0, clean
go vet ./...                   # exit 0, clean
go test -p 1 -count=1 ./...    # see below
```

New in `backend/internal/postgresstore/policy_anchor_integration_test.go`:

- `TestPrePolicyReconciliationCheckpointIsIgnoredForAccountWithoutState` --
  a pre-policy baseline-member checkpoint is ignored (no eligibility state
  created, exactly one `eligibility.pre_policy_checkpoint.skipped` audit
  event); a second, post-policy checkpoint for the same (still unbootstrapped)
  account still returns `ErrSourceUnavailable`, locking in the unchanged
  boundary documented above.
- `TestRequeueSourceDependencySkipsPrePolicyUsageAndBalanceBacklog` -- a
  `source_external_account` wake skips a pre-policy `usage_event` and a
  pre-policy `balance_checkpoint` (`processed`/`PRE_POLICY_SKIPPED`), releases
  a post-policy `usage_event` normally (`queued`), and releases a pre-policy
  `credit_event` normally too (never skipped, per design).
- `TestEligibilityProjectionNeverAllocatesPreAnchorUsageFact` -- design 2.3's
  assertion test, described above.
- `TestScanCycleFrozenDeadEventPublishesWhilePureTransientDeadEventHoldsCycle`
  -- two independent scan cycles (different streams to avoid the
  one-active-cycle-per-stream constraint): a `dead` event with no freeze
  leaves its cycle `processing`; a `dead` event whose `payload_hash` matches
  an open freeze's `source_revision_hash` lets its cycle `published`.

Modified in `backend/internal/application/service_integration_test.go`:
`TestBaselineMemberPrePolicyCheckpointIgnoredWithoutConsumingRetryBudget`
(see Files changed above for exactly what changed and why).

All four new tests plus the modified one pass; a full re-run of
`./internal/postgresstore/...` (42.2s, every test in the package, not just
the new ones) and `./internal/application/...` (12.9s, ditto) after the code
changes shows zero regressions in either package -- every pre-existing test
in both still passes unmodified.

Full-suite (`go test -p 1 -count=1 ./...`) result: exit 0 for `go build`,
exit 0 for `go vet`. `go test` itself: ran it twice.
- First run: every package green except `internal/document`'s
  `TestClamAVScannerStreamsDocument` (no ClamAV daemon in this environment;
  see **Not run**).
- Second run (after the gofmt fix below): `internal/document` passed this
  time, but `internal/auth` failed 3 tests (2 postgres-connection timeouts,
  1 OIDC-discovery-over-HTTP failure) while I had several other commands
  (gofmt, gitleaks, git add) running concurrently against the same
  containers/ports. Re-ran `internal/auth` alone immediately after, with no
  concurrent load: 100% clean, 3.2s. `internal/document`/`internal/auth` are
  both untouched by this diff, and the postgres container's uptime (17h,
  unchanged across both runs) rules out a container restart -- this is
  environment contention from my own concurrent tool calls, not a real
  regression. `internal/postgresstore` (43.6s) and `internal/application`
  (12.3s) -- the two packages this diff actually touches -- were green in
  both full runs and in every standalone re-run.

`gofmt` (the `go env GOROOT` copy, not the stale one on PATH -- see this
repo's own toolchain notes) is clean on all four touched files; the one real
(non-CRLF) alignment issue it found, a struct-literal field alignment in the
new test file, is fixed in the commit.

gitleaks 8.x (`gitleaks git --pre-commit --staged --verbose --redact=0`) on
the staged diff: 0 leaks.

## Not run

- Anything requiring a server/production connection -- never connected to
  one, per the task's constraints.
- `TestClamAVScannerStreamsDocument` (`internal/document`) -- fails in this
  environment (`dial tcp 127.0.0.1:50508: ... connectex ...`, no ClamAV
  daemon running here). Unrelated to this slice: nothing in this diff touches
  document scanning, and this package was untouched by me. Flagging so it
  isn't mistaken for a regression from this work.
- `processEligibilityProjectionJob` -- not touched (2.4 is not implemented;
  see Summary).
- The rehearsal-on-a-backup-restore verification gate (design section 3 item
  3) -- requires production backup access and `deploy/backup/restore-drill.sh`
  against an isolated restore; out of scope for this worktree/task, and moot
  for the two blocked design sections regardless.

## Risks / things to sign off on

1. **The core mechanism of design 2.1 and all of 2.4 cannot be implemented
   without a migration.** `source_account_eligibility_state`'s
   `source_account_eligibility_state_bootstrap_kind_check` CHECK constraint
   (`backend/migrations/0011_invoice_eligibility_policy.sql:167-170`) allows
   only `'SIGNED_CUTOVER'`/`'POST_CUTOVER_REPLAY'`; the
   `enforce_account_eligibility_cutover_contract` trigger (same file,
   `172-205`) validates only those two kinds' `cutover_at`/
   `cutover_balance_units` shape. I verified no later migration (`0012`
   through `0015`) touches either. Implementing 2.1/2.4 as designed needs a
   migration adding `'POLICY_ANCHOR'` to the CHECK constraint and a new
   trigger branch validating it (its `cutover_at` is per-account, not tied to
   `manifest.cutover_at` the way the other two kinds are) -- which this
   slice's constraints explicitly forbid me from adding. I reported this to
   the team lead before implementing anything further, and scoped the rest
   of the slice to what's achievable without it, rather than fabricating a
   workaround (there isn't a safe one: reusing `'POST_CUTOVER_REPLAY'` is not
   just inadvisable but actually rejected by the trigger for any `cutover_at`
   other than the exact global `manifest.cutover_at`, which is precisely what
   2.1 needs to not use).
2. **Same class of gap for half of 2.5.** `eligibility_freezes.freeze_reason`'s
   CHECK constraint (`backend/migrations/0009_consumption_eligibility_ledger.sql:501-511`)
   has no `'EVENT_DEAD'` value and no later migration adds one. This means
   the production incident cited in the design's Problem section 1 (a dead
   event with no prior freeze holding a scan cycle in `processing`
   indefinitely) is **not fixed** by what shipped here -- only cycles where
   the account was already frozen through some other existing path (payload
   drift/conflict, which the processor already does today per the design's
   own parenthetical) now correctly stop holding the cycle.
3. **The 2.5 freeze correlation is a best-effort join on content hash, not an
   explicit foreign key.** `source_revision_hash`/`payload_hash` are both
   SHA-256-shaped hashes of distinct fact content; a collision across two
   unrelated facts is not a realistic concern in practice (every real
   payload carries unique IDs/timestamps/sequence numbers), but this is a
   correlation by construction, not a declared relationship the schema
   enforces -- worth a second look if anyone later changes how
   `source_revision_hash`/`payload_hash` are derived.
4. **`TestEligibilityProjectionNeverAllocatesPreAnchorUsageFact`'s account is
   not literally `bootstrap_kind='POLICY_ANCHOR'`** (it's
   `'POST_CUTOVER_REPLAY'`, the closest existing stand-in, since the real
   value can't be constructed yet). The windowing logic under test reads only
   `account.CutoverAt`, so this is a faithful test of the mechanism 2.3
   describes, but it does not prove anything about `bootstrap_kind='POLICY_ANCHOR'`
   specifically -- there is nothing to test yet, since no code path can ever
   produce that value.
5. Per the task's own instruction, I did not decide on my own to change the
   design to route around the blocker (e.g. quietly picking a different
   enum string, or writing a migration anyway) -- flagging it here and in
   the message to the team lead instead.

## Follow-ups (recommended, not blocking my delivery of this task)

1. A migration adding `'POLICY_ANCHOR'` to
   `source_account_eligibility_state_bootstrap_kind_check` and a new
   validation branch in `enforce_account_eligibility_cutover_contract` (its
   `cutover_at` is per-account/per-checkpoint, unlike the other two kinds'
   fixed relationship to `manifest.cutover_at` -- the new branch likely just
   needs to allow any `cutover_at > manifest.cutover_at` with
   `bootstrap_kind='POLICY_ANCHOR'`, but that's a design decision for
   whoever writes it, not mine to make unilaterally here). Once that lands,
   the rest of 2.1 (the actual bootstrap write) and all of 2.4 (the
   re-anchor migration job, mounted in `processEligibilityProjectionJob`)
   can be implemented as designed.
2. A migration adding `'EVENT_DEAD'` to `eligibility_freezes.freeze_reason`'s
   CHECK constraint, then the "freeze a dead event with no freeze" half of
   2.5, to actually fix the cited production incident.
3. Once both migrations exist, re-run the four integration tests the design's
   verification gate lists (2.1 full bootstrap, 2.2 -- already done, 2.4
   re-anchor, 2.5 full) and the rehearsal-on-a-restored-backup step (design
   section 3 items 2-3) before any production rollout.
