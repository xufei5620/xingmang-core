package sms

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// WeComNotifier 把收到的验证码推到企业微信群机器人。
//
// **刻意不复用 cards 的那个**，尽管两者有十来行相似：那个吃的是
// cards.Notification（带卡号掩码、商户、金额、状态这些卡片专有的字段），
// 而这里要说的是「哪个号收到了什么码」。为了共用而把两个领域的字段并进
// 一个结构体，会让任一方加字段时都要改另一方——共用的其实只有
// 「群机器人收 markdown」这一个事实，那十来行不值得为它抽一层。
//
// 与卡片那条通道**用不同的凭据引用**：接码的码与卡片 3DS 的码可能要发给
// 不同的人。想发同一个群时把同一个地址填两遍即可；合成一条则会让任一方
// 想换群时把另一方也弄坏。
type WeComNotifier struct {
	endpoint func(context.Context) (string, error)
	client   *http.Client
}

func NewWeComNotifier(endpoint func(context.Context) (string, error), client *http.Client) *WeComNotifier {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &WeComNotifier{endpoint: endpoint, client: client}
}

func (w *WeComNotifier) NotifyCode(ctx context.Context, provider, phoneMask, code string) error {
	url, err := w.endpoint(ctx)
	if err != nil {
		// **错误里绝不带 webhook 地址**：它本身就是凭据，一条带着它的
		// 错误日志等于把推送通道公开了。
		return fmt.Errorf("wecom: 取推送地址失败: %w", err)
	}
	if url == "" {
		// 没配 = 运营还没填。不是错误，不推就是了。
		return nil
	}

	text := fmt.Sprintf("**接码验证码**\n>号码：%s\n>验证码：`%s`\n>供应商：%s\n>时间：%s",
		phoneMask, code, provider, time.Now().UTC().Format("2006-01-02 15:04:05 UTC"))

	body, err := json.Marshal(map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]string{"content": text},
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("wecom: 构造请求失败")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("wecom: 请求失败")
	}
	defer func() { _ = resp.Body.Close() }()

	// **HTTP 200 不等于送达**：企业微信在 body 里用 errcode 说明结果，
	// 只看状态码会让一条「机器人被移出群」的失败看起来像成功。
	var out struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("wecom: 响应无法解析（HTTP %d）", resp.StatusCode)
	}
	if out.ErrCode != 0 {
		return fmt.Errorf("wecom: 上游拒绝（errcode %d: %s）", out.ErrCode, out.ErrMsg)
	}
	return nil
}
