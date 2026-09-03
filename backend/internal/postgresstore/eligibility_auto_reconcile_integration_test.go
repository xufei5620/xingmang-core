package postgresstore

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
)

// autoReconcileUUID generates deterministic UUID-shaped literals for this
// file's fixtures, matching balance_blip_integration_test.go's blipUUID
// convention ('prefix' groups by role: '1'=source instance, '2'=user,
// '3'=account, '8'/'9'=event/cycle ids, '5'/'6'/'7'=funding-lot-related
// ids). Every test in this file gets its own fresh schema (integrationStore's
// DROP SCHEMA CASCADE), so reusing the same n across different test
// functions in this file is safe.
func autoReconcileUUID(prefix rune, n int) string {
	return fmt.Sprintf("%c0000000-0000-4000-8000-%012d", prefix, n)
}

// newAutoReconcileFixture bootstraps a POLICY_ANCHOR account with a ZERO
// opening balance via a real ObserveBalanceCheckpoint call, then runs one
// projection job so the anchor checkpoint evaluates 'matched' (its
// difference is exactly zero -- unlike balance_blip_integration_test.go's
// fixture, no XM-INV-ANCHOR-BALANCE self-heal credit is synthesized here,
// keeping this file's own scenarios free of an unrelated non-cash pool to
// account for). The account starts 'active' with real prior balance-evidence
// history (one matched evaluation) -- production shape, not
// XM-INV-ANCHOR-BALANCE's own already-tested fresh-bootstrap case.
func newAutoReconcileFixture(t *testing.T) (store *Store, ctx context.Context, sourceID, accountID, userID, manifestHash, configHash string, anchorAt time.Time, chain *v3TestChain) {
	t.Helper()
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow.Add(-2 * time.Hour)
	store, ctx = integrationStoreWithPolicyStart(t, policyStart)
	sourceID = autoReconcileUUID('1', 800)
	userID = autoReconcileUUID('2', 800)
	accountID = autoReconcileUUID('3', 800)
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','auto-reconcile-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','auto-reconcile-user')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,'800','test','verified')`, accountID, userID, sourceID); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"balances", "usage", "credits", "payments"} {
		if err := store.ProvisionSourceStream(ctx, sourceID, stream, AuditActor{Type: "system", ID: "test"}); err != nil {
			t.Fatal(err)
		}
	}

	cutoverAt := policyStart.Add(-10 * time.Hour)
	chain = newV3TestChain()
	manifestEvent := SourceBatchEvent{EventID: autoReconcileUUID('8', 801),
		EntityType: "cutover_manifest", Operation: "upsert", PayloadHash: testHash("auto-reconcile-manifest-event"),
		PayloadCiphertext: bytes.Repeat([]byte{1}, 32), ObservedAt: cutoverAt}
	manifestCycle := chain.commit(t, store, ctx, sourceID, "balances", autoReconcileUUID('9', 801), cutoverAt, []SourceBatchEvent{manifestEvent})
	manifestHash = testHash("auto-reconcile-manifest")
	configHash = testHash("auto-reconcile-config")
	snapshotHash := testHash("auto-reconcile-snapshot")
	if err := store.RegisterCutoverManifest(ctx, CutoverManifest{
		SourceInstanceID: sourceID, ManifestHash: manifestHash, SourceRuntimeVersion: "v3-test",
		ProjectionContract: "sub2api-economic-v4", ConfigurationHash: configHash,
		UnitCode: "SUB2_BALANCE_1E8", PaymentsCeiling: "p0", UsageCeiling: "u0",
		CreditsCeiling: "c0", BalancesCeiling: "b0", BaselineSnapshotID: snapshotHash,
		BaselineSnapshotHash: snapshotHash, BaselineRowCount: 0, SigningKeyID: "ignored-payload-key",
		CutoverAt: cutoverAt, DatabaseClock: cutoverAt, StreamWatermarkAt: cutoverAt,
		ExternalEventID: manifestEvent.EventID, BatchID: manifestCycle.batchID,
		ScanCycleID: manifestCycle.cycleID, SourceRevision: manifestEvent.PayloadHash,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", manifestCycle)

	anchorAt = policyStart.Add(1 * time.Hour)
	anchorEvent := SourceBatchEvent{EventID: autoReconcileUUID('8', 802),
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("auto-reconcile-anchor-event"),
		PayloadCiphertext: bytes.Repeat([]byte{2}, 32), ObservedAt: anchorAt}
	anchorCycle := chain.commit(t, store, ctx, sourceID, "balances", autoReconcileUUID('9', 802), anchorAt, []SourceBatchEvent{anchorEvent})
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "800", ExternalEventID: anchorEvent.EventID,
		CheckpointID: "auto-reconcile-anchor", CheckpointKind: "reconciliation", BaselineMember: true,
		BalanceServiceUnits: "0", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(anchorCycle.cycleID),
		SnapshotRowCount: "1", AsOf: anchorAt, ObservedAt: anchorAt, StreamWatermarkAt: anchorAt,
		SourceCursor: "balance:800:anchor", SourceRevision: anchorEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: anchorCycle.batchID, ScanCycleID: anchorCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", anchorCycle)

	worker := AuditActor{Type: "system", ID: "test-worker"}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now())`, accountID, anchorAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	processed, err := store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute), worker)
	if err != nil || processed != 1 {
		t.Fatalf("fixture: anchor processing processed=%d err=%v", processed, err)
	}
	var anchorStatus, accountStatus string
	if err := store.pool.QueryRow(ctx, `
		SELECT evaluation_status FROM balance_checkpoint_evaluations
		WHERE checkpoint_id=(SELECT id FROM balance_reconciliation_checkpoints
			WHERE external_account_id=$1 AND checkpoint_id='auto-reconcile-anchor')`,
		accountID).Scan(&anchorStatus); err != nil || anchorStatus != "matched" {
		t.Fatalf("fixture: anchor checkpoint evaluation status=%q err=%v, want matched", anchorStatus, err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_status FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&accountStatus); err != nil || accountStatus != "active" {
		t.Fatalf("fixture: account status=%q err=%v, want active", accountStatus, err)
	}
	return store, ctx, sourceID, accountID, userID, manifestHash, configHash, anchorAt, chain
}

// insertCreditEventDirect inserts a source_credit_events row directly
// (bypassing ObserveCreditEvent's scan-cycle plumbing, same bypass rationale
// as balance_blip_integration_test.go's insertUsageEventDirect) so a
// scenario can put real expected balance behind an account without a full
// scan cycle per credit.
func insertCreditEventDirect(t *testing.T, store *Store, ctx context.Context, sourceID, accountID, creditID string,
	eventTime time.Time, units, creditKind string, sourceSequence int64, manifestHash, configHash string) string {
	t.Helper()
	id := randomUUID()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_credit_events(
			id,source_instance_id,external_account_id,external_event_id,external_credit_id,
			event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
			credit_kind,source_sequence,source_cursor,stream_watermark_at,
			source_revision_hash,observed_at)
		VALUES($1,$2,$3,$4,$4,$5,$6::numeric,'SUB2_BALANCE_1E8',$7,$8,$9,$10,$11,$5,$12,$5)`,
		id, sourceID, accountID, creditID, eventTime, units, manifestHash, configHash,
		creditKind, sourceSequence, "cursor:"+creditID, testHash(creditID)); err != nil {
		t.Fatal(err)
	}
	return id
}

type pendingReconciliationRow struct {
	status, reason, triggerType, triggerID, detail string
	// sinceSet is false when pending_reconciliation_since is NULL in the
	// database (scanned via COALESCE to the Unix epoch sentinel, matching
	// EligibilityProjectionHealth's own NULL-timestamp convention elsewhere
	// in this package -- Go's time.Time{} zero value is year 1, not the Unix
	// epoch, so a plain since.IsZero() would never be true for a COALESCEd
	// column).
	since              time.Time
	sinceSet           bool
	consecutiveMatches int
}

func readPendingReconciliation(t *testing.T, store *Store, ctx context.Context, accountID string) pendingReconciliationRow {
	t.Helper()
	var row pendingReconciliationRow
	if err := store.pool.QueryRow(ctx, `
		SELECT eligibility_status,COALESCE(pending_reconciliation_reason,''),
			COALESCE(pending_reconciliation_trigger_type,''),COALESCE(pending_reconciliation_trigger_id,''),
			COALESCE(pending_reconciliation_detail,''),COALESCE(pending_reconciliation_since,'epoch'::timestamptz),
			pending_reconciliation_consecutive_matches
		FROM source_account_eligibility_state WHERE external_account_id=$1`, accountID).Scan(
		&row.status, &row.reason, &row.triggerType, &row.triggerID, &row.detail, &row.since, &row.consecutiveMatches); err != nil {
		t.Fatal(err)
	}
	row.sinceSet = !row.since.Equal(time.Unix(0, 0).UTC())
	return row
}

// enterAutoReconcilePending puts the fixture account into
// not_invoiceable_pending_reconciliation via a real code path: a BONUS
// credit establishes a nonzero expected balance, then a checkpoint reporting
// less than that expected balance evaluates negative_frozen. Returns the
// as_of of the negative checkpoint, so callers can add later evidence after
// it.
func enterAutoReconcilePending(t *testing.T, store *Store, ctx context.Context, sourceID, accountID, manifestHash, configHash string, anchorAt time.Time) time.Time {
	t.Helper()
	creditAt := anchorAt.Add(5 * time.Minute)
	insertCreditEventDirect(t, store, ctx, sourceID, accountID, "auto-reconcile-credit",
		creditAt, "100", "BONUS", 2, manifestHash, configHash)
	negativeAt := anchorAt.Add(10 * time.Minute)
	insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "auto-reconcile-negative",
		negativeAt, "60", 2, manifestHash, configHash) // expected 100, reported 60: difference -40

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = evaluatePendingBalanceEvidenceTx(ctx, tx, accountID, negativeAt.Add(time.Minute),
		AuditActor{Type: "system", ID: "test-worker"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	row := readPendingReconciliation(t, store, ctx, accountID)
	if row.status != "not_invoiceable_pending_reconciliation" || row.reason != "UNKNOWN_NEGATIVE_BALANCE" ||
		row.triggerType != "balance_checkpoint" || row.triggerID != "auto-reconcile-negative" ||
		row.detail == "" || !row.sinceSet || row.consecutiveMatches != 0 {
		t.Fatalf("account did not enter pending reconciliation as expected: %+v", row)
	}
	if count := openFreezeCount(t, store, ctx, accountID); count != 0 {
		t.Fatalf("entering pending reconciliation must not open a manual freeze: open freezes=%d", count)
	}
	var evaluationStatus, difference string
	if err := store.pool.QueryRow(ctx, `
		SELECT evaluation_status,difference_service_units::text FROM balance_checkpoint_evaluations
		WHERE checkpoint_id=(SELECT id FROM balance_reconciliation_checkpoints
			WHERE external_account_id=$1 AND checkpoint_id='auto-reconcile-negative')`,
		accountID).Scan(&evaluationStatus, &difference); err != nil {
		t.Fatal(err)
	}
	if evaluationStatus != "negative_frozen" || difference != "-40" {
		t.Fatalf("negative checkpoint evaluation status=%q difference=%s, want negative_frozen/-40", evaluationStatus, difference)
	}
	return negativeAt
}

// TestPendingReconciliationSingleMatchDoesNotExit covers design section
// 3(A)'s "single reconciliation does not exit" test-matrix case: after the
// account enters not_invoiceable_pending_reconciliation, one subsequent real
// balance evidence item evaluating matched increments the consecutive-match
// counter but must not, by itself, return the account to active.
func TestPendingReconciliationSingleMatchDoesNotExit(t *testing.T) {
	store, ctx, sourceID, accountID, _, manifestHash, configHash, anchorAt, _ := newAutoReconcileFixture(t)
	negativeAt := enterAutoReconcilePending(t, store, ctx, sourceID, accountID, manifestHash, configHash, anchorAt)

	matchAt := negativeAt.Add(10 * time.Minute)
	insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "auto-reconcile-match1",
		matchAt, "100", 3, manifestHash, configHash) // expected still 100: reconciles cleanly

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = evaluatePendingBalanceEvidenceTx(ctx, tx, accountID, matchAt.Add(time.Minute),
		AuditActor{Type: "system", ID: "test-worker"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	row := readPendingReconciliation(t, store, ctx, accountID)
	if row.status != "not_invoiceable_pending_reconciliation" || row.consecutiveMatches != 1 {
		t.Fatalf("single match must not exit pending reconciliation: %+v", row)
	}
	if row.reason != "UNKNOWN_NEGATIVE_BALANCE" || row.triggerID != "auto-reconcile-negative" {
		t.Fatalf("pending reconciliation columns must stay from original entry after a non-exiting match: %+v", row)
	}
	var matchStatus string
	if err := store.pool.QueryRow(ctx, `
		SELECT evaluation_status FROM balance_checkpoint_evaluations
		WHERE checkpoint_id=(SELECT id FROM balance_reconciliation_checkpoints
			WHERE external_account_id=$1 AND checkpoint_id='auto-reconcile-match1')`,
		accountID).Scan(&matchStatus); err != nil || matchStatus != "matched" {
		t.Fatalf("match1 checkpoint evaluation status=%q err=%v, want matched", matchStatus, err)
	}
}

// TestPendingReconciliationTwoConsecutiveMatchesExit covers design section
// 3(A)'s "two consecutive reconciliations exit, and not earlier" case: the
// second consecutive matched evaluation (after the first already proved,
// alone, insufficient) clears the five pending_reconciliation_* columns,
// returns the account to active, and writes the exit audit event.
func TestPendingReconciliationTwoConsecutiveMatchesExit(t *testing.T) {
	store, ctx, sourceID, accountID, _, manifestHash, configHash, anchorAt, _ := newAutoReconcileFixture(t)
	negativeAt := enterAutoReconcilePending(t, store, ctx, sourceID, accountID, manifestHash, configHash, anchorAt)

	match1At := negativeAt.Add(10 * time.Minute)
	insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "auto-reconcile-match1",
		match1At, "100", 3, manifestHash, configHash)
	runEvaluate := func(through time.Time) {
		t.Helper()
		tx, err := store.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if err = evaluatePendingBalanceEvidenceTx(ctx, tx, accountID, through,
			AuditActor{Type: "system", ID: "test-worker"}); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	runEvaluate(match1At.Add(time.Minute))
	if row := readPendingReconciliation(t, store, ctx, accountID); row.status != "not_invoiceable_pending_reconciliation" || row.consecutiveMatches != 1 {
		t.Fatalf("after first match, must still be pending with matches=1: %+v", row)
	}

	match2At := match1At.Add(10 * time.Minute)
	insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "auto-reconcile-match2",
		match2At, "100", 4, manifestHash, configHash)
	runEvaluate(match2At.Add(time.Minute))

	row := readPendingReconciliation(t, store, ctx, accountID)
	if row.status != "active" || row.reason != "" || row.triggerType != "" || row.triggerID != "" ||
		row.detail != "" || row.sinceSet || row.consecutiveMatches != 0 {
		t.Fatalf("two consecutive matches must exit to active with all five columns cleared: %+v", row)
	}
	var exitedAudits int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.pending_reconciliation.exited' AND object_id=$1`,
		accountID).Scan(&exitedAudits); err != nil || exitedAudits != 1 {
		t.Fatalf("exit audit count=%d err=%v, want 1", exitedAudits, err)
	}
	if count := openFreezeCount(t, store, ctx, accountID); count != 0 {
		t.Fatalf("open freeze count=%d, want 0", count)
	}
}

// TestPendingReconciliationOpenFreezeBlocksAutoExit covers design section
// 3(A)'s "an unrelated open freeze prevents auto-exit despite reaching the
// match count" case. Although not_invoiceable_pending_reconciliation and
// 'frozen' are mutually exclusive on eligibility_status (enterPendingReconciliationTx
// never downgrades a frozen account), an account can still carry an open
// eligibility_freezes row without eligibility_status being 'frozen' --
// freezeRefundedLotTx (SOURCE_REFUND) never touches eligibility_status, so a
// real refund exposure and this state can coexist. A directly-inserted open
// freeze row (matching this package's own precedent for constructing this
// "account has both an unrelated open freeze and this issue" shape, e.g.
// balance_anchor_repair_integration_test.go's account B) stands in for that
// real freeze reason here, to isolate the auto-exit guard from also
// reconstructing a full SOURCE_REFUND fixture.
func TestPendingReconciliationOpenFreezeBlocksAutoExit(t *testing.T) {
	store, ctx, sourceID, accountID, _, manifestHash, configHash, anchorAt, _ := newAutoReconcileFixture(t)
	negativeAt := enterAutoReconcilePending(t, store, ctx, sourceID, accountID, manifestHash, configHash, anchorAt)

	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_freezes(id,external_account_id,funding_lot_id,freeze_reason,trigger_object_type,trigger_object_id,source_revision_hash)
		VALUES($1,$2,NULL,'SOURCE_GAP','balance_checkpoint','unrelated-source-gap',$3)`,
		randomUUID(), accountID, testHash("unrelated-source-gap")); err != nil {
		t.Fatal(err)
	}

	match1At := negativeAt.Add(10 * time.Minute)
	insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "auto-reconcile-match1",
		match1At, "100", 3, manifestHash, configHash)
	match2At := match1At.Add(10 * time.Minute)
	insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "auto-reconcile-match2",
		match2At, "100", 4, manifestHash, configHash)

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = evaluatePendingBalanceEvidenceTx(ctx, tx, accountID, match2At.Add(time.Minute),
		AuditActor{Type: "system", ID: "test-worker"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	row := readPendingReconciliation(t, store, ctx, accountID)
	if row.status != "not_invoiceable_pending_reconciliation" || row.consecutiveMatches != 2 {
		t.Fatalf("two matches reached but an unrelated open freeze must block auto-exit: %+v", row)
	}
	var exitedAudits int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.pending_reconciliation.exited' AND object_id=$1`,
		accountID).Scan(&exitedAudits); err != nil || exitedAudits != 0 {
		t.Fatalf("exit audit count=%d err=%v, want 0 (blocked by the open freeze)", exitedAudits, err)
	}
	if count := openFreezeCount(t, store, ctx, accountID); count != 1 {
		t.Fatalf("open freeze count=%d, want 1 (the unrelated freeze, untouched)", count)
	}
}

// TestPendingReconciliationDoesNotDowngradeAFrozenAccount covers design
// section 3(A)'s "frozen takes priority, never downgraded" case: an account
// that is already genuinely frozen (a real, distinct freeze reason) stays
// frozen when a negative balance difference arrives -- the evidence is still
// classified negative_frozen (an honest record of what this piece of
// evidence showed), but the account-level handling defers entirely to the
// existing freeze.
func TestPendingReconciliationDoesNotDowngradeAFrozenAccount(t *testing.T) {
	store, ctx, sourceID, accountID, _, manifestHash, configHash, anchorAt, _ := newAutoReconcileFixture(t)

	freezeTx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = freezeEligibilityTx(ctx, freezeTx, accountID, "", "SOURCE_GAP",
		"balance_checkpoint", "frozen-marker", "", AuditActor{Type: "system", ID: "test-worker"}); err != nil {
		t.Fatal(err)
	}
	if err = freezeTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var statusAfterFreeze string
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_status FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&statusAfterFreeze); err != nil || statusAfterFreeze != "frozen" {
		t.Fatalf("fixture: account status=%q err=%v, want frozen", statusAfterFreeze, err)
	}

	creditAt := anchorAt.Add(5 * time.Minute)
	insertCreditEventDirect(t, store, ctx, sourceID, accountID, "auto-reconcile-credit",
		creditAt, "100", "BONUS", 2, manifestHash, configHash)
	negativeAt := anchorAt.Add(10 * time.Minute)
	insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "auto-reconcile-negative",
		negativeAt, "60", 2, manifestHash, configHash)

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = evaluatePendingBalanceEvidenceTx(ctx, tx, accountID, negativeAt.Add(time.Minute),
		AuditActor{Type: "system", ID: "test-worker"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	row := readPendingReconciliation(t, store, ctx, accountID)
	if row.status != "frozen" || row.reason != "" || row.triggerType != "" || row.triggerID != "" ||
		row.detail != "" || row.sinceSet || row.consecutiveMatches != 0 {
		t.Fatalf("a genuinely frozen account must not be downgraded to pending reconciliation: %+v", row)
	}
	var evaluationStatus string
	if err := store.pool.QueryRow(ctx, `
		SELECT evaluation_status FROM balance_checkpoint_evaluations
		WHERE checkpoint_id=(SELECT id FROM balance_reconciliation_checkpoints
			WHERE external_account_id=$1 AND checkpoint_id='auto-reconcile-negative')`,
		accountID).Scan(&evaluationStatus); err != nil || evaluationStatus != "negative_frozen" {
		t.Fatalf("negative checkpoint evaluation status=%q err=%v, want negative_frozen (still recorded honestly)", evaluationStatus, err)
	}
	if count := openFreezeCount(t, store, ctx, accountID); count != 1 {
		t.Fatalf("open freeze count=%d, want 1 (the original SOURCE_GAP freeze, untouched)", count)
	}
}

// observeSimpleCreditEvent/observeSimpleUsageEvent/observeSimpleCheckpoint
// commit a real scan cycle and observe one fact through it via the real
// Observe*/ObserveBalanceCheckpoint entrypoints. Unlike this file's other
// direct-insert helpers (insertCreditEventDirect,
// balance_blip_integration_test.go's insertUsageEventDirect/
// insertReconciliationCheckpoint), these are required whenever a scenario
// goes on to exercise the real ProcessEligibilityProjectionJobs entrypoint:
// ensureBalanceCarryForwardProofTx's own completeness/visibility checks
// require every usage/credit/payment fact and balance checkpoint to be
// reachable through a published scan cycle, which the direct-insert bypass
// helpers deliberately skip (safe only paired with a direct
// evaluatePendingBalanceEvidenceTx/reprojectEligibilityTx call -- see
// balance_blip_integration_test.go's own insertReconciliationCheckpoint doc
// comment, and TestBalanceBlipProductionCascadeDoesNotFreezeAccount for the
// real-entrypoint precedent this mirrors).
func observeSimpleCreditEvent(t *testing.T, store *Store, ctx context.Context, chain *v3TestChain, sourceID, manifestHash, configHash, creditID, creditKind string,
	eventTime time.Time, units string, sourceSequence int64) {
	t.Helper()
	event := SourceBatchEvent{EventID: autoReconcileUUID('8', 820+int(sourceSequence)),
		EntityType: "credit_event", Operation: "upsert", PayloadHash: testHash("auto-reconcile-credit-" + creditID),
		PayloadCiphertext: bytes.Repeat([]byte{byte(20 + sourceSequence)}, 32), ObservedAt: eventTime}
	cycle := chain.commit(t, store, ctx, sourceID, "credits", autoReconcileUUID('9', 820+int(sourceSequence)), eventTime, []SourceBatchEvent{event})
	if err := store.ObserveCreditEvent(ctx, CreditObservation{
		SourceInstanceID: sourceID, ExternalUserID: "800", ExternalEventID: event.EventID,
		ExternalCreditID: creditID, EventTime: eventTime, ObservedAt: eventTime, StreamWatermarkAt: eventTime,
		ServiceUnits: units, UnitCode: "SUB2_BALANCE_1E8", CreditKind: creditKind,
		SourceCursor: "credit:" + creditID, SourceRevision: event.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: sourceSequence,
		BatchID: cycle.batchID, ScanCycleID: cycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "credits", cycle)
}

func observeSimpleUsageEvent(t *testing.T, store *Store, ctx context.Context, chain *v3TestChain, sourceID, accountID, manifestHash, configHash, usageID string,
	eventTime time.Time, units string, sourceSequence int64) string {
	t.Helper()
	event := SourceBatchEvent{EventID: autoReconcileUUID('8', 830+int(sourceSequence)),
		EntityType: "usage_event", Operation: "upsert", PayloadHash: testHash("auto-reconcile-usage-" + usageID),
		PayloadCiphertext: bytes.Repeat([]byte{byte(40 + sourceSequence)}, 32), ObservedAt: eventTime}
	cycle := chain.commit(t, store, ctx, sourceID, "usage", autoReconcileUUID('9', 830+int(sourceSequence)), eventTime, []SourceBatchEvent{event})
	if err := store.ObserveUsageEvent(ctx, UsageObservation{
		SourceInstanceID: sourceID, ExternalUserID: "800", ExternalEventID: event.EventID,
		ExternalUsageID: usageID, EventTime: eventTime, ObservedAt: eventTime, StreamWatermarkAt: eventTime,
		ServiceUnits: units, UnitCode: "SUB2_BALANCE_1E8", BillingScope: "wallet",
		SourceCursor: "usage:" + usageID, SourceRevision: event.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: sourceSequence,
		BatchID: cycle.batchID, ScanCycleID: cycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "usage", cycle)
	var id string
	if err := store.pool.QueryRow(ctx, `SELECT id FROM source_usage_events WHERE external_account_id=$1 AND external_usage_id=$2`,
		accountID, usageID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func observeSimpleCheckpoint(t *testing.T, store *Store, ctx context.Context, chain *v3TestChain, sourceID, manifestHash, configHash, checkpointID string,
	asOf time.Time, balance string, sourceSequence int64) {
	t.Helper()
	event := SourceBatchEvent{EventID: autoReconcileUUID('8', 840+int(sourceSequence)),
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("auto-reconcile-checkpoint-" + checkpointID),
		PayloadCiphertext: bytes.Repeat([]byte{byte(60 + sourceSequence)}, 32), ObservedAt: asOf}
	cycle := chain.commit(t, store, ctx, sourceID, "balances", autoReconcileUUID('9', 840+int(sourceSequence)), asOf, []SourceBatchEvent{event})
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "800", ExternalEventID: event.EventID,
		CheckpointID: checkpointID, CheckpointKind: "reconciliation", BaselineMember: false,
		BalanceServiceUnits: balance, UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(cycle.cycleID),
		SnapshotRowCount: "1", AsOf: asOf, ObservedAt: asOf, StreamWatermarkAt: asOf,
		SourceCursor: "balance:" + checkpointID, SourceRevision: event.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: sourceSequence,
		BatchID: cycle.batchID, ScanCycleID: cycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", cycle)
}

// TestPendingReconciliationDoesNotStickReadiness covers design section
// 3(A)'s readyz/EligibilityProjectionHealth non-regression case, exercised
// through the real ProcessEligibilityProjectionJobs entrypoint (not a direct
// evaluatePendingBalanceEvidenceTx call): processEligibilityProjectionJob
// unconditionally deletes its own eligibility_projection_jobs row on success
// regardless of which eligibility_status the account ends up in, so
// not_invoiceable_pending_reconciliation never leaves a row behind for
// EligibilityProjectionHealth (the query readyz's Readiness closure reads,
// cmd/api/runtime.go) to see.
func TestPendingReconciliationDoesNotStickReadiness(t *testing.T) {
	store, ctx, sourceID, accountID, _, manifestHash, configHash, anchorAt, chain := newAutoReconcileFixture(t)
	worker := AuditActor{Type: "system", ID: "test-worker"}

	creditAt := anchorAt.Add(5 * time.Minute)
	observeSimpleCreditEvent(t, store, ctx, chain, sourceID, manifestHash, configHash, "auto-reconcile-credit", "BONUS",
		creditAt, "100", 2)
	negativeAt := anchorAt.Add(10 * time.Minute)
	observeSimpleCheckpoint(t, store, ctx, chain, sourceID, manifestHash, configHash, "auto-reconcile-negative",
		negativeAt, "60", 2)

	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now())`, accountID, negativeAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	processed, err := store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute), worker)
	if err != nil || processed != 1 {
		t.Fatalf("projection processed=%d err=%v", processed, err)
	}

	var status string
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_status FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&status); err != nil || status != "not_invoiceable_pending_reconciliation" {
		t.Fatalf("account status=%q err=%v, want not_invoiceable_pending_reconciliation", status, err)
	}
	var jobRows int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM eligibility_projection_jobs
		WHERE external_account_id=$1`, accountID).Scan(&jobRows); err != nil || jobRows != 0 {
		t.Fatalf("job rows=%d err=%v, want 0 -- the job deletes itself unconditionally on success", jobRows, err)
	}
	health, err := store.EligibilityProjectionHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.Queued != 0 || health.Failed != 0 || health.Processing != 0 || !health.OldestPending.IsZero() ||
		health.ProofPending != 0 || !health.OldestProofPending.IsZero() {
		t.Fatalf("EligibilityProjectionHealth must show nothing stuck for a pending-reconciliation account: %+v", health)
	}
}

// observeSimpleWalletCashLot creates one verified WALLET_CASH funding lot
// for this file's (B) usage-overage scenarios via the real ObserveFundingLot
// entrypoint, mirroring the shape consumption_integration_test.go's own
// post-bootstrap wallet payment fixtures use (paidMinor=units*1000, so a
// fully-consumed lot's rounding is exact with no fractional remainder to
// account for).
func observeSimpleWalletCashLot(t *testing.T, store *Store, ctx context.Context, chain *v3TestChain, sourceID, accountID, userID, manifestHash, configHash, unitCode, orderSuffix string,
	units int64, completedAt time.Time, sourceSequence int64) string {
	t.Helper()
	paidMinor := units * 1000
	event := SourceBatchEvent{EventID: autoReconcileUUID('8', 810+int(sourceSequence)),
		EntityType: "payment_order", Operation: "upsert", PayloadHash: testHash("auto-reconcile-payment-" + orderSuffix),
		PayloadCiphertext: bytes.Repeat([]byte{byte(3 + sourceSequence)}, 32), ObservedAt: completedAt}
	cycle := chain.commit(t, store, ctx, sourceID, "payments", autoReconcileUUID('9', 810+int(sourceSequence)), completedAt, []SourceBatchEvent{event})
	result, err := store.ObserveFundingLot(ctx, SourceObservation{
		Lot: domain.FundingLot{PrincipalID: userID, SourceInstanceID: sourceID, SourceType: domain.SourceSub2API,
			ExternalOrderID: "auto-reconcile-payment-" + orderSuffix, Currency: domain.CurrencyCNY, OriginalMinor: paidMinor,
			CurrentCapMinor: paidMinor, Verification: domain.VerificationVerified, SourceStatus: "COMPLETED",
			SourceRevision: event.PayloadHash, CompletedAt: completedAt, ObservedAt: completedAt},
		ExternalUserID: "800", EventKind: "payment", ExternalEventID: event.EventID,
		SchemaVersion: "source-agent-v3.0", SourceUpdatedAt: completedAt, SourceSequence: sourceSequence,
		EligibilityKind: domain.EligibilityWalletCash, CashServiceUnits: fmt.Sprint(units), WalletUnitCode: unitCode,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash,
		SourceCursor: "auto-reconcile-payment:" + orderSuffix, BatchID: cycle.batchID,
		ScanCycleID: cycle.cycleID, StreamWatermarkAt: completedAt,
	}, AuditActor{Type: "source_connector", ID: sourceID})
	if err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "payments", cycle)
	return result.Lot.ID
}

// insertWalletCashLotDirect inserts a fresh, fully-verified, unconsumed
// WALLET_CASH funding lot directly (bypassing ObserveFundingLot/
// applyFundingObservationEligibilityTx's own late-finalized-event gating --
// same bypass rationale as this file's other direct-insert helpers), for
// scenarios that call reprojectEligibilityTx directly rather than through
// ProcessEligibilityProjectionJobs (whose finalized_through watermark makes
// any later-observed fact dated at or before it "late" and freeze the
// account, an orthogonal, unrelated system behavior this file's own
// overage-clearing scenario must not trip over).
func insertWalletCashLotDirect(t *testing.T, store *Store, ctx context.Context, sourceID, accountID, userID, orderSuffix string,
	units int64, completedAt time.Time) string {
	t.Helper()
	paidMinor := units * 1000
	lotID := randomUUID()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO funding_lots(
			id,invoice_user_id,external_account_id,source_instance_id,external_order_id,currency,
			original_minor,current_cap_minor,reserved_minor,issued_minor,verification_state,source_status,
			source_revision_hash,completed_at,observed_at,eligibility_kind,eligibility_cutover_at,
			verified_cash_minor,consumed_cash_minor,refund_frozen,eligibility_revision)
		VALUES($1,$2,$3,$4,$5,'CNY',$6,$6,0,0,'verified','COMPLETED',$7,$8,$8,'WALLET_CASH',$8,$6,0,FALSE,1)`,
		lotID, userID, accountID, sourceID, "auto-reconcile-direct-payment-"+orderSuffix, paidMinor,
		testHash("auto-reconcile-direct-payment-"+orderSuffix), completedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO funding_lot_consumption_state(
			funding_lot_id,cash_service_units,consumed_service_units,cumulative_cash_numerator,
			rounded_consumed_cash_minor,rounding_remainder_numerator)
		VALUES($1,$2::numeric,0,0,0,0)`, lotID, units); err != nil {
		t.Fatal(err)
	}
	return lotID
}

func readUsageOverage(t *testing.T, store *Store, ctx context.Context, accountID string) (status, units, usageEventID string) {
	t.Helper()
	if err := store.pool.QueryRow(ctx, `
		SELECT eligibility_status,COALESCE(non_invoiceable_overage_units::text,''),
			COALESCE(non_invoiceable_overage_usage_event_id::text,'')
		FROM source_account_eligibility_state WHERE external_account_id=$1`, accountID).Scan(
		&status, &units, &usageEventID); err != nil {
		t.Fatal(err)
	}
	return status, units, usageEventID
}

// TestUsageOverageRecordedWithoutFreezingAccount covers design section 3(B):
// usage exceeding every available non-cash/cash pool no longer freezes the
// account -- reprojectEligibilityTx now records the shortfall on the account
// row instead. The paid lot's own consumed_cash_minor stays capped at its
// verified_cash_minor (unchanged behavior: the overage was never allocated
// into any cash pool in the first place, freeze or no freeze).
func TestUsageOverageRecordedWithoutFreezingAccount(t *testing.T) {
	store, ctx, sourceID, accountID, userID, manifestHash, configHash, anchorAt, chain := newAutoReconcileFixture(t)

	paymentAt := anchorAt.Add(5 * time.Minute)
	lotID := observeSimpleWalletCashLot(t, store, ctx, chain, sourceID, accountID, userID, manifestHash, configHash,
		"SUB2_BALANCE_1E8", "lot1", 100, paymentAt, 1)

	usageAt := anchorAt.Add(20 * time.Minute)
	usageID := observeSimpleUsageEvent(t, store, ctx, chain, sourceID, accountID, manifestHash, configHash,
		"auto-reconcile-overage-usage", usageAt, "150", 1) // 100 units of cash available, 150 used: 50 unallocated

	// An empty published "balances" cycle covering the usage fact's own
	// visibility, with no real checkpoint of its own -- same pattern as
	// TestPolicyAnchorAccountCarryForwardProofEvaluatesWithoutSourceGap:
	// ensureBalanceCarryForwardProofTx needs the balances stream to have
	// published through this window before it will derive a carry-forward
	// proof (or otherwise let reprojection/evaluation proceed) instead of
	// backing off with errBalanceCarryForwardProofPending.
	emptyBalancesAt := anchorAt.Add(25 * time.Minute)
	emptyBalancesCycle := chain.commit(t, store, ctx, sourceID, "balances", autoReconcileUUID('9', 850), emptyBalancesAt, nil)
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", emptyBalancesCycle)

	worker := AuditActor{Type: "system", ID: "test-worker"}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now())`, accountID, emptyBalancesAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	processed, err := store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute), worker)
	if err != nil || processed != 1 {
		t.Fatalf("projection processed=%d err=%v", processed, err)
	}

	status, units, overageUsageID := readUsageOverage(t, store, ctx, accountID)
	if status != "active" || units != "50" || overageUsageID != usageID {
		t.Fatalf("overage columns status=%q units=%s usageEventID=%s, want active/50/%s", status, units, overageUsageID, usageID)
	}
	if count := openFreezeCount(t, store, ctx, accountID); count != 0 {
		t.Fatalf("usage overage must not open a manual freeze: open freezes=%d", count)
	}
	lot, err := store.GetFundingLot(ctx, lotID)
	if err != nil || lot.ConsumedCashMinor != 100_000 || lot.VerifiedCashMinor != 100_000 {
		t.Fatalf("lot=%+v err=%v, want consumed/verified capped at the full 100_000 paid (no more)", lot, err)
	}
	var recordedAudits int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.usage.overage_recorded' AND object_id=$1`,
		accountID).Scan(&recordedAudits); err != nil || recordedAudits != 1 {
		t.Fatalf("overage_recorded audit count=%d err=%v, want 1", recordedAudits, err)
	}
}

// TestUsageOverageClearsOnceLedgerCatchesUp covers design section 3(B)'s own
// "otherwise clear" half: once a second payment lot gives the same,
// already-recorded usage fact enough cash to be fully allocated, the next
// reprojection clears both overage columns back to NULL and writes the
// clearing audit event -- source facts are immutable, but the derived
// allocation/overage state is rebuilt from scratch every reprojection.
// reprojectEligibilityTx is called directly (like enterAutoReconcilePending
// and this package's own balance_blip_integration_test.go scenario tests),
// not through ProcessEligibilityProjectionJobs: that entrypoint advances the
// account's finalized_through watermark, and a *second* payment lot dated
// before an already-advanced watermark is a distinct, unrelated system
// behavior (the manual LATE_FINALIZED_EVENT freeze, applyFundingObservationEligibilityTx) --
// this test isolates recordUsageOverageTx's own clear/set behavior across
// two reprojection calls from that orthogonal concern.
func TestUsageOverageClearsOnceLedgerCatchesUp(t *testing.T) {
	store, ctx, sourceID, accountID, userID, manifestHash, configHash, anchorAt, _ := newAutoReconcileFixture(t)
	actor := AuditActor{Type: "system", ID: "test-worker"}

	payment1At := anchorAt.Add(5 * time.Minute)
	lot1ID := insertWalletCashLotDirect(t, store, ctx, sourceID, accountID, userID, "lot1", 100, payment1At)
	usageAt := anchorAt.Add(20 * time.Minute)
	usageID := insertUsageEventDirect(t, store, ctx, sourceID, accountID, "auto-reconcile-overage-usage",
		usageAt, "150", 1, manifestHash, configHash) // 100 units of cash available, 150 used: 50 unallocated

	through := usageAt.Add(time.Minute)
	runReproject := func() {
		t.Helper()
		tx, err := store.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if err = reprojectEligibilityTx(ctx, tx, accountID, through, actor); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	runReproject()
	if status, units, overageUsageID := readUsageOverage(t, store, ctx, accountID); status != "active" || units != "50" || overageUsageID != usageID {
		t.Fatalf("overage not recorded before catch-up: status=%q units=%s usageEventID=%s", status, units, overageUsageID)
	}

	// A second lot, completed before the usage fact's own event_time, gives
	// exactly the missing 50 units -- reprojectEligibilityTx rebuilds
	// allocations from scratch every call, so the same immutable usage event
	// now draws against both lots on the next reprojection.
	payment2At := anchorAt.Add(10 * time.Minute)
	lot2ID := insertWalletCashLotDirect(t, store, ctx, sourceID, accountID, userID, "lot2", 50, payment2At)
	runReproject()

	status, units, overageUsageID := readUsageOverage(t, store, ctx, accountID)
	if status != "active" || units != "" || overageUsageID != "" {
		t.Fatalf("overage columns status=%q units=%q usageEventID=%q, want active/''/'' once the ledger catches up", status, units, overageUsageID)
	}
	lot1, err := store.GetFundingLot(ctx, lot1ID)
	if err != nil || lot1.ConsumedCashMinor != 100_000 {
		t.Fatalf("lot1=%+v err=%v, want fully consumed at 100_000", lot1, err)
	}
	lot2, err := store.GetFundingLot(ctx, lot2ID)
	if err != nil || lot2.ConsumedCashMinor != 50_000 {
		t.Fatalf("lot2=%+v err=%v, want fully consumed at 50_000", lot2, err)
	}
	var clearedAudits int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.usage.overage_cleared' AND object_id=$1`,
		accountID).Scan(&clearedAudits); err != nil || clearedAudits != 1 {
		t.Fatalf("overage_cleared audit count=%d err=%v, want 1", clearedAudits, err)
	}
	if count := openFreezeCount(t, store, ctx, accountID); count != 0 {
		t.Fatalf("open freeze count=%d, want 0 throughout", count)
	}
}
