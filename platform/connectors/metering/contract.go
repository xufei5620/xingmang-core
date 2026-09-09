// Package metering 是**计量型渠道成本 / 收入取数**的只读接入契约
// （XM-0037a，设计稿 §3.1/§3.2）。
//
// ⚠️ **为什么不并进 connectors/sub2api 与 connectors/newapi**：
// 那两个包读的是**自营实例的运营面板**（用户数、充值收入、渠道余额），
// 用的是管理员凭据与面板端点；本包读的是**上游账号的实扣与自营账号的使用
// 计费收入**，用的是每令牌凭据与另一组端点。设计稿 §3.1 有一段显著警告：
//
//	平台现有 `connectors/sub2api` 的 `sub2api.cost.daily` 读的是 admin 面板
//	`trend[].cost`（刻意避开 `actual_cost`），是自营实例口径，**不是本核算成本**。
//	XM-0037 成本侧必须新走「每令牌 /v1/usage actual_cost」，**不得复用**。
//
// 两个口径同名不同义，是最容易被静静用错的一类东西。把它们放进**不同的包**、
// 用**不同的指标键**，那条警告才从一句注释变成结构上做不到的事
// （contract_test.go 里有一条断言专门钉住指标键不碰撞）。
//
// 铁律：
//   - ReadClient **只有读方法**。写能力在 Foundation-B 后另立接口，
//     不得往本接口上加方法（ADR-018 闸 4 在这里体现为接口形状本身）；
//   - 每个返回值都带 ObservedAt 与 Watermark（规格 §9.1：禁止裸数字）；
//   - 金额一律 int64 scale-6 微单位 + Currency，**全程零 float**（宪法 13 条）；
//   - 取数**只搬运上游的数**，折算是另一步（见 CostOf）——取数与口径分开，
//     倍率改了不必重新取数，取数错了也不会被折算掩盖。
package metering

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

const (
	// ConnectorKey 是本 Connector 在 Registry 中的键。
	ConnectorKey = "metering"
	// ContractVersion 是本只读契约的版本。破坏性变更必须发新版本。
	ContractVersion = "1"
)

// UsageScale 是本契约所有金额的定点标度：scale-6 微单位（设计稿 §2.4/§12）。
//
// 导出它是给调用方用的：037b 往台账写 `*_minor` 时必须知道这些整数是几位小数，
// 而那个数字只该有一个来源。
const UsageScale = money.MicroScale

// BusinessDayLayout 是业务日字符串的格式。
const BusinessDayLayout = "2006-01-02"

// ReadCapabilities 是本连接器的只读能力清单（规格 §8.2）。
//
// 每一项都必须能被 registry.ParseCapability 解析，且 IsWrite() 为 false——
// contracttest 会断言这一点，让「只读」成为可验证的属性而非口头承诺。
var ReadCapabilities = []registry.Capability{
	"metering.service.version_read",
	"metering.health.read",
	"metering.token.usage_read",
	"metering.account.revenue_read",
	// ⚠️ 余额是**覆盖率最低**的一项（§7：newapi 主力盲区、订阅制上游没有余额）。
	// 它单独成一项能力而不是并进 usage_read，正是为了让「这个上游答不答得出
	// 余额」在 Capabilities() 里就看得见——调用方不必先打一次再看错误分类。
	"metering.upstream.balance_read",
}

// ErrInvalidDay：业务日不符合 BusinessDayLayout。
//
// 调用方用 errors.Is 判定，实现方负责包装成 connector.Error（KindBadResponse）。
var ErrInvalidDay = errors.New("metering: invalid business day")

// ValidateBusinessDay 校验业务日格式。
//
// 放在契约层而不是各实现里：Fake、sub2api 客户端与 newapi 客户端必须用同一条
// 判据，否则「Fake 上能跑、真环境上报错」这类问题会一路漏到联调。
//
// 严格 YYYY-MM-DD：time.Parse 对 "2026-8-1" 是宽容的，但业务日一旦有两种写法，
// 落进台账就是两条记录（profit_daily 的主键含 business_day）。
func ValidateBusinessDay(day string) error {
	parsed, err := time.Parse(BusinessDayLayout, day)
	if err != nil {
		return fmt.Errorf("business day %q 须形如 %s: %w", day, BusinessDayLayout, ErrInvalidDay)
	}
	if parsed.Format(BusinessDayLayout) != day {
		return fmt.Errorf("business day %q 必须严格为 %s（不接受少位写法）: %w",
			day, BusinessDayLayout, ErrInvalidDay)
	}
	return nil
}

// Snapshot 是每个读取结果都必须携带的新鲜度元数据（规格 §9.1）。
//
// **不复用 sub2api.Snapshot / newapi.Snapshot**：三个契约独立演进，各自钉住
// 各自上游的版本与语义（同 connectors/newapi/contract.go 的理由）。字段重复
// 几行是刻意付出的代价——契约之间的耦合应当是零。
type Snapshot struct {
	// ObservedAt 是上游数据的观测时刻，不是本地接收时刻。
	ObservedAt time.Time
	// Watermark 是上游的数据水位，用于判断是否读到了完整区间。
	Watermark string
	// IsPartial 表示本次只读到了部分数据。
	//
	// ⚠️ 两个真实驱动**从不置位**：它们的每次读取都是单值原子读
	// （一个 actual_cost 或一个 quota），要么拿到要么报错，没有「读到一半」
	// 这种状态。本字段真正的用武之地在**聚合**那一层——ToObservations 把
	// 几十条令牌读数汇成一条观测时，一部分令牌失败就是货真价实的部分数据
	// （见 aggregateSnapshot）。留着它是为了让那一层不必再发明一个标志位。
	IsPartial bool
}

// TokenRef 指认一个上游令牌（对应 finance.token_map 的一行）。
type TokenRef struct {
	// UpstreamTokenID 是**成本侧键**：
	//   sub2api → 上游令牌 id（仅用于回填结果与报错，取数本身靠 CredentialRef）
	//   newapi  → token_name（直接作为 /api/log/self/stat 的查询参数）
	UpstreamTokenID string

	// CredentialRef 是该令牌的凭据引用（`secret://<scope>/<name>`）。
	//
	// sub2api 必填：成本侧要用**该令牌自己的明文**打 `/v1/usage`（§3.1，
	// apikey 自鉴权，不是 admin key）。明文由 SecretProvider 解析，
	// Reveal() 只在拼 HTTP 头那一瞬出现，绝不入日志、错误或返回值
	// （ADR-014、宪法 7 条）。
	//
	// newapi 留空：它的成本侧走账号级 New-Api-User + Cookie。
	CredentialRef string
}

// TokenUsage 是一个上游令牌在某业务日的**上游实扣**读数（§3.1）。
//
// ⚠️ 这是**未经 recharge_ratio 折算**的原始读数，不是平台成本。折算是
// 另一步（CostOf），两者分开有三个实打实的理由：
//   - 倍率改了不必重新打上游；
//   - 取数错了不会被折算掩盖（两步各有各的测试）；
//   - 台账要冻结 ratio_snapshot（§6.3），而「用哪个倍率折的」只有折算那一步知道。
type TokenUsage struct {
	Snapshot

	UpstreamTokenID string
	// Day 是业务日（YYYY-MM-DD），按账号登记的业务日时区切分（§4）。
	Day string

	// UsageMinorUnits 是上游实扣，整数最小单位 @ UsageScale。
	UsageMinorUnits int64
	Currency        string

	// RawUnits 是上游给的**整数计数**（newapi 的 quota credits）；
	// 上游本来就给金额时（sub2api 的 actual_cost）为 nil。
	//
	// 保留它是为了「先 SUM 再除」（§3.1）：把同一账号下各令牌的 quota
	// 整数相加、只在最后折一次，比逐条折算后相加少掉每条半个微单位的舍入。
	// 调用方要做账号级合计时用 RawUnits + UnitsPerWhole 自己算，
	// 而不是把各条 UsageMinorUnits 加起来。
	RawUnits *int64
	// UnitsPerWhole 是 RawUnits 折 1 单位货币所需的计数（newapi 的
	// quota_per_unit，★口径常量 500000，见 §4）。RawUnits 为 nil 时它也为 nil。
	UnitsPerWhole *int64
}

// AccountRevenue 是一个自营账号在某业务日的**使用计费收入**（§3.2）。
//
// §10.5 铁律：收入 = 使用计费，**用户充值 / 余额 / 赠送额度不得混入**。
// sub2api 侧取的是 `today.user_cost`（使用量计费），本就满足这条。
type AccountRevenue struct {
	Snapshot

	// OwnAccountID 是**收入侧键**：sub2api 为自营账号 id，newapi 为 channel_id。
	OwnAccountID string
	Day          string

	// RevenueMinorUnits 是使用计费收入，整数最小单位 @ UsageScale。
	//
	// 返回 0 且 error 为 nil 表示**已知的 0**（上游明确说这天没有流量，
	// §3.2：`today==null` = 今日零流量）。读不到则返回 error——
	// 「未知」与「已知 0」在台账里落成不同的东西（NULL vs 0，§5.1），
	// 所以它们在契约层就必须是两种返回形态，而不是同一个 0。
	RevenueMinorUnits int64
	Currency          string
}

// UpstreamBalance 是**上游账号自己**的余额读数（§2.3 + §7 + §10.4）。
//
// 三件事把它与 TokenUsage / AccountRevenue 区分开，每一件都有后果：
//
//  1. **它是账号级的，不是令牌级也不是自营账号级**——余额属于那套凭据背后的
//     上游账户。所以本方法没有参数：客户端本来就是按上游账号构造的。
//  2. **它不参与成本核算**（§2.3 逐字）。余额差分会被充值污染——正跳变是充值
//     不是负成本，SoloAI 正因此不用它算成本。它唯一的用途是可用天数预警。
//  3. **读的是上游已存的余额，平台不去催上游刷新**（§7）：sub2api 走管理员
//     token 读 `/admin/accounts` 的 extra 快照，newapi 读 `channel.balance`
//     （需上游先开 CHANNEL_UPDATE_FREQUENCY，否则是一潭死水）。
//     平台**不**调 `update_balance`——那是写操作，而且会禁渠道。
type UpstreamBalance struct {
	Snapshot

	// BalanceMinorUnits 是余额，整数最小单位 @ UsageScale。
	//
	// **可以为负**：上游允许透支时余额就是负的，那正是最该报警的时刻。
	BalanceMinorUnits int64

	// Currency 是余额的币种。
	//
	// §10.4 硬性要求「余额和消耗单位一致」：单位不一致时可用天数算出来是一个
	// 纯粹的错数字，所以币种必须随读数一起回来，由计算侧比对（finance/runway.go）。
	Currency string
}

// TokenCost 是折算后的平台成本（§3.1 + §2.4）。
type TokenCost struct {
	TokenUsage

	// CostMinorUnits = round_halfup(UsageMinorUnits × 10^ratioScale / ratio_num)，
	// 整数最小单位 @ UsageScale，全程零 float。
	CostMinorUnits int64

	// RatioSnapshot 是本次折算**实际用的**倍率，逐条冻结（§6.3）。
	//
	// 037b 把它写进 profit_daily.ratio_snapshot（NOT NULL，第一天就写全）。
	// 不冻结的话，倍率随时可被改写且 SoloAI 侧无历史（迁移 0084），
	// 「上游涨价」与「倍率被调整」就再也分不开。
	RatioSnapshot money.Ratio
}

// CostOf 把上游实扣折算成平台成本：cost = usage ÷ recharge_ratio（§3.1/§2.4）。
//
// 整数定点除法 + 半进，全程零 float，唯一的舍入 ≤ 0.5 微单位。
// 与 SoloAI 的 float64 除法在「分」粒度必然落到同一个数（§2.4 的 worked example），
// 分以下平台这条路更准。
//
// 倍率未配置时报错而不是按 1 折算：「没配倍率」是配置缺失，
// 悄悄按 1 算出来的成本会是一个**看起来完全正常的错数字**（宪法 12 条）。
// 倍率为 0 或负数由 money.Divide 按 1 处理（对齐 SoloAI relay_profit.go:97），
// 但登记簿的库层 CHECK 让这种行根本进不来——见 money.Divide 的注释。
func CostOf(usage TokenUsage, ratio money.Ratio) (TokenCost, error) {
	if ratio.IsZero() {
		return TokenCost{}, fmt.Errorf(
			"令牌 %s 的上游账号未配置 recharge_ratio，无法折算成本（§3.1）: %w",
			usage.UpstreamTokenID, money.ErrFormat)
	}
	cost, err := money.Divide(usage.UsageMinorUnits, ratio)
	if err != nil {
		return TokenCost{}, fmt.Errorf("令牌 %s 折算失败: %w", usage.UpstreamTokenID, err)
	}
	return TokenCost{
		TokenUsage:     usage,
		CostMinorUnits: cost,
		RatioSnapshot:  ratio,
	}, nil
}

// SumRawUnits 把同一账号下多条令牌读数的**整数计数**相加，再一次性折成金额
// （§3.1「先 SUM 再除」）。
//
// 只对 RawUnits 非 nil 的读数有意义（newapi）。第二个返回值报告有没有可合计的
// 条目：全都没有 RawUnits 时返回 false，让调用方知道该退回逐条 UsageMinorUnits
// 相加，而不是拿到一个理直气壮的 0。
//
// 混用不同 UnitsPerWhole 的条目会报错：那意味着上游在一轮采集中途改了
// quota_per_unit（它是运行期可变的），把两种刻度的计数加起来是纯粹的错数。
func SumRawUnits(usages []TokenUsage, scale int) (int64, bool, error) {
	var (
		total    int64
		perWhole int64
		found    bool
	)
	for _, u := range usages {
		if u.RawUnits == nil || u.UnitsPerWhole == nil {
			continue
		}
		if found && *u.UnitsPerWhole != perWhole {
			return 0, false, fmt.Errorf(
				"同一批读数里出现两种 quota_per_unit（%d 与 %d）：上游在采集中途改了刻度，"+
					"两种计数不可相加", perWhole, *u.UnitsPerWhole)
		}
		perWhole = *u.UnitsPerWhole
		found = true

		// 整数相加会不会溢出：quota 是 int64，几十条相加远不到上限，
		// 但溢出一次就是一个负成本，所以还是判一下。
		if (total > 0 && *u.RawUnits > 0 && total > (1<<62)) ||
			(total < 0 && *u.RawUnits < 0 && total < -(1<<62)) {
			return 0, false, fmt.Errorf("quota 合计溢出 int64: %w", money.ErrOverflow)
		}
		total += *u.RawUnits
	}
	if !found {
		return 0, false, nil
	}
	minor, err := money.DivideByUnits(total, perWhole, scale)
	if err != nil {
		return 0, false, err
	}
	return minor, true, nil
}

// ReadClient 是计量型渠道的只读取数契约。
//
// **只有读方法。** 写能力在 Foundation-B 后另立接口，绝不往本接口加方法——
// ADR-018 闸 4 在这里体现为接口形状本身。
type ReadClient interface {
	// Version 探测上游版本并给出是否在兼容矩阵内。
	// 不支持的版本返回 Supported=false 而非报错：是否 Fail Closed 由调用方
	// 按场景决定（读取可降级，写入必须停）。
	Version(ctx context.Context) (connector.VersionInfo, error)

	// Health 返回运营健康状态；失败时用结构化 ErrorKind，不透传上游原始错误。
	Health(ctx context.Context) (connector.HealthResult, error)

	// Capabilities 返回本连接当前实际可用的能力。
	Capabilities(ctx context.Context) ([]registry.Capability, error)

	// TokenUsage 读一个上游令牌在某业务日的上游实扣（§3.1）。
	//
	// ⚠️ **上游未必答得出「过去某一天」**：sub2api 的 `/v1/usage` 只回答
	// 今天（响应里就叫 `today`，没有日期参数）。实现遇到自己答不出的业务日
	// 必须返回 connector.KindNotSupported，**不得**把今天的数当成那一天的
	// 返回回去——那是一个看起来完全正常的错数字。
	TokenUsage(ctx context.Context, token TokenRef, day string) (TokenUsage, error)

	// AccountRevenue 读一个自营账号在某业务日的使用计费收入（§3.2）。
	//
	// 上游没有这条能力时返回 connector.KindNotSupported（例如 newapi 的收入
	// 在自营 new-api 库里，走的是数据库通道而不是 HTTP，见 newapi 客户端）。
	AccountRevenue(ctx context.Context, ownAccountID string, day string) (AccountRevenue, error)

	// UpstreamBalance 读本上游账号当前的余额（§2.3 + §7）。
	//
	// 没有业务日参数：余额是一个**当前值**，不是某一天的累计量。
	//
	// ⚠️ **覆盖率是这条能力的第一等事实**（§7：newapi 是主力盲区、覆盖率低；
	// 订阅制上游根本没有余额这个概念）。读不到时返回
	// connector.KindNotSupported，调用方据此把可用天数落成「未知」——
	// 而不是落成一个 0 天或一个无穷大（§10.4：无消耗或数据过期不显示
	// 伪精确天数）。
	UpstreamBalance(ctx context.Context) (UpstreamBalance, error)
}

// 指标键（写进 ops.metric_observation 的 metric_key）。
//
// ⚠️ **绝不能与 connectors/sub2api 的 `sub2api.cost.daily` 碰撞或复用**：
// 那条是自营面板口径（admin `trend[].cost`），本组是核算成本口径
// （每令牌 `/v1/usage actual_cost` ÷ 倍率）。设计稿 §3.1 有显著标注。
// contract_test.go 的 TestMetricKeysDoNotCollideWithPanelCost 钉住这一点。
//
// ⚠️ 这几个常量在 internal/platform/ops 的白名单里有一份**字面量副本**
// （ops 不能反向 import connectors，会成环）。改动这里必须同步改那边，
// ops_test 的 TestRegisteredMetricsMatchConnectorContracts 会当场拦住遗漏。
const (
	// MetricCostDaily 是**核算口径**的当日供给成本（已折算，§3.1 + §2.4）。
	MetricCostDaily = "finance.cost.daily"
	// MetricRevenueDaily 是当日使用计费收入（§3.2，不含充值 / 赠送）。
	MetricRevenueDaily = "finance.revenue.daily"
)

// DefaultStalenessThresholdSeconds 是这批指标的默认新鲜度阈值。
//
// 1800 秒，与 sub2api / newapi 取齐：成本采集按 §12 拍板默认 5 分钟一轮，
// 半小时没有新数据说明采集链路有问题，而不是「今天没有消费」。
const DefaultStalenessThresholdSeconds int32 = 1800

func snapshotToObservation(
	metricKey, instanceID, environment string, snap Snapshot, now time.Time,
	value map[string]any,
) ops.Observation {
	observedAt := snap.ObservedAt.UTC()
	o := ops.Observation{
		MetricKey:                 metricKey,
		Source:                    instanceID,
		Environment:               environment,
		SyncedAt:                  now.UTC(),
		Watermark:                 snap.Watermark,
		Status:                    ops.SyncOK,
		IsPartial:                 snap.IsPartial,
		StalenessThresholdSeconds: DefaultStalenessThresholdSeconds,
		Value:                     value,
	}
	// 零值 ObservedAt 表示上游没给观测时刻——保持为空而不是用 now 冒充，
	// 否则「从未采集」与「刚采集」无法区分（规格 §9.1）。
	if !snap.ObservedAt.IsZero() {
		o.ObservedAt = &observedAt
		o.LastSuccess = &observedAt
	}
	return o
}

// aggregateSnapshot 把一组逐项快照收敛成聚合指标的快照。
//
// 观测时刻取**最旧**的那一项：聚合指标的新鲜度由最不新鲜的成员决定。
// 取最新会让一个刚更新的令牌替十个陈旧的令牌背书，看板显示「数据新鲜」，
// 而那一格里九成的数字其实已经过期了。
func aggregateSnapshot[T any](items []T, snapshotOf func(T) Snapshot) Snapshot {
	out := Snapshot{}
	for _, item := range items {
		snap := snapshotOf(item)
		if snap.IsPartial {
			out.IsPartial = true
		}
		if snap.ObservedAt.IsZero() {
			continue
		}
		if out.ObservedAt.IsZero() || snap.ObservedAt.Before(out.ObservedAt) {
			out.ObservedAt = snap.ObservedAt
			out.Watermark = snap.Watermark
		}
	}
	return out
}

// ToObservations 把折算后的成本与收入转成新鲜度模型，接进看板（规格 §9.1）。
//
// 这是 Connector 与看板之间的唯一接缝：Connector 不直接写库，
// 由调用方（XM-0037b 的 cost_sync 任务）拿这些 Observation 去 Upsert。
//
// 金额以 `*_minor_units`（int64）承载，前端转 §13 的 `Money{amountMinor:string}`；
// 读回路径不过 float（ops/store.go 的 UseNumber）。
func ToObservations(
	now time.Time, instanceID, environment string,
	costs []TokenCost, revenues []AccountRevenue,
) []ops.Observation {
	return []ops.Observation{
		costsObservation(now, instanceID, environment, costs),
		revenuesObservation(now, instanceID, environment, revenues),
	}
}

// costsObservation 把逐令牌成本聚合成一条指标。
//
// 聚合成一条而不是每个令牌一条：指标表的一行是「一个可以设阈值、可以画趋势的量」，
// 令牌数量随运营增删而变，逐令牌建指标会让指标白名单变成一张永远追不上的表，
// 历史趋势也会在令牌下线那天断掉。逐令牌明细走 value 里的数组。
func costsObservation(
	now time.Time, instanceID, environment string, costs []TokenCost,
) ops.Observation {
	rows := make([]any, 0, len(costs))
	var totalCost, totalUsage int64
	currency := ""
	mixedCurrency := false
	for _, c := range costs {
		if currency == "" {
			currency = c.Currency
		} else if currency != c.Currency {
			// 把不同币种的最小单位加在一起是纯粹的错数（同 newapi
			// modelsObservation 的纪律）。标记出来，合计留给调用方按币种分桶。
			mixedCurrency = true
		}
		totalCost += c.CostMinorUnits
		totalUsage += c.UsageMinorUnits
		rows = append(rows, map[string]any{
			"upstream_token_id": c.UpstreamTokenID,
			"day":               c.Day,
			"usage_minor_units": c.UsageMinorUnits,
			"cost_minor_units":  c.CostMinorUnits,
			"currency":          c.Currency,
			// 倍率进 value 是必要的：看板上「这条渠道成本怎么涨了」的第一个
			// 追问就是「倍率是不是被改了」，答案必须能就地看到（§6.3）。
			"ratio_snapshot": c.RatioSnapshot.String(),
		})
	}
	value := map[string]any{
		"tokens":      rows,
		"token_count": len(costs),
		"scale":       UsageScale,
	}
	if mixedCurrency {
		// 合计不给（会是错数），但**说清为什么不给**——留一个空位比留一个
		// 错数字好，留一个没有解释的空位则会让人以为是 bug（宪法 12 条）。
		value["total_omitted_reason"] = "mixed_currency"
	} else {
		value["total_cost_minor_units"] = totalCost
		value["total_usage_minor_units"] = totalUsage
		value["currency"] = currency
	}
	return snapshotToObservation(MetricCostDaily, instanceID, environment,
		aggregateSnapshot(costs, func(c TokenCost) Snapshot { return c.Snapshot }), now, value)
}

// revenuesObservation 把逐自营账号收入聚合成一条指标（理由同 costsObservation）。
func revenuesObservation(
	now time.Time, instanceID, environment string, revenues []AccountRevenue,
) ops.Observation {
	rows := make([]any, 0, len(revenues))
	var total int64
	currency := ""
	mixedCurrency := false
	for _, r := range revenues {
		if currency == "" {
			currency = r.Currency
		} else if currency != r.Currency {
			mixedCurrency = true
		}
		total += r.RevenueMinorUnits
		rows = append(rows, map[string]any{
			"own_account_id":      r.OwnAccountID,
			"day":                 r.Day,
			"revenue_minor_units": r.RevenueMinorUnits,
			"currency":            r.Currency,
		})
	}
	value := map[string]any{
		"accounts":      rows,
		"account_count": len(revenues),
		"scale":         UsageScale,
	}
	if mixedCurrency {
		value["total_omitted_reason"] = "mixed_currency"
	} else {
		value["total_revenue_minor_units"] = total
		value["currency"] = currency
	}
	return snapshotToObservation(MetricRevenueDaily, instanceID, environment,
		aggregateSnapshot(revenues, func(r AccountRevenue) Snapshot { return r.Snapshot }), now, value)
}
