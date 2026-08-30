package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/xufei5620/xingmang-platform/connectors/cpa"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// stubCPAClient is a minimal cpa.ReadClient test double. CPA has no "fake"
// mode in production (see cpa_sync.go's file doc comment), so unlike
// sub2api/newapi there is no connectors/cpa.NewFake to reuse — this stub is
// local to the worker test, the same way a hand-rolled double is used
// wherever a package under test has no shared Fake.
type stubCPAClient struct {
	usage     cpa.UsageSummary
	usageErr  error
	keys      cpa.KeyUsagePage
	keysErr   error
	health    cpa.AccountHealthSummary
	healthErr error
}

func (s stubCPAClient) Version(context.Context) (connector.VersionInfo, error) {
	return connector.VersionInfo{Supported: true, Detected: "stub/1"}, nil
}
func (s stubCPAClient) Health(context.Context) (connector.HealthResult, error) {
	return connector.HealthResult{Healthy: true}, nil
}
func (s stubCPAClient) Capabilities(context.Context) ([]registry.Capability, error) {
	return cpa.ReadCapabilities, nil
}
func (s stubCPAClient) UsageSummary(context.Context, string) (cpa.UsageSummary, error) {
	return s.usage, s.usageErr
}
func (s stubCPAClient) KeyUsage(context.Context, string) (cpa.KeyUsagePage, error) {
	return s.keys, s.keysErr
}
func (s stubCPAClient) AccountHealth(context.Context) (cpa.AccountHealthSummary, error) {
	return s.health, s.healthErr
}

func cpaFactory(client stubCPAClient, factoryErr error) CPAClientFactory {
	return func(context.Context) (cpa.ReadClient, error) {
		if factoryErr != nil {
			return nil, factoryErr
		}
		return client, nil
	}
}

func newTestCPAWorker(store ObservationStore, factory CPAClientFactory, out *bytes.Buffer) *CPASyncWorker {
	logger := slog.New(slog.NewJSONHandler(out, nil))
	return NewCPASyncWorker(CPASyncOptions{
		Logger:      logger,
		Environment: "staging",
		InstanceID:  DefaultCPAInstanceID,
		Mode:        CPAModeFile,
		Store:       store,
		NewClient:   factory,
		Now:         func() time.Time { return fixedNow },
	})
}

func cpaSyncJob() *river.Job[CPASyncArgs] {
	return &river.Job[CPASyncArgs]{
		JobRow: &rivertype.JobRow{
			ID: 501, Kind: CPASyncJobKind, Queue: QueueMaintenance,
			Attempt: 1, MaxAttempts: cpaSyncMaxAttempts,
		},
	}
}

func successfulUsage() cpa.UsageSummary {
	cost := int64(1500)
	return cpa.UsageSummary{
		Snapshot:          cpa.Snapshot{ObservedAt: fixedNow, Watermark: "wm", Instance: cpa.FileInstance},
		BusinessDay:       fixedNow.Format("2006-01-02"),
		TotalRequestCount: 10,
		TotalCostMicros:   &cost,
		Currency:          cpa.Currency,
		Rows:              []cpa.ProviderModelUsage{{Provider: "anthropic", Model: "claude-x", RequestCount: 10, CostMicros: &cost}},
	}
}

func successfulKeys() cpa.KeyUsagePage {
	return cpa.KeyUsagePage{
		Snapshot:      cpa.Snapshot{ObservedAt: fixedNow, Watermark: "wm", Instance: cpa.FileInstance},
		BusinessDay:   fixedNow.Format("2006-01-02"),
		Rows:          []cpa.KeyUsageRow{{APIKeyHash: "hash-a", RequestCount: 10}},
		TotalKeyCount: 1,
	}
}

func successfulHealth() cpa.AccountHealthSummary {
	return cpa.AccountHealthSummary{
		Snapshot:     cpa.Snapshot{ObservedAt: fixedNow, Watermark: "wm", Instance: cpa.FileInstance},
		RunID:        "run-1",
		AccountCount: 3,
	}
}

func TestCPASyncSuccessWritesAllFourMetrics(t *testing.T) {
	store := newMemoryStore()
	var logs bytes.Buffer
	worker := newTestCPAWorker(store, cpaFactory(stubCPAClient{
		usage: successfulUsage(), keys: successfulKeys(), health: successfulHealth(),
	}, nil), &logs)

	if err := worker.Work(context.Background(), cpaSyncJob()); err != nil {
		t.Fatalf("Work = %v, want nil", err)
	}

	got := store.keys()
	want := []string{cpa.MetricAccountsHealth, cpa.MetricCostDaily, cpa.MetricKeysUsage, cpa.MetricRequestsDaily}
	if len(got) != len(want) {
		t.Fatalf("wrote metrics %v, want %v", got, want)
	}
	for _, key := range want {
		row := store.byKey(t, key)
		if row.Status != ops.SyncOK {
			t.Fatalf("%s status = %s, want ok", key, row.Status)
		}
		if row.ObservedAt == nil {
			t.Fatalf("%s ObservedAt is nil, want the fixed observation time", key)
		}
		if row.RollupPolicyVersion != ops.RollupPolicyVersion {
			t.Fatalf("%s RollupPolicyVersion = %d, want %d (annotateRollupMetadata must run)", key, row.RollupPolicyVersion, ops.RollupPolicyVersion)
		}
	}
}

// TestCPASyncPartialFailurePreservesOtherMetrics proves the key adaptation
// this worker makes over sub2api_sync/cost_sync: one read failing (usage)
// must not fail the metrics backed by the other two, independent reads.
func TestCPASyncPartialFailurePreservesOtherMetrics(t *testing.T) {
	store := newMemoryStore()
	var logs bytes.Buffer
	usageErr := connector.NewError(connector.KindUnavailable, "cpa.usage.read", errors.New("boom"))
	worker := newTestCPAWorker(store, cpaFactory(stubCPAClient{
		usageErr: usageErr, keys: successfulKeys(), health: successfulHealth(),
	}, nil), &logs)

	if err := worker.Work(context.Background(), cpaSyncJob()); err != nil {
		t.Fatalf("Work = %v, want nil (a read failure is not a job failure)", err)
	}

	for _, key := range []string{cpa.MetricRequestsDaily, cpa.MetricCostDaily} {
		row := store.byKey(t, key)
		if row.Status != ops.SyncFailed || row.LastErrorCode != string(connector.KindUnavailable) {
			t.Fatalf("%s = %+v, want SyncFailed/unavailable", key, row)
		}
	}
	for _, key := range []string{cpa.MetricKeysUsage, cpa.MetricAccountsHealth} {
		row := store.byKey(t, key)
		if row.Status != ops.SyncOK {
			t.Fatalf("%s status = %s, want ok (independent read must not be affected by usage failure)", key, row.Status)
		}
	}
}

// TestCPASyncFailurePreservesPriorGoodValue proves a failed sync keeps the
// dashboard showing the last known value/observed-at instead of erasing
// history (same discipline as sub2api_sync/cost_sync).
func TestCPASyncFailurePreservesPriorGoodValue(t *testing.T) {
	store := newMemoryStore()
	previousObserved := fixedNow.Add(-time.Hour)
	seed := ops.Observation{
		MetricKey: cpa.MetricRequestsDaily, Source: DefaultCPAInstanceID, Environment: "staging",
		ObservedAt: &previousObserved, SyncedAt: previousObserved, Status: ops.SyncOK,
		LastSuccess: &previousObserved, Watermark: "old-wm",
		StalenessThresholdSeconds: cpa.RequestsStalenessThresholdSeconds,
		Value:                     map[string]any{"total_request_count": int64(42)},
	}
	if _, err := store.UpsertWithSample(context.Background(), seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var logs bytes.Buffer
	usageErr := connector.NewError(connector.KindUnavailable, "cpa.usage.read", errors.New("boom"))
	worker := newTestCPAWorker(store, cpaFactory(stubCPAClient{
		usageErr: usageErr, keys: successfulKeys(), health: successfulHealth(),
	}, nil), &logs)
	if err := worker.Work(context.Background(), cpaSyncJob()); err != nil {
		t.Fatalf("Work = %v, want nil", err)
	}

	row := store.byKey(t, cpa.MetricRequestsDaily)
	if row.Status != ops.SyncFailed {
		t.Fatalf("status = %s, want failed", row.Status)
	}
	if row.ObservedAt == nil || !row.ObservedAt.Equal(previousObserved) {
		t.Fatalf("ObservedAt = %v, want preserved prior value %v", row.ObservedAt, previousObserved)
	}
	if row.Value["total_request_count"] != int64(42) {
		t.Fatalf("Value = %v, want the prior successful value preserved", row.Value)
	}
}

func TestCPASyncClientFactoryFailureFailsAllFourMetrics(t *testing.T) {
	store := newMemoryStore()
	var logs bytes.Buffer
	factoryErr := connector.NewError(connector.KindNotSupported, "cpa.client.factory", errors.New("mode off"))
	worker := newTestCPAWorker(store, cpaFactory(stubCPAClient{}, factoryErr), &logs)

	if err := worker.Work(context.Background(), cpaSyncJob()); err != nil {
		t.Fatalf("Work = %v, want nil", err)
	}
	for _, key := range []string{cpa.MetricRequestsDaily, cpa.MetricCostDaily, cpa.MetricKeysUsage, cpa.MetricAccountsHealth} {
		row := store.byKey(t, key)
		if row.Status != ops.SyncFailed || row.LastErrorCode != string(connector.KindNotSupported) {
			t.Fatalf("%s = %+v, want SyncFailed/not_supported", key, row)
		}
	}
}

func TestCPASyncWriteFailureReturnsErrorForRetry(t *testing.T) {
	store := newMemoryStore()
	store.writeErr = errors.New("db unavailable")
	var logs bytes.Buffer
	worker := newTestCPAWorker(store, cpaFactory(stubCPAClient{
		usage: successfulUsage(), keys: successfulKeys(), health: successfulHealth(),
	}, nil), &logs)

	if err := worker.Work(context.Background(), cpaSyncJob()); err == nil {
		t.Fatal("Work = nil, want an error so River retries a failed write")
	}
}

func TestCPASyncContextCancelledDoesNotRecordFailure(t *testing.T) {
	store := newMemoryStore()
	var logs bytes.Buffer
	worker := newTestCPAWorker(store, cpaFactory(stubCPAClient{
		usage: successfulUsage(), keys: successfulKeys(), health: successfulHealth(),
	}, nil), &logs)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := worker.Work(ctx, cpaSyncJob()); err == nil {
		t.Fatal("Work = nil on a cancelled context, want an error (River treats this as shutdown, not failure)")
	}
	if len(store.writes) != 0 {
		t.Fatalf("writes = %d, want 0 (a cancelled context must not be recorded as a sync failure)", len(store.writes))
	}
}

// TestCPAConfigValidation covers the fail-closed invariants around CPAMode:
// off must never coexist with Enabled=true, and file mode requires a data
// directory — both are checked at Config.validate() so a hand-built Config
// (integration tests, a future embedder) gets the same safety net
// cmd/platform-worker/config.go relies on.
func TestCPAConfigValidation(t *testing.T) {
	base := DefaultConfig()
	base.Environment = "staging"
	base.WorkerClusterID = "platform-staging"
	base.RiverSchema = "river"

	if err := base.validate(); err != nil {
		t.Fatalf("default config (CPA off, disabled) must validate: %v", err)
	}

	t.Run("enabled with mode off is rejected", func(t *testing.T) {
		cfg := base
		cfg.CPASyncEnabled = true
		cfg.CPAMode = CPAModeOff
		cfg.CPADataDir = "/var/lib/xm/cpa"
		if err := cfg.validate(); err == nil {
			t.Fatal("validate() = nil, want error (off must never be enabled)")
		}
	})

	t.Run("enabled with file mode but no data dir is rejected", func(t *testing.T) {
		cfg := base
		cfg.CPASyncEnabled = true
		cfg.CPAMode = CPAModeFile
		cfg.CPADataDir = ""
		if err := cfg.validate(); err == nil {
			t.Fatal("validate() = nil, want error (file mode requires CPADataDir)")
		}
	})

	t.Run("enabled with file mode and data dir is accepted", func(t *testing.T) {
		cfg := base
		cfg.CPASyncEnabled = true
		cfg.CPAMode = CPAModeFile
		cfg.CPADataDir = "/var/lib/xm/cpa"
		cfg.CPASyncInterval = DefaultCPASyncInterval
		if err := cfg.validate(); err != nil {
			t.Fatalf("validate() = %v, want nil", err)
		}
	})

	t.Run("run ID set in production is rejected", func(t *testing.T) {
		cfg := base
		cfg.Environment = "production"
		cfg.CPASyncRunID = "test-only"
		if err := cfg.validate(); err == nil {
			t.Fatal("validate() = nil, want error (RunID must not be set in production)")
		}
	})

	t.Run("unknown mode is rejected", func(t *testing.T) {
		cfg := base
		cfg.CPAMode = "bogus"
		if err := cfg.validate(); err == nil {
			t.Fatal("validate() = nil, want error (unknown CPAMode)")
		}
	})
}

func TestCPASyncWorkerMissingDependenciesReturnError(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	if err := (&CPASyncWorker{logger: logger}).Work(context.Background(), cpaSyncJob()); err == nil {
		t.Fatal("Work with no store = nil, want error")
	}
	store := newMemoryStore()
	if err := (&CPASyncWorker{logger: logger, store: store}).Work(context.Background(), cpaSyncJob()); err == nil {
		t.Fatal("Work with no client factory = nil, want error")
	}
}
