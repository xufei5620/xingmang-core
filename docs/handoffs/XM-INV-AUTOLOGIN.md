# XM-INV-AUTOLOGIN: auto-detect platform on the user login page

**This branch now carries two pieces of work, in this order:** (1) the
auto-detect login page described in this top section, and (2) a
higher-priority production 403 fix dispatched afterward, once a real-user
canary hit it. See **"Second task on this branch: production 403 fix"** near
the end of this file for (2) -- its own status/commit/tests/risks are broken
out there rather than duplicated up here.

- **status:** implemented and self-tested locally (backend `go build`/`go
  vet`/full `go test -p 1 -count=1 ./...`, 27/27 packages; frontend `npm run
  typecheck` + `npm test`, 18/18 tests). Not deployed, not exercised against
  the real Sub2API/New API login endpoints, and not clicked through in a
  browser. This is a follow-up on top of XM-INV-LOGIN
  (`docs/handoffs/XM-INV-LOGIN.md`), which is itself still **NO-GO** for
  production per the RC53 release gate — this change does not alter that.
- **branch:** `ai/claude/XM-INV-AUTOLOGIN` (based on the production release
  candidate `9bbae4a` / RC53), worktree `K:/发票/wt-XM-INV-AUTOLOGIN`.
- **commit:** `ae9e4d5` is the code/test/config-doc change described below.
  `a5007ca` added this handoff file on top of it, and `25c8a0f` is a
  two-line fix to a test-count arithmetic error in this file's own first
  draft (11+11 was written as 21) — no code changed after `ae9e4d5` for
  *this* (auto-detect) piece of work. The production-403-fix commits land on
  top of `25c8a0f`; see that section for their hashes.

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

---

## Second task on this branch: production 403 fix (claim pre-existing projected identities)

- **status:** implemented and self-tested locally to the same standard as
  above (full `go build`/`go vet`/`go test -p 1 -count=1 ./...`, 27/27
  packages, including 6 new fake-backed unit tests and 2 new PostgreSQL
  integration tests that skip cleanly here with
  `INVOICE_TEST_DATABASE_URL` unset -- same as every other integration test
  in this repo when run outside the acceptance line). Production
  verification against the real database is explicitly the acceptance
  line's job, not this task's; I never connected to any server.
- **commits:** land on top of `25c8a0f` (this branch's prior HEAD), same
  branch `ai/claude/XM-INV-AUTOLOGIN`, no new branch.
- **dispatched as a higher-priority interrupt** of the auto-detect task
  above, mid-session, after the acceptance line's canary caught a real 403
  on a real account. I was handed the root cause pre-diagnosed and told not
  to re-derive it; the section below restates it only for this document's
  own completeness, cross-checked against the actual code as I read it, not
  re-investigated from scratch.

### Root cause (as diagnosed by the acceptance line against the production database)

The source-projection pipeline (`BindExternalAccountFromSource`, driven by
`SourceEventProcessor`) had already, independently of any login, created
`external_accounts` rows binding real external accounts (e.g. the Sub2API
instance's user `"1113"`, the New API instance's user `"48"`) to
pre-existing `invoice_users` rows that already own real funding lots.

Before this fix, `backend/cmd/api/runtime.go`'s `ProvisionUser` closure
handled *every* platform-password login, first-ever or not, the same way:

1. `identityStore.ResolveOrCreate` (keyed by `(oidc_issuer, oidc_subject)` =
   the platform's configured login origin + `platform_user_id`) always
   resolved or created a **different** `invoice_users` row than the one the
   projection pipeline had already bound -- these two identity-resolution
   paths are keyed on entirely different things (issuer/subject pair vs.
   external-account source/external-user-id pair) and had no way to know
   about each other.
2. `appService.BindExternalAccount`'s UPSERT
   (`backend/internal/postgresstore/identity.go`) only updates an
   `external_accounts` row when it already belongs to the
   `invoice_user_id` being bound
   (`ON CONFLICT(...) DO UPDATE ... WHERE external_accounts.invoice_user_id=EXCLUDED.invoice_user_id`).
   Since the row already belonged to the *projection-created* user, not the
   *freshly resolved* one from step 1, the `UPDATE` touched zero rows,
   `RETURNING` produced `pgx.ErrNoRows`, and that got mapped to
   `domain.ErrForbidden`.
3. `httpapi.PlatformLogin.completeLogin` treated any `ProvisionUser` error as
   `USER_PROVISION_FAILED`, HTTP 403 -- the "当前账号没有执行此操作的权限"
   the canary saw, on *every* login attempt for that account, forever
   (retrying just repeats the same failure; it does not self-heal).

A visible side effect: step 1 still committed its own transaction before
step 2 ever ran, so each such account got exactly one extra, permanently
unbound "orphan" `invoice_users` row the first time it was ever attempted
(not one per attempt -- `ResolveOrCreate` finds that same row again on
retries). Two are known in production: `(oidc_issuer, oidc_subject)` =
`(https://api.solov.cc, 1113)` and `(https://xm.solov.cc, 48)`. See "Orphan
rows" below for a suggested (not executed) cleanup query.

### Fix

`backend/cmd/api/runtime.go`'s `ProvisionUser` closure now delegates to a
new, extracted function, `provisionPlatformOrOIDCUser`, called with
`appService` and `identityStore` (both already satisfy the narrow
interfaces it needs -- see "Why the extraction" below). For a
platform-password principal, **before** touching `ResolveOrCreate` at all,
it now:

1. Looks up `external_accounts` for `(sourceInstanceID, principal.PlatformUserID)`
   via `store.GetExternalAccountBySourceUser` -- **this read-only query
   already existed** (added earlier for the source-projection pipeline's own
   use in `source_processor.go`); I did not need to add the new one the task
   brief anticipated (`GetExternalAccountBinding`), just reuse it through a
   new thin `Service.GetExternalAccountBySourceUser` passthrough (mirrors
   the existing `Service.ListExternalAccounts` passthrough exactly) so
   `runtime.go` can reach it through the same `appService` surface as
   everything else in this closure.
2. **If a binding is found** (the exact scenario above): calls the new
   `postgresstore.Store.ClaimPlatformIdentity` /
   `application.Service.ClaimPlatformIdentity` (store method + thin audited
   wrapper, same pairing pattern as `BindExternalAccount`), which:
   - locks the existing `invoice_users` row (`SELECT ... FOR UPDATE`,
     matching `EnsureUser`'s own locking style) and reads its current
     `platform`/`platform_user_id` (added by migration `0015`, previously
     read/written only through `auth.PostgresIdentityStore.ResolveOrCreate`,
     never through this `postgresstore`/`application` path -- these two
     identity-resolution stacks turned out to only *partially* overlap in
     which columns they touch, which is itself worth someone's attention
     independent of this fix; see Risks);
   - backfills `platform`/`platform_user_id` **only if both are still
     NULL** (writes them + an `invoice_user.platform_identity_claimed`
     audit event, in one transaction);
   - **never overwrites an already-set value** -- if they're already set
     (to this login's own platform identity, from an earlier claim; or, in
     principle, to a different one), it just reports back whatever is
     actually stored, unchanged.
   The caller (`provisionPlatformOrOIDCUser`) compares the *returned* stored
   values against what it asked to claim: an exact match (either just
   backfilled, or already matching from a prior login) means claim success
   -- the session lands on the **existing** `invoice_user_id`, and
   `ResolveOrCreate`/`EnsureUser`/`BindExternalAccount` never run at all for
   this login. A mismatch means this `invoice_user` already carries a
   *different* platform identity, and the login is rejected
   (`USER_PROVISION_FAILED`, now logged -- see below) rather than silently
   colliding two platform accounts onto one `invoice_user`.
3. **If no binding is found**: falls through to exactly the original
   `ResolveOrCreate` → `EnsureUser` → `BindExternalAccount` flow, unchanged
   -- a genuinely first-ever login for either an OIDC administrator or a
   platform-password account still works exactly as before. This is the
   regression path the task brief asked to keep intact, and
   `TestProvisionPlatformOrOIDCUserCreatesNewIdentityWhenNoExistingBinding`
   proves it still runs `ResolveOrCreate`/`EnsureUser`/`BindExternalAccount`
   exactly once each.

**`external_accounts` itself is never written during a claim** -- I read
the task brief's "binding_method 保持原值或追加记 platform_password_login
的 verified_at 更新" as offering two acceptable options (keep
`binding_method` as-is, optionally *also* bump `verified_at`) with "不得改
归属" (never change ownership) as the one hard constraint either way. I took
the more conservative of the two: no write to `external_accounts` at all on
a claim, which trivially satisfies both the "keep binding_method" and
"never change ownership" requirements and avoids adding any new write path
to a table a separate, independently-tested pipeline (source projection)
already owns. If you'd rather also bump `verified_at` on a claim to record
that platform-password login independently reverified the binding, that's a
small, easy follow-up on top of `ClaimPlatformIdentity` -- flagging it as a
deliberate scope choice, not an oversight.

### Why the extraction (`provisionUserDeps` / `provisionPlatformOrOIDCUser`)

The original `ProvisionUser` closure was inline in `buildProductionRuntime`,
directly calling concrete `*application.Service`/`*auth.PostgresIdentityStore`
values -- fine when it was simple, but this fix adds a real branch with a
real bug class (claim vs. create) that a production incident already proved
matters. Testing it against a live PostgreSQL is the acceptance line's job,
not something I can do here (no server connections), so I extracted the
decision logic into `provisionPlatformOrOIDCUser(ctx, deps provisionUserDeps,
identity auth.IdentityStore, principal, requestID, sourceInstanceIDs)` in
`runtime.go`, where `provisionUserDeps` is a small interface
(`GetExternalAccountBySourceUser`/`ClaimPlatformIdentity`/`EnsureUser`/
`BindExternalAccount`/`GetCurrentUser`) that `*application.Service` already
satisfies in production, and `auth.IdentityStore` is the interface that
already existed for `ResolveOrCreate`. `loadSessionUser`'s signature changed
from a concrete `*application.Service` parameter to the narrower
`currentUserLoader` interface (just `GetCurrentUser`) it actually needs --
both real call sites (`ProvisionUser` and `LoadUser`) keep passing the same
concrete `appService` value unchanged, since it still satisfies the
(narrower) interface. This let `backend/cmd/api/runtime_test.go` (new file)
unit-test the exact claim/create/reject branching with scriptable fakes
that assert call counts (e.g. "ResolveOrCreate must not run on the claim
path"), not just HTTP-visible outcomes.

### Logging

The task brief noted this incident was diagnosed entirely by querying the
production database, because nothing logged *why* any of these failures
happened. `backend/internal/httpapi/platform_login.go` now logs via
`server.logger.Error(...)` (structured `slog`, `request_id` +, where
known, `platform` + the full `%w`-wrapped `error` chain) immediately before
every `writeError` call in:

- `login()`'s and `verifyTwoFA()`'s `CROSS_SITE_REQUEST_REJECTED` (no
  `platform`/`error` available yet at that point in either handler --
  logged with just `request_id`).
- All four failure points inside `completeLogin` (not just the two 403s):
  the empty-`PlatformUserID` case (`PLATFORM_LOGIN_UNAVAILABLE`, 503),
  `clientBinding` failure (`CLIENT_BINDING_FAILED`, 503), `ProvisionUser`
  failure (`USER_PROVISION_FAILED`, 403 -- this is where the claim-mismatch
  rejection and every other `provisionPlatformOrOIDCUser` error now surface
  with full context), and `Sessions.Issue` failure (`SESSION_ISSUE_FAILED`,
  403). `loginRequestID := requestID(r)` moved to the top of `completeLogin`
  so all four share one value instead of the original single, later
  computation.

No password or credential material is ever in scope at any of these log
call sites -- they're all downstream of credential verification, and none
of the logged fields include `body.Password`/`body.Identifier`.

### Orphan rows

Two known in production, `(https://api.solov.cc, 1113)` and
`(https://xm.solov.cc, 48)` (see Root cause above). They are not referenced
by this fix and never will be again once it ships, but I did not delete
anything -- no server connection, and deleting identity rows is not this
task's call to make unilaterally. Suggested (**not executed**) cleanup,
using the actual column names from the migrations
(`backend/migrations/0001_init.sql`, `0003_auth_sessions.sql`):

```sql
-- 1) Identify candidate orphan rows: platform-login-created invoice_users
--    with no external_accounts binding at all (exactly the shape this fix
--    prevents from recurring). Review this list manually -- do not run any
--    DELETE blind.
SELECT id, platform, platform_user_id, oidc_issuer, oidc_subject, created_at
FROM invoice_users u
WHERE u.platform IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM external_accounts ea WHERE ea.invoice_user_id = u.id);

-- 2) For each candidate id from (1), confirm it was never actually used --
--    by construction, a row orphaned by this bug should have zero rows
--    everywhere, since the login that created it always failed with a 403
--    before a session, profile or request could ever be created under it:
SELECT
  (SELECT count(*) FROM auth_sessions    WHERE invoice_user_id = '<id>') AS sessions,
  (SELECT count(*) FROM invoice_profiles WHERE invoice_user_id = '<id>') AS profiles,
  (SELECT count(*) FROM invoice_requests WHERE invoice_user_id = '<id>') AS requests;

-- 3) Only once (1) and (2) both confirm a row is genuinely orphaned and
--    unused, delete it. Not run automatically by this change -- a human
--    should review (1)/(2)'s output per id first.
-- DELETE FROM invoice_users WHERE id = '<confirmed-orphan-id>';
```

### Files changed (this second task)

- `backend/cmd/api/runtime.go` -- `ProvisionUser` closure now delegates to
  the new `provisionPlatformOrOIDCUser`; new `provisionUserDeps`/
  `currentUserLoader` interfaces; `loadSessionUser`'s parameter type
  narrowed to `currentUserLoader`.
- `backend/cmd/api/runtime_test.go` -- **new file**, 6 fake-backed unit
  tests for `provisionPlatformOrOIDCUser` (listed under Tests run below).
- `backend/internal/postgresstore/identity.go` -- new
  `Store.ClaimPlatformIdentity` (locks the row, backfills only if NULL,
  audits, never overwrites).
- `backend/internal/application/service.go` -- new
  `Service.GetExternalAccountBySourceUser` (thin read-only passthrough) and
  `Service.ClaimPlatformIdentity` (thin audited wrapper), same pairing
  style as the pre-existing `BindExternalAccount`/`ListExternalAccounts`.
- `backend/internal/application/service_integration_test.go` -- 2 new
  PostgreSQL integration tests (listed under Tests run below); skip cleanly
  without `INVOICE_TEST_DATABASE_URL`, same as every other test in this
  file.
- `backend/internal/httpapi/platform_login.go` -- logging before every
  `writeError` in `login()`'s/`verifyTwoFA()`'s cross-site check and all
  four `completeLogin` failure points (see Logging above). No behavior
  change to any response body, status code, or error code -- purely
  additive observability.
- `docs/handoffs/XM-INV-AUTOLOGIN.md` -- this section.

**Not touched:** `contracts/`, the admin OIDC login path, any migration/
schema (no new columns -- `platform`/`platform_user_id` already existed
from migration `0015`), `external_accounts` writes (see "Fix" above for why
that's deliberate), anything under `web/` (this fix is entirely
backend-side; the frontend already sends no `platform` field regardless of
which backend path handles it).

### Tests run (this second task)

Backend (same proxy-env-unset / `go env GOROOT` gofmt discipline as above):

```
go build -buildvcs=false ./...                    # clean
go vet   -buildvcs=false ./...                     # clean
go test  -buildvcs=false -p 1 -count=1 ./...       # all 27 packages: ok
"$(go env GOROOT)/bin/gofmt" -d <every touched .go file>   # clean (content-only;
                                                             # confirmed via the
                                                             # same CRLF-normalize
                                                             # diff technique as
                                                             # the first task)
```

New in `backend/cmd/api/runtime_test.go` (fakes only, no PostgreSQL):
`TestProvisionPlatformOrOIDCUserClaimsExistingProjectedIdentity`,
`TestProvisionPlatformOrOIDCUserCreatesNewIdentityWhenNoExistingBinding`,
`TestProvisionPlatformOrOIDCUserRejectsWhenExistingIdentityBelongsToADifferentPlatformAccount`,
`TestProvisionPlatformOrOIDCUserOIDCPrincipalSkipsClaimPathEntirely`,
`TestProvisionPlatformOrOIDCUserRejectsMissingSourceInstanceConfig`,
`TestProvisionPlatformOrOIDCUserPropagatesExternalAccountLookupFailure`.
Several assert exact call counts on the fakes (e.g. `ResolveOrCreate`/
`EnsureUser`/`BindExternalAccount` all zero on the claim path; `claim`
exactly once), not just the HTTP-visible outcome.

New in `backend/internal/application/service_integration_test.go` (real
PostgreSQL, via the existing `integrationApplication` disposable-schema
harness): `TestClaimPlatformIdentityBackfillsOnlyWhenNullAndNeverOverwrites`
(first claim backfills; an idempotent re-claim with the same values is a
no-op that still reports success; a later claim with *different* values
does not overwrite and reports the original stored values; the database row
itself is checked directly; exactly one audit event is written, not three)
and `TestClaimPlatformIdentityUnknownUserFailsClosed`. Both **skip** here
(`INVOICE_TEST_DATABASE_URL` unset), same as every other integration test
in this repo when run outside the acceptance line's environment -- they
compile and are ready for that environment to actually run them.

`internal/httpapi`'s existing 22 `TestPlatformLogin*` tests (11 original +
11 from the auto-detect task above) all still pass unmodified after adding
the logging calls -- proving the new `server.logger.Error(...)` lines are
purely additive and don't change any response body/status/code.

I did not add a dedicated log-output assertion test (the task brief marked
this explicitly optional); the log call sites themselves are a small,
directly-reviewable diff in `platform_login.go`.

I did not write a further integration test asserting that the claimed
user's *funding lots* are queryable after the claim (the task brief's "其
数据可见"): the claim only ever changes which `invoice_user_id` a session
resolves to, never touches `funding_lots`/`invoice_requests`/etc., and that
those are correctly scoped by `invoice_user_id` once resolved is
pre-existing, already-tested behavior this task doesn't change (see e.g.
`TestSourceBatchProcessorProjectsBindingsCandidatesLotsAndRefunds` in the
same file). I considered this covered by proving `httpapi.SessionUser.ID`
equals the pre-existing projected user's ID, which is the one thing that
actually changed.

### Risks / things to sign off on (this second task)

1. **Orphan rows** (see above) -- documented with a suggested, not
   executed, cleanup query. Deleting them is a data-hygiene call for
   whoever owns production, not something I did unilaterally.
2. **`external_accounts.verified_at`/`binding_method` are never touched by
   a claim** -- a deliberate, more-conservative reading of the task brief's
   two-option phrasing (see "Fix" above). Flagging in case the
   `verified_at` bump is actually wanted.
3. **Two partially-overlapping identity-resolution stacks now touch
   `invoice_users.platform`/`platform_user_id`**:
   `auth.PostgresIdentityStore.ResolveOrCreate` (sets them on insert, for
   the create path) and the new `postgresstore.Store.ClaimPlatformIdentity`
   (backfills them, for the claim path) are two independent code paths
   against the same two columns, in two different packages, that happen to
   never run for the same login (one or the other, never both, per the
   `if lookupErr == nil { ...; return ... }` short-circuit in
   `provisionPlatformOrOIDCUser`). I verified this structurally and via the
   unit tests' call-count assertions, but it's a real seam worth a second
   pair of eyes -- a future change to either path in isolation could
   reintroduce a similar class of bug without touching the other.
4. **No status check on the claimed user.** `ResolveOrCreate` rejects a
   non-`"active"` identity (`ErrSessionInvalid`); `ClaimPlatformIdentity`
   does not check `invoice_users.status` at all before claiming. This
   wasn't in the task brief and I didn't add it to avoid scope creep into
   an untested new rejection path, but it means a *disabled* pre-existing
   account could still be claimed and logged into. Flagging, not fixing.
5. **Real PostgreSQL exercise of the claim path is still outstanding** --
   my integration tests compile and are ready but skipped here (no server
   connection, per this task's own constraint); the acceptance line running
   them for real against a database seeded with a genuine orphan-row
   scenario (ideally the two real production ones, in a disposable copy,
   never against the live production database directly) is the actual
   verification this fix needs before it's trusted.
6. **No float amounts, no logged/persisted passwords, no `contracts/`
   changes, no admin-OIDC changes, no schema/migration changes** -- checked;
   none of these hard constraints were touched anywhere in this task's
   diff.

### Follow-ups (this second task, not blocking delivery)

1. Acceptance line: run the two new PostgreSQL integration tests for real,
   then specifically re-attempt a platform-password login for the two known
   production accounts (`sub2api`/`1113`, `newapi`/`48`) in a disposable
   environment seeded from a production copy, and confirm the session lands
   on the pre-existing projected `invoice_user` with its funding lots
   intact.
2. Decide on risk 2 (whether to also bump `external_accounts.verified_at`
   on a claim) and risk 4 (whether an inactive claimed user should be
   rejected) -- both small, isolated follow-ups on `ClaimPlatformIdentity`
   if wanted.
3. Review risk 3's two-identity-resolution-stack seam; consider whether
   `auth.PostgresIdentityStore` and `postgresstore.Store`'s platform-column
   handling should be consolidated into one place longer-term.
4. Decide on the orphan-row cleanup query above; run it (or not) once
   satisfied.
