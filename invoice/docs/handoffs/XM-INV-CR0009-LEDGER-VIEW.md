# XM-INV-CR0009-LEDGER-VIEW: operator "用户账本" view (CR-0009)

- **status:** implemented and self-tested locally; full backend suite green
  (`go test -p 1 -count=1 ./...`, two full runs), `go vet`/`go build`/`gofmt`
  clean; web `npm run typecheck`/`npm test -- --run`/`npm run build` clean;
  `gitleaks` clean.
- **branch:** `ai/claude/XM-INV-CR0009-LEDGER-VIEW` (based on
  `ai/claude/XM-INV-AUTOLOGIN` at commit `a7c5072`), worktree
  `K:/发票/wt-XM-INV-CR0009`.
- **spec:** `docs/change-requests/CR-0009-invoice-admin-user-ledger-view.md`
  (read in full before coding); design doc
  `docs/superpowers/specs/2026-09-03-xm-inv-eligibility-simplification-design.md`
  section 3(E) (finalized, not re-derived); prerequisite handoffs read first:
  `docs/handoffs/XM-INV-USER-LEDGER-QUERY.md` (slice 4, provisional route),
  `docs/handoffs/XM-INV-ELIG-AUTO-RECONCILE.md` (slice 1),
  `docs/handoffs/XM-INV-ELIG-QUEUE-NARROW.md` (slice 2),
  `docs/ELIGIBILITY-OPERATIONS.md`.

## Summary

Finalizes CR-0009's operator "用户账本" (user ledger) view end to end:

- **Backend**: replaces slice 4's provisional `GET /api/v1/admin/
  eligibility-ledger` with `GET /api/v1/admin/accounts/ledger` (list) and
  `GET /api/v1/admin/accounts/{external_account_id}/ledger` (detail), CR-0009's
  exact contract, computing a four-state `block_state`
  (`frozen_manual_review`/`not_invoiceable_pending_reconciliation`/
  `below_threshold`/`invoiceable`) as a read-only composition of existing
  signals, plus a concrete Chinese `block_reason` sentence for the detail
  endpoint.
- **Frontend**: a new "用户账本" admin nav tab/route next to "资格冻结"
  (list + detail drawer, reusing existing components exactly), and the
  "资格冻结" tab's default view narrowed to hide freeze rows the
  auto-reconciliation/queue-narrowing slices made obsolete, with a
  "显示全部" toggle.
- **Docs**: `docs/ELIGIBILITY-OPERATIONS.md`'s "管理员账本视图" section
  replaces the provisional "Per-account ledger" section; a route-finalized
  pointer note on `docs/handoffs/XM-INV-USER-LEDGER-QUERY.md`.

## Exact routes

- `GET /api/v1/admin/accounts/ledger` (list)
- `GET /api/v1/admin/accounts/{external_account_id}/ledger` (detail)

Both behind the same `s.require("admin", ...)` middleware as
`/admin/eligibility-freezes` (admin role + IP allowlist; no MFA/CSRF, pure
`GET`s). The provisional `/admin/eligibility-ledger` route is removed
(never shipped to production, per the task brief).

## Design decisions and deviations, flagged as made

1. **`recharges_since_start_minor`/`_count` now require
   `verification_state='verified'`.** Slice 4's provisional query omitted
   this filter for this specific sum (its own handoff says so explicitly,
   citing "matching the design doc's wording exactly"). Design section 2's
   fuller formula, and every other WALLET_CASH/SUBSCRIPTION_CASH formula in
   this codebase (`ListEligibilitySummaries`'s own `l`/`r` LATERALs,
   `accountLedgerConsumedInvoiceableLateral`), does include it. Corrected
   here as a deliberate fix, not an unexplained behavior change --
   `verification_state='verified'` is what every sibling formula requires
   before counting a lot's cash as real. Flagged since it changes what the
   provisional endpoint (never in production) would have returned.
2. **`frozen_manual_review` is `has_open_freeze OR eligibility_status=
   'frozen'`, not filtered by design 3(C)'s manual-review reason
   whitelist.** Per the task brief: "after slices 1-2 every remaining open
   freeze is a manual-review one" (XM-INV-ELIG-AUTO-RECONCILE/
   XM-INV-ELIG-QUEUE-NARROW stopped any new UNKNOWN_NEGATIVE_BALANCE/
   USAGE_EXCEEDS_LEDGER/generalized-LATE_FINALIZED_EVENT freeze from being
   created), so filtering by reason at this layer would be redundant. The
   whitelist is still kept as a documented Go constant
   (`accountLedgerFreezeReasonDescriptions`, `httpapi/accounts_ledger.go`),
   per the brief's own explicit instruction, doing double duty as the
   Chinese description source for `block_reason`. The `OR eligibility_status
   ='frozen'` half is this implementation's own addition, not brief text:
   it exists specifically to satisfy the brief's named hard-rule shape ("a
   frozen status with no open freeze row must still produce a row with a
   sensible block_state") -- without it, that defensive shape would fall
   through to a wrong state.
3. **"No evaluation ever recorded" counts as "not in the good set"** for
   `not_invoiceable_pending_reconciliation`'s "latest evaluation" branch --
   the brief's own formula, taken literally (`COALESCE(...,'')` is trivially
   `NOT IN` the three good values). This matches
   `ResolveEligibilityFreeze`'s own existing precedent (no evaluation on
   record maps to `ELIGIBILITY_EVALUATION_UNMATCHED`, not a free pass), so
   it is not a new invention, but it does mean a freshly-bootstrapped
   account with zero lots/evaluations/jobs shows as
   `not_invoiceable_pending_reconciliation` rather than `invoiceable` --
   the brief's own named hard-rule test case for this exact shape, covered
   by `TestAccountLedgerPageCoversEveryBlockStateAndFilters`'s "zero lots,
   no evaluation, no job, no freeze" subtest.
4. **Detail response keeps `recharges_since_start_count`** even though
   CR-0009's own illustrative detail JSON omits it (redundant with
   `recharges_since_start[].length`). The CR's own prose says the detail
   contract is "list fields plus X" (an addition, not a replacement), read
   literally here rather than inferring an omission from the shortened
   example.
5. **`block_reason` renders amounts in 元 only when the amount is already
   known to be true CNY minor units** (`invoiceable_now_minor`,
   `threshold_minor`, a frozen `LATE_FINALIZED_EVENT` lot's own
   `issued_minor`). A balance-checkpoint/carry-forward-proof discrepancy
   (`LatestEvaluationExpectedUnits`/`DifferenceUnits`) is reported in its own
   raw service units, explicitly labeled as such, not converted to 元:
   `source_account_eligibility_state.unit_code` (e.g. this codebase's own
   test fixture value `SUB2_BALANCE_1E8`) is an upstream-source-specific
   accounting unit with no established, verified global conversion factor to
   CNY anywhere in this system (funding-lot-level cash/service-unit exchange
   rates are computed individually per lot inside
   `funding_lot_consumption_state`, never as one global constant) --
   inventing one for display would be a fabricated precision this endpoint
   cannot stand behind. Consistent with this same design's own explicit
   principle elsewhere ("不假装非现金单位是人民币" for
   `opening_balance_units`/`legacy_noninvoiceable`/`noncash`). A deliberate,
   narrower reading of the brief's "amounts in 元 with two decimals"
   instruction for this one sentence shape.
6. **`sort=block_state` orders `frozen_manual_review` first** (most-needs-
   attention first), tie-broken by id descending. CR-0009 does not specify
   a direction; this is an implementation choice for an operator triage
   view, not brief text.
7. **Consumption timeline is bucketed "by day" (Asia/Shanghai calendar
   day), not "by usage checkpoint"** -- the task brief asked to say which of
   the two the existing tables support without a new aggregate table.
   `consumption_allocations` joined to `source_usage_events` (for
   `event_time`) and `funding_lots` (to scope to
   WALLET_CASH/SUBSCRIPTION_CASH/verified/CNY) already supports a daily
   `sum(cash_minor_delta)` grouping with no new table; bucketing by the
   Asia/Shanghai calendar day (not UTC) matches how the policy itself is
   anchored (`2026-09-01 00:00 Asia/Shanghai`) and how an operator reads
   "daily consumption".
8. **A real pgx/Postgres parameter-type-inference limitation, worked
   around.** The first implementation of the `sort=block_state` cursor's
   rank lookup was a second SQL query whose only projected column was a
   bare `CASE` expression (no real table column) evaluated against the
   shared CTE. That query failed at runtime with `could not determine data
   type of parameter $N` (SQLSTATE 42P18) under pgx's default extended
   protocol, *even with every parameter explicitly cast* -- verified this was
   not a real SQL error by preparing the identical query text directly via
   plain `psql PREPARE` (both with and without explicit parameter types),
   which succeeded cleanly both times. The fix: select the account's raw,
   *typed* signal columns instead (`has_open_freeze`, `eligibility_status`,
   `has_projection_job`, `latest_evaluation_status`, `invoiceable_minor`) and
   compute the rank in Go (`accountBlockStateRank`, a plain mirror of the SQL
   `accountBlockStateRankExpr`'s own logic) -- this sidesteps the issue
   entirely and is simpler to reason about. Flagged in code comments at both
   functions; `TestAccountLedgerPageCoversEveryBlockStateAndFilters`'s
   `sort=block_state` subtest exercises every block state produced by the
   SQL side through this Go-side function across a real paginated walk, so a
   drift between the two would surface as an out-of-order page there.
9. **Email-prefix search not implemented.** User emails are encrypted via
   securefields; there is no plaintext searchable column, and the task
   brief's own instruction was to omit search and record the deviation in
   that case. Recorded here.
10. **File renames**: `postgresstore/eligibility_ledger.go` ->
    `accounts_ledger.go` (and its test file), `httpapi/eligibility_ledger.go`
    -> `accounts_ledger.go` (and its test file) -- via `git mv`, preserving
    history. Not literally required by the brief, but the route/contract
    changed completely (list+detail, new field names, new path), and the
    old "eligibility ledger" name no longer matches what either file
    contains; done for clarity, flagged since it is a naming choice beyond
    the brief's own literal instructions.
11. **No source-instance-id filter control on the new list page's UI.**
    The task brief's own frontend scope wording (item 4) names only "search
    box by external_user_id, sort toggle, block_state badge column" -- no
    source-instance selector, unlike the "资格冻结" page's own (pre-existing)
    one. Embedded-platform auto-scoping (the existing
    `useEmbeddedAdminPlatformSourceInstanceId` effect, mirrored from
    `EligibilityFreezesPage`) still applies invisibly; only the manual,
    visible dropdown is omitted, matching the brief's literal scope.
12. **Added a `NOT_FOUND` branch to the shared `friendlyError` fallback**
    (`web/src/lib/http-api.ts`), rather than only handling it locally in the
    new detail drawer. This account ledger detail endpoint is this app's
    first GET-by-id 404 consumer; the raw server message for
    `domain.ErrNotFound` is the English text `"not found"`, which would
    otherwise surface verbatim through the generic `message ||` fallback.
    The new branch benefits every existing and future 404 in this app, not
    only this feature -- flagged as a small, deliberate, non-breaking
    improvement (no existing test pinned the old fallback behavior for this
    code).
13. **Vitest coverage lives at the `http-api` mapping layer (mocked
    `fetch`), not as rendered-component tests.** This repository has no
    jsdom/happy-dom test environment or `@testing-library/react` installed
    (confirmed via `package-lock.json`/`node_modules`), and no existing test
    anywhere in this project renders `App.tsx`'s components -- every
    existing "frontend coverage" test in this codebase (e.g.
    `http-api.eligibility-external-user-id.test.ts`) already follows this
    same mocked-fetch-at-the-API-layer style for exactly this reason. The
    new `http-api.account-ledger.test.ts` exercises the identical data every
    rendered row/badge/drawer field is built from (all four `block_state`
    values, an empty page, detail field mapping, cursor round-tripping,
    filter forwarding, block_state/block_reason consistency, the new 404
    path, and the 403/generic-503 `friendlyError` fallbacks) as the closest
    available substitute for a true component-rendering test in this
    project's actual test infrastructure. Flagged as the brief's own
    "Vitest coverage" ask being satisfied at this layer rather than a
    render-based one.
14. **First backend commit's scope grew beyond its own message.** Commit
    `55052f4` ("evolve account ledger store query...") ended up including
    the httpapi layer, `server.go`, `service_contract.go` and
    `application/service.go` too, not just the postgresstore files its
    message describes -- those files were already `git add`-staged from an
    earlier `gofmt`-verification pass and were not unstaged before
    committing. The commit's actual contents are correct and complete (the
    full, coherent backend implementation for this CR), just broader than
    its own message's scope prefix suggests; flagging rather than silently
    leaving it unmentioned.

### A pre-existing gap discovered, fixed in a follow-up commit

While reading `web/src/lib/http-api.ts`'s `eligibilityFreezeReasons` list
and `App.tsx`'s `eligibilityFreezeReasonLabels` (both used to validate/label
`/admin/eligibility-freezes` rows), neither included `EVENT_DEAD` or
`POLICY_ANCHOR_BLOCKED` -- two `freeze_reason` values migration `0016`
(XM-INV-POLICY-ANCHOR) added to the backend's own CHECK constraint before
this CR-0009 task started. If the backend ever returned a freeze row with
either reason, `mapEligibilityFreeze`'s existing validation would throw for
that *entire page* (`"资格冻结记录包含无效字段，已停止显示。"`), not just
that one row -- predating this task, unrelated to CR-0009's own stated
scope. Originally left flagged-but-unfixed here; addressed in a same-branch
follow-up commit (see "Follow-up" below) at the requesting team lead's ask,
before merge.

## Follow-up: freeze-reason drift fixed, response mapping made tolerant

Requested by the team lead reviewing this branch before merge, addressing
the gap flagged above:

- `EVENT_DEAD`/`POLICY_ANCHOR_BLOCKED` added to `types.ts`'s
  `EligibilityFreezeReason` union, `http-api.ts`'s `eligibilityFreezeReasons`
  (used for the `reason` *filter* query param's own validation -- an
  operator can now filter the freeze queue by either), and `App.tsx`'s
  `eligibilityFreezeReasonLabels` (`EVENT_DEAD` → "事件已失效（源事实死信）",
  `POLICY_ANCHOR_BLOCKED` → "策略锚定被阻断").
- **Root-caused and closed the underlying class of bug, not just this one
  instance of it.** `mapEligibilityFreeze`'s validation of a *response*
  item's `freeze_reason` (`http-api.ts`) is no longer "must be a member of
  the closed, labeled list" -- it is now "must be a well-formed, enum-shaped
  code" (`freezeReasonPattern`, `/^[A-Z][A-Z0-9_]{0,62}$/`; still rejects
  empty/lowercase/injection-shaped/over-length values). This means a future
  backend `freeze_reason` this frontend has not labeled yet renders instead
  of blanking the entire "资格冻结" list -- the exact failure mode this
  gap already caused once. `EligibilityFreeze.reason`'s own type widened
  from the closed `EligibilityFreezeReason` union to
  `EligibilityFreezeReason | (string & {})` to match (literal-type
  autocomplete preserved for known values, still accepts any string).
  The `reason` *filter* query param is deliberately **not** loosened the
  same way -- it stays validated against the closed, labeled list, since
  filtering by a reason this UI cannot label would be a confusing dead end,
  not a data-tolerance question.
- `App.tsx` gained `freezeReasonLabel(reason)`, a small helper
  (`eligibilityFreezeReasonLabels[reason] ?? reason`) used at both places a
  freeze reason is rendered (the list row and the resolution drawer's
  title) -- an unlabeled reason now shows its raw code instead of
  `undefined`.
- New test file `http-api.eligibility-freeze-reason-tolerance.test.ts`:
  both newly-catalogued reasons map through cleanly; an unrecognized-but-
  well-formed reason on one row does not reject that row or the rest of the
  page; six malformed-shape cases (empty, lowercase, leading digit, embedded
  space, CR/LF injection, over the length cap) are still rejected; a
  non-string `freeze_reason` is rejected; the `reason` filter param still
  rejects an uncatalogued value and still accepts both new reasons.
- Gates re-run on this range: `npm run typecheck` (both tsconfigs), `npm
  test -- --run` (10 files, 141 tests, all green), `npm run build` all exit
  0; `gitleaks git --log-opts="a7c5072..HEAD" .` clean over the full
  updated range (see the commit id reported to the team lead).

## Files changed

**Backend:**
- `backend/internal/postgresstore/accounts_ledger.go` (renamed from
  `eligibility_ledger.go`, rewritten) -- `AccountLedgerListEntry`/
  `AccountLedgerPageQuery`/`AccountLedgerPage`/`AccountLedgerRecharge`/
  `AccountLedgerConsumptionDay`/`AccountLedgerDetail`,
  `Store.ListAccountLedgerPage`/`Store.GetAccountLedgerDetail`,
  `accountBlockStateExpr`/`accountBlockStateRankExpr`/
  `accountBlockStateRank`, the shared LATERAL SQL fragments and signals
  select list.
- `backend/internal/postgresstore/accounts_ledger_integration_test.go`
  (renamed, rewritten) -- see Tests below.
- `backend/internal/httpapi/accounts_ledger.go` (renamed from
  `eligibility_ledger.go`, rewritten) -- `listAccountLedger`/
  `getAccountLedgerDetail` handlers, `accountLedgerListDTO`/
  `accountLedgerDetailDTO`, `buildAccountBlockReason` and its
  `accountLedgerFreezeReasonDescriptions`/
  `accountLedgerEvaluationStatusDescriptions` maps, `formatShanghai`/
  `formatYuanMinor`.
- `backend/internal/httpapi/accounts_ledger_test.go` (renamed, rewritten) --
  see Tests below.
- `backend/internal/httpapi/server.go` -- two route registrations replacing
  the one provisional route.
- `backend/internal/httpapi/server_test.go` -- `TestAdminEndpointsRequireAdminRole`'s
  table updated to the two new paths.
- `backend/internal/httpapi/service_contract.go` -- `OperationsService`
  interface: `ListAccountLedgerPage`/`GetAccountLedgerDetail` replacing
  `ListEligibilityLedgerPage`.
- `backend/internal/httpapi/operations_source_filter_test.go` --
  `fakeSourceFilterOperations` updated to the new interface methods
  (required, not optional -- see XM-INV-USER-LEDGER-QUERY's own handoff for
  why a missed method here fails silently at runtime, not compile time).
- `backend/internal/application/service.go` -- `ListAccountLedgerPage`/
  `GetAccountLedgerDetail` passthroughs replacing the old one.
- `docs/ELIGIBILITY-OPERATIONS.md` -- "管理员账本视图" section replacing
  "Per-account ledger".
- `docs/handoffs/XM-INV-USER-LEDGER-QUERY.md` -- route-finalized pointer
  note.
- `docs/handoffs/XM-INV-CR0009-LEDGER-VIEW.md` -- this file.

**Frontend:**
- `web/src/types.ts` -- `AccountBlockState`/`AccountLedgerListItem`/
  `AccountLedgerRecharge`/`AccountLedgerConsumptionDay`/
  `AccountLedgerDetail`/`AccountLedgerPage`/`AccountLedgerFilters`.
- `web/src/lib/api-contract.ts` -- `getAccountLedger`/
  `getAccountLedgerDetail` added to `InvoiceApiClient`.
- `web/src/lib/http-api.ts` -- Backend wire types, `mapAccountLedgerListItem`/
  `mapAccountLedgerRecharge`/`mapAccountLedgerConsumptionDay`/
  `mapAccountLedgerDetail`, `accountLedgerCursor`, the two API methods, a
  `NOT_FOUND` branch on `friendlyError`.
- `web/src/lib/mock-api.ts` -- fixture accounts (one per `block_state`) and
  the two mock methods.
- `web/src/lib/format.ts` -- `dateTimeShanghai`.
- `web/src/lib/workflow.ts` -- `isMechanicalReconciliationFreeze`.
- `web/src/lib/embedded-admin-scope.ts` -- `"account-ledger"`
  `AdminNavItemKey`.
- `web/src/App.tsx` -- nav item + route, `AccountLedgerPage`/
  `AccountLedgerDetailDrawer`, `accountBlockStateLabels`/
  `accountBlockStateTone`, `EligibilityFreezesPage`'s narrowing toggle.
- `web/src/styles.css` -- `.toolbar-toggle` (the narrowing checkbox; every
  other new element reuses existing classes verbatim -- see the code
  comment at this rule for why `.confirm-row` was not reused instead).
- `web/src/lib/http-api.account-ledger.test.ts` (new).
- `web/src/lib/workflow.test.ts` (new).
- `web/src/lib/embedded-admin-scope.test.ts` -- extended to the new
  `"account-ledger"` key.

**Follow-up** (freeze-reason drift fix, see that section above):
- `web/src/types.ts` -- `EligibilityFreezeReason` gains `EVENT_DEAD`/
  `POLICY_ANCHOR_BLOCKED`; `EligibilityFreeze.reason` widened to
  `EligibilityFreezeReason | (string & {})`.
- `web/src/lib/http-api.ts` -- `eligibilityFreezeReasons` gains the two
  reasons; `mapEligibilityFreeze`'s response-side reason check replaced
  with `freezeReasonPattern`, an enum-shape check.
- `web/src/App.tsx` -- the two new labels; `freezeReasonLabel` helper used
  at both render sites.
- `web/src/lib/http-api.eligibility-freeze-reason-tolerance.test.ts` (new).

**Not touched** (per the task brief's explicit scope, confirmed by grep
before finishing): `backend/internal/postgresstore/consumption.go`, any
evaluator/projection-worker code, `backend/Dockerfile`, `contracts/`,
`deploy/`, `K:/sub2api-src`, `K:/newapi-src`; the `/admin/eligibility-freezes`
server contract and `POST .../resolve` flow (both byte-for-byte unchanged,
per CR-0009's own "契约不变" statement); no migration added or changed (no
new columns needed); no tags created.

## Tests

**Store (`accounts_ledger_integration_test.go`),
`TestAccountLedgerPageCoversEveryBlockStateAndFilters`** -- real PostgreSQL,
one fixture per scenario plus subtests:

- `invoiceable` (a healthy account with a fresh `matched` evaluation).
- `source_instance_id` disambiguating two accounts sharing one
  `external_user_id` across different sources (CR-0007's own precedent).
- `not_invoiceable_pending_reconciliation` via the `eligibility_status`
  column and its five plaintext columns directly.
- `frozen_manual_review` reading the *latest* open `eligibility_freezes`
  row (two open rows on one account, asserts the newer one wins).
- `frozen_manual_review` with **no** open freeze row (the brief's own
  named defensive edge case) -- still returns a defined row.
- Recorded usage overage does not leak into `block_state` or perturb
  amounts.
- Zero funding lots, zero evaluations, zero projection jobs, zero freeze --
  the brief's own named hard-rule shape -- still returns a defined,
  `not_invoiceable_pending_reconciliation` row (see deviation 3 above for
  why that state, not `invoiceable`).
- The "latest evaluation" branch alone (a `negative_frozen` evaluation with
  no other signal) drives the pending state, constructed via direct SQL to
  test the guard in isolation (same precedent
  `TestPendingReconciliationOpenFreezeBlocksAutoExit` established for a
  different guard) -- not reachable via the normal evaluator today since
  `enterPendingReconciliationTx` sets `eligibility_status` atomically with
  any `negative_frozen` write.
- The projection-job signal alone drives the pending state.
- `below_threshold` (healthy in every other respect, under the threshold).
- Invalid `before_id`/`external_user_id`/`sort` are all rejected.
- A mismatched default-sort cursor pair (`before_invoiceable_minor` without
  `before_id`) is rejected.
- Keyset pagination walks every account exactly once in both sort modes
  (default `invoiceable_now_minor` descending, and `sort=block_state`
  -- asserts the rank never goes backwards across pages and a
  `frozen_manual_review` account leads).
- A stale/unknown `sort=block_state` cursor yields a defined empty page,
  not an error.
- `limit` bounded to the documented default/max of 100.

**`TestAccountLedgerDetail`**: unknown account -> not found; malformed
account id -> not found (not a 500-shaped error); a full round trip
(recharges, opening balance, a two-day consumption timeline summing
correctly); pending-reconciliation detail exposing the raw ingredients
`block_reason` is built from; `last_reconciled_at` staying pinned to the
earlier *matched* evaluation while `last_checkpoint_at` moves to a later,
unmatched one.

**HTTP handler (`accounts_ledger_test.go`)**: admin-role denial and IP
-allowlist denial for both routes (mirroring the freezes endpoint's own
tests, per the task brief's explicit ask); list contract shape (every
field, `threshold_reached` on both sides of the threshold, filter/sort/
cursor forwarding, default limit); detail contract shape (every field
including `opening_balance_units`/`recharges_since_start`/
`consumption_timeline`/`last_reconciled_at`, a non-generic `block_reason`);
404 for an unknown account through the existing `NOT_FOUND` envelope; a
null `block_reason` for an `invoiceable` account.

**Web (`http-api.account-ledger.test.ts`/`workflow.test.ts`)**: see
deviation 13 above for why these are the frontend's actual coverage of the
brief's four Vitest-coverage asks (list rows/badges, drawer fields, empty
state, 403/503 error states) -- field mapping for all four `block_state`
values, an empty page, negative-amount/unrecognized-`block_state`/extra
-field rejection, filter/cursor round-tripping, detail field mapping,
`block_state`/`block_reason` consistency validation (both directions),
opening-balance unit-code contract mismatch rejection, the new
404/`NOT_FOUND` friendly message, 403/generic-503 `friendlyError`
fallbacks, and `isMechanicalReconciliationFreeze`'s own narrowing
predicate against every relevant `freeze_reason`/`scope` combination.

## Gate results

**Backend**, from `backend/`, with the eight proxy variables unset and
`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_cr0009?sslmode=disable`
(a dedicated database created for this task, never `invoice_test`/
`invoice_test_release`/another agent's database):

```
go build ./...                 # exit 0
go vet ./...                   # exit 0
"$(go env GOROOT)/bin/gofmt" -l <every file this slice touches or adds>   # empty (staged-blob method, this machine's documented CRLF-checkout false-positive handling)
go test -p 1 -count=1 ./...    # two full runs, both fully green on the second attempt of any failing package
```

**First full run**: every package green except `cmd/eligibility-repair`
(5 subtests) and `cmd/identity-migrate` (2 subtests), all failing with a raw
`dial tcp 127.0.0.1:55432 ... connectex` error, not an assertion failure.
The Postgres container itself was confirmed healthy and reachable
throughout (`pg_isready`, a direct `psql` query, and `docker stats` all
checked clean during the failure) -- consistent with the documented pattern
in sibling slices' own handoffs (loopback TCP contention from several
concurrently-running agents sharing the same container/port, not a real
service outage). Re-running `./cmd/identity-migrate/...` alone: clean
immediately. Re-running `./cmd/eligibility-repair/...` alone: failed again
on the first retry (different symptom count, same connection-error
signature), clean on the second retry.

**Second full run** (confirmation pass): every package green on the first
try, no flake, including `internal/postgresstore` (151s) and
`internal/testdb`.

```
gitleaks git --no-banner --log-opts="a7c5072..HEAD" .
```

Clean: `5 commits scanned`, `no leaks found`, exit 0.

**Web**, from `web/` (node_modules mirrored from
`K:/发票/wt-XM-INV-AUTOLOGIN/web/node_modules` via `robocopy /MIR /XJ` --
run via PowerShell, not Git Bash, which mis-tokenizes the `/MIR` flag as a
POSIX path):

```
npm run typecheck   # tsc --noEmit -p tsconfig.app.json && tsc --noEmit -p tsconfig.node.json -- exit 0, both
npm test -- --run   # 9 test files, 128 tests, all passed
npm run build       # tsc (both configs) + vite build -- exit 0, 1585 modules transformed
```

Commit list: see `git log --oneline a7c5072..HEAD` -- 5 commits (store
layer, docs, web lib layer, web UI layer, web tests); see deviation 14
above for why the first commit's actual file list is broader than its own
message describes.

## Not verified

- No server/production connection -- explicitly out of scope, matching
  every prior sibling slice's own precedent.
- No live browser/embedded-iframe visual check (Playwright/chrome-devtools)
  of the three embedding entry points CR-0009's own acceptance criterion 5
  names -- the task brief's own gates list asked for `npm run typecheck`/
  `test`/`build`, not an end-to-end visual pass, and the platform side
  (`InvoiceConsolePanel.tsx`/`EmbeddedConsoleFrame.tsx`) is explicitly
  "无改动" per CR-0009 itself (the new tab is an iframe-internal route the
  embedding shell does not need to know about, same as CR-0007's own
  precedent) -- not exercised end-to-end here.
- `scripts/verify.ps1` in full -- spans Docker release-image gates and
  Keycloak/Nginx verification, none of which this additive-only slice
  touches, matching every prior sibling slice's own precedent.
- The two legacy freeze reasons (`UNKNOWN_NEGATIVE_BALANCE`/
  `USAGE_EXCEEDS_LEDGER`) and the generalized `LATE_FINALIZED_EVENT`
  shape were exercised only via direct-SQL fixtures (this codebase's own
  established convention for these shapes, per XM-INV-ELIG-QUEUE-NARROW's
  own precedent) -- whether production currently has any such rows for the
  "资格冻结" tab's new narrowing toggle to actually hide is not checked
  (no production access for this task).
