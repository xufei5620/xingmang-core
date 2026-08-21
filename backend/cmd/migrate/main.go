package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/migrate"
)

func main() {
	databaseURL, err := migrationDatabaseURL()
	if err != nil {
		slog.Error("load database credential", "error", err)
		os.Exit(1)
	}
	dir := os.Getenv("MIGRATIONS_DIR")
	if dir == "" {
		dir = "migrations"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	if strings.EqualFold(strings.TrimSpace(os.Getenv("MIGRATION_MODE")), "verify") {
		if err = migrate.Verify(ctx, pool, dir); err != nil {
			slog.Error("verify migrations", "error", err)
			os.Exit(1)
		}
		slog.Info("database migrations verified")
	} else {
		if err = migrate.Up(ctx, pool, dir); err != nil {
			slog.Error("apply migrations", "error", err)
			os.Exit(1)
		}
		slog.Info("database migrations applied")
	}
}

func migrationDatabaseURL() (string, error) {
	production := strings.EqualFold(strings.TrimSpace(os.Getenv("APP_ENV")), "production")
	plain := os.Getenv("DATABASE_URL")
	filePath := strings.TrimSpace(os.Getenv("DATABASE_URL_FILE"))
	if production && plain != "" {
		return "", errors.New("production forbids plaintext DATABASE_URL; use DATABASE_URL_FILE")
	}
	if filePath == "" {
		if plain == "" {
			return "", errors.New("DATABASE_URL_FILE is required")
		}
		return plain, nil
	}
	if !filepath.IsAbs(filePath) {
		return "", errors.New("DATABASE_URL_FILE must be absolute")
	}
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil {
		return "", err
	}
	if len(body) == 0 || len(body) > 64<<10 {
		return "", errors.New("DATABASE_URL_FILE has invalid size")
	}
	body = bytes.TrimSuffix(body, []byte("\r\n"))
	body = bytes.TrimSuffix(body, []byte("\n"))
	if len(body) == 0 || bytes.ContainsAny(body, "\r\n\x00") {
		return "", errors.New("DATABASE_URL_FILE must contain exactly one non-empty line")
	}
	return string(body), nil
}
