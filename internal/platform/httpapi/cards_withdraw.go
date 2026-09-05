package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/cards"
)

// WithdrawQuerier 是提现页需要的读能力。
//
// 与 CardQuerier 分开定义，不是为了整洁：这两组端点的权限不同
// （card.read / fund.withdraw），合成一个接口会让「谁能读什么」
// 变成一件要读路由才知道的事。
type WithdrawQuerier interface {
	ListWithdrawAddresses(ctx context.Context) ([]cards.WithdrawAddress, error)
	// RecentWithdrawals 返回最近的提现记录，新的在前。
	RecentWithdrawals(ctx context.Context, limit int) ([]cards.WithdrawRecord, error)
	// ListWithdrawLimits 返回已设置的提现额度（没设过的账号不在结果里）。
	ListWithdrawLimits(ctx context.Context) ([]cards.WithdrawLimitRow, error)
}

type withdrawAddressItem struct {
	ID      string `json:"address_id"`
	Account string `json:"account"`
	Chain   string `json:"chain"`
	// Address 是**完整地址**。
	//
	// 与审计摘要那边刻意相反（那里只有链与标签）：审计摘要会被广泛展示，
	// 而这个端点由 fund.withdraw 把守，能提现的人本来就必须核对地址。
	// 让他去别处查，等于逼他在页面之外做核对——那才是出错的地方。
	Address string `json:"address"`
	Label   string `json:"label"`
	// Enabled 为假表示这条地址已下线：仍然列出来（可查、可重新启用），
	// 但提现表单不该提供它。
	Enabled bool `json:"enabled"`
}

type withdrawItem struct {
	RequestID string `json:"request_id"`
	Account   string `json:"account"`
	Chain     string `json:"chain"`
	TokenType string `json:"token_type"`
	Amount    string `json:"amount"`
	AddressID string `json:"address_id"`
	Address   string `json:"address"`
	Status    string `json:"status"`
	// TxHash 让人拿去区块浏览器自己核对——「平台说转成功了」和
	// 「链上确实有这笔」是两件事，而后者才是钱到没到的依据。
	TxHash         string `json:"tx_hash,omitempty"`
	ActualAmount   string `json:"actual_amount,omitempty"`
	GasFee         string `json:"gas_fee,omitempty"`
	GasFeeCurrency string `json:"gas_fee_currency,omitempty"`
	Note           string `json:"note,omitempty"`
	StartedAt      string `json:"started_at,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

// ListWithdrawAddressesHandler 返回已登记的可提现地址。
func ListWithdrawAddressesHandler(store WithdrawQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := store.ListWithdrawAddresses(r.Context())
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]withdrawAddressItem, 0, len(rows))
		for _, a := range rows {
			out = append(out, withdrawAddressItem{
				ID: a.ID, Account: a.Account, Chain: a.Chain,
				Address: a.Address, Label: a.Label, Enabled: a.Enabled,
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// withdrawalsPageSize 是提现历史一次回多少条。
//
// 提现是低频动作（一天几笔），所以不做分页：给一个够看的上限比给一套
// 谁也不会翻到第二页的分页控件诚实。
const withdrawalsPageSize = 100

// ListWithdrawalsHandler 返回最近的提现记录。
func ListWithdrawalsHandler(store WithdrawQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := store.RecentWithdrawals(r.Context(), withdrawalsPageSize)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]withdrawItem, 0, len(rows))
		for _, x := range rows {
			out = append(out, withdrawItem{
				RequestID: x.RequestID, Account: x.Account, Chain: x.Chain,
				TokenType: x.TokenType, Amount: x.Amount,
				AddressID: x.AddressID, Address: x.Address, Status: x.Status,
				TxHash: x.TxHash, ActualAmount: x.ActualAmount,
				GasFee: x.GasFee, GasFeeCurrency: x.GasFeeCurrency, Note: x.Note,
				StartedAt: formatUTC(x.StartedAt), UpdatedAt: formatUTC(x.UpdatedAt),
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}

// formatUTC 把零值时间翻成空串而不是 0001-01-01——
// 页面上一个 0001 年的日期只会让人以为数据坏了。
func formatUTC(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

type withdrawLimitItem struct {
	Account      string `json:"account"`
	PerOperation string `json:"per_operation"`
	PerDay       string `json:"per_day"`
	// UpdatedBy / UpdatedAt 让「这个上限是谁定的、什么时候定的」在额度旁边
	// 就看得到。审计里也有，但为一个数字去翻审计太贵，而这恰恰是看到一个
	// 上限时第一个会冒出来的问题。
	UpdatedBy string `json:"updated_by,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// ListWithdrawLimitsHandler 返回各账号的提现额度。
//
// **没设过额度的账号不会出现在结果里**，由前端显示成「未设置（不能提现）」。
// 补一行零值会让「设成 0」和「没设过」长得一样，而前者是有人刻意关掉了
// 这个账号的提现，后者是还没人管过它。
func ListWithdrawLimitsHandler(store WithdrawQuerier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := store.ListWithdrawLimits(r.Context())
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]withdrawLimitItem, 0, len(rows))
		for _, l := range rows {
			out = append(out, withdrawLimitItem{
				Account: l.Account, PerOperation: l.PerOperation, PerDay: l.PerDay,
				UpdatedBy: l.UpdatedBy, UpdatedAt: formatUTC(l.UpdatedAt),
			})
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}
