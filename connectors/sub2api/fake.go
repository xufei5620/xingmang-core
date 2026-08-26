package sub2api

import (
	"context"
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// FakeOptions 控制 Fake 的行为，用于覆盖规格 §19.4 的 Connector 测试矩阵。
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

type fakeClient struct {
	opts FakeOptions
}

// NewFake 创建一个满足 ReadClient 契约的假实现。
//
// 它存在的意义有两层：让 XM-0017 之前的上层开发不被真实凭据阻塞；
// 以及作为 contracttest 套件的第一个被测实现——套件本身要先被验证有效。
func NewFake(opts FakeOptions) ReadClient {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Version == "" {
		opts.Version = "0.1.152"
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
		return connector.VersionInfo{}, connector.NewError(connector.KindUnavailable, "sub2api.version", err)
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
		return connector.HealthResult{}, connector.NewError(connector.KindUnavailable, "sub2api.health", err)
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
		return nil, connector.NewError(connector.KindUnavailable, "sub2api.capabilities", err)
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
		return UserStats{}, connector.NewError(connector.KindUnavailable, "sub2api.users.read", err)
	}
	if err := f.fail("sub2api.users.read"); err != nil {
		return UserStats{}, err
	}
	return UserStats{
		Snapshot:            f.snapshot(),
		TotalUsers:          2482,
		ActiveUsers:         311,
		BalanceMinorUnits:   184_236_00,
		OverdraftMinorUnits: 1_250_00,
		Currency:            "CNY",
	}, nil
}

func (f *fakeClient) DailyOrders(ctx context.Context, day string) (OrderSummary, error) {
	if err := f.wait(ctx); err != nil {
		return OrderSummary{}, connector.NewError(connector.KindUnavailable, "sub2api.orders.read", err)
	}
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return OrderSummary{}, connector.NewError(connector.KindBadResponse, "sub2api.orders.read", err)
	}
	if err := f.fail("sub2api.orders.read"); err != nil {
		return OrderSummary{}, err
	}
	return OrderSummary{
		Snapshot:          f.snapshot(),
		Day:               day,
		RevenueMinorUnits: 12_345_60,
		CostMinorUnits:    7_890_10,
		Currency:          "CNY",
		OrderCount:        168,
	}, nil
}

func (f *fakeClient) ChannelBalances(ctx context.Context) ([]ChannelBalance, error) {
	if err := f.wait(ctx); err != nil {
		return nil, connector.NewError(connector.KindUnavailable, "sub2api.channels.balance_read", err)
	}
	if err := f.fail("sub2api.channels.balance_read"); err != nil {
		return nil, err
	}
	snap := f.snapshot()
	return []ChannelBalance{
		{Snapshot: snap, ChannelID: "ch-1", ChannelName: "upstream-a",
			BalanceMinorUnits: 45_600_00, Currency: "CNY", TokenValid: true},
		{Snapshot: snap, ChannelID: "ch-2", ChannelName: "upstream-b",
			BalanceMinorUnits: 1_200_00, Currency: "CNY", TokenValid: true},
		{Snapshot: snap, ChannelID: "ch-3", ChannelName: "upstream-c",
			BalanceMinorUnits: 0, Currency: "CNY", TokenValid: false},
	}, nil
}
