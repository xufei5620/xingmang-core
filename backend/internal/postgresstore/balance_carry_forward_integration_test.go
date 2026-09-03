package postgresstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
)

type carryForwardFixture struct {
	store                       *Store
	ctx                         context.Context
	sourceID, userID, accountID string
	cutover, requested          time.Time
	proofCycle                  v3TestCycle
	manifestHash, configHash    string
}

func seedCarryForwardFixture(t *testing.T, creditUnits, usageUnits string) carryForwardFixture {
	t.Helper()
	store, ctx := integrationStore(t)
	const (
		sourceID  = "14000000-0000-4000-8000-000000000001"
		userID    = "24000000-0000-4000-8000-000000000001"
		accountID = "34000000-0000-4000-8000-000000000001"
	)
	var policyStart time.Time
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_start_at FROM invoice_eligibility_policy
		WHERE singleton_id=1`).Scan(&policyStart); err != nil {
		t.Fatal(err)
	}
	cutover := policyStart.UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)
	requested := cutover.Add(45 * time.Minute)
	manifestHash := testHash("carry-forward-manifest")
	configHash := testHash("carry-forward-config")
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','carry-forward-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','carry-forward-user')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,'carry-user','test','verified')`, accountID, userID, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "balances", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'v3-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
		'p0','u0','c0','b0',$5,$5,1,'test-key')`, sourceID, manifestHash, cutover,
		configHash, testHash("carry-forward-baseline")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_account_eligibility_state(
		external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
		cutover_manifest_hash,finalized_through,finalization_delay_seconds)
		VALUES($1,$2,$3,'SUB2_BALANCE_1E8',100,$4,$3,900)`, accountID, sourceID,
		cutover, manifestHash); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO balance_reconciliation_checkpoints(
		id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
		checkpoint_kind,baseline_snapshot_id,baseline_member,source_snapshot_id,snapshot_row_count,
		as_of,balance_service_units,balance_negative,unit_code,cutover_manifest_hash,
		configuration_hash,reconciliation_status,source_sequence,source_cursor,
		stream_watermark_at,source_revision_hash,observed_at)
		VALUES('44000000-0000-4000-8000-000000000001',$1,$2,'carry-cutover-event',
		'carry-cutover-checkpoint','cutover',$3,TRUE,$3,1,$4,100,FALSE,'SUB2_BALANCE_1E8',
		$5,$6,'cutover_baseline',1,'carry-cutover:1',$4,$7,$4)`, sourceID, accountID,
		testHash("carry-forward-baseline"), cutover, manifestHash, configHash,
		testHash("carry-cutover-revision")); err != nil {
		t.Fatal(err)
	}
	// ObserveBalanceCheckpoint(cutover) normally derives this opening non-cash
	// pool. Keep the direct fixture equivalent so expected balance starts at the
	// signed actual 100 before post-cutover facts are applied.
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_credit_events(
		id,source_instance_id,external_account_id,external_event_id,external_credit_id,
		event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
		credit_kind,source_sequence,source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES('54000000-0000-4000-8000-000000000000',$1,$2,'carry-opening-event','carry-opening',
		$3,100,'SUB2_BALANCE_1E8',$4,$5,'LEGACY_NON_INVOICEABLE',0,'carry-opening:0',$3,$6,$3)`,
		sourceID, accountID, cutover, manifestHash, configHash, testHash("carry-opening-revision")); err != nil {
		t.Fatal(err)
	}
	if creditUnits != "" {
		if _, err := store.pool.Exec(ctx, `INSERT INTO source_credit_events(
			id,source_instance_id,external_account_id,external_event_id,external_credit_id,
			event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
			credit_kind,source_sequence,source_cursor,stream_watermark_at,source_revision_hash,observed_at)
			VALUES('54000000-0000-4000-8000-000000000001',$1,$2,'carry-credit-event','carry-credit',
			$3,$4::numeric,'SUB2_BALANCE_1E8',$5,$6,'BONUS',1,'carry-credit:1',$7,$8,$7)`,
			sourceID, accountID, cutover.Add(10*time.Minute), creditUnits, manifestHash, configHash,
			cutover.Add(20*time.Minute), testHash("carry-credit-revision")); err != nil {
			t.Fatal(err)
		}
	}
	if usageUnits != "" {
		if _, err := store.pool.Exec(ctx, `INSERT INTO source_usage_events(
			id,source_instance_id,external_account_id,external_event_id,external_usage_id,
			event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
			billing_scope,invoice_eligible,source_sequence,source_cursor,stream_watermark_at,
			source_revision_hash,observed_at)
			VALUES('64000000-0000-4000-8000-000000000001',$1,$2,'carry-usage-event','carry-usage',
			$3,$4::numeric,'SUB2_BALANCE_1E8',$5,$6,'wallet',TRUE,1,'carry-usage:1',$7,$8,$7)`,
			sourceID, accountID, cutover.Add(15*time.Minute), usageUnits, manifestHash, configHash,
			cutover.Add(20*time.Minute), testHash("carry-usage-revision")); err != nil {
			t.Fatal(err)
		}
	}
	chain := newV3TestChain()
	// Sequence 1 establishes a prior balances cycle. The proof cycle below is
	// sequence 2, allowing same-as_of lower-sequence prior-actual regressions.
	chain.commit(t, store, ctx, sourceID, "balances",
		"74000000-0000-4000-8000-000000000000", cutover, nil)
	unrelatedParked := SourceBatchEvent{EventID: "75000000-0000-4000-8000-000000000001",
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("unrelated-parked-balance"),
		PayloadCiphertext: []byte("unrelated-parked-balance-proof"), ObservedAt: cutover.Add(30 * time.Minute)}
	proofCycle := chain.commit(t, store, ctx, sourceID, "balances",
		"74000000-0000-4000-8000-000000000001", cutover.Add(30*time.Minute), []SourceBatchEvent{unrelatedParked})
	proofTx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer proofTx.Rollback(context.Background())
	if _, err = proofTx.Exec(ctx, `UPDATE source_ingest_events
		SET processing_status='parked_identity',dependency_kind='source_external_account',
			dependency_key_hmac='h1:'||repeat('c',64),updated_at=now()
		WHERE source_instance_id=$1 AND stream_id='balances' AND event_id=$2::uuid`,
		sourceID, unrelatedParked.EventID); err != nil {
		t.Fatal(err)
	}
	if err = tryPublishEconomicScanCyclesTx(ctx, proofTx, sourceID, "balances",
		AuditActor{Type: "source_connector", ID: sourceID, Reason: "unrelated parked identity proof"}); err != nil {
		t.Fatal(err)
	}
	if err = proofTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO eligibility_projection_jobs(
		external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now()-interval '1 second')`, accountID, requested); err != nil {
		t.Fatal(err)
	}
	return carryForwardFixture{store: store, ctx: ctx, sourceID: sourceID, userID: userID, accountID: accountID,
		cutover: cutover, requested: requested, proofCycle: proofCycle,
		manifestHash: manifestHash, configHash: configHash}
}

// TestBalanceDeltaCarryForwardFreezesUnchangedActualMismatch's name predates
// XM-INV-ELIG-AUTO-RECONCILE (design section 3(A)): a carry-forward proof's
// unchanged-actual negative mismatch no longer opens a manual
// eligibility_freezes row -- the account downgrades to the self-clearing
// not_invoiceable_pending_reconciliation state instead (kept unrenamed,
// matching XM-INV-ANCHOR-BALANCE/XM-INV-BALANCE-BLIP's own precedent of
// correcting assertions in place rather than renaming the test). Verified
// the *unmodified* assertions below fail against the fixed evaluator (status
// stayed "not_invoiceable_pending_reconciliation", not "frozen") before
// updating them, confirming the fix -- not a fixture change -- is what
// changed the outcome.
func TestBalanceDeltaCarryForwardFreezesUnchangedActualMismatch(t *testing.T) {
	fixture := seedCarryForwardFixture(t, "20", "")
	before := time.Now().UTC()
	processed, err := fixture.store.ProcessEligibilityProjectionJobs(fixture.ctx, 10, time.Now().UTC().Add(time.Minute),
		AuditActor{Type: "system", ID: "carry-forward-worker"})
	if err != nil || processed != 1 {
		t.Fatalf("carry-forward projection processed=%d err=%v", processed, err)
	}
	var status, reason, triggerType, triggerID, detail string
	var finalized, since time.Time
	var consecutiveMatches int
	if err = fixture.store.pool.QueryRow(fixture.ctx, `SELECT eligibility_status,finalized_through,
			COALESCE(pending_reconciliation_reason,''),COALESCE(pending_reconciliation_trigger_type,''),
			COALESCE(pending_reconciliation_trigger_id,''),COALESCE(pending_reconciliation_detail,''),
			COALESCE(pending_reconciliation_since,'epoch'::timestamptz),pending_reconciliation_consecutive_matches
		FROM source_account_eligibility_state WHERE external_account_id=$1`, fixture.accountID).Scan(
		&status, &finalized, &reason, &triggerType, &triggerID, &detail, &since, &consecutiveMatches); err != nil {
		t.Fatal(err)
	}
	if status != "not_invoiceable_pending_reconciliation" || !finalized.Equal(fixture.requested) {
		t.Fatalf("unchanged actual was not downgraded to pending reconciliation status=%q finalized=%s requested=%s",
			status, finalized, fixture.requested)
	}
	if reason != "UNKNOWN_NEGATIVE_BALANCE" || triggerType != "balance_carry_forward_proof" || triggerID == "" ||
		detail == "" || since.Before(before) || consecutiveMatches != 0 {
		t.Fatalf("pending reconciliation columns reason=%q triggerType=%q triggerID=%q detail=%q since=%s matches=%d",
			reason, triggerType, triggerID, detail, since, consecutiveMatches)
	}
	var freezes int
	if err = fixture.store.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM eligibility_freezes
		WHERE external_account_id=$1 AND status='open'`,
		fixture.accountID).Scan(&freezes); err != nil || freezes != 0 {
		t.Fatalf("carry-forward mismatch no longer opens a manual freeze: open freezes=%d err=%v", freezes, err)
	}
	var proofKey, revision, evaluationStatus, balance, expected, difference string
	if err = fixture.store.pool.QueryRow(fixture.ctx, `
		SELECT proof.proof_key,proof.source_revision_hash,proof.balance_service_units::text,
			evaluation.expected_service_units::text,evaluation.difference_service_units::text,
			evaluation.evaluation_status
		FROM balance_carry_forward_proofs proof
		JOIN balance_carry_forward_evaluations evaluation ON evaluation.proof_id=proof.id
		WHERE proof.external_account_id=$1`, fixture.accountID).Scan(
		&proofKey, &revision, &balance, &expected, &difference, &evaluationStatus); err != nil {
		t.Fatal(err)
	}
	var signedBatchHash string
	if err = fixture.store.pool.QueryRow(fixture.ctx, `SELECT body_hash FROM source_ingest_batches
		WHERE source_instance_id=$1 AND stream_id='balances' AND batch_id=$2::uuid`,
		fixture.sourceID, fixture.proofCycle.batchID).Scan(&signedBatchHash); err != nil {
		t.Fatal(err)
	}
	wantKey := carryProofKey(fixture.proofCycle.cycleID, fixture.accountID)
	if proofKey != wantKey || revision != signedBatchHash || balance != "100" || expected != "120" ||
		difference != "-20" || evaluationStatus != "negative_frozen" {
		t.Fatalf("carry proof key=%q revision=%q balance=%s expected=%s difference=%s status=%s",
			proofKey, revision, balance, expected, difference, evaluationStatus)
	}
	items, err := fixture.store.ListEligibilitySummaries(fixture.ctx, fixture.userID, "")
	if err != nil || len(items) != 1 || items[0].EligibilityStatus != "not_invoiceable_pending_reconciliation" ||
		items[0].AvailableMinor != 0 || items[0].HasOpenFreeze {
		t.Fatalf("pending-reconciliation eligibility summary=%+v err=%v", items, err)
	}
	var audits int
	if err = fixture.store.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM audit_events
		WHERE action IN ('eligibility.balance_carry_forward.derived',
			'eligibility.balance_carry_forward.evaluated')`).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("carry-forward audit count=%d err=%v", audits, err)
	}
	if _, err = fixture.store.pool.Exec(fixture.ctx, `INSERT INTO eligibility_projection_jobs(
		external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now()-interval '1 second')`, fixture.accountID, fixture.requested); err != nil {
		t.Fatal(err)
	}
	processed, err = fixture.store.ProcessEligibilityProjectionJobs(fixture.ctx, 10, time.Now().UTC().Add(time.Minute),
		AuditActor{Type: "system", ID: "carry-forward-worker"})
	if err != nil || processed != 1 {
		t.Fatalf("idempotent carry retry processed=%d err=%v", processed, err)
	}
	var proofs, evaluations int
	if err = fixture.store.pool.QueryRow(fixture.ctx, `SELECT
		(SELECT count(*) FROM balance_carry_forward_proofs WHERE external_account_id=$1),
		(SELECT count(*) FROM balance_carry_forward_evaluations evaluation
		 JOIN balance_carry_forward_proofs proof ON proof.id=evaluation.proof_id
		 WHERE proof.external_account_id=$1)`, fixture.accountID).Scan(&proofs, &evaluations); err != nil || proofs != 1 || evaluations != 1 {
		t.Fatalf("idempotent carry rows proofs=%d evaluations=%d err=%v", proofs, evaluations, err)
	}
}

func TestBalanceDeltaCarryForwardMatchedNetFactsFinalize(t *testing.T) {
	fixture := seedCarryForwardFixture(t, "20", "20")
	processed, err := fixture.store.ProcessEligibilityProjectionJobs(fixture.ctx, 10, time.Now().UTC().Add(time.Minute),
		AuditActor{Type: "system", ID: "carry-forward-worker"})
	if err != nil || processed != 1 {
		t.Fatalf("matched carry projection processed=%d err=%v", processed, err)
	}
	var status, evaluation, difference string
	var finalized time.Time
	if err = fixture.store.pool.QueryRow(fixture.ctx, `
		SELECT state.eligibility_status,state.finalized_through,
			evaluation.evaluation_status,evaluation.difference_service_units::text
		FROM source_account_eligibility_state state
		JOIN balance_carry_forward_proofs proof ON proof.external_account_id=state.external_account_id
		JOIN balance_carry_forward_evaluations evaluation ON evaluation.proof_id=proof.id
		WHERE state.external_account_id=$1`, fixture.accountID).Scan(
		&status, &finalized, &evaluation, &difference); err != nil {
		t.Fatal(err)
	}
	if status != "active" || !finalized.Equal(fixture.requested) || evaluation != "matched" || difference != "0" {
		t.Fatalf("matched carry status=%s finalized=%s evaluation=%s difference=%s",
			status, finalized, evaluation, difference)
	}
}

func TestBalanceDeltaCarryForwardMissingProofFailsWithoutAdvancing(t *testing.T) {
	fixture := seedCarryForwardFixture(t, "20", "")
	tooEarly := fixture.cutover.Add(25 * time.Minute)
	if _, err := fixture.store.pool.Exec(fixture.ctx, `UPDATE eligibility_projection_jobs
		SET requested_through=$2 WHERE external_account_id=$1`, fixture.accountID, tooEarly); err != nil {
		t.Fatal(err)
	}
	processed, err := fixture.store.ProcessEligibilityProjectionJobs(fixture.ctx, 10, time.Now().UTC().Add(time.Minute),
		AuditActor{Type: "system", ID: "carry-forward-worker"})
	if err != nil || processed != 0 {
		t.Fatalf("missing carry proof processed=%d err=%v", processed, err)
	}
	var status string
	var finalized time.Time
	var proofs int
	if err = fixture.store.pool.QueryRow(fixture.ctx, `SELECT state.eligibility_status,state.finalized_through,
		(SELECT count(*) FROM balance_carry_forward_proofs proof WHERE proof.external_account_id=state.external_account_id)
		FROM source_account_eligibility_state state WHERE state.external_account_id=$1`, fixture.accountID).Scan(
		&status, &finalized, &proofs); err != nil {
		t.Fatal(err)
	}
	if status != "active" || !finalized.Equal(fixture.cutover) || proofs != 0 {
		t.Fatalf("missing proof advanced status=%s finalized=%s proofs=%d", status, finalized, proofs)
	}
	var jobStatus, errorCode string
	if err = fixture.store.pool.QueryRow(fixture.ctx, `SELECT status,COALESCE(last_error_code,'')
		FROM eligibility_projection_jobs WHERE external_account_id=$1`, fixture.accountID).Scan(
		&jobStatus, &errorCode); err != nil || jobStatus != "queued" || errorCode != "BALANCE_PROOF_PENDING" {
		t.Fatalf("missing proof job status=%q error_code=%q err=%v", jobStatus, errorCode, err)
	}
	health, err := fixture.store.EligibilityProjectionHealth(fixture.ctx)
	if err != nil || health.Dead != 0 || health.Queued != 1 {
		t.Fatalf("pending proof health=%+v err=%v", health, err)
	}
}

func TestBalanceDeltaCarryForwardUsesLatestLowerSequenceActualAtSameAsOf(t *testing.T) {
	fixture := seedCarryForwardFixture(t, "20", "")
	const priorID = "44000000-0000-4000-8000-000000000002"
	if _, err := fixture.store.pool.Exec(fixture.ctx, `INSERT INTO balance_reconciliation_checkpoints(
		id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
		checkpoint_kind,baseline_member,as_of,balance_service_units,balance_negative,
		unit_code,cutover_manifest_hash,configuration_hash,reconciliation_status,
		source_sequence,source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES($1,$2,$3,'same-asof-prior-event','same-asof-prior','reconciliation',TRUE,
		$4,140,FALSE,'SUB2_BALANCE_1E8',$5,$6,'pending_finalization',1,
		'same-asof-prior:1',$4,$7,$4)`, priorID, fixture.sourceID, fixture.accountID,
		fixture.proofCycle.ceiling, fixture.manifestHash, fixture.configHash,
		testHash("same-asof-prior-revision")); err != nil {
		t.Fatal(err)
	}
	// seedCarryForwardFixture's account defaults to a legacy bootstrap kind,
	// and this same-as_of reconciliation checkpoint is dated at/after policy
	// start -- through the job queue, reanchorLegacyEligibilityAccountTx's
	// candidate lookup would mistake it for a design-2.4 re-anchor candidate
	// and re-anchor instead of ever reaching the carry-forward proof logic
	// this test (predating that slice) actually exercises. See
	// processEligibilityWithoutReanchor's doc comment.
	processEligibilityWithoutReanchor(t, fixture.store, fixture.ctx, fixture.accountID, fixture.requested,
		AuditActor{Type: "system", ID: "carry-forward-worker"})
	var storedPrior string
	if err := fixture.store.pool.QueryRow(fixture.ctx, `SELECT prior_checkpoint_id::text
		FROM balance_carry_forward_proofs WHERE external_account_id=$1`, fixture.accountID).Scan(&storedPrior); err != nil {
		t.Fatal(err)
	}
	if storedPrior != priorID {
		t.Fatalf("same-asof proof prior=%s want=%s", storedPrior, priorID)
	}
	var realStatus, carryStatus, carryDifference string
	if err := fixture.store.pool.QueryRow(fixture.ctx, `SELECT
		(SELECT evaluation_status FROM balance_checkpoint_evaluations
		 WHERE checkpoint_id=$1),
		(SELECT evaluation.evaluation_status FROM balance_carry_forward_evaluations evaluation
		 JOIN balance_carry_forward_proofs proof ON proof.id=evaluation.proof_id
		 WHERE proof.external_account_id=$2),
		(SELECT evaluation.difference_service_units::text FROM balance_carry_forward_evaluations evaluation
		 JOIN balance_carry_forward_proofs proof ON proof.id=evaluation.proof_id
		 WHERE proof.external_account_id=$2)`, priorID, fixture.accountID).Scan(
		&realStatus, &carryStatus, &carryDifference); err != nil {
		t.Fatal(err)
	}
	if realStatus != "positive_classified_non_cash" || carryStatus != "matched" || carryDifference != "0" {
		t.Fatalf("same-asof unified order real=%s carry=%s difference=%s",
			realStatus, carryStatus, carryDifference)
	}
}

func TestBalanceDeltaCarryForwardRejectsUnmappedFundingVisibility(t *testing.T) {
	fixture := seedCarryForwardFixture(t, "", "")
	const (
		sourceEventID = "45000000-0000-4000-8000-000000000001"
		lotID         = "55000000-0000-4000-8000-000000000001"
	)
	if _, err := fixture.store.pool.Exec(fixture.ctx, `INSERT INTO source_events(
		id,source_instance_id,external_user_id,event_kind,external_event_id,source_status,
		schema_version,source_revision_hash,source_updated_at,observed_at,source_sequence)
		VALUES($1,$2,'carry-user','payment','46000000-0000-4000-8000-000000000001',
		'COMPLETED','source-agent-v3.0',$3,$4,$4,1)`, sourceEventID, fixture.sourceID,
		testHash("unmapped-funding-revision"), fixture.cutover.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.pool.Exec(fixture.ctx, `INSERT INTO funding_lots(
		id,invoice_user_id,external_account_id,source_instance_id,source_event_id,
		external_order_id,currency,original_minor,current_cap_minor,verification_state,
		source_status,source_revision_hash,completed_at,observed_at,eligibility_kind,
		eligibility_cutover_at,verified_cash_minor,consumed_cash_minor,eligibility_revision)
		VALUES($1,$2,$3,$4,$5,'unmapped-funding','CNY',10000,10000,'verified',
		'COMPLETED',$6,$7,$7,'WALLET_CASH',$8,10000,0,1)`, lotID, fixture.userID,
		fixture.accountID, fixture.sourceID, sourceEventID, testHash("unmapped-funding-revision"),
		fixture.cutover.Add(10*time.Minute), fixture.cutover.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.pool.Exec(fixture.ctx, `INSERT INTO funding_lot_consumption_state(
		funding_lot_id,cash_service_units,consumed_service_units,cumulative_cash_numerator,
		rounded_consumed_cash_minor,rounding_remainder_numerator)
		VALUES($1,10,0,0,0,0)`, lotID); err != nil {
		t.Fatal(err)
	}
	var unmappedKind string
	if err := fixture.store.pool.QueryRow(fixture.ctx, `SELECT eligibility_kind FROM funding_lots
		WHERE id=$1`, lotID).Scan(&unmappedKind); err != nil || unmappedKind != "WALLET_CASH" {
		t.Fatalf("unmapped funding kind=%q err=%v", unmappedKind, err)
	}
	processed, err := fixture.store.ProcessEligibilityProjectionJobs(fixture.ctx, 10, time.Now().UTC().Add(time.Minute),
		AuditActor{Type: "system", ID: "carry-forward-worker"})
	if err == nil || !strings.Contains(err.Error(), errBalanceCarryForwardProofInvalid.Error()) || processed != 0 {
		t.Fatalf("unmapped funding visibility processed=%d err=%v", processed, err)
	}
	var finalized time.Time
	if err = fixture.store.pool.QueryRow(fixture.ctx, `SELECT finalized_through
		FROM source_account_eligibility_state WHERE external_account_id=$1`, fixture.accountID).Scan(&finalized); err != nil {
		t.Fatal(err)
	}
	if !finalized.Equal(fixture.cutover) {
		t.Fatalf("unmapped funding advanced finalized boundary=%s", finalized)
	}
	// XM-INV-PROJECTION-FAILURE-GRADING: a single per-account processing
	// error no longer leaves the job status='failed' (which used to trip
	// EligibilityProjectionHealth's Failed count, and /readyz, immediately)
	// -- it is graded like any other transient failure, requeued with
	// backoff (status='queued', attempts=1). Only
	// projectionFailureDeadThreshold consecutive failures would escalate it
	// to Dead; see TestProjectionFailureGradingEscalatesToDeadAfterConsecutiveFailures
	// in projection_failure_grading_integration_test.go for that ladder.
	var jobStatus string
	var attempts int64
	var lastError string
	if err = fixture.store.pool.QueryRow(fixture.ctx, `SELECT status,attempts,COALESCE(last_error,'')
		FROM eligibility_projection_jobs WHERE external_account_id=$1`, fixture.accountID).Scan(
		&jobStatus, &attempts, &lastError); err != nil {
		t.Fatal(err)
	}
	if jobStatus != "queued" || attempts != 1 || !strings.Contains(lastError, errBalanceCarryForwardProofInvalid.Error()) {
		t.Fatalf("invalid funding proof job status=%q attempts=%d last_error=%q", jobStatus, attempts, lastError)
	}
	health, err := fixture.store.EligibilityProjectionHealth(fixture.ctx)
	if err != nil || health.Dead != 0 || health.Retrying != 1 {
		t.Fatalf("invalid funding proof health=%+v err=%v", health, err)
	}
}

func TestBalanceDeltaCarryForwardUsesMappedFundingVisibility(t *testing.T) {
	fixture := seedCarryForwardFixture(t, "", "")
	if err := fixture.store.ProvisionSourceStream(fixture.ctx, fixture.sourceID, "payments",
		AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	paymentEvent := SourceBatchEvent{EventID: "47000000-0000-4000-8000-000000000001",
		EntityType: "payment_order", Operation: "upsert", PayloadHash: testHash("mapped-funding-revision"),
		PayloadCiphertext: []byte("mapped-funding-visibility-proof"), ObservedAt: fixture.cutover.Add(20 * time.Minute)}
	chain := newV3TestChain()
	paymentCycle := chain.commit(t, fixture.store, fixture.ctx, fixture.sourceID, "payments",
		"48000000-0000-4000-8000-000000000001", fixture.cutover.Add(20*time.Minute), []SourceBatchEvent{paymentEvent})
	observed, err := fixture.store.ObserveFundingLot(fixture.ctx, SourceObservation{
		Lot: domain.FundingLot{PrincipalID: fixture.userID, SourceInstanceID: fixture.sourceID,
			SourceType: domain.SourceSub2API, ExternalOrderID: "mapped-funding", Currency: domain.CurrencyCNY,
			OriginalMinor: 20_000, CurrentCapMinor: 20_000, Verification: domain.VerificationVerified,
			SourceStatus: "COMPLETED", SourceRevision: paymentEvent.PayloadHash,
			CompletedAt: fixture.cutover.Add(10 * time.Minute), ObservedAt: fixture.cutover.Add(20 * time.Minute)},
		ExternalUserID: "carry-user", EventKind: "payment", ExternalEventID: paymentEvent.EventID,
		SchemaVersion: "source-agent-v3.0", SourceUpdatedAt: fixture.cutover.Add(10 * time.Minute),
		SourceSequence: 1, EligibilityKind: domain.EligibilityWalletCash, CashServiceUnits: "20",
		WalletUnitCode: "SUB2_BALANCE_1E8", CutoverManifestHash: fixture.manifestHash,
		ConfigurationHash: fixture.configHash, SourceCursor: "mapped-funding:1",
		BatchID: paymentCycle.batchID, ScanCycleID: paymentCycle.cycleID,
		StreamWatermarkAt: fixture.cutover.Add(20 * time.Minute),
	}, AuditActor{Type: "source_connector", ID: fixture.sourceID})
	if err != nil {
		t.Fatal(err)
	}
	if observed.Lot.EligibilityKind != domain.EligibilityWalletCash {
		t.Fatalf("mapped funding was not post-policy wallet cash: %+v", observed.Lot)
	}
	markV3CycleProcessed(t, fixture.store, fixture.ctx, fixture.sourceID, "payments", paymentCycle)
	processed, err := fixture.store.ProcessEligibilityProjectionJobs(fixture.ctx, 10, time.Now().UTC().Add(time.Minute),
		AuditActor{Type: "system", ID: "carry-forward-worker"})
	if err != nil || processed != 1 {
		t.Fatalf("mapped funding carry processed=%d err=%v", processed, err)
	}
	var proofs, postCutoverCredits int
	var proofAt, paymentVisibility time.Time
	if err = fixture.store.pool.QueryRow(fixture.ctx, `SELECT
		(SELECT count(*) FROM balance_carry_forward_proofs WHERE external_account_id=$1),
		(SELECT as_of FROM balance_carry_forward_proofs WHERE external_account_id=$1),
		(SELECT scan_ceiling_at FROM source_economic_scan_cycles
		 WHERE source_instance_id=$2 AND stream_id='payments' AND scan_cycle_id=$3::uuid),
		(SELECT count(*) FROM source_credit_events WHERE external_account_id=$1 AND event_time>$4)`,
		fixture.accountID, fixture.sourceID, paymentCycle.cycleID, fixture.cutover).Scan(
		&proofs, &proofAt, &paymentVisibility, &postCutoverCredits); err != nil || proofs != 1 {
		t.Fatalf("mapped funding carry proofs=%d err=%v", proofs, err)
	}
	if proofAt.Before(paymentVisibility) || postCutoverCredits != 0 {
		t.Fatalf("mapped funding visibility proof_at=%s payment_visibility=%s post_cutover_credits=%d",
			proofAt, paymentVisibility, postCutoverCredits)
	}
}

func TestBalanceEvidenceEvaluatesCarryBeforeLaterRealCheckpoint(t *testing.T) {
	fixture := seedCarryForwardFixture(t, "", "20")
	proofTx, err := fixture.store.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = proofTx.Exec(fixture.ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,43))`, fixture.accountID); err != nil {
		_ = proofTx.Rollback(context.Background())
		t.Fatal(err)
	}
	account, err := getEligibilityAccountTx(fixture.ctx, proofTx, fixture.accountID, true)
	if err == nil {
		err = ensureBalanceCarryForwardProofTx(fixture.ctx, proofTx, account, fixture.requested,
			AuditActor{Type: "system", ID: "carry-forward-worker"})
	}
	if err != nil {
		_ = proofTx.Rollback(context.Background())
		t.Fatal(err)
	}
	if err = proofTx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	chain := newV3TestChain()
	var balanceSequence int64
	var balanceHash string
	if err = fixture.store.pool.QueryRow(fixture.ctx, `SELECT sequence,last_batch_hash
		FROM source_ingest_state WHERE source_instance_id=$1 AND stream_id='balances'`, fixture.sourceID).Scan(
		&balanceSequence, &balanceHash); err != nil {
		t.Fatal(err)
	}
	chain.sequence["balances"] = balanceSequence
	chain.hash["balances"] = balanceHash
	realEvent := SourceBatchEvent{EventID: "76000000-0000-4000-8000-000000000001",
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("later-real-balance"),
		PayloadCiphertext: []byte("later-real-balance-checkpoint"), ObservedAt: fixture.cutover.Add(40 * time.Minute)}
	realCycle := chain.commit(t, fixture.store, fixture.ctx, fixture.sourceID, "balances",
		"77000000-0000-4000-8000-000000000001", fixture.cutover.Add(40*time.Minute), []SourceBatchEvent{realEvent})
	if err = fixture.store.ObserveBalanceCheckpoint(fixture.ctx, BalanceCheckpointObservation{
		SourceInstanceID: fixture.sourceID, ExternalUserID: "carry-user", ExternalEventID: realEvent.EventID,
		CheckpointID: strings.Repeat("7", 64) + ":1", CheckpointKind: "reconciliation",
		BalanceServiceUnits: "100", UnitCode: "SUB2_BALANCE_1E8", BaselineMember: true,
		SourceSnapshotID: testHash(realCycle.cycleID), SnapshotRowCount: "1",
		AsOf: fixture.cutover.Add(40 * time.Minute), ObservedAt: fixture.cutover.Add(40 * time.Minute),
		StreamWatermarkAt: fixture.cutover.Add(40 * time.Minute), SourceCursor: "later-real:1",
		SourceRevision: realEvent.PayloadHash, CutoverManifestHash: fixture.manifestHash,
		ConfigurationHash: fixture.configHash, SourceSequence: chain.sequence["balances"],
		BatchID: realCycle.batchID, ScanCycleID: realCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: fixture.sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, fixture.store, fixture.ctx, fixture.sourceID, "balances", realCycle)
	// realEvent's checkpoint is also dated at/after policy start; see
	// processEligibilityWithoutReanchor's doc comment for why the job queue
	// is bypassed here, same as the carry-forward-proof call above.
	processEligibilityWithoutReanchor(t, fixture.store, fixture.ctx, fixture.accountID, fixture.requested,
		AuditActor{Type: "system", ID: "carry-forward-worker"})
	var carryStatus, realStatus string
	if err = fixture.store.pool.QueryRow(fixture.ctx, `SELECT
		(SELECT evaluation.evaluation_status FROM balance_carry_forward_evaluations evaluation
		 JOIN balance_carry_forward_proofs proof ON proof.id=evaluation.proof_id
		 WHERE proof.external_account_id=$1),
		(SELECT evaluation.evaluation_status FROM balance_checkpoint_evaluations evaluation
		 JOIN balance_reconciliation_checkpoints checkpoint ON checkpoint.id=evaluation.checkpoint_id
		 WHERE checkpoint.external_event_id=$2)`, fixture.accountID, realEvent.EventID).Scan(
		&carryStatus, &realStatus); err != nil {
		t.Fatal(err)
	}
	if carryStatus != "positive_classified_non_cash" || realStatus != "matched" {
		t.Fatalf("interleaved evidence carry=%s real=%s", carryStatus, realStatus)
	}
}

func carryProofKey(cycleID, accountID string) string {
	return "carry-forward:" + cycleID + ":" + strings.ToLower(accountID)
}
