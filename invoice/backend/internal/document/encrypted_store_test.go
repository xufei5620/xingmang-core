package document

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"invoice-system/backend/internal/securefields"
)

func testDocumentKeyring() securefields.Keyring {
	return securefields.Keyring{
		CurrentKeyID:   "document-v1",
		EncryptionKeys: map[string][]byte{"document-v1": bytes.Repeat([]byte{0x44}, 32)},
		IndexKey:       bytes.Repeat([]byte{0x55}, 32),
	}
}

func TestEncryptedLocalStoreRoundTripAndPlaintextAbsence(t *testing.T) {
	root := t.TempDir()
	store := EncryptedLocalStore{Root: root, Scanner: ScannerFunc(cleanScanner), Keyring: testDocumentKeyring()}
	want := []byte("%PDF-1.7\nprivate invoice content\n%%EOF")
	stored, err := store.SavePDF(context.Background(), bytes.NewReader(want))
	if err != nil {
		t.Fatal(err)
	}
	if stored.ObjectVersion != "encrypted-local-v1:document-v1" || !stringsHasSuffix(stored.ObjectKey, ".pdf.enc") {
		t.Fatalf("unexpected metadata: %+v", stored)
	}
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(stored.ObjectKey)))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("%PDF-")) || bytes.Contains(raw, []byte("private invoice content")) {
		t.Fatal("encrypted object contains plaintext")
	}
	reader, err := store.OpenAuthorized(stored.ObjectKey)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("round trip error=%v body=%q", err, got)
	}
	if err = store.Delete(stored.ObjectKey); err != nil {
		t.Fatal(err)
	}
	if _, err = store.OpenAuthorized(stored.ObjectKey); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted encrypted object is still available: %v", err)
	}
}

func TestEncryptedLocalStoreDetectsTamperingAndTraversal(t *testing.T) {
	root := t.TempDir()
	store := EncryptedLocalStore{Root: root, Scanner: ScannerFunc(cleanScanner), Keyring: testDocumentKeyring()}
	stored, err := store.SavePDF(context.Background(), bytes.NewReader([]byte("%PDF-sensitive")))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(stored.ObjectKey))
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body[len(body)-6] ^= 0xff
	if err = os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := store.OpenAuthorized(stored.ObjectKey)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr == nil {
		t.Fatal("tampered encrypted document was accepted")
	}
	if _, err = store.OpenAuthorized("../invoice.pdf.enc"); err == nil {
		t.Fatal("encrypted store accepted traversal")
	}

	wrong := store
	wrong.Keyring.EncryptionKeys = map[string][]byte{"other": bytes.Repeat([]byte{0x66}, 32)}
	if _, err = wrong.OpenAuthorized(stored.ObjectKey); err == nil {
		t.Fatal("store without the historical key opened the document")
	}
}

func TestEncryptedLocalStoreFailsClosedWhenDirectorySyncFails(t *testing.T) {
	root := t.TempDir()
	store := EncryptedLocalStore{
		Root: root, Scanner: ScannerFunc(cleanScanner), Keyring: testDocumentKeyring(),
		DirectorySync: func(string) error { return errors.New("injected directory sync failure") },
	}
	if _, err := store.SavePDF(context.Background(), bytes.NewReader([]byte("%PDF-sync-failure"))); err == nil {
		t.Fatal("document save succeeded despite issued-directory sync failure")
	}
	entries, err := os.ReadDir(filepath.Join(root, "issued"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("unsafely published object remains after sync failure: %v", entries)
	}
}

func stringsHasSuffix(value, suffix string) bool {
	return len(value) >= len(suffix) && value[len(value)-len(suffix):] == suffix
}
