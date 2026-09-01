# Sub2API channel catalog v3

XM-CHAN-FIELDS0. Extends the `ManagedChannel`/`ManagedChannelDirectory` types added in v2
(`sub2api.read.v2.md`) with the full account-catalog fields the merged 渠道管理 table needs.
This is a **channel-catalog-scoped** version, independent of the package's overall read-contract
`ContractVersion` (still `"2"` — no `ReadClient` method changed shape or signature; every new
field is additive on `ManagedChannel`, and old fields are unchanged). v1/v2 docs stay frozen;
this is a new file, not an edit to them.

Every new field below is traced to a specific `K:/sub2api-src` field or endpoint. A field with no
real upstream backing is documented as always `null`, never approximated. All are individually
nullable — treat every one as optional even where the current real/fake clients always populate
it, since an older observation written before this slice deployed will lack the key entirely.

## Source
All fields come from a single list call to `GET /api/v1/admin/accounts` (route confirmed at
`K:/sub2api-src/backend/internal/server/routes/admin.go:360`, handler `List` at
`backend/internal/handler/admin/account_handler.go:500`), whose JSON shape is
`AccountWithConcurrency` (same file, lines 191-200: embeds `*dto.Account` flattened with
`current_concurrency`, `current_window_cost`, `active_sessions`, `current_rpm`) — **except**
`today`, which needs one additional GET per account to
`/api/v1/admin/accounts/:id/today-stats` (see below).

## Field-by-field

| JSON field | Source (file:line) | Semantics |
|---|---|---|
| `id` | `dto.Account.ID` (`handler/dto/types.go:197`) | Unchanged from v1/v2 (`ManagedChannel.ChannelID`). |
| `name` | `dto.Account.Name` (`types.go:198`) | Unchanged. |
| `kind` | Derived from `dto.Account.Type` (`types.go:201`), enum `domain/constants.go:52-57` | `oauth`/`setup-token` → `"subscription"`; `apikey`/`upstream`/`bedrock`/`service_account` → `"upstream"`. This mapping is the connector's own classification (see `classifyAccountKind` in `connectors/sub2api/channel_directory.go`), not a literal upstream field — a future 7th `Type` value not in either bucket yields `null` rather than a guess. |
| `vendor` | `dto.Account.Platform` (`types.go:200`), enum `domain/constants.go:21-30` + legacy `kiro` (`service/domain_constants.go:53`) | Passed through verbatim: `anthropic`, `openai`, `gemini`, `antigravity`, `grok`, `kimi`, `zhipu`, `deepseek`, `composite`, or the legacy `kiro`. Not translated to any other vocabulary. |
| `capacity.used` | `AccountWithConcurrency.CurrentConcurrency` (`account_handler.go:193`) | Live concurrent-request count, same response as the list. |
| `capacity.limit` | `dto.Account.Concurrency` (`types.go:211`) with `LoadFactor` (`types.go:212`) override when set and `>0` | Mirrors upstream's own `EffectiveLoadFactor()` (`service/account.go:168-179`). |
| `status` | `dto.Account.Status` (`types.go:215`) | Unchanged from v1/v2. Values seen in practice: `active`, `disabled`, `error` (`domain/constants.go:5-7`); `expired` is defined but never assigned to an Account (verified by exhaustive grep of every `Account.Status =`/`==` site). |
| `scheduling.enabled` | `dto.Account.Schedulable` (`types.go:223`) | Coarse on/off switch. |
| `scheduling.priority` | `dto.Account.Priority` (`types.go:213`) | Account-level priority. **Lower number = higher priority** (ent schema comment, `account.go:107-108`) — opposite direction from NewAPI's channel `priority` (higher = more preferred). Not normalized across platforms; each platform's own convention is passed through as-is. |
| `today.requests` | `WindowStats.Requests` (`service/account_usage_service.go:140`), from `GET /api/v1/admin/accounts/:id/today-stats` | Real DB aggregation over `usage_logs`, not mocked (confirmed against the connector's own list of hard-coded-mock endpoints, `upstream.go:45-54` — `accounts/:id/today-stats` is not on it). |
| `today.success_rate` | — | **Always `null`.** `WindowStats`/`AccountUsageStatsResponse` never carry a success/error count at account level anywhere in `K:/sub2api-src` (exhaustively searched by field name and by every struct that embeds cost/request data). |
| `today.cost_minor` | `WindowStats.Cost` (`account_usage_service.go:142`) | **Account-dimension cost** — already includes this account's own `rate_multiplier` (field doc comment, `account_usage_service.go:135-138`: `cost: 账号口径费用(total_cost*account_rate_multiplier)`). Deliberately **not** `standard_cost` (base cost, no multiplier — cross-account comparison metric) or `user_cost` (revenue-side, billed to end user). |
| `today.currency`, `today.scale` | Connector's own configured currency/scale (`WithCurrency` option, default USD/2) | Not an upstream field; the connector's own accounting currency, same as every other amount in this contract. |
| `usage_window.used_ratio` | `AccountWithConcurrency.CurrentWindowCost` (`account_handler.go:197`) ÷ `dto.Account.WindowCostLimit` (`types.go:238`, from `extra["window_cost_limit"]`) | Only computed when `WindowCostLimit` is configured and `>0`; only for `kind == "subscription"` accounts. **This is a cost-cap utilization ratio, not the vendor's own quota/session utilization number.** The Anthropic-native 5-hour-window percentage exists (`UsageProgress.Utilization`, `account_usage_service.go:149`) but only via a **separate, per-account, live-proxied** call to `GET /api/v1/admin/accounts/:id/usage`, which itself calls Anthropic's real `/api/oauth/usage` endpoint. This slice does not fan out an unbounded per-account live vendor call on every directory read; see Follow-ups. |
| `usage_window.resets_at` | `dto.Account.SessionWindowEnd` (`types.go:233`) | The locally-tracked session window's end time, whenever non-nil — not gated on the exact `SessionWindowStatus` string value. |
| `proxy` | `dto.Proxy.Name` (`types.go:328`), reached via `dto.Account.Proxy` (`types.go:309`) | Human-assigned display label. **Never** `Host`/`Username`/`Password` — the ent schema's own comment says `Name` contains no credential content by construction, and the DTO mapper (`mappers.go:467-482`) never even copies `Password` into the response (also tagged `json:"-"` defensively, `types.go:333`). |
| `rate_multiplier` | `dto.Account.RateMultiplier` (`types.go:214`, ent column `decimal(10,4)`, `account.go:110-114`) | Local/platform billing multiplier for this account. May be a manually-set value, or an auto-mirrored copy of `upstream_multiplier` when the account has rate-sync enabled (see next row). |
| `upstream_multiplier` | `accounts.extra["upstream_billing_probe"].data.resolved_rate_multiplier` (`service/upstream_billing_probe.go:104-146`) | The multiplier the upstream vendor's own billing-probe endpoint reported for this account, independent of whether rate-sync is enabled. **Only present for accounts that have the "upstream billing probe" feature active** — most accounts will have `null` here, which is the correct answer, not a gap. Precision note: this value is read out of `extra`, a generic `map[string]any` after JSON decode, so it is a native `float64` at that point — this is `extra`'s own precision ceiling (documented by upstream itself as a "sanitized" catch-all field), not something this connector's decode introduces. |
| `last_used_at` | `dto.Account.LastUsedAt` (`types.go:217`) | |
| `created_at` | `dto.Account.CreatedAt` (`types.go:220`) | |
| `expires_at` | `dto.Account.ExpiresAt` (`types.go:218`) | Upstream wire type is `*int64` **unix seconds**, unlike `last_used_at`/`created_at` which are RFC3339 — converted to a timestamp here for consistency with the rest of this contract. Covers OAuth/manual/subscription-period expiry uniformly; upstream has no separate per-scenario expiry field. |

## Numeric encoding

`success_rate`, `used_ratio`, `rate_multiplier`, `upstream_multiplier` are all plain JSON numbers
(fractions/multipliers, e.g. `0.42`, `1.5`), computed internally as **ppm (parts-per-million)
integers** — the same convention as `ErrorRatePPM` elsewhere in this codebase (constitution §13:
ratios use Decimal, never float) — and converted to a JSON number only at the very last,
non-accumulating step before serialization (`ppmToFraction`). `used_ratio` may legitimately exceed
`1.0`: a cost overrun past `window_cost_limit` is a real, expected state upstream (that is what
the field exists to detect), not an error.

`today.cost_minor` is a **decimal string**, not a bare JSON number — same convention as
`amount.minor_units` elsewhere in this API (XM-PAY0), because JS's `Number` is a float64 and
silently loses precision for large integers.

## Today-stats: why per-account GET, not the upstream batch endpoint

Upstream has a real batch endpoint, `POST /api/v1/admin/accounts/today-stats/batch`
(`account_handler.go:2457-2501`, route `admin.go:394`), purpose-built for exactly this case. This
connector **cannot use it**: the read-only transport
(`internal/platform/connector.ReadOnlyTransport`) only permits `GET`/`HEAD` at the HTTP-method
level (ADR-018 gate 2/4) — any `POST`, including a semantically-read-only one, is rejected before
the request is even sent. `today` is therefore fetched via the per-account `GET
/api/v1/admin/accounts/:id/today-stats`, budgeted at `maxTodayStatsAccounts = 40` per directory
read (see `connectors/sub2api/upstream.go`), prioritizing schedulable accounts first. Accounts
beyond the budget get `today: null` and the directory is marked `coverage_partial`. This is a
real, load-bearing limitation for large account fleets — see Follow-ups.

## Follow-ups / known limitations

- **Today-stats budget** (40 accounts/read) means large fleets will see `today: null` on most
  rows. If real account counts turn out to exceed this significantly, consider either lifting the
  read-only transport's method allowlist for this one verified-safe batch route (requires an
  ADR-018 review, out of scope for this slice) or a smarter windowed/cached approach.
- **`usage_window.used_ratio`** is a cost-cap ratio, not Anthropic's native 5-hour utilization
  percentage. If the product wants the vendor-native number, it requires a budgeted per-account
  live call to `/api/v1/admin/accounts/:id/usage` (itself proxying Anthropic's real API) — a
  separate, deliberate design decision this slice did not make, to avoid compounding two
  unbounded-fan-out features in one pass.
- **`upstream_multiplier`** will be `null` for the large majority of accounts (only populated when
  the upstream billing-probe feature is active for that account) — expected, not a bug.
- None of this has been verified against a real Sub2API instance; only against the local source
  and httptest fixtures built from it (same disclaimer as `sub2api.read.v1.md`/`v2.md`).
