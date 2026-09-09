package sub2api_test

import (
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/sub2api"
	"github.com/xufei5620/xingmang-platform/connectors/sub2api/contracttest"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// Fake 必须通过契约套件——套件本身要先被验证有效，
// 否则 XM-0017 用它做合规判据就没有意义。
func TestFakeSatisfiesContract(t *testing.T) {
	contracttest.RunSuite(t, func(opts sub2api.FakeOptions) sub2api.ReadClient {
		return sub2api.NewFake(opts)
	})
}

func TestToObservationsProducesFreshnessReadyMetrics(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	c := sub2api.NewFake(sub2api.FakeOptions{Now: func() time.Time { return now }})
	ctx := t.Context()

	stats, err := c.UserStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	orders, err := c.DailyOrders(ctx, "2026-08-27")
	if err != nil {
		t.Fatal(err)
	}
	balances, err := c.ChannelBalances(ctx)
	if err != nil {
		t.Fatal(err)
	}

	obs := sub2api.ToObservations(now, "sub2api-prod", "production", stats, orders, balances)
	if len(obs) != 5 {
		t.Fatalf("应产出 5 个指标, got %d", len(obs))
	}

	seen := map[string]bool{}
	for _, o := range obs {
		// 每个观测都必须能过领域校验，否则落库会失败
		if err := o.Validate(); err != nil {
			t.Fatalf("指标 %s 不合法: %v", o.MetricKey, err)
		}
		if o.Source != "sub2api-prod" || o.Environment != "production" {
			t.Fatalf("指标 %s 的来源/环境不对: %+v", o.MetricKey, o)
		}
		if o.ObservedAt == nil {
			t.Fatalf("指标 %s 缺少 ObservedAt", o.MetricKey)
		}
		if o.Watermark == "" {
			t.Fatalf("指标 %s 缺少 Watermark", o.MetricKey)
		}
		if o.StalenessThresholdSeconds <= 0 {
			t.Fatalf("指标 %s 缺少新鲜度阈值", o.MetricKey)
		}
		// 刚采集的数据应判为新鲜
		if f := o.Freshness(now); f.State != ops.StateFresh {
			t.Fatalf("指标 %s 刚采集应为 fresh, got %q", o.MetricKey, f.State)
		}
		seen[o.MetricKey] = true
	}

	for _, key := range []string{
		sub2api.MetricUsersTotal, sub2api.MetricUsersBalance,
		sub2api.MetricRevenueDaily, sub2api.MetricCostDaily,
		sub2api.MetricChannelBalance,
	} {
		if !seen[key] {
			t.Fatalf("缺少指标 %s", key)
		}
	}
}

func TestToObservationsMarksPartialAndOldestObservation(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	c := sub2api.NewFake(sub2api.FakeOptions{
		Now: func() time.Time { return now }, Partial: true, ObservedAge: 2 * time.Hour,
	})
	ctx := t.Context()
	stats, _ := c.UserStats(ctx)
	orders, _ := c.DailyOrders(ctx, "2026-08-27")
	balances, _ := c.ChannelBalances(ctx)

	obs := sub2api.ToObservations(now, "sub2api-prod", "production", stats, orders, balances)
	for _, o := range obs {
		if !o.IsPartial {
			t.Fatalf("指标 %s 应被标记为部分数据", o.MetricKey)
		}
		// 2 小时前采集、默认阈值 30 分钟 → 应判为延迟
		if f := o.Freshness(now); f.State != ops.StateStale {
			t.Fatalf("指标 %s 应为 stale, got %q", o.MetricKey, f.State)
		}
	}
}

func TestToObservationsKeepsObservedAtNilWhenUpstreamGivesNone(t *testing.T) {
	// 上游没给观测时刻时保持为空，而不是用 now 冒充——
	// 否则「从未采集」与「刚采集」无法区分（规格 §9.1）
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	obs := sub2api.ToObservations(now, "sub2api-prod", "production",
		sub2api.UserStats{Currency: "CNY"},
		sub2api.OrderSummary{Day: "2026-08-27", Currency: "CNY"},
		nil)

	for _, o := range obs {
		if o.ObservedAt != nil {
			t.Fatalf("指标 %s 的 ObservedAt 应为空: %v", o.MetricKey, *o.ObservedAt)
		}
		if f := o.Freshness(now); f.State != ops.StateUninitialized {
			t.Fatalf("指标 %s 应为 uninitialized, got %q", o.MetricKey, f.State)
		}
	}
}
