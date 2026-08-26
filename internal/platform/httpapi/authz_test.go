package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

type emptyServices struct{}

func (emptyServices) ListServicesByEnvironment(context.Context, registry.Environment) ([]registry.Service, error) {
	return nil, nil
}

type emptyMetrics struct{}

func (emptyMetrics) ListByEnvironment(context.Context, string) ([]ops.Observation, error) {
	return nil, nil
}

func readRequest(t *testing.T, path, scopes string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	devHeaders(req, scopes)
	rec := httptest.NewRecorder()
	testRouterWithMetrics(t, nil, emptyServices{}, emptyMetrics{}).ServeHTTP(rec, req)
	return rec
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法错误体: %s", rec.Body.String())
	}
	return body.Error.Code
}

func TestReadEndpointsRequireScope(t *testing.T) {
	// 规格 §2.4：Query 也要权限检查。在接入真实运营数据前这是纸面要求，
	// 之后就是「有没有人能越权看到收入数字」。
	for _, path := range []string{"/api/v1/services", "/api/v1/metrics"} {
		rec := readRequest(t, path, "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s 无权限时 status = %d, want 403", path, rec.Code)
		}
		if c := errCode(t, rec); c != "PERMISSION_DENIED" {
			t.Fatalf("%s 无权限时 code = %s", path, c)
		}
	}
}

func TestReadScopesAreNotInterchangeable(t *testing.T) {
	// 两个 scope 分开授予才有意义——共用一个「读」权限的话，
	// 给人看服务清单就顺手给了收入数字
	cases := []struct{ path, scope string }{
		{"/api/v1/services", ops.ScopeRead},     // 拿 ops.read 读 registry
		{"/api/v1/metrics", registry.ScopeRead}, // 拿 registry.read 读 metrics
	}
	for _, c := range cases {
		rec := readRequest(t, c.path, c.scope)
		if rec.Code != http.StatusForbidden {
			t.Errorf("用 %s 访问 %s 应被拒绝, got %d", c.scope, c.path, rec.Code)
		}
	}

	// 正确的 scope 能过
	for _, c := range []struct{ path, scope string }{
		{"/api/v1/services", registry.ScopeRead},
		{"/api/v1/metrics", ops.ScopeRead},
	} {
		if rec := readRequest(t, c.path, c.scope); rec.Code != http.StatusOK {
			t.Errorf("用 %s 访问 %s 应放行, got %d: %s",
				c.scope, c.path, rec.Code, rec.Body.String())
		}
	}
}

func TestReadEndpointsRejectCrossEnvironment(t *testing.T) {
	// 之前的实现允许任意 environment 查询参数：一个 development 身份
	// 加个 ?environment=production 就能读生产。规格 §20.5 说生产权限不继承，
	// 那跨环境读取就得是显式授予的能力，不能是个查询参数。
	for _, c := range []struct{ path, scope string }{
		{"/api/v1/services", registry.ScopeRead},
		{"/api/v1/metrics", ops.ScopeRead},
	} {
		rec := readRequest(t, c.path+"?environment=production", c.scope)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s 跨环境读取应 403, got %d: %s", c.path, rec.Code, rec.Body.String())
		}
		if code := errCode(t, rec); code != "PERMISSION_DENIED" {
			t.Errorf("%s 跨环境读取 code = %s", c.path, code)
		}

		// 显式传自己的环境是允许的——这不是跨环境
		if rec := readRequest(t, c.path+"?environment=development", c.scope); rec.Code != http.StatusOK {
			t.Errorf("%s 显式传本环境应放行, got %d", c.path, rec.Code)
		}
	}
}

func TestInvalidEnvironmentIsBadRequestNotForbidden(t *testing.T) {
	// 拼错环境名是参数问题（400），不是权限问题（403）。混在一起的话，
	// 调用方会拿着 403 去查权限配置，其实只是把 "staging" 写成了 "stage"
	for _, c := range []struct{ path, scope string }{
		{"/api/v1/services", registry.ScopeRead},
		{"/api/v1/metrics", ops.ScopeRead},
	} {
		rec := readRequest(t, c.path+"?environment=stage", c.scope)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s 非法环境应 400, got %d: %s", c.path, rec.Code, rec.Body.String())
		}
		if code := errCode(t, rec); code != "INVALID_PARAMS" {
			t.Errorf("%s 非法环境 code = %s, want INVALID_PARAMS", c.path, code)
		}
	}
}
