package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	rivermirror "github.com/xufei5620/xingmang-platform/db/migrations/river"
)

const riverMigrationLine = "main"

// NewMigrator constructs the pinned River migrator after checking that the
// reviewable SQL mirror matches River's embedded driver bundle. A nil pool is
// accepted here so unit tests can inspect the migration set without connecting
// to Postgres; Migrate still rejects a nil pool before execution.
func NewMigrator(pool *pgxpool.Pool, logger *slog.Logger) (*rivermigrate.Migrator[pgx.Tx], error) {
	driver := riverpgxv5.New(pool)
	if err := validateMigrationMirror(driver.GetMigrationFS(riverMigrationLine)); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = structuredDefaultLogger()
	}
	return rivermigrate.New(driver, &rivermigrate.Config{Logger: logger})
}

func validateMigrationMirror(upstream fs.FS) error {
	paths, err := fs.Glob(upstream, "migration/main/*.sql")
	if err != nil {
		return fmt.Errorf("river migration bundle: list upstream files: %w", err)
	}
	if len(paths) != 14 {
		return fmt.Errorf("river migration bundle: got %d files, want 14", len(paths))
	}
	localPaths, err := fs.Glob(rivermirror.FS, "*.sql")
	if err != nil {
		return fmt.Errorf("river migration mirror: list files: %w", err)
	}
	if len(localPaths) != len(paths) {
		return fmt.Errorf("river migration mirror: got %d files, want %d", len(localPaths), len(paths))
	}
	for _, upstreamPath := range paths {
		name := path.Base(upstreamPath)
		upstreamSQL, err := fs.ReadFile(upstream, upstreamPath)
		if err != nil {
			return fmt.Errorf("river migration bundle: read %s: %w", upstreamPath, err)
		}
		mirrorSQL, err := fs.ReadFile(rivermirror.FS, name)
		if err != nil {
			return fmt.Errorf("river migration mirror: missing %s: %w", name, err)
		}
		if !bytes.Equal(bytes.TrimRight(upstreamSQL, "\r\n"), bytes.TrimRight(mirrorSQL, "\r\n")) {
			return fmt.Errorf("river migration mirror: %s differs from pinned River bundle", name)
		}
	}
	return nil
}

// Migrate applies River's versioned OSS main-line migrations. It is an
// explicit lifecycle operation and is intentionally not called by NewClient
// or normal worker startup.
func Migrate(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	if pool == nil {
		return errors.New("jobs: postgres pool is nil")
	}
	migrator, err := NewMigrator(pool, logger)
	if err != nil {
		return err
	}
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	return err
}
