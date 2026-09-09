package archive

import (
	"os"
	"strings"
	"testing"
)

func goldenCheckpoint(t *testing.T) SignedCheckpointV1 {
	t.Helper()
	raw, err := os.ReadFile("testdata/checkpoint-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := DecodeSignedCheckpointV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestAUD2CheckpointDigestBindsExactSignedBytes(t *testing.T) {
	checkpoint := goldenCheckpoint(t)
	digest, err := SignedCheckpointDigest(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeSignedCheckpointV1(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if digest != sha256Hex(encoded) {
		t.Fatalf("checkpoint digest=%s want exact wire digest=%s", digest, sha256Hex(encoded))
	}
	checkpoint.Signature = strings.Repeat("A", len(checkpoint.Signature))
	if err := ValidateCheckpointBinding(checkpoint); err == nil {
		t.Fatal("changed signed checkpoint unexpectedly accepted")
	}
}

func TestAUD2CheckpointRejectsCatalogDigestWithoutGeneration(t *testing.T) {
	checkpoint := goldenCheckpoint(t)
	checkpoint.Unsigned.Generation = 0
	if err := ValidateCheckpointBinding(checkpoint); err == nil {
		t.Fatal("zero-generation checkpoint unexpectedly accepted")
	}
}
