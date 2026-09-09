package postgresstore

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// PreAnchorUsageRepairInput drives RepairPreAnchorUsageEligibility (design
// XM-INV-PREANCHOR-USAGE, part 3): a versioned, human-approved lifecycle
// operation that cleans up the 2026-09-02 incident's SOURCE_GAP/EVENT_DEAD
// freezes and stuck source_ingest_events, now that the projection rule
// itself no longer produces them for POLICY_ANCHOR accounts.
type PreAnchorUsageRepairInput struct {
	// Apply, when false (the default), runs every selection query and
	// reports what would change without writing anything (ROLLBACK at the
	// end regardless of what the queries found).
	Apply bool
	// OperatorID is required when Apply is true: the resolved_by UUID
	// recorded on every freeze this run resolves.
	OperatorID string
	// NoteCiphertext/NoteHash and EvidenceCiphertext/EvidenceHash are the
	// already-encrypted, fixed resolution note ("pre-anchor usage fact
	// skipped by XM-INV-PREANCHOR-USAGE repair") this run writes on every
	// freeze it resolves -- encryption happens in the caller, mirroring
	// ReviewPaymentCandidateInput's PaymentEvidence{Hash,Ciphertext} split
	// (this package has no securefields dependency). Required when Apply is
	// true; ignored for a dry run.
	NoteCiphertext     []byte
	NoteHash           string
	EvidenceCiphertext []byte
	EvidenceHash       string
}

// PreAnchorUsageRepairAccount is one row of the repair's per-account summary
// table.
type PreAnchorUsageRepairAccount struct {
	ExternalAccountID        string
	SourceInstanceID         string
	SourceGapFreezesResolved int
	EventDeadFreezesResolved int
	EventsRequeued           int
	Reactivated              bool
}

// PreAnchorUsageRepairResult is the repair's full summary, printable as-is
// by the CLI in both dry-run and apply modes.
type PreAnchorUsageRepairResult struct {
	Applied                       bool
	Accounts                      []PreAnchorUsageRepairAccount
	TotalSourceGapFreezesResolved int
	TotalEventDeadFreezesResolved int
	TotalEventsRequeued           int
}

type preAnchorFreezeCandidate struct {
	id                 string
	externalAccountID  string
	sourceInstanceID   string
	sourceRevisionHash string
}

type preAnchorIngestCandidate struct {
	sourceInstanceID string
	streamID         string
	eventID          string
	payloadHash      string
}

// RepairPreAnchorUsageEligibility implements design XM-INV-PREANCHOR-USAGE
// part 3. Everything below runs inside one transaction: dry-run rolls it
// back unconditionally after gathering the same counts apply would report;
// apply commits only if every selected row still matches its predicate at
// UPDATE time (re-asserted in each UPDATE's own WHERE clause) -- any
// mismatch aborts the whole run with nothing written, per the task's
// "refuses to apply if any selected freeze does not match the predicate."
//
// Scope, precise by construction (see docs/handoffs/XM-INV-PREANCHOR-USAGE.md
// for the full derivation):
//   - SOURCE_GAP freezes with trigger_object_type IN ('usage','credit') are
//     produced by exactly one code path in this package (observeEligibilityFact),
//     so restricting to accounts whose bootstrap_kind is POLICY_ANCHOR
//     identifies exactly the accounts this incident's design gap hit -- a
//     legacy (SIGNED_CUTOVER/POST_CUTOVER_REPLAY) account's SOURCE_GAP freeze
//     with the same object types is a real gap and is never touched.
//   - EVENT_DEAD freezes are a general mechanism (design XM-INV-POLICY-ANCHOR
//     2.5) that can freeze for reasons unrelated to this incident, so those
//     are scoped further: only accounts already identified via a SOURCE_GAP
//     freeze above, and only EVENT_DEAD freezes correlated (via
//     source_revision_hash, the same content-hash link
//     MarkSourceEventFailed's own dead-branch correlation uses) to a
//     specific ingest event's payload_hash.
//   - source_ingest_events rows to requeue are identified the same way: dead
//     or failed usage_event/credit_event rows whose payload_hash matches a
//     source_revision_hash collected from a freeze this run is resolving.
//     This is why requeuing needs no separate account-attribution step (the
//     ingest layer never stores one in plaintext) -- it rides on the
//     freezes' own correlation.
func (s *Store) RepairPreAnchorUsageEligibility(ctx context.Context, in PreAnchorUsageRepairInput, actor AuditActor) (PreAnchorUsageRepairResult, error) {
	if in.Apply {
		if !eligibilityUUIDPattern.MatchString(strings.TrimSpace(in.OperatorID)) {
			return PreAnchorUsageRepairResult{}, errors.New("a valid operator UUID is required to apply")
		}
		if !hexHashPattern.MatchString(in.NoteHash) || !hexHashPattern.MatchString(in.EvidenceHash) ||
			len(in.NoteCiphertext) < 16 || len(in.NoteCiphertext) > 8192 ||
			len(in.EvidenceCiphertext) < 16 || len(in.EvidenceCiphertext) > 8192 {
			return PreAnchorUsageRepairResult{}, errors.New("encrypted resolution note and evidence are required to apply")
		}
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return PreAnchorUsageRepairResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	gapRows, err := tx.Query(ctx, `
		SELECT ef.id,ef.external_account_id::text,eas.source_instance_id::text,COALESCE(ef.source_revision_hash,'')
		FROM eligibility_freezes ef
		JOIN source_account_eligibility_state eas ON eas.external_account_id=ef.external_account_id
		WHERE ef.status='open' AND ef.freeze_reason='SOURCE_GAP' AND ef.trigger_object_type IN ('usage','credit')
			AND eas.bootstrap_kind='POLICY_ANCHOR'
		ORDER BY ef.external_account_id,ef.id
		FOR UPDATE OF ef`)
	if err != nil {
		return PreAnchorUsageRepairResult{}, err
	}
	gapFreezes, err := scanPreAnchorFreezeCandidates(gapRows)
	if err != nil {
		return PreAnchorUsageRepairResult{}, err
	}

	accountIDs := make([]string, 0)
	accountSource := map[string]string{}
	revisionToAccount := map[string]string{}
	seenAccount := map[string]bool{}
	for _, freeze := range gapFreezes {
		if !seenAccount[freeze.externalAccountID] {
			seenAccount[freeze.externalAccountID] = true
			accountIDs = append(accountIDs, freeze.externalAccountID)
			accountSource[freeze.externalAccountID] = freeze.sourceInstanceID
		}
		if freeze.sourceRevisionHash != "" {
			revisionToAccount[freeze.sourceRevisionHash] = freeze.externalAccountID
		}
	}

	deadFreezes := make([]preAnchorFreezeCandidate, 0)
	if len(accountIDs) > 0 {
		deadRows, queryErr := tx.Query(ctx, `
			SELECT ef.id,ef.external_account_id::text,eas.source_instance_id::text,COALESCE(ef.source_revision_hash,'')
			FROM eligibility_freezes ef
			JOIN source_account_eligibility_state eas ON eas.external_account_id=ef.external_account_id
			WHERE ef.status='open' AND ef.freeze_reason='EVENT_DEAD' AND ef.trigger_object_type IN ('usage_event','credit_event')
				AND ef.external_account_id=ANY($1::uuid[])
			ORDER BY ef.external_account_id,ef.id
			FOR UPDATE OF ef`, accountIDs)
		if queryErr != nil {
			return PreAnchorUsageRepairResult{}, queryErr
		}
		deadFreezes, err = scanPreAnchorFreezeCandidates(deadRows)
		if err != nil {
			return PreAnchorUsageRepairResult{}, err
		}
		for _, freeze := range deadFreezes {
			if freeze.sourceRevisionHash != "" {
				revisionToAccount[freeze.sourceRevisionHash] = freeze.externalAccountID
			}
		}
	}

	ingestCandidates := make([]preAnchorIngestCandidate, 0)
	if len(revisionToAccount) > 0 {
		// source_ingest_events carries no account column (the ingest
		// envelope's payload is opaque ciphertext until an application-layer
		// attempt decrypts it) -- payload_hash is the only correlatable key,
		// the same one MarkSourceEventFailed's own dead-branch correlation
		// already relies on. Scoped to dead/failed usage_event/credit_event
		// rows only; every match is then re-verified against
		// revisionToAccount in Go before being treated as a candidate.
		ingestRows, queryErr := tx.Query(ctx, `
			SELECT source_instance_id::text,stream_id,event_id::text,payload_hash
			FROM source_ingest_events
			WHERE processing_status IN ('dead','failed') AND entity_type IN ('usage_event','credit_event')
			ORDER BY source_instance_id,stream_id,event_id
			FOR UPDATE`)
		if queryErr != nil {
			return PreAnchorUsageRepairResult{}, queryErr
		}
		defer ingestRows.Close()
		for ingestRows.Next() {
			var c preAnchorIngestCandidate
			if scanErr := ingestRows.Scan(&c.sourceInstanceID, &c.streamID, &c.eventID, &c.payloadHash); scanErr != nil {
				return PreAnchorUsageRepairResult{}, scanErr
			}
			if _, ok := revisionToAccount[c.payloadHash]; ok {
				ingestCandidates = append(ingestCandidates, c)
			}
		}
		if rowsErr := ingestRows.Err(); rowsErr != nil {
			return PreAnchorUsageRepairResult{}, rowsErr
		}
	}

	summary := map[string]*PreAnchorUsageRepairAccount{}
	ensureAccount := func(id string) *PreAnchorUsageRepairAccount {
		if existing, ok := summary[id]; ok {
			return existing
		}
		row := &PreAnchorUsageRepairAccount{ExternalAccountID: id, SourceInstanceID: accountSource[id]}
		summary[id] = row
		return row
	}

	result := PreAnchorUsageRepairResult{Applied: in.Apply}
	// XM-INV-DEAD-CONTAINMENT: the requeue runs first, ahead of both
	// resolution loops. This tool always did both halves in one transaction,
	// so the order was previously immaterial -- nothing outside the
	// transaction could observe an intermediate state. It matters now because
	// applyPreAnchorFreezeResolution asks assertNoBlockingDeadEventForFreezeTx
	// like every other door that resolves a freeze, and that guard reads the
	// event's status inside this same transaction. Requeuing first is not a
	// way of slipping past the guard: it is the ordering the guard exists to
	// require -- repair the event, then release the freeze that was
	// containing it. A correlated event this tool does not requeue (anything
	// that is not a dead/failed usage_event/credit_event) still stops the run,
	// which is the correct answer rather than an inconvenience.
	for _, candidate := range ingestCandidates {
		accountID := revisionToAccount[candidate.payloadHash]
		if in.Apply {
			command, execErr := tx.Exec(ctx, `
				UPDATE source_ingest_events SET processing_status='queued',attempt_count=0,
					processing_error=NULL,next_attempt_at=now(),lease_token=NULL,lease_expires_at=NULL,
					updated_at=now()
				WHERE source_instance_id=$1 AND stream_id=$2 AND event_id=$3
					AND processing_status IN ('dead','failed') AND entity_type IN ('usage_event','credit_event')`,
				candidate.sourceInstanceID, candidate.streamID, candidate.eventID)
			if execErr != nil {
				return PreAnchorUsageRepairResult{}, execErr
			}
			if command.RowsAffected() != 1 {
				return PreAnchorUsageRepairResult{}, errors.New("selected source_ingest_events row no longer matches the repair predicate; refusing to apply")
			}
			if auditErr := writeAudit(ctx, tx, actor, "source_ingest_event.repair_requeued", "source_ingest_event",
				candidate.eventID, nil, map[string]any{"source_instance_id": candidate.sourceInstanceID,
					"stream_id": candidate.streamID, "external_account_id": accountID,
					"repair_tool": "XM-INV-PREANCHOR-USAGE"}); auditErr != nil {
				return PreAnchorUsageRepairResult{}, auditErr
			}
		}
		ensureAccount(accountID).EventsRequeued++
		result.TotalEventsRequeued++
	}
	for _, freeze := range gapFreezes {
		if in.Apply {
			if resolveErr := applyPreAnchorFreezeResolution(ctx, tx, freeze, in, actor); resolveErr != nil {
				return PreAnchorUsageRepairResult{}, resolveErr
			}
		}
		ensureAccount(freeze.externalAccountID).SourceGapFreezesResolved++
		result.TotalSourceGapFreezesResolved++
	}
	for _, freeze := range deadFreezes {
		if in.Apply {
			if resolveErr := applyPreAnchorFreezeResolution(ctx, tx, freeze, in, actor); resolveErr != nil {
				return PreAnchorUsageRepairResult{}, resolveErr
			}
		}
		row := ensureAccount(freeze.externalAccountID)
		if row.SourceInstanceID == "" {
			row.SourceInstanceID = accountSource[freeze.externalAccountID]
		}
		row.EventDeadFreezesResolved++
		result.TotalEventDeadFreezesResolved++
	}

	if in.Apply {
		for _, accountID := range accountIDs {
			var remaining int
			if scanErr := tx.QueryRow(ctx, `SELECT count(*) FROM eligibility_freezes
				WHERE external_account_id=$1 AND status='open'`, accountID).Scan(&remaining); scanErr != nil {
				return PreAnchorUsageRepairResult{}, scanErr
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
				return PreAnchorUsageRepairResult{}, execErr
			}
			command, execErr := tx.Exec(ctx, `UPDATE source_account_eligibility_state
				SET eligibility_status='active',projection_version=projection_version+1,updated_at=now()
				WHERE external_account_id=$1 AND eligibility_status='frozen'`, accountID)
			if execErr != nil {
				return PreAnchorUsageRepairResult{}, execErr
			}
			if command.RowsAffected() == 1 {
				row.Reactivated = true
			}
			if auditErr := writeAudit(ctx, tx, actor, "eligibility.policy_anchor.pre_anchor_usage_repaired",
				"external_account", accountID, nil, map[string]any{
					"source_gap_freezes_resolved": row.SourceGapFreezesResolved,
					"event_dead_freezes_resolved": row.EventDeadFreezesResolved,
					"events_requeued":             row.EventsRequeued,
					"reactivated":                 row.Reactivated,
					"repair_tool":                 "XM-INV-PREANCHOR-USAGE",
				}); auditErr != nil {
				return PreAnchorUsageRepairResult{}, auditErr
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
		return PreAnchorUsageRepairResult{}, err
	}
	return result, nil
}

func scanPreAnchorFreezeCandidates(rows pgx.Rows) ([]preAnchorFreezeCandidate, error) {
	defer rows.Close()
	candidates := make([]preAnchorFreezeCandidate, 0)
	for rows.Next() {
		var c preAnchorFreezeCandidate
		if err := rows.Scan(&c.id, &c.externalAccountID, &c.sourceInstanceID, &c.sourceRevisionHash); err != nil {
			return nil, err
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

// applyPreAnchorFreezeResolution resolves one freeze with the exact column
// shape the manual admin resolution path (ResolveEligibilityFreeze) writes
// (status/resolved_at/resolved_by/resolution_evidence_hash/
// resolution_evidence_ciphertext/resolution_note_hash/
// resolution_note_ciphertext/resolution_version) and an
// "eligibility.freeze.resolved" audit row, plus the repair-specific note in
// the audit's after payload. The freeze's full original predicate
// (status='open', freeze_reason, trigger_object_type) is re-asserted in the
// UPDATE's own WHERE clause -- a RowsAffected()!=1 here means the row no
// longer matches what was selected (e.g. resolved by something else between
// SELECT and UPDATE, impossible within this single serializable transaction
// today, but checked unconditionally rather than assumed) and aborts the
// whole repair run.
//
// XM-INV-DEAD-CONTAINMENT: this door is the one that always did establish the
// right ordering by itself, since it requeues the correlated events in the
// same transaction. It still asks assertNoBlockingDeadEventForFreezeTx, for
// two reasons: a survey-by-reading is what left three other doors unguarded
// for a whole slice, and "this one is safe because of something it does
// elsewhere in the function" is exactly the kind of claim that stops being
// true without anything going red. The requeue now runs before both
// resolution loops so this call sees the repaired events.
func applyPreAnchorFreezeResolution(ctx context.Context, tx pgx.Tx, freeze preAnchorFreezeCandidate, in PreAnchorUsageRepairInput, actor AuditActor) error {
	if err := assertNoBlockingDeadEventForFreezeTx(ctx, tx, freeze.id, freeze.sourceInstanceID); err != nil {
		return err
	}
	var before struct {
		freezeReason, triggerObjectType string
		resolutionVersion               int64
	}
	if err := tx.QueryRow(ctx, `SELECT freeze_reason,trigger_object_type,resolution_version
		FROM eligibility_freezes WHERE id=$1`, freeze.id).Scan(
		&before.freezeReason, &before.triggerObjectType, &before.resolutionVersion); err != nil {
		return err
	}
	expectedObjectTypes := "('usage','credit')"
	if before.freezeReason == "EVENT_DEAD" {
		expectedObjectTypes = "('usage_event','credit_event')"
	}
	command, err := tx.Exec(ctx, `
		UPDATE eligibility_freezes SET status='resolved',resolved_at=now(),resolved_by=$1::uuid,
			resolution_evidence_hash=$2,resolution_evidence_ciphertext=$3,
			resolution_note_hash=$4,resolution_note_ciphertext=$5,
			resolution_version=resolution_version+1,updated_at=now()
		WHERE id=$6 AND status='open' AND freeze_reason=$7 AND trigger_object_type IN `+expectedObjectTypes,
		in.OperatorID, in.EvidenceHash, in.EvidenceCiphertext, in.NoteHash, in.NoteCiphertext,
		freeze.id, before.freezeReason)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errors.New("selected freeze no longer matches the repair predicate; refusing to apply")
	}
	return writeAudit(ctx, tx, actor, "eligibility.freeze.resolved", "eligibility_freeze", freeze.id,
		map[string]any{"status": "open", "resolution_version": before.resolutionVersion,
			"freeze_reason": before.freezeReason},
		map[string]any{"status": "resolved", "resolution_version": before.resolutionVersion + 1,
			"freeze_reason": before.freezeReason, "resolution_note": "pre-anchor usage fact skipped by XM-INV-PREANCHOR-USAGE repair",
			"repair_tool": "XM-INV-PREANCHOR-USAGE"})
}
