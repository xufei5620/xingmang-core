package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WeComSender 把一条渲染好的消息 POST 给企业微信群机器人。
//
// **整个 Webhook 地址就是凭据**：企微把鉴权 key 放在地址的查询参数里
// （.../webhook/send?key=xxx）。因此它绝不进日志、错误串或任何 API 响应。
// 下面每一条返回的错误都刻意不带地址——net/http 会把请求 URL 塞进它自己造的
// 错误，那个错误不经过我们的手，所以传输失败那条干脆不带 err。
//
// **地址在每次投递时现取**（XM-INV-NOTICE-WEBHOOK-SETTING），不是构造时定死：
// 运营在管理端换了群机器人之后，下一条通知就该用新地址，而不是等 api 重启。
type WeComSender struct {
	resolve func(context.Context) (string, error)
	client  *http.Client
}

// NewWeComSender 构造发送器。resolve 由装配处注入（今天是从管理端设置里读）。
//
// 这里**不再校验地址形状**：地址已经不是启动期的常量了。校验挪到了保存那一刻
// （见 ValidateWebhookAddress 的调用点）——那时人就在页面前，比等到进程重启
// 才在日志里报错强得多。
func NewWeComSender(resolve func(context.Context) (string, error), client *http.Client) *WeComSender {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &WeComSender{resolve: resolve, client: client}
}

// ValidateWebhookAddress 校验地址形状。**错误里不回显地址本身**——它是凭据，
// 而这个错误会一路回到管理端页面上。
func ValidateWebhookAddress(endpoint string) error {
	raw := strings.TrimSpace(endpoint)
	if raw == "" {
		return errors.New("wecom: webhook address is empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("wecom: webhook address is not a valid URL")
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("wecom: webhook address must be https, got scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return errors.New("wecom: webhook address has no host")
	}
	return nil
}

type weComPayload struct {
	MsgType  string       `json:"msgtype"`
	Markdown weComContent `json:"markdown"`
}

type weComContent struct {
	Content string `json:"content"`
}

type weComResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

// Send 投递一条消息。返回的错误串是要落进发件箱 last_error_code 的**码**，
// 因此保持短、稳定、不含上游原文与地址。
func (w *WeComSender) Send(ctx context.Context, content string) error {
	// 现取地址。取不到（没配、或解密失败）就是"这条发不出去"，按失败记账：
	// 发件箱会重试，而运营在管理端看得到失败原因。
	endpoint, err := w.resolve(ctx)
	if err != nil {
		return errors.New("wecom: webhook address unavailable")
	}
	if err = ValidateWebhookAddress(endpoint); err != nil {
		// 库里的值不成形状（理论上进不来，保存时校验过）——不回显它。
		return errors.New("wecom: stored webhook address is malformed")
	}
	body, err := json.Marshal(weComPayload{MsgType: "markdown", Markdown: weComContent{Content: content}})
	if err != nil {
		return errors.New("wecom: encode failed")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New(scrub(endpoint, "wecom: build request failed"))
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := w.client.Do(request)
	if err != nil {
		// net/http 把请求 URL 放进它自己的错误里，所以这里**不带 err**。
		return errors.New("wecom: request failed")
	}
	defer func() { _ = response.Body.Close() }()

	// **HTTP 200 不等于送达**：企微在响应体里用 errcode 说明结果，只看状态码
	// 会让一条"机器人被移出群"的失败看起来像成功。
	var parsed weComResponse
	if err = json.NewDecoder(response.Body).Decode(&parsed); err != nil {
		return fmt.Errorf("wecom: unreadable response (HTTP %d)", response.StatusCode)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("wecom: HTTP %d", response.StatusCode)
	}
	if parsed.ErrCode != 0 {
		// 只带 errcode，不带 errmsg：后者是上游自由文本，会进发件箱与管理端。
		return fmt.Errorf("wecom: errcode %d", parsed.ErrCode)
	}
	return nil
}

// scrub 把这一次用到的地址与它的查询串从任意文本里抹掉。最后一道防线，
// 不是第一道——正确的做法始终是不把地址放进错误里。
//
// 地址现在是每次投递现取的，所以它是个自由函数、按调用点传入当次的地址，
// 而不是读一个构造期字段。
func scrub(endpoint, text string) string {
	if endpoint == "" {
		return text
	}
	out := strings.ReplaceAll(text, endpoint, "[REDACTED]")
	if parsed, err := url.Parse(endpoint); err == nil && parsed.RawQuery != "" {
		out = strings.ReplaceAll(out, parsed.RawQuery, "[REDACTED]")
	}
	return out
}
