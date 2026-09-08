package application

import (
	"testing"

	"invoice-system/backend/internal/eligibilitywire"
	"invoice-system/backend/internal/postgresstore"
)

// summaryStatusSpace is the status half of the input space, discovered rather
// than read out of the contract field these probes exist to validate. The
// summary's vocabulary is NARROWER than the funding lot's -- it rides
// COALESCE(eas.eligibility_status,'syncing') and gets no freshness override --
// so borrowing the lot's list would feed the decision function two statuses it
// can never see, and borrowing contract.SummaryStatus would make the probe
// agree with itself.
func summaryStatusSpace(t *testing.T) []string {
	t.Helper()
	space, err := eligibilitywire.SummaryStatusInputSpace()
	if err != nil {
		t.Fatal(err)
	}
	return space
}

// summaryInputSpace enumerates every field userEligibilitySummaryReasons
// branches on. As with the funding-lot probe, the expected reason set is
// discovered from the real decision function rather than hand-listed -- the
// incident this slice exists for was precisely a hand-listed copy that nobody
// noticed had gone stale.
func summaryInputSpace(statuses []string) []struct {
	item        postgresstore.EligibilitySummary
	sourceReady bool
} {
	bindings := []string{"pending", "verified", "frozen", "revoked"}
	out := []struct {
		item        postgresstore.EligibilitySummary
		sourceReady bool
	}{}
	for _, status := range statuses {
		for _, binding := range bindings {
			for _, hasOpenFreeze := range []bool{false, true} {
				for _, projectionPending := range []bool{false, true} {
					for _, available := range []int64{0, 30_000} {
						for _, sourceReady := range []bool{false, true} {
							out = append(out, struct {
								item        postgresstore.EligibilitySummary
								sourceReady bool
							}{
								item: postgresstore.EligibilitySummary{
									SourceInstanceID:  "10000000-0000-4000-8000-000000000001",
									BindingStatus:     binding,
									EligibilityStatus: status,
									HasOpenFreeze:     hasOpenFreeze,
									ProjectionPending: projectionPending,
									AvailableMinor:    available,
								},
								sourceReady: sourceReady,
							})
						}
					}
				}
			}
		}
	}
	return out
}

// TestUserEligibilitySummaryReasonSetIsExhaustive pins the summary endpoint's
// reason vocabulary against the wire contract, both directions.
//
// This endpoint is the second, independent place the same drift broke the page:
// even with mapLot fixed, an account in not_invoiceable_pending_reconciliation
// still returns PENDING_RECONCILIATION here, which the frontend's separate
// hand-copied summary list also did not know, and the summary request sits
// beside the orders request in the same load. Fixing one without the other just
// moves which request rejects.
func TestUserEligibilitySummaryReasonSetIsExhaustive(t *testing.T) {
	contract, err := eligibilitywire.Load()
	if err != nil {
		t.Fatal(err)
	}
	emitted := map[string]bool{}
	maxCount := 0
	for _, input := range summaryInputSpace(summaryStatusSpace(t)) {
		reasons, available := userEligibilitySummaryReasons(input.item, input.sourceReady)
		if len(reasons) == 0 {
			t.Fatalf("status %q binding %q produced no reasons at all",
				input.item.EligibilityStatus, input.item.BindingStatus)
		}
		if len(reasons) > maxCount {
			maxCount = len(reasons)
		}
		seen := map[string]bool{}
		for _, reason := range reasons {
			if seen[reason] {
				t.Fatalf("status %q repeated reason %q; the frontend rejects duplicate reasons",
					input.item.EligibilityStatus, reason)
			}
			seen[reason] = true
			emitted[reason] = true
		}
		// READY is the only reason that may accompany a disclosed amount, and
		// any blocking reason must zero it. The frontend's degraded path leans
		// on this: an unrecognised status can never show money as invoiceable.
		if available != 0 && !seen["READY"] {
			t.Fatalf("status %q disclosed %d minor alongside blocking reasons %v",
				input.item.EligibilityStatus, available, reasons)
		}
	}

	extra, missing := eligibilitywire.Diff(emitted, eligibilitywire.Set(contract.SummaryReason))
	if len(extra) > 0 || len(missing) > 0 {
		t.Fatalf("summary_reason drifted from userEligibilitySummaryReasons:\n"+
			"  emitted but not in the contract: %v\n"+
			"  in the contract but never emitted: %v\n"+
			"update contracts/invoice-eligibility-wire.v1.json, regenerate the frontend module\n"+
			"(cd backend && go test ./internal/eligibilitywire/... -update) and add a Chinese label\n"+
			"for any new reason in web/src/App.tsx before shipping.",
			extra, missing)
	}
	if maxCount != contract.SummaryReasonMaxCount {
		t.Fatalf("summary_reason_max_count is %d but the emitter can produce %d reasons at once; "+
			"the frontend uses this as its response length cap",
			contract.SummaryReasonMaxCount, maxCount)
	}
}

// TestUserEligibilitySummaryPendingReconciliationIsNotAccountFrozen keeps the
// two states distinguishable at the source. They mean different things to the
// user -- one clears itself, the other waits on a human -- and the Chinese
// labels the frontend shows for them say so. If the backend ever collapsed
// them, the frontend would keep reassuring people that a manual freeze will
// resolve on its own.
func TestUserEligibilitySummaryPendingReconciliationIsNotAccountFrozen(t *testing.T) {
	pending := postgresstore.EligibilitySummary{
		BindingStatus:     "verified",
		EligibilityStatus: "not_invoiceable_pending_reconciliation",
	}
	reasons, available := userEligibilitySummaryReasons(pending, true)
	if !contains(reasons, "PENDING_RECONCILIATION") {
		t.Fatalf("expected PENDING_RECONCILIATION, got %v", reasons)
	}
	if contains(reasons, "ACCOUNT_FROZEN") {
		t.Fatalf("pending reconciliation must not report as a freeze: %v", reasons)
	}
	if available != 0 {
		t.Fatalf("a pending-reconciliation account must disclose 0, got %d", available)
	}

	frozen := postgresstore.EligibilitySummary{BindingStatus: "verified", EligibilityStatus: "frozen"}
	reasons, _ = userEligibilitySummaryReasons(frozen, true)
	if !contains(reasons, "ACCOUNT_FROZEN") || contains(reasons, "PENDING_RECONCILIATION") {
		t.Fatalf("a frozen account must report only ACCOUNT_FROZEN, got %v", reasons)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
