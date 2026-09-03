# XM-INV-BALANCE-BLIP: a single transient positive balance-evidence checkpoint must not synthesize a permanent credit

- **status:** fully implemented and self-tested locally — the evaluator fix, the repair tool, and
  docs all landed; full backend suite green (`go test -p 1 -count=1 ./...`), `go vet` clean,
  `go build` clean, `gofmt` clean (checked via the staged-blob method, see Gate results below),
  `gitleaks` clean.
- **branch:** `ai/claude/XM-INV-BALANCE-BLIP` (based on `ai/claude/XM-INV-AUTOLOGIN` at `840a2f7`,
  tag RC74), worktree `K:/发票/wt-XM-INV-BALANCE-BLIP`.
- **commits:** three, in dependency order (oldest first):
  1. `27357a6` `fix(eligibility): a single transient positive balance-evidence checkpoint must not
     synthesize a credit` — the evaluator fix, migration `0019`, new tests, and two corrections to
     pre-existing tests (see "Pre-existing test corrected" below).
  2. `8ecfb81` `feat(eligibility-repair): balance-blip repair mode` — the repair tool, its
     integration tests, and CLI wiring.
  3. this docs-only commit (design doc section 2.8 plus this file).

## Incident recap (production, read-only investigation by the team lead, 2026-09-02)

Design `XM-INV-ANCHOR-BALANCE` (section 2.7) fixed `evaluatePendingBalanceEvidenceTx` so a
`POLICY_ANCHOR` account's own anchor checkpoint could self-heal a positive difference into an
`UNKNOWN_POSITIVE` non-cash credit instead of freezing `SOURCE_GAP`. That fix was correct for the
account's *first-ever* balance evidence, but the same unconditional self-heal also applied to every
*later* checkpoint's positive difference — with no corroboration required, a single transient blip
looked identical to a genuine, persistent gap.

Account `40bd883d-...` (sub2api's largest customer by volume, already re-anchored to
`POLICY_ANCHOR` on 08-31 via design 2.4) hit this on 2026-09-02:

- Balance checkpoints arrive roughly once a minute; usage events carry their own `event_time`. At
  22:24:47Z a checkpoint (`balance_service_units` 31,540,281,827) had `as_of` exactly equal to the
  `event_time` of a usage event of 3,667,080 units (`source_sequence` 92410). The upstream snapshot
  was captured *before* that instant's own debit was applied on the source side, while
  `buildEligibilityProjectionTx`'s inclusive `event_time<=through` window already subtracted it —
  difference = +3,667,080.
- The evaluator (pre-fix) took the `positive_classified_non_cash` path unconditionally and
  synthesized an `UNKNOWN_POSITIVE` credit of 3,667,080 (`external_event_id`
  `unknown-positive:528cdc19-…`, dated at the account's own trusted interval start, 22:23:38Z).
  This is not a bootstrap case: the account had 606 `matched` and 2 `positive_classified_non_cash`
  evaluations earlier that same day, firmly mid-stream.
- Every later checkpoint (22:25:49Z … 22:33:27Z, eight of them) and the 23:06Z carry-forward proof
  then evaluated to exactly −3,667,080 against the now-permanently-inflated expected balance →
  `negative_frozen` → nine `UNKNOWN_NEGATIVE_BALANCE` freezes opened at 23:06:09Z. The account's
  `eligibility_status` became `frozen`; it also carries two older, genuine `USAGE_EXCEEDS_LEDGER`
  freezes that must stay untouched by any repair.
- The usage event was ingested about 25 minutes late (source agents were recreated mid
  roll-forward, delaying usage-stream ingestion), but it had already landed in
  `source_usage_events` well before the 23:06:09Z batch evaluation that produced the credit — the
  evaluator had the usage fact the whole time and still got it wrong. This was not a race a single
  job run could have dodged by waiting longer; it needed a different rule, not a later retry.

## Rule chosen, and why

The task specified three candidates: (A) defer until a following item confirms or disconfirms; (B)
a boundary rule recomputing the projection with same-instant usage excluded; (C) both. Implemented
**(C), both, applied in order** — boundary rule first, defer/confirm rule second — because neither
alone satisfies the requirement:

- **(B) alone** fully explains and would have prevented the *specific* 2026-09-02 incident (the
  usage event really was present in the database at evaluation time), but does nothing for a
  positive difference with no boundary-usage cause — the task's own wording ("a positive difference
  on ONE checkpoint must not synthesize... by itself") is a general requirement, not scoped to
  timing coincidences.
- **(A) alone** would satisfy the general requirement but unnecessarily delays the exact,
  already-diagnosed production cause by a full extra evaluation cycle, when the boundary rule can
  resolve it immediately and precisely.
- **(C)** applies the boundary rule first (a narrow, exact-match-only correction with no new
  evaluation status and no migration), and only falls through to defer/confirm when the boundary
  rule does not resolve the difference exactly to zero.

**Boundary rule.** For a checkpoint/proof with a positive difference, look for usage events landing
within `balanceEvidenceBoundaryTolerance` (one second — the balances and usage streams are captured
independently on the source side, so conceptually-simultaneous facts can carry timestamps a small
amount apart rather than bit-identical) at or before the item's own `as_of`. If any exist, recompute
the projection excluding exactly those usage rows
(`buildEligibilityProjectionExcludingUsageTx`, a `buildEligibilityProjectionTx` variant that takes
an `excludeUsageIDs` set and is otherwise byte-identical — the two existing call sites both still
pass `nil` and are unaffected). If the excluded-usage projection reconciles exactly (difference is
provably zero), the item is `matched` — no credit, no freeze, no deferral. If no boundary usage
exists, or excluding it does not reconcile exactly, fall through to the defer/confirm rule using the
original (inclusive) projection/difference, unchanged.

**Defer/confirm rule.** `balanceEvidenceTrustIntervalTx` (the existing trusted-interval-start query,
unchanged) gains a second return value, `hasPriorRealEvaluation`: whether the query's own trust
source came from an actual prior evaluation (`matched`/`positive_classified_non_cash`, the second or
third `UNION` branch) rather than only a structural bootstrap anchor (a legacy
`checkpoint_kind='cutover'` row, or design 2.7's `POLICY_ANCHOR` `state.cutover_at`, the first or
fourth branch). When `hasPriorRealEvaluation` is false, this is the account's first-ever balance
evidence — design `XM-INV-ANCHOR-BALANCE`'s own bootstrap case — and it self-heals immediately,
completely unchanged. When it is true, the item is *deferred*: no evaluation row is written this
pass. The evaluator's own item loop already processes every pending checkpoint/proof for an account
in one ordered, in-memory pass (`ORDER BY as_of,source_sequence,...`), so "the next item" is simply
the next iteration of that same loop (across separate evaluator runs when a deferred item is the
last one in a batch, since it is left with no evaluation row and is picked up again, unchanged, by
the same pending-evidence query next time). If the next item's own difference is *exactly* the same
positive value (not a tolerance window — see below), the deferred item is confirmed: synthesize now,
dated at the deferred item's own trust interval start (cached from when it was first examined,
nothing having changed in between since nothing was written for it), exactly reusing design 2.7's
existing synthesis mechanism; the confirming item's own projection is then rebuilt (not assumed
algebraically) and must reconcile to exactly zero. Any other outcome for the next item — it matches
cleanly, or shows a different value, or is itself negative — disconfirms the deferred item: recorded
`positive_blip_ignored`, a new terminal `evaluation_status` (migration `0019`, extending both
`balance_checkpoint_evaluations` and `balance_carry_forward_evaluations`'s CHECK constraints
identically), deliberately excluded from the trusted-interval query's own `IN (...)` list so it can
never itself become a later item's trust anchor.

**Exact equality, not a tolerance window, for confirmation.** The task's own candidate wording
suggested "within a small tolerance." Deliberately not implemented: these are `NUMERIC(78,0)`
integer service units with exact `big.Int` arithmetic throughout, and a genuine structural gap
reproduces as a *constant* offset regardless of how much other real activity (usage, credits)
occurs in between — both sides of the comparison (the checkpoint's own reported balance and the
ledger's own expected balance) shift by the same real amounts, so the *difference* stays exactly
constant unless the gap itself changes. This is exactly the signature production showed: eight
different checkpoints across nine minutes, each differing by precisely −3,667,080, not
approximately. A tolerance window would need an arbitrary magnitude with no support anywhere else in
this codebase's exact-integer ledger arithmetic, and would risk accepting a near-miss that is not
actually the same root cause.

## Files changed

- `backend/internal/postgresstore/consumption.go` — `buildEligibilityProjectionTx` split into a
  thin wrapper plus `buildEligibilityProjectionExcludingUsageTx` (the exclusion hook, ~20 lines);
  `evaluatePendingBalanceEvidenceTx` restructured around a `pendingBlip` one-item lookahead buffer
  and extracted `writeEvaluation`/`synthesizeUnknownPositive` closures (the loop body's shape
  changed, but every existing write path — `negative_frozen`, `source_gap_frozen`, the bootstrap
  `positive_classified_non_cash` self-heal, `matched` — is otherwise unchanged); two new functions,
  `resolveBalanceEvidenceBoundaryUsageTx` (the boundary rule) and `balanceEvidenceTrustIntervalTx`
  (the existing trust query, now returning `hasPriorRealEvaluation` too); the
  `balanceEvidenceBoundaryTolerance` constant.
- `backend/internal/postgresstore/balance_blip_integration_test.go` (new) — four integration tests
  for the evaluator fix (see Tests below).
- `backend/internal/postgresstore/balance_anchor_repair_integration_test.go` — one assertion
  corrected (see "Pre-existing test corrected" below); no other lines touched.
- `backend/internal/migrate/migrate_test.go` — `migrationMapBeforeReadinessIndex` now also excludes
  `0019_balance_blip_repair.sql` (see "Deviations found during implementation" below).
- `backend/migrations/0019_balance_blip_repair.sql` (new) — extends
  `balance_checkpoint_evaluations`/`balance_carry_forward_evaluations`'s `evaluation_status` CHECK
  with `'positive_blip_ignored'`; replaces `source_credit_events`'s immutability trigger with a
  narrow, GUC-gated exception permitting `DELETE` only for `credit_kind='UNKNOWN_POSITIVE'`.
- `backend/internal/postgresstore/balance_blip_repair.go` (new) — `RepairBalanceBlipEligibility`
  and its supporting types.
- `backend/internal/postgresstore/balance_blip_repair_integration_test.go` (new) — the repair's
  fixture (production shape, direct SQL — see Tests below) and its three tests.
- `backend/cmd/eligibility-repair/main.go` — new `--kind=balance-blip`; the existing
  `pre-anchor-usage`/`balance-anchor` code paths are otherwise byte-for-byte unchanged in behavior.
- `backend/cmd/eligibility-repair/main_test.go` — existing tests extended for the third kind
  (`TestRunApplyWithoutOperatorIDIsRejected` now loops over all three); new tests for
  `--kind=balance-blip`'s dry-run wiring and its summary formatter.
- `docs/superpowers/specs/2026-09-02-invoice-policy-anchor-design.md` — status line and new
  section `2.8`.
- `docs/handoffs/XM-INV-BALANCE-BLIP.md` — this file.

**Not touched:** `backend/Dockerfile` (this slice only adds code inside the existing
`postgresstore`/`cmd/eligibility-repair` packages; the `COPY`/build-stage lines needed no change);
`scripts/release-image-gate-lib.ps1`; `test-release-image-gate.ps1`;
`verify-release-image-artifacts.ps1`; `RELEASE-READINESS.md`; `docs/PRODUCTION-RUNBOOK.md`;
`docs/IMAGE-SCAN-REVIEW.md`; `docs/superpowers/plans/`; any RC version identity; `contracts/`; no
tags created.

## Pre-existing test corrected (not a regression — the fix exposed a latent test gap)

`TestRepairBalanceAnchorEligibilityApplyResolvesResetsAndReactivates`
(`balance_anchor_repair_integration_test.go`) builds account A with two reset checkpoints: its own
anchor (`ckptA1`, balance 500) and a second one (`ckptA2`, balance 620, ten minutes later). The
test's own post-repair assertion re-runs a real projection job and originally required *both*
checkpoints to re-evaluate to a terminal `matched`/`positive_classified_non_cash` status. Under the
fixed evaluator: `ckptA1` (the account's first-ever balance evidence) still self-heals immediately
(`positive_classified_non_cash`, credit 500) — unchanged. `ckptA2` is now evaluated in the *same*
batch pass, immediately after `ckptA1`'s own row has just been written — so
`hasPriorRealEvaluation` is now true for it, and its own difference (620 − 500 = +120) is a genuine
mid-stream positive difference with no boundary-usage cause and no confirming third checkpoint
anywhere in this fixture. It is correctly deferred: no evaluation row is written for it in this
pass. This is not a bug — it is the exact new behavior this slice implements — but it means the
original assertion ("both checkpoints have a terminal status") no longer holds for `ckptA2`
specifically. **Fix:** split the assertion in two — `ckptA1` still strictly requires a terminal
`matched`/`positive_classified_non_cash` status (unchanged, still proving design
`XM-INV-ANCHOR-BALANCE`'s own guarantee); `ckptA2` now accepts either a terminal status (`matched`,
`positive_classified_non_cash`, or the new `positive_blip_ignored`) *or* no evaluation row at all
(legitimately still pending, confirmed via `errors.Is(err, pgx.ErrNoRows)`) — both are equally valid
proof of "no new freeze, real forward progress," which is what this test's own comment says it is
checking. Verified the *unmodified* assertion fails against the fixed evaluator (confirming the fix,
not a fixture change, is what changed the outcome) before relaxing it.

## Tests

New tests (`backend/internal/postgresstore/balance_blip_integration_test.go`, all account state
built through real `ObserveBalanceCheckpoint`/`ObserveUsageEvent`/`ProcessEligibilityProjectionJobs`
calls for the fixture and the full end-to-end reproduction test; the three unit-style scenario
tests insert their own scenario-specific checkpoints/usage directly via SQL — matching the
pre-existing `TestUnknownPositiveCheckpointIsConservativelyPlacedBeforeIntervalUsage`/
`TestLegacyAccountBalanceCheckpointWithNoTrustAnchorStillFreezesSourceGap` convention — then call
`evaluatePendingBalanceEvidenceTx` directly for precise control over which items are evaluated
together in one batch):

- `TestBalanceBlipBoundaryUsageMatchesWithoutSynthesizingCredit` — a checkpoint whose `as_of`
  exactly equals a usage event's `event_time` evaluates `matched` (expected/difference 1000/0), not
  `positive_classified_non_cash`; confirms exactly one `UNKNOWN_POSITIVE` credit exists afterward
  (the fixture's own anchor, none synthesized for this checkpoint); zero open freezes.
- `TestBalanceBlipTransientPositiveIsIgnoredNotSynthesized` — a mid-stream checkpoint with a +200
  difference, followed by a checkpoint reconciling cleanly (the excess did not recur): the first is
  `positive_blip_ignored`/200, the second is `matched`/0, no credit synthesized, zero open freezes.
- `TestBalanceBlipPersistentPositiveConfirmedByNextCheckpointSynthesizesCredit` — phase 1 evaluates
  a mid-stream +200 checkpoint alone and requires it to have *no* evaluation row yet (proving
  deferral, the concrete behavior distinguishing this from the old immediate-synthesis rule — the
  first version of this test evaluated both checkpoints in one pass and passed even against the
  unfixed evaluator, since the old and new code produce identical *final* values for a genuinely
  confirmed gap; it was rewritten into two phases specifically to exercise the difference). Phase 2
  adds a confirming checkpoint with the same +200 and requires the first to become
  `positive_classified_non_cash`/1000/200, the credit to exist (200 units), and the second to become
  `matched`/0.
- `TestBalanceBlipProductionCascadeDoesNotFreezeAccount` — full end-to-end reproduction through the
  real entrypoints: three real checkpoints (the first at the exact boundary of a usage event
  ingested only afterward, matching the production timeline) evaluated together in one batch job,
  all three `matched`, exactly one `UNKNOWN_POSITIVE` credit (the fixture's anchor), account
  `active`, zero open freezes — production's own bug produced nine freezes here.

Repair tool tests (`backend/internal/postgresstore/balance_blip_repair_integration_test.go`, a
fixture built via direct SQL — real code paths can no longer produce this exact stuck shape now
that the fix is in, matching the sibling repair's own precedent):

- `TestRepairBalanceBlipEligibilityDryRunReportsWithoutMutating` — reports exact totals (1 credit,
  2 freezes, 2 checkpoint evaluations reset) without writing anything.
- `TestRepairBalanceBlipEligibilityApplyResolvesResetsAndReactivates` — three accounts in one run:
  account A (two poisoned checkpoints, no other freeze) fully recovers — credit removed, both
  checkpoint evaluations reset, both freezes resolved, reactivated — **and a real subsequent
  `ProcessEligibilityProjectionJobs` run re-evaluates both reset checkpoints `matched` with no new
  freeze**, proving genuine recovery, not merely a status flip; account B (one poisoned checkpoint
  plus one unrelated, genuinely open `USAGE_EXCEEDS_LEDGER` freeze) has its poisoned checkpoint
  resolved but stays frozen because the unrelated freeze remains; account C (a real unrelated
  `SOURCE_GAP` freeze, no blip credit at all) is completely untouched and never appears in the
  repair's own account list; a second apply run is idempotent (finds nothing left).
- `TestRepairBalanceBlipEligibilityApplyRequiresOperatorAndEvidence` — the same apply-mode guard as
  both sibling tools.

CLI tests (`backend/cmd/eligibility-repair/main_test.go`):
`TestRunBalanceBlipDryRunAgainstEmptyDatabaseReportsNothing` (new);
`TestRunApplyWithoutOperatorIDIsRejected` now loops over all three kinds;
`TestPrintBalanceBlipSummaryFormatsAccountsAndTotals` (new).

**Deliberately not separately tested:** the repair's handling of a poisoned
`balance_carry_forward_evaluations` row (immutable, freeze-only-resolved, credit-remains-untouched
path) is exercised by code review and by the sibling `XM-INV-ANCHOR-BALANCE` repair's own equivalent,
already-tested logic for the identical table/trigger — not by a dedicated fixture in this slice's
own test file. Constructing a real `balance_carry_forward_proofs` row that also satisfies migration
0014's `enforce_balance_carry_forward_proof_contract` trigger requires the full scan-cycle/proof
derivation pipeline (`ensureBalanceCarryForwardProofTx`); given time budget, the checkpoint-kind
path (the dominant shape in the actual incident — eight checkpoints to one proof) was prioritized.
Flagged here explicitly as a real coverage gap, not silently omitted.

## Gate results

From `backend/`, with the eight proxy variables unset (`HTTP_PROXY`, `HTTPS_PROXY`, `http_proxy`,
`https_proxy`, `ALL_PROXY`, `all_proxy`, `NO_PROXY`, `no_proxy`) and
`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_blip?sslmode=disable`
(a dedicated database created for this task in the already-running `invoice-test-pg` container,
separate from other agents' databases; never `invoice_test_release`):

```
go build ./...                 # exit 0
go vet ./...                   # exit 0
go test -p 1 -count=1 ./...    # ok, all packages (see below)
```

Full package list, all `ok` (two complete runs of the full suite, both green — the second after the
sibling-test correction landed): `cmd/api`, `cmd/archive-verify` (no tests), `cmd/backup-verify` (no
tests), `cmd/bootstrap-settings`, `cmd/bootstrap-sources`, `cmd/document-gc` (no tests),
`cmd/eligibility-repair` (this slice's new tests included), `cmd/identity-migrate`, `cmd/keygen`,
`cmd/migrate`, `cmd/mtlsgen`, `cmd/oidc-logout-retention` (no tests), `cmd/oidc-preflight`,
`cmd/pdf-policy-check`, `cmd/pdf-scanner` (no tests), `internal/adminsettings`,
`internal/application`, `internal/auth` (passed cleanly both full-suite runs — no isolated rerun
needed, no flake observed this session), `internal/backuparchive`, `internal/backupverify`,
`internal/document`, `internal/domain`, `internal/httpapi`, `internal/ledger`, `internal/mailer`,
`internal/migrate` (this slice's fixture-helper fix included), `internal/oidcretention`,
`internal/pdfscanner`, `internal/postgresstore` (this slice's new tests included; ~100s per run),
`internal/securefields`, `internal/sourceingest`, `internal/testdb`.

`"$(go env GOROOT)/bin/gofmt" -l` against the **staged git blob** of every file this slice touches
or adds (`git show ":<path>"`, which passes through the `core.autocrlf=true` clean filter and
therefore matches what a commit actually contains — the working-tree files themselves report
false-positive diffs under plain `gofmt -l` purely from CRLF, per this machine's documented
quirk): clean on every file after one real fix (a map-literal alignment `gofmt -w` would have caught
in `balance_blip_repair.go`, corrected before committing).

`gitleaks git --log-opts="840a2f7..HEAD" .`: clean.

## Not run

- `scripts/verify.ps1` in full — spans frontend, Docker release-image gates, and
  Keycloak/Nginx verification, none of which this backend-only slice touches. Its backend-relevant
  lines are covered in spirit by the gates above, run without `-race` (not requested for this task).
- Anything requiring a server/production connection — explicitly out of scope, matching both
  sibling slices' own precedent.
- Rehearsal against a restored production backup — recommended before running the production repair
  procedure below for real, per the design doc's own standing verification-gate item.

## Risks / things to sign off on

1. **Migration number is `0019`, not the `0017` the task brief named.** `0017_session_display_name.sql`
   and `0018_console_assertion_nonces.sql` already existed on this branch's base commit (from other
   concurrent work) by the time this task started — the brief's own number was written before those
   landed. Used the next actually-available number instead of the one literally specified.
2. **`source_credit_events` is unconditionally immutable, not anticipated in the task's literal
   repair description** ("delete that synthesized credit row" was written as if this were as simple
   as the sibling repair's checkpoint-evaluation deletes). Discovered mid-implementation: without
   removing the credit, the repair would be functionally inert — re-evaluating the poisoned
   checkpoints under the fixed code would immediately reproduce the identical wrong negative
   difference, since the phantom credit's ledger effect never goes away on its own. Resolved with a
   migration adding a narrow, GUC-gated `DELETE` exception scoped to exactly
   `credit_kind='UNKNOWN_POSITIVE'` (mirroring migration 0016's own precedent for a comparable
   problem), verified by proving the naive "just delete the downstream rows" approach does not work
   (traced through the arithmetic by hand before writing any repair code) rather than assumed.
3. **`consumption_allocations` rows referencing the removed credit are deleted directly** (a
   surgical `WHERE credit_event_id=$1`, not the account-wide bulk delete `reprojectEligibilityTx`
   itself uses) purely to satisfy the foreign key before the credit's own `DELETE` — the next
   projection job this repair queues rebuilds the rest of the account's allocations from scratch
   regardless, exactly as it always does.
4. **The proof-kind (immutable `balance_carry_forward_evaluations`) repair path has no dedicated
   integration test** — see "Deliberately not separately tested" above. The code path mirrors the
   already-tested sibling repair's equivalent logic closely, but this is a real coverage gap, not a
   claim of exhaustive verification.
5. **The exact production counts (1 credit, 9 freezes: 8 checkpoint + 1 carry-forward, for account
   `40bd883d-...`) are a snapshot from the team lead's 2026-09-02 investigation**, not re-measured by
   this task (no production access). The account's `USAGE_EXCEEDS_LEDGER` freezes (two, older) are
   explicitly unrelated and must remain untouched — confirmed by the repair's own scoping (it only
   ever selects `UNKNOWN_NEGATIVE_BALANCE` freezes whose trigger object correlates to a detected
   blip-signature credit).
6. **`RepairBalanceBlipEligibility` was not run against a restored production backup** — no
   server/production access for this task. See "Not run" above.
7. No new float amounts, no logged/persisted secrets, no `contracts/` changes, no admin-OIDC
   changes, no touch to any release-identity file, no tags created, no touch to any file outside
   this slice's stated scope — checked.

## Follow-ups (recommended, not blocking this delivery)

1. **Roll-forward restarts of the source agents delay usage ingestion** (observed ~25 minutes in
   this incident) — the team lead's brief itself flagged this: consider whether checkpoints should
   only be evaluated up to the usage stream's own watermark minus a safety delay, so a checkpoint
   whose window could still receive a late-arriving usage fact is not evaluated at all until that
   window has definitely closed. This slice's boundary rule handles the *coincident-timestamp* case
   specifically; a watermark-based safety delay would be a complementary, more general mitigation
   for the underlying ingestion-delay hazard, not a replacement for it.
2. Consider a dedicated integration test for the repair's proof-kind (immutable
   `balance_carry_forward_evaluations`) path, built through the real `ensureBalanceCarryForwardProofTx`
   derivation pipeline (matching `TestPolicyAnchorAccountCarryForwardProofEvaluatesWithoutSourceGap`'s
   own construction) with a manually inserted poisoned evaluation row layered on top.
3. Wire `eligibility-repair --kind=balance-blip` into whatever operational tooling/alerting already
   exists for the other maintenance binaries in the `tools` image — this task added the `--kind`
   value only, no deployment/invocation automation beyond that, matching both sibling slices' own
   precedent.

## Production repair procedure

**Do not run this anywhere but the test database from this task** unless/until explicitly directed
to run it against production. Intended production sequence, mirroring both sibling repairs' own
runbook shape:

1. Fresh backup first, per the standing release/ops discipline.
2. Deploy the release containing this slice's fix (`consumption.go`'s boundary and defer/confirm
   rules) — this must be live *before* running the repair, so that once the poisoned checkpoint
   evaluations are reset and the credit removed, the corrected code is what actually re-evaluates the
   account on its next projection job, not the still-buggy one.
3. `eligibility-repair --database-url-file=... --field-keyring-file=... --migrations-dir=...
   --kind=balance-blip` (no `--apply`, i.e. dry run — `--kind` defaults to `pre-anchor-usage`, so it
   must be passed explicitly here). Expect **1 account (`40bd883d-...`), 1 blip credit removed, 9
   negative freezes resolved, 8 checkpoint evaluations reset** (2026-09-02 snapshot — the account's
   real state may have shifted since; treat as order of magnitude, re-check the actual dry-run
   output). Read the per-account table before proceeding; confirm no unexpected account appears and
   the totals are in the expected range. The two older `USAGE_EXCEEDS_LEDGER` freezes on this account
   must **not** appear anywhere in this repair's own accounting — if they do, stop and investigate
   before proceeding.
4. Get the dry-run output reviewed/approved by whoever is authorizing the production change.
5. `eligibility-repair ... --kind=balance-blip --apply --operator-id=99ed401b-e78a-4883-b9bf-f4cb4ba1cf17`
   only after owner approval. Confirm the summary table matches the dry run's counts. The account
   should show `REACTIVATED=false` (the two unrelated `USAGE_EXCEEDS_LEDGER` freezes remain open) —
   if it shows `true`, something unexpected resolved those freezes too and needs separate
   investigation before continuing.
6. Re-run the dry run once more (no `--apply`, `--kind=balance-blip`): expect `accounts affected: 0`
   for this specific bug signature (the two `USAGE_EXCEEDS_LEDGER` freezes are a separate, known,
   pre-existing condition this repair never touches and does not resolve).
7. Confirm the account's `eligibility_status` and its checkpoint evaluations directly:
   `SELECT eligibility_status FROM source_account_eligibility_state WHERE external_account_id=
   '40bd883d-...'` (expect `frozen`, due to the two remaining `USAGE_EXCEEDS_LEDGER` freezes — this
   is expected and correct, not a sign the repair failed); then, separately, confirm the eight
   previously-poisoned checkpoints and the one carry-forward proof re-evaluate cleanly (`matched` or
   `positive_classified_non_cash`, no new `UNKNOWN_NEGATIVE_BALANCE` freeze) once the account's next
   projection job runs — the repair queues that job automatically whenever it clears every open
   freeze; here it will not, since the unrelated freezes remain, so this account's next
   forward-progress check depends on whatever separate process resolves those two freezes.
