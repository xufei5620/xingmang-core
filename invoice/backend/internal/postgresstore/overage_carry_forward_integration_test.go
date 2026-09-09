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
// 500 units of cash funding, 800 units spent, then 500 more units of cash. The
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

// insertOverdrawNonCashCredit gives the account a non-cash credit pool -- the
// shape a gift, a REBATE, or an evaluator-synthesized UNKNOWN_POSITIVE takes
// once it reaches the projection.
func insertOverdrawNonCashCredit(t *testing.T, store *Store, ctx context.Context,
	accountID, sourceID, suffix string, eventTime time.Time, units string,
	manifestHash, configHash string) string {
	t.Helper()
	id := randomUUID()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_credit_events(
			id,source_instance_id,external_account_id,external_event_id,external_credit_id,
			event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
			credit_kind,source_sequence,source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES($1,$2,$3,$4,$4,$5,$6::numeric,'SUB2_BALANCE_1E8',$7,$8,'REBATE',1,$9,$5,$10,$5)`,
		id, sourceID, accountID, "overdraw-credit-"+suffix, eventTime, units,
		manifestHash, configHash, "cursor:credit:"+suffix, testHash("credit-"+suffix)); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestOverdrawnUsageIsNotSettledByNonCashCredit pins the boundary the product
// owner drew: only cash settles a carried debt.
//
// A gift, a REBATE, or an UNKNOWN_POSITIVE the evaluator synthesized to
// explain a positive difference it could not attribute is not a payment.
// Draining a debt against one would lower the expected balance with no money
// behind it, and the consumption would stay non-invoiceable regardless -- so
// the debt stays outstanding and keeps being reported as the overage.
//
// Production account 40bd883d is the live case: its overdraw is not deducted
// upstream either (its difference reads 0), and its pools are four
// synthesized UNKNOWN_POSITIVE credits. Draining into those would have pushed
// a reconciling account off zero for nothing.
func TestOverdrawnUsageIsNotSettledByNonCashCredit(t *testing.T) {
	store, ctx, sourceID, accountID, manifestHash, configHash, policyStart := seedOverdrawFixture(t, 215)
	insertOverdrawCashLot(t, store, ctx, accountID, sourceID, "only-cash",
		policyStart.Add(1*time.Hour), "500")
	usageRowID := insertUsageEventDirect(t, store, ctx, sourceID, accountID, "overdraw-usage-noncash",
		policyStart.Add(2*time.Hour), "800", 1, manifestHash, configHash)
	insertOverdrawNonCashCredit(t, store, ctx, accountID, sourceID, "gift",
		policyStart.Add(3*time.Hour), "500", manifestHash, configHash)

	projection := projectOverdrawAccount(t, store, ctx, accountID, policyStart.Add(4*time.Hour))

	if want := big.NewInt(500); projection.ExpectedBalance.Cmp(want) != 0 {
		t.Fatalf("expected balance %s, want %s: the non-cash credit must stay whole -- "+
			"draining the 300-unit debt into it would lower the expected balance with no payment behind it",
			projection.ExpectedBalance, want)
	}
	if projection.ShortfallUsage != usageRowID {
		t.Fatalf("shortfall usage %q, want %q: an overdraw no cash has covered is still an overage",
			projection.ShortfallUsage, usageRowID)
	}
	if want := big.NewInt(300); projection.ShortfallUnits == nil || projection.ShortfallUnits.Cmp(want) != 0 {
		t.Fatalf("shortfall units %v, want %s", projection.ShortfallUnits, want)
	}
	for _, allocation := range projection.Allocations {
		if allocation.CreditID != "" {
			t.Fatalf("usage was allocated against non-cash credit %s: only cash settles a debt", allocation.CreditID)
		}
	}
}

// TestOverdrawnUsageWithNoLaterTopUpStaysAnOverage pins the other half: carry
// forward is not forgiveness. Until cash actually arrives, the overdrawn units
// remain unallocated and keep being reported as the account's
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

// TestPreAnchorCheckpointIsNotGapEvidence is XM-INV-PREANCHOR-BALANCE's guard.
//
// A POLICY_ANCHOR account's ledger begins at its own cutover_at. Balance
// evidence dated before that says nothing about this account's books, because
// buildEligibilityProjectionTx floors every fact query at cutover_at -- so the
// expected balance comes out 0, the whole reported balance reads as an
// unexplained positive difference, and there is no trust interval to anchor a
// synthesis against. The result is SOURCE_GAP, permanently: the repair tool
// resolves the freeze and reopens the checkpoint, the next projection redoes
// the identical arithmetic, and the freeze comes straight back.
//
// Production account 98cce4c8 sat there with four such checkpoints, stranded
// in the 21 hours between its own first post-policy checkpoint and the RC68
// deploy that first taught the system to bootstrap an anchor at all. The fact
// side already skips rather than freezes for the same reason
// (XM-INV-PREANCHOR-USAGE); this is the balance side catching up.
func TestPreAnchorCheckpointIsNotGapEvidence(t *testing.T) {
	store, ctx, sourceID, accountID, manifestHash, configHash, policyStart := seedOverdrawFixture(t, 216)

	var anchor time.Time
	var bootstrapKind string
	if err := store.pool.QueryRow(ctx, `SELECT cutover_at,bootstrap_kind
		FROM source_account_eligibility_state WHERE external_account_id=$1`,
		accountID).Scan(&anchor, &bootstrapKind); err != nil {
		t.Fatal(err)
	}
	if bootstrapKind != "POLICY_ANCHOR" {
		t.Fatalf("fixture bootstrap_kind=%q, want POLICY_ANCHOR", bootstrapKind)
	}

	// A checkpoint dated before the account's own anchor, carrying a real
	// upstream balance -- exactly the production shape.
	// Note what the fixture shows about the blast radius: the current
	// bootstrap anchors an account at the *global policy start*, so for any
	// account created today "after the policy start but before its own
	// anchor" is an empty window and this shape cannot arise. Production
	// account 98cce4c8 has it only because it was bootstrapped by the older
	// code, which anchored at the triggering checkpoint's own as_of. That is
	// why this is a one-off to clean up rather than a growing problem -- and
	// why the evidence here is placed relative to the anchor rather than to
	// the policy start, which is the same instant.
	_ = policyStart
	before := anchor.Add(-30 * time.Minute)
	insertPreAnchorCheckpoint(t, store, ctx, sourceID, accountID, "pre-anchor-216",
		before, "3338358924", manifestHash, configHash)

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = evaluatePendingBalanceEvidenceTx(ctx, tx, accountID, time.Now().UTC().Add(time.Hour),
		AuditActor{Type: "system", ID: "pre-anchor-test"}); err != nil {
		t.Fatalf("evaluating pending evidence failed: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var evaluations, freezes int
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM balance_checkpoint_evaluations e
		JOIN balance_reconciliation_checkpoints c ON c.id=e.checkpoint_id
		WHERE c.external_account_id=$1 AND c.as_of<$2`, accountID, anchor).Scan(&evaluations); err != nil {
		t.Fatal(err)
	}
	if evaluations != 0 {
		t.Fatalf("a pre-anchor checkpoint was evaluated (%d rows): its expected balance is 0 by construction, "+
			"so evaluating it can only ever produce an unexplainable difference", evaluations)
	}
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes
		WHERE external_account_id=$1 AND status='open'`, accountID).Scan(&freezes); err != nil {
		t.Fatal(err)
	}
	if freezes != 0 {
		t.Fatalf("a pre-anchor checkpoint froze the account: open freezes=%d", freezes)
	}
}

// insertPreAnchorCheckpoint writes one reconciliation checkpoint directly, at
// an as_of the caller chooses -- ObserveBalanceCheckpoint would refuse to
// place evidence before an already-bootstrapped anchor, which is the very
// state this test needs to reproduce.
func insertPreAnchorCheckpoint(t *testing.T, store *Store, ctx context.Context,
	sourceID, accountID, key string, asOf time.Time, balanceUnits, manifestHash, configHash string) {
	t.Helper()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO balance_reconciliation_checkpoints(
			id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
			checkpoint_kind,as_of,balance_service_units,unit_code,cutover_manifest_hash,
			configuration_hash,reconciliation_status,source_sequence,source_cursor,
			stream_watermark_at,source_revision_hash,observed_at)
		VALUES($1,$2,$3,$4,$4,'reconciliation',$5,$6::numeric,'SUB2_BALANCE_1E8',$7,$8,
			'pending_finalization',1,$9,$5,$10,$5)`,
		randomUUID(), sourceID, accountID, key, asOf, balanceUnits,
		manifestHash, configHash, "cursor:"+key, testHash(key)); err != nil {
		t.Fatal(err)
	}
}
