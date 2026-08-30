package sub2api

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
func NewFake(opts FakeOptions) PaymentsReadClient {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Version == "" {
		opts.Version = "0.1.152"
	}
	return &fakeClient{opts: opts}
}

func (f *fakeClient) ChannelDirectory(ctx context.Context) (ManagedChannelDirectory, error) {
	balances, err := f.ChannelBalances(ctx)
	if err != nil {
		return ManagedChannelDirectory{}, err
	}
	items := make([]ManagedChannel, 0, len(balances))
	for _, balance := range balances {
		value := balance.BalanceMinorUnits
		status := "disabled"
		if balance.TokenValid {
			status = "active"
		}
		items = append(items, ManagedChannel{
			Snapshot: balance.Snapshot, ChannelID: balance.ChannelID, Name: balance.ChannelName,
			Status: status, BalanceMinorUnits: &value, Currency: balance.Currency,
		})
	}
	reported := int64(len(items))
	return ManagedChannelDirectory{
		Snapshot: f.snapshot(),
		Completeness: DirectoryCompleteness{
			Complete: true, ReportedCount: &reported, FetchedCount: reported,
			Evidence: "fake_reported_count",
		},
		CoveragePartial: f.opts.Partial,
		Items:           items,
	}, nil
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

// fakeOrderMethods 循环用于 fakeOrders 生成的支付方式，取值取自
// K:/sub2api-src internal/payment/types.go 的 PaymentType 常量。
var fakeOrderMethods = []string{"alipay", "wxpay", "stripe", "card", "link", "easypay", "airwallex"}

// fakeOrders 生成 [from,to] 窗口内均匀分布的确定性订单，逐个状态各一笔
// （KnownOrderStatuses 全覆盖），供 ListOrders/DailyPaymentSummary 的假实现
// 共用——真实客户端里两者也共用同一个 fetchOrders，这里保持同一个形状。
//
// 时间戳锚定在调用方给的窗口内而不是某个固定的"现在"：这样无论前端联调时
// 查询哪一天/哪个区间，假数据都会落进窗口里，而不是只在"最近几天"内可见。
func fakeOrders(from, to time.Time) []Order {
	span := to.Sub(from)
	n := len(KnownOrderStatuses)
	items := make([]Order, 0, n)
	for i, status := range KnownOrderStatuses {
		offset := time.Duration(int64(span) * int64(i) / int64(n))
		items = append(items, Order{
			OrderID:          fmt.Sprintf("9%03d", i),
			CreatedAt:        from.Add(offset),
			Status:           status,
			AmountMinorUnits: int64(1_000 * (i + 1)),
			Currency:         "CNY",
			Method:           fakeOrderMethods[i%len(fakeOrderMethods)],
			UserRef:          fmt.Sprintf("fa***@example.com"),
			UpstreamOrderRef: fmt.Sprintf("FAKE-SUB2API-%04d", 9000+i),
		})
	}
	return items
}

// ListOrders 见 PaymentsReadClient；假实现在 [filter.From, filter.To] 内
// 生成确定性订单并按 filter.Status 过滤。
func (f *fakeClient) ListOrders(ctx context.Context, filter OrderFilter) (OrderPage, error) {
	const op = "sub2api.orders.read"
	if err := f.wait(ctx); err != nil {
		return OrderPage{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	if filter.From.IsZero() || filter.To.IsZero() {
		return OrderPage{}, connector.NewError(connector.KindBadResponse, op, errors.New("from/to 必须非零"))
	}
	if filter.To.Before(filter.From) {
		return OrderPage{}, connector.NewError(connector.KindBadResponse, op, errors.New("to 不能早于 from"))
	}
	status := strings.ToUpper(strings.TrimSpace(filter.Status))
	if status != "" && !KnownOrderStatus(status) {
		return OrderPage{}, connector.NewError(connector.KindBadResponse, op,
			fmt.Errorf("status %q 不是已知的上游状态", filter.Status))
	}
	if err := f.fail(op); err != nil {
		return OrderPage{}, err
	}

	items := make([]Order, 0)
	stats := make(map[string]OrderStats)
	for _, o := range fakeOrders(filter.From, filter.To) {
		if status != "" && o.Status != status {
			continue
		}
		items = append(items, o)
		s := stats[o.Status]
		s.Count++
		s.AmountMinorUnits += o.AmountMinorUnits
		stats[o.Status] = s
	}
	return OrderPage{Snapshot: f.snapshot(), Items: items, StatsByStatus: stats}, nil
}

// DailyPaymentSummary 见 PaymentsReadClient；假实现按业务日窗口生成确定性
// 订单并归一化分桶（与 paymentStatusBucket 同一套判据，见 payments.go）。
func (f *fakeClient) DailyPaymentSummary(ctx context.Context, day string) (DailyPaymentSummary, error) {
	const op = "sub2api.orders.read"
	if err := f.wait(ctx); err != nil {
		return DailyPaymentSummary{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	parsed, err := time.Parse(sub2apiBusinessDayLayout, day)
	if err != nil {
		return DailyPaymentSummary{}, connector.NewError(connector.KindBadResponse, op, err)
	}
	if err := f.fail(op); err != nil {
		return DailyPaymentSummary{}, err
	}

	from := parsed
	to := parsed.AddDate(0, 0, 1).Add(-time.Nanosecond)
	byStatus := make(map[string]StatusAmount)
	var fee int64
	for _, o := range fakeOrders(from, to) {
		bucket, ok := paymentStatusBucket(o.Status)
		if !ok {
			continue
		}
		s := byStatus[bucket]
		s.Count++
		s.AmountMinorUnits += o.AmountMinorUnits
		byStatus[bucket] = s
		if bucket == PaymentStatusSucceeded || bucket == PaymentStatusRefunded {
			// 假数据里没有真实的 pay_amount/amount 差值，用一个明显是虚构的
			// 固定比例（1%）撑出一个非零手续费，让消费方看得到这个字段非空。
			fee += o.AmountMinorUnits / 100
		}
	}
	return DailyPaymentSummary{
		Snapshot:      f.snapshot(),
		Day:           parsed.Format(sub2apiBusinessDayLayout),
		Currency:      "CNY",
		ByStatus:      byStatus,
		FeeMinorUnits: &fee,
		NetMinorUnits: nil,
	}, nil
}
