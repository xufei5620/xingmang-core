# XM-INV-AUTOLOGIN: auto-detect platform on the user login page

- **status:** implemented and self-tested locally (backend `go build`/`go
  vet`/full `go test -p 1 -count=1 ./...`, 27/27 packages; frontend `npm run
  typecheck` + `npm test`, 18/18 tests). Not deployed, not exercised against
  the real Sub2API/New API login endpoints, and not clicked through in a
  browser. This is a follow-up on top of XM-INV-LOGIN
  (`docs/handoffs/XM-INV-LOGIN.md`), which is itself still **NO-GO** for
  production per the RC53 release gate — this change does not alter that.
- **branch:** `ai/claude/XM-INV-AUTOLOGIN` (based on the production release
  candidate `9bbae4a` / RC53), worktree `K:/发票/wt-XM-INV-AUTOLOGIN`.
- **commit:** `ae9e4d5` (all code/test/doc changes described below, one
  commit).

## Summary

User request: "登录页面能不能调整不显示两个平台呢？用户根据对应的登录账号密码
自动检索登录相应的平台？" — stop asking the user to pick Sub2API or New API up
front; let them just enter their existing account + password and have the
backend figure out which platform it belongs to.

**Frontend** (`web/src/App.tsx`, `LoginPage`): removed the `segmented`
platform-tab selector and the `platform`/`platformLabel`/`identifierLabel`
state entirely. The credentials form is now one "账号" field (label +
`<small>` hint: "填写你在 SoloV API 的邮箱，或 SoloV 模型平台的用户名"), a
password field, and the subtitle now reads "使用你已有的账号密码登录，系统
自动识别所属平台。" The input's `type` changed from a platform-conditional
`email`/`text` to a plain `text` (a username is not a valid HTML `email`
value, and there is no longer a platform to condition on before submit). The
2FA step UI, the "管理员登录" toggle/OIDC button, and everything outside
`LoginPage` are unchanged.

**Wire contract**: `PlatformLoginInput.platform` and
`PlatformLoginTwoFAInput.platform` (`web/src/types.ts`) both became optional.
`web/src/lib/http-api.ts` gained two small exported pure functions,
`platformLoginBody`/`platformLoginTwoFABody` (mirroring the existing
`profileMutationBody` convention), so the "omit `platform` when
auto-detecting" behavior is unit-testable the same way the rest of this
file's request-body mappers already are — `platform: undefined` in the
returned object is dropped by `JSON.stringify`, so the wire body simply has
no `"platform"` key. `web/src/lib/mock-api.ts` needed **no changes**: its
`platformLogin`/`verifyPlatformLoginTwoFA` mocks already ignore their input
entirely.

**Backend auto-detect** (`backend/internal/httpapi/platform_login.go`):
`platformLoginRequest.Platform` is now optional.

- **Explicit `platform` in the request body** (non-empty): unchanged from
  before this task — tries exactly that platform, no fallback. This is the
  only path the 11 pre-existing `platform_login_test.go` tests exercise, and
  all of them pass unmodified, which is the regression proof for this path.
- **Omitted `platform`**: `autoDetectPlatformOrder(identifier)` picks
  candidates — `[sub2api, newapi]` if the identifier contains `@`, `[newapi]`
  otherwise (Sub2API accounts are always keyed by email, so a bare username
  is never worth sending it). `attemptLogin` tries each candidate's
  `Login()` in order and stops at the **first** of: success, a
  `RequiresTwoFA` result, or any error that is not
  `auth.ErrPlatformCredentialsInvalid`. Only a definitive
  "credentials invalid" advances to the next candidate. Concretely: an
  upstream 5xx/timeout/network error on the *first* candidate tried is
  returned immediately as "登录服务暂时不可用" — the second candidate is
  **never called** in that case, so one platform's outage can never get
  silently reinterpreted as a wrong password by falling through.
- **Rate limiting**: `rateLimitTag` is the fixed constant `"auto"` for every
  auto-detect request (regardless of which candidate(s) end up being tried),
  vs. the real platform name for an explicit-`platform` request. `Allow`/
  `RecordFailure` are each called **once per HTTP request** (not once per
  upstream candidate tried), so a submission that internally tries both
  Sub2API and New API still only consumes one slot of the caller's
  IP+identifier attempt budget — verified by
  `TestPlatformLoginAutoDetectRateLimitSharesOneBucketPerRequest`, which
  would fail if a single auto-detect request were (incorrectly) charged
  twice.
- **2FA without a client-resent platform**
  (`auth.PendingTwoFAPlatforms`, new type in
  `backend/internal/auth/platform_login.go`): when `Login` returns
  `RequiresTwoFA`, the handler now calls
  `PendingTwoFA.Remember(tempToken, matchedPlatform)` regardless of whether
  that platform was auto-detected or explicit. `POST
  .../platform-login/2fa` resolves the platform in this order: (1) an
  explicit `platform` field if the caller still sends one (backward
  compat); else (2) `PendingTwoFA.Lookup(temp_token)`; else (3) treated
  exactly like a wrong 2FA code (same generic response, same rate-limit
  accounting) — this covers a forged/unknown/expired `temp_token` without a
  distinguishable error. The store is bounded and TTL-pruned (10 minutes,
  fixed, not env-configured — this is internal bookkeeping, not an operator
  tuning knob), same shape as the pre-existing `LoginRateLimiter`. A
  successfully-verified `temp_token` is `Forget()`-ed; a wrong code leaves
  the entry in place so the browser can retry with the same token.
- **Response/session carries the actually-matched platform**: unchanged
  mechanism — `completeLogin` was already called with a `platform`
  parameter, which now always receives `matchedPlatform` (the candidate that
  actually authenticated), not a client-asserted one. `GET
  /api/v1/auth/session`'s existing `user.platform` field is therefore
  correct for an auto-detected login with no new code needed there.
- **`PlatformLogin.Validate()`** now also requires `PendingTwoFA != nil`,
  matching the existing fail-closed style for `RateLimiter`/`Auth`/the two
  authenticators. `backend/cmd/api/runtime.go`'s `buildPlatformLogin` wires
  `PendingTwoFA: auth.NewPendingTwoFAPlatforms(10 * time.Minute)`.

## Files changed

- `backend/internal/auth/platform_login.go` — added `PendingTwoFAPlatforms`
  (`NewPendingTwoFAPlatforms`/`Remember`/`Lookup`/`Forget`), a bounded,
  TTL-pruned, mutex-guarded map, same shape as the pre-existing
  `LoginRateLimiter` in the same file.
- `backend/internal/httpapi/platform_login.go` — `PlatformLogin` gained a
  `PendingTwoFA *auth.PendingTwoFAPlatforms` field (required by
  `Validate()`); `login()` and `verifyTwoFA()` rewritten for the optional
  `platform` field as described above; new `autoDetectPlatformOrder`,
  `platformAutoDetectTag`, and `(*PlatformLogin) attemptLogin` helpers.
  `handleAuthenticatorError`/`completeLogin`/everything below them in the
  file is byte-for-byte unchanged.
- `backend/internal/httpapi/platform_login_test.go` — added
  `PendingTwoFA: auth.NewPendingTwoFAPlatforms(10*time.Minute)` to the
  `platformLoginServer` test-runtime helper (otherwise `Validate()` now
  fails); added 11 new tests (listed under Tests run below). All 11
  pre-existing tests are otherwise untouched.
- `backend/cmd/api/runtime.go` — `buildPlatformLogin` wires the new
  `PendingTwoFA` field.
- `docs/CONFIGURATION.md` — rewrote the "Platform-password login (CR-0004)"
  section's opening paragraph to describe auto-detect instead of a
  user-chosen platform; env vars/Turnstile/ambiguous-failure paragraphs
  below it are unchanged.
- `web/src/App.tsx` — `LoginPage` rewritten (see Summary above); no other
  component touched.
- `web/src/types.ts` — `PlatformLoginInput.platform` and
  `PlatformLoginTwoFAInput.platform` changed from required to optional,
  with a doc comment on each explaining why.
- `web/src/lib/http-api.ts` — added `platformLoginBody`/
  `platformLoginTwoFABody` (exported pure functions); `platformLogin`/
  `verifyPlatformLoginTwoFA` now call them instead of inlining the body
  object literal. No behavior change to `requestJSON`, CSRF handling, or
  anything else in the file.
- `web/src/lib/invoice-contract.test.ts` — added 3 tests for
  `platformLoginBody`/`platformLoginTwoFABody` (omitted vs. explicit
  `platform`).
- `docs/handoffs/XM-INV-AUTOLOGIN.md` — this file.

**Not touched**: `web/src/lib/mock-api.ts` (its mocks already ignore
`platform`), `contracts/` (no changes; same scope conclusion as
XM-INV-LOGIN — this is invoice-system's own login surface, not the
source-agent financial-sync bridge protocol), the admin OIDC login path
(`production_auth.go`, `RequireAdmin`, the "管理员登录" button/flow — all
structurally untouched), migration `0015` / any schema (this task adds no
new columns or tables; `PendingTwoFAPlatforms` is purely in-memory, matching
`LoginRateLimiter`'s existing single-replica V1 topology).

## Tests run

Backend (from `backend/`, proxy env vars unset first — Windows/httptest
quirk, see prior handoffs):

```
go build -buildvcs=false ./...                    # clean
go vet   -buildvcs=false ./...                     # clean
go test  -buildvcs=false -p 1 -count=1 ./...       # all 27 packages: ok
"$(go env GOROOT)/bin/gofmt" -d <every touched .go file>   # clean after
                                                             # normalizing this
                                                             # Windows checkout's
                                                             # CRLF (see prior
                                                             # handoff/memory —
                                                             # confirmed a pure
                                                             # line-ending
                                                             # false positive,
                                                             # not a real
                                                             # formatting diff,
                                                             # by diffing
                                                             # gofmt's output
                                                             # against the file
                                                             # with \r stripped)
```

`internal/httpapi` now has 22 `TestPlatformLogin*` tests (11 pre-existing,
unmodified, still passing — the explicit-`platform` regression proof; 11
new): `TestPlatformLoginRuntimeRequiresPendingTwoFAStore`,
`TestPlatformLoginAutoDetectEmailIdentifierTriesSub2APIFirst`,
`TestPlatformLoginAutoDetectFallsBackToNewAPIWhenSub2APIRejectsEmailShapedIdentifier`,
`TestPlatformLoginAutoDetectUsernameIdentifierOnlyTriesNewAPI`,
`TestPlatformLoginAutoDetectBothPlatformsInvalidIsGeneric`,
`TestPlatformLoginAutoDetectUpstreamUnavailableDoesNotFallBack`,
`TestPlatformLoginAutoDetectMissingCredentialsStillRejected`,
`TestPlatformLoginExplicitPlatformBypassesAutoDetectOrder`,
`TestPlatformLoginAutoDetectRateLimitSharesOneBucketPerRequest`,
`TestPlatformLoginAutoDetectTwoFARemembersPlatformWithoutClientResend`,
`TestPlatformLoginTwoFAWithoutPlatformAndUnknownTempTokenIsRejectedGenerically`.
All use the existing `fakePlatformAuthenticator`/`platformLoginServer`
httptest doubles (no real Sub2API/New API contact); several assert exact
authenticator call counts (`.counts()`) to prove no-call/one-call/two-call
claims rather than just checking the HTTP response.

Frontend (from `web/`, `web/node_modules` is a Windows directory junction
to `K:/发票/invoice-system/web/node_modules` — this worktree's
`package.json`/`package-lock.json` are byte-identical to the main checkout's
after normalizing line endings, confirmed by diff before linking):

```
npm run typecheck    # tsc --noEmit x2, clean
npm test -- --run    # vitest: 2 files, 18 tests, all pass (was 15 before
                      # this task's 3 new platformLoginBody/
                      # platformLoginTwoFABody tests)
```

**gitleaks 8.30.1** run against the staged diff (`gitleaks protect --staged
--verbose --redact=0`, no `.gitleaksignore`/`gitleaks.toml` in this repo,
none added): 0 leaks. The new Go test fixtures reuse the same
low-entropy dashed-word `temp_token` shape already present in
`platform_login_test.go` before this task (e.g. `"temp-token-0123456789"`),
which is on record as not triggering gitleaks's `generic-api-key` rule
(only a 16-hex-digit fixture in the *frontend* test file triggered it during
XM-INV-LOGIN's release prep, per that handoff); the new frontend test reuses
the exact same two-low-entropy-fragments-joined-at-runtime pattern the prior
task adopted for that reason.

## Not run

- **Real Sub2API/New API connectivity.** Same gap as XM-INV-LOGIN: nothing
  in this task ever contacted the real platforms. The auto-detect fallback
  logic (try Sub2API, fall back to New API on a definitive invalid-credentials
  result) is only exercised against `fakePlatformAuthenticator` doubles.
- **Browser/e2e verification of the new login form.** Not clicked through in
  a real or headless browser; `npm run typecheck`/`npm test` cover types and
  the two pure body-mapper functions, not rendered layout, the removed-tabs
  visual state, or focus/label association for the new "账号" field.
- **The repo's full `scripts/verify.ps1` gate** (if one exists in this repo
  the way it does in the sibling `xingmang-platform` project) — I ran the
  equivalent build/vet/test/typecheck steps directly instead, scoped to the
  ~90-minute time box.
- **PostgreSQL integration tests** — this task adds no schema/migration, so
  I did not set up `INVOICE_TEST_DATABASE_URL`; the full non-DB suite (27
  packages) is green, matching the "skip cleanly" baseline XM-INV-LOGIN
  already established for the DB-only tests.

## Risks / things to sign off on

1. **Email-shaped identifier tried against Sub2API before New API, always.**
   If a New API account's username happens to be email-shaped *and* an
   unrelated Sub2API account happens to exist with that exact email but a
   different password, auto-detect will burn one Sub2API upstream call
   (correctly returning "invalid credentials" for that unrelated Sub2API
   account) before falling back to New API and succeeding there. This is
   inherent to "try the more specific platform first" and does not leak
   anything (the response is identical either way, and the *fallback* case
   itself has a dedicated test,
   `TestPlatformLoginAutoDetectFallsBackToNewAPIWhenSub2APIRejectsEmailShapedIdentifier`)
   but it does mean every email-shaped login for a New-API-only account now
   costs **two** upstream calls instead of one where previously (with an
   explicit platform picker) it cost exactly one. Combined with risk 2
   below, this is worth watching.
2. **Auto-detect roughly doubles Sub2API's upstream call volume for
   email-shaped logins that turn out to belong to New API**, and every
   non-2FA login now potentially makes two sequential upstream HTTP calls
   instead of one (added latency, not just count) before the response
   reaches the browser. XM-INV-LOGIN's handoff already flagged that
   "Sub2API's own rate limit (20 req/min per IP) is shared across every
   invoice-system user" because all calls originate from this backend's one
   outbound IP; this task does not fix that (unchanged scope — still an
   ops/infra conversation with whoever owns the Sub2API deployment), but it
   does make that shared budget absorb more calls per login than before.
   Only affects the case in risk 1 (email identifier, New-API-only
   account); a bare-username login still costs exactly one call (New API
   only, Sub2API is never attempted per `autoDetectPlatformOrder`), same as
   an email identifier that is actually a Sub2API account (one call,
   succeeds immediately, no fallback attempted).
3. **`PendingTwoFAPlatforms` is in-memory, single-replica, 10-minute TTL,
   not configurable via env.** If this service is ever scaled to multiple
   replicas without a shared store, a 2FA follow-up request could land on a
   different instance than the one that remembered the platform, and (since
   the client no longer sends `platform`) would hit the "unknown temp_token"
   path and be rejected as if the code were wrong — a real regression for
   any user unlucky enough to hit a different replica between the two
   requests, in a way that pre-auto-detect (client-resent `platform`) was
   immune to. Not a concern for the documented single-replica V1 topology
   (`RELEASE-READINESS.md`), same caveat XM-INV-LOGIN already recorded for
   `LoginRateLimiter`, but worth naming explicitly since this is a *new*
   instance of that same class of gap, not a restatement of the old one.
4. **No new float amounts, no logged/persisted passwords, no `contracts/`
   changes, no admin-OIDC changes** — checked; none of these hard
   constraints were touched anywhere in the diff.
5. This is a much smaller change than XM-INV-LOGIN (no schema, no new
   external endpoints, no new identity-resolution logic — `completeLogin`
   and everything it calls is untouched), so I did not think a
   provenance-style disclosure was needed here: I wrote 100% of this diff
   myself, working directly (no sub-agents/forks dispatched, per the task's
   own constraint), and read every existing line I built on top of
   (`platform_login.go` in both packages, `runtime.go`'s
   `buildPlatformLogin`, `App.tsx`'s `LoginPage`, `http-api.ts`,
   `mock-api.ts`, `types.ts`, `api-contract.ts`) before changing it.

## Follow-ups (recommended, not blocking my delivery of this task)

1. One live smoke test per platform once this merges toward a real
   environment: an email-shaped identifier that is a real Sub2API account,
   one that is a real New-API-only account (to exercise the fallback path
   for real), a bare username, and one deliberately-wrong-password case —
   mirrors XM-INV-LOGIN's own unresolved live-smoke-test follow-up, now
   extended to also cover the fallback ordering.
2. Manual or Playwright-driven browser pass over the new single-field login
   form and the unchanged 2FA step (this repo has no component-rendering
   test harness at all yet — no jsdom/`@testing-library/react`/`.test.tsx`
   files exist anywhere in `web/src`; only pure-function `.test.ts` tests
   exist, which is what I added to). Standing up that harness is a bigger
   investment than this task's scope; flagging it rather than quietly
   skipping component coverage.
3. Talk to whoever owns the Sub2API deployment about risk 2's added call
   volume, ideally alongside XM-INV-LOGIN's already-open "IP
   allowlist/raised rate limit" follow-up — same conversation, slightly
   bigger number now.
4. If this service is ever scaled beyond one replica, `PendingTwoFAPlatforms`
   needs a shared store (Redis/DB-backed) before that happens, same as
   `LoginRateLimiter` already needs — risk 3 above.
5. Run the repo's full CI-equivalent gate (whatever that is beyond what I
   ran directly) before merge, per the repo's own release discipline.
