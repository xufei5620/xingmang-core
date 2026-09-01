# XM-INV-OBS-BUNDLE: silent-failure logging + platform username display + zero-value sync time

- **status:** implemented and self-tested locally (backend `go build`/`go
  vet`/full `go test -p 1 -count=1 ./...` with the real integration
  database, 27/27 packages; frontend `npm run typecheck`, `npm test`
  40/40 tests, `npm run build`). Not deployed, not clicked through in a
  browser.
- **branch:** `ai/claude/XM-INV-OBS-BUNDLE`, based on
  `ai/claude/XM-INV-AUTOLOGIN` at `aa04e34`, worktree
  `K:/发票/wt-XM-INV-OBS`.
- **commit:** implementation/tests land in one commit; this handoff file
  lands in a follow-up commit on top of it (see the final report for the
  exact hashes).

## Summary

Three independent small tasks, one branch, per the dispatch brief.

### Task 1: structured logging for three previously-silent failures

**(a) `sourceingest/receiver.go` — rejected source batch commits.**
`AcceptSourceBatch`'s error (409 `SOURCE_BATCH_COMMIT_REJECTED`, or 503 on
`context.Canceled`/`DeadlineExceeded`) was returned to the caller but never
logged server-side — diagnosing it required going straight to the
database. `Receiver` gained an optional `Logger *slog.Logger` field
(nil-safe, defaults to `slog.Default()`, mirroring `httpapi.NewWithConfig`'s
existing pattern) and now logs an `Error` with `source_instance_id`,
`stream_id`, `batch_id`, `sequence`, `status`, and the full `error` chain —
never the batch payload itself. Wired at the one production construction
site (`cmd/api/runtime.go`) with `Logger: slog.Default()`.

**(b) `cmd/api/runtime.go` `runWorker` — eligibility-projection failures.**
Traced whether `RunOnce` actually receives
`postgresstore.ProcessEligibilityProjectionJobs`'s error: it does —
`store.ProcessEligibilityProjectionJobs(...)` is returned directly as the
`eligibility-projection` worker spec's `RunOnce`, and that function
(`consumption.go:1969-2047`, read-only reference, not modified) returns
`processed, firstProcessingError` — the first non-`BALANCE_PROOF_PENDING`
job failure in the batch, after marking that job (and any later failures
in the same batch) `status='failed'`/`last_error_code='PROJECTION_FAILED'`
in the database and continuing to process the rest. So `runWorker` was
already logging on error, but the one line
(`"background worker failed", "worker", spec.Name, "error", err`) never
said how much of the batch actually succeeded before the failure — the
only place that count is visible at all, since only the *first* error is
ever returned. Added `"processed", processed` to the existing log call.
This is generic to every `workerSpec`, not eligibility-projection-specific,
so `email-outbox`/`source-projection`/`auth-cleanup` get the same
improvement for free. No new plumbing needed (ruled out the brief's
fallback plan B — a bespoke `eligibility_projection_jobs` poller in
`cmd/api` — since `RunOnce` already receives the error).

**(c) `httpapi/platform_login.go` `completeLogin` — successful logins.**
Failures were already logged (`server.logger.Error(...)` at three points
in this function); success fell straight through to `writeJSON` with no
log at all. Added one `Info` log right before the response:
`request_id`, `platform`, `claimed` (see Task 2 below for what "claimed"
means and how it's threaded through), and an 8-character `user_id` prefix
(`idPrefix`, a small new helper — safe on any length, never panics on a
short input even though in practice IDs are UUIDs).

### Task 2: platform username capture and display

`auth.Principal` gained a `DisplayName` field (sourced from
`auth.PlatformLoginResult.Username`, which both `sub2api_login.go` and
`newapi_login.go` already populate from the real upstream login response —
confirmed by reading both parsers). `httpapi.SessionUser` already *had* a
`DisplayName` field before this task (established by an earlier slice,
XM-INV-EMBED-SCOPE) that `sessionStatus` already merges with
`maskedEmailName`'s fallback into the existing `display_name` response
key — that plumbing needed populating, not building. `SessionUser` also
gained a new `Claimed bool` field (see below).

**Persistence investigation (per the brief's required order):**
`invoice_users` has no `display_name`/`username` column
(`migrations/0001_init.sql`, confirmed against every later migration that
touches the table); `auth_sessions` has no such column either
(`migrations/0003_auth_sessions.sql`). Per the brief's explicit fallback,
**no column was added**. Instead: `completeLogin` sets
`principal.DisplayName = strings.TrimSpace(result.Username)`;
`cmd/api/runtime.go`'s `provisionPlatformOrOIDCUser` threads it (and a new
`claimed bool`, `true` only on the claim-path return, `false` on the
create-path return) into `loadSessionUser`'s two new parameters, which set
them on the returned `SessionUser`. `ProductionAuth.LoadUser` (the
session-*reload* path, called with only a `userID`, no principal) passes
`"", false` — it has nothing else to source them from.
`sessionStatus`'s response gained a new `"username"` key alongside the
existing `"display_name"`: the **raw**, un-backfilled value (empty unless
a real captured name is on hand), specifically so a caller can tell "we
have a real name" apart from "there was nothing better to show" without
pattern-matching `maskedEmailName`'s generic `"用户"` placeholder string —
which is exactly what `embedded-scope.ts`'s `accountIdentityLabel` used to
do. It now checks `user.username` (new, always either real or absent)
instead of `user.displayName !== "用户"` (old, fragile: comparing a merged
field against a magic string that happened to describe one specific
backend fallback). `GENERIC_DISPLAY_NAME_FALLBACK` and its literal-string
comparison are gone from `embedded-scope.ts`.

**Important limitation found during implementation — read this before
assuming the username now "just shows up":** see Risk 1 below. The
short version: it is correctly wired and observably works within
`completeLogin`'s own request (proven by the Task 1c log line and by
`provisionPlatformOrOIDCUser`'s unit tests), but `GET /api/v1/auth/session`
— the *only* endpoint that actually returns user identity to the
frontend, called via `refresh()` right after every login — resolves the
user through `LoadUser(userID)` alone, with no principal, so it does not
currently observe a real username on any request other than... none. It
never does, today, without further work. This is a materially more
specific and more pessimistic finding than "shows after a fresh login,
lost on reload" (which is what the brief's fallback text anticipated) —
worth flagging clearly rather than silently shipping wiring that never
visibly does anything.

### Task 3: zero-value sync time display

Traced `lastObservedAt`'s source: `postgresstore.ListExternalAccounts`
(`identity.go:830-873`) already normalizes a genuinely-never-observed
account's SQL `COALESCE(max(fl.observed_at),'epoch'::timestamptz)` back to
Go's zero `time.Time{}` — but `httpapi/operations.go:43` then serializes
that zero value directly (`"last_observed_at": account.LastObservedAt`),
which JSON-marshals as the literal string `"0001-01-01T00:00:00Z"`. The
frontend's `mapSourceAccount` (`http-api.ts`) passed it straight through
as `lastObservedAt`, and both render sites in `App.tsx`
(`account.lastObservedAt ? \`...${dateTime(...)}\` : "等待首次同步"`)
already had the right fallback text — they just never triggered, because
a non-empty sentinel *string* is truthy. `Intl.DateTimeFormat('zh-CN', ...)`
formatting `0001-01-01T00:00:00Z` in the `Asia/Shanghai` zone lands on
the pre-1901 Shanghai LMT offset (+8:05:43, before the modern +08:00
standard), which is the exact "1/01/01 08:05" from the bug report.

Fixed at the data-mapping boundary, not at each render call site: a new
`isUnobservedTimestamp` helper in `lib/format.ts` (checks for both the
`0001-01-01` Go-zero-time shape actually observed in production, and
`1970-01-01`/epoch defensively, per the brief, since a different query
path could plausibly emit the un-normalized epoch sentinel directly).
`mapSourceAccount` now maps a sentinel or missing `last_observed_at` to
`undefined` instead of passing it through, so both existing `App.tsx`
ternaries work correctly with no changes needed there — `SourceAccount.
lastObservedAt` was already typed `string | undefined`, so this is
exactly what callers already expected "absent" to look like. Wrote the
reproduction as a `mapSourceAccount` unit test using the literal sentinel
string traced above (not a synthetic one) before/alongside the fix, per
the brief.

## Files changed

Backend:
- `backend/internal/sourceingest/receiver.go` — `Logger` field + helper +
  the commit-rejected `Error` log (Task 1a).
- `backend/internal/sourceingest/receiver_test.go` — `captureAcceptor`
  gained a controllable `err`; new
  `TestReceiverLogsCommitRejectionWithoutLeakingPayload`.
- `backend/cmd/api/runtime.go` — `runWorker`'s log gains `processed`
  (1b); `Receiver{...}` construction gains `Logger: slog.Default()` (1a
  wiring); `loadSessionUser` gains `displayName, claimed` parameters,
  threaded from both `provisionPlatformOrOIDCUser` return points and the
  `LoadUser` closure (Task 2).
- `backend/cmd/api/runtime_test.go` — new
  `TestRunWorkerLogsProcessedCountAlongsideError` (1b); the claim-path and
  create-path provisioning tests extended with `DisplayName` on the input
  `principal` and `DisplayName`/`Claimed` assertions on the result (2).
- `backend/internal/auth/oidc.go` — `Principal` gains `DisplayName string`
  (Task 2).
- `backend/internal/httpapi/platform_login.go` — `completeLogin` sets
  `principal.DisplayName`, logs the new success `Info` line, new
  `idPrefix` helper (1c, 2).
- `backend/internal/httpapi/platform_login_test.go` — new
  `TestPlatformLoginSuccessLogsStructuredInfo` (1c); the `platformLoginServer`
  test helper's `ProvisionUser` fake now propagates `DisplayName`;
  `TestPlatformLoginSuccessIssuesSessionWithPlatformIdentity` extended
  with an explicit, commented assertion of the session-reload limitation
  (Risk 1).
- `backend/internal/httpapi/production_auth.go` — `SessionUser` gains
  `Claimed bool` (1c/2); `sessionStatus` response gains the raw
  `"username"` key (2).

Frontend:
- `web/src/lib/format.ts` — new `isUnobservedTimestamp` (Task 3).
- `web/src/lib/http-api.ts` — `BackendSourceAccount` exported;
  `mapSourceAccount` exported and normalizes `last_observed_at` (3);
  `BackendSession.user` gains `username?`; `mapSession` maps it through
  (2).
- `web/src/lib/invoice-contract.test.ts` — new `"source account HTTP
  contract"` describe block: real timestamp passthrough, both sentinel
  shapes, and a missing field, all normalizing to `undefined` (3).
- `web/src/types.ts` — `AuthUser` gains `username: string | null` (2).
- `web/src/lib/mock-api.ts` — `mockSession.user` gains `username: null`
  (TypeScript forced this; the demo session has no captured username).
- `web/src/lib/embedded-scope.ts` — `accountIdentityLabel` keys off the
  new `user.username` instead of the `displayName !== "用户"` literal
  comparison; `GENERIC_DISPLAY_NAME_FALLBACK` removed (2).
- `web/src/lib/embedded-scope.test.ts` — `baseUser` fixture gains
  `username: null`; the "prefers a real name" test now sets `username`
  instead of `displayName`; added an explicit regression-guard test that
  a generic `display_name` alone (no `username`) still falls back to the
  platform+ID form (2).
- `docs/handoffs/XM-INV-OBS-BUNDLE.md` — this file.

**Not touched:** `release/`, `scripts/`, `RELEASE-READINESS.md`,
`docs/PRODUCTION-RUNBOOK.md`, `docs/IMAGE-SCAN-REVIEW.md`,
`docs/superpowers/`, any RC/version file, any database migration,
`backend/internal/postgresstore/consumption.go`,
`backend/internal/postgresstore/source_sync.go`,
`backend/internal/application/source_processor.go` (per the brief — a
parallel agent is on these), `AuthProvider.tsx`/`LoginPage`'s session
state flow (see Risk 1 — deliberately not touched), no server/production
connection of any kind, no sub-agents or forks dispatched.

## Tests run

Backend (from `backend/`, `GOFLAGS=-buildvcs=false`):

```
go build ./...                                              # exit 0, clean
go vet ./...                                                 # exit 0, clean
env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy \
    -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy \
    INVOICE_TEST_DATABASE_URL="postgres://postgres:test@127.0.0.1:55432/invoice_test?sslmode=disable" \
    go test -p 1 -count=1 ./...                              # exit 0, 27/27 packages ok (incl. postgresstore integration, 41s)
```

`gofmt` on every touched `.go` file: raw `gofmt -d` against the working
tree flags whole-file diffs (this checkout's known CRLF-vs-LF false
positive, not a real formatting issue — see `[[windows-toolchain-quirks]]`
memory). Re-verified by stripping `\r` from each touched file into a temp
copy and re-running `gofmt -d`: clean on all 8 files. Since this repo's
`.gitattributes` has no `eol=lf` rule for `*.go` (unlike `.sh`/`.ps1`/
`.sql`), the committed blob is whatever `core.autocrlf` normalizes to —
LF — so this normalized check is what CI will actually see.

Frontend (from `web/`; `npm ci --prefer-offline` installed a fresh,
independent `node_modules` in this worktree — not a junction to the main
checkout — in 8s with no EPERM issues, unlike this repo's prior
pnpm-workspace pain; this project uses plain npm):

```
npm run typecheck    # tsc --noEmit x2, exit 0, clean
npm test              # vitest: 3 files, 40 tests, all pass
                       #   invoice-contract.test.ts   19 (16 pre-existing + 3 new source-account tests)
                       #   portal-navigation.test.ts   2 (pre-existing, untouched)
                       #   embedded-scope.test.ts     19 (17 pre-existing, 1 changed, 1 new)
npm run build         # tsc --noEmit x2 + vite build, exit 0, clean
```

## Not run

- **Browser/e2e verification.** Not clicked through in a real or headless
  browser. Not required by the dispatch brief's explicit gate list (build/
  vet/test/typecheck/test/build); the reported symptoms (zero-value sync
  time, generic-only account label) are both covered by unit tests that
  reproduce the exact backend-observed values, not just plausible
  stand-ins. This repo has no component-rendering test harness (no
  jsdom/`@testing-library/react`/`.test.tsx` files anywhere in `web/src`,
  same note as the two prior handoffs on this branch).
- **Multi-replica/production cache behavior** for Risk 1 below — not
  applicable, since no cache was built.

## Risks / things to sign off on

1. **The username plumbing is correct but, as implemented, is not
   currently visible in the running application at all — this is more
   pessimistic than the brief's anticipated "shows after a fresh login,
   lost on reload" fallback, and worth a deliberate decision rather than
   quietly shipping inert wiring.** Concretely: `POST
   /api/v1/auth/platform-login`'s response body is `{"ok": true}` and
   carries no user data; the frontend (`LoginPage.handleCredentialsSubmit`/
   `handleTwoFASubmit`) always follows a successful login with `await
   refresh()`, which calls `GET /api/v1/auth/session` — the only endpoint
   that actually returns identity to the client. That endpoint resolves
   the user via `ProductionAuth.LoadUser(userID)` alone (see
   `sessionStatus`), which has no principal to draw a captured username
   from — by design, since nothing persists it (this task's very
   constraint). So `principal.DisplayName`, though correctly threaded all
   the way to `SessionUser.Claimed`/`DisplayName` inside
   `provisionPlatformOrOIDCUser` (proven by `runtime_test.go`'s
   assertions and by the Task 1c log line, which *does* fire correctly
   and *is* real, working, immediately-useful observability), never
   reaches any HTTP response the frontend actually reads for display.
   `TestPlatformLoginSuccessIssuesSessionWithPlatformIdentity` now
   asserts this exact behavior explicitly (with a comment explaining why)
   rather than leaving it as an untested gap. I deliberately did not
   build a server-side cache (userID → captured name, bridging the
   login→session-fetch boundary) or touch `AuthProvider.tsx`'s `refresh()`/
   session-merge logic to work around this: either would be a materially
   bigger, riskier change (new stateful component, or touching the one
   piece of shared state every page in the app depends on) than what a
   three-small-things logging/display bundle should carry, and neither
   was named in the dispatch brief's explicit file list. **This needs a
   decision**: is the current state (correct wiring, zero current
   end-user visibility, ready for either a future DB column or a
   deliberately-scoped cache to complete it) acceptable to land as-is, or
   should a follow-up task be opened immediately for one of those two
   completions? I did not pick one myself since it's a real
   architecture choice (persistence vs. cache vs. defer), not a
   logging-only decision.
2. **`Claimed` on `SessionUser` is new surface** purely for Task 1c's log
   line (never serialized to any HTTP response — `sessionStatus` builds
   its JSON map by hand and doesn't include it). Low risk, but flagging
   since it's a field addition to a struct several other tests construct
   literally (`production_auth_test.go`, `server_test.go`) — all confirmed
   still compiling and passing (zero value `false`, no behavior change
   for them).
3. **`runWorker`'s `processed` addition is generic to all four worker
   specs**, not eligibility-projection-specific — confirmed this is
   harmless (email-outbox/source-projection/auth-cleanup workers return
   `(int, error)` the same way already) rather than scoping the change to
   one worker by name.
4. No server/production contact of any kind; no migration; the three
   forbidden files (`consumption.go`, `source_sync.go`,
   `source_processor.go`) untouched — confirmed via `git status` before
   writing this document. Ran directly, single agent, no sub-agents/forks.

## Follow-ups (recommended, not blocking this task's delivery)

1. **Resolve Risk 1** — either accept a DB column after all (the brief's
   first-choice design, ruled out only by explicit instruction), or scope
   a small server-side cache (`userID` → captured name, short TTL,
   populated in `provisionPlatformOrOIDCUser`, read as a fallback
   enrichment in the `LoadUser` closure) as its own reviewed task. Either
   makes the wiring this task built actually visible end-to-end.
2. Consider the same `Claimed` signal for OIDC logins' first-login-vs-
   returning distinction if that ever becomes operationally interesting
   (today it is always `false` for an OIDC principal, since the claim
   path is gated on `principal.Platform != ""`).
3. `embedded-scope.ts`'s account badge and `sessionStatus`'s `"username"`
   key will both start actually reflecting real data automatically, with
   no further frontend change, the moment Follow-up 1 is resolved — that
   was the point of keying off presence/absence of the raw field instead
   of a fallback-string literal.
