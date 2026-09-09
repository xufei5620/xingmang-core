package postgresstore

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// insertBlockingSynthesizedCredit pre-inserts a zero-unit UNKNOWN_POSITIVE
// credit under the exact external_event_id synthesizeUnknownPositive would
// use for this checkpoint, so its ON CONFLICT DO NOTHING makes the
// confirmation's tentative synthesis a silent no-op. Zero units so the row
// itself changes no arithmetic anywhere: the only thing it changes is
// whether the tentative credit can be written at all -- which is exactly the
// remaining way a blip confirmation can fail to reconcile once the evaluator
// compares against the signed expectation (see the softfail test below).
func insertBlockingSynthesizedCredit(t *testing.T, store *Store, ctx context.Context,
	sourceID, accountID, checkpointID string, at time.Time, sourceSequence int64, manifestHash, configHash string) {
	t.Helper()
	insertCreditEventDirect(t, store, ctx, sourceID, accountID,
		"unknown-positive:"+checkpointID+"-event", at, "0", "UNKNOWN_POSITIVE",
		sourceSequence, manifestHash, configHash)
}

// TestBalanceBlipConfirmationOnAFlooredLedgerReconcilesUnderTheSignedComparison
// is the 2026-09-03 production incident's own shape (RC75, account 98cce4c8 --
// docs/handoffs/XM-INV-BLIP-SOFTFAIL.md), re-founded by XM-INV-PENDING-RECON
// C5.
//
// The shape is unchanged: a checkpoint's positive difference is deferred
// (XM-INV-BALANCE-BLIP), the very next checkpoint shows the exact same
// difference -- the pre-check that looks like a genuine confirm -- and
// between the deferred item's own trust interval start and the confirming
// item's as_of, usage already exceeded the credit pool available at that
// time (the "ledger is missing its anchor opening balance" shape).
//
// What changed is why it used not to reconcile. ExpectedBalance is floored at
// zero, so the 200 units of usage no pool could cover simply disappeared from
// the comparison, and a tentative credit sized against a difference computed
// without them could never explain the whole gap. signedExpectedUnits
// subtracts them: the difference is 500, not 300, and the synthesized credit
// now accounts for the gap exactly. The deferred item is classified
// positive_classified_non_cash and the confirming item matches -- the account
// self-heals instead of rebaselining.
//
// The softfail branch itself is untouched and still covered, by
// TestBalanceBlipConfirmationThatCannotSynthesiseIsSoftfailedNotErrored
// below. And the guarantee this test was originally written for still holds
// and is still asserted first: this must never return an error.
func TestBalanceBlipConfirmationOnAFlooredLedgerReconcilesUnderTheSignedComparison(t *testing.T) {
	store, ctx, sourceID, accountID, manifestHash, configHash, anchorAt, _ := newBalanceBlipFixture(t)

	// Usage of 1200 lands just after the anchor (which only carries 1000) --
	// it exceeds the anchor credit alone, so 200 units stay unallocated. Not
	// a timing coincidence the boundary rule would catch: the usage lands
	// nowhere near any checkpoint's own as_of.
	usageAt := anchorAt.Add(time.Minute)
	insertUsageEventDirect(t, store, ctx, sourceID, accountID, "softfail-usage-a",
		usageAt, "1200", 1, manifestHash, configHash)

	// cp2: reported balance 300 against a signed expectation of -200 (the
	// 1000 anchor credit fully consumed, 200 units still unallocated) --
	// difference +500. Deferred (real prior history exists: the anchor).
	cp2At := anchorAt.Add(10 * time.Minute)
	cp2ID := insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "softfail-cp2",
		cp2At, "300", 2, manifestHash, configHash)

	// cp3: reported balance also 300 against the same unchanged ledger (no
	// new usage/credit since cp2) -- its own difference is also +500,
	// exactly matching cp2's deferred difference: the confirm pre-check.
	cp3At := anchorAt.Add(20 * time.Minute)
	cp3ID := insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "softfail-cp3",
		cp3At, "300", 3, manifestHash, configHash)

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// Before XM-INV-BLIP-SOFTFAIL this returned "balance blip confirmation
	// for checkpoint/proof softfail-cp3 did not reconcile: still differs by
	// 200" on every retry, forever. It must never error.
	if err = evaluatePendingBalanceEvidenceTx(ctx, tx, accountID, cp3At.Add(time.Minute),
		AuditActor{Type: "system", ID: "test-worker"}); err != nil {
		t.Fatalf("evaluatePendingBalanceEvidenceTx returned an error: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// cp2 is the confirmed blip: classified non-cash, with its own original
	// (signed) difference recorded.
	var cp2Status, cp2Expected, cp2Difference string
	if err := store.pool.QueryRow(ctx, `
		SELECT evaluation_status,expected_service_units::text,difference_service_units::text
		FROM balance_checkpoint_evaluations WHERE checkpoint_id=$1`, cp2ID).Scan(
		&cp2Status, &cp2Expected, &cp2Difference); err != nil {
		t.Fatal(err)
	}
	if cp2Status != "positive_classified_non_cash" || cp2Expected != "0" || cp2Difference != "500" {
		t.Fatalf("cp2 status=%q expected=%s difference=%s, want positive_classified_non_cash/0/500",
			cp2Status, cp2Expected, cp2Difference)
	}

	// cp3 confirms it: the rebuilt projection now holds 1000+500-1200=300,
	// exactly what cp3 reported.
	var cp3Status, cp3Expected, cp3Difference string
	if err := store.pool.QueryRow(ctx, `
		SELECT evaluation_status,expected_service_units::text,difference_service_units::text
		FROM balance_checkpoint_evaluations WHERE checkpoint_id=$1`, cp3ID).Scan(
		&cp3Status, &cp3Expected, &cp3Difference); err != nil {
		t.Fatal(err)
	}
	if cp3Status != "matched" || cp3Expected != "300" || cp3Difference != "0" {
		t.Fatalf("cp3 status=%q expected=%s difference=%s, want matched/300/0", cp3Status, cp3Expected, cp3Difference)
	}

	// The synthesized credit survives, sized at the signed difference, and
	// carries no cash: it can never become invoiceable value.
	var synthesizedUnits, synthesizedKind string
	if err := store.pool.QueryRow(ctx, `SELECT service_units::text,credit_kind FROM source_credit_events
		WHERE external_account_id=$1 AND external_event_id=$2`,
		accountID, "unknown-positive:softfail-cp2-event").Scan(&synthesizedUnits, &synthesizedKind); err != nil {
		t.Fatalf("the confirmed blip's synthesized credit: %v", err)
	}
	if synthesizedUnits != "500" || synthesizedKind != "UNKNOWN_POSITIVE" {
		t.Fatalf("synthesized credit units=%s kind=%s, want 500/UNKNOWN_POSITIVE", synthesizedUnits, synthesizedKind)
	}
	if count := countUnknownPositiveCredits(t, store, ctx, accountID); count != 2 {
		t.Fatalf("UNKNOWN_POSITIVE credit count=%d, want 2 (the anchor's plus this confirmed blip's)", count)
	}
	if count := openFreezeCount(t, store, ctx, accountID); count != 0 {
		t.Fatalf("open freeze count=%d, want 0", count)
	}
	// A clean confirm is not a rebaseline, so no rebaseline audit is written.
	var rebaselineAudits int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.balance_blip.rebaselined' AND object_type='balance_checkpoint' AND object_id=$1`,
		cp2ID).Scan(&rebaselineAudits); err != nil {
		t.Fatal(err)
	}
	if rebaselineAudits != 0 {
		t.Fatalf("eligibility.balance_blip.rebaselined audit rows for cp2=%d, want 0 (it confirmed cleanly)", rebaselineAudits)
	}
}

// TestBalanceBlipConfirmationThatCannotSynthesiseIsSoftfailedNotErrored keeps
// XM-INV-BLIP-SOFTFAIL's own branch covered after XM-INV-PENDING-RECON C5.
//
// C5 makes the arithmetic exact: signedExpectedUnits is credits plus cash
// minus usage with nothing floored away, so inserting a credit of exactly the
// deferred difference always rebuilds to zero -- when the insert actually
// happens. The way left for a confirmation to fail to reconcile is for the
// tentative credit not to be written at all, which is what
// synthesizeUnknownPositive's ON CONFLICT DO NOTHING does when a row already
// exists under that item's synthetic event id (one left behind by an earlier,
// partially repaired run is the realistic route).
//
// The contract under test is unchanged: never an error. The deferred item is
// recorded positive_blip_ignored, the tentative credit is rolled back, an
// eligibility.balance_blip.rebaselined audit records both amounts, and the
// confirming item becomes the new pending blip.
func TestBalanceBlipConfirmationThatCannotSynthesiseIsSoftfailedNotErrored(t *testing.T) {
	store, ctx, sourceID, accountID, manifestHash, configHash, anchorAt, _ := newBalanceBlipFixture(t)

	usageAt := anchorAt.Add(time.Minute)
	insertUsageEventDirect(t, store, ctx, sourceID, accountID, "softfail-block-usage-a",
		usageAt, "1200", 1, manifestHash, configHash)
	// The blocker: a zero-unit row already occupying cp2's synthetic credit
	// id. It changes no balance anywhere -- it only makes the tentative
	// insert a no-op.
	insertBlockingSynthesizedCredit(t, store, ctx, sourceID, accountID, "softfail-block-cp2",
		anchorAt, 10, manifestHash, configHash)

	cp2At := anchorAt.Add(10 * time.Minute)
	cp2ID := insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "softfail-block-cp2",
		cp2At, "300", 2, manifestHash, configHash)
	cp3At := anchorAt.Add(20 * time.Minute)
	cp3ID := insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "softfail-block-cp3",
		cp3At, "300", 3, manifestHash, configHash)

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = evaluatePendingBalanceEvidenceTx(ctx, tx, accountID, cp3At.Add(time.Minute),
		AuditActor{Type: "system", ID: "test-worker"}); err != nil {
		t.Fatalf("evaluatePendingBalanceEvidenceTx returned an error instead of a softfailed outcome: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var cp2Status, cp2Expected, cp2Difference string
	if err := store.pool.QueryRow(ctx, `
		SELECT evaluation_status,expected_service_units::text,difference_service_units::text
		FROM balance_checkpoint_evaluations WHERE checkpoint_id=$1`, cp2ID).Scan(
		&cp2Status, &cp2Expected, &cp2Difference); err != nil {
		t.Fatal(err)
	}
	if cp2Status != "positive_blip_ignored" || cp2Expected != "0" || cp2Difference != "500" {
		t.Fatalf("cp2 status=%q expected=%s difference=%s, want positive_blip_ignored/0/500",
			cp2Status, cp2Expected, cp2Difference)
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

	// The blocking row survives: the rollback undoes the credit this run
	// wrote, and this run wrote none (that is why the confirmation could not
	// reconcile). Deleting a row it did not write is how RC108's shadow
	// evaluation failed an account's projection outright -- older syntheses
	// have consumption allocated against them, and that FK is RESTRICT.
	if count := countUnknownPositiveCredits(t, store, ctx, accountID); count != 2 {
		t.Fatalf("UNKNOWN_POSITIVE credit count=%d, want 2 (the anchor's, plus the pre-existing row this run must not delete)", count)
	}
	if count := openFreezeCount(t, store, ctx, accountID); count != 0 {
		t.Fatalf("open freeze count=%d, want 0 (still within the rebaseline cap)", count)
	}

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
// XM-INV-BLIP-SOFTFAIL escalation half: an account that reproduces the exact
// non-reconciling shape from
// TestBalanceBlipConfirmationThatCannotSynthesiseIsSoftfailedNotErrored on
// every subsequent checkpoint (the underlying obstruction never resolves
// itself). Rebaselining forever would loop without end; after
// balanceBlipRebaselineCap consecutive rebaselines with no clean
// confirm/disconfirm in between, the account gets a single SOURCE_GAP freeze
// on the triggering item instead, so the evaluator always makes forward
// progress.
//
// XM-INV-PENDING-RECON C5 note: the floored-ledger shape this used to be
// built on now reconciles cleanly on the first confirmation (see the test
// named for it above), so the repeated non-reconciliation is driven here by a
// blocked synthesis instead -- the escalation logic under test is identical
// either way, and this is the shape that still reaches it.
func TestBalanceBlipSoftfailRebaselineCapEscalatesToSourceGapFreeze(t *testing.T) {
	store, ctx, sourceID, accountID, manifestHash, configHash, anchorAt, _ := newBalanceBlipFixture(t)

	usageAt := anchorAt.Add(time.Minute)
	insertUsageEventDirect(t, store, ctx, sourceID, accountID, "softfail-cap-usage-a",
		usageAt, "1200", 1, manifestHash, configHash)

	// Four checkpoints, each reporting the same 300 against the same
	// unchanged ledger. cp2 through cp4 rebaseline (3, at the cap); cp5's own
	// confirmation attempt against cp4 also fails to reconcile, but by then
	// the cap has been reached, so cp5 escalates to SOURCE_GAP instead of
	// becoming yet another pending blip.
	//
	// Only cp2..cp4 need a blocker: each is deferred and then fails its own
	// confirmation attempt. cp5 is never deferred -- it is the escalation
	// trigger -- so nothing is ever synthesized for it.
	type checkpoint struct {
		id, checkpointID string
		asOf             time.Time
	}
	checkpoints := make([]checkpoint, 0, 4)
	for i, minutes := range []int{10, 20, 30, 40} {
		asOf := anchorAt.Add(time.Duration(minutes) * time.Minute)
		checkpointID := "softfail-cap-cp" + string(rune('2'+i))
		if i < 3 {
			insertBlockingSynthesizedCredit(t, store, ctx, sourceID, accountID, checkpointID,
				anchorAt, int64(10+i), manifestHash, configHash)
		}
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

	// No credit this run wrote survives -- every tentative one was rolled
	// back. The three pre-existing blockers stay: they are not this run's to
	// remove, and deleting an already-allocated synthesis raises the FK
	// RESTRICT that failed a whole account in RC108's shadow evaluation.
	if count := countUnknownPositiveCredits(t, store, ctx, accountID); count != 4 {
		t.Fatalf("UNKNOWN_POSITIVE credit count=%d, want 4 (the anchor's plus three pre-existing blockers)", count)
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

// TestBlipRollbackNeverDeletesACreditItDidNotWrite reproduces the failure that
// made RC108's shadow evaluation not_ready, on the production shape that
// caused it.
//
// The softfail rollback deletes "the tentative credit" by its synthetic event
// id. When the synthesis was a no-op -- ON CONFLICT DO NOTHING, because an
// older synthesis already occupies that id -- the row it deleted was not
// tentative at all: projections since then have allocated consumption against
// it, and consumption_allocations' FK is RESTRICT. The delete raises SQLSTATE
// 23001, which fails the whole account's projection job, on every retry, until
// the failure grading marks it dead.
//
// Production account 40bd883d carried four such credits with 8,336 allocations
// between them; the shadow run cleared its evaluations, replayed them, and hit
// this on the first one.
func TestBlipRollbackNeverDeletesACreditItDidNotWrite(t *testing.T) {
	store, ctx, sourceID, accountID, manifestHash, configHash, anchorAt, _ := newBalanceBlipFixture(t)

	usageAt := anchorAt.Add(time.Minute)
	insertUsageEventDirect(t, store, ctx, sourceID, accountID, "fk-usage-a",
		usageAt, "1200", 1, manifestHash, configHash)
	// The older synthesis, on the id cp2's confirmation would use. Zero units
	// so it changes no arithmetic -- what it changes is that the tentative
	// insert becomes a no-op and the rollback would target this row.
	insertBlockingSynthesizedCredit(t, store, ctx, sourceID, accountID, "fk-cp2",
		anchorAt, 10, manifestHash, configHash)

	cp2At := anchorAt.Add(10 * time.Minute)
	cp2ID := insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "fk-cp2",
		cp2At, "300", 2, manifestHash, configHash)
	cp3At := anchorAt.Add(20 * time.Minute)
	insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "fk-cp3",
		cp3At, "300", 3, manifestHash, configHash)

	// An allocation referencing that credit, the way a projection round leaves
	// one behind. This is what turns the delete into a constraint violation.
	var creditID, usageID string
	if err := store.pool.QueryRow(ctx, `SELECT id::text FROM source_credit_events
		WHERE external_account_id=$1 AND external_event_id=$2`,
		accountID, "unknown-positive:fk-cp2-event").Scan(&creditID); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT id::text FROM source_usage_events
		WHERE external_account_id=$1 AND external_usage_id='fk-usage-a'`, accountID).Scan(&usageID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO consumption_allocations(id,usage_event_id,credit_event_id,
			allocation_order,service_units,cash_minor_delta,projection_version)
		VALUES($1,$2,$3::uuid,1,1,0,1)`,
		randomUUID(), usageID, creditID); err != nil {
		t.Fatalf("seed the allocation that makes the credit undeletable: %v", err)
	}

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = evaluatePendingBalanceEvidenceTx(ctx, tx, accountID, cp3At.Add(time.Minute),
		AuditActor{Type: "system", ID: "test-worker"}); err != nil {
		t.Fatalf("the rollback tried to delete an allocated credit and failed the whole job: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// The older credit and its allocation are untouched -- the rollback had
	// nothing of its own to undo.
	var credits, allocations int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM source_credit_events
		WHERE external_account_id=$1 AND external_event_id=$2`,
		accountID, "unknown-positive:fk-cp2-event").Scan(&credits); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM consumption_allocations
		WHERE credit_event_id=$1::uuid`, creditID).Scan(&allocations); err != nil {
		t.Fatal(err)
	}
	if credits != 1 || allocations != 1 {
		t.Fatalf("pre-existing credit rows=%d allocations=%d, want 1/1 (neither is this run's to remove)",
			credits, allocations)
	}
	// And the outcome is the ordinary softfail: the deferred item is ignored,
	// with the audit row saying no tentative credit was written.
	if status, _, _ := f0Evaluation(t, store, ctx, cp2ID); status != "positive_blip_ignored" {
		t.Fatalf("cp2 status=%q, want positive_blip_ignored", status)
	}
	var rebaselines int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.balance_blip.rebaselined' AND object_id=$1`, cp2ID).Scan(&rebaselines); err != nil {
		t.Fatal(err)
	}
	if rebaselines != 1 {
		t.Fatalf("rebaselined audits=%d, want 1", rebaselines)
	}
}

func f0Evaluation(t *testing.T, store *Store, ctx context.Context, checkpointID string) (status, expected, difference string) {
	t.Helper()
	if err := store.pool.QueryRow(ctx, `
		SELECT evaluation_status,expected_service_units::text,difference_service_units::text
		FROM balance_checkpoint_evaluations WHERE checkpoint_id=$1`, checkpointID).Scan(
		&status, &expected, &difference); err != nil {
		t.Fatal(err)
	}
	return status, expected, difference
}

// TestBlipSynthesisConflictIsAuditedNotSilent is the external review's first
// point. A synthesis that finds a row already on its synthetic event id has
// two very different reasons for doing nothing: the row says the same thing
// (idempotent, this work was already done), or it says something else -- a
// different amount, instant or account. The second is a real disagreement
// between what the ledger holds and what this pass computed, and reporting it
// as "the synthesis did not happen" files it under a routine outcome where
// nobody will look.
//
// The account is not released either way and the older row is never touched;
// what this pins is that the conflicting case is visible.
func TestBlipSynthesisConflictIsAuditedNotSilent(t *testing.T) {
	store, ctx, sourceID, accountID, manifestHash, configHash, anchorAt, _ := newBalanceBlipFixture(t)

	usageAt := anchorAt.Add(time.Minute)
	insertUsageEventDirect(t, store, ctx, sourceID, accountID, "conflict-usage-a",
		usageAt, "1200", 1, manifestHash, configHash)
	// A pre-existing synthesis on cp2's own id carrying a different, non-zero
	// amount: the confirmation would compute 500, the ledger holds 77.
	insertCreditEventDirect(t, store, ctx, sourceID, accountID,
		"unknown-positive:conflict-cp2-event", anchorAt, "77", "UNKNOWN_POSITIVE",
		11, manifestHash, configHash)

	cp2At := anchorAt.Add(10 * time.Minute)
	cp2ID := insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "conflict-cp2",
		cp2At, "377", 2, manifestHash, configHash)
	cp3At := anchorAt.Add(20 * time.Minute)
	insertReconciliationCheckpoint(t, store, ctx, sourceID, accountID, "conflict-cp3",
		cp3At, "377", 3, manifestHash, configHash)

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = evaluatePendingBalanceEvidenceTx(ctx, tx, accountID, cp3At.Add(time.Minute),
		AuditActor{Type: "system", ID: "test-worker"}); err != nil {
		t.Fatalf("a conflicting synthesis must not fail the job: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var conflicts int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.balance_blip.synthesis_conflict' AND object_id=$1`, cp2ID).Scan(&conflicts); err != nil {
		t.Fatal(err)
	}
	if conflicts != 1 {
		t.Fatalf("synthesis_conflict audits=%d, want 1: a row carrying different values is a disagreement, "+
			"not a routine no-op", conflicts)
	}
	// The older row and its amount are untouched.
	var units string
	if err := store.pool.QueryRow(ctx, `SELECT service_units::text FROM source_credit_events
		WHERE external_account_id=$1 AND external_event_id=$2`,
		accountID, "unknown-positive:conflict-cp2-event").Scan(&units); err != nil {
		t.Fatalf("the pre-existing credit must survive: %v", err)
	}
	if units != "77" {
		t.Fatalf("pre-existing credit units=%s, want 77 (never rewritten, never removed)", units)
	}
	if status, _, _ := f0Evaluation(t, store, ctx, cp2ID); status != "positive_blip_ignored" {
		t.Fatalf("cp2 status=%q, want positive_blip_ignored", status)
	}

	// The audit says which fields disagreed, so an operator can tell an amount
	// disagreement from an account or instant one without going to the table.
	var afterHash string
	if err := store.pool.QueryRow(ctx, `SELECT COALESCE(after_hash,'') FROM audit_events
		WHERE action='eligibility.balance_blip.synthesis_conflict' AND object_id=$1`, cp2ID).Scan(&afterHash); err != nil {
		t.Fatal(err)
	}
	wantHash := stateHash(map[string]any{
		"external_event_id":   "unknown-positive:conflict-cp2-event",
		"computed_units":      "500",
		"computed_event_time": anchorAt.UTC(),
		"same_account":        true, "same_units": false, "same_event_time": true,
	})
	if afterHash != wantHash {
		t.Fatalf("conflict audit payload hash=%s, want %s -- it must name which fields disagreed",
			afterHash, wantHash)
	}
}

// Note on the identical-row branch: it cannot be reached through the
// evaluator with a natural fixture, and pretending otherwise would be a
// control that proves nothing. A pre-existing credit carrying exactly the
// amount this pass would compute is, by construction, already in the
// projection -- so the item's difference is zero, it evaluates matched, and
// it is never deferred, so no synthesis is attempted at all. The branch
// exists so that a row which agrees is not reported as a disagreement; the
// comparison itself is pinned by the mutation that treats conflicting rows as
// identical, which turns the test above red.
