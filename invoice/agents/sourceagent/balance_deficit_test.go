package sourceagent

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// XM-INV-NEGATIVE-DEFICIT: the bridge reports a negative balance as zero
// units plus its magnitude. These tests pin the agent-side invariants that
// let the receiver trust that magnitude.

func TestBalanceRowDeficitRequiresTheUpgradedBridge(t *testing.T) {
	if _, err := balanceRowDeficit(sql.NullString{}, "0", true); err == nil ||
		!strings.Contains(err.Error(), "install the current economic bridge") {
		t.Fatalf("a NULL deficit (bridge without deficit_service_units) must fail closed, got %v", err)
	}
}

func TestBalanceRowDeficitEnforcesTheFlagInvariant(t *testing.T) {
	cases := []struct {
		name     string
		deficit  string
		units    string
		negative bool
		want     string
		wantErr  bool
	}{
		{name: "positive balance, zero deficit", deficit: "0", units: "150", negative: false, want: "0"},
		{name: "zero balance, zero deficit", deficit: "0", units: "0", negative: false, want: "0"},
		{name: "negative balance with magnitude", deficit: "1361800", units: "0", negative: true, want: "1361800"},
		{name: "negative flag without magnitude", deficit: "0", units: "0", negative: true, wantErr: true},
		{name: "magnitude without negative flag", deficit: "5", units: "0", negative: false, wantErr: true},
		{name: "negative balance with floored units above zero", deficit: "5", units: "3", negative: true, wantErr: true},
		{name: "malformed magnitude", deficit: "-5", units: "0", negative: true, wantErr: true},
		{name: "leading zero magnitude", deficit: "05", units: "0", negative: true, wantErr: true},
	}
	for _, tc := range cases {
		got, err := balanceRowDeficit(sql.NullString{String: tc.deficit, Valid: true}, tc.units, tc.negative)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: expected an error, got deficit %q", tc.name, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("%s: got %q err=%v, want %q", tc.name, got, err, tc.want)
		}
	}
}

func TestChangedBalanceRowsTreatsADeficitMoveAsAChange(t *testing.T) {
	previous := []BalanceSnapshotRow{
		{ExternalUserID: "12", ServiceUnits: "0", BalanceNegative: true, DeficitServiceUnits: "300000"},
		{ExternalUserID: "34", ServiceUnits: "500", DeficitServiceUnits: "0"},
	}
	captured := []BalanceSnapshotRow{
		{ExternalUserID: "12", ServiceUnits: "0", BalanceNegative: true, DeficitServiceUnits: "500000"},
		{ExternalUserID: "34", ServiceUnits: "500", DeficitServiceUnits: "0"},
	}
	changed, retired, err := changedBalanceRowsAndRetirements(previous, captured)
	if err != nil {
		t.Fatal(err)
	}
	if len(retired) != 0 {
		t.Fatalf("no account retired, got %v", retired)
	}
	if len(changed) != 1 || changed[0].ExternalUserID != "12" || changed[0].DeficitServiceUnits != "500000" {
		t.Fatalf("a deficit moving from -300000 to -500000 units must publish a new checkpoint, got %+v", changed)
	}
}

func TestChangedBalanceRowsNeverRetiresANegativeAccount(t *testing.T) {
	previous := []BalanceSnapshotRow{
		{ExternalUserID: "12", ServiceUnits: "0", BalanceNegative: true, DeficitServiceUnits: "7"},
	}
	if _, _, err := changedBalanceRowsAndRetirements(previous, nil); err == nil {
		t.Fatal("a negative account missing from a full capture must fail closed, not retire")
	}
}

// TestBalanceSnapshotHashIgnoresAnAbsentDeficit proves an immutable baseline
// sealed before the field existed still verifies: its stored JSON has no
// deficit key, and re-marshalling the decoded struct must reproduce the
// same bytes (omitempty), hence the same SnapshotID.
func TestBalanceSnapshotHashIgnoresAnAbsentDeficit(t *testing.T) {
	legacy := testBalanceSnapshot(t, "cutover", "2026-08-20T00:00:00Z", "", []BalanceSnapshotRow{
		{ExternalUserID: "12", ServiceUnits: "100", BaselineMember: true},
		{ExternalUserID: "34", ServiceUnits: "0", BalanceNegative: true, BaselineMember: true},
	})
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "deficit_service_units") {
		t.Fatalf("a snapshot without deficits must serialise without the key, got %s", raw)
	}
	var decoded BalanceSnapshot
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	recomputed, err := balanceSnapshotID(decoded)
	if err != nil || recomputed != legacy.SnapshotID {
		t.Fatalf("legacy baseline must keep its content hash: recomputed %s want %s err=%v", recomputed, legacy.SnapshotID, err)
	}
	if err := validateBalanceSnapshot(decoded); err != nil {
		t.Fatalf("legacy baseline (deficit absent) must still validate: %v", err)
	}
}

func TestValidateBalanceSnapshotRejectsAnInconsistentDeficit(t *testing.T) {
	cases := []struct {
		name string
		row  BalanceSnapshotRow
	}{
		{"negative with zero deficit", BalanceSnapshotRow{ExternalUserID: "12", ServiceUnits: "0", BalanceNegative: true, DeficitServiceUnits: "0"}},
		{"positive with deficit", BalanceSnapshotRow{ExternalUserID: "12", ServiceUnits: "9", DeficitServiceUnits: "4"}},
		{"negative with units", BalanceSnapshotRow{ExternalUserID: "12", ServiceUnits: "9", BalanceNegative: true, DeficitServiceUnits: "4"}},
		{"malformed deficit", BalanceSnapshotRow{ExternalUserID: "12", ServiceUnits: "0", BalanceNegative: true, DeficitServiceUnits: "x"}},
	}
	for _, tc := range cases {
		value := BalanceSnapshot{SchemaVersion: cutoverSchemaVersion,
			SourceID: "10000000-0000-4000-8000-000000000001", SourceType: SourceSub2API,
			CheckpointKind: "reconciliation", AsOf: "2026-08-21T00:00:00Z",
			CutoverAt: "2026-08-20T00:00:00Z", UnitCode: "SUB2_BALANCE_1E8", Rows: []BalanceSnapshotRow{tc.row}}
		var err error
		if value.SnapshotID, err = balanceSnapshotID(value); err != nil {
			t.Fatal(err)
		}
		if err := validateBalanceSnapshot(value); err == nil || !strings.Contains(err.Error(), "deficit") {
			t.Fatalf("%s: expected the deficit invariant to reject, got %v", tc.name, err)
		}
	}
	consistent := testBalanceSnapshot(t, "reconciliation", "2026-08-21T00:00:00Z", "", []BalanceSnapshotRow{
		{ExternalUserID: "12", ServiceUnits: "0", BalanceNegative: true, DeficitServiceUnits: "1361800"},
	})
	if consistent.Rows[0].DeficitServiceUnits != "1361800" {
		t.Fatal("consistent deficit row must validate unchanged")
	}
}

func TestBalanceCheckpointProjectionCarriesTheDeficit(t *testing.T) {
	manifest := CutoverManifest{ManifestHash: strings.Repeat("a", 64), ConfigurationHash: strings.Repeat("b", 64)}
	snapshot := testBalanceSnapshot(t, "reconciliation", "2026-08-21T00:00:00Z", "", []BalanceSnapshotRow{
		{ExternalUserID: "12", ServiceUnits: "0", BalanceNegative: true, DeficitServiceUnits: "1361800"},
	})
	observedAt := time.Date(2026, 8, 21, 0, 1, 0, 0, time.UTC)
	projection := balanceCheckpointProjection(manifest, snapshot, snapshot.Rows[0], observedAt)
	payload, ok := projection.Payload.(BalanceCheckpointPayload)
	if !ok {
		t.Fatalf("projection payload is %T, want BalanceCheckpointPayload", projection.Payload)
	}
	if !payload.BalanceNegative || payload.BalanceServiceUnits != "0" || payload.DeficitServiceUnits != "1361800" {
		t.Fatalf("payload must carry zero units, the negative flag and the deficit: %+v", payload)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"deficit_service_units":"1361800"`) {
		t.Fatalf("serialised payload lacks the deficit: %s", raw)
	}
	var decoded BalanceCheckpointPayload
	if err := decodeStrict(raw, &decoded); err != nil || decoded.DeficitServiceUnits != "1361800" {
		t.Fatalf("strict decode must accept the deficit field: %v %+v", err, decoded)
	}
	positive := balanceCheckpointProjection(manifest, snapshot, BalanceSnapshotRow{ExternalUserID: "34", ServiceUnits: "5", DeficitServiceUnits: "0"}, observedAt)
	positiveRaw, err := json.Marshal(positive.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(positiveRaw), `"deficit_service_units":"0"`) {
		t.Fatalf("a non-negative row must still declare a zero deficit explicitly: %s", positiveRaw)
	}
}
