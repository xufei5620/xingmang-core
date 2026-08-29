package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

type fakeBindingLister struct {
	service  finance.BindingService
	active   []finance.PlatformChannelBinding
	history  map[string][]finance.PlatformChannelBinding
	evidence []finance.TokenMapEvidence
}

func (f fakeBindingLister) GetService(context.Context, uuid.UUID) (finance.BindingService, error) {
	return f.service, nil
}
func (f fakeBindingLister) ListActive(context.Context, uuid.UUID) ([]finance.PlatformChannelBinding, error) {
	return f.active, nil
}
func (f fakeBindingLister) History(_ context.Context, ref finance.ChannelRef) ([]finance.PlatformChannelBinding, error) {
	return f.history[ref.ExternalChannelID], nil
}
func (f fakeBindingLister) TokenEvidence(context.Context, string) ([]finance.TokenMapEvidence, error) {
	return f.evidence, nil
}

type fakeMetricListerForBinding struct{ items []ops.Observation }

func (f fakeMetricListerForBinding) ListByEnvironment(context.Context, string) ([]ops.Observation, error) {
	return f.items, nil
}

func bindingObservation(serviceID string) ops.Observation {
	now := time.Date(2026, 8, 29, 1, 0, 0, 0, time.UTC)
	reported := int64(3)
	return ops.Observation{
		MetricKey: "newapi.channels.status", Source: "newapi-prod", Environment: "production",
		ObservedAt: &now, SyncedAt: now, Status: ops.SyncOK, Value: map[string]any{
			"inventory_completeness": map[string]any{"complete": true, "truncated": false, "reported_count": reported, "fetched_count": int64(3), "evidence": "reported_count"},
			"coverage_partial":       true,
			"channels": []any{
				map[string]any{"channel_id": "1", "name": "OpenAI 主"},
				map[string]any{"channel_id": "2", "name": "Claude 备用"},
				map[string]any{"channel_id": "3", "name": "Gemini"},
			},
		}, StalenessThresholdSeconds: 1800,
	}
}

func callBindingQuery(t *testing.T, source PlatformChannelBindingLister, metrics MetricLister, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/finance/platform-channel-bindings?"+query, nil)
	req = req.WithContext(principal.WithPrincipal(req.Context(), principal.Principal{ID: "staff", Type: principal.TypeHuman, IdentityZone: "staff", Issuer: "issuer", Subject: "subject", Environment: "production"}))
	rec := httptest.NewRecorder()
	ListPlatformChannelBindingsHandler(source, metrics).ServeHTTP(rec, req)
	return rec
}

func TestPlatformChannelBindingsQueryReturnsCandidatesAndSeparateCoverage(t *testing.T) {
	serviceID := uuid.New()
	account := uuid.New()
	source := fakeBindingLister{
		service:  finance.BindingService{ID: serviceID, ServiceType: "newapi", InstanceID: "newapi-prod", Environment: "production", Status: "active"},
		evidence: []finance.TokenMapEvidence{{ExternalChannelID: "2", UpstreamAccountID: account, ActiveServiceCount: 1, SystemTypeMatches: true}},
	}
	rec := callBindingQuery(t, source, fakeMetricListerForBinding{items: []ops.Observation{bindingObservation(serviceID.String())}}, "service_id="+serviceID.String()+"&include_history=true&limit=2")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Inventory struct {
			Complete bool   `json:"complete"`
			Coverage bool   `json:"coverage_partial"`
			Reported *int64 `json:"reported_count"`
		} `json:"inventory"`
		Items []struct {
			Candidate struct {
				State string `json:"state"`
			} `json:"candidate"`
		} `json:"items"`
		Next *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Inventory.Complete || !body.Inventory.Coverage || body.Inventory.Reported == nil || *body.Inventory.Reported != 3 {
		t.Fatalf("inventory=%+v", body.Inventory)
	}
	if len(body.Items) != 2 || body.Items[0].Candidate.State != "unmapped" || body.Next == nil || *body.Next == "" {
		t.Fatalf("items=%+v next=%v", body.Items, body.Next)
	}
}

func TestPlatformChannelBindingsQueryCursorIsOpaqueAndStable(t *testing.T) {
	serviceID := uuid.New()
	source := fakeBindingLister{service: finance.BindingService{ID: serviceID, ServiceType: "newapi", InstanceID: "newapi-prod", Environment: "production", Status: "active"}}
	first := callBindingQuery(t, source, fakeMetricListerForBinding{items: []ops.Observation{bindingObservation(serviceID.String())}}, "service_id="+serviceID.String()+"&limit=1")
	var page struct {
		Items []struct {
			Ref struct {
				ID string `json:"external_channel_id"`
			} `json:"channel_ref"`
		} `json:"items"`
		Next *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Next == nil {
		t.Fatal("expected next cursor")
	}
	second := callBindingQuery(t, source, fakeMetricListerForBinding{items: []ops.Observation{bindingObservation(serviceID.String())}}, "service_id="+serviceID.String()+"&limit=2&cursor="+*page.Next)
	if second.Code != http.StatusOK || strings.Contains(second.Body.String(), `"external_channel_id":"1"`) {
		t.Fatalf("cursor response=%d %s", second.Code, second.Body.String())
	}
}

func TestPlatformChannelsQueryKeepsChannelRefRowsAndRequiresBothScopesAtRouter(t *testing.T) {
	serviceID := uuid.New()
	source := fakeBindingLister{service: finance.BindingService{ID: serviceID, ServiceType: "newapi", InstanceID: "newapi-prod", Environment: "production", Status: "active"}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/platforms/newapi/channels?service_id="+serviceID.String(), nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("platform", "newapi")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	request = request.WithContext(principal.WithPrincipal(request.Context(), principal.Principal{ID: "staff", Type: principal.TypeHuman, IdentityZone: "staff", Issuer: "issuer", Subject: "subject", Environment: "production"}))
	recorder := httptest.NewRecorder()
	ListPlatformChannelsHandler(source, fakeMetricListerForBinding{items: []ops.Observation{bindingObservation(serviceID.String())}}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"channel_ref"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPlatformChannelDateRangeRejectsPartialReversedAndOverlongWindows(t *testing.T) {
	cases := []struct {
		name    string
		from    string
		to      string
		wantErr bool
	}{
		{name: "partial from", from: "2026-08-01", wantErr: true},
		{name: "reversed", from: "2026-08-20", to: "2026-08-01", wantErr: true},
		{name: "over ninety two days", from: "2026-01-01", to: "2026-04-10", wantErr: true},
		{name: "inclusive ninety two days", from: "2026-08-01", to: "2026-10-31", wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseBusinessDayRange(tc.from, tc.to)
			if (err != nil) != tc.wantErr {
				t.Fatalf("range %q..%q error=%v, wantErr=%v", tc.from, tc.to, err, tc.wantErr)
			}
		})
	}
}
