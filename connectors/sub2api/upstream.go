package sub2api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
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
//
// ---------------------------------------------------------------------------
// 接入真实实例前置清单（XM-R013，依据 0.1.183 源码普查）
// ---------------------------------------------------------------------------
//
//  1. **先做合规确认，否则 admin 组一条都读不出来。**
//     0.1.183 在 /api/v1/admin 组（以及 payment 的 admin 子组）挂了
//     AdminComplianceGuard：该 admin 账号没确认过合规声明时，**所有** admin
//     GET 都返 423 Locked，正文 code=ADMIN_COMPLIANCE_ACK_REQUIRED。
//     采集凭据对应的账号必须先 `POST /api/v1/admin/compliance/accept` 一次
//     （人工做，只做一次；平台是只读通道，发不出这个 POST，也不该发）。
//     现状可用 `GET /api/v1/admin/compliance` 查——它自己在 guard 的白名单里。
//     423 在 classifyStatus 里归 auth，见那里的注释。
//
//  2. **这 5 个端点返回的是写死的假数据，永远不要采。**
//     它们 HTTP 200、形状也正常，但 handler 里就是常量（源码注释写着
//     "Return mock data for now"）。采了会得到一批**永远不动且看起来正常**
//     的指标——比读不到更糟：
//
//     /api/v1/admin/users/:id/usage
//     /api/v1/admin/dashboard/realtime
//     /api/v1/admin/redeem-codes/stats
//     /api/v1/admin/groups/:id/stats
//     /api/v1/admin/proxies/:id/stats
//
//     本文件在用的 7 条路由都不在这份名单里；将来加路由前先回源码确认
//     handler 真的查了库。

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

	// routePaymentOrders 是逐笔订单的来源（XM-PAY0，规格 §8.3 收入订单摘要的
	// 明细展开）。
	//
	// ⚠️ 它**没有任何日期过滤参数**（只有 status/order_type/payment_type/
	// keyword/user_id），排序固定 created_at DESC——与 routePaymentDashboard
	// 完全不同：那条按 days-back 取，这条只能翻页自己按 created_at 判断窗口，
	// 见 fetchOrders。
	routePaymentOrders = "/api/v1/admin/payment/orders"

	// routeAccountTodayStatsPrefix/Suffix 拼出单账号今日统计路由
	// GET /api/v1/admin/accounts/:id/today-stats（XM-CHAN-FIELDS0）。
	//
	// ⚠️ 只有这一条 GET 路由，**没有**用上游真正的批量端点
	// POST /api/v1/admin/accounts/today-stats/batch：只读通道的传输层
	// （internal/platform/connector.ReadOnlyTransport）只放行 GET/HEAD，
	// 任何 POST 在请求发出前就会被拒（ADR-018 闸 2/4），这条路线在
	// 只读连接器架构下天然走不通，不是漏接——见 fetchAccountTodayStats
	// 的预算与逐账号调用。
	routeAccountTodayStatsPrefix = "/api/v1/admin/accounts/"
	routeAccountTodayStatsSuffix = "/today-stats"

	// maxTodayStatsAccounts 给「今日统计」逐账号探测一个硬预算（GET-only
	// 约束下只能逐个账号打，见上）。超预算的账号 Today* 留 nil 并标记
	// CoveragePartial，与 accountPageSize/maxAccountPages 同一条纪律：
	// 预算耗尽是可以被看见的降级，不是把一次目录读取拖成几百次上游请求。
	// 数值与 newapi 侧 maxErrorRateChannels 对齐（同一类"逐项探测预算"）。
	maxTodayStatsAccounts = 40

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

	// paymentOrderPageSize 是拉逐笔订单的页大小（上游硬上限 1000，见
	// response.ParsePagination；这里不取上限，避免单次响应过大拖慢单次请求）。
	paymentOrderPageSize = 200
	// maxPaymentOrderPages 给逐笔订单翻页一个硬上限（200×20=4000 笔/次查询）。
	//
	// 到顶还没翻出窗口起点就**标记为部分数据**：少报一部分订单是可以被看见的，
	// 把一次交互式查询拖成几十次上游请求不是——这条端点在 HTTP 请求超时预算
	// （router.go 的 30 秒）内被调用，不是后台 worker 的独立协程。
	maxPaymentOrderPages = 20
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

// fetchVersion 取上游版本串。
//
// **这条路由只给 version 一个字段。** 0.1.133 到 0.1.183 的 handler 都是
//
//	response.Success(c, gin.H{"version": info.CurrentVersion})
//
// 之前这里还解析 commit / build_type 并把它们拼进版本指纹——那两个字段
// 上游从来没发过，于是拼出来的补充信息恒等于常量 "|"，对指纹的贡献是零。
// 一个恒定值参与指纹只会让人以为指纹比实际更有分辨力（XM-R013 清掉）。
// 指纹现在只由「端点主机 + 版本串」构成，见 Version() 里的 fingerprint 调用。
//
// build_type 确实存在，但在 /admin/system/check-updates 那条路由上，
// 而那条路由会**主动去打 GitHub**——采集链路不该顺手触发上游的外网请求。
func (c *client) fetchVersion(ctx context.Context, op string) (string, respMeta, error) {
	var env upstreamEnvelope
	meta, err := c.get(ctx, op, routeVersion, nil, &env)
	if err != nil {
		return "", meta, err
	}
	// 上游的版本串来自编译期嵌入的 VERSION 文件，形如 "0.1.183"——
	// **没有 v 前缀**（0.1.183 源码核对过）。normalizeVersion 两种都吃。
	var payload struct {
		Version string `json:"version"`
	}
	if err := env.decode(op, &payload); err != nil {
		return "", meta, err
	}
	return strings.TrimSpace(payload.Version), meta, nil
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

	pay, err := c.fetchPaymentDay(ctx, op, day, dayText)
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
			//
			// currencyGap 是另一条独立的理由：那天有收入，但落在本契约
			// 装不下的币种里，见 paymentDay.currencyGap。
			IsPartial: (!pay.found && !costFound) || pay.currencyGap,
		},
		Day:               dayText,
		RevenueMinorUnits: pay.revenue,
		CostMinorUnits:    cost,
		Currency:          c.currency,
		OrderCount:        pay.orderCount,
	}, nil
}

// paymentDay 是支付看板对某个业务日的回答。
//
// 用结构体而不是四个返回值：自从金额按币种分桶之后，"这一天读到了什么"
// 有三个彼此独立的维度（有没有这一天、这一天的合约币种金额、有没有
// 装不下的币种），挤在返回值列表里靠位置区分很容易接错。
type paymentDay struct {
	// revenue 是**合约币种**那一桶的金额，整数最小货币单位。
	revenue int64
	// orderCount 是这一天的已支付订单数。
	//
	// ⚠️ 它是**跨币种**的：上游按天累加订单数时不分币种（daily_series 的
	// count 就是那天所有已支付订单的条数）。所以多币种的日子里
	// orderCount 覆盖的范围比 revenue 大，两者不构成"均价"。
	orderCount int64
	// found 表示序列里有这一天（哪怕金额是空桶）。
	found bool
	// currencyGap 表示这一天有收入落在合约币种之外的桶里，
	// 那部分金额被丢掉了——必须体现成部分数据，见 fetchPaymentDay。
	currencyGap bool
}

// currencyBuckets 是 0.1.183 起支付看板的金额形状：币种码 → 金额。
//
// 上游 0.1.183 把 DashboardStats/DailyStats 的 amount 从标量 float64 改成了
// `CurrencyAmounts = map[string]float64`（源码注释："Amounts in different
// currencies must never be added together"）。币种码经
// payment.NormalizePaymentCurrency 规范化，一定是 3 位大写 ISO 4217。
type currencyBuckets map[string]rawAmount

// legacyScalarCurrency 是"上游给的是标量、没说币种"这一桶的键。
//
// 空串不可能与真实币种码相撞：上游的 NormalizePaymentCurrency 只放行
// 恰好 3 位的大写 ISO 4217 字母码。
const legacyScalarCurrency = ""

// UnmarshalJSON 同时吃两种形状：0.1.183 的币种 map 与更早版本的标量。
//
// 为什么要向后兼容而不是只认新形状：兼容矩阵登记的是整条 `0.1` 线，
// 而金额改成 map 是这条线中间某个补丁版的事（具体哪一版无从考证——
// 手头的上游源码是 depth=1 的浅克隆，没有历史可二分）。只认 map 等于
// 把"0.1.183 上炸"换成"0.1.133 上炸"，一个阻塞换另一个阻塞。
//
// 标量落进无币种桶而不是直接当成合约币种：币种信息是**上游没给的**，
// 这个事实要保留到 pick 里再按运维声明去解释，而不是在解码这一步就
// 假装上游说过。
func (b *currencyBuckets) UnmarshalJSON(raw []byte) error {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		*b = nil
		return nil
	}
	if strings.HasPrefix(text, "{") {
		var m map[string]rawAmount
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		*b = m
		return nil
	}
	var scalar rawAmount
	if err := scalar.UnmarshalJSON(raw); err != nil {
		return err
	}
	*b = currencyBuckets{legacyScalarCurrency: scalar}
	return nil
}

// pick 取出 currency 那一桶。
//
// 第三个返回值说明"除它以外还有别的币种"——那意味着这一天有我们**看得见
// 却报不出来**的收入。契约的 OrderSummary 只有一个 Currency 字段，
// 跨币种相加是明令禁止的（规格 §5.9、上游源码同样的纪律），
// 所以只能报一桶 + 如实标记不完整，不能悄悄合计。
func (b currencyBuckets) pick(currency string) (rawAmount, bool, bool) {
	// 旧上游的标量：那时 daily_series 的 amount 就是"当天全部已支付订单
	// 之和"，一个币种字段都没有。唯一的币种信息来自运维的 WithCurrency
	// 声明，按它解释即可——这正是 0.1.183 之前既有的（也是唯一可能的）口径。
	if v, ok := b[legacyScalarCurrency]; ok && len(b) == 1 {
		return v, true, false
	}

	want := strings.ToUpper(strings.TrimSpace(currency))
	var (
		got   rawAmount
		found bool
		other bool
	)
	for code, amount := range b {
		if strings.ToUpper(strings.TrimSpace(code)) == want {
			got, found = amount, true
			continue
		}
		other = true
	}
	return got, found, other
}

// fetchPaymentDay 取某业务日的充值收入与已支付订单数。
//
// ⚠️ 上游这条路由**只认 days（从今天往回数几天）**，不接受具体日期，
// 而且它按**上游服务器自己配置的时区**（默认 Asia/Shanghai）分日，
// 没有 timezone 参数可以覆盖。业务日边界因此可能与平台的声明不一致，
// 这是账号到位后必须核对的第一梯队事项（见 RUNBOOK 验证清单）。
//
// ⚠️ **金额按币种分桶（0.1.183 破坏性变更，XM-R013 修）。**
// 旧代码把 amount 解进单个 rawAmount，在 0.1.183 上会拿到
// `{"CNY":100.5}` 这个**对象的字面量文本**，decimalToMinorUnits 一看
// 全是非数字字符就报错 → 整条 DailyOrders 变成 bad_response。
//
// 为什么空实例联调测不出来：0.1.183 的 buildDailySeries 对**没有已支付订单**
// 的那天给的是 `"amount":{}`（空 map），空 map 在旧代码里被 rawAmount 收成
// 空串、minorUnits 按 0 处理——一路全绿。金额桶只有在**真的有人充了钱**
// 那天才非空，所以这个 bug 会精准地等到第一笔真实收入才炸。
// 回归测试因此必须构造"某天有已支付订单且金额是币种 map"的场景，
// 见 TestRealClientReadsCurrencyBucketedRevenue。
func (c *client) fetchPaymentDay(
	ctx context.Context, op string, day time.Time, dayText string,
) (paymentDay, error) {
	days := daysBack(c.clock(), day, c.businessDay)
	if days > maxPaymentLookbackDays {
		return paymentDay{}, connector.NewError(connector.KindNotSupported, op,
			fmt.Errorf("业务日 %s 超出上游 %d 天的回溯窗口", dayText, maxPaymentLookbackDays))
	}

	var env upstreamEnvelope
	if _, err := c.get(ctx, op, routePaymentDashboard, url.Values{"days": {strconv.Itoa(days)}}, &env); err != nil {
		return paymentDay{}, err
	}
	var payload struct {
		DailySeries []struct {
			Date   string          `json:"date"`
			Amount currencyBuckets `json:"amount"`
			Count  rawAmount       `json:"count"`
		} `json:"daily_series"`
	}
	if err := env.decode(op, &payload); err != nil {
		return paymentDay{}, err
	}
	for _, item := range payload.DailySeries {
		if strings.TrimSpace(item.Date) != dayText {
			continue
		}
		count, err := item.Count.count()
		if err != nil {
			return paymentDay{}, connector.NewError(connector.KindBadResponse, op, err)
		}
		out := paymentDay{orderCount: count, found: true}

		amount, ok, other := item.Amount.pick(c.currency)
		out.currencyGap = other
		if !ok {
			// 合约币种那一桶不存在。**不报错也不换一桶顶上**：
			// 报错会让整天的成本侧也一起读不到（其实是好的），换一桶
			// 则会把另一个币种的数字当成本币种的收入——那是"看起来完全
			// 正常的错数字"，最难查。
			//
			// 所以收入留 0 并标记为部分数据（规格 §9.1：部分数据可见）。
			// 桶里一个币种都没有（`"amount":{}`）时 other 也是 false，
			// 这是"那天没人充值"的正常表达，不该标记为不完整。
			return out, nil
		}
		v, err := amount.minorUnits(c.scale)
		if err != nil {
			return paymentDay{}, connector.NewError(connector.KindBadResponse, op, err)
		}
		out.revenue = v
		return out, nil
	}
	// 序列里没有这一天：多半是那天没有已支付订单。返回 0 但把 found=false
	// 带出去，让调用方结合成本侧一起判断要不要标记为部分数据。
	return paymentDay{}, nil
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

func (c *client) fetchChannelDirectory(ctx context.Context) (ManagedChannelDirectory, error) {
	const op = "sub2api.channels.read"

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

		// XM-CHAN-FIELDS0：以下字段全部来自同一次 /api/v1/admin/accounts
		// 列表响应（AccountWithConcurrency 内嵌 *dto.Account 后展平的同一层
		// JSON 对象），不需要额外请求。字段名与类型逐个对照
		// K:/sub2api-src backend/internal/handler/dto/types.go:196-342 与
		// backend/internal/handler/admin/account_handler.go:191-200。
		Type               string     `json:"type"`
		Platform           string     `json:"platform"`
		Concurrency        int        `json:"concurrency"`
		LoadFactor         *int       `json:"load_factor"`
		Priority           int        `json:"priority"`
		RateMultiplier     rawAmount  `json:"rate_multiplier"`
		Schedulable        bool       `json:"schedulable"`
		LastUsedAt         *time.Time `json:"last_used_at"`
		CreatedAt          time.Time  `json:"created_at"`
		ExpiresAt          *int64     `json:"expires_at"` // 上游是 unix 秒，不是 RFC3339（与其余时间字段不同）
		SessionWindowEnd   *time.Time `json:"session_window_end"`
		WindowCostLimit    rawAmount  `json:"window_cost_limit"`
		CurrentConcurrency int        `json:"current_concurrency"`
		CurrentWindowCost  rawAmount  `json:"current_window_cost"`
		Proxy              *struct {
			// Name 是人工填写的展示名；ent schema 明确不含凭据内容，
			// 上游的 mapper 也从不把 Password 拷进这个响应（见契约文档的
			// proxy 字段来源说明）。绝不解 Host/Username/Password。
			Name string `json:"name"`
		} `json:"proxy"`
		// Extra 只用来找 upstream_billing_probe 这一个键；不整体转发、
		// 不落库、不进日志——它是上游「未来响应字段」的沙箱字段，
		// 内容未经审查，仅按已知子键读取（见 upstreamMultiplierFromExtra）。
		Extra map[string]any `json:"extra"`
	}

	var (
		out             []ManagedChannel
		fetched         int64
		total           int64
		coveragePartial bool
		truncated       bool
		lastMeta        respMeta
	)

	for page := 1; page <= maxAccountPages; page++ {
		var env upstreamEnvelope
		query := url.Values{
			"page":      {strconv.Itoa(page)},
			"page_size": {strconv.Itoa(accountPageSize)},
		}
		meta, err := c.get(ctx, op, routeAccounts, query, &env)
		if err != nil {
			return ManagedChannelDirectory{}, err
		}
		lastMeta = meta

		var payload upstreamPage[accountItem]
		if err := env.decode(op, &payload); err != nil {
			return ManagedChannelDirectory{}, err
		}
		if page == 1 {
			t, err := payload.Total.count()
			if err != nil {
				return ManagedChannelDirectory{}, connector.NewError(connector.KindBadResponse, op, err)
			}
			total = t
		}
		for _, item := range payload.Items {
			id := strings.TrimSpace(string(item.ID))
			if id == "" {
				// 没有 ID 的渠道无法被引用，也说明我们对响应形状的理解错了
				return ManagedChannelDirectory{}, connector.NewError(connector.KindBadResponse, op,
					fmt.Errorf("账号条目缺少 id 字段"))
			}
			remaining, ok, err := accountRemaining(item.QuotaLimit, item.QuotaUsed,
				item.QuotaDailyLimit, item.QuotaDailyUsed,
				item.QuotaWeeklyLimit, item.QuotaWeeklyUsed, c.scale)
			if err != nil {
				return ManagedChannelDirectory{}, connector.NewError(connector.KindBadResponse, op, err)
			}
			var balance *int64
			if !ok {
				coveragePartial = true
			} else {
				value := remaining
				balance = &value
			}

			// XM-CHAN-FIELDS0：capacity——used 是实时并发（同一次响应自带，
			// 无需二次请求），limit 与上游 EffectiveLoadFactor() 同一条判据：
			// load_factor 已配置且 >0 时取它，否则取 concurrency
			// （K:/sub2api-src backend/internal/service/account.go:168-179）。
			used := item.CurrentConcurrency
			limit := item.Concurrency
			if item.LoadFactor != nil && *item.LoadFactor > 0 {
				limit = *item.LoadFactor
			}
			usedI64, limitI64 := int64(used), int64(limit)

			priority := int64(item.Priority)
			schedulable := item.Schedulable

			// RateMultiplier 的上游列类型是 decimal(10,4)（ent account.go:110-114），
			// 按 4 位小数精确解析后再换算到 ppm 精度（scale 6），全程不经
			// float64——比例属于宪法 13 条"比例使用 Decimal"的范围。
			var rateMultiplierPPM *int64
			if scaled4, err := parseScaledAmount(item.RateMultiplier, 4); err != nil {
				return ManagedChannelDirectory{}, connector.NewError(connector.KindBadResponse, op, err)
			} else if scaled4 != nil {
				ppm, err := rescaleMinorUnits(*scaled4, 4, 6)
				if err != nil {
					return ManagedChannelDirectory{}, connector.NewError(connector.KindBadResponse, op, err)
				}
				rateMultiplierPPM = &ppm
			}

			var expiresAt *time.Time
			if item.ExpiresAt != nil {
				v := time.Unix(*item.ExpiresAt, 0).UTC()
				expiresAt = &v
			}
			createdAt := item.CreatedAt.UTC()

			var proxyLabel *string
			if item.Proxy != nil && strings.TrimSpace(item.Proxy.Name) != "" {
				v := strings.TrimSpace(item.Proxy.Name)
				proxyLabel = &v
			}

			kind := classifyAccountKind(strings.TrimSpace(item.Type))

			// usage_window 只对 kind=="subscription" 的账号计算，且只用
			// 同一次响应里已有的字段（current_window_cost / window_cost_limit /
			// session_window_end），不对每个订阅账号再打一次
			// /api/v1/admin/accounts/:id/usage——那条会代理到 Anthropic 自己的
			// 实时用量接口，对目录读取做未设预算的按账号扇出不合适，
			// 见 contracts/connectors/sub2api.channel-catalog.v3.md 的取舍说明。
			var usedRatioPPM *int64
			var resetsAt *time.Time
			if kind != nil && *kind == channelKindSubscription {
				// window_cost_limit / current_window_cost 都是账号计费币种下的
				// 美元金额，按连接器配置的币种小数位精确解析成整数分（与本文件
				// 其余金额字段同一条路径），比例再从两个整数分值算出——全程不经
				// float64，包括比例本身（宪法 13 条）。
				windowCostLimit, err := parseScaledAmount(item.WindowCostLimit, c.scale)
				if err != nil {
					return ManagedChannelDirectory{}, connector.NewError(connector.KindBadResponse, op, err)
				}
				currentWindowCost, err := parseScaledAmount(item.CurrentWindowCost, c.scale)
				if err != nil {
					return ManagedChannelDirectory{}, connector.NewError(connector.KindBadResponse, op, err)
				}
				if windowCostLimit != nil && *windowCostLimit > 0 && currentWindowCost != nil {
					ppm, err := costRatioPPM(*currentWindowCost, *windowCostLimit)
					if err != nil {
						return ManagedChannelDirectory{}, connector.NewError(connector.KindBadResponse, op, err)
					}
					usedRatioPPM = &ppm
				}
				if item.SessionWindowEnd != nil {
					v := item.SessionWindowEnd.UTC()
					resetsAt = &v
				}
			}

			out = append(out, ManagedChannel{
				ChannelID:         id,
				Name:              strings.TrimSpace(item.Name),
				Status:            strings.TrimSpace(item.Status),
				BalanceMinorUnits: balance,
				Currency:          c.currency,

				Kind:                    kind,
				Vendor:                  optionalString(item.Platform),
				CapacityUsed:            &usedI64,
				CapacityLimit:           &limitI64,
				SchedulingEnabled:       &schedulable,
				SchedulingPriority:      &priority,
				UsageWindowUsedRatioPPM: usedRatioPPM,
				UsageWindowResetsAt:     resetsAt,
				ProxyLabel:              proxyLabel,
				RateMultiplierPPM:       rateMultiplierPPM,
				UpstreamMultiplierPPM:   upstreamMultiplierPPMFromExtra(item.Extra),
				LastUsedAt:              item.LastUsedAt,
				CreatedAt:               &createdAt,
				ExpiresAt:               expiresAt,
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
		IsPartial:  truncated || coveragePartial,
	}
	for i := range out {
		out[i].Snapshot = snapshot
	}

	// XM-CHAN-FIELDS0：今日统计逐账号预算探测，见 maxTodayStatsAccounts 的
	// 注释——只读通道走不通批量端点，只能预算内逐个打 GET。预算耗尽是
	// 已预期的降级，标进 coveragePartial；真正的请求失败（非
	// KindNotSupported）仍然让整次目录读取失败，与本文件其余字段
	// （aggregateUserBalances 等）同一条「预算内降级、请求失败即报错」纪律。
	if todayPartial, err := c.fetchAccountTodayStats(ctx, op, out); err != nil {
		return ManagedChannelDirectory{}, err
	} else if todayPartial {
		coveragePartial = true
		snapshot.IsPartial = true
		for i := range out {
			out[i].Snapshot = snapshot
		}
	}

	reported := total
	return ManagedChannelDirectory{
		Snapshot: snapshot,
		Completeness: DirectoryCompleteness{
			Complete:      !truncated && fetched == total,
			Truncated:     truncated,
			ReportedCount: &reported,
			FetchedCount:  fetched,
			Evidence:      "reported_count",
		},
		CoveragePartial: coveragePartial,
		Items:           out,
	}, nil
}

func (c *client) fetchChannelBalances(ctx context.Context) ([]ChannelBalance, error) {
	directory, err := c.fetchChannelDirectory(ctx)
	if err != nil {
		return nil, err
	}
	return LegacyChannelBalances(directory), nil
}

// todayStatsPriority 给出探测「今日统计」的账号顺序：可调度（schedulable）的
// 账号优先——预算有限时，先给运维正在实际服务的账号一个今日数字，比先测一个
// 早就被停用的账号更有用。同组内保持读到的顺序，结果稳定可复现（与 newapi
// errorRatePriority 同一条设计理由）。
func todayStatsPriority(items []ManagedChannel) []int {
	order := make([]int, len(items))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		as := items[order[a]].SchedulingEnabled != nil && *items[order[a]].SchedulingEnabled
		bs := items[order[b]].SchedulingEnabled != nil && *items[order[b]].SchedulingEnabled
		return as && !bs
	})
	return order
}

// fetchAccountTodayStats 在预算内逐账号读取今日统计并就地写回 items 的
// Today* 字段。返回值表示是否因预算或「上游没有这个能力」而只覆盖了部分账号
// ——那种情况下调用方把它并入 CoveragePartial，而不是让整次目录读取失败。
//
// 真正的请求失败（网络、鉴权、非法响应……）仍然整体报错返回：与
// aggregateUserBalances/fetchOrders 同一条纪律——预算耗尽是可预期的降级，
// 请求本身失败不是。
func (c *client) fetchAccountTodayStats(ctx context.Context, op string, items []ManagedChannel) (bool, error) {
	if len(items) == 0 {
		return false, nil
	}
	partial := len(items) > maxTodayStatsAccounts
	budget := maxTodayStatsAccounts

	for _, idx := range todayStatsPriority(items) {
		if budget <= 0 {
			break
		}
		budget--

		stats, err := c.fetchOneAccountTodayStats(ctx, op, items[idx].ChannelID)
		if err != nil {
			if connector.KindOf(err) == connector.KindNotSupported {
				// 这个上游版本没有 today-stats 端点：不是这一个账号的问题，
				// 后面的账号也不会有——停止探测但不报错，整体标记部分数据。
				return true, nil
			}
			return false, err
		}
		items[idx].TodayRequests = &stats.requests
		items[idx].TodayCostMinorUnits = &stats.costMinorUnits
		currency := c.currency
		scale := c.scale
		items[idx].TodayCurrency = &currency
		items[idx].TodayScale = &scale
		// TodaySuccessRate 恒为 nil：WindowStats 不带成功率或失败数
		// （K:/sub2api-src backend/internal/service/account_usage_service.go:
		// 139-145，逐字段核对过，无该字段），不是本次采集遗漏。
	}
	return partial, nil
}

type accountTodayStats struct {
	requests       int64
	costMinorUnits int64
}

// fetchOneAccountTodayStats 读取单个账号的今日统计
// （GET /api/v1/admin/accounts/:id/today-stats）。Cost 走 rawAmount 精确解析，
// 不经 strconv.ParseFloat（本文件金额字段统一纪律）。
func (c *client) fetchOneAccountTodayStats(ctx context.Context, op, accountID string) (accountTodayStats, error) {
	var env upstreamEnvelope
	route := routeAccountTodayStatsPrefix + url.PathEscape(accountID) + routeAccountTodayStatsSuffix
	if _, err := c.get(ctx, op, route, nil, &env); err != nil {
		return accountTodayStats{}, err
	}
	var payload struct {
		Requests rawAmount `json:"requests"`
		Cost     rawAmount `json:"cost"`
	}
	if err := env.decode(op, &payload); err != nil {
		return accountTodayStats{}, err
	}
	requests, err := payload.Requests.count()
	if err != nil {
		return accountTodayStats{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	cost, err := payload.Cost.minorUnits(c.scale)
	if err != nil {
		return accountTodayStats{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	return accountTodayStats{requests: requests, costMinorUnits: cost}, nil
}

// optionalString 把可能为空的字符串字段转成 *string：空串按「上游没给」处理，
// 返回 nil 而不是一个空字符串指针——与本文件其余可空字段同一条纪律。
func optionalString(s string) *string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// parseScaledAmount 把十进制文本按 scale 位小数精确转成整数（scale 位小数
// 的定点表示），复用 decimalToMinorUnits 的整数换算路径而不经
// strconv.ParseFloat 直接解析十进制文本——避免十进制转二进制浮点在解析这
// 一步就引入误差。是 rawAmount.minorUnits 的可空版本：区分「上游没给」
// （nil）与「上游给的恰好是 0」（指向 0 的指针），minorUnits 本身两者都
// 收成 0，分不开。
func parseScaledAmount(a rawAmount, scale int) (*int64, error) {
	if a.empty() {
		return nil, nil
	}
	scaled, err := decimalToMinorUnits(string(a), scale)
	if err != nil {
		return nil, err
	}
	return &scaled, nil
}

// costRatioPPM 把两个整数分值的比例换算成 ppm（百万分之一）整数，
// 全程整数运算，不经 float64（宪法 13 条：比例使用 Decimal）。
//
// 与 newapi 的 ratePPM 不同之处：这里**允许分子大于分母**（当前窗口费用
// 超出费用上限是真实场景——那正是上游 window_cost_limit 这个字段存在的
// 意义之一，超限之后才会触发自动暂停），返回值可以合法地超过
// 1_000_000，调用方不该把它当成百分比封顶在 100% 的信号。
func costRatioPPM(numeratorMinorUnits, denominatorMinorUnits int64) (int64, error) {
	if numeratorMinorUnits < 0 || denominatorMinorUnits <= 0 {
		return 0, fmt.Errorf("窗口费用比例的分子分母非法(%d/%d): %w",
			numeratorMinorUnits, denominatorMinorUnits, errAmountFormat)
	}
	const ppmScale = 1_000_000
	if numeratorMinorUnits > maxInt64/ppmScale {
		return 0, fmt.Errorf("窗口费用比例分子 %d 放大 %d 倍后溢出 int64: %w",
			numeratorMinorUnits, ppmScale, errAmountFormat)
	}
	// 四舍五入而不是截断，理由与本包金额换算的一贯纪律相同。
	return (numeratorMinorUnits*ppmScale + denominatorMinorUnits/2) / denominatorMinorUnits, nil
}

// upstreamMultiplierPPMFromExtra 从账号的 extra 字段里找上游计费探测写入的
// resolved_rate_multiplier（K:/sub2api-src backend/internal/service/
// upstream_billing_probe.go:104-146 的 UpstreamBillingProbeSnapshot.Data），
// 换算成 ppm 整数。extra 是「已知子键」而非整体转发的沙箱字段（同文件注释：
// "Data is kept as a sanitized map so future response fields do not require
// a database change"），只在账号开启过上游计费探测时才会有值，多数账号会是
// nil——这不是缺陷，是这项能力本身的覆盖范围，契约文档里有专门说明。
//
// ⚠️ 精度上限：这里读到的是 extra 整体解码成 map[string]any 之后的原生
// float64（encoding/json 把任意数值字面量解进 any 都会变成 float64，无法
// 绕开），换算到 ppm 时四舍五入——这是 extra 这个「上游沙箱字段」本身的精度
// 上限，不是本函数新引入的误差，也不参与本包任何货币汇总，仅用于展示。
func upstreamMultiplierPPMFromExtra(extra map[string]any) *int64 {
	probe, ok := extra["upstream_billing_probe"].(map[string]any)
	if !ok {
		return nil
	}
	data, ok := probe["data"].(map[string]any)
	if !ok {
		return nil
	}
	v, ok := data["resolved_rate_multiplier"].(float64)
	if !ok {
		return nil
	}
	ppm := int64(math.Round(v * 1_000_000))
	return &ppm
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

// ---------------------------------------------------------------------------
// 逐笔订单与按日资金汇总（XM-PAY0）
// ---------------------------------------------------------------------------

// adminPaymentOrderItem 是 /api/v1/admin/payment/orders 列表元素里本包用得到
// 的字段（K:/sub2api-src backend/internal/handler/admin/payment_handler.go
// 的 AdminPaymentOrderResult，只读核对，未修改上游）。
//
// 不解 user_name/payment_trade_no：user_name 是姓名，展示用只需要打码邮箱；
// payment_trade_no 是网关自己的对账号，本片的 UpstreamOrderRef 用
// out_trade_no（上游自己生成、后台可查的业务单号），见 payments.go 的
// Order.UpstreamOrderRef 注释。
type adminPaymentOrderItem struct {
	ID           rawAmount `json:"id"`
	UserID       rawAmount `json:"user_id"`
	UserEmail    string    `json:"user_email"`
	Amount       rawAmount `json:"amount"`
	PayAmount    rawAmount `json:"pay_amount"`
	Currency     string    `json:"currency"`
	OutTradeNo   string    `json:"out_trade_no"`
	PaymentType  string    `json:"payment_type"`
	Status       string    `json:"status"`
	RefundAmount rawAmount `json:"refund_amount"`
	CreatedAt    string    `json:"created_at"`
}

// orderRow 是 fetchOrders 的内部产出：公开的 Order 字段之外，多带
// RefundAmountMinorUnits 与 FeeMinorUnits——公开的 Order 类型故意不含这两个
// 字段（见 payments.go 的 DailyPaymentSummary 注释：逐笔明细不展开退款金额，
// 只在按日汇总里用到），但 DailyPaymentSummary 的"refunded"桶与手续费合计
// 需要它们，所以内部多带一份，不为了传两个数就去解两遍上游响应。
type orderRow struct {
	Order
	RefundAmountMinorUnits int64
	FeeMinorUnits          int64
}

// fetchOrders 翻页读取 [from, to] 闭区间（按 created_at）内、可选按 status
// 过滤的全部订单。
//
// 上游按 created_at DESC 排序且**没有日期过滤参数**（见 routePaymentOrders），
// 所以从第一页往后翻，一旦遇到 created_at < from 的订单就可以停手——不需要
// 像 newapi 的 fetchRechargeDay 那样留回溯余量：那边的排序键（id）与窗口键
// （complete_time）是两个不同的字段，可能不同步；这里排序键与窗口键都是
// created_at，天然同步。
//
// status 非空时把它交给上游做服务端过滤（该端点原生支持 status 参数），
// 而不是拉全量自己筛——按状态查是本端点最常见的用法，服务端过滤能把翻页量
// 降到最低。
func (c *client) fetchOrders(
	ctx context.Context, op string, from, to time.Time, status string,
) ([]orderRow, respMeta, bool, error) {
	var (
		rows      []orderRow
		lastMeta  respMeta
		fetched   int64
		reported  int64
		truncated bool
	)

	query := url.Values{"page_size": {strconv.Itoa(paymentOrderPageSize)}}
	if status != "" {
		query.Set("status", status)
	}

	for page := 1; page <= maxPaymentOrderPages; page++ {
		pageQuery := url.Values{}
		for k, v := range query {
			pageQuery[k] = v
		}
		pageQuery.Set("page", strconv.Itoa(page))

		var env upstreamEnvelope
		meta, err := c.get(ctx, op, routePaymentOrders, pageQuery, &env)
		if err != nil {
			return nil, meta, false, err
		}
		lastMeta = meta

		var payload upstreamPage[adminPaymentOrderItem]
		if err := env.decode(op, &payload); err != nil {
			return nil, meta, false, err
		}
		if page == 1 {
			t, err := payload.Total.count()
			if err != nil {
				return nil, meta, false, connector.NewError(connector.KindBadResponse, op, err)
			}
			reported = t
		}

		reachedFloor := false
		for _, item := range payload.Items {
			createdAt := parseUpstreamTime(item.CreatedAt)
			if !createdAt.IsZero() && createdAt.Before(from) {
				// 按 created_at DESC 排序，走到这里说明后面只会更早。
				reachedFloor = true
				continue
			}
			if !createdAt.IsZero() && createdAt.After(to) {
				// 窗口终点之后的订单（例如 to 早于"现在"时，第一页可能
				// 全部落在窗口之后）：跳过但继续翻页，直到进入窗口或触底。
				continue
			}

			row, err := decodeOrderRow(item, createdAt, c.scale)
			if err != nil {
				return nil, meta, false, connector.NewError(connector.KindBadResponse, op, err)
			}
			rows = append(rows, row)
		}

		fetched += int64(len(payload.Items))
		if reachedFloor || len(payload.Items) == 0 || (reported > 0 && fetched >= reported) {
			break
		}
		if page == maxPaymentOrderPages {
			truncated = true
		}
	}
	return rows, lastMeta, truncated, nil
}

// decodeOrderRow 把上游的原始订单 JSON 换算成 orderRow：金额一律走
// rawAmount.minorUnits，全程不经过 float（宪法 13 条）。
func decodeOrderRow(item adminPaymentOrderItem, createdAt time.Time, scale int) (orderRow, error) {
	id, err := item.ID.count()
	if err != nil {
		return orderRow{}, err
	}
	userID, err := item.UserID.count()
	if err != nil {
		return orderRow{}, err
	}
	amount, err := item.Amount.minorUnits(scale)
	if err != nil {
		return orderRow{}, err
	}
	payAmount, err := item.PayAmount.minorUnits(scale)
	if err != nil {
		return orderRow{}, err
	}
	refundAmount, err := item.RefundAmount.minorUnits(scale)
	if err != nil {
		return orderRow{}, err
	}

	currency := strings.ToUpper(strings.TrimSpace(item.Currency))
	fee := payAmount - amount
	if fee < 0 {
		// pay_amount 理论上恒 >= amount（手续费不会是负的）。上游若出现
		// 反常数据，宁可把手续费钉在 0 也不要报出一个负手续费——负数会让
		// FeeMinorUnits 的求和悄悄把别的订单的手续费抵消掉。
		fee = 0
	}

	return orderRow{
		Order: Order{
			OrderID:          strconv.FormatInt(id, 10),
			CreatedAt:        createdAt,
			Status:           strings.ToUpper(strings.TrimSpace(item.Status)),
			AmountMinorUnits: amount,
			Currency:         currency,
			Method:           strings.TrimSpace(item.PaymentType),
			UserRef:          maskUserRef(item.UserEmail, userID),
			UpstreamOrderRef: strings.TrimSpace(item.OutTradeNo),
		},
		RefundAmountMinorUnits: refundAmount,
		FeeMinorUnits:          fee,
	}, nil
}

// fetchOrderPage 读取 filter 窗口内的全部订单及其按原始状态的统计。
// filter 已由调用方（client.go 的 ListOrders）校验过。
func (c *client) fetchOrderPage(ctx context.Context, op string, filter OrderFilter) (OrderPage, error) {
	status := strings.ToUpper(strings.TrimSpace(filter.Status))
	rows, meta, truncated, err := c.fetchOrders(ctx, op, filter.From, filter.To, status)
	if err != nil {
		return OrderPage{}, err
	}

	items := make([]Order, 0, len(rows))
	stats := make(map[string]OrderStats)
	currencyGap := false
	for _, row := range rows {
		items = append(items, row.Order)
		s := stats[row.Status]
		s.Count++
		if row.Currency == c.currency {
			s.AmountMinorUnits += row.AmountMinorUnits
		} else {
			currencyGap = true
		}
		stats[row.Status] = s
	}

	observedAt := meta.observedAt(time.Time{})
	return OrderPage{
		Snapshot: Snapshot{
			ObservedAt: observedAt,
			Watermark:  orderWindowWatermark(filter.From, filter.To, observedAt, truncated, currencyGap),
			IsPartial:  truncated || currencyGap,
		},
		Items:         items,
		StatsByStatus: stats,
	}, nil
}

// orderWindowWatermark 把窗口边界与"为什么可能不完整"一起编码进水位。
//
// 只给 IsPartial 布尔值，运维看到"部分数据"却不知道是翻页到顶还是撞见了
// 非合约币种——两种情况的处置完全不同（前者可能要放宽 maxPaymentOrderPages，
// 后者要去核对 WithCurrency 的假设）。做法与既有 rechargeWatermark/
// paymentDay 的水位纪律一致：恒定信息（窗口边界）总在，只在真的发生时才
// 出现的信号（缺口原因）才出现——见 upstream.go 顶部 fetchPaymentDay 的
// rechargeWatermark 同款注释。
func orderWindowWatermark(from, to, observedAt time.Time, truncated, currencyGap bool) string {
	mark := fmt.Sprintf("window:%s..%s@%d", from.Format(time.RFC3339), to.Format(time.RFC3339), observedAt.Unix())
	if currencyGap {
		mark += "/currency_gap"
	}
	if truncated {
		mark += "/truncated"
	}
	return mark
}

// fetchDailyPaymentSummary 读取某业务日按归一化状态分桶的资金汇总。
// parsed 已由调用方（client.go 的 DailyPaymentSummary）按业务日时区解析过。
func (c *client) fetchDailyPaymentSummary(ctx context.Context, op string, parsed time.Time) (DailyPaymentSummary, error) {
	from := time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, c.businessDay)
	to := from.AddDate(0, 0, 1).Add(-time.Nanosecond)

	rows, meta, truncated, err := c.fetchOrders(ctx, op, from, to, "")
	if err != nil {
		return DailyPaymentSummary{}, err
	}

	byStatus := make(map[string]StatusAmount)
	// currencyGap 与 unknownStatus 分开计：两者都会把 IsPartial 置真，但
	// 运维要判断"这天的数字能不能用"，得先知道是哪一种——前者意味着某些
	// 订单的金额被排除在合约币种之外（笔数仍计入），后者意味着上游给了
	// KnownOrderStatuses 之外的新状态、这一批订单**连桶都进不了**（笔数也不计）。
	// 混成一个标志位（早期版本的写法）会让水位说不清到底缺了什么。
	currencyGap := false
	unknownStatus := false
	var feeTotal int64
	for _, row := range rows {
		bucket, ok := paymentStatusBucket(row.Status)
		if !ok {
			// 上游出现了 KnownOrderStatuses 之外的新状态：不猜它属于哪个桶，
			// 整体标记为部分数据，让人去核实（见 payments.go 的"不猜字段"约束）。
			unknownStatus = true
			continue
		}
		amount := row.AmountMinorUnits
		if bucket == PaymentStatusRefunded {
			// 退款桶统计的是**实际退还金额**，不是原订单面值——两者对
			// PARTIALLY_REFUNDED 从定义上就不同，见 payments.go 顶部的
			// DailyPaymentSummary 注释。
			amount = row.RefundAmountMinorUnits
		}
		s := byStatus[bucket]
		s.Count++
		if row.Currency == c.currency {
			s.AmountMinorUnits += amount
			if bucket == PaymentStatusSucceeded || bucket == PaymentStatusRefunded {
				feeTotal += row.FeeMinorUnits
			}
		} else {
			currencyGap = true
		}
		byStatus[bucket] = s
	}

	observedAt := meta.observedAt(time.Time{})
	dayText := parsed.Format(sub2apiBusinessDayLayout)
	fee := feeTotal
	return DailyPaymentSummary{
		Snapshot: Snapshot{
			ObservedAt: observedAt,
			Watermark:  dailyPaymentWatermark(dayText, observedAt, truncated, currencyGap, unknownStatus),
			IsPartial:  truncated || currencyGap || unknownStatus,
		},
		Day:           dayText,
		Currency:      c.currency,
		ByStatus:      byStatus,
		FeeMinorUnits: &fee,
		NetMinorUnits: nil,
	}, nil
}

// dailyPaymentWatermark 把业务日与"为什么可能不完整"一起编码进水位，
// 理由与 orderWindowWatermark 相同——见该函数的注释。
func dailyPaymentWatermark(dayText string, observedAt time.Time, truncated, currencyGap, unknownStatus bool) string {
	mark := fmt.Sprintf("day:%s@%d", dayText, observedAt.Unix())
	if currencyGap {
		mark += "/currency_gap"
	}
	if unknownStatus {
		mark += "/unknown_status"
	}
	if truncated {
		mark += "/truncated"
	}
	return mark
}
