package cards

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
)

// CardRef 是「哪个账号的哪张卡」。
//
// 卡 id 只在自己账号内有意义：两个账号的卡各自独立，拿一个账号的 id 去
// 另一个账号查是没有意义的调用（好一点的情况是查不到，坏一点的情况是
// 撞上同名 id 拿回别人的卡）。
type CardRef struct {
	Account string
	CardID  string
}

// SyncStore 是同步作业额外需要的读取能力。
//
// 与 Store 分开声明：写路径（Action）不需要遍历台账，同步作业不需要限额，
// 两者的依赖面本来就不一样。
type SyncStore interface {
	Store
	// UnresolvedOperations 返回尚未收敛的操作（pending 与 unknown），
	// 每条都带账号。
	//
	// 已经 succeeded/failed 的不该被反复对账——每轮都打一次上游既浪费配额，
	// 也提高触发限流的概率。
	UnresolvedOperations(ctx context.Context) ([]Operation, error)
	// TrackedCards 返回投影里已有的卡，供状态与流水同步遍历。
	TrackedCards(ctx context.Context) ([]CardRef, error)
	// UpsertTransactions 落流水投影。
	UpsertTransactions(ctx context.Context, account, cardID string, txs []infini.CardTransaction) error
}

// SyncOptions 是同步作业的可调参数。
type SyncOptions struct {
	// UnknownGrace 是不确定态的宽限期：期内查不到就继续等，
	// 超期仍无结论才亮红条要人工。
	UnknownGrace time.Duration
	// SyncTransactions 为真时同时同步流水。默认关闭，
	// 因为流水同步的调用量与卡数成正比，而上游限流阈值未知。
	SyncTransactions bool
	Now              func() time.Time
}

// Syncer 承担三件事：把异步开卡轮询到终态、把不确定态对账收敛、
// 把卡状态与流水刷进投影。
//
// 逻辑放在领域层而不是 River Worker 里，是为了能用替身完整测到——
// 不确定态收敛这类分支在真实环境里既难复现又昂贵。
type Syncer struct {
	accounts map[string]Account
	order    []string
	store    SyncStore
	opts     SyncOptions
}

func NewSyncer(accounts []Account, store SyncStore, opts SyncOptions) *Syncer {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.UnknownGrace <= 0 {
		opts.UnknownGrace = 30 * time.Minute
	}
	s := &Syncer{
		accounts: make(map[string]Account, len(accounts)),
		store:    store,
		opts:     opts,
	}
	for _, a := range accounts {
		s.accounts[a.ID] = a
		s.order = append(s.order, a.ID)
	}
	return s
}

// RunOnce 跑一轮同步。
//
// 错误处理的纪律：上游读不到时**返回错误让作业重试，但绝不动台账状态**。
// 把「我查不到」当成「它没发生」，正是可能开出第二张卡的那条路。
func (s *Syncer) RunOnce(ctx context.Context) error {
	var errs []error

	if err := s.reconcileOperations(ctx); err != nil {
		errs = append(errs, fmt.Errorf("对账未收敛的操作: %w", err))
	}
	if err := s.refreshTrackedCards(ctx); err != nil {
		errs = append(errs, fmt.Errorf("刷新卡状态: %w", err))
	}
	if s.opts.SyncTransactions {
		if err := s.syncTransactions(ctx); err != nil {
			errs = append(errs, fmt.Errorf("同步流水: %w", err))
		}
	}
	return errors.Join(errs...)
}

// reconcileOperations 处理未收敛的操作。
func (s *Syncer) reconcileOperations(ctx context.Context) error {
	ops, err := s.store.UnresolvedOperations(ctx)
	if err != nil {
		return err
	}

	now := s.opts.Now()
	var errs []error
	for _, op := range ops {
		// 只有开卡需要 alias 对账：其余操作要么天然幂等，要么它们的
		// 不确定态由人工按台账处理（上游没有可用来精确匹配的信标）。
		if op.Kind != OpIssue || op.State != StateUnknown {
			continue
		}

		acct, ok := s.accounts[op.Account]
		if !ok {
			// 台账里有一笔操作，但它的账号已经不在配置里了。
			// **不能当成「查不到卡」处理**——那会让它超宽限期后被判成
			// 需人工，而真正的原因是配置被改掉了，两者的处置完全不同。
			errs = append(errs, fmt.Errorf(
				"操作 %s 的账号 %q 未配置，无法对账（配置被改动过？）",
				op.IdempotencyKey, op.Account))
			continue
		}

		// **只在这笔操作自己的账号里查**：跨账号找到一张 alias 相同的卡
		// 就认下来，等于把别的账号的卡记到这笔操作头上。
		page, err := acct.Client.ListCards(ctx, infini.ListCardsQuery{Alias: op.Alias})
		if err != nil {
			// 读不到就下一轮再来，**不动台账**。
			errs = append(errs, fmt.Errorf("账号 %s 按 alias %s 查卡: %w",
				acct.ID, op.Alias, err))
			continue
		}

		decision := ReconcileIssue(op, page.Cards, now, s.opts.UnknownGrace)
		if err := s.applyDecision(ctx, op, decision, page.Cards); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// applyDecision 把对账结论写回台账。
func (s *Syncer) applyDecision(ctx context.Context, op Operation, d ReconcileDecision, found []infini.Card) error {
	// 结论没变化就别写：每轮重写一遍 updated_at 会让「这条记录什么时候
	// 真的变过」在排查时失去意义。
	if d.NewState == op.State && d.NeedsHumanReview == op.NeedsHumanReview && d.CardID == "" {
		return nil
	}

	resolved := op
	resolved.State = d.NewState
	resolved.NeedsHumanReview = d.NeedsHumanReview
	resolved.Reason = d.Reason
	if d.CardID != "" {
		resolved.CardID = d.CardID
		resolved.ResolvedAt = s.opts.Now()
	}

	if err := s.store.ResolveOperation(ctx, resolved); err != nil {
		return fmt.Errorf("写回台账 %s: %w", op.IdempotencyKey, err)
	}

	// 收敛成功时把那张卡落进投影——它此前从未被平台记录过。
	if d.CardID != "" {
		for _, c := range found {
			if c.ID == d.CardID {
				if err := s.store.UpsertCard(ctx, op.Account, c, ""); err != nil {
					return fmt.Errorf("落卡片投影 %s: %w", c.ID, err)
				}
				break
			}
		}
	}
	return nil
}

// refreshTrackedCards 刷新投影里每张卡的状态。
//
// 这也是异步开卡的轮询路径：申请单出来时卡是 init，靠这里推进到 active。
func (s *Syncer) refreshTrackedCards(ctx context.Context) error {
	refs, err := s.store.TrackedCards(ctx)
	if err != nil {
		return err
	}

	var errs []error
	for _, ref := range refs {
		acct, ok := s.accounts[ref.Account]
		if !ok {
			errs = append(errs, fmt.Errorf("卡 %s 的账号 %q 未配置", ref.CardID, ref.Account))
			continue
		}
		card, err := acct.Client.CardStatus(ctx, ref.CardID)
		if err != nil {
			errs = append(errs, fmt.Errorf("账号 %s 查卡 %s: %w", ref.Account, ref.CardID, err))
			continue
		}
		if err := s.store.UpsertCard(ctx, ref.Account, card, ""); err != nil {
			errs = append(errs, fmt.Errorf("落卡片投影 %s: %w", ref.CardID, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Syncer) syncTransactions(ctx context.Context) error {
	refs, err := s.store.TrackedCards(ctx)
	if err != nil {
		return err
	}

	var errs []error
	for _, ref := range refs {
		acct, ok := s.accounts[ref.Account]
		if !ok {
			continue // 上面 refreshTrackedCards 已经报过这个账号缺配置
		}
		page, err := acct.Client.CardTransactions(ctx, ref.CardID, 1, transactionPageSize)
		if err != nil {
			errs = append(errs, fmt.Errorf("账号 %s 查流水 %s: %w", ref.Account, ref.CardID, err))
			continue
		}
		if err := s.store.UpsertTransactions(ctx, ref.Account, ref.CardID, page.Transactions); err != nil {
			errs = append(errs, fmt.Errorf("落流水 %s: %w", ref.CardID, err))
		}
	}
	return errors.Join(errs...)
}

// transactionPageSize 保守取值：上游限流阈值文档未提及，
// 而流水同步的调用量与卡数成正比。
const transactionPageSize = 50
