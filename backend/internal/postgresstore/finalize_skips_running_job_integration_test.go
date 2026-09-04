package postgresstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// XM-INV-CATCHUP-BURST-BACKPRESSURE. finalizeSourceAccountsTx runs inside the
// cycle-publication transaction, which already holds the stream's scan-cycle
// and watermark rows. If its enqueue waits on a job row that a running
// projection holds FOR UPDATE, the runtime role's lock_timeout kills the whole
// publication -- and with it the stream's watermark and readiness. This
// fixture reproduces exactly that shape: one account's job row held by
// another transaction for the duration, a second account with an equally
// live window, and a finalization pass under a lock_timeout shorter than the
// hold.

type finalizeSkipFixture struct {
	store               *Store
	ctx                 context.Context
	sourceID            string
	heldAccount         string
	freeAccount         string
	configHash          string
	heldRequestedBefore time.Time
}

func seedFinalizeSkipFixture(t *testing.T) finalizeSkipFixture {
	t.Helper()
	store, ctx := integrationStore(t)
	const (
		sourceID    = "16000000-0000-4000-8000-000000000001"
		userID      = "26000000-0000-4000-8000-000000000001"
		heldAccount = "36000000-0000-4000-8000-000000000001"
		freeAccount = "36000000-0000-4000-8000-000000000002"
	)
	var policyStart time.Time
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_start_at FROM invoice_eligibility_policy WHERE singleton_id=1`).Scan(&policyStart); err != nil {
		t.Fatal(err)
	}
	cutover := policyStart.UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)
	manifestHash := testHash("finalize-skip-manifest")
	configHash := testHash("finalize-skip-config")
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','finalize-skip-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','finalize-skip-user')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'v3-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
		'p0','u0','c0','b0',$5,$5,1,'test-key')`, sourceID, manifestHash,
		cutover, configHash, testHash("finalize-skip-baseline")); err != nil {
		t.Fatal(err)
	}
	for i, accountID := range []string{heldAccount, freeAccount} {
		suffix := string(rune('a' + i))
		if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
			id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
			VALUES($1,$2,$3,$4,$5,'test','verified')`, accountID, userID, sourceID, "finalize-skip-"+suffix, testHash("finalize-skip-subject-"+suffix)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.pool.Exec(ctx, `INSERT INTO source_account_eligibility_state(
			external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
			cutover_manifest_hash,finalized_through,finalization_delay_seconds)
			VALUES($1,$2,$3,'SUB2_BALANCE_1E8',0,$4,$3,900)`, accountID, sourceID, cutover, manifestHash); err != nil {
			t.Fatal(err)
		}
		// One usage fact inside every finalization window, so both accounts
		// are in `changed` on every pass.
		if _, err := store.pool.Exec(ctx, `INSERT INTO source_usage_events(
			id,source_instance_id,external_account_id,external_event_id,external_usage_id,
			event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
			billing_scope,invoice_eligible,source_sequence,source_cursor,stream_watermark_at,
			source_revision_hash,observed_at)
			VALUES($1,$2,$3,$4,$5,$6,10,'SUB2_BALANCE_1E8',$7,$8,'wallet',TRUE,$9,$10,$6,$11,$6)`,
			"46000000-0000-4000-8000-00000000000"+string(rune('1'+i)), sourceID, accountID,
			"finalize-skip-event-"+suffix, "finalize-skip-usage-"+suffix,
			cutover.Add(10*time.Minute), manifestHash, configHash, int64(i+1),
			"finalize-skip:"+suffix, testHash("finalize-skip-revision-"+suffix)); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	// The held account's job is mid-flight: status='processing' with a live
	// lease, exactly as ProcessEligibilityProjectionJobs leaves it before
	// processEligibilityProjectionJob takes the row lock.
	seedEligibilityProjectionJobRowProcessing(t, store, ctx, heldAccount, now, now, now.Add(2*time.Minute))
	var requestedBefore time.Time
	if err := store.pool.QueryRow(ctx, `SELECT requested_through FROM eligibility_projection_jobs WHERE external_account_id=$1`, heldAccount).Scan(&requestedBefore); err != nil {
		t.Fatal(err)
	}
	return finalizeSkipFixture{store: store, ctx: ctx, sourceID: sourceID, heldAccount: heldAccount,
		freeAccount: freeAccount, configHash: configHash, heldRequestedBefore: requestedBefore}
}

// runFinalizeUnderLockTimeout runs one finalization pass in its own
// transaction with a lock_timeout far shorter than the fixture's hold, the
// way the runtime role's 5s budget bounds the real publication transaction.
func (f finalizeSkipFixture) runFinalizeUnderLockTimeout(t *testing.T) (time.Duration, error) {
	t.Helper()
	tx, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(f.ctx, `SET LOCAL lock_timeout='2s'`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(f.ctx, `INSERT INTO source_economic_stream_watermarks(
		source_instance_id,stream_kind,watermark_at,source_sequence,source_cursor,configuration_hash)
		VALUES($1,'payments',now(),1,'c',$2),($1,'usage',now(),1,'c',$2),
			($1,'credits',now(),1,'c',$2),($1,'balances',now(),1,'c',$2)
		ON CONFLICT(source_instance_id,stream_kind) DO UPDATE SET watermark_at=EXCLUDED.watermark_at`,
		f.sourceID, f.configHash); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	finalizeErr := finalizeSourceAccountsTx(f.ctx, tx, f.sourceID, AuditActor{Type: "system", ID: "finalize-skip-test"})
	elapsed := time.Since(started)
	if finalizeErr != nil {
		return elapsed, finalizeErr
	}
	return elapsed, tx.Commit(f.ctx)
}

func (f finalizeSkipFixture) jobRow(t *testing.T, accountID string) (status string, lease *string, requested time.Time, exists bool) {
	t.Helper()
	err := f.store.pool.QueryRow(f.ctx, `SELECT status,lease_token,requested_through FROM eligibility_projection_jobs
		WHERE external_account_id=$1`, accountID).Scan(&status, &lease, &requested)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return "", nil, time.Time{}, false
		}
		t.Fatal(err)
	}
	return status, lease, requested, true
}

func TestFinalizeSourceAccountsSkipsAJobRowHeldByARunningProjection(t *testing.T) {
	f := seedFinalizeSkipFixture(t)

	// Another connection holds the held account's job row for the whole test,
	// as processEligibilityProjectionJob does for the length of its
	// transaction.
	holder, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	holderReleased := false
	defer func() {
		if !holderReleased {
			_ = holder.Rollback(context.Background())
		}
	}()
	if _, err = holder.Exec(f.ctx, `SELECT 1 FROM eligibility_projection_jobs WHERE external_account_id=$1 FOR UPDATE`, f.heldAccount); err != nil {
		t.Fatal(err)
	}

	elapsed, err := f.runFinalizeUnderLockTimeout(t)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			t.Fatalf("finalization waited on the running job's row and died on lock_timeout after %s -- this is the production publication failure", elapsed)
		}
		t.Fatalf("finalization failed: %v", err)
	}
	if elapsed >= 1500*time.Millisecond {
		t.Fatalf("finalization took %s; it must not wait on a held job row at all", elapsed)
	}

	// The free account was enqueued in the same pass: skipping one account
	// must not cost the others their window.
	status, _, _, exists := f.jobRow(t, f.freeAccount)
	if !exists || status != "queued" {
		t.Fatalf("free account should have been enqueued as queued, got exists=%v status=%q", exists, status)
	}
	// The held account's row is exactly as the running job left it: still
	// processing, lease intact, requested_through unchanged. Touching any of
	// those would strip a live job of its lease.
	status, lease, requested, exists := f.jobRow(t, f.heldAccount)
	if !exists || status != "processing" || lease == nil || *lease != "test-lease-"+f.heldAccount || !requested.Equal(f.heldRequestedBefore) {
		t.Fatalf("held account's job row was modified: exists=%v status=%q lease=%v requested=%s (was %s)", exists, status, lease, requested, f.heldRequestedBefore)
	}
	// And finalized_through was not advanced behind the running job's back:
	// the second statement's own NOT EXISTS(job) guard still sees the row.
	var finalized, cutover time.Time
	if err = f.store.pool.QueryRow(f.ctx, `SELECT finalized_through,cutover_at FROM source_account_eligibility_state WHERE external_account_id=$1`, f.heldAccount).Scan(&finalized, &cutover); err != nil {
		t.Fatal(err)
	}
	if !finalized.Equal(cutover) {
		t.Fatalf("held account's finalized_through moved to %s while its job was running", finalized)
	}

	// Once the job's transaction is gone, the very next pass takes the row --
	// the skip cost one poll interval, not the window.
	if err = holder.Rollback(f.ctx); err != nil {
		t.Fatal(err)
	}
	holderReleased = true
	if _, err = f.runFinalizeUnderLockTimeout(t); err != nil {
		t.Fatalf("finalization after release failed: %v", err)
	}
	// Reclaimed: queued, lease cleared, and requested_through merged with
	// GREATEST -- never lowered (the seed's own value is newer than this
	// pass's watermark-derived window, so it is kept as is).
	status, lease, requested, exists = f.jobRow(t, f.heldAccount)
	if !exists || status != "queued" || lease != nil || requested.Before(f.heldRequestedBefore) {
		t.Fatalf("held account should have been reclaimed once unheld, got status=%q lease=%v requested=%s (was %s)", status, lease, requested, f.heldRequestedBefore)
	}
}
