package postgresstore

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// BalanceBlipRepairInput drives RepairBalanceBlipEligibility (design
// XM-INV-BALANCE-BLIP): a versioned, human-approved lifecycle operation
// that reverses a synthesized UNKNOWN_POSITIVE credit that turned out to be
// a single transient positive checkpoint wrongly treated as self-healing
// (the bug evaluatePendingBalanceEvidenceTx's defer/confirm rule and
// boundary rule, this same slice, now prevent going forward), and every
// downstream UNKNOWN_NEGATIVE_BALANCE freeze that credit's permanent excess
// produced.
type BalanceBlipRepairInput struct {
	// Apply, when false (the default), runs every selection query and
	// reports what would change without writing anything (ROLLBACK at the
	// end regardless of what the queries found).
	Apply bool
	// OperatorID is required when Apply is true: the resolved_by UUID
	// recorded on every freeze this run resolves.
	OperatorID string
	// NoteCiphertext/NoteHash and EvidenceCiphertext/EvidenceHash are the
	// already-encrypted, fixed resolution note ("balance blip credit
	// reversed by XM-INV-BALANCE-BLIP repair") this run writes on every
	// freeze it resolves -- encryption happens in the caller, mirroring the
	// sibling repairs' identical split. Required when Apply is true;
	// ignored for a dry run.
	NoteCiphertext     []byte
	NoteHash           string
	EvidenceCiphertext []byte
	EvidenceHash       string
}

// BalanceBlipRepairAccount is one row of the repair's per-account summary
// table. CheckpointEvaluationsReset counts only balance_checkpoint
// triggers: balance_checkpoint_evaluations carries no immutability trigger,
// so its stale negative_frozen row can be deleted, letting
// evaluatePendingBalanceEvidenceTx's pending query treat the checkpoint as
// pending again and re-evaluate it with the fixed evaluator.
// balance_carry_forward_evaluations is immutable by design (migration
// 0014's balance_carry_forward_evaluations_immutable trigger, no
// exception, exactly as design XM-INV-ANCHOR-BALANCE's own repair already
// found) -- a balance_carry_forward_proof trigger's freeze is still
// resolved and counted in NegativeFreezesResolved, but its stale evaluation
// row is left exactly as it is, a permanent (and, once resolved, inert)
// historical record.
type BalanceBlipRepairAccount struct {
	ExternalAccountID          string
	SourceInstanceID           string
	BlipCreditsRemoved         int
	NegativeFreezesResolved    int
	CheckpointEvaluationsReset int
	Reactivated                bool
}

// BalanceBlipRepairResult is the repair's full summary, printable as-is by
// the CLI in both dry-run and apply modes.
type BalanceBlipRepairResult struct {
	Applied                         bool
	Accounts                        []BalanceBlipRepairAccount
	TotalBlipCreditsRemoved         int
	TotalNegativeFreezesResolved    int
	TotalCheckpointEvaluationsReset int
}

type blipCreditCandidate struct {
	id                string
	externalAccountID string
	sourceInstanceID  string
	externalCreditID  string
	serviceUnits      string
}

type blipPoisonedRow struct {
	kind              string // "checkpoint" or "carry"
	externalAccountID string
	businessKey       string // checkpoint_id or proof_key
	evaluationID      string
}

// blipOriginAsOfTx finds the as_of of the checkpoint or carry-forward proof
// that synthesized originID's UNKNOWN_POSITIVE credit -- i.e. the row whose
// own id is originID and whose own evaluation is positive_classified_non_cash
// (evaluatePendingBalanceEvidenceTx's synthesis branch always evaluates its
// origin item positive_classified_non_cash in the same pass it inserts the
// credit). found is false if neither table has such a row, which means this
// credit is not a recognizable blip-signature credit (skip conservatively).
func blipOriginAsOfTx(ctx context.Context, tx pgx.Tx, originID string) (asOf time.Time, found bool, err error) {
	err = tx.QueryRow(ctx, `
		SELECT checkpoint.as_of FROM balance_reconciliation_checkpoints checkpoint
		JOIN balance_checkpoint_evaluations evaluation ON evaluation.checkpoint_id=checkpoint.id
		WHERE checkpoint.id=$1 AND evaluation.evaluation_status='positive_classified_non_cash'`,
		originID).Scan(&asOf)
	if err == nil {
		return asOf, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, err
	}
	err = tx.QueryRow(ctx, `
		SELECT proof.as_of FROM balance_carry_forward_proofs proof
		JOIN balance_carry_forward_evaluations evaluation ON evaluation.proof_id=proof.id
		WHERE proof.id=$1 AND evaluation.evaluation_status='positive_classified_non_cash'`,
		originID).Scan(&asOf)
	if err == nil {
		return asOf, true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	return time.Time{}, false, err
}

// RepairBalanceBlipEligibility implements design XM-INV-BALANCE-BLIP's
// production repair. Everything below runs inside one transaction: dry-run
// rolls it back unconditionally after gathering the same counts apply would
// report; apply commits only if every selected row still matches its
// predicate at UPDATE/DELETE time (re-asserted in each statement's own
// WHERE clause) -- any mismatch aborts the whole run with nothing written,
// matching the sibling repairs' own "refuses to apply if any selected row
// no longer matches" contract.
//
// Detection, precise by construction: a credit_kind='UNKNOWN_POSITIVE' row
// is, by construction, only ever written by evaluatePendingBalanceEvidenceTx's
// self-healing synthesis (the sole INSERT site for this credit_kind in this
// package) -- never a real, externally-sourced fact. Its
// external_credit_id ("unknown-positive:"+the originating checkpoint's or
// proof's own row id) identifies exactly which evaluation synthesized it.
// The blip signature: that origin evaluated positive_classified_non_cash,
// and one or more *later* checkpoint/proof evaluations for the same
// account are negative_frozen by exactly the negation of the credit's own
// amount -- the precise, permanent shape a wrongly self-healed single
// checkpoint leaves behind (every evaluation after it differs by the same
// constant until the account froze), and a shape no other cause produces:
// a real credit or a real matching balance movement would not leave a
// *constant*, exactly-negated difference across every later checkpoint.
// Not scoped to any particular bootstrap_kind (POLICY_ANCHOR or legacy):
// the bug this reverses was in the evaluator's own defer/boundary logic,
// not in any POLICY_ANCHOR-specific code path.
func (s *Store) RepairBalanceBlipEligibility(ctx context.Context, in BalanceBlipRepairInput, actor AuditActor) (BalanceBlipRepairResult, error) {
	if in.Apply {
		if !eligibilityUUIDPattern.MatchString(strings.TrimSpace(in.OperatorID)) {
			return BalanceBlipRepairResult{}, errors.New("a valid operator UUID is required to apply")
		}
		if !hexHashPattern.MatchString(in.NoteHash) || !hexHashPattern.MatchString(in.EvidenceHash) ||
			len(in.NoteCiphertext) < 16 || len(in.NoteCiphertext) > 8192 ||
			len(in.EvidenceCiphertext) < 16 || len(in.EvidenceCiphertext) > 8192 {
			return BalanceBlipRepairResult{}, errors.New("encrypted resolution note and evidence are required to apply")
		}
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return BalanceBlipRepairResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if in.Apply {
		// Transaction-local only (set_config(...,true)), never persisted --
		// see migration 0019's doc comment. Lifts source_credit_events'
		// immutability for exactly a credit_kind='UNKNOWN_POSITIVE' DELETE,
		// nothing else.
		if _, execErr := tx.Exec(ctx, `SELECT set_config('invoice.balance_blip_repair_delete','on',true)`); execErr != nil {
			return BalanceBlipRepairResult{}, execErr
		}
	}

	rows, err := tx.Query(ctx, `
		SELECT credit.id,credit.external_account_id::text,eas.source_instance_id::text,
			credit.external_credit_id,credit.service_units::text
		FROM source_credit_events credit
		JOIN source_account_eligibility_state eas ON eas.external_account_id=credit.external_account_id
		WHERE credit.credit_kind='UNKNOWN_POSITIVE'
		ORDER BY credit.external_account_id,credit.id
		FOR UPDATE OF credit`)
	if err != nil {
		return BalanceBlipRepairResult{}, err
	}
	candidates := make([]blipCreditCandidate, 0)
	for rows.Next() {
		var c blipCreditCandidate
		if err = rows.Scan(&c.id, &c.externalAccountID, &c.sourceInstanceID, &c.externalCreditID, &c.serviceUnits); err != nil {
			rows.Close()
			return BalanceBlipRepairResult{}, err
		}
		candidates = append(candidates, c)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return BalanceBlipRepairResult{}, err
	}
	rows.Close()

	summary := map[string]*BalanceBlipRepairAccount{}
	accountIDs := make([]string, 0)
	ensureAccount := func(externalAccountID, sourceInstanceID string) *BalanceBlipRepairAccount {
		if existing, ok := summary[externalAccountID]; ok {
			return existing
		}
		row := &BalanceBlipRepairAccount{ExternalAccountID: externalAccountID, SourceInstanceID: sourceInstanceID}
		summary[externalAccountID] = row
		accountIDs = append(accountIDs, externalAccountID)
		return row
	}

	result := BalanceBlipRepairResult{Applied: in.Apply}
	for _, c := range candidates {
		const prefix = "unknown-positive:"
		if !strings.HasPrefix(c.externalCreditID, prefix) {
			continue
		}
		originID := strings.TrimPrefix(c.externalCreditID, prefix)

		originAsOf, found, originErr := blipOriginAsOfTx(ctx, tx, originID)
		if originErr != nil {
			return BalanceBlipRepairResult{}, originErr
		}
		if !found {
			// Not a recognizable blip-signature credit (its origin
			// evaluation is gone or was never positive_classified_non_cash)
			// -- skip conservatively rather than guess.
			continue
		}

		poisoned := make([]blipPoisonedRow, 0)
		checkpointRows, queryErr := tx.Query(ctx, `
			SELECT c.checkpoint_id,bce.id
			FROM balance_checkpoint_evaluations bce
			JOIN balance_reconciliation_checkpoints c ON c.id=bce.checkpoint_id
			WHERE c.external_account_id=$1 AND bce.evaluation_status='negative_frozen'
				AND bce.difference_service_units=(-1)*$2::numeric AND c.as_of>=$3
			ORDER BY c.as_of`, c.externalAccountID, c.serviceUnits, originAsOf)
		if queryErr != nil {
			return BalanceBlipRepairResult{}, queryErr
		}
		for checkpointRows.Next() {
			var row blipPoisonedRow
			row.kind = "checkpoint"
			if scanErr := checkpointRows.Scan(&row.businessKey, &row.evaluationID); scanErr != nil {
				checkpointRows.Close()
				return BalanceBlipRepairResult{}, scanErr
			}
			row.externalAccountID = c.externalAccountID
			poisoned = append(poisoned, row)
		}
		if scanErr := checkpointRows.Err(); scanErr != nil {
			checkpointRows.Close()
			return BalanceBlipRepairResult{}, scanErr
		}
		checkpointRows.Close()

		proofRows, queryErr := tx.Query(ctx, `
			SELECT p.proof_key,bcfe.id
			FROM balance_carry_forward_evaluations bcfe
			JOIN balance_carry_forward_proofs p ON p.id=bcfe.proof_id
			WHERE p.external_account_id=$1 AND bcfe.evaluation_status='negative_frozen'
				AND bcfe.difference_service_units=(-1)*$2::numeric AND p.as_of>=$3
			ORDER BY p.as_of`, c.externalAccountID, c.serviceUnits, originAsOf)
		if queryErr != nil {
			return BalanceBlipRepairResult{}, queryErr
		}
		for proofRows.Next() {
			var row blipPoisonedRow
			row.kind = "carry"
			if scanErr := proofRows.Scan(&row.businessKey, &row.evaluationID); scanErr != nil {
				proofRows.Close()
				return BalanceBlipRepairResult{}, scanErr
			}
			row.externalAccountID = c.externalAccountID
			poisoned = append(poisoned, row)
		}
		if scanErr := proofRows.Err(); scanErr != nil {
			proofRows.Close()
			return BalanceBlipRepairResult{}, scanErr
		}
		proofRows.Close()

		if len(poisoned) == 0 {
			// The credit exists but nothing downstream ever froze negative
			// because of it -- not this bug's signature (could be a
			// legitimate self-heal that simply has not been contradicted by
			// anything yet). Leave it alone.
			continue
		}

		row := ensureAccount(c.externalAccountID, c.sourceInstanceID)
		for _, p := range poisoned {
			objectType := "balance_checkpoint"
			if p.kind == "carry" {
				objectType = "balance_carry_forward_proof"
			}
			var freezeID string
			var resolutionVersion int64
			if err = tx.QueryRow(ctx, `
				SELECT id,resolution_version FROM eligibility_freezes
				WHERE external_account_id=$1 AND status='open' AND freeze_reason='UNKNOWN_NEGATIVE_BALANCE'
					AND trigger_object_type=$2 AND trigger_object_id=$3
				FOR UPDATE`, p.externalAccountID, objectType, p.businessKey).Scan(&freezeID, &resolutionVersion); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					// The evaluation is stuck negative_frozen but its
					// freeze was already resolved by something else (e.g.
					// a prior partial repair run) -- reset/leave the
					// evaluation as appropriate below without a second
					// freeze resolution.
					err = nil
				} else {
					return BalanceBlipRepairResult{}, err
				}
			}
			if in.Apply {
				if p.kind == "checkpoint" {
					if _, execErr := tx.Exec(ctx, `
						DELETE FROM balance_checkpoint_evaluations
						WHERE id=$1 AND evaluation_status='negative_frozen'`, p.evaluationID); execErr != nil {
						return BalanceBlipRepairResult{}, execErr
					}
				}
				if freezeID != "" {
					if resolveErr := applyBalanceBlipFreezeResolution(ctx, tx, freezeID, c.sourceInstanceID,
						resolutionVersion, objectType, in, actor); resolveErr != nil {
						return BalanceBlipRepairResult{}, resolveErr
					}
				}
			}
			row.NegativeFreezesResolved++
			result.TotalNegativeFreezesResolved++
			if p.kind == "checkpoint" {
				row.CheckpointEvaluationsReset++
				result.TotalCheckpointEvaluationsReset++
			}
		}

		if in.Apply {
			// consumption_allocations is derived/rebuildable (reprojectEligibilityTx
			// freely deletes and reinserts it for the whole account on every
			// projection job); clearing only this credit's own allocations
			// satisfies the FK the credit's own DELETE below would
			// otherwise hit, without disturbing any other lot/credit's rows
			// -- the account's next projection job (queued below) rebuilds
			// the rest from scratch as it always does.
			if _, execErr := tx.Exec(ctx, `DELETE FROM consumption_allocations WHERE credit_event_id=$1`, c.id); execErr != nil {
				return BalanceBlipRepairResult{}, execErr
			}
			command, execErr := tx.Exec(ctx, `
				DELETE FROM source_credit_events WHERE id=$1 AND credit_kind='UNKNOWN_POSITIVE'`, c.id)
			if execErr != nil {
				return BalanceBlipRepairResult{}, execErr
			}
			if command.RowsAffected() != 1 {
				return BalanceBlipRepairResult{}, errors.New("selected blip credit no longer matches the repair predicate; refusing to apply")
			}
			if auditErr := writeAudit(ctx, tx, actor, "eligibility.balance_blip.credit_removed", "source_credit_event", c.id,
				nil, map[string]any{"external_account_id": c.externalAccountID, "service_units": c.serviceUnits,
					"repair_tool": "XM-INV-BALANCE-BLIP"}); auditErr != nil {
				return BalanceBlipRepairResult{}, auditErr
			}
		}
		row.BlipCreditsRemoved++
		result.TotalBlipCreditsRemoved++
	}

	if in.Apply {
		for _, accountID := range accountIDs {
			var remaining int
			if scanErr := tx.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes
				WHERE external_account_id=$1 AND status='open'`, accountID).Scan(&remaining); scanErr != nil {
				return BalanceBlipRepairResult{}, scanErr
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
				return BalanceBlipRepairResult{}, execErr
			}
			command, execErr := tx.Exec(ctx, `UPDATE source_account_eligibility_state
				SET eligibility_status='active',projection_version=projection_version+1,updated_at=now()
				WHERE external_account_id=$1 AND eligibility_status='frozen'`, accountID)
			if execErr != nil {
				return BalanceBlipRepairResult{}, execErr
			}
			if command.RowsAffected() == 1 {
				row.Reactivated = true
			}
			if auditErr := writeAudit(ctx, tx, actor, "eligibility.balance_blip.repaired",
				"external_account", accountID, nil, map[string]any{
					"blip_credits_removed":         row.BlipCreditsRemoved,
					"negative_freezes_resolved":    row.NegativeFreezesResolved,
					"checkpoint_evaluations_reset": row.CheckpointEvaluationsReset,
					"reactivated":                  row.Reactivated,
					"repair_tool":                  "XM-INV-BALANCE-BLIP",
				}); auditErr != nil {
				return BalanceBlipRepairResult{}, auditErr
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
		return BalanceBlipRepairResult{}, err
	}
	return result, nil
}

// applyBalanceBlipFreezeResolution resolves one freeze with the exact
// column shape the manual admin resolution path (ResolveEligibilityFreeze)
// writes, mirroring the sibling repairs' own pattern exactly (including
// re-asserting the freeze's full original predicate in the UPDATE's own
// WHERE clause, aborting the whole repair run on any mismatch).
//
// XM-INV-DEAD-CONTAINMENT: sourceInstanceID is on this signature only so this
// door can ask assertNoBlockingDeadEventForFreezeTx like every other one. An
// UNKNOWN_NEGATIVE_BALANCE freeze carries the balance evidence's own
// source_revision_hash, so it too can be the freeze containing a dead event.
func applyBalanceBlipFreezeResolution(ctx context.Context, tx pgx.Tx, freezeID, sourceInstanceID string, resolutionVersion int64,
	triggerObjectType string, in BalanceBlipRepairInput, actor AuditActor) error {
	if err := assertNoBlockingDeadEventForFreezeTx(ctx, tx, freezeID, sourceInstanceID); err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `
		UPDATE eligibility_freezes SET status='resolved',resolved_at=now(),resolved_by=$1::uuid,
			resolution_evidence_hash=$2,resolution_evidence_ciphertext=$3,
			resolution_note_hash=$4,resolution_note_ciphertext=$5,
			resolution_version=resolution_version+1,updated_at=now()
		WHERE id=$6 AND status='open' AND freeze_reason='UNKNOWN_NEGATIVE_BALANCE'
			AND trigger_object_type=$7`,
		in.OperatorID, in.EvidenceHash, in.EvidenceCiphertext, in.NoteHash, in.NoteCiphertext,
		freezeID, triggerObjectType)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errors.New("selected freeze no longer matches the repair predicate; refusing to apply")
	}
	return writeAudit(ctx, tx, actor, "eligibility.freeze.resolved", "eligibility_freeze", freezeID,
		map[string]any{"status": "open", "resolution_version": resolutionVersion,
			"freeze_reason": "UNKNOWN_NEGATIVE_BALANCE", "trigger_object_type": triggerObjectType},
		map[string]any{"status": "resolved", "resolution_version": resolutionVersion + 1,
			"freeze_reason": "UNKNOWN_NEGATIVE_BALANCE", "trigger_object_type": triggerObjectType,
			"resolution_note": "balance blip credit reversed by XM-INV-BALANCE-BLIP repair",
			"repair_tool":     "XM-INV-BALANCE-BLIP"})
}
