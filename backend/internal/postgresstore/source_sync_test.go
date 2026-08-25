package postgresstore

import (
	"testing"
	"time"
)

func TestEconomicHeartbeatAndWatermarkHaveIndependentFreshnessBudgets(t *testing.T) {
	now := time.Date(2026, time.August, 25, 2, 0, 0, 0, time.UTC)
	newItem := func() SourceStreamHealth {
		return SourceStreamHealth{
			SourceEnabled:          true,
			StreamID:               "payments",
			ApprovedRuntimeVersion: "0.1.179",
			ObservedRuntimeVersion: "0.1.179",
			ProjectionStatus:       "healthy",
			LastAcceptedAt:         now.Add(-30 * time.Second),
			EconomicWatermarkAt:    now.Add(-6 * time.Minute),
		}
	}
	policy := SourceFreshnessPolicy{
		EconomicHeartbeatMaxAge: 5 * time.Minute, EconomicWatermarkMaxAge: 15 * time.Minute,
		IdentitiesMaxAge: 15 * time.Minute, Now: now,
	}
	item := newItem()
	evaluateSourceStreamHealth(&item, SourceFreshnessPolicy{
		EconomicHeartbeatMaxAge: policy.EconomicHeartbeatMaxAge,
		EconomicWatermarkMaxAge: policy.EconomicWatermarkMaxAge,
		IdentitiesMaxAge:        policy.IdentitiesMaxAge, Now: policy.Now,
	})
	if !item.Ready || len(item.Reasons) != 0 || item.MaximumAgeSeconds != 300 || item.EconomicWatermarkMaximumAgeSeconds != 900 {
		t.Fatalf("quiet stream should be ready with reviewed headroom: %#v", item)
	}

	item = newItem()
	item.LastAcceptedAt = now.Add(-6 * time.Minute)
	evaluateSourceStreamHealth(&item, policy)
	if item.Ready || !containsReason(item.Reasons, "STREAM_STALE") || containsReason(item.Reasons, "ECONOMIC_WATERMARK_STALE") {
		t.Fatalf("six-minute heartbeat did not fail independently: %#v", item)
	}

	item = newItem()
	item.EconomicWatermarkAt = now.Add(-15*time.Minute - time.Nanosecond)
	evaluateSourceStreamHealth(&item, policy)
	if item.Ready || containsReason(item.Reasons, "STREAM_STALE") || !containsReason(item.Reasons, "ECONOMIC_WATERMARK_STALE") {
		t.Fatalf("watermark beyond 15 minutes did not fail independently: %#v", item)
	}

	identity := newItem()
	identity.StreamID = "identities"
	identity.EconomicWatermarkAt = time.Time{}
	evaluateSourceStreamHealth(&identity, policy)
	if !identity.Ready || identity.MaximumAgeSeconds != 900 || identity.EconomicWatermarkMaximumAgeSeconds != 0 {
		t.Fatalf("identity stream inherited an economic watermark budget: %#v", identity)
	}
}

func containsReason(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
