package postgresstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"invoice-system/backend/internal/domain"
)

// TestCommitSourceBatchRejectsSecondActiveScanCycleUntilFirstPublishes exercises
// the one-active-cycle constraint (source_economic_one_active_scan_cycle,
// backend/migrations/0009_consumption_eligibility_ledger.sql) that CommitSourceBatch
// enforces via source_economic_scan_cycles. A second, different scan_cycle_id for
// the same (source_instance_id,stream_id) must be rejected with the distinct
// domain.ErrScanCycleBusy sentinel -- not a generic wrapped error and not the
// domain.ErrConflict used by the *other* conflict paths in this file (stale
// batch_id replay, finalized-cycle append, duplicate event with a different
// hash) -- while the first cycle is still receiving/processing, and must
// succeed once that first cycle publishes.
func TestCommitSourceBatchRejectsSecondActiveScanCycleUntilFirstPublishes(t *testing.T) {
	store, ctx := integrationStore(t)
	const sourceID = "10000000-0000-4000-8000-000000000088"
	const firstCycleID = "84000000-0000-4000-8000-000000000001"
	const secondCycleID = "84000000-0000-4000-8000-000000000002"
	ceiling := time.Now().UTC().Truncate(time.Microsecond)

	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','v3-cycle-busy-test','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "payments", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	// Publishing (below) needs a cutover manifest on file for the source, or
	// tryPublishEconomicScanCyclesTx silently skips the cycle forever.
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'v3-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
			'payment_orders:10','usage_logs:0','credits:0','balance_snapshot:0',$2,$2,0,'test-key')`,
		sourceID, testHash("cycle-busy-manifest"), ceiling.Add(-48*time.Hour), testHash("cycle-busy-config")); err != nil {
		t.Fatal(err)
	}

	chain := newV3TestChain()
	firstEvent := SourceBatchEvent{
		EventID: "85000000-0000-4000-8000-000000000001", EntityType: "payment_order",
		Operation: "upsert", PayloadHash: strings.Repeat("1", 64),
		PayloadCiphertext: bytes.Repeat([]byte{1}, 32), ObservedAt: ceiling,
	}
	firstCycle := chain.commit(t, store, ctx, sourceID, "payments", firstCycleID, ceiling, []SourceBatchEvent{firstEvent})

	var firstStatus string
	if err := store.pool.QueryRow(ctx, `SELECT cycle_status FROM source_economic_scan_cycles
		WHERE source_instance_id=$1 AND stream_id='payments' AND scan_cycle_id=$2::uuid`,
		sourceID, firstCycleID).Scan(&firstStatus); err != nil || firstStatus != "processing" {
		t.Fatalf("first cycle status=%q err=%v (want processing: its event is not yet processed)", firstStatus, err)
	}

	secondEvent := SourceBatchEvent{
		EventID: "85000000-0000-4000-8000-000000000002", EntityType: "payment_order",
		Operation: "upsert", PayloadHash: strings.Repeat("2", 64),
		PayloadCiphertext: bytes.Repeat([]byte{2}, 32), ObservedAt: ceiling,
	}
	busyInput := SourceBatchInput{
		SchemaVersion: "3.0", SourceInstanceID: sourceID, StreamID: "payments",
		BatchID: "86000000-0000-4000-8000-000000000001", Sequence: 2,
		BodyHash: testHash("second-cycle-while-busy"), PreviousBatchHash: chain.hash["payments"],
		SigningKeyID: "test-key", SourceRuntimeVersion: "v3-test", SourceAgentVersion: "v3-agent-test",
		SourceCapturedAt: ceiling, ProjectionStatus: "healthy", StreamWatermarkAt: ceiling,
		SourceCursor: "payments:2", ScanCeilingAt: ceiling, ScanCeilingCursor: "ceiling:" + secondCycleID,
		ScanCycleID: secondCycleID, ScanComplete: true, Events: []SourceBatchEvent{secondEvent},
		Actor: AuditActor{Type: "source_connector", ID: sourceID, Reason: "v3 busy-cycle regression"},
	}

	// While the first cycle is still active, a second, different cycle for the
	// same stream must be rejected with the distinct busy sentinel -- not the
	// generic wrapped error this used to be, and not domain.ErrConflict.
	if _, err := store.CommitSourceBatch(ctx, busyInput); !errors.Is(err, domain.ErrScanCycleBusy) {
		t.Fatalf("second cycle while first is processing: got err=%v, want domain.ErrScanCycleBusy", err)
	}
	var sequenceAfterRejection int64
	if err := store.pool.QueryRow(ctx, `SELECT sequence FROM source_ingest_state
		WHERE source_instance_id=$1 AND stream_id='payments'`, sourceID).Scan(&sequenceAfterRejection); err != nil || sequenceAfterRejection != 1 {
		t.Fatalf("rejected busy attempt advanced sequence=%d err=%v", sequenceAfterRejection, err)
	}

	// Publish the first cycle (mirrors what the eligibility worker does once
	// every event in the cycle reaches processing_status='processed').
	markV3CycleProcessed(t, store, ctx, sourceID, "payments", firstCycle)
	var publishedStatus string
	if err := store.pool.QueryRow(ctx, `SELECT cycle_status FROM source_economic_scan_cycles
		WHERE source_instance_id=$1 AND stream_id='payments' AND scan_cycle_id=$2::uuid`,
		sourceID, firstCycleID).Scan(&publishedStatus); err != nil || publishedStatus != "published" {
		t.Fatalf("first cycle did not publish: status=%q err=%v", publishedStatus, err)
	}

	// The exact same batch (same batch_id/sequence/hashes) the agent would
	// retry after honoring Retry-After must now succeed.
	if _, err := store.CommitSourceBatch(ctx, busyInput); err != nil {
		t.Fatalf("second cycle should succeed once the first cycle is published: %v", err)
	}
	var secondStatus string
	if err := store.pool.QueryRow(ctx, `SELECT s.sequence,c.cycle_status
		FROM source_ingest_state s JOIN source_economic_scan_cycles c
		  ON c.source_instance_id=s.source_instance_id AND c.stream_id=s.stream_id
		WHERE s.source_instance_id=$1 AND s.stream_id='payments' AND c.scan_cycle_id=$2::uuid`,
		sourceID, secondCycleID).Scan(&sequenceAfterRejection, &secondStatus); err != nil || sequenceAfterRejection != 2 || secondStatus != "processing" {
		t.Fatalf("second cycle sequence=%d status=%q err=%v", sequenceAfterRejection, secondStatus, err)
	}
}

// TestSourceHealthDowngradesStaleWatermarkOnlyWhileAScanCycleIsActivelyUpdating
// is the receiver-side proof for XM-INV-AGENT-RESTART-GRACE part A, using the
// real write path (CommitSourceBatch, the same INSERT into
// source_economic_scan_cycles production traffic goes through) rather than a
// seeded fixture: SourceHealth's new join must see a row CommitSourceBatch
// just wrote and grade it correctly, both while it is fresh (grace applies)
// and once it stops being fresh (grace does not).
func TestSourceHealthDowngradesStaleWatermarkOnlyWhileAScanCycleIsActivelyUpdating(t *testing.T) {
	store, ctx := integrationStore(t)
	const sourceID = "10000000-0000-4000-8000-000000000089"
	const oldCycleID = "84000000-0000-4000-8000-000000000011"
	const activeCycleID = "84000000-0000-4000-8000-000000000012"
	oldCeiling := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)

	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version,enabled)
		VALUES($1,'sub2api','v3-rescan-grace-test','v3-test',true)`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "usage", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'v3-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
			'payment_orders:0','usage_logs:0','credits:0','balance_snapshot:0',$2,$2,0,'test-key')`,
		sourceID, testHash("rescan-grace-manifest"), oldCeiling.Add(-48*time.Hour), testHash("rescan-grace-config")); err != nil {
		t.Fatal(err)
	}

	chain := newV3TestChain()
	oldEvent := SourceBatchEvent{
		EventID: "85000000-0000-4000-8000-000000000011", EntityType: "usage_event",
		Operation: "upsert", PayloadHash: strings.Repeat("3", 64),
		PayloadCiphertext: bytes.Repeat([]byte{3}, 32), ObservedAt: oldCeiling,
	}
	oldCycle := chain.commit(t, store, ctx, sourceID, "usage", oldCycleID, oldCeiling, []SourceBatchEvent{oldEvent})
	markV3CycleProcessed(t, store, ctx, sourceID, "usage", oldCycle)
	var publishedWatermark time.Time
	if err := store.pool.QueryRow(ctx, `SELECT watermark_at FROM source_economic_stream_watermarks
		WHERE source_instance_id=$1 AND stream_kind='usage'`, sourceID).Scan(&publishedWatermark); err != nil || !publishedWatermark.Equal(oldCeiling) {
		t.Fatalf("first cycle did not publish the expected watermark got=%s want=%s err=%v", publishedWatermark, oldCeiling, err)
	}
	// Runtime version/heartbeat must also look healthy, or ECONOMIC_WATERMARK_STALE
	// would be moot: another reason would already make the stream not-ready.
	if _, err := store.pool.Exec(ctx, `UPDATE source_ingest_state SET last_accepted_at=now() WHERE source_instance_id=$1 AND stream_id='usage'`, sourceID); err != nil {
		t.Fatal(err)
	}

	// A second, still-open cycle is exactly what a restart-forced (or
	// periodic) ScanReconcile in progress looks like on the receiver side:
	// CommitSourceBatch's own INSERT sets its updated_at to the real,
	// current transaction time.
	activeEvent := SourceBatchEvent{
		EventID: "85000000-0000-4000-8000-000000000012", EntityType: "usage_event",
		Operation: "upsert", PayloadHash: strings.Repeat("4", 64),
		PayloadCiphertext: bytes.Repeat([]byte{4}, 32), ObservedAt: time.Now().UTC(),
	}
	chain.commit(t, store, ctx, sourceID, "usage", activeCycleID, time.Now().UTC().Truncate(time.Microsecond), []SourceBatchEvent{activeEvent})
	var activeStatus string
	var activeUpdatedAt time.Time
	if err := store.pool.QueryRow(ctx, `SELECT cycle_status,updated_at FROM source_economic_scan_cycles
		WHERE source_instance_id=$1 AND stream_id='usage' AND scan_cycle_id=$2::uuid`,
		sourceID, activeCycleID).Scan(&activeStatus, &activeUpdatedAt); err != nil || activeStatus != "processing" {
		t.Fatalf("active cycle status=%q err=%v (want processing: its event is not yet processed)", activeStatus, err)
	}
	// Mark the event processed without publishing the cycle (unlike
	// markV3CycleProcessed, this deliberately does not call
	// tryPublishEconomicScanCyclesTx): cycle_status stays 'processing' and
	// updated_at stays as CommitSourceBatch set it, but PendingEvents drops
	// to zero so this test isolates the ECONOMIC_RESCAN_ACTIVE grade from the
	// separate, already-covered EVENTS_PENDING reason.
	if _, err := store.pool.Exec(ctx, `UPDATE source_ingest_events SET processing_status='processed',processed_at=now()
		WHERE source_instance_id=$1 AND stream_id='usage' AND event_id=$2`, sourceID, activeEvent.EventID); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	gracePolicy := SourceFreshnessPolicy{
		EconomicHeartbeatMaxAge: 5 * time.Minute, EconomicWatermarkMaxAge: 15 * time.Minute,
		IdentitiesMaxAge: 15 * time.Minute, EconomicRescanActivityMaxAge: 10 * time.Minute, Now: now,
	}
	report, err := store.SourceHealth(ctx, gracePolicy)
	if err != nil {
		t.Fatal(err)
	}
	item := findStreamHealthForTest(t, report, sourceID, "usage")
	if !item.ActiveRescanUpdatedAt.Equal(activeUpdatedAt) {
		t.Fatalf("SourceHealth did not surface CommitSourceBatch's own updated_at: got=%s want=%s", item.ActiveRescanUpdatedAt, activeUpdatedAt)
	}
	if !item.Ready || len(item.Reasons) != 1 || item.Reasons[0] != "ECONOMIC_RESCAN_ACTIVE" {
		t.Fatalf("a freshly written active scan cycle was not graded as an active rescan: %#v", item)
	}

	stalledPolicy := gracePolicy
	stalledPolicy.EconomicRescanActivityMaxAge = time.Nanosecond
	stalledReport, err := store.SourceHealth(ctx, stalledPolicy)
	if err != nil {
		t.Fatal(err)
	}
	stalledItem := findStreamHealthForTest(t, stalledReport, sourceID, "usage")
	if stalledItem.Ready || len(stalledItem.Reasons) != 1 || stalledItem.Reasons[0] != "ECONOMIC_WATERMARK_STALE" {
		t.Fatalf("an activity window narrower than any real gap did not fail closed to the plain reason: %#v", stalledItem)
	}
}

func findStreamHealthForTest(t *testing.T, report SourceHealthReport, sourceID, streamID string) SourceStreamHealth {
	t.Helper()
	for _, item := range report.Items {
		if item.SourceInstanceID == sourceID && item.StreamID == streamID {
			return item
		}
	}
	t.Fatalf("stream %s/%s not found in source health report", sourceID, streamID)
	return SourceStreamHealth{}
}

// scanCycleRow is the subset of source_economic_scan_cycles the
// XM-INV-SCAN-CYCLE-SUPERSEDE tests below assert on.
type scanCycleRow struct {
	cycleStatus                                    string
	updatedAt                                      time.Time
	supersededByScanCycleID, supersedeReason       string
	supersededByScanCycleIDSet, supersedeReasonSet bool
}

func readScanCycleRowForTest(t *testing.T, store *Store, ctx context.Context, sourceID, streamID, cycleID string) scanCycleRow {
	t.Helper()
	var row scanCycleRow
	var supersededBy, reason *string
	if err := store.pool.QueryRow(ctx, `
		SELECT cycle_status,updated_at,superseded_by_scan_cycle_id::text,supersede_reason
		FROM source_economic_scan_cycles
		WHERE source_instance_id=$1 AND stream_id=$2 AND scan_cycle_id=$3::uuid`,
		sourceID, streamID, cycleID).Scan(&row.cycleStatus, &row.updatedAt, &supersededBy, &reason); err != nil {
		t.Fatalf("read scan cycle row %s/%s/%s: %v", sourceID, streamID, cycleID, err)
	}
	if supersededBy != nil {
		row.supersededByScanCycleID, row.supersededByScanCycleIDSet = *supersededBy, true
	}
	if reason != nil {
		row.supersedeReason, row.supersedeReasonSet = *reason, true
	}
	return row
}

// newV3SupersedeBatchInput builds a schema-v3 SourceBatchInput for a brand
// new scan cycle id on the given stream, wired with the Now/
// StaleActiveScanCycleMaxAge fields the shared v3TestChain helper (designed
// before XM-INV-SCAN-CYCLE-SUPERSEDE existed) does not expose.
func newV3SupersedeBatchInput(chain *v3TestChain, sourceID, stream, cycleID string, sequence int64,
	ceiling, now time.Time, staleMaxAge time.Duration, event SourceBatchEvent) SourceBatchInput {
	batchID := fmt.Sprintf("87%06d-0000-4000-8000-%012d", sequence, sequence)
	bodyHash := testHash(stream + cycleID + fmt.Sprint(sequence))
	return SourceBatchInput{
		SchemaVersion: "3.0", SourceInstanceID: sourceID, StreamID: stream,
		BatchID: batchID, Sequence: sequence, BodyHash: bodyHash,
		PreviousBatchHash: chain.hash[stream], SigningKeyID: "test-key",
		SourceRuntimeVersion: "v3-test", SourceAgentVersion: "v3-agent-test",
		SourceCapturedAt: ceiling, ProjectionStatus: "healthy",
		StreamWatermarkAt: ceiling, SourceCursor: fmt.Sprintf("%s:%d", stream, sequence),
		ScanCeilingAt: ceiling, ScanCeilingCursor: "ceiling:" + cycleID,
		ScanCycleID: cycleID, ScanComplete: false, Events: []SourceBatchEvent{event},
		Actor: AuditActor{Type: "source_connector", ID: sourceID, Reason: "v3 supersede fixture"},
		Now:   now, StaleActiveScanCycleMaxAge: staleMaxAge,
	}
}

func provisionV3SupersedeSource(t *testing.T, store *Store, ctx context.Context, sourceID string, streams ...string) {
	t.Helper()
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_instances(id,source_type,name,runtime_version,enabled)
		VALUES($1,'sub2api','v3-supersede-test','v3-test',true)`, sourceID); err != nil {
		t.Fatal(err)
	}
	for _, stream := range streams {
		if err := store.ProvisionSourceStream(ctx, sourceID, stream, AuditActor{Type: "system", ID: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	ceiling := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)
	if _, err := store.pool.Exec(ctx, `INSERT INTO source_cutover_manifests(
		source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
		projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
		credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
		baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'v3-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
			'payment_orders:0','usage_logs:0','credits:0','balance_snapshot:0',$2,$2,0,'test-key')`,
		sourceID, testHash(sourceID+"-supersede-manifest"), ceiling, testHash(sourceID+"-supersede-config")); err != nil {
		t.Fatal(err)
	}
}

// TestCommitSourceBatchSupersedesStaleActiveScanCycle is the direct
// regression test for the production incident this slice fixes: an agent
// restart abandons a legacy in-flight scan cycle and starts a new
// scan_cycle_id, and CommitSourceBatch must self-heal instead of returning
// domain.ErrScanCycleBusy forever once the old cycle has gone stale (no
// updated_at movement within StaleActiveScanCycleMaxAge of Now). It also
// proves two-stream isolation: superseding stream A's cycle must not touch
// stream B's independent active cycle.
func TestCommitSourceBatchSupersedesStaleActiveScanCycle(t *testing.T) {
	store, ctx := integrationStore(t)
	const sourceID = "10000000-0000-4000-8000-000000000090"
	const oldCycleID = "84000000-0000-4000-8000-000000000021"
	const newCycleID = "84000000-0000-4000-8000-000000000022"
	const otherStreamCycleID = "84000000-0000-4000-8000-000000000023"
	provisionV3SupersedeSource(t, store, ctx, sourceID, "usage", "payments")

	chain := newV3TestChain()
	ceiling := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	oldEvent := SourceBatchEvent{
		EventID: "85000000-0000-4000-8000-000000000021", EntityType: "usage_event",
		Operation: "upsert", PayloadHash: strings.Repeat("5", 64),
		PayloadCiphertext: bytes.Repeat([]byte{5}, 32), ObservedAt: ceiling,
	}
	oldCycle := chain.commit(t, store, ctx, sourceID, "usage", oldCycleID, ceiling, []SourceBatchEvent{oldEvent})
	before := readScanCycleRowForTest(t, store, ctx, sourceID, "usage", oldCycleID)
	if before.cycleStatus != "processing" {
		t.Fatalf("old cycle status=%q, want processing before supersede", before.cycleStatus)
	}

	// An independent active cycle on a different stream of the same source:
	// superseding "usage" below must never touch this one.
	otherEvent := SourceBatchEvent{
		EventID: "85000000-0000-4000-8000-000000000022", EntityType: "payment_order",
		Operation: "upsert", PayloadHash: strings.Repeat("6", 64),
		PayloadCiphertext: bytes.Repeat([]byte{6}, 32), ObservedAt: ceiling,
	}
	chain.commit(t, store, ctx, sourceID, "payments", otherStreamCycleID, ceiling, []SourceBatchEvent{otherEvent})
	otherBefore := readScanCycleRowForTest(t, store, ctx, sourceID, "payments", otherStreamCycleID)

	// The new cycle's Now is far enough past the old cycle's real updated_at
	// (set by CommitSourceBatch's own INSERT, real wall-clock time) that a
	// one-minute grace window has long since elapsed -- stale.
	newEvent := SourceBatchEvent{
		EventID: "85000000-0000-4000-8000-000000000023", EntityType: "usage_event",
		Operation: "upsert", PayloadHash: strings.Repeat("7", 64),
		PayloadCiphertext: bytes.Repeat([]byte{7}, 32), ObservedAt: ceiling,
	}
	now := time.Now().UTC()
	input := newV3SupersedeBatchInput(chain, sourceID, "usage", newCycleID, chain.sequence["usage"]+1,
		ceiling, now.Add(time.Hour), time.Minute, newEvent)
	if _, err := store.CommitSourceBatch(ctx, input); err != nil {
		t.Fatalf("new cycle over a stale active cycle should be accepted: %v", err)
	}
	chain.sequence["usage"]++
	chain.hash["usage"] = input.BodyHash

	after := readScanCycleRowForTest(t, store, ctx, sourceID, "usage", oldCycleID)
	if after.cycleStatus != "blocked" {
		t.Fatalf("superseded cycle status=%q, want blocked", after.cycleStatus)
	}
	if !after.supersededByScanCycleIDSet || after.supersededByScanCycleID != newCycleID {
		t.Fatalf("superseded_by_scan_cycle_id=%q set=%t, want %q", after.supersededByScanCycleID, after.supersededByScanCycleIDSet, newCycleID)
	}
	if !after.supersedeReasonSet || strings.TrimSpace(after.supersedeReason) == "" {
		t.Fatalf("supersede_reason not recorded: %#v", after)
	}
	if !after.updatedAt.After(before.updatedAt) {
		t.Fatalf("superseded row's updated_at did not advance: before=%s after=%s", before.updatedAt, after.updatedAt)
	}

	newRow := readScanCycleRowForTest(t, store, ctx, sourceID, "usage", newCycleID)
	if newRow.cycleStatus != "receiving" {
		t.Fatalf("new cycle status=%q, want receiving", newRow.cycleStatus)
	}
	if newRow.supersededByScanCycleIDSet {
		t.Fatalf("new cycle must not itself carry a superseded_by_scan_cycle_id: %#v", newRow)
	}

	// The superseded cycle's already-committed events remain intact: neither
	// the mapping table nor the underlying ingest event was touched.
	var mappedEvents int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM source_economic_scan_cycle_events
		WHERE source_instance_id=$1 AND stream_id='usage' AND scan_cycle_id=$2::uuid`,
		sourceID, oldCycleID).Scan(&mappedEvents); err != nil || mappedEvents != 1 {
		t.Fatalf("superseded cycle lost its event mapping: count=%d err=%v", mappedEvents, err)
	}
	var eventStatus string
	if err := store.pool.QueryRow(ctx, `SELECT processing_status FROM source_ingest_events
		WHERE source_instance_id=$1 AND stream_id='usage' AND event_id=$2`,
		sourceID, oldEvent.EventID).Scan(&eventStatus); err != nil || eventStatus != "queued" {
		t.Fatalf("superseded cycle's event was mutated: status=%q err=%v", eventStatus, err)
	}

	// An audit event records the supersede with both cycle ids and sequences.
	var auditCount int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events
		WHERE action=$1 AND object_id=$2`, scanCycleSupersededAction,
		sourceID+"/usage/"+oldCycleID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("supersede audit event missing: count=%d err=%v", auditCount, err)
	}

	// Untouched: the other stream's independent active cycle.
	otherAfter := readScanCycleRowForTest(t, store, ctx, sourceID, "payments", otherStreamCycleID)
	if otherAfter.cycleStatus != otherBefore.cycleStatus || otherAfter.supersededByScanCycleIDSet {
		t.Fatalf("superseding the usage stream leaked into the isolated payments stream: before=%#v after=%#v", otherBefore, otherAfter)
	}

	// A blocked (superseded) cycle must never publish a watermark, even once
	// its event finishes processing -- tryPublishEconomicScanCyclesTx only
	// considers cycle_status='processing' rows.
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE source_ingest_events SET processing_status='processed',processed_at=now()
		WHERE source_instance_id=$1 AND stream_id='usage' AND event_id=$2`, sourceID, oldEvent.EventID); err != nil {
		t.Fatal(err)
	}
	if err = tryPublishEconomicScanCyclesTx(ctx, tx, sourceID, "usage",
		AuditActor{Type: "source_connector", ID: sourceID, Reason: "supersede test: attempt publish of blocked cycle"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var watermarkCount int
	if err = store.pool.QueryRow(ctx, `SELECT count(*) FROM source_economic_stream_watermarks
		WHERE source_instance_id=$1 AND stream_kind='usage'`, sourceID).Scan(&watermarkCount); err != nil || watermarkCount != 0 {
		t.Fatalf("a blocked (superseded) cycle published a watermark: count=%d err=%v", watermarkCount, err)
	}
	stillBlocked := readScanCycleRowForTest(t, store, ctx, sourceID, "usage", oldCycleID)
	if stillBlocked.cycleStatus != "blocked" {
		t.Fatalf("blocked cycle status changed after its event processed: %q", stillBlocked.cycleStatus)
	}
	_ = oldCycle
}

// TestCommitSourceBatchKeepsFreshActiveScanCycleBusy proves the other half of
// the same decision: a different scan_cycle_id racing an active cycle that
// is still fresh (its updated_at is within StaleActiveScanCycleMaxAge of Now)
// must keep returning domain.ErrScanCycleBusy, matching the pre-existing race
// comment on CommitSourceBatch's unique_violation mapping -- the grace only
// ever applies to a cycle that has actually gone stale.
func TestCommitSourceBatchKeepsFreshActiveScanCycleBusy(t *testing.T) {
	store, ctx := integrationStore(t)
	const sourceID = "10000000-0000-4000-8000-000000000091"
	const oldCycleID = "84000000-0000-4000-8000-000000000031"
	const newCycleID = "84000000-0000-4000-8000-000000000032"
	provisionV3SupersedeSource(t, store, ctx, sourceID, "usage")

	chain := newV3TestChain()
	ceiling := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	oldEvent := SourceBatchEvent{
		EventID: "85000000-0000-4000-8000-000000000031", EntityType: "usage_event",
		Operation: "upsert", PayloadHash: strings.Repeat("8", 64),
		PayloadCiphertext: bytes.Repeat([]byte{8}, 32), ObservedAt: ceiling,
	}
	chain.commit(t, store, ctx, sourceID, "usage", oldCycleID, ceiling, []SourceBatchEvent{oldEvent})
	before := readScanCycleRowForTest(t, store, ctx, sourceID, "usage", oldCycleID)

	newEvent := SourceBatchEvent{
		EventID: "85000000-0000-4000-8000-000000000032", EntityType: "usage_event",
		Operation: "upsert", PayloadHash: strings.Repeat("9", 64),
		PayloadCiphertext: bytes.Repeat([]byte{9}, 32), ObservedAt: ceiling,
	}
	now := time.Now().UTC()
	// Now is effectively "right now" relative to the old cycle's real
	// updated_at, well inside a ten-minute grace window: fresh, not stale.
	input := newV3SupersedeBatchInput(chain, sourceID, "usage", newCycleID, chain.sequence["usage"]+1,
		ceiling, now, 10*time.Minute, newEvent)
	if _, err := store.CommitSourceBatch(ctx, input); !errors.Is(err, domain.ErrScanCycleBusy) {
		t.Fatalf("fresh active cycle: got err=%v, want domain.ErrScanCycleBusy", err)
	}

	after := readScanCycleRowForTest(t, store, ctx, sourceID, "usage", oldCycleID)
	if after.cycleStatus != before.cycleStatus || after.supersededByScanCycleIDSet {
		t.Fatalf("a fresh active cycle was mutated by a rejected competing batch: before=%#v after=%#v", before, after)
	}
	var newRowExists bool
	if err := store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_economic_scan_cycles
		WHERE source_instance_id=$1 AND stream_id='usage' AND scan_cycle_id=$2::uuid)`,
		sourceID, newCycleID).Scan(&newRowExists); err != nil || newRowExists {
		t.Fatalf("rejected new cycle should never be inserted: exists=%t err=%v", newRowExists, err)
	}
	var sequenceAfterRejection int64
	if err := store.pool.QueryRow(ctx, `SELECT sequence FROM source_ingest_state
		WHERE source_instance_id=$1 AND stream_id='usage'`, sourceID).Scan(&sequenceAfterRejection); err != nil || sequenceAfterRejection != 1 {
		t.Fatalf("rejected busy attempt advanced sequence=%d err=%v", sequenceAfterRejection, err)
	}
}

// TestCommitSourceBatchContinuationOfActiveScanCycleIsUnaffectedBySupersedeGrace
// proves the supersede logic never activates for a continuation batch of the
// SAME active cycle, even when the grace is fully enabled (a non-zero Now and
// StaleActiveScanCycleMaxAge) and even after the cycle has gone well past
// that staleness window -- the id match short-circuits before any staleness
// check runs.
func TestCommitSourceBatchContinuationOfActiveScanCycleIsUnaffectedBySupersedeGrace(t *testing.T) {
	store, ctx := integrationStore(t)
	const sourceID = "10000000-0000-4000-8000-000000000092"
	const cycleID = "84000000-0000-4000-8000-000000000041"
	provisionV3SupersedeSource(t, store, ctx, sourceID, "usage")

	chain := newV3TestChain()
	ceiling := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	firstEvent := SourceBatchEvent{
		EventID: "85000000-0000-4000-8000-000000000041", EntityType: "usage_event",
		Operation: "upsert", PayloadHash: strings.Repeat("a", 64),
		PayloadCiphertext: bytes.Repeat([]byte{10}, 32), ObservedAt: ceiling,
	}
	// First page: not yet complete, still 'receiving'.
	chain.commitPage(t, store, ctx, sourceID, "usage", cycleID, ceiling, ceiling, false, []SourceBatchEvent{firstEvent})
	firstRow := readScanCycleRowForTest(t, store, ctx, sourceID, "usage", cycleID)
	if firstRow.cycleStatus != "receiving" {
		t.Fatalf("first page status=%q, want receiving", firstRow.cycleStatus)
	}

	secondEvent := SourceBatchEvent{
		EventID: "85000000-0000-4000-8000-000000000042", EntityType: "usage_event",
		Operation: "upsert", PayloadHash: strings.Repeat("b", 64),
		PayloadCiphertext: bytes.Repeat([]byte{11}, 32), ObservedAt: ceiling,
	}
	// Far beyond any plausible staleness window, and grace fully enabled --
	// still must be treated as a plain continuation, not a supersede target.
	input := newV3SupersedeBatchInput(chain, sourceID, "usage", cycleID, chain.sequence["usage"]+1,
		ceiling, time.Now().UTC().Add(24*time.Hour), time.Minute, secondEvent)
	input.ScanComplete = true
	if _, err := store.CommitSourceBatch(ctx, input); err != nil {
		t.Fatalf("continuation of the same active cycle should succeed: %v", err)
	}
	secondRow := readScanCycleRowForTest(t, store, ctx, sourceID, "usage", cycleID)
	if secondRow.cycleStatus != "processing" || secondRow.supersededByScanCycleIDSet {
		t.Fatalf("continuation batch was treated as a supersede: %#v", secondRow)
	}
}
