package sms62

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestClient 把客户端指向测试服务器。
//
// **仍然走 NewVendorWriteClient 的传输层**（只换底层 RoundTripper 与 baseURL）：
// 方法限制、allowlist 与拒绝重定向在测试路径和生产路径上是同一份代码——
// 换掉整个 http.Client 的测试证明不了生产那条路。
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	c := &Client{
		baseURL: srv.URL,
		apiKey:  "test-key",
		httpc: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // 测试服务器自签证书
			},
		},
	}
	return c
}
