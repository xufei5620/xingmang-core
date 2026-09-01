# XM-INV-POLICY-ANCHOR — 记账锚定到开票政策日

**Status:** Implemented (2026-09-02). Sections 2.1–2.5 implemented and tested, including 2.4's
guarded re-anchor UPDATE (see 2.4 and 2.0 below for the mechanism finally adopted — this
superseded an earlier DELETE-and-reinsert draft that turned out to be blocked by non-deferrable
foreign keys, and a since-corrected "cutover_at < policy start" trigger-signal draft that was not
actually a distinguishing condition). See **2.0** for the schema decision, and
`docs/handoffs/XM-INV-POLICY-ANCHOR.md` for the full implementation record (files changed, tests,
exact gate results).
**Owner decision:** 开票只针对 2026-09-01 00:00 (Asia/Shanghai) 之后的真实充值；此前流水对开票无用。
**Replaces:** the idea of re-running `cutover-init` — rejected because `CaptureCutover`
snapshots the live database at run time and cannot produce a historical (9/1) baseline.

## 2.0 Implementation note: migration 0016

This slice was originally scoped as pure code changes, no migration. Implementation found two
schema conflicts before writing any code: `source_account_eligibility_state`'s
`..._bootstrap_kind_check` CHECK constraint and its `enforce_account_eligibility_cutover_contract`
trigger (migration `0011`) only recognize `SIGNED_CUTOVER`/`POST_CUTOVER_REPLAY` — no
`POLICY_ANCHOR` value exists and neither can be reused (the trigger rejects any `cutover_at`
other than the exact global `manifest.cutover_at` for `POST_CUTOVER_REPLAY`, which is precisely
what 2.1 needs to not use). Separately, `eligibility_freezes.freeze_reason` (migration `0009`)
has no `EVENT_DEAD` value, blocking 2.5's auto-freeze half. `backend/migrations/0016_policy_anchor.sql`
addresses both, plus 2.4's re-anchor mechanism (part 3, below), in one file:

1. Extend the `bootstrap_kind` CHECK to `IN ('SIGNED_CUTOVER','POST_CUTOVER_REPLAY','POLICY_ANCHOR')`.
2. Rewrite `enforce_account_eligibility_cutover_contract()`'s `POLICY_ANCHOR` branch: `NEW.cutover_at`
   must be `>= invoice_eligibility_policy.eligibility_start_at`, strictly `>` the account's own
   `source_cutover_manifests.cutover_at`, and `<= now()`; `NEW.finalized_through` must equal
   `NEW.cutover_at` (a `POLICY_ANCHOR` account is fully caught up through its own anchor point by
   construction); and a matching `balance_reconciliation_checkpoints` row must exist
   (`checkpoint_kind='reconciliation'`, `as_of=NEW.cutover_at`,
   `balance_service_units=NEW.cutover_balance_units`, `unit_code=NEW.unit_code`) — plus the
   existing manifest-hash check, unchanged. The `SIGNED_CUTOVER`/`POST_CUTOVER_REPLAY` branches
   are unchanged.
3. The same function/trigger also carries 2.4's guarded re-anchor UPDATE exception (see 2.4
   below) — the trust boundary (`cutover_at`/`cutover_balance_units`/`bootstrap_kind`/
   `finalized_through`, plus `external_account_id`/`source_instance_id`/`unit_code`/
   `cutover_manifest_hash`/`finalization_delay_seconds`) stays immutable on `UPDATE` with exactly
   one exception: a transaction carrying the transaction-local setting
   `invoice.policy_anchor_reanchor='on'` (via `set_config(...,true)`, never persisted) may change
   `cutover_at`, `cutover_balance_units`, `bootstrap_kind` and `finalized_through` on a row whose
   `OLD.bootstrap_kind` is `SIGNED_CUTOVER` or `POST_CUTOVER_REPLAY` and whose `NEW.bootstrap_kind`
   is `POLICY_ANCHOR` — every other column must stay byte-for-byte identical, and the new values
   still go through the exact same `POLICY_ANCHOR` validation from part 2 above. This is why the
   guarded UPDATE replaces the DELETE-and-reinsert shape an earlier draft of 2.4 called for (see
   2.4) rather than needing its own separate mechanism.
4. Extend `eligibility_freezes.freeze_reason`'s CHECK with `'EVENT_DEAD'` (design 2.5) and
   `'POLICY_ANCHOR_BLOCKED'` (design 2.4's block-on-exposure branch).

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
changes nothing about what it enforces, only when. This deferred-commit shape is also exactly
what makes the guarded re-anchor UPDATE (part 3) safe to reason about: it is checked once, at
`COMMIT`, against whatever the row and its checkpoint evidence look like by then, not against an
intermediate state. Verified with a throwaway scratch program before touching real code (9
scenarios spanning both the `POLICY_ANCHOR` insert validation and the guarded UPDATE exception),
then confirmed against the full existing test suite (no regression in the `SIGNED_CUTOVER`/
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
- **Superseded during implementation:** this bullet originally read "non-baseline accounts keep
  POST_CUTOVER_REPLAY unchanged." The team lead's follow-up decision (2026-09-02, alongside 2.4's
  final mechanism below) corrected this: `ObserveBalanceCheckpoint`'s no-state branch has exactly
  two outcomes for every account, baseline member or not — `as_of < PolicyStartAt` → skip (audit
  as above), `as_of >= PolicyStartAt` → bootstrap `POLICY_ANCHOR` exactly as the baseline-member
  case above. After this slice, no code path creates a new `SIGNED_CUTOVER` or
  `POST_CUTOVER_REPLAY` row; those two kinds are legacy only, carried forward by accounts
  bootstrapped before this slice and re-anchored in place by 2.4.
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

### 2.4 Re-anchoring already-bootstrapped accounts (one-time per account, idempotent)

**Signal.** The distinguishing condition is `bootstrap_kind`, not `cutover_at < PolicyStartAt` —
an earlier draft of this section used the latter and it does not work: `RegisterCutoverManifest`
and a DB trigger both hard-require `manifest.cutover_at < policy_start` for *every* real
manifest, and both legacy bootstrap kinds set `cutover_at := manifest.cutover_at`, so
`cutover_at < PolicyStartAt` is true for essentially every bootstrapped account there will ever
be — including a brand-new legacy-kind account bootstrapping under perfectly normal operation
after this slice ships, not just accounts that predate it with a genuinely stale boundary. Since
2.1 (as corrected above) means no code path creates a new `SIGNED_CUTOVER`/`POST_CUTOVER_REPLAY`
row after this slice deploys, `bootstrap_kind IN ('SIGNED_CUTOVER','POST_CUTOVER_REPLAY')` by
itself correctly identifies exactly the accounts this section is for: ones bootstrapped *before*
this slice existed.

**Mechanism.** `processEligibilityProjectionJob`, right after loading the account and before
projecting, when `account.bootstrap_kind` is legacy:
- If any `invoice_allocations` row referencing one of the account's `funding_lots` is `reserved`,
  `issued`, or `refund_attention`: freeze the account (`POLICY_ANCHOR_BLOCKED`, migration 0016),
  audit `eligibility.policy_anchor.blocked`, and do not touch the ledger. Idempotent: if a
  `POLICY_ANCHOR_BLOCKED` freeze is already open for this account, skip silently (no duplicate
  freeze or audit row) — this also self-resolves on a later job once the exposure clears (the
  reservation is released or issued), since the next run's exposure check will no longer find it
  and will fall through to re-anchoring.
- Else if no reconciliation checkpoint with `as_of >= PolicyStartAt` exists yet for this account:
  continue the projection under the existing (legacy) anchor, no audit, no error — forcing an
  error here would incorrectly block settlement for every legacy account until one arrives; it is
  retried (implicitly, by finding the same "no candidate" state) on the next job.
- Else, in one transaction: set the session-local, transaction-scoped setting
  `invoice.policy_anchor_reanchor='on'` (`set_config(...,true)`, never persisted outside the
  transaction — this is deliberately not a role/session-level bypass); delete the account's
  `consumption_allocations`; reset the affected `WALLET_CASH`/`SUBSCRIPTION_CASH` lots'
  consumption state (`funding_lot_consumption_state` and `funding_lots.consumed_cash_minor` back
  to zero); `UPDATE source_account_eligibility_state` in place — `cutover_at` and
  `cutover_balance_units` from the candidate checkpoint, `bootstrap_kind='POLICY_ANCHOR'`,
  `finalized_through := cutover_at` — which migration 0016's guarded UPDATE exception (see 2.0)
  accepts precisely because the GUC is set, the row's `OLD.bootstrap_kind` is legacy, and the new
  values pass the same `POLICY_ANCHOR` validation an INSERT would; audit
  `eligibility.policy_anchor.migrated` with before/after values (`bootstrap_kind`, `cutover_at`,
  `cutover_balance_units`) plus the number of `consumption_allocations` deleted; then continue
  projecting from the new anchor in the same job run. `bootstrap_kind` transitioning away from
  `SIGNED_CUTOVER`/`POST_CUTOVER_REPLAY` is itself what makes a second run a no-op — no separate
  idempotency bookkeeping is needed for this branch.

**Why an UPDATE, not the DELETE-and-reinsert this section originally called for:**
`eligibility_projection_jobs.external_account_id` (the job's own row, locked for the job's
duration) and `source_account_stream_watermarks.external_account_id` both carry non-deferrable
`ON DELETE RESTRICT` foreign keys to `source_account_eligibility_state` — a DELETE cannot run
inside the job that would need to issue it without restructuring the shared job's own
row-lifecycle contract, used by every account's ordinary reprojection, not just this case. The
guarded UPDATE (migration 0016 part 3) sidesteps this entirely: those foreign keys never fire
because the row is never deleted, only updated in place, under a narrow, auditable exception to
its otherwise-immutable trust boundary.

Verified with integration tests covering: the re-anchor happy path (real prior consumption —
built through the normal observe+project pipeline — has its `consumption_allocations` deleted,
lot consumption reset to zero, the state row updated, and a `migrated` audit row with distinct
before/after hashes; a second job run is a no-op); the blocked-by-reservation path (frozen,
ledger untouched, reservation released and its request rejected by the freeze's own pre-existing
cascade; a second run with a fresh reservation confirms the idempotent already-blocked skip
specifically, not just an account with nothing left to block on); the no-candidate-yet path
(projection proceeds unchanged under the existing anchor, no freeze, no audit — concretely
surfaces today as the pre-existing, unrelated `errBalanceCarryForwardProofPending` mechanism
retrying the job rather than as some design-2.4-specific "pending" state); and the guarded
UPDATE trigger exception directly (rejects without the GUC, with the GUC but a non-legacy
`OLD.bootstrap_kind`, with the GUC but a non-`POLICY_ANCHOR` `NEW.bootstrap_kind`, and with values
matching no checkpoint; accepts with the GUC and valid values). See the handoff doc for the exact
test names and the list of pre-existing tests whose fixtures needed updating for the "legacy
kinds truly legacy" rule (2.1) rather than being weakened to keep an old assertion.

### 2.5 Cycle completeness vs failed/dead events (postgresstore/source_sync.go)
`tryPublishEconomicScanCyclesTx` treats `failed` and `dead` events like `parked_identity`
(not holding the cycle) **only when** the account was frozen for that event (the processor
already freezes on payload drift / conflict). Pure transient failures (retry budget not yet
exhausted) still hold the cycle. A `dead` event without a freeze freezes the account with
reason `EVENT_DEAD` so the ledger stays honest.

## 3. Verification gates (all must pass before production)
1. Unit + integration suites (full `go test -p 1 ./...` with INVOICE_TEST_DATABASE_URL). Done as
   of 2026-09-02 — see the handoff doc for exact package/test results.
2. New integration tests: policy-anchor bootstrap, unified for baseline and non-baseline accounts
   per the corrected 2.1 (done, includes the ignore-pre-policy half, the post-policy bootstrap
   half, and the account functioning normally afterward); pre-policy skip at wake (done); 2.4's
   re-anchor of a legacy account — happy path, blocked-by-reservation, no-candidate-yet, and the
   guarded UPDATE trigger exception directly (done — see 2.4 above and the handoff doc for the
   exact test names); failed-event cycle publication with freeze (done, covers both the
   frozen-excludes-the-cycle half and, separately, the `EVENT_DEAD` auto-freeze). Also added:
   migration-0016-specific tests covering both the `POLICY_ANCHOR` insert validation and the
   guarded re-anchor UPDATE exception (pre-migration rejection, valid post-migration bootstrap,
   no-matching-checkpoint rejection, and the four guarded-UPDATE trigger scenarios).
3. **Rehearsal on an isolated restore** of production backup `invoice-20260901T091600Z`
   (deploy/backup/restore-drill.sh): run the new image against it, wake sub2api user 34, and
   record: anchor checkpoint chosen, cutover balance, released vs skipped counts, projection
   wall time, resulting `consumed_cash_minor` for lot 3347 (order 3347, 500分) and the pre-policy
   legacy lots' display state. Compare against a manual expectation before signing off.
4. Production ceremony as usual (fresh backup, signed release, cutover order incl.
   `restart api → restart ingest-proxy`).

## 4. Out of scope
Chunked/set-based replay (XM-INV-REPLAY-PERF phase 2), username persistence, embed link params.
