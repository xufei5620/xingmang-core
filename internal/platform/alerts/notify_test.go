package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// fakeBotToken 是测试用的假 Bot Token，形态与真实 token 一致
// （<bot_id>:<35 位 base64ish>），足以让「泄漏检测」有意义。
const fakeBotToken = "8123456789:AAHrKvTESTtokenNOTreal000000000000000"

const testTokenRef = "secret://alerts/telegram-bot"

// tokenProvider 返回一个只认识测试引用的 SecretProvider。
func tokenProvider(t *testing.T, token string) secrets.SecretProvider {
	t.Helper()
	provider, err := secrets.NewEnvProvider(
		map[string]string{testTokenRef: "XM_TEST_ALERT_TOKEN"},
		secrets.WithLookup(func(name string) (string, bool) {
			if name == "XM_TEST_ALERT_TOKEN" && token != "" {
				return token, true
			}
			return "", false
		}),
	)
	if err != nil {
		t.Fatalf("构造 Provider: %v", err)
	}
	return provider
}

func testAlert() Alert {
	now := time.Date(2026, 8, 27, 5, 0, 0, 0, time.UTC)
	return Alert{
		ID:              uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		RuleKey:         RuleMetricSyncFailed,
		DedupKey:        RuleMetricSyncFailed + ":production:sub2api.revenue.daily",
		Severity:        SeverityCritical,
		Status:          StatusOpen,
		Title:           "指标 sub2api.revenue.daily 同步失败",
		Detail:          "来源 sub2api-prod，错误码 timeout。",
		Environment:     "production",
		SourceMetricKey: revenueMetric,
		OpenedAt:        now,
		LastSeenAt:      now.Add(3 * time.Minute),
		FireCount:       4,
		NotifyStatus:    NotifyPending,
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTelegram 构造指向 httptest 假 Bot 的投递器。
func newTelegram(t *testing.T, baseURL, token string) *TelegramNotifier {
	t.Helper()
	n, err := NewTelegramNotifier(TelegramOptions{
		TokenRef: secrets.MustCredentialRef(testTokenRef),
		ChatID:   "-1001234567890",
		Secrets:  tokenProvider(t, token),
		BaseURL:  baseURL,
		Client:   &http.Client{Timeout: 5 * time.Second},
	})
	if err != nil {
		t.Fatalf("NewTelegramNotifier: %v", err)
	}
	return n
}

// TestTelegramNotifierSendsMessage：正常路径——token 出现在 URL 路径里，
// 消息体带 chat_id 与正文。
func TestTelegramNotifierSendsMessage(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer srv.Close()

	if err := newTelegram(t, srv.URL, fakeBotToken).Notify(context.Background(), testAlert()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if gotPath != "/bot"+fakeBotToken+"/sendMessage" {
		t.Fatalf("请求路径 = %q", gotPath)
	}
	if gotBody["chat_id"] != "-1001234567890" {
		t.Fatalf("chat_id 未送达: %v", gotBody["chat_id"])
	}
	text, _ := gotBody["text"].(string)
	for _, want := range []string{"CRITICAL", revenueMetric, "production", RuleMetricSyncFailed, "累计 4 次"} {
		if !strings.Contains(text, want) {
			t.Fatalf("消息正文缺少 %q:\n%s", want, text)
		}
	}
}

// TestTelegramNotifierNeverLeaksToken 是本模块最重要的一条测试（宪法 7 条）。
//
// Bot Token 长在 **URL 路径**里，而 net/http 造的错误默认带 URL。任何一处
// `return err` 都会把 token 送进 notify_error（会落库、会回前端）或日志。
// 这里穷举每一条会返回错误的路径，逐条断言 token 不在错误串里。
func TestTelegramNotifierNeverLeaksToken(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		// closeServer 模拟「连不上」——这是最危险的一条路径：
		// 错误由 net/http 生成，完整 URL（含 token）就在里面。
		closeServer bool
	}{
		{
			name: "上游返回 401",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
			},
		},
		{
			name: "HTTP 200 但 ok=false",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"ok":false,"description":"chat not found"}`)
			},
		},
		{
			name: "响应不是合法 JSON",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `<html>gateway error</html>`)
			},
		},
		{
			name: "上游把请求 URL 回显进错误描述",
			handler: func(w http.ResponseWriter, r *http.Request) {
				// 一个把完整请求 URL 写回错误页的网关，足以让「不把 err 往外
				// 冒」这条纪律破功——所以响应体也必须过一遍脱敏。
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, `{"ok":false,"description":"upstream failed for `+r.URL.Path+`"}`)
			},
		},
		{
			name:        "连不上（错误由 net/http 生成，含完整 URL）",
			closeServer: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := tc.handler
			if handler == nil {
				handler = func(http.ResponseWriter, *http.Request) {}
			}
			srv := httptest.NewServer(handler)
			baseURL := srv.URL
			if tc.closeServer {
				srv.Close()
			} else {
				defer srv.Close()
			}

			err := newTelegram(t, baseURL, fakeBotToken).Notify(context.Background(), testAlert())
			if err == nil {
				t.Fatal("期望投递失败")
			}
			assertNoToken(t, err.Error())
			// 落库路径同样必须干净：notify_error 会被前端原样显示。
			assertNoToken(t, SanitizeNotifyError(err, fakeBotToken))
			// 即使调用方忘了把 token 传进 SanitizeNotifyError，
			// 也不该泄漏——脱敏的责任在 notifier 内部，不在调用方。
			assertNoToken(t, SanitizeNotifyError(err))
		})
	}
}

// assertNoToken 断言文本里既没有完整 token，也没有 token 的任何一半。
//
// 检查两半是必要的：一个只 ReplaceAll 了完整串的实现，遇到被截断或被
// 转义的 token 就漏了。冒号前的 bot_id 单独泄漏危害有限，冒号后的秘密部分
// 泄漏则等同于泄漏整个 token。
func assertNoToken(t *testing.T, s string) {
	t.Helper()
	botID, secret, _ := strings.Cut(fakeBotToken, ":")
	for _, needle := range []string{fakeBotToken, secret} {
		if strings.Contains(s, needle) {
			t.Fatalf("文本泄漏了 Bot Token（片段 %q）:\n%s", needle, s)
		}
	}
	// bot_id 单独出现不算泄漏，但它出现说明整条 URL 可能进来了——
	// 那意味着脱敏是按整串匹配的，遇到任何变形就会失效。
	if strings.Contains(s, botID+":") {
		t.Fatalf("文本疑似包含完整 Bot Token 前缀:\n%s", s)
	}
}

// TestTelegramNotifierResolvesTokenPerCall：每次投递现解析凭据，
// 不缓存——凭据会轮换，握着的字符串不会知道。
func TestTelegramNotifierResolvesTokenPerCall(t *testing.T) {
	resolved := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	n, err := NewTelegramNotifier(TelegramOptions{
		TokenRef: secrets.MustCredentialRef(testTokenRef),
		ChatID:   "-100",
		Secrets:  countingProvider{inner: tokenProvider(t, fakeBotToken), calls: &resolved},
		BaseURL:  srv.URL,
	})
	if err != nil {
		t.Fatalf("NewTelegramNotifier: %v", err)
	}
	for range 3 {
		if err := n.Notify(context.Background(), testAlert()); err != nil {
			t.Fatalf("Notify: %v", err)
		}
	}
	if resolved != 3 {
		t.Fatalf("凭据解析次数 = %d, want 3（每次投递现解析）", resolved)
	}
}

type countingProvider struct {
	inner secrets.SecretProvider
	calls *int
}

func (c countingProvider) Resolve(ctx context.Context, ref secrets.CredentialRef, purpose string) (secrets.SecretValue, error) {
	*c.calls++
	return c.inner.Resolve(ctx, ref, purpose)
}

func (c countingProvider) Metadata(ctx context.Context, ref secrets.CredentialRef) (secrets.SecretMetadata, error) {
	return c.inner.Metadata(ctx, ref)
}

// TestTelegramNotifierRejectsIncompleteConfig：配不全就报错，
// 不返回一个「什么都不做」的实例——静默不投递的渠道最危险。
func TestTelegramNotifierRejectsIncompleteConfig(t *testing.T) {
	full := TelegramOptions{
		TokenRef: secrets.MustCredentialRef(testTokenRef),
		ChatID:   "-100",
		Secrets:  tokenProvider(t, fakeBotToken),
	}
	cases := map[string]func(o *TelegramOptions){
		"缺 TokenRef":       func(o *TelegramOptions) { o.TokenRef = secrets.CredentialRef{} },
		"缺 ChatID":         func(o *TelegramOptions) { o.ChatID = "  " },
		"缺 SecretProvider": func(o *TelegramOptions) { o.Secrets = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			opts := full
			mutate(&opts)
			if _, err := NewTelegramNotifier(opts); err == nil {
				t.Fatal("配置不全应当场报错")
			}
		})
	}
}

// TestTelegramNotifierFailsWhenSecretMissing：解析不出凭据时报错，
// 且错误里只有引用（引用不是秘密），没有任何值。
func TestTelegramNotifierFailsWhenSecretMissing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	err := newTelegram(t, srv.URL, "").Notify(context.Background(), testAlert())
	if err == nil {
		t.Fatal("解析不出凭据应报错")
	}
	if !strings.Contains(err.Error(), testTokenRef) {
		t.Fatalf("错误应指名是哪个引用（引用本身不是秘密）: %v", err)
	}
	assertNoToken(t, err.Error())
}

// TestWebhookNotifierRequiresHTTPS：告警正文带环境名与余额，明文过网
// 等于广播运营态势。
func TestWebhookNotifierRequiresHTTPS(t *testing.T) {
	for _, raw := range []string{"http://example.com/hook", "ftp://example.com", "  ", "://broken"} {
		if _, err := NewWebhookNotifier(raw, nil); err == nil {
			t.Fatalf("%q 应被拒绝", raw)
		}
	}
	if _, err := NewWebhookNotifier("https://example.com/hook", nil); err != nil {
		t.Fatalf("合法 https 地址应被接受: %v", err)
	}
}

// TestWebhookNotifierNeverLeaksURL：Webhook 地址本身常常就是凭据
// （Slack / 飞书的 incoming webhook 里带 token）。错误里不许出现它。
func TestWebhookNotifierNeverLeaksURL(t *testing.T) {
	const secretPath = "/services/T00000/B00000/XXXXsecretXXXX"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		// 自建端点把请求路径回显进错误页——常见且完全合法的行为。
		_, _ = io.WriteString(w, "failed handling "+r.URL.Path)
	}))
	defer srv.Close()

	n, err := NewWebhookNotifier(srv.URL+secretPath, srv.Client())
	if err != nil {
		t.Fatalf("NewWebhookNotifier: %v", err)
	}
	notifyErr := n.Notify(context.Background(), testAlert())
	if notifyErr == nil {
		t.Fatal("HTTP 500 应算投递失败")
	}
	if strings.Contains(notifyErr.Error(), secretPath) || strings.Contains(notifyErr.Error(), "XXXXsecretXXXX") {
		t.Fatalf("错误泄漏了 Webhook 地址: %v", notifyErr)
	}
	if !strings.Contains(notifyErr.Error(), "500") {
		t.Fatalf("错误应保留状态码以便排查: %v", notifyErr)
	}

	// 连不上时同样不许泄漏——那条路径上的错误由 net/http 生成。
	srv.Close()
	unreachable := n.Notify(context.Background(), testAlert())
	if unreachable == nil {
		t.Fatal("连不上应算投递失败")
	}
	if strings.Contains(unreachable.Error(), secretPath) {
		t.Fatalf("网络错误泄漏了 Webhook 地址: %v", unreachable)
	}
}

// TestWebhookNotifierPostsAlertPayload：投递体的形状与 API 响应对齐。
func TestWebhookNotifierPostsAlertPayload(t *testing.T) {
	var got webhookPayload
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	n, err := NewWebhookNotifier(srv.URL+"/hook", srv.Client())
	if err != nil {
		t.Fatalf("NewWebhookNotifier: %v", err)
	}
	a := testAlert()
	if err := n.Notify(context.Background(), a); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got.AlertID != a.ID.String() || got.RuleKey != a.RuleKey || got.Severity != "critical" {
		t.Fatalf("投递体不完整: %+v", got)
	}
	if got.FireCount != 4 || got.Environment != "production" {
		t.Fatalf("投递体字段不对: %+v", got)
	}
}

const weComTestRef = "secret://alerts/wecom-webhook"

// weComWebhookProvider 返回一个只认识测试引用的 SecretProvider，解析结果是
// 给定的 webhook 地址；地址为空模拟「凭据还没在后台填」。
func weComWebhookProvider(t *testing.T, webhookURL string) secrets.SecretProvider {
	t.Helper()
	provider, err := secrets.NewEnvProvider(
		map[string]string{weComTestRef: "XM_TEST_ALERT_WECOM_WEBHOOK"},
		secrets.WithLookup(func(name string) (string, bool) {
			if name == "XM_TEST_ALERT_WECOM_WEBHOOK" && webhookURL != "" {
				return webhookURL, true
			}
			return "", false
		}),
	)
	if err != nil {
		t.Fatalf("构造 Provider: %v", err)
	}
	return provider
}

// newWeCom 构造指向 httptest 假企微端点的投递器。client 为 nil 时用一个
// 带超时的默认客户端（解析凭据失败等不需要真实发请求的用例够用）。
func newWeCom(t *testing.T, webhookURL string, client *http.Client) *WeComNotifier {
	t.Helper()
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	n, err := NewWeComNotifier(WeComOptions{
		WebhookRef: secrets.MustCredentialRef(weComTestRef),
		Secrets:    weComWebhookProvider(t, webhookURL),
		Client:     client,
	})
	if err != nil {
		t.Fatalf("NewWeComNotifier: %v", err)
	}
	return n
}

// TestWeComNotifierSendsMessage：正常路径——msgtype 固定为 markdown，
// content 里带齐环境、严重度、规则、指标键与时间。
func TestWeComNotifierSendsMessage(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"errcode":0,"errmsg":"ok"}`)
	}))
	defer srv.Close()

	webhookURL := srv.URL + "/cgi-bin/webhook/send?key=test-only"
	if err := newWeCom(t, webhookURL, srv.Client()).Notify(context.Background(), testAlert()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if gotBody["msgtype"] != "markdown" {
		t.Fatalf("msgtype = %v, want markdown", gotBody["msgtype"])
	}
	markdown, _ := gotBody["markdown"].(map[string]any)
	content, _ := markdown["content"].(string)
	// 信封（XM-NOTIFY-ENVELOPE）：徽标 + 中文严重度 + 环境 + 类型编号 + 处理入口，
	// 正文字段一个不少。「严重」取代了此前的 "CRITICAL"——同一个 Severity，
	// 面向的是群里的人，不是日志。
	for _, want := range []string{
		"【星芒·告警】", "严重", revenueMetric, "production", RuleMetricSyncFailed, "累计 4 次",
		"XM-ALERT-" + RuleMetricSyncFailed, "管理后台 → 告警与故障",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("markdown content 缺少 %q:\n%s", want, content)
		}
	}
}

// TestWeComNotifierNeverLeaksWebhookURL 是本渠道最重要的一条测试
// （宪法 7 条）。与 Telegram 不同的是泄漏对象：那边是 token 长在 URL 路径
// 里，这边是**整个 Webhook 地址就是凭据**——地址里的 key 查询参数一旦
// 出现在 notify_error 或日志里，效果等同于泄漏完整凭据。
func TestWeComNotifierNeverLeaksWebhookURL(t *testing.T) {
	const secretQuery = "key=leak-canary-0000000000"
	cases := []struct {
		name        string
		handler     http.HandlerFunc
		closeServer bool
	}{
		{
			name: "errcode 非 0",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `{"errcode":93000,"errmsg":"invalid webhook url"}`)
			},
		},
		{
			name: "HTTP 500",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, "internal error")
			},
		},
		{
			name: "响应不是合法 JSON",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, `<html>gateway error</html>`)
			},
		},
		{
			name: "上游把请求 URL 回显进错误描述",
			handler: func(w http.ResponseWriter, r *http.Request) {
				// 一个把完整请求 URL（含 key 查询参数）写回错误页的网关，
				// 足以让「不把 err 往外冒」这条纪律破功——响应体也必须脱敏。
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, `{"errcode":-1,"errmsg":"upstream failed for `+r.URL.RequestURI()+`"}`)
			},
		},
		{
			name:        "连不上（错误由 net/http 生成，含完整 URL）",
			closeServer: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := tc.handler
			if handler == nil {
				handler = func(http.ResponseWriter, *http.Request) {}
			}
			srv := httptest.NewTLSServer(handler)
			webhookURL := srv.URL + "/cgi-bin/webhook/send?" + secretQuery
			client := srv.Client()
			if tc.closeServer {
				srv.Close()
			} else {
				defer srv.Close()
			}

			err := newWeCom(t, webhookURL, client).Notify(context.Background(), testAlert())
			if err == nil {
				t.Fatal("期望投递失败")
			}
			if strings.Contains(err.Error(), secretQuery) {
				t.Fatalf("错误泄漏了 Webhook 地址: %v", err)
			}
			if sanitized := SanitizeNotifyError(err); strings.Contains(sanitized, secretQuery) {
				t.Fatalf("落库文本泄漏了 Webhook 地址: %q", sanitized)
			}
		})
	}
}

// TestWeComNotifierHandlesTimeout：请求超时是团队要求覆盖的四类响应之一
// （errcode 0 / 非 0 / 超时 / 非 JSON），且与「连不上」是不同的运行时路径
// ——这里 TCP 连接能建立，只是上游一直不回包。用一个挂住的 handler +
// 很短的 context 超时模拟，不真的等 defaultNotifyTimeout 的 10 秒。
func TestWeComNotifierHandlesTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-block // 挂住到测试收尾——defer 顺序保证先放行 handler，再关服务器。
	}))
	defer srv.Close()
	defer close(block)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := newWeCom(t, srv.URL+"/cgi-bin/webhook/send?key=test-only", srv.Client()).
		Notify(ctx, testAlert())
	if err == nil {
		t.Fatal("超时应算投递失败")
	}
	if !strings.HasPrefix(err.Error(), "wecom:") {
		t.Fatalf("错误应带 wecom 前缀: %v", err)
	}
}

// TestWeComNotifierReportsErrCodeOnFailure：非 0 的 errcode 必须原样出现在
// 错误里——运维要能不翻代码就知道是企微侧拒收了什么（规格 §9.4 扩展）。
func TestWeComNotifierReportsErrCodeOnFailure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errcode":45009,"errmsg":"content too long"}`)
	}))
	defer srv.Close()

	err := newWeCom(t, srv.URL+"/cgi-bin/webhook/send?key=test-only", srv.Client()).
		Notify(context.Background(), testAlert())
	if err == nil {
		t.Fatal("errcode 非 0 应算投递失败")
	}
	if !strings.Contains(err.Error(), "45009") {
		t.Fatalf("错误应带上游 errcode: %v", err)
	}
}

// TestWeComNotifierResolvesWebhookPerCall：每次投递现解析凭据，不缓存——
// 凭据会轮换，握着的字符串不会知道（与 Telegram 同一条纪律）。
func TestWeComNotifierResolvesWebhookPerCall(t *testing.T) {
	resolved := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errcode":0}`)
	}))
	defer srv.Close()

	n, err := NewWeComNotifier(WeComOptions{
		WebhookRef: secrets.MustCredentialRef(weComTestRef),
		Secrets: countingProvider{
			inner: weComWebhookProvider(t, srv.URL+"/cgi-bin/webhook/send?key=test-only"),
			calls: &resolved,
		},
		Client: srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewWeComNotifier: %v", err)
	}
	for range 3 {
		if err := n.Notify(context.Background(), testAlert()); err != nil {
			t.Fatalf("Notify: %v", err)
		}
	}
	if resolved != 3 {
		t.Fatalf("凭据解析次数 = %d, want 3（每次投递现解析）", resolved)
	}
}

// TestWeComNotifierRejectsIncompleteConfig：配不全就报错，不返回一个
// 「什么都不做」的实例——静默不投递的渠道最危险。
func TestWeComNotifierRejectsIncompleteConfig(t *testing.T) {
	full := WeComOptions{
		WebhookRef: secrets.MustCredentialRef(weComTestRef),
		Secrets:    weComWebhookProvider(t, "https://example.com/webhook?key=test-only"),
	}
	cases := map[string]func(o *WeComOptions){
		"缺 WebhookRef":     func(o *WeComOptions) { o.WebhookRef = secrets.CredentialRef{} },
		"缺 SecretProvider": func(o *WeComOptions) { o.Secrets = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			opts := full
			mutate(&opts)
			if _, err := NewWeComNotifier(opts); err == nil {
				t.Fatal("配置不全应当场报错")
			}
		})
	}
}

// TestWeComNotifierFailsWhenSecretMissing：解析不出凭据时报错，且错误里
// 只有引用（引用不是秘密），没有任何值——覆盖「ref 配了但还没在后台粘贴
// 凭据」这个预期中的过渡态。
func TestWeComNotifierFailsWhenSecretMissing(t *testing.T) {
	err := newWeCom(t, "", nil).Notify(context.Background(), testAlert())
	if err == nil {
		t.Fatal("解析不出凭据应报错")
	}
	if !strings.Contains(err.Error(), weComTestRef) {
		t.Fatalf("错误应指名是哪个引用（引用本身不是秘密）: %v", err)
	}
}

// TestWeComNotifierRejectsNonHTTPSResolvedURL：解析出的地址不是合法 https
// URL 时拒绝发送，且错误不回显该地址——地址本身就是凭据。这项校验只能在
// 发送时做（构造阶段还不知道解析结果是什么），与 WebhookNotifier 在构造时
// 校验静态配置值不同。
func TestWeComNotifierRejectsNonHTTPSResolvedURL(t *testing.T) {
	for _, raw := range []string{
		"http://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-only",
		"ftp://example.com",
		"   ",
		"://broken",
	} {
		err := newWeCom(t, raw, nil).Notify(context.Background(), testAlert())
		if err == nil {
			t.Fatalf("解析出的地址 %q 应被拒绝", raw)
		}
		if strings.TrimSpace(raw) != "" && strings.Contains(err.Error(), strings.TrimSpace(raw)) {
			t.Fatalf("错误不该回显解析出的地址: %v", err)
		}
	}
}

// TestFormatWeComMarkdownTruncatesTo4096Bytes：企微群机器人 markdown 消息
// content 的官方上限是**字节**，UTF-8 下中文截断更容易越界，必须按字节量。
func TestFormatWeComMarkdownTruncatesTo4096Bytes(t *testing.T) {
	a := testAlert()
	a.Detail = strings.Repeat("详情文本很长很长很长。", 1000)
	content := FormatWeComMarkdown(a)
	if n := len(content); n > weComMaxContentBytes {
		t.Fatalf("content 字节数 = %d，超过企微上限 %d", n, weComMaxContentBytes)
	}
	if !strings.Contains(content, "已截断") {
		t.Fatalf("超限应如实标注截断（宪法 12 条）: %q", content[:80])
	}
	if !utf8.ValidString(content) {
		t.Fatal("截断结果必须是合法 UTF-8（不能切断多字节字符）")
	}
}

// TestFormatWeComMarkdownIncludesAllRequiredFields：内容须含环境、严重度、
// 规则、指标键、详情（当前值落在这里）与两个时间戳。
func TestFormatWeComMarkdownIncludesAllRequiredFields(t *testing.T) {
	content := FormatWeComMarkdown(testAlert())
	for _, want := range []string{
		"严重", "production", RuleMetricSyncFailed, revenueMetric,
		"2026-08-27T05:00:00Z", "2026-08-27T05:03:00Z", "累计 4 次",
		"来源 sub2api-prod，错误码 timeout。",
		"XM-ALERT-" + RuleMetricSyncFailed, "管理后台 → 告警与故障",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("content 缺少 %q:\n%s", want, content)
		}
	}
	// 首行是信封头部（域徽标 + 严重度 + 环境）——闭集文案，可以带标记；
	// 其余每行都是引用块，上游可控的字符串只出现在那里。
	rendered := strings.Split(content, "\n")
	if !strings.HasPrefix(rendered[0], "【星芒·告警】") {
		t.Fatalf("首行应是域徽标: %q", rendered[0])
	}
	for _, line := range rendered[1:] {
		if line != "" && !strings.HasPrefix(line, "> ") {
			t.Fatalf("正文每行应以引用块 `> ` 开头: %q", line)
		}
	}
}

// TestMultiNotifierDeliversIfAnyChannelSucceeds：一个通了就算送到。
//
// 取舍见 MultiNotifier.Notify 的注释：整体判失败会让下一轮重试，
// 于是通着的那个渠道每 60 秒收到一条重复消息。
func TestMultiNotifierDeliversIfAnyChannelSucceeds(t *testing.T) {
	ok := &stubNotifier{name: "ok"}
	bad := &stubNotifier{name: "bad", err: errors.New("boom")}

	m := NewMultiNotifier(discardLogger(), bad, ok)
	if err := m.Notify(context.Background(), testAlert()); err != nil {
		t.Fatalf("有渠道成功时不该返回错误: %v", err)
	}
	if ok.calls != 1 || bad.calls != 1 {
		t.Fatalf("每个渠道都该被尝试一次: ok=%d bad=%d", ok.calls, bad.calls)
	}

	allBad := NewMultiNotifier(discardLogger(), bad, &stubNotifier{name: "bad2", err: errors.New("boom2")})
	err := allBad.Notify(context.Background(), testAlert())
	if err == nil {
		t.Fatal("全部失败才算失败")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Fatalf("错误应指名失败的渠道: %v", err)
	}
}

// TestMultiNotifierWithNoChannelsReportsErrNoNotifier：一个渠道都没配是
// 一个**事实**而不是故障——调用方据此打 warn「仅落库未投递」，不标 failed。
func TestMultiNotifierWithNoChannelsReportsErrNoNotifier(t *testing.T) {
	m := NewMultiNotifier(discardLogger())
	if m.Len() != 0 {
		t.Fatalf("Len = %d", m.Len())
	}
	err := m.Notify(context.Background(), testAlert())
	if !errors.Is(err, ErrNoNotifier) {
		t.Fatalf("应返回 ErrNoNotifier，实际 %v", err)
	}
	// nil 渠道要被过滤掉，不能在 Notify 里 panic。
	if NewMultiNotifier(discardLogger(), nil, nil).Len() != 0 {
		t.Fatal("nil 渠道应被过滤")
	}
}

type stubNotifier struct {
	name  string
	err   error
	calls int
}

func (s *stubNotifier) Name() string { return s.name }

func (s *stubNotifier) Notify(context.Context, Alert) error {
	s.calls++
	return s.err
}

// TestSanitizeNotifyErrorTruncatesAndFlattens：notify_error 会落库、
// 会进前端列表，必须压平换行并截断。
func TestSanitizeNotifyErrorTruncatesAndFlattens(t *testing.T) {
	if got := SanitizeNotifyError(nil); got != "" {
		t.Fatalf("nil 错误应返回空串，实际 %q", got)
	}
	multiline := errors.New("第一行\n第二行\t第三行")
	if got := SanitizeNotifyError(multiline); strings.ContainsAny(got, "\n\t") {
		t.Fatalf("应压平换行与制表符: %q", got)
	}

	long := errors.New(strings.Repeat("错", 1000))
	got := SanitizeNotifyError(long)
	if len([]rune(got)) > maxNotifyErrorLen+10 {
		t.Fatalf("应截断到 %d 个字符左右，实际 %d", maxNotifyErrorLen, len([]rune(got)))
	}
	if !strings.Contains(got, "已截断") {
		t.Fatalf("截断必须如实标注（宪法 12 条）: %q", got)
	}
	// 按 rune 截断：按字节截会切出半个汉字，前端显示成替换符。
	if !strings.HasPrefix(got, "错错") {
		t.Fatalf("截断结果不是合法 UTF-8 前缀: %q", got)
	}
}

// TestFormatMessageHasNoMarkupToEscape：正文不带 Markdown/HTML 标记。
// 带标记就得转义，而漏转义的后果是消息发不出去。
func TestFormatMessageHasNoMarkupToEscape(t *testing.T) {
	a := testAlert()
	a.Title = "指标 *特殊* _名字_ [带] `标记`"
	msg := FormatMessage(a)
	if !strings.Contains(msg, a.Title) {
		t.Fatalf("标题应原样出现（不转义、不剥离）:\n%s", msg)
	}
	if strings.Contains(msg, "parse_mode") {
		t.Fatal("不该引入 parse_mode")
	}
}
