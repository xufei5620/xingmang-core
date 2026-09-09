package postgresstore

import (
	"errors"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
)

// TestSubmitHonoursAConfiguredMinimumBelowTheOldFixedFloor is
// XM-INV-SETTABLE-INVOICE-MINIMUM's third guard, and it exists because the
// first two passes both declared the job done while a ¥200 floor was still
// standing somewhere.
//
// It mirrors production account 34 at the moment three submissions returned
// HTTP 500: eligibility active, no projection job, one verified WALLET_CASH
// lot whose consumed_cash_minor equals both the request amount and the
// configured minimum (¥5.00), nothing reserved or issued. Every application
// check passed and `invoice_requests_amount_minor_check` (>= 20000, from
// migration 0001) refused the INSERT. A raw database error matches none of
// handleDomainError's sentinels, so it became a bare 500 that logged nothing.
//
// Migration 0025 relaxes that CHECK to `> 0`. The real minimum belongs to
// admin_settings and is enforced in the application, where a violation can say
// which number it was.
func TestSubmitHonoursAConfiguredMinimumBelowTheOldFixedFloor(t *testing.T) {
	store, ctx := integrationStore(t)
	// Production's lot is a ¥5.00 top-up consumed in full, so paid == consumed.
	// The default fixture lot is ¥1000, and setFixtureConsumedCash's
	// single-unit shape can only express "fully consumed" without tripping
	// enforce_lot_consumption_mirror's numerator check.
	const lotID = "50000000-0000-4000-8000-000000000009"
	lot := domain.FundingLot{ID: lotID, PrincipalID: "20000000-0000-4000-8000-000000000001",
		SourceInstanceID: "10000000-0000-4000-8000-000000000001", SourceType: domain.SourceSub2API,
		ExternalOrderID: "order-five-yuan", Currency: domain.CurrencyCNY,
		OriginalMinor: 500, CurrentCapMinor: 500, Verification: domain.VerificationVerified,
		SourceStatus: "COMPLETED", SourceRevision: "r-five", CompletedAt: time.Now(), ObservedAt: time.Now()}
	if err := store.UpsertFundingLot(ctx, lot); err != nil {
		t.Fatal(err)
	}
	setFixtureConsumedCash(t, store, ctx, lotID, 500)

	in := SubmitInput{
		PrincipalID:               "20000000-0000-4000-8000-000000000001",
		ProfileID:                 "40000000-0000-4000-8000-000000000001",
		SourceInstanceID:          "10000000-0000-4000-8000-000000000001",
		ProfileSnapshotCiphertext: []byte("encrypted-snapshot"),
		MinimumRequestMinor:       500,
		IdempotencyKey:            "repro-500",
		Allocations:               []AllocationInput{{FundingLotID: lotID, AmountMinor: 500}},
	}
	got, err := store.Submit(ctx, in)
	if err != nil {
		t.Fatalf("a ¥5.00 request under a ¥5.00 configured minimum was refused: %v", err)
	}
	if got.ID == "" {
		t.Fatal("submit returned no request id")
	}

	// The configured minimum still binds -- relaxing the CHECK moved the rule
	// into the application, it did not remove it.
	below := in
	below.IdempotencyKey = "below-configured-minimum"
	below.Allocations = []AllocationInput{{FundingLotID: lotID, AmountMinor: 499}}
	if _, err = store.Submit(ctx, below); !errors.Is(err, domain.ErrMinimumAmount) {
		t.Fatalf("a request below the configured minimum must be refused with ErrMinimumAmount, got %v", err)
	}
}
