package infini

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Fake 是内存版的 CardClient，供领域层与作业的测试使用。
//
// 存在的理由不只是「不联网」：开卡是花真钱的异步操作，不确定态、限流、
// 被拒这些分支在真实环境里既难复现又昂贵，而它们恰恰是最需要覆盖的。
//
// 刻意保留的行为：新申请的卡是 init 而不是 active，必须显式 Advance
// 才会推进——否则轮询逻辑在测试里根本走不到。
type Fake struct {
	mu    sync.Mutex
	seq   int
	cards map[string]*Card

	// failNext 让调用方指定下一次调用返回什么错误。
	failNext error

	// Now 可注入，默认用固定时刻，让测试断言稳定。
	Now func() time.Time
}

func NewFake() *Fake {
	return &Fake{
		cards: make(map[string]*Card),
		Now: func() time.Time {
			return time.Date(2026, time.September, 1, 10, 0, 0, 0, time.UTC)
		},
	}
}

// FailNext 让下一次调用返回 err。
func (f *Fake) FailNext(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = err
}

// Advance 把一张卡推进到指定状态，模拟上游的异步流转。
func (f *Fake) Advance(cardID, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.cards[cardID]; ok {
		c.Status = status
		c.UpdatedAt = f.Now()
	}
}

func (f *Fake) takeErr() error {
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return err
	}
	return nil
}

func (f *Fake) ApplyCard(ctx context.Context, req ApplyCardRequest) (CardApplication, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeErr(); err != nil {
		return CardApplication{}, err
	}

	f.seq++
	id := "fake-card-" + strconv.Itoa(f.seq)
	f.cards[id] = &Card{
		ID:         id,
		Mask:       "000000******0000",
		HolderName: req.HolderName,
		Alias:      req.Alias,
		Status:     "init",
		Currency:   "USD",
		CreatedAt:  f.Now(),
		UpdatedAt:  f.Now(),
	}

	return CardApplication{
		ID:               id,
		Status:           "init",
		TotalTopUpAmount: req.TopUpAmount,
		TotalFee:         "0",
		TotalPayAmount:   req.TopUpAmount,
	}, nil
}

func (f *Fake) ListCards(ctx context.Context, q ListCardsQuery) (CardPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeErr(); err != nil {
		return CardPage{}, err
	}

	var matched []Card
	for _, c := range f.cards {
		if q.Alias != "" && c.Alias != q.Alias {
			continue
		}
		if q.Status != "" && c.Status != q.Status {
			continue
		}
		matched = append(matched, *c)
	}

	return CardPage{
		Cards:      matched,
		Total:      len(matched),
		Page:       1,
		PageSize:   len(matched),
		TotalPages: 1,
	}, nil
}

func (f *Fake) CardStatus(ctx context.Context, cardID string) (Card, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeErr(); err != nil {
		return Card{}, err
	}
	c, ok := f.cards[cardID]
	if !ok {
		return Card{}, fmt.Errorf("fake: 卡 %s 不存在", cardID)
	}
	return *c, nil
}

func (f *Fake) CardTransactions(ctx context.Context, cardID string, page, pageSize int) (TransactionPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeErr(); err != nil {
		return TransactionPage{}, err
	}
	return TransactionPage{Page: 1, PageSize: pageSize, TotalPages: 1}, nil
}

func (f *Fake) TopUpCard(ctx context.Context, req TopUpRequest) (FundsResult, error) {
	return f.fundsOp(req, +1)
}

func (f *Fake) RedeemCard(ctx context.Context, req TopUpRequest) (FundsResult, error) {
	return f.fundsOp(req, -1)
}

func (f *Fake) fundsOp(req TopUpRequest, sign int) (FundsResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeErr(); err != nil {
		return FundsResult{}, err
	}
	c, ok := f.cards[req.CardID]
	if !ok {
		return FundsResult{}, fmt.Errorf("fake: 卡 %s 不存在", req.CardID)
	}
	c.UpdatedAt = f.Now()
	// 替身不做金额算术：真实换算口径（token 与法币的关系）尚未验证，
	// 在替身里编一个会让依赖它的测试建立在假前提上。
	return FundsResult{TxID: "fake-tx", CardBalance: ""}, nil
}

func (f *Fake) FreezeCard(ctx context.Context, cardID string) error {
	return f.setStatus(cardID, "frozen")
}

func (f *Fake) UnfreezeCard(ctx context.Context, cardID string) error {
	return f.setStatus(cardID, "active")
}

func (f *Fake) setStatus(cardID, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeErr(); err != nil {
		return err
	}
	c, ok := f.cards[cardID]
	if !ok {
		return fmt.Errorf("fake: 卡 %s 不存在", cardID)
	}
	c.Status = status
	c.UpdatedAt = f.Now()
	return nil
}

func (f *Fake) RevealCard(ctx context.Context, cardID string) (RevealedCard, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeErr(); err != nil {
		return RevealedCard{}, err
	}
	if _, ok := f.cards[cardID]; !ok {
		return RevealedCard{}, fmt.Errorf("fake: 卡 %s 不存在", cardID)
	}

	// 刻意不长得像真卡号：测试固定值会被复制粘贴，一个 16 位的
	// 4/5 开头数字迟早会被人当成真数据处理。
	return RevealedCard{
		Number:     "fake-pan-" + strings.TrimPrefix(cardID, "fake-card-"),
		CVV:        "000",
		ExpiryMMYY: "0130",
		Currency:   "USD",
	}, nil
}

// BatchCardStatus 与真实客户端同构：不存在的卡不出现在结果里。
func (f *Fake) BatchCardStatus(ctx context.Context, cardIDs []string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeErr(); err != nil {
		return nil, err
	}
	if len(cardIDs) > batchStatusMax {
		return nil, fmt.Errorf("fake: 一次最多 %d 张", batchStatusMax)
	}
	out := make(map[string]string, len(cardIDs))
	for _, id := range cardIDs {
		if c, ok := f.cards[id]; ok {
			out[id] = c.Status
		}
	}
	return out, nil
}

// AccountBalances 回一组固定的演示余额。
func (f *Fake) AccountBalances(ctx context.Context) (AccountBalances, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeErr(); err != nil {
		return AccountBalances{}, err
	}
	return AccountBalances{USDT: "1000.00", USDC: "0", USD: "0"}, nil
}
