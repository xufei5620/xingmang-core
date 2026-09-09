package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/cpa"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

type fakeCPAKeysQuerier struct {
	gotDay string
	page   cpa.KeyUsagePage
	err    error
}

func (f *fakeCPAKeysQuerier) KeyUsage(_ context.Context, day string) (cpa.KeyUsagePage, error) {
	f.gotDay = day
	return f.page, f.err
}

func cpaKeysRouter(t *testing.T, q CPAKeysQuerier) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger: discardLogger(), Service: "platform-api", Environment: "development",
		DB: fakePinger{}, Resolver: res, ActionRegistry: action.NewRegistry(),
		CPAKeys: q,
	})
}

func getCPAKeys(t *testing.T, q CPAKeysQuerier, scopes, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/platforms/cpa/keys"+query, nil)
	devHeaders(req, scopes)
	rec := httptest.NewRecorder()
	cpaKeysRouter(t, q).ServeHTTP(rec, req)
	return rec
}

func sampleCPAKeyPage(day string) cpa.KeyUsagePage {
	observed := time.Date(2026, 8, 31, 5, 0, 0, 0, time.UTC)
	cost := int64(249000)
	lastUsed := time.Date(2026, 8, 31, 4, 0, 0, 0, time.UTC)
	return cpa.KeyUsagePage{
		Snapshot:    cpa.Snapshot{ObservedAt: observed, Watermark: "wm", Instance: cpa.FileInstance},
		BusinessDay: day,
		Rows: []cpa.KeyUsageRow{
			{APIKeyHash: "hash-a", Alias: "prod-key-a", RequestCount: 2, TokensIn: 3000, TokensOut: 1500,
				CostMicros: &cost, LastUsedAt: &lastUsed},
			{APIKeyHash: "hash-b", RequestCount: 1, TokensIn: 300, UnpricedRequestCount: 1},
		},
		TotalKeyCount: 2,
	}
}

func TestListCPAKeysHandler_Success(t *testing.T) {
	q := &fakeCPAKeysQuerier{page: sampleCPAKeyPage("2026-08-31")}
	rec := getCPAKeys(t, q, ScopeCPAKeysRead, "?day=2026-08-31")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if q.gotDay != "2026-08-31" {
		t.Fatalf("querier received day = %q, want 2026-08-31", q.gotDay)
	}
	var body cpaKeyUsagePageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.TotalKeyCount != 2 || len(body.Items) != 2 {
		t.Fatalf("body = %+v", body)
	}
	first := body.Items[0]
	if first.APIKeyHash != "hash-a" || first.Alias != "prod-key-a" {
		t.Fatalf("items[0] = %+v, want hash-a/prod-key-a", first)
	}
	if first.Cost == nil || first.Cost.AmountMinor != "249000" || first.Cost.Currency != cpa.Currency || first.Cost.Scale != 6 {
		t.Fatalf("items[0].Cost = %+v, want amount_minor=249000 currency=%s scale=6", first.Cost, cpa.Currency)
	}
	if first.LastUsedAt == nil {
		t.Fatal("items[0].LastUsedAt = nil, want a timestamp")
	}
	second := body.Items[1]
	if second.Cost != nil {
		t.Fatalf("items[1].Cost = %+v, want nil (unpriced key, must not fabricate a cost)", second.Cost)
	}
	if second.UnpricedRequestCount != 1 {
		t.Fatalf("items[1].UnpricedRequestCount = %d, want 1", second.UnpricedRequestCount)
	}
	if body.Snapshot.Source != cpa.FileInstance {
		t.Fatalf("snapshot.source = %q, want %q", body.Snapshot.Source, cpa.FileInstance)
	}
}

func TestListCPAKeysHandler_DefaultsDayToTodayUTC(t *testing.T) {
	q := &fakeCPAKeysQuerier{page: sampleCPAKeyPage(time.Now().UTC().Format("2006-01-02"))}
	rec := getCPAKeys(t, q, ScopeCPAKeysRead, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	want := time.Now().UTC().Format("2006-01-02")
	if q.gotDay != want {
		t.Fatalf("querier received day = %q, want today (%q)", q.gotDay, want)
	}
}

func TestListCPAKeysHandler_RejectsMalformedDay(t *testing.T) {
	q := &fakeCPAKeysQuerier{page: sampleCPAKeyPage("2026-08-31")}
	for _, bad := range []string{"not-a-day", "2026/08/31", "2026-8-31"} {
		rec := getCPAKeys(t, q, ScopeCPAKeysRead, "?day="+bad)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("day=%q status = %d, want 400", bad, rec.Code)
		}
	}
}

func TestListCPAKeysHandler_RequiresScope(t *testing.T) {
	q := &fakeCPAKeysQuerier{page: sampleCPAKeyPage("2026-08-31")}
	rec := getCPAKeys(t, q, "ops.read", "?day=2026-08-31") // wrong scope entirely
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestListCPAKeysHandler_NotMountedWhenDepIsNil(t *testing.T) {
	rec := getCPAKeys(t, nil, ScopeCPAKeysRead, "?day=2026-08-31")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (route must not be registered at all when CPAKeys is nil)", rec.Code)
	}
}

func TestListCPAKeysHandler_ConnectorErrorsMapToSensibleStatus(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"bad_response", connector.NewError(connector.KindBadResponse, "cpa.key_usage.read", nil), http.StatusBadRequest},
		{"not_supported", connector.NewError(connector.KindNotSupported, "cpa.key_usage.read", nil), http.StatusNotImplemented},
		{"unavailable", connector.NewError(connector.KindUnavailable, "cpa.key_usage.read", nil), http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &fakeCPAKeysQuerier{err: tc.err}
			rec := getCPAKeys(t, q, ScopeCPAKeysRead, "?day=2026-08-31")
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestListCPAKeysHandler_RejectsUninitializedSnapshot(t *testing.T) {
	q := &fakeCPAKeysQuerier{page: cpa.KeyUsagePage{}} // zero Snapshot: ObservedAt/Instance both empty
	rec := getCPAKeys(t, q, ScopeCPAKeysRead, "?day=2026-08-31")
	if rec.Code == http.StatusOK {
		t.Fatal("status = 200, want an error status for an uninitialized snapshot")
	}
}

func TestListCPAKeysHandler_NeverEchoesRawAPIKeyMaterialField(t *testing.T) {
	// Guard against a future field rename accidentally exposing something
	// beyond the one-way hash: the response must only ever carry
	// "api_key_hash", never a bare "api_key" or "key" field.
	q := &fakeCPAKeysQuerier{page: sampleCPAKeyPage("2026-08-31")}
	rec := getCPAKeys(t, q, ScopeCPAKeysRead, "?day=2026-08-31")
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	items, _ := raw["items"].([]any)
	for _, item := range items {
		obj, _ := item.(map[string]any)
		if _, ok := obj["api_key"]; ok {
			t.Fatal("response item has a bare api_key field")
		}
		if _, ok := obj["key"]; ok {
			t.Fatal("response item has a bare key field")
		}
	}
}
