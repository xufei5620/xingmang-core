package postgresstore

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestIdlePendingDerivationWaitsForAStrandedCheckpointBeforeTheFreezeGuard
// pins the ordering the XM-INV-PENDING-RECON idle branch must keep against
// XM-INV-DEAD-CONTAINMENT A2, which shipped first (RC107). When the cycle the
// idle branch would derive from carries a balance_checkpoint event that is
// dead behind an open freeze on this same account, both guards apply -- and
// they do different things. The stranded wait must win.
//
// Deriving would write an immutable "no checkpoint arrived in this cycle"
// proof, and migration 0014's reject_real_checkpoint_after_carry_forward
// trigger would then refuse the real checkpoint forever once an operator
// requeued it: a replayable balance fact lost as a side effect of an idle
// re-evaluation. Both guards refuse to derive, so the proof count alone
// cannot tell them apart. What separates them is what the job does next. The
// stranded wait returns errBalanceCarryForwardProofPending, which requeues
// the job and leaves finalized_through where it is, so the cycle survives for
// the requeued checkpoint. Returning nil -- the freeze guard's own answer --
// would finish the job and advance finalized_through straight past it.
func TestIdlePendingDerivationWaitsForAStrandedCheckpointBeforeTheFreezeGuard(t *testing.T) {
	f := newIdlePendingFixture(t)
	proofsBefore := f.proofCount(t)
	finalizedBefore := f.finalizedThrough(t)

	carryAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	strandedEvent := SourceBatchEvent{EventID: autoReconcileUUID('8', 889),
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("idle-stranded-checkpoint"),
		PayloadCiphertext: bytes.Repeat([]byte{13}, 32), ObservedAt: carryAt}
	cycle := f.chain.commit(t, f.store, f.ctx, f.sourceID, "balances", autoReconcileUUID('9', 889),
		carryAt, []SourceBatchEvent{strandedEvent})
	// The event dies before it can become a checkpoint, and an open freeze on
	// this account contains it -- the ordinary XM-INV-DEAD-CONTAINMENT shape.
	var payloadHash string
	if err := f.store.pool.QueryRow(f.ctx, `
		UPDATE source_ingest_events SET processing_status='dead',processing_error='PROJECTION_FAILED',
			attempt_count=8,dependency_kind=NULL,dependency_key_hmac=NULL,updated_at=now()
		WHERE source_instance_id=$1 AND stream_id='balances' AND event_id=$2::uuid
		RETURNING payload_hash`, f.sourceID, strandedEvent.EventID).Scan(&payloadHash); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.pool.Exec(f.ctx, `
		INSERT INTO eligibility_freezes(id,external_account_id,freeze_reason,trigger_object_type,
			trigger_object_id,source_revision_hash,status)
		VALUES($1,$2,'EVENT_DEAD','balance_checkpoint','idle-stranded-checkpoint',$3,'open')`,
		autoReconcileUUID('6', 889), f.accountID, payloadHash); err != nil {
		t.Fatal(err)
	}
	// Containment publishes the cycle anyway -- that is what lets the idle
	// branch see it at all, and why this ordering matters.
	tx, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = tryPublishEconomicScanCyclesTx(f.ctx, tx, f.sourceID, "balances",
		AuditActor{Type: "source_connector", ID: f.sourceID}); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	var cycleStatus string
	if err = f.store.pool.QueryRow(f.ctx, `SELECT cycle_status FROM source_economic_scan_cycles
		WHERE source_instance_id=$1 AND stream_id='balances' AND scan_cycle_id=$2::uuid`,
		f.sourceID, cycle.cycleID).Scan(&cycleStatus); err != nil {
		t.Fatal(err)
	}
	if cycleStatus != "published" {
		t.Fatalf("fixture: containment must publish the cycle, got %q", cycleStatus)
	}

	f.runFinalize(t, carryAt.Add(time.Minute))
	if _, exists := f.jobStatus(t); !exists {
		t.Fatal("C1 must still enqueue: the stranded-checkpoint decision belongs to the proof phase")
	}
	if processed := f.processJobs(t); processed != 0 {
		t.Fatalf("projection jobs processed=%d, want 0 (the proof phase must wait)", processed)
	}

	if got := f.proofCount(t); got != proofsBefore {
		t.Fatalf("proofs=%d, want %d: no proof may be written over a stranded checkpoint", got, proofsBefore)
	}
	var status, errorCode string
	if err = f.store.pool.QueryRow(f.ctx, `SELECT status,COALESCE(last_error_code,'')
		FROM eligibility_projection_jobs WHERE external_account_id=$1`, f.accountID).Scan(&status, &errorCode); err != nil {
		t.Fatalf("the job must still exist, waiting: %v", err)
	}
	if status != "queued" || errorCode != "BALANCE_PROOF_PENDING" {
		t.Fatalf("job status=%q last_error_code=%q, want queued/BALANCE_PROOF_PENDING -- the freeze guard's own answer "+
			"(return nil) would have finished the job instead", status, errorCode)
	}
	if now := f.finalizedThrough(t); !now.Equal(finalizedBefore) {
		t.Fatalf("finalized_through advanced to %s (was %s): the cycle carrying the stranded checkpoint was burned",
			now, finalizedBefore)
	}
	if row := readPendingReconciliation(t, f.store, f.ctx, f.accountID); row.status != "not_invoiceable_pending_reconciliation" ||
		row.consecutiveMatches != pendingReconciliationExitMatches-1 {
		t.Fatalf("the waiting account must keep its state and streak: %+v", row)
	}
}

// TestFinalizeDoesNotWaitOnAHeldJobRowForAnIdlePendingAccount is design
// matrix row (l): C1 adds a fifth OR branch to the same set-based enqueue
// that XM-INV-CATCHUP-BURST-BACKPRESSURE made non-blocking, after a
// production cycle publication died on lock_timeout and took the balances
// stream down for 29 minutes. The new branch must not reintroduce the wait:
// an idle pending account whose job row is held by a running projection is
// skipped by the same SKIP LOCKED clause as everyone else.
func TestFinalizeDoesNotWaitOnAHeldJobRowForAnIdlePendingAccount(t *testing.T) {
	f := newIdlePendingFixture(t)
	carryAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	f.publishEmptyBalancesCycle(t, 890, carryAt)
	now := time.Now().UTC().Truncate(time.Microsecond)
	seedEligibilityProjectionJobRowProcessing(t, f.store, f.ctx, f.accountID, now, now, now.Add(2*time.Minute))

	holder, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err = holder.Exec(f.ctx, `SELECT 1 FROM eligibility_projection_jobs
		WHERE external_account_id=$1 FOR UPDATE`, f.accountID); err != nil {
		t.Fatal(err)
	}

	tx, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(f.ctx, `SET LOCAL lock_timeout='2s'`); err != nil {
		t.Fatal(err)
	}
	watermark := carryAt.Add(time.Minute).Add(f.finalizationDelay(t))
	if _, err = tx.Exec(f.ctx, `INSERT INTO source_economic_stream_watermarks(
		source_instance_id,stream_kind,watermark_at,source_sequence,source_cursor,configuration_hash)
		VALUES($1,'payments',$2,1,'c',$3),($1,'usage',$2,1,'c',$3),
			($1,'credits',$2,1,'c',$3),($1,'balances',$2,1,'c',$3)
		ON CONFLICT(source_instance_id,stream_kind) DO UPDATE SET
			watermark_at=GREATEST(source_economic_stream_watermarks.watermark_at,EXCLUDED.watermark_at)`,
		f.sourceID, watermark, f.configHash); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	finalizeErr := finalizeSourceAccountsTx(f.ctx, tx, f.sourceID, AuditActor{Type: "system", ID: "pending-recon-test"})
	elapsed := time.Since(started)
	if finalizeErr != nil {
		var pgErr *pgconn.PgError
		if errors.As(finalizeErr, &pgErr) && pgErr.Code == "55P03" {
			t.Fatalf("the idle branch waited on the running job's row and died on lock_timeout after %s", elapsed)
		}
		t.Fatalf("finalization failed: %v", finalizeErr)
	}
	if elapsed >= 1500*time.Millisecond {
		t.Fatalf("finalization took %s; it must not wait on a held job row at all", elapsed)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	// The running job keeps its claim untouched.
	var status string
	var lease *string
	if err = f.store.pool.QueryRow(f.ctx, `SELECT status,lease_token FROM eligibility_projection_jobs
		WHERE external_account_id=$1`, f.accountID).Scan(&status, &lease); err != nil {
		t.Fatal(err)
	}
	if status != "processing" || lease == nil || *lease != "test-lease-"+f.accountID {
		t.Fatalf("held job row was modified: status=%q lease=%v", status, lease)
	}
}
