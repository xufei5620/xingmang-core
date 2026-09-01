# XM-INV-ADMIN-EMBED: embed the invoice admin console in the xingmang platform console

- **status:** implemented and self-tested locally (backend `go build`/`go
  vet`/full `go test -p 1 -count=1 ./...` against real PostgreSQL, 27/27
  packages; frontend `npm run typecheck`, `npm test` 57/57 tests, `npm run
  build`; `scripts/verify-web-security-headers.ps1` passing against a live
  Docker container in its local dev-build mode). Not deployed, no production
  or server contact of any kind, no release ceremony run, no RC advanced.
  Not clicked through in a real browser or a real/staged console embed.
- **branch:** `ai/claude/XM-INV-ADMIN-EMBED`, based on
  `ai/claude/XM-INV-AUTOLOGIN` at `8802e08` (production RC67 line), worktree
  `K:/发票/wt-XM-INV-ADMIN-EMBED`.
- **commit:** eight commits on top of the base, see `git log --oneline
  8802e08..HEAD`:
  - `e8f5fb3` feat(backend): filter payment-candidate and refund-case admin
    lists by source_instance_id
  - `1f8df35` test(backend): cover the new source_instance_id filters at
    store and handler layers
  - `1e1b757` feat(web): add the embedded-admin-scope module and popup auth
    handshake helpers
  - `42309ab` feat(web): scope the admin console's nav, lists, chrome and
    auth flow to the embedded platform/global mode
  - `839f864` feat(nginx): add embedded admin entry points and scope /admin
    framing to the console origin
  - `a38e344` docs(security): document admin embed framing and the popup
    auth handshake
  - `f388556` docs(handoff): add XM-INV-ADMIN-EMBED handoff
  - `e1a7ed0` test(web): prove admin list requests carry source_instance_id
    only when scoped

## Summary

CR-0005 (`docs/change-requests/CR-0005-invoice-admin-console.md`), 开票线
slice: the xingmang platform console embeds the invoice admin console per
platform (Sub2API / New API) and once for the cross-platform governance tab,
via iframe. Authentication, RBAC, IP allowlist, dual control and audit all
stay in the invoice system unchanged; the platform side gains no new data
channel or write path (ADR-018's four read-only gates are untouched). This
is CR-0005's a-f (开票线); g-k (平台线, `ui-admin`'s `EmbeddedConsoleFrame`,
xingmang-platform nav) is a separate slice (XM-INVCON0) in a different repo,
not touched here.

### a. nginx entry points

Three new clean entry points in `deploy/nginx/invoice.solov.cc.conf.template`,
mirroring the existing `/embed/sub2api` and `/embed/newapi` (same
`access_log off` + `no-store` + 303, no bearer token to protect here since
admin auth is the existing OIDC/MFA session):

- `/embed/admin/sub2api` -> `/admin?ui_mode=embedded_admin&platform=sub2api`
- `/embed/admin/newapi` -> `/admin?ui_mode=embedded_admin&platform=newapi`
- `/embed/admin/global` -> `/admin?ui_mode=embedded_admin&scope=global`

### b. Embedded admin mode (frontend)

New module `web/src/lib/embedded-admin-scope.ts` (deliberately **not** an
extension of `lib/embedded-scope.ts`, which is the user embed's
session/scoping code the concurrent `XM-INV-PLATFORM-SCOPE` branch is
actively editing — kept separate so the merge stays clean):

- `parseEmbeddedAdminMode` / `parseEmbeddedAdminScope` parse
  `ui_mode=embedded_admin` + `platform=`/`scope=global` once from the initial
  URL into module-level `embeddedAdminMode`/`embeddedAdminScope` constants in
  `App.tsx`, the same pattern `embeddedUserMode`/`embeddedPlatform` already
  use.
- `appendEmbeddedAdminParams` (a new `adminRoute()` helper in `App.tsx`,
  parallel to the existing `userRoute()`) keeps those params attached across
  in-app navigation between admin sub-pages — the admin nav previously used
  raw `item.to` with no such preservation at all, which would have silently
  dropped the embed context on the first click.
- `isAdminNavItemVisible` decides which of the fixed admin nav entries show,
  per CR-0005 (b): platform mode renders 审核工作台/发票档案 (both tagged
  `"review"`), 资格冻结, 退款与红冲, 源健康 (client-side filtered to that
  platform's 5 streams — see below), plus 支付候选 for NewAPI only (that
  queue is already server-side newapi-only, not merely hidden). global mode
  renders only 系统设置 and the source-health overview (both platforms,
  unfiltered). 返回用户端 never shows inside an admin-only embed. Standalone
  (no scope, including an embedded-admin URL whose platform/scope didn't
  parse) shows everything unchanged — point (f).
- `resolvePlatformSourceInstanceId` resolves a platform's
  `source_instance_id` from the admin source-health report (the one
  already-reachable admin endpoint exposing `source_type -> source_instance_id`)
  instead of ever hardcoding a UUID. `App.tsx`'s
  `useEmbeddedAdminPlatformSourceInstanceId` hook wraps this in a fetch +
  `useState`, used independently by every page that needs it (no shared
  cache exists elsewhere in this codebase either).

`PortalLayout` gets a new `embeddedAdmin` flag (`admin && embeddedAdminMode`,
kept separate from the user embed's `embedded`, which explicitly excludes
admin) and a parallel `portal-embedded-admin` CSS class (new rules in
`styles.css`, written as their own block rather than extending
`.portal-embedded`'s selectors, same reasoning as the scope module) hiding
the same chrome the user embed hides — brand, workspace chip, topbar,
sidebar-security — while keeping the nav as a horizontal bar.

### c. Server-side platform filtering

`PaymentCandidatePageQuery` and `RefundCasePageQuery`
(`backend/internal/postgresstore/records.go`) gained a `SourceInstanceID`
field, same convention as the existing `RequestPageQuery`/
`EligibilityFreezePageQuery`. `ListPaymentCandidatesPage`
(`funding.go`) adds an optional `fl.source_instance_id=$N::uuid` filter.
`ListRefundCasesPage` (`refunds.go`) reaches the source instance through the
case's (always-present, `NOT NULL`) funding lot via an optional `JOIN
funding_lots`; `refundCaseSelect`'s column list is now qualified with the
`refund_cases.` table prefix so that join can't collide with funding_lots'
own `id`/`updated_at`/`source_revision_hash` columns (this is a pure
qualification, not a behavior change, for `ResolveRefundCase`'s existing use
of the same constant). Both validate the UUID shape with the existing
`eligibilityUUIDPattern`. `listPaymentCandidates`/`listRefundCases`
(`backend/internal/httpapi/operations.go`) read `source_instance_id` off the
query string the same way `listEligibilityFreezes` already does (no HTTP-layer
format validation there either — kept consistent with that nearest sibling
rather than "fixed" to a different standard; see Risks).

Frontend: `DataProvider`, `EligibilityFreezesPage`, `PaymentCandidatesPage`
and `RefundCasesPage` each resolve the platform's `source_instance_id` and
pass it into their list requests, gating the request until it resolves
rather than briefly requesting an unscoped page and re-requesting a moment
later. Their platform/source-instance selector controls are hidden in
platform mode; `AdminPage`'s own client-side source filter is initialized to
match and hidden the same way, mirroring the existing user-embed
`OrdersPage` scoping pattern from `XM-INV-EMBED-SCOPE`. `SourceHealthPage`
filters its already-fetched report to the platform's 5 streams client-side —
CR-0005 doesn't ask for a server-side filter on that endpoint, and it
already returns both platforms' 10 rows in one cheap response regardless.
`AdminDrawer` shows a "不在当前平台" state instead of a request's detail
when its `source` doesn't match the embedded platform (normally unreachable,
since the list it's selected from is already filtered — kept as defense in
depth per CR-0005's own "view constraint, not a security boundary" framing).

### d. Auth: popup-based OIDC login and admin step-up

`AuthProvider.login`/`stepUp` open OIDC in a real popup window (`window.open`
on the click that triggers it) instead of the user embed's `_top`
navigation, when embedded-admin mode is on — `_top` would drag the whole
hosting console page over to `invoice.solov.cc`, not just leave the iframe.
The popup's own return page (detected via a `return_to` query marker)
notifies its opener with a small versioned `postMessage` and closes without
ever rendering the app; the opener re-reads its own session via the existing
`refresh()` rather than trusting the message content. `StepUpPage` stops
auto-triggering step-up in embedded-admin mode (a popup needs a direct user
gesture or it gets blocked) and its button reads "重新验证" as CR-0005
specifies (unchanged "继续管理员验证" for standalone). See **Risks** below —
this handshake is new, not an extension of a pre-existing mechanism, despite
CR-0005 assuming one existed.

### e. Security headers

`/admin` and `/admin/`'s CSP moves from `frame-ancestors 'none'` to
`frame-ancestors https://console.solov.cc` in both
`deploy/nginx/invoice.solov.cc.conf.template` (host layer) **and**
`web/nginx.conf` (the web container's own layer, which was not in CR-0005's
literal text but is necessary — see Risks/the nginx commit message for the
internal-redirect bug this surfaced and fixed). `postMessage` in the new
handshake accepts only this page's own origin (same-origin by construction —
see (d) and Risks). `docs/SECURITY-ARCHITECTURE.md` §8 is amended.
`scripts/verify-web-security-headers.ps1` now asserts the new `/admin` value
against a live container.

### f. Standalone unchanged

Every new branch in the frontend is conditional on
`embeddedAdminMode`/`embeddedAdminScope` being set, which only ever happens
via `ui_mode=embedded_admin` — absent that, `adminNav`/`PortalLayout`/every
list page/`StepUpPage`/`AuthProvider` behave exactly as before. The backend's
new filter parameters are optional and unfiltered when absent.

## Files changed

Backend:
- `backend/internal/postgresstore/records.go` — `SourceInstanceID` on
  `PaymentCandidatePageQuery`/`RefundCasePageQuery`.
- `backend/internal/postgresstore/funding.go` — optional filter in
  `ListPaymentCandidatesPage`'s query.
- `backend/internal/postgresstore/refunds.go` — `refundCaseSelect` column
  qualification + optional `funding_lots` join and filter in
  `ListRefundCasesPage`.
- `backend/internal/httpapi/operations.go` — read `source_instance_id` off
  the query string in both handlers.
- `backend/internal/postgresstore/embedded_admin_source_filter_integration_test.go`
  — **new**, store-layer tests (see Tests run).
- `backend/internal/httpapi/operations_source_filter_test.go` — **new**,
  handler-layer tests with a narrow `OperationsService` double.

Frontend:
- `web/src/lib/embedded-admin-scope.ts` — **new**. Scope parsing/nav
  visibility/source-instance resolution/popup-handshake helpers.
- `web/src/lib/embedded-admin-scope.test.ts` — **new**, 22 tests.
- `web/src/lib/http-api.source-instance-filter.test.ts` — **new**, 4 tests
  (see Tests run).
- `web/src/App.tsx` — `embeddedAdminMode`/`embeddedAdminScope` constants,
  `adminRoute()`, `useEmbeddedAdminPlatformSourceInstanceId` hook; `adminNav`
  scope-key tagging; `PortalLayout` filtering/chrome/class; `DataProvider`,
  `AdminPage`, `AdminDrawer`, `EligibilityFreezesPage`,
  `PaymentCandidatesPage`, `RefundCasesPage`, `SourceHealthPage`,
  `StepUpPage` all touched for their piece of (b)/(c)/(d) above.
- `web/src/AuthProvider.tsx` — popup open/notify/listen handshake for
  `login`/`stepUp` in embedded-admin mode.
- `web/src/lib/api-contract.ts` — optional `sourceInstanceId` on
  `getAdminRequestPage`/`getPaymentCandidates`/`getRefundCases`.
- `web/src/lib/http-api.ts` — implements the above, validated the same way
  as the existing `getEligibilityFreezes` filter.
- `web/src/lib/mock-api.ts` — mirrors the interface (refund cases: accepted
  for conformance, the demo fixture has no field to filter by — see the
  parameter's own comment there).
- `web/src/styles.css` — new `.portal-embedded-admin` block.

nginx / security:
- `deploy/nginx/invoice.solov.cc.conf.template` — three new
  `/embed/admin/*` locations; `/admin` + `/admin/` CSP change.
- `web/nginx.conf` — `/admin`/`/admin/` split into their own named-location
  block with the console-only CSP (see Risks for why this needed more than
  a second copy of the existing pattern).
- `scripts/verify-web-security-headers.ps1` — new `/admin` assertion.
- `docs/SECURITY-ARCHITECTURE.md` — §8 amended.

**Not touched:** `release/`, `RELEASE-READINESS.md`,
`docs/PRODUCTION-RUNBOOK.md`, `docs/IMAGE-SCAN-REVIEW.md`,
`docs/superpowers/`, any release-identity/RC advancement, `contracts/`, any
migration/schema, `backend/internal/postgresstore/consumption.go` or
`source_sync.go` (owned by concurrent branches per this task's brief),
`web/src/lib/embedded-scope.ts` or its test (the user embed's own
session/scoping code, owned by the concurrent `XM-INV-PLATFORM-SCOPE`
branch), any server/production connection of any kind.

## Tests run

Backend (from `backend/`; `GOFLAGS=-buildvcs=false`; proxy env vars unset
for `go test` per this repo's known Windows/httptest quirk;
`INVOICE_TEST_DATABASE_URL` pointed at a pre-existing disposable PostgreSQL
18 container, `postgresql://postgres:test@127.0.0.1:55432/invoice_test`):

```
go build ./...                                              # clean
go vet ./...                                                 # clean
env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy \
    -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy \
    INVOICE_TEST_DATABASE_URL=postgresql://postgres:test@127.0.0.1:55432/invoice_test \
    go test -p 1 -count=1 ./...                              # 27/27 packages: ok (run twice, both clean)
"$(go env GOROOT)/bin/gofmt" -d <every touched .go file>     # clean after
    stripping this Windows checkout's CRLF (confirmed pure line-ending
    noise via normalize-then-diff, not a real formatting difference; did
    NOT run gofmt -w repo-wide)
```

New backend tests specifically:
- `TestListPaymentCandidatesPageFiltersBySourceInstance` (store): two newapi
  source instances each get one candidate; each filter returns only its own
  instance's item; unscoped returns both; a real but wrong-type (sub2api)
  instance returns zero, not an error; a malformed value is rejected.
- `TestListRefundCasesPageFiltersBySourceInstanceThroughFundingLots` (store):
  same shape, through the funding-lot join; also asserts the unscoped case
  returns both.
- `TestListPaymentCandidatesHandlerForwardsSourceInstanceFilter` /
  `TestListRefundCasesHandlerForwardsSourceInstanceFilter` (httpapi): a
  narrow `OperationsService` double proves the handler reads the URL
  parameter into the store query and that omitting it leaves it empty.
- The first store-test run hit a transient `deadlock detected` (shared
  container, concurrent agents in this session each running their own
  `DROP SCHEMA public CASCADE` against it) and, separately, a real bug in my
  first draft (both fixed before landing; see the store test file and Risks
  for the second one, an `UpsertFundingLot` fixture-timing trap unrelated to
  the actual feature).

Frontend (from `web/`; `web/node_modules` is a Windows directory junction to
`K:/发票/wt-XM-INV-AUTOLOGIN/web/node_modules`, `package.json` confirmed
byte-identical after normalizing line endings, before linking):

```
npm run typecheck    # tsc --noEmit x2, clean
npm test              # vitest: 5 files, 61 tests, all pass
                       #   invoice-contract.test.ts                16 (pre-existing, untouched)
                       #   portal-navigation.test.ts                2 (pre-existing, untouched)
                       #   embedded-scope.test.ts                  17 (pre-existing, untouched)
                       #   embedded-admin-scope.test.ts            22 (new, this task)
                       #   http-api.source-instance-filter.test.ts  4 (new, this task)
npm run build         # tsc --noEmit x2 + vite build, clean
```

`embedded-admin-scope.test.ts` covers: `parseEmbeddedAdminMode`;
`parseEmbeddedAdminScope` (both platforms, global, global-over-platform
precedence, unrecognized/missing -> null); `appendEmbeddedAdminParams`
(no-op outside the mode, `?`/`&` correctness, both scope kinds);
`isAdminNavItemVisible` (all 7 nav keys x all 3 scope shapes, including the
Sub2API-has-no-payment-candidates and global-shows-only-settings-and-health
rules); `resolvePlatformSourceInstanceId` (match, and null on no match);
the popup handshake's message-shape validation (exact match required on
every field) and return-marker round-trip.

`http-api.source-instance-filter.test.ts` stubs global `fetch` (and just
enough of `window` for `requestJSON`'s abort-timer use of
`setTimeout`/`clearTimeout`, since this project's vitest config runs plain
Node with no jsdom/happy-dom dependency available to switch to — no other
test anywhere in this codebase exercises the fetch layer of any admin list
endpoint, including the pre-existing `source_instance_id` filter on
eligibility-freezes, so there was no fetch-mock precedent to extend) to
directly prove `getAdminRequestPage`/`getPaymentCandidates`/
`getRefundCases` put `source_instance_id` on the request URL when given one
and omit it otherwise, plus the malformed-value rejection short-circuiting
before `fetch` is ever called.

PowerShell (from the repo root):

```
.\scripts\verify-web-security-headers.ps1     # PASS (local dev-build mode:
                                                # spins up a real Docker
                                                # container with web/dist +
                                                # web/nginx.conf mounted; run
                                                # three times across the fix
                                                # below, all green on the
                                                # final two)
```

This run **caught a real bug**: my first `web/nginx.conf` draft duplicated
the existing `location = /admin { try_files $uri $uri/ /index.html; ... }`
pattern with a different CSP, which parses fine and shows correctly in
`nginx -T`, but at *request time* `try_files`'s fallback to the literal path
`/index.html` triggers an internal redirect that re-enters normal location
matching for the new URI — landing back in whichever location matches
`/index.html` (the catch-all `location /`), whose `add_header` set is what
actually governs the response. This was invisible in the pre-existing code
because root and (old, unscoped) `/admin` carried identical headers. Fixed
with a named location (`location = /admin { try_files ... @admin_spa; }` /
`location @admin_spa { add_header ...; try_files /index.html =404; }`),
which is invoked directly with no re-match. Full diagnosis and fix are in
the nginx commit message; I would not have caught this from reading the
config alone.

## Not run

- **`verify-web-security-headers.ps1`'s release-bound mode**
  (`-Image`/`-ExpectedImageID`) — needs a real release image, not built in
  this dev worktree; only the local dev-build mode ran.
- **Browser/Playwright click-through** of the embedded admin console, the
  popup login/step-up flow, or the "不在当前平台" state's actual rendered
  appearance. This repo has no component-rendering test harness (no
  jsdom/`@testing-library/react`/`.test.tsx` anywhere in `web/src`, same as
  the prior `XM-INV-EMBED-SCOPE` and `XM-INV-AUTOLOGIN` handoffs note) —
  only pure-function `.test.ts` tests exist, which is what this task added
  to. `AuthProvider.tsx`'s pre-existing `window.open`-based
  `navigateTopLevel` also has no direct test coverage today, for the same
  reason; my new `openAdminAuthPopup` and the effects wiring the handshake
  follow that same existing boundary.
- **A real or staged console embed.** No platform-side counterpart
  (`EmbeddedConsoleFrame`, XM-INVCON0) exists yet to actually iframe this
  against.
- **PostgreSQL integration tests were not skipped** — a disposable
  PostgreSQL 18 container was available and used for the full suite,
  including the two new store tests.

## Risks / things to sign off on

1. **The popup-auth `postMessage` handshake is a new mechanism, not an
   extension of an existing one**, despite CR-0005's text assuming one
   ("沿用用户嵌入的版本化消息规范" / "extend the existing allowed parent
   origin configuration"). I searched thoroughly — no `postMessage`/
   `onmessage`/`MessageEvent` usage exists anywhere in `web/src` before this
   task, and `EMBED_ALLOWED_PARENT_ORIGINS` exists only in
   `deploy/.env.example`/`docs/CONFIGURATION.md`, never read by any Go or
   TypeScript code. What I built is a new, narrow, **same-origin**
   (`invoice.solov.cc` popup <-> `invoice.solov.cc` opener) channel scoped
   to exactly this login/step-up handshake; it does not touch or need
   `EMBED_ALLOWED_PARENT_ORIGINS`, since that pair is same-origin regardless
   of what frames the iframe. I deliberately did **not** build the
   cross-origin iframe<->console `postMessage` channel (height sync,
   navigation) CR-0005 point (h) describes — that's explicitly the
   platform-side `XM-INVCON0` slice's `EmbeddedConsoleFrame` component, not
   this one. I'm confident the functional need for (d) is fully met, but
   this is a real deviation from the CR's literal wording and worth a second
   opinion.
2. **The nginx internal-redirect bug** (see Tests run and the nginx commit)
   was genuinely non-obvious — `nginx -T` shows the config as "correct" even
   when the bug is present, since the problem is about which location
   *serves* the response after `try_files` falls through, not about parsing.
   I only caught it by actually running the verification script against a
   live container rather than reasoning about the config alone. Worth
   independent review given how easy it would have been to ship silently
   broken (the admin embed would have been unframeable by *any* origin,
   `console.solov.cc` included, indistinguishable from `'none'` until
   someone tried it).
3. **HTTP 500, not 400, on an invalid `source_instance_id` for the two new
   endpoints** — this exactly mirrors `listEligibilityFreezes`'s pre-existing
   behavior (a generic store-layer `errors.New` falls through
   `handleDomainError`'s default case), which I matched for consistency with
   its nearest sibling rather than "fixing" to a different standard on just
   these two. Now three endpoints share this rough edge instead of one; see
   Follow-ups.
4. **`AdminDrawer`'s "不在当前平台" state is normally unreachable** — the
   list it's populated from is already server-filtered in platform mode, so
   there's no ordinary path that selects a mismatched-platform id. Kept as
   defense in depth per CR-0005's own "view constraint, not a security
   boundary" framing; I could not visually verify it.
5. **`SourceHealthPage`'s platform-mode readiness badge is a client-side
   computation** (`visibleItems.every(ready)` over the 5 matching rows), not
   a distinct server concept — CR-0005 says "源健康（仅该平台的 5 条流）"
   without specifying the readiness-badge semantics precisely; I judged this
   the least confusing reading (a "READY" badge that doesn't match what the
   5 visible rows actually show would be worse).
6. Ran directly, single agent, no sub-agents or forks dispatched. No
   server/production contact of any kind; no `release/`/`RELEASE-READINESS.md`/
   runbook/image-scan/superpowers docs touched; no RC advancement; did not
   touch `consumption.go`/`source_sync.go`, or `lib/embedded-scope.ts`/its
   test — all per this task's explicit constraints.

## Follow-ups (recommended, not blocking this task's delivery)

1. **Platform-side XM-INVCON0 slice**: `ui-admin`'s `EmbeddedConsoleFrame`
   component, the Sub2API/New API nav entries, `XM_INVOICE_CONSOLE_ORIGIN`
   config and `web-app-config.sh` wiring, and the `ADMIN-IA.md` §8.2 #2
   amendment recording this CR's reversal — all in the `xingmang-platform`
   repo, out of this slice's scope.
2. If the invoice app itself ever needs to actively cooperate with a
   console-side height-sync/navigation `postMessage` channel (rather than
   the console wrapper handling sizing unilaterally on its own side),
   that's its own reviewed design task. `EMBED_ALLOWED_PARENT_ORIGINS` looks
   like the natural place to finally wire up a real parent-origin allowlist
   for it, extended with `console.solov.cc` — it is unused today (risk 1).
3. Browser/Playwright pass over the embedded admin console at real
   iframe/popup widths once a real or staged embed exists — mirrors the
   `XM-INV-EMBED-SCOPE` handoff's identical follow-up for the user embed.
   Confirm the horizontal nav, popup centering/sizing, and the "不在当前平台"
   state's actual rendered appearance.
4. Consider a small dedicated fix so an invalid `source_instance_id` returns
   400 `INVALID_FILTER` consistently across all three filtered endpoints
   (`eligibility-freezes`, `payment-candidates`, `refund-cases`) rather than
   500 on two of them and — via `listRequestsPage`'s own, different,
   HTTP-layer `validUUIDText` check — 400 on `invoice-requests` alone. Out of
   this task's scope; flagged for its own reviewed task (risk 3).

## Operator steps

1. **Apply the updated `deploy/nginx/invoice.solov.cc.conf.template` to the
   production nginx host.** It is not part of the image release pipeline.
   Adds the three new `/embed/admin/*` locations and changes `/admin` +
   `/admin/`'s CSP from `frame-ancestors 'none'` to `frame-ancestors
   https://console.solov.cc`.
2. **Confirm `https://console.solov.cc` is the platform console's actual,
   final production origin** before rolling this out. If it differs, both
   this nginx template *and* `web/nginx.conf` (which **does** ship via the
   normal web image release, unlike the template) must use the correct
   value, and the two must always agree — see the nginx commit message for
   why disagreement silently blocks *all* framing (indistinguishable from
   `'none'`) rather than failing open to something safe.
3. After rollout, run `scripts/verify-web-security-headers.ps1` in its
   release-bound mode (`-Image`/`-ExpectedImageID`) against the actual
   release image as part of the normal release gate, and independently
   `curl -I https://invoice.solov.cc/admin` to confirm the live CSP reads
   `frame-ancestors https://console.solov.cc`.
4. No invoice-side environment variable changes are needed for this slice —
   `EMBED_ALLOWED_PARENT_ORIGINS` is unrelated to what was built here (risk 1
   / follow-up 2).
5. This is RC-line production code, but I did not run the signed release
   ceremony, advance RC, or touch `RELEASE-READINESS.md`/the production
   runbook. That remains the normal release step once this branch is
   reviewed and merged.
