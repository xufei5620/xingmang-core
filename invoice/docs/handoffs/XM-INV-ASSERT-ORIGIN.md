# XM-INV-ASSERT-ORIGIN: accept the invoice app's own origin on console-assertion exchange

- **status:** implemented and self-tested locally (backend `go build`/`go
  vet`/full `go test -p 1 -count=1 ./...` against real PostgreSQL, 28/28
  packages with tests (33 total including no-test-file packages); `gofmt`
  clean on both touched files, verified against the staged git blob per
  this repo's own CRLF-checkout caveat -- see "Tests run"). Not deployed,
  no production or server contact of any kind, no release ceremony run, no
  RC advanced.
- **branch:** `ai/claude/XM-INV-ASSERT-ORIGIN`, based on
  `ai/claude/XM-INV-AUTOLOGIN` at `ced1bfb` (production RC81 line),
  worktree `K:/发票/wt-XM-INV-ASSERT-ORIGIN`.
- **commits:**
  - `45c49a7` fix(auth): accept the invoice app's own origin on console-assertion exchange
  - `9fcf132` test(auth): cover invoice-origin and console-issuer console-assertion redeem
  - *(this commit)* docs(handoff): add XM-INV-ASSERT-ORIGIN handoff

## Production finding (2026-09-03 14:00Z, second canary after RC81)

With `XM-INV-CONSOLE-ASSERT` (the exchange endpoint) and
`XM-INV-ASSERT-HANDSHAKE` (the iframe's request/re-issue leg) both live,
the handshake worked end to end for the first time: the console issued an
assertion on request and the invoice frontend redeemed it. But every
redemption was rejected with audit reason `origin_rejected` (HTTP 403
`ORIGIN_REJECTED`, UI: "当前账号没有执行此操作的权限").

**Cause:** `consoleAssertionExchange`
(`backend/internal/httpapi/production_auth.go`) required the request's
single `Origin` header to equal exactly
`a.ConsoleAssertionConfig.Issuer` (`https://console.solov.cc`). That check
was written under `XM-INV-CONSOLE-ASSERT`'s own assumption -- recorded in
its handoff's "Request" section -- that the console itself would POST
cross-origin. The protocol that actually got built and integrated
(`XM-INV-ASSERT-HANDSHAKE`'s design, `docs/handoffs/
XM-INV-ASSERT-HANDSHAKE.md`) is different: the console `postMessage`s the
signed assertion *into* the embedded invoice iframe, and it is the
**invoice web app itself**, running at its own public origin
(`https://invoice.solov.cc`), that redeems it with a same-origin `fetch`.
The browser therefore sends `Origin: https://invoice.solov.cc`, which
never matched the console-issuer-only check.

## Fix

`consoleAssertionOriginAllowed(origin, invoiceOrigin, consoleIssuer string)
bool` (new, `production_auth.go`) accepts the request's Origin when it
equals **either**:

1. The invoice app's own public origin -- the actual caller in the
   integrated protocol. Wired as `server.publicOrigin`, which is the
   exact same value runtime.go's `buildProductionRuntime` already derives
   once from `PUBLIC_ORIGIN` (`exactHTTPSOrigin`, no trailing slash) and
   already threads into `Config.PublicOrigin` -- the same value that
   builds the OIDC redirect URI (`publicOrigin + "/api/v1/auth/callback"`)
   and seeds the CSRF policy's allowed-origin list
   (`auth.NewCSRFPolicy([]string{publicOrigin})`). **No new config knob was
   needed or added**: `consoleAssertionExchange` already receives `server
   *Server` as its first argument and both live in package `httpapi`, so
   the existing unexported `server.publicOrigin` field was directly
   reachable -- reusing the one existing value the task brief asked to
   look for first, rather than introducing a
   `CONSOLE_ASSERTION_ALLOWED_ORIGINS`-style variable that could drift
   from it.
2. The configured console issuer (`a.ConsoleAssertionConfig.Issuer`) --
   kept for a hypothetical direct cross-origin POST from the console
   itself. Never actually observed in production, and not contradicted by
   the frozen design spec either, so removing it was not this fix's call
   to make; it costs nothing to keep since it is still exactly one more
   equality check.

Everything else about the check is unchanged: **exactly one** `Origin`
header is still required (a request with two Origin values -- even two
individually-legitimate ones -- is still rejected, since a real browser
never sends more than one), a rejection still calls
`ConsoleAssertionRateLimiter.RecordFailure` before responding, and the
audit reason recorded server-side is still the literal string
`"origin_rejected"` (`auditConsoleAssertionRejection`) for every case that
reaches this check and fails it -- genuinely foreign origins included.
Only the *set* of origins the check accepts changed; the shape of the
check, its failure accounting, and its audit vocabulary did not.

### Security rationale (why this is still a real check, not a formality)

The console assertion itself is Ed25519-signed by a key in a static,
reviewed keyring (`ConsoleAssertionKeyring`), structurally capped at a
5-minute lifetime independent of clock-skew tolerance, and single-use (the
nonce store's atomic `ConsumeNonce`, see `XM-INV-CONSOLE-ASSERT`'s handoff
part b) -- forging or replaying a valid assertion is not the threat this
Origin check defends against. The threat is a cross-site form/fetch POST
from an attacker-controlled page that has somehow obtained (or is
relaying) a legitimately-issued assertion string and tries to redeem it
against the exchange endpoint from a third origin, either to launch the
resulting session-fixation-shaped attack against a victim's browser or, in
combination with a stolen assertion, simply as unauthorized replay
surface. Restricting the accepted callers to precisely the two addresses
that can legitimately hold a fresh assertion and redeem it -- the invoice
app's own origin (which received the assertion via `postMessage` from its
own embedding iframe) and the console's own origin (the signer) -- keeps
this endpoint's defense-in-depth intact while finally matching what the
integrated protocol actually does. This is the same posture the
endpoint's own doc comment already described ("Origin is the entire
cross-site defense here, same as the design spec specifies" --
`XM-INV-CONSOLE-ASSERT` handoff, "Endpoint contract"); only the accepted
value(s) needed correcting, not the mechanism.

## What is verified

New/changed coverage in `backend/internal/httpapi/production_auth_test.go`:

- `TestConsoleAssertionExchangeAcceptsInvoiceOriginSameOriginRedeem` --
  Origin set to the invoice app's own public origin (matching the
  `PublicOrigin` now wired into the `consoleAssertionServer` test fixture)
  succeeds end to end (`200 {"ok":true}`, session cookies set). This is
  the exact production-failure shape, now passing.
- `TestConsoleAssertionExchangeAcceptsConfiguredConsoleIssuerOrigin` --
  Origin set to the console issuer still succeeds (every pre-existing
  exchange test already relied on this implicitly via
  `consoleAssertionExchangeRequest`'s default; this makes it an explicit,
  named assertion).
- `TestConsoleAssertionExchangeRejectsWrongOrDuplicateOrigin` (extended) --
  new `duplicate_both_legitimate` case: the invoice origin added as a
  *second* header alongside the default console-issuer origin is still
  403 `ORIGIN_REJECTED` (exactly-one is enforced regardless of whether
  every individual value would otherwise be accepted). The pre-existing
  `missing`/`wrong`/`duplicate`/`http_not_https` cases are unchanged and
  still pass.
- `TestConsoleAssertionExchangeForeignOriginRejectionCountsAgainstRateLimit`
  -- three foreign-origin rejections exhaust the exchange rate limiter
  exactly like any other rejection reason; a fourth attempt with a
  *legitimate* origin still gets `429 RATE_LIMITED`. Proves
  `RecordFailure` accounting on this branch survived the change.
- `TestConsoleAssertionOriginAllowed` -- a pure-function table test of
  `consoleAssertionOriginAllowed` itself: matches either configured value,
  matches neither, an empty request Origin never matches, and an unset
  config value (empty `invoiceOrigin` or empty `consoleIssuer`) never
  widens the check to match anything.

All pre-existing console-assertion, OIDC, CSRF, and coexistence tests in
the same file continue to pass unmodified.

## Files changed

- `backend/internal/httpapi/production_auth.go` -- new
  `consoleAssertionOriginAllowed` helper; `consoleAssertionExchange`'s
  Origin check now calls it with `(origins[0], server.publicOrigin,
  a.ConsoleAssertionConfig.Issuer)` instead of a single `!=` comparison;
  doc comment on `consoleAssertionExchange` extended with the finding and
  the origin rationale; the `ORIGIN_REJECTED` error message text updated
  from "is not the configured console origin" to "is not an allowed
  origin for console-assertion exchange" (there are now two legitimate
  values, not one -- the error *code* and HTTP status are unchanged).
- `backend/internal/httpapi/production_auth_test.go` -- `PublicOrigin:
  "https://invoice.example"` added to the `consoleAssertionServer` test
  fixture's `Config{...}` (mirrors production's `publicOrigin` wiring,
  matching the pre-existing CSRF policy origin already used in the same
  fixture); five new/extended test functions (see "What is verified").

**Not touched:** `backend/cmd/api/runtime.go` (no change needed --
`PublicOrigin: publicOrigin` was already threaded into `httpapi.Config` by
the pre-existing production runtime construction, see "Fix" above),
`backend/internal/auth/console_assertion.go` /
`ConsoleAssertionConfig` (its `Issuer` field keeps its existing two jobs --
the assertion's own `iss` claim check inside `VerifyConsoleAssertion`, and
the session-issuer tag `ConsolePrincipalFromClaims` writes, which
`logout()`'s `consoleAssertionSession` check and the coexistence tests
both depend on -- neither needed to change), `deploy/`, `contracts/`,
`agents/`, `consumption.go`, `web/`, any Sub2API/NewAPI source.

## Config knob for the operator

**None.** `PUBLIC_ORIGIN` is already a required production environment
variable (`buildProductionRuntime` fails startup without it, since it also
builds the OIDC redirect/post-logout URLs and the CSRF allowed-origin
list) and is presumably already set correctly to `https://invoice.solov.cc`
in production today, since OIDC login and same-origin CSRF-protected
mutations already work there. No redeploy-time configuration change is
required for this fix beyond deploying the new binary -- the same
`PUBLIC_ORIGIN` value that already makes OIDC and CSRF work on this
deployment is what now also makes the console-assertion Origin check
correct, with no separate value to keep in sync. If a future deployment
ever legitimately needs the invoice app reachable at more than one public
origin, that would require reintroducing a real
multi-value-config change at that time -- not needed today, since
`PUBLIC_ORIGIN` is already declared as one exact origin
(`exactHTTPSOrigin`'s doc comment: "must be one exact HTTPS origin").

## Tests run

Backend (from `backend/`; proxy env vars unset for `go test`/`go build`/
`go vet` per this repo's known Windows/httptest quirk;
`INVOICE_TEST_DATABASE_URL` pointed at a fresh, dedicated database created
for this task, `postgres://postgres:test@127.0.0.1:55432/
invoice_test_assertorigin?sslmode=disable`, via `docker exec
invoice-test-pg psql -U postgres -c "CREATE DATABASE
invoice_test_assertorigin;"`):

```
go build ./...             # clean
go vet ./...                # clean
go test -p 1 -count=1 ./... # 28/28 packages with tests: ok (no isolation
                             # rerun needed -- nothing failed on the first
                             # pass, including no loopback timeouts)
```

`gofmt`, verified against the **staged git blob content**, not the working
tree (this repo's Windows checkout renders tracked `.go` files as CRLF,
which makes a working-tree `gofmt -l`/`-d` report false full-file diffs on
every touched file regardless of real formatting -- see
`windows-toolchain-quirks` project note; `git add` then `git cat-file -p
:backend/internal/httpapi/<file>` recovers the actual bytes that would be
committed, saved to a temp file and checked with `gofmt -l`/`-d` against
that):

```
git add internal/httpapi/production_auth.go internal/httpapi/production_auth_test.go
git cat-file -p :backend/internal/httpapi/production_auth.go      > /tmp/a.go && gofmt -l /tmp/a.go  # clean
git cat-file -p :backend/internal/httpapi/production_auth_test.go > /tmp/b.go && gofmt -l /tmp/b.go  # one real
  # struct-field alignment issue found this way (TestConsoleAssertionOriginAllowed's
  # table type), fixed in the source file, re-staged, re-checked clean
```

gitleaks: see the commit this handoff accompanies for the scan command and
result (full-branch scan, base `ced1bfb..HEAD`).

## Risks / things to sign off on

1. **The `ORIGIN_REJECTED` error message text changed** (not the code or
   HTTP status) to reflect that two origins are now legitimate instead of
   one. If any caller (frontend, monitoring) matches on the exact message
   string rather than the `error.code` field, this is a breaking text
   change -- grep of `web/` for the literal message text found no match,
   but flagging since I did not exhaustively audit every log-scraping or
   alerting rule outside this repo.
2. **The console-issuer-origin acceptance path is unverified against a
   live signer**, same boundary every prior slice in this handshake has
   recorded -- no real console/platform counterpart was reachable from
   this worktree. Only the invoice-origin path is what the 2026-09-03
   canary actually proved was broken and (per this fix, pending a real
   redeploy and re-canary) fixed.
3. Ran directly, single agent, no sub-agents or forks dispatched for
   research or implementation. No server/production contact of any kind.

## Follow-ups (recommended, not blocking this slice's delivery)

1. After this fix deploys, run a real canary identical in shape to the one
   that found this bug: confirm the embedded admin iframe's redemption now
   succeeds end to end and `auth.console_assertion.exchanged` appears in
   the audit log, not `auth.console_assertion.rejected` with
   `origin_rejected`.
2. If the console-issuer-origin acceptance path is ever confirmed to have
   no real caller (the design intent was always same-origin redeem by the
   invoice app, per `XM-INV-ASSERT-HANDSHAKE`'s protocol diagram), consider
   whether keeping it is worth the extra branch versus removing it as
   dead code -- not done here since removing a still-plausible legitimate
   path was not this fix's call to make unprompted.
