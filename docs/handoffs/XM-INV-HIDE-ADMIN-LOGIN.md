# XM-INV-HIDE-ADMIN-LOGIN: hide the admin-login entry point from the user-facing invoice frontend

- **status:** implemented and self-tested locally (frontend `npm run
  typecheck`, `npx vitest run` 79/79, `npm run build`, all clean). Not
  deployed, no server/production contact of any kind, not clicked through in
  a real browser. Frontend-only change; the backend was not touched and its
  own gates were not run (nothing in `backend/` changed -- see Files
  changed).
- **branch:** `ai/claude/XM-INV-HIDE-ADMIN-LOGIN`, based on
  `ai/claude/XM-INV-AUTOLOGIN` at `f15290e`, worktree
  `K:/发票/wt-XM-INV-HIDEADMIN`.
- **commit:** `0f5ce10b7c64fe3aaaf6c32367f85e00f944637d`.

## Owner ruling this implements (2026-09-02)

The user-facing invoice frontend must no longer show any "管理员登录" entry
point (no link, button, hint text, or footer item leading to the
administrator OIDC login). Business users authenticate with their platform
(Sub2API/New API) password only (`XM-INV-LOGIN`/`XM-INV-AUTOLOGIN`). The
administrator area itself stays reachable by direct URL and through the
console embed (`/admin`, `/embed/admin/*`) until CR-0006 replaces its
authentication -- routes and auth logic were not to be removed.

## Summary

`web/src/App.tsx`'s `LoginPage` is the **one and only** component
`AuthenticatedApplication` renders for *any* unauthenticated visitor,
regardless of which path they landed on (`/`, `/orders`, `/admin`, ...) --
there is no separate admin login page, no route-based split before
authentication. Before this change it always rendered a "管理员登录" ghost
button that, once clicked, revealed a "使用统一身份账号登录" button wired to
`AuthProvider`'s OIDC `login()`. That made the administrator login
discoverable from every user-facing page, including the ones business users
actually land on.

Fix: gate that whole toggle+button block on a new pure predicate,
`isAdminAreaPath(pathname)` (`web/src/lib/portal-navigation.ts`), evaluated
against `useLocation().pathname` inside `LoginPage`. It returns true only for
`/admin` and its sub-pages -- which is also exactly what every
`/embed/admin/*` nginx location resolves to
(`deploy/nginx/invoice.solov.cc.conf.template`: `/embed/admin/sub2api` ->
`/admin?ui_mode=embedded_admin&platform=sub2api`, etc.) -- and false for
every user-facing path, including the *user* embed's landing path
(`/embed/sub2api`/`/embed/newapi` -> `/?ui_mode=embedded&source=...`, not
`/admin`). Nothing else about `LoginPage`, `AuthProvider`, routing, or the
admin pages' own behavior changed: an unauthenticated visit to `/admin`
(direct URL or console embed) still renders the identical toggle -> OIDC
button -> popup/`_top` flow it did before, byte-for-byte.

I deliberately left two other things unchanged, both out of this ruling's
scope:

- **`shouldShowAdminReturn`** (`portal-navigation.ts`, pre-existing, drives
  the "返回管理端" sidebar link on the *user* side of `PortalLayout`). It
  only ever shows to a session the server has already put in the `admin`
  role, or flagged `stepUpRequired` -- i.e. someone already authenticated (or
  already recognized as admin-eligible), not a discovery path for an
  anonymous business user. It does not "lead to the administrator OIDC
  login" the way the removed button did: for the ordinary `role === "admin"`
  case it lands directly on already-authenticated admin content, no login
  prompt shown. Removing it would also cut into `RequireAdmin`/`StepUpPage`
  auth logic the task said not to touch. Flagging this reasoning explicitly
  in case the ruling was meant to reach further than the login page's own
  button -- I read "管理员登录 entry" as the discoverable, pre-auth entry
  point, not this already-authenticated nav aid.
- **The "接收邮箱" field's hint text** on the invoice-profile form
  (`App.tsx:1739`, `"V1 仅支持统一身份账号已经验证的邮箱；备用邮箱验证将在
  后续版本提供。"`). It mentions "统一身份账号" (the OIDC identity concept)
  but is not a link/button and does not lead anywhere -- it is unrelated,
  pre-existing copy about which emails an invoice profile accepts, and reads
  as possibly stale now that business users log in via platform password
  rather than "统一身份账号" (that predates `XM-INV-LOGIN`). Left untouched
  as out of scope for "remove the admin-login entry point"; flagging since it
  is the only other place in `web/src` referencing that phrase outside
  `LoginPage` itself.

I searched the full `web/src` tree (`管理员`, `admin`/`Admin`, `/admin`,
`OIDC`, `Keycloak`, `统一身份`/`统一账号`, `已是`/`是否为管理员`-style
failure-redirect phrasing, `footer`/`Footer`) before concluding these were
the only two hits worth a decision; no footer component exists at all in
this codebase, and no "已是管理员？"-style post-login-failure prompt exists
anywhere -- there was nothing to remove for that part of the brief.

## Files changed

- `web/src/lib/portal-navigation.ts` -- new `isAdminAreaPath(pathname):
  boolean`, a pure `pathname.startsWith("/admin")` predicate (matches the
  same convention `App.tsx`'s own `embeddedUserMode`/`DataProvider` checks
  already use), with a doc comment explaining the nginx-redirect reasoning
  above.
- `web/src/lib/portal-navigation.test.ts` -- 3 new tests for
  `isAdminAreaPath`: false for every user-facing path (`/`, `/orders`,
  `/profiles`, `/records`); true for `/admin` and every admin sub-page;
  and one documenting the (pre-existing, shared, not a live bug) prefix-match
  limitation against a hypothetical `/administration` path.
- `web/src/App.tsx` -- `LoginPage`: added `const location = useLocation();`
  and `const showAdminLoginEntry = isAdminAreaPath(location.pathname);`;
  wrapped the existing `showAdminLogin ? <OIDC button> : <"管理员登录"
  button>` block in `{showAdminLoginEntry && (...)}`. No other line in the
  component changed -- the admin-path rendering is byte-for-byte what it was
  before.

**Not touched:** `AuthProvider.tsx` (OIDC `login()`/`stepUp()`, the popup
handshake for the console embed -- all unchanged, still reachable exactly as
before from `/admin`), any route (`AuthenticatedApplication`'s `<Routes>`,
`RequireAdmin`), `StepUpPage`, `shouldShowAdminReturn`, any backend
(`backend/` -- no Go files in this diff, so backend gates were not run; see
Not run), `contracts/`, `deploy/nginx/invoice.solov.cc.conf.template` (see
Nginx check below), any release-identity file
(`scripts/release-image-gate-lib.ps1`, `scripts/test-release-image-gate.ps1`,
`scripts/verify-release-image-artifacts.ps1`, `RELEASE-READINESS.md`,
`docs/PRODUCTION-RUNBOOK.md`, `docs/IMAGE-SCAN-REVIEW.md`), no tags created.

### Nginx check (task item 4)

Read `deploy/nginx/invoice.solov.cc.conf.template` in full for every
`admin`-adjacent location. This change removes no page and no route -- only
a button's visibility on an existing page -- so no location's only target
disappeared. Specifically verified both families still resolve to pages that
render exactly as before:

- `/embed/sub2api`, `/embed/newapi` -> `/?ui_mode=embedded&source=...`
  (`isAdminAreaPath` false, as intended -- these already never showed the
  admin toggle differently from any other `/` visit).
- `/embed/admin/sub2api`, `/embed/admin/newapi`, `/embed/admin/global` ->
  `/admin?ui_mode=embedded_admin&...` (`isAdminAreaPath` true -- toggle still
  renders, popup OIDC flow unchanged).

No nginx change needed or made.

## Tests run

From `K:/发票/wt-XM-INV-HIDEADMIN/web`:

```
npm run typecheck    # tsc --noEmit x2 (tsconfig.app.json, tsconfig.node.json), clean
npx vitest run       # 5 files, 79 tests, all pass (was 76 before this task's
                      # 3 new isAdminAreaPath tests in portal-navigation.test.ts)
npm run build        # tsc --noEmit x2 + vite build, clean
```

`gitleaks protect --staged --verbose --redact=0` (8.30.1) against this
commit's staged diff: 0 leaks.

I did not add a rendering/component test for `LoginPage` itself. This repo
has no component-rendering test harness (`vite.config.ts` has no `test`
block, so vitest runs in plain Node -- no jsdom/happy-dom environment; no
`@testing-library/react` in `package.json`'s `devDependencies`; confirmed no
`.test.tsx` file exists anywhere in `web/src`), a gap every prior handoff on
this branch (`XM-INV-LOGIN`, `XM-INV-AUTOLOGIN`, `XM-INV-ADMIN-EMBED`) has
already flagged and none has closed. I did not stand one up here (would mean
adding new dev dependencies I cannot install -- `web/node_modules` is a
directory junction to a pre-built install, and I was told not to run `npm
install`), and it would be a much larger, separate-task-sized lift than this
slice. Instead I followed this codebase's own established workaround for
that exact gap -- see e.g. `shouldShowAdminReturn`/`isAdminNavItemVisible`/
`shouldSyncEmbeddedAdminHeight`, all pure predicates factored out of
component bodies specifically so they're unit-testable without a render
harness -- and did the same for the new admin-login-visibility decision.
`isAdminAreaPath`'s tests plus `npx vitest run`'s clean pass together prove
the predicate is correct and that `LoginPage` compiles and type-checks with
it wired in; they do not prove the rendered DOM actually omits the button
pixel-for-pixel in a browser.

## Not run

- **Backend gates** (`go build`/`go vet`/`go test`/`gofmt`). No file under
  `backend/` is part of this diff -- confirmed via `git status`/`git diff`
  showing only the three `web/src/...` files above -- so there is nothing
  for those gates to cover. Not run, as expected for a pure frontend change.
- **Browser/manual click-through** of `/orders` (button gone) and `/admin`
  (button unchanged) in a real browser. Same gap the three prior handoffs on
  this branch already carry for this app's login/admin screens; not closed
  here either.
- **A real or staged console embed** exercising `/embed/admin/*` end to end.
  Not available in this environment; verified structurally instead (the
  nginx redirect targets and `isAdminAreaPath`'s own tests) per the Nginx
  check above.

## Risks / things to sign off on

1. **`shouldShowAdminReturn`/"返回管理端" left in place** (see Summary) --
   an explicit scope judgment call, not an oversight. If the ruling was
   actually meant to also hide that already-authenticated-admin nav aid from
   the user-side sidebar, that's a one-line change
   (`portal-navigation.ts`/its call site in `App.tsx`'s `PortalLayout`) on
   top of this branch; flagging rather than guessing and doing it
   unilaterally, since removing it also touches `RequireAdmin`-adjacent
   step-up navigation the task said not to touch.
2. **The "统一身份账号" hint text on the invoice-profile email field left in
   place** (see Summary) -- same reasoning, plus it is not itself a login
   entry point.
3. **No browser click-through** (see Not run) -- the change is small and
   mechanically simple (one boolean gate around an unchanged JSX block,
   `npm run build`'s clean `vite build` proves it compiles into the bundle
   correctly), but nobody has visually confirmed the button is actually gone
   from a rendered `/orders` page or that the admin toggle still visually
   works on a rendered `/admin` page.
4. No routes, auth logic, backend code, or nginx config changed; no
   release-identity files touched; no tags created; no server/production
   contact of any kind -- checked, per this task's explicit constraints.

## Follow-ups (recommended, not blocking this task's delivery)

1. Product/owner sign-off on risk 1 and 2 above (whether "返回管理端" and
   the profile-form hint text are actually in scope for this ruling).
2. Manual or Playwright-driven browser pass over `/orders` (confirm the
   button is gone) and `/admin` (confirm the toggle -> OIDC button flow still
   renders and works) -- mirrors the identical open follow-up every prior
   handoff on this branch already carries for this app's auth screens.
3. When CR-0006 replaces the admin area's authentication, revisit whether
   `isAdminAreaPath`/this gating is still the right mechanism, since the
   thing being gated (an OIDC button) may not exist in its current form
   afterward.
