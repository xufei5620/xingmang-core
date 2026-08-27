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

// SubscriptionRegistry 是摊销用到的订阅登记只读子集（*SubscriptionStore 满足）。
//
// 与 AccountRegistry 分开而不是并进去：计量型采集一个方法都用不上它，
// 合成一个接口只会让内存假货为了跑一条计量型用例去实现三个空方法。
type SubscriptionRegistry interface {
	ListAmortizableBatches(
		ctx context.Context, accountID uuid.UUID, day time.Time,
	) ([]AmortizableBatch, error)
}

// LedgerWriter 是采集用到的台账写入子集（*ProfitStore 满足）。
//
// 两个方法对应两条不同的入账纪律，**刻意不合并成一个**：
//
//	WriteRow           计量型：§5.1 三条分支（读不到写 NULL、只有一侧不建行）
//	WriteAmortizedRow  订阅型：成本是算出来的，收入未知时照样建行（见那里的注释）
//
// 分开是为了让「这一行按哪条纪律入账」在**调用点**就定下来，而不是靠某个
// 字段的取值在库层临时判断——后者会让一次计量型的读取失败在某个角落里
// 走进「照样建行」那条路，写出一行毛利 = −成本的记录。
// 接口里没有第三个写入口，采集因而绕不过这两条（同 jobs.ObservationStore
// 不声明 Upsert 的理由）。
type LedgerWriter interface {
	WriteRow(ctx context.Context, row ProfitRow) (ProfitRow, error)
	WriteAmortizedRow(ctx context.Context, row ProfitRow) (ProfitRow, error)
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
// 返回空串 = 未配对，落库为 NULL 并进「未归属」那一桶。
//
// **XM-0037c 起它有真实取值了**：登记簿新增了 upstream_account.platform_id
// （迁移 000010），默认解析器就读那一列。037b 留这个钩子时登记簿里还没有
// 任何一列能回答「这个上游账号被哪个自营平台在用」，于是它恒返回空串。
//
// 钩子本身保留而不是改成直接读字段：037d 可能需要更细的归属规则
// （同一个上游账号的不同令牌供给不同平台），届时注入一个函数即可，
// 不必回来改台账的写入路径——而改写入路径正是 SoloAI 演进期
// 「写入方漏写 platform_id」那个缺陷的来源。
type PlatformResolver func(account UpstreamAccount, mapping TokenMapping) string

// CollectorOptions 是采集器的构造参数。
type CollectorOptions struct {
	Logger      *slog.Logger
	Environment string
	// InstanceID 会成为台账行的 source 与观测的 Source。
	InstanceID string

	Registry AccountRegistry
	// Subscriptions 供订阅型渠道的摊销取数（XM-0037c，§3.5）。
	Subscriptions SubscriptionRegistry
	Ledger        LedgerWriter

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
	subscriptions   SubscriptionRegistry
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
		// 默认读登记簿那一列（XM-0037c 新增），而不是编一个平台名：
		// 一个猜出来的归属会让四桶里「有效平台」那一桶凭空多出金额，
		// 且完全看不出是猜的（宪法 12 条）。没配就是空串 = 未归属。
		opts.ResolvePlatform = func(account UpstreamAccount, _ TokenMapping) string {
			return account.PlatformID
		}
	}
	return &Collector{
		logger:          opts.Logger,
		environment:     opts.Environment,
		instanceID:      opts.InstanceID,
		registry:        opts.Registry,
		subscriptions:   opts.Subscriptions,
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

	// RowsAggregated：因「多把令牌供给同一自营账号」而写成**账号级聚合行**
	// 的行数（§12.2 的渠道键裁定）。
	//
	// 单独计数是因为它改变了台账的粒度：那几把令牌从此没有独立的下钻行。
	// 数字忽然变大意味着有人给某个自营账号加挂了第二把 key——那是一次
	// 值得知道的拓扑变化，而不是一个静默发生的聚合。
	RowsAggregated int

	// --- 订阅型渠道（XM-0037c，§3.5）---

	// SubscriptionAccountsTotal 是本轮遍历到的订阅型账号数。
	SubscriptionAccountsTotal int
	// SubscriptionRowsWritten 是摊销入账的行数（RowsWritten 的子集）。
	SubscriptionRowsWritten int
	// SubscriptionRowsCostOnly 是其中**收入未知**的行数。
	//
	// 它不是失败：订阅型渠道的收入通道在 v1 常常还没接（newapi 的收入 DSN
	// 是单独一片）。但它必须可见——一批只有成本没有收入的行会让渠道毛利
	// 全是 NULL，看板上得说得出为什么（宪法 12 条）。
	SubscriptionRowsCostOnly int
	// RowsSkippedNoBatch：订阅账号当日没有任何覆盖批次 → 成本未知，跳过。
	//
	// 「还没登记批次」与「订阅真的到期了」在库里长得一样，平台分不出来，
	// 所以两者都落成「未知」并计进这个数，让运营去补一笔批次或停用账号。
	RowsSkippedNoBatch int
	// RowsSkippedNoOwner：订阅账号没有唯一的自营账号可归属，跳过。
	//
	// 摊销成本是**账号级**的一笔钱，它必须记在某一个自营账号头上；
	// 零个映射（还没配）与多个不同的自营账号（拆分口径未定）都给不出那个头。
	RowsSkippedNoOwner int

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
	if c.subscriptions == nil {
		// 必填而不是「没配就跳过订阅型」：跳过的话，一个装配漏了这一项的部署
		// 会安安静静地把订阅渠道的成本全部漏掉——报表上那几条渠道的毛利
		// 恰好等于收入，看起来完全正常（宪法 12 条）。
		return result, errors.New("finance: collector 缺少 subscriptions registry（订阅型渠道的摊销取数）")
	}

	// 两轮分别按接入方式取清单，而不是取全量再在内存里 switch：
	// 三种接入方式对应三条完全不同的成本算法（§2.0），漏掉一个新枚举值时，
	// 按方式取什么都不做，取全量再 switch 会拿其中一套算法去算它。
	// official_api v1 占位后置（§12 拍板），因而两轮都不碰它。
	if err := c.collectMetered(ctx, &result); err != nil {
		return result, err
	}
	if err := c.collectSubscriptions(ctx, &result); err != nil {
		return result, err
	}

	if result.RowsFailed > 0 || result.RowsSkippedNothingKnown > 0 ||
		result.RowsSkippedOneSided > 0 || result.RowsSkippedNoBatch > 0 ||
		result.RowsSkippedNoOwner > 0 {
		result.Partial = true
	}
	return result, nil
}

// collectMetered 跑计量型那一轮（§3.1/§3.2，XM-0037b 的原路径）。
func (c *Collector) collectMetered(ctx context.Context, result *CollectResult) error {
	accounts, err := c.registry.ListActiveAccountsByAccessMethod(
		ctx, c.environment, AccessUpstreamKey)
	if err != nil {
		return fmt.Errorf("取计量型账号清单: %w", err)
	}
	result.AccountsTotal = len(accounts)

	for _, account := range accounts {
		if err := ctx.Err(); err != nil {
			// 上下文取消说明本进程在关机，不是采集出问题。剩下的账号留到下一轮。
			return err
		}
		if err := c.collectAccount(ctx, account, result); err != nil {
			c.recordAccountFailure(ctx, account, result, err)
		}
	}
	return nil
}

// collectSubscriptions 跑订阅型那一轮（§3.5 摊销，XM-0037c）。
//
// 与计量型完全对称的失败隔离：一个账号的批次配错了不该让其余账号今天也没有数。
func (c *Collector) collectSubscriptions(ctx context.Context, result *CollectResult) error {
	accounts, err := c.registry.ListActiveAccountsByAccessMethod(
		ctx, c.environment, AccessSubscriptionAccount)
	if err != nil {
		return fmt.Errorf("取订阅型账号清单: %w", err)
	}
	result.SubscriptionAccountsTotal = len(accounts)

	for _, account := range accounts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := c.amortizeAccount(ctx, account, result); err != nil {
			c.recordAccountFailure(ctx, account, result, err)
		}
	}
	return nil
}

// recordAccountFailure 把一个账号整体没采成计进结果并落结构化日志。
func (c *Collector) recordAccountFailure(
	ctx context.Context, account UpstreamAccount, result *CollectResult, err error,
) {
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
		slog.String("access_method", string(account.AccessMethod)),
		slog.String("error_code", string(connector.KindOf(err))),
	)
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
	groups := groupByOwnAccount(mappings)
	revenues := c.readRevenues(ctx, client, account, groups, dayText, result)

	for _, group := range groups {
		if len(group.Mappings) == 1 {
			// 单令牌账号维持**令牌级行**，保留下钻（§12.2）。
			c.collectToken(ctx, client, account, group.Mappings[0], day, dayText, revenues, result)
			continue
		}
		c.collectAggregatedAccount(ctx, client, account, group, day, dayText, revenues, result)
	}
	return nil
}

// ownAccountGroup 是「同一个自营账号名下的全部上游令牌」。
type ownAccountGroup struct {
	OwnAccountID string
	Mappings     []TokenMapping
}

// groupByOwnAccount 按自营账号把映射分组，顺序确定。
//
// 确定的顺序不是洁癖：分组顺序决定了日志里事件出现的次序，也决定了
// 聚合行取哪一条映射去解析 platform_id。让它随 map 迭代顺序变，
// 会让同一份配置在两轮采集里产出两种归属。
func groupByOwnAccount(mappings []TokenMapping) []ownAccountGroup {
	index := make(map[string]int, len(mappings))
	out := make([]ownAccountGroup, 0, len(mappings))
	for _, m := range mappings {
		if i, seen := index[m.OwnAccountID]; seen {
			out[i].Mappings = append(out[i].Mappings, m)
			continue
		}
		index[m.OwnAccountID] = len(out)
		out = append(out, ownAccountGroup{OwnAccountID: m.OwnAccountID, Mappings: []TokenMapping{m}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OwnAccountID < out[j].OwnAccountID })
	for i := range out {
		sort.Slice(out[i].Mappings, func(a, b int) bool {
			return out[i].Mappings[a].UpstreamTokenID < out[i].Mappings[b].UpstreamTokenID
		})
	}
	return out
}

// readRevenues 逐自营账号读一次使用计费收入（§3.2）。
//
// 一个自营账号一次，与它名下挂了几把令牌无关——端点本来就是账号级的。
func (c *Collector) readRevenues(
	ctx context.Context, client metering.ReadClient, account UpstreamAccount,
	groups []ownAccountGroup, dayText string, result *CollectResult,
) map[string]metering.AccountRevenue {
	revenues := make(map[string]metering.AccountRevenue, len(groups))
	for _, group := range groups {
		revenue, err := client.AccountRevenue(ctx, group.OwnAccountID, dayText)
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
				slog.String("own_account_id", group.OwnAccountID),
				slog.String("business_day", dayText),
				slog.String("error_code", string(kind)),
			)
			if result.FirstError == nil && kind != connector.KindNotSupported {
				result.FirstError = err
			}
			result.Partial = true
			continue
		}
		revenues[group.OwnAccountID] = revenue
		result.Revenues = append(result.Revenues, revenue)
	}
	return revenues
}

// collectAggregatedAccount 把「多把令牌供给同一个自营账号」写成一行
// **账号级聚合行**（§12.2 的渠道键裁定，XM-0037c）。
//
// 037b 在这里的选择是「整组不入账」，因为收入端点是账号级的、而台账的行是
// 令牌级的，两条显而易见的路都产出错数字：把同一个收入写进 N 行，按渠道 SUM
// 会算 N 遍；只写进其中一行，另外几行变成「成本已知、收入未知」按 §5.1
// 不建行，那几笔成本从台账里消失。
//
// 产品口径已定：**写一行账号级聚合行**——成本取该账号名下全部令牌之和，
// 收入取账号级那一个数，主键第三段用 `account:<own_account_id>` 哨兵。
// 收入只出现一次（不重复计），成本一分不少（不蒸发），按 upstream_account
// 上卷的总数与逐令牌写法完全一致。代价是这几把令牌没有独立的下钻行——
// 那正是 RowsAggregated 单独计数的原因。
//
// **任一令牌的成本读不到，整行的成本就是未知**（不是「已知的那几把之和」）：
// 部分之和会给出一个偏低且看不出偏低的成本，那正是 §5.1 要挡的错数字。
// 与 PlatformBucket.ProfitMinorSum 在覆盖行数不足时返回 nil 是同一条纪律。
func (c *Collector) collectAggregatedAccount(
	ctx context.Context, client metering.ReadClient,
	account UpstreamAccount, group ownAccountGroup,
	day time.Time, dayText string,
	revenues map[string]metering.AccountRevenue, result *CollectResult,
) {
	row := ProfitRow{
		UpstreamAccountID: account.ID,
		BusinessDay:       day,
		BusinessDayTZ:     account.BusinessDayTZ,
		TokenID:           AccountGrainTokenID(group.OwnAccountID),
		AccountID:         group.OwnAccountID,
		PlatformID:        c.resolvePlatform(account, group.Mappings[0]),
		Currency:          account.Currency,
		Source:            c.instanceID,
		RatioSnapshot:     account.RechargeRatio,
	}

	var (
		total     int64
		allKnown  = true
		oldest    time.Time
		anyCost   bool
		firstCost metering.TokenCost
	)
	for _, mapping := range group.Mappings {
		// 即使已经有令牌读失败也继续读完：每一把的失败都该各自进日志，
		// 提前退出会让运维只看得见第一把坏的那个。
		cost, ok := c.readCost(ctx, client, account, mapping, dayText, result)
		if !ok {
			allKnown = false
			continue
		}
		result.Costs = append(result.Costs, cost)
		total += cost.CostMinorUnits
		if !anyCost {
			anyCost, firstCost = true, cost
		}
		// 观测时刻取**最旧**的那个：聚合值的新鲜度由最不新鲜的成员决定，
		// 取最新会让一把刚刷新的令牌替其余几把陈旧的读数背书
		// （同 metering.aggregateSnapshot）。
		if !cost.ObservedAt.IsZero() && (oldest.IsZero() || cost.ObservedAt.Before(oldest)) {
			oldest = cost.ObservedAt
		}
	}

	if allKnown && anyCost {
		row.CostMinor = &total
		row.RatioSnapshot = firstCost.RatioSnapshot
		if firstCost.Currency != "" {
			row.Currency = firstCost.Currency
		}
		if !oldest.IsZero() {
			observed := oldest.UTC()
			row.CostObservedAt = &observed
		}
	}
	if revenue, ok := revenues[group.OwnAccountID]; ok {
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

	c.logger.LogAttrs(ctx, slog.LevelInfo, "finance_revenue_attribution_aggregated",
		slog.String("module", "platform.finance"),
		slog.String("environment", c.environment),
		slog.String("upstream_account_id", account.ID.String()),
		slog.String("own_account_id", group.OwnAccountID),
		slog.String("business_day", dayText),
		slog.Int("token_count", len(group.Mappings)),
		slog.Bool("cost_fully_known", allKnown && anyCost),
		slog.String("hint", "同一自营账号挂了多把上游令牌：写账号级聚合行"+
			"（成本取各令牌之和、收入只计一次），这几把令牌没有独立的下钻行"),
	)

	if c.writeRow(ctx, account, row, dayText, result) {
		result.RowsAggregated++
	}
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

	c.writeRow(ctx, account, row, dayText, result)
}

// writeRow 把一行计量型台账交给 §5.1 的三条分支，并把三种结果分开计数。
//
// 返回是否真的写进去了。令牌级行与账号级聚合行共用它——两者的入账纪律
// 完全相同，分头写两遍只会让其中一处漏掉某一类计数。
func (c *Collector) writeRow(
	ctx context.Context, account UpstreamAccount, row ProfitRow,
	dayText string, result *CollectResult,
) bool {
	switch stored, err := c.ledger.WriteRow(ctx, row); {
	case err == nil:
		result.RowsWritten++
		// 合计取**库里那一行**而不是刚组装的 row：一次只刷新成本侧的写入
		// 会保留今天早些时候已经读到的收入（见 UpdateProfitDailyCost），
		// 于是存进去的那一行可能两侧都全，而手里这个 row 只有一侧。
		// 拿手里的算会把已经入账的收入漏掉。
		result.accumulate(stored)
		return true
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
			slog.String("upstream_token_id", row.TokenID),
			slog.String("business_day", dayText),
			slog.String("error_code", string(connector.KindOf(err))),
		)
	}
	return false
}

// amortizeAccount 给一个订阅型账号算当日摊销并入账（§3.5，XM-0037c）。
//
// 与计量型那条路的三处不同，每一处都有它自己的理由：
//
//  1. **成本不是读来的**——它是登记的付款按天、按账号摊出来的算术。
//     所以这里没有「成本读失败」这种事，只有「当天没有批次覆盖」（未知）。
//  2. **行是账号级的**（token_id 用 `account:` 哨兵）：摊销值本来就没有令牌维度，
//     编一个令牌出来会让下钻点开一个不存在的东西。
//  3. **收入未知时照样建行**（WriteAmortizedRow）：理由见那个方法的注释。
//
// 返回 error 表示这个账号整体没采成（业务日算不出、取不到批次、币种混杂）。
// 「没有批次」「归属不出唯一自营账号」都不是失败——它们是配置还没到位，
// 各自计数并显式报出来，重试一百次也不会变。
func (c *Collector) amortizeAccount(
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
	owner, ok := soleOwnAccount(mappings)
	if !ok {
		// 摊销成本是账号级的一笔钱，必须记在某一个自营账号头上。
		// 零个映射（还没配）与多个不同的自营账号（拆分口径未定）都给不出那个头。
		result.RowsSkippedNoOwner++
		c.logger.LogAttrs(ctx, slog.LevelWarn, "finance_subscription_owner_unresolved",
			slog.String("module", "platform.finance"),
			slog.String("environment", c.environment),
			slog.String("upstream_account_id", account.ID.String()),
			slog.String("business_day", dayText),
			slog.Int("mapping_count", len(mappings)),
			slog.Int("distinct_own_accounts", countDistinctOwners(mappings)),
			slog.String("error_code", "subscription_owner_unresolved"),
			slog.String("hint", "订阅账号需要恰好一个自营账号来承接摊销成本："+
				"零个 = 还没配 token_map；多个 = 一笔订阅摊给几个自营账号的口径未定"),
		)
		return nil
	}

	batches, err := c.subscriptions.ListAmortizableBatches(ctx, account.ID, day)
	if err != nil {
		return fmt.Errorf("取订阅批次: %w", err)
	}
	cost, err := AmortizeDay(batches, day)
	switch {
	case errors.Is(err, ErrNoAmortizableBatch):
		// 成本**未知**，不是 0（见 ErrNoAmortizableBatch 的注释）。
		result.RowsSkippedNoBatch++
		c.logger.LogAttrs(ctx, slog.LevelWarn, "finance_subscription_no_batch",
			slog.String("module", "platform.finance"),
			slog.String("environment", c.environment),
			slog.String("upstream_account_id", account.ID.String()),
			slog.String("own_account_id", owner),
			slog.String("business_day", dayText),
			slog.String("error_code", "subscription_no_covering_batch"),
			slog.String("hint", "当日无覆盖的订阅批次：成本按未知处理（不写 0）。"+
				"补一笔续费批次，或把账号停用"),
		)
		return nil
	case err != nil:
		// 币种混杂之类：这个账号今天算不出成本，计为账号失败。
		return err
	}

	row := ProfitRow{
		UpstreamAccountID: account.ID,
		BusinessDay:       day,
		BusinessDayTZ:     account.BusinessDayTZ,
		TokenID:           AccountGrainTokenID(owner),
		AccountID:         owner,
		PlatformID:        c.resolvePlatform(account, mappings[0]),
		CostMinor:         &cost.CostMinor,
		Currency:          cost.Currency,
		Source:            c.instanceID,
		// §12 拍板：摊销行的 ratio_snapshot 恒为 1——摊销值即成本，未经折算。
		RatioSnapshot: AmortizationRatio,
		// 成本的「观测时刻」就是这一轮算它的时刻：它不是从上游读来的读数，
		// 而是此刻按登记簿现算的。留空会让看板把一个每轮都在刷新的值
		// 显示成「从未采到」（宪法 12 条）。
		CostObservedAt: timePtr(c.now().UTC()),
	}
	if revenue, ok := c.readSubscriptionRevenue(ctx, account, owner, dayText, result); ok {
		minor := revenue.RevenueMinorUnits
		row.RevenueMinor = &minor
		if !revenue.ObservedAt.IsZero() {
			observed := revenue.ObservedAt.UTC()
			row.RevenueObservedAt = &observed
		}
	}

	stored, err := c.ledger.WriteAmortizedRow(ctx, row)
	if err != nil {
		result.RowsFailed++
		result.Partial = true
		if result.FirstError == nil {
			result.FirstError = err
		}
		c.logger.LogAttrs(ctx, slog.LevelError, "finance_ledger_write_failed",
			slog.String("module", "platform.finance"),
			slog.String("environment", c.environment),
			slog.String("upstream_account_id", account.ID.String()),
			slog.String("own_account_id", owner),
			slog.String("business_day", dayText),
			slog.String("error_code", string(connector.KindOf(err))),
		)
		return nil
	}
	result.RowsWritten++
	result.SubscriptionRowsWritten++
	if stored.RevenueMinor == nil {
		result.SubscriptionRowsCostOnly++
	}
	result.accumulate(stored)
	return nil
}

// readSubscriptionRevenue 读订阅账号的账号级使用计费收入（§3.2）。
//
// **收入通道缺席是常态，不是故障**：订阅型渠道的收入源在 v1 常常还没接
// （newapi 的收入 DSN 是单独一片），而没有 base_url 的账号根本建不起客户端。
// 那种情况下这里安静地返回未知——成本照常入账，毛利显示为 NULL。
//
// 不为此打日志：它是一个稳定的配置事实（登记簿里看得见），每 5 分钟
// 重复一次只会把真正的读取失败淹掉。规模由 SubscriptionRowsCostOnly 呈现。
func (c *Collector) readSubscriptionRevenue(
	ctx context.Context, account UpstreamAccount, ownAccountID, dayText string,
	result *CollectResult,
) (metering.AccountRevenue, bool) {
	if strings.TrimSpace(account.BaseURL) == "" {
		return metering.AccountRevenue{}, false
	}
	client, err := c.newClient(ctx, account)
	if err != nil {
		return metering.AccountRevenue{}, false
	}
	revenue, err := client.AccountRevenue(ctx, ownAccountID, dayText)
	if err != nil {
		kind := connector.KindOf(err)
		if kind == connector.KindNotSupported {
			// 这条渠道没有收入端点——同上，安静地当作未知。
			return metering.AccountRevenue{}, false
		}
		// 有端点却读不到：那是真的读取失败，与计量型同一条处理。
		c.logger.LogAttrs(ctx, slog.LevelWarn, "finance_revenue_read_failed",
			slog.String("module", "platform.finance"),
			slog.String("environment", c.environment),
			slog.String("upstream_account_id", account.ID.String()),
			slog.String("own_account_id", ownAccountID),
			slog.String("business_day", dayText),
			slog.String("error_code", string(kind)),
		)
		if result.FirstError == nil {
			result.FirstError = err
		}
		result.Partial = true
		return metering.AccountRevenue{}, false
	}
	result.Revenues = append(result.Revenues, revenue)
	return revenue, true
}

// soleOwnAccount 取「这批映射唯一指向的自营账号」；不唯一时返回 false。
func soleOwnAccount(mappings []TokenMapping) (string, bool) {
	if len(mappings) == 0 {
		return "", false
	}
	owner := mappings[0].OwnAccountID
	for _, m := range mappings[1:] {
		if m.OwnAccountID != owner {
			return "", false
		}
	}
	return owner, true
}

func countDistinctOwners(mappings []TokenMapping) int {
	seen := make(map[string]struct{}, len(mappings))
	for _, m := range mappings {
		seen[m.OwnAccountID] = struct{}{}
	}
	return len(seen)
}

func timePtr(t time.Time) *time.Time { return &t }

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
			return metering.NewNewAPIClient(conn, cfg.Secrets, opts...)
		default:
			return nil, connector.NewError(
				connector.KindNotSupported, "metering.client.real",
				fmt.Errorf("system_type %q 还没有计量取数驱动（official 的成本口径 v1 占位后置，§2.0）",
					account.SystemType))
		}
	}
}
