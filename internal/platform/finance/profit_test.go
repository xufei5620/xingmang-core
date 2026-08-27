package finance_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

func minor(v int64) *int64 { return &v }

func day(s string) time.Time {
	d, err := finance.ParseBusinessDay(s)
	if err != nil {
		panic(err)
	}
	return d
}

func validRow() finance.ProfitRow {
	return finance.ProfitRow{
		UpstreamAccountID: uuid.New(),
		BusinessDay:       day("2026-08-28"),
		BusinessDayTZ:     finance.DefaultBusinessDayTZ,
		TokenID:           "tok-1",
		AccountID:         "acct-1",
		RevenueMinor:      minor(12_345_600),
		CostMinor:         minor(3_875_819),
		Currency:          "USD",
		RatioSnapshot:     money.MustParseRatio("1.5"),
		Source:            "finance-collect-test",
	}
}

// TestProfitMinorIsUnknownWhenEitherSideIs 钉住 §5.1 最要紧的推论：
// 缺一侧的毛利是**未知**，不是「等于另一侧」。
//
// 这条弄错的症状不是报错，是一张利润凭空等于收入的报表。
func TestProfitMinorIsUnknownWhenEitherSideIs(t *testing.T) {
	both := validRow()
	profit := both.ProfitMinor()
	if profit == nil {
		t.Fatal("两侧已知时毛利应算得出")
	}
	if want := int64(12_345_600 - 3_875_819); *profit != want {
		t.Fatalf("毛利 = %d, want %d", *profit, want)
	}

	for name, mutate := range map[string]func(*finance.ProfitRow){
		"成本未知": func(r *finance.ProfitRow) { r.CostMinor = nil },
		"收入未知": func(r *finance.ProfitRow) { r.RevenueMinor = nil },
	} {
		row := validRow()
		mutate(&row)
		if got := row.ProfitMinor(); got != nil {
			t.Fatalf("%s 时毛利必须是 nil，got %d", name, *got)
		}
	}
}

// TestKnownZeroIsNotUnknown 钉住「已知的 0」与「未知」是两件事（§5.1）。
//
// sub2api 的 `today==null` 是今日零流量 = 已知 0；读取失败才是未知。
// 两者用同一个 0 表示的话，一条真的没有流量的渠道与一条采集挂了的渠道
// 在台账里长得一模一样。
func TestKnownZeroIsNotUnknown(t *testing.T) {
	zero := validRow()
	zero.RevenueMinor = minor(0)
	if err := zero.Validate(); err != nil {
		t.Fatalf("已知的 0 是合法值: %v", err)
	}
	profit := zero.ProfitMinor()
	if profit == nil || *profit != -3_875_819 {
		t.Fatalf("收入已知为 0 时毛利 = 负成本，got %v", profit)
	}

	unknown := validRow()
	unknown.RevenueMinor = nil
	if unknown.ProfitMinor() != nil {
		t.Fatal("收入未知时毛利必须是 nil")
	}
}

// TestValidateRejectsEntirelyUnknownRow：两侧全未知的行不该存在（§5.1）。
func TestValidateRejectsEntirelyUnknownRow(t *testing.T) {
	row := validRow()
	row.RevenueMinor, row.CostMinor = nil, nil
	if err := row.Validate(); !errors.Is(err, finance.ErrProfitNothingKnown) {
		t.Fatalf("两侧全未知应为 ErrProfitNothingKnown, got %v", err)
	}
}

// TestValidateRequiresRatioSnapshotWhenCostKnown 钉住 §6.3：
// 有成本就必须说得出是用哪个倍率折的。
//
// 不冻结倍率的话，「上游涨价」与「倍率被调整」在台账上分不开——
// 那正是 SoloAI 在 0176 之前的处境。
func TestValidateRequiresRatioSnapshotWhenCostKnown(t *testing.T) {
	row := validRow()
	row.RatioSnapshot = money.Ratio{}
	if err := row.Validate(); !errors.Is(err, finance.ErrInconsistent) {
		t.Fatalf("成本已知却无倍率应为 ErrInconsistent, got %v", err)
	}

	// 收入侧单独入账时不需要倍率：那一轮根本没有折算发生
	revenueOnly := validRow()
	revenueOnly.CostMinor = nil
	revenueOnly.RatioSnapshot = money.Ratio{}
	if err := revenueOnly.Validate(); err != nil {
		t.Fatalf("只有收入时不该要求倍率: %v", err)
	}

	nonPositive := validRow()
	nonPositive.RatioSnapshot = money.MustParseRatio("0")
	if err := nonPositive.Validate(); !errors.Is(err, finance.ErrInvalidFormat) {
		t.Fatalf("非正倍率应被拒: %v", err)
	}
}

// TestValidateRejectsObservedAtWithoutValue：未知的金额不得带观测时刻。
//
// 一个「读不到成本、但有成本观测时间」的行会让看板显示一个有时间戳的空值，
// 读起来像是「刚采到，值就是空」。
func TestValidateRejectsObservedAtWithoutValue(t *testing.T) {
	now := time.Now().UTC()
	row := validRow()
	row.CostMinor = nil
	row.CostObservedAt = &now
	if err := row.Validate(); !errors.Is(err, finance.ErrInconsistent) {
		t.Fatalf("成本未知却带观测时刻应被拒, got %v", err)
	}
}

// TestValidateRejectsNonCalendarBusinessDay：带时分秒的「业务日」不是日历日。
//
// 放过去的话，同一天会因为两次采集的秒数不同而落成两行（主键含 business_day）。
func TestValidateRejectsNonCalendarBusinessDay(t *testing.T) {
	row := validRow()
	row.BusinessDay = time.Date(2026, 8, 28, 13, 5, 0, 0, time.UTC)
	if err := row.Validate(); !errors.Is(err, finance.ErrInvalidFormat) {
		t.Fatalf("带时分秒的业务日应被拒, got %v", err)
	}

	// 非 UTC 的零点同样不行：它在库里会被解释成另一个日历日
	shanghai := time.FixedZone("+08:00", 8*3600)
	row = validRow()
	row.BusinessDay = time.Date(2026, 8, 28, 0, 0, 0, 0, shanghai)
	if err := row.Validate(); !errors.Is(err, finance.ErrInvalidFormat) {
		t.Fatalf("非 UTC 位置的业务日应被拒, got %v", err)
	}
}

// TestValidateRejectsMalformedPlatformID：形态错的 platform_id 会永久停在
// 「指向已移除平台」那一桶里，而那一桶本该表示「平台真的下线了」。
func TestValidateRejectsMalformedPlatformID(t *testing.T) {
	for _, bad := range []string{"Has Space", "UPPER", "-leading", "has_underscore"} {
		row := validRow()
		row.PlatformID = bad
		if err := row.Validate(); !errors.Is(err, finance.ErrInvalidFormat) {
			t.Fatalf("platform_id %q 应被拒, got %v", bad, err)
		}
	}
	ok := validRow()
	ok.PlatformID = "sub2api-prod-1"
	if err := ok.Validate(); err != nil {
		t.Fatalf("合法 platform_id 被拒: %v", err)
	}
	// 空串是「未配对」的正常表达，不是非法值
	empty := validRow()
	empty.PlatformID = ""
	if err := empty.Validate(); err != nil {
		t.Fatalf("未配对（空串）应合法: %v", err)
	}
}

// TestValidateRequiresSource：无来源的行等于一个裸数字（宪法 12 条、规格 §9.1）。
func TestValidateRequiresSource(t *testing.T) {
	row := validRow()
	row.Source = "   "
	if err := row.Validate(); !errors.Is(err, finance.ErrMissingField) {
		t.Fatalf("缺 source 应被拒, got %v", err)
	}
}

// TestProfitRowRejectsUnregisteredCurrency：币种最小单位小数位猜错的那 100 倍
// 不会有任何症状（宪法 13 条）。
func TestProfitRowRejectsUnregisteredCurrency(t *testing.T) {
	row := validRow()
	row.Currency = "XYZ"
	if err := row.Validate(); !errors.Is(err, finance.ErrInvalidFormat) {
		t.Fatalf("未登记币种应被拒, got %v", err)
	}
}

// TestBusinessDayAtUsesFixedOffset 钉住 ★口径常量（§4）：
// CST 是固定 +08:00 无夏令时，业务日按它切而不是按 UTC 日历日。
//
// 这条错了的症状是整批数据错开一天，且不报错。
func TestBusinessDayAtUsesFixedOffset(t *testing.T) {
	cst := finance.DefaultBusinessDayLocation()

	// UTC 的 8/27 17:00 已经是 CST 的 8/28 01:00
	at := time.Date(2026, 8, 27, 17, 0, 0, 0, time.UTC)
	if got := finance.BusinessDayAt(at, cst).Format(finance.ProfitBusinessDayLayout); got != "2026-08-28" {
		t.Fatalf("CST 业务日 = %s, want 2026-08-28", got)
	}
	// UTC 的 8/27 15:59 还是 CST 的 8/27 23:59
	at = time.Date(2026, 8, 27, 15, 59, 0, 0, time.UTC)
	if got := finance.BusinessDayAt(at, cst).Format(finance.ProfitBusinessDayLayout); got != "2026-08-27" {
		t.Fatalf("CST 业务日 = %s, want 2026-08-27", got)
	}
	// 同一时刻按 UTC 切是 8/27——两套口径确实不同，所以「显式声明」是必要的
	if got := finance.BusinessDayAt(at, time.UTC).Format(finance.ProfitBusinessDayLayout); got != "2026-08-27" {
		t.Fatalf("UTC 业务日 = %s, want 2026-08-27", got)
	}

	// 结果必须是归一到 UTC 零点的日历日，否则 Validate 会拒
	row := validRow()
	row.BusinessDay = finance.BusinessDayAt(time.Now(), cst)
	if err := row.Validate(); err != nil {
		t.Fatalf("BusinessDayAt 的结果必须能直接入库: %v", err)
	}
}

// TestParseBusinessDayIsStrict：宽容的解析会让同一天落成两行（主键含 business_day）。
func TestParseBusinessDayIsStrict(t *testing.T) {
	if _, err := finance.ParseBusinessDay("2026-08-28"); err != nil {
		t.Fatalf("合法业务日被拒: %v", err)
	}
	for _, bad := range []string{"2026-8-1", "26-08-28", "2026/08/28", "", "2026-08-28T00:00:00Z"} {
		if _, err := finance.ParseBusinessDay(bad); err == nil {
			t.Fatalf("业务日 %q 应被拒", bad)
		}
	}
}

// TestPlatformBucketProfitNeedsFullCoverage 钉住 §5.2 的诚实边界：
// 少一行成本的和减去完整的收入和，是一个偏高且无从察觉的毛利。
func TestPlatformBucketProfitNeedsFullCoverage(t *testing.T) {
	full := finance.PlatformBucket{
		RowCount: 3, RevenueKnownRows: 3, CostKnownRows: 3,
		RevenueMinorSum: 900, CostMinorSum: 300, Currency: "USD",
	}
	if profit := full.ProfitMinorSum(); profit == nil || *profit != 600 {
		t.Fatalf("覆盖完整时毛利 = %v, want 600", profit)
	}

	partial := full
	partial.CostKnownRows = 2
	if got := partial.ProfitMinorSum(); got != nil {
		t.Fatalf("成本缺一行时毛利必须给不出，got %d", *got)
	}

	mixed := full
	mixed.MixedCurrency = true
	if got := mixed.ProfitMinorSum(); got != nil {
		t.Fatalf("币种混杂时毛利必须给不出，got %d", *got)
	}
}

// TestParsePlatformBucketKindRejectsUnknown：归默认桶会让「SQL 里新加了一类」
// 表现为「某一桶数字变大了」，而恒等式照样成立——一种查不出来的错。
func TestParsePlatformBucketKindRejectsUnknown(t *testing.T) {
	for _, ok := range []finance.PlatformBucketKind{
		finance.BucketPlatform, finance.BucketRemovedPlatform, finance.BucketUnattributed,
	} {
		if _, err := finance.ParsePlatformBucketKind(string(ok)); err != nil {
			t.Fatalf("已知分桶 %q 被拒: %v", ok, err)
		}
	}
	if _, err := finance.ParsePlatformBucketKind("retired_platform"); err == nil {
		t.Fatal("未知分桶必须报错，不能归到某个默认桶")
	}
}
