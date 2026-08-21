package domain

import (
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
