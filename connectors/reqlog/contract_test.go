package reqlog_test

import (
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/connectors/reqlog"
	"github.com/xufei5620/xingmang-platform/connectors/reqlog/contracttest"
)

// Fake 必须通过契约套件——套件本身要先被验证有效，
// 否则将来的真实实现拿它做合规判据就没有意义。
func TestFakeSatisfiesContract(t *testing.T) {
	contracttest.RunSuite(t, func(opts reqlog.FakeOptions) reqlog.ReadClient {
		return reqlog.NewFake(opts)
	})
}

func TestMaskIP(t *testing.T) {
	cases := map[string]string{
		// IPv4：保留前三段
		"203.0.113.42":     "203.0.113.x",
		"203.0.113.42:443": "203.0.113.x",
		"10.0.0.1":         "10.0.0.x",
		"0.0.0.0":          "0.0.0.x",
		// IPv6：保留前 48 位。压缩写法里的第三组必须按展开形态取，
		// 而不是「字符串里下一个出现的数」
		"2001:db8:1234:5678::1":   "2001:db8:1234:x",
		"[2001:db8:1234:5678::1]": "2001:db8:1234:x",
		"[2001:db8::1]:9300":      "2001:db8:0:x",
		"::1":                     "0:0:0:x",
		// IPv4-mapped IPv6 按 IPv4 处理
		"::ffff:203.0.113.42": "203.0.113.x",
		// 空串是「没记 IP」，与「记了但看不清」不是一回事
		"":    "",
		"   ": "",
		// 解析不出一律给占位符，**绝不回退到透传**
		"not-an-ip":              reqlog.UnparsedIPPlaceholder,
		"203.0.113.42, 10.0.0.1": reqlog.UnparsedIPPlaceholder,
		"999.1.1.1":              reqlog.UnparsedIPPlaceholder,
	}
	for in, want := range cases {
		if got := reqlog.MaskIP(in); got != want {
			t.Errorf("MaskIP(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaskIPNeverEchoesLastOctet(t *testing.T) {
	// 上一条断言的是「等于期望值」，这条断言的是「末段真的没了」——
	// 前者会被一次「顺手改了期望值」的提交蒙混过去，后者不会
	for _, raw := range []string{"203.0.113.42", "198.51.100.7", "192.0.2.181"} {
		masked := reqlog.MaskIP(raw)
		last := raw[strings.LastIndex(raw, ".")+1:]
		if strings.Contains(masked, "."+last) {
			t.Errorf("MaskIP(%q) = %q 仍含末段 %q", raw, masked, last)
		}
	}
}

func TestTruncateUTF8DoesNotSplitRunes(t *testing.T) {
	// 「你好世界」每个字 3 字节。截到 5 字节必须退回到 3（一个完整的字），
	// 而不是切出半个字符——那会产出 U+FFFD，看起来像上游返回了乱码
	const s = "你好世界"
	got, truncated, original := reqlog.TruncateUTF8(s, 5)
	if !truncated {
		t.Fatal("应标记为已截断")
	}
	if got != "你" {
		t.Fatalf("TruncateUTF8 = %q, want %q", got, "你")
	}
	if original != int64(len(s)) {
		t.Fatalf("OriginalBytes = %d, want %d", original, len(s))
	}

	// 恰好等于上限时不截断
	if out, tr, _ := reqlog.TruncateUTF8(s, len(s)); tr || out != s {
		t.Fatalf("恰好等于上限不该截断: %q %v", out, tr)
	}
	// max <= 0 表示不限
	if out, tr, _ := reqlog.TruncateUTF8(s, 0); tr || out != s {
		t.Fatalf("max<=0 应表示不限: %q %v", out, tr)
	}
	// 每个字符都超过上限时退到空串，而不是切坏
	if out, tr, _ := reqlog.TruncateUTF8(s, 1); !tr || out != "" {
		t.Fatalf("上限小于单个字符时应退到空串: %q %v", out, tr)
	}
}

func TestParseSource(t *testing.T) {
	for _, in := range []string{"sub2api", " Sub2API ", "NEWAPI"} {
		if _, err := reqlog.ParseSource(in); err != nil {
			t.Errorf("ParseSource(%q) 应通过: %v", in, err)
		}
	}
	for _, in := range []string{"", "cpa", "invoice", "sub2api;drop"} {
		if _, err := reqlog.ParseSource(in); err == nil {
			t.Errorf("ParseSource(%q) 应被拒绝", in)
		}
	}
}

func TestNormalizeLimitClamps(t *testing.T) {
	cases := map[int]int{
		0:                       reqlog.DefaultListLimit,
		-5:                      reqlog.DefaultListLimit,
		10:                      10,
		reqlog.MaxListLimit:     reqlog.MaxListLimit,
		reqlog.MaxListLimit + 1: reqlog.MaxListLimit,
		1 << 20:                 reqlog.MaxListLimit,
	}
	for in, want := range cases {
		if got := reqlog.NormalizeLimit(in); got != want {
			t.Errorf("NormalizeLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestBilledAmountValidationFailsClosed(t *testing.T) {
	cases := map[string]reqlog.BilledAmount{
		"负数":   {AmountMinor: -1, Currency: "USD", Scale: 2},
		"空币种":  {AmountMinor: 1, Currency: "", Scale: 2},
		"负标度":  {AmountMinor: 1, Currency: "USD", Scale: -1},
		"标度过大": {AmountMinor: 1, Currency: "USD", Scale: 19},
	}
	for name, amount := range cases {
		if err := amount.Validate(); err == nil {
			t.Errorf("%s应被拒绝: %+v", name, amount)
		}
	}
	if err := (reqlog.BilledAmount{AmountMinor: 0, Currency: "USD", Scale: 3}).Validate(); err != nil {
		t.Fatalf("已知的 0 是合法金额: %v", err)
	}
}
