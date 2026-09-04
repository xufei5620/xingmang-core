package connector

import (
	"net/http"
	"strings"
	"time"
)

// vendorWriteMethods 是供应商写通道允许的 HTTP 方法。
//
// 只放 Infini 卡接口实际用到的三个：查询是 GET，其余全是 POST。
// 新增供应商如果需要别的方法，改这里要连带评审——放宽一个方法，
// 所有走这条通道的连接器都跟着放宽。
var vendorWriteMethods = map[string]struct{}{
	http.MethodGet:  {},
	http.MethodHead: {},
	http.MethodPost: {},
}

// VendorWriteTransport 是供应商写通道的传输层。
//
// 与 ReadOnlyTransport 的关系：两者是并列的两条通道，不是继承或放宽。
// 只读通道（ADR-018）用于平台不拥有其业务真相的上游（NewAPI/Sub2API），
// 那里的写请求是配置错误；写通道用于平台作为客户去调用的供应商 API
// （第一个用例是 Infini 开卡），那里的 POST 是正常业务。
type VendorWriteTransport struct {
	base      http.RoundTripper
	allowlist map[string]struct{}
}

// NewVendorWriteTransport 包装一个 RoundTripper，加上写通道的方法与
// allowlist 限制。
func NewVendorWriteTransport(base http.RoundTripper, allowlist []string) http.RoundTripper {
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
	return &VendorWriteTransport{base: base, allowlist: set}
}

// RoundTrip 实现 http.RoundTripper。
//
// 两道闸都在请求发出**之前**判定：这条通道上的一次误发是真扣钱，
// 事后补救比只读通道贵得多。
func (t *VendorWriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, ok := vendorWriteMethods[req.Method]; !ok {
		return nil, NewError(KindMethodNotAllowed,
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
// 口径与 ReadOnlyTransport.allowed 一致：只比主机名、要求完全相等、
// allowlist 为空时 fail closed。
func (t *VendorWriteTransport) allowed(hostPort string) bool {
	if len(t.allowlist) == 0 {
		return false // fail closed
	}
	_, ok := t.allowlist[hostOnly(hostPort)]
	return ok
}

// NewVendorWriteClient 创建一个供应商写通道客户端：限方法、限目标、
// 拒重定向、带超时。
func NewVendorWriteClient(allowlist []string, timeout time.Duration) *http.Client {
	return NewVendorWriteClientWithBase(nil, allowlist, timeout)
}

// NewVendorWriteClientWithBase 与 NewVendorWriteClient 相同，但允许指定底层
// RoundTripper（base 为 nil 时用 http.DefaultTransport）。
//
// 与只读通道同一条纪律：可注入的是「怎么连」，不是「能不能连」——
// base 依旧被 VendorWriteTransport 包在里面，方法限制、allowlist 与
// 重定向拒绝在测试路径和生产路径上是同一份代码。
func NewVendorWriteClientWithBase(base http.RoundTripper, allowlist []string, timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{
		Transport: NewVendorWriteTransport(base, allowlist),
		Timeout:   timeout,
		// 不跟随重定向：写请求被 302 引到别处，等于把带签名的请求体
		// 送给了第三方
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return NewError(KindForbiddenTarget, "redirect refused", nil)
		},
	}
}
