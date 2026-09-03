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

func balanceAnchorApplyResultFixture() postgresstore.BalanceAnchorRepairResult {
	return postgresstore.BalanceAnchorRepairResult{
		Applied: true,
		Accounts: []postgresstore.BalanceAnchorRepairAccount{{
			ExternalAccountID: "30000000-0000-4000-8000-000000000080", SourceInstanceID: "10000000-0000-4000-8000-000000000080",
			SourceGapFreezesResolved: 3, CheckpointEvaluationsReset: 2, Reactivated: true,
		}},
		TotalSourceGapFreezesResolved: 3, TotalCheckpointEvaluationsReset: 2,
	}
}

func balanceBlipApplyResultFixture() postgresstore.BalanceBlipRepairResult {
	return postgresstore.BalanceBlipRepairResult{
		Applied: true,
		Accounts: []postgresstore.BalanceBlipRepairAccount{{
			ExternalAccountID: "30000000-0000-4000-8000-000000000081", SourceInstanceID: "10000000-0000-4000-8000-000000000081",
			BlipCreditsRemoved: 1, NegativeFreezesResolved: 8, CheckpointEvaluationsReset: 8, Reactivated: true,
		}},
		TotalBlipCreditsRemoved: 1, TotalNegativeFreezesResolved: 8, TotalCheckpointEvaluationsReset: 8,
	}
}

func queueNarrowApplyResultFixture() postgresstore.QueueNarrowRepairResult {
	return postgresstore.QueueNarrowRepairResult{
		Applied: true,
		Accounts: []postgresstore.QueueNarrowRepairAccount{{
			ExternalAccountID: "30000000-0000-4000-8000-000000000082", SourceInstanceID: "10000000-0000-4000-8000-000000000082",
			NegativeBalanceFreezesResolved: 1, UsageExceedsLedgerFreezesResolved: 1, LateFactFreezesResolved: 1,
			RebuiltPendingReconciliation: true, UsageOverageReprojected: true, Reactivated: false,
		}},
		Errors: []postgresstore.QueueNarrowRepairAccountError{{
			ExternalAccountID: "30000000-0000-4000-8000-000000000083", Message: "simulated per-account failure",
		}},
		TotalNegativeBalanceFreezesResolved: 1, TotalUsageExceedsLedgerFreezesResolved: 1, TotalLateFactFreezesResolved: 1,
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
// Omitting --kind (the empty-string default flag.String would never
// actually produce, since main() supplies kindPreAnchorUsage -- this test
// passes it explicitly to exercise exactly what an existing, unmodified
// invocation like rc70-repair.sh gets) proves the pre-anchor-usage path
// still works unchanged.
func TestRunDryRunAgainstEmptyDatabaseReportsNothing(t *testing.T) {
	databaseURLFile, keyringFile, migrationsDir := setupRepairCLIEnv(t)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, databaseURLFile, keyringFile, migrationsDir, false, "", kindPreAnchorUsage, &out); err != nil {
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

// TestRunBalanceAnchorDryRunAgainstEmptyDatabaseReportsNothing is the same
// wiring smoke test for --kind=balance-anchor (design XM-INV-ANCHOR-BALANCE).
func TestRunBalanceAnchorDryRunAgainstEmptyDatabaseReportsNothing(t *testing.T) {
	databaseURLFile, keyringFile, migrationsDir := setupRepairCLIEnv(t)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, databaseURLFile, keyringFile, migrationsDir, false, "", kindBalanceAnchor, &out); err != nil {
		t.Fatal(err)
	}
	printed := out.String()
	if !strings.Contains(printed, "XM-INV-ANCHOR-BALANCE") || !strings.Contains(printed, "DRY RUN") {
		t.Fatalf("dry run output missing expected banner: %s", printed)
	}
	if !strings.Contains(printed, "accounts affected: 0") {
		t.Fatalf("dry run against an empty database found work: %s", printed)
	}
}

// TestRunBalanceBlipDryRunAgainstEmptyDatabaseReportsNothing is the same
// wiring smoke test for --kind=balance-blip (design XM-INV-BALANCE-BLIP).
func TestRunBalanceBlipDryRunAgainstEmptyDatabaseReportsNothing(t *testing.T) {
	databaseURLFile, keyringFile, migrationsDir := setupRepairCLIEnv(t)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, databaseURLFile, keyringFile, migrationsDir, false, "", kindBalanceBlip, &out); err != nil {
		t.Fatal(err)
	}
	printed := out.String()
	if !strings.Contains(printed, "XM-INV-BALANCE-BLIP") || !strings.Contains(printed, "DRY RUN") {
		t.Fatalf("dry run output missing expected banner: %s", printed)
	}
	if !strings.Contains(printed, "accounts affected: 0") {
		t.Fatalf("dry run against an empty database found work: %s", printed)
	}
}

// TestRunQueueNarrowDryRunAgainstEmptyDatabaseReportsNothing is the same
// wiring smoke test for --kind=queue-narrow (design XM-INV-ELIG-SIMPLIFY
// section 3(C)).
func TestRunQueueNarrowDryRunAgainstEmptyDatabaseReportsNothing(t *testing.T) {
	databaseURLFile, keyringFile, migrationsDir := setupRepairCLIEnv(t)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, databaseURLFile, keyringFile, migrationsDir, false, "", kindQueueNarrow, &out); err != nil {
		t.Fatal(err)
	}
	printed := out.String()
	if !strings.Contains(printed, "XM-INV-ELIG-QUEUE-NARROW") || !strings.Contains(printed, "DRY RUN") {
		t.Fatalf("dry run output missing expected banner: %s", printed)
	}
	if !strings.Contains(printed, "accounts affected: 0") {
		t.Fatalf("dry run against an empty database found work: %s", printed)
	}
}

// TestRunUnknownKindIsRejected confirms an unrecognized --kind fails
// closed before ever opening the database.
func TestRunUnknownKindIsRejected(t *testing.T) {
	databaseURLFile, keyringFile, migrationsDir := setupRepairCLIEnv(t)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := run(ctx, databaseURLFile, keyringFile, migrationsDir, false, "", "unknown-kind", &out)
	if err == nil {
		t.Fatal("unknown --kind was accepted")
	}
	if out.Len() != 0 {
		t.Fatalf("rejected kind still printed output: %s", out.String())
	}
}

// TestRunApplyWithoutOperatorIDIsRejected confirms --apply refuses to run
// without an approving operator id, per the task's "human-approved" and
// "resolved_by = a caller-supplied operator id" requirements -- for all
// three repair kinds.
func TestRunApplyWithoutOperatorIDIsRejected(t *testing.T) {
	for _, kind := range []string{kindPreAnchorUsage, kindBalanceAnchor, kindBalanceBlip, kindQueueNarrow} {
		databaseURLFile, keyringFile, migrationsDir := setupRepairCLIEnv(t)
		var out bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := run(ctx, databaseURLFile, keyringFile, migrationsDir, true, "", kind, &out)
		cancel()
		if err == nil {
			t.Fatalf("--apply without --operator-id was accepted for --kind=%s", kind)
		}
		if out.Len() != 0 {
			t.Fatalf("rejected apply still printed output for --kind=%s: %s", kind, out.String())
		}
	}
}

// TestPrintSummaryFormatsAccountsAndTotals is a pure formatting check
// (no database) for the table printPreAnchorUsageSummary emits.
func TestPrintSummaryFormatsAccountsAndTotals(t *testing.T) {
	var out bytes.Buffer
	printPreAnchorUsageSummary(&out, applyResultFixture())
	printed := out.String()
	for _, want := range []string{"APPLIED", "30000000-0000-4000-8000-000000000079", "TOTAL",
		"accounts affected: 1"} {
		if !strings.Contains(printed, want) {
			t.Fatalf("summary output missing %q: %s", want, printed)
		}
	}
}

// TestPrintBalanceAnchorSummaryFormatsAccountsAndTotals is the same
// formatting check for printBalanceAnchorSummary.
func TestPrintBalanceAnchorSummaryFormatsAccountsAndTotals(t *testing.T) {
	var out bytes.Buffer
	printBalanceAnchorSummary(&out, balanceAnchorApplyResultFixture())
	printed := out.String()
	for _, want := range []string{"XM-INV-ANCHOR-BALANCE", "APPLIED", "30000000-0000-4000-8000-000000000080",
		"TOTAL", "accounts affected: 1"} {
		if !strings.Contains(printed, want) {
			t.Fatalf("summary output missing %q: %s", want, printed)
		}
	}
}

// TestPrintBalanceBlipSummaryFormatsAccountsAndTotals is the same
// formatting check for printBalanceBlipSummary.
func TestPrintBalanceBlipSummaryFormatsAccountsAndTotals(t *testing.T) {
	var out bytes.Buffer
	printBalanceBlipSummary(&out, balanceBlipApplyResultFixture())
	printed := out.String()
	for _, want := range []string{"XM-INV-BALANCE-BLIP", "APPLIED", "30000000-0000-4000-8000-000000000081",
		"TOTAL", "accounts affected: 1"} {
		if !strings.Contains(printed, want) {
			t.Fatalf("summary output missing %q: %s", want, printed)
		}
	}
}

// TestPrintQueueNarrowSummaryFormatsAccountsAndTotals is the same formatting
// check for printQueueNarrowSummary, plus its own account-error section
// (design's own account-isolation requirement: a per-account failure must be
// visible to the operator without aborting the printed report for every
// other account).
func TestPrintQueueNarrowSummaryFormatsAccountsAndTotals(t *testing.T) {
	var out bytes.Buffer
	printQueueNarrowSummary(&out, queueNarrowApplyResultFixture())
	printed := out.String()
	for _, want := range []string{"XM-INV-ELIG-QUEUE-NARROW", "APPLIED", "30000000-0000-4000-8000-000000000082",
		"TOTAL", "accounts affected: 1", "ACCOUNT ERRORS", "30000000-0000-4000-8000-000000000083",
		"simulated per-account failure"} {
		if !strings.Contains(printed, want) {
			t.Fatalf("summary output missing %q: %s", want, printed)
		}
	}
}
