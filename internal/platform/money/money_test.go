package money

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParseMinorUnitsHalfUp(t *testing.T) {
	for _, tt := range []struct {
		raw   string
		scale int
		want  int64
	}{
		// 设计稿 §2.4 的 worked example：sub2api actual_cost 的原始字面量
		{"5.813729", MicroScale, 5_813_729},
		// 位数不足按右补零，不是按 float 放大
		{"5.8", MicroScale, 5_800_000},
		{"5", MicroScale, 5_000_000},
		// 超出标度的部分半进（away from zero），不截断
		{"0.0000005", MicroScale, 1},
		{"0.0000004", MicroScale, 0},
		{"-0.0000005", MicroScale, -1},
		{"1.2345675", MicroScale, 1_234_568},
		{"1.2345674", MicroScale, 1_234_567},
		// 整个数值都在最小单位以下
		{"0.00000004", MicroScale, 0},
		// 上游用 float64 序列化时会出现的科学计数法
		{"1e+06", MicroScale, 1_000_000_000_000},
		{"1.5e-3", MicroScale, 1_500},
		// 分标度（展示层）
		{"12.345", 2, 1235},
		{"12.344", 2, 1234},
		// 零标度（计数）
		{"7", 0, 7},
	} {
		got, err := ParseMinorUnits(tt.raw, tt.scale)
		if err != nil {
			t.Fatalf("ParseMinorUnits(%q, %d) 报错: %v", tt.raw, tt.scale, err)
		}
		if got != tt.want {
			t.Fatalf("ParseMinorUnits(%q, %d) = %d, want %d", tt.raw, tt.scale, got, tt.want)
		}
	}
}

// TestParseMinorUnitsNeverUsesFloat 锁住本包存在的理由。
//
// 0.1+0.2 在 float64 里是 0.30000000000000004；一条经过 float 的实现会在
// scale-17 上暴露出来，而经过整数的实现原样保留字面量。用一个 float64
// 表示不了的十进制数做判据，比断言某个具体数值更能说明问题。
func TestParseMinorUnitsNeverUsesFloat(t *testing.T) {
	// 8.7 在 float64 里是 8.699999999999999289457264239899814128875732421875。
	// 先乘 10^6 再取整的浮点实现会得到 8699999，整数实现得到 8700000。
	got, err := ParseMinorUnits("8.7", MicroScale)
	if err != nil {
		t.Fatalf("解析报错: %v", err)
	}
	if got != 8_700_000 {
		t.Fatalf("ParseMinorUnits(\"8.7\", 6) = %d, want 8700000（疑似经过了 float64）", got)
	}
	// 19 位有效数字远超 float64 的 53 位尾数，经过 float 必然失真
	got, err = ParseMinorUnits("9007199254740993.000001", MicroScale)
	if err == nil && got == 0 {
		t.Fatalf("大数解析静默返回 0")
	}
}

func TestParseMinorUnitsRejects(t *testing.T) {
	for _, tt := range []struct {
		raw   string
		scale int
	}{
		{"", MicroScale},
		{"   ", MicroScale},
		{"abc", MicroScale},
		{"1.2.3", MicroScale},
		{"1,234.5", MicroScale},
		{".", MicroScale},
		{"1e999", MicroScale},
		{"1e+31", MicroScale},
		{"99999999999999999999999999", MicroScale},
		{"1.0", -1},
		{"1.0", maxScale + 1},
	} {
		if _, err := ParseMinorUnits(tt.raw, tt.scale); err == nil {
			t.Fatalf("ParseMinorUnits(%q, %d) 应报错", tt.raw, tt.scale)
		}
	}
}

func TestRescaleRoundsHalfUpAndDetectsOverflow(t *testing.T) {
	for _, tt := range []struct {
		v           int64
		from, to    int
		want        int64
		wantErrOnly bool
	}{
		{5_813_729, MicroScale, 2, 581, false}, // §2.4 worked example 折到分
		{3_875_819, MicroScale, 2, 388, false}, // 折算后的成本折到分 = $3.88
		{1_235_000, MicroScale, 2, 124, false},
		{1_234_999, MicroScale, 2, 123, false},
		{-1_235_000, MicroScale, 2, -124, false},
		{1234, 2, MicroScale, 12_340_000, false},
		{7, 0, 0, 7, false},
		// 升标度溢出
		{maxInt64 / 10, 0, 6, 0, true},
	} {
		got, err := Rescale(tt.v, tt.from, tt.to)
		if tt.wantErrOnly {
			if !errors.Is(err, ErrOverflow) {
				t.Fatalf("Rescale(%d, %d, %d) 应溢出报错, got %d / %v", tt.v, tt.from, tt.to, got, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Rescale(%d, %d, %d) 报错: %v", tt.v, tt.from, tt.to, err)
		}
		if got != tt.want {
			t.Fatalf("Rescale(%d, %d, %d) = %d, want %d", tt.v, tt.from, tt.to, got, tt.want)
		}
	}
}

func TestCurrencyScaleRefusesToGuess(t *testing.T) {
	for _, code := range []string{"USD", "usd", " CNY "} {
		s, err := CurrencyScale(code)
		if err != nil || s != 2 {
			t.Fatalf("CurrencyScale(%q) = %d, %v；期望 2, nil", code, s, err)
		}
	}
	if s, err := CurrencyScale("JPY"); err != nil || s != 0 {
		t.Fatalf("CurrencyScale(JPY) = %d, %v；期望 0, nil", s, err)
	}
	// 猜错币种的 100 倍不会有任何症状，所以未登记的币种必须硬报错
	for _, code := range []string{"", "XYZ", "BTC", "usdt"} {
		if _, err := CurrencyScale(code); !errors.Is(err, ErrUnknownCurrency) {
			t.Fatalf("CurrencyScale(%q) 必须报 ErrUnknownCurrency, got %v", code, err)
		}
	}
}

func TestRawAmountPreservesLiteralText(t *testing.T) {
	var payload struct {
		Number RawAmount `json:"number"`
		Text   RawAmount `json:"text"`
		Null   RawAmount `json:"null"`
		Absent RawAmount `json:"absent"`
	}
	// 12345.67 与 5.813729 都是 float64 表示不精确的十进制数
	raw := []byte(`{"number":5.813729,"text":"12345.67","null":null}`)
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if payload.Number != "5.813729" {
		t.Fatalf("数字字面量被改写成 %q", payload.Number)
	}
	if payload.Text != "12345.67" {
		t.Fatalf("字符串金额被改写成 %q", payload.Text)
	}
	// null 与字段缺失都算「上游什么都没说」，与「上游说 0」必须可区分（§5.1）
	if !payload.Null.Empty() || !payload.Absent.Empty() {
		t.Fatalf("null / 缺失字段应为 Empty，got %q / %q", payload.Null, payload.Absent)
	}
	zero := RawAmount("0")
	if zero.Empty() {
		t.Fatalf("字面量 0 是**已知 0**，不能判成 Empty")
	}
	v, err := zero.MinorUnits(MicroScale)
	if err != nil || v != 0 {
		t.Fatalf("RawAmount(\"0\").MinorUnits = %d, %v", v, err)
	}
	// 空值解析报错而不是当 0：该不该当 0 由调用方按 §5.1 决定
	if _, err := payload.Null.MinorUnits(MicroScale); !errors.Is(err, ErrFormat) {
		t.Fatalf("空 RawAmount 解析必须报错，got %v", err)
	}
}

func TestRawAmountInt64(t *testing.T) {
	v, err := RawAmount("500000").Int64()
	if err != nil || v != 500000 {
		t.Fatalf("Int64 = %d, %v", v, err)
	}
}
