package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
)

type fakeRunwayPreviewSource struct {
	items      []finance.UpstreamRunway
	thresholds finance.RunwayThresholds
	calls      int
}

func (f *fakeRunwayPreviewSource) UpstreamRunways(_ context.Context, _ string, thresholds finance.RunwayThresholds) ([]finance.UpstreamRunway, error) {
	f.calls++
	f.thresholds = thresholds
	return f.items, nil
}

type fakePreviewAlerts struct{ items []alerts.Alert }

func (f *fakePreviewAlerts) ListByStatus(_ context.Context, _ string, _ []alerts.Status, _ int32) ([]alerts.Alert, error) {
	return f.items, nil
}

func (f *fakePreviewAlerts) ListRecent(_ context.Context, _ string, _ int32) ([]alerts.Alert, error) {
	return f.items, nil
}

func previewRouter(t *testing.T, provider RunwayThresholdProvider, source RunwayPreviewSource, alertStore AlertLister) http.Handler {
	t.Helper()
	resolver, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger: discardLogger(), Service: "platform-api", Environment: "development", DB: fakePinger{}, Resolver: resolver,
		ActionRegistry: action.NewRegistry(), FinanceRunwayConfig: provider, FinanceRunwayPreviewSource: source, Alerts: alertStore,
	})
}

func TestPreviewRunwayThresholdHandlerReturnsEvaluationAndObservationTimes(t *testing.T) {
	id := uuid.New()
	days := 8
	observed := time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC)
	current := finance.RunwayThresholds{CriticalDays: 5, WarningDays: 10, SeriousDays: 20}
	provider := &fakeRunwayConfigProvider{snapshot: finance.RunwayThresholdSnapshot{
		Thresholds: current, Revision: 8, Source: "database", UpdatedAt: observed,
	}}
	source := &fakeRunwayPreviewSource{items: []finance.UpstreamRunway{{
		AccountID: id, Name: "Relay A", AccessMethod: finance.AccessUpstreamKey,
		Runway: finance.Runway{Days: &days, BalanceObservedAt: &observed},
	}}}
	alertStore := &fakePreviewAlerts{items: []alerts.Alert{{
		RuleKey:  alerts.RuleUpstreamRunwayLow,
		DedupKey: alerts.RuleUpstreamRunwayLow + ":development:" + id.String(),
		Severity: alerts.SeverityWarning, Status: alerts.StatusOpen,
	}}}
	path := "/api/v1/finance/runway-thresholds/preview?critical_days=10&warning_days=15&serious_days=20"
	req := httptest.NewRequest(http.MethodGet, path, nil)
	devHeaders(req, finance.ScopeRead)
	rec := httptest.NewRecorder()
	previewRouter(t, provider, source, alertStore).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		CurrentRevision int64  `json:"current_revision"`
		EvaluationAt    string `json:"evaluation_at"`
		Items           []struct {
			ObservedAt *string `json:"observed_at"`
			Transition string  `json:"alert_transition"`
		} `json:"items"`
		Counts struct {
			CurrentInconsistent int `json:"current_inconsistent"`
		} `json:"counts"`
		AlertCoverageComplete bool `json:"alert_coverage_complete"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.CurrentRevision != 8 || body.EvaluationAt == "" || len(body.Items) != 1 || body.Items[0].ObservedAt == nil {
		t.Fatalf("missing preview evidence: %+v", body)
	}
	if *body.Items[0].ObservedAt != observed.Format(time.RFC3339) {
		t.Fatalf("observed_at=%s", *body.Items[0].ObservedAt)
	}
	if body.EvaluationAt == *body.Items[0].ObservedAt {
		t.Fatal("evaluation_at must not be copied to item observed_at")
	}
	if body.Items[0].Transition != string(alerts.RunwayWouldEscalate) || body.Counts.CurrentInconsistent != 0 {
		t.Fatalf("unexpected transition/counts: %+v", body)
	}
	if body.AlertCoverageComplete {
		t.Fatal("legacy alert reader must not claim complete coverage")
	}
	if source.calls != 1 || source.thresholds != current {
		t.Fatalf("source call/threshold mismatch: %+v", source)
	}
}

func TestPreviewRunwayThresholdHandlerRejectsUnorderedThresholdsWithoutReads(t *testing.T) {
	provider := &fakeRunwayConfigProvider{}
	source := &fakeRunwayPreviewSource{}
	alertStore := &fakePreviewAlerts{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/finance/runway-thresholds/preview?critical_days=10&warning_days=10&serious_days=20", nil)
	devHeaders(req, finance.ScopeRead)
	rec := httptest.NewRecorder()
	previewRouter(t, provider, source, alertStore).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "必须满足") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if provider.calls != 0 || source.calls != 0 {
		t.Fatalf("invalid params must not read: provider=%d source=%d", provider.calls, source.calls)
	}
}

func TestPreviewRunwayThresholdHandlerRequiresFinanceRead(t *testing.T) {
	provider := &fakeRunwayConfigProvider{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/finance/runway-thresholds/preview?critical_days=5&warning_days=10&serious_days=20", nil)
	devHeaders(req, "")
	rec := httptest.NewRecorder()
	previewRouter(t, provider, &fakeRunwayPreviewSource{}, &fakePreviewAlerts{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), finance.ScopeRead) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if provider.calls != 0 {
		t.Fatal("provider must not be called when denied")
	}
}
