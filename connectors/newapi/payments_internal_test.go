package newapi

import "testing"

func TestPaymentStatusBucketCoversEveryKnownStatus(t *testing.T) {
	want := map[string]string{
		"pending": PaymentStatusPending,
		"success": PaymentStatusSucceeded,
		"failed":  PaymentStatusFailed,
		"expired": PaymentStatusFailed,
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
		if bucket == PaymentStatusRefunded {
			t.Errorf("NewAPI 没有退款概念，任何状态都不该落进 refunded 桶（状态 %s）", status)
		}
	}
}

func TestPaymentStatusBucketRejectsUnknownStatus(t *testing.T) {
	for _, status := range []string{"", "made_up_status", "successx", "refunded"} {
		if _, ok := paymentStatusBucket(status); ok {
			t.Errorf("未知状态 %q 不该有分桶", status)
		}
	}
	if bucket, ok := paymentStatusBucket("  SUCCESS  "); !ok || bucket != PaymentStatusSucceeded {
		t.Errorf("大小写与首尾空白不该影响归类, got %q ok=%v", bucket, ok)
	}
}

func TestKnownOrderStatusCaseInsensitive(t *testing.T) {
	if !KnownOrderStatus("success") || !KnownOrderStatus("SUCCESS") || !KnownOrderStatus(" success ") {
		t.Fatal("KnownOrderStatus 应大小写不敏感且容忍首尾空白")
	}
	if KnownOrderStatus("bogus") {
		t.Fatal("未知状态不该被认作已知")
	}
}

func TestUserRefFormatsNumericID(t *testing.T) {
	if got := userRef(42); got != "u42" {
		t.Errorf("userRef(42) = %q, want u42", got)
	}
}
