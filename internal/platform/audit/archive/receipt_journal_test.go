package archive

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testOperationIntent() OperationIntentV1 {
	operationID := uuid.New()
	return OperationIntentV1{
		OperationID: operationID, ApprovalEnvelopeSHA256: strings.Repeat("a", 64),
		DeterministicBytesDigest: strings.Repeat("b", 64),
		Objects: []ObjectWriteIntentV1{{
			OperationID: operationID, Ordinal: 0, BucketID: "local-fixture",
			Key:    "audit/v1/payload/seq-0000000000000000001-0000000000000000001-" + strings.Repeat("c", 64) + ".ndjson",
			SHA256: strings.Repeat("c", 64), SizeBytes: 1, ContentType: PayloadContentType,
			EncryptionMode: "LOCAL-ONLY", ObjectLockMode: "NONE",
			RetainUntil:              NewWireTime(time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC)),
			ProviderIdempotencyToken: "fixture-intent-token",
		}},
	}
}

func TestAUD2MemoryReceiptJournalIsIdempotentAndConflictsOnChangedBytes(t *testing.T) {
	journal := NewMemoryReceiptJournal()
	intent := testOperationIntent()
	if err := journal.BeginIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if err := journal.BeginIntent(context.Background(), intent); err != nil {
		t.Fatalf("same intent should be idempotent: %v", err)
	}
	changed := intent
	changed.DeterministicBytesDigest = strings.Repeat("d", 64)
	if err := journal.BeginIntent(context.Background(), changed); err == nil {
		t.Fatal("changed intent unexpectedly accepted")
	}
	version := ObjectVersionV1{BucketID: "local-fixture", Key: intent.Objects[0].Key, VersionID: "fixture-version", SHA256: intent.Objects[0].SHA256,
		SizeBytes: 1, ContentType: PayloadContentType, ProviderChecksum: "sha256:" + intent.Objects[0].SHA256, ETag: "fixture-etag",
		EncryptionMode: "LOCAL-ONLY", ObjectLockMode: "NONE", RetainUntil: intent.Objects[0].RetainUntil, RowCount: 1}
	if err := journal.AppendPutResult(context.Background(), intent.OperationID, 0, version); err != nil {
		t.Fatal(err)
	}
	if err := journal.AppendPutResult(context.Background(), intent.OperationID, 0, version); err != nil {
		t.Fatalf("same put receipt should be idempotent: %v", err)
	}
	version.VersionID = "other-version"
	if err := journal.AppendPutResult(context.Background(), intent.OperationID, 0, version); err == nil {
		t.Fatal("changed put receipt unexpectedly accepted")
	}
}

func TestAUD2MemoryReceiptJournalRequiresIntentAndTerminalIsAppendOnly(t *testing.T) {
	journal := NewMemoryReceiptJournal()
	intent := testOperationIntent()
	version := ObjectVersionV1{BucketID: "local-fixture", Key: intent.Objects[0].Key, VersionID: "fixture-version", SHA256: intent.Objects[0].SHA256,
		SizeBytes: 1, ContentType: PayloadContentType, ProviderChecksum: "sha256:" + intent.Objects[0].SHA256, ETag: "fixture-etag",
		EncryptionMode: "LOCAL-ONLY", ObjectLockMode: "NONE", RetainUntil: intent.Objects[0].RetainUntil, RowCount: 1}
	if err := journal.AppendPutResult(context.Background(), intent.OperationID, 0, version); err == nil {
		t.Fatal("put receipt without intent unexpectedly accepted")
	}
	if err := journal.BeginIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if err := journal.AppendPutResult(context.Background(), intent.OperationID, 0, version); err != nil {
		t.Fatal(err)
	}
	terminal := []byte("terminal")
	if err := journal.AppendTerminalResult(context.Background(), intent.OperationID, terminal, nil, sha256Hex(terminal)); err != nil {
		t.Fatal(err)
	}
	if err := journal.AppendTerminalResult(context.Background(), intent.OperationID, terminal, nil, sha256Hex(terminal)); err != nil {
		t.Fatalf("same terminal receipt should be idempotent: %v", err)
	}
	if err := journal.AppendTerminalResult(context.Background(), intent.OperationID, []byte("changed"), nil, sha256Hex([]byte("changed"))); err == nil {
		t.Fatal("changed terminal receipt unexpectedly accepted")
	}
}

func TestAUD2MemoryReceiptJournalLoadReturnsDefensiveCopy(t *testing.T) {
	journal := NewMemoryReceiptJournal()
	intent := testOperationIntent()
	if err := journal.BeginIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	receipt, err := journal.LoadOperation(context.Background(), intent.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Intent.ApprovalEnvelopeSHA256 = strings.Repeat("z", 64)
	again, err := journal.LoadOperation(context.Background(), intent.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if again.Intent.ApprovalEnvelopeSHA256 == receipt.Intent.ApprovalEnvelopeSHA256 {
		t.Fatal("journal returned mutable internal state")
	}
}
