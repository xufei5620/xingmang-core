package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

func TestDevResolverRefusedInProduction(t *testing.T) {
	// 开发期身份注入器绝不能在生产启用——这是整个授权体系的地基
	if _, err := NewDevHeaderResolver("production"); err == nil {
		t.Fatal("生产环境必须拒绝创建开发期 Principal 注入器")
	}
	for _, env := range []string{"development", "staging"} {
		if _, err := NewDevHeaderResolver(env); err != nil {
			t.Fatalf("%s 环境应允许: %v", env, err)
		}
	}
}

func TestDevResolverParsesHeaders(t *testing.T) {
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Dev-Principal-ID", "staff_alice")
	req.Header.Set("X-Dev-Principal-Type", "HUMAN")
	req.Header.Set("X-Dev-Scopes", "registry.read, registry.service.manage")

	p, err := res.Resolve(req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p.ID != "staff_alice" || p.Type != principal.TypeHuman {
		t.Fatalf("解析错误: %+v", p)
	}
	if !p.HasScope("registry.read") || !p.HasScope("registry.service.manage") {
		t.Fatalf("scope 解析错误: %+v", p.Scopes)
	}
	// Environment 必须来自服务配置，不能被 Header 指定（否则调用方可自称生产）
	if p.Environment != "development" {
		t.Fatalf("Environment 应取服务配置, got %q", p.Environment)
	}
}

func TestDevResolverRejectsBadInput(t *testing.T) {
	res, _ := NewDevHeaderResolver("development")
	for name, set := range map[string]func(*http.Request){
		"缺 ID":   func(r *http.Request) { r.Header.Set("X-Dev-Principal-Type", "HUMAN") },
		"缺 Type": func(r *http.Request) { r.Header.Set("X-Dev-Principal-ID", "a") },
		"非法 Type": func(r *http.Request) {
			r.Header.Set("X-Dev-Principal-ID", "a")
			r.Header.Set("X-Dev-Principal-Type", "ROBOT")
		},
	} {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		set(req)
		if _, err := res.Resolve(req); err == nil {
			t.Fatalf("%s：应被拒绝", name)
		}
	}
}

func TestRequirePrincipalInjectsAndRejects(t *testing.T) {
	res, _ := NewDevHeaderResolver("development")
	var seen principal.Principal
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := principal.FromContext(r.Context())
		if !ok {
			t.Fatal("handler 应能从 ctx 取到 Principal")
		}
		seen = p
		WriteJSON(w, http.StatusOK, nil)
	})
	h := RequestID(RequirePrincipal(res)(inner))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Dev-Principal-ID", "staff_alice")
	req.Header.Set("X-Dev-Principal-Type", "HUMAN")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || seen.ID != "staff_alice" {
		t.Fatalf("status=%d principal=%+v", rec.Code, seen)
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("无身份应 403, got %d", rec2.Code)
	}
	if strings.Contains(rec2.Body.String(), "X-Dev-Principal") {
		t.Fatalf("响应不应回显内部 Header 约定: %s", rec2.Body.String())
	}
}
