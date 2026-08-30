// Package cpa defines the read-only contract for CPA — the CLI Proxy API
// stack (container `cli-proxy-api`, image `eceasy/cli-proxy-api`) plus its
// management sidecar cpa-manager-plus (container, 127.0.0.1:18317).
//
// ⚠️ **This contract is DRAFT** (XM-CPA0). Only the `file` backend exists: a
// read-only bind mount of cpa-manager-plus's own SQLite usage database
// (`/root/cpa-stack/cpam-data/usage.sqlite` on the host). There is no `real`
// (HTTP management API) backend yet — nobody has confirmed cpa-manager-plus
// exposes one worth reading from, and CLI Proxy API's own management surface
// is explicitly out of bounds (see below). `off` is the only other mode.
//
// Table shapes were confirmed against a live usage.sqlite by the task brief
// for the columns this package actually reads; a handful of usage_events
// token-count column names were **not** confirmed and are resolved at
// runtime via `PRAGMA table_info` with a documented fallback (see schema.go).
// The unverified list lives in contracts/connectors/cpa.read.v1.md §8, same
// discipline as reqlog's DRAFT contract.
//
// **Never mounted, never read**: /root/cpa-stack/cpa/auths and any
// config.yaml under the CPA stack. Those hold upstream OAuth tokens and raw
// API keys for the providers CLI Proxy API fronts — credentials never enter
// this platform's containers, mounted or otherwise (constitution §7). The
// only thing this connector ever opens is a read-only copy of
// cpa-manager-plus's own usage/telemetry SQLite database, which contains
// hashes and aggregates, not credential material.
//
// Rules shared with every other read-only connector in this repo (ADR-018
// gate 4, sub2api/newapi/reqlog precedent):
//   - ReadClient has only read methods;
//   - every result carries a Snapshot (spec §9.1: no bare numbers);
//   - api_key_hash is opaque and one-way — this package never attempts to
//     recover the key it was derived from, and never mounts the file that
//     could (auths/config.yaml, see above);
//   - money never touches float64 on any path (constitution §13) — see
//     cost.go.
package cpa

import (
	"context"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

const (
	// ConnectorKey is this connector's key in the Registry.
	ConnectorKey = "cpa"
	// ContractVersion is this read contract's version.
	ContractVersion = "1"
)

// Capability keys (registry.ParseCapability requires <system>.<resource>.<action>,
// 3-4 lowercase dot-separated segments; every one of these must parse and
// have IsWrite()==false — enforced by TestReadCapabilitiesAreReadOnly).
const (
	CapabilityServiceVersionRead registry.Capability = "cpa.service.version_read"
	CapabilityHealthRead         registry.Capability = "cpa.health.read"
	CapabilityUsageRead          registry.Capability = "cpa.usage.read"
	CapabilityKeyUsageRead       registry.Capability = "cpa.key_usage.read"
	CapabilityAccountHealthRead  registry.Capability = "cpa.account_health.read"
)

// ReadCapabilities is the full read-only capability list this connector can
// ever advertise. FileClient.Capabilities returns exactly this set — unlike
// reqlog's file backend, every one of these is backed by a real query against
// usage.sqlite, so there is nothing to subtract (see file_client.go).
var ReadCapabilities = []registry.Capability{
	CapabilityServiceVersionRead,
	CapabilityHealthRead,
	CapabilityUsageRead,
	CapabilityKeyUsageRead,
	CapabilityAccountHealthRead,
}

// Metric keys written by internal/platform/jobs.CPASyncWorker into
// internal/platform/ops.  These string literals are duplicated (not
// imported) in ops/freshness.go's registeredMetrics map — ops cannot import
// connector packages (would cycle back through ToObservations), so the two
// copies are kept honest by ops's TestRegisteredMetricsMatchConnectorContracts.
const (
	// MetricRequestsDaily is today's request volume by provider/model.
	MetricRequestsDaily = "cpa.requests.daily"
	// MetricCostDaily is today's cost folded through model_prices, fixed-point.
	MetricCostDaily = "cpa.cost.daily"
	// MetricKeysUsage is a small per-key usage summary (key_count + a capped
	// top-N sample) — the full per-key breakdown lives behind the dedicated
	// `GET /api/v1/platforms/cpa/keys` query, read live rather than from this
	// periodic observation.
	MetricKeysUsage = "cpa.keys.usage"
	// MetricAccountsHealth is the latest codex_inspection_runs/results snapshot.
	MetricAccountsHealth = "cpa.accounts.health"
)

// Staleness thresholds for the four metrics above.  1800s (30 minutes)
// matches the repo-wide default for a 5-minute sync cadence (finance/cost_sync
// precedent): a couple of missed cycles is not yet "stale," several in a row is.
const (
	RequestsStalenessThresholdSeconds int32 = 1800
	CostStalenessThresholdSeconds     int32 = 1800
	KeysStalenessThresholdSeconds     int32 = 1800
	AccountsStalenessThresholdSeconds int32 = 1800
)

// Currency is the assumed denomination of every price in model_prices.
//
// ⚠️ Unverified assumption (contract §8): CLI Proxy API's model_prices table
// has no currency column, and every provider it fronts (Anthropic/OpenAI/etc.
// pricing pages) publishes USD per-token pricing. If a non-USD priced
// provider is ever added upstream, cost math here would silently mix
// currencies — flagged as a follow-up, not something this slice can verify.
const Currency = "USD"

// FileInstance is the file backend's Snapshot.Instance value.
//
// There is no "fake" CPA mode (only off/file — see cmd/platform-worker
// config), so unlike reqlog there is no adjacent demo-data name this could be
// confused with. The name is kept explicit anyway: any future backend must
// pick its own distinct Instance, never reuse this one.
const FileInstance = "cpa-file"

// Snapshot is the freshness envelope every read result carries (spec §9.1).
type Snapshot struct {
	// ObservedAt is when this read happened (UTC) — usage.sqlite has no
	// "as of" watermark of its own beyond the rows themselves, so this is the
	// only honest freshness anchor.
	ObservedAt time.Time
	// Watermark free-form identifies how current the read is; the file
	// backend uses ObservedAt's RFC3339 form (there is no upstream sequence
	// number to report instead).
	Watermark string
	// IsPartial is true when some rows could not be priced/classified and
	// the result is a knowingly incomplete view (see UnpricedRequestCount on
	// UsageSummary/KeyUsageRow) or when the anomaly list in AccountHealth was
	// capped.
	IsPartial bool
	// Instance identifies which CPA backend answered (FileInstance today).
	Instance string
}

// ProviderModelUsage is one (provider, model) row for a business day.
type ProviderModelUsage struct {
	Provider     string
	Model        string
	RequestCount int64
	TokensIn     int64
	TokensOut    int64
	// TokensCacheRead / TokensCacheCreation are kept separate (not folded
	// into one "cache tokens" number): model_prices prices them differently
	// (cache_read_per_1m vs cache_creation_per_1m), and folding them before
	// pricing would make correct cost math impossible to recover.
	TokensCacheRead     int64
	TokensCacheCreation int64
	// CostMicros is this row's cost in micro-USD (scale money.MicroScale),
	// computed only when model_prices has every price tier this row actually
	// used. nil means "unpriced" — never a fabricated 0 (constitution §12).
	CostMicros *int64
}

// UsageSummary is CPA's request/cost summary for one UTC business day.
type UsageSummary struct {
	Snapshot
	// BusinessDay is the UTC calendar day this summary covers, YYYY-MM-DD.
	BusinessDay string
	// Rows is one entry per (provider, model) with at least one request that day.
	Rows []ProviderModelUsage
	// TotalRequestCount sums every row regardless of pricing coverage —
	// request counts never depend on price configuration.
	TotalRequestCount int64
	// TotalCostMicros sums CostMicros over priced rows only. nil iff no row
	// is priced (nothing known, not "everything is free"). When some rows
	// are priced and others are not, this is a documented **partial** sum —
	// UnpricedRequestCount/UnpricedModels say what is missing so a caller
	// never mistakes it for the full total.
	TotalCostMicros *int64
	// Currency is Currency when TotalCostMicros != nil, "" otherwise.
	Currency string
	// UnpricedRequestCount is how many of TotalRequestCount belong to rows
	// with no usable price.
	UnpricedRequestCount int64
	// UnpricedModels lists distinct "provider/model" pairs with no usable
	// price, sorted, deduplicated. Not capped: model catalogs are small
	// (tens, not thousands) by construction of the join.
	UnpricedModels []string
}

// KeyUsageRow is one api_key_hash's usage for a business day.
type KeyUsageRow struct {
	// APIKeyHash is the raw hash from usage_events.api_key_hash — one-way,
	// never reversed, never joined against anything that could reverse it
	// (constitution §7; see package doc's "never mounted" list).
	APIKeyHash string
	// Alias is api_key_aliases's human label for this hash; "" when no alias
	// is registered (a hash with no alias is a normal, expected state, not
	// an error).
	Alias                                                     string
	RequestCount                                              int64
	TokensIn, TokensOut, TokensCacheRead, TokensCacheCreation int64
	// CostMicros sums priced (key, model) usage only; nil iff nothing for
	// this key was priced that day. See UsageSummary.TotalCostMicros for the
	// same "partial sum, not a fabricated total" discipline.
	CostMicros *int64
	// UnpricedRequestCount is how many of this key's RequestCount used a
	// model with no usable price.
	UnpricedRequestCount int64
	// LastUsedAt is the latest usage_events timestamp for this key **within
	// the requested day** — not an all-time last-used value.
	LastUsedAt *time.Time
}

// KeyUsagePage is CPA's per-key usage breakdown for one UTC business day —
// backs both the `cpa.keys.usage` observation (capped) and the
// `GET /api/v1/platforms/cpa/keys` query (uncapped, live).
type KeyUsagePage struct {
	Snapshot
	BusinessDay string
	// Rows is sorted by RequestCount descending (most active key first).
	Rows []KeyUsageRow
	// TotalKeyCount is len(Rows) when the caller asked for every key
	// (httpapi path); when a caller caps the result (the periodic
	// observation), this is still the true total and Truncated says the row
	// list was cut.
	TotalKeyCount int64
	Truncated     bool
}

// AccountAnomaly is one account codex_inspection flagged in its latest run.
type AccountAnomaly struct {
	AccountKey     string
	DisplayAccount string
	Provider       string
	Disabled       bool
	Status         string
	State          string
	Action         string
	ActionReason   string
}

// AccountHealthSummary is the latest codex_inspection_runs/results snapshot —
// CPA's account-level health, not to be confused with a model-routing
// verification system (see contracts/connectors/cpa.read.v1.md §9 for why
// this data lands on the shared "渠道保障" tab slot anyway).
type AccountHealthSummary struct {
	Snapshot
	// RunID identifies which codex_inspection_runs row this summary reflects.
	// "" when no run has ever completed.
	RunID string
	// RunAt is that run's approximate time. The file backend derives it from
	// SQLite rowid ordering (codex_inspection_runs has no confirmed timestamp
	// column — contract §8), so this is best-effort ordering evidence, not a
	// guaranteed wall-clock time; nil when no run exists.
	RunAt *time.Time
	// AccountCount is every account codex_inspection_results reported for
	// this run, priced or not, anomalous or not.
	AccountCount  int64
	DisabledCount int64
	// Anomalies is every flagged account (disabled, or action/action_reason
	// non-empty), capped at MaxAnomalies and sorted disabled-first then by
	// AccountKey for determinism. Truncated says whether AnomalyCount exceeds
	// len(Anomalies).
	Anomalies    []AccountAnomaly
	AnomalyCount int64
	Truncated    bool
}

// MaxAnomalies caps the anomaly list carried in one AccountHealthSummary /
// cpa.accounts.health observation. A list without a cap is a page that can
// grow to thousands of rows and stall rendering (XM-0051 lesson: list cards
// need an explicit cap plus a visible "N more" count, not a silent truncation).
const MaxAnomalies = 50

// ReadClient is CPA's read-only contract. Only read methods — no exceptions
// (ADR-018 gate 4).
type ReadClient interface {
	// Version reports the backend's self-described format version. The file
	// backend has no real upstream version to probe (usage.sqlite carries no
	// schema version marker known to this package), so it reports a stable
	// "file/1" format identifier for its own reading logic, not CPA's version.
	Version(ctx context.Context) (connector.VersionInfo, error)
	// Health reports whether the data source is currently reachable.
	Health(ctx context.Context) (connector.HealthResult, error)
	// Capabilities reports which of ReadCapabilities this instance can
	// currently back with a real query.
	Capabilities(ctx context.Context) ([]registry.Capability, error)
	// UsageSummary aggregates requests/tokens/cost by (provider, model) for
	// one UTC business day (YYYY-MM-DD). day must not be empty — callers
	// decide "today" with their own clock (worker: sync time; httpapi: the
	// request's `day` query param).
	UsageSummary(ctx context.Context, day string) (UsageSummary, error)
	// KeyUsage aggregates requests/tokens/cost by api_key_hash for one UTC
	// business day. Same day contract as UsageSummary.
	KeyUsage(ctx context.Context, day string) (KeyUsagePage, error)
	// AccountHealth reads the latest codex_inspection run's results. Has no
	// day parameter — it always reflects the most recent completed run,
	// whenever that was; RunAt tells the caller how old that is.
	AccountHealth(ctx context.Context) (AccountHealthSummary, error)
}
