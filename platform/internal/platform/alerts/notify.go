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
	"unicode/utf8"

	"github.com/xufei5620/xingmang-platform/internal/platform/notify"
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
	// weComMaxContentBytes 是企业微信群机器人 markdown 消息 content 字段的
	// 官方长度上限。单位是**字节**，不是字符——UTF-8 下一个汉字占 3 字节，
	// 按 rune 数截断仍可能越过这个上限，上游会直接拒收整条消息。
	weComMaxContentBytes = 4096
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
	fmt.Fprintf(&b, "首次发现: %s\n", describeFirstOpened(a))
	fmt.Fprintf(&b, "最近发现: %s（已评估 %d 轮）\n",
		a.LastSeenAt.UTC().Format(time.RFC3339), a.FireCount)
	fmt.Fprintf(&b, "触发次数: %s\n", describeTriggerCount(a))
	if a.SourceMetricKey != "" {
		fmt.Fprintf(&b, "指标: %s\n", a.SourceMetricKey)
	}
	if a.Detail != "" {
		fmt.Fprintf(&b, "详情: %s", a.Detail)
	}
	return strings.TrimRight(b.String(), "\n")
}

// FormatWeComMarkdown 把一条告警渲染成企业微信群机器人 markdown 消息的
// content 字段（XM-ALERT-WECOM；信封自 XM-NOTIFY-ENVELOPE 起统一）。
//
// 排版纪律已搬进 internal/platform/notify，理由不变：闭集（域徽标、严重度、
// 环境、编号、处理入口）用 markdown 语法是安全的；Title / Detail / RuleKey /
// SourceMetricKey 这些**由上游数据决定**的字符串一律进 `> ` 引用块且不带任何
// 标记——带标记就得转义，漏转义的后果是排版错乱甚至整条发不出去。截断也在
// 那里按**字节**做（企微 content 上限 4096 字节，按 rune 截仍会越界）。
//
// 本通道与卡片、接码两条**不合并**（三个领域的字段完全不同）；统一的只是信封：
// 运营把同一个 Webhook 地址填进三个凭据之后，同一个群里的每条消息都能自己说清
// 自己是哪个域、多严重、哪个环境、编号是什么、该去哪处理。
func FormatWeComMarkdown(a Alert) string {
	return notify.RenderWeComMarkdown(notify.Envelope{
		Domain:      notify.DomainAlert,
		Kind:        a.RuleKey,
		Severity:    notify.Severity(a.Severity),
		Environment: a.Environment,
		Title:       a.Title,
		Lines: []notify.Line{
			{Label: "规则", Value: a.RuleKey},
			{Label: "状态", Value: string(a.Status)},
			{Label: "首次发现", Value: describeFirstOpened(a)},
			{Label: "最近发现", Value: fmt.Sprintf("%s（已评估 %d 轮）",
				a.LastSeenAt.UTC().Format(time.RFC3339), a.FireCount)},
			{Label: "触发次数", Value: describeTriggerCount(a)},
			{Label: "指标", Value: a.SourceMetricKey},
			{Label: "详情", Value: a.Detail},
		},
		Action: "管理后台 → 告警与故障",
	})
}

// describeFirstOpened 渲染「这个问题从什么时候开始的，到现在多久了」。
//
// 2026-09-08 的教训在这里同样适用，而且更要紧：管理端那一路已经把
// fire_count（评估轮数）与 trigger_count（触发次数）分开了，但半夜真正被人
// 读到的是这条推送。它此前写的是「最近发现: …（累计 669 次）」——用的就是
// FireCount，也就是把「持续了 668 分钟」念成「触发了 669 次」。
//
// 时刻取 EffectiveFirstOpenedAt 而不是 OpenedAt：复发链上 OpenedAt 只是
// 「这一行」什么时候开的，而人想知道的是这件事什么时候开始的。兜底规则只写在
// alerts.Alert 那一处，这里不复刻。
func describeFirstOpened(a Alert) string {
	first, estimated := a.EffectiveFirstOpenedAt()
	line := first.Format(time.RFC3339)
	if lasted := a.LastSeenAt.UTC().Sub(first); lasted > 0 {
		line += fmt.Sprintf("（已持续 %s）", lasted.Round(time.Minute))
	}
	if estimated {
		// 短一句，但不能没有：这条告警早于 first_opened_at 上线，上面那个
		// 时刻是按本行的 opened_at 兜的底。完整口径在 API 的
		// first_opened_at_estimated 字段与 docs/modules/alerts/README.md。
		line += "（首开时刻为估计值）"
	}
	return line
}

// describeTriggerCount 渲染「真正触发过几次」。
//
// nil 写「—」而不是 0：一条正在响的告警触发过 0 次是不可能的，那是个看起来
// 像真答案的假答案（宪法 12 条）。它与 API 的 trigger_count 空值口径一致。
func describeTriggerCount(a Alert) string {
	if a.TriggerCount == nil {
		return "—（未记录）"
	}
	return fmt.Sprintf("%d 次", *a.TriggerCount)
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

// truncateBytes 把 s 截到最多 max 字节，不切断 UTF-8 多字节字符，并如实
// 标注被截断。与 truncate 的区别：那个函数按 rune 数截断，服务的是落库的
// notify_error（数据库列没有字节上限顾虑）；这个函数服务的是企微 markdown
// 内容——上游按**字节**计数，按 rune 截断仍可能越界导致整条消息被拒收。
func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const marker = "…（已截断）"
	budget := max - len(marker)
	if budget < 0 {
		budget = 0
	}
	cut := s
	if len(cut) > budget {
		cut = cut[:budget]
	}
	// 按字节截可能落在一个多字节字符中间；回退到上一个合法的 rune 边界。
	for len(cut) > 0 {
		r, size := utf8.DecodeLastRuneInString(cut)
		if r != utf8.RuneError || size != 1 {
			break
		}
		cut = cut[:len(cut)-1]
	}
	return cut + marker
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
	// FireCount 是**评估轮数**，不是发生次数。名字与语义都不改——接收端
	// 已经在消费它，改名会让它当场炸。要「触发几次」用下一个字段。
	FireCount int32 `json:"fire_count"`
	// TriggerCount / FirstOpenedAt / FirstOpenedAtEstimated 与
	// GET /api/v1/alerts 的同名字段**逐字同义**（含空值口径：trigger_count
	// 可为 null，first_opened_at 恒非空）。
	//
	// 补上它们是因为上面那句「字段与 API 响应对齐」在子片 B 加完三个字段
	// 之后就不成立了——一条说自己与别处对齐的注释，在别处变了之后不会报错，
	// 只会安静地给接收端一个旧形状。
	TriggerCount           *int32 `json:"trigger_count"`
	FirstOpenedAt          string `json:"first_opened_at"`
	FirstOpenedAtEstimated bool   `json:"first_opened_at_estimated"`
}

// Notify 投递一条告警。
//
// 与 Telegram 同一条纪律，原因不同：那边是 token 在 URL 里，这边是
// **整个 URL 可能就是凭据**（Slack / 飞书的 incoming webhook 地址）。
// 因此这里连脱敏都不做——直接不把 net/http 的原始错误往外冒，
// 只报分类与状态码。运维要查具体地址去看配置，不该从告警列表里读到它。
func (w *WebhookNotifier) Notify(ctx context.Context, a Alert) error {
	firstOpenedAt, firstOpenedEstimated := a.EffectiveFirstOpenedAt()
	payload, err := json.Marshal(webhookPayload{
		AlertID:                a.ID.String(),
		RuleKey:                a.RuleKey,
		DedupKey:               a.DedupKey,
		Severity:               string(a.Severity),
		Status:                 string(a.Status),
		Title:                  a.Title,
		Detail:                 a.Detail,
		Environment:            a.Environment,
		SourceMetricKey:        a.SourceMetricKey,
		OpenedAt:               a.OpenedAt.UTC().Format(time.RFC3339),
		LastSeenAt:             a.LastSeenAt.UTC().Format(time.RFC3339),
		FireCount:              a.FireCount,
		TriggerCount:           a.TriggerCount,
		FirstOpenedAt:          firstOpenedAt.Format(time.RFC3339),
		FirstOpenedAtEstimated: firstOpenedEstimated,
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

// WeComNotifier 经企业微信群机器人 Webhook 投递告警（XM-ALERT-WECOM，
// 规格 §9.4 投递渠道的扩展）。
//
// 与 TelegramNotifier 同一条纪律、不同的理由：Telegram 是 token 长在 URL
// 路径里，这边是**整个 Webhook 地址就是凭据**（企微把鉴权 key 放在查询
// 参数里：.../webhook/send?key=xxx）。因此地址经 CredentialRef 解析、由
// SecretProvider 在发送那一瞬现场给出，绝不作为静态配置值出现在 .env 以外
// 的任何地方（PROJECT-CONSTITUTION 第 7 条）。这一点与本包已有的
// WebhookNotifier 不同：那边的自建端点地址允许直接来自环境变量。
type WeComNotifier struct {
	webhookRef secrets.CredentialRef
	secrets    secrets.SecretProvider
	client     *http.Client
}

// WeComOptions 是构造参数。
type WeComOptions struct {
	// WebhookRef 是群机器人 Webhook 地址（含 key）的引用，
	// 形如 secret://alerts/wecom-webhook（ADR-014）。
	WebhookRef secrets.CredentialRef
	// Secrets 解析 WebhookRef。缺它等于没有凭据。
	Secrets secrets.SecretProvider
	// Client 可注入；零值用带超时的默认客户端。
	Client *http.Client
}

// NewWeComNotifier 构造投递器。
//
// 与 NewTelegramNotifier 同一条理由：配置不全时返回错误而不是一个
// 「什么都不做」的实例——一个静默不投递的告警渠道，比没有渠道危险得多。
func NewWeComNotifier(opts WeComOptions) (*WeComNotifier, error) {
	if opts.WebhookRef.IsZero() {
		return nil, fmt.Errorf("wecom: 缺少 Webhook 地址的 CredentialRef")
	}
	if opts.Secrets == nil {
		return nil, fmt.Errorf("wecom: 缺少 SecretProvider")
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: defaultNotifyTimeout}
	}
	return &WeComNotifier{webhookRef: opts.WebhookRef, secrets: opts.Secrets, client: client}, nil
}

func (w *WeComNotifier) Name() string { return "wecom" }

// weComMarkdownPayload 是发给群机器人 Webhook 的请求体形状
// （企微开放文档：msgtype=markdown）。
type weComMarkdownPayload struct {
	MsgType  string           `json:"msgtype"`
	Markdown weComMarkdownDoc `json:"markdown"`
}

type weComMarkdownDoc struct {
	Content string `json:"content"`
}

// weComResponse 是群机器人 Webhook 的响应形状。errcode=0 才算成功；
// 非 0 时 errmsg 是上游给的原因（可能夹带上游自己的诊断信息，仍需脱敏）。
type weComResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

// Notify 投递一条告警。
//
// **地址只在解析到发出这一小段里以明文存在**（宪法 7 条）：每次投递现解析
// （凭据会轮换，握着的字符串不会知道），从不存进结构体字段，之后每一条
// 要冒泡出去的文本都过一遍 redact——原因与 Telegram 一致：net/http 造的
// 错误会带上完整请求 URL，而这里的 URL 本身就是秘密。
func (w *WeComNotifier) Notify(ctx context.Context, a Alert) error {
	value, err := w.secrets.Resolve(ctx, w.webhookRef, "alert wecom delivery")
	if err != nil {
		// 只报引用与分类，不报值——与 Telegram 同一条纪律。
		return fmt.Errorf("wecom: 解析凭据 %s 失败: %w", w.webhookRef, err)
	}
	endpoint := strings.TrimSpace(value.Reveal())
	if endpoint == "" {
		return fmt.Errorf("wecom: 凭据 %s 解析出空值", w.webhookRef)
	}
	wecomURL, err := url.Parse(endpoint)
	if err != nil || wecomURL.Scheme != "https" || wecomURL.Host == "" {
		// 不回显 endpoint：它就是凭据。只报「不是合法 https 地址」这个事实，
		// 与 Connector 的 endpoint 校验同一条规则（ADR-004）。
		return fmt.Errorf("wecom: 凭据 %s 解析出的地址不是合法的 https URL", w.webhookRef)
	}

	// redact 只按「完整 endpoint 整串出现」匹配是不够的：一个把请求信息
	// 回显进错误页的网关，常见做法是只回显 r.URL.RequestURI()（路径 + 查询
	// 串，不含 scheme/host），这时 endpoint 整串根本不会出现在文本里，
	// 但企微鉴权用的 key 就长在查询串里，同样是泄漏。所以把「完整地址」与
	// 「查询串」都当作敏感片段——两者任一出现都要被抹掉。
	// TestWeComNotifierNeverLeaksWebhookURL 的「上游把请求 URL 回显进错误
	// 描述」用例专门盯这一条，之前只传 endpoint 时在这里漏过。
	sensitive := []string{endpoint}
	if wecomURL.RawQuery != "" {
		sensitive = append(sensitive, wecomURL.RawQuery)
	}

	payload, err := json.Marshal(weComMarkdownPayload{
		MsgType:  "markdown",
		Markdown: weComMarkdownDoc{Content: FormatWeComMarkdown(a)},
	})
	if err != nil {
		return fmt.Errorf("wecom: 编码请求体失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		// NewRequestWithContext 的错误会带上 URL。
		return errors.New(redact(fmt.Sprintf("wecom: 构造请求失败: %v", err), sensitive...))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		// 这里是最危险的一处：err 里几乎必然带着完整 URL，也就带着 key。
		return errors.New(redact(fmt.Sprintf("wecom: 请求失败: %v", err), sensitive...))
	}
	defer func() { _ = resp.Body.Close() }()

	// 限流读：一个失控的上游不该把 worker 的内存吃光（规格 §18.1-4）。
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	// 响应体理论上不含地址，但它是上游控制的内容，照样过一遍脱敏——一个把
	// 请求 URL 回显进错误描述的网关就足以让这条纪律破功（Telegram 踩过同一
	// 类坑，见 TestTelegramNotifierNeverLeaksToken）。
	safeBody := redact(string(body), sensitive...)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wecom: HTTP %d: %s", resp.StatusCode, truncate(safeBody, 200))
	}

	var result weComResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("wecom: 响应不是合法 JSON: %s", truncate(safeBody, 200))
	}
	if result.ErrCode != 0 {
		// HTTP 200 + errcode!=0 是企微会给的组合（例如 93000 地址不合法、
		// 45009 内容超限），必须当失败处理——否则一条根本没发出去的告警
		// 会被记成 delivered。errmsg 是上游文案，同样过一遍脱敏。
		return fmt.Errorf("wecom: 上游拒绝，errcode=%d errmsg=%s",
			result.ErrCode, redact(result.ErrMsg, sensitive...))
	}
	return nil
}
