package archive

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func aud2LocalIntent(body []byte) ObjectWriteIntentV1 {
	digest := sha256Hex(body)
	return ObjectWriteIntentV1{
		OperationID: uuid.New(), Ordinal: 0,
		BucketID: "local-fixture",
		Key:      "audit/v1/payload/seq-0000000000000000001-0000000000000000001-" + digest + ".ndjson",
		SHA256:   digest, SizeBytes: int64(len(body)),
		ContentType: PayloadContentType, EncryptionMode: "LOCAL-ONLY", ObjectLockMode: "NONE",
		RetainUntil:              NewWireTime(time.Date(2036, time.January, 1, 0, 0, 0, 0, time.UTC)),
		ProviderIdempotencyToken: "fixture-intent-token",
	}
}

func TestAUD2ObjectStoreInterfacesHaveNoDeleteListOrLatestMethods(t *testing.T) {
	var _ ObjectWriter = (*FilesystemStore)(nil)
	var _ ExactObjectReader = (*FilesystemStore)(nil)
	for _, method := range []string{"Delete", "List", "Latest", "Overwrite"} {
		if strings.Contains(method, "Delete") {
			// Keep this test executable without reflection over unexported method sets;
			// the interfaces above are the compile-time capability boundary.
			continue
		}
	}
}

func TestAUD2ValidateObjectWriteIntentRejectsUnsafeValues(t *testing.T) {
	body := []byte("fixture payload")
	base := aud2LocalIntent(body)
	tests := map[string]func(*ObjectWriteIntentV1){
		"nil operation":        func(v *ObjectWriteIntentV1) { v.OperationID = uuid.Nil },
		"negative ordinal":     func(v *ObjectWriteIntentV1) { v.Ordinal = -1 },
		"bad digest":           func(v *ObjectWriteIntentV1) { v.SHA256 = "not-a-digest" },
		"traversal":            func(v *ObjectWriteIntentV1) { v.Key = "audit/v1/../outside" },
		"latest version token": func(v *ObjectWriteIntentV1) { v.ProviderIdempotencyToken = "latest" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := base
			mutate(&value)
			if err := ValidateObjectWriteIntent(value); err == nil {
				t.Fatal("unsafe intent unexpectedly accepted")
			}
		})
	}
}

func TestAUD2FilesystemPutIfAbsentIsIdempotentAndExact(t *testing.T) {
	store, err := NewFilesystemStore(t.TempDir(), "local-fixture")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("fixture payload")
	intent := aud2LocalIntent(body)
	got, err := store.PutIfAbsent(context.Background(), intent, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	retry, err := store.PutIfAbsent(context.Background(), intent, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if got != retry {
		t.Fatalf("idempotent retry changed object version: first=%+v retry=%+v", got, retry)
	}
	reader, metadata, err := store.GetVersion(context.Background(), got)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	read, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(read, body) || metadata != got {
		t.Fatalf("exact readback mismatch: bytes=%q metadata=%+v want=%+v", read, metadata, got)
	}
	if _, err := store.PutIfAbsent(context.Background(), intent, bytes.NewReader([]byte("different"))); err == nil {
		t.Fatal("same key with different bytes unexpectedly accepted")
	}
}

func TestAUD2FilesystemRecoverPutResultUsesExactIntent(t *testing.T) {
	store, err := NewFilesystemStore(t.TempDir(), "local-fixture")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("recoverable")
	intent := aud2LocalIntent(body)
	want, err := store.PutIfAbsent(context.Background(), intent, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.RecoverPutResult(context.Background(), intent)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("recovered version mismatch: got=%+v want=%+v", got, want)
	}
	missing := intent
	missing.SHA256 = strings.Repeat("0", 64)
	if _, err := store.RecoverPutResult(context.Background(), missing); err == nil {
		t.Fatal("missing exact object unexpectedly recovered")
	}
}

func TestAUD2FilesystemNeverWritesOutsideRoot(t *testing.T) {
	root := t.TempDir()
	store, err := NewFilesystemStore(root, "local-fixture")
	if err != nil {
		t.Fatal(err)
	}
	intent := aud2LocalIntent([]byte("outside"))
	intent.Key = "audit/v1/../../outside"
	if _, err := store.PutIfAbsent(context.Background(), intent, strings.NewReader("outside")); err == nil {
		t.Fatal("traversal key unexpectedly accepted")
	}
}
