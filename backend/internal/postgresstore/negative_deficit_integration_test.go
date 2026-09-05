package postgresstore

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// XM-INV-NEGATIVE-DEFICIT (design docs/superpowers/specs/2026-09-06-xm-inv-
// negative-deficit-design.md): a negative upstream balance now arrives with
// its magnitude, and the evaluator compares that magnitude with the overdraw
// the projection itself still carries. Equal means the ledger and the source
// agree (matched, account stays active); anything else, or an unknown
// magnitude, keeps the XM-INV-ELIG-AUTO-RECONCILE treatment.

// overdrawnAccountFixture is TestUsageOverageRecordedWithoutFreezingAccount's
// own scenario: a ¥100-unit cash lot, 150 units of usage, hence 50 units the
// projection cannot allocate (non_invoiceable_overage_units=50) while the
// account stays active.
type overdrawnAccountFixture struct {
	store                     *Store
	ctx                       context.Context
	sourceID, accountID       string
	manifestHash, configHash  string
	anchorAt, emptyBalancesAt time.Time
	chain                     *v3TestChain
	worker                    AuditActor
}

func newOverdrawnAccountFixture(t *testing.T) overdrawnAccountFixture {
	t.Helper()
	store, ctx, sourceID, accountID, userID, manifestHash, configHash, anchorAt, chain := newAutoReconcileFixture(t)
	paymentAt := anchorAt.Add(5 * time.Minute)
	observeSimpleWalletCashLot(t, store, ctx, chain, sourceID, accountID, userID, manifestHash, configHash,
		"SUB2_BALANCE_1E8", "lot1", 100, paymentAt, 1)
	usageAt := anchorAt.Add(20 * time.Minute)
	observeSimpleUsageEvent(t, store, ctx, chain, sourceID, accountID, manifestHash, configHash,
		"deficit-overage-usage", usageAt, "150", 1)
	emptyBalancesAt := anchorAt.Add(25 * time.Minute)
	emptyBalancesCycle := chain.commit(t, store, ctx, sourceID, "balances", autoReconcileUUID('9', 870), emptyBalancesAt, nil)
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", emptyBalancesCycle)
	worker := AuditActor{Type: "system", ID: "test-worker"}
	fixture := overdrawnAccountFixture{store: store, ctx: ctx, sourceID: sourceID, accountID: accountID,
		manifestHash: manifestHash, configHash: configHash, anchorAt: anchorAt, emptyBalancesAt: emptyBalancesAt,
		chain: chain, worker: worker}
	fixture.project(t, emptyBalancesAt.Add(time.Minute))
	status, units, _ := readUsageOverage(t, store, ctx, accountID)
	if status != "active" || units != "50" {
		t.Fatalf("fixture: overage status=%q units=%s, want active/50", status, units)
	}
	return fixture
}

func (f overdrawnAccountFixture) project(t *testing.T, through time.Time) {
	t.Helper()
	if _, err := f.store.pool.Exec(f.ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now())`, f.accountID, through); err != nil {
		t.Fatal(err)
	}
	processed, err := f.store.ProcessEligibilityProjectionJobs(f.ctx, 10, time.Now().UTC().Add(time.Minute), f.worker)
	if err != nil || processed != 1 {
		t.Fatalf("projection processed=%d err=%v", processed, err)
	}
}

// observeNegativeCheckpoint mirrors observeSimpleCheckpoint for a checkpoint
// that reports a negative balance: zero units, balance_negative=true and,
// when deficit is non-nil, its magnitude.
func (f overdrawnAccountFixture) observeNegativeCheckpoint(t *testing.T, checkpointID string, asOf time.Time, deficit *string, sourceSequence int64) {
	t.Helper()
	event := SourceBatchEvent{EventID: autoReconcileUUID('8', 870+int(sourceSequence)),
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("deficit-checkpoint-" + checkpointID),
		PayloadCiphertext: bytes.Repeat([]byte{byte(90 + sourceSequence)}, 32), ObservedAt: asOf}
	cycle := f.chain.commit(t, f.store, f.ctx, f.sourceID, "balances", autoReconcileUUID('9', 870+int(sourceSequence)), asOf, []SourceBatchEvent{event})
	if err := f.store.ObserveBalanceCheckpoint(f.ctx, BalanceCheckpointObservation{
		SourceInstanceID: f.sourceID, ExternalUserID: "800", ExternalEventID: event.EventID,
		CheckpointID: checkpointID, CheckpointKind: "reconciliation", BaselineMember: false,
		BalanceServiceUnits: "0", BalanceNegative: true, DeficitServiceUnits: deficit,
		UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(cycle.cycleID),
		SnapshotRowCount: "1", AsOf: asOf, ObservedAt: asOf, StreamWatermarkAt: asOf,
		SourceCursor: "balance:" + checkpointID, SourceRevision: event.PayloadHash,
		CutoverManifestHash: f.manifestHash, ConfigurationHash: f.configHash, SourceSequence: sourceSequence,
		BatchID: cycle.batchID, ScanCycleID: cycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: f.sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, f.store, f.ctx, f.sourceID, "balances", cycle)
}

func (f overdrawnAccountFixture) checkpointEvaluation(t *testing.T, checkpointID string) (status, difference, storedDeficit string) {
	t.Helper()
	var deficit *string
	if err := f.store.pool.QueryRow(f.ctx, `
		SELECT evaluation.evaluation_status,evaluation.difference_service_units::text,checkpoint.deficit_service_units::text
		FROM balance_reconciliation_checkpoints checkpoint
		JOIN balance_checkpoint_evaluations evaluation ON evaluation.checkpoint_id=checkpoint.id
		WHERE checkpoint.external_account_id=$1 AND checkpoint.checkpoint_id=$2`,
		f.accountID, checkpointID).Scan(&status, &difference, &deficit); err != nil {
		t.Fatalf("evaluation for %s: %v", checkpointID, err)
	}
	if deficit != nil {
		storedDeficit = *deficit
	} else {
		storedDeficit = "<null>"
	}
	return status, difference, storedDeficit
}

func (f overdrawnAccountFixture) pendingEnteredCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.pending_reconciliation.entered' AND object_id=$1`, f.accountID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func stringPtr(value string) *string { return &value }

func TestExplainedNegativeBalanceEvaluatesMatchedAndKeepsTheAccountActive(t *testing.T) {
	f := newOverdrawnAccountFixture(t)
	negativeAt := f.anchorAt.Add(30 * time.Minute)
	f.observeNegativeCheckpoint(t, "deficit-explained", negativeAt, stringPtr("50"), 4)
	f.project(t, negativeAt.Add(time.Minute))
	status, difference, stored := f.checkpointEvaluation(t, "deficit-explained")
	if status != "matched" || difference != "0" || stored != "50" {
		t.Fatalf("explained overdraw: status=%q difference=%s deficit=%s, want matched/0/50", status, difference, stored)
	}
	row := readPendingReconciliation(t, f.store, f.ctx, f.accountID)
	if row.status != "active" || row.reason != "" {
		t.Fatalf("an explained negative balance must not park the account: %+v", row)
	}
	if entered := f.pendingEnteredCount(t); entered != 0 {
		t.Fatalf("pending_reconciliation.entered audits=%d, want 0", entered)
	}
	if count := openFreezeCount(t, f.store, f.ctx, f.accountID); count != 0 {
		t.Fatalf("open freezes=%d, want 0", count)
	}
	accountStatus, units, _ := readUsageOverage(t, f.store, f.ctx, f.accountID)
	if accountStatus != "active" || units != "50" {
		t.Fatalf("overage after the explained checkpoint status=%q units=%s, want active/50 (unchanged)", accountStatus, units)
	}
}

func TestLargerReportedDeficitEntersPendingWithSignedDetail(t *testing.T) {
	f := newOverdrawnAccountFixture(t)
	negativeAt := f.anchorAt.Add(30 * time.Minute)
	f.observeNegativeCheckpoint(t, "deficit-larger", negativeAt, stringPtr("60"), 4)
	f.project(t, negativeAt.Add(time.Minute))
	status, difference, stored := f.checkpointEvaluation(t, "deficit-larger")
	if status != "negative_frozen" || difference != "-10" || stored != "60" {
		t.Fatalf("larger deficit: status=%q difference=%s deficit=%s, want negative_frozen/-10/60", status, difference, stored)
	}
	row := readPendingReconciliation(t, f.store, f.ctx, f.accountID)
	if row.status != "not_invoiceable_pending_reconciliation" || row.reason != "UNKNOWN_NEGATIVE_BALANCE" ||
		row.triggerID != "deficit-larger" || !strings.Contains(row.detail, "reported balance -60, expected -50 (difference -10)") {
		t.Fatalf("larger deficit must park the account with the signed numbers in detail: %+v", row)
	}
	if count := openFreezeCount(t, f.store, f.ctx, f.accountID); count != 0 {
		t.Fatalf("open freezes=%d, want 0 (never a manual freeze)", count)
	}
}

func TestSmallerReportedDeficitAlsoEntersPending(t *testing.T) {
	f := newOverdrawnAccountFixture(t)
	negativeAt := f.anchorAt.Add(30 * time.Minute)
	f.observeNegativeCheckpoint(t, "deficit-smaller", negativeAt, stringPtr("40"), 4)
	f.project(t, negativeAt.Add(time.Minute))
	status, difference, _ := f.checkpointEvaluation(t, "deficit-smaller")
	if status != "negative_frozen" || difference != "10" {
		t.Fatalf("smaller deficit (the source owes less than the ledger explains): status=%q difference=%s, want negative_frozen/10", status, difference)
	}
	row := readPendingReconciliation(t, f.store, f.ctx, f.accountID)
	if row.status != "not_invoiceable_pending_reconciliation" ||
		!strings.Contains(row.detail, "reported balance -40, expected -50 (difference 10)") {
		t.Fatalf("smaller deficit must park the account, deliberately not synthesising a credit: %+v", row)
	}
}

func TestUnknownDeficitKeepsTheHistoricalTreatment(t *testing.T) {
	f := newOverdrawnAccountFixture(t)
	negativeAt := f.anchorAt.Add(30 * time.Minute)
	f.observeNegativeCheckpoint(t, "deficit-unknown", negativeAt, nil, 4)
	f.project(t, negativeAt.Add(time.Minute))
	status, _, stored := f.checkpointEvaluation(t, "deficit-unknown")
	if status != "negative_frozen" || stored != "<null>" {
		t.Fatalf("unknown deficit: status=%q deficit=%s, want negative_frozen/<null>", status, stored)
	}
	row := readPendingReconciliation(t, f.store, f.ctx, f.accountID)
	if row.status != "not_invoiceable_pending_reconciliation" || !strings.Contains(row.detail, "unknown magnitude") {
		t.Fatalf("evidence without a magnitude must keep parking the account and say so: %+v", row)
	}
}

func TestObserveBalanceCheckpointRejectsADeficitThatContradictsTheFlag(t *testing.T) {
	f := newOverdrawnAccountFixture(t)
	negativeAt := f.anchorAt.Add(30 * time.Minute)
	event := SourceBatchEvent{EventID: autoReconcileUUID('8', 899),
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("deficit-contradiction"),
		PayloadCiphertext: bytes.Repeat([]byte{99}, 32), ObservedAt: negativeAt}
	cycle := f.chain.commit(t, f.store, f.ctx, f.sourceID, "balances", autoReconcileUUID('9', 899), negativeAt, []SourceBatchEvent{event})
	base := BalanceCheckpointObservation{
		SourceInstanceID: f.sourceID, ExternalUserID: "800", ExternalEventID: event.EventID,
		CheckpointID: "deficit-contradiction", CheckpointKind: "reconciliation",
		BalanceServiceUnits: "0", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(cycle.cycleID),
		SnapshotRowCount: "1", AsOf: negativeAt, ObservedAt: negativeAt, StreamWatermarkAt: negativeAt,
		SourceCursor: "balance:deficit-contradiction", SourceRevision: event.PayloadHash,
		CutoverManifestHash: f.manifestHash, ConfigurationHash: f.configHash, SourceSequence: 4,
		BatchID: cycle.batchID, ScanCycleID: cycle.cycleID,
	}
	actor := AuditActor{Type: "source_connector", ID: f.sourceID}
	flagged := base
	flagged.BalanceNegative, flagged.DeficitServiceUnits = true, stringPtr("0")
	if err := f.store.ObserveBalanceCheckpoint(f.ctx, flagged, actor); err == nil {
		t.Fatal("balance_negative=true with a zero deficit must be rejected")
	}
	unflagged := base
	unflagged.BalanceNegative, unflagged.DeficitServiceUnits = false, stringPtr("7")
	if err := f.store.ObserveBalanceCheckpoint(f.ctx, unflagged, actor); err == nil {
		t.Fatal("a positive deficit without balance_negative must be rejected")
	}
	malformed := base
	malformed.BalanceNegative, malformed.DeficitServiceUnits = true, stringPtr("-7")
	if err := f.store.ObserveBalanceCheckpoint(f.ctx, malformed, actor); err == nil {
		t.Fatal("a malformed deficit must be rejected")
	}
}

// TestCarryForwardProofRestatesTheDeficit: a later usage fact needs balance
// coverage, and the delta snapshot omits the account (claiming it unchanged
// at -50), so ensureBalanceCarryForwardProofTx derives a carry-forward proof.
// That proof must restate the prior checkpoint's deficit (the contract
// trigger refuses anything else) and is evaluated with it: the ledger now
// expects -60, the restated -50 is a genuine gap, so the proof parks the
// account with the signed numbers in detail -- exactly what a real checkpoint
// carrying the same stale magnitude would have done.
func TestCarryForwardProofRestatesTheDeficit(t *testing.T) {
	f := newOverdrawnAccountFixture(t)
	negativeAt := f.anchorAt.Add(30 * time.Minute)
	f.observeNegativeCheckpoint(t, "deficit-explained", negativeAt, stringPtr("50"), 4)
	f.project(t, negativeAt.Add(time.Minute))
	moreUsageAt := f.anchorAt.Add(35 * time.Minute)
	observeSimpleUsageEvent(t, f.store, f.ctx, f.chain, f.sourceID, f.accountID, f.manifestHash, f.configHash,
		"deficit-more-usage", moreUsageAt, "10", 2)
	carryAt := f.anchorAt.Add(40 * time.Minute)
	carryCycle := f.chain.commit(t, f.store, f.ctx, f.sourceID, "balances", autoReconcileUUID('9', 880), carryAt, nil)
	markV3CycleProcessed(t, f.store, f.ctx, f.sourceID, "balances", carryCycle)
	f.project(t, carryAt.Add(time.Minute))
	var proofID, proofDeficit, proofStatus, proofDifference string
	var proofNegative bool
	if err := f.store.pool.QueryRow(f.ctx, `
		SELECT proof.id::text,proof.deficit_service_units::text,proof.balance_negative,
			evaluation.evaluation_status,evaluation.difference_service_units::text
		FROM balance_carry_forward_proofs proof
		JOIN balance_carry_forward_evaluations evaluation ON evaluation.proof_id=proof.id
		WHERE proof.external_account_id=$1 AND proof.scan_cycle_id=$2::uuid`,
		f.accountID, carryCycle.cycleID).Scan(&proofID, &proofDeficit, &proofNegative, &proofStatus, &proofDifference); err != nil {
		t.Fatalf("carry-forward proof for the omitted negative account: %v", err)
	}
	if proofDeficit != "50" || !proofNegative {
		t.Fatalf("proof must restate the prior deficit: deficit=%s negative=%v, want 50/true", proofDeficit, proofNegative)
	}
	if proofStatus != "negative_frozen" || proofDifference != "10" {
		t.Fatalf("proof evaluation status=%s difference=%s, want negative_frozen/10 (restated -50 against an expected -60)", proofStatus, proofDifference)
	}
	row := readPendingReconciliation(t, f.store, f.ctx, f.accountID)
	if row.status != "not_invoiceable_pending_reconciliation" || row.triggerType != "balance_carry_forward_proof" ||
		!strings.Contains(row.detail, "reported balance -50, expected -60 (difference 10)") {
		t.Fatalf("stale carried magnitude must park the account with signed detail: %+v", row)
	}
	// The overage columns keep naming the oldest outstanding debt (50); the
	// evaluator compared against every unallocated unit (50+10=60), which is
	// what the -60 in the detail above proves.
	if status, units, _ := readUsageOverage(t, f.store, f.ctx, f.accountID); units != "50" || status != "not_invoiceable_pending_reconciliation" {
		t.Fatalf("overage columns after the extra usage status=%q units=%s, want pending/50 (oldest debt)", status, units)
	}
	// The contract trigger: a proof restating anything but the prior's deficit
	// is refused before the unique index can even reject the duplicate cycle.
	_, err := f.store.pool.Exec(f.ctx, `
		INSERT INTO balance_carry_forward_proofs(
			id,source_instance_id,external_account_id,proof_key,prior_checkpoint_id,
			scan_cycle_id,final_batch_id,as_of,balance_service_units,balance_negative,
			baseline_member,source_snapshot_id,snapshot_row_count,source_sequence,
			source_cursor,stream_watermark_at,source_revision_hash,observed_at,deficit_service_units)
		SELECT $2::uuid,source_instance_id,external_account_id,proof_key,prior_checkpoint_id,
			scan_cycle_id,final_batch_id,as_of,balance_service_units,balance_negative,
			baseline_member,source_snapshot_id,snapshot_row_count,source_sequence,
			source_cursor,stream_watermark_at,source_revision_hash,observed_at,NULL
		FROM balance_carry_forward_proofs WHERE id=$1::uuid`, proofID, randomUUID())
	if err == nil || !strings.Contains(err.Error(), "prior actual is invalid") {
		t.Fatalf("a proof dropping the prior's deficit must be refused by the contract trigger, got %v", err)
	}
}

// TestExplainedNegativeBalanceLeavesOtherAccountsUntouched is the
// account-level isolation check the evaluator-change discipline requires:
// a second account in the same source, bootstrapped and reconciled, keeps
// byte-identical eligibility state and evaluations while the first account's
// explained negative checkpoint is processed.
func TestExplainedNegativeBalanceLeavesOtherAccountsUntouched(t *testing.T) {
	f := newOverdrawnAccountFixture(t)
	otherUser := autoReconcileUUID('2', 901)
	otherAccount := autoReconcileUUID('3', 901)
	if _, err := f.store.pool.Exec(f.ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','deficit-isolation-user')`, otherUser); err != nil {
		t.Fatal(err)
	}
	// external_subject_hmac: the unique constraint is NULLS NOT DISTINCT, so a
	// second account in the same source needs its own value.
	if _, err := f.store.pool.Exec(f.ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
		VALUES($1,$2,$3,'901','deficit-isolation-hmac','test','verified')`, otherAccount, otherUser, f.sourceID); err != nil {
		t.Fatal(err)
	}
	// Anchored after every cycle the fixture already published, so the
	// account bootstraps against a current balances stream (no gap).
	otherAnchorAt := f.emptyBalancesAt.Add(2 * time.Minute)
	otherEvent := SourceBatchEvent{EventID: autoReconcileUUID('8', 901),
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("deficit-isolation-anchor"),
		PayloadCiphertext: bytes.Repeat([]byte{7}, 32), ObservedAt: otherAnchorAt}
	otherCycle := f.chain.commit(t, f.store, f.ctx, f.sourceID, "balances", autoReconcileUUID('9', 901), otherAnchorAt, []SourceBatchEvent{otherEvent})
	if err := f.store.ObserveBalanceCheckpoint(f.ctx, BalanceCheckpointObservation{
		SourceInstanceID: f.sourceID, ExternalUserID: "901", ExternalEventID: otherEvent.EventID,
		CheckpointID: "deficit-isolation-anchor", CheckpointKind: "reconciliation", BaselineMember: true,
		BalanceServiceUnits: "500", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(otherCycle.cycleID),
		SnapshotRowCount: "1", AsOf: otherAnchorAt, ObservedAt: otherAnchorAt, StreamWatermarkAt: otherAnchorAt,
		SourceCursor: "balance:901:anchor", SourceRevision: otherEvent.PayloadHash,
		CutoverManifestHash: f.manifestHash, ConfigurationHash: f.configHash, SourceSequence: 1,
		BatchID: otherCycle.batchID, ScanCycleID: otherCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: f.sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, f.store, f.ctx, f.sourceID, "balances", otherCycle)
	if _, err := f.store.pool.Exec(f.ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now())`, otherAccount, otherAnchorAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if processed, err := f.store.ProcessEligibilityProjectionJobs(f.ctx, 10, time.Now().UTC().Add(time.Minute), f.worker); err != nil || processed != 1 {
		t.Fatalf("other account bootstrap processed=%d err=%v", processed, err)
	}
	snapshot := func() string {
		var state, evaluations string
		if err := f.store.pool.QueryRow(f.ctx, `
			SELECT to_jsonb(state)-'updated_at' FROM source_account_eligibility_state state WHERE external_account_id=$1`,
			otherAccount).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if err := f.store.pool.QueryRow(f.ctx, `
			SELECT COALESCE(jsonb_agg(to_jsonb(evaluation) ORDER BY evaluation.id),'[]'::jsonb)::text
			FROM balance_checkpoint_evaluations evaluation
			JOIN balance_reconciliation_checkpoints checkpoint ON checkpoint.id=evaluation.checkpoint_id
			WHERE checkpoint.external_account_id=$1`, otherAccount).Scan(&evaluations); err != nil {
			t.Fatal(err)
		}
		return state + "\n" + evaluations
	}
	before := snapshot()
	if !strings.Contains(before, `"eligibility_status": "active"`) || !strings.Contains(before, `"matched"`) {
		t.Fatalf("other account must be active and reconciled before the scenario: %s", before)
	}
	negativeAt := f.anchorAt.Add(30 * time.Minute)
	f.observeNegativeCheckpoint(t, "deficit-explained", negativeAt, stringPtr("50"), 4)
	f.project(t, negativeAt.Add(time.Minute))
	if status, _, _ := f.checkpointEvaluation(t, "deficit-explained"); status != "matched" {
		t.Fatalf("scenario account evaluation=%q, want matched", status)
	}
	if after := snapshot(); after != before {
		t.Fatalf("the other account changed while an explained negative balance was evaluated:\nbefore=%s\nafter=%s", before, after)
	}
}
