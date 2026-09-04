package cards

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// webhookSkewTolerance 是回调时间戳的容忍窗口。
//
// 300 秒来自官方参考实现（infini-skill/references/WEBHOOKS.md 的
// `abs(now - timestamp) > 300`），与请求签名那一侧的 Date 容忍窗口同值。
const webhookSkewTolerance = 5 * time.Minute

// ErrWebhookRejected：回调没通过校验。
//
// 只有一个错误值，不细分「签名不对」「时间戳过期」「密钥没配」：
// 这个端点对公网开放，把失败原因回给调用方等于告诉攻击者他猜到了哪一步。
// 细分原因只进服务端日志。
var ErrWebhookRejected = errors.New("cards: webhook 校验未通过")

// WebhookHeaders 是回调携带的三个签名相关头。
type WebhookHeaders struct {
	Signature string // X-Webhook-Signature
	Timestamp string // X-Webhook-Timestamp（Unix 秒）
	EventID   string // X-Webhook-Event-Id
}

// WebhookVerifier 校验 Infini 回调的签名与时效。
type WebhookVerifier struct {
	now func() time.Time
}

// NewWebhookVerifier 构造校验器。
func NewWebhookVerifier(now func() time.Time) *WebhookVerifier {
	if now == nil {
		now = time.Now
	}
	return &WebhookVerifier{now: now}
}

// Verify 校验一次回调。
//
// 签名内容是 `{timestamp}.{event_id}.{payload}`，payload 必须是**原始请求体
// 字节**——先反序列化再重新序列化会改变字节序，签名必然对不上。
//
// 校验顺序是刻意的：先看密钥与头是否齐全（本地判断），再比时间戳（本地判断），
// 最后才算 HMAC。反过来会让每一个畸形请求都付出一次 HMAC 计算。
func (v *WebhookVerifier) Verify(secret string, h WebhookHeaders, payload []byte) error {
	if strings.TrimSpace(secret) == "" {
		// 没配密钥 = 这个端点不可用。空串是一个所有人都知道的「密钥」，
		// 拿它验签等于不验。
		return fmt.Errorf("%w: 未配置 webhook 密钥", ErrWebhookRejected)
	}
	if h.Signature == "" || h.Timestamp == "" || h.EventID == "" {
		return fmt.Errorf("%w: 签名头不齐全", ErrWebhookRejected)
	}

	seconds, err := strconv.ParseInt(strings.TrimSpace(h.Timestamp), 10, 64)
	if err != nil {
		return fmt.Errorf("%w: 时间戳不是 Unix 秒", ErrWebhookRejected)
	}
	skew := v.now().Sub(time.Unix(seconds, 0))
	if skew < 0 {
		skew = -skew
	}
	if skew > webhookSkewTolerance {
		// 双向都卡：过老的是重放，来自未来的说明有一侧时钟错了，
		// 而时钟错了的那一侧算出来的签名也不可信。
		return fmt.Errorf("%w: 时间戳超出容忍窗口", ErrWebhookRejected)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(h.Timestamp))
	mac.Write([]byte("."))
	mac.Write([]byte(h.EventID))
	mac.Write([]byte("."))
	mac.Write(payload)
	want := mac.Sum(nil)

	// 十六进制。官方参考实现（infini-skill/references/WEBHOOKS.md）写死
	// hex(HMAC-SHA256(secret, content))，Python 侧是 .hexdigest()。
	// 网页文档没写编码方式，一度只能两种都收；确认之后收窄成一种——
	// 多认一种编码就是多一条我们没有依据的信任路径。
	got, err := hex.DecodeString(h.Signature)
	if err != nil {
		return fmt.Errorf("%w: 签名不是十六进制", ErrWebhookRejected)
	}
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return fmt.Errorf("%w: 签名不匹配", ErrWebhookRejected)
	}
	return nil
}

// formatUnix 把时刻格式化成 Unix 秒文本，供测试与日志使用。
func formatUnix(t time.Time) string {
	return strconv.FormatInt(t.Unix(), 10)
}

// 卡片相关的回调事件类型（Infini webhook 文档）。
//
// 只列我们真的会处理的两个。order.* 与 subscription.* 属于收单业务，
// 与卡无关；card.challenge 是 3DS 验证挑战，需要人实时介入，
// 不是投影能表达的东西，本片不处理。
const (
	WebhookEventCardStatusChange = "card.status_change"
	WebhookEventCardTransaction  = "card.transaction"
)

// WebhookEvent 是回调信封里我们**真正使用**的那几项。
//
// 刻意不解析 data 里的金额、类型、商户等字段：回调只当触发器，
// 具体数据一律以主动读取上游为准。理由见 NeedsCardRefresh 的注释。
type WebhookEvent struct {
	ID   string
	Type string
	// CardID 只在卡片类事件里有意义。
	CardID     string
	OccurredAt time.Time
}

// NeedsCardRefresh 说明这个事件是否要求我们去重新读一次这张卡。
//
// **回调不写数据，只触发读取。** 签名只证明「这条消息来自 Infini」，
// 不证明我们对字段形状的理解是对的——卡 API 文档里交易类型写作 "Consume"、
// 状态写作 "Completed"，webhook 文档里却是 "consume" / "authorized"，
// 同一个概念两套大小写。把金额按字面写进账，一处理解错就是账目错，
// 而且错得安静。让回调只回答「哪张卡该重读了」，把「读到什么」交给
// 已经在跑的同步路径，全系统就只有一条写投影的路。
func (e WebhookEvent) NeedsCardRefresh() bool {
	switch e.Type {
	case WebhookEventCardStatusChange, WebhookEventCardTransaction:
		return e.CardID != ""
	default:
		return false
	}
}

// ParseWebhookEvent 解析回调信封。
//
// eventID 来自 **X-Webhook-Event-Id 请求头**，不是载荷。文档明写它是幂等
// 依据；更要紧的是订单与订阅事件的信封是扁平的、根本没有 id 字段，
// 只有卡片事件用带 id 的版本化信封。要求载荷里必须有 id，会让订阅了全部
// 事件的端点在每个订单事件上回 400，然后被上游重试 8 次——噪音很大，
// 而且会掩盖真正的失败。
func ParseWebhookEvent(payload []byte, eventID string) (WebhookEvent, error) {
	if strings.TrimSpace(eventID) == "" {
		return WebhookEvent{}, fmt.Errorf("%w: 缺少事件 id 请求头", ErrWebhookRejected)
	}
	var raw struct {
		Event      string `json:"event"`
		OccurredAt int64  `json:"occurred_at"`
		Data       struct {
			Card struct {
				CardID string `json:"card_id"`
			} `json:"card"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return WebhookEvent{}, fmt.Errorf("%w: 载荷不是 JSON", ErrWebhookRejected)
	}
	if strings.TrimSpace(raw.Event) == "" {
		return WebhookEvent{}, fmt.Errorf("%w: 缺少事件类型", ErrWebhookRejected)
	}

	ev := WebhookEvent{ID: eventID, Type: raw.Event, CardID: raw.Data.Card.CardID}
	if raw.OccurredAt > 0 {
		ev.OccurredAt = time.Unix(raw.OccurredAt, 0).UTC()
	}

	switch ev.Type {
	case WebhookEventCardStatusChange, WebhookEventCardTransaction:
		if ev.CardID == "" {
			// 卡片事件没有卡 id 就无从处理。静默放行会让一次真实的状态
			// 变更被悄悄丢掉，而我们还回了 200 让上游不再重试。
			return WebhookEvent{}, fmt.Errorf("%w: 卡片事件缺少 card_id", ErrWebhookRejected)
		}
	}
	return ev, nil
}
