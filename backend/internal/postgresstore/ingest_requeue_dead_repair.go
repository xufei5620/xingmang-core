package postgresstore

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// IngestRequeueDeadRepairInput drives RepairIngestRequeueDead
// (XM-INV-DEAD-REQUEUE): a versioned, human-approved lifecycle operation
// that resets every source_ingest_events row MarkSourceEventFailed escalated
// to the terminal processing_status='dead' (attempt_count>=8) back to
// 'queued' with attempt_count=0, so ClaimUnprocessedSourceEvents picks it up
// again on the source processor's next tick.
//
// This is the source_ingest_events twin of RepairProjectionRequeueDead
// (XM-INV-PROJECTION-FAILURE-GRADING), and follows that repair's shape
// deliberately: dry run by default with every per-row transaction rolled
// back, an operator UUID required to apply, one transaction and one audit
// event per requeued row, per-row error isolation, and a summary table that
// reports what was *found* (attempts, error, how long dead, which scan
// cycles, which open freezes) in both modes so an operator sees what they
// are about to requeue before touching anything.
//
// Three deliberate non-behaviors, each load-bearing:
//
//   - It never touches eligibility_freezes. The EVENT_DEAD freeze
//     MarkSourceEventFailed opens beside a dead event is what lets
//     tryPublishEconomicScanCyclesTx count that event as complete (see its
//     `NOT (processing_status IN ('failed','dead') AND ef.id IS NOT NULL)`
//     filter). Resolving the freeze here while the requeue itself might fail
//     again would leave a dead event with *no* freeze -- which holds its scan
//     cycle in 'processing' indefinitely and, through the
//     source_economic_one_active_scan_cycle partial unique index, wedges the
//     whole stream. Leaving the freeze open makes the worst case ("the
//     requeue dies again") degrade back to exactly today's state instead of
//     to a worse one. The freeze is also not orphaned by a successful
//     requeue: it stays an ordinary open row on the admin resolution path
//     (ResolveEligibilityFreeze), whose own preconditions -- no
//     eligibility_projection_jobs row for the account, and a *matched*
//     latest finalized balance evaluation -- are precisely what force the
//     correct order of operations (requeue the facts first, resolve the
//     freeze afterwards on real evidence). Mirrors
//     ProjectionRequeueDeadRepairInput's own "never touches
//     eligibility_freezes" contract.
//
//   - It never touches source_economic_scan_cycles. A cycle that already
//     published is never re-evaluated (tryPublishEconomicScanCyclesTx selects
//     only cycle_status='processing', and no code path moves a row back out
//     of 'published'), and a requeued event's fact is still accepted on its
//     way in: verifyFactBatchContextTx explicitly admits 'published'
//     alongside 'receiving'/'processing'. The fact then lands as a *late*
//     fact under the account's finalized_through, which observeEligibilityFact
//     already has a designed path for (reprojectEligibilityTx). A cycle still
//     in 'processing', by contrast, does go back to incomplete for as long as
//     the requeued event is un-terminal -- correct, and self-healing in both
//     directions -- so ScanCycles below reports every cycle each candidate is
//     mapped to, with its current status, in dry-run mode too.
//
//   - It never touches created_at. SourceIngestHealth.OldestPending and the
//     readiness query both age a pending event from created_at, so a
//     long-dead event requeued here is immediately "old" and /readyz stays
//     503 -- with a changed reason ("source ingestion processing is
//     unhealthy" instead of "contains dead events") -- until the event
//     actually reaches 'processed'. That is honest: the deployment really is
//     not caught up until the fact lands. Rewriting created_at to hide it
//     would falsify when the event arrived.
type IngestRequeueDeadRepairInput struct {
	// Apply, when false (the default), reports what each candidate row's own
	// transaction would do without committing anything (every per-event
	// transaction is rolled back). Apply commits one transaction per event.
	Apply bool
	// OperatorID is required when Apply is true: the actor recorded on the
	// audit event this run writes for every event it requeues.
	OperatorID string
	// EventID, when non-empty, narrows the run to exactly this
	// source_ingest_events.event_id. This is the exact, unambiguous
	// narrowing -- it needs no correlation and works for a dead event that
	// never opened a freeze at all -- and is the intended way to repair a
	// known, individually reviewed set of rows in production.
	EventID string
	// AccountID, when non-empty, narrows the run to dead events correlated
	// to that external account through an *open* eligibility_freezes row
	// whose source_revision_hash equals the event's payload_hash. That is
	// the same content-hash correlation MarkSourceEventFailed's own dead
	// branch and RepairPreAnchorUsageEligibility already use, and the only
	// one available: source_ingest_events stores no account column, because
	// its payload is opaque ciphertext until an application-layer attempt
	// decrypts it. Consequence, deliberate and documented rather than
	// papered over: a dead event whose account was never frozen (the
	// structural gap MarkSourceEventFailed's own comment describes -- every
	// attempt failed before anything was persisted *and* the application
	// layer never resolved an account to hint with) is invisible to this
	// filter. Run without --account, or narrow with EventID, to see those.
	AccountID string
}

// IngestRequeueDeadScanCycle is one economic scan cycle a candidate event is
// mapped to (source_economic_scan_cycle_events), with that cycle's current
// status. Reported in both modes because it is the single piece of context
// that decides whether a requeue is a no-op for cycle publication
// ('published' -- never re-evaluated), a temporary hold ('processing' -- the
// event counts as incomplete again until it terminates), or futile
// ('blocked' -- verifyFactBatchContextTx rejects the fact outright, so the
// requeued event will spend its whole retry ladder and die again).
type IngestRequeueDeadScanCycle struct {
	ScanCycleID string
	CycleStatus string
}

// IngestRequeueDeadFreeze is one still-open eligibility_freezes row
// correlated to a candidate event by source_revision_hash=payload_hash.
// Reported, never modified: an operator needs to see which freeze will still
// be holding the account after the data hole is filled, and which account it
// belongs to (the only account attribution the ingest layer has).
type IngestRequeueDeadFreeze struct {
	FreezeID          string
	ExternalAccountID string
	FreezeReason      string
}

// IngestRequeueDeadRepairEvent is one row of the repair's per-event summary
// table.
type IngestRequeueDeadRepairEvent struct {
	SourceInstanceID string
	StreamID         string
	EventID          string
	// EntityType/Operation/PayloadHash identify what kind of fact is
	// encrypted in the row. PayloadHash is a content hash, never payload
	// content, and is already stored in the clear on every freeze it
	// correlates to.
	EntityType  string
	Operation   string
	PayloadHash string
	// PreviousAttempts/ProcessingError/DeadSince/CreatedAt describe the dead
	// row as found, before any reset -- populated in both dry-run and apply
	// mode so an operator can see what they are about to requeue (or, in dry
	// run, what they would requeue) without a separate query.
	PreviousAttempts int64
	ProcessingError  string
	DeadSince        time.Time
	CreatedAt        time.Time
	// HasCatchupKey reports whether this row still carries a
	// catchup_key_hmac. A dead row with one blocks completeEligibilityCatchupTx
	// forever (it counts every processing_status<>'processed' row), which in
	// turn keeps its account's own catchup_key_hmac set -- and an account
	// with that key set is excluded from finalizeSourceAccountsTx entirely,
	// so its finalized_through never advances again. Requeuing is the only
	// thing that can release it, so the operator is told when that second,
	// less obvious harm is in play.
	HasCatchupKey bool
	ScanCycles    []IngestRequeueDeadScanCycle
	OpenFreezes   []IngestRequeueDeadFreeze
	// Requeued is true once this row was (apply) or would be (dry run) reset
	// to processing_status='queued'/attempt_count=0/next_attempt_at=now.
	Requeued bool
}

// Key is the stable (source,stream,event) identity of one summary row, used
// for ordering and for the CLI's error table.
func (e IngestRequeueDeadRepairEvent) Key() string {
	return e.SourceInstanceID + "/" + e.StreamID + "/" + e.EventID
}

// IngestRequeueDeadRepairEventError records one event whose own repair
// transaction failed -- collected, never allowed to abort the run for any
// other event, the same per-unit isolation every sibling repair in this
// package uses (see QueueNarrowRepairAccountError's own doc comment for the
// production incident that established this pattern). A serialization
// failure here is expected rather than exceptional: the very condition this
// repair exists to clean up is a 40001 storm on the ingest path, and the
// repair is idempotent, so re-running it after a per-event error is always
// safe (a row already requeued is no longer 'dead' and is simply not found).
type IngestRequeueDeadRepairEventError struct {
	EventKey string
	Message  string
}

// IngestRequeueDeadRepairResult is the repair's full summary, printable
// as-is by the CLI in both dry-run and apply modes.
type IngestRequeueDeadRepairResult struct {
	Applied       bool
	Events        []IngestRequeueDeadRepairEvent
	Errors        []IngestRequeueDeadRepairEventError
	TotalRequeued int
}

// RepairIngestRequeueDead lists every source_ingest_events row currently
// processing_status='dead' (optionally narrowed to one event id, or to one
// account via its open freezes) and, in apply mode, resets each one to
// processing_status='queued', attempt_count=0, next_attempt_at=now with its
// lease and stale processing_error cleared.
//
// attempt_count=0 is not cosmetic and not a choice between "eight more
// chances" and "dies immediately": ClaimUnprocessedSourceEvents filters on
// `attempt_count < 8`, so a row left at 8 and flipped to 'queued' is never
// claimed at all. It would sit pending forever, counted by
// SourceIngestHealth.Pending with a created_at hours or days old, keeping
// /readyz 503 with no dead-event signal left to explain why -- strictly
// worse than leaving it dead. Zero is therefore the only value that makes
// the requeue mean anything, and it matches both in-repo precedents
// (RepairPreAnchorUsageEligibility's own source_ingest_events requeue, and
// ProjectionRequeueDeadRepair's attempts=0).
//
// That is also safe when the underlying cause is *not* yet fixed: the claim
// step increments to 1, SourceEventProcessor.RunOnce reschedules a failure
// five minutes out, and after eight failures MarkSourceEventFailed marks the
// row dead again and freezeEligibilityTx's ON CONFLICT (external_account_id,
// freeze_reason, trigger_object_type, trigger_object_id) WHERE status='open'
// reuses the freeze that is still standing rather than opening a second one.
// Worst case is therefore a bounded ~35-minute round trip back to exactly
// today's state, with an audit trail of the attempt, and the tool can simply
// be run again once the cause is fixed.
//
// Mirrors RepairProjectionRequeueDead's per-unit transaction isolation: one
// event's own conflict or data inconsistency is reported as a per-event
// error and never blocks any other event in the same run.
func (s *Store) RepairIngestRequeueDead(ctx context.Context, in IngestRequeueDeadRepairInput, actor AuditActor) (IngestRequeueDeadRepairResult, error) {
	if in.Apply && !eligibilityUUIDPattern.MatchString(strings.TrimSpace(in.OperatorID)) {
		return IngestRequeueDeadRepairResult{}, errors.New("a valid operator UUID is required to apply")
	}
	if id := strings.TrimSpace(in.EventID); id != "" && !eligibilityUUIDPattern.MatchString(id) {
		return IngestRequeueDeadRepairResult{}, errors.New("event id filter must be a UUID")
	}
	if id := strings.TrimSpace(in.AccountID); id != "" && !eligibilityUUIDPattern.MatchString(id) {
		return IngestRequeueDeadRepairResult{}, errors.New("account id filter must be a UUID")
	}
	candidates, err := s.ingestRequeueDeadCandidates(ctx, in)
	if err != nil {
		return IngestRequeueDeadRepairResult{}, err
	}

	result := IngestRequeueDeadRepairResult{Applied: in.Apply}
	for _, candidate := range candidates {
		event, repairErr := s.repairIngestRequeueDeadEvent(ctx, candidate, in, actor)
		if repairErr != nil {
			result.Errors = append(result.Errors, IngestRequeueDeadRepairEventError{
				EventKey: candidate.Key(), Message: repairErr.Error()})
			continue
		}
		result.Events = append(result.Events, event)
		if event.Requeued {
			result.TotalRequeued++
		}
	}
	sort.Slice(result.Events, func(i, j int) bool { return result.Events[i].Key() < result.Events[j].Key() })
	sort.Slice(result.Errors, func(i, j int) bool { return result.Errors[i].EventKey < result.Errors[j].EventKey })
	return result, nil
}

// ingestRequeueDeadCandidates is a plain, transaction-less snapshot read,
// mirroring projectionRequeueDeadCandidateAccountIDs' own reasoning: a stale
// snapshot here (a row requeued or processed concurrently between this read
// and that event's own transaction) is harmless -- that event's transaction
// simply finds no matching row and reports an empty, error-free result.
func (s *Store) ingestRequeueDeadCandidates(ctx context.Context, in IngestRequeueDeadRepairInput) ([]IngestRequeueDeadRepairEvent, error) {
	eventID := strings.TrimSpace(in.EventID)
	accountID := strings.TrimSpace(in.AccountID)
	// NULLIF(...)::uuid rather than a bare $n='' OR ... : PostgreSQL does not
	// promise to short-circuit OR, so a literal ''::uuid cast in the unused
	// branch can still be evaluated and raise. NULLIF('','') is NULL, and
	// NULL::uuid is always safe.
	rows, err := s.pool.Query(ctx, `
		SELECT sie.source_instance_id::text,sie.stream_id,sie.event_id::text
		FROM source_ingest_events sie
		WHERE sie.processing_status='dead'
			AND (NULLIF($1::text,'') IS NULL OR sie.event_id=NULLIF($1::text,'')::uuid)
			AND (NULLIF($2::text,'') IS NULL OR EXISTS (
				SELECT 1 FROM eligibility_freezes ef
				WHERE ef.status='open' AND ef.external_account_id=NULLIF($2::text,'')::uuid
					AND ef.source_revision_hash=sie.payload_hash))
		ORDER BY 1,2,3`, eventID, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := make([]IngestRequeueDeadRepairEvent, 0)
	for rows.Next() {
		var candidate IngestRequeueDeadRepairEvent
		if err = rows.Scan(&candidate.SourceInstanceID, &candidate.StreamID, &candidate.EventID); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

// repairIngestRequeueDeadEvent is the whole per-event unit of work: its own
// transaction, committed on success in apply mode and always rolled back in
// dry-run mode. A defined, zero-error, Requeued=false result covers the case
// where the row no longer matches (processed or requeued concurrently since
// the candidate snapshot read) -- only a genuine database/query error is
// ever returned.
func (s *Store) repairIngestRequeueDeadEvent(ctx context.Context, candidate IngestRequeueDeadRepairEvent,
	in IngestRequeueDeadRepairInput, actor AuditActor) (IngestRequeueDeadRepairEvent, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return IngestRequeueDeadRepairEvent{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	row := candidate
	var processingError *string
	var catchupKey *string
	err = tx.QueryRow(ctx, `
		SELECT entity_type,operation,payload_hash,attempt_count,processing_error,
			updated_at,created_at,catchup_key_hmac
		FROM source_ingest_events
		WHERE source_instance_id=$1 AND stream_id=$2 AND event_id=$3 AND processing_status='dead'
		FOR UPDATE`, candidate.SourceInstanceID, candidate.StreamID, candidate.EventID).Scan(
		&row.EntityType, &row.Operation, &row.PayloadHash, &row.PreviousAttempts,
		&processingError, &row.DeadSince, &row.CreatedAt, &catchupKey)
	if errors.Is(err, pgx.ErrNoRows) {
		// No longer a matching dead row -- a defined, empty, error-free
		// result, not a fault.
		return candidate, nil
	}
	if err != nil {
		return IngestRequeueDeadRepairEvent{}, err
	}
	if processingError != nil {
		row.ProcessingError = *processingError
	}
	row.HasCatchupKey = catchupKey != nil
	if row.ScanCycles, err = ingestRequeueDeadScanCyclesTx(ctx, tx, candidate); err != nil {
		return IngestRequeueDeadRepairEvent{}, err
	}
	if row.OpenFreezes, err = ingestRequeueDeadOpenFreezesTx(ctx, tx, row.PayloadHash); err != nil {
		return IngestRequeueDeadRepairEvent{}, err
	}

	if !in.Apply {
		// Dry run: everything above is a read, and the deferred Rollback is
		// what makes "reports exactly what apply would do, writes nothing"
		// true rather than merely intended.
		row.Requeued = true
		return row, nil
	}

	// attempt_count=0 is what makes the row claimable again at all
	// (ClaimUnprocessedSourceEvents filters attempt_count<8); the lease
	// columns and dependency pair are re-asserted NULL rather than assumed,
	// because the table's own CHECK constraints tie them to the status this
	// UPDATE writes. processing_error is cleared for the same reason
	// MarkSourceEventBusy and the readiness query both read it as *current*
	// state: a stale 'PROJECTION_FAILED' on a queued row is a lie. The
	// human-readable text survives in this run's own summary output, which
	// the runbook requires be captured.
	command, err := tx.Exec(ctx, `
		UPDATE source_ingest_events SET processing_status='queued',attempt_count=0,
			processing_error=NULL,next_attempt_at=now(),lease_token=NULL,lease_expires_at=NULL,
			dependency_kind=NULL,dependency_key_hmac=NULL,updated_at=now()
		WHERE source_instance_id=$1 AND stream_id=$2 AND event_id=$3 AND processing_status='dead'`,
		candidate.SourceInstanceID, candidate.StreamID, candidate.EventID)
	if err != nil {
		return IngestRequeueDeadRepairEvent{}, err
	}
	if command.RowsAffected() != 1 {
		return IngestRequeueDeadRepairEvent{}, errors.New("dead ingest event no longer matches the repair predicate; refusing to apply")
	}
	row.Requeued = true

	// One audit event per requeued row, never one summary row for the run --
	// reusing RepairPreAnchorUsageEligibility's own action/object_type/
	// object_id shape verbatim so both tools' requeues are one query away
	// from each other, and distinguished only by repair_tool in the payload.
	if err = writeAudit(ctx, tx, actor, "source_ingest_event.repair_requeued", "source_ingest_event",
		candidate.EventID,
		map[string]any{"processing_status": "dead", "attempt_count": row.PreviousAttempts,
			"processing_error": row.ProcessingError},
		map[string]any{"processing_status": "queued", "attempt_count": 0,
			"source_instance_id": candidate.SourceInstanceID, "stream_id": candidate.StreamID,
			"entity_type": row.EntityType, "repair_tool": "XM-INV-DEAD-REQUEUE"}); err != nil {
		return IngestRequeueDeadRepairEvent{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return IngestRequeueDeadRepairEvent{}, err
	}
	return row, nil
}

// ingestRequeueDeadScanCyclesTx lists every economic scan cycle this event is
// mapped to. An event can appear in more than one cycle (a rescan re-sends
// the same event_id under a new scan_cycle_id, and CommitSourceBatch inserts
// the mapping unconditionally while inserting the event row only once), so
// this returns all of them rather than assuming a single owner.
func ingestRequeueDeadScanCyclesTx(ctx context.Context, tx pgx.Tx, candidate IngestRequeueDeadRepairEvent) ([]IngestRequeueDeadScanCycle, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.scan_cycle_id::text,c.cycle_status
		FROM source_economic_scan_cycle_events m
		JOIN source_economic_scan_cycles c ON c.source_instance_id=m.source_instance_id
			AND c.stream_id=m.stream_id AND c.scan_cycle_id=m.scan_cycle_id
		WHERE m.source_instance_id=$1 AND m.stream_id=$2 AND m.event_id=$3
		ORDER BY 1`, candidate.SourceInstanceID, candidate.StreamID, candidate.EventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cycles := make([]IngestRequeueDeadScanCycle, 0)
	for rows.Next() {
		var cycle IngestRequeueDeadScanCycle
		if err = rows.Scan(&cycle.ScanCycleID, &cycle.CycleStatus); err != nil {
			return nil, err
		}
		cycles = append(cycles, cycle)
	}
	return cycles, rows.Err()
}

// ingestRequeueDeadOpenFreezesTx lists the still-open freezes correlated to
// this event's payload_hash -- the same source_revision_hash correlation
// MarkSourceEventFailed's dead branch and tryPublishEconomicScanCyclesTx's
// completeness filter both use. Read-only, on purpose: see the input type's
// doc comment for why this repair never resolves one.
func ingestRequeueDeadOpenFreezesTx(ctx context.Context, tx pgx.Tx, payloadHash string) ([]IngestRequeueDeadFreeze, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text,external_account_id::text,freeze_reason
		FROM eligibility_freezes
		WHERE status='open' AND source_revision_hash=$1
		ORDER BY 1`, payloadHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	freezes := make([]IngestRequeueDeadFreeze, 0)
	for rows.Next() {
		var freeze IngestRequeueDeadFreeze
		if err = rows.Scan(&freeze.FreezeID, &freeze.ExternalAccountID, &freeze.FreezeReason); err != nil {
			return nil, err
		}
		freezes = append(freezes, freeze)
	}
	return freezes, rows.Err()
}
