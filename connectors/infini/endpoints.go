package infini

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// ---------- 开卡 ----------

// ApplyCardRequest 是开卡申请的入参。
//
// Alias 是平台自己的幂等信标：写进 card_alias，超时后用
// ListCards(Alias=...) 精确匹配来判定卡到底开没开成。上游是否真的接受
// 并回显这个字段尚未验证——若不接受，幂等要退回「持卡人 + 时间窗 + 金额」
// 的模糊匹配，见分支 Handoff 的 risks 段。
type ApplyCardRequest struct {
	ProductID   int
	TopUpAmount string // 十进制文本，不用 float
	TokenType   string // USDT / USDC
	UserEmail   string
	HolderName  string
	Alias       string
}

// CardApplication 是开卡申请单。
//
// 开卡是**异步**的：这里返回的不是一张可用的卡，而是一张申请单，
// 要轮询 CardStatus 直到 status 变成 active。
//
// 三个金额保持文本形态：它们的单位是 token（USDT/USDC），而
// money.CurrencyScale 只登记了法币——最小单位小数位无从判断时宁可不换算，
// 也不猜 2 位或 6 位（宪法条款 13 的同一条纪律）。
type CardApplication struct {
	ID               string
	Status           string
	TotalTopUpAmount string
	TotalFee         string
	TotalPayAmount   string
	UpstreamMessage  string
}

func (c *Client) ApplyCard(ctx context.Context, req ApplyCardRequest) (CardApplication, error) {
	payload := map[string]any{
		"product_id":    req.ProductID,
		"top_up_amount": req.TopUpAmount,
		"token_type":    req.TokenType,
		"user_email":    req.UserEmail,
		"holder_name":   req.HolderName,
	}
	if req.Alias != "" {
		payload["card_alias"] = req.Alias
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return CardApplication{}, connector.NewError(connector.KindInternal, "infini apply", err)
	}

	var data struct {
		ID               string `json:"id"`
		Status           string `json:"status"`
		TotalTopUpAmount string `json:"total_top_up_amount"`
		TotalFee         string `json:"total_fee"`
		TotalPayAmount   string `json:"total_pay_amount"`
		Message          string `json:"message"`
	}
	if err := c.do(ctx, http.MethodPost, "/v2/cards/apply", body, &data); err != nil {
		return CardApplication{}, err
	}

	return CardApplication{
		ID:               data.ID,
		Status:           data.Status,
		TotalTopUpAmount: data.TotalTopUpAmount,
		TotalFee:         data.TotalFee,
		TotalPayAmount:   data.TotalPayAmount,
		UpstreamMessage:  data.Message,
	}, nil
}

// ---------- 查询 ----------

// ListCardsQuery 是卡片列表的过滤条件。
type ListCardsQuery struct {
	Status   string
	Alias    string
	Page     int
	PageSize int
}

// CardPage 是一页卡片。
type CardPage struct {
	Cards      []Card
	Total      int
	Page       int
	PageSize   int
	TotalPages int
}

func (c *Client) ListCards(ctx context.Context, q ListCardsQuery) (CardPage, error) {
	values := url.Values{}
	if q.Status != "" {
		values.Set("status", q.Status)
	}
	if q.Alias != "" {
		values.Set("card_alias", q.Alias)
	}
	if q.Page > 0 {
		values.Set("page", strconv.Itoa(q.Page))
	}
	if q.PageSize > 0 {
		values.Set("page_size", strconv.Itoa(q.PageSize))
	}

	path := "/v2/cards/list"
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var data struct {
		Cards      []rawCard `json:"cards"`
		Total      int       `json:"total"`
		Page       int       `json:"page"`
		PageSize   int       `json:"page_size"`
		TotalPages int       `json:"total_pages"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &data); err != nil {
		return CardPage{}, err
	}

	cards := make([]Card, 0, len(data.Cards))
	for _, raw := range data.Cards {
		card, err := raw.toCard()
		if err != nil {
			return CardPage{}, connector.NewError(connector.KindBadResponse, "infini GET /v2/cards/list", err)
		}
		cards = append(cards, card)
	}

	return CardPage{
		Cards:      cards,
		Total:      data.Total,
		Page:       data.Page,
		PageSize:   data.PageSize,
		TotalPages: data.TotalPages,
	}, nil
}

// CardStatus 查单张卡的当前状态，也是开卡异步流程的轮询入口。
func (c *Client) CardStatus(ctx context.Context, cardID string) (Card, error) {
	path := "/v2/cards/status?" + url.Values{"id": {cardID}}.Encode()

	var raw rawCard
	if err := c.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return Card{}, err
	}
	card, err := raw.toCard()
	if err != nil {
		return Card{}, connector.NewError(connector.KindBadResponse, "infini GET /v2/cards/status", err)
	}
	return card, nil
}

// ---------- 资金操作 ----------

// TopUpRequest 是充值/赎回的入参（两个接口的请求体形状相同）。
type TopUpRequest struct {
	CardID    string
	Amount    string // 十进制文本
	TokenType string
	Note      string
}

// FundsResult 是充值/赎回的结果。CardBalance 保持文本形态，理由同
// CardApplication 的三个金额。
type FundsResult struct {
	TxID        string
	CardBalance string
}

func (c *Client) TopUpCard(ctx context.Context, req TopUpRequest) (FundsResult, error) {
	return c.fundsOp(ctx, "/v2/cards/top-up", req)
}

func (c *Client) RedeemCard(ctx context.Context, req TopUpRequest) (FundsResult, error) {
	return c.fundsOp(ctx, "/v2/cards/redeem", req)
}

func (c *Client) fundsOp(ctx context.Context, path string, req TopUpRequest) (FundsResult, error) {
	body, err := json.Marshal(map[string]any{
		"id":         req.CardID,
		"amount":     req.Amount,
		"token_type": req.TokenType,
		"note":       req.Note,
	})
	if err != nil {
		return FundsResult{}, connector.NewError(connector.KindInternal, "infini "+path, err)
	}

	var data struct {
		TxID        string `json:"tx_id"`
		CardBalance string `json:"card_balance"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &data); err != nil {
		return FundsResult{}, err
	}
	return FundsResult{TxID: data.TxID, CardBalance: data.CardBalance}, nil
}

// ---------- 冻结与解冻 ----------

func (c *Client) FreezeCard(ctx context.Context, cardID string) error {
	return c.switchOp(ctx, "/v2/cards/freeze", cardID)
}

func (c *Client) UnfreezeCard(ctx context.Context, cardID string) error {
	return c.switchOp(ctx, "/v2/cards/unfreeze", cardID)
}

// switchOp 处理 freeze/unfreeze 这类返回 {success, message} 的接口。
//
// 信封 code=0 不等于操作成功：data.success=false 时上游是在说「这张卡已经
// 冻着了」之类的话，当成功会让调用方以为状态已变。
func (c *Client) switchOp(ctx context.Context, path, cardID string) error {
	body, err := json.Marshal(map[string]any{"id": cardID})
	if err != nil {
		return connector.NewError(connector.KindInternal, "infini "+path, err)
	}

	var data struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := c.do(ctx, http.MethodPost, path, body, &data); err != nil {
		return err
	}
	if !data.Success {
		// 上游原文只进 Unwrap 链
		return connector.NewError(connector.KindRejected, "infini "+path,
			fmt.Errorf("upstream refused: %s", data.Message))
	}
	return nil
}

// ---------- 敏感信息 ----------

// RevealedCard 是明文卡面数据。
//
// 这个类型**永远不该被落库、写日志或进入审计正文**（宪法条款 7），
// 所以它的 String/GoString 都是打码的：一次 %v 就足以把卡号写进日志，
// 而日志会被归档、会被搜索。调用方要用卡号必须显式取字段。
type RevealedCard struct {
	Number     string
	CVV        string
	ExpiryMMYY string
	Currency   string
}

// String 实现 fmt.Stringer，永远不输出明文。
func (r RevealedCard) String() string { return "RevealedCard{redacted}" }

// GoString 实现 fmt.GoStringer，挡住 %#v。
func (r RevealedCard) GoString() string { return "RevealedCard{redacted}" }

func (c *Client) RevealCard(ctx context.Context, cardID string) (RevealedCard, error) {
	body, err := json.Marshal(map[string]any{"id": cardID})
	if err != nil {
		return RevealedCard{}, connector.NewError(connector.KindInternal, "infini reveal", err)
	}

	var data struct {
		CardNumber     string `json:"card_number"`
		CVV            string `json:"cvv"`
		ExpirationMMYY string `json:"expiration_mmyy"`
		CardCurrency   string `json:"card_currency"`
	}
	if err := c.do(ctx, http.MethodPost, "/v2/cards/reveal", body, &data); err != nil {
		return RevealedCard{}, err
	}

	return RevealedCard{
		Number:     data.CardNumber,
		CVV:        data.CVV,
		ExpiryMMYY: data.ExpirationMMYY,
		Currency:   data.CardCurrency,
	}, nil
}

// ---------- 交易流水 ----------

// CardTransaction 是一笔卡交易。
type CardTransaction struct {
	CardID      string
	Type        string
	AmountMinor int64
	FeeMinor    int64
	Status      string
	Currency    string
	Merchant    string
	OccurredAt  string
}

// TransactionPage 是一页流水。
type TransactionPage struct {
	Transactions []CardTransaction
	Total        int
	Page         int
	PageSize     int
	TotalPages   int
}

func (c *Client) CardTransactions(ctx context.Context, cardID string, page, pageSize int) (TransactionPage, error) {
	values := url.Values{"id": {cardID}}
	if page > 0 {
		values.Set("page", strconv.Itoa(page))
	}
	if pageSize > 0 {
		values.Set("page_size", strconv.Itoa(pageSize))
	}

	var data struct {
		Transactions []struct {
			CardID          string          `json:"card_id"`
			Type            string          `json:"type"`
			Amount          money.RawAmount `json:"amount"`
			Fee             money.RawAmount `json:"fee"`
			Status          string          `json:"status"`
			Currency        string          `json:"currency"`
			Merchant        string          `json:"merchant"`
			TransactionTime string          `json:"transaction_time"`
		} `json:"transactions"`
		Total      int `json:"total"`
		Page       int `json:"page"`
		PageSize   int `json:"page_size"`
		TotalPages int `json:"total_pages"`
	}
	if err := c.do(ctx, http.MethodGet, "/v2/cards/transactions?"+values.Encode(), nil, &data); err != nil {
		return TransactionPage{}, err
	}

	const op = "infini GET /v2/cards/transactions"
	txs := make([]CardTransaction, 0, len(data.Transactions))
	for _, raw := range data.Transactions {
		scale, err := money.CurrencyScale(raw.Currency)
		if err != nil {
			return TransactionPage{}, connector.NewError(connector.KindBadResponse, op, err)
		}
		amount, err := minorOrZero(raw.Amount, scale)
		if err != nil {
			return TransactionPage{}, connector.NewError(connector.KindBadResponse, op, err)
		}
		fee, err := minorOrZero(raw.Fee, scale)
		if err != nil {
			return TransactionPage{}, connector.NewError(connector.KindBadResponse, op, err)
		}

		txs = append(txs, CardTransaction{
			CardID:      raw.CardID,
			Type:        raw.Type,
			AmountMinor: amount,
			FeeMinor:    fee,
			Status:      raw.Status,
			Currency:    raw.Currency,
			Merchant:    raw.Merchant,
			OccurredAt:  raw.TransactionTime,
		})
	}

	return TransactionPage{
		Transactions: txs,
		Total:        data.Total,
		Page:         data.Page,
		PageSize:     data.PageSize,
		TotalPages:   data.TotalPages,
	}, nil
}

// minorOrZero 把金额文本换算成最小单位；空字段按 0 处理（手续费常常不返回）。
func minorOrZero(raw money.RawAmount, scale int) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	return money.ParseMinorUnits(string(raw), scale)
}
