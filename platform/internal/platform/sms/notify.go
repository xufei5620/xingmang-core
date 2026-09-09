package sms

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/notify"
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

	// 信封自 XM-NOTIFY-ENVELOPE 起统一（internal/platform/notify）：运营把同一个
	// Webhook 地址填进了告警、卡片、接码三个凭据，同一个群里的每条消息都要能
	// 自己说清是哪个域、多严重、编号是什么、该去哪处理。码有几分钟时效，
	// 因此是 warning 而不是 info；码本身放进标题，位置越靠前越容易被抓到。
	text := notify.RenderWeComMarkdown(notify.Envelope{
		Domain: notify.DomainSMS, Kind: "code", Severity: notify.SeverityWarning,
		Title: "验证码 " + code + "（" + phoneMask + "）",
		Lines: []notify.Line{
			{Label: "号码", Value: phoneMask},
			{Label: "验证码", Value: code},
			{Label: "供应商", Value: provider},
			{Label: "时间", Value: time.Now().UTC().Format("2006-01-02 15:04:05 UTC")},
		},
		Action: "管理后台 → 接码中心",
	})

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
