package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/buildinfo"
	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// The four metric keys read here are defined and written by
// internal/platform/jobs (heartbeat.go / connector_probe.go / retention.go)
// and registered in internal/platform/ops's whitelist (freshness.go). They
// are repeated as literals rather than imported even though this file now
// does import jobs (for ResolveEffectiveMode -- see modeFor): importing a
// package to reuse a *decision* that must not be duplicated is worth the
// layering cost, importing it to copy four string constants is not -- the
// same tradeoff ops.freshness.go itself makes for
// every connector's metric key constants. ops_overview_test.go cross-checks
// these literals against the jobs package constants so the two cannot drift
// silently.
const (
	metricPlatformHeartbeat      = "platform.heartbeat"
	metricPlatformRetentionLast  = "platform.retention.last_run"
	metricSub2APIConnectorHealth = "sub2api.connector.health"
	metricNewAPIConnectorHealth  = "newapi.connector.health"
)

// ConnectorConfigLister is the read-only capability needed to show each
// sync pipeline's effective mode (*credentials.Store satisfies it). Reuses
// the same store XM-CRED0's GET /connectors/config already reads from,
// rather than opening a second read path to core.connector_config.
type ConnectorConfigLister interface {
	ListConnectorConfigs(ctx context.Context, environment string) ([]credentials.ConnectorConfig, error)
}

// AlertDeliveryStatus reports whether alert notification channels are
// configured -- presence only, never the credential ref or endpoint value
// (constitution clause 7 / ADR-014). Computed once at process startup in
// cmd/platform-api from the same XM_ALERT_* env var names the worker reads;
// see that wiring's own comment for why this is env-based rather than
// DB-backed, and for the operational assumption it depends on (both
// processes deployed from the same environment).
type AlertDeliveryStatus struct {
	TelegramConfigured bool
	WebhookConfigured  bool
}

// OpsOverviewDeps are the dependencies for OpsOverviewHandler.
type OpsOverviewDeps struct {
	// Observations backs every metric-derived section below (worker
	// heartbeat, sync pipelines, connector health, retention); *ops.Store
	// satisfies it. Always required -- unlike RequestLogs/PlatformUsers/
	// Credentials, ops.Store is not an optional subsystem in this codebase.
	Observations MetricLister
	// ConnectorConfigs is nil when the credentials module isn't mounted in
	// this deployment (see Deps.Credentials in router.go) -- the
	// sync-pipeline table then reports config_available=false and omits
	// both effective_mode and effective_mode_source, rather than guessing.
	ConnectorConfigs ConnectorConfigLister
	// DB backs the database.connected probe; reuses the same Pinger and 2s
	// timeout the /readyz handler already uses (health.go), just embedded
	// as a field instead of requiring a second HTTP round trip.
	DB Pinger
	// AlertDelivery is computed once at startup, never per-request (no
	// upstream or config-file read happens in this handler).
	AlertDelivery AlertDeliveryStatus
}

type opsOverviewBuildBody struct {
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	Environment string `json:"environment"`
}

// opsOverviewMetricBody pairs a metric's value with its freshness, same
// discipline as metricItem in metrics.go: there is no field combination that
// returns a value without freshness (规格 §9.1).
type opsOverviewMetricBody struct {
	MetricKey string         `json:"metric_key"`
	Source    string         `json:"source"`
	Value     map[string]any `json:"value"`
	Freshness freshnessBody  `json:"freshness"`
}

type opsSyncPipelineBody struct {
	Kind     string `json:"kind"`
	Platform string `json:"platform"`
	// ConfigAvailable is false only when the credentials module isn't
	// mounted at all (ConnectorConfigs == nil) -- distinct from "mounted,
	// but no row exists for this platform yet", which is a normal
	// unconfigured state. The two are told apart by EffectiveModeSource,
	// not by squashing both into an invented "fake" (see modeFor).
	ConfigAvailable bool `json:"config_available"`
	// EffectiveMode is "fake" / "real" / "" -- empty means this process
	// cannot know (EffectiveModeSource == jobs.ModeSourceUnknown), which is
	// the honest answer when core.connector_config has no row for this
	// platform: what runs then is decided by the *worker* process's
	// XM_SUB2API_MODE / XM_NEWAPI_MODE, and platform-api's container does
	// not carry those keys (it runs no sync). Clients must render the
	// source, never assume a default.
	EffectiveMode string `json:"effective_mode"`
	// EffectiveModeSource is jobs.ModeSourceDatabase or
	// jobs.ModeSourceUnknown here (never "env": this process has no
	// connector env vars of its own to fall back to).
	EffectiveModeSource string        `json:"effective_mode_source"`
	ConfigUpdatedAt     *string       `json:"config_updated_at"`
	SampleMetricKey     string        `json:"sample_metric_key"`
	Source              string        `json:"source"`
	Freshness           freshnessBody `json:"freshness"`
}

type opsAlertDeliveryBody struct {
	TelegramConfigured bool `json:"telegram_configured"`
	WebhookConfigured  bool `json:"webhook_configured"`
}

type opsDatabaseBody struct {
	Connected bool `json:"connected"`
}

type opsOverviewResponse struct {
	Build           opsOverviewBuildBody    `json:"build"`
	WorkerHeartbeat opsOverviewMetricBody   `json:"worker_heartbeat"`
	SyncPipelines   []opsSyncPipelineBody   `json:"sync_pipelines"`
	ConnectorHealth []opsOverviewMetricBody `json:"connector_health"`
	AlertDelivery   opsAlertDeliveryBody    `json:"alert_delivery"`
	Retention       opsOverviewMetricBody   `json:"retention"`
	Database        opsDatabaseBody         `json:"database"`
}

// OpsOverviewHandler serves the "控制平面健康" (control-plane health)
// sub-tab of the 运行保障 (operations assurance) page (XM-OPS0): build
// info, worker liveness, each sync pipeline's effective mode and freshness,
// connector health, alert delivery configuration, retention's last run, and
// database connectivity.
//
// Everything here comes from already-written observations and
// already-loaded configuration -- this handler never calls any upstream
// connector and never reads a live config file on the request path
// (constitution clause 5: the platform does not sit on any request path to
// a third party). Permission (ops.ScopeRead) is enforced by the router's
// RequireScope.
func OpsOverviewHandler(deps OpsOverviewDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}
		env, err := resolveEnvironment(r, p)
		if err != nil {
			WriteError(w, r, err)
			return
		}

		observations, err := deps.Observations.ListByEnvironment(r.Context(), string(env))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		byKey := make(map[string]ops.Observation, len(observations))
		for _, o := range observations {
			byKey[o.MetricKey] = o
		}
		now := time.Now().UTC()

		// metricBody looks up one metric by key. Absence is not an error --
		// it means "never observed", which Observation.Freshness already
		// renders honestly as StateUninitialized; there is no separate
		// "not found" branch to fabricate here (规格 §9.1).
		metricBody := func(metricKey string) opsOverviewMetricBody {
			o, found := byKey[metricKey]
			if !found {
				o = ops.Observation{MetricKey: metricKey}
			}
			f := o.Freshness(now)
			return opsOverviewMetricBody{
				MetricKey: metricKey,
				Source:    o.Source,
				Value:     o.Value,
				Freshness: freshnessBody{
					State:            string(f.State),
					StalenessSeconds: f.StalenessSeconds,
					ThresholdSeconds: f.ThresholdSeconds,
					IsPartial:        f.IsPartial,
					ObservedAt:       rfc3339Ptr(f.ObservedAt),
					LastSuccess:      rfc3339Ptr(f.LastSuccess),
					LastErrorCode:    f.LastErrorCode,
				},
			}
		}

		configAvailable := deps.ConnectorConfigs != nil
		var connectorConfigs []credentials.ConnectorConfig
		if configAvailable {
			connectorConfigs, err = deps.ConnectorConfigs.ListConnectorConfigs(r.Context(), string(env))
			if err != nil {
				WriteError(w, r, err)
				return
			}
		}
		// modeFor goes through the *same* resolver the worker walks every
		// round (jobs.ResolveEffectiveMode) rather than a copy of it.
		//
		// With no row it does **not** answer "fake": what that round
		// actually runs is decided by the platform-worker process's
		// XM_SUB2API_MODE / XM_NEWAPI_MODE, and this container does not
		// have those keys at all (platform-api runs no sync -- confirmed on
		// production 2026-09-08). The old implementation hardcoded "fake"
		// here, which happened to be right only because production's env
		// default also happened to be fake; it was the third of the three
		// disagreeing answers in that incident's report (§三.1). Passing an
		// empty processDefault makes the resolver say "unknown", which is
		// the only thing this process can honestly say.
		modeFor := func(platform string) (jobs.EffectiveConnectorConfig, *string) {
			for _, c := range connectorConfigs {
				if c.Platform == platform {
					stamp := c.UpdatedAt.UTC().Format(time.RFC3339)
					row := &jobs.ConnectorConfig{Platform: c.Platform, Mode: c.Mode}
					return jobs.ResolveEffectiveMode(platform, row, ""), &stamp
				}
			}
			return jobs.ResolveEffectiveMode(platform, nil, ""), nil
		}

		syncPipeline := func(kind, platform, sampleMetricKey string) opsSyncPipelineBody {
			var eff jobs.EffectiveConnectorConfig
			var updatedAt *string
			if configAvailable {
				eff, updatedAt = modeFor(platform)
			}
			sample := metricBody(sampleMetricKey)
			return opsSyncPipelineBody{
				Kind:                kind,
				Platform:            platform,
				ConfigAvailable:     configAvailable,
				EffectiveMode:       eff.Mode,
				EffectiveModeSource: eff.Source,
				ConfigUpdatedAt:     updatedAt,
				SampleMetricKey:     sampleMetricKey,
				Source:              sample.Source,
				Freshness:           sample.Freshness,
			}
		}

		// database.connected reuses ReadyHandler's exact probe (health.go):
		// same Pinger, same 2s timeout. A nil DB (only possible via a
		// misconfigured embedder/test, never in cmd/platform-api's own
		// wiring) fails closed rather than claiming a connection nobody
		// checked.
		dbConnected := false
		if deps.DB != nil {
			pingCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			dbConnected = deps.DB.Ping(pingCtx) == nil
			cancel()
		}

		WriteJSON(w, http.StatusOK, opsOverviewResponse{
			Build: opsOverviewBuildBody{
				Version:     buildinfo.Version,
				Commit:      buildinfo.Commit,
				Environment: string(env),
			},
			WorkerHeartbeat: metricBody(metricPlatformHeartbeat),
			SyncPipelines: []opsSyncPipelineBody{
				syncPipeline("sub2api_sync", "sub2api", "sub2api.channels.status"),
				syncPipeline("newapi_sync", "newapi", "newapi.channels.status"),
			},
			ConnectorHealth: []opsOverviewMetricBody{
				metricBody(metricSub2APIConnectorHealth),
				metricBody(metricNewAPIConnectorHealth),
			},
			AlertDelivery: opsAlertDeliveryBody{
				TelegramConfigured: deps.AlertDelivery.TelegramConfigured,
				WebhookConfigured:  deps.AlertDelivery.WebhookConfigured,
			},
			Retention: metricBody(metricPlatformRetentionLast),
			Database:  opsDatabaseBody{Connected: dbConnected},
		})
	}
}
