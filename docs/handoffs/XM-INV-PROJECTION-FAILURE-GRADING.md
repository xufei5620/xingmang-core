# XM-INV-PROJECTION-FAILURE-GRADING: grade per-account projection failures instead of failing readiness immediately

- **status:** implemented and self-tested locally; gates below. Not merged, not pushed, not tagged.
- **branch:** `ai/claude/XM-INV-PROJECTION-FAILURE-GRADING` (based on `ai/claude/XM-INV-AUTOLOGIN` at
  `5cd525a`, RC79 line, in production), worktree `K:/发票/wt-XM-INV-PROJ-GRADE`.
- **commits:** see "Commits" below.

## Audit finding (read-only audit of RC79, 2026-09-03)

`ProcessEligibilityProjectionJobs` (`backend/internal/postgresstore/consumption.go`, then around
lines 2653-2663) marked any per-account error from `processEligibilityProjectionJob` other than
`errBalanceCarryForwardProofPending` `status='failed'` immediately: no attempt counter, no backoff,
no terminal grade. `EligibilityProjectionHealth` counted every `status='failed'` row into `Failed`,
and `eligibilityProjectionReady` (`backend/cmd/api/runtime.go`) turned `/readyz` 503 unconditionally
whenever `Failed>0`. A single account hitting a transient error -- a serialization failure, a
momentary database error, a one-off evaluator bug -- took the whole API not-ready until a human
intervened, with no self-healing path. This is the exact RC75 incident mechanism (see
`docs/handoffs/XM-INV-BLIP-SOFTFAIL.md`'s own account of that incident), now fixed at its structural
root rather than only for that one bug class.

The source-ingest path already had the right shape: `MarkSourceEventFailed`
(`backend/internal/postgresstore/source_sync.go`) retries with backoff and escalates to a terminal
`'dead'` status only after 8 consecutive failures, and readiness looks only at `Dead>0` for that
stream. This slice gives `eligibility_projection_jobs` the identical grading.

## Design and what changed

### 1. Migration `0023_projection_failure_grading.sql`

- Widens `eligibility_projection_jobs`'s `status` CHECK to add `'dead'`, keeping `'failed'` in the
  vocabulary for compatibility with any row already in that status at deploy time (the worker's claim
  query still picks those up -- unchanged -- and grades them going forward under the new rules).
- Adds `attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0)`: a new column, deliberately
  distinct from the pre-existing `attempt_count`, which the claim step's `UPDATE` increments on
  *every* claim regardless of outcome (proof-pending, success, or failure) and so cannot tell a
  genuine consecutive-failure streak from a job merely claimed many times while otherwise healthy.
- Adds `last_error TEXT` (bounded, 2000 chars): the actual Go error text, distinct from the
  pre-existing `last_error_code`'s short discriminator values (`'PROJECTION_FAILED'`,
  `'BALANCE_PROOF_PENDING'`, now also `'PROJECTION_DEAD'`), so an operator inspecting a dead job --
  or `invoice-eligibility-repair --kind=projection-requeue-dead`'s listing -- sees what actually
  happened.
- Added to `internal/migrate/migrate_test.go`'s reduced-fixture exclusion list
  (`TestConsumptionMigrationClosesPreCutoverReservationsAndPreservesIssuedExposure`), for the same
  reason 0016/0020/0022 are excluded there: it alters `eligibility_projection_jobs`, created by the
  already-excluded `0009`.

### 2. Per-account grading (`ProcessEligibilityProjectionJobs` / new `markEligibilityProjectionJobFailedOrDead`)

The per-account error branch (still per-account isolated, never aborting the batch -- unchanged) now
calls a new helper instead of unconditionally setting `status='failed'`:

- `attempts=attempts+1`.
- Below `projectionFailureDeadThreshold` (8, a named constant mirroring `source_ingest_events`' own
  `attempt_count>=8` threshold) consecutive failures: `status='queued'`, `last_error_code=
  'PROJECTION_FAILED'`, `last_error=<bounded Go error text>`, and `next_attempt_at` set from a named
  exponential schedule (`projectionFailureBackoffBaseSeconds`=30, doubling, capped at
  `projectionFailureBackoffCapSeconds`=1800 -- 30 minutes -- mirroring `balanceProofPendingBackoffCapSeconds`'s
  own style exactly).
- At the threshold: `status='dead'`, `last_error_code='PROJECTION_DEAD'`, and an audit event
  `eligibility.projection.dead` is written (account id, `attempts`, `last_error`, threshold) in the
  same transaction as the status flip.
- `errBalanceCarryForwardProofPending` keeps its own existing, unrelated backoff branch untouched --
  it never spends an `attempts` increment, confirmed by a dedicated test (see Tests below).
- A successful run still deletes the job row outright (unchanged, `processEligibilityProjectionJob`'s
  final `DELETE`) -- the account's next error, if any, starts a fresh `attempts=0` row. This *is* "a
  successful run resets attempts to 0," per the task brief, since there is no row left to carry a
  stale count.
- Every branch of the new helper returns a defined result: a lease mismatch (another process already
  reclaimed the row -- not expected under this function's serial, single-claim-per-batch design, but
  never treated as fatal) is silently ignored, matching the pre-existing `BALANCE_PROOF_PENDING`
  branch's own tolerance for that case; only a genuine database error propagates and aborts the whole
  `ProcessEligibilityProjectionJobs` call (same as before this slice -- a markErr, not a per-account
  processing error, is systemic).

### 3. `EligibilityProjectionHealth`: `Dead`, `Retrying`, and a widened `OldestPending` exclusion

- `Failed` renamed to `Dead` (readiness and the release-rehearsal shadow tool's `toReportHealth` were
  its only two non-test consumers, both updated) -- now `count(status='dead')`, the new terminal
  grade only. A job merely retrying with backoff is never terminal.
- New `Retrying`: `count(status='queued' AND attempts>0)` -- every job currently in the failure-grading
  retry ladder, due or not. Purely informational/operational, never a readiness input. Documented,
  intentional overlap with `ProofPending`: a job that failed before (`attempts>0`) and later separately
  hit a proof-pending outcome (which never resets `attempts`) legitimately counts in both.
- `OldestPending`'s exclusion (already covering `BALANCE_PROOF_PENDING` backoff with a 30-second
  worker-reclaim grace, and live-lease `processing` rows -- XM-INV-READY-PENDING/XM-INV-READY-LEASE)
  is widened to also exclude a queued job whose own failure-grading backoff has not elapsed (`attempts>0
  AND next_attempt_at` still in the future, same 30-second grace) -- a job legitimately scheduled
  forward is not "pending" for the 15-minute stuck-job rule, exactly like a proof-pending job.

### 4. Readiness (`cmd/api/runtime.go`)

`eligibilityProjectionReady`: `Dead>0` (renamed from `Failed>0`) or `OldestPending` past the existing
15-minute budget -- unchanged decision shape, renamed field. Two named reason-string constants
(`eligibilityProjectionDeadReason`/`eligibilityProjectionStuckReason`) replace the single generic
`"invoice eligibility projection is unhealthy"` string both branches previously shared, so an
operator reading `/readyz`'s error text (or application logs) can immediately tell "a job needs
`--kind=projection-requeue-dead`" apart from "the queue is not making progress." A retrying account
(`Retrying>0`, `Dead=0`) never makes the API not ready -- it was already excluded from `OldestPending`
by change 3 above, so no separate readiness-function change was needed for that guarantee.

### 5. `invoice-eligibility-repair --kind=projection-requeue-dead` (new `internal/postgresstore/projection_requeue_dead_repair.go`)

Same dry-run/apply/operator-id flow and one-account-per-transaction isolation as
`queue_narrow_repair.go`. Lists every dead job (optionally narrowed to one account with `--account`)
with its previous `attempts`, `last_error_code`/`last_error` and how long it has been dead; apply
resets `attempts=0`, `status='queued'`, clears `last_error`/`last_error_code`, sets
`next_attempt_at=now()`, and writes an audit event `eligibility.projection.requeued`. Unlike the
freeze-resolution repairs it never touches `eligibility_freezes` or an encrypted resolution
note/evidence -- there is nothing to resolve, only a job to requeue, matching
`policy-start-reanchor`'s own precedent for a repair kind with no freeze interaction.

### 6. A dead job must never silently revive (deviation surfaced during implementation, not in the brief's literal file pointers)

Auditing every writer of `eligibility_projection_jobs` (not just the lines the brief pointed at)
found two `ON CONFLICT(external_account_id) DO UPDATE` upserts -- `finalizeSourceAccountsTx`'s batch
requeue and `ObserveBalanceCheckpoint`'s single-account requeue, both in `consumption.go` -- that
unconditionally set `status='queued'` whenever a new fact for the account arrived, with no awareness
of the new `'dead'` status. Left unchanged, any ordinary new usage/credit/checkpoint fact for a dead
account would have silently revived its job with `attempts` unreset, defeating the entire point of a
terminal grade (an operator resolves it via the repair tool, a new fact revives it seconds later,
and, if the underlying condition is still broken, it silently redies without ever showing up as
"has failed before" again). Both upserts now preserve `status='dead'` (and its `next_attempt_at`/
`updated_at`) unconditionally when the existing row is already dead -- only `requested_through` still
advances, so the account's full backlog is picked up the instant it *is* requeued through the repair
tool. Every other repair tool's own unconditional `status='queued'` set (`balance-anchor`,
`balance-blip`, `queue-narrow`, `policy-start-reanchor`) was deliberately left untouched: those are
explicit, operator-approved interventions (apply + operator id), which this slice treats as a
legitimate revival path exactly like its own new `projection-requeue-dead` tool -- only *automatic*,
no-operator-involved revival is guarded against.

### 7. Audit finding 2: `evaluatePendingBalanceEvidenceTx`'s softfail path

The task brief's finding 2 flagged `countConsecutiveRebaselinedBlipsTx`'s bare `return streakErr` (a
database error surfacing as a plain retryable error, not a freeze) as something to confirm is
correct, not to change. Confirmed: turning an infrastructure error into a freeze here would
misclassify a transient problem as a genuine structural gap. No behavior change; a comment was added
at the exact line explaining that this grading slice (`markEligibilityProjectionJobFailedOrDead`) is
now the correct home for "this keeps failing," not a premature freeze.

### 8. `cmd/eligibility-shadow` (release-rehearsal tool)

- `ProjectionHealth.Failed` (the report's JSON shape) is now populated from
  `postgresstore.EligibilityProjectionHealth.Dead`, kept under its historical JSON key name
  deliberately -- `deploy/rehearsal/shadow-eval-lib.sh`'s `shadow_eval_has_errors` only reads
  `round_errors`/`failed_accounts`, never this field, so renaming the key would have been pure churn
  across `deploy/rehearsal/test-shadow-eval.sh`'s many fixture blocks for no behavioral benefit. Not
  touched: no JSON shape change, so no rehearsal static test needed updating.
  `deploy/rehearsal/test-shadow-eval.sh` verified unaffected (not run -- see "Not run" below -- but
  its fixtures reference only the `"failed"` key name, which is unchanged).
- `EligibilityShadowFailedJobs` (`internal/postgresstore/eligibility_shadow_report.go`) -- the
  per-account durable trace the shadow tool reads after a drain -- now queries `status='dead'`, not
  the legacy `'failed'` (which the worker no longer produces). `RoundErrors`/`HasProjectionErrors`'s
  verdict semantics are otherwise unchanged: they still come from `ProcessEligibilityProjectionJobs`'s
  own per-round `firstProcessingError`, unaffected by whether an error ultimately grades to
  queued-with-backoff or dead within the rehearsal's bounded round count.

### 9. Admin visibility: `GET /api/v1/admin/source-health`

There is no separate, already-existing "projection health" HTTP endpoint -- the brief's own premise
("expose Retrying and Dead in the admin source-health / projection health report the api already
serves") assumed one existed; the only admin-facing health report the API currently serves is
`GET /api/v1/admin/source-health` (the five-stream `SourceHealthReport`). This slice merges
`EligibilityProjectionHealth` into that same response as a new, additional `eligibility_projection`
object (`queued`/`processing`/`retrying`/`dead`/`proof_pending`/`oldest_pending`/`oldest_proof_pending`),
deliberately without changing the endpoint's existing top-level `ready` field's meaning (still purely
the five-source-stream readiness it always was) -- `eligibility_projection` is additional,
informational detail, not a second readiness signal folded into the same boolean. New
`OperationsService.EligibilityProjectionHealth` method (interface + `application.Service` wrapper +
`getSourceHealth` handler + `eligibilityProjectionHealthDTO`); the one concrete test double that
implements `OperationsService` fully (`fakeSourceFilterOperations`) got a `panic("unused in this
test")` stub, matching its own established pattern for every method its narrow tests do not exercise.
**Not done:** no admin console (`web/`) page surfaces this new field yet -- explicitly out of scope
for this backend-only slice (the brief's stated file restrictions do not mention `web/`, and no
sibling slice that has extended this same JSON response, e.g. XM-INV-AGENT-RESTART-GRACE's
`ActiveRescanUpdatedAt`, did frontend work either); flagged as a follow-up.

## Deviations from the brief, flagged explicitly

1. **Section 6 above** (the two `ON CONFLICT` upserts) is not one of the brief's named files/lines --
   found by auditing every writer of `eligibility_projection_jobs`, not just the pointed-at lines,
   because a dead-letter grade that silently un-deads itself on the next fact is not actually a
   terminal grade at all. Treated as in-scope because it is required for the grading's own stated
   contract ("at the threshold set status='dead'") to hold.
2. **The admin exposure's home** (finding 9) deviates from the brief's literal "the admin source-health
   / projection health report the api already serves" -- there is only one such report, and it is
   named. Implemented as a merge into that one report's response rather than inventing a second,
   unrequested endpoint.
3. **`EligibilityShadowFailedJobs`'s query predicate changed** (`'failed'`→`'dead'`) even though the
   brief's item 7 only said "update ... if it reads Failed" -- it doesn't read the `Failed` *field*,
   but it does read the `'failed'` *status literal*, which the worker no longer produces; left
   unchanged it would have silently gone permanently empty, losing the shadow tool's only per-account
   attribution for a genuinely stuck account. The JSON shape (`FailedAccount`/`failed_accounts`) is
   unchanged, so no rehearsal static test update was needed for this change either.
4. `TestRunApplyWithoutOperatorIDIsRejected` (cmd/eligibility-repair) was widened to also cover
   `kindPolicyStartReanchor`, which the pre-existing loop omitted (an existing gap, not something this
   slice introduced) -- fixed opportunistically since this exact test needed touching anyway to add
   the new kind.

## Commits

(oldest first)

1. `ffc94ae` `feat(migrate): widen eligibility_projection_jobs for failure grading` -- migration
   0023, `internal/migrate/migrate_test.go`'s exclusion.
2. `d18b404` `fix(eligibility): grade per-account projection failures instead of failing readiness
   immediately` -- the core behavior change: `consumption.go`, `cmd/api/runtime.go`,
   `cmd/api/main_test.go`, `cmd/eligibility-shadow/report.go`,
   `internal/postgresstore/eligibility_shadow_report.go` and its integration test, every touched
   existing integration test (`balance_carry_forward`, `eligibility_auto_reconcile`,
   `eligibility_projection_health`), and the new `projection_failure_grading_integration_test.go`
   ladder. Kept as one commit: the `Failed`→`Dead` rename alone touches every one of these files
   simultaneously and the tree does not compile (let alone pass tests) at any smaller split.
3. `42767d4` `feat(eligibility-repair): add --kind=projection-requeue-dead` -- the new repair tool,
   CLI wiring, CLI tests.
4. `549007e` `feat(admin): expose eligibility projection retrying/dead in source-health` -- the
   httpapi/application wiring for finding 9.
5. (this commit) `docs: XM-INV-PROJECTION-FAILURE-GRADING operations notes and handoff` -- this
   file, `docs/PRODUCTION-RUNBOOK.md`, `docs/ELIGIBILITY-OPERATIONS.md`.

## Files changed

- `backend/migrations/0023_projection_failure_grading.sql` (new)
- `backend/internal/migrate/migrate_test.go`
- `backend/internal/postgresstore/consumption.go`
- `backend/internal/postgresstore/projection_requeue_dead_repair.go` (new)
- `backend/internal/postgresstore/projection_failure_grading_integration_test.go` (new)
- `backend/internal/postgresstore/eligibility_projection_health_integration_test.go`
- `backend/internal/postgresstore/eligibility_shadow_report.go`
- `backend/internal/postgresstore/eligibility_shadow_report_integration_test.go`
- `backend/internal/postgresstore/balance_carry_forward_integration_test.go`
- `backend/internal/postgresstore/eligibility_auto_reconcile_integration_test.go`
- `backend/cmd/api/runtime.go`
- `backend/cmd/api/main_test.go`
- `backend/cmd/eligibility-repair/main.go`
- `backend/cmd/eligibility-repair/main_test.go`
- `backend/cmd/eligibility-shadow/report.go`
- `backend/internal/httpapi/service_contract.go`
- `backend/internal/httpapi/operations.go`
- `backend/internal/httpapi/operations_source_filter_test.go`
- `backend/internal/application/source_processor.go`
- `docs/PRODUCTION-RUNBOOK.md`
- `docs/ELIGIBILITY-OPERATIONS.md`
- `docs/handoffs/XM-INV-PROJECTION-FAILURE-GRADING.md` (this file)

**Not touched:** `agents/`, `backend/Dockerfile`, `contracts/`, `deploy/`, any Sub2API/NewAPI source,
`web/` (see finding 9's "Not done" note), any release-identity file, no tags created, no evaluator
semantics changed beyond the one comment in finding 7.

## Tests

All new/updated tests are in `backend/internal/postgresstore` and `backend/cmd/*` packages, run
against a dedicated database (see Gates below). New file
`projection_failure_grading_integration_test.go`:

- `TestProjectionFailureGradingTransientErrorRequeuesWithBackoff` -- one processing error requeues
  (`status='queued'`, `attempts=1`, `next_attempt_at`~30s out), never a bare `'failed'`; `Dead=0`,
  `Retrying=1`, `OldestPending` stays zero.
- `TestProjectionFailureGradingEscalatesToDeadAfterConsecutiveFailures` -- drives the same
  deterministically-failing account through all 8 rounds (`ProcessEligibilityProjectionJobs` called
  with `now` advanced 40 minutes each round, comfortably past the 30-minute backoff cap); asserts
  `attempts`/`status` after every round, the final `dead`/`PROJECTION_DEAD` row, exactly one
  `eligibility.projection.dead` audit row, and `Dead=1`/`Retrying=0`.
- `TestProjectionFailureGradingProofPendingDoesNotCountAsAttempt` -- reuses
  `seedAlwaysPendingBalanceProofFixture` (proof_contention_backoff_integration_test.go); asserts
  `attempts` stays 0 while the pre-existing `attempt_count`/`BALANCE_PROOF_PENDING` behavior is
  unchanged, and `Retrying=0`/`ProofPending=1`.
- `TestProjectionFailureGradingRequeueDeadDryRunWritesNothingApplyRequeues` -- dry run reports the
  dead job and writes nothing (row unchanged); apply resets `attempts=0`/`status='queued'`/clears
  `last_error` and writes the `eligibility.projection.requeued` audit event; apply without an
  operator id is rejected.
- `TestProjectionFailureGradingTwoAccountIsolationAcrossRetryLadder` -- one poison account (the same
  deterministic error) and one ordinary healthy account in the same
  `ProcessEligibilityProjectionJobs` batch, every one of the 8 rounds: the healthy account succeeds
  every round (`processed=1`, its job row deleted and re-seeded for the next round) while the poison
  account's own attempts climb to `dead`, completely isolated from each other throughout the whole
  ladder.

**Reproduction fixture reused, not invented:** the poison account is exactly
`TestBalanceDeltaCarryForwardRejectsUnmappedFundingVisibility`'s existing "unmapped funding
visibility" shape (`balance_carry_forward_integration_test.go`) -- a `WALLET_CASH` funding lot
referencing a payments-stream `source_events` row that never went through the real, verified ingest
path, which deterministically returns `errBalanceCarryForwardProofInvalid` on every attempt for as
long as the fixture stands (the failing transaction rolls back every write, so nothing about the
condition resolves on its own). This is a real, already-passing production-code error path
reproduced under the new grading, not a synthetic error injected only for this slice's tests.

**Updated (not merely renamed) existing tests**, per the task's own instruction to flag and say so:

- `TestBalanceDeltaCarryForwardRejectsUnmappedFundingVisibility` -- previously asserted
  `health.Failed==1` after one processing error; now asserts the job requeues
  (`status='queued'`/`attempts=1`/`last_error` contains the underlying error) and
  `health.Dead==0`/`health.Retrying==1`, matching the new grading (one failure is never terminal).
- `TestEligibilityProjectionHealthSeparatesProofPendingFromStuck`'s case (c) -- previously seeded
  `status='failed'` to prove `Failed` reflects a failed job regardless of age; now seeds
  `status='dead'` (the renamed field's actual new source) to prove the equivalent for `Dead`.
- `TestEligibilityShadowFailedJobs` -- previously seeded and expected a `status='failed'` row; now
  seeds a `status='dead'` row (plus a new, additional `status='queued'`/`attempts=3` control row
  proving a merely-retrying job is correctly excluded).
- Every other bare `health.Failed`/`Failed:` reference across the touched test files
  (`eligibility_auto_reconcile_integration_test.go`, `cmd/api/main_test.go`) mechanically renamed to
  `Dead`, with `cmd/api/main_test.go` gaining one new case (`"retrying account alone never fails"`)
  the rename did not previously need to cover.

## Gates

All commands run from `K:/发票/wt-XM-INV-PROJ-GRADE/backend`, proxy env vars unset
(`HTTP_PROXY`/`HTTPS_PROXY`/`http_proxy`/`https_proxy`/`ALL_PROXY`/`all_proxy`/`NO_PROXY`/`no_proxy`),
`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_projgrade` (dedicated
database created via `docker exec invoice-test-pg psql -U postgres -c "CREATE DATABASE
invoice_test_projgrade;"`).

```
go build ./...                 # exit 0
go vet ./...                   # exit 0
go test -p 1 -count=1 ./...    # exit 0
```

Full package list, all `ok` (no reruns needed -- no loopback-timeout flake this run):
`cmd/api` (0.33s), `cmd/archive-verify`/`cmd/backup-verify`/`cmd/document-gc`/`cmd/oidc-logout-retention`/
`cmd/pdf-scanner` (no tests), `cmd/bootstrap-settings`, `cmd/bootstrap-sources`, `cmd/eligibility-repair`
(10.57s, includes every new `--kind=projection-requeue-dead` test), `cmd/eligibility-shadow`,
`cmd/identity-migrate`, `cmd/keygen`, `cmd/migrate`, `cmd/mtlsgen`, `cmd/oidc-preflight`,
`cmd/pdf-policy-check`, `internal/adminsettings`, `internal/application` (17.83s), `internal/auth`
(10.14s), `internal/backuparchive`, `internal/backupverify`, `internal/document`, `internal/domain`,
`internal/httpapi`, `internal/ledger`, `internal/mailer`, `internal/migrate` (7.41s, confirms migration
0023's reduced-fixture exclusion is correct), `internal/oidcretention`, `internal/pdfscanner`,
`internal/postgresstore` (166.54s, this slice's new/updated tests included), `internal/securefields`,
`internal/sourceingest`, `internal/testdb`.

`gofmt`: staged every changed/new `.go` file (`git add`) and ran `"$(go env GOROOT)/bin/gofmt" -l`
against each staged blob (`git show ":<path>"`, the autocrlf-clean-filtered bytes that will actually
be committed -- this machine's documented CRLF-checkout `gofmt` false positive, recorded in the
`windows-toolchain-quirks` memory for future agents): clean on every file.

`gitleaks git --no-banner --log-opts="5cd525a..HEAD" .`: 4 commits scanned, ~59.46 KB, "no leaks
found".

## Not run

- `scripts/verify.ps1` in full -- spans frontend, Docker release-image gates, and Keycloak/Nginx
  verification, none of which this backend-only slice touches, matching multiple prior slices' own
  precedent for the same reason.
- `deploy/rehearsal/test-shadow-eval.sh` -- its fixtures reference only the unchanged `"failed"`/
  `"failed_accounts"` JSON key names (see finding 8/deviation 3 above); no JSON shape change means no
  static-test update was needed, and this bash test suite is outside a Go-only backend slice's
  standard gate set regardless.
- Anything requiring a server/production connection -- no production access for this task.

## Risks / things to sign off on

1. **`projectionFailureDeadThreshold=8`** is the brief's own suggested value, chosen to exactly
   mirror `source_ingest_events`' existing threshold -- not independently derived from eligibility-
   projection-specific production data (no equivalent incident history to calibrate against, unlike
   `balanceBlipRebaselineCap`'s own documented judgment call in XM-INV-BLIP-SOFTFAIL). Tunable without
   a migration if 8 proves too eager or too lax for this worker's own failure characteristics.
2. **The two `ON CONFLICT` upsert guards (finding 6)** are the one piece of this slice not named by
   the brief's literal file/line pointers. Read closely before signing off: they preserve
   `status='dead'` unconditionally, including `next_attempt_at`/`updated_at`, while still advancing
   `requested_through` -- verified this is the only column that needs to keep moving for the repair
   tool's later requeue to pick up everything that arrived while dead.
3. **No admin console (web/) surfacing of `eligibility_projection`** -- an operator today can only see
   `Retrying`/`Dead` via `GET /api/v1/admin/source-health`'s raw JSON or the repair tool's own dry-run
   report, not a rendered admin page. Flagged as a follow-up, not blocking, per precedent (see finding
   9).
4. No new float amounts, no logged/persisted secrets beyond the bounded `last_error` text (which is a
   Go error string -- verified it never includes ciphertext, keys, or PII by construction: every error
   in this call chain originates from SQL/business-rule failures, not from decrypting anything), no
   `contracts/` changes, no admin-OIDC changes, no touch to any release-identity file, no tags
   created, no touch to any file outside this slice's stated scope, `backend/Dockerfile` untouched --
   checked.

## Follow-ups (recommended, not blocking this delivery)

1. Surface `eligibility_projection` (`Retrying`/`Dead` at minimum) on the admin console's existing
   `/admin/source-health` page (`web/src/App.tsx`'s `SourceHealthPage`), once there is a natural
   opportunity to touch that page's own slice of frontend work.
2. Once this ships and accumulates real production data, revisit whether
   `projectionFailureDeadThreshold=8` is the right threshold specifically for this worker's own
   failure characteristics (see risk 1).
3. Consider whether `EligibilityShadowFailedJobs`/`EligibilityShadowFailedJob` should also surface the
   new `attempts` column (currently it still reports only the legacy `attempt_count`) for a richer
   shadow-rehearsal report -- deliberately not added in this slice to keep the JSON shape (and
   therefore `deploy/rehearsal/test-shadow-eval.sh`'s fixtures) untouched.
