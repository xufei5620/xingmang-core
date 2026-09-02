# XM-INV-READY-PENDING: readiness tolerates BALANCE_PROOF_PENDING backoff

- **status:** implemented and self-tested locally; gates below.
- **branch:** `ai/claude/XM-INV-READY-PENDING` (based on `ai/claude/XM-INV-AUTOLOGIN` at
  `b9d51f6`, tag `v0.1.0-rc70-signed`), worktree `K:/发票/wt-XM-INV-READYPEND`.
- **commit:** `dbe58c1` fix(readiness): tolerate BALANCE_PROOF_PENDING backoff in eligibility
  projection health (single commit, trailers per the standing attribution instructions).

## Problem

Production observation, 2026-09-02 04:39Z (read-only): after RC70's repair removed all dead
events, `/readyz` stayed 503 solely because of one row in `eligibility_projection_jobs` —
account `40bd883d-26fa-4938-b8c8-0f51c8b88686`, `status=queued`, `attempt_count=49`,
`last_error_code=BALANCE_PROOF_PENDING`, `created_at=04:10:06Z`, `next_attempt_at=04:49:42Z` (a
10-minute backoff from the last attempt at `04:39:42Z`, per XM-INV-PROOF-CONTENTION's exponential
schedule). `EligibilityProjectionHealth.OldestPending` was `min(created_at)` across every row in
the table, unfiltered by status or reason. A job legitimately waiting on a balance proof — every
attempt so far correctly decided "not provable yet" and rescheduled itself on the documented
backoff — is not the same thing as a job making no progress, but the old rule could not tell them
apart: `created_at` only reflects when this unresolved-job episode began, which keeps growing for
as long as the source stream stays behind, regardless of how faithfully the job is being retried.

## The rule implemented

`EligibilityProjectionHealth` (`backend/internal/postgresstore/consumption.go`) now buckets every
row into exactly one of two groups:

- **Proof-pending** (`ProofPending` count, `OldestProofPending` = the oldest `updated_at` in this
  group): `status='queued' AND last_error_code='BALANCE_PROOF_PENDING' AND next_attempt_at` is
  still in the future. (`status='queued'` already implies `lease_token`/`lease_expires_at` are
  both clear, per the table's own CHECK constraint — "lease clear" is not a separate predicate.)
  This is an actively-scheduled backoff: the row's last attempt succeeded in deciding "pending"
  and is due to retry later, exactly as designed.
- **Everything else** (`OldestPending` = the oldest `updated_at` in this group): processing rows,
  failed rows, plain queued rows, and — the important edge case — a `BALANCE_PROOF_PENDING` row
  whose own `next_attempt_at` has already **elapsed without a retry**. That last case reverts to
  counting toward `OldestPending`: if the worker misses a due proof-pending job, that is no longer
  "waiting on schedule," it is stuck, and must still trip readiness the same way any other stalled
  job does.

Both timestamps switched from `created_at` to `updated_at` (the last real attempt: set by the
claim `UPDATE`, the failure/backoff marks, and the two `ON CONFLICT` requeue upserts from
XM-INV-PROOF-CONTENTION) — a job cycling through genuine attempts never looks stale merely because
its row has existed a long time; only a real gap between attempts ages it.

`Failed` is unchanged (still an unfiltered `count(*) FILTER (WHERE status='failed')`) and
continues to fail readiness unconditionally, regardless of age.

`cmd/api/runtime.go`'s Readiness closure now calls a pure, table-tested function:

```go
func eligibilityProjectionReady(health postgresstore.EligibilityProjectionHealth, now time.Time) error {
	if health.Failed > 0 {
		return errors.New("invoice eligibility projection is unhealthy")
	}
	if !health.OldestPending.IsZero() && now.Sub(health.OldestPending) > eligibilityProjectionStuckAfter {
		return errors.New("invoice eligibility projection is unhealthy")
	}
	return nil
}
```

`eligibilityProjectionStuckAfter` is the pre-existing 15 minutes, unchanged. A proof-pending job
never appears in `OldestPending`, so it can never fail readiness by itself, however long the
source stream stays behind — this directly fixes the production false-positive.

## New health fields

```go
type EligibilityProjectionHealth struct {
	Queued, Failed, Processing int64
	OldestPending              time.Time // now excludes actively-waiting proof-pending rows
	ProofPending               int64     // new
	OldestProofPending         time.Time // new
}
```

`Queued` is unchanged (still every `status='queued'` row, including proof-pending ones) — the two
pre-existing call sites in `balance_carry_forward_integration_test.go` that assert `Queued==1` for
a proof-pending fixture needed no changes and still pass.

## Operational visibility (non-blocking)

A proof-pending job can legitimately wait for hours if the source's balance stream stays behind.
That should be visible in logs without querying the database, but must never flap readiness.
`cmd/api/runtime.go` adds:

- `shouldWarnProofPending(health, now, lastWarnedAt) bool` — pure decision: true only when
  `ProofPending > 0`, the oldest has waited more than `eligibilityProofPendingWarnAge` (60
  minutes), and at least `eligibilityProofPendingWarnInterval` (5 minutes) has passed since the
  last warning (or none has fired yet).
- `eligibilityProofPendingWarner` — one instance per process (constructed once in
  `buildProductionRuntime`, so its rate-limit state survives across `/readyz` calls), whose
  `warnIfStale` method applies that decision under a mutex and, when it fires, logs at `slog.Warn`
  with `proof_pending` and `oldest_proof_pending_age`. This runs on every readiness evaluation
  (right after fetching health, before the pass/fail decision) but the interval keeps it from
  spamming a probe hitting `/readyz` every few seconds.

## Files changed

- `backend/internal/postgresstore/consumption.go` — `EligibilityProjectionHealth` struct/query
  (new fields, `updated_at`-based bucketing) and its doc comment. Also fixed a pre-existing
  one-space `gofmt` misalignment in the adjacent `balanceProofPendingBackoffCapSeconds`/
  `balanceProofPendingRequeueResetWindow` const block (present in the baseline from
  XM-INV-PROOF-CONTENTION, caught while gofmt-checking this file).
- `backend/cmd/api/runtime.go` — `eligibilityProjectionReady`, `eligibilityProjectionStuckAfter`,
  `shouldWarnProofPending`, `eligibilityProofPendingWarnAge`/`-Interval`,
  `eligibilityProofPendingWarner`, wiring in the Readiness closure and `buildProductionRuntime`;
  added the `sync` import.
- `backend/cmd/api/main_test.go` — `TestEligibilityProjectionReadyDistinguishesProofPendingFromStuck`,
  `TestShouldWarnProofPendingRateLimitsToOncePerInterval`,
  `TestEligibilityProofPendingWarnerRateLimitsAcrossCalls`.
- `backend/internal/postgresstore/eligibility_projection_health_integration_test.go` (new) —
  `TestEligibilityProjectionHealthSeparatesProofPendingFromStuck` (cases (a)/(b)/(c) from the task
  brief plus the "backoff window already elapsed" edge, in one aggregate snapshot across four
  accounts) and `TestEligibilityProjectionHealthEmptyIsZeroValue`.
- `docs/handoffs/XM-INV-PROOF-CONTENTION.md` — added a follow-up note pointing here.

**Admin/ops surface check:** grepped every consumer of `EligibilityProjectionHealth` and of
`eligibility_projection_jobs` in `backend/` and `web/`. The only consumers are `cmd/api/runtime.go`'s
Readiness closure and the two field-level assertions in `balance_carry_forward_integration_test.go`
(`Failed`/`Queued`, both unaffected). The frontend's only admin health view
(`web/src/lib/http-api.ts`'s `getSourceHealth`/`/api/v1/admin/source-health`) renders
`postgresstore.SourceHealthReport` — source ingestion stream health, a separate subsystem — and
never touches `EligibilityProjectionHealth`. There is no existing admin/ops surface displaying
proof-pending counts to adjust.

**Not touched:** any migration file (no schema change — the split reads existing
`status`/`last_error_code`/`next_attempt_at`/`updated_at` columns); the six release identity
files; `release/`; no tags created; `contracts/`; `web/` (confirmed above it has nothing to
adjust); any file outside this slice's stated scope.

## Tests run

From `backend/`, with the eight proxy env vars unset, `GOFLAGS=-buildvcs=false`, and
`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_readypend`
(dedicated database, created via `docker exec invoice-test-pg psql -U postgres -c 'CREATE DATABASE
invoice_test_readypend'`):

```
go build ./...                 # exit 0
go vet ./...                   # exit 0
go test -p 1 -count=1 ./...    # exit 0, all 30 packages (23 ran tests, 7 have none)
```

Full package list, all `ok`: `cmd/api` (0.4s), `cmd/bootstrap-settings`, `cmd/bootstrap-sources`,
`cmd/eligibility-repair` (3.4s), `cmd/keygen`, `cmd/migrate`, `cmd/mtlsgen`, `cmd/oidc-preflight`,
`cmd/pdf-policy-check`, `internal/adminsettings` (2.4s), `internal/application` (17.7s),
`internal/auth` (23.2s — no loopback timeout this run), `internal/backuparchive`,
`internal/backupverify`, `internal/document`, `internal/domain`, `internal/httpapi`,
`internal/ledger`, `internal/mailer`, `internal/migrate` (7.5s), `internal/oidcretention` (4.4s),
`internal/pdfscanner`, `internal/postgresstore` (91.2s — includes both new tests below plus the
pre-existing ~27s 1,000-checkpoint scale test and the 5s+ contention-reproduction tests),
`internal/securefields`, `internal/sourceingest`, `internal/testdb` (21.9s).

New tests added by this slice (6 total), all passing individually and as part of the targeted
package runs below, run multiple times:

- `TestEligibilityProjectionReadyDistinguishesProofPendingFromStuck` (cmd/api)
- `TestShouldWarnProofPendingRateLimitsToOncePerInterval` (cmd/api)
- `TestEligibilityProofPendingWarnerRateLimitsAcrossCalls` (cmd/api)
- `TestEligibilityProjectionHealthSeparatesProofPendingFromStuck` (internal/postgresstore)
- `TestEligibilityProjectionHealthEmptyIsZeroValue` (internal/postgresstore)

Also re-ran (unmodified, confirming no regression) the pre-existing tests that assert on
`EligibilityProjectionHealth` fields or exercise the same `eligibility_projection_jobs` machinery:
`TestBalanceDeltaCarryForward{FreezesUnchangedActualMismatch,MatchedNetFactsFinalize,
MissingProofFailsWithoutAdvancing,UsesLatestLowerSequenceActualAtSameAsOf,
RejectsUnmappedFundingVisibility,UsesMappedFundingVisibility}`,
`TestBalanceCarryForwardProofScalesToOneThousandCheckpoints`,
`TestProcessEligibilityProjectionJobEvaluatesProofWithoutTheAdvisoryLock`,
`TestBalanceProofPendingBackoffGrowsExponentiallyPerAttempt`,
`TestBalanceProofPendingRequeuePreservesDeepBackoffButPullsInShallowOne` — all pass.

`gofmt`: `"$(go env GOROOT)/bin/gofmt" -d <touched files>` on the three pre-existing (CRLF) files
shows the known whole-file `@@ -1,N +1,N @@` false alarm (same line count before/after — Windows
checkout stores `.go` as CRLF, `gofmt` emits LF). Verified there is no genuine formatting issue
hiding underneath by diffing an LF-normalized copy of each file against `gofmt -d`: zero diff on
all four touched files (the three CRLF ones plus the new LF-native test file) after fixing two real
issues found this way — the pre-existing const misalignment noted above, and this slice's own new
map-literal column alignment in `main_test.go` and the new integration test file. `gofmt -l` on the
whole repo is not used (known repo-wide CRLF false-positive list, documented in prior handoffs);
per-file `-d` diffing is the reliable check.

`pwsh -NoProfile -File scripts/test-release-image-gate.ps1`: "Release image gate offline/static
fixtures passed." (unaffected by this slice; no `release/`, `scripts/`, or release-identity file
touched).

`gitleaks`: `gitleaks detect --source=. --log-opts="b9d51f6..HEAD" --verbose --redact=0` against
this slice's one commit (`dbe58c1`) — "1 commits scanned... no leaks found".

## Not run

- Anything requiring a server/production connection or a release/deploy step — out of scope,
  explicitly excluded (no release ceremony, no image gate beyond the self-test above, no
  release-identity files touched, no tags created).

## Risks / things to sign off on

1. **The "backoff window elapsed" revert is deliberate, not incidental.** A proof-pending job only
   stays excluded from `OldestPending` while its own `next_attempt_at` is still ahead of it. If the
   eligibility-projection worker ever stalls or falls badly behind (e.g. under sustained load, more
   due jobs per cycle than the worker's `limit=25` can claim), an individual proof-pending job's
   `next_attempt_at` passing without a retry converts it back into a stuck-bucket row using its last
   `updated_at` — so readiness still eventually trips if the *worker* itself is unhealthy, not just
   if a proof stays unresolved. Worth confirming this is the intended failure mode: readiness is a
   signal about the worker keeping up, not about how long any individual proof takes to resolve.
2. **`eligibilityProofPendingWarnAge` (60 minutes) and `eligibilityProofPendingWarnInterval` (5
   minutes) are new tunables** with no prior precedent to match against, chosen to be well clear of
   the 10-minute backoff cap (so a healthy backoff cycle never trips the warning) while still
   surfacing a stream that's been behind for over an hour. Easy to retune (two constants) if
   production experience suggests otherwise.
3. Found and fixed one **pre-existing** `gofmt` misalignment in `consumption.go` (the
   `balanceProofPendingBackoffCapSeconds`/`balanceProofPendingRequeueResetWindow` const block, from
   XM-INV-PROOF-CONTENTION) while gofmt-diff-checking this file — flagging in case that block's
   exact byte content is referenced elsewhere (it is not, by grep).
4. No new float amounts, no logged/persisted secrets, no `contracts/` changes, no schema/migration
   changes, no admin-OIDC changes, no touch to any file outside this slice's stated scope — checked.

## Follow-ups (recommended, not blocking)

1. If production sees the eligibility-projection worker fall behind under load (many accounts
   simultaneously proof-pending, more due per 2-second cycle than `limit=25` claims), consider
   whether that `limit` should scale with `ProofPending`/`Queued` — out of scope here since nothing
   in the current incident suggested a throughput problem, only a healthy backoff being
   misclassified as unhealthy.
2. `eligibility_projection_jobs` has no admin/ops HTTP surface today (confirmed above). If one is
   ever added, it should read `ProofPending`/`OldestProofPending` honestly rather than lumping them
   into a single "pending" number, for the same reason readiness now separates them.
