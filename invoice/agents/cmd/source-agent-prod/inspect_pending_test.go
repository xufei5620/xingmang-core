package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"invoice-system/agents/sourceagent"
)

type pendingInspectionFixture struct {
	values      map[string]string
	batchID     string
	bodyHash    string
	scanCycleID string
	sensitive   string
	spoolKey    string
	before      sourceagent.ScanCursor
	after       sourceagent.ScanCursor
}

type pendingInspectionFileSnapshot struct {
	Mode     os.FileMode
	Size     int64
	Modified int64
	SHA256   string
}

func newPendingInspectionFixture(t *testing.T, withPending bool) pendingInspectionFixture {
	t.Helper()
	values := validEnvironment(t, sourceagent.SourceSub2API, sourceagent.StreamUsage)
	directory := filepath.Dir(values["SOURCE_STATE_FILE"])
	values["SOURCE_SCHEMA_VERSION"] = sourceagent.SchemaVersionV3
	values["SOURCE_CUTOVER_MANIFEST_FILE"] = filepath.Join(directory, "cutover.enc")
	values["SOURCE_CUTOVER_KEY_FILE"] = filepath.Join(directory, "cutover.key")

	spoolKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	if err := os.WriteFile(values["SOURCE_SPOOL_KEY_FILE"], []byte(spoolKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(values["SOURCE_SPOOL_KEY_FILE"], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(values["SOURCE_DB_DSN_FILE"], []byte("postgresql://reader:dsn-secret-token@source.invalid/source"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := &sourceagent.FileStateStore{
		Path: values["SOURCE_STATE_FILE"], SourceID: values["SOURCE_ID"], StreamID: values["SOURCE_STATE_STREAM"],
	}
	if err := sourceagent.InitializeFileState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	fixture := pendingInspectionFixture{
		values: values, sensitive: "9876543210987654321", spoolKey: spoolKey,
	}
	if !withPending {
		return fixture
	}

	cutoverAt := "2026-08-20T00:00:00Z"
	watermarkAt := "2026-09-02T01:00:00Z"
	ceilingAt := "2026-09-02T02:00:00Z"
	fixture.after = sourceagent.ScanCursor{
		Version: 2, CutoverAt: cutoverAt, WatermarkAt: watermarkAt, WatermarkCursor: "usage:100",
		CeilingAt: ceilingAt, CeilingCursor: "usage:999", PositionCursor: "usage:100",
		ScanCycleID: "20000000-0000-4000-8000-000000000001", Completed: true,
	}
	causalOrder := "100"
	hash := strings.Repeat("a", 64)
	batch, err := (sourceagent.BatchBuilder{
		SchemaVersion: sourceagent.SchemaVersionV3, SourceInstanceID: values["SOURCE_ID"],
		StreamID: sourceagent.StreamUsage, SourceType: sourceagent.SourceSub2API,
		SourceRuntimeVersion: values["SOURCE_RUNTIME_VERSION"], AgentVersion: "inspect-pending-test",
		Mode: "db_projection", Now: func() time.Time { return time.Date(2026, 9, 2, 2, 1, 0, 0, time.UTC) },
	}).BuildPage(1, "", []sourceagent.Projection{{
		EntityType: sourceagent.EntityUsageEvent, ExternalID: "usage:100", ObservedAt: watermarkAt,
		Payload: sourceagent.UsageEventPayload{
			ExternalUserID: fixture.sensitive, ExternalUsageID: "usage:100", OccurredAt: watermarkAt,
			ServiceUnits: "100", UnitCode: "SUB2_BALANCE_1E8", BillingScope: "wallet",
			FactMetadata: sourceagent.FactMetadata{
				SourceCursor: "usage:100", CausalDomain: "wallet_usage", CausalOrder: &causalOrder,
				CutoverManifestHash: hash, ConfigurationHash: hash,
			},
		},
	}}, fixture.after)
	if err != nil {
		t.Fatal(err)
	}
	pending := sourceagent.PendingBatch{Batch: batch, CursorBefore: fixture.before, CursorAfter: fixture.after}
	store := &sourceagent.EncryptedFilePendingStore{
		Path: values["SOURCE_SPOOL_FILE"], SourceID: values["SOURCE_ID"], StreamID: values["SOURCE_STATE_STREAM"],
		Keys: sourceagent.FileSpoolKeyProvider{Path: values["SOURCE_SPOOL_KEY_FILE"]},
	}
	stored, err := store.SaveIfAbsent(context.Background(), pending)
	if err != nil || !stored {
		pending.Destroy()
		t.Fatalf("save encrypted pending fixture: stored=%t err=%v", stored, err)
	}
	fixture.batchID = batch.Batch.BatchID
	fixture.bodyHash = batch.BodyHash
	fixture.scanCycleID = batch.Batch.ScanCycleID
	pending.Destroy()
	return fixture
}

func snapshotPendingInspectionDirectory(t *testing.T, directory string) map[string]pendingInspectionFileSnapshot {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]pendingInspectionFileSnapshot, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("unexpected directory in inspection fixture: %s", entry.Name())
		}
		path := filepath.Join(directory, entry.Name())
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = pendingInspectionFileSnapshot{
			Mode: info.Mode(), Size: info.Size(), Modified: info.ModTime().UnixNano(), SHA256: sourceagent.SHA256Hex(raw),
		}
		for index := range raw {
			raw[index] = 0
		}
	}
	return result
}

func TestInspectPendingEmitsOnlyAuthenticatedMetadataWithoutWriting(t *testing.T) {
	fixture := newPendingInspectionFixture(t, true)
	directory := filepath.Dir(fixture.values["SOURCE_STATE_FILE"])
	before := snapshotPendingInspectionDirectory(t, directory)
	var output bytes.Buffer
	if err := inspectPendingFromEnvironment(mapEnvironment(fixture.values), &output); err != nil {
		t.Fatal(err)
	}
	after := snapshotPendingInspectionDirectory(t, directory)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("read-only pending inspection changed the state directory\nbefore=%#v\nafter=%#v", before, after)
	}
	if locks, err := filepath.Glob(filepath.Join(directory, "*.lock")); err != nil || len(locks) != 0 {
		t.Fatalf("read-only pending inspection created a lock: locks=%v err=%v", locks, err)
	}

	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	decoder.DisallowUnknownFields()
	var report pendingInspectionReport
	if err := decoder.Decode(&report); err != nil {
		t.Fatal(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("inspection output was not exactly one JSON value: %v", err)
	}
	if report.InspectionSchemaVersion != 1 || report.Source != fixture.values["SOURCE_ID"] ||
		report.SourceType != sourceagent.SourceSub2API || report.Stream != sourceagent.StreamUsage || !report.Pending {
		t.Fatalf("unexpected report identity: %#v", report)
	}
	if report.Batch == nil || report.Batch.BatchID != fixture.batchID || report.Batch.Sequence != 1 ||
		report.Batch.PreviousBatchHash != nil || report.Batch.BodyHash != fixture.bodyHash ||
		report.Batch.RecordCount != 1 || report.Batch.ScanCycleID != fixture.scanCycleID || !report.Batch.ScanComplete {
		t.Fatalf("unexpected batch metadata: %#v", report.Batch)
	}
	expectedBefore, _ := pendingCursorMetadata(fixture.before)
	expectedAfter, _ := pendingCursorMetadata(fixture.after)
	if report.CursorBefore == nil || *report.CursorBefore != expectedBefore || report.CursorAfter == nil || *report.CursorAfter != expectedAfter {
		t.Fatalf("unexpected cursor fingerprints: before=%#v after=%#v", report.CursorBefore, report.CursorAfter)
	}
	if report.DurableState.Sequence != 0 || report.DurableState.PublishRevision != 0 || report.DurableState.LastBatchHash != "" {
		t.Fatalf("unexpected durable state: %#v", report.DurableState)
	}
	if report.Consistency == nil || report.Consistency.Lifecycle != "sequence_uncommitted" ||
		!report.Consistency.SourceStream || !report.Consistency.SchemaRuntime || !report.Consistency.BodyHash ||
		!report.Consistency.HashChain || !report.Consistency.Sequence || !report.Consistency.PublishRevision ||
		!report.Consistency.Cursor || !report.Consistency.CursorRevision || !report.Consistency.BatchCursorScanMetadata {
		t.Fatalf("unexpected consistency proof: %#v", report.Consistency)
	}

	rawOutput := output.String()
	for _, forbidden := range []string{
		fixture.sensitive, fixture.spoolKey, "dsn-secret-token", fixture.values["SOURCE_DB_DSN_FILE"],
		`"records"`, `"payload"`, `"raw_body"`, `"external_user_id"`,
	} {
		if strings.Contains(rawOutput, forbidden) {
			t.Fatalf("inspection leaked forbidden plaintext %q: %s", forbidden, rawOutput)
		}
	}
}

func TestInspectPendingReportsAbsenceWithoutCreatingSpoolOrLock(t *testing.T) {
	fixture := newPendingInspectionFixture(t, false)
	directory := filepath.Dir(fixture.values["SOURCE_STATE_FILE"])
	before := snapshotPendingInspectionDirectory(t, directory)
	var output bytes.Buffer
	if err := inspectPendingFromEnvironment(mapEnvironment(fixture.values), &output); err != nil {
		t.Fatal(err)
	}
	after := snapshotPendingInspectionDirectory(t, directory)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("inspection of an absent spool changed the state directory")
	}
	var report pendingInspectionReport
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Pending || report.Batch != nil || report.CursorBefore != nil || report.CursorAfter != nil || report.Consistency != nil {
		t.Fatalf("absent pending spool produced batch metadata: %#v", report)
	}
	if _, err := os.Lstat(fixture.values["SOURCE_SPOOL_FILE"]); !os.IsNotExist(err) {
		t.Fatalf("inspection created an absent spool: %v", err)
	}
}

func TestInspectPendingFailsClosedOnWrongSpoolKeyWithoutOutputOrWrites(t *testing.T) {
	fixture := newPendingInspectionFixture(t, true)
	wrongKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x24}, 32))
	if err := os.WriteFile(fixture.values["SOURCE_SPOOL_KEY_FILE"], []byte(wrongKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(fixture.values["SOURCE_STATE_FILE"])
	before := snapshotPendingInspectionDirectory(t, directory)
	var output bytes.Buffer
	err := inspectPendingFromEnvironment(mapEnvironment(fixture.values), &output)
	after := snapshotPendingInspectionDirectory(t, directory)
	if err == nil || !strings.Contains(err.Error(), "pending spool authentication failed") {
		t.Fatalf("wrong spool key was not rejected generically: %v", err)
	}
	if output.Len() != 0 || !reflect.DeepEqual(before, after) {
		t.Fatalf("failed inspection emitted output or changed files: output=%q", output.String())
	}
}

func TestInspectPendingRejectsLockWithoutRemovingItOrEmittingOutput(t *testing.T) {
	fixture := newPendingInspectionFixture(t, true)
	directory := filepath.Dir(fixture.values["SOURCE_STATE_FILE"])
	lockPath := filepath.Join(directory, "operator-observed.lock")
	if err := os.WriteFile(lockPath, []byte("held-or-stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotPendingInspectionDirectory(t, directory)
	var output bytes.Buffer
	err := inspectPendingFromEnvironment(mapEnvironment(fixture.values), &output)
	after := snapshotPendingInspectionDirectory(t, directory)
	if err == nil || !strings.Contains(err.Error(), "stopped agent and a lock-free state directory") {
		t.Fatalf("state lock was not rejected generically: %v", err)
	}
	if output.Len() != 0 || !reflect.DeepEqual(before, after) {
		t.Fatalf("lock rejection emitted output, removed the lock or changed state: output=%q", output.String())
	}
}

func TestPendingLifecycleAcceptsOnlyReachableCrashWindows(t *testing.T) {
	bodyHash := strings.Repeat("b", 64)
	before := sourceagent.ScanCursor{}
	after := sourceagent.ScanCursor{Version: 1, ID: 10}
	pending := sourceagent.PendingBatch{
		Batch:        sourceagent.ValidatedBatch{Batch: sourceagent.Batch{Sequence: 1}, BodyHash: bodyHash},
		CursorBefore: before, CursorAfter: after,
	}
	committedCursor := after
	committedCursor.Revision++
	for _, test := range []struct {
		name     string
		cursor   sourceagent.ScanCursor
		sequence sourceagent.SequenceState
		want     string
		invalid  bool
	}{
		{name: "sequence uncommitted", cursor: before, sequence: sourceagent.SequenceState{}, want: "sequence_uncommitted"},
		{name: "sequence committed cursor pending", cursor: before, sequence: sourceagent.SequenceState{Revision: 1, Sequence: 1, LastBatchHash: bodyHash}, want: "sequence_committed_cursor_pending"},
		{name: "cursor committed pending clear", cursor: committedCursor, sequence: sourceagent.SequenceState{Revision: 1, Sequence: 1, LastBatchHash: bodyHash}, want: "cursor_committed_pending_clear"},
		{name: "cursor cannot lead sequence", cursor: committedCursor, sequence: sourceagent.SequenceState{}, invalid: true},
		{name: "publish revision mismatch", cursor: before, sequence: sourceagent.SequenceState{Revision: 2, Sequence: 1, LastBatchHash: bodyHash}, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := pendingLifecycle(test.cursor, test.sequence, pending)
			if test.invalid {
				if err == nil {
					t.Fatalf("invalid state accepted as %q", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("got=%q err=%v want=%q", got, err, test.want)
			}
		})
	}
}
