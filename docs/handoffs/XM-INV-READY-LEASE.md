# XM-INV-READY-LEASE: readiness tolerates live-lease processing rows and the worker-reclaim grace

- **status:** implemented and self-tested locally; gates below.
- **branch:** `ai/claude/XM-INV-READY-LEASE` (based on `ai/claude/XM-INV-AUTOLOGIN` at `9cf0046`,
  RC75 line), worktree `K:/发票/wt-XM-INV-READY-LEASE`.
- **commit:** see `git log` on this branch (single commit, trailers per the standing attribution
  instructions).

## Problem

Confirmed production false positive, 2026-09-03 03:17:24Z: `/readyz` returned 503 for a single
probe while two `eligibility_projection_jobs` rows -- both carrying
`last_error_code=BALANCE_PROOF_PENDING` on a 10-minute backoff, `attempt_count` 12 and 18 -- were
being actively attempted (`status='processing'`). `/readyz` was 200 immediately before and after.

**Mechanism (primary).** `ProcessEligibilityProjectionJobs`' batch claim
(`backend/internal/postgresstore/consumption.go`) claims up to `limit` (25) rows in one `UPDATE`,
stamping every claimed row's `updated_at` with a single shared `now` and setting
`lease_expires_at=now+interval '2 minutes'`. It then works through the claimed account IDs
serially; each account's `processEligibilityProjectionJob` gets its own up-to-300s transaction
budget. A row still waiting its turn later in that loop legitimately sits `status='processing'`
with an `updated_at` that ages well past `EligibilityProjectionHealth`'s 15-minute
`eligibilityProjectionStuckAfter` threshold before its own turn even arrives -- even though its
lease is still live and the worker has not abandoned it.
`EligibilityProjectionHealth`'s `OldestPending` bucket (added by XM-INV-READY-PENDING) excluded
only queued proof-pending rows from this staleness check; a processing row was counted
unconditionally, regardless of how live its lease still was. That is exactly what tripped
readiness on the two rows above.

**Mechanism (secondary, smaller contributor).** Between a queued `BALANCE_PROOF_PENDING` row's
`next_attempt_at` elapsing and the 2-second eligibility-projection worker's next tick actually
reclaiming it, the row briefly fails the "still actively scheduled" test (`next_attempt_at>now`)
and falls into the stuck bucket carrying its *previous* attempt's `updated_at` -- which, for a job
deep into the exponential backoff (up to the 10-minute cap), can already be several minutes old by
the time `next_attempt_at` elapses.

## The change implemented

`EligibilityProjectionHealth`'s query now excludes two additional cases from the "stuck" bucket
(`OldestPending`), alongside XM-INV-READY-PENDING's existing proof-pending exclusion:

1. **Live-lease processing rows.** `status='processing' AND lease_expires_at>now` is now excluded
   from `OldestPending` outright (the table's own CHECK constraint guarantees a processing row
   always has a non-NULL `lease_expires_at`, so this is safe unconditionally). Only a processing
   row whose lease has actually lapsed -- the worker crashed, or the process died mid-batch --
   still counts as stuck.
2. **Worker-reclaim grace on the proof-pending exclusion.** The existing `next_attempt_at>now` test
   (deciding whether a queued `BALANCE_PROOF_PENDING` row is "actively scheduled" vs. "stuck") is
   widened to `next_attempt_at>now-eligibilityProjectionReclaimGraceSeconds` (30 seconds, a new
   constant next to `balanceProofPendingBackoffCapSeconds`). A row whose backoff elapsed less than
   30 seconds ago is still "actively scheduled" as far as readiness is concerned -- the worker
   simply hasn't ticked yet (its interval is 2 seconds; 30s is a comfortable margin, still far
   short of the 15-minute stuck threshold or any realistic backoff step).

The exact predicate change, in `EligibilityProjectionHealth`'s query:

- Before:
  `WHERE NOT (status='queued' AND last_error_code='BALANCE_PROOF_PENDING' AND next_attempt_at>$1)`
- After:
  ```sql
  WHERE NOT (
      (status='queued' AND last_error_code='BALANCE_PROOF_PENDING' AND next_attempt_at>$1::timestamptz-interval '30 seconds')
      OR (status='processing' AND lease_expires_at>$1)
  )
  ```

(The `ProofPending`/`OldestProofPending` fields' own predicate is widened by the same grace,
symmetrically with before -- a row inside the grace window still counts as proof-pending, carrying
its previous attempt's `updated_at`, matching the ops-visibility warning's existing intent.)

`eligibilityProjectionReady` (`cmd/api/runtime.go`) and its 15-minute `eligibilityProjectionStuckAfter`
threshold are unchanged: this slice is entirely a fix to which rows feed `OldestPending`, not to the
threshold decision itself. No new `cmd/api` test cases were needed -- its existing
`TestEligibilityProjectionReadyDistinguishesProofPendingFromStuck` already covers that function's
logic exhaustively against literal health-struct fixtures, independent of how the struct gets
populated.

**A parameter-typing pitfall caught by running the new tests red first:** `$1` needed an explicit
`::timestamptz` cast in the new arithmetic (`$1::timestamptz-interval '...'`). Without it, Postgres's
parameter-type inference resolved `$1` as `interval` instead of `timestamptz` (an ambiguous operator
resolution between `interval-interval` and `timestamptz-interval` candidates for an as-yet-untyped
placeholder), producing `operator does not exist: timestamp with time zone > interval`. Fixed using
the same `$N::timestamptz` pattern already established a few hundred lines below in this same file
(the `BALANCE_PROOF_PENDING` backoff requeue).

## Considered, not implemented: refreshing `updated_at` per-account in the serial loop

The task brief raised, as optional hardening, refreshing each claimed row's `updated_at` at the
start of its own turn in `ProcessEligibilityProjectionJobs`' serial loop (rather than relying solely
on the batch's shared claim-time stamp). I evaluated this and chose not to implement it in this
slice:

- The live-lease exclusion above already fully covers the confirmed incident: both reported rows
  were `status='processing'` (i.e., within an active attempt), which is exactly what a live lease
  reflects. A proof-pending job's own attempt (checking whether the balance proof is published yet)
  is not the multi-minute whale-account reprojection case `processEligibilityProjectionJob`'s 300s
  budget defensively allows for -- it is a quick published-yet-or-not check -- so realistically its
  lease (2 minutes) comfortably outlives its own processing time.
- The residual gap this would additionally close -- a row waiting deep in a batch full of
  genuinely slow (multi-minute) accounts, such that even its live-lease window elapses before its
  own turn arrives -- is the same scenario XM-INV-READY-PENDING's own risk 1 already accepts as a
  legitimate trip: "readiness is a signal about the worker keeping up, not about how long any
  individual proof takes to resolve." If the worker is that far behind, tripping readiness is
  arguably correct, not a false positive.
- It would add a write (one `UPDATE` per account) to the hot serial-processing loop on every tick
  under load, purely to shave a residual edge case that has not been observed in production, and
  would need its own careful interaction check against the existing backoff/failure-marking/
  advisory-lock tests in this same function.

Flagged as a follow-up below rather than blocking on it, per the task brief's own "include it only
if..." framing for this piece.

## Files changed

- `backend/internal/postgresstore/consumption.go` -- `EligibilityProjectionHealth`'s query and doc
  comment (live-lease and reclaim-grace exclusions), new `eligibilityProjectionReclaimGraceSeconds`
  constant with rationale, placed next to `balanceProofPendingBackoffCapSeconds`.
- `backend/internal/postgresstore/eligibility_projection_health_integration_test.go` -- four new
  tests: `TestEligibilityProjectionHealthLiveLeaseProcessingExcludedFromStuck`,
  `TestEligibilityProjectionHealthLapsedLeaseProcessingCountsAsStuck` (control),
  `TestEligibilityProjectionHealthGraceWindowProofPendingExcludedFromStuck`,
  `TestEligibilityProjectionHealthGraceElapsedProofPendingCountsAsStuck` (control); plus two small
  helpers (`seedEligibilityProjectionJobRowProcessing`, `seedEligibilityProjectionHealthSource`)
  factoring out the processing-row and fixture-source boilerplate each of the four needs in its own
  isolated schema.
- `docs/handoffs/XM-INV-READY-PENDING.md` -- short follow-up note pointing here.

**Not touched:** `cmd/api/runtime.go` (no change needed -- see above), any migration file (no schema
change), the six release identity files, `release/`, `contracts/`, `web/`, or any file outside this
slice's stated scope.

## Tests run

From `backend/`, with the eight proxy env vars unset
(`env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy`),
`GOFLAGS=-buildvcs=false`, and
`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_readylease`
(dedicated database, created via `docker exec invoice-test-pg psql -U postgres -c 'CREATE DATABASE
invoice_test_readylease'`):

```
go build ./...                 # exit 0
go vet ./...                   # exit 0
go test -p 1 -count=1 ./...    # exit 0, all 32 packages (27 ran tests, 5 have none)
```

Every package reported `ok`, including `internal/auth` (9.5s, no loopback-timeout flake this run --
not rerun since it did not flake) and `internal/postgresstore` (104.4s, includes the four new tests
below plus every pre-existing test in that package).

New tests added by this slice (4 total), run individually first -- both red against the unmodified
query and green after the fix -- and again as part of the full suite above:

- `TestEligibilityProjectionHealthLiveLeaseProcessingExcludedFromStuck` (red before the fix:
  `OldestPending` wrongly reported the live-lease row's `updated_at` instead of zero; green after)
- `TestEligibilityProjectionHealthLapsedLeaseProcessingCountsAsStuck` (control: passed unmodified,
  confirming the fix does not over-exclude a genuinely abandoned lease)
- `TestEligibilityProjectionHealthGraceWindowProofPendingExcludedFromStuck` (red before the fix:
  `ProofPending` was 0 instead of 1, `OldestPending` wrongly non-zero; green after)
- `TestEligibilityProjectionHealthGraceElapsedProofPendingCountsAsStuck` (control: passed
  unmodified, confirming the fix does not over-exclude a genuinely elapsed backoff)

Also re-ran (individually, unmodified from this slice, confirming no regression) the two
pre-existing tests in the same file that this slice's query change could plausibly affect:
`TestEligibilityProjectionHealthSeparatesProofPendingFromStuck` (the XM-INV-READY-PENDING
regression test) and `TestEligibilityProjectionHealthEmptyIsZeroValue` -- both still pass.

`gofmt`: staged both touched files (`git add`) and ran `"C:\Program Files\Go\bin\gofmt" -l` (this
machine's active toolchain per `go env GOROOT`, matching go.mod's `go 1.25.0`/`toolchain go1.25.13`
pin exactly -- no separately-downloaded toolchain involved here) against the staged blobs extracted
via `git cat-file -p :<path>` (the autocrlf-clean-filtered bytes that will actually be committed,
per this machine's known CRLF-checkout `gofmt` false positive -- see prior handoffs' toolchain
notes); zero files listed, both clean.

`gitleaks`: `gitleaks git --no-banner --log-opts="9cf0046..HEAD" .` against this slice's commit --
"no leaks found".

## Not run

- Anything requiring a server/production connection or a release/deploy step -- out of scope,
  explicitly excluded (no release ceremony, no image gate, no release-identity files touched, no
  tags created).
- `scripts/test-release-image-gate.ps1` -- not run; this slice touches no `release/` or
  release-identity file, so it would be unaffected (matching XM-INV-READY-PENDING's own reasoning
  for skipping it), and the task brief did not ask for it.

## Risks / things to sign off on

1. The reclaim-grace constant (30 seconds) is new, chosen to comfortably exceed the
   eligibility-projection worker's 2-second tick interval while staying far short of the 15-minute
   stuck threshold and the shortest backoff step. Easy to retune (one constant) if production
   experience suggests otherwise.
2. Requirement 3 from the task brief (refreshing `updated_at` per-account inside the serial loop)
   was deliberately not implemented -- see the dedicated section above and follow-up 1 below.
3. No new float amounts, no logged/persisted secrets, no `contracts/` changes, no schema/migration
   changes, no admin-OIDC changes, no touch to any file outside this slice's stated scope -- checked.

## Follow-ups (recommended, not blocking)

1. If production observes a batch containing several genuinely slow (multi-minute) accounts such
   that a later account's live-lease window elapses before its own turn arrives, revisit the
   per-account `updated_at` refresh considered above -- at that point it would be closing a real,
   observed gap rather than a theoretical one.
2. Same as XM-INV-READY-PENDING's own follow-up 1: if the eligibility-projection worker's `limit=25`
   per tick becomes a real throughput bottleneck under load, consider scaling it -- unrelated to
   this slice's fix, noted here only because both slices touch the same worker.
