package sourceagent

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testBalanceSnapshot(t *testing.T, kind, asOf, predecessor string, rows []BalanceSnapshotRow) BalanceSnapshot {
	t.Helper()
	value := BalanceSnapshot{SchemaVersion: cutoverSchemaVersion,
		SourceID: "10000000-0000-4000-8000-000000000001", SourceType: SourceSub2API,
		PreviousSnapshotID: predecessor, CheckpointKind: kind, AsOf: asOf,
		CutoverAt: "2026-08-20T00:00:00Z", UnitCode: "SUB2_BALANCE_1E8", Rows: rows}
	var err error
	value.SnapshotID, err = balanceSnapshotID(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateBalanceSnapshot(value); err != nil {
		t.Fatal(err)
	}
	return value
}

func testBalanceStores(t *testing.T, baseline BalanceSnapshot) (EncryptedStateFile, EncryptedStateFile) {
	t.Helper()
	directory := t.TempDir()
	keyPath := filepath.Join(directory, "balance.key")
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x51}, 32))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	keys := FileSpoolKeyProvider{Path: keyPath}
	baselineStore := EncryptedStateFile{Path: filepath.Join(directory, "baseline.enc"), Purpose: "balance_baseline", Keys: keys}
	currentStore := EncryptedStateFile{Path: filepath.Join(directory, "current.enc"), Purpose: "balance_snapshot", Keys: keys}
	if err := baselineStore.SaveNew(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}
	return baselineStore, currentStore
}

func testBalanceState(t *testing.T, schema int, kind string, retired []string) balanceReconciliationState {
	t.Helper()
	captured := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:01:00Z", "", nil)
	emission := testBalanceSnapshot(t, "reconciliation", captured.AsOf, strings.Repeat("a", 64), nil)
	return balanceReconciliationState{SchemaVersion: schema, StateKind: kind,
		BaseSnapshotID: strings.Repeat("a", 64), CapturedSnapshot: captured,
		EmissionSnapshot: emission, RetiredZeroAccountIDs: retired}
}

func buildStateWithCapturedRow(t *testing.T, retired []string, row BalanceSnapshotRow) (balanceReconciliationState, error) {
	t.Helper()
	previous := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:00:00Z", "", nil)
	captured := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:01:00Z", "", []BalanceSnapshotRow{row})
	return buildBalanceReconciliationStateWithRetired(previous.SnapshotID, previous, captured, retired)
}

func buildStateWithMissingPriorRow(t *testing.T, retired []string, row BalanceSnapshotRow) (balanceReconciliationState, error) {
	t.Helper()
	previous := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:00:00Z", "", []BalanceSnapshotRow{row})
	captured := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:01:00Z", "", nil)
	return buildBalanceReconciliationStateWithRetired(previous.SnapshotID, previous, captured, retired)
}

func requireReplaceBalanceState(t *testing.T, store EncryptedStateFile, state balanceReconciliationState) {
	t.Helper()
	if err := store.Replace(context.Background(), state); err != nil {
		t.Fatal(err)
	}
}

func testBalanceManifest(baseline BalanceSnapshot) CutoverManifest {
	return CutoverManifest{SourceID: baseline.SourceID, SourceType: baseline.SourceType,
		CutoverAt: baseline.CutoverAt, BaselineSnapshotID: baseline.SnapshotID,
		ManifestHash: strings.Repeat("a", 64), ConfigurationHash: strings.Repeat("b", 64)}
}

func TestBalanceDeltaIdenticalFullScanPublishesSignedEmptyCompletion(t *testing.T) {
	rows := []BalanceSnapshotRow{{ExternalUserID: "1", ServiceUnits: "100", BaselineMember: true}}
	previous := testBalanceSnapshot(t, "cutover", "2026-08-20T00:00:00Z", "", rows)
	captured := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:01:00Z", "", rows)
	state, err := buildBalanceReconciliationState(previous.SnapshotID, previous, captured)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.EmissionSnapshot.Rows) != 0 {
		t.Fatalf("unchanged full scan emitted %d balance events", len(state.EmissionSnapshot.Rows))
	}
	cursor := ScanCursor{Version: 2, CutoverAt: previous.CutoverAt,
		WatermarkAt: captured.AsOf, WatermarkCursor: "balance_snapshot:1",
		CeilingAt: captured.AsOf, CeilingCursor: "balance_snapshot:1", PositionCursor: "balance_snapshot:0",
		SnapshotID: state.EmissionSnapshot.SnapshotID, SnapshotRowCount: 0, HasSnapshotMetadata: true,
		ScanCycleID: deterministicUUID("unchanged-balance-cycle"), Completed: true}
	built, err := (BatchBuilder{SchemaVersion: SchemaVersionV3, SourceInstanceID: previous.SourceID,
		StreamID: StreamBalances, SourceType: SourceSub2API, SourceRuntimeVersion: "0.1.179",
		AgentVersion: "test", Mode: "db_projection"}).BuildPage(1, "", nil, cursor)
	if err != nil {
		t.Fatal(err)
	}
	if !built.Batch.ScanComplete || built.Batch.ScanSnapshotRowCount == nil || *built.Batch.ScanSnapshotRowCount != 0 || len(built.Batch.Records) != 0 {
		t.Fatalf("unchanged scan was not a complete signed empty heartbeat: %#v", built.Batch)
	}
}

func TestBalanceDeltaEmitsFirstPostCutoverAndOnlyRealChanges(t *testing.T) {
	baselineRows := []BalanceSnapshotRow{{ExternalUserID: "1", ServiceUnits: "100", BaselineMember: true}}
	baseline := testBalanceSnapshot(t, "cutover", "2026-08-20T00:00:00Z", "", baselineRows)
	capturedRows := []BalanceSnapshotRow{
		{ExternalUserID: "1", ServiceUnits: "100", BaselineMember: true},
		{ExternalUserID: "2", ServiceUnits: "0", BaselineMember: false},
	}
	captured := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:01:00Z", "", capturedRows)
	state, err := buildBalanceReconciliationState(baseline.SnapshotID, baseline, captured)
	if err != nil {
		t.Fatal(err)
	}
	if want := []BalanceSnapshotRow{capturedRows[1]}; !reflect.DeepEqual(state.EmissionSnapshot.Rows, want) {
		t.Fatalf("first post-cutover account delta=%#v want=%#v", state.EmissionSnapshot.Rows, want)
	}

	changedRows := append([]BalanceSnapshotRow(nil), capturedRows...)
	changedRows[0].ServiceUnits = "75"
	changedRows[0].BalanceNegative = true
	changed := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:02:00Z", "", changedRows)
	next, err := buildBalanceReconciliationState(state.EmissionSnapshot.SnapshotID, captured, changed)
	if err != nil {
		t.Fatal(err)
	}
	if want := []BalanceSnapshotRow{changedRows[0]}; !reflect.DeepEqual(next.EmissionSnapshot.Rows, want) {
		t.Fatalf("real balance change delta=%#v want=%#v", next.EmissionSnapshot.Rows, want)
	}
}

func TestBalanceDeltaPreparedCycleIsRetryStableAndPreservesABA(t *testing.T) {
	rowsA := []BalanceSnapshotRow{{ExternalUserID: "1", ServiceUnits: "100", BaselineMember: true}}
	baseline := testBalanceSnapshot(t, "cutover", "2026-08-20T00:00:00Z", "", rowsA)
	baselineStore, currentStore := testBalanceStores(t, baseline)
	connector := &BalanceDBConnector{Source: SourceSub2API, SourceID: baseline.SourceID,
		Manifest: testBalanceManifest(baseline), Baseline: baselineStore, Current: currentStore}
	baseCursor := ScanCursor{Version: 2, CutoverAt: baseline.CutoverAt, SnapshotID: baseline.SnapshotID, Completed: true}

	rowsB := []BalanceSnapshotRow{{ExternalUserID: "1", ServiceUnits: "80", BaselineMember: true}}
	capturedB := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:01:00Z", "", rowsB)
	captures := 0
	first, err := connector.prepareReconciliationCycle(context.Background(), baseCursor, func(context.Context) (BalanceSnapshot, error) {
		captures++
		return capturedB, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := connector.prepareReconciliationCycle(context.Background(), baseCursor, func(context.Context) (BalanceSnapshot, error) {
		captures++
		return BalanceSnapshot{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if captures != 1 || !reflect.DeepEqual(first, retry) {
		t.Fatalf("prepared ACK retry drifted: captures=%d first=%#v retry=%#v", captures, first, retry)
	}
	manifest := testBalanceManifest(baseline)
	recordB1, err := projectionRecord(baseline.SourceID,
		balanceCheckpointProjection(manifest, first.Snapshot, first.Snapshot.Rows[0], time.Date(2026, 8, 25, 0, 1, 1, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	recordB2, err := projectionRecord(baseline.SourceID,
		balanceCheckpointProjection(manifest, retry.Snapshot, retry.Snapshot.Rows[0], time.Date(2026, 8, 25, 0, 1, 2, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	if recordB1.EventID != recordB2.EventID || recordB1.PayloadSHA256 != recordB2.PayloadSHA256 {
		t.Fatal("ACK retry changed the balance fact identity")
	}

	ackedCursor := baseCursor
	ackedCursor.SnapshotID = first.Snapshot.SnapshotID
	rowsAAgain := []BalanceSnapshotRow{{ExternalUserID: "1", ServiceUnits: "100", BaselineMember: true}}
	// Deliberately reuse the same source timestamp: the predecessor hash must
	// still distinguish A->B->A from the earlier A state.
	capturedAAgain := testBalanceSnapshot(t, "reconciliation", capturedB.AsOf, "", rowsAAgain)
	returned, err := connector.prepareReconciliationCycle(context.Background(), ackedCursor, func(context.Context) (BalanceSnapshot, error) {
		return capturedAAgain, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(returned.Snapshot.Rows) != 1 || returned.Snapshot.SnapshotID == first.Snapshot.SnapshotID || returned.Snapshot.PreviousSnapshotID != first.Snapshot.SnapshotID {
		t.Fatalf("A->B->A did not form a distinct ordered snapshot chain: %#v", returned.Snapshot)
	}
	recordA, err := projectionRecord(baseline.SourceID,
		balanceCheckpointProjection(manifest, returned.Snapshot, returned.Snapshot.Rows[0], time.Date(2026, 8, 25, 0, 1, 3, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	if recordA.EventID == recordB1.EventID {
		t.Fatal("A->B->A reused the B balance event identity")
	}
}

func TestBalanceDeltaLoadsLegacyCurrentSnapshotAndRejectsDisappearances(t *testing.T) {
	baselineRows := []BalanceSnapshotRow{{ExternalUserID: "1", ServiceUnits: "100", BaselineMember: true}}
	baseline := testBalanceSnapshot(t, "cutover", "2026-08-20T00:00:00Z", "", baselineRows)
	baselineStore, currentStore := testBalanceStores(t, baseline)
	legacy := testBalanceSnapshot(t, "reconciliation", "2026-08-24T00:00:00Z", "", baselineRows)
	if err := currentStore.Replace(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
	connector := &BalanceDBConnector{Source: SourceSub2API, SourceID: baseline.SourceID,
		Manifest: testBalanceManifest(baseline), Baseline: baselineStore, Current: currentStore}
	cursor := ScanCursor{Version: 2, CutoverAt: baseline.CutoverAt, SnapshotID: legacy.SnapshotID, Completed: true}
	captured := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:00:00Z", "", baselineRows)
	cycle, err := connector.prepareReconciliationCycle(context.Background(), cursor, func(context.Context) (BalanceSnapshot, error) { return captured, nil })
	if err != nil || len(cycle.Snapshot.Rows) != 0 {
		t.Fatalf("legacy state was not migrated to an empty delta: cycle=%#v err=%v", cycle, err)
	}
	state, legacyAfter, exists, err := loadCurrentBalanceReconciliationState(context.Background(), currentStore)
	if err != nil || !exists || state == nil || legacyAfter != nil {
		t.Fatalf("legacy current state was not replaced by delta state: exists=%t state=%#v legacy=%#v err=%v", exists, state, legacyAfter, err)
	}

	missing := testBalanceSnapshot(t, "reconciliation", "2026-08-26T00:00:00Z", "", nil)
	if _, err = buildBalanceReconciliationState(cycle.Snapshot.SnapshotID, captured, missing); err == nil || !strings.Contains(err.Error(), "removed an account") {
		t.Fatalf("account disappearance did not fail closed: %v", err)
	}
}

func TestBalanceDeltaLoadsV1StateAsV2WithEmptyRetiredSet(t *testing.T) {
	state := testBalanceState(t, 1, "balance_delta_v1", nil)
	_, current := testBalanceStores(t, state.CapturedSnapshot)
	requireReplaceBalanceState(t, current, state)
	loaded, legacy, exists, err := loadCurrentBalanceReconciliationState(context.Background(), current)
	if err != nil || !exists || legacy != nil || loaded.SchemaVersion != 2 || len(loaded.RetiredZeroAccountIDs) != 0 {
		t.Fatalf("v1 migration failed: loaded=%#v legacy=%#v exists=%t err=%v", loaded, legacy, exists, err)
	}
}

func TestBalanceDeltaRejectsInvalidRetiredSets(t *testing.T) {
	for _, ids := range [][]string{{"2", "2"}, {"10", "9"}, {"01"}, {"1\n"}} {
		state := testBalanceState(t, 2, "balance_delta_v2", ids)
		if err := validateBalanceReconciliationState(state); err == nil {
			t.Fatalf("invalid retired set accepted: %#v", ids)
		}
	}

	captured := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:01:00Z", "",
		[]BalanceSnapshotRow{{ExternalUserID: "2", ServiceUnits: "0"}})
	state := testBalanceState(t, 2, "balance_delta_v2", []string{"2"})
	state.CapturedSnapshot = captured
	if err := validateBalanceReconciliationState(state); err == nil {
		t.Fatal("retired set intersecting the captured rows was accepted")
	}

	oversized := make([]string, maxRetiredBalanceAccounts+1)
	for index := range oversized {
		oversized[index] = strconv.Itoa(index + 1)
	}
	state = testBalanceState(t, 2, "balance_delta_v2", oversized)
	if err := validateBalanceReconciliationState(state); err == nil {
		t.Fatal("oversized retired set was accepted")
	}
}

func TestBalanceDeltaRetiresMultipleZeroAccountsInNumericOrder(t *testing.T) {
	previous := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:00:00Z", "", []BalanceSnapshotRow{
		{ExternalUserID: "2", ServiceUnits: "0"},
		{ExternalUserID: "9", ServiceUnits: "0"},
		{ExternalUserID: "10", ServiceUnits: "0"},
		{ExternalUserID: "11", ServiceUnits: "50"},
	})
	capturedRows := []BalanceSnapshotRow{
		{ExternalUserID: "3", ServiceUnits: "0"},
		{ExternalUserID: "11", ServiceUnits: "50"},
	}
	captured := testBalanceSnapshot(t, "reconciliation", "2026-08-25T00:01:00Z", "", capturedRows)
	state, err := buildBalanceReconciliationStateWithRetired(previous.SnapshotID, previous, captured, []string{"1"})
	if err != nil || !reflect.DeepEqual(state.RetiredZeroAccountIDs, []string{"1", "2", "9", "10"}) {
		t.Fatalf("retirement union failed: state=%#v err=%v", state, err)
	}
	if want := []BalanceSnapshotRow{capturedRows[0]}; !reflect.DeepEqual(state.EmissionSnapshot.Rows, want) {
		t.Fatalf("retirements emitted synthetic rows or ordinary new account was omitted: got=%#v want=%#v", state.EmissionSnapshot.Rows, want)
	}
}

func TestBalanceDeltaRetiredAccountReappearanceAlwaysFails(t *testing.T) {
	for _, row := range []BalanceSnapshotRow{
		{ExternalUserID: "9", ServiceUnits: "0"},
		{ExternalUserID: "9", ServiceUnits: "1"},
		{ExternalUserID: "9", ServiceUnits: "0", BalanceNegative: true},
		{ExternalUserID: "9", ServiceUnits: "0", BaselineMember: true},
	} {
		if _, err := buildStateWithCapturedRow(t, []string{"9"}, row); err == nil {
			t.Fatalf("retired ID reappeared without failure: %#v", row)
		}
	}
}

func TestBalanceDeltaNonZeroRemovalLeavesRetiredSetUnchanged(t *testing.T) {
	for _, row := range []BalanceSnapshotRow{
		{ExternalUserID: "9", ServiceUnits: "1"},
		{ExternalUserID: "9", ServiceUnits: "0", BalanceNegative: true},
	} {
		if _, err := buildStateWithMissingPriorRow(t, []string{"2"}, row); err == nil {
			t.Fatalf("unsafe removal was accepted: %#v", row)
		}
	}
}
