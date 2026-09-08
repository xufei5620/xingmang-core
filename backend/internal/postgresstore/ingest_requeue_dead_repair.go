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
//   - It never touches source_economic_scan_cycles -- it reads them, and
//     refuses to act when they say the replay cannot succeed. A cycle that
//     already published is never re-evaluated (tryPublishEconomicScanCyclesTx
//     selects only cycle_status='processing', and no code path moves a row
//     back out of 'published'), and a requeued event's fact is still accepted
//     on its way in: verifyFactBatchContextTx explicitly admits 'published'
//     alongside 'receiving'/'processing'. The fact then lands as a *late*
//     fact under the account's finalized_through, which observeEligibilityFact
//     already has a designed path for (reprojectEligibilityTx). A cycle still
//     in 'processing', by contrast, does go back to incomplete for as long as
//     the requeued event is un-terminal -- correct, and self-healing in both
//     directions. A cycle in any other status means the fact is refused on
//     arrival, so the event is reported and skipped rather than requeued --
//     see ReplayBlocked, and IncludeBlockedCycles to override.
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
	// IncludeBlockedCycles forces the run to requeue events whose replay the
	// runtime will reject -- an unusable binding shape, or one whose scan
	// ceiling runs past the event's observed_at (see ReplayBlocked). Those
	// events are skipped by default in both modes,
	// because requeuing them cannot succeed: the fact is refused on arrival,
	// so the event spends all eight attempts and dies again, landing exactly
	// where it started. This flag exists only so an operator who has decided
	// to spend that ladder deliberately can; it is never how such an event
	// gets fixed. Fixing it means giving it a valid binding, which is
	// upstream of this tool.
	IncludeBlockedCycles bool
}

// ingestReplayableCycleStatuses is verifyFactBatchContextTx's own accepted
// set, restated here because this repair's whole job is to predict that
// function's verdict before spending a retry ladder discovering it. Keep the
// two in step: consumption.go's
// `cycleStatus != "receiving" && cycleStatus != "processing" && cycleStatus != "published"`
// is the authority.
var ingestReplayableCycleStatuses = map[string]bool{"receiving": true, "processing": true, "published": true}

// IngestRequeueDeadScanCycle is one economic scan cycle a candidate event is
// mapped to (source_economic_scan_cycle_events), with that cycle's current
// status and the batch the mapping came in on.
//
// An event can be mapped to several cycles, and only one of them is the one
// a replay actually travels through -- see ReplayBinding. That distinction is
// not cosmetic: on 2026-09-08 a production dry run listed a 'published' cycle
// beside a 'blocked' one for the same event, and only the blocked one was on
// the replay path -- the published mapping's batch carried a scan ceiling
// hours past the event's frozen observed_at, so claimBindingSelect's time
// predicate (XM-INV-BINDING-SKEW) excludes it and the claim falls back to
// first_batch_id. Reporting the list without saying which entry governs
// invites exactly the wrong conclusion.
type IngestRequeueDeadScanCycle struct {
	ScanCycleID string
	CycleStatus string
	BatchID     string
	// ReplayBinding marks the single mapping a replay of this event would be
	// verified against: whichever one claimBindingSelect resolves to -- the
	// newest usable binding, or first_batch_id when there is none.
	ReplayBinding bool
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
	// ReplayBatchID/ReplayScanCycleID/ReplayCycleStatus describe the exact
	// binding a replay of this event would be verified against, reproducing
	// what the runtime actually does rather than approximating it:
	// ClaimUnprocessedSourceEvents resolves the binding with claimBindingSelect
	// -- the event's newest usable economic binding, or first_batch_id when it
	// has none -- and hands that batch's id and scan_cycle_id to the
	// application layer, which passes them to verifyFactBatchContextTx, which
	// looks the mapping up by the exact (event_id, batch_id, scan_cycle_id)
	// triple. Which of an event's several bindings that is, is exactly what
	// this field reports; the others are listed in ScanCycles as evidence.
	// Empty when the event has no v3 economic binding at all (see
	// ReplayBlocked).
	ReplayBatchID     string
	ReplayScanCycleID string
	ReplayCycleStatus string
	// ReplayBlocked is true when the runtime would reject this event's replay
	// outright, so requeuing it can only burn its retry ladder and die again.
	// Such an event is reported but not requeued unless the caller sets
	// IncludeBlockedCycles. ReplayBlockedReason says which condition fails, in
	// the terms of the function that would apply it -- verifyFactBatchContextTx
	// for the binding's shape, validateFactMetadata for its clock skew.
	ReplayBlocked       bool
	ReplayBlockedReason string
	// Requeued is true once this row was (apply) or would be (dry run) reset
	// to processing_status='queued'/attempt_count=0/next_attempt_at=now. It
	// is false for a skipped ReplayBlocked event -- the summary's own
	// verdict, so nobody has to cross-reference a runbook to reach it.
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
	Applied bool
	Events  []IngestRequeueDeadRepairEvent
	Errors  []IngestRequeueDeadRepairEventError
	// TotalRequeued counts only events actually requeued (apply) or that
	// would be (dry run). TotalBlockedSkipped counts events left alone
	// because their replay binding would be rejected. The two are reported
	// separately so "3 found" can never be read as "3 requeued".
	TotalRequeued       int
	TotalBlockedSkipped int
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
		switch {
		case event.Requeued:
			result.TotalRequeued++
		case event.ReplayBlocked:
			result.TotalBlockedSkipped++
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
	if err = ingestRequeueDeadReplayBindingTx(ctx, tx, &row); err != nil {
		return IngestRequeueDeadRepairEvent{}, err
	}
	if row.ScanCycles, err = ingestRequeueDeadScanCyclesTx(ctx, tx, candidate, row.ReplayBatchID); err != nil {
		return IngestRequeueDeadRepairEvent{}, err
	}
	if row.OpenFreezes, err = ingestRequeueDeadOpenFreezesTx(ctx, tx, row.PayloadHash); err != nil {
		return IngestRequeueDeadRepairEvent{}, err
	}

	if row.ReplayBlocked && !in.IncludeBlockedCycles {
		// The verdict is the tool's, not the reader's: a replay that
		// verifyFactBatchContextTx will refuse can only burn eight attempts
		// and die again, so it is reported with its reason and left alone in
		// both modes. Requeued stays false, which is what the summary prints
		// and what TotalRequeued counts.
		return row, nil
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

// ingestRequeueDeadReplayBindingTx reproduces, as a prediction, the exact
// path a replay of this event travels:
//
//   - ClaimUnprocessedSourceEvents resolves the binding with
//     claimBindingSelect -- the event's newest economic binding that is still
//     usable, falling back to sie.first_batch_id when it has none -- and puts
//     that batch's batch_id, scan_cycle_id, schema_version and scan_ceiling_at
//     on the claim, alongside the event's own frozen observed_at;
//   - the application layer passes BatchID/ScanCycleID straight through to
//     ObserveUsageEvent/ObserveBalanceCheckpoint/..., with the batch's
//     scan_ceiling_at as the fact's StreamWatermarkAt;
//   - validateFactMetadata (consumption.go) runs first and refuses the fact
//     outright when that watermark runs more than factClockSkewTolerance past
//     the event's observed_at -- before the verifier is reached at all;
//   - verifyFactBatchContextTx then looks the mapping up by the exact
//     (event_id, batch_id, scan_cycle_id) triple, requires the batch to be
//     schema_version='3.0', requires m.payload_hash to equal the event's own
//     payload hash, and accepts the cycle only in
//     ingestReplayableCycleStatuses.
//
// Everything the runtime checks is therefore knowable before spending a
// retry ladder finding out, and this function checks it. The gate applies
// only to the four economic streams (validEconomicStream) delivered on a v3
// batch -- an identities-stream event never reaches either function and is
// freely replayable.
func ingestRequeueDeadReplayBindingTx(ctx context.Context, tx pgx.Tx, row *IngestRequeueDeadRepairEvent) error {
	var schemaVersion, batchID string
	var mappingPayloadHash, cycleID, cycleStatus *string
	// scan_ceiling_at is nullable in the table (the v2 half of
	// source_ingest_batches_economic_metadata forces it NULL), so it is scanned
	// as a pointer. For a schema_version='3.0' batch the same CHECK forces it
	// NOT NULL, so a nil past the early return below is structurally
	// impossible -- and is still treated as blocked rather than as "no skew",
	// because a NULL that reads as "fine" is how a guard silently stops
	// guarding. sie.observed_at is NOT NULL (migrations/0004) and never
	// rewritten after the event's first delivery.
	var replayCeilingAt *time.Time
	var observedAt time.Time
	// claimBindingSelect is embedded verbatim rather than reimplemented. This
	// function's only job is to predict what the claim will hand the verifier,
	// so it has to resolve the binding with the *same SQL the claim uses*.
	// Restating the rule would let the two drift, and a repair tool whose
	// prediction has drifted from the runtime is worse than no prediction --
	// a report disagreeing with reality is exactly the defect this tool was
	// last fixed for.
	err := tx.QueryRow(ctx, `
		SELECT sib.schema_version,sib.batch_id::text,m.payload_hash,
			c.scan_cycle_id::text,c.cycle_status,sib.scan_ceiling_at,sie.observed_at
		FROM source_ingest_events sie`+claimBindingSelect+`
		LEFT JOIN source_economic_scan_cycle_events m ON m.source_instance_id=sie.source_instance_id
			AND m.stream_id=sie.stream_id AND m.event_id=sie.event_id
			AND m.batch_id=sib.batch_id AND m.scan_cycle_id=sib.scan_cycle_id
		LEFT JOIN source_economic_scan_cycles c ON c.source_instance_id=m.source_instance_id
			AND c.stream_id=m.stream_id AND c.scan_cycle_id=m.scan_cycle_id
		WHERE sie.source_instance_id=$1 AND sie.stream_id=$2 AND sie.event_id=$3`,
		row.SourceInstanceID, row.StreamID, row.EventID).Scan(
		&schemaVersion, &batchID, &mappingPayloadHash, &cycleID, &cycleStatus,
		&replayCeilingAt, &observedAt)
	if err != nil {
		return err
	}
	row.ReplayBatchID = batchID
	if cycleID != nil {
		row.ReplayScanCycleID = *cycleID
	}
	if cycleStatus != nil {
		row.ReplayCycleStatus = *cycleStatus
	}
	if !validEconomicStream(row.StreamID) || schemaVersion != "3.0" {
		// No economic fact context is ever verified for this event.
		return nil
	}
	switch {
	case cycleStatus == nil:
		row.ReplayBlocked = true
		row.ReplayBlockedReason = "the event's first_batch_id (" + batchID +
			") carries no scan-cycle binding for it; verifyFactBatchContextTx would return ErrForbidden"
	case mappingPayloadHash == nil || *mappingPayloadHash != row.PayloadHash:
		row.ReplayBlocked = true
		row.ReplayBlockedReason = "the replay binding's payload_hash does not match the event's; verifyFactBatchContextTx would return ErrConflict"
	case !ingestReplayableCycleStatuses[*cycleStatus]:
		row.ReplayBlocked = true
		row.ReplayBlockedReason = "replay scan cycle " + row.ReplayScanCycleID + " is '" + *cycleStatus +
			"'; verifyFactBatchContextTx accepts only receiving/processing/published, so every attempt would be refused"
	case replayCeilingAt == nil:
		row.ReplayBlocked = true
		row.ReplayBlockedReason = "replay batch " + batchID + " is schema_version='3.0' but carries no scan_ceiling_at; " +
			"the claim would hand the fact 'epoch' as its stream_watermark_at and validateFactMetadata would reject it " +
			"with \"" + factMetadataTimeInvalidMessage + "\", so every attempt would be refused"
	case replayCeilingAt.After(observedAt.Add(factClockSkewTolerance)):
		// XM-INV-BINDING-SKEW, the case that is not about the verifier.
		// Everything above predicts verifyFactBatchContextTx; this one
		// predicts validateFactMetadata, which runs before it and, on failure,
		// means the verifier is never reached at all. That ordering is the
		// whole reason the three cases above could not see the 2026-09-07
		// failure: the tool said "replayable" while the runtime refused the
		// fact eight times, and ingest-acknowledge-unreplayable -- which
		// re-derives its verdict from this same function -- then refused to
		// write the events off. Two tools, each pointing at the other.
		//
		// After claimBindingSelect's own time predicate, arm 1 can never
		// produce a binding this case would catch (the predicate is that same
		// comparison), so it can only ever fire on the first_batch_id
		// fallback. Today's agent makes even that unreachable: every v3
		// connector stamps a record's observed_at at page-emission time while
		// the batch's scan_ceiling_at is the cycle horizon fixed at cycle
		// start, so ceiling <= observed on a first delivery. Nothing in this
		// system enforces that -- sourceingest/receiver.go bounds observed_at,
		// source_captured_at and scan_ceiling_at only from above and relates
		// none of them to each other, and the batch-level CHECK ties the
		// ceiling to source_captured_at rather than to any record's
		// observation. The case is here so this tool's verdict rests on this
		// code instead of on an agent's current habit.
		row.ReplayBlocked = true
		row.ReplayBlockedReason = "replay batch " + batchID + " has scan_ceiling_at " +
			replayCeilingAt.UTC().Format(time.RFC3339Nano) + ", more than " + factClockSkewTolerance.String() +
			" after the event's observed_at " + observedAt.UTC().Format(time.RFC3339Nano) +
			"; validateFactMetadata would reject the fact with \"" + factMetadataTimeInvalidMessage +
			"\" before verifyFactBatchContextTx is reached, so every attempt would be refused"
	}
	return nil
}

// ingestRequeueDeadScanCyclesTx lists every economic scan cycle this event is
// mapped to, flagging the one on the replay path. An event can appear in more
// than one cycle: agent event ids are deterministic
// (agents/sourceagent/batch.go's deterministicUUID over
// source/entity/external id/operation/payload hash), so a rescan re-delivers
// the identical event id, and CommitSourceBatch then inserts a fresh mapping
// row under the new cycle while deliberately leaving the existing ingest row
// -- dead included -- untouched. The extra mappings are real evidence and
// worth reporting, but only replayBatchID's is the one a replay is verified
// against.
func ingestRequeueDeadScanCyclesTx(ctx context.Context, tx pgx.Tx, candidate IngestRequeueDeadRepairEvent,
	replayBatchID string) ([]IngestRequeueDeadScanCycle, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.scan_cycle_id::text,c.cycle_status,m.batch_id::text
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
		if err = rows.Scan(&cycle.ScanCycleID, &cycle.CycleStatus, &cycle.BatchID); err != nil {
			return nil, err
		}
		cycle.ReplayBinding = replayBatchID != "" && cycle.BatchID == replayBatchID
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
