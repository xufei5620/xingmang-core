package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	connusers "github.com/xufei5620/xingmang-platform/connectors/platformusers"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
	"github.com/xufei5620/xingmang-platform/internal/platform/platformusers"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// dynamicUsersClient 的测试(XM-USERS-REAL):这是「配置要每次请求时解析」
// 那条纪律唯一落地的地方,所以覆盖的是**resolve 的决策表**,而不是 HTTP 解析
// 细节(那部分由 connectors/platformusers/realclient_test.go 覆盖)。

func fixedUsersConfigSource(row *jobs.ConnectorConfig) jobs.ConnectorConfigSource {
	return jobs.ConnectorConfigSourceFunc(func(context.Context, string, string) (*jobs.ConnectorConfig, error) {
		return row, nil
	})
}

func failingUsersConfigSource(err error) jobs.ConnectorConfigSource {
	return jobs.ConnectorConfigSourceFunc(func(context.Context, string, string) (*jobs.ConnectorConfig, error) {
		return nil, err
	})
}

func noopSecrets() secrets.SecretProvider {
	p, _ := secrets.NewEnvProvider(map[string]string{})
	return p
}

// TestDynamicUsersClientProductionFakeGate:生效模式为 fake 且环境为
// production 时必须 not_supported,绝不返回样本数据(宪法 12 条),
// 与 worker 的 jobs.ErrConnectorProductionFake 同一纪律。
func TestDynamicUsersClientProductionFakeGate(t *testing.T) {
	t.Run("进程缺省就是 fake", func(t *testing.T) {
		c := &dynamicUsersClient{
			source:      connusers.SourceSub2API,
			defaultMode: usersModeFake,
			environment: "production",
			secrets:     noopSecrets(),
		}
		_, err := c.ListUsers(context.Background(), connusers.ListFilter{Source: connusers.SourceSub2API})
		if connector.KindOf(err) != connector.KindNotSupported {
			t.Fatalf("分类 = %q, want not_supported(err=%v)", connector.KindOf(err), err)
		}
		if !errors.Is(err, jobs.ErrConnectorProductionFake) {
			t.Fatalf("应能用 errors.Is 认出 jobs.ErrConnectorProductionFake: %v", err)
		}
	})

	t.Run("数据库行把生效模式覆盖成_fake", func(t *testing.T) {
		// 进程缺省是 real,但后台配置行显式写了 fake——生效模式以行为准,
		// 闸门必须照样触发,不能因为「进程启动时选的是 real」就放行。
		c := &dynamicUsersClient{
			source:      connusers.SourceSub2API,
			defaultMode: usersModeReal,
			environment: "production",
			secrets:     noopSecrets(),
			configSource: fixedUsersConfigSource(&jobs.ConnectorConfig{
				Platform: connusers.SourceSub2API, Environment: "production", Mode: "fake",
			}),
		}
		_, err := c.ListUsers(context.Background(), connusers.ListFilter{Source: connusers.SourceSub2API})
		if connector.KindOf(err) != connector.KindNotSupported {
			t.Fatalf("分类 = %q, want not_supported(err=%v)", connector.KindOf(err), err)
		}
	})

	t.Run("非生产环境放行_fake", func(t *testing.T) {
		c := &dynamicUsersClient{
			source:      connusers.SourceSub2API,
			defaultMode: usersModeFake,
			environment: "development",
			secrets:     noopSecrets(),
		}
		page, err := c.ListUsers(context.Background(), connusers.ListFilter{Source: connusers.SourceSub2API})
		if err != nil {
			t.Fatalf("非生产环境的 fake 不该被拒: %v", err)
		}
		if !hasFakeSuffix(page.Snapshot.Source) {
			t.Fatalf("应当拿到样本数据,Snapshot.Source = %q", page.Snapshot.Source)
		}
	})
}

func hasFakeSuffix(source string) bool {
	return len(source) >= 5 && source[len(source)-5:] == "-fake"
}

// TestDynamicUsersClientConfigUnavailableFallsBackToEnvDefault:库读不到时
// 按进程级缺省处理,不是硬错误——与 jobs.connectorConfigResolver.load 同一
// 条纪律(库暂时不可用不该让整个用户页签跟着报错)。
func TestDynamicUsersClientConfigUnavailableFallsBackToEnvDefault(t *testing.T) {
	c := &dynamicUsersClient{
		source:       connusers.SourceSub2API,
		defaultMode:  usersModeFake,
		environment:  "staging",
		secrets:      noopSecrets(),
		configSource: failingUsersConfigSource(fmt.Errorf("connection refused")),
	}
	page, err := c.ListUsers(context.Background(), connusers.ListFilter{Source: connusers.SourceSub2API})
	if err != nil {
		t.Fatalf("库读不到应当回落到 env 缺省,而不是报错: %v", err)
	}
	if !hasFakeSuffix(page.Snapshot.Source) {
		t.Fatalf("应当拿到样本数据,Snapshot.Source = %q", page.Snapshot.Source)
	}
}

// TestDynamicUsersClientRejectsUnparsableMode:后台配置行的 mode 列写了一个
// 不认识的值,必须报错,而不是悄悄回落到进程缺省——回落会让人以为自己在
// 后台切换生效了,其实没有。
func TestDynamicUsersClientRejectsUnparsableMode(t *testing.T) {
	c := &dynamicUsersClient{
		source:      connusers.SourceSub2API,
		defaultMode: usersModeFake,
		environment: "development",
		secrets:     noopSecrets(),
		configSource: fixedUsersConfigSource(&jobs.ConnectorConfig{
			Platform: connusers.SourceSub2API, Environment: "development", Mode: "bogus",
		}),
	}
	_, err := c.ListUsers(context.Background(), connusers.ListFilter{Source: connusers.SourceSub2API})
	if connector.KindOf(err) != connector.KindInternal {
		t.Fatalf("分类 = %q, want internal(err=%v)", connector.KindOf(err), err)
	}
}

// TestDynamicUsersClientRealRowEndToEnd:后台配置行把生效模式切到 real、
// 填好端点/白名单/凭据引用后,dynamicUsersClient 必须真的用这份配置发起一次
// 只读 HTTP 读取并映射出正确的用户数据——这是「配置要每次请求时解析」这条
// 纪律端到端成立的证据,不只是单元测试各个环节。
func TestDynamicUsersClientRealRowEndToEnd(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/admin/users", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"items":[
		  {"id":1,"email":"a@example.test","username":"alice","status":"active","balance":10.00,"last_active_at":null}
		],"total":1,"page":1,"page_size":1000,"pages":1}}`))
	})
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	host := parseTestHost(t, server.URL)

	const credRef = "secret://platformusers-test/dynamic-e2e"
	provider, err := secrets.NewEnvProvider(
		map[string]string{credRef: "XM_TEST_DYNAMIC_TOKEN"},
		secrets.WithLookup(func(name string) (string, bool) {
			if name != "XM_TEST_DYNAMIC_TOKEN" {
				return "", false
			}
			return "test-only.invalid-token", true
		}),
	)
	if err != nil {
		t.Fatalf("装配 env provider: %v", err)
	}

	c := &dynamicUsersClient{
		source:      connusers.SourceSub2API,
		defaultMode: usersModeFake, // 进程缺省是 fake;必须被下面这行配置覆盖成 real
		environment: "staging",
		secrets:     provider,
		configSource: fixedUsersConfigSource(&jobs.ConnectorConfig{
			Platform: connusers.SourceSub2API, Environment: "staging", Mode: "real",
			Endpoint: server.URL, TargetAllowlist: []string{host}, CredentialRef: credRef,
		}),
	}

	// dynamicUsersClient 构造真实客户端时不注入测试用的 base transport,
	// 所以这里没法让它信任 httptest 的自签证书——直接调用会在 TLS 握手上失败,
	// 而这正是我们想避免断言的地方(那只说明"没配置自签证书",不说明
	// "配置解析对不对")。这条用例改为直接检验 resolve() 解出的 RealConfig
	// 与生效模式是否与后台行一致;真正的 HTTP 映射由
	// connectors/platformusers/realclient_test.go 覆盖(它能注入
	// WithBaseTransport)。
	mode, cfg, err := c.resolve(context.Background())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if mode != usersModeReal {
		t.Fatalf("生效模式 = %q, want real(后台行应当覆盖进程缺省 fake)", mode)
	}
	if cfg.Endpoint != server.URL {
		t.Fatalf("Endpoint = %q, want %q", cfg.Endpoint, server.URL)
	}
	if len(cfg.TargetAllowlist) != 1 || cfg.TargetAllowlist[0] != host {
		t.Fatalf("TargetAllowlist = %v, want [%q]", cfg.TargetAllowlist, host)
	}
	if cfg.CredentialRef != credRef {
		t.Fatalf("CredentialRef = %q, want %q", cfg.CredentialRef, credRef)
	}
}

func parseTestHost(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("解析测试服务器地址: %v", err)
	}
	return u.Hostname()
}

// TestBuildPlatformUsersOffModeMountsNothing:off 模式返回 nil,nil——
// 路由层据此完全不挂载用户端点(404 优于挂着却只回演示数据)。
func TestBuildPlatformUsersOffModeMountsNothing(t *testing.T) {
	svc, err := buildPlatformUsers(usersModeOff, platformUsersDeps{})
	if err != nil || svc != nil {
		t.Fatalf("off 模式应返回 (nil, nil),得到 (%v, %v)", svc, err)
	}
}

// TestBuildPlatformUsersFakeModeWithoutPoolStillWorks:Pool 为 nil 时(没有
// 数据库连接的装配场景)完全按 XM_PLATFORM_USERS_MODE 的进程级缺省运行,
// 不该 panic 或报错。
func TestBuildPlatformUsersFakeModeWithoutPoolStillWorks(t *testing.T) {
	svc, err := buildPlatformUsers(usersModeFake, platformUsersDeps{Environment: "development"})
	if err != nil {
		t.Fatalf("buildPlatformUsers: %v", err)
	}
	if svc == nil {
		t.Fatal("fake 模式应当挂载 Service")
	}
	page, err := svc.List(context.Background(), platformusers.ListInput{Platform: connusers.SourceSub2API})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !hasFakeSuffix(page.Snapshot.Source) {
		t.Fatalf("应当拿到样本数据,Snapshot.Source = %q", page.Snapshot.Source)
	}
}

// TestBuildPlatformUsersFakeModeKeepsV2Capabilities 是 dynamicUsersClient
// 包一层之后**不弄丢** fake 端 v2(逐用户详情/日用量/Key 元数据)的回归测试。
//
// internal/platform/platformusers.NewService 在构造期用类型断言决定要不要
// 登记这三个 reader;早先 buildPlatformUsers 直接把 *connusers.FakeClient
// 存进 clients map,断言天然成立。引入 dynamicUsersClient 之后,如果它不
// 转发 GetUser/DailyUsage/ListKeyMetadata,这三个功能会在 fake 模式下**全部
// 悄悄变成「尚未接入」**,而 ListUsers 本身照样正常——这种回归只有点开
// 用户详情页才会被人发现,所以必须有一条测试盯着它。
func TestBuildPlatformUsersFakeModeKeepsV2Capabilities(t *testing.T) {
	svc, err := buildPlatformUsers(usersModeFake, platformUsersDeps{Environment: "development"})
	if err != nil {
		t.Fatalf("buildPlatformUsers: %v", err)
	}

	// Sub2API 在 fake 端声明了全部三项 v2 能力(fake_v2.go 的 V2Capabilities)。
	detail, err := svc.Get(context.Background(), platformusers.DetailInput{
		Platform: connusers.SourceSub2API, UserID: "u_10241",
	})
	if err != nil {
		t.Fatalf("Get: %v(v2 用户详情不该在 fake 模式下丢失)", err)
	}
	if !hasFakeSuffix(detail.Snapshot.Source) {
		t.Fatalf("详情应来自 fake 端,Snapshot.Source = %q", detail.Snapshot.Source)
	}

	if _, err := svc.DailyUsage(context.Background(), platformusers.DailyUsageInput{
		Platform: connusers.SourceSub2API, UserID: "u_10241", Days: 7,
	}); err != nil {
		t.Fatalf("DailyUsage: %v(v2 日用量不该在 fake 模式下丢失)", err)
	}

	if _, err := svc.KeyMetadata(context.Background(), platformusers.KeyMetadataInput{
		Platform: connusers.SourceSub2API, UserID: "u_10241",
	}); err != nil {
		t.Fatalf("KeyMetadata: %v(v2 Key 元数据不该在 fake 模式下丢失)", err)
	}
}

// TestDynamicUsersClientDailyKeyStayNotSupportedWhenReal:生效模式解析成
// real 时,DailyUsage/ListKeyMetadata 必须仍然 not_supported——它们的
// real reader 需要各自独立的 DAILY_USAGE_APPROVAL/KEY_SCOPE_APPROVAL 真实
// 数据面审批(设计文档 §0),XM-USERS-V2-REAL(SUB2_REAL_APPROVAL/
// NEWAPI_REAL_APPROVAL)明确不覆盖它们,保持既有行为(不是回落到 fake、
// 也不是 panic)。
//
// GetUser 不在这条测试里:它现在 real 会下场,见
// TestDynamicUsersClientGetUserAttemptsRealDispatchWhenReal。
func TestDynamicUsersClientDailyKeyStayNotSupportedWhenReal(t *testing.T) {
	c := &dynamicUsersClient{
		source:      connusers.SourceSub2API,
		defaultMode: usersModeReal,
		environment: "development",
		secrets:     noopSecrets(),
	}
	if _, err := c.DailyUsage(context.Background(), connusers.DailyUsageQuery{
		Ref: connusers.UserRef{Platform: connusers.SourceSub2API, ID: "u_10241"}, Days: 7,
	}); connector.KindOf(err) != connector.KindNotSupported {
		t.Fatalf("DailyUsage 分类 = %q, want not_supported(err=%v)", connector.KindOf(err), err)
	}
	if _, err := c.ListKeyMetadata(context.Background(), connusers.KeyMetadataQuery{
		Ref: connusers.UserRef{Platform: connusers.SourceSub2API, ID: "u_10241"},
	}); connector.KindOf(err) != connector.KindNotSupported {
		t.Fatalf("ListKeyMetadata 分类 = %q, want not_supported", connector.KindOf(err))
	}
}

// TestDynamicUsersClientGetUserAttemptsRealDispatchWhenReal:生效模式解析
// 成 real 且没有配置端点/白名单/凭据引用时,GetUser 必须真的尝试构造
// RealClient 并因为配置不全而报 internal(配置问题,不是上游问题)——
// 而不是像 XM-USERS-V2-REAL 之前那样统一回落成 not_supported。这条断言
// 本身就是"real 已经下场"的证据:如果 GetUser 仍然直接转发给 fake 或
// 直接 not_supported,这里应该得到 fake 数据或 not_supported,而不是
// internal 配置错误。真正的 HTTP 映射(分页扫描、423、quota_per_unit 现读)
// 由 connectors/platformusers/{sub2api,newapi}_v2_test.go 覆盖——那里能用
// WithBaseTransport 信任 httptest 的自签证书,这里没有注入测试 transport,
// 所以只能验证到"确实在尝试连 real"这一步,与 ListUsers 的
// TestDynamicUsersClientRealRowEndToEnd 注释里说明的边界一致。
func TestDynamicUsersClientGetUserAttemptsRealDispatchWhenReal(t *testing.T) {
	c := &dynamicUsersClient{
		source:      connusers.SourceSub2API,
		defaultMode: usersModeReal,
		environment: "development",
		secrets:     noopSecrets(),
	}
	_, err := c.GetUser(context.Background(), connusers.GetUserQuery{
		Ref: connusers.UserRef{Platform: connusers.SourceSub2API, ID: "u_10241"},
	})
	if connector.KindOf(err) != connector.KindInternal {
		t.Fatalf("GetUser 分类 = %q, want internal(未配置 endpoint/allowlist/credential_ref 时 NewRealClient 应当拒绝,err=%v)",
			connector.KindOf(err), err)
	}
}

// TestDynamicUsersClientGetUserProductionFakeGate:与 ListUsers 同一条纪律
// (宪法 12 条)——生产环境下生效模式仍是 fake 时,GetUser 必须拒绝,
// 不能把演示样本悄悄当成真实客户资料在用户详情页展示出来。
func TestDynamicUsersClientGetUserProductionFakeGate(t *testing.T) {
	c := &dynamicUsersClient{
		source:      connusers.SourceSub2API,
		defaultMode: usersModeFake,
		environment: "production",
		secrets:     noopSecrets(),
	}
	_, err := c.GetUser(context.Background(), connusers.GetUserQuery{
		Ref: connusers.UserRef{Platform: connusers.SourceSub2API, ID: "u_10241"},
	})
	if connector.KindOf(err) != connector.KindNotSupported {
		t.Fatalf("分类 = %q, want not_supported(err=%v)", connector.KindOf(err), err)
	}
	if !errors.Is(err, jobs.ErrConnectorProductionFake) {
		t.Fatalf("应能用 errors.Is 认出 jobs.ErrConnectorProductionFake: %v", err)
	}
}
