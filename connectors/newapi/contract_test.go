package newapi_test

import (
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/newapi"
	"github.com/xufei5620/xingmang-platform/connectors/newapi/contracttest"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

const testDay = "2026-08-27"

// Fake 必须通过契约套件——套件本身要先被验证有效，
// 否则 XM-0038 用它做合规判据就没有意义。
func TestFakeSatisfiesContract(t *testing.T) {
	contracttest.RunSuite(t, func(opts newapi.FakeOptions) newapi.ReadClient {
		return newapi.NewFake(opts)
	})
}

// TestReadCapabilitiesAreAllReadOnly 直接盯住声明的清单本身，
// 而不只是盯 Capabilities() 的返回值：实现可以返回子集，清单却是源头。
// 有人往清单里加一个 `newapi.channels.manage` 时，这条会先于任何实现变红。
func TestReadCapabilitiesAreAllReadOnly(t *testing.T) {
	if len(newapi.ReadCapabilities) == 0 {
		t.Fatal("能力清单不应为空")
	}
	for _, c := range newapi.ReadCapabilities {
		parsed, err := registry.ParseCapability(string(c))
		if err != nil {
			t.Fatalf("能力 %q 不符合命名规范: %v", c, err)
		}
		if parsed.IsWrite() {
			t.Fatalf("只读契约的能力清单里出现了写能力 %q（ADR-018 闸 4）", c)
		}
	}
}

func fakeReads(t *testing.T, now time.Time, opts newapi.FakeOptions) (
	newapi.UserStats, newapi.OrderSummary, []newapi.ChannelStatus, []newapi.ModelUsage,
) {
	t.Helper()
	if opts.Now == nil {
		opts.Now = func() time.Time { return now }
	}
	c := newapi.NewFake(opts)
	ctx := t.Context()

	stats, err := c.UserStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	orders, err := c.DailyOrders(ctx, testDay)
	if err != nil {
		t.Fatal(err)
	}
	channels, err := c.Channels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	usages, err := c.ModelUsages(ctx, testDay)
	if err != nil {
		t.Fatal(err)
	}
	return stats, orders, channels, usages
}

func TestToObservationsProducesFreshnessReadyMetrics(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	stats, orders, channels, usages := fakeReads(t, now, newapi.FakeOptions{})

	obs := newapi.ToObservations(now, "newapi-prod", "production", stats, orders, channels, usages)
	if len(obs) != 5 {
		t.Fatalf("应产出 5 个指标, got %d", len(obs))
	}

	seen := map[string]bool{}
	for _, o := range obs {
		// 每个观测都必须能过领域校验，否则落库会失败
		if err := o.Validate(); err != nil {
			t.Fatalf("指标 %s 不合法: %v", o.MetricKey, err)
		}
		if o.Source != "newapi-prod" || o.Environment != "production" {
			t.Fatalf("指标 %s 的来源/环境不对: %+v", o.MetricKey, o)
		}
		if o.ObservedAt == nil {
			t.Fatalf("指标 %s 缺少 ObservedAt", o.MetricKey)
		}
		if o.Watermark == "" {
			t.Fatalf("指标 %s 缺少 Watermark", o.MetricKey)
		}
		if o.StalenessThresholdSeconds != newapi.DefaultStalenessThresholdSeconds {
			t.Fatalf("指标 %s 的新鲜度阈值 = %d, want %d",
				o.MetricKey, o.StalenessThresholdSeconds, newapi.DefaultStalenessThresholdSeconds)
		}
		// 刚采集的数据应判为新鲜
		if f := o.Freshness(now); f.State != ops.StateFresh {
			t.Fatalf("指标 %s 刚采集应为 fresh, got %q", o.MetricKey, f.State)
		}
		// 指标键必须在 ops 白名单里，否则 /metrics/history 会把它判成未注册
		// 而 /metrics 照样返回——同一个指标在两个端点上是两种事实（XM-0031）。
		if !ops.KnownMetricKey(o.MetricKey) {
			t.Fatalf("指标 %s 不在 ops 白名单里", o.MetricKey)
		}
		seen[o.MetricKey] = true
	}

	for _, key := range []string{
		newapi.MetricUsersTotal, newapi.MetricRechargeDaily, newapi.MetricSubscriptionDaily,
		newapi.MetricChannelsStatus, newapi.MetricModelsUsage,
	} {
		if !seen[key] {
			t.Fatalf("缺少指标 %s", key)
		}
	}
}

// TestChannelsObservationAggregatesCounts 锁住渠道聚合的三个计数。
//
// 这是 UI 原型对 NewAPI 页面的核心诉求：一眼看出「几个渠道、几个开着、
// 几个不对劲」。三个数算错的方式各不相同，所以分别断言而不是只数总数。
func TestChannelsObservationAggregatesCounts(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	_, _, channels, _ := fakeReads(t, now, newapi.FakeOptions{})

	obs := newapi.ToObservations(now, "newapi-staging", "staging",
		newapi.UserStats{}, newapi.OrderSummary{}, channels, nil)
	value := observationValue(t, obs, newapi.MetricChannelsStatus)

	if got := value["channel_count"]; got != 6 {
		t.Fatalf("channel_count = %v, want 6", got)
	}
	// Fake 的 6 条里 ch-3 是停用的
	if got := value["enabled_channel_count"]; got != 5 {
		t.Fatalf("enabled_channel_count = %v, want 5", got)
	}
	// 只有 ch-4（187500 ppm = 18.75%）越过阈值。ch-3 停用，即便错误率高也不算——
	// 一个被人为关掉的渠道在看板上永久亮红，只会让人学会无视那个数字。
	if got := value["unhealthy_channel_count"]; got != 1 {
		t.Fatalf("unhealthy_channel_count = %v, want 1", got)
	}
	if got := value["unhealthy_threshold_ppm"]; got != newapi.ErrorRateUnhealthyPPM {
		t.Fatalf("unhealthy_threshold_ppm = %v, want %d", got, newapi.ErrorRateUnhealthyPPM)
	}
}

// TestDisabledChannelIsNeverUnhealthy 单独钉住「停用不算异常」这条判断。
//
// 用构造数据而不是 Fake：Fake 里那条停用渠道的错误率是 0，
// 就算判据写错了也测不出来。这里给一个**停用且错误率爆表**的渠道。
func TestDisabledChannelIsNeverUnhealthy(t *testing.T) {
	disabled := newapi.ChannelStatus{
		ChannelID: "ch-off", Enabled: false, ErrorRatePPM: 1_000_000,
	}
	if disabled.Unhealthy() {
		t.Fatal("停用的渠道不该算异常——它没在服务，谈不上出错")
	}
	enabled := disabled
	enabled.Enabled = true
	if !enabled.Unhealthy() {
		t.Fatal("启用且错误率 100% 的渠道必须算异常")
	}
	// 边界取等号：恰好等于阈值算异常
	atThreshold := newapi.ChannelStatus{
		ChannelID: "ch-edge", Enabled: true, ErrorRatePPM: newapi.ErrorRateUnhealthyPPM,
	}
	if !atThreshold.Unhealthy() {
		t.Fatalf("错误率恰好等于阈值 %d ppm 应算异常", newapi.ErrorRateUnhealthyPPM)
	}
	belowThreshold := atThreshold
	belowThreshold.ErrorRatePPM--
	if belowThreshold.Unhealthy() {
		t.Fatal("低于阈值一个 ppm 不该算异常")
	}
}

// TestChannelBalanceNilOmitsKey 是 nil 语义在落库形状上的断言。
//
// 契约套件里已有一条同源断言（对任意实现都跑）；这里再钉一次是因为
// 「写成 0」这个退化太容易发生：任何一次「顺手给它个默认值」的改动都会
// 让看板把「没配余额」显示成「余额已耗尽」，而两者的处置完全相反。
func TestChannelBalanceNilOmitsKey(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	configured := int64(1_000_00)
	channels := []newapi.ChannelStatus{
		{Snapshot: newapi.Snapshot{ObservedAt: now, Watermark: "wm"},
			ChannelID: "with-balance", Enabled: true,
			BalanceMinorUnits: &configured, Currency: "CNY"},
		{Snapshot: newapi.Snapshot{ObservedAt: now, Watermark: "wm"},
			ChannelID: "no-balance", Enabled: true,
			BalanceMinorUnits: nil, Currency: "CNY"},
		{Snapshot: newapi.Snapshot{ObservedAt: now, Watermark: "wm"},
			ChannelID: "zero-balance", Enabled: true,
			BalanceMinorUnits: new(int64), Currency: "CNY"},
	}

	obs := newapi.ToObservations(now, "newapi-staging", "staging",
		newapi.UserStats{}, newapi.OrderSummary{}, channels, nil)
	rows := channelRows(t, observationValue(t, obs, newapi.MetricChannelsStatus))
	if len(rows) != 3 {
		t.Fatalf("应有 3 条渠道行, got %d", len(rows))
	}

	if _, ok := rows[0]["balance_minor_units"]; !ok {
		t.Fatal("已配置余额的渠道缺 balance_minor_units")
	}
	if got, ok := rows[1]["balance_minor_units"]; ok {
		t.Fatalf("未配置余额的渠道不该有 balance_minor_units, got %v", got)
	}
	// 余额确实是 0 的渠道**必须**有这个键：0 是「已耗尽」，是要立刻处理的事故，
	// 与「未配置」是两回事。这一条与上一条合起来才说明两种语义没有合流。
	if got, ok := rows[2]["balance_minor_units"]; !ok || got != int64(0) {
		t.Fatalf("余额为 0 的渠道应有 balance_minor_units=0, got %v (present=%v)", got, ok)
	}
}

func TestModelsObservationAggregatesRequestsWithoutMixingCurrencies(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	_, _, _, usages := fakeReads(t, now, newapi.FakeOptions{})

	obs := newapi.ToObservations(now, "newapi-staging", "staging",
		newapi.UserStats{}, newapi.OrderSummary{}, nil, usages)
	value := observationValue(t, obs, newapi.MetricModelsUsage)

	if got := value["model_count"]; got != 4 {
		t.Fatalf("model_count = %v, want 4", got)
	}
	// 18420 + 12060 + 5130 + 2740
	if got := value["total_request_count"]; got != int64(38_350) {
		t.Fatalf("total_request_count = %v, want 38350", got)
	}
	// 不给消耗合计：币种可能不一致，把不同币种的最小单位加在一起是纯粹的错数
	if _, ok := value["total_consumed_minor_units"]; ok {
		t.Fatal("不该给出跨模型的消耗合计——币种可能不一致，合计是错数")
	}
}

func TestToObservationsMarksPartialAndOldestObservation(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	stats, orders, channels, usages := fakeReads(t, now, newapi.FakeOptions{
		Partial: true, ObservedAge: 2 * time.Hour,
	})

	obs := newapi.ToObservations(now, "newapi-prod", "production", stats, orders, channels, usages)
	for _, o := range obs {
		if !o.IsPartial {
			t.Fatalf("指标 %s 应被标记为部分数据", o.MetricKey)
		}
		// 2 小时前采集、阈值 30 分钟 → 应判为延迟
		if f := o.Freshness(now); f.State != ops.StateStale {
			t.Fatalf("指标 %s 应为 stale, got %q", o.MetricKey, f.State)
		}
	}
}

// TestAggregateTakesOldestObservation 钉住聚合快照取**最旧**成员这条规则。
//
// 取最新会让一个刚更新的渠道替十个陈旧的渠道背书：看板显示「数据新鲜」，
// 而那一格里九成的数字其实已经过期了。
func TestAggregateTakesOldestObservation(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-1 * time.Minute)
	old := now.Add(-3 * time.Hour)
	channels := []newapi.ChannelStatus{
		{Snapshot: newapi.Snapshot{ObservedAt: fresh, Watermark: "wm-fresh"}, ChannelID: "a"},
		{Snapshot: newapi.Snapshot{ObservedAt: old, Watermark: "wm-old"}, ChannelID: "b"},
	}

	obs := newapi.ToObservations(now, "newapi-staging", "staging",
		newapi.UserStats{}, newapi.OrderSummary{}, channels, nil)
	for _, o := range obs {
		if o.MetricKey != newapi.MetricChannelsStatus {
			continue
		}
		if o.ObservedAt == nil || !o.ObservedAt.Equal(old) {
			t.Fatalf("聚合观测时刻应取最旧的 %v, got %v", old, o.ObservedAt)
		}
		if o.Watermark != "wm-old" {
			t.Fatalf("水位应跟着最旧的那一项, got %q", o.Watermark)
		}
		if f := o.Freshness(now); f.State != ops.StateStale {
			t.Fatalf("最旧成员已 3 小时未更新，聚合应为 stale, got %q", f.State)
		}
		return
	}
	t.Fatalf("没找到 %s", newapi.MetricChannelsStatus)
}

func TestToObservationsKeepsObservedAtNilWhenUpstreamGivesNone(t *testing.T) {
	// 上游没给观测时刻时保持为空，而不是用 now 冒充——
	// 否则「从未采集」与「刚采集」无法区分（规格 §9.1）
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	obs := newapi.ToObservations(now, "newapi-staging", "staging",
		newapi.UserStats{Currency: "CNY"},
		newapi.OrderSummary{Day: testDay, Currency: "CNY"},
		nil, nil)

	if len(obs) != 5 {
		t.Fatalf("即便没有渠道与模型也应产出 5 个指标, got %d", len(obs))
	}
	for _, o := range obs {
		if o.ObservedAt != nil {
			t.Fatalf("指标 %s 的 ObservedAt 应为空: %v", o.MetricKey, *o.ObservedAt)
		}
		if f := o.Freshness(now); f.State != ops.StateUninitialized {
			t.Fatalf("指标 %s 应为 uninitialized, got %q", o.MetricKey, f.State)
		}
	}
}

// --- 测试辅助 ---

func observationValue(t *testing.T, obs []ops.Observation, metricKey string) map[string]any {
	t.Helper()
	for _, o := range obs {
		if o.MetricKey == metricKey {
			return o.Value
		}
	}
	t.Fatalf("没找到指标 %s", metricKey)
	return nil
}

func channelRows(t *testing.T, value map[string]any) []map[string]any {
	t.Helper()
	raw, ok := value["channels"].([]any)
	if !ok {
		t.Fatalf("value.channels 不是数组: %T", value["channels"])
	}
	out := make([]map[string]any, 0, len(raw))
	for i, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("渠道 #%d 不是对象: %T", i, item)
		}
		out = append(out, row)
	}
	return out
}
