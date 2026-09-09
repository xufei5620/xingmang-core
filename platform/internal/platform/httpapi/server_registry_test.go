package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/server"
)

type fakeServerRegistry struct {
	gotEnv string

	assets    []server.Asset
	suppliers []server.Supplier
	domains   []server.ServerDomain
	notes     []server.ServiceNote

	err error
}

func (f *fakeServerRegistry) ListAssetsByEnvironment(_ context.Context, env string) ([]server.Asset, error) {
	f.gotEnv = env
	return f.assets, f.err
}

func (f *fakeServerRegistry) ListSuppliersByEnvironment(_ context.Context, env string) ([]server.Supplier, error) {
	f.gotEnv = env
	return f.suppliers, f.err
}

func (f *fakeServerRegistry) ListServerDomainsByEnvironment(_ context.Context, env string) ([]server.ServerDomain, error) {
	f.gotEnv = env
	return f.domains, f.err
}

func (f *fakeServerRegistry) ListServiceNotesByEnvironment(_ context.Context, env string) ([]server.ServiceNote, error) {
	f.gotEnv = env
	return f.notes, f.err
}

func serverRegistryRouter(t *testing.T, reg *fakeServerRegistry) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger:             discardLogger(),
		Service:            "platform-api",
		Environment:        "development",
		DB:                 fakePinger{},
		Resolver:           res,
		Kernel:             nil,
		ActionRegistry:     action.NewRegistry(),
		ServerAssets:       reg,
		ServerSuppliers:    reg,
		ServerDomains:      reg,
		ServerServiceNotes: reg,
	})
}

func devHeaderRequest(method, path, scopes string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	devHeaders(req, scopes)
	return req
}

func sampleServerAsset() server.Asset {
	now := time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC)
	cost := int64(9990)
	vcpu, mem, disk := 4, 8, 160
	return server.Asset{
		ID:                    uuid.New(),
		Hostname:              "srv-sin-01",
		IPAddresses:           []string{"203.0.113.9", "10.0.0.5"},
		Datacenter:            "SIN",
		SupplierID:            uuid.New(),
		VCPU:                  &vcpu,
		MemoryGB:              &mem,
		DiskGB:                &disk,
		Purpose:               "sub2api relay",
		Status:                server.AssetActive,
		MonthlyCostMinorUnits: &cost,
		Currency:              "USD",
		BillingCycle:          server.BillingMonthly,
		ExpiresAt:             now.AddDate(0, 0, 20),
		Notes:                 "primary relay node",
		Environment:           "development",
		CreatedAt:             now,
		UpdatedAt:             now,
	}
}

func TestListServerAssetsHandlerReturnsItemsAndRespectsScope(t *testing.T) {
	reg := &fakeServerRegistry{assets: []server.Asset{sampleServerAsset()}}
	h := serverRegistryRouter(t, reg)

	t.Run("registry.read 授权时返回列表", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, devHeaderRequest(http.MethodGet, "/api/v1/servers/assets", "registry.read"))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
		}
		if reg.gotEnv != "development" {
			t.Fatalf("gotEnv = %q, want development", reg.gotEnv)
		}
		var body struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		if len(body.Items) != 1 {
			t.Fatalf("items 长度 = %d, want 1", len(body.Items))
		}
		item := body.Items[0]
		if item["hostname"] != "srv-sin-01" {
			t.Fatalf("hostname = %v", item["hostname"])
		}
		// 金额必须是字符串（宪法 13 条：JSON 数字会经 float64，2^53 以上截断）
		if _, ok := item["monthly_cost_minor_units"].(string); !ok {
			t.Fatalf("monthly_cost_minor_units 应为字符串, got %#v", item["monthly_cost_minor_units"])
		}
	})

	t.Run("缺 registry.read 时 403", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, devHeaderRequest(http.MethodGet, "/api/v1/servers/assets", ""))
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403, body = %s", w.Code, w.Body.String())
		}
	})
}

// TestServerAssetItemOmitsCostWhenUnset：未登记月付成本必须序列化成
// null，不能悄悄变成 "0"——那会被读成「免费」而不是「没登记」。
func TestServerAssetItemOmitsCostWhenUnset(t *testing.T) {
	a := sampleServerAsset()
	a.MonthlyCostMinorUnits = nil
	a.Currency = ""
	a.SupplierID = uuid.Nil
	a.ExpiresAt = time.Time{}

	reg := &fakeServerRegistry{assets: []server.Asset{a}}
	h := serverRegistryRouter(t, reg)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, devHeaderRequest(http.MethodGet, "/api/v1/servers/assets", "registry.read"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	item := body.Items[0]
	if item["monthly_cost_minor_units"] != nil {
		t.Fatalf("未登记成本应序列化为 null, got %#v", item["monthly_cost_minor_units"])
	}
	if item["supplier_id"] != "" {
		t.Fatalf("未登记供应商应为空串, got %#v", item["supplier_id"])
	}
	if item["expires_at"] != "" {
		t.Fatalf("未登记到期日应为空串, got %#v", item["expires_at"])
	}
}

func TestListServerSuppliersHandler(t *testing.T) {
	now := time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC)
	reg := &fakeServerRegistry{suppliers: []server.Supplier{{
		ID: uuid.New(), Name: "Vultr", Website: "https://vultr.com",
		Environment: "development", CreatedAt: now, UpdatedAt: now,
	}}}
	h := serverRegistryRouter(t, reg)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, devHeaderRequest(http.MethodGet, "/api/v1/servers/suppliers", "registry.read"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0]["name"] != "Vultr" {
		t.Fatalf("items = %#v", body.Items)
	}
}

func TestListServerDomainsHandler(t *testing.T) {
	now := time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC)
	reg := &fakeServerRegistry{domains: []server.ServerDomain{{
		ID: uuid.New(), DomainName: "console.example.com", CertSource: server.CertACME,
		Environment: "development", CreatedAt: now, UpdatedAt: now,
	}}}
	h := serverRegistryRouter(t, reg)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, devHeaderRequest(http.MethodGet, "/api/v1/servers/domains", "registry.read"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0]["domain_name"] != "console.example.com" {
		t.Fatalf("items = %#v", body.Items)
	}
}

func TestListServerServiceNotesHandler(t *testing.T) {
	now := time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC)
	port := 8080
	reg := &fakeServerRegistry{notes: []server.ServiceNote{{
		ID: uuid.New(), ServerID: uuid.New(), ServiceName: "api", ServiceKind: server.ServiceContainer,
		Port: &port, CreatedAt: now, UpdatedAt: now,
	}}}
	h := serverRegistryRouter(t, reg)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, devHeaderRequest(http.MethodGet, "/api/v1/servers/service-notes", "registry.read"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0]["service_name"] != "api" {
		t.Fatalf("items = %#v", body.Items)
	}
}
