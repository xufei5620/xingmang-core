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

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

type fakeServiceLister struct {
	gotEnv registry.Environment
	items  []registry.Service
	err    error
}

func (f *fakeServiceLister) ListServicesByEnvironment(
	_ context.Context, env registry.Environment,
) ([]registry.Service, error) {
	f.gotEnv = env
	return f.items, f.err
}

func TestListServicesDefaultsToPrincipalEnvironment(t *testing.T) {
	lister := &fakeServiceLister{items: []registry.Service{{
		ID: uuid.New(), ServiceType: "sub2api", InstanceID: "sub2api-dev",
		Environment: registry.EnvDevelopment, Endpoint: "https://api.solov.cc",
		Owner: "platform", Status: registry.ServiceActive,
	}}}
	h := testRouter(t, &fakeExecutor{}, lister)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	devHeaders(req, "registry.read")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	// 未指定 environment 时用 Principal 的环境，不默认生产
	if lister.gotEnv != registry.EnvDevelopment {
		t.Fatalf("environment = %q, want development", lister.gotEnv)
	}
	var got struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应非预期结构: %v (%s)", err, rec.Body.String())
	}
	if len(got.Items) != 1 || got.Items[0]["instance_id"] != "sub2api-dev" {
		t.Fatalf("items = %+v", got.Items)
	}
	// 从未采集时 observed_at 与 stale_seconds 必须是 null，
	// 前端据此显示「未初始化」而不是裸数字（规格 §9.1）
	if got.Items[0]["observed_at"] != nil || got.Items[0]["stale_seconds"] != nil {
		t.Fatalf("未采集时应为 null: %+v", got.Items[0])
	}
}

func TestListServicesComputesStaleSeconds(t *testing.T) {
	observed := time.Now().UTC().Add(-90 * time.Second)
	lister := &fakeServiceLister{items: []registry.Service{{
		ID: uuid.New(), ServiceType: "sub2api", InstanceID: "sub2api-dev",
		Environment: registry.EnvDevelopment, Endpoint: "https://api.solov.cc",
		Owner: "platform", Status: registry.ServiceActive,
		SourceWatermark: "wm-1", ObservedAt: &observed,
	}}}
	h := testRouter(t, &fakeExecutor{}, lister)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	devHeaders(req, "registry.read")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var got struct {
		Items []struct {
			ObservedAt   *string `json:"observed_at"`
			StaleSeconds *int64  `json:"stale_seconds"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应非预期结构: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].StaleSeconds == nil {
		t.Fatalf("stale_seconds 应被计算: %+v", got.Items)
	}
	if s := *got.Items[0].StaleSeconds; s < 85 || s > 100 {
		t.Fatalf("stale_seconds = %d, 期望约 90", s)
	}
	if got.Items[0].ObservedAt == nil || !strings.Contains(*got.Items[0].ObservedAt, "T") {
		t.Fatalf("observed_at 应为 RFC3339: %+v", got.Items[0].ObservedAt)
	}
}

func TestListServicesRejectsUnknownEnvironment(t *testing.T) {
	h := testRouter(t, &fakeExecutor{}, &fakeServiceLister{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/services?environment=prod", nil)
	devHeaders(req, "registry.read")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法环境应 400, got %d", rec.Code)
	}
}

func TestListServicesMapsStoreError(t *testing.T) {
	h := testRouter(t, &fakeExecutor{}, &fakeServiceLister{
		err: errors.New("dial tcp 10.0.0.5:5432: connect: connection refused"),
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	devHeaders(req, "registry.read")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Fatalf("响应泄漏内网地址: %s", rec.Body.String())
	}
}
