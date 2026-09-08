package postgresstore

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// QueueNarrowRepairInput drives RepairQueueNarrowEligibility (design
// XM-INV-ELIG-SIMPLIFY section 3(C) item 3): a versioned, human-approved
// lifecycle operation that migrates every still-open eligibility_freezes row
// that XM-INV-ELIG-AUTO-RECONCILE (slice 1) and this slice's own
// observeEligibilityFact change made obsolete -- UNKNOWN_NEGATIVE_BALANCE,
// USAGE_EXCEEDS_LEDGER, and a funding_lot-less LATE_FINALIZED_EVENT -- into
// the new self-clearing state (or simply resolves it, for the
// LATE_FINALIZED_EVENT case, which has no new recording path of its own).
type QueueNarrowRepairInput struct {
	// Apply, when false (the default), reports what each affected account's
	// own transaction would do without committing anything (every per-account
	// transaction is rolled back). Apply commits one transaction per account.
	Apply bool
	// OperatorID is required when Apply is true: the resolved_by UUID
	// recorded on every freeze this run resolves.
	OperatorID string
	// NoteCiphertext/NoteHash and EvidenceCiphertext/EvidenceHash are the
	// already-encrypted, fixed resolution note ("由 XM-INV-ELIG-SIMPLIFY
	// 迁移自动解除") this run writes on every freeze it resolves --
	// encryption happens in the caller, mirroring the sibling repairs'
	// identical split. Required when Apply is true; ignored for a dry run.
	NoteCiphertext     []byte
	NoteHash           string
	EvidenceCiphertext []byte
	EvidenceHash       string
}

// QueueNarrowRepairAccount is one row of the repair's per-account summary
// table.
type QueueNarrowRepairAccount struct {
	ExternalAccountID                 string
	SourceInstanceID                  string
	NegativeBalanceFreezesResolved    int
	UsageExceedsLedgerFreezesResolved int
	LateFactFreezesResolved           int
	// RebuiltPendingReconciliation is true when this account's negative
	// balance freeze(s) were resolved and the account's latest real balance
	// evaluation still shows a negative/unreconciled difference: the account
	// is put into not_invoiceable_pending_reconciliation (slice 1's
	// self-clearing state) rather than simply reported "resolved" while
	// still genuinely negative. False (including when there was no
	// UNKNOWN_NEGATIVE_BALANCE freeze to resolve at all) means either there
	// was nothing to rebuild, or the account's latest evidence already
	// reconciles.
	RebuiltPendingReconciliation bool
	// UsageOverageReprojected is true when this account had a
	// USAGE_EXCEEDS_LEDGER freeze resolved and was reprojected (apply) or
	// would be reprojected (dry run) so recordUsageOverageTx can set or
	// clear non_invoiceable_overage_* from the current projection.
	UsageOverageReprojected bool
	// Reactivated is true when, after every resolution and rebuild above,
	// no open eligibility_freezes row remained for the account and it
	// returned from 'frozen' to 'active' (never forced -- a no-op if the
	// account is not 'frozen', including because a rebuild above just put
	// it into not_invoiceable_pending_reconciliation, or because reprojection
	// itself opened a new, different freeze).
	Reactivated bool
}

// QueueNarrowRepairAccountError records one account whose own repair
// transaction failed -- collected, never allowed to abort the run for any
// other account (a production regression in a prior evaluator change taught
// this codebase that a batch operation must isolate failures per account;
// see docs/handoffs/XM-INV-ELIG-QUEUE-NARROW.md).
type QueueNarrowRepairAccountError struct {
	ExternalAccountID string
	Message           string
}

// QueueNarrowRepairResult is the repair's full summary, printable as-is by
// the CLI in both dry-run and apply modes.
type QueueNarrowRepairResult struct {
	Applied                                bool
	Accounts                               []QueueNarrowRepairAccount
	Errors                                 []QueueNarrowRepairAccountError
	TotalNegativeBalanceFreezesResolved    int
	TotalUsageExceedsLedgerFreezesResolved int
	TotalLateFactFreezesResolved           int
}

// queueNarrowTargetPredicate is the SQL fragment (against an
// eligibility_freezes-aliased "ef") selecting exactly design section
// 3(C) item 3's three migration categories: UNKNOWN_NEGATIVE_BALANCE and
// USAGE_EXCEEDS_LEDGER unconditionally (their new recording paths, slice 1
// and this slice's own (B) rebuild below, fully replace what these freezes
// used to guard), and LATE_FINALIZED_EVENT only when funding_lot_id IS NULL
// (the generalized, no-longer-opened freeze this slice's own
// observeEligibilityFact change removed -- a funding_lot-scoped
// LATE_FINALIZED_EVENT freeze is the real, still-manual red-reversal case
// and must never be touched by this tool).
const queueNarrowTargetPredicate = `
	(ef.freeze_reason IN ('UNKNOWN_NEGATIVE_BALANCE','USAGE_EXCEEDS_LEDGER')
		OR (ef.freeze_reason='LATE_FINALIZED_EVENT' AND ef.funding_lot_id IS NULL))`

// RepairQueueNarrowEligibility implements design XM-INV-ELIG-SIMPLIFY section
// 3(C) item 3's production migration. Unlike the sibling repair tools (one
// SERIALIZABLE transaction spanning the whole run), this repair processes
// one account per transaction: a single misbehaving account (inconsistent
// data, a concurrent conflict, an unexpected evidence shape) must never
// abort the run for every other account waiting in the same batch -- see
// QueueNarrowRepairAccountError's own doc comment. Apply commits each
// account's transaction independently; dry run rolls every one of them back
// unconditionally after computing the same preview apply would report.
func (s *Store) RepairQueueNarrowEligibility(ctx context.Context, in QueueNarrowRepairInput, actor AuditActor) (QueueNarrowRepairResult, error) {
	if in.Apply {
		if !eligibilityUUIDPattern.MatchString(strings.TrimSpace(in.OperatorID)) {
			return QueueNarrowRepairResult{}, errors.New("a valid operator UUID is required to apply")
		}
		if !hexHashPattern.MatchString(in.NoteHash) || !hexHashPattern.MatchString(in.EvidenceHash) ||
			len(in.NoteCiphertext) < 16 || len(in.NoteCiphertext) > 8192 ||
			len(in.EvidenceCiphertext) < 16 || len(in.EvidenceCiphertext) > 8192 {
			return QueueNarrowRepairResult{}, errors.New("encrypted resolution note and evidence are required to apply")
		}
	}

	accountIDs, err := s.queueNarrowCandidateAccountIDs(ctx)
	if err != nil {
		return QueueNarrowRepairResult{}, err
	}

	result := QueueNarrowRepairResult{Applied: in.Apply}
	for _, accountID := range accountIDs {
		account, repairErr := s.repairQueueNarrowAccount(ctx, accountID, in, actor)
		if repairErr != nil {
			result.Errors = append(result.Errors, QueueNarrowRepairAccountError{
				ExternalAccountID: accountID, Message: repairErr.Error()})
			continue
		}
		result.Accounts = append(result.Accounts, account)
		result.TotalNegativeBalanceFreezesResolved += account.NegativeBalanceFreezesResolved
		result.TotalUsageExceedsLedgerFreezesResolved += account.UsageExceedsLedgerFreezesResolved
		result.TotalLateFactFreezesResolved += account.LateFactFreezesResolved
	}
	sort.Slice(result.Accounts, func(i, j int) bool {
		return result.Accounts[i].ExternalAccountID < result.Accounts[j].ExternalAccountID
	})
	sort.Slice(result.Errors, func(i, j int) bool {
		return result.Errors[i].ExternalAccountID < result.Errors[j].ExternalAccountID
	})
	return result, nil
}

// queueNarrowCandidateAccountIDs is a plain, transaction-less snapshot read:
// it only decides which accounts are worth attempting. Each account's own
// repairQueueNarrowAccount call re-selects and locks (FOR UPDATE) its own
// rows from scratch, so a stale snapshot here (a row resolved concurrently
// between this read and that account's own transaction) is harmless -- that
// account's transaction simply finds zero matching rows and reports an
// empty, error-free result.
func (s *Store) queueNarrowCandidateAccountIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ef.external_account_id::text
		FROM eligibility_freezes ef
		WHERE ef.status='open' AND`+queueNarrowTargetPredicate+`
		ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

type queueNarrowFreezeRow struct {
	id, reason, triggerObjectType string
	resolutionVersion             int64
}

// repairQueueNarrowAccount is the whole per-account unit of work: its own
// SERIALIZABLE transaction, committed on success in apply mode and always
// rolled back in dry-run mode. Every branch below returns a defined,
// zero-error result for any data shape this account might legitimately be
// in (including zero matching rows, e.g. resolved concurrently since
// queueNarrowCandidateAccountIDs read its snapshot) -- only a genuine
// database/query error is ever returned, and the caller collects that as
// one QueueNarrowRepairAccountError without touching any other account.
func (s *Store) repairQueueNarrowAccount(ctx context.Context, accountID string, in QueueNarrowRepairInput, actor AuditActor) (QueueNarrowRepairAccount, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return QueueNarrowRepairAccount{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	// Same per-account advisory lock processEligibilityProjectionJob takes
	// before reprojecting/evaluating this account (hashtextextended(...,43)):
	// this repair calls the identical reprojectEligibilityTx/
	// enterPendingReconciliationTx machinery outside the normal job queue,
	// so it must serialize against a concurrent real projection job or
	// Observe*/evaluatePendingBalanceEvidenceTx call the same way.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,43))`, accountID); err != nil {
		return QueueNarrowRepairAccount{}, err
	}

	var sourceInstanceID string
	if err = tx.QueryRow(ctx, `SELECT source_instance_id::text FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&sourceInstanceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return QueueNarrowRepairAccount{}, nil
		}
		return QueueNarrowRepairAccount{}, err
	}
	row := QueueNarrowRepairAccount{ExternalAccountID: accountID, SourceInstanceID: sourceInstanceID}

	rows, err := tx.Query(ctx, `
		SELECT ef.id,ef.resolution_version,ef.freeze_reason,ef.trigger_object_type
		FROM eligibility_freezes ef
		WHERE ef.external_account_id=$1 AND ef.status='open' AND`+queueNarrowTargetPredicate+`
		ORDER BY ef.id
		FOR UPDATE OF ef`, accountID)
	if err != nil {
		return QueueNarrowRepairAccount{}, err
	}
	freezes := make([]queueNarrowFreezeRow, 0)
	for rows.Next() {
		var f queueNarrowFreezeRow
		if err = rows.Scan(&f.id, &f.resolutionVersion, &f.reason, &f.triggerObjectType); err != nil {
			rows.Close()
			return QueueNarrowRepairAccount{}, err
		}
		freezes = append(freezes, f)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return QueueNarrowRepairAccount{}, err
	}
	rows.Close()

	if len(freezes) == 0 {
		// Nothing (any longer) matches this account -- a defined, empty,
		// error-free result, not a fault.
		if in.Apply {
			if err = tx.Commit(ctx); err != nil {
				return QueueNarrowRepairAccount{}, err
			}
		}
		return row, nil
	}

	hasNegativeBalance, hasUsageExceeds := false, false
	for _, f := range freezes {
		switch f.reason {
		case "UNKNOWN_NEGATIVE_BALANCE":
			hasNegativeBalance = true
			row.NegativeBalanceFreezesResolved++
		case "USAGE_EXCEEDS_LEDGER":
			hasUsageExceeds = true
			row.UsageExceedsLedgerFreezesResolved++
		case "LATE_FINALIZED_EVENT":
			row.LateFactFreezesResolved++
		}
		if in.Apply {
			if err = applyQueueNarrowFreezeResolution(ctx, tx, f, sourceInstanceID, in, actor); err != nil {
				return QueueNarrowRepairAccount{}, err
			}
		}
	}

	if !in.Apply {
		// Dry run: report what apply would do without calling any of the
		// mutating rebuild helpers below (which would themselves write
		// nothing durable inside a transaction this function always rolls
		// back, but reprojectEligibilityTx/enterPendingReconciliationTx are
		// the real production code paths and are exercised, committed, by
		// the apply-mode tests instead -- the preview here only inspects
		// already-recorded evidence).
		if hasNegativeBalance {
			eval, evalErr := latestBalanceEvaluationTx(ctx, tx, accountID)
			if evalErr != nil {
				return QueueNarrowRepairAccount{}, evalErr
			}
			row.RebuiltPendingReconciliation = eval.found && eval.status == "negative_frozen"
		}
		row.UsageOverageReprojected = hasUsageExceeds
		var remainingOthers int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes ef
			WHERE ef.external_account_id=$1 AND ef.status='open' AND NOT(`+queueNarrowTargetPredicate+`)`,
			accountID).Scan(&remainingOthers); err != nil {
			return QueueNarrowRepairAccount{}, err
		}
		row.Reactivated = remainingOthers == 0 && !row.RebuiltPendingReconciliation
		return row, nil
	}

	// Apply: trigger (A)/(B)'s own recording paths from current data, per
	// design section 3(C) item 3 -- rebuild, not just clear, so an account
	// that is still genuinely negative or still genuinely over-ledger is
	// never misreported "resolved". Order matters here:
	//   1. reproject first (it may itself open a brand-new, different
	//      freeze -- e.g. AMBIGUOUS_EVENT_ORDER, or a genuine funding_lot
	//      red-reversal -- which the "no remaining open freeze" check right
	//      after must see);
	//   2. only then decide whether no open freeze remains at all and, if
	//      so, actually flip eligibility_status from 'frozen' to 'active' --
	//      enterPendingReconciliationTx's own frozen-priority guard
	//      (eligibility_status<>'frozen') would otherwise silently no-op
	//      for an account this repair just finished resolving, since
	//      resolving a freeze row does not by itself touch
	//      eligibility_status;
	//   3. only after that does the negative-balance rebuild run, so it
	//      sees the account's *current* status, not its stale pre-repair
	//      'frozen' value.
	// Reactivated is decided once, at the very end, from the account's
	// actual final status -- not from whether the reactivation UPDATE in
	// step 2 happened to affect a row, since step 3 can immediately reverse
	// it (briefly 'active', then downgraded to pending reconciliation) when
	// the account turns out to still be genuinely negative.
	if hasUsageExceeds {
		account, acctErr := getEligibilityAccountTx(ctx, tx, accountID, true)
		if acctErr != nil {
			return QueueNarrowRepairAccount{}, acctErr
		}
		if err = reprojectEligibilityTx(ctx, tx, accountID, account.FinalizedThrough, actor); err != nil {
			return QueueNarrowRepairAccount{}, err
		}
		row.UsageOverageReprojected = true
	}

	var remaining int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes
		WHERE external_account_id=$1 AND status='open'`, accountID).Scan(&remaining); err != nil {
		return QueueNarrowRepairAccount{}, err
	}
	if remaining == 0 {
		if _, err = tx.Exec(ctx, `
			INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
			SELECT external_account_id,finalized_through,'queued',now() FROM source_account_eligibility_state
			WHERE external_account_id=$1
			ON CONFLICT(external_account_id) DO UPDATE SET status='queued',lease_token=NULL,
				lease_expires_at=NULL,next_attempt_at=now(),updated_at=now()`, accountID); err != nil {
			return QueueNarrowRepairAccount{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE source_account_eligibility_state
			SET eligibility_status='active',projection_version=projection_version+1,updated_at=now()
			WHERE external_account_id=$1 AND eligibility_status='frozen'`, accountID); err != nil {
			return QueueNarrowRepairAccount{}, err
		}
	}

	if hasNegativeBalance {
		eval, evalErr := latestBalanceEvaluationTx(ctx, tx, accountID)
		if evalErr != nil {
			return QueueNarrowRepairAccount{}, evalErr
		}
		if eval.found && eval.status == "negative_frozen" {
			detail := fmt.Sprintf(
				"%s %s at %s reported balance difference %s against expected %s (rebuilt by XM-INV-ELIG-QUEUE-NARROW repair)",
				eval.kind, eval.key, eval.asOf.UTC().Format(time.RFC3339Nano), eval.differenceUnits, eval.expectedUnits)
			if err = enterPendingReconciliationTx(ctx, tx, accountID, "UNKNOWN_NEGATIVE_BALANCE",
				eval.kind, eval.key, detail, actor); err != nil {
				return QueueNarrowRepairAccount{}, err
			}
		}
	}

	var finalStatus string
	if err = tx.QueryRow(ctx, `SELECT eligibility_status FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&finalStatus); err != nil {
		return QueueNarrowRepairAccount{}, err
	}
	row.RebuiltPendingReconciliation = finalStatus == "not_invoiceable_pending_reconciliation"
	row.Reactivated = finalStatus == "active" && remaining == 0

	if err = writeAudit(ctx, tx, actor, "eligibility.queue_narrow.repaired", "external_account", accountID, nil, map[string]any{
		"negative_balance_freezes_resolved":     row.NegativeBalanceFreezesResolved,
		"usage_exceeds_ledger_freezes_resolved": row.UsageExceedsLedgerFreezesResolved,
		"late_fact_freezes_resolved":            row.LateFactFreezesResolved,
		"rebuilt_pending_reconciliation":        row.RebuiltPendingReconciliation,
		"usage_overage_reprojected":             row.UsageOverageReprojected,
		"reactivated":                           row.Reactivated,
		"repair_tool":                           "XM-INV-ELIG-QUEUE-NARROW",
	}); err != nil {
		return QueueNarrowRepairAccount{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return QueueNarrowRepairAccount{}, err
	}
	return row, nil
}

// applyQueueNarrowFreezeResolution resolves one freeze with the exact
// column shape the manual admin resolution path (ResolveEligibilityFreeze)
// writes, mirroring the sibling repairs' own pattern exactly (including
// re-asserting the freeze's full original predicate -- status='open' and
// the same category predicate -- in the UPDATE's own WHERE clause, aborting
// this account's transaction on any mismatch rather than silently skipping
// it).
//
// XM-INV-DEAD-CONTAINMENT: this is the broadest door of the four -- one run
// resolves every open UNKNOWN_NEGATIVE_BALANCE / USAGE_EXCEEDS_LEDGER /
// LATE_FINALIZED_EVENT freeze on every candidate account -- so it asks
// assertNoBlockingDeadEventForFreezeTx like the others. freezeEligibilityTx
// writes source_revision_hash for all three of those reasons, and the
// containment predicate is deliberately reason-blind, so any of them can be
// the freeze holding a dead event contained. The refusal aborts this
// account's transaction (the run's per-account error contract), leaving every
// other account repairable.
func applyQueueNarrowFreezeResolution(ctx context.Context, tx pgx.Tx, freeze queueNarrowFreezeRow,
	sourceInstanceID string, in QueueNarrowRepairInput, actor AuditActor) error {
	if err := assertNoBlockingDeadEventForFreezeTx(ctx, tx, freeze.id, sourceInstanceID); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `
		UPDATE eligibility_freezes ef SET status='resolved',resolved_at=now(),resolved_by=$1::uuid,
			resolution_evidence_hash=$2,resolution_evidence_ciphertext=$3,
			resolution_note_hash=$4,resolution_note_ciphertext=$5,
			resolution_version=resolution_version+1,updated_at=now()
		WHERE ef.id=$6 AND ef.status='open' AND`+queueNarrowTargetPredicate,
		in.OperatorID, in.EvidenceHash, in.EvidenceCiphertext, in.NoteHash, in.NoteCiphertext, freeze.id)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errors.New("selected freeze no longer matches the repair predicate; refusing to apply")
	}
	return writeAudit(ctx, tx, actor, "eligibility.freeze.resolved", "eligibility_freeze", freeze.id,
		map[string]any{"status": "open", "resolution_version": freeze.resolutionVersion,
			"freeze_reason": freeze.reason, "trigger_object_type": freeze.triggerObjectType},
		map[string]any{"status": "resolved", "resolution_version": freeze.resolutionVersion + 1,
			"freeze_reason": freeze.reason, "trigger_object_type": freeze.triggerObjectType,
			"resolution_note": "由 XM-INV-ELIG-SIMPLIFY 迁移自动解除",
			"repair_tool":     "XM-INV-ELIG-QUEUE-NARROW"})
}

// latestBalanceEvaluation is the most recent (by as_of, then source_sequence,
// then id) already-evaluated balance_checkpoint_evaluations/
// balance_carry_forward_evaluations row on record for an account, among
// checkpoints/proofs at or before its own finalized_through -- the same
// "latest finalized evaluation" ResolveEligibilityFreeze itself reads to
// judge whether it is safe to resolve a freeze. found is false when the
// account has no evaluated balance evidence at all yet (never treated as
// "still negative": no evidence of an unreconciled difference is not
// evidence of one).
type latestBalanceEvaluation struct {
	found                          bool
	kind, key                      string
	asOf                           time.Time
	status                         string
	expectedUnits, differenceUnits string
}

func latestBalanceEvaluationTx(ctx context.Context, tx pgx.Tx, accountID string) (latestBalanceEvaluation, error) {
	var e latestBalanceEvaluation
	err := tx.QueryRow(ctx, `
		SELECT latest.kind,latest.key,latest.as_of,latest.evaluation_status,
			COALESCE(latest.expected_service_units::text,''),COALESCE(latest.difference_service_units::text,'')
		FROM (
			SELECT 'balance_checkpoint'::text AS kind,checkpoint.checkpoint_id AS key,checkpoint.as_of,
				checkpoint.source_sequence,checkpoint.id,
				(SELECT evaluation.evaluation_status FROM balance_checkpoint_evaluations evaluation
				 WHERE evaluation.checkpoint_id=checkpoint.id
				 ORDER BY evaluation.projection_version DESC LIMIT 1) AS evaluation_status,
				(SELECT evaluation.expected_service_units FROM balance_checkpoint_evaluations evaluation
				 WHERE evaluation.checkpoint_id=checkpoint.id
				 ORDER BY evaluation.projection_version DESC LIMIT 1) AS expected_service_units,
				(SELECT evaluation.difference_service_units FROM balance_checkpoint_evaluations evaluation
				 WHERE evaluation.checkpoint_id=checkpoint.id
				 ORDER BY evaluation.projection_version DESC LIMIT 1) AS difference_service_units
			FROM balance_reconciliation_checkpoints checkpoint
			JOIN source_account_eligibility_state state ON state.external_account_id=checkpoint.external_account_id
			WHERE checkpoint.external_account_id=$1 AND checkpoint.checkpoint_kind='reconciliation'
				AND checkpoint.as_of<=state.finalized_through
			UNION ALL
			SELECT 'balance_carry_forward_proof',proof.proof_key,proof.as_of,proof.source_sequence,proof.id,
				(SELECT evaluation.evaluation_status FROM balance_carry_forward_evaluations evaluation
				 WHERE evaluation.proof_id=proof.id
				 ORDER BY evaluation.projection_version DESC LIMIT 1),
				(SELECT evaluation.expected_service_units FROM balance_carry_forward_evaluations evaluation
				 WHERE evaluation.proof_id=proof.id
				 ORDER BY evaluation.projection_version DESC LIMIT 1),
				(SELECT evaluation.difference_service_units FROM balance_carry_forward_evaluations evaluation
				 WHERE evaluation.proof_id=proof.id
				 ORDER BY evaluation.projection_version DESC LIMIT 1)
			FROM balance_carry_forward_proofs proof
			JOIN source_account_eligibility_state state ON state.external_account_id=proof.external_account_id
			WHERE proof.external_account_id=$1 AND proof.as_of<=state.finalized_through
		) latest
		WHERE latest.evaluation_status IS NOT NULL
		ORDER BY latest.as_of DESC,latest.source_sequence DESC,latest.id DESC LIMIT 1`, accountID).
		Scan(&e.kind, &e.key, &e.asOf, &e.status, &e.expectedUnits, &e.differenceUnits)
	if errors.Is(err, pgx.ErrNoRows) {
		return latestBalanceEvaluation{}, nil
	}
	if err != nil {
		return latestBalanceEvaluation{}, err
	}
	e.found = true
	return e, nil
}
