package newapi

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// TestRawAmountKeepsLiteralText：上游的数值字段在解码这一步就必须躲开 float64。
//
// 这是整条金额纪律的第一块砖：json 解码器只要把 12345.67 塞进 float64，
// 精度就已经丢了，后面再怎么小心也追不回来。
func TestRawAmountKeepsLiteralText(t *testing.T) {
	var payload struct {
		A rawAmount `json:"a"`
		B rawAmount `json:"b"`
		C rawAmount `json:"c"`
		D rawAmount `json:"d"`
	}
	// c 是一个 float64 存不下的数：如果解码路径上有 float64，
	// 它会变成 123456789012345680 之类，字面量比对当场失败。
	raw := `{"a":12345.67,"b":"89.10","c":123456789012345678,"d":null}`
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.A != "12345.67" {
		t.Fatalf("数字字面量 = %q, want 12345.67", payload.A)
	}
	if payload.B != "89.10" {
		t.Fatalf("字符串金额 = %q, want 89.10", payload.B)
	}
	if payload.C != "123456789012345678" {
		t.Fatalf("大整数 = %q——解码路径上混进了 float64", payload.C)
	}
	if !payload.D.empty() {
		t.Fatalf("null 应收成空串, got %q", payload.D)
	}
}

func TestDecimalToMinorUnits(t *testing.T) {
	cases := []struct {
		raw   string
		scale int
		want  int64
	}{
		{"0", 2, 0},
		{"31.50", 2, 3150},
		{"31.5", 2, 3150},
		{"-3.25", 2, -325},
		// 四舍五入而不是截断：截断的误差带方向，会系统性地少算
		{"12.345", 2, 1235},
		{"12.344", 2, 1234},
		{"0.005", 2, 1},
		{"0.004", 2, 0},
		// 上游用 Go 的 encoding/json 序列化 float64 时会写成科学计数法
		{"5e5", 0, 500000},
		{"1e-07", 2, 0},
		{"1.5e3", 2, 150000},
		// JPY 这类零小数位币种
		{"1200", 0, 1200},
	}
	for _, tc := range cases {
		got, err := decimalToMinorUnits(tc.raw, tc.scale)
		if err != nil {
			t.Fatalf("decimalToMinorUnits(%q, %d): %v", tc.raw, tc.scale, err)
		}
		if got != tc.want {
			t.Fatalf("decimalToMinorUnits(%q, %d) = %d, want %d", tc.raw, tc.scale, got, tc.want)
		}
	}

	// 垃圾输入必须报错而不是解出一个「差不多」的数
	for _, bad := range []string{"", "abc", "1.2.3", "1,234.5", "12 34", "1e999", "0x10", "∞"} {
		if _, err := decimalToMinorUnits(bad, 2); !errors.Is(err, errAmountFormat) {
			t.Fatalf("decimalToMinorUnits(%q) 应报格式错误, got %v", bad, err)
		}
	}
}

// TestQuotaToMinorUnitsSumsBeforeDividing 是本包最重要的一条金额断言。
//
// NewAPI 的钱是整数 quota，换算成美元要除 quota_per_unit（默认 500000）。
// 「先 SUM 再除」与「各自除完再加」的差别在这个用例上是可见的：
// 三笔 2500 quota 各自换算是 1 分（0.5 分进位），加起来 3 分；
// 先加成 7500 再换算是 2 分（1.5 分进位）。**2 分才是对的**——
// 7500/500000 = $0.015 = 1.5 分。
//
// 这条差异在真实数据上会随条数放大：几千条明细各自进位一次，
// 误差能累积到几十块钱，而且不会报错。
func TestQuotaToMinorUnitsSumsBeforeDividing(t *testing.T) {
	const quotaPerUnit = 500_000

	summedFirst, err := quotaToMinorUnits(2500*3, quotaPerUnit, 2)
	if err != nil {
		t.Fatal(err)
	}
	if summedFirst != 2 {
		t.Fatalf("先 SUM 再除 = %d 分, want 2（7500/500000 = $0.015）", summedFirst)
	}

	var eachThenSum int64
	for range 3 {
		v, err := quotaToMinorUnits(2500, quotaPerUnit, 2)
		if err != nil {
			t.Fatal(err)
		}
		eachThenSum += v
	}
	if eachThenSum == summedFirst {
		t.Fatal("这个用例失去意义了：两种算法给出了同一个数，" +
			"换一组能区分它们的输入，否则纪律没有守卫")
	}
}

func TestQuotaToMinorUnits(t *testing.T) {
	const qpu = 500_000
	cases := []struct {
		quota int64
		want  int64
	}{
		{0, 0},
		{500_000, 100},      // 1 美元
		{5_000, 1},          // 1 分
		{2_500, 1},          // 0.5 分 → 进位
		{2_499, 0},          // 差一点，不进位
		{-500_000, -100},    // 负额度（透支）对称处理
		{10_005_000, 2_001}, // 契约测试里用户余额那一组
	}
	for _, tc := range cases {
		got, err := quotaToMinorUnits(tc.quota, qpu, 2)
		if err != nil {
			t.Fatalf("quotaToMinorUnits(%d): %v", tc.quota, err)
		}
		if got != tc.want {
			t.Fatalf("quotaToMinorUnits(%d) = %d, want %d", tc.quota, got, tc.want)
		}
	}

	// quota_per_unit <= 0 必须报错。上游的 option 写入路径把 ParseFloat 的
	// error 丢掉了，这个值**真的可能**是 0；拿 0 做除数是崩溃，
	// 拿默认值顶上是编数，两个都不行。
	for _, bad := range []int64{0, -1} {
		if _, err := quotaToMinorUnits(100, bad, 2); !errors.Is(err, errAmountFormat) {
			t.Fatalf("quota_per_unit=%d 应报错, got %v", bad, err)
		}
	}
	// 溢出报错而不是静静地绕回去
	if _, err := quotaToMinorUnits(maxInt64, qpu, 2); !errors.Is(err, errAmountFormat) {
		t.Fatalf("放大溢出应报错, got %v", err)
	}
}

func TestRatePPM(t *testing.T) {
	cases := []struct {
		num, den int64
		want     int64
	}{
		{0, 0, 0},         // 没有请求就没有错误率
		{0, 1000, 0},      // 有请求、没错误
		{3, 1000, 3_000},  // 0.3%
		{1, 1_000_000, 1}, // ppm 的分辨率下限
		{1000, 1000, 1_000_000},
		{1, 3, 333_333}, // 四舍五入
	}
	for _, tc := range cases {
		got, err := ratePPM(tc.num, tc.den)
		if err != nil {
			t.Fatalf("ratePPM(%d, %d): %v", tc.num, tc.den, err)
		}
		if got != tc.want {
			t.Fatalf("ratePPM(%d, %d) = %d, want %d", tc.num, tc.den, got, tc.want)
		}
		if got < 0 || got > 1_000_000 {
			t.Fatalf("ratePPM(%d, %d) = %d 超出 ppm 定义域", tc.num, tc.den, got)
		}
	}

	// 分子大于分母说明口径搞错了（比如分母漏加了错误条数）。
	// 静静返回一个 >1000000 的 ppm 会让契约套件的定义域断言在别处炸，
	// 那时已经查不回这里了。
	if _, err := ratePPM(5, 3); !errors.Is(err, errAmountFormat) {
		t.Fatal("分子大于分母应报错")
	}
	if _, err := ratePPM(-1, 3); !errors.Is(err, errAmountFormat) {
		t.Fatal("负分子应报错")
	}
}

// TestTopupProviderSemantics 钉死充值金额的三种语义。
//
// 上游没有「这笔订单值多少美元」这个字段，只有 Amount 与 Money 两个数，
// **哪个是美元取决于 provider**（普查报告：charge money 三语义按 provider
// 分桶）。抄错一支不会报错，只会让某个支付渠道的收入静静地差一个
// quota_per_unit 倍——那是 50 万倍，但如果那个渠道用得少，
// 看板上只会显示成「某天收入异常」。
func TestTopupProviderSemantics(t *testing.T) {
	const qpu = 500_000
	cases := []struct {
		name     string
		item     topupItem
		wantQ    int64
		wantKind topupKind
	}{
		{
			// 易支付：quota = Amount × QuotaPerUnit，所以 Amount 是美元。
			// Money（实付款项，可能是人民币）**不参与**换算。
			name:     "epay 取 amount",
			item:     topupItem{PaymentProvider: "epay", Amount: "10", Money: "72.50"},
			wantQ:    10 * qpu,
			wantKind: topupRevenue,
		},
		{
			// Stripe：quota = Money × QuotaPerUnit，Money 才是美元。
			name:     "stripe 取 money",
			item:     topupItem{PaymentProvider: "stripe", Amount: "999", Money: "20.5"},
			wantQ:    20.5 * qpu,
			wantKind: topupRevenue,
		},
		{
			// Creem 是特例：Amount 直接就是 quota，**不乘** QuotaPerUnit。
			name:     "creem 的 amount 已经是 quota",
			item:     topupItem{PaymentProvider: "creem", Amount: "3000000"},
			wantQ:    3_000_000,
			wantKind: topupRevenue,
		},
		{
			name:     "waffo 同易支付",
			item:     topupItem{PaymentProvider: "waffo", Amount: "2"},
			wantQ:    2 * qpu,
			wantKind: topupRevenue,
		},
		{
			name:     "waffo_pancake 同易支付",
			item:     topupItem{PaymentProvider: "waffo_pancake", Amount: "2"},
			wantQ:    2 * qpu,
			wantKind: topupRevenue,
		},
		{
			// 内部划转：钱早就在系统里了，计进当日充值等于把同一笔钱数两遍。
			name:     "balance 是内部划转不是收入",
			item:     topupItem{PaymentProvider: "balance", Amount: "5"},
			wantQ:    0,
			wantKind: topupInternal,
		},
		{
			// 认不出来就**不猜**：既不按 Amount 也不按 Money 顶上。
			// 猜错的那一笔是「看起来完全正常」的错数字，最难查。
			name:     "没见过的 provider 不猜",
			item:     topupItem{PaymentProvider: "mystery_pay", Amount: "10", Money: "10"},
			wantQ:    0,
			wantKind: topupUnknown,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			quota, kind, err := topupUSDQuota(tc.item, qpu)
			if err != nil {
				t.Fatal(err)
			}
			if kind != tc.wantKind {
				t.Fatalf("kind = %d, want %d", kind, tc.wantKind)
			}
			if quota != tc.wantQ {
				t.Fatalf("quota = %d, want %d", quota, tc.wantQ)
			}
		})
	}
}

func TestCountModels(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
	}{
		{"", 0},
		{"gpt-4o", 1},
		{"gpt-4o,claude-3", 2},
		// 上游的 GetModels() 只 Trim 首尾逗号、不做 TrimSpace，
		// 所以脏数据是可能的：空段与纯空白段都不该被算成一个模型。
		{",gpt-4o,,claude-3, ,", 2},
		{" , , ", 0},
	}
	for _, tc := range cases {
		if got := countModels(tc.raw); got != tc.want {
			t.Fatalf("countModels(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

// TestDayWindowIsClosedInterval：上游的时间过滤是闭区间
// （created_at >= start AND created_at <= end，log 与 usedata 两处都是）。
//
// end 取次日零点会把那一秒的数据同时算进两天，跨日边界上出现重复计数——
// 一个只在极少数秒里发生、几乎不可能被复现的差异。
func TestDayWindowIsClosedInterval(t *testing.T) {
	day := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	start, end := dayWindow(day, time.UTC)

	wantStart := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC).Unix()
	wantEnd := time.Date(2026, 8, 27, 23, 59, 59, 0, time.UTC).Unix()
	if start != wantStart || end != wantEnd {
		t.Fatalf("dayWindow = [%d, %d], want [%d, %d]", start, end, wantStart, wantEnd)
	}
	if end-start != 86399 {
		t.Fatalf("窗口长度 = %d 秒, want 86399（闭区间的一天）", end-start)
	}

	// 业务日结时区必须真的起作用：同一个日期串在东八区对应的是另外 86400 秒。
	shanghai := time.FixedZone("CST", 8*3600)
	localStart, _ := dayWindow(time.Date(2026, 8, 27, 0, 0, 0, 0, shanghai), shanghai)
	if localStart == start {
		t.Fatal("换了业务日结时区，窗口起点却没变——时区参数没被用上")
	}
	if start-localStart != 8*3600 {
		t.Fatalf("东八区的零点应比 UTC 零点早 8 小时, got %d 秒", start-localStart)
	}
}
