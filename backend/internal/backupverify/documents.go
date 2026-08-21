package backupverify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/document"
	"invoice-system/backend/internal/securefields"
)

type Result struct {
	DatabaseDocuments int
	ArchivedObjects   int
	DecryptedSamples  int
}

type documentRecord struct {
	ID        string
	ObjectKey string
	SHA256    string
	SizeBytes int64
	MIME      string
}

func VerifyDocuments(ctx context.Context, pool *pgxpool.Pool, root string, keyring securefields.Keyring, sample int) (Result, error) {
	if pool == nil || strings.TrimSpace(root) == "" {
		return Result{}, errors.New("database pool and restored document root are required")
	}
	if err := keyring.Validate(); err != nil {
		return Result{}, fmt.Errorf("field keyring: %w", err)
	}
	if sample <= 0 || sample > 1000 {
		sample = 10
	}
	records, err := loadDocumentRecords(ctx, pool)
	if err != nil {
		return Result{}, err
	}
	archiveKeys, err := archivedObjectKeys(root)
	if err != nil {
		return Result{}, err
	}
	databaseKeys := make(map[string]struct{}, len(records))
	for _, record := range records {
		if record.MIME != "application/pdf" || record.SizeBytes <= 0 || len(record.SHA256) != 64 {
			return Result{}, fmt.Errorf("document %s has invalid database metadata", record.ID)
		}
		databaseKeys[record.ObjectKey] = struct{}{}
		if _, ok := archiveKeys[record.ObjectKey]; !ok {
			return Result{}, fmt.Errorf("document %s object %s is absent from archive", record.ID, record.ObjectKey)
		}
	}
	for objectKey := range archiveKeys {
		if _, ok := databaseKeys[objectKey]; !ok {
			return Result{}, fmt.Errorf("archived object %s has no database metadata", objectKey)
		}
	}
	store := document.EncryptedLocalStore{Root: root, Keyring: keyring}
	verified := 0
	for _, record := range records {
		if verified >= sample {
			break
		}
		if err = verifyDocumentRecord(store, record); err != nil {
			return Result{}, err
		}
		verified++
	}
	return Result{DatabaseDocuments: len(records), ArchivedObjects: len(archiveKeys), DecryptedSamples: verified}, nil
}

func loadDocumentRecords(ctx context.Context, pool *pgxpool.Pool) ([]documentRecord, error) {
	rows, err := pool.Query(ctx, `
		SELECT id,object_key,sha256,size_bytes,mime_type
		FROM invoice_documents ORDER BY id`)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query restored document metadata: %w", err)
	}
	defer rows.Close()
	records := make([]documentRecord, 0)
	for rows.Next() {
		var record documentRecord
		if err = rows.Scan(&record.ID, &record.ObjectKey, &record.SHA256, &record.SizeBytes, &record.MIME); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func archivedObjectKeys(root string) (map[string]struct{}, error) {
	issued := filepath.Join(root, "issued")
	entries, err := os.ReadDir(issued)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]struct{}{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read restored document archive: %w", err)
	}
	keys := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".pdf.enc") {
			return nil, fmt.Errorf("unexpected entry in restored issued archive: %s", entry.Name())
		}
		keys[filepath.ToSlash(filepath.Join("issued", entry.Name()))] = struct{}{}
	}
	return keys, nil
}

func verifyDocumentRecord(store document.EncryptedLocalStore, record documentRecord) error {
	reader, err := store.OpenAuthorized(record.ObjectKey)
	if err != nil {
		return fmt.Errorf("open restored document %s: %w", record.ID, err)
	}
	defer reader.Close()
	hash := sha256.New()
	header := make([]byte, 5)
	written, err := io.Copy(io.MultiWriter(hash, &prefixWriter{prefix: header}), io.LimitReader(reader, record.SizeBytes+1))
	if err != nil {
		return fmt.Errorf("decrypt restored document %s: %w", record.ID, err)
	}
	if written != record.SizeBytes {
		return fmt.Errorf("restored document %s size mismatch", record.ID)
	}
	if string(header) != "%PDF-" {
		return fmt.Errorf("restored document %s has invalid PDF header", record.ID)
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), record.SHA256) {
		return fmt.Errorf("restored document %s SHA-256 mismatch", record.ID)
	}
	return nil
}

type prefixWriter struct {
	prefix []byte
	offset int
}

func (writer *prefixWriter) Write(body []byte) (int, error) {
	if writer.offset < len(writer.prefix) {
		writer.offset += copy(writer.prefix[writer.offset:], body)
	}
	return len(body), nil
}
