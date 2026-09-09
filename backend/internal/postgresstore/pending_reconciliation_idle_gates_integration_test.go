package postgresstore

import (
	"bytes"
	"testing"
	"time"
)

// The three gates on XM-INV-PENDING-RECON C2's idle derivation, each written
// against the scenario that showed it was needed. All three were found by the
// first adversarial review; before the gates existed, every test below
// released an account that should have stayed parked.

// observeIdlePositiveCheckpoint reports a plain positive balance, the shape a
// source sends after a top-up the ledger has no record of.
func (f *idlePendingFixture) observeIdlePositiveCheckpoint(t *testing.T, checkpointID string,
	asOf time.Time, units string, sourceSequence int64) {
	t.Helper()
	event := SourceBatchEvent{EventID: autoReconcileUUID('8', 870+int(sourceSequence)),
		EntityType: "balance_checkpoint", Operation: "upsert", PayloadHash: testHash("idle-positive-" + checkpointID),
		PayloadCiphertext: bytes.Repeat([]byte{byte(60 + sourceSequence)}, 32), ObservedAt: asOf}
	cycle := f.chain.commit(t, f.store, f.ctx, f.sourceID, "balances",
		autoReconcileUUID('9', 870+int(sourceSequence)), asOf, []SourceBatchEvent{event})
	if err := f.store.ObserveBalanceCheckpoint(f.ctx, BalanceCheckpointObservation{
		SourceInstanceID: f.sourceID, ExternalUserID: "800", ExternalEventID: event.EventID,
		CheckpointID: checkpointID, CheckpointKind: "reconciliation", BaselineMember: false,
		BalanceServiceUnits: units, UnitCode: "SUB2_BALANCE_1E8", SourceSnapshotID: testHash(cycle.cycleID),
		SnapshotRowCount: "1", AsOf: asOf, ObservedAt: asOf, StreamWatermarkAt: asOf,
		SourceCursor: "balance:" + checkpointID, SourceRevision: event.PayloadHash,
		CutoverManifestHash: f.manifestHash, ConfigurationHash: f.configHash, SourceSequence: sourceSequence,
		BatchID: cycle.batchID, ScanCycleID: cycle.cycleID,
	}, AuditActor{Type: "source_connector", ID: f.sourceID}); err != nil {
		t.Fatal(err)
	}
	markV3CycleProcessed(t, f.store, f.ctx, f.sourceID, "balances", cycle)
}

func (f *idlePendingFixture) unknownPositiveUnits(t *testing.T) string {
	t.Helper()
	var units string
	if err := f.store.pool.QueryRow(f.ctx, `SELECT COALESCE(sum(service_units),0)::text
		FROM source_credit_events WHERE external_account_id=$1 AND credit_kind='UNKNOWN_POSITIVE'`,
		f.accountID).Scan(&units); err != nil {
		t.Fatal(err)
	}
	return units
}

func (f *idlePendingFixture) unevaluatedCheckpointCount(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM balance_reconciliation_checkpoints c
		WHERE c.external_account_id=$1 AND c.checkpoint_kind='reconciliation'
		  AND NOT EXISTS (SELECT 1 FROM balance_checkpoint_evaluations e WHERE e.checkpoint_id=c.id)`,
		f.accountID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestIdleDerivationWaitsForTheEvaluatorToJudgeARealCheckpoint is the first
// review's blocker B1, and the reason the idle branch refuses to run while any
// real checkpoint is still unevaluated.
//
// A positive difference the ledger cannot explain is not classified on the
// spot. XM-INV-BALANCE-BLIP defers it and waits for the *next independent*
// piece of evidence to confirm or disconfirm it -- that independence is the
// whole mechanism. An idle proof is not independent of the checkpoint it
// restates: same numbers, same projection (no facts landed, which is the
// precondition for deriving at all), therefore the same difference, therefore
// a confirmation that cannot fail. One upstream observation confirmed itself,
// synthesized a credit for the entire unexplained amount, and released the
// account to invoiceable.
func TestIdleDerivationWaitsForTheEvaluatorToJudgeARealCheckpoint(t *testing.T) {
	f := newIdlePendingFixture(t)
	proofsBefore := f.proofCount(t)

	// One real positive checkpoint the ledger cannot explain: it reports 300
	// against a signed expectation of -50.
	positiveAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	f.observeIdlePositiveCheckpoint(t, "idle-gate-positive", positiveAt, "300", 9)
	carryAt := positiveAt.Add(idleCycleSpacing)
	f.publishEmptyBalancesCycle(t, 971, carryAt)
	f.runFinalize(t, carryAt.Add(time.Minute))
	f.processJobs(t)

	row := readPendingReconciliation(t, f.store, f.ctx, f.accountID)
	if row.status != "not_invoiceable_pending_reconciliation" {
		t.Fatalf("one unexplained observation must not release the account: %+v", row)
	}
	if got := f.unknownPositiveUnits(t); got != "0" {
		t.Fatalf("synthesized %s units of UNKNOWN_POSITIVE from a single observation confirming itself, want 0", got)
	}
	if got := f.proofCount(t); got != proofsBefore {
		t.Fatalf("proofs=%d, want %d: no proof may be derived while a real checkpoint is unjudged", got, proofsBefore)
	}
	// The checkpoint is still deferred, exactly as the blip rule intends: no
	// evaluation row was written for it, and it waits for real next evidence.
	if got := f.unevaluatedCheckpointCount(t); got != 1 {
		t.Fatalf("unevaluated checkpoints=%d, want 1 (deferred, awaiting genuinely independent evidence)", got)
	}

	// Control: once a second, genuinely independent real checkpoint disconfirms
	// it, the ordinary machinery resolves the deferral -- so the assertion above
	// is the gate holding, not the scenario being unable to progress at all.
	disconfirmAt := carryAt.Add(idleCycleSpacing)
	f.observeIdlePositiveCheckpoint(t, "idle-gate-disconfirm", disconfirmAt, "400", 10)
	f.runFinalize(t, disconfirmAt.Add(time.Minute))
	f.processJobs(t)
	if status, _, _ := f.checkpointEvaluation(t, "idle-gate-positive"); status != "positive_blip_ignored" {
		t.Fatalf("control: the deferred checkpoint should now be %q, got %q", "positive_blip_ignored", status)
	}
}

// TestIdleDerivationIsGatedOnTheStreakNotOnlyOnTheEnqueue is the first
// review's M3. The 2026-09-09 ruling (design section 7 D2(a)) narrowed idle
// re-evaluation to accounts already one match from the exit, and that
// narrowing was implemented only in finalizeSourceAccountsTx's enqueue
// predicate -- which bounds nothing, because a projection job has other
// sources. An ordinary real checkpoint landing in the window enqueues the
// account whatever its streak is, and the derivation then ran.
func TestIdleDerivationIsGatedOnTheStreakNotOnlyOnTheEnqueue(t *testing.T) {
	f := newIdlePendingFixture(t)
	// Reset the streak to zero through a real path.
	resetAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	f.observeNegativeCheckpoint(t, "idle-gate-reset", resetAt, stringPtr("70"), 6)
	f.project(t, resetAt.Add(time.Minute))
	if row := readPendingReconciliation(t, f.store, f.ctx, f.accountID); row.consecutiveMatches != 0 {
		t.Fatalf("fixture: want a zero streak, got %+v", row)
	}
	proofsBefore := f.proofCount(t)

	// A published cycle the derivation could otherwise use, and a job enqueued
	// by something other than the streak-narrowed predicate.
	carryAt := resetAt.Add(idleCycleSpacing)
	f.publishEmptyBalancesCycle(t, 972, carryAt)
	if _, err := f.store.pool.Exec(f.ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now())`, f.accountID, carryAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	f.processJobs(t)

	if got := f.proofCount(t); got != proofsBefore {
		t.Fatalf("proofs=%d, want %d: a zero-streak account must not get an idle proof no matter who enqueued it",
			got, proofsBefore)
	}
	row := readPendingReconciliation(t, f.store, f.ctx, f.accountID)
	if row.status != "not_invoiceable_pending_reconciliation" || row.consecutiveMatches != 0 {
		t.Fatalf("zero-streak account must stay exactly where it was: %+v", row)
	}
}

// TestIdleDerivationNeverBackfillsBelowANewerRealCheckpoint is the first
// review's blocker B2. The branch used to walk candidates newest-first and
// skip any cycle already carrying this account's real checkpoint -- so when
// the newest cycle was the one carrying a NEW, disagreeing checkpoint, it fell
// back to an older empty cycle and derived there, restating the PREVIOUS
// checkpoint and dating it below the new one.
//
// The evaluator reads by as_of, so it read the stale proof first, counted it
// as the exit match, cleared the pending columns (destroying
// pending_reconciliation_since, which enterPendingReconciliationTx promises to
// preserve across a re-entry) and only then read the checkpoint that
// disagrees. The exit audit it left behind says the account reconciled twice.
// It had not.
func TestIdleDerivationNeverBackfillsBelowANewerRealCheckpoint(t *testing.T) {
	f := newIdlePendingFixture(t)
	sinceBefore := readPendingReconciliation(t, f.store, f.ctx, f.accountID).since
	proofsBefore := f.proofCount(t)

	// An empty cycle, and then a newer real checkpoint that disagrees: the
	// ledger expects -50, the source says -70.
	emptyAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	f.publishEmptyBalancesCycle(t, 973, emptyAt)
	newerAt := emptyAt.Add(idleCycleSpacing)
	f.observeNegativeCheckpoint(t, "idle-gate-newer-mismatch", newerAt, stringPtr("70"), 7)

	f.runFinalize(t, newerAt.Add(time.Minute))
	f.processJobs(t)

	if got := f.auditCount(t, "eligibility.pending_reconciliation.exited", f.accountID); got != 0 {
		t.Fatalf("exit audits=%d, want 0: a backfilled restatement of stale numbers is not a reconciliation", got)
	}
	if got := f.proofCount(t); got != proofsBefore {
		t.Fatalf("proofs=%d, want %d: nothing may be derived under a newer real checkpoint", got, proofsBefore)
	}
	row := readPendingReconciliation(t, f.store, f.ctx, f.accountID)
	if row.status != "not_invoiceable_pending_reconciliation" {
		t.Fatalf("the account must stay parked -- its newest evidence disagrees: %+v", row)
	}
	if !row.since.Equal(sinceBefore) {
		t.Fatalf("pending_reconciliation_since moved from %s to %s; a re-entry must preserve it",
			sinceBefore, row.since)
	}
	// The newer checkpoint was evaluated on its own merits, which is the whole
	// point: it is the evidence, and it says the account is still not reconciled.
	if status, difference, _ := f.checkpointEvaluation(t, "idle-gate-newer-mismatch"); status != "negative_frozen" ||
		difference != "-20" {
		t.Fatalf("the newer checkpoint should have been judged negative_frozen/-20, got %s/%s", status, difference)
	}
}

// TestIdleDerivationDoesNotReleaseAnAccountWhoseNewestEvidenceIsUnfinalizable
// is the durable form of B2, and the one that would have shipped. The
// disagreeing checkpoint is newer than the finalization window -- the ordinary
// case, since finalization_delay_seconds holds back the most recent quarter
// hour -- so the evaluator cannot see it yet. The idle branch nevertheless
// derived from an older empty cycle, restating the last checkpoint that did
// match, and released the account to invoiceable while the newest thing the
// source had said about it sat on file, unevaluated, disagreeing.
func TestIdleDerivationDoesNotReleaseAnAccountWhoseNewestEvidenceIsUnfinalizable(t *testing.T) {
	f := newIdlePendingFixture(t)
	proofsBefore := f.proofCount(t)

	emptyAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	f.publishEmptyBalancesCycle(t, 974, emptyAt)
	newerAt := emptyAt.Add(idleCycleSpacing)
	f.observeNegativeCheckpoint(t, "idle-gate-unfinalizable", newerAt, stringPtr("70"), 7)

	// The window stops below the newer checkpoint, as a real finalization
	// delay does.
	f.runFinalize(t, emptyAt.Add(time.Minute))
	f.processJobs(t)

	row := readPendingReconciliation(t, f.store, f.ctx, f.accountID)
	unevaluated := f.unevaluatedCheckpointCount(t)
	if unevaluated == 0 {
		t.Fatal("fixture: the newer checkpoint must still be unevaluated, or this proves nothing")
	}
	if row.status != "not_invoiceable_pending_reconciliation" {
		t.Fatalf("account released to %s while %d newer checkpoint(s) remain unevaluated", row.status, unevaluated)
	}
	if got := f.proofCount(t); got != proofsBefore {
		t.Fatalf("proofs=%d, want %d", got, proofsBefore)
	}
}

// TestIdleDerivationStopsAtTheNewestCandidateEvenIfAnOlderOneIsFree drives
// deriveIdlePendingCarryForwardProofTx directly, because through the ordinary
// path the earlier gate hides this one: a newer real checkpoint is, at proof
// time, an unevaluated real checkpoint, so B1's guard refuses first. Once the
// evaluator has judged it the account has either exited (matched) or had its
// streak reset (not matched), so the streak gate refuses instead.
//
// That makes "never reach past a cycle carrying a real checkpoint" a second
// line of defence with no reachable scenario of its own today -- which is
// exactly the kind of guard that quietly stops working. So it gets a test that
// calls the function with the candidate list that would break it: newest
// carries a real checkpoint, an older one is free. It must derive nothing
// rather than fall back.
func TestIdleDerivationStopsAtTheNewestCandidateEvenIfAnOlderOneIsFree(t *testing.T) {
	f := newIdlePendingFixture(t)
	proofsBefore := f.proofCount(t)

	olderAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	newerAt := olderAt.Add(idleCycleSpacing)
	olderCycle := f.publishEmptyBalancesCycle(t, 975, olderAt)
	newerCycle := f.publishEmptyBalancesCycle(t, 976, newerAt)

	tx, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	account, err := getEligibilityAccountTx(f.ctx, tx, f.accountID, false)
	if err != nil {
		t.Fatal(err)
	}
	// Candidates in the ascending order the derivation receives them. Only the
	// prior-checkpoint fields the insert reads need to be real; what is under
	// test is which candidate is chosen.
	candidates := []carryCandidate{
		f.candidateForCycle(t, olderCycle.cycleID),
		f.candidateForCycle(t, newerCycle.cycleID),
	}
	candidates[1].hasRealCheckpoint = true
	if err = deriveIdlePendingCarryForwardProofTx(f.ctx, tx, account, candidates,
		AuditActor{Type: "system", ID: "idle-gate-test"}); err != nil {
		t.Fatalf("derivation returned an error: %v", err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.proofCount(t); got != proofsBefore {
		t.Fatalf("proofs=%d, want %d: the derivation reached past a cycle carrying a real checkpoint "+
			"and backfilled under it", got, proofsBefore)
	}

	// Control: with the newest candidate free, the same call does derive --
	// so the assertion above is the guard, not a call that cannot write.
	tx, err = f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	candidates[1].hasRealCheckpoint = false
	if err = deriveIdlePendingCarryForwardProofTx(f.ctx, tx, account, candidates,
		AuditActor{Type: "system", ID: "idle-gate-test"}); err != nil {
		t.Fatalf("control derivation returned an error: %v", err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.proofCount(t); got != proofsBefore+1 {
		t.Fatalf("control: proofs=%d, want %d", got, proofsBefore+1)
	}
}

// candidateForCycle builds the carryCandidate the derivation's own query
// would produce for one published balances cycle, reading the same cycle,
// final-batch and prior-checkpoint rows it does. Built from the database
// rather than by hand so the row this test asks the derivation to write is
// one the contract triggers accept.
func (f *idlePendingFixture) candidateForCycle(t *testing.T, cycleID string) carryCandidate {
	t.Helper()
	var item carryCandidate
	if err := f.store.pool.QueryRow(f.ctx, `
		SELECT cycle.scan_cycle_id::text,batch.batch_id::text,cycle.scan_snapshot_id,
			cycle.scan_snapshot_row_count,cycle.scan_ceiling_at,cycle.stream_watermark_at,
			cycle.source_cursor,cycle.final_sequence,batch.body_hash,batch.source_captured_at,
			prior.id::text,prior.balance_service_units::text,prior.balance_negative,
			prior.baseline_member,prior.deficit_service_units::text
		FROM source_economic_scan_cycles cycle
		JOIN source_ingest_batches batch
		  ON batch.source_instance_id=cycle.source_instance_id AND batch.stream_id=cycle.stream_id
		 AND batch.scan_cycle_id=cycle.scan_cycle_id AND batch.sequence=cycle.final_sequence
		JOIN LATERAL (
			SELECT checkpoint.id,checkpoint.balance_service_units,checkpoint.balance_negative,
				checkpoint.baseline_member,checkpoint.deficit_service_units
			FROM balance_reconciliation_checkpoints checkpoint
			WHERE checkpoint.external_account_id=$1
			  AND (checkpoint.as_of<cycle.scan_ceiling_at
			       OR (checkpoint.as_of=cycle.scan_ceiling_at
			           AND checkpoint.source_sequence<cycle.final_sequence))
			ORDER BY checkpoint.as_of DESC,checkpoint.source_sequence DESC,checkpoint.id DESC LIMIT 1
		) prior ON true
		WHERE cycle.source_instance_id=$2 AND cycle.stream_id='balances'
		  AND cycle.scan_cycle_id=$3::uuid`, f.accountID, f.sourceID, cycleID).Scan(
		&item.cycleID, &item.batchID, &item.snapshotID, &item.snapshotRows, &item.asOf,
		&item.watermark, &item.cursor, &item.sequence, &item.revision, &item.observed,
		&item.priorID, &item.balance, &item.negative, &item.baselineMember, &item.priorDeficit); err != nil {
		t.Fatalf("candidate for cycle %s: %v", cycleID, err)
	}
	item.asOf, item.watermark, item.observed = item.asOf.UTC(), item.watermark.UTC(), item.observed.UTC()
	return item
}
