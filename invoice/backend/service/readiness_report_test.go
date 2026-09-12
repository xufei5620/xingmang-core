package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"invoice-system/backend/internal/httpapi"
)

func TestModuleReadinessPreservesSafeDiagnosticsAndContainedDegradation(t *testing.T) {
	privateErr := errors.New("dial postgres://reader:fixture@10.24.6.8:5432/invoice user_id=customer-391")
	cases := []struct {
		name    string
		outcome httpapi.ReadinessOutcome
		err     error
		want    ReadinessReport
	}{
		{"healthy", httpapi.ReadinessOutcome{}, nil, ReadinessReport{Ready: true}},
		{"contained dead remains visible", httpapi.ReadinessOutcome{Degraded: []string{"source_ingest_dead_events_contained"}}, nil, ReadinessReport{Ready: true, Degraded: []string{"source_ingest_dead_events_contained"}}},
		{"unsafe degraded name filtered", httpapi.ReadinessOutcome{Degraded: []string{"source_ingest_dead_events_contained", "10.24.6.8:5432", "customer-391"}}, nil, ReadinessReport{Ready: true, Degraded: []string{"source_ingest_dead_events_contained"}}},
		{"named failure", httpapi.ReadinessOutcome{}, httpapi.NotReady("database", "the invoice database is unreachable", privateErr), ReadinessReport{Check: "database", Summary: "the invoice database is unreachable"}},
		{"plain private failure", httpapi.ReadinessOutcome{}, privateErr, ReadinessReport{Summary: "required dependencies are unavailable"}},
		{"unsafe check filtered", httpapi.ReadinessOutcome{}, httpapi.NotReady("10.24.6.8:5432", "the invoice database is unreachable", privateErr), ReadinessReport{Summary: "required dependencies are unavailable"}},
		{"unsafe summary filtered", httpapi.ReadinessOutcome{}, httpapi.NotReady("database", privateErr.Error(), privateErr), ReadinessReport{Summary: "required dependencies are unavailable"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			module := newRuntime(appRuntime{AuthMode: "session", SourceMode: "agent", readiness: func(context.Context) (httpapi.ReadinessOutcome, error) {
				calls++
				return tc.outcome, tc.err
			}})
			got := module.Readiness(context.Background())
			// A legacy injected closure supplies no per-gate observations.
			unexecuted := unevaluatedReadinessReport()
			tc.want.Checks = unexecuted.Checks
			tc.want.SourceNonFreshnessStatus = unexecuted.SourceNonFreshnessStatus
			tc.want.SourceFreshness = unexecuted.SourceFreshness
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("public readiness=%+v want %+v", got, tc.want)
			}
			body, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"10.24.6.8", "5432", "postgres://", "customer-391", "reader:fixture"} {
				if strings.Contains(string(body), forbidden) {
					t.Errorf("public readiness exposed %q", forbidden)
				}
			}
			if calls != 1 {
				t.Fatalf("report evaluated dependencies %d times", calls)
			}
			if err := module.Ready(context.Background()); !errors.Is(err, tc.err) {
				t.Fatalf("Ready lost original error identity: %v", err)
			}
			if calls != 2 {
				t.Fatalf("Ready evaluated dependencies more than once: %d", calls)
			}
		})
	}
}

func TestModuleReadinessReportsUnavailableLifecycleWithoutPrivateDetails(t *testing.T) {
	module := newRuntime(appRuntime{AuthMode: "session", SourceMode: "agent"})
	if got := module.Readiness(context.Background()); got.Ready || got.Summary != "required dependencies are unavailable" {
		t.Fatalf("uninitialized probe report=%+v", got)
	}
	if err := module.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := module.Readiness(context.Background()); got.Ready || got.Summary != "required dependencies are unavailable" {
		t.Fatalf("shutdown report=%+v", got)
	}
}

func TestModuleReadinessRetainsBoundedFailureAndRecoveryLogs(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	privateErr := errors.New("fixture internal database 10.24.6.8:5432 unavailable")
	dependencyErr := httpapi.NotReady("database", "the invoice database is unreachable", privateErr)
	module := newRuntime(appRuntime{AuthMode: "session", SourceMode: "agent", readiness: func(context.Context) (httpapi.ReadinessOutcome, error) {
		return httpapi.ReadinessOutcome{}, dependencyErr
	}})
	for i := 0; i < 30; i++ {
		if module.Readiness(context.Background()).Ready {
			t.Fatal("failed dependency reported ready")
		}
	}
	if got := strings.Count(logs.String(), "readiness check failed"); got != 1 {
		t.Fatalf("30 repeated probes logged %d failure lines", got)
	}
	if !strings.Contains(logs.String(), privateErr.Error()) {
		t.Fatal("the underlying dependency error was lost from private logs")
	}
	dependencyErr = nil
	for i := 0; i < 2; i++ {
		if !module.Readiness(context.Background()).Ready {
			t.Fatal("recovered dependency reported unavailable")
		}
	}
	if got := strings.Count(logs.String(), "readiness recovered"); got != 1 {
		t.Fatalf("recovery logged %d times", got)
	}
}
