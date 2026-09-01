package sub2api

import (
	"context"
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// ManagedChannel 是一个上游账号的目录行（v2 起为渠道余额，v3 起为完整目录字段，
// 见 contracts/connectors/sub2api.channel-catalog.v3.md）。
//
// v3 新增字段全部可空：每个字段要么能追到 K:/sub2api-src 的具体字段/端点
// （契约文档逐项列出来源），要么恒为 nil 并在契约里写明原因——不做近似
// （XM-CHAN-FIELDS0 任务硬约束）。旧字段（ChannelID..Currency）语义不变。
type ManagedChannel struct {
	Snapshot
	ChannelID         string
	Name              string
	Status            string
	BalanceMinorUnits *int64
	Currency          string

	// Kind 区分订阅型账号（oauth/setup-token 登录，如 Claude Code Max）与
	// 密钥型账号（apikey/upstream/bedrock/service_account）。取值
	// "subscription" | "upstream"；来源账号的 Type 出现本包未登记的新取值时
	// 为 nil，而不是猜一个桶（见 classifyAccountKind）。
	Kind *string
	// Vendor 是上游 Platform 字段原样透传（anthropic/openai/gemini/
	// antigravity/grok/kimi/zhipu/deepseek/composite，含历史值 kiro）。
	Vendor *string

	// CapacityUsed 是当前并发数（AccountWithConcurrency.current_concurrency，
	// 实时值，与账号列表同一次请求返回，无需额外调用）。
	CapacityUsed *int64
	// CapacityLimit 是有效并发上限：LoadFactor 已配置且 >0 时取 LoadFactor，
	// 否则取 Concurrency——与上游 EffectiveLoadFactor() 同一条判据
	// （K:/sub2api-src backend/internal/service/account.go:168-179）。
	CapacityLimit *int64

	// SchedulingEnabled 对应上游 Schedulable（粗粒度调度开关）。
	SchedulingEnabled *bool
	// SchedulingPriority 对应上游 Priority（数值越小优先级越高，
	// 账号级；不是 AccountGroup 的分组内 Priority）。
	SchedulingPriority *int64

	// Today* 来自 /api/v1/admin/accounts/:id/today-stats 的 WindowStats，
	// 预算内逐账号探测（见 upstream.go fetchAccountTodayStats）。
	// TodayRequests 是当日请求数；TodaySuccessRatePPM 恒为 nil——WindowStats/
	// AccountUsageStatsResponse 在账号维度都不带成功率或失败数（已核对
	// K:/sub2api-src 全部相关类型，无该字段）。TodayCostMinorUnits 取
	// WindowStats.Cost（账号口径费用，已计入该账号自己的 rate_multiplier，
	// 见 account_usage_service.go:135-138 的字段注释），不是 standard_cost
	// （不含倍率，跨账号比较用）或 user_cost（用户/Key 口径，收入侧）。
	TodayRequests       *int64
	TodaySuccessRatePPM *int64
	TodayCostMinorUnits *int64
	TodayCurrency       *string
	TodayScale          *int

	// UsageWindow* 仅对 Kind=="subscription" 的账号可能非空，其余恒为 nil
	// （任务要求：key 账号的 usage_window 必须是 null）。
	//
	// UsedRatioPPM 是「当前窗口费用 / 窗口费用上限」（CurrentWindowCost /
	// WindowCostLimit，均来自与账号列表同一次响应，无需额外调用，且全程按
	// 整数分换算再做整数比例——不经 float64，与 ErrorRatePPM 同一条纪律：
	// 比率属于宪法 13 条"比例使用 Decimal"的范围，不用浮点），仅当
	// WindowCostLimit 已配置且 >0 时给出；允许超过 1_000_000（费用超出上限是
	// 真实场景，不钳位）。**这不是** Anthropic 自己上报的 5 小时窗口用量百分比
	// （那个数字来自逐账号实时调用 /api/v1/admin/accounts/:id/usage，会对每个
	// 订阅账号各打一次 Anthropic 官方用量接口——本片不对目录读取做这种未设
	// 预算的按账号扇出，见 contracts/connectors/sub2api.channel-catalog.v3.md
	// 的取舍说明）。
	// ResetsAt 取 SessionWindowEnd（本地追踪的会话窗口结束时刻，非空即视为
	// 一个真实的、已排定的重置时刻，不看 SessionWindowStatus 的具体取值）。
	UsageWindowUsedRatioPPM *int64
	UsageWindowResetsAt     *time.Time

	// ProxyLabel 取上游 Proxy.Name（人工填写的展示名，ent schema 明确不含
	// 凭据；上游的 mapper 也从不把 Password 拷进响应）。账号未配置代理时为
	// nil。**绝不**暴露 Host/Username/完整代理 URL——宪法 7 条 + 任务约束。
	ProxyLabel *string

	// RateMultiplierPPM 是本地计费倍率（accounts.rate_multiplier，账号维度
	// 计费口径，可由人工设置，也可能是 UpstreamMultiplierPPM 自动回写的镜像，
	// 见该字段注释），按 ppm 精确表达（上游列是 decimal(10,4)，本包按
	// decimalToMinorUnits 精确解析后再换算到 ppm 精度，全程不经 float64）。
	RateMultiplierPPM *int64
	// UpstreamMultiplierPPM 是上游账号自己上报的计费倍率探测结果
	// （accounts.extra["upstream_billing_probe"].data.resolved_rate_multiplier，
	// 只在该账号启用过「上游计费探测」功能时才存在，多数账号会是 nil）。
	// 它与 RateMultiplierPPM 是两个独立存储：只有账号开启同步开关时，探测值
	// 才会被写回 RateMultiplierPPM；本字段读的是探测原始值，不受该开关影响。
	// ⚠️ 精度上限：这个值来自 extra 整体解成 map[string]any 之后的原生
	// float64（encoding/json 把任意数值字面量解进 any 都会变成 float64，
	// 无法绕开），换算到 ppm 时四舍五入——这是 extra 这个"上游沙箱字段"
	// 本身的精度上限，不是本包新引入的误差。
	UpstreamMultiplierPPM *int64

	LastUsedAt *time.Time
	CreatedAt  *time.Time
	// ExpiresAt 是账号过期时间（NULL 表示不过期），覆盖 OAuth/手动/订阅周期
	// 到期等全部场景——上游只有这一个统一字段，没有分场景的独立到期字段。
	ExpiresAt *time.Time
}

type DirectoryCompleteness struct {
	Complete      bool
	Truncated     bool
	ReportedCount *int64
	FetchedCount  int64
	Evidence      string
}

type ManagedChannelDirectory struct {
	Snapshot
	Completeness    DirectoryCompleteness
	CoveragePartial bool
	Items           []ManagedChannel
}

type ReadClientV2 interface {
	ReadClient
	ChannelDirectory(context.Context) (ManagedChannelDirectory, error)
}

// subscriptionAccountTypes / keyAccountTypes 是账号 Type 到 Kind 的分桶，
// 依据 K:/sub2api-src backend/internal/domain/constants.go:52-57 的六个
// AccountType* 常量逐个核对：oauth/setup-token 是登录态订阅账号（Claude Code
// Max、ChatGPT Plus 等）；apikey/upstream/bedrock/service_account 是凭据型
// 账号（原始 API Key、上游透传、AWS SigV4/Key、GCP service account）。
// 未出现在任何一桶里的取值（未来上游新增类型）返回 ok=false，调用方据此把
// Kind 置 nil，不猜一个桶。
var subscriptionAccountTypes = map[string]bool{
	"oauth":       true,
	"setup-token": true,
}

var keyAccountTypes = map[string]bool{
	"apikey":          true,
	"upstream":        true,
	"bedrock":         true,
	"service_account": true,
}

const (
	channelKindSubscription = "subscription"
	channelKindUpstream     = "upstream"
)

// ppmToFraction 把 ppm（百万分之一）整数换算成 JSON 数值型的小数——只在
// 对外呈现这一步、只做一次除法；内部的解析、换算、比较全部停留在整数 ppm
// 上（宪法 13 条"比例使用 Decimal"，ErrorRatePPM 同一条纪律），单次终值
// 换算不会引入那条纪律真正想避免的累积误差。
func ppmToFraction(ppm int64) float64 {
	return float64(ppm) / 1_000_000
}

// classifyAccountKind 把上游账号 Type 映射成 "subscription" | "upstream"。
func classifyAccountKind(accountType string) *string {
	switch {
	case subscriptionAccountTypes[accountType]:
		v := channelKindSubscription
		return &v
	case keyAccountTypes[accountType]:
		v := channelKindUpstream
		return &v
	default:
		return nil
	}
}

func LegacyChannelBalances(directory ManagedChannelDirectory) []ChannelBalance {
	out := make([]ChannelBalance, 0, len(directory.Items))
	skipped := 0
	for _, item := range directory.Items {
		if item.BalanceMinorUnits == nil {
			skipped++
			continue
		}
		snapshot := item.Snapshot
		snapshot.IsPartial = snapshot.IsPartial || skipped > 0 || !directory.Completeness.Complete
		out = append(out, ChannelBalance{
			Snapshot: snapshot, ChannelID: item.ChannelID, ChannelName: item.Name,
			BalanceMinorUnits: *item.BalanceMinorUnits, Currency: item.Currency,
			TokenValid: item.Status == "active",
		})
	}
	if skipped > 0 {
		for i := range out {
			out[i].IsPartial = true
			out[i].Watermark = fmt.Sprintf("%s/skipped_no_balance:%d", out[i].Watermark, skipped)
		}
	}
	return out
}

// catalogFields 把 ManagedChannel 的 v3 目录字段编码成观测值的一个嵌套
// map——形状与 httpapi 最终要暴露的 JSON 一一对应（platform_channels.go
// 从同一个形状里原样取回），中间不经过任何第二次转译。
//
// 与既有字段同一条纪律：某个维度完全没有数据时**不写这个键**，而不是写
// null——见 newapi/contract.go channelsObservation 的同款注释。
func (item ManagedChannel) catalogFields() map[string]any {
	row := map[string]any{}
	if item.Kind != nil {
		row["kind"] = *item.Kind
	}
	if item.Vendor != nil {
		row["vendor"] = *item.Vendor
	}
	if item.CapacityUsed != nil || item.CapacityLimit != nil {
		capacity := map[string]any{}
		if item.CapacityUsed != nil {
			capacity["used"] = *item.CapacityUsed
		}
		if item.CapacityLimit != nil {
			capacity["limit"] = *item.CapacityLimit
		}
		row["capacity"] = capacity
	}
	if item.SchedulingEnabled != nil || item.SchedulingPriority != nil {
		scheduling := map[string]any{}
		if item.SchedulingEnabled != nil {
			scheduling["enabled"] = *item.SchedulingEnabled
		}
		if item.SchedulingPriority != nil {
			scheduling["priority"] = *item.SchedulingPriority
		}
		row["scheduling"] = scheduling
	}
	if item.TodayRequests != nil || item.TodaySuccessRatePPM != nil || item.TodayCostMinorUnits != nil {
		today := map[string]any{}
		if item.TodayRequests != nil {
			today["requests"] = *item.TodayRequests
		}
		if item.TodaySuccessRatePPM != nil {
			today["success_rate"] = ppmToFraction(*item.TodaySuccessRatePPM)
		}
		if item.TodayCostMinorUnits != nil {
			today["cost_minor_units"] = *item.TodayCostMinorUnits
			if item.TodayCurrency != nil {
				today["currency"] = *item.TodayCurrency
			}
			if item.TodayScale != nil {
				today["scale"] = *item.TodayScale
			}
		}
		row["today"] = today
	}
	if item.UsageWindowUsedRatioPPM != nil || item.UsageWindowResetsAt != nil {
		window := map[string]any{}
		if item.UsageWindowUsedRatioPPM != nil {
			window["used_ratio"] = ppmToFraction(*item.UsageWindowUsedRatioPPM)
		}
		if item.UsageWindowResetsAt != nil {
			window["resets_at"] = item.UsageWindowResetsAt.UTC().Format(time.RFC3339Nano)
		}
		row["usage_window"] = window
	}
	if item.ProxyLabel != nil {
		row["proxy"] = *item.ProxyLabel
	}
	if item.RateMultiplierPPM != nil {
		row["rate_multiplier"] = ppmToFraction(*item.RateMultiplierPPM)
	}
	if item.UpstreamMultiplierPPM != nil {
		row["upstream_multiplier"] = ppmToFraction(*item.UpstreamMultiplierPPM)
	}
	if item.LastUsedAt != nil {
		row["last_used_at"] = item.LastUsedAt.UTC().Format(time.RFC3339Nano)
	}
	if item.CreatedAt != nil {
		row["created_at"] = item.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	if item.ExpiresAt != nil {
		row["expires_at"] = item.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return row
}

func ToChannelDirectoryObservation(
	now time.Time, instanceID, environment string, directory ManagedChannelDirectory,
) ops.Observation {
	items := make([]any, 0, len(directory.Items))
	for _, item := range directory.Items {
		row := map[string]any{
			"channel_id": item.ChannelID, "name": item.Name, "status": item.Status,
			"currency": item.Currency,
		}
		if item.BalanceMinorUnits != nil {
			row["balance_minor_units"] = *item.BalanceMinorUnits
		}
		for k, v := range item.catalogFields() {
			row[k] = v
		}
		items = append(items, row)
	}
	completeness := map[string]any{
		"complete":       directory.Completeness.Complete,
		"truncated":      directory.Completeness.Truncated,
		"reported_count": directory.Completeness.ReportedCount,
		"fetched_count":  directory.Completeness.FetchedCount,
		"evidence":       directory.Completeness.Evidence,
	}
	return snapshotToObservation(
		MetricChannelsStatus, instanceID, environment, directory.Snapshot, now,
		map[string]any{
			"channels":               items,
			"channel_count":          len(items),
			"inventory_completeness": completeness,
			"coverage_partial":       directory.CoveragePartial,
		},
	)
}
