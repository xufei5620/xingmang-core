package service

import (
	"context"
	"errors"
	"sort"
	"time"

	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/httpapi"
	"invoice-system/backend/internal/postgresstore"
)

// GateStatus describes observed execution, never an inferred healthy dependency.
type GateStatus string

const (
	GateReady        GateStatus = "ready"
	GateNotReady     GateStatus = "not_ready"
	GateNotEvaluated GateStatus = "not_evaluated"
)

type GateReport struct {
	Status GateStatus `json:"status"`
}

type SourceFreshnessReport struct {
	Status  GateStatus `json:"status"`
	Reasons []string   `json:"reasons"`
}

const (
	sourceHeartbeatExpired = "source_heartbeat_expired"
	sourceWatermarkExpired = "source_watermark_expired"
	sourceHeartbeatMissing = "source_heartbeat_missing"
	sourceWatermarkMissing = "source_watermark_missing"
	sourceHeartbeatFuture  = "source_heartbeat_future"
	sourceWatermarkFuture  = "source_watermark_future"
)

var readinessGates = [...]string{
	readinessCheckDatabase, readinessCheckAdminSettings, readinessCheckInvoiceIssuer,
	readinessCheckClamAVDaemon, readinessCheckClamAVSignatures, readinessCheckPDFScanner,
	readinessCheckSourceHealthQuery, readinessCheckSourceIngest, readinessCheckEligibilityQuery,
	readinessCheckEligibility, readinessCheckSourceStreams,
}

func unevaluatedReadinessReport() ReadinessReport {
	report := ReadinessReport{
		Checks:                   make(map[string]GateReport, len(readinessGates)),
		SourceNonFreshnessStatus: GateNotEvaluated,
		SourceFreshness:          SourceFreshnessReport{Status: GateNotEvaluated, Reasons: []string{}},
	}
	for _, gate := range readinessGates {
		report.Checks[gate] = GateReport{Status: GateNotEvaluated}
	}
	return report
}

func validGateStatus(status GateStatus) bool {
	return status == GateReady || status == GateNotReady || status == GateNotEvaluated
}

func validFreshnessReason(reason string) bool {
	switch reason {
	case sourceHeartbeatExpired, sourceWatermarkExpired, sourceHeartbeatMissing, sourceWatermarkMissing, sourceHeartbeatFuture, sourceWatermarkFuture:
		return true
	}
	return false
}

// Publication is closed over names and values. An incomplete or invalid internal
// report provides no evidence, and cannot publish a host, identifier or error.
func (r ReadinessReport) filteredDiagnostics() ReadinessReport {
	out := unevaluatedReadinessReport()
	if len(r.Checks) != len(readinessGates) || !validGateStatus(r.SourceNonFreshnessStatus) || !validGateStatus(r.SourceFreshness.Status) {
		return out
	}
	for _, gate := range readinessGates {
		state, ok := r.Checks[gate]
		if !ok || !validGateStatus(state.Status) {
			return unevaluatedReadinessReport()
		}
		out.Checks[gate] = state
	}
	if r.SourceFreshness.Status != GateNotReady && len(r.SourceFreshness.Reasons) != 0 {
		return unevaluatedReadinessReport()
	}
	seen := make(map[string]bool)
	for _, reason := range r.SourceFreshness.Reasons {
		if !validFreshnessReason(reason) || seen[reason] {
			return unevaluatedReadinessReport()
		}
		seen[reason] = true
		out.SourceFreshness.Reasons = append(out.SourceFreshness.Reasons, reason)
	}
	out.SourceNonFreshnessStatus, out.SourceFreshness.Status = r.SourceNonFreshnessStatus, r.SourceFreshness.Status
	sort.Strings(out.SourceFreshness.Reasons)
	return out
}

// The original probe remains the sole eleven-gate decision. Capture the health
// it already read and classify its first failure; no dependency is queried twice
// and no later dependency executes after a failed check.
func (p readinessProbe) evaluateWithReport(ctx context.Context) (httpapi.ReadinessOutcome, ReadinessReport, error) {
	report := unevaluatedReadinessReport()
	var source postgresstore.SourceReadinessHealth
	var observedAt time.Time
	readSource, readClock := p.sourceHealth, p.now
	p.sourceHealth = func(ctx context.Context) (postgresstore.SourceReadinessHealth, error) {
		var err error
		source, err = readSource(ctx)
		return source, err
	}
	p.now = func() time.Time { observedAt = readClock(); return observedAt }
	outcome, err := p.evaluate(ctx)
	failed := len(readinessGates)
	if err != nil {
		var named *httpapi.ReadinessCheckError
		if !errors.As(err, &named) {
			return outcome, report, err
		}
		gate := named.Check
		switch gate {
		case readinessCheckSourceIngestDead:
			gate = readinessCheckSourceIngest
		case readinessCheckEligibilityDead, readinessCheckEligibilityStuck:
			gate = readinessCheckEligibility
		case readinessCheckSourceStreamDead:
			gate = readinessCheckSourceStreams
		}
		failed = -1
		for i, name := range readinessGates {
			if name == gate {
				failed = i
				break
			}
		}
		if failed < 0 {
			return outcome, report, err
		}
	}
	for i, gate := range readinessGates {
		if i < failed {
			report.Checks[gate] = GateReport{Status: GateReady}
		}
		if i == failed {
			report.Checks[gate] = GateReport{Status: GateNotReady}
		}
	}
	if failed >= len(readinessGates)-1 {
		policy := p.sourcePolicy
		policy.Now = observedAt
		report.SourceNonFreshnessStatus, report.SourceFreshness = sourceReadinessDiagnostics(source.Report, policy)
	}
	return outcome, report, err
}

// sourceReadinessDiagnostics validates the original evidence before removing
// any expiry reason. The normalized copy is checked by the original validator,
// so all enabled instances, all five streams and both source types must pass.
// Only the two actual age expiries can be removed; future or absent timestamps
// retain their failure and remain distinguishable in the public closed set.
func sourceReadinessDiagnostics(report postgresstore.SourceHealthReport, policy postgresstore.SourceFreshnessPolicy) (GateStatus, SourceFreshnessReport) {
	freshness := SourceFreshnessReport{Status: GateNotReady, Reasons: []string{}}
	if policy.Now.IsZero() || policy.EconomicHeartbeatMaxAge <= 0 || policy.EconomicWatermarkMaxAge <= 0 || policy.IdentitiesMaxAge <= 0 {
		return GateNotReady, freshness
	}
	copyReport := postgresstore.SourceHealthReport{Ready: true, Items: make([]postgresstore.SourceStreamHealth, 0, len(report.Items))}
	originalReady, enabledCount := true, 0
	enabledTypes := make(map[domain.SourceType]bool)
	expiryReasons := make(map[string]bool)
	coherent := true
	for _, item := range report.Items {
		if !item.SourceEnabled {
			copyReport.Items = append(copyReport.Items, item)
			continue
		}
		enabledCount++
		enabledTypes[item.SourceType] = true
		originalReady = originalReady && item.Ready
		if !sourceEvidenceCoherent(item, policy) {
			coherent = false
		}
		heartbeatMaxAge := policy.EconomicHeartbeatMaxAge
		if item.StreamID == "identities" {
			heartbeatMaxAge = policy.IdentitiesMaxAge
		}
		heartbeat := classifySourceTime(item.LastAcceptedAt, heartbeatMaxAge, policy.Now, sourceHeartbeatMissing, sourceHeartbeatFuture, sourceHeartbeatExpired)
		watermark := ""
		if item.StreamID != "identities" {
			watermark = classifySourceTime(item.EconomicWatermarkAt, policy.EconomicWatermarkMaxAge, policy.Now, sourceWatermarkMissing, sourceWatermarkFuture, sourceWatermarkExpired)
		}
		if item.MaximumAgeSeconds <= 0 || (item.StreamID != "identities" && item.EconomicWatermarkMaximumAgeSeconds <= 0) {
			coherent = false
		}
		if heartbeat != "" {
			expiryReasons[heartbeat] = true
		}
		if watermark != "" {
			expiryReasons[watermark] = true
		}
		normalized := make([]string, 0, len(item.Reasons))
		for _, reason := range item.Reasons {
			if reason == "STREAM_STALE" && heartbeat == sourceHeartbeatExpired {
				continue
			}
			if reason == "ECONOMIC_WATERMARK_STALE" && watermark == sourceWatermarkExpired {
				continue
			}
			normalized = append(normalized, reason)
		}
		item.Reasons, item.Ready = normalized, true
		for _, reason := range normalized {
			if !readySourceStreamAllowedReasons[reason] {
				item.Ready = false
			}
		}
		copyReport.Ready = copyReport.Ready && item.Ready
		copyReport.Items = append(copyReport.Items, item)
	}
	if !enabledTypes[domain.SourceSub2API] || !enabledTypes[domain.SourceNewAPI] {
		originalReady, copyReport.Ready = false, false
	}
	if report.Ready != originalReady || enabledCount == 0 {
		coherent = false
	}
	for reason := range expiryReasons {
		freshness.Reasons = append(freshness.Reasons, reason)
	}
	sort.Strings(freshness.Reasons)
	if !coherent {
		return GateNotReady, freshness
	}
	if len(freshness.Reasons) == 0 {
		freshness.Status = GateReady
	}
	if validateSourceRuntimeReadiness(copyReport) != nil {
		return GateNotReady, freshness
	}
	return GateReady, freshness
}

func classifySourceTime(at time.Time, maximumAge time.Duration, now time.Time, missing, future, expired string) string {
	if at.IsZero() {
		return missing
	}
	if at.After(now.Add(5 * time.Minute)) {
		return future
	}
	if maximumAge > 0 && now.Sub(at) > maximumAge {
		return expired
	}
	return ""
}

func sourceEvidenceCoherent(item postgresstore.SourceStreamHealth, policy postgresstore.SourceFreshnessPolicy) bool {
	if item.PendingEvents < 0 || item.DeadEvents < 0 || item.ContainedDeadEvents < 0 || item.ContainedDeadEvents > item.DeadEvents {
		return false
	}
	// Reuse the real producer instead of maintaining another definition of
	// blocked/version/count/freshness/rescan reasons. This also rejects a forged
	// Ready flag before expiry normalization could accidentally conceal it.
	expected := item
	postgresstore.EvaluateSourceStreamHealth(&expected, policy)
	if item.Ready != expected.Ready || item.MaximumAgeSeconds != expected.MaximumAgeSeconds || item.EconomicWatermarkMaximumAgeSeconds != expected.EconomicWatermarkMaximumAgeSeconds || len(item.Reasons) != len(expected.Reasons) {
		return false
	}
	seen := make(map[string]bool, len(item.Reasons))
	for _, reason := range item.Reasons {
		if seen[reason] {
			return false
		}
		seen[reason] = true
	}
	for _, reason := range expected.Reasons {
		if !seen[reason] {
			return false
		}
	}
	return true
}
