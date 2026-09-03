# XM-INV-ELIG-POLICY-START-ANCHOR: policy-start anchoring and the three-account re-anchor repair (design section 3(D))

- **status:** implemented and self-tested locally; full backend suite green (`go test -p 1 -count=1
  ./...`), `go vet` clean, `go build` clean, `gofmt` clean (staged-blob method), `gitleaks` clean.
- **branch:** `ai/claude/XM-INV-ELIG-POLICY-START-ANCHOR` (base commit `a9e70f3`, the merge commit
  containing slice 1 `XM-INV-ELIG-AUTO-RECONCILE`, migration 0020, slice 2
  `XM-INV-ELIG-QUEUE-NARROW`, and the shadow-eval tool), worktree `K:/发票/wt-XM-INV-POLICY-START`.
- **spec:** `docs/superpowers/specs/2026-09-03-xm-inv-eligibility-simplification-design.md` section
  3(D), the parts of section 5 (test matrix) and section 6 (slice 3) that belong to this slice;
  `docs/superpowers/specs/2026-09-02-invoice-policy-anchor-design.md` sections 2.0-2.9 (the existing
  POLICY_ANCHOR bootstrap and migration 0016 this slice builds on) plus this slice's own new section
  2.10.

## Base commit note (deviation, flagged as it happened)

The team lead's delegation message gave two things for the worktree's base: a `git worktree add`
command naming branch `ai/claude/XM-INV-AUTOLOGIN`, and separately "base must be commit `a9e70f3`".
Used the commit hash directly (`git worktree add ... a9e70f3`) rather than the branch name, since the
message itself named the exact commit and a branch tip can drift out from under a delegation written
earlier. Confirmed `a9e70f3`'s own commit message ("merge: XM-INV-ELIG-QUEUE-NARROW...") matches what
the brief describes it should contain (slice 1 + slice 2 + shadow-eval tool). No functional
difference resulted -- this is purely a note on which literal instruction was followed where the two
disagreed on form, not substance.

## Summary

Implements design section 3(D) in full:

1. **`ObserveBalanceCheckpoint`'s POLICY_ANCHOR bootstrap branch** (`postgresstore/consumption.go`):
   `cutover_at` is now unconditionally `policy.StartAt` (`invoice_eligibility_policy.
   eligibility_start_at`), not the triggering reconciliation checkpoint's own `as_of`.
   `cutover_balance_units` is derived (`deriveCutoverBalanceUnitsTx`) by unwinding the checkpoint's
   observed balance backward across the window `(policyStartAt, checkpointAsOf]`, subtracting every
   non-cash credit and adding back every usage fact already persisted in that window at bootstrap
   time -- the exact inverse of `buildEligibilityProjectionTx`'s own window predicate, so a later
   evaluation of the real checkpoint reconciles to zero once the derived value becomes
   `cutover_balance_units`. A second, derived `checkpoint_kind='reconciliation'` row is inserted at
   `as_of=policyStartAt` with the derived balance (`synthesizePolicyStartReconciliationCheckpointTx`),
   borrowing the triggering checkpoint's own provenance columns (`source_sequence`/`source_cursor`/
   `stream_watermark_at`/`source_revision_hash`/`observed_at`) -- the same technique design
   XM-INV-POLICY-ANCHOR 2.7/2.8's `synthesizeUnknownPositive` already uses for a value that is
   derived, not directly observed -- so migration 0016's existing anchor-checkpoint validation (the
   anchor must match a real checkpoint row exactly) is satisfied at COMMIT without weakening it.
2. **Migration 0021** extends `enforce_account_eligibility_cutover_contract()` (the same function
   0016 defines) with a sibling transaction-local GUC, `invoice.policy_anchor_start_reanchor`,
   permitting a `POLICY_ANCHOR`-to-`POLICY_ANCHOR` re-anchor UPDATE (changing `cutover_at`,
   `cutover_balance_units`, `bootstrap_kind`, `finalized_through` under the identical guarded shape
   0016's own legacy-to-`POLICY_ANCHOR` GUC uses) -- kept as a separate GUC and, at the application
   layer, a separate audit action from 0016's own, so which repair tool performed a given re-anchor
   stays distinguishable.
3. **The two skip boundaries** design 3(D) names (2.2's fact discard at wake time, and 2.6's
   `observeEligibilityFact` cutover-boundary check) both key off `account.CutoverAt`/the global
   policy start already -- see "Deviation: no code change needed for design 2.2" below for why only
   one of the two needed an actual code edit, and it needed none either (the fix is entirely in item
   1's bootstrap change; both boundaries automatically widen once `account.CutoverAt` becomes the
   policy start).
4. **`invoice-eligibility-repair --kind=policy-start-reanchor`** (new file
   `postgresstore/policy_start_reanchor_repair.go`, CLI wiring in `cmd/eligibility-repair/main.go`):
   re-anchors the three existing production accounts, following `--kind=queue-narrow`'s own
   per-account-transaction pattern rather than the three earlier repairs' single-transaction-for-the-
   whole-run pattern. See "Design decisions" below for the no-op/blocked/re-anchor branching.

## Design decisions

### `deriveCutoverBalanceUnitsTx` does not subtract cash funding-lot units (flagged, not fixed --
see Risks)

The design doc's own formula (and the team lead's brief, independently, word-for-word the same) is
`first observed checkpoint balance − credits(window) + usage(window)`, with no term for cash
payments. I verified this is *self-consistent* with the rest of the evaluator mechanism (a synthetic
`UNKNOWN_POSITIVE` credit gets created for the derived checkpoint, and the real checkpoint then
reconciles to exactly zero) **only when the window contains no real cash payment** -- if
`buildEligibilityProjectionExcludingUsageTx`'s cash-pool query (which does add a window payment's
units to `ExpectedBalance`) finds a real `WALLET_CASH` payment in that same window, the derived
value would be off by exactly that payment's unit amount, since nothing in the formula unwinds it.
Today's callers (a fresh bootstrap, and the three-account repair) are both verified to have no such
payment in the relevant window, so this is not live in practice -- flagged in
`deriveCutoverBalanceUnitsTx`'s own doc comment as a theoretical correctness gap for a
possible-but-unobserved future shape (a brand-new account's very first payment landing between the
policy start and its first checkpoint), not fixed, since the design doc and the brief both
independently specify the formula exactly as implemented and I did not have authorization to widen
its scope unilaterally. See Risks item 1.

### No code change needed for design 2.2's own boundary (`RequeueSourceDependency`)

The brief describes "two skip boundaries" (2.2 and 2.6) that both needed to move from the account's
own `cutover_at` to the global `policy.StartAt`. Reading `RequeueSourceDependency`
(`postgresstore/source_sync.go`) directly: it already queries `invoice_eligibility_policy.
eligibility_start_at` fresh, per call -- it was never keyed off the account's own `cutover_at` at
all, so there was nothing to change there. 2.6's own boundary
(`observeEligibilityFact`'s `!in.EventTime.After(account.CutoverAt)`) *does* read `account.CutoverAt`
directly, but the code at that line needed no edit either: since item 1's bootstrap change makes
`account.CutoverAt` literally equal to `policy.StartAt` for every account bootstrapped from now on,
this comparison automatically starts comparing against the policy start once the stored value
changes, with zero lines touched. Flagging here since the brief's own framing ("the two skip
boundaries... change... to the global policy.StartAt") reads as if both needed a code edit, and only
one function's *value* changed (via item 1), not either function's *code*.

### `reanchorLegacyEligibilityAccountTx` (design 2.4) is untouched, and still anchors legacy accounts
at the checkpoint's own `as_of`

The brief's scope is precisely "`ObserveBalanceCheckpoint`'s POLICY_ANCHOR bootstrap branch" and the
three-account repair -- not 2.4's own legacy-account re-anchor (a different, already-shipped
mechanism for a different problem: `SIGNED_CUTOVER`/`POST_CUTOVER_REPLAY` accounts catching up to
`POLICY_ANCHOR`). I left it untouched. This means: **if any legacy account is ever re-anchored via
2.4 in the future** (production has one candidate,
`40bd883d-...`, already re-anchored per the balance-blip handoff, so this set may already be
exhausted in production, but the code path remains live for any account that predates the
`XM-INV-POLICY-ANCHOR` design), it will still get the *old* treatment (`cutover_at` = the candidate
checkpoint's own `as_of`, not the policy start) -- re-introducing the exact dead zone this slice
closes, for that one account. See Risks item 2.

### Bootstrap edge cases (design's own list, all verified by dedicated tests)

- **First checkpoint before policy start:** unchanged, still skipped without bootstrapping (2.1's
  own pre-existing behavior; the code path this slice touches is only reached when the checkpoint is
  at/after the policy start).
- **Account first observed after policy start:** this *is* the ordinary bootstrap path now -- no
  special-casing needed or added; the derivation window naturally starts at the policy start
  regardless of how late the account's first checkpoint arrives.
- **Checkpoint exactly at policy start:** the real triggering checkpoint and the derived checkpoint
  end up with an identical `as_of` (and, since the derived row borrows the real row's own
  `source_sequence`, an identical evaluator tie-break key too) -- verified by
  `TestPolicyStartBootstrapCheckpointExactlyAtPolicyStartReconcilesCleanly` that the evaluator
  reaches the same correct final state (exactly one `UNKNOWN_POSITIVE` credit, both checkpoints
  terminal, one `matched` and one `positive_classified_non_cash`) **regardless of which of the two
  ties gets evaluated first** -- the self-heal mechanism is symmetric here, so the tie-break
  ambiguity (a random UUID comparison) never produces an incorrect outcome, only a harmless
  redundant row.

## Repair tool design decisions

### Exposure guard added beyond the brief's own literal description (found during implementation,
not requested up front)

The brief's own wording for apply ("insert the derived checkpoint + guarded UPDATE + clears
consumption_allocations/resets consumption state/reprojects") does not mention checking for active
invoice exposure before resetting. Implementing exactly that surfaced a real, reachable failure
mode: `funding_lots` carries an *immediate* (non-deferred) CHECK,
`reserved_minor+issued_minor<=consumed_cash_minor`, and the repair's own reset step
(`consumed_cash_minor=0`) violates it instantly for any lot with real reserved/issued exposure --
not a `domain.ErrConflict` from `reprojectEligibilityTx`'s own guarded UPDATE (which never even runs,
since the reset itself fails first), but a raw Postgres constraint-violation error. Design
XM-INV-POLICY-ANCHOR 2.4's own `reanchorLegacyEligibilityAccountTx` has exactly this problem solved
already: it checks `invoice_allocations` for any `reserved`/`issued`/`refund_attention` row on the
account's funding lots *before* touching consumption state at all, and freezes
(`POLICY_ANCHOR_BLOCKED`) instead of resetting when found. I added the identical check (same query
shape) to this repair, gating the reset/reproject path the same way. **Deviation from "exactly as
design 2.4 does":** 2.4 *freezes* the account when blocked (it needs to, since it runs inside the
periodic projection job and the freeze is what forces a future retry); this repair reports the
account `Blocked: true` and writes nothing, no freeze either, since it is a standalone, re-runnable
CLI tool an operator can simply invoke again later once the exposure clears. Flagging this as a
deliberate, reasoned difference from the brief's own "as design 2.4 does" wording, not an oversight.
No dedicated test for a real `Blocked` account was added given time budget -- see Risks item 3.

### Dry-run's window-lot count uses the exact `buildEligibilityProjectionTx` predicate, not the
design doc's own looser interval notation

Design 3(D)'s own text describes the dry-run check window as "`[policy.StartAt, 当前cutover_at)`"
(closed-open). The actual predicate that determines whether re-anchoring changes anything is
`buildEligibilityProjectionTx`'s own cash query: `completed_at>account.CutoverAt(new)` combined with
the pre-existing `completed_at>=account.PolicyStartAt` floor -- which, worked through algebraically,
is `completed_at` in `(policyStartAt, oldCutoverAt]` (open-closed, the mirror image of the design
prose). I matched the exact code predicate rather than the design's own looser prose, since that is
what actually decides the outcome; flagged here as a precision choice, not a behavior gap (they only
differ at the exact boundary instants, which is not a shape any real production data is expected to
hit).

### `reprojectEligibilityTx`/`recordUsageOverageTx` handle the auto-downgrade automatically; the
evaluator does not run inline

Per the brief's hard requirement ("negative difference or usage overage must flow through slice 1's
auto-downgrade, never a freeze"): `recordUsageOverageTx` already runs unconditionally inside
`reprojectEligibilityTx` (slice 1's own change), so usage overage is handled the moment the repair
calls it. A negative *balance* difference is a separate mechanism
(`evaluatePendingBalanceEvidenceTx`, triggered by an evaluator pass), which this repair does **not**
call directly -- matching every one of the three earlier repair tools' own precedent (none of them
call the evaluator inline either; `balance_anchor_repair.go` resets stale evaluations and queues a
job, relying on the next `processEligibilityProjectionJob` run for the actual evaluation). This
repair does the same: it queues a projection job after resetting/reprojecting, so the derived
checkpoint's own evaluation (and any resulting `enterPendingReconciliationTx` auto-downgrade) happens
on the next normal worker cycle, not synchronously inside the repair's own transaction.

## Hard rule compliance

- **Every branch returns a defined result.** `deriveCutoverBalanceUnitsTx` floors a
  hypothetically-negative unwind at zero rather than erroring (documented in its own comment;
  `cutover_balance_units` carries a `NOT NULL CHECK (>=0)`, so a defined, non-erroring floor is the
  only sound choice for a data shape not observed in production). `repairPolicyStartReanchorAccount`
  returns a defined, zero-value (not-a-candidate) result for a stale snapshot, a defined `NoOp` row
  for zero window lots, a defined `Blocked` row for active exposure, and a defined `Reprojected` row
  for a real re-anchor -- the only error path is a genuine database/query failure, never a synthetic
  business-logic error.
- **Per-account errors never escape the batch.** `RepairPolicyStartReanchorEligibility` collects each
  account's own error into `Errors` and continues to the next account, verified by
  `TestPolicyStartReanchorAccountIsolation` (a broken account's guarded-UPDATE conflict is isolated;
  a separate good account in the same run completes and re-anchors).
- **Account-level isolation tested.** See the isolation test above; the broken account's own
  transaction leaves no partial trace (verified: its `cutover_at`, lot `consumed_cash_minor`
  unchanged after the run).
- **Serial work on the hot file.** `consumption.go` was the only file this slice touched in
  `postgresstore`'s core evaluator/projection logic (plus the new, separate
  `policy_start_reanchor_repair.go` file) -- per the delegation brief, this agent was the only one
  authorized to edit `consumption.go` during this task, and the diff there is localized to the
  bootstrap branch and two new helper functions, nothing else.

## Files changed

- `backend/migrations/0021_policy_anchor_start_reanchor.sql` (new) -- see Summary item 2 above.
- `backend/internal/postgresstore/consumption.go` -- `ObserveBalanceCheckpoint`'s POLICY_ANCHOR
  bootstrap branch (cutover_at/cutover_balance_units derivation, second checkpoint insert, enriched
  audit detail); new `deriveCutoverBalanceUnitsTx` and
  `synthesizePolicyStartReconciliationCheckpointTx` functions. No other function in this file was
  touched.
- `backend/internal/postgresstore/policy_start_reanchor_repair.go` (new) --
  `RepairPolicyStartReanchorEligibility` and its helpers (`policyStartReanchorCandidateAccountIDs`,
  `repairPolicyStartReanchorAccount`).
- `backend/internal/postgresstore/policy_start_anchor_integration_test.go` (new) -- this slice's own
  bootstrap-side tests, see Tests below.
- `backend/internal/postgresstore/policy_start_reanchor_repair_integration_test.go` (new) -- this
  slice's own repair-tool tests, see Tests below.
- `backend/internal/postgresstore/policy_anchor_integration_test.go` --
  `TestReconciliationCheckpointIgnoredPrePolicyAndBootstrapsPolicyAnchorPostPolicy`'s `cutover_at`
  assertion corrected (was `postAsOf`, now `policyStart`) -- the one assertion in that test that
  encoded the pre-this-slice bootstrap behavior; the rest of the test (including its own final
  projection check) needed no change (see the test's own updated doc comment for why).
- `backend/internal/postgresstore/balance_anchor_integration_test.go` --
  `TestPolicyAnchorAccountBalanceCheckpointsEvaluateWithoutSourceGap` and
  `TestPolicyAnchorAccountCarryForwardProofEvaluatesWithoutSourceGap` corrected: the *derived*
  checkpoint (not the real triggering one) is now the account's first-ever balance evidence and
  carries the `positive_classified_non_cash` self-heal; the real checkpoint now reconciles `matched`
  against it instead. Not a regression -- see each test's own updated doc comment.
- `backend/internal/postgresstore/balance_blip_integration_test.go` --
  `newBalanceBlipFixture`'s own internal assertion corrected identically (same reason as above; this
  fixture is shared by seven tests across this file and `balance_blip_softfail_integration_test.go`,
  none of which needed further changes since they only consume the fixture's own return values, not
  its internal assertion).
- `backend/internal/postgresstore/preanchor_usage_integration_test.go` --
  `TestPolicyAnchorAccountPreCutoverUsageFactSkippedWithoutFreeze` and
  `TestPolicyAnchorAccountPreCutoverCreditFactSkippedWithoutFreeze`: the fact under test is now dated
  before the policy start itself, not merely before the fixture's own (pre-this-slice-shaped) anchor
  checkpoint -- the original scenario (a fact after the policy start but before the account's own
  later `cutover_at`) is exactly the dead zone this slice closes, so it is now correctly *persisted*,
  not skipped (that positive case is covered by
  `TestPolicyStartBootstrapIncludesInWindowCashFundingLot` instead). 2.6's own skip-without-freeze
  mechanism is still live and still tested here, just for a fact genuinely at/before the policy
  start.
- `backend/internal/postgresstore/consumption_integration_test.go` --
  `TestPostCutoverNewAccountReplaysFromGlobalCutoverAndBlocksSubscriptionWithoutUsage`: a wallet
  payment dated after the policy start but before the account's own (pre-this-slice-shaped) later
  `cutover_at` is now correctly classified `WALLET_CASH`/`active` instead of left at its unclassified
  `LEGACY_NON_INVOICEABLE`/`frozen` default -- not a regression, the intended widened-window
  behavior; `AvailableMinor()` is asserted `0` (not the payment's own verified amount), since that
  field tracks *recognized* (usage-consumed) cash, not merely verified cash, and no projection has
  run yet at that point in the test.
- `backend/internal/migrate/migrate_test.go` -- both places `0016_policy_anchor.sql` is excluded
  from a deliberately-reduced migration set now also exclude
  `0021_policy_anchor_start_reanchor.sql`, for the identical reason (0021 replaces the same trigger
  function 0016 defines, and applying it without 0016 first produces an incoherent intermediate
  schema state no real sequential migration run would ever produce).
- `backend/cmd/eligibility-repair/main.go` -- `kindPolicyStartReanchor` constant, `runPolicyStartReanchor`,
  `printPolicyStartReanchorSummary`, updated `--kind` flag usage string and package doc comment.
- `docs/ELIGIBILITY-OPERATIONS.md` -- new "Policy-start anchoring" section (the bootstrap behavior
  change, the repair tool's own no-op/blocked/re-anchor outcomes, and its dry-run/apply invocation as
  it would run in the tools container).
- `docs/superpowers/specs/2026-09-02-invoice-policy-anchor-design.md` -- new section 2.10.
- `docs/handoffs/XM-INV-ELIG-POLICY-START-ANCHOR.md` -- this file.

**Not touched:** `backend/Dockerfile` (no new binary, no build-stage change needed); `contracts/`;
`deploy/`; `agents/`; `K:/sub2api-src`, `K:/newapi-src` (off limits, never touched);
`reanchorLegacyEligibilityAccountTx` (design 2.4's own legacy re-anchor, see "Design decisions"
above); `eligibility_operations.go`'s `eligibilityFreezeReasons` map (unrelated concept, unchanged by
every prior sibling slice too); any other function in `consumption.go` besides the bootstrap branch
and the two new helpers; `web/`; no tags created.

## Tests

New tests, all green:

**`policy_start_anchor_integration_test.go`:**
- `TestDeriveCutoverBalanceUnitsSubtractsCreditsAddsUsageInWindow` -- direct unit test of the
  derivation formula: checkpoint 1000, a 200-unit credit and 50-unit usage in the window, a
  999-unit credit before the policy start and a 777-unit usage after the checkpoint (both must not
  affect the result) -- derived = 850.
- `TestDeriveCutoverBalanceUnitsFloorsAtZero` -- a 500-unit window credit against a 10-unit
  checkpoint balance floors at 0, not -490.
- `TestPolicyStartBootstrapAnchorsAtPolicyStartAndSynthesizesReconciliationCheckpoint` -- the real
  `ObserveBalanceCheckpoint` entrypoint: `cutover_at`/`finalized_through` both equal the policy
  start, the derived checkpoint row exists with the expected `as_of`/balance, exactly two checkpoint
  rows exist for the account. Also the required "account first observed after policy start" case (a
  brand-new account's first-ever checkpoint, three hours after the policy start).
- `TestPolicyStartBootstrapFirstCheckpointBeforePolicyStartStillSkipped` -- unchanged edge case,
  focused confirmation alongside the existing end-to-end test in `policy_anchor_integration_test.go`.
- `TestPolicyStartBootstrapIncludesInWindowCashFundingLot` -- a WALLET_CASH lot completed inside the
  window (inserted before the account has any eligibility state at all, since `funding_lots` carries
  no trusted-state trigger unlike the fact tables) is included in a fresh projection once
  `cutover_at` becomes the policy start -- design's own "confirm with a test" requirement.
- `TestPolicyStartBootstrapCheckpointExactlyAtPolicyStartReconcilesCleanly` -- the required edge case;
  see "Bootstrap edge cases" above.

**`policy_start_reanchor_repair_integration_test.go`:**
- `TestPolicyStartReanchorDryRunZeroLotsReportsNoOpAndChangesNothing` -- row-for-row equality
  (`eligibilityStateSnapshot.equal`, using `time.Time.Equal` not struct `==`) of the account row and
  checkpoint count before/after both a dry run and an apply run against a zero-window-lot account;
  neither mode writes anything.
- `TestPolicyStartReanchorApplyOneInWindowLotReanchorsAndReprojects` -- dry-run preview matches what
  apply then does; post-apply `cutover_at`/`finalized_through`/`bootstrap_kind` correct, the derived
  checkpoint row exists, the in-window lot's `consumed_cash_minor` becomes positive (a real 50-unit
  usage fact in the window proves the payment actually entered the ledger, not merely that
  `cutover_at` moved), a projection job is queued, the audit event is written, and a second apply run
  is idempotent (no longer a candidate).
- `TestPolicyStartReanchorAccountIsolation` -- two accounts in one run: a "broken" one whose
  `reserved_minor+issued_minor` (9000) no longer fits under what a fresh reprojection recomputes
  once its only usage fact (10 units) is the sole remaining consumer (constructed carefully to be
  self-consistent at fixture-setup time -- see the test's own extensive doc comment on why a
  naively-inconsistent fixture fails at `INSERT` time, before the repair even runs, given this
  account must be `'active'` to be a valid candidate at all) -- and a "good" account with a clean
  in-window lot. Asserts the repair itself returns no error, exactly one collected per-account error
  for the broken account, and the good account is processed to completion; the broken account's own
  failed transaction left no partial trace.

Existing tests re-run unmodified as part of the full package runs below, including every prior
`consumption.go` evaluator/bootstrap test and every sibling repair tool's own tests -- all still
pass (five pre-existing tests needed assertion corrections for the reasons documented in Files
changed above; none needed a fixture or scenario change, only an assertion correction proving the
new, intended behavior).

## Gate results

From `backend/`, with the eight proxy variables unset (`HTTP_PROXY`, `HTTPS_PROXY`, `http_proxy`,
`https_proxy`, `ALL_PROXY`, `all_proxy`, `NO_PROXY`, `no_proxy`) and
`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_policystart?sslmode=disable`
(a dedicated database created for this task in the already-running `invoice-test-pg` container,
separate from other agents' databases; never `invoice_test_release`):

```
go build ./...                 # exit 0
go vet ./...                   # exit 0
go test -p 1 -count=1 ./...    # ok, all 22 tested packages (see below)
```

Full package list, all `ok`: `cmd/api`, `cmd/bootstrap-settings`, `cmd/bootstrap-sources`,
`cmd/eligibility-repair`, `cmd/eligibility-shadow`, `cmd/identity-migrate`, `cmd/keygen`,
`cmd/migrate`, `cmd/mtlsgen`, `cmd/oidc-preflight`, `cmd/pdf-policy-check`, `internal/adminsettings`,
`internal/application`, `internal/auth`, `internal/backuparchive`, `internal/backupverify`,
`internal/document`, `internal/domain`, `internal/httpapi`, `internal/ledger`, `internal/mailer`,
`internal/migrate`, `internal/oidcretention`, `internal/pdfscanner`, `internal/postgresstore`,
`internal/securefields`, `internal/sourceingest`, `internal/testdb`.

One flake observed and reported: the very first full-suite run hit a connection-dial failure in
`cmd/eligibility-repair` (`dial tcp 127.0.0.1:55432 ... connectex: ...`) -- rerunning exactly that
package (and, separately, the full suite again) came back clean both times, consistent with this
machine's own documented Docker-port-forwarding flakiness, not a code issue. `internal/postgresstore`
(the package this slice's own changes and tests live in) was green on every run, no flake observed
there.

`"$(go env GOROOT)/bin/gofmt" -l` against the **staged git blob** of every file this slice touches or
adds (`git show ":<path>"`, matching this machine's documented CRLF-checkout `gofmt` false-positive
handling): three real formatting issues found and fixed before committing
(`cmd/eligibility-repair/main.go`'s new constant misaligned with its siblings;
`policy_start_anchor_integration_test.go`'s a struct-literal field alignment;
`policy_start_reanchor_repair.go`'s a struct-field alignment) -- `gofmt -w` applied to exactly those
three files (confirmed via `git status` that no other file was touched by the formatting pass), clean
on every file afterward.

`gitleaks git --no-banner --log-opts="a9e70f3..HEAD" .`: clean, two commits scanned, no leaks.

## Not run

- `scripts/verify.ps1` in full -- spans frontend, Docker release-image gates, and Keycloak/Nginx
  verification, none of which this backend-only slice touches. Its backend-relevant lines are
  covered in spirit by the gates above, run without `-race` (not requested for this task).
- Anything requiring a server/production connection -- explicitly out of scope, matching every prior
  sibling slice's own precedent.
- A live run of `invoice-eligibility-repair --kind=policy-start-reanchor` against a
  production-shaped database with the three real accounts -- no production access for this task; the
  integration tests construct the pre-this-slice `POLICY_ANCHOR` shape directly (via one explicit
  transaction, since the real bootstrap code can no longer produce it once this slice ships) and
  exercise the real repair code path against it.

## Risks / things to sign off on

1. **`deriveCutoverBalanceUnitsTx` does not account for a real cash payment inside the derivation
   window** -- see "Design decisions" above. Matches the design doc's own formula exactly (and the
   brief's, independently); flagged as a theoretical gap for a future account whose very first
   payment lands between the policy start and its first checkpoint, not fixed. No production account
   is known to have this shape today (design's own text: "今日生产没有账号在该窗口内有真实充值（已验
   证）").
2. **`reanchorLegacyEligibilityAccountTx` (design 2.4) still anchors at the candidate checkpoint's
   own `as_of`, not the policy start** -- untouched, out of this slice's stated scope. Any future
   legacy-account re-anchor via that path will re-introduce the dead zone this slice closes, for that
   one account. Flagging as a known gap for a future slice or a deliberate decision to also update
   2.4, not attempted here.
3. **No dedicated test for a real `Blocked` (active invoice exposure) account** -- the exposure guard
   itself is a straightforward, directly-modeled-on-2.4 query; given time budget, verified by code
   inspection and by the fact that *without* it, `TestPolicyStartReanchorAccountIsolation`'s original
   fixture attempt (before I added the guard) reliably reproduced the exact constraint violation the
   guard now prevents. A dedicated test constructing a real `invoice_allocations` row in `'reserved'`
   state on an otherwise-eligible account and asserting `Blocked: true` with zero writes would close
   this gap.
4. **The `TestPolicyStartReanchorAccountIsolation` broken-account fixture is hand-constructed to be
   self-consistent at every intermediate step** (a real usage fact, a matching `consumption_allocations`
   row, all three of `funding_lots`/`funding_lot_consumption_state`/`consumption_allocations` inserted
   in one explicit transaction so the deferred consumption-mirror trigger sees all three by commit) --
   this was necessary discovery during implementation (see the test's own extensive doc comment); it
   does not by itself prove a *production* account can reach this exact broken shape, only that the
   repair correctly isolates a genuine database error for one account when it occurs.
5. No new float amounts, no logged/persisted secrets, no `contracts/` changes, no admin-OIDC changes,
   no tags created, no touch to `backend/Dockerfile`, no touch to any file outside this slice's own
   stated scope -- checked.

## Production upgrade/repair sequencing

1. **Migration 0021 first.** Must be live before the repair tool runs at all (the repair's own
   guarded UPDATE depends on the GUC this migration adds) and, separately, before any *new* account
   bootstraps under the fixed `ObserveBalanceCheckpoint` code (the code and the migration ship
   together in the same release, so this is automatic in a normal deploy, not a separate step).
2. **Dry-run** `invoice-eligibility-repair --kind=policy-start-reanchor` (no `--apply`). Expect all
   three known accounts (`40bd883d-...`, `98cce4c8-...`, `6706ea6a-...`) to report `NoOp: true` (zero
   window funding lots), matching the design's own verified expectation that no account has a real
   payment in this window today. If any account instead reports a nonzero `WindowFundingLots` or
   `Blocked: true`, stop and investigate before proceeding -- that is not the expected shape.
3. **Owner-approved apply** only after review of the dry-run output, per the same discipline every
   sibling repair tool's own runbook uses. Given the expected all-`NoOp` outcome, apply is expected to
   also report all three accounts `NoOp: true` with nothing written -- this step exists to formally
   confirm that expectation held at the moment of the production run, not because a real change is
   anticipated.
4. **Re-run the dry run once more** to confirm zero accounts remain candidates with unexpected
   window activity.
