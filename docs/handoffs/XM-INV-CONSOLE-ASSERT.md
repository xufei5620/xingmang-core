# XM-INV-CONSOLE-ASSERT: console-assertion admin login (CR-0006 phase 1, invoice side)

- **status:** implemented and self-tested locally (backend `go build`/`go
  vet`/full `go test -p 1 -count=1 ./...` against real PostgreSQL, 26/26
  packages; frontend `npm run typecheck`, `npx vitest run` 82/82 tests, `npm
  run build`; `scripts/test-release-image-gate.ps1` offline/static fixtures
  passing; gitleaks scan of every commit on this branch since the base --
  see "Tests run"). Not deployed, no production or server contact of any
  kind, no release ceremony run, no RC advanced. Not clicked through in a
  real browser or against a real/staged console.
- **branch:** `ai/claude/XM-INV-CONSOLE-ASSERT`, based on
  `ai/claude/XM-INV-AUTOLOGIN` at `ca4c1c0` (production RC71 line), worktree
  `K:/发票/wt-XM-INV-CONSOLEASSERT`.
- **commits:** six commits on top of the base, see `git log --oneline
  ca4c1c0..HEAD`:
  - `058fb13` feat(auth): add console-assertion verifier and keyring (CR-0006)
  - `b0103ec` feat(auth): persist console-assertion nonces for replay protection
  - `cd1c8dc` feat(httpapi): add console-assertion exchange endpoint, gate OIDC admin login
  - `177b6ca` feat(api): wire console-assertion config and OIDC_ADMIN_LOGIN_ENABLED
  - `514efd2` feat(deploy): document console-assertion env vars, ship keyring placeholder
  - `fcf7a10` feat(web): receive console-assertion postMessage, exchange it for a session
  - *(this commit)* docs(handoff): add XM-INV-CONSOLE-ASSERT handoff

## Summary

CR-0006 (`docs/change-requests/CR-0006-console-auth-for-invoice-admin.md`
and its frozen technical specification,
`docs/superpowers/specs/2026-09-02-cr0006-console-assertion-design.md`, both
in the platform repo), phase 1, 开票线 slice: adds a second admin login path
alongside OIDC/Keycloak. The xingmang platform console signs a short-lived
(≤5 minute) Ed25519-signed login credential proving "this operator recently
passed TOTP", posts it into the embedded admin iframe, and the invoice
backend exchanges it for exactly the same kind of session an OIDC login
would produce — same cookies, same `AdminPolicy`/CSRF judgment code, same
audit trail shape. `OIDC_ADMIN_LOGIN_ENABLED` (default **true**) and
`CONSOLE_ASSERTION_ENABLED` (default **false**) both ship in this phase
without changing today's production behavior: OIDC/Keycloak stays the sole
active admin login path until an operator deliberately turns the new one on
and, later and separately, turns the old one off (phase 2,
XM-INV-KEYCLOAK-RETIRE, not this slice). This is CR-0006's e/g items on the
开票线; the platform-side signing endpoint, TOTP and `EmbeddedConsoleFrame`
work (XM-AUTH-TOTP0, XM-INVCON1) is a separate slice in `xingmang-platform`,
not touched here — this slice was built against the frozen contract, not
against a live counterpart, since XM-INVCON1 had not landed as of this
commit (see Risks).

### a. Assertion verifier and keyring (`backend/internal/auth/console_assertion.go`)

`VerifyConsoleAssertion` checks the compact JWS's structure (three
dot-separated base64url segments, reusing the existing
`validateJWTJSONSegments` helper), decodes and strictly unmarshals header/
payload (`DisallowUnknownFields`, duplicate-key rejection), rejects any
`alg` other than `EdDSA` and any `typ` other than `xm-console-assertion+jwt`
outright (no algorithm negotiation), looks the header's `kid` up in a static
keyring, verifies the Ed25519 signature, then checks every claim from the
design spec's section 3.2: `exp-iat` structurally capped at 5 minutes
independent of clock-skew tolerance (a token cannot buy a longer lifetime
merely by having a valid signature); `iat`/`nbf`/`exp` within ±60 seconds of
now; `iss`/`aud` exact match against configured values; `acr` exact match
against a *fixed* protocol constant (`xingmang-console-totp-v1` — see the
ACR note below); `roles` containing the configured admin role; `amr`
containing both `pwd` and `otp`; `scope` one of `sub2api`/`newapi`/`global`;
`nonce` a well-formed 32-byte value. Nonce *replay* is deliberately excluded
from this function (a storage-layer concern) so it stays a pure,
database-free unit under test.

`ConsoleAssertionKeyring`/`ConsoleAssertionTrustedKey`/
`LoadConsoleAssertionKeyringJSON` mirror xingmang-platform's
`internal/platform/jobs/fleet_keyring.go` shape and validation rules by
hand (different module/repo, so not literally shared code): same
`key_id`/`purpose`/`protocol`/`valid_from`/`valid_until`/`revoked_at`
fields, same "at least one key, sorted, no duplicate id or reused public
material, revocation must precede expiry with a reason" rules, different
signing domain (`purpose=console_admin_assertion_signing`,
`protocol=xm-console-assertion-v1`) so a signature from one domain can
never be replayed as the other.

**The ACR reconciliation (read this before touching `AdminPolicy`):** the
assertion's own `acr` claim is checked against a *fixed* constant
(`xingmang-console-totp-v1`), not against `AdminPolicy.RequiredACR`
(today's `OIDC_REQUIRED_ADMIN_ACR`, e.g. production's `urn:solov:loa:2`).
`ConsolePrincipalFromClaims` then sets the *resulting session's* ACR to
whatever `AdminPolicy.RequiredACR` is *currently configured as* — not to
the assertion's own claim. This is the mechanism that lets OIDC and
assertion sessions coexist under one `AdminPolicy` during phase 1: since
`AuthorizeSession`'s ACR check is a single exact-value equality, two
different real ACR values (Keycloak's vs. the assertion domain's) could
never both satisfy it if either were stored literally. CR-0006's own text
says "只需把 `OIDC_REQUIRED_ADMIN_ACR` 配置成新的 `xingmang-console-totp-v1`
常量" — read literally and applied *today*, that sentence would break
Keycloak logins the moment it was applied, since phase 1 requires both
paths working *simultaneously*. I read it as describing phase 2's
steady state (once Keycloak is retired and only assertion sessions exist,
an operator *could* reconfigure `OIDC_REQUIRED_ADMIN_ACR` to that constant
for a cosmetically-matching stored value, and my "copy the current policy
value" mechanism would then naturally do that, at zero further code
change) rather than a phase-1 instruction. **Flag this explicitly for
review** — it is the one place I resolved a real tension in the frozen
spec rather than following its literal text, and the reasoning is also
recorded as a code comment on `consoleAssertionRequiredACR` in
`console_assertion.go`.

### b. Nonce replay protection (`backend/migrations/0018_console_assertion_nonces.sql`, `postgres.go`)

New table `console_assertion_nonces` (`nonce_hash` PK, `consumed_at`,
`expires_at`); `PostgresConsoleAssertionNonceStore.ConsumeNonce` uses
`INSERT ... ON CONFLICT DO NOTHING`, the same atomic single-use judgment
`oidc_backchannel_logout_events`' `(issuer_hash, jti_hash)` unique index
already uses — no TOCTOU window, proven by a 5-way concurrent-goroutine
integration test resolving to exactly one winner. `DeleteExpired` sweeps
rows more than an hour past their own `expires_at` (a troubleshooting
window, not a security requirement) and is wired into the existing hourly
`auth-cleanup` background worker.

One real, non-obvious bug this caught before it ever shipped: the first
`DeleteExpired` query (`expires_at < $1 - interval '1 hour'`) failed at
runtime with `operator does not exist: timestamp with time zone <
interval`. PostgreSQL's parameter-type inference resolves a parameter used
*only once* in `$1 - interval '1 hour'` via the `interval - interval`
overload (defaulting `$1` to `interval`) rather than the intended
`timestamptz - interval`, because nothing else in the query pins `$1`'s
type first. Fixed with an explicit `$1::timestamptz` cast; documented at
the call site since `PostgresFlowStore`'s near-identical existing query
never hits this (it uses its parameter a second time in a plain comparison
first, which pins the type before the subtraction is resolved) — worth
knowing before writing another retention query this shape.

### c. Exchange endpoint (`backend/internal/httpapi/production_auth.go`)

`POST /api/v1/auth/console-assertion` (unauthenticated, `ProductionAuth.
Register`, always registered regardless of `ConsoleAssertionEnabled` — see
"Endpoint contract" below for why): exact-Origin check, body parse, assertion
verification, nonce consumption, then the *existing, unmodified*
`ProvisionUser` closure and `Sessions.Issue` — CR-0006's "AdminPolicy/
ProductionAuth.Require/CSRFPolicy 判定代码不需要改一行" holds exactly, this
endpoint is new code that feeds the existing pipeline, not a rewrite of it.
Step-up is satisfied by the same mechanism an OIDC login already uses:
`AdminPolicy.VerifyFreshPrincipal` re-derives `mfa_at` from the assertion's
own `amr`/`AuthTime` (mapped from `iat`), so there is no bespoke step-up
branch anywhere in this endpoint.

Every rejection is audited server-side (`auth.console_assertion.rejected`,
carrying a short internal reason string) independent of the single generic
`ASSERTION_INVALID` the caller receives (design spec §5.2's deliberate
non-disclosure — distinguishing "replayed" from "malformed" externally
would turn this endpoint into a free oracle). A successful exchange is
additionally audited as `auth.console_assertion.exchanged` (operator id,
`kid`, `scope`, a truncated nonce hash) on top of the *existing*
`auth.identity.create`/`auth.session.issue` events `ResolveOrCreate`/
`Sessions.Issue` already fire unchanged.

`OIDC_ADMIN_LOGIN_ENABLED=false` (`ProductionAuth.DisableOIDCAdminLogin`,
named as a negative so the Go zero value preserves every pre-existing
caller/test's behavior) un-registers `GET /api/v1/auth/login`, `GET
/api/v1/auth/callback`, `GET /api/v1/auth/admin/step-up` and `POST
/api/v1/auth/backchannel-logout` entirely (404, not a 503 with a body) and
relaxes `Validate()` to not require `OIDC`/`Logout`. `logout()` now also
skips RP-initiated Keycloak logout for a console-assertion-issued session
(matched by `Session.Issuer == CONSOLE_ASSERTION_ISSUER` — it has
`Platform==""` just like an OIDC session, so the pre-existing
`Platform==""` check alone was not enough to tell them apart) and for
*any* session once OIDC admin login is disabled outright (there is no
`a.Logout` to call through in that case). `GET /api/v1/auth/session` now
reports `oidc_admin_login_enabled` in both branches so the frontend can
gate its own UI without a new endpoint.

**Break-glass network policy is unaffected, by construction.** This slice
does not touch `internal/adminsettings`, `server.go`'s `adminIPAllowed`, or
either of `AdminIPAllowlist`/`BreakGlassCIDRs` in any way — the existing
IP-network restriction on admin routes (`Require("admin", ...)` in
production_auth.go, checked identically regardless of whether the session
came from OIDC or a console assertion) and the separate, deployment-only
break-glass CIDR recovery path (`ADMIN_BREAK_GLASS_CIDRS_FILE`) continue to
apply exactly as they do today, for every login mechanism. This holds even
once `OIDC_ADMIN_LOGIN_ENABLED=false`: an operator physically on a
break-glass network can still reach the admin API regardless of which
login path is active, since network-level access and login-mechanism
choice are two independent, unrelated gates in the existing design.

### d. Runtime wiring (`backend/cmd/api/runtime.go`, `main.go`)

`boolEnv` (new) parses exactly `"true"`/`"false"`, erroring on anything
else — both new flags gate a login path and should never silently guess a
default from an ambiguous value like `"1"` or `"True"`. OIDC discovery
(`auth.NewOIDCClient`, a real network round-trip to the IdP at every
process start) is skipped entirely when `OIDC_ADMIN_LOGIN_ENABLED=false`,
not merely un-registered at the route level — disabling admin OIDC login
must not also require Keycloak to be reachable at boot. The resulting
`*auth.OIDCClient` is only ever assigned into the `OIDC`/`Logout` interface
fields when the flag is true, specifically to avoid wrapping a nil typed
pointer inside a non-nil interface value (the classic Go footgun) as a
second, independent guard beyond the route-registration gate.

`CONSOLE_ASSERTION_ENABLED=true` requires `CONSOLE_ASSERTION_ISSUER`/
`CONSOLE_ASSERTION_AUDIENCE` to validate and `CONSOLE_ASSERTION_KEYS_FILE`
to exist and strictly decode before the process is allowed to start ("fail
closed on malformed config", scoped to only apply when the feature is
actually meant to be used).

**On `validateIssuerReadiness`, named in the task brief's readiness item —
deliberately not touched.** I read that function before changing anything
near it: `validateIssuerReadiness`/`adminsettings.IsIssuerConfigured` check
`adminsettings.Settings.IssuerName` against the reserved placeholder
`"待配置开票主体"` — this is the invoice-issuing **business entity's legal
name** (printed on issued invoices), a completely unrelated admin setting
with no connection to OIDC or any identity provider; grep confirms no
second function of that name exists and no OIDC-related check appears
anywhere in the `Readiness` closure passed to `httpapi.Config` at all. I
believe the task brief's premise here was a naming-pattern
mis-identification (matching "issuer" without checking which "issuer"),
not an intentional instruction, so I left `validateIssuerReadiness`
untouched — changing it would have broken an unrelated invoice-business
readiness gate for no OIDC-related benefit. The actual, real dependency the
brief's intent was pointing at — "readiness/startup must not hard-require
OIDC when it is disabled" — is what the OIDC-discovery-skip two paragraphs
above addresses: `auth.NewOIDCClient` (the one real startup-time network
dependency on the IdP) is now conditional on `OIDC_ADMIN_LOGIN_ENABLED`,
and there was nothing else to gate since no OIDC-specific check exists in
the readiness callback to begin with.

## Endpoint contract

`POST /api/v1/auth/console-assertion` — **note the path differs from the
literal text of my task brief**, which wrote `/api/v1/auth/console-assertion/
exchange`. I implemented the path exactly as the frozen technical
specification states it five separate times (§§3.5, 4.1, 4.3, 5.2, 10),
matching the CR's own framing ("与今天的 `/callback` 同级") and the
*platform-side* slice's identical path for its own (different-purpose)
signing endpoint — this is the shared contract XM-INVCON1 is independently
implementing against the same document, and a path mismatch between the two
sides would be exactly the kind of drift the spec-first process exists to
prevent. Flagging this explicitly since it is a real deviation from the
literal delegation text; happy to rename if the `/exchange` suffix was
actually intentional and the spec itself needs a follow-up correction.

Request: `Origin: https://<CONSOLE_ASSERTION_ISSUER>` header (exact,
singular), JSON body `{"assertion":"<compact JWS>"}`. No CSRF token (no
pre-existing session to synchronize against) — Origin is the entire
cross-site defense here, same as the design spec specifies.

Success: `200 {"ok":true}`, `Set-Cookie: __Host-invoice_session`/
`__Host-invoice_csrf` — byte-identical cookie shape to the OIDC callback's.

Errors (all `{"error":{"code":...,"message":...}}`):

| HTTP | code | when |
|---|---|---|
| 503 | `CONSOLE_ASSERTION_DISABLED` | `CONSOLE_ASSERTION_ENABLED=false` (not in the frozen spec's own table — an addition needed because the brief asked for a default-false flag the spec didn't anticipate) |
| 403 | `ORIGIN_REJECTED` | missing/wrong/duplicate `Origin`, or non-HTTPS |
| 400 | `ASSERTION_MALFORMED` | missing/empty `assertion`, invalid JSON |
| 401 | `ASSERTION_INVALID` | every verification failure and every replay, unified (see §c above) |
| 429 | `RATE_LIMITED` | this endpoint's own 20/minute-by-IP limiter tripped |
| 403 | `USER_PROVISION_FAILED` / `SESSION_ISSUE_FAILED` | downstream provisioning/session-issue failure (matches the OIDC callback's own error codes for the same failure classes) |
| 500 | `INTERNAL` | nonce-store/binding failure |

## Config names

`OIDC_ADMIN_LOGIN_ENABLED` (bool, default `true`), `CONSOLE_ASSERTION_ENABLED`
(bool, default `false`), `CONSOLE_ASSERTION_ISSUER` (exact HTTPS URL, also
the required `Origin` value and the assertion's `iss` claim — deliberately
one value, not three that could drift), `CONSOLE_ASSERTION_AUDIENCE`
(default `xingmang-console-assertion-v1`), `CONSOLE_ASSERTION_KEYS_FILE`
(absolute path to the static reviewed keyring manifest). Documented with
safe defaults in `deploy/.env.example` and `deploy/.env.production.example`;
`docker-compose.prod.yml` is **not** touched (see Production rollout below).

## What is verified

- Signature/claim verification: well-formed acceptance; ±60s clock-skew
  boundary on `iat`/`nbf`/`exp` independently; the 5-minute structural
  lifetime cap rejecting a longer-but-validly-signed token; tampered
  payload and corrupted signature bytes; wrong `iss` (including a trailing
  slash and a case difference) / `aud` / `acr`; unknown `kid`; a `kid`
  revoked before the assertion's `iat` vs. after (only the latter is
  rejected); a `kid` not yet valid; `alg` downgraded to `none`/`HS256`/
  `RS256`/empty; wrong `typ`; `amr` missing `pwd` and/or `otp`; `roles`
  missing the configured admin role; malformed `scope`/`nonce`; mismatched
  `nbf`; malformed compact serialization (wrong segment count, non-base64,
  injected whitespace, oversized); duplicate/unknown JSON keys in either
  segment.
- Keyring manifest validation: empty manifest, non-Ed25519 algorithm,
  foreign signing domain (job-fleet's own purpose/protocol), fingerprint
  mismatch, duplicate key id, reused public-key material across two ids,
  invalid `valid_from`/`valid_until` ordering, revocation at/after expiry,
  revocation without a reason; strict JSON manifest decoding (duplicate
  top-level keys, unknown fields, trailing data).
- Nonce store: single-use consumption against real PostgreSQL, a 5-way
  concurrent race resolving to exactly one winner, and the retention
  sweep's exact boundary (>1h past expiry swept, ≤1h or not-yet-expired
  survives).
- Exchange endpoint (in-memory session store, matching this codebase's own
  existing OIDC-callback test pattern): a fresh exchange satisfies admin
  authorization and step-up on the very first request with the session's
  ACR equal to the *configured* value, not the assertion's own domain
  constant; replay rejected; wrong/missing/duplicate Origin rejected;
  feature-disabled response; rate limiting; `OIDC_ADMIN_LOGIN_ENABLED=false`
  un-registering the four OIDC routes (404) while the exchange endpoint and
  its resulting session's logout keep working without ever touching a nil
  OIDC client; OIDC login and console-assertion exchange succeeding
  side by side on one server/`AdminPolicy` (coexistence, design spec test
  #23's shape).
- Frontend: the exact valid `xm-embed`/`admin-assertion` envelope is
  accepted; 17 distinct malformed shapes are ignored rather than throwing;
  the sibling height-sync message is never mistaken for an assertion.

## Not run / not verified

- **The origin check itself in `AuthProvider.tsx`'s listener** (`event.
  origin !== XM_EMBED_CONSOLE_ORIGIN`) has no direct test — this repo has no
  jsdom/happy-dom dependency at all (confirmed via `package.json`), so no
  `MessageEvent`/`window.addEventListener` DOM simulation is possible
  without adding one, which was out of this task's scope. This is the same
  pre-existing boundary `docs/handoffs/XM-INV-ADMIN-EMBED.md`'s "Not run"
  section already recorded for the sibling popup-auth listener; the shape
  validation the origin-checked payload is handed to (`parseXmEmbedAdmin
  AssertionMessage`) is fully unit tested.
- **No real console/platform counterpart exists to test against.** XM-INVCON1
  (the platform-side signing endpoint, TOTP, and `EmbeddedConsoleFrame`'s
  `postMessage`) was not confirmed dispatched as of this commit — this
  slice was built strictly against the frozen technical specification
  document, never against a live signer. The keyring contract file
  (`contracts/auth/console-assertion-keyring.v1.json`) ships as an empty
  array for exactly this reason; nothing here has ever verified against a
  key XM-INVCON1 actually generated.
- **The `invoice_users` identity-migration tool** (CR-0006 change item f:
  a `dry-run`/`apply` CLI, same lifecycle as `cmd/eligibility-repair`, to
  move the one existing admin's `oidc_issuer`/`oidc_subject` from Keycloak's
  values to the console's once assertion login is adopted) is described in
  `docs/roadmap/CR-0006-console-auth-slices.md` as belonging to this
  slice's deliverable, but was **not** explicitly listed in my task's
  numbered scope and was not built here. Flagging this as a real gap
  against the roadmap document's fuller description — needed before anyone
  can actually cut the admin account over to assertion login in production
  (see Production rollout, step 5).
- No browser/Playwright pass of any kind (no component-rendering test
  harness exists in this repo at all, same boundary every prior invoice-web
  handoff has recorded).
- `scripts/test-release-image-gate.ps1` ran only its offline/static-fixture
  self-test (as instructed) — not a real image build.
- No server/production contact of any kind; no `release/`/
  `RELEASE-READINESS.md`/`docs/PRODUCTION-RUNBOOK.md`/`docs/IMAGE-SCAN-REVIEW.md`
  touched; no RC advancement; `scripts/release-image-gate-lib.ps1` and its
  sibling verification scripts untouched.

## Production rollout (config, keys-file custody, canary)

This slice ships **capability, not activation** — both new flags default to
today's behavior. To actually turn console-assertion login on in
production, in order:

1. **Wait for XM-INVCON1** (platform-side signing endpoint + first real
   Ed25519 keypair) to land and generate its first production key.
2. **Populate the real keyring.** Replace the empty array in both repos'
   `contracts/auth/console-assertion-keyring.v1.json` with the reviewed
   real key record (`purpose=console_admin_assertion_signing`,
   `protocol=xm-console-assertion-v1`) once XM-INVCON1 publishes it. Ship
   the manifest file to the server as a root-managed file (same custody
   discipline as `FIELD_KEYRING_FILE`/`SOURCE_TRUST_FILE`); it contains
   only public key material, no secret to protect beyond integrity.
3. **Wire the compose plumbing this slice deliberately did not touch**:
   add `CONSOLE_ASSERTION_ENABLED`/`CONSOLE_ASSERTION_ISSUER`/
   `CONSOLE_ASSERTION_AUDIENCE`/`CONSOLE_ASSERTION_KEYS_FILE` to the `api`
   service's `environment:` block in `docker-compose.prod.yml`, and a
   read-only volume mount for the keys file (same pattern as
   `SOURCE_TRUST_CONFIG_FILE:/config/source-trust.json:ro` a few lines
   above it) — a new `CONSOLE_ASSERTION_KEYS_CONFIG_FILE` host-side env var
   pointing at the deployed manifest is the natural name to match that
   existing convention. This is a real, reviewable compose change; I did
   not make it because the task brief explicitly said not to touch deploy
   compose files beyond adding env vars to the example templates.
4. **Run the CR-0006 data migration (change item f) AFTER enabling the
   assertion path, not before.** Acceptance-line ruling (2026-09-03,
   platform ACCEPTANCE-LOG): first set `CONSOLE_ASSERTION_ENABLED=true`
   with the key manifest deployed (OIDC stays enabled), then run
   `invoice-identity-migrate` (XM-INV-IDENTITY-MIGRATE: dry-run, then
   `--apply` with the approved operator) to move the existing admin's
   `oidc_issuer`/`oidc_subject` onto the console's values, and only then
   turn `OIDC_ADMIN_LOGIN_ENABLED` off. Migrating first would break the
   still-working Keycloak path for a window with no replacement; enabling
   first only risks an orphaned row if someone tries the new console path
   before the migration, which the migration tool refuses to guess about
   (see docs/handoffs/XM-INV-IDENTITY-MIGRATE.md, "Production runbook").
5. **Enable and canary.** Set `CONSOLE_ASSERTION_ENABLED=true`, redeploy,
   confirm `GET /api/v1/auth/session`'s `oidc_admin_login_enabled` still
   reads `true` (OIDC stays live throughout this step — `OIDC_ADMIN_LOGIN_
   ENABLED` does not move yet). Have the one admin operator complete a real
   console → embedded-admin login and confirm: no popup, immediate admin
   access, `auth.console_assertion.exchanged` appears in the audit log with
   the expected `kid`, and the Keycloak OIDC path still independently works
   from the standalone `/admin` entry (design spec test matrix #23/#8, now
   for real). Watch for a full release/deploy cycle before treating the
   canary as clean.
6. **Only after a clean full cycle, and only with the product owner's
   explicit phase-2 sign-off**, a separate slice (XM-INV-KEYCLOAK-RETIRE)
   flips `OIDC_ADMIN_LOGIN_ENABLED=false`, retires the Keycloak containers,
   and updates the release-image gate's image count from nine to eight —
   none of that is this slice's authorization or scope.

## Files changed

Backend:
- `backend/internal/auth/console_assertion.go` — **new**. Verifier,
  keyring, claims-to-`Principal` mapping.
- `backend/internal/auth/console_assertion_test.go` — **new**, 20 test
  functions (see "What is verified").
- `backend/internal/auth/postgres.go` — `PostgresConsoleAssertionNonceStore`
  (`ConsumeNonce`, `DeleteExpired`).
- `backend/internal/auth/postgres_integration_test.go` — new integration
  test for the nonce store.
- `backend/migrations/0018_console_assertion_nonces.sql` — **new**.
- `backend/internal/httpapi/production_auth.go` — new `ProductionAuth`
  fields, conditional OIDC route registration, `consoleAssertionExchange`
  handler and its audit helpers, `logout()`/`sessionStatus()` updates.
- `backend/internal/httpapi/production_auth_test.go` — 7 new test
  functions plus a new `consoleAssertionServer` fixture.
- `backend/cmd/api/runtime.go` — config loading, conditional OIDC client
  construction, console-assertion wiring, cleanup-worker extension.
- `backend/cmd/api/main.go` — `boolEnv` helper.

Frontend:
- `web/src/lib/embedded-admin-scope.ts` — `XmEmbedAdminAssertionMessage`,
  `parseXmEmbedAdminAssertionMessage`.
- `web/src/lib/embedded-admin-scope.test.ts` — new `describe` block, 3
  tests (one covering 17 malformed-shape variants).
- `web/src/AuthProvider.tsx` — new unconditional postMessage listener,
  `oidcAdminLoginEnabled` exposed on the context, `setSession` calls
  preserve it across a logout/session-failure transition.
- `web/src/App.tsx` — `LoginPage` hides the OIDC entry when disabled and
  shows a console pointer on the standalone `/admin` path.
- `web/src/types.ts` — `AuthSession.oidcAdminLoginEnabled`.
- `web/src/lib/api-contract.ts` — `InvoiceApiClient.exchangeConsoleAssertion`.
- `web/src/lib/http-api.ts` — implementation, `mapSession` threading.
- `web/src/lib/mock-api.ts` — stub implementation, canned session field.

Config/contracts:
- `deploy/.env.example`, `deploy/.env.production.example` — new env vars.
- `contracts/auth/console-assertion-keyring.v1.json` — **new**, empty-array
  placeholder (see "Not run").

**Not touched:** `release/`, `RELEASE-READINESS.md`,
`docs/PRODUCTION-RUNBOOK.md`, `docs/IMAGE-SCAN-REVIEW.md`,
`docs/superpowers/`, any release-identity/RC advancement,
`scripts/release-image-gate-lib.ps1`, `scripts/verify-release-image-
artifacts.ps1`, any compose file, any `contracts/` file other than the new
one, `web/src/lib/embedded-scope.ts` (the user embed's own session/scoping
code, a different, concurrently-owned module per XM-INV-ADMIN-EMBED's own
precedent).

## Tests run

Backend (from `backend/`; proxy env vars unset for `go test`/`go build`/
`go vet` per this repo's known Windows/httptest quirk;
`INVOICE_TEST_DATABASE_URL` pointed at the shared default
`postgres://postgres:test@127.0.0.1:55432/invoice_test?sslmode=disable`,
which `internal/testdb` automatically rewrote to and created a per-worktree
database, `invoice_test_wt_xm_inv_consoleassert` — my task brief suggested
manually creating `invoice_test_assert`, but that per-worktree-database
automation (XM-INV-TOOLCHAIN0) had already merged into the base branch
before this task started, so the manual step was unnecessary; noting the
actual database name used instead of the one suggested):

```
go build ./...                                              # clean
go vet ./...                                                 # clean
go test -p 1 -count=1 ./...                                  # 26/26 packages: ok
"$(go env GOROOT)/bin/gofmt" -l <every touched .go file>     # clean
```

New backend tests specifically: 20 functions in `console_assertion_test.go`
(table-driven where noted, several with sub-tests), 1 new integration test
in `postgres_integration_test.go`, 7 new functions in
`production_auth_test.go` (see "What is verified" for the full breakdown).

Frontend (from `web/`; `web/node_modules` is a Windows directory junction
to `K:/发票/wt-XM-INV-AUTOLOGIN/web/node_modules`):

```
npm run typecheck    # tsc --noEmit x2, clean
npx vitest run        # 5 files, 82 tests, all pass
npm run build         # tsc --noEmit x2 + vite build, clean
```

PowerShell (from the repo root):

```
pwsh -NoProfile -File scripts/test-release-image-gate.ps1   # PASS (offline/static fixtures)
```

gitleaks (from a plain clone of this worktree, since a worktree's `.git` is
a file some scanning setups cannot resolve — `git clone --no-local --branch
ai/claude/XM-INV-CONSOLE-ASSERT --single-branch <worktree> <tmp>`, then a
locally-installed `gitleaks.exe` (v8.30.1, no Docker pull needed this run)
`detect --source=<tmp> --log-opts=ca4c1c0..HEAD --verbose --redact=0`):
**PASS, no leaks found** across all six code commits (6 commits, ~107KB
scanned). Every Ed25519 keypair used in tests is generated at test-run time
(`ed25519.GenerateKey`), never a literal committed key.

## Risks / things to sign off on

1. **The endpoint path deviates from the literal task brief** (no
   `/exchange` suffix) — see "Endpoint contract" above for the full
   reasoning. I followed the frozen, cross-referenced spec over the more
   recent but informal chat instruction, on the theory that the spec is the
   actual shared contract the platform-side slice is independently coding
   against. Please confirm this was the right call.
2. **The ACR-reconciliation design** (assertion's own `acr` claim checked
   against a fixed constant; the resulting session's ACR copied from the
   *currently configured* `AdminPolicy.RequiredACR` instead) resolves a
   real tension between CR-0006's literal text and phase-1 coexistence
   requirements that I do not believe the frozen spec actually addressed
   explicitly — see section (a) above for the full reasoning, also recorded
   as a code comment. This is the single most load-bearing design decision
   in this slice; please review it specifically.
3. **The `invoice_users` migration tool (CR-0006 change item f) is not
   built.** The roadmap document lists it as part of this slice's
   deliverable; my numbered task brief did not mention it. I did not build
   it unprompted, per this task's own scope discipline, but it blocks
   step 4 of the production rollout above and should be tracked explicitly
   — either as a follow-up to this slice or folded into
   XM-INV-KEYCLOAK-RETIRE.
4. **Never verified against a real signer.** XM-INVCON1 was not visibly
   in progress as of this commit. Everything here is proven correct against
   the frozen written contract, not against a byte a real platform process
   ever produced. The first real integration test is the rollout's step 5
   canary, not anything in this repo.
5. **The "standalone `/admin` also uses the assertion path" instruction**
   (task brief, item 3's parenthetical) is implemented narrowly: the
   postMessage listener is unconditionally active (not gated on
   embedded-admin mode), so a standalone tab *would* accept an assertion if
   one somehow arrived — but no new mechanism was built to actually *get*
   one there (no query-parameter/redirect transport), because putting a
   bearer-shaped secret in a URL would land it in browser history and
   (absent a `location =` nginx exception) access logs, which this
   codebase's own `ExtractDesktopBearer` precedent on the Go side already
   refuses to do for exactly this reason. If a real top-level entry point
   is wanted later, it needs its own reviewed design (most plausibly: the
   console opens a popup exactly like today's OIDC step-up popup, and posts
   the assertion into it the same way — piggybacking on the *existing*
   same-origin popup infrastructure rather than a new cross-origin
   transport). Flagging this as a narrower interpretation than the literal
   words might suggest, not a silent omission.
6. Ran directly, single agent, no sub-agents or forks dispatched for
   research or implementation. No server/production contact of any kind.

## Follow-ups (recommended, not blocking this slice's delivery)

1. Build the `invoice_users` identity-migration dry-run/apply tool (CR-0006
   change item f) — blocks production rollout step 4.
2. Once XM-INVCON1 lands, replace the empty keyring placeholder with the
   real reviewed key and re-verify the exchange end-to-end against a live
   signer (not just the frozen contract) before any canary.
3. Wire `docker-compose.prod.yml`'s `environment:`/volume plumbing for the
   five new env vars — deliberately out of this slice's scope, see
   Production rollout step 3.
4. If a real top-level (non-embedded) console-assertion entry is ever
   wanted, design it as its own reviewed piece — see Risk 5.
5. Consider whether `docs/SECURITY-ARCHITECTURE.md` should gain a section
   for this new login path, mirroring §8's treatment of the embedded-admin
   popup handshake (XM-INV-ADMIN-EMBED). Not done here since it was not in
   this task's explicit scope and the endpoint is inert by default.
