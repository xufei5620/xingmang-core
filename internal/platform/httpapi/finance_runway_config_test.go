package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
)

type fakeRunwayConfigProvider struct {
	snapshot finance.RunwayThresholdSnapshot
	err      error
	calls    int
}

func (f *fakeRunwayConfigProvider) Current(_ context.Context, environment string) (finance.RunwayThresholdSnapshot, error) {
	f.calls++
	if f.err != nil {
		return finance.RunwayThresholdSnapshot{}, f.err
	}
	f.snapshot.Environment = environment
	return f.snapshot, nil
}

type fakeRunwayHistoryLister struct {
	items []finance.RunwayThresholdHistoryEntry
	err   error
	query finance.RunwayThresholdHistoryQuery
}

func (f *fakeRunwayHistoryLister) ListHistory(_ context.Context, q finance.RunwayThresholdHistoryQuery) ([]finance.RunwayThresholdHistoryEntry, error) {
	f.query = q
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

func runwayConfigRouter(t *testing.T, provider RunwayThresholdProvider, history RunwayThresholdHistoryLister) http.Handler {
	t.Helper()
	resolver, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger: discardLogger(), Service: "platform-api", Environment: "development",
		DB: fakePinger{}, Resolver: resolver, ActionRegistry: action.NewRegistry(),
		FinanceRunwayConfig: provider, FinanceRunwayConfigHistory: history,
	})
}

func runwayRequest(t *testing.T, h http.Handler, path, scopes string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	devHeaders(req, scopes)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestGetRunwayThresholdsReturnsDatabaseRevision(t *testing.T) {
	provider := &fakeRunwayConfigProvider{snapshot: finance.RunwayThresholdSnapshot{
		Thresholds: finance.RunwayThresholds{CriticalDays: 5, WarningDays: 10, SeriousDays: 20},
		Revision:   7, Source: "database", UpdatedAt: time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC),
		UpdatedBy: "staff:operator-01", Reason: "按近期充值提前量调整", RequestID: "req-7",
	}}
	rec := runwayRequest(t, runwayConfigRouter(t, provider, nil), "/api/v1/finance/runway-thresholds", finance.ScopeRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Environment  string `json:"environment"`
		CriticalDays int    `json:"critical_days"`
		WarningDays  int    `json:"warning_days"`
		SeriousDays  int    `json:"serious_days"`
		Revision     int64  `json:"revision"`
		Source       string `json:"source"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Environment != "development" || got.CriticalDays != 5 || got.WarningDays != 10 || got.SeriousDays != 20 || got.Revision != 7 || got.Source != "database" {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls=%d want 1", provider.calls)
	}
}

func TestGetRunwayThresholdsRequiresFinanceRead(t *testing.T) {
	provider := &fakeRunwayConfigProvider{}
	rec := runwayRequest(t, runwayConfigRouter(t, provider, nil), "/api/v1/finance/runway-thresholds", "")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), finance.ScopeRead) {
		t.Fatalf("expected finance.read 403, got %d %s", rec.Code, rec.Body.String())
	}
	if provider.calls != 0 {
		t.Fatalf("provider must not be called on denied request: %d", provider.calls)
	}
}

func TestGetRunwayThresholdsUnavailableDoesNotSynthesizeDefaults(t *testing.T) {
	provider := &fakeRunwayConfigProvider{err: finance.ErrRunwayConfigUnavailable}
	rec := runwayRequest(t, runwayConfigRouter(t, provider, nil), "/api/v1/finance/runway-thresholds", finance.ScopeRead)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, string(action.CodeRunwayConfigUnavailable)) || !strings.Contains(body, "暂不可用") {
		t.Fatalf("missing safe unavailable response: %s", body)
	}
	if strings.Contains(body, "critical_days") || strings.Contains(body, "warning_days") || strings.Contains(body, "serious_days") {
		t.Fatalf("unavailable response must not synthesize thresholds: %s", body)
	}
}

func TestListRunwayThresholdHistoryIsBoundedAndDescending(t *testing.T) {
	history := &fakeRunwayHistoryLister{items: []finance.RunwayThresholdHistoryEntry{
		{Environment: "development", Revision: 3, Thresholds: finance.RunwayThresholds{CriticalDays: 5, WarningDays: 10, SeriousDays: 20}, ChangedAt: time.Date(2026, 8, 28, 3, 0, 0, 0, time.UTC), ChangedBy: "staff:c", Reason: "r3", RequestID: "req-3", ChangeSource: "action"},
		{Environment: "development", Revision: 2, Thresholds: finance.RunwayThresholds{CriticalDays: 4, WarningDays: 9, SeriousDays: 18}, ChangedAt: time.Date(2026, 8, 27, 3, 0, 0, 0, time.UTC), ChangedBy: "staff:b", Reason: "r2", RequestID: "req-2", ChangeSource: "action"},
		{Environment: "development", Revision: 1, Thresholds: finance.RunwayThresholds{CriticalDays: 3, WarningDays: 8, SeriousDays: 16}, ChangedAt: time.Date(2026, 8, 26, 3, 0, 0, 0, time.UTC), ChangedBy: "bootstrap", Reason: "r1", RequestID: "req-1", ChangeSource: "bootstrap"},
	}}
	rec := runwayRequest(t, runwayConfigRouter(t, &fakeRunwayConfigProvider{}, history), "/api/v1/finance/runway-thresholds/history?limit=2&before_revision=9", finance.ScopeRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Items []struct {
			Revision int64 `json:"revision"`
		} `json:"items"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 || got.Items[0].Revision != 3 || got.Items[1].Revision != 2 || !got.HasMore {
		t.Fatalf("unexpected history page: %+v", got)
	}
	if history.query.Environment != "development" || history.query.BeforeRevision != 9 || history.query.Limit != 3 {
		t.Fatalf("unexpected lister query: %+v", history.query)
	}
}

func TestListRunwayThresholdHistoryRejectsInvalidLimit(t *testing.T) {
	history := &fakeRunwayHistoryLister{err: errors.New("should not call")}
	rec := runwayRequest(t, runwayConfigRouter(t, &fakeRunwayConfigProvider{}, history), "/api/v1/finance/runway-thresholds/history?limit=101", finance.ScopeRead)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if history.query.Limit != 0 {
		t.Fatalf("invalid limit must not call lister: %+v", history.query)
	}
}
