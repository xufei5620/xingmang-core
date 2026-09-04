package postgresstore

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"testing"
	"time"
)

// seedOverdrawFixture builds an account anchored at the policy start with a
// zero opening balance, so every unit the assertions below talk about comes
// from the lots and usage the test itself inserts.
//
// The shape is production's, not a contrivance: both sources bill as they go
// and allow a request to overdraw, so a user who spends down to zero and tops
// up again -- which, per the product owner, is how essentially every user
// behaves -- leaves exactly this trail.
func seedOverdrawFixture(t *testing.T, idSuffix int) (store *Store, ctx context.Context,
	sourceID, accountID, manifestHash, configHash string, policyStart time.Time) {
	t.Helper()
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart = fixtureNow.Add(-6 * time.Hour).Truncate(time.Second)
	var chain *v3TestChain
	store, ctx, sourceID, accountID, manifestHash, configHash, chain = policyStartBootstrapFixture(t, idSuffix, policyStart)

	anchorAt := policyStart.Add(time.Minute)
	anchorEvent := SourceBatchEvent{EventID: policyStartUUID('8', idSuffix*10),
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("overdraw-anchor-event"),
		PayloadCiphertext: bytes.Repeat([]byte{4}, 32), ObservedAt: anchorAt}
	anchorCycle := chain.commit(t, store, ctx, sourceID, "balances",
		policyStartUUID('9', idSuffix*10), anchorAt, []SourceBatchEvent{anchorEvent})
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: fmt.Sprint(idSuffix), ExternalEventID: anchorEvent.EventID,
		CheckpointID: "overdraw-anchor", CheckpointKind: "reconciliation", BaselineMember: true,
		BalanceServiceUnits: "0", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(anchorCycle.cycleID),
		SnapshotRowCount: "1", AsOf: anchorAt, ObservedAt: anchorAt, StreamWatermarkAt: anchorAt,
		SourceCursor: "balance:overdraw:anchor", SourceRevision: anchorEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: anchorCycle.batchID, ScanCycleID: anchorCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatalf("zero-balance anchor should bootstrap POLICY_ANCHOR: %v", err)
	}
	return store, ctx, sourceID, accountID, manifestHash, configHash, policyStart
}

// insertOverdrawCashLot gives the account one verified WALLET_CASH lot worth
// cashServiceUnits of service, paid for with 50000 minor.
func insertOverdrawCashLot(t *testing.T, store *Store, ctx context.Context,
	accountID, sourceID, orderSuffix string, completedAt time.Time, cashServiceUnits string) string {
	t.Helper()
	lotID := randomUUID()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO funding_lots(
			id,invoice_user_id,external_account_id,source_instance_id,external_order_id,currency,
			original_minor,current_cap_minor,reserved_minor,issued_minor,verification_state,source_status,
			source_revision_hash,completed_at,observed_at,eligibility_kind,eligibility_cutover_at,
			verified_cash_minor,consumed_cash_minor,refund_frozen,eligibility_revision)
		VALUES($1,(SELECT invoice_user_id FROM external_accounts WHERE id=$2),$2,$3,$4,'CNY',
			50000,50000,0,0,'verified','COMPLETED',$5,$6,$6,'WALLET_CASH',$6,50000,0,FALSE,1)`,
		lotID, accountID, sourceID, "overdraw-order-"+orderSuffix,
		testHash("overdraw-payment-"+orderSuffix), completedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO funding_lot_consumption_state(
			funding_lot_id,cash_service_units,consumed_service_units,cumulative_cash_numerator,
			rounded_consumed_cash_minor,rounding_remainder_numerator)
		VALUES($1,$2::numeric,0,0,0,0)`, lotID, cashServiceUnits); err != nil {
		t.Fatal(err)
	}
	return lotID
}

func projectOverdrawAccount(t *testing.T, store *Store, ctx context.Context,
	accountID string, through time.Time) eligibilityProjection {
	t.Helper()
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	account, err := getEligibilityAccountTx(ctx, tx, accountID, true)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := buildEligibilityProjectionTx(ctx, tx, account, through)
	if err != nil {
		t.Fatalf("projection failed: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return projection
}

// TestOverdrawnUsageIsChargedToTheNextTopUp is XM-INV-OVERAGE-CARRY-FORWARD's
// regression guard.
//
// 500 units of funding, 800 units spent, then 500 more units of funding. The
// source lets that request through and carries the account 300 units negative,
// then settles the debt out of the next top-up -- so after it, the source
// reports 200 units of balance, and 300 units of the second top-up's cash paid
// for real consumption.
//
// Before the fix this projection dropped the 300 units: it expected 500,
// disagreed with the source by exactly 300 on that checkpoint and every
// checkpoint after it, and parked the account in
// not_invoiceable_pending_reconciliation with a difference it could never
// clear -- while also never invoicing the 300 units the user had paid for.
// Production account acdcdce9 sat there for two days behind 380 identical
// -1361800 differences.
func TestOverdrawnUsageIsChargedToTheNextTopUp(t *testing.T) {
	store, ctx, sourceID, accountID, manifestHash, configHash, policyStart := seedOverdrawFixture(t, 213)
	firstLot := insertOverdrawCashLot(t, store, ctx, accountID, sourceID, "first",
		policyStart.Add(1*time.Hour), "500")
	usageRowID := insertUsageEventDirect(t, store, ctx, sourceID, accountID, "overdraw-usage",
		policyStart.Add(2*time.Hour), "800", 1, manifestHash, configHash)
	secondLot := insertOverdrawCashLot(t, store, ctx, accountID, sourceID, "second",
		policyStart.Add(3*time.Hour), "500")

	projection := projectOverdrawAccount(t, store, ctx, accountID, policyStart.Add(4*time.Hour))

	if want := big.NewInt(200); projection.ExpectedBalance.Cmp(want) != 0 {
		t.Fatalf("expected balance %s, want %s: the overdrawn 300 units must come off the second top-up, "+
			"otherwise every later checkpoint disagrees with the source by exactly that amount",
			projection.ExpectedBalance, want)
	}
	if projection.ShortfallUsage != "" {
		t.Fatalf("a debt the next top-up settled is not an overage: shortfall=%s units=%s",
			projection.ShortfallUsage, projection.ShortfallUnits)
	}
	byLot := map[string]*big.Int{}
	cashByLot := map[string]int64{}
	orders := map[int]bool{}
	for _, allocation := range projection.Allocations {
		if allocation.UsageID != usageRowID {
			t.Fatalf("unexpected allocation for usage %s", allocation.UsageID)
		}
		if byLot[allocation.LotID] == nil {
			byLot[allocation.LotID] = new(big.Int)
		}
		byLot[allocation.LotID].Add(byLot[allocation.LotID], allocation.Units)
		cashByLot[allocation.LotID] += allocation.CashMinorDelta
		if orders[allocation.Order] {
			t.Fatalf("allocation order %d reused: consumption_allocations is UNIQUE(usage_event_id,allocation_order)",
				allocation.Order)
		}
		orders[allocation.Order] = true
	}
	if got := byLot[firstLot]; got == nil || got.Cmp(big.NewInt(500)) != 0 {
		t.Fatalf("first lot took %v units, want 500", got)
	}
	if got := byLot[secondLot]; got == nil || got.Cmp(big.NewInt(300)) != 0 {
		t.Fatalf("second lot took %v units, want the 300 carried over", got)
	}
	// 300 of the second lot's 500 units, priced at its 50000 minor, is what
	// the user actually paid for that overdraw -- so it is invoiceable, and
	// the cash has to land on the lot rather than being written off.
	if got := cashByLot[secondLot]; got != 30000 {
		t.Fatalf("second lot cash delta %d, want 30000", got)
	}
}

// TestOverdrawnUsageWithNoLaterTopUpStaysAnOverage pins the other half: carry
// forward is not forgiveness. Until funding actually arrives, the overdrawn
// units remain unallocated and keep being reported as the account's
// non-invoiceable overage, exactly as before.
func TestOverdrawnUsageWithNoLaterTopUpStaysAnOverage(t *testing.T) {
	store, ctx, sourceID, accountID, manifestHash, configHash, policyStart := seedOverdrawFixture(t, 214)
	insertOverdrawCashLot(t, store, ctx, accountID, sourceID, "only",
		policyStart.Add(1*time.Hour), "500")
	usageRowID := insertUsageEventDirect(t, store, ctx, sourceID, accountID, "overdraw-usage-unpaid",
		policyStart.Add(2*time.Hour), "800", 1, manifestHash, configHash)

	projection := projectOverdrawAccount(t, store, ctx, accountID, policyStart.Add(4*time.Hour))

	if projection.ExpectedBalance.Sign() != 0 {
		t.Fatalf("expected balance %s, want 0", projection.ExpectedBalance)
	}
	if projection.ShortfallUsage != usageRowID {
		t.Fatalf("shortfall usage %q, want %q", projection.ShortfallUsage, usageRowID)
	}
	if want := big.NewInt(300); projection.ShortfallUnits == nil || projection.ShortfallUnits.Cmp(want) != 0 {
		t.Fatalf("shortfall units %v, want %s", projection.ShortfallUnits, want)
	}
	if len(projection.Allocations) != 1 {
		t.Fatalf("allocations=%d, want the single 500-unit allocation the one lot could cover",
			len(projection.Allocations))
	}
}
