package invoice_test

import (
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/invoice"
	"github.com/xufei5620/xingmang-platform/connectors/invoice/contracttest"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// Fake 必须通过契约套件——套件本身要先被验证有效，
// 否则 XM-0029 用它做合规判据就没有意义。
func TestFakeSatisfiesContract(t *testing.T) {
	contracttest.RunSuite(t, func(opts invoice.FakeOptions) invoice.ReadClient {
		return invoice.NewFake(opts)
	})
}

func fixedNow() time.Time { return time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC) }

func TestToObservationsProducesFreshnessReadyMetrics(t *testing.T) {
	now := fixedNow()
	c := invoice.NewFake(invoice.FakeOptions{Now: func() time.Time { return now }})

	sum, err := c.DailySummary(t.Context(), "2026-08-27")
	if err != nil {
		t.Fatal(err)
	}

	obs := invoice.ToObservations(now, "invoice-prod", "production", sum)
	if len(obs) != 2 {
		t.Fatalf("应产出 2 个指标, got %d", len(obs))
	}

	seen := map[string]bool{}
	for _, o := range obs {
		// 每个观测都必须能过领域校验，否则落库会失败
		if err := o.Validate(); err != nil {
			t.Fatalf("指标 %s 不合法: %v", o.MetricKey, err)
		}
		if o.Source != "invoice-prod" || o.Environment != "production" {
			t.Fatalf("指标 %s 的来源/环境不对: %+v", o.MetricKey, o)
		}
		if o.ObservedAt == nil {
			t.Fatalf("指标 %s 缺少 ObservedAt", o.MetricKey)
		}
		if o.Watermark == "" {
			t.Fatalf("指标 %s 缺少 Watermark", o.MetricKey)
		}
		if o.StalenessThresholdSeconds != invoice.DefaultStalenessThresholdSeconds {
			t.Fatalf("指标 %s 的新鲜度阈值 = %d, want %d",
				o.MetricKey, o.StalenessThresholdSeconds, invoice.DefaultStalenessThresholdSeconds)
		}
		if o.Value["day"] != "2026-08-27" {
			t.Fatalf("指标 %s 缺少业务日: %+v", o.MetricKey, o.Value)
		}
		// 刚采集的数据应判为新鲜
		if f := o.Freshness(now); f.State != ops.StateFresh {
			t.Fatalf("指标 %s 刚采集应为 fresh, got %q", o.MetricKey, f.State)
		}
		seen[o.MetricKey] = true
	}

	for _, key := range []string{invoice.MetricRequestsDaily, invoice.MetricAmountDaily} {
		if !seen[key] {
			t.Fatalf("缺少指标 %s", key)
		}
	}
}

func TestToObservationsCarriesSummaryFields(t *testing.T) {
	now := fixedNow()
	c := invoice.NewFake(invoice.FakeOptions{Now: func() time.Time { return now }})
	sum, err := c.DailySummary(t.Context(), "2026-08-27")
	if err != nil {
		t.Fatal(err)
	}
	obs := invoice.ToObservations(now, "invoice-prod", "production", sum)

	byKey := map[string]map[string]any{}
	for _, o := range obs {
		byKey[o.MetricKey] = o.Value
	}

	// 单量指标必须同时带总数/失败/待处理：看板卡片一次渲染三个数，
	// 拆成三条指标会让它们的新鲜度各自漂移，出现「总数是新的、失败是旧的」
	reqs := byKey[invoice.MetricRequestsDaily]
	for _, k := range []string{"count", "failed_count", "pending_count"} {
		if _, ok := reqs[k]; !ok {
			t.Fatalf("%s 缺少 %s: %+v", invoice.MetricRequestsDaily, k, reqs)
		}
	}
	if reqs["count"] != sum.Count {
		t.Fatalf("count = %v, want %d", reqs["count"], sum.Count)
	}

	// 金额指标必须带币种：没有币种的金额是无意义的数字（规格 §5.9）
	amt := byKey[invoice.MetricAmountDaily]
	if amt["currency"] != invoice.CurrencyCNY {
		t.Fatalf("%s 的币种 = %v, want %s", invoice.MetricAmountDaily, amt["currency"], invoice.CurrencyCNY)
	}
	if amt["amount_minor_units"] != sum.TotalAmountMinor {
		t.Fatalf("amount_minor_units = %v, want %d", amt["amount_minor_units"], sum.TotalAmountMinor)
	}
}

func TestToObservationsMarksPartialAndStale(t *testing.T) {
	now := fixedNow()
	c := invoice.NewFake(invoice.FakeOptions{
		Now: func() time.Time { return now }, Partial: true, ObservedAge: 3 * time.Hour,
	})
	sum, err := c.DailySummary(t.Context(), "2026-08-27")
	if err != nil {
		t.Fatal(err)
	}

	for _, o := range invoice.ToObservations(now, "invoice-prod", "production", sum) {
		if !o.IsPartial {
			t.Fatalf("指标 %s 应被标记为部分数据", o.MetricKey)
		}
		// 3 小时前采集、阈值 1 小时 → 应判为延迟
		if f := o.Freshness(now); f.State != ops.StateStale {
			t.Fatalf("指标 %s 应为 stale, got %q", o.MetricKey, f.State)
		}
	}
}

func TestToObservationsKeepsObservedAtNilWhenUpstreamGivesNone(t *testing.T) {
	// 上游没给观测时刻时保持为空，而不是用 now 冒充——
	// 否则「从未采集」与「刚采集」无法区分（规格 §9.1）
	now := fixedNow()
	obs := invoice.ToObservations(now, "invoice-prod", "production",
		invoice.DailySummary{Day: "2026-08-27", Currency: invoice.CurrencyCNY})

	for _, o := range obs {
		if o.ObservedAt != nil {
			t.Fatalf("指标 %s 的 ObservedAt 应为空: %v", o.MetricKey, *o.ObservedAt)
		}
		if f := o.Freshness(now); f.State != ops.StateUninitialized {
			t.Fatalf("指标 %s 应为 uninitialized, got %q", o.MetricKey, f.State)
		}
	}
}

// 能力清单是契约的对外承诺，逐项钉住：漏一项、多一项、或哪天有人手滑
// 写进一个写能力，都应当在这里当场红（ADR-018 闸 4）。
func TestReadCapabilitiesAreExactlyTheDeclaredFour(t *testing.T) {
	want := []string{
		"invoice.service.version_read",
		"invoice.requests.read",
		"invoice.summary.daily_read",
		"invoice.health.read",
	}
	if len(invoice.ReadCapabilities) != len(want) {
		t.Fatalf("能力清单应为 %d 项, got %d", len(want), len(invoice.ReadCapabilities))
	}
	for i, w := range want {
		if string(invoice.ReadCapabilities[i]) != w {
			t.Fatalf("能力 #%d = %q, want %q", i, invoice.ReadCapabilities[i], w)
		}
	}
}

func TestParseStatusAcceptsExactlyNineValues(t *testing.T) {
	all := invoice.AllStatuses()
	if len(all) != 9 {
		t.Fatalf("状态枚举应为 9 项（CR-0002 勘察结论）, got %d", len(all))
	}
	for _, s := range all {
		got, err := invoice.ParseStatus(string(s))
		if err != nil {
			t.Fatalf("合法状态 %q 被拒: %v", s, err)
		}
		if got != s {
			t.Fatalf("ParseStatus(%q) = %q", s, got)
		}
	}
	for _, bad := range []string{"", "ISSUED", "issued ", "paid", "pending"} {
		if _, err := invoice.ParseStatus(bad); err == nil {
			t.Fatalf("非法状态 %q 应被拒绝", bad)
		}
	}
}

// AllStatuses 返回副本：调用方改了不该影响别人。
func TestAllStatusesReturnsCopy(t *testing.T) {
	first := invoice.AllStatuses()
	first[0] = "tampered"
	if invoice.AllStatuses()[0] != invoice.StatusPendingReview {
		t.Fatal("AllStatuses 返回了可被外部篡改的共享切片")
	}
}

func TestEffectiveLimitClampsInsteadOfFailing(t *testing.T) {
	cases := map[int32]int32{
		0:                          invoice.DefaultPageLimit,
		-1:                         invoice.DefaultPageLimit,
		10:                         10,
		invoice.MaxPageLimit:       invoice.MaxPageLimit,
		invoice.MaxPageLimit + 1:   invoice.MaxPageLimit,
		invoice.MaxPageLimit * 100: invoice.MaxPageLimit,
	}
	for in, want := range cases {
		if got := (invoice.ListQuery{Limit: in}).EffectiveLimit(); got != want {
			t.Fatalf("EffectiveLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

// Fake 的日汇总必须与它自己的明细自洽：口径改了、数据集改了，
// 两边会一起变，这条断言才有意义——它钉的是「汇总由明细算出」这件事本身。
func TestFakeSummaryAgreesWithItsOwnItems(t *testing.T) {
	now := fixedNow()
	c := invoice.NewFake(invoice.FakeOptions{Now: func() time.Time { return now }})
	ctx := t.Context()

	page, err := c.ListRequests(ctx, invoice.ListQuery{Limit: invoice.MaxPageLimit})
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor != "" {
		t.Fatal("单页应能取完 Fake 的固定数据集")
	}
	if len(page.Items) != invoice.FakeRecordCount {
		t.Fatalf("固定数据集应为 %d 条, got %d", invoice.FakeRecordCount, len(page.Items))
	}

	var wantAmount int64
	for _, it := range page.Items {
		wantAmount += it.AmountMinor
	}

	sum, err := c.DailySummary(ctx, "2026-08-27")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Count != int64(len(page.Items)) {
		t.Fatalf("汇总条数 %d 与明细 %d 不符", sum.Count, len(page.Items))
	}
	if sum.TotalAmountMinor != wantAmount {
		t.Fatalf("汇总金额 %d 与明细合计 %d 不符", sum.TotalAmountMinor, wantAmount)
	}
	// 失败与待处理都是全量的子集，且不该互相重叠到超过总数
	if sum.FailedCount+sum.PendingCount > sum.Count {
		t.Fatalf("失败 %d + 待处理 %d 超过总数 %d", sum.FailedCount, sum.PendingCount, sum.Count)
	}
	if sum.FailedCount == 0 || sum.PendingCount == 0 {
		t.Fatalf("Fake 数据集应同时覆盖失败与待处理，便于上层验证卡片: failed=%d pending=%d",
			sum.FailedCount, sum.PendingCount)
	}
}

// 时间范围过滤必须真的生效，而不是被静默忽略。
func TestFakeAppliesTimeRangeFilter(t *testing.T) {
	now := fixedNow()
	c := invoice.NewFake(invoice.FakeOptions{Now: func() time.Time { return now }})
	ctx := t.Context()

	// Fake 的数据每条比前一条早一小时，最新一条在 now-1h。
	// 取 [now-5h, now] 应恰好命中 5 条。
	from := now.Add(-5 * time.Hour)
	page, err := c.ListRequests(ctx, invoice.ListQuery{From: from, To: now, Limit: invoice.MaxPageLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 5 {
		t.Fatalf("[now-5h, now] 应命中 5 条, got %d", len(page.Items))
	}
	for _, it := range page.Items {
		if it.SubmittedAt.Before(from) || it.SubmittedAt.After(now) {
			t.Fatalf("记录 %s 的 SubmittedAt=%v 落在区间外", it.ID, it.SubmittedAt)
		}
	}

	// 完全在数据之前的区间应返回空页——但空页**仍带新鲜度**，
	// 这样看板能区分「这段时间真没有开票」与「上游读不到」
	empty, err := c.ListRequests(ctx, invoice.ListQuery{
		From: now.Add(-100 * time.Hour), To: now.Add(-90 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Items) != 0 {
		t.Fatalf("区间内应无数据, got %d 条", len(empty.Items))
	}
	if empty.ObservedAt.IsZero() || empty.Watermark == "" {
		t.Fatal("空页也必须带新鲜度（规格 §9.1：0 也是一个需要知道观测时刻的数）")
	}
	if empty.NextCursor != "" {
		t.Fatal("空页不应给 NextCursor")
	}
}

// 换了过滤条件却沿用旧游标：必须报错，不能静默从头开始。
func TestFakeRejectsStaleCursorAfterFilterChange(t *testing.T) {
	now := fixedNow()
	c := invoice.NewFake(invoice.FakeOptions{Now: func() time.Time { return now }})
	ctx := t.Context()

	first, err := c.ListRequests(ctx, invoice.ListQuery{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if first.NextCursor == "" {
		t.Fatal("应还有下一页")
	}

	// 同样的游标配上一个不含该记录的过滤条件
	_, err = c.ListRequests(ctx, invoice.ListQuery{
		Limit:  3,
		Cursor: first.NextCursor,
		Status: string(invoice.StatusIssued),
	})
	if err == nil {
		t.Fatal("失效游标应被拒绝——静默从头开始会让调用方把第一页当成第二页")
	}
}
