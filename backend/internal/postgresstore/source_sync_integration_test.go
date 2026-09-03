package postgresstore

import (
	"bytes"
	"errors"
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
