package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	connusers "github.com/xufei5620/xingmang-platform/connectors/platformusers"
	platformusers "github.com/xufei5620/xingmang-platform/internal/platform/platformusers"
)

type fakeDailyUsageQuerier struct {
	series connusers.DailyUsageSeries
	err    error
	input  platformusers.DailyUsageInput
}

func (f *fakeDailyUsageQuerier) DailyUsage(_ context.Context, input platformusers.DailyUsageInput) (connusers.DailyUsageSeries, error) {
	f.input = input
	return f.series, f.err
}

func TestGetPlatformUserDailyUsageUsesCanonicalRefAndSeparateCoverage(t *testing.T) {
	q := &fakeDailyUsageQuerier{series: connusers.DailyUsageSeries{From: "2026-08-27", To: "2026-08-29", Points: []connusers.DailyUsagePoint{{Day: "2026-08-27", Consumed: connusers.KnownAmount(0, "CNY"), Requests: connusers.KnownCount(0)}, {Day: "2026-08-28", Consumed: connusers.UnknownAmount(), Requests: connusers.UnknownCount()}}, Coverage: connusers.SeriesCoverage{ExpectedDays: 3, CoveredDays: 1, Complete: false}}}
	router := chi.NewRouter()
	router.Get("/platforms/{platform}/users/{userID}/daily-usage", GetPlatformUserDailyUsageHandler(q))
	req := httptest.NewRequest(http.MethodGet, "/platforms/sub2api/users/u-755f3130323431/daily-usage?day=2026-08-29&days=3", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || q.input.UserID != "u_10241" || q.input.Days != 3 {
		t.Fatalf("status=%d input=%+v body=%s", rec.Code, q.input, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"complete":false`) || !strings.Contains(rec.Body.String(), `"minor_units":"0"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
}
