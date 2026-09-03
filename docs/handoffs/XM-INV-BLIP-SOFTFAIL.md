# XM-INV-BLIP-SOFTFAIL: a blip confirmation that does not reconcile must never fail the batch

- **status:** fully implemented and self-tested locally — the evaluator fix, three new integration
  tests (one direct reproduction, one rebaseline-cap escalation, one batch-isolation check), and
  docs all landed; full backend suite green (`go test -p 1 -count=1 ./...`), `go vet` clean,
  `go build` clean, `gofmt` clean (staged-blob method, see Gate results below), `gitleaks` clean.
- **branch:** `ai/claude/XM-INV-BLIP-SOFTFAIL` (based on `ai/claude/XM-INV-AUTOLOGIN` at `578b6b2`,
  which already contains XM-INV-BALANCE-BLIP and XM-INV-READY-LEASE), worktree
  `K:/发票/wt-XM-INV-BLIP-SOFTFAIL`.
- **commits:** two, in dependency order (oldest first):
  1. `2ef613b` `fix(eligibility): a blip confirmation that does not reconcile must softfail, not
     error` — the evaluator fix and its three new integration tests.
  2. this commit (docs-only): design doc section 2.9 plus this file.

## Incident recap (production, RC75 = `37636ca`, 2026-09-03)

Account `98cce4c8-...` (`POLICY_ANCHOR`, already re-anchored, but carrying 66 historical
`source_gap_frozen` checkpoint evaluations from *before* design XM-INV-ANCHOR-BALANCE landed — the
`eligibility-repair --kind=balance-anchor` repair for those has not yet been run against this
account) made the eligibility projection worker fail as a whole roughly every poll cycle:

```
ERROR background worker failed worker=eligibility-projection processed=0 error="eligibility projection job failed: balance blip confirmation for checkpoint/proof 7da221762ec783e0c843de1a7a72a2099665caf253fd7f13bfab8432e6f51b2e:2092 did not reconcile: still differs by 104322912"
```

The job row reached `attempt_count` 298, `status='failed'`, `last_error_code='PROJECTION_FAILED'`.
`/readyz` was 503 for hours. Production was rolled back to RC74 (`e38d7c0`) while this was fixed;
migration `0019` (from the sibling XM-INV-BALANCE-BLIP slice) stayed applied throughout — this slice
needed no new migration.

### Mechanism, reconstructed from the code (not merely the incident brief)

The team lead's own brief described the trigger as "the deferred positive difference... followed by
an item whose difference is positive but a DIFFERENT amount." Reading
`evaluatePendingBalanceEvidenceTx` (`backend/internal/postgresstore/consumption.go`) precisely: the
only place the exact error string `"balance blip confirmation for checkpoint/proof %s did not
reconcile"` could originate requires the *opposite* at the pre-check stage — `difference.Cmp(pending.difference) == 0`,
an **exact match** — because that condition alone gates entry into the confirm branch. The
"different amount" in the brief refers to the **post-synthesis residual**, not the pre-check: the
confirm branch's own rebuild-and-verify step (design XM-INV-BALANCE-BLIP section 2.8, "rebuild
rather than trust algebra") is exactly what catches the account's real, second, unrelated problem —
and the bug is that the code's response to catching it was `return fmt.Errorf(...)` instead of a
defined outcome. Flagging this discrepancy explicitly per this session's own practice of surfacing
brief/implementation deviations as they are found, not just in a final summary.

**Why a pre-check match and a post-check mismatch can both be true.** `buildEligibilityProjectionTx`
allocates usage against available credit oldest-first, per pool, floored at zero — a usage event
that arrives when insufficient credit exists at that instant permanently "wastes" the excess (tracked
as `ShortfallUsage`, but the pool itself never goes negative). If a *later* credit gets inserted
**dated earlier** than that already-floored usage (exactly what happens here: the deferred item's
credit is dated at its own *trust interval start*, which can be well before the confirming item's own
`as_of`), recomputing the projection from scratch lets that new credit retroactively fund some of the
previously-wasted usage — recovering less than the credit's full face value from the *confirming
item's* point of view, because part of it went to backfill the earlier shortfall instead of surviving
to offset the confirming item's own balance. The net effect: the confirming item's post-synthesis
difference can legitimately land on some *other*, smaller-but-still-nonzero value, even though its
own pre-synthesis raw difference matched the deferred item's exactly. This is precisely "the
account's ledger is missing its anchor opening balance, and parked usage/credits shift the gap" from
the incident brief, expressed in the projection's own arithmetic. Verified directly: a minimal fixture
reproducing this shape (a 1000-unit anchor, 1200 units of usage landing immediately after it —
exceeding the anchor credit alone by 200 — then two mid-stream checkpoints both reporting 300)
produces the exact same class of residual (200, not zero) after a tentative 300-unit credit is
inserted and the confirming item's projection rebuilt, matching production's shape one-for-one (a
nonzero, non-matching residual, not an error in the arithmetic itself).

### Batch isolation: already correct, not the actual defect

The task brief asked to check whether `ProcessEligibilityProjectionJobs`'s per-account isolation
"already exists and was bypassed by the blip path." Read closely
(`backend/internal/postgresstore/consumption.go`, `ProcessEligibilityProjectionJobs` /
`processEligibilityProjectionJob`): each account in a claimed batch is processed via its own call
inside the loop, in its own transaction; an error there marks *only that account*
`status='failed'`/`PROJECTION_FAILED` with the existing 5-minute backoff, and the loop unconditionally
continues to the next account. This isolation was **not** bypassed by the blip path — it already
protects every other account in any given batch. Verified with a new test,
`TestBalanceBlipSoftfailBatchStillProcessesHealthyAccount`, which builds a batch containing both the
blip account and an ordinary healthy account and confirms both get processed
(`ProcessEligibilityProjectionJobs` returns `processed=2`, no error) in the same call.

The real production symptom — "the whale account's checkpoints stopped being evaluated," "`/readyz`
down for hours" — was this **one** account's own job perpetually failing and retrying into the
identical error (no forward progress was possible: the transaction rolled back every write on error,
so the next attempt started from the exact same state and reproduced the exact same residual), plus
`eligibilityProjectionReady`'s own pre-existing, unconditional `Failed>0` rule
(`backend/cmd/api/runtime.go`) correctly surfacing that one stuck job as a readiness failure — not a
batch-level abort touching unrelated accounts.

## Required change 3: readiness semantics — confirmed unchanged, as instructed

`eligibilityProjectionReady` (`backend/cmd/api/runtime.go:520`) is:
```go
if health.Failed > 0 {
    return errors.New("invoice eligibility projection is unhealthy")
}
```
Unconditional — a single `status='failed'` row, of any age, trips `/readyz` immediately; there is no
grace period for `Failed` the way there is for `OldestPending` (`eligibilityProjectionStuckAfter`,
15 minutes). This is already covered by an existing test,
`TestEligibilityProjectionReadyDistinguishesProofPendingFromStuck/failed_alone`
(`backend/cmd/api/main_test.go:280`), which asserts `Failed: 1` → not ready; it passes unchanged (see
Gate results). **Decision: keep this rule unchanged**, per the task's own instruction and because it
is the correct behavior — a genuinely stuck job (unable to make forward progress, as this one was)
should immediately affect readiness, not wait out a grace period meant for jobs that are still
actively cycling through legitimate backoff. This slice's fix addresses the actual defect (the
blip-confirmation path no longer *produces* a permanently-stuck failed job for this bug class), which
resolves the practical `/readyz` symptom as a consequence, without touching the readiness rule
itself. `docs/superpowers/specs/2026-09-02-invoice-policy-anchor-design.md` section 2.9 records this
decision alongside the fix.

## The outcome rule implemented

`evaluatePendingBalanceEvidenceTx`'s confirm branch (case 1's `pending != nil` handling,
`backend/internal/postgresstore/consumption.go`), restructured:

1. **Tentative confirm, then verify** (unchanged in spirit from XM-INV-BALANCE-BLIP): pre-check
   exact match still gates entry; the deferred item's credit is synthesized; the projection is
   rebuilt for the confirming item.
2. **Reconciles to exactly zero → confirm, exactly as before.** The deferred item becomes
   `positive_classified_non_cash` (its credit stands), the confirming item becomes `matched`.
3. **Does not reconcile → softfail (new in this slice), never an error:**
   - The tentative credit is deleted (the same GUC-gated `source_credit_events` exception migration
     `0019` already added, `invoice.balance_blip_repair_delete='on'`, reused here inline rather than
     via a separate repair run — the credit never really existed as far as any other evaluation is
     concerned).
   - The deferred item is recorded `positive_blip_ignored` (never confirmed) with a new audit event,
     `eligibility.balance_blip.rebaselined`, carrying both the deferred item's own difference and the
     confirming item's raw (pre-synthesis) difference — this is what "carrying both amounts" means in
     the task brief, and it is what lets a later query distinguish "confirmation attempted and failed"
     from an ordinary, healthy clean disconfirmation.
   - The confirming item falls through and is classified fresh, exactly like an ordinary
     disconfirmation (its own `difference`/`balanceNegative`, computed before any credit ever existed,
     are untouched) — since it is still a positive difference on an account with real prior
     evaluation history, it becomes the **new** pending blip: a **rebaseline**.
4. **Rebaseline cap (`balanceBlipRebaselineCap = 3`).** Before deferring *any* item (not just after a
   softfail — the same code path handles a fresh first-time defer, where the count is always 0), a
   new helper, `countConsecutiveRebaselinedBlipsTx`, counts how many of the account's most recent
   checkpoint/proof evaluations are an unbroken run of `positive_blip_ignored` rows that specifically
   carry an `eligibility.balance_blip.rebaselined` audit event — deliberately *not* a raw count of
   `positive_blip_ignored`, which would also catch ordinary clean disconfirmations (XM-INV-BALANCE-BLIP's
   own pre-existing, healthy, non-looping outcome) and could cap an account that never actually had a
   reconciliation failure. Once the streak reaches the cap, the *next* would-be rebaseline instead
   writes a single `SOURCE_GAP` freeze on the triggering item (the same terminal outcome an account
   with no trust anchor at all already gets) — the designed escalation, guaranteeing the evaluator
   always makes forward progress instead of looping forever on an unresolved structural gap.

## Files changed

- `backend/internal/postgresstore/consumption.go` — `evaluatePendingBalanceEvidenceTx`'s confirm
  branch restructured per the outcome rule above; new constant `balanceBlipRebaselineCap`; new
  function `countConsecutiveRebaselinedBlipsTx`. No other function's behavior changes: the
  boundary rule, the bootstrap self-heal, the ordinary disconfirm path, `negative_frozen`, and
  `source_gap_frozen`'s pre-existing trigger (no trust interval at all) are all byte-for-byte
  unchanged.
- `backend/internal/postgresstore/balance_blip_softfail_integration_test.go` (new) — three
  integration tests (see Tests below).
- `docs/superpowers/specs/2026-09-02-invoice-policy-anchor-design.md` — status line and new section
  2.9.
- `docs/handoffs/XM-INV-BLIP-SOFTFAIL.md` — this file.

**Not touched:** `backend/Dockerfile` (pure code change inside the existing `postgresstore`
package); any migration (this slice needed none — the `positive_blip_ignored` status and the
`source_credit_events` GUC-gated delete exception both already exist from migration `0019`);
`backend/internal/migrate/migrate_test.go`; `scripts/release-image-gate-lib.ps1`;
`test-release-image-gate.ps1`; `verify-release-image-artifacts.ps1`; `RELEASE-READINESS.md`;
`docs/PRODUCTION-RUNBOOK.md`; `docs/IMAGE-SCAN-REVIEW.md`; `docs/superpowers/plans/`; any RC version
identity; `contracts/`; `cmd/eligibility-repair/`; `cmd/api/runtime.go`'s readiness logic itself; no
tags created.

## Tests

New tests, all in `backend/internal/postgresstore/balance_blip_softfail_integration_test.go`, all
verified to **fail with the exact production error message against the pre-fix code** (confirmed by
temporarily stashing the fix and re-running — see below) and pass after it:

- `TestBalanceBlipConfirmationThatDoesNotReconcileIsSoftfailedNotErrored` — the direct reproduction:
  a `POLICY_ANCHOR` account with real prior evaluation history (the fixture's own anchor), a usage
  event exceeding the anchor credit alone (floored), a deferred checkpoint (+300), and a second
  checkpoint whose raw pre-check difference exactly matches (+300, satisfying the "looks like a
  confirm" pre-check) but whose post-synthesis rebuild leaves a nonzero residual. Asserts:
  `evaluatePendingBalanceEvidenceTx` returns no error; the deferred item is
  `positive_blip_ignored`/0/300; the confirming item has no evaluation row (it is the new pending
  blip); exactly one `UNKNOWN_POSITIVE` credit exists (the fixture's anchor only — the tentative one
  was rolled back); zero open freezes; exactly one `eligibility.balance_blip.rebaselined` audit row.
  Before the fix, this returns `"balance blip confirmation for checkpoint/proof softfail-cp3 did not
  reconcile: still differs by 200"`.
- `TestBalanceBlipSoftfailRebaselineCapEscalatesToSourceGapFreeze` — five checkpoints (the anchor
  plus four more), each reproducing the identical non-reconciling shape (the underlying usage
  shortfall never actually resolves, matching a genuine, persistent structural gap): the first three
  rebaseline (`positive_blip_ignored` + a `rebaselined` audit row each), the fourth escalates to
  `source_gap_frozen` instead of becoming a fourth pending blip. Asserts the per-item statuses, the
  audit rows on exactly the first three, exactly one open `SOURCE_GAP` freeze, and that no credit
  ever survives (every tentative one was rolled back in turn). Before the fix, this also returns the
  reconciliation error (on the very first rebaseline attempt).
- `TestBalanceBlipSoftfailBatchStillProcessesHealthyAccount` — a
  `ProcessEligibilityProjectionJobs` batch containing both the blip account (reproducing the
  non-reconciling shape) and a second, ordinary healthy account (its own anchor checkpoint only, no
  blips). Asserts the batch call returns `processed=2` with no error, the healthy account is
  `active` with zero freezes, and both accounts' job rows are cleared (both succeeded). Confirms the
  pre-existing per-account isolation in `ProcessEligibilityProjectionJobs` continues to work
  correctly with the fix in place; before the fix, the batch call itself returns the wrapped
  reconciliation error (though — per the isolation section above — the healthy account's own
  processing was never actually blocked by this, even pre-fix).

**Reproduction verified against the pre-fix code:** ran all three new tests with
`consumption.go`'s changes temporarily reverted (`git stash push -- .../consumption.go`); all three
failed with the exact production error class (`"... did not reconcile: still differs by ..."`),
confirming genuine reproduction rather than a fixture artifact; then restored the fix
(`git stash pop`) and confirmed all three pass.

**Deliberately not separately tested:** a `balance_carry_forward_proofs`/`_evaluations` version of
the softfail/rebaseline/escalation path — the checkpoint-kind path is the dominant, actually-observed
production shape (as with the sibling XM-INV-BALANCE-BLIP slice's own equivalent note), and the new
code (`countConsecutiveRebaselinedBlipsTx`'s carry-forward UNION branch, the credit-delete/audit
logic, which is evidence-kind-agnostic) mirrors the already-tested checkpoint path's logic exactly.
Flagged as a real coverage gap, not silently omitted, matching this session's own practice of
surfacing gaps explicitly.

## Gate results

From `backend/`, with the eight proxy variables unset and
`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_blipsoft` (a
dedicated database created for this task in the already-running `invoice-test-pg` container, created
manually via `docker exec invoice-test-pg psql -U postgres -c "CREATE DATABASE
invoice_test_blipsoft;"` since this explicit, non-default name is outside the `testdb` package's
auto-create-on-`invoice_test` rewrite):

```
go build ./...                 # exit 0
go vet ./...                   # exit 0
go test -p 1 -count=1 ./...    # ok, all packages
```

Full package list, all `ok`: `cmd/api`, `cmd/archive-verify` (no tests), `cmd/backup-verify` (no
tests), `cmd/bootstrap-settings`, `cmd/bootstrap-sources`, `cmd/document-gc` (no tests),
`cmd/eligibility-repair`, `cmd/identity-migrate`, `cmd/keygen`, `cmd/migrate`, `cmd/mtlsgen`,
`cmd/oidc-logout-retention` (no tests), `cmd/oidc-preflight`, `cmd/pdf-policy-check`,
`cmd/pdf-scanner` (no tests), `internal/adminsettings`, `internal/application`, `internal/auth`,
`internal/backuparchive`, `internal/backupverify`, `internal/document`, `internal/domain`,
`internal/httpapi`, `internal/ledger`, `internal/mailer`, `internal/migrate`, `internal/oidcretention`,
`internal/pdfscanner`, `internal/postgresstore` (this slice's new tests included; ~100s), `internal/securefields`,
`internal/sourceingest`, `internal/testdb`.

`"$(go env GOROOT)/bin/gofmt" -l` against the **staged git blob** of every file this slice touches or
adds (`git show ":<path>"`, per this machine's documented CRLF/gofmt quirk): clean on both files.

`gitleaks git --no-banner --log-opts="578b6b2..HEAD" .`: clean, against both commits above.

## Not run

- `scripts/verify.ps1` in full — spans frontend, Docker release-image gates, and Keycloak/Nginx
  verification, none of which this backend-only slice touches, matching both sibling slices' own
  precedent.
- Anything requiring a server/production connection — no production access for this task.
- Rehearsal against a restored production backup.

## Risks / things to sign off on

1. **No migration needed or added.** This slice reuses migration `0019`'s existing
   `positive_blip_ignored` status and `source_credit_events` GUC-gated delete exception unchanged —
   verified both were sufficient before writing any code, not assumed.
2. **`countConsecutiveRebaselinedBlipsTx` is a new, moderately complex query** (a `UNION ALL` over
   checkpoint and carry-forward evaluations, each joined against an `EXISTS` subquery on
   `audit_events`, limited to `balanceBlipRebaselineCap+1` rows). Chosen over a simpler raw
   `positive_blip_ignored` count specifically to avoid capping an account that only ever has ordinary,
   healthy, clean disconfirmations — verified this distinction actually matters by construction (the
   two `positive_blip_ignored` outcomes are semantically different: one is the designed,
   already-tested, non-looping XM-INV-BALANCE-BLIP outcome; the other is this slice's own new,
   loop-risk outcome) rather than assumed to be an unnecessary refinement.
3. **`balanceBlipRebaselineCap = 3` is a judgment call**, per the task's own "e.g. 3" — not derived
   from any production measurement of how many consecutive rebaselines a real structural gap might
   need before an operator should be paged via a freeze. Tunable without a migration if 3 proves too
   eager or too lax in practice.
4. **The historical 66 `source_gap_frozen` checkpoint evaluations on account `98cce4c8-...` are
   explicitly out of scope** for this slice — they predate XM-INV-ANCHOR-BALANCE and need
   `eligibility-repair --kind=balance-anchor` (a separate, already-existing repair), not anything
   this slice touches. This slice's fix lets the account's projection job *run* again (no more
   permanent error/retry loop) and will correctly re-evaluate whatever checkpoints are pending from
   here forward, but does not retroactively fix those 66 already-frozen historical rows.
5. **The exact production numbers (attempt_count 298, residual 104322912) are a snapshot from the
   team lead's 2026-09-03 investigation**, not re-measured by this task (no production access). The
   reproduction test uses different, minimal numbers (300/1200/200) chosen to demonstrate the same
   *class* of bug deterministically, not to replay the exact production magnitudes.
6. No new float amounts, no logged/persisted secrets, no `contracts/` changes, no admin-OIDC changes,
   no touch to any release-identity file, no tags created, no touch to any file outside this slice's
   stated scope, `backend/Dockerfile` untouched — checked.

## Follow-ups (recommended, not blocking this delivery)

1. Consider whether `balanceBlipRebaselineCap` should be configurable (env var / admin setting)
   rather than a compile-time constant, once there is real production data on how often genuine
   rebaseline chains occur and how long they typically run before either resolving or needing the
   escalation.
2. A dedicated integration test for the carry-forward-proof shape of the softfail/rebaseline path
   (see "Deliberately not separately tested" above), matching the sibling slice's own equivalent
   follow-up for XM-INV-BALANCE-BLIP's repair tool.
3. Run `eligibility-repair --kind=balance-anchor` against account `98cce4c8-...`'s 66 historical
   frozen checkpoints once this fix is live in production — a separate, already-existing repair, not
   part of this slice, but the natural next step for that specific account's full recovery.
