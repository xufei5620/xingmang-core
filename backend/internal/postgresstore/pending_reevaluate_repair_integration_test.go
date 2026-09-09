package postgresstore

import (
	"strings"
	"testing"
	"time"
)

// XM-INV-PENDING-RECON C3 (design section 3, acceptance ruling 2026-09-09 on
// section 7 D5(a)): the operator-facing repair kind. Design section 5.1 A8
// and matrix rows (e) and (m).

const pendingReevaluateOperator = "70000000-0000-4000-8000-000000000001"

func (f *idlePendingFixture) runReevaluate(t *testing.T, apply bool, operatorID string) PendingReevaluateRepairResult {
	t.Helper()
	result, err := f.store.RepairPendingReevaluate(f.ctx, PendingReevaluateRepairInput{
		Apply: apply, OperatorID: operatorID, AccountID: f.accountID},
		AuditActor{Type: "admin", ID: operatorID, Reason: "XM-INV-PENDING-RECON repair tool test"})
	if err != nil {
		t.Fatalf("pending-reevaluate (apply=%t): %v", apply, err)
	}
	return result
}

func (f *idlePendingFixture) reevaluateAuditCount(t *testing.T) int {
	t.Helper()
	return f.auditCount(t, "eligibility.pending_reconciliation.reevaluation_requested", f.accountID)
}

// requireCheck returns the named check, failing the test when the report does
// not carry it at all -- so a report that silently stopped producing a check
// cannot pass as "the check held".
func requireCheck(t *testing.T, result PendingReevaluateRepairResult, name string) PendingReevaluateCheck {
	t.Helper()
	for _, check := range result.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("report has no %q check: %+v", name, result.Checks)
	return PendingReevaluateCheck{}
}

// TestPendingReevaluateDryRunReportsAndChangesNothing is design section 5.1
// A8's dry-run half: the report is computed in full, and not one row moves.
func TestPendingReevaluateDryRunReportsAndChangesNothing(t *testing.T) {
	f := newIdlePendingFixture(t)
	carryAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	f.publishEmptyBalancesCycle(t, 891, carryAt)
	// Production shape: the window a finalization pass asks for is
	// min(watermarks) minus the account's delay, and it lands wherever it
	// lands -- here ten minutes past the cycle ceiling, on no boundary at all.
	requested := carryAt.Add(10 * time.Minute)
	f.setWatermarks(t, requested.Add(f.finalizationDelay(t)))
	proofsBefore := f.proofCount(t)
	finalizedBefore := f.finalizedThrough(t)

	result := f.runReevaluate(t, false, "")
	if result.Applied || result.Queued {
		t.Fatalf("a dry run must neither apply nor queue: %+v", result)
	}
	if !result.Found || result.Status != "not_invoiceable_pending_reconciliation" ||
		result.ConsecutiveMatches != pendingReconciliationExitMatches-1 ||
		result.ExitMatches != pendingReconciliationExitMatches {
		t.Fatalf("report does not describe the account: %+v", result)
	}
	if result.Reason != "UNKNOWN_NEGATIVE_BALANCE" || result.TriggerID == "" || result.Since.IsZero() {
		t.Fatalf("report must carry the pending trigger detail: %+v", result)
	}
	if result.OpenFreezes != 0 || result.JobStatus != "" || result.UnevaluatedEvidence != 0 {
		t.Fatalf("report state: freezes=%d job=%q unevaluated=%d, want 0/none/0",
			result.OpenFreezes, result.JobStatus, result.UnevaluatedEvidence)
	}
	// The window the apply will ask for, stated outright: not
	// finalized_through, but what a finalization pass would request from the
	// current watermarks.
	if !result.EffectiveRequestedThrough.Equal(requested.UTC()) {
		t.Fatalf("report requeue window=%s, want %s (min watermark minus the account's finalization delay)",
			result.EffectiveRequestedThrough, requested.UTC())
	}
	if result.WatermarkStreams != 4 {
		t.Fatalf("report watermark streams=%d, want 4", result.WatermarkStreams)
	}
	// The target cycle is the empty one just published, and it is inside that
	// window -- which is the only reason an apply could do anything.
	if result.TargetCycleID == "" || !result.TargetCycleAt.Equal(carryAt.UTC()) {
		t.Fatalf("report target cycle=%q at %s, want the cycle published at %s",
			result.TargetCycleID, result.TargetCycleAt, carryAt.UTC())
	}
	if !result.NewestPublishedCycleAt.Equal(carryAt.UTC()) {
		t.Fatalf("report newest published cycle=%s, want %s", result.NewestPublishedCycleAt, carryAt.UTC())
	}
	if result.TargetHasRealCheckpoint {
		t.Fatalf("the in-window cycle must not already carry a real checkpoint: %+v", result)
	}
	if result.UnevaluatedCheckpoints != 0 {
		t.Fatalf("report unevaluated real checkpoints=%d, want 0", result.UnevaluatedCheckpoints)
	}
	if result.PriorCheckpointID == "" || !result.PriorNegative || result.PriorDeficit != "50" {
		t.Fatalf("report must name the checkpoint a proof would restate: %+v", result)
	}
	// Recomputed here, from the ledger -- not read from any stored evaluation.
	if result.RecomputedStatus != "matched" || result.RecomputedDifference != "0" || result.RecomputedExpected != "-50" {
		t.Fatalf("recomputed expected=%s difference=%s status=%s, want -50/0/matched",
			result.RecomputedExpected, result.RecomputedDifference, result.RecomputedStatus)
	}
	if result.Blocked() {
		t.Fatalf("nothing should block this account: %+v", result.Checks)
	}

	// Nothing moved.
	if _, exists := f.jobStatus(t); exists {
		t.Fatal("a dry run must not enqueue a job")
	}
	if got := f.proofCount(t); got != proofsBefore {
		t.Fatalf("proofs=%d, want %d", got, proofsBefore)
	}
	if now := f.finalizedThrough(t); !now.Equal(finalizedBefore) {
		t.Fatalf("finalized_through moved to %s (was %s)", now, finalizedBefore)
	}
	if got := f.reevaluateAuditCount(t); got != 0 {
		t.Fatalf("dry run wrote %d audit rows for this kind, want 0", got)
	}
	if row := readPendingReconciliation(t, f.store, f.ctx, f.accountID); row.status != "not_invoiceable_pending_reconciliation" ||
		row.consecutiveMatches != pendingReconciliationExitMatches-1 {
		t.Fatalf("account state changed under a dry run: %+v", row)
	}
}

// TestPendingReevaluateApplyDerivesOnAProductionShapedWindow is design matrix
// row (m), rebuilt on the shape production actually has after the first
// review found the old one unreachable there.
//
// finalized_through is min(the source's four stream watermarks) minus the
// account's finalization delay -- a value in seconds, moving every pass. It
// therefore essentially never coincides with a scan cycle ceiling. The
// original requeue wrote requested_through = finalized_through, giving the
// derivation the window [finalized_through, finalized_through], which can
// only ever contain a cycle whose ceiling is exactly that instant. The apply
// printed APPLIED and one account affected, wrote its audit event, and
// derived nothing; the only test that showed it working had put
// finalized_through onto a ceiling by hand.
//
// So: two published cycles, finalized_through below both, and a window that
// lands strictly between them. Nothing here sits on a boundary. The apply
// must still derive, from the cycle inside the window and not the one above
// it.
func TestPendingReevaluateApplyDerivesOnAProductionShapedWindow(t *testing.T) {
	f := newIdlePendingFixture(t)
	firstAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	secondAt := firstAt.Add(idleCycleSpacing)
	f.publishEmptyBalancesCycle(t, 893, firstAt)
	f.publishEmptyBalancesCycle(t, 894, secondAt)
	// Ten minutes past the first ceiling, ten minutes short of the second.
	requested := firstAt.Add(10 * time.Minute)
	f.setWatermarks(t, requested.Add(f.finalizationDelay(t)))
	finalizedBefore := f.finalizedThrough(t)
	proofsBefore := f.proofCount(t)
	if !finalizedBefore.Before(firstAt.UTC()) || !requested.UTC().Before(secondAt.UTC()) {
		t.Fatalf("fixture: the window must straddle exactly one cycle -- finalized=%s first=%s requested=%s second=%s",
			finalizedBefore, firstAt.UTC(), requested.UTC(), secondAt.UTC())
	}

	report := f.runReevaluate(t, false, pendingReevaluateOperator)
	if !report.EffectiveRequestedThrough.Equal(requested.UTC()) {
		t.Fatalf("report requeue window=%s, want %s", report.EffectiveRequestedThrough, requested.UTC())
	}
	if !report.TargetCycleAt.Equal(firstAt.UTC()) {
		t.Fatalf("report target cycle at %s, want the one inside the window (%s), not the one above it (%s)",
			report.TargetCycleAt, firstAt.UTC(), secondAt.UTC())
	}
	if !report.NewestPublishedCycleAt.Equal(secondAt.UTC()) {
		t.Fatalf("report newest published=%s, want %s", report.NewestPublishedCycleAt, secondAt.UTC())
	}
	if report.Blocked() {
		t.Fatalf("nothing should block this account: %+v", report.Checks)
	}

	result := f.runReevaluate(t, true, pendingReevaluateOperator)
	if !result.Applied || !result.Queued {
		t.Fatalf("apply did not queue: %+v", result)
	}
	// The row carries the window the dry run predicted, not finalized_through.
	if got := f.requestedThrough(t); !got.Equal(requested.UTC()) {
		t.Fatalf("job row requested_through=%s, want %s (the dry run's own prediction)", got, requested.UTC())
	}

	f.processJobs(t)
	if got := f.proofCount(t); got != proofsBefore+1 {
		t.Fatalf("proofs=%d, want %d: the apply must actually derive on a production-shaped window",
			got, proofsBefore+1)
	}
	var proofAt time.Time
	if err := f.store.pool.QueryRow(f.ctx, `SELECT as_of FROM balance_carry_forward_proofs
		WHERE external_account_id=$1 AND as_of>$2`, f.accountID, finalizedBefore).Scan(&proofAt); err != nil {
		t.Fatalf("the derived proof: %v", err)
	}
	if !proofAt.UTC().Equal(firstAt.UTC()) {
		t.Fatalf("proof as_of=%s, want the in-window cycle ceiling %s", proofAt.UTC(), firstAt.UTC())
	}
	if row := readPendingReconciliation(t, f.store, f.ctx, f.accountID); row.status != "active" {
		t.Fatalf("the derived evidence should have taken the account to its second match: %+v", row)
	}

	// A second apply over the same window finds the proof already there and
	// refuses: idempotent by refusal, not by writing a second one.
	second := f.runReevaluate(t, true, pendingReevaluateOperator)
	if second.Applied || second.Queued {
		t.Fatalf("a second apply must be refused: %+v", second)
	}
	if got := f.reevaluateAuditCount(t); got != 1 {
		t.Fatalf("reevaluation_requested audits=%d, want 1 (the refused run writes none)", got)
	}
}

// TestPendingReevaluateReportsTheWindowAnExistingJobRowAlreadyAsksFor is the
// other half of what the first review found: the requeue's ON CONFLICT never
// lowers requested_through, so an account whose job row already asks for a
// wider window keeps it. The old report computed the window from
// finalized_through alone and therefore told the operator the apply would
// change nothing -- and the apply then derived evidence, evaluated it matched
// and returned the account to invoiceable. Being told "nothing will happen"
// and getting an account made billable is the worst shape a repair tool can
// have.
//
// Here the watermarks alone would give a window of exactly finalized_through
// (no room at all), and only the existing row's window reaches the cycle. The
// report must say so, and the apply must do exactly that.
func TestPendingReevaluateReportsTheWindowAnExistingJobRowAlreadyAsksFor(t *testing.T) {
	f := newIdlePendingFixture(t)
	carryAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	f.publishEmptyBalancesCycle(t, 895, carryAt)
	finalizedBefore := f.finalizedThrough(t)
	// Watermarks that leave the natural window at finalized_through exactly.
	f.setWatermarks(t, finalizedBefore.Add(f.finalizationDelay(t)))
	// An existing queued job row already asking for more, the shape a
	// finalization pass leaves behind whenever the worker has not caught up.
	wider := carryAt.Add(time.Minute)
	if _, err := f.store.pool.Exec(f.ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now())`, f.accountID, wider); err != nil {
		t.Fatal(err)
	}
	proofsBefore := f.proofCount(t)

	report := f.runReevaluate(t, false, pendingReevaluateOperator)
	if !report.EffectiveRequestedThrough.Equal(wider.UTC()) {
		t.Fatalf("report requeue window=%s, want the existing row's own wider window %s -- "+
			"the upsert never lowers it", report.EffectiveRequestedThrough, wider.UTC())
	}
	if !report.TargetCycleAt.Equal(carryAt.UTC()) {
		t.Fatalf("report target cycle=%s, want %s: the wider window reaches it", report.TargetCycleAt, carryAt.UTC())
	}
	if report.Blocked() {
		t.Fatalf("the wider window makes this account actionable, so nothing should block: %+v", report.Checks)
	}

	result := f.runReevaluate(t, true, pendingReevaluateOperator)
	if !result.Applied || !result.Queued {
		t.Fatalf("apply did not queue: %+v", result)
	}
	if got := f.requestedThrough(t); !got.Equal(wider.UTC()) {
		t.Fatalf("job row requested_through=%s, want %s (never lowered)", got, wider.UTC())
	}
	f.processJobs(t)
	if got := f.proofCount(t); got != proofsBefore+1 {
		t.Fatalf("proofs=%d, want %d -- and the report said this would happen", got, proofsBefore+1)
	}
	if row := readPendingReconciliation(t, f.store, f.ctx, f.accountID); row.status != "active" {
		t.Fatalf("the account exited, exactly as the report predicted: %+v", row)
	}
}

// TestPendingReevaluateRefusesAnAccountThatIsNotPending is design check 1.
func TestPendingReevaluateRefusesAnAccountThatIsNotPending(t *testing.T) {
	f := newOverdrawnAccountFixture(t)
	idle := &idlePendingFixture{overdrawnAccountFixture: f, lastEvidenceAt: f.anchorAt}
	result := idle.runReevaluate(t, true, pendingReevaluateOperator)
	if result.Applied || result.Queued {
		t.Fatalf("an active account must not be requeued by this tool: %+v", result)
	}
	// A refused apply must still report that an apply was asked for -- the
	// printed banner reads "REFUSED", never "DRY RUN", so an operator who
	// typed --apply is not left thinking they mistyped the flag.
	if !result.ApplyRequested {
		t.Fatalf("a refused apply must still record that --apply was requested: %+v", result)
	}
	if check := requireCheck(t, result, "状态"); check.Passed || !check.Blocker {
		t.Fatalf("the state check must refuse an active account: %+v", check)
	}
	if _, exists := idle.jobStatus(t); exists {
		t.Fatal("a refused apply must not enqueue a job")
	}
	if got := idle.reevaluateAuditCount(t); got != 0 {
		t.Fatalf("a refused apply wrote %d audit rows, want 0", got)
	}
}

// TestPendingReevaluateRefusesWhileAFreezeIsOpen is design check 2: the exit
// would be blocked by the freeze guard and C2 would not derive anything, so
// the requeue can only burn a cycle of the operator's attention.
func TestPendingReevaluateRefusesWhileAFreezeIsOpen(t *testing.T) {
	f := newIdlePendingFixture(t)
	if _, err := f.store.pool.Exec(f.ctx, `INSERT INTO eligibility_freezes(
		id,external_account_id,freeze_reason,trigger_object_type,trigger_object_id)
		VALUES($1,$2,'EVENT_PAYLOAD_DRIFT','source_stream','balances')`,
		autoReconcileUUID('6', 894), f.accountID); err != nil {
		t.Fatal(err)
	}
	result := f.runReevaluate(t, true, pendingReevaluateOperator)
	if result.Applied || result.Queued {
		t.Fatalf("an account with an open freeze must not be requeued: %+v", result)
	}
	if check := requireCheck(t, result, "冻结"); check.Passed || !check.Blocker {
		t.Fatalf("the freeze check must refuse: %+v", check)
	}
	if result.OpenFreezes != 1 {
		t.Fatalf("report open freezes=%d, want 1", result.OpenFreezes)
	}
}

// TestPendingReevaluateRefusesAProcessingJob is design check 3, first half:
// a worker is holding the account right now.
func TestPendingReevaluateRefusesAProcessingJob(t *testing.T) {
	f := newIdlePendingFixture(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	seedEligibilityProjectionJobRowProcessing(t, f.store, f.ctx, f.accountID, now, now, now.Add(2*time.Minute))

	result := f.runReevaluate(t, true, pendingReevaluateOperator)
	if result.Applied || result.Queued {
		t.Fatalf("a processing job must refuse the apply: %+v", result)
	}
	if check := requireCheck(t, result, "作业"); check.Passed || !check.Blocker {
		t.Fatalf("the job check must refuse a processing row: %+v", check)
	}
	status, _ := f.jobStatus(t)
	if status != "processing" {
		t.Fatalf("job status=%q, want processing (untouched)", status)
	}
}

// TestPendingReevaluateRefusesADeadJobAndNeverRevivesIt is design check 3,
// second half and A8's dead-row clause. status='dead' is
// --kind=projection-requeue-dead's decision to make, not this tool's: the
// report says so by name, the apply is refused, and the DO UPDATE's own
// status<>'dead' clause means the refusal holds even if the check were
// bypassed.
func TestPendingReevaluateRefusesADeadJobAndNeverRevivesIt(t *testing.T) {
	f := newIdlePendingFixture(t)
	if _, err := f.store.pool.Exec(f.ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,
			attempt_count,next_attempt_at,last_error_code)
		VALUES($1,$2,'dead',8,now(),'PROJECTION_FAILED')`, f.accountID, f.finalizedThrough(t)); err != nil {
		t.Fatal(err)
	}
	result := f.runReevaluate(t, true, pendingReevaluateOperator)
	if result.Applied || result.Queued {
		t.Fatalf("a dead job must refuse the apply: %+v", result)
	}
	check := requireCheck(t, result, "作业")
	if check.Passed || !check.Blocker {
		t.Fatalf("the job check must refuse a dead row: %+v", check)
	}
	if !strings.Contains(check.Detail, "projection-requeue-dead") {
		t.Fatalf("the refusal must name the tool that owns this decision: %s", check.Detail)
	}
	status, _ := f.jobStatus(t)
	if status != "dead" {
		t.Fatalf("job status=%q, want dead (never revived from here)", status)
	}
	if got := f.reevaluateAuditCount(t); got != 0 {
		t.Fatalf("a refused apply wrote %d audit rows, want 0", got)
	}
}

// TestPendingReevaluateRequeueStatementRefusesADeadRowOnItsOwn tests the
// second of the two layers above directly. The report's job check refuses a
// dead row before the write is ever reached, so in the ordinary flow the
// statement's own status<>'dead' clause never decides anything -- and a test
// that goes through the tool therefore cannot tell whether the clause is
// there at all. This one calls the statement, so the clause has to earn its
// place: a dead row is left dead and nothing is queued, while a queued row
// through the same statement is requeued.
func TestPendingReevaluateRequeueStatementRefusesADeadRowOnItsOwn(t *testing.T) {
	f := newIdlePendingFixture(t)
	if _, err := f.store.pool.Exec(f.ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,
			attempt_count,next_attempt_at,last_error_code)
		VALUES($1,$2,'dead',8,now(),'PROJECTION_FAILED')`, f.accountID, f.finalizedThrough(t)); err != nil {
		t.Fatal(err)
	}
	tx, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	queued, _, err := requeuePendingReevaluateJobTx(f.ctx, tx, f.accountID)
	if err != nil {
		_ = tx.Rollback(f.ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if queued {
		t.Fatal("the statement reported a dead row as queued")
	}
	status, _ := f.jobStatus(t)
	if status != "dead" {
		t.Fatalf("job status=%q, want dead: the statement itself must never revive one", status)
	}

	// Control: the same statement does requeue a row that is not dead, so
	// the refusal above is the status clause and not a broken statement.
	if _, err = f.store.pool.Exec(f.ctx, `UPDATE eligibility_projection_jobs
		SET status='failed',next_attempt_at=now()+interval '1 hour' WHERE external_account_id=$1`, f.accountID); err != nil {
		t.Fatal(err)
	}
	tx, err = f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	queued, _, err = requeuePendingReevaluateJobTx(f.ctx, tx, f.accountID)
	if err != nil {
		_ = tx.Rollback(f.ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if !queued {
		t.Fatal("control: a non-dead row must be requeued by the same statement")
	}
	if status, _ = f.jobStatus(t); status != "queued" {
		t.Fatalf("control: job status=%q, want queued", status)
	}
}

// TestPendingReevaluateRefusesTheAccountsOwnOperator is design check 7,
// reusing ResolveEligibilityFreeze's own rule: an operator may not act on
// their own account. The production account this slice was written around is
// the operator's own, so this guard is not hypothetical.
func TestPendingReevaluateRefusesTheAccountsOwnOperator(t *testing.T) {
	f := newIdlePendingFixture(t)
	carryAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	f.publishEmptyBalancesCycle(t, 895, carryAt)
	var ownerID string
	if err := f.store.pool.QueryRow(f.ctx, `SELECT invoice_user_id::text FROM external_accounts WHERE id=$1`,
		f.accountID).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}

	result := f.runReevaluate(t, true, ownerID)
	if result.Applied || result.Queued {
		t.Fatalf("an operator must not re-evaluate their own account: %+v", result)
	}
	if check := requireCheck(t, result, "自利守卫"); check.Passed || !check.Blocker {
		t.Fatalf("the self-dealing guard must refuse: %+v", check)
	}
	// Control: a different operator on the same account is allowed through
	// that check, so the refusal above is the identity comparison and not a
	// blanket rejection.
	control := f.runReevaluate(t, false, pendingReevaluateOperator)
	if check := requireCheck(t, control, "自利守卫"); !check.Passed || check.Blocker {
		t.Fatalf("a different operator must pass the self-dealing guard: %+v", check)
	}
}

// TestPendingReevaluateRefusesAnUnknownMagnitudePrior is design check 5's
// last clause: if the checkpoint a proof would restate reports a negative
// balance with no magnitude, the re-evaluation can only produce
// negative_frozen(unknown) -- which cannot advance anything -- so the apply
// is refused rather than spending a cycle's exclusivity on it.
func TestPendingReevaluateRefusesAnUnknownMagnitudePrior(t *testing.T) {
	f := newIdlePendingFixture(t)
	// A newer checkpoint with no magnitude becomes the prior a derivation
	// would restate. C4 keeps the streak, so the account is still at N-1.
	unknownAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	f.observeNegativeCheckpoint(t, "reevaluate-unknown", unknownAt, nil, 6)
	f.project(t, unknownAt.Add(time.Minute))
	carryAt := unknownAt.Add(idleCycleSpacing)
	f.publishEmptyBalancesCycle(t, 896, carryAt)
	f.setWatermarks(t, carryAt.Add(10*time.Minute).Add(f.finalizationDelay(t)))

	result := f.runReevaluate(t, true, pendingReevaluateOperator)
	if result.Applied || result.Queued {
		t.Fatalf("an unknown-magnitude prior must refuse the apply: %+v", result)
	}
	if check := requireCheck(t, result, "复述量级"); check.Passed || !check.Blocker {
		t.Fatalf("the restated-magnitude check must refuse: %+v", check)
	}
	if result.RecomputedStatus != "negative_frozen(unknown)" {
		t.Fatalf("recomputed status=%q, want negative_frozen(unknown)", result.RecomputedStatus)
	}
}

// TestPendingReevaluateRefusesWhileARealCheckpointIsUnevaluated is design
// check 4, corrected by the first review. It used to be a note -- "the worker
// will get to it, you do not need this tool" -- on the reasoning that
// requeueing was merely unnecessary. It is not merely unnecessary: while the
// evaluator still owes a verdict on real evidence the derivation refuses to
// run at all (an idle proof restating an unjudged checkpoint is not
// independent of it), so an apply would enqueue a job that cannot do the thing
// the operator asked for.
//
// The count comes from countUnevaluatedBalanceEvidenceTx, the same function
// the derivation's own guard calls, so the report and the behaviour cannot
// disagree about what "unevaluated" means.
func TestPendingReevaluateRefusesWhileARealCheckpointIsUnevaluated(t *testing.T) {
	f := newIdlePendingFixture(t)
	pendingAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	f.observeNegativeCheckpoint(t, "reevaluate-waiting", pendingAt, stringPtr("50"), 6)
	// Deliberately not projected: the checkpoint sits unevaluated.

	result := f.runReevaluate(t, true, pendingReevaluateOperator)
	if result.Applied || result.Queued {
		t.Fatalf("an apply must be refused while a real checkpoint is unjudged: %+v", result)
	}
	if result.UnevaluatedCheckpoints != 1 || result.UnevaluatedEvidence != 1 {
		t.Fatalf("report unevaluated checkpoints=%d total=%d, want 1/1",
			result.UnevaluatedCheckpoints, result.UnevaluatedEvidence)
	}
	check := requireCheck(t, result, "待评估证据")
	if check.Passed || !check.Blocker {
		t.Fatalf("an unevaluated real checkpoint must refuse the apply: %+v", check)
	}
	if !strings.Contains(check.Detail, "1") {
		t.Fatalf("the refusal must carry the count: %s", check.Detail)
	}

	// Control: once the worker has judged it, the same check passes -- so the
	// refusal above is the condition holding, not a check that always blocks.
	f.project(t, pendingAt.Add(time.Minute))
	control := f.runReevaluate(t, false, pendingReevaluateOperator)
	if control.UnevaluatedCheckpoints != 0 {
		t.Fatalf("control: unevaluated checkpoints=%d, want 0", control.UnevaluatedCheckpoints)
	}
	if check := requireCheck(t, control, "待评估证据"); !check.Passed || check.Blocker {
		t.Fatalf("control: with nothing unevaluated the check must pass: %+v", check)
	}
}

// TestPendingReevaluateRefusesBelowTheIdleStreakThreshold is the second
// review's first new finding. The report reproduced the derivation's cycle
// selection and its arithmetic faithfully, but not its first gate: since M3
// the derivation refuses below pendingReconciliationIdleMinMatches, and the
// report only mentioned the streak in passing, as a passing check.
//
// The shape that reaches it: an account is pending with a zero streak (an
// earlier item that did not reconcile reset it), the ledger is then corrected
// by a repair that produces no new fact -- policy-start-reanchor,
// balance-anchor -- and an operator runs this tool. Every other check passes,
// the report promises a derivation and a matched verdict, and the worker stops
// at the streak gate. APPLIED, one account affected, nothing moved: the same
// "looks correct, fails silently" shape the first review caught on the window.
//
// Both sides read pendingReconciliationIdleMinMatches, so the report cannot
// promise what the derivation will not do.
func TestPendingReevaluateRefusesBelowTheIdleStreakThreshold(t *testing.T) {
	f := newIdlePendingFixture(t)
	// Reset the streak through a real path: a stated magnitude that disagrees.
	resetAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	f.observeNegativeCheckpoint(t, "streak-gate-reset", resetAt, stringPtr("70"), 6)
	f.project(t, resetAt.Add(time.Minute))
	if row := readPendingReconciliation(t, f.store, f.ctx, f.accountID); row.consecutiveMatches != 0 {
		t.Fatalf("fixture: want a zero streak, got %+v", row)
	}
	// Everything else the tool looks at is in order: a published cycle inside
	// a real window, nothing unevaluated, no freeze, no job row.
	carryAt := resetAt.Add(idleCycleSpacing)
	f.publishEmptyBalancesCycle(t, 981, carryAt)
	f.setWatermarks(t, carryAt.Add(10*time.Minute).Add(f.finalizationDelay(t)))
	proofsBefore := f.proofCount(t)

	report := f.runReevaluate(t, false, pendingReevaluateOperator)
	check := requireCheck(t, report, "连击门槛")
	if check.Passed || !check.Blocker {
		t.Fatalf("a streak below the idle threshold must be a STOP: %+v", check)
	}
	if report.ConsecutiveMatches != 0 {
		t.Fatalf("report streak=%d, want 0", report.ConsecutiveMatches)
	}
	// The rest of the report is still computed -- the operator needs to see
	// that the only thing missing is the streak.
	if report.TargetCycleID == "" {
		t.Fatalf("the report must still describe the cycle it would have used: %+v", report)
	}

	result := f.runReevaluate(t, true, pendingReevaluateOperator)
	if result.Applied || result.Queued {
		t.Fatalf("apply must be refused below the idle streak threshold: %+v", result)
	}
	if _, exists := f.jobStatus(t); exists {
		t.Fatal("a refused apply must not enqueue a job")
	}
	if got := f.proofCount(t); got != proofsBefore {
		t.Fatalf("proofs=%d, want %d", got, proofsBefore)
	}
	if got := f.reevaluateAuditCount(t); got != 0 {
		t.Fatalf("a refused apply wrote %d audit rows, want 0", got)
	}

	// Control: one real matched evaluation lifts the streak to the threshold,
	// and the same account then passes -- so the STOP above is the threshold,
	// not a check that always blocks.
	// Two spacings, not one: setWatermarks above raised the balances watermark
	// to carryAt+25m, and committing a cycle below a stream's own watermark is a
	// STREAM_WATERMARK_REGRESSION -- which freezes the account and would make
	// this control prove nothing.
	matchAt := carryAt.Add(2 * idleCycleSpacing)
	f.observeNegativeCheckpoint(t, "streak-gate-match", matchAt, stringPtr("50"), 8)
	f.project(t, matchAt.Add(time.Minute))
	if row := readPendingReconciliation(t, f.store, f.ctx, f.accountID); row.consecutiveMatches != pendingReconciliationIdleMinMatches {
		t.Fatalf("control: want a streak of %d, got %+v", pendingReconciliationIdleMinMatches, row)
	}
	nextAt := matchAt.Add(idleCycleSpacing)
	f.publishEmptyBalancesCycle(t, 982, nextAt)
	f.setWatermarks(t, nextAt.Add(10*time.Minute).Add(f.finalizationDelay(t)))
	control := f.runReevaluate(t, false, pendingReevaluateOperator)
	if check := requireCheck(t, control, "连击门槛"); !check.Passed || check.Blocker {
		t.Fatalf("control: at the threshold the check must pass: %+v", check)
	}
}
