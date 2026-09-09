package domain

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestFundingLotValidateRejectsOverflowedAllocationSum(t *testing.T) {
	lot := FundingLot{
		ID: "lot", PrincipalID: "user", SourceInstanceID: "source",
		SourceType: SourceSub2API, ExternalOrderID: "order", Currency: CurrencyCNY,
		OriginalMinor: math.MaxInt64, CurrentCapMinor: math.MaxInt64,
		ReservedMinor: math.MaxInt64, IssuedMinor: 1,
	}
	if err := lot.Validate(); err == nil {
		t.Fatal("overflowing reserved+issued allocation was accepted")
	}
}

func TestInvoiceProfileValidationRejectsHeaderAndDisplayNameInputs(t *testing.T) {
	base := InvoiceProfile{
		PrincipalID: "user", Type: ProfilePersonal, Title: "个人",
		Email: "billing@example.com", EmailVerified: true,
	}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	display := base
	display.Email = "Billing <billing@example.com>"
	if err := display.Validate(); err == nil {
		t.Fatal("display-name email was accepted")
	}
	injected := base
	injected.Title = "title\r\nX-Forged: true"
	if err := injected.Validate(); err == nil {
		t.Fatal("CRLF profile field was accepted")
	}
	oversized := base
	oversized.Address = strings.Repeat("a", 501)
	if err := oversized.Validate(); err == nil {
		t.Fatal("oversized profile field was accepted")
	}
}

// CR-0007 problem three: the four eligibility-resolution sentinels must each
// still satisfy errors.Is against the generic sentinel they replaced (so any
// caller checking only the generic one keeps working unchanged), while also
// being distinguishable from each other and from the generic sentinel of the
// *other* family (a stale-source outcome must never also read as a conflict).
func TestEligibilityResolutionSentinelsWrapGenericOnesAndStayDistinct(t *testing.T) {
	if !errors.Is(ErrEligibilitySourceStale, ErrSourceUnavailable) {
		t.Fatal("ErrEligibilitySourceStale no longer satisfies errors.Is(err, ErrSourceUnavailable)")
	}
	invalidStateSpecific := []error{ErrEligibilityProjectionPending, ErrEligibilityRefundExposed,
		ErrEligibilityEvaluationUnmatched, ErrEligibilityDeadEventUnrepaired}
	for _, specific := range invalidStateSpecific {
		if !errors.Is(specific, ErrInvalidState) {
			t.Fatalf("%v no longer satisfies errors.Is(err, ErrInvalidState)", specific)
		}
		if errors.Is(specific, ErrSourceUnavailable) {
			t.Fatalf("%v unexpectedly satisfies errors.Is(err, ErrSourceUnavailable)", specific)
		}
	}
	if errors.Is(ErrEligibilitySourceStale, ErrInvalidState) {
		t.Fatal("ErrEligibilitySourceStale unexpectedly satisfies errors.Is(err, ErrInvalidState)")
	}
	all := append([]error{ErrEligibilitySourceStale}, invalidStateSpecific...)
	for i, left := range all {
		for j, right := range all {
			if i != j && errors.Is(left, right) {
				t.Fatalf("%v unexpectedly satisfies errors.Is(err, %v)", left, right)
			}
		}
	}
}
