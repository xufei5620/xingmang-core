package newapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 护栏的**直接**证明：客户端手里那个 http.Client 必须是带护栏的那一个。
//
// 外部测试（client_contract_test.go）能证明「重定向被拒」，但证明不了
// 「往 allowlist 之外发请求会被拒」——客户端只会请求自己的端点。
// 所以这条断言必须从包内部对着 c.http 直接发，让「有人把它换成
// http.DefaultClient」这种改动当场变红。
//
// 注意这两个请求**都不会产生任何网络 I/O**：护栏在 RoundTrip 里就拒了，
// 连 DNS 都不会查。
func TestClientTransportIsTheGuardedOne(t *testing.T) {
	impl := newTestClient(t)

	// allowlist 之外的主机：一个请求都发不出去
	if _, err := impl.http.Get("https://evil.example.test/api/channel/"); connector.KindOf(err) != connector.KindForbiddenTarget {
		t.Fatalf("allowlist 之外的分类 = %q, want forbidden_target（err=%v）", connector.KindOf(err), err)
	}

	// 写方法：只读通道上走不通（ADR-018 闸 4，机械强制而非评审约定）
	req, err := http.NewRequest(http.MethodPost, "https://xm.solov.cc/api/channel/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := impl.http.Do(req); connector.KindOf(err) != connector.KindWriteAttempt {
		t.Fatalf("写方法的分类 = %q, want write_attempt（err=%v）", connector.KindOf(err), err)
	}

	// 超时是硬性要求（规格 §18.1-4）
	if impl.http.Timeout <= 0 {
		t.Fatal("客户端没有超时")
	}
	// 业务日结时区必须显式，默认 UTC（规格 §5.9）
	if impl.businessDay != time.UTC {
		t.Fatalf("默认业务日时区 = %v, want UTC", impl.businessDay)
	}
	// 币种决定最小单位小数位，不能是零值
	if impl.currency != "USD" || impl.scale != 2 {
		t.Fatalf("默认币种/精度 = %q/%d, want USD/2（NewAPI 的 quota 换算基准是美元）",
			impl.currency, impl.scale)
	}
	// 活跃用户判据是平台自定的，必须有一个明确的默认值
	if impl.activeWindow != defaultActiveUserWindow {
		t.Fatalf("默认活跃窗口 = %v, want %v", impl.activeWindow, defaultActiveUserWindow)
	}
}

// TestNewClientFillsMissingTimeout：漏填超时回落到默认值，
// 绝不能变成「没有超时」——一个没有超时的采集请求能把整轮同步挂死。
func TestNewClientFillsMissingTimeout(t *testing.T) {
	built, err := NewClient(connector.Config{
		ServiceInstanceID: "newapi-test",
		Environment:       "test",
		Endpoint:          "https://xm.solov.cc",
		CredentialRef:     testCredentialRef,
		TargetAllowlist:   []string{"xm.solov.cc"},
	}, testSecretProvider(t))
	if err != nil {
		t.Fatalf("零超时应回落到默认值而不是报错: %v", err)
	}
	if got := built.(*client).http.Timeout; got != defaultRequestTimeout {
		t.Fatalf("超时 = %v, want %v", got, defaultRequestTimeout)
	}
}

// ---------------------------------------------------------------------------
// 第五道闸：伪装成 GET 的写端点黑名单
// ---------------------------------------------------------------------------

// TestWriteDisguisedAsGetRoutesAreBlocked 钉死黑名单本身**存在且生效**。
//
// 这是 NewAPI 相对 Sub2API 多出来的那道闸，也是本包最容易被无声改坏的地方：
// 通用的四道闸看的是 HTTP 方法与目标主机，而这些端点方法就是 GET、主机就是
// 同一个上游——闸门全部放行，库照写不误。
//
// 用例里的路径**逐条对应普查报告第 5 节追到的写库调用点**，包括带 :id 的
// 形态与把参数夹在中间的 /api/channel/:id/codex/usage。有人删掉黑名单里的
// 一条、或者把前缀匹配改成全等匹配，这里会当场红。
func TestWriteDisguisedAsGetRoutesAreBlocked(t *testing.T) {
	blocked := []struct {
		path string
		why  string
	}{
		{"/api/channel/test", "INSERT system_task，异步跑全量渠道测试"},
		{"/api/channel/test/42", "真发上游请求（消耗额度）+ UPDATE response_time/test_time"},
		{"/api/channel/update_balance", "UPDATE 全部启用渠道的 balance；余额 ≤0 还会自动禁用渠道"},
		{"/api/channel/update_balance/42", "UPDATE 单渠道 balance/balance_updated_time"},
		{"/api/channel/fetch_models/42", "Codex 渠道 401 时把刷新后的 token 写回 channel.key"},
		{"/api/channel/42/codex/usage", "上游 401/403 时回写 token"},
		{"/api/channel/42/codex/usage/reset-credits", "同上"},
		{"/api/user/token", "静默轮换调用者自己的 access token——调一次就把采集凭据打掉"},
		{"/api/user/aff", "首次调用会生成并持久化邀请码（lazy write on GET）"},
		{"/api/user/epay/notify", "标记充值订单成功 + 给用户加额度"},
		{"/api/subscription/epay/notify", "创建用户订阅 + 结单"},
		{"/api/subscription/epay/return", "同上"},
		{"/api/oauth/wechat", "可能 INSERT 新用户 + 必定 INSERT 登录会话"},
		{"/api/oauth/telegram/bind/abc", "UPDATE 用户 telegram_id"},
		{"/v1/realtime", "WebSocket 升级，扣额度 + 写消费日志"},
		{"/v1/video/generations/task-1", "轮询时 UPDATE task 状态"},
		{"/v1/videos/task-1", "同上"},
		{"/kling/v1/videos/text2video/task-1", "同上"},
	}
	for _, tc := range blocked {
		if err := assertReadOnlyRoute(tc.path); err == nil {
			t.Fatalf("%s 必须被黑名单挡住（%s）", tc.path, tc.why)
		}
	}

	// 本包**真正在用**的路由一条都不能被误伤：黑名单写宽了会让整条采集
	// 链路静静地全部失败，而失败分类是 write_attempt——那会让人以为
	// 代码里混进了写操作，去查一个不存在的 bug。
	for _, route := range []string{
		routeStatus, routeChannels, routeUsers, routeTopups, routeLogs, routeUsageData,
	} {
		if err := assertReadOnlyRoute(route); err != nil {
			t.Fatalf("在用的只读路由 %s 被黑名单误伤: %v", route, err)
		}
	}

	// 段前缀匹配而不是字符串前缀：这两条长得像黑名单项但并不是它们。
	// 用字符串前缀实现的话，它们会被误伤。
	for _, ok := range []string{"/api/user/tokens-report", "/api/channel/testing"} {
		if err := assertReadOnlyRoute(ok); err != nil {
			t.Fatalf("%s 不该被误伤（黑名单必须按路径段匹配，不是字符串前缀）: %v", ok, err)
		}
	}
}

// TestBlockedRouteNeverLeavesTheProcess 证明黑名单是在**发请求之前**生效的。
//
// 光有 assertReadOnlyRoute 的单元测试不够：它证明函数会返回错误，
// 证明不了 do() 真的调用了它、也证明不了拦截发生在请求发出之前。
// 这里把端点指向一个必然不可达的主机——如果请求真的发出去了，
// 得到的会是 unavailable（连不上）而不是 write_attempt。
func TestBlockedRouteNeverLeavesTheProcess(t *testing.T) {
	impl := newTestClient(t)
	_, err := impl.get(t.Context(), "newapi.test", "/api/channel/update_balance", nil, nil)
	if connector.KindOf(err) != connector.KindWriteAttempt {
		t.Fatalf("分类 = %q, want write_attempt（err=%v）——"+
			"若是 unavailable/forbidden_target，说明请求已经发出去了",
			connector.KindOf(err), err)
	}
	// 错误文本只有分类 + 操作名，不带上游路径细节
	if got := err.Error(); got != "write_attempt: newapi.test" {
		t.Fatalf("对外错误文本 = %q", got)
	}
}

// TestRoutesKeepTrailingSlash 钉死几条**必须**带尾斜杠的路由。
//
// 它们在 gin 里注册的是 "/"，少一个斜杠会得到 301 重定向；而本客户端拒绝
// 一切重定向（一个 302 就能把请求引到 allowlist 之外）。于是「少写一个字符」
// 的症状会是 forbidden_target——一个看起来像 allowlist 配错了的故障。
// 这条测试让那个字符掉了的瞬间就红，而不是等到接真实实例那天。
func TestRoutesKeepTrailingSlash(t *testing.T) {
	for _, route := range routeMustEndWithSlash {
		if !strings.HasSuffix(route, "/") {
			t.Fatalf("%s 必须以 / 结尾：上游注册的是 \"/\"，少一个斜杠会 301，"+
				"而本客户端拒绝重定向 → 症状会是 forbidden_target", route)
		}
	}
	// /api/status 反过来：它注册的是 "/status"，**多**一个斜杠同样会 301。
	if strings.HasSuffix(routeStatus, "/") {
		t.Fatalf("%s 不该以 / 结尾", routeStatus)
	}
}

// ---------------------------------------------------------------------------
// 测试装配
// ---------------------------------------------------------------------------

const (
	// testCredentialRef 是测试用的凭据引用；引用本身不是秘密。
	testCredentialRef = "secret://newapi/readonly-token"
	// testTokenEnvVar 是引用在 env Provider 下的登记落点。
	testTokenEnvVar = "XM_TEST_NEWAPI_TOKEN"
	// testTokenValue 刻意是一句明显的占位符，而不是像真凭据的高熵串：
	// 仓库里不该出现任何长得像凭据的东西（宪法 7 条）。
	testTokenValue = "placeholder-placeholder"
)

func testSecretProvider(t *testing.T) secrets.SecretProvider {
	t.Helper()
	provider, err := secrets.NewEnvProvider(
		map[string]string{testCredentialRef: testTokenEnvVar},
		secrets.WithLookup(func(name string) (string, bool) {
			if name != testTokenEnvVar {
				return "", false
			}
			return testTokenValue, true
		}),
	)
	if err != nil {
		t.Fatalf("装配 env provider: %v", err)
	}
	return provider
}

func newTestClient(t *testing.T) *client {
	t.Helper()
	built, err := NewClient(connector.Config{
		ServiceInstanceID: "newapi-test",
		Environment:       "test",
		Endpoint:          "https://xm.solov.cc",
		CredentialRef:     testCredentialRef,
		TargetAllowlist:   []string{"xm.solov.cc"},
		Timeout:           5 * time.Second,
	}, testSecretProvider(t))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	impl, ok := built.(*client)
	if !ok {
		t.Fatalf("NewClient 返回了意料之外的类型 %T", built)
	}
	return impl
}
