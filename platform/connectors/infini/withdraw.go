package infini

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// 资金提现（/v2/funds）。
//
// 与卡片端点的根本区别：**这是把钱转到平台之外**。开卡最坏的结果是钱变成
// 一张卡上的余额、还在自己名下；提现最坏的结果是钱到了别人的钱包，
// 不可逆、不可追回。所以这一组的每一处都比卡片那边更保守。
//
// 一个实实在在的好消息：提现有**真正的幂等键**（request_id，UUID），
// 重投返回原结果并标 is_duplicate。卡 API 完全没有这个能力（我们靠
// card_alias 硬凑），所以提现的不确定态反而好处理——超时后按 request_id
// 查一次状态就有答案，不必等对账。

// WithdrawRequest 是一次提现申请。
type WithdrawRequest struct {
	// RequestID 是幂等键，**必填**。见 Withdraw 的注释。
	RequestID string
	Chain     string
	TokenType string
	// SourceCurrency 留空时等于 TokenType；填 USD 表示从 USD 现金账户扣。
	SourceCurrency string
	Amount         string
	WalletAddress  string
	Note           string
}

// WithdrawResult 是申请的受理结果。
type WithdrawResult struct {
	RequestID string
	Status    string
	// IsDuplicate 为真表示这是同一个 request_id 的重投，上游返回的是原结果。
	IsDuplicate bool
}

// WithdrawState 是一笔提现的当前状态。
type WithdrawState struct {
	RequestID string
	Status    string
	// Amount 是从账户扣掉的总额，ActualAmount 是实际到链上的金额。
	// 两个数不同——手续费在中间。只显示一个会让人对不上账。
	Amount       string
	ActualAmount string

	TransactionHash string
	Chain           string
	TokenType       string
	SourceCurrency  string

	// 手续费分 gas 与 fx 两项：跨币种提现两者都有。
	GasFee         string
	GasFeeCurrency string
	FXFee          string
	FXFeeCurrency  string
	FeePaidBy      string
}

// WithdrawFee 是某条链某个代币的提现手续费。
type WithdrawFee struct {
	Chain       string
	TokenType   string
	WithdrawFee string
}

// Withdraw 申请一次提现。
//
// **RequestID 必填**。文档说它可以省略、由上游生成——但那样重试就没有任何
// 幂等保障了，而提现不可逆：一次没有幂等键的重试可能就是转两次。所以这里
// 在本地拒绝空值，不把这个选择留给调用方。
func (c *Client) Withdraw(ctx context.Context, req WithdrawRequest) (WithdrawResult, error) {
	const path = "/v2/funds/withdraw"

	if strings.TrimSpace(req.RequestID) == "" {
		return WithdrawResult{}, connector.NewError(connector.KindRejected, "infini "+path,
			fmt.Errorf("request_id 必填：省略它等于放弃重试保护，而提现不可逆"))
	}

	payload := map[string]any{
		"request_id":     req.RequestID,
		"chain":          req.Chain,
		"token_type":     req.TokenType,
		"amount":         req.Amount,
		"wallet_address": req.WalletAddress,
	}
	if req.SourceCurrency != "" {
		payload["source_currency"] = req.SourceCurrency
	}
	if req.Note != "" {
		payload["note"] = req.Note
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return WithdrawResult{}, connector.NewError(connector.KindInternal, "infini "+path, err)
	}

	var data struct {
		RequestID   string `json:"request_id"`
		Status      string `json:"status"`
		IsDuplicate bool   `json:"is_duplicate"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &data); err != nil {
		return WithdrawResult{}, err
	}
	return WithdrawResult{
		RequestID: data.RequestID, Status: data.Status, IsDuplicate: data.IsDuplicate,
	}, nil
}

// WithdrawStatus 查一笔提现的当前状态。
//
// 这是提现比开卡好办的地方：超时之后按 request_id 查一次就有答案，
// 不必像开卡那样靠 alias 对账、还要等宽限期。
func (c *Client) WithdrawStatus(ctx context.Context, requestID string) (WithdrawState, error) {
	values := url.Values{"request_id": {requestID}}
	// 查询串必须进签名路径（文档第 4 章），do 收到的就是完整路径。
	path := "/v2/funds/withdraw/status?" + values.Encode()

	var data struct {
		RequestID       string `json:"request_id"`
		Status          string `json:"status"`
		Amount          string `json:"amount"`
		ActualAmount    string `json:"actual_amount"`
		TransactionHash string `json:"transaction_hash"`
		Chain           string `json:"chain"`
		TokenType       string `json:"token_type"`
		SourceCurrency  string `json:"source_currency"`
		GasFee          string `json:"gas_fee"`
		GasFeeCurrency  string `json:"gas_fee_currency"`
		FXFee           string `json:"fx_fee"`
		FXFeeCurrency   string `json:"fx_fee_currency"`
		FeePaidBy       string `json:"fee_paid_by"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &data); err != nil {
		return WithdrawState{}, err
	}
	return WithdrawState(data), nil
}

// WithdrawFees 列出各链各代币的提现手续费。
//
// 开提现表单前要能告诉人「这条链收多少」：以太坊两块、Tron 一块，
// 差别足以改变选哪条链。
func (c *Client) WithdrawFees(ctx context.Context) ([]WithdrawFee, error) {
	var data struct {
		Fees []struct {
			Chain       string `json:"chain"`
			TokenType   string `json:"token_type"`
			WithdrawFee string `json:"withdraw_fee"`
		} `json:"fees"`
	}
	if err := c.do(ctx, http.MethodGet, "/v2/funds/withdraw/fees", nil, &data); err != nil {
		return nil, err
	}
	out := make([]WithdrawFee, 0, len(data.Fees))
	for _, f := range data.Fees {
		out = append(out, WithdrawFee{Chain: f.Chain, TokenType: f.TokenType, WithdrawFee: f.WithdrawFee})
	}
	return out, nil
}
