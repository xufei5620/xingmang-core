package metering

import (
	"context"
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
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

	// TodayOnly 模拟「上游只回答今天」的真实约束。
	//
	// 默认开启：两个真实驱动**都**只回答今天（sub2api 的端点没有日期参数，
	// newapi 的历史区间边界语义未经核对，见各自的注释）。Fake 若无条件
	// 回答任何业务日，上层就会在假上游上写出一条真环境跑不通的回填逻辑，
	// 而那正是 Fake 存在的意义要避免的。想测「上游支持历史」的分支时
	// 显式关掉它。
	TodayOnly *bool

	// RawQuota 非 nil 时，TokenUsage 按 newapi 的形状返回（整数 credits +
	// quota_per_unit），让调用方能测「先 SUM 再除」；nil 则按 sub2api 的
	// 形状返回（上游直接给金额）。
	RawQuota *int64
	// QuotaPerUnit 是 RawQuota 的刻度；0 则用 ★ 口径常量 500000。
	QuotaPerUnit int64

	// RevenueSupported 为 false 时 AccountRevenue 返回 not_supported，
	// 模拟 newapi 侧「收入走数据库通道，不在本 HTTP 客户端」的真实形态。
	// 零值 false 会让默认 Fake 拒答收入，所以它是**指针**：不设就支持。
	RevenueSupported *bool

	// BalanceSupported 为 false 时 UpstreamBalance 返回 not_supported，
	// 模拟 §7 说的那两种真实盲区：newapi 的 channel.balance 是一潭死水
	// （上游没开 CHANNEL_UPDATE_FREQUENCY），订阅制上游压根没有余额。
	// 同样是**指针**：不设就支持，零值 false 会让默认 Fake 拒答余额。
	BalanceSupported *bool

	// BalanceMinorUnits 覆盖 Fake 的余额起点（整数最小单位 @ UsageScale）；
	// 0 用默认值。
	BalanceMinorUnits int64

	// Now 可注入固定时钟，默认 time.Now。
	Now func() time.Time
	// BusinessDay 是业务日时区；nil 则用 CST 固定 +08:00。
	BusinessDay *time.Location
}

type fakeClient struct {
	opts FakeOptions
}

// NewFake 创建一个满足 ReadClient 契约的假实现。
//
// 它存在的意义有两层：让真实只读凭据到位前的上层开发（XM-0037b 的台账与
// 周期任务）不被阻塞；以及作为 contracttest 套件的第一个被测实现——
// 套件本身要先被验证有效。
//
// **Fake 是默认模式**（同 connectors/sub2api 的 fake→real 演进）：
// 采集装配在没有凭据引用时用它，而不是让 worker 起不来。
func NewFake(opts FakeOptions) ReadClient {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.BusinessDay == nil {
		opts.BusinessDay = DefaultBusinessDayLocation()
	}
	if opts.Version == "" {
		opts.Version = "0.1.183"
	}
	if opts.QuotaPerUnit == 0 {
		opts.QuotaPerUnit = newapiDefaultQuotaPerUnit
	}
	if opts.TodayOnly == nil {
		yes := true
		opts.TodayOnly = &yes
	}
	if opts.RevenueSupported == nil {
		yes := true
		opts.RevenueSupported = &yes
	}
	if opts.BalanceSupported == nil {
		yes := true
		opts.BalanceSupported = &yes
	}
	if opts.BalanceMinorUnits == 0 {
		opts.BalanceMinorUnits = fakeDefaultBalanceMinorUnits
	}
	return &fakeClient{opts: opts}
}

func (f *fakeClient) now() time.Time { return f.opts.Now().UTC() }

func (f *fakeClient) observedAt() time.Time {
	return f.now().Add(-f.opts.ObservedAge)
}

func (f *fakeClient) snapshot(day string) Snapshot {
	observedAt := f.observedAt()
	return Snapshot{
		ObservedAt: observedAt,
		Watermark:  watermark(day, observedAt),
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

// checkDay 复刻真实驱动的业务日约束。
//
// 用**契约层**的 ValidateBusinessDay 而不是自己写一遍 time.Parse：
// Fake 与真实实现对同一个非法输入必须给出同一个分类，否则「换实现」
// 就会换错误处理（同 connectors/sub2api 客户端对 DailyOrders 的注释）。
func (f *fakeClient) checkDay(op, day string) error {
	if err := ValidateBusinessDay(day); err != nil {
		return connector.NewError(connector.KindBadResponse, op, err)
	}
	if *f.opts.TodayOnly {
		today := f.now().In(f.opts.BusinessDay).Format(BusinessDayLayout)
		if day != today {
			return connector.NewError(connector.KindNotSupported, op,
				fmt.Errorf("上游只回答今天（%s）；请求的业务日是 %s", today, day))
		}
	}
	return nil
}

func (f *fakeClient) Version(ctx context.Context) (connector.VersionInfo, error) {
	if err := f.wait(ctx); err != nil {
		return connector.VersionInfo{}, connector.NewError(
			connector.KindUnavailable, "metering.service.version_read", err)
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
		return connector.HealthResult{}, connector.NewError(
			connector.KindUnavailable, "metering.health.read", err)
	}
	if f.opts.Unhealthy {
		return connector.HealthResult{
			Healthy:   false,
			CheckedAt: f.now(),
			LatencyMS: f.opts.Latency.Milliseconds(),
			ErrorKind: connector.KindUnavailable,
			// 安全的简短说明，不含上游原始错误文本
			Detail: "upstream status endpoint returned non-2xx",
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
		return nil, connector.NewError(connector.KindUnavailable, "metering.capabilities", err)
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
		if c == "metering.account.revenue_read" && !*f.opts.RevenueSupported {
			continue
		}
		if c == "metering.upstream.balance_read" && !*f.opts.BalanceSupported {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// TokenUsage 返回一条假的上游实扣读数。
//
// 金额取 §2.4 worked example 的那个值（actual_cost = "5.813729"，
// 即 5813729 微单位）：上层拿 Fake 跑通之后，用同一个数去核对真实链路
// 与设计稿的标准答案，省掉一次「这个数应该是多少」的来回。
func (f *fakeClient) TokenUsage(
	ctx context.Context, token TokenRef, day string,
) (TokenUsage, error) {
	const op = "metering.token.usage_read"
	if err := f.wait(ctx); err != nil {
		return TokenUsage{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	if err := f.checkDay(op, day); err != nil {
		return TokenUsage{}, err
	}
	if err := f.fail(op); err != nil {
		return TokenUsage{}, err
	}

	usage := TokenUsage{
		Snapshot:        f.snapshot(day),
		UpstreamTokenID: token.UpstreamTokenID,
		Day:             day,
		Currency:        DefaultCurrency,
		UsageMinorUnits: 5_813_729,
	}
	if f.opts.RawQuota != nil {
		// newapi 形状：上游给整数 credits，金额由刻度折算而来
		quota := *f.opts.RawQuota
		perUnit := f.opts.QuotaPerUnit
		minor, err := money.DivideByUnits(quota, perUnit, UsageScale)
		if err != nil {
			return TokenUsage{}, connector.NewError(connector.KindBadResponse, op, err)
		}
		usage.UsageMinorUnits = minor
		usage.RawUnits = &quota
		usage.UnitsPerWhole = &perUnit
	}
	return usage, nil
}

func (f *fakeClient) AccountRevenue(
	ctx context.Context, ownAccountID string, day string,
) (AccountRevenue, error) {
	const op = "metering.account.revenue_read"
	if err := f.wait(ctx); err != nil {
		return AccountRevenue{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	if !*f.opts.RevenueSupported {
		return AccountRevenue{}, connector.NewError(connector.KindNotSupported, op,
			fmt.Errorf("本上游的使用计费收入不走 HTTP 通道"))
	}
	if err := f.checkDay(op, day); err != nil {
		return AccountRevenue{}, err
	}
	if err := f.fail(op); err != nil {
		return AccountRevenue{}, err
	}
	return AccountRevenue{
		Snapshot:          f.snapshot(day),
		OwnAccountID:      ownAccountID,
		Day:               day,
		Currency:          DefaultCurrency,
		RevenueMinorUnits: 12_345_600,
	}, nil
}

// fakeDefaultBalanceMinorUnits 是 Fake 的余额起点：$420.00 @ scale-6。
//
// 取一个**能被可用天数算出漂亮数字**的量级：Fake 的日成本是
// 5813729 ÷ 1.5 ≈ 3875819 微单位（§2.4 worked example），
// 420000000 ÷ 3875819 ≈ 108 天——落在「充裕」档，
// 于是默认演示不会一上来就是一片红。要演示告警把它调小即可。
const fakeDefaultBalanceMinorUnits int64 = 420_000_000

// UpstreamBalance 返回一条假的余额读数（§2.3 + §7）。
//
// 它**随时间缓慢下降**而不是一个常数：可用天数的整条链路（游程编码落库、
// 「只在变化时插入新行」、新鲜度取 observed_at）只有在余额真的会变的时候
// 才跑得到那几个分支。一个恒定的 Fake 余额会让「变化检测」永远走不到
// insert 那一支，于是那段代码在演示里从未被执行过。
//
// 下降速度按**每小时**一档：采集默认 5 分钟一轮，每小时变一次意味着
// 一小时内的十二轮里有十一轮走「值没变，只更新 observed_at」那条路——
// 正是真实部署里的比例。
func (f *fakeClient) UpstreamBalance(ctx context.Context) (UpstreamBalance, error) {
	const op = "metering.upstream.balance_read"
	if err := f.wait(ctx); err != nil {
		return UpstreamBalance{}, connector.NewError(connector.KindUnavailable, op, err)
	}
	if !*f.opts.BalanceSupported {
		return UpstreamBalance{}, connector.NewError(connector.KindNotSupported, op,
			fmt.Errorf("本上游答不出余额（§7：newapi 需上游先开 CHANNEL_UPDATE_FREQUENCY；"+
				"订阅制上游没有余额这个概念）"))
	}
	if err := f.fail(op); err != nil {
		return UpstreamBalance{}, err
	}

	observedAt := f.observedAt()
	// 以 UTC 小时数为台阶。用 Unix 小时而不是「距某个起点的时长」：
	// 后者要选一个起点，而任何起点都会让 Fake 的输出依赖它被调用的日期。
	hours := observedAt.Unix() / 3600
	balance := f.opts.BalanceMinorUnits - (hours%240)*fakeBalanceHourlyDrainMinorUnits
	return UpstreamBalance{
		Snapshot: Snapshot{
			ObservedAt: observedAt,
			// 余额没有业务日：它是一个当前值，不是某一天的累计量。
			// 水位因此用 balance 前缀而不是 day:——让两种读数的水位
			// 在日志里一眼分得开。
			Watermark: fmt.Sprintf("balance@%d", observedAt.Unix()),
			IsPartial: f.opts.Partial,
		},
		BalanceMinorUnits: balance,
		Currency:          DefaultCurrency,
	}, nil
}

// fakeBalanceHourlyDrainMinorUnits 是 Fake 余额每小时的下降量（$0.15）。
//
// 240 小时（10 天）一个周期后回到起点：演示环境长期跑着，
// 单调下降迟早会把余额跑成一个巨大的负数，那会让「透支预警」
// 变成一条永远亮着的红灯，反而没人看了。
const fakeBalanceHourlyDrainMinorUnits int64 = 150_000
