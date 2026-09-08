package postgresstore

import (
	"testing"
	"time"
)

// XM-INV-CLAIM-BINDING: ClaimUnprocessedSourceEvents must hand
// verifyFactBatchContextTx a binding that is currently valid, not the batch
// the event first arrived in. These tests reuse deadIngestFixture
// (ingest_requeue_dead_repair_integration_test.go) because it already builds
// exactly the shape needed -- a v3 source with provisioned streams, a cutover
// manifest, and an account with eligibility state -- and its
// commitSupersedingCycle helper drives the real supersede path rather than
// writing cycle_status by hand.

// reviveForClaim puts a committed event into a claimable state:
// processing_status='failed' with an attempt budget left and a due
// next_attempt_at. That is the state a real event is in between failures, and
// the state the ingest-requeue-dead repair leaves a requeued row in.
func (f deadIngestFixture) reviveForClaim(t *testing.T, stream, eventID string, attempts int) {
	t.Helper()
	command, err := f.store.pool.Exec(f.ctx, `
		UPDATE source_ingest_events SET processing_status='failed',attempt_count=$4,
			processing_error='PROJECTION_FAILED',next_attempt_at=now()-interval '1 minute',
			lease_token=NULL,lease_expires_at=NULL,updated_at=now()
		WHERE source_instance_id=$1 AND stream_id=$2 AND event_id=$3`,
		f.sourceID, stream, eventID, attempts)
	if err != nil {
		t.Fatal(err)
	}
	if command.RowsAffected() != 1 {
		t.Fatalf("reviveForClaim matched %d rows", command.RowsAffected())
	}
}

// claimFor claims a batch of events and returns the one for eventID, or fails
// the test if the claim query did not return it.
func (f deadIngestFixture) claimFor(t *testing.T, eventID string) SourceEventClaim {
	t.Helper()
	claims, err := f.store.ClaimUnprocessedSourceEvents(f.ctx, 50, time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, claim := range claims {
		if claim.EventID == eventID {
			return claim
		}
	}
	t.Fatalf("event %s was not claimed; claims=%+v", eventID, claims)
	return SourceEventClaim{}
}

func (f deadIngestFixture) batchIDForSequence(t *testing.T, stream string, sequence int64) string {
	t.Helper()
	var batchID string
	if err := f.store.pool.QueryRow(f.ctx, `SELECT batch_id::text FROM source_ingest_batches
		WHERE source_instance_id=$1 AND stream_id=$2 AND sequence=$3`,
		f.sourceID, stream, sequence).Scan(&batchID); err != nil {
		t.Fatal(err)
	}
	return batchID
}

// TestClaimBindingPrefersTheNewestValidBindingOverASupersededFirstBatch is
// the production shape from 2026-09-08: an agent restart superseded the cycle
// the event's first_batch_id belongs to (now 'blocked'), and the agent's next
// scan re-delivered the identical deterministic event id into a cycle that
// published.
//
// Before this slice the claim handed the verifier the blocked binding and
// every attempt was refused. Now it must hand over the published one, and
// verifyFactBatchContextTx -- unmodified, reached through its own exported
// entry point ValidateEconomicFactContext -- must accept it.
func TestClaimBindingPrefersTheNewestValidBindingOverASupersededFirstBatch(t *testing.T) {
	fixture := seedDeadIngestFixture(t, "usage")
	const eventID = "88000000-0000-4000-8000-0000000000b1"
	const supersededCycleID = "89000000-0000-4000-8000-0000000000b1"
	const successorCycleID = "89000000-0000-4000-8000-0000000000b2"
	event := newDeadIngestEvent(eventID, "usage_event")
	fixture.commitEvents(t, "usage", supersededCycleID, []SourceBatchEvent{event})
	firstBatchID := fixture.batchIDForSequence(t, "usage", 1)
	fixture.reviveForClaim(t, "usage", eventID, 3)
	// The open freeze is what lets the successor cycle count this
	// still-unfinished event as complete and publish, exactly as in
	// production.
	fixture.freezeOverEvent(t, eventID, "usage_event")
	fixture.commitSupersedingCycle(t, "usage", successorCycleID, []SourceBatchEvent{event})
	successorBatchID := fixture.batchIDForSequence(t, "usage", 2)

	if status := fixture.cycleStatus(t, "usage", supersededCycleID); status != "blocked" {
		t.Fatalf("superseded cycle status=%q, want blocked", status)
	}
	if status := fixture.cycleStatus(t, "usage", successorCycleID); status != "published" {
		t.Fatalf("successor cycle status=%q, want published", status)
	}

	claim := fixture.claimFor(t, eventID)
	if claim.BatchID != successorBatchID {
		t.Fatalf("claim.BatchID=%s, want the successor batch %s (not the superseded first batch %s)",
			claim.BatchID, successorBatchID, firstBatchID)
	}
	if claim.ScanCycleID != successorCycleID {
		t.Fatalf("claim.ScanCycleID=%s, want %s", claim.ScanCycleID, successorCycleID)
	}
	if claim.BatchSequence != 2 {
		t.Fatalf("claim.BatchSequence=%d, want 2 (the agent's latest statement about this event)", claim.BatchSequence)
	}
	if claim.SchemaVersion != "3.0" || claim.SigningKeyID == "" || claim.ScanCeilingAt.IsZero() {
		t.Fatalf("claim carries incomplete batch context: %+v", claim)
	}

	// The real verifier, untouched by this slice, must accept the triple the
	// claim now carries -- and must still have refused the old one.
	if err := fixture.store.ValidateEconomicFactContext(fixture.ctx, fixture.sourceID, "usage",
		claim.EventID, claim.BatchID, claim.ScanCycleID, claim.PayloadHash, claim.ScanCeilingAt); err != nil {
		t.Fatalf("verifyFactBatchContextTx refused the newly chosen binding: %v", err)
	}
	if err := fixture.store.ValidateEconomicFactContext(fixture.ctx, fixture.sourceID, "usage",
		claim.EventID, firstBatchID, supersededCycleID, claim.PayloadHash, claim.ScanCeilingAt); err == nil {
		t.Fatal("the superseded binding was accepted; the fixture does not reproduce the production failure")
	}
}

// TestClaimBindingTakesTheNewestOfSeveralValidBindings pins the selection
// rule itself: among bindings the verifier would accept, the newest batch
// wins -- the agent's latest statement about this event.
//
// It exists because the production-shape test above cannot check that: there
// only one binding is acceptable, so the ORDER BY direction is unobservable
// and any assertion about "newest" would be vacuous. Here the event has two
// acceptable bindings alongside the unusable one.
func TestClaimBindingTakesTheNewestOfSeveralValidBindings(t *testing.T) {
	fixture := seedDeadIngestFixture(t, "usage")
	const eventID = "88000000-0000-4000-8000-0000000000b7"
	const blockedCycleID = "89000000-0000-4000-8000-0000000000b7"
	const olderValidCycleID = "89000000-0000-4000-8000-0000000000b8"
	const newestValidCycleID = "89000000-0000-4000-8000-0000000000b9"
	event := newDeadIngestEvent(eventID, "usage_event")

	fixture.commitEvents(t, "usage", blockedCycleID, []SourceBatchEvent{event})
	fixture.reviveForClaim(t, "usage", eventID, 3)
	// The freeze lets each later cycle count this still-unfinished event as
	// complete, so both of them publish and both become valid bindings.
	fixture.freezeOverEvent(t, eventID, "usage_event")
	fixture.commitSupersedingCycle(t, "usage", olderValidCycleID, []SourceBatchEvent{event})
	olderValidBatchID := fixture.batchIDForSequence(t, "usage", 2)
	// The third cycle needs no supersede (nothing is active any more), but it
	// must still be committed through commitSupersedingCycle: that helper
	// stamps a ceiling one minute ahead, and a plain commitEvents would carry
	// an *earlier* stream_watermark_at than the cycle just published, which
	// tryPublishEconomicScanCyclesTx treats as a regression and blocks. (Found
	// by this test failing with cycle_status="blocked".) With no active cycle,
	// supersedeStaleActiveScanCycleTx finds nothing and returns immediately.
	fixture.commitSupersedingCycle(t, "usage", newestValidCycleID, []SourceBatchEvent{event})
	newestValidBatchID := fixture.batchIDForSequence(t, "usage", 3)

	for cycleID, want := range map[string]string{
		blockedCycleID: "blocked", olderValidCycleID: "published", newestValidCycleID: "published",
	} {
		if status := fixture.cycleStatus(t, "usage", cycleID); status != want {
			t.Fatalf("cycle %s status=%q, want %q", cycleID, status, want)
		}
	}

	claim := fixture.claimFor(t, eventID)
	if claim.BatchID != newestValidBatchID {
		t.Fatalf("claim.BatchID=%s, want the newest valid binding %s (not the older valid %s)",
			claim.BatchID, newestValidBatchID, olderValidBatchID)
	}
	if claim.ScanCycleID != newestValidCycleID || claim.BatchSequence != 3 {
		t.Fatalf("claim cycle=%s sequence=%d, want %s/3", claim.ScanCycleID, claim.BatchSequence, newestValidCycleID)
	}
	// Both valid bindings really are acceptable to the unmodified verifier, so
	// choosing between them is a genuine choice rather than one of them being
	// the only option left.
	for _, binding := range []struct{ batchID, cycleID string }{
		{olderValidBatchID, olderValidCycleID},
		{newestValidBatchID, newestValidCycleID},
	} {
		if err := fixture.store.ValidateEconomicFactContext(fixture.ctx, fixture.sourceID, "usage",
			eventID, binding.batchID, binding.cycleID, claim.PayloadHash,
			batchCeilingForTest(t, fixture, "usage", binding.batchID)); err != nil {
			t.Fatalf("binding batch=%s cycle=%s should be acceptable: %v", binding.batchID, binding.cycleID, err)
		}
	}
}

// batchCeilingForTest reads a batch's own scan_ceiling_at, which is what
// verifyFactBatchContextTx compares the caller's watermark against.
func batchCeilingForTest(t *testing.T, f deadIngestFixture, stream, batchID string) time.Time {
	t.Helper()
	var ceiling time.Time
	if err := f.store.pool.QueryRow(f.ctx, `SELECT scan_ceiling_at FROM source_ingest_batches
		WHERE source_instance_id=$1 AND stream_id=$2 AND batch_id=$3`,
		f.sourceID, stream, batchID).Scan(&ceiling); err != nil {
		t.Fatal(err)
	}
	return ceiling
}

// TestClaimBindingKeepsTheOriginalBindingWhenNoValidBindingExists is the
// reverse assertion: an event whose every binding is unusable -- the balance
// checkpoint's shape -- must not be handed some other batch just because one
// exists. It keeps its original binding and the verifier still refuses it, so
// it fails loudly and becomes dead exactly as before this slice.
//
// The fixture deliberately gives the event two *different* unusable batches,
// so "fell back to first_batch_id" and "picked the newest mapping" are
// distinguishable outcomes; with a single binding the two are identical and
// the assertion would be vacuous.
func TestClaimBindingKeepsTheOriginalBindingWhenNoValidBindingExists(t *testing.T) {
	fixture := seedDeadIngestFixture(t, "usage")
	const eventID = "88000000-0000-4000-8000-0000000000b3"
	const otherEventID = "88000000-0000-4000-8000-0000000000b4"
	const firstCycleID = "89000000-0000-4000-8000-0000000000b3"
	const secondCycleID = "89000000-0000-4000-8000-0000000000b4"
	const thirdCycleID = "89000000-0000-4000-8000-0000000000b5"
	event := newDeadIngestEvent(eventID, "usage_event")

	// Cycle 1 carries the event and is later superseded.
	fixture.commitEvents(t, "usage", firstCycleID, []SourceBatchEvent{event})
	firstBatchID := fixture.batchIDForSequence(t, "usage", 1)
	fixture.reviveForClaim(t, "usage", eventID, 3)
	// Cycle 2 re-delivers the same event id (so it becomes a second binding,
	// on a different batch) and supersedes cycle 1. No freeze this time, so
	// the still-unfinished event holds cycle 2 in 'processing'...
	fixture.commitSupersedingCycle(t, "usage", secondCycleID, []SourceBatchEvent{event})
	secondBatchID := fixture.batchIDForSequence(t, "usage", 2)
	// ...which lets cycle 3 supersede cycle 2 in turn. Cycle 3 carries a
	// different event, so it never becomes a binding for this one.
	fixture.commitSupersedingCycle(t, "usage", thirdCycleID,
		[]SourceBatchEvent{newDeadIngestEvent(otherEventID, "usage_event")})

	for cycleID, want := range map[string]string{firstCycleID: "blocked", secondCycleID: "blocked"} {
		if status := fixture.cycleStatus(t, "usage", cycleID); status != want {
			t.Fatalf("cycle %s status=%q, want %q", cycleID, status, want)
		}
	}

	claim := fixture.claimFor(t, eventID)
	if claim.BatchID != firstBatchID {
		t.Fatalf("claim.BatchID=%s, want the original batch %s (never the newer but equally unusable %s)",
			claim.BatchID, firstBatchID, secondBatchID)
	}
	if claim.ScanCycleID != firstCycleID {
		t.Fatalf("claim.ScanCycleID=%s, want %s", claim.ScanCycleID, firstCycleID)
	}
	// The floodgate assertion: no binding this event holds is acceptable, and
	// the unmodified verifier must still refuse every one of them.
	for _, binding := range []struct{ batchID, cycleID string }{
		{firstBatchID, firstCycleID},
		{secondBatchID, secondCycleID},
		{claim.BatchID, claim.ScanCycleID},
	} {
		if err := fixture.store.ValidateEconomicFactContext(fixture.ctx, fixture.sourceID, "usage",
			eventID, binding.batchID, binding.cycleID, claim.PayloadHash, claim.ScanCeilingAt); err == nil {
			t.Fatalf("binding batch=%s cycle=%s was accepted; this event has no valid binding at all",
				binding.batchID, binding.cycleID)
		}
	}
}

// TestClaimBindingUnchangedForEventsWithNoEconomicBinding is the control for
// the common path: an identities-stream event has no
// source_economic_scan_cycle_events row at all (validEconomicStream excludes
// that stream, and verifyFactBatchContextTx is never reached for it), so the
// claim must still use first_batch_id exactly as before.
func TestClaimBindingUnchangedForEventsWithNoEconomicBinding(t *testing.T) {
	fixture := seedDeadIngestFixture(t, "identities")
	const eventID = "88000000-0000-4000-8000-0000000000b6"
	if _, err := fixture.store.CommitSourceBatch(fixture.ctx, SourceBatchInput{
		SchemaVersion: "2.0", SourceInstanceID: fixture.sourceID, StreamID: "identities",
		BatchID: "87900001-0000-4000-8000-000000000001", Sequence: 1,
		BodyHash: testHash("identities-1"), SigningKeyID: "test-key",
		SourceRuntimeVersion: "v3-test", SourceAgentVersion: "v3-agent-test",
		SourceCapturedAt: time.Now().UTC().Truncate(time.Microsecond), ProjectionStatus: "healthy",
		Events: []SourceBatchEvent{newDeadIngestEvent(eventID, "identity_binding")},
		Actor:  AuditActor{Type: "source_connector", ID: fixture.sourceID, Reason: "identities fixture"},
	}); err != nil {
		t.Fatal(err)
	}
	fixture.reviveForClaim(t, "identities", eventID, 1)

	claim := fixture.claimFor(t, eventID)
	if claim.BatchID != "87900001-0000-4000-8000-000000000001" || claim.BatchSequence != 1 {
		t.Fatalf("claim=%+v, want the event's own first batch", claim)
	}
	if claim.SchemaVersion != "2.0" || claim.ScanCycleID != "" {
		t.Fatalf("claim=%+v, want the v2 batch context unchanged", claim)
	}
	var mappings int
	if err := fixture.store.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM source_economic_scan_cycle_events
		WHERE source_instance_id=$1 AND event_id=$2`, fixture.sourceID, eventID).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if mappings != 0 {
		t.Fatalf("identities event has %d economic mappings, want 0", mappings)
	}
}
