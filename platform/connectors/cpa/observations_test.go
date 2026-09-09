package cpa

import (
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

var obsNow = time.Date(2026, 8, 31, 12, 5, 0, 0, time.UTC)
var obsObserved = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)

func mustValidate(t *testing.T, o ops.Observation) {
	t.Helper()
	if err := o.Validate(); err != nil {
		t.Fatalf("Observation.Validate(): %v\nobservation: %+v", err, o)
	}
}

func TestRequestsObservation(t *testing.T) {
	usage := UsageSummary{
		Snapshot:          Snapshot{ObservedAt: obsObserved, Watermark: "wm", Instance: FileInstance},
		BusinessDay:       "2026-08-31",
		TotalRequestCount: 3,
		Rows: []ProviderModelUsage{
			{Provider: "anthropic", Model: "claude-x", RequestCount: 2},
			{Provider: "openai", Model: "gpt-y", RequestCount: 1},
		},
	}
	o := RequestsObservation(obsNow, "cpa-file", "production", usage)
	mustValidate(t, o)
	if o.MetricKey != MetricRequestsDaily {
		t.Fatalf("MetricKey = %q, want %q", o.MetricKey, MetricRequestsDaily)
	}
	if o.Value["total_request_count"] != int64(3) {
		t.Fatalf("total_request_count = %v, want 3", o.Value["total_request_count"])
	}
	byProvider, ok := o.Value["by_provider"].(map[string]int64)
	if !ok || byProvider["anthropic"] != 2 || byProvider["openai"] != 1 {
		t.Fatalf("by_provider = %v, want anthropic:2 openai:1", o.Value["by_provider"])
	}
}

func TestCostObservation_Priced(t *testing.T) {
	cost := int64(249000)
	usage := UsageSummary{
		Snapshot:        Snapshot{ObservedAt: obsObserved, Watermark: "wm", Instance: FileInstance},
		BusinessDay:     "2026-08-31",
		TotalCostMicros: &cost,
		Currency:        Currency,
	}
	o := CostObservation(obsNow, "cpa-file", "production", usage)
	mustValidate(t, o)
	if o.Value["total_cost_minor_units"] != cost {
		t.Fatalf("total_cost_minor_units = %v, want %d", o.Value["total_cost_minor_units"], cost)
	}
	if o.Value["currency"] != Currency {
		t.Fatalf("currency = %v, want %q", o.Value["currency"], Currency)
	}
	if _, present := o.Value["total_omitted_reason"]; present {
		t.Fatal("total_omitted_reason must not be present when a total is known")
	}
}

func TestCostObservation_Unpriced(t *testing.T) {
	usage := UsageSummary{
		Snapshot:             Snapshot{ObservedAt: obsObserved, Watermark: "wm", Instance: FileInstance, IsPartial: true},
		BusinessDay:          "2026-08-31",
		TotalCostMicros:      nil,
		UnpricedRequestCount: 5,
		UnpricedModels:       []string{"openai/gpt-y"},
	}
	o := CostObservation(obsNow, "cpa-file", "production", usage)
	mustValidate(t, o)
	if _, present := o.Value["total_cost_minor_units"]; present {
		t.Fatal("total_cost_minor_units must not be present when nothing is priced")
	}
	if o.Value["total_omitted_reason"] != "no_priced_models" {
		t.Fatalf("total_omitted_reason = %v, want no_priced_models", o.Value["total_omitted_reason"])
	}
	if !o.IsPartial {
		t.Fatal("IsPartial = false, want true")
	}
}

func TestKeysObservation_CapsSample(t *testing.T) {
	rows := make([]KeyUsageRow, 0, observationKeySampleLimit+5)
	for i := 0; i < observationKeySampleLimit+5; i++ {
		rows = append(rows, KeyUsageRow{APIKeyHash: "hash", RequestCount: int64(i)})
	}
	page := KeyUsagePage{
		Snapshot:      Snapshot{ObservedAt: obsObserved, Watermark: "wm", Instance: FileInstance},
		BusinessDay:   "2026-08-31",
		Rows:          rows,
		TotalKeyCount: int64(len(rows)),
	}
	o := KeysObservation(obsNow, "cpa-file", "production", page)
	mustValidate(t, o)
	if o.Value["key_count"] != int64(len(rows)) {
		t.Fatalf("key_count = %v, want %d", o.Value["key_count"], len(rows))
	}
	sample, ok := o.Value["top_keys"].([]map[string]any)
	if !ok || len(sample) != observationKeySampleLimit {
		t.Fatalf("top_keys len = %v, want %d", o.Value["top_keys"], observationKeySampleLimit)
	}
	if o.Value["truncated"] != true {
		t.Fatal("truncated = false, want true (more keys than the sample cap)")
	}
	if !o.IsPartial {
		t.Fatal("IsPartial = false, want true when truncated")
	}
}

func TestAccountsObservation(t *testing.T) {
	runAt := time.Date(2026, 8, 31, 11, 58, 0, 0, time.UTC)
	health := AccountHealthSummary{
		Snapshot:      Snapshot{ObservedAt: obsObserved, Watermark: "wm", Instance: FileInstance},
		RunID:         "run-new",
		RunAt:         &runAt,
		AccountCount:  2,
		DisabledCount: 0,
		AnomalyCount:  1,
		Anomalies: []AccountAnomaly{
			{AccountKey: "acct-bad", DisplayAccount: "Bad Account", Action: "reauth_required", ActionReason: "token expired"},
		},
	}
	o := AccountsObservation(obsNow, "cpa-file", "production", health)
	mustValidate(t, o)
	if o.Value["run_id"] != "run-new" {
		t.Fatalf("run_id = %v, want run-new", o.Value["run_id"])
	}
	if o.Value["run_at"] != "2026-08-31T11:58:00Z" {
		t.Fatalf("run_at = %v, want the real inspection start time", o.Value["run_at"])
	}
	if o.Value["account_count"] != int64(2) {
		t.Fatalf("account_count = %v, want 2", o.Value["account_count"])
	}
	anomalies, ok := o.Value["anomalies"].([]map[string]any)
	if !ok || len(anomalies) != 1 || anomalies[0]["account_key"] != "acct-bad" {
		t.Fatalf("anomalies = %v, want one entry for acct-bad", o.Value["anomalies"])
	}
}

// TestObservations_NeverObservedIsHonest proves the zero-value case (no
// ObservedAt, e.g. AccountHealth before any inspection run has ever
// completed) never fabricates an observed time — nil ObservedAt is a valid,
// distinct state from a real observation (constitution §12).
func TestObservations_NeverObservedIsHonest(t *testing.T) {
	health := AccountHealthSummary{Snapshot: Snapshot{Watermark: "wm", Instance: FileInstance}}
	o := AccountsObservation(obsNow, "cpa-file", "production", health)
	mustValidate(t, o)
	if o.ObservedAt != nil {
		t.Fatalf("ObservedAt = %v, want nil", o.ObservedAt)
	}
	if o.LastSuccess != nil {
		t.Fatalf("LastSuccess = %v, want nil", o.LastSuccess)
	}
}
