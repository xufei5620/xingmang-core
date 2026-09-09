package herosms

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return &Client{
		baseURL: srv.URL,
		apiKey:  "test-key",
		httpc: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // 测试自签证书
			},
		},
	}
}

// 4xx 与 5xx 必须落到**不同的类型**上。
//
// 这是整个连接器最要紧的一条：4xx 说明钱没花出去、可以改参数重来；
// 5xx 说明可能已经扣费、必须落 unknown 交人工。把 5xx 当成拒绝去重试，
// 正是重复买号的那条路。分成两个类型是为了让调用方在编译期就得区分。
func TestHTTPErrorsSplitRejectedFromUnknown(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		check  func(error) bool
	}{
		{"4xx 是明确拒绝", http.StatusBadRequest, func(e error) bool {
			var r *RejectedError
			return errors.As(e, &r)
		}},
		{"5xx 是结果不确定", http.StatusBadGateway, func(e error) bool {
			var u *UnknownOutcomeError
			return errors.As(e, &u)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"title":"boom","details":"x"}`))
			}))
			defer srv.Close()

			err := newTestClient(t, srv).TestConnection(t.Context())
			if !tc.check(err) {
				t.Fatalf("错误分档不对: %T %v", err, err)
			}
		})
	}
}

// 鉴权是 ApiKey，且密钥绝不进 URL。
func TestAuthorizationHeaderIsApiKey(t *testing.T) {
	var gotAuth, gotURL string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotURL = r.Header.Get("Authorization"), r.URL.String()
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	if err := newTestClient(t, srv).TestConnection(t.Context()); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "ApiKey test-key" {
		t.Fatalf("鉴权头 = %q", gotAuth)
	}
	if want := "test-key"; contains(gotURL, want) {
		t.Fatalf("密钥不该出现在 URL 里: %q", gotURL)
	}
}

// 取码时 404 是**正常状态**（码还没到），不是失败。
//
// 把它当错误会让页面在等码的那几十秒里一直红着，而人就是要等的。
func TestGetLastOTPTreats404AsNotYet(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"title":"not found"}`))
	}))
	defer srv.Close()

	_, found, err := newTestClient(t, srv).GetLastOTP(t.Context(), "a1")
	if err != nil {
		t.Fatalf("404 不该是错误: %v", err)
	}
	if found {
		t.Fatal("404 时不该说找到了码")
	}
}

// 购买回来的数量与要的不符 = 不确定，不能把拿到的几个当成全部收下。
func TestPurchaseRejectsCountMismatch(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"a1","phone":"+1555","status":"active"}]}`))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).PurchaseActivations(t.Context(), PurchaseInput{
		Service: "op", Country: 1, Amount: 3,
	})
	var pe *ProtocolError
	if !errors.As(err, &pe) {
		t.Fatalf("数量不符应是协议错误（进而落 unknown）, got %T %v", err, err)
	}
}

// 扫满所有页仍未找到 ≠ 不存在。
//
// 已终结的 activation 本来就不在 active 列表里；把"扫不完"当成"不存在"，
// 会让一次本可以导入的号码在人工核对时被判成上游没有这笔。
func TestLookupDistinguishesIncompleteFromMissing(t *testing.T) {
	full := `{"data":[` + repeatActivation(100) + `]}`
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(full))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).LookupActivation(t.Context(), "not-there")
	if !errors.Is(err, ErrLookupIncomplete) {
		t.Fatalf("扫满页未找到应是 incomplete, got %v", err)
	}
}

// 翻到不满的一页仍没找到——这才是确实不存在。
func TestLookupReportsMissingOnShortPage(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"other","phone":"+1","status":"active"}]}`))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).LookupActivation(t.Context(), "not-there")
	if err == nil || errors.Is(err, ErrLookupIncomplete) {
		t.Fatalf("短页未找到应是明确的不存在, got %v", err)
	}
}

func contains(s, sub string) bool { return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func repeatActivation(n int) string {
	out := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			out += ","
		}
		out += `{"id":"x` + itoa(i) + `","phone":"+1","status":"active"}`
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
