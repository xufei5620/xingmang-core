package sub2api

import "testing"

func TestPaymentStatusBucketCoversEveryKnownStatus(t *testing.T) {
	// 每一个 KnownOrderStatuses 都必须落进四个桶之一——漏一个就会在
	// DailyPaymentSummary 里被静默丢弃（归进 currencyGap/IsPartial，但笔数
	// 与金额都不会出现在任何桶里，等于凭空消失）。
	want := map[string]string{
		"PENDING":            PaymentStatusPending,
		"PAID":               PaymentStatusSucceeded,
		"RECHARGING":         PaymentStatusSucceeded,
		"COMPLETED":          PaymentStatusSucceeded,
		"EXPIRED":            PaymentStatusFailed,
		"CANCELLED":          PaymentStatusFailed,
		"FAILED":             PaymentStatusFailed,
		"REFUND_REQUESTED":   PaymentStatusRefunded,
		"REFUNDING":          PaymentStatusRefunded,
		"REFUND_PENDING":     PaymentStatusRefunded,
		"PARTIALLY_REFUNDED": PaymentStatusRefunded,
		"REFUNDED":           PaymentStatusRefunded,
		"REFUND_FAILED":      PaymentStatusRefunded,
	}
	if len(want) != len(KnownOrderStatuses) {
		t.Fatalf("测试用例覆盖 %d 个状态，KnownOrderStatuses 有 %d 个——两边必须同步",
			len(want), len(KnownOrderStatuses))
	}
	for _, status := range KnownOrderStatuses {
		bucket, ok := paymentStatusBucket(status)
		if !ok {
			t.Errorf("状态 %s 没有落进任何分桶", status)
			continue
		}
		if bucket != want[status] {
			t.Errorf("状态 %s 落进 %s，want %s", status, bucket, want[status])
		}
	}
}

func TestPaymentStatusBucketRejectsUnknownStatus(t *testing.T) {
	for _, status := range []string{"", "made_up_status", "paidx"} {
		if _, ok := paymentStatusBucket(status); ok {
			t.Errorf("未知状态 %q 不该有分桶", status)
		}
	}
	// 大小写不敏感、容忍首尾空白：上游恒为大写，但判据不该因大小写或
	// 拼接时带进来的空白而漏判。
	for _, status := range []string{"paid", "  PAID  "} {
		if bucket, ok := paymentStatusBucket(status); !ok || bucket != PaymentStatusSucceeded {
			t.Errorf("paymentStatusBucket(%q) = %q ok=%v, want succeeded/true", status, bucket, ok)
		}
	}
}

func TestKnownOrderStatusCaseInsensitive(t *testing.T) {
	if !KnownOrderStatus("paid") || !KnownOrderStatus("PAID") || !KnownOrderStatus(" Paid ") {
		t.Fatal("KnownOrderStatus 应大小写不敏感且容忍首尾空白")
	}
	if KnownOrderStatus("bogus") {
		t.Fatal("未知状态不该被认作已知")
	}
}

func TestMaskEmailBoundaries(t *testing.T) {
	cases := []struct{ in, want string }{
		{"zhangwei@example.com", "zh***@example.com"},
		{"ab@example.com", "a***@example.com"},
		{"a@example.com", "***@example.com"},
		{"", ""},
		{"not-an-email", unparsedEmailPlaceholder},
		{"a@b", unparsedEmailPlaceholder}, // 域名没有点
	}
	for _, c := range cases {
		if got := maskEmail(c.in); got != c.want {
			t.Errorf("maskEmail(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMaskUserRefFallsBackToUserID(t *testing.T) {
	if got := maskUserRef("zhangwei@example.com", 42); got != "zh***@example.com" {
		t.Errorf("有邮箱时应优先打码邮箱, got %q", got)
	}
	if got := maskUserRef("", 42); got != "u42" {
		t.Errorf("空邮箱应退到 u<user_id>, got %q", got)
	}
	if got := maskUserRef("not-an-email", 7); got != "u7" {
		t.Errorf("解析不出的邮箱应退到 u<user_id>而不是占位符, got %q", got)
	}
}
