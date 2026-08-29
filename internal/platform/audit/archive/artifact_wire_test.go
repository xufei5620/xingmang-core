package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestArtifactWireLiteralBytesAndSHA256AreStable(t *testing.T) {
	manifest, err := os.ReadFile("testdata/artifact-wire-v1.sha256")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		parts := strings.SplitN(line, "  ", 2)
		if len(parts) != 2 {
			t.Fatalf("bad digest line %q", line)
		}
		name := parts[1]
		literal, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(literal)
		if got := hex.EncodeToString(sum[:]); got != parts[0] {
			t.Fatalf("%s hash = %s, want %s", name, got, parts[0])
		}

		var encoded []byte
		switch name {
		case "chain-root-ref-v1.json":
			value, err := DecodeChainRootRefV1(literal)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err = EncodeChainRootRefV1(value)
			if err != nil {
				t.Fatal(err)
			}
		case "manifest-v1.json":
			value, err := DecodeSignedManifestV1(literal)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err = EncodeSignedManifestV1(value)
			if err != nil {
				t.Fatal(err)
			}
		case "checkpoint-v1.json":
			value, err := DecodeSignedCheckpointV1(literal)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err = EncodeSignedCheckpointV1(value)
			if err != nil {
				t.Fatal(err)
			}
		case "recovery-index-v1.json":
			value, err := DecodeSignedRecoveryIndexV1(literal)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err = EncodeSignedRecoveryIndexV1(value)
			if err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unhandled golden %q", name)
		}
		if !bytes.Equal(encoded, literal) {
			t.Fatalf("%s re-encoding changed frozen bytes\nwant=%s\n got=%s", name, literal, encoded)
		}
	}
}

func TestArtifactWireRejectsUnknownDuplicateMissingNullMisorderedAndBadTime(t *testing.T) {
	manifest, err := os.ReadFile("testdata/manifest-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		raw    []byte
		decode func([]byte) error
	}{
		"root-unknown": {
			raw:    mustReadTestdata(t, "root-unknown-field.json"),
			decode: func(raw []byte) error { _, err := DecodeChainRootRefV1(raw); return err },
		},
		"root-duplicate": {
			raw:    mustReadTestdata(t, "root-duplicate-field.json"),
			decode: func(raw []byte) error { _, err := DecodeChainRootRefV1(raw); return err },
		},
		"root-missing": {
			raw:    mustReadTestdata(t, "root-missing-field.json"),
			decode: func(raw []byte) error { _, err := DecodeChainRootRefV1(raw); return err },
		},
		"root-null": {
			raw:    mustReadTestdata(t, "root-null-field.json"),
			decode: func(raw []byte) error { _, err := DecodeChainRootRefV1(raw); return err },
		},
		"root-misordered": {
			raw:    mustReadTestdata(t, "root-misordered-field.json"),
			decode: func(raw []byte) error { _, err := DecodeChainRootRefV1(raw); return err },
		},
		"manifest-bad-time": {
			raw:    []byte(strings.Replace(string(manifest), `"created_at":"2026-08-28T10:06:00.000000Z"`, `"created_at":"2026-08-28T10:06:00Z"`, 1)),
			decode: func(raw []byte) error { _, err := DecodeSignedManifestV1(raw); return err },
		},
		"manifest-unknown-top-level": {
			raw:    []byte(strings.Replace(string(manifest), `,"signature":"0iRD`, `,"unexpected":true,"signature":"0iRD`, 1)),
			decode: func(raw []byte) error { _, err := DecodeSignedManifestV1(raw); return err },
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if err := test.decode(test.raw); err == nil {
				t.Fatal("non-canonical artifact wire was accepted")
			}
		})
	}
}

func mustReadTestdata(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestChainRootRefWireExcludesPublicKeyExportedAtAndExportTarget(t *testing.T) {
	raw, err := os.ReadFile("testdata/chain-root-ref-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"public_key", "signed_payload", "exported_at", "export_target", "unsigned"} {
		if _, found := fields[forbidden]; found {
			t.Fatalf("ChainRootRefV1 contains forbidden self-authentication/status field %q", forbidden)
		}
	}
	if len(fields) != 7 {
		t.Fatalf("ChainRootRefV1 keys = %v", fields)
	}
}

func TestFirstManifestRequiresExactGenesisRef(t *testing.T) {
	raw, err := os.ReadFile("testdata/manifest-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodeSignedManifestV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateManifestStructure(manifest.Unsigned); err != nil {
		t.Fatal(err)
	}
	badBoundary := manifest.Unsigned
	badBoundary.FirstPrevHash = strings.Repeat("f", 64)
	if err := ValidateManifestStructure(badBoundary); err == nil {
		t.Fatal("first manifest accepted a non-Genesis event predecessor")
	}
	for name, mutate := range map[string]func(*ManifestV1){
		"kind":    func(value *ManifestV1) { value.PreviousManifest.Kind = "object" },
		"bucket":  func(value *ManifestV1) { value.PreviousManifest.BucketID = "archive-fixture" },
		"key":     func(value *ManifestV1) { value.PreviousManifest.Key = "manifest.json" },
		"version": func(value *ManifestV1) { value.PreviousManifest.VersionID = "v1" },
		"hash":    func(value *ManifestV1) { value.PreviousManifest.SHA256 = strings.Repeat("0", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := manifest.Unsigned
			mutate(&bad)
			if err := ValidateManifestStructure(bad); err == nil {
				t.Fatal("invalid first-manifest predecessor was accepted")
			}
		})
	}
}

func TestLaterManifestRequiresPreviousBucketKeyVersionAndSHA(t *testing.T) {
	raw, err := os.ReadFile("testdata/manifest-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodeSignedManifestV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	later := manifest.Unsigned
	later.FromSequence = 3
	later.ToSequence = 4
	const oldRange = "seq-0000000000000000001-0000000000000000002-"
	const newRange = "seq-0000000000000000003-0000000000000000004-"
	later.Payload.Key = strings.Replace(later.Payload.Key, oldRange, newRange, 1)
	later.Projections = append([]ProjectionRefV1(nil), later.Projections...)
	for index := range later.Projections {
		later.Projections[index].Object.Key = strings.Replace(
			later.Projections[index].Object.Key, oldRange, newRange, 1)
	}
	later.PreviousManifest = PreviousManifestRefV1{
		Kind: "object", BucketID: "archive-fixture",
		Key:       "audit/v1/manifest/seq-0000000000000000001-0000000000000000002-" + strings.Repeat("d", 64) + ".json",
		VersionID: "manifest-v1", SHA256: strings.Repeat("d", 64),
	}
	if err := ValidateManifestStructure(later); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*PreviousManifestRefV1){
		"bucket":  func(value *PreviousManifestRefV1) { value.BucketID = "" },
		"key":     func(value *PreviousManifestRefV1) { value.Key = "" },
		"version": func(value *PreviousManifestRefV1) { value.VersionID = "" },
		"hash":    func(value *PreviousManifestRefV1) { value.SHA256 = "" },
		"ads":     func(value *PreviousManifestRefV1) { value.Key = "audit/v1/manifest/file:stream" },
		"not-content-addressed": func(value *PreviousManifestRefV1) {
			value.Key = "audit/v1/manifest/previous.json"
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := later
			mutate(&bad.PreviousManifest)
			if err := ValidateManifestStructure(bad); err == nil {
				t.Fatal("hash-only/latest/incomplete predecessor was accepted")
			}
		})
	}
}

func TestManifestRejectsProjectionEnvironmentTraversalAndUnknown(t *testing.T) {
	manifest := goldenManifest(t).Unsigned
	for name, mutate := range map[string]func(*ManifestV1){
		"traversal": func(value *ManifestV1) { value.Projections[0].Environment = "../production" },
		"unknown":   func(value *ManifestV1) { value.Projections[1].Environment = "unknown" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := manifest
			bad.Projections = append([]ProjectionRefV1(nil), manifest.Projections...)
			mutate(&bad)
			if err := ValidateManifestStructure(bad); err == nil {
				t.Fatal("invalid environment entered signed manifest wire")
			}
		})
	}
}

func TestArtifactObjectKeyRejectsTraversalAndAbsolutePaths(t *testing.T) {
	manifest := goldenManifest(t).Unsigned
	for _, key := range []string{
		"../payload.ndjson", "/absolute/payload.ndjson", `audit\\v1\\payload.ndjson`,
		"audit/v1/../payload.ndjson", "audit/v1/payload/file:stream",
	} {
		t.Run(key, func(t *testing.T) {
			bad := manifest
			bad.Payload.Key = key
			if err := ValidateManifestStructure(bad); err == nil {
				t.Fatal("unsafe object key entered signed artifact wire")
			}
		})
	}
}

func TestCheckpointRejectsPreviousObjectTraversal(t *testing.T) {
	raw, err := os.ReadFile("testdata/checkpoint-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := DecodeSignedCheckpointV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]string{
		"traversal":             "audit/v1/../checkpoint.json",
		"not-content-addressed": "audit/v1/checkpoint/previous.json",
	} {
		t.Run(name, func(t *testing.T) {
			value := checkpoint.Unsigned
			value.Generation = 2
			value.PreviousCheckpoint = PreviousCheckpointRefV1{
				Kind: "object", BucketID: "archive-fixture", Key: key,
				VersionID: "checkpoint-v1", SHA256: strings.Repeat("a", 64),
			}
			if err := validateCheckpoint(value); err == nil {
				t.Fatal("unsafe previous checkpoint key entered signed artifact wire")
			}
		})
	}
}

func TestManifestRequiresContentAddressedPayloadAndEnvironmentProjectionKeys(t *testing.T) {
	manifest := goldenManifest(t).Unsigned
	for name, mutate := range map[string]func(*ManifestV1){
		"payload-prefix": func(value *ManifestV1) {
			value.Payload.Key = strings.Replace(value.Payload.Key, "audit/v1/payload/", "audit/v1/projection/staging/", 1)
		},
		"payload-digest": func(value *ManifestV1) {
			value.Payload.Key = strings.Replace(value.Payload.Key, value.Payload.SHA256, strings.Repeat("f", 64), 1)
		},
		"projection-environment": func(value *ManifestV1) {
			value.Projections[1].Object.Key = strings.Replace(value.Projections[1].Object.Key,
				"audit/v1/projection/staging/", "audit/v1/projection/production/", 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := manifest
			bad.Projections = append([]ProjectionRefV1(nil), manifest.Projections...)
			mutate(&bad)
			if err := ValidateManifestStructure(bad); err == nil {
				t.Fatal("non-content-addressed or mislabeled object key was accepted")
			}
		})
	}
}

func TestCheckpointAndRecoveryRequireContentAddressedArtifactRefs(t *testing.T) {
	checkpointRaw, err := os.ReadFile("testdata/checkpoint-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := DecodeSignedCheckpointV1(checkpointRaw)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.Unsigned.FirstManifest.Key = "audit/v1/manifest/first.json"
	if err := validateCheckpoint(checkpoint.Unsigned); err == nil {
		t.Fatal("Checkpoint accepted non-content-addressed first manifest ref")
	}

	recoveryRaw, err := os.ReadFile("testdata/recovery-index-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := DecodeSignedRecoveryIndexV1(recoveryRaw)
	if err != nil {
		t.Fatal(err)
	}
	recovery.Unsigned.Checkpoint.Key = "audit/v1/checkpoint/current.json"
	if err := validateRecoveryIndex(recovery.Unsigned); err == nil {
		t.Fatal("RecoveryIndex accepted non-content-addressed checkpoint ref")
	}
}

func TestCheckpointBindsItsTerminalSequence(t *testing.T) {
	raw, err := os.ReadFile("testdata/checkpoint-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := DecodeSignedCheckpointV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	bad := checkpoint.Unsigned
	bad.ChainRoot.ToSequence = bad.ToSequence - 1
	if err := validateCheckpoint(bad); err == nil {
		t.Fatal("Checkpoint accepted a terminal sequence mismatch")
	}
}

func TestCheckpointAndRecoveryRejectLocalFixtureRefs(t *testing.T) {
	checkpointRaw, err := os.ReadFile("testdata/checkpoint-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := DecodeSignedCheckpointV1(checkpointRaw)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.Unsigned.FirstManifest.BucketID = "local-fixture"
	if err := validateCheckpoint(checkpoint.Unsigned); err == nil {
		t.Fatal("Checkpoint accepted local-only fixture evidence")
	}

	recoveryRaw, err := os.ReadFile("testdata/recovery-index-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := DecodeSignedRecoveryIndexV1(recoveryRaw)
	if err != nil {
		t.Fatal(err)
	}
	recovery.Unsigned.Checkpoint.BucketID = "local-fixture"
	if err := validateRecoveryIndex(recovery.Unsigned); err == nil {
		t.Fatal("RecoveryIndex accepted local-only fixture evidence")
	}
}
