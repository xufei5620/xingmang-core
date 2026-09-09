package postgresstore

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// XM-INV-PENDING-RECON C1+C2 (design docs/handoffs/XM-INV-PENDING-RECON-DESIGN.md
// section 3, acceptance ruling 2026-09-09 D1(a)/D2(a)/D8(a)): an account
// parked in not_invoiceable_pending_reconciliation one matched evaluation
// short of auto-exit, with nothing else happening to it, used to wait
// forever. It produces no checkpoint (the agent only emits balance rows whose
// units, negative flag or deficit changed) and no carry-forward proof (no
// facts in the window means no visibility to derive one at), while every
// finalization pass advanced finalized_through past the published balances
// cycles a proof could still have been derived from.
//
// C1 enqueues a projection job for exactly those accounts instead of empty
// advancing; C2 derives one idle proof from the newest published balances
// cycle that can still take one. Nothing about the exit rule itself changes:
// the proof is evaluated by the ordinary evaluator and counts as one item.
//
// Timing convention in this file: every scan cycle these tests publish is at
// least idleCycleSpacing apart, and runFinalize sets all four stream
// watermarks to the requested window plus the account's finalization delay
// (900s). Spacing wider than that delay is what keeps a later cycle's own
// commit from moving the balances watermark backwards -- which the receiver
// correctly treats as a STREAM_WATERMARK_REGRESSION and freezes on.
const idleCycleSpacing = 20 * time.Minute

// idlePendingFixture is an overdrawnAccountFixture driven to
// not_invoiceable_pending_reconciliation with
// pending_reconciliation_consecutive_matches at exactly
// pendingReconciliationExitMatches-1 -- production account shape, reached
// only through real code paths (two negative checkpoints, the first with a
// magnitude the ledger cannot explain, the second with one it can).
type idlePendingFixture struct {
	overdrawnAccountFixture
	// lastEvidenceAt is the as_of of the checkpoint that took the account to
	// N-1 matches; later cycles in these tests are dated after it.
	lastEvidenceAt time.Time
}

func newIdlePendingFixture(t *testing.T) *idlePendingFixture {
	t.Helper()
	f := newOverdrawnAccountFixture(t)
	// The ledger carries 50 unallocated units, so it expects a signed -50. A
	// reported -60 is a genuine gap: negative_frozen, into pending, streak 0.
	enterAt := f.anchorAt.Add(30 * time.Minute)
	f.observeNegativeCheckpoint(t, "idle-enter", enterAt, stringPtr("60"), 4)
	f.project(t, enterAt.Add(time.Minute))
	if row := readPendingReconciliation(t, f.store, f.ctx, f.accountID); row.status != "not_invoiceable_pending_reconciliation" ||
		row.consecutiveMatches != 0 {
		t.Fatalf("fixture: account did not enter pending reconciliation: %+v", row)
	}
	// A reported -50 is exactly what the ledger expects: matched, streak 1.
	matchAt := f.anchorAt.Add(35 * time.Minute)
	f.observeNegativeCheckpoint(t, "idle-match1", matchAt, stringPtr("50"), 5)
	f.project(t, matchAt.Add(time.Minute))
	row := readPendingReconciliation(t, f.store, f.ctx, f.accountID)
	if row.status != "not_invoiceable_pending_reconciliation" || row.consecutiveMatches != pendingReconciliationExitMatches-1 {
		t.Fatalf("fixture: want pending at %d matches, got %+v", pendingReconciliationExitMatches-1, row)
	}
	return &idlePendingFixture{overdrawnAccountFixture: f, lastEvidenceAt: matchAt}
}

// publishEmptyBalancesCycle commits and publishes a balances scan cycle that
// carries no events at all -- the delta shape a quiet account produces, where
// the agent reports "nothing in this stream changed".
func (f *idlePendingFixture) publishEmptyBalancesCycle(t *testing.T, seed int, at time.Time) v3TestCycle {
	t.Helper()
	cycle := f.chain.commit(t, f.store, f.ctx, f.sourceID, "balances", autoReconcileUUID('9', seed), at, nil)
	markV3CycleProcessed(t, f.store, f.ctx, f.sourceID, "balances", cycle)
	var status string
	if err := f.store.pool.QueryRow(f.ctx, `SELECT cycle_status FROM source_economic_scan_cycles
		WHERE source_instance_id=$1 AND stream_id='balances' AND scan_cycle_id=$2::uuid`,
		f.sourceID, cycle.cycleID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "published" {
		t.Fatalf("fixture: balances cycle at %s is %q, want published", at.UTC(), status)
	}
	return cycle
}

// runFinalize advances all four stream watermarks so the pass asks for
// exactly `through`, then runs one finalization pass -- the same call
// tryPublishEconomicScanCyclesTx makes after it publishes a cycle. Setting
// the watermarks outright (rather than committing a cycle in each of the four
// streams) keeps requested_through a value the test states instead of one it
// has to infer.
func (f *idlePendingFixture) runFinalize(t *testing.T, through time.Time) {
	t.Helper()
	watermark := through.Add(f.finalizationDelay(t))
	tx, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(f.ctx, `INSERT INTO source_economic_stream_watermarks(
		source_instance_id,stream_kind,watermark_at,source_sequence,source_cursor,configuration_hash)
		VALUES($1,'payments',$2,1,'c',$3),($1,'usage',$2,1,'c',$3),
			($1,'credits',$2,1,'c',$3),($1,'balances',$2,1,'c',$3)
		ON CONFLICT(source_instance_id,stream_kind) DO UPDATE SET
			watermark_at=GREATEST(source_economic_stream_watermarks.watermark_at,EXCLUDED.watermark_at)`,
		f.sourceID, watermark, f.configHash); err != nil {
		t.Fatal(err)
	}
	if err = finalizeSourceAccountsTx(f.ctx, tx, f.sourceID, AuditActor{Type: "system", ID: "pending-recon-test"}); err != nil {
		t.Fatalf("finalization pass: %v", err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
}

func (f *idlePendingFixture) finalizationDelay(t *testing.T) time.Duration {
	t.Helper()
	var delaySeconds int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT finalization_delay_seconds
		FROM source_account_eligibility_state WHERE external_account_id=$1`, f.accountID).Scan(&delaySeconds); err != nil {
		t.Fatal(err)
	}
	return time.Duration(delaySeconds) * time.Second
}

func (f *idlePendingFixture) finalizedThrough(t *testing.T) time.Time {
	t.Helper()
	var finalized time.Time
	if err := f.store.pool.QueryRow(f.ctx, `SELECT finalized_through
		FROM source_account_eligibility_state WHERE external_account_id=$1`, f.accountID).Scan(&finalized); err != nil {
		t.Fatal(err)
	}
	return finalized.UTC()
}

func (f *idlePendingFixture) jobStatus(t *testing.T) (status string, exists bool) {
	t.Helper()
	err := f.store.pool.QueryRow(f.ctx, `SELECT status FROM eligibility_projection_jobs
		WHERE external_account_id=$1`, f.accountID).Scan(&status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false
		}
		t.Fatal(err)
	}
	return status, true
}

func (f *idlePendingFixture) proofCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM balance_carry_forward_proofs
		WHERE external_account_id=$1`, f.accountID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// derivedAuditCount counts derivation audit rows belonging to this account's
// own proofs (audit object ids are proof ids, so the join is what scopes it).
func (f *idlePendingFixture) derivedAuditCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_events ae
		JOIN balance_carry_forward_proofs proof ON proof.id::text=ae.object_id
		WHERE ae.action='eligibility.balance_carry_forward.derived' AND proof.external_account_id=$1`,
		f.accountID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f *idlePendingFixture) auditCount(t *testing.T, action, objectID string) int {
	t.Helper()
	var count int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_events
		WHERE action=$1 AND object_id=$2`, action, objectID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// processJobs drains the projection queue the way the runtime worker does.
func (f *idlePendingFixture) processJobs(t *testing.T) int {
	t.Helper()
	processed, err := f.store.ProcessEligibilityProjectionJobs(f.ctx, 25, time.Now().UTC().Add(time.Minute), f.worker)
	if err != nil {
		t.Fatalf("process projection jobs: %v", err)
	}
	return processed
}

// TestIdlePendingAccountExitsOnTheNextPublishedBalancesCycle is design
// section 5.1 A1 and matrix row (a): the whole point of the slice. An idle
// account at N-1 matches, with a balances cycle published in the new
// finalization window and no facts of any kind in it, gets a job (C1), one
// idle carry-forward proof (C2), one matched evaluation, and auto-exits.
func TestIdlePendingAccountExitsOnTheNextPublishedBalancesCycle(t *testing.T) {
	f := newIdlePendingFixture(t)
	before := f.finalizedThrough(t)
	proofsBefore := f.proofCount(t)

	carryAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	cycle := f.publishEmptyBalancesCycle(t, 881, carryAt)
	if !carryAt.After(before) {
		t.Fatalf("fixture: the new cycle must land above finalized_through (%s vs %s)", carryAt, before)
	}
	f.runFinalize(t, carryAt.Add(time.Minute))

	// C1: the pass enqueued the account instead of empty advancing past the
	// cycle. Both halves are asserted -- the job exists, and finalized_through
	// did not move -- so this cannot pass on the job row alone.
	status, exists := f.jobStatus(t)
	if !exists || status != "queued" {
		t.Fatalf("idle pending account at N-1 was not enqueued: exists=%v status=%q", exists, status)
	}
	if now := f.finalizedThrough(t); !now.Equal(before) {
		t.Fatalf("finalized_through empty advanced past the cycle anyway: %s (was %s)", now, before)
	}

	if processed := f.processJobs(t); processed != 1 {
		t.Fatalf("projection jobs processed=%d, want 1", processed)
	}

	// C2: exactly one new proof, at that cycle, restating the prior checkpoint.
	var proofID, proofDeficit, proofPrior, proofRevision, evaluationStatus, difference string
	var proofAsOf time.Time
	if err := f.store.pool.QueryRow(f.ctx, `
		SELECT proof.id::text,proof.deficit_service_units::text,proof.prior_checkpoint_id::text,
			proof.source_revision_hash,proof.as_of,
			evaluation.evaluation_status,evaluation.difference_service_units::text
		FROM balance_carry_forward_proofs proof
		JOIN balance_carry_forward_evaluations evaluation ON evaluation.proof_id=proof.id
		WHERE proof.external_account_id=$1 AND proof.scan_cycle_id=$2::uuid`,
		f.accountID, cycle.cycleID).Scan(&proofID, &proofDeficit, &proofPrior, &proofRevision, &proofAsOf,
		&evaluationStatus, &difference); err != nil {
		t.Fatalf("idle carry-forward proof for the quiet pending account: %v", err)
	}
	if got := f.proofCount(t); got != proofsBefore+1 {
		t.Fatalf("idle re-evaluation derived %d proofs in total, want %d (exactly one new)", got, proofsBefore+1)
	}
	if proofDeficit != "50" || !proofAsOf.UTC().Equal(carryAt.UTC()) {
		t.Fatalf("proof must restate the prior deficit at the cycle ceiling: deficit=%s as_of=%s (cycle %s)",
			proofDeficit, proofAsOf.UTC(), carryAt.UTC())
	}
	if evaluationStatus != "matched" || difference != "0" {
		t.Fatalf("idle proof evaluation status=%q difference=%s, want matched/0", evaluationStatus, difference)
	}

	// The derivation audit says which path wrote it. Only the payload hash is
	// stored, so the expected map is hashed here: this pins the exact
	// after-state, idle_reevaluation included, not merely that some audit row
	// with the right action exists.
	var afterHash string
	if err := f.store.pool.QueryRow(f.ctx, `SELECT COALESCE(after_hash,'') FROM audit_events
		WHERE action='eligibility.balance_carry_forward.derived' AND object_id=$1`, proofID).Scan(&afterHash); err != nil {
		t.Fatalf("derivation audit for the idle proof: %v", err)
	}
	wantHash := stateHash(map[string]any{
		"proof_key":           "carry-forward:" + cycle.cycleID + ":" + f.accountID,
		"scan_cycle_id":       cycle.cycleID,
		"prior_checkpoint_id": proofPrior,
		"source_revision":     proofRevision,
		"idle_reevaluation":   true,
	})
	if afterHash != wantHash {
		t.Fatalf("idle derivation audit after-state hash=%s, want %s (idle_reevaluation:true)", afterHash, wantHash)
	}

	// A1's seven state columns, all asserted together.
	row := readPendingReconciliation(t, f.store, f.ctx, f.accountID)
	if row.status != "active" || row.reason != "" || row.triggerType != "" || row.triggerID != "" ||
		row.detail != "" || row.sinceSet || row.consecutiveMatches != 0 {
		t.Fatalf("idle re-evaluation must return the account to active with every pending column cleared: %+v", row)
	}
	if got := f.auditCount(t, "eligibility.pending_reconciliation.exited", f.accountID); got != 1 {
		t.Fatalf("exit audits=%d, want exactly 1", got)
	}
	if count := openFreezeCount(t, f.store, f.ctx, f.accountID); count != 0 {
		t.Fatalf("open freezes=%d, want 0 (the state is self-clearing, never a manual freeze)", count)
	}
	health, err := f.store.EligibilityProjectionHealth(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.Queued != 0 || health.Dead != 0 || health.Processing != 0 || health.ProofPending != 0 || health.Retrying != 0 {
		t.Fatalf("projection health must be back to zero after the worker drains: %+v", health)
	}
}

// TestIdlePendingAccountWithNoStreakIsLeftToEmptyAdvance is design section
// 5.1 A2 and matrix row (b): the trigger is deliberately narrow. An account
// with consecutive_matches=0 is not enqueued, derives nothing, and is empty
// advanced exactly as before -- and both positive anchors (finalized_through
// actually moved, and it landed on the requested window) are asserted, so
// this cannot pass merely because nothing ran at all.
func TestIdlePendingAccountWithNoStreakIsLeftToEmptyAdvance(t *testing.T) {
	f := newIdlePendingFixture(t)
	// Reset the streak the way a fresh unexplained negative item does.
	resetAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	f.observeNegativeCheckpoint(t, "idle-reset", resetAt, stringPtr("70"), 6)
	f.project(t, resetAt.Add(time.Minute))
	row := readPendingReconciliation(t, f.store, f.ctx, f.accountID)
	if row.status != "not_invoiceable_pending_reconciliation" || row.consecutiveMatches != 0 {
		t.Fatalf("fixture: want pending with a reset streak, got %+v", row)
	}
	before := f.finalizedThrough(t)
	proofsBefore := f.proofCount(t)

	carryAt := resetAt.Add(idleCycleSpacing)
	f.publishEmptyBalancesCycle(t, 882, carryAt)
	requested := carryAt.Add(time.Minute)
	f.runFinalize(t, requested)

	if _, exists := f.jobStatus(t); exists {
		t.Fatal("an account with no match streak must not be enqueued by the idle branch")
	}
	// Positive anchor 1: finalized_through really did advance, so the empty
	// advance path was taken (not "nothing happened at all").
	after := f.finalizedThrough(t)
	if !after.After(before) {
		t.Fatalf("finalized_through did not empty advance: %s (was %s)", after, before)
	}
	// Positive anchor 2: it landed exactly on the requested window.
	if !after.Equal(requested.UTC()) {
		t.Fatalf("empty advance landed at %s, want the requested window %s", after, requested.UTC())
	}
	if got := f.proofCount(t); got != proofsBefore {
		t.Fatalf("carry-forward proofs=%d, want %d (none derived for a zero-streak account)", got, proofsBefore)
	}
	if row := readPendingReconciliation(t, f.store, f.ctx, f.accountID); row.status != "not_invoiceable_pending_reconciliation" ||
		row.consecutiveMatches != 0 {
		t.Fatalf("zero-streak account must stay exactly where it was: %+v", row)
	}
}

// TestIdlePendingDerivationIsSuppressedByAnOpenFreeze is design section 5.1
// A3 and matrix row (g): C1 may enqueue the account, but C2 refuses to derive
// while a real freeze is open. advancePendingReconciliationMatchTx would
// refuse the exit anyway, and a proof is immutable -- deriving one would burn
// its cycle's exclusivity to produce evidence nobody can act on.
func TestIdlePendingDerivationIsSuppressedByAnOpenFreeze(t *testing.T) {
	f := newIdlePendingFixture(t)
	if _, err := f.store.pool.Exec(f.ctx, `INSERT INTO eligibility_freezes(
		id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id)
		VALUES($1,$2,'EVENT_PAYLOAD_DRIFT','source_stream','balances')`,
		autoReconcileUUID('6', 883), f.accountID); err != nil {
		t.Fatal(err)
	}
	proofsBefore := f.proofCount(t)

	carryAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	f.publishEmptyBalancesCycle(t, 883, carryAt)
	f.runFinalize(t, carryAt.Add(time.Minute))
	if _, exists := f.jobStatus(t); !exists {
		t.Fatal("C1 enqueues on the account's own state; the freeze guard belongs to C2, not C1")
	}
	f.processJobs(t)

	if got := f.proofCount(t); got != proofsBefore {
		t.Fatalf("carry-forward proofs=%d, want %d (an open freeze suppresses idle derivation)", got, proofsBefore)
	}
	row := readPendingReconciliation(t, f.store, f.ctx, f.accountID)
	if row.status != "not_invoiceable_pending_reconciliation" || row.consecutiveMatches != pendingReconciliationExitMatches-1 {
		t.Fatalf("frozen pending account must keep its state and streak untouched: %+v", row)
	}

	// Control: with no open freeze left, the next published cycle derives and
	// the account exits -- so the assertion above is the freeze guard doing
	// its job, not the scenario being incapable of deriving.
	if _, err := f.store.pool.Exec(f.ctx, `DELETE FROM eligibility_freezes WHERE external_account_id=$1`, f.accountID); err != nil {
		t.Fatal(err)
	}
	nextAt := carryAt.Add(idleCycleSpacing)
	f.publishEmptyBalancesCycle(t, 884, nextAt)
	f.runFinalize(t, nextAt.Add(time.Minute))
	f.processJobs(t)
	if got := f.proofCount(t); got != proofsBefore+1 {
		t.Fatalf("after the freeze was gone, proofs=%d, want %d", got, proofsBefore+1)
	}
	if row := readPendingReconciliation(t, f.store, f.ctx, f.accountID); row.status != "active" {
		t.Fatalf("with no open freeze the idle account must exit: %+v", row)
	}
}

// TestIdlePendingDerivationSkipsACycleThatAlreadyHasARealCheckpoint is
// design matrix row (h), first arm: the idle branch picks the newest
// published cycle that can still take a proof, and a cycle carrying this
// account's own real checkpoint cannot -- migration 0014's mutually
// exclusive proof/checkpoint contract would reject one of the two.
func TestIdlePendingDerivationSkipsACycleThatAlreadyHasARealCheckpoint(t *testing.T) {
	f := newIdlePendingFixture(t)
	proofsBefore := f.proofCount(t)
	// The newest published balances cycle is the one carrying idle-match1's
	// own checkpoint, and no cycle above it exists yet. Ask for a window that
	// includes that cycle's ceiling.
	f.runFinalize(t, f.lastEvidenceAt.Add(2*time.Minute))
	f.processJobs(t)
	if got := f.proofCount(t); got != proofsBefore {
		t.Fatalf("proofs=%d, want %d: a cycle that already carries this account's real checkpoint must be skipped", got, proofsBefore)
	}

	// Control: publish one empty cycle above it and the same run derives, so
	// the assertion above is the has_real_checkpoint skip, not an inability
	// to derive in this fixture at all.
	carryAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	f.publishEmptyBalancesCycle(t, 885, carryAt)
	f.runFinalize(t, carryAt.Add(time.Minute))
	f.processJobs(t)
	if got := f.proofCount(t); got != proofsBefore+1 {
		t.Fatalf("control: proofs=%d, want %d", got, proofsBefore+1)
	}
}

// TestIdlePendingDerivationIsIdempotentAtTheSameCycle is design matrix row
// (h), second arm and A4's third clause: a second pass over the same cycle
// inserts nothing (ON CONFLICT DO NOTHING) and the consistency re-read
// passes, rather than raising a conflict or writing a second proof.
func TestIdlePendingDerivationIsIdempotentAtTheSameCycle(t *testing.T) {
	f := newIdlePendingFixture(t)
	proofsBefore := f.proofCount(t)
	windowStart := f.finalizedThrough(t)
	carryAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	f.publishEmptyBalancesCycle(t, 886, carryAt)
	f.runFinalize(t, carryAt.Add(time.Minute))
	f.processJobs(t)
	if got := f.proofCount(t); got != proofsBefore+1 {
		t.Fatalf("first pass proofs=%d, want %d", got, proofsBefore+1)
	}
	derivedBefore := f.derivedAuditCount(t)

	// Run the proof phase again over the identical window, as a requeued job
	// would. The account has exited by now, so drive the function directly
	// with the account state that was in force when the first pass ran.
	tx, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	account, err := getEligibilityAccountTx(f.ctx, tx, f.accountID, false)
	if err != nil {
		t.Fatal(err)
	}
	account.Status = "not_invoiceable_pending_reconciliation"
	account.FinalizedThrough = windowStart
	if err = ensureBalanceCarryForwardProofTx(f.ctx, tx, account, carryAt.Add(time.Minute), f.worker); err != nil {
		t.Fatalf("second idle derivation over the same cycle: %v", err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.proofCount(t); got != proofsBefore+1 {
		t.Fatalf("second pass proofs=%d, want %d (idempotent at the cycle key)", got, proofsBefore+1)
	}
	if got := f.derivedAuditCount(t); got != derivedBefore {
		t.Fatalf("second pass wrote %d extra derivation audits, want 0", got-derivedBefore)
	}
}

// TestRealCheckpointAfterAnIdleProofIsRefusedByTheExclusivityContract is
// design matrix row (i): pinning the known consequence of C2 for whoever
// reads this next. Once an idle proof asserts "no checkpoint arrived in this
// cycle", migration 0014 refuses a real checkpoint at that cycle forever.
// That is why the stranded-checkpoint wait (XM-INV-DEAD-CONTAINMENT A2) is
// evaluated before the idle branch derives anything.
func TestRealCheckpointAfterAnIdleProofIsRefusedByTheExclusivityContract(t *testing.T) {
	f := newIdlePendingFixture(t)
	proofsBefore := f.proofCount(t)
	carryAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	cycle := f.publishEmptyBalancesCycle(t, 887, carryAt)
	f.runFinalize(t, carryAt.Add(time.Minute))
	f.processJobs(t)
	if got := f.proofCount(t); got != proofsBefore+1 {
		t.Fatalf("proofs=%d, want %d", got, proofsBefore+1)
	}

	event := SourceBatchEvent{EventID: autoReconcileUUID('8', 887),
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("idle-late-checkpoint"),
		PayloadCiphertext: bytes.Repeat([]byte{7}, 32), ObservedAt: carryAt}
	err := f.store.ObserveBalanceCheckpoint(f.ctx, BalanceCheckpointObservation{
		SourceInstanceID: f.sourceID, ExternalUserID: "800", ExternalEventID: event.EventID,
		CheckpointID: "idle-late", CheckpointKind: "reconciliation",
		BalanceServiceUnits: "0", BalanceNegative: true, DeficitServiceUnits: stringPtr("50"),
		UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(cycle.cycleID),
		SnapshotRowCount: "1", AsOf: carryAt, ObservedAt: carryAt, StreamWatermarkAt: carryAt,
		SourceCursor: "balance:idle-late", SourceRevision: event.PayloadHash,
		CutoverManifestHash: f.manifestHash, ConfigurationHash: f.configHash, SourceSequence: 9,
		BatchID: cycle.batchID, ScanCycleID: cycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: f.sourceID})
	if err == nil {
		t.Fatal("a real checkpoint at a cycle that already carries a carry-forward proof must be refused")
	}
}

// TestIdlePendingDerivationLeavesOtherAccountsUntouched is design section
// 5.1 A9: the account-level isolation the evaluator-change discipline
// requires. A second, quiet account in the same source keeps byte-identical
// eligibility state and evaluations across the whole idle re-evaluation.
//
// finalized_through, projection_version and updated_at are excluded from the
// comparison and asserted separately: every finalization pass advances the
// window of every account in the source, so pinning them would be pinning
// ordinary progress, not isolation. What must not change is anything the
// evaluator writes -- the status, the five pending_reconciliation_* columns,
// the overage columns, the evaluations, and any proof.
func TestIdlePendingDerivationLeavesOtherAccountsUntouched(t *testing.T) {
	f := newIdlePendingFixture(t)
	otherUser := autoReconcileUUID('2', 905)
	otherAccount := autoReconcileUUID('3', 905)
	if _, err := f.store.pool.Exec(f.ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','idle-isolation-user')`, otherUser); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.pool.Exec(f.ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
		VALUES($1,$2,$3,'905','idle-isolation-hmac','test','verified')`, otherAccount, otherUser, f.sourceID); err != nil {
		t.Fatal(err)
	}
	anchorAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	anchorEvent := SourceBatchEvent{EventID: autoReconcileUUID('8', 905),
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("idle-isolation-anchor"),
		PayloadCiphertext: bytes.Repeat([]byte{11}, 32), ObservedAt: anchorAt}
	anchorCycle := f.chain.commit(t, f.store, f.ctx, f.sourceID, "balances", autoReconcileUUID('9', 905),
		anchorAt, []SourceBatchEvent{anchorEvent})
	if err := f.store.ObserveBalanceCheckpoint(f.ctx, BalanceCheckpointObservation{
		SourceInstanceID: f.sourceID, ExternalUserID: "905", ExternalEventID: anchorEvent.EventID,
		CheckpointID: "idle-isolation-anchor", CheckpointKind: "reconciliation", BaselineMember: true,
		BalanceServiceUnits: "0", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(anchorCycle.cycleID),
		SnapshotRowCount: "1", AsOf: anchorAt, ObservedAt: anchorAt, StreamWatermarkAt: anchorAt,
		SourceCursor: "balance:idle-isolation-anchor", SourceRevision: anchorEvent.PayloadHash,
		CutoverManifestHash: f.manifestHash, ConfigurationHash: f.configHash, SourceSequence: 7,
		BatchID: anchorCycle.batchID, ScanCycleID: anchorCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: f.sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, f.store, f.ctx, f.sourceID, "balances", anchorCycle)
	if _, err := f.store.pool.Exec(f.ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now())`, otherAccount, anchorAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	f.processJobs(t)

	snapshot := func() (string, string) {
		t.Helper()
		var state, evaluations string
		if err := f.store.pool.QueryRow(f.ctx, `
			SELECT (to_jsonb(state)-'updated_at'-'finalized_through'-'projection_version')::text,
				COALESCE((SELECT jsonb_agg(to_jsonb(e)-'created_at' ORDER BY e.id)::text
					FROM balance_checkpoint_evaluations e
					JOIN balance_reconciliation_checkpoints c ON c.id=e.checkpoint_id
					WHERE c.external_account_id=$1),'[]')
			FROM source_account_eligibility_state state WHERE state.external_account_id=$1`,
			otherAccount).Scan(&state, &evaluations); err != nil {
			t.Fatal(err)
		}
		return state, evaluations
	}
	stateBefore, evaluationsBefore := snapshot()
	if evaluationsBefore == "[]" {
		t.Fatal("fixture: the other account must have a real evaluation to compare, not an empty list")
	}

	carryAt := anchorAt.Add(idleCycleSpacing)
	f.publishEmptyBalancesCycle(t, 888, carryAt)
	f.runFinalize(t, carryAt.Add(time.Minute))
	f.processJobs(t)
	if row := readPendingReconciliation(t, f.store, f.ctx, f.accountID); row.status != "active" {
		t.Fatalf("the pending account must have exited: %+v", row)
	}

	stateAfter, evaluationsAfter := snapshot()
	if stateAfter != stateBefore {
		t.Fatalf("the other account's eligibility state changed:\nbefore %s\nafter  %s", stateBefore, stateAfter)
	}
	if evaluationsAfter != evaluationsBefore {
		t.Fatalf("the other account's evaluations changed:\nbefore %s\nafter  %s", evaluationsBefore, evaluationsAfter)
	}
	var otherProofs int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM balance_carry_forward_proofs
		WHERE external_account_id=$1`, otherAccount).Scan(&otherProofs); err != nil {
		t.Fatal(err)
	}
	if otherProofs != 0 {
		t.Fatalf("the other account (not pending) got %d idle proofs, want 0", otherProofs)
	}
}
