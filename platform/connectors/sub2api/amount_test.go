package sub2api

import (
	"encoding/json"
	"testing"
)

// 金额换算的直接测试。
//
// 这条路径在契约套件里只被间接覆盖（一个总额对不对），但它是整个连接器里
// 最容易出**沉默错误**的地方：单位错了、精度丢了，数字看起来完全正常，
// 没有任何报错，只是永远对不上账（宪法 13 条）。所以单独钉死。

func TestDecimalToMinorUnits(t *testing.T) {
	for _, tc := range []struct {
		name  string
		raw   string
		scale int
		want  int64
	}{
		{"整数", "120", 2, 12000},
		{"两位小数", "12345.67", 2, 1234567},
		{"补零", "12.3", 2, 1230},
		{"负数", "-3.25", 2, -325},
		{"零", "0", 2, 0},
		{"负零", "-0.000", 2, 0},
		{"只有小数部分", ".5", 2, 50},
		{"整数币种", "1234", 0, 1234},

		// float64 序列化产生的脏值：0.1+0.2 在二进制里就是这个样子。
		// 这正是"不要先转 float 再乘 100"的理由——转了就再也回不来了。
		{"浮点脏值", "0.30000000000000004", 2, 30},
		// 多余精度四舍五入而不是截断：截断的误差带方向，会系统性少算
		{"进位", "12.345", 2, 1235},
		{"不进位", "12.344", 2, 1234},
		{"负数进位", "-12.345", 2, -1235},
		{"小于半分", "0.004", 2, 0},
		{"恰好半分", "0.005", 2, 1},
		{"远小于最小单位", "0.0000001", 2, 0},

		// Go 的 encoding/json 会把大的 float64 写成科学计数法，
		// 上游只要用 float64 存金额，我们迟早会收到 1e+06 这种东西
		{"正指数", "1e+06", 2, 100000000},
		{"负指数", "1.5e-2", 2, 2},
		{"负指数不进位", "1.4e-2", 2, 1},

		// 高精度累加用的 8 位小数口径（上游用户余额是 decimal(20,8)）
		{"八位小数", "120.50000000", 8, 12050000000},
		{"八位小数尾数", "0.004", 8, 400000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decimalToMinorUnits(tc.raw, tc.scale)
			if err != nil {
				t.Fatalf("decimalToMinorUnits(%q, %d) 报错: %v", tc.raw, tc.scale, err)
			}
			if got != tc.want {
				t.Fatalf("decimalToMinorUnits(%q, %d) = %d, want %d", tc.raw, tc.scale, got, tc.want)
			}
		})
	}
}

func TestDecimalToMinorUnitsRejectsGarbage(t *testing.T) {
	// 看不懂的金额必须**报错**，不能悄悄当 0：
	// 一个静默的 0 会以"这个渠道没钱了"的形式出现在看板上。
	for _, raw := range []string{
		"", "   ", "abc", "12.3.4", "1,234.00", "12 34", "0x10",
		"12.34USD", "--1", "1e", "1e999", "NaN", "Inf",
		"999999999999999999999999999999", // 溢出 int64
	} {
		if got, err := decimalToMinorUnits(raw, 2); err == nil {
			t.Fatalf("decimalToMinorUnits(%q) = %d, 应报错", raw, got)
		}
	}
}

func TestRescaleMinorUnits(t *testing.T) {
	for _, tc := range []struct {
		name           string
		v              int64
		from, to       int
		want           int64
		wantErrorState bool
	}{
		{name: "同精度", v: 1234, from: 2, to: 2, want: 1234},
		{name: "降精度四舍五入", v: 12050800000, from: 8, to: 2, want: 12051},
		{name: "降精度不进位", v: 12050400000, from: 8, to: 2, want: 12050},
		{name: "负数降精度", v: -325000000, from: 8, to: 2, want: -325},
		{name: "升精度", v: 1234, from: 2, to: 4, want: 123400},
		{name: "升精度溢出", v: maxInt64 / 10, from: 0, to: 18, wantErrorState: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rescaleMinorUnits(tc.v, tc.from, tc.to)
			if tc.wantErrorState {
				if err == nil {
					t.Fatalf("应报错, got %d", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("rescaleMinorUnits(%d, %d, %d) = %d, want %d",
					tc.v, tc.from, tc.to, got, tc.want)
			}
		})
	}
}

// TestRawAmountKeepsLiteralText 锁住这条链路最关键的一步：
// JSON 里的金额字面量必须**原样**进到解析器，中途一步都不能经过 float64。
func TestRawAmountKeepsLiteralText(t *testing.T) {
	var payload struct {
		Number rawAmount `json:"number"`
		Text   rawAmount `json:"text"`
		Null   rawAmount `json:"null"`
		Absent rawAmount `json:"absent"`
	}
	// 这个数字用 float64 转一圈之后会变成 12345.678900000001 之类的东西
	const raw = `{"number":12345.6789000000001,"text":"  42.50 ","null":null}`
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	if string(payload.Number) != "12345.6789000000001" {
		t.Fatalf("数字字面量被改写了: %q", payload.Number)
	}
	if string(payload.Text) != "42.50" {
		t.Fatalf("字符串金额 = %q, want 42.50", payload.Text)
	}
	if !payload.Null.empty() || !payload.Absent.empty() {
		t.Fatal("null 与缺失字段都该是空值——空值与 0 是两件事")
	}
	// 空值按 0 处理，但 count/minorUnits 都不该报错
	if v, err := payload.Null.minorUnits(2); err != nil || v != 0 {
		t.Fatalf("null 的换算 = %d, %v", v, err)
	}
	if v, err := payload.Text.minorUnits(2); err != nil || v != 4250 {
		t.Fatalf("字符串金额换算 = %d, %v", v, err)
	}
}

func TestCurrencyScaleRefusesToGuess(t *testing.T) {
	if scale, err := currencyScale("usd"); err != nil || scale != 2 {
		t.Fatalf("currencyScale(usd) = %d, %v", scale, err)
	}
	if scale, err := currencyScale("JPY"); err != nil || scale != 0 {
		t.Fatalf("currencyScale(JPY) = %d, %v", scale, err)
	}
	// 不认识的币种必须报错：猜 2 位小数错了会静静地把所有金额差 100 倍
	if _, err := currencyScale("XYZ"); err == nil {
		t.Fatal("未登记的币种必须被拒，不能猜小数位")
	}
	if _, err := currencyScale(""); err == nil {
		t.Fatal("空币种必须被拒")
	}
}

func TestVersionSupported(t *testing.T) {
	matrix := []string{"0.1"}
	for _, tc := range []struct {
		detected string
		want     bool
	}{
		{"0.1.133", true},
		{"v0.1.133", true},     // 带不带 v 前缀都认
		{"0.1.152-rc.1", true}, // 预发布后缀不影响主线判断
		{"0.1", true},
		{"0.2.0", false},
		{"1.1.0", false},
		{"unknown", false},
		{"", false},
	} {
		if got := versionSupported(tc.detected, matrix); got != tc.want {
			t.Fatalf("versionSupported(%q) = %v, want %v", tc.detected, got, tc.want)
		}
	}
	// 矩阵写到补丁位时只认那一个补丁版本
	if versionSupported("0.1.134", []string{"0.1.133"}) {
		t.Fatal("矩阵写到补丁位时不该放行别的补丁版本")
	}
}
