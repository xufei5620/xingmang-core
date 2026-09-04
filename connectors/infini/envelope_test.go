package infini

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// serverClient 起一个假上游，并把客户端指向它。
// 走的是真的 VendorWriteTransport——护栏在测试路径上和生产路径上是同一份代码。
func serverClient(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	host := srv.Listener.Addr().String()
	return &Client{
		baseURL: "http://" + host,
		keyID:   "testkey",
		secret:  "testsecret",
		httpc:   connector.NewVendorWriteClient([]string{hostOnly(host)}, 5*time.Second),
		now:     fixedClock,
	}, srv
}

func hostOnly(hostPort string) string {
	if i := strings.LastIndex(hostPort, ":"); i > 0 {
		return hostPort[:i]
	}
	return hostPort
}

func TestDoDecodesDataOnSuccessCode(t *testing.T) {
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"message":"ok","data":{"id":"card_123"}}`))
	})

	var out struct {
		ID string `json:"id"`
	}
	if err := c.do(context.Background(), http.MethodGet, "/v2/cards/status?id=card_123", nil, &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "card_123" {
		t.Fatalf("data 未解出, got %+v", out)
	}
}

// 上游收下了请求但业务上拒绝（余额不足、产品不可用等），与「连不上」
// 是完全不同的处置：前者重试无意义，后者可以退避重试。
func TestDoMapsNonZeroCodeToRejected(t *testing.T) {
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":40001,"message":"insufficient balance","data":null}`))
	})

	err := c.do(context.Background(), http.MethodPost, "/v2/cards/apply", []byte(`{}`), nil)
	if err == nil {
		t.Fatal("code != 0 必须报错")
	}
	if connector.KindOf(err) != connector.KindRejected {
		t.Fatalf("错误分类 = %q, want rejected", connector.KindOf(err))
	}
}

// ADR-004 铁律：供应商的原始错误文本不透传给调用方，只进服务端日志。
func TestDoDoesNotLeakUpstreamMessageIntoErrorText(t *testing.T) {
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":40001,"message":"secret internal detail","data":null}`))
	})

	err := c.do(context.Background(), http.MethodPost, "/v2/cards/apply", []byte(`{}`), nil)
	if err == nil {
		t.Fatal("code != 0 必须报错")
	}
	if strings.Contains(err.Error(), "secret internal detail") {
		t.Fatalf("上游原文不该出现在对外错误里: %q", err.Error())
	}
}

func TestDoMapsHTTPStatusToKind(t *testing.T) {
	cases := []struct {
		status int
		want   connector.ErrorKind
	}{
		{http.StatusUnauthorized, connector.KindAuth},
		{http.StatusForbidden, connector.KindAuth},
		{http.StatusTooManyRequests, connector.KindRateLimited},
		{http.StatusInternalServerError, connector.KindUnavailable},
		{http.StatusBadGateway, connector.KindUnavailable},
	}

	for _, tc := range cases {
		c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			w.Write([]byte(`{"code":1,"message":"nope"}`))
		})

		err := c.do(context.Background(), http.MethodGet, "/v2/cards/list", nil, nil)
		if connector.KindOf(err) != tc.want {
			t.Fatalf("HTTP %d 的分类 = %q, want %q", tc.status, connector.KindOf(err), tc.want)
		}
	}
}

func TestDoMapsUnparseableBodyToBadResponse(t *testing.T) {
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>维护中</html>`))
	})

	err := c.do(context.Background(), http.MethodGet, "/v2/cards/list", nil, nil)
	if connector.KindOf(err) != connector.KindBadResponse {
		t.Fatalf("非 JSON 响应的分类 = %q, want bad_response", connector.KindOf(err))
	}
}

// 401 最可能的成因是 IP 白名单没生效或时钟偏差超 ±300 秒，
// 这两种都不该被吞成「网络错误」——它们的排查方向完全不同。
func TestDoSendsSignedHeaders(t *testing.T) {
	var gotAuth, gotDate string
	c, _ := serverClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotDate = r.Header.Get("Date")
		w.Write([]byte(`{"code":0,"message":"ok","data":{}}`))
	})

	if err := c.do(context.Background(), http.MethodGet, "/v2/cards/list", nil, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gotAuth, `Signature keyId="testkey"`) {
		t.Fatalf("Authorization 头没发出去: %q", gotAuth)
	}
	if gotDate == "" {
		t.Fatal("Date 头没发出去")
	}
}
