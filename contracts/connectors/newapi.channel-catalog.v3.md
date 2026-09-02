# NewAPI channel catalog v3

XM-CHAN-FIELDS0. Extends the `ChannelStatus` type (v1 read contract, `newapi.read.v1.md`; the
directory-completeness envelope was added in `newapi.channel-directory.v2.md`) with the full
channel-catalog fields the merged 渠道管理 table needs. This is a **channel-catalog-scoped**
version, independent of the package's overall `ContractVersion` (still `"2"` — no `ReadClient`
method changed shape or signature; every new field is additive on `ChannelStatus`, and old fields
are unchanged). v1/v2 docs stay frozen; this is a new file, not an edit to them.

**2026-09-02, XM-CHAN-GROUP0**: added the `group` field (see table below) on top of
XM-CHAN-FIELDS0's original delivery. This file stays a living, additive registry for the
channel-catalog scope — unlike the interface-level v1/v2 docs it extends, it isn't frozen per
slice; a later slice adding one more catalog field extends this same file rather than forking a
v4, exactly because the field is additive on the same `ChannelStatus` type under the same
catalog-scoped version. Every field this addendum did not touch is unchanged from the original
delivery.

Every new field below is traced to a specific `K:/newapi-src` field or endpoint. Fields with no
real upstream backing are documented as always `null`, never approximated — for NewAPI this is
most of the new surface (see table). All fields are individually nullable, including ones the
current real/fake clients always populate, for the same forward-compatibility reason given in the
Sub2API sibling document.

## Source
Identity/state fields come from `GET /api/channel/` (route confirmed `router/channel-router.go:40`
→ `controller.GetAllChannels`, `controller/channel.go:100`; both list and detail (`GET
/api/channel/:id`) serialize the full `Channel` struct's JSON tags except `key`, which is
`Omit`-ted). `today` needs two additional GETs per channel to `/api/log/stat` (see below).

## Field-by-field

| JSON field | Source (file:line) | Semantics |
|---|---|---|
| `id`, `name` | `Channel.Id`/`Name` (`model/channel.go:24,30`) | Unchanged from v1. |
| `kind` | — | **Always `null`.** No reachable field distinguishes an OAuth/subscription-backed channel (e.g. the "ChatGPT Subscription (Codex)" `Type`, see below) from a plain API-key channel. The one real signal — an `OAuthKey` JSON blob (`IDToken`/`AccessToken`/`RefreshToken`/`Expired`/…, `relay/channel/codex/oauth_key.go:9-19`) — lives *inside* the `Key` string column, which both `/api/channel/` and `/api/channel/:id` omit from every response (verified: both call sites use `selectAll=false`). Reachable only via the credential-gated `POST /api/channel/:id/key`, which this read-only connector does not and must not call. |
| `vendor` | Derived from `Channel.Type` (`model/channel.go:25`) via `newapiVendorNames`, a **verbatim copy** of upstream's own `ChannelTypeNames` map (`constant/channel.go:129-187`, the data backing `GetChannelTypeName`) | Not a separate upstream string field — NewAPI only reports a type integer; this is upstream's own name table, copied 1:1 with citation, not an approximation. A `Type` value not yet in the copied table (future upstream addition) yields `vendor: null`, not a guess — see Follow-ups. `Type` itself (the existing v1 field) stays an untouched opaque string passthrough. |
| `capacity` | — | **Always `null`.** No per-channel concurrency-limit field exists anywhere in `Channel`, `dto.ChannelSettings`, or `dto.ChannelOtherSettings` (exhaustively grepped for `ratelimit`/`concurrency`/`max_concurrent` and variants). All real rate-limit machinery upstream is scoped by **user group**, not by channel. |
| `status` | Derived from `Channel.Status` (`model/channel.go:29`), enum `common/constants.go:244-247` | String label for the existing numeric/bool `Status`/`Enabled`: `0→"unknown"`, `1→"enabled"`, `2→"manually_disabled"`, `3→"auto_disabled"`. The pre-existing `Enabled` bool (`Status==1`) is unchanged. |
| `scheduling.enabled` | Reuses `Enabled` (`Status==1`) | No separate field; NewAPI has no enabled/disabled distinct from `Status`. |
| `scheduling.priority` | `Channel.Priority` (`model/channel.go:45`, `*int64`) | **Higher number = more preferred** — opposite convention from Sub2API's account `priority` (lower = higher); not normalized across platforms, see the Sub2API sibling doc's same note. Selection: the in-memory path (`model/channel_cache.go:114`) and the DB-fallback path (`model/ability.go:108-147`, `getPriority` at lines 63-91) both pick the highest-priority tier first, then weighted-random within it using `Channel.Weight` (unchanged v1 concept, not re-exposed here). |
| `today.requests`, `today.success_rate`, `today.cost_minor` | `GET /api/log/stat` (`router/api-router.go:274` → `controller.GetLogsStat`, `controller/log.go:98-123` → `model.SumUsedQuota`, `model/log.go:618`), called twice per channel: `type=2` (consume) and `type=5` (error), for the **business-day** window | `requests`/cost come from the `type=2` call: `rpm` in the response is actually `COUNT(*)` over the queried window (upstream's own field naming is historical, not literally "per minute" — `model/log.go:622`), used as the request count; `quota` is `COALESCE(sum(quota),0)`, converted to minor units via the channel's `quota_per_unit`. `success_rate` = `type=2 count / (type=2 count + type=5 count)`, encoded as ppm (see Numeric encoding below); left `null` when both counts are zero (no meaningful ratio for "no activity today", not a 0% or 100% guess). This window is **not** the same as the existing rolling-24-hour window used for `ErrorRatePPM` — different question, different window, cannot share one call. |
| `today.currency`, `today.scale` | Connector's own configured currency/scale | Same as Sub2API's sibling field. |
| `usage_window` | — | **Always `null`.** No stored window/reset-quota concept anywhere in `Channel`, `OtherInfo`, `Setting`, or `ChannelOtherSettings`. The closest analog is Codex-specific and purely a live passthrough with nothing persisted locally: `GET /api/channel/:id/codex/usage` and two sibling endpoints (`controller/codex_usage.go:20-27`, `service/codex_wham_usage.go:15-152`) proxy OpenAI's own "wham" usage API in real time, decoding the body as opaque `any` — NewAPI defines no typed shape for it and stores nothing. Not read by this connector. |
| `proxy` | `dto.ChannelSettings.Proxy` (`relaykit/dto/channel_settings.go:16`), reached by unmarshaling `Channel.Setting` (`model/channel.go:49`) | **Redacted.** Upstream stores this as a full proxy URL and never strips embedded credentials (`common.ParseProxyURLStrict`/`parseProxyURL`, `common/proxy_url.go:12-77`, validates scheme/host/port but accepts and returns `user:pass@host` verbatim) — a live credential-shaped value. This connector parses the URL and keeps only `scheme://host[:port]`, discarding any userinfo; an empty proxy string or an unparseable value yields `null` rather than passing through something that might be dirty. |
| `rate_multiplier`, `upstream_multiplier` | — | **Always `null`.** NewAPI's pricing ratios (`ModelRatio`/`CompletionRatio`/group ratios, `setting/ratio_setting/`) are keyed by model name or group name, not by channel — no channel-scoped override field exists anywhere in `Channel`, `Setting`, or `OtherSettings` (exhaustively grepped for `ratio`/`multiplier`). |
| `last_used_at` | — | **Always `null`.** No field tracks "channel last served a request" — `TestTime` (`model/channel.go:33`) is a connectivity-*test* timestamp, not a request-serving one, and conflating them would misrepresent an untouched-but-tested channel as recently active. |
| `created_at` | `Channel.CreatedTime` (`model/channel.go:32`, unix seconds) | |
| `expires_at` | — | **Always `null`** at the channel level. The one real expiry concept — Codex OAuth token expiry, `Expired` inside the `OAuthKey` blob (`relay/channel/codex/oauth_key.go:18`) — lives inside the excluded `Key` column, same unreachability as `kind` above. |
| `group` (XM-CHAN-GROUP0) | `Channel.Group` (`model/channel.go:40`, non-pointer `string`, gorm default `'default'`) | Comma-separated group-name list (e.g. `"default,vip"`), the dimension upstream itself uses for group-scoped routing (`model.ApplyChannelGroupFilter`) and group-scoped pricing ratios (`setting/ratio_setting/`). This connector normalizes it the same way upstream's own `Channel.GetGroups()` does (`model/channel.go:296-305`, cited in `parseChannelGroup`'s doc comment) — trim outer commas/whitespace, split on `,`, trim each segment — then additionally drops segments that are empty after trimming (upstream's own helper does not: `"a,,b"` becomes a literal empty-string group entry there) and rejoins with `,`. `null` when the normalized result has no segments left, i.e. this channel has no group configured — not an empty string. Sub2API has no analogous concept anywhere in its account model; its `catalogFields()` never writes a `group` key, so httpapi decodes `null` for every Sub2API row through the same "dimension absent from the observation" path every other platform-specific-null field already uses (see `sub2api.channel-catalog.v3.md`'s own per-field table for what that connector does emit) — this file does not need to say anything on Sub2API's behalf, and `sub2api.channel-catalog.v3.md` does not gain a `group` row: it emits no such field to document. |

## Numeric encoding

Same convention as the Sub2API sibling doc: `success_rate` is computed internally as a ppm integer
(reusing this package's existing `ratePPM` helper, the same one behind `ErrorRatePPM`) and
converted to a plain JSON fraction only at the final serialization step (`ppmToFraction`). This
package's `contracttest` suite mechanically enforces "no float fields" on the connector's contract
types via reflection (`testErrorRateIsIntegerPPM`/`assertNoFloatFields`); this slice also fixed a
gap in that check — it previously inspected `reflect.Kind()` directly and so missed
pointer-wrapped float fields (`*float64`), which would have slipped past undetected. The check now
dereferences pointers before the kind switch.

`today.cost_minor` is a decimal string, not a bare JSON number — same reasoning as the Sub2API
sibling doc.

## Today-stats budget

Budgeted like the existing error-rate probe (`maxTodayStatsChannels = 40`, same value as
`maxErrorRateChannels`, same reasoning: two GETs per channel is real request volume against
`/api/log/*`), enabled channels prioritized first (reuses `errorRatePriority`). Channels beyond
budget get `today: null` and the directory is marked `coverage_partial`. Unlike Sub2API, NewAPI's
`/api/log/stat` **is** a `GET` endpoint, so no read-only-transport obstacle applies here — the
budget exists purely for request-volume/latency reasons, not a protocol constraint.

If `quota_per_unit` (fetched once via `/api/status`, needed to convert `today.cost_minor`) is
itself unreachable, `today` degrades to `null` across all channels and the read is marked partial
— it does **not** fail the whole directory read, matching this file's pre-existing design choice
to keep channel identity/status independent of `/api/status` availability (`fetchChannels`'s own
comment: a `/api/status` outage should not also take down channel status).

## Follow-ups / known limitations

- **`vendor`**: the copied `newapiVendorNames` table will silently return `null` for any `Type`
  value NewAPI adds after this slice, until someone updates the copy — a real, known maintenance
  cost of not importing upstream's own table (not possible; separate Go module).
- **Today-stats budget** (40 channels/read): large fleets will see `today: null` on most rows,
  same caveat as the Sub2API sibling doc.
- **`group`** (XM-CHAN-GROUP0): upstream's own gorm default is `'default'`, so in practice most
  real channels are expected to carry at least that one group — a `null` value on a real instance
  most likely means the channel row predates that default (migrated data) or was explicitly
  cleared, not a connector gap. `group` is a routing/pricing dimension, not a health or identity
  one; this connector does not interpret group membership (e.g. it does not derive `vendor` or
  `kind` from it) and callers should not either.
- Not verified against a real NewAPI instance — same disclaimer as `newapi.read.v1.md`.
