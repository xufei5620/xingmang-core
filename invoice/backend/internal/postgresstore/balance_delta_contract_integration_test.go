package postgresstore

import (
	"context"
	"strings"
	"testing"
	"time"
)

// This is a receiver-contract test for the balances sender's zero-change
// heartbeat. It deliberately exercises the existing store without changing
// receiver implementation: a signed complete balances cycle whose snapshot
// row count is zero must publish and advance the source watermark.
func TestZeroRowBalanceSnapshotPublishesAndAdvancesWatermark(t *testing.T) {
	store, ctx := integrationStore(t)
	const sourceID = "10000000-0000-4000-8000-000000000098"
	const cycleID = "8a000000-0000-4000-8000-000000000001"
	const batchID = "8a000000-0000-4000-8000-000000000002"
	var policyStart time.Time
	if err := store.pool.QueryRow(ctx, `SELECT eligibility_start_at FROM invoice_eligibility_policy WHERE singleton_id=1`).Scan(&policyStart); err != nil {
		t.Fatal(err)
	}
	cutover := policyStart.UTC().Add(-time.Hour).Truncate(time.Microsecond)
	ceiling := policyStart.UTC().Add(time.Hour).Truncate(time.Microsecond)
	manifestHash := testHash("zero-balance-manifest")
	configHash := testHash("zero-balance-config")
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_instances(id,source_type,name,runtime_version)
		VALUES($1,'sub2api','zero-balance-contract','v3-test')`, sourceID); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionSourceStream(ctx, sourceID, "balances", AuditActor{Type: "system", ID: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO source_cutover_manifests(
			source_instance_id,manifest_hash,cutover_at,database_clock,source_runtime_version,
			projection_contract,configuration_hash,unit_code,payments_ceiling,usage_ceiling,
			credits_ceiling,balances_ceiling,baseline_snapshot_id,baseline_snapshot_hash,
			baseline_row_count,signing_key_id)
		VALUES($1,$2,$3,$3,'v3-test','sub2api-economic-v4',$4,'SUB2_BALANCE_1E8',
			'p0','u0','c0','b0',$5,$5,0,'test-key')`, sourceID, manifestHash, cutover, configHash,
		testHash("zero-balance-baseline")); err != nil {
		t.Fatal(err)
	}
	result, err := store.CommitSourceBatch(ctx, SourceBatchInput{
		SchemaVersion: "3.0", SourceInstanceID: sourceID, StreamID: "balances",
		BatchID: batchID, Sequence: 1, BodyHash: strings.Repeat("c", 64), SigningKeyID: "test-key",
		SourceRuntimeVersion: "v3-test", SourceAgentVersion: "balance-delta-test",
		SourceCapturedAt: ceiling, ProjectionStatus: "healthy", StreamWatermarkAt: ceiling,
		SourceCursor: "balance_snapshot:99", ScanCeilingAt: ceiling, ScanCeilingCursor: "balance_snapshot:99",
		ScanCycleID: cycleID, ScanComplete: true, ScanSnapshotID: testHash("zero-change-snapshot"),
		ScanSnapshotRowCount: 0, Events: nil,
		Actor: AuditActor{Type: "source_connector", ID: sourceID, Reason: "zero balance delta contract"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AcceptedRecords != 0 || result.Sequence != 1 {
		t.Fatalf("zero balance result=%+v", result)
	}
	var status string
	var watermark time.Time
	var sequence int64
	var cursor string
	if err = store.pool.QueryRow(ctx, `
		SELECT c.cycle_status,w.watermark_at,w.source_sequence,w.source_cursor
		FROM source_economic_scan_cycles c
		JOIN source_economic_stream_watermarks w
		  ON w.source_instance_id=c.source_instance_id AND w.stream_kind=c.stream_id
		WHERE c.source_instance_id=$1 AND c.stream_id='balances' AND c.scan_cycle_id=$2::uuid`,
		sourceID, cycleID).Scan(&status, &watermark, &sequence, &cursor); err != nil {
		t.Fatal(err)
	}
	if status != "published" || !watermark.Equal(ceiling) || sequence != 1 || cursor != "balance_snapshot:99" {
		t.Fatalf("zero balance cycle status=%q watermark=%s sequence=%d cursor=%q", status, watermark, sequence, cursor)
	}
	var mapped int
	if err = store.pool.QueryRow(context.Background(), `SELECT count(*) FROM source_economic_scan_cycle_events
		WHERE source_instance_id=$1 AND stream_id='balances' AND scan_cycle_id=$2::uuid`, sourceID, cycleID).Scan(&mapped); err != nil {
		t.Fatal(err)
	}
	if mapped != 0 {
		t.Fatalf("zero balance heartbeat mapped %d events", mapped)
	}
}
