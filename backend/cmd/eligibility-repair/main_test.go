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

func projectionRequeueDeadApplyResultFixture() postgresstore.ProjectionRequeueDeadRepairResult {
	return postgresstore.ProjectionRequeueDeadRepairResult{
		Applied: true,
		Accounts: []postgresstore.ProjectionRequeueDeadRepairAccount{{
			ExternalAccountID: "30000000-0000-4000-8000-000000000084", PreviousAttempts: 8,
			LastErrorCode: "PROJECTION_DEAD", LastError: "simulated poison account error",
			DeadSince: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC), Requeued: true,
		}},
		Errors: []postgresstore.ProjectionRequeueDeadRepairAccountError{{
			ExternalAccountID: "30000000-0000-4000-8000-000000000085", Message: "simulated per-account failure",
		}},
		TotalRequeued: 1,
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
	if err := run(ctx, databaseURLFile, keyringFile, migrationsDir, false, "", kindPreAnchorUsage, repairFilters{}, &out); err != nil {
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
	if err := run(ctx, databaseURLFile, keyringFile, migrationsDir, false, "", kindBalanceAnchor, repairFilters{}, &out); err != nil {
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
	if err := run(ctx, databaseURLFile, keyringFile, migrationsDir, false, "", kindBalanceBlip, repairFilters{}, &out); err != nil {
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
	if err := run(ctx, databaseURLFile, keyringFile, migrationsDir, false, "", kindQueueNarrow, repairFilters{}, &out); err != nil {
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

// TestRunProjectionRequeueDeadDryRunAgainstEmptyDatabaseReportsNothing is the
// same wiring smoke test for --kind=projection-requeue-dead
// (XM-INV-PROJECTION-FAILURE-GRADING).
func TestRunProjectionRequeueDeadDryRunAgainstEmptyDatabaseReportsNothing(t *testing.T) {
	databaseURLFile, keyringFile, migrationsDir := setupRepairCLIEnv(t)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, databaseURLFile, keyringFile, migrationsDir, false, "", kindProjectionRequeueDead, repairFilters{}, &out); err != nil {
		t.Fatal(err)
	}
	printed := out.String()
	if !strings.Contains(printed, "XM-INV-PROJECTION-FAILURE-GRADING") || !strings.Contains(printed, "DRY RUN") {
		t.Fatalf("dry run output missing expected banner: %s", printed)
	}
	if !strings.Contains(printed, "accounts affected: 0") {
		t.Fatalf("dry run against an empty database found work: %s", printed)
	}
}

// TestRunIngestRequeueDeadDryRunAgainstEmptyDatabaseReportsNothing is the
// same wiring smoke test for --kind=ingest-requeue-dead (XM-INV-DEAD-REQUEUE).
func TestRunIngestRequeueDeadDryRunAgainstEmptyDatabaseReportsNothing(t *testing.T) {
	databaseURLFile, keyringFile, migrationsDir := setupRepairCLIEnv(t)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, databaseURLFile, keyringFile, migrationsDir, false, "", kindIngestRequeueDead, repairFilters{}, &out); err != nil {
		t.Fatal(err)
	}
	printed := out.String()
	if !strings.Contains(printed, "XM-INV-DEAD-REQUEUE") || !strings.Contains(printed, "DRY RUN") {
		t.Fatalf("dry run output missing expected banner: %s", printed)
	}
	for _, want := range []string{"events affected: 0", "total requeued: 0", "not requeued (replay blocked): 0"} {
		if !strings.Contains(printed, want) {
			t.Fatalf("dry run against an empty database is missing %q: %s", want, printed)
		}
	}
}

// TestRunNarrowingFlagsRejectedForWrongKind confirms --event/--account/
// --include-blocked-cycles fail closed rather than being silently ignored
// when the chosen --kind does not implement them: an operator narrowing to
// three reviewed rows who mistypes --kind must not instead get an unnarrowed
// run across every dead row.
func TestRunNarrowingFlagsRejectedForWrongKind(t *testing.T) {
	databaseURLFile, keyringFile, migrationsDir := setupRepairCLIEnv(t)
	const someUUID = "40000000-0000-4000-8000-000000000001"
	for _, testCase := range []struct {
		name, kind string
		filters    repairFilters
	}{
		{"event with projection kind", kindProjectionRequeueDead, repairFilters{eventID: someUUID}},
		{"event with pre-anchor kind", kindPreAnchorUsage, repairFilters{eventID: someUUID}},
		{"account with queue-narrow kind", kindQueueNarrow, repairFilters{accountID: someUUID}},
		{"include-blocked-cycles with projection kind", kindProjectionRequeueDead, repairFilters{includeBlockedCycles: true}},
		{"event with pending-reevaluate kind", kindPendingReevaluate, repairFilters{eventID: someUUID, accountID: someUUID}},
		{"include-blocked-cycles with pending-reevaluate kind", kindPendingReevaluate,
			repairFilters{includeBlockedCycles: true, accountID: someUUID}},
		// pending-reevaluate is never run unnarrowed: omitting --account is a
		// refusal, not a default of "every pending account".
		{"pending-reevaluate without an account", kindPendingReevaluate, repairFilters{}},
	} {
		var out bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := run(ctx, databaseURLFile, keyringFile, migrationsDir, false, "", testCase.kind, testCase.filters, &out)
		cancel()
		if err == nil {
			t.Fatalf("%s: --kind=%s accepted a flag it ignores", testCase.name, testCase.kind)
		}
		if out.Len() != 0 {
			t.Fatalf("%s: rejected invocation still printed output: %s", testCase.name, out.String())
		}
	}
	// Control: the kinds that do implement each flag must still accept it,
	// so the rejection above cannot pass by rejecting everything.
	for _, testCase := range []struct {
		name, kind string
		filters    repairFilters
	}{
		{"account with projection kind", kindProjectionRequeueDead, repairFilters{accountID: someUUID}},
		{"account with ingest kind", kindIngestRequeueDead, repairFilters{accountID: someUUID}},
		{"event with ingest kind", kindIngestRequeueDead, repairFilters{eventID: someUUID}},
		{"include-blocked-cycles with ingest kind", kindIngestRequeueDead, repairFilters{includeBlockedCycles: true}},
		{"account with pending-reevaluate kind", kindPendingReevaluate, repairFilters{accountID: someUUID}},
	} {
		var out bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := run(ctx, databaseURLFile, keyringFile, migrationsDir, false, "", testCase.kind, testCase.filters, &out)
		cancel()
		if err != nil {
			t.Fatalf("%s: --kind=%s rejected a flag it implements: %v", testCase.name, testCase.kind, err)
		}
	}
}

// TestRunAcknowledgeUnreplayableRequiresAnEvent pins the guardrail that
// matters most for this kind: it writes off customer data as permanently
// lost, so it is never allowed to run unnarrowed. Omitting --event must fail
// closed rather than defaulting to "every unreplayable event".
func TestRunAcknowledgeUnreplayableRequiresAnEvent(t *testing.T) {
	databaseURLFile, keyringFile, migrationsDir := setupRepairCLIEnv(t)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := run(ctx, databaseURLFile, keyringFile, migrationsDir, false, "",
		kindIngestAcknowledgeUnreplayable, repairFilters{}, &out)
	if err == nil {
		t.Fatal("--kind=ingest-acknowledge-unreplayable ran without --event")
	}
	if !strings.Contains(err.Error(), "--event is required") {
		t.Fatalf("error=%v, want it to name the missing flag", err)
	}
	if out.Len() != 0 {
		t.Fatalf("rejected invocation still printed output: %s", out.String())
	}

	// --account is meaningless here and must not be accepted as a substitute
	// for naming the event.
	var accountOut bytes.Buffer
	if err := run(ctx, databaseURLFile, keyringFile, migrationsDir, false, "",
		kindIngestAcknowledgeUnreplayable,
		repairFilters{accountID: "40000000-0000-4000-8000-000000000001"}, &accountOut); err == nil {
		t.Fatal("--account was accepted for the acknowledge kind")
	}
}

// TestRunUnknownKindIsRejected confirms an unrecognized --kind fails
// closed before ever opening the database.
func TestRunUnknownKindIsRejected(t *testing.T) {
	databaseURLFile, keyringFile, migrationsDir := setupRepairCLIEnv(t)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := run(ctx, databaseURLFile, keyringFile, migrationsDir, false, "", "unknown-kind", repairFilters{}, &out)
	if err == nil {
		t.Fatal("unknown --kind was accepted")
	}
	if out.Len() != 0 {
		t.Fatalf("rejected kind still printed output: %s", out.String())
	}
}

// TestRunApplyWithoutOperatorIDIsRejected confirms --apply refuses to run
// without an approving operator id, per the task's "human-approved" and
// "resolved_by = a caller-supplied operator id" requirements -- for every
// repair kind.
func TestRunApplyWithoutOperatorIDIsRejected(t *testing.T) {
	const someUUID = "40000000-0000-4000-8000-000000000001"
	// Every kind, including the two that require a narrowing flag of their
	// own: those are given one, so the rejection under test is the missing
	// operator id and not the missing --event/--account.
	for _, testCase := range []struct {
		kind    string
		filters repairFilters
	}{
		{kindPreAnchorUsage, repairFilters{}},
		{kindBalanceAnchor, repairFilters{}},
		{kindBalanceBlip, repairFilters{}},
		{kindQueueNarrow, repairFilters{}},
		{kindPolicyStartReanchor, repairFilters{}},
		{kindProjectionRequeueDead, repairFilters{}},
		{kindIngestRequeueDead, repairFilters{}},
		{kindIngestAcknowledgeUnreplayable, repairFilters{eventID: someUUID}},
		{kindPendingReevaluate, repairFilters{accountID: someUUID}},
	} {
		databaseURLFile, keyringFile, migrationsDir := setupRepairCLIEnv(t)
		var out bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := run(ctx, databaseURLFile, keyringFile, migrationsDir, true, "", testCase.kind, testCase.filters, &out)
		cancel()
		if err == nil {
			t.Fatalf("--apply without --operator-id was accepted for --kind=%s", testCase.kind)
		}
		if out.Len() != 0 {
			t.Fatalf("rejected apply still printed output for --kind=%s: %s", testCase.kind, out.String())
		}
	}
}

// TestRunPendingReevaluateDryRunAgainstEmptyDatabaseReportsNothing is the
// wiring smoke test for --kind=pending-reevaluate (XM-INV-PENDING-RECON):
// against a freshly migrated, empty database the named account simply does
// not exist, so the run must succeed, say so, and report nothing affected.
func TestRunPendingReevaluateDryRunAgainstEmptyDatabaseReportsNothing(t *testing.T) {
	databaseURLFile, keyringFile, migrationsDir := setupRepairCLIEnv(t)
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, databaseURLFile, keyringFile, migrationsDir, false, "", kindPendingReevaluate,
		repairFilters{accountID: "40000000-0000-4000-8000-000000000001"}, &out); err != nil {
		t.Fatal(err)
	}
	printed := out.String()
	if !strings.Contains(printed, "XM-INV-PENDING-RECON") || !strings.Contains(printed, "DRY RUN") {
		t.Fatalf("dry run output missing expected banner: %s", printed)
	}
	if !strings.Contains(printed, "accounts affected: 0") {
		t.Fatalf("dry run against an empty database found work: %s", printed)
	}
	if !strings.Contains(printed, "CHECKS") {
		t.Fatalf("dry run must print its checks: %s", printed)
	}
}

// TestPrintPendingReevaluateSummaryDistinguishesARefusedApply is a pure
// formatting check (no database) for the one banner that must never be wrong:
// a refused --apply reads REFUSED, not DRY RUN. An operator who typed --apply
// and saw "DRY RUN (nothing was changed)" would reasonably conclude the flag
// had not registered and run it again, rather than read the STOP line.
func TestPrintPendingReevaluateSummaryDistinguishesARefusedApply(t *testing.T) {
	refused := postgresstore.PendingReevaluateRepairResult{
		ApplyRequested: true, Found: true, AccountID: "30000000-0000-4000-8000-000000000001",
		Status: "active", ExitMatches: 2,
		Checks: []postgresstore.PendingReevaluateCheck{{Name: "状态", Detail: "不在待对平", Blocker: true}},
	}
	var out bytes.Buffer
	printPendingReevaluateSummary(&out, refused)
	printed := out.String()
	if !strings.Contains(printed, "REFUSED") || strings.Contains(printed, "DRY RUN") {
		t.Fatalf("a refused apply must not print the dry-run banner: %s", printed)
	}
	if !strings.Contains(printed, "accounts affected: 0") {
		t.Fatalf("a refused apply affected nothing: %s", printed)
	}

	out.Reset()
	printPendingReevaluateSummary(&out, postgresstore.PendingReevaluateRepairResult{
		Found: true, AccountID: "30000000-0000-4000-8000-000000000001", ExitMatches: 2})
	if printed = out.String(); !strings.Contains(printed, "DRY RUN") || strings.Contains(printed, "REFUSED") {
		t.Fatalf("an ordinary dry run must print the dry-run banner: %s", printed)
	}

	out.Reset()
	printPendingReevaluateSummary(&out, postgresstore.PendingReevaluateRepairResult{
		Applied: true, ApplyRequested: true, Queued: true, Found: true,
		AccountID: "30000000-0000-4000-8000-000000000001", ExitMatches: 2})
	printed = out.String()
	if !strings.Contains(printed, "APPLIED") || strings.Contains(printed, "REFUSED") {
		t.Fatalf("a successful apply must print APPLIED: %s", printed)
	}
	if !strings.Contains(printed, "accounts affected: 1") {
		t.Fatalf("a successful apply affected one account: %s", printed)
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

// TestPrintProjectionRequeueDeadSummaryFormatsAccountsAndTotals is the same
// formatting check for printProjectionRequeueDeadSummary, plus its own
// account-error section (matching every other repair kind's per-account
// isolation).
func TestPrintProjectionRequeueDeadSummaryFormatsAccountsAndTotals(t *testing.T) {
	var out bytes.Buffer
	printProjectionRequeueDeadSummary(&out, projectionRequeueDeadApplyResultFixture())
	printed := out.String()
	for _, want := range []string{"XM-INV-PROJECTION-FAILURE-GRADING", "APPLIED", "30000000-0000-4000-8000-000000000084",
		"PROJECTION_DEAD", "simulated poison account error", "total requeued: 1", "accounts affected: 1",
		"ACCOUNT ERRORS", "30000000-0000-4000-8000-000000000085", "simulated per-account failure"} {
		if !strings.Contains(printed, want) {
			t.Fatalf("summary output missing %q: %s", want, printed)
		}
	}
}
