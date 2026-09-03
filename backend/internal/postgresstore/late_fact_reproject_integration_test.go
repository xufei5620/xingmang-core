package postgresstore

import (
	"testing"
	"time"
)

// TestLateFactReprojectsWithoutFreezingAccount covers design
// XM-INV-ELIG-SIMPLIFY section 3(C) item 2: observeEligibilityFact's
// generalized "any late fact freezes the whole account" defense is removed
// -- a late fact that reprojects cleanly (no real red-reversal) must open no
// eligibility_freezes row at all, and the audit trail must record
// eligibility.late_fact.reprojected instead of a freeze.
func TestLateFactReprojectsWithoutFreezingAccount(t *testing.T) {
	store, ctx, sourceID, accountID, _, manifestHash, configHash, anchorAt, chain := newAutoReconcileFixture(t)

	var finalizedThrough time.Time
	if err := store.pool.QueryRow(ctx, `SELECT finalized_through FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&finalizedThrough); err != nil {
		t.Fatal(err)
	}
	// Sanity check on the fixture's own timing assumption (finalized_through
	// anchorAt+1min): lateAt must be at or before it to be "late" per
	// observeEligibilityFact's own !EventTime.After(...) rule.
	lateAt := anchorAt.Add(30 * time.Second)
	if lateAt.After(finalizedThrough) {
		t.Fatalf("fixture timing changed: lateAt=%s must be <= finalized_through=%s for this test to exercise the late path", lateAt, finalizedThrough)
	}

	observeSimpleCreditEvent(t, store, ctx, chain, sourceID, manifestHash, configHash,
		"late-harmless-credit", "BONUS", lateAt, "10", 1)

	if count := openFreezeCount(t, store, ctx, accountID); count != 0 {
		t.Fatalf("a late fact that reprojects cleanly must not open any freeze: open freezes=%d", count)
	}
	var status string
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_status FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&status); err != nil || status != "active" {
		t.Fatalf("account status=%q err=%v, want active (unfrozen)", status, err)
	}

	var creditID string
	if err := store.pool.QueryRow(ctx, `SELECT id::text FROM source_credit_events
		WHERE external_account_id=$1 AND external_credit_id='late-harmless-credit'`, accountID).Scan(&creditID); err != nil {
		t.Fatal(err)
	}
	var reprojectedAudits int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.late_fact.reprojected' AND object_type='credit' AND object_id=$1`,
		creditID).Scan(&reprojectedAudits); err != nil || reprojectedAudits != 1 {
		t.Fatalf("late_fact.reprojected audit count=%d err=%v, want 1", reprojectedAudits, err)
	}
	var frozenAudits int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action LIKE 'eligibility.frozen.%' AND object_id=$1`, accountID).Scan(&frozenAudits); err != nil || frozenAudits != 0 {
		t.Fatalf("frozen.* audit count=%d err=%v, want 0", frozenAudits, err)
	}
}

// TestLateFactRedReversalStillOpensPreciseFreeze covers design
// XM-INV-ELIG-SIMPLIFY section 3(C) item 2's other half: observeEligibilityFact
// still calls reprojectEligibilityTx unconditionally when a fact is late, and
// that function's own, untouched lot.RoundedMinor<lot.IssuedMinor branch must
// still open its precise, funding_lot-scoped LATE_FINALIZED_EVENT freeze when
// a late fact genuinely causes a red-reversal (an already-issued invoice's
// recognized amount dropping below what was issued). A lot is fully consumed
// (100 units), then an invoice-issued exposure is simulated directly via SQL
// (markLotIssuedAttentionTx itself no-ops cleanly with no matching
// invoice_allocations rows -- sufficient to arm the guard without a full
// invoice fixture); a late-arriving, earlier-dated non-cash credit then
// reallocates 50 of the usage's units away from the cash lot, dropping its
// recognized amount to 50_000 -- below the 100_000 "issued" -- which must
// still freeze, precisely, exactly as before this slice.
func TestLateFactRedReversalStillOpensPreciseFreeze(t *testing.T) {
	store, ctx, sourceID, accountID, userID, manifestHash, configHash, anchorAt, chain := newAutoReconcileFixture(t)
	worker := AuditActor{Type: "system", ID: "test-worker"}

	paymentAt := anchorAt.Add(5 * time.Minute)
	lotID := observeSimpleWalletCashLot(t, store, ctx, chain, sourceID, accountID, userID, manifestHash, configHash,
		"SUB2_BALANCE_1E8", "redflush-lot", 100, paymentAt, 1)

	usageAt := anchorAt.Add(20 * time.Minute)
	observeSimpleUsageEvent(t, store, ctx, chain, sourceID, accountID, manifestHash, configHash,
		"redflush-usage", usageAt, "100", 1)

	emptyBalancesAt := anchorAt.Add(25 * time.Minute)
	emptyBalancesCycle := chain.commit(t, store, ctx, sourceID, "balances", autoReconcileUUID('9', 860), emptyBalancesAt, nil)
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", emptyBalancesCycle)

	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now())`, accountID, emptyBalancesAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	processed, err := store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute), worker)
	if err != nil || processed != 1 {
		t.Fatalf("projection processed=%d err=%v", processed, err)
	}

	lot, err := store.GetFundingLot(ctx, lotID)
	if err != nil || lot.ConsumedCashMinor != 100_000 {
		t.Fatalf("lot=%+v err=%v, want fully consumed at 100_000 before simulating an issued invoice", lot, err)
	}

	if _, err := store.pool.Exec(ctx, `UPDATE funding_lots SET issued_minor=100000 WHERE id=$1`, lotID); err != nil {
		t.Fatal(err)
	}

	lateCreditAt := anchorAt.Add(2 * time.Minute)
	observeSimpleCreditEvent(t, store, ctx, chain, sourceID, manifestHash, configHash,
		"redflush-late-bonus", "BONUS", lateCreditAt, "50", 1)

	if count := openFreezeCount(t, store, ctx, accountID); count != 1 {
		t.Fatalf("open freeze count=%d, want exactly 1 (the precise, funding_lot-scoped freeze)", count)
	}
	var reason, triggerType, triggerID, fundingLotID string
	if err := store.pool.QueryRow(ctx, `SELECT freeze_reason,trigger_object_type,trigger_object_id,COALESCE(funding_lot_id::text,'')
		FROM eligibility_freezes WHERE external_account_id=$1 AND status='open'`, accountID).Scan(
		&reason, &triggerType, &triggerID, &fundingLotID); err != nil {
		t.Fatal(err)
	}
	if reason != "LATE_FINALIZED_EVENT" || triggerType != "funding_lot" || triggerID != lotID || fundingLotID != lotID {
		t.Fatalf("freeze reason=%s triggerType=%s triggerID=%s fundingLotID=%s, want precise funding_lot-scoped LATE_FINALIZED_EVENT for %s",
			reason, triggerType, triggerID, fundingLotID, lotID)
	}
	var status string
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_status FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&status); err != nil || status != "frozen" {
		t.Fatalf("account status=%q err=%v, want frozen", status, err)
	}

	var creditID string
	if err := store.pool.QueryRow(ctx, `SELECT id::text FROM source_credit_events
		WHERE external_account_id=$1 AND external_credit_id='redflush-late-bonus'`, accountID).Scan(&creditID); err != nil {
		t.Fatal(err)
	}
	var reprojectedAudits int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.late_fact.reprojected' AND object_type='credit' AND object_id=$1`,
		creditID).Scan(&reprojectedAudits); err != nil || reprojectedAudits != 1 {
		t.Fatalf("late_fact.reprojected audit count=%d err=%v, want 1 (unconditional even when reprojection also freezes)", reprojectedAudits, err)
	}
	var frozenAudits int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.frozen.late_finalized_event' AND object_id IN (
			SELECT id::text FROM eligibility_freezes WHERE external_account_id=$1)`,
		accountID).Scan(&frozenAudits); err != nil || frozenAudits != 1 {
		t.Fatalf("frozen.late_finalized_event audit count=%d err=%v, want 1 (still opened by reprojectEligibilityTx's own red-reversal branch)", frozenAudits, err)
	}
}
