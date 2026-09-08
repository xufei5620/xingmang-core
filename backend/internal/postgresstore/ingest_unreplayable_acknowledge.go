package postgresstore

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ingestUnreplayableMarker is the processing_error this disposition writes.
//
// The shape is copied deliberately from RequeueSourceDependency's own
// PRE_POLICY_SKIPPED path (source_sync.go): processing_status='processed'
// with processed_at set, the lease and dependency columns cleared, and a
// marker in processing_error naming why. That precedent is the whole reason
// this operation is sound rather than a fabrication -- it does not claim the
// fact was applied, it records that it never will be. Anyone reviewing this
// should only have to check that it matches PRE_POLICY_SKIPPED, not judge a
// new idea.
const ingestUnreplayableMarker = "UNREPLAYABLE_BINDING"

// ingestUnreplayableAuditAction is the per-event audit action. One row per
// acknowledged event, never a run summary: this operation writes off customer
// data, so every single row it touches must be individually attributable.
const ingestUnreplayableAuditAction = "source_ingest_event.unreplayable_acknowledged"

// IngestAcknowledgeUnreplayableInput drives AcknowledgeUnreplayableIngestEvent
// (XM-INV-DEAD-REQUEUE follow-up): the terminal disposition for a dead
// source_ingest_events row whose fact can never be replayed, because it holds
// no binding verifyFactBatchContextTx would accept and -- for a balance
// checkpoint -- can never acquire one (agent event ids fold the payload hash
// in, so any later scan produces a different event entirely).
//
// Such a row would otherwise stay 'dead' forever, and
// validateSourceIngestRuntimeReadiness fails on Dead>0, so /readyz would stay
// 503 permanently even after the account's freezes are resolved through the
// normal admin path. Deleting the row is not an option: the
// source_economic_scan_cycle_events foreign key is ON DELETE RESTRICT, and
// deleting would destroy the evidence of what was lost. This gives the row a
// non-dead terminal state without asserting anything untrue.
//
// It is deliberately its own operation rather than a mode of
// RepairIngestRequeueDead. Acknowledging lost customer data is a different
// decision from retrying, and it must not be reachable by adding a flag to a
// requeue command.
type IngestAcknowledgeUnreplayableInput struct {
	// Apply, when false (the default), reports what would happen and commits
	// nothing (the transaction is rolled back).
	Apply bool
	// OperatorID is required when Apply is true: the actor on the audit event.
	OperatorID string
	// EventID is required, always. There is deliberately no "acknowledge
	// every unreplayable event" mode: each one is a separate admission that a
	// specific piece of customer data is gone, and is reviewed on its own.
	EventID string
}

// IngestAcknowledgeUnreplayableResult is the operation's summary. The event
// detail reuses IngestRequeueDeadRepairEvent so the CLI prints the same
// evidence block (replay binding, other bindings, open freezes, catch-up key)
// an operator reads before a requeue -- the decision needs the same facts.
type IngestAcknowledgeUnreplayableResult struct {
	Applied bool
	Event   IngestRequeueDeadRepairEvent
	// Acknowledged is true once the row was (apply) or would be (dry run)
	// moved to processing_status='processed' with the marker.
	Acknowledged bool
}

// AcknowledgeUnreplayableIngestEvent marks one dead, unreplayable ingest
// event as permanently not applicable.
//
// It refuses unless the event is genuinely unreplayable, re-deriving that
// through the same ingestRequeueDeadReplayBindingTx the requeue tool uses:
// if any binding verifyFactBatchContextTx would accept exists, the answer is
// to requeue the event, not to write its fact off. That guard is what keeps
// this from being a "mark anything processed" button.
func (s *Store) AcknowledgeUnreplayableIngestEvent(ctx context.Context, in IngestAcknowledgeUnreplayableInput,
	actor AuditActor) (IngestAcknowledgeUnreplayableResult, error) {
	eventID := strings.TrimSpace(in.EventID)
	if !eligibilityUUIDPattern.MatchString(eventID) {
		return IngestAcknowledgeUnreplayableResult{}, errors.New("an event id is required: this disposition is never applied in bulk")
	}
	if in.Apply && !eligibilityUUIDPattern.MatchString(strings.TrimSpace(in.OperatorID)) {
		return IngestAcknowledgeUnreplayableResult{}, errors.New("a valid operator UUID is required to apply")
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return IngestAcknowledgeUnreplayableResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	row := IngestRequeueDeadRepairEvent{EventID: eventID}
	var processingError, catchupKey *string
	err = tx.QueryRow(ctx, `
		SELECT source_instance_id::text,stream_id,entity_type,operation,payload_hash,
			attempt_count,processing_error,updated_at,created_at,catchup_key_hmac
		FROM source_ingest_events
		WHERE event_id=$1 AND processing_status='dead'
		FOR UPDATE`, eventID).Scan(&row.SourceInstanceID, &row.StreamID, &row.EntityType,
		&row.Operation, &row.PayloadHash, &row.PreviousAttempts, &processingError,
		&row.DeadSince, &row.CreatedAt, &catchupKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return IngestAcknowledgeUnreplayableResult{}, errors.New("no dead source_ingest_events row with that event id; only a dead event can be acknowledged as unreplayable")
	}
	if err != nil {
		return IngestAcknowledgeUnreplayableResult{}, err
	}
	if processingError != nil {
		row.ProcessingError = *processingError
	}
	row.HasCatchupKey = catchupKey != nil
	if err = ingestRequeueDeadReplayBindingTx(ctx, tx, &row); err != nil {
		return IngestAcknowledgeUnreplayableResult{}, err
	}
	if row.ScanCycles, err = ingestRequeueDeadScanCyclesTx(ctx, tx, row, row.ReplayBatchID); err != nil {
		return IngestAcknowledgeUnreplayableResult{}, err
	}
	if row.OpenFreezes, err = ingestRequeueDeadOpenFreezesTx(ctx, tx, row.PayloadHash); err != nil {
		return IngestAcknowledgeUnreplayableResult{}, err
	}

	// The guard. A replayable event has a repair; it does not have a write-off.
	if !row.ReplayBlocked {
		return IngestAcknowledgeUnreplayableResult{Applied: in.Apply, Event: row},
			errors.New("this event has a usable replay binding; requeue it with --kind=ingest-requeue-dead instead of acknowledging its fact as lost")
	}

	result := IngestAcknowledgeUnreplayableResult{Applied: in.Apply, Event: row, Acknowledged: true}
	if !in.Apply {
		// Dry run: everything above is a read, and the deferred Rollback is
		// what makes "writes nothing" true rather than merely intended.
		return result, nil
	}

	// Exactly RequeueSourceDependency's PRE_POLICY_SKIPPED column shape, with
	// this operation's own marker. Note what is *not* written: no fact row in
	// source_usage_events / source_credit_events /
	// balance_reconciliation_checkpoints, and no change to any watermark or
	// scan cycle. The event is closed, the fact is not invented.
	command, err := tx.Exec(ctx, `
		UPDATE source_ingest_events SET processing_status='processed',processing_error=$4,
			processed_at=now(),lease_token=NULL,lease_expires_at=NULL,
			dependency_kind=NULL,dependency_key_hmac=NULL,updated_at=now()
		WHERE source_instance_id=$1 AND stream_id=$2 AND event_id=$3 AND processing_status='dead'`,
		row.SourceInstanceID, row.StreamID, eventID, ingestUnreplayableMarker)
	if err != nil {
		return IngestAcknowledgeUnreplayableResult{}, err
	}
	if command.RowsAffected() != 1 {
		return IngestAcknowledgeUnreplayableResult{}, errors.New("dead ingest event no longer matches the predicate; refusing to apply")
	}

	if err = writeAudit(ctx, tx, actor, ingestUnreplayableAuditAction, "source_ingest_event", eventID,
		map[string]any{"processing_status": "dead", "attempt_count": row.PreviousAttempts,
			"processing_error": row.ProcessingError},
		map[string]any{"processing_status": "processed", "processing_error": ingestUnreplayableMarker,
			"source_instance_id": row.SourceInstanceID, "stream_id": row.StreamID,
			"entity_type": row.EntityType, "payload_hash": row.PayloadHash,
			"replay_blocked_reason": row.ReplayBlockedReason,
			"fact_applied":          false, "repair_tool": "XM-INV-DEAD-REQUEUE"}); err != nil {
		return IngestAcknowledgeUnreplayableResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return IngestAcknowledgeUnreplayableResult{}, err
	}
	return result, nil
}
