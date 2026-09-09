package sub2api

import (
	"net/http"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 护栏的**直接**证明：客户端手里那个 http.Client 必须是带护栏的那一个。
//
// 外部测试（client_contract_test.go）能证明「重定向被拒」，
// 但证明不了「往 allowlist 之外发请求会被拒」——客户端只会请求自己的端点。
// 所以这条断言必须从包内部对着 c.http 直接发，让「有人把它换成
// http.DefaultClient」这种改动当场变红。
//
// 注意这两个请求**都不会产生任何网络 I/O**：护栏在 RoundTrip 里就拒了，
// 连 DNS 都不会查。
func TestClientTransportIsTheGuardedOne(t *testing.T) {
	provider, err := secrets.NewEnvProvider(
		map[string]string{"secret://sub2api/readonly-token": "XM_TEST_SUB2API_TOKEN"},
		secrets.WithLookup(func(string) (string, bool) { return "placeholder-placeholder", true }),
	)
	if err != nil {
		t.Fatal(err)
	}
	built, err := NewClient(connector.Config{
		ServiceInstanceID: "sub2api-test",
		Environment:       "test",
		Endpoint:          "https://api.solov.cc",
		CredentialRef:     "secret://sub2api/readonly-token",
		TargetAllowlist:   []string{"api.solov.cc"},
		Timeout:           5 * time.Second,
	}, provider)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	impl, ok := built.(*client)
	if !ok {
		t.Fatalf("NewClient 返回了意料之外的类型 %T", built)
	}

	// allowlist 之外的主机：一个请求都发不出去
	if _, err := impl.http.Get("https://evil.example.test/api/v1/admin/users"); connector.KindOf(err) != connector.KindForbiddenTarget {
		t.Fatalf("allowlist 之外的分类 = %q, want forbidden_target（err=%v）", connector.KindOf(err), err)
	}

	// 写方法：只读通道上走不通（ADR-018 闸 4，机械强制而非评审约定）
	req, err := http.NewRequest(http.MethodPost, "https://api.solov.cc/api/v1/admin/users", nil)
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
		t.Fatalf("默认币种/精度 = %q/%d, want USD/2", impl.currency, impl.scale)
	}
}

// TestNewClientFillsMissingTimeout：漏填超时回落到默认值，
// 绝不能变成「没有超时」——一个没有超时的采集请求能把整轮同步挂死。
func TestNewClientFillsMissingTimeout(t *testing.T) {
	provider, err := secrets.NewEnvProvider(
		map[string]string{"secret://sub2api/readonly-token": "XM_TEST_SUB2API_TOKEN"},
		secrets.WithLookup(func(string) (string, bool) { return "placeholder-placeholder", true }),
	)
	if err != nil {
		t.Fatal(err)
	}
	built, err := NewClient(connector.Config{
		ServiceInstanceID: "sub2api-test",
		Environment:       "test",
		Endpoint:          "https://api.solov.cc",
		CredentialRef:     "secret://sub2api/readonly-token",
		TargetAllowlist:   []string{"api.solov.cc"},
	}, provider)
	if err != nil {
		t.Fatalf("零超时应回落到默认值而不是报错: %v", err)
	}
	if got := built.(*client).http.Timeout; got != defaultRequestTimeout {
		t.Fatalf("超时 = %v, want %v", got, defaultRequestTimeout)
	}
}
