package finance_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
)

// The append-only/history assertions require a disposable database and are
// intentionally opt-in. A shared integration database must never be cleaned
// with TRUNCATE (the production trigger rejects it), so these tests only run
// when the dedicated harness sets XM_RUNWAY_TEST_ISOLATED=1.
func runwayIsolatedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testing.Short() || getenvForTest("XM_RUNWAY_TEST_ISOLATED") != "1" {
		t.Skip("runway threshold store integration requires XM_RUNWAY_TEST_ISOLATED=1 and a disposable database")
	}
	dsn := getenvForTest("XM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("XM_RUNWAY_TEST_ISOLATED=1 requires XM_TEST_DATABASE_URL; refusing a skipped database gate")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Kept as a small indirection so ordinary unit runs never mutate process env.
var getenvForTest = func(name string) string { return lookupEnv(name) }

func lookupEnv(name string) string {
	// os.Getenv is isolated here to make the opt-in guard obvious in reviews.
	return os.Getenv(name)
}

func TestRunwayThresholdStoreBootstrapRequiresValidInputBeforeDatabase(t *testing.T) {
	store := finance.NewRunwayThresholdStore(nil, nil)
	_, err := store.Bootstrap(context.Background(), finance.BootstrapRunwayThresholdInput{
		Environment: "development",
		Thresholds:  finance.RunwayThresholds{CriticalDays: 10, WarningDays: 5, SeriousDays: 20},
		Actor:       "bootstrap", Reason: "test", RequestID: "req-test",
	})
	if err == nil || (!errors.Is(err, finance.ErrInvalidFormat) && !errors.Is(err, finance.ErrInconsistent)) {
		t.Fatalf("invalid thresholds should be rejected before pool use: %v", err)
	}
}

func TestRunwayThresholdStoreBootstrapRejectsMissingLifecycleEvidence(t *testing.T) {
	store := finance.NewRunwayThresholdStore(nil, nil)
	_, err := store.Bootstrap(context.Background(), finance.BootstrapRunwayThresholdInput{
		Environment: "development", Thresholds: finance.DefaultRunwayThresholds(),
		Actor: "", Reason: "", RequestID: "",
	})
	if err == nil || !errors.Is(err, finance.ErrMissingField) {
		t.Fatalf("missing lifecycle evidence should fail before pool use: %v", err)
	}
}

// Placeholder for the disposable-db harness contract. The real append-only
// checks live in scripts/test-runway-threshold-db.ps1 once an isolated DSN is
// supplied; this test makes the opt-in behavior explicit without touching a
// shared database.
func TestRunwayThresholdStoreIsolatedHarnessIsOptIn(t *testing.T) {
	if getenvForTest("XM_RUNWAY_TEST_ISOLATED") != "1" {
		t.Skip("opt-in only")
	}
	_ = runwayIsolatedPool(t)
}
