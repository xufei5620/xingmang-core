# XM-INV-ASSERT-STEPUP: renew administrator step-up through the console assertion

- **status:** implemented on the release branch, gated locally (web typecheck,
  170 tests across 12 files, production build), and folded into RC86.
- **branch:** `ai/claude/XM-INV-AUTOLOGIN` (the release line).
- **found by the product owner in production**, 2026-09-04, roughly forty
  minutes after CR-0006 phase 2 step 5 closed the transitional Keycloak admin
  login: the embedded admin console showed 需要管理员二次验证 with a
  重新验证 button that could not work.

## The gap step 5 exposed

An administrator session carries an MFA freshness window
(`AdminPolicy.StepUpMaxAge`, ten minutes). When it lapses,
`AuthorizeSession` returns `ErrMFAStepUpRequired` and the frontend shows the
step-up card. That card has always had exactly one action: open
`/api/v1/auth/admin/step-up`, the Keycloak step-up route.

Step 5 set `OIDC_ADMIN_LOGIN_ENABLED=false`, which unregisters that route
(it answers 404 by design). So an operator whose ten minutes lapsed had **no
way forward at all** -- the button led nowhere, and the only escape was to log
out and let the auto-login handshake mint a new session.

The handshake could have fixed this by itself, but it was gated on
`!authenticated`: a live-but-stale session never asked for anything.

## Fix

`shouldRequestAdminAssertion` now asks for an assertion in **two** resolved
states rather than one: no session, or a live session whose step-up has
lapsed. Exchanging an assertion issues a session with a fresh `mfa_at`, so
requesting one *is* the step-up for this login path. The parameter defaults to
`false`, so every existing caller behaves exactly as before.

`stepUp()` in `AuthProvider` posts `admin-assertion-needed` to the framing
console when the page is framed and the session status reports
`oidc_admin_login_enabled: false`, instead of opening a popup at a route that
does not exist. It keeps the popup path for a deployment where the Keycloak
login is still on.

The step-up card says which of the two is happening, and its button becomes
the *manual* path -- the automatic handshake normally renews within a second,
so the card is usually a flash rather than a stop.

No backend change: the exchange endpoint already mints a fresh `mfa_at`, and
the ten-minute policy is unchanged.

## Tests

`web/src/lib/embedded-admin-scope.test.ts` gains four cases: an authenticated
session with `stepUpRequired` posts; the same combination still withholds
while the session check is in flight and when not framed; and the defaulted
parameter leaves both original outcomes unchanged.

## Gates

`npm run typecheck` exit 0; `npm test -- --run` 12 files / 170 tests passed;
`npm run build` exit 0.

## Operational note

Until RC86 is deployed, an operator who hits the step-up card can still get
back in by clicking 退出登录: the logout triggers the auto-login handshake,
which mints a fresh session. That is the workaround, not the fix.
