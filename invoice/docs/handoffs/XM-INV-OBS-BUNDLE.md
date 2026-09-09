# XM-INV-OBS-BUNDLE: silent-failure logging + platform username display + zero-value sync time

- **status:** implemented and self-tested locally (backend `go build`/`go
  vet`/full `go test -p 1 -count=1 ./...` with the real integration
  database, 27/27 packages; frontend `npm run typecheck`, `npm test`
  40/40 tests, `npm run build`; `gitleaks` clean on both commit history
  and working tree). Includes one schema migration (0017, see Task 2).
  Not deployed, not clicked through in a browser.
- **branch:** `ai/claude/XM-INV-OBS-BUNDLE`, based on
  `ai/claude/XM-INV-AUTOLOGIN` at `aa04e34`, worktree
  `K:/发票/wt-XM-INV-OBS`.
- **commit:** three commits on this branch: implementation/tests, the
  original handoff doc, and a follow-up implementation+doc-update commit
  resolving Risk 1 per team-lead's decision (see the final report for the
  exact hashes).

## Summary

Three independent small tasks, one branch, per the dispatch brief. Task 2
went through a second round after the first pass surfaced a real
architecture question (see "Task 2, round 2" below) that team-lead then
resolved with a decision.

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
`request_id`, `platform`, `claimed`, and an 8-character `user_id` prefix
(`idPrefix`, a small new helper — safe on any length, never panics on a
short input even though in practice IDs are UUIDs). `claimed` comes from
`SessionUser.Claimed`, set by `provisionPlatformOrOIDCUser`
(`cmd/api/runtime.go`) — `true` only on the external-account claim-path
return, `false` on the create-path return; it exists purely for this log
line and is never serialized to any HTTP response.

### Task 2: platform username capture and display

`auth.Principal` gained a `DisplayName` field, sourced in `completeLogin`
from `auth.PlatformLoginResult.Username` (which both `sub2api_login.go`
and `newapi_login.go` already populate from the real upstream login
response — confirmed by reading both parsers).

**Round 1 (session-only display, per the brief's fallback plan):** the
first pass threaded `DisplayName` through `SessionUser` and had
`sessionStatus` merge it into the response the same way
`maskedEmailName`'s fallback already worked. Tracing the actual request
flow surfaced a real problem: `GET /api/v1/auth/session` — the *only*
endpoint that returns user identity to the frontend, called via
`refresh()` right after every login — resolves the user via
`ProductionAuth.LoadUser(userID)` alone, with **no principal available**,
so the session-only design had **zero end-user visibility** in the
running app, on any request, ever — more pessimistic than "shows after a
fresh login, lost on reload." This was reported to team-lead as Risk 1
instead of silently shipping wiring with no observable effect, without
picking a fix myself (persistence vs. cache vs. defer is an architecture
decision, not a logging-only one).

**Round 2 (team-lead's decision — persist on the session row):** identity
is strictly per-platform-login (CR-0003, approved the same day), and one
invoice_user can hold sessions from different platforms with different
captured names, so the *session* is the correct owner, not
`invoice_users`. Implemented:

- **Migration `backend/migrations/0017_session_display_name.sql`**
  (0016 is reserved for the concurrent XM-INV-POLICY-ANCHOR branch, not
  yet merged here — confirmed no `0016_*.sql` exists in this worktree
  before picking 0017). Adds one nullable
  `auth_sessions.display_name_ciphertext BYTEA`, `CHECK (... IS NULL OR
  octet_length(...) BETWEEN 16 AND 4096)` — same bound convention as
  every other `*_ciphertext` column (`migrations/0004_persistent_
  application.sql`'s `email_ciphertext`, etc.). No blind-index column:
  this value is never queried by, only ever decrypted for, its own
  session row.
- **Encryption**: `auth.PostgresSessionStore` now takes a
  `securefields.Keyring` (`NewPostgresSessionStore(pool, keyring)`,
  wired from the already-loaded production `keyring` in
  `cmd/api/runtime.go`). `encryptDisplayName`/`decryptDisplayName` reuse
  `Keyring.Encrypt`/`Decrypt` directly — the exact same AES-256-GCM
  field-encryption primitive `application.Service` uses for
  `invoice_users.email_ciphertext` (`s.keys.Encrypt`/`Decrypt` in
  `service.go`/`crypto.go`), just called from the `auth` package instead
  of `application` since sessions live there. AAD is
  `"invoice-auth-session-display-name\n" + sessionID`
  (`sessionDisplayNameAAD`), mirroring `application/crypto.go`'s
  per-record AAD convention (`userEmailAAD`, `profileAAD`, etc.) — binds
  ciphertext to its own row, so it can never be replayed onto a
  different session. An empty `DisplayName` (OIDC principals; a platform
  response that returned none) stores `NULL`, never an encrypted empty
  string, matching `application/crypto.go`'s `encryptProfile` convention.
  A `NULL` ciphertext decrypts to `""` with no error (covers both a
  pre-migration session row and a session that never had a name); a
  *present but corrupt* ciphertext still fails closed as an error —
  same posture `application.Service`'s email decryption uses.
- **Plumbing**: `Session` (auth/session.go) gained a plaintext
  `DisplayName` field, decrypted by the (now method, not bare function)
  `PostgresSessionStore.scanSession` on every load — `Create`, `Rotate`'s
  old-row lookup, `AuthenticateAndTouch`, and `RevokeToken` all return it.
  `SessionManager.Issue`/`Rotate` copy `input.Principal.DisplayName` onto
  the new `Session` literal, same as `Roles`/`ACR`/`Platform` already do.
  `sessionStatus` (`production_auth.go`) now reads `current.Session.
  DisplayName` directly (decrypted by the store layer before
  `sessionStatus` ever sees it) instead of going through `SessionUser` —
  that field was removed from `SessionUser` once it became clear the real
  source of truth is the session, not a `LoadUser`-loaded user record.
  The CSRF-rotation branch inside `sessionStatus` (silent token renewal,
  not a fresh login) now carries `current.Session.DisplayName` forward
  into the `Principal` it builds for `Sessions.Rotate`, so a renewal
  never drops the name. `loadSessionUser`/`ProductionAuth.LoadUser` lost
  the `displayName` parameter they gained in round 1 (dead weight once
  the session became the source of truth) but kept `claimed` (still
  needed for the Task 1c log line).
- **Response shape unchanged from round 1**: `sessionStatus`'s existing
  merged `"display_name"` key and the new raw `"username"` key (added in
  round 1, kept as-is) both now source from `current.Session.DisplayName`
  — so they finally carry real data on every request, not just
  immediately after a login. `embedded-scope.ts`'s `accountIdentityLabel`
  (already changed in round 1 to key off `user.username`'s presence
  instead of the fragile `displayName !== "用户"` literal match) needed
  **no further frontend change** — it was already correctly wired to
  the right field, just waiting for the backend to actually populate it.

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
- `backend/migrations/0017_session_display_name.sql` — **new** (Task 2
  round 2). See above.
- `backend/internal/sourceingest/receiver.go` — `Logger` field + helper +
  the commit-rejected `Error` log (Task 1a).
- `backend/internal/sourceingest/receiver_test.go` — `captureAcceptor`
  gained a controllable `err`; new
  `TestReceiverLogsCommitRejectionWithoutLeakingPayload`.
- `backend/internal/auth/oidc.go` — `Principal` gains `DisplayName string`
  (Task 2).
- `backend/internal/auth/session.go` — `Session` gains `DisplayName`;
  `Issue`/`Rotate` copy it from `Principal.DisplayName`;
  `validateSessionRecord` bounds it (≤512 chars, no control bytes,
  trimmed) same as `PlatformUserID` (Task 2 round 2).
- `backend/internal/auth/postgres.go` — `PostgresSessionStore` gains a
  `securefields.Keyring`; `NewPostgresSessionStore`'s signature changed
  (now takes the keyring); `sessionDisplayNameAAD` +
  `encrypt`/`decryptDisplayName` helpers; `insertSessionSQL` and every
  session `SELECT`/`RETURNING` gained the ciphertext column;
  `scanSession` became a method (needs `s.keyring`) instead of a bare
  function (Task 2 round 2).
- `backend/internal/auth/postgres_integration_test.go` — both
  `NewPostgresSessionStore` call sites updated with a new
  `testSessionKeyring()` helper; new
  `TestPostgresSessionDisplayNameEncryptedRotatedAndBackwardCompatible`
  (encrypt/decrypt round trip via `Authenticate`, ciphertext-not-plaintext
  assertion, `Rotate` carrying it forward, a no-name principal storing
  `NULL` — extends the existing `TestPostgresPlatformSessionWithoutRolesOrAMR`
  empty-array pattern to this column — and a hand-inserted
  pre-migration-shaped row with the column entirely absent from the
  INSERT, proving `AuthenticateAndTouch` still succeeds with an empty
  `DisplayName` instead of erroring) (Task 2 round 2).
- `backend/internal/application/service_integration_test.go` — its one
  `NewPostgresSessionStore` call site updated with the package's existing
  `testKeys()` helper (Task 2 round 2).
- `backend/cmd/api/runtime.go` — `runWorker`'s log gains `processed`
  (1b); `Receiver{...}` construction gains `Logger: slog.Default()` (1a
  wiring); `sessionStore := auth.NewPostgresSessionStore(store.Pool(),
  keyring)` (2 round 2); `loadSessionUser` keeps only its `claimed`
  addition from round 1 (the `displayName` parameter was removed again —
  see Task 2 summary above).
- `backend/cmd/api/runtime_test.go` — new
  `TestRunWorkerLogsProcessedCountAlongsideError` (1b); the claim-path and
  create-path provisioning tests assert `Claimed` (their round-1
  `DisplayName` assertions were removed along with the `SessionUser`
  field).
- `backend/internal/httpapi/platform_login.go` — `completeLogin` sets
  `principal.DisplayName`, logs the new success `Info` line, new
  `idPrefix` helper (1c, 2).
- `backend/internal/httpapi/platform_login_test.go` — new
  `TestPlatformLoginSuccessLogsStructuredInfo` (1c); the
  `platformLoginServer` test helper's `ProvisionUser` fake reverted to
  its round-0 shape (no `DisplayName` field to set — `SessionUser` no
  longer has one); `TestPlatformLoginSuccessIssuesSessionWithPlatformIdentity`
  now asserts `user["username"]`/`["display_name"]` **do** show the real
  captured name on the `GET /session` reload (round 1 had this asserting
  the opposite, documenting the since-resolved limitation).
- `backend/internal/httpapi/production_auth.go` — `SessionUser` gains
  `Claimed bool` (1c/2) but *not* `DisplayName` (round 1 added it, round
  2 removed it — see Task 2 summary); `sessionStatus` sources both the
  merged `"display_name"` and raw `"username"` response keys from
  `current.Session.DisplayName`; the CSRF-rotation branch's `Principal`
  literal carries `DisplayName` forward.

Frontend (unchanged since round 1 — see prior handoff text above; no
frontend edits were needed for round 2):
- `web/src/lib/format.ts` — new `isUnobservedTimestamp` (Task 3).
- `web/src/lib/http-api.ts` — `BackendSourceAccount` exported;
  `mapSourceAccount` exported and normalizes `last_observed_at` (3);
  `BackendSession.user` gains `username?`; `mapSession` maps it through
  (2).
- `web/src/lib/invoice-contract.test.ts` — new `"source account HTTP
  contract"` describe block (3).
- `web/src/types.ts` — `AuthUser` gains `username: string | null`; its
  doc comment updated in round 2 to describe the now-resolved persistence
  (2).
- `web/src/lib/mock-api.ts` — `mockSession.user` gains `username: null`.
- `web/src/lib/embedded-scope.ts` — `accountIdentityLabel` keys off
  `user.username`; `GENERIC_DISPLAY_NAME_FALLBACK` removed (2).
- `web/src/lib/embedded-scope.test.ts` — `baseUser` fixture gains
  `username: null`; a real-name test and a regression-guard test added
  (2).
- `docs/handoffs/XM-INV-OBS-BUNDLE.md` — this file.

**Not touched:** `release/`, `scripts/`, `RELEASE-READINESS.md`,
`docs/PRODUCTION-RUNBOOK.md`, `docs/IMAGE-SCAN-REVIEW.md`,
`docs/superpowers/`, any RC/version file,
`backend/internal/postgresstore/consumption.go`,
`backend/internal/postgresstore/source_sync.go`,
`backend/internal/application/source_processor.go` (per the brief — a
parallel agent is on these), `invoice_users`/`auth_sessions`' other
columns, `AuthProvider.tsx`/`LoginPage`'s session state flow (never
needed — the fix landed entirely in the session-storage layer, not the
frontend), no server/production connection of any kind, no sub-agents or
forks dispatched.

## Tests run

Backend (from `backend/`, `GOFLAGS=-buildvcs=false`):

```
go build ./...                                              # exit 0, clean
go vet ./...                                                 # exit 0, clean
env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy \
    -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy \
    INVOICE_TEST_DATABASE_URL="postgres://postgres:test@127.0.0.1:55432/invoice_test_obsbundle?sslmode=disable" \
    go test -p 1 -count=1 ./...                              # exit 0, 27/27 packages ok (incl. postgresstore integration, ~40-45s)
gitleaks detect --log-opts="aa04e34..HEAD"                   # 2 commits scanned, no leaks found
gitleaks protect                                             # working tree, no leaks found
```

**Isolated database note**: the shared `invoice_test` database (the
connection string named in the dispatch brief) showed cross-agent
contention while this task ran — another agent's concurrent integration
test run was also issuing `DROP SCHEMA public CASCADE` against the same
literal `INVOICE_TEST_DATABASE_URL`, intermittently racing this task's own
schema setup (`relation "schema_migrations" does not exist`,
reproduced twice, same failure both times — not a one-off flake, and not
present at all once isolated). Created a second scratch database on the
same `invoice-test-pg` container (`CREATE DATABASE
invoice_test_obsbundle`) and pointed `INVOICE_TEST_DATABASE_URL` at it for
every subsequent test run in this task, per this repo's own documented
pattern for exactly this failure mode
(`[[windows-toolchain-quirks]]` memory, "真库集成测试必须换独立库"). One
unrelated, genuinely transient failure was also observed and confirmed
non-reproducible in isolation (`TestProviderPreflightAcceptsStrictDiscoveryWithoutReadingSecret`,
an OIDC-discovery test unrelated to any change in this task — passed
instantly on retry). The final 27/27 all-green run above is from the
isolated database. Team-lead/whoever runs CI against the shared database
name should not see this — it only reproduced under concurrent agent load
on this one shared dev container.

`gofmt` on every touched `.go` file: raw `gofmt -d` against the working
tree flags whole-file diffs for files with pre-existing CRLF content
(this checkout's known CRLF-vs-LF false positive — see
`[[windows-toolchain-quirks]]` memory). Re-verified by stripping `\r`
into a temp copy and re-running `gofmt -d`: one genuine misalignment was
caught this way (`session.go`'s `Roles:`/`ACR:`/`AMR:` line, after adding
a `DisplayName:` line shifted gofmt's column alignment for that group) and
fixed with `go fmt ./internal/auth/...` (confirmed via `git diff --stat`
that this only touched the two files with real changes,
`postgres.go`/`session.go` — the whole-package run also rewrote
`client.go` but produced a byte-identical result to what's already
committed, so it shows no diff at all). All touched files clean after.

Frontend (from `web/`; `npm ci --prefer-offline` installed a fresh,
independent `node_modules` in this worktree in round 1, reused unchanged
for round 2 since no frontend files needed further edits):

```
npm run typecheck    # tsc --noEmit x2, exit 0, clean
npm test              # vitest: 3 files, 40 tests, all pass (unchanged from round 1)
npm run build         # tsc --noEmit x2 + vite build, exit 0, clean
```

## Not run

- **Browser/e2e verification.** Not clicked through in a real or headless
  browser. Not required by the dispatch brief's explicit gate list; the
  reported symptoms (zero-value sync time, generic-only account label)
  are both covered by unit tests that reproduce the exact backend-observed
  values. This repo has no component-rendering test harness (no
  jsdom/`@testing-library/react`/`.test.tsx` files anywhere in `web/src`).
- **Multi-replica session-store behavior.** Not applicable to this
  design — the display name lives on each session row individually
  (encrypted), so there is no shared cache or in-memory state to reason
  about across replicas; every replica reads the same Postgres row.

## Risks / things to sign off on

1. **Risk 1 (username end-to-end visibility) is resolved** by team-lead's
   decision to persist on the session row. Superseded — see Task 2 above
   for the final design; nothing further needed here.
2. **The encryption key surface**: `PostgresSessionStore` now holds the
   same production field-encryption `securefields.Keyring` the rest of
   the app uses for `invoice_users`/profile PII, extending its blast
   radius to session rows too. This is the same keyring, not a new one —
   no new secret to provision or rotate, and `FIELD_KEYRING_FILE` already
   has to exist for the app to start at all. A present-but-corrupt
   `display_name_ciphertext` (e.g. after a future key rotation that drops
   an old key ID) fails the whole session load closed rather than
   degrading gracefully to "no name" — matching how `application.Service`
   already treats a corrupt `email_ciphertext` (a hard error, not a silent
   fallback) but worth flagging since a session load failure is more
   user-facing (forces re-login) than a profile-field read failure.
3. **`Claimed` on `SessionUser` remains new surface** purely for Task 1c's
   log line (never serialized to any HTTP response). Low risk — a field
   addition to a struct several other tests construct literally
   (`production_auth_test.go`, `server_test.go`), all confirmed still
   compiling and passing (zero value `false`, no behavior change).
4. **`runWorker`'s `processed` addition is generic to all four worker
   specs**, not eligibility-projection-specific — confirmed harmless
   (every worker already returns `(int, error)` the same way).
5. No server/production contact of any kind; the three forbidden files
   (`consumption.go`, `source_sync.go`, `source_processor.go`) untouched
   — reconfirmed via `git status` before writing this update. Ran
   directly, single agent, no sub-agents/forks, both rounds.
6. **Migration ordering**: this branch's `0017_session_display_name.sql`
   assumes `0016` lands from the concurrent XM-INV-POLICY-ANCHOR branch
   first (or is renumbered on merge if that branch's `0016` hasn't landed
   yet) — flagging so whoever merges checks both branches don't also
   collide on `0017`.

## Follow-ups (recommended, not blocking this task's delivery)

1. Consider the same `Claimed` signal for OIDC logins' first-login-vs-
   returning distinction if that ever becomes operationally interesting
   (today it is always `false` for an OIDC principal, since the claim
   path is gated on `principal.Platform != ""`).
2. If the field-encryption keyring is ever rotated (old key ID retired),
   confirm session rows encrypted under the retired key are expected to
   still decrypt (keys must stay in `EncryptionKeys` even after rotation,
   same requirement `invoice_users`/profile ciphertext already has) —
   this task didn't change key-rotation behavior, just extended what uses
   the keyring.
