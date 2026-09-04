package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/cards"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// CardQuerier 是卡片只读端点需要的仓储能力。
//
// 写路径（开卡/充值/冻结/解冻/赎回/查看卡面）**只走 cards.card.* Action**，
// 这里不开第二条写路径。
type CardQuerier interface {
	ListCards(ctx context.Context, account, ownerRef string) ([]cards.CardView, error)
	ListTransactions(ctx context.Context, account, cardID string, limit int) ([]cards.TransactionView, error)
	OperationsNeedingAttention(ctx context.Context) ([]cards.Operation, error)
}

type cardItem struct {
	// Account 是内部运营维度。管理端按它筛选与展示；
	// 以后开放外部用户时，那一侧的响应不该带这个字段。
	Account string `json:"account"`
	CardID  string `json:"card_id"`
	// Mask 是掩码卡号，库里存的就是它。完整卡号只能经
	// cards.card.reveal Action 取得，不在任何读路径上。
	Mask         string              `json:"mask"`
	HolderName   string              `json:"holder_name"`
	Alias        string              `json:"card_alias"`
	Status       string              `json:"status"`
	Currency     string              `json:"currency"`
	BalanceMinor int64               `json:"balance_minor"`
	OwnerRef     string              `json:"owner_ref,omitempty"`
	Freshness    cards.FreshnessInfo `json:"freshness"`
}

type cardTransactionItem struct {
	Account     string `json:"account"`
	CardID      string `json:"card_id"`
	Type        string `json:"type"`
	AmountMinor int64  `json:"amount_minor"`
	FeeMinor    int64  `json:"fee_minor"`
	Currency    string `json:"currency"`
	Status      string `json:"status"`
	Merchant    string `json:"merchant"`
	OccurredAt  string `json:"occurred_at,omitempty"`
}

type cardOperationItem struct {
	IdempotencyKey string `json:"idempotency_key"`
	Account        string `json:"account"`
	Kind           string `json:"kind"`
	State          string `json:"state"`
	CardID         string `json:"card_id,omitempty"`
	Alias          string `json:"card_alias,omitempty"`
	AmountText     string `json:"amount,omitempty"`
	TokenType      string `json:"token_type,omitempty"`
	Reason         string `json:"reason,omitempty"`
	StartedAt      string `json:"started_at"`
	// RetryAllowed 明确告诉前端这笔操作能不能重试。
	// 判定放服务端：让前端按状态自己推，迟早会推出一个「看起来该能重试」
	// 的不确定态，而那正是可能重复扣钱的入口。
	RetryAllowed bool `json:"retry_allowed"`
}

// ListCardsHandler 列出卡片投影。
//
// accounts 是已配置的账号清单，随响应一起返回：管理端的开卡表单要拿它
// 填下拉。从卡片数据里反推账号是不行的——一个还没开过卡的环境会得到
// 一个没有选项的表单。
func ListCardsHandler(store CardQuerier, accounts []string, syncInterval time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := principal.FromContext(r.Context()); !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}

		// account 与 owner_ref 都由调用方显式给出（内部运营看全部）。
		//
		// 两者不是一回事：account 是内部资金来源，owner_ref 是面向客户的
		// 归属。以后开放给外部用户时，owner_ref 必须改成由身份推导而不是
		// 由参数决定（那是权限边界，不能让请求方自己挑），而 account
		// 干脆不出现在那一侧——账号是内部维度。
		account := strings.TrimSpace(r.URL.Query().Get("account"))
		ownerRef := strings.TrimSpace(r.URL.Query().Get("owner_ref"))

		items, err := store.ListCards(r.Context(), account, ownerRef)
		if err != nil {
			WriteError(w, r, err)
			return
		}

		now := time.Now().UTC()
		out := make([]cardItem, 0, len(items))
		for _, c := range items {
			out = append(out, cardItem{
				Account:      c.Account,
				CardID:       c.CardID,
				Mask:         c.Mask,
				HolderName:   c.HolderName,
				Alias:        c.Alias,
				Status:       c.Status,
				Currency:     c.Currency,
				BalanceMinor: c.BalanceMinor,
				OwnerRef:     c.OwnerRef,
				Freshness:    cards.Freshness(c.LastSyncedAt, now, syncInterval),
			})
		}
		if accounts == nil {
			accounts = []string{}
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out, "accounts": accounts})
	}
}

// ListCardTransactionsHandler 列出一张卡的流水。
func ListCardTransactionsHandler(store CardQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := principal.FromContext(r.Context()); !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}

		cardID := strings.TrimSpace(chi.URLParam(r, "cardID"))
		if cardID == "" {
			WriteError(w, r, action.NewError(action.CodeInvalidParams, "缺少卡片 id", nil))
			return
		}
		// 账号必填：卡 id 只在自己账号内有意义，不带账号查等于让存储层猜。
		account := strings.TrimSpace(r.URL.Query().Get("account"))
		if account == "" {
			WriteError(w, r, action.NewError(action.CodeInvalidParams, "缺少账号", nil))
			return
		}

		limit := 100
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				// 拼错的 limit 当场 400，不静默回落到默认值：
				// 静默回落会让调用方拿到一份看起来对、其实没按他要求截断的列表。
				WriteError(w, r, action.NewError(action.CodeInvalidParams, "limit 非法", nil))
				return
			}
			limit = parsed
		}

		items, err := store.ListTransactions(r.Context(), account, cardID, limit)
		if err != nil {
			WriteError(w, r, err)
			return
		}

		out := make([]cardTransactionItem, 0, len(items))
		for _, t := range items {
			item := cardTransactionItem{
				Account: account,
				CardID:  t.CardID, Type: t.Type, AmountMinor: t.AmountMinor,
				FeeMinor: t.FeeMinor, Currency: t.Currency, Status: t.Status,
				Merchant: t.Merchant,
			}
			if !t.OccurredAt.IsZero() {
				item.OccurredAt = t.OccurredAt.UTC().Format(time.RFC3339)
			}
			out = append(out, item)
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// ListCardOperationsNeedingAttentionHandler 列出需要人工处置的操作。
//
// 这是管理端红条的数据源：每一条都意味着「有一笔花钱操作，我们至今
// 不知道它到底成没成」。
func ListCardOperationsNeedingAttentionHandler(store CardQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := principal.FromContext(r.Context()); !ok {
			WriteError(w, r, action.NewError(action.CodePermissionDenied, "缺少身份", nil))
			return
		}

		ops, err := store.OperationsNeedingAttention(r.Context())
		if err != nil {
			WriteError(w, r, err)
			return
		}

		out := make([]cardOperationItem, 0, len(ops))
		for _, op := range ops {
			out = append(out, cardOperationItem{
				IdempotencyKey: op.IdempotencyKey,
				Account:        op.Account,
				Kind:           op.Kind,
				State:          string(op.State),
				CardID:         op.CardID,
				Alias:          op.Alias,
				AmountText:     op.AmountText,
				TokenType:      op.TokenType,
				Reason:         op.Reason,
				StartedAt:      op.StartedAt.UTC().Format(time.RFC3339),
				RetryAllowed:   op.RetryAllowed(),
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}
