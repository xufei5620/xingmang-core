package cards

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// 通知类型。
const (
	NotifyChallenge    = "challenge"
	NotifyTransaction  = "transaction"
	NotifyStatusChange = "status_change"
)

// Notification 是一次要推给人的卡片事件。
//
// **刻意不复用 alerts.Alert**：那个模型带去重、持续时长、确认与解除——
// 它描述的是「一个持续存在的问题」。卡片事件是一次性的：一笔消费、一个
// 验证码、一次状态变更，发生了就是发生了，没有"仍然满足条件"这回事。
// 套进告警模型会得到一条永远不解除的告警，把告警列表变成流水账。
//
// 卡号只带掩码。**完整卡号绝不进推送**——群机器人的消息留在聊天记录里，
// 那不是我们能控制的存储。
type Notification struct {
	Kind     string
	Account  string
	CardMask string

	// 验证码（NotifyChallenge）。Code 可能为空——上游不一定给。
	ChallengeCode string
	ExpiresAt     time.Time

	// 交易（NotifyTransaction）。
	Merchant          string
	Amount            string
	Currency          string
	TransactionType   string
	TransactionStatus string
	FailureReason     string

	// 状态（NotifyStatusChange）。
	Status string
}

// Notifier 投递一条卡片通知。
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// 交易类型与状态的中文，与前端 cardStatus.ts 同一份口径。
//
// 查表前一律小写：上游在 REST 与 webhook 两处的大小写不同
// （Consume vs consume），按字面匹配必有一条路悄悄匹配不上。
var (
	notifyTxTypes = map[string]string{
		"consume": "消费", "reversal": "冲正", "refund": "退款",
		"topup": "充值", "top_up": "充值", "redeem": "赎回",
	}
	notifyTxStatuses = map[string]string{
		"authorized": "授权中", "completed": "已完成", "failed": "失败", "pending": "处理中",
	}
	notifyCardStatuses = map[string]string{
		"init": "初始化", "pending": "处理中", "active": "已激活",
		"suspend": "已锁定", "pending_delete": "删除中", "deleted": "已删除",
	}
)

func lookup(table map[string]string, value string) string {
	if value == "" {
		return ""
	}
	if label, ok := table[strings.ToLower(value)]; ok {
		return label
	}
	return value // 没见过的取值原样显示，与页面同一条纪律
}

// FormatNotification 渲染成给人看的 markdown。
//
// now 显式传入而不是取 time.Now()：剩余有效期要能在测试里断言。
func FormatNotification(n Notification, now time.Time) string {
	var b strings.Builder

	head := n.Account
	if n.CardMask != "" {
		head += " · " + n.CardMask
	}

	switch n.Kind {
	case NotifyChallenge:
		if n.ChallengeCode != "" {
			fmt.Fprintf(&b, "**卡片验证码** %s\n验证码：**%s**\n", head, n.ChallengeCode)
		} else {
			// 上游没给码（生产实测就有这种）：说清去哪儿看，
			// 而不是推一条空的「验证码：」。
			fmt.Fprintf(&b, "**卡片待验证** %s\n上游未给出验证码，请到管理端或 Infini 后台查看\n", head)
		}
		if !n.ExpiresAt.IsZero() {
			// 剩余有效期而不是过期时刻：人看到消息时往往已经过了一分钟，
			// 而一条过期的码和一条有效的长得一模一样。
			left := n.ExpiresAt.Sub(now)
			if left < 0 {
				b.WriteString("**已过期**\n")
			} else {
				fmt.Fprintf(&b, "剩余约 %d 分钟\n", int(left.Minutes())+1)
			}
		}

	case NotifyTransaction:
		status := lookup(notifyTxStatuses, n.TransactionStatus)
		fmt.Fprintf(&b, "**卡片%s** %s\n", lookup(notifyTxTypes, n.TransactionType), head)
		if n.Merchant != "" {
			fmt.Fprintf(&b, "商户：%s\n", n.Merchant)
		}
		if n.Amount != "" {
			fmt.Fprintf(&b, "金额：%s %s\n", n.Amount, n.Currency)
		}
		if status != "" {
			fmt.Fprintf(&b, "状态：%s\n", status)
		}
		if n.FailureReason != "" {
			// 上游原文直接转述：这里的读者是运营本人，而「余额不足」
			// 这类原因正是他要拿去处理的东西。
			fmt.Fprintf(&b, "原因：%s\n", n.FailureReason)
		}

	case NotifyStatusChange:
		fmt.Fprintf(&b, "**卡片状态变更** %s\n当前状态：%s\n",
			head, lookup(notifyCardStatuses, n.Status))

	default:
		fmt.Fprintf(&b, "**卡片事件** %s\n", head)
	}

	return strings.TrimRight(b.String(), "\n")
}

// WeComNotifier 经企业微信群机器人 Webhook 推送。
//
// 与 alerts 那个同名类型是两回事，理由见 Notification 的注释。共用的只有
// 「群机器人收 markdown」这一个事实，重复的那十来行不值得为它抽一层。
type WeComNotifier struct {
	// endpoint 每次调用时解析 webhook 地址——它本身就是凭据（含 key），
	// 运营在管理端轮换后下一条推送就该用新值，而不是等进程重启。
	endpoint func(context.Context) (string, error)
	client   *http.Client
}

// NewWeComNotifier 构造推送器。endpoint 由装配处注入（走 SecretProvider）。
func NewWeComNotifier(endpoint func(context.Context) (string, error), client *http.Client) *WeComNotifier {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &WeComNotifier{endpoint: endpoint, client: client}
}

func (w *WeComNotifier) Notify(ctx context.Context, n Notification) error {
	url, err := w.endpoint(ctx)
	if err != nil {
		// 不带 webhook 地址：它本身是凭据。
		return fmt.Errorf("wecom: 取推送地址失败: %w", err)
	}

	body, err := json.Marshal(map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]string{"content": FormatNotification(n, time.Now())},
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
		// 错误链里会带 URL，而 URL 含 key——这里只留分类。
		return fmt.Errorf("wecom: 投递失败")
	}
	defer resp.Body.Close()

	var out struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("wecom: 响应无法解析（http %d）", resp.StatusCode)
	}
	if out.ErrCode != 0 {
		// **HTTP 200 不代表送达**：企微把错误放在响应体里。
		return fmt.Errorf("wecom: 上游拒绝（errcode %d: %s）", out.ErrCode, out.ErrMsg)
	}
	return nil
}
