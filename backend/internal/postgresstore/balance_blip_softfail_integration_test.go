package postgresstore

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// TestBalanceBlipConfirmationThatDoesNotReconcileIsSoftfailedNotErrored
// reproduces the 2026-09-03 production incident (RC75, account 98cce4c8 --
// see docs/handoffs/XM-INV-BLIP-SOFTFAIL.md): a checkpoint's positive
// difference is deferred (XM-INV-BALANCE-BLIP), and the very next checkpoint
// shows the exact same difference -- the pre-check that looks like a genuine
// confirm. But between the deferred item's own trust interval start and the
// confirming item's as_of, usage already exceeded the credit pool available
// at that time (floored at zero, an "account's ledger is missing its anchor
// opening balance" shape) -- so once the tentative credit is actually
// inserted and the projection rebuilt, it only partially recovers that
// floored usage, and the rebuild does not reconcile to exactly zero. Before
// the fix, evaluatePendingBalanceEvidenceTx returned an error here
// ("balance blip confirmation ... did not reconcile"), which propagated out
// of ProcessEligibilityProjectionJobs's per-account handling as
// PROJECTION_FAILED and reproduced identically on every retry -- the account
// could never make forward progress. After the fix, this must never error:
// the deferred item is recorded positive_blip_ignored (not confirmed), the
// tentative credit is removed, and the confirming item itself becomes the
// new pending blip (a rebaseline), with an eligibility.balance_blip.rebaselined
// audit row recording both amounts.
func TestBalanceBlipConfirmationThatDoesNotReconcileIsSoftfailedNotErrored(t *testing.T) {
	store, ctx, sourceID, accountID, manifestHash, configHash, anchorAt, _ := newBalanceBlipFixture(t)

	// Usage of 1200 lands just after the anchor (which only carries 1000) --
	// it exceeds the anchor credit alone, so 200 of it is unfunded/floored at
	// the time it is observed. This is the "ledger is missing its anchor
	// opening balance" shape from the incident: a real structural shortfall,
	// not a timing coincidence the boundary rule would catch (usage lands
	// nowhere near any checkpoint's own as_of).
	usageAt := anchorAt.Add(time.Minute)
	insertUsageEventDirect(t, store, ctx, sourceID, accountID, "softfail-usage-a",
		usageAt, "1200", 1, manifestHash, configHash)

	// cp2: reported balance 300 against an ExpectedBalance of 0 (the 1000
	// anchor credit was entirely consumed by the 1200 usage, floored) --
	// difference +300. Deferred (real prior history exists: the anchor).
	cp2At := anchorAt.Add(10 * time.Minute)
	cp2ID := insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "softfail-cp2",
		cp2At, "300", 2, manifestHash, configHash)

	// cp3: reported balance also 300 against the same unchanged ledger (no
	// new usage/credit since cp2) -- its own pre-check difference is also
	// +300, exactly matching cp2's deferred difference. This is the "looks
	// like a genuine confirm" pre-check.
	cp3At := anchorAt.Add(20 * time.Minute)
	cp3ID := insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "softfail-cp3",
		cp3At, "300", 3, manifestHash, configHash)

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// This must never return an error -- that is the entire point of this
	// slice. Before the fix, this exact scenario returned:
	// "balance blip confirmation for checkpoint/proof softfail-cp3 did not
	// reconcile: still differs by 200".
	if err = evaluatePendingBalanceEvidenceTx(ctx, tx, accountID, cp3At.Add(time.Minute),
		AuditActor{Type: "system", ID: "test-worker"}); err != nil {
		t.Fatalf("evaluatePendingBalanceEvidenceTx returned an error instead of a softfailed outcome: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// cp2 must be recorded ignored, not confirmed -- the tentative credit
	// never actually explained the whole gap.
	var cp2Status, cp2Expected, cp2Difference string
	if err := store.pool.QueryRow(ctx, `
		SELECT evaluation_status,expected_service_units::text,difference_service_units::text
		FROM balance_checkpoint_evaluations WHERE checkpoint_id=$1`, cp2ID).Scan(
		&cp2Status, &cp2Expected, &cp2Difference); err != nil {
		t.Fatal(err)
	}
	if cp2Status != "positive_blip_ignored" || cp2Expected != "0" || cp2Difference != "300" {
		t.Fatalf("cp2 status=%q expected=%s difference=%s, want positive_blip_ignored/0/300", cp2Status, cp2Expected, cp2Difference)
	}

	// cp3 must NOT have an evaluation row yet -- it is the new pending blip
	// (rebaselined), waiting for a future item to confirm or disconfirm it.
	var cp3Rows int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM balance_checkpoint_evaluations
		WHERE checkpoint_id=$1`, cp3ID).Scan(&cp3Rows); err != nil {
		t.Fatal(err)
	}
	if cp3Rows != 0 {
		t.Fatalf("cp3 evaluation rows=%d, want 0 (must be the new pending blip, not yet resolved)", cp3Rows)
	}

	// The tentative credit must have been fully removed -- only the
	// fixture's own anchor credit remains.
	if count := countUnknownPositiveCredits(t, store, ctx, accountID); count != 1 {
		t.Fatalf("UNKNOWN_POSITIVE credit count=%d, want 1 (the anchor's only -- the tentative credit must be rolled back)", count)
	}
	if count := openFreezeCount(t, store, ctx, accountID); count != 0 {
		t.Fatalf("open freeze count=%d, want 0 (still within the rebaseline cap)", count)
	}

	// An audit row records the rebaseline, distinct from an ordinary
	// disconfirmation.
	var rebaselineAudits int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.balance_blip.rebaselined' AND object_type='balance_checkpoint' AND object_id=$1`,
		cp2ID).Scan(&rebaselineAudits); err != nil {
		t.Fatal(err)
	}
	if rebaselineAudits != 1 {
		t.Fatalf("eligibility.balance_blip.rebaselined audit rows for cp2=%d, want 1", rebaselineAudits)
	}
}

// TestBalanceBlipSoftfailRebaselineCapEscalatesToSourceGapFreeze covers the
// XM-INV-BLIP-SOFTFAIL escalation half: an account whose ledger is missing a
// real structural fact reproduces the exact non-reconciling shape from
// TestBalanceBlipConfirmationThatDoesNotReconcileIsSoftfailedNotErrored on
// every subsequent checkpoint (the underlying gap never actually resolves).
// Rebaselining forever would loop without end; after
// balanceBlipRebaselineCap consecutive rebaselines with no clean
// confirm/disconfirm in between, the account gets a single SOURCE_GAP freeze
// on the triggering item instead, so the evaluator always makes forward
// progress.
func TestBalanceBlipSoftfailRebaselineCapEscalatesToSourceGapFreeze(t *testing.T) {
	store, ctx, sourceID, accountID, manifestHash, configHash, anchorAt, _ := newBalanceBlipFixture(t)

	usageAt := anchorAt.Add(time.Minute)
	insertUsageEventDirect(t, store, ctx, sourceID, accountID, "softfail-cap-usage-a",
		usageAt, "1200", 1, manifestHash, configHash)

	// Five checkpoints, each reporting the same 300 against the same
	// unchanged, ledger-with-a-floored-usage-shortfall -- this reproduces
	// the identical rebaseline shape as many times in a row as there are
	// checkpoints. cp2 through cp4 rebaseline (3, at the cap); cp5's own
	// confirmation attempt against cp4 also fails to reconcile, but by then
	// the cap has been reached, so cp5 escalates to SOURCE_GAP instead of
	// becoming yet another pending blip.
	type checkpoint struct {
		id, checkpointID string
		asOf             time.Time
	}
	checkpoints := make([]checkpoint, 0, 4)
	for i, minutes := range []int{10, 20, 30, 40} {
		asOf := anchorAt.Add(time.Duration(minutes) * time.Minute)
		checkpointID := "softfail-cap-cp" + string(rune('2'+i))
		id := insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, checkpointID,
			asOf, "300", int64(2+i), manifestHash, configHash)
		checkpoints = append(checkpoints, checkpoint{id: id, checkpointID: checkpointID, asOf: asOf})
	}

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	through := checkpoints[len(checkpoints)-1].asOf.Add(time.Minute)
	if err = evaluatePendingBalanceEvidenceTx(ctx, tx, accountID, through,
		AuditActor{Type: "system", ID: "test-worker"}); err != nil {
		t.Fatalf("evaluatePendingBalanceEvidenceTx returned an error instead of escalating: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// cp2, cp3, cp4 (the first balanceBlipRebaselineCap=3 items) are each
	// rebaselined: ignored, with a rebaselined audit row.
	for i := 0; i < 3; i++ {
		cp := checkpoints[i]
		var status string
		if err := store.pool.QueryRow(ctx, `SELECT evaluation_status FROM balance_checkpoint_evaluations
			WHERE checkpoint_id=$1`, cp.id).Scan(&status); err != nil {
			t.Fatalf("%s: %v", cp.checkpointID, err)
		}
		if status != "positive_blip_ignored" {
			t.Fatalf("%s status=%q, want positive_blip_ignored", cp.checkpointID, status)
		}
		var audits int
		if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
			WHERE action='eligibility.balance_blip.rebaselined' AND object_type='balance_checkpoint' AND object_id=$1`,
			cp.id).Scan(&audits); err != nil {
			t.Fatal(err)
		}
		if audits != 1 {
			t.Fatalf("%s rebaselined audit rows=%d, want 1", cp.checkpointID, audits)
		}
	}

	// cp5 (the 4th) is the escalation trigger: source_gap_frozen, not
	// deferred and not rebaselined.
	cp5 := checkpoints[3]
	var cp5Status string
	if err := store.pool.QueryRow(ctx, `SELECT evaluation_status FROM balance_checkpoint_evaluations
		WHERE checkpoint_id=$1`, cp5.id).Scan(&cp5Status); err != nil {
		t.Fatal(err)
	}
	if cp5Status != "source_gap_frozen" {
		t.Fatalf("%s status=%q, want source_gap_frozen (the escalation)", cp5.checkpointID, cp5Status)
	}

	// No credit ever survives -- every tentative one was rolled back.
	if count := countUnknownPositiveCredits(t, store, ctx, accountID); count != 1 {
		t.Fatalf("UNKNOWN_POSITIVE credit count=%d, want 1 (the anchor's only)", count)
	}
	// Exactly one open freeze: the escalation's own SOURCE_GAP.
	if count := openFreezeCount(t, store, ctx, accountID); count != 1 {
		t.Fatalf("open freeze count=%d, want 1 (the escalation's SOURCE_GAP)", count)
	}
	var freezeReason string
	if err := store.pool.QueryRow(ctx, `SELECT freeze_reason FROM eligibility_freezes
		WHERE external_account_id=$1 AND status='open'`, accountID).Scan(&freezeReason); err != nil {
		t.Fatal(err)
	}
	if freezeReason != "SOURCE_GAP" {
		t.Fatalf("open freeze reason=%q, want SOURCE_GAP", freezeReason)
	}
}

// TestBalanceBlipSoftfailBatchStillProcessesHealthyAccount covers the
// isolation requirement alongside the softfail fix: a batch that includes
// this account (whose confirmation attempt does not reconcile) plus a
// second, ordinary healthy account must still process the healthy account
// successfully in the same ProcessEligibilityProjectionJobs call -- no
// batch-level failure. With the softfail fix, the blip account itself no
// longer errors either (see the two tests above), so both accounts succeed
// in the one batch call.
func TestBalanceBlipSoftfailBatchStillProcessesHealthyAccount(t *testing.T) {
	store, ctx, sourceID, accountID, manifestHash, configHash, anchorAt, chain := newBalanceBlipFixture(t)

	usageAt := anchorAt.Add(time.Minute)
	insertUsageEventDirect(t, store, ctx, sourceID, accountID, "softfail-batch-usage-a",
		usageAt, "1200", 1, manifestHash, configHash)
	cp2At := anchorAt.Add(10 * time.Minute)
	insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "softfail-batch-cp2",
		cp2At, "300", 2, manifestHash, configHash)
	cp3At := anchorAt.Add(20 * time.Minute)
	insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "softfail-batch-cp3",
		cp3At, "300", 3, manifestHash, configHash)

	worker := AuditActor{Type: "system", ID: "test-worker"}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now())
		ON CONFLICT(external_account_id) DO UPDATE SET
			requested_through=GREATEST(eligibility_projection_jobs.requested_through,EXCLUDED.requested_through),
			status='queued',lease_token=NULL,lease_expires_at=NULL,next_attempt_at=now(),updated_at=now()`,
		accountID, cp3At.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	// A second, ordinary healthy account in the same source, sharing the
	// fixture's manifest/config -- just its own anchor checkpoint, nothing
	// else pending.
	healthyUserID := blipUUID('2', 950)
	healthyAccountID := blipUUID('3', 950)
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','balance-blip-healthy-user')`, healthyUserID); err != nil {
		t.Fatal(err)
	}
	// external_subject_hmac must be set to a value distinct from the
	// fixture's own account row: the column is nullable, but its unique
	// constraint is NULLS NOT DISTINCT, so a second row sharing this
	// source_instance_id with an unset (NULL) hmac would collide with the
	// fixture's own account instead of coexisting as a separate account.
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
		VALUES($1,$2,$3,'950','balance-blip-healthy-hmac','test','verified')`, healthyAccountID, healthyUserID, sourceID); err != nil {
		t.Fatal(err)
	}
	healthyAnchorAt := anchorAt
	healthyAnchorEvent := SourceBatchEvent{EventID: blipUUID('8', 950),
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("blip-healthy-anchor-event"),
		PayloadCiphertext: bytes.Repeat([]byte{7}, 32), ObservedAt: healthyAnchorAt}
	healthyAnchorCycle := chain.commit(t, store, ctx, sourceID, "balances", blipUUID('9', 950), healthyAnchorAt, []SourceBatchEvent{healthyAnchorEvent})
	if err := store.ObserveBalanceCheckpoint(ctx, BalanceCheckpointObservation{
		SourceInstanceID: sourceID, ExternalUserID: "950", ExternalEventID: healthyAnchorEvent.EventID,
		CheckpointID: "blip-healthy-anchor", CheckpointKind: "reconciliation", BaselineMember: true,
		BalanceServiceUnits: "500", UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(healthyAnchorCycle.cycleID),
		SnapshotRowCount: "1", AsOf: healthyAnchorAt, ObservedAt: healthyAnchorAt, StreamWatermarkAt: healthyAnchorAt,
		SourceCursor: "balance:950:anchor", SourceRevision: healthyAnchorEvent.PayloadHash,
		CutoverManifestHash: manifestHash, ConfigurationHash: configHash, SourceSequence: 1,
		BatchID: healthyAnchorCycle.batchID, ScanCycleID: healthyAnchorCycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, store, ctx, sourceID, "balances", healthyAnchorCycle)
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now())`, healthyAccountID, healthyAnchorAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	processed, err := store.ProcessEligibilityProjectionJobs(ctx, 10, time.Now().UTC().Add(time.Minute), worker)
	if err != nil {
		t.Fatalf("batch returned an error: %v", err)
	}
	if processed != 2 {
		t.Fatalf("processed=%d, want 2 (both the softfailed account and the healthy account)", processed)
	}

	var healthyStatus string
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_status FROM source_account_eligibility_state
		WHERE external_account_id=$1`, healthyAccountID).Scan(&healthyStatus); err != nil || healthyStatus != "active" {
		t.Fatalf("healthy account status=%q err=%v, want active", healthyStatus, err)
	}
	if count := openFreezeCount(t, store, ctx, healthyAccountID); count != 0 {
		t.Fatalf("healthy account open freeze count=%d, want 0", count)
	}
	var remainingJobs int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM eligibility_projection_jobs
		WHERE external_account_id IN ($1,$2)`, accountID, healthyAccountID).Scan(&remainingJobs); err != nil {
		t.Fatal(err)
	}
	if remainingJobs != 0 {
		t.Fatalf("remaining eligibility_projection_jobs rows=%d, want 0 (both succeeded)", remainingJobs)
	}
}
