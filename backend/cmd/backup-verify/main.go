package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/backupverify"
	"invoice-system/backend/internal/migrate"
	"invoice-system/backend/internal/securefields"
)

func main() {
	databaseURLFile := flag.String("database-url-file", "", "absolute path to isolated restored database URL secret")
	documentRoot := flag.String("document-root", "", "absolute restored document archive root")
	keyringFile := flag.String("field-keyring-file", "", "absolute offline-restored field keyring path")
	migrationsDir := flag.String("migrations-dir", "/app/migrations", "bundled migration directory")
	sample := flag.Int("sample", 10, "number of encrypted documents to decrypt and hash (1..1000)")
	flag.Parse()
	if flag.NArg() != 0 {
		slog.Error("backup verification does not accept positional arguments")
		os.Exit(2)
	}
	for name, value := range map[string]string{
		"database URL file": *databaseURLFile, "document root": *documentRoot,
		"field keyring file": *keyringFile, "migrations directory": *migrationsDir,
	} {
		if !filepath.IsAbs(value) {
			slog.Error("backup verification path must be absolute", "field", name)
			os.Exit(2)
		}
	}
	if *sample <= 0 || *sample > 1000 {
		slog.Error("sample must be between 1 and 1000")
		os.Exit(2)
	}
	databaseURL, err := readOneLineSecret(*databaseURLFile)
	if err != nil {
		slog.Error("read restored database credential", "error", err)
		os.Exit(1)
	}
	keyring, err := securefields.LoadKeyringFile(*keyringFile)
	if err != nil {
		slog.Error("load offline field keyring", "error", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		slog.Error("open restored database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		slog.Error("ping restored database", "error", err)
		os.Exit(1)
	}
	if err = migrate.Verify(ctx, pool, *migrationsDir); err != nil {
		slog.Error("restored database migration set mismatch", "error", err)
		os.Exit(1)
	}
	result, err := backupverify.VerifyDocuments(ctx, pool, *documentRoot, keyring, *sample)
	if err != nil {
		slog.Error("restored document verification failed", "error", err)
		os.Exit(1)
	}
	slog.Info("backup verification passed",
		"database_documents", result.DatabaseDocuments,
		"archived_objects", result.ArchivedObjects,
		"decrypted_samples", result.DecryptedSamples)
}

func readOneLineSecret(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("secret path must be absolute")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil {
		return "", err
	}
	if len(body) == 0 || len(body) > 64<<10 {
		return "", errors.New("secret file has invalid size")
	}
	body = bytes.TrimSuffix(body, []byte("\r\n"))
	body = bytes.TrimSuffix(body, []byte("\n"))
	if len(body) == 0 || bytes.ContainsAny(body, "\r\n\x00") {
		return "", fmt.Errorf("secret file must contain exactly one non-empty line")
	}
	return string(body), nil
}
