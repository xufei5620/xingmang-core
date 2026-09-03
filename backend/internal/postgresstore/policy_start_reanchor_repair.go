package postgresstore

import (
	"context"
	"errors"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"invoice-system/backend/internal/domain"
)

// PolicyStartReanchorRepairInput drives RepairPolicyStartReanchorEligibility
// (design XM-INV-ELIG-SIMPLIFY section 3(D)'s final paragraph): re-anchors
// the accounts bootstrapped POLICY_ANCHOR *before* this slice shipped --
// whose cutover_at is still the triggering checkpoint's own as_of rather
// than the global policy start -- so they get the same dead-zone fix
// ObserveBalanceCheckpoint's own bootstrap branch now gives every account
// bootstrapped after this slice. A versioned, human-approved lifecycle
// operation, following the same dry-run/apply/operator-id convention as the
// other three eligibility-repair kinds.
type PolicyStartReanchorRepairInput struct {
	// Apply, when false (the default), reports what each affected account's
	// own transaction would do without committing anything (every
	// per-account transaction is rolled back, including for an account this
	// run determines is a no-op). Apply commits one transaction per account
	// that has a real, in-window funding lot to include; an account with no
	// such lot is left completely untouched in both modes -- see NoOp below.
	Apply bool
	// OperatorID is required when Apply is true: the acting admin UUID
	// recorded on this run's own audit event. Unlike the other three repair
	// kinds, this one never resolves an eligibility_freezes row (a
	// POLICY_ANCHOR account's cutover boundary is not gated behind one), so
	// there is no encrypted resolution note/evidence to supply.
	OperatorID string
}

// PolicyStartReanchorRepairAccount is one row of the repair's per-account
// summary table.
type PolicyStartReanchorRepairAccount struct {
	ExternalAccountID      string
	SourceInstanceID       string
	OldCutoverAt           time.Time
	NewCutoverAt           time.Time
	OldCutoverBalanceUnits string
	NewCutoverBalanceUnits string
	// WindowFundingLots is the number of verified, non-refund-frozen
	// WALLET_CASH funding lots with completed_at in (policyStartAt,
	// OldCutoverAt] -- exactly the predicate buildEligibilityProjectionTx's
	// own cash-pool query uses, so this count precisely predicts which lots
	// change from excluded to included once cutover_at moves back to the
	// policy start. Design section 3(D)'s own text describes this window
	// loosely as "[policy.StartAt, current cutover_at)"; this repair matches
	// the exact projection predicate instead (see the doc comment on the
	// window query below) since that is what actually determines whether
	// re-anchoring changes anything.
	WindowFundingLots int
	// NoOp is true when WindowFundingLots is zero: the design's own
	// "expected no-op" case (usage/credit facts in this window are already
	// unrecoverable for these accounts -- see deriveCutoverBalanceUnitsTx's
	// doc comment -- so with no funding lot either, moving cutover_at
	// backward would change no invoiceable amount, only add risk). Neither
	// dry-run nor apply writes anything for a NoOp account -- see the
	// RepairPolicyStartReanchorEligibility doc comment.
	NoOp bool
	// Blocked is true when the account has a real in-window funding lot
	// (NoOp is false) but at least one of its funding lots also carries an
	// active invoice_allocations exposure (reserved, issued or
	// refund_attention) -- resetting consumption state would corrupt real
	// invoice accounting already depending on it, mirroring design
	// XM-INV-POLICY-ANCHOR 2.4's own identical guard in
	// reanchorLegacyEligibilityAccountTx. Neither mode writes anything for a
	// Blocked account; unlike 2.4 (which freezes POLICY_ANCHOR_BLOCKED,
	// since it runs inside the periodic projection job and needs the freeze
	// to force a retry), this is a standalone, re-runnable CLI tool -- an
	// operator can simply re-run it later once the exposure clears, so this
	// repair reports Blocked and takes no action rather than also freezing.
	// See the handoff doc's own note on this deliberate difference from "the
	// same as design 2.4."
	Blocked bool
	// Reprojected is true once apply has rebuilt this account's allocations
	// through its own prior finalized_through (always false for a dry run,
	// a NoOp account, or a Blocked account).
	Reprojected bool
}

// PolicyStartReanchorRepairAccountError records one account whose own repair
// transaction failed -- collected, never allowed to abort the run for any
// other account (the same per-account isolation
// docs/handoffs/XM-INV-ELIG-QUEUE-NARROW.md's own repair established, for
// the identical reason: a single account's own data inconsistency must never
// block every other account waiting in the same batch).
type PolicyStartReanchorRepairAccountError struct {
	ExternalAccountID string
	Message           string
}

// PolicyStartReanchorRepairResult is the repair's full summary, printable
// as-is by the CLI in both dry-run and apply modes.
type PolicyStartReanchorRepairResult struct {
	Applied  bool
	Accounts []PolicyStartReanchorRepairAccount
	Errors   []PolicyStartReanchorRepairAccountError
}

// RepairPolicyStartReanchorEligibility implements design
// XM-INV-ELIG-SIMPLIFY section 3(D)'s production migration for the accounts
// this design's own ObserveBalanceCheckpoint fix cannot reach retroactively.
// Like XM-INV-ELIG-QUEUE-NARROW's own repair (and unlike the three earlier
// sibling repairs, which each run their whole pass in one transaction), this
// processes one account per transaction: a per-account SERIALIZABLE
// transaction, its own advisory lock, and a per-account error collected into
// Errors rather than aborting the run -- the same discipline a prior
// evaluator-change regression (RC75) taught this codebase to require for any
// batch operation over eligibility state (see
// docs/handoffs/XM-INV-ELIG-QUEUE-NARROW.md's own "Deviation 1").
//
// A candidate account with zero in-window funding lots is a defined,
// deliberate no-op in *both* modes, not merely a dry-run preview: moving
// cutover_at backward with no invoiceable amount to gain is pure downside
// risk (design 3(D)'s own words: "只增风险无收益"), so this repair does not
// write anything for such an account even under --apply. Only an account
// with a real, unexpected in-window WALLET_CASH funding lot is actually
// re-anchored.
func (s *Store) RepairPolicyStartReanchorEligibility(ctx context.Context, in PolicyStartReanchorRepairInput, actor AuditActor) (PolicyStartReanchorRepairResult, error) {
	if in.Apply && !eligibilityUUIDPattern.MatchString(strings.TrimSpace(in.OperatorID)) {
		return PolicyStartReanchorRepairResult{}, errors.New("a valid operator UUID is required to apply")
	}

	accountIDs, err := s.policyStartReanchorCandidateAccountIDs(ctx)
	if err != nil {
		return PolicyStartReanchorRepairResult{}, err
	}

	result := PolicyStartReanchorRepairResult{Applied: in.Apply}
	for _, accountID := range accountIDs {
		account, repairErr := s.repairPolicyStartReanchorAccount(ctx, accountID, in, actor)
		if repairErr != nil {
			result.Errors = append(result.Errors, PolicyStartReanchorRepairAccountError{
				ExternalAccountID: accountID, Message: repairErr.Error()})
			continue
		}
		if account.ExternalAccountID == "" {
			// A stale snapshot candidate that no longer matches (concurrently
			// re-anchored, or no longer POLICY_ANCHOR) -- see
			// repairPolicyStartReanchorAccount's own doc comment. Not an
			// error, not reported as a row.
			continue
		}
		result.Accounts = append(result.Accounts, account)
	}
	sort.Slice(result.Accounts, func(i, j int) bool {
		return result.Accounts[i].ExternalAccountID < result.Accounts[j].ExternalAccountID
	})
	sort.Slice(result.Errors, func(i, j int) bool {
		return result.Errors[i].ExternalAccountID < result.Errors[j].ExternalAccountID
	})
	return result, nil
}

// policyStartReanchorCandidateAccountIDs is a plain, transaction-less
// snapshot read -- mirroring queueNarrowCandidateAccountIDs's own precedent:
// it only decides which accounts are worth attempting. Each account's own
// repairPolicyStartReanchorAccount call re-selects and locks (FOR UPDATE) its
// own row from scratch, so a stale snapshot here (an account re-anchored
// concurrently between this read and that account's own transaction) is
// harmless.
func (s *Store) policyStartReanchorCandidateAccountIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT eas.external_account_id::text FROM source_account_eligibility_state eas
		CROSS JOIN invoice_eligibility_policy policy
		WHERE eas.bootstrap_kind='POLICY_ANCHOR' AND eas.cutover_at>policy.eligibility_start_at
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

// repairPolicyStartReanchorAccount is the whole per-account unit of work: its
// own SERIALIZABLE transaction, committed on success in apply mode (only for
// a real, non-NoOp re-anchor) and always rolled back otherwise (dry-run, or
// a NoOp account in either mode). Every branch below returns a defined,
// zero-error result for any data shape this account might legitimately be
// in -- only a genuine database/query error is ever returned, and the caller
// collects that as one PolicyStartReanchorRepairAccountError without
// touching any other account. A zero-value result (ExternalAccountID=="")
// means the account is no longer a candidate at all (concurrently resolved
// since the snapshot read) -- distinct from a legitimate NoOp row, which
// still carries the account's identity and the (zero) window lot count.
func (s *Store) repairPolicyStartReanchorAccount(ctx context.Context, accountID string, in PolicyStartReanchorRepairInput, actor AuditActor) (PolicyStartReanchorRepairAccount, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return PolicyStartReanchorRepairAccount{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	// Same per-account advisory lock processEligibilityProjectionJob and the
	// sibling repairs take before touching this account's eligibility state:
	// this repair calls the identical reprojectEligibilityTx machinery
	// outside the normal job queue, so it must serialize against a
	// concurrent real projection job or Observe*/evaluator call the same
	// way.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,43))`, accountID); err != nil {
		return PolicyStartReanchorRepairAccount{}, err
	}

	account, err := getEligibilityAccountTx(ctx, tx, accountID, true)
	if errors.Is(err, domain.ErrSourceUnavailable) {
		return PolicyStartReanchorRepairAccount{}, nil
	}
	if err != nil {
		return PolicyStartReanchorRepairAccount{}, err
	}
	if account.BootstrapKind != "POLICY_ANCHOR" || !account.CutoverAt.After(account.PolicyStartAt) {
		// No longer a candidate: either re-anchored by an earlier run of
		// this same repair, or (defensively) never one to begin with.
		return PolicyStartReanchorRepairAccount{}, nil
	}

	var oldBalanceText string
	if err = tx.QueryRow(ctx, `SELECT cutover_balance_units::text FROM source_account_eligibility_state
		WHERE external_account_id=$1`, accountID).Scan(&oldBalanceText); err != nil {
		return PolicyStartReanchorRepairAccount{}, err
	}
	oldBalance, ok := new(big.Int).SetString(strings.TrimSpace(oldBalanceText), 10)
	if !ok {
		return PolicyStartReanchorRepairAccount{}, errors.New("stored cutover_balance_units is not a canonical integer")
	}

	row := PolicyStartReanchorRepairAccount{
		ExternalAccountID:      accountID,
		SourceInstanceID:       account.SourceInstanceID,
		OldCutoverAt:           account.CutoverAt,
		OldCutoverBalanceUnits: oldBalanceText,
	}

	// The exact predicate buildEligibilityProjectionTx's own cash-pool query
	// uses (see that function's doc comment and the WindowFundingLots field
	// doc comment above) -- this is what precisely determines whether
	// re-anchoring changes any invoiceable amount at all.
	if err = tx.QueryRow(ctx, `
		SELECT count(*) FROM funding_lots
		WHERE external_account_id=$1 AND eligibility_kind='WALLET_CASH'
			AND verification_state='verified' AND refund_frozen=FALSE
			AND completed_at>$2 AND completed_at<=$3`,
		accountID, account.PolicyStartAt, account.CutoverAt).Scan(&row.WindowFundingLots); err != nil {
		return PolicyStartReanchorRepairAccount{}, err
	}
	if row.WindowFundingLots == 0 {
		// Design 3(D)'s own "expected no-op": change nothing, in either
		// mode -- see the function's own doc comment.
		row.NoOp = true
		row.NewCutoverAt = account.CutoverAt
		row.NewCutoverBalanceUnits = oldBalanceText
		return row, nil
	}

	// Mirror design XM-INV-POLICY-ANCHOR 2.4's own exposure guard (see the
	// Blocked field's own doc comment): resetting consumption state for a
	// lot with a live invoice_allocations reservation/issuance/refund
	// review would corrupt real invoice accounting already depending on it
	// -- and would also violate funding_lots' own immediate CHECK
	// (reserved_minor+issued_minor<=consumed_cash_minor) the instant the
	// reset UPDATE sets consumed_cash_minor back to zero, surfacing as a
	// raw constraint-violation error instead of a deliberate, defined
	// outcome. Checked before any write for this account.
	var hasExposure bool
	if err = tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM invoice_allocations ia
			JOIN funding_lots fl ON fl.id=ia.funding_lot_id
			WHERE fl.external_account_id=$1 AND ia.allocation_state IN ('reserved','issued','refund_attention')
		)`, accountID).Scan(&hasExposure); err != nil {
		return PolicyStartReanchorRepairAccount{}, err
	}
	if hasExposure {
		row.Blocked = true
		row.NewCutoverAt = account.CutoverAt
		row.NewCutoverBalanceUnits = oldBalanceText
		return row, nil
	}

	derivedBalance, err := deriveCutoverBalanceUnitsTx(ctx, tx, accountID, account.PolicyStartAt, account.CutoverAt, oldBalance)
	if err != nil {
		return PolicyStartReanchorRepairAccount{}, err
	}
	row.NewCutoverAt = account.PolicyStartAt
	row.NewCutoverBalanceUnits = derivedBalance.String()

	if !in.Apply {
		return row, nil
	}

	// The account's own existing anchor checkpoint -- migration 0016's
	// validation already guarantees exactly one such row exists for the
	// account's current (cutover_at,cutover_balance_units) pair. Its
	// provenance is what the newly-derived checkpoint borrows, the same
	// technique the bootstrap path uses for a freshly-observed checkpoint --
	// see synthesizePolicyStartReconciliationCheckpointTx's own doc comment.
	var borrowCheckpointID, borrowExternalEventID, borrowCursor, borrowRevision string
	var borrowSequence int64
	var borrowWatermark, borrowObserved time.Time
	if err = tx.QueryRow(ctx, `
		SELECT checkpoint_id,external_event_id,source_sequence,source_cursor,stream_watermark_at,
			source_revision_hash,observed_at
		FROM balance_reconciliation_checkpoints
		WHERE external_account_id=$1 AND checkpoint_kind='reconciliation'
			AND as_of=$2 AND balance_service_units=$3::numeric
		LIMIT 1`, accountID, account.CutoverAt, oldBalanceText).Scan(
		&borrowCheckpointID, &borrowExternalEventID, &borrowSequence, &borrowCursor,
		&borrowWatermark, &borrowRevision, &borrowObserved); err != nil {
		return PolicyStartReanchorRepairAccount{}, err
	}

	if err = synthesizePolicyStartReconciliationCheckpointTx(ctx, tx, accountID, account.SourceInstanceID,
		account.UnitCode, account.ManifestHash, account.ConfigurationHash, borrowCheckpointID,
		borrowExternalEventID, account.PolicyStartAt, derivedBalance, borrowSequence, borrowCursor,
		borrowWatermark, borrowRevision, borrowObserved); err != nil {
		return PolicyStartReanchorRepairAccount{}, err
	}

	// Reset consumption state, mirroring reanchorLegacyEligibilityAccountTx's
	// own reset (design XM-INV-POLICY-ANCHOR 2.4) exactly, including its
	// scope (both WALLET_CASH and SUBSCRIPTION_CASH lots, though only
	// WALLET_CASH participates in buildEligibilityProjectionTx's own cash
	// pool today -- resetting both keeps this repair consistent with the
	// one other place in the codebase that performs this reset, rather than
	// inventing a narrower scope).
	if _, err = tx.Exec(ctx, `
		DELETE FROM consumption_allocations
		WHERE usage_event_id IN (SELECT id FROM source_usage_events WHERE external_account_id=$1)`,
		accountID); err != nil {
		return PolicyStartReanchorRepairAccount{}, err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE funding_lot_consumption_state flcs SET
			consumed_service_units=0,cumulative_cash_numerator=0,
			rounded_consumed_cash_minor=0,rounding_remainder_numerator=0,
			state_version=state_version+1,updated_at=now()
		FROM funding_lots fl
		WHERE fl.id=flcs.funding_lot_id AND fl.external_account_id=$1
			AND fl.eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH')`, accountID); err != nil {
		return PolicyStartReanchorRepairAccount{}, err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE funding_lots SET consumed_cash_minor=0,eligibility_revision=eligibility_revision+1,updated_at=now()
		WHERE external_account_id=$1 AND eligibility_kind IN ('WALLET_CASH','SUBSCRIPTION_CASH')`,
		accountID); err != nil {
		return PolicyStartReanchorRepairAccount{}, err
	}

	if _, err = tx.Exec(ctx, `SELECT set_config('invoice.policy_anchor_start_reanchor','on',true)`); err != nil {
		return PolicyStartReanchorRepairAccount{}, err
	}
	command, err := tx.Exec(ctx, `
		UPDATE source_account_eligibility_state SET
			cutover_at=$2,cutover_balance_units=$3::numeric,bootstrap_kind='POLICY_ANCHOR',
			finalized_through=$2,projection_version=projection_version+1,updated_at=now()
		WHERE external_account_id=$1`, accountID, account.PolicyStartAt, derivedBalance.String())
	if err != nil {
		return PolicyStartReanchorRepairAccount{}, err
	}
	if command.RowsAffected() != 1 {
		return PolicyStartReanchorRepairAccount{}, domain.ErrConflict
	}

	// Rebuild allocations immediately through the account's own prior
	// finalized_through (design's own "reprojects exactly as design 2.4
	// does") -- reprojectEligibilityTx also calls recordUsageOverageTx
	// unconditionally, so any usage overage this wider window produces
	// already flows through XM-INV-ELIG-AUTO-RECONCILE's own auto-downgrade,
	// never a freeze. The balance-evidence evaluation of the newly-derived
	// checkpoint (and any negative-difference auto-downgrade it may itself
	// produce) is deliberately left to the next projection job -- the
	// defensive queue below -- matching every sibling repair's own
	// precedent (none of the four existing repair tools call
	// evaluatePendingBalanceEvidenceTx directly; XM-INV-POLICY-ANCHOR 2.4's
	// own re-anchor, which this mirrors, only reprojects and relies on
	// processEligibilityProjectionJob's own subsequent steps for
	// evaluation).
	oldFinalizedThrough := account.FinalizedThrough
	if err = reprojectEligibilityTx(ctx, tx, accountID, oldFinalizedThrough, actor); err != nil {
		return PolicyStartReanchorRepairAccount{}, err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE source_account_eligibility_state
		SET finalized_through=GREATEST(finalized_through,$2),projection_version=projection_version+1,updated_at=now()
		WHERE external_account_id=$1`, accountID, oldFinalizedThrough); err != nil {
		return PolicyStartReanchorRepairAccount{}, err
	}
	row.Reprojected = true

	if _, err = tx.Exec(ctx, `
		INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
		VALUES($1,$2,'queued',now())
		ON CONFLICT(external_account_id) DO UPDATE SET
			requested_through=GREATEST(eligibility_projection_jobs.requested_through,EXCLUDED.requested_through),
			status='queued',lease_token=NULL,lease_expires_at=NULL,next_attempt_at=now(),updated_at=now()`,
		accountID, oldFinalizedThrough); err != nil {
		return PolicyStartReanchorRepairAccount{}, err
	}

	if err = writeAudit(ctx, tx, actor, "eligibility.policy_anchor.start_reanchored", "external_account", accountID,
		map[string]any{"cutover_at": row.OldCutoverAt, "cutover_balance_units": row.OldCutoverBalanceUnits},
		map[string]any{"cutover_at": row.NewCutoverAt, "cutover_balance_units": row.NewCutoverBalanceUnits,
			"window_funding_lots": row.WindowFundingLots, "operator_id": in.OperatorID,
			"repair_tool": "XM-INV-ELIG-POLICY-START-ANCHOR"}); err != nil {
		return PolicyStartReanchorRepairAccount{}, err
	}

	if err = tx.Commit(ctx); err != nil {
		return PolicyStartReanchorRepairAccount{}, err
	}
	return row, nil
}
