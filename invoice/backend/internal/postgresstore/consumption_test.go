package postgresstore

import (
	"math/big"
	"testing"
	"time"
)

func TestCumulativeCashRoundUsesExactHalfUpAndDelta(t *testing.T) {
	tests := []struct {
		name              string
		paid              int64
		consumed, total   int64
		want              int64
		wantRemainderSign int
	}{
		{name: "one cent exact half", paid: 1, consumed: 1, total: 2, want: 1, wantRemainderSign: -1},
		{name: "below half", paid: 1, consumed: 1, total: 3, want: 0, wantRemainderSign: 1},
		{name: "above half", paid: 1, consumed: 2, total: 3, want: 1, wantRemainderSign: -1},
		{name: "full consumption", paid: 60_00, consumed: 7, total: 7, want: 60_00, wantRemainderSign: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, numerator, remainder, err := cumulativeCashRound(tt.paid, big.NewInt(tt.consumed), big.NewInt(tt.total))
			if err != nil || got != tt.want || remainder.Sign() != tt.wantRemainderSign {
				t.Fatalf("rounded=%d numerator=%s remainder=%s err=%v", got, numerator, remainder, err)
			}
		})
	}
	first, _, _, err := cumulativeCashRound(1, big.NewInt(1), big.NewInt(3))
	if err != nil {
		t.Fatal(err)
	}
	second, _, _, err := cumulativeCashRound(1, big.NewInt(2), big.NewInt(3))
	if err != nil || second-first != 1 {
		t.Fatalf("cumulative delta=%d want 1 err=%v", second-first, err)
	}
}

func TestAmbiguousFactGroupRequiresComparableCausality(t *testing.T) {
	now := time.Now().UTC()
	payment := eligibilityFact{Kind: "payment", ID: "p", At: now}
	usage := eligibilityFact{Kind: "usage", ID: "u", At: now}
	if !ambiguousFactGroup([]eligibilityFact{payment, usage}) {
		t.Fatal("cross-kind equal timestamp without causality was accepted")
	}
	payment.CausalDomain, usage.CausalDomain = "source-ledger", "source-ledger"
	payment.CausalOrder, usage.CausalOrder = big.NewInt(1), big.NewInt(2)
	if ambiguousFactGroup([]eligibilityFact{payment, usage}) {
		t.Fatal("strict comparable causal order was rejected")
	}
	usage.CausalOrder = big.NewInt(1)
	if !ambiguousFactGroup([]eligibilityFact{payment, usage}) {
		t.Fatal("duplicate causal order was accepted")
	}
}
