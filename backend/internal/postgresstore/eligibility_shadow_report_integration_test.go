package postgresstore

import (
	"context"
	"testing"
	"time"
)

// seedEligibilityShadowCheckpoint inserts the minimal
// balance_reconciliation_checkpoints row that balance_checkpoint_evaluations'
// foreign key requires, reusing the source/account fixture
// seedEligibilityProjectionHealthSource/seedEligibilityProjectionHealthAccount
// already build for the sibling XM-INV-READY-PENDING/READY-LEASE health
// tests in this package.
func seedEligibilityShadowCheckpoint(t *testing.T, store *Store, ctx context.Context, sourceID, accountID, manifestHash, configHash string, asOf time.Time) string {
	t.Helper()
	checkpointID := randomUUID()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO balance_reconciliation_checkpoints(
			id,source_instance_id,external_account_id,external_event_id,checkpoint_id,checkpoint_kind,
			as_of,balance_service_units,unit_code,cutover_manifest_hash,configuration_hash,
			reconciliation_status,expected_service_units,difference_service_units,
			source_sequence,source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES($1,$2,$3,$4,$4,'reconciliation',$5,0,'SUB2_BALANCE_1E8',$6,$7,'matched',0,0,1,'cursor-1',$5,$8,$5)`,
		checkpointID, sourceID, accountID, "event-"+checkpointID, asOf, manifestHash, configHash,
		testHash("shadow-checkpoint-"+checkpointID)); err != nil {
		t.Fatal(err)
	}
	return checkpointID
}

func seedEligibilityShadowCheckpointEvaluation(t *testing.T, store *Store, ctx context.Context, checkpointID string, projectionVersion int64, status string) {
	t.Helper()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO balance_checkpoint_evaluations(
			id,checkpoint_id,projection_version,expected_service_units,difference_service_units,evaluation_status)
		VALUES($1,$2,$3,0,0,$4)`, randomUUID(), checkpointID, projectionVersion, status); err != nil {
		t.Fatal(err)
	}
}

func TestEligibilityShadowSnapshotAccountsAndFreezes(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	activeAccount := "60000000-0000-4000-8000-000000000001"
	frozenAccount := "60000000-0000-4000-8000-000000000002"
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, activeAccount, "shadow-active", cutover, manifestHash, configHash)
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, frozenAccount, "shadow-frozen", cutover, manifestHash, configHash)

	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_freezes(id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id)
		VALUES($1,$2,'SOURCE_GAP','external_account',$3)`, randomUUID(), frozenAccount, frozenAccount); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		UPDATE source_account_eligibility_state SET eligibility_status='frozen' WHERE external_account_id=$1`,
		frozenAccount); err != nil {
		t.Fatal(err)
	}

	snapshot, err := store.EligibilityShadowSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]EligibilityShadowAccountStatus{}
	for _, account := range snapshot.Accounts {
		statuses[account.ExternalAccountID] = account
	}
	active, ok := statuses[activeAccount]
	if !ok || active.EligibilityStatus != "active" || active.OpenFreezes != 0 {
		t.Fatalf("active account snapshot wrong: %+v (present=%v)", active, ok)
	}
	frozen, ok := statuses[frozenAccount]
	if !ok || frozen.EligibilityStatus != "frozen" || frozen.OpenFreezes != 1 {
		t.Fatalf("frozen account snapshot wrong: %+v (present=%v)", frozen, ok)
	}
	if len(snapshot.OpenFreezes) != 1 || snapshot.OpenFreezes[0].FreezeReason != "SOURCE_GAP" || snapshot.OpenFreezes[0].Open != 1 {
		t.Fatalf("open freezes by reason wrong: %+v", snapshot.OpenFreezes)
	}
}

// TestEligibilityShadowSnapshotEvaluationsKeepsLatestPerCheckpoint proves the
// evaluation-status count only counts each checkpoint's current (highest
// projection_version) evaluation, not one row per historical re-evaluation
// attempt -- a checkpoint re-evaluated from source_gap_frozen to matched
// (the exact XM-INV-ANCHOR-BALANCE repair transition) must count once, as
// matched.
func TestEligibilityShadowSnapshotEvaluationsKeepsLatestPerCheckpoint(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	accountID := "60000000-0000-4000-8000-000000000003"
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, accountID, "shadow-eval", cutover, manifestHash, configHash)
	checkpointID := seedEligibilityShadowCheckpoint(t, store, ctx, sourceID, accountID, manifestHash, configHash, cutover.Add(time.Hour))
	seedEligibilityShadowCheckpointEvaluation(t, store, ctx, checkpointID, 1, "source_gap_frozen")
	seedEligibilityShadowCheckpointEvaluation(t, store, ctx, checkpointID, 2, "matched")

	snapshot, err := store.EligibilityShadowSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, count := range snapshot.Evaluations {
		counts[count.EvaluationStatus] = count.Count
	}
	if counts["matched"] != 1 {
		t.Fatalf("expected exactly one matched evaluation, got counts=%+v", counts)
	}
	if counts["source_gap_frozen"] != 0 {
		t.Fatalf("superseded evaluation must not still be counted, got counts=%+v", counts)
	}
}

func TestEligibilityProjectionClaimableCount(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	now := time.Now().UTC().Truncate(time.Second)

	dueQueued := "60000000-0000-4000-8000-0000000000a1"
	futureQueued := "60000000-0000-4000-8000-0000000000a2"
	dueFailed := "60000000-0000-4000-8000-0000000000a3"
	lapsedProcessing := "60000000-0000-4000-8000-0000000000a4"
	liveProcessing := "60000000-0000-4000-8000-0000000000a5"
	for _, accountID := range []string{dueQueued, futureQueued, dueFailed, lapsedProcessing, liveProcessing} {
		seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, accountID, "claim-"+accountID, cutover, manifestHash, configHash)
	}
	seedEligibilityProjectionJobRow(t, store, ctx, dueQueued, "queued", nil, now, now, now.Add(-time.Minute))
	seedEligibilityProjectionJobRow(t, store, ctx, futureQueued, "queued", strPtr("BALANCE_PROOF_PENDING"), now, now, now.Add(10*time.Minute))
	seedEligibilityProjectionJobRow(t, store, ctx, dueFailed, "failed", strPtr("PROJECTION_FAILED"), now, now, now.Add(-time.Minute))
	seedEligibilityProjectionJobRowProcessing(t, store, ctx, lapsedProcessing, now, now, now.Add(-time.Minute))
	seedEligibilityProjectionJobRowProcessing(t, store, ctx, liveProcessing, now, now, now.Add(time.Minute))

	claimable, err := store.EligibilityProjectionClaimableCount(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	// Claimable: dueQueued, dueFailed, lapsedProcessing. Not claimable:
	// futureQueued (backoff not elapsed), liveProcessing (lease still live).
	if claimable != 3 {
		t.Fatalf("expected 3 claimable rows, got %d", claimable)
	}
}

func TestEligibilityShadowFailedJobs(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	failedAccount := "60000000-0000-4000-8000-0000000000b1"
	queuedAccount := "60000000-0000-4000-8000-0000000000b2"
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, failedAccount, "failed-job", cutover, manifestHash, configHash)
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, queuedAccount, "queued-job", cutover, manifestHash, configHash)
	now := time.Now().UTC().Truncate(time.Second)
	seedEligibilityProjectionJobRow(t, store, ctx, failedAccount, "failed", strPtr("PROJECTION_FAILED"), now, now, now.Add(5*time.Minute))
	seedEligibilityProjectionJobRow(t, store, ctx, queuedAccount, "queued", nil, now, now, now)

	failed, err := store.EligibilityShadowFailedJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 || failed[0].ExternalAccountID != failedAccount || failed[0].LastErrorCode != "PROJECTION_FAILED" {
		t.Fatalf("expected exactly the one failed job, got %+v", failed)
	}
}
