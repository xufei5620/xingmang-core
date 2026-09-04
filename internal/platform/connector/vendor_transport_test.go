package connector

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 供应商写通道存在的理由：平台需要调用供应商自己的写接口（第一个用例是
// Infini 开卡），而 ReadOnlyTransport 在 RoundTrip 层就把 POST 拒了。
//
// 这与 ADR-018 的四道只读闸不冲突——那四道闸约束的是平台对 NewAPI/Sub2API
// 这类「平台不拥有其业务真相」的上游的接入方式，宪法条款 5 禁止的是直写
// 第三方原始表，不是调用供应商公开 API。
func TestVendorWriteTransportAllowsPost(t *testing.T) {
	base := &recordingTransport{}
	rt := NewVendorWriteTransport(base, []string{"api.infini.money"})

	_, err := rt.RoundTrip(req(t, http.MethodPost, "https://api.infini.money/v2/cards/apply"))
	if err != nil {
		t.Fatalf("POST 应被放行: %v", err)
	}
	if base.calls != 1 {
		t.Fatalf("应实际发出 1 个请求, got %d", base.calls)
	}
}

// 写通道放行 POST，不等于放行一切。Infini 的卡接口只用 GET 和 POST，
// 其余方法出现在这条通道上就是代码写错了，必须在请求发出前拦住。
func TestVendorWriteTransportBlocksOtherMethods(t *testing.T) {
	base := &recordingTransport{}
	rt := NewVendorWriteTransport(base, []string{"api.infini.money"})

	for _, m := range []string{
		http.MethodPut, http.MethodPatch, http.MethodDelete,
		http.MethodOptions, http.MethodConnect, http.MethodTrace,
	} {
		_, err := rt.RoundTrip(req(t, m, "https://api.infini.money/v2/cards/apply"))
		if err == nil {
			t.Fatalf("%s 必须被拒绝", m)
		}
		if KindOf(err) != KindMethodNotAllowed {
			t.Fatalf("%s 的错误分类 = %q, want method_not_allowed", m, KindOf(err))
		}
	}
	if base.calls != 0 {
		t.Fatalf("被拒的请求不该发出去, got %d", base.calls)
	}
}

// 与只读通道同款：主机必须完全相等，不做后缀匹配，
// 否则 api.infini.money.attacker.com 会被放行。
func TestVendorWriteTransportBlocksHostOffAllowlist(t *testing.T) {
	base := &recordingTransport{}
	rt := NewVendorWriteTransport(base, []string{"api.infini.money"})

	_, err := rt.RoundTrip(req(t, http.MethodPost, "https://api.infini.money.attacker.com/v2/cards/apply"))
	if err == nil {
		t.Fatal("allowlist 之外的主机必须被拒绝")
	}
	if KindOf(err) != KindForbiddenTarget {
		t.Fatalf("错误分类 = %q, want forbidden_target", KindOf(err))
	}
	if base.calls != 0 {
		t.Fatalf("被拒的请求不该发出去, got %d", base.calls)
	}
}

// allowlist 漏配必须变成「什么都连不上」，而不是「放行一切」——
// 这条通道会花真钱，fail closed 比只读通道更要紧。
func TestVendorWriteTransportEmptyAllowlistBlocksEverything(t *testing.T) {
	base := &recordingTransport{}
	rt := NewVendorWriteTransport(base, nil)

	_, err := rt.RoundTrip(req(t, http.MethodPost, "https://api.infini.money/v2/cards/apply"))
	if err == nil {
		t.Fatal("空 allowlist 必须拒绝一切")
	}
	if KindOf(err) != KindForbiddenTarget {
		t.Fatalf("错误分类 = %q, want forbidden_target", KindOf(err))
	}
	if base.calls != 0 {
		t.Fatalf("被拒的请求不该发出去, got %d", base.calls)
	}
}

func TestNewVendorWriteClientRefusesRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example.com/", http.StatusFound)
	}))
	defer target.Close()

	host := target.Listener.Addr().String()
	c := NewVendorWriteClient([]string{hostOnly(host)}, 5*time.Second)

	_, err := c.Post("http://"+host+"/v2/cards/apply", "application/json", nil)
	if err == nil {
		t.Fatal("重定向必须被拒绝")
	}
	if KindOf(err) != KindForbiddenTarget {
		t.Fatalf("重定向被拒的错误分类 = %q, want forbidden_target（err=%v）", KindOf(err), err)
	}
}

func TestNewVendorWriteClientHasTimeout(t *testing.T) {
	c := NewVendorWriteClient([]string{"api.infini.money"}, 0)
	if c.Timeout <= 0 {
		t.Fatal("超时为零时应回落到默认值，绝不能没有超时（规格 §18.1-4）")
	}
}

func TestNewVendorWriteClientWithBaseKeepsGuards(t *testing.T) {
	base := &recordingTransport{}
	c := NewVendorWriteClientWithBase(base, []string{"api.infini.money"}, 5*time.Second)

	if _, err := c.Do(req(t, http.MethodPost, "https://api.infini.money/v2/cards/apply")); err != nil {
		t.Fatalf("allowlist 内的 POST 应放行: %v", err)
	}
	if base.calls != 1 {
		t.Fatalf("应实际发出 1 个请求, got %d", base.calls)
	}

	if _, err := c.Do(req(t, http.MethodDelete, "https://api.infini.money/v2/cards/apply")); err == nil {
		t.Fatal("换底层之后 DELETE 照样必须被拒")
	}
	if _, err := c.Do(req(t, http.MethodPost, "https://evil.example.com/v2/cards/apply")); err == nil {
		t.Fatal("换底层之后 allowlist 照样必须生效")
	}
	if base.calls != 1 {
		t.Fatalf("被拒的请求不该发出去, got %d", base.calls)
	}
}
