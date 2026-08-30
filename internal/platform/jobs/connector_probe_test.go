package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// stubProbeClient is a minimal probeReadClient test double giving direct
// control over Version/Health results.
//
// It is deliberately not built on sub2api.NewFake/newapi.NewFake: those
// fakes only ever return an error from Version/Health on context
// cancellation (see fake.go) -- everything else this worker needs to
// exercise (unsupported version, unhealthy-but-answering, and outright
// Version/Health errors) either isn't reachable through the fakes' option
// surface at all (a hard Version/Health error) or is more directly expressed
// here than by reasoning about FakeOptions indirectly.
type stubProbeClient struct {
	version    connector.VersionInfo
	versionErr error
	health     connector.HealthResult
	healthErr  error
}

func (s stubProbeClient) Version(context.Context) (connector.VersionInfo, error) {
	return s.version, s.versionErr
}

func (s stubProbeClient) Health(context.Context) (connector.HealthResult, error) {
	return s.health, s.healthErr
}

func connectorProbeFactory(client probeReadClient, err error) probeClientFactory {
	return func(context.Context) (probeReadClient, error) {
		if err != nil {
			return nil, err
		}
		return client, nil
	}
}

func healthyProbeClient(version string) probeReadClient {
	return stubProbeClient{
		version: connector.VersionInfo{
			Detected: version, Fingerprint: "fp-" + version, Supported: true, DetectedAt: fixedNow,
		},
		health: connector.HealthResult{Healthy: true, CheckedAt: fixedNow, LatencyMS: 12},
	}
}

func newTestConnectorProbeWorker(store ObservationStore, sub2apiFactory, newapiFactory probeClientFactory) *ConnectorProbeWorker {
	return NewConnectorProbeWorker(ConnectorProbeOptions{
		Environment:      "staging",
		Store:            store,
		Sub2APISource:    "sub2api-test",
		Sub2APINewClient: sub2apiFactory,
		NewAPISource:     "newapi-test",
		NewAPINewClient:  newapiFactory,
		Now:              func() time.Time { return fixedNow },
	})
}

func connectorProbeJob() *river.Job[ConnectorProbeArgs] {
	return &river.Job[ConnectorProbeArgs]{
		JobRow: &rivertype.JobRow{
			ID:          301,
			Kind:        ConnectorProbeJobKind,
			Queue:       QueueMaintenance,
			Attempt:     1,
			MaxAttempts: connectorProbeMaxAttempts,
		},
	}
}

func TestConnectorProbeArgsDeclareRetryQueueAndUniqueness(t *testing.T) {
	args := ConnectorProbeArgs{}
	if got := args.Kind(); got != ConnectorProbeJobKind {
		t.Fatalf("Kind() = %q, want %q", got, ConnectorProbeJobKind)
	}
	opts := args.InsertOpts()
	if opts.Queue != QueueMaintenance {
		t.Fatalf("queue = %q, want %q", opts.Queue, QueueMaintenance)
	}
	if !opts.UniqueOpts.ByArgs || !opts.UniqueOpts.ByQueue {
		t.Fatal("must be unique by args and queue")
	}
	if opts.UniqueOpts.ByPeriod <= 0 {
		t.Fatal("uniqueness period must be positive")
	}
}

func TestConnectorProbeSuccessWritesBothMetrics(t *testing.T) {
	store := newMemoryStore()
	w := newTestConnectorProbeWorker(store,
		connectorProbeFactory(healthyProbeClient("0.1.152"), nil),
		connectorProbeFactory(healthyProbeClient("v1.0.0-rc.25"), nil))

	if err := w.Work(context.Background(), connectorProbeJob()); err != nil {
		t.Fatalf("Work() error = %v", err)
	}

	for _, tc := range []struct {
		key     string
		version string
	}{
		{MetricSub2APIConnectorHealth, "0.1.152"},
		{MetricNewAPIConnectorHealth, "v1.0.0-rc.25"},
	} {
		row := store.byKey(t, tc.key)
		if row.Status != ops.SyncOK {
			t.Fatalf("%s status = %q, want ok", tc.key, row.Status)
		}
		if row.LastErrorCode != "" {
			t.Fatalf("%s last_error_code = %q, want empty", tc.key, row.LastErrorCode)
		}
		if row.StalenessThresholdSeconds != ConnectorProbeStalenessThresholdSeconds {
			t.Fatalf("%s threshold = %d, want %d", tc.key, row.StalenessThresholdSeconds, ConnectorProbeStalenessThresholdSeconds)
		}
		if row.ObservedAt == nil || !row.ObservedAt.Equal(fixedNow) {
			t.Fatalf("%s observed_at = %v, want %v", tc.key, row.ObservedAt, fixedNow)
		}
		if row.LastSuccess == nil || !row.LastSuccess.Equal(fixedNow) {
			t.Fatalf("%s last_success = %v, want %v", tc.key, row.LastSuccess, fixedNow)
		}
		if row.Value["version"] != tc.version {
			t.Fatalf("%s value.version = %v, want %q", tc.key, row.Value["version"], tc.version)
		}
		if row.Value["supported"] != true {
			t.Fatalf("%s value.supported = %v, want true", tc.key, row.Value["supported"])
		}
		if row.Value["healthy"] != true {
			t.Fatalf("%s value.healthy = %v, want true", tc.key, row.Value["healthy"])
		}
		if row.Value["kind"] != "" {
			t.Fatalf("%s value.kind = %v, want empty", tc.key, row.Value["kind"])
		}
		if row.Value["latency_ms"] != int64(12) {
			t.Fatalf("%s value.latency_ms = %v, want 12", tc.key, row.Value["latency_ms"])
		}
		f := row.Freshness(fixedNow)
		if f.State != ops.StateFresh {
			t.Fatalf("%s freshness state = %q, want fresh", tc.key, f.State)
		}
	}
}

// TestConnectorProbeUnhealthyIsStillASuccessfulProbe pins the design's core
// distinction: Health() answering Healthy=false is a legitimate result (the
// connector responded, it just reports unhealthy), not a probe failure.
// Status must stay SyncOK; the unhealthy verdict lives in Value, not in the
// sync-status/error-code fields.
func TestConnectorProbeUnhealthyIsStillASuccessfulProbe(t *testing.T) {
	store := newMemoryStore()
	unhealthy := stubProbeClient{
		version: connector.VersionInfo{Detected: "0.1.152", Fingerprint: "fp", Supported: true, DetectedAt: fixedNow},
		health: connector.HealthResult{
			Healthy: false, CheckedAt: fixedNow, LatencyMS: 30,
			ErrorKind: connector.KindUnavailable,
		},
	}
	w := newTestConnectorProbeWorker(store,
		connectorProbeFactory(unhealthy, nil),
		connectorProbeFactory(healthyProbeClient("v1"), nil))

	if err := w.Work(context.Background(), connectorProbeJob()); err != nil {
		t.Fatalf("Work() error = %v", err)
	}

	row := store.byKey(t, MetricSub2APIConnectorHealth)
	if row.Status != ops.SyncOK {
		t.Fatalf("status = %q, want ok -- an unhealthy-but-answering connector is a successful probe", row.Status)
	}
	if row.Value["healthy"] != false {
		t.Fatalf("value.healthy = %v, want false", row.Value["healthy"])
	}
	if row.Value["kind"] != string(connector.KindUnavailable) {
		t.Fatalf("value.kind = %v, want %q", row.Value["kind"], connector.KindUnavailable)
	}
}

func TestConnectorProbeVersionErrorMarksFailed(t *testing.T) {
	store := newMemoryStore()
	failing := stubProbeClient{versionErr: connector.NewError(connector.KindAuth, "sub2api.version", errors.New("boom"))}
	w := newTestConnectorProbeWorker(store,
		connectorProbeFactory(failing, nil),
		connectorProbeFactory(healthyProbeClient("v1"), nil))

	if err := w.Work(context.Background(), connectorProbeJob()); err != nil {
		t.Fatalf("Work() error = %v", err)
	}

	row := store.byKey(t, MetricSub2APIConnectorHealth)
	if row.Status != ops.SyncFailed {
		t.Fatalf("status = %q, want failed", row.Status)
	}
	if row.LastErrorCode != string(connector.KindAuth) {
		t.Fatalf("last_error_code = %q, want %q", row.LastErrorCode, connector.KindAuth)
	}
	if row.ObservedAt != nil {
		t.Fatalf("observed_at = %v, want nil (never observed before)", row.ObservedAt)
	}
	// newapi must be unaffected by sub2api's failure.
	other := store.byKey(t, MetricNewAPIConnectorHealth)
	if other.Status != ops.SyncOK {
		t.Fatalf("newapi status = %q, want ok -- the two probes must be independent", other.Status)
	}
}

func TestConnectorProbeHealthErrorMarksFailed(t *testing.T) {
	store := newMemoryStore()
	failing := stubProbeClient{
		version:   connector.VersionInfo{Detected: "0.1.152", Supported: true},
		healthErr: connector.NewError(connector.KindUnavailable, "newapi.health", errors.New("timeout")),
	}
	w := newTestConnectorProbeWorker(store,
		connectorProbeFactory(healthyProbeClient("v1"), nil),
		connectorProbeFactory(failing, nil))

	if err := w.Work(context.Background(), connectorProbeJob()); err != nil {
		t.Fatalf("Work() error = %v", err)
	}
	row := store.byKey(t, MetricNewAPIConnectorHealth)
	if row.Status != ops.SyncFailed || row.LastErrorCode != string(connector.KindUnavailable) {
		t.Fatalf("row = %+v", row)
	}
}

// TestConnectorProbeFactoryErrorMarksFailedNotSupported covers both "real
// mode misconfigured" and the production+fake guard baked into the dynamic
// client factories (connector_config.go's NewDynamicSub2APIClientFactory /
// NewDynamicNewAPIClientFactory): both surface identically here as a
// factory error classified KindNotSupported. This is the mechanism by which
// "production+fake is simply never probed" holds -- there is no
// production+fake special case in connector_probe.go itself, only ordinary
// factory-error handling.
func TestConnectorProbeFactoryErrorMarksFailedNotSupported(t *testing.T) {
	store := newMemoryStore()
	factoryErr := connector.NewError(connector.KindNotSupported, "sub2api.client.mode", ErrConnectorProductionFake)
	w := newTestConnectorProbeWorker(store,
		connectorProbeFactory(nil, factoryErr),
		connectorProbeFactory(healthyProbeClient("v1"), nil))

	if err := w.Work(context.Background(), connectorProbeJob()); err != nil {
		t.Fatalf("Work() error = %v", err)
	}
	row := store.byKey(t, MetricSub2APIConnectorHealth)
	if row.Status != ops.SyncFailed {
		t.Fatalf("status = %q, want failed", row.Status)
	}
	if row.LastErrorCode != string(connector.KindNotSupported) {
		t.Fatalf("last_error_code = %q, want %q -- production+fake and real-unconfigured must never silently look healthy",
			row.LastErrorCode, connector.KindNotSupported)
	}
}

// TestConnectorProbeFailurePreservesLastKnownGoodValue mirrors
// Sub2APISyncWorker/NewAPISyncWorker's failureObservation contract: a
// transient failure after a prior success must degrade the dashboard's
// freshness badge (via growing staleness), not blank out the last good
// reading.
func TestConnectorProbeFailurePreservesLastKnownGoodValue(t *testing.T) {
	store := newMemoryStore()
	w := newTestConnectorProbeWorker(store,
		connectorProbeFactory(healthyProbeClient("0.1.152"), nil),
		connectorProbeFactory(healthyProbeClient("v1"), nil))
	if err := w.Work(context.Background(), connectorProbeJob()); err != nil {
		t.Fatal(err)
	}
	good := store.byKey(t, MetricSub2APIConnectorHealth)

	later := fixedNow.Add(20 * time.Minute)
	w2 := NewConnectorProbeWorker(ConnectorProbeOptions{
		Environment:      "staging",
		Store:            store,
		Sub2APISource:    "sub2api-test",
		Sub2APINewClient: connectorProbeFactory(nil, connector.NewError(connector.KindUnavailable, "sub2api.client", errors.New("down"))),
		NewAPISource:     "newapi-test",
		NewAPINewClient:  connectorProbeFactory(healthyProbeClient("v1"), nil),
		Now:              func() time.Time { return later },
	})
	if err := w2.Work(context.Background(), connectorProbeJob()); err != nil {
		t.Fatal(err)
	}

	row := store.byKey(t, MetricSub2APIConnectorHealth)
	if row.Status != ops.SyncFailed {
		t.Fatalf("status = %q, want failed", row.Status)
	}
	if row.ObservedAt == nil || !row.ObservedAt.Equal(good.ObservedAt.UTC()) {
		t.Fatalf("observed_at should be preserved from last success, got %v want %v", row.ObservedAt, good.ObservedAt)
	}
	if row.Value["version"] != "0.1.152" {
		t.Fatalf("value should be preserved from last success, got %v", row.Value)
	}
	f := row.Freshness(later)
	if f.State != ops.StateFailed {
		t.Fatalf("state = %q, want failed (failed outranks stale in priority)", f.State)
	}
}

// TestConnectorProbeWriteFailureIsReturnedButBothAttempted proves the two
// writes are independent: a DB hiccup on one metric must not prevent the
// other from landing, and the overall error must still be returned so River
// retries.
func TestConnectorProbeWriteFailureIsReturnedButBothAttempted(t *testing.T) {
	store := newMemoryStore()
	store.failWriteFor = MetricSub2APIConnectorHealth
	w := newTestConnectorProbeWorker(store,
		connectorProbeFactory(healthyProbeClient("0.1.152"), nil),
		connectorProbeFactory(healthyProbeClient("v1"), nil))

	err := w.Work(context.Background(), connectorProbeJob())
	if err == nil {
		t.Fatal("a store write failure must be returned so River retries")
	}
	if _, ok := store.rows[store.key(MetricNewAPIConnectorHealth, "staging")]; !ok {
		t.Fatal("newapi observation should still be written even though sub2api's write failed")
	}
	if _, ok := store.rows[store.key(MetricSub2APIConnectorHealth, "staging")]; ok {
		t.Fatal("sub2api's failed write should not have landed a row")
	}
}

func TestConnectorProbeHonorsCancellation(t *testing.T) {
	store := newMemoryStore()
	w := newTestConnectorProbeWorker(store,
		connectorProbeFactory(healthyProbeClient("v1"), nil),
		connectorProbeFactory(healthyProbeClient("v1"), nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Work(ctx, connectorProbeJob()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(store.writes) != 0 {
		t.Fatal("a cancelled context should not attempt any writes")
	}
}

func TestConnectorProbeDefaultConfigEnablesProbing(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.ConnectorProbeEnabled {
		t.Fatal("probing should default to enabled -- same reasoning as the sync jobs")
	}
	if cfg.ConnectorProbeInterval != DefaultConnectorProbeInterval {
		t.Fatalf("interval = %s, want %s", cfg.ConnectorProbeInterval, DefaultConnectorProbeInterval)
	}
	if !cfg.ConnectorProbeRunOnStart {
		t.Fatal("run-on-start should default to true so the ops page has data immediately")
	}
}

func TestConnectorProbeConfigNormalizesZeroInterval(t *testing.T) {
	cfg := Config{Environment: "test"}.normalized()
	if cfg.ConnectorProbeInterval != DefaultConnectorProbeInterval {
		t.Fatalf("interval = %s, want %s", cfg.ConnectorProbeInterval, DefaultConnectorProbeInterval)
	}
}

func TestConnectorProbeRejectsSubSecondIntervalAndProductionRunID(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Environment = "test"
	cfg.ConnectorProbeInterval = 500 * time.Millisecond
	if err := cfg.validate(); err == nil {
		t.Fatal("sub-second probe interval should be rejected")
	}

	cfg = DefaultConfig()
	cfg.Environment = "production"
	cfg.ConnectorProbeRunID = "isolated"
	cfg.Sub2APIMode = Sub2APIModeReal
	cfg.NewAPIMode = NewAPIModeReal
	if err := cfg.validate(); err == nil {
		t.Fatal("connector probe run ID must not be settable in production")
	}
}

// TestConnectorProbeFakeInProductionRejectedAtStartupWithoutConnectorConfigs
// exercises the standalone probe guard added in client.go: it is
// independent of the Sub2API/NewAPI sync gates precisely so probing can be
// enabled (or disabled) on its own regardless of whether either sync job is
// running.
func TestConnectorProbeFakeInProductionRejectedAtStartupWithoutConnectorConfigs(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Environment = "production"
	cfg.Sub2APISyncEnabled = false
	cfg.NewAPISyncEnabled = false
	cfg.FinanceCollectEnabled = false
	// ConnectorProbeEnabled stays true (default) and both modes stay fake
	// (default): this is exactly the gap the standalone probe guard closes.
	if err := cfg.normalized().validate(); err == nil {
		t.Fatal("production + fake connector probing (no ConnectorConfigs) must be rejected at startup")
	}

	cfg.ConnectorProbeEnabled = false
	if err := cfg.normalized().validate(); err != nil {
		t.Fatalf("disabling probing should allow startup: %v", err)
	}
}
