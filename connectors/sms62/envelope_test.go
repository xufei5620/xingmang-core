package sms62

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 62 的信封是 {code,msg,request_id,time,data}，**HTTP 2xx 不等于成功**：
// 业务 code 必须是 1。
//
// 这是这家最容易踩的一处：按 HTTP 状态判成功，会把一次明确的业务拒绝
// （余额不足、商品下架）当成买到了号。
func TestEnvelopeRequiresBusinessCodeOne(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":400,"msg":"余额不足","request_id":"r1","time":1757000000,"data":{}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.GetInfo(t.Context())

	var be *BusinessError
	if !errors.As(err, &be) {
		t.Fatalf("HTTP 200 + code!=1 必须是业务错误, got %v", err)
	}
	if be.Code != 400 || be.Message != "余额不足" {
		t.Fatalf("业务码与文案要留住: %+v", be)
	}
}

// code=1 但 HTTP 不是 2xx 也不算成功。
//
// 两个条件都要满足才采纳——只看一个就会在上游把错误页配成 200、或把成功
// 响应配成 202 时判错，而这两种都真实发生过（参考实现的 do() 同时检查）。
func TestEnvelopeRequiresHTTPSuccessToo(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":1,"msg":"","request_id":"r1","time":1757000000,"data":{"ip":"1.2.3.4"}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if _, err := c.GetInfo(t.Context()); err == nil {
		t.Fatal("HTTP 5xx 即便业务码为 1 也不能算成功")
	}
}

// data 为 null 或缺失是协议失败，不是空结果。
//
// 伪装成空集合会让「上游改了响应形状」这件事悄悄变成「今天没有商品」。
func TestEnvelopeRejectsNullData(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":1,"msg":"","request_id":"r1","time":1757000000,"data":null}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	_, err := c.GetInfo(t.Context())

	var pe *ProtocolError
	if !errors.As(err, &pe) {
		t.Fatalf("data 为 null 必须是协议错误, got %v", err)
	}
}

// 鉴权头是 Bearer；密钥绝不进 URL。
func TestAuthorizationHeaderIsBearer(t *testing.T) {
	var gotAuth, gotQuery string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"code":1,"msg":"","request_id":"r1","time":1757000000,"data":{"ip":"1.2.3.4"}}`))
	}))
	defer srv.Close()

	c := newTestClient(t, srv)
	if _, err := c.GetInfo(t.Context()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("鉴权头 = %q", gotAuth)
	}
	if gotQuery != "" {
		t.Fatalf("密钥或参数不该进 query: %q", gotQuery)
	}
}
