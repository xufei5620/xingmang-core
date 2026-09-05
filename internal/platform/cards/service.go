package cards

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
)

// ErrUnknownAccount：请求指定的账号没有配置。
//
// 单独成一类是因为它必须 fail closed：绝不能回落到「第一个账号」或
// 「默认账号」——那会把钱花到调用方没打算用的账号上，而且事后从台账里
// 看不出这是一次回落。
var ErrUnknownAccount = errors.New("cards: 未配置的账号")

// Account 是一个 Infini 账号的运行时。
//
// **账号是内部运营维度，不是面向客户的维度**（产品负责人 2026-09-04 拍板）：
// 它只体现在管理后端，以后开放外部用户时由平台按策略挑账号，外部调用方
// 既看不到也不传。面向客户的归属维度是投影表的 owner_ref，两者正交——
// 一个回答「这张卡的钱从哪个资金池出」，一个回答「这张卡归谁用」。
type Account struct {
	// ID 是平台侧的稳定标识（如 main / backup），进台账与投影表。
	// 不用上游的账号 id：那个是上游的东西，换供应商就没了。
	ID     string
	Client infini.CardClient
	// Limits 按账号各配一份。两个账号的资金是分开的，用一套全局上限
	// 会让「单日 500」变成两个账号抢同一个额度。
	Limits Limits
	// 提现额度**不在这里**：它存在库里，由管理后台的
	// cards.withdraw.limit.set Action 修改（产品负责人 2026-09-05 决定）。
	// 见 WithdrawStore.WithdrawLimitsFor。
}

// Store 是领域层需要的持久化能力。
//
// 定成接口而不是直接依赖具体实现：本包最要紧的逻辑（限额、幂等、
// 不确定态）必须能在没有数据库的情况下被完整测到——那些分支在真实环境里
// 既难复现又昂贵。
type Store interface {
	// BeginOperation 落一条 pending 台账。
	// 同一幂等键已存在时返回既有记录，第二个返回值为 true。
	//
	// 幂等键在**环境内全局唯一**，不按账号分：键标识的是「哪一笔业务
	// 操作」，允许同键在两个账号上各来一次等于让去重失效。
	BeginOperation(ctx context.Context, op Operation) (Operation, bool, error)
	// ResolveOperation 更新一笔操作的终局状态。
	ResolveOperation(ctx context.Context, op Operation) error
	// SpentToday 返回**某个账号**某类操作今日累计金额（十进制文本）。
	//
	// **必须把 unknown 状态的操作算进去**：那些可能真的花掉了，
	// 当没花过会让上限在最需要生效的时候失效。
	SpentToday(ctx context.Context, account, kind string, day time.Time) (string, error)
	// UpsertCard 落卡片投影，归属到指定账号。
	//
	// attribution 里的字段**只在非空时覆盖**：同步作业不知道这些（上游的卡片
	// 对象里没有），传空会把运营填的东西冲掉。
	UpsertCard(ctx context.Context, account string, card infini.Card, attribution CardAttribution) error
	// SetCardUsage 写卡片的业务用途登记（绑定账号、订阅服务、续费日期）。
	//
	// 放在 Store 而不是靠运行时类型断言取一个可选接口：断言失败是运行时
	// 才炸的错误，而「存储支不支持登记」本该在编译期就定下来。
	SetCardUsage(ctx context.Context, usage CardUsage) error
	// StoreCardSecrets 落卡面明文。
	//
	// 开卡成功后当场拉一次（卡通常已经是 active），不必等周期同步——
	// 运营开完卡看到一行没有卡号的记录，只能反复刷新。
	StoreCardSecrets(ctx context.Context, account, cardID string, revealed infini.RevealedCard) error
}

// CardAttribution 是只有平台自己知道的卡片归属信息。
//
// 上游的卡片对象里没有这两样：OwnerRef 是平台的用途标签，UserEmail 是开卡时
// 指定的企业成员邮箱（上游只回 user_id）。所以它们只能在开卡那一刻写入，
// 之后同步作业刷新状态时传空，由存储层保留原值。
//
// 成组传参而不是继续往 UpsertCard 后面加参数：再加一个就是四个位置参数，
// 调用点上分不清哪个是哪个。
type CardAttribution struct {
	OwnerRef string
	// UserEmail 供管理端的开卡表单做「选历史用过的成员」下拉——
	// 上游没有成员列表接口，可选项只能来自平台自己开过的卡。
	UserEmail string
	// IssueFee / IssuePayAmount 来自 apply 响应的 total_fee / total_pay_amount。
	//
	// 保持十进制文本：单位是申请时的代币（USDT/USDC），真实标度未验证，
	// 与操作台账的 AmountText 同一条纪律。实测开卡费是 1 USD 固定
	// （不是文档示例暗示的按比例），$1 的卡成本 100%——不落库，
	// 成本核算就永远少一块。
	IssueFee       string
	IssuePayAmount string
	// IssueFeeToken 是开卡费的计价代币（USDT / USDC），来自开卡请求。
	//
	// 没有它，一列开卡费就只是一串没有单位的数字——而成本汇总把它加到
	// USD 的消费上时，看起来完全正常。空 = 单位未记录（老数据），
	// **不回填**：1 USDT ≈ 1 USD 是汇率假设不是事实。
	IssueFeeToken string
}

// Service 是卡业务的领域服务。
type Service struct {
	accounts map[string]Account
	// order 保留配置顺序，让遍历与错误信息稳定可复现。
	order []string
	store Store
	now   func() time.Time
}

// NewService 组装领域服务。
func NewService(accounts []Account, store Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	s := &Service{
		accounts: make(map[string]Account, len(accounts)),
		store:    store,
		now:      now,
	}
	for _, a := range accounts {
		s.accounts[a.ID] = a
		s.order = append(s.order, a.ID)
	}
	return s
}

// AccountIDs 返回已配置的账号，供 Action 的枚举校验与管理端下拉使用。
func (s *Service) AccountIDs() []string {
	out := make([]string, len(s.order))
	copy(out, s.order)
	return out
}

// account 取出账号运行时；未配置一律报错，不回落。
func (s *Service) account(id string) (Account, error) {
	a, ok := s.accounts[id]
	if !ok {
		return Account{}, fmt.Errorf("%w: %q（已配置：%v）", ErrUnknownAccount, id, s.order)
	}
	return a, nil
}

// IssueRequest 是开卡入参。
type IssueRequest struct {
	// Account 指定用哪个 Infini 账号。**必填**：双账号下没有安全的默认值，
	// 回落到「第一个」会把钱花到调用方没打算用的账号上。
	//
	// 这是管理后端的参数。以后若开放外部用户，选账号的逻辑在平台侧，
	// 不暴露给外部调用方（账号是内部维度）。
	Account string
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
	Account       string
	State         OperationState
	CardID        string
	ApplicationID string
}

// IssueCard 开一张卡。
//
// 顺序是刻意的：账号解析与限额校验都在**落台账与调上游之前**——
// 拦晚了钱就已经花出去了。幂等去重在限额之后、调上游之前，所以重复调用
// 既不会重复扣钱，也不会因为「今天已花」被自己的第一次调用顶到超限。
func (s *Service) IssueCard(ctx context.Context, req IssueRequest) (IssueResult, error) {
	acct, err := s.account(req.Account)
	if err != nil {
		return IssueResult{}, err
	}
	now := s.now()

	spent, err := s.store.SpentToday(ctx, acct.ID, OpIssue, now)
	if err != nil {
		return IssueResult{}, fmt.Errorf("读取今日累计: %w", err)
	}
	if err := acct.Limits.Check(req.TopUpAmount, spent); err != nil {
		return IssueResult{}, err
	}

	// 用途标签进 alias 的可读前缀：上游把 card_alias 当卡片名称显示。
	alias := AliasFor(req.IdempotencyKey, req.OwnerRef)
	op := Operation{
		IdempotencyKey: req.IdempotencyKey,
		Account:        acct.ID,
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
			Account:      stored.Account,
			State:        stored.State,
			CardID:       stored.CardID,
		}, nil
	}

	app, err := acct.Client.ApplyCard(ctx, infini.ApplyCardRequest{
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
		resolved.Reason = upstreamReason(err)
		if resolved.State == StateUnknown {
			// 宽限期从 StartedAt 算起，对账靠 Alias——两者都必须留存，
			// 否则这笔操作再也收敛不了。
			resolved.ResolvedAt = time.Time{}
		}
		if storeErr := s.store.ResolveOperation(ctx, resolved); storeErr != nil {
			return IssueResult{}, fmt.Errorf("上游失败且台账写入失败: %w", storeErr)
		}
		return IssueResult{
			OperationKey: op.IdempotencyKey,
			Account:      acct.ID,
			State:        resolved.State,
		}, err
	}

	// 开卡是异步的：这里拿到的是申请单，卡要等作业轮询到 active。
	// 台账记成 succeeded 表示「请求确定被上游接受了」，不表示卡已可用。
	if card, statusErr := acct.Client.CardStatus(ctx, app.ID); statusErr == nil {
		attribution := CardAttribution{
			OwnerRef:       req.OwnerRef,
			UserEmail:      req.UserEmail,
			IssueFee:       app.TotalFee,
			IssuePayAmount: app.TotalPayAmount,
			IssueFeeToken:  req.TokenType,
		}
		if err := s.store.UpsertCard(ctx, acct.ID, card, attribution); err != nil {
			return IssueResult{}, fmt.Errorf("落卡片投影: %w", err)
		}
		// 卡已经可用就当场把明文拉下来。失败**不算错误**：卡已经开出来、
		// 钱已经花了，此时因为「明文没拉到」返回错误会诱使调用方重试，
		// 而重试开卡是这套设计里最危险的动作。明文有周期同步兜底。
		if card.Status == cardStatusActive {
			if revealed, revealErr := acct.Client.RevealCard(ctx, app.ID); revealErr == nil {
				_ = s.store.StoreCardSecrets(ctx, acct.ID, app.ID, revealed)
			}
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
		Account:       acct.ID,
		State:         StateSucceeded,
		CardID:        app.ID,
		ApplicationID: app.ID,
	}, nil
}

// RevealCard 取一张卡的明文卡面数据。
//
// 不改任何状态，但它经领域层与 Action——理由是权限与审计：
// 「谁在何时看了哪张卡的明文」是本功能最该留痕的一条记录，
// 而普通读路径没有审计钩子。返回值不落库、不进日志。
func (s *Service) RevealCard(ctx context.Context, account, cardID string) (infini.RevealedCard, error) {
	acct, err := s.account(account)
	if err != nil {
		return infini.RevealedCard{}, err
	}
	return acct.Client.RevealCard(ctx, cardID)
}

// AccountBalances 读某个账号的资金池可用余额。
//
// **不落投影**：余额变化频繁，而且只在人要看的时候才重要；给它建一张
// 投影表就要再建一个同步作业，而那个作业每轮都在打一个没人看的接口。
// 未配置的账号一律报错，不回落——与其余账号操作同一条纪律。
func (s *Service) AccountBalances(ctx context.Context, account string) (infini.AccountBalances, error) {
	acct, err := s.account(account)
	if err != nil {
		return infini.AccountBalances{}, err
	}
	return acct.Client.AccountBalances(ctx)
}
