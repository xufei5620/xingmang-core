package newapi

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

const (
	// ConnectorKey 是本 Connector 在 Registry 中的键。
	ConnectorKey = "newapi"
	// ContractVersion 是本只读契约的版本。破坏性变更必须发新版本。
	ContractVersionV1 = "1"
	ContractVersionV2 = "2"
	ContractVersion   = ContractVersionV2
)

// ReadCapabilities 是 Foundation-A 阶段的只读能力清单（规格 §8.2、§8.4）。
//
// 每一项都必须能被 registry.ParseCapability 解析，且 IsWrite() 为 false——
// contracttest 会断言这一点，让「只读」成为可验证的属性而非口头承诺。
//
// 与规格 §8.2 的能力示例（newapi.users.read / newapi.tokens.read /
// newapi.channels.read / newapi.models.probe…）对齐但**更窄**：
// tokens.read 与 models.probe 不在 Foundation-A 的只读范围（§8.4）里，
// 现在就要来只会多一份被滥用的面。能力清单不是许愿单。
var ReadCapabilities = []registry.Capability{
	"newapi.service.version_read",
	"newapi.users.read",
	"newapi.orders.read",
	"newapi.channels.read",
	"newapi.models.usage_read",
	"newapi.errors.read",
	"newapi.health.read",
}

// Snapshot 是每个读取结果都必须携带的新鲜度元数据（规格 §9.1）。
//
// 没有「只返回值不返回快照信息」的数据结构——裸数字在类型层面就不存在。
//
// **为什么不复用 sub2api.Snapshot / invoice.Snapshot**：三个契约独立演进，
// 各自钉住各自上游的版本与语义。共享一个结构体会让任意一边的破坏性变更
// （比如给水位换个类型、把 IsPartial 拆成两个标志）无声地传染到另一边，
// 然后在毫不相干的 Connector 上炸出编译错误或——更糟——语义漂移。
// 字段重复几行是刻意付出的代价：契约之间的耦合应当是零，哪怕代价是抄一遍。
type Snapshot struct {
	// ObservedAt 是上游数据的观测时刻，不是本地接收时刻。
	ObservedAt time.Time
	// Watermark 是上游的数据水位，用于判断是否读到了完整区间（规格 §8.4「数据水位」）。
	Watermark string
	// IsPartial 表示本次只读到了部分数据（如分页中断、部分渠道探测超时）。
	IsPartial bool
}

// BusinessDayLayout 是业务日字符串的格式。
const BusinessDayLayout = "2006-01-02"

// ErrInvalidDay：业务日不符合 BusinessDayLayout。
// 调用方用 errors.Is 判定，实现方负责包装成 connector.Error。
var ErrInvalidDay = errors.New("invalid business day")

// ValidateBusinessDay 校验业务日格式。
//
// 放在契约层而不是各实现里：Fake 与 XM-0038 的真实客户端必须用同一条判据，
// 否则「Fake 上能跑、真环境上报错」这类问题会一路漏到联调。
func ValidateBusinessDay(day string) error {
	if _, err := time.Parse(BusinessDayLayout, day); err != nil {
		return fmt.Errorf("business day %q 须形如 %s: %w", day, BusinessDayLayout, ErrInvalidDay)
	}
	return nil
}

// UserStats 是用户与余额概览（规格 §8.4：用户数量、余额和消费）。
type UserStats struct {
	Snapshot
	TotalUsers  int64
	ActiveUsers int64
	// BalanceMinorUnits 是全站用户余额合计，整数最小货币单位（分），
	// 禁止 float（规格 §5.9）。
	BalanceMinorUnits int64
	Currency          string
}

// OrderSummary 是某一业务日的充值与订阅摘要（规格 §8.4：充值/订阅订单摘要）。
//
// 充值与订阅分成两个字段而不是一个「收入」：NewAPI 的这两笔钱业务含义不同
// （一次性买额度 vs 周期订阅），合成一个数之后就再也拆不开了，而运营看板上
// 「这个月订阅涨了没有」恰恰是要分开看的问题。
type OrderSummary struct {
	Snapshot
	// Day 是业务日，格式 2006-01-02。
	Day                    string
	RechargeMinorUnits     int64
	SubscriptionMinorUnits int64
	// OrderCount 是两类订单合计单数。
	OrderCount int64
	Currency   string
}

// ErrorRateUnhealthyPPM 是「这个渠道算不算异常」的**展示分组**阈值，
// 50000 ppm = 5%。
//
// 它只用于 ToObservations 里那个 unhealthy_channel_count 聚合数，让看板能一眼
// 看出「有几个渠道不对劲」。**它不是告警阈值**：告警阈值是运营策略，该由
// alerts 包按环境配置（对照 jobs.Config.AlertBalanceThresholdMinorUnits），
// 而不是由一个契约常量替所有部署拍板。
//
// 契约里之所以还是要有一个数：聚合指标必须给得出「异常数」这个字段，
// 而计数总得有个判据。把判据写成导出常量并说清它的适用范围，比让每个消费方
// 各自在前端拍一个数好——后者会让同一批渠道在总览页和详情页显示成不同的异常数。
const ErrorRateUnhealthyPPM int64 = 50_000

// ChannelStatus 是一个上游渠道的状态（规格 §8.4：模型和渠道状态、错误率、性能指标）。
type ChannelStatus struct {
	Snapshot
	ChannelID string
	Name      string
	// Type 是 NewAPI 标注的渠道类型。平台**不解释其语义**，原样透传给看板做
	// 分组——上游改了取值集合不该逼平台跟着发版。
	Type string
	// Enabled 是渠道的启停状态。它是**读**到的状态，不是平台能改的东西
	// （渠道启停属于 §8.4 的「后续写范围」）。
	Enabled bool

	// BalanceMinorUnits 是渠道余额，**可空**。
	//
	// nil 表示「NewAPI 上这个渠道没有配置余额」，与 0 是两回事：0 是
	// 「配了，而且已经花光了」——那是要立刻处理的事故；nil 是「这个渠道
	// 本来就不按余额计费」——那是正常状态。用 int64 零值同时表达两者，
	// 看板上就会出现一排理直气壮的「¥0.00」，运营分不出哪个该救。
	//
	// 这条是 XM-0017 sub2api-real 上踩过的教训：那边把「上游没给字段」
	// 和「上游给了 0」在解码时合流了，前端只好靠猜。这里从类型上就分开。
	BalanceMinorUnits *int64
	Currency          string

	// ModelCount 是该渠道当前挂了多少个模型。
	ModelCount int64

	// ErrorRatePPM 是错误率，单位 **ppm（百万分之一）** 的整数。
	//
	// 为什么不是 float64：比率和金额是同一类东西——一旦落进浮点，
	// 0.1% 就不再等于 0.1%，两次采集算出来的同一个比率可能不相等，
	// 阈值比较会在边界上抖动，JSON 往返还会把 0.001 变成
	// 0.0009999999999999998。规格 §5.9 对金额的纪律在这里同样适用。
	//
	// 为什么是 ppm 而不是万分之或百分之：ppm 的分辨率够细，
	// 一个健康网关的错误率常常在 0.01% 量级（= 100 ppm），
	// 用百分之整数表达时它和 0 无法区分。
	//
	// 换算：1% = 10000 ppm。显示成百分比时用整数运算
	// （ppm/10000 取整数位，ppm%10000/100 取两位小数），不要先转成 float 再除。
	ErrorRatePPM int64

	// LatencyMS 是该渠道最近一次探测的响应延迟（毫秒，规格 §8.4「性能指标」）。
	LatencyMS int64

	// 以下字段为 XM-CHAN-FIELDS0 新增，全部可空；契约文档
	// contracts/connectors/newapi.channel-catalog.v3.md 逐项列出来源，
	// 无上游字段支撑的维度恒为 nil 并写明原因——不做近似。
	// 与 sub2api.ManagedChannel 的同名字段是**独立定义**，不是共享类型
	// （两个连接器包之间零耦合，见本包 Snapshot 类型的同款设计理由）。

	// Kind 恒为 nil：K:/newapi-src 没有可达的字段区分 OAuth/订阅型渠道与
	// API Key 渠道——唯一线索（Codex 渠道的 OAuth 凭据）编码在被
	// /api/channel/、/api/channel/:id 两个列表/详情端点一律排除的 Key
	// 字符串列里，读不到。
	Kind *string
	// Vendor 派生自 Type，经 newapiVendorNames（本包按
	// K:/newapi-src constant/channel.go 的 ChannelTypeNames 原样抄录的映射表）
	// 查表得到。**不是**上游自己的字符串字段——上游只给类型整数。查不到的
	// Type 值（未来上游新增类型）为 nil，不猜一个名字。
	Vendor *string
	// CapacityUsed/CapacityLimit 恒为 nil：上游没有任何渠道级并发上限字段
	// （已排查 Channel 结构体、Setting、OtherSettings 的全部 JSON 键）。
	CapacityUsed  *int64
	CapacityLimit *int64
	// StatusLabel 是 Status 的字符串枚举名（unknown/enabled/
	// manually_disabled/auto_disabled），供合并表统一展示；Enabled 字段
	// 保留原语义不变。
	StatusLabel *string
	// SchedulingPriority 来自渠道的 Priority（*int64，数值越大越优先——
	// 与 sub2api 账号级 Priority「数值越小优先级越高」方向相反，见契约文档
	// 的平台差异说明）。scheduling.enabled 复用既有 Enabled 字段，不重复。
	SchedulingPriority *int64

	// Today* 来自 /api/log/stat 的预算内探测（见 upstream.go
	// fetchChannelTodayStats），窗口是**业务日**（与既有 24h 滚动错误率窗口
	// 不同），预算独立计数（maxTodayStatsChannels）。
	//
	// TodaySuccessRatePPM 与 ErrorRatePPM 同一条纪律（ppm 整数，不用
	// float64——比率一旦落进浮点，两次采集算出来的同一个比率可能不相等，
	// 阈值比较会在边界上抖动，见 ErrorRatePPM 字段注释、宪法 13 条）。
	TodayRequests       *int64
	TodaySuccessRatePPM *int64
	TodayCostMinorUnits *int64
	TodayCurrency       *string
	TodayScale          *int

	// UsageWindow* 恒为 nil：NewAPI 渠道没有订阅/配额窗口概念（已排查
	// Channel/Setting/OtherSettings 的全部 JSON 键，唯一近似物是 Codex 渠道
	// 实时代理 OpenAI 官方用量接口的三个端点，纯透传不落库，见契约文档）。
	// 类型仍按 ppm 整数声明（与 TodaySuccessRatePPM 同一条纪律），保持字段
	// 形状即便在恒为 nil 时也不给将来的实现开一道浮点后门。
	UsageWindowUsedRatioPPM *int64
	UsageWindowResetsAt     *time.Time

	// ProxyLabel 来自 Channel.Setting JSON 里的 proxy 字段，**必须脱敏**：
	// 上游把它当完整代理 URL 存（可能内嵌 user:pass@），见
	// safeProxyLabel——只保留 scheme://host:port，绝不透出凭据。
	ProxyLabel *string

	// RateMultiplierPPM/UpstreamMultiplierPPM 恒为 nil：NewAPI 的计费倍率
	// （model_ratio/completion_ratio/分组倍率）按模型名/分组名配置，不存在
	// 任何渠道级覆盖字段（已排查 Channel/Setting/OtherSettings 全部键）。
	// ppm 整数命名同上，理由一致。
	RateMultiplierPPM     *int64
	UpstreamMultiplierPPM *int64

	// Group 来自 Channel.Group（model/channel.go:40，逗号分隔的分组名字符串，
	// 上游默认值 "default"，用于按分组路由/计费——XM-CHAN-GROUP0 新增，晚于
	// 本文件其余字段一轮）。原样保留逗号分隔字符串这个上游自己的线上格式,
	// 不拆成数组：上游 JSON 字段本身就是一个字符串（不是数组），把它拆开
	// 再重组是替上游做了一次它自己都没做的结构决定。仅做归一化——去掉外层
	// 多余逗号与每段的首尾空白（同 model/channel.go:296-305 GetGroups() 的
	// 归一化规则，逐行核对），再丢弃归一化后仍为空的段——不是近似，是把
	// "1,,2" 这种脏数据在展示前清理成 "1,2"，见 parseChannelGroup。
	// 归一化后整条为空（原始未配置任何分组）时为 nil，不是空字符串。
	Group *string

	// LastUsedAt 恒为 nil：上游没有「渠道最近一次服务请求」的时间字段
	// （TestTime 是连通性测试时刻，不是服务请求时刻，两者不能混用）。
	LastUsedAt *time.Time
	// CreatedAt 来自 Channel.CreatedTime（unix 秒）。
	CreatedAt *time.Time
	// ExpiresAt 恒为 nil：渠道级没有通用到期字段；唯一的到期概念
	// （Codex 渠道 OAuth token 的 expired 时刻）编码在被列表/详情端点排除的
	// Key 字符串列里，读不到。
	ExpiresAt *time.Time
}

// newapiVendorNames 原样抄录 K:/newapi-src constant/channel.go:129-187 的
// ChannelTypeNames 映射表（GetChannelTypeName 的数据源）。
//
// 这是「已知取值的查表」而不是「猜一个名字」：每一项都能追到上游那份常量表
// 的同一行；上游新增渠道类型时，这里查不到的 Type 值会让 Vendor 变成 nil
// （而不是显示一个错误的名字），需要人工同步更新——见契约文档的维护说明。
var newapiVendorNames = map[int]string{
	0: "Unknown", 1: "OpenAI", 2: "Midjourney", 3: "Azure", 4: "Ollama",
	5: "MidjourneyPlus", 6: "OpenAIMax", 7: "OhMyGPT", 8: "Custom", 9: "AILS",
	10: "AIProxy", 11: "PaLM", 12: "API2GPT", 13: "AIGC2D", 14: "Anthropic",
	15: "Baidu", 16: "Zhipu", 17: "Ali", 18: "Xunfei", 19: "360",
	20: "OpenRouter", 21: "AIProxyLibrary", 22: "FastGPT", 23: "Tencent",
	24: "Gemini", 25: "Moonshot", 26: "ZhipuV4", 27: "Perplexity",
	31: "LingYiWanWu", 33: "AWS", 34: "Cohere", 35: "MiniMax", 36: "SunoAPI",
	37: "Dify", 38: "Jina", 39: "Cloudflare", 40: "SiliconFlow",
	41: "VertexAI", 42: "Mistral", 43: "DeepSeek", 44: "MokaAI",
	45: "VolcEngine", 46: "BaiduV2", 47: "Xinference", 48: "xAI", 49: "Coze",
	50: "Kling", 51: "Jimeng", 52: "Vidu", 53: "Submodel", 54: "DoubaoVideo",
	55: "Sora", 56: "Replicate", 57: "ChatGPT Subscription (Codex)",
	58: "Advanced Custom", 59: "Sub2API", 60: "New API",
}

// vendorNameForType 查 newapiVendorNames；未登记的 Type 返回 (\"\", false)——
// 调用方据此把 Vendor 置 nil，不猜一个名字。
func vendorNameForType(channelType int) (string, bool) {
	name, ok := newapiVendorNames[channelType]
	return name, ok
}

// channelStatusEnumNames 是 Status 到字符串标签的映射
// （common/constants.go:244-247 的 ChannelStatus* 常量，逐个核对）。
var channelStatusEnumNames = map[int]string{
	0: "unknown", 1: "enabled", 2: "manually_disabled", 3: "auto_disabled",
}

// ppmToFraction 把 ppm（百万分之一）整数换算成 JSON 数值型的小数——只在
// 对外呈现这一步、只做一次除法；内部的解析、换算、比较全部停留在整数 ppm
// 上，与 ErrorRatePPM 同一条纪律（宪法 13 条"比例使用 Decimal"），单次终值
// 换算不会引入那条纪律真正想避免的累积误差。
func ppmToFraction(ppm int64) float64 {
	return float64(ppm) / 1_000_000
}

// catalogFields 把 ChannelStatus 的 v3 目录字段编码成观测值的一个嵌套
// map，形状与 httpapi 最终暴露的 JSON 一一对应——与 sub2api 的同名方法
// 同一条纪律：某个维度完全没有数据时不写这个键，而不是写 null。
func (c ChannelStatus) catalogFields() map[string]any {
	row := map[string]any{}
	if c.Kind != nil {
		row["kind"] = *c.Kind
	}
	if c.Vendor != nil {
		row["vendor"] = *c.Vendor
	}
	if c.CapacityUsed != nil || c.CapacityLimit != nil {
		capacity := map[string]any{}
		if c.CapacityUsed != nil {
			capacity["used"] = *c.CapacityUsed
		}
		if c.CapacityLimit != nil {
			capacity["limit"] = *c.CapacityLimit
		}
		row["capacity"] = capacity
	}
	if c.StatusLabel != nil {
		row["status"] = *c.StatusLabel
	}
	scheduling := map[string]any{"enabled": c.Enabled}
	if c.SchedulingPriority != nil {
		scheduling["priority"] = *c.SchedulingPriority
	}
	row["scheduling"] = scheduling
	if c.TodayRequests != nil || c.TodaySuccessRatePPM != nil || c.TodayCostMinorUnits != nil {
		today := map[string]any{}
		if c.TodayRequests != nil {
			today["requests"] = *c.TodayRequests
		}
		if c.TodaySuccessRatePPM != nil {
			today["success_rate"] = ppmToFraction(*c.TodaySuccessRatePPM)
		}
		if c.TodayCostMinorUnits != nil {
			today["cost_minor_units"] = *c.TodayCostMinorUnits
			if c.TodayCurrency != nil {
				today["currency"] = *c.TodayCurrency
			}
			if c.TodayScale != nil {
				today["scale"] = *c.TodayScale
			}
		}
		row["today"] = today
	}
	if c.UsageWindowUsedRatioPPM != nil || c.UsageWindowResetsAt != nil {
		window := map[string]any{}
		if c.UsageWindowUsedRatioPPM != nil {
			window["used_ratio"] = ppmToFraction(*c.UsageWindowUsedRatioPPM)
		}
		if c.UsageWindowResetsAt != nil {
			window["resets_at"] = c.UsageWindowResetsAt.UTC().Format(time.RFC3339Nano)
		}
		row["usage_window"] = window
	}
	if c.ProxyLabel != nil {
		row["proxy"] = *c.ProxyLabel
	}
	if c.Group != nil {
		row["group"] = *c.Group
	}
	if c.RateMultiplierPPM != nil {
		row["rate_multiplier"] = ppmToFraction(*c.RateMultiplierPPM)
	}
	if c.UpstreamMultiplierPPM != nil {
		row["upstream_multiplier"] = ppmToFraction(*c.UpstreamMultiplierPPM)
	}
	if c.LastUsedAt != nil {
		row["last_used_at"] = c.LastUsedAt.UTC().Format(time.RFC3339Nano)
	}
	if c.CreatedAt != nil {
		row["created_at"] = c.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	if c.ExpiresAt != nil {
		row["expires_at"] = c.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return row
}

// Unhealthy 报告该渠道是否算「异常」（判据见 ErrorRateUnhealthyPPM）。
//
// 停用的渠道**一律不算异常**：它没在服务，谈不上出错。把停用渠道计进异常数
// 会让一个被人为关掉的渠道在看板上永久亮红，然后所有人学会无视那个数字。
func (c ChannelStatus) Unhealthy() bool {
	return c.Enabled && c.ErrorRatePPM >= ErrorRateUnhealthyPPM
}

// ModelUsage 是某个模型在某业务日的使用量（规格 §8.4：模型状态、余额和消费）。
type ModelUsage struct {
	Snapshot
	ModelName    string
	RequestCount int64
	// ConsumedMinorUnits 是该模型消耗的额度，整数最小货币单位。
	ConsumedMinorUnits int64
	Currency           string
}

// ReadClient 是 NewAPI 的只读接入契约。
//
// **只有读方法。** 规格 §8.4 的「后续写范围」（Token 管理、渠道启停、
// 模型配置、有限用户管理）在 Foundation-B 后另立接口，绝不往本接口加方法——
// ADR-018 闸 4 在这里体现为接口形状本身。
type ReadClient interface {
	// Version 探测上游版本并给出是否在兼容矩阵内（规格 §8.4：服务状态）。
	// 不支持的版本返回 Supported=false 而非报错：是否 Fail Closed 由调用方
	// 按场景决定（读取可降级，写入必须停）。
	Version(ctx context.Context) (connector.VersionInfo, error)

	// Health 返回运营健康状态；失败时用结构化 ErrorKind，不透传上游原始错误。
	Health(ctx context.Context) (connector.HealthResult, error)

	// Capabilities 返回本连接当前实际可用的能力（可能因上游版本而少于 ReadCapabilities）。
	Capabilities(ctx context.Context) ([]registry.Capability, error)

	// UserStats 读取用户数量与余额概览。
	UserStats(ctx context.Context) (UserStats, error)

	// DailyOrders 读取某业务日（2006-01-02）的充值与订阅摘要。
	DailyOrders(ctx context.Context, day string) (OrderSummary, error)

	// Channels 读取全部渠道的状态、错误率与延迟。
	Channels(ctx context.Context) ([]ChannelStatus, error)

	// ModelUsages 读取某业务日（2006-01-02）的逐模型使用量。
	ModelUsages(ctx context.Context, day string) ([]ModelUsage, error)
}

// 指标键（写进 ops.metric_observation 的 metric_key）。
//
// ⚠️ 这几个常量在 internal/platform/ops 的白名单里有一份**字面量副本**
// （ops 不能反向 import connectors，会成环）。改动这里必须同步改那边，
// ops_test 的 TestRegisteredMetricsMatchConnectorContracts 会当场拦住遗漏。
const (
	// MetricUsersTotal 是用户数量与余额概览。余额同在此指标的 value 里，
	// 不像 sub2api 那样再拆一条 users.balance：NewAPI 侧没有「透支」这一维，
	// 拆出去之后那张卡片上只剩一个数，不值得占一格。
	MetricUsersTotal = "newapi.users.total"
	// MetricRechargeDaily 是当日充值金额。
	MetricRechargeDaily = "newapi.recharge.daily"
	// MetricSubscriptionDaily 是当日订阅金额。
	MetricSubscriptionDaily = "newapi.subscription.daily"
	// MetricChannelsStatus 是渠道状态聚合（总数/启用数/异常数 + 逐渠道数组）。
	MetricChannelsStatus = "newapi.channels.status"
	// MetricModelsUsage 是逐模型使用量聚合。
	MetricModelsUsage = "newapi.models.usage"
)

// DefaultStalenessThresholdSeconds 是这批指标的默认新鲜度阈值。
//
// 1800 秒，与 Sub2API 取齐（不像开票放宽到 3600）：NewAPI 是**在线网关**，
// 请求量与错误率是分钟级变化的东西，半小时没有新数据就已经说明采集链路有问题，
// 而不是「今天没人提交」。阈值该贴着业务节奏定，不该跨 Connector 抄一个数。
//
// 导出它是给前端用的：页面要显示「多久算旧」，判据必须与产出指标时用的是
// 同一个常量，而不是在前端再写一遍 1800。
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
	// 否则「从未采集」与「刚采集」无法区分（规格 §9.1）
	if !snap.ObservedAt.IsZero() {
		o.ObservedAt = &observedAt
		o.LastSuccess = &observedAt
	}
	return o
}

// aggregateSnapshot 把一组逐项快照收敛成聚合指标的快照。
//
// 观测时刻取**最旧**的那一项：聚合指标的新鲜度由最不新鲜的成员决定。
// 取最新会让一个刚更新的渠道替十个陈旧的渠道背书，看板显示「数据新鲜」，
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

// ToObservations 把契约数据转成新鲜度模型，接进看板（规格 §9.1）。
//
// 这是 Connector 与看板之间的唯一接缝：Connector 不直接写库，
// 由调用方（jobs 的 newapi_sync 任务）拿这些 Observation 去 Upsert。
func ToObservations(
	now time.Time, instanceID, environment string,
	stats UserStats, orders OrderSummary, channels []ChannelStatus, usages []ModelUsage,
) []ops.Observation {
	out := []ops.Observation{
		snapshotToObservation(MetricUsersTotal, instanceID, environment, stats.Snapshot, now,
			map[string]any{
				"total_users":         stats.TotalUsers,
				"active_users":        stats.ActiveUsers,
				"balance_minor_units": stats.BalanceMinorUnits,
				"currency":            stats.Currency,
			}),
		snapshotToObservation(MetricRechargeDaily, instanceID, environment, orders.Snapshot, now,
			map[string]any{
				"day":                orders.Day,
				"amount_minor_units": orders.RechargeMinorUnits,
				"currency":           orders.Currency,
				"order_count":        orders.OrderCount,
			}),
		snapshotToObservation(MetricSubscriptionDaily, instanceID, environment, orders.Snapshot, now,
			map[string]any{
				"day":                orders.Day,
				"amount_minor_units": orders.SubscriptionMinorUnits,
				"currency":           orders.Currency,
			}),
		channelsObservation(now, instanceID, environment, channels),
		modelsObservation(now, instanceID, environment, usages),
	}
	return out
}

// channelsObservation 把逐渠道状态聚合成一条指标。
//
// 聚合成一条而不是每个渠道一条：指标表的一行是「一个可以设阈值、可以画趋势
// 的量」，渠道数量会随运营增删而变，逐渠道建指标会让指标白名单变成一张
// 永远追不上的表，历史趋势也会在渠道下线那天断掉。看板要的是「有没有渠道
// 不对劲」，逐渠道明细走详情页（前端从本指标的 value 里读 channels 数组）。
func channelsObservation(
	now time.Time, instanceID, environment string, channels []ChannelStatus,
) ops.Observation {
	rows := make([]any, 0, len(channels))
	enabled, unhealthy := 0, 0
	for _, c := range channels {
		if c.Enabled {
			enabled++
		}
		if c.Unhealthy() {
			unhealthy++
		}
		row := map[string]any{
			"channel_id":     c.ChannelID,
			"name":           c.Name,
			"type":           c.Type,
			"enabled":        c.Enabled,
			"currency":       c.Currency,
			"model_count":    c.ModelCount,
			"error_rate_ppm": c.ErrorRatePPM,
			"latency_ms":     c.LatencyMS,
		}
		// 余额未配置时**不写这个键**，而不是写 0 或写 null。
		//
		// 前端因此能用「键在不在」区分「没配余额」与「余额是 0」；
		// 写成 0 会被当成真的 0（宪法 12 条），写成 null 则要求 JSON 往返
		// 全程保住 null 语义——多一个环节就多一处会把它变回 0 的地方。
		if c.BalanceMinorUnits != nil {
			row["balance_minor_units"] = *c.BalanceMinorUnits
		}
		for k, v := range c.catalogFields() {
			row[k] = v
		}
		rows = append(rows, row)
	}
	return snapshotToObservation(MetricChannelsStatus, instanceID, environment,
		aggregateSnapshot(channels, func(c ChannelStatus) Snapshot { return c.Snapshot }),
		now, map[string]any{
			"channels":                rows,
			"channel_count":           len(channels),
			"enabled_channel_count":   enabled,
			"unhealthy_channel_count": unhealthy,
			// 把判据一起写进去：看板显示「2 个异常」时，人要能问出
			// 「异常是按什么算的」并当场得到答案，而不是去翻源码。
			"unhealthy_threshold_ppm": ErrorRateUnhealthyPPM,
		})
}

// modelsObservation 把逐模型用量聚合成一条指标（理由同 channelsObservation）。
func modelsObservation(
	now time.Time, instanceID, environment string, usages []ModelUsage,
) ops.Observation {
	rows := make([]any, 0, len(usages))
	var requests int64
	for _, u := range usages {
		requests += u.RequestCount
		rows = append(rows, map[string]any{
			"model_name":           u.ModelName,
			"request_count":        u.RequestCount,
			"consumed_minor_units": u.ConsumedMinorUnits,
			"currency":             u.Currency,
		})
	}
	// 不给「消耗合计」：币种可能不一致，把不同币种的最小单位加在一起是纯粹的
	// 错数。请求数没有这个问题，所以只合计请求数（对照前端 channelTotal 的同款纪律）。
	return snapshotToObservation(MetricModelsUsage, instanceID, environment,
		aggregateSnapshot(usages, func(u ModelUsage) Snapshot { return u.Snapshot }),
		now, map[string]any{
			"models":              rows,
			"model_count":         len(usages),
			"total_request_count": requests,
		})
}
