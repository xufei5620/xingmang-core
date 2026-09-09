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
	// The target cycle is the empty one just published, and the report says
	// plainly that this tool's own requeue window cannot reach it.
	if result.TargetCycleID == "" || !result.TargetCycleAt.Equal(carryAt.UTC()) {
		t.Fatalf("report target cycle=%q at %s, want the cycle published at %s",
			result.TargetCycleID, result.TargetCycleAt, carryAt.UTC())
	}
	if result.TargetInRequeueWindow {
		t.Fatalf("a cycle above finalized_through is not reachable by this tool's own window: %+v", result)
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

// TestPendingReevaluateApplyOnTheCeilingBoundaryDerivesEvidence is design
// matrix row (m) proper, built so the inclusive lower bound is the only thing
// that can make it pass: finalized_through is set to the target cycle's own
// ceiling, so the requeued window is [ceiling, ceiling].
func TestPendingReevaluateApplyOnTheCeilingBoundaryDerivesEvidence(t *testing.T) {
	f := newIdlePendingFixture(t)
	carryAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	f.publishEmptyBalancesCycle(t, 893, carryAt)
	// Put finalized_through exactly on the cycle ceiling, the state an empty
	// advance leaves behind when the pass lands on a cycle boundary.
	if _, err := f.store.pool.Exec(f.ctx, `UPDATE source_account_eligibility_state
		SET finalized_through=$2,projection_version=projection_version+1,updated_at=now()
		WHERE external_account_id=$1`, f.accountID, carryAt); err != nil {
		t.Fatal(err)
	}
	proofsBefore := f.proofCount(t)

	report := f.runReevaluate(t, false, pendingReevaluateOperator)
	if !report.TargetInRequeueWindow {
		t.Fatalf("a cycle whose ceiling equals finalized_through must be in the requeue window: %+v", report)
	}
	if report.Blocked() {
		t.Fatalf("nothing should block this account: %+v", report.Checks)
	}

	result := f.runReevaluate(t, true, pendingReevaluateOperator)
	if !result.Applied || !result.Queued {
		t.Fatalf("apply did not queue: %+v", result)
	}
	f.processJobs(t)
	if got := f.proofCount(t); got != proofsBefore+1 {
		t.Fatalf("proofs=%d, want %d: the inclusive lower bound is what lets the requeued window reach this cycle",
			got, proofsBefore+1)
	}
	if row := readPendingReconciliation(t, f.store, f.ctx, f.accountID); row.status != "active" {
		t.Fatalf("the derived evidence should have taken the account to its second match: %+v", row)
	}

	// A second apply over the same cycle finds the proof already there and
	// refuses: idempotent by refusal, not by writing a second one.
	second := f.runReevaluate(t, true, pendingReevaluateOperator)
	if second.Applied || second.Queued {
		t.Fatalf("a second apply must be refused: %+v", second)
	}
	if got := f.reevaluateAuditCount(t); got != 1 {
		t.Fatalf("reevaluation_requested audits=%d, want 1 (the refused run writes none)", got)
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
	queued, err := requeuePendingReevaluateJobTx(f.ctx, tx, f.accountID)
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
	queued, err = requeuePendingReevaluateJobTx(f.ctx, tx, f.accountID)
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

// TestPendingReevaluateReportsEvidenceTheWorkerWillHandle is design check 4:
// when evidence is already waiting for the evaluator, the worker gets to it
// on its own and the operator does not need this tool. That is a note, not a
// refusal -- requeueing is harmless, just unnecessary.
func TestPendingReevaluateReportsEvidenceTheWorkerWillHandle(t *testing.T) {
	f := newIdlePendingFixture(t)
	pendingAt := f.lastEvidenceAt.Add(idleCycleSpacing)
	f.observeNegativeCheckpoint(t, "reevaluate-waiting", pendingAt, stringPtr("50"), 6)
	// Deliberately not projected: the checkpoint sits unevaluated.

	result := f.runReevaluate(t, false, pendingReevaluateOperator)
	if result.UnevaluatedEvidence != 1 {
		t.Fatalf("report unevaluated evidence=%d, want 1", result.UnevaluatedEvidence)
	}
	check := requireCheck(t, result, "待评估证据")
	if check.Blocker {
		t.Fatalf("evidence already waiting is a note, not a refusal: %+v", check)
	}
	if !strings.Contains(check.Detail, "1") {
		t.Fatalf("the note must carry the count: %s", check.Detail)
	}
}
