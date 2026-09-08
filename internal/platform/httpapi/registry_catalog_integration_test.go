package httpapi

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// 端到端：真实 core.connector / core.connection 表 + 真实仓储 + 真实路由。
//
// 单元测试（registry_catalog_test.go）用假仓储证明 handler 会把调用者的环境
// **传下去**；这里证明的是另一段——那个参数真的落在 SQL 的 WHERE 上。
// 两段是分开的代码：闸门在 resolveEnvironment（Go），过滤在
// ListConnectionsByEnvironment（SQL）。只测前一段的话，一个「WHERE 忘了带
// environment」的实现照样能让假仓储那组全绿，而 development 的界面上会列出
// 生产的连接与它们的 credential_ref。

func catalogTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}
	t.Cleanup(pool.Close)
	// 三张表一起清：connection 引用 connector 与 service，
	// platform_channel_binding 又引用 service——TRUNCATE 要求一次列全引用方。
	if _, err := pool.Exec(ctx,
		"TRUNCATE core.connection, finance.platform_channel_binding, "+
			"core.connector, core.service CASCADE"); err != nil {
		t.Fatalf("清空资源目录失败: %v", err)
	}
	return pool
}

func catalogIntRouter(t *testing.T, store *registry.Store) http.Handler {
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
		Connectors:     store,
		Connections:    store,
	})
}

// seedConnection 在指定环境下造一条完整的「服务 + 连接」。
// connector 是全局的（没有 environment 列），所以由调用方传进来复用。
func seedConnection(
	t *testing.T, store *registry.Store, connectorID uuid.UUID, env, instance, credentialRef string,
) registry.Connection {
	t.Helper()
	ctx := context.Background()
	service, err := store.CreateService(ctx, registry.Service{
		ID:          uuid.New(),
		ServiceType: "sub2api",
		InstanceID:  instance,
		Environment: registry.Environment(env),
		Endpoint:    "https://" + instance + ".example.test",
		Owner:       "staff_alice",
		Status:      registry.ServiceActive,
	})
	if err != nil {
		t.Fatalf("登记服务(%s): %v", env, err)
	}
	conn, err := store.CreateConnection(ctx, registry.Connection{
		ID:                  uuid.New(),
		ConnectorID:         connectorID,
		ServiceID:           service.ID,
		Environment:         registry.Environment(env),
		CredentialRef:       credentialRef,
		TargetAllowlist:     []string{"api.example.test"},
		GrantedCapabilities: []registry.Capability{"sub2api.user.list"},
		Status:              registry.ConnectionEnabled,
	})
	if err != nil {
		t.Fatalf("登记连接(%s): %v", env, err)
	}
	return conn
}

func seedConnector(t *testing.T, store *registry.Store, key, version string) registry.Connector {
	t.Helper()
	got, err := store.CreateConnector(context.Background(), registry.Connector{
		ID:                   uuid.New(),
		Key:                  key,
		Version:              version,
		ContractVersion:      "v1",
		ConnectionSchemaPath: "contracts/connectors/" + key + ".v1.json",
		TargetAllowlist:      []string{"api.example.test"},
		ReadCapabilities:     []registry.Capability{"sub2api.user.list"},
	})
	if err != nil {
		t.Fatalf("登记连接器 %s@%s: %v", key, version, err)
	}
	return got
}

// TestConnectionsEndpointFiltersByEnvironmentAgainstRealStore：跨环境的行
// 不能出现在结果里，而且判据是「恰好那一条」而不是「至少有一条」。
//
// 变异验证（见交接文档）：把 ListConnectionsByEnvironment 的 WHERE 条件
// 从 `environment = $1` 改成 `environment = $1 OR true`（恒真；改条件而不是
// 删代码，$1 仍在用、签名不变、编译照常通过），本用例立刻红在下面「只该
// 看见本环境的 1 条」那一行，报「实际 2 条」。同一次运行里假仓储那组与
// 连接器那两条**全部保持绿**——所以红的是这条判据本身，不是整包连坐。
func TestConnectionsEndpointFiltersByEnvironmentAgainstRealStore(t *testing.T) {
	store := registry.NewStore(catalogTestPool(t))
	connector := seedConnector(t, store, "sub2api", "0.1.0")
	mine := seedConnection(t, store, connector.ID, "development", "dev-1", "secret://sub2api/dev-token")
	seedConnection(t, store, connector.ID, "production", "prod-1", "secret://sub2api/prod-token")

	rec := getAs(t, catalogIntRouter(t, store), "/api/v1/connections", "registry.read")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
	}
	items := decodeItems(t, rec)
	if len(items) != 1 {
		t.Fatalf("只该看见本环境的 1 条，实际 %d 条：%+v", len(items), items)
	}
	if items[0]["id"] != mine.ID.String() {
		t.Fatalf("看见的不是本环境那条：%+v", items[0])
	}
	if items[0]["environment"] != "development" {
		t.Fatalf("environment = %v", items[0]["environment"])
	}
	// 生产那条的凭据引用一个字都不该出现在响应里。
	if got := rec.Body.String(); strings.Contains(got, "secret://sub2api/prod-token") {
		t.Fatalf("生产连接的 credential_ref 泄漏到 development 的响应里了：%s", got)
	}
	// 本环境那条的引用要在——上面那条缺席断言因此不可能是「整个字段没输出」。
	if items[0]["credential_ref"] != "secret://sub2api/dev-token" {
		t.Fatalf("credential_ref = %v", items[0]["credential_ref"])
	}
}

// TestConnectorsEndpointIsGlobalAgainstRealStore：连接器目录不按环境筛。
//
// core.connector 没有 environment 列，所以「登记在哪个环境」这个问题不成立。
// 这条与上一条互为对照：同一次请求、同一个身份，连接只看见 1 条，
// 连接器两条都看得见——于是上一条的「1 条」不可能是因为整个路由都空了。
func TestConnectorsEndpointIsGlobalAgainstRealStore(t *testing.T) {
	store := registry.NewStore(catalogTestPool(t))
	seedConnector(t, store, "sub2api", "0.1.0")
	seedConnector(t, store, "newapi", "0.2.0")

	rec := getAs(t, catalogIntRouter(t, store), "/api/v1/connectors", "registry.read")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
	}
	items := decodeItems(t, rec)
	if len(items) != 2 {
		t.Fatalf("应返回 2 个连接器，实际 %d：%+v", len(items), items)
	}
	// 排序是 (key, version)：newapi 在 sub2api 前面。顺序也钉住——
	// 「同一个 key 的多个版本挨在一起」是这一列的阅读前提。
	if items[0]["key"] != "newapi" || items[1]["key"] != "sub2api" {
		t.Fatalf("排序不对：%v / %v", items[0]["key"], items[1]["key"])
	}
}

// TestConnectorVersionsOfSameKeyStayTogetherAgainstRealStore：同 key 多版本
// 按版本串在一起，且 ORDER BY 真的落在 SQL 上。
//
// 用**逆序登记**（0.10.0 先登记、0.2.0 后登记）来构造判据：如果 ORDER BY
// 被漏掉，结果会退化成插入顺序，这条就红。
func TestConnectorVersionsOfSameKeyStayTogetherAgainstRealStore(t *testing.T) {
	store := registry.NewStore(catalogTestPool(t))
	seedConnector(t, store, "sub2api", "0.10.0")
	seedConnector(t, store, "newapi", "1.0.0")
	seedConnector(t, store, "sub2api", "0.2.0")

	rec := getAs(t, catalogIntRouter(t, store), "/api/v1/connectors", "registry.read")
	items := decodeItems(t, rec)
	if len(items) != 3 {
		t.Fatalf("应返回 3 行，实际 %d", len(items))
	}
	gotKeys := []string{
		items[0]["key"].(string) + "@" + items[0]["version"].(string),
		items[1]["key"].(string) + "@" + items[1]["version"].(string),
		items[2]["key"].(string) + "@" + items[2]["version"].(string),
	}
	// version 是 text 列，所以是字典序："0.10.0" < "0.2.0"。
	// 断言逐字写死，避免把「按版本排序」误解成语义化版本排序。
	want := []string{"newapi@1.0.0", "sub2api@0.10.0", "sub2api@0.2.0"}
	for i := range want {
		if gotKeys[i] != want[i] {
			t.Fatalf("第 %d 行 = %q，期望 %q（完整顺序 %v）", i, gotKeys[i], want[i], gotKeys)
		}
	}
}
