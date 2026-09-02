package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/migrate"
	"invoice-system/backend/internal/postgresstore"
	"invoice-system/backend/internal/testdb"
)

func applyResultFixture() postgresstore.PreAnchorUsageRepairResult {
	return postgresstore.PreAnchorUsageRepairResult{
		Applied: true,
		Accounts: []postgresstore.PreAnchorUsageRepairAccount{{
			ExternalAccountID: "30000000-0000-4000-8000-000000000079", SourceInstanceID: "10000000-0000-4000-8000-000000000079",
			SourceGapFreezesResolved: 2, EventDeadFreezesResolved: 1, EventsRequeued: 3, Reactivated: true,
		}},
		TotalSourceGapFreezesResolved: 2, TotalEventDeadFreezesResolved: 1, TotalEventsRequeued: 3,
	}
}

// setupRepairCLIEnv migrates a fresh isolated schema (mirroring
// postgresstore's own integration test helpers, which this package cannot
// import directly -- they are unexported test-file helpers in a different
// package) and writes the two secret files run() reads, returning their
// paths and the migrations directory to pass in.
func setupRepairCLIEnv(t *testing.T) (databaseURLFile, keyringFile, migrationsDir string) {
	t.Helper()
	databaseURL := testdb.URL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	migrationsDir = filepath.Join("..", "..", "migrations")
	if err = migrate.Up(ctx, pool, migrationsDir); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	databaseURLFile = filepath.Join(dir, "database-url")
	if err = os.WriteFile(databaseURLFile, []byte(databaseURL), 0o600); err != nil {
		t.Fatal(err)
	}
	keyringFile = filepath.Join(dir, "keyring.json")
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	index := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("i", 32)))
	keyringBody := `{"current_key_id":"2026-09","encryption_keys":{"2026-09":"` + key + `"},"index_key":"` + index + `"}`
	if err = os.WriteFile(keyringFile, []byte(keyringBody), 0o600); err != nil {
		t.Fatal(err)
	}
	return databaseURLFile, keyringFile, migrationsDir
}

// TestRunDryRunAgainstEmptyDatabaseReportsNothing is the CLI wiring's
// smoke test: against a freshly migrated, empty database (no incident
// state), run() must succeed and report zero accounts affected -- proving
// the flag parsing, secret loading, migration check and store call all wire
// together correctly. The repair logic itself (candidate selection, apply,
// safety, idempotency) is exhaustively covered at the store layer by
// internal/postgresstore's TestRepairPreAnchorUsageEligibility* tests.
func TestRunDryRunAgainstEmptyDatabaseReportsNothing(t *testing.T) {
	databaseURLFile, keyringFile, migrationsDir := setupRepairCLIEnv(t)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, databaseURLFile, keyringFile, migrationsDir, false, "", &out); err != nil {
		t.Fatal(err)
	}
	printed := out.String()
	if !strings.Contains(printed, "DRY RUN") {
		t.Fatalf("dry run output missing DRY RUN banner: %s", printed)
	}
	if !strings.Contains(printed, "accounts affected: 0") {
		t.Fatalf("dry run against an empty database found work: %s", printed)
	}
}

// TestRunApplyWithoutOperatorIDIsRejected confirms --apply refuses to run
// without an approving operator id, per the task's "human-approved" and
// "resolved_by = a caller-supplied operator id" requirements.
func TestRunApplyWithoutOperatorIDIsRejected(t *testing.T) {
	databaseURLFile, keyringFile, migrationsDir := setupRepairCLIEnv(t)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := run(ctx, databaseURLFile, keyringFile, migrationsDir, true, "", &out)
	if err == nil {
		t.Fatal("--apply without --operator-id was accepted")
	}
	if out.Len() != 0 {
		t.Fatalf("rejected apply still printed output: %s", out.String())
	}
}

// TestPrintSummaryFormatsAccountsAndTotals is a pure formatting check
// (no database) for the table printSummary emits.
func TestPrintSummaryFormatsAccountsAndTotals(t *testing.T) {
	var out bytes.Buffer
	printSummary(&out, applyResultFixture())
	printed := out.String()
	for _, want := range []string{"APPLIED", "30000000-0000-4000-8000-000000000079", "TOTAL",
		"accounts affected: 1"} {
		if !strings.Contains(printed, want) {
			t.Fatalf("summary output missing %q: %s", want, printed)
		}
	}
}
