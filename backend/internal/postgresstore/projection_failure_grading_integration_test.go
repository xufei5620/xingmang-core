package postgresstore

import (
	"context"
	"strings"
	"testing"
	"time"
)

// poisonProjectionFixture wraps seedCarryForwardFixture (balance_carry_forward_integration_test.go)
// with the same "unmapped funding visibility" shape
// TestBalanceDeltaCarryForwardRejectsUnmappedFundingVisibility uses: a
// WALLET_CASH funding lot referencing a payments-stream source_events row
// that never went through the real, verified ingest path. This makes
// processEligibilityProjectionJob deterministically return
// errBalanceCarryForwardProofInvalid on every attempt for as long as the
// fixture stands (the failing transaction rolls back every write, so nothing
// about the underlying condition ever resolves on its own) -- exactly the
// "genuinely, persistently failing account" this slice's grading ladder
// needs to exercise.
type poisonProjectionFixture struct {
	carryForwardFixture
	lotID string
}

func seedPoisonProjectionAccountFixture(t *testing.T) poisonProjectionFixture {
	t.Helper()
	fixture := seedCarryForwardFixture(t, "", "")
	const (
		sourceEventID = "45000000-0000-4000-8000-000000000098"
		lotID         = "55000000-0000-4000-8000-000000000098"
	)
	if _, err := fixture.store.pool.Exec(fixture.ctx, `INSERT INTO source_events(
		id,source_instance_id,external_user_id,event_kind,external_event_id,source_status,
		schema_version,source_revision_hash,source_updated_at,observed_at,source_sequence)
		VALUES($1,$2,'carry-user','payment','46000000-0000-4000-8000-000000000098',
		'COMPLETED','source-agent-v3.0',$3,$4,$4,1)`, sourceEventID, fixture.sourceID,
		testHash("grading-poison-funding-revision"), fixture.cutover.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.pool.Exec(fixture.ctx, `INSERT INTO funding_lots(
		id,invoice_user_id,external_account_id,source_instance_id,source_event_id,
		external_order_id,currency,original_minor,current_cap_minor,verification_state,
		source_status,source_revision_hash,completed_at,observed_at,eligibility_kind,
		eligibility_cutover_at,verified_cash_minor,consumed_cash_minor,eligibility_revision)
		VALUES($1,$2,$3,$4,$5,'grading-poison-funding','CNY',10000,10000,'verified',
		'COMPLETED',$6,$7,$7,'WALLET_CASH',$8,10000,0,1)`, lotID, fixture.userID,
		fixture.accountID, fixture.sourceID, sourceEventID, testHash("grading-poison-funding-revision"),
		fixture.cutover.Add(10*time.Minute), fixture.cutover.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.pool.Exec(fixture.ctx, `INSERT INTO funding_lot_consumption_state(
		funding_lot_id,cash_service_units,consumed_service_units,cumulative_cash_numerator,
		rounded_consumed_cash_minor,rounding_remainder_numerator)
		VALUES($1,10,0,0,0,0)`, lotID); err != nil {
		t.Fatal(err)
	}
	return poisonProjectionFixture{carryForwardFixture: fixture, lotID: lotID}
}

// jobRow reads back the grading-relevant columns this slice added/changed.
func (f poisonProjectionFixture) jobRow(t *testing.T) (status string, attempts int64, lastErrorCode, lastError string, nextAttemptAt time.Time) {
	t.Helper()
	if err := f.store.pool.QueryRow(f.ctx, `SELECT status,attempts,COALESCE(last_error_code,''),
		COALESCE(last_error,''),next_attempt_at
		FROM eligibility_projection_jobs WHERE external_account_id=$1`, f.accountID).Scan(
		&status, &attempts, &lastErrorCode, &lastError, &nextAttemptAt); err != nil {
		t.Fatal(err)
	}
	return
}

// TestProjectionFailureGradingTransientErrorRequeuesWithBackoff covers the
// first rung of the ladder: one processing error requeues the job
// (status='queued', attempts=1, a future next_attempt_at ~30s out -- the
// first backoff step), never a bare status='failed' the way pre-grading code
// did, and readiness-relevant counters reflect a merely-retrying job, not a
// terminal one.
func TestProjectionFailureGradingTransientErrorRequeuesWithBackoff(t *testing.T) {
	fixture := seedPoisonProjectionAccountFixture(t)
	roundNow := time.Now().UTC()

	processed, err := fixture.store.ProcessEligibilityProjectionJobs(fixture.ctx, 10, roundNow,
		AuditActor{Type: "system", ID: "grading-test-worker"})
	if processed != 0 || err == nil || !strings.Contains(err.Error(), errBalanceCarryForwardProofInvalid.Error()) {
		t.Fatalf("processed=%d err=%v, want processed=0 and the wrapped proof-invalid error", processed, err)
	}

	status, attempts, lastErrorCode, lastError, nextAttemptAt := fixture.jobRow(t)
	if status != "queued" {
		t.Fatalf("status=%q, want queued (never a bare 'failed')", status)
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d, want 1", attempts)
	}
	if lastErrorCode != "PROJECTION_FAILED" {
		t.Fatalf("last_error_code=%q, want PROJECTION_FAILED", lastErrorCode)
	}
	if !strings.Contains(lastError, errBalanceCarryForwardProofInvalid.Error()) {
		t.Fatalf("last_error=%q, want it to contain the underlying error text", lastError)
	}
	wantNext := roundNow.Add(projectionFailureBackoffBaseSeconds * time.Second)
	if diff := nextAttemptAt.Sub(wantNext); diff < -2*time.Second || diff > 2*time.Second {
		t.Fatalf("next_attempt_at=%s, want ~%s (first backoff step)", nextAttemptAt, wantNext)
	}

	// Readiness-relevant counters: Retrying (not Dead) reflects this job, and
	// -- the "backoff job not counted in OldestPending" requirement -- it
	// must not age into the stuck bucket while its backoff has not elapsed.
	health, err := fixture.store.EligibilityProjectionHealth(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.Dead != 0 {
		t.Fatalf("Dead=%d, want 0 (a single failure is never terminal)", health.Dead)
	}
	if health.Retrying != 1 {
		t.Fatalf("Retrying=%d, want 1", health.Retrying)
	}
	if !health.OldestPending.IsZero() {
		t.Fatalf("OldestPending=%s, want zero (a job still inside its own backoff is not stuck)", health.OldestPending)
	}
}

// TestProjectionFailureGradingEscalatesToDeadAfterConsecutiveFailures drives
// the poison account through the entire retry ladder: the same
// deterministic error, projectionFailureDeadThreshold (8) times in a row,
// escalates the job to a terminal status='dead' with an audit event carrying
// the account id, attempts and last error -- and EligibilityProjectionHealth
// reflects that as Dead, not Retrying.
func TestProjectionFailureGradingEscalatesToDeadAfterConsecutiveFailures(t *testing.T) {
	fixture := seedPoisonProjectionAccountFixture(t)
	roundNow := time.Now().UTC()

	for round := 1; round <= projectionFailureDeadThreshold; round++ {
		// Jump roundNow comfortably past projectionFailureBackoffCapSeconds
		// (30 minutes) each round so every round's claim query picks the
		// job up regardless of how far its backoff advanced.
		roundNow = roundNow.Add(40 * time.Minute)
		processed, err := fixture.store.ProcessEligibilityProjectionJobs(fixture.ctx, 10, roundNow,
			AuditActor{Type: "system", ID: "grading-test-worker"})
		if processed != 0 || err == nil || !strings.Contains(err.Error(), errBalanceCarryForwardProofInvalid.Error()) {
			t.Fatalf("round %d: processed=%d err=%v, want processed=0 and the wrapped proof-invalid error", round, processed, err)
		}
		status, attempts, _, _, _ := fixture.jobRow(t)
		wantStatus := "queued"
		if round == projectionFailureDeadThreshold {
			wantStatus = "dead"
		}
		if status != wantStatus || attempts != int64(round) {
			t.Fatalf("round %d: status=%q attempts=%d, want %q/%d", round, status, attempts, wantStatus, round)
		}
	}

	status, attempts, lastErrorCode, lastError, _ := fixture.jobRow(t)
	if status != "dead" || attempts != projectionFailureDeadThreshold || lastErrorCode != "PROJECTION_DEAD" {
		t.Fatalf("final job row status=%q attempts=%d last_error_code=%q, want dead/%d/PROJECTION_DEAD",
			status, attempts, lastErrorCode, projectionFailureDeadThreshold)
	}
	if !strings.Contains(lastError, errBalanceCarryForwardProofInvalid.Error()) {
		t.Fatalf("last_error=%q, want it to contain the underlying error text", lastError)
	}

	var auditCount int
	var auditReason string
	if err := fixture.store.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.projection.dead' AND object_type='external_account' AND object_id=$1`,
		fixture.accountID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("eligibility.projection.dead audit rows=%d, want 1", auditCount)
	}
	_ = auditReason

	health, err := fixture.store.EligibilityProjectionHealth(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.Dead != 1 {
		t.Fatalf("Dead=%d, want 1", health.Dead)
	}
	if health.Retrying != 0 {
		t.Fatalf("Retrying=%d, want 0 (a dead job is status='dead', not 'queued')", health.Retrying)
	}
}

// TestProjectionFailureGradingProofPendingDoesNotCountAsAttempt reuses
// seedAlwaysPendingBalanceProofFixture (proof_contention_backoff_integration_test.go),
// which deterministically returns errBalanceCarryForwardProofPending on
// every attempt -- a wholly different, pre-existing outcome from this
// slice's own per-account processing-error grading. attempts (the new
// column) must stay 0: XM-INV-PROOF-CONTENTION's own attempt_count keeps
// incrementing (the claim step's unconditional attempt_count=attempt_count+1),
// but the proof-pending branch never touches attempts.
func TestProjectionFailureGradingProofPendingDoesNotCountAsAttempt(t *testing.T) {
	fixture := seedAlwaysPendingBalanceProofFixture(t)

	processed, err := fixture.store.ProcessEligibilityProjectionJobs(fixture.ctx, 10, time.Now().UTC(),
		AuditActor{Type: "system", ID: "grading-proof-pending-test-worker"})
	if err != nil {
		t.Fatalf("ProcessEligibilityProjectionJobs err=%v, want nil (BALANCE_PROOF_PENDING is reported via the job row)", err)
	}
	if processed != 0 {
		t.Fatalf("processed=%d, want 0", processed)
	}

	var status, lastErrorCode string
	var attemptCount, attempts int64
	if err := fixture.store.pool.QueryRow(fixture.ctx, `SELECT status,COALESCE(last_error_code,''),attempt_count,attempts
		FROM eligibility_projection_jobs WHERE external_account_id=$1`, fixture.accountID).Scan(
		&status, &lastErrorCode, &attemptCount, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "queued" || lastErrorCode != "BALANCE_PROOF_PENDING" || attemptCount != 1 {
		t.Fatalf("status=%q last_error_code=%q attempt_count=%d, want queued/BALANCE_PROOF_PENDING/1", status, lastErrorCode, attemptCount)
	}
	if attempts != 0 {
		t.Fatalf("attempts=%d, want 0 (proof-pending must never spend a grading attempt)", attempts)
	}

	health, err := fixture.store.EligibilityProjectionHealth(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.Retrying != 0 {
		t.Fatalf("Retrying=%d, want 0 (attempts=0 excludes this row from the failure-grading retry count)", health.Retrying)
	}
	if health.ProofPending != 1 {
		t.Fatalf("ProofPending=%d, want 1", health.ProofPending)
	}
}

// TestProjectionFailureGradingRequeueDeadDryRunWritesNothingApplyRequeues
// covers invoice-eligibility-repair --kind=projection-requeue-dead's
// dry-run/apply contract: dry run reports the dead job but writes nothing;
// apply resets it to status='queued'/attempts=0/next_attempt_at<=now and
// writes an audit event.
func TestProjectionFailureGradingRequeueDeadDryRunWritesNothingApplyRequeues(t *testing.T) {
	store, ctx := integrationStore(t)
	sourceID, cutover, manifestHash, configHash := seedEligibilityProjectionHealthSource(t, store, ctx)
	const accountID = "65000000-0000-4000-8000-000000000099"
	seedEligibilityProjectionHealthAccount(t, store, ctx, sourceID, accountID, "requeue-dead", cutover, manifestHash, configHash)

	now := time.Now().UTC().Truncate(time.Microsecond)
	seedEligibilityProjectionJobRowWithAttempts(t, store, ctx, accountID, "dead", projectionFailureDeadThreshold,
		strPtr("PROJECTION_DEAD"), now.Add(-time.Hour), now.Add(-time.Minute), now.Add(-time.Minute))
	if _, err := store.pool.Exec(ctx, `UPDATE eligibility_projection_jobs SET last_error='simulated poison error'
		WHERE external_account_id=$1`, accountID); err != nil {
		t.Fatal(err)
	}

	const operatorID = "70000000-0000-4000-8000-000000000097"

	dryRun, err := store.RepairProjectionRequeueDead(ctx, ProjectionRequeueDeadRepairInput{Apply: false}, AuditActor{Type: "admin", ID: operatorID})
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.Applied || dryRun.TotalRequeued != 1 || len(dryRun.Accounts) != 1 ||
		dryRun.Accounts[0].ExternalAccountID != accountID || !dryRun.Accounts[0].Requeued ||
		dryRun.Accounts[0].PreviousAttempts != projectionFailureDeadThreshold {
		t.Fatalf("dry run result=%+v", dryRun)
	}
	status, attempts, _, lastError, _ := gradingJobRow(t, store, ctx, accountID)
	if status != "dead" || attempts != projectionFailureDeadThreshold || lastError != "simulated poison error" {
		t.Fatalf("dry run must write nothing: status=%q attempts=%d last_error=%q", status, attempts, lastError)
	}

	apply, err := store.RepairProjectionRequeueDead(ctx, ProjectionRequeueDeadRepairInput{Apply: true, OperatorID: operatorID},
		AuditActor{Type: "admin", ID: operatorID})
	if err != nil {
		t.Fatal(err)
	}
	if !apply.Applied || apply.TotalRequeued != 1 || len(apply.Accounts) != 1 || !apply.Accounts[0].Requeued {
		t.Fatalf("apply result=%+v", apply)
	}
	status, attempts, _, lastError, nextAttemptAt := gradingJobRow(t, store, ctx, accountID)
	if status != "queued" || attempts != 0 || lastError != "" {
		t.Fatalf("after apply: status=%q attempts=%d last_error=%q, want queued/0/empty", status, attempts, lastError)
	}
	if nextAttemptAt.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("after apply: next_attempt_at=%s, want <= now", nextAttemptAt)
	}

	var auditCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.projection.requeued' AND object_type='external_account' AND object_id=$1`,
		accountID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("eligibility.projection.requeued audit rows=%d, want 1", auditCount)
	}

	// Apply requires a valid operator id, matching every sibling repair.
	if _, err := store.RepairProjectionRequeueDead(ctx, ProjectionRequeueDeadRepairInput{Apply: true}, AuditActor{Type: "admin"}); err == nil {
		t.Fatal("apply without an operator id was accepted")
	}
}

// gradingJobRow is a free-standing variant of poisonProjectionFixture.jobRow
// for tests that build their own account outside that fixture's own recipe.
func gradingJobRow(t *testing.T, store *Store, ctx context.Context, accountID string) (status string, attempts int64, lastErrorCode, lastError string, nextAttemptAt time.Time) {
	t.Helper()
	if err := store.pool.QueryRow(ctx, `SELECT status,attempts,COALESCE(last_error_code,''),
		COALESCE(last_error,''),next_attempt_at
		FROM eligibility_projection_jobs WHERE external_account_id=$1`, accountID).Scan(
		&status, &attempts, &lastErrorCode, &lastError, &nextAttemptAt); err != nil {
		t.Fatal(err)
	}
	return
}

// TestProjectionFailureGradingTwoAccountIsolationAcrossRetryLadder drives one
// poison account (always errors) and one ordinary healthy account through
// the poison account's entire retry ladder, in the same
// ProcessEligibilityProjectionJobs batch every round: the healthy account
// must keep succeeding every single round, completely unaffected by the
// poison account's own accumulating attempts and eventual escalation to
// dead.
func TestProjectionFailureGradingTwoAccountIsolationAcrossRetryLadder(t *testing.T) {
	poison := seedPoisonProjectionAccountFixture(t)

	const healthyAccountID = "34000000-0000-4000-8000-000000000099"
	if _, err := poison.store.pool.Exec(poison.ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','grading-healthy-user')`, healthyAccountID); err != nil {
		t.Fatal(err)
	}
	if _, err := poison.store.pool.Exec(poison.ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
		VALUES($1,$1,$2,'grading-healthy','grading-healthy-hmac','test','verified')`,
		healthyAccountID, poison.sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := poison.store.pool.Exec(poison.ctx, `INSERT INTO source_account_eligibility_state(
		external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
		cutover_manifest_hash,finalized_through,finalization_delay_seconds)
		VALUES($1,$2,$3,'SUB2_BALANCE_1E8',50,$4,$3,900)`, healthyAccountID, poison.sourceID,
		poison.cutover, poison.manifestHash); err != nil {
		t.Fatal(err)
	}
	if _, err := poison.store.pool.Exec(poison.ctx, `INSERT INTO balance_reconciliation_checkpoints(
		id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
		checkpoint_kind,baseline_snapshot_id,baseline_member,source_snapshot_id,snapshot_row_count,
		as_of,balance_service_units,balance_negative,unit_code,cutover_manifest_hash,
		configuration_hash,reconciliation_status,source_sequence,source_cursor,
		stream_watermark_at,source_revision_hash,observed_at)
		VALUES('44000000-0000-4000-8000-000000000099',$1,$2,'grading-healthy-cutover-event',
		'grading-healthy-cutover-checkpoint','cutover',$3,TRUE,$3,1,$4,50,FALSE,'SUB2_BALANCE_1E8',
		$5,$6,'cutover_baseline',1,'grading-healthy-cutover:1',$4,$7,$4)`, poison.sourceID, healthyAccountID,
		testHash("carry-forward-baseline"), poison.cutover, poison.manifestHash, poison.configHash,
		testHash("grading-healthy-cutover-revision")); err != nil {
		t.Fatal(err)
	}
	if _, err := poison.store.pool.Exec(poison.ctx, `INSERT INTO source_credit_events(
		id,source_instance_id,external_account_id,external_event_id,external_credit_id,
		event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
		credit_kind,source_sequence,source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES('54000000-0000-4000-8000-000000000099',$1,$2,'grading-healthy-opening-event','grading-healthy-opening',
		$3,50,'SUB2_BALANCE_1E8',$4,$5,'LEGACY_NON_INVOICEABLE',0,'grading-healthy-opening:0',$3,$6,$3)`,
		poison.sourceID, healthyAccountID, poison.cutover, poison.manifestHash, poison.configHash,
		testHash("grading-healthy-opening-revision")); err != nil {
		t.Fatal(err)
	}

	seedHealthyJob := func() {
		if _, err := poison.store.pool.Exec(poison.ctx, `INSERT INTO eligibility_projection_jobs(
			external_account_id,requested_through,status,next_attempt_at)
			VALUES($1,$2,'queued',now()-interval '1 second')`, healthyAccountID, poison.cutover); err != nil {
			t.Fatal(err)
		}
	}
	seedHealthyJob()

	roundNow := time.Now().UTC()
	for round := 1; round <= projectionFailureDeadThreshold; round++ {
		roundNow = roundNow.Add(40 * time.Minute)
		processed, err := poison.store.ProcessEligibilityProjectionJobs(poison.ctx, 10, roundNow,
			AuditActor{Type: "system", ID: "grading-isolation-test-worker"})
		if err == nil || !strings.Contains(err.Error(), errBalanceCarryForwardProofInvalid.Error()) {
			t.Fatalf("round %d: err=%v, want the poison account's wrapped proof-invalid error", round, err)
		}
		if processed != 1 {
			t.Fatalf("round %d: processed=%d, want 1 (the healthy account, isolated from the poison account's failure)", round, processed)
		}

		var healthyStatus string
		if err := poison.store.pool.QueryRow(poison.ctx, `SELECT eligibility_status
			FROM source_account_eligibility_state WHERE external_account_id=$1`, healthyAccountID).Scan(&healthyStatus); err != nil {
			t.Fatal(err)
		}
		if healthyStatus != "active" {
			t.Fatalf("round %d: healthy account status=%q, want active", round, healthyStatus)
		}
		var healthyJobRows int
		if err := poison.store.pool.QueryRow(poison.ctx, `SELECT count(*) FROM eligibility_projection_jobs
			WHERE external_account_id=$1`, healthyAccountID).Scan(&healthyJobRows); err != nil {
			t.Fatal(err)
		}
		if healthyJobRows != 0 {
			t.Fatalf("round %d: healthy account job rows=%d, want 0 (it succeeded and self-deleted)", round, healthyJobRows)
		}

		poisonStatus, poisonAttempts, _, _, _ := poison.jobRow(t)
		wantStatus := "queued"
		if round == projectionFailureDeadThreshold {
			wantStatus = "dead"
		}
		if poisonStatus != wantStatus || poisonAttempts != int64(round) {
			t.Fatalf("round %d: poison status=%q attempts=%d, want %q/%d", round, poisonStatus, poisonAttempts, wantStatus, round)
		}

		if round < projectionFailureDeadThreshold {
			seedHealthyJob()
		}
	}

	if count := openFreezeCount(t, poison.store, poison.ctx, healthyAccountID); count != 0 {
		t.Fatalf("healthy account open freeze count=%d, want 0", count)
	}
	health, err := poison.store.EligibilityProjectionHealth(poison.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.Dead != 1 {
		t.Fatalf("Dead=%d, want 1 (the poison account, and only it)", health.Dead)
	}
}
