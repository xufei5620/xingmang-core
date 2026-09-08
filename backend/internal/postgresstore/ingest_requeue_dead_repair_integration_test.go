package postgresstore

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// deadIngestFixture is XM-INV-DEAD-REQUEUE's fixture: one v3 economic stream
// carrying real, batch-committed source_ingest_events rows, an account with
// eligibility state (so freezeEligibilityTx can run against it exactly as
// production does), and helpers to drive individual events into the terminal
// processing_status='dead' state MarkSourceEventFailed produces after eight
// attempts.
//
// The events are committed through CommitSourceBatch rather than inserted
// directly: source_ingest_events' foreign key needs a real
// source_ingest_batches row, and -- more importantly for this slice -- only
// the real path also populates source_economic_scan_cycle_events, which is
// what the scan-cycle tests below need to be about anything.
type deadIngestFixture struct {
	store     *Store
	ctx       context.Context
	sourceID  string
	accountID string
	cutover   time.Time
	chain     *v3TestChain
}

const (
	deadIngestSourceID  = "10000000-0000-4000-8000-0000000000d1"
	deadIngestAccountID = "30000000-0000-4000-8000-0000000000d1"
	deadIngestOperator  = "70000000-0000-4000-8000-0000000000d1"
)

func seedDeadIngestFixture(t *testing.T, streams ...string) deadIngestFixture {
	t.Helper()
	store, ctx := integrationStore(t)
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','dead-requeue-test','v3-test')`, deadIngestSourceID); err != nil {
		t.Fatal(err)
	}
	for _, stream := range streams {
		if err := store.ProvisionSourceStream(ctx, deadIngestSourceID, stream,
			AuditActor{Type: "system", ID: "dead-requeue-test"}); err != nil {
			t.Fatal(err)
		}
	}
	var policyStart time.Time
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_start_at FROM invoice_eligibility_policy
		WHERE singleton_id=1`).Scan(&policyStart); err != nil {
		t.Fatal(err)
	}
	cutover := policyStart.UTC().Add(-10 * time.Minute).Truncate(time.Microsecond)
	manifestHash := testHash("dead-requeue-manifest")
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'v3-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
		'p0','u0','c0','b0',$5,$5,1,'test-key')`, deadIngestSourceID, manifestHash, cutover,
		testHash("dead-requeue-config"), testHash("dead-requeue-baseline")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO invoice_users(id,oidc_issuer,oidc_subject)
		VALUES($1,'test','dead-requeue-user')`, deadIngestAccountID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO external_accounts(
		id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,binding_method,binding_status)
		VALUES($1,$1,$2,'dead-requeue',$3,'test','verified')`, deadIngestAccountID, deadIngestSourceID,
		testHash("dead-requeue-subject")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_account_eligibility_state(
		external_account_id,source_instance_id,cutover_at,unit_code,cutover_balance_units,
		cutover_manifest_hash,finalized_through,finalization_delay_seconds)
		VALUES($1,$2,$3,'SUB2_BALANCE_1E8',0,$4,$3,900)`, deadIngestAccountID, deadIngestSourceID,
		cutover, manifestHash); err != nil {
		t.Fatal(err)
	}
	return deadIngestFixture{store: store, ctx: ctx, sourceID: deadIngestSourceID,
		accountID: deadIngestAccountID, cutover: cutover, chain: newV3TestChain()}
}

// commitEvents commits one complete v3 scan cycle carrying the given events,
// through the real CommitSourceBatch path.
func (f deadIngestFixture) commitEvents(t *testing.T, stream, cycleID string, events []SourceBatchEvent) v3TestCycle {
	t.Helper()
	ceiling := time.Now().UTC().Truncate(time.Microsecond)
	return f.chain.commit(t, f.store, f.ctx, f.sourceID, stream, cycleID, ceiling, events)
}

// commitSupersedingCycle commits a brand-new scan cycle through the real
// CommitSourceBatch path with the Now/StaleActiveScanCycleMaxAge fields set
// so supersedeStaleActiveScanCycleTx (XM-INV-SCAN-CYCLE-SUPERSEDE) fires and
// marks the stream's current active cycle 'blocked' -- exactly what an agent
// restart does in production. Passing an event a previous cycle already
// carried reproduces the other half of that shape: agent event ids are
// deterministic (agents/sourceagent/batch.go's deterministicUUID), so
// CommitSourceBatch finds the row, leaves it exactly as it is -- dead
// included -- and only adds a fresh mapping row under the new cycle.
func (f deadIngestFixture) commitSupersedingCycle(t *testing.T, stream, cycleID string, events []SourceBatchEvent) {
	t.Helper()
	f.chain.sequence[stream]++
	sequence := f.chain.sequence[stream]
	batchID := fmt.Sprintf("87%06d-0000-4000-8000-%012d", sequence, sequence)
	bodyHash := testHash(stream + cycleID + fmt.Sprint(sequence))
	ceiling := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	input := SourceBatchInput{
		SchemaVersion: "3.0", SourceInstanceID: f.sourceID, StreamID: stream,
		BatchID: batchID, Sequence: sequence, BodyHash: bodyHash,
		PreviousBatchHash: f.chain.hash[stream], SigningKeyID: "test-key",
		SourceRuntimeVersion: "v3-test", SourceAgentVersion: "v3-agent-test",
		SourceCapturedAt: ceiling, ProjectionStatus: "healthy",
		StreamWatermarkAt: ceiling, SourceCursor: fmt.Sprintf("%s:%d", stream, sequence),
		ScanCeilingAt: ceiling, ScanCeilingCursor: "ceiling:" + cycleID,
		ScanCycleID: cycleID, ScanComplete: true, Events: events,
		Actor: AuditActor{Type: "source_connector", ID: f.sourceID, Reason: "dead requeue supersede fixture"},
		// One hour "later" than the cycle it supersedes, against a one-minute
		// staleness budget, so the existing active cycle is unambiguously
		// stale by economicRescanActivityWithinWindow's own test.
		Now: time.Now().UTC().Add(time.Hour), StaleActiveScanCycleMaxAge: time.Minute,
	}
	// The balances stream carries a signed snapshot id and row count, and
	// validateSourceBatch rejects a v3 balances batch without them. Derived
	// exactly as v3TestChain.commitPageWithSnapshotRows does, so the count
	// matches what tryPublishEconomicScanCyclesTx compares it against.
	if stream == "balances" {
		input.ScanSnapshotID = testHash(cycleID)
		for _, event := range events {
			if event.EntityType == "balance_checkpoint" {
				input.ScanSnapshotRowCount++
			}
		}
	}
	if _, err := f.store.CommitSourceBatch(f.ctx, input); err != nil {
		t.Fatalf("commit superseding %s cycle: %v", stream, err)
	}
	f.chain.hash[stream] = bodyHash
}

// newDeadIngestEvent builds one batch event with a deterministic, distinct
// payload hash derived from its own id.
func newDeadIngestEvent(eventID, entityType string) SourceBatchEvent {
	return SourceBatchEvent{
		EventID: eventID, EntityType: entityType, Operation: "upsert",
		PayloadHash:       testHash("dead-requeue-payload-" + eventID),
		PayloadCiphertext: bytes.Repeat([]byte{7}, 32),
		ObservedAt:        time.Now().UTC().Truncate(time.Microsecond),
	}
}

// killEvent drives one committed event into exactly the state
// MarkSourceEventFailed leaves behind on its eighth consecutive failure:
// processing_status='dead', attempt_count=8, a PROJECTION_FAILED error, and
// timestamps aged so the row looks like production's (dead for hours, not
// milliseconds). Written directly rather than by looping the real processor
// eight times because the failure this repairs is a serialization storm the
// store layer cannot reproduce deterministically, and every column this
// repair reads or writes is pinned explicitly here.
func (f deadIngestFixture) killEvent(t *testing.T, stream, eventID string, deadFor time.Duration) {
	t.Helper()
	command, err := f.store.pool.Exec(f.ctx, `
		UPDATE source_ingest_events SET processing_status='dead',attempt_count=8,
			processing_error='PROJECTION_FAILED',next_attempt_at=now()-$4::interval,
			created_at=now()-$4::interval-interval '5 seconds',updated_at=now()-$4::interval,
			lease_token=NULL,lease_expires_at=NULL
		WHERE source_instance_id=$1 AND stream_id=$2 AND event_id=$3`,
		f.sourceID, stream, eventID, fmt.Sprintf("%d seconds", int64(deadFor.Seconds())))
	if err != nil {
		t.Fatal(err)
	}
	if command.RowsAffected() != 1 {
		t.Fatalf("killEvent matched %d rows for %s", command.RowsAffected(), eventID)
	}
}

// freezeOverEvent opens the EVENT_DEAD freeze MarkSourceEventFailed's own
// dead branch opens beside such an event, through the production helper
// (freezeEligibilityTx) so the freeze row, the account's eligibility_status
// flip and the audit row all have exactly the production shape. Returns the
// freeze id.
func (f deadIngestFixture) freezeOverEvent(t *testing.T, eventID, entityType string) string {
	t.Helper()
	tx, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = freezeEligibilityTx(f.ctx, tx, f.accountID, "", "EVENT_DEAD", entityType, eventID,
		testHash("dead-requeue-payload-"+eventID),
		AuditActor{Type: "source_connector", ID: f.sourceID, Reason: "dead event fixture"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	var freezeID string
	if err = f.store.pool.QueryRow(f.ctx, `SELECT id::text FROM eligibility_freezes
		WHERE external_account_id=$1 AND status='open' AND trigger_object_id=$2`,
		f.accountID, eventID).Scan(&freezeID); err != nil {
		t.Fatal(err)
	}
	return freezeID
}

type ingestEventRow struct {
	status          string
	attemptCount    int64
	processingError string
	leaseToken      string
	nextAttemptDue  bool
	createdAt       time.Time
	updatedAt       time.Time
}

// readIngestEvent reads back every column this repair reads or writes.
// nextAttemptDue is evaluated against the database's own clock rather than
// Go's, so the "claimable right now" assertion cannot flake on clock skew
// between the test process and PostgreSQL.
func (f deadIngestFixture) readIngestEvent(t *testing.T, stream, eventID string) ingestEventRow {
	t.Helper()
	var row ingestEventRow
	if err := f.store.pool.QueryRow(f.ctx, `
		SELECT processing_status,attempt_count,COALESCE(processing_error,''),
			COALESCE(lease_token,''),next_attempt_at<=now(),created_at,updated_at
		FROM source_ingest_events WHERE source_instance_id=$1 AND stream_id=$2 AND event_id=$3`,
		f.sourceID, stream, eventID).Scan(&row.status, &row.attemptCount, &row.processingError,
		&row.leaseToken, &row.nextAttemptDue, &row.createdAt, &row.updatedAt); err != nil {
		t.Fatal(err)
	}
	return row
}

func (f deadIngestFixture) requeueAuditCount(t *testing.T, eventID string) int {
	t.Helper()
	var count int
	if err := f.store.pool.QueryRow(f.ctx, `SELECT count(*) FROM audit_events
		WHERE action='source_ingest_event.repair_requeued' AND object_type='source_ingest_event'
			AND object_id=$1`, eventID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f deadIngestFixture) cycleStatus(t *testing.T, stream, cycleID string) string {
	t.Helper()
	var status string
	if err := f.store.pool.QueryRow(f.ctx, `SELECT cycle_status FROM source_economic_scan_cycles
		WHERE source_instance_id=$1 AND stream_id=$2 AND scan_cycle_id=$3::uuid`,
		f.sourceID, stream, cycleID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

// publishCycle runs the real tryPublishEconomicScanCyclesTx, the single
// function that decides whether a cycle's events are complete enough to
// publish its stream watermark.
func (f deadIngestFixture) publishCycle(t *testing.T, stream string) {
	t.Helper()
	tx, err := f.store.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = tryPublishEconomicScanCyclesTx(f.ctx, tx, f.sourceID, stream,
		AuditActor{Type: "source_connector", ID: f.sourceID, Reason: "dead requeue fixture publish attempt"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
}

func (f deadIngestFixture) markProcessed(t *testing.T, stream, eventID string) {
	t.Helper()
	command, err := f.store.pool.Exec(f.ctx, `
		UPDATE source_ingest_events SET processing_status='processed',processed_at=now(),
			processing_error=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=now()
		WHERE source_instance_id=$1 AND stream_id=$2 AND event_id=$3`, f.sourceID, stream, eventID)
	if err != nil {
		t.Fatal(err)
	}
	if command.RowsAffected() != 1 {
		t.Fatalf("markProcessed matched %d rows", command.RowsAffected())
	}
}

// TestIngestRequeueDeadDryRunWritesNothing is the dry-run half of
// XM-INV-DEAD-REQUEUE's contract, kept in its own test (rather than folded
// into the apply test the way the projection sibling does it) precisely so
// that the mutation which proves it -- forcing the dry-run branch to take
// the apply path -- turns exactly this test red while every apply-side test
// below stays green as a control.
//
// It asserts both halves of "reports what it would do, writes nothing": the
// summary carries the found state (attempts, error text, dead-since, the
// scan cycle, the open freeze), and every column of the row, the freeze, and
// the audit table is exactly as it was.
func TestIngestRequeueDeadDryRunWritesNothing(t *testing.T) {
	fixture := seedDeadIngestFixture(t, "usage")
	const eventID = "88000000-0000-4000-8000-0000000000d1"
	const cycleID = "89000000-0000-4000-8000-0000000000d1"
	fixture.commitEvents(t, "usage", cycleID, []SourceBatchEvent{newDeadIngestEvent(eventID, "usage_event")})
	fixture.killEvent(t, "usage", eventID, 20*time.Hour)
	freezeID := fixture.freezeOverEvent(t, eventID, "usage_event")
	before := fixture.readIngestEvent(t, "usage", eventID)

	result, err := fixture.store.RepairIngestRequeueDead(fixture.ctx,
		IngestRequeueDeadRepairInput{Apply: false}, AuditActor{Type: "admin", ID: deadIngestOperator})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.TotalRequeued != 1 || len(result.Events) != 1 || len(result.Errors) != 0 {
		t.Fatalf("dry run result=%+v", result)
	}
	found := result.Events[0]
	if found.EventID != eventID || !found.Requeued || found.PreviousAttempts != 8 ||
		found.ProcessingError != "PROJECTION_FAILED" || found.EntityType != "usage_event" ||
		found.StreamID != "usage" || found.SourceInstanceID != fixture.sourceID {
		t.Fatalf("dry run must report the found state: %+v", found)
	}
	if found.DeadSince.IsZero() || found.CreatedAt.IsZero() || !found.CreatedAt.Before(found.DeadSince) {
		t.Fatalf("dry run must report both timestamps, created before dead: created=%s dead=%s",
			found.CreatedAt, found.DeadSince)
	}
	if len(found.ScanCycles) != 1 || found.ScanCycles[0].ScanCycleID != cycleID ||
		found.ScanCycles[0].CycleStatus != "processing" || !found.ScanCycles[0].ReplayBinding {
		t.Fatalf("dry run must report the event's scan cycles and mark the replay one: %+v", found.ScanCycles)
	}
	if found.ReplayScanCycleID != cycleID || found.ReplayCycleStatus != "processing" ||
		found.ReplayBatchID == "" || found.ReplayBlocked || found.ReplayBlockedReason != "" {
		t.Fatalf("dry run must resolve the replay binding and find it acceptable: %+v", found)
	}
	if len(found.OpenFreezes) != 1 || found.OpenFreezes[0].FreezeID != freezeID ||
		found.OpenFreezes[0].ExternalAccountID != fixture.accountID ||
		found.OpenFreezes[0].FreezeReason != "EVENT_DEAD" {
		t.Fatalf("dry run must report the correlated open freezes: %+v", found.OpenFreezes)
	}

	after := fixture.readIngestEvent(t, "usage", eventID)
	if after != before {
		t.Fatalf("dry run wrote to the ingest row: before=%+v after=%+v", before, after)
	}
	if count := fixture.requeueAuditCount(t, eventID); count != 0 {
		t.Fatalf("dry run wrote %d requeue audit rows, want 0", count)
	}
	var freezeStatus string
	var resolutionVersion int64
	if err = fixture.store.pool.QueryRow(fixture.ctx, `SELECT status,resolution_version
		FROM eligibility_freezes WHERE id=$1`, freezeID).Scan(&freezeStatus, &resolutionVersion); err != nil {
		t.Fatal(err)
	}
	if freezeStatus != "open" || resolutionVersion != 1 {
		t.Fatalf("dry run touched the freeze: status=%q resolution_version=%d", freezeStatus, resolutionVersion)
	}
}

// TestIngestRequeueDeadApplyRequeuesEveryRowWithItsOwnAudit reproduces
// production's exact shape -- two dead usage facts and one dead balance
// checkpoint for the same customer -- and pins that apply writes one audit
// row per requeued event, keyed to that event's own id, not a single summary
// row for the run.
func TestIngestRequeueDeadApplyRequeuesEveryRowWithItsOwnAudit(t *testing.T) {
	fixture := seedDeadIngestFixture(t, "usage", "balances")
	usageEvents := []SourceBatchEvent{
		newDeadIngestEvent("88000000-0000-4000-8000-0000000000e1", "usage_event"),
		newDeadIngestEvent("88000000-0000-4000-8000-0000000000e2", "usage_event"),
	}
	balanceEvent := newDeadIngestEvent("88000000-0000-4000-8000-0000000000e3", "balance_checkpoint")
	fixture.commitEvents(t, "usage", "89000000-0000-4000-8000-0000000000e1", usageEvents)
	fixture.commitEvents(t, "balances", "89000000-0000-4000-8000-0000000000e2", []SourceBatchEvent{balanceEvent})
	type deadRow struct{ stream, eventID, entityType string }
	dead := []deadRow{
		{"usage", usageEvents[0].EventID, "usage_event"},
		{"usage", usageEvents[1].EventID, "usage_event"},
		{"balances", balanceEvent.EventID, "balance_checkpoint"},
	}
	for _, row := range dead {
		fixture.killEvent(t, row.stream, row.eventID, 20*time.Hour)
		fixture.freezeOverEvent(t, row.eventID, row.entityType)
	}

	result, err := fixture.store.RepairIngestRequeueDead(fixture.ctx,
		IngestRequeueDeadRepairInput{Apply: true, OperatorID: deadIngestOperator},
		AuditActor{Type: "admin", ID: deadIngestOperator})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.TotalRequeued != 3 || len(result.Events) != 3 || len(result.Errors) != 0 {
		t.Fatalf("apply result=%+v errors=%+v", result.Events, result.Errors)
	}
	for _, row := range dead {
		after := fixture.readIngestEvent(t, row.stream, row.eventID)
		if after.status != "queued" {
			t.Fatalf("%s: processing_status=%q, want queued", row.eventID, after.status)
		}
		if after.attemptCount != 0 {
			t.Fatalf("%s: attempt_count=%d, want 0 (ClaimUnprocessedSourceEvents filters attempt_count<8)",
				row.eventID, after.attemptCount)
		}
		if after.processingError != "" {
			t.Fatalf("%s: processing_error=%q, want cleared", row.eventID, after.processingError)
		}
		if after.leaseToken != "" {
			t.Fatalf("%s: lease_token=%q, want cleared", row.eventID, after.leaseToken)
		}
		if !after.nextAttemptDue {
			t.Fatalf("%s: next_attempt_at is still in the future", row.eventID)
		}
		// created_at is provenance -- when the event actually arrived -- and
		// must survive the repair even though leaving it makes the requeued
		// row immediately "old" to readiness. See the input type's own doc
		// comment.
		if !after.createdAt.Equal(after.createdAt.UTC()) || time.Since(after.createdAt) < 19*time.Hour {
			t.Fatalf("%s: created_at=%s was rewritten by the repair", row.eventID, after.createdAt)
		}
		if count := fixture.requeueAuditCount(t, row.eventID); count != 1 {
			t.Fatalf("%s: requeue audit rows=%d, want exactly 1 per requeued event", row.eventID, count)
		}
	}

	// Re-running is a defined no-op: nothing is 'dead' any more, so nothing
	// is found and no second audit row is written.
	second, err := fixture.store.RepairIngestRequeueDead(fixture.ctx,
		IngestRequeueDeadRepairInput{Apply: true, OperatorID: deadIngestOperator},
		AuditActor{Type: "admin", ID: deadIngestOperator})
	if err != nil {
		t.Fatal(err)
	}
	if second.TotalRequeued != 0 || len(second.Events) != 0 {
		t.Fatalf("second apply was not a no-op: %+v", second)
	}
	for _, row := range dead {
		if count := fixture.requeueAuditCount(t, row.eventID); count != 1 {
			t.Fatalf("%s: audit rows=%d after re-run, want still 1", row.eventID, count)
		}
	}
}

// TestIngestRequeueDeadRequeuedEventIsClaimableAgain is the assertion that
// actually matters operationally: not that processing_status flipped, but
// that ClaimUnprocessedSourceEvents -- the real worker's claim query, with
// its `attempt_count < 8 AND next_attempt_at <= now` predicate and its lease
// bookkeeping -- picks the row up again on the next tick.
//
// This is what makes attempt_count=0 load-bearing rather than cosmetic: a
// row left at attempt_count=8 and merely flipped to 'queued' is filtered out
// of this query entirely and would sit pending forever.
func TestIngestRequeueDeadRequeuedEventIsClaimableAgain(t *testing.T) {
	fixture := seedDeadIngestFixture(t, "usage")
	const eventID = "88000000-0000-4000-8000-0000000000f1"
	fixture.commitEvents(t, "usage", "89000000-0000-4000-8000-0000000000f1",
		[]SourceBatchEvent{newDeadIngestEvent(eventID, "usage_event")})
	fixture.killEvent(t, "usage", eventID, 20*time.Hour)
	fixture.freezeOverEvent(t, eventID, "usage_event")

	// Before the repair the worker cannot see it at all.
	claims, err := fixture.store.ClaimUnprocessedSourceEvents(fixture.ctx, 10, time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 0 {
		t.Fatalf("a dead event was claimable before the repair: %+v", claims)
	}

	if _, err = fixture.store.RepairIngestRequeueDead(fixture.ctx,
		IngestRequeueDeadRepairInput{Apply: true, OperatorID: deadIngestOperator},
		AuditActor{Type: "admin", ID: deadIngestOperator}); err != nil {
		t.Fatal(err)
	}

	claims, err = fixture.store.ClaimUnprocessedSourceEvents(fixture.ctx, 10, time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].EventID != eventID {
		t.Fatalf("requeued event was not claimable: %+v", claims)
	}
	// Attempt is the claim's own post-increment view: a fresh ladder starts
	// at 1, not at 9.
	if claims[0].Attempt != 1 {
		t.Fatalf("claimed attempt=%d, want 1 (a full fresh retry ladder)", claims[0].Attempt)
	}
	if claims[0].LeaseToken == "" || claims[0].PayloadHash != testHash("dead-requeue-payload-"+eventID) {
		t.Fatalf("claim did not carry a usable lease/payload: %+v", claims[0])
	}
	after := fixture.readIngestEvent(t, "usage", eventID)
	if after.status != "processing" || after.attemptCount != 1 {
		t.Fatalf("after claim: status=%q attempt_count=%d, want processing/1", after.status, after.attemptCount)
	}
}

// TestIngestRequeueDeadLeavesFreezesAndAccountStateUntouched is
// XM-INV-DEAD-REQUEUE's "never touches eligibility_freezes" contract as an
// executable assertion. It is an absence assertion, so it is mutation-checked
// by adding a freeze-resolving UPDATE to the apply path and confirming this
// test -- and only this test -- goes red (see the handoff's mutation table).
func TestIngestRequeueDeadLeavesFreezesAndAccountStateUntouched(t *testing.T) {
	fixture := seedDeadIngestFixture(t, "usage")
	const eventID = "88000000-0000-4000-8000-0000000000f2"
	fixture.commitEvents(t, "usage", "89000000-0000-4000-8000-0000000000f2",
		[]SourceBatchEvent{newDeadIngestEvent(eventID, "usage_event")})
	fixture.killEvent(t, "usage", eventID, 20*time.Hour)
	freezeID := fixture.freezeOverEvent(t, eventID, "usage_event")

	if _, err := fixture.store.RepairIngestRequeueDead(fixture.ctx,
		IngestRequeueDeadRepairInput{Apply: true, OperatorID: deadIngestOperator},
		AuditActor{Type: "admin", ID: deadIngestOperator}); err != nil {
		t.Fatal(err)
	}

	var status string
	var resolutionVersion int64
	var resolvedAt, resolvedBy, evidenceHash, noteHash *string
	if err := fixture.store.pool.QueryRow(fixture.ctx, `
		SELECT status,resolution_version,resolved_at::text,resolved_by::text,
			resolution_evidence_hash,resolution_note_hash
		FROM eligibility_freezes WHERE id=$1`, freezeID).Scan(&status, &resolutionVersion,
		&resolvedAt, &resolvedBy, &evidenceHash, &noteHash); err != nil {
		t.Fatal(err)
	}
	if status != "open" || resolutionVersion != 1 || resolvedAt != nil || resolvedBy != nil ||
		evidenceHash != nil || noteHash != nil {
		t.Fatalf("the repair resolved a freeze: status=%q version=%d resolved_at=%v resolved_by=%v",
			status, resolutionVersion, resolvedAt, resolvedBy)
	}
	if count := openFreezeCount(t, fixture.store, fixture.ctx, fixture.accountID); count != 1 {
		t.Fatalf("open freeze count=%d, want 1 (the repair must neither resolve nor open one)", count)
	}
	// The account itself must also stay frozen: filling the data hole is not
	// the same judgment as declaring the account clean, and ResolveEligibilityFreeze
	// is the only thing allowed to make that second call.
	var eligibilityStatus string
	if err := fixture.store.pool.QueryRow(fixture.ctx, `SELECT eligibility_status
		FROM source_account_eligibility_state WHERE external_account_id=$1`,
		fixture.accountID).Scan(&eligibilityStatus); err != nil {
		t.Fatal(err)
	}
	if eligibilityStatus != "frozen" {
		t.Fatalf("eligibility_status=%q, want frozen", eligibilityStatus)
	}
	// And no freeze-resolution audit row was written under any actor.
	var resolvedAudits int
	if err := fixture.store.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM audit_events
		WHERE action='eligibility.freeze.resolved'`).Scan(&resolvedAudits); err != nil {
		t.Fatal(err)
	}
	if resolvedAudits != 0 {
		t.Fatalf("freeze-resolution audit rows=%d, want 0", resolvedAudits)
	}
}

// TestIngestRequeueDeadDoesNotReopenAPublishedScanCycle is design question 3
// for the case that matters in production: the dead event's scan cycle has
// already published (its open freeze let tryPublishEconomicScanCyclesTx count
// it complete). Requeuing must leave that cycle published and its stream
// watermark untouched -- nothing re-evaluates a published cycle, because that
// function only ever selects cycle_status='processing'.
func TestIngestRequeueDeadDoesNotReopenAPublishedScanCycle(t *testing.T) {
	fixture := seedDeadIngestFixture(t, "usage")
	const liveEventID = "88000000-0000-4000-8000-0000000000f3"
	const deadEventID = "88000000-0000-4000-8000-0000000000f4"
	const cycleID = "89000000-0000-4000-8000-0000000000f3"
	fixture.commitEvents(t, "usage", cycleID, []SourceBatchEvent{
		newDeadIngestEvent(liveEventID, "usage_event"),
		newDeadIngestEvent(deadEventID, "usage_event"),
	})
	fixture.markProcessed(t, "usage", liveEventID)
	fixture.killEvent(t, "usage", deadEventID, 20*time.Hour)
	fixture.freezeOverEvent(t, deadEventID, "usage_event")

	fixture.publishCycle(t, "usage")
	if status := fixture.cycleStatus(t, "usage", cycleID); status != "published" {
		t.Fatalf("cycle_status=%q before the repair, want published (dead+frozen counts as complete)", status)
	}
	var watermarkBefore time.Time
	var sequenceBefore int64
	if err := fixture.store.pool.QueryRow(fixture.ctx, `SELECT watermark_at,source_sequence
		FROM source_economic_stream_watermarks WHERE source_instance_id=$1 AND stream_kind='usage'`,
		fixture.sourceID).Scan(&watermarkBefore, &sequenceBefore); err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.store.RepairIngestRequeueDead(fixture.ctx,
		IngestRequeueDeadRepairInput{Apply: true, OperatorID: deadIngestOperator},
		AuditActor{Type: "admin", ID: deadIngestOperator}); err != nil {
		t.Fatal(err)
	}

	if status := fixture.cycleStatus(t, "usage", cycleID); status != "published" {
		t.Fatalf("cycle_status=%q after the repair, want published (a published cycle is never re-evaluated)", status)
	}
	// Another publish pass (the source processor runs one after every event
	// mark) must not disturb it either.
	fixture.publishCycle(t, "usage")
	if status := fixture.cycleStatus(t, "usage", cycleID); status != "published" {
		t.Fatalf("cycle_status=%q after a further publish pass, want published", status)
	}
	var watermarkAfter time.Time
	var sequenceAfter int64
	if err := fixture.store.pool.QueryRow(fixture.ctx, `SELECT watermark_at,source_sequence
		FROM source_economic_stream_watermarks WHERE source_instance_id=$1 AND stream_kind='usage'`,
		fixture.sourceID).Scan(&watermarkAfter, &sequenceAfter); err != nil {
		t.Fatal(err)
	}
	if !watermarkAfter.Equal(watermarkBefore) || sequenceAfter != sequenceBefore {
		t.Fatalf("published watermark moved: before=%s/%d after=%s/%d",
			watermarkBefore, sequenceBefore, watermarkAfter, sequenceAfter)
	}
}

// TestIngestRequeueDeadHoldsAnUnpublishedScanCycleUntilTheEventTerminates is
// the other half of design question 3, and the one an operator needs to know
// about before applying: while the dead event's cycle is still 'processing',
// requeuing genuinely does make that cycle incomplete again (a 'queued' event
// is no longer covered by the `failed/dead + open freeze` exemption), so the
// cycle stops publishing until the event terminates -- and publishes as soon
// as it does. This is why the summary reports every cycle's status in dry-run
// mode.
func TestIngestRequeueDeadHoldsAnUnpublishedScanCycleUntilTheEventTerminates(t *testing.T) {
	fixture := seedDeadIngestFixture(t, "usage")
	const eventID = "88000000-0000-4000-8000-0000000000f5"
	const cycleID = "89000000-0000-4000-8000-0000000000f5"
	fixture.commitEvents(t, "usage", cycleID, []SourceBatchEvent{newDeadIngestEvent(eventID, "usage_event")})
	fixture.killEvent(t, "usage", eventID, 20*time.Hour)
	fixture.freezeOverEvent(t, eventID, "usage_event")
	if status := fixture.cycleStatus(t, "usage", cycleID); status != "processing" {
		t.Fatalf("cycle_status=%q, want processing before any publish attempt", status)
	}

	if _, err := fixture.store.RepairIngestRequeueDead(fixture.ctx,
		IngestRequeueDeadRepairInput{Apply: true, OperatorID: deadIngestOperator},
		AuditActor{Type: "admin", ID: deadIngestOperator}); err != nil {
		t.Fatal(err)
	}

	// The requeued event now counts as incomplete, so the cycle must not
	// publish -- exactly the behavior that would have published it a moment
	// earlier, while the event was dead with its freeze standing.
	fixture.publishCycle(t, "usage")
	if status := fixture.cycleStatus(t, "usage", cycleID); status != "processing" {
		t.Fatalf("cycle_status=%q after requeue, want processing (a queued event holds its cycle)", status)
	}

	// It self-heals the moment the event terminates, in either direction.
	fixture.markProcessed(t, "usage", eventID)
	fixture.publishCycle(t, "usage")
	if status := fixture.cycleStatus(t, "usage", cycleID); status != "published" {
		t.Fatalf("cycle_status=%q after the requeued event processed, want published", status)
	}
}

// TestIngestRequeueDeadNarrowsByEventAndByAccount covers both narrowing
// flags, including the account filter's own correlation path (open freeze
// source_revision_hash = event payload_hash) and its documented blind spot: a
// dead event with no freeze is invisible to --account and reachable only
// unnarrowed or by --event.
func TestIngestRequeueDeadNarrowsByEventAndByAccount(t *testing.T) {
	fixture := seedDeadIngestFixture(t, "usage")
	const frozenEventID = "88000000-0000-4000-8000-0000000000f6"
	const unfrozenEventID = "88000000-0000-4000-8000-0000000000f7"
	fixture.commitEvents(t, "usage", "89000000-0000-4000-8000-0000000000f6", []SourceBatchEvent{
		newDeadIngestEvent(frozenEventID, "usage_event"),
		newDeadIngestEvent(unfrozenEventID, "usage_event"),
	})
	fixture.killEvent(t, "usage", frozenEventID, 20*time.Hour)
	fixture.killEvent(t, "usage", unfrozenEventID, 20*time.Hour)
	fixture.freezeOverEvent(t, frozenEventID, "usage_event")

	unnarrowed, err := fixture.store.RepairIngestRequeueDead(fixture.ctx,
		IngestRequeueDeadRepairInput{}, AuditActor{Type: "admin", ID: deadIngestOperator})
	if err != nil {
		t.Fatal(err)
	}
	if len(unnarrowed.Events) != 2 {
		t.Fatalf("unnarrowed dry run found %d events, want 2", len(unnarrowed.Events))
	}

	byEvent, err := fixture.store.RepairIngestRequeueDead(fixture.ctx,
		IngestRequeueDeadRepairInput{EventID: unfrozenEventID}, AuditActor{Type: "admin", ID: deadIngestOperator})
	if err != nil {
		t.Fatal(err)
	}
	if len(byEvent.Events) != 1 || byEvent.Events[0].EventID != unfrozenEventID {
		t.Fatalf("--event narrowing found %+v", byEvent.Events)
	}
	if len(byEvent.Events[0].OpenFreezes) != 0 {
		t.Fatalf("the unfrozen event reported freezes: %+v", byEvent.Events[0].OpenFreezes)
	}

	byAccount, err := fixture.store.RepairIngestRequeueDead(fixture.ctx,
		IngestRequeueDeadRepairInput{AccountID: fixture.accountID}, AuditActor{Type: "admin", ID: deadIngestOperator})
	if err != nil {
		t.Fatal(err)
	}
	if len(byAccount.Events) != 1 || byAccount.Events[0].EventID != frozenEventID {
		t.Fatalf("--account narrowing found %+v (it correlates only through open freezes)", byAccount.Events)
	}

	// An account with no correlating freeze finds nothing rather than
	// everything -- the filter must never silently degrade to unnarrowed.
	other, err := fixture.store.RepairIngestRequeueDead(fixture.ctx,
		IngestRequeueDeadRepairInput{AccountID: "30000000-0000-4000-8000-0000000000d9"},
		AuditActor{Type: "admin", ID: deadIngestOperator})
	if err != nil {
		t.Fatal(err)
	}
	if len(other.Events) != 0 {
		t.Fatalf("--account for an unrelated account found %+v", other.Events)
	}
}

// TestIngestRequeueDeadRequeuesAnEventRescuedByALaterValidBinding is the
// production shape found on 2026-09-08 -- an agent restart superseded the
// cycle the dead event's own first_batch_id belongs to (now 'blocked'), and
// the agent's next scan re-delivered the identical deterministic event id
// under a cycle that went on to publish -- read *after* XM-INV-CLAIM-BINDING.
//
// Before that slice the claim was pinned to first_batch_id, so the replay was
// verified against the blocked binding and refused, and this repair correctly
// refused to requeue. Now the claim prefers the newest valid binding, so the
// published one governs and the event is replayable again. The repair must
// reach the *same* verdict as the runtime: it resolves the binding with the
// very SQL the claim uses (claimBindingSelect), so the two cannot disagree.
//
// This test is the executable form of the expected dry-run change for the two
// production usage events.
func TestIngestRequeueDeadRequeuesAnEventRescuedByALaterValidBinding(t *testing.T) {
	fixture := seedDeadIngestFixture(t, "usage")
	const eventID = "88000000-0000-4000-8000-0000000000a1"
	const supersededCycleID = "89000000-0000-4000-8000-0000000000a1"
	const successorCycleID = "89000000-0000-4000-8000-0000000000a2"
	event := newDeadIngestEvent(eventID, "usage_event")
	fixture.commitEvents(t, "usage", supersededCycleID, []SourceBatchEvent{event})
	fixture.killEvent(t, "usage", eventID, 20*time.Hour)
	fixture.freezeOverEvent(t, eventID, "usage_event")
	fixture.commitSupersedingCycle(t, "usage", successorCycleID, []SourceBatchEvent{event})
	if status := fixture.cycleStatus(t, "usage", supersededCycleID); status != "blocked" {
		t.Fatalf("superseded cycle status=%q, want blocked", status)
	}
	if status := fixture.cycleStatus(t, "usage", successorCycleID); status != "published" {
		t.Fatalf("successor cycle status=%q, want published", status)
	}
	if before := fixture.readIngestEvent(t, "usage", eventID); before.status != "dead" {
		t.Fatalf("re-delivery must not revive the dead row: status=%q", before.status)
	}

	dryRun, err := fixture.store.RepairIngestRequeueDead(fixture.ctx,
		IngestRequeueDeadRepairInput{}, AuditActor{Type: "admin", ID: deadIngestOperator})
	if err != nil {
		t.Fatal(err)
	}
	if len(dryRun.Events) != 1 {
		t.Fatalf("events=%+v, want one", dryRun.Events)
	}
	found := dryRun.Events[0]
	if found.ReplayBlocked || !found.Requeued {
		t.Fatalf("verdict=%+v, want replayable via the published binding", found)
	}
	if found.ReplayScanCycleID != successorCycleID || found.ReplayCycleStatus != "published" {
		t.Fatalf("replay binding=%s/%s, want the published successor %s",
			found.ReplayScanCycleID, found.ReplayCycleStatus, successorCycleID)
	}
	if dryRun.TotalRequeued != 1 || dryRun.TotalBlockedSkipped != 0 {
		t.Fatalf("totals requeued=%d blocked=%d, want 1/0", dryRun.TotalRequeued, dryRun.TotalBlockedSkipped)
	}
	// Both mappings are still reported, and exactly the published one is now
	// marked as the binding that governs.
	if len(found.ScanCycles) != 2 {
		t.Fatalf("scan cycles=%+v, want both mappings reported", found.ScanCycles)
	}
	for _, cycle := range found.ScanCycles {
		if wantReplay := cycle.ScanCycleID == successorCycleID; cycle.ReplayBinding != wantReplay {
			t.Fatalf("cycle %s ReplayBinding=%t, want %t", cycle.ScanCycleID, cycle.ReplayBinding, wantReplay)
		}
	}

	// Apply, then prove the prediction matches the runtime: the worker claims
	// the row and carries the same binding the report named.
	if _, err := fixture.store.RepairIngestRequeueDead(fixture.ctx,
		IngestRequeueDeadRepairInput{Apply: true, OperatorID: deadIngestOperator},
		AuditActor{Type: "admin", ID: deadIngestOperator}); err != nil {
		t.Fatal(err)
	}
	claim := fixture.claimFor(t, eventID)
	if claim.ScanCycleID != found.ReplayScanCycleID || claim.BatchID != found.ReplayBatchID {
		t.Fatalf("claim carried %s/%s but the report predicted %s/%s; prediction and runtime disagree",
			claim.BatchID, claim.ScanCycleID, found.ReplayBatchID, found.ReplayScanCycleID)
	}
	if err := fixture.store.ValidateEconomicFactContext(fixture.ctx, fixture.sourceID, "usage",
		claim.EventID, claim.BatchID, claim.ScanCycleID, claim.PayloadHash, claim.ScanCeilingAt); err != nil {
		t.Fatalf("the verifier refused the binding the repair promised: %v", err)
	}
}

// TestIngestRequeueDeadSkipsWhenEveryBindingIsUnusable is the balance
// checkpoint's shape, and the reverse of the test above: an event whose
// bindings are *all* unusable stays skipped, with the totals and the reason
// saying so. Two bindings on different batches, so "picked the newest" and
// "fell back to the first" are distinguishable outcomes.
func TestIngestRequeueDeadSkipsWhenEveryBindingIsUnusable(t *testing.T) {
	fixture := seedDeadIngestFixture(t, "usage")
	const eventID = "88000000-0000-4000-8000-0000000000a5"
	const otherEventID = "88000000-0000-4000-8000-0000000000a6"
	const firstCycleID = "89000000-0000-4000-8000-0000000000a5"
	const secondCycleID = "89000000-0000-4000-8000-0000000000a6"
	const thirdCycleID = "89000000-0000-4000-8000-0000000000a7"
	event := newDeadIngestEvent(eventID, "usage_event")
	fixture.commitEvents(t, "usage", firstCycleID, []SourceBatchEvent{event})
	fixture.killEvent(t, "usage", eventID, 20*time.Hour)
	// No freeze: the dead event keeps each cycle incomplete, so cycle 2 can be
	// superseded by cycle 3 and both of this event's bindings end up blocked.
	fixture.commitSupersedingCycle(t, "usage", secondCycleID, []SourceBatchEvent{event})
	fixture.commitSupersedingCycle(t, "usage", thirdCycleID,
		[]SourceBatchEvent{newDeadIngestEvent(otherEventID, "usage_event")})
	for _, cycleID := range []string{firstCycleID, secondCycleID} {
		if status := fixture.cycleStatus(t, "usage", cycleID); status != "blocked" {
			t.Fatalf("cycle %s status=%q, want blocked", cycleID, status)
		}
	}
	before := fixture.readIngestEvent(t, "usage", eventID)

	for _, mode := range []bool{false, true} {
		result, err := fixture.store.RepairIngestRequeueDead(fixture.ctx,
			IngestRequeueDeadRepairInput{Apply: mode, OperatorID: deadIngestOperator, EventID: eventID},
			AuditActor{Type: "admin", ID: deadIngestOperator})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Events) != 1 {
			t.Fatalf("apply=%t: events=%+v", mode, result.Events)
		}
		found := result.Events[0]
		if found.Requeued || !found.ReplayBlocked {
			t.Fatalf("apply=%t: Requeued=%t ReplayBlocked=%t, want false/true", mode, found.Requeued, found.ReplayBlocked)
		}
		if !strings.Contains(found.ReplayBlockedReason, "verifyFactBatchContextTx") {
			t.Fatalf("apply=%t: reason=%q must name the check", mode, found.ReplayBlockedReason)
		}
		if result.TotalRequeued != 0 || result.TotalBlockedSkipped != 1 {
			t.Fatalf("apply=%t: totals requeued=%d blocked=%d, want 0/1",
				mode, result.TotalRequeued, result.TotalBlockedSkipped)
		}
		if after := fixture.readIngestEvent(t, "usage", eventID); after != before {
			t.Fatalf("apply=%t: the skipped event was modified", mode)
		}
		if count := fixture.requeueAuditCount(t, eventID); count != 0 {
			t.Fatalf("apply=%t: requeue audit rows=%d, want 0", mode, count)
		}
	}

	// The override exists, is off by default, and does requeue when asked.
	forced, err := fixture.store.RepairIngestRequeueDead(fixture.ctx,
		IngestRequeueDeadRepairInput{Apply: true, OperatorID: deadIngestOperator,
			EventID: eventID, IncludeBlockedCycles: true},
		AuditActor{Type: "admin", ID: deadIngestOperator})
	if err != nil {
		t.Fatal(err)
	}
	if forced.TotalRequeued != 1 || forced.TotalBlockedSkipped != 0 ||
		!forced.Events[0].Requeued || !forced.Events[0].ReplayBlocked {
		t.Fatalf("forced result=%+v, want requeued but still flagged", forced.Events)
	}
	if after := fixture.readIngestEvent(t, "usage", eventID); after.status != "queued" || after.attemptCount != 0 {
		t.Fatalf("forced apply: status=%q attempt_count=%d, want queued/0", after.status, after.attemptCount)
	}
	if count := fixture.requeueAuditCount(t, eventID); count != 1 {
		t.Fatalf("forced apply: requeue audit rows=%d, want 1", count)
	}
}

// TestIngestRequeueDeadBlocksAnEventWithNoReplayBinding covers the other
// verdict verifyFactBatchContextTx can reach on the replay path: an economic
// v3 event whose first_batch_id carries no scan-cycle mapping for it at all
// gets ErrForbidden, not ErrConflict, and is equally unreplayable.
func TestIngestRequeueDeadBlocksAnEventWithNoReplayBinding(t *testing.T) {
	fixture := seedDeadIngestFixture(t, "usage")
	const eventID = "88000000-0000-4000-8000-0000000000a3"
	fixture.commitEvents(t, "usage", "89000000-0000-4000-8000-0000000000a3",
		[]SourceBatchEvent{newDeadIngestEvent(eventID, "usage_event")})
	fixture.killEvent(t, "usage", eventID, 20*time.Hour)
	// Drop only the cycle-event mapping, leaving the batch and the cycle
	// themselves intact -- the exact shape verifyFactBatchContextTx's
	// ErrNoRows/ErrForbidden branch describes.
	if _, err := fixture.store.pool.Exec(fixture.ctx, `DELETE FROM source_economic_scan_cycle_events
		WHERE source_instance_id=$1 AND stream_id='usage' AND event_id=$2`,
		fixture.sourceID, eventID); err != nil {
		t.Fatal(err)
	}

	result, err := fixture.store.RepairIngestRequeueDead(fixture.ctx,
		IngestRequeueDeadRepairInput{Apply: true, OperatorID: deadIngestOperator},
		AuditActor{Type: "admin", ID: deadIngestOperator})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || result.Events[0].Requeued || !result.Events[0].ReplayBlocked ||
		result.TotalBlockedSkipped != 1 {
		t.Fatalf("result=%+v, want the unbound event reported and skipped", result.Events)
	}
	if !strings.Contains(result.Events[0].ReplayBlockedReason, "ErrForbidden") {
		t.Fatalf("reason=%q, want it to name the verifier's own outcome", result.Events[0].ReplayBlockedReason)
	}
	if after := fixture.readIngestEvent(t, "usage", eventID); after.status != "dead" {
		t.Fatalf("skipped event status=%q, want dead", after.status)
	}

}

// TestIngestRequeueDeadBlocksAnEventWhoseBindingPayloadHashDiverges covers
// the verifier's third condition. It is defensive rather than reachable
// through the real ingest path -- CommitSourceBatch refuses a batch whose
// event payload hash differs from the stored row's, and writes the mapping
// from that same value -- so the divergence is written directly. Covered so
// the branch is not dead-untested and its reason stays distinct from the
// other two.
//
// It gets its own fixture on purpose: the sibling test's dead event has no
// freeze, so its scan cycle never becomes complete and never publishes, and a
// second cycle on the same stream is rejected by
// source_economic_one_active_scan_cycle. (Found by the full suite, not by
// reasoning -- the first draft appended this case to that test and failed
// with "stream already has an active scan cycle".)
func TestIngestRequeueDeadBlocksAnEventWhoseBindingPayloadHashDiverges(t *testing.T) {
	fixture := seedDeadIngestFixture(t, "usage")
	const eventID = "88000000-0000-4000-8000-0000000000a4"
	fixture.commitEvents(t, "usage", "89000000-0000-4000-8000-0000000000a4",
		[]SourceBatchEvent{newDeadIngestEvent(eventID, "usage_event")})
	fixture.killEvent(t, "usage", eventID, 20*time.Hour)
	if _, err := fixture.store.pool.Exec(fixture.ctx, `UPDATE source_economic_scan_cycle_events
		SET payload_hash=$3 WHERE source_instance_id=$1 AND stream_id='usage' AND event_id=$2`,
		fixture.sourceID, eventID, testHash("a-different-payload")); err != nil {
		t.Fatal(err)
	}

	drifted, err := fixture.store.RepairIngestRequeueDead(fixture.ctx,
		IngestRequeueDeadRepairInput{Apply: true, OperatorID: deadIngestOperator, EventID: eventID},
		AuditActor{Type: "admin", ID: deadIngestOperator})
	if err != nil {
		t.Fatal(err)
	}
	if len(drifted.Events) != 1 || drifted.Events[0].Requeued || !drifted.Events[0].ReplayBlocked ||
		!strings.Contains(drifted.Events[0].ReplayBlockedReason, "payload_hash") {
		t.Fatalf("payload-hash divergence result=%+v", drifted.Events)
	}
	if after := fixture.readIngestEvent(t, "usage", eventID); after.status != "dead" {
		t.Fatalf("skipped event status=%q, want dead", after.status)
	}
}

// TestIngestRequeueDeadApplyRequiresOperatorAndValidFilters mirrors every
// sibling repair's own operator-id gate, and pins that a malformed narrowing
// id is rejected instead of silently matching nothing (which would look like
// "no work to do" to an operator who mistyped an id).
func TestIngestRequeueDeadApplyRequiresOperatorAndValidFilters(t *testing.T) {
	fixture := seedDeadIngestFixture(t, "usage")
	const eventID = "88000000-0000-4000-8000-0000000000f8"
	fixture.commitEvents(t, "usage", "89000000-0000-4000-8000-0000000000f8",
		[]SourceBatchEvent{newDeadIngestEvent(eventID, "usage_event")})
	fixture.killEvent(t, "usage", eventID, 20*time.Hour)

	if _, err := fixture.store.RepairIngestRequeueDead(fixture.ctx,
		IngestRequeueDeadRepairInput{Apply: true}, AuditActor{Type: "admin"}); err == nil {
		t.Fatal("apply without an operator id was accepted")
	}
	if _, err := fixture.store.RepairIngestRequeueDead(fixture.ctx,
		IngestRequeueDeadRepairInput{EventID: "not-a-uuid"}, AuditActor{Type: "admin", ID: deadIngestOperator}); err == nil {
		t.Fatal("a malformed --event id was accepted")
	}
	if _, err := fixture.store.RepairIngestRequeueDead(fixture.ctx,
		IngestRequeueDeadRepairInput{AccountID: "not-a-uuid"}, AuditActor{Type: "admin", ID: deadIngestOperator}); err == nil {
		t.Fatal("a malformed --account id was accepted")
	}
	// None of the rejections may have written anything.
	if row := fixture.readIngestEvent(t, "usage", eventID); row.status != "dead" || row.attemptCount != 8 {
		t.Fatalf("a rejected invocation mutated the row: %+v", row)
	}
}
