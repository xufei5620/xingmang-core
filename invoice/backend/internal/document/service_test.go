package document

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func cleanScanner(context.Context, string) error { return nil }

func TestSavePDFQuarantinesScansAndPromotes(t *testing.T) {
	root := t.TempDir()
	store := LocalStore{Root: root, Scanner: ScannerFunc(cleanScanner)}
	stored, err := store.SavePDF(context.Background(), bytes.NewBufferString("%PDF-1.7\nexample\n%%EOF"))
	if err != nil {
		t.Fatal(err)
	}
	if stored.MIME != "application/pdf" || stored.SizeBytes == 0 || len(stored.SHA256) != 64 {
		t.Fatalf("unexpected metadata: %+v", stored)
	}
	file, err := store.OpenAuthorized(stored.ObjectKey)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	body, _ := io.ReadAll(file)
	_ = file.Close()
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		t.Fatal("stored body changed")
	}
	entries, err := os.ReadDir(filepath.Join(root, "quarantine"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("quarantine not empty: %v", entries)
	}
	if err = store.Delete(stored.ObjectKey); err != nil {
		t.Fatal(err)
	}
	if _, err = store.OpenAuthorized(stored.ObjectKey); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted object is still available: %v", err)
	}
}

func TestSavePDFRejectsInvalidOversizedAndScannerFailure(t *testing.T) {
	root := t.TempDir()
	store := LocalStore{Root: root, MaxBytes: 8, Scanner: ScannerFunc(cleanScanner)}
	if _, err := store.SavePDF(context.Background(), bytes.NewBufferString("not-pdf")); !errors.Is(err, ErrInvalidPDF) {
		t.Fatalf("invalid PDF error=%v", err)
	}
	if _, err := store.SavePDF(context.Background(), bytes.NewBufferString("%PDF-123456")); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("large PDF error=%v", err)
	}
	store.MaxBytes = 1024
	store.Scanner = ScannerFunc(func(context.Context, string) error { return errors.New("malware") })
	if _, err := store.SavePDF(context.Background(), bytes.NewBufferString("%PDF-clean-looking")); !errors.Is(err, ErrScanRejected) {
		t.Fatalf("scanner error=%v", err)
	}
}

func TestOpenAuthorizedRejectsTraversal(t *testing.T) {
	// Both the store and its readable sibling are owned by this synthetic
	// sandbox: a removed boundary must open a real file, not fail on ENOENT.
	sandbox := t.TempDir()
	root := filepath.Join(sandbox, "store")
	if err := os.MkdirAll(filepath.Join(root, "issued"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sandbox, "outside.pdf"), []byte("outside fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "issued", "allowed.pdf"), []byte("allowed fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := LocalStore{Root: root, Scanner: ScannerFunc(cleanScanner)}
	file, err := store.OpenAuthorized("../outside.pdf")
	if file != nil {
		_ = file.Close()
	}
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected traversal policy rejection for existing sibling, got %v", err)
	}
	file, err = store.OpenAuthorized("issued/allowed.pdf")
	if err != nil {
		t.Fatalf("allowed issued file rejected: %v", err)
	}
	_ = file.Close()
}
