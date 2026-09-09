package backupverify

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"invoice-system/backend/internal/document"
	"invoice-system/backend/internal/securefields"
)

func backupTestKeyring() securefields.Keyring {
	return securefields.Keyring{
		CurrentKeyID:   "backup-test-v1",
		EncryptionKeys: map[string][]byte{"backup-test-v1": bytes.Repeat([]byte{0x42}, 32)},
		IndexKey:       bytes.Repeat([]byte{0x24}, 32),
	}
}

func TestVerifyDocumentRecordDecryptsAndChecksDatabaseMetadata(t *testing.T) {
	root := t.TempDir()
	store := document.EncryptedLocalStore{
		Root: root, Scanner: document.ScannerFunc(func(context.Context, string) error { return nil }),
		Keyring: backupTestKeyring(),
	}
	stored, err := store.SavePDF(context.Background(), bytes.NewBufferString("%PDF-1.7\nrestored invoice\n%%EOF"))
	if err != nil {
		t.Fatal(err)
	}
	record := documentRecord{ID: "doc-1", ObjectKey: stored.ObjectKey, SHA256: stored.SHA256, SizeBytes: stored.SizeBytes, MIME: stored.MIME}
	if err = verifyDocumentRecord(store, record); err != nil {
		t.Fatal(err)
	}
	record.SHA256 = strings.Repeat("0", 64)
	if err = verifyDocumentRecord(store, record); err == nil {
		t.Fatal("database SHA mismatch was accepted")
	}
}

func TestArchivedObjectKeysRejectsUnexpectedEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "issued"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "issued", "invoice.pdf.enc"), []byte("cipher"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := archivedObjectKeys(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["issued/invoice.pdf.enc"]; !ok {
		t.Fatalf("keys=%v", keys)
	}
	if err = os.WriteFile(filepath.Join(root, "issued", "orphan.part"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = archivedObjectKeys(root); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected archive entry error=%v", err)
	}
}
