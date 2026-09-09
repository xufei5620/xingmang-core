package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/backupverify"
	"invoice-system/backend/internal/document"
	"invoice-system/backend/internal/postgresstore"
)

func main() {
	databaseURLFile := flag.String("database-url-file", "", "absolute owner/runtime database URL file")
	documentRoot := flag.String("document-root", "", "absolute encrypted document root")
	minimumAge := flag.Duration("minimum-age", 24*time.Hour, "minimum unreferenced age before deletion")
	execute := flag.Bool("execute", false, "delete eligible orphans; default is dry-run")
	maintenance := flag.Bool("maintenance-confirmed", false, "confirm API and source ingress are stopped")
	reason := flag.String("reason", "", "bounded audited maintenance reason")
	flag.Parse()
	if flag.NArg() != 0 || !filepath.IsAbs(*databaseURLFile) || !filepath.IsAbs(*documentRoot) || *minimumAge < time.Hour {
		slog.Error("invalid document GC arguments")
		os.Exit(2)
	}
	if *execute && (!*maintenance || strings.TrimSpace(*reason) == "" || len(*reason) > 500 || strings.ContainsAny(*reason, "\r\n\x00")) {
		slog.Error("execute requires maintenance-confirmed and a bounded audit reason")
		os.Exit(2)
	}
	databaseURL, err := readSecret(*databaseURLFile)
	if err != nil {
		slog.Error("read database URL", "error", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	orphans, err := backupverify.FindOrphanDocuments(ctx, pool, *documentRoot, *minimumAge, time.Now().UTC())
	if err != nil {
		slog.Error("scan orphan documents", "error", err)
		os.Exit(1)
	}
	for _, orphan := range orphans {
		slog.Info("eligible orphan document", "object_key", orphan.ObjectKey, "size_bytes", orphan.SizeBytes, "modified_at", orphan.ModifiedAt)
	}
	if !*execute {
		slog.Info("document GC dry-run complete", "eligible", len(orphans))
		return
	}
	requestID, err := randomUUID()
	if err != nil {
		slog.Error("generate maintenance request ID", "error", err)
		os.Exit(1)
	}
	store := document.EncryptedLocalStore{Root: *documentRoot}
	audit := postgresstore.New(pool)
	deleted := 0
	for _, orphan := range orphans {
		referenced, checkErr := backupverify.IsDocumentReferenced(ctx, pool, orphan.ObjectKey)
		if checkErr != nil {
			slog.Error("recheck orphan reference", "error", checkErr)
			os.Exit(1)
		}
		if referenced {
			continue
		}
		if err = audit.RecordMaintenanceAudit(ctx, "document-gc", "document.orphan_gc.intent", orphan.ObjectKey, requestID, "success", *reason); err != nil {
			slog.Error("audit orphan deletion intent", "error", err)
			os.Exit(1)
		}
		if err = store.Delete(orphan.ObjectKey); err != nil {
			_ = audit.RecordMaintenanceAudit(ctx, "document-gc", "document.orphan_gc.failed", orphan.ObjectKey, requestID, "failure", *reason)
			slog.Error("delete orphan document", "error", err)
			os.Exit(1)
		}
		if err = audit.RecordMaintenanceAudit(ctx, "document-gc", "document.orphan_gc.deleted", orphan.ObjectKey, requestID, "success", *reason); err != nil {
			slog.Error("audit orphan deletion", "error", err)
			os.Exit(1)
		}
		deleted++
	}
	slog.Info("document GC execution complete", "eligible", len(orphans), "deleted", deleted, "request_id", requestID)
}

func readSecret(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil || len(body) == 0 || len(body) > 64<<10 {
		return "", errors.New("database URL file has invalid size")
	}
	body = bytes.TrimSuffix(body, []byte("\r\n"))
	body = bytes.TrimSuffix(body, []byte("\n"))
	if len(body) == 0 || bytes.ContainsAny(body, "\r\n\x00") {
		return "", errors.New("database URL file must contain one line")
	}
	return string(body), nil
}

func randomUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
