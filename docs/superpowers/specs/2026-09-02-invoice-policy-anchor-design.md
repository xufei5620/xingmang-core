# XM-INV-POLICY-ANCHOR — 记账锚定到开票政策日

**Status:** Implemented (2026-09-02). Sections 2.1–2.5 implemented and tested, including 2.4's
guarded re-anchor UPDATE (see 2.4 and 2.0 below for the mechanism finally adopted — this
superseded an earlier DELETE-and-reinsert draft that turned out to be blocked by non-deferrable
foreign keys, and a since-corrected "cutover_at < policy start" trigger-signal draft that was not
actually a distinguishing condition). See **2.0** for the schema decision, and
`docs/handoffs/XM-INV-POLICY-ANCHOR.md` for the full implementation record (files changed, tests,
exact gate results). **2.6 (added 2026-09-02, slice XM-INV-PREANCHOR-USAGE) closes a production
gap this design left open**: the projection path (`observeEligibilityFact`, not `2.1`'s bootstrap
path) still froze a POLICY_ANCHOR account's own pre-anchor usage/credit facts as `SOURCE_GAP` —
see **2.6** and `docs/handoffs/XM-INV-PREANCHOR-USAGE.md`. **2.7 (added 2026-09-03, slice
XM-INV-ANCHOR-BALANCE) closes a second, distinct production gap**: 2.1's own claim in its
"Carry-forward proofs" bullet below ("anchor at `account.CutoverAt`... no change needed") turned
out to be only half true — `account.CutoverAt` was already the correct lower bound for
`buildEligibilityProjectionTx`'s window, but `evaluatePendingBalanceEvidenceTx`'s *separate*
trusted-interval query never recognized it as a valid interval start, so a POLICY_ANCHOR account's
own anchor checkpoint (and everything evaluated before any later checkpoint matched) froze
`SOURCE_GAP` — see **2.7** and `docs/handoffs/XM-INV-ANCHOR-BALANCE.md`. **2.8 (added 2026-09-03,
slice XM-INV-BALANCE-BLIP) closes a third, distinct production gap in the same evaluator**: once
2.7's fix let a checkpoint self-heal on a positive difference, a single *transient* positive
difference (a boundary-timing coincidence between the balances and usage streams, or any other
one-off blip) still permanently inflated the account's expected balance, freezing every later
checkpoint `UNKNOWN_NEGATIVE_BALANCE` by the same constant amount — see **2.8** and
`docs/handoffs/XM-INV-BALANCE-BLIP.md`.
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

### 2.6 Pre-anchor usage/credit facts must not freeze SOURCE_GAP (added 2026-09-02, slice
XM-INV-PREANCHOR-USAGE, production incident)

**Gap.** RC68 (this design) went live 2026-09-01T23:58Z. At 2026-09-02T02:00:13Z the Sub2API
balances cycle bootstrapped 106 accounts to `bootstrap_kind='POLICY_ANCHOR'` via 2.1's checkpoint
path. Within the same second, a Sub2API usage-stream daily reconcile replay (~99,900 usage
records) delivered pre-anchor usage/credit facts for those same 106 accounts —
`event_time` after the invoice policy start but at or before each account's own fresh
`cutover_at`. **2.1/2.2/2.3 only handle this timing at bootstrap time** (a no-state account) and
at identity-wake time (`RequeueSourceDependency`'s bulk skip) — neither covers a fact that arrives
through the *ordinary projection path*, `postgresstore/consumption.go`'s `observeEligibilityFact`,
for an account that **already has** `POLICY_ANCHOR` state. That function's
`!in.EventTime.After(account.CutoverAt)` check (present before this design, meant to catch a real
gap in a legacy account's replayed history) treated every one of these facts as `SOURCE_GAP`,
freezing the account and returning `ErrConflict`; the source events retried every 5 minutes and
went `dead` after 8 attempts. Production result: 106 open `SOURCE_GAP` freezes, ~100 open
`EVENT_DEAD` freezes, ~102 dead/failed `source_ingest_events`, and the readiness gate returning
503 (`cmd/api/runtime.go`'s `validateSourceIngestRuntimeReadiness` fails closed whenever any dead
event exists).

**Why this is not evidence of a gap.** A `POLICY_ANCHOR` account's `cutover_at` is a reconciliation
checkpoint's own `as_of` (2.1) — an observed fact, not a replayed history boundary the way a
`SIGNED_CUTOVER`/`POST_CUTOVER_REPLAY` account's cutover is. A fact at or before it can never be
attributed to any ledger baseline, by construction, whether or not it also predates the account's
own policy start. `SOURCE_GAP` correctly still applies to a legacy account's fact at or before its
cutover (real replayed history, so a missing predecessor really is a gap) — this section changes
nothing about that case.

**Fix.** `observeEligibilityFact`'s `SOURCE_GAP` branch now checks `account.BootstrapKind` first:
for `POLICY_ANCHOR`, the fact is skipped (one audit row, `eligibility.usage.pre_anchor_skipped` /
`eligibility.credit.pre_anchor_skipped`, no freeze, `nil` returned so the caller marks the source
event processed) instead of frozen. No schema change — consistent with 2.1/2.2's own precedent, a
skipped fact is represented by its audit row only, never persisted as a `source_usage_events`/
`source_credit_events` row. Legacy bootstrap kinds are unchanged.

**Production repair.** A versioned, human-approved `eligibility-repair` CLI
(`backend/cmd/eligibility-repair`, store method `RepairPreAnchorUsageEligibility`) resolves the
freezes and requeues the events this gap left behind, scoped precisely to the accounts and facts
this bug — and only this bug — could have produced; a legacy account's real `SOURCE_GAP` freeze is
never touched. See `docs/handoffs/XM-INV-PREANCHOR-USAGE.md` for the exact runbook and expected
counts.

Also added: `application/source_processor.go`'s `RunOnce` now logs (slog Warn) every projection
failure — source/stream/event ids and the error text, never payload contents — before marking an
event `PROJECTION_FAILED`/dead, so a future incident of this shape is visible in application logs,
not only discoverable by querying the database.

### 2.7 A POLICY_ANCHOR account's own anchor checkpoint must be a trusted balance-evidence
interval start (added 2026-09-03, slice XM-INV-ANCHOR-BALANCE, production incident)

**Gap.** `evaluatePendingBalanceEvidenceTx` (`postgresstore/consumption.go`) reconciles every real
balance checkpoint and carry-forward proof against `buildEligibilityProjectionTx`'s computed
`ExpectedBalance`. When the evidence reports *more* than expected, the code needs a trusted
interval start before it will treat the excess as an unattributed, self-healing non-cash credit
(`positive_classified_non_cash`) instead of freezing `SOURCE_GAP` — this trust query recognized
exactly two sources: a legacy account's `checkpoint_kind='cutover'` row, or an evidence item
already evaluated `matched`/`positive_classified_non_cash` earlier in time. Neither exists for a
fresh `POLICY_ANCHOR` account: its bootstrap (2.1) inserts only a `checkpoint_kind='reconciliation'`
row (which itself needs evaluating, not a separate cutover-kind anchor) and seeds no opening
credit the way a legacy `SIGNED_CUTOVER`/`POST_CUTOVER_REPLAY` bootstrap does. `account.CutoverAt`
was already the correct *lower bound* for `buildEligibilityProjectionTx`'s own window — 2.1's
"carry-forward proofs... no change needed" claim was checking that bound, not this separate trust
query — but it was never a candidate *interval start* for the evidence-evaluation trust chain.
Result: a `POLICY_ANCHOR` account's very first balance evaluation (typically its own anchor
checkpoint, reporting its full opening balance against an `ExpectedBalance` of `0`) always froze
`SOURCE_GAP`, and — since `eligibility_freezes_one_open_trigger` is unique per trigger object —
every subsequent checkpoint and carry-forward proof opened its *own* new `SOURCE_GAP` freeze too,
none of them ever reaching a trusted starting point. Measured in production (2026-09-02): account
`98cce4c8-a03c-4b55-9049-61b650db2d0e` (anchored 09-02 02:00Z) accumulated 60 `balance_checkpoint`
+ 6 `balance_carry_forward_proof` open `SOURCE_GAP` freezes; account
`6706ea6a-...` (anchored 09-02 14:49Z) accumulated 11 + 2. The one production `POLICY_ANCHOR`
account unaffected, `40bd883d-...`, has a *legacy* cutover checkpoint from before its later
`POLICY_ANCHOR` re-anchor (2.4), which happened to satisfy the query's first branch.

**This also affects design 2.4's re-anchored legacy accounts, not only 2.1's fresh bootstraps.**
Once `reanchorLegacyEligibilityAccountTx` re-anchors a legacy account to a candidate checkpoint,
that account is `bootstrap_kind='POLICY_ANCHOR'` with the identical shape a fresh bootstrap has —
its own anchor checkpoint is the same `checkpoint_kind='reconciliation'` row, still pending
evaluation, with the same missing trust source. The fix below is not scoped to "freshly bootstrapped
via 2.1"; it is scoped to `bootstrap_kind='POLICY_ANCHOR'`, covering both origins.

**Fix.** `evaluatePendingBalanceEvidenceTx`'s trusted-interval query gains a fourth `UNION ALL`
branch: for a `bootstrap_kind='POLICY_ANCHOR'` account, its own `source_account_eligibility_state.cutover_at`
is unconditionally a valid interval start (`state.cutover_at<=$2`, no source_sequence tie-break
needed — a state row is not a same-instant competing evidence row the way a checkpoint or proof
is). This plays exactly the role a legacy account's `checkpoint_kind='cutover'` row plays in the
first `UNION` branch, just sourced from the state table instead of a separate checkpoint row,
because a `POLICY_ANCHOR` bootstrap never creates one. The first time this fires (typically the
account's own anchor checkpoint), the evidence's full reported balance becomes a synthesized
`UNKNOWN_POSITIVE` non-cash credit dated exactly at `cutover_at` — the *existing*
`positive_classified_non_cash` mechanism this design's legacy accounts already use for the
symmetric case, applied here for the first time to a `POLICY_ANCHOR` account. Because
`buildEligibilityProjectionTx`'s window already starts at `account.CutoverAt` and its credit query
already includes a credit dated exactly at `cutover_at` with `credit_kind IN ('LEGACY_NON_INVOICEABLE',
'UNKNOWN_POSITIVE')` (added by 2.1/pre-existing respectively), every evaluation *after* this first
one reconciles directly against the synthesized credit with no further reliance on the new branch —
the same way a legacy account's single `checkpoint_kind='cutover'` row is superseded the moment a
later evaluation has matched. **The opening balance stays entirely non-invoiceable**: it is a
non-cash credit like any other, drained by usage before any real paid cash lot, exactly as 2.1
specifies — verified end-to-end (a real `WALLET_CASH` payment survives untouched until usage
exceeds the synthesized opening credit, then only the excess draws on it).

No migration: `UNKNOWN_POSITIVE` was already an allowed `source_credit_events.credit_kind` value
(migration 0009), added for this exact self-healing mechanism on the legacy side.

**Not a new risk for legacy accounts, and not a new blind spot for POLICY_ANCHOR accounts either.**
The new branch is scoped to `bootstrap_kind='POLICY_ANCHOR'`, so it contributes nothing to a
legacy account's query — verified with a regression test constructing a legacy account with
*no* trust source at all (no `checkpoint_kind='cutover'` row, direct SQL only, since no production
code path creates one otherwise), confirming it still freezes `SOURCE_GAP` exactly as before this
slice. The self-healing permissiveness this branch grants a `POLICY_ANCHOR` account's *first*
evaluation is identical in kind to what a legacy account's `checkpoint_kind='cutover'` row already
grants *every* evaluation before the first real match — this design's evidence-reconciliation
mechanism has always treated "no attributable gap since the last trusted point" as
self-healing rather than fail-closed; this fix extends that existing posture to `POLICY_ANCHOR`
accounts rather than introducing a new one.

**Production repair.** A second mode on the existing `eligibility-repair` CLI
(`--kind=balance-anchor`; the pre-existing `--kind=pre-anchor-usage` stays the default), store
method `RepairBalanceAnchorEligibility`, resolves the `SOURCE_GAP` freezes this gap produced for
`POLICY_ANCHOR` accounts (`trigger_object_type IN ('balance_checkpoint','balance_carry_forward_proof')`)
and deletes the stale `source_gap_frozen` `balance_checkpoint_evaluations` rows so the fixed code
re-evaluates them on the account's next projection job. `balance_carry_forward_evaluations` is
immutable by design (migration 0014) — a `balance_carry_forward_proof` freeze is still resolved,
but its own stale evaluation row is left as a permanent (and, once resolved, inert) historical
record; this does not block the account's recovery, since the trust query only ever counts
`matched`/`positive_classified_non_cash` rows, never `source_gap_frozen` ones. See
`docs/handoffs/XM-INV-ANCHOR-BALANCE.md` for the exact runbook and expected counts.

### 2.8 A single transient positive balance-evidence difference must not, by itself,
synthesize a permanent credit (added 2026-09-03, slice XM-INV-BALANCE-BLIP, production incident)

**Gap.** 2.7's fix made a trusted interval start unconditionally treat a positive difference as
self-healing (`positive_classified_non_cash`, synthesizing an `UNKNOWN_POSITIVE` credit) the
instant one was available — correct for the account's own first-ever balance evidence (2.7's own
scope), but too permissive for every evaluation after that: a single checkpoint's positive
difference could be a genuine, structural gap, or it could be a one-off timing artifact that the
very next checkpoint would show never happened. The evaluator could not tell the two apart and
always synthesized immediately, permanently inflating `buildEligibilityProjectionTx`'s expected
balance from that point forward. Measured in production (account `40bd883d-...`, sub2api's largest
customer, 2026-09-02): a checkpoint at 22:24:47Z had `as_of` exactly equal to the `event_time` of a
3,667,080-unit usage event (`source_sequence` 92410) — the upstream balance snapshot was captured
before that instant's own debit was applied, while `buildEligibilityProjectionTx`'s inclusive
`event_time<=through` window already subtracted it, producing a difference of exactly
+3,667,080. The account was firmly mid-stream (606 `matched` and 2 `positive_classified_non_cash`
evaluations earlier that same day), so this was never a first-evaluation bootstrap case — it was a
single transient blip that the evaluator nonetheless treated as a permanent credit. Every one of
the eight following checkpoints (22:25:49Z–22:33:27Z) and the 23:06Z carry-forward proof then
differed by exactly −3,667,080 (the phantom credit permanently inflating expected balance), each
freezing `UNKNOWN_NEGATIVE_BALANCE` — nine freezes opened at 23:06:09Z, on top of two genuine,
older `USAGE_EXCEEDS_LEDGER` freezes that must stay untouched by any repair. The usage event itself
was ingested 25 minutes late (source agents were recreated mid roll-forward, delaying usage
ingestion) but had already landed in `source_usage_events` well before the 23:06:09Z batch
evaluation that produced the credit — this was not a race the evaluator could dodge by waiting
longer within a single job run; the batch had the usage the whole time and still got it wrong.

**Fix.** `evaluatePendingBalanceEvidenceTx`'s positive-difference branch (case 1) gains two rules,
applied in order, before any credit is ever synthesized:
1. **Boundary rule.** If one or more usage events land within one second at or before the
   checkpoint/proof's own `as_of`, recompute the projection with exactly those usage events
   excluded (`buildEligibilityProjectionExcludingUsageTx`, a `buildEligibilityProjectionTx`
   variant used only here) and accept an exact match there as `matched` — no credit, no freeze.
   This resolves the production incident's own root cause immediately, on the very checkpoint that
   hit it, with no new evaluation status and no migration.
2. **Defer/confirm rule.** Otherwise, check whether the account already has *any* prior real
   evaluation (`matched` or `positive_classified_non_cash`) before this item
   (`balanceEvidenceTrustIntervalTx`'s new second return value, `hasPriorRealEvaluation`). If not —
   this is the account's first-ever balance evidence, exactly design XM-INV-ANCHOR-BALANCE's own
   bootstrap case — synthesize immediately as before; **this is what keeps the existing
   XM-INV-ANCHOR-BALANCE tests green**. If real prior evaluation history exists, the item is
   *deferred*: no evaluation row is written yet, and the very next item in the same ordered
   sequence either **confirms** it (an exact repeat of the same positive difference, proving a
   real, persistent gap — synthesize now, dated at the deferred item's own trust interval start,
   exactly as 2.7's mechanism already does) or **disconfirms** it (anything else — the ledger
   reconciled without the excess recurring, proving it was transient). A disconfirmed item is
   recorded with a new terminal `evaluation_status`, `'positive_blip_ignored'` (migration
   `0019_balance_blip_repair.sql`, extending both `balance_checkpoint_evaluations` and
   `balance_carry_forward_evaluations`'s CHECK constraints identically) — deliberately excluded
   from the trusted-interval query's own candidate list, so it can never itself become a later
   item's trust anchor. If the deferred item is the last one in a batch, it simply stays pending
   (no row at all) for a future evaluator run to pair with whatever real evidence arrives next —
   the same "no evaluation row yet" shape `evaluatePendingBalanceEvidenceTx`'s own pending-evidence
   query already treats as unevaluated, requiring no new bookkeeping. Exact equality (not a
   tolerance window) is required to confirm: a genuine structural gap reproduces as a *constant*
   offset regardless of what else happened in between (both sides of the comparison shift together
   by the same real activity), which is exactly the signature production showed across all eight
   poisoned checkpoints.

**Production repair.** A third mode on the existing `eligibility-repair` CLI
(`--kind=balance-blip`; `--kind=pre-anchor-usage` stays the default), store method
`RepairBalanceBlipEligibility`. Not scoped to `POLICY_ANCHOR` accounts only (unlike 2.6's and 2.7's
own repairs) — this bug lived entirely in the evaluator's own defer/boundary logic, not in any
`POLICY_ANCHOR`-specific code path, so a legacy account's blip is repaired identically. Detection:
an `UNKNOWN_POSITIVE` credit's `external_credit_id` names its own origin checkpoint/proof; the
"blip signature" is that origin having evaluated `positive_classified_non_cash` while one or more
*later* checkpoint/proof evaluations for the same account are `negative_frozen` by exactly the
negation of the credit's own amount — the precise, permanent shape only this bug produces. The
repair deletes the poisoned `balance_checkpoint_evaluations` rows (no immutability trigger, so the
fixed evaluator re-evaluates them on the next projection job, exactly as 2.7's own repair already
established) but, as with `balance_carry_forward_evaluations`, cannot delete a poisoned
`balance_carry_forward_evaluations` row (immutable) and instead only resolves its freeze. It
resolves every `UNKNOWN_NEGATIVE_BALANCE` freeze the poisoned rows opened, then deletes the
now-untrue `UNKNOWN_POSITIVE` credit itself. `source_credit_events` is otherwise unconditionally
immutable (migration 0009) — correct for every externally-sourced fact, but an `UNKNOWN_POSITIVE`
row is never external, it is the one credit_kind this system invents by inference rather than
observes. Migration `0019_balance_blip_repair.sql` therefore also replaces
`source_credit_events`'s own immutability trigger with a narrow, transaction-scoped, GUC-gated
exception (`invoice.balance_blip_repair_delete='on'`, set via `set_config(...,true)`, never
persisted) that permits `DELETE` only when `OLD.credit_kind='UNKNOWN_POSITIVE'` — mirroring
migration 0016's own guarded `POLICY_ANCHOR` re-anchor `UPDATE` exception in spirit (a narrow,
auditable carve-out for one specific repair operation, not a general weakening), every other
`credit_kind` and every other table's fact-immutability trigger untouched. The origin checkpoint's
own `positive_classified_non_cash` evaluation row is left exactly as it is (a true historical
record: at the time, that credit did explain its own difference) even after the credit backing it
is gone — the same "some historical rows become inert after a repair" posture 2.7's own repair
already established for its stuck carry-forward proof evaluations. See
`docs/handoffs/XM-INV-BALANCE-BLIP.md` for the exact runbook and expected counts.

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
