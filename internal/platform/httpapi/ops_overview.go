package httpapi

import (
	"context"
	"fmt"
	"log/slog"
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
	// Jobs backs failed_jobs_by_kind; *jobs.QueryStore satisfies it.
	//
	// nil 时那一段回 null（**不是空数组**）——与 ConfigAvailable 同一条
	// 「不假装有数据」的纪律：空数组读作「查过了，一条失败都没有」，
	// 而 nil 读作「这个部署没接这份数据源」，两者不能混。
	// 「没接」与「接了但这次查库失败」也不能混，见 failed_jobs_status。
	Jobs JobsFailureSummarizer
	// Logger 用于把「这一格读不到」写进日志。nil 时回落到 slog.Default()。
	//
	// 加它是因为这个 handler 此前**一行日志都没有**：failed_jobs 的查询错误
	// 被 `if err == nil` 静默吞掉，于是「运行保障页那一格永久瞎着」在生产里
	// 是绝对不可见的。一个不留痕的降级就是下一个「安静地给你一个旧答案」
	// ——那正是本片立项要治的病。
	Logger *slog.Logger
}

// failed_jobs_status 的三个取值。
//
// 这一格此前用同一个 JSON 值 null 表达两种完全不同的状态：「这个部署没接
// jobs 数据源」和「接了，但这次查库失败了」。前端按 handoff 的口径会把 null
// 渲染成「本部署未接入」——一个良性的永久状态——而真相可能是运行保障页正
// 瞎着，且日志里一个字都没有。旁边的 database 那格是正确做法的对照：它
// fail-closed 到 connected:false，字段本身能说出发生了什么。
const (
	opsFailedJobsOK          = "ok"
	opsFailedJobsNotWired    = "not_wired"
	opsFailedJobsQueryFailed = "query_failed"
)

// JobsFailureSummarizer 是失败作业摘要的只读能力（*jobs.QueryStore 满足）。
type JobsFailureSummarizer interface {
	FailedRunSummaryByKind(ctx context.Context, environment string, since time.Time) ([]jobs.FailedRunSummary, error)
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

// opsFailedJobErrorBody 是最近一次失败尝试的错误投影。
//
// truncated / original_length 一起给：一条被截断的错误如果不说自己被截断了，
// 读的人会以为上游就说了这么多。
type opsFailedJobErrorBody struct {
	At             string `json:"at"`
	Message        string `json:"message"`
	Truncated      bool   `json:"truncated"`
	OriginalLength int    `json:"original_length"`
}

// opsFailedJobKindBody 是某一类后台作业在窗口内的失败摘要。
//
// 它存在的理由是 2026-09-08 现场那一格：card_sync 24 小时内 288 条
// discarded，「我的待处理」只取最新 20 条、不合并同类，于是真正要人处理的
// 东西被挤出首屏。按 kind 合并之后那 288 条是一行。
type opsFailedJobKindBody struct {
	Kind    string `json:"kind"`
	Count   int64  `json:"count"`
	FirstAt string `json:"first_at"`
	LastAt  string `json:"last_at"`
	// LastRunID 让前端能跳到 /jobs/runs 定位那一条。
	LastRunID int64 `json:"last_run_id"`
	// ErrorCount 是最近那条作业的尝试次数（不是本类的失败总数，那是 Count）。
	ErrorCount int                    `json:"error_count"`
	LastError  *opsFailedJobErrorBody `json:"last_error"`
}

type opsOverviewResponse struct {
	Build           opsOverviewBuildBody    `json:"build"`
	WorkerHeartbeat opsOverviewMetricBody   `json:"worker_heartbeat"`
	SyncPipelines   []opsSyncPipelineBody   `json:"sync_pipelines"`
	ConnectorHealth []opsOverviewMetricBody `json:"connector_health"`
	AlertDelivery   opsAlertDeliveryBody    `json:"alert_delivery"`
	Retention       opsOverviewMetricBody   `json:"retention"`
	Database        opsDatabaseBody         `json:"database"`
	// FailedJobsByKind 为 null 表示这一段没有数据（为什么没有由
	// FailedJobsStatus 说）；空数组表示窗口内确实没有失败作业。
	// 两者不同，前端要分开渲染。
	FailedJobsByKind []opsFailedJobKindBody `json:"failed_jobs_by_kind"`
	// FailedJobsStatus 是上一段的三态：ok / not_wired / query_failed。
	//
	// 有了它，null 才有确定含义。前端应当 switch 这个字段而不是判 null——
	// query_failed 是一个要人去看的状态（运行保障页这一格正瞎着），
	// not_wired 是一个良性的部署事实。
	FailedJobsStatus string `json:"failed_jobs_status"`
	// FailedJobsWindowHours 是上一段的回看窗口，让前端能把「288 次」说成
	// 「24 小时内 288 次」而不是一个没有量纲的数。
	FailedJobsWindowHours int `json:"failed_jobs_window_hours"`
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

		// 失败作业摘要：读库失败**不让整个 overview 失败**，与 database
		// 那一格同一条纪律——这个端点是「控制平面健康」，它自己不该因为
		// 其中一格读不到就整页 500。
		//
		// 但降级必须留痕、而且必须与「没接数据源」分得开：两者都回 null，
		// 靠 failed_jobs_status 区分，读库失败还会写一条 Warn。日志里的
		// err_kind 只写错误类型，不写 err.Error()——那可能带库连接串。
		var failedJobs []opsFailedJobKindBody
		failedJobsStatus := opsFailedJobsNotWired
		if deps.Jobs != nil {
			summaries, err := deps.Jobs.FailedRunSummaryByKind(
				r.Context(), string(env), time.Now().UTC().Add(-jobs.FailedRunSummaryWindow))
			switch {
			case err != nil:
				failedJobsStatus = opsFailedJobsQueryFailed
				logger := deps.Logger
				if logger == nil {
					logger = slog.Default()
				}
				logger.WarnContext(r.Context(), "ops_overview_failed_jobs_unavailable",
					slog.String("module", "platform.httpapi"),
					slog.String("environment", string(env)),
					slog.String("err_kind", fmt.Sprintf("%T", err)),
				)
			default:
				failedJobsStatus = opsFailedJobsOK
				failedJobs = make([]opsFailedJobKindBody, 0, len(summaries))
				for _, s := range summaries {
					item := opsFailedJobKindBody{
						Kind:       s.Kind,
						Count:      s.Count,
						FirstAt:    s.FirstAt.UTC().Format(time.RFC3339),
						LastAt:     s.LastAt.UTC().Format(time.RFC3339),
						LastRunID:  s.LastRunID,
						ErrorCount: s.ErrorCount,
					}
					if s.LastError != nil {
						item.LastError = &opsFailedJobErrorBody{
							At:             s.LastError.At.UTC().Format(time.RFC3339),
							Message:        s.LastError.Message,
							Truncated:      s.LastError.Truncated,
							OriginalLength: s.LastError.OriginalLength,
						}
					}
					failedJobs = append(failedJobs, item)
				}
			}
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
			Retention:             metricBody(metricPlatformRetentionLast),
			Database:              opsDatabaseBody{Connected: dbConnected},
			FailedJobsByKind:      failedJobs,
			FailedJobsStatus:      failedJobsStatus,
			FailedJobsWindowHours: int(jobs.FailedRunSummaryWindow / time.Hour),
		})
	}
}
