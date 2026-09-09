package ops

import (
	"reflect"
	"testing"
	"time"
)

func TestDailyAccumulatorDeterministicAcrossRuns(t *testing.T) {
	policy := accumulatorPolicy(ValueGauge, PrimaryCount, "/count", "")
	at := time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC)
	sample := rawSample(1, at, SyncOK, false, map[string]any{"count": int64(7)})
	first, err := NewDailyAccumulator(policy, sample)
	if err != nil {
		t.Fatal(err)
	}
	// Ensure wall-clock implementations cannot accidentally pass due to the
	// platform clock's coarse resolution.
	time.Sleep(2 * time.Millisecond)
	second, err := NewDailyAccumulator(policy, sample)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("same samples produced non-deterministic accumulators:\nfirst=%#v\nsecond=%#v", first, second)
	}
}
