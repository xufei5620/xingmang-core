package sub2api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 上游（Sub2API）的路由与响应形状——**唯一定义处**。
//
// 形状依据：`K:/sub2api-src`（Go + Gin + ent，模块 github.com/Wei-Shaw/sub2api）
// 的路由注册与 handler/DTO 源码，只读参考、未做任何改动；口径以
// contracts/connectors/sub2api.read.v1.md 为准。
// 上游没有 OpenAPI/Swagger 规格，所以这里的每个字段名都是从源码里逐个抄的。
//
// ⚠️ **未对真实实例验证过**：真实只读账号到位前，本文件的正确性只由本地
// 假上游的契约测试保证。账号到位后按 docs/modules/connector/RUNBOOK.md
// 的「账号到位后的验证清单」逐项核对——尤其是金额单位与业务日边界，
// 那两样错了之后数字看起来完全正常。
//
// 上游改版时，要改的应该只有这一个文件。

const (
	// routeHealth 挂在引擎根上，**不带 /api/v1 前缀，也不需要认证**，
	// 而且不检查数据库/Redis——它是存活探针，不是就绪探针。
	routeHealth = "/health"
	// routeVersion 是唯一的版本来源；上游没有公开的 /api/status 之类端点，
	// 这条路由本身也要 admin 认证。
	routeVersion = "/api/v1/admin/system/version"
	// routeDashboardStats 给用户数与活跃数（以及一个数据水位时间戳）。
	// 它**只回答"今天 + 全量"**，不接受日期参数。
	routeDashboardStats = "/api/v1/admin/dashboard/stats"
	// routeUsers 是余额的唯一来源：上游没有任何余额聚合端点，
	// 想要总额只能翻页自己加（见 aggregateUserBalances 的注释）。
	routeUsers = "/api/v1/admin/users"
	// routePaymentDashboard 给充值收入与订单数的按日序列。
	routePaymentDashboard = "/api/v1/admin/payment/dashboard"
	// routeUsageTrend 给按日的用量成本。
	routeUsageTrend = "/api/v1/admin/dashboard/trend"
	// routeAccounts 是"渠道余额"的实际来源。
	//
	// ⚠️ 命名陷阱：上游的 `Channel`（/admin/channels）是**计费与路由配置**，
	// 没有凭据、没有余额、没有 token 有效性；真正带额度与可用状态的是
	// `Account`（上游供应商账号）。契约里的 ChannelBalance 对应的是后者。
	routeAccounts = "/api/v1/admin/accounts"

	// authHeader 是上游为**程序化访问**准备的专用头，值形如 "admin-"+64 位十六进制。
	//
	// 不用 Authorization: Bearer <JWT>：那条路走的是登录令牌，会过期，
	// 需要拿账号密码去续——一个每 5 分钟跑一次的采集任务不该握着密码。
	authHeader = "X-Api-Key"

	// defaultCurrency 是上游的记账币种。
	//
	// 上游全线用**美元**记账：用户余额 decimal(20,8)、支付金额 decimal(20,2)、
	// 用量成本与账号额度都是 float64 美元，源码注释写的就是"美元"。
	// 这里写死成 CNY 会得到一批**看起来完全正常**的错数字，
	// 所以币种是显式常量 + WithCurrency 可覆盖，不是猜出来的。
	defaultCurrency = "USD"

	// sub2apiBusinessDayLayout 是业务日格式（规格 §5.9）。
	sub2apiBusinessDayLayout = "2006-01-02"

	// userPageSize 是拉用户列表的页大小（上游允许的上限是 1000）。
	userPageSize = 1000
	// maxUserPages 给翻页一个硬上限。
	//
	// 没有上限的翻页在上游用户数暴涨时会把一轮同步拖成几分钟，
	// 而这条链路的预算是 20 秒。到顶还没拉完就**标记为部分数据**——
	// 少报一部分余额是可以被看见的，卡死整轮同步不是。
	maxUserPages = 20

	// accountPageSize 是拉上游账号的页大小。账号数量远小于用户数。
	accountPageSize = 200
	maxAccountPages = 10

	// userBalanceScale 是用户余额在上游的小数位（ent 字段 decimal(20,8)）。
	//
	// 先按 8 位小数把每个人的余额换算成整数累加，最后一次性折成分：
	// 每个人先四舍五入到分再加，几千个人加起来能差出几十分钱。
	userBalanceScale = 8

	// maxPaymentLookbackDays 是支付看板能回溯的天数上限。
	//
	// 上游那条路由只认 `days`（从今天往回数几天），不接受具体日期，
	// 所以查一个很旧的业务日等于让它把中间所有天都算一遍。
	// 超过上限归 not_supported：这是上游的能力边界，不是一次失败的读取。
	maxPaymentLookbackDays = 90
)

// currencyScale 返回币种的最小单位小数位。
//
// 不认识的币种**报错而不是猜 2 位**：猜错的那 100 倍不会有任何症状，
// 只会让所有金额静静地错着（宪法 13 条）。
func currencyScale(code string) (int, error) {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "USD", "CNY", "EUR", "GBP", "HKD", "AUD", "CAD", "SGD":
		return 2, nil
	case "JPY", "KRW", "VND":
		return 0, nil
	default:
		return 0, fmt.Errorf("未登记的币种 %q：最小单位小数位必须显式登记，不能猜", code)
	}
}

// applyAuth 把凭据明文写进请求头。
//
// 这是明文**唯一**的去处，调用点见 client.authorize——那里 Reveal，
// 这里写头，之后明文再没有第三个落点（宪法 7 条）。
func applyAuth(req *http.Request, v secrets.SecretValue) {
	req.Header.Set(authHeader, v.Reveal())
}

// upstreamEnvelope 是 /api/v1 下所有 JSON 响应的统一外壳。
//
// Code 用 rawAmount 而不是 int：上游的**中间件级**错误（401/403）用的是
// 另一套外壳，code 是字符串（"UNAUTHORIZED" 之类）。虽然那些响应会先被
// 状态码判定拦掉，但把类型放宽一点，成本是零，省掉的是一次"解析失败"
// 伪装成"响应格式非法"的误诊。
type upstreamEnvelope struct {
	Code    rawAmount       `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// decode 校验业务码并把 data 解进 out。
//
// 上游的 message 一个字都不进错误链：它是上游写给人看的文本，
// 可能带用户邮箱、订单号这类东西（ADR-004、宪法 7 条）。
func (e upstreamEnvelope) decode(op string, out any) error {
	if code := strings.TrimSpace(string(e.Code)); code != "" && code != "0" {
		return connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("upstream business code %s", code))
	}
	if len(e.Data) == 0 {
		return connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("响应缺少 data 字段"))
	}
	if err := json.Unmarshal(e.Data, out); err != nil {
		return connector.NewError(connector.KindBadResponse, op, err)
	}
	return nil
}

// upstreamPage 是分页外壳。上游没有 has_more，只有 total/pages。
type upstreamPage[T any] struct {
	Items    []T       `json:"items"`
	Total    rawAmount `json:"total"`
	Page     rawAmount `json:"page"`
	PageSize rawAmount `json:"page_size"`
	Pages    rawAmount `json:"pages"`
}

// parseUpstreamTime 解析上游的 RFC3339 时间戳；解析不出返回零值。
//
// 零值是有意义的信号：调用方会退到 Date 头或本地接收时刻，
// 而不是拿当前时间冒充一次观测（规格 §9.1）。
func parseUpstreamTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// ---------------------------------------------------------------------------
// 能力探测
// ---------------------------------------------------------------------------

type routeProbe struct {
	route string
	query url.Values
}

func (p routeProbe) key() string { return p.route + "?" + p.query.Encode() }

type capabilityProbe struct {
	capability registry.Capability
	// probes 全部可达该能力才算可用：一项能力靠两条路由拼出来时
	// （收入来自支付看板、成本来自用量趋势），少一条就等于给不出完整答案。
	probes []routeProbe
}

// implementedCapabilities 是本客户端**实现了**的能力及其支撑路由。
//
// 契约的 ReadCapabilities 有 9 项，这里只有 7 项：groups.read 与
// models.usage_read 没有对应的 ReadClient 方法。契约明写允许返回子集，
// 而声明一项没有方法可以兑现的能力等于说谎。
//
// 探测用 GET 而不是 HEAD：上游是 Gin，GET 路由不会自动响应 HEAD，
// 一发 HEAD 全都是 404——那会让所有能力看起来都"不存在"。
// 所以探测查询一律取最小页（page_size=1），代价可控。
var implementedCapabilities = []capabilityProbe{
	{capability: "sub2api.health.read", probes: []routeProbe{{route: routeHealth}}},
	{capability: "sub2api.service.version_read", probes: []routeProbe{{route: routeVersion}}},
	{capability: "sub2api.users.read", probes: []routeProbe{{route: routeDashboardStats}}},
	{capability: "sub2api.users.balance_read", probes: []routeProbe{{route: routeUsers, query: minimalPageQuery()}}},
	{capability: "sub2api.orders.read", probes: []routeProbe{
		{route: routePaymentDashboard, query: url.Values{"days": {"1"}}},
		{route: routeUsageTrend, query: url.Values{"granularity": {"day"}}},
	}},
	{capability: "sub2api.accounts.read", probes: []routeProbe{{route: routeAccounts, query: minimalPageQuery()}}},
	{capability: "sub2api.channels.balance_read", probes: []routeProbe{{route: routeAccounts, query: minimalPageQuery()}}},
}

func minimalPageQuery() url.Values {
	return url.Values{"page": {"1"}, "page_size": {"1"}}
}

// ---------------------------------------------------------------------------
// 版本与健康
// ---------------------------------------------------------------------------

type upstreamVersion struct {
	version string
	// fingerprintExtra 是除版本号之外能标识"这是哪个上游"的补充信息。
	fingerprintExtra string
}

func (c *client) fetchVersion(ctx context.Context, op string) (upstreamVersion, respMeta, error) {
	var env upstreamEnvelope
	meta, err := c.get(ctx, op, routeVersion, nil, &env)
	if err != nil {
		return upstreamVersion{}, meta, err
	}
	// 上游的版本串来自编译期嵌入的 VERSION 文件，形如 "0.1.133"——
	// **没有 v 前缀**。normalizeVersion 两种都吃，这里不做假设。
	var payload struct {
		Version   string `json:"version"`
		Commit    string `json:"commit"`
		BuildType string `json:"build_type"`
	}
	if err := env.decode(op, &payload); err != nil {
		return upstreamVersion{}, meta, err
	}
	return upstreamVersion{
		version:          strings.TrimSpace(payload.Version),
		fingerprintExtra: strings.TrimSpace(payload.Commit + "|" + payload.BuildType),
	}, meta, nil
}

func (c *client) fetchHealth(ctx context.Context, op string) (bool, respMeta, error) {
	// /health 不带信封也不需要认证，直接是 {"status":"ok"}。
	var payload struct {
		Status string `json:"status"`
	}
	meta, err := c.get(ctx, op, routeHealth, nil, &payload)
	if err != nil {
		return false, meta, err
	}
	return strings.EqualFold(strings.TrimSpace(payload.Status), "ok"), meta, nil
}

// ---------------------------------------------------------------------------
// 用户与余额
// ---------------------------------------------------------------------------

func (c *client) fetchUserStats(ctx context.Context) (UserStats, error) {
	const op = "sub2api.users.read"

	var env upstreamEnvelope
	meta, err := c.get(ctx, op, routeDashboardStats, nil, &env)
	if err != nil {
		return UserStats{}, err
	}
	var stats struct {
		TotalUsers  rawAmount `json:"total_users"`
		ActiveUsers rawAmount `json:"active_users"`
		// StatsUpdatedAt 是上游自己给的数据水位（RFC3339 UTC）——
		// 这条链路上唯一一个"上游说数据是什么时候的"的字段，很宝贵。
		StatsUpdatedAt string `json:"stats_updated_at"`
		// StatsStale 表示上游给的是稍旧的缓存快照。
		// 它**不**映射成 IsPartial：陈旧不等于不完整，陈旧已经由
		// ObservedAt 变旧表达了（规格 §9.1 的状态优先级里两者是不同的态）。
		StatsStale bool `json:"stats_stale"`
	}
	if err := env.decode(op, &stats); err != nil {
		return UserStats{}, err
	}
	totalUsers, err := stats.TotalUsers.count()
	if err != nil {
		return UserStats{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	activeUsers, err := stats.ActiveUsers.count()
	if err != nil {
		return UserStats{}, connector.NewError(connector.KindBadResponse, op, err)
	}

	balances, err := c.aggregateUserBalances(ctx, op)
	if err != nil {
		return UserStats{}, err
	}

	observedAt := meta.observedAt(parseUpstreamTime(stats.StatsUpdatedAt))
	return UserStats{
		Snapshot: Snapshot{
			ObservedAt: observedAt,
			Watermark:  watermark(strings.TrimSpace(stats.StatsUpdatedAt), observedAt),
			IsPartial:  balances.truncated,
		},
		TotalUsers:          totalUsers,
		ActiveUsers:         activeUsers,
		BalanceMinorUnits:   balances.credit,
		OverdraftMinorUnits: balances.overdraft,
		Currency:            c.currency,
	}, nil
}

// balanceTotals 是余额聚合结果，单位是**本币种的最小单位**。
type balanceTotals struct {
	// credit 是所有非负余额之和。
	credit int64
	// overdraft 是所有负余额的绝对值之和（正数）。
	//
	// 上游明确允许余额透支为负（源码注释："允许余额变为负数"），
	// 所以"总余额"必须拆成两个数：把正负混在一起相加会让
	// "1000 人各有 10 块、100 人各欠 100 块"和"没人有钱也没人欠钱"
	// 显示成同一个 0。
	overdraft int64
	// truncated 表示没有拉完全部用户（到达翻页上限）。
	truncated bool
}

// aggregateUserBalances 翻页累加用户余额。
//
// 为什么要翻页：上游**没有任何余额聚合端点**——没有 SUM(balance)，
// 没有欠费用户数，用户列表也不支持 balance<0 过滤（源码里逐个确认过）。
// 想要"总余额"只能自己把每一页加起来。
//
// 代价是每轮同步几次列表请求，收益是这条指标真的存在。
// 上限 maxUserPages 之后停手并标记部分数据，见该常量的注释。
//
// 用户列表里带着邮箱等个人数据：这里**只取 balance 字段**，
// 其余字段解都不解，更不会进日志或落库。
func (c *client) aggregateUserBalances(ctx context.Context, op string) (balanceTotals, error) {
	var (
		out       balanceTotals
		creditSub int64 // 1e-userBalanceScale 单位下的累加值
		debitSub  int64
		fetched   int64
		total     int64
	)

	for page := 1; page <= maxUserPages; page++ {
		var env upstreamEnvelope
		query := url.Values{
			"page":      {strconv.Itoa(page)},
			"page_size": {strconv.Itoa(userPageSize)},
		}
		if _, err := c.get(ctx, op, routeUsers, query, &env); err != nil {
			return balanceTotals{}, err
		}
		var payload upstreamPage[struct {
			Balance rawAmount `json:"balance"`
		}]
		if err := env.decode(op, &payload); err != nil {
			return balanceTotals{}, err
		}
		if page == 1 {
			t, err := payload.Total.count()
			if err != nil {
				return balanceTotals{}, connector.NewError(connector.KindBadResponse, op, err)
			}
			total = t
		}
		for _, item := range payload.Items {
			// 逐个换算成整数再累加，中途不经 float（宪法 13 条）
			v, err := item.Balance.minorUnits(userBalanceScale)
			if err != nil {
				return balanceTotals{}, connector.NewError(connector.KindBadResponse, op, err)
			}
			if v >= 0 {
				creditSub += v
			} else {
				debitSub += -v
			}
		}
		fetched += int64(len(payload.Items))
		if len(payload.Items) == 0 || (total > 0 && fetched >= total) {
			break
		}
		if page == maxUserPages {
			out.truncated = true
		}
	}
	if total > fetched {
		out.truncated = true
	}

	credit, err := rescaleMinorUnits(creditSub, userBalanceScale, c.scale)
	if err != nil {
		return balanceTotals{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	debit, err := rescaleMinorUnits(debitSub, userBalanceScale, c.scale)
	if err != nil {
		return balanceTotals{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	out.credit, out.overdraft = credit, debit
	return out, nil
}

// ---------------------------------------------------------------------------
// 业务日收入与成本
// ---------------------------------------------------------------------------

func (c *client) fetchDailyOrders(ctx context.Context, day time.Time) (OrderSummary, error) {
	const op = "sub2api.orders.read"
	dayText := day.Format(sub2apiBusinessDayLayout)

	revenue, orderCount, revenueFound, err := c.fetchPaymentDay(ctx, op, day, dayText)
	if err != nil {
		return OrderSummary{}, err
	}
	cost, costFound, meta, err := c.fetchUsageCostDay(ctx, op, dayText)
	if err != nil {
		return OrderSummary{}, err
	}

	// 上游这两条路由都不返回数据时间戳，只能退到 Date 头/本地接收时刻。
	// 含义因此从"数据什么时候被观测"滑向"我们什么时候问的"——
	// 文档里写明了，RUNBOOK 的验证清单里也有一条专门核对它。
	observedAt := meta.observedAt(time.Time{})
	return OrderSummary{
		Snapshot: Snapshot{
			ObservedAt: observedAt,
			// 水位带上业务日：光有时间戳分不清"这是哪一天的汇总"
			Watermark: fmt.Sprintf("day:%s@%d", dayText, observedAt.Unix()),
			// 两个来源都没有这一天的条目时才算部分数据：
			// 单边缺失是"那天没有订单/没有用量"的正常表达，
			// 双边都缺才说明我们很可能压根没拿到那天的数据，
			// 而三个 0 看起来和"那天真的是 0"一模一样。
			IsPartial: !revenueFound && !costFound,
		},
		Day:               dayText,
		RevenueMinorUnits: revenue,
		CostMinorUnits:    cost,
		Currency:          c.currency,
		OrderCount:        orderCount,
	}, nil
}

// fetchPaymentDay 取某业务日的充值收入与已支付订单数。
//
// ⚠️ 上游这条路由**只认 days（从今天往回数几天）**，不接受具体日期，
// 而且它按**上游服务器自己配置的时区**（默认 Asia/Shanghai）分日，
// 没有 timezone 参数可以覆盖。业务日边界因此可能与平台的声明不一致，
// 这是账号到位后必须核对的第一梯队事项（见 RUNBOOK 验证清单）。
func (c *client) fetchPaymentDay(
	ctx context.Context, op string, day time.Time, dayText string,
) (revenue, orderCount int64, found bool, err error) {
	days := daysBack(c.clock(), day, c.businessDay)
	if days > maxPaymentLookbackDays {
		return 0, 0, false, connector.NewError(connector.KindNotSupported, op,
			fmt.Errorf("业务日 %s 超出上游 %d 天的回溯窗口", dayText, maxPaymentLookbackDays))
	}

	var env upstreamEnvelope
	if _, err := c.get(ctx, op, routePaymentDashboard, url.Values{"days": {strconv.Itoa(days)}}, &env); err != nil {
		return 0, 0, false, err
	}
	var payload struct {
		DailySeries []struct {
			Date   string    `json:"date"`
			Amount rawAmount `json:"amount"`
			Count  rawAmount `json:"count"`
		} `json:"daily_series"`
	}
	if err := env.decode(op, &payload); err != nil {
		return 0, 0, false, err
	}
	for _, item := range payload.DailySeries {
		if strings.TrimSpace(item.Date) != dayText {
			continue
		}
		amount, err := item.Amount.minorUnits(c.scale)
		if err != nil {
			return 0, 0, false, connector.NewError(connector.KindBadResponse, op, err)
		}
		count, err := item.Count.count()
		if err != nil {
			return 0, 0, false, connector.NewError(connector.KindBadResponse, op, err)
		}
		return amount, count, true, nil
	}
	// 序列里没有这一天：多半是那天没有已支付订单。返回 0 但把 found=false
	// 带出去，让调用方结合成本侧一起判断要不要标记为部分数据。
	return 0, 0, false, nil
}

// fetchUsageCostDay 取某业务日的用量成本。
//
// 成本取 `cost`（标准计费成本）而不是 `actual_cost`（按倍率实际扣减的金额）：
// 契约里的 CostMinorUnits 要的是"这一天的服务成本"，而 actual_cost 表达的是
// "从用户余额里扣走了多少"——后者更接近收入侧，混用会让毛利算反。
func (c *client) fetchUsageCostDay(
	ctx context.Context, op, dayText string,
) (cost int64, found bool, meta respMeta, err error) {
	query := url.Values{
		"start_date":  {dayText},
		"end_date":    {dayText},
		"granularity": {"day"},
		// 显式带上时区，别让上游用它自己的本地时区去切这一天（规格 §5.9：
		// 业务日结时区必须显式声明）。
		"timezone": {c.businessDay.String()},
	}
	var env upstreamEnvelope
	meta, err = c.get(ctx, op, routeUsageTrend, query, &env)
	if err != nil {
		return 0, false, meta, err
	}
	var payload struct {
		Trend []struct {
			Date string    `json:"date"`
			Cost rawAmount `json:"cost"`
		} `json:"trend"`
	}
	if err := env.decode(op, &payload); err != nil {
		return 0, false, meta, err
	}
	for _, item := range payload.Trend {
		if strings.TrimSpace(item.Date) != dayText {
			continue
		}
		v, err := item.Cost.minorUnits(c.scale)
		if err != nil {
			return 0, false, meta, connector.NewError(connector.KindBadResponse, op, err)
		}
		return v, true, meta, nil
	}
	return 0, false, meta, nil
}

// daysBack 算出"从今天往回数几天能覆盖到 day"。
//
// 按业务日时区的**日历日**做差，不用小时数除以 24：跨夏令时的时区里
// 一天不是 24 小时，用小时数算会在切换那天差出一天。
func daysBack(now, day time.Time, loc *time.Location) int {
	today := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day(), 0, 0, 0, 0, loc)
	target := time.Date(day.In(loc).Year(), day.In(loc).Month(), day.In(loc).Day(), 0, 0, 0, 0, loc)
	diff := int(today.Sub(target).Hours()/24 + 0.5)
	if diff < 0 {
		// 未来日期：上游的窗口从今天往回数，覆盖不到，取 1 让它至少回答今天，
		// 结果自然会"没有这一天"，由调用方标记为部分数据。
		return 1
	}
	return diff + 1
}

// ---------------------------------------------------------------------------
// 渠道（上游账号）余额
// ---------------------------------------------------------------------------

func (c *client) fetchChannelBalances(ctx context.Context) ([]ChannelBalance, error) {
	const op = "sub2api.channels.balance_read"

	type accountItem struct {
		ID     rawAmount `json:"id"`
		Name   string    `json:"name"`
		Status string    `json:"status"`
		// 额度字段在上游是 *float64（美元），没配就是 null。
		// rawAmount 把 null 收成空串，于是"没配额度"与"额度为 0"能分开——
		// 这个区分很关键，见下面 remaining 的注释。
		QuotaLimit       rawAmount `json:"quota_limit"`
		QuotaUsed        rawAmount `json:"quota_used"`
		QuotaDailyLimit  rawAmount `json:"quota_daily_limit"`
		QuotaDailyUsed   rawAmount `json:"quota_daily_used"`
		QuotaWeeklyLimit rawAmount `json:"quota_weekly_limit"`
		QuotaWeeklyUsed  rawAmount `json:"quota_weekly_used"`
	}

	var (
		out       []ChannelBalance
		fetched   int64
		total     int64
		skipped   int
		truncated bool
		lastMeta  respMeta
	)

	for page := 1; page <= maxAccountPages; page++ {
		var env upstreamEnvelope
		query := url.Values{
			"page":      {strconv.Itoa(page)},
			"page_size": {strconv.Itoa(accountPageSize)},
		}
		meta, err := c.get(ctx, op, routeAccounts, query, &env)
		if err != nil {
			return nil, err
		}
		lastMeta = meta

		var payload upstreamPage[accountItem]
		if err := env.decode(op, &payload); err != nil {
			return nil, err
		}
		if page == 1 {
			t, err := payload.Total.count()
			if err != nil {
				return nil, connector.NewError(connector.KindBadResponse, op, err)
			}
			total = t
		}
		for _, item := range payload.Items {
			id := strings.TrimSpace(string(item.ID))
			if id == "" {
				// 没有 ID 的渠道无法被引用，也说明我们对响应形状的理解错了
				return nil, connector.NewError(connector.KindBadResponse, op,
					fmt.Errorf("账号条目缺少 id 字段"))
			}
			remaining, ok, err := accountRemaining(item.QuotaLimit, item.QuotaUsed,
				item.QuotaDailyLimit, item.QuotaDailyUsed,
				item.QuotaWeeklyLimit, item.QuotaWeeklyUsed, c.scale)
			if err != nil {
				return nil, connector.NewError(connector.KindBadResponse, op, err)
			}
			if !ok {
				// 没配任何额度上限 = 这个账号根本没有"余额"这个概念。
				// 报成 0 会在看板上变成"这个渠道没钱了"的假警报，
				// 所以宁可不报，并把"少报了几个"如实标记成部分数据。
				skipped++
				continue
			}
			out = append(out, ChannelBalance{
				ChannelID:         id,
				ChannelName:       strings.TrimSpace(item.Name),
				BalanceMinorUnits: remaining,
				Currency:          c.currency,
				// TokenValid 是**能力证据**：上游认为这个账号当前可用。
				// 它不是一次实时的凭据验证——那需要发 POST 去打真实上游，
				// 只读通道上做不到，也不该做（ADR-018 闸 4）。
				TokenValid: strings.EqualFold(strings.TrimSpace(item.Status), "active"),
			})
		}
		fetched += int64(len(payload.Items))
		if len(payload.Items) == 0 || (total > 0 && fetched >= total) {
			break
		}
		if page == maxAccountPages {
			truncated = true
		}
	}
	if total > fetched {
		truncated = true
	}

	observedAt := lastMeta.observedAt(time.Time{})
	snapshot := Snapshot{
		ObservedAt: observedAt,
		Watermark:  watermark("", observedAt),
		IsPartial:  truncated || skipped > 0,
	}
	for i := range out {
		out[i].Snapshot = snapshot
	}
	return out, nil
}

// accountRemaining 算出一个上游账号的剩余额度。
//
// 优先级：总额度 > 日额度 > 周额度。取第一个**配了上限**的口径，
// 而不是把三者相加或取最小：三者语义不同，混算出来的数没有意义。
//
// 返回 ok=false 表示这个账号没配任何额度上限。注意这与"额度剩 0"是两件事：
// 前者是"没有余额这个概念"，后者是"没钱了"，看板上一个该沉默、一个该报警。
//
// ⚠️ 上游的 quota_* 是**管理员配的内部预算上限**，不是从 Anthropic/OpenAI
// 那边查回来的真实账户余额——上游从不调用供应商的计费接口。
// 看板上这个数字回答的是"我们给这个账号的预算还剩多少"。
func accountRemaining(limit, used, dailyLimit, dailyUsed, weeklyLimit, weeklyUsed rawAmount, scale int) (int64, bool, error) {
	for _, pair := range [][2]rawAmount{
		{limit, used},
		{dailyLimit, dailyUsed},
		{weeklyLimit, weeklyUsed},
	} {
		if pair[0].empty() {
			continue
		}
		limitMinor, err := pair[0].minorUnits(scale)
		if err != nil {
			return 0, false, err
		}
		usedMinor, err := pair[1].minorUnits(scale)
		if err != nil {
			return 0, false, err
		}
		return limitMinor - usedMinor, true, nil
	}
	return 0, false, nil
}
