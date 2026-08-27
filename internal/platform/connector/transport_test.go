package connector

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// recordingTransport 记录是否真的把请求发了出去。
type recordingTransport struct{ calls int }

func (r *recordingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls++
	return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
}

func req(t *testing.T, method, rawurl string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(method, rawurl, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReadOnlyTransportAllowsReadMethods(t *testing.T) {
	base := &recordingTransport{}
	rt := NewReadOnlyTransport(base, []string{"api.solov.cc"})
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		if _, err := rt.RoundTrip(req(t, m, "https://api.solov.cc/v1/users")); err != nil {
			t.Fatalf("%s 应被放行: %v", m, err)
		}
	}
	if base.calls != 2 {
		t.Fatalf("应实际发出 2 个请求, got %d", base.calls)
	}
}

func TestReadOnlyTransportBlocksWriteMethods(t *testing.T) {
	// ADR-018 闸 4：只读通道里的写请求必须走不通，而且**根本不发出去**
	base := &recordingTransport{}
	rt := NewReadOnlyTransport(base, []string{"api.solov.cc"})
	for _, m := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions, http.MethodConnect,
	} {
		_, err := rt.RoundTrip(req(t, m, "https://api.solov.cc/v1/users"))
		if err == nil {
			t.Fatalf("%s 必须被拒绝", m)
		}
		if KindOf(err) != KindWriteAttempt {
			t.Fatalf("%s 的错误分类 = %q, want write_attempt", m, KindOf(err))
		}
	}
	if base.calls != 0 {
		t.Fatalf("被拒的请求不应发出，实际发出 %d 个", base.calls)
	}
}

func TestReadOnlyTransportEnforcesAllowlist(t *testing.T) {
	base := &recordingTransport{}
	rt := NewReadOnlyTransport(base, []string{"api.solov.cc"})

	for name, u := range map[string]string{
		"完全不同的主机": "https://evil.example.com/v1/users",
		// 后缀匹配会让这个通过——所以必须用完全相等比对
		"allowlist 作为后缀": "https://api.solov.cc.attacker.com/v1/users",
		"子域名":            "https://internal.api.solov.cc/v1/users",
	} {
		_, err := rt.RoundTrip(req(t, http.MethodGet, u))
		if err == nil {
			t.Fatalf("%s 必须被拒绝: %s", name, u)
		}
		if KindOf(err) != KindForbiddenTarget {
			t.Fatalf("%s 的错误分类 = %q, want forbidden_target", name, KindOf(err))
		}
	}
	if base.calls != 0 {
		t.Fatalf("被拒的请求不应发出，实际发出 %d 个", base.calls)
	}

	// 端口与大小写不影响主机匹配
	for _, u := range []string{
		"https://API.SOLOV.CC/v1/users",
		"https://api.solov.cc:8443/v1/users",
	} {
		if _, err := rt.RoundTrip(req(t, http.MethodGet, u)); err != nil {
			t.Fatalf("%s 应被放行: %v", u, err)
		}
	}
}

func TestReadOnlyTransportEmptyAllowlistFailsClosed(t *testing.T) {
	// 配置漏填必须变成「什么都连不上」，不能变成「放行一切」
	base := &recordingTransport{}
	rt := NewReadOnlyTransport(base, nil)
	_, err := rt.RoundTrip(req(t, http.MethodGet, "https://api.solov.cc/v1/users"))
	if err == nil {
		t.Fatal("空 allowlist 必须拒绝一切（fail closed）")
	}
	if KindOf(err) != KindForbiddenTarget {
		t.Fatalf("错误分类 = %q", KindOf(err))
	}
	if base.calls != 0 {
		t.Fatal("不应发出任何请求")
	}
}

func TestReadOnlyTransportErrorDoesNotLeakQueryString(t *testing.T) {
	rt := NewReadOnlyTransport(&recordingTransport{}, []string{"api.solov.cc"})
	_, err := rt.RoundTrip(req(t, http.MethodGet,
		"https://evil.example.com/v1?token=super-secret-value"))
	if err == nil {
		t.Fatal("应被拒绝")
	}
	if got := err.Error(); strings.Contains(got, "super-secret-value") {
		t.Fatalf("错误信息泄漏了查询参数: %s", got)
	}
}

func TestNewReadOnlyClientRefusesRedirects(t *testing.T) {
	// 上游一个 302 就能把请求引到 allowlist 之外
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example.com/", http.StatusFound)
	}))
	defer target.Close()

	host := target.Listener.Addr().String()
	c := NewReadOnlyClient([]string{hostOnly(host)}, 5*time.Second)
	// httptest 用 http；此处只验证重定向被拒这一条
	_, err := c.Get("http://" + host + "/")
	if err == nil {
		t.Fatal("重定向必须被拒绝")
	}
	// http.Client 会把 CheckRedirect 的错误包进 *url.Error，因此用 errors.As 取分类
	if KindOf(err) != KindForbiddenTarget {
		t.Fatalf("重定向被拒的错误分类 = %q, want forbidden_target（err=%v）", KindOf(err), err)
	}
}

func TestNewReadOnlyClientHasTimeout(t *testing.T) {
	c := NewReadOnlyClient([]string{"api.solov.cc"}, 0)
	if c.Timeout <= 0 {
		t.Fatal("超时为零时应回落到默认值，绝不能没有超时（规格 §18.1-4）")
	}
}

func TestNewReadOnlyClientWithBaseKeepsGuards(t *testing.T) {
	// 可注入的是「怎么连」，不是「能不能连」：换掉底层 RoundTripper 之后，
	// 写方法与 allowlist 之外的主机照样走不通。
	base := &recordingTransport{}
	c := NewReadOnlyClientWithBase(base, []string{"api.solov.cc"}, 5*time.Second)

	if _, err := c.Do(req(t, http.MethodGet, "https://api.solov.cc/v1/users")); err != nil {
		t.Fatalf("allowlist 内的 GET 应放行: %v", err)
	}
	if base.calls != 1 {
		t.Fatalf("注入的 base 应被真正使用, calls = %d", base.calls)
	}

	if _, err := c.Do(req(t, http.MethodPost, "https://api.solov.cc/v1/users")); KindOf(err) != KindWriteAttempt {
		t.Fatalf("写方法的错误分类 = %q, want write_attempt", KindOf(err))
	}
	if _, err := c.Do(req(t, http.MethodGet, "https://evil.example.com/v1")); KindOf(err) != KindForbiddenTarget {
		t.Fatalf("allowlist 之外的错误分类 = %q, want forbidden_target", KindOf(err))
	}
	if base.calls != 1 {
		t.Fatalf("被拒的请求不应发出，base.calls = %d", base.calls)
	}
	if c.Timeout <= 0 {
		t.Fatal("必须有超时（规格 §18.1-4）")
	}
}
