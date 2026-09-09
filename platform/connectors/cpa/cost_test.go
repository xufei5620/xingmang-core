package cpa

import (
	"database/sql"
	"testing"
)

func TestPriceMicroPer1M(t *testing.T) {
	t.Run("null is unpriced, not zero", func(t *testing.T) {
		got, err := priceMicroPer1M(sql.NullFloat64{Valid: false})
		if err != nil {
			t.Fatalf("priceMicroPer1M: %v", err)
		}
		if got != nil {
			t.Fatalf("got %v, want nil (unpriced)", *got)
		}
	})

	t.Run("a real zero price is known, not nil", func(t *testing.T) {
		got, err := priceMicroPer1M(sql.NullFloat64{Valid: true, Float64: 0})
		if err != nil {
			t.Fatalf("priceMicroPer1M: %v", err)
		}
		if got == nil || *got != 0 {
			t.Fatalf("got %v, want a known 0", got)
		}
	})

	t.Run("3.00 dollars per 1M becomes 3_000_000 micro-USD", func(t *testing.T) {
		got, err := priceMicroPer1M(sql.NullFloat64{Valid: true, Float64: 3.00})
		if err != nil {
			t.Fatalf("priceMicroPer1M: %v", err)
		}
		if got == nil || *got != 3_000_000 {
			t.Fatalf("got %v, want 3000000", got)
		}
	})

	t.Run("a price too far from a clean decimal is still parsed exactly via shortest round-trip text", func(t *testing.T) {
		// 0.15 is what a human wrote; float64(0.15) is not exactly 0.15, but
		// strconv.FormatFloat('f', -1, 64) recovers the shortest decimal that
		// round-trips back to the same float64 — which is "0.15", not some
		// long binary-fraction expansion. This is the whole point of
		// formatting before parsing instead of doing float arithmetic.
		got, err := priceMicroPer1M(sql.NullFloat64{Valid: true, Float64: 0.15})
		if err != nil {
			t.Fatalf("priceMicroPer1M: %v", err)
		}
		if got == nil || *got != 150_000 {
			t.Fatalf("got %v, want 150000 (0.15 * 1e6)", got)
		}
	})
}

func TestCostMicros(t *testing.T) {
	cases := []struct {
		name            string
		tokens          int64
		priceMicroPer1M int64
		want            int64
		wantErr         bool
	}{
		{name: "one million tokens at $3/1M is exactly 3,000,000 micro-USD",
			tokens: 1_000_000, priceMicroPer1M: 3_000_000, want: 3_000_000},
		{name: "half-up rounding on a .5 remainder",
			// 1234567 * 2500000 = 3,086,417,500,000 ; /1e6 = 3,086,417.5 -> 3,086,418
			tokens: 1_234_567, priceMicroPer1M: 2_500_000, want: 3_086_418},
		{name: "zero tokens costs zero regardless of price",
			tokens: 0, priceMicroPer1M: 999_999_999, want: 0},
		{name: "zero price costs zero regardless of volume",
			tokens: 999_999_999, priceMicroPer1M: 0, want: 0},
		{name: "large volume against an expensive model does not overflow int64 in the intermediate product",
			// 10,000,000,000 tokens * 1,000,000,000 micro-USD/1M ($1000/1M) would
			// overflow int64 as a raw product (1e19); math/big must carry it.
			tokens: 10_000_000_000, priceMicroPer1M: 1_000_000_000, want: 10_000_000_000_000},
		{name: "negative tokens are rejected, never silently negated",
			tokens: -1, priceMicroPer1M: 1, wantErr: true},
		{name: "negative price is rejected",
			tokens: 1, priceMicroPer1M: -1, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := costMicros(tc.tokens, tc.priceMicroPer1M)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("costMicros(%d, %d) = %d, nil; want error", tc.tokens, tc.priceMicroPer1M, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("costMicros(%d, %d): %v", tc.tokens, tc.priceMicroPer1M, err)
			}
			if got != tc.want {
				t.Fatalf("costMicros(%d, %d) = %d, want %d", tc.tokens, tc.priceMicroPer1M, got, tc.want)
			}
		})
	}
}

func microPtr(v int64) *int64 { return &v }

func TestRowCost(t *testing.T) {
	t.Run("no model_prices row at all is unpriced", func(t *testing.T) {
		row := ProviderModelUsage{TokensIn: 100, TokensOut: 50}
		got, err := rowCost(row, nil)
		if err != nil {
			t.Fatalf("rowCost: %v", err)
		}
		if got != nil {
			t.Fatalf("got %v, want nil", *got)
		}
	})

	t.Run("fully priced row sums every used tier", func(t *testing.T) {
		row := ProviderModelUsage{
			TokensIn: 1_000_000, TokensOut: 1_000_000,
			TokensCacheRead: 1_000_000, TokensCacheCreation: 1_000_000,
		}
		price := &modelPrice{
			promptMicroPer1M: microPtr(3_000_000), completionMicroPer1M: microPtr(15_000_000),
			cacheReadMicroPer1M: microPtr(300_000), cacheCreationMicroPer1M: microPtr(3_750_000),
		}
		got, err := rowCost(row, price)
		if err != nil {
			t.Fatalf("rowCost: %v", err)
		}
		want := int64(3_000_000 + 15_000_000 + 300_000 + 3_750_000)
		if got == nil || *got != want {
			t.Fatalf("got %v, want %d", got, want)
		}
	})

	t.Run("an unused tier with no price does not block pricing the row", func(t *testing.T) {
		row := ProviderModelUsage{TokensIn: 1_000_000, TokensOut: 1_000_000} // no cache tokens at all
		price := &modelPrice{promptMicroPer1M: microPtr(1_000_000), completionMicroPer1M: microPtr(2_000_000)}
		got, err := rowCost(row, price)
		if err != nil {
			t.Fatalf("rowCost: %v", err)
		}
		if got == nil || *got != 3_000_000 {
			t.Fatalf("got %v, want 3000000", got)
		}
	})

	t.Run("a used tier with no price makes the whole row unpriced, not partially priced", func(t *testing.T) {
		row := ProviderModelUsage{TokensIn: 1_000_000, TokensCacheCreation: 1_000_000}
		price := &modelPrice{promptMicroPer1M: microPtr(1_000_000)} // no cache_creation price at all
		got, err := rowCost(row, price)
		if err != nil {
			t.Fatalf("rowCost: %v", err)
		}
		if got != nil {
			t.Fatalf("got %v, want nil (must not silently omit the unpriced cache_creation tier)", *got)
		}
	})

	t.Run("generic cache_per_1m fills in for missing split cache prices", func(t *testing.T) {
		row := ProviderModelUsage{TokensCacheRead: 1_000_000, TokensCacheCreation: 1_000_000}
		price := &modelPrice{cacheMicroPer1M: microPtr(500_000)}
		got, err := rowCost(row, price)
		if err != nil {
			t.Fatalf("rowCost: %v", err)
		}
		if got == nil || *got != 1_000_000 {
			t.Fatalf("got %v, want 1000000 (500000 twice)", got)
		}
	})

	t.Run("split cache prices win over the generic fallback when both are present", func(t *testing.T) {
		row := ProviderModelUsage{TokensCacheRead: 1_000_000}
		price := &modelPrice{cacheMicroPer1M: microPtr(999_999_999), cacheReadMicroPer1M: microPtr(300_000)}
		got, err := rowCost(row, price)
		if err != nil {
			t.Fatalf("rowCost: %v", err)
		}
		if got == nil || *got != 300_000 {
			t.Fatalf("got %v, want 300000 (split price, not the generic fallback)", got)
		}
	})
}
