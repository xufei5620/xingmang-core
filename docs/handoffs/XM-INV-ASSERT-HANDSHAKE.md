# XM-INV-ASSERT-HANDSHAKE: iframe-side request/re-issue half of the console-assertion handshake

- **status:** implemented and self-tested locally (`npm run typecheck`,
  `npm test -- --run` 155/155, `npm run build`, all clean; gitleaks scan of
  both commits on this branch -- see "Tests run"). Not deployed, no
  production or server contact of any kind, no release ceremony run, no RC
  advanced. Not clicked through in a real browser or against a real/staged
  console.
- **branch:** `ai/claude/XM-INV-ASSERT-HANDSHAKE`, based on
  `ai/claude/XM-INV-AUTOLOGIN` at `8ba98e3`, worktree
  `K:/发票/wt-XM-INV-HANDSHAKE`.
- **commits:**
  - `fec8979` feat(web): add admin-assertion-needed message contract and handshake predicates
  - `07553f9` feat(web): request console assertion with bounded retry, guard duplicate exchange
  - *(this commit)* docs(handoff): add XM-INV-ASSERT-HANDSHAKE handoff

## Summary

**Production finding this slice fixes (2026-09-03 13:04Z, first real
canary):** the platform console (`xingmang-platform`,
`web/packages/ui-admin/src/EmbeddedConsoleFrame.tsx`) signs a console
assertion, embeds the invoice console in an iframe, and posts
`{type:"xm-embed",version:1,kind:"admin-assertion",assertion}` to the
invoice origin exactly twice: once when the assertion is ready, once more
on the iframe's own `onLoad`. `XM-INV-CONSOLE-ASSERT` built the invoice
side's *receiving* half of that contract
(`web/src/lib/embedded-admin-scope.ts`'s
`parseXmEmbedAdminAssertionMessage`, `AuthProvider.tsx`'s unconditional
`message` listener) but registers it only after the React bundle has run --
both of the platform's fixed deliveries can land before that listener
exists, and the invoice frontend never told the console redemption failed.
Result: the embedded admin saw the "登录开票中心" login card despite the
platform having signed and delivered a valid assertion (platform log showed
`POST /api/v1/auth/console-assertion` never called).

This slice adds the missing request/re-issue leg: while framed and
confirmed unauthenticated, the invoice iframe now asks the console for an
assertion -- once immediately, then on a bounded retry schedule -- instead
of only ever passively waiting for one of the console's two fixed
deliveries to have won the race. The platform side (re-issuing a fresh
assertion on receipt of this message and re-delivering it) is **out of
scope for this slice** -- it belongs to the platform repo
(`xingmang-platform`) and is assumed already built or being built there
per this task's own brief; nothing here can verify it since no live
platform counterpart was reachable from this worktree.

## Protocol (after this slice)

```
EmbeddedConsoleFrame (platform)              Invoice iframe (this slice)
------------------------------              ---------------------------
1. TOTP passes, console signs
   an assertion
2. postMessage(admin-assertion) -----------> (lost if the React bundle /
   [on "ready"]                               message listener isn't
                                               mounted yet -- the original
                                               production gap)
3. iframe onLoad fires
   postMessage(admin-assertion) -----------> (may also be lost, or may
   [same assertion, 2nd delivery]             land: nonce is single-use,
                                               so only the FIRST delivery
                                               that actually reaches a
                                               live listener is ever
                                               exchanged -- see "Duplicate
                                               delivery" below)

                                          4. Listener now mounted; session
                                             check resolves to "no admin
                                             session yet" and this page is
                                             framed
                                          5. postMessage(admin-assertion-
                                             needed) -- once immediately
6. (platform, NOT built in this
   slice) receives admin-assertion-
   needed, re-issues a FRESH
   assertion (new nonce), re-
   delivers it
   postMessage(admin-assertion) -----------> 7. Exchanged: POST /api/v1/
                                             auth/console-assertion, then
                                             the listener's own refresh()
                                             observes authenticated=true

                                          [if step 6 never happens, or is
                                           still in flight:]
                                          8. Retry: 2s, 4s, 8s, 16s, then
                                             every 30s, up to
                                             ADMIN_ASSERTION_NEEDED_MAX_
                                             ATTEMPTS (40) total posts --
                                             stops for good once
                                             authenticated flips true, or
                                             the tab unmounts
```

**Duplicate delivery.** Because the console's own two deliveries (ready,
onLoad) carry the *identical* signed JWS -- not two distinct assertions --
and the backend's nonce store makes every assertion single-use, exchanging
that same string twice always fails the second time with a replay error.
`AuthProvider.tsx`'s listener now tracks the last-exchanged assertion string
in a ref and skips a second identical delivery outright, so a
successfully-redeemed `ready` delivery followed a moment later by the
matching `onLoad` delivery produces no spurious error. A *fresh* assertion
(a different string, which is exactly what step 6 above produces) is never
blocked by this guard.

**Nonce replay protection makes each assertion single-use by design** --
this is the mechanism that lets steps 2/3 and 6/7 above coexist safely: no
matter how many times the console (re-)delivers an assertion, at most one
listener-side exchange of any given signed value can ever succeed.

## Files changed

- `web/src/lib/embedded-admin-scope.ts` -- new
  `XmEmbedAdminAssertionNeededMessage` type and
  `buildXmEmbedAdminAssertionNeededMessage()` (the new envelope, `kind:
  "admin-assertion-needed"`, no payload beyond the envelope itself);
  `ADMIN_ASSERTION_NEEDED_RETRY_DELAYS_MS` (`[2000, 4000, 8000, 16000]`),
  `ADMIN_ASSERTION_NEEDED_STEADY_INTERVAL_MS` (`30000`),
  `ADMIN_ASSERTION_NEEDED_MAX_ATTEMPTS` (`40`, and
  `nextAdminAssertionNeededDelayMs`/
  `shouldScheduleNextAdminAssertionNeededAttempt` as the pure schedule
  functions); `shouldRequestAdminAssertion` (the framed-and-unauthenticated
  posting gate); `shouldExchangeAdminAssertion` (the duplicate-assertion
  guard's pure predicate).
- `web/src/lib/embedded-admin-scope.test.ts` -- new tests for all of the
  above: the message shape; the backoff schedule at each named boundary
  (including past the array into the steady interval); the attempt cap's
  exact edge; the posting gate's full truth table (framed x loading x
  authenticated); the duplicate-assertion guard (first exchange allowed, an
  identical repeat blocked, a fresh different assertion still allowed after
  a prior success).
- `web/src/AuthProvider.tsx` -- new `exchangedAssertionRef` guarding the
  existing admin-assertion listener against a duplicate exchange; new effect
  posting `admin-assertion-needed` to `window.parent` on the bounded retry
  schedule, gated by `shouldRequestAdminAssertion`, with unmount cleanup;
  `authenticated` hoisted to a local `const` (was previously only computed
  inline inside the context-value `useMemo`) since both the new gate and the
  memo now need it.
- **Not touched:** the platform-side re-issue/re-delivery logic (out of
  scope, belongs to `xingmang-platform`); `backend/` (this slice is
  frontend-only, matching the task brief); `deploy/`, `contracts/`; any
  Sub2API/NewAPI source.

## What is verified

- The new message contract's exact shape (`buildXmEmbedAdminAssertionNeededMessage`),
  and that it is never confused with the sibling height or admin-assertion
  envelopes.
- The full backoff schedule (`nextAdminAssertionNeededDelayMs`): each of the
  four widening delays by index, and the steady 30s interval both
  immediately after the backoff array is exhausted and far past it.
- The attempt cap's exact edge (`shouldScheduleNextAdminAssertionNeededAttempt`):
  allowed one attempt below the cap, refused at and above it.
- The posting gate's full truth table (`shouldRequestAdminAssertion`):
  framed+resolved+unauthenticated is the only `true` case; not framed,
  still loading, or already authenticated (individually and combined) are
  all `false`.
- The duplicate-exchange guard (`shouldExchangeAdminAssertion`): a first
  assertion is always exchanged; the identical string is refused on a
  second delivery; a genuinely different (fresh) assertion is still
  exchanged even after a prior one already succeeded.

## Not run / not verified

- **The AuthProvider effects themselves (the actual `setTimeout` scheduling,
  `window.addEventListener`/`postMessage` wiring, and unmount cleanup) are
  not integration-tested against a simulated DOM.** This repo has no
  jsdom/happy-dom dependency at all (confirmed via `package.json` and the
  mirrored `node_modules`) and no component-rendering test harness exists --
  the exact same pre-existing boundary `XM-INV-CONSOLE-ASSERT`'s own handoff
  already recorded for the sibling admin-assertion listener, and the one
  `XM-INV-ADMIN-EMBED` recorded before that for the popup-auth listener.
  Per this task's own instruction to work "at this repo's established
  layer," every piece of decision logic the effects depend on
  (`shouldRequestAdminAssertion`, `shouldExchangeAdminAssertion`,
  `nextAdminAssertionNeededDelayMs`,
  `shouldScheduleNextAdminAssertionNeededAttempt`) was pulled out into pure,
  fully unit-tested functions instead -- the same pattern this file already
  uses for `shouldSyncEmbeddedAdminHeight` (App.tsx's height-sync effect,
  also never DOM-tested directly). What is *not* independently proven is
  that the effect bodies wire those functions together correctly at
  runtime (timer scheduling, listener registration/cleanup order,
  `window.parent`/`window.top` behavior) -- that requires either adding a
  DOM-simulation dependency (out of this task's scope; also would have
  required an `npm install` this task was explicitly told to avoid) or a
  real browser pass.
- **No real console/platform counterpart exists to test against**, and the
  platform-side re-issue-on-`admin-assertion-needed` logic is not built in
  this slice (see Summary) -- nothing here has been exercised against a
  live signer or a live console.
- No browser/Playwright pass of any kind (no component-rendering test
  harness exists in this repo at all, same boundary every prior invoice-web
  handoff has recorded).
- No `go build`/`go vet`/`go test`, `scripts/test-release-image-gate.ps1`,
  or compose validation run -- this slice touches only `web/`, no backend
  or deploy files.
- No server/production contact of any kind; no `release/`,
  `RELEASE-READINESS.md`, `docs/PRODUCTION-RUNBOOK.md`,
  `docs/IMAGE-SCAN-REVIEW.md` touched; no RC advancement.

## Tests run

Frontend (from `web/`; `web/node_modules` mirrored via `robocopy /MIR /XJ`
from `K:/发票/wt-XM-INV-AUTOLOGIN/web/node_modules` -- no `npm install`
run):

```
npm run typecheck    # tsc --noEmit x2, clean
npm test -- --run     # 10 files, 155 tests, all pass
npm run build         # tsc --noEmit x2 + vite build, clean
```

gitleaks (native binary, `/c/Users/58439/.local/bin/gitleaks`):

```
gitleaks git --no-banner --log-opts="8ba98e3..HEAD" .
# no leaks found across both code commits
```

## Risks / things to sign off on

1. **This slice only builds the iframe's request side.** Whether the
   platform actually re-issues and re-delivers a fresh assertion on receipt
   of `admin-assertion-needed` is entirely a `xingmang-platform`-side
   concern, not verified or built here. Until that lands, this slice's
   retry loop will post faithfully but never receive a reply for a genuinely
   stuck session -- it degrades to "the iframe asks and eventually gives up
   after `ADMIN_ASSERTION_NEEDED_MAX_ATTEMPTS`," not a fix on its own.
2. **The retry effect's dependency is the gate's boolean result
   (`shouldRequest`), not `loading`/`authenticated` individually** -- chosen
   so an unrelated loading flicker (e.g. `refresh()` after a failed
   exchange, session still unauthenticated) does not reset an in-flight
   backoff back to the 2s start. This is a real behavioral choice, not
   just a lint nicety; please confirm the reasoning in
   `AuthProvider.tsx`'s comment above the effect is the intended semantics.
3. **No DOM-level test of the effects themselves** (see "Not run" above) --
   the pure predicate/schedule functions are fully covered, but the actual
   `setTimeout`/`addEventListener` wiring is not. This is a continuation of
   a boundary this repo has carried since `XM-INV-ADMIN-EMBED`, not a new
   gap introduced by this slice, but it means the very first real proof of
   the retry timing and cleanup behavior will be a browser pass or a
   production canary.
4. Ran directly, single agent, no sub-agents or forks dispatched for
   research or implementation. No server/production contact of any kind.

## Follow-ups (recommended, not blocking this slice's delivery)

1. Confirm (or build, if not already in progress) the platform-side
   `xingmang-platform`/`EmbeddedConsoleFrame` handler for
   `admin-assertion-needed`: on receipt, sign a fresh assertion and deliver
   it the same way as the existing ready/onLoad paths.
2. Once a real platform counterpart exists, run a real canary end-to-end
   (an operator's console session, TOTP, embedded admin) and confirm the
   full round trip: login card shows briefly, `admin-assertion-needed`
   fires, the platform re-issues, the iframe exchanges it, admin access
   appears without a manual refresh.
3. If jsdom/happy-dom is ever added to this repo for another slice's needs,
   revisit this slice's effects for direct DOM-level tests of the retry
   timer and listener cleanup -- the pure functions this slice added make
   that a thin wiring test at that point, not a rewrite.
