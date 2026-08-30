package cpa

import (
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// TestReadCapabilitiesAreReadOnly proves every declared capability parses
// under registry's <system>.<resource>.<action> shape and is never a write
// capability (ADR-018 gate 4) — referenced from contract.go's doc comment.
func TestReadCapabilitiesAreReadOnly(t *testing.T) {
	if len(ReadCapabilities) == 0 {
		t.Fatal("ReadCapabilities is empty")
	}
	seen := map[registry.Capability]bool{}
	for _, c := range ReadCapabilities {
		parsed, err := registry.ParseCapability(string(c))
		if err != nil {
			t.Fatalf("capability %q does not parse: %v", c, err)
		}
		if parsed.IsWrite() {
			t.Fatalf("capability %q is classified as a write capability", c)
		}
		if seen[c] {
			t.Fatalf("capability %q is declared twice", c)
		}
		seen[c] = true
	}
}

// TestMetricKeysAreValid proves every metric key this package writes is
// shaped so ops.ValidMetricKey accepts it — a key ops would reject at write
// time would fail every sync silently until someone read the logs.
func TestMetricKeysAreValid(t *testing.T) {
	for _, key := range []string{MetricRequestsDaily, MetricCostDaily, MetricKeysUsage, MetricAccountsHealth} {
		if !ops.ValidMetricKey(key) {
			t.Fatalf("metric key %q is not a valid ops metric key shape", key)
		}
	}
}
