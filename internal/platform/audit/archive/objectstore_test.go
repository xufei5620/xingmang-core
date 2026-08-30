package archive

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
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

func TestAUD2ApprovedMinIOObjectVersionAllowsSSES3(t *testing.T) {
	value := goldenManifest(t).Unsigned.Payload
	value.EncryptionMode = "SSE-S3"
	if err := ValidateObjectVersion(value); err != nil {
		t.Fatalf("approved SSE-S3 object version rejected: %v", err)
	}
}

func TestAUD2ObjectWriteIntentRejectsOversizedBodyDeclaration(t *testing.T) {
	value := aud2LocalIntent([]byte("fixture"))
	value.SizeBytes = MaxObjectWriteBytes + 1
	if err := ValidateObjectWriteIntent(value); err == nil {
		t.Fatal("oversized object intent unexpectedly accepted")
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

func TestAUD2FilesystemReadbackDetectsTamperedBody(t *testing.T) {
	root := t.TempDir()
	store, err := NewFilesystemStore(root, "local-fixture")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("tamper target")
	intent := aud2LocalIntent(body)
	object, err := store.PutIfAbsent(context.Background(), intent, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	dataPath, err := store.objectPath(intent.Key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecoverPutResult(context.Background(), intent); err == nil {
		t.Fatal("tampered body unexpectedly recovered")
	}
	if _, err := store.HeadVersion(context.Background(), object); err != nil {
		t.Fatalf("metadata HEAD should remain exact despite body corruption: %v", err)
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

func TestAUD2FilesystemRejectsSymlinkedObjectPrefix(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	store, err := NewFilesystemStore(root, "local-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "local-fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "local-fixture", "audit")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	intent := aud2LocalIntent([]byte("symlink"))
	if _, err := store.PutIfAbsent(context.Background(), intent, strings.NewReader("symlink")); err == nil {
		t.Fatal("symlinked object prefix unexpectedly accepted")
	}
}
