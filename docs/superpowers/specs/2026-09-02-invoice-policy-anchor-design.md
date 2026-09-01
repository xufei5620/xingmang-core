# XM-INV-POLICY-ANCHOR — 记账锚定到开票政策日

**Status:** DESIGN (accepted by owner 2026-09-02; implementation dispatched)
**Owner decision:** 开票只针对 2026-09-01 00:00 (Asia/Shanghai) 之后的真实充值；此前流水对开票无用。
**Replaces:** the idea of re-running `cutover-init` — rejected because `CaptureCutover`
snapshots the live database at run time and cannot produce a historical (9/1) baseline.

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

### 2.5 Cycle completeness vs failed/dead events (postgresstore/source_sync.go)
`tryPublishEconomicScanCyclesTx` treats `failed` and `dead` events like `parked_identity`
(not holding the cycle) **only when** the account was frozen for that event (the processor
already freezes on payload drift / conflict). Pure transient failures (retry budget not yet
exhausted) still hold the cycle. A `dead` event without a freeze freezes the account with
reason `EVENT_DEAD` so the ledger stays honest.

## 3. Verification gates (all must pass before production)
1. Unit + integration suites (full `go test -p 1 ./...` with INVOICE_TEST_DATABASE_URL).
2. New integration tests: policy-anchor bootstrap for a baseline member; pre-policy skip at wake;
   re-anchor migration of a SIGNED_CUTOVER account; failed-event cycle publication with freeze.
3. **Rehearsal on an isolated restore** of production backup `invoice-20260901T091600Z`
   (deploy/backup/restore-drill.sh): run the new image against it, wake sub2api user 34, and
   record: anchor checkpoint chosen, cutover balance, released vs skipped counts, projection
   wall time, resulting `consumed_cash_minor` for lot 3347 (order 3347, 500分) and the pre-policy
   legacy lots' display state. Compare against a manual expectation before signing off.
4. Production ceremony as usual (fresh backup, signed release, cutover order incl.
   `restart api → restart ingest-proxy`).

## 4. Out of scope
Chunked/set-based replay (XM-INV-REPLAY-PERF phase 2), username persistence, embed link params.
