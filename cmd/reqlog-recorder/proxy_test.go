package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/reqlogformat"
)

func TestHeaderMapMasksSensitiveHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-thisisaveryveryverylongtoken")
	h.Set("X-Api-Key", "abcdefghijklmnopqrstuvwxyz")
	h.Set("Cookie", "session=abcdefghijklmnopqrstuvwxyz")
	h.Set("Content-Type", "application/json")

	m := headerMap(h)
	for _, k := range []string{"Authorization", "X-Api-Key", "Cookie"} {
		v := m[k]
		if !strings.HasSuffix(v, "...[masked]") {
			t.Errorf("%s 应被截断打码，got %q", k, v)
		}
		if len(v) > 24+len("...[masked]") {
			t.Errorf("%s 打码后过长: %q", k, v)
		}
	}
	if m["Content-Type"] != "application/json" {
		t.Errorf("非敏感头部不该被改动: got %q", m["Content-Type"])
	}
}

func TestHeaderMapLeavesShortSensitiveValuesUnmasked(t *testing.T) {
	// 24 字符以内的值原样保留——原型的截断只在超过阈值时才生效，
	// 这条边界值得钉住，否则改动阈值判断（用 >= 而不是 >）会悄悄影响短令牌
	h := http.Header{}
	h.Set("Authorization", "Bearer short")
	m := headerMap(h)
	if m["Authorization"] != "Bearer short" {
		t.Errorf("短值不该被截断: got %q", m["Authorization"])
	}
}

func TestTokenPrefixFromBearerAndAPIKey(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "/v1/x", nil)
	req.Header.Set("Authorization", "Bearer sk-0123456789abcdefghijklmnop")
	if got := tokenPrefix(req); got != "sk-0123456789abcdefg" {
		t.Errorf("got %q, want 20 字符截断", got)
	}

	req2, _ := http.NewRequest(http.MethodPost, "/v1/x", nil)
	req2.Header.Set("x-api-key", "abcdefghijklmnopqrstuvwxyz")
	if got := tokenPrefix(req2); got != "abcdefghijklmnopqrst" {
		t.Errorf("got %q, want 20 字符截断", got)
	}

	req3, _ := http.NewRequest(http.MethodPost, "/v1/x", nil)
	if got := tokenPrefix(req3); got != "" {
		t.Errorf("没有令牌应返回空串, got %q", got)
	}
}

// TestMakeProxyRecordsNonStreamingChatRequest 端到端验证：一次非流式
// chat completions 请求经代理转发后，应该产出一条落进 writeQ 的
// FullRecord，字段与请求/响应内容对得上（model、token 用量、状态码、
// 请求体/响应体原文）。
func TestMakeProxyRecordsNonStreamingChatRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/v1/chat/completions" {
			t.Errorf("上游收到意外路径 %q", req.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Oneapi-Request-Id", "req_test_123")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer upstream.Close()

	r := testRecorder(t, t.TempDir())
	handler, err := r.makeProxy("sub2api", upstream.URL)
	if err != nil {
		t.Fatalf("makeProxy: %v", err)
	}

	body := `{"model":"gpt-4o","stream":false,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-testtoken0000000000001")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("客户端收到状态码 = %d, want 200", rec.Code)
	}

	select {
	case fr := <-r.writeQ:
		if fr.Source != "sub2api" {
			t.Errorf("Source = %q", fr.Source)
		}
		if fr.Model != "gpt-4o" {
			t.Errorf("Model = %q, want gpt-4o", fr.Model)
		}
		if fr.Status != http.StatusOK {
			t.Errorf("Status = %d", fr.Status)
		}
		if fr.InTok != 5 || fr.OutTok != 2 {
			t.Errorf("用量 = in=%d out=%d, want 5/2", fr.InTok, fr.OutTok)
		}
		if fr.UpReqID != "req_test_123" {
			t.Errorf("UpReqID = %q", fr.UpReqID)
		}
		if fr.ReqBody != body {
			t.Errorf("ReqBody 未原样保留: got %q", fr.ReqBody)
		}
		if !strings.Contains(fr.RespBody, "chatcmpl-1") {
			t.Errorf("RespBody 未原样保留: got %q", fr.RespBody)
		}
		if fr.TokenPfx != "sk-testtoken00000000" {
			t.Errorf("TokenPfx = %q", fr.TokenPfx)
		}
		if !reqlogformat.RecordIDPattern.MatchString(fr.ID) {
			t.Errorf("ID 形状不对: %q", fr.ID)
		}
		if auth := fr.ReqHeaders["Authorization"]; !strings.HasSuffix(auth, "...[masked]") {
			t.Errorf("落盘的 Authorization 头应已打码: got %q", auth)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("超时：writeQ 里没有收到记录")
	}
}

// TestMakeProxyPassesThroughUninterestingPaths 验证 GET 请求与非受抄录
// 前缀的路径直接透传，不产生任何落盘记录（与原型 reqlogger.go 的
// `interesting` 判断一致）。
func TestMakeProxyPassesThroughUninterestingPaths(t *testing.T) {
	var upstreamHit bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		upstreamHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	r := testRecorder(t, t.TempDir())
	handler, err := r.makeProxy("newapi", upstream.URL)
	if err != nil {
		t.Fatalf("makeProxy: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !upstreamHit {
		t.Fatal("GET 请求应该透传给上游")
	}
	select {
	case fr := <-r.writeQ:
		t.Fatalf("GET 请求不该产生落盘记录: %+v", fr)
	default:
	}
}

func TestMakeProxyRejectsUnparsableUpstream(t *testing.T) {
	r := testRecorder(t, t.TempDir())
	if _, err := r.makeProxy("newapi", "://not a url"); err == nil {
		t.Fatal("非法上游地址应在构造期报错，而不是留到第一次请求才发现")
	}
}

func TestServeRespectsContextCancellation(t *testing.T) {
	r := testRecorder(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- r.serve(ctx, "newapi", "127.0.0.1:0", "http://127.0.0.1:1") }()

	// 给 ListenAndServe 一点时间真正绑定端口
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatalf("ctx 取消后 serve 应干净返回，got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时：serve 在 ctx 取消后没有及时返回")
	}
}
