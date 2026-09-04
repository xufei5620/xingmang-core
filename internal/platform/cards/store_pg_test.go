package cards

import (
	"testing"

	"github.com/xufei5620/xingmang-platform/connectors/infini"
)

// 整数最小单位还原成文本时不经 float：大额下 float64 会悄悄丢精度。
func TestMinorToDecimalRoundTripsWithoutFloat(t *testing.T) {
	cases := []struct {
		minor int64
		want  string
	}{
		{0, "0.000000"},
		{1, "0.000001"},
		{1000000, "1.000000"},
		{10500000, "10.500000"},
		{-2500000, "-2.500000"},
		// 超过 float64 尾数精度的金额：这正是不能走 float 的理由
		{9007199254740993, "9007199254.740993"},
	}

	for _, tc := range cases {
		if got := minorToDecimal(tc.minor, limitScale); got != tc.want {
			t.Fatalf("minorToDecimal(%d) = %q, want %q", tc.minor, got, tc.want)
		}
	}
}

// 还原出来的文本必须能被限额校验重新解析——两者是同一条链上的相邻环节。
func TestMinorToDecimalOutputIsAcceptedByLimits(t *testing.T) {
	l := Limits{PerOperation: "100", PerDay: "500"}
	spent := minorToDecimal(495000000, limitScale) // 495

	if err := l.Check("10", spent); err == nil {
		t.Fatal("495 + 10 应超过单日上限 500")
	}
}

// 上游不给交易 id，去重键从内容派生：同一笔算同一个，不同笔必须不同。
func TestTransactionDedupeKeyDistinguishesTransactions(t *testing.T) {
	base := infini.CardTransaction{
		OccurredAt: "2026-09-02T08:00:00Z", AmountMinor: 350,
		Merchant: "OPENAI", Type: "purchase",
	}

	same := transactionDedupeKey("card_1", base)
	if same != transactionDedupeKey("card_1", base) {
		t.Fatal("同一笔交易必须派生出同一个去重键")
	}

	other := base
	other.AmountMinor = 351
	if same == transactionDedupeKey("card_1", other) {
		t.Fatal("金额不同必须派生出不同的去重键")
	}
	if same == transactionDedupeKey("card_2", base) {
		t.Fatal("卡不同必须派生出不同的去重键")
	}

	otherTime := base
	otherTime.OccurredAt = "2026-09-02T08:00:01Z"
	if same == transactionDedupeKey("card_1", otherTime) {
		t.Fatal("时刻不同必须派生出不同的去重键")
	}
}
