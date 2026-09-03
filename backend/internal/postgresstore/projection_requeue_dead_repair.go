package postgresstore

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ProjectionRequeueDeadRepairInput drives RepairProjectionRequeueDead
// (XM-INV-PROJECTION-FAILURE-GRADING): a versioned, human-approved lifecycle
// operation that resets every eligibility_projection_jobs row the failure
// grading escalated to the terminal status='dead' back to status='queued'
// with attempts=0, so the eligibility-projection worker picks it up again on
// its next tick. Unlike the freeze-resolution repairs (queue-narrow,
// balance-anchor, balance-blip), this one never touches eligibility_freezes
// or writes an encrypted resolution note/evidence -- there is nothing to
// resolve, only a job to requeue -- so its input is deliberately smaller,
// matching PolicyStartReanchorRepairInput's own precedent for a repair with
// no freeze interaction.
type ProjectionRequeueDeadRepairInput struct {
	// Apply, when false (the default), reports what each affected account's
	// own transaction would do without committing anything (every per-account
	// transaction is rolled back). Apply commits one transaction per account.
	Apply bool
	// OperatorID is required when Apply is true: the actor recorded on the
	// audit event this run writes for every account it requeues.
	OperatorID string
	// AccountID, when non-empty, narrows the run to exactly this external
	// account id instead of every dead job. Ignored when empty.
	AccountID string
}

// ProjectionRequeueDeadRepairAccount is one row of the repair's per-account
// summary table.
type ProjectionRequeueDeadRepairAccount struct {
	ExternalAccountID string
	// PreviousAttempts/LastErrorCode/LastError/DeadSince describe the dead
	// job as found, before any reset -- populated in both dry-run and apply
	// mode so an operator can see what they are about to requeue (or, in dry
	// run, what they would requeue) without a separate query.
	PreviousAttempts int64
	LastErrorCode    string
	LastError        string
	DeadSince        time.Time
	// Requeued is true once this account's job was (apply) or would be (dry
	// run) reset to status='queued'/attempts=0/next_attempt_at=now.
	Requeued bool
}

// ProjectionRequeueDeadRepairAccountError records one account whose own
// repair transaction failed -- collected, never allowed to abort the run for
// any other account, the same per-account isolation every sibling repair in
// this package uses (see QueueNarrowRepairAccountError's own doc comment for
// the production incident that established this pattern).
type ProjectionRequeueDeadRepairAccountError struct {
	ExternalAccountID string
	Message           string
}

// ProjectionRequeueDeadRepairResult is the repair's full summary, printable
// as-is by the CLI in both dry-run and apply modes.
type ProjectionRequeueDeadRepairResult struct {
	Applied       bool
	Accounts      []ProjectionRequeueDeadRepairAccount
	Errors        []ProjectionRequeueDeadRepairAccountError
	TotalRequeued int
}

// RepairProjectionRequeueDead lists every eligibility_projection_jobs row
// currently status='dead' (optionally narrowed to one account) and, in apply
// mode, resets each one to status='queued', attempts=0, next_attempt_at=now
// -- the only way a dead job returns to the worker's queue, deliberately:
// see the guard this slice added to the two ON CONFLICT upserts in
// finalizeSourceAccountsTx/ObserveBalanceCheckpoint, which now refuse to
// silently revive a dead row on their own. Mirrors QueueNarrowRepairEligibility's
// per-account transaction isolation: one account's own conflict or data
// inconsistency is reported as a per-account error and never blocks any
// other account in the same run.
func (s *Store) RepairProjectionRequeueDead(ctx context.Context, in ProjectionRequeueDeadRepairInput, actor AuditActor) (ProjectionRequeueDeadRepairResult, error) {
	if in.Apply && !eligibilityUUIDPattern.MatchString(strings.TrimSpace(in.OperatorID)) {
		return ProjectionRequeueDeadRepairResult{}, errors.New("a valid operator UUID is required to apply")
	}
	accountIDs, err := s.projectionRequeueDeadCandidateAccountIDs(ctx, in.AccountID)
	if err != nil {
		return ProjectionRequeueDeadRepairResult{}, err
	}

	result := ProjectionRequeueDeadRepairResult{Applied: in.Apply}
	for _, accountID := range accountIDs {
		account, repairErr := s.repairProjectionRequeueDeadAccount(ctx, accountID, in, actor)
		if repairErr != nil {
			result.Errors = append(result.Errors, ProjectionRequeueDeadRepairAccountError{
				ExternalAccountID: accountID, Message: repairErr.Error()})
			continue
		}
		result.Accounts = append(result.Accounts, account)
		if account.Requeued {
			result.TotalRequeued++
		}
	}
	sort.Slice(result.Accounts, func(i, j int) bool {
		return result.Accounts[i].ExternalAccountID < result.Accounts[j].ExternalAccountID
	})
	sort.Slice(result.Errors, func(i, j int) bool {
		return result.Errors[i].ExternalAccountID < result.Errors[j].ExternalAccountID
	})
	return result, nil
}

// projectionRequeueDeadCandidateAccountIDs is a plain, transaction-less
// snapshot read, mirroring queueNarrowCandidateAccountIDs' own reasoning: a
// stale snapshot here (a row requeued concurrently between this read and
// that account's own transaction) is harmless -- that account's transaction
// simply finds zero matching rows and reports an empty, error-free result.
func (s *Store) projectionRequeueDeadCandidateAccountIDs(ctx context.Context, accountID string) ([]string, error) {
	accountID = strings.TrimSpace(accountID)
	var rows pgx.Rows
	var err error
	if accountID != "" {
		rows, err = s.pool.Query(ctx, `
			SELECT external_account_id::text FROM eligibility_projection_jobs
			WHERE status='dead' AND external_account_id=$1
			ORDER BY 1`, accountID)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT external_account_id::text FROM eligibility_projection_jobs
			WHERE status='dead'
			ORDER BY 1`)
	}
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

// repairProjectionRequeueDeadAccount is the whole per-account unit of work:
// its own transaction, committed on success in apply mode and always rolled
// back in dry-run mode. A defined, zero-error, Requeued=false result covers
// the case where the row no longer matches (resolved or requeued
// concurrently since the candidate snapshot read) -- only a genuine
// database/query error is ever returned.
func (s *Store) repairProjectionRequeueDeadAccount(ctx context.Context, accountID string, in ProjectionRequeueDeadRepairInput, actor AuditActor) (ProjectionRequeueDeadRepairAccount, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return ProjectionRequeueDeadRepairAccount{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	row := ProjectionRequeueDeadRepairAccount{ExternalAccountID: accountID}
	var lastErrorCode, lastError *string
	err = tx.QueryRow(ctx, `
		SELECT attempts,last_error_code,last_error,updated_at
		FROM eligibility_projection_jobs WHERE external_account_id=$1 AND status='dead'
		FOR UPDATE`, accountID).Scan(&row.PreviousAttempts, &lastErrorCode, &lastError, &row.DeadSince)
	if errors.Is(err, pgx.ErrNoRows) {
		// No longer a matching dead row -- a defined, empty, error-free
		// result, not a fault.
		return ProjectionRequeueDeadRepairAccount{ExternalAccountID: accountID}, nil
	}
	if err != nil {
		return ProjectionRequeueDeadRepairAccount{}, err
	}
	if lastErrorCode != nil {
		row.LastErrorCode = *lastErrorCode
	}
	if lastError != nil {
		row.LastError = *lastError
	}

	if !in.Apply {
		row.Requeued = true
		return row, nil
	}

	command, err := tx.Exec(ctx, `
		UPDATE eligibility_projection_jobs SET
			status='queued',attempts=0,lease_token=NULL,lease_expires_at=NULL,
			last_error_code=NULL,last_error=NULL,next_attempt_at=now(),updated_at=now()
		WHERE external_account_id=$1 AND status='dead'`, accountID)
	if err != nil {
		return ProjectionRequeueDeadRepairAccount{}, err
	}
	if command.RowsAffected() != 1 {
		return ProjectionRequeueDeadRepairAccount{}, errors.New("dead job no longer matches the repair predicate; refusing to apply")
	}
	row.Requeued = true

	if err = writeAudit(ctx, tx, actor, "eligibility.projection.requeued", "external_account", accountID,
		map[string]any{"status": "dead", "attempts": row.PreviousAttempts, "last_error_code": row.LastErrorCode},
		map[string]any{"status": "queued", "attempts": 0, "repair_tool": "XM-INV-PROJECTION-FAILURE-GRADING"}); err != nil {
		return ProjectionRequeueDeadRepairAccount{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ProjectionRequeueDeadRepairAccount{}, err
	}
	return row, nil
}
