# XM-INV-EMBED-SCOPE: platform-scoped embedded invoice view + account identity

- **status:** implemented and self-tested locally (backend `go build`/`go
  vet`/full `go test -p 1 -count=1 ./...`, 25/25 packages; frontend `npm run
  typecheck`, `npm test` 35/35 tests, `npm run build`). Not deployed, not
  clicked through in a browser, and no real platform ever embedded this view
  to exercise the new `platform=` URL param end-to-end.
- **branch:** `ai/claude/XM-INV-EMBED-SCOPE`, based on
  `ai/claude/XM-INV-AUTOLOGIN` at `7975339` (RC59), worktree
  `K:/发票/wt-XM-INV-EMBED`.
- **commit:** `dc93ffa` is the code/test change described below. This
  handoff file itself lands in a follow-up commit on top of it.

## Summary

User request (verbatim): "我们从哪个平台登录对应的账号就只能读取对应账号的
充值金额，以及消费金额进行开票" and "页面也没有标识账号，应该显示对应的用户
名". A platform embeds the invoice center per-account via
`?ui_mode=embedded` (`App.tsx:106-120`), but one `invoice_user` can
legitimately hold bound accounts on **both** Sub2API and New API (the
multi-platform identity claim path from `007f17f`), and the embedded
chrome hides the sidebar/topbar that would otherwise show whose account is
signed in (`.portal-embedded .topbar/.workspace-chip { display: none }` in
`styles.css`) — so the embedded iframe had no way to narrow itself to one
platform's data, and no visible account identity at all.

### 1. Platform-scoped URL param

New param `platform=sub2api|newapi`, effective only alongside
`ui_mode=embedded` (any other value, or the param on its own without
`ui_mode=embedded`, is treated as unscoped — unchanged behavior).
`lib/embedded-scope.ts`'s `parseEmbeddedPlatform` parses it once from the
initial URL into a new module-level `embeddedPlatform` constant in
`App.tsx`, the same pattern `embeddedUserMode` already uses.
`appendEmbeddedParams` (replacing the old inline body of `userRoute`)
appends `&platform=...` after `ui_mode=embedded` so it survives in-app
navigation between the embedded routes, same as `ui_mode` already did.

### 2. Scoped views

`scopeBySource(items, embeddedPlatform)` (generic over anything with a
`source: SourceType` field — funding orders, eligibility summaries, and
source accounts all qualify) is a no-op when unscoped and otherwise filters
to the one platform. Applied in `OrdersPage`:

- **选择可开票资金**: the 全部平台/SoloV API/模型平台 tab row is hidden
  when scoped; `filteredOrders` is scoped first (defense in depth — the
  tab-selection `source` state is also *initialized* to `embeddedPlatform`,
  but the hidden tabs can never change it away, and scoping the list
  independently means it stays correct even if that ever changed).
- **按平台计算的开票资格**: `EligibilitySummaryPanel`'s `items` prop is
  scoped before being passed in.
- **已关联的平台账号** (`SourceAccountStatus`): the account list is
  scoped; the "not yet bound" guidance shows only the scoped platform's
  "打开 Sub2API"/"打开 New API" link (both show when unscoped, unchanged);
  the empty-state check (`已关联的平台账号` vs `关联平台账号` heading, and
  whether the binding-guidance steps render at all) is now based on the
  *scoped* count, so a user bound only on the other platform still sees the
  binding guidance for the platform this embed actually cares about.

Submission itself is untouched — it already operates per-batch/per-source
via `sourceInstanceId`, not globally.

Unscoped behavior (no `platform` param, or non-embedded mode entirely) is
byte-for-byte unchanged: `scopeBySource(items, null)` returns `items` as-is
and every call site above only branches on `embeddedPlatform` being
truthy.

### 3. Account identity badge

The embedded view's top tab row (`.sidebar` repurposed horizontally in
`.portal-embedded`, next to the nav tabs and the existing logout button)
now shows a compact badge with the signed-in account's label, via
`lib/embedded-scope.ts`'s `accountIdentityLabel`:

- **Platform-password session** (`user.platform` set): email if present,
  else a real display name if present, else `"<platform 中文名> · 用户
  <platform_user_id>"` (e.g. **"SoloV 模型平台 · 用户 48"**) — this last
  case is the one the user's complaint actually hits today: a
  username-only New API account has no email, and
  `production_auth.go`'s `maskedEmailName` falls back to the literal
  string `"用户"` for *every* such account, making them indistinguishable.
  The frontend compares against that exact literal (documented in a
  comment next to the constant) rather than assuming "session has a
  platform ⇒ always use platform+ID", so this stays forward-compatible if
  a real captured platform username is ever persisted into `display_name`
  later (out of scope here — see Risks).
- **OIDC session** (`user.platform` is null) **+ `platform` URL param
  set**: shows that platform's bound source account's label +
  masked external ID, if bound; falls back to display name/email if not
  bound to that platform.
- Otherwise: existing display name/email fallback, same text
  ("已登录用户") already used elsewhere in the app.

### Backend: `platform_user_id` on the session response

`GET /api/v1/auth/session` already computed `current.Session.PlatformUserID`
(used internally for CSRF-rotation) but never put it in the JSON body —
only `platform` was exposed. This is the one piece of data the frontend
badge above needs that wasn't already reachable from an existing endpoint.
Purely additive: one new `"platform_user_id"` key in the existing `user`
map in `production_auth.go`'s `sessionStatus`, sourced from the
already-computed `current.Session.PlatformUserID` (empty string for every
OIDC session, unchanged semantics of every existing field).

I looked at whether the *real* platform username (captured in
`auth.PlatformLoginResult.Username` by both `sub2api_login.go` and
`newapi_login.go`) could be persisted into `display_name` instead, which
would fix the underlying "generic name" problem at its root rather than
working around it with `platform_user_id`. It currently is **not**
persisted anywhere — `auth.Principal` (the only thing that flows into
`ProvisionUser`) has no field to carry it, so it's silently discarded
today. I did not make that change: it touches user-provisioning semantics
(`auth.Principal`, `ProvisionUser`, first-login behavior) well beyond an
additive field, is out of this task's explicit "严禁改语义" constraint, and
overlaps code the parallel XM-INV-AUTOLOGIN work on this same branch has
been actively changing. Flagged as a follow-up.

## Files changed

- `web/src/lib/embedded-scope.ts` — **new**. `parseEmbeddedPlatform`,
  `appendEmbeddedParams`, `scopeBySource`, `accountIdentityLabel`.
- `web/src/lib/embedded-scope.test.ts` — **new**, 17 tests (see Tests run).
- `web/src/lib/source-labels.ts` — **new**. Extracted `sourceName` (the
  `SourceType` → Chinese label map) out of `App.tsx` so
  `embedded-scope.ts` can reuse it without duplicating the strings;
  `App.tsx` now imports it instead of declaring it locally. No text
  changed.
- `web/src/App.tsx` — `embeddedPlatform` constant + `userRoute` now
  delegates to `appendEmbeddedParams`; `PortalLayout` renders the new
  badge and reads `sourceAccounts` from `useData()` for it; `OrdersPage`
  scopes `source` state's initial value, `filteredOrders`, the
  `EligibilitySummaryPanel` items, and hides the tab row when scoped;
  `SourceAccountStatus` scopes its list, heading, and binding-guidance
  links.
- `web/src/styles.css` — `.portal-embedded .embedded-account-badge` (+ a
  narrow-screen `max-width` tweak in the existing `@media (max-width:
  480px)` block). No existing rule touched.
- `web/src/types.ts` — `AuthUser` gained `platformUserId: string | null`.
- `web/src/lib/http-api.ts` — `BackendSession.user` gained optional
  `platform_user_id`; `mapSession` maps it to `platformUserId` (empty/
  missing → `null`, same pattern as the existing `platform` field).
- `web/src/lib/mock-api.ts` — `mockSession.user` gained
  `platformUserId: null` (TypeScript forced this — the demo session is an
  OIDC-shaped admin, so `null` is correct, not a behavior change).
- `backend/internal/httpapi/production_auth.go` — `sessionStatus` response
  gained `"platform_user_id"` (see Backend section above).
- `backend/internal/httpapi/production_auth_test.go` — extended
  `TestProductionOIDCLoginSessionAndCSRF`'s response struct and assertions
  to prove `platform`/`platform_user_id` are both empty for an OIDC
  session.
- `backend/internal/httpapi/platform_login_test.go` — extended
  `TestPlatformLoginSuccessIssuesSessionWithPlatformIdentity` and
  `TestPlatformLoginSessionsAreIsolatedAcrossPlatformsAndAccounts` with
  `platform_user_id` assertions.
- `docs/handoffs/XM-INV-EMBED-SCOPE.md` — this file.

**Not touched:** `release/`, `scripts/`, `RELEASE-READINESS.md`,
`docs/PRODUCTION-RUNBOOK.md`, `docs/IMAGE-SCAN-REVIEW.md`,
`docs/superpowers/`, any release-identity/RC advancement, `contracts/`,
any migration/schema, the admin workspace/OIDC login path (admin never
receives `portal-embedded` — untouched structurally and behaviorally),
`AuthProvider.tsx` (session shape flows through unchanged; no new state
needed there), no server/production connection of any kind.

## Tests run

Backend (from `backend/`, `GOFLAGS=-buildvcs=false`; proxy env vars unset
for `go test` per this repo's known Windows/httptest quirk):

```
go build ./...                                              # clean
go vet ./...                                                 # clean
env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy \
    -u ALL_PROXY -u all_proxy -u NO_PROXY -u no_proxy \
    go test -p 1 -count=1 ./...                              # 25/25 packages: ok
"$(go env GOROOT)/bin/gofmt" -d <every touched .go file>     # clean after
    stripping this Windows checkout's CRLF (confirmed pure line-ending
    noise, not a real formatting diff, same technique as the prior
    XM-INV-AUTOLOGIN handoff — do NOT run gofmt -w on this checkout)
```

Frontend (from `web/`; `web/node_modules` is a Windows directory junction
to `K:/发票/invoice-system/web/node_modules` — this worktree's
`package.json`/`package-lock.json` confirmed byte-identical to the main
checkout's after normalizing line endings, before linking):

```
npm run typecheck    # tsc --noEmit x2, clean
npm test              # vitest: 3 files, 35 tests, all pass
                       #   invoice-contract.test.ts   16 (pre-existing, untouched)
                       #   portal-navigation.test.ts   2 (pre-existing, untouched)
                       #   embedded-scope.test.ts     17 (new, this task)
npm run build         # tsc --noEmit x2 + vite build, clean
```

`embedded-scope.test.ts` covers: `parseEmbeddedPlatform` (unscoped outside
embedded mode; both valid platforms; missing/invalid value);
`appendEmbeddedParams` (no-op outside embedded mode; `?`/`&` separator
correctness; `platform` appended after `ui_mode`); `scopeBySource`
(pass-through when unscoped; filters correctly when scoped; empty result
when nothing matches); `accountIdentityLabel` (all branches described
under "Account identity badge" above, including the platform+ID fallback
matching the exact example format and the OIDC-scoped-but-unbound
fallback).

## Not run

- **Browser/e2e verification.** Not clicked through in a real or headless
  browser — no `platform=` embed was ever actually loaded in an iframe or
  otherwise. `npm run typecheck`/`npm test`/`npm run build` cover types,
  the four pure functions in `embedded-scope.ts`, and that the bundle
  compiles, not rendered layout, the hidden-tabs visual state, or the new
  badge's actual appearance/truncation at real embed widths. This repo has
  no component-rendering test harness yet (no jsdom/`@testing-library/
  react`/`.test.tsx` files anywhere in `web/src`, per the prior
  XM-INV-AUTOLOGIN handoff's same note) — only pure-function `.test.ts`
  tests exist, which is what this task added to as instructed.
- **Real platform embed of `?ui_mode=embedded&platform=...`.** No platform
  actually appends the new `platform` param yet — see Follow-ups.
- **PostgreSQL integration tests** — this task adds no schema/migration;
  the full non-DB suite (25 packages) is green.

## Risks / things to sign off on

1. **The account-identity badge's platform+ID fallback trusts the exact
   literal `"用户"`** that `production_auth.go`'s `maskedEmailName` returns
   today as the signal "nothing better to show" (documented in a comment
   next to `GENERIC_DISPLAY_NAME_FALLBACK` in `embedded-scope.ts`). If that
   backend fallback string is ever changed without updating this constant,
   the frontend would silently stop taking the platform+ID branch for
   accounts with no email (falling back to showing the new generic string
   verbatim instead) — not a crash, just a quiet regression of exactly the
   bug this task fixes. A more structural fix (a dedicated boolean flag
   from the backend, or persisting a real username) would remove this
   coupling; I judged the string-literal coupling an acceptable, minimal
   trade-off for an additive-only backend change, but it's worth a second
   opinion.
2. **The real fix for "should show the actual username"** — persisting
   `auth.PlatformLoginResult.Username` (already captured from both
   platforms' login responses, currently discarded before it ever reaches
   `invoice_users.display_name`) — was deliberately left undone; this task
   ships the platform+ID fallback instead. See Follow-ups.
3. **Session platform vs. URL `platform` param can disagree** (e.g. a
   platform-password session on `sub2api` sitting inside an iframe whose
   embedder passed `platform=newapi` by misconfiguration). The identity
   badge always trusts the session (server-verified) over the URL param in
   that case; the view-scoping (§2) always follows the URL param
   regardless, per the task spec's wording. In that mismatch scenario the
   scoped lists would legitimately show empty/wrong-platform data for that
   session. This is presented as the embedding platform's own
   configuration responsibility (see Follow-ups), not reconciled further
   here — the task spec ties scoping strictly to the URL param and I did
   not invent extra cross-checking logic beyond what was asked.
4. **No server/production contact of any kind**, no `release/`/`scripts/`/
   `RELEASE-READINESS.md`/runbook/image-scan/superpowers docs touched, no
   RC advancement, no sub-agents or forks dispatched — checked against the
   task's explicit constraints; all satisfied.
5. Ran directly, single agent, no sub-agents/forks — per this task's own
   constraint. I read every existing line I built on top of (`App.tsx`
   end-to-end for the affected components, `types.ts`, `http-api.ts`,
   `mock-api.ts`, `AuthProvider.tsx`, `production_auth.go`,
   `platform_login.go`/`newapi_login.go`/`sub2api_login.go`,
   `runtime.go`'s provisioning closure, and `styles.css`'s
   `.portal-embedded` block) before changing or building on it.

## Follow-ups (recommended, not blocking this task's delivery)

1. **Platform-side configuration**: the two platforms' embed links need
   their own configuration updated to append `&platform=newapi` /
   `&platform=sub2api` to the existing `?ui_mode=embedded` embed URL —
   that configuration lives on the platform side, not in this repo, and is
   outside this task's scope.
2. Consider persisting the real platform username (risk 2 above) into
   `invoice_users.display_name` on first provisioning, which would let
   `accountIdentityLabel`'s existing "real display name" branch show it
   automatically (no frontend change needed) and fix the underlying
   "generic name" gap at its source instead of the platform+ID workaround.
   Touches `auth.Principal`/`ProvisionUser`/first-login semantics, so
   scope it as its own reviewed task.
3. Browser/Playwright pass over the embedded view at real iframe widths,
   once a real or staged embed exists — confirm the badge's truncation
   (`max-width: 220px`, `120px` under 480px) and the hidden-tabs layout
   read correctly, and click through both a platform-password embed and
   an OIDC-with-`platform=`-param embed end to end.
4. If risk 1's session-vs-URL-param mismatch is judged worth hardening,
   revisit whether the scoped views should also fall back to the session's
   own platform when it disagrees with the URL param, rather than trusting
   the URL param unconditionally.
