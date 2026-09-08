package httpapi

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"invoice-system/backend/internal/adminsettings"
	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/eligibilitywire"
)

// lotInputSpace is the cartesian product of every FundingLot field the reason
// chain in userFundingLotDTOs actually branches on. Enumerating inputs and
// collecting the outputs is the whole point: a hand-written table of expected
// reason codes would be a fourth copy of the same list, and adding a branch to
// the chain would leave it green.
func lotInputSpace(statuses []string) []domain.FundingLot {
	kinds := []domain.EligibilityKind{
		"", // the in-memory bootstrap fixture's empty kind
		domain.EligibilityLegacyNonInvoiceable,
		domain.EligibilityWalletCash,
		domain.EligibilitySubscriptionCash,
		domain.EligibilityNonCash,
	}
	verifications := []domain.VerificationState{
		domain.VerificationPending,
		domain.VerificationVerified,
		domain.VerificationFrozen,
	}
	completions := []time.Time{
		{}, // zero: never completed
		adminsettings.RequiredEligibilityStartAt.Add(-24 * time.Hour),
		adminsettings.RequiredEligibilityStartAt,
		adminsettings.RequiredEligibilityStartAt.Add(24 * time.Hour),
	}
	consumptions := []int64{0, 30_000}

	lots := []domain.FundingLot{}
	for _, status := range statuses {
		for _, kind := range kinds {
			for _, verification := range verifications {
				for _, completedAt := range completions {
					for _, consumed := range consumptions {
						for _, refundFrozen := range []bool{false, true} {
							lots = append(lots, domain.FundingLot{
								ID:                fmt.Sprintf("lot-%d", len(lots)),
								SourceType:        domain.SourceSub2API,
								EligibilityStatus: status,
								EligibilityKind:   kind,
								Verification:      verification,
								CompletedAt:       completedAt,
								ConsumedCashMinor: consumed,
								RefundFrozen:      refundFrozen,
							})
						}
					}
				}
			}
		}
	}
	return lots
}

// TestUserFundingLotReasonCodeSetIsExhaustive is the "提交时" half of
// XM-INV-LOT-REASON-CONTRACT: it runs the real emitter over every input shape
// that can change its answer, collects the (eligibility_status, reason_code)
// pairs that actually come out, and compares them against the wire contract in
// both directions.
//
// Both directions matter. "The emitter produced a code the contract does not
// declare" is the drift that broke production (LEDGER_PENDING_RECONCILIATION
// shipped in the backend while three frontend copies of the list stayed
// behind). "The contract declares a code no emitter can produce" is the
// opposite drift, and a gate that ignores it can only ever grow -- exactly the
// failure mode of an exemption list that never shrinks.
func TestUserFundingLotReasonCodeSetIsExhaustive(t *testing.T) {
	contract, err := eligibilitywire.Load()
	if err != nil {
		t.Fatal(err)
	}
	emitted := map[string]bool{}
	emittedStatuses := map[string]bool{}
	pairs := map[string]map[string]bool{}
	for _, dto := range userFundingLotDTOs(lotInputSpace(contract.LotEligibilityStatus)) {
		emittedStatuses[dto.EligibilityStatus] = true
		if pairs[dto.EligibilityStatus] == nil {
			pairs[dto.EligibilityStatus] = map[string]bool{}
		}
		pairs[dto.EligibilityStatus][dto.ReasonCode] = true
		if dto.ReasonCode != "" {
			emitted[dto.ReasonCode] = true
		}
	}

	extra, missing := eligibilitywire.Diff(emitted, eligibilitywire.Set(contract.LotReasonCode))
	if len(extra) > 0 || len(missing) > 0 {
		t.Fatalf("lot_reason_code drifted from userFundingLotDTOs:\n"+
			"  emitted but not in the contract: %v\n"+
			"  in the contract but never emitted: %v\n"+
			"update contracts/invoice-eligibility-wire.v1.json, regenerate the frontend module\n"+
			"(cd backend && go test ./internal/eligibilitywire/... -update) and add a Chinese label\n"+
			"for any new code in web/src/App.tsx before shipping.",
			extra, missing)
	}

	extra, missing = eligibilitywire.Diff(emittedStatuses, eligibilitywire.Set(contract.LotEligibilityStatus))
	if len(extra) > 0 || len(missing) > 0 {
		t.Fatalf("lot_eligibility_status drifted from userFundingLotDTOs:\n  extra: %v\n  missing: %v", extra, missing)
	}

	for status, want := range contract.LotStatusReasonPairs {
		got := pairs[status]
		if got == nil {
			t.Fatalf("contract declares reason pairings for status %q but the emitter never produced that status", status)
		}
		extra, missing := eligibilitywire.Diff(got, eligibilitywire.Set(want))
		if len(extra) > 0 || len(missing) > 0 {
			t.Fatalf("lot_status_reason_pairs[%q] drifted:\n  emitted but not declared: %v\n  declared but never emitted: %v",
				status, quoteEmpty(extra), quoteEmpty(missing))
		}
	}
	for status := range pairs {
		if _, ok := contract.LotStatusReasonPairs[status]; !ok {
			t.Fatalf("the emitter produced status %q with no declared reason pairings in the contract", status)
		}
	}
}

// TestUserFundingLotPendingReconciliationCarriesFiveDistinctReasons is the
// machine-checked form of the finding that decided this slice's shape: a lot on
// an account in not_invoiceable_pending_reconciliation does NOT necessarily
// carry LEDGER_PENDING_RECONCILIATION. Four other reasons outrank it in the
// chain, while eligibility_status stays pinned to the new value for all of
// them. Widening only the frontend's reason_code list -- the obvious reading of
// the incident -- would therefore have left the page just as broken, and the
// person who tried it would have believed they had fixed it.
func TestUserFundingLotPendingReconciliationCarriesFiveDistinctReasons(t *testing.T) {
	const status = "not_invoiceable_pending_reconciliation"
	seen := map[string]bool{}
	for _, dto := range userFundingLotDTOs(lotInputSpace([]string{status})) {
		if dto.EligibilityStatus != status {
			t.Fatalf("the emitter rewrote eligibility_status to %q", dto.EligibilityStatus)
		}
		seen[dto.ReasonCode] = true
	}
	want := []string{
		"BEFORE_ELIGIBILITY_START",
		"LEDGER_PENDING_RECONCILIATION",
		"NO_POST_START_CONSUMPTION",
		"SOURCE_REFUND",
		"SUBSCRIPTION_USAGE_UNSUPPORTED",
	}
	extra, missing := eligibilitywire.Diff(seen, eligibilitywire.Set(want))
	if len(extra) > 0 || len(missing) > 0 {
		t.Fatalf("reasons reachable under %s changed:\n  extra: %v\n  missing: %v", status, extra, missing)
	}
	if seen[""] {
		t.Fatal("a non-active status must always carry a reason code")
	}
}

// TestUserFundingLotNonActiveStatusAlwaysCarriesAReason pins the invariant the
// frontend's degraded path relies on: any status other than active resolves to
// some reason code, so an unknown future status still renders with an
// explanation rather than a blank badge.
func TestUserFundingLotNonActiveStatusAlwaysCarriesAReason(t *testing.T) {
	contract, err := eligibilitywire.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, dto := range userFundingLotDTOs(lotInputSpace(contract.LotEligibilityStatus)) {
		if dto.EligibilityStatus != "active" && dto.ReasonCode == "" {
			t.Fatalf("lot %s: status %q carried no reason code", dto.ID, dto.EligibilityStatus)
		}
	}
}

func quoteEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			out = append(out, "(no reason_code key)")
			continue
		}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
