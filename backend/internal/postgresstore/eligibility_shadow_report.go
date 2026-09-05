package postgresstore

import (
	"context"
	"errors"
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

	// XM-INV-SHADOW-EVAL-VACUOUS: the quantities an allocation change
	// actually moves, so a reviewer can diff the numbers the change is
	// about rather than only their classifications. RC88's carry-forward
	// slice is the worked example: it was meant to make one account's
	// expected balance move by a specific amount, and the report it was
	// gated on could not have shown that even if the projection had run,
	// because it carried no per-account quantity at all.
	//
	// ProjectionVersion increments once per completed reprojection, so an
	// account whose version did not move was not reprojected -- the
	// per-account counterpart to the report's AccountsProjected total.
	// OverageUnits is read as text: non_invoiceable_overage_units is
	// NUMERIC and source balance units are 1e8-scaled, so its magnitude is
	// not bounded by int64 and this report has no reason to impose one.
	// It is also nullable, and NULL is the ordinary case -- six of the eight
	// accounts in production carry no overage. It is rendered as "0", which
	// is unambiguous here rather than merely convenient: migration 0020's
	// CHECK is (IS NULL OR > 0), so a stored value can never be zero and "0"
	// can only mean "none". Scanning it straight into a string, as the first
	// draft of this did, fails on every such row -- the integration test
	// below caught that before it could break the rehearsal it was meant to
	// fix.
	ProjectionVersion int64
	FinalizedThrough  time.Time
	OverageUnits      string
	CapMinor          int64
	ConsumedCashMinor int64
	ReservedMinor     int64
	IssuedMinor       int64
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
// status='dead' (XM-INV-PROJECTION-FAILURE-GRADING renamed the durable
// terminal grade from 'failed', which markEligibilityProjectionJobFailedOrDead
// no longer produces, to 'dead' after projectionFailureDeadThreshold
// consecutive failures; a job merely retrying with backoff is status='queued'
// and intentionally excluded here -- see the type's own field-shape note
// below). It is the only durable, per-account trace of a genuinely stuck
// projection error: ProcessEligibilityProjectionJobs' round-level
// firstProcessingError (surfaced as this report's RoundErrors) records that
// some account failed during a round, but not which one, nor whether it
// later recovered on its own backoff-driven retry within the same rehearsal.
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
			 WHERE ef.external_account_id=eas.external_account_id AND ef.status='open'),
			eas.projection_version,eas.finalized_through,
			COALESCE(eas.non_invoiceable_overage_units::text,'0'),
			COALESCE(lots.cap,0),COALESCE(lots.consumed_cash,0),
			COALESCE(lots.reserved,0),COALESCE(lots.issued,0)
		FROM source_account_eligibility_state eas
		LEFT JOIN LATERAL (
			SELECT sum(lot.current_cap_minor) AS cap,
				sum(lot.consumed_cash_minor) AS consumed_cash,
				sum(lot.reserved_minor) AS reserved,
				sum(lot.issued_minor) AS issued
			FROM funding_lots lot
			WHERE lot.external_account_id=eas.external_account_id
		) lots ON TRUE
		ORDER BY eas.external_account_id`)
	if err != nil {
		return snapshot, err
	}
	for accountRows.Next() {
		var account EligibilityShadowAccountStatus
		if err = accountRows.Scan(&account.ExternalAccountID, &account.EligibilityStatus, &account.OpenFreezes,
			&account.ProjectionVersion, &account.FinalizedThrough, &account.OverageUnits,
			&account.CapMinor, &account.ConsumedCashMinor, &account.ReservedMinor, &account.IssuedMinor); err != nil {
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
// currently left in status='dead' (see EligibilityShadowFailedJob's own doc
// comment for why 'dead', not the legacy 'failed', is the right predicate
// after XM-INV-PROJECTION-FAILURE-GRADING).
func (s *Store) EligibilityShadowFailedJobs(ctx context.Context) ([]EligibilityShadowFailedJob, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT external_account_id::text,COALESCE(last_error_code,''),attempt_count,updated_at
		FROM eligibility_projection_jobs WHERE status='dead' ORDER BY external_account_id`)
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

// EnqueueEligibilityShadowReprojection queues one projection job per account,
// at that account's own finalized_through, and returns how many rows it
// inserted. It is the only write in this file, and it exists because without
// it the rehearsal it serves is vacuous by construction.
//
// XM-INV-SHADOW-EVAL-VACUOUS: eligibility-shadow drains whatever is already
// in eligibility_projection_jobs. A healthy production has an empty queue --
// that is what healthy means -- so a backup restored from one gives the
// candidate evaluator nothing to do, and the rehearsal reported "ready" with
// before and after byte-identical. RC78, RC79 and RC88 all shipped evaluator
// or migration changes past that verdict.
//
// Requesting each account at its own finalized_through is an existing,
// supported call shape rather than a new one: processEligibilityProjectionJob
// reads requested_through, raises it to finalized_through when it is below
// (never lowers it, and never skips), and then calls reprojectEligibilityTx
// unconditionally -- there is no short-circuit for "the window did not
// advance". The same shape is what observeEligibilityFactTx's late-fact path
// uses. The job's own final UPDATE is
// finalized_through=GREATEST(finalized_through,requested), so replaying an
// account at its current boundary cannot move that boundary backwards or
// forwards; the account ends where it started, having recomputed everything
// in between with the candidate's code.
//
// A pre-existing job that is not dead is retargeted to the account's own
// boundary rather than left alone (RC92 finding). A continuously-consuming
// account always has a queued job at backup time, requested through a window
// whose tail no balances cycle in the frozen copy will ever cover, so left as
// captured it is claimed once, returns BALANCE_PROOF_PENDING, and the account
// -- the one whose reprojection matters most -- is the one the rehearsal
// never exercises. Requested at its own finalized_through the proof has no
// uncovered visibility to wait on and the replay runs. A status='dead' row is
// still left untouched: that terminal grade exists so a persistently failing
// account stops being retried until an operator looks, and a rehearsal must
// not quietly revive it. The returned count includes retargeted rows.
func (s *Store) EnqueueEligibilityShadowReprojection(ctx context.Context) (int64, error) {
	command, err := s.pool.Exec(ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		SELECT external_account_id,finalized_through,'queued',now()-interval '1 second'
		FROM source_account_eligibility_state
		ON CONFLICT (external_account_id) DO UPDATE SET
			requested_through=EXCLUDED.requested_through,status='queued',
			next_attempt_at=EXCLUDED.next_attempt_at,lease_token=NULL,lease_expires_at=NULL,
			last_error_code=NULL,attempt_count=0,updated_at=now()
		WHERE eligibility_projection_jobs.status<>'dead'`)
	if err != nil {
		return 0, err
	}
	return command.RowsAffected(), nil
}

// EligibilityShadowPendingJob is one eligibility_projection_jobs row still
// present after the drain that is not dead: a job the drain could not finish,
// usually because it was requeued with backoff (BALANCE_PROOF_PENDING) and its
// next attempt lies beyond the drain's "nothing claimable" exit. Without this
// list the report can only say "7 of 8"; with it, it says which account and
// why.
type EligibilityShadowPendingJob struct {
	ExternalAccountID string
	Status            string
	LastErrorCode     string
	AttemptCount      int64
	RequestedThrough  time.Time
	NextAttemptAt     time.Time
}

// EligibilityShadowPendingJobs lists every non-dead job row left after the
// drain. Read-only; the dead rows are EligibilityShadowFailedJobs' concern.
func (s *Store) EligibilityShadowPendingJobs(ctx context.Context) ([]EligibilityShadowPendingJob, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT external_account_id::text,status,COALESCE(last_error_code,''),attempt_count,requested_through,next_attempt_at
		FROM eligibility_projection_jobs WHERE status<>'dead' ORDER BY external_account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pending []EligibilityShadowPendingJob
	for rows.Next() {
		var job EligibilityShadowPendingJob
		if err = rows.Scan(&job.ExternalAccountID, &job.Status, &job.LastErrorCode, &job.AttemptCount, &job.RequestedThrough, &job.NextAttemptAt); err != nil {
			return nil, err
		}
		pending = append(pending, job)
	}
	return pending, rows.Err()
}

// EligibilityShadowReevaluateEvidence deletes every balance-evidence
// evaluation at or after each account's anchor floor, so the next projection
// re-evaluates that evidence from scratch. It exists for one purpose: the
// differential rehearsal of a bounded evidence pass (XM-INV-CATCHUP-BURST-BACKPRESSURE
// fix 3). A restored copy carries every evaluation production has already
// made, so a rehearsal's evidence pass finds nothing pending and a bounded run
// is trivially identical to an unbounded one -- which proves nothing. Clearing
// the evaluations on the copy hands both runs the same pile, the shape of the
// 2026-09-04 incident, and makes the comparison mean something.
//
// Evaluation rows are immutable history under triggers, and this must never
// touch production: it refuses unless the session is a superuser, which the
// production runtime and owner roles are not and the rehearsal's throwaway
// container's `postgres` role is; and it lifts the triggers only for its own
// transaction via session_replication_role, never with the repair GUC that
// production code paths honour.
func (s *Store) EligibilityShadowReevaluateEvidence(ctx context.Context) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var superuser bool
	if err = tx.QueryRow(ctx, `SELECT current_setting('is_superuser')='on'`).Scan(&superuser); err != nil {
		return 0, err
	}
	if !superuser {
		return 0, errors.New("reevaluate-evidence is a rehearsal-only operation and requires a superuser session on a restored copy")
	}
	if _, err = tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		return 0, err
	}
	checkpoints, err := tx.Exec(ctx, `
		DELETE FROM balance_checkpoint_evaluations evaluation
		USING balance_reconciliation_checkpoints checkpoint
		JOIN source_account_eligibility_state state ON state.external_account_id=checkpoint.external_account_id
		WHERE evaluation.checkpoint_id=checkpoint.id
		  AND checkpoint.as_of>=CASE WHEN state.bootstrap_kind='POLICY_ANCHOR' THEN state.cutover_at ELSE '-infinity'::timestamptz END`)
	if err != nil {
		return 0, err
	}
	proofs, err := tx.Exec(ctx, `
		DELETE FROM balance_carry_forward_evaluations evaluation
		USING balance_carry_forward_proofs proof
		JOIN source_account_eligibility_state state ON state.external_account_id=proof.external_account_id
		WHERE evaluation.proof_id=proof.id
		  AND proof.as_of>=CASE WHEN state.bootstrap_kind='POLICY_ANCHOR' THEN state.cutover_at ELSE '-infinity'::timestamptz END`)
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return checkpoints.RowsAffected() + proofs.RowsAffected(), nil
}
