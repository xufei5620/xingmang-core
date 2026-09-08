package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// TestOpsOverviewMetricKeyLiteralsMatchJobsPackage pins the four literal
// metric keys in ops_overview.go against the jobs package constants they
// duplicate (see the doc comment at the top of ops_overview.go for why they
// are literals and not an import). If either side changes without the
// other, this test -- not a production 500 -- is where it shows up.
func TestOpsOverviewMetricKeyLiteralsMatchJobsPackage(t *testing.T) {
	cases := map[string]string{
		metricPlatformHeartbeat:      jobs.MetricPlatformHeartbeat,
		metricPlatformRetentionLast:  jobs.MetricRetentionLastRun,
		metricSub2APIConnectorHealth: jobs.MetricSub2APIConnectorHealth,
		metricNewAPIConnectorHealth:  jobs.MetricNewAPIConnectorHealth,
	}
	for literal, want := range cases {
		if literal != want {
			t.Errorf("literal %q does not match jobs package constant %q", literal, want)
		}
	}
}

type fakeConnectorConfigLister struct {
	gotEnv string
	items  []credentials.ConnectorConfig
	err    error
}

func (f *fakeConnectorConfigLister) ListConnectorConfigs(_ context.Context, env string) ([]credentials.ConnectorConfig, error) {
	f.gotEnv = env
	return f.items, f.err
}

func testRouterWithOpsOverview(
	t *testing.T, metrics MetricLister, connectorConfigs ConnectorConfigLister, db Pinger, alertDelivery AlertDeliveryStatus,
) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	if db == nil {
		db = fakePinger{}
	}
	return NewRouter(Deps{
		Logger:              discardLogger(),
		Service:             "platform-api",
		Environment:         "development",
		DB:                  db,
		Resolver:            res,
		Kernel:              &fakeExecutor{},
		ActionRegistry:      action.NewRegistry(),
		Metrics:             metrics,
		OpsConnectorConfigs: connectorConfigs,
		OpsAlertDelivery:    alertDelivery,
	})
}

func opsOverviewGet(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ops/overview", nil)
	devHeaders(req, "ops.read")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// decodedOpsOverview mirrors the wire shape closely enough to assert on
// (kept local to the test so this file exercises the real JSON tags rather
// than the production structs directly).
type decodedOpsOverview struct {
	Build struct {
		Version     string `json:"version"`
		Commit      string `json:"commit"`
		Environment string `json:"environment"`
	} `json:"build"`
	WorkerHeartbeat decodedOpsMetric   `json:"worker_heartbeat"`
	SyncPipelines   []decodedOpsSync   `json:"sync_pipelines"`
	ConnectorHealth []decodedOpsMetric `json:"connector_health"`
	AlertDelivery   struct {
		TelegramConfigured bool `json:"telegram_configured"`
		WebhookConfigured  bool `json:"webhook_configured"`
	} `json:"alert_delivery"`
	Retention decodedOpsMetric `json:"retention"`
	Database  struct {
		Connected bool `json:"connected"`
	} `json:"database"`
}

type decodedFreshness struct {
	State            string  `json:"state"`
	StalenessSeconds *int64  `json:"staleness_seconds"`
	ThresholdSeconds int32   `json:"threshold_seconds"`
	IsPartial        bool    `json:"is_partial"`
	ObservedAt       *string `json:"observed_at"`
	LastSuccess      *string `json:"last_success"`
	LastErrorCode    string  `json:"last_error_code"`
}

type decodedOpsMetric struct {
	MetricKey string           `json:"metric_key"`
	Source    string           `json:"source"`
	Value     map[string]any   `json:"value"`
	Freshness decodedFreshness `json:"freshness"`
}

type decodedOpsSync struct {
	Kind                string           `json:"kind"`
	Platform            string           `json:"platform"`
	ConfigAvailable     bool             `json:"config_available"`
	EffectiveMode       string           `json:"effective_mode"`
	EffectiveModeSource string           `json:"effective_mode_source"`
	ConfigUpdatedAt     *string          `json:"config_updated_at"`
	SampleMetricKey     string           `json:"sample_metric_key"`
	Source              string           `json:"source"`
	Freshness           decodedFreshness `json:"freshness"`
}

func decodeOpsOverview(t *testing.T, rec *httptest.ResponseRecorder) decodedOpsOverview {
	t.Helper()
	var got decodedOpsOverview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unexpected response shape: %v (%s)", err, rec.Body.String())
	}
	return got
}

func TestOpsOverviewRequiresPrincipalAndScope(t *testing.T) {
	h := testRouterWithOpsOverview(t, &fakeMetricLister{}, nil, nil, AlertDeliveryStatus{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/ops/overview", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no principal: status = %d, want 403", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ops/overview", nil)
	devHeaders(req, "audit.read") // any scope other than ops.read
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("wrong scope: status = %d, want 403", rec2.Code)
	}
}

func TestOpsOverviewNeverObservedMetricsAreUninitializedNotFabricated(t *testing.T) {
	// An empty store must not 404 or 500 -- every field renders honestly as
	// "never observed" (规格 §9.1: no bare numbers standing in for real data).
	h := testRouterWithOpsOverview(t, &fakeMetricLister{}, nil, nil, AlertDeliveryStatus{})
	rec := opsOverviewGet(t, h)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	got := decodeOpsOverview(t, rec)

	if got.WorkerHeartbeat.Freshness.State != "uninitialized" {
		t.Fatalf("worker_heartbeat.freshness.state = %q, want uninitialized", got.WorkerHeartbeat.Freshness.State)
	}
	if got.Retention.Freshness.State != "uninitialized" {
		t.Fatalf("retention.freshness.state = %q, want uninitialized", got.Retention.Freshness.State)
	}
	if len(got.ConnectorHealth) != 2 {
		t.Fatalf("connector_health length = %d, want 2", len(got.ConnectorHealth))
	}
	for _, m := range got.ConnectorHealth {
		if m.Freshness.State != "uninitialized" {
			t.Fatalf("%s freshness.state = %q, want uninitialized", m.MetricKey, m.Freshness.State)
		}
	}
	if len(got.SyncPipelines) != 2 {
		t.Fatalf("sync_pipelines length = %d, want 2", len(got.SyncPipelines))
	}
	if got.SyncPipelines[0].Platform != "sub2api" || got.SyncPipelines[1].Platform != "newapi" {
		t.Fatalf("sync_pipelines order/platform = %+v, want [sub2api, newapi]", got.SyncPipelines)
	}
}

func TestOpsOverviewConnectorConfigsNilMeansConfigUnavailable(t *testing.T) {
	h := testRouterWithOpsOverview(t, &fakeMetricLister{}, nil, nil, AlertDeliveryStatus{})
	rec := opsOverviewGet(t, h)
	got := decodeOpsOverview(t, rec)
	for _, p := range got.SyncPipelines {
		if p.ConfigAvailable {
			t.Fatalf("%s config_available = true, want false when ConnectorConfigs is nil", p.Platform)
		}
		if p.EffectiveMode != "" {
			t.Fatalf("%s effective_mode = %q, want empty when config unavailable", p.Platform, p.EffectiveMode)
		}
		if p.EffectiveModeSource != "" {
			t.Fatalf("%s effective_mode_source = %q, want empty when config unavailable", p.Platform, p.EffectiveModeSource)
		}
	}
}

// TestOpsOverviewMissingRowIsUnknownNotFake replaces the old
// "...MissingRowDefaultsToFake": answering "fake" here was the third of the
// three disagreeing answers in the 2026-09-08 alert-storm report (§三.1).
//
// With no row, what actually runs is decided by the platform-worker
// process's XM_SUB2API_MODE / XM_NEWAPI_MODE, and platform-api's container
// carries neither key. The old answer was right only because production's
// env default happened to also be fake -- it stood on a fact from
// somewhere else (memory: 条件恰好为真≠条件正确).
func TestOpsOverviewMissingRowIsUnknownNotFake(t *testing.T) {
	configs := &fakeConnectorConfigLister{items: nil}
	h := testRouterWithOpsOverview(t, &fakeMetricLister{}, configs, nil, AlertDeliveryStatus{})
	rec := opsOverviewGet(t, h)
	got := decodeOpsOverview(t, rec)
	for _, p := range got.SyncPipelines {
		// config_available and "no row for this platform" stay two different
		// facts: the module IS mounted, it just has nothing to say yet.
		if !p.ConfigAvailable {
			t.Fatalf("%s config_available = false, want true when ConnectorConfigs is set", p.Platform)
		}
		if p.EffectiveMode != "" {
			t.Fatalf("%s effective_mode = %q, want empty (this process cannot know)", p.Platform, p.EffectiveMode)
		}
		if p.EffectiveMode == "fake" {
			t.Fatalf("%s effective_mode = fake -- that is the guess this slice removed", p.Platform)
		}
		if p.EffectiveModeSource != jobs.ModeSourceUnknown {
			t.Fatalf("%s effective_mode_source = %q, want %q", p.Platform, p.EffectiveModeSource, jobs.ModeSourceUnknown)
		}
		if p.ConfigUpdatedAt != nil {
			t.Fatalf("%s config_updated_at = %v, want nil (no row exists)", p.Platform, p.ConfigUpdatedAt)
		}
	}
	// Guard the empty-string assertion above against the field simply being
	// deleted, which would make it vacuously true (memory: 缺席型断言要做变异验证).
	if !strings.Contains(rec.Body.String(), `"effective_mode_source"`) {
		t.Fatalf("response has no effective_mode_source field at all: %s", rec.Body.String())
	}
	if configs.gotEnv != "development" {
		t.Fatalf("connector configs queried for env %q, want development", configs.gotEnv)
	}
}

func TestOpsOverviewConnectorConfigsRowReportsRealMode(t *testing.T) {
	updated := time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC)
	configs := &fakeConnectorConfigLister{items: []credentials.ConnectorConfig{
		{Platform: "sub2api", Environment: "development", Mode: "real", UpdatedAt: updated},
	}}
	h := testRouterWithOpsOverview(t, &fakeMetricLister{}, configs, nil, AlertDeliveryStatus{})
	rec := opsOverviewGet(t, h)
	got := decodeOpsOverview(t, rec)

	var sub2api decodedOpsSync
	for _, p := range got.SyncPipelines {
		if p.Platform == "sub2api" {
			sub2api = p
		}
	}
	if sub2api.EffectiveMode != "real" {
		t.Fatalf("sub2api effective_mode = %q, want real", sub2api.EffectiveMode)
	}
	if sub2api.EffectiveModeSource != jobs.ModeSourceDatabase {
		t.Fatalf("sub2api effective_mode_source = %q, want %q", sub2api.EffectiveModeSource, jobs.ModeSourceDatabase)
	}
	if sub2api.ConfigUpdatedAt == nil || *sub2api.ConfigUpdatedAt != updated.Format(time.RFC3339) {
		t.Fatalf("sub2api config_updated_at = %v, want %s", sub2api.ConfigUpdatedAt, updated.Format(time.RFC3339))
	}
}

func TestOpsOverviewReportsConnectorHealthValueAndFreshness(t *testing.T) {
	observedAt := time.Now().UTC().Add(-90 * time.Second)
	lister := &fakeMetricLister{items: []ops.Observation{
		{
			MetricKey: "sub2api.connector.health", Source: "sub2api-real", Environment: "development",
			ObservedAt: &observedAt, SyncedAt: observedAt, Status: ops.SyncOK, LastSuccess: &observedAt,
			StalenessThresholdSeconds: 900,
			Value: map[string]any{
				"version": "0.1.152", "supported": true, "healthy": true,
				"kind": "", "latency_ms": int64(42), "checked_at": observedAt.Format(time.RFC3339),
			},
		},
	}}
	h := testRouterWithOpsOverview(t, lister, nil, nil, AlertDeliveryStatus{})
	rec := opsOverviewGet(t, h)
	got := decodeOpsOverview(t, rec)

	var sub2apiHealth decodedOpsMetric
	for _, m := range got.ConnectorHealth {
		if m.MetricKey == "sub2api.connector.health" {
			sub2apiHealth = m
		}
	}
	if sub2apiHealth.Freshness.State != "fresh" {
		t.Fatalf("state = %q, want fresh", sub2apiHealth.Freshness.State)
	}
	if sub2apiHealth.Freshness.ThresholdSeconds != 900 {
		t.Fatalf("threshold_seconds = %d, want 900", sub2apiHealth.Freshness.ThresholdSeconds)
	}
	if sub2apiHealth.Value["version"] != "0.1.152" || sub2apiHealth.Value["healthy"] != true {
		t.Fatalf("value = %+v", sub2apiHealth.Value)
	}
	// newapi must still be present and uninitialized -- one platform having
	// data must never hide the other from the response.
	found := false
	for _, m := range got.ConnectorHealth {
		if m.MetricKey == "newapi.connector.health" {
			found = true
			if m.Freshness.State != "uninitialized" {
				t.Fatalf("newapi state = %q, want uninitialized", m.Freshness.State)
			}
		}
	}
	if !found {
		t.Fatal("newapi.connector.health missing from connector_health")
	}
}

func TestOpsOverviewAlertDeliveryPassesThroughBooleansOnly(t *testing.T) {
	h := testRouterWithOpsOverview(t, &fakeMetricLister{}, nil, nil, AlertDeliveryStatus{
		TelegramConfigured: true, WebhookConfigured: false,
	})
	rec := opsOverviewGet(t, h)
	body := rec.Body.String()
	got := decodeOpsOverview(t, rec)
	if !got.AlertDelivery.TelegramConfigured || got.AlertDelivery.WebhookConfigured {
		t.Fatalf("alert_delivery = %+v", got.AlertDelivery)
	}
	// Defense in depth: this handler must never carry a credential ref,
	// bot token, or webhook URL shape into the response body.
	for _, forbidden := range []string{"secret://", "bot_ref", "chat_id", "webhook_url"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaks a config value shape (%q): %s", forbidden, body)
		}
	}
}

func TestOpsOverviewDatabaseConnectivityReflectsPinger(t *testing.T) {
	healthy := testRouterWithOpsOverview(t, &fakeMetricLister{}, nil, fakePinger{}, AlertDeliveryStatus{})
	rec := opsOverviewGet(t, healthy)
	got := decodeOpsOverview(t, rec)
	if !got.Database.Connected {
		t.Fatal("database.connected = false, want true when Ping succeeds")
	}

	unhealthy := testRouterWithOpsOverview(t, &fakeMetricLister{}, nil, fakePinger{err: context.DeadlineExceeded}, AlertDeliveryStatus{})
	rec2 := opsOverviewGet(t, unhealthy)
	got2 := decodeOpsOverview(t, rec2)
	if got2.Database.Connected {
		t.Fatal("database.connected = true, want false when Ping fails")
	}
}

func TestOpsOverviewBuildInfoReflectsResolvedEnvironment(t *testing.T) {
	h := testRouterWithOpsOverview(t, &fakeMetricLister{}, nil, nil, AlertDeliveryStatus{})
	rec := opsOverviewGet(t, h)
	got := decodeOpsOverview(t, rec)
	if got.Build.Environment != "development" {
		t.Fatalf("build.environment = %q, want development (the resolved query environment)", got.Build.Environment)
	}
}
