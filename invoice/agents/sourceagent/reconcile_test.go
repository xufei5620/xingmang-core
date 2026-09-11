package sourceagent

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

const reconcileTestSourceID = "10000000-0000-4000-8000-000000000001"

func newTestReconciler(t *testing.T, stream, sourceType string) *FileReconciler {
	t.Helper()
	r := &FileReconciler{
		Path: filepath.Join(t.TempDir(), "reconcile.json"), SourceID: reconcileTestSourceID,
		StreamID: stream, SourceType: sourceType, MissThreshold: 3,
		Now: func() time.Time { return time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC) },
	}
	if err := InitializeFileReconciler(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return r
}

func reconcilePaymentProjection(id string) Projection {
	return Projection{
		EntityType: EntityPaymentOrder, ExternalID: id, Operation: "upsert",
		ObservedAt: time.Date(2026, 8, 21, 11, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Payload:    PaymentOrderPayload{ExternalOrderID: id, ExternalUserID: "user-1"},
	}
}

func commitReconcilePage(t *testing.T, r *FileReconciler, cursors CursorStore, mode ScanMode, page ScanPage, synthetic bool) ScanCursor {
	t.Helper()
	ctx := context.Background()
	cursor, err := cursors.Load(ctx, reconcileTestSourceID)
	if err != nil {
		t.Fatal(err)
	}
	target := page.NextCursor
	if synthetic {
		target = cursor
	}
	_, err = r.CommitAcknowledgedPage(mode, cursor, target, page, synthetic)
	if err != nil {
		t.Fatal(err)
	}
	matched, err := cursors.CompareAndSwap(ctx, reconcileTestSourceID, cursor, target)
	if err != nil || !matched {
		t.Fatalf("cursor transition matched=%t err=%v", matched, err)
	}
	if err = r.CursorCommitted(); err != nil {
		t.Fatal(err)
	}
	result, err := cursors.Load(ctx, reconcileTestSourceID)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestReconcileRequiresRepeatedCompleteMissesAndResetsFalseNegative(t *testing.T) {
	r := newTestReconciler(t, "payments", SourceSub2API)
	if check, err := r.CheckReadOnly(); err != nil || check.Phase != "idle" || check.InventoryEntries != 0 {
		t.Fatalf("initial read-only reconcile check=%+v err=%v", check, err)
	}
	cursors := NewMemoryCursorStore()
	full := func(projections []Projection) {
		t.Helper()
		if err := r.StartCycle(ScanReconcile); err != nil {
			t.Fatal(err)
		}
		commitReconcilePage(t, r, cursors, ScanReconcile, ScanPage{
			Projections: projections, NextCursor: ScanCursor{Version: 1, Completed: true}, HasMore: false,
		}, false)
	}

	// Cold start learns the row but can never emit a deletion.
	full([]Projection{reconcilePaymentProjection("order-1")})
	if _, synthetic, err := r.SyntheticPage(100); err != nil || synthetic {
		t.Fatalf("cold start produced tombstones: synthetic=%t err=%v", synthetic, err)
	}

	// One false-negative complete scan increments a miss, then seeing the row
	// again resets it. It therefore still takes three new complete misses.
	full(nil)
	full([]Projection{reconcilePaymentProjection("order-1")})
	full(nil)
	full(nil)
	if _, synthetic, err := r.SyntheticPage(100); err != nil || synthetic {
		t.Fatalf("two consecutive misses produced a tombstone: synthetic=%t err=%v", synthetic, err)
	}

	if err := r.StartCycle(ScanReconcile); err != nil {
		t.Fatal(err)
	}
	page := ScanPage{NextCursor: ScanCursor{Version: 1, Completed: true}, HasMore: false}
	cursor := commitReconcilePage(t, r, cursors, ScanReconcile, page, false)
	tombstones, synthetic, err := r.SyntheticPage(100)
	if err != nil || !synthetic || len(tombstones.Projections) != 1 || tombstones.HasMore {
		t.Fatalf("third complete miss did not create one final tombstone: page=%+v synthetic=%t err=%v", tombstones, synthetic, err)
	}
	payload, ok := tombstones.Projections[0].Payload.(TombstonePayload)
	if !ok || payload.ExternalID != "order-1" || tombstones.Projections[0].EntityType != EntityPaymentOrder {
		t.Fatalf("unexpected tombstone: %+v", tombstones.Projections[0])
	}
	_ = cursor
	commitReconcilePage(t, r, cursors, ScanReconcile, tombstones, true)
	if _, synthetic, err = r.SyntheticPage(100); err != nil || synthetic {
		t.Fatalf("accepted tombstone remained queued: synthetic=%t err=%v", synthetic, err)
	}
}

func TestBlockedProjectionPublishesBlockedAndNeverCountsMissingRows(t *testing.T) {
	r := newTestReconciler(t, "payments", SourceSub2API)
	cursors := NewMemoryCursorStore()
	full := func(projections []Projection, blocked bool) {
		t.Helper()
		if err := r.StartCycle(ScanReconcile); err != nil {
			t.Fatal(err)
		}
		commitReconcilePage(t, r, cursors, ScanReconcile, ScanPage{
			Projections: projections, ReconcileBlocked: blocked,
			NextCursor: ScanCursor{Version: 1, Completed: true},
		}, false)
	}
	full([]Projection{reconcilePaymentProjection("order-block-safe")}, false)
	for range 5 {
		full(nil, true)
	}
	if page, synthetic, err := r.SyntheticPage(100); err != nil || synthetic || len(page.Projections) != 0 {
		t.Fatalf("blocked visibility produced tombstone: page=%+v synthetic=%t err=%v", page, synthetic, err)
	}
	inventory, err := r.loadInventory()
	var retained reconcileEntry
	for _, entry := range inventory {
		retained = entry
	}
	if err != nil || len(inventory) != 1 || retained.Misses != 0 {
		t.Fatalf("blocked visibility incremented missing count: inventory=%+v err=%v", inventory, err)
	}
	// Once evidence is healthy again, three complete real misses are still
	// required; blocked cycles did not advance the threshold.
	full(nil, false)
	full(nil, false)
	if _, synthetic, err := r.SyntheticPage(100); err != nil || synthetic {
		t.Fatalf("two post-block misses produced tombstone: synthetic=%t err=%v", synthetic, err)
	}
	full(nil, false)
	if page, synthetic, err := r.SyntheticPage(100); err != nil || !synthetic || len(page.Projections) != 1 {
		t.Fatalf("third healthy miss did not tombstone: page=%+v synthetic=%t err=%v", page, synthetic, err)
	}

	connector := &onePageConnector{page: ScanPage{
		ReconcileBlocked: true, NextCursor: ScanCursor{Version: 1, Completed: true},
	}}
	client := &captureAckClient{}
	publisher := &Publisher{
		Builder: BatchBuilder{SchemaVersion: SchemaVersionV2, SourceInstanceID: reconcileTestSourceID,
			StreamID: "payments", SourceType: SourceSub2API, SourceRuntimeVersion: "0.1.179",
			AgentVersion: "test", Mode: "db_projection"},
		Store: &MemorySequenceStore{}, Client: client, Pending: &MemoryPendingBatchStore{},
	}
	coordinator := &DurableSyncCoordinator{SourceID: reconcileTestSourceID, Connector: connector,
		Cursors: NewMemoryCursorStore(), Publisher: publisher,
		Reconcile: newTestReconciler(t, "payments", SourceSub2API), Limit: 100}
	if _, _, err := coordinator.SyncPage(context.Background(), ScanReconcile); err != nil {
		t.Fatal(err)
	}
	if len(client.batches) != 1 || client.batches[0].Batch.ProjectionStatus != "blocked" {
		t.Fatalf("blocked aggregate did not produce blocked signed batch: %+v", client.batches)
	}
}

func TestReconcilePaginationRestartAndPartialScanNeverCountAsMiss(t *testing.T) {
	r := newTestReconciler(t, "payments", SourceSub2API)
	cursors := NewMemoryCursorStore()
	if err := r.StartCycle(ScanReconcile); err != nil {
		t.Fatal(err)
	}
	commitReconcilePage(t, r, cursors, ScanReconcile, ScanPage{
		Projections: []Projection{reconcilePaymentProjection("order-1")},
		NextCursor:  ScanCursor{Version: 1, ID: 1}, HasMore: true,
	}, false)

	// A fresh process object resumes the same complete scan and retains page 1.
	restarted := &FileReconciler{Path: r.Path, SourceID: r.SourceID, StreamID: r.StreamID, SourceType: r.SourceType, MissThreshold: 3, Now: r.Now}
	if err := restarted.StartCycle(ScanReconcile); err != nil {
		t.Fatal(err)
	}
	commitReconcilePage(t, restarted, cursors, ScanReconcile, ScanPage{
		Projections: []Projection{reconcilePaymentProjection("order-2")},
		NextCursor:  ScanCursor{Version: 1, ID: 2, Completed: true}, HasMore: false,
	}, false)

	before, err := restarted.loadInventory()
	if err != nil {
		t.Fatal(err)
	}
	// Start a later scan, acknowledge only its first page, then fail. Since the
	// complete boundary was never reached, no miss count or tombstone is made.
	if err := restarted.StartCycle(ScanReconcile); err != nil {
		t.Fatal(err)
	}
	commitReconcilePage(t, restarted, cursors, ScanReconcile, ScanPage{
		Projections: []Projection{reconcilePaymentProjection("order-1")},
		NextCursor:  ScanCursor{Version: 1, ID: 1}, HasMore: true,
	}, false)
	if _, synthetic, err := restarted.SyntheticPage(100); err != nil || synthetic {
		t.Fatalf("partial scan created a deletion plan: synthetic=%t err=%v", synthetic, err)
	}
	// Reopen to check durable state rather than only the current object.
	reopened := &FileReconciler{Path: r.Path, SourceID: r.SourceID, StreamID: r.StreamID, SourceType: r.SourceType, MissThreshold: 3, Now: r.Now}
	after, err := reopened.loadInventory()
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("partial scan changed durable inventory or miss counters: before=%+v after=%+v err=%v", before, after, err)
	}
	control, err := reopened.loadControl()
	if err != nil || control.Phase != "scanning" {
		t.Fatalf("partial scan left the scanning phase: phase=%s err=%v", control.Phase, err)
	}
}

func TestReconcileRecoversAcknowledgedCursorAcrossRestart(t *testing.T) {
	r := newTestReconciler(t, "payments", SourceSub2API)
	cursors := NewMemoryCursorStore()
	if err := r.StartCycle(ScanReconcile); err != nil {
		t.Fatal(err)
	}
	old, _ := cursors.Load(context.Background(), reconcileTestSourceID)
	target := ScanCursor{Revision: old.Revision, Version: 1, ID: 7, Completed: true}
	if _, err := r.CommitAcknowledgedPage(ScanReconcile, old, target, ScanPage{
		Projections: []Projection{reconcilePaymentProjection("order-7")}, NextCursor: target,
	}, false); err != nil {
		t.Fatal(err)
	}

	// Crash before cursor CAS. Recovery must complete exactly that acknowledged
	// transition and finalize the inventory, not rescan or lose the page.
	restarted := &FileReconciler{Path: r.Path, SourceID: r.SourceID, StreamID: r.StreamID, SourceType: r.SourceType, MissThreshold: 3, Now: r.Now}
	loaded, _ := cursors.Load(context.Background(), reconcileTestSourceID)
	recovered, err := restarted.RecoverCursor(context.Background(), cursors, loaded)
	if err != nil || recovered.Revision != 1 || recovered.ID != 7 {
		t.Fatalf("recovered cursor=%+v err=%v", recovered, err)
	}
	inventory, err := restarted.loadInventory()
	if err != nil || len(inventory) != 1 {
		t.Fatalf("recovered inventory=%+v err=%v", inventory, err)
	}
}

type onePageConnector struct {
	page  ScanPage
	calls int
}

func (c *onePageConnector) SourceType() string { return SourceSub2API }
func (c *onePageConnector) Scan(context.Context, ScanRequest) (ScanPage, error) {
	c.calls++
	return c.page, nil
}

type ackLossClient struct {
	calls   int
	batches []ValidatedBatch
}

type captureAckClient struct{ batches []ValidatedBatch }

func (c *captureAckClient) Send(_ context.Context, batch ValidatedBatch) (IngestAck, error) {
	c.batches = append(c.batches, batch)
	return IngestAck{Accepted: true, SourceInstanceID: batch.Batch.SourceInstanceID,
		StreamID: batch.Batch.StreamID, BatchID: batch.Batch.BatchID,
		Sequence: batch.Batch.Sequence, AcceptedRecords: len(batch.Batch.Records)}, nil
}

func (c *ackLossClient) Send(_ context.Context, batch ValidatedBatch) (IngestAck, error) {
	c.calls++
	c.batches = append(c.batches, batch)
	if c.calls == 1 {
		return IngestAck{}, errors.New("simulated ACK loss")
	}
	return IngestAck{Accepted: true, SourceInstanceID: batch.Batch.SourceInstanceID,
		StreamID: batch.Batch.StreamID, BatchID: batch.Batch.BatchID,
		Sequence: batch.Batch.Sequence, AcceptedRecords: len(batch.Batch.Records)}, nil
}

func TestDurableCoordinatorEmptyHeartbeatAndACKLossReplay(t *testing.T) {
	r := newTestReconciler(t, "payments", SourceSub2API)
	cursors := NewMemoryCursorStore()
	connector := &onePageConnector{page: ScanPage{Projections: nil, NextCursor: ScanCursor{Version: 1, Completed: true}}}
	client := &ackLossClient{}
	publisher := &Publisher{
		Builder: BatchBuilder{SchemaVersion: SchemaVersionV2, SourceInstanceID: reconcileTestSourceID,
			StreamID: "payments", SourceType: SourceSub2API, SourceRuntimeVersion: "0.1.179",
			AgentVersion: "test", Mode: "db_projection"},
		Store: &MemorySequenceStore{}, Client: client, Pending: &MemoryPendingBatchStore{},
	}
	coordinator := &DurableSyncCoordinator{SourceID: reconcileTestSourceID, Connector: connector,
		Cursors: cursors, Publisher: publisher, Reconcile: r, Limit: 100}
	if _, _, err := coordinator.SyncPage(context.Background(), ScanReconcile); err == nil {
		t.Fatal("simulated ACK loss was accepted")
	}
	loaded, _ := cursors.Load(context.Background(), reconcileTestSourceID)
	if loaded.Revision != 0 {
		t.Fatalf("cursor advanced before ACK: %+v", loaded)
	}

	// A new coordinator instance reuses the durable pending batch. The empty
	// signed batch is a heartbeat, advances sequence exactly once, and proves a
	// static/empty source is alive without inventing a row or tombstone.
	restarted := &DurableSyncCoordinator{SourceID: reconcileTestSourceID, Connector: connector,
		Cursors: cursors, Publisher: publisher,
		Reconcile: &FileReconciler{Path: r.Path, SourceID: r.SourceID, StreamID: r.StreamID, SourceType: r.SourceType, MissThreshold: 3, Now: r.Now}, Limit: 100}
	page, ack, err := restarted.SyncPage(context.Background(), ScanReconcile)
	if err != nil || page.HasMore || !ack.Accepted || ack.AcceptedRecords != 0 || ack.Sequence != 1 {
		t.Fatalf("empty heartbeat replay page=%+v ack=%+v err=%v", page, ack, err)
	}
	if len(client.batches) != 2 || client.batches[0].Batch.BatchID != client.batches[1].Batch.BatchID || client.batches[0].BodyHash != client.batches[1].BodyHash {
		t.Fatalf("ACK-loss retry was not byte-exact: %#v", client.batches)
	}
	state, _ := publisher.Store.Load(context.Background())
	if state.Sequence != 1 {
		t.Fatalf("empty heartbeat advanced sequence %d times", state.Sequence)
	}
}
