# XM-INV-ELIG-QUEUE-NARROW: manual-queue narrowing and legacy-freeze migration (design section 3(C))

- **status:** implemented and self-tested locally; full backend suite green (`go test -p 1 -count=1
  ./...`), `go vet` clean, `go build` clean, `gofmt` clean (staged-blob method).
- **branch:** `ai/claude/XM-INV-ELIG-QUEUE-NARROW` (based on `ai/claude/XM-INV-AUTOLOGIN` at `a8605ac`,
  which already contains slice 1 `XM-INV-ELIG-AUTO-RECONCILE`, migration 0020, and the shadow-eval
  tool), worktree `K:/发票/wt-XM-INV-QUEUE-NARROW`.
- **spec:** `docs/superpowers/specs/2026-09-03-xm-inv-eligibility-simplification-design.md`, section
  3(C) items 2 and 3 (item 1 was already implemented by slice 1), the parts of section 5 (test matrix)
  and section 6 (slice 2) that belong to this slice.

## Summary

Two changes, per design section 3(C):

### Item 2: `observeEligibilityFact`'s generalized late-fact freeze removed

`backend/internal/postgresstore/consumption.go`'s `observeEligibilityFact` used to freeze the whole
account (`LATE_FINALIZED_EVENT`, no `funding_lot_id`, trigger object the fact itself) on *any* late
fact, purely as a blanket defense, regardless of whether the resulting reprojection found a real
problem. That freeze call is removed; the function still calls `reprojectEligibilityTx` unconditionally
when a fact is late (unchanged), and now writes `eligibility.late_fact.reprojected` instead
(`object_type=in.Kind`, `object_id`=the newly-inserted fact's own row id). `reprojectEligibilityTx`
itself is untouched: its own, separate, precise freeze (`lot.RoundedMinor<lot.IssuedMinor`, a real
red-reversal against an already-issued invoice) still fires exactly as before, with `funding_lot_id`
set -- verified by `TestLateFactRedReversalStillOpensPreciseFreeze` driving the real
`ObserveCreditEvent`/`observeEligibilityFact` entrypoint into that exact branch.

This is the **only** change made to `consumption.go` in this slice, localized to the single `if late {
... }` block inside `observeEligibilityFact` -- per the task brief, another agent may be editing this
same file's other evaluator branches in parallel.

### Item 3: `invoice-eligibility-repair --kind=queue-narrow`

New store-layer file `backend/internal/postgresstore/queue_narrow_repair.go` (`RepairQueueNarrowEligibility`)
and a new CLI branch in `backend/cmd/eligibility-repair/main.go`, following the existing
`balance_anchor_repair.go`/`balance_blip_repair.go` dry-run/apply/operator-id/encrypted-fixed-note
conventions, with one deliberate structural difference (see "Deviation 1" below).

For every still-open `eligibility_freezes` row where `freeze_reason IN ('UNKNOWN_NEGATIVE_BALANCE',
'USAGE_EXCEEDS_LEDGER')` or (`freeze_reason='LATE_FINALIZED_EVENT' AND funding_lot_id IS NULL`):

1. Resolves it with the same column shape `ResolveEligibilityFreeze` writes (`status='resolved'`,
   `resolved_by`, encrypted evidence/note, `resolution_version+1`), fixed note "由
   XM-INV-ELIG-SIMPLIFY 迁移自动解除" (design's own specified text, verbatim).
2. For `UNKNOWN_NEGATIVE_BALANCE`: reads the account's latest real balance evaluation (the same
   "latest finalized evaluation" query shape `ResolveEligibilityFreeze` itself uses, generalized to
   return the evaluation's own kind/key/as_of/status/expected/difference). If that evaluation is
   `negative_frozen`, calls the existing `enterPendingReconciliationTx` (slice 1) to rebuild
   `not_invoiceable_pending_reconciliation` from it -- **never simply clears the account**, so one
   still genuinely negative is never misreported "resolved". If no evaluation is on record, or the
   latest one is not `negative_frozen`, nothing is rebuilt (the account is not pushed into pending
   reconciliation over stale, already-superseded evidence).
3. For `USAGE_EXCEEDS_LEDGER`: calls the existing `reprojectEligibilityTx` (slice 1), which
   unconditionally calls `recordUsageOverageTx` -- sets or clears the account's overage columns from
   the *current* projection, not the stale value the old freeze recorded.
4. After both rebuilds, if no `eligibility_freezes` row remains open at all, the account returns to
   `active` (never forced while a real, distinct freeze -- including one this run does not target --
   is still open), through the same reactivate-and-queue-a-projection-job idiom the sibling repair
   tools use.

## Deviations from the design/brief (flagged inline during implementation, not only at the end)

1. **Per-account transaction, not one transaction for the whole run.** The three existing sibling
   repair tools (`pre-anchor-usage`, `balance-anchor`, `balance-blip`) run their entire dry-run/apply
   pass inside one `SERIALIZABLE` transaction. The task brief for this slice explicitly required the
   opposite -- "apply performs them in one transaction per account" and "per-account errors must never
   escape the projection batch or abort the repair run for other accounts" -- citing the RC75
   evaluator regression as the reason. Implemented exactly as briefed:
   `RepairQueueNarrowEligibility` takes one cheap, transaction-less snapshot read of candidate account
   ids (`queueNarrowCandidateAccountIDs`), then calls `repairQueueNarrowAccount` once per account, each
   opening and (on success, in apply mode) committing its own `SERIALIZABLE` transaction; a failure for
   one account is caught, recorded in `QueueNarrowRepairResult.Errors`, and the loop continues. A stale
   snapshot (a freeze resolved concurrently between the snapshot read and that account's own
   transaction) is harmless: the account's own `FOR UPDATE` re-select simply finds zero matching rows
   and returns an empty, error-free result. This is a real, deliberate departure from the sibling
   tools' own established pattern, called out here because a future maintainer comparing this file to
   `balance_blip_repair.go` will otherwise wonder why the shape differs.
2. **No new migration.** The brief said to add migration 0021 (mirroring 0019's GUC-gated pattern) only
   if an existing trigger/constraint blocks the resolve write. Verified by reading every migration that
   touches `eligibility_freezes` (0009, 0010, 0016, 0020): there is no `BEFORE UPDATE` trigger on that
   table at all (unlike `source_credit_events`/`balance_carry_forward_proofs`/etc., which are fact
   tables with immutability triggers) -- `ResolveEligibilityFreeze` and both existing repair tools
   already do a plain `UPDATE ... SET status='resolved',...` with no GUC. This repair's own
   `applyQueueNarrowFreezeResolution` does the identical plain `UPDATE`. No migration added.
3. **`enterPendingReconciliationTx`'s frozen-priority guard required reordering the apply steps.**
   First implementation attempt resolved freezes, then called `enterPendingReconciliationTx` before
   checking whether any freeze remained open -- but resolving a freeze row does not by itself touch
   `eligibility_status`, so the account's own status column was still literally `'frozen'` at that
   point, and `enterPendingReconciliationTx`'s own frozen-priority guard (`WHERE ...
   eligibility_status<>'frozen'`) correctly, silently no-opped -- exactly its documented behavior,
   protecting against downgrading a genuinely frozen account, but wrong here since the freeze that
   made it "frozen" was one this run had *just* resolved. Caught by
   `TestQueueNarrowApplyRebuildsPendingReconciliationForStillNegativeAccount` failing with
   `Reactivated=true` where the design instead requires `RebuiltPendingReconciliation=true,
   Reactivated=false`. Fixed by reordering: reproject (may itself open a new, different freeze) →
   compute the true remaining-open-freeze count → reactivate to `active` if and only if zero remain →
   only then run the negative-balance rebuild (which now sees the account's *current* status, not its
   stale pre-repair `'frozen'` value) → read the account's *final* status once, at the very end, to
   decide `Reactivated`/`RebuiltPendingReconciliation` (rather than tracking intermediate per-step
   flags, since the negative-balance rebuild step can immediately reverse a reactivation the remaining-
   freeze-count step just performed, within the same transaction, and no external caller ever observes
   the intermediate state). This is exactly the "every code branch returns a defined result" /
   "collect and report, do not force" discipline the brief's hard rules asked for -- flagging the fix
   here since it changed the internal step order from a first, more naive draft, not because it departs
   from anything explicitly specified.
4. **`recordUsageOverageTx`/`enterPendingReconciliationTx`/`reprojectEligibilityTx` are called
   unmodified from slice 1** -- this slice adds no new production logic to those functions, only new
   call sites in the new repair file. `eligibility_operations.go`'s `eligibilityFreezeReasons` map
   (admin queue filter whitelist) is unchanged, matching slice 1's own precedent of leaving it alone (a
   distinct concept from which reasons this repair tool targets).
5. **Advisory lock choice.** `repairQueueNarrowAccount` takes `pg_advisory_xact_lock(hashtextextended($1,43))`
   per account before doing any work -- the same lock number `processEligibilityProjectionJob` and
   `ObserveBalanceCheckpoint`/`Observe{Usage,Credit}Event` already use for exactly this account-level
   consistency purpose (this repair calls the identical `reprojectEligibilityTx`/
   `enterPendingReconciliationTx` machinery outside the normal job queue, so it must serialize against
   a concurrent real projection job or Observe* call the same way). Not specified by the brief; chosen
   for consistency with the existing convention rather than inventing a new lock number.

## Files changed

- `backend/internal/postgresstore/consumption.go` -- `observeEligibilityFact`'s late-fact branch (see
  Summary item 2 above). The only change in this file.
- `backend/internal/postgresstore/queue_narrow_repair.go` (new) -- `RepairQueueNarrowEligibility` and
  its helpers (`queueNarrowCandidateAccountIDs`, `repairQueueNarrowAccount`,
  `applyQueueNarrowFreezeResolution`, `latestBalanceEvaluationTx`).
- `backend/internal/postgresstore/late_fact_reproject_integration_test.go` (new) --
  `TestLateFactReprojectsWithoutFreezingAccount`,
  `TestLateFactRedReversalStillOpensPreciseFreeze`.
- `backend/internal/postgresstore/queue_narrow_repair_integration_test.go` (new) -- this slice's own
  fixtures and six repair-tool tests (see Tests below).
- `backend/cmd/eligibility-repair/main.go` -- `kindQueueNarrow` constant,
  `queueNarrowFixedResolutionNote`, `runQueueNarrow`, `printQueueNarrowSummary`, updated `--kind` flag
  usage string, updated package doc comment.
- `backend/cmd/eligibility-repair/main_test.go` -- `queueNarrowApplyResultFixture`,
  `TestRunQueueNarrowDryRunAgainstEmptyDatabaseReportsNothing`,
  `TestPrintQueueNarrowSummaryFormatsAccountsAndTotals`; added `kindQueueNarrow` to
  `TestRunApplyWithoutOperatorIDIsRejected`'s loop.
- `docs/ELIGIBILITY-OPERATIONS.md` -- new "Manual queue narrowing" section (the late-fact behavior
  change, the repair tool's own behavior, and its dry-run/apply invocation as it would run in the
  tools container).
- `docs/handoffs/XM-INV-ELIG-QUEUE-NARROW.md` -- this file.

**Not touched:** `backend/migrations/` (no new migration -- see Deviation 2); `backend/Dockerfile`;
`contracts/`; `deploy/`; `web/`; `eligibility_operations.go`'s `eligibilityFreezeReasons` map; any other
branch of `consumption.go`; `K:/sub2api-src`, `K:/newapi-src` (off limits, never touched); no tags
created.

## Tests

New tests, all green:

**`late_fact_reproject_integration_test.go`:**
- `TestLateFactReprojectsWithoutFreezingAccount` -- a late, harmless credit fact opens no freeze,
  writes `eligibility.late_fact.reprojected`, writes no `eligibility.frozen.*` audit, account stays
  `active`.
- `TestLateFactRedReversalStillOpensPreciseFreeze` -- a fully-consumed 100-unit `WALLET_CASH` lot with
  a simulated 100,000-minor issued exposure (direct SQL; `markLotIssuedAttentionTx` itself no-ops
  cleanly with no matching `invoice_allocations` rows, so a full invoice fixture is unnecessary to arm
  the guard), then a late, earlier-dated 50-unit non-cash credit observed through the real
  `ObserveCreditEvent` entrypoint reallocates half the usage away from the lot, dropping its
  recognized amount to 50,000 -- below the 100,000 issued. Asserts exactly one open freeze, precise
  (`funding_lot_id` set, `trigger_object_type='funding_lot'`), account `frozen`, and both audit events
  present (`eligibility.late_fact.reprojected` *and* `eligibility.frozen.late_finalized_event`,
  confirming the new unconditional audit does not replace or suppress reprojection's own separate
  freeze audit).

**`queue_narrow_repair_integration_test.go`:**
- `TestQueueNarrowDryRunChangesNothing` -- an account with all three target categories open: dry run
  reports the correct 1/1/1 counts, and every freeze/account/audit row is verified unchanged
  afterward.
- `TestQueueNarrowApplyResolvesAllThreeCategoriesAndReactivates` -- same three-category account, no
  real ledger facts at all: apply resolves all three, no rebuild happens (nothing to rebuild from),
  and with zero freezes left the account reactivates to `active`.
- `TestQueueNarrowApplyRebuildsPendingReconciliationForStillNegativeAccount` -- a real
  `negative_frozen` checkpoint evaluation on record: apply resolves the stale freeze but rebuilds
  `not_invoiceable_pending_reconciliation` from that evaluation (reason/trigger/detail/since all
  correctly populated), and does *not* reactivate.
- `TestQueueNarrowApplyDoesNotRebuildPendingReconciliationForReconciledAccount` -- the mirror case: the
  latest evaluation is `matched`. Apply resolves and reactivates; no rebuild.
- `TestQueueNarrowApplyLeavesNonTargetReasonsUntouched` -- `SOURCE_GAP` and `SOURCE_REFUND` freezes
  alongside a target `UNKNOWN_NEGATIVE_BALANCE` freeze on the same account: only the target resolves;
  both non-target freezes stay `open` at `resolution_version=1`; the account correctly stays `frozen`
  (real exposure remains).
- `TestQueueNarrowAccountIsolation` -- two accounts in one `RepairQueueNarrowEligibility` call: a
  "broken" account whose funding lot's `reserved_minor+issued_minor` (55,000) no longer fits under
  what a fresh reprojection recomputes (50,000, once a 50-unit usage fact is the only remaining
  consumer) -- `reprojectEligibilityTx`'s own pre-existing guarded `UPDATE` conflicts
  (`domain.ErrConflict`, a real and reachable production data shape, not a synthetic error injection)
  -- and a "good" account with a clean `UNKNOWN_NEGATIVE_BALANCE` freeze. Asserts
  `RepairQueueNarrowEligibility` itself returns no error; exactly one collected
  `QueueNarrowRepairAccountError` for the broken account; the good account is processed to completion
  and reactivated; and the broken account's own failed transaction left no partial trace (freeze still
  open at version 1, account still `frozen`, lot's `consumed_cash_minor` unchanged).

Existing tests re-run unmodified as part of the full package runs below, including every prior
`consumption.go` evaluator test, every sibling repair tool's own tests, and everything in
`eligibility_auto_reconcile_integration_test.go` -- all still pass with this slice's changes (no
pre-existing assertion needed correcting, unlike slice 1's own two carry-forward fixes).

## Gate results

From `backend/`, with the eight proxy variables unset and
`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_queuenarrow?sslmode=disable`
(a dedicated database created for this task, separate from other agents' databases; never
`invoice_test_release`):

```
go build ./...                 # exit 0
go vet ./...                   # exit 0
go test -p 1 -count=1 ./...    # ok, all packages, single full run, no flakes observed
```

Full run summary (all `ok`): `cmd/api`, `cmd/bootstrap-settings`, `cmd/bootstrap-sources`,
`cmd/eligibility-repair`, `cmd/eligibility-shadow`, `cmd/identity-migrate`, `cmd/keygen`,
`cmd/migrate`, `cmd/mtlsgen`, `cmd/oidc-preflight`, `cmd/pdf-policy-check`, `internal/adminsettings`,
`internal/application`, `internal/auth`, `internal/backuparchive`, `internal/backupverify`,
`internal/document`, `internal/domain`, `internal/httpapi`, `internal/ledger`, `internal/mailer`,
`internal/migrate`, `internal/oidcretention`, `internal/pdfscanner`, `internal/postgresstore`,
`internal/securefields`, `internal/sourceingest`, `internal/testdb` -- no isolated rerun was needed, no
flake observed.

`"$(go env GOROOT)/bin/gofmt" -l` against the **staged git blob** of every file this slice touches or
adds (`git show ":<path>"`, matching this machine's documented CRLF-checkout `gofmt` false-positive
handling): clean on every file.

`gitleaks git --no-banner --log-opts="a8605ac..HEAD" .`: clean (see commit log for exact range).

## Not run

- `scripts/verify.ps1` in full -- spans frontend, Docker release-image gates, and Keycloak/Nginx
  verification, none of which this backend-only slice touches. Its backend-relevant lines are covered
  in spirit by the gates above, run without `-race` (not requested for this task).
- Anything requiring a server/production connection -- explicitly out of scope, matching every prior
  sibling slice's own precedent.
- A live run of `invoice-eligibility-repair --kind=queue-narrow` against a real production-shaped
  database with actual pre-slice-1 `UNKNOWN_NEGATIVE_BALANCE`/`USAGE_EXCEEDS_LEDGER`/generalized-
  `LATE_FINALIZED_EVENT` freezes -- no production access for this task; the integration tests construct
  each shape directly and exercise the real repair code path against it.

## Risks / things to sign off on

1. **The negative-balance rebuild's "latest evaluation" query does not correlate to the specific
   freeze(s) being resolved.** If an account has more than one open `UNKNOWN_NEGATIVE_BALANCE` freeze
   (historically possible: `freezeEligibilityTx`'s `ON CONFLICT` only dedupes by
   `(account,reason,trigger_object_type,trigger_object_id)`, so different triggering checkpoints each
   got their own row), this repair resolves all of them but rebuilds pending-reconciliation state at
   most once, from the account's single latest real evaluation -- not once per historical freeze. This
   is the deliberately correct behavior (the account only has one current negative/not-negative state,
   not one per stale historical freeze row), but is called out since it means
   `NegativeBalanceFreezesResolved` can be `>1` while `RebuiltPendingReconciliation` reflects only the
   single current answer.
2. **`latestBalanceEvaluationTx` only considers evaluations at or before the account's own
   `finalized_through`**, mirroring `ResolveEligibilityFreeze`'s own identical query precedent exactly.
   An account with newer, not-yet-evaluated checkpoints/proofs past `finalized_through` is judged on
   its latest *evaluated* evidence, not on data that has not been projected yet -- consistent with how
   the interactive resolve endpoint already judges "is this account safe," but flagging that this
   repair does not itself trigger a fresh evaluation pass before judging.
3. **Dry-run's `Reactivated`/`RebuiltPendingReconciliation` preview does not simulate
   `reprojectEligibilityTx`'s own potential side effect of opening a brand-new, different freeze**
   (e.g. `AMBIGUOUS_EVENT_ORDER`, or a genuine funding-lot red-reversal this repair's own
   `USAGE_EXCEEDS_LEDGER` rebuild step could theoretically trigger). Dry run only inspects currently-
   open freezes and already-recorded evaluations, since it must write nothing; apply's own final-status
   read is the source of truth. A dry-run preview that predicted "would reactivate" could, in a rare
   case, differ from what apply actually does if reprojection surfaces a brand-new problem -- explicitly
   noted in the code's own comment at the dry-run branch.
4. No new float amounts, no logged/persisted secrets, no `contracts/` changes, no admin-OIDC changes,
   no touch to any release-identity file, no tags created, no touch to `backend/Dockerfile`, no touch
   to any file outside this slice's stated scope -- checked.

## Follow-ups (recommended, not blocking this delivery)

1. Slice 3 (`XM-INV-ELIG-POLICY-START-ANCHOR`, design section 3(D)) and slice 4
   (`XM-INV-ELIG-USER-LEDGER-QUERY`, design section 3(E)) remain -- both depend on slice 1
   (already shipped) and, for slice 4, additionally on slice 3's "recharged since policy start" framing.
   Neither attempted here.
2. Consider whether production has any existing open `UNKNOWN_NEGATIVE_BALANCE`/`USAGE_EXCEEDS_LEDGER`/
   generalized-`LATE_FINALIZED_EVENT` freezes right now that this repair tool should be run against --
   no production access for this task to check (same open question slice 1's own handoff left).
