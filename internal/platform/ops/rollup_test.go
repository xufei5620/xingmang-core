package ops

import (
	"encoding/json"
	"errors"
	"math/big"
	"reflect"
	"testing"
	"time"
)

func accumulatorPolicy(kind ValueKind, primary PrimaryKind, pointer, currency string) RollupPolicy {
	return RollupPolicy{
		Version: 1, MetricKey: "test.metric", ValueKind: kind, PrimaryKind: primary,
		PrimaryJSONPointer: pointer, CurrencyJSONPointer: currency, Unit: "minor_units",
		Scale: 1, SumMode: "forbidden", BucketTimezone: "UTC", FullValueRequired: true,
		PartialValueAllowed: true,
	}
}

func rawSample(id int64, at time.Time, status SyncStatus, partial bool, value map[string]any) RawRollupSample {
	errorCode := ""
	if status == SyncFailed {
		errorCode = "test_failure"
	}
	return RawRollupSample{
		ID: id, MetricKey: "test.metric", Source: "source-a", Environment: "development",
		SyncedAt: at.UTC(), Status: status, IsPartial: partial, LastErrorCode: errorCode, Value: value,
		PolicyVersion: 1,
	}
}

func TestAccumulatorUsesSyncedAtUTCDayAndIDTieBreak(t *testing.T) {
	policy := accumulatorPolicy(ValueGauge, PrimaryCount, "/count", "")
	day := time.Date(2026, time.August, 28, 23, 59, 59, 0, time.UTC)
	first, err := NewDailyAccumulator(policy, rawSample(2, day, SyncOK, false, map[string]any{"count": json.Number("20")}))
	if err != nil {
		t.Fatal(err)
	}
	got, err := first.Merge(policy, []RawRollupSample{
		rawSample(3, day, SyncOK, false, map[string]any{"count": json.Number("30")}),
		rawSample(4, day, SyncOK, false, map[string]any{"count": json.Number("40")}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.BucketDay.Equal(time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("bucket day = %v", got.BucketDay)
	}
	if got.FirstNumeric == nil || got.FirstNumeric.String() != "20" || got.LastNumeric == nil || got.LastNumeric.String() != "40" {
		t.Fatalf("first/last = %v/%v", got.FirstNumeric, got.LastNumeric)
	}
	if got.FirstFullSampleID != 2 || got.LastFullSampleID != 4 {
		t.Fatalf("first/last ids = %d/%d", got.FirstFullSampleID, got.LastFullSampleID)
	}
}

func TestFailedOldValueNeverEntersNumeric(t *testing.T) {
	policy := accumulatorPolicy(ValueGauge, PrimaryCount, "/count", "")
	start := time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC)
	got, err := NewDailyAccumulator(policy, rawSample(1, start, SyncFailed, false, map[string]any{"count": json.Number("999")}))
	if err != nil {
		t.Fatal(err)
	}
	if got.FirstNumeric != nil || got.LastNumeric != nil || got.NumericCount != 0 || got.FailedCount != 1 {
		t.Fatalf("failed value polluted accumulator: %+v", got)
	}
}

func TestPartialSuccessStaysSeparateFromFull(t *testing.T) {
	policy := accumulatorPolicy(ValueGauge, PrimaryCount, "/count", "")
	start := time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC)
	got, err := NewDailyAccumulator(policy, rawSample(1, start, SyncOK, true, map[string]any{"count": json.Number("10")}))
	if err != nil {
		t.Fatal(err)
	}
	got, err = got.Merge(policy, []RawRollupSample{rawSample(2, start.Add(time.Minute), SyncOK, false, map[string]any{"count": json.Number("20")})})
	if err != nil {
		t.Fatal(err)
	}
	if got.FullSuccessCount != 1 || got.PartialSuccessCount != 1 || got.SampleCount != 2 || got.NumericCount != 1 {
		t.Fatalf("quality buckets = %+v", got)
	}
	if got.LastPartialValue == nil || got.LastPartialValue["count"] != json.Number("10") {
		t.Fatalf("partial representative = %#v", got.LastPartialValue)
	}
}

func TestSnapshotFirstLastMinMaxButNeverSum(t *testing.T) {
	policy := accumulatorPolicy(ValueDailySnapshot, PrimaryCount, "/count", "")
	start := time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC)
	got, err := NewDailyAccumulator(policy, rawSample(1, start, SyncOK, false, map[string]any{"count": json.Number("10")}))
	if err != nil {
		t.Fatal(err)
	}
	got, err = got.Merge(policy, []RawRollupSample{
		rawSample(2, start.Add(time.Minute), SyncOK, false, map[string]any{"count": json.Number("30")}),
		rawSample(3, start.Add(2*time.Minute), SyncOK, false, map[string]any{"count": json.Number("20")}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.SumNumeric != nil || got.MinNumeric.String() != "10" || got.MaxNumeric.String() != "30" {
		t.Fatalf("snapshot stats = %+v", got)
	}
}

func TestMoneyMixedCurrencyOmitsNumeric(t *testing.T) {
	policy := accumulatorPolicy(ValueDailySnapshot, PrimaryMoneyMinor, "/amount", "/currency")
	start := time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC)
	got, err := NewDailyAccumulator(policy, rawSample(1, start, SyncOK, false, map[string]any{"amount": json.Number("10"), "currency": "CNY"}))
	if err != nil {
		t.Fatal(err)
	}
	got, err = got.Merge(policy, []RawRollupSample{rawSample(2, start.Add(time.Minute), SyncOK, false, map[string]any{"amount": json.Number("20"), "currency": "USD"})})
	if err != nil {
		t.Fatal(err)
	}
	if got.FirstNumeric != nil || got.LastNumeric != nil || got.MinNumeric != nil || got.MaxNumeric != nil {
		t.Fatalf("mixed currency retained numeric values: %+v", got)
	}
	if !reflect.DeepEqual(got.CurrencySet, []string{"CNY", "USD"}) || got.NumericUnknownReason != "mixed_currency" {
		t.Fatalf("currency set/reason = %#v/%q", got.CurrencySet, got.NumericUnknownReason)
	}
}

func TestIntegerAboveTwoToThe53AndFixedPointStayExact(t *testing.T) {
	policy := accumulatorPolicy(ValueGauge, PrimaryCount, "/count", "")
	large := "9007199254740995"
	start := time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC)
	got, err := NewDailyAccumulator(policy, rawSample(1, start, SyncOK, false, map[string]any{"count": json.Number(large)}))
	if err != nil {
		t.Fatal(err)
	}
	if got.FirstNumeric.String() != large || got.LastNumeric.String() != large {
		t.Fatalf("large integer changed: %+v", got)
	}
	if _, err := NewDailyAccumulator(policy, rawSample(2, start, SyncOK, false, map[string]any{"count": 1.25})); err == nil {
		t.Fatal("float primary must be rejected")
	}
}

func TestDocumentKeepsRepresentativeJSONWithoutArrayAggregation(t *testing.T) {
	policy := accumulatorPolicy(ValueDocumentStatus, PrimaryCount, "/channel_count", "")
	start := time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC)
	value := map[string]any{"channel_count": json.Number("2"), "channels": []any{map[string]any{"id": "a"}}}
	got, err := NewDailyAccumulator(policy, rawSample(1, start, SyncOK, false, value))
	if err != nil {
		t.Fatal(err)
	}
	if got.FirstFullValue == nil || got.LastFullValue == nil {
		t.Fatalf("representative JSON missing: %+v", got)
	}
	if _, ok := got.FirstFullValue["channels"].([]any); !ok {
		t.Fatalf("channels array was transformed: %#v", got.FirstFullValue["channels"])
	}
}

func TestCoverageUsesSlotsAndUnknownCadenceReturnsNull(t *testing.T) {
	policy := accumulatorPolicy(ValueGauge, PrimaryCount, "/count", "")
	interval := int32(300)
	start := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	a := rawSample(1, start, SyncOK, false, map[string]any{"count": json.Number("1")})
	a.ExpectedIntervalSeconds = &interval
	b := rawSample(2, start.Add(5*time.Minute), SyncFailed, false, map[string]any{"count": json.Number("2")})
	b.ExpectedIntervalSeconds = &interval
	got, err := NewDailyAccumulator(policy, a)
	if err != nil {
		t.Fatal(err)
	}
	got, err = got.Merge(policy, []RawRollupSample{b})
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpectedSlotCount == nil || *got.ExpectedSlotCount != 288 || got.CoveredSlotCount == nil || *got.CoveredSlotCount != 2 {
		t.Fatalf("coverage slots = %v/%v", got.ExpectedSlotCount, got.CoveredSlotCount)
	}
	if got.CoveragePPM == nil || *got.CoveragePPM != 6944 {
		t.Fatalf("coverage ppm = %v", got.CoveragePPM)
	}
	legacy := rawSample(3, start, SyncOK, false, map[string]any{"count": json.Number("3")})
	unknown, err := NewDailyAccumulator(policy, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if unknown.ExpectedSlotCount != nil || unknown.CoveredSlotCount != nil || unknown.CoveragePPM != nil {
		t.Fatalf("legacy cadence should be unknown: %+v", unknown)
	}
}

func TestAccumulatorRejectsDuplicateOrNonIncreasingSampleID(t *testing.T) {
	policy := accumulatorPolicy(ValueGauge, PrimaryCount, "/count", "")
	start := time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC)
	got, err := NewDailyAccumulator(policy, rawSample(2, start, SyncOK, false, map[string]any{"count": json.Number("1")}))
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []RawRollupSample{
		rawSample(2, start.Add(time.Minute), SyncOK, false, map[string]any{"count": json.Number("2")}),
		rawSample(1, start.Add(time.Minute), SyncOK, false, map[string]any{"count": json.Number("2")}),
	} {
		if _, err := got.Merge(policy, []RawRollupSample{sample}); err == nil {
			t.Fatalf("sample id %d should be rejected", sample.ID)
		}
	}
}

func TestAccumulatorRejectsPolicyOrStreamMismatch(t *testing.T) {
	policy := accumulatorPolicy(ValueGauge, PrimaryCount, "/count", "")
	start := time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC)
	got, err := NewDailyAccumulator(policy, rawSample(1, start, SyncOK, false, map[string]any{"count": json.Number("1")}))
	if err != nil {
		t.Fatal(err)
	}
	other := policy
	other.Version = 2
	if _, err := got.Merge(other, nil); err == nil {
		t.Fatal("policy version mismatch must fail")
	}
}

func TestInvoiceAccumulatorIsGatedUntilCR0002(t *testing.T) {
	policy := accumulatorPolicy(ValueDailySnapshot, PrimaryCount, "/count", "")
	policy.MetricKey = "invoice.requests.daily"
	sample := rawSample(1, time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC), SyncOK, false, map[string]any{"count": json.Number("1")})
	sample.MetricKey = policy.MetricKey
	if _, err := NewDailyAccumulator(policy, sample); !errors.Is(err, ErrInvoiceRollupGated) {
		t.Fatalf("invoice accumulator error = %v, want ErrInvoiceRollupGated", err)
	}
}

func TestAccumulatorNeverUsesFloatArithmetic(t *testing.T) {
	policy := accumulatorPolicy(ValueGauge, PrimaryCount, "/count", "")
	start := time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC)
	for _, value := range []any{float32(1), float64(1), json.Number("1.0")} {
		if _, err := NewDailyAccumulator(policy, rawSample(1, start, SyncOK, false, map[string]any{"count": value})); err == nil {
			t.Errorf("value %T should be rejected as non-integer", value)
		}
	}
	if _, ok := new(big.Int).SetString("1", 10); !ok {
		t.Fatal("big.Int sanity")
	}
}

func TestCoverageSlotsUseMathematicalFloorBeforeUnixEpoch(t *testing.T) {
	policy := accumulatorPolicy(ValueGauge, PrimaryCount, "/count", "")
	interval := int32(300)
	at := time.Unix(-1, 0).UTC()
	sample := rawSample(1, at, SyncOK, false, map[string]any{"count": json.Number("1")})
	sample.ExpectedIntervalSeconds = &interval
	got, err := NewDailyAccumulator(policy, sample)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.coverageSlots) != 1 {
		t.Fatalf("coverage slots = %#v", got.coverageSlots)
	}
	if _, ok := got.coverageSlots[-1]; !ok {
		t.Fatalf("slot for unix -1 must floor to -1, got %#v", got.coverageSlots)
	}
}
