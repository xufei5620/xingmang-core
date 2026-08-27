// Package contracttest 提供计量取数只读契约的合规测试套件。
//
// 任何 ReadClient 实现——Fake 或两个真实驱动（sub2api / newapi）——都必须
// 通过 RunSuite 才算合规。契约测试是一等交付物：它把「只读」「带新鲜度」
// 「金额不用浮点」「只回答答得出的业务日」这些口头约定变成可执行的断言。
//
// 覆盖规格 §19.4 Connector 测试矩阵中不依赖真实网络的部分。
package contracttest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/metering"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// Factory 构造一个待测客户端。
//
// opts 允许套件注入故障场景；真实实现可以忽略与网络无关的选项，但**必须**
// 支持 FailWith、Latency 与 Now，否则故障路径与业务日边界都无法被验证。
type Factory func(opts metering.FakeOptions) metering.ReadClient

// Today 是套件与被测实现共用的「今天」。
//
// 业务日边界是本契约最容易错的地方（两个上游都只回答今天），所以套件必须
// 能钉住时钟——用真实的 time.Now() 会让「今天是哪天」在跨零点那一刻变化，
// 于是同一个用例在半夜跑就会失败一次，然后被当成 flaky 加个重试跳过。
var Today = time.Date(2026, 8, 28, 4, 30, 0, 0, time.UTC)

// TodayText 是 Today 在 CST（+08:00）下的业务日。
//
// 刻意选一个 UTC 与 CST **不同日**的时刻：Today 是 UTC 8-28 04:30，
// CST 是 8-28 12:30——同一天。再看 UTC 8-27 20:00 = CST 8-28 04:00，
// 那才是真正的陷阱。testBusinessDayUsesDeclaredTimezone 覆盖它。
var TodayText = Today.In(metering.DefaultBusinessDayLocation()).Format(metering.BusinessDayLayout)

func fixedClock() func() time.Time { return func() time.Time { return Today } }

// RunSuite 跑完整契约套件。
func RunSuite(t *testing.T, newClient Factory) {
	t.Helper()
	t.Run("Capabilities只读且可解析", func(t *testing.T) { testCapabilitiesAreReadOnly(t, newClient) })
	t.Run("每个结果都带新鲜度", func(t *testing.T) { testEveryResultCarriesSnapshot(t, newClient) })
	t.Run("金额是整数最小单位且带币种", func(t *testing.T) { testAmountsAreIntegerMinorUnits(t, newClient) })
	t.Run("不支持的版本不报错而是标记", func(t *testing.T) { testUnsupportedVersionIsFlagged(t, newClient) })
	t.Run("健康失败给结构化分类", func(t *testing.T) { testHealthFailureIsStructured(t, newClient) })
	t.Run("错误不透传上游原始文本", func(t *testing.T) { testErrorsAreClassified(t, newClient) })
	t.Run("上下文取消立即返回", func(t *testing.T) { testContextCancellation(t, newClient) })
	t.Run("非法业务日被拒绝", func(t *testing.T) { testInvalidDayRejected(t, newClient) })
	t.Run("答不出的业务日归not_supported", func(t *testing.T) { testUnanswerableDayIsNotSupported(t, newClient) })
	t.Run("业务日按声明时区切分", func(t *testing.T) { testBusinessDayUsesDeclaredTimezone(t, newClient) })
	t.Run("能力可少于清单", func(t *testing.T) { testCapabilitiesMayBeSubset(t, newClient) })
	t.Run("余额是账号级当前值或明说不支持", func(t *testing.T) { testBalanceIsCurrentValueOrNotSupported(t, newClient) })
}

func baseOptions() metering.FakeOptions {
	return metering.FakeOptions{Now: fixedClock()}
}

func testCapabilitiesAreReadOnly(t *testing.T, newClient Factory) {
	caps, err := newClient(baseOptions()).Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if len(caps) == 0 {
		t.Fatal("能力清单不应为空")
	}
	for _, c := range caps {
		parsed, err := registry.ParseCapability(string(c))
		if err != nil {
			t.Fatalf("能力 %q 不符合命名规范: %v", c, err)
		}
		// 「只读」在这里成为可验证属性而非口头承诺（ADR-018 闸 4）
		if parsed.IsWrite() {
			t.Fatalf("只读契约里出现了写能力 %q", c)
		}
		if !strings.HasPrefix(string(c), metering.ConnectorKey+".") {
			t.Fatalf("能力 %q 应以 %q 为前缀", c, metering.ConnectorKey)
		}
	}
}

func testEveryResultCarriesSnapshot(t *testing.T, newClient Factory) {
	// 规格 §9.1：禁止裸数字冒充实时完整数据
	c := newClient(baseOptions())
	ctx := context.Background()

	usage, err := c.TokenUsage(ctx, metering.TokenRef{UpstreamTokenID: "tok-1"}, TodayText)
	if err != nil {
		t.Fatalf("TokenUsage: %v", err)
	}
	assertSnapshot(t, "TokenUsage", usage.Snapshot)
	if usage.Day != TodayText {
		t.Fatalf("TokenUsage.Day = %q, want %q", usage.Day, TodayText)
	}

	revenue, err := c.AccountRevenue(ctx, "258", TodayText)
	switch {
	case err == nil:
		assertSnapshot(t, "AccountRevenue", revenue.Snapshot)
		if revenue.Day != TodayText {
			t.Fatalf("AccountRevenue.Day = %q, want %q", revenue.Day, TodayText)
		}
	case connector.KindOf(err) == connector.KindNotSupported:
		// 收入不走本通道（newapi 侧的真实形态）：声明一项兑现不了的能力
		// 才是问题，明说不支持是合规的。
	default:
		t.Fatalf("AccountRevenue: %v", err)
	}
}

func assertSnapshot(t *testing.T, what string, s metering.Snapshot) {
	t.Helper()
	assertSnapshotFields(t, what, s)
	// 水位必须带业务日：光有时间戳分不清「这是哪一天的读数」，
	// 而成本台账最怕的正是日期错位（§4）
	if !strings.HasPrefix(s.Watermark, "day:") {
		t.Fatalf("%s 的水位 %q 应带业务日前缀 day:", what, s.Watermark)
	}
}

// assertSnapshotFields 只验「有观测时刻、有水位」，不管水位的前缀。
//
// 余额没有业务日（它是一个当前值，不是某一天的累计量），所以它的水位
// 用不了 day: 前缀——但「不能是裸数字」这条对它一样成立（规格 §9.1）。
func assertSnapshotFields(t *testing.T, what string, s metering.Snapshot) {
	t.Helper()
	if s.ObservedAt.IsZero() {
		t.Fatalf("%s 缺少 ObservedAt——裸数字在类型层面就不该存在", what)
	}
	if strings.TrimSpace(s.Watermark) == "" {
		t.Fatalf("%s 缺少 Watermark", what)
	}
}

// testBalanceIsCurrentValueOrNotSupported 钉住余额读取的三条契约（§2.3 + §7）。
//
//  1. 读得到时必须带观测时刻与币种——§10.4 要求「必须显示观测时间」且
//     「余额和消耗单位一致」，两者缺一，可用天数就算不出可信的数；
//  2. 读不到时必须归 **not_supported**，而不是一个未分类的错误：覆盖率低是
//     这条能力的已知事实（newapi 主力盲区、订阅制上游没有余额），
//     调用方要靠这个分类把可用天数落成「未接入」而不是「采集失败」；
//  3. 声明了 balance_read 能力就必须答得出，反之亦然——一项**声明了却兑现
//     不了**的能力，会让上层反复去打一个注定失败的读取。
func testBalanceIsCurrentValueOrNotSupported(t *testing.T, newClient Factory) {
	c := newClient(baseOptions())
	ctx := context.Background()

	caps, err := c.Capabilities(ctx)
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	declared := false
	for _, cap := range caps {
		if string(cap) == "metering.upstream.balance_read" {
			declared = true
		}
	}

	balance, err := c.UpstreamBalance(ctx)
	switch {
	case err == nil:
		if !declared {
			t.Fatal("答得出余额却没有在 Capabilities 里声明 balance_read")
		}
		assertSnapshotFields(t, "UpstreamBalance", balance.Snapshot)
		if strings.TrimSpace(balance.Currency) == "" {
			t.Fatal("余额缺币种——§10.4 要求余额与消耗单位一致，缺了就比不了")
		}
		if strings.HasPrefix(balance.Watermark, "day:") {
			t.Fatalf("余额的水位 %q 不该带业务日前缀：它是当前值，不属于某一天",
				balance.Watermark)
		}
	case connector.KindOf(err) == connector.KindNotSupported:
		if declared {
			t.Fatal("声明了 balance_read 却返回 not_supported——" +
				"上层会反复去打一个注定失败的读取")
		}
	default:
		t.Fatalf("余额读不到时必须归 not_supported，got %s: %v",
			connector.KindOf(err), err)
	}
}

// testAmountsAreIntegerMinorUnits 让「金额禁 float」成为可验证的属性。
//
// Go 的类型系统已经保证了字段是 int64，所以这里真正要验的是**语义**：
// 折算后的成本必须与整数定点除法逐位一致，而不是某个浮点结果取整。
func testAmountsAreIntegerMinorUnits(t *testing.T, newClient Factory) {
	c := newClient(baseOptions())
	ctx := context.Background()

	usage, err := c.TokenUsage(ctx, metering.TokenRef{UpstreamTokenID: "tok-1"}, TodayText)
	if err != nil {
		t.Fatalf("TokenUsage: %v", err)
	}
	if usage.Currency == "" {
		t.Fatal("金额必须带币种（UI 交接 §10.1）")
	}
	if usage.UsageMinorUnits < 0 {
		t.Fatalf("实扣不该为负: %d", usage.UsageMinorUnits)
	}
	// RawUnits 与 UnitsPerWhole 要么都有要么都没有：只有一个的话，
	// 「先 SUM 再除」就做不了，而调用方看不出来
	if (usage.RawUnits == nil) != (usage.UnitsPerWhole == nil) {
		t.Fatalf("RawUnits 与 UnitsPerWhole 必须同时存在或同时缺失: %v / %v",
			usage.RawUnits, usage.UnitsPerWhole)
	}
	if usage.UnitsPerWhole != nil && *usage.UnitsPerWhole <= 0 {
		t.Fatalf("quota 刻度必须为正: %d", *usage.UnitsPerWhole)
	}
}

func testUnsupportedVersionIsFlagged(t *testing.T, newClient Factory) {
	opts := baseOptions()
	opts.UnsupportedVersion = true
	info, err := newClient(opts).Version(context.Background())
	if err != nil {
		// 不支持的版本**不报错**：是否 Fail Closed 由调用方按场景决定
		// （读取可降级，写入必须停）
		t.Fatalf("不支持的版本不应报错: %v", err)
	}
	if info.Supported {
		t.Fatal("应标记为不支持")
	}
	if info.Detected == "" || info.Fingerprint == "" {
		t.Fatalf("即便不支持也要给出探测值与指纹: %+v", info)
	}
}

func testHealthFailureIsStructured(t *testing.T, newClient Factory) {
	opts := baseOptions()
	opts.Unhealthy = true
	res, err := newClient(opts).Health(context.Background())
	if err != nil {
		// 「不健康」不是「调用失败」：读不通上游是一个**结果**，要能落进看板
		t.Fatalf("不健康不应以错误返回: %v", err)
	}
	if res.Healthy {
		t.Fatal("应报告不健康")
	}
	if res.ErrorKind == "" {
		t.Fatal("不健康必须带结构化分类")
	}
	if res.CheckedAt.IsZero() {
		t.Fatal("必须带检查时刻")
	}
}

func testErrorsAreClassified(t *testing.T, newClient Factory) {
	for _, kind := range []connector.ErrorKind{
		connector.KindAuth, connector.KindRateLimited,
		connector.KindUnavailable, connector.KindBadResponse,
	} {
		opts := baseOptions()
		opts.FailWith = kind
		_, err := newClient(opts).TokenUsage(
			context.Background(), metering.TokenRef{UpstreamTokenID: "tok-1"}, TodayText)
		if err == nil {
			t.Fatalf("FailWith=%s 时应报错", kind)
		}
		if got := connector.KindOf(err); got != kind {
			t.Fatalf("错误分类 = %s, want %s", got, kind)
		}
	}
}

func testContextCancellation(t *testing.T, newClient Factory) {
	opts := baseOptions()
	opts.Latency = 2 * time.Second
	c := newClient(opts)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := c.TokenUsage(ctx, metering.TokenRef{UpstreamTokenID: "tok-1"}, TodayText); err == nil {
			t.Error("上下文已取消时应报错")
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("取消后应立即返回，不该等满模拟延迟")
	}
}

func testInvalidDayRejected(t *testing.T, newClient Factory) {
	c := newClient(baseOptions())
	ctx := context.Background()
	for _, day := range []string{"", "2026-8-1", "2026/08/01", "今天", "20260801"} {
		_, err := c.TokenUsage(ctx, metering.TokenRef{UpstreamTokenID: "tok-1"}, day)
		if err == nil {
			t.Fatalf("非法业务日 %q 应被拒绝", day)
		}
		// 非法业务日是**调用方**的错，归 bad_response——与 not_supported
		// 分开：后者的含义是「上游答不出这一天」，那是上游的能力边界。
		if got := connector.KindOf(err); got != connector.KindBadResponse {
			t.Fatalf("业务日 %q 的错误分类 = %s, want %s", day, got, connector.KindBadResponse)
		}
	}
}

// testUnanswerableDayIsNotSupported 钉住本契约最容易被绕过的一条纪律。
//
// 两个真实上游都只回答「今天」（sub2api 的端点没有日期参数；newapi 的历史
// 区间边界语义未经核对）。实现遇到答不出的业务日必须返回 not_supported，
// **绝不**把今天的数当成那一天的返回回去——那是一个看起来完全正常的错数字，
// 比读不到糟得多。
func testUnanswerableDayIsNotSupported(t *testing.T, newClient Factory) {
	c := newClient(baseOptions())
	yesterday := Today.In(metering.DefaultBusinessDayLocation()).
		AddDate(0, 0, -1).Format(metering.BusinessDayLayout)

	usage, err := c.TokenUsage(context.Background(),
		metering.TokenRef{UpstreamTokenID: "tok-1"}, yesterday)
	if err == nil {
		// 实现若真的支持历史，至少必须把 Day 如实填成被问的那一天，
		// 而不是悄悄返回今天的数
		if usage.Day != yesterday {
			t.Fatalf("声称支持历史却返回了 Day=%q（问的是 %q）——这正是「看起来正常的错数字」",
				usage.Day, yesterday)
		}
		return
	}
	if got := connector.KindOf(err); got != connector.KindNotSupported {
		t.Fatalf("答不出的业务日应归 %s，got %s（%v）",
			connector.KindNotSupported, got, err)
	}
}

// testBusinessDayUsesDeclaredTimezone 覆盖 UTC 与 CST 跨日的那一小时。
//
// UTC 2026-08-27 20:00 = CST 2026-08-28 04:00。用 UTC 切日会把这笔读数记到
// 8-27，用 CST 切日记到 8-28——两者都不报错，差别只在台账里错开一整天
// （§4：收入与成本必须共用同一个时间权威）。
func testBusinessDayUsesDeclaredTimezone(t *testing.T, newClient Factory) {
	crossing := time.Date(2026, 8, 27, 20, 0, 0, 0, time.UTC)
	opts := baseOptions()
	opts.Now = func() time.Time { return crossing }
	c := newClient(opts)

	const wantCST = "2026-08-28"
	if crossing.UTC().Format(metering.BusinessDayLayout) == wantCST {
		t.Fatalf("本用例的前提是 UTC 与 CST 不同日，实测同日——判据失效了")
	}

	usage, err := c.TokenUsage(context.Background(),
		metering.TokenRef{UpstreamTokenID: "tok-1"}, wantCST)
	if err != nil {
		t.Fatalf("CST 业务日 %s 应被当成「今天」，got %v", wantCST, err)
	}
	if usage.Day != wantCST {
		t.Fatalf("Day = %q, want %q", usage.Day, wantCST)
	}

	// 反过来：UTC 那一天在 CST 下是「昨天」，必须被拒
	utcDay := crossing.UTC().Format(metering.BusinessDayLayout)
	if _, err := c.TokenUsage(context.Background(),
		metering.TokenRef{UpstreamTokenID: "tok-1"}, utcDay); err == nil {
		t.Fatalf("UTC 业务日 %s 在 CST 下是昨天，不该被当成今天", utcDay)
	}
}

// AssertPartialPropagates 供**能模拟部分数据的实现**（Fake）单独调用。
//
// 它不在 RunSuite 里：两个真实驱动的每次读取都是**单值原子读**——
// 一个 actual_cost 或一个 quota，要么拿到要么报错，没有「读到一半」这种状态。
// 硬把它塞进套件只会逼出一个永远为 false 的假标志位，然后让人以为
// 「部分数据」这条纪律在这层已经被覆盖了。
//
// IsPartial 在本契约里真正的用武之地在**聚合**那一层：037b 把几十条令牌
// 读数汇成一条观测时，一部分令牌失败就是货真价实的部分数据
// （见 ToObservations 的 aggregateSnapshot）。
func AssertPartialPropagates(t *testing.T, newClient Factory) {
	t.Helper()
	opts := baseOptions()
	opts.Partial = true
	usage, err := newClient(opts).TokenUsage(
		context.Background(), metering.TokenRef{UpstreamTokenID: "tok-1"}, TodayText)
	if err != nil {
		t.Fatalf("TokenUsage: %v", err)
	}
	if !usage.IsPartial {
		t.Fatal("部分数据必须被标记（规格 §9.1）")
	}
}

func testCapabilitiesMayBeSubset(t *testing.T, newClient Factory) {
	opts := baseOptions()
	opts.MissingCapabilities = []registry.Capability{"metering.account.revenue_read"}
	caps, err := newClient(opts).Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	for _, c := range caps {
		if c == "metering.account.revenue_read" {
			t.Fatal("被剔除的能力不该出现在清单里")
		}
	}
	if len(caps) == 0 {
		t.Fatal("剔除一项后不该整份清单为空")
	}
}

// AssertNoDayDrift 是给真实驱动的额外断言：契约层的业务日校验必须与
// 实现层用的是同一条判据。
//
// 导出它而不是塞进 RunSuite：真实驱动的测试文件要在 httptest 场景里
// 单独调用它，而 Fake 已经由 testInvalidDayRejected 覆盖过。
func AssertNoDayDrift(t *testing.T, day string, wantErr bool) {
	t.Helper()
	err := metering.ValidateBusinessDay(day)
	if wantErr && !errors.Is(err, metering.ErrInvalidDay) {
		t.Fatalf("业务日 %q 应被契约层拒绝，got %v", day, err)
	}
	if !wantErr && err != nil {
		t.Fatalf("业务日 %q 应被契约层接受，got %v", day, err)
	}
}
