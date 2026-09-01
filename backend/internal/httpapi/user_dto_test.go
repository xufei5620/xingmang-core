package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"invoice-system/backend/internal/domain"
)

func TestUserInvoiceRequestDTOHidesIssuerCode(t *testing.T) {
	body, err := json.Marshal(userRequestDTOs([]domain.InvoiceRequest{{
		ID:          "request-1",
		IssuerCode:  "issuer-revision-secret",
		ServiceItem: domain.FixedServiceItem,
	}}))
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(body)
	if strings.Contains(encoded, "issuer_code") || strings.Contains(encoded, "issuer-revision-secret") {
		t.Fatalf("user response leaked issuer routing data: %s", encoded)
	}
	if !strings.Contains(encoded, `"service_item":"技术服务"`) {
		t.Fatalf("user response lost the public service item: %s", encoded)
	}
}

// TestUserFundingLotSourceUnavailableAlwaysMapsToSourceNotReady pins the web
// client's invariant: eligibility_status=source_unavailable MUST pair with
// reason_code=SOURCE_NOT_READY, for every lot shape. The RC58 production
// canary hit the counterexample -- a verified, zero-consumption wallet lot
// under a stale source got NO_POST_START_CONSUMPTION and the client refused
// to render the entire list.
func TestUserFundingLotSourceUnavailableAlwaysMapsToSourceNotReady(t *testing.T) {
	lots := []domain.FundingLot{
		{ // the production counterexample: fresh verified lot, nothing consumed
			ID: "lot-1", EligibilityKind: domain.EligibilityWalletCash,
			Verification: domain.VerificationVerified, ConsumedCashMinor: 0,
			EligibilityStatus: "source_unavailable",
		},
		{ // refund-frozen under a stale source: freshness still outranks
			ID: "lot-2", EligibilityKind: domain.EligibilityWalletCash,
			Verification: domain.VerificationVerified, RefundFrozen: true,
			EligibilityStatus: "source_unavailable",
		},
		{ // subscription lot under a stale source
			ID: "lot-3", EligibilityKind: domain.EligibilitySubscriptionCash,
			Verification: domain.VerificationVerified,
			EligibilityStatus: "source_unavailable",
		},
	}
	for _, dto := range userFundingLotDTOs(lots) {
		if dto.ReasonCode != "SOURCE_NOT_READY" {
			t.Fatalf("lot %s: source_unavailable must map to SOURCE_NOT_READY, got %q", dto.ID, dto.ReasonCode)
		}
	}
	// And a healthy zero-consumption lot keeps its consumption reason.
	healthy := userFundingLotDTOs([]domain.FundingLot{{
		ID: "lot-4", EligibilityKind: domain.EligibilityWalletCash,
		Verification: domain.VerificationVerified, ConsumedCashMinor: 0,
		EligibilityStatus: "active",
	}})
	if healthy[0].ReasonCode != "NO_POST_START_CONSUMPTION" {
		t.Fatalf("active zero-consumption lot must keep NO_POST_START_CONSUMPTION, got %q", healthy[0].ReasonCode)
	}
}
