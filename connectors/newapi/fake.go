package newapi

import (
	"context"
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// FakeOptions 控制 Fake 的行为，用于覆盖规格 §19.4 的 Connector 测试矩阵。
//
// 选项集合刻意与 sub2api.FakeOptions / invoice.FakeOptions 一一对应：
// 三个 Connector 的测试矩阵是同一套（版本/健康/错误分类/取消/部分数据/
// 能力子集），套件写法能互相参照，评审时也不用重新理解一遍「这个 Fake
// 又是怎么造故障的」。
type FakeOptions struct {
	// Version 是上游版本；空则用默认值。
	Version string
	// UnsupportedVersion 让 Version() 返回 Supported=false。
	UnsupportedVersion bool
	// Unhealthy 让 Health() 返回不健康。
	Unhealthy bool
	// FailWith 非空时，所有数据读取方法返回该分类的错误。
	FailWith connector.ErrorKind
	// Partial 让返回的数据标记为部分数据。
	Partial bool
	// ObservedAge 是观测时刻距今的时长（用于制造陈旧数据）。
	ObservedAge time.Duration
	// Latency 是每次调用的模拟延迟（用于验证上下文取消）。
	Latency time.Duration
	// MissingCapabilities 从能力清单中剔除这些项（模拟旧版本上游）。
	MissingCapabilities []registry.Capability
	// Now 可注入固定时钟，默认 time.Now。
	Now func() time.Time
}

// 操作名（进 connector.Error 的 Op，与能力清单对齐，便于按能力排查）。
const (
	opVersion      = "newapi.service.version_read"
	opHealth       = "newapi.health.read"
	opCapabilities = "newapi.capabilities"
	opUsers        = "newapi.users.read"
	opOrders       = "newapi.orders.read"
	opChannels     = "newapi.channels.read"
	opModels       = "newapi.models.usage_read"
)

// fakeCurrency 是 Fake 数据的币种。
const fakeCurrency = "CNY"

// defaultFakeVersion 是 Fake 报告的上游版本。
//
// 取自 docs/inventory/managed-systems.yaml 里 newapi-prod 的 detected_version，
// 好让 Fake 在版本形状上就贴着真实实例——XM-0038 接真实客户端时，
// 版本串长什么样（带不带 v 前缀、几段）不该是那天才发现的事。
const defaultFakeVersion = "v1.0.0-rc.25"

type fakeClient struct {
	opts FakeOptions
}

// NewFake 创建一个满足 ReadClient 契约的假实现。
//
// 它存在的意义有两层：让 XM-0038 之前的上层开发（平台页、同步任务）不被
// 真实凭据阻塞；以及作为 contracttest 套件的第一个被测实现——套件本身
// 要先被验证有效。
func NewFake(opts FakeOptions) ReadClient {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Version == "" {
		opts.Version = defaultFakeVersion
	}
	return &fakeClient{opts: opts}
}

func (f *fakeClient) now() time.Time { return f.opts.Now().UTC() }

func (f *fakeClient) observedAt() time.Time {
	return f.now().Add(-f.opts.ObservedAge)
}

func (f *fakeClient) snapshot() Snapshot {
	return Snapshot{
		ObservedAt: f.observedAt(),
		Watermark:  fmt.Sprintf("wm-%d", f.observedAt().Unix()),
		IsPartial:  f.opts.Partial,
	}
}

// wait 模拟延迟，同时尊重上下文取消——契约要求取消时立即返回。
func (f *fakeClient) wait(ctx context.Context) error {
	if f.opts.Latency <= 0 {
		return ctx.Err()
	}
	select {
	case <-time.After(f.opts.Latency):
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeClient) fail(op string) error {
	if f.opts.FailWith == "" {
		return nil
	}
	return connector.NewError(f.opts.FailWith, op, nil)
}

func (f *fakeClient) Version(ctx context.Context) (connector.VersionInfo, error) {
	if err := f.wait(ctx); err != nil {
		return connector.VersionInfo{}, connector.NewError(connector.KindUnavailable, opVersion, err)
	}
	return connector.VersionInfo{
		Detected:    f.opts.Version,
		Fingerprint: "fake-" + f.opts.Version,
		// 不支持的版本返回 Supported=false 而非报错——由调用方决定是否 Fail Closed
		Supported:  !f.opts.UnsupportedVersion,
		DetectedAt: f.now(),
	}, nil
}

func (f *fakeClient) Health(ctx context.Context) (connector.HealthResult, error) {
	if err := f.wait(ctx); err != nil {
		return connector.HealthResult{}, connector.NewError(connector.KindUnavailable, opHealth, err)
	}
	if f.opts.Unhealthy {
		return connector.HealthResult{
			Healthy:   false,
			CheckedAt: f.now(),
			LatencyMS: f.opts.Latency.Milliseconds(),
			ErrorKind: connector.KindUnavailable,
			// 安全的简短说明，不含上游原始错误文本
			Detail: "upstream health endpoint returned non-2xx",
		}, nil
	}
	return connector.HealthResult{
		Healthy:   true,
		CheckedAt: f.now(),
		LatencyMS: f.opts.Latency.Milliseconds(),
	}, nil
}

func (f *fakeClient) Capabilities(ctx context.Context) ([]registry.Capability, error) {
	if err := f.wait(ctx); err != nil {
		return nil, connector.NewError(connector.KindUnavailable, opCapabilities, err)
	}
	missing := make(map[registry.Capability]struct{}, len(f.opts.MissingCapabilities))
	for _, c := range f.opts.MissingCapabilities {
		missing[c] = struct{}{}
	}
	out := make([]registry.Capability, 0, len(ReadCapabilities))
	for _, c := range ReadCapabilities {
		if _, gone := missing[c]; gone {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeClient) UserStats(ctx context.Context) (UserStats, error) {
	if err := f.wait(ctx); err != nil {
		return UserStats{}, connector.NewError(connector.KindUnavailable, opUsers, err)
	}
	if err := f.fail(opUsers); err != nil {
		return UserStats{}, err
	}
	return UserStats{
		Snapshot:          f.snapshot(),
		TotalUsers:        1_284,
		ActiveUsers:       176,
		BalanceMinorUnits: 92_450_00,
		Currency:          fakeCurrency,
	}, nil
}

func (f *fakeClient) DailyOrders(ctx context.Context, day string) (OrderSummary, error) {
	if err := f.wait(ctx); err != nil {
		return OrderSummary{}, connector.NewError(connector.KindUnavailable, opOrders, err)
	}
	if err := ValidateBusinessDay(day); err != nil {
		return OrderSummary{}, connector.NewError(connector.KindBadResponse, opOrders, err)
	}
	if err := f.fail(opOrders); err != nil {
		return OrderSummary{}, err
	}
	return OrderSummary{
		Snapshot:               f.snapshot(),
		Day:                    day,
		RechargeMinorUnits:     8_642_00,
		SubscriptionMinorUnits: 3_180_00,
		OrderCount:             94,
		Currency:               fakeCurrency,
	}, nil
}

// fakeBalance 是给 *int64 字段造值的小工具。
//
// 存在的唯一理由是 Go 不能对字面量取地址；单列一个函数比在数据表里到处写
// 临时变量干净，也让「哪几条是 nil」在数据表里一眼看得出来。
func fakeBalance(minorUnits int64) *int64 { return &minorUnits }

func (f *fakeClient) Channels(ctx context.Context) ([]ChannelStatus, error) {
	if err := f.wait(ctx); err != nil {
		return nil, connector.NewError(connector.KindUnavailable, opChannels, err)
	}
	if err := f.fail(opChannels); err != nil {
		return nil, err
	}
	snap := f.snapshot()
	// 固定 6 条，刻意覆盖三种「看板必须画对」的边界：
	//   ch-3  Enabled=false —— 停用渠道不该被计进异常数（见 ChannelStatus.Unhealthy）
	//   ch-4  错误率高      —— 唯一一条越过 ErrorRateUnhealthyPPM 的启用渠道
	//   ch-5  余额 nil      —— 「未配置余额」而不是「余额为 0」
	// 数据固定不随机：Fake 的意义是让上层不被真实凭据阻塞，不是模拟真实波动。
	// 随机化只会让「这条数据是假的」更难被看出来。
	return []ChannelStatus{
		{Snapshot: snap, ChannelID: "ch-1", Name: "openai-main", Type: "openai",
			Enabled: true, BalanceMinorUnits: fakeBalance(31_500_00), Currency: fakeCurrency,
			ModelCount: 12, ErrorRatePPM: 1_200, LatencyMS: 480},
		{Snapshot: snap, ChannelID: "ch-2", Name: "anthropic-main", Type: "anthropic",
			Enabled: true, BalanceMinorUnits: fakeBalance(18_900_00), Currency: fakeCurrency,
			ModelCount: 8, ErrorRatePPM: 350, LatencyMS: 620},
		{Snapshot: snap, ChannelID: "ch-3", Name: "gemini-backup", Type: "gemini",
			Enabled: false, BalanceMinorUnits: fakeBalance(4_200_00), Currency: fakeCurrency,
			ModelCount: 5, ErrorRatePPM: 0, LatencyMS: 0},
		{Snapshot: snap, ChannelID: "ch-4", Name: "relay-cheap", Type: "custom",
			Enabled: true, BalanceMinorUnits: fakeBalance(760_00), Currency: fakeCurrency,
			ModelCount: 21, ErrorRatePPM: 187_500, LatencyMS: 2_340},
		{Snapshot: snap, ChannelID: "ch-5", Name: "self-hosted-llama", Type: "ollama",
			Enabled: true, BalanceMinorUnits: nil, Currency: fakeCurrency,
			ModelCount: 3, ErrorRatePPM: 2_500, LatencyMS: 150},
		{Snapshot: snap, ChannelID: "ch-6", Name: "azure-east", Type: "azure",
			Enabled: true, BalanceMinorUnits: fakeBalance(12_040_00), Currency: fakeCurrency,
			ModelCount: 9, ErrorRatePPM: 8_900, LatencyMS: 710},
	}, nil
}

func (f *fakeClient) ModelUsages(ctx context.Context, day string) ([]ModelUsage, error) {
	if err := f.wait(ctx); err != nil {
		return nil, connector.NewError(connector.KindUnavailable, opModels, err)
	}
	if err := ValidateBusinessDay(day); err != nil {
		return nil, connector.NewError(connector.KindBadResponse, opModels, err)
	}
	if err := f.fail(opModels); err != nil {
		return nil, err
	}
	snap := f.snapshot()
	return []ModelUsage{
		{Snapshot: snap, ModelName: "gpt-4o", RequestCount: 18_420,
			ConsumedMinorUnits: 4_310_00, Currency: fakeCurrency},
		{Snapshot: snap, ModelName: "claude-sonnet-4", RequestCount: 12_060,
			ConsumedMinorUnits: 3_770_00, Currency: fakeCurrency},
		{Snapshot: snap, ModelName: "gemini-2.5-pro", RequestCount: 5_130,
			ConsumedMinorUnits: 980_00, Currency: fakeCurrency},
		{Snapshot: snap, ModelName: "llama-3.3-70b", RequestCount: 2_740,
			ConsumedMinorUnits: 0, Currency: fakeCurrency},
	}, nil
}
