package archive

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// This test is opt-in and must run against a fresh disposable PG18 database
// with migrations 000001..000018 already applied. It never targets the shared
// xingmang-launch stack.
func TestAUD2PostgresStoresRoundTrip(t *testing.T) {
	pool := aud2ContractPool(t)
	segment := testCommittedSegment(t, 1, 2)
	resolver := func(_ context.Context, locator ObjectVersionV1) (SignedManifestV1, ObjectVersionV1, error) {
		if locator.Key != segment.ManifestObject.Key || locator.VersionID != segment.ManifestObject.VersionID || locator.SHA256 != segment.ManifestObject.SHA256 {
			return SignedManifestV1{}, ObjectVersionV1{}, errors.New("manifest locator mismatch")
		}
		return segment.Manifest, segment.ManifestObject, nil
	}
	catalog, err := NewPostgresCatalogWithProofAndChecker(pool,
		func(context.Context, CommittedSegment) (CatalogCoverageProof, error) {
			return CatalogCoverageProof{
				Generation: 1, CheckpointSHA256: segment.CheckpointSHA256,
				TerminalManifestSHA256:    segment.ManifestObject.SHA256,
				TerminalManifestKey:       segment.ManifestObject.Key,
				TerminalManifestVersionID: segment.ManifestObject.VersionID,
				TerminalSequence:          segment.Manifest.Unsigned.ToSequence,
				TerminalRootHash:          segment.Manifest.Unsigned.ChainRoot.RootHash,
			}, nil
		},
		func(context.Context, CommittedSegment) error { return nil }, resolver, "archive-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.CommitCoveredSegment(context.Background(), segment); err != nil {
		t.Fatalf("catalog commit: %v", err)
	}
	if err := catalog.CommitCoveredSegment(context.Background(), segment); err != nil {
		t.Fatalf("catalog replay: %v", err)
	}
	latest, err := catalog.Latest(context.Background())
	if err != nil || latest.ManifestObject.SHA256 != segment.ManifestObject.SHA256 {
		t.Fatalf("catalog latest=%+v err=%v", latest, err)
	}

	journal, err := NewPostgresReceiptJournal(pool)
	if err != nil {
		t.Fatal(err)
	}
	intent := testOperationIntent()
	body := []byte("journal-body")
	intent.Objects[0].SHA256 = sha256Hex(body)
	intent.Objects[0].SizeBytes = int64(len(body))
	intent.Objects[0].Key = "audit/v1/payload/seq-0000000000000000001-0000000000000000001-" + intent.Objects[0].SHA256 + ".ndjson"
	if err := journal.BeginIntent(context.Background(), intent); err != nil {
		t.Fatalf("begin intent: %v", err)
	}
	object := aud2LocalIntent(body)
	object.OperationID = intent.OperationID
	if err := journal.AppendPutResult(context.Background(), intent.OperationID, 0, expectedObjectForTest(object, body)); err != nil {
		t.Fatalf("append put: %v", err)
	}
	terminal := []byte("journal-terminal")
	if err := journal.AppendTerminalResult(context.Background(), intent.OperationID, terminal, nil, sha256Hex(terminal)); err != nil {
		t.Fatalf("append terminal: %v", err)
	}
	receipt, err := journal.LoadOperation(context.Background(), intent.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.PutResults) != 1 || !bytes.Equal(receipt.TerminalResultBytes, terminal) {
		t.Fatalf("receipt=%+v", receipt)
	}
	if _, err := journal.LoadOperation(context.Background(), uuid.New()); !errors.Is(err, ErrObjectNotFound) && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("missing operation error=%v", err)
	}
}

func TestAUD2PostgresReceiptRejectsCompressedIncompleteOrdinals(t *testing.T) {
	pool := aud2ContractPool(t)
	intent := testOperationIntent()
	intent.Objects = append(intent.Objects, intent.Objects[0])
	intent.Objects[1].Ordinal = 1
	intent.Objects[1].Key = "audit/v1/payload/seq-0000000000000000001-0000000000000000001-" + strings.Repeat("e", 64) + ".ndjson"
	intent.Objects[1].SHA256 = strings.Repeat("e", 64)
	intent.Objects[1].ProviderIdempotencyToken = "fixture-intent-token-2"
	journal, err := NewPostgresReceiptJournal(pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.BeginIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	object := ObjectVersionV1{BucketID: "local-fixture", Key: intent.Objects[1].Key, VersionID: "fixture-version-2", SHA256: intent.Objects[1].SHA256,
		SizeBytes: 1, ContentType: PayloadContentType, ProviderChecksum: "sha256:" + intent.Objects[1].SHA256, ETag: "fixture-etag-2",
		EncryptionMode: "LOCAL-ONLY", ObjectLockMode: "NONE", RetainUntil: intent.Objects[1].RetainUntil, RowCount: 1}
	objectBytes, err := CanonicalObjectVersionBytes(object)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(context.Background(), `
		INSERT INTO audit.archive_put_receipt
		  (operation_id, ordinal, object_version_bytes, object_version_sha256, recorded_at)
		VALUES ($1, 1, $2, $3, now())`, intent.OperationID, objectBytes, sha256Hex(objectBytes))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.LoadOperation(context.Background(), intent.OperationID); !errors.Is(err, ErrJournalIncomplete) {
		t.Fatalf("incomplete ordinal set should fail closed with ErrJournalIncomplete, got %v", err)
	}
}

func expectedObjectForTest(intent ObjectWriteIntentV1, body []byte) ObjectVersionV1 {
	return ObjectVersionV1{BucketID: intent.BucketID, Key: intent.Key, VersionID: "fixture-version", SHA256: intent.SHA256,
		SizeBytes: int64(len(body)), ContentType: intent.ContentType, ProviderChecksum: "sha256:" + intent.SHA256,
		ETag: "fixture-etag", EncryptionMode: intent.EncryptionMode, KMSKeyID: intent.KMSKeyID,
		ObjectLockMode: intent.ObjectLockMode, RetainUntil: intent.RetainUntil, RowCount: 1}
}
