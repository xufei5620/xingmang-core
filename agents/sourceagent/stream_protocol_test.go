package sourceagent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileSigningKeyProviderLoadsPrivatePKCS8WithoutExposingIt(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "signing.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := (FileSigningKeyProvider{KeyID: "key-1", PrivateKeyFile: path}).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	message := []byte("signing-provider-self-test")
	signature := ed25519.Sign(snapshot.privateKey, message)
	snapshot.Destroy()
	if !ed25519.Verify(publicKey, message, signature) {
		t.Fatal("file signing key provider returned corrupted key material")
	}
}

func TestVersion2RejectsNonProductionEnvelopeMetadata(t *testing.T) {
	base := BatchBuilder{SchemaVersion: SchemaVersionV2,
		SourceInstanceID: "10000000-0000-4000-8000-000000000002", StreamID: "payments",
		SourceType: SourceNewAPI, SourceRuntimeVersion: "v1", AgentVersion: "test",
		Mode: "db_projection", ProjectionStatus: "healthy"}
	for name, mutate := range map[string]func(*BatchBuilder){
		"slug source":    func(value *BatchBuilder) { value.SourceInstanceID = "newapi-main" },
		"desktop":        func(value *BatchBuilder) { value.SourceType = SourceDesktop },
		"custom stream":  func(value *BatchBuilder) { value.StreamID = "custom" },
		"missing health": func(value *BatchBuilder) { value.ProjectionStatus = "missing" },
	} {
		builder := base
		mutate(&builder)
		if _, err := builder.Build(1, "", nil); err == nil {
			t.Fatalf("v2 builder accepted %s", name)
		}
	}
	batch, err := base.Build(1, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err = json.Unmarshal(batch.RawBody, &object); err != nil {
		t.Fatal(err)
	}
	delete(object, "projection_status")
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeBatch(raw); err == nil {
		t.Fatal("v2 validator accepted missing projection_status")
	}
}

func TestVersion2SchemaRequiresStreamAndVersion1DoesNot(t *testing.T) {
	readSchema := func(path string) map[string]any {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		return schema
	}
	v2 := readSchema("../../contracts/source-agent-batch.v2.schema.json")
	required, ok := v2["required"].([]any)
	if !ok || !containsJSONText(required, "stream_id") {
		t.Fatal("v2 JSON Schema does not require stream_id")
	}
	if !containsJSONText(required, "projection_status") {
		t.Fatal("v2 JSON Schema does not require projection_status")
	}
	properties, _ := v2["properties"].(map[string]any)
	if _, ok := properties["stream_id"]; !ok {
		t.Fatal("v2 JSON Schema has no stream_id property")
	}
	definitions, _ := v2["$defs"].(map[string]any)
	streamDefinition, _ := definitions["streamId"].(map[string]any)
	streamEnum, _ := streamDefinition["enum"].([]any)
	if !containsJSONText(streamEnum, "payments") || !containsJSONText(streamEnum, "identities") || len(streamEnum) != 2 {
		t.Fatalf("v2 stream enum drifted: %#v", streamEnum)
	}
	sourceDefinition, _ := definitions["sourceId"].(map[string]any)
	if sourceDefinition["pattern"] != `^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$` {
		t.Fatalf("v2 source UUID pattern drifted: %#v", sourceDefinition["pattern"])
	}
	projectionDefinition, _ := properties["projection_status"].(map[string]any)
	projectionEnum, _ := projectionDefinition["enum"].([]any)
	if !containsJSONText(projectionEnum, "healthy") || !containsJSONText(projectionEnum, "blocked") || len(projectionEnum) != 2 {
		t.Fatalf("v2 projection status enum drifted: %#v", projectionEnum)
	}
	v1 := readSchema("../../contracts/source-agent-batch.v1.schema.json")
	v1Required, _ := v1["required"].([]any)
	v1Properties, _ := v1["properties"].(map[string]any)
	if containsJSONText(v1Required, "stream_id") || v1Properties["stream_id"] != nil {
		t.Fatal("legacy v1 JSON Schema was changed to contain stream_id")
	}
}

func containsJSONText(values []any, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func TestVersion2ContractExampleValidates(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/examples/source-agent-batch.v2.newapi.json")
	if err != nil {
		t.Fatal(err)
	}
	validated, err := NewValidator("10000000-0000-4000-8000-000000000002", "payments").ValidateAndCommit(raw, SHA256Hex(raw))
	if err != nil {
		t.Fatal(err)
	}
	if validated.Batch.StreamID != "payments" || len(validated.Batch.Records) != 1 {
		t.Fatalf("unexpected v2 example: %#v", validated.Batch)
	}
}

func TestSchemaV2RequiresStreamAndV1RemainsStreamless(t *testing.T) {
	base := BatchBuilder{
		SourceInstanceID: "10000000-0000-4000-8000-000000000002", SourceType: SourceNewAPI,
		SourceRuntimeVersion: "v1.0.0-rc.25", AgentVersion: "test", Mode: "db_projection",
	}
	if _, err := base.Build(1, "", []Projection{candidateProjectionForTest()}); err == nil {
		t.Fatal("schema 2.0 builder accepted a missing stream")
	}
	base.SchemaVersion = SchemaVersionV1
	base.Mode = "mock"
	if _, err := base.Build(1, "", nil); err != nil {
		t.Fatalf("legacy schema 1.0 streamless mock was rejected: %v", err)
	}
	base.StreamID = "payments"
	if _, err := base.Build(1, "", nil); err == nil {
		t.Fatal("schema 1.0 accepted a v2 stream field")
	}
}

func TestValidatorRejectsV2CrossStreamAndMissingExpectedStream(t *testing.T) {
	batch, err := (BatchBuilder{
		SchemaVersion: SchemaVersionV2, SourceInstanceID: "10000000-0000-4000-8000-000000000002",
		StreamID: "payments", SourceType: SourceNewAPI,
		SourceRuntimeVersion: "v1.0.0-rc.25", AgentVersion: "test", Mode: "db_projection",
	}).Build(1, "", []Projection{candidateProjectionForTest()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewValidator("10000000-0000-4000-8000-000000000002", "identities").ValidateAndCommit(batch.RawBody, batch.BodyHash); !errors.Is(err, ErrSourceMismatch) {
		t.Fatalf("expected cross-stream rejection, got %v", err)
	}
	if _, err := NewValidator("10000000-0000-4000-8000-000000000002").ValidateAndCommit(batch.RawBody, batch.BodyHash); !errors.Is(err, ErrSourceMismatch) {
		t.Fatalf("v2 validator without expected stream did not fail closed: %v", err)
	}
}

func TestStreamIsSignedAndCannotBeSubstituted(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := NewAtomicSigningKeyProvider("key-1", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := (BatchBuilder{
		SourceInstanceID: "10000000-0000-4000-8000-000000000002", StreamID: "payments", SourceType: SourceNewAPI,
		SourceRuntimeVersion: "v1.0.0-rc.25", AgentVersion: "test", Mode: "db_projection",
	}).Build(1, "", []Projection{candidateProjectionForTest()})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	metadata, err := SignBatch(context.Background(), batch, keys, now)
	if err != nil {
		t.Fatal(err)
	}
	resolver := StaticPublicKeySet{"10000000-0000-4000-8000-000000000002": {"payments": {"key-1": publicKey}}}
	if err := VerifyBatchSignature(batch.RawBody, metadata, resolver, now, time.Minute); err != nil {
		t.Fatal(err)
	}
	wrongStreamResolver := StaticPublicKeySet{"10000000-0000-4000-8000-000000000002": {"identities": {"key-1": publicKey}}}
	if err := VerifyBatchSignature(batch.RawBody, metadata, wrongStreamResolver, now, time.Minute); err == nil {
		t.Fatal("payments signature key was resolved from another stream keyset")
	}
	metadata.StreamID = "identities"
	if err := VerifyBatchSignature(batch.RawBody, metadata, resolver, now, time.Minute); err == nil {
		t.Fatal("signature verified after stream substitution")
	}
}

func TestVersion2SignatureCanonicalOrderIsPinned(t *testing.T) {
	metadata := SignatureMetadata{
		SourceID: "10000000-0000-4000-8000-000000000001", StreamID: "payments",
		BatchID: "018f4ec7-08d0-7b72-a2d4-1df742eec6d0", Sequence: 42,
		SentAt:     "2026-08-21T00:00:00Z",
		BodySHA256: strings.Repeat("a", 64),
	}
	expected := strings.Join([]string{
		"POST", "/internal/v1/source-batches", "10000000-0000-4000-8000-000000000001", "payments",
		"018f4ec7-08d0-7b72-a2d4-1df742eec6d0", "42",
		"2026-08-21T00:00:00Z", strings.Repeat("a", 64),
	}, "\n")
	if actual := string(signatureInput(metadata)); actual != expected {
		t.Fatalf("v2 canonical signature input drifted:\n%s", actual)
	}
}

func TestRetryAfterIsBoundedWithoutDurationOverflow(t *testing.T) {
	now := time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)
	if value := boundedRetryAfter("9223372036854775807", now); value != time.Hour {
		t.Fatalf("huge Retry-After = %s", value)
	}
	if value := boundedRetryAfter("-1", now); value != 0 {
		t.Fatalf("negative Retry-After = %s", value)
	}
}

func TestStreamDoesNotChangeDeterministicEventID(t *testing.T) {
	build := func(stream string) ValidatedBatch {
		batch, err := (BatchBuilder{
			SourceInstanceID: "10000000-0000-4000-8000-000000000002", StreamID: stream, SourceType: SourceNewAPI,
			SourceRuntimeVersion: "v1.0.0-rc.25", AgentVersion: "test", Mode: "db_projection",
		}).Build(1, "", []Projection{candidateProjectionForTest()})
		if err != nil {
			t.Fatal(err)
		}
		return batch
	}
	payments := build("payments")
	if _, err := (BatchBuilder{
		SourceInstanceID: "10000000-0000-4000-8000-000000000002", StreamID: "payments.reconcile", SourceType: SourceNewAPI,
		SourceRuntimeVersion: "v1.0.0-rc.25", AgentVersion: "test", Mode: "db_projection",
	}).Build(1, "", []Projection{candidateProjectionForTest()}); err == nil {
		t.Fatal("v2 accepted a non-production transport stream")
	}
	if payments.Batch.Records[0].EventID != "daa4072a-7b25-8506-94ea-2c52e6dd522d" {
		t.Fatalf("established deterministic event identity drifted: %s", payments.Batch.Records[0].EventID)
	}
	production, err := (BatchBuilder{
		SourceInstanceID: "10000000-0000-4000-8000-000000000002",
		StreamID:         "payments", SourceType: SourceNewAPI,
		SourceRuntimeVersion: "v1.0.0-rc.25", AgentVersion: "test", Mode: "db_projection",
	}).Build(1, "", []Projection{candidateProjectionForTest()})
	if err != nil {
		t.Fatal(err)
	}
	if production.Batch.Records[0].EventID != "daa4072a-7b25-8506-94ea-2c52e6dd522d" {
		t.Fatalf("production UUID event identity drifted: %s", production.Batch.Records[0].EventID)
	}
}

func TestPublisherRejectsAckFromAnotherStream(t *testing.T) {
	store := &MemorySequenceStore{}
	publisher := &Publisher{
		Builder: BatchBuilder{
			SourceInstanceID: "10000000-0000-4000-8000-000000000002", StreamID: "payments", SourceType: SourceNewAPI,
			SourceRuntimeVersion: "v1.0.0-rc.25", AgentVersion: "test", Mode: "db_projection",
		},
		Store: store,
		Client: ingestFunc(func(_ context.Context, batch ValidatedBatch) (IngestAck, error) {
			return IngestAck{
				Accepted: true, SourceInstanceID: batch.Batch.SourceInstanceID,
				StreamID: "identities", BatchID: batch.Batch.BatchID,
				Sequence: batch.Batch.Sequence, AcceptedRecords: len(batch.Batch.Records),
			}, nil
		}),
	}
	if _, err := publisher.Publish(context.Background(), []Projection{candidateProjectionForTest()}); err == nil {
		t.Fatal("publisher accepted an acknowledgement from another stream")
	}
	state, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Sequence != 0 {
		t.Fatal("cross-stream acknowledgement advanced local sequence")
	}
}
