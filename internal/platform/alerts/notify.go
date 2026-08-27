package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// ErrNoNotifier 表示一个投递渠道都没配置。
//
// 它是一个**事实**而不是故障：本地开发与刚起的环境本来就还没配 Telegram。
// 调用方（投递环节）据此打一条 warn「仅落库未投递」，不把告警标成 failed——
// 标成 failed 会让 notify_error 里写满「没配渠道」，把真正的投递故障淹掉。
var ErrNoNotifier = errors.New("没有配置任何告警投递渠道")

const (
	// redactedMarker 与 secrets.SecretValue 的脱敏标记保持一致的观感。
	redactedMarker = "[REDACTED]"
	// maxNotifyErrorLen 限制落库的失败原因长度。
	//
	// notify_error 会原样进数据库、进 API 响应、进前端列表。上游返回一整页
	// HTML 错误页是常有的事，不截断的话一条告警能把列表撑爆。
	maxNotifyErrorLen = 300
	// defaultNotifyTimeout 是单次投递请求的超时。
	//
	// 必须有：一个挂住的投递会把整轮评估拖过 River 的 JobTimeout，
	// 结果是这一轮**什么都没投出去也什么都没记下来**（规格 §18.1-4）。
	defaultNotifyTimeout = 10 * time.Second
	// telegramDefaultBaseURL 是 Bot API 的官方地址。测试注入 httptest 地址。
	telegramDefaultBaseURL = "https://api.telegram.org"
)

// Notifier 把一条告警投递到一个外部渠道（规格 §9.4 平台内部告警）。
//
// 实现方的**唯一硬约束**：返回的 error 与写出的日志里绝不能出现任何凭据
// 材料（宪法 7 条）。Telegram 的 token 直接长在 URL 里，
// 而 net/http 的错误默认带 URL——这条约束因此不是自动成立的，
// 每个实现都必须显式脱敏。TestTelegramNotifierNeverLeaksToken 机械地守住它。
type Notifier interface {
	// Name 是渠道名，进日志与 notify_error 前缀。它不是秘密。
	Name() string
	// Notify 投递一条告警。返回 nil 表示对方确认收下了。
	Notify(ctx context.Context, a Alert) error
}

// FormatMessage 把一条告警渲染成纯文本（Telegram 与日志共用）。
//
// 刻意不带 Markdown/HTML 标记：告警标题里会出现指标键、渠道名这类
// 由上游决定的字符串，带标记就得转义，而漏转义的后果是消息发不出去——
// 一条发不出去的告警比一条丑的告警糟糕得多。
func FormatMessage(a Alert) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s] %s\n", strings.ToUpper(string(a.Severity)), a.Title)
	fmt.Fprintf(&b, "环境: %s\n", a.Environment)
	fmt.Fprintf(&b, "规则: %s\n", a.RuleKey)
	fmt.Fprintf(&b, "状态: %s\n", a.Status)
	fmt.Fprintf(&b, "首次发现: %s\n", a.OpenedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "最近发现: %s（累计 %d 次）\n",
		a.LastSeenAt.UTC().Format(time.RFC3339), a.FireCount)
	if a.SourceMetricKey != "" {
		fmt.Fprintf(&b, "指标: %s\n", a.SourceMetricKey)
	}
	if a.Detail != "" {
		fmt.Fprintf(&b, "详情: %s", a.Detail)
	}
	return strings.TrimRight(b.String(), "\n")
}

// redact 把给定的敏感串从文本里抹掉。
//
// 它是**最后一道**防线而不是第一道：正确的做法始终是不把凭据放进错误里。
// 但 net/http 会把请求 URL 塞进它自己造的错误，那个错误不经过我们的手，
// 所以每一条要冒泡出去的文本都得再过一遍这里。
func redact(s string, sensitive ...string) string {
	for _, secret := range sensitive {
		if secret == "" {
			continue
		}
		s = strings.ReplaceAll(s, secret, redactedMarker)
	}
	return s
}

// truncate 把落库文本截到上限，并如实标注被截断。
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// 按字节截会切断 UTF-8 多字节字符，落库是合法的（bytea 不校验）但
	// 前端会显示成一个替换符。按 rune 截。
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…（已截断）"
}

// SanitizeNotifyError 把一个投递错误整理成可以落库、可以回前端的文本。
//
// 三件事：抹掉凭据、压平换行、截断。调用方（投递环节）在写
// alert.notify_error 之前必须过这里——那一列会被前端原样显示。
func SanitizeNotifyError(err error, sensitive ...string) string {
	if err == nil {
		return ""
	}
	s := redact(err.Error(), sensitive...)
	s = strings.Join(strings.Fields(s), " ")
	return truncate(s, maxNotifyErrorLen)
}

// MultiNotifier 把一条告警扇出到全部已配置的渠道。
type MultiNotifier struct {
	notifiers []Notifier
	logger    *slog.Logger
}

// NewMultiNotifier 组合零个或多个渠道。一个都没有时 Notify 返回 ErrNoNotifier。
func NewMultiNotifier(logger *slog.Logger, ns ...Notifier) *MultiNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	kept := make([]Notifier, 0, len(ns))
	for _, n := range ns {
		if n != nil {
			kept = append(kept, n)
		}
	}
	return &MultiNotifier{notifiers: kept, logger: logger}
}

// Len 返回已配置的渠道数。
func (m *MultiNotifier) Len() int { return len(m.notifiers) }

// Names 返回已配置的渠道名（进启动日志：运维必须能一眼看出告警会往哪儿投）。
func (m *MultiNotifier) Names() []string {
	out := make([]string, 0, len(m.notifiers))
	for _, n := range m.notifiers {
		out = append(out, n.Name())
	}
	return out
}

func (m *MultiNotifier) Name() string { return "multi" }

// Notify 扇出投递。
//
// **只要有一个渠道收下就算投递成功。** 这个取舍是有意的：
// 两个渠道配着，Telegram 通了、Webhook 挂了，如果整体判失败，下一轮会重试，
// 于是 Telegram 每 60 秒收到一条一模一样的消息——为了如实记录一个次要渠道的
// 故障，把主渠道变成了垃圾消息源，而收件人最终会静音它。
//
// 代价是部分失败不会进 notify_error（库层 CHECK 要求 delivered 不带错误）。
// 补偿是每一个失败的渠道都单独打一条 warn 日志，带渠道名与告警 ID——
// 「Webhook 一直投不出去」在日志里看得见，只是不占用告警本身的投递状态。
func (m *MultiNotifier) Notify(ctx context.Context, a Alert) error {
	if len(m.notifiers) == 0 {
		return ErrNoNotifier
	}
	var failures []string
	delivered := false
	for _, n := range m.notifiers {
		if err := n.Notify(ctx, a); err != nil {
			// err 已由各实现脱敏；这里不再拼接任何配置值。
			m.logger.WarnContext(ctx, "alert_notify_channel_failed",
				slog.String("module", "platform.alerts"),
				slog.String("channel", n.Name()),
				slog.String("alert_id", a.ID.String()),
				slog.String("rule_key", a.RuleKey),
				slog.String("environment", a.Environment),
				slog.String("error_code", "notify_failed"),
				slog.String("err", SanitizeNotifyError(err)),
			)
			failures = append(failures, n.Name()+": "+SanitizeNotifyError(err))
			continue
		}
		delivered = true
	}
	if delivered {
		return nil
	}
	return fmt.Errorf("全部渠道投递失败：%s", strings.Join(failures, "；"))
}

// TelegramNotifier 经 Bot API 投递（规格 §9.4）。
//
// 零新增依赖：Bot API 就是一个 POST JSON 的 HTTP 端点，标准库够用。
// 引一个 Telegram SDK 只为发一条消息，换来的是一整棵传递依赖树和一条
// 需要跟着升级的供应链（宪法 25 条）。
type TelegramNotifier struct {
	tokenRef secrets.CredentialRef
	chatID   string
	secrets  secrets.SecretProvider
	client   *http.Client
	baseURL  string
}

// TelegramOptions 是构造参数。
type TelegramOptions struct {
	// TokenRef 是 Bot Token 的引用，形如 secret://<scope>/<name>（ADR-014）。
	TokenRef secrets.CredentialRef
	// ChatID 是目标会话。它不是秘密（知道 chat_id 也发不了消息），
	// 但仍然不进错误文本——没有必要。
	ChatID string
	// Secrets 解析 TokenRef。缺它等于没有凭据。
	Secrets secrets.SecretProvider
	// Client 可注入；零值用带超时的默认客户端。
	Client *http.Client
	// BaseURL 仅供测试注入 httptest 地址；留空用官方地址。
	BaseURL string
}

// NewTelegramNotifier 构造投递器。
//
// 配置不全时返回错误而不是一个「什么都不做」的实例：一个静默不投递的
// 告警渠道，比没有渠道危险得多——运维以为配好了。
func NewTelegramNotifier(opts TelegramOptions) (*TelegramNotifier, error) {
	if opts.TokenRef.IsZero() {
		return nil, fmt.Errorf("telegram: 缺少 Bot Token 的 CredentialRef")
	}
	if strings.TrimSpace(opts.ChatID) == "" {
		return nil, fmt.Errorf("telegram: 缺少 chat_id")
	}
	if opts.Secrets == nil {
		return nil, fmt.Errorf("telegram: 缺少 SecretProvider")
	}
	base := strings.TrimSpace(opts.BaseURL)
	if base == "" {
		base = telegramDefaultBaseURL
	}
	base = strings.TrimSuffix(base, "/")
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: defaultNotifyTimeout}
	}
	return &TelegramNotifier{
		tokenRef: opts.TokenRef,
		chatID:   strings.TrimSpace(opts.ChatID),
		secrets:  opts.Secrets,
		client:   client,
		baseURL:  base,
	}, nil
}

func (t *TelegramNotifier) Name() string { return "telegram" }

type telegramResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

// Notify 投递一条告警。
//
// **token 只在构造 URL 到发出请求这一小段里以明文存在**（宪法 7 条）：
// 每次投递现解析（凭据会轮换，握着的字符串不会知道），从不存进结构体字段，
// 之后每一条要冒泡出去的文本都过一遍 redact。
//
// 为什么必须脱敏而不是「小心不要写进去」：Bot API 把 token 放在 **URL 路径**
// 里，而 net/http 造的错误长这样——
//
//	Post "https://api.telegram.org/bot123456:AAH.../sendMessage": dial tcp ...
//
// 那个错误不经过我们的手就已经带着 token 了。任何一处 `return err` 都会
// 把它送进 notify_error（会落库、会回前端），或者送进日志。
func (t *TelegramNotifier) Notify(ctx context.Context, a Alert) error {
	value, err := t.secrets.Resolve(ctx, t.tokenRef, "alert telegram delivery")
	if err != nil {
		// 只报引用与分类，不报值。secrets 包的错误本身不含明文，
		// 但仍然经 SanitizeNotifyError 压平截断后再冒泡。
		return fmt.Errorf("telegram: 解析凭据 %s 失败: %w", t.tokenRef, err)
	}
	token := value.Reveal()
	if token == "" {
		return fmt.Errorf("telegram: 凭据 %s 解析出空值", t.tokenRef)
	}

	payload, err := json.Marshal(map[string]any{
		"chat_id":                  t.chatID,
		"text":                     FormatMessage(a),
		"disable_web_page_preview": true,
	})
	if err != nil {
		return fmt.Errorf("telegram: 编码请求体失败: %w", err)
	}

	endpoint := t.baseURL + "/bot" + token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		// NewRequestWithContext 的错误会带上 URL。
		return errors.New(redact(fmt.Sprintf("telegram: 构造请求失败: %v", err), token))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		// 这里是最危险的一处：err 里几乎必然带着完整 URL，也就带着 token。
		return errors.New(redact(fmt.Sprintf("telegram: 请求失败: %v", err), token))
	}
	defer func() { _ = resp.Body.Close() }()

	// 限流读：一个失控的上游不该把 worker 的内存吃光（规格 §18.1-4）。
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	// 响应体理论上不含 token，但它是上游控制的内容，照样过一遍脱敏——
	// 一个把请求 URL 回显进错误描述的网关就足以让这条纪律破功。
	safeBody := redact(string(body), token)

	if resp.StatusCode != http.StatusOK {
		var parsed telegramResponse
		if json.Unmarshal(body, &parsed) == nil && parsed.Description != "" {
			return fmt.Errorf("telegram: HTTP %d: %s",
				resp.StatusCode, redact(parsed.Description, token))
		}
		return fmt.Errorf("telegram: HTTP %d: %s", resp.StatusCode, truncate(safeBody, 200))
	}

	var parsed telegramResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("telegram: 响应不是合法 JSON: %s", truncate(safeBody, 200))
	}
	if !parsed.OK {
		// HTTP 200 + ok:false 是 Bot API 会给的组合，必须当失败处理——
		// 否则一条根本没发出去的告警会被记成 delivered。
		return fmt.Errorf("telegram: Bot API 拒绝: %s", redact(parsed.Description, token))
	}
	return nil
}

// WebhookNotifier 把告警 POST 给一个自建端点（规格 §9.4）。
type WebhookNotifier struct {
	endpoint string
	client   *http.Client
}

// NewWebhookNotifier 构造投递器。
//
// **强制 https**：告警正文里有环境名、指标键、余额数字，明文过网等于把
// 运营态势广播出去。与 Connector 的 endpoint 校验同一条规则（ADR-004）。
func NewWebhookNotifier(rawURL string, client *http.Client) (*WebhookNotifier, error) {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return nil, fmt.Errorf("webhook: 地址为空")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		// 解析错误会带上原始 URL，而 Webhook URL 本身常常就是凭据
		// （Slack / 飞书的 incoming webhook 地址里带 token）。不回显它。
		return nil, fmt.Errorf("webhook: 地址不是合法 URL")
	}
	if parsed.Scheme != "https" {
		return nil, fmt.Errorf("webhook: 地址必须是 https，实际 scheme=%q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("webhook: 地址缺少主机名")
	}
	if client == nil {
		client = &http.Client{Timeout: defaultNotifyTimeout}
	}
	return &WebhookNotifier{endpoint: raw, client: client}, nil
}

func (w *WebhookNotifier) Name() string { return "webhook" }

// webhookPayload 是投递给自建端点的结构。
//
// 字段与 GET /api/v1/alerts 的响应对齐，让接收端只需要认识一套形状。
// 不含任何凭据、不含 value_json 原文。
type webhookPayload struct {
	AlertID         string `json:"alert_id"`
	RuleKey         string `json:"rule_key"`
	DedupKey        string `json:"dedup_key"`
	Severity        string `json:"severity"`
	Status          string `json:"status"`
	Title           string `json:"title"`
	Detail          string `json:"detail"`
	Environment     string `json:"environment"`
	SourceMetricKey string `json:"source_metric_key"`
	OpenedAt        string `json:"opened_at"`
	LastSeenAt      string `json:"last_seen_at"`
	FireCount       int32  `json:"fire_count"`
}

// Notify 投递一条告警。
//
// 与 Telegram 同一条纪律，原因不同：那边是 token 在 URL 里，这边是
// **整个 URL 可能就是凭据**（Slack / 飞书的 incoming webhook 地址）。
// 因此这里连脱敏都不做——直接不把 net/http 的原始错误往外冒，
// 只报分类与状态码。运维要查具体地址去看配置，不该从告警列表里读到它。
func (w *WebhookNotifier) Notify(ctx context.Context, a Alert) error {
	payload, err := json.Marshal(webhookPayload{
		AlertID:         a.ID.String(),
		RuleKey:         a.RuleKey,
		DedupKey:        a.DedupKey,
		Severity:        string(a.Severity),
		Status:          string(a.Status),
		Title:           a.Title,
		Detail:          a.Detail,
		Environment:     a.Environment,
		SourceMetricKey: a.SourceMetricKey,
		OpenedAt:        a.OpenedAt.UTC().Format(time.RFC3339),
		LastSeenAt:      a.LastSeenAt.UTC().Format(time.RFC3339),
		FireCount:       a.FireCount,
	})
	if err != nil {
		return fmt.Errorf("webhook: 编码请求体失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.endpoint, bytes.NewReader(payload))
	if err != nil {
		return errors.New("webhook: 构造请求失败")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		// 刻意丢弃 err 的文本：它带着完整 URL。分类信息由 ctx 是否取消来分。
		if ctx.Err() != nil {
			return fmt.Errorf("webhook: 请求被取消或超时")
		}
		return fmt.Errorf("webhook: 请求失败（网络不可达或 TLS 握手失败）")
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 只报状态码，不报响应体：自建端点的错误页可能回显请求 URL。
		return fmt.Errorf("webhook: HTTP %d", resp.StatusCode)
	}
	return nil
}
