package document

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const DefaultMaxPDFBytes int64 = 20 << 20

var (
	ErrTooLarge     = errors.New("PDF exceeds configured size limit")
	ErrInvalidPDF   = errors.New("file is not a supported PDF")
	ErrScanRejected = errors.New("PDF failed security scanning")
)

type Scanner interface {
	Scan(context.Context, string) error
}

type ScannerFunc func(context.Context, string) error

func (f ScannerFunc) Scan(ctx context.Context, path string) error { return f(ctx, path) }

type Store interface {
	SavePDF(context.Context, io.Reader) (StoredPDF, error)
	OpenAuthorized(string) (io.ReadCloser, error)
	Delete(string) error
}

var _ Store = LocalStore{}

type StoredPDF struct {
	ObjectKey     string
	ObjectVersion string
	SHA256        string
	SizeBytes     int64
	MIME          string
}

type LocalStore struct {
	Root          string
	MaxBytes      int64
	Scanner       Scanner
	DirectorySync func(string) error
}

// SavePDF writes to a quarantine directory first, verifies the PDF signature,
// runs the configured scanner, then atomically promotes the file into the
// issued directory. The caller-supplied filename is never used as a path.
func (s LocalStore) SavePDF(ctx context.Context, reader io.Reader) (StoredPDF, error) {
	if strings.TrimSpace(s.Root) == "" || s.Scanner == nil {
		return StoredPDF{}, errors.New("document store is not safely configured")
	}
	maxBytes := s.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxPDFBytes
	}
	quarantine := filepath.Join(s.Root, "quarantine")
	issued := filepath.Join(s.Root, "issued")
	if err := os.MkdirAll(quarantine, 0o700); err != nil {
		return StoredPDF{}, err
	}
	if err := os.MkdirAll(issued, 0o700); err != nil {
		return StoredPDF{}, err
	}
	key, err := randomKey()
	if err != nil {
		return StoredPDF{}, err
	}
	tempPath := filepath.Join(quarantine, key+".pdf.part")
	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return StoredPDF{}, err
	}
	removeTemp := true
	defer func() {
		_ = file.Close()
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()

	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(reader, maxBytes+1))
	if copyErr != nil {
		return StoredPDF{}, copyErr
	}
	if written > maxBytes {
		return StoredPDF{}, ErrTooLarge
	}
	if err := file.Sync(); err != nil {
		return StoredPDF{}, err
	}
	if err := file.Close(); err != nil {
		return StoredPDF{}, err
	}
	valid, err := hasPDFMagic(tempPath)
	if err != nil {
		return StoredPDF{}, err
	}
	if !valid {
		return StoredPDF{}, ErrInvalidPDF
	}
	if err := s.Scanner.Scan(ctx, tempPath); err != nil {
		return StoredPDF{}, fmt.Errorf("%w: %v", ErrScanRejected, err)
	}
	finalPath := filepath.Join(issued, key+".pdf")
	if err := os.Rename(tempPath, finalPath); err != nil {
		return StoredPDF{}, err
	}
	if err := s.syncDirectory(issued); err != nil {
		_ = os.Remove(finalPath)
		_ = s.syncDirectory(issued)
		return StoredPDF{}, fmt.Errorf("sync issued document directory: %w", err)
	}
	removeTemp = false
	return StoredPDF{ObjectKey: "issued/" + key + ".pdf", ObjectVersion: "local-v1", SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: written, MIME: "application/pdf"}, nil
}

func (s LocalStore) OpenAuthorized(objectKey string) (io.ReadCloser, error) {
	clean := filepath.Clean(filepath.FromSlash(objectKey))
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") || !strings.HasPrefix(filepath.ToSlash(clean), "issued/") {
		return nil, errors.New("invalid object key")
	}
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return nil, err
	}
	target, err := filepath.Abs(filepath.Join(root, clean))
	if err != nil {
		return nil, err
	}
	if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		return nil, errors.New("object path escapes storage root")
	}
	return os.Open(target)
}

func (s LocalStore) Delete(objectKey string) error {
	target, err := resolveIssuedPath(s.Root, objectKey, ".pdf")
	if err != nil {
		return err
	}
	removed := false
	if err = os.Remove(target); err == nil {
		removed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if removed {
		return s.syncDirectory(filepath.Dir(target))
	}
	return nil
}

func (s LocalStore) syncDirectory(path string) error {
	if s.DirectorySync != nil {
		return s.DirectorySync(path)
	}
	return syncDocumentDirectory(path)
}

func randomKey() (string, error) {
	var value [18]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
func hasPDFMagic(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	header := make([]byte, 5)
	if _, err = io.ReadFull(file, header); err != nil {
		return false, nil
	}
	return string(header) == "%PDF-", nil
}
