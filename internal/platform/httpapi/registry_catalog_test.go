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

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// 资源目录「连接器」「连接」两张表的只读端点（XM-READONLY-QUERIES）。
//
// 本文件用假仓储证明 **handler 这一段**的契约：字段形状、空值语义、
// 权限与跨环境闸门、以及依赖为 nil 时端点根本不存在。
// 「SQL 真的按环境过滤了」是另一段代码，在
// registry_catalog_integration_test.go 里用真库验证——只测这一段的话，
// 一个「WHERE 忘了带 environment」的实现照样能让本文件全绿。

type fakeConnectorLister struct {
	items []registry.Connector
	err   error
	calls int
}

func (f *fakeConnectorLister) ListConnectors(context.Context) ([]registry.Connector, error) {
	f.calls++
	return f.items, f.err
}

type fakeConnectionLister struct {
	items []registry.Connection
	err   error
	// gotEnv 记下 handler 实际传下来的环境。判据不能只看「返回了什么」——
	// 假仓储无条件返回全部行，只有把入参记下来才看得出 handler 有没有把
	// 调用者的环境传下去。
	gotEnv registry.Environment
	calls  int
}

func (f *fakeConnectionLister) ListConnectionsByEnvironment(
	_ context.Context, env registry.Environment,
) ([]registry.Connection, error) {
	f.calls++
	f.gotEnv = env
	return f.items, f.err
}

func catalogRouter(t *testing.T, connectors ConnectorLister, connections ConnectionLister) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger:         discardLogger(),
		Service:        "platform-api",
		Environment:    "development",
		DB:             fakePinger{},
		Resolver:       res,
		Kernel:         &fakeExecutor{},
		ActionRegistry: action.NewRegistry(),
		Connectors:     connectors,
		Connections:    connections,
	})
}

func getAs(t *testing.T, h http.Handler, path, scopes string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	devHeaders(req, scopes)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sampleConnector() registry.Connector {
	return registry.Connector{
		ID:                        uuid.MustParse("11111111-1111-4111-8111-111111111111"),
		Key:                       "sub2api",
		Version:                   "0.1.0",
		ContractVersion:           "v1",
		ConnectionSchemaPath:      "contracts/connectors/sub2api.v1.json",
		TargetAllowlist:           []string{"api.example.test"},
		ReadCapabilities:          []registry.Capability{"sub2api.user.list"},
		WriteCapabilities:         []registry.Capability{"sub2api.user.update"},
		SupportedUpstreamVersions: []string{"0.1.179"},
		CompatibilityTestPath:     "tests/connectors/sub2api",
		CreatedAt:                 time.Date(2026, 9, 1, 3, 4, 5, 0, time.UTC),
		UpdatedAt:                 time.Date(2026, 9, 2, 3, 4, 5, 0, time.UTC),
	}
}

func sampleConnection() registry.Connection {
	return registry.Connection{
		ID:                      uuid.MustParse("22222222-2222-4222-8222-222222222222"),
		ConnectorID:             uuid.MustParse("11111111-1111-4111-8111-111111111111"),
		ServiceID:               uuid.MustParse("33333333-3333-4333-8333-333333333333"),
		Environment:             "development",
		CredentialRef:           "secret://sub2api/dev-token",
		TargetAllowlist:         []string{"api.example.test"},
		GrantedCapabilities:     []registry.Capability{"sub2api.user.list"},
		KillSwitch:              "",
		Status:                  registry.ConnectionEnabled,
		DetectedUpstreamVersion: "0.1.179",
		VersionFingerprint:      "sha256:abc",
		CreatedAt:               time.Date(2026, 9, 1, 3, 4, 5, 0, time.UTC),
		UpdatedAt:               time.Date(2026, 9, 2, 3, 4, 5, 0, time.UTC),
	}
}

func decodeItems(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是 {items:[…]}: %v（%s）", err, rec.Body.String())
	}
	return body.Items
}

// TestConnectorsEndpointReturnsTheCatalog 钉住连接器一行的**逐字段**契约。
//
// 读写能力分两列是 ADR-004 的约束落点（写集合非空的连接必须配 Kill Switch），
// 合成一列之后界面上看不出这条约束落在哪儿——所以两列都要断言，
// 而不只断言「返回了一行」。
func TestConnectorsEndpointReturnsTheCatalog(t *testing.T) {
	store := &fakeConnectorLister{items: []registry.Connector{sampleConnector()}}
	rec := getAs(t, catalogRouter(t, store, nil), "/api/v1/connectors", "registry.read")

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
	}
	items := decodeItems(t, rec)
	if len(items) != 1 {
		t.Fatalf("应返回 1 行，实际 %d 行", len(items))
	}
	got := items[0]
	for _, c := range []struct{ key, want string }{
		{"id", "11111111-1111-4111-8111-111111111111"},
		{"key", "sub2api"},
		{"version", "0.1.0"},
		{"contract_version", "v1"},
		{"connection_schema_path", "contracts/connectors/sub2api.v1.json"},
		{"compatibility_test_path", "tests/connectors/sub2api"},
		{"created_at", "2026-09-01T03:04:05Z"},
		{"updated_at", "2026-09-02T03:04:05Z"},
	} {
		if got[c.key] != c.want {
			t.Fatalf("%s = %v，期望 %q", c.key, got[c.key], c.want)
		}
	}
	for _, c := range []struct {
		key  string
		want []any
	}{
		{"target_allowlist", []any{"api.example.test"}},
		{"read_capabilities", []any{"sub2api.user.list"}},
		{"write_capabilities", []any{"sub2api.user.update"}},
		{"supported_upstream_versions", []any{"0.1.179"}},
	} {
		list, ok := got[c.key].([]any)
		if !ok {
			t.Fatalf("%s 不是数组：%v", c.key, got[c.key])
		}
		if len(list) != len(c.want) || list[0] != c.want[0] {
			t.Fatalf("%s = %v，期望 %v", c.key, list, c.want)
		}
	}
}

// TestConnectorEmptyListsAreArraysNotNull：没有声明能力时出 []，不出 null。
//
// 前端对这几列直接 `.length`，多一个 null 分支只会多一处漏判（同
// serverAssetItem.IPAddresses 的纪律）。
func TestConnectorEmptyListsAreArraysNotNull(t *testing.T) {
	bare := sampleConnector()
	bare.ReadCapabilities = nil
	bare.WriteCapabilities = nil
	bare.SupportedUpstreamVersions = nil
	store := &fakeConnectorLister{items: []registry.Connector{bare}}

	rec := getAs(t, catalogRouter(t, store, nil), "/api/v1/connectors", "registry.read")
	got := decodeItems(t, rec)[0]

	for _, key := range []string{"read_capabilities", "write_capabilities", "supported_upstream_versions"} {
		list, ok := got[key].([]any)
		if !ok {
			t.Fatalf("%s 应为数组（哪怕是空的），实际 %#v", key, got[key])
		}
		if len(list) != 0 {
			t.Fatalf("%s 应为空数组，实际 %v", key, list)
		}
	}
}

// TestCatalogEndpointsRequireRegistryRead：缺权限 403，文案带 scope 名。
//
// 「有权限时是 200」那半边同样断言：只测 403 的话，一条**根本没挂载**的
// 路由也会让人以为闸门生效了（那时是 404 不是 403，但一个只看
// `!= http.StatusOK` 的判据会把两者混为一谈）。
func TestCatalogEndpointsRequireRegistryRead(t *testing.T) {
	h := catalogRouter(t,
		&fakeConnectorLister{items: []registry.Connector{sampleConnector()}},
		&fakeConnectionLister{items: []registry.Connection{sampleConnection()}})

	for _, path := range []string{"/api/v1/connectors", "/api/v1/connections"} {
		denied := getAs(t, h, path, "ops.read")
		if denied.Code != http.StatusForbidden {
			t.Fatalf("%s 用 ops.read 应 403，实际 %d：%s", path, denied.Code, denied.Body.String())
		}
		if got := decodeError(t, denied); got.Message != "缺少权限 registry.read" {
			t.Fatalf("%s 的 403 文案 = %q，期望「缺少权限 registry.read」", path, got.Message)
		}

		// 对照组：同一条路由、同一份数据，只换 scope。
		allowed := getAs(t, h, path, "registry.read")
		if allowed.Code != http.StatusOK {
			t.Fatalf("%s 用 registry.read 应 200，实际 %d：%s", path, allowed.Code, allowed.Body.String())
		}
	}
}

// TestConnectionsEndpointRefusesCrossEnvironment：跨环境读取一律拒绝。
//
// 连接带着 credential_ref 与已授能力，一个 development 身份拿 production
// 当参数打过来必须 403（规格 §20.5：生产权限不继承）。
//
// 对照组是同一条路由带 ?environment=development——它 200，所以上面那个 403
// 不可能是因为「带 environment 参数就报错」。
func TestConnectionsEndpointRefusesCrossEnvironment(t *testing.T) {
	store := &fakeConnectionLister{items: []registry.Connection{sampleConnection()}}
	h := catalogRouter(t, nil, store)

	denied := getAs(t, h, "/api/v1/connections?environment=production", "registry.read")
	if denied.Code != http.StatusForbidden {
		t.Fatalf("跨环境应 403，实际 %d：%s", denied.Code, denied.Body.String())
	}
	if got := decodeError(t, denied); got.Message != "不允许跨环境读取：调用者身份属于 development" {
		t.Fatalf("403 文案 = %q", got.Message)
	}
	// 被拒的请求不该已经查过库——闸门在 handler 里，不在仓储里。
	if store.calls != 0 {
		t.Fatalf("跨环境请求不该到达仓储，实际调用了 %d 次", store.calls)
	}

	allowed := getAs(t, h, "/api/v1/connections?environment=development", "registry.read")
	if allowed.Code != http.StatusOK {
		t.Fatalf("同环境应 200，实际 %d：%s", allowed.Code, allowed.Body.String())
	}
	if store.gotEnv != "development" {
		t.Fatalf("handler 传给仓储的环境 = %q，期望 development", store.gotEnv)
	}
}

// TestConnectionsEndpointUsesCallerEnvironmentWhenParamOmitted：不传参数时
// 用调用者自己的环境，而不是某个默认值。
func TestConnectionsEndpointUsesCallerEnvironmentWhenParamOmitted(t *testing.T) {
	store := &fakeConnectionLister{items: []registry.Connection{sampleConnection()}}
	rec := getAs(t, catalogRouter(t, nil, store), "/api/v1/connections", "registry.read")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
	}
	if store.gotEnv != "development" {
		t.Fatalf("不传 environment 时传给仓储的是 %q，期望调用者自己的 development", store.gotEnv)
	}
}

// TestConnectionItemShape：连接一行的逐字段契约，重点是两处空值语义。
//
//   - last_verified_at 为 null = **从未核验过**，不是「核验过但很久以前」；
//   - kill_switch 空串 = 未配置（纯读连接），不是「有一个叫空串的开关」。
func TestConnectionItemShape(t *testing.T) {
	verified := time.Date(2026, 9, 3, 6, 7, 8, 0, time.UTC)
	withVerification := sampleConnection()
	withVerification.LastVerifiedAt = &verified
	withVerification.KillSwitch = "sub2api.kill"

	store := &fakeConnectionLister{items: []registry.Connection{sampleConnection(), withVerification}}
	rec := getAs(t, catalogRouter(t, nil, store), "/api/v1/connections", "registry.read")
	items := decodeItems(t, rec)
	if len(items) != 2 {
		t.Fatalf("应返回 2 行，实际 %d", len(items))
	}

	never, done := items[0], items[1]
	if never["last_verified_at"] != nil {
		t.Fatalf("从未核验时 last_verified_at 应为 null，实际 %v", never["last_verified_at"])
	}
	if done["last_verified_at"] != "2026-09-03T06:07:08Z" {
		t.Fatalf("last_verified_at = %v", done["last_verified_at"])
	}
	if never["kill_switch"] != "" {
		t.Fatalf("未配置 kill_switch 时应为空串，实际 %v", never["kill_switch"])
	}
	if done["kill_switch"] != "sub2api.kill" {
		t.Fatalf("kill_switch = %v", done["kill_switch"])
	}
	for _, c := range []struct{ key, want string }{
		{"connector_id", "11111111-1111-4111-8111-111111111111"},
		{"service_id", "33333333-3333-4333-8333-333333333333"},
		{"environment", "development"},
		// 引用出、明文永不出（ADR-014）。这一列是刻意保留的，见 connectionItem。
		{"credential_ref", "secret://sub2api/dev-token"},
		{"status", "enabled"},
		{"detected_upstream_version", "0.1.179"},
		{"version_fingerprint", "sha256:abc"},
	} {
		if never[c.key] != c.want {
			t.Fatalf("%s = %v，期望 %q", c.key, never[c.key], c.want)
		}
	}
}

// TestCatalogRoutesAreAbsentWithoutStores：依赖为 nil 时端点根本不存在。
//
// 这是一条缺席型断言，所以配了**对照组**：同一条路径在装了仓储的路由上
// 必须 200。没有对照组的话，一个路径拼错的用例会永远绿。
func TestCatalogRoutesAreAbsentWithoutStores(t *testing.T) {
	bare := catalogRouter(t, nil, nil)
	wired := catalogRouter(t,
		&fakeConnectorLister{items: []registry.Connector{sampleConnector()}},
		&fakeConnectionLister{items: []registry.Connection{sampleConnection()}})

	for _, path := range []string{"/api/v1/connectors", "/api/v1/connections"} {
		if got := getAs(t, bare, path, "registry.read"); got.Code != http.StatusNotFound {
			t.Fatalf("未装配仓储时 %s 应 404，实际 %d：%s", path, got.Code, got.Body.String())
		}
		if got := getAs(t, wired, path, "registry.read"); got.Code != http.StatusOK {
			t.Fatalf("对照组：装了仓储的 %s 应 200，实际 %d：%s", path, got.Code, got.Body.String())
		}
	}
}

// TestCatalogStoreErrorsDoNotLeak：仓储报错时不把底层细节吐给调用方。
func TestCatalogStoreErrorsDoNotLeak(t *testing.T) {
	leaky := errors.New(`pq: permission denied for table connector on 10.0.3.14:5432`)
	h := catalogRouter(t,
		&fakeConnectorLister{err: leaky},
		&fakeConnectionLister{err: leaky})

	for _, path := range []string{"/api/v1/connectors", "/api/v1/connections"} {
		rec := getAs(t, h, path, "registry.read")
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s 仓储报错应 500，实际 %d：%s", path, rec.Code, rec.Body.String())
		}
		if got := decodeError(t, rec); got.Message != "服务内部错误" {
			t.Fatalf("%s 的错误文案 = %q，不该是底层细节", path, got.Message)
		}
		for _, secret := range []string{"pq:", "10.0.3.14", "permission denied"} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Fatalf("%s 的响应泄漏了底层细节 %q：%s", path, secret, rec.Body.String())
			}
		}
	}
}
