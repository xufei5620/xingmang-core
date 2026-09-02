# XM-INV-ANCHOR-BALANCE: a POLICY_ANCHOR account's own anchor checkpoint must be a trusted balance-evidence interval start

- **status:** fully implemented and self-tested locally — the fix, the repair tool, and docs all
  landed; full backend suite green (`go test -p 1 -count=1 ./...`), `go vet` clean, `go build`
  clean.
- **branch:** `ai/claude/XM-INV-ANCHOR-BALANCE` (based on `ai/claude/XM-INV-AUTOLOGIN` at
  `cadf009`, tag RC72), worktree `K:/发票/wt-XM-INV-ANCHOR-BALANCE`.
- **commits:** three, in dependency order (oldest first) — see each commit's own message for the
  exact file list:
  1. `fix(eligibility): trust POLICY_ANCHOR origin when evaluating balance evidence` (the
     projection-query fix, its new tests, and the correction of two pre-existing tests that were
     silently relying on the bug — see "Pre-existing tests corrected" below)
  2. `feat(eligibility-repair): balance-anchor repair mode`
  3. docs-only commit (this file plus the design doc's new section 2.7)

## Incident recap (production, read-only investigation by the team lead, 2026-09-02)

Design `XM-INV-POLICY-ANCHOR` 2.1 bootstraps a fresh account directly to `bootstrap_kind=
'POLICY_ANCHOR'` from the first post-policy-start reconciliation checkpoint, instead of waiting
for a legacy signed cutover row. That bootstrap inserts a `source_account_eligibility_state` row
and a `checkpoint_kind='reconciliation'` checkpoint — but, unlike the legacy `SIGNED_CUTOVER`
bootstrap path, seeds no opening credit and no `checkpoint_kind='cutover'` row.

`evaluatePendingBalanceEvidenceTx` reconciles every real checkpoint/carry-forward proof against
`buildEligibilityProjectionTx`'s `ExpectedBalance`. When the evidence reports more than expected
(a positive difference), it looks for a trusted interval start before treating the excess as a
self-healing, unattributed non-cash credit (`positive_classified_non_cash`); with none, it freezes
`SOURCE_GAP` instead. The trust query recognized only two sources — a legacy account's
`checkpoint_kind='cutover'` row, or an earlier evidence item already evaluated `matched`/
`positive_classified_non_cash`. A fresh `POLICY_ANCHOR` account has neither: its own anchor
checkpoint (`ExpectedBalance=0` against its full reported opening balance) is always the *first*
evidence evaluated, so it always froze `SOURCE_GAP` — and because
`eligibility_freezes_one_open_trigger` is unique per trigger object, every checkpoint and
carry-forward proof after it opened its *own* fresh `SOURCE_GAP` freeze too, none ever reaching a
trusted starting point.

Measured on production (2026-09-02, read-only, by the team lead before this task began):
account `98cce4c8-a03c-4b55-9049-61b650db2d0e` (anchored 09-02 02:00Z) — 60 `balance_checkpoint` +
6 `balance_carry_forward_proof` open `SOURCE_GAP` freezes; account `6706ea6a-...` (anchored 09-02
14:49Z) — 11 + 2. Checkpoints arrive roughly once a minute while the upstream account is active,
so both counts were still growing. Account `40bd883d-...` (a legacy cutover account re-anchored to
`POLICY_ANCHOR` on 08-31 via design 2.4) was unaffected only because its pre-existing legacy
`checkpoint_kind='cutover'` row happened to satisfy the trust query's first branch.

**This is a design gap distinct from `XM-INV-PREANCHOR-USAGE`** (which fixed
`observeEligibilityFact`'s *usage/credit* fact path for `POLICY_ANCHOR` accounts). This slice fixes
the *balance-evidence* reconciliation path, a different function entirely
(`evaluatePendingBalanceEvidenceTx`) with a different freeze surface
(`balance_checkpoint`/`balance_carry_forward_proof`, not `usage`/`credit`).

**Also affects design 2.4's re-anchored legacy accounts, not only fresh 2.1 bootstraps.** Once
`reanchorLegacyEligibilityAccountTx` re-anchors a legacy account, it is `bootstrap_kind=
'POLICY_ANCHOR'` with the identical shape a fresh bootstrap has — the same missing trust source
applies to its own anchor checkpoint. Confirmed directly: two pre-existing tests
(`TestMissingUsageIsCaughtByLowerBalanceCheckpointWithoutIncreasingEligibility` and
`TestPostCutoverNewAccountReplaysFromGlobalCutoverAndBlocksSubscriptionWithoutUsage`) were, on the
unmodified base commit, silently exercising this exact bug via an incidental design-2.4 re-anchor
or a genuine 2.1 bootstrap respectively — see "Pre-existing tests corrected" below.

## Mechanism chosen, and why

Two candidates were on the table:

**(A, chosen) Trust `account.CutoverAt` in the evaluator's own query.** Add a fourth `UNION ALL`
branch to `evaluatePendingBalanceEvidenceTx`'s trusted-interval query: for a `bootstrap_kind=
'POLICY_ANCHOR'` account, `source_account_eligibility_state.cutover_at` is unconditionally a valid
interval start. The first time this fires, the evidence's full reported balance becomes a
synthesized `UNKNOWN_POSITIVE` non-cash credit dated exactly at `cutover_at` — reusing the
*existing* `positive_classified_non_cash` self-healing mechanism this design's legacy accounts
already use for the symmetric case (a stale `checkpoint_kind='cutover'` row plus a mid-stream gap),
applied for the first time to a `POLICY_ANCHOR` account.

**(B, rejected) Seed the opening balance as a credit at bootstrap time**, mirroring the legacy
`LEGACY_NON_INVOICEABLE` credit `ObserveBalanceCheckpoint`'s `SIGNED_CUTOVER` branch inserts.

**Why (A):** smallest blast radius, no migration, no new `credit_kind`, and zero behavior change
for legacy accounts (verified — see "Tests" below). `buildEligibilityProjectionTx`'s window already
starts at `account.CutoverAt`, and its credit query already includes a credit dated exactly at
`cutover_at` with `credit_kind IN ('LEGACY_NON_INVOICEABLE','UNKNOWN_POSITIVE')` — so once the
synthesized credit exists, every *subsequent* evaluation reconciles directly against it with no
further reliance on the new branch (the same way a legacy account's single `checkpoint_kind=
'cutover'` row is superseded the instant a later evaluation has matched). (B) would have made
`ExpectedBalance` include the opening amount deterministically from the first evaluation, but
requires a new migration (an eager-seeded credit needs a `credit_kind` that is honest about being a
`POLICY_ANCHOR` opening balance, not a repurposed `LEGACY_NON_INVOICEABLE`) and does not reuse
already-covered code — the self-healing mechanism (A) reuses was proven correct on the legacy side
already. `UNKNOWN_POSITIVE` was already an allowed `source_credit_events.credit_kind` value
(migration 0009); no migration needed either way for (A).

**The opening balance stays entirely non-invoiceable** under (A), matching design 2.1's own
"entirely non-invoiceable" requirement: `UNKNOWN_POSITIVE`, like every credit kind, sits in
`buildEligibilityProjectionTx`'s non-cash pool, drained by usage strictly before any real paid cash
lot. Verified end-to-end in
`TestPolicyAnchorAccountBalanceCheckpointsEvaluateWithoutSourceGap` (see Tests below): a real
`WALLET_CASH` payment survives with `consumed_cash_minor=0` until usage exceeds the synthesized
100-unit non-cash credit, at which point only the excess (120 of 700 units) draws on it.

## Files changed

- `backend/internal/postgresstore/consumption.go` — `evaluatePendingBalanceEvidenceTx`'s
  trusted-interval query, new `UNION ALL` branch (the fix itself, ~25 lines including the doc
  comment explaining the mechanism and its scope).
- `backend/internal/postgresstore/balance_anchor_integration_test.go` (new) — three integration
  tests for the fix (see Tests below).
- `backend/internal/postgresstore/consumption_integration_test.go` — two pre-existing tests
  corrected (see "Pre-existing tests corrected" below); no other lines touched.
- `backend/internal/postgresstore/balance_anchor_repair.go` (new) — `RepairBalanceAnchorEligibility`
  and its supporting types.
- `backend/internal/postgresstore/balance_anchor_repair_integration_test.go` (new) — the repair's
  fixture (production shape) and its four tests.
- `backend/cmd/eligibility-repair/main.go` — new `--kind` flag (`pre-anchor-usage`, the default, or
  `balance-anchor`); the existing pre-anchor-usage code path is otherwise byte-for-byte unchanged
  in behavior, just refactored into its own function alongside the new one.
- `backend/cmd/eligibility-repair/main_test.go` — existing tests updated for `run`'s new `kind`
  parameter (passing `kindPreAnchorUsage` explicitly, unchanged behavior); new tests for
  `--kind=balance-anchor`, an unknown `--kind`, and the balance-anchor summary formatter.
- `docs/superpowers/specs/2026-09-02-invoice-policy-anchor-design.md` — status line and new section
  `2.7`.
- `docs/handoffs/XM-INV-ANCHOR-BALANCE.md` — this file.

**Not touched:** any migration file (none needed — see "Mechanism chosen" above);
`backend/Dockerfile` (the existing `invoice-eligibility-repair` binary already builds from
`./cmd/eligibility-repair`; this slice only adds code inside that existing package, so the
`COPY`/build-stage lines needed no change — the required `/out/invoice-oidc-preflight
/usr/local/bin/` substring is unmodified); `scripts/release-image-gate-lib.ps1`;
`test-release-image-gate.ps1`; `verify-release-image-artifacts.ps1`; `RELEASE-READINESS.md`;
`docs/PRODUCTION-RUNBOOK.md`; `docs/IMAGE-SCAN-REVIEW.md`; `docs/superpowers/plans/`; any RC version
identity; `contracts/`; no tags created.

## Pre-existing tests corrected (not a regression — the fix exposed a latent test gap)

Two pre-existing tests in `consumption_integration_test.go` passed on the unmodified base commit
but were, on inspection, silently relying on this exact bug — not testing what their names claim.
Both use the fix's own new self-healing path once it exists; their assertions needed updating to
the mathematically correct post-fix values, not merely "whatever makes it pass":

1. **`TestMissingUsageIsCaughtByLowerBalanceCheckpointWithoutIncreasingEligibility`.** This test's
   account is `integrationStore`'s shared default (bootstrap_kind `SIGNED_CUTOVER`, from
   `UpsertFundingLot`'s fixture bootstrap path — which, unlike the real `ObserveBalanceCheckpoint`
   cutover path, seeds no `checkpoint_kind='cutover'` row). The checkpoint the test inserts (dated
   at/after the policy start) is indistinguishable, to `reanchorLegacyEligibilityAccountTx`'s
   candidate lookup, from a design-2.4 re-anchor candidate. Going through the real job queue
   silently re-anchors the account to that checkpoint (`bootstrap_kind` flips to `POLICY_ANCHOR`,
   confirmed via the `eligibility.policy_anchor.migrated` audit action) *before* the checkpoint is
   evaluated as balance evidence — replacing the negative-mismatch scenario the test intends
   (checkpoint reports 90, ledger expects 100 → `UNKNOWN_NEGATIVE_BALANCE`) with a POLICY_ANCHOR
   evaluation of the *same* checkpoint against its own now-empty window (`ExpectedBalance=0`
   against 90 → positive difference). On the base commit this still froze the account (via the
   bug's `SOURCE_GAP`, not the negative-balance detection the test's name and comment describe) —
   the test's loose assertion (`EligibilityStatus != "frozen"`) didn't distinguish freeze *reason*,
   so it passed for the wrong reason. **Fix:** use `processEligibilityWithoutReanchor` (an
   existing helper in `store_integration_test.go`, already used elsewhere in this file for exactly
   this fixture/design-2.4 interaction) instead of `store.ProcessEligibilityProjectionJobs`,
   restoring the test's original intent — a stable `SIGNED_CUTOVER` boundary, no re-anchor. Passes
   for the right reason now.
2. **`TestPostCutoverNewAccountReplaysFromGlobalCutoverAndBlocksSubscriptionWithoutUsage`.** A
   genuine, direct `POLICY_ANCHOR` bootstrap (not an incidental re-anchor). Its final checkpoint
   (hardcoded `balance_service_units="50"`) and its final assertion
   (`postPaymentLot.ConsumedCashMinor==100_000`, i.e. 100 units of usage fully drawn from the real
   paid `postPayment` lot) were both computed under the bug's actual (incorrect) behavior — the
   account's own 100-unit opening balance never became an available non-cash credit, so usage had
   nothing else to draw from. Under the fix, that 100-unit opening balance *is* now a real,
   trusted non-cash credit and is drained by the 100-unit usage fact *before* it ever reaches
   `postPayment`'s cash — the correct, design-2.1-intended outcome (`ConsumedCashMinor=0`), and the
   only value that reconciles against a hand-picked checkpoint balance at all (recomputed:
   `ExpectedBalance` at that point is `100` — 0 remaining non-cash + 100 untouched cash — not `50`).
   **Fix:** corrected the checkpoint's hardcoded balance from `50` to `100` (the true reconciling
   value) and the final assertion from `100_000` to `0`, with comments explaining why each changed.

Both corrections were verified by first confirming the *unmodified* test failed with the *old*
assertion once the fix (consumption.go) was applied — proving the fix, not a fixture change,
caused the difference — then updating each assertion to the value independently derived from the
fixed code's own arithmetic (not just "whatever the code now outputs").

## Tests

New tests (all in `backend/internal/postgresstore`, all built through the real
`ObserveBalanceCheckpoint`/`ObserveCreditEvent`/`ObserveUsageEvent`/`ProcessEligibilityProjectionJobs`
calls, not hand-crafted SQL state, except where noted):

- `TestPolicyAnchorAccountBalanceCheckpointsEvaluateWithoutSourceGap` — a fresh `POLICY_ANCHOR`
  account's own anchor checkpoint evaluates `positive_classified_non_cash` (not `SOURCE_GAP`); a
  second real checkpoint reflecting a post-anchor credit evaluates `matched`; a real `WALLET_CASH`
  payment plus usage sized to spill past the non-cash pool proves the opening balance stays
  non-invoiceable (`consumed_cash_minor` reflects only the 120-unit spillover of 700 units of
  usage, not the full amount); a third checkpoint after that also evaluates `matched`; zero open
  freezes throughout.
- `TestPolicyAnchorAccountCarryForwardProofEvaluatesWithoutSourceGap` — design 2.1's own claim
  ("carry-forward proofs anchor at `account.CutoverAt`... no change needed") exercised for real: a
  net-zero credit/usage pair covered by an empty published balances cycle (no real checkpoint)
  derives a carry-forward proof via `ensureBalanceCarryForwardProofTx`, which evaluates `matched`
  once the account's own anchor checkpoint (evaluated first, in the same pass) has self-healed.
- `TestLegacyAccountBalanceCheckpointWithNoTrustAnchorStillFreezesSourceGap` — regression guard: a
  legacy account constructed with genuinely no trust source at all (direct SQL, no
  `checkpoint_kind='cutover'` row — not producible via any real code path post-`XM-INV-POLICY-ANCHOR`,
  but the precise edge case that proves the new branch's `bootstrap_kind='POLICY_ANCHOR'` scoping
  is what excludes legacy accounts, not an accidental side effect) still freezes `SOURCE_GAP`
  exactly as before this slice.

Repair tool tests (`backend/internal/postgresstore/balance_anchor_repair_integration_test.go`, a
fixture reproducing the production shape via direct SQL for the "stuck" evaluation/freeze rows —
real code paths cannot reproduce them anymore now that the fix is in — layered onto real
`ObserveBalanceCheckpoint`/`ensureBalanceCarryForwardProofTx`-derived checkpoint/proof rows so
every FK and contract-guard trigger is satisfied exactly as production requires):

- `TestRepairBalanceAnchorEligibilityDryRunReportsWithoutMutating` — reports exact totals
  (freezes resolved, checkpoint evaluations reset) without writing anything.
- `TestRepairBalanceAnchorEligibilityApplyResolvesResetsAndReactivates` — the full happy path in
  one fixture: account A (two stuck checkpoint freezes including its own anchor, one stuck
  carry-forward-proof freeze) fully resolves, its two checkpoint evaluations reset (deleted), its
  proof's evaluation is deliberately left untouched (immutable — see Risks), and it reactivates;
  account B (one stuck checkpoint freeze plus one unrelated, genuinely open
  `USAGE_EXCEEDS_LEDGER` freeze) has its checkpoint freeze resolved but **stays frozen** because
  the unrelated freeze remains open; account C (legacy, a real unrelated `SOURCE_GAP` freeze) is
  completely untouched; a second apply run is idempotent (finds nothing left); **and, crucially, a
  real subsequent `ProcessEligibilityProjectionJobs` run on the reactivated account A completes
  cleanly** — both reset checkpoints re-evaluate `matched`/`positive_classified_non_cash` with no
  new freeze, proving the repair leaves the account able to make real forward progress, not merely
  flips a status flag.
- `TestRepairBalanceAnchorEligibilityApplyRequiresOperatorAndEvidence` — the same apply-mode guard
  as the sibling tool.

CLI tests (`backend/cmd/eligibility-repair/main_test.go`): `TestRunDryRunAgainstEmptyDatabaseReportsNothing`
now passes `kindPreAnchorUsage` explicitly (proving the default/existing path is unchanged);
`TestRunBalanceAnchorDryRunAgainstEmptyDatabaseReportsNothing` (new); `TestRunUnknownKindIsRejected`
(new); `TestRunApplyWithoutOperatorIDIsRejected` now loops over both kinds;
`TestPrintBalanceAnchorSummaryFormatsAccountsAndTotals` (new).

## Gate results

From `backend/`, with the eight proxy variables unset (`HTTP_PROXY`, `HTTPS_PROXY`, `http_proxy`,
`https_proxy`, `ALL_PROXY`, `all_proxy`, `NO_PROXY`, `no_proxy` — required on this machine for any
test using `httptest.NewTLSServer`, unrelated to this slice) and
`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_anchorbal?sslmode=disable`
(a dedicated database created for this task in the already-running `invoice-test-pg` container,
separate from other agents' databases; never `invoice_test_release`):

```
go build ./...                 # exit 0
go vet ./...                   # exit 0
go test -p 1 -count=1 ./...    # ok, all packages, see below
```

Full package list, all `ok` (two complete runs of the full suite, both green — the second after
the repair tool and CLI changes landed): `cmd/api`, `cmd/archive-verify` (no tests),
`cmd/backup-verify` (no tests), `cmd/bootstrap-settings`, `cmd/bootstrap-sources`,
`cmd/document-gc` (no tests), `cmd/eligibility-repair` (this slice's new tests included),
`cmd/identity-migrate`, `cmd/keygen`, `cmd/migrate`, `cmd/mtlsgen`, `cmd/oidc-logout-retention`
(no tests), `cmd/oidc-preflight`, `cmd/pdf-policy-check`, `cmd/pdf-scanner` (no tests),
`internal/adminsettings`, `internal/application`, `internal/auth` (passed cleanly both full-suite
runs — no isolated rerun needed, no flake observed this session), `internal/backuparchive`,
`internal/backupverify`, `internal/document`, `internal/domain`, `internal/httpapi`,
`internal/ledger`, `internal/mailer`, `internal/migrate`, `internal/oidcretention`,
`internal/pdfscanner`, `internal/postgresstore` (this slice's new tests included; ~88s per run),
`internal/securefields`, `internal/sourceingest`, `internal/testdb`.

`"$(go env GOROOT)/bin/gofmt" -d <file>` (diff mode, per this machine's documented CRLF false-alarm
mode — see `windows-toolchain-quirks` — checked on every file this slice touches or adds, with
pre-existing files' CRLF stripped first via `sed 's/\r$//'` before diffing so only real formatting
differences show): clean on every file. New files were formatted directly with `gofmt -w` (safe —
written by the editing tools as LF-native, so no CRLF conversion risk).

`gitleaks git --log-opts="cadf009..HEAD" .`: <fill in after commits are made — see note below>.

## Not run

- `scripts/verify.ps1` in full — it spans frontend (`npm test`/`typecheck`/`build`/`audit`), Docker
  release-image gates, backup/restore capacity checks, and Keycloak/Nginx verification, none of
  which this backend-only slice touches. Its backend-relevant lines (`go test -race ./...` and
  `go vet ./...` from `backend/`) are covered in spirit by the gates above, run without `-race`
  (not requested for this task); this slice adds no new goroutines or shared mutable state beyond
  ordinary `pgx` transaction usage already exercised elsewhere in the same functions.
- Anything requiring a server/production connection — explicitly out of scope for this task,
  matching the sibling `XM-INV-PREANCHOR-USAGE` slice's own precedent.
- Rehearsal against a restored production backup — the design doc's existing verification-gate
  item 3 (from `XM-INV-POLICY-ANCHOR` itself) already flags this as a pre-production-rollout
  follow-up; this fix and its repair tool should be exercised the same way before the production
  runbook below is executed for real.

## Risks / things to sign off on

1. **`balance_carry_forward_evaluations` is immutable (migration 0014's `BEFORE UPDATE OR DELETE`
   trigger, no exception) — discovered during implementation, not anticipated in the task
   description's "deletes the source_gap_frozen evaluation rows... so they are re-evaluated."**
   That instruction is fully implemented for `balance_checkpoint` triggers
   (`balance_checkpoint_evaluations` carries no such trigger). For `balance_carry_forward_proof`
   triggers, the repair resolves the freeze but cannot delete the stale evaluation row; it is left
   as a permanent historical record. Verified this does not block the account's recovery — the
   trust query only ever counts `matched`/`positive_classified_non_cash` evaluations, never
   `source_gap_frozen` ones, and (see Tests above) a real subsequent projection job on a repaired
   account completes cleanly regardless. Flagged here explicitly since it is a deviation from the
   task's literal wording, driven by a schema constraint discovered mid-implementation, not a
   judgment call made in advance.
2. **Team-lead brief specified code comments in Chinese with full-width punctuation "like the
   surrounding code."** The actual surrounding code in every file this slice touches
   (`consumption.go`, every `_test.go` in `internal/postgresstore`, `eligibility_repair.go`,
   `cmd/eligibility-repair`) is 100% English with zero CJK characters (verified via a full-package
   grep before writing anything) — the "surrounding code" convention is English, not Chinese. All
   comments in this slice are written in English to match. Flagged here per the task's own
   "surrounding code" framing, in case Chinese was intended as a hard requirement independent of
   what the touched files actually look like.
3. **The exact production `SOURCE_GAP` freeze counts (66 for `98cce4c8...`, 13 for `6706ea6a...`)
   are a snapshot from the team lead's 2026-09-02 investigation**, not re-measured by this task
   (no production access). Treat as expected order of magnitude, not a guaranteed exact match —
   production's real state may have shifted (further checkpoints have very likely arrived since,
   growing both counts; an operator's manual intervention is less likely but possible).
4. **`RepairBalanceAnchorEligibility` was not run against a restored production backup** — no
   server/production access for this task. See "Not run" above.
5. No new float amounts, no logged/persisted secrets, no `contracts/` changes, no admin-OIDC
   changes, no touch to any release-identity file, no tags created, no migration added, no touch
   to any file outside this slice's stated scope — checked.

## Follow-ups (recommended, not blocking this delivery)

1. Consider whether `balance_carry_forward_proofs`/`balance_carry_forward_evaluations`'s
   immutability should gain the same kind of narrow, GUC-gated escape hatch design 2.4's guarded
   re-anchor UPDATE uses (migration 0016), specifically for this class of repair — would let a
   future repair actually re-evaluate a stuck carry-forward proof instead of leaving its record
   permanently `source_gap_frozen`. Not needed for this incident (the account recovers fully
   without it — verified), but would tidy the historical reconciliation record for the affected
   proofs.
2. The production repair runbook below assumes `98cce4c8...`/`6706ea6a...`'s counts are still
   growing (checkpoints arrive ~once/minute). Consider whether the fix (this slice, once deployed)
   should be released *before* running the repair, so the count stops growing before the repair's
   dry-run snapshot is taken — mirroring `XM-INV-PREANCHOR-USAGE`'s own runbook ordering (deploy
   the fix, then repair).
3. Wire `eligibility-repair --kind=balance-anchor` into whatever operational tooling/alerting
   already exists for the other maintenance binaries in the `tools` image — this task added the
   `--kind` flag only, no deployment/invocation automation beyond that (matching the sibling
   slice's own precedent).

## Production repair procedure

**Do not run this anywhere but the test database from this task** unless/until explicitly directed
to run it against production. The following is the intended production sequence for whoever runs
it later, once this branch is released, mirroring `XM-INV-PREANCHOR-USAGE`'s own runbook shape:

1. Fresh backup first, per the standing release/ops discipline.
2. Deploy the release containing this slice's fix (`consumption.go`'s new `UNION` branch) — this
   must be live *before* running the repair, so that once freezes clear and checkpoint evaluations
   reset, the corrected code is what actually re-evaluates the account on its next projection job,
   not the still-buggy one.
3. `eligibility-repair --database-url-file=... --field-keyring-file=... --migrations-dir=...
   --kind=balance-anchor` (no `--apply`, i.e. dry run — `--kind` defaults to `pre-anchor-usage`,
   so it must be passed explicitly here). Expect roughly **66 SOURCE_GAP freezes resolved / up to
   ~60 checkpoint evaluations reset for account `98cce4c8-a03c-4b55-9049-61b650db2d0e`, and 13 /
   up to ~11 for account `6706ea6a-...`** (2026-09-02 snapshot counts — see Risks item 3 above:
   treat as order of magnitude, re-check the actual dry-run output, since both counts were still
   growing as of that snapshot). Read the per-account table before proceeding; confirm no
   unexpected account appears and totals are in the expected range.
4. Get the dry-run output reviewed/approved by whoever is authorizing the production change.
5. `eligibility-repair ... --kind=balance-anchor --apply --operator-id=99ed401b-e78a-4883-b9bf-f4cb4ba1cf17`
   only after owner approval. Confirm the summary table matches the dry run's counts. An account
   with no other open freeze should show `REACTIVATED=true`; one that still shows `false` has
   another, unrelated open freeze and needs separate investigation (not expected for these two
   accounts based on the 2026-09-02 snapshot, but the tool does not assume it away — see the
   `TestRepairBalanceAnchorEligibilityApplyResolvesResetsAndReactivates` account-B scenario for
   exactly this case).
6. Re-run the dry run once more (no `--apply`, `--kind=balance-anchor`): expect
   `accounts affected: 0` (barring new checkpoints that arrived and froze again in the interim,
   which the next scheduled repair run — or a manual one — would catch).
7. Confirm both accounts are `eligibility_status='active'` and their next projection job (queued
   automatically by the repair's own reactivation step) completes without opening a new freeze —
   `SELECT eligibility_status FROM source_account_eligibility_state WHERE external_account_id IN
   ('98cce4c8-a03c-4b55-9049-61b650db2d0e', '6706ea6a-...')`, then re-check after the next
   projection-worker cycle.
