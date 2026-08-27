package finance_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// 本文件跑在真库上。利润台账有一半不变量在 SQL 里：
// 「两侧全空的行不得存在」CHECK、profit_minor 生成列的 NULL 传播、
// platform_id 的 COALESCE 方向、四桶分类的 EXISTS 判据与恒等式。
// 用内存假货复刻只会测到假货（采集侧的分类行为在 collector_test.go）。
//
// 最要紧的两条只有真库测得出来：
//   - ratio_snapshot 经 NUMERIC 往返是否**逐位不变**（§6.3 + §9 的地基）；
//   - 四桶行数之和 == 独立窗口计数（§5.2 的恒等式）。

// profitClock 固定「现在」，让「今日可覆盖、过去冻结」有确定的判据。
// 用 time.Now 的话这条纪律只能在跨零点时靠人肉验证。
var profitClock = time.Date(2026, 8, 28, 6, 0, 0, 0, time.UTC) // CST 14:00

func profitToday() time.Time {
	return finance.BusinessDayAt(profitClock, finance.DefaultBusinessDayLocation())
}

// newProfitFixture 建一个登记簿账号并返回台账仓储。
func newProfitFixture(t *testing.T) (*finance.ProfitStore, *finance.Store, finance.UpstreamAccount) {
	t.Helper()
	pool := testPool(t)
	registry := finance.NewStore(pool)
	ledger := finance.NewProfitStore(pool, func() time.Time { return profitClock })

	account := mustCreate(t, registry, integrationAccount())
	return ledger, registry, account
}

func ledgerRow(account finance.UpstreamAccount, token string) finance.ProfitRow {
	return finance.ProfitRow{
		UpstreamAccountID: account.ID,
		BusinessDay:       profitToday(),
		BusinessDayTZ:     account.BusinessDayTZ,
		TokenID:           token,
		AccountID:         "own-" + token,
		RevenueMinor:      minor(12_345_600),
		CostMinor:         minor(3_875_819),
		Currency:          "USD",
		RatioSnapshot:     account.RechargeRatio,
		Source:            "finance-collect-test",
	}
}

// TestWriteRowNothingKnownIsRejected 是 §5.1 的第一条分支：
// 两侧都未知 → 整对跳过，一个字都不写。
func TestWriteRowNothingKnownIsRejected(t *testing.T) {
	ledger, _, account := newProfitFixture(t)
	row := ledgerRow(account, "tok-nothing")
	row.RevenueMinor, row.CostMinor = nil, nil

	if _, err := ledger.WriteRow(context.Background(), row); !errors.Is(err, finance.ErrProfitNothingKnown) {
		t.Fatalf("两侧全未知应为 ErrProfitNothingKnown, got %v", err)
	}
	if _, err := ledger.GetRow(context.Background(), account.ID, profitToday(), "tok-nothing"); !errors.Is(err, finance.ErrNotFound) {
		t.Fatalf("不该建行, got %v", err)
	}
}

// TestWriteRowOneSidedDoesNotCreate 是 §5.1 里最容易被「顺手优化」掉的一条：
// 只有一侧已知且台账无此行 → **纯 UPDATE 不建行**。
//
// 反面就是那条要挡的错数字：把未知的那侧当 0 建行，报表上会出现一条
// 「毛利 = 收入」的记录，不报错、不缺字段、看起来完全正常，而它是假的。
func TestWriteRowOneSidedDoesNotCreate(t *testing.T) {
	ledger, _, account := newProfitFixture(t)
	ctx := context.Background()

	costOnly := ledgerRow(account, "tok-cost-only")
	costOnly.RevenueMinor = nil
	if _, err := ledger.WriteRow(ctx, costOnly); !errors.Is(err, finance.ErrProfitOneSidedNoRow) {
		t.Fatalf("只有成本应为 ErrProfitOneSidedNoRow, got %v", err)
	}

	revenueOnly := ledgerRow(account, "tok-revenue-only")
	revenueOnly.CostMinor = nil
	if _, err := ledger.WriteRow(ctx, revenueOnly); !errors.Is(err, finance.ErrProfitOneSidedNoRow) {
		t.Fatalf("只有收入应为 ErrProfitOneSidedNoRow, got %v", err)
	}

	for _, token := range []string{"tok-cost-only", "tok-revenue-only"} {
		if _, err := ledger.GetRow(ctx, account.ID, profitToday(), token); !errors.Is(err, finance.ErrNotFound) {
			t.Fatalf("%s 不该建行, got %v", token, err)
		}
	}
}

// TestWriteRowOneSidedRefreshesExistingRow：行已经存在时，
// 单侧读数照常刷新它，且**不碰另一侧**——一次失败的成本读取不该把今天
// 已经读到的收入连带抹掉。
func TestWriteRowOneSidedRefreshesExistingRow(t *testing.T) {
	ledger, _, account := newProfitFixture(t)
	ctx := context.Background()

	if _, err := ledger.WriteRow(ctx, ledgerRow(account, "tok-a")); err != nil {
		t.Fatalf("建行失败: %v", err)
	}

	costOnly := ledgerRow(account, "tok-a")
	costOnly.RevenueMinor = nil
	costOnly.CostMinor = minor(4_000_000)
	costOnly.RatioSnapshot = money.MustParseRatio("2")
	stored, err := ledger.WriteRow(ctx, costOnly)
	if err != nil {
		t.Fatalf("刷新既有行失败: %v", err)
	}
	if stored.CostMinor == nil || *stored.CostMinor != 4_000_000 {
		t.Fatalf("成本应刷新, got %v", stored.CostMinor)
	}
	if stored.RevenueMinor == nil || *stored.RevenueMinor != 12_345_600 {
		t.Fatalf("收入应保留, got %v", stored.RevenueMinor)
	}
	// 倍率跟着成本走：这一行的 cost 是用新倍率折的（§6.3）
	if got := stored.RatioSnapshot.String(); got != "2" {
		t.Fatalf("ratio_snapshot = %q, want 2", got)
	}

	// 反向：只有收入时不碰 ratio_snapshot —— 这一轮根本没有折算发生
	revenueOnly := ledgerRow(account, "tok-a")
	revenueOnly.CostMinor = nil
	revenueOnly.RevenueMinor = minor(20_000_000)
	revenueOnly.RatioSnapshot = money.MustParseRatio("9")
	stored, err = ledger.WriteRow(ctx, revenueOnly)
	if err != nil {
		t.Fatalf("刷新收入侧失败: %v", err)
	}
	if got := stored.RatioSnapshot.String(); got != "2" {
		t.Fatalf("只有收入时不该改 ratio_snapshot，got %q", got)
	}
	if stored.CostMinor == nil || *stored.CostMinor != 4_000_000 {
		t.Fatalf("成本应保留, got %v", stored.CostMinor)
	}
}

// TestWriteRowFreezesPastAndFutureDays 是 §5.3：今日可覆盖、过去冻结。
//
// 未来日期同样拒绝：一次时钟跳变或一个把 +08:00 写成 -08:00 的配置，
// 会让采集在真正的今天之前先建出一行「明天」，而那一行从此就是历史行、
// 再也刷新不了——它会带着半轮采集的残缺数字永久留在报表里。
func TestWriteRowFreezesPastAndFutureDays(t *testing.T) {
	ledger, _, account := newProfitFixture(t)
	ctx := context.Background()

	today := profitToday()
	for name, businessDay := range map[string]time.Time{
		"昨天":  today.AddDate(0, 0, -1),
		"上个月": today.AddDate(0, -1, 0),
		"明天":  today.AddDate(0, 0, 1),
	} {
		row := ledgerRow(account, "tok-"+name)
		row.BusinessDay = businessDay
		if _, err := ledger.WriteRow(ctx, row); !errors.Is(err, finance.ErrProfitDayFrozen) {
			t.Fatalf("%s（%s）应被冻结纪律拒绝, got %v",
				name, businessDay.Format(finance.ProfitBusinessDayLayout), err)
		}
	}

	// 今天照常可写，且可反复覆盖
	for i := 0; i < 3; i++ {
		if _, err := ledger.WriteRow(ctx, ledgerRow(account, "tok-today")); err != nil {
			t.Fatalf("第 %d 次写今日行失败: %v", i+1, err)
		}
	}
}

// TestWriteRowFrozenDayUsesRowTimezone：判「是不是今天」用的是**这一行自己的**
// 业务日时区，不是进程本地时区。
//
// 固定时钟是 UTC 8/28 06:00 = CST 8/28 14:00 = UTC-08:00 的 8/27 22:00。
// 同一时刻，两个时区下的「今天」是不同的日历日——这正是为什么切日时区
// 必须逐行冻结（宪法 14 条）。
func TestWriteRowFrozenDayUsesRowTimezone(t *testing.T) {
	pool := testPool(t)
	registry := finance.NewStore(pool)
	ledger := finance.NewProfitStore(pool, func() time.Time { return profitClock })

	west := integrationAccount()
	west.BaseURL = "https://west.example.test"
	west.BusinessDayTZ = "-08:00"
	account := mustCreate(t, registry, west)

	row := ledgerRow(account, "tok-west")
	row.BusinessDayTZ = "-08:00"
	row.BusinessDay = mustDay(t, "2026-08-27") // UTC-08:00 下的今天
	if _, err := ledger.WriteRow(context.Background(), row); err != nil {
		t.Fatalf("按本行时区算的今天应可写: %v", err)
	}

	// CST 下的今天（8/28）在 UTC-08:00 下是明天 → 拒
	row.BusinessDay = mustDay(t, "2026-08-28")
	if _, err := ledger.WriteRow(context.Background(), row); !errors.Is(err, finance.ErrProfitDayFrozen) {
		t.Fatalf("按本行时区算的明天应被拒, got %v", err)
	}
}

func mustDay(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := finance.ParseBusinessDay(s)
	if err != nil {
		t.Fatalf("解析业务日 %q 失败: %v", s, err)
	}
	return d
}

// TestRatioSnapshotSurvivesNumericRoundTrip 是本文件最要紧的一条。
//
// ratio_snapshot 是**除数**（§3.4）：它经 NUMERIC 往返后必须逐位不变，
// 否则影子对比（§9 要求分粒度 0 差异）从第一天起就没有地基。
// 特别要挡住 pgtype.Numeric.Float64Value() 那条路——1.15 走一遍 float64
// 会变成 1.1499999999999999，而那个差异不会报错，只会让成本静静地偏一点。
func TestRatioSnapshotSurvivesNumericRoundTrip(t *testing.T) {
	ledger, _, account := newProfitFixture(t)
	ctx := context.Background()

	for _, raw := range []string{
		"1.5", "1", "2.00", "0.85",
		"1.15",        // float64 表示不了：1.1499999999999999
		"0.1",         // float64 表示不了
		"3.14159",     //
		"0.000000001", // maxRatioScale 边界
		"1000000",     // 大整数倍率
	} {
		token := "tok-ratio-" + raw
		row := ledgerRow(account, token)
		row.RatioSnapshot = money.MustParseRatio(raw)

		written, err := ledger.WriteRow(ctx, row)
		if err != nil {
			t.Fatalf("倍率 %s 写入失败: %v", raw, err)
		}
		if got := written.RatioSnapshot.String(); got != raw {
			t.Fatalf("写入即返回的倍率 = %q, want %q", got, raw)
		}

		// 真正的判据是**重新读一次**：上一步的返回值可能来自 RETURNING 的
		// 内存值，只有再查一遍才证明它在库里也是这个数。
		reloaded, err := ledger.GetRow(ctx, account.ID, profitToday(), token)
		if err != nil {
			t.Fatalf("重读失败: %v", err)
		}
		if got := reloaded.RatioSnapshot.String(); got != raw {
			t.Fatalf("从库读回的倍率 = %q, want %q", got, raw)
		}
		// 折算结果一致才是倍率精度真正影响的东西
		before, err := money.Divide(5_813_729, written.RatioSnapshot)
		if err != nil {
			t.Fatalf("折算失败: %v", err)
		}
		after, err := money.Divide(5_813_729, reloaded.RatioSnapshot)
		if err != nil {
			t.Fatalf("折算失败: %v", err)
		}
		if before != after {
			t.Fatalf("倍率 %s 往返后折算结果不同：%d vs %d", raw, before, after)
		}
	}
}

// TestProfitMinorGeneratedColumnPropagatesNull 钉住库里那个生成列：
// 它必须与 Go 侧的 ProfitMinor() 逐条一致，且任一侧 NULL 时它也是 NULL。
//
// 为什么值得单独测：把毛利做成生成列的全部理由就是「三个数不可能漂移」，
// 而那个保证只有在库里真的这么算时才成立。
func TestProfitMinorGeneratedColumnPropagatesNull(t *testing.T) {
	pool := testPool(t)
	registry := finance.NewStore(pool)
	ledger := finance.NewProfitStore(pool, func() time.Time { return profitClock })
	account := mustCreate(t, registry, integrationAccount())
	ctx := context.Background()

	row := ledgerRow(account, "tok-generated")
	if _, err := ledger.WriteRow(ctx, row); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	var dbProfit *int64
	err := pool.QueryRow(ctx,
		`SELECT profit_minor FROM finance.profit_daily
		 WHERE upstream_account_id = $1 AND business_day = $2 AND token_id = $3`,
		account.ID, profitToday(), "tok-generated").Scan(&dbProfit)
	if err != nil {
		t.Fatalf("读生成列失败: %v", err)
	}
	goProfit := row.ProfitMinor()
	if dbProfit == nil || goProfit == nil || *dbProfit != *goProfit {
		t.Fatalf("库里的 profit_minor = %v，Go 算的 = %v，两者必须一致", dbProfit, goProfit)
	}

	// 把成本刷成未知是做不到的（WriteRow 不接受「把已知抹成未知」），
	// 所以直接建一条只有一侧的行来验 NULL 传播：先建行，再用一次
	// 只写收入的 UPDATE 是不够的（它保留旧成本）。用库层直插最直接——
	// 这里测的正是**库的** NULL 传播，不是领域层的。
	if _, err := pool.Exec(ctx,
		`INSERT INTO finance.profit_daily
		   (upstream_account_id, business_day, business_day_tz, token_id, account_id,
		    revenue_minor, currency, ratio_snapshot, source)
		 VALUES ($1, $2, '+08:00', 'tok-half', 'own-half', 500, 'USD', 1.5, 'test')`,
		account.ID, profitToday()); err != nil {
		t.Fatalf("直插半行失败: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT profit_minor FROM finance.profit_daily
		 WHERE upstream_account_id = $1 AND business_day = $2 AND token_id = 'tok-half'`,
		account.ID, profitToday()).Scan(&dbProfit); err != nil {
		t.Fatalf("读生成列失败: %v", err)
	}
	if dbProfit != nil {
		t.Fatalf("成本未知时 profit_minor 必须是 NULL，got %d", *dbProfit)
	}
}

// TestLedgerRejectsEntirelyUnknownRowAtDatabaseLevel：领域层拦得住的东西，
// 库层也要拦得住——任何绕过本包的写入路径（比如将来某个脚本）同样造不出
// 两侧全空的行（§5.1）。
func TestLedgerRejectsEntirelyUnknownRowAtDatabaseLevel(t *testing.T) {
	pool := testPool(t)
	registry := finance.NewStore(pool)
	account := mustCreate(t, registry, integrationAccount())

	_, err := pool.Exec(context.Background(),
		`INSERT INTO finance.profit_daily
		   (upstream_account_id, business_day, business_day_tz, token_id, account_id,
		    currency, ratio_snapshot, source)
		 VALUES ($1, $2, '+08:00', 'tok-empty', 'own-empty', 'USD', 1.5, 'test')`,
		account.ID, profitToday())
	if err == nil {
		t.Fatal("库层必须拒绝两侧全空的行")
	}
}

// TestPlatformIDIsFilledButNeverOverwritten 是 §5.3 的后半条：
// platform_id 空缺可补、已有不动——绑定变更只影响新行，不追溯改写历史归属。
//
// 否则今天调一次绑定，上个月的平台毛利就变了。
func TestPlatformIDIsFilledButNeverOverwritten(t *testing.T) {
	ledger, _, account := newProfitFixture(t)
	ctx := context.Background()

	// 第一轮未配对
	first := ledgerRow(account, "tok-p")
	stored, err := ledger.WriteRow(ctx, first)
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if stored.PlatformID != "" {
		t.Fatalf("未配对时应为空, got %q", stored.PlatformID)
	}

	// 第二轮补上归属：空缺可补
	second := ledgerRow(account, "tok-p")
	second.PlatformID = "sub2api-prod"
	stored, err = ledger.WriteRow(ctx, second)
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if stored.PlatformID != "sub2api-prod" {
		t.Fatalf("空缺应被补上, got %q", stored.PlatformID)
	}

	// 第三轮换一个归属：已有不动
	third := ledgerRow(account, "tok-p")
	third.PlatformID = "newapi-prod"
	stored, err = ledger.WriteRow(ctx, third)
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if stored.PlatformID != "sub2api-prod" {
		t.Fatalf("已有归属不该被改写, got %q", stored.PlatformID)
	}
}

// TestSumByPlatformFourBucketsAndIdentity 是 §5.2 的正题：
// 有效平台 / 指向已移除平台 / 未归属各自成桶，且四桶行数之和 ==
// 独立窗口计数。
//
// 恒等式是这条纪律唯一可自动验证的部分：分桶一旦丢了行，金额少一块
// 而总数看起来毫无异常，只有拿一个**独立**算出来的计数去对才发现得了。
func TestSumByPlatformFourBucketsAndIdentity(t *testing.T) {
	pool := testPool(t)
	registry := finance.NewStore(pool)
	ledger := finance.NewProfitStore(pool, func() time.Time { return profitClock })
	account := mustCreate(t, registry, integrationAccount())
	ctx := context.Background()

	// 一个真实存在的自营平台，用来喂「有效平台」那一桶
	const livePlatform = "live-platform-1"
	if _, err := pool.Exec(ctx,
		`INSERT INTO core.service
		   (id, service_type, instance_id, environment, endpoint, owner, status)
		 VALUES ($1, 'sub2api', $2, $3, 'https://live.example.test', 'platform', 'active')
		 ON CONFLICT (service_type, instance_id) DO NOTHING`,
		uuid.New(), livePlatform, intEnv); err != nil {
		t.Fatalf("登记自营平台失败: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM core.service WHERE instance_id = $1`, livePlatform)
	})

	cases := []struct {
		token    string
		platform string
	}{
		{"tok-live-1", livePlatform},
		{"tok-live-2", livePlatform},
		{"tok-gone", "platform-that-vanished"},
		{"tok-none", ""},
	}
	for _, c := range cases {
		row := ledgerRow(account, c.token)
		row.PlatformID = c.platform
		if _, err := ledger.WriteRow(ctx, row); err != nil {
			t.Fatalf("写入 %s 失败: %v", c.token, err)
		}
	}

	today := profitToday()
	attribution, err := ledger.SumByPlatform(ctx, intEnv, today, today)
	if err != nil {
		t.Fatalf("四桶归集失败: %v", err)
	}
	if attribution.TotalRows != 4 {
		t.Fatalf("独立窗口计数 = %d, want 4", attribution.TotalRows)
	}
	// 恒等式由 SumByPlatform 内部校验，这里再断言一次是为了让「它真的对上了」
	// 成为本用例的显式结论，而不是依赖被测代码自己没报错
	if got := attribution.BucketedRows(); got != attribution.TotalRows {
		t.Fatalf("四桶合计 %d ≠ 独立窗口 %d", got, attribution.TotalRows)
	}

	byKind := map[finance.PlatformBucketKind]finance.PlatformBucket{}
	for _, b := range attribution.Buckets {
		byKind[b.Kind] = b
	}
	if got := byKind[finance.BucketPlatform]; got.RowCount != 2 || got.PlatformID != livePlatform {
		t.Fatalf("有效平台桶 = %+v, want 2 行 / %s", got, livePlatform)
	}
	if got := byKind[finance.BucketRemovedPlatform]; got.RowCount != 1 ||
		got.PlatformID != "platform-that-vanished" {
		t.Fatalf("已移除平台桶 = %+v, want 1 行且保留原 id", got)
	}
	if got := byKind[finance.BucketUnattributed]; got.RowCount != 1 || got.PlatformID != "" {
		t.Fatalf("未归属桶 = %+v, want 1 行 / 空 id", got)
	}

	// 金额合计与覆盖行数一起给，合计才可解释
	live := byKind[finance.BucketPlatform]
	if live.RevenueKnownRows != 2 || live.CostKnownRows != 2 {
		t.Fatalf("覆盖行数 = %d/%d, want 2/2", live.RevenueKnownRows, live.CostKnownRows)
	}
	if profit := live.ProfitMinorSum(); profit == nil || *profit != 2*(12_345_600-3_875_819) {
		t.Fatalf("有效平台桶毛利 = %v", profit)
	}
}

// TestSumByPlatformReportsUnknownCoverage：金额列可空，SUM 会跳过 NULL。
// 只报和不报覆盖行数的话，「三行里只有一行有成本」与「三行都有成本」
// 会给出同一种呈现（宪法 12 条）。
func TestSumByPlatformReportsUnknownCoverage(t *testing.T) {
	pool := testPool(t)
	registry := finance.NewStore(pool)
	ledger := finance.NewProfitStore(pool, func() time.Time { return profitClock })
	account := mustCreate(t, registry, integrationAccount())
	ctx := context.Background()

	if _, err := ledger.WriteRow(ctx, ledgerRow(account, "tok-full")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	// 直插一条只有收入的行（库层允许，领域层的「不建行」只管采集路径）
	if _, err := pool.Exec(ctx,
		`INSERT INTO finance.profit_daily
		   (upstream_account_id, business_day, business_day_tz, token_id, account_id,
		    revenue_minor, currency, ratio_snapshot, source)
		 VALUES ($1, $2, '+08:00', 'tok-half', 'own-half', 1000, 'USD', 1.5, 'test')`,
		account.ID, profitToday()); err != nil {
		t.Fatalf("直插半行失败: %v", err)
	}

	today := profitToday()
	attribution, err := ledger.SumByPlatform(ctx, intEnv, today, today)
	if err != nil {
		t.Fatalf("四桶归集失败: %v", err)
	}
	if len(attribution.Buckets) != 1 {
		t.Fatalf("应只有未归属一桶, got %d", len(attribution.Buckets))
	}
	bucket := attribution.Buckets[0]
	if bucket.RowCount != 2 || bucket.RevenueKnownRows != 2 || bucket.CostKnownRows != 1 {
		t.Fatalf("覆盖行数 = %d 行 / rev %d / cost %d, want 2/2/1",
			bucket.RowCount, bucket.RevenueKnownRows, bucket.CostKnownRows)
	}
	if got := bucket.ProfitMinorSum(); got != nil {
		t.Fatalf("成本缺一行时毛利合计必须给不出, got %d", *got)
	}
}

// TestListRowsReportsTruncation：被悄悄截断的区间会被读成
// 「这几天真的没有数据」（宪法 12 条）。
func TestListRowsReportsTruncation(t *testing.T) {
	ledger, _, account := newProfitFixture(t)
	ctx := context.Background()
	for _, token := range []string{"tok-1", "tok-2", "tok-3"} {
		if _, err := ledger.WriteRow(ctx, ledgerRow(account, token)); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}

	today := profitToday()
	rows, truncated, err := ledger.ListRows(ctx, finance.ProfitQuery{
		Environment: intEnv, From: today, To: today, Limit: 2,
	})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(rows) != 2 || !truncated {
		t.Fatalf("应返回 2 行且报告截断, got %d 行 truncated=%v", len(rows), truncated)
	}

	rows, truncated, err = ledger.ListRows(ctx, finance.ProfitQuery{
		Environment: intEnv, From: today, To: today, Limit: 10,
	})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(rows) != 3 || truncated {
		t.Fatalf("应返回 3 行且不截断, got %d 行 truncated=%v", len(rows), truncated)
	}
}

// TestListRowsFiltersByEnvironmentAndPlatform：环境是身份边界（宪法 15 条），
// 台账没有 environment 列，它经 JOIN 登记簿判定——这条断言钉住那个 JOIN。
func TestListRowsFiltersByEnvironmentAndPlatform(t *testing.T) {
	ledger, _, account := newProfitFixture(t)
	ctx := context.Background()

	withPlatform := ledgerRow(account, "tok-plat")
	withPlatform.PlatformID = "some-platform"
	if _, err := ledger.WriteRow(ctx, withPlatform); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if _, err := ledger.WriteRow(ctx, ledgerRow(account, "tok-noplat")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	today := profitToday()
	rows, _, err := ledger.ListRows(ctx, finance.ProfitQuery{
		Environment: intEnv, From: today, To: today, PlatformID: "some-platform",
	})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(rows) != 1 || rows[0].TokenID != "tok-plat" {
		t.Fatalf("平台过滤失效, got %d 行", len(rows))
	}

	// 另一个环境读不到本环境的行
	rows, _, err = ledger.ListRows(ctx, finance.ProfitQuery{
		Environment: "staging", From: today, To: today,
	})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("跨环境不该读到行, got %d", len(rows))
	}
}
