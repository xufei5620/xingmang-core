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

	"invoice-system/backend/internal/auth"
	"invoice-system/backend/internal/migrate"
	"invoice-system/backend/internal/testdb"
)

const (
	testFromIssuer  = "https://auth.solov.cc/realms/solov"
	testToIssuer    = "https://console.solov.cc"
	testFromSubject = "10000000-0000-4000-8000-000000000001"
	testToSubject   = "20000000-0000-4000-8000-000000000002"
	testOperatorID  = "30000000-0000-4000-8000-000000000003"
)

// setupIdentityMigrateCLIEnv migrates a fresh isolated schema and writes the
// two secret files run() reads, returning their paths and the migrations
// directory to pass in -- mirrors cmd/eligibility-repair/main_test.go's own
// setupRepairCLIEnv exactly (that package's unexported test helpers cannot be
// imported from here, hence the duplication).
func setupIdentityMigrateCLIEnv(t *testing.T) (databaseURLFile, keyringFile, migrationsDir string) {
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

func baseMigrationInput() auth.IdentityMigrationInput {
	return auth.IdentityMigrationInput{
		FromIssuer: testFromIssuer, FromSubject: testFromSubject,
		ToIssuer: testToIssuer, ToSubject: testToSubject,
	}
}

// TestRunDryRunAgainstEmptyDatabaseIsRefused is the CLI wiring's smoke test:
// against a freshly migrated, empty database (no matching identity), run()
// must surface MigrateOIDCBinding's refusal -- proving flag plumbing, secret
// loading and the migration check all wire together. The migration logic
// itself (candidate selection, apply, idempotency, session invalidation) is
// exhaustively covered at the auth package layer by
// internal/auth's TestMigrateOIDCBinding* tests.
func TestRunDryRunAgainstEmptyDatabaseIsRefused(t *testing.T) {
	databaseURLFile, keyringFile, migrationsDir := setupIdentityMigrateCLIEnv(t)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, databaseURLFile, keyringFile, migrationsDir, baseMigrationInput(), &out); err == nil {
		t.Fatal("expected run() to refuse a database with no matching identity")
	}
	if out.Len() != 0 {
		t.Fatalf("refused run must not print a summary: %s", out.String())
	}
}

// TestRunApplyWithoutOperatorIDIsRejected confirms --apply refuses to run
// without an approving operator id, before ever touching the database's
// identity rows.
func TestRunApplyWithoutOperatorIDIsRejected(t *testing.T) {
	databaseURLFile, keyringFile, migrationsDir := setupIdentityMigrateCLIEnv(t)
	in := baseMigrationInput()
	in.Apply = true
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, databaseURLFile, keyringFile, migrationsDir, in, &out); err == nil {
		t.Fatal("--apply without --operator-id was accepted")
	}
	if out.Len() != 0 {
		t.Fatalf("rejected apply still printed output: %s", out.String())
	}
}

func TestPrintSummaryFormatsDryRun(t *testing.T) {
	var out bytes.Buffer
	printSummary(&out, auth.IdentityMigrationResult{
		FromIssuer: testFromIssuer, FromSubject: testFromSubject,
		ToIssuer: testToIssuer, ToSubject: testToSubject,
		Row: auth.IdentityMigrationRow{
			UserID: "99ed401b-e78a-4883-b9bf-f4cb4ba1cf17", Status: "active", MaskedEmail: "a***b",
			AuthSessionsTotal: 2, AuthSessionsLive: 1, AuditRows: 5,
		},
	})
	printed := out.String()
	for _, want := range []string{"DRY RUN", testFromIssuer, testToIssuer, "99ed401b-e78a-4883-b9bf-f4cb4ba1cf17",
		"a***b", "auth_sessions_live:  1", "planned:"} {
		if !strings.Contains(printed, want) {
			t.Fatalf("dry run summary missing %q: %s", want, printed)
		}
	}
}

func TestPrintSummaryFormatsApplied(t *testing.T) {
	var out bytes.Buffer
	printSummary(&out, auth.IdentityMigrationResult{
		Applied: true, SessionsInvalidated: 1,
		FromIssuer: testFromIssuer, FromSubject: testFromSubject,
		ToIssuer: testToIssuer, ToSubject: testToSubject,
		Row: auth.IdentityMigrationRow{UserID: "99ed401b-e78a-4883-b9bf-f4cb4ba1cf17", Status: "active", MaskedEmail: "a***b"},
	})
	printed := out.String()
	for _, want := range []string{"APPLIED", "invalidated 1 live auth_sessions row(s)"} {
		if !strings.Contains(printed, want) {
			t.Fatalf("applied summary missing %q: %s", want, printed)
		}
	}
}

func TestPrintSummaryFormatsAlreadyMigrated(t *testing.T) {
	var out bytes.Buffer
	printSummary(&out, auth.IdentityMigrationResult{
		AlreadyMigrated: true,
		FromIssuer:      testFromIssuer, FromSubject: testFromSubject,
		ToIssuer: testToIssuer, ToSubject: testToSubject,
		Row: auth.IdentityMigrationRow{UserID: "99ed401b-e78a-4883-b9bf-f4cb4ba1cf17", Status: "active", MaskedEmail: "a***b"},
	})
	printed := out.String()
	if !strings.Contains(printed, "ALREADY MIGRATED") || !strings.Contains(printed, "no changes made") {
		t.Fatalf("already-migrated summary missing expected text: %s", printed)
	}
}
