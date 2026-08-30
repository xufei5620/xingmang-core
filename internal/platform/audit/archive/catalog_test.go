package archive

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testCommittedSegment(t *testing.T, from, to int64) CommittedSegment {
	t.Helper()
	manifest := goldenManifest(t)
	rowCount := to - from + 1
	if from < 1 || rowCount < 1 {
		t.Fatal("invalid test range")
	}
	manifest.Unsigned.FromSequence = from
	manifest.Unsigned.ToSequence = to
	manifest.Unsigned.RowCount = rowCount
	manifest.Unsigned.FirstPrevHash = hashForTest("prev", from)
	manifest.Unsigned.FirstEventHash = hashForTest("first", from)
	manifest.Unsigned.LastEventHash = hashForTest("last", to)
	manifest.Unsigned.CanonicalVersionCounts = [2]CanonicalCountV1{{Version: 1, RowCount: rowCount}, {Version: 2, RowCount: 0}}
	manifest.Unsigned.EligibleActionRunCount = 0
	manifest.Unsigned.MatchedActionRunCount = 0
	manifest.Unsigned.MissingAuditEventCount = 0
	manifest.Unsigned.DuplicateAuditEventCount = 0
	if from == 1 {
		manifest.Unsigned.FirstPrevHash = "0000000000000000000000000000000000000000000000000000000000000000"
		manifest.Unsigned.PreviousManifest = PreviousManifestRefV1{Kind: "genesis", SHA256: ManifestGenesisSHA256}
	} else {
		previousDigest := hashForTest("previous-manifest", from-1)
		manifest.Unsigned.PreviousManifest = PreviousManifestRefV1{
			Kind: "object", BucketID: "archive-fixture",
			Key:       fmt.Sprintf("audit/v1/manifest/seq-%019d-%019d-%s.json", from-1, from-1, previousDigest),
			VersionID: fmt.Sprintf("manifest-fixture-%d", from-1), SHA256: previousDigest,
		}
	}
	manifest.Unsigned.ChainRoot.FromSequence = from
	manifest.Unsigned.ChainRoot.ToSequence = to
	manifest.Unsigned.ChainRoot.RootHash = manifest.Unsigned.LastEventHash
	payloadDigest := hashForTest("payload", from)
	manifest.Unsigned.Payload = ObjectVersionV1{
		BucketID:  "archive-fixture",
		Key:       fmt.Sprintf("audit/v1/payload/seq-%019d-%019d-%s.ndjson", from, to, payloadDigest),
		VersionID: fmt.Sprintf("payload-fixture-%d", from), SHA256: payloadDigest,
		SizeBytes: rowCount * 100, ContentType: PayloadContentType,
		ProviderChecksum: "sha256:" + payloadDigest, ETag: "payload-etag",
		EncryptionMode: "SSE-KMS", KMSKeyID: "kms-fixture", ObjectLockMode: "COMPLIANCE",
		RetainUntil: NewWireTime(time.Date(2036, 8, 28, 0, 0, 0, 0, time.UTC)), RowCount: rowCount,
	}
	projectionDigest := hashForTest("projection", from)
	manifest.Unsigned.Projections = []ProjectionRefV1{{Environment: "production", Object: ObjectVersionV1{
		BucketID:  "archive-fixture",
		Key:       fmt.Sprintf("audit/v1/projection/production/seq-%019d-%019d-%s.ndjson", from, to, projectionDigest),
		VersionID: fmt.Sprintf("projection-fixture-%d", from), SHA256: projectionDigest,
		SizeBytes: rowCount * 50, ContentType: ProjectionContentType,
		ProviderChecksum: "sha256:" + projectionDigest, ETag: "projection-etag",
		EncryptionMode: "SSE-KMS", KMSKeyID: "kms-fixture", ObjectLockMode: "COMPLIANCE",
		RetainUntil: NewWireTime(time.Date(2036, 8, 28, 0, 0, 0, 0, time.UTC)), RowCount: rowCount,
	}}}
	manifest = signGoldenManifest(t, manifest.Unsigned)
	encoded, err := EncodeSignedManifestV1(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := sha256Hex(encoded)
	manifestObject := ObjectVersionV1{
		BucketID:  "archive-fixture",
		Key:       "audit/v1/manifest/seq-0000000000000000001-0000000000000000002-" + manifestDigest + ".json",
		VersionID: "manifest-fixture-version", SHA256: manifestDigest,
		ContentType: "application/json", ProviderChecksum: "sha256:" + manifestDigest,
		ETag: "manifest-etag", EncryptionMode: "SSE-KMS", KMSKeyID: "kms-fixture",
		ObjectLockMode: "COMPLIANCE", RetainUntil: NewWireTime(time.Date(2036, 8, 28, 0, 0, 0, 0, time.UTC)), RowCount: 1,
	}
	manifestObject.SizeBytes = int64(len(encoded))
	return CommittedSegment{
		ID: uuid.New(), Manifest: manifest, ManifestObject: manifestObject,
		CheckpointSHA256: strings.Repeat("a", 64), RecoveryGeneration: 1,
		CommittedAt: time.Date(2026, time.August, 30, 1, 0, 0, 0, time.UTC),
		VerifiedAt:  time.Date(2026, time.August, 30, 0, 59, 0, 0, time.UTC),
	}
}

func hashForTest(label string, sequence int64) string {
	return sha256Hex([]byte(fmt.Sprintf("aud2-%s-%d", label, sequence)))
}

func TestAUD2MemoryCatalogRequiresContiguousRanges(t *testing.T) {
	catalog := NewMemoryCatalog()
	first := testCommittedSegment(t, 1, 2)
	if err := catalog.CommitCoveredSegment(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Manifest.Unsigned.FromSequence = 4
	second.Manifest.Unsigned.ToSequence = 5
	second.Manifest.Unsigned.RowCount = 2
	if err := catalog.CommitCoveredSegment(context.Background(), second); err == nil {
		t.Fatal("catalog accepted a skipped range")
	}
}

func TestAUD2MemoryCatalogIdempotentReplayAndConflict(t *testing.T) {
	catalog := NewMemoryCatalog()
	segment := testCommittedSegment(t, 1, 2)
	if err := catalog.CommitCoveredSegment(context.Background(), segment); err != nil {
		t.Fatal(err)
	}
	if err := catalog.CommitCoveredSegment(context.Background(), segment); err != nil {
		t.Fatalf("same committed segment should be idempotent: %v", err)
	}
	conflict := segment
	conflict.ID = uuid.New()
	conflict.CheckpointSHA256 = strings.Repeat("b", 64)
	if err := catalog.CommitCoveredSegment(context.Background(), conflict); err == nil {
		t.Fatal("same range with a different checkpoint unexpectedly accepted")
	}
}

func TestAUD2MemoryCatalogLatestAndBeforeAreBounded(t *testing.T) {
	catalog := NewMemoryCatalog()
	segment := testCommittedSegment(t, 1, 2)
	if err := catalog.CommitCoveredSegment(context.Background(), segment); err != nil {
		t.Fatal(err)
	}
	latest, err := catalog.Latest(context.Background())
	if err != nil || latest.ID != segment.ID {
		t.Fatalf("latest = %+v, err=%v", latest, err)
	}
	rows, err := catalog.ListBefore(context.Background(), 99, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("before rows=%d err=%v", len(rows), err)
	}
	if _, err := catalog.ListBefore(context.Background(), 99, 0); err == nil {
		t.Fatal("zero limit unexpectedly accepted")
	}
}

func TestAUD2CatalogCoverageFailsClosedOnGenerationOrCheckpointMismatch(t *testing.T) {
	segment := testCommittedSegment(t, 1, 2)
	index := goldenRecoveryIndex(t)
	if err := ValidateCatalogCoverage(segment, index); err == nil {
		t.Fatal("unbound recovery index unexpectedly covered catalog segment")
	}
	segment.RecoveryGeneration = index.Unsigned.Generation
	segment.CheckpointSHA256 = index.Unsigned.Checkpoint.SHA256
	if err := ValidateCatalogCoverage(segment, index); err == nil {
		t.Fatal("terminal manifest mismatch unexpectedly covered catalog segment")
	}
}
