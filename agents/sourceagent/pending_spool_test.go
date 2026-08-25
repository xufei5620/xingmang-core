package sourceagent

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixedConnector struct{ page ScanPage }

func (c fixedConnector) SourceType() string { return SourceNewAPI }
func (c fixedConnector) Scan(context.Context, ScanRequest) (ScanPage, error) {
	return c.page, nil
}

type failOnScanConnector struct{}

func (failOnScanConnector) SourceType() string { return SourceNewAPI }
func (failOnScanConnector) Scan(context.Context, ScanRequest) (ScanPage, error) {
	return ScanPage{}, errors.New("connector scan ran before pending replay")
}

func TestEncryptedPendingSpoolRoundTripAndTamperFailsClosed(t *testing.T) {
	directory := t.TempDir()
	keyPath := filepath.Join(directory, "spool.key")
	key := bytes.Repeat([]byte{0x42}, 32)
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	store := &EncryptedFilePendingStore{
		Path: filepath.Join(directory, "pending.enc"), SourceID: "10000000-0000-4000-8000-000000000002", StreamID: "payments",
		Keys: FileSpoolKeyProvider{Path: keyPath},
	}
	batch, err := (BatchBuilder{
		SourceInstanceID: "10000000-0000-4000-8000-000000000002", StreamID: "payments", SourceType: SourceNewAPI,
		SourceRuntimeVersion: "v1.0.0-rc.25", AgentVersion: "test", Mode: "db_projection",
	}).Build(1, "", []Projection{candidateProjectionForTest()})
	if err != nil {
		t.Fatal(err)
	}
	pending := PendingBatch{
		Batch: batch, CursorBefore: ScanCursor{},
		CursorAfter: ScanCursor{Version: 1, ID: 41, Completed: true},
	}
	stored, err := store.SaveIfAbsent(context.Background(), pending)
	if err != nil || !stored {
		t.Fatalf("store pending: stored=%t err=%v", stored, err)
	}
	onDisk, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(onDisk, []byte("10000000-0000-4000-8000-000000000002")) || bytes.Contains(onDisk, []byte("external_order_id")) || bytes.Contains(onDisk, batch.RawBody) {
		t.Fatal("encrypted spool exposed batch metadata or plaintext")
	}
	loaded, exists, err := store.Load(context.Background())
	if err != nil || !exists {
		t.Fatalf("load pending: exists=%t err=%v", exists, err)
	}
	if !bytes.Equal(loaded.Batch.RawBody, batch.RawBody) || loaded.Batch.Batch.BatchID != batch.Batch.BatchID || loaded.CursorAfter.ID != 41 {
		t.Fatal("pending spool round trip changed the exact batch or cursor")
	}
	beforeCheck, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	checked, exists, err := store.CheckReadOnly(context.Background())
	if err != nil || !exists || checked.Batch.BodyHash != batch.BodyHash {
		t.Fatalf("read-only pending check exists=%t err=%v", exists, err)
	}
	checked.Destroy()
	afterCheck, _ := os.Stat(store.Path)
	if !beforeCheck.ModTime().Equal(afterCheck.ModTime()) {
		t.Fatal("read-only pending check changed the spool")
	}
	if _, err = os.Lstat(store.Path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only pending check created a lock file")
	}

	// Flip one authenticated ciphertext byte without making the outer JSON
	// unreadable. Any corruption must fail closed before returning plaintext.
	for index := len(onDisk) / 2; index < len(onDisk); index++ {
		if onDisk[index] >= 'A' && onDisk[index] <= 'Y' {
			onDisk[index]++
			break
		}
	}
	if err := os.WriteFile(store.Path, onDisk, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Load(context.Background()); err == nil {
		t.Fatal("tampered encrypted spool was accepted")
	}
}

func TestAckLossRetriesExactPendingBatch(t *testing.T) {
	ctx := context.Background()
	sequence := &MemorySequenceStore{}
	cursors := NewMemoryCursorStore()
	pending := &MemoryPendingBatchStore{}
	page := ScanPage{
		Projections: []Projection{candidateProjectionForTest()},
		NextCursor:  ScanCursor{Version: 1, ID: 41, Completed: true},
	}
	connector := fixedConnector{page: page}
	builder := BatchBuilder{
		SourceInstanceID: "10000000-0000-4000-8000-000000000002", StreamID: "payments", SourceType: SourceNewAPI,
		SourceRuntimeVersion: "v1.0.0-rc.25", AgentVersion: "test", Mode: "db_projection",
	}
	var first ValidatedBatch
	firstPublisher := &Publisher{
		Builder: builder, Store: sequence, Pending: pending,
		Client: ingestFunc(func(_ context.Context, batch ValidatedBatch) (IngestAck, error) {
			first = cloneValidatedBatchForTest(batch)
			return IngestAck{}, errors.New("ack lost")
		}),
	}
	firstCoordinator := &SyncCoordinator{SourceID: "10000000-0000-4000-8000-000000000002", Connector: connector, Cursors: cursors, Publisher: firstPublisher, Limit: 100}
	if _, _, err := firstCoordinator.SyncPage(ctx, ScanFull); err == nil {
		t.Fatal("simulated ACK loss unexpectedly succeeded")
	}
	sequenceAfterLoss, _ := sequence.Load(ctx)
	cursorAfterLoss, _ := cursors.Load(ctx, "10000000-0000-4000-8000-000000000002")
	if sequenceAfterLoss.Sequence != 0 || cursorAfterLoss.ID != 0 {
		t.Fatal("ACK loss advanced sequence or cursor")
	}

	secondPublisher := &Publisher{
		Builder: builder, Store: sequence, Pending: pending,
		Client: ingestFunc(func(_ context.Context, retried ValidatedBatch) (IngestAck, error) {
			if retried.Batch.BatchID != first.Batch.BatchID || retried.BodyHash != first.BodyHash || !bytes.Equal(retried.RawBody, first.RawBody) {
				t.Fatal("ACK-loss retry rebuilt instead of replaying the exact pending batch")
			}
			return IngestAck{
				Accepted: true, SourceInstanceID: retried.Batch.SourceInstanceID,
				StreamID: retried.Batch.StreamID, BatchID: retried.Batch.BatchID,
				Sequence: retried.Batch.Sequence, AcceptedRecords: len(retried.Batch.Records),
				Duplicate: true,
			}, nil
		}),
	}
	secondCoordinator := &SyncCoordinator{SourceID: "10000000-0000-4000-8000-000000000002", Connector: failOnScanConnector{}, Cursors: cursors, Publisher: secondPublisher, Limit: 100}
	if _, _, err := secondCoordinator.SyncPage(ctx, ScanFull); err != nil {
		t.Fatal(err)
	}
	finalSequence, _ := sequence.Load(ctx)
	finalCursor, _ := cursors.Load(ctx, "10000000-0000-4000-8000-000000000002")
	if finalSequence.Sequence != 1 || finalCursor.ID != 41 {
		t.Fatalf("exact retry did not commit state: sequence=%#v cursor=%#v", finalSequence, finalCursor)
	}
	if _, exists, _ := pending.Load(ctx); exists {
		t.Fatal("pending spool was not cleared after ACK and cursor commit")
	}
}

func TestRevisionSaltDoesNotRewriteLegacyPendingBatch(t *testing.T) {
	ctx := context.Background()
	const sourceID = "10000000-0000-4000-8000-000000000001"
	const ceilingAt = "2026-08-25T00:00:00Z"
	const ceilingCursor = "payment_orders:10"
	legacyCycleID := deterministicUUID(strings.Join([]string{sourceID, StreamPayments, ceilingAt, ceilingCursor}, "\x00"))
	cursorAfter := ScanCursor{
		Version: 2, CutoverAt: "2026-08-24T00:00:00Z",
		WatermarkAt: ceilingAt, WatermarkCursor: ceilingCursor,
		CeilingAt: ceilingAt, CeilingCursor: ceilingCursor, PositionCursor: ceilingCursor,
		ScanCycleID: legacyCycleID, Completed: true,
	}
	sequence := &MemorySequenceStore{}
	cursors := NewMemoryCursorStore()
	pending := &MemoryPendingBatchStore{}
	builder := BatchBuilder{
		SchemaVersion: SchemaVersionV3, SourceInstanceID: sourceID, StreamID: StreamPayments,
		SourceType: SourceSub2API, SourceRuntimeVersion: "0.1.179", AgentVersion: "rc29", Mode: "db_projection",
	}
	var legacyPending ValidatedBatch
	first := &SyncCoordinator{
		SourceID: sourceID, Connector: fixedConnector{page: ScanPage{NextCursor: cursorAfter}}, Cursors: cursors,
		Publisher: &Publisher{Builder: builder, Store: sequence, Pending: pending,
			Client: ingestFunc(func(_ context.Context, batch ValidatedBatch) (IngestAck, error) {
				legacyPending = cloneValidatedBatchForTest(batch)
				return IngestAck{}, errors.New("receiver rejected finalized legacy cycle")
			})},
	}
	if _, _, err := first.SyncPage(ctx, ScanFull); err == nil {
		t.Fatal("legacy pending fixture unexpectedly succeeded")
	}
	if legacyPending.Batch.ScanCycleID != legacyCycleID {
		t.Fatal("legacy pending fixture did not carry the legacy cycle ID")
	}

	builder.AgentVersion = "rc30"
	second := &SyncCoordinator{
		SourceID: sourceID, Connector: failOnScanConnector{}, Cursors: cursors,
		Publisher: &Publisher{Builder: builder, Store: sequence, Pending: pending,
			Client: ingestFunc(func(_ context.Context, replayed ValidatedBatch) (IngestAck, error) {
				if replayed.Batch.ScanCycleID != legacyCycleID || replayed.BodyHash != legacyPending.BodyHash || !bytes.Equal(replayed.RawBody, legacyPending.RawBody) {
					t.Fatal("RC30 rebuilt or revision-salted an already durable legacy pending batch")
				}
				return IngestAck{Accepted: true, SourceInstanceID: sourceID, StreamID: StreamPayments,
					BatchID: replayed.Batch.BatchID, Sequence: replayed.Batch.Sequence,
					AcceptedRecords: len(replayed.Batch.Records), Duplicate: true}, nil
			})},
	}
	page, _, err := second.SyncPage(ctx, ScanFull)
	if err != nil {
		t.Fatal(err)
	}
	if page.ScanCycleID != legacyCycleID {
		t.Fatal("pending-first recovery did not commit the exact legacy cursor")
	}
}

func TestProcessRestartReplaysExactEncryptedFilePendingBatch(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	keyPath := filepath.Join(directory, "spool.key")
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x33}, 32))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	const sourceID = "10000000-0000-4000-8000-000000000002"
	statePath := filepath.Join(directory, "state.json")
	spoolPath := filepath.Join(directory, "pending.enc")
	firstState := &FileStateStore{Path: statePath, SourceID: sourceID, StreamID: "payments"}
	if err := InitializeFileState(ctx, firstState); err != nil {
		t.Fatal(err)
	}
	newSpool := func() *EncryptedFilePendingStore {
		return &EncryptedFilePendingStore{
			Path: spoolPath, SourceID: sourceID, StreamID: "payments",
			Keys: FileSpoolKeyProvider{Path: keyPath},
		}
	}
	builder := BatchBuilder{
		SourceInstanceID: sourceID, StreamID: "payments", SourceType: SourceNewAPI,
		SourceRuntimeVersion: "v1.0.0-rc.25", AgentVersion: "test", Mode: "db_projection",
	}
	page := ScanPage{
		Projections: []Projection{candidateProjectionForTest()},
		NextCursor:  ScanCursor{Version: 1, ID: 41, Completed: true},
	}
	var sentBeforeRestart ValidatedBatch
	first := &SyncCoordinator{
		SourceID: sourceID, Connector: fixedConnector{page: page},
		Cursors: FileCursorStore{State: firstState},
		Publisher: &Publisher{
			Builder: builder, Store: FileSequenceStore{State: firstState}, Pending: newSpool(),
			Client: ingestFunc(func(_ context.Context, batch ValidatedBatch) (IngestAck, error) {
				sentBeforeRestart = cloneValidatedBatchForTest(batch)
				return IngestAck{}, errors.New("connection reset after receiver commit")
			}),
		},
	}
	if _, _, err := first.SyncPage(ctx, ScanFull); err == nil {
		t.Fatal("first process did not observe simulated ACK loss")
	}
	if _, err := os.Stat(spoolPath); err != nil {
		t.Fatal("first process did not durably persist encrypted pending batch")
	}

	// New state/spool objects simulate a new process with no shared memory.
	secondState := &FileStateStore{Path: statePath, SourceID: sourceID, StreamID: "payments"}
	second := &SyncCoordinator{
		SourceID: sourceID, Connector: failOnScanConnector{},
		Cursors: FileCursorStore{State: secondState},
		Publisher: &Publisher{
			Builder: builder, Store: FileSequenceStore{State: secondState}, Pending: newSpool(),
			Client: ingestFunc(func(_ context.Context, batch ValidatedBatch) (IngestAck, error) {
				if batch.Batch.BatchID != sentBeforeRestart.Batch.BatchID || batch.BodyHash != sentBeforeRestart.BodyHash || !bytes.Equal(batch.RawBody, sentBeforeRestart.RawBody) {
					t.Fatal("new process did not recover the byte-exact encrypted pending batch")
				}
				return IngestAck{
					Accepted: true, SourceInstanceID: batch.Batch.SourceInstanceID,
					StreamID: batch.Batch.StreamID, BatchID: batch.Batch.BatchID,
					Sequence: batch.Batch.Sequence, AcceptedRecords: len(batch.Batch.Records), Duplicate: true,
				}, nil
			}),
		},
	}
	if _, _, err := second.SyncPage(ctx, ScanFull); err != nil {
		t.Fatal(err)
	}
	sequence, err := (FileSequenceStore{State: secondState}).Load(ctx)
	if err != nil || sequence.Sequence != 1 {
		t.Fatalf("restarted process did not commit sequence: %#v err=%v", sequence, err)
	}
	cursor, err := (FileCursorStore{State: secondState}).Load(ctx, sourceID)
	if err != nil || cursor.ID != 41 {
		t.Fatalf("restarted process did not commit stored cursor: %#v err=%v", cursor, err)
	}
	if _, err := os.Stat(spoolPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("finalized encrypted spool still exists: %v", err)
	}
}

func cloneValidatedBatchForTest(batch ValidatedBatch) ValidatedBatch {
	copy := batch
	copy.RawBody = append([]byte(nil), batch.RawBody...)
	copy.Batch.Records = append([]Record(nil), batch.Batch.Records...)
	for index := range copy.Batch.Records {
		copy.Batch.Records[index].Payload = append([]byte(nil), batch.Batch.Records[index].Payload...)
	}
	return copy
}

func TestCrashAfterAckRecoversStoredCursorWithoutResending(t *testing.T) {
	ctx := context.Background()
	sequence := &MemorySequenceStore{}
	cursors := NewMemoryCursorStore()
	pending := &MemoryPendingBatchStore{}
	builder := BatchBuilder{
		SourceInstanceID: "10000000-0000-4000-8000-000000000002", StreamID: "payments", SourceType: SourceNewAPI,
		SourceRuntimeVersion: "v1.0.0-rc.25", AgentVersion: "test", Mode: "db_projection",
	}
	publisher := &Publisher{
		Builder: builder, Store: sequence, Pending: pending,
		Client: ingestFunc(func(_ context.Context, batch ValidatedBatch) (IngestAck, error) {
			return IngestAck{
				Accepted: true, SourceInstanceID: batch.Batch.SourceInstanceID,
				StreamID: batch.Batch.StreamID, BatchID: batch.Batch.BatchID,
				Sequence: batch.Batch.Sequence, AcceptedRecords: len(batch.Batch.Records),
			}, nil
		}),
	}
	storedTarget := ScanCursor{Version: 1, ID: 41, Completed: true}
	if _, err := publisher.PublishPage(ctx, ScanCursor{}, storedTarget, []Projection{candidateProjectionForTest()}); err != nil {
		t.Fatal(err)
	}
	// Simulate a process crash before cursor CAS/finalize. The source has since
	// changed and now reports a later cursor, which must not replace the cursor
	// bound into the acknowledged spool.
	recoveryPublisher := &Publisher{
		Builder: builder, Store: sequence, Pending: pending,
		Client: ingestFunc(func(context.Context, ValidatedBatch) (IngestAck, error) {
			t.Fatal("locally acknowledged pending batch should not be resent")
			return IngestAck{}, nil
		}),
	}
	changedPage := ScanPage{
		Projections: []Projection{candidateProjectionForTest()},
		NextCursor:  ScanCursor{Version: 1, ID: 99, Completed: true},
	}
	coordinator := &SyncCoordinator{
		SourceID: "10000000-0000-4000-8000-000000000002", Connector: fixedConnector{page: changedPage},
		Cursors: cursors, Publisher: recoveryPublisher,
	}
	page, _, err := coordinator.SyncPage(ctx, ScanFull)
	if err != nil {
		t.Fatal(err)
	}
	committed, _ := cursors.Load(ctx, "10000000-0000-4000-8000-000000000002")
	if committed.ID != 41 || page.NextCursor.ID != 41 {
		t.Fatalf("recovery skipped to live cursor instead of stored cursor: committed=%#v page=%#v", committed, page.NextCursor)
	}
	if _, exists, _ := pending.Load(ctx); exists {
		t.Fatal("recovered pending spool was not finalized")
	}
}

func TestCrashAfterCursorCommitClearsStaleSpoolBeforeNextBatch(t *testing.T) {
	ctx := context.Background()
	sequence := &MemorySequenceStore{}
	cursors := NewMemoryCursorStore()
	pending := &MemoryPendingBatchStore{}
	builder := BatchBuilder{
		SourceInstanceID: "10000000-0000-4000-8000-000000000002", StreamID: "payments", SourceType: SourceNewAPI,
		SourceRuntimeVersion: "v1.0.0-rc.25", AgentVersion: "test", Mode: "db_projection",
	}
	sends := 0
	publisher := &Publisher{
		Builder: builder, Store: sequence, Pending: pending,
		Client: ingestFunc(func(_ context.Context, batch ValidatedBatch) (IngestAck, error) {
			sends++
			return IngestAck{
				Accepted: true, SourceInstanceID: batch.Batch.SourceInstanceID,
				StreamID: batch.Batch.StreamID, BatchID: batch.Batch.BatchID,
				Sequence: batch.Batch.Sequence, AcceptedRecords: len(batch.Batch.Records),
			}, nil
		}),
	}
	before, _ := cursors.Load(ctx, "10000000-0000-4000-8000-000000000002")
	after := ScanCursor{Revision: before.Revision, Version: 1, ID: 41, Completed: true}
	receipt, err := publisher.PublishPage(ctx, before, after, []Projection{candidateProjectionForTest()})
	if err != nil {
		t.Fatal(err)
	}
	committed, err := cursors.CompareAndSwap(ctx, "10000000-0000-4000-8000-000000000002", before, receipt.CursorAfter)
	if err != nil || !committed {
		t.Fatalf("simulate cursor commit: committed=%t err=%v", committed, err)
	}
	// Simulate the crash before FinalizePage. A fresh call sees the after-cursor
	// with its incremented CAS revision, removes the stale spool and publishes
	// the next sequence instead of failing permanently.
	current, _ := cursors.Load(ctx, "10000000-0000-4000-8000-000000000002")
	next := ScanCursor{Revision: current.Revision, Version: 1, ID: 42, Completed: true}
	if _, err := publisher.PublishPage(ctx, current, next, []Projection{candidateProjectionForTest()}); err != nil {
		t.Fatal(err)
	}
	if sends != 2 {
		t.Fatalf("expected one original and one next-batch send, got %d", sends)
	}
}
