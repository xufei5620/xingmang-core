package cards

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
)

// 资金提现。
//
// 放在 cards 包里而不是另起一个模块：账号、凭据、连接器客户端全在这儿，
// 分出去要把那套装配再抄一遍，而抄出来的两份迟早不一致。名字上把「卡片」
// 与「资金」分清楚即可。
//
// **这是整个平台风险最高的动作**：把钱转到平台之外，不可逆、不可追回。
// 三道闸依次是——地址白名单、金额上限（不许 unlimited）、幂等台账。

var (
	// ErrAddressNotAllowed：目标地址没有登记，或不属于这个账号/这条链。
	ErrAddressNotAllowed = errors.New("cards: 提现地址未登记")
	// ErrWithdrawLimitsUnbounded：提现额度配成了 unlimited。
	ErrWithdrawLimitsUnbounded = errors.New("cards: 提现额度不接受 unlimited")
)

// WithdrawAddress 是一条登记过的提现地址。
//
// **调用方只能选「哪一条登记」，地址本身由服务端从库里取。**
// 让调用方传地址、服务端再比对是另一回事——那样一个比对逻辑的疏漏
// 就能让任意地址过去。
type WithdrawAddress struct {
	ID      string
	Account string
	Chain   string
	Address string
	Label   string
}

// WithdrawRequest 是一次提现申请。注意它**不含地址**，只含地址登记的 id。
type WithdrawRequest struct {
	Account string
	// RequestID 是幂等键，直接作为上游的 request_id（UUID）。
	RequestID string
	Chain     string
	TokenType string
	// SourceCurrency 留空等于 TokenType。
	SourceCurrency string
	Amount         string
	AddressID      string
	Note           string
}

// WithdrawOutcome 是受理结果。
type WithdrawOutcome struct {
	RequestID   string
	Account     string
	Status      string
	IsDuplicate bool
}

// WithdrawStore 是提现需要的持久化能力。
type WithdrawStore interface {
	// AllowedAddress 取一条登记；不存在返回 ErrAddressNotAllowed。
	AllowedAddress(ctx context.Context, id string) (WithdrawAddress, error)
	// BeginWithdraw 落一条待收敛的提现；同 requestID 已存在时第二个返回值为 true。
	BeginWithdraw(ctx context.Context, w WithdrawRecord) (WithdrawRecord, bool, error)
	// UpdateWithdraw 回写状态（受理结果或后续查询到的终态）。
	UpdateWithdraw(ctx context.Context, w WithdrawRecord) error
	// OpenWithdrawals 返回尚未收敛的提现（pending / processing）。
	//
	// 只返回未收敛的：终态的再查一次既浪费配额，也提高触发限流的概率
	// ——与 UnresolvedOperations 同一条纪律。
	OpenWithdrawals(ctx context.Context) ([]WithdrawRecord, error)
	// WithdrawnToday 返回某账号今日已提现累计（十进制文本）。
	//
	// **必须把未收敛的算进去**：那些可能真的转出去了，当没转过会让上限
	// 在最需要生效的时候失效——与卡片那边同一条纪律。
	WithdrawnToday(ctx context.Context, account string, day time.Time) (string, error)
}

// WithdrawRecord 是提现台账里的一条。
type WithdrawRecord struct {
	RequestID string
	Account   string
	Chain     string
	TokenType string
	Amount    string
	AddressID string
	Address   string
	Status    string
	// TxHash 与手续费在终态查询时回填。
	TxHash         string
	ActualAmount   string
	GasFee         string
	GasFeeCurrency string
	FXFee          string
	FXFeeCurrency  string
	Note           string
	StartedAt      time.Time
	UpdatedAt      time.Time
}

// CheckWithdraw 校验一笔提现金额。
//
// 与卡片的 Check 只差一点，但那一点很要紧：**不接受 unlimited**。
// 卡片那边可以不限，因为 Infini 账户余额本身就是硬顶；提现的目的就是
// 把余额搬空，余额不构成任何约束。
func (l Limits) CheckWithdraw(amount, spentToday string) error {
	if unlimited(l.PerOperation) || unlimited(l.PerDay) {
		return fmt.Errorf("%w：把余额搬空正是提现的目的，余额不构成上限",
			ErrWithdrawLimitsUnbounded)
	}
	return l.Check(amount, spentToday)
}

// WithdrawService 执行提现。
type WithdrawService struct {
	accounts map[string]Account
	store    WithdrawStore
	now      func() time.Time
}

// NewWithdrawService 组装提现服务。
func NewWithdrawService(accounts []Account, store WithdrawStore, now func() time.Time) *WithdrawService {
	if now == nil {
		now = time.Now
	}
	s := &WithdrawService{accounts: make(map[string]Account, len(accounts)), store: store, now: now}
	for _, a := range accounts {
		s.accounts[a.ID] = a
	}
	return s
}

// Withdraw 发起一次提现。
//
// 顺序是刻意的，每一步都在**打上游之前**：
//
//	账号解析 → 地址白名单 → 链一致性 → 金额上限 → 幂等台账 → 调上游
//
// 拦晚了钱就已经出去了，而提现没有「撤回」这回事。
func (s *WithdrawService) Withdraw(ctx context.Context, req WithdrawRequest) (WithdrawOutcome, error) {
	acct, ok := s.accounts[req.Account]
	if !ok {
		return WithdrawOutcome{}, fmt.Errorf("%w: %q", ErrUnknownAccount, req.Account)
	}
	if strings.TrimSpace(req.RequestID) == "" {
		return WithdrawOutcome{}, fmt.Errorf("cards: 提现必须带幂等键")
	}

	addr, err := s.store.AllowedAddress(ctx, req.AddressID)
	if err != nil {
		return WithdrawOutcome{}, err
	}
	if addr.Account != req.Account {
		// 两个账号的资金是分开的：拿 A 登记的地址从 B 提现，
		// 等于绕过了 A 那一侧的白名单审核。
		return WithdrawOutcome{}, fmt.Errorf("%w：该地址登记在账号 %s 下", ErrAddressNotAllowed, addr.Account)
	}
	if !strings.EqualFold(addr.Chain, req.Chain) {
		// 同一串地址在不同链上可能都「看起来合法」，而转错链的钱找不回来。
		return WithdrawOutcome{}, fmt.Errorf("%w：该地址登记的链是 %s，本次却选了 %s",
			ErrAddressNotAllowed, addr.Chain, req.Chain)
	}

	spent, err := s.store.WithdrawnToday(ctx, acct.ID, s.now())
	if err != nil {
		return WithdrawOutcome{}, fmt.Errorf("读取今日提现累计: %w", err)
	}
	if err := acct.WithdrawLimits.CheckWithdraw(req.Amount, spent); err != nil {
		return WithdrawOutcome{}, err
	}

	record := WithdrawRecord{
		RequestID: req.RequestID, Account: acct.ID, Chain: req.Chain,
		TokenType: req.TokenType, Amount: req.Amount,
		AddressID: addr.ID, Address: addr.Address, Note: req.Note,
		Status: "pending", StartedAt: s.now(), UpdatedAt: s.now(),
	}
	stored, existed, err := s.store.BeginWithdraw(ctx, record)
	if err != nil {
		return WithdrawOutcome{}, fmt.Errorf("落提现台账: %w", err)
	}
	if existed {
		// 同键重投：绝不再打一次上游。上游自己也幂等，但我们没必要靠它——
		// 少一次调用就少一次出错的机会。
		return WithdrawOutcome{
			RequestID: stored.RequestID, Account: stored.Account,
			Status: stored.Status, IsDuplicate: true,
		}, nil
	}

	res, err := acct.Client.Withdraw(ctx, infini.WithdrawRequest{
		RequestID: req.RequestID, Chain: req.Chain, TokenType: req.TokenType,
		SourceCurrency: req.SourceCurrency, Amount: req.Amount,
		// **用库里存的地址**，不是调用方传的。
		WalletAddress: addr.Address, Note: req.Note,
	})
	if err != nil {
		record.Status = "failed"
		record.UpdatedAt = s.now()
		if storeErr := s.store.UpdateWithdraw(ctx, record); storeErr != nil {
			return WithdrawOutcome{}, fmt.Errorf("上游失败且台账写入失败: %w", storeErr)
		}
		return WithdrawOutcome{RequestID: req.RequestID, Account: acct.ID, Status: "failed"}, err
	}

	record.Status = res.Status
	record.UpdatedAt = s.now()
	if err := s.store.UpdateWithdraw(ctx, record); err != nil {
		return WithdrawOutcome{}, fmt.Errorf("台账写入: %w", err)
	}
	return WithdrawOutcome{
		RequestID: res.RequestID, Account: acct.ID,
		Status: res.Status, IsDuplicate: res.IsDuplicate,
	}, nil
}

// RefreshWithdraw 按 request_id 查一次上游状态并回写。
//
// 提现比开卡好办的地方就在这里：上游有真正的幂等键，超时之后查一次就有
// 答案，不必像开卡那样靠 alias 对账、还要等宽限期。
func (s *WithdrawService) RefreshWithdraw(ctx context.Context, account, requestID string) error {
	acct, ok := s.accounts[account]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownAccount, account)
	}
	st, err := acct.Client.WithdrawStatus(ctx, requestID)
	if err != nil {
		return err
	}
	return s.store.UpdateWithdraw(ctx, WithdrawRecord{
		RequestID: requestID, Account: account, Status: st.Status,
		TxHash: st.TransactionHash, ActualAmount: st.ActualAmount,
		GasFee: st.GasFee, GasFeeCurrency: st.GasFeeCurrency,
		FXFee: st.FXFee, FXFeeCurrency: st.FXFeeCurrency,
		UpdatedAt: s.now(),
	})
}

// OpenWithdrawals 返回尚未收敛的提现，供周期同步遍历。
//
// 转发到 store 而不是让调用方直接拿 store：同步作业只该认识
// WithdrawService 这一个入口，否则「谁在推进提现」会散在两个地方。
func (s *WithdrawService) OpenWithdrawals(ctx context.Context) ([]WithdrawRecord, error) {
	return s.store.OpenWithdrawals(ctx)
}
