package cards

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
)

// SyncStore 是同步作业额外需要的读取能力。
//
// 与 Store 分开声明：写路径（Action）不需要遍历台账，同步作业不需要限额，
// 两者的依赖面本来就不一样。
type SyncStore interface {
	Store
	// UnresolvedOperations 返回尚未收敛的操作（pending 与 unknown）。
	//
	// 已经 succeeded/failed 的不该被反复对账——每轮都打一次上游既浪费配额，
	// 也提高触发限流的概率。
	UnresolvedOperations(ctx context.Context) ([]Operation, error)
	// TrackedCardIDs 返回投影里已有的卡 id，供状态与流水同步遍历。
	TrackedCardIDs(ctx context.Context) ([]string, error)
	// UpsertTransactions 落流水投影。
	UpsertTransactions(ctx context.Context, cardID string, txs []infini.CardTransaction) error
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
	client infini.CardClient
	store  SyncStore
	opts   SyncOptions
}

func NewSyncer(client infini.CardClient, store SyncStore, opts SyncOptions) *Syncer {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.UnknownGrace <= 0 {
		opts.UnknownGrace = 30 * time.Minute
	}
	return &Syncer{client: client, store: store, opts: opts}
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

		page, err := s.client.ListCards(ctx, infini.ListCardsQuery{Alias: op.Alias})
		if err != nil {
			// 读不到就下一轮再来，**不动台账**。
			errs = append(errs, fmt.Errorf("按 alias %s 查卡: %w", op.Alias, err))
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
				if err := s.store.UpsertCard(ctx, c, ""); err != nil {
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
	ids, err := s.store.TrackedCardIDs(ctx)
	if err != nil {
		return err
	}

	var errs []error
	for _, id := range ids {
		card, err := s.client.CardStatus(ctx, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("查卡 %s: %w", id, err))
			continue
		}
		if err := s.store.UpsertCard(ctx, card, ""); err != nil {
			errs = append(errs, fmt.Errorf("落卡片投影 %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Syncer) syncTransactions(ctx context.Context) error {
	ids, err := s.store.TrackedCardIDs(ctx)
	if err != nil {
		return err
	}

	var errs []error
	for _, id := range ids {
		page, err := s.client.CardTransactions(ctx, id, 1, transactionPageSize)
		if err != nil {
			errs = append(errs, fmt.Errorf("查流水 %s: %w", id, err))
			continue
		}
		if err := s.store.UpsertTransactions(ctx, id, page.Transactions); err != nil {
			errs = append(errs, fmt.Errorf("落流水 %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// transactionPageSize 保守取值：上游限流阈值文档未提及，
// 而流水同步的调用量与卡数成正比。
const transactionPageSize = 50
