package finance

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance/gen"
)

func TestCurrentHistoryMatchesRejectsOrphanOrMismatchedEvidence(t *testing.T) {
	current := gen.FinanceRunwayThresholdConfig{
		Environment: "development", CriticalDays: 5, WarningDays: 10,
		SeriousDays: 20, Revision: 7,
	}
	matching := gen.FinanceRunwayThresholdHistory{
		Environment: "development", CriticalDays: 5, WarningDays: 10,
		SeriousDays: 20, Revision: 7,
		ChangedAt: pgtype.Timestamptz{Valid: true},
	}
	if !currentHistoryMatches(current, matching) {
		t.Fatal("matching current/history rows should satisfy the invariant")
	}

	cases := []struct {
		name   string
		mutate func(*gen.FinanceRunwayThresholdHistory)
	}{
		{name: "different environment", mutate: func(h *gen.FinanceRunwayThresholdHistory) { h.Environment = "staging" }},
		{name: "different revision", mutate: func(h *gen.FinanceRunwayThresholdHistory) { h.Revision = 6 }},
		{name: "different critical", mutate: func(h *gen.FinanceRunwayThresholdHistory) { h.CriticalDays = 4 }},
		{name: "different warning", mutate: func(h *gen.FinanceRunwayThresholdHistory) { h.WarningDays = 9 }},
		{name: "different serious", mutate: func(h *gen.FinanceRunwayThresholdHistory) { h.SeriousDays = 21 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			history := matching
			tc.mutate(&history)
			if currentHistoryMatches(current, history) {
				t.Fatalf("mismatched %s row was accepted", tc.name)
			}
		})
	}
}
