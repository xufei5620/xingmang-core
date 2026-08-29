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

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/savedviews"
)

type fakeSavedViewLister struct {
	items []savedviews.SavedView
	err   error
	owner savedviews.Owner
	table string
}

func (f *fakeSavedViewLister) List(_ context.Context, owner savedviews.Owner, table string) ([]savedviews.SavedView, error) {
	f.owner, f.table = owner, table
	return f.items, f.err
}

func savedViewPrincipal(typ principal.Type) principal.Principal {
	return principal.Principal{
		ID: "staff_alice", Type: typ, IdentityZone: "staff",
		Issuer: "https://auth.example/realms/staff", Subject: "sub-alice",
		Environment: "production", Scopes: []string{savedviews.ScopeManage},
	}
}

func savedViewItem() savedviews.SavedView {
	now := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
	return savedviews.SavedView{
		ID:       uuid.MustParse("3c6d7c6f-5eec-4db4-8a23-55754aa50ceb"),
		TableKey: "platform.sub2api.channels", Name: "需关注",
		State: savedviews.StateV1{
			SchemaVersion: 1, Query: "openai", Filters: map[string]string{"status": "需关注"},
			Sort:           &savedviews.Sort{ColumnID: "grossProfit", Direction: savedviews.SortDesc},
			KnownColumns:   []string{"name", "status", "grossProfit"},
			VisibleColumns: []string{"name", "status"}, Density: savedviews.DensityCompact,
		},
		StateHash: strings.Repeat("a", 64), CreatedAt: now, UpdatedAt: now,
	}
}

func callSavedViews(t *testing.T, store SavedViewLister, rawURL string, p principal.Principal) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, rawURL, nil)
	req = req.WithContext(principal.WithPrincipal(req.Context(), p))
	recorder := httptest.NewRecorder()
	ListSavedViewsHandler(store).ServeHTTP(recorder, req)
	return recorder
}

func TestListSavedViewsReturnsExplicitOwnerSafeDTO(t *testing.T) {
	store := &fakeSavedViewLister{items: []savedviews.SavedView{savedViewItem()}}
	recorder := callSavedViews(t, store, "/api/v1/ui/saved-views?table_key=platform.sub2api.channels", savedViewPrincipal(principal.TypeHuman))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if store.owner.Subject != "sub-alice" || store.owner.Environment != "production" || store.table != "platform.sub2api.channels" {
		t.Fatalf("store call = %#v / %q", store.owner, store.table)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	encoded := recorder.Body.String()
	for _, forbidden := range []string{"owner_issuer", "owner_subject", "identity_zone", "environment", "state_hash"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("response leaked %s: %s", forbidden, encoded)
		}
	}
	for _, required := range []string{`"table_key"`, `"state_version"`, `"schema_version"`, `"columns"`, `"updated_at"`} {
		if !strings.Contains(encoded, required) {
			t.Fatalf("response missing %s: %s", required, encoded)
		}
	}
}

func TestListSavedViewsRejectsMissingOrClientBoundaryParams(t *testing.T) {
	for _, rawURL := range []string{
		"/api/v1/ui/saved-views",
		"/api/v1/ui/saved-views?table_key=platform.sub2api.channels&environment=staging",
		"/api/v1/ui/saved-views?table_key=platform.sub2api.channels&owner_subject=attacker",
	} {
		recorder := callSavedViews(t, &fakeSavedViewLister{}, rawURL, savedViewPrincipal(principal.TypeHuman))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", rawURL, recorder.Code, recorder.Body.String())
		}
	}
}

func TestListSavedViewsRejectsMachinePrincipal(t *testing.T) {
	for _, typ := range []principal.Type{principal.TypeService, principal.TypeAI, principal.TypeServerAgent} {
		recorder := callSavedViews(t, &fakeSavedViewLister{}, "/api/v1/ui/saved-views?table_key=global.audit.events", savedViewPrincipal(typ))
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s status=%d body=%s", typ, recorder.Code, recorder.Body.String())
		}
	}
}

func TestSavedViewRouteRequiresDedicatedScope(t *testing.T) {
	resolver, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeSavedViewLister{items: []savedviews.SavedView{}}
	handler := NewRouter(Deps{
		Logger: discardLogger(), Service: "platform-api", Environment: "development",
		DB: fakePinger{}, Resolver: resolver, SavedViews: store,
	})
	for _, tc := range []struct {
		scopes string
		want   int
	}{
		{"registry.read,ops.read", http.StatusForbidden},
		{savedviews.ScopeManage, http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/ui/saved-views?table_key=global.audit.events", nil)
		devHeaders(req, tc.scopes)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != tc.want {
			t.Fatalf("scopes=%q status=%d body=%s", tc.scopes, recorder.Code, recorder.Body.String())
		}
	}
}
