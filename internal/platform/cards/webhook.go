package cards

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
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

	content := signedContent(h, payload)
	want := webhookMAC([]byte(secret), content)

	// 十六进制。官方参考实现（infini-skill/references/WEBHOOKS.md）写死
	// hex(HMAC-SHA256(secret, content))，Python 侧是 .hexdigest()。
	// 网页文档没写编码方式，一度只能两种都收；确认之后收窄成一种——
	// 多认一种编码就是多一条我们没有依据的信任路径。
	got, err := hex.DecodeString(h.Signature)
	if err != nil {
		return fmt.Errorf("%w: 签名不是十六进制（收到 %d 字符）",
			ErrWebhookRejected, len(h.Signature))
	}
	if subtle.ConstantTimeCompare(got, want) != 1 {
		// 不匹配时把两个 MAC 的开头并排放进错误链。**它们不是密钥**：
		// HMAC 值泄漏不会反推出密钥，而没有这一行，"签名不匹配"这五个字
		// 无法区分「密钥填错了」与「签名内容拼法不同」——这两者的排查方向
		// 完全相反。同时给出签名内容的形状，好核对 timestamp/event_id
		// 有没有带上多余空白。
		return fmt.Errorf("%w: 签名不匹配（收到 %s… 本地算出 %s…，"+
			"ts=%q event=%q 载荷 %d 字节，%s）",
			ErrWebhookRejected,
			shortHex(h.Signature), shortHex(hex.EncodeToString(want)),
			h.Timestamp, h.EventID, len(payload),
			diagnoseSecretForm(secret, content, got))
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
	// WebhookEventCardChallenge 是在线支付的 3DS 验证挑战。
	//
	// 它**不触发卡片刷新**：与卡的状态、余额都无关，重读一次上游只是白打。
	// 它的价值在于把验证码（如果上游给）摆到管理端，省得持卡人去翻邮件。
	WebhookEventCardChallenge = "card.challenge"
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
	// 以下只在 card.transaction 事件里有值。
	//
	// **金额刻意不解析**：回调只当触发器，金额一律以主动读取上游为准
	// （理由见 NeedsCardRefresh）。但交易身份要留下来——REST 的流水接口
	// 不给交易 id，我们的去重键是派生的；回调给了真 id，存下来才能把
	// 两边对上，也才能认出「同一笔交易从 authorized 变成 completed」。
	TransactionID        string
	RelatedTransactionID string
	// TransactionType / TransactionStatus 保持上游原文，不在这里归一化：
	// 大小写在 REST 与 webhook 之间不一致（Consume vs consume），
	// 归一化放在显示层做，存储层留原文才能回答「上游当时到底说了什么」。
	TransactionType   string
	TransactionStatus string
	// 以下只在 card.challenge 事件里有值。
	//
	// ChallengeCode 是 3DS 验证码，**可能为空**：官方文档的示例带
	// "challenge":"123456"，但 2026-09-05 生产收到的真实事件里没有这个字段。
	// 按文档假定它一定存在，会做出一个永远显示空白的验证码栏位。
	ChallengeID        string
	ChallengeType      string
	ChallengeCode      string
	ChallengeExpiresAt time.Time
	// 以下**只用于推送正文**，不进任何账目。
	//
	// 金额仍然不用于写库（理由见 NeedsCardRefresh）——但推送给人的那条
	// 消息里必须有金额与商户，否则"你的卡刚被刷了"这句话没法判断是不是自己。
	// 读一遍上游再推会让通知慢好几秒，而通知的价值恰恰在于快。
	CardLastFour  string
	CardStatus    string
	Merchant      string
	Amount        string
	Currency      string
	FailureReason string
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
				CardID   string `json:"card_id"`
				LastFour string `json:"last_four"`
				Status   string `json:"status"`
			} `json:"card"`
			Amount   string `json:"amount"`
			Currency string `json:"currency"`
			Merchant struct {
				Name string `json:"name"`
			} `json:"merchant"`
			Failure struct {
				Reason string `json:"reason"`
			} `json:"failure"`
			TransactionID        string `json:"transaction_id"`
			RelatedTransactionID string `json:"related_transaction_id"`
			Type                 string `json:"type"`
			Status               string `json:"status"`
			ChallengeID          string `json:"challenge_id"`
			ChallengeType        string `json:"challenge_type"`
			Challenge            string `json:"challenge"`
			ExpiresAt            int64  `json:"expires_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return WebhookEvent{}, fmt.Errorf("%w: 载荷不是 JSON", ErrWebhookRejected)
	}
	if strings.TrimSpace(raw.Event) == "" {
		return WebhookEvent{}, fmt.Errorf("%w: 缺少事件类型", ErrWebhookRejected)
	}

	ev := WebhookEvent{
		ID: eventID, Type: raw.Event, CardID: raw.Data.Card.CardID,
		TransactionID:        raw.Data.TransactionID,
		RelatedTransactionID: raw.Data.RelatedTransactionID,
		TransactionType:      raw.Data.Type,
		TransactionStatus:    raw.Data.Status,
		ChallengeID:          raw.Data.ChallengeID,
		ChallengeType:        raw.Data.ChallengeType,
		ChallengeCode:        raw.Data.Challenge,
		CardLastFour:         raw.Data.Card.LastFour,
		CardStatus:           raw.Data.Card.Status,
		Merchant:             raw.Data.Merchant.Name,
		Amount:               raw.Data.Amount,
		Currency:             raw.Data.Currency,
		FailureReason:        raw.Data.Failure.Reason,
	}
	if raw.Data.ExpiresAt > 0 {
		ev.ChallengeExpiresAt = time.Unix(raw.Data.ExpiresAt, 0).UTC()
	}
	if raw.OccurredAt > 0 {
		ev.OccurredAt = time.Unix(raw.OccurredAt, 0).UTC()
	}

	switch ev.Type {
	case WebhookEventCardStatusChange, WebhookEventCardTransaction, WebhookEventCardChallenge:
		if ev.CardID == "" {
			// 卡片事件没有卡 id 就无从处理。静默放行会让一次真实的状态
			// 变更被悄悄丢掉，而我们还回了 200 让上游不再重试。
			return WebhookEvent{}, fmt.Errorf("%w: 卡片事件缺少 card_id", ErrWebhookRejected)
		}
	}
	return ev, nil
}

// shortHex 截取十六进制串的开头，用于把两个 MAC 并排放进诊断信息。
//
// 只取前 12 个字符：足以判断「完全不同」还是「只差编码」，
// 又不至于把一个完整的有效签名写进日志。
func shortHex(v string) string {
	if len(v) <= 12 {
		return v
	}
	return v[:12]
}

// signedContent 拼出被签名的内容：{timestamp}.{event_id}.{payload}。
//
// 用**请求头里的原始值**而不是解析后的：上游签的是它自己发出去的那几个
// 字节，任何规范化（去空白、重新格式化时间戳）都会让本地算出的 MAC
// 与之不同，而症状与"密钥不对"一模一样。
func signedContent(h WebhookHeaders, payload []byte) []byte {
	out := make([]byte, 0, len(h.Timestamp)+len(h.EventID)+len(payload)+2)
	out = append(out, h.Timestamp...)
	out = append(out, '.')
	out = append(out, h.EventID...)
	out = append(out, '.')
	out = append(out, payload...)
	return out
}

func webhookMAC(key, content []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(content)
	return mac.Sum(nil)
}

// diagnoseSecretForm 在验签失败时逐一试出「哪种密钥解释方式能对上」。
//
// 起因：Infini 的密钥是 44 个字符，正好是 32 字节的 base64——但用的是
// **base64url 字母表**（真实密钥里出现过 `_`），标准 base64 解码器会拒绝它。
// 第一版诊断只试了标准 base64，于是把一个「可能能对上」的情况误报成
// 「密钥不是 base64」。
//
// 现在把现实可能一次覆盖：原文、标准 base64、base64url，各自带填充与不带。
// 服务端到底用哪种没有文档，示例代码用原文（Python 的 SECRET.encode），
// 但示例未必等于实现。
//
// **只诊断，不放行**：多认一种密钥解释方式，就是多一条没有依据的信任路径。
// 真相清楚之后改成唯一的那一种。
func diagnoseSecretForm(secret string, content, received []byte) string {
	trimmed := strings.TrimSpace(secret)

	decoders := []struct {
		name   string
		decode func(string) ([]byte, error)
	}{
		{"标准 base64", base64.StdEncoding.DecodeString},
		{"标准 base64（无填充）", base64.RawStdEncoding.DecodeString},
		{"base64url", base64.URLEncoding.DecodeString},
		{"base64url（无填充）", base64.RawURLEncoding.DecodeString},
	}

	var tried []string
	for _, d := range decoders {
		key, err := d.decode(trimmed)
		if err != nil {
			continue
		}
		tried = append(tried, d.name)
		if subtle.ConstantTimeCompare(webhookMAC(key, content), received) == 1 {
			return "注意：按「" + d.name + "」解码后的密钥算能对上——" +
				"是代码的密钥解释方式错了，不是密钥填错了"
		}
	}

	if len(tried) == 0 {
		return "密钥无法按任何 base64 变体解码，只可能按原文用；原文也对不上，说明密钥值本身不对"
	}
	return "原文与 " + strings.Join(tried, "、") + " 解码后都对不上——大概率是密钥值本身不对"
}
