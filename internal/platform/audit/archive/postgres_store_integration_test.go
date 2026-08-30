package archive

import (
	"bytes"
	"context"
	"errors"
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
	catalog, err := NewPostgresCatalogWithBucket(pool, func(context.Context, CommittedSegment) error { return nil }, resolver, "archive-fixture")
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

func expectedObjectForTest(intent ObjectWriteIntentV1, body []byte) ObjectVersionV1 {
	return ObjectVersionV1{BucketID: intent.BucketID, Key: intent.Key, VersionID: "fixture-version", SHA256: intent.SHA256,
		SizeBytes: int64(len(body)), ContentType: intent.ContentType, ProviderChecksum: "sha256:" + intent.SHA256,
		ETag: "fixture-etag", EncryptionMode: intent.EncryptionMode, KMSKeyID: intent.KMSKeyID,
		ObjectLockMode: intent.ObjectLockMode, RetainUntil: intent.RetainUntil, RowCount: 1}
}
