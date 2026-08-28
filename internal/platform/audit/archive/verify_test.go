package archive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
)

func goldenManifest(t *testing.T) SignedManifestV1 {
	t.Helper()
	raw, err := os.ReadFile("testdata/manifest-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := DecodeSignedManifestV1(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func goldenKeyrings(t *testing.T) (Keyring, Keyring) {
	t.Helper()
	rootRaw, err := os.ReadFile("testdata/root-keyring.json")
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := os.ReadFile("testdata/manifest-keyring.json")
	if err != nil {
		t.Fatal(err)
	}
	root, err := LoadStaticKeyringJSON(rootRaw)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadStaticKeyringJSON(manifestRaw)
	if err != nil {
		t.Fatal(err)
	}
	return root, manifest
}

func goldenReaders(t *testing.T) (io.Reader, map[string]io.Reader) {
	t.Helper()
	payload, err := os.ReadFile("testdata/mixed-v1-v2.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	production, err := os.ReadFile("testdata/projection-production.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	staging, err := os.ReadFile("testdata/projection-staging.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(payload), map[string]io.Reader{
		"production": bytes.NewReader(production), "staging": bytes.NewReader(staging),
	}
}

func signGoldenManifest(t *testing.T, manifest ManifestV1) SignedManifestV1 {
	t.Helper()
	signer, err := NewEd25519PurposeSigner("manifest-key-1", repeatedSeed(0x22),
		PurposeManifest, ProtocolManifestV1)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := SignManifest(manifest, signer)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func objectEvidence(raw []byte, current ObjectVersionV1) ObjectVersionV1 {
	sum := sha256.Sum256(raw)
	previousSHA := current.SHA256
	current.SHA256 = hex.EncodeToString(sum[:])
	current.Key = strings.Replace(current.Key, previousSHA, current.SHA256, 1)
	current.ProviderChecksum = "sha256:" + current.SHA256
	current.SizeBytes = int64(len(raw))
	return current
}

func TestVerifyMixedCanonicalVersions(t *testing.T) {
	payload, projections := goldenReaders(t)
	rootKeys, manifestKeys := goldenKeyrings(t)
	report, err := VerifySegment(context.Background(), goldenManifest(t), payload, projections,
		rootKeys, manifestKeys, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Code != VerificationOK || report.VerifiedRows != 2 || report.VerifiedObjects != 3 ||
		report.VerifiedRootHash != "aeaa30852897113afdec0bd8266320b65f5c68582efdbf9e2271c0839cd764a7" ||
		report.ChainIntegrity != "verified" || report.CaptureCompleteness != "gaps_found" ||
		!reflect.DeepEqual(report.Caveats, []string{"legacy_canonical_v1_non_injective"}) {
		t.Fatalf("report = %+v", report)
	}
}

func TestVerifyAllowsNonTerminalSegmentBoundToLaterTrustedRoot(t *testing.T) {
	manifest := goldenManifest(t).Unsigned
	manifest.ChainRoot.ToSequence = 4
	manifest.ChainRoot.RootHash = strings.Repeat("f", 64)
	computedAt, err := ParseWireTime(manifest.ChainRoot.ComputedAt)
	if err != nil {
		t.Fatal(err)
	}
	rootSigner, err := audit.NewEd25519Signer("root-key-1", repeatedSeed(0x11))
	if err != nil {
		t.Fatal(err)
	}
	root := audit.ChainRoot{
		ID: mustParseUUID(t, manifest.ChainRoot.ID), ComputedAt: computedAt,
		FromSequence: manifest.ChainRoot.FromSequence, ToSequence: manifest.ChainRoot.ToSequence,
		RootHash: manifest.ChainRoot.RootHash, KeyID: manifest.ChainRoot.KeyID,
	}
	manifest.ChainRoot.Signature = base64.StdEncoding.EncodeToString(rootSigner.Sign(root.SigningPayload()))
	signed := signGoldenManifest(t, manifest)
	payload, projections := goldenReaders(t)
	rootKeys, manifestKeys := goldenKeyrings(t)
	report, err := VerifySegment(context.Background(), signed, payload, projections,
		rootKeys, manifestKeys, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Code != VerificationOK {
		t.Fatalf("non-terminal segment report = %+v", report)
	}
}

func mustParseUUID(t *testing.T, raw string) uuid.UUID {
	t.Helper()
	value, err := uuid.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSignedCheckpointAndRecoveryGoldenVerifyWithIndependentKeys(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2037, 1, 1, 0, 0, 0, 0, time.UTC)
	checkpointKeys, err := NewStaticKeyring([]TrustedKey{publicRecord(
		"checkpoint-key-1", repeatedSeed(0x33), PurposeCheckpoint, ProtocolCheckpointV1, from, until)})
	if err != nil {
		t.Fatal(err)
	}
	recoveryKeys, err := NewStaticKeyring([]TrustedKey{publicRecord(
		"recovery-key-1", repeatedSeed(0x44), PurposeRecoveryIndex, ProtocolRecoveryIndexV1, from, until)})
	if err != nil {
		t.Fatal(err)
	}
	checkpointRaw, err := os.ReadFile("testdata/checkpoint-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := DecodeSignedCheckpointV1(checkpointRaw)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyCheckpointSignature(checkpoint, checkpointKeys); err != nil {
		t.Fatal(err)
	}
	recoveryRaw, err := os.ReadFile("testdata/recovery-index-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := DecodeSignedRecoveryIndexV1(recoveryRaw)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyRecoveryIndexSignature(recovery, recoveryKeys); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCheckpointSignature(checkpoint, recoveryKeys); err == nil {
		t.Fatal("RecoveryIndex key verified a Checkpoint signature")
	}
	if err := VerifyRecoveryIndexSignature(recovery, checkpointKeys); err == nil {
		t.Fatal("Checkpoint key verified a RecoveryIndex signature")
	}
}

func TestSignatureVerificationBindsCurrentUnsignedBytes(t *testing.T) {
	rootKeys, manifestKeys := goldenKeyrings(t)
	_ = rootKeys
	manifest := goldenManifest(t)
	manifest.Unsigned.ExporterVersion = "tampered"
	if err := VerifyManifestSignature(manifest, manifestKeys); err == nil {
		t.Fatal("manifest signature accepted mutated unsigned bytes")
	}

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := time.Date(2037, 1, 1, 0, 0, 0, 0, time.UTC)
	checkpointKeys, err := NewStaticKeyring([]TrustedKey{publicRecord(
		"checkpoint-key-1", repeatedSeed(0x33), PurposeCheckpoint, ProtocolCheckpointV1, from, until)})
	if err != nil {
		t.Fatal(err)
	}
	checkpointRaw, err := os.ReadFile("testdata/checkpoint-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := DecodeSignedCheckpointV1(checkpointRaw)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.Unsigned.CatalogRowsDigest = strings.Repeat("d", 64)
	if err := VerifyCheckpointSignature(checkpoint, checkpointKeys); err == nil {
		t.Fatal("checkpoint signature accepted mutated unsigned bytes")
	}

	recoveryKeys, err := NewStaticKeyring([]TrustedKey{publicRecord(
		"recovery-key-1", repeatedSeed(0x44), PurposeRecoveryIndex, ProtocolRecoveryIndexV1, from, until)})
	if err != nil {
		t.Fatal(err)
	}
	recoveryRaw, err := os.ReadFile("testdata/recovery-index-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	recovery, err := DecodeSignedRecoveryIndexV1(recoveryRaw)
	if err != nil {
		t.Fatal(err)
	}
	recovery.Unsigned.TerminalRootHash = strings.Repeat("e", 64)
	if err := VerifyRecoveryIndexSignature(recovery, recoveryKeys); err == nil {
		t.Fatal("RecoveryIndex signature accepted mutated unsigned bytes")
	}
}

func TestVerifyRejectsUnknownCanonicalVersionAsVerifierOutdated(t *testing.T) {
	raw, err := os.ReadFile("testdata/mixed-v1-v2.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), `"canonical_version":1`, `"canonical_version":3`, 1))
	manifest := goldenManifest(t).Unsigned
	manifest.Payload = objectEvidence(raw, manifest.Payload)
	signed := signGoldenManifest(t, manifest)
	_, projections := goldenReaders(t)
	rootKeys, manifestKeys := goldenKeyrings(t)
	_, err = VerifySegment(context.Background(), signed, bytes.NewReader(raw), projections,
		rootKeys, manifestKeys, nil)
	var compatibility *CompatibilityError
	if !errors.As(err, &compatibility) || compatibility.Code != CompatibilityVerifierOutdated {
		t.Fatalf("error = %#v, want typed verifier-outdated", err)
	}
}

func TestVerifyDetectsChangedPayloadByte(t *testing.T) {
	raw, err := os.ReadFile("testdata/mixed-v1-v2.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "operator-1", "operator-2", 1))
	_, projections := goldenReaders(t)
	rootKeys, manifestKeys := goldenKeyrings(t)
	report, err := VerifySegment(context.Background(), goldenManifest(t), bytes.NewReader(raw),
		projections, rootKeys, manifestKeys, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Code != VerificationObjectHashMismatch {
		t.Fatalf("report = %+v", report)
	}
}

func TestVerifyDetectsEventHashMismatchAfterObjectAndManifestAreResigned(t *testing.T) {
	raw, err := os.ReadFile("testdata/mixed-v1-v2.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), `"name":"alpha"`, `"name":"tampered"`, 1))
	manifest := goldenManifest(t).Unsigned
	manifest.Payload = objectEvidence(raw, manifest.Payload)
	signed := signGoldenManifest(t, manifest)
	_, projections := goldenReaders(t)
	rootKeys, manifestKeys := goldenKeyrings(t)
	report, err := VerifySegment(context.Background(), signed, bytes.NewReader(raw), projections,
		rootKeys, manifestKeys, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Code != VerificationEventHashMismatch || report.FirstBadSequence != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestVerifyDetectsSequenceGapAndBrokenLink(t *testing.T) {
	for name, mutate := range map[string]func([]auditEventMutation) []auditEventMutation{
		"sequence-gap": func(items []auditEventMutation) []auditEventMutation {
			items[1].Sequence = 3
			return items
		},
		"broken-link": func(items []auditEventMutation) []auditEventMutation {
			items[1].PrevHash = strings.Repeat("d", 64)
			return items
		},
	} {
		t.Run(name, func(t *testing.T) {
			events := fixtureEvents(t)
			items := []auditEventMutation{{Event: &events[0]}, {Event: &events[1]}}
			items = mutate(items)
			for _, item := range items {
				if item.Sequence != 0 {
					item.Event.Sequence = item.Sequence
				}
				if item.PrevHash != "" {
					item.Event.PrevHash = item.PrevHash
				}
				hash, err := item.Event.ComputeHash()
				if err != nil {
					t.Fatal(err)
				}
				item.Event.EventHash = hash
			}
			var payload bytes.Buffer
			object, err := EncodePayload(&payload, events)
			if err != nil {
				t.Fatal(err)
			}
			manifest := goldenManifest(t).Unsigned
			previousPayloadSHA := manifest.Payload.SHA256
			manifest.Payload.SHA256 = object.SHA256
			manifest.Payload.Key = strings.Replace(manifest.Payload.Key,
				previousPayloadSHA, object.SHA256, 1)
			manifest.Payload.ProviderChecksum = "sha256:" + object.SHA256
			manifest.Payload.SizeBytes = object.SizeBytes
			manifest.LastEventHash = events[1].EventHash
			signed := signGoldenManifest(t, manifest)
			_, projections := goldenReaders(t)
			rootKeys, manifestKeys := goldenKeyrings(t)
			report, err := VerifySegment(context.Background(), signed, bytes.NewReader(payload.Bytes()),
				projections, rootKeys, manifestKeys, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := VerificationSequenceGap
			if name == "broken-link" {
				want = VerificationBrokenLink
			}
			if report.Code != want {
				t.Fatalf("report = %+v, want %s", report, want)
			}
		})
	}
}

type auditEventMutation struct {
	Event    *audit.Event
	Sequence int64
	PrevHash string
}

func TestVerifyDetectsMissingOverlappingAndReorderedManifest(t *testing.T) {
	previous := goldenManifest(t)
	previousBytes, err := EncodeSignedManifestV1(previous)
	if err != nil {
		t.Fatal(err)
	}
	previousSum := sha256.Sum256(previousBytes)
	previousDigest := hex.EncodeToString(previousSum[:])
	previousRef := ArtifactRefV1{
		BucketID:  "archive-fixture",
		Key:       "audit/v1/manifest/seq-0000000000000000001-0000000000000000002-" + previousDigest + ".json",
		VersionID: "manifest-v1", SHA256: previousDigest,
	}
	current := previous
	current.Unsigned.FromSequence = 3
	current.Unsigned.ToSequence = 4
	current.Unsigned.PreviousManifest = PreviousManifestRefV1{
		Kind: "object", BucketID: previousRef.BucketID, Key: previousRef.Key,
		VersionID: previousRef.VersionID, SHA256: previousRef.SHA256,
	}
	for name, test := range map[string]struct {
		current  SignedManifestV1
		previous *SignedManifestV1
		ref      *ArtifactRefV1
	}{
		"missing":     {current, nil, nil},
		"overlapping": {func() SignedManifestV1 { value := current; value.Unsigned.FromSequence = 2; return value }(), &previous, &previousRef},
		"reordered":   {func() SignedManifestV1 { value := current; value.Unsigned.FromSequence = 1; return value }(), &previous, &previousRef},
	} {
		t.Run(name, func(t *testing.T) {
			report := VerifyManifestContinuity(test.current, test.previous, test.ref)
			if report.Code != VerificationManifestGap {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}

func TestVerifyRejectsWrongManifestKeyAndWrongRootKey(t *testing.T) {
	rootKeys, manifestKeys := goldenKeyrings(t)
	for name, keys := range map[string]struct{ root, manifest Keyring }{
		"manifest": {rootKeys, rootKeys},
		"root":     {manifestKeys, manifestKeys},
	} {
		t.Run(name, func(t *testing.T) {
			payload, projections := goldenReaders(t)
			report, err := VerifySegment(context.Background(), goldenManifest(t), payload, projections,
				keys.root, keys.manifest, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := VerificationManifestSignature
			if name == "root" {
				want = VerificationRootSignature
			}
			if report.Code != want {
				t.Fatalf("report = %+v, want %s", report, want)
			}
		})
	}
}

func TestVerifyRejectsArtifactEmbeddedPublicKeySelfAuthentication(t *testing.T) {
	raw, err := os.ReadFile("testdata/chain-root-ref-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	withEmbedded := []byte(strings.Replace(string(raw), `,"key_id":"root-key-1"}`, `,"public_key":"attacker","key_id":"root-key-1"}`, 1))
	if _, err := DecodeChainRootRefV1(withEmbedded); err == nil {
		t.Fatal("artifact-supplied public key was accepted as part of the trust input")
	}
}

type failReader struct{ err error }

func (reader failReader) Read([]byte) (int, error) { return 0, reader.err }

func TestVerifySeparatesOperationalFailureFromIntegrityReport(t *testing.T) {
	rootKeys, manifestKeys := goldenKeyrings(t)
	_, projections := goldenReaders(t)
	_, err := VerifySegment(context.Background(), goldenManifest(t),
		failReader{err: context.DeadlineExceeded}, projections, rootKeys, manifestKeys, nil)
	var operational *OperationalError
	if !errors.As(err, &operational) || operational.Code != "object_read_failed" {
		t.Fatalf("error = %#v, want typed operational failure", err)
	}
}

func TestVerifierOutdatedIsOnlyTypedCompatibilityError(t *testing.T) {
	for _, code := range []VerificationCode{
		VerificationOK, VerificationObjectHashMismatch, VerificationManifestSignature,
		VerificationManifestGap, VerificationSequenceGap, VerificationBrokenLink,
		VerificationEventHashMismatch, VerificationRootSignature, VerificationFormatInvalid,
	} {
		if strings.Contains(string(code), "outdated") {
			t.Fatalf("verification report code %q improperly carries compatibility state", code)
		}
	}
}

func TestVerifyRejectsProjectionHiddenFields(t *testing.T) {
	payload, projections := goldenReaders(t)
	stagingRaw, err := os.ReadFile("testdata/projection-staging.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	stagingRaw = []byte(strings.Replace(string(stagingRaw), `,"event_hash"`, `,"reason":"hidden","event_hash"`, 1))
	manifest := goldenManifest(t).Unsigned
	for index := range manifest.Projections {
		if manifest.Projections[index].Environment == "staging" {
			manifest.Projections[index].Object = objectEvidence(stagingRaw, manifest.Projections[index].Object)
		}
	}
	signed := signGoldenManifest(t, manifest)
	projections["staging"] = bytes.NewReader(stagingRaw)
	rootKeys, manifestKeys := goldenKeyrings(t)
	report, err := VerifySegment(context.Background(), signed, payload, projections,
		rootKeys, manifestKeys, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Code != VerificationFormatInvalid {
		t.Fatalf("report = %+v", report)
	}
}
