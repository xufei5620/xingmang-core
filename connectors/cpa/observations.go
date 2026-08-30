package cpa

// Successful-read → ops.Observation conversion (XM-CPA0).
//
// Unlike sub2api/finance's single collector call producing every metric
// atomically, CPA's three ReadClient methods (UsageSummary/KeyUsage/
// AccountHealth) are independent SQL queries against the same file: one can
// fail while the others succeed (e.g. codex_inspection_* tables missing from
// an older usage.sqlite while usage_events is fine). So this package only
// converts a **successful** result into its observation(s); deciding which
// metric failed and building its failure observation (preserving the prior
// good value) is internal/platform/jobs.CPASyncWorker's job, the same
// failureObservation shape sub2api_sync/cost_sync use, applied per-metric
// instead of all-at-once.

import (
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// observationKeySampleLimit caps the "top_keys" sample carried inside the
// cpa.keys.usage observation. This is a small, periodic freshness signal —
// the full per-key breakdown is read live from GET /api/v1/platforms/cpa/keys,
// not from this capped sample (see contract.go's MetricKeysUsage doc comment).
const observationKeySampleLimit = 20

// RequestsObservation converts a successful UsageSummary into the
// cpa.requests.daily observation.
func RequestsObservation(now time.Time, source, environment string, usage UsageSummary) ops.Observation {
	byProvider := map[string]int64{}
	byModel := map[string]int64{}
	for _, row := range usage.Rows {
		byProvider[nonEmpty(row.Provider, "(未知 provider)")] += row.RequestCount
		byModel[unpricedModelLabel(row)] += row.RequestCount
	}
	return ops.Observation{
		MetricKey:                 MetricRequestsDaily,
		Source:                    source,
		Environment:               environment,
		ObservedAt:                timePtr(usage.ObservedAt),
		SyncedAt:                  now.UTC(),
		Watermark:                 usage.Watermark,
		Status:                    ops.SyncOK,
		IsPartial:                 usage.IsPartial,
		LastSuccess:               timePtr(usage.ObservedAt),
		StalenessThresholdSeconds: RequestsStalenessThresholdSeconds,
		Value: map[string]any{
			"business_day":        usage.BusinessDay,
			"total_request_count": usage.TotalRequestCount,
			"by_provider":         byProvider,
			"by_model":            byModel,
		},
	}
}

// CostObservation converts a successful UsageSummary into the
// cpa.cost.daily observation.
func CostObservation(now time.Time, source, environment string, usage UsageSummary) ops.Observation {
	value := map[string]any{
		"business_day":           usage.BusinessDay,
		"scale":                  6, // money.MicroScale — documentation copy, see finance/observations.go precedent
		"unpriced_request_count": usage.UnpricedRequestCount,
		"unpriced_models":        usage.UnpricedModels,
	}
	if usage.TotalCostMicros != nil {
		value["total_cost_minor_units"] = *usage.TotalCostMicros
		value["currency"] = usage.Currency
	} else {
		// No fabricated 0: every row was unpriced, so there is nothing to sum
		// (constitution §12). Mirrors finance/observations.go's
		// total_omitted_reason escape hatch for a mixed-currency total.
		value["total_omitted_reason"] = "no_priced_models"
	}
	return ops.Observation{
		MetricKey:                 MetricCostDaily,
		Source:                    source,
		Environment:               environment,
		ObservedAt:                timePtr(usage.ObservedAt),
		SyncedAt:                  now.UTC(),
		Watermark:                 usage.Watermark,
		Status:                    ops.SyncOK,
		IsPartial:                 usage.IsPartial,
		LastSuccess:               timePtr(usage.ObservedAt),
		StalenessThresholdSeconds: CostStalenessThresholdSeconds,
		Value:                     value,
	}
}

// KeysObservation converts a successful KeyUsagePage into the
// cpa.keys.usage observation: key_count plus a capped top-N sample.
func KeysObservation(now time.Time, source, environment string, keys KeyUsagePage) ops.Observation {
	limit := observationKeySampleLimit
	if limit > len(keys.Rows) {
		limit = len(keys.Rows)
	}
	sample := make([]map[string]any, 0, limit)
	for _, row := range keys.Rows[:limit] {
		entry := map[string]any{
			"label":                  keyDisplayLabel(row),
			"request_count":          row.RequestCount,
			"unpriced_request_count": row.UnpricedRequestCount,
		}
		if row.CostMicros != nil {
			entry["cost_minor_units"] = *row.CostMicros
		}
		sample = append(sample, entry)
	}
	truncated := keys.TotalKeyCount > int64(observationKeySampleLimit)
	return ops.Observation{
		MetricKey:                 MetricKeysUsage,
		Source:                    source,
		Environment:               environment,
		ObservedAt:                timePtr(keys.ObservedAt),
		SyncedAt:                  now.UTC(),
		Watermark:                 keys.Watermark,
		IsPartial:                 keys.IsPartial || truncated,
		Status:                    ops.SyncOK,
		LastSuccess:               timePtr(keys.ObservedAt),
		StalenessThresholdSeconds: KeysStalenessThresholdSeconds,
		Value: map[string]any{
			"business_day": keys.BusinessDay,
			"key_count":    keys.TotalKeyCount,
			"top_keys":     sample,
			"truncated":    truncated,
		},
	}
}

// keyDisplayLabel is alias when registered, else a short hash prefix. The
// hash itself is not a credential (constitution §7 concerns api_key_hash's
// *reversal*, not display of the one-way hash) — this only trims it for a
// compact observation sample; the full hash remains available from the live
// GET /api/v1/platforms/cpa/keys query.
func keyDisplayLabel(row KeyUsageRow) string {
	if row.Alias != "" {
		return row.Alias
	}
	if len(row.APIKeyHash) > 12 {
		return row.APIKeyHash[:12] + "…"
	}
	return nonEmpty(row.APIKeyHash, "(无 key 归属)")
}

// AccountsObservation converts a successful AccountHealthSummary into the
// cpa.accounts.health observation. Anomalies are already capped by the
// connector (MaxAnomalies); this function does not cap again.
func AccountsObservation(now time.Time, source, environment string, health AccountHealthSummary) ops.Observation {
	anomalies := make([]map[string]any, 0, len(health.Anomalies))
	for _, a := range health.Anomalies {
		anomalies = append(anomalies, map[string]any{
			"account_key":     a.AccountKey,
			"display_account": a.DisplayAccount,
			"provider":        a.Provider,
			"disabled":        a.Disabled,
			"status":          a.Status,
			"state":           a.State,
			"action":          a.Action,
			"action_reason":   a.ActionReason,
		})
	}
	return ops.Observation{
		MetricKey:                 MetricAccountsHealth,
		Source:                    source,
		Environment:               environment,
		ObservedAt:                timePtr(health.ObservedAt),
		SyncedAt:                  now.UTC(),
		Watermark:                 health.Watermark,
		IsPartial:                 health.IsPartial,
		Status:                    ops.SyncOK,
		LastSuccess:               timePtr(health.ObservedAt),
		StalenessThresholdSeconds: AccountsStalenessThresholdSeconds,
		Value: map[string]any{
			"run_id":         health.RunID,
			"account_count":  health.AccountCount,
			"disabled_count": health.DisabledCount,
			"anomaly_count":  health.AnomalyCount,
			"anomalies":      anomalies,
			"truncated":      health.Truncated,
		},
	}
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	utc := t.UTC()
	return &utc
}

func nonEmpty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
