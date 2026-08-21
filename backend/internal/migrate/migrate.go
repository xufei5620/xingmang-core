package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const advisoryLockID int64 = 0x494e564f494345 // "INVOICE"

// Up applies all *.sql files in dir in lexical order. A PostgreSQL advisory
// lock prevents two application instances from migrating concurrently.
func Up(ctx context.Context, pool *pgxpool.Pool, dir string) error {
	return UpFS(ctx, pool, os.DirFS(dir))
}

// Verify checks that every bundled migration is present with the exact
// checksum. Runtime application roles use this read-only gate and never gain
// DDL privileges or silently migrate production on startup.
func Verify(ctx context.Context, pool *pgxpool.Pool, dir string) error {
	return VerifyFS(ctx, pool, os.DirFS(dir))
}

func VerifyFS(ctx context.Context, pool *pgxpool.Pool, migrations fs.FS) error {
	if pool == nil {
		return errors.New("nil postgres pool")
	}
	files, err := bundledMigrations(migrations)
	if err != nil {
		return err
	}
	expected := make(map[string]string, len(files))
	for _, file := range files {
		expected[file.Name] = file.Checksum
	}
	rows, err := pool.Query(ctx, `SELECT name,checksum FROM schema_migrations ORDER BY name`)
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	defer rows.Close()
	recorded := make([]recordedMigration, 0, len(files))
	for rows.Next() {
		var item recordedMigration
		if err = rows.Scan(&item.Name, &item.Checksum); err != nil {
			return fmt.Errorf("scan applied migration: %w", err)
		}
		recorded = append(recorded, item)
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	return verifyRecordedMigrations(expected, recorded)
}

type recordedMigration struct {
	Name     string
	Checksum string
}

func verifyRecordedMigrations(expected map[string]string, recorded []recordedMigration) error {
	seen := make(map[string]struct{}, len(recorded))
	for _, item := range recorded {
		expectedChecksum, ok := expected[item.Name]
		if !ok {
			return fmt.Errorf("database contains unknown migration %s", item.Name)
		}
		if item.Checksum != expectedChecksum {
			return fmt.Errorf("migration %s checksum changed", item.Name)
		}
		if _, duplicate := seen[item.Name]; duplicate {
			return fmt.Errorf("database records migration %s more than once", item.Name)
		}
		seen[item.Name] = struct{}{}
	}
	for name := range expected {
		if _, ok := seen[name]; !ok {
			return fmt.Errorf("required migration %s is not applied", name)
		}
	}
	return nil
}

// UpFS is the testable migration entry point.
func UpFS(ctx context.Context, pool *pgxpool.Pool, migrations fs.FS) error {
	if pool == nil {
		return errors.New("nil postgres pool")
	}
	files, err := bundledMigrations(migrations)
	if err != nil {
		return err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockID); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, advisoryLockID) }()

	if _, err = conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name TEXT PRIMARY KEY,
			checksum CHAR(64) NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	recordedRows, err := conn.Query(ctx, `SELECT name,checksum FROM schema_migrations ORDER BY name`)
	if err != nil {
		return fmt.Errorf("read applied migrations: %w", err)
	}
	recorded := make([]recordedMigration, 0, len(files))
	for recordedRows.Next() {
		var item recordedMigration
		if err = recordedRows.Scan(&item.Name, &item.Checksum); err != nil {
			recordedRows.Close()
			return fmt.Errorf("scan applied migration: %w", err)
		}
		recorded = append(recorded, item)
	}
	if err = recordedRows.Err(); err != nil {
		recordedRows.Close()
		return fmt.Errorf("read applied migrations: %w", err)
	}
	recordedRows.Close()
	expected := make(map[string]string, len(files))
	applied := make(map[string]string, len(recorded))
	for _, file := range files {
		expected[file.Name] = file.Checksum
	}
	for _, item := range recorded {
		expectedChecksum, ok := expected[item.Name]
		if !ok {
			return fmt.Errorf("database contains unknown migration %s", item.Name)
		}
		if item.Checksum != expectedChecksum {
			return fmt.Errorf("migration %s checksum changed", item.Name)
		}
		applied[item.Name] = item.Checksum
	}
	for _, file := range files {
		if _, ok := applied[file.Name]; ok {
			continue
		}
		tx, beginErr := conn.BeginTx(ctx, pgx.TxOptions{})
		if beginErr != nil {
			return fmt.Errorf("begin migration %s: %w", file.Name, beginErr)
		}
		if _, err = tx.Exec(ctx, string(file.Body)); err != nil {
			_ = tx.Rollback(context.Background())
			return fmt.Errorf("apply migration %s: %w", file.Name, err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(name,checksum) VALUES($1,$2)`, file.Name, file.Checksum); err != nil {
			_ = tx.Rollback(context.Background())
			return fmt.Errorf("record migration %s: %w", file.Name, err)
		}
		if err = tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", file.Name, err)
		}
	}
	return nil
}

type migrationFile struct {
	Name     string
	Body     []byte
	Checksum string
}

func bundledMigrations(migrations fs.FS) ([]migrationFile, error) {
	names, err := migrationNames(migrations)
	if err != nil {
		return nil, err
	}
	files := make([]migrationFile, 0, len(names))
	for _, name := range names {
		body, readErr := fs.ReadFile(migrations, filepath.ToSlash(name))
		if readErr != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, readErr)
		}
		if hasTopLevelTransactionControl(body) {
			return nil, fmt.Errorf("migration %s contains transaction control; UpFS owns the transaction", name)
		}
		sum := sha256.Sum256(body)
		files = append(files, migrationFile{Name: name, Body: body, Checksum: hex.EncodeToString(sum[:])})
	}
	return files, nil
}

func hasTopLevelTransactionControl(body []byte) bool {
	inDollarQuote := false
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if !inDollarQuote {
			statement := strings.ToUpper(strings.TrimSpace(strings.TrimSuffix(trimmed, ";")))
			if statement == "BEGIN" || statement == "COMMIT" || statement == "START TRANSACTION" || statement == "ROLLBACK" {
				return true
			}
		}
		if strings.Count(trimmed, "$$")%2 == 1 {
			inDollarQuote = !inDollarQuote
		}
	}
	return false
}

func migrationNames(migrations fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(migrations, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}
