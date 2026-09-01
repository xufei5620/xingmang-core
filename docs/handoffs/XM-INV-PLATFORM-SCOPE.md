# XM-INV-PLATFORM-SCOPE: server-side platform scoping for platform-password sessions

## status

Implemented and tested locally against real PostgreSQL 18, with the full
non-mock backend test suite, `go vet`, and the web `typecheck`/`test`/`build`
scripts all green. Not deployed, not exercised through a real browser, and
no server/production access of any kind was made.

## branch

`ai/claude/XM-INV-PLATFORM-SCOPE`, based on `ai/claude/XM-INV-AUTOLOGIN` at
`8802e08` (the RC67 production line), worktree `K:/发票/wt-XM-INV-SCOPE`.

## commit

See the branch's commit history; the last commit adds this file. (Filled in
mechanically by the commit sequence below -- run `git log --oneline -1` on
this branch for the exact hash if it isn't visible from context.)

## Summary

CR-0003 (owner-approved 2026-09-02): a platform-password login session
(`Principal.Platform` = `sub2api` or `newapi`) must only ever see and operate
on that platform's data, enforced **server-side**, even though one
`invoice_user` can legitimately have external accounts bound on both
platforms (the same person can be a Sub2API and a New API customer at once;
the signed identity projection / platform-password claim path already
produces exactly that). Administrator (OIDC) sessions are unchanged and see
everything; a session with no platform (legacy/OIDC user session) is
unchanged.

### 1. Session platform field -- already implemented, no change made

The task brief asked for `GET /api/v1/auth/session` to expose the session's
login platform as `platform` next to `platform_user_id`. **This was already
shipped**, by XM-INV-LOGIN (the `platform` field itself) and confirmed by
XM-INV-EMBED-SCOPE (`platform_user_id`) -- `production_auth.go`'s
`sessionStatus` already returns `"platform": current.Session.Platform` in
the `user` map (line ~342), and `web/src/lib/http-api.ts`'s `mapSession`
already maps it into `AuthUser.platform`. I read the code, confirmed it, and
left it untouched. It is also already tested end-to-end through the real
`GET /api/v1/auth/session` handler by two existing tests in
`platform_login_test.go` (`TestPlatformLoginSuccessIssuesSessionWithPlatformIdentity`,
`TestPlatformLoginSessionsAreIsolatedAcrossPlatformsAndAccounts`) and by
`production_auth_test.go`'s `TestProductionOIDCLoginSessionAndCSRF` (proving
it's empty for an OIDC session). No migration was needed or added anywhere
in this task -- the whole change is query-level filtering over columns that
already exist.

### 2. Server-side scoping of `/api/v1/user/*`

**Design decision:** the task brief offered two ways to implement the
filter -- the runtime's `sourceInstanceIDs map[auth.Platform]string`
(`cmd/api/runtime.go`), or the source instance's `source_type` column. I used
**`source_type`**, not the runtime map, for three reasons: (1) it requires no
change to `cmd/api/runtime.go` or any wiring through `httpapi.Config`/
`Server`, since every query I touched already joins `source_instances`; (2)
`domain.SourceType`'s two values (`"sub2api"`/`"newapi"`) are byte-identical
to `auth.Platform`'s, so the session's platform converts to the scoping
value with a plain type conversion, no lookup table; (3) it scopes by
platform *type*, which stays correct even if the "exactly one enabled source
instance per platform" assumption (documented as an existing risk in
XM-INV-LOGIN's handoff) ever stops holding, whereas the runtime map only
ever knows about *one* pinned instance ID per platform.

**Mechanism:** a new `sessionPlatform(r *http.Request) domain.SourceType`
helper in `internal/httpapi/server.go` reads `principal(r).Session.Platform`
(nil-safe: mock-mode identities carry no `*auth.Session` at all, so it
returns `""`/unscoped there, matching the existing constraint that mock mode
cannot enable `ProductionAuth`/`PlatformLogin` at all). Every affected
handler now passes `sessionPlatform(r)` as a new trailing `domain.SourceType`
parameter through `InvoiceService`/`OperationsService` down to the
`postgresstore` query, or forces it to `""` on the admin branch of a
shared user/admin handler (admin sessions never carry a platform anyway,
since platform-password login never grants the admin role and OIDC never
sets `Principal.Platform` -- the explicit zeroing is defensive, not load
-bearing). This is a **query-level predicate**, not a handler-side
post-filter, in every case:

- `ListFundingLots`, `ListExternalAccounts`, `ListEligibilitySummaries`:
  `AND ($N='' OR si.source_type=$N)` appended to the existing joined query.
- `ListRequestRecordsPage` (paged) and the non-paged `ListRequestRecords`
  fallback: a new `Platform domain.SourceType` field on
  `application.RequestPageQuery`/`postgresstore.RequestPageQuery`, threaded
  into the same dynamic `conditions`/`addArg` WHERE-builder the admin-only
  `SourceInstanceID` filter already uses (`si.source_type=$N`).
- `Submit`: a new `Platform domain.SourceType` field on
  `postgresstore.SubmitInput` (**not** on `ledger.SubmitInput`, the JSON wire
  type the client controls -- the platform is always the server-derived
  trailing argument to `InvoiceService.Submit`, never client input). Inside
  the funding-lot lock/validate loop, a lot whose `source_type` disagrees
  with a non-empty `Platform` is rejected with `domain.ErrForbidden`, in the
  exact same `if` as the pre-existing "lot belongs to someone else" check --
  same shape, same HTTP mapping (403), and since it fires inside the
  allocation-validation loop *before* any `UPDATE`/`INSERT` runs (all in one
  transaction), a submission with a mix of same-platform and other-platform
  lots is rejected atomically, never partially accepted.
- `GetRequestRecord`, `CancelRequest`, `GetInvoiceDeliveryState`: a platform
  mismatch reports `domain.ErrNotFound`, **not** `ErrForbidden` -- CR-0003's
  "never leak existence" requirement. The check sits next to (after) the
  existing ownership check, so a request that is genuinely someone else's
  keeps its pre-existing 403, while a request that is *this principal's own*
  but on the other platform now reads exactly like "does not exist", the
  same as a truly-nonexistent ID. For `CancelRequest` specifically this
  check runs *before* the version/status checks, so cancelling an
  already-issued other-platform request still 404s rather than leaking its
  real status via a different error.
- `GetDocumentForRequest`: the platform predicate is folded directly into
  the SQL `WHERE` clause (`AND ($3='' OR si.source_type=$3)`, alongside the
  existing owner/status predicates) -- a wrong-platform document is
  indistinguishable from "no such document" at the query level itself, no
  extra branching needed.

**Unaffected on purpose (per the task brief, stated explicitly here as
required):** `GET/POST /api/v1/user/profiles` and `GET
/api/v1/user/invoice-policy` are untouched -- profiles are the person's own
tax-title records with no platform content, and the eligibility policy is
global. Every `/api/v1/admin/*` handler is untouched.

### 3. Web: session-first embedded scope

`web/src/lib/embedded-scope.ts` gains `resolveEmbeddedPlatform(sessionPlatform,
urlPlatform)` -- a one-line pure function (`sessionPlatform ?? urlPlatform`)
with its own unit tests. `App.tsx`'s previous module-level `embeddedPlatform`
constant (parsed once from the initial URL) is renamed to
`urlEmbeddedPlatform` and kept only as (a) the fallback value and (b) what
`userRoute`/`appendEmbeddedParams` uses to build in-app navigation links --
that function runs at module scope, outside any component, and deliberately
keeps reflecting exactly what was in the original URL rather than injecting
a platform the URL never had. A new `useEmbeddedPlatform()` hook combines
`useAuth()`'s `user.platform` with `urlEmbeddedPlatform` via
`resolveEmbeddedPlatform`, and every component that scopes actual data or UI
(`PortalLayout`, `SummaryCards`, `SourceAccountStatus`, `OrdersPage`) now
calls it instead of reading the old module constant -- no other line at any
of those call sites changed, since they already just reference the local
name `embeddedPlatform`.

Net effect: once a platform-password session exists, the embedded view
scopes itself correctly even if the embedding platform's iframe URL carries
no `&platform=` param at all (the common case going forward, since the
server now enforces the boundary on its own) -- previously the tab-selector
row and per-platform lists would incorrectly stay "unscoped" without that
param. The URL param still works exactly as before for a session with no
platform.

### Docs

`docs/CONFIGURATION.md` gained a new "Embedded platform scope (CR-0003)"
subsection (there was no existing `&platform=` guidance in this file to
remove -- I checked; XM-INV-EMBED-SCOPE never documented the param here,
only in its own handoff -- so this is additive, stating plainly that
operators no longer need to add it and that it survives only as a fallback).

## Files changed

Backend, production code:
- `backend/internal/httpapi/service_contract.go` -- `InvoiceService`/
  `OperationsService` interface signatures gain the trailing
  `domain.SourceType` parameter (see design above).
- `backend/internal/httpapi/server.go` -- new `sessionPlatform(r)` helper;
  `listLots`, `listRequestsPage`, `getRequest`, `submitRequest`,
  `cancelRequest`, `downloadDocumentForRole` pass it through.
- `backend/internal/httpapi/operations.go` -- `listSourceAccounts`,
  `listUserEligibilitySummary`, `getDeliveryState` pass it through.
- `backend/internal/application/service.go` -- `RequestPageQuery` gains
  `Platform`; `ListFundingLots`, `Submit`, `Cancel`, `GetDocumentForRequest`,
  `GetInvoiceDeliveryState`, `ListRequests`, `GetRequest`,
  `ListExternalAccounts`, `ListUserEligibilitySummaries`,
  `ListRequestsPage` thread the new parameter; two admin-only
  `GetRequestRecord` call sites (`GetIssueSnapshot`, `AttachDocument`) pass
  `""` explicitly.
- `backend/internal/postgresstore/records.go` -- `RequestPageQuery` gains
  `Platform`.
- `backend/internal/postgresstore/requests.go` -- `GetRequestRecord`,
  `ListRequestRecords`, `ListRequestRecordsPage`, `CancelRequest`,
  `GetDocumentForRequest` gain the parameter and the query-level predicate /
  404-not-403 semantics described above.
- `backend/internal/postgresstore/outbox.go` -- `GetInvoiceDeliveryState`.
- `backend/internal/postgresstore/funding.go` -- `ListFundingLots`.
- `backend/internal/postgresstore/store.go` -- `SubmitInput` gains
  `Platform`; the allocation-validation loop in `Submit` rejects a
  platform-mismatched lot.
- `backend/internal/postgresstore/identity.go` -- `ListExternalAccounts`.
- `backend/internal/postgresstore/eligibility_operations.go` --
  `ListEligibilitySummaries`.
- `backend/internal/ledger/service.go` -- the in-memory mock implements the
  identical scoping/404 semantics for parity (mock mode structurally never
  receives a non-empty platform today, since `AUTH_MODE=mock` rejects
  `ProductionAuth`/`PlatformLogin`, but the interface is shared so it must
  compile and behave correctly regardless).

Backend, test-only mechanical updates (new trailing argument/field added to
existing calls; no behavior change intended for these):
`backend/internal/httpapi/server_test.go`,
`backend/internal/ledger/service_test.go`,
`backend/internal/postgresstore/store_integration_test.go`,
`backend/internal/postgresstore/balance_carry_forward_integration_test.go`,
`backend/internal/postgresstore/eligibility_operations_integration_test.go`,
`backend/internal/application/service_integration_test.go`.

Backend, new tests:
- `backend/internal/application/platform_scope_integration_test.go` --
  **new**, the mandatory PostgreSQL integration test (see Tests run).
- `backend/internal/httpapi/session_platform_test.go` -- **new**, a small
  unit test for the one piece of new logic that lives entirely in the
  httpapi package (see Tests run).

Web:
- `web/src/lib/embedded-scope.ts` -- new `resolveEmbeddedPlatform`.
- `web/src/lib/embedded-scope.test.ts` -- 4 new tests for it.
- `web/src/App.tsx` -- `embeddedPlatform` module constant renamed to
  `urlEmbeddedPlatform`; new `useEmbeddedPlatform()` hook; `PortalLayout`,
  `SummaryCards`, `SourceAccountStatus`, `OrdersPage` call it.

Docs:
- `docs/CONFIGURATION.md` -- new "Embedded platform scope (CR-0003)"
  subsection.
- `docs/handoffs/XM-INV-PLATFORM-SCOPE.md` -- this file.

**Not touched:** `backend/internal/postgresstore/consumption.go`,
`backend/internal/postgresstore/source_sync.go` (explicitly off-limits, a
concurrent branch owns them -- I never needed to touch either; my grep of
every call site I changed confirmed neither file calls any of the methods
whose signatures changed), `cmd/api/runtime.go` (no wiring needed, per the
design decision above), any migration (none was needed), `contracts/`,
`release/`, `scripts/`, any RC/release-identity file, the admin OIDC login
path, `AuthProvider.tsx`.

## Tests run

Backend, from `backend/` (GOFLAGS=-buildvcs=false; proxy env vars unset --
this repo's known Windows/httptest quirk):

```
go build -buildvcs=false ./...      # clean
go vet   -buildvcs=false ./...      # clean
```

PostgreSQL: a disposable database (`invoice_test_platscope`) inside the
already-running `invoice-test-pg` container (`postgres:18.6-alpine`,
published on `127.0.0.1:55432`), migrated with `cmd/migrate`, used only by
this task (never the acceptance line's or another concurrent agent's
database):

```
DATABASE_URL="postgres://postgres:test@127.0.0.1:55432/invoice_test_platscope?sslmode=disable" \
  MIGRATIONS_DIR="migrations" go run ./cmd/migrate         # applied cleanly

env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy \
    -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy \
    INVOICE_TEST_DATABASE_URL="postgres://postgres:test@127.0.0.1:55432/invoice_test_platscope?sslmode=disable" \
    go test -p 1 -count=1 ./...
```

All packages: `ok` (5 `cmd/*` packages have no test files). Top-level test
counts for every touched or DB-backed `internal/*` package, from a `-v` run
of the same command: `adminsettings` 13, `application` 23 (including the new
platform-scope test), `auth` 46, `backuparchive` 3, `backupverify` 3,
`document` 18, `domain` 2, `httpapi` 56 (including the new
`TestSessionPlatform`), `ledger` 6, `mailer` 9, `migrate` 8, `oidcretention`
5, `pdfscanner` 5, `postgresstore` 42, `securefields` 4, `sourceingest` 4 --
247 top-level tests across `internal/*`, zero failures. (One run hit a
single transient `dial tcp 127.0.0.1:55432: ... connectex` failure in
`oidcretention` -- a package this task never touches, reconnecting to a
throwaway Postgres role it creates itself; re-running just that package
immediately afterward passed cleanly, and it was green in two earlier full
runs. Documented per this repo's own known "Docker Desktop port-forwarding
is intermittently broken" note, not treated as a real regression, but not
hidden either.)

`"$(go env GOROOT)/bin/gofmt" -d` on every one of the 20 Go files this task
touched or added, after stripping this Windows checkout's CRLF line endings
(confirmed pure line-ending noise on the CRLF version too, same technique as
every prior handoff in this repo) -- clean on all 20.

`gofmt -l .` (repo-wide) was **not** used as a signal, per this repo's own
documented false-positive (Windows checkout converts `.go` files to CRLF;
`gofmt` only emits LF) -- confirmed once more directly (`file
internal/httpapi/service_contract.go` reports "ASCII text, with CRLF line
terminators", 0 bare LF bytes in that file).

New: `TestPlatformScopedSessionSeesOnlyItsOwnPlatformData`
(`internal/application/platform_scope_integration_test.go`) -- one
`invoice_user` with external accounts bound on **both** Sub2API and New API
(via `service.BindExternalAccount`, the same call the identity projection /
platform-password claim path uses), funding lots and one submitted invoice
request on each platform (the New API lot required its own genuine
two-person dual-control payment review -- an unrelated, pre-existing
database-trigger-enforced business rule -- and the New API request was
fully issued with a real document, to exercise the delivery-state/document
checks against real issued data, not just "not found because not issued
yet"). Verifies, from a Sub2API-scoped call: `ListFundingLots`/
`ListExternalAccounts`/`ListEligibilitySummaries`/`ListRequests` return only
Sub2API rows; `GetRequest`/`GetInvoiceDeliveryState`/`GetDocumentForRequest`/
`Cancel` of the New API request all report `ErrNotFound`; `Submit`
referencing a New API lot is rejected with `ErrForbidden`. Mirrors every one
of those checks from a New API-scoped call (including that its own issued
request's delivery state and document *do* succeed -- proving the filter
doesn't also block legitimate same-platform access). Confirms an admin
(unscoped) call sees both platforms' requests, and a session with no
platform sees both platforms' lots and requests and can reach either
request directly.

New: `TestSessionPlatform` (`internal/httpapi/session_platform_test.go`) --
unit-tests the one piece of this task's logic that lives entirely in the
httpapi package and isn't otherwise exercised by an HTTP round-trip: a
platform-password session reports its platform, an OIDC/admin session and a
session-less mock-mode identity both report unscoped, and a request with no
identity in context at all does not panic and reports unscoped.

Frontend, from `web/` (`web/node_modules` junctioned from
`K:/发票/wt-XM-INV-AUTOLOGIN/web/node_modules` per this repo's own
documented worktree recipe):

```
npm run typecheck    # tsc --noEmit x2, clean
npm test -- --run    # vitest: 3 files, 39 tests, all pass (was 35 before
                      #   this task's 4 new resolveEmbeddedPlatform tests)
npm run build         # tsc --noEmit x2 + vite build, clean
```

## Not run

- **Browser/e2e verification.** Not clicked through in a real or headless
  browser. This repo has no component-rendering test harness (no
  jsdom/`@testing-library/react`/`.test.tsx` files anywhere in `web/src`, per
  every prior handoff's same note) -- only pure-function `.test.ts` tests
  exist, which is what this task added to, exactly as instructed.
- **Real Sub2API/New API connectivity, or any server/production contact of
  any kind.** No release, no deploy, no `scripts/verify.ps1` /
  `scripts/verify-postgres.ps1` (this task ran the equivalent Go/npm
  commands directly against a disposable database instead, per the task
  brief's own instruction to "reuse the harness" rather than run the full
  release-gate script, which also builds Docker images and exercises PG15
  source-contract compatibility unrelated to this change).
- **`consumption.go`/`source_sync.go`.** No change was needed there, and I
  did not touch them, per the task's explicit constraint.

## Risks / things to sign off on

1. **`docs/CONFIGURATION.md` never actually told operators to add
   `&platform=`** (I checked; only the XM-INV-EMBED-SCOPE handoff itself
   documented it, not this file), so my "no longer tells them to" framing is
   additive rather than a removal -- flagging in case the intent was
   specifically to *delete* existing operator-facing text that in fact never
   existed here.
2. **The New API dual-control payment-review flow and its "an admin who
   reviewed this lot's payment cannot also confirm-issue its invoice"
   separation-of-duties check (`requests.go`, unrelated pre-existing logic
   discovered while building the integration test fixture) are real,
   independent production business rules** this task's test had to satisfy
   to reach a submittable/issuable New API lot -- they are not new, not
   touched, and not part of CR-0003, but worth knowing about if this test
   ever needs to change.
3. **No new float amounts, no logged/persisted passwords, no `contracts/`
   changes, no admin-OIDC changes, no schema/migration changes, no changes
   to `consumption.go`/`source_sync.go`** -- checked; none of these were
   touched anywhere in this task's diff.
4. Ran directly, single agent, no sub-agents/forks dispatched, matching the
   task's own scope discipline. I read every existing line I built on top of
   (the full `InvoiceService`/`OperationsService` interfaces and both their
   implementations, every SQL query I added a predicate to, `App.tsx`'s
   `PortalLayout`/`SummaryCards`/`SourceAccountStatus`/`OrdersPage`, and
   `embedded-scope.ts`/`.test.ts`) before changing or building on it.

## Follow-ups (recommended, not blocking this task's delivery)

1. Browser/Playwright pass over the embedded view once a real or staged
   embed exists without a `&platform=` param, to visually confirm the
   platform tab-selector row now correctly hides itself for a
   platform-password session (this was the concrete UX gap the session-first
   change fixes, beyond defense-in-depth over the already-server-scoped
   data).
2. If `GetInvoiceDeliveryStatus` (the sibling of `GetInvoiceDeliveryState`
   that is not reachable from any HTTP handler -- see the doc comment added
   next to it in `service.go`) is ever wired up to a handler in the future,
   it will need the same `platform` parameter threaded through; today it is
   intentionally left unscoped since nothing calls it with a platform
   -scoped session.
3. Tell whoever owns the two platforms' embed-link configuration that the
   `&platform=` param is no longer necessary to add (XM-INV-EMBED-SCOPE's
   own still-open follow-up asking them to add it can now be closed instead,
   not actioned).
