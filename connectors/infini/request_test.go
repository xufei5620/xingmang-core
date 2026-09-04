package infini

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// fixedClock 让 Date 头可预测；真实时钟在 Client 构造时注入。
func fixedClock() time.Time {
	return time.Date(2025, time.January, 21, 12, 0, 0, 0, time.UTC)
}

func testClient() *Client {
	return &Client{
		baseURL: "https://openapi.infini.money",
		keyID:   "testkey",
		secret:  "testsecret",
		now:     fixedClock,
	}
}

func TestNewRequestSetsDateInRFC1123GMT(t *testing.T) {
	req, err := testClient().newRequest(context.Background(), http.MethodGet, "/v2/cards/list", nil)
	if err != nil {
		t.Fatal(err)
	}

	// 文档要求 RFC1123 GMT，且服务端只容忍 ±300 秒偏差
	if got, want := req.Header.Get("Date"), "Tue, 21 Jan 2025 12:00:00 GMT"; got != want {
		t.Fatalf("Date = %q, want %q", got, want)
	}
}

func TestNewRequestSignsWithSameDateItSends(t *testing.T) {
	// 签名用的 date 与实际发出的 Date 头必须是同一个值——两者取自不同的
	// time.Now() 调用是最容易写出的 bug，而它只在跨秒的那一次请求上失败。
	req, err := testClient().newRequest(context.Background(), http.MethodGet, "/v2/cards/list?size=20", nil)
	if err != nil {
		t.Fatal(err)
	}

	want := authorizationHeader("testkey",
		signature("testsecret", signingString("testkey", "GET", "/v2/cards/list?size=20", req.Header.Get("Date"))))

	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization 与 Date 头不自洽\ngot  %s\nwant %s", got, want)
	}
}

func TestNewRequestWithoutBodyHasNoDigest(t *testing.T) {
	req, err := testClient().newRequest(context.Background(), http.MethodGet, "/v2/cards/list", nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := req.Header.Get("Digest"); got != "" {
		t.Fatalf("无请求体时不该有 Digest, got %q", got)
	}
}

func TestNewRequestWithBodySetsDigestAndContentType(t *testing.T) {
	body := []byte(`{"product_id":1}`)
	req, err := testClient().newRequest(context.Background(), http.MethodPost, "/v2/cards/apply", body)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := req.Header.Get("Digest"), "SHA-256=W0SAyTJOpE6Ipbk+FKZDi8l18WoFR9c5T5lYAAiqf8M="; got != want {
		t.Fatalf("Digest = %q, want %q", got, want)
	}
	if got, want := req.Header.Get("Content-Type"), "application/json"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
}

// 请求体不参与签名（文档明写），所以带体与不带体的签名只应因方法和路径而不同。
// 这条测试钉住这个反直觉的口径，防止有人「顺手把 Digest 加进待签名串」。
func TestNewRequestBodyDoesNotAffectSignature(t *testing.T) {
	c := testClient()

	withBody, err := c.newRequest(context.Background(), http.MethodPost, "/v2/cards/apply", []byte(`{"product_id":1}`))
	if err != nil {
		t.Fatal(err)
	}
	otherBody, err := c.newRequest(context.Background(), http.MethodPost, "/v2/cards/apply", []byte(`{"product_id":2}`))
	if err != nil {
		t.Fatal(err)
	}

	if withBody.Header.Get("Authorization") != otherBody.Header.Get("Authorization") {
		t.Fatal("请求体变化不该改变签名——文档规定 body 不参与签名")
	}
	if withBody.Header.Get("Digest") == otherBody.Header.Get("Digest") {
		t.Fatal("请求体变化必须改变 Digest")
	}
}

func TestNewRequestBuildsURLFromBase(t *testing.T) {
	req, err := testClient().newRequest(context.Background(), http.MethodGet, "/v2/cards/list?size=20", nil)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := req.URL.String(), "https://openapi.infini.money/v2/cards/list?size=20"; got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
}

// 凭据绝不能出现在任何请求头里除了 Authorization 的签名值中（宪法条款 7）。
func TestNewRequestNeverSendsSecretInClear(t *testing.T) {
	req, err := testClient().newRequest(context.Background(), http.MethodPost, "/v2/cards/apply", []byte(`{"product_id":1}`))
	if err != nil {
		t.Fatal(err)
	}

	for name, values := range req.Header {
		for _, v := range values {
			if strings.Contains(v, "testsecret") {
				t.Fatalf("请求头 %s 里出现了明文密钥: %q", name, v)
			}
		}
	}
	if strings.Contains(req.URL.String(), "testsecret") {
		t.Fatal("URL 里出现了明文密钥")
	}
}
