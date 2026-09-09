package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// This file is the "规则存在≠调用得到" probe for XM-OPS-TRUTH.
//
// Extracting jobs.ResolveEffectiveMode is not enough on its own: if the
// handler kept a branch of its own, every unit test on either side would
// still pass while the two processes disagreed -- which is exactly what
// happened before this slice (worker resolved per round, ops_overview
// hardcoded "fake"). So this test drives a **real worker round** and a
// **real HTTP request** off the same connector-config row, then compares
// what each one says. Neither side's expected value is written down here;
// each is taken from the other.

// stubObservationStore is the narrowest thing that satisfies
// jobs.ObservationStore. It deliberately does NOT re-implement ops.Store's
// validation: a second copy of the ops consistency rules living in the
// httpapi test package is precisely the "被信任的过期闸" pattern this slice
// exists to remove. The jobs package's own memoryStore covers that.
type stubObservationStore struct{}

func (stubObservationStore) Get(context.Context, string, string) (ops.Observation, error) {
	return ops.Observation{}, errors.New("no rows")
}

func (stubObservationStore) UpsertWithSample(_ context.Context, o ops.Observation) (ops.Observation, error) {
	return o, nil
}

// workerEffectiveMode runs one real newapi_sync round against the given row
// and returns what its job_completed line says about the effective mode.
func workerEffectiveMode(t *testing.T, row *jobs.ConnectorConfig, envDefault jobs.NewAPIMode) (mode, source string) {
	t.Helper()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	source2 := jobs.ConnectorConfigSourceFunc(
		func(_ context.Context, _, _ string) (*jobs.ConnectorConfig, error) { return row, nil })
	worker := jobs.NewNewAPISyncWorker(jobs.NewAPISyncOptions{
		Logger:      logger,
		Environment: "development",
		InstanceID:  "newapi-test",
		Store:       stubObservationStore{},
		NewClient: jobs.NewDynamicNewAPIClientFactory(jobs.NewAPIDynamicOptions{
			Source:      source2,
			Logger:      logger,
			DefaultMode: envDefault,
			// Deliberately incomplete: the round then fails with
			// not_supported, but the *mode* is still resolved and still has
			// to be reported. "客户端造不出来" and "不知道按什么在跑" are two
			// different facts.
			Defaults: jobs.NewAPIRealConfig{Environment: "development", InstanceID: "newapi-test"},
		}),
		Now: func() time.Time { return time.Date(2026, 8, 27, 3, 4, 5, 0, time.UTC) },
	})
	job := &river.Job[jobs.NewAPISyncArgs]{JobRow: &rivertype.JobRow{
		ID: 1, Kind: jobs.NewAPISyncJobKind, Queue: jobs.QueueMaintenance, Attempt: 1, MaxAttempts: 3,
	}}
	if err := worker.Work(context.Background(), job); err != nil {
		t.Fatalf("worker round failed: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(line), &parsed); err != nil {
			continue
		}
		if parsed["event"] != "job_completed" {
			continue
		}
		got, _ := parsed["newapi_mode"].(string)
		gotSource, _ := parsed["newapi_mode_source"].(string)
		return got, gotSource
	}
	t.Fatalf("worker produced no job_completed line: %s", logs.String())
	return "", ""
}

// apiPipeline runs one real GET /ops/overview against the same row.
func apiPipeline(t *testing.T, platform string, items []credentials.ConnectorConfig) (decodedOpsSync, string) {
	t.Helper()
	h := testRouterWithOpsOverview(t, &fakeMetricLister{},
		&fakeConnectorConfigLister{items: items}, nil, AlertDeliveryStatus{})
	rec := opsOverviewGet(t, h)
	if rec.Code != 200 {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	for _, p := range decodeOpsOverview(t, rec).SyncPipelines {
		if p.Platform == platform {
			return p, rec.Body.String()
		}
	}
	t.Fatalf("%s pipeline missing from response: %s", platform, rec.Body.String())
	return decodedOpsSync{}, ""
}

// TestOpsOverviewAgreesWithWorkerWhenRowExists: with a row, both sides read
// the same row through the same resolver, so their answers must be equal
// **character for character** -- and neither expectation is hardcoded here.
func TestOpsOverviewAgreesWithWorkerWhenRowExists(t *testing.T) {
	updated := time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC)
	cases := []struct {
		name       string
		rowMode    string
		envDefault jobs.NewAPIMode
	}{
		// The production shape: row says real while the worker's env default
		// still says fake. Anyone reading env (either side) disagrees here.
		{"row real / env fake", "real", jobs.NewAPIModeFake},
		// The opposite direction, so "the row wins" cannot be true by accident.
		{"row fake / env real", "fake", jobs.NewAPIModeReal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workerMode, workerSource := workerEffectiveMode(t, &jobs.ConnectorConfig{
				Platform: "newapi", Environment: "development", Mode: tc.rowMode,
			}, tc.envDefault)
			api, _ := apiPipeline(t, "newapi", []credentials.ConnectorConfig{
				{Platform: "newapi", Environment: "development", Mode: tc.rowMode, UpdatedAt: updated},
			})
			if api.EffectiveMode != workerMode {
				t.Fatalf("API says effective_mode=%q, the worker's job_completed says newapi_mode=%q",
					api.EffectiveMode, workerMode)
			}
			if api.EffectiveModeSource != workerSource {
				t.Fatalf("API says effective_mode_source=%q, the worker says newapi_mode_source=%q",
					api.EffectiveModeSource, workerSource)
			}
			if workerSource != jobs.ModeSourceDatabase {
				t.Fatalf("with a row present both sides must say %q, got %q", jobs.ModeSourceDatabase, workerSource)
			}
		})
	}
}

// TestOpsOverviewAbstainsWhereWorkerFallsBackToEnv: with **no** row the two
// processes legitimately know different things, and the API's job is to say
// so rather than to guess.
//
// The worker sees its own XM_NEWAPI_MODE and answers from it (source=env).
// platform-api's container has no such key -- so it answers "" / unknown.
// The old implementation answered "fake" here, which was the exact opposite
// of what the worker was doing on production.
func TestOpsOverviewAbstainsWhereWorkerFallsBackToEnv(t *testing.T) {
	workerMode, workerSource := workerEffectiveMode(t, nil, jobs.NewAPIModeReal)
	if workerMode != "real" || workerSource != jobs.ModeSourceEnv {
		t.Fatalf("worker with no row and env real said %q/%q, want real/%s",
			workerMode, workerSource, jobs.ModeSourceEnv)
	}

	api, body := apiPipeline(t, "newapi", nil)
	if api.EffectiveMode == "fake" {
		t.Fatal("API answered fake with no row -- that is the removed guess, " +
			"and on production it was the opposite of what the worker ran")
	}
	if api.EffectiveMode != "" {
		t.Fatalf("API effective_mode = %q, want empty (it cannot see the worker's env)", api.EffectiveMode)
	}
	if api.EffectiveModeSource != jobs.ModeSourceUnknown {
		t.Fatalf("API effective_mode_source = %q, want %q", api.EffectiveModeSource, jobs.ModeSourceUnknown)
	}
	// "config_available" and "we know the mode" stay separate facts.
	if !api.ConfigAvailable {
		t.Fatal("config_available = false, want true: the credentials module IS mounted")
	}
	// Keep the empty-string assertion from going vacuous if the field is dropped.
	if !strings.Contains(body, `"effective_mode_source"`) {
		t.Fatalf("response carries no effective_mode_source field: %s", body)
	}
	// Never leak an endpoint or credential ref through this surface.
	if strings.Contains(body, "secret://") {
		t.Fatalf("response leaked a credential ref: %s", body)
	}
}

// TestOpsOverviewBothPipelinesUseTheResolver: one platform being right must
// not hide the other. Both rows go in, both answers come out.
func TestOpsOverviewBothPipelinesUseTheResolver(t *testing.T) {
	updated := time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC)
	h := testRouterWithOpsOverview(t, &fakeMetricLister{}, &fakeConnectorConfigLister{
		items: []credentials.ConnectorConfig{
			{Platform: "sub2api", Environment: "development", Mode: "real", UpdatedAt: updated},
			// newapi deliberately absent -- the two pipelines must answer
			// independently, not share one computed value.
		},
	}, nil, AlertDeliveryStatus{})
	got := decodeOpsOverview(t, opsOverviewGet(t, h))
	if len(got.SyncPipelines) != 2 {
		t.Fatalf("sync_pipelines = %d, want 2", len(got.SyncPipelines))
	}
	for _, p := range got.SyncPipelines {
		switch p.Platform {
		case "sub2api":
			if p.EffectiveMode != "real" || p.EffectiveModeSource != jobs.ModeSourceDatabase {
				t.Fatalf("sub2api = %q/%q, want real/%s", p.EffectiveMode, p.EffectiveModeSource, jobs.ModeSourceDatabase)
			}
		case "newapi":
			if p.EffectiveMode != "" || p.EffectiveModeSource != jobs.ModeSourceUnknown {
				t.Fatalf("newapi = %q/%q, want \"\"/%s", p.EffectiveMode, p.EffectiveModeSource, jobs.ModeSourceUnknown)
			}
		default:
			t.Fatalf("unexpected pipeline platform %q", p.Platform)
		}
	}
}
