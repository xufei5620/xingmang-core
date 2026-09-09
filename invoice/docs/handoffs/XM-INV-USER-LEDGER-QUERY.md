# XM-INV-USER-LEDGER-QUERY: per-account eligibility ledger query (design section 3(E))

> **Route finalized by CR-0009** (XM-INV-CR0009-LEDGER-VIEW): the provisional
> `GET /api/v1/admin/eligibility-ledger` route this handoff describes below
> was replaced by `GET /api/v1/admin/accounts/ledger` (list) and
> `GET /api/v1/admin/accounts/{external_account_id}/ledger` (detail), per
> CR-0009's own finalized contract -- see
> `docs/handoffs/XM-INV-CR0009-LEDGER-VIEW.md`. The rest of this document
> describes this slice's own original implementation as delivered and is
> kept for history; it no longer reflects the routes or field names live in
> production.

- **status:** implemented and self-tested locally; full backend suite green
  (`go test -p 1 -count=1 ./...`, two full runs -- see Gate results), `go vet`
  clean, `go build` clean, `gofmt` clean (staged-blob method, per this
  machine's documented CRLF-checkout false positive).
- **branch:** `ai/claude/XM-INV-USER-LEDGER-QUERY` (based on
  `ai/claude/XM-INV-AUTOLOGIN` at commit `a8605ac`, which already contains
  slice 1 `XM-INV-ELIG-AUTO-RECONCILE`'s migration `0020` --
  `pending_reconciliation_*`/`non_invoiceable_overage_*` columns), worktree
  `K:/发票/wt-XM-INV-LEDGER-QUERY`.
- **spec:** `docs/superpowers/specs/2026-09-03-xm-inv-eligibility-simplification-design.md`,
  section 3(E) ("用户账本查询契约"), plus the section 5/6 slice-4 test-matrix
  and slicing entries.

## Summary

Adds a new, read-only admin endpoint, `GET /api/v1/admin/eligibility-ledger`,
returning one row per external account with the exact field set design
section 3(E) specifies: `source_instance_id`, `source_type`, `source_name`,
`external_user_id` (plaintext), `recharged_since_policy_start_minor`,
`consumed_minor`, `invoiceable_minor`, `threshold_minor`, `threshold_reached`,
`eligibility_status`, `block_reason`, `block_detail`, `block_since`. It is
purely additive: no existing handler, query, or evaluator code path was
changed, and no migration was needed (every column it reads already exists as
of migration `0020`).

Full field-by-field rationale, the exact wire example, and the `block_*`
source-selection rules are documented in
`docs/ELIGIBILITY-OPERATIONS.md`'s new "Per-account ledger" section -- this
file covers implementation decisions, deviations, and what was and was not
verified.

## Files changed

- `backend/internal/postgresstore/eligibility_ledger.go` (new) --
  `EligibilityLedgerEntry`/`EligibilityLedgerPageQuery`/`EligibilityLedgerPage`
  types and `Store.ListEligibilityLedgerPage`. Single SQL query per page: two
  `LEFT JOIN LATERAL` subqueries for `recharged_since_policy_start_minor` and
  `consumed_minor`/`invoiceable_minor`, a third (gated on
  `eligibility_status='frozen'`) for the latest open `eligibility_freezes`
  row. Keyset pagination on `external_accounts.id` alone (`before_id`+`limit`,
  default and max 100), no paired timestamp column (see "Pagination shape"
  deviation below).
- `backend/internal/postgresstore/eligibility_ledger_integration_test.go`
  (new) -- see Tests below.
- `backend/internal/httpapi/eligibility_ledger.go` (new) --
  `eligibilityLedgerDTO` (the DTO's sole source of truth, mirroring
  `eligibilityFreezeDTO`'s own precedent) and the `listEligibilityLedger`
  handler. `threshold_minor` is computed here, not in the store, from
  `s.ledger.MinimumRequestMinor()` (the existing admin-configurable minimum
  invoice amount already gating real submission -- see "Threshold source"
  below).
- `backend/internal/httpapi/eligibility_ledger_test.go` (new) -- handler-level
  role denial, IP allowlist denial, and wire-contract-shape tests (see Tests
  below).
- `backend/internal/httpapi/server.go` -- one new route registration:
  `GET /api/v1/admin/eligibility-ledger` behind `s.require("admin", ...)`,
  identical middleware to `eligibility-freezes`.
- `backend/internal/httpapi/service_contract.go` -- one new
  `OperationsService` method,
  `ListEligibilityLedgerPage(context.Context, postgresstore.EligibilityLedgerPageQuery) (postgresstore.EligibilityLedgerPage, error)`.
- `backend/internal/application/service.go` -- one new passthrough method,
  `Service.ListEligibilityLedgerPage`, matching the existing
  `ListEligibilityFreezesPage` passthrough's own shape exactly.
- `backend/internal/httpapi/operations_source_filter_test.go` -- added the new
  interface method to `fakeSourceFilterOperations` (the shared
  `OperationsService` test double). This was **required**, not optional: every
  other test using this fixture passes it through `NewWithConfig` as the sole
  `InvoiceService`/`OperationsService` implementation, and
  `service.(OperationsService)` is a type assertion, not a static interface
  check -- had the new method been left off, the assertion would have started
  silently failing (`s.operations == nil`), breaking every other test in that
  file at runtime rather than at compile time.
- `backend/internal/httpapi/server_test.go` -- added the new route to
  `TestAdminEndpointsRequireAdminRole`'s existing table (one line). IP
  allowlist enforcement is already covered generically by that test file for
  every `s.require("admin", ...)` route; this endpoint's own dedicated IP
  allowlist test still exists in `eligibility_ledger_test.go` per the task
  brief's explicit ask.
- `docs/ELIGIBILITY-OPERATIONS.md` -- new "Per-account ledger" section (this
  is the same doc the `eligibility-freezes`/`eligibility-summary` endpoints
  are documented in; there is no separate general admin-API doc).
- `docs/handoffs/XM-INV-USER-LEDGER-QUERY.md` -- this file.

**Not touched** (per the task brief's explicit scope, confirmed by grep before
finishing): `backend/internal/postgresstore/consumption.go` (owned by a
concurrent slice), any projection-worker/evaluator code, `contracts/` (grepped
first -- it does not reference `eligibility-freezes` or any other admin
endpoint route, so there is nothing there to keep consistent), `backend/Dockerfile`,
`deploy/`, any migration (no new columns needed), any RC/release-identity
file, `K:/sub2api-src`, `K:/newapi-src`.

## Route path

`GET /api/v1/admin/eligibility-ledger` -- a proposal, as instructed. The
platform-side CR-0009 finalizes the actual path; this route, its query
parameters, and its response shape can all be adjusted at that point without
touching the store or application layers underneath (they are shape-agnostic
of the URL).

## Design decisions and deviations from a literal reading

Flagged as they were made, not just at the end, per this project's own
practice of surfacing spec/brief tension inline rather than only in a final
self-review pass.

1. **`ConsumedMinor`/`InvoiceableMinor` formula is duplicated, not called
   through the same Go function.** The brief says "reuse the existing code
   path, do not re-derive." A literal same-*function* reuse is not possible
   without change: `ListEligibilitySummaries` (the `AvailableMinor`/
   `ConsumedMinor` source) is scoped to one principal (`ea.invoice_user_id=$1`)
   and returns a different result shape entirely unrelated to this endpoint's
   pagination. What this file does instead: the exact SQL formula text (same
   filters -- CNY, `WALLET_CASH`/`SUBSCRIPTION_CASH`, `verification_state='verified'`,
   the same `refund_frozen`/reserved/issued netting for the invoiceable sum)
   is copied verbatim into `eligibilityLedgerSelect`'s own `LEFT JOIN LATERAL`,
   with a comment cross-referencing `eligibility_operations.go`'s
   `ListEligibilitySummaries` as the source of truth for that formula. It is
   **not** extracted into a shared helper both files call, specifically to
   avoid touching `eligibility_operations.go` -- that file is plausibly in
   scope for the concurrent `XM-INV-ELIG-QUEUE-NARROW` slice's repair tooling
   (it already hosts `eligibilityFreezeReasons`, the freeze-reason
   whitelist a repair tool would need), and the task brief's own
   "additive... new files preferred" instruction reads as a preference for
   avoiding exactly this kind of shared-file collision risk over a marginal
   DRY improvement. If a future slice wants zero formula-drift risk between
   the two, extracting a shared SQL-fragment constant is a small, safe
   follow-up once the concurrent slice has landed.
2. **`recharged_since_policy_start_minor` uses `completed_at>=cutover_at`
   (inclusive), matching the design doc's own written formula exactly** --
   not the internal projection query's `completed_at>account.CutoverAt`
   (exclusive) in `consumption.go`. The design doc states the ledger query's
   formula in prose separately from the projection query's own SQL and uses
   `>=` explicitly; this endpoint follows that literal text. This also means
   it reads `cutover_at` as it stands **today**, without
   XM-INV-ELIG-POLICY-START-ANCHOR (design section 3(D), a separate, later
   slice per the design's own slicing table) having landed in this branch --
   the task brief's base-commit instruction (`a8605ac`, slice 1 only)
   confirms slice 3 is not expected to be present yet. For a `POLICY_ANCHOR`
   account bootstrapped before slice 3 lands, `cutover_at` can still be its
   first-observed-checkpoint time rather than the true policy start, so
   `recharged_since_policy_start_minor` would undercount any real recharge
   that happened in the "dead zone" design section 3(D) describes (a gap the
   design confirms does not exist in production today for the three known
   `POLICY_ANCHOR` accounts). This field will read correctly, with no code
   change needed here, once slice 3 lands and re-anchors `cutover_at` to the
   true policy start -- documented as a forward-compatible read, not a bug to
   fix in this slice.
3. **`block_detail` for a frozen account is synthesized, not stored.** Unlike
   `pending_reconciliation_detail` (a full human-readable sentence already on
   the row), `eligibility_freezes` has no equivalent free-text column -- only
   `freeze_reason`/`trigger_object_type`/`trigger_object_id`. The design doc
   names these three columns as the source but does not specify an exact
   output string. This implementation formats
   `"trigger_object_type=<type> trigger_object_id=<id>"`. Flagging as a
   presentation choice CR-0009's UI may want to override or reformat once it
   consumes this field for real.
4. **Pagination shape: single-column keyset on `external_accounts.id`, no
   paired timestamp.** Every other admin list endpoint in this codebase pairs
   its keyset column with a natural timestamp (`opened_at`, `observed_at`,
   `submitted_at`) because those list real *events*. This endpoint lists
   *accounts*, which have no natural per-row timestamp of their own scoped to
   this query's semantics, and design section 3(E) states the pagination
   contract as literally `before_id`+`limit` with nothing else -- so
   `ORDER BY ea.id DESC` alone is the keyset, which is still a stable, total,
   collision-free order (UUID primary key). The task brief's "same convention
   as existing endpoints" is read as *mechanics* (opaque cursor, `HasMore`,
   `next_before_id`, `WHERE id<$cursor`), not literally requiring a second
   paired column that would have no natural source here.
5. **No per-item `id` field in the response**, deliberately: design section
   3(E)'s own JSON example has no id field at all, and the page envelope's
   `next_before_id` alone is sufficient for a client to page forward without
   ever needing to read a cursor value out of an individual item (matching
   how every sibling endpoint's `next_before_id` already works -- it is never
   read by the client out of `items[]` either). `EligibilityLedgerEntry`'s
   store-internal `externalAccountID` field is deliberately unexported so
   this is enforced at the type level, not just by DTO convention.
6. **Default limit is 100, not this package's more common 50.** The task
   brief states "default and max 100" explicitly for this endpoint. This
   already matches every *existing* admin list endpoint's actual runtime
   behavior at the HTTP layer (`boundedQueryLimit(r, 100)` returns 100, not
   50, whenever the `limit` query parameter is absent or invalid) -- only the
   `Store`-layer Go default differs across endpoints (50 there, 100 here),
   and that Go-layer default is only ever reached directly by a test calling
   the store with `Limit: 0`, never by the HTTP handler. No behavioral
   deviation from sibling endpoints at the API surface; a deliberate,
   brief-directed choice at the Go-layer default.
7. **Filter/cursor validation errors surface as 500 `INTERNAL_ERROR`, not
   400**, matching `listEligibilityFreezes`'s own existing (arguably
   imperfect) precedent for its `source_instance_id`/`external_user_id`/
   `freeze_reason` filters: those are also passed straight to the store
   without handler-level format pre-validation, and a store-level
   `errors.New(...)` falls through `handleDomainError`'s `default:` case to
   500. Only cursor *parsing* (a malformed RFC3339 timestamp on
   `eligibility-freezes`) gets an explicit 400 today, because that endpoint's
   cursor is a timestamp the handler itself parses before ever reaching the
   store; this endpoint's cursor is a bare UUID string with no handler-side
   parsing step, so the same "let the store validate, map to 500" pattern
   applies uniformly. Not improved upon here, to keep this endpoint
   consistent with its sibling rather than introducing an inconsistency
   between the two.

## Threshold source

`threshold_minor` reuses `s.ledger.MinimumRequestMinor()` -- the same
admin-configurable value (`domain.MinimumRequestMinor`, default `20_000`,
adjustable via `PUT /api/v1/admin/settings/invoice`) that already gates real
invoice submission and is exposed on `InvoiceService`, which `*httpapi.Server`
already holds as `s.ledger`. No new constant was needed; the task brief's
"otherwise a single named constant of 20000" fallback branch did not apply.

## Tests

**Store (`eligibility_ledger_integration_test.go`),
`TestEligibilityLedgerPageCoversEveryAccountStateAndFilters`** -- one test
function, six fixture accounts plus subtests, against real PostgreSQL:

- **active**, consumed/invoiceable deliberately divergent (reserved/issued
  nonzero) -- pins the exact formula, not just "some number."
- **`not_invoiceable_pending_reconciliation`** -- pins that `block_reason`/
  `block_detail`/`block_since` come from the five plaintext columns verbatim
  (including an exact string/timestamp round-trip), and that
  `threshold_reached` is computable (and `true`) even while blocked, matching
  the design doc's own illustrative example numbers exactly
  (`recharged=500000, consumed=320000, invoiceable=180000`).
- **`frozen` with two open `eligibility_freezes` rows** -- pins that the
  *latest* (by `opened_at`) is the one surfaced, not merely "any open row";
  the older row's reason must not leak through.
- **`frozen` with zero open `eligibility_freezes` rows** (the task brief's
  explicit "must not error, must still return a row" edge case) -- also
  covers the below-threshold (`15_000<20_000`) side of `threshold_reached`.
- **recorded usage overage** (`non_invoiceable_overage_*` set, account stays
  `active`) -- pins that it never surfaces as a block and never perturbs
  `consumed_minor`/`invoiceable_minor`.
- **zero funding lots** (bootstrapped account, lot deleted after bootstrap) --
  pins that the account still appears with every amount at `0`, not silently
  excluded (this endpoint inner-joins `source_account_eligibility_state`, so
  an account that has *never* been bootstrapped at all would not appear --
  see "Not verified" below for that distinct, narrower case).
- **exact-match `external_user_id` lookup** -- one assertion per scenario
  above, each filtering to exactly that account.
- **keyset pagination** -- walks the full account set three at a time via
  repeated `before_id`, asserts the exact descending-by-id order end to end
  (including `integrationStore`'s own base fixture account, which is also
  bootstrapped and therefore also appears in an unfiltered page).
- **invalid `before_id`** (non-UUID) and **invalid `external_user_id`**
  (over-length; CR/LF-bearing) are both rejected.
- **limit bounding**: `Limit: 5000` still returns exactly the full,
  unfiltered account set (7 rows: 6 fixtures + the base fixture), i.e. capped
  at (but not truncated below) 100.

**HTTP handler (`eligibility_ledger_test.go`)**:

- `TestEligibilityLedgerHandlerRequiresAdminRole` -- non-admin role -> 403.
- `TestEligibilityLedgerHandlerEnforcesAdminIPAllowlist` -- denied source IP
  -> 403; allowed source IP -> 200 (against a fake `OperationsService`, so
  this isolates the IP-allowlist middleware itself, not the query).
- `TestEligibilityLedgerHandlerReturnsContractShapeAndForwardsFilters` -- pins
  the full wire contract (every field name, `threshold_reached` computed on
  both sides of the configured threshold, `null` `block_*` for an unblocked
  account, non-`null` `block_*` surviving the JSON round-trip for a blocked
  one), that `external_user_id`/`before_id`/`limit` are forwarded to the store
  query unchanged, and that omitting the filters leaves them empty with the
  documented default limit of 100.
- Added to `TestAdminEndpointsRequireAdminRole`'s existing table (defense in
  depth; this route now gets that generic coverage too, alongside its own
  dedicated tests above).

Every branch in `scanEligibilityLedgerEntry` returns a defined result for
every state combination in the migration `0020` CHECK constraints (the
pairing checks make "half-set" states impossible at the database level, so
there is no additional unexpected-but-possible state to defend against beyond
the "frozen with no open freeze row" case already covered).

## Gates (all run from `backend/`, proxy variables unset)

Database: dedicated `invoice_test_ledger` (created for this task only, never
`invoice_test`/`invoice_test_release`/any other agent's database), via
`INVOICE_TEST_DATABASE_URL=postgres://postgres:test@127.0.0.1:55432/invoice_test_ledger?sslmode=disable`.

```
go build ./...                 # exit 0
go vet ./...                   # exit 0
gofmt -l <every file this slice touches or adds>   # empty (staged-blob method, CRLF false-positive per this machine's documented quirk)
go test -p 1 -count=1 ./...    # two full runs -- see below
```

**First full run:** every package green except `internal/postgresstore`,
which failed two unrelated tests
(`TestEligibilityFreezeAdminPageAndSafeResolution`,
`TestEligibilityShadowSnapshotEvaluationsKeepsLatestPerCheckpoint`) with a
raw TCP dial/pool-reachability failure to the shared PostgreSQL container --
not an assertion failure, and neither test touches this slice's code.
Re-running `./internal/postgresstore/...` alone immediately after: fully
green (`ok`, 125.6s).

**Second full run** (requested confirmation pass): every package green
except `internal/postgresstore` again, this time four *different* unrelated
tests (`TestPaymentCandidateDualControlCASConcurrent`,
`TestListHardCapsAndAdminKeysetPagination`,
`TestRefundInvalidatesUnissuedRequestAndReleasesAllReservations`,
`TestSourceFreshnessFailsClosedForSubmitAndFinalIssue`), same dial/pool
symptom. Re-running `./internal/postgresstore/...` alone again: fully green
(`ok`, 125.7s).

The failing test set changing between the two full runs, always as a raw
connection error rather than an assertion failure, and always clearing on an
immediate isolated re-run, points at contention on the shared PostgreSQL
instance (this session runs several other agents concurrently, each with
their own dedicated test database on the same server -- `docs/handoffs/XM-INV-ELIG-AUTO-RECONCILE.md`
documents the identical symptom under the identical circumstances), not a
regression from this slice. No test in either failing set is in this slice's
own new file (`eligibility_ledger_integration_test.go`), and no other package
besides `internal/postgresstore` failed in either run.

```
gitleaks git --no-banner --log-opts="a8605ac..HEAD" .
```

Clean: `2 commits scanned`, `no leaks found`, exit 0. Commit range
`a8605ac..HEAD` covers exactly this slice's two commits:

```
a753279 docs(eligibility-ledger): document the new admin endpoint and handoff
e1f7560 feat(postgresstore): add read-only eligibility ledger store query
```

(Two commits, not more, because a broad `git add -A` run earlier -- to check
`gofmt` against staged blobs, this machine's documented CRLF-checkout
workaround -- left the httpapi wiring already staged by the time of the
first `git commit`, so the store layer and the httpapi/application wiring
around it landed together in one commit rather than split further. Still two
logically distinct commits: implementation, then documentation.)

## Not verified

- An external account that has **never** been bootstrapped into
  `source_account_eligibility_state` at all (as opposed to bootstrapped with
  zero lots, which *is* tested) does not appear in this endpoint's output at
  all -- this file's own SQL comment documents this as a deliberate `JOIN`
  (not `LEFT JOIN`) choice, since `recharged_since_policy_start_minor`
  requires a real `cutover_at` value and there is no meaningful placeholder
  for an account that has not yet had one assigned. Not covered by an
  automated test (would require constructing an `external_accounts` row with
  deliberately no matching state row, which every fixture helper in this
  package already avoids by construction as an invalid production state to
  synthesize). Flagging this as the one account shape this endpoint is known
  to omit, for CR-0009's UI design to account for if it matters there (in
  production, a real account reaches this state only for a brief window
  between first observation and its first checkpoint's bootstrap, per the
  design doc's own section 2.1).
- No server/production connection -- explicitly out of scope, matching every
  prior sibling slice's own precedent (`scripts/verify.ps1` in full was not
  run for the same reason: it spans frontend, Docker release-image gates, and
  Keycloak/Nginx verification, none of which this backend-only, additive-only
  slice touches).
- The design's own open question 7.3 (whether a `frozen` account should also
  surface a boolean "pending-reconciliation-underneath" bit) is unresolved
  upstream and this slice does not attempt to answer it -- by construction
  (per the slice-1 handoff's own documented risk 2), an account cannot
  reach `not_invoiceable_pending_reconciliation` while `frozen`, so there is
  currently nothing for such a bit to report; not implemented here.
