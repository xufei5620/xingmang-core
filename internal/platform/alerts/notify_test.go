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
