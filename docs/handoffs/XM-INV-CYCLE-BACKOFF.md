# XM-INV-CYCLE-BACKOFF: stop the busy-cycle restart loop on sub2api/newapi source agents

- **status:** implemented and fully self-tested (backend `go build`/`go
  vet`/full `go test -p 1 -count=1 ./...` against a disposable PostgreSQL 18,
  29/29 packages ok; agents module `go build`/`go vet`/full `go test -p 1
  -count=1 ./...`, 4/4 packages ok). Not deployed, no production access, no
  release ceremony run.
- **branch:** `ai/claude/XM-INV-CYCLE-BACKOFF`, based on
  `ai/claude/XM-INV-AUTOLOGIN` at `8802e08` (production RC67 line), worktree
  `K:/发票/wt-XM-INV-BACKOFF`.
- **commit:** see `git log ai/claude/XM-INV-CYCLE-BACKOFF` — five small
  commits, this handoff file lands last.

## Summary

Production defect (measured 2026-09-01 ~20:00Z): `sub2api`/`newapi` source
agents for the four v3 economic streams were restarting in a loop —
`sub2api-balances` 122 restarts/day, `usage` 93, `payments` 25, `credits` 22
— each restart re-running a full reconcile sweep (hundreds of thousands of
usage rows re-ingested), which fed the next slow cycle. Root cause: the
one-active-cycle constraint (`source_economic_one_active_scan_cycle`, a
partial unique index on `source_economic_scan_cycles(source_instance_id,
stream_id) WHERE cycle_status IN ('receiving','processing')`) correctly
rejects a second, different `scan_cycle_id` while the stream's current cycle
is still active — but that rejection is a *legitimate, temporary* condition
(a cycle can take up to ~30 minutes), and it was indistinguishable from a
real commit conflict by the time it reached the agent:
`receiver.go`'s catch-all mapped every non-context error from
`Acceptor.AcceptSourceBatch` to HTTP 409 `SOURCE_BATCH_COMMIT_REJECTED`, and
`IngestHTTPError.Permanent()` treats 409 as permanent, so the runner exited,
Docker restarted the container, and the fresh process began with a reconcile
sweep.

### 1. A distinct sentinel for exactly this rejection

`backend/internal/domain/types.go`: added `ErrScanCycleBusy` next to
`ErrConflict`.

`backend/internal/postgresstore/source_sync.go`'s `CommitSourceBatch`
already has several `domain.ErrConflict` returns for *other* conflicts
(stale batch replay at line ~270, append past a finalized cycle or a
sequence gap at line ~330, a duplicate event with a different hash at line
~347) — those are untouched. The one-active-cycle rejection is different:
the `INSERT INTO source_economic_scan_cycles ... ON CONFLICT
(source_instance_id,stream_id,scan_cycle_id) DO UPDATE ...` names the
cycle's own primary key as the `ON CONFLICT` arbiter, so a *different*,
concurrent `scan_cycle_id` racing an already-active cycle for the same
stream is not absorbed by that arbiter — it surfaces as a real Postgres
`unique_violation` (23505) on the *other* unique index
(`source_economic_one_active_scan_cycle`), which the code previously just
wrapped as a generic `fmt.Errorf`. The fix inspects that error with
`errors.As` into `*pgconn.PgError`, checks `Code=="23505"` and
`ConstraintName=="source_economic_one_active_scan_cycle"` specifically (the
existing conflict-detection pattern already used in `requests.go:693-695`),
and returns `domain.ErrScanCycleBusy` only for that exact case. The hunk is
four lines plus one import, inserted at the single existing `if cycleErr !=
nil` check — no reformatting, no other line moved (a concurrent branch was
noted to be editing this file heavily).

### 2. Receiver: 503 + Retry-After, not 409

`backend/internal/sourceingest/receiver.go`: `errors.Is(err,
domain.ErrScanCycleBusy)` is checked before the existing
context-cancel/deadline branch, and returns HTTP 503 with a `Retry-After:
30` header and error code `SOURCE_SCAN_CYCLE_BUSY` (distinct from the
existing `SOURCE_BATCH_COMMIT_REJECTED`, so operators and the agent's own
backoff logic can both tell it apart from a real conflict). The
context-cancel/deadline path and the generic-409 path are byte-for-byte
unchanged. Added one `slog.Warn` log line for this case only — status,
`stream_id`, `batch_id`, `sequence`, and the error string, never the batch
payload — matching this codebase's `log/slog` convention (package-level
calls, as already used in `cmd/api/runtime.go`; the receiver had no logging
at all before this).

### 3. Agent: 503 is already transient; the real bug was the failure budget

`IngestHTTPError.Permanent()` (`agents/sourceagent/signing.go`) already
excludes 503 from its permanent-status list, and the runner
(`agents/sourceagent/runner.go`) already uses `IngestHTTPError.RetryAfter`
as a *minimum* wait, never shortened by `MaxBackoff` — both verified by
reading the existing code and by the new `TestRunnerDoesNotShortenRetryAfter`
test that already covered it. So mapping busy to 503 alone would already
stop the immediate exit-on-409 problem.

But it would not have been enough on its own: I traced the retry math with
the deployed defaults (`SOURCE_MAX_CONSECUTIVE_FAILURES=10`,
`SOURCE_MAX_BACKOFF=1m`, both confirmed against
`deploy/docker-compose.sources.yml`) against a 30-second `Retry-After`. Each
busy response waits `max(backoff, 30s)`; `backoff` itself keeps doubling
every attempt regardless of what the actual sleep was
(1,2,4,8,16,32,64→60,60,...). Ten consecutive busy responses exhaust in
~422 seconds (≈7 minutes) — far less than a legitimate 30-minute cycle. So
without a further change, a single long-but-healthy cycle would still trip
`MaxConsecutiveFailures` and cause exactly the same exit → restart →
reconcile-sweep loop, just less often.

**Decision (as flagged as an open question in the task): exempt busy
responses from the consecutive-failure budget, rather than raising the
default limits.** `runner.go`'s retry branch now computes `busy :=
errors.As(err, &ingestErr) && ingestErr.StatusCode ==
http.StatusServiceUnavailable && ingestErr.RetryAfter > 0` and only
increments/checks `consecutiveFailures` when `!busy`. This is receiver-side
signal-driven, not a heuristic: `receiver.go` sets a positive `Retry-After`
*only* on the `SOURCE_SCAN_CYCLE_BUSY` path (not on the context-cancel 503,
not on any other response), so a positive `RetryAfter` on a 503
unambiguously means "the receiver told me to wait, not that it's failing."
I considered raising `MaxConsecutiveFailures`/`MaxBackoff` instead, but
rejected it: those knobs are shared with every *other* transient failure
type (network errors, unexpected 5xx), and loosening them to tolerate a
30-minute busy cycle would also let genuine sustained outages retry far
longer than today before the circuit breaker fails the process closed —
weakening a real protection to fix an unrelated one. The exemption leaves
that budget fully intact for real transient failures and makes a long
legitimate cycle transparent to the circuit breaker regardless of how long
it runs: `backoff` still grows and caps at `MaxBackoff` (so retries settle
at roughly one per minute, not one per 30 seconds, for a long cycle — a
minor chattiness trade-off I judged acceptable rather than in scope to also
tune), the same batch/sequence is retried (the coordinator never advanced
its cursor since the prior attempt errored before an ack), and the process
never exits, so no reconcile sweep fires. No default values changed.

### 4. Docs

`docs/PRODUCTION-RUNBOOK.md` — new §7.1 "Reading agent restart counts, and
409 vs 503 SOURCE_SCAN_CYCLE_BUSY": how to read `RestartCount` per compose
service, and what distinguishes a real incident (409, permanent, restart
count climbing) from expected backpressure (503 busy, restart count flat,
only possible on the four v3 economic streams since `identities` runs
schema 2.0 and never opens a scan cycle) — including a note that seeing 409
climbing restarts alongside busy-shaped logs on the current build would mean
the deployed image predates this fix.

## Files changed

- `backend/internal/domain/types.go` — new `ErrScanCycleBusy` sentinel.
- `backend/internal/postgresstore/source_sync.go` — detects the
  one-active-cycle `unique_violation` specifically and returns the sentinel
  (4 lines + 1 import; every other conflict path untouched).
- `backend/internal/postgresstore/source_sync_integration_test.go` — **new**.
  `TestCommitSourceBatchRejectsSecondActiveScanCycleUntilFirstPublishes`
  (mandatory integration test, see below).
- `backend/internal/sourceingest/receiver.go` — busy → 503 + `Retry-After:
  30` + `SOURCE_SCAN_CYCLE_BUSY`, plus the one `slog.Warn` log line; other
  paths unchanged.
- `backend/internal/sourceingest/receiver_test.go` — `captureAcceptor`
  gained an optional `err` field (nil-default, no behavior change for
  existing tests); three new tests (see below).
- `agents/sourceagent/runner.go` — busy responses no longer spend the
  consecutive-failure budget; `net/http` import added.
- `agents/sourceagent/runner_test.go` — new
  `TestRunnerScanCycleBusyBacksOffWithoutOpeningCircuit`.
- `docs/PRODUCTION-RUNBOOK.md` — new §7.1 (see above).
- `docs/handoffs/XM-INV-CYCLE-BACKOFF.md` — this file.

**Not touched:** `backend/internal/postgresstore/consumption.go` (per
explicit instruction — read it to understand `tryPublishEconomicScanCyclesTx`
for the integration test, changed nothing in it), `agents/sourceagent/
signing.go` (verified `Permanent()`/`RetryAfter` already did the right
thing, no change needed), any release/deploy/RC-advancement file, any
migration, any production or server connection.

## Tests run

Backend (from `backend/`; `GOFLAGS=-buildvcs=false`; all eight proxy env
vars unset per this repo's known Windows/httptest quirk; disposable
PostgreSQL 18 via the `invoice-test-pg` container, dedicated database
`invoice_test_backoff` created for this run — see Risks #1 for why a
dedicated database was necessary):

```
go build ./...                                              # clean
go vet ./...                                                 # clean
INVOICE_TEST_DATABASE_URL="postgres://postgres:test@127.0.0.1:55432/invoice_test_backoff?sslmode=disable" \
  env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy \
      -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy \
  go test -p 1 -count=1 ./...                                # 29/29 packages: ok
"$(go env GOROOT)/bin/gofmt" -l <every touched .go file's git-index blob>
                                                               # clean (see
    Risks #2 for why -l/-d against the raw working-tree files is noisy here)
```

Agents module (from `agents/`; same proxy-unset prefix, `GOFLAGS=-buildvcs=false`):

```
go build ./...           # clean
go vet ./...              # clean
go test -p 1 -count=1 ./...   # 4/4 packages: ok (cmd/source-agent has no
                                # test files; cmd/source-agent-prod,
                                # cmd/source-keygen, sourceagent all ok)
```

New tests added by this task, and what each proves:

- `TestCommitSourceBatchRejectsSecondActiveScanCycleUntilFirstPublishes`
  (postgresstore, integration): commits a v3 cycle with one unresolved event
  (stays `processing`, confirmed via direct query), commits a second,
  different `scan_cycle_id` for the same stream and asserts
  `errors.Is(err, domain.ErrScanCycleBusy)` — and that the rejection did not
  advance `source_ingest_state.sequence`; publishes the first cycle via the
  same `markV3CycleProcessed` helper `consumption_integration_test.go`
  already uses; retries the *exact same* `SourceBatchInput` and asserts it
  now succeeds. This also incidentally proves the "retry the same
  batch/sequence" assumption the agent relies on actually holds at the
  store level (a rolled-back attempt leaves `sequence`/`last_batch_hash`
  untouched).
- `TestReceiverMapsScanCycleBusyTo503WithRetryAfter` /
  `TestReceiverMapsOtherCommitErrorsTo409WithoutRetryAfter` /
  `TestReceiverMapsContextErrorsTo503WithoutBusyCode` (sourceingest, unit,
  via a `captureAcceptor.err` override): busy → 503 + `Retry-After: 30` +
  `SOURCE_SCAN_CYCLE_BUSY` in the body; a generic error → unchanged 409
  `SOURCE_BATCH_COMMIT_REJECTED` with no `Retry-After`; `context.Canceled`
  and `context.DeadlineExceeded` → unchanged 503 `SOURCE_BATCH_COMMIT_REJECTED`
  with no `Retry-After` (proves the pre-existing path is untouched, not just
  that the new one works).
- `TestRunnerScanCycleBusyBacksOffWithoutOpeningCircuit` (sourceagent,
  runner-level): a scripted `PageSyncer` returns `&IngestHTTPError{StatusCode:
  503, RetryAfter: 30s}` forever; `MaxConsecutiveFailures` is deliberately
  set to 2. Asserts every observed sleep equals exactly 30 seconds
  (backoff-equals-Retry-After), that the coordinator is called again on every
  attempt (same source is re-synced, not skipped), and — the actual
  regression this task fixes — that after 5 consecutive busy responses (more
  than twice the configured limit) `runner.Run` has neither returned an
  error nor opened the circuit; it only stops because the test's `Sleep`
  hook cancels the context.

## Not run

- **Real production/staging exercise of the fix** — no server access, per
  task constraints. The 422-second-to-circuit-open math above is a
  hand-verified trace of the existing (unchanged by this task) backoff
  arithmetic against the deployed compose defaults, not something re-run
  against a live 30-minute cycle.
- **A test that exercises the full agent binary against a real receiver
  HTTP server for the busy path** (e.g. `httptest` end-to-end through
  `HTTPIngestClient.Send`) — the unit/integration tests above cover the
  receiver's HTTP mapping and the runner's retry/circuit-breaker logic
  separately, which was judged sufficient scope for "agent runner test" as
  specified; the wire format between them (`Retry-After` header parsing)
  already has pre-existing coverage in `signing.go`'s own tests
  (unchanged by this task).
- Frontend — this task has no frontend surface; nothing in `web/` touched.

## Risks / things to sign off on

1. **The shared `invoice-test-pg` container's `invoice_test` database is not
   agent-isolated.** My first full-suite run against it failed two
   unrelated tests (`adminsettings`, `auth`) with "database contains unknown
   migration 0016_policy_anchor.sql" — that migration belongs to the
   concurrent XM-INV-POLICY-ANCHOR work in a different worktree, not this
   branch, and the failure disappeared entirely on a re-run against a
   dedicated `invoice_test_backoff` database I created in the same
   container (`integrationStore(t)`'s `DROP SCHEMA public CASCADE` races
   across worktrees sharing one database name). This is a pre-existing gap
   in the shared test harness, not something this task introduced or fixed;
   flagged as a follow-up. `invoice_test_backoff` is left in place in case
   it's useful again — safe to drop.
2. **Windows `core.autocrlf=true` makes raw `gofmt -d`/`-l` against
   working-tree files show every touched file as fully rewritten** — a
   known false alarm (also hit and documented by the prior
   XM-INV-EMBED-SCOPE handoff). Verified this is pure noise two ways: `git
   diff` (which is autocrlf-aware) shows only the intended hunks for every
   file, and `gofmt -l` against each file's actual `git`-index blob (what
   would really be committed, LF-normalized) is clean for all seven touched
   files. Did not run `gofmt -w` anywhere.
3. **The one-line receiver log (`slog.Warn`) is new** — `receiver.go` had no
   logging at all before this change. I used the package-level `log/slog`
   calls already established at the `cmd/api` composition-root layer rather
   than adding a `*slog.Logger` field to `Receiver` (the dependency-injected
   pattern `internal/httpapi.Server` uses) — judged proportionate for one
   log line without touching `Receiver`'s field list or its construction
   site in `runtime.go`, but a reviewer preferring strict DI consistency
   might want it threaded through instead.
4. **`MaxBackoff`/`MaxConsecutiveFailures` defaults were deliberately left
   unchanged** (see Summary §3) — the exemption approach makes this
   unnecessary for the stated problem, but it does mean a long busy cycle
   still polls roughly once a minute rather than being tuned to the
   receiver's 30-second hint more precisely. Not fixed; judged out of scope
   for "minimal, no refactors."
5. Ran directly, single agent, in my own worktree; touched no file outside
   what's listed above; never ran a release ceremony, deploy step, or
   production/server command.

## Follow-ups (recommended, not blocking this task's delivery)

1. Give the shared Postgres test harness (`store_integration_test.go`'s
   `integrationStore`) a per-worktree or per-run-unique database name (e.g.
   derived from `git rev-parse --show-toplevel` or a random suffix) instead
   of the fixed `invoice_test`, so concurrent agents/worktrees stop racing
   each other's schema drops. Affects every integration test in this
   package, not just this task's.
2. If the receiver ever needs more than this one log line, thread a
   `*slog.Logger` through `Receiver` (mirroring `internal/httpapi.Server`)
   rather than continuing to add package-level `slog` calls piecemeal.
3. Consider whether the agent should back off *closer* to the receiver's
   30-second `Retry-After` for the whole duration of a long busy cycle
   (right now `backoff`'s own doubling — independent of `RetryAfter` — walks
   it up to the 1-minute `MaxBackoff` cap after a few attempts). Not a
   correctness issue, just a chattiness/latency tuning question explicitly
   left out of this task's scope.
