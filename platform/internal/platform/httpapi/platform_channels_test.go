package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// XM-CHAN-FIELDS0: DTO tests for the v3 channel catalog fields exposed by
// ListPlatformChannelsHandler. The observation fixtures below use exactly
// the nested map[string]any shape connectors/sub2api and connectors/newapi
// produce (ToChannelDirectoryObservation / channelsObservation +
// catalogFields) — these tests exercise the httpapi decode
// (catalogRowsForService/applyCatalog), not the connector encode side
// (already covered by each connector's own tests).
//
// XM-CHAN-GROUP0 extended this same suite with one more field ("group",
// NewAPI-only — connectors/newapi.ChannelStatus.Group) rather than adding a
// parallel test file: it decodes through the exact same
// catalogRowsForService/applyCatalog path as every other v3 field, so it
// belongs in the same round-trip test, not a separate one.

// catalogObservation builds a "<service_type>.channels.status" observation
// whose single channel row carries the full v3 catalog shape, mirroring
// what a real connector's ToChannelDirectoryObservation/channelsObservation
// would produce for one fully-populated subscription-kind channel.
func catalogObservation(serviceType string) ops.Observation {
	now := time.Date(2026, 8, 29, 1, 0, 0, 0, time.UTC)
	reported := int64(1)
	return ops.Observation{
		MetricKey: serviceType + ".channels.status", Source: serviceType + "-prod", Environment: "production",
		ObservedAt: &now, SyncedAt: now, Status: ops.SyncOK, Value: map[string]any{
			"inventory_completeness": map[string]any{"complete": true, "truncated": false, "reported_count": reported, "fetched_count": int64(1), "evidence": "reported_count"},
			"coverage_partial":       false,
			"channels": []any{
				map[string]any{
					"channel_id": "acct-1", "name": "Claude Max 主账号", "status": "active", "currency": "USD",
					"kind": "subscription", "vendor": "anthropic",
					"capacity":   map[string]any{"used": int64(2), "limit": int64(5)},
					"scheduling": map[string]any{"enabled": true, "priority": int64(10)},
					"today": map[string]any{
						"requests": int64(842), "success_rate": 0.990588,
						"cost_minor_units": int64(1560), "currency": "USD", "scale": int(2),
					},
					"usage_window":        map[string]any{"used_ratio": 0.42, "resets_at": "2026-08-29T15:00:00Z"},
					"proxy":               "hk-residential-01",
					"rate_multiplier":     1.5,
					"upstream_multiplier": 1.1,
					"last_used_at":        "2026-08-29T00:55:00Z",
					"created_at":          "2026-01-01T00:00:00Z",
					"expires_at":          "2027-01-01T00:00:00Z",
					"group":               "default,vip",
				},
			},
		}, StalenessThresholdSeconds: 1800,
	}
}

// catalogObservationSparse builds an observation for a channel with none of
// the v3 catalog fields populated (only the pre-existing identity fields) —
// the shape a channel with nothing backing any new dimension would produce.
func catalogObservationSparse(serviceType string) ops.Observation {
	now := time.Date(2026, 8, 29, 1, 0, 0, 0, time.UTC)
	reported := int64(1)
	return ops.Observation{
		MetricKey: serviceType + ".channels.status", Source: serviceType + "-prod", Environment: "production",
		ObservedAt: &now, SyncedAt: now, Status: ops.SyncOK, Value: map[string]any{
			"inventory_completeness": map[string]any{"complete": true, "truncated": false, "reported_count": reported, "fetched_count": int64(1), "evidence": "reported_count"},
			"coverage_partial":       false,
			"channels": []any{
				map[string]any{"channel_id": "ch-bare", "name": "无目录字段的渠道", "status": "active", "currency": "USD"},
			},
		}, StalenessThresholdSeconds: 1800,
	}
}

type platformChannelsCatalogBody struct {
	Items []struct {
		ID                 string   `json:"id"`
		Name               string   `json:"name"`
		Kind               *string  `json:"kind"`
		Vendor             *string  `json:"vendor"`
		Status             *string  `json:"status"`
		RateMultiplier     *float64 `json:"rate_multiplier"`
		UpstreamMultiplier *float64 `json:"upstream_multiplier"`
		Proxy              *string  `json:"proxy"`
		LastUsedAt         *string  `json:"last_used_at"`
		CreatedAt          *string  `json:"created_at"`
		ExpiresAt          *string  `json:"expires_at"`
		Group              *string  `json:"group"`
		Capacity           *struct {
			Used  *int64 `json:"used"`
			Limit *int64 `json:"limit"`
		} `json:"capacity"`
		Scheduling *struct {
			Enabled  *bool  `json:"enabled"`
			Priority *int64 `json:"priority"`
		} `json:"scheduling"`
		Today *struct {
			Requests    *int64   `json:"requests"`
			SuccessRate *float64 `json:"success_rate"`
			CostMinor   *string  `json:"cost_minor"`
			Currency    *string  `json:"currency"`
			Scale       *int     `json:"scale"`
		} `json:"today"`
		UsageWindow *struct {
			UsedRatio *float64 `json:"used_ratio"`
			ResetsAt  *string  `json:"resets_at"`
		} `json:"usage_window"`
	} `json:"items"`
}

func decodeChannelsBody(t *testing.T, rec *httptest.ResponseRecorder) platformChannelsCatalogBody {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body platformChannelsCatalogBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	return body
}

func callChannelsQuery(t *testing.T, platform string, source PlatformChannelBindingLister, metrics MetricLister, serviceID uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/platforms/"+platform+"/channels?service_id="+serviceID.String(), nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("platform", platform)
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
	request = request.WithContext(principal.WithPrincipal(request.Context(), principal.Principal{
		ID: "staff", Type: principal.TypeHuman, IdentityZone: "staff", Issuer: "issuer", Subject: "subject", Environment: "production",
	}))
	recorder := httptest.NewRecorder()
	ListPlatformChannelsHandler(source, metrics).ServeHTTP(recorder, request)
	return recorder
}

// TestPlatformChannelsQueryExposesV3CatalogFields is the field-by-field
// round trip: every XM-CHAN-FIELDS0 field the connector observation carries
// must come out under exactly the JSON name the product-owner spec names
// (chanmerge codes against these), including the cost_minor
// decimal-string conversion (money never rides a bare JSON number here,
// see amountString).
func TestPlatformChannelsQueryExposesV3CatalogFields(t *testing.T) {
	serviceID := uuid.New()
	source := fakeBindingLister{service: finance.BindingService{ID: serviceID, ServiceType: "sub2api", InstanceID: "sub2api-prod", Environment: "production", Status: "active"}}
	rec := callChannelsQuery(t, "sub2api", source, fakeMetricListerForBinding{items: []ops.Observation{catalogObservation("sub2api")}}, serviceID)
	body := decodeChannelsBody(t, rec)
	if len(body.Items) != 1 {
		t.Fatalf("items=%d, want 1: %s", len(body.Items), rec.Body.String())
	}
	item := body.Items[0]

	if item.ID != "acct-1" {
		t.Fatalf("id = %q, want acct-1 (upstream's own account id)", item.ID)
	}
	if item.Kind == nil || *item.Kind != "subscription" {
		t.Fatalf("kind = %v, want subscription", item.Kind)
	}
	if item.Vendor == nil || *item.Vendor != "anthropic" {
		t.Fatalf("vendor = %v, want anthropic", item.Vendor)
	}
	if item.Status == nil || *item.Status != "active" {
		t.Fatalf("status = %v, want active", item.Status)
	}
	if item.Capacity == nil || item.Capacity.Used == nil || *item.Capacity.Used != 2 ||
		item.Capacity.Limit == nil || *item.Capacity.Limit != 5 {
		t.Fatalf("capacity = %+v, want used=2 limit=5", item.Capacity)
	}
	if item.Scheduling == nil || item.Scheduling.Enabled == nil || !*item.Scheduling.Enabled ||
		item.Scheduling.Priority == nil || *item.Scheduling.Priority != 10 {
		t.Fatalf("scheduling = %+v, want enabled=true priority=10", item.Scheduling)
	}
	if item.Today == nil {
		t.Fatal("today is nil, want populated")
	}
	if item.Today.Requests == nil || *item.Today.Requests != 842 {
		t.Fatalf("today.requests = %v, want 842", item.Today.Requests)
	}
	if item.Today.CostMinor == nil || *item.Today.CostMinor != "1560" {
		t.Fatalf("today.cost_minor = %v, want the decimal string \"1560\" (not a bare JSON number)", item.Today.CostMinor)
	}
	if item.Today.Currency == nil || *item.Today.Currency != "USD" || item.Today.Scale == nil || *item.Today.Scale != 2 {
		t.Fatalf("today.currency/scale = %v/%v, want USD/2", item.Today.Currency, item.Today.Scale)
	}
	if item.UsageWindow == nil || item.UsageWindow.UsedRatio == nil || *item.UsageWindow.UsedRatio != 0.42 {
		t.Fatalf("usage_window.used_ratio = %v, want 0.42", item.UsageWindow)
	}
	if item.UsageWindow.ResetsAt == nil || *item.UsageWindow.ResetsAt != "2026-08-29T15:00:00Z" {
		t.Fatalf("usage_window.resets_at = %v", item.UsageWindow.ResetsAt)
	}
	if item.Proxy == nil || *item.Proxy != "hk-residential-01" {
		t.Fatalf("proxy = %v, want hk-residential-01 (a label, never a credentialed URL)", item.Proxy)
	}
	if item.RateMultiplier == nil || *item.RateMultiplier != 1.5 {
		t.Fatalf("rate_multiplier = %v, want 1.5", item.RateMultiplier)
	}
	if item.UpstreamMultiplier == nil || *item.UpstreamMultiplier != 1.1 {
		t.Fatalf("upstream_multiplier = %v, want 1.1", item.UpstreamMultiplier)
	}
	if item.LastUsedAt == nil || *item.LastUsedAt != "2026-08-29T00:55:00Z" {
		t.Fatalf("last_used_at = %v", item.LastUsedAt)
	}
	if item.CreatedAt == nil || *item.CreatedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("created_at = %v", item.CreatedAt)
	}
	if item.ExpiresAt == nil || *item.ExpiresAt != "2027-01-01T00:00:00Z" {
		t.Fatalf("expires_at = %v", item.ExpiresAt)
	}
	if item.Group == nil || *item.Group != "default,vip" {
		t.Fatalf("group = %v, want default,vip", item.Group)
	}
}

// TestPlatformChannelsQueryLeavesCatalogFieldsNilWhenAbsent: a channel with
// none of the v3 dimensions in its observation row must come back with
// every new field JSON-null, never a fabricated zero, empty string, or
// empty object — the whole point of "each field null when the upstream has
// no such value" (task's hard constraint).
func TestPlatformChannelsQueryLeavesCatalogFieldsNilWhenAbsent(t *testing.T) {
	serviceID := uuid.New()
	source := fakeBindingLister{service: finance.BindingService{ID: serviceID, ServiceType: "newapi", InstanceID: "newapi-prod", Environment: "production", Status: "active"}}
	rec := callChannelsQuery(t, "newapi", source, fakeMetricListerForBinding{items: []ops.Observation{catalogObservationSparse("newapi")}}, serviceID)
	body := decodeChannelsBody(t, rec)
	if len(body.Items) != 1 {
		t.Fatalf("items=%d, want 1: %s", len(body.Items), rec.Body.String())
	}
	item := body.Items[0]
	if item.ID != "ch-bare" {
		t.Fatalf("id = %q, want ch-bare", item.ID)
	}
	if item.Status == nil || *item.Status != "active" {
		t.Fatalf("status = %v, want active (this one IS present in the fixture)", item.Status)
	}
	if item.Kind != nil || item.Vendor != nil || item.Capacity != nil || item.Scheduling != nil ||
		item.Today != nil || item.UsageWindow != nil || item.Proxy != nil ||
		item.RateMultiplier != nil || item.UpstreamMultiplier != nil ||
		item.LastUsedAt != nil || item.CreatedAt != nil || item.ExpiresAt != nil ||
		item.Group != nil {
		t.Fatalf("expected every v3 field except status to be JSON null for a bare channel row, got %+v", item)
	}
	// Also confirm this is genuinely a pre-existing-field regression guard:
	// the old "channel_ref"/"name" shape must still be there untouched.
	if item.Name != "无目录字段的渠道" {
		t.Fatalf("name = %q", item.Name)
	}
}

// TestCatalogRowsForServiceCoercesBothNumericForms confirms the httpapi
// decode is defensive about int64 vs float64 (an observation kept in memory
// by the Fake path carries native Go int64/int, while one round-tripped
// through real JSON storage decodes numbers as float64 — see
// inventoryForService's identical dual-type handling for reported_count).
func TestCatalogRowsForServiceCoercesBothNumericForms(t *testing.T) {
	service := finance.BindingService{ServiceType: "sub2api", InstanceID: "sub2api-prod"}
	makeObservation := func(used, limit, priority any) []ops.Observation {
		return []ops.Observation{{
			MetricKey: "sub2api.channels.status", Source: "sub2api-prod",
			Value: map[string]any{"channels": []any{
				map[string]any{
					"channel_id": "x", "capacity": map[string]any{"used": used, "limit": limit},
					"scheduling": map[string]any{"enabled": true, "priority": priority},
				},
			}},
		}}
	}
	forNativeInt := catalogRowsForService(makeObservation(int64(2), int64(5), int64(10)), service)
	forFloat := catalogRowsForService(makeObservation(float64(2), float64(5), float64(10)), service)
	for name, got := range map[string]platformChannelCatalog{"int64": forNativeInt["x"], "float64": forFloat["x"]} {
		if got.capacityUsed == nil || *got.capacityUsed != 2 || got.capacityLimit == nil || *got.capacityLimit != 5 {
			t.Fatalf("%s form: capacity = used=%v limit=%v, want 2/5", name, got.capacityUsed, got.capacityLimit)
		}
		if got.schedulingPriority == nil || *got.schedulingPriority != 10 {
			t.Fatalf("%s form: scheduling priority = %v, want 10", name, got.schedulingPriority)
		}
	}
}
