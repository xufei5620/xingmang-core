package httpapi

import (
	"context"
	"net/http"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// CardBalanceReader 读某个账号在上游的可用余额。
//
// 定成窄接口而不是直接吃 *cards.Service：这条路径要能在没有网络的情况下
// 被测到，而它的两个分支（逐账号返回、单账号失败降级）恰恰是最该被测的。
type CardBalanceReader interface {
	AccountBalances(ctx context.Context, account string) (infini.AccountBalances, error)
}

type cardBalanceItem struct {
	Account string `json:"account"`
	USDT    string `json:"usdt,omitempty"`
	USDC    string `json:"usdc,omitempty"`
	USD     string `json:"usd,omitempty"`
	// Error 是失败**分类**，不是上游原文（ADR-004：供应商原文只进服务端
	// 日志）。有它就说明这个账号这次没取到，页面据此显示成"—"而不是"0"——
	// 把取不到显示成零余额，会让人以为钱花光了。
	Error string `json:"error,omitempty"`
}

// CardBalancesHandler 返回每个账号的资金池可用余额。
//
// 这是**实时上游调用**，不是投影：余额没有对应的同步作业，也不该有——
// 它变化频繁，而且只在人要看的时候才重要。因此这个端点比其余读端点慢，
// 前端应当按需拉取而不是随列表一起轮询。
//
// 单个账号失败不影响其余账号：两个账号的凭据、权限、IP 白名单各自独立，
// 一个配歪了不该让另一个也看不到数——整页 500 会让运营以为"余额功能坏了"，
// 而实际上只坏了一半。
func CardBalancesHandler(reader CardBalanceReader, accounts []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := make([]cardBalanceItem, 0, len(accounts))
		// 按配置顺序遍历，不按 map 迭代：否则页面上两个账号会随机换位置。
		for _, account := range accounts {
			item := cardBalanceItem{Account: account}
			balances, err := reader.AccountBalances(r.Context(), account)
			if err != nil {
				item.Error = string(connector.KindOf(err))
				LoggerFrom(r.Context()).ErrorContext(r.Context(), "card_balances_failed",
					"module", "httpapi", "account", account, "err", err.Error())
			} else {
				item.USDT, item.USDC, item.USD = balances.USDT, balances.USDC, balances.USD
			}
			out = append(out, item)
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": out})
	}
}
