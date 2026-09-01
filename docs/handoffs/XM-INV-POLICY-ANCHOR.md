# XM-INV-POLICY-ANCHOR: anchor ledgers at the invoice policy start

- **status:** implemented and self-tested locally for design sections 2.1, 2.2, 2.3, and 2.5 in
  full, plus migration `0016`. Section 2.4 is partially implemented: the freeze-and-continue
  fallback helper is written and tested in isolation, but is **not wired into the ordinary
  reprojection job path** because the design's own trigger condition
  (`cutover_at < PolicyStartAt`) turns out to be true for essentially every bootstrapped account
  by construction, not a genuine staleness signal — wiring it in as specified started freezing
  healthy, unrelated accounts. This was reported to the team lead mid-implementation and is an
  open question at the time of this commit; see 2.4's section below and the design doc's own
  implementation note for the exact finding.
- **branch:** `ai/claude/XM-INV-POLICY-ANCHOR` (based on `ai/claude/XM-INV-AUTOLOGIN` at
  `41edb5c`), worktree `K:/发票/wt-XM-INV-ANCHOR`.
- **commit:** see `git log` on this branch. This is a second commit on top of the slice's first
  (schema-blocked, partial) delivery, after the team lead reviewed that blocker and authorized
  migration `0016` with an exact specification (see the design doc's `2.0` section for what was
  authorized vs. the one deviation implementation required).

## Summary

This picks up after an initial delivery that found `bootstrap_kind='POLICY_ANCHOR'` blocked by
the schema (no such CHECK-constraint value, no migration permitted) and shipped only the parts of
the design that didn't need it. The team lead reviewed that finding, authorized a scoped
migration (`backend/migrations/0016_policy_anchor.sql`), and gave an exact specification for its
three parts. What follows is what actually shipped against that specification, section by
section of the original design.

**2.1 (postgresstore/consumption.go, `ObserveBalanceCheckpoint`) — now implemented in full.** A
baseline member's reconciliation checkpoint with no eligibility state yet: before the policy
start, ignored (as in the first delivery); at/after the policy start, now bootstraps directly
(`bootstrap_kind='POLICY_ANCHOR'`, `cutover_at := checkpoint.as_of`,
`cutover_balance_units := checkpoint.balance_service_units`, entirely non-invoiceable — the same
treatment `SIGNED_CUTOVER` gives its opening balance). The state row is inserted **before** the
checkpoint row (checkpoint-kind `reconciliation`, `baseline_member=TRUE`,
`reconciliation_status='cutover_baseline'`): the checkpoint table's own (unchanged, immediate)
`balance_reconciliation_checkpoints_contract_guard` trigger requires a trusted state row to
already exist, while the state row's own new `POLICY_ANCHOR` validation (migration `0016`) is
deferred to `COMMIT` specifically so it can, in turn, require this same checkpoint row to exist
by then — see migration `0016`'s own comment and the design doc's `2.0` section for the full
ordering-deadlock explanation. A baseline member's checkpoint arriving after the account has
already bootstrapped some other way still hits the existing "state already exists → `ErrConflict`"
guard, unchanged.

**2.2 (postgresstore/source_sync.go, `RequeueSourceDependency`) — unchanged from the first
delivery, implemented in full.**

**2.3 (postgresstore/consumption.go, `buildEligibilityProjectionTx`) — unchanged from the first
delivery: no code change needed, assertion test present,** now additionally exercised end-to-end
by the rewritten 2.1 test (a real `POLICY_ANCHOR` account's post-anchor usage/credit projection).

**2.4 (one-time re-anchoring of already-bootstrapped accounts) — partially implemented, not
wired in.** Two problems found, in order:

1. The DELETE-and-reinsert mechanism the design calls for cannot run inside
   `processEligibilityProjectionJob` as written. `eligibility_projection_jobs.external_account_id`
   (the job's own row, necessarily present and locked for the account being processed) and
   `source_account_stream_watermarks.external_account_id` both carry non-deferrable
   `ON DELETE RESTRICT` foreign keys to `source_account_eligibility_state`. Per instruction, this
   was not forced through — implemented instead as freeze-and-continue:
   `flagPolicyAnchorMigrationBlockedTx` (`postgresstore/consumption.go`) detects the condition,
   freezes the account (new reason `POLICY_ANCHOR_BLOCKED`, migration `0016`) with an audit event
   recording the would-be migration's before/after values, and lets the normal (unchanged-boundary)
   reprojection continue — the ledger math is unaffected either way; freezing only blocks *new*
   invoice activity until a human resolves it.
2. Wiring that helper into `processEligibilityProjectionJob` (right after
   `getEligibilityAccountTx`, before `ensureBalanceCarryForwardProofTx`, as instructed) and
   running the full suite revealed `cutover_at < PolicyStartAt` is not a distinguishing
   condition: `RegisterCutoverManifest` and a DB trigger both hard-require
   `manifest.cutover_at < policy_start` for *every* real manifest, and both `SIGNED_CUTOVER` and
   `POST_CUTOVER_REPLAY` set `cutover_at := manifest.cutover_at` — so this is true for essentially
   every bootstrapped account there will ever be, including a brand-new `POST_CUTOVER_REPLAY`
   account bootstrapping tomorrow under perfectly normal operation. Wired in as specified, it
   started freezing `TestV3FinalizedUsagePublishesConsumedCashAndAllowsPartialInvoices`'s
   completely healthy `SIGNED_CUTOVER` account (cutover 2 hours before that test's policy start —
   ordinary fixture timing, not staleness). **Reverted the call site** rather than ship a
   financial freeze condition that fires on every account; reported to the team lead and is
   awaiting a decision (candidates: run this as a true one-time backfill over the account
   population that existed at deploy time rather than logic in the ordinary per-job path; find a
   real distinguishing condition, if one exists, that isn't `cutover_at < PolicyStartAt` alone;
   or drop 2.4 from scope).

The helper itself is implemented and has its own dedicated test proving its freeze+audit
behavior is correct in isolation (see Tests run below) — it just isn't reachable from normal
operation yet.

**2.5 (postgresstore/consumption.go `tryPublishEconomicScanCyclesTx`; postgresstore/source_sync.go
`MarkSourceEventFailed`) — now implemented in full.** The already-frozen-account half is
unchanged from the first delivery. The auto-freeze half is new:
`MarkSourceEventFailed` now runs in a transaction; when an event transitions to `dead`, it
attempts to correlate the dead event to an account via `source_revision_hash` matching against
`balance_reconciliation_checkpoints`/`source_usage_events`/`source_credit_events` (whichever
table, if any, actually persisted a fact with that exact content hash on some earlier attempt —
the same correlation the cycle-completeness query already uses for an *existing* freeze). When
found, freezes that account with the new reason `EVENT_DEAD` (migration `0016`) and re-attempts
cycle publication immediately. **This is necessarily a partial fix for the production incident
the design's Problem section describes**: a dead event whose underlying domain write never
succeeded on any attempt (the likely shape of a pure transient/Serializable-conflict failure, and
plausibly what actually holds scan cycle `5b5c26bc`) leaves no fact row to correlate through —
there is no ingest-layer column identifying which account an encrypted, unprocessed event belongs
to, so this remains structurally unresolvable without decrypting payloads at this layer (out of
scope, and not something I considered safe to add unilaterally). Such an event still holds its
cycle, same as before this slice.

## Migration 0016

`backend/migrations/0016_policy_anchor.sql`, authorized and specified by the team lead (see the
design doc's `2.0` section for the full text of what was authorized and the one deviation
implementation required — the cutover-boundary trigger becoming a `DEFERRABLE INITIALLY DEFERRED`
`CONSTRAINT TRIGGER`, same pattern already used for the analogous `funding_lots` /
`funding_lot_consumption_state` mutual check). Three parts:
1. `source_account_eligibility_state_bootstrap_kind_check` extended to include `'POLICY_ANCHOR'`.
2. `enforce_account_eligibility_cutover_contract()` gained a `POLICY_ANCHOR` branch requiring (a)
   `cutover_at >= invoice_eligibility_policy.eligibility_start_at` and (b) a matching
   `balance_reconciliation_checkpoints` row exists (`checkpoint_kind='reconciliation'`,
   `as_of=cutover_at`, `balance_service_units=cutover_balance_units`, `unit_code` match) — plus
   the existing manifest-hash check, unchanged. Its trigger became a deferred constraint trigger
   (see above).
3. `eligibility_freezes.freeze_reason` extended with `'EVENT_DEAD'` (design 2.5) and
   `'POLICY_ANCHOR_BLOCKED'` (design 2.4's fallback — not in the original three-item list; added
   because 2.4's fallback needed it, flagged in the design doc's implementation note).

Verified empirically with a throwaway scratch Go program before touching real code (5 scenarios:
valid state-then-checkpoint bootstrap succeeds; mismatched balance, wrong `cutover_at`, and no
matching checkpoint at all each correctly rejected at `COMMIT`; deleted before committing, not
part of this diff), then via the dedicated migration test (below) and the full existing suite
(no regression in the `SIGNED_CUTOVER`/`POST_CUTOVER_REPLAY` paths, which share the same trigger).

Registered in `backend/internal/migrate`'s own tests the way earlier migrations with the same
dependency shape are: added to `TestConsumptionMigrationClosesPreCutoverReservationsAndPreservesIssuedExposure`'s
exclusion list (it alters `source_account_eligibility_state`/`eligibility_freezes`, both created
by `0009`, which that test's first phase deliberately excludes — same reason `0010`-`0012` are
already excluded there).

## Files changed (this commit, on top of the first delivery's diff)

- `backend/migrations/0016_policy_anchor.sql` — new.
- `backend/internal/postgresstore/consumption.go` — `ObserveBalanceCheckpoint`'s baseline-member
  branch now bootstraps `POLICY_ANCHOR` instead of returning `ErrSourceUnavailable`; new
  `flagPolicyAnchorMigrationBlockedTx` (written, not called from `processEligibilityProjectionJob`
  — see 2.4 above).
- `backend/internal/postgresstore/source_sync.go` — `MarkSourceEventFailed` rewritten to run in a
  transaction with the dead-event `EVENT_DEAD` auto-freeze correlation and cycle-republish retry.
- `backend/internal/postgresstore/policy_anchor_integration_test.go` — the first delivery's
  pre-policy-ignore test extended into a full bootstrap test (renamed
  `TestReconciliationCheckpointIgnoredPrePolicyAndBootstrapsPolicyAnchorPostPolicy`); new
  `TestFlagPolicyAnchorMigrationBlockedFreezesWithCandidateAndIsNoOpWithoutOne` (2.4's helper,
  tested in isolation).
- `backend/internal/postgresstore/consumption_integration_test.go` —
  `TestPostCutoverNewAccountReplaysFromGlobalCutoverAndBlocksSubscriptionWithoutUsage`'s leading
  "baseline member must wait forever" guard removed: that was asserting the exact old behavior
  2.1 now replaces (a baseline member's post-policy checkpoint bootstraps directly instead of
  waiting), and the account it used happened to fall into that now-changed case. The test's own
  actual subject (a genuinely new, non-baseline account's `POST_CUTOVER_REPLAY` bootstrap and
  downstream subscription/wallet/usage ordering) is unaffected and unchanged.
- `backend/internal/migrate/migrate_test.go` — `0016_policy_anchor.sql` added to
  `TestConsumptionMigrationClosesPreCutoverReservationsAndPreservesIssuedExposure`'s exclusion
  list; new `TestPolicyAnchorMigrationValidatesBootstrapKindAndTriggerBoundary`.
- `docs/superpowers/specs/2026-09-02-invoice-policy-anchor-design.md` — new `2.0` section
  recording the migration-0016 decision and its rationale (as instructed); implementation notes
  added to `2.4` (the open question) and `3` (actual verification-gate status).
- `docs/handoffs/XM-INV-POLICY-ANCHOR.md` — this file, rewritten.

**Not touched (still):** any other migration file, `release/`, `scripts/`,
`RELEASE-READINESS.md`, `docs/PRODUCTION-RUNBOOK.md`, `docs/IMAGE-SCAN-REVIEW.md`,
`docs/superpowers/plans/`, any RC version identity, `contracts/`.

## Tests run

From `backend/`, `GOFLAGS=-buildvcs=false`,
`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test?sslmode=disable`:

```
go build ./...                 # exit 0
go vet ./...                   # exit 0
go test -p 1 -count=1 ./...    # see below
```

New/changed in `backend/internal/postgresstore/policy_anchor_integration_test.go`:
- `TestReconciliationCheckpointIgnoredPrePolicyAndBootstrapsPolicyAnchorPostPolicy` (renamed,
  extended) — pre-policy checkpoint ignored (unchanged from the first delivery); post-policy
  checkpoint now bootstraps `POLICY_ANCHOR` (state row `bootstrap_kind`/`cutover_at`/
  `cutover_balance_units` correct, checkpoint row `baseline_member`/`reconciliation_status`
  correct, bootstrap audit event present); the account then correctly processes a subsequent
  credit fact via a normal projection (`ExpectedBalance` reflects only the post-anchor credit,
  confirming the anchor's own balance is opening/non-invoiceable, not double-counted).
- `TestRequeueSourceDependencySkipsPrePolicyUsageAndBalanceBacklog` — unchanged.
- `TestEligibilityProjectionNeverAllocatesPreAnchorUsageFact` — unchanged.
- `TestScanCycleFrozenDeadEventPublishesWhilePureTransientDeadEventHoldsCycle` — unchanged
  (already covered the frozen-excludes-the-cycle half from the first delivery).
- `TestFlagPolicyAnchorMigrationBlockedFreezesWithCandidateAndIsNoOpWithoutOne` (new) — an
  account with a candidate post-policy checkpoint gets frozen (`POLICY_ANCHOR_BLOCKED`, open,
  idempotent across a second call) with an audit event recording before/after values; an account
  with no candidate is a no-op (no freeze, `eligibility_status` stays `active`).

New in `backend/internal/migrate/migrate_test.go`:
- `TestPolicyAnchorMigrationValidatesBootstrapKindAndTriggerBoundary` — `POLICY_ANCHOR` rejected
  pre-migration (CHECK constraint); post-migration, a matching state-then-checkpoint bootstrap
  succeeds and is stored correctly; a claimed balance with no matching checkpoint at all is
  rejected at `COMMIT` with "policy anchor boundary is invalid".

Modified in `backend/internal/postgresstore/consumption_integration_test.go`:
`TestPostCutoverNewAccountReplaysFromGlobalCutoverAndBlocksSubscriptionWithoutUsage` (see Files
changed above).

`internal/postgresstore` (56 tests incl. the above), `internal/application`, and
`internal/migrate` (incl. the new migration test) all pass, run together and individually,
multiple times across this session. `go build`/`go vet` clean throughout.

**Flakiness encountered and diagnosed, not a regression:** twice during this session, a full
`./...` run showed a handful of unrelated tests (`internal/auth`'s OIDC/postgres tests once;
`internal/postgresstore`/`internal/migrate` setup helpers reporting "relation ... does not exist"
a second time, different tests failing each time) that were not reproducible on an immediate,
isolated re-run with no other load on the shared `invoice-test-pg` container — confirmed
transient (container uptime never changed across runs; connection count and resource usage were
normal when checked). This matches a known class of issue in this environment (intermittent
Docker Desktop / Windows host-to-container port-forwarding instability), not anything in this
diff. `internal/document`'s `TestClamAVScannerStreamsDocument` also intermittently fails/passes
depending on whether a ClamAV daemon happens to be reachable in this environment — unrelated,
untouched package.

`gofmt` (`go env GOROOT` copy, not the stale PATH one) is clean on every file this commit
touches — the pre-existing-file diffs `gofmt -l .` shows repo-wide are the known CRLF-checkout
false positive (verified empty after stripping `\r`), not real formatting issues; files newly
written by this session's Write tool are LF already and match directly.

## Not run

- Anything requiring a server/production connection.
- The rehearsal-on-a-backup-restore verification gate (design section 3 item 3) — out of scope
  for this worktree/task.
- Real production data for 2.4's decision — the "candidates" listed above are my best read of the
  gap, not verified against production account population shape.

## Risks / things to sign off on

1. **2.4 is not wired in.** No re-anchor migration and no freeze-and-continue fallback actually
   run today for any account — `flagPolicyAnchorMigrationBlockedTx` exists and is tested in
   isolation but has no caller. This is a known, reported gap awaiting the team lead's decision,
   not an oversight.
2. **2.5's `EVENT_DEAD` auto-freeze only catches events whose underlying fact was persisted on
   some earlier attempt.** A pure repeated-transient failure that never wrote anything — the more
   likely shape of the actual cited production incident — still cannot be attributed to a
   specific account from this layer, and such an event still holds its cycle. Flagged in both the
   design doc and here; not something I could resolve without either a schema addition beyond
   what was authorized or decrypting payloads in `postgresstore` (a much larger, unauthorized
   change).
3. **Migration 0016's `DEFERRABLE INITIALLY DEFERRED` conversion is a deviation from the team
   lead's literal migration content list**, made because condition (b) as specified is not
   satisfiable under the previous immediate-trigger semantics (see the design doc's `2.0` section
   for the full mechanical explanation). Verified against the full existing suite and with a
   dedicated throwaway scratch program before touching real code; still worth the team lead's own
   review at merge given it changes a security/trust-boundary trigger's timing semantics (not
   what it enforces).
4. **`POLICY_ANCHOR_BLOCKED` freeze reason is also a deviation** from the literal three-item
   migration spec (which named only `EVENT_DEAD`) — added because 2.4's freeze fallback needed a
   reason value and reusing an existing, semantically-mismatched one seemed worse than adding one
   to the same CHECK constraint already being touched. Also worth review at merge.
5. **`TestPostCutoverNewAccountReplaysFromGlobalCutoverAndBlocksSubscriptionWithoutUsage`'s
   removed block** was deliberately testing pre-XM-INV-POLICY-ANCHOR behavior (a baseline member
   must always wait) that this slice exists to eliminate — removing it rather than updating it in
   place was a judgment call (the alternative, positive-path coverage for that same input, is
   already provided by this file's own dedicated `POLICY_ANCHOR` bootstrap test), flagging in
   case a reviewer wants it restated differently.
6. No new float amounts, no logged/persisted secrets, no `contracts/` changes, no admin-OIDC
   changes, no touch to any file outside this slice's stated scope — checked.

## Follow-ups (recommended, not blocking my delivery of this task)

1. **Resolve 2.4's open question** (see above) and wire `flagPolicyAnchorMigrationBlockedTx` (or
   its eventual replacement) into whatever mechanism the team lead decides on.
2. **Resolve 2.5's remaining gap** if the actual production incident's dead event turns out to be
   the un-correlatable (pure transient, no persisted fact) shape — would need either a schema
   change beyond migration `0016`'s scope or an architecturally different approach (e.g.
   decrypting payloads at the point of freezing, which is a real design decision, not a small
   patch).
3. Once 2.4 is resolved, add the "re-anchor migration of a SIGNED_CUTOVER account" integration
   test the design's verification gate originally asked for (not deliverable until the mechanism
   itself is decided).
4. Re-run the design's rehearsal-on-a-restored-backup step (section 3, item 3) before any
   production rollout, once 2.4 is resolved and merged.
