package newapi

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
func NewFake(opts FakeOptions) PaymentsReadClient {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Version == "" {
		opts.Version = defaultFakeVersion
	}
	return &fakeClient{opts: opts}
}

func (f *fakeClient) ChannelDirectory(ctx context.Context) (ChannelDirectorySnapshot, error) {
	items, err := f.Channels(ctx)
	if err != nil {
		return ChannelDirectorySnapshot{}, err
	}
	reported := int64(len(items))
	coveragePartial := false
	for _, item := range items {
		coveragePartial = coveragePartial || item.IsPartial
	}
	return ChannelDirectorySnapshot{
		Snapshot: f.snapshot(),
		Completeness: DirectoryCompleteness{
			Complete: true, ReportedCount: &reported, FetchedCount: reported,
			Evidence: "fake_reported_count",
		},
		CoveragePartial: coveragePartial,
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
	items := []ChannelStatus{
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
	}
	for i := range items {
		applyFakeCatalogFields(&items[i])
	}
	return items, nil
}

// fakeVendorNames/fakeSchedulingPriority/fakeTodayStats 是 applyFakeCatalogFields
// 的按 ChannelID 查表数据（XM-CHAN-FIELDS0）。
var fakeVendorNames = map[string]string{
	"ch-1": "OpenAI", "ch-2": "Anthropic", "ch-3": "Gemini",
	"ch-4": "Custom", "ch-5": "Ollama", "ch-6": "Azure",
}

// applyFakeCatalogFields 给固定的六条假渠道附上 XM-CHAN-FIELDS0 目录字段。
//
// Kind/CapacityUsed/CapacityLimit/UsageWindow*/RateMultiplierPPM/
// UpstreamMultiplierPPM/LastUsedAt/ExpiresAt 恒为 nil——与真实客户端一致：
// NewAPI 没有可达的上游字段支撑这些维度（见 ChannelStatus 各字段注释），
// Fake 不该在真实客户端给不出的地方凭空造数据。
//
// 数值固定不随机，理由同 Channels() 顶部注释。
func applyFakeCatalogFields(item *ChannelStatus) {
	if name, ok := fakeVendorNames[item.ChannelID]; ok {
		item.Vendor = &name
	}
	statusLabel := "enabled"
	if !item.Enabled {
		statusLabel = "manually_disabled"
	}
	item.StatusLabel = &statusLabel

	createdAt := item.Snapshot.ObservedAt.Add(-90 * 24 * time.Hour)
	item.CreatedAt = &createdAt

	switch item.ChannelID {
	case "ch-1":
		priority := int64(100)
		item.SchedulingPriority = &priority
		setFakeToday(item, 1_200, 950_000, 45_00)
	case "ch-2":
		priority := int64(90)
		item.SchedulingPriority = &priority
		label := "us-west-proxy-01"
		item.ProxyLabel = &label
		setFakeToday(item, 800, 990_000, 62_00)
	case "ch-3":
		priority := int64(10)
		item.SchedulingPriority = &priority
		// 停用渠道今天没有请求：0 是真实的零，success_rate 因分母为零留 nil。
		setFakeTodayZero(item)
	case "ch-4":
		priority := int64(50)
		item.SchedulingPriority = &priority
		setFakeToday(item, 300, 400_000, 8_00) // 错误率高，success_rate 相应偏低
	case "ch-5":
		priority := int64(20)
		item.SchedulingPriority = &priority
		setFakeToday(item, 150, 980_000, 0) // 自托管，没有上游费用
	case "ch-6":
		priority := int64(70)
		item.SchedulingPriority = &priority
		setFakeToday(item, 500, 970_000, 30_00)
	}
}

// setFakeToday 填充 Today* 字段，costMinorUnits=0 时仍然给出（区别于
// setFakeTodayZero 的"完全没有活动"）。
func setFakeToday(item *ChannelStatus, requests, successRatePPM, costMinorUnits int64) {
	item.TodayRequests = &requests
	item.TodaySuccessRatePPM = &successRatePPM
	item.TodayCostMinorUnits = &costMinorUnits
	currency, scale := fakeCurrency, 2
	item.TodayCurrency = &currency
	item.TodayScale = &scale
}

func setFakeTodayZero(item *ChannelStatus) {
	requests, cost := int64(0), int64(0)
	item.TodayRequests = &requests
	item.TodayCostMinorUnits = &cost
	currency, scale := fakeCurrency, 2
	item.TodayCurrency = &currency
	item.TodayScale = &scale
	// TodaySuccessRatePPM 留 nil：0 个请求时分母为零，没有意义的比率。
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

// fakeOrderMethods 循环用于 fakeOrders 生成的支付提供方，取值取自
// K:/newapi-src common/constants.go 的 PaymentProvider* 常量。
var fakeOrderMethods = []string{"stripe", "creem", "waffo", "epay"}

// fakeOrders 生成 [from,to] 窗口内均匀分布的确定性订单，逐个状态循环
// （KnownOrderStatuses 只有 4 个，重复几轮凑出更多笔数），供
// ListOrders/DailyPaymentSummary 的假实现共用——理由与 sub2api 的同名函数
// 相同：时间戳锚定在调用方给的窗口内，联调时查询任何区间都有数据可看。
func fakeOrders(from, to time.Time) []Order {
	const n = 8
	span := to.Sub(from)
	items := make([]Order, 0, n)
	for i := 0; i < n; i++ {
		offset := time.Duration(int64(span) * int64(i) / int64(n))
		items = append(items, Order{
			OrderID:          fmt.Sprintf("9%03d", i),
			CreatedAt:        from.Add(offset),
			Status:           KnownOrderStatuses[i%len(KnownOrderStatuses)],
			AmountMinorUnits: int64(500 * (i + 1)),
			Currency:         fakeCurrency,
			Method:           fakeOrderMethods[i%len(fakeOrderMethods)],
			UserRef:          userRef(int64(20000 + i)),
			UpstreamOrderRef: fmt.Sprintf("FAKE-NEWAPI-%04d", 9000+i),
		})
	}
	return items
}

// ListOrders 见 PaymentsReadClient；假实现在 [filter.From, filter.To] 内
// 生成确定性订单并按 filter.Status 过滤。
func (f *fakeClient) ListOrders(ctx context.Context, filter OrderFilter) (OrderPage, error) {
	if err := f.wait(ctx); err != nil {
		return OrderPage{}, connector.NewError(connector.KindUnavailable, opOrders, err)
	}
	if filter.From.IsZero() || filter.To.IsZero() {
		return OrderPage{}, connector.NewError(connector.KindBadResponse, opOrders, errors.New("from/to 必须非零"))
	}
	if filter.To.Before(filter.From) {
		return OrderPage{}, connector.NewError(connector.KindBadResponse, opOrders, errors.New("to 不能早于 from"))
	}
	status := strings.ToLower(strings.TrimSpace(filter.Status))
	if status != "" && !KnownOrderStatus(status) {
		return OrderPage{}, connector.NewError(connector.KindBadResponse, opOrders,
			fmt.Errorf("status %q 不是已知的上游状态", filter.Status))
	}
	if err := f.fail(opOrders); err != nil {
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
// 订单并归一化分桶。FeeMinorUnits/NetMinorUnits 恒为 nil，与真实客户端一致
// （NewAPI 的 TopUp 模型没有手续费/净现金流字段）。
func (f *fakeClient) DailyPaymentSummary(ctx context.Context, day string) (DailyPaymentSummary, error) {
	if err := f.wait(ctx); err != nil {
		return DailyPaymentSummary{}, connector.NewError(connector.KindUnavailable, opOrders, err)
	}
	if err := ValidateBusinessDay(day); err != nil {
		return DailyPaymentSummary{}, connector.NewError(connector.KindBadResponse, opOrders, err)
	}
	if err := f.fail(opOrders); err != nil {
		return DailyPaymentSummary{}, err
	}

	parsed, _ := time.Parse(BusinessDayLayout, day)
	from := parsed
	to := parsed.AddDate(0, 0, 1).Add(-time.Nanosecond)
	byStatus := make(map[string]StatusAmount)
	for _, o := range fakeOrders(from, to) {
		bucket, ok := paymentStatusBucket(o.Status)
		if !ok {
			continue
		}
		s := byStatus[bucket]
		s.Count++
		s.AmountMinorUnits += o.AmountMinorUnits
		byStatus[bucket] = s
	}
	return DailyPaymentSummary{
		Snapshot:      f.snapshot(),
		Day:           day,
		Currency:      fakeCurrency,
		ByStatus:      byStatus,
		FeeMinorUnits: nil,
		NetMinorUnits: nil,
	}, nil
}
