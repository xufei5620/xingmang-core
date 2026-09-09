package postgresstore

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"
)

// blipUUID generates a deterministic UUID-shaped literal for this file's
// fixtures ('prefix' groups by role, matching this package's existing test
// convention: '1'=source instance, '2'=user, '3'=account, '8'/'9'=event/cycle
// ids), so each scenario's checkpoints/usage events don't need hand-picked
// hex strings. Every test in this file gets its own fresh schema
// (integrationStore's DROP SCHEMA CASCADE), so reusing the same n across
// different test functions in this file is safe.
func blipUUID(prefix rune, n int) string {
	return fmt.Sprintf("%c0000000-0000-4000-8000-%012d", prefix, n)
}

// newBalanceBlipFixture bootstraps a POLICY_ANCHOR account (design
// XM-INV-POLICY-ANCHOR 2.1) with a 1000-unit opening balance via a real
// ObserveBalanceCheckpoint call, then runs one projection job so the
// anchor's own opening balance self-heals into a real
// positive_classified_non_cash evaluation (design XM-INV-ANCHOR-BALANCE).
// This establishes real prior balance-evidence history for the account
// before any scenario in this file introduces its own blip -- matching
// production account 40bd883d's own shape (already re-anchored to
// POLICY_ANCHOR, with hundreds of prior real evaluations, by the time the
// 2026-09-02 blip incident hit it). A fresh single-checkpoint bootstrap,
// by contrast, is design XM-INV-ANCHOR-BALANCE's own already-tested case
// and must keep self-healing immediately -- this fixture is deliberately
// not that case.
func newBalanceBlipFixture(t *testing.T) (store *Store, ctx context.Context, sourceID, accountID, manifestHash, configHash string, anchorAt time.Time, chain *v3TestChain) {
	t.Helper()
	fixtureNow := time.Now().UTC().Truncate(time.Microsecond)
	policyStart := fixtureNow.Add(-2 * time.Hour)
	store, ctx = integrationStoreWithPolicyStart(t, policyStart)
	sourceID = blipUUID('1', 900)
	userID := blipUUID('2', 900)
	accountID = blipUUID('3', 900)
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','balance-blip-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','balance-blip-user')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,binding_method,binding_status)
		VALUES($1,$2,$3,'900','test','verified')`, accountID, userID, sourceID); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []string{"balances", "usage", "credits"} {
		if err := store.ProvisionSourceStream(ctx, sourceID, stream, AuditActor{Type: "system", ID: "test"}); err != nil {
			t.Fatal(err)
		}
	}

	cutoverAt := policyStart.Add(-10 * time.Hour)
	chain = newV3TestChain()
	manifestEvent := SourceBatchEvent{EventID: blipUUID('8', 901),
		EntityType: "cutover_manifest", Operation: "upsert", PayloadHash: testHash("blip-manifest-event"),
		PayloadCiphertext: bytes.Repeat([]byte{1}, 32), ObservedAt: cutoverAt}
	manifestCycle := chain.commit(t, store, ctx, sourceID, "balances", blipUUID('9', 901), cutoverAt, []SourceBatchEvent{manifestEvent})
	manifestHash = testHash("blip-manifest")
	configHash = testHash("blip-config")
	snapshotHash := testHash("blip-snapshot")
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
	anchorEvent := SourceBatchEvent{EventID: blipUUID('8', 902),
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("blip-anchor-event"),
		PayloadCiphertext: bytes.Repeat([]byte{2}, 32), ObservedAt: anchorAt}
	anchorCycle := chain.commit(t, store, ctx, sourceID, "balances", blipUUID('9', 902), anchorAt, []SourceBatchEvent{anchorEvent})
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "900", ExternalEventID: anchorEvent.EventID,
		CheckpointID: "blip-anchor", CheckpointKind: "reconciliation", BaselineMember: true,
		BalanceServiceUnits: "1000", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(anchorCycle.cycleID),
		SnapshotRowCount: "1", AsOf: anchorAt, ObservedAt: anchorAt, StreamWatermarkAt: anchorAt,
		SourceCursor: "balance:900:anchor", SourceRevision: anchorEvent.PayloadHash,
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
		t.Fatalf("fixture: anchor self-heal processed=%d err=%v", processed, err)
	}
	// XM-INV-ELIG-POLICY-START-ANCHOR (design XM-INV-ELIG-SIMPLIFY section
	// 3(D)): cutover_at is now the global policy start, not this checkpoint's
	// own as_of, and a second, derived reconciliation checkpoint exists at
	// as_of=policyStart (this fixture has no window facts between policyStart
	// and anchorAt, so its derived balance equals the same 1000 unchanged).
	// That derived checkpoint -- not the real "blip-anchor" row -- is now the
	// account's first-ever balance evidence (earliest as_of) and carries the
	// positive_classified_non_cash self-heal; the real "blip-anchor"
	// checkpoint reconciles against that credit and is matched instead. This
	// fixture's own goal (establish one real, trusted piece of prior
	// evaluation history before any test-specific blip) is unaffected -- it
	// is simply now split across two rows instead of one.
	var anchorStatus string
	if err := store.pool.QueryRow(ctx, `
		SELECT evaluation_status FROM balance_checkpoint_evaluations
		WHERE checkpoint_id=(SELECT id FROM balance_reconciliation_checkpoints
			WHERE external_account_id=$1 AND checkpoint_id='blip-anchor')`,
		accountID).Scan(&anchorStatus); err != nil || anchorStatus != "matched" {
		t.Fatalf("fixture: real anchor checkpoint evaluation status=%q err=%v, want matched", anchorStatus, err)
	}
	var derivedStatus string
	if err := store.pool.QueryRow(ctx, `
		SELECT evaluation_status FROM balance_checkpoint_evaluations
		WHERE checkpoint_id=(SELECT id FROM balance_reconciliation_checkpoints
			WHERE external_account_id=$1 AND checkpoint_id='policy-start:blip-anchor')`,
		accountID).Scan(&derivedStatus); err != nil || derivedStatus != "positive_classified_non_cash" {
		t.Fatalf("fixture: derived checkpoint evaluation status=%q err=%v, want positive_classified_non_cash", derivedStatus, err)
	}
	return store, ctx, sourceID, accountID, manifestHash, configHash, anchorAt, chain
}

// insertReconciliationCheckpoint inserts a pending balance_reconciliation_checkpoints
// row directly (bypassing ObserveBalanceCheckpoint's scan-cycle plumbing,
// exactly like the pre-existing
// TestUnknownPositiveCheckpointIsConservativelyPlacedBeforeIntervalUsage and
// TestLegacyAccountBalanceCheckpointWithNoTrustAnchorStillFreezesSourceGap
// fixtures do) so a scenario can control exact as_of/balance/source_sequence
// values without needing a full scan cycle per checkpoint.
func insertReconciliationCheckpoint(t *testing.T, store *Store, ctx context.Context, sourceID, accountID, checkpointID string,
	asOf time.Time, balance string, sourceSequence int64, manifestHash, configHash string) string {
	t.Helper()
	id := randomUUID()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO balance_reconciliation_checkpoints(
			id,source_instance_id,external_account_id,external_event_id,checkpoint_id,
			checkpoint_kind,baseline_member,as_of,balance_service_units,balance_negative,unit_code,
			cutover_manifest_hash,configuration_hash,reconciliation_status,source_sequence,
			source_cursor,stream_watermark_at,source_revision_hash,observed_at)
		VALUES($1,$2,$3,$4,$5,'reconciliation',FALSE,$6,$7::numeric,FALSE,'SUB2_BALANCE_1E8',
			$8,$9,'pending_finalization',$10,$11,$6,$12,$6)`,
		id, sourceID, accountID, checkpointID+"-event", checkpointID, asOf, balance,
		manifestHash, configHash, sourceSequence, "cursor:"+checkpointID, testHash(checkpointID)); err != nil {
		t.Fatal(err)
	}
	return id
}

// insertUsageEventDirect inserts a source_usage_events row directly (same
// bypass rationale as insertReconciliationCheckpoint above).
func insertUsageEventDirect(t *testing.T, store *Store, ctx context.Context, sourceID, accountID, usageID string,
	eventTime time.Time, units string, sourceSequence int64, manifestHash, configHash string) string {
	t.Helper()
	id := randomUUID()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_usage_events(
			id,source_instance_id,external_account_id,external_event_id,external_usage_id,
			event_time,service_units,unit_code,cutover_manifest_hash,configuration_hash,
			billing_scope,invoice_eligible,source_sequence,source_cursor,stream_watermark_at,
			source_revision_hash,observed_at)
		VALUES($1,$2,$3,$4,$4,$5,$6::numeric,'SUB2_BALANCE_1E8',$7,$8,'wallet',TRUE,$9,$10,$5,$11,$5)`,
		id, sourceID, accountID, usageID, eventTime, units, manifestHash, configHash,
		sourceSequence, "cursor:"+usageID, testHash(usageID)); err != nil {
		t.Fatal(err)
	}
	return id
}

func countUnknownPositiveCredits(t *testing.T, store *Store, ctx context.Context, accountID string) int {
	t.Helper()
	var count int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM source_credit_events
		WHERE external_account_id=$1 AND credit_kind='UNKNOWN_POSITIVE'`, accountID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func openFreezeCount(t *testing.T, store *Store, ctx context.Context, accountID string) int {
	t.Helper()
	var count int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes
		WHERE external_account_id=$1 AND status='open'`, accountID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestBalanceBlipBoundaryUsageMatchesWithoutSynthesizingCredit covers the
// XM-INV-BALANCE-BLIP boundary rule: production account 40bd883d had a
// checkpoint whose as_of exactly equalled a usage event's event_time (the
// upstream balance snapshot was captured before that instant's own debit was
// applied, while buildEligibilityProjectionTx's inclusive event_time<=through
// window already subtracts it) -- evaluatePendingBalanceEvidenceTx must
// reconcile this as `matched` against the projection that excludes that
// boundary usage, not synthesize an UNKNOWN_POSITIVE credit for the
// difference.
func TestBalanceBlipBoundaryUsageMatchesWithoutSynthesizingCredit(t *testing.T) {
	store, ctx, sourceID, accountID, manifestHash, configHash, anchorAt, _ := newBalanceBlipFixture(t)

	boundaryAt := anchorAt.Add(10 * time.Minute)
	// Usage of 200 units lands at exactly the checkpoint's as_of -- the
	// upstream snapshot (balance=1000, unchanged) was taken before this
	// debit was applied on the source side.
	insertUsageEventDirect(t, store, ctx, sourceID, accountID, "blip-boundary-usage",
		boundaryAt, "200", 1, manifestHash, configHash)
	checkpointID := insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "blip-boundary-checkpoint",
		boundaryAt, "1000", 2, manifestHash, configHash)

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = evaluatePendingBalanceEvidenceTx(ctx, tx, accountID, boundaryAt.Add(time.Minute),
		AuditActor{Type: "system", ID: "test-worker"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var status, expected, difference string
	if err := store.pool.QueryRow(ctx, `
		SELECT evaluation_status,expected_service_units::text,difference_service_units::text
		FROM balance_checkpoint_evaluations WHERE checkpoint_id=$1`, checkpointID).Scan(
		&status, &expected, &difference); err != nil {
		t.Fatal(err)
	}
	if status != "matched" || expected != "1000" || difference != "0" {
		t.Fatalf("boundary checkpoint status=%q expected=%s difference=%s, want matched/1000/0", status, expected, difference)
	}
	// Only the fixture's own anchor credit exists -- the boundary checkpoint
	// must not have synthesized a second one.
	if count := countUnknownPositiveCredits(t, store, ctx, accountID); count != 1 {
		t.Fatalf("UNKNOWN_POSITIVE credit count=%d, want 1 (the anchor's only)", count)
	}
	if count := openFreezeCount(t, store, ctx, accountID); count != 0 {
		t.Fatalf("open freeze count=%d, want 0", count)
	}
}

// TestBalanceBlipTransientPositiveIsIgnoredNotSynthesized covers the defer
// half of the XM-INV-BALANCE-BLIP fix: a mid-stream checkpoint's positive
// difference (on an account with real prior balance-evidence history, not a
// fresh bootstrap) must not synthesize a credit on its own -- if the very
// next real checkpoint reconciles cleanly against the *unchanged* ledger
// (proving the first checkpoint's excess was a transient blip, not a real
// gap), the first is recorded as positive_blip_ignored and no credit is ever
// created.
func TestBalanceBlipTransientPositiveIsIgnoredNotSynthesized(t *testing.T) {
	store, ctx, sourceID, accountID, manifestHash, configHash, anchorAt, _ := newBalanceBlipFixture(t)

	blipAt := anchorAt.Add(10 * time.Minute)
	blipID := insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "blip-transient-blip",
		blipAt, "1200", 2, manifestHash, configHash) // 200 more than the ledger expects
	nextAt := anchorAt.Add(20 * time.Minute)
	nextID := insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "blip-transient-next",
		nextAt, "1000", 3, manifestHash, configHash) // ledger unchanged: the blip did not recur

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = evaluatePendingBalanceEvidenceTx(ctx, tx, accountID, nextAt.Add(time.Minute),
		AuditActor{Type: "system", ID: "test-worker"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var blipStatus, blipDifference string
	if err := store.pool.QueryRow(ctx, `
		SELECT evaluation_status,difference_service_units::text FROM balance_checkpoint_evaluations
		WHERE checkpoint_id=$1`, blipID).Scan(&blipStatus, &blipDifference); err != nil {
		t.Fatal(err)
	}
	if blipStatus != "positive_blip_ignored" || blipDifference != "200" {
		t.Fatalf("blip checkpoint status=%q difference=%s, want positive_blip_ignored/200", blipStatus, blipDifference)
	}
	var nextStatus, nextDifference string
	if err := store.pool.QueryRow(ctx, `
		SELECT evaluation_status,difference_service_units::text FROM balance_checkpoint_evaluations
		WHERE checkpoint_id=$1`, nextID).Scan(&nextStatus, &nextDifference); err != nil {
		t.Fatal(err)
	}
	if nextStatus != "matched" || nextDifference != "0" {
		t.Fatalf("next checkpoint status=%q difference=%s, want matched/0", nextStatus, nextDifference)
	}
	if count := countUnknownPositiveCredits(t, store, ctx, accountID); count != 1 {
		t.Fatalf("UNKNOWN_POSITIVE credit count=%d, want 1 (the anchor's only -- no credit for the transient blip)", count)
	}
	if count := openFreezeCount(t, store, ctx, accountID); count != 0 {
		t.Fatalf("open freeze count=%d, want 0", count)
	}
}

// TestBalanceBlipPersistentPositiveConfirmedByNextCheckpointSynthesizesCredit
// covers the confirm half: when the *next* real checkpoint shows the exact
// same positive difference (a genuine, persistent gap, not a one-off blip),
// the deferred checkpoint synthesizes its UNKNOWN_POSITIVE credit exactly as
// design XM-INV-ANCHOR-BALANCE's self-healing mechanism already does, and
// the confirming checkpoint reconciles matched against it. Phase 1 evaluates
// the first checkpoint alone and requires it to stay pending (no evaluation
// row, no credit) -- the concrete behavior that distinguishes "defer until
// confirmed" from the old "synthesize on a single checkpoint" rule, which
// would have written a positive_classified_non_cash row immediately here.
// Phase 2 then adds the confirming checkpoint and requires both to resolve.
func TestBalanceBlipPersistentPositiveConfirmedByNextCheckpointSynthesizesCredit(t *testing.T) {
	store, ctx, sourceID, accountID, manifestHash, configHash, anchorAt, _ := newBalanceBlipFixture(t)

	blipAt := anchorAt.Add(10 * time.Minute)
	blipID := insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "blip-persist-first",
		blipAt, "1200", 2, manifestHash, configHash) // 200 more than the ledger expects

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

	// Phase 1: only the blip checkpoint is pending. It must stay pending
	// (deferred, no evaluation row written, no credit synthesized) rather
	// than self-heal on its own.
	runEvaluate(blipAt.Add(time.Minute))
	var pendingRows int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM balance_checkpoint_evaluations
		WHERE checkpoint_id=$1`, blipID).Scan(&pendingRows); err != nil {
		t.Fatal(err)
	}
	if pendingRows != 0 {
		t.Fatalf("blip checkpoint evaluation rows=%d after phase 1, want 0 (must stay deferred, unconfirmed)", pendingRows)
	}
	if count := countUnknownPositiveCredits(t, store, ctx, accountID); count != 1 {
		t.Fatalf("UNKNOWN_POSITIVE credit count=%d after phase 1, want 1 (the anchor's only -- none yet for the unconfirmed blip)", count)
	}

	// Phase 2: the confirming checkpoint arrives with the exact same +200
	// difference, proving the gap is real and persistent.
	confirmAt := anchorAt.Add(20 * time.Minute)
	confirmID := insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "blip-persist-confirm",
		confirmAt, "1200", 3, manifestHash, configHash) // same +200, ledger otherwise unchanged: a real gap
	runEvaluate(confirmAt.Add(time.Minute))

	var blipStatus, blipExpected, blipDifference string
	if err := store.pool.QueryRow(ctx, `
		SELECT evaluation_status,expected_service_units::text,difference_service_units::text
		FROM balance_checkpoint_evaluations WHERE checkpoint_id=$1`, blipID).Scan(
		&blipStatus, &blipExpected, &blipDifference); err != nil {
		t.Fatal(err)
	}
	if blipStatus != "positive_classified_non_cash" || blipExpected != "1000" || blipDifference != "200" {
		t.Fatalf("first checkpoint status=%q expected=%s difference=%s, want positive_classified_non_cash/1000/200",
			blipStatus, blipExpected, blipDifference)
	}
	var confirmStatus, confirmDifference string
	if err := store.pool.QueryRow(ctx, `
		SELECT evaluation_status,difference_service_units::text FROM balance_checkpoint_evaluations
		WHERE checkpoint_id=$1`, confirmID).Scan(&confirmStatus, &confirmDifference); err != nil {
		t.Fatal(err)
	}
	if confirmStatus != "matched" || confirmDifference != "0" {
		t.Fatalf("confirming checkpoint status=%q difference=%s, want matched/0", confirmStatus, confirmDifference)
	}
	// Two real UNKNOWN_POSITIVE credits now: the fixture's own anchor, plus
	// this confirmed 200-unit gap.
	if count := countUnknownPositiveCredits(t, store, ctx, accountID); count != 2 {
		t.Fatalf("UNKNOWN_POSITIVE credit count=%d, want 2 (anchor + confirmed gap)", count)
	}
	var newCreditUnits string
	if err := store.pool.QueryRow(ctx, `SELECT service_units::text FROM source_credit_events
		WHERE external_account_id=$1 AND credit_kind='UNKNOWN_POSITIVE' AND service_units='200'`,
		accountID).Scan(&newCreditUnits); err != nil {
		t.Fatalf("confirmed 200-unit credit missing: %v", err)
	}
	if count := openFreezeCount(t, store, ctx, accountID); count != 0 {
		t.Fatalf("open freeze count=%d, want 0", count)
	}
}

// TestBalanceBlipProductionCascadeDoesNotFreezeAccount reproduces the
// 2026-09-02 production incident's shape end to end through the real
// ObserveBalanceCheckpoint/ObserveUsageEvent/ProcessEligibilityProjectionJobs
// entrypoints: a checkpoint's as_of coincides with a usage event's
// event_time (the boundary hazard), and by the time a single batch
// projection job evaluates every pending checkpoint together, the usage
// event has already been ingested -- exactly the account 40bd883d shape
// (usage ingested at 22:49:20Z, batch evaluation at 23:06:09Z). Before this
// fix, this cascaded into a synthesized credit at the origin checkpoint
// followed by every later checkpoint freezing UNKNOWN_NEGATIVE_BALANCE by
// the same constant amount. After the fix, the origin checkpoint reconciles
// via the boundary rule and nothing downstream ever goes wrong.
func TestBalanceBlipProductionCascadeDoesNotFreezeAccount(t *testing.T) {
	store, ctx, sourceID, accountID, manifestHash, configHash, anchorAt, chain := newBalanceBlipFixture(t)

	// Checkpoints land roughly a minute apart, same as production, but the
	// usage fact for the boundary instant is not ingested until afterward
	// (source agents restarted mid roll-forward, in production) -- so all
	// three checkpoints below are inserted, and only then does the usage
	// event arrive, and only then does one batch projection job evaluate
	// everything pending together.
	boundaryAt := anchorAt.Add(10 * time.Minute)
	checkpoint1Event := SourceBatchEvent{EventID: blipUUID('8', 910),
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("blip-cascade-checkpoint1-event"),
		PayloadCiphertext: bytes.Repeat([]byte{3}, 32), ObservedAt: boundaryAt}
	checkpoint1Cycle := chain.commit(t, store, ctx, sourceID, "balances", blipUUID('9', 910), boundaryAt, []SourceBatchEvent{checkpoint1Event})
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "900", ExternalEventID: checkpoint1Event.EventID,
		CheckpointID: "blip-cascade-checkpoint1", CheckpointKind: "reconciliation", BaselineMember: false,
		// Upstream balance snapshot taken before the boundary usage's own
		// debit was applied -- still the pre-debit 1000.
		BalanceServiceUnits: "1000", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(checkpoint1Cycle.cycleID),
		SnapshotRowCount: "1", AsOf: boundaryAt, ObservedAt: boundaryAt, StreamWatermarkAt: boundaryAt,
		SourceCursor: "balance:900:1", SourceRevision: checkpoint1Event.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 2,
		BatchID: checkpoint1Cycle.batchID, ScanCycleID: checkpoint1Cycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", checkpoint1Cycle)

	checkpoint2At := boundaryAt.Add(time.Minute)
	checkpoint2Event := SourceBatchEvent{EventID: blipUUID('8', 911),
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("blip-cascade-checkpoint2-event"),
		PayloadCiphertext: bytes.Repeat([]byte{4}, 32), ObservedAt: checkpoint2At}
	checkpoint2Cycle := chain.commit(t, store, ctx, sourceID, "balances", blipUUID('9', 911), checkpoint2At, []SourceBatchEvent{checkpoint2Event})
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "900", ExternalEventID: checkpoint2Event.EventID,
		CheckpointID: "blip-cascade-checkpoint2", CheckpointKind: "reconciliation", BaselineMember: false,
		// By now the source correctly reflects the 200-unit debit.
		BalanceServiceUnits: "800", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(checkpoint2Cycle.cycleID),
		SnapshotRowCount: "1", AsOf: checkpoint2At, ObservedAt: checkpoint2At, StreamWatermarkAt: checkpoint2At,
		SourceCursor: "balance:900:2", SourceRevision: checkpoint2Event.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 3,
		BatchID: checkpoint2Cycle.batchID, ScanCycleID: checkpoint2Cycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", checkpoint2Cycle)

	checkpoint3At := checkpoint2At.Add(time.Minute)
	checkpoint3Event := SourceBatchEvent{EventID: blipUUID('8', 912),
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("blip-cascade-checkpoint3-event"),
		PayloadCiphertext: bytes.Repeat([]byte{5}, 32), ObservedAt: checkpoint3At}
	checkpoint3Cycle := chain.commit(t, store, ctx, sourceID, "balances", blipUUID('9', 912), checkpoint3At, []SourceBatchEvent{checkpoint3Event})
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "900", ExternalEventID: checkpoint3Event.EventID,
		CheckpointID: "blip-cascade-checkpoint3", CheckpointKind: "reconciliation", BaselineMember: false,
		BalanceServiceUnits: "800", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(checkpoint3Cycle.cycleID),
		SnapshotRowCount: "1", AsOf: checkpoint3At, ObservedAt: checkpoint3At, StreamWatermarkAt: checkpoint3At,
		SourceCursor: "balance:900:3", SourceRevision: checkpoint3Event.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 4,
		BatchID: checkpoint3Cycle.batchID, ScanCycleID: checkpoint3Cycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", checkpoint3Cycle)

	// The usage fact for the boundary instant is ingested only now -- late,
	// after all three checkpoints already exist, matching the production
	// timeline (source agents recreated mid roll-forward delayed usage
	// ingestion by ~25 minutes). Its own visibility watermark is the
	// boundary instant itself (already covered by checkpoint1's own
	// published balances cycle above), not a later instant -- ensureBalance-
	// CarryForwardProofTx would otherwise wait for a further balances cycle
	// to publish past it before this job could proceed at all.
	usageEvent := SourceBatchEvent{EventID: blipUUID('8', 913),
		EntityType: "usage_event", Operation: "upsert", PayloadHash: testHash("blip-cascade-usage-event"),
		PayloadCiphertext: bytes.Repeat([]byte{6}, 32), ObservedAt: boundaryAt}
	usageCycle := chain.commit(t, store, ctx, sourceID, "usage", blipUUID('9', 913), boundaryAt, []SourceBatchEvent{usageEvent})
	if err := store.ObserveUsageEvent(ctx, UsageObservation{
		SourceInstanceID: sourceID, ExternalUserID: "900", ExternalEventID: usageEvent.EventID,
		ExternalUsageID: "blip-cascade-usage", EventTime: boundaryAt, ObservedAt: boundaryAt,
		StreamWatermarkAt: boundaryAt, ServiceUnits: "200", UnitCode: "SUB2_BALANCE_1E8",
		BillingScope: "wallet", SourceCursor: "usage:900", SourceRevision: usageEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: usageCycle.batchID, ScanCycleID: usageCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "usage", usageCycle)

	// One batch job evaluates all three pending checkpoints together, the
	// usage fact for the boundary instant already present -- exactly the
	// 23:06:09Z production batch.
	worker := AuditActor{Type: "system", ID: "test-worker"}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now())
		ON CONFLICT(external_account_id) DO UPDATE SET
			requested_through=GREATEST(eligibility_projection_jobs.requested_through,EXCLUDED.requested_through),
			status='queued',lease_token=NULL,lease_expires_at=NULL,next_attempt_at=now(),updated_at=now()`,
		accountID, checkpoint3At.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	processed, err := store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute), worker)
	if err != nil || processed != 1 {
		t.Fatalf("cascade batch processed=%d err=%v", processed, err)
	}

	for _, tc := range []struct{ checkpointID, want string }{
		{"blip-cascade-checkpoint1", "matched"},
		{"blip-cascade-checkpoint2", "matched"},
		{"blip-cascade-checkpoint3", "matched"},
	} {
		var status, difference string
		if err := store.pool.QueryRow(ctx, `
			SELECT evaluation_status,difference_service_units::text FROM balance_checkpoint_evaluations
			WHERE checkpoint_id=(SELECT id FROM balance_reconciliation_checkpoints
				WHERE external_account_id=$1 AND checkpoint_id=$2)`,
			accountID, tc.checkpointID).Scan(&status, &difference); err != nil {
			t.Fatalf("%s: %v", tc.checkpointID, err)
		}
		if status != tc.want || difference != "0" {
			t.Fatalf("%s status=%q difference=%s, want %s/0", tc.checkpointID, status, difference, tc.want)
		}
	}
	if count := countUnknownPositiveCredits(t, store, ctx, accountID); count != 1 {
		t.Fatalf("UNKNOWN_POSITIVE credit count=%d, want 1 (the anchor's only -- the boundary checkpoint must not add a second)", count)
	}
	var status string
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_status FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&status); err != nil || status != "active" {
		t.Fatalf("account status=%q err=%v, want active", status, err)
	}
	if count := openFreezeCount(t, store, ctx, accountID); count != 0 {
		t.Fatalf("open freeze count=%d, want 0 -- production's own bug produced 9 here", count)
	}
}
