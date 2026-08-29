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

	connusers "github.com/xufei5620/xingmang-platform/connectors/platformusers"
	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	platformusers "github.com/xufei5620/xingmang-platform/internal/platform/platformusers"
)

type fakeDetailQuerier struct {
	detail connusers.UserDetail
	err    error
	called bool
}

func (f *fakeDetailQuerier) Get(_ context.Context, in platformusers.DetailInput) (connusers.UserDetail, error) {
	f.called = true
	f.detail.Ref = connusers.UserRef{Platform: in.Platform, ID: in.UserID}
	return f.detail, f.err
}

func serveDetail(t *testing.T, q PlatformUserDetailsQuerier, target string) *httptest.ResponseRecorder {
	t.Helper()
	router := chi.NewRouter()
	router.Get("/platforms/{platform}/users/{userID}", GetPlatformUserHandler(q))
	req := httptest.NewRequest(http.MethodGet, target, nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func TestGetPlatformUserRouteRejectsNonCanonicalBeforeQuery(t *testing.T) {
	q := &fakeDetailQuerier{}
	rec := serveDetail(t, q, "/platforms/sub2api/users/raw-id")
	if rec.Code != http.StatusNotFound || q.called {
		t.Fatalf("status=%d called=%v", rec.Code, q.called)
	}
}

func TestGetPlatformUserReturnsExplicitSafeDTO(t *testing.T) {
	now := time.Date(2026, 8, 29, 1, 2, 3, 0, time.UTC)
	q := &fakeDetailQuerier{detail: connusers.UserDetail{User: connusers.User{ID: "u_1", Username: "张伟", EmailMasked: "张***@example.com", Balance: connusers.KnownAmount(100, "CNY")}, Period: connusers.Period{Day: "2026-08-29", Granularity: connusers.GranularityDay, From: "2026-08-29", To: "2026-08-29"}, Snapshot: connusers.EvidenceSnapshot{ObservedAt: now, Source: "sub2api-fake", Watermark: "wm"}}}
	rec := serveDetail(t, q, "/platforms/sub2api/users/u-755f31")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"capabilities"`) || strings.Contains(rec.Body.String(), "owner") {
		t.Fatalf("body=%s", rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["ref"].(map[string]any)["id"] != "u_1" {
		t.Fatalf("body=%v", body)
	}
}

func TestGetPlatformUserErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   action.Code
		status int
	}{
		{connusers.ErrNotFound, action.CodeNotRegistered, http.StatusNotFound},
		{connusers.ErrLookupIncomplete, action.CodeExecutionFailed, http.StatusBadGateway},
	} {
		q := &fakeDetailQuerier{err: tc.err}
		rec := serveDetail(t, q, "/platforms/sub2api/users/u-755f31")
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"code":"`+string(tc.code)+`"`) {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
}
