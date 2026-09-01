// Package cpasnapshot publishes a consistent, standalone read copy of the
// cpa-manager-plus SQLite database. It is a host lifecycle boundary: platform
// API/worker containers consume only the published file and never the active
// WAL directory.
package cpasnapshot

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqlite3driver "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

const (
	metadataTable = "xingmang_snapshot_metadata_v1"
	busyTimeoutMS = 5000
)

// Options identifies the one source database and atomic publication target.
type Options struct {
	SourcePath string
	TargetPath string
	// GroupID is applied to the target directory and file. Use -1 to leave
	// ownership unchanged (portable tests); production uses 10001.
	GroupID int
}

// Metadata is embedded in the published SQLite file and is the only freshness
// source used by CPA connector reads.
type Metadata struct {
	Generation string
	ObservedAt time.Time
}

// RowQueryer is the narrow database/sql surface needed to read embedded
// snapshot metadata from the same connection/pool used for business queries.
type RowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ReadMetadata reads and validates the one metadata row from an already-open
// snapshot. Consumers use this instead of stat/mtime so data and watermark are
// bound to the same SQLite file descriptor.
func ReadMetadata(ctx context.Context, queryer RowQueryer) (Metadata, error) {
	if queryer == nil {
		return Metadata{}, errors.New("cpa snapshot: metadata queryer is nil")
	}
	return readMetadata(ctx, queryer)
}

// Publish uses SQLite's online-backup API and atomically replaces TargetPath
// only after the copied database is standalone and verified.
func Publish(ctx context.Context, opts Options) (Metadata, error) {
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}
	source, target, err := validatePaths(opts.SourcePath, opts.TargetPath)
	if err != nil {
		return Metadata{}, err
	}
	if err = validateSource(source); err != nil {
		return Metadata{}, err
	}
	targetDir := filepath.Dir(target)
	if err = ensureTargetDirectory(targetDir, opts.GroupID); err != nil {
		return Metadata{}, err
	}
	unlock, err := acquirePublishLock(filepath.Join(filepath.Dir(targetDir), ".producer.lock"))
	if err != nil {
		return Metadata{}, err
	}
	defer unlock()
	if err = cleanupTemporaryFiles(targetDir, ".usage.sqlite.tmp-"); err != nil {
		return Metadata{}, err
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, statErr := os.Lstat(target + suffix); statErr == nil {
			return Metadata{}, fmt.Errorf("cpa snapshot: published target has forbidden companion %s", suffix)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return Metadata{}, fmt.Errorf("cpa snapshot: inspect target companion: %w", statErr)
		}
	}

	temp, err := os.CreateTemp(targetDir, ".usage.sqlite.tmp-*")
	if err != nil {
		return Metadata{}, fmt.Errorf("cpa snapshot: create temporary target: %w", err)
	}
	tempPath := temp.Name()
	if closeErr := temp.Close(); closeErr != nil {
		_ = os.Remove(tempPath)
		return Metadata{}, fmt.Errorf("cpa snapshot: close temporary target: %w", closeErr)
	}
	published := false
	defer func() {
		if !published {
			_ = os.Remove(tempPath)
			_ = os.Remove(tempPath + "-wal")
			_ = os.Remove(tempPath + "-shm")
			_ = os.Remove(tempPath + "-journal")
		}
	}()

	if err = onlineBackup(ctx, source, tempPath); err != nil {
		return Metadata{}, err
	}
	metadata, err := finalizeTemporary(ctx, tempPath)
	if err != nil {
		return Metadata{}, err
	}
	if err = os.Chmod(tempPath, 0o640); err != nil {
		return Metadata{}, fmt.Errorf("cpa snapshot: chmod temporary target: %w", err)
	}
	if opts.GroupID >= 0 {
		if err = os.Chown(tempPath, 0, opts.GroupID); err != nil {
			return Metadata{}, fmt.Errorf("cpa snapshot: chown temporary target: %w", err)
		}
	}
	if err = syncFile(tempPath); err != nil {
		return Metadata{}, err
	}
	historyDir, err := preservePrevious(ctx, target, opts.GroupID)
	if err != nil {
		return Metadata{}, err
	}
	if err = os.Rename(tempPath, target); err != nil {
		return Metadata{}, fmt.Errorf("cpa snapshot: atomic rename: %w", err)
	}
	published = true
	if err = syncDirectory(targetDir); err != nil {
		return Metadata{}, fmt.Errorf("cpa snapshot: publish durability outcome unknown after atomic rename; generation %s remains installed: %w", metadata.Generation, err)
	}
	verified, err := Verify(ctx, target)
	if err != nil {
		return Metadata{}, fmt.Errorf("cpa snapshot: published generation %s is installed but post-rename verification failed: %w", metadata.Generation, err)
	}
	if verified != metadata {
		return Metadata{}, errors.New("cpa snapshot: published metadata changed during rename")
	}
	if historyDir != "" {
		if err = cleanupHistory(historyDir, 2); err != nil {
			return Metadata{}, fmt.Errorf("cpa snapshot: generation %s is published but history cleanup failed: %w", metadata.Generation, err)
		}
	}
	return metadata, nil
}

func preservePrevious(ctx context.Context, target string, groupID int) (string, error) {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("cpa snapshot: inspect previous target: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("cpa snapshot: previous target is not a regular non-symlink file")
	}
	metadata, err := Verify(ctx, target)
	if err != nil {
		return "", fmt.Errorf("cpa snapshot: previous target is not a verified snapshot: %w", err)
	}
	historyDir := filepath.Join(filepath.Dir(filepath.Dir(target)), "history")
	if err = os.MkdirAll(historyDir, 0o700); err != nil {
		return "", fmt.Errorf("cpa snapshot: create history directory: %w", err)
	}
	historyInfo, err := os.Lstat(historyDir)
	if err != nil || !historyInfo.IsDir() || historyInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("cpa snapshot: history path is not a real directory")
	}
	if err = os.Chmod(historyDir, 0o700); err != nil {
		return "", fmt.Errorf("cpa snapshot: chmod history directory: %w", err)
	}
	if groupID >= 0 {
		if err = os.Chown(historyDir, 0, 0); err != nil {
			return "", fmt.Errorf("cpa snapshot: chown history directory: %w", err)
		}
	}
	historyPath := filepath.Join(historyDir, metadata.Generation+".sqlite")
	if existing, statErr := os.Lstat(historyPath); statErr == nil {
		if !existing.Mode().IsRegular() || existing.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, existing) {
			return "", errors.New("cpa snapshot: history generation collides with a different file")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", fmt.Errorf("cpa snapshot: inspect history generation: %w", statErr)
	} else if err = os.Link(target, historyPath); err != nil {
		return "", fmt.Errorf("cpa snapshot: preserve previous generation: %w", err)
	}
	if err = syncDirectory(historyDir); err != nil {
		return "", fmt.Errorf("cpa snapshot: fsync history directory: %w", err)
	}
	return historyDir, nil
}

func cleanupHistory(historyDir string, keep int) error {
	entries, err := os.ReadDir(historyDir)
	if err != nil {
		return err
	}
	type generationFile struct {
		path string
		info os.FileInfo
	}
	var known []generationFile
	for _, entry := range entries {
		name := entry.Name()
		if len(name) != 39 || !strings.HasSuffix(name, ".sqlite") {
			continue
		}
		if decoded, decodeErr := hex.DecodeString(strings.TrimSuffix(name, ".sqlite")); decodeErr != nil || len(decoded) != 16 {
			continue
		}
		path := filepath.Join(historyDir, name)
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("cpa snapshot: known history path is not a regular file")
		}
		known = append(known, generationFile{path: path, info: info})
	}
	sort.Slice(known, func(i, j int) bool { return known[i].info.ModTime().After(known[j].info.ModTime()) })
	if len(known) <= keep {
		return syncDirectory(historyDir)
	}
	for _, old := range known[keep:] {
		if err = os.Remove(old.path); err != nil {
			return err
		}
	}
	return syncDirectory(historyDir)
}

// Verify requires the exact metadata contract, DELETE journal mode and an
// exact quick_check result. It never creates or repairs files.
func Verify(ctx context.Context, path string) (Metadata, error) {
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "." || !filepath.IsAbs(clean) || strings.ContainsAny(clean, "\x00?#%") {
		return Metadata{}, errors.New("cpa snapshot: verify path must be an absolute safe path")
	}
	db, err := sql.Open("sqlite3", readOnlyDSN(clean))
	if err != nil {
		return Metadata{}, fmt.Errorf("cpa snapshot: open published target: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err = db.PingContext(ctx); err != nil {
		return Metadata{}, fmt.Errorf("cpa snapshot: ping published target: %w", err)
	}

	var journal string
	if err = db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journal); err != nil {
		return Metadata{}, fmt.Errorf("cpa snapshot: read journal mode: %w", err)
	}
	if !strings.EqualFold(journal, "delete") {
		return Metadata{}, fmt.Errorf("cpa snapshot: journal mode %q is not DELETE", journal)
	}
	if err = requireQuickCheck(ctx, db); err != nil {
		return Metadata{}, err
	}
	if err = requireCPASchema(ctx, db); err != nil {
		return Metadata{}, err
	}
	return ReadMetadata(ctx, db)
}

func validatePaths(sourcePath, targetPath string) (string, string, error) {
	source := filepath.Clean(strings.TrimSpace(sourcePath))
	target := filepath.Clean(strings.TrimSpace(targetPath))
	if source == "." || target == "." || !filepath.IsAbs(source) || !filepath.IsAbs(target) {
		return "", "", errors.New("cpa snapshot: source and target must be absolute paths")
	}
	if source == target || filepath.Dir(source) == filepath.Dir(target) {
		return "", "", errors.New("cpa snapshot: source and target must be in separate directories")
	}
	if strings.ContainsAny(source, "\x00?#%") || strings.ContainsAny(target, "\x00?#%") {
		return "", "", errors.New("cpa snapshot: paths contain SQLite URI control characters")
	}
	if filepath.Base(target) != "usage.sqlite" {
		return "", "", errors.New("cpa snapshot: target file name must be usage.sqlite")
	}
	return source, target, nil
}

func validateSource(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("cpa snapshot: source database unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() == 0 {
		return errors.New("cpa snapshot: source database must be a non-empty regular non-symlink file")
	}
	return nil
}

func ensureTargetDirectory(path string, groupID int) error {
	if err := os.MkdirAll(path, 0o750); err != nil {
		return fmt.Errorf("cpa snapshot: create target directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("cpa snapshot: inspect target directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("cpa snapshot: target directory must be a real directory")
	}
	if err = os.Chmod(path, 0o750); err != nil {
		return fmt.Errorf("cpa snapshot: chmod target directory: %w", err)
	}
	if groupID >= 0 {
		if err = os.Chown(path, 0, groupID); err != nil {
			return fmt.Errorf("cpa snapshot: chown target directory: %w", err)
		}
	}
	return nil
}

func cleanupTemporaryFiles(directory, prefix string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("cpa snapshot: list temporary files: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return fmt.Errorf("cpa snapshot: inspect stale temporary file: %w", statErr)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("cpa snapshot: stale temporary path is not a regular file")
		}
		if err = os.Remove(path); err != nil {
			return fmt.Errorf("cpa snapshot: remove stale temporary file: %w", err)
		}
	}
	return nil
}

func onlineBackup(ctx context.Context, sourcePath, destinationPath string) error {
	db, err := sql.Open("sqlite3", readOnlyDSN(sourcePath))
	if err != nil {
		return fmt.Errorf("cpa snapshot: open source: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("cpa snapshot: acquire source connection: %w", err)
	}
	defer conn.Close()
	destinationURI := "file:" + filepath.ToSlash(destinationPath)
	if err = conn.Raw(func(raw any) error {
		driverConn, ok := raw.(sqlite3driver.Conn)
		if !ok {
			return errors.New("unexpected sqlite driver connection")
		}
		rawConn := driverConn.Raw()
		oldInterrupt := rawConn.SetInterrupt(ctx)
		defer rawConn.SetInterrupt(oldInterrupt)
		backup, initErr := rawConn.BackupInit("main", destinationURI)
		if initErr != nil {
			return initErr
		}
		return finishBackup(ctx, backup)
	}); err != nil {
		return fmt.Errorf("cpa snapshot: online backup: %w", err)
	}
	return nil
}

type backupOperation interface {
	Step(int) (bool, error)
	Close() error
}

func finishBackup(ctx context.Context, backup backupOperation) error {
	if backup == nil {
		return errors.New("cpa snapshot: backup handle is nil")
	}
	for {
		done, stepErr := backup.Step(256)
		if stepErr == nil {
			if done {
				return backup.Close()
			}
			continue
		}
		if stepErr != nil && !errors.Is(stepErr, sqlite3.BUSY) && !errors.Is(stepErr, sqlite3.LOCKED) {
			return errors.Join(stepErr, backup.Close())
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(err, backup.Close())
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(ctx.Err(), backup.Close())
		case <-timer.C:
		}
	}
}

func finalizeTemporary(ctx context.Context, path string) (Metadata, error) {
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?mode=rw")
	if err != nil {
		return Metadata{}, fmt.Errorf("cpa snapshot: open temporary target: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	var journal string
	if err = db.QueryRowContext(ctx, `PRAGMA journal_mode=DELETE`).Scan(&journal); err != nil {
		return Metadata{}, fmt.Errorf("cpa snapshot: set DELETE journal: %w", err)
	}
	if !strings.EqualFold(journal, "delete") {
		return Metadata{}, fmt.Errorf("cpa snapshot: target refused DELETE journal mode: %q", journal)
	}
	if err = requireCPASchema(ctx, db); err != nil {
		return Metadata{}, err
	}
	var metadataCollision int
	if err = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, metadataTable).Scan(&metadataCollision); err != nil {
		return Metadata{}, fmt.Errorf("cpa snapshot: inspect metadata collision: %w", err)
	}
	if metadataCollision != 0 {
		return Metadata{}, errors.New("cpa snapshot: source database owns the reserved metadata object")
	}
	generation, err := randomGeneration()
	if err != nil {
		return Metadata{}, err
	}
	observed := time.UnixMilli(time.Now().UTC().UnixMilli()).UTC()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Metadata{}, fmt.Errorf("cpa snapshot: begin metadata transaction: %w", err)
	}
	if _, err = tx.ExecContext(ctx, `CREATE TABLE `+metadataTable+` (
			singleton_id INTEGER PRIMARY KEY CHECK(singleton_id = 1),
			generation TEXT NOT NULL CHECK(length(generation) = 32),
			observed_at_ms INTEGER NOT NULL CHECK(observed_at_ms > 0)
		) STRICT`); err != nil {
		_ = tx.Rollback()
		return Metadata{}, fmt.Errorf("cpa snapshot: create metadata: %w", err)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO `+metadataTable+`(singleton_id,generation,observed_at_ms) VALUES(1,?,?)`, generation, observed.UnixMilli())
	}
	if err != nil {
		_ = tx.Rollback()
		return Metadata{}, fmt.Errorf("cpa snapshot: write metadata: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return Metadata{}, fmt.Errorf("cpa snapshot: commit metadata: %w", err)
	}
	if err = requireQuickCheck(ctx, db); err != nil {
		return Metadata{}, err
	}
	if err = db.Close(); err != nil {
		return Metadata{}, fmt.Errorf("cpa snapshot: close temporary target: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, statErr := os.Stat(path + suffix); !errors.Is(statErr, os.ErrNotExist) {
			return Metadata{}, fmt.Errorf("cpa snapshot: temporary target left companion %s", suffix)
		}
	}
	return Metadata{Generation: generation, ObservedAt: observed}, nil
}

func readMetadata(ctx context.Context, db RowQueryer) (Metadata, error) {
	var generation string
	var observedMS int64
	err := db.QueryRowContext(ctx, `SELECT generation, observed_at_ms FROM `+metadataTable+` WHERE singleton_id=1`).Scan(&generation, &observedMS)
	if err != nil {
		return Metadata{}, fmt.Errorf("cpa snapshot: metadata missing or unreadable: %w", err)
	}
	decoded, err := hex.DecodeString(generation)
	if err != nil || len(decoded) != 16 || observedMS <= 0 {
		return Metadata{}, errors.New("cpa snapshot: metadata is malformed")
	}
	observed := time.UnixMilli(observedMS).UTC()
	if observed.After(time.Now().UTC().Add(5 * time.Minute)) {
		return Metadata{}, errors.New("cpa snapshot: metadata is future-dated")
	}
	var count int
	if err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+metadataTable).Scan(&count); err != nil || count != 1 {
		return Metadata{}, errors.New("cpa snapshot: metadata must contain exactly one row")
	}
	return Metadata{Generation: generation, ObservedAt: observed}, nil
}

func requireQuickCheck(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `PRAGMA quick_check`)
	if err != nil {
		return fmt.Errorf("cpa snapshot: quick_check failed: %w", err)
	}
	defer rows.Close()
	results := 0
	for rows.Next() {
		var result string
		if err = rows.Scan(&result); err != nil {
			return fmt.Errorf("cpa snapshot: read quick_check result: %w", err)
		}
		results++
		if result != "ok" {
			return fmt.Errorf("cpa snapshot: quick_check result %q", result)
		}
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("cpa snapshot: iterate quick_check: %w", err)
	}
	if results != 1 {
		return fmt.Errorf("cpa snapshot: quick_check returned %d rows", results)
	}
	return nil
}

func requireCPASchema(ctx context.Context, db *sql.DB) error {
	required := map[string][]string{
		"usage_events":             {"timestamp_ms", "provider", "model", "api_key_hash"},
		"model_prices":             {"model", "prompt_per_1m", "completion_per_1m", "cache_per_1m", "cache_read_per_1m", "cache_creation_per_1m"},
		"api_key_aliases":          {"api_key_hash", "alias"},
		"codex_inspection_runs":    {"id", "started_at_ms"},
		"codex_inspection_results": {"run_id", "account_key", "display_account", "provider", "disabled", "status", "state", "action", "action_reason"},
	}
	for table, columns := range required {
		rows, err := db.QueryContext(ctx, `PRAGMA table_info('`+table+`')`)
		if err != nil {
			return fmt.Errorf("cpa snapshot: inspect required table %s: %w", table, err)
		}
		seen := map[string]struct{}{}
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue any
			if err = rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				return fmt.Errorf("cpa snapshot: read required table %s: %w", table, err)
			}
			seen[name] = struct{}{}
		}
		rowsErr := rows.Err()
		rows.Close()
		if rowsErr != nil {
			return fmt.Errorf("cpa snapshot: iterate required table %s: %w", table, rowsErr)
		}
		for _, column := range columns {
			if _, ok := seen[column]; !ok {
				return fmt.Errorf("cpa snapshot: required CPA schema is missing %s.%s", table, column)
			}
		}
	}
	return nil
}

func randomGeneration() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("cpa snapshot: generate identifier: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func readOnlyDSN(path string) string {
	return fmt.Sprintf("file:%s?mode=ro&_pragma=query_only(ON)&_pragma=busy_timeout(%d)", filepath.ToSlash(path), busyTimeoutMS)
}

func syncFile(path string) error {
	// Windows requires a writable handle for FlushFileBuffers; Linux accepts
	// either, but O_RDWR keeps the durability path identical in tests and
	// production. This handle is opened only on the unpublished destination.
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("cpa snapshot: open target for fsync: %w", err)
	}
	defer file.Close()
	if err = file.Sync(); err != nil {
		return fmt.Errorf("cpa snapshot: fsync target: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("cpa snapshot: open target directory for fsync: %w", err)
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return fmt.Errorf("cpa snapshot: fsync target directory: %w", err)
	}
	return nil
}
