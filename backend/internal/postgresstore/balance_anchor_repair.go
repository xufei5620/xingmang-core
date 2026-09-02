package postgresstore

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// BalanceAnchorRepairInput drives RepairBalanceAnchorEligibility (design
// XM-INV-ANCHOR-BALANCE): a versioned, human-approved lifecycle operation
// that resolves the SOURCE_GAP freezes a POLICY_ANCHOR account's own anchor
// produced before evaluatePendingBalanceEvidenceTx's intervalStart query
// trusted it (consumption.go's case 1 branch, the
// bootstrap_kind='POLICY_ANCHOR' UNION arm added by this slice), now that
// the fixed code self-heals a fresh evaluation of the same evidence instead
// of freezing it.
type BalanceAnchorRepairInput struct {
	// Apply, when false (the default), runs every selection query and
	// reports what would change without writing anything (ROLLBACK at the
	// end regardless of what the queries found).
	Apply bool
	// OperatorID is required when Apply is true: the resolved_by UUID
	// recorded on every freeze this run resolves.
	OperatorID string
	// NoteCiphertext/NoteHash and EvidenceCiphertext/EvidenceHash are the
	// already-encrypted, fixed resolution note ("POLICY_ANCHOR balance
	// evidence self-healed by XM-INV-ANCHOR-BALANCE repair") this run
	// writes on every freeze it resolves -- encryption happens in the
	// caller, mirroring RepairPreAnchorUsageEligibility's identical split.
	// Required when Apply is true; ignored for a dry run.
	NoteCiphertext     []byte
	NoteHash           string
	EvidenceCiphertext []byte
	EvidenceHash       string
}

// BalanceAnchorRepairAccount is one row of the repair's per-account summary
// table. CheckpointEvaluationsReset counts only balance_checkpoint
// triggers: balance_checkpoint_evaluations carries no immutability trigger,
// so its stale source_gap_frozen row can be deleted, letting
// evaluatePendingBalanceEvidenceTx's pending query (its NOT EXISTS clause)
// treat the checkpoint as pending again and re-evaluate it with the fixed
// code. balance_carry_forward_evaluations is immutable by design
// (migration 0014's balance_carry_forward_evaluations_immutable trigger,
// BEFORE UPDATE OR DELETE, no exception) -- a balance_carry_forward_proof
// trigger's freeze is still resolved and counted in
// SourceGapFreezesResolved, but its stale evaluation row is left exactly as
// it is, a permanent (and, once resolved, inert) historical record; it does
// not block the account's recovery, since evaluatePendingBalanceEvidenceTx's
// own trusted-interval query only ever counts matched/positive_classified_non_cash
// rows as trust sources, never source_gap_frozen ones, and this repair's own
// fix works from account.CutoverAt forward regardless of what any single
// historical proof's stuck record says.
type BalanceAnchorRepairAccount struct {
	ExternalAccountID          string
	SourceInstanceID           string
	SourceGapFreezesResolved   int
	CheckpointEvaluationsReset int
	Reactivated                bool
}

// BalanceAnchorRepairResult is the repair's full summary, printable as-is
// by the CLI in both dry-run and apply modes.
type BalanceAnchorRepairResult struct {
	Applied                         bool
	Accounts                        []BalanceAnchorRepairAccount
	TotalSourceGapFreezesResolved   int
	TotalCheckpointEvaluationsReset int
}

type balanceAnchorFreezeCandidate struct {
	id                 string
	externalAccountID  string
	sourceInstanceID   string
	triggerObjectType  string
	triggerObjectID    string
	sourceRevisionHash string
}

// RepairBalanceAnchorEligibility implements design XM-INV-ANCHOR-BALANCE's
// production repair. Everything below runs inside one transaction: dry-run
// rolls it back unconditionally after gathering the same counts apply would
// report; apply commits only if every selected row still matches its
// predicate at UPDATE/DELETE time (re-asserted in each statement's own WHERE
// clause) -- any mismatch aborts the whole run with nothing written, matching
// RepairPreAnchorUsageEligibility's own "refuses to apply if any selected
// freeze does not match the predicate" contract.
//
// Scope, precise by construction (see
// docs/handoffs/XM-INV-ANCHOR-BALANCE.md for the full derivation): open
// SOURCE_GAP freezes with trigger_object_type IN ('balance_checkpoint',
// 'balance_carry_forward_proof'), scoped to accounts whose
// bootstrap_kind='POLICY_ANCHOR'. Verified (by reading
// evaluatePendingBalanceEvidenceTx, the only call site that freezes
// SOURCE_GAP with either of those two trigger_object_type values) that this
// predicate can only ever match freezes this specific bug produced -- a
// legacy (SIGNED_CUTOVER/POST_CUTOVER_REPLAY) account's SOURCE_GAP freeze on
// the same object types is a genuine gap (no equivalent trust source exists
// for it in the fixed code either) and is never touched; nor is any other
// freeze_reason on a POLICY_ANCHOR account (e.g. USAGE_EXCEEDS_LEDGER),
// which this repair never selects at all.
func (s *Store) RepairBalanceAnchorEligibility(ctx context.Context, in BalanceAnchorRepairInput, actor AuditActor) (BalanceAnchorRepairResult, error) {
	if in.Apply {
		if !eligibilityUUIDPattern.MatchString(strings.TrimSpace(in.OperatorID)) {
			return BalanceAnchorRepairResult{}, errors.New("a valid operator UUID is required to apply")
		}
		if !hexHashPattern.MatchString(in.NoteHash) || !hexHashPattern.MatchString(in.EvidenceHash) ||
			len(in.NoteCiphertext) < 16 || len(in.NoteCiphertext) > 8192 ||
			len(in.EvidenceCiphertext) < 16 || len(in.EvidenceCiphertext) > 8192 {
			return BalanceAnchorRepairResult{}, errors.New("encrypted resolution note and evidence are required to apply")
		}
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return BalanceAnchorRepairResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	rows, err := tx.Query(ctx, `
		SELECT ef.id,ef.external_account_id::text,eas.source_instance_id::text,
			ef.trigger_object_type,ef.trigger_object_id,COALESCE(ef.source_revision_hash,'')
		FROM eligibility_freezes ef
		JOIN source_account_eligibility_state eas ON eas.external_account_id=ef.external_account_id
		WHERE ef.status='open' AND ef.freeze_reason='SOURCE_GAP'
			AND ef.trigger_object_type IN ('balance_checkpoint','balance_carry_forward_proof')
			AND eas.bootstrap_kind='POLICY_ANCHOR'
		ORDER BY ef.external_account_id,ef.id
		FOR UPDATE OF ef`)
	if err != nil {
		return BalanceAnchorRepairResult{}, err
	}
	candidates := make([]balanceAnchorFreezeCandidate, 0)
	for rows.Next() {
		var c balanceAnchorFreezeCandidate
		if err = rows.Scan(&c.id, &c.externalAccountID, &c.sourceInstanceID,
			&c.triggerObjectType, &c.triggerObjectID, &c.sourceRevisionHash); err != nil {
			rows.Close()
			return BalanceAnchorRepairResult{}, err
		}
		candidates = append(candidates, c)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return BalanceAnchorRepairResult{}, err
	}
	rows.Close()

	summary := map[string]*BalanceAnchorRepairAccount{}
	accountIDs := make([]string, 0)
	ensureAccount := func(c balanceAnchorFreezeCandidate) *BalanceAnchorRepairAccount {
		if existing, ok := summary[c.externalAccountID]; ok {
			return existing
		}
		row := &BalanceAnchorRepairAccount{ExternalAccountID: c.externalAccountID, SourceInstanceID: c.sourceInstanceID}
		summary[c.externalAccountID] = row
		accountIDs = append(accountIDs, c.externalAccountID)
		return row
	}

	result := BalanceAnchorRepairResult{Applied: in.Apply}
	for _, c := range candidates {
		row := ensureAccount(c)
		if c.triggerObjectType == "balance_checkpoint" {
			reset, resetErr := resetSourceGapCheckpointEvaluation(ctx, tx, c.externalAccountID, c.triggerObjectID, in.Apply)
			if resetErr != nil {
				return BalanceAnchorRepairResult{}, resetErr
			}
			row.CheckpointEvaluationsReset += reset
			result.TotalCheckpointEvaluationsReset += reset
		}
		if in.Apply {
			if resolveErr := applyBalanceAnchorFreezeResolution(ctx, tx, c, in, actor); resolveErr != nil {
				return BalanceAnchorRepairResult{}, resolveErr
			}
		}
		row.SourceGapFreezesResolved++
		result.TotalSourceGapFreezesResolved++
	}

	if in.Apply {
		for _, accountID := range accountIDs {
			var remaining int
			if scanErr := tx.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes
				WHERE external_account_id=$1 AND status='open'`, accountID).Scan(&remaining); scanErr != nil {
				return BalanceAnchorRepairResult{}, scanErr
			}
			row := summary[accountID]
			if remaining != 0 {
				continue
			}
			if _, execErr := tx.Exec(ctx, `
				INSERT INTO eligibility_projection_jobs(external_account_id,requested_through,status,next_attempt_at)
				SELECT external_account_id,finalized_through,'queued',now() FROM source_account_eligibility_state
				WHERE external_account_id=$1
				ON CONFLICT(external_account_id) DO UPDATE SET status='queued',lease_token=NULL,
					lease_expires_at=NULL,next_attempt_at=now(),updated_at=now()`, accountID); execErr != nil {
				return BalanceAnchorRepairResult{}, execErr
			}
			command, execErr := tx.Exec(ctx, `UPDATE source_account_eligibility_state
				SET eligibility_status='active',projection_version=projection_version+1,updated_at=now()
				WHERE external_account_id=$1 AND eligibility_status='frozen'`, accountID)
			if execErr != nil {
				return BalanceAnchorRepairResult{}, execErr
			}
			if command.RowsAffected() == 1 {
				row.Reactivated = true
			}
			if auditErr := writeAudit(ctx, tx, actor, "eligibility.policy_anchor.balance_anchor_repaired",
				"external_account", accountID, nil, map[string]any{
					"source_gap_freezes_resolved":  row.SourceGapFreezesResolved,
					"checkpoint_evaluations_reset": row.CheckpointEvaluationsReset,
					"reactivated":                  row.Reactivated,
					"repair_tool":                  "XM-INV-ANCHOR-BALANCE",
				}); auditErr != nil {
				return BalanceAnchorRepairResult{}, auditErr
			}
		}
	}

	for _, id := range accountIDs {
		result.Accounts = append(result.Accounts, *summary[id])
	}
	sort.Slice(result.Accounts, func(i, j int) bool {
		return result.Accounts[i].ExternalAccountID < result.Accounts[j].ExternalAccountID
	})

	if !in.Apply {
		return result, nil
	}
	if err = tx.Commit(ctx); err != nil {
		return BalanceAnchorRepairResult{}, err
	}
	return result, nil
}

// resetSourceGapCheckpointEvaluation reports (dry run) or deletes (apply)
// the stale source_gap_frozen balance_checkpoint_evaluations row for the
// real checkpoint identified by (externalAccountID, checkpointBusinessID)
// -- checkpointBusinessID is the freeze's trigger_object_id, which
// evaluatePendingBalanceEvidenceTx populated from the checkpoint's own
// checkpoint_id business key (balance_reconciliation_checkpoints.checkpoint_id
// is unique per source instance, and every candidate here belongs to exactly
// one account's one source instance, so external_account_id+checkpoint_id
// identifies the row unambiguously). balance_checkpoint_evaluations carries
// no immutability trigger (unlike balance_carry_forward_evaluations), so
// this DELETE is the mechanism that lets the checkpoint's own row
// re-satisfy evaluatePendingBalanceEvidenceTx's pending query and be
// re-evaluated by the now-fixed code on the next projection job.
func resetSourceGapCheckpointEvaluation(ctx context.Context, tx pgx.Tx, externalAccountID, checkpointBusinessID string, apply bool) (int, error) {
	if !apply {
		var count int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM balance_checkpoint_evaluations bce
			JOIN balance_reconciliation_checkpoints c ON c.id=bce.checkpoint_id
			WHERE c.external_account_id=$1 AND c.checkpoint_id=$2
				AND bce.evaluation_status='source_gap_frozen'`,
			externalAccountID, checkpointBusinessID).Scan(&count); err != nil {
			return 0, err
		}
		return count, nil
	}
	command, err := tx.Exec(ctx, `
		DELETE FROM balance_checkpoint_evaluations
		WHERE evaluation_status='source_gap_frozen' AND checkpoint_id=(
			SELECT c.id FROM balance_reconciliation_checkpoints c
			WHERE c.external_account_id=$1 AND c.checkpoint_id=$2
		)`, externalAccountID, checkpointBusinessID)
	if err != nil {
		return 0, err
	}
	return int(command.RowsAffected()), nil
}

// applyBalanceAnchorFreezeResolution resolves one freeze with the exact
// column shape the manual admin resolution path (ResolveEligibilityFreeze)
// writes, mirroring applyPreAnchorFreezeResolution's own pattern exactly
// (including re-asserting the freeze's full original predicate in the
// UPDATE's own WHERE clause, aborting the whole repair run on any mismatch).
func applyBalanceAnchorFreezeResolution(ctx context.Context, tx pgx.Tx, freeze balanceAnchorFreezeCandidate, in BalanceAnchorRepairInput, actor AuditActor) error {
	var before struct {
		resolutionVersion int64
	}
	if err := tx.QueryRow(ctx, `SELECT resolution_version
		FROM eligibility_freezes WHERE id=$1`, freeze.id).Scan(&before.resolutionVersion); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `
		UPDATE eligibility_freezes SET status='resolved',resolved_at=now(),resolved_by=$1::uuid,
			resolution_evidence_hash=$2,resolution_evidence_ciphertext=$3,
			resolution_note_hash=$4,resolution_note_ciphertext=$5,
			resolution_version=resolution_version+1,updated_at=now()
		WHERE id=$6 AND status='open' AND freeze_reason='SOURCE_GAP'
			AND trigger_object_type IN ('balance_checkpoint','balance_carry_forward_proof')`,
		in.OperatorID, in.EvidenceHash, in.EvidenceCiphertext, in.NoteHash, in.NoteCiphertext, freeze.id)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errors.New("selected freeze no longer matches the repair predicate; refusing to apply")
	}
	return writeAudit(ctx, tx, actor, "eligibility.freeze.resolved", "eligibility_freeze", freeze.id,
		map[string]any{"status": "open", "resolution_version": before.resolutionVersion,
			"freeze_reason": "SOURCE_GAP", "trigger_object_type": freeze.triggerObjectType},
		map[string]any{"status": "resolved", "resolution_version": before.resolutionVersion + 1,
			"freeze_reason": "SOURCE_GAP", "trigger_object_type": freeze.triggerObjectType,
			"resolution_note": "POLICY_ANCHOR balance evidence self-healed by XM-INV-ANCHOR-BALANCE repair",
			"repair_tool":     "XM-INV-ANCHOR-BALANCE"})
}
