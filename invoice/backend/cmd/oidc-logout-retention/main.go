package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/oidcretention"
)

func main() {
	databaseURLFile := flag.String("database-url-file", "", "absolute invoice owner database URL file")
	retention := flag.Duration("retention", oidcretention.DefaultRetention, "replay-event retention (minimum 4320h / 180 days)")
	batchSize := flag.Int("batch-size", 500, "bounded delete batch size (1-5000)")
	execute := flag.Bool("execute", false, "delete eligible records; default is dry-run")
	maintenance := flag.Bool("maintenance-confirmed", false, "confirm the approved maintenance window")
	reason := flag.String("reason", "", "bounded audited maintenance reason")
	lockTimeout := flag.Duration("lock-timeout", 5*time.Second, "per-transaction PostgreSQL lock timeout")
	statementTimeout := flag.Duration("statement-timeout", 30*time.Second, "per-transaction PostgreSQL statement timeout")
	flag.Parse()
	if flag.NArg() != 0 || !filepath.IsAbs(*databaseURLFile) {
		slog.Error("database-url-file must be an absolute path and positional arguments are forbidden")
		os.Exit(2)
	}
	databaseURL, err := readSecret(*databaseURLFile)
	if err != nil {
		slog.Error("read owner database URL", "error", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		slog.Error("parse owner database URL", "error", err)
		os.Exit(1)
	}
	config.MaxConns = 1
	config.MinConns = 0
	config.ConnConfig.RuntimeParams["application_name"] = "invoice-oidc-logout-retention"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		slog.Error("open owner database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	result, err := oidcretention.Run(ctx, pool, oidcretention.Options{
		Retention: *retention, BatchSize: *batchSize, Execute: *execute,
		MaintenanceConfirmed: *maintenance, Reason: *reason,
		LockTimeout: *lockTimeout, StatementTimeout: *statementTimeout,
	})
	if err != nil {
		slog.Error("OIDC logout retention failed", "error", err)
		os.Exit(1)
	}
	if !*execute {
		slog.Info("OIDC logout retention dry-run complete", "eligible", result.Eligible,
			"cutoff", result.Cutoff, "retention", retention.String())
		return
	}
	slog.Info("OIDC logout retention execution complete", "eligible_at_start", result.Eligible,
		"deleted", result.Deleted, "batches", result.Batches, "cutoff", result.Cutoff,
		"request_id", result.RequestID)
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
	if len(body) == 0 || bytes.ContainsAny(body, "\r\n\x00") || strings.TrimSpace(string(body)) != string(body) {
		return "", errors.New("database URL file must contain exactly one non-empty unpadded line")
	}
	return string(body), nil
}
