package postgresstore

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"invoice-system/backend/internal/domain"
)

// PendingReevaluateRepairInput drives RepairPendingReevaluate (design
// XM-INV-PENDING-RECON section 3 C3, acceptance ruling 2026-09-09 on section
// 7 D5(a)): the operator-facing half of this slice's idle re-evaluation.
//
// What it does is deliberately small: it asks the projection worker to look at
// one account again, now, by enqueueing that account's own projection job.
// The re-evaluation itself is then done by the ordinary worker through the
// ordinary path -- ensureBalanceCarryForwardProofTx derives the evidence and
// evaluatePendingBalanceEvidenceTx judges it. This tool writes no proof, no
// evaluation and no eligibility_status; it cannot make an account exit
// not_invoiceable_pending_reconciliation, and the acceptance line's N=2
// ruling is untouched by it.
//
// What it mostly is, therefore, is a diagnosis. The dry run answers the six
// questions an operator would otherwise work out by hand against production
// -- is the account actually pending, is anything blocking the exit, is the
// worker already going to handle this, which cycle would the evidence come
// from, and what would the evaluator say about it -- and refuses to apply
// when any of them says the requeue cannot help. Alternative (b) from the
// design, an operator standing in for the second match, is deliberately not
// implemented here: it changes the N=2 ruling and needs the acceptance line
// to rule again first.
type PendingReevaluateRepairInput struct {
	// Apply, when false (the default), computes and reports every check
	// below and changes nothing. Apply enqueues the account's projection job
	// and writes the audit event, but only if every check passed.
	Apply bool
	// OperatorID is required when Apply is true: the admin UUID recorded on
	// the audit event, and the identity the self-dealing guard checks.
	OperatorID string
	// AccountID is the one external account to re-evaluate. Required in both
	// modes -- this tool is never run unnarrowed. See the design's own "one
	// reviewed account at a time" rule, the same one
	// ingest-acknowledge-unreplayable follows for events.
	AccountID string
}

// PendingReevaluateCheck is one line of the dry-run report: a named
// precondition, whether it holds, and the operator-facing explanation. Detail
// is Chinese because it is read by whoever is on call, not parsed.
type PendingReevaluateCheck struct {
	// Code is the stable, ASCII identity of the condition, used to keep the
	// runbook's STOP table and this code from drifting apart. Name and Detail
	// are prose and may be reworded freely; Code may not.
	Code    string
	Name    string
	Passed  bool
	Detail  string
	Blocker bool
}

// PendingReevaluateBlockerCodes is every condition that can refuse an apply.
// The runbook's STOP table documents exactly these, one row each, and
// TestPendingReevaluateBlockersMatchTheRunbookTable holds the two to the same
// length -- a blocker added here without a row there fails the build, which is
// the only way a table of "what to do instead" stays true as the tool grows.
var PendingReevaluateBlockerCodes = []string{
	"account_missing",
	"not_pending",
	"open_freeze",
	"job_processing",
	"job_dead",
	"window_empty",
	"no_derivable_cycle",
	"cycle_has_proof",
	"cycle_has_stranded",
	"prior_unknown_magnitude",
	"self_dealing",
}

// PendingReevaluateRepairResult is the tool's whole answer, printable as-is
// by the CLI in both modes.
type PendingReevaluateRepairResult struct {
	// Applied is true only when --apply ran and every check passed.
	Applied bool
	// ApplyRequested records that --apply was asked for, whether or not it
	// went through. Without it a refused apply is indistinguishable from a
	// dry run in the printed report, and an operator reading
	// "DRY RUN (nothing was changed)" after typing --apply would reasonably
	// conclude they had mistyped the flag rather than that the tool refused.
	ApplyRequested bool
	// Queued is true only when Apply actually enqueued the job.
	Queued bool
	// Found is false when no source_account_eligibility_state row exists for
	// the requested account -- a defined, empty result, not an error.
	Found              bool
	AccountID          string
	SourceInstanceID   string
	Status             string
	Reason             string
	TriggerType        string
	TriggerID          string
	Since              time.Time
	ConsecutiveMatches int
	ExitMatches        int
	FinalizedThrough   time.Time
	OpenFreezes        int
	// JobStatus is the account's current eligibility_projection_jobs status,
	// or "" when it has no job row.
	JobStatus string
	// UnevaluatedEvidence counts checkpoints and proofs already waiting for
	// the evaluator -- the same NOT EXISTS predicate the evaluator selects on.
	UnevaluatedEvidence int
	// EffectiveRequestedThrough is the requested_through the account's job row
	// will actually carry after an apply, and therefore the whole window the
	// worker will then derive over. It is deliberately not finalized_through:
	// the requeue asks for the window a finalization pass would ask for, and
	// an existing job row's own wider window is kept rather than lowered.
	// Every window judgement below is made against this one value, so the dry
	// run and the apply cannot disagree about what will happen.
	EffectiveRequestedThrough time.Time
	// WatermarkStreams is how many of the source's four economic stream
	// watermarks exist. Below four, finalization itself does not run at all
	// and there is no window to derive over.
	WatermarkStreams int
	// TargetCycleID/TargetCycleAt describe the published balances scan cycle
	// an idle derivation would actually take: the newest one inside
	// [FinalizedThrough, EffectiveRequestedThrough] that does not already
	// carry a real checkpoint for this account -- ensureBalanceCarryForwardProofTx's
	// own selection rule, not an approximation of it. Empty when there is none.
	TargetCycleID string
	TargetCycleAt time.Time
	// SkippedRealCheckpointCycles counts cycles inside the window passed over
	// because they already carry a real checkpoint for this account, so a
	// target older than the newest cycle in the window is explained rather
	// than surprising.
	SkippedRealCheckpointCycles int
	// NewestPublishedCycleAt is the newest published balances cycle for the
	// source, window or no window. When it sits above the window the account
	// is simply waiting for the finalization delay to pass, which is the
	// ordinary state and worth saying out loud rather than reporting as a
	// missing cycle.
	NewestPublishedCycleAt time.Time
	TargetHasProof         bool
	TargetHasStranded      bool
	PriorCheckpointID      string
	PriorAsOf              time.Time
	PriorBalance           string
	PriorNegative          bool
	PriorDeficit           string
	// RecomputedStatus/RecomputedDifference are this run's own arithmetic for
	// the evidence a derivation would produce -- computed here, never read
	// from a stored evaluation row.
	RecomputedStatus     string
	RecomputedExpected   string
	RecomputedDifference string
	Checks               []PendingReevaluateCheck
}

// Blocked reports whether any check refuses an apply.
func (r PendingReevaluateRepairResult) Blocked() bool {
	for _, check := range r.Checks {
		if check.Blocker {
			return true
		}
	}
	return false
}

// RepairPendingReevaluate implements design XM-INV-PENDING-RECON section 3
// C3. One account, one SERIALIZABLE transaction, and the same per-account
// advisory lock (hashtextextended(...,43)) processEligibilityProjectionJob
// takes -- this reads the account's own projection state and, in apply mode,
// its job row, so it must serialize against a running projection the same
// way the sibling repairs do.
//
// Every branch returns a defined result. An account that does not exist, is
// not pending, or is blocked by any check is a report with Applied/Queued
// false, not an error; only a genuine database failure is returned as one.
func (s *Store) RepairPendingReevaluate(ctx context.Context, in PendingReevaluateRepairInput, actor AuditActor) (PendingReevaluateRepairResult, error) {
	accountID := strings.TrimSpace(in.AccountID)
	if !eligibilityUUIDPattern.MatchString(accountID) {
		return PendingReevaluateRepairResult{}, errors.New("a valid external account UUID is required")
	}
	if in.Apply && !eligibilityUUIDPattern.MatchString(strings.TrimSpace(in.OperatorID)) {
		return PendingReevaluateRepairResult{}, errors.New("a valid operator UUID is required to apply")
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return PendingReevaluateRepairResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,43))`, accountID); err != nil {
		return PendingReevaluateRepairResult{}, err
	}

	result := PendingReevaluateRepairResult{Applied: in.Apply, ApplyRequested: in.Apply,
		AccountID: accountID, ExitMatches: pendingReconciliationExitMatches}
	account, err := getEligibilityAccountTx(ctx, tx, accountID, false)
	if err != nil {
		if errors.Is(err, domain.ErrSourceUnavailable) {
			result.Applied = false
			result.Checks = append(result.Checks, PendingReevaluateCheck{Code: "account_missing", Name: "账号",
				Detail: "该外部账号没有开票侧的资格状态行，无法重评", Blocker: true})
			return result, nil
		}
		return PendingReevaluateRepairResult{}, err
	}
	result.Found = true
	result.SourceInstanceID = account.SourceInstanceID
	result.Status = account.Status
	result.FinalizedThrough = account.FinalizedThrough.UTC()

	if err = s.collectPendingReevaluateChecks(ctx, tx, account, in, &result); err != nil {
		return PendingReevaluateRepairResult{}, err
	}
	if !in.Apply || result.Blocked() {
		// Dry run, or an apply that any check refuses: nothing is committed.
		result.Applied = in.Apply && !result.Blocked()
		return result, nil
	}

	queued, written, err := requeuePendingReevaluateJobTx(ctx, tx, accountID)
	if err != nil {
		return PendingReevaluateRepairResult{}, err
	}
	result.Queued = queued
	if queued && !written.Equal(result.EffectiveRequestedThrough) {
		// The report told the operator which window the apply would ask for,
		// and every judgement above -- which cycle, what the evaluator would
		// decide -- was made against it. If the write disagreed, the report
		// they approved described a different operation, so refuse rather
		// than commit one they were not shown.
		return PendingReevaluateRepairResult{}, fmt.Errorf(
			"predicted requeue window %s but wrote %s; refusing to commit a run the report did not describe",
			result.EffectiveRequestedThrough.Format(time.RFC3339Nano), written.Format(time.RFC3339Nano))
	}
	after := map[string]any{
		"repair_tool":           "XM-INV-PENDING-RECON",
		"consecutive_matches":   result.ConsecutiveMatches,
		"target_cycle_id":       result.TargetCycleID,
		"prior_checkpoint_id":   result.PriorCheckpointID,
		"recomputed_difference": result.RecomputedDifference,
		"recomputed_status":     result.RecomputedStatus,
		"requested_through":     written.Format(time.RFC3339Nano),
		"queued":                result.Queued,
	}
	if err = writeAudit(ctx, tx, actor, "eligibility.pending_reconciliation.reevaluation_requested",
		"external_account", accountID, nil, after); err != nil {
		return PendingReevaluateRepairResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return PendingReevaluateRepairResult{}, err
	}
	return result, nil
}

// pendingReevaluateWindowSQL is the requested_through a requeue asks for,
// against a source_account_eligibility_state aliased "eas". It is
// finalizeSourceAccountsTx's own expression -- GREATEST(cutover_at, the
// source's minimum stream watermark minus the account's finalization delay),
// floored at finalized_through -- and EnqueueEligibilityShadowFinalizationWindow
// computes the same thing for the shadow harness.
//
// Asking for exactly what a finalization pass would ask for is the point.
// requested_through is what the job publishes as the new finalized_through,
// so a window wider than that would finalize an interval the ordinary path
// has deliberately not finalized yet, and any fact still arriving inside it
// would become a late fact. A window narrower than that -- in particular
// finalized_through itself, which is what this statement used to write --
// gives the derivation a zero-width window: the candidate range
// [finalized_through, requested_through] then holds only a cycle whose
// ceiling is exactly finalized_through, and since finalized_through is
// min(watermarks) minus a delay measured in seconds it essentially never
// lands on a cycle ceiling. The apply reported success and derived nothing.
//
// The four-watermark requirement is finalizeSourceAccountsTx's own guard
// (it returns without doing anything when a stream is missing). GREATEST
// ignores NULLs, so a source short of a watermark row yields finalized_through
// and the report says so rather than silently asking for a useless window.
const pendingReevaluateWindowSQL = `GREATEST(eas.finalized_through,eas.cutover_at,
	(SELECT min(w.watermark_at) FROM source_economic_stream_watermarks w
	  WHERE w.source_instance_id=eas.source_instance_id
	  HAVING count(*)=4)-make_interval(secs=>eas.finalization_delay_seconds))`

// requeuePendingReevaluateJobTx is the only statement this repair writes to
// eligibility_projection_jobs, and the only thing about the account it
// changes at all: queued, due now, lease cleared, and the window above --
// raised onto an existing row, never lowered, exactly as both
// finalizeSourceAccountsTx and the shadow harness do it. It returns the
// requested_through the row ended up with, which is the ground truth the
// report's own prediction is checked against.
//
// The DO UPDATE deliberately refuses a status='dead' row.
// XM-INV-PROJECTION-FAILURE-GRADING's terminal grade exists so a persistently
// failing account stops being retried until an operator investigates, and
// reviving one from here would route around --kind=projection-requeue-dead,
// the tool that owns that decision. The report's own job check refuses the
// apply before this is ever reached, so in the ordinary flow this clause
// never decides anything -- it is here so that the refusal is a property of
// the write and not only of the check that precedes it, and it has its own
// test for exactly that reason.
func requeuePendingReevaluateJobTx(ctx context.Context, tx pgx.Tx, accountID string) (bool, time.Time, error) {
	var requested time.Time
	err := tx.QueryRow(ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		SELECT eas.external_account_id,`+pendingReevaluateWindowSQL+`,'queued',now()
		FROM source_account_eligibility_state eas
		WHERE eas.external_account_id=$1
		ON CONFLICT(external_account_id) DO UPDATE SET
			requested_through=GREATEST(eligibility_projection_jobs.requested_through,EXCLUDED.requested_through),
			status='queued',lease_token=NULL,lease_expires_at=NULL,
			next_attempt_at=now(),updated_at=now()
		WHERE eligibility_projection_jobs.status<>'dead'
		RETURNING requested_through`, accountID).Scan(&requested)
	if errors.Is(err, pgx.ErrNoRows) {
		// The DO UPDATE's own WHERE refused the row (dead), or the account
		// has no state row: nothing was written.
		return false, time.Time{}, nil
	}
	if err != nil {
		return false, time.Time{}, err
	}
	return true, requested.UTC(), nil
}

// loadPendingReevaluateWindowTx predicts, without writing anything, exactly
// what requeuePendingReevaluateJobTx would leave in requested_through: the
// same window expression, raised by the same GREATEST against whatever the
// existing job row already asks for. Sharing the fragment is what keeps the
// dry run and the apply from drifting into two different answers -- the
// failure the first review caught, where the report said "applying now
// changes nothing" and the apply then took an existing row's wider window and
// returned an account to invoiceable.
func loadPendingReevaluateWindowTx(ctx context.Context, tx pgx.Tx, accountID string) (window time.Time, streams int, err error) {
	err = tx.QueryRow(ctx, `
		SELECT GREATEST(`+pendingReevaluateWindowSQL+`,
			(SELECT j.requested_through FROM eligibility_projection_jobs j
			  WHERE j.external_account_id=eas.external_account_id)),
			(SELECT count(*) FROM source_economic_stream_watermarks w
			  WHERE w.source_instance_id=eas.source_instance_id)
		FROM source_account_eligibility_state eas
		WHERE eas.external_account_id=$1`, accountID).Scan(&window, &streams)
	if err != nil {
		return time.Time{}, 0, err
	}
	return window.UTC(), streams, nil
}

// collectPendingReevaluateChecks fills in every reported field and the six
// design checks. It only reads.
func (s *Store) collectPendingReevaluateChecks(ctx context.Context, tx pgx.Tx,
	account eligibilityAccount, in PendingReevaluateRepairInput, result *PendingReevaluateRepairResult) error {
	accountID := account.ExternalAccountID
	add := func(code, name string, passed, blocker bool, format string, args ...any) {
		result.Checks = append(result.Checks, PendingReevaluateCheck{Code: code, Name: name, Passed: passed,
			Detail: fmt.Sprintf(format, args...), Blocker: blocker && !passed})
	}

	// Check 1: the account is actually in the state this tool addresses.
	var reason, triggerType, triggerID, detail *string
	var since *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT pending_reconciliation_reason,pending_reconciliation_trigger_type,
			pending_reconciliation_trigger_id,pending_reconciliation_detail,
			pending_reconciliation_since,pending_reconciliation_consecutive_matches
		FROM source_account_eligibility_state WHERE external_account_id=$1`, accountID).Scan(
		&reason, &triggerType, &triggerID, &detail, &since, &result.ConsecutiveMatches); err != nil {
		return err
	}
	if reason != nil {
		result.Reason = *reason
	}
	if triggerType != nil {
		result.TriggerType = *triggerType
	}
	if triggerID != nil {
		result.TriggerID = *triggerID
	}
	if since != nil {
		result.Since = since.UTC()
	}
	pending := account.Status == "not_invoiceable_pending_reconciliation"
	add("not_pending", "状态", pending, true,
		"当前 eligibility_status=%s；原因=%s 触发=%s/%s 起始=%s 连续匹配=%d/%d",
		account.Status, result.Reason, result.TriggerType, result.TriggerID,
		formatOptionalTime(result.Since), result.ConsecutiveMatches, pendingReconciliationExitMatches)

	// Check 2: an open freeze would block the exit even with fresh evidence,
	// and (this slice's own C2 guard) suppresses the derivation outright.
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes
		WHERE external_account_id=$1 AND status='open'`, accountID).Scan(&result.OpenFreezes); err != nil {
		return err
	}
	add("open_freeze", "冻结", result.OpenFreezes == 0, true,
		"open 冻结 %d 条；大于 0 时退出会被冻结守卫挡住，且闲置派生本身就不会发生",
		result.OpenFreezes)

	// Check 3: the job row. processing means a worker holds it right now;
	// dead is owned by --kind=projection-requeue-dead and is never revived
	// from here.
	err := tx.QueryRow(ctx, `SELECT status FROM eligibility_projection_jobs
		WHERE external_account_id=$1`, accountID).Scan(&result.JobStatus)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	switch result.JobStatus {
	case "":
		add("job_ok", "作业", true, true, "该账号当前没有投影作业行，可以排队")
	case "processing":
		add("job_processing", "作业", false, true, "投影作业正在 processing，worker 正在处理该账号；等它结束后再看")
	case "dead":
		add("job_dead", "作业", false, true,
			"投影作业已是 dead，本工具绝不复活它；先跑 --kind=projection-requeue-dead --account %s", accountID)
	default:
		add("job_ok", "作业", true, true, "投影作业当前状态=%s，重新排队即可", result.JobStatus)
	}

	// Check 4: evidence already waiting. The worker will get to it on its own
	// -- the same NOT EXISTS predicate evaluatePendingBalanceEvidenceTx
	// selects on, so this is the evaluator's own view, not an approximation.
	if err = tx.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM balance_reconciliation_checkpoints checkpoint
			WHERE checkpoint.external_account_id=$1 AND checkpoint.checkpoint_kind='reconciliation'
			  AND NOT EXISTS (SELECT 1 FROM balance_checkpoint_evaluations evaluation
				WHERE evaluation.checkpoint_id=checkpoint.id))
		+ (SELECT count(*) FROM balance_carry_forward_proofs proof
			WHERE proof.external_account_id=$1
			  AND NOT EXISTS (SELECT 1 FROM balance_carry_forward_evaluations evaluation
				WHERE evaluation.proof_id=proof.id))`, accountID).Scan(&result.UnevaluatedEvidence); err != nil {
		return err
	}
	add("unevaluated_evidence", "待评估证据", true, false,
		"尚未评估的检查点/证明 %d 条；大于 0 说明 worker 自己就会处理，通常不需要本工具",
		result.UnevaluatedEvidence)

	// Check 5a: the window an apply will actually ask for. Predicted from the
	// same SQL the requeue writes, so what the dry run says here is what the
	// apply does -- including the case that caught the first review, where an
	// existing job row's own wider window survives the upsert.
	window, streams, err := loadPendingReevaluateWindowTx(ctx, tx, accountID)
	if err != nil {
		return err
	}
	result.EffectiveRequestedThrough, result.WatermarkStreams = window, streams
	add("window_empty", "重评窗口", streams == 4 && window.After(result.FinalizedThrough), true,
		"apply 后作业行会带 requested_through=%s（finalized_through=%s，四条流水位齐了 %d/4）；派生只在这个窗口内发生",
		formatOptionalTime(result.EffectiveRequestedThrough),
		formatOptionalTime(result.FinalizedThrough), streams)

	// Check 5b: the cycle an idle derivation would take inside that window --
	// selected by ensureBalanceCarryForwardProofTx's own rule, newest first,
	// skipping any that already carries this account's real checkpoint.
	found, err := s.loadPendingReevaluateTarget(ctx, tx, account, result)
	if err != nil {
		return err
	}
	if err = s.loadPendingReevaluateCycleContext(ctx, tx, account, result); err != nil {
		return err
	}
	if !found {
		detail := fmt.Sprintf(
			"窗口 (%s, %s] 内没有可派生的已发布 balances 周期（或该账号还没有可复述的真实检查点）",
			formatOptionalTime(result.FinalizedThrough), formatOptionalTime(result.EffectiveRequestedThrough))
		if !result.NewestPublishedCycleAt.IsZero() && result.NewestPublishedCycleAt.After(result.EffectiveRequestedThrough) {
			detail += fmt.Sprintf("；该源最新已发布周期在 %s，还压在 finalization_delay 里，等下一次 finalize 把窗口推上去即可",
				formatOptionalTime(result.NewestPublishedCycleAt))
		}
		if result.SkippedRealCheckpointCycles > 0 {
			detail += fmt.Sprintf("；窗口内另有 %d 个周期已带该账号的真实检查点，按派生规则跳过",
				result.SkippedRealCheckpointCycles)
		}
		add("no_derivable_cycle", "目标周期", false, true, "%s", detail)
		return s.addSelfDealingCheck(ctx, tx, account, in, result)
	}
	switch {
	case result.TargetHasProof:
		add("cycle_has_proof", "目标周期", false, true,
			"窗口内可派生的最新周期 %s（天花板 %s）已有该账号的结转证明，重评不会产生新证据",
			result.TargetCycleID, formatOptionalTime(result.TargetCycleAt))
	case result.TargetHasStranded:
		add("cycle_has_stranded", "目标周期", false, true,
			"窗口内可派生的最新周期 %s（天花板 %s）带着该账号被兜住的失败检查点，派生会被 XM-INV-DEAD-CONTAINMENT 的等待挡住；先处理那条死信",
			result.TargetCycleID, formatOptionalTime(result.TargetCycleAt))
	default:
		add("cycle_ok", "目标周期", true, true,
			"窗口内可派生的最新周期 %s（天花板 %s，跳过 %d 个已带真实检查点的周期）可派生，复述的检查点 %s（as_of %s）",
			result.TargetCycleID, formatOptionalTime(result.TargetCycleAt),
			result.SkippedRealCheckpointCycles,
			result.PriorCheckpointID, formatOptionalTime(result.PriorAsOf))
	}
	if result.PriorNegative && result.PriorDeficit == "" {
		add("prior_unknown_magnitude", "复述量级", false, true,
			"要复述的检查点 %s 报负余额但没有量级（deficit 为空），重评只能得到 negative_frozen(unknown)",
			result.PriorCheckpointID)
	} else {
		add("prior_magnitude_ok", "复述量级", true, true, "要复述的检查点 %s 余额=%s 负标志=%t deficit=%s",
			result.PriorCheckpointID, result.PriorBalance, result.PriorNegative, orDash(result.PriorDeficit))
	}

	// Check 6: recompute here, from the ledger, what the evaluator would say
	// about that evidence. Deliberately not read from any stored evaluation
	// row -- the point of the tool is to tell the operator what the next run
	// will decide, not what an old one decided.
	if err = s.recomputePendingReevaluateOutcome(ctx, tx, account, result); err != nil {
		return err
	}
	add("recomputed", "预期评估", result.RecomputedStatus == "matched", false,
		"按当前账本重算：期望=%s 差值=%s 预期结果=%s",
		orDash(result.RecomputedExpected), orDash(result.RecomputedDifference), result.RecomputedStatus)

	return s.addSelfDealingCheck(ctx, tx, account, in, result)
}

// addSelfDealingCheck is design check 7, reusing ResolveEligibilityFreeze's
// own rule: an operator may not act on their own account. Only meaningful
// when an operator id was supplied, so a dry run without one reports it as
// not applicable rather than as a pass.
func (s *Store) addSelfDealingCheck(_ context.Context, _ pgx.Tx, account eligibilityAccount,
	in PendingReevaluateRepairInput, result *PendingReevaluateRepairResult) error {
	operator := strings.TrimSpace(in.OperatorID)
	if operator == "" {
		result.Checks = append(result.Checks, PendingReevaluateCheck{Code: "self_dealing", Name: "自利守卫", Passed: true,
			Detail: "未提供 --operator-id，apply 时才校验"})
		return nil
	}
	self := strings.EqualFold(operator, account.PrincipalID)
	result.Checks = append(result.Checks, PendingReevaluateCheck{Code: "self_dealing", Name: "自利守卫", Passed: !self,
		Blocker: self,
		Detail:  "操作者不得是该账号所属的开票用户"})
	return nil
}

// loadPendingReevaluateTarget finds the cycle an idle derivation would
// actually take: inside the window an apply will ask for, and -- like
// ensureBalanceCarryForwardProofTx's own backwards walk -- the newest one
// that does not already carry a real checkpoint for this account. The
// prior-checkpoint LATERAL and the stranded-checkpoint predicate are the same
// text the derivation uses (the latter rendered from the same
// sourceEventContainedByOpenFreezeSQL), so the report cannot drift from what
// the derivation will do.
//
// The window bound is EffectiveRequestedThrough, not finalized_through. Under
// the old bound the report described a cycle the apply could never reach, and
// on production shapes -- where finalized_through is min(watermarks) minus a
// delay in seconds and cycles publish about once a minute -- that was every
// cycle.
func (s *Store) loadPendingReevaluateTarget(ctx context.Context, tx pgx.Tx,
	account eligibilityAccount, result *PendingReevaluateRepairResult) (bool, error) {
	var deficit *string
	err := tx.QueryRow(ctx, `
		SELECT cycle.scan_cycle_id::text,cycle.scan_ceiling_at,
			prior.id::text,prior.as_of,prior.balance_service_units::text,
			prior.balance_negative,prior.deficit_service_units::text,
			EXISTS (
				SELECT 1 FROM balance_carry_forward_proofs proof
				WHERE proof.external_account_id=$1 AND proof.scan_cycle_id=cycle.scan_cycle_id
			),
			EXISTS (
				SELECT 1
				FROM source_economic_scan_cycle_events mapped
				JOIN source_ingest_events sie
				  ON sie.source_instance_id=mapped.source_instance_id
				 AND sie.stream_id=mapped.stream_id
				 AND sie.event_id=mapped.event_id
				WHERE mapped.source_instance_id=cycle.source_instance_id
				  AND mapped.stream_id='balances'
				  AND mapped.scan_cycle_id=cycle.scan_cycle_id
				  AND sie.entity_type='balance_checkpoint'
				  AND sie.processing_status IN ('failed','dead')
				  AND (`+sourceEventContainedByOpenFreezeSQL("sie", "ef.external_account_id=$1")+`
				       OR NOT `+sourceEventContainedByOpenFreezeSQL("sie")+`)
			)
		FROM source_economic_scan_cycles cycle
		JOIN LATERAL (
			SELECT checkpoint.id,checkpoint.as_of,checkpoint.balance_service_units,
				checkpoint.balance_negative,checkpoint.deficit_service_units
			FROM balance_reconciliation_checkpoints checkpoint
			WHERE checkpoint.external_account_id=$1
			  AND (checkpoint.as_of<cycle.scan_ceiling_at
			       OR (checkpoint.as_of=cycle.scan_ceiling_at
			           AND checkpoint.source_sequence<cycle.final_sequence))
			ORDER BY checkpoint.as_of DESC,checkpoint.source_sequence DESC,checkpoint.id DESC LIMIT 1
		) prior ON true
		WHERE cycle.source_instance_id=$2 AND cycle.stream_id='balances'
		  AND cycle.cycle_status='published'
		  AND cycle.scan_ceiling_at>=$3 AND cycle.scan_ceiling_at<=$4
		  AND NOT EXISTS (
			SELECT 1 FROM balance_reconciliation_checkpoints checkpoint
			JOIN source_economic_scan_cycle_events mapped
			  ON mapped.source_instance_id=checkpoint.source_instance_id
			 AND mapped.stream_id='balances'
			 AND mapped.event_id=CASE WHEN checkpoint.external_event_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN checkpoint.external_event_id::uuid END
			 AND mapped.payload_hash=checkpoint.source_revision_hash
			WHERE checkpoint.external_account_id=$1 AND mapped.scan_cycle_id=cycle.scan_cycle_id
		  )
		ORDER BY cycle.scan_ceiling_at DESC,cycle.first_sequence DESC
		LIMIT 1`, account.ExternalAccountID, account.SourceInstanceID,
		account.FinalizedThrough, result.EffectiveRequestedThrough).Scan(
		&result.TargetCycleID, &result.TargetCycleAt, &result.PriorCheckpointID, &result.PriorAsOf,
		&result.PriorBalance, &result.PriorNegative, &deficit,
		&result.TargetHasProof, &result.TargetHasStranded)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	result.TargetCycleAt = result.TargetCycleAt.UTC()
	result.PriorAsOf = result.PriorAsOf.UTC()
	if deficit != nil {
		result.PriorDeficit = *deficit
	}
	return true, nil
}

// loadPendingReevaluateCycleContext fills in the two numbers that explain a
// target the operator might otherwise find surprising: how many cycles inside
// the window were passed over for already carrying this account's own real
// checkpoint, and where the newest published cycle for the source sits. The
// second is what distinguishes "this source has stopped publishing" from the
// ordinary "the newest cycles are still inside the finalization delay".
func (s *Store) loadPendingReevaluateCycleContext(ctx context.Context, tx pgx.Tx,
	account eligibilityAccount, result *PendingReevaluateRepairResult) error {
	var newest *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM source_economic_scan_cycles cycle
			WHERE cycle.source_instance_id=$2 AND cycle.stream_id='balances'
			  AND cycle.cycle_status='published'
			  AND cycle.scan_ceiling_at>=$3 AND cycle.scan_ceiling_at<=$4
			  AND EXISTS (
				SELECT 1 FROM balance_reconciliation_checkpoints checkpoint
				JOIN source_economic_scan_cycle_events mapped
				  ON mapped.source_instance_id=checkpoint.source_instance_id
				 AND mapped.stream_id='balances'
				 AND mapped.event_id=CASE WHEN checkpoint.external_event_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN checkpoint.external_event_id::uuid END
				 AND mapped.payload_hash=checkpoint.source_revision_hash
				WHERE checkpoint.external_account_id=$1 AND mapped.scan_cycle_id=cycle.scan_cycle_id)),
			(SELECT max(cycle.scan_ceiling_at) FROM source_economic_scan_cycles cycle
			WHERE cycle.source_instance_id=$2 AND cycle.stream_id='balances'
			  AND cycle.cycle_status='published')`,
		account.ExternalAccountID, account.SourceInstanceID,
		account.FinalizedThrough, result.EffectiveRequestedThrough).Scan(
		&result.SkippedRealCheckpointCycles, &newest); err != nil {
		return err
	}
	if newest != nil {
		result.NewestPublishedCycleAt = newest.UTC()
	}
	return nil
}

// recomputePendingReevaluateOutcome runs this run's own arithmetic for the
// evidence a derivation would produce: the projection at the proof's own
// as_of (the cycle ceiling, which is where the evaluator would build it),
// compared through signedExpectedUnits -- the same helper the evaluator uses,
// so the two cannot answer differently.
func (s *Store) recomputePendingReevaluateOutcome(ctx context.Context, tx pgx.Tx,
	account eligibilityAccount, result *PendingReevaluateRepairResult) error {
	projection, err := buildEligibilityProjectionTx(ctx, tx, account, result.TargetCycleAt)
	if err != nil {
		return err
	}
	expectedSigned := signedExpectedUnits(projection)
	result.RecomputedExpected = expectedSigned.String()
	if result.PriorNegative {
		if result.PriorDeficit == "" {
			result.RecomputedStatus = "negative_frozen(unknown)"
			return nil
		}
		deficit, parseErr := parseUnsignedUnits(result.PriorDeficit, "balance deficit service units", true)
		if parseErr != nil {
			return parseErr
		}
		difference := new(big.Int).Sub(new(big.Int).Neg(deficit), expectedSigned)
		result.RecomputedDifference = difference.String()
		result.RecomputedStatus = "matched"
		if difference.Sign() != 0 {
			result.RecomputedStatus = "negative_frozen"
		}
		return nil
	}
	balance, parseErr := parseUnsignedUnits(result.PriorBalance, "carry-forward balance", true)
	if parseErr != nil {
		return parseErr
	}
	difference := new(big.Int).Sub(balance, expectedSigned)
	result.RecomputedDifference = difference.String()
	switch difference.Sign() {
	case 0:
		result.RecomputedStatus = "matched"
	case -1:
		result.RecomputedStatus = "negative_frozen"
	default:
		// A positive difference goes through the boundary rule and the
		// XM-INV-BALANCE-BLIP deferral, whose outcome depends on the item
		// after it. Saying "positive" is the honest answer; claiming a
		// specific status here would be guessing.
		result.RecomputedStatus = "positive(待防抖判定)"
	}
	return nil
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
