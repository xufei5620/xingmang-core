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
// （.../webhook/send?key=xxx）。因此地址只经一个 `*_FILE` 环境变量从文件读入
// （与本仓库其它凭据同一手法），绝不进日志、错误串或任何 API 响应。下面每一条
// 返回的错误都刻意不带地址，`scrub` 是最后一道防线——net/http 会把请求 URL
// 塞进它自己造的错误，那个错误不经过我们的手。
type WeComSender struct {
	endpoint string
	client   *http.Client
}

// NewWeComSender 校验地址形状并构造发送器。
//
// 形状校验放在构造期而不是发送期：一个填错的地址应该在进程起来时就说清楚，
// 而不是等到第一条通知该发的时候才发现——那时候没人在看日志。
func NewWeComSender(endpoint string, client *http.Client) (*WeComSender, error) {
	raw := strings.TrimSpace(endpoint)
	if raw == "" {
		return nil, errors.New("wecom: webhook address is empty")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		// 不回显原始地址：它本身就是凭据。
		return nil, errors.New("wecom: webhook address is not a valid URL")
	}
	if parsed.Scheme != "https" {
		return nil, fmt.Errorf("wecom: webhook address must be https, got scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, errors.New("wecom: webhook address has no host")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &WeComSender{endpoint: raw, client: client}, nil
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
	body, err := json.Marshal(weComPayload{MsgType: "markdown", Markdown: weComContent{Content: content}})
	if err != nil {
		return errors.New("wecom: encode failed")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, w.endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New(w.scrub("wecom: build request failed"))
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

// scrub 把地址与它的查询串从任意文本里抹掉。最后一道防线，不是第一道——
// 正确的做法始终是不把地址放进错误里。
func (w *WeComSender) scrub(text string) string {
	out := strings.ReplaceAll(text, w.endpoint, "[REDACTED]")
	if parsed, err := url.Parse(w.endpoint); err == nil && parsed.RawQuery != "" {
		out = strings.ReplaceAll(out, parsed.RawQuery, "[REDACTED]")
	}
	return out
}
