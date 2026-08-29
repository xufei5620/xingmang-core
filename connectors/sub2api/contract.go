// Package sub2api 定义 Sub2API 的只读接入契约（规格 §8.3 Foundation-A 只读范围）。
//
// 本包**只声明契约与提供 Fake**，不含任何真实网络实现——真实实现是 XM-0017。
// 任何实现（Fake 或真实）都必须通过 contracttest 套件才算合规。
//
// 铁律：
//   - ReadClient 接口**只有读方法**。写能力在 Foundation-B 后另立接口，
//     不得往本接口上加方法（ADR-018 闸 4）；
//   - 每个返回值都带 ObservedAt 与 Watermark（规格 §9.1：禁止裸数字）；
//   - 金额一律整数最小货币单位 + Currency，禁止 float（规格 §5.9）。
package sub2api

import (
	"context"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

const (
	// ConnectorKey 是本 Connector 在 Registry 中的键。
	ConnectorKey      = "sub2api"
	ContractVersionV1 = "1"
	ContractVersionV2 = "2"
	// ContractVersion 指向当前 Registry 版本；v2 嵌入 v1，旧调用仍兼容。
	ContractVersion = ContractVersionV2
)

// ReadCapabilities 是 Foundation-A 阶段的只读能力清单（规格 §8.2、§8.3）。
//
// 每一项都必须能被 registry.ParseCapability 解析，且 IsWrite() 为 false——
// contracttest 会断言这一点，让「只读」成为可验证的属性而非口头承诺。
var ReadCapabilities = []registry.Capability{
	"sub2api.service.version_read",
	"sub2api.users.read",
	"sub2api.users.balance_read",
	"sub2api.orders.read",
	"sub2api.groups.read",
	"sub2api.accounts.read",
	"sub2api.models.usage_read",
	"sub2api.channels.balance_read",
	"sub2api.channels.read",
	"sub2api.health.read",
}

// Snapshot 是每个读取结果都必须携带的新鲜度元数据（规格 §9.1）。
//
// 没有「只返回值不返回快照信息」的数据结构——裸数字在类型层面就不存在。
type Snapshot struct {
	// ObservedAt 是上游数据的观测时刻，不是本地接收时刻。
	ObservedAt time.Time
	// Watermark 是上游的数据水位，用于判断是否读到了完整区间。
	Watermark string
	// IsPartial 表示本次只读到了部分数据（如分页中断、部分渠道超时）。
	IsPartial bool
}

// UserStats 是用户与余额概览（规格 §8.3：注册用户、用户余额和透支）。
type UserStats struct {
	Snapshot
	TotalUsers  int64
	ActiveUsers int64
	// 金额一律整数最小货币单位（分），禁止 float（规格 §5.9）
	BalanceMinorUnits   int64
	OverdraftMinorUnits int64
	Currency            string
}

// OrderSummary 是某一自然日的收入成本摘要（规格 §8.3：收入和订单摘要）。
type OrderSummary struct {
	Snapshot
	// Day 是业务日，格式 2006-01-02，时区由平台的业务日结时区决定（规格 §5.9）。
	Day               string
	RevenueMinorUnits int64
	CostMinorUnits    int64
	Currency          string
	OrderCount        int64
}

// ChannelBalance 是一个上游渠道的余额与可用性（规格 §8.3：渠道余额）。
type ChannelBalance struct {
	Snapshot
	ChannelID         string
	ChannelName       string
	BalanceMinorUnits int64
	Currency          string
	// TokenValid 是能力证据而非身份凭证（ADR-010 的同类纪律）。
	TokenValid bool
}

// ReadClient 是 Sub2API 的只读接入契约。
//
// **只有读方法。** 写能力在 Foundation-B 后另立接口（如 WriteClient），
// 绝不往本接口加方法——ADR-018 闸 4 在这里体现为接口形状本身。
type ReadClient interface {
	// Version 探测上游版本并给出是否在兼容矩阵内。
	// 不支持的版本返回 Supported=false 而非报错：是否 Fail Closed 由调用方
	// 按场景决定（读取可降级，写入必须停）。
	Version(ctx context.Context) (connector.VersionInfo, error)

	// Health 返回运营健康状态；失败时用结构化 ErrorKind，不透传上游原始错误。
	Health(ctx context.Context) (connector.HealthResult, error)

	// Capabilities 返回本连接当前实际可用的能力（可能因上游版本而少于 ReadCapabilities）。
	Capabilities(ctx context.Context) ([]registry.Capability, error)

	// UserStats 读取用户与余额概览。
	UserStats(ctx context.Context) (UserStats, error)

	// DailyOrders 读取某业务日（2006-01-02）的收入成本摘要。
	DailyOrders(ctx context.Context, day string) (OrderSummary, error)

	// ChannelBalances 读取全部上游渠道余额。
	ChannelBalances(ctx context.Context) ([]ChannelBalance, error)
}

// 指标键（写进 ops.metric_observation 的 metric_key）。
const (
	MetricUsersTotal     = "sub2api.users.total"
	MetricUsersBalance   = "sub2api.users.balance"
	MetricRevenueDaily   = "sub2api.revenue.daily"
	MetricCostDaily      = "sub2api.cost.daily"
	MetricChannelBalance = "sub2api.channels.balance"
	MetricChannelsStatus = "sub2api.channels.status"
)

// defaultStalenessThresholdSeconds 是这批指标的默认新鲜度阈值。
// 30 分钟对齐 SoloAI 既有的快照周期（ADR-001：SoloAI 作为口径来源）。
const defaultStalenessThresholdSeconds int32 = 1800

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
		StalenessThresholdSeconds: defaultStalenessThresholdSeconds,
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

// ToObservations 把契约数据转成新鲜度模型，接进看板（规格 §9.1）。
//
// 这是 Connector 与看板之间的唯一接缝：Connector 不直接写库，
// 由调用方（Worker 任务）拿这些 Observation 去 Upsert。
func ToObservations(
	now time.Time, instanceID, environment string,
	stats UserStats, orders OrderSummary, balances []ChannelBalance,
) []ops.Observation {
	out := []ops.Observation{
		snapshotToObservation(MetricUsersTotal, instanceID, environment, stats.Snapshot, now,
			map[string]any{"total_users": stats.TotalUsers, "active_users": stats.ActiveUsers}),
		snapshotToObservation(MetricUsersBalance, instanceID, environment, stats.Snapshot, now,
			map[string]any{
				"balance_minor_units":   stats.BalanceMinorUnits,
				"overdraft_minor_units": stats.OverdraftMinorUnits,
				"currency":              stats.Currency,
			}),
		snapshotToObservation(MetricRevenueDaily, instanceID, environment, orders.Snapshot, now,
			map[string]any{
				"day":                orders.Day,
				"amount_minor_units": orders.RevenueMinorUnits,
				"currency":           orders.Currency,
				"order_count":        orders.OrderCount,
			}),
		snapshotToObservation(MetricCostDaily, instanceID, environment, orders.Snapshot, now,
			map[string]any{
				"day":                orders.Day,
				"amount_minor_units": orders.CostMinorUnits,
				"currency":           orders.Currency,
			}),
	}

	// 渠道余额聚合成一条指标：看板要的是「有没有渠道快没钱了」，
	// 逐渠道明细走详情页而非总览指标
	channels := make([]any, 0, len(balances))
	anyPartial := false
	oldest := time.Time{}
	watermark := ""
	for _, b := range balances {
		channels = append(channels, map[string]any{
			"channel_id":          b.ChannelID,
			"channel_name":        b.ChannelName,
			"balance_minor_units": b.BalanceMinorUnits,
			"currency":            b.Currency,
			"token_valid":         b.TokenValid,
		})
		if b.IsPartial {
			anyPartial = true
		}
		// 取最旧的观测时刻：聚合指标的新鲜度由最不新鲜的那一项决定
		if !b.ObservedAt.IsZero() && (oldest.IsZero() || b.ObservedAt.Before(oldest)) {
			oldest = b.ObservedAt
			watermark = b.Watermark
		}
	}
	out = append(out, snapshotToObservation(
		MetricChannelBalance, instanceID, environment,
		Snapshot{ObservedAt: oldest, Watermark: watermark, IsPartial: anyPartial},
		now, map[string]any{"channels": channels, "channel_count": len(balances)}))

	return out
}
