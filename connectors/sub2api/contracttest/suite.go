// Package contracttest 提供 Sub2API 只读契约的合规测试套件。
//
// 任何 ReadClient 实现——Fake 或 XM-0017 的真实实现——都必须通过 RunSuite
// 才算合规。契约测试是一等交付物：它把「只读」「带新鲜度」「金额不用浮点」
// 这些口头约定变成可执行的断言。
//
// 覆盖规格 §19.4 Connector 测试矩阵中不依赖真实网络的部分。
package contracttest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/sub2api"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// Factory 构造一个待测客户端。opts 允许套件注入故障场景；
// 真实实现可以忽略与网络无关的选项，但**必须**支持 FailWith 与 Latency，
// 否则故障路径无法被验证。
type Factory func(opts sub2api.FakeOptions) sub2api.ReadClient

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
	t.Run("部分数据被标记", func(t *testing.T) { testPartialIsFlagged(t, newClient) })
	t.Run("能力可少于清单", func(t *testing.T) { testCapabilitiesMayBeSubset(t, newClient) })
}

func testCapabilitiesAreReadOnly(t *testing.T, newClient Factory) {
	caps, err := newClient(sub2api.FakeOptions{}).Capabilities(context.Background())
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
		// 「只读」在这里成为可验证属性而非口头承诺
		if parsed.IsWrite() {
			t.Fatalf("只读契约里出现了写能力 %q（ADR-018 闸 4）", c)
		}
		if !strings.HasPrefix(string(c), sub2api.ConnectorKey+".") {
			t.Fatalf("能力 %q 应以 %q 为前缀", c, sub2api.ConnectorKey)
		}
	}
}

func testEveryResultCarriesSnapshot(t *testing.T, newClient Factory) {
	// 规格 §9.1：禁止裸数字冒充实时完整数据
	c := newClient(sub2api.FakeOptions{})
	ctx := context.Background()

	stats, err := c.UserStats(ctx)
	if err != nil {
		t.Fatalf("UserStats: %v", err)
	}
	assertSnapshot(t, "UserStats", stats.Snapshot)

	orders, err := c.DailyOrders(ctx, "2026-08-27")
	if err != nil {
		t.Fatalf("DailyOrders: %v", err)
	}
	assertSnapshot(t, "DailyOrders", orders.Snapshot)

	balances, err := c.ChannelBalances(ctx)
	if err != nil {
		t.Fatalf("ChannelBalances: %v", err)
	}
	if len(balances) == 0 {
		t.Fatal("渠道余额不应为空")
	}
	for i, b := range balances {
		assertSnapshot(t, "ChannelBalances["+b.ChannelID+"]", b.Snapshot)
		if b.ChannelID == "" {
			t.Fatalf("渠道 #%d 缺少 ChannelID", i)
		}
	}
}

func assertSnapshot(t *testing.T, what string, s sub2api.Snapshot) {
	t.Helper()
	if s.ObservedAt.IsZero() {
		t.Fatalf("%s 缺少 ObservedAt——没有观测时刻就无法判断新鲜度（规格 §9.1）", what)
	}
	if s.ObservedAt.Location() != time.UTC {
		t.Fatalf("%s 的 ObservedAt 应为 UTC（规格 §5.9），got %v", what, s.ObservedAt.Location())
	}
	if s.Watermark == "" {
		t.Fatalf("%s 缺少 Watermark——无法判断是否读到完整区间", what)
	}
}

func testAmountsAreIntegerMinorUnits(t *testing.T, newClient Factory) {
	// 规格 §5.9：货币金额用整数最小货币单位，禁止 float。
	// 类型系统已保证是 int64；这里验证 Currency 必填——
	// 没有币种的金额是无意义的数字。
	c := newClient(sub2api.FakeOptions{})
	ctx := context.Background()

	stats, err := c.UserStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Currency == "" {
		t.Fatal("UserStats 的金额缺少 Currency")
	}
	orders, err := c.DailyOrders(ctx, "2026-08-27")
	if err != nil {
		t.Fatal(err)
	}
	if orders.Currency == "" {
		t.Fatal("OrderSummary 的金额缺少 Currency")
	}
	balances, err := c.ChannelBalances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range balances {
		if b.Currency == "" {
			t.Fatalf("渠道 %s 的余额缺少 Currency", b.ChannelID)
		}
	}
}

func testUnsupportedVersionIsFlagged(t *testing.T, newClient Factory) {
	// 不支持的版本应返回 Supported=false 而非报错：
	// 是否 Fail Closed 由调用方按场景决定（读取可降级，写入必须停）
	v, err := newClient(sub2api.FakeOptions{UnsupportedVersion: true}).
		Version(context.Background())
	if err != nil {
		t.Fatalf("不支持的版本不应报错，而应标记 Supported=false: %v", err)
	}
	if v.Supported {
		t.Fatal("应标记为不支持")
	}
	if v.Detected == "" || v.Fingerprint == "" {
		t.Fatalf("即便不支持也要给出探测值与指纹: %+v", v)
	}

	ok, err := newClient(sub2api.FakeOptions{}).Version(context.Background())
	if err != nil || !ok.Supported {
		t.Fatalf("受支持的版本应 Supported=true: %+v %v", ok, err)
	}
	if ok.DetectedAt.IsZero() {
		t.Fatal("缺少 DetectedAt")
	}
}

func testHealthFailureIsStructured(t *testing.T, newClient Factory) {
	h, err := newClient(sub2api.FakeOptions{Unhealthy: true}).Health(context.Background())
	if err != nil {
		t.Fatalf("不健康不等于调用失败，应返回结构化结果: %v", err)
	}
	if h.Healthy {
		t.Fatal("应标记为不健康")
	}
	if h.ErrorKind == "" {
		t.Fatal("不健康时必须给出 ErrorKind，调用方据此决策")
	}
	if h.CheckedAt.IsZero() {
		t.Fatal("缺少 CheckedAt")
	}
}

func testErrorsAreClassified(t *testing.T, newClient Factory) {
	// ADR-004 铁律：第三方错误不得原样透传
	for _, kind := range []connector.ErrorKind{
		connector.KindUnavailable, connector.KindAuth,
		connector.KindRateLimited, connector.KindBadResponse,
	} {
		c := newClient(sub2api.FakeOptions{FailWith: kind})
		_, err := c.UserStats(context.Background())
		if err == nil {
			t.Fatalf("注入 %s 后应报错", kind)
		}
		if connector.KindOf(err) != kind {
			t.Fatalf("错误分类 = %q, want %q", connector.KindOf(err), kind)
		}
	}
}

func testContextCancellation(t *testing.T, newClient Factory) {
	c := newClient(sub2api.FakeOptions{Latency: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.UserStats(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("上下文取消后应返回错误")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("上下文取消后应立即返回，实际耗时 %v", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) && connector.KindOf(err) != connector.KindUnavailable {
		t.Fatalf("取消错误应可追溯到 context 或归类为 unavailable: %v", err)
	}
}

func testInvalidDayRejected(t *testing.T, newClient Factory) {
	c := newClient(sub2api.FakeOptions{})
	for _, day := range []string{"", "2026-13-01", "20260827", "2026/08/27", "yesterday"} {
		if _, err := c.DailyOrders(context.Background(), day); err == nil {
			t.Fatalf("非法业务日 %q 应被拒绝", day)
		}
	}
}

func testPartialIsFlagged(t *testing.T, newClient Factory) {
	// 部分数据必须被标记，否则看板会把不完整数据当完整数据展示（规格 §9.1）
	stats, err := newClient(sub2api.FakeOptions{Partial: true}).UserStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !stats.IsPartial {
		t.Fatal("部分数据未被标记")
	}
	full, err := newClient(sub2api.FakeOptions{}).UserStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if full.IsPartial {
		t.Fatal("完整数据不应被标记为部分")
	}
}

func testCapabilitiesMayBeSubset(t *testing.T, newClient Factory) {
	// 旧版本上游可能少支持几项能力；契约允许返回子集，
	// 但不允许返回清单之外的能力
	missing := sub2api.ReadCapabilities[0]
	caps, err := newClient(sub2api.FakeOptions{
		MissingCapabilities: []registry.Capability{missing},
	}).Capabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	known := make(map[registry.Capability]struct{}, len(sub2api.ReadCapabilities))
	for _, c := range sub2api.ReadCapabilities {
		known[c] = struct{}{}
	}
	for _, c := range caps {
		if _, ok := known[c]; !ok {
			t.Fatalf("返回了清单之外的能力 %q", c)
		}
		if c == missing {
			t.Fatalf("被移除的能力 %q 仍然出现", missing)
		}
	}
}
