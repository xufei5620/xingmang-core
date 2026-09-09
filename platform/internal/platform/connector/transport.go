package connector

import (
	"net"
	"net/http"
	"strings"
	"time"
)

// readOnlyMethods 是只读通道允许的 HTTP 方法。
var readOnlyMethods = map[string]struct{}{
	http.MethodGet:  {},
	http.MethodHead: {},
}

// ReadOnlyTransport 是 ADR-018 闸 4「Connector 包内无写路径」的**机械强制**。
//
// 不靠代码评审保证"这个包里没有写操作"——任何非 GET/HEAD 请求、任何不在
// allowlist 内的主机，在这里就被拒绝，请求根本不会发出。
//
// allowlist 为空时**拒绝一切**：配置漏填必须变成"什么都连不上"，
// 而不是"放行一切"。
type ReadOnlyTransport struct {
	base      http.RoundTripper
	allowlist map[string]struct{}
}

// NewReadOnlyTransport 包装一个 RoundTripper，加上只读与 allowlist 限制。
func NewReadOnlyTransport(base http.RoundTripper, allowlist []string) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	set := make(map[string]struct{}, len(allowlist))
	for _, h := range allowlist {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" {
			set[h] = struct{}{}
		}
	}
	return &ReadOnlyTransport{base: base, allowlist: set}
}

// RoundTrip 实现 http.RoundTripper。
func (t *ReadOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, ok := readOnlyMethods[req.Method]; !ok {
		return nil, NewError(KindWriteAttempt,
			req.Method+" "+req.URL.Path, nil)
	}
	if !t.allowed(req.URL.Host) {
		// 不把完整 URL 放进错误：URL 可能带查询参数
		return nil, NewError(KindForbiddenTarget, hostOnly(req.URL.Host), nil)
	}
	return t.base.RoundTrip(req)
}

// allowed 判断主机是否在 allowlist 内。
//
// 只比对主机名（忽略端口与大小写），且要求**完全相等**——不做后缀匹配，
// 否则 `evil-api.solov.cc.attacker.com` 会被 `solov.cc` 放行。
func (t *ReadOnlyTransport) allowed(hostPort string) bool {
	if len(t.allowlist) == 0 {
		return false // fail closed
	}
	_, ok := t.allowlist[hostOnly(hostPort)]
	return ok
}

func hostOnly(hostPort string) string {
	h := strings.ToLower(strings.TrimSpace(hostPort))
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

// NewReadOnlyClient 创建一个只读、限目标、带超时的 HTTP 客户端。
//
// 超时是硬性要求（规格 §18.1-4：所有外部 I/O 必须有超时）。
func NewReadOnlyClient(allowlist []string, timeout time.Duration) *http.Client {
	return NewReadOnlyClientWithBase(nil, allowlist, timeout)
}

// NewReadOnlyClientWithBase 与 NewReadOnlyClient 相同，但允许指定底层
// RoundTripper（base 为 nil 时用 http.DefaultTransport）。
//
// 存在的理由只有一个：契约测试要对着 httptest.NewTLSServer 的自签证书跑，
// 必须换掉底层的 TLS 配置。**护栏一个都不能少**——base 依旧被
// ReadOnlyTransport 包在里面，所以非 GET/HEAD、allowlist 之外的主机、
// 重定向，在测试路径上和生产路径上被同一份代码拒绝。
// 换句话说：可注入的是「怎么连」，不是「能不能连」。
func NewReadOnlyClientWithBase(base http.RoundTripper, allowlist []string, timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{
		Transport: NewReadOnlyTransport(base, allowlist),
		Timeout:   timeout,
		// 不跟随重定向：上游一个 302 就能把请求引到 allowlist 之外
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return NewError(KindForbiddenTarget, "redirect refused", nil)
		},
	}
}
