package finance

// 余额历史的写入与看板供数的读取（XM-0037d，设计稿 §2.3 + §7 + §8.5）。
//
// 第四个仓储类型。四者的分工：
//
//	Store             登记簿——怎么算成本（配置，随时可改）
//	ProfitStore       利润台账——那天算出了什么（事实，过去冻结）
//	SubscriptionStore 订阅付款——付了多少钱（凭证，登记后大部分冻结）
//	SummaryStore      余额与看板供数——**读多写少，且写的那一样不参与成本**
//
// 余额单独放在这里而不是并进 ProfitStore，正是因为最后那半句（§2.3）：
// 台账里的每一个数都会进毛利，余额一个都不会。两者混在一个类型里，
// 「这个方法写的东西算不算成本」就成了一件要靠记忆的事。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance/gen"
)

// SummaryStore 是余额历史与看板供数的仓储。
type SummaryStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
	now  func() time.Time
}

// NewSummaryStore 创建仓储。
//
// now 可注入固定时钟：可用天数的「余额过期没有」与「近 7 个完整业务日是哪几天」
// 都靠它，一个没法在测试里推进的时钟意味着那两条口径只能靠人肉在跨零点时验证。
func NewSummaryStore(pool *pgxpool.Pool, now func() time.Time) *SummaryStore {
	if now == nil {
		now = time.Now
	}
	return &SummaryStore{pool: pool, q: gen.New(pool), now: now}
}

func balanceFromRow(r gen.FinanceBalanceHistory) BalanceReading {
	return BalanceReading{
		ID:                r.ID,
		UpstreamAccountID: r.UpstreamAccountID,
		BalanceMinor:      r.BalanceMinor,
		Currency:          r.Currency,
		CapturedAt:        fromTS(r.CapturedAt),
		ObservedAt:        fromTS(r.ObservedAt),
		Source:            r.Source,
	}
}

// RecordBalance 落一条余额读数，按 §7 的「仅变化时落一条」。
//
// 两条路：
//
//	值变了（或从未有过）→ INSERT，开一段新游程
//	值没变             → UPDATE 最新那一行的 observed_at，返回 ErrBalanceUnchanged
//
// 第二条路返回一个错误而不是静默成功，是为了让调用方**数得清**：
// 「今天余额动过 3 次」与「今天确认了 288 次余额没动」是两件事，
// 合成一个「写入成功」之后，看板上就再也看不出余额是不是卡住了。
// 它**不是故障**——调用方计一个「已确认」并继续。
//
// 返回的 BalanceReading 在两条路上都是**库里那一行的当前状态**，
// 所以调用方拿到的 ObservedAt 总是最新的，不必自己拼。
func (s *SummaryStore) RecordBalance(
	ctx context.Context, in BalanceReading,
) (BalanceReading, error) {
	if in.ObservedAt.IsZero() {
		in.ObservedAt = s.now().UTC()
	}
	if err := in.Validate(); err != nil {
		return BalanceReading{}, err
	}

	latest, err := s.q.GetLatestBalance(ctx, in.UpstreamAccountID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// 第一条读数：直接开游程。
	case err != nil:
		return BalanceReading{}, fmt.Errorf("get latest balance: %w", err)
	default:
		previous := balanceFromRow(latest)
		if previous.SameValueAs(in) {
			touched, err := s.q.TouchBalance(ctx, gen.TouchBalanceParams{
				ID:         previous.ID,
				ObservedAt: pgtype.Timestamptz{Time: in.ObservedAt.UTC(), Valid: true},
				Source:     in.Source,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				// 并发的另一轮已经把观测时刻推到更晚了。那一轮的读数与本轮
				// 相同（值没变才走到这里），所以库里已经是对的——
				// 返回已有的那一行，仍然报「未变化」。
				return previous, ErrBalanceUnchanged
			}
			if err != nil {
				return BalanceReading{}, fmt.Errorf("touch balance: %w", err)
			}
			return balanceFromRow(touched), ErrBalanceUnchanged
		}
	}

	row, err := s.q.InsertBalance(ctx, gen.InsertBalanceParams{
		UpstreamAccountID: in.UpstreamAccountID,
		BalanceMinor:      in.BalanceMinor,
		Currency:          in.Currency,
		ObservedAt:        pgtype.Timestamptz{Time: in.ObservedAt.UTC(), Valid: true},
		Source:            in.Source,
	})
	if err != nil {
		return BalanceReading{}, fmt.Errorf("insert balance: %w", err)
	}
	return balanceFromRow(row), nil
}

// LatestBalance 取一个账号最新的那一行余额；没有则返回 ErrNotFound。
func (s *SummaryStore) LatestBalance(
	ctx context.Context, accountID uuid.UUID,
) (BalanceReading, error) {
	row, err := s.q.GetLatestBalance(ctx, accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return BalanceReading{}, fmt.Errorf("balance for %s: %w", accountID, ErrNotFound)
	}
	if err != nil {
		return BalanceReading{}, fmt.Errorf("get latest balance: %w", err)
	}
	return balanceFromRow(row), nil
}

// SummaryQuery 是两个看板端点共用的查询条件。
type SummaryQuery struct {
	Environment string
	// From / To 是业务日闭区间（含两端）。零值时由调用方补默认窗口。
	From time.Time
	To   time.Time
	// Thresholds 必须是已验证的 DB/provider 快照；零值会 fail closed。
	// 旧 HTTP 兼容处理器若需要默认值，会在进入 Store 前显式补齐。
	Thresholds RunwayThresholds
}

// ChannelSummaries 给出逐渠道（= 逐上游账号，§12.2）的收入 / 成本 / 毛利。
//
// **列出的是登记簿里的全部账号，不是台账里有行的那些**：一个今天还没入账的
// 渠道要显示成「今天没有数据」，而不是从列表里消失——消失会让人以为它被删了
// （宪法 12 条）。所以这里以登记簿为骨架，台账聚合往上贴。
func (s *SummaryStore) ChannelSummaries(
	ctx context.Context, q SummaryQuery,
) ([]ChannelSummary, error) {
	accounts, windows, tokenCounts, err := s.summaryParts(ctx, q)
	if err != nil {
		return nil, err
	}
	out := make([]ChannelSummary, 0, len(accounts))
	for _, account := range accounts {
		out = append(out, ChannelSummary{
			Account:    account,
			TokenCount: tokenCounts[account.ID],
			Window:     windows[account.ID],
		})
	}
	return out, nil
}

// UpstreamRunway 是一个上游账号的可用天数，**不含任何金额窗口**（XM-0049）。
//
// 单独一个瘦类型给告警规则用：它只关心「哪条上游快见底了」，
// 不关心今天赚了多少。让告警去拉整份 UpstreamSummary，等于每轮评估
// 多跑两条与判据无关的聚合查询，还把 alerts 包和收入/成本的形状绑在一起。
type UpstreamRunway struct {
	AccountID uuid.UUID
	// Name 是给人看的名字（`system_type · base_url`），进告警标题。
	Name         string
	SystemType   SystemType
	AccessMethod AccessMethod
	Runway       Runway
}

// UpstreamRunways 只算可用天数，跳过金额窗口（XM-0049）。
//
// 与 UpstreamSummaries **共用同一段计算**（都走下面的 runwayFor），
// 所以看板上那个天数与告警判据上的天数出自同一份代码——
// 两处各算一遍迟早在某个边界上分叉，而那时没人知道该信哪个。
func (s *SummaryStore) UpstreamRunways(
	ctx context.Context, environment string, thresholds RunwayThresholds,
) ([]UpstreamRunway, error) {
	if environment == "" {
		return nil, fmt.Errorf("environment: %w", ErrMissingField)
	}
	accountRows, err := s.q.ListUpstreamAccountsByEnvironment(ctx, environment)
	if err != nil {
		return nil, fmt.Errorf("list upstream accounts: %w", err)
	}
	accounts, err := accountsFromRows(accountRows)
	if err != nil {
		return nil, err
	}
	balances, recent, thresholds, now, err := s.runwayInputs(ctx, environment, thresholds)
	if err != nil {
		return nil, err
	}

	out := make([]UpstreamRunway, 0, len(accounts))
	for _, account := range accounts {
		balance := balancePtr(balances, account.ID)
		runway, err := runwayFor(account, balance, recent[account.ID], thresholds, now)
		if err != nil {
			return nil, err
		}
		out = append(out, UpstreamRunway{
			AccountID:    account.ID,
			Name:         AccountDisplayName(account),
			SystemType:   account.SystemType,
			AccessMethod: account.AccessMethod,
			Runway:       runway,
		})
	}
	return out, nil
}

// runwayInputs 取可用天数两侧的原料并补齐阈值与时钟。
func (s *SummaryStore) runwayInputs(
	ctx context.Context, environment string, thresholds RunwayThresholds,
) (map[uuid.UUID]BalanceReading, map[uuid.UUID]recentCostRow, RunwayThresholds, time.Time, error) {
	// Validate before touching the database: a malformed provider snapshot is a
	// configuration error, not a reason to spend a full balance/cost query round.
	if err := thresholds.Validate(); err != nil {
		return nil, nil, thresholds, time.Time{}, err
	}
	balances, err := s.latestBalances(ctx, environment)
	if err != nil {
		return nil, nil, thresholds, time.Time{}, err
	}
	recent, err := s.recentCost(ctx, environment)
	if err != nil {
		return nil, nil, thresholds, time.Time{}, err
	}
	return balances, recent, thresholds, s.now().UTC(), nil
}

func balancePtr(balances map[uuid.UUID]BalanceReading, id uuid.UUID) *BalanceReading {
	balance, ok := balances[id]
	if !ok {
		return nil
	}
	return &balance
}

// runwayFor 是**唯一**一处把账号 + 余额 + 近期消耗喂给 ComputeRunway 的地方。
func runwayFor(
	account UpstreamAccount, balance *BalanceReading,
	cost recentCostRow, thresholds RunwayThresholds, now time.Time,
) (Runway, error) {
	return ComputeRunway(RunwayInput{
		AccessMethod: account.AccessMethod,
		Balance:      balance,
		CostMinorSum: cost.sum,
		CoveredDays:  cost.coveredDays,
		CostCurrency: cost.currency,
		Now:          now,
		Thresholds:   thresholds,
	})
}

// AccountDisplayName 给出一个稳定的、给人看的渠道 / 上游名。
//
// 后端拼而不是各处各拼：这个名字会出现在告警标题、审计与看板三处，
// 三处各拼一遍迟早会有一处不一样，然后没人能确定说的是不是同一条渠道。
func AccountDisplayName(a UpstreamAccount) string {
	if a.BaseURL != "" {
		return string(a.SystemType) + " · " + a.BaseURL
	}
	return string(a.SystemType) + " · " + string(a.AccessMethod)
}

// UpstreamSummaries 给出逐上游的供给侧供数：余额、可用天数、充值成本率。
//
// 可用天数的两侧在这里合流：分子是 balance_history 的最新一行，
// 分母是 profit_daily 近 7 个**完整**业务日的日均（不含今天，见
// RunwayWindowDays）。两个窗口刻意不同——看板的窗口由调用方给，
// 日均消耗的窗口是口径的一部分，不该随调用方选的区间变。
func (s *SummaryStore) UpstreamSummaries(
	ctx context.Context, q SummaryQuery,
) ([]UpstreamSummary, error) {
	accounts, windows, tokenCounts, err := s.summaryParts(ctx, q)
	if err != nil {
		return nil, err
	}

	balances, recent, thresholds, now, err := s.runwayInputs(ctx, q.Environment, q.Thresholds)
	if err != nil {
		return nil, err
	}

	out := make([]UpstreamSummary, 0, len(accounts))
	for _, account := range accounts {
		item := UpstreamSummary{
			Account:    account,
			TokenCount: tokenCounts[account.ID],
			Window:     windows[account.ID],
			Balance:    balancePtr(balances, account.ID),
		}
		// 与 UpstreamRunways 走同一段计算——看板上那个天数与告警判据上的
		// 天数因此出自同一份代码。
		runway, err := runwayFor(account, item.Balance, recent[account.ID], thresholds, now)
		if err != nil {
			return nil, err
		}
		item.Runway = runway
		out = append(out, item)
	}
	return out, nil
}

// summaryParts 取两个端点共用的三样东西：登记簿骨架、台账聚合、令牌计数。
func (s *SummaryStore) summaryParts(ctx context.Context, q SummaryQuery) (
	[]UpstreamAccount, map[uuid.UUID]ProfitWindow, map[uuid.UUID]int, error,
) {
	if q.Environment == "" {
		return nil, nil, nil, fmt.Errorf("environment: %w", ErrMissingField)
	}
	if q.From.IsZero() || q.To.IsZero() {
		return nil, nil, nil, fmt.Errorf("业务日区间: %w", ErrMissingField)
	}
	if q.To.Before(q.From) {
		return nil, nil, nil, fmt.Errorf("业务日区间 %s..%s 起止颠倒: %w",
			q.From.Format(ProfitBusinessDayLayout), q.To.Format(ProfitBusinessDayLayout),
			ErrInvalidFormat)
	}

	accountRows, err := s.q.ListUpstreamAccountsByEnvironment(ctx, q.Environment)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("list upstream accounts: %w", err)
	}
	accounts, err := accountsFromRows(accountRows)
	if err != nil {
		return nil, nil, nil, err
	}

	mappingRows, err := s.q.ListTokenMappingsByEnvironment(ctx, q.Environment)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("list token mappings: %w", err)
	}
	tokenCounts := make(map[uuid.UUID]int, len(accounts))
	for _, m := range mappingRows {
		tokenCounts[m.UpstreamAccountID]++
	}

	sums, err := s.q.SummarizeProfitDailyByAccount(ctx,
		gen.SummarizeProfitDailyByAccountParams{
			Environment: q.Environment,
			FromDay:     dateValue(q.From),
			ToDay:       dateValue(q.To),
		})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("summarize profit_daily: %w", err)
	}
	windows := make(map[uuid.UUID]ProfitWindow, len(sums))
	for _, r := range sums {
		window := newProfitWindow(q.From, q.To,
			r.RowCount, r.RevenueKnownRows, r.CostKnownRows,
			r.RevenueMinorSum, r.CostMinorSum, r.AccountGrainRows,
			r.Currency, r.CurrencyCount > 1)
		window.OldestCostObservedAt = tsValue(r.OldestCostObservedAt)
		window.OldestRevenueObservedAt = tsValue(r.OldestRevenueObservedAt)
		window.LatestUpdatedAt = tsValue(r.LatestUpdatedAt)
		window.Source = r.Source
		windows[r.UpstreamAccountID] = window
	}
	// 登记簿里有、台账里没有的账号补一个**空窗口**而不是留 map 零值：
	// 零值窗口的 From/To 是零时间，前端拿去渲染会得到 0001-01-01。
	for _, account := range accounts {
		if _, ok := windows[account.ID]; !ok {
			windows[account.ID] = newProfitWindow(q.From, q.To, 0, 0, 0, 0, 0, 0, "", false)
		}
	}
	return accounts, windows, tokenCounts, nil
}

func (s *SummaryStore) latestBalances(
	ctx context.Context, environment string,
) (map[uuid.UUID]BalanceReading, error) {
	rows, err := s.q.ListLatestBalancesByEnvironment(ctx, environment)
	if err != nil {
		return nil, fmt.Errorf("list latest balances: %w", err)
	}
	out := make(map[uuid.UUID]BalanceReading, len(rows))
	for _, r := range rows {
		out[r.UpstreamAccountID] = balanceFromRow(r)
	}
	return out, nil
}

// recentCostRow 是可用天数分母那一侧的原始材料。
type recentCostRow struct {
	sum         int64
	coveredDays int
	currency    string
}

// recentCost 取近 RunwayWindowDays 个**完整**业务日的成本与覆盖天数。
//
// 窗口是 [今天−7, 今天−1]（含两端）——**不含今天**。今天还在累积，
// 算进去会让日均偏低、可用天数虚高，而且那个虚高幅度每天早上最大、
// 随时间缩小：一个每天规律性说谎的预警值。
//
// 「今天」按平台默认的 CST +08:00 算（★口径常量 §4）。逐账号的
// business_day_tz 可以不同，但那影响的是台账切日；这里选的是一个
// 统一的查询窗口，用各账号各自的时区会让同一次请求查出七八个不同的区间。
func (s *SummaryStore) recentCost(
	ctx context.Context, environment string,
) (map[uuid.UUID]recentCostRow, error) {
	today := BusinessDayAt(s.now(), DefaultBusinessDayLocation())
	to := today.AddDate(0, 0, -1)
	from := to.AddDate(0, 0, -(RunwayWindowDays - 1))

	rows, err := s.q.SumRecentCostByAccount(ctx, gen.SumRecentCostByAccountParams{
		Environment: environment,
		FromDay:     dateValue(from),
		ToDay:       dateValue(to),
	})
	if err != nil {
		return nil, fmt.Errorf("sum recent cost: %w", err)
	}
	out := make(map[uuid.UUID]recentCostRow, len(rows))
	for _, r := range rows {
		item := recentCostRow{
			sum:         r.CostMinorSum,
			coveredDays: int(r.CoveredDays),
			currency:    r.Currency,
		}
		if r.CurrencyCount > 1 {
			// 币种混杂：这个和是错数，日均也就无从谈起。清空币种让
			// ComputeRunway 走 currency_mismatch 那一支并说清原因。
			item.currency = ""
		}
		out[r.UpstreamAccountID] = item
	}
	return out, nil
}

// SortChannelSummaries 按稳定顺序排列渠道摘要。
//
// 稳定顺序不是洁癖：看板是一张人反复扫的表，行序每次刷新都变的话，
// 「这条渠道刚才是不是在上面」就没法回答。按接入方式再按系统与账号 id 排——
// 同一类口径的渠道挨在一起，比按金额排更适合核对（金额排序会让一条渠道
// 因为今天多花了几分钱就跳到别处）。
func SortChannelSummaries(items []ChannelSummary) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i].Account, items[j].Account
		if a.AccessMethod != b.AccessMethod {
			return a.AccessMethod < b.AccessMethod
		}
		if a.SystemType != b.SystemType {
			return a.SystemType < b.SystemType
		}
		if a.BaseURL != b.BaseURL {
			return a.BaseURL < b.BaseURL
		}
		return a.ID.String() < b.ID.String()
	})
}

// SortUpstreamSummaries 按「最该被看见的排前面」排列上游摘要。
//
// 与渠道表刻意不同：这张表是**预警**用的，可用天数最少的必须在最上面。
// 算不出天数的排在有天数的之后（它们要看的是「为什么算不出」，不是紧急程度），
// 再按渠道表的那套稳定顺序兜底。
func SortUpstreamSummaries(items []UpstreamSummary) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Runway.Known() != b.Runway.Known() {
			return a.Runway.Known()
		}
		if a.Runway.Known() && b.Runway.Known() && *a.Runway.Days != *b.Runway.Days {
			return *a.Runway.Days < *b.Runway.Days
		}
		if a.Account.SystemType != b.Account.SystemType {
			return a.Account.SystemType < b.Account.SystemType
		}
		return a.Account.ID.String() < b.Account.ID.String()
	})
}
