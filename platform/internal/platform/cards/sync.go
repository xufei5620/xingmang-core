package cards

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
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
	// CardStatusOf 返回投影里记着的状态，供批量刷新判断「这张卡变了没有」。
	CardStatusOf(ctx context.Context, account, cardID string) (string, error)
	// TrackedCards 返回投影里已有的卡，供状态与流水同步遍历。
	TrackedCards(ctx context.Context) ([]CardRef, error)
	// UpsertTransactions 落流水投影。
	UpsertTransactions(ctx context.Context, account, cardID string, txs []infini.CardTransaction) error
	// CardsMissingSecrets 返回**已激活但还没拉到明文卡面**的卡。
	//
	// 只返回没拉过的：卡号在卡的生命周期内不变，每轮都拉等于每个周期
	// N 次 reveal 调用——上游限流阈值未知，而 reveal 还是个要 IP 白名单的
	// 受限权限。未激活的卡也不返回：那时上游还没把卡建出来，拉了也是白拉。
	CardsMissingSecrets(ctx context.Context) ([]CardRef, error)
	// StoreCardSecrets 落卡面明文。
	StoreCardSecrets(ctx context.Context, account, cardID string, revealed infini.RevealedCard) error
}

// SyncOptions 是同步作业的可调参数。
type SyncOptions struct {
	// UnknownGrace 是不确定态的宽限期：期内查不到就继续等，
	// 超期仍无结论才亮红条要人工。
	UnknownGrace time.Duration
	// SyncTransactions 为真时同时同步流水。
	//
	// 调用方（worker）默认给 true——原先默认关闭的理由是「调用量与卡数
	// 成正比，而上游限流阈值未知」，那个未知已在 2026-09-05 实测掉：
	// 600 次/分钟/密钥，250 张卡 5 分钟一轮只占 8%。
	// 这个字段本身仍不设默认值：领域层不该替调用方决定花多少调用。
	SyncTransactions bool
	// Withdrawals 为非 nil 时，每轮把未收敛的提现推进到终态。
	//
	// 上游受理后是异步上链的，提现响应只回一个受理确认；没有这一步，
	// 页面上的状态会永远停在「已提交」。做成可选是因为提现本身可选
	// （账号可以不配额度），而「没配提现就让整轮同步报错」会把卡片同步
	// 也一起拖停。
	Withdrawals *WithdrawService
	Now         func() time.Time
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

// RunOnce 跑一轮同步，返回按账号/步骤拆开的结果。
//
// 错误处理的纪律：上游读不到时**绝不动台账状态**。把「我查不到」当成
// 「它没发生」，正是可能开出第二张卡的那条路。
//
// 返回的 error **只在整轮全砸了时才非 nil**（XM-CARD-VISIBILITY）。
// 此前是「六步里砸一步整轮就报错」，于是一个账号的批量查状态被上游拒，
// 会让另一个账号刚刚成功的发现与刷新一起被判失败、被 River 重试三次、
// 最后落一条 discarded——2026-09-08 的 24 小时里这样烧掉了 288 个作业，
// 而卡状态其实一直在被兜底路径正常刷新。部分成功的细节在 RoundResult 里，
// 由作业写进日志与 ops 观测，而不是靠一个二值的成败。
func (s *Syncer) RunOnce(ctx context.Context) (RoundResult, error) {
	var res RoundResult

	// 暂停开关**每轮只读一次**：读六次的话它可以在一轮中间被翻转，
	// 留下「前三步做了、后三步跳过」的结果，那种结果最难解释。
	paused, err := s.store.PausedAccounts(ctx)
	if err != nil {
		// 读不到就让整轮失败，**不回落到「当作没暂停」**。回落会在一次
		// 数据库抖动里重新把请求打向一个被人为停掉的上游，而且这件事在
		// 任何地方都看不见。这里是判断 fail closed，不是同步 fail closed。
		wrapped := fmt.Errorf("读取账号同步开关: %w", err)
		res.fail("", "account_sync_pause", wrapped)
		return res, wrapped
	}

	var errs []error

	if err := s.reconcileOperations(ctx, paused, &res); err != nil {
		errs = append(errs, fmt.Errorf("对账未收敛的操作: %w", err))
	}
	// 发现排在刷新之前：这一轮新拉进来的卡，同一轮里就能拿到状态与明文，
	// 不用等下一个周期。
	if err := s.discoverCards(ctx, paused, &res); err != nil {
		errs = append(errs, fmt.Errorf("发现卡片: %w", err))
	}
	if err := s.refreshTrackedCards(ctx, paused, &res); err != nil {
		errs = append(errs, fmt.Errorf("刷新卡状态: %w", err))
	}
	// 拉明文排在刷新卡状态**之后**：刚推进到 active 的卡在同一轮里就能被拉到，
	// 不用等下一个周期。
	if err := s.fetchMissingSecrets(ctx, paused, &res); err != nil {
		errs = append(errs, fmt.Errorf("拉取卡面明文: %w", err))
	}
	if s.opts.SyncTransactions {
		if err := s.syncTransactions(ctx, paused, &res); err != nil {
			errs = append(errs, fmt.Errorf("同步流水: %w", err))
		}
	}
	if err := s.advanceWithdrawals(ctx, paused, &res); err != nil {
		errs = append(errs, fmt.Errorf("推进提现: %w", err))
	}

	joined := errors.Join(errs...)
	if joined == nil || res.AnySucceeded() {
		// 部分成功不是作业失败。它仍然全部记在 RoundResult 里，
		// 由 cards.sync.failed 告警按「同一账号同一步骤连续 N 轮」把持续
		// 失败挑出来——那条告警不是可选的后续，它是这次改动的另一半：
		// 少了它，这里就把一个吵闹但真实的信号换成了一个安静的盲区。
		return res, nil
	}
	return res, joined
}

// pausedFor 返回该账号的暂停记录；未暂停时第二个返回值为 false。
func pausedFor(paused map[string]AccountSyncPause, account string) (AccountSyncPause, bool) {
	p, ok := paused[account]
	if !ok || !p.Paused {
		return AccountSyncPause{}, false
	}
	return p, true
}

// advanceWithdrawals 把未收敛的提现查一遍上游，推进到终态。
//
// 未配提现时整步跳过（返回 nil），不影响其余同步——见 SyncOptions.Withdrawals。
//
// 逐笔的失败只收集不中断：一笔查不动不该让后面几笔也停在 pending，
// 而「钱到底转出去没有」是最不该被一次网络抖动推迟的那个答案。
func (s *Syncer) advanceWithdrawals(ctx context.Context, paused map[string]AccountSyncPause, res *RoundResult) error {
	svc := s.opts.Withdrawals
	if svc == nil {
		return nil
	}
	open, err := svc.OpenWithdrawals(ctx)
	if err != nil {
		res.fail("", StepWithdrawals, err)
		return err
	}
	tally := newStepTally(StepWithdrawals)
	defer tally.flush(res)

	var errs []error
	for _, w := range open {
		if p, ok := pausedFor(paused, w.Account); ok {
			tally.markSkip(w.Account, pauseSkipReason(p))
			continue
		}
		if err := svc.RefreshWithdraw(ctx, w.Account, w.RequestID); err != nil {
			wrapped := fmt.Errorf("提现 %s: %w", w.RequestID, err)
			tally.markErr(w.Account, wrapped)
			errs = append(errs, wrapped)
			continue
		}
		tally.markOK(w.Account)
	}
	return errors.Join(errs...)
}

// reconcileOperations 处理未收敛的操作。
func (s *Syncer) reconcileOperations(ctx context.Context, paused map[string]AccountSyncPause, res *RoundResult) error {
	ops, err := s.store.UnresolvedOperations(ctx)
	if err != nil {
		res.fail("", StepReconcile, err)
		return err
	}

	now := s.opts.Now()
	tally := newStepTally(StepReconcile)
	defer tally.flush(res)

	var errs []error
	for _, op := range ops {
		// 只有开卡需要 alias 对账：其余操作要么天然幂等，要么它们的
		// 不确定态由人工按台账处理（上游没有可用来精确匹配的信标）。
		if op.Kind != OpIssue || op.State != StateUnknown {
			continue
		}

		// 暂停的账号连对账都不做：对账要打上游，而暂停的语义正是
		// 「这一轮别碰这个账号的上游」。
		if p, ok := pausedFor(paused, op.Account); ok {
			tally.markSkip(op.Account, pauseSkipReason(p))
			continue
		}

		acct, ok := s.accounts[op.Account]
		if !ok {
			// 台账里有一笔操作，但它的账号已经不在配置里了。
			// **不能当成「查不到卡」处理**——那会让它超宽限期后被判成
			// 需人工，而真正的原因是配置被改掉了，两者的处置完全不同。
			unconfigured := fmt.Errorf(
				"操作 %s 的账号 %q 未配置，无法对账（配置被改动过？）",
				op.IdempotencyKey, op.Account)
			tally.markErr(op.Account, unconfigured)
			errs = append(errs, unconfigured)
			continue
		}

		// **只在这笔操作自己的账号里查**：跨账号找到一张 alias 相同的卡
		// 就认下来，等于把别的账号的卡记到这笔操作头上。
		page, err := acct.Client.ListCards(ctx, infini.ListCardsQuery{Alias: op.Alias})
		if err != nil {
			// 读不到就下一轮再来，**不动台账**。
			wrapped := fmt.Errorf("账号 %s 按 alias %s 查卡: %w",
				acct.ID, op.Alias, err)
			tally.markErr(op.Account, wrapped)
			errs = append(errs, wrapped)
			continue
		}

		decision := ReconcileIssue(op, page.Cards, now, s.opts.UnknownGrace)
		if err := s.applyDecision(ctx, op, decision, page.Cards); err != nil {
			tally.markErr(op.Account, err)
			errs = append(errs, err)
			continue
		}
		tally.markOK(op.Account)
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
				if err := s.store.UpsertCard(ctx, op.Account, c, CardAttribution{}); err != nil {
					return fmt.Errorf("落卡片投影 %s: %w", c.ID, err)
				}
				break
			}
		}
	}
	return nil
}

// discoveryPageSize 是发现遍历一次拉多少张。
//
// 取 100 而不是更大：上游的 page_size 上限没有文档，而 100 已经让常见规模
// （几十张卡）一次拉完。真超了会翻页，多几次调用而已——限流预算是
// 600/分钟/密钥，发现每 5 分钟才跑一轮。
const discoveryPageSize = 100

// discoveryMaxPages 是翻页的硬上限。
//
// 存在的理由是**防呆而不是防大**：如果上游的分页字段哪天变了语义
// （比如 total_pages 恒为 1、或 page 参数被忽略），没有上限的循环会一直
// 拉同一页直到把限流预算烧光，而那种故障从日志上看只是「同步慢」。
const discoveryMaxPages = 50

// discoverCards 把上游有、而投影里没有的卡拉进来。
//
// **这个遍历原本不存在**，于是在上游后台直接建的卡对平台完全不可见：
// 卡只有两条路径能进投影（我们自己开的、回调带来的）。2026-09-05 生产上
// 产品负责人有 11 张卡，页面只显示 2 张——一个叫「卡片管理」的页面
// 只管着 18% 的卡。
//
// 只对**没见过的**卡写投影，已知的交给 refreshTrackedCards：那条路径带着
// 批量状态查询与明文补拉，这里重复写一遍只会把归属信息（用途、绑定账号）
// 覆盖成空。
func (s *Syncer) discoverCards(ctx context.Context, paused map[string]AccountSyncPause, res *RoundResult) error {
	known, err := s.store.TrackedCards(ctx)
	if err != nil {
		res.fail("", StepDiscover, err)
		return err
	}
	seen := make(map[string]bool, len(known))
	for _, ref := range known {
		seen[ref.Account+"/"+ref.CardID] = true
	}

	tally := newStepTally(StepDiscover)
	defer tally.flush(res)

	var errs []error
	for _, id := range s.order {
		if p, ok := pausedFor(paused, id); ok {
			tally.markSkip(id, pauseSkipReason(p))
			continue
		}
		acct := s.accounts[id]
		for page := 1; page <= discoveryMaxPages; page++ {
			listed, err := acct.Client.ListCards(ctx, infini.ListCardsQuery{
				Page: page, PageSize: discoveryPageSize,
			})
			if err != nil {
				wrapped := fmt.Errorf("账号 %s 列卡（第 %d 页）: %w", id, page, err)
				tally.markErr(id, wrapped)
				errs = append(errs, wrapped)
				break
			}
			tally.markOK(id)
			for _, c := range listed.Cards {
				if seen[id+"/"+c.ID] {
					continue
				}
				// 归属留空：这张卡是在上游建的，平台这边还没人给它登记用途。
				// 页面上会显示成「—」，而那正是实情。
				if err := s.store.UpsertCard(ctx, id, c, CardAttribution{}); err != nil {
					wrapped := fmt.Errorf("落新发现的卡 %s: %w", c.ID, err)
					tally.markErr(id, wrapped)
					errs = append(errs, wrapped)
					continue
				}
				seen[id+"/"+c.ID] = true
			}
			if len(listed.Cards) == 0 || (listed.TotalPages > 0 && page >= listed.TotalPages) {
				break
			}
		}
	}
	return errors.Join(errs...)
}

// refreshTrackedCards 刷新投影里每张卡的状态。
//
// 这也是异步开卡的轮询路径：申请单出来时卡是 init，靠这里推进到 active。
//
// 一次批量状态查询的卡数上限用 infini.BatchStatusMax，**不在这里再写一个
// 100**（XM-CARD-VISIBILITY）：此前这里有个 batchStatusChunk = 100，与连接器
// 的上限是同一个数却各写一份，改一处不会有任何测试发红——领域层漂到 150
// 之后每一批都会被连接器本地拒掉，而日志上看起来像上游出了问题。
// 上面的 discoveryPageSize 也是 100，但那是**另一个数**（列表翻页大小），
// 不要顺手合并。
func (s *Syncer) refreshTrackedCards(ctx context.Context, paused map[string]AccountSyncPause, res *RoundResult) error {
	refs, err := s.store.TrackedCards(ctx)
	if err != nil {
		res.fail("", StepBatchStatus, err)
		return err
	}

	// 按账号分组：批量接口是按凭据发的，两个账号的卡不能混进同一次调用。
	byAccount := make(map[string][]string, len(s.accounts))
	known := make(map[string]map[string]string, len(s.accounts))
	for _, ref := range refs {
		byAccount[ref.Account] = append(byAccount[ref.Account], ref.CardID)
	}

	// defer 是后进先出：先声明 fallback 的 defer，批量那条才会排在结果前面。
	fallbackTally := newStepTally(StepBatchStatusFallback)
	defer fallbackTally.flush(res)
	batchTally := newStepTally(StepBatchStatus)
	defer batchTally.flush(res)

	skipped := make(map[string]bool, len(s.accounts))

	var errs []error
	for _, account := range s.order {
		if p, ok := pausedFor(paused, account); ok {
			skipped[account] = true
			batchTally.markSkip(account, pauseSkipReason(p))
			continue
		}
		ids := byAccount[account]
		if len(ids) == 0 {
			continue
		}
		acct := s.accounts[account]
		statuses := make(map[string]string, len(ids))
		for start := 0; start < len(ids); start += infini.BatchStatusMax {
			end := start + infini.BatchStatusMax
			if end > len(ids) {
				end = len(ids)
			}
			got, err := acct.Client.BatchCardStatus(ctx, ids[start:end])
			if err != nil {
				// 一批失败不该让其余批次与其余账号一起报废。
				wrapped := fmt.Errorf("账号 %s 批量查状态: %w", account, err)
				batchTally.markErr(account, wrapped)
				errs = append(errs, wrapped)
				if !retryableKind(connector.KindOf(err)) {
					// 上游明确拒绝（或认证/IP 问题）时，**这个账号的后续批次
					// 一批都不再发**：再发 100 张不是一个不同的请求，
					// 它注定被同样地拒掉，白烧配额还在日志里多出几条看起来
					// 像上游故障的记录。只中断这个账号——保住既有的
					// 「一批失败不该让其余账号报废」。
					break
				}
				continue
			}
			for id, status := range got {
				statuses[id] = status
			}
		}
		known[account] = statuses
	}

	// 账号没配的卡单独报错：这类错误此前也报，保持不变。
	// 也记进结果——否则「整轮只有这一类错误」时，AllFailed 会因为它不在
	// 任何一条 StepOutcome 里而算出 false，把一次配置事故判成成功。
	for _, ref := range refs {
		if _, ok := s.accounts[ref.Account]; !ok {
			unconfigured := fmt.Errorf("卡 %s 的账号 %q 未配置", ref.CardID, ref.Account)
			batchTally.markErr(ref.Account, unconfigured)
			errs = append(errs, unconfigured)
		}
	}

	// 只有状态变了的卡才值得再取一次全量字段。
	//
	// 批量接口只回 card_id + status，余额、掩码、更新时间这些要单查。
	// 而绝大多数轮次里绝大多数卡什么都没变——对它们再单查一遍，就等于
	// 把批量省下的调用又花回去了。余额变化会伴随状态之外的事件（充值、
	// 消费），那条路由回调与资金操作各自触发定向刷新，不靠这一轮兜底。
	for _, ref := range refs {
		if skipped[ref.Account] {
			continue // 账号被暂停，上面已经记过一条 skipped
		}
		acct, ok := s.accounts[ref.Account]
		if !ok {
			continue // 上面已经报过
		}
		status, seen := known[ref.Account][ref.CardID]
		if !seen {
			// 批量结果里没有这张卡：可能那一批失败了，也可能上游不认识它。
			// 退回单查，让它自己报错——静默跳过会让一张卡永远不再刷新。
			//
			// 这条兜底**行为一个字都没改**，只是把它的成果记下来
			// （XM-CARD-VISIBILITY）：2026-09-08 那次风暴里它一直在正常工作，
			// 卡状态其实被刷新着，红的只是作业状态，而没有任何地方看得出来。
			// 「批量红了但兜底接住了」与「两条都断了」的处置完全不同——
			// 前者不该叫人起床。
			if err := s.refreshOneCard(ctx, acct, ref); err != nil {
				fallbackTally.markErr(ref.Account, err)
				errs = append(errs, err)
				continue
			}
			fallbackTally.markOK(ref.Account)
			fallbackTally.addRecovered(ref.Account, 1)
			continue
		}
		if current, err := s.store.CardStatusOf(ctx, ref.Account, ref.CardID); err == nil && current == status {
			continue
		}
		if err := s.refreshOneCard(ctx, acct, ref); err != nil {
			batchTally.markErr(ref.Account, err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// refreshOneCard 取一张卡的全量字段并回写投影。
func (s *Syncer) refreshOneCard(ctx context.Context, acct Account, ref CardRef) error {
	card, err := acct.Client.CardStatus(ctx, ref.CardID)
	if err != nil {
		return fmt.Errorf("账号 %s 查卡 %s: %w", ref.Account, ref.CardID, err)
	}
	if err := s.store.UpsertCard(ctx, ref.Account, card, CardAttribution{}); err != nil {
		return fmt.Errorf("落卡片投影 %s: %w", ref.CardID, err)
	}
	return nil
}

// fetchMissingSecrets 给还没拉过明文的活卡各调一次 reveal。
//
// 产品负责人 2026-09-04 决定卡面明文落库，而上游的列表接口只给 mask——
// 明文只能从 reveal 来。这一步刻意做成「一次性」：拉到就不再拉，
// 卡号在卡的生命周期内不变。
func (s *Syncer) fetchMissingSecrets(ctx context.Context, paused map[string]AccountSyncPause, res *RoundResult) error {
	refs, err := s.store.CardsMissingSecrets(ctx)
	if err != nil {
		res.fail("", StepFetchSecrets, err)
		return err
	}

	tally := newStepTally(StepFetchSecrets)
	defer tally.flush(res)

	var errs []error
	for _, ref := range refs {
		if p, ok := pausedFor(paused, ref.Account); ok {
			tally.markSkip(ref.Account, pauseSkipReason(p))
			continue
		}
		acct, ok := s.accounts[ref.Account]
		if !ok {
			continue // refreshTrackedCards 已经报过这个账号缺配置
		}
		revealed, err := acct.Client.RevealCard(ctx, ref.CardID)
		if err != nil {
			// 单张卡拉失败不该让整轮报废：卡状态刷新是独立的一件事，
			// 而下一轮还会再试这一张。
			wrapped := fmt.Errorf("账号 %s 拉卡 %s 的明文: %w", ref.Account, ref.CardID, err)
			tally.markErr(ref.Account, wrapped)
			errs = append(errs, wrapped)
			continue
		}
		if err := s.store.StoreCardSecrets(ctx, ref.Account, ref.CardID, revealed); err != nil {
			wrapped := fmt.Errorf("落卡面明文 %s: %w", ref.CardID, err)
			tally.markErr(ref.Account, wrapped)
			errs = append(errs, wrapped)
			continue
		}
		tally.markOK(ref.Account)
	}
	return errors.Join(errs...)
}

func (s *Syncer) syncTransactions(ctx context.Context, paused map[string]AccountSyncPause, res *RoundResult) error {
	refs, err := s.store.TrackedCards(ctx)
	if err != nil {
		res.fail("", StepTransactions, err)
		return err
	}

	tally := newStepTally(StepTransactions)
	defer tally.flush(res)

	var errs []error
	for _, ref := range refs {
		if p, ok := pausedFor(paused, ref.Account); ok {
			tally.markSkip(ref.Account, pauseSkipReason(p))
			continue
		}
		acct, ok := s.accounts[ref.Account]
		if !ok {
			continue // 上面 refreshTrackedCards 已经报过这个账号缺配置
		}
		page, err := acct.Client.CardTransactions(ctx, ref.CardID, 1, transactionPageSize)
		if err != nil {
			wrapped := fmt.Errorf("账号 %s 查流水 %s: %w", ref.Account, ref.CardID, err)
			tally.markErr(ref.Account, wrapped)
			errs = append(errs, wrapped)
			continue
		}
		if err := s.store.UpsertTransactions(ctx, ref.Account, ref.CardID, page.Transactions); err != nil {
			wrapped := fmt.Errorf("落流水 %s: %w", ref.CardID, err)
			tally.markErr(ref.Account, wrapped)
			errs = append(errs, wrapped)
			continue
		}
		tally.markOK(ref.Account)
	}
	return errors.Join(errs...)
}

// transactionPageSize 保守取值。
//
// 限流阈值现在已知（600/分钟/密钥，2026-09-05 实测），但这个数不由它决定：
// 它是**一次拉多少条**，与调用次数无关。取 50 是因为一张卡在一个同步周期
// 内产生超过 50 笔流水属于异常，而拉多了只是白白搬运数据。
const transactionPageSize = 50

// cardStatusActive 是"卡已可用"的上游取值。
//
// 只定义这一个，不定义一整套枚举：文档没有给出完整的状态列表（示例里出现过
// init / pending / active / pending_delete / deleted，但**冻结后变成什么从未
// 写明**）。凭示例拼一个"完整"枚举，会让第一个没见过的取值被静默归到
// 某个已知分类里，而那正是最需要被人看见的时刻。
const cardStatusActive = "active"

// RefreshCard 定向刷新一张卡：状态 → 明文卡面（若还没拉过）→ 流水。
//
// 供两条路径复用：
//
//	回调到达时（XM-CARD4）——把感知延迟从一个同步周期压到秒级；
//	开卡刚成功时——卡若已 active，当场就有卡号，不必等下一轮。
//
// 与周期同步共用同一套写投影的代码。**全系统只有一条写投影的路**，
// 是这个设计最要紧的性质：回调只决定「什么时候读」，不决定「写什么」。
//
// 流水只在 withTransactions 时拉：状态变更事件没必要顺带翻一遍流水，
// 而交易事件必须。
func (s *Syncer) RefreshCard(ctx context.Context, account, cardID string, withTransactions bool) error {
	acct, ok := s.accounts[account]
	if !ok {
		// 与领域层同一条纪律：未配置的账号一律报错，绝不回落到别的账号。
		return fmt.Errorf("%w: %q", ErrUnknownAccount, account)
	}

	card, err := acct.Client.CardStatus(ctx, cardID)
	if err != nil {
		return fmt.Errorf("读卡 %s 状态: %w", cardID, err)
	}
	// 归属信息传空：这条路径不知道这些，由存储层保留原值。
	if err := s.store.UpsertCard(ctx, account, card, CardAttribution{}); err != nil {
		return fmt.Errorf("落卡片投影 %s: %w", cardID, err)
	}

	// 卡刚变成 active 时顺手把明文拉下来。失败不让整次刷新报废：
	// 卡状态已经写进去了，明文还有周期同步兜底，而让「明文拉失败」
	// 把一次成功的状态刷新变成错误，会诱使上游重投——重投解决不了这个问题。
	// 只有 active 的卡才有明文可拉。**不写死"其余状态一定拉不到"**——
	// 文档没有给出完整的状态枚举，冻结后的取值至今未知（见契约的验证清单），
	// 所以这里只对确定能拉的那一个取值动作，其余交给周期同步的既有判据。
	if card.Status == cardStatusActive {
		if revealed, err := acct.Client.RevealCard(ctx, cardID); err == nil {
			_ = s.store.StoreCardSecrets(ctx, account, cardID, revealed)
		}
	}

	if !withTransactions {
		return nil
	}
	page, err := acct.Client.CardTransactions(ctx, cardID, 1, transactionPageSize)
	if err != nil {
		return fmt.Errorf("读卡 %s 流水: %w", cardID, err)
	}
	if err := s.store.UpsertTransactions(ctx, account, cardID, page.Transactions); err != nil {
		return fmt.Errorf("落流水 %s: %w", cardID, err)
	}
	return nil
}
