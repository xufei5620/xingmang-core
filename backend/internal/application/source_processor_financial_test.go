package application

import "testing"

func TestProratedCNYRefundMinor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                       string
		amount, paid, refund, want int64
	}{
		{name: "balance multiplier", amount: 12_000, paid: 10_000, refund: 6_000, want: 5_000},
		{name: "subscription conversion", amount: 999, paid: 7_243, refund: 500, want: 3_625},
		{name: "full", amount: 999, paid: 7_243, refund: 999, want: 7_243},
		{name: "half cent up", amount: 800, paid: 100, refund: 100, want: 13},
		{name: "fee", amount: 10_000, paid: 10_300, refund: 2_000, want: 2_060},
		{name: "zero", amount: 10_000, paid: 8_000, refund: 0, want: 0},
		{name: "large no overflow", amount: 9_000_000_000_000_000_000, paid: 8_000_000_000_000_000_000, refund: 4_500_000_000_000_000_000, want: 4_000_000_000_000_000_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := proratedCNYRefundMinor(tc.amount, tc.paid, tc.refund)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got=%d want=%d", got, tc.want)
			}
		})
	}
}

func TestProratedCNYRefundMinorRejectsAnomalies(t *testing.T) {
	t.Parallel()
	for _, tc := range [][3]int64{
		{0, 100, 1}, {-1, 100, 1}, {100, -1, 1}, {100, 100, -1}, {100, 100, 101},
	} {
		if got, err := proratedCNYRefundMinor(tc[0], tc[1], tc[2]); err == nil {
			t.Fatalf("anomaly accepted as %d: %+v", got, tc)
		}
	}
}
