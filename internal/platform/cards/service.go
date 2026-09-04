package cards

import (
	"context"
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// Store 是领域层需要的持久化能力。
//
// 定成接口而不是直接依赖具体实现：本包最要紧的逻辑（限额、幂等、
// 不确定态）必须能在没有数据库的情况下被完整测到——那些分支在真实环境里
// 既难复现又昂贵。
type Store interface {
	// BeginOperation 落一条 pending 台账。
	// 同一幂等键已存在时返回既有记录，第二个返回值为 true。
	BeginOperation(ctx context.Context, op Operation) (Operation, bool, error)
	// ResolveOperation 更新一笔操作的终局状态。
	ResolveOperation(ctx context.Context, op Operation) error
	// SpentToday 返回某类操作今日累计金额（十进制文本，与限额同单位）。
	//
	// **必须把 unknown 状态的操作算进去**：那些可能真的花掉了，
	// 当没花过会让上限在最需要生效的时候失效。
	SpentToday(ctx context.Context, kind string, day time.Time) (string, error)
	// UpsertCard 落卡片投影。
	UpsertCard(ctx context.Context, card infini.Card, ownerRef string) error
}

// Service 是卡业务的领域服务。
type Service struct {
	client infini.CardClient
	store  Store
	limits Limits
	now    func() time.Time
}

// NewService 组装领域服务。
func NewService(client infini.CardClient, store Store, limits Limits, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{client: client, store: store, limits: limits, now: now}
}

// IssueRequest 是开卡入参。
type IssueRequest struct {
	// IdempotencyKey 由调用方给定，决定 alias，也决定重复调用的去重。
	IdempotencyKey string
	ProductID      int
	TopUpAmount    string
	TokenType      string
	UserEmail      string
	HolderName     string
	// OwnerRef 记录这张卡归谁用。现在是内部用途标签；
	// 以后开放给外部用户时，这一列就是「只能看自己的卡」的过滤依据。
	OwnerRef string
}

// IssueResult 是开卡结果。
type IssueResult struct {
	OperationKey  string
	State         OperationState
	CardID        string
	ApplicationID string
}

// IssueCard 开一张卡。
//
// 顺序是刻意的：限额校验在**落台账与调上游之前**——拦晚了钱就已经花出去了。
// 幂等去重在限额之后、调上游之前，所以重复调用既不会重复扣钱，
// 也不会因为「今天已花」被自己的第一次调用顶到超限。
func (s *Service) IssueCard(ctx context.Context, req IssueRequest) (IssueResult, error) {
	now := s.now()

	spent, err := s.store.SpentToday(ctx, OpIssue, now)
	if err != nil {
		return IssueResult{}, fmt.Errorf("读取今日累计: %w", err)
	}
	if err := s.limits.Check(req.TopUpAmount, spent); err != nil {
		return IssueResult{}, err
	}

	alias := AliasFor(req.IdempotencyKey)
	op := Operation{
		IdempotencyKey: req.IdempotencyKey,
		Kind:           OpIssue,
		State:          StatePending,
		Alias:          alias,
		AmountText:     req.TopUpAmount,
		TokenType:      req.TokenType,
		StartedAt:      now,
	}

	stored, existed, err := s.store.BeginOperation(ctx, op)
	if err != nil {
		return IssueResult{}, fmt.Errorf("落台账: %w", err)
	}
	if existed {
		// 已经有同幂等键的记录：绝不再调一次上游。
		// 无论它是成功、失败还是不确定，都原样返回——尤其是不确定态，
		// 「再试一次看看」正是可能开出第二张卡的那条路。
		return IssueResult{
			OperationKey: stored.IdempotencyKey,
			State:        stored.State,
			CardID:       stored.CardID,
		}, nil
	}

	app, err := s.client.ApplyCard(ctx, infini.ApplyCardRequest{
		ProductID:   req.ProductID,
		TopUpAmount: req.TopUpAmount,
		TokenType:   req.TokenType,
		UserEmail:   req.UserEmail,
		HolderName:  req.HolderName,
		Alias:       alias,
	})
	if err != nil {
		resolved := op
		resolved.State = stateForUpstreamError(err, OpIssue)
		resolved.ResolvedAt = now
		resolved.Reason = string(connector.KindOf(err))
		if resolved.State == StateUnknown {
			// 宽限期从 StartedAt 算起，对账靠 Alias——两者都必须留存，
			// 否则这笔操作再也收敛不了。
			resolved.ResolvedAt = time.Time{}
		}
		if storeErr := s.store.ResolveOperation(ctx, resolved); storeErr != nil {
			return IssueResult{}, fmt.Errorf("上游失败且台账写入失败: %w", storeErr)
		}
		return IssueResult{OperationKey: op.IdempotencyKey, State: resolved.State}, err
	}

	// 开卡是异步的：这里拿到的是申请单，卡要等作业轮询到 active。
	// 台账记成 succeeded 表示「请求确定被上游接受了」，不表示卡已可用。
	card, statusErr := s.client.CardStatus(ctx, app.ID)
	if statusErr == nil {
		if err := s.store.UpsertCard(ctx, card, req.OwnerRef); err != nil {
			return IssueResult{}, fmt.Errorf("落卡片投影: %w", err)
		}
	}

	resolved := op
	resolved.State = StateSucceeded
	resolved.CardID = app.ID
	resolved.ResolvedAt = now
	if err := s.store.ResolveOperation(ctx, resolved); err != nil {
		return IssueResult{}, fmt.Errorf("台账写入: %w", err)
	}

	return IssueResult{
		OperationKey:  op.IdempotencyKey,
		State:         StateSucceeded,
		CardID:        app.ID,
		ApplicationID: app.ID,
	}, nil
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
	// 天然幂等的操作（冻结/解冻）不进不确定态：重复执行不产生新效果，
	// 所以重试是安全的。把它们也锁住只会让卡在出事时冻不上。
	if naturallyIdempotent(kind) {
		return StateFailed
	}
	switch connector.KindOf(err) {
	case connector.KindRejected,
		connector.KindAuth,
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

// RevealCard 取一张卡的明文卡面数据。
//
// 不改任何状态，但它经领域层与 Action——理由是权限与审计：
// 「谁在何时看了哪张卡的明文」是本功能最该留痕的一条记录，
// 而普通读路径没有审计钩子。返回值不落库、不进日志。
func (s *Service) RevealCard(ctx context.Context, cardID string) (infini.RevealedCard, error) {
	return s.client.RevealCard(ctx, cardID)
}
