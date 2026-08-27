package finance

// 计量型渠道的成本 / 收入采集（XM-0037b，设计稿 §8.1）。
//
// 一轮采集 = 遍历登记簿里还在跑的计量型账号 → 逐令牌读上游实扣（§3.1）
// → 按 recharge_ratio 折算（§2.4）→ 读自营账号使用计费收入（§3.2）
// → 按 §5 三条纪律写台账。
//
// 采集编排放在本包而不是 jobs 包，是为了让它在**没有 River、没有库**的情况下
// 也能被完整测试：三条不静默纪律的分支（两侧未知 / 只有一侧 / 都有）与逐账号
// 失败隔离，都是这一层的行为。jobs 包里那个 Worker 只负责「多久跑一次、
// 失败要不要重试、日志长什么样」。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/connectors/metering"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// AccountRegistry 是采集用到的登记簿只读子集（*Store 满足）。
//
// 只声明用得到的两个方法而不是直接依赖 *Store：单元测试能用内存实现跑完
// 整条纪律路径，不必先起一个库。
type AccountRegistry interface {
	ListActiveAccountsByAccessMethod(
		ctx context.Context, environment string, method AccessMethod,
	) ([]UpstreamAccount, error)
	ListTokenMappingsByAccount(
		ctx context.Context, accountID uuid.UUID,
	) ([]TokenMapping, error)
}

// LedgerWriter 是采集用到的台账写入子集（*ProfitStore 满足）。
//
// 刻意只有 WriteRow 一个方法：三条纪律全在它里面，接口里没有第二个写入口，
// 采集就不可能绕过它们——编译期挡住，比注释挡住可靠
// （同 jobs.ObservationStore 不声明 Upsert 的理由）。
type LedgerWriter interface {
	WriteRow(ctx context.Context, row ProfitRow) (ProfitRow, error)
}

// MeteringClientFactory 按上游账号构造一个只读取数客户端。
//
// 用工厂而不是进程启动时构造一次：凭据会轮换，握着的连接不会知道；
// 而且每个上游账号有各自的 endpoint 与 CredentialRef，本来就不是一个客户端
// （同 jobs.Sub2APIClientFactory 的理由）。
type MeteringClientFactory func(
	ctx context.Context, account UpstreamAccount,
) (metering.ReadClient, error)

// PlatformResolver 给出一条映射的自营平台归属（§5.2 的分桶键）。
//
// 返回空串 = 未配对，落库为 NULL 并进「未归属」那一桶——**这是当前的正常状态**：
// 登记簿（§2.1）里没有任何一列记录「这个自营账号属于哪个自营平台」，
// 平台归属的配置面是 XM-0037c/d 的事。
//
// 那为什么现在就要这个钩子？因为 §1.3 要求 platform_id **第一天就写全**——
// 指的是「写入路径从第一天起就带着这一列」，不是「第一天就必须有值」（§2.2
// 明确 NULL = 写入当时未配对）。留下解析点，037d 接上归属配置时只需注入一个
// 函数，不必回来改台账的写入路径——而改写入路径正是 SoloAI 演进期
// 「写入方漏写 platform_id」那个缺陷的来源。
type PlatformResolver func(account UpstreamAccount, mapping TokenMapping) string

// CollectorOptions 是采集器的构造参数。
type CollectorOptions struct {
	Logger      *slog.Logger
	Environment string
	// InstanceID 会成为台账行的 source 与观测的 Source。
	InstanceID string

	Registry AccountRegistry
	Ledger   LedgerWriter

	NewClient       MeteringClientFactory
	ResolvePlatform PlatformResolver

	// Now 可注入固定时钟；默认 time.Now。业务日切分与「今日可覆盖、过去冻结」
	// 都靠它，所以它必须可注入——否则那条纪律只能在跨零点时靠人肉验证。
	Now func() time.Time
}

// Collector 执行一轮成本采集。
type Collector struct {
	logger      *slog.Logger
	environment string
	instanceID  string

	registry        AccountRegistry
	ledger          LedgerWriter
	newClient       MeteringClientFactory
	resolvePlatform PlatformResolver
	now             func() time.Time
}

// NewCollector 构造采集器并补齐安全默认值。
func NewCollector(opts CollectorOptions) *Collector {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.ResolvePlatform == nil {
		// 默认「未配对」而不是编一个平台名：一个猜出来的归属会让四桶里
		// 「有效平台」那一桶凭空多出金额，且完全看不出是猜的（宪法 12 条）。
		opts.ResolvePlatform = func(UpstreamAccount, TokenMapping) string { return "" }
	}
	return &Collector{
		logger:          opts.Logger,
		environment:     opts.Environment,
		instanceID:      opts.InstanceID,
		registry:        opts.Registry,
		ledger:          opts.Ledger,
		newClient:       opts.NewClient,
		resolvePlatform: opts.ResolvePlatform,
		now:             opts.Now,
	}
}

// CollectResult 是一轮采集的结果统计。
//
// 每一类「没写成」都单独计数，不合并成一个 failed：
// 「两侧都没读到」（上游今天没数据）、「只有一侧因而不建行」（纪律生效）、
// 「写库失败」（真故障）是三件完全不同的事，合成一个数字之后，
// 看板上的红点就再也说不清该不该有人起来处理。
type CollectResult struct {
	AccountsTotal  int
	AccountsFailed int

	RowsWritten int
	// RowsSkippedNothingKnown：两侧都未知，整对跳过（§5.1，ErrProfitNothingKnown）。
	RowsSkippedNothingKnown int
	// RowsSkippedOneSided：只有一侧已知且台账无此行，不建行（§5.1）。
	RowsSkippedOneSided int
	// RowsFailed：写库失败或行本身非法——这一类才是真故障。
	RowsFailed int

	// RowsWithProfit 是写完之后**两侧都已知**、因而算得出毛利的行数。
	//
	// 它与 RowsWritten 分开计数：一次只刷新了成本侧的写入也算「写成了」，
	// 但那一行的毛利仍然是未知。下面三个合计只覆盖 RowsWithProfit 那些行，
	// 两个数字一起给，合计才可解释（宪法 12 条）。
	RowsWithProfit  int
	RevenueMinorSum int64
	CostMinorSum    int64
	// Currency 是上面两个合计的币种；MixedCurrency 为真时合计无意义
	// （不同币种的最小单位不能相加），调用方不得展示它们。
	Currency      string
	MixedCurrency bool

	// Costs / Revenues 是本轮读到的原始读数，交给调用方转成 ops 观测
	// （Connector 不直接写库，见 metering.ToObservations 的注释）。
	Costs    []metering.TokenCost
	Revenues []metering.AccountRevenue

	// Partial 表示这一轮没有采全：有账号失败、有行被跳过，或有归属歧义。
	// 它会原样进观测的 IsPartial，让看板把这一轮画成虚线而不是实线。
	Partial bool

	// FirstError 是本轮第一个非语义错误，用于给整轮一个代表性的错误分类。
	// 两个「跳过」不算错误，不会进这里。
	FirstError error
}

// accumulate 把一行**已入库**的台账并进本轮合计。
//
// 只统计两侧都已知的行：缺一侧的行没有可断言的毛利（§5.1），
// 把它的成本并进合计会得到一个偏低的毛利，而收入侧看起来完全正常。
func (r *CollectResult) accumulate(row ProfitRow) {
	revenue, cost := row.RevenueMinor, row.CostMinor
	if revenue == nil || cost == nil {
		return
	}
	switch {
	case r.Currency == "":
		r.Currency = row.Currency
	case r.Currency != row.Currency:
		// 把不同币种的最小单位加在一起是纯粹的错数（同
		// metering.costsObservation 的 mixed_currency 纪律）。标记出来，
		// 合计留给调用方按币种分桶——或者干脆不展示。
		r.MixedCurrency = true
	}
	r.RowsWithProfit++
	r.RevenueMinorSum += *revenue
	r.CostMinorSum += *cost
}

// ProfitMinorSum 返回本轮已入账部分的毛利合计。
//
// 币种混杂时给不出（返回 nil）：那个和是错数，留一个空位比留一个错数字好。
func (r CollectResult) ProfitMinorSum() *int64 {
	if r.MixedCurrency {
		return nil
	}
	profit := r.RevenueMinorSum - r.CostMinorSum
	return &profit
}

// BusinessDays 是本轮实际写入涉及的业务日（去重、升序）。
//
// 多个上游账号可以有不同的 business_day_tz（登记簿允许逐账号配），
// 于是同一轮采集可能同时落在两个业务日上。观测里报一个「代表性的今天」
// 会掩盖这件事，所以原样报全部。
func (r CollectResult) BusinessDays() []string {
	seen := make(map[string]struct{}, len(r.Costs)+len(r.Revenues))
	for _, c := range r.Costs {
		seen[c.Day] = struct{}{}
	}
	for _, v := range r.Revenues {
		seen[v.Day] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for day := range seen {
		out = append(out, day)
	}
	sort.Strings(out)
	return out
}

// CollectOnce 跑一轮采集。
//
// 返回 error 的条件很窄：**只有取不到工作清单**（读不了登记簿）才算整轮失败。
// 逐账号的失败已经被隔离并计数了——一个上游挂掉不该让其余账号今天也没有数
// （brief 的「逐账号失败隔离」，同 sub2api_sync 把读取失败分组保留的理由）。
func (c *Collector) CollectOnce(ctx context.Context) (CollectResult, error) {
	var result CollectResult
	if c.registry == nil || c.ledger == nil || c.newClient == nil {
		return result, errors.New("finance: collector 缺少 registry / ledger / client factory")
	}

	// 只取计量型：三种接入方式对应三条完全不同的成本算法（§2.0），
	// 订阅型走 §3.5 摊销（XM-0037c），official_api v1 占位后置（§12）。
	// 按接入方式取清单而不是取全量再在内存里 switch——漏掉一个新枚举值时，
	// 前者什么都不做，后者会拿计量型的算法去算它。
	accounts, err := c.registry.ListActiveAccountsByAccessMethod(
		ctx, c.environment, AccessUpstreamKey)
	if err != nil {
		return result, fmt.Errorf("取计量型账号清单: %w", err)
	}
	result.AccountsTotal = len(accounts)

	for _, account := range accounts {
		if err := ctx.Err(); err != nil {
			// 上下文取消说明本进程在关机，不是采集出问题。剩下的账号留到下一轮。
			return result, err
		}
		if err := c.collectAccount(ctx, account, &result); err != nil {
			result.AccountsFailed++
			result.Partial = true
			if result.FirstError == nil {
				result.FirstError = err
			}
			c.logger.LogAttrs(ctx, slog.LevelWarn, "finance_collect_account_failed",
				slog.String("module", "platform.finance"),
				slog.String("environment", c.environment),
				slog.String("upstream_account_id", account.ID.String()),
				slog.String("system_type", string(account.SystemType)),
				slog.String("error_code", string(connector.KindOf(err))),
			)
		}
	}
	if result.RowsFailed > 0 || result.RowsSkippedNothingKnown > 0 ||
		result.RowsSkippedOneSided > 0 {
		result.Partial = true
	}
	return result, nil
}

// collectAccount 采集一个上游账号。
//
// 返回 error 表示**这个账号整体没采成**（业务日算不出、客户端建不起来、
// 映射读不到）。逐令牌的失败不返回，它们按 §5.1 落成 NULL 并计数——
// 一个令牌读不到不该让同账号其他令牌今天也没有数。
func (c *Collector) collectAccount(
	ctx context.Context, account UpstreamAccount, result *CollectResult,
) error {
	loc, err := account.BusinessDayLocation()
	if err != nil {
		return err
	}
	day := BusinessDayAt(c.now(), loc)
	dayText := day.Format(ProfitBusinessDayLayout)

	mappings, err := c.registry.ListTokenMappingsByAccount(ctx, account.ID)
	if err != nil {
		return fmt.Errorf("取令牌映射: %w", err)
	}
	if len(mappings) == 0 {
		// 没有映射的账号不是错误：刚登记、还没配令牌是正常状态。
		// 但它也确实没有数据，所以这一轮它什么都不写——不编一行零成本。
		return nil
	}

	client, err := c.newClient(ctx, account)
	if err != nil {
		return fmt.Errorf("建取数客户端: %w", err)
	}

	// 收入按**自营账号**取一次（§3.2 的端点是账号级），而不是每个令牌取一次：
	// 同一账号取 N 次会拿到 N 个相同的数，白打上游 N-1 次。
	revenues, ambiguous := c.readRevenues(ctx, client, account, mappings, dayText, result)
	if len(ambiguous) > 0 {
		result.Partial = true
	}

	for _, mapping := range mappings {
		if _, skip := ambiguous[mapping.OwnAccountID]; skip {
			// 归属歧义：这一组令牌今天不入账，理由见 readRevenues。
			continue
		}
		c.collectToken(ctx, client, account, mapping, day, dayText, revenues, result)
	}
	return nil
}

// readRevenues 逐自营账号读一次使用计费收入（§3.2）。
//
// 第二个返回值是「收入归属有歧义」的自营账号集合。歧义来自登记簿允许的一种
// 真实形态：**多把上游令牌供给同一个自营账号**（token_map 的反向索引刻意不是
// 唯一索引，见 000008 迁移的注释）。此时收入是账号级的一个数，而台账的行是
// 令牌级的——把同一个收入写进 N 行，按渠道 SUM 就会把它算 N 遍；
// 只写进其中一行，另外几行就变成「成本已知、收入未知」，按 §5.1 不建行，
// 那几笔成本会从台账里消失。
//
// 两条路都会产出一个**看起来完全正常的错数字**，而设计稿没有给这种形态的
// 归属规则。所以这里的选择是：**这一组令牌今天不入账，并把歧义显式报出来**
// （宪法 12 条：宁可缺一块并说清楚，也不给一个不知道错在哪的数）。
// 需要产品定义 N:1 的收入拆分口径，已记入 PR 的 follow_ups。
func (c *Collector) readRevenues(
	ctx context.Context, client metering.ReadClient, account UpstreamAccount,
	mappings []TokenMapping, dayText string, result *CollectResult,
) (map[string]metering.AccountRevenue, map[string]struct{}) {
	tokensPerAccount := make(map[string]int, len(mappings))
	for _, m := range mappings {
		tokensPerAccount[m.OwnAccountID]++
	}

	revenues := make(map[string]metering.AccountRevenue, len(tokensPerAccount))
	ambiguous := make(map[string]struct{})
	for _, m := range mappings {
		ownAccountID := m.OwnAccountID
		if _, done := revenues[ownAccountID]; done {
			continue
		}
		if _, known := ambiguous[ownAccountID]; known {
			continue
		}
		if tokensPerAccount[ownAccountID] > 1 {
			ambiguous[ownAccountID] = struct{}{}
			c.logger.LogAttrs(ctx, slog.LevelWarn, "finance_revenue_attribution_ambiguous",
				slog.String("module", "platform.finance"),
				slog.String("environment", c.environment),
				slog.String("upstream_account_id", account.ID.String()),
				slog.String("own_account_id", ownAccountID),
				slog.Int("token_count", tokensPerAccount[ownAccountID]),
				slog.String("error_code", "revenue_attribution_ambiguous"),
				slog.String("hint", "同一自营账号挂了多把上游令牌；账号级收入无法逐令牌归属，"+
					"本轮不入账。需产品定义拆分口径（设计稿 §12.2 的「渠道键」问题）"),
			)
			continue
		}

		revenue, err := client.AccountRevenue(ctx, ownAccountID, dayText)
		if err != nil {
			// 读不到 = 未知，不是 0（§5.1）。不进 revenues，后面就写 NULL。
			//
			// not_supported 在这里是**预期内**的常态而不是故障：newapi 的收入
			// 走自营 new-api 库的只读直连（§3.2），不在这条 HTTP 契约上。
			// 它照样只让收入侧为未知，不影响成本侧照常入账。
			level := slog.LevelWarn
			kind := connector.KindOf(err)
			if kind == connector.KindNotSupported {
				level = slog.LevelInfo
			}
			c.logger.LogAttrs(ctx, level, "finance_revenue_read_failed",
				slog.String("module", "platform.finance"),
				slog.String("environment", c.environment),
				slog.String("upstream_account_id", account.ID.String()),
				slog.String("own_account_id", ownAccountID),
				slog.String("business_day", dayText),
				slog.String("error_code", string(kind)),
			)
			if result.FirstError == nil && kind != connector.KindNotSupported {
				result.FirstError = err
			}
			result.Partial = true
			continue
		}
		revenues[ownAccountID] = revenue
		result.Revenues = append(result.Revenues, revenue)
	}
	return revenues, ambiguous
}

// collectToken 采集一个令牌并写一行台账。
//
// 成本读失败 → CostMinor 为 nil（未知，不是 0）；收入这一侧同理。
// 两侧都未知时 WriteRow 会返回 ErrProfitNothingKnown，整对跳过（§5.1）。
func (c *Collector) collectToken(
	ctx context.Context, client metering.ReadClient,
	account UpstreamAccount, mapping TokenMapping,
	day time.Time, dayText string,
	revenues map[string]metering.AccountRevenue, result *CollectResult,
) {
	row := ProfitRow{
		UpstreamAccountID: account.ID,
		BusinessDay:       day,
		BusinessDayTZ:     account.BusinessDayTZ,
		TokenID:           mapping.UpstreamTokenID,
		AccountID:         mapping.OwnAccountID,
		PlatformID:        c.resolvePlatform(account, mapping),
		Currency:          account.Currency,
		Source:            c.instanceID,
		// 倍率无论成本读成没读成都先带上：它是本行**将要用**的折算依据，
		// 而 Validate 只在成本已知时要求它。带上不会有副作用——
		// 收入侧的写入语句根本不碰 ratio_snapshot（§6.3）。
		RatioSnapshot: account.RechargeRatio,
	}

	if cost, ok := c.readCost(ctx, client, account, mapping, dayText, result); ok {
		minor := cost.CostMinorUnits
		row.CostMinor = &minor
		// 冻结**实际用的**那个倍率，而不是再从 account 上读一遍：
		// 两者此刻相同，但「实际用的」这件事只有折算那一步知道（§6.3）。
		row.RatioSnapshot = cost.RatioSnapshot
		if !cost.ObservedAt.IsZero() {
			observed := cost.ObservedAt.UTC()
			row.CostObservedAt = &observed
		}
		if cost.Currency != "" {
			row.Currency = cost.Currency
		}
		result.Costs = append(result.Costs, cost)
	}
	if revenue, ok := revenues[mapping.OwnAccountID]; ok {
		minor := revenue.RevenueMinorUnits
		row.RevenueMinor = &minor
		if !revenue.ObservedAt.IsZero() {
			observed := revenue.ObservedAt.UTC()
			row.RevenueObservedAt = &observed
		}
		if revenue.Currency != "" && row.CostMinor == nil {
			row.Currency = revenue.Currency
		}
	}

	switch stored, err := c.ledger.WriteRow(ctx, row); {
	case err == nil:
		result.RowsWritten++
		// 合计取**库里那一行**而不是刚组装的 row：一次只刷新成本侧的写入
		// 会保留今天早些时候已经读到的收入（见 UpdateProfitDailyCost），
		// 于是存进去的那一行可能两侧都全，而手里这个 row 只有一侧。
		// 拿手里的算会把已经入账的收入漏掉。
		result.accumulate(stored)
	case errors.Is(err, ErrProfitNothingKnown):
		// 两侧都没读到：这一对今天没有可入账的事实。不是故障。
		result.RowsSkippedNothingKnown++
	case errors.Is(err, ErrProfitOneSidedNoRow):
		// 只有一侧已知且台账还没有这一行：纪律生效，不建行。不是故障。
		result.RowsSkippedOneSided++
	default:
		result.RowsFailed++
		result.Partial = true
		if result.FirstError == nil {
			result.FirstError = err
		}
		c.logger.LogAttrs(ctx, slog.LevelError, "finance_ledger_write_failed",
			slog.String("module", "platform.finance"),
			slog.String("environment", c.environment),
			slog.String("upstream_account_id", account.ID.String()),
			slog.String("upstream_token_id", mapping.UpstreamTokenID),
			slog.String("business_day", dayText),
			slog.String("error_code", string(connector.KindOf(err))),
		)
	}
}

// readCost 读一个令牌的上游实扣并折算成平台成本（§3.1 + §2.4）。
func (c *Collector) readCost(
	ctx context.Context, client metering.ReadClient,
	account UpstreamAccount, mapping TokenMapping, dayText string, result *CollectResult,
) (metering.TokenCost, bool) {
	usage, err := client.TokenUsage(ctx, metering.TokenRef{
		UpstreamTokenID: mapping.UpstreamTokenID,
		// 凭据只经引用（ADR-014、宪法 7 条）：明文由 SecretProvider 在驱动内部
		// 拼 HTTP 头那一瞬解析，本包从头到尾只见得到这个 secret:// 串。
		CredentialRef: mapping.CredentialRef,
	}, dayText)
	if err == nil {
		var cost metering.TokenCost
		cost, err = metering.CostOf(usage, account.RechargeRatio)
		if err == nil {
			return cost, true
		}
	}

	// 读不到或折不出 = 成本未知，写 NULL 不写 0（§5.1）。
	c.logger.LogAttrs(ctx, slog.LevelWarn, "finance_cost_read_failed",
		slog.String("module", "platform.finance"),
		slog.String("environment", c.environment),
		slog.String("upstream_account_id", account.ID.String()),
		slog.String("upstream_token_id", mapping.UpstreamTokenID),
		slog.String("business_day", dayText),
		slog.String("error_code", string(connector.KindOf(err))),
	)
	result.Partial = true
	if result.FirstError == nil {
		result.FirstError = err
	}
	return metering.TokenCost{}, false
}

// NewFakeMeteringClientFactory 造一个全程走假上游的客户端工厂。
//
// **Fake 是默认模式**（同 connectors/sub2api 的 fake→real 演进）：真实只读凭据
// 到位前，采集链路要能整条跑通，而不是让 worker 起不来。
//
// 业务日时区跟着**每个账号**走而不是取一个全局值：Fake 的 checkDay 复刻了
// 真实驱动「只回答今天」的约束（见 metering.FakeOptions.TodayOnly），
// 时区不一致会让采集在跨零点的那几个小时里对某些账号收到 not_supported——
// 那正是真环境会发生的事，所以这里必须传对。
func NewFakeMeteringClientFactory(now func() time.Time) MeteringClientFactory {
	if now == nil {
		now = time.Now
	}
	return func(_ context.Context, account UpstreamAccount) (metering.ReadClient, error) {
		loc, err := account.BusinessDayLocation()
		if err != nil {
			return nil, err
		}
		return metering.NewFake(metering.FakeOptions{
			Now:         now,
			BusinessDay: loc,
		}), nil
	}
}

// ErrMeteringRealClientUnavailable 是 real 模式**配置未就绪**时的确定性失败。
//
// 登记簿已经提供了每个账号的 endpoint（base_url）与 CredentialRef，
// 但真实取数还需要两样只能由部署给出的东西：目标主机 allowlist（ADR-004，
// 留空不是「放行一切」而是「一个请求都发不出去」）与一个能解析引用的
// SecretProvider（ADR-014）。
//
// 分类保持 not_supported：它表达的是「本部署还不具备真实读取能力」，
// 与「配了但配错了」区分开——后者由 metering 的构造函数归 internal，
// 运维一看 error_code 就知道该去补配置还是去改配置
// （与 jobs.ErrSub2APIRealClientUnavailable 同一条纪律）。
var ErrMeteringRealClientUnavailable = errors.New(
	"metering 真实只读客户端未配置：缺少目标 allowlist / SecretProvider")

// RealMeteringConfig 是 real 模式**由部署提供**的那部分配置。
//
// 它刻意只装「允许连哪些主机、用什么解析引用、多久超时」，不装 endpoint 与
// 凭据引用——那两样逐账号不同，来自登记簿（§2.1），进程级配置里再放一份
// 只会让两处漂开。
type RealMeteringConfig struct {
	// TargetAllowlist 是允许连接的主机精确清单（ADR-004）；
	// 为空时一个请求都发不出去——护栏 fail closed，不是「放行一切」。
	TargetAllowlist []string
	// Secrets 解析账号级与每令牌的 CredentialRef。
	//
	// 采集器本身**不解析任何凭据**：它只是把 Provider 原样递给驱动，
	// 明文在驱动里拼 HTTP 头那一瞬才出现（ADR-014、宪法 7 条）。
	Secrets secrets.SecretProvider
	// Timeout 是单次 HTTP 请求的超时；零值由客户端回落到保守默认。
	Timeout time.Duration
	// Environment / InstanceID 进连接配置（connector.Config 要求非空）。
	Environment string
	InstanceID  string

	// NewAPIRevenue 是 NewAPI 收入侧的只读数据库通道（XM-0044，§3.2）。
	//
	// **nil 是合法且是默认**：没配 XM_NEWAPI_REVENUE_DSN 的部署里，
	// newapi 账号的收入继续返回 not_supported、台账写 NULL——那是「这条链路
	// 还没接通」的如实表达，不是故障（collector 里 not_supported 走 Info 级）。
	//
	// 它是**进程级**的一个实例而不是逐账号新建：连接池连的是别人家的生产库，
	// 每个账号开一个池就是占 N 倍连接（对齐 SoloAI BorrowSource 的取舍）。
	// 因此这里收的是一个已经建好的通道，生命周期由进程入口负责（含 Close）。
	NewAPIRevenue metering.RevenueSource
}

// missing 列出缺了哪几项配置。
//
// 返回**清单**而不是第一个错：运维一次就能把配置补齐，而不是补一个重启一次
// 再看下一个缺什么。名字用环境变量名而不是 Go 字段名——看日志的人手里
// 拿的是 .env，不是源码。
func (c RealMeteringConfig) missing() []string {
	var out []string
	if len(c.TargetAllowlist) == 0 {
		out = append(out, "XM_FINANCE_COLLECT_TARGET_ALLOWLIST")
	}
	if c.Secrets == nil {
		// 装配问题而不是环境变量问题，但同样让 real 模式立不起来，
		// 所以并进同一份清单，用能让人找到装配点的名字。
		out = append(out, "secret provider")
	}
	return out
}

// NewRealMeteringClientFactory 按登记簿逐账号构造真实的只读取数客户端。
//
// 「按 system_type 挑驱动」是这里唯一的分支：两个上游的成本端点、鉴权方式与
// 收入通道完全不同（§3.1/§3.2），而登记簿里的 system_type 就是那个选择器。
// official 走到这里报 not_supported——它的成本口径 v1 占位后置（§2.0/§12），
// 而一个「占位」的口径绝不能悄悄套用计量型的算法。
//
// 配置不全时返回**分类明确**的错误而不是 nil client：调用方按
// connector.KindOf 归类，这一轮该账号计为失败并落进结构化日志，
// 而不是数据静静停更（规格 §9.1）。
func NewRealMeteringClientFactory(
	cfg RealMeteringConfig, now func() time.Time,
) MeteringClientFactory {
	return func(_ context.Context, account UpstreamAccount) (metering.ReadClient, error) {
		if missing := cfg.missing(); len(missing) > 0 {
			return nil, connector.NewError(
				connector.KindNotSupported, "metering.client.real",
				fmt.Errorf("缺少 %s: %w", strings.Join(missing, ", "),
					ErrMeteringRealClientUnavailable))
		}
		if strings.TrimSpace(account.BaseURL) == "" {
			// 登记簿允许 base_url 为空（订阅型可能没有可读端点），但计量型
			// 没有端点就无从取数。在这里报，而不是让 connector.Config.Validate
			// 报一句「endpoint 为空」——后者说不清是哪个账号。
			return nil, connector.NewError(
				connector.KindInternal, "metering.client.real",
				fmt.Errorf("上游账号 %s 没有 base_url，计量型渠道无从取数", account.ID))
		}
		loc, err := account.BusinessDayLocation()
		if err != nil {
			return nil, connector.NewError(connector.KindInternal, "metering.client.real", err)
		}

		conn := connector.Config{
			ServiceInstanceID: cfg.InstanceID,
			Environment:       cfg.Environment,
			Endpoint:          account.BaseURL,
			CredentialRef:     account.CredentialRef,
			TargetAllowlist:   cfg.TargetAllowlist,
			Timeout:           cfg.Timeout,
		}
		opts := []metering.Option{
			metering.WithBusinessDayTimezone(loc),
			metering.WithCurrency(account.Currency),
		}
		if now != nil {
			opts = append(opts, metering.WithClock(now))
		}

		switch account.SystemType {
		case SystemSub2API:
			return metering.NewSub2APIClient(conn, cfg.Secrets, opts...)
		case SystemNewAPI:
			// 收入通道挂上去（XM-0044）。nil 时这一项等于没传，
			// AccountRevenue 保持 not_supported——「没配」与「配了但连不上」
			// 是两件事，前者不该被记成故障。
			if cfg.NewAPIRevenue != nil {
				opts = append(opts, metering.WithRevenueSource(cfg.NewAPIRevenue))
			}
			return metering.NewNewAPIClient(conn, cfg.Secrets, opts...)
		default:
			return nil, connector.NewError(
				connector.KindNotSupported, "metering.client.real",
				fmt.Errorf("system_type %q 还没有计量取数驱动（official 的成本口径 v1 占位后置，§2.0）",
					account.SystemType))
		}
	}
}
