package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func readJobManifestContract(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "contracts", "jobs", "cluster-jobs.v1.json"))
	if err != nil {
		t.Fatalf("read job manifest contract: %v", err)
	}
	return raw
}

func TestManifestCoversExactlySevenRegisteredPeriodicJobs(t *testing.T) {
	manifest, hash, err := LoadJobManifest(readJobManifestContract(t))
	if err != nil {
		t.Fatalf("load frozen manifest: %v", err)
	}
	if hash == "" {
		t.Fatal("manifest hash must not be empty")
	}
	registered := RegisteredPeriodicJobSpecs()
	if len(registered) != 7 {
		t.Fatalf("registered periodic jobs = %d, want 7", len(registered))
	}
	if len(manifest.Jobs) != len(registered) {
		t.Fatalf("manifest jobs = %d, registered = %d", len(manifest.Jobs), len(registered))
	}
	for _, spec := range registered {
		found := false
		for _, row := range manifest.Jobs {
			if row.ID == spec.ID {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("manifest does not cover registered job %q", spec.ID)
		}
	}
}

func TestManifestPinsStableIDsKindsQueuesScheduleSourcesAndCatchup(t *testing.T) {
	manifest, _, err := LoadJobManifest(readJobManifestContract(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]JobSpec{
		HeartbeatJobKind: {
			ID: HeartbeatJobKind, Kind: HeartbeatJobKind, Queue: QueueMaintenance,
			OwnerProcess: "platform-worker", Ownership: OwnershipClusterSingleton,
			ScheduleConfig: "HEARTBEAT_INTERVAL", RunOnStartSource: "jobs.DefaultConfig.HeartbeatRunOnStart",
			CatchUp: "at_most_one_immediate",
		},
		Sub2APISyncJobKind: {
			ID: Sub2APISyncJobKind, Kind: Sub2APISyncJobKind, Queue: QueueMaintenance,
			OwnerProcess: "platform-worker", Ownership: OwnershipClusterSingleton,
			ScheduleConfig: "XM_SUB2API_SYNC_INTERVAL", RunOnStartSource: "jobs.DefaultConfig.Sub2APISyncRunOnStart",
			CatchUp: "at_most_one_immediate",
		},
		NewAPISyncJobKind: {
			ID: NewAPISyncJobKind, Kind: NewAPISyncJobKind, Queue: QueueMaintenance,
			OwnerProcess: "platform-worker", Ownership: OwnershipClusterSingleton,
			ScheduleConfig: "XM_NEWAPI_SYNC_INTERVAL", RunOnStartSource: "jobs.DefaultConfig.NewAPISyncRunOnStart",
			CatchUp: "at_most_one_immediate",
		},
		FinanceCollectJobKind: {
			ID: FinanceCollectJobKind, Kind: FinanceCollectJobKind, Queue: QueueMaintenance,
			OwnerProcess: "platform-worker", Ownership: OwnershipClusterSingleton,
			ScheduleConfig: "XM_FINANCE_COLLECT_INTERVAL", RunOnStartSource: "jobs.DefaultConfig.FinanceCollectRunOnStart",
			CatchUp: "at_most_one_immediate",
		},
		RetentionJobKind: {
			ID: RetentionJobKind, Kind: RetentionJobKind, Queue: QueueMaintenance,
			OwnerProcess: "platform-worker", Ownership: OwnershipClusterSingleton,
			ScheduleConfig: "XM_RETENTION_INTERVAL", RunOnStartSource: "jobs.DefaultConfig.RetentionRunOnStart",
			CatchUp: "at_most_one_immediate",
		},
		AlertEvaluateJobKind: {
			ID: AlertEvaluateJobKind, Kind: AlertEvaluateJobKind, Queue: QueueMaintenance,
			OwnerProcess: "platform-worker", Ownership: OwnershipClusterSingleton,
			ScheduleConfig: "XM_ALERT_EVALUATE_INTERVAL", RunOnStartSource: "jobs.DefaultConfig.AlertEvaluateRunOnStart",
			CatchUp: "at_most_one_immediate",
		},
		ReqlogMetricsJobKind: {
			ID: ReqlogMetricsJobKind, Kind: ReqlogMetricsJobKind, Queue: QueueMaintenance,
			OwnerProcess: "platform-worker", Ownership: OwnershipClusterSingleton,
			ScheduleConfig: "XM_REQLOG_METRICS_INTERVAL", RunOnStartSource: "jobs.DefaultConfig.ReqlogMetricsRunOnStart",
			CatchUp: "at_most_one_immediate",
		},
	}
	if manifest.Version != 1 || manifest.Scheduler != "river-oss-postgres-leader-v0.45" ||
		manifest.ClusterModel != "one-environment-per-database-schema" {
		t.Fatalf("manifest header drifted: %+v", manifest)
	}
	for _, got := range manifest.Jobs {
		wantRow, ok := want[got.ID]
		if !ok {
			t.Fatalf("unexpected manifest job %q", got.ID)
		}
		for name, gotValue := range map[string]string{
			"kind": got.Kind, "queue": got.Queue, "owner_process": got.OwnerProcess,
			"ownership": string(got.Ownership), "schedule_config": got.ScheduleConfig,
			"run_on_start_source": got.RunOnStartSource, "catch_up": got.CatchUp,
		} {
			var wantValue string
			switch name {
			case "kind":
				wantValue = wantRow.Kind
			case "queue":
				wantValue = wantRow.Queue
			case "owner_process":
				wantValue = wantRow.OwnerProcess
			case "ownership":
				wantValue = string(wantRow.Ownership)
			case "schedule_config":
				wantValue = wantRow.ScheduleConfig
			case "run_on_start_source":
				wantValue = wantRow.RunOnStartSource
			case "catch_up":
				wantValue = wantRow.CatchUp
			}
			if gotValue != wantValue {
				t.Errorf("%s.%s = %q, want %q", got.ID, name, gotValue, wantValue)
			}
		}
	}
}

func TestEveryArgsUsesArgsQueueEffectivePeriodAndExplicitDefaultStates(t *testing.T) {
	defaultStates := rivertype.UniqueOptsByStateDefault()
	checks := []struct {
		name   string
		kind   string
		queue  string
		period time.Duration
		opts   func() river.InsertOpts
	}{
		{name: "heartbeat", kind: HeartbeatJobKind, queue: QueueMaintenance, period: DefaultHeartbeatInterval, opts: func() river.InsertOpts { return HeartbeatArgs{}.InsertOpts() }},
		{name: "sub2api", kind: Sub2APISyncJobKind, queue: QueueMaintenance, period: DefaultSub2APISyncInterval, opts: func() river.InsertOpts { return Sub2APISyncArgs{}.InsertOpts() }},
		{name: "newapi", kind: NewAPISyncJobKind, queue: QueueMaintenance, period: DefaultNewAPISyncInterval, opts: func() river.InsertOpts { return NewAPISyncArgs{}.InsertOpts() }},
		{name: "finance", kind: FinanceCollectJobKind, queue: QueueMaintenance, period: DefaultFinanceCollectInterval, opts: func() river.InsertOpts { return FinanceCollectArgs{}.InsertOpts() }},
		{name: "retention", kind: RetentionJobKind, queue: QueueMaintenance, period: DefaultRetentionInterval, opts: func() river.InsertOpts { return RetentionArgs{}.InsertOpts() }},
		{name: "alerts", kind: AlertEvaluateJobKind, queue: QueueMaintenance, period: DefaultAlertEvaluateInterval, opts: func() river.InsertOpts { return AlertEvaluateArgs{}.InsertOpts() }},
		{name: "reqlog_metrics", kind: ReqlogMetricsJobKind, queue: QueueMaintenance, period: DefaultReqlogMetricsInterval, opts: func() river.InsertOpts { return ReqlogMetricsArgs{}.InsertOpts() }},
	}
	for _, tt := range checks {
		t.Run(tt.name, func(t *testing.T) {
			raw := tt.opts()
			if raw.Queue != tt.queue {
				t.Fatalf("queue = %q, want %q", raw.Queue, tt.queue)
			}
			if !raw.UniqueOpts.ByArgs || !raw.UniqueOpts.ByQueue {
				t.Fatal("ByArgs and ByQueue must be enabled")
			}
			if raw.UniqueOpts.ByPeriod != tt.period {
				t.Fatalf("period = %v, want %v", raw.UniqueOpts.ByPeriod, tt.period)
			}
			if !reflect.DeepEqual(raw.UniqueOpts.ByState, defaultStates) {
				t.Fatalf("states = %v, want explicit River defaults %v", raw.UniqueOpts.ByState, defaultStates)
			}
		})
	}
}

func TestProductionArgsContainNoReplicaRunID(t *testing.T) {
	for _, args := range []any{HeartbeatArgs{}, Sub2APISyncArgs{}, NewAPISyncArgs{}, FinanceCollectArgs{}, RetentionArgs{}, AlertEvaluateArgs{}, ReqlogMetricsArgs{}} {
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "replica") || strings.Contains(string(raw), "cluster") || strings.Contains(string(raw), "boot") {
			t.Fatalf("production args contain replica identity: %s", raw)
		}
	}
}

func TestManifestRejectsUnknownDuplicateMissingAndNonRiverJobs(t *testing.T) {
	base := map[string]any{}
	if err := json.Unmarshal(readJobManifestContract(t), &base); err != nil {
		t.Fatal(err)
	}
	mutate := func(t *testing.T, f func(map[string]any)) {
		t.Helper()
		copyMap := deepCopyJSONMap(base)
		f(copyMap)
		raw, err := json.Marshal(copyMap)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadJobManifest(raw); err == nil {
			t.Fatal("mutated manifest unexpectedly accepted")
		}
	}
	mutate(t, func(m map[string]any) { m["unexpected"] = true })
	mutate(t, func(m map[string]any) {
		jobs := m["jobs"].([]any)
		jobs = append(jobs, jobs[0])
		m["jobs"] = jobs
	})
	mutate(t, func(m map[string]any) {
		jobs := m["jobs"].([]any)
		m["jobs"] = jobs[:len(jobs)-1]
	})
	mutate(t, func(m map[string]any) {
		jobs := m["jobs"].([]any)
		jobs[0].(map[string]any)["id"] = "not_a_river_job"
	})
	mutate(t, func(m map[string]any) {
		m["scheduler"] = "cron"
	})
}

func TestEverySideEffectDeclaresAtLeastOnceIdempotencyEvidence(t *testing.T) {
	manifest, _, err := LoadJobManifest(readJobManifestContract(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range manifest.Jobs {
		if row.Execution != "at_least_once" || strings.TrimSpace(row.SideEffectClass) == "" || strings.TrimSpace(row.IdempotencyEvidence) == "" {
			t.Errorf("%s lacks at-least-once/idempotency declaration: %+v", row.ID, row)
		}
	}
}

func validEffectiveConfig() Config {
	cfg := DefaultConfig()
	cfg.Environment = "staging"
	cfg.WorkerClusterID = "platform-staging"
	cfg.RiverSchema = "river"
	return cfg
}

func TestEffectiveManifestSortsJobsAndUsesIntegerSeconds(t *testing.T) {
	manifest, _, err := LoadJobManifest(readJobManifestContract(t))
	if err != nil {
		t.Fatal(err)
	}
	effective, hash, err := BuildEffectiveManifest(validEffectiveConfig(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) != 64 || strings.ToLower(hash) != hash {
		t.Fatalf("invalid effective hash %q", hash)
	}
	if effective.Environment != "staging" || effective.WorkerClusterID != "platform-staging" || effective.RiverSchema != "river" {
		t.Fatalf("identity not carried into effective manifest: %+v", effective)
	}
	ids := make([]string, len(effective.Jobs))
	for i, row := range effective.Jobs {
		ids[i] = row.ID
		if row.IntervalSeconds <= 0 {
			t.Fatalf("%s interval seconds = %d", row.ID, row.IntervalSeconds)
		}
		if row.IntervalSeconds != int64(intervalForJob(row.ID).Seconds()) {
			t.Fatalf("%s interval = %d", row.ID, row.IntervalSeconds)
		}
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatalf("effective jobs are not byte-order sorted: %v", ids)
	}
}

func TestEffectiveManifestRejectsEmptyIdentityAndSubsecondPeriods(t *testing.T) {
	manifest, _, err := LoadJobManifest(readJobManifestContract(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Config){
		"environment": func(c *Config) { c.Environment = "" },
		"cluster":     func(c *Config) { c.WorkerClusterID = "" },
		"schema":      func(c *Config) { c.RiverSchema = "" },
		"subsecond":   func(c *Config) { c.HeartbeatInterval = 500 * time.Millisecond },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := validEffectiveConfig()
			mutate(&cfg)
			if _, _, err := BuildEffectiveManifest(cfg, manifest); err == nil {
				t.Fatal("invalid effective config accepted")
			}
		})
	}
}

func TestEffectiveManifestDisabledJobAndConfigChangesChangeHash(t *testing.T) {
	manifest, _, err := LoadJobManifest(readJobManifestContract(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg := validEffectiveConfig()
	base, baseHash, err := BuildEffectiveManifest(cfg, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(base.Jobs) != 7 {
		t.Fatalf("jobs = %d", len(base.Jobs))
	}
	cfg.RetentionEnabled = false
	disabled, disabledHash, err := BuildEffectiveManifest(cfg, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if baseHash == disabledHash {
		t.Fatal("disabling a job must change effective hash")
	}
	for _, row := range disabled.Jobs {
		if row.ID == RetentionJobKind && row.Enabled {
			t.Fatal("retention should be disabled")
		}
	}
	cfg = validEffectiveConfig()
	cfg.HeartbeatRunOnStart = !cfg.HeartbeatRunOnStart
	_, runOnStartHash, err := BuildEffectiveManifest(cfg, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if baseHash == runOnStartHash {
		t.Fatal("RunOnStart change must change effective hash")
	}
	cfg = validEffectiveConfig()
	cfg.Sub2APISyncInterval += time.Second
	_, intervalHash, err := BuildEffectiveManifest(cfg, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if baseHash == intervalHash {
		t.Fatal("interval change must change effective hash")
	}
}

func TestEffectiveManifestRejectsMismatchedScheduleSource(t *testing.T) {
	manifest, _, err := LoadJobManifest(readJobManifestContract(t))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Jobs[0].ScheduleConfig = "XM_DOES_NOT_EXIST"
	if _, _, err := BuildEffectiveManifest(validEffectiveConfig(), manifest); err == nil {
		t.Fatal("unknown schedule source accepted")
	}
}

func deepCopyJSONMap(src map[string]any) map[string]any {
	raw, _ := json.Marshal(src)
	var dst map[string]any
	_ = json.Unmarshal(raw, &dst)
	return dst
}

func intervalForJob(id string) time.Duration {
	switch id {
	case HeartbeatJobKind:
		return DefaultHeartbeatInterval
	case Sub2APISyncJobKind:
		return DefaultSub2APISyncInterval
	case NewAPISyncJobKind:
		return DefaultNewAPISyncInterval
	case FinanceCollectJobKind:
		return DefaultFinanceCollectInterval
	case RetentionJobKind:
		return DefaultRetentionInterval
	case AlertEvaluateJobKind:
		return DefaultAlertEvaluateInterval
	case ReqlogMetricsJobKind:
		return DefaultReqlogMetricsInterval
	default:
		return 0
	}
}
