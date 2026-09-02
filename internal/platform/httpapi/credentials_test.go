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

	"github.com/xufei5620/xingmang-platform/internal/platform/credentials"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

type fakeCredentialQuerier struct {
	items   []credentials.Metadata
	configs []credentials.ConnectorConfig
	err     error
	env     string
}

func (f *fakeCredentialQuerier) List(_ context.Context, env string) ([]credentials.Metadata, error) {
	f.env = env
	return f.items, f.err
}

func (f *fakeCredentialQuerier) ListConnectorConfigs(_ context.Context, env string) ([]credentials.ConnectorConfig, error) {
	f.env = env
	return f.configs, f.err
}

func credentialPrincipal(scopes ...string) principal.Principal {
	return principal.Principal{
		ID: "staff_alice", Type: principal.TypeHuman, IdentityZone: "staff",
		Issuer: "https://auth.example/realms/staff", Subject: "sub-alice",
		Environment: "staging", Scopes: scopes,
	}
}

func credentialFixtures() []credentials.Metadata {
	at := time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC)
	revokedAt := at.Add(time.Hour)
	return []credentials.Metadata{
		{
			Ref: "secret://sub2api-prod/read-token", Scope: "sub2api-prod", Name: "read-token",
			Environment: "staging", Fingerprint: "ba7816bf8f01cfea", Version: 2,
			UpdatedAt: at, UpdatedBy: "staff_alice", Available: true,
		},
		{
			Ref: "secret://newapi/readonly-token", Scope: "newapi", Name: "readonly-token",
			Environment: "staging", Fingerprint: "0123456789abcdef", Version: 1,
			UpdatedAt: at, UpdatedBy: "staff_bob", RevokedAt: &revokedAt, Available: false,
		},
		{
			// 登记了但文件丢了：available=false，expected 里不算 configured
			Ref: "secret://newapi/revenue-db", Scope: "newapi", Name: "revenue-db",
			Environment: "staging", Fingerprint: "fedcba9876543210", Version: 1,
			UpdatedAt: at, UpdatedBy: "staff_bob", Available: false,
		},
	}
}

func callCredentials(t *testing.T, h http.Handler, rawURL string, p principal.Principal) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, rawURL, nil)
	req = req.WithContext(principal.WithPrincipal(req.Context(), p))
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, req)
	return recorder
}

func TestListCredentialsReturnsMetadataOnly(t *testing.T) {
	store := &fakeCredentialQuerier{items: credentialFixtures()}
	recorder := callCredentials(t, ListCredentialsHandler(store), "/api/v1/credentials", credentialPrincipal(credentials.ScopeManage))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if store.env != "staging" {
		t.Fatalf("环境应来自 Principal, got %q", store.env)
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 3 {
		t.Fatalf("items = %#v", body.Items)
	}
	first := body.Items[0]
	for key, want := range map[string]any{
		"credential_ref": "secret://sub2api-prod/read-token", "scope": "sub2api-prod",
		"updated_at": "2026-08-30T01:02:03Z", "fingerprint": "ba7816bf8f01cfea",
		"version": float64(2), "available": true, "revoked": false,
	} {
		if first[key] != want {
			t.Fatalf("items[0].%s = %#v, want %#v", key, first[key], want)
		}
	}
	if len(first) != 7 {
		t.Fatalf("响应字段应恰好是契约里的 7 个, got %#v", first)
	}
	if body.Items[1]["revoked"] != true || body.Items[1]["available"] != false {
		t.Fatalf("已吊销项 = %#v", body.Items[1])
	}
	encoded := recorder.Body.String()
	for _, forbidden := range []string{"secret_value", "value", "environment", "updated_by", "revoked_at"} {
		if strings.Contains(encoded, `"`+forbidden+`"`) {
			t.Fatalf("response leaked %s: %s", forbidden, encoded)
		}
	}
}

func TestListCredentialsEnvironmentRules(t *testing.T) {
	store := &fakeCredentialQuerier{items: credentialFixtures()}
	p := credentialPrincipal(credentials.ScopeManage)
	if rec := callCredentials(t, ListCredentialsHandler(store), "/api/v1/credentials?environment=staging", p); rec.Code != http.StatusOK {
		t.Fatalf("同环境应 200, got %d %s", rec.Code, rec.Body.String())
	}
	if rec := callCredentials(t, ListCredentialsHandler(store), "/api/v1/credentials?environment=production", p); rec.Code != http.StatusForbidden {
		t.Fatalf("跨环境应 403, got %d %s", rec.Code, rec.Body.String())
	}
	if rec := callCredentials(t, ListCredentialsHandler(store), "/api/v1/credentials?environment=stage", p); rec.Code != http.StatusBadRequest {
		t.Fatalf("拼错环境应 400, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestListCredentialsStoreFailureIsSafe(t *testing.T) {
	store := &fakeCredentialQuerier{err: errors.New("pq: relation core.credential_ref does not exist at /var/lib/pg")}
	rec := callCredentials(t, ListCredentialsHandler(store), "/api/v1/credentials", credentialPrincipal(credentials.ScopeManage))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("仓储失败应 502, got %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "relation") || strings.Contains(rec.Body.String(), "/var/lib") {
		t.Fatalf("响应泄漏了底层错误: %s", rec.Body.String())
	}
}

func TestListExpectedCredentialsMarksConfigured(t *testing.T) {
	store := &fakeCredentialQuerier{items: credentialFixtures()}
	rec := callCredentials(t, ListExpectedCredentialsHandler(store), "/api/v1/credentials/expected", credentialPrincipal(credentials.ScopeManage))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	expected := credentials.ExpectedRefs()
	if len(body.Items) != len(expected) {
		t.Fatalf("items = %#v", body.Items)
	}
	configured := map[string]bool{}
	for i, item := range body.Items {
		if item["credential_ref"] != expected[i].Ref || item["platform"] != expected[i].Platform || item["purpose"] != expected[i].Purpose {
			t.Fatalf("items[%d] = %#v, want %#v", i, item, expected[i])
		}
		if len(item) != 4 {
			t.Fatalf("响应字段应恰好是契约里的 4 个, got %#v", item)
		}
		configured[item["credential_ref"].(string)] = item["configured"] == true
	}
	if !configured["secret://sub2api-prod/read-token"] {
		t.Fatal("已登记且文件可读的应 configured=true")
	}
	if configured["secret://newapi/readonly-token"] {
		t.Fatal("已吊销的应 configured=false")
	}
	if configured["secret://newapi/revenue-db"] {
		t.Fatal("文件不可读的应 configured=false")
	}
	if configured["secret://archive/minio-runtime"] || configured["secret://archive/minio-kms"] {
		t.Fatal("未登记的应 configured=false")
	}
}

func TestListConnectorConfigsShape(t *testing.T) {
	store := &fakeCredentialQuerier{configs: []credentials.ConnectorConfig{{
		Platform: "sub2api", Environment: "staging", Mode: "real",
		Endpoint: "https://api.solov.cc", TargetAllowlist: []string{"api.solov.cc"},
		CredentialRef: "secret://sub2api-prod/read-token", Version: 3,
		UpdatedAt: time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC), UpdatedBy: "staff_alice",
		// ProbeEnabled/ProbeCredentialRef：XM-ASSURE1-glue 补的投影覆盖——
		// 探测 Kill Switch 已打开、已登记一个探测专用凭据引用。
		ProbeEnabled: true, ProbeCredentialRef: "secret://sub2api-probe/probe-token",
	}, {
		Platform: "newapi", Environment: "staging", Mode: "fake", TargetAllowlist: nil, Version: 1,
		UpdatedAt: time.Date(2026, 8, 30, 1, 2, 3, 0, time.UTC), UpdatedBy: "staff_bob",
		// 零值：探测 Kill Switch 未打开、未登记探测凭据——覆盖
		// probe_credential_registered 的 false 分支。
	}}}
	rec := callCredentials(t, ListConnectorConfigsHandler(store), "/api/v1/connectors/config", credentialPrincipal(credentials.ScopeConnectorManage))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 {
		t.Fatalf("items = %#v", body.Items)
	}
	first := body.Items[0]
	for key, want := range map[string]any{
		"platform": "sub2api", "mode": "real", "endpoint": "https://api.solov.cc",
		"credential_ref": "secret://sub2api-prod/read-token", "version": float64(3),
		"updated_at": "2026-08-30T01:02:03Z", "updated_by": "staff_alice",
		"probe_enabled": true, "probe_credential_registered": true,
	} {
		if first[key] != want {
			t.Fatalf("items[0].%s = %#v, want %#v", key, first[key], want)
		}
	}
	if list, ok := first["target_allowlist"].([]any); !ok || len(list) != 1 || list[0] != "api.solov.cc" {
		t.Fatalf("target_allowlist = %#v", first["target_allowlist"])
	}
	if len(first) != 10 {
		t.Fatalf("响应字段应恰好是契约里的 10 个（XM-ASSURE1-glue 加了 probe_enabled/probe_credential_registered）, got %#v", first)
	}
	// 零值行（未打开 Kill Switch、未登记探测凭据）两个新字段都应是 false——
	// XM-ASSURE1-ui 交接文档 risks #2 描述的"零声明时读不到当前状态"这条
	// 缺口，本片补的正是这两个 false，不是省略字段。
	second := body.Items[1]
	if second["probe_enabled"] != false || second["probe_credential_registered"] != false {
		t.Fatalf("items[1] probe_enabled/probe_credential_registered = %#v/%#v, want false/false",
			second["probe_enabled"], second["probe_credential_registered"])
	}
	// nil 白名单要输出 []，不能是 null——前端按数组遍历
	if list, ok := body.Items[1]["target_allowlist"].([]any); !ok || len(list) != 0 {
		t.Fatalf("空白名单应为 []: %#v", body.Items[1]["target_allowlist"])
	}
	if strings.Contains(rec.Body.String(), `"environment"`) {
		t.Fatalf("response leaked environment: %s", rec.Body.String())
	}
	// probe_credential_registered 必须是布尔值——探测专用凭据引用的字面串
	// 一律不出现在响应里（与 credential_ref 字段的既有先例刻意不同，见
	// connectorConfigItem 的文档注释）。
	if strings.Contains(rec.Body.String(), "probe-token") {
		t.Fatalf("response leaked probe_credential_ref literal: %s", rec.Body.String())
	}
}

func TestCredentialRoutesRequireDedicatedScopesAndMountOnlyWhenWired(t *testing.T) {
	resolver, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	deps := Deps{
		Logger: discardLogger(), Service: "platform-api", Environment: "development",
		DB: fakePinger{}, Resolver: resolver,
	}
	// 未接入：三条端点都不存在
	unwired := NewRouter(deps)
	for _, path := range []string{"/api/v1/credentials", "/api/v1/credentials/expected", "/api/v1/connectors/config"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		devHeaders(req, credentials.ScopeManage+","+credentials.ScopeConnectorManage)
		rec := httptest.NewRecorder()
		unwired.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("未接入时 %s 应 404, got %d", path, rec.Code)
		}
	}

	deps.Credentials = &fakeCredentialQuerier{}
	wired := NewRouter(deps)
	for _, tc := range []struct {
		path   string
		scopes string
		want   int
	}{
		{"/api/v1/credentials", "registry.read,ops.read,finance.read", http.StatusForbidden},
		{"/api/v1/credentials", credentials.ScopeConnectorManage, http.StatusForbidden},
		{"/api/v1/credentials", credentials.ScopeManage, http.StatusOK},
		{"/api/v1/credentials/expected", credentials.ScopeConnectorManage, http.StatusForbidden},
		{"/api/v1/credentials/expected", credentials.ScopeManage, http.StatusOK},
		{"/api/v1/connectors/config", credentials.ScopeManage, http.StatusForbidden},
		{"/api/v1/connectors/config", credentials.ScopeConnectorManage, http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		devHeaders(req, tc.scopes)
		rec := httptest.NewRecorder()
		wired.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s scopes=%q status=%d body=%s", tc.path, tc.scopes, rec.Code, rec.Body.String())
		}
		if rec.Code == http.StatusOK && rec.Header().Get("Cache-Control") == "" {
			t.Fatalf("%s 应继承 /api/v1 的 no-store", tc.path)
		}
	}
}
