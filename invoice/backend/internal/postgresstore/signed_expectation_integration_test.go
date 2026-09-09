package postgresstore

import (
	"context"
	"testing"
	"time"
)

// XM-INV-PENDING-RECON C5 (design section 3, acceptance ruling 2026-09-09 on
// section 7 D4(a)): the evaluator used two different definitions of "what the
// ledger expects". The negative branch subtracted UnallocatedUnits
// (XM-INV-NEGATIVE-DEFICIT); the positive branch compared against
// ExpectedBalance, which is floored at zero because the column it is written
// to has a >=0 CHECK. Once an account's pools are exhausted that floor pins
// the expectation at zero while the debt keeps growing, so a genuine top-up
// looks like an unexplainable surplus -- or, once the sign flips, like a
// negative difference that parks the account in pending reconciliation on
// every single checkpoint.
//
// signedExpectedUnits is now the single definition, used by all four
// comparisons. These tests pin the behaviour for an account that carries a
// debt, together with a same-account control that has nothing unallocated
// and must be completely unaffected.

// evaluateSignedFixture runs the evaluator over one account's pending
// evidence up to `through`, committing, exactly as the projection worker's
// own phase does.
func evaluateSignedFixture(t *testing.T, store *Store, ctx context.Context, accountID string, through time.Time) {
	t.Helper()
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = evaluatePendingBalanceEvidenceTx(ctx, tx, accountID, through,
		AuditActor{Type: "system", ID: "test-worker"}); err != nil {
		t.Fatalf("evaluate through %s: %v", through.UTC(), err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func signedCheckpointEvaluation(t *testing.T, store *Store, ctx context.Context, accountID, checkpointID string) (status, expected, difference string) {
	t.Helper()
	if err := store.pool.QueryRow(ctx, `
		SELECT evaluation.evaluation_status,evaluation.expected_service_units::text,
			evaluation.difference_service_units::text
		FROM balance_reconciliation_checkpoints checkpoint
		JOIN balance_checkpoint_evaluations evaluation ON evaluation.checkpoint_id=checkpoint.id
		WHERE checkpoint.external_account_id=$1 AND checkpoint.checkpoint_id=$2`,
		accountID, checkpointID).Scan(&status, &expected, &difference); err != nil {
		t.Fatalf("evaluation for %s: %v", checkpointID, err)
	}
	return status, expected, difference
}

// TestPositiveBalanceIsExplainedByTheSignedExpectation is design matrix row
// (c) and acceptance criterion A5. An account runs its non-cash pool dry and
// keeps consuming, so 200 units are unallocated and ExpectedBalance is
// floored at zero. A later non-cash top-up of 500 does not settle that debt
// (only cash does), so the ledger's true position is 500 - 200 = 300 -- and
// that is exactly what the source reports.
//
// Against the signed expectation this matches. Against the floored one it
// was a difference of -200: negative_frozen, and the account parked in
// pending reconciliation with nothing wrong with it.
//
// The same account's earlier checkpoint, taken while nothing was
// unallocated, is the control: its numbers are identical under both
// definitions, so a mutation of signedExpectedUnits must leave it green.
func TestPositiveBalanceIsExplainedByTheSignedExpectation(t *testing.T) {
	store, ctx, sourceID, accountID, _, manifestHash, configHash, anchorAt, _ := newAutoReconcileFixture(t)

	// Control leg: 100 units of credit, no usage at all, a checkpoint that
	// reports exactly that. Nothing unallocated, so the two definitions of
	// "expected" agree.
	creditAt := anchorAt.Add(5 * time.Minute)
	insertCreditEventDirect(t, store, ctx, sourceID, accountID, "signed-credit-a",
		creditAt, "100", "BONUS", 2, manifestHash, configHash)
	controlAt := anchorAt.Add(7 * time.Minute)
	insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "signed-control",
		controlAt, "100", 2, manifestHash, configHash)
	evaluateSignedFixture(t, store, ctx, accountID, controlAt.Add(time.Minute))
	if status, expected, difference := signedCheckpointEvaluation(t, store, ctx, accountID, "signed-control"); status != "matched" ||
		expected != "100" || difference != "0" {
		t.Fatalf("control (nothing unallocated) status=%q expected=%s difference=%s, want matched/100/0",
			status, expected, difference)
	}

	// Debt leg: 300 units of usage against a 100-unit pool leaves 200
	// unallocated and ExpectedBalance floored at 0.
	usageAt := anchorAt.Add(10 * time.Minute)
	insertUsageEventDirect(t, store, ctx, sourceID, accountID, "signed-usage-a",
		usageAt, "300", 1, manifestHash, configHash)
	// A non-cash top-up of 500. Only cash ever settles a carried debt, so
	// this leaves the ledger at pools=500, unallocated=200.
	topUpAt := anchorAt.Add(15 * time.Minute)
	insertCreditEventDirect(t, store, ctx, sourceID, accountID, "signed-credit-b",
		topUpAt, "500", "BONUS", 3, manifestHash, configHash)

	reportedAt := anchorAt.Add(20 * time.Minute)
	insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "signed-explained",
		reportedAt, "300", 3, manifestHash, configHash)
	evaluateSignedFixture(t, store, ctx, accountID, reportedAt.Add(time.Minute))

	status, expected, difference := signedCheckpointEvaluation(t, store, ctx, accountID, "signed-explained")
	if status != "matched" || difference != "0" {
		t.Fatalf("a balance the ledger fully explains status=%q difference=%s, want matched/0 "+
			"(the floored comparison called this -200 and parked the account)", status, difference)
	}
	// expected_service_units keeps storing the unsigned ExpectedBalance --
	// the column has a >=0 CHECK, and the signed value is what difference
	// already encodes.
	if expected != "500" {
		t.Fatalf("expected_service_units=%s, want 500 (the unsigned pool total, per the column's own >=0 CHECK)", expected)
	}
	row := readPendingReconciliation(t, store, ctx, accountID)
	if row.status != "active" || row.reason != "" {
		t.Fatalf("an explained balance must not park the account: %+v", row)
	}
	if count := openFreezeCount(t, store, ctx, accountID); count != 0 {
		t.Fatalf("open freezes=%d, want 0", count)
	}
}

// TestBoundaryRuleUsesTheSignedExpectation is design matrix row (k), the
// boundary-rule leg: the XM-INV-BALANCE-BLIP rule that forgives a usage event
// landing at a checkpoint's own as_of must compare its recomputed projection
// the same way every other comparison does. On an account with a carried
// debt it did not, so the rule silently stopped applying to exactly the
// accounts that needed it, and the checkpoint fell through to deferral.
func TestBoundaryRuleUsesTheSignedExpectation(t *testing.T) {
	store, ctx, sourceID, accountID, _, manifestHash, configHash, anchorAt, _ := newAutoReconcileFixture(t)

	creditAt := anchorAt.Add(5 * time.Minute)
	insertCreditEventDirect(t, store, ctx, sourceID, accountID, "boundary-credit-a",
		creditAt, "100", "BONUS", 2, manifestHash, configHash)
	usageAt := anchorAt.Add(10 * time.Minute)
	insertUsageEventDirect(t, store, ctx, sourceID, accountID, "boundary-usage-a",
		usageAt, "300", 1, manifestHash, configHash)
	topUpAt := anchorAt.Add(15 * time.Minute)
	insertCreditEventDirect(t, store, ctx, sourceID, accountID, "boundary-credit-b",
		topUpAt, "500", "BONUS", 3, manifestHash, configHash)

	// The coincidence: 50 units of usage whose event_time is the checkpoint's
	// own as_of. The inclusive projection subtracts it; the upstream snapshot
	// was captured before it was applied, so the source still reports 300.
	reportedAt := anchorAt.Add(20 * time.Minute)
	insertUsageEventDirect(t, store, ctx, sourceID, accountID, "boundary-usage-coincident",
		reportedAt, "50", 2, manifestHash, configHash)
	insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "boundary-coincident",
		reportedAt, "300", 3, manifestHash, configHash)
	evaluateSignedFixture(t, store, ctx, accountID, reportedAt.Add(time.Minute))

	status, expected, difference := signedCheckpointEvaluation(t, store, ctx, accountID, "boundary-coincident")
	if status != "matched" || difference != "0" {
		t.Fatalf("boundary coincidence on an account with a carried debt status=%q difference=%s, want matched/0",
			status, difference)
	}
	if expected != "500" {
		t.Fatalf("boundary-resolved expected_service_units=%s, want 500 (the adjusted pool total)", expected)
	}
	if row := readPendingReconciliation(t, store, ctx, accountID); row.status != "active" {
		t.Fatalf("the boundary rule must resolve this without parking the account: %+v", row)
	}
}

// insertNegativeReconciliationCheckpoint is insertReconciliationCheckpoint
// for a checkpoint reporting a negative balance with its magnitude, which is
// what an account whose pools are exhausted actually reports.
func insertNegativeReconciliationCheckpoint(t *testing.T, store *Store, ctx context.Context,
	sourceID, accountID, checkpointID string, asOf time.Time, deficit string, sourceSequence int64,
	manifestHash, configHash string) {
	t.Helper()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO balance_reconciliation_checkpoints(
			id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
			checkpoint_kind,baseline_member,as_of,balance_service_units,balance_negative,
			deficit_service_units,unit_code,cutover_manifest_hash,configuration_hash,
			reconciliation_status,source_sequence,source_cursor,stream_watermark_at,
			source_revision_hash,observed_at)
		VALUES($1,$2,$3,$4,$5,'reconciliation',FALSE,$6,0,TRUE,$7::numeric,'SUB2_BALANCE_1E8',
			$8,$9,'pending_finalization',$10,$11,$6,$12,$6)`,
		randomUUID(), sourceID, accountID, checkpointID+"-event", checkpointID, asOf, deficit,
		manifestHash, configHash, sourceSequence, "cursor:"+checkpointID, testHash(checkpointID)); err != nil {
		t.Fatal(err)
	}
}

// TestBlipConfirmationRebuildUsesTheSignedExpectation is design matrix row
// (k), the confirmation-rebuild leg -- and the shape the slice was written
// for: an account carrying a debt that a later non-cash credit explains.
//
// Only cash ever settles a carried debt, so when the synthesized credit is
// dated after the usage that created the debt, the rebuilt projection still
// holds that debt: pools 700, unallocated 200, true position 500 -- exactly
// what the source reports. The rebuild reconciles against the signed
// expectation and the pair is confirmed. Against the floored one it is 200
// short, and the account rebaselines instead of self-healing.
//
// This is the call site the floored-ledger test above cannot distinguish:
// there the synthesized credit lands before the usage, so nothing is left
// unallocated afterwards and the two definitions agree.
func TestBlipConfirmationRebuildUsesTheSignedExpectation(t *testing.T) {
	store, ctx, sourceID, accountID, _, manifestHash, configHash, anchorAt, _ := newAutoReconcileFixture(t)

	creditAt := anchorAt.Add(5 * time.Minute)
	insertCreditEventDirect(t, store, ctx, sourceID, accountID, "rebuild-credit-a",
		creditAt, "100", "BONUS", 2, manifestHash, configHash)
	usageAt := anchorAt.Add(10 * time.Minute)
	insertUsageEventDirect(t, store, ctx, sourceID, accountID, "rebuild-usage-a",
		usageAt, "300", 1, manifestHash, configHash)

	// A matched negative checkpoint after the usage: the source agrees the
	// account is 200 in the hole. This is what moves the trust interval past
	// the usage, so a later synthesized credit is dated after the debt and
	// cannot settle it.
	trustedAt := anchorAt.Add(12 * time.Minute)
	insertNegativeReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "rebuild-trusted",
		trustedAt, "200", 2, manifestHash, configHash)
	evaluateSignedFixture(t, store, ctx, accountID, trustedAt.Add(time.Minute))
	if status, _, difference := signedCheckpointEvaluation(t, store, ctx, accountID, "rebuild-trusted"); status != "matched" ||
		difference != "0" {
		t.Fatalf("fixture: the agreed overdraw must evaluate matched/0, got %s/%s", status, difference)
	}

	// Two checkpoints reporting the same 500 against the same ledger: the
	// deferral, then the confirmation.
	cpAAt := anchorAt.Add(20 * time.Minute)
	insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "rebuild-cp-a",
		cpAAt, "500", 3, manifestHash, configHash)
	cpBAt := anchorAt.Add(30 * time.Minute)
	insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "rebuild-cp-b",
		cpBAt, "500", 4, manifestHash, configHash)
	evaluateSignedFixture(t, store, ctx, accountID, cpBAt.Add(time.Minute))

	if status, expected, difference := signedCheckpointEvaluation(t, store, ctx, accountID, "rebuild-cp-a"); status != "positive_classified_non_cash" ||
		expected != "0" || difference != "700" {
		t.Fatalf("deferred item status=%q expected=%s difference=%s, want positive_classified_non_cash/0/700",
			status, expected, difference)
	}
	// The rebuild: pools 700, still 200 unallocated, signed position 500.
	if status, expected, difference := signedCheckpointEvaluation(t, store, ctx, accountID, "rebuild-cp-b"); status != "matched" ||
		expected != "700" || difference != "0" {
		t.Fatalf("confirming item status=%q expected=%s difference=%s, want matched/700/0 "+
			"(the floored rebuild is 200 short and rebaselines instead)", status, expected, difference)
	}
	var rebaselineAudits int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.balance_blip.rebaselined'`).Scan(&rebaselineAudits); err != nil {
		t.Fatal(err)
	}
	if rebaselineAudits != 0 {
		t.Fatalf("rebaselined audits=%d, want 0 (the pair confirmed cleanly)", rebaselineAudits)
	}
	if row := readPendingReconciliation(t, store, ctx, accountID); row.status != "active" {
		t.Fatalf("a confirmed, explained surplus must leave the account active: %+v", row)
	}
}

// TestPositiveBlipIgnoredDoesNotAdvanceThePendingCounter is design section
// 5.1 A7 and matrix row (d): the absence assertion, with its own positive
// anchor. positive_blip_ignored is not a matched evaluation and must never
// count toward the pending-reconciliation exit -- only writeEvaluation's
// status=="matched" gate stands between the two.
//
// The anchor that keeps this from being trivially true: two
// positive_blip_ignored rows must actually exist. Without them the counter
// would read zero because nothing happened, not because the gate held.
func TestPositiveBlipIgnoredDoesNotAdvanceThePendingCounter(t *testing.T) {
	store, ctx, sourceID, accountID, _, manifestHash, configHash, anchorAt, _ := newAutoReconcileFixture(t)
	negativeAt := enterAutoReconcilePending(t, store, ctx, sourceID, accountID, manifestHash, configHash, anchorAt)

	// Three checkpoints, each reporting a different surplus against the same
	// unchanged ledger (expected 100): every one disconfirms the one before,
	// so the first two are recorded positive_blip_ignored and the third is
	// left deferred.
	for i, spec := range []struct {
		id      string
		asOf    time.Time
		balance string
	}{
		{"blip-ignored-a", negativeAt.Add(10 * time.Minute), "150"},
		{"blip-ignored-b", negativeAt.Add(20 * time.Minute), "170"},
		{"blip-ignored-c", negativeAt.Add(30 * time.Minute), "190"},
	} {
		insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, spec.id,
			spec.asOf, spec.balance, int64(3+i), manifestHash, configHash)
	}
	evaluateSignedFixture(t, store, ctx, accountID, negativeAt.Add(31*time.Minute))

	// Positive anchor: the two ignored rows really are there.
	var ignored int
	if err := store.pool.QueryRow(ctx, `
		SELECT count(*) FROM balance_checkpoint_evaluations evaluation
		JOIN balance_reconciliation_checkpoints checkpoint ON checkpoint.id=evaluation.checkpoint_id
		WHERE checkpoint.external_account_id=$1 AND evaluation.evaluation_status='positive_blip_ignored'`,
		accountID).Scan(&ignored); err != nil {
		t.Fatal(err)
	}
	if ignored != 2 {
		t.Fatalf("positive_blip_ignored evaluations=%d, want 2 -- without them the counter assertion below is vacuous", ignored)
	}

	row := readPendingReconciliation(t, store, ctx, accountID)
	if row.status != "not_invoiceable_pending_reconciliation" || row.consecutiveMatches != 0 {
		t.Fatalf("ignored blips must neither advance the exit counter nor exit the state: %+v", row)
	}
	if got := f0AuditCount(t, store, ctx, "eligibility.pending_reconciliation.exited", accountID); got != 0 {
		t.Fatalf("exit audits=%d, want 0", got)
	}
}

func f0AuditCount(t *testing.T, store *Store, ctx context.Context, action, objectID string) int {
	t.Helper()
	var count int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action=$1 AND object_id=$2`, action, objectID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
