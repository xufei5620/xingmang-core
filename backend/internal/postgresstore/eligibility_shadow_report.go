package postgresstore

import (
	"context"
	"time"
)

// XM-INV-SHADOW-EVAL: this file adds read-only reporting queries for the
// release-rehearsal tool (backend/cmd/eligibility-shadow). It runs the real
// exported eligibility-projection worker (ProcessEligibilityProjectionJobs)
// and EligibilityProjectionHealth against a throwaway restored copy of a
// production backup, then uses the three methods below to describe the
// account-level and evidence-level state before and after the drain -- none
// of them lock or mutate anything, and none is used on the production
// database in the normal request or worker paths.

// EligibilityShadowAccountStatus is one external account's current
// eligibility_status (see getEligibilityAccountTx and the "active" /
// "frozen" / "syncing" values ProcessEligibilityProjectionJobs and identity
// catch-up write) plus its open freeze count, as of the moment the snapshot
// query ran.
type EligibilityShadowAccountStatus struct {
	ExternalAccountID string
	EligibilityStatus string
	OpenFreezes       int64
}

// EligibilityShadowFreezeCount is the open freeze count for one
// freeze_reason (eligibility_freezes.freeze_reason), across every account.
type EligibilityShadowFreezeCount struct {
	FreezeReason string
	Open         int64
}

// EligibilityShadowEvaluationCount is the count of balance evidence items
// (checkpoints and carry-forward proofs, combined) currently at one
// evaluation_status, keeping only the latest -- highest projection_version
// -- evaluation per checkpoint/proof. A re-evaluated item is counted once
// under its current status, not once per historical attempt, matching how
// eligibility_operations.go's freeze-queue detail view reads "the" status of
// an evidence item.
type EligibilityShadowEvaluationCount struct {
	EvaluationStatus string
	Count            int64
}

// EligibilityShadowFailedJob is one eligibility_projection_jobs row left in
// status='failed'. It is the only durable trace of a per-account projection
// error: ProcessEligibilityProjectionJobs marks a genuinely failing account
// 'failed' with the fixed last_error_code 'PROJECTION_FAILED' (see its doc
// comment) rather than persisting the underlying Go error text, which is
// returned to the caller only as that round's in-memory
// firstProcessingError.
type EligibilityShadowFailedJob struct {
	ExternalAccountID string
	LastErrorCode     string
	AttemptCount      int64
	UpdatedAt         time.Time
}

// EligibilityShadowSnapshot is a point-in-time, lock-free read of every
// account's eligibility status, open freezes by reason, and balance evidence
// outcomes by evaluation status.
type EligibilityShadowSnapshot struct {
	Accounts    []EligibilityShadowAccountStatus
	OpenFreezes []EligibilityShadowFreezeCount
	Evaluations []EligibilityShadowEvaluationCount
}

// EligibilityShadowSnapshot gathers the three read-only views above in one
// call. It takes no lock and writes nothing, so it is safe to call
// immediately before and immediately after a ProcessEligibilityProjectionJobs
// drain to compare the two.
func (s *Store) EligibilityShadowSnapshot(ctx context.Context) (EligibilityShadowSnapshot, error) {
	var snapshot EligibilityShadowSnapshot

	accountRows, err := s.pool.Query(ctx, `
		SELECT eas.external_account_id::text,eas.eligibility_status,
			(SELECT count(*) FROM eligibility_freezes ef
			 WHERE ef.external_account_id=eas.external_account_id AND ef.status='open')
		FROM source_account_eligibility_state eas
		ORDER BY eas.external_account_id`)
	if err != nil {
		return snapshot, err
	}
	for accountRows.Next() {
		var account EligibilityShadowAccountStatus
		if err = accountRows.Scan(&account.ExternalAccountID, &account.EligibilityStatus, &account.OpenFreezes); err != nil {
			accountRows.Close()
			return snapshot, err
		}
		snapshot.Accounts = append(snapshot.Accounts, account)
	}
	if err = accountRows.Err(); err != nil {
		accountRows.Close()
		return snapshot, err
	}
	accountRows.Close()

	freezeRows, err := s.pool.Query(ctx, `
		SELECT freeze_reason,count(*) FROM eligibility_freezes
		WHERE status='open' GROUP BY freeze_reason ORDER BY freeze_reason`)
	if err != nil {
		return snapshot, err
	}
	for freezeRows.Next() {
		var count EligibilityShadowFreezeCount
		if err = freezeRows.Scan(&count.FreezeReason, &count.Open); err != nil {
			freezeRows.Close()
			return snapshot, err
		}
		snapshot.OpenFreezes = append(snapshot.OpenFreezes, count)
	}
	if err = freezeRows.Err(); err != nil {
		freezeRows.Close()
		return snapshot, err
	}
	freezeRows.Close()

	evaluationRows, err := s.pool.Query(ctx, `
		WITH latest_checkpoint AS (
			SELECT DISTINCT ON (checkpoint_id) evaluation_status
			FROM balance_checkpoint_evaluations
			ORDER BY checkpoint_id,projection_version DESC
		), latest_proof AS (
			SELECT DISTINCT ON (proof_id) evaluation_status
			FROM balance_carry_forward_evaluations
			ORDER BY proof_id,projection_version DESC
		)
		SELECT evaluation_status,count(*) FROM (
			SELECT evaluation_status FROM latest_checkpoint
			UNION ALL
			SELECT evaluation_status FROM latest_proof
		) combined
		GROUP BY evaluation_status ORDER BY evaluation_status`)
	if err != nil {
		return snapshot, err
	}
	for evaluationRows.Next() {
		var count EligibilityShadowEvaluationCount
		if err = evaluationRows.Scan(&count.EvaluationStatus, &count.Count); err != nil {
			evaluationRows.Close()
			return snapshot, err
		}
		snapshot.Evaluations = append(snapshot.Evaluations, count)
	}
	if err = evaluationRows.Err(); err != nil {
		evaluationRows.Close()
		return snapshot, err
	}
	evaluationRows.Close()

	return snapshot, nil
}

// EligibilityProjectionClaimableCount returns the number of
// eligibility_projection_jobs rows ProcessEligibilityProjectionJobs' claim
// query would pick up right now. The WHERE condition is kept identical to
// that query's CTE by hand -- the claim query is a single UPDATE...FROM CTE
// and cannot itself be reused as a read-only count. XM-INV-SHADOW-EVAL uses
// this after every round to decide whether the queue has drained (nothing
// left immediately claimable, whether because it is empty or because every
// remaining row is on a future backoff) versus merely hit its --max-rounds
// cap while still doing useful work.
func (s *Store) EligibilityProjectionClaimableCount(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var count int64
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM eligibility_projection_jobs
		WHERE (status IN ('queued','failed') AND next_attempt_at<=$1)
			OR (status='processing' AND lease_expires_at<=$1)`, now.UTC()).Scan(&count)
	return count, err
}

// EligibilityShadowFailedJobs lists every eligibility_projection_jobs row
// currently left in status='failed'.
func (s *Store) EligibilityShadowFailedJobs(ctx context.Context) ([]EligibilityShadowFailedJob, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT external_account_id::text,COALESCE(last_error_code,''),attempt_count,updated_at
		FROM eligibility_projection_jobs WHERE status='failed' ORDER BY external_account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var failed []EligibilityShadowFailedJob
	for rows.Next() {
		var job EligibilityShadowFailedJob
		if err = rows.Scan(&job.ExternalAccountID, &job.LastErrorCode, &job.AttemptCount, &job.UpdatedAt); err != nil {
			return nil, err
		}
		failed = append(failed, job)
	}
	return failed, rows.Err()
}
