package sourceagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeTestStateEnvelope(t *testing.T, state *FileStateStore, cursor ScanCursor) {
	t.Helper()
	raw, err := json.Marshal(fileStateEnvelope{SchemaVersion: fileStateSchemaVersion, SourceID: state.SourceID, StreamID: state.StreamID, Cursor: cursor})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(state.Path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFileStateStoreDurableCAS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sender-state.json")
	state := &FileStateStore{Path: path, SourceID: "10000000-0000-4000-8000-000000000001", StreamID: "payments"}
	ctx := context.Background()
	if err := InitializeFileState(ctx, state); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("state permissions are too broad: %o", info.Mode().Perm())
	}

	cursors := FileCursorStore{State: state}
	loaded, err := cursors.Load(ctx, "10000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 0 {
		t.Fatalf("unexpected initial cursor: %#v", loaded)
	}
	next := ScanCursor{Revision: loaded.Revision, Version: 1, UpdatedAt: "2026-08-20T12:00:00Z", ID: 7}
	matched, err := cursors.CompareAndSwap(ctx, "10000000-0000-4000-8000-000000000001", loaded, next)
	if err != nil || !matched {
		t.Fatalf("cursor CAS failed: matched=%t err=%v", matched, err)
	}
	if matched, err := cursors.CompareAndSwap(ctx, "10000000-0000-4000-8000-000000000001", loaded, next); err != nil || matched {
		t.Fatalf("stale cursor CAS must not match: matched=%t err=%v", matched, err)
	}

	sequences := FileSequenceStore{State: state}
	publish, err := sequences.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	matched, err = sequences.CompareAndSwap(ctx, publish, SequenceState{Revision: publish.Revision, Sequence: 1, LastBatchHash: hash})
	if err != nil || !matched {
		t.Fatalf("sequence CAS failed: matched=%t err=%v", matched, err)
	}

	// A fresh object proves that state came from disk rather than process memory.
	reopened := &FileStateStore{Path: path, SourceID: "10000000-0000-4000-8000-000000000001", StreamID: "payments"}
	reloadedCursor, err := (FileCursorStore{State: reopened}).Load(ctx, "10000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if reloadedCursor.Revision != 1 || reloadedCursor.ID != 7 {
		t.Fatalf("cursor was not durable: %#v", reloadedCursor)
	}
	reloadedPublish, err := (FileSequenceStore{State: reopened}).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if reloadedPublish.Revision != 1 || reloadedPublish.Sequence != 1 || reloadedPublish.LastBatchHash != hash {
		t.Fatalf("publish state was not durable: %#v", reloadedPublish)
	}
	checkedCursor, checkedSequence, err := reopened.CheckReadOnly()
	if err != nil || checkedCursor != reloadedCursor || checkedSequence != reloadedPublish {
		t.Fatalf("read-only state check cursor=%+v sequence=%+v err=%v", checkedCursor, checkedSequence, err)
	}
	if _, err = os.Lstat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only state check created a lock file")
	}
}

// TestFileScheduleStoreLegacyFileLoadsUnchanged proves that a state.json
// written before the reconcile/full schedule fields existed decodes them as
// empty (XM-INV-AGENT-RESTART-GRACE part C: "load unchanged"), and that
// reading it does not disturb the cursor already on disk.
func TestFileScheduleStoreLegacyFileLoadsUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sender-state.json")
	state := &FileStateStore{Path: path, SourceID: "10000000-0000-4000-8000-000000000001", StreamID: "usage"}
	ctx := context.Background()
	legacyCursor := ScanCursor{Version: 1, UpdatedAt: "2026-08-20T12:00:00Z", ID: 9}
	writeTestStateEnvelope(t, state, legacyCursor)

	schedule := FileScheduleStore{State: state}
	loaded, err := schedule.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != (ScheduleState{}) {
		t.Fatalf("legacy state file must decode an empty schedule, got %#v", loaded)
	}
	cursor, err := (FileCursorStore{State: state}).Load(ctx, state.SourceID)
	if err != nil || cursor.ID != 9 {
		t.Fatalf("reading the schedule must not disturb the existing cursor: cursor=%#v err=%v", cursor, err)
	}
}

// TestFileScheduleStoreDurableAndSharesEnvelopeWithCursor proves the schedule
// persists across a reopen (a fresh *FileStateStore, proving it came from
// disk) and that saving it never clobbers the cursor/sequence fields sharing
// the same envelope file.
func TestFileScheduleStoreDurableAndSharesEnvelopeWithCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sender-state.json")
	state := &FileStateStore{Path: path, SourceID: "10000000-0000-4000-8000-000000000001", StreamID: "usage"}
	ctx := context.Background()
	if err := InitializeFileState(ctx, state); err != nil {
		t.Fatal(err)
	}
	cursors := FileCursorStore{State: state}
	loadedCursor, err := cursors.Load(ctx, state.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	nextCursor := ScanCursor{Revision: loadedCursor.Revision, Version: 1, UpdatedAt: "2026-08-20T12:00:00Z", ID: 3}
	if matched, err := cursors.CompareAndSwap(ctx, state.SourceID, loadedCursor, nextCursor); err != nil || !matched {
		t.Fatalf("seed cursor CAS failed: matched=%t err=%v", matched, err)
	}

	schedule := FileScheduleStore{State: state}
	saved := ScheduleState{LastReconcileAt: "2026-08-21T00:00:00Z", LastFullAt: "2026-08-21T01:00:00Z"}
	if err := schedule.Save(ctx, saved); err != nil {
		t.Fatal(err)
	}

	reopened := &FileStateStore{Path: path, SourceID: state.SourceID, StreamID: state.StreamID}
	reloadedSchedule, err := (FileScheduleStore{State: reopened}).Load(ctx)
	if err != nil || reloadedSchedule != saved {
		t.Fatalf("schedule was not durable: got=%#v err=%v", reloadedSchedule, err)
	}
	reloadedCursor, err := (FileCursorStore{State: reopened}).Load(ctx, state.SourceID)
	if err != nil || reloadedCursor.ID != 3 {
		t.Fatalf("saving the schedule must not disturb the cursor: cursor=%#v err=%v", reloadedCursor, err)
	}

	if err := schedule.Save(ctx, ScheduleState{LastReconcileAt: "not-a-time"}); err == nil {
		t.Fatal("an unparseable schedule timestamp was accepted")
	}
}

func TestFileStateStoreFailsClosedWithoutInitializedVolumeState(t *testing.T) {
	state := &FileStateStore{Path: filepath.Join(t.TempDir(), "missing.json"), SourceID: "10000000-0000-4000-8000-000000000002", StreamID: "payments"}
	if _, err := (FileCursorStore{State: state}).Load(context.Background(), "10000000-0000-4000-8000-000000000002"); err == nil {
		t.Fatal("missing state file was accepted")
	}
}

func TestFileStateStoreCheckReadOnlyEnforcesV3SnapshotMetadataByStream(t *testing.T) {
	ctx := context.Background()
	sourceID := "10000000-0000-4000-8000-000000000002"

	balances := &FileStateStore{Path: filepath.Join(t.TempDir(), "balances.json"), SourceID: sourceID, StreamID: StreamBalances}
	if err := InitializeFileState(ctx, balances); err != nil {
		t.Fatal(err)
	}
	missing := testV3Cursor(StreamBalances, "missing-snapshot")
	writeTestStateEnvelope(t, balances, missing)
	if _, _, err := balances.CheckReadOnly(); err == nil {
		t.Fatal("V3 balances state without snapshot metadata was accepted")
	}

	payments := &FileStateStore{Path: filepath.Join(t.TempDir(), "payments.json"), SourceID: sourceID, StreamID: StreamPayments}
	if err := InitializeFileState(ctx, payments); err != nil {
		t.Fatal(err)
	}
	extra := testV3Cursor(StreamPayments, "unexpected-snapshot")
	extra.SnapshotID = SHA256Hex([]byte("unexpected"))
	extra.SnapshotRowCount = 0
	extra.HasSnapshotMetadata = true
	writeTestStateEnvelope(t, payments, extra)
	if _, _, err := payments.CheckReadOnly(); err == nil {
		t.Fatal("V3 non-balance state with snapshot metadata was accepted")
	}
}

func TestFileStateStoreRejectsBroadPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows permissions are enforced through the volume ACL and Chmod emulation")
	}
	path := filepath.Join(t.TempDir(), "state.json")
	state := &FileStateStore{Path: path, SourceID: "10000000-0000-4000-8000-000000000002", StreamID: "identity"}
	if err := InitializeFileState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (FileSequenceStore{State: state}).Load(context.Background()); err == nil {
		t.Fatal("broad state-file permissions were accepted")
	}
}

func TestFileStateStoreRejectsBroadStateDirectoryPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows directory access is enforced through the volume DACL")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	state := &FileStateStore{
		Path: filepath.Join(directory, "state.json"), SourceID: "10000000-0000-4000-8000-000000000002", StreamID: "payments",
	}
	if err := InitializeFileState(context.Background(), state); err == nil {
		t.Fatal("world-readable/traversable state directory was accepted")
	}
}
