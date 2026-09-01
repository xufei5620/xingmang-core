# XM-INV-POLICY-ANCHOR — 记账锚定到开票政策日

**Status:** DESIGN, mid-implementation (2026-09-02). Sections 2.1/2.2/2.3/2.5 implemented and
tested; 2.4 blocked on an open question (see 2.4's implementation note). See **2.0** for a
schema decision made during implementation, and `docs/handoffs/XM-INV-POLICY-ANCHOR.md` for the
full implementation record (files changed, tests, exact gate results).
**Owner decision:** 开票只针对 2026-09-01 00:00 (Asia/Shanghai) 之后的真实充值；此前流水对开票无用。
**Replaces:** the idea of re-running `cutover-init` — rejected because `CaptureCutover`
snapshots the live database at run time and cannot produce a historical (9/1) baseline.

## 2.0 Implementation note: migration 0016 (decision made 2026-09-02, mid-implementation)

This slice was originally scoped as pure code changes, no migration. Implementation found two
schema conflicts before writing any code: `source_account_eligibility_state`'s
`..._bootstrap_kind_check` CHECK constraint and its `enforce_account_eligibility_cutover_contract`
trigger (migration `0011`) only recognize `SIGNED_CUTOVER`/`POST_CUTOVER_REPLAY` — no
`POLICY_ANCHOR` value exists and neither can be reused (the trigger rejects any `cutover_at`
other than the exact global `manifest.cutover_at` for `POST_CUTOVER_REPLAY`, which is precisely
what 2.1 needs to not use). Separately, `eligibility_freezes.freeze_reason` (migration `0009`)
has no `EVENT_DEAD` value, blocking 2.5's auto-freeze half.

**Decision:** add `backend/migrations/0016_policy_anchor.sql`, scoped to exactly:
1. Extend the `bootstrap_kind` CHECK to `IN ('SIGNED_CUTOVER','POST_CUTOVER_REPLAY','POLICY_ANCHOR')`.
2. Add a `POLICY_ANCHOR` branch to `enforce_account_eligibility_cutover_contract()`: requires (a)
   `NEW.cutover_at >= invoice_eligibility_policy.eligibility_start_at`, and (b) a matching
   `balance_reconciliation_checkpoints` row exists (`checkpoint_kind='reconciliation'`,
   `as_of=NEW.cutover_at`, `balance_service_units=NEW.cutover_balance_units`,
   `unit_code=NEW.unit_code`) — plus the existing manifest-hash check, unchanged.
3. Extend `eligibility_freezes.freeze_reason`'s CHECK with `'EVENT_DEAD'` (design 2.5) and
   `'POLICY_ANCHOR_BLOCKED'` (design 2.4's fallback — see 2.4's implementation note below; not in
   the original migration content list, added because 2.4's fallback needed it).

**A necessary deviation found while implementing (2):** the cutover-boundary trigger had to
become a `DEFERRABLE INITIALLY DEFERRED` `CONSTRAINT TRIGGER` (was a plain immediate `BEFORE`
trigger). Condition (b) reads `balance_reconciliation_checkpoints` to confirm the anchor
checkpoint exists, but that table's own pre-existing `balance_reconciliation_checkpoints_contract_guard`
trigger requires a *trusted* `source_account_eligibility_state` row to already exist before
accepting a checkpoint insert. A `POLICY_ANCHOR` bootstrap inserts both rows in one transaction;
under immediate `BEFORE` semantics, whichever row is inserted first is rejected by the *other*
table's trigger, because each side's validation depends on the other row already existing (state
row first satisfies the checkpoint's trigger but not condition (b), checked too early;
checkpoint first satisfies condition (b) but fails the checkpoint table's own trigger). Deferring
this table's check to `COMMIT` — the same pattern migration `0009` already uses for the
analogous mutual check between `funding_lots` and `funding_lot_consumption_state`
(`funding_lots_consumption_state_guard` / `funding_lot_consumption_mirror_guard`) — resolves the
ordering deadlock without weakening either check: insert the state row then the checkpoint row
(the only order the checkpoint table's own trigger allows), and by `COMMIT` both rows exist
regardless of order, so this trigger's read of the checkpoint table sees it. The function never
modifies `NEW` (pure validation), so `BEFORE`→`AFTER` (required for a `CONSTRAINT TRIGGER`)
changes nothing about what it enforces, only when. Verified with a throwaway scratch program
before touching real code (5 scenarios: valid bootstrap succeeds; mismatched balance, wrong
`cutover_at`, and no matching checkpoint at all each correctly rejected at `COMMIT`), then
confirmed against the full existing test suite (no regression in the `SIGNED_CUTOVER`/
`POST_CUTOVER_REPLAY` paths, which use the same trigger).

## 1. Problem (measured on production, 2026-09-02)

- Signed cutover baseline is 2026-08-24T23:49Z; invoice policy start
  (`adminsettings.RequiredEligibilityStartAt`) is 2026-08-31T16:00Z (= 9/1 00:00 Beijing).
- Every first login replays the account's full post-cutover history. Of the parked sub2api
  backlog, 290,146 of 331,606 events (87.5%) precede the policy start; the first production
  whale (account 40bd883d…, sub2api user 34) carried 3,481 pre-policy vs 646 post-policy usage
  facts (84% waste). Pre-policy facts only serve to *derive* the account's non-invoiceable
  balance at the policy boundary — a number the balances stream already *observes*
  (reconciliation checkpoints exist in the boundary hour: 10 rows between 15:30Z and 15:49Z).
- Separately: a single `failed` balance_checkpoint event (PROJECTION_FAILED ×3, `dead` after 8)
  holds scan cycle 5b5c26bc in `processing`, which blocks `BALANCE_PROOF` coverage for every
  account on the stream — the RC62 wedge in a second costume.

## 2. Design

### 2.1 Policy-anchored bootstrap (postgresstore/consumption.go, ObserveBalanceCheckpoint)
When an account has no eligibility state and `manifest.CutoverAt < policy.StartAt`:
- Baseline members: do **not** wait for the parked signed cutover row. Bootstrap from the
  first *reconciliation* checkpoint whose `as_of >= PolicyStartAt`:
  `cutover_at := checkpoint.as_of`, `cutover_balance_units := checkpoint.balance_service_units`
  (entirely non-invoiceable), `bootstrap_kind := 'POLICY_ANCHOR'`, `cutover_manifest_hash` as
  today. The signed cutover row, if it later arrives, is recorded as a checkpoint but must not
  re-bootstrap (existing "state already exists → ErrConflict" guard stays).
- Checkpoints with `as_of < PolicyStartAt` for an account without state: ignore (return nil,
  audit `eligibility.pre_policy_checkpoint.skipped`) instead of waiting on
  `source_eligibility_cutover`.
- Non-baseline accounts keep POST_CUTOVER_REPLAY unchanged (their first checkpoint is already
  post-policy in practice; if it is not, the same anchor rule applies).
- Carry-forward proofs (`ensureBalanceCarryForwardProofTx`) anchor at `account.CutoverAt`, which
  is now the policy checkpoint — no change needed, but add a test that a POLICY_ANCHOR account
  passes proof evaluation with only post-anchor checkpoints.

### 2.2 Pre-policy fact skipping at wake time (postgresstore/source_sync.go, RequeueSourceDependency)
When a `source_external_account` / `invoice_oidc_user` wake releases parked events for an
account, bulk-mark parked **usage_event** and **balance_checkpoint** events with
`observed_at < PolicyStartAt` as `processed` with `processing_error='PRE_POLICY_SKIPPED'`
in the same UPDATE; release the rest as today. Payments, credits, identities and manifests are
never skipped (few in number; legacy funding lots keep their BEFORE_ELIGIBILITY_START display).
The scan-cycle completeness query already ignores `processed`, so skipped events never hold a
cycle. PolicyStartAt comes from `invoice_eligibility_policy` (same source the projection uses).

### 2.3 Projection windows
No query change required: usage/credit/lot/checkpoint windows are bounded below by
`account.CutoverAt`. Add an assertion-style test that a POLICY_ANCHOR account with a pre-anchor
usage fact present in `source_usage_events` never allocates it.

### 2.4 Re-anchoring already-bootstrapped accounts (one-time, idempotent)
Accounts with `bootstrap_kind IN ('SIGNED_CUTOVER','POST_CUTOVER_REPLAY')` and
`cutover_at < PolicyStartAt` are re-anchored by the next eligibility projection job:
pick the first reconciliation checkpoint `as_of >= PolicyStartAt`; rewrite `cutover_at`,
`cutover_balance_units`, `bootstrap_kind='POLICY_ANCHOR'`, `finalized_through := cutover_at`,
delete the account's `consumption_allocations`, reset lot consumption state, and reproject.
Audit `eligibility.policy_anchor.migrated` with before/after values. Invoice reservations on
those lots are zero in production today (verify in rehearsal; if non-zero, freeze instead of
migrating and surface for manual review).

**Implementation note (2026-09-02, blocked, open question):** two problems found while
implementing, in order.

First — the DELETE-and-reinsert mechanism this section calls for cannot run inside
`processEligibilityProjectionJob` as written: `eligibility_projection_jobs.external_account_id`
(the job's own row, necessarily present and locked for the entire duration of the job attempting
the DELETE) and `source_account_stream_watermarks.external_account_id` both carry non-deferrable
`ON DELETE RESTRICT` foreign keys to `source_account_eligibility_state`. Per instruction, this
was not forced through (would mean restructuring the shared job's own row-lifecycle contract,
used by every account's ordinary reprojection, not just this case) — implemented instead as
freeze-and-continue: detect the condition, freeze the account (new reason
`POLICY_ANCHOR_BLOCKED`, migration 0016) with an audit event recording the would-be migration's
before/after values for manual review, and continue the normal (unchanged-boundary) reprojection
that follows rather than skip it — the ledger math is unaffected either way; freezing only blocks
*new* invoice activity until a human resolves it. This part is written
(`flagPolicyAnchorMigrationBlockedTx` in `postgresstore/consumption.go`) but not yet wired in —
see the second problem.

Second, found by running the full test suite after wiring the above into
`processEligibilityProjectionJob`: **`cutover_at < PolicyStartAt` is not a distinguishing
condition.** `RegisterCutoverManifest` and a DB trigger both hard-require
`manifest.cutover_at < policy_start` for *every* real manifest, and both `SIGNED_CUTOVER` and
`POST_CUTOVER_REPLAY` set `cutover_at := manifest.cutover_at` — so this is true for essentially
every bootstrapped account there will ever be, including a brand-new `POST_CUTOVER_REPLAY`
account bootstrapping under perfectly normal operation tomorrow, not just accounts that predate
this slice with a genuinely stale boundary. Wiring the check in as written started freezing
healthy, unrelated accounts in existing tests. Reverted the call site rather than ship a
financial freeze condition that fires on every account. **Open question, not resolved by this
implementation pass:** what the real distinguishing signal should be. Candidates: (a) run this as
a true one-time backfill (a script/job executed once over the account population that existed
*at deploy time*, not logic living inside the ordinary per-job path that runs forever), (b) some
other real distinguishing condition in the data that isn't `cutover_at < PolicyStartAt` alone
(none identified so far — there is no column recording "bootstrapped before this slice shipped"),
or (c) drop 2.4 from scope and leave those accounts on their existing, working boundary. See the
handoff doc for the account-by-account detail of what broke and how it was diagnosed.

### 2.5 Cycle completeness vs failed/dead events (postgresstore/source_sync.go)
`tryPublishEconomicScanCyclesTx` treats `failed` and `dead` events like `parked_identity`
(not holding the cycle) **only when** the account was frozen for that event (the processor
already freezes on payload drift / conflict). Pure transient failures (retry budget not yet
exhausted) still hold the cycle. A `dead` event without a freeze freezes the account with
reason `EVENT_DEAD` so the ledger stays honest.

## 3. Verification gates (all must pass before production)
1. Unit + integration suites (full `go test -p 1 ./...` with INVOICE_TEST_DATABASE_URL). Done as
   of 2026-09-02 for everything except 2.4 (see 2.4's implementation note) — see the handoff doc
   for exact package/test results.
2. New integration tests: policy-anchor bootstrap for a baseline member (done, includes the
   ignore-pre-policy half and the post-policy bootstrap half, plus the account functioning
   normally afterward); pre-policy skip at wake (done); re-anchor migration of a `SIGNED_CUTOVER`
   account (**not done** — blocked on 2.4's open question above); failed-event cycle publication
   with freeze (done, covers both the frozen-excludes-the-cycle half and, separately, the
   `EVENT_DEAD` auto-freeze). Also added: three tests directly on migration 0016 (pre-migration
   rejection, valid post-migration bootstrap, condition-(b) rejection).
3. **Rehearsal on an isolated restore** of production backup `invoice-20260901T091600Z`
   (deploy/backup/restore-drill.sh): run the new image against it, wake sub2api user 34, and
   record: anchor checkpoint chosen, cutover balance, released vs skipped counts, projection
   wall time, resulting `consumed_cash_minor` for lot 3347 (order 3347, 500分) and the pre-policy
   legacy lots' display state. Compare against a manual expectation before signing off.
4. Production ceremony as usual (fresh backup, signed release, cutover order incl.
   `restart api → restart ingest-proxy`).

## 4. Out of scope
Chunked/set-based replay (XM-INV-REPLAY-PERF phase 2), username persistence, embed link params.
