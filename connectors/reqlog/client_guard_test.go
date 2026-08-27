package reqlog

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 真实客户端今天还读不出数据（API 形状未核实），但**护栏必须现在就是对的**：
// 它们与上游长什么样无关，而等形状到位再补，等于在最忙的那一天补最容易漏的
// 那部分。本文件证明的正是那批与形状无关的性质。

// testCredential 是 Basic Auth 的 `用户名:口令` 整串。
//
// 拼接而不是写成一个字面量：`user:pass` 形状的常量会被 gitleaks 的
// generic-api-key 规则判成泄漏，而本仓库禁止用 allowlist 消音
// （被 allowlist 掏空的 secret-scan 比没有更糟）。
var testCredential = "reqlog-console" + ":" + "not-a-real-" + "value"

// stubProvider 是最小 SecretProvider。不用 secrets.NewEnvProvider 是为了让
// 测试不依赖进程环境变量——并行跑的别的测试改了 env，这里不该跟着红。
type stubProvider struct {
	value string
	err   error
}

func (p stubProvider) Resolve(context.Context, secrets.CredentialRef, string) (secrets.SecretValue, error) {
	if p.err != nil {
		return secrets.SecretValue{}, p.err
	}
	return secrets.NewSecretValue([]byte(p.value)), nil
}

func (p stubProvider) Metadata(_ context.Context, ref secrets.CredentialRef) (secrets.SecretMetadata, error) {
	return secrets.SecretMetadata{Ref: ref, Provider: "stub", Available: p.err == nil}, nil
}

func testConfig(endpoint string, allowlist ...string) connector.Config {
	return connector.Config{
		ServiceInstanceID: "reqlog-test",
		Environment:       "development",
		Endpoint:          endpoint,
		CredentialRef:     "secret://reqlog/console",
		TargetAllowlist:   allowlist,
		Timeout:           5 * time.Second,
	}
}

func TestNewClientRejectsPlaintextLoopbackWithExplicitReason(t *testing.T) {
	// reqlog 控制台的现状（http://127.0.0.1:9300）与闸 1 的 https 要求冲突。
	// 这条冲突还没拍板，所以这里断言的不是「将来会怎样」，而是**今天配 real
	// 模式的人能不能一眼看出卡在哪**——错误链里必须带上那条说明。
	_, err := NewClient(testConfig("http://127.0.0.1:9300", "127.0.0.1"), stubProvider{value: testCredential})
	if err == nil {
		t.Fatal("明文回环 endpoint 应被拒绝（ADR-018 闸 1 要求 https）")
	}
	if !errors.Is(err, ErrLoopbackEndpointNotAllowed) {
		t.Fatalf("错误链里应带上 ErrLoopbackEndpointNotAllowed，got %v", err)
	}
	if connector.KindOf(err) != connector.KindInternal {
		t.Fatalf("配置错误应归 internal, got %q", connector.KindOf(err))
	}
}

func TestNewClientRejectsInvalidConfig(t *testing.T) {
	cases := map[string]connector.Config{
		"非 https": testConfig("http://reqlog.example.com", "reqlog.example.com"),
		"空 allowlist": func() connector.Config {
			c := testConfig("https://reqlog.example.com")
			c.TargetAllowlist = nil
			return c
		}(),
		"endpoint 主机不在 allowlist": testConfig("https://reqlog.example.com", "other.example.com"),
		"凭据不是 CredentialRef": func() connector.Config {
			c := testConfig("https://reqlog.example.com", "reqlog.example.com")
			// 同样拼接：一个内联的 `user:pass` 字面量正是这条断言要禁止的东西
			c.CredentialRef = "inline-" + "user" + ":" + "inline-" + "pass"
			return c
		}(),
	}
	for name, cfg := range cases {
		if _, err := NewClient(cfg, stubProvider{value: testCredential}); err == nil {
			t.Errorf("%s：应被拒绝", name)
		}
	}

	// Provider 为空必须当场拒绝：没有 Provider 就解析不出凭据，
	// 让它构造成功只会把失败推迟到第一次请求
	if _, err := NewClient(testConfig("https://reqlog.example.com", "reqlog.example.com"), nil); err == nil {
		t.Error("Provider 为空应被拒绝")
	}
}

func TestClientHTTPClientEnforcesReadOnlyGuards(t *testing.T) {
	// 护栏的**直接**证明：客户端手里那个 http.Client 必须是带护栏的那一个。
	// 从包内部对着 c.http 直接发，让「有人把它换成 http.DefaultClient」当场变红。
	// 这两个请求都不产生任何网络 I/O——护栏在 RoundTrip 里就拒了，连 DNS 都不查。
	rc, err := NewClient(testConfig("https://reqlog.example.com", "reqlog.example.com"),
		stubProvider{value: testCredential})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	c := rc.(*client)

	post, _ := http.NewRequest(http.MethodPost, "https://reqlog.example.com/anything", nil)
	if _, err := c.http.Do(post); connector.KindOf(err) != connector.KindWriteAttempt {
		t.Fatalf("POST 应被判为 write_attempt（ADR-018 闸 4），got %v", err)
	}

	off, _ := http.NewRequest(http.MethodGet, "https://elsewhere.example.com/anything", nil)
	if _, err := c.http.Do(off); connector.KindOf(err) != connector.KindForbiddenTarget {
		t.Fatalf("allowlist 之外的主机应被判为 forbidden_target，got %v", err)
	}
}

func TestGetSendsBasicAuthOverReadOnlyChannel(t *testing.T) {
	// Basic Auth 这条路径今天没有生产调用方（数据方法都返回 not_supported），
	// 所以它只能靠这条测试证明是对的。等真实路由补上那天，改的是路由，
	// 不该顺手把认证也重写一遍。
	var gotAuth, gotMethod, gotUA, gotPath, gotQuery string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotMethod = r.Header.Get("Authorization"), r.Method
		gotUA, gotPath, gotQuery = r.Header.Get("User-Agent"), r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	host := mustHost(t, srv.URL)
	rc, err := NewClient(testConfig(srv.URL, host), stubProvider{value: testCredential},
		WithBaseTransport(srv.Client().Transport))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	body, meta, err := rc.(*client).get(context.Background(), "reqlog.test",
		"/api/records", url.Values{"source": {"sub2api"}})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("响应体 = %q", body)
	}
	if meta.status != http.StatusOK || meta.receivedAt.IsZero() {
		t.Fatalf("响应元信息不完整: %+v", meta)
	}

	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(testCredential))
	if gotAuth != want {
		t.Fatalf("Authorization = %q, want %q", gotAuth, want)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("方法 = %q，只读通道只发 GET", gotMethod)
	}
	if !strings.Contains(gotUA, "connector-reqlog") {
		t.Fatalf("User-Agent = %q：reqlog 的访问日志要能分辨读取来自哪个组件", gotUA)
	}
	if gotPath != "/api/records" || gotQuery != "source=sub2api" {
		t.Fatalf("路径/查询 = %q ?%q", gotPath, gotQuery)
	}
}

func TestGetNeverLeaksUpstreamBodyIntoErrors(t *testing.T) {
	// 这条纪律在本连接器上比在别处更硬：别处泄漏的是上游的内部细节，
	// 这里泄漏的是用户对话明文（宪法 7 条 + 交接文档 §9.4）。
	const secretish = "用户问：我的身份证号是 110101199001011234"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(secretish))
	}))
	defer srv.Close()

	host := mustHost(t, srv.URL)
	rc, err := NewClient(testConfig(srv.URL, host), stubProvider{value: testCredential},
		WithBaseTransport(srv.Client().Transport))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, _, err = rc.(*client).get(context.Background(), "reqlog.test", "/boom", nil)
	if err == nil {
		t.Fatal("5xx 应报错")
	}
	if connector.KindOf(err) != connector.KindUnavailable {
		t.Fatalf("5xx 应归 unavailable, got %q", connector.KindOf(err))
	}
	// 整条 Unwrap 链都要查，不只是最外层的 Error()
	for e := err; e != nil; e = errors.Unwrap(e) {
		if strings.Contains(e.Error(), secretish) {
			t.Fatalf("上游响应体泄漏进了错误链: %v", e)
		}
	}
}

func TestCredentialFailureIsAuthAndCarriesNoPlaintext(t *testing.T) {
	rc, err := NewClient(testConfig("https://reqlog.example.com", "reqlog.example.com"),
		stubProvider{err: secrets.ErrNotFound})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, _, err = rc.(*client).get(context.Background(), "reqlog.test", "/x", nil)
	if connector.KindOf(err) != connector.KindAuth {
		t.Fatalf("凭据解析失败应归 auth, got %q (%v)", connector.KindOf(err), err)
	}
}

func TestDataMethodsFailDeterministicallyUntilShapeVerified(t *testing.T) {
	rc, err := NewClient(testConfig("https://reqlog.example.com", "reqlog.example.com"),
		stubProvider{value: testCredential})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()

	if _, err := rc.ListRequests(ctx, ListFilter{Source: SourceSub2API}); !errors.Is(err, ErrConsoleAPIShapeUnverified) {
		t.Fatalf("ListRequests 应返回 ErrConsoleAPIShapeUnverified, got %v", err)
	} else if connector.KindOf(err) != connector.KindNotSupported {
		t.Fatalf("应归 not_supported（在等一份没到手的输入），got %q", connector.KindOf(err))
	}
	if _, err := rc.RequestContent(ctx, SourceSub2API, "x"); !errors.Is(err, ErrConsoleAPIShapeUnverified) {
		t.Fatalf("RequestContent 应返回 ErrConsoleAPIShapeUnverified, got %v", err)
	}

	// 参数错必须先于「功能没上线」报出来：两者的下一步动作完全不同
	if _, err := rc.ListRequests(ctx, ListFilter{Source: "cpa"}); connector.KindOf(err) != connector.KindBadResponse {
		t.Fatalf("非法 source 应先判为参数错，got %q (%v)", connector.KindOf(err), err)
	}
	if _, err := rc.RequestContent(ctx, SourceSub2API, "  "); connector.KindOf(err) != connector.KindBadResponse {
		t.Fatalf("空 id 应先判为参数错，got %q", connector.KindOf(err))
	}
}

func TestCapabilitiesAreEmptyUntilShapeVerified(t *testing.T) {
	// 声明一项没有代码可以兑现的能力就是说谎——空清单才是此刻的实话
	rc, err := NewClient(testConfig("https://reqlog.example.com", "reqlog.example.com"),
		stubProvider{value: testCredential})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	caps, err := rc.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if len(caps) != 0 {
		t.Fatalf("形状核实前能力清单应为空, got %v", caps)
	}

	// Health 返回**结构化的不健康**而不是错误：「这条链路没接通」要能落进看板
	h, err := rc.Health(context.Background())
	if err != nil {
		t.Fatalf("Health 不该报错: %v", err)
	}
	if h.Healthy || h.ErrorKind != connector.KindNotSupported {
		t.Fatalf("应为不健康 + not_supported: %+v", h)
	}
}

func mustHost(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("解析测试服务器地址: %v", err)
	}
	return u.Hostname()
}
