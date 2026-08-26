package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

func TestProbesDoNotRequirePrincipal(t *testing.T) {
	h := testRouter(t, &fakeExecutor{}, nil)
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 应免鉴权返回 200, got %d", path, rec.Code)
		}
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	h := testRouter(t, &fakeExecutor{}, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nope", nil)
	devHeaders(req, "registry.read")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知路由应 404, got %d", rec.Code)
	}
}

func TestListActionsRequiresPrincipal(t *testing.T) {
	h := testRouter(t, &fakeExecutor{}, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/actions", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("无身份应 403, got %d", rec.Code)
	}
}

func TestListActionsMarksAdvancedControlLevelsAsBlocked(t *testing.T) {
	reg := action.NewRegistry()
	mk := func(id string, lvl action.RiskLevel) action.Definition {
		return action.Definition{
			ID: id, Version: "1", RiskLevel: lvl, Permission: "x.y.z",
			Environments:   []string{"development"},
			PrincipalTypes: []principal.Type{principal.TypeHuman},
		}
	}
	noop := func(context.Context, map[string]any) (any, error) { return nil, nil }
	for _, d := range []action.Definition{mk("a.b.read", action.L1), mk("a.b.manage", action.L3)} {
		if err := reg.Register(d, noop); err != nil {
			t.Fatal(err)
		}
	}
	res, _ := NewDevHeaderResolver("development")
	h := NewRouter(Deps{
		Logger: discardLogger(), Service: "platform-api", Environment: "development",
		DB: fakePinger{}, Resolver: res, Kernel: &fakeExecutor{}, ActionRegistry: reg,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/actions", nil)
	devHeaders(req, "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var got struct {
		Items []struct {
			ID            string `json:"id"`
			Executable    bool   `json:"executable"`
			BlockedReason string `json:"blocked_reason"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应非预期结构: %v (%s)", err, rec.Body.String())
	}
	if len(got.Items) != 2 {
		t.Fatalf("应有 2 条: %+v", got.Items)
	}
	for _, it := range got.Items {
		switch it.ID {
		case "a.b.read":
			if !it.Executable || it.BlockedReason != "" {
				t.Fatalf("L1 应可执行: %+v", it)
			}
		case "a.b.manage":
			if it.Executable || it.BlockedReason == "" {
				t.Fatalf("L3 应被标记为不可执行并给出原因: %+v", it)
			}
		}
	}
}
