package sourceagent

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVersion2DetachedSignatureInteropVector(t *testing.T) {
	type vector struct {
		BatchFile      string `json:"batch_file"`
		Method         string `json:"method"`
		Path           string `json:"path"`
		SourceID       string `json:"source_id"`
		StreamID       string `json:"stream_id"`
		BatchID        string `json:"batch_id"`
		Sequence       uint64 `json:"sequence"`
		SentAt         string `json:"sent_at"`
		BodySHA256     string `json:"body_sha256"`
		KeyID          string `json:"key_id"`
		PublicKey      string `json:"public_key_raw_base64"`
		Signature      string `json:"signature_base64"`
		CanonicalInput string `json:"canonical_input"`
	}
	directory := "../../contracts/examples"
	rawVector, err := os.ReadFile(filepath.Join(directory, "source-agent-signature.v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture vector
	if err := json.Unmarshal(rawVector, &fixture); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(directory, fixture.BatchFile))
	if err != nil {
		t.Fatal(err)
	}
	if SHA256Hex(body) != fixture.BodySHA256 {
		t.Fatal("signature vector body hash drifted")
	}
	publicKey, err := base64.StdEncoding.Strict().DecodeString(fixture.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		t.Fatal("signature vector public key is invalid")
	}
	metadata := SignatureMetadata{
		SourceID: fixture.SourceID, StreamID: fixture.StreamID,
		BatchID: fixture.BatchID, Sequence: fixture.Sequence, SentAt: fixture.SentAt,
		BodySHA256: fixture.BodySHA256, KeyID: fixture.KeyID, Signature: fixture.Signature,
	}
	if string(signatureInput(metadata)) != fixture.CanonicalInput {
		t.Fatal("signature vector canonical input drifted")
	}
	sentAt, err := time.Parse(time.RFC3339Nano, fixture.SentAt)
	if err != nil {
		t.Fatal(err)
	}
	keys := StaticPublicKeySet{
		fixture.SourceID: {fixture.StreamID: {fixture.KeyID: ed25519.PublicKey(publicKey)}},
	}
	if err := VerifyBatchSignature(body, metadata, keys, sentAt, time.Minute); err != nil {
		t.Fatalf("verify published v2 signature vector: %v", err)
	}
}
