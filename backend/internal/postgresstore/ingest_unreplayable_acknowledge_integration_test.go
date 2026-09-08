package postgresstore

import (
	"strings"
	"testing"
	"time"
)

// seedUnreplayableDeadEvent builds the balance checkpoint's production shape:
// a dead event whose only scan-cycle binding was superseded to 'blocked', so
// verifyFactBatchContextTx refuses every replay and no later scan can ever
// give it a valid one. Returns the fixture and the event id.
func seedUnreplayableDeadEvent(t *testing.T) (deadIngestFixture, string) {
	t.Helper()
	fixture := seedDeadIngestFixture(t, "balances")
	const eventID = "88000000-0000-4000-8000-0000000000c1"
	const blockedCycleID = "89000000-0000-4000-8000-0000000000c1"
	const successorCycleID = "89000000-0000-4000-8000-0000000000c2"
	fixture.commitEvents(t, "balances", blockedCycleID,
		[]SourceBatchEvent{newDeadIngestEvent(eventID, "balance_checkpoint")})
	fixture.killEvent(t, "balances", eventID, 30*time.Hour)
	fixture.freezeOverEvent(t, eventID, "balance_checkpoint")
	// The agent restarts and rescans. A balance snapshot's payload differs
	// every time, so the rescan carries a *different* event id -- this one is
	// never re-delivered, and its blocked binding is the only one it will
	// ever have.
	fixture.commitSupersedingCycle(t, "balances", successorCycleID,
		[]SourceBatchEvent{newDeadIngestEvent("88000000-0000-4000-8000-0000000000c2", "balance_checkpoint")})
	if status := fixture.cycleStatus(t, "balances", blockedCycleID); status != "blocked" {
		t.Fatalf("cycle status=%q, want blocked", status)
	}
	return fixture, eventID
}

// factRowsForPayload counts every "the fact was applied" row this payload
// could possibly have produced, across all three domain fact tables.
func factRowsForPayload(t *testing.T, f deadIngestFixture, payloadHash string) int {
	t.Helper()
	var count int
	if err := f.store.pool.QueryRow(f.ctx, `
		SELECT (SELECT count(*) FROM source_usage_events WHERE source_revision_hash=$1)
			+ (SELECT count(*) FROM source_credit_events WHERE source_revision_hash=$1)
			+ (SELECT count(*) FROM balance_reconciliation_checkpoints WHERE source_revision_hash=$1)`,
		payloadHash).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func unreplayableAuditCount(t *testing.T, f deadIngestFixture, eventID string) int {
	t.Helper()
	var count int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_events
		WHERE action='source_ingest_event.unreplayable_acknowledged'
			AND object_type='source_ingest_event' AND object_id=$1`, eventID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestAcknowledgeUnreplayableDryRunWritesNothing keeps the dry-run half in
// its own test so the mutation that proves it (forcing the dry-run branch
// into the apply path) turns exactly this one red while the apply-side tests
// below stay green as controls.
func TestAcknowledgeUnreplayableDryRunWritesNothing(t *testing.T) {
	fixture, eventID := seedUnreplayableDeadEvent(t)
	before := fixture.readIngestEvent(t, "balances", eventID)

	result, err := fixture.store.AcknowledgeUnreplayableIngestEvent(fixture.ctx,
		IngestAcknowledgeUnreplayableInput{EventID: eventID}, AuditActor{Type: "admin", ID: deadIngestOperator})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || !result.Acknowledged || result.Event.EventID != eventID ||
		!result.Event.ReplayBlocked || result.Event.EntityType != "balance_checkpoint" {
		t.Fatalf("dry run result=%+v", result)
	}
	if after := fixture.readIngestEvent(t, "balances", eventID); after != before {
		t.Fatalf("dry run wrote to the ingest row: before=%+v after=%+v", before, after)
	}
	if count := unreplayableAuditCount(t, fixture, eventID); count != 0 {
		t.Fatalf("dry run wrote %d audit rows, want 0", count)
	}
}

// TestAcknowledgeUnreplayableClosesTheEventWithoutApplyingItsFact is the
// operation's whole contract, and requirement 5's absence assertion: the row
// reaches a non-dead terminal state in exactly RequeueSourceDependency's
// PRE_POLICY_SKIPPED column shape, readiness stops counting it as Dead, and
// **no fact appears anywhere**. Mutation-verified by making the apply path
// also insert a fact row (see the handoff's mutation table).
func TestAcknowledgeUnreplayableClosesTheEventWithoutApplyingItsFact(t *testing.T) {
	fixture, eventID := seedUnreplayableDeadEvent(t)
	payloadHash := testHash("dead-requeue-payload-" + eventID)

	healthBefore, err := fixture.store.SourceIngestHealth(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if healthBefore.Dead != 1 {
		t.Fatalf("Dead=%d before, want 1 (the fixture must actually be blocking readiness)", healthBefore.Dead)
	}
	if factRowsForPayload(t, fixture, payloadHash) != 0 {
		t.Fatal("the fixture already has a fact for this payload; the absence assertion would be vacuous")
	}

	result, err := fixture.store.AcknowledgeUnreplayableIngestEvent(fixture.ctx,
		IngestAcknowledgeUnreplayableInput{Apply: true, OperatorID: deadIngestOperator, EventID: eventID},
		AuditActor{Type: "admin", ID: deadIngestOperator})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || !result.Acknowledged {
		t.Fatalf("apply result=%+v", result)
	}

	// The PRE_POLICY_SKIPPED column shape, verbatim.
	var status, processingError string
	var processedAt *time.Time
	var leaseToken, dependencyKind *string
	if err := fixture.store.pool.QueryRow(fixture.ctx, `
		SELECT processing_status,COALESCE(processing_error,''),processed_at,lease_token,dependency_kind
		FROM source_ingest_events WHERE event_id=$1`, eventID).Scan(
		&status, &processingError, &processedAt, &leaseToken, &dependencyKind); err != nil {
		t.Fatal(err)
	}
	if status != "processed" || processingError != "UNREPLAYABLE_BINDING" || processedAt == nil ||
		leaseToken != nil || dependencyKind != nil {
		t.Fatalf("row=%q/%q processed_at=%v lease=%v dependency=%v, want the PRE_POLICY_SKIPPED shape",
			status, processingError, processedAt, leaseToken, dependencyKind)
	}

	// The absence assertion: nothing anywhere claims this fact was applied.
	if count := factRowsForPayload(t, fixture, payloadHash); count != 0 {
		t.Fatalf("%d fact rows exist for this payload; acknowledging must never write one", count)
	}

	// Readiness: Dead is what keeps /readyz at 503, and it must now be clear.
	healthAfter, err := fixture.store.SourceIngestHealth(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if healthAfter.Dead != 0 {
		t.Fatalf("Dead=%d after, want 0 (the whole point is releasing the readiness gate)", healthAfter.Dead)
	}
	if healthAfter.Pending != healthBefore.Pending {
		t.Fatalf("Pending moved from %d to %d; a closed event must not become pending work",
			healthBefore.Pending, healthAfter.Pending)
	}

	// One audit row for this event, carrying the write-off explicitly.
	if count := unreplayableAuditCount(t, fixture, eventID); count != 1 {
		t.Fatalf("audit rows=%d, want exactly 1", count)
	}

	// Freezes are untouched, same contract as the requeue tool.
	if count := openFreezeCount(t, fixture.store, fixture.ctx, fixture.accountID); count != 1 {
		t.Fatalf("open freeze count=%d, want 1 (acknowledging closes the event, not the freeze)", count)
	}

	// Re-running is a defined refusal, not a second audit row: the event is
	// no longer dead.
	if _, err := fixture.store.AcknowledgeUnreplayableIngestEvent(fixture.ctx,
		IngestAcknowledgeUnreplayableInput{Apply: true, OperatorID: deadIngestOperator, EventID: eventID},
		AuditActor{Type: "admin", ID: deadIngestOperator}); err == nil {
		t.Fatal("acknowledging an already-acknowledged event was accepted")
	}
	if count := unreplayableAuditCount(t, fixture, eventID); count != 1 {
		t.Fatalf("audit rows=%d after the re-run, want still 1", count)
	}
}

// TestAcknowledgeUnreplayableRefusesAReplayableEvent is the guard that keeps
// this from being a "mark anything processed" button: an event that still has
// a binding the verifier would accept must be requeued, not written off.
func TestAcknowledgeUnreplayableRefusesAReplayableEvent(t *testing.T) {
	fixture := seedDeadIngestFixture(t, "usage")
	const eventID = "88000000-0000-4000-8000-0000000000c3"
	fixture.commitEvents(t, "usage", "89000000-0000-4000-8000-0000000000c3",
		[]SourceBatchEvent{newDeadIngestEvent(eventID, "usage_event")})
	fixture.killEvent(t, "usage", eventID, 20*time.Hour)
	before := fixture.readIngestEvent(t, "usage", eventID)

	_, err := fixture.store.AcknowledgeUnreplayableIngestEvent(fixture.ctx,
		IngestAcknowledgeUnreplayableInput{Apply: true, OperatorID: deadIngestOperator, EventID: eventID},
		AuditActor{Type: "admin", ID: deadIngestOperator})
	if err == nil {
		t.Fatal("a replayable event was accepted for write-off")
	}
	if !strings.Contains(err.Error(), "ingest-requeue-dead") {
		t.Fatalf("refusal=%q, want it to point at the repair that does apply", err)
	}
	if after := fixture.readIngestEvent(t, "usage", eventID); after != before {
		t.Fatalf("the refused event was modified: before=%+v after=%+v", before, after)
	}
	if count := unreplayableAuditCount(t, fixture, eventID); count != 0 {
		t.Fatalf("a refused acknowledgement wrote %d audit rows", count)
	}
}

// TestAcknowledgeUnreplayableRequiresAnEventAndAnOperator pins the two
// guardrails the brief called for: it is never applied in bulk, and applying
// needs a named operator.
func TestAcknowledgeUnreplayableRequiresAnEventAndAnOperator(t *testing.T) {
	fixture, eventID := seedUnreplayableDeadEvent(t)
	before := fixture.readIngestEvent(t, "balances", eventID)

	for name, in := range map[string]IngestAcknowledgeUnreplayableInput{
		"no event id":       {Apply: true, OperatorID: deadIngestOperator},
		"malformed event":   {Apply: true, OperatorID: deadIngestOperator, EventID: "not-a-uuid"},
		"no operator":       {Apply: true, EventID: eventID},
		"bad operator":      {Apply: true, OperatorID: "not-a-uuid", EventID: eventID},
		"unknown event id":  {Apply: true, OperatorID: deadIngestOperator, EventID: "88000000-0000-4000-8000-0000000000ff"},
		"dry run no opertr": {EventID: ""},
	} {
		if _, err := fixture.store.AcknowledgeUnreplayableIngestEvent(fixture.ctx, in,
			AuditActor{Type: "admin", ID: deadIngestOperator}); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	if after := fixture.readIngestEvent(t, "balances", eventID); after != before {
		t.Fatalf("a rejected invocation mutated the row: before=%+v after=%+v", before, after)
	}
	if count := unreplayableAuditCount(t, fixture, eventID); count != 0 {
		t.Fatalf("rejected invocations wrote %d audit rows", count)
	}
}
