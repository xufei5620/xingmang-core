package cards

import (
	"context"
	"fmt"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// FundsRequest 是充值与赎回的入参。
type FundsRequest struct {
	// Account 必填，理由同 IssueRequest.Account。
	Account        string
	IdempotencyKey string
	CardID         string
	Amount         string
	TokenType      string
	Note           string
}

// FundsOutcome 是充值/赎回的结果。
//
// CardBalance 保持上游给的文本形态，理由与连接器层一致：单位是 token，
// 真实标度尚未验证。
type FundsOutcome struct {
	OperationKey string
	Account      string
	State        OperationState
	TxID         string
	CardBalance  string
}

// TopUpCard 给已有的卡追加额度。
//
// 与开卡同级别对待：花钱、上游不幂等、超时落 unknown 禁止重试。
func (s *Service) TopUpCard(ctx context.Context, req FundsRequest) (FundsOutcome, error) {
	acct, err := s.account(req.Account)
	if err != nil {
		return FundsOutcome{}, err
	}

	spent, err := s.store.SpentToday(ctx, acct.ID, OpTopUp, s.now())
	if err != nil {
		return FundsOutcome{}, fmt.Errorf("读取今日累计: %w", err)
	}
	if err := acct.Limits.Check(req.Amount, spent); err != nil {
		return FundsOutcome{}, err
	}

	return s.fundsOperation(ctx, acct, OpTopUp, req, acct.Client.TopUpCard)
}

// RedeemCard 把卡上的余额退回账户。
//
// **不受金额上限约束**：赎回是资金回流不是花钱，用上限卡住它，
// 会在最需要止损的时候拦住止损动作。幂等仍然要——重复赎回会重复动账。
func (s *Service) RedeemCard(ctx context.Context, req FundsRequest) (FundsOutcome, error) {
	acct, err := s.account(req.Account)
	if err != nil {
		return FundsOutcome{}, err
	}
	return s.fundsOperation(ctx, acct, OpRedeem, req, acct.Client.RedeemCard)
}

// fundsOperation 是充值与赎回共用的编排：幂等台账 → 调上游 → 收敛台账。
func (s *Service) fundsOperation(
	ctx context.Context,
	acct Account,
	kind string,
	req FundsRequest,
	call func(context.Context, infini.TopUpRequest) (infini.FundsResult, error),
) (FundsOutcome, error) {
	now := s.now()
	op := Operation{
		IdempotencyKey: req.IdempotencyKey,
		Account:        acct.ID,
		Kind:           kind,
		State:          StatePending,
		CardID:         req.CardID,
		Alias:          AliasFor(req.IdempotencyKey),
		AmountText:     req.Amount,
		TokenType:      req.TokenType,
		StartedAt:      now,
	}

	stored, existed, err := s.store.BeginOperation(ctx, op)
	if err != nil {
		return FundsOutcome{}, fmt.Errorf("落台账: %w", err)
	}
	if existed {
		return FundsOutcome{
			OperationKey: stored.IdempotencyKey,
			Account:      stored.Account,
			State:        stored.State,
		}, nil
	}

	res, err := call(ctx, infini.TopUpRequest{
		CardID:    req.CardID,
		Amount:    req.Amount,
		TokenType: req.TokenType,
		Note:      req.Note,
	})
	if err != nil {
		resolved := op
		resolved.State = stateForUpstreamError(err, kind)
		resolved.Reason = upstreamReason(err)
		if resolved.State != StateUnknown {
			resolved.ResolvedAt = now
		}
		if storeErr := s.store.ResolveOperation(ctx, resolved); storeErr != nil {
			return FundsOutcome{}, fmt.Errorf("上游失败且台账写入失败: %w", storeErr)
		}
		return FundsOutcome{
			OperationKey: op.IdempotencyKey,
			Account:      acct.ID,
			State:        resolved.State,
		}, err
	}

	resolved := op
	resolved.State = StateSucceeded
	resolved.ResolvedAt = now
	if err := s.store.ResolveOperation(ctx, resolved); err != nil {
		return FundsOutcome{}, fmt.Errorf("台账写入: %w", err)
	}

	// 余额变了，顺手刷新投影，免得管理端显示一个刚被自己改过的旧值。
	s.refreshCard(ctx, acct, req.CardID)

	return FundsOutcome{
		OperationKey: op.IdempotencyKey,
		Account:      acct.ID,
		State:        StateSucceeded,
		TxID:         res.TxID,
		CardBalance:  res.CardBalance,
	}, nil
}

// FreezeCard 冻结一张卡。
func (s *Service) FreezeCard(ctx context.Context, account, idempotencyKey, cardID string) error {
	acct, err := s.account(account)
	if err != nil {
		return err
	}
	return s.switchOperation(ctx, acct, OpFreeze, idempotencyKey, cardID, acct.Client.FreezeCard)
}

// UnfreezeCard 解冻一张卡。
func (s *Service) UnfreezeCard(ctx context.Context, account, idempotencyKey, cardID string) error {
	acct, err := s.account(account)
	if err != nil {
		return err
	}
	return s.switchOperation(ctx, acct, OpUnfreeze, idempotencyKey, cardID, acct.Client.UnfreezeCard)
}

func (s *Service) switchOperation(
	ctx context.Context,
	acct Account,
	kind, idempotencyKey, cardID string,
	call func(context.Context, string) error,
) error {
	now := s.now()
	op := Operation{
		IdempotencyKey: idempotencyKey,
		Account:        acct.ID,
		Kind:           kind,
		State:          StatePending,
		CardID:         cardID,
		StartedAt:      now,
	}

	stored, existed, err := s.store.BeginOperation(ctx, op)
	if err != nil {
		return fmt.Errorf("落台账: %w", err)
	}
	if existed && stored.State == StateSucceeded {
		return nil
	}

	if err := call(ctx, cardID); err != nil {
		resolved := op
		resolved.State = stateForUpstreamError(err, kind)
		resolved.Reason = upstreamReason(err)
		resolved.ResolvedAt = now
		if storeErr := s.store.ResolveOperation(ctx, resolved); storeErr != nil {
			return fmt.Errorf("上游失败且台账写入失败: %w", storeErr)
		}
		return err
	}

	resolved := op
	resolved.State = StateSucceeded
	resolved.ResolvedAt = now
	if err := s.store.ResolveOperation(ctx, resolved); err != nil {
		return fmt.Errorf("台账写入: %w", err)
	}

	s.refreshCard(ctx, acct, cardID)
	return nil
}

// refreshCard 拉一次卡状态并回写投影。
//
// 失败不算错误：投影落后一轮由同步作业兜底，而让一次成功的资金操作
// 因为「刷新失败」而报错，会诱使调用方重试——重试才是真的危险。
func (s *Service) refreshCard(ctx context.Context, acct Account, cardID string) {
	card, err := acct.Client.CardStatus(ctx, cardID)
	if err != nil {
		return
	}
	_ = s.store.UpsertCard(ctx, acct.ID, card, CardAttribution{})
}

// naturallyIdempotent 标记那些重复执行不产生新效果的操作。
//
// 冻结、解冻在上游是幂等的：重复冻结不会冻两次。因此它们的超时可以安全
// 重试，落 failed 而不是 unknown——把天然幂等的操作也锁进不确定态，
// 只会让卡在出事时冻不上，而那正是最需要它能冻上的时刻。
func naturallyIdempotent(kind string) bool {
	return kind == OpFreeze || kind == OpUnfreeze
}

// stateForUpstreamError 把连接器错误分类映射成台账状态。
//
// 判据只有一条：**这次请求有没有可能已经在上游生效了**。
//
//	确定没生效 → failed，允许重试
//	可能生效了 → unknown，禁止重试，交给对账
//
// 把「可能已经执行」错判成「确定没执行」，就是允许了一次可能重复扣钱的重试。
// 所以默认分支落在 unknown 一侧——不认识的错误按最坏情况处理。
func stateForUpstreamError(err error, kind string) OperationState {
	// 天然幂等的操作不进不确定态：重复执行不产生新效果，重试是安全的。
	if naturallyIdempotent(kind) {
		return StateFailed
	}
	switch connector.KindOf(err) {
	case connector.KindRejected,
		connector.KindAuth,
		// IP 不在上游白名单：请求被网关挡在业务处理之前，与 auth 同一档。
		connector.KindIPNotAllowed,
		connector.KindRateLimited,
		connector.KindForbiddenTarget,
		connector.KindMethodNotAllowed,
		connector.KindNotSupported:
		// 这几类都在上游真正处理业务之前就被挡下了：
		// 认证不过、被限流、请求根本没发出去。钱确定没花。
		return StateFailed
	default:
		// unavailable（超时、网络不可达、5xx）与 bad_response 都可能是
		// 「上游已经开了卡，只是回复没到」。
		return StateUnknown
	}
}

// upstreamReason 是进台账的失败原因。
//
// 只记错误**分类**，不记上游原文：台账会被管理端展示给运营，
// 而 ADR-004 的纪律是供应商原文只进服务端日志。分类足够回答
// 「这笔为什么卡住」，细节去日志里按 action_run_id 查。
func upstreamReason(err error) string {
	return string(connector.KindOf(err))
}
