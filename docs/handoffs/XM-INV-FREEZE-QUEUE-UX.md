# XM-INV-FREEZE-QUEUE-UX: eligibility freeze queue operability (CR-0007)

- **status:** implemented and self-tested locally (backend `go build`/`go
  vet` clean; full `go test -p 1 -count=1 ./...` against real PostgreSQL,
  17/17 packages green including `internal/auth` on the first try, no rerun
  needed; frontend `npm run typecheck`, `npm test` 94/94 tests, `npm run
  build`; `gofmt` clean on every touched file; `gitleaks git` clean over the
  three new commits). Not deployed, no production or server contact, no
  release ceremony run, no RC advanced, not clicked through in a real
  browser or a real/staged console embed.
- **branch:** `ai/claude/XM-INV-FREEZE-QUEUE-UX`, based on
  `ai/claude/XM-INV-AUTOLOGIN` at `4c72520`, worktree
  `K:/发票/wt-XM-INV-FREEZE-QUEUE-UX`.
- **commits** (`git log --oneline 4c72520..HEAD`, oldest first):
  - `eb232a0` feat(eligibility): show source platform user id on the admin
    freeze queue (CR-0007 problem one)
  - `64671d9` feat(eligibility): style the embedded-admin toast anchor and
    inline resolve result (CR-0007 problem two's CSS)
  - `6e427a9` feat(eligibility): add distinguishable resolve-precondition
    error codes (CR-0007 problem three)
- Not pushed, not merged.

## Summary

Implements `docs/change-requests/CR-0007-invoice-admin-freeze-queue-operability.md`
in full: problems one and two (P1, blocking normal manual review of the
eligibility-freeze queue) and problem three (P2, informational). The queue
(`GET /api/v1/admin/eligibility-freezes`) and drawer
(`POST .../resolve`) previously gave operators no way to identify which
upstream account a frozen record belonged to (an 83-record production
backlog was only resolvable by querying the database directly), gave no
visible feedback when a resolve request was rejected inside the
platform-console iframe embed, and collapsed four different rejection
reasons into one indistinguishable 409/503.

### Problem one -- source platform user ID

- Backend: `eligibilityFreezeSelect` now selects the already-joined
  `external_accounts.external_user_id`; `EligibilityFreeze` and
  `EligibilityFreezePageQuery` gain the field and an optional exact-match
  filter (`external_user_id`, combinable with `source_instance_id`);
  `eligibilityFreezeDTO` adds it to the response, plain and unmasked (the
  same disclosure posture this endpoint already applies to other
  administrator-only fields, and the same numeric ID the platform console's
  own user detail page already shows in the clear).
- Frontend: `BackendEligibilityFreeze`, `mapEligibilityFreeze`'s allowed-key
  whitelist, `EligibilityFreeze`/`EligibilityFreezeFilters` all gained the
  field in the *same commit* as the backend DTO change -- the frontend's
  strict `exactObjectKeys` whitelist means a one-sided rollout would make
  the whole queue page report "invalid format" and stop rendering, exactly
  as the CR's compatibility section warns.
- UI: a new "来源用户 ID" column and drawer field, plus a debounced
  (400ms) free-text filter input next to the existing status/reason
  selects, visible in every entry point (standalone and all three embedded
  scopes) since it is not gated behind `embeddedAdminPlatform()`.
- `docs/ELIGIBILITY-OPERATIONS.md`'s "It never returns external user IDs"
  claim is reversed with a pointer to this CR.

### Problem two -- resolve result visibility in embedded mode

- The drawer's `resolve()` now keeps a local `resolveError` state and shows
  an inline status block (message + server error code) under the form on
  every failure path (client-side validation *and* server rejection), in
  addition to the existing toast.
- On success, the drawer is no longer nulled immediately: `EligibilityFreezesPage`
  now passes the resolved item back into `selected`, so the drawer's
  existing "该冻结记录已安全解除" branch renders as the inline success
  confirmation, then auto-closes after 1.6s (guarded so it only closes if
  the operator has not already navigated elsewhere) -- balancing "show
  success inline" against not slowing down working through dozens of queue
  records one at a time.
- The toast itself gets a `toast-anchor-top` class in `ui_mode=embedded_admin`
  mode, repositioning it to the document's top instead of bottom-right: the
  embedded console can stretch this page's iframe up to 4000px while the
  real browser viewport stays much shorter, so a toast anchored to the
  document's bottom-right corner could previously render entirely outside
  the visible area. The drawer itself did not have this problem
  (`.drawer-layer` is already `position: fixed; inset: 0`, i.e. viewport-
  pinned regardless of document height) -- the inline status block alone
  would have been enough to satisfy the CR's acceptance criterion; the toast
  fix is additional, since the CR asked for both.
- No platform-line (xingmang-platform) changes: the drawer-inline-status +
  toast-repositioning approach fully resolves the issue without a
  postMessage envelope, which the CR left as the implementer's choice.

### Problem three -- distinguishable resolve error codes

- `domain` gains four sentinels: `ErrEligibilitySourceStale` (wraps
  `ErrSourceUnavailable`), `ErrEligibilityProjectionPending`,
  `ErrEligibilityRefundExposed`, `ErrEligibilityEvaluationUnmatched` (all
  three wrap `ErrInvalidState`) -- every existing `errors.Is(err,
  domain.ErrSourceUnavailable)` / `errors.Is(err, domain.ErrInvalidState)`
  check anywhere else in the codebase keeps matching unchanged.
- `ResolveEligibilityFreeze`'s six return points (five-stream freshness,
  the target row's own `SOURCE_REFUND` short-circuit, the combined
  refund-exposure query's four sub-cases, the projection-job check, and
  both branches of the balance-evaluation check) now report the specific
  sentinel for the exact condition each already checked -- no condition,
  order, or permission/CSRF/audit behavior changed; only which error value
  an existing return statement reports.
- `handleDomainError` maps each to its own code: `ELIGIBILITY_SOURCE_STALE`
  503, `ELIGIBILITY_PROJECTION_PENDING`/`ELIGIBILITY_REFUND_EXPOSED`/
  `ELIGIBILITY_EVALUATION_UNMATCHED` 409 -- version conflict and every other
  existing error unchanged.
- `friendlyError()` shows a specific Chinese sentence per new code while
  preserving the server's `code` on the thrown error; fallback for every
  other status/code is byte-for-byte unchanged.

**Naming deviation from the CR text, flagged explicitly:** CR-0007's own
"契约变化" section suggests `ELIGIBILITY_BALANCE_NOT_SAFE` and
`ELIGIBILITY_REFUND_EXPOSURE`, but labels these as a suggestion ("新增细分
错误码**建议**"). This slice's dispatch from the team lead specified
`ELIGIBILITY_EVALUATION_UNMATCHED` and `ELIGIBILITY_REFUND_EXPOSED` instead.
Since the CR text itself frames its names as non-binding and the dispatch is
the direct task instruction, the dispatch's names shipped. No functional
difference either way -- purely a string literal choice.

## Commit-to-file mapping (why some files span more than one commit)

Because problems one/two/three touch the same small set of shared files and
functions (the queue endpoint, the DTO, the drawer, `friendlyError`), a
clean one-file-per-problem split was not achievable everywhere without
fragile hunk-level surgery. Where a *compilation* dependency existed --
`postgresstore/eligibility_operations.go`'s `ResolveEligibilityFreeze` remap
and its integration test's updated assertions both reference the new
`domain` sentinels, and `httpapi`'s new test does too -- those pieces were
deliberately held back and re-applied only in the third commit, verified
buildable and green at each step along the way (backend `go build`/`go vet`
and the full eligibility test subset ran clean after every commit's staged
content, not just at the end). Where no such dependency existed (`web/src/App.tsx`,
which carries problem one's UI and problem two's drawer/toast JS in the same
two functions), the files were bundled into the commit for their dominant
concern and this is called out explicitly in that commit's message rather
than left implicit.

## Files changed

Backend:
- `backend/internal/domain/types.go` -- four new sentinel errors (problem
  three).
- `backend/internal/domain/types_test.go` -- sentinel-compatibility test.
- `backend/internal/httpapi/operations.go` -- `eligibilityFreezeDTO` gains
  `external_user_id`; `listEligibilityFreezes` parses the new query param
  (problem one).
- `backend/internal/httpapi/operations_source_filter_test.go` -- extends
  the existing filter-forwarding fake/tests with a new eligibility-freeze
  `external_user_id` case.
- `backend/internal/httpapi/eligibility_operations_test.go` -- the DTO
  redaction test's forbidden/required lists updated for the (intentional)
  reversal, plus a new `handleDomainError` mapping test for all four new
  codes and the unchanged generic ones.
- `backend/internal/httpapi/server.go` -- `handleDomainError` gains the
  four new cases, ordered ahead of the generic ones they wrap.
- `backend/internal/postgresstore/eligibility_operations.go` -- struct
  field, SELECT column, page-query filter + validation, and the
  `ResolveEligibilityFreeze` sentinel remap.
- `backend/internal/postgresstore/eligibility_operations_integration_test.go`
  -- five existing assertions tightened to the specific sentinel; new test
  covering the SELECT/filter (including the combined
  `external_user_id`+`source_instance_id` case and validation rejection).

Frontend:
- `web/src/types.ts` -- `EligibilityFreeze.externalUserId`,
  `EligibilityFreezeFilters.externalUserId`.
- `web/src/lib/http-api.ts` -- `BackendEligibilityFreeze`,
  `mapEligibilityFreeze`, filter validation/query building, `friendlyError`.
- `web/src/lib/mock-api.ts` -- fixtures gain `externalUserId`, filter
  support, and the simulated SOURCE_REFUND rejection's code.
- `web/src/App.tsx` -- queue table column + filter input +
  `EligibilityFreezesPage`/`EligibilityFreezeDrawer` changes for both
  problems one and two (see mapping note above).
- `web/src/styles.css` -- `.toast-anchor-top` (base + narrow-viewport) and
  `.freeze-resolution-result[-error]`.
- `web/src/lib/http-api.eligibility-external-user-id.test.ts` (new) --
  mapping and filter tests.
- `web/src/lib/http-api.eligibility-resolve-error-mapping.test.ts` (new) --
  per-code error-mapping tests.

Docs:
- `docs/ELIGIBILITY-OPERATIONS.md` -- reverses the "never returns external
  user IDs" claim; documents the new filter and the four new error codes.

`backend/Dockerfile` untouched. No files under the platform repo
(`xingmang-platform`) touched.

## Acceptance criteria (from the CR)

1. *Admin can see the source platform's digital user ID from list and
   drawer without querying the DB, in all three embedded entries and
   standalone* -- met. The column/filter/drawer field are unconditional
   (not gated behind `embeddedAdminPlatform()`), so they render identically
   in every entry point.
2. *Filter by digital user ID (optionally with source instance), matching a
   direct DB query* -- met. Backend-verified end to end by
   `TestEligibilityFreezePageSelectsAndFiltersByExternalUserID` (unscoped,
   `external_user_id`-only, combined with `source_instance_id`, an
   unrelated ID returning zero rows, and two invalid-input rejections).
3. *A rejected resolve inside any embedded entry shows its reason within the
   drawer's visible area without scrolling the host page* -- met via the
   inline `resolveError` status block; `.drawer-layer` being
   viewport-`fixed` (not iframe-document-relative) means this is visible
   regardless of iframe height. The toast anchor fix is additional
   defense-in-depth for the toast itself, which was the other half of the
   CR's described symptom.
4. *Four distinct triggering conditions produce four distinct Chinese
   reasons in the drawer, corresponding 1:1 to the actual condition* --
   met. Backend integration test drives all four conditions through
   `ResolveEligibilityFreeze` and asserts the specific sentinel each time;
   `http-api.eligibility-resolve-error-mapping.test.ts` drives all four
   codes through `resolveEligibilityFreeze` end to end and asserts the four
   distinct Chinese sentences (plus that two ordinary codes keep their
   unchanged generic fallback).
5. *Standalone behavior does not regress; docs match implementation* --
   the toast-anchor class only applies in `ui_mode=embedded_admin`, so
   standalone toast positioning is byte-for-byte unchanged. One deliberate,
   flagged behavior change applies to *both* standalone and embedded: the
   drawer no longer auto-closes instantly on a successful resolve (it shows
   the resolved confirmation for 1.6s first) -- this is the mechanism that
   satisfies "show the mutation result inline... success" from the slice's
   dispatch, not a regression; see Risks below. `docs/ELIGIBILITY-OPERATIONS.md`
   was updated to match every change in this slice.

## Tests run

Backend (from `backend/`, `INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_freezeux`,
a dedicated database created for this slice -- never `invoice_test_release`):
- `go build ./...` -- clean.
- `go vet ./...` -- clean.
- `go test -p 1 -count=1 ./...` -- **all 17 packages `ok`**, including
  `internal/auth` (the known loopback-flake package) passing on the first
  try, no rerun needed. Ran this full suite twice (once mid-implementation,
  once after the final commit) plus multiple targeted `-run "Eligibility|HandleDomainError"`
  passes while iterating; all green every time.
- `"$(go env GOROOT)/bin/gofmt" -l` on every touched file after stripping
  CRLF (this checkout's `.go` files are CRLF on disk, which makes raw
  `gofmt -l`/`-d` report full-file noise unrelated to real formatting --
  see the file-level check method used) -- clean.

Frontend (from `web/`, via `npm run <script>`; confirmed this project uses
npm/`package-lock.json`, not pnpm, despite the dispatch's "pnpm or the
repo's script" hedge):
- `npm run typecheck` -- clean.
- `npm test` (vitest) -- **94/94 passed**, 7 files (5 pre-existing + 2 new).
- `npm run build` -- clean, including the production tree-shake path.

`gitleaks git --no-banner --log-opts="4c72520..HEAD" .` -- 3 commits
scanned, no leaks.

## Not run / could not fully verify

- **No literal component "render" test for the queue list/drawer.** This
  repo's `web/` package has no DOM test harness at all (no jsdom/happy-dom,
  no `@testing-library/react`; `vitest` runs in plain Node, confirmed by an
  existing test file's own comment and by `package.json`/`vite.config.ts`
  having no `test.environment` or testing-library dependency anywhere in
  the repo, including the main checkout). Adding one would mean introducing
  net-new dev dependencies repo-wide -- a real infrastructure decision
  beyond this CR's scope, and `npm install`-style dependency additions in a
  fresh worktree on this machine are a known source of Windows
  rename-to-nonempty-directory failures per prior sessions' experience.
  Instead, the "mapping"/"filter" tests the dispatch asked for were
  implemented as fetch-mocked tests against the exported API client
  (following this repo's one existing fetch-level test's precedent exactly),
  and the drawer's error-message-per-code logic is covered end to end at
  the `resolveEligibilityFreeze` function level (the same logic the drawer
  would render, minus the JSX itself). I read through the final JSX by hand
  (table cell, drawer `<dl>` row, filter input wiring, inline status block,
  toast class) to check it, but that is code review, not an executed test.
- **Not exercised in a real browser or against a real console embed** (no
  screenshot/Playwright pass) -- the CR's acceptance criteria talk about
  three embedded entry points and a standalone one, none of which were
  clicked through live.
- Did not verify against a staged/production copy of the 83-record backlog
  mentioned in the CR's evidence section -- only against the integration
  test's synthetic fixtures.

## Risks / things to sign off on

- **Drawer no longer auto-closes instantly on a successful resolve** (see
  acceptance criterion 5 above) -- it now shows the "已安全解除"
  confirmation for ~1.6s before closing itself. This is an intentional
  behavior change in both standalone and embedded modes, chosen because it
  was the only way to literally satisfy "show the mutation result inline...
  success" given the drawer unmounted immediately before. If this extra
  1.6s per successful resolution is unwanted while working through a long
  queue, it is a one-line change (drop the `window.setTimeout`, or shorten
  it) in `EligibilityFreezesPage`'s `onResolved`.
- **Error-code naming deviates from the CR text's own suggestion** (see the
  naming-deviation note above) -- purely cosmetic (string literals only,
  same HTTP status codes, same underlying conditions), but worth an
  explicit sign-off since it is a literal string mismatch against the CR
  document if anyone diffs against it later.
- The `external_user_id` filter's validation bound (512 chars, no control
  characters) is a judgment call: the column itself
  (`external_accounts.external_user_id`) has no `CHECK` constraint; 512 was
  chosen to match the closest existing precedent for this same semantic
  value (`external_account_binding_proofs.external_user_id`'s existing
  512-char bound), not a value taken directly from this CR.

## Follow-ups (recommended, not blocking this task's delivery)

- Consider adding a jsdom/`@testing-library/react` harness to `web/` as its
  own separate infrastructure task, if literal component-render/snapshot
  tests are wanted for this or future admin-console UI work -- flagged
  above as the one gap this slice could not close within its own scope.
- The CR explicitly excludes investigating *why* the 83-record backlog
  keeps growing (a separate read-only research task per the CR's own "不在
  本 CR 范围" section) -- unaffected by this slice, just noting it is still
  open elsewhere.
