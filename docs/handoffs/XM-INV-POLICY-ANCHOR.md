# XM-INV-POLICY-ANCHOR: anchor ledgers at the invoice policy start

- **status:** fully implemented and self-tested locally — design sections 2.1, 2.2, 2.3, 2.4, and
  2.5 all implemented in full, including 2.4's re-anchor mechanism, which went through two
  revisions during implementation before landing on what's in this commit (see 2.4 below and the
  design doc's `2.0`/`2.4` sections for the full history: a DELETE-and-reinsert draft blocked by
  non-deferrable foreign keys, then a `cutover_at < policy start` trigger-signal draft that turned
  out not to be a distinguishing condition, both superseded by the guarded-UPDATE mechanism the
  team lead specified in a follow-up decision).
- **branch:** `ai/claude/XM-INV-POLICY-ANCHOR` (based on `ai/claude/XM-INV-AUTOLOGIN` at
  `41edb5c`), worktree `K:/发票/wt-XM-INV-ANCHOR`.
- **commit:** see `git log` on this branch — this is a third commit on top of the slice's first
  two deliveries (`eb68989`: sections 2.1/2.2/2.3/2.5 without migration 0016, schema-blocked at
  the time; `cb53142`: migration 0016 per the team lead's first authorization). This commit
  replaces `cb53142`'s migration 0016 content with the team lead's revised specification for 2.4
  (guarded UPDATE, not DELETE-and-reinsert; `bootstrap_kind` as the re-anchor signal, not
  `cutover_at`), wires 2.4 into `processEligibilityProjectionJob`, and updates every test whose
  fixture the resulting "legacy kinds are now truly legacy" rule affected.

## Summary

**2.1 (postgresstore/consumption.go, `ObserveBalanceCheckpoint`) — implemented in full, including
a correction made mid-implementation.** A reconciliation checkpoint with no eligibility state yet:
before the policy start, ignored (audit `eligibility.pre_policy_checkpoint.skipped`); at/after the
policy start, bootstraps directly (`bootstrap_kind='POLICY_ANCHOR'`, `cutover_at :=
checkpoint.as_of`, `cutover_balance_units := checkpoint.balance_service_units`, entirely
non-invoiceable — the same treatment `SIGNED_CUTOVER` gives its opening balance). **This is now
unconditional on `BaselineMember`** — the design's original §2.1 bullet 3 said non-baseline
accounts would keep the old `POST_CUTOVER_REPLAY` bootstrap; the team lead's follow-up decision
corrected this: after this slice, no code path creates a new `SIGNED_CUTOVER` or
`POST_CUTOVER_REPLAY` row, baseline member or not. The state row is inserted **before** the
checkpoint row (checkpoint-kind `reconciliation`, `reconciliation_status='cutover_baseline'`,
`baseline_member` carried through from the observation): the checkpoint table's own (unchanged,
immediate) `balance_reconciliation_checkpoints_contract_guard` trigger requires a trusted state
row to already exist, while the state row's own `POLICY_ANCHOR` validation (migration `0016`) is
deferred to `COMMIT` specifically so it can, in turn, require this same checkpoint row to exist by
then — see migration `0016`'s own comment and the design doc's `2.0` section. An account that
already has state hits the existing "state already exists → `ErrConflict`" guard, unchanged.

**2.2/2.3/2.5 — unchanged from the second delivery (`eb68989`), still implemented in full.**

**2.4 (one-time-per-account re-anchoring of already-bootstrapped legacy accounts) — implemented in
full.** The final mechanism, per the team lead's follow-up decision:

- **Signal:** `bootstrap_kind IN ('SIGNED_CUTOVER','POST_CUTOVER_REPLAY')`, not
  `cutover_at < PolicyStartAt` (an earlier draft used the latter and it does not work — see the
  design doc's `2.4` section for why: it is true for essentially every bootstrapped account by
  construction, not a staleness signal). Since 2.1 (as corrected) means no code path creates a new
  legacy-kind row after this slice deploys, `bootstrap_kind` alone correctly identifies exactly
  the accounts bootstrapped *before* it.
- **Mechanism:** `processEligibilityProjectionJob`, right after loading the account and before
  projecting, when the account's kind is legacy: (a) if any `invoice_allocations` row referencing
  one of its funding lots is `reserved`/`issued`/`refund_attention`, freeze
  (`POLICY_ANCHOR_BLOCKED`), audit `eligibility.policy_anchor.blocked`, and continue without
  touching the ledger — idempotent (skips silently if already frozen with that reason); (b) else
  if no reconciliation checkpoint with `as_of >= PolicyStartAt` exists yet, continue projecting
  under the existing anchor unchanged, no audit, no error; (c) else, in one transaction: set the
  transaction-local GUC `invoice.policy_anchor_reanchor='on'`, delete the account's
  `consumption_allocations`, reset the affected lots' consumption state to zero, `UPDATE
  source_account_eligibility_state` in place (`cutover_at`/`cutover_balance_units` from the
  candidate checkpoint, `bootstrap_kind='POLICY_ANCHOR'`, `finalized_through := cutover_at`),
  audit `eligibility.policy_anchor.migrated` with before/after values and the allocations-deleted
  count, then continue projecting from the new anchor in the same job run.
- **Why an UPDATE, not the DELETE-and-reinsert the design originally called for:**
  `eligibility_projection_jobs.external_account_id` (the job's own row) and
  `source_account_stream_watermarks.external_account_id` both carry non-deferrable `ON DELETE
  RESTRICT` foreign keys to `source_account_eligibility_state` — a DELETE cannot run inside the
  job that would need to issue it. Migration `0016`'s guarded UPDATE exception (see below)
  sidesteps this: the row is updated in place, under a narrow, auditable, GUC-gated exception to
  its otherwise-immutable trust boundary; those foreign keys never fire.
- `reanchorLegacyEligibilityAccountTx` (`postgresstore/consumption.go`) implements (a)/(b)/(c)
  above; wired into `processEligibilityProjectionJob` right after `getEligibilityAccountTx`, as
  the team lead specified.

## Migration 0016 (final content — supersedes `cb53142`'s version)

`backend/migrations/0016_policy_anchor.sql`. Four parts (see the design doc's `2.0` section for
the full text and rationale):
1. `source_account_eligibility_state_bootstrap_kind_check` extended to include `'POLICY_ANCHOR'`.
2. `enforce_account_eligibility_cutover_contract()`'s `POLICY_ANCHOR` branch: `cutover_at` must be
   `>= invoice_eligibility_policy.eligibility_start_at`, strictly `>` the manifest's own
   `cutover_at`, and `<= now()`; `finalized_through` must equal `cutover_at`; and a matching
   `balance_reconciliation_checkpoints` row must exist (`checkpoint_kind='reconciliation'`,
   `as_of=cutover_at`, `balance_service_units=cutover_balance_units`, `unit_code` match) — plus
   the existing manifest-hash check, unchanged. (`cb53142`'s version only had the first and last
   of these five conditions; the `> manifest cutover`, `<= now()`, and
   `finalized_through=cutover_at` conditions are new in this commit, per the team lead's
   follow-up.)
3. The same trigger function carries the guarded re-anchor UPDATE exception (2.4's mechanism): the
   trust boundary (`cutover_at`/`cutover_balance_units`/`bootstrap_kind`/`finalized_through`, plus
   `external_account_id`/`source_instance_id`/`unit_code`/`cutover_manifest_hash`/
   `finalization_delay_seconds`) is immutable on `UPDATE`, except a transaction with the
   transaction-local setting `invoice.policy_anchor_reanchor='on'` (`set_config(...,true)`, never
   persisted) may change `cutover_at`/`cutover_balance_units`/`bootstrap_kind`/`finalized_through`
   on a row whose `OLD.bootstrap_kind` is legacy and `NEW.bootstrap_kind='POLICY_ANCHOR'` — every
   other column must stay identical, and the new values still go through the same `POLICY_ANCHOR`
   validation from part 2. This entirely replaces `cb53142`'s migration, which had no UPDATE
   exception at all (2.4 wasn't wired in yet when it was authorized).
4. `eligibility_freezes.freeze_reason` extended with `'EVENT_DEAD'` (design 2.5, unchanged from
   `cb53142`) and `'POLICY_ANCHOR_BLOCKED'` (design 2.4's block-on-exposure branch, unchanged).

The trigger is (unchanged from `cb53142`) a `DEFERRABLE INITIALLY DEFERRED` `CONSTRAINT TRIGGER`,
not a plain immediate `BEFORE` trigger — required so a `POLICY_ANCHOR` bootstrap's state-row and
checkpoint-row inserts (each depending on the other existing) can both land in one transaction
without an ordering deadlock; same pattern already used for the analogous `funding_lots` /
`funding_lot_consumption_state` mutual check. See the design doc's `2.0` section for the full
mechanical explanation.

Verified empirically with a throwaway scratch Go program before touching real code (9 scenarios
spanning both the `POLICY_ANCHOR` insert validation and the guarded UPDATE exception — deleted
before committing, not part of this diff), then via the dedicated migration test (expanded in this
commit, see below) and the full existing suite.

## Files changed (this commit, on top of `eb68989`/`cb53142`)

- `backend/migrations/0016_policy_anchor.sql` — rewritten (see Migration 0016 above).
- `backend/internal/postgresstore/consumption.go`:
  - `ObserveBalanceCheckpoint`'s no-state bootstrap branch unified for baseline and non-baseline
    accounts (2.1's correction, above).
  - Fixed a latent bug found while validating the above: the `POLICY_ANCHOR` bootstrap's
    `balance_reconciliation_checkpoints` INSERT was missing a value placeholder for
    `configuration_hash` entirely (column list had 21 entries, VALUES only 20), and — masked by
    that error until fixed — a `::numeric` cast was on the wrong parameter (bound to `as_of`, a
    timestamp column, instead of `balance_service_units`). Both were introduced earlier in this
    branch's history (not present in `cb53142`), caught by this session's own test runs before
    being committed anywhere.
  - New `reanchorLegacyEligibilityAccountTx` (replaces the earlier, never-wired-in
    `flagPolicyAnchorMigrationBlockedTx`) implementing 2.4's full (a)/(b)/(c) flow above; wired
    into `processEligibilityProjectionJob` right after `getEligibilityAccountTx`.
- `backend/internal/postgresstore/policy_anchor_integration_test.go` — see Tests changed below.
- `backend/internal/postgresstore/consumption_integration_test.go` — see Tests changed below.
- `backend/internal/postgresstore/balance_carry_forward_integration_test.go` — see Tests changed
  below.
- `backend/internal/postgresstore/store_integration_test.go` — new shared test helper
  `processEligibilityWithoutReanchor` (see Tests changed below).
- `backend/internal/migrate/migrate_test.go` —
  `TestPolicyAnchorMigrationValidatesBootstrapKindAndTriggerBoundary` extended with the guarded
  UPDATE trigger scenarios (see Tests changed below).
- `docs/superpowers/specs/2026-09-02-invoice-policy-anchor-design.md` — status line, `2.0`, `2.1`
  bullet 3, `2.4`, and `3` rewritten to match what shipped (superseding the "blocked, open
  question" framing from the prior commit).
- `docs/handoffs/XM-INV-POLICY-ANCHOR.md` — this file, rewritten.

**Not touched:** `backend/internal/postgresstore/source_sync.go` (`MarkSourceEventFailed`'s 2.5
auto-freeze rewrite already landed in `eb68989`, unaffected by this commit); any other migration
file; `release/`; `scripts/`; `RELEASE-READINESS.md`; `docs/PRODUCTION-RUNBOOK.md`;
`docs/IMAGE-SCAN-REVIEW.md`; `docs/superpowers/plans/`; any RC version identity; `contracts/`.

## Tests changed (every one, with its reason — nothing weakened to keep an old assertion)

**New, in `policy_anchor_integration_test.go`** (design 2.4's three required scenarios, built
through the real observe+project pipeline, not hand-crafted SQL state):
- `TestReanchorLegacyAccountMigratesAllocationsAndAudits` — happy path. Real prior consumption
  (payment + usage, built via the actual production projection functions called directly to stand
  in for a job that ran before this slice's re-anchor check existed — see the test's own doc
  comment for why that's not a contrivance: `reanchorLegacyEligibilityAccountTx`'s candidate
  lookup doesn't care whether a checkpoint was already used as balance evidence, so this is
  exactly what happens to every already-active legacy account the moment this slice deploys) is
  present, then a candidate checkpoint arrives and the next job run deletes the
  `consumption_allocations`, resets the lot to zero, updates the state row, and writes the
  `migrated` audit with distinct before/after hashes; a second run is a no-op.
- `TestReanchorLegacyAccountBlockedByReservationFreezesWithoutTouchingLedger` — blocked path. A
  reserved `invoice_allocations` row against the account's funding lot causes a freeze
  (`POLICY_ANCHOR_BLOCKED`) instead of a reset; the consumption ledger is provably untouched
  (stays at its real prior value, not reset to zero); the reservation itself is released and its
  request rejected by `freezeEligibilityTx`'s own pre-existing cascade (unrelated to this slice —
  every freeze reason does this); a second run with a *fresh* reservation (the first is gone,
  released by the freeze) confirms the idempotent already-blocked skip specifically, not just an
  account with nothing left to block on.
- `TestReanchorLegacyAccountWithNoPostPolicyCheckpointYetContinuesProjectionUnchanged` — no
  candidate yet. With genuinely no balance evidence at all covering new usage since
  `finalized_through`, the job legitimately hits the pre-existing (unrelated to design 2.4)
  `errBalanceCarryForwardProofPending` path — `ProcessEligibilityProjectionJobs` re-queues for
  retry rather than erroring, which is what "no error… retried on the next job" actually looks
  like from the caller's side; account stays `SIGNED_CUTOVER`/`active`, `finalized_through`
  unchanged, no freeze, no audit.

**Modified, in `policy_anchor_integration_test.go`:**
- `TestReconciliationCheckpointIgnoredPrePolicyAndBootstrapsPolicyAnchorPostPolicy` — its own
  `policyStart := fixtureNow` left no headroom for `postAsOf := policyStart.Add(1*time.Hour)` to
  also satisfy migration 0016's new `cutover_at <= now()` check (that check didn't exist when this
  test was last touched); changed to `policyStart := fixtureNow.Add(-2*time.Hour)`. No assertion
  changed — this is a pure fixture-timing fix.
- `TestEligibilityProjectionNeverAllocatesPreAnchorUsageFact` — doc comment only (it claimed
  `bootstrap_kind='POLICY_ANCHOR'` "cannot be constructed without a schema migration," stale now
  that migration 0016 is applied and wired in); the test itself never asserted `bootstrap_kind` so
  there was no logic to fix.
- Removed the now-unused `addCandidateCheckpoint` fixture helper (dead code after the 2.4 tests'
  own redesign settled on building real prior consumption directly, below).

**Modified, in `consumption_integration_test.go`:**
- `TestPostCutoverNewAccountReplaysFromGlobalCutoverAndBlocksSubscriptionWithoutUsage` — several
  changes, all consequences of 2.1's correction (no code path creates
  `POST_CUTOVER_REPLAY` any more) and 0016's new `cutover_at<=now()` check, not test bugs:
  - Bootstrap-kind assertion changed `"POST_CUTOVER_REPLAY"` → `"POLICY_ANCHOR"` (this account is
    exactly the non-baseline case 2.1's correction covers).
  - Switched from `integrationStore`'s shared, `UpsertFundingLot`-bootstrapped source instance and
    manifest (immutable once registered, and pinned to essentially "now") to a dedicated one this
    test registers itself, so the account's own cutover (2h after the manifest's, with further
    offsets up to +2h30m past that) has room to also satisfy `cutover_at<=now()`.
  - The `preWallet` payment's expected outcome changed from "retained for replay, `WALLET_CASH`,
    zero consumed" to "`LEGACY_NON_INVOICEABLE`, frozen" — a genuine behavioral consequence of
    2.1, not a fixture-timing issue: under the old `POST_CUTOVER_REPLAY` bootstrap,
    `account.CutoverAt` was pinned to the source's *global* cutover, so a wallet payment completed
    anywhere after that (even before this specific account's later per-account boundary) was
    retained for later replay. Under `POLICY_ANCHOR`, `account.CutoverAt` is the anchoring
    checkpoint's own `as_of` — later than the global cutover — and
    `applyFundingObservationEligibilityTx`'s wallet-cash branch keys off `account.CutoverAt`, so a
    payment completed before that (as `preWallet`'s is, by design) is simply not in scope for this
    account yet. `domain.EligibilitySubscriptionCash` is unaffected (keys off
    `account.GlobalCutoverAt`, unchanged by 2.1), which is why the subscription check right above
    it in the same test needed no change.
  - The final section's usage amount reduced 150→100 units, and its `postPayment`/`preWallet`
    consumption assertions updated to match (100,000/0 instead of 60,000/50,000 split): with
    `preWallet` no longer cash-eligible (previous bullet), only `postPayment`'s 100 units of
    eligible cash remain, so 150 units of usage would exceed available cash and freeze the account
    (`USAGE_EXCEEDS_LEDGER`, pre-existing and unrelated to this slice) instead of exercising what
    this section actually targets — post-bootstrap wallet usage becoming eligible and consumed.
- `TestV3FinalizedUsagePublishesConsumedCashAndAllowsPartialInvoices` and
  `TestUnknownPositiveCheckpointIsConservativelyPlacedBeforeIntervalUsage` — both fixtures predate
  design 2.4 and bootstrap `SIGNED_CUTOVER` accounts with a later "reconciliation" checkpoint
  dated at/after the account's policy start, for reasons unrelated to re-anchoring (usage-cash
  projection and unknown-positive-checkpoint placement respectively). `reanchorLegacyEligibilityAccountTx`'s
  candidate lookup doesn't know that — through the normal job queue, it mistook that checkpoint
  for a design-2.4 re-anchor candidate and re-anchored the account before either test's own logic
  ever ran, corrupting both (in one case masking real consumption entirely; in the other,
  eventually surfacing as a downstream `Submit` failure because the re-anchor's job-queue
  bookkeeping diverged from what `Submit`'s `projectionPending` check expected). Both switched
  their `ProcessEligibilityProjectionJobs` call(s) to the new shared helper
  `processEligibilityWithoutReanchor` (below) instead of adjusting fixture timing, since (for
  `TestV3Finalized...` especially) no single `policyStart` value could keep its payment
  post-policy *and* its checkpoint pre-policy at once — the two constraints are contradictory once
  both are pinned to the same fixture-relative offsets.

**New shared helper, in `store_integration_test.go`:**
- `processEligibilityWithoutReanchor(t, store, ctx, accountID, through, actor)` — drives the exact
  same sequence `processEligibilityProjectionJob` does (`ensureBalanceCarryForwardProofTx`,
  `reprojectEligibilityTx` twice, `evaluatePendingBalanceEvidenceTx`, advancing
  `finalized_through`, and clearing any queued `eligibility_projection_jobs` row for the account —
  the last of these was a bug in this helper's own first draft: without it, `Submit`'s
  `projectionPending` check saw a permanently-stuck queued job and rejected submissions
  downstream), called directly rather than through the job queue, skipping only
  `reanchorLegacyEligibilityAccountTx`. Used by the three tests above (in
  `consumption_integration_test.go`) plus two more in `balance_carry_forward_integration_test.go`
  (below) whose fixtures have the identical "legacy account, later checkpoint, unrelated test
  purpose" shape.

**Modified, in `balance_carry_forward_integration_test.go`:**
- `TestBalanceDeltaCarryForwardUsesLatestLowerSequenceActualAtSameAsOf` and
  `TestBalanceEvidenceEvaluatesCarryBeforeLaterRealCheckpoint` — same root cause and same fix as
  the two `consumption_integration_test.go` tests above: `seedCarryForwardFixture`'s shared
  account bootstraps legacy-kind with a reconciliation checkpoint dated at/after its policy start,
  for a test purpose (carry-forward proof evaluation) entirely unrelated to re-anchoring. Switched
  their `ProcessEligibilityProjectionJobs` calls to `processEligibilityWithoutReanchor`.

**Extended, in `migrate_test.go`:**
- `TestPolicyAnchorMigrationValidatesBootstrapKindAndTriggerBoundary` — the team lead's point 4
  explicitly asked for guarded-UPDATE trigger coverage beyond what the pre-existing three
  INSERT-focused scenarios (pre-migration rejection, valid bootstrap, no-matching-checkpoint
  rejection — unchanged) covered. Added, against a new legacy-bootstrapped account with a real
  matching post-policy checkpoint: rejects the guarded UPDATE with no GUC set; rejects with the
  GUC set but `OLD.bootstrap_kind` already `POLICY_ANCHOR` (not legacy — exercised against the
  first scenario's own now-`POLICY_ANCHOR` account); rejects with the GUC set but
  `NEW.bootstrap_kind` staying legacy; rejects with the GUC set, correct kinds, but values matching
  no checkpoint; accepts with the GUC set, correct kinds, and values matching the real checkpoint.

## Tests run

From `backend/`, `GOFLAGS=-buildvcs=false`,
`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test?sslmode=disable`:

```
go build ./...                 # exit 0
go vet ./...                   # exit 0
go test -p 1 -count=1 ./...    # all packages ok, see below
```

Full package list, all passing (run together and individually, multiple times across this
session): `cmd/api`, `cmd/bootstrap-settings`, `cmd/bootstrap-sources`, `cmd/keygen`,
`cmd/migrate`, `cmd/mtlsgen`, `cmd/oidc-preflight`, `cmd/pdf-policy-check`,
`internal/adminsettings`, `internal/application`, `internal/auth`, `internal/backuparchive`,
`internal/backupverify`, `internal/document`, `internal/domain`, `internal/httpapi`,
`internal/ledger`, `internal/mailer`, `internal/migrate` (incl. the expanded migration test),
`internal/oidcretention`, `internal/pdfscanner`, `internal/postgresstore` (incl. all tests listed
above), `internal/securefields`, `internal/sourceingest`.

**One transient infrastructure flake encountered and confirmed non-reproducible:** a single full
`./...` run showed `TestSubmitTransactionIdempotencyAndConcurrency` failing several of its
concurrent goroutines with raw `dial tcp 127.0.0.1:55432` connection errors (Docker Desktop /
Windows host-to-container port-forwarding instability, a known class of issue in this
environment) — re-ran in isolation immediately after and it passed cleanly; re-ran the entire
`./...` suite again immediately after that and everything passed with no flakes. Not a code issue.

`gofmt` is clean on every file this commit touches. `go vet ./...` clean.

## Not run

- Anything requiring a server/production connection.
- The rehearsal-on-a-backup-restore verification gate (design section 3 item 3) — out of scope for
  this worktree/task, no server access.

## Risks / things to sign off on

1. **Migration 0016 in this commit fully replaces `cb53142`'s version** (not an incremental
   `ALTER`/new migration file) — both are unreleased/undeployed, so per the team lead's own
   confirmation earlier in this slice's history, editing the file in place is correct
   ("forward-only" applies to the deployed chain, not an in-development migration). Worth
   double-checking at merge that no other branch/worktree has taken a dependency on `cb53142`'s
   specific (narrower) trigger conditions in the meantime.
2. **The latent `balance_reconciliation_checkpoints` INSERT bug** (missing `configuration_hash`
   placeholder, misplaced `::numeric` cast — see Files changed above) was introduced somewhere
   between `eb68989` and this commit's starting point, not present in `eb68989` itself as far as
   this session determined; it was masked by the SQL-syntax error until the column-count bug was
   fixed, at which point the type-mismatch became visible. Both are fixed together in this commit.
   Worth a second look given it's a correctness fix bundled into a feature commit, not a separate
   one — kept together because splitting it out would have required re-deriving which commit
   introduced it, and this commit is still unreleased.
3. **`reanchorLegacyEligibilityAccountTx`'s block-then-self-resolve behavior:** once a
   `POLICY_ANCHOR_BLOCKED` freeze is open and the blocking reservation later clears (released or
   issued), the *next* job run finds no more exposure and proceeds straight to re-anchoring
   (assuming a candidate checkpoint exists) without any special "unblock" step — this is by design
   (the exposure check and the candidate check are both evaluated fresh, unconditionally, every
   run) but is worth the team lead's explicit confirmation that this closed-loop behavior (freeze
   → cleared reservation → automatic re-anchor on the very next run) is intended, since the
   original spec's wording ("freeze… for manual review") could also be read as expecting a human
   step in between.
4. No new float amounts, no logged/persisted secrets, no `contracts/` changes, no admin-OIDC
   changes, no touch to any file outside this slice's stated scope — checked.

## Follow-ups (recommended, not blocking this delivery)

1. Re-run the design's rehearsal-on-a-restored-backup step (design doc section 3, item 3) before
   any production rollout — this needs server/production access this task explicitly did not
   have.
2. Consider whether `reanchorLegacyEligibilityAccountTx`'s exposure check (reserved/issued/
   refund_attention allocations) should also be exercised against a `refund_attention` allocation
   specifically in a dedicated test — the current blocked-path test uses `reserved` only; the
   trigger condition itself covers all three states identically, but no test isolates the other
   two.
3. 2.5's remaining gap (documented in the design doc, unchanged by this commit): a dead event
   whose underlying domain write never succeeded on any attempt still cannot be attributed to a
   specific account from this layer and still holds its cycle — out of scope for this slice.
