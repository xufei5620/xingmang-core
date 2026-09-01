package cpasnapshot

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

type fakeBackup struct {
	stepErr  error
	closeErr error
}

func (b *fakeBackup) Step(int) (bool, error) { return b.stepErr == nil, b.stepErr }
func (b *fakeBackup) Close() error           { return b.closeErr }

func TestFinishBackupPropagatesStepAndCloseErrors(t *testing.T) {
	t.Parallel()

	stepErr := errors.New("step failed")
	if err := finishBackup(context.Background(), &fakeBackup{stepErr: stepErr}); !errors.Is(err, stepErr) {
		t.Fatalf("finishBackup step error = %v, want %v", err, stepErr)
	}
	closeErr := errors.New("finish failed")
	if err := finishBackup(context.Background(), &fakeBackup{closeErr: closeErr}); !errors.Is(err, closeErr) {
		t.Fatalf("finishBackup close error = %v, want %v", err, closeErr)
	}
}

type retryBackup struct {
	calls int
}

func (b *retryBackup) Step(pages int) (bool, error) {
	b.calls++
	if pages != 256 {
		return false, errors.New("unexpected page batch")
	}
	if b.calls == 1 {
		return false, sqlite3.BUSY
	}
	return true, nil
}
func (b *retryBackup) Close() error { return nil }

func TestFinishBackupRetriesOnlyTemporaryLock(t *testing.T) {
	t.Parallel()

	backup := &retryBackup{}
	if err := finishBackup(context.Background(), backup); err != nil {
		t.Fatalf("finishBackup after BUSY = %v", err)
	}
	if backup.calls != 2 {
		t.Fatalf("Step calls = %d, want 2", backup.calls)
	}
}

type progressBackup struct{ calls int }

func (b *progressBackup) Step(int) (bool, error) {
	b.calls++
	return b.calls == 2, nil
}
func (b *progressBackup) Close() error { return nil }

func TestFinishBackupDoesNotBackoffAfterNormalProgress(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	backup := &progressBackup{}
	if err := finishBackup(ctx, backup); err != nil {
		t.Fatalf("finishBackup normal progress = %v", err)
	}
	if backup.calls != 2 {
		t.Fatalf("Step calls = %d, want 2", backup.calls)
	}
}

func TestPublishCopiesLiveWALTransactionsAndPublishesStandaloneFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sourcePath := filepath.Join(root, "source", "usage.sqlite")
	targetPath := filepath.Join(root, "published", "usage.sqlite")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o700); err != nil {
		t.Fatal(err)
	}

	db := openTestDB(t, sourcePath)
	defer db.Close()
	mustExec(t, db, `PRAGMA journal_mode=WAL`)
	mustCreateRequiredCPASchema(t, db)
	mustExec(t, db, `CREATE TABLE facts(batch INTEGER NOT NULL, item INTEGER NOT NULL, PRIMARY KEY(batch,item))`)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var writerWG sync.WaitGroup
	writerErr := make(chan error, 1)
	writerWG.Add(1)
	go func() {
		defer writerWG.Done()
		for batch := 1; batch <= 40; batch++ {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				writerErr <- err
				return
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO facts(batch,item) VALUES (?,1),(?,2)`, batch, batch); err != nil {
				_ = tx.Rollback()
				writerErr <- err
				return
			}
			if err = tx.Commit(); err != nil {
				writerErr <- err
				return
			}
		}
		writerErr <- nil
	}()

	seenGeneration := map[string]struct{}{}
	for attempt := 0; attempt < 12; attempt++ {
		metadata, err := Publish(ctx, Options{
			SourcePath: sourcePath,
			TargetPath: targetPath,
			GroupID:    -1,
		})
		if err != nil {
			t.Fatalf("Publish attempt %d: %v", attempt, err)
		}
		if metadata.Generation == "" || metadata.ObservedAt.IsZero() {
			t.Fatalf("metadata = %+v, want generation and observed time", metadata)
		}
		if _, duplicate := seenGeneration[metadata.Generation]; duplicate {
			t.Fatalf("generation %q was reused", metadata.Generation)
		}
		seenGeneration[metadata.Generation] = struct{}{}

		verified, err := Verify(ctx, targetPath)
		if err != nil {
			t.Fatalf("Verify attempt %d: %v", attempt, err)
		}
		if verified != metadata {
			t.Fatalf("Verify metadata = %+v, Publish metadata = %+v", verified, metadata)
		}
		assertCompleteBatches(t, targetPath)
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, err := os.Stat(targetPath + suffix); !os.IsNotExist(err) {
				t.Fatalf("published snapshot left %s companion: %v", suffix, err)
			}
		}
	}
	writerWG.Wait()
	if err := <-writerErr; err != nil {
		t.Fatalf("WAL writer: %v", err)
	}

	if _, err := Publish(ctx, Options{SourcePath: sourcePath, TargetPath: targetPath, GroupID: -1}); err != nil {
		t.Fatalf("final Publish: %v", err)
	}
	assertCompleteBatches(t, targetPath)

	final := openTestDBReadOnly(t, targetPath)
	defer final.Close()
	var rows int
	if err := final.QueryRow(`SELECT COUNT(*) FROM facts`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 80 {
		t.Fatalf("final row count = %d, want 80", rows)
	}
}

func TestPublishFailureNeverReplacesLastGoodSnapshot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.sqlite")
	targetPath := filepath.Join(root, "published", "usage.sqlite")
	db := openTestDB(t, sourcePath)
	mustCreateRequiredCPASchema(t, db)
	mustExec(t, db, `CREATE TABLE facts(id INTEGER PRIMARY KEY)`)
	mustExec(t, db, `INSERT INTO facts(id) VALUES (1)`)
	db.Close()

	ctx := context.Background()
	if _, err := Publish(ctx, Options{SourcePath: sourcePath, TargetPath: targetPath, GroupID: -1}); err != nil {
		t.Fatalf("initial Publish: %v", err)
	}
	before, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}

	badSource := filepath.Join(root, "missing.sqlite")
	if _, err := Publish(ctx, Options{SourcePath: badSource, TargetPath: targetPath, GroupID: -1}); err == nil {
		t.Fatal("Publish with missing source succeeded")
	}
	after, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed publish changed the last good snapshot")
	}
}

func TestVerifyRejectsOrdinaryDatabaseWithoutSnapshotMetadata(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "ordinary.sqlite")
	db := openTestDB(t, path)
	mustExec(t, db, `CREATE TABLE facts(id INTEGER PRIMARY KEY)`)
	db.Close()

	if _, err := Verify(context.Background(), path); err == nil {
		t.Fatal("Verify accepted a database without xingmang snapshot metadata")
	}
}

func TestPublishRejectsSourceMetadataCollision(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.sqlite")
	targetPath := filepath.Join(root, "published", "usage.sqlite")
	db := openTestDB(t, sourcePath)
	mustCreateRequiredCPASchema(t, db)
	mustExec(t, db, `CREATE TABLE xingmang_snapshot_metadata_v1(singleton_id INTEGER PRIMARY KEY)`)
	db.Close()

	if _, err := Publish(context.Background(), Options{SourcePath: sourcePath, TargetPath: targetPath, GroupID: -1}); err == nil {
		t.Fatal("Publish replaced a source-owned table that collides with snapshot metadata")
	}
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Fatalf("collision published a target: %v", err)
	}
}

func TestPublishRejectsDatabaseWithoutRequiredCPASchema(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.sqlite")
	targetPath := filepath.Join(root, "published", "usage.sqlite")
	db := openTestDB(t, sourcePath)
	mustExec(t, db, `CREATE TABLE unrelated(id INTEGER PRIMARY KEY)`)
	db.Close()

	if _, err := Publish(context.Background(), Options{SourcePath: sourcePath, TargetPath: targetPath, GroupID: -1}); err == nil {
		t.Fatal("Publish accepted a database without the required CPA tables")
	}
}

func TestPublishKeepsThePreviousGenerationOutsideConsumerDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sourcePath := filepath.Join(root, "source", "usage.sqlite")
	targetPath := filepath.Join(root, "published", "usage.sqlite")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o700); err != nil {
		t.Fatal(err)
	}
	db := openTestDB(t, sourcePath)
	mustCreateRequiredCPASchema(t, db)
	mustExec(t, db, `CREATE TABLE facts(id INTEGER PRIMARY KEY)`)
	mustExec(t, db, `INSERT INTO facts(id) VALUES(1)`)

	ctx := context.Background()
	first, err := Publish(ctx, Options{SourcePath: sourcePath, TargetPath: targetPath, GroupID: -1})
	if err != nil {
		t.Fatalf("first Publish: %v", err)
	}
	mustExec(t, db, `INSERT INTO facts(id) VALUES(2)`)
	second, err := Publish(ctx, Options{SourcePath: sourcePath, TargetPath: targetPath, GroupID: -1})
	if err != nil {
		t.Fatalf("second Publish: %v", err)
	}
	db.Close()
	if second.Generation == first.Generation {
		t.Fatal("second publication reused the first generation")
	}

	previousPath := filepath.Join(root, "history", first.Generation+".sqlite")
	previous, err := Verify(ctx, previousPath)
	if err != nil {
		t.Fatalf("Verify previous generation: %v", err)
	}
	if previous != first {
		t.Fatalf("previous metadata = %+v, want first %+v", previous, first)
	}
	previousDB := openTestDBReadOnly(t, previousPath)
	var rows int
	if err = previousDB.QueryRow(`SELECT COUNT(*) FROM facts`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("previous facts = %d, want 1", rows)
	}
	previousDB.Close()

	for id := 3; id <= 5; id++ {
		db = openTestDB(t, sourcePath)
		mustExec(t, db, `INSERT INTO facts(id) VALUES(?)`, id)
		db.Close()
		if _, err = Publish(ctx, Options{SourcePath: sourcePath, TargetPath: targetPath, GroupID: -1}); err != nil {
			t.Fatalf("Publish generation %d: %v", id, err)
		}
	}
	historyEntries, err := os.ReadDir(filepath.Join(root, "history"))
	if err != nil {
		t.Fatal(err)
	}
	if len(historyEntries) != 2 {
		t.Fatalf("history entries = %d, want bounded retention of 2", len(historyEntries))
	}
	if _, err = os.Stat(previousPath); !os.IsNotExist(err) {
		t.Fatalf("oldest generation was not retired: %v", err)
	}
}

func TestPublishLockRejectsConcurrentProducer(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	release, err := acquirePublishLock(filepath.Join(root, ".producer.lock"))
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if _, err = acquirePublishLock(filepath.Join(root, ".producer.lock")); err == nil {
		release()
		t.Fatal("second producer acquired the same lock")
	}
	release()
	if releaseAgain, retryErr := acquirePublishLock(filepath.Join(root, ".producer.lock")); retryErr != nil {
		t.Fatalf("lock after release: %v", retryErr)
	} else {
		releaseAgain()
	}
}

func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func openTestDBReadOnly(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func mustExec(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatalf("exec %q: %v", statement, err)
	}
}

func mustCreateRequiredCPASchema(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `CREATE TABLE usage_events(timestamp_ms INTEGER, provider TEXT, model TEXT, api_key_hash TEXT)`)
	mustExec(t, db, `CREATE TABLE model_prices(
		model TEXT PRIMARY KEY,
		prompt_per_1m REAL,
		completion_per_1m REAL,
		cache_per_1m REAL,
		cache_read_per_1m REAL,
		cache_creation_per_1m REAL
	)`)
	mustExec(t, db, `CREATE TABLE api_key_aliases(api_key_hash TEXT PRIMARY KEY, alias TEXT)`)
	mustExec(t, db, `CREATE TABLE codex_inspection_runs(id INTEGER PRIMARY KEY, started_at_ms INTEGER NOT NULL)`)
	mustExec(t, db, `CREATE TABLE codex_inspection_results(
		run_id INTEGER NOT NULL,
		account_key TEXT,
		display_account TEXT,
		provider TEXT,
		disabled INTEGER,
		status TEXT,
		state TEXT,
		action TEXT,
		action_reason TEXT
	)`)
}

func assertCompleteBatches(t *testing.T, path string) {
	t.Helper()
	db := openTestDBReadOnly(t, path)
	defer db.Close()
	var incomplete int
	if err := db.QueryRow(`SELECT COUNT(*) FROM (SELECT batch FROM facts GROUP BY batch HAVING COUNT(*) <> 2)`).Scan(&incomplete); err != nil {
		t.Fatal(err)
	}
	if incomplete != 0 {
		t.Fatalf("snapshot contains %d partially copied transactions", incomplete)
	}
}
