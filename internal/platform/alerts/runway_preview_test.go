package alerts_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/alerts"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
)

var previewCurrent = finance.RunwayThresholds{CriticalDays: 5, WarningDays: 10, SeriousDays: 20}

func previewRunway(days *int, reason finance.RunwayUnknownReason) finance.UpstreamRunway {
	id := uuid.New()
	return finance.UpstreamRunway{
		AccountID: id, Name: "Relay " + id.String()[:8],
		AccessMethod: finance.AccessUpstreamKey,
		Runway:       finance.Runway{Days: days, Reason: reason, BalanceObservedAt: timePtr(time.Date(2026, 8, 29, 1, 0, 0, 0, time.UTC))},
	}
}

func timePtr(v time.Time) *time.Time { return &v }

func activeR5(id uuid.UUID, severity alerts.Severity) alerts.Alert {
	return alerts.Alert{
		RuleKey:  alerts.RuleUpstreamRunwayLow,
		DedupKey: alerts.RuleUpstreamRunwayLow + ":development:" + id.String(),
		Severity: severity, Status: alerts.StatusOpen,
	}
}

func TestPreviewRunwayThresholdsCoversEveryTransition(t *testing.T) {
	cases := []struct {
		name          string
		days          int
		proposed      finance.RunwayThresholds
		alertSeverity alerts.Severity
		want          alerts.RunwayImpactTransition
	}{
		{name: "open", days: 15, proposed: finance.RunwayThresholds{CriticalDays: 5, WarningDays: 20, SeriousDays: 30}, want: alerts.RunwayWouldOpen},
		{name: "escalate", days: 8, proposed: finance.RunwayThresholds{CriticalDays: 10, WarningDays: 15, SeriousDays: 20}, alertSeverity: alerts.SeverityWarning, want: alerts.RunwayWouldEscalate},
		{name: "deescalate", days: 3, proposed: finance.RunwayThresholds{CriticalDays: 2, WarningDays: 5, SeriousDays: 20}, alertSeverity: alerts.SeverityCritical, want: alerts.RunwayWouldDeescalate},
		{name: "resolve", days: 8, proposed: finance.RunwayThresholds{CriticalDays: 5, WarningDays: 7, SeriousDays: 20}, alertSeverity: alerts.SeverityWarning, want: alerts.RunwayWouldResolve},
		{name: "unchanged-actionable", days: 8, proposed: previewCurrent, alertSeverity: alerts.SeverityWarning, want: alerts.RunwayUnchanged},
		{name: "unchanged-non-actionable", days: 30, proposed: previewCurrent, want: alerts.RunwayUnchanged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runway := previewRunway(&tc.days, "")
			var current []alerts.Alert
			if tc.alertSeverity != "" {
				current = []alerts.Alert{activeR5(runway.AccountID, tc.alertSeverity)}
			}
			preview, err := alerts.PreviewRunwayThresholds(previewCurrent, tc.proposed, []finance.UpstreamRunway{runway}, current)
			if err != nil {
				t.Fatal(err)
			}
			if len(preview.Items) != 1 || preview.Items[0].Transition != tc.want {
				t.Fatalf("transition=%+v want %q", preview.Items, tc.want)
			}
		})
	}
}

func TestPreviewRunwayThresholdsPrioritizesInconsistency(t *testing.T) {
	tests := []struct {
		name       string
		makeRunway func() finance.UpstreamRunway
		alerts     []alerts.Alert
		wantReason string
	}{
		{name: "missing", makeRunway: func() finance.UpstreamRunway { d := 8; return previewRunway(&d, "") }, wantReason: "missing_active_alert"},
		{name: "unexpected", makeRunway: func() finance.UpstreamRunway { d := 30; return previewRunway(&d, "") }, wantReason: "unexpected_active_alert"},
		{name: "unknown", makeRunway: func() finance.UpstreamRunway { return previewRunway(nil, finance.RunwayReasonNoBalance) }, wantReason: "unexpected_active_alert"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runway := tc.makeRunway()
			current := tc.alerts
			if len(current) == 0 {
				current = []alerts.Alert{activeR5(runway.AccountID, alerts.SeverityWarning)}
				if tc.name == "missing" {
					current = nil
				}
			}
			preview, err := alerts.PreviewRunwayThresholds(previewCurrent, previewCurrent, []finance.UpstreamRunway{runway}, current)
			if err != nil {
				t.Fatal(err)
			}
			if len(preview.Items) != 1 || preview.Items[0].Transition != alerts.RunwayCurrentInconsistent || preview.Items[0].ConsistencyReason != tc.wantReason {
				t.Fatalf("item=%+v want reason %s", preview.Items, tc.wantReason)
			}
		})
	}
}

func TestPreviewRunwayThresholdsDetectsSeverityAndDuplicate(t *testing.T) {
	runway := previewRunway(intPtr(8), "")
	first := activeR5(runway.AccountID, alerts.SeverityCritical)
	second := activeR5(runway.AccountID, alerts.SeverityWarning)
	preview, err := alerts.PreviewRunwayThresholds(previewCurrent, previewCurrent, []finance.UpstreamRunway{runway}, []alerts.Alert{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Items) != 1 || preview.Items[0].ConsistencyReason != "duplicate_active_alert" || preview.Counts.CurrentInconsistent != 1 {
		t.Fatalf("duplicate should win: %+v", preview)
	}

	preview, err = alerts.PreviewRunwayThresholds(previewCurrent, previewCurrent, []finance.UpstreamRunway{runway}, []alerts.Alert{first})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Items[0].ConsistencyReason != "severity_mismatch" {
		t.Fatalf("severity mismatch not detected: %+v", preview.Items[0])
	}
}

func TestPreviewRunwayThresholdsExcludesSubscriptionsFromCoverage(t *testing.T) {
	sub := previewRunway(nil, finance.RunwayReasonNotApplicable)
	sub.AccessMethod = finance.AccessSubscriptionAccount
	preview, err := alerts.PreviewRunwayThresholds(previewCurrent, previewCurrent, []finance.UpstreamRunway{sub}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Coverage.Total != 0 || preview.Coverage.Known != 0 || len(preview.Coverage.UnknownReasons) != 0 {
		t.Fatalf("subscription coverage should be excluded: %+v", preview.Coverage)
	}
	if len(preview.Items) != 0 {
		t.Fatalf("subscription should not produce impact row: %+v", preview.Items)
	}
}

func TestPreviewRunwayThresholdsFlagsUnexpectedSubscriptionAlert(t *testing.T) {
	sub := previewRunway(nil, finance.RunwayReasonNotApplicable)
	sub.AccessMethod = finance.AccessSubscriptionAccount
	preview, err := alerts.PreviewRunwayThresholds(previewCurrent, previewCurrent, []finance.UpstreamRunway{sub}, []alerts.Alert{activeR5(sub.AccountID, alerts.SeverityWarning)})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Items) != 1 || preview.Items[0].ConsistencyReason != "unexpected_active_alert" || preview.Counts.CurrentInconsistent != 1 {
		t.Fatalf("subscription active alert must be inconsistent: %+v", preview)
	}
}

func TestPreviewRunwayThresholdsExcludesAllNonMeteredMethods(t *testing.T) {
	official := previewRunway(nil, finance.RunwayReasonNoBalance)
	official.AccessMethod = finance.AccessOfficialAPI
	future := previewRunway(nil, finance.RunwayReasonCurrencyMismatch)
	future.AccessMethod = finance.AccessMethod("future_non_metered")
	preview, err := alerts.PreviewRunwayThresholds(previewCurrent, previewCurrent, []finance.UpstreamRunway{official, future}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Coverage.Total != 0 || len(preview.Coverage.UnknownReasons) != 0 || len(preview.Items) != 0 {
		t.Fatalf("所有非计量型都不应进入 runway 影响或覆盖率: %+v", preview)
	}
}

func intPtr(v int) *int { return &v }
