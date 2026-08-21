package sourceagent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestV3JSONSchemaHasStrictEconomicAndPaymentBoundaries(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/source-agent-batch.v3.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	streams := properties["stream_id"].(map[string]any)["enum"].([]any)
	if len(streams) != 4 || streams[0] != "payments" || streams[1] != "usage" || streams[2] != "credits" || streams[3] != "balances" {
		t.Fatalf("V3 stream enum drifted: %#v", streams)
	}
	defs := schema["$defs"].(map[string]any)
	for _, name := range []string{"paymentOrderPayload", "paymentCandidatePayload", "paymentAdjustmentPayload", "creditPayload", "balancePayload", "cutoverManifestPayload"} {
		definition := defs[name].(map[string]any)
		if definition["additionalProperties"] != false {
			t.Fatalf("%s is not strict", name)
		}
	}
	usageDefinition := defs["usagePayload"].(map[string]any)
	usageParts := usageDefinition["allOf"].([]any)
	if usageParts[1].(map[string]any)["additionalProperties"] != false {
		t.Fatal("usagePayload is not strict")
	}
	text := string(raw)
	for _, forbidden := range []string{`"email"`, `"content"`, `"trade_no"`, `"provider_payload"`, `"ip"`, `"password"`, `"secret"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("V3 schema contains forbidden source field %s", forbidden)
		}
	}
}

func TestV3PublishedExamplePassesReceiverValidator(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/examples/source-agent-batch.v3.sub2api-usage.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewValidator("10000000-0000-4000-8000-000000000001", StreamUsage).ValidateAndCommit(raw, SHA256Hex(raw)); err != nil {
		t.Fatal(err)
	}
}

func testV3Manifest(t *testing.T) CutoverManifest {
	t.Helper()
	at := "2026-08-20T00:00:00Z"
	value := CutoverManifest{SchemaVersion: cutoverSchemaVersion, SourceID: "10000000-0000-4000-8000-000000000001",
		SourceType: SourceSub2API, SourceRuntime: "0.1.179", CutoverAt: at, DatabaseClock: at,
		ProjectionContract: "sub2api-economic-v3", ConfigurationHash: SHA256Hex([]byte("config")),
		UnitCode: "SUB2_BALANCE_1E8", SigningKeyID: "key-1", BaselineSnapshotID: SHA256Hex([]byte("snapshot")), BaselineRowCount: "0",
		HighWaters: map[string]SourceHighWater{
			StreamPayments: {EventTime: at, Cursor: "payment_orders:10"},
			StreamUsage:    {EventTime: at, Cursor: "usage_logs:20"},
			StreamCredits:  {EventTime: at, Cursor: "promo_code_usages:1;user_affiliate_ledger:2;redeem_codes:3"},
			StreamBalances: {EventTime: at, Cursor: "balance_snapshot:0"},
		}}
	var err error
	value.ManifestHash, err = cutoverManifestHash(value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func testV3Cursor(stream string, cycleSalt string) ScanCursor {
	watermark := "2026-08-21T00:01:00Z"
	ceiling := "2026-08-21T00:02:00Z"
	return ScanCursor{Version: 2, CutoverAt: "2026-08-21T00:00:00Z", WatermarkAt: watermark,
		WatermarkCursor: stream + "_published:1", CeilingAt: ceiling, CeilingCursor: stream + "_ceiling:2",
		PositionCursor: stream + "_position:1", ScanCycleID: deterministicUUID(stream + "\x00" + cycleSalt)}
}

func TestV3FactIdentityDoesNotDriftAcrossScanCycles(t *testing.T) {
	manifest := testV3Manifest(t)
	order := "21"
	payload := UsageEventPayload{ExternalUserID: "7", ExternalUsageID: "usage_logs:21", OccurredAt: "2026-08-21T00:00:30Z",
		ServiceUnits: "125000", UnitCode: manifest.UnitCode, BillingScope: "wallet",
		FactMetadata: FactMetadata{SourceCursor: "usage_logs:21", CausalDomain: "usage_logs", CausalOrder: &order,
			CutoverManifestHash: manifest.ManifestHash, ConfigurationHash: manifest.ConfigurationHash}}
	projection := Projection{EntityType: EntityUsageEvent, ExternalID: "usage_logs:21", Operation: "upsert",
		ObservedAt: "2026-08-21T00:03:00Z", Payload: payload}
	builder := BatchBuilder{SchemaVersion: SchemaVersionV3, SourceInstanceID: manifest.SourceID, StreamID: StreamUsage,
		SourceType: SourceSub2API, SourceRuntimeVersion: manifest.SourceRuntime, AgentVersion: "test", Mode: "db_projection"}
	first, err := builder.BuildPage(1, "", []Projection{projection}, testV3Cursor(StreamUsage, "one"))
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if json.Unmarshal(first.RawBody, &wire) != nil {
		t.Fatal("decode V3 wire")
	}
	if value, exists := wire["scan_complete"]; !exists || value != false {
		t.Fatal("V3 intermediate batch omitted explicit scan_complete=false")
	}
	if _, exists := wire["scan_snapshot_row_count"]; exists {
		t.Fatal("non-balances V3 batch carried snapshot metadata")
	}
	secondCursor := testV3Cursor(StreamUsage, "two")
	secondCursor.WatermarkAt = "2026-08-21T00:04:00Z"
	secondCursor.CeilingAt = "2026-08-21T00:05:00Z"
	second, err := builder.BuildPage(2, first.BodyHash, []Projection{projection}, secondCursor)
	if err != nil {
		t.Fatal(err)
	}
	if first.Batch.Records[0].EventID != second.Batch.Records[0].EventID || first.Batch.Records[0].PayloadSHA256 != second.Batch.Records[0].PayloadSHA256 {
		t.Fatal("transport watermark or scan cycle changed immutable economic fact identity")
	}
	if first.Batch.Records[0].EventID != "a9eeea97-0d98-874e-8dc5-f211fe4e1da2" || first.Batch.Records[0].PayloadSHA256 != "aad078d8fcc00734c1d6a1958d33ee61363ccba4660f95e8c20650217d736c00" {
		t.Fatalf("V3 cross-language fact vector event=%s payload=%s", first.Batch.Records[0].EventID, first.Batch.Records[0].PayloadSHA256)
	}
}

func TestV3EntityStreamMatrixAndTombstoneFailClosed(t *testing.T) {
	manifest := testV3Manifest(t)
	order := "21"
	payload := UsageEventPayload{ExternalUserID: "7", ExternalUsageID: "usage_logs:21", OccurredAt: "2026-08-21T00:00:30Z", ServiceUnits: "1", UnitCode: manifest.UnitCode, BillingScope: "wallet", FactMetadata: FactMetadata{SourceCursor: "usage_logs:21", CausalDomain: "usage_logs", CausalOrder: &order, CutoverManifestHash: manifest.ManifestHash, ConfigurationHash: manifest.ConfigurationHash}}
	projection := Projection{EntityType: EntityUsageEvent, ExternalID: "usage_logs:21", ObservedAt: "2026-08-21T00:03:00Z", Operation: "upsert", Payload: payload}
	builder := BatchBuilder{SchemaVersion: SchemaVersionV3, SourceInstanceID: manifest.SourceID, StreamID: StreamPayments, SourceType: SourceSub2API, SourceRuntimeVersion: manifest.SourceRuntime, AgentVersion: "test", Mode: "db_projection"}
	if _, err := builder.BuildPage(1, "", []Projection{projection}, testV3Cursor(StreamPayments, "wrong-stream")); err == nil {
		t.Fatal("usage fact was accepted on payments stream")
	}
	builder.StreamID = StreamUsage
	projection.Operation = "tombstone"
	projection.Payload = TombstonePayload{ExternalID: "21", Reason: "reconciliation_missing", ConfirmedAt: "2026-08-21T00:03:00Z"}
	if _, err := builder.BuildPage(1, "", []Projection{projection}, testV3Cursor(StreamUsage, "tombstone")); err == nil {
		t.Fatal("V3 economic tombstone was accepted")
	}
}

func TestV3ManifestCanonicalHashAndVerifiedKeyBinding(t *testing.T) {
	manifest := testV3Manifest(t)
	if manifest.ManifestHash != "e83c5ba06912f46f1058fd9703db7b6849f9bed9125f61509842f34919fa5ae2" {
		t.Fatalf("V3 canonical manifest vector=%s", manifest.ManifestHash)
	}
	projection := Projection{EntityType: EntityCutoverManifest, ExternalID: manifest.ManifestHash, ObservedAt: "2026-08-21T00:03:00Z", Operation: "upsert", Payload: manifest.Payload()}
	cursor := testV3Cursor(StreamBalances, "manifest")
	cursor.SnapshotID = manifest.BaselineSnapshotID
	cursor.SnapshotRowCount = 0
	cursor.HasSnapshotMetadata = true
	builder := BatchBuilder{SchemaVersion: SchemaVersionV3, SourceInstanceID: manifest.SourceID, StreamID: StreamBalances, SourceType: SourceSub2API, SourceRuntimeVersion: manifest.SourceRuntime, AgentVersion: "test", Mode: "db_projection"}
	batch, err := builder.BuildPage(1, "", []Projection{projection}, cursor)
	if err != nil {
		t.Fatal(err)
	}
	var balanceWire map[string]any
	if json.Unmarshal(batch.RawBody, &balanceWire) != nil {
		t.Fatal("decode balances wire")
	}
	if count, ok := balanceWire["scan_snapshot_row_count"].(float64); !ok || count != 0 {
		t.Fatalf("empty balance snapshot row count was not explicit JSON integer: %#v", balanceWire["scan_snapshot_row_count"])
	}
	var manifestWire CutoverManifestPayload
	if err = json.Unmarshal(batch.Batch.Records[0].Payload, &manifestWire); err != nil || manifestWire.BaselineSnapshotHash != batch.Batch.ScanSnapshotID || manifestWire.BaselineRowCount != "0" {
		t.Fatal("empty cutover manifest was not bound to the signed batch snapshot metadata")
	}
	validator := NewValidator(manifest.SourceID, StreamBalances)
	if _, err = validator.ValidateAndCommit(batch.RawBody, batch.BodyHash); err != nil {
		t.Fatal(err)
	}
	mismatchedCursor := cursor
	mismatchedCursor.SnapshotID = SHA256Hex([]byte("different-empty-snapshot"))
	if _, err = builder.BuildPage(1, "", []Projection{projection}, mismatchedCursor); err == nil {
		t.Fatal("manifest was accepted with a different signed batch snapshot id")
	}
	tampered := manifest.Payload()
	tampered.BaselineRowCount = "1"
	projection.Payload = tampered
	tamperedBatch, err := builder.BuildPage(1, "", []Projection{projection}, cursor)
	if err == nil {
		if _, err = NewValidator(manifest.SourceID, StreamBalances).ValidateAndCommit(tamperedBatch.RawBody, tamperedBatch.BodyHash); err == nil {
			t.Fatal("tampered manifest retained an old canonical hash")
		}
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := NewAtomicSigningKeyProvider("different-key", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := SignBatch(context.Background(), batch, keys, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resolver := StaticPublicKeySet{manifest.SourceID: {StreamBalances: {"different-key": publicKey}}}
	if _, err = NewValidator(manifest.SourceID, StreamBalances).ValidateSigned(batch.RawBody, metadata, resolver, time.Now(), time.Minute); err == nil {
		t.Fatal("manifest signing_key_id differed from verified header key")
	}
}

func TestEncryptedCutoverStateIsAuthenticatedAndCreateOnly(t *testing.T) {
	directory := t.TempDir()
	keyPath := filepath.Join(directory, "key")
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	store := EncryptedStateFile{Path: filepath.Join(directory, "manifest.enc"), Purpose: "cutover_manifest", Keys: FileSpoolKeyProvider{Path: keyPath}}
	manifest := testV3Manifest(t)
	if err := store.SaveNew(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNew(context.Background(), manifest); err == nil {
		t.Fatal("cutover manifest was overwritten")
	}
	loaded, err := LoadCutoverManifest(context.Background(), store, manifest.SourceID, manifest.SourceType, manifest.SourceRuntime)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ManifestHash != manifest.ManifestHash {
		t.Fatal("encrypted manifest changed")
	}
	raw, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if json.Unmarshal(raw, &envelope) != nil || envelope["ciphertext"] == nil {
		t.Fatal("cutover state is not an encrypted envelope")
	}
}
