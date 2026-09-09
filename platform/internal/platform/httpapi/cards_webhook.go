package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xufei5620/xingmang-platform/internal/platform/cards"
)

// webhookMaxBody 是回调体的上限。
//
// 这个端点无需鉴权即可到达，没有上限等于把内存交给任何一个知道路径的人。
// 单个卡片事件的信封不到 1KB，64KB 已经宽松得多。
const webhookMaxBody = 64 << 10

// CardWebhookProcessor 是回调处理需要的能力。
//
// 定成接口而不是直接吃 *cards.Syncer + *cards.PgStore：这条路径的每一个
// 分支（签名不过、重复投递、刷新失败）都必须能在没有网络与数据库的情况下
// 被测到，而它们恰恰是最不该出错的部分。
type CardWebhookProcessor interface {
	// WebhookSecret 取某账号的回调密钥。
	WebhookSecret(ctx context.Context, account string) (string, error)
	// RecordWebhookEvent 登记投递并回答是否已处理过。
	RecordWebhookEvent(ctx context.Context, account string, ev cards.WebhookEvent) (cards.WebhookRecord, error)
	// RefreshCard 定向重读一张卡。
	RefreshCard(ctx context.Context, account, cardID string, withTransactions bool) error
	// MarkWebhookEventProcessed 只在处理成功后调用。
	MarkWebhookEventProcessed(ctx context.Context, account, eventID string) error
	// RecordWebhookEventFailure 记下失败分类，供管理端排查。
	RecordWebhookEventFailure(ctx context.Context, account, eventID, reason string) error
	// RecordCardChallenge 落一次 3DS 验证挑战。
	RecordCardChallenge(ctx context.Context, c cards.CardChallenge) error
	// NotifyCardEvent 把事件推给人。**尽力而为**：推送失败绝不能让回调失败。
	NotifyCardEvent(ctx context.Context, n cards.Notification)
}

// CardWebhookHandler 处理 Infini 的卡片回调。
//
// 路径里带账号（/webhooks/infini/{account}）：每个 Infini 账号在它自己的
// 后台配一条回调地址，密钥也各自独立。不带账号就无从知道该用哪把密钥验签，
// 而"挨个密钥试一遍"等于把两个账号的信任边界打通。
//
// **这是全系统唯一一个不需要鉴权就能触发写动作的入口**，所以：
//
//	签名不过 → 401，且绝不做任何事；
//	账号不认识 → 404，不回落；
//	处理失败 → 5xx，让上游按它的重试策略重投（最多 8 次）；
//	重复投递 → 200，不重复处理。
//
// 回 200 的含义是"收到并处理完了"。处理失败时回 200 会让上游不再重试，
// 一次真实的状态变更就此丢失——这是这个端点最危险的一种写法。
func CardWebhookHandler(
	p CardWebhookProcessor,
	accounts []string,
	now func() time.Time,
	logger *slog.Logger,
) http.HandlerFunc {
	if now == nil {
		now = time.Now
	}
	if logger == nil {
		logger = slog.Default()
	}
	verifier := cards.NewWebhookVerifier(now)

	return func(w http.ResponseWriter, r *http.Request) {
		account := chi.URLParam(r, "account")
		if !slices.Contains(accounts, account) {
			// 不回落、也不透露"这个账号存在但密钥不对"。
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		payload, err := io.ReadAll(io.LimitReader(r.Body, webhookMaxBody+1))
		if err != nil || len(payload) > webhookMaxBody {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		secret, err := p.WebhookSecret(r.Context(), account)
		if err != nil {
			// 密钥取不到时**按拒绝处理**，不是按 5xx 让它重投：
			// 重投改变不了"没配密钥"这件事，只会把日志刷满。
			logger.ErrorContext(r.Context(), "card_webhook_rejected",
				"module", "httpapi", "account", account, "reason", "secret_unavailable")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		headers := cards.WebhookHeaders{
			Signature: r.Header.Get("X-Webhook-Signature"),
			Timestamp: r.Header.Get("X-Webhook-Timestamp"),
			EventID:   r.Header.Get("X-Webhook-Event-Id"),
		}
		if err := verifier.Verify(secret, headers, payload); err != nil {
			// 具体是签名不对还是时间戳过期只进服务端日志：回给调用方
			// 等于告诉他猜到了哪一步。
			logger.WarnContext(r.Context(), "card_webhook_rejected",
				"module", "httpapi", "account", account, "err", err.Error())
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		ev, err := cards.ParseWebhookEvent(payload, headers.EventID)
		if err != nil {
			logger.WarnContext(r.Context(), "card_webhook_unparseable",
				"module", "httpapi", "account", account, "err", err.Error())
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		record, err := p.RecordWebhookEvent(r.Context(), account, ev)
		if err != nil {
			logger.ErrorContext(r.Context(), "card_webhook_record_failed",
				"module", "httpapi", "account", account, "event_id", ev.ID, "err", err.Error())
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		if record.AlreadyProcessed {
			// 重投而上次已经成功：直接确认，别再打一次上游。
			w.WriteHeader(http.StatusOK)
			return
		}

		// 挑战事件不刷新卡片（与状态、余额无关），只把它记下来给页面用。
		if ev.Type == cards.WebhookEventCardChallenge && ev.ChallengeID != "" {
			if err := p.RecordCardChallenge(r.Context(), cards.CardChallenge{
				Account: account, CardID: ev.CardID, ID: ev.ChallengeID,
				Type: ev.ChallengeType, Code: ev.ChallengeCode,
				ExpiresAt: ev.ChallengeExpiresAt,
			}); err != nil {
				_ = p.RecordWebhookEventFailure(r.Context(), account, ev.ID, "challenge_store_failed")
				logger.ErrorContext(r.Context(), "card_challenge_store_failed",
					"module", "httpapi", "account", account, "event_id", ev.ID, "err", err.Error())
				http.Error(w, "internal", http.StatusInternalServerError)
				return
			}
		}

		if ev.NeedsCardRefresh() {
			// 交易事件才顺带拉流水；状态变更没必要每次翻一遍流水。
			withTx := ev.Type == cards.WebhookEventCardTransaction
			if err := p.RefreshCard(r.Context(), account, ev.CardID, withTx); err != nil {
				_ = p.RecordWebhookEventFailure(r.Context(), account, ev.ID, "refresh_failed")
				logger.ErrorContext(r.Context(), "card_webhook_refresh_failed",
					"module", "httpapi", "account", account, "event_id", ev.ID,
					"card_id", ev.CardID, "err", err.Error())
				// 回 5xx 让上游重投。绝不在这里标记完成。
				http.Error(w, "internal", http.StatusInternalServerError)
				return
			}
		}

		if err := p.MarkWebhookEventProcessed(r.Context(), account, ev.ID); err != nil {
			// 刷新已经做了，只是没记上。回 5xx 会让上游重投，而重投会
			// 再刷新一次——刷新是幂等的（重读上游、覆盖投影），所以这样安全。
			logger.ErrorContext(r.Context(), "card_webhook_mark_failed",
				"module", "httpapi", "account", account, "event_id", ev.ID, "err", err.Error())
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}

		logger.InfoContext(r.Context(), "card_webhook_processed",
			"module", "httpapi", "account", account, "event_id", ev.ID,
			"event_type", ev.Type, "card_id", ev.CardID)

		// 推送放在**标记完成之后**，且失败不改变响应。
		//
		// 顺序是刻意的：推送失败若回 5xx，上游会重投同一个事件，而它已经
		// 处理过了——重投只会被去重挡掉，推送依然不会补发，白白让上游重试
		// 八次。丢一条推送是可接受的损失，丢一次状态刷新不是。
		p.NotifyCardEvent(r.Context(), notificationFor(account, ev))

		w.WriteHeader(http.StatusOK)
	}
}

// notificationFor 把回调事件翻成一条给人看的通知。
//
// 卡号只带回调里给的 last_four：完整卡号绝不进推送（群机器人的消息留在
// 聊天记录里，那不是我们能控制的存储）。
func notificationFor(account string, ev cards.WebhookEvent) cards.Notification {
	n := cards.Notification{Account: account, CardMask: ev.CardLastFour}
	switch ev.Type {
	case cards.WebhookEventCardChallenge:
		n.Kind = cards.NotifyChallenge
		n.ChallengeCode = ev.ChallengeCode
		n.ExpiresAt = ev.ChallengeExpiresAt
	case cards.WebhookEventCardTransaction:
		n.Kind = cards.NotifyTransaction
		n.Merchant = ev.Merchant
		n.Amount = ev.Amount
		n.Currency = ev.Currency
		n.TransactionType = ev.TransactionType
		n.TransactionStatus = ev.TransactionStatus
		n.FailureReason = ev.FailureReason
	default:
		n.Kind = cards.NotifyStatusChange
		n.Status = ev.CardStatus
	}
	return n
}
