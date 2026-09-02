# XM-INV-PREANCHOR-USAGE: pre-anchor usage/credit facts must not freeze SOURCE_GAP

- **status:** fully implemented and self-tested locally — all three required changes landed
  (projection rule, observability, repair tool), full backend suite green
  (`go test -p 1 -count=1 ./...`), `go vet` clean, `go build ./...` clean.
- **branch:** `ai/claude/XM-INV-PREANCHOR-USAGE` (based on `ai/claude/XM-INV-AUTOLOGIN` at
  `43bdc59`, tag `v0.1.0-rc69-signed`), worktree `K:/发票/wt-XM-INV-PREANCHOR`.
- **commits:** three, in dependency order (oldest first):
  1. `299548b` fix(postgresstore): skip pre-anchor usage/credit facts for POLICY_ANCHOR accounts
     (requirement 1 — the projection rule)
  2. `7a3ecf8` fix(application): log projection failures before marking events failed/dead
     (requirement 2 — observability)
  3. `993ea8f` feat(tools): add eligibility-repair CLI for the pre-anchor usage incident
     (requirement 3 — the repair tool)

  This document itself, plus the design doc update, is a fourth, docs-only commit on top (see
  Files changed below).

## Incident recap (production, 2026-09-02, read-only evidence given by the team lead)

RC68 (design `XM-INV-POLICY-ANCHOR`) went live at 2026-09-01T23:58Z. At 2026-09-02T02:00:13Z the
Sub2API balances cycle delivered verified v3 balance checkpoints for accounts that had no
eligibility state yet; 106 accounts were bootstrapped with `bootstrap_kind=POLICY_ANCHOR`,
`cutover_at` = that checkpoint's own time (e.g. external_account_id
`98cce4c8-a03c-4b55-9049-61b650db2d0e`, source `2d831a79-95ba-40b4-b19f-4b5f5ac57fd2`, unit
`SUB2_BALANCE_1E8`, cutover_balance_units `2176467924`, checkpoint at 02:00:12.97Z).

Within the same second, the Sub2API usage stream's daily reconcile replay (~99,900 usage records
over 1000 pages) delivered usage facts for those same accounts whose `event_time` was before the
fresh `cutover_at` (but after the invoice policy start, 2026-08-31T16:00:00Z). The projection path
(`postgresstore/consumption.go`'s `observeEligibilityFact`, shared by `ObserveUsageEvent` and
`ObserveCreditEvent`) hit `!in.EventTime.After(account.CutoverAt)` →
`freezeEligibilityTx(..., "SOURCE_GAP", "usage", "usage_logs:3868628", ...)` + `ErrConflict` → the
source event was marked `PROJECTION_FAILED`, retried every 5 minutes, and after 8 attempts marked
`dead`. Audit trail: 105 `eligibility.frozen.source_gap` at 02:00, 1 at 02:04, 100 at 02:05, then
100 `eligibility.frozen.event_dead` (one per dead usage event, all on the same stream) at
02:37:08Z. Net result: 106 open `SOURCE_GAP` freezes, 100 open `EVENT_DEAD` freezes, 100 dead + 2
failed `source_ingest_events` (stream `usage`, entity `usage_event`, error `PROJECTION_FAILED`),
and the API readiness gate returning 503 (`"source ingestion contains dead events"`,
`cmd/api/runtime.go`'s `validateSourceIngestRuntimeReadiness`, unmodified by this slice — see
Verification below).

**Root cause: a design gap, not a coding bug.** `SOURCE_GAP` exists to catch a real missing
predecessor in a legacy (`SIGNED_CUTOVER`/`POST_CUTOVER_REPLAY`) account's *replayed history*. A
`POLICY_ANCHOR` account's `cutover_at` is not a replayed-history boundary — it is a reconciliation
checkpoint's own observed `as_of` (design `XM-INV-POLICY-ANCHOR` 2.1). A fact at or before it
cannot be attributed to any ledger baseline by construction, whether or not it also predates the
account's policy start; that is not evidence of a gap. Design `XM-INV-POLICY-ANCHOR` 2.1/2.2
handled this timing correctly at *bootstrap* time (a no-state account) and at *identity-wake* time
(`RequeueSourceDependency`'s bulk pre-policy skip) but never at *ordinary projection* time for an
account that already has `POLICY_ANCHOR` state — exactly the case this incident hit, since the
bootstrap and the usage replay landed in the same second.

## Requirement 1: projection rule (`299548b`)

`observeEligibilityFact`'s `SOURCE_GAP` branch (`postgresstore/consumption.go`, around the
`!in.EventTime.After(account.CutoverAt)` check) now branches on `account.BootstrapKind` first:

- `POLICY_ANCHOR`: skip. Write one audit row (`eligibility.usage.pre_anchor_skipped` or
  `eligibility.credit.pre_anchor_skipped`, object type `external_account`, details include the
  fact's external object id, event time, the account's `cutover_at`, and the source revision),
  do **not** freeze, and `return tx.Commit(ctx)` — a `nil` error, so the caller
  (`application/source_processor.go`'s `RunOnce`) calls `MarkSourceEventProcessed`, not
  `MarkSourceEventFailed`. Deliberately does **not** insert a `source_usage_events`/
  `source_credit_events` row for the fact: this matches the existing precedent set by 2.1's
  `eligibility.pre_policy_checkpoint.skipped` path and 2.2's `PRE_POLICY_SKIPPED` bulk-skip — a
  skipped fact is represented purely by its audit row, never persisted as a ledger-visible fact.
  No new column, no migration.
- Legacy (`SIGNED_CUTOVER`/`POST_CUTOVER_REPLAY`, or any future kind): unchanged — freezes
  `SOURCE_GAP` exactly as before this slice, since a legacy account's cutover really does replay
  history and a fact at or before it really can be a gap.

**No migration.** The task description flagged "add the minimal column/status the design needs,
with a forward-only migration 0018" as a fallback if the schema had no existing skipped
representation to reuse. It does: the audit-row-only, no-persisted-fact pattern already
established by `ObserveBalanceCheckpoint`'s `eligibility.pre_policy_checkpoint.skipped` path (and
`RequeueSourceDependency`'s `PRE_POLICY_SKIPPED` bulk skip) is exactly that representation, reused
here verbatim. (`source_credit_events` does carry an unrelated `PRE_POLICY_NON_INVOICEABLE`
`credit_kind` — checked and rejected as inapplicable: it is 1:1 with a `funding_lot_id` under a
unique index, modeling a pre-policy *payment* converted to a non-invoiceable display credit, not a
generic "skip this fact" marker, and `source_usage_events` has no `credit_kind` column at all.)
Migration numbering stays at `0017` (last existing); no `0018` was created.

**Tests** (new file `backend/internal/postgresstore/preanchor_usage_integration_test.go`, all
built through the real `ObserveUsageEvent`/`ObserveCreditEvent`/`ObserveBalanceCheckpoint` calls,
not hand-crafted SQL state):
- `TestPolicyAnchorAccountPreCutoverUsageFactSkippedWithoutFreeze` /
  `...CreditFactSkippedWithoutFreeze` — a `POLICY_ANCHOR` account's pre-cutover usage/credit fact:
  `Observe*Event` returns `nil`, no open freeze, no `source_usage_events`/`source_credit_events`
  row, exactly one matching skip audit row.
- `TestPolicyAnchorAccountPostCutoverUsageFactAllocatesNormally` — a fact after `cutover_at` still
  allocates normally against a credit in the same window (drives `buildEligibilityProjectionTx`
  directly and confirms the fact's own persisted row id appears in `Allocations`).
- `TestLegacyAccountPreCutoverUsageFactStillFreezesSourceGap` — regression guard: a legacy
  (`SIGNED_CUTOVER`, constructed via direct SQL since no production code path creates one anymore
  post-`XM-INV-POLICY-ANCHOR`) account's pre-cutover usage fact still freezes `SOURCE_GAP` and
  returns an error, unchanged.

## Requirement 2: observability (`7a3ecf8`)

`application/source_processor.go`'s `SourceEventProcessor` gains an optional `Logger *slog.Logger`
field (`logger()` helper, falls back to `slog.Default()` — matches `sourceingest.Receiver`'s
existing convention exactly). A new `logProjectionFailure(logger, claim, cause)` helper logs
(`slog.Warn`, message `"source event projection failed"`) the source instance id, stream id, event
id, and the error text — **never payload contents** — and `RunOnce` calls it immediately before
its one call to `store.MarkSourceEventFailed(ctx, claim, "PROJECTION_FAILED", ...)`. That is the
single call site responsible for both the resulting `'failed'` and, after enough attempts,
`'dead'` outcomes (`MarkSourceEventFailed` decides between them internally off the claim's own
`attempt_count`), so one log call site covers both without needing to know here which one a given
attempt will become.

**Tests:**
- `TestLogProjectionFailureLogsSourceStreamEventAndErrorNotPayload` /
  `...DefaultsToSlogDefaultWithoutPanicking` (`internal/application/source_processor_test.go`) —
  pure unit tests against the extracted `logProjectionFailure` helper directly (buffer-backed
  `slog.Logger`, hand-built claim), asserting the four expected fields are present and a payload
  marker is absent, and that a `nil` logger does not panic.
- `TestSourceProjectionWorkerLogsProjectionFailure` (`internal/application/service_integration_test.go`)
  — end-to-end wiring check: a real `SourceEventProcessor.RunOnce` call against a deterministically
  failing usage event (the same `WRONG_UNIT_CODE` trick
  `TestDeadUsageEventWithoutPersistedFactStillFreezesViaApplicationLayerAccountHint` already uses)
  with a buffer-backed `Logger`, asserting the Warn line appears with the right ids and does not
  leak the payload's `external_usage_id` marker.

**Deviation from the task's literal "unit test with a fake store," with rationale.**
`application.Service.store` is a concrete `*postgresstore.Store`, not an interface — every other
test in this package that exercises `RunOnce` (including the two pre-existing ones this slice's
integration test is modeled on) uses the real integration store, because there is no seam to fake
it through. Introducing one (a store interface, or a func-field abstraction over
`MarkSourceEventFailed` specifically) would be a structural change well beyond a logging feature
for a production incident fix. Delivered instead: a genuine no-DB unit test against the extracted
logging helper (satisfies "unit test... asserting the log line is emitted" in full), plus a real
end-to-end integration test proving the wiring into `RunOnce` is correct — arguably stronger
verification than a fake store would have given, since it also proves the log fires at exactly the
right moment in the real control flow. Flagged here explicitly per the task's "anything you could
not verify [as literally specified]."

## Requirement 3: repair tool (`993ea8f`)

New store method `RepairPreAnchorUsageEligibility` (`internal/postgresstore/eligibility_repair.go`)
and CLI `backend/cmd/eligibility-repair` (built into the Dockerfile's existing `tools` image stage
alongside the other maintenance binaries — `backend/Dockerfile`, both the `build` stage's binary
list and the `tools` stage's `COPY` line).

### Scope (why the predicate is exclusive to this incident, not just "probably fine")

- **`SOURCE_GAP` freezes selected:** `status='open' AND freeze_reason='SOURCE_GAP' AND
  trigger_object_type IN ('usage','credit') AND` the account's `bootstrap_kind='POLICY_ANCHOR'`.
  Verified (by reading every `freezeSourceAccountsTx`/`freezeEligibilityTx` call site with reason
  `SOURCE_GAP`) that `trigger_object_type IN ('usage','credit')` is produced by exactly one code
  path in the whole codebase — `observeEligibilityFact`'s freeze at the line requirement 1 fixed.
  The other three `SOURCE_GAP` call sites use `trigger_object_type` `balance_snapshot_cycle`,
  `source_stream`, or `balance_checkpoint`/`balance_carry_forward_proof` — never `usage`/`credit`.
  Combined with the `bootstrap_kind` filter, this predicate can only ever match freezes this
  specific bug produced; a legacy account's real `SOURCE_GAP` freeze (same object types, but
  `bootstrap_kind` is `SIGNED_CUTOVER`/`POST_CUTOVER_REPLAY`) is never selected.
- **`EVENT_DEAD` freezes selected:** further scoped to accounts already identified by a
  `SOURCE_GAP` freeze above (this mechanism is general-purpose — design `XM-INV-POLICY-ANCHOR`
  2.5 — and can freeze for reasons unrelated to this incident even on a `POLICY_ANCHOR` account, so
  `bootstrap_kind` alone is not a safe-enough filter here).
- **`source_ingest_events` requeued:** dead or failed `usage_event`/`credit_event` rows whose
  `payload_hash` matches a `source_revision_hash` collected from a freeze this run is resolving —
  the same content-hash correlation `MarkSourceEventFailed`'s own dead-branch already relies on
  (`source_sync.go`). The ingest layer stores no plaintext account reference at all (the payload is
  encrypted ciphertext until an application-layer attempt decrypts it), so this correlation is the
  only way to attribute an ingest row to an account without adding a keyring dependency to the
  repair tool. **Known limitation:** a merely-`failed` (not yet `dead`) event for an account whose
  *only* freeze so far is on a *different* fact has no freeze to correlate against yet and will not
  be requeued by this tool — it is not stuck, though: it retries on its own schedule (already set
  by the pre-existing failure path, at most 5 minutes out) and the now-fixed projection rule
  handles it correctly the moment it does. See Risks below.

### Mechanics

Everything above runs inside one `Serializable` transaction, rows locked with `FOR UPDATE` at
selection time. `--apply` (requires `--operator-id`, an admin UUID) resolves each freeze with the
exact column shape the manual admin resolution path (`ResolveEligibilityFreeze`) writes
(`status`/`resolved_at`/`resolved_by`/`resolution_evidence_hash`/`resolution_evidence_ciphertext`/
`resolution_note_hash`/`resolution_note_ciphertext`/`resolution_version`) and the same
`"eligibility.freeze.resolved"` audit action (object type `eligibility_freeze`), with the fixed
resolution note **"pre-anchor usage fact skipped by XM-INV-PREANCHOR-USAGE repair"** encrypted
under the CLI's field keyring (used for both the evidence and note ciphertext columns — an
automated repair has no human-authored justification distinct from its own reason). Every UPDATE
re-asserts its **full** original selection predicate in its own `WHERE` clause (not just the row
id); a `RowsAffected()!=1` on any one of them aborts the whole transaction with nothing written —
this is the "refuses to apply if any selected freeze does not match the predicate" requirement,
implemented as an all-or-nothing transaction rather than a partial best-effort apply. Once an
account's last open freeze is resolved, its `eligibility_status` flips back to `active`, a fresh
`eligibility_projection_jobs` row is queued (mirroring `ResolveEligibilityFreeze`'s own
reactivation logic exactly), and a per-account summary audit row
(`eligibility.policy_anchor.pre_anchor_usage_repaired`) is written — this last action name is a
deliberate addition beyond "the same audit rows the manual path writes" (which has no per-run,
multi-freeze summary concept), for full traceability of a bulk/automated operation; flagged here in
case a narrower reading was intended. `--dry-run` (no flag; the default) runs every one of the
above selection queries and prints the identical summary, then rolls back unconditionally.

### CLI

```
eligibility-repair \
  --database-url-file=/path/to/db-url-secret \
  --field-keyring-file=/path/to/keyring.json \
  --migrations-dir=/app/migrations \
  [--apply --operator-id=<admin-uuid>]
```

Follows `cmd/backup-verify`'s layout exactly: absolute-path flags, one-line secret file reader,
`migrate.Verify` against the live schema before touching any data (refuses to run against a
database whose migration set does not match the bundled `migrations/` — a stale binary cannot
silently run partial/wrong SQL). Output (both modes print the same table shape; sample captured
from a real run against a seeded fixture matching the incident's shape, see Tests run below):

```
eligibility-repair XM-INV-PREANCHOR-USAGE: DRY RUN (nothing was changed)

ACCOUNT                                   SRC_GAP   EVT_DEAD   REQUEUED REACTIVATED
30000000-0000-4000-8000-000000000250            2          1          3       false

TOTAL                                           2          1          3

accounts affected: 1
```

```
eligibility-repair XM-INV-PREANCHOR-USAGE: APPLIED

ACCOUNT                                   SRC_GAP   EVT_DEAD   REQUEUED REACTIVATED
30000000-0000-4000-8000-000000000250            2          1          3        true

TOTAL                                           2          1          3

accounts affected: 1
```

A second `--apply` run immediately afterward reports `accounts affected: 0` (idempotent — verified
both by the integration test and by re-running the real binary against the same seeded database).

### Production runbook

**Do not run this anywhere but the test database from this task** — the team lead's explicit
instruction. The following is the intended production sequence for whoever runs it later, once
this branch is released:

1. Fresh backup first, per the standing release/ops discipline (unrelated to this tool, but always
   precedes any production data-mutating operation on this system).
2. Deploy the release containing requirement 1's projection fix (`299548b`) — this must be live
   *before* running the repair, so that once events are requeued and freezes cleared, the
   corrected projection code is what actually re-evaluates the account, not the still-buggy one.
3. `eligibility-repair --database-url-file=... --field-keyring-file=... --migrations-dir=...` (no
   `--apply`, i.e. dry run). Expect roughly **106 SOURCE_GAP freezes resolved, 100 EVENT_DEAD
   freezes resolved, 102 events requeued** (100 dead + 2 failed), matching the incident's own
   counts (2026-09-02 audit trail, this doc's Incident recap above) — treat these as the expected
   order of magnitude to sanity-check against, not a guaranteed exact match: production's real
   state may have shifted since the incident's own snapshot (further retries, an operator's manual
   intervention, etc.). Read the per-account table before proceeding; confirm no unexpected account
   appears (every listed account should be one of the 106 bootstrapped that morning) and that the
   totals are in the expected range.
4. Get the dry-run output reviewed/approved by whoever is authorizing the production change (this
   is the "human-approved" part of "a versioned lifecycle operation").
5. `eligibility-repair ... --apply --operator-id=<the approving operator's admin UUID>`. Confirm the
   summary table matches the dry run's counts and every listed account shows `REACTIVATED=true`
   (an account with an unrelated freeze still open after this run's freezes clear would show
   `false` and needs separate investigation — expected to be rare to nonexistent for this
   incident's population, but the tool does not assume it away).
6. Re-run the dry run once more (no `--apply`): expect `accounts affected: 0`.
7. Confirm readiness recovers: `cmd/api/runtime.go`'s `validateSourceIngestRuntimeReadiness` fails
   closed (`"source ingestion contains dead events"`) whenever `SourceIngestHealth.Dead > 0`, and
   nothing else in this slice touches that check. `--apply`'s requeue step moves every matched dead
   event straight to `'queued'`, which zeroes `Dead` immediately — readiness should recover as soon
   as the apply step completes, **not** only after the background worker has actually reprocessed
   the requeued events (that reprocessing still needs to happen, and will succeed cleanly under the
   now-fixed projection rule, but it is not what readiness is gating on). Watch `/readyz` (or the
   equivalent production health check) return 200 within a few seconds of the apply step finishing;
   if it does not, `Dead` is still nonzero and step 3's predicate did not catch everything currently
   dead — investigate before assuming the incident is closed.

## Files changed

- `backend/internal/postgresstore/consumption.go` — `observeEligibilityFact`'s `SOURCE_GAP` branch
  (requirement 1).
- `backend/internal/postgresstore/preanchor_usage_integration_test.go` (new) — requirement 1's four
  tests.
- `backend/internal/application/source_processor.go` — `SourceEventProcessor.Logger`/`logger()`,
  `logProjectionFailure`, the new call site in `RunOnce` (requirement 2).
- `backend/internal/application/source_processor_test.go` — requirement 2's two pure unit tests.
- `backend/internal/application/service_integration_test.go` — requirement 2's one integration test.
- `backend/internal/postgresstore/eligibility_repair.go` (new) — `RepairPreAnchorUsageEligibility`
  and its supporting types (requirement 3).
- `backend/internal/postgresstore/eligibility_repair_integration_test.go` (new) — requirement 3's
  fixture and four tests.
- `backend/cmd/eligibility-repair/main.go` (new), `backend/cmd/eligibility-repair/main_test.go`
  (new) — the CLI and its wiring tests (requirement 3).
- `backend/Dockerfile` — new binary added to the `build` stage's build list and the `tools` stage's
  `COPY` line (requirement 3's "follow the tools image conventions").
- `docs/superpowers/specs/2026-09-02-invoice-policy-anchor-design.md` — status line and new
  section `2.6` documenting this gap and its fix (requirement 4).
- `docs/handoffs/XM-INV-PREANCHOR-USAGE.md` — this file (requirement 4).

**Not touched:** any other migration file (no `0018` was created — see requirement 1 above);
`scripts/release-image-gate-lib.ps1`; `test-release-image-gate.ps1`;
`verify-release-image-artifacts.ps1`; `RELEASE-READINESS.md`; `docs/PRODUCTION-RUNBOOK.md`;
`docs/IMAGE-SCAN-REVIEW.md`; `docs/superpowers/plans/`; any RC version identity; `contracts/`; no
tags created.

## Tests run

From `backend/`, `GOFLAGS=-buildvcs=false`, the eight proxy variables unset (`HTTP_PROXY`,
`HTTPS_PROXY`, `http_proxy`, `https_proxy`, `ALL_PROXY`, `all_proxy`, `NO_PROXY`, `no_proxy` — the
known Windows/this-machine requirement, see internal notes; without this, tests using
`httptest.NewTLSServer` fail deterministically, unrelated to this slice), and
`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_preanchor?sslmode=disable`
(a dedicated database created for this task in the already-running `invoice-test-pg` container,
separate from other agents' databases):

```
go build ./...                 # exit 0
go vet ./...                   # exit 0
go test -p 1 -count=1 ./...    # all packages ok, see below
```

Full package list, all passing (run together, then the touched packages re-run individually
multiple times across this session): `cmd/api`, `cmd/archive-verify`, `cmd/backup-verify`,
`cmd/bootstrap-settings`, `cmd/bootstrap-sources`, `cmd/eligibility-repair` (new),
`cmd/keygen`, `cmd/migrate`, `cmd/mtlsgen`, `cmd/oidc-preflight`, `cmd/pdf-policy-check`,
`cmd/pdf-scanner`, `internal/adminsettings`, `internal/application` (includes this slice's new
`TestLogProjectionFailure*` and `TestSourceProjectionWorkerLogsProjectionFailure`), `internal/auth`,
`internal/backuparchive`, `internal/backupverify`, `internal/document`, `internal/domain`,
`internal/httpapi`, `internal/ledger`, `internal/mailer`, `internal/migrate`,
`internal/oidcretention`, `internal/pdfscanner`, `internal/postgresstore` (includes this slice's
new `TestPolicyAnchorAccountPreCutoverUsageFactSkippedWithoutFreeze` and siblings, and
`TestRepairPreAnchorUsageEligibility*`; full package run ~85–98s across repeated runs),
`internal/securefields`, `internal/sourceingest`.

New tests added by this slice (13 total, all passing individually and as part of the full run,
multiple times across this session):
- `TestPolicyAnchorAccountPreCutoverUsageFactSkippedWithoutFreeze`,
  `TestPolicyAnchorAccountPreCutoverCreditFactSkippedWithoutFreeze`,
  `TestPolicyAnchorAccountPostCutoverUsageFactAllocatesNormally`,
  `TestLegacyAccountPreCutoverUsageFactStillFreezesSourceGap` (postgresstore)
- `TestLogProjectionFailureLogsSourceStreamEventAndErrorNotPayload`,
  `TestLogProjectionFailureDefaultsToSlogDefaultWithoutPanicking` (application, pure unit)
- `TestSourceProjectionWorkerLogsProjectionFailure` (application, integration)
- `TestRepairPreAnchorUsageEligibilityDryRunReportsWithoutMutating`,
  `TestRepairPreAnchorUsageEligibilityApplyResolvesRequeuesAndReactivates`,
  `TestRepairPreAnchorUsageEligibilityApplyRequiresOperatorAndEvidence` (postgresstore)
- `TestRunDryRunAgainstEmptyDatabaseReportsNothing`, `TestRunApplyWithoutOperatorIDIsRejected`,
  `TestPrintSummaryFormatsAccountsAndTotals` (cmd/eligibility-repair)

Beyond the automated suite: the compiled `eligibility-repair` binary was run for real (not just
its Go tests) against a database seeded with a fixture mirroring the incident's exact shape (a
`POLICY_ANCHOR` account with 2 `SOURCE_GAP` + 1 `EVENT_DEAD` freeze and 3 dead/failed ingest
events, plus an untouched legacy account) — dry run, apply, and a second dry run confirming zero
remaining work, with the database state independently checked via `psql` between steps (the legacy
account's freeze stayed `open` and its `eligibility_status` stayed `frozen` throughout). The sample
CLI output in this doc's Production runbook section is captured verbatim from that run, not
hand-written.

`gofmt`: every file this slice touches or adds was checked with
`"$(go env GOROOT)/bin/gofmt" -l/-d` after stripping the repo's pre-existing CRLF line endings
(`sed 's/\r$//'`) — the documented false-alarm mode for this repo, where `gofmt -l` on an untouched
CRLF file reports a spurious whole-file diff with an identical line count. After stripping: every
new/touched file is clean, with one pre-existing, unrelated misalignment surfaced in
`consumption.go` at its `balanceProofPendingBackoffCapSeconds`/`balanceProofPendingRequeueResetWindow`
`const` block (confirmed present in the base commit `43bdc59`, nowhere near this slice's edit) —
left untouched, out of scope. Did not run `gofmt -w`/`go fmt` repo-wide.

`gitleaks protect --staged` — clean on all three commits (no leaks found).

`pwsh -NoProfile -File scripts/test-release-image-gate.ps1` — see below.

## Not run

- Anything requiring a server/production connection — explicitly out of scope for this task ("do
  not run it anywhere but the test database").
- `internal/auth`'s loopback-timeout-sensitive tests were run as part of the full suite (passed);
  no isolation re-run was needed this session (no flake observed).

## Risks / things to sign off on

1. **The `source_ingest_events` requeue scope has one known gap** (documented above, in the
   requirement-3 Scope section): a merely-`failed` (not yet `dead`) event for an account with no
   *own* correlating freeze yet is not requeued by this tool. It self-heals on its own existing
   retry schedule (worst case ~5 minutes) once the projection fix (requirement 1) is deployed, so
   this is a completeness gap in the tool's coverage, not a correctness or data-safety risk — but
   worth the team lead's explicit sign-off, since "expected counts 106/100/102" in the runbook
   could read as a stronger completeness guarantee than the tool actually provides for that edge.
2. **The per-account summary audit action
   (`eligibility.policy_anchor.pre_anchor_usage_repaired`) is a deliberate addition** beyond
   literally reusing only the manual path's `eligibility.freeze.resolved` shape — flagged in the
   requirement-3 Mechanics section above with its rationale. Easy to remove if a narrower audit
   footprint is preferred.
3. **Requirement 2's "unit test with a fake store"** was not literal — `application.Service.store`
   is a concrete type with no interface seam; delivered a pure unit test of the extracted logging
   helper plus a real integration test of the full wiring instead. See the requirement-2 section
   above for the full reasoning. Worth a second look if a fake-store-shaped test specifically (not
   just equivalent coverage) is required for other reasons (e.g. a project-wide testing-style
   preference this task's instructions didn't otherwise surface).
4. **`RepairPreAnchorUsageEligibility` was not run against a restored production backup** — this
   task had no server/production access, matching the explicit instruction. The design doc's
   existing verification-gate item 3 (rehearsal on an isolated restore, from
   `XM-INV-POLICY-ANCHOR`) already flags this class of gap as a pre-production-rollout follow-up;
   this repair tool should be exercised the same way (against a restore, or at minimum against a
   copy of the actual current incident state) before the production runbook above is executed for
   real.
5. No new float amounts, no logged/persisted secrets (the CLI's own log calls never include
   ciphertext or decrypted payload contents — checked), no `contracts/` changes, no admin-OIDC
   changes, no touch to any release-identity file, no tags created, no touch to any file outside
   this slice's stated scope — checked.

## Follow-ups (recommended, not blocking this delivery)

1. Close requirement 3's known requeue-scope gap (risk 1 above) if production experience shows it
   matters in practice — would need either a lightweight decrypt-to-attribute step (adding a
   keyring dependency to the repair tool) or a broader, less-precise scoping tradeoff.
2. Wire `eligibility-repair` into whatever operational tooling/alerting already exists for the
   other maintenance binaries in the `tools` image (this task added it to the Docker build only,
   per "follow... the tools image conventions" — it did not add any deployment/invocation
   automation beyond that, since none of the sibling tools in that stage have any either).
3. Consider adding a dedicated readiness/alert check specifically for "any open `SOURCE_GAP` freeze
   on a `POLICY_ANCHOR` account with `trigger_object_type IN ('usage','credit')`" so a *future*
   design gap of this same shape (a new bootstrap kind interacting badly with an existing freeze
   rule) surfaces as a targeted alert rather than only as a generic dead-event readiness failure.
