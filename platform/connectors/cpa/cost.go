package cpa

import (
	"database/sql"
	"fmt"
	"math/big"
	"strconv"

	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// priceMicroPer1M converts one model_prices REAL column (USD per 1,000,000
// tokens) into fixed-point micro-USD-per-1M-tokens.
//
// The column crosses from SQLite's REAL storage into a Go float64 the moment
// database/sql scans it — that boundary crossing is unavoidable given the
// column's declared type, but it stops there: this function immediately
// formats the float64 back to its shortest round-tripping decimal text
// (strconv's documented algorithm for 'f'/-1, the same one %v uses) and hands
// that text to money.ParseMinorUnits, which never touches a float. Every
// caller of this function receives only the resulting int64; no float value
// is retained or propagated (constitution §13).
//
// !v.Valid (SQL NULL) returns (nil, nil): "no price configured." That is a
// different fact from a real, known price of exactly $0/1M tokens
// (v.Valid==true, v.Float64==0), which returns a non-nil zero. Conflating the
// two would let an unpriced model silently cost nothing instead of "unknown."
func priceMicroPer1M(v sql.NullFloat64) (*int64, error) {
	if !v.Valid {
		return nil, nil
	}
	text := strconv.FormatFloat(v.Float64, 'f', -1, 64)
	minor, err := money.ParseMinorUnits(text, money.MicroScale)
	if err != nil {
		return nil, fmt.Errorf("cpa: parse model price %q: %w", text, err)
	}
	return &minor, nil
}

// microDenominator is 1,000,000 — both the "per 1M tokens" pricing unit and,
// conveniently, 10^6 = money.MicroScale's own base, so the two purposes
// (per-million pricing, micro-currency-unit scale) cancel exactly with no
// separate rescale step.
var microDenominator = big.NewInt(1_000_000)
var microHalf = big.NewInt(500_000)

// costMicros computes tokens * priceMicroPer1M / 1,000,000 in micro-USD,
// i.e. the cost of `tokens` tokens at a price of `priceMicroPer1M` micro-USD
// per one million tokens.
//
// The multiplication is done in math/big, not int64, because the direct
// product can exceed int64 for realistic inputs: a busy key can push 1e10
// tokens through a $1,000/1M-token model (priceMicroPer1M = 1e9), whose
// product is 1e19 — already past int64's ~9.22e18 ceiling. math/big.Int is
// exact integer arithmetic, not floating point; using it here introduces no
// rounding beyond the single, deliberate half-up step below.
//
// Rounding is half-up (away from zero), matching money.ParseMinorUnits's
// documented convention so a value that round-trips through text and back
// lands on the same integer either way. Both inputs are counts/prices, never
// negative in any legitimate reading of usage_events or model_prices, so
// away-from-zero and towards-positive-infinity coincide; negative inputs are
// rejected rather than silently negated.
func costMicros(tokens int64, priceMicroPer1M int64) (int64, error) {
	if tokens < 0 || priceMicroPer1M < 0 {
		return 0, fmt.Errorf("cpa: cost inputs must be non-negative (tokens=%d price_micro_per_1m=%d)",
			tokens, priceMicroPer1M)
	}
	if tokens == 0 || priceMicroPer1M == 0 {
		return 0, nil
	}
	product := new(big.Int).Mul(big.NewInt(tokens), big.NewInt(priceMicroPer1M))
	product.Add(product, microHalf)
	quotient := new(big.Int).Quo(product, microDenominator)
	if !quotient.IsInt64() {
		return 0, fmt.Errorf("cpa: cost result overflows int64 (tokens=%d price_micro_per_1m=%d)",
			tokens, priceMicroPer1M)
	}
	return quotient.Int64(), nil
}

// modelPrice is one model_prices row's four price tiers, each possibly
// unconfigured (nil).
type modelPrice struct {
	promptMicroPer1M        *int64
	completionMicroPer1M    *int64
	cacheMicroPer1M         *int64 // generic fallback when the split tiers below are absent
	cacheReadMicroPer1M     *int64
	cacheCreationMicroPer1M *int64
}

// rowCost prices one usage row against its model's price tiers.
//
// The rule is all-or-nothing per row, deliberately conservative: a row is
// priced only if every tier it actually has non-zero tokens for resolves to
// a known price. A row with cache_creation tokens but no configured
// cache-creation price (and no usable generic fallback) is reported unpriced
// in full, never as "prompt+completion cost only" silently missing a piece —
// that would look like a complete, trustworthy number while quietly omitting
// part of the bill (constitution §12: no bare numbers standing in for
// incomplete data).
func rowCost(row ProviderModelUsage, price *modelPrice) (*int64, error) {
	if price == nil {
		return nil, nil // no model_prices row at all for this model
	}

	type tier struct {
		tokens int64
		micro  *int64
	}
	cacheReadPrice, cacheCreationPrice := price.cacheReadMicroPer1M, price.cacheCreationMicroPer1M
	if cacheReadPrice == nil && price.cacheMicroPer1M != nil {
		cacheReadPrice = price.cacheMicroPer1M
	}
	if cacheCreationPrice == nil && price.cacheMicroPer1M != nil {
		cacheCreationPrice = price.cacheMicroPer1M
	}
	tiers := []tier{
		{row.TokensIn, price.promptMicroPer1M},
		{row.TokensOut, price.completionMicroPer1M},
		{row.TokensCacheRead, cacheReadPrice},
		{row.TokensCacheCreation, cacheCreationPrice},
	}

	var total int64
	for _, t := range tiers {
		if t.tokens == 0 {
			continue // an unpriced, unused tier never blocks pricing the row
		}
		if t.micro == nil {
			return nil, nil // used tier has no usable price: whole row is unpriced
		}
		cost, err := costMicros(t.tokens, *t.micro)
		if err != nil {
			return nil, err
		}
		next := total + cost
		if next < total {
			return nil, fmt.Errorf("cpa: cost accumulation overflowed int64")
		}
		total = next
	}
	return &total, nil
}
