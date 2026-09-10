package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// This is the direct proof requested for this change: two harness instances
// resolving the shared default database URL from two different worktree
// directories must land on two different, worktree-derived database names.
func TestRewriteURLForWorktreeDerivesDistinctDatabasesPerWorktree(t *testing.T) {
	const shared = "postgres://postgres:test@127.0.0.1:55432/invoice_test?sslmode=disable"

	urlA, dbA, rewrittenA, err := rewriteURLForWorktree(shared, `K:\发票\wt-XM-INV-TOOLCHAIN`)
	if err != nil {
		t.Fatal(err)
	}
	urlB, dbB, rewrittenB, err := rewriteURLForWorktree(shared, `K:\发票\wt-XM-INV-AUTOLOGIN`)
	if err != nil {
		t.Fatal(err)
	}

	if !rewrittenA || !rewrittenB {
		t.Fatalf("shared default database was not rewritten: rewrittenA=%v rewrittenB=%v", rewrittenA, rewrittenB)
	}
	if dbA == dbB {
		t.Fatalf("two different worktrees derived the same database name: %q", dbA)
	}
	if !strings.HasPrefix(dbA, perWorktreeDatabasePrefix) || !strings.HasPrefix(dbB, perWorktreeDatabasePrefix) {
		t.Fatalf("derived database names do not carry the per-worktree prefix: %q %q", dbA, dbB)
	}
	if dbA == sharedDefaultDatabaseName || dbB == sharedDefaultDatabaseName {
		t.Fatalf("derived database name still equals the shared default: %q %q", dbA, dbB)
	}
	if !strings.Contains(urlA, dbA) || !strings.Contains(urlB, dbB) {
		t.Fatalf("resolved url does not embed its own derived database name: urlA=%q dbA=%q urlB=%q dbB=%q", urlA, dbA, urlB, dbB)
	}
	if urlA == urlB {
		t.Fatalf("two different worktrees resolved the same connection url: %q", urlA)
	}

	// Same worktree, called twice, must be stable (idempotent derivation).
	_, dbAAgain, _, err := rewriteURLForWorktree(shared, `K:\发票\wt-XM-INV-TOOLCHAIN`)
	if err != nil {
		t.Fatal(err)
	}
	if dbAAgain != dbA {
		t.Fatalf("derivation is not stable for the same worktree: %q vs %q", dbA, dbAAgain)
	}
}

func TestRewriteURLForWorktreeRespectsExplicitNonDefaultDatabase(t *testing.T) {
	const explicit = "postgres://postgres:test@127.0.0.1:55432/invoice_test_backoff?sslmode=disable"
	resolvedURL, dbName, rewritten, err := rewriteURLForWorktree(explicit, `K:\发票\wt-XM-INV-BACKOFF`)
	if err != nil {
		t.Fatal(err)
	}
	if rewritten {
		t.Fatalf("an explicit non-default database name was rewritten: %q", dbName)
	}
	if resolvedURL != explicit {
		t.Fatalf("an explicit non-default database url was changed: got %q want %q", resolvedURL, explicit)
	}
	if dbName != "invoice_test_backoff" {
		t.Fatalf("unexpected database name parsed: %q", dbName)
	}
}

func TestRewriteURLForWorktreeRejectsUnparsableURL(t *testing.T) {
	if _, _, _, err := rewriteURLForWorktree("://not a url", `K:\wt`); err == nil {
		t.Fatal("expected an error for an unparsable url")
	}
}

func TestSanitizeIdentifierPart(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"lowercased and hyphens become underscores", "wt-XM-INV-TOOLCHAIN", "wt_xm_inv_toolchain"},
		{"repeated separators collapse", "wt--A__B", "wt_a_b"},
		{"leading and trailing separators trimmed", "-leading-and-trailing-", "leading_and_trailing"},
		{"non-ascii collapses to underscores then trims", "发票", "worktree"},
		{"empty falls back to a fixed name", "", "worktree"},
		{"digits are preserved", "wt2", "wt2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeIdentifierPart(tc.in)
			if got != tc.want {
				t.Fatalf("sanitizeIdentifierPart(%q) = %q, want %q", tc.in, got, tc.want)
			}
			for _, r := range got {
				if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_') {
					t.Fatalf("sanitizeIdentifierPart(%q) produced an unsafe character %q in %q", tc.in, r, got)
				}
			}
		})
	}
}

func TestPerWorktreeDatabaseNameTruncatesLongNamesWithoutColliding(t *testing.T) {
	longA := "wt-" + strings.Repeat("a", 120) + "-branch-one"
	longB := "wt-" + strings.Repeat("a", 120) + "-branch-two"
	nameA := perWorktreeDatabaseName(longA)
	nameB := perWorktreeDatabaseName(longB)

	if len(nameA) > postgresIdentifierMaxBytes || len(nameB) > postgresIdentifierMaxBytes {
		t.Fatalf("derived name exceeds PostgreSQL's %d-byte identifier limit: %d %d", postgresIdentifierMaxBytes, len(nameA), len(nameB))
	}
	if nameA == nameB {
		t.Fatalf("two long worktree names that only differ near the end collided after truncation: %q", nameA)
	}
	if !strings.HasPrefix(nameA, perWorktreeDatabasePrefix) {
		t.Fatalf("truncated name lost its prefix: %q", nameA)
	}
}

func TestWorktreeRootFromWalksUpToGitMarker(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: ../elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deepChild := filepath.Join(root, "backend", "internal", "testdb")
	if err := os.MkdirAll(deepChild, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := worktreeRootFrom(deepChild)
	if err != nil {
		t.Fatal(err)
	}
	wantAbs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != wantAbs {
		t.Fatalf("worktreeRootFrom = %q, want %q", got, wantAbs)
	}
}

func TestWorktreeRootFromFailsWithoutGitMarker(t *testing.T) {
	// A fresh temp directory tree has no ".git" anywhere above it in
	// practice (it lives under the OS temp root, never inside a checkout),
	// so this exercises the "walked off the top without finding one" error
	// path deterministically.
	start := filepath.Join(t.TempDir(), "nested", "deeper")
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := worktreeRootFrom(start); err == nil {
		t.Fatal("expected an error when no .git marker exists above start")
	}
}

func TestWorktreeRootIsFindableFromThisPackagesOwnSourceFile(t *testing.T) {
	// worktreeRoot (unlike worktreeRootFrom) walks up from this file's own
	// runtime.Caller path, i.e. the real repository/worktree this test is
	// actually compiled from -- proving the non-test entry point some other
	// package's test binary depends on actually resolves in this checkout.
	root, err := worktreeRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "invoice", "backend", "go.mod")); err != nil {
		t.Fatalf("worktreeRoot()=%q does not look like this repository's root: %v", root, err)
	}
}

func TestEnsureDatabaseExistsIsIdempotentAndPerWorktreeDatabasesAreIndependent(t *testing.T) {
	rawURL := os.Getenv("INVOICE_TEST_DATABASE_URL")
	if rawURL == "" {
		t.Skip("INVOICE_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()

	// Two distinct "worktree" database names, standing in for two harness
	// instances started from different worktrees, proven independent end to
	// end: both get created against the real server, neither call errors on
	// a second (idempotent) attempt, and each is a real, separately queryable
	// database distinct from the other and from the shared default.
	dbA := uniqueTestDatabaseName(t, "a")
	dbB := uniqueTestDatabaseName(t, "b")
	adminURL, err := withDatabaseName(rawURL, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dropDatabase(t, adminURL, dbA) })
	t.Cleanup(func() { dropDatabase(t, adminURL, dbB) })

	if err := ensureDatabaseExists(ctx, rawURL, dbA); err != nil {
		t.Fatalf("first ensureDatabaseExists(%q): %v", dbA, err)
	}
	if err := ensureDatabaseExists(ctx, rawURL, dbA); err != nil {
		t.Fatalf("second, idempotent ensureDatabaseExists(%q): %v", dbA, err)
	}
	if err := ensureDatabaseExists(ctx, rawURL, dbB); err != nil {
		t.Fatalf("ensureDatabaseExists(%q): %v", dbB, err)
	}

	for _, name := range []string{dbA, dbB} {
		exists, err := databaseExists(ctx, adminURL, name)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Fatalf("database %q was not actually created", name)
		}
	}

	dbURL, err := withDatabaseName(rawURL, dbA)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := waitPoolReady(ctx, pool, 10*time.Second); err != nil {
		t.Fatalf("created database %q did not become reachable: %v", dbA, err)
	}
}

// uniqueTestDatabaseName returns a database name this test package's own
// scratch tests can use without colliding with a real per-worktree database
// or with a concurrent run of this same test in another worktree/process.
func uniqueTestDatabaseName(t *testing.T, label string) string {
	t.Helper()
	var randomSuffix [4]byte
	if _, err := rand.Read(randomSuffix[:]); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("invoice_test_testdb_pkg_%s_%s", label, hex.EncodeToString(randomSuffix[:]))
}

func databaseExists(ctx context.Context, adminURL, dbName string) (bool, error) {
	pool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		return false, err
	}
	defer pool.Close()
	var exists bool
	err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, dbName).Scan(&exists)
	return exists, err
}

// dropDatabase is a best-effort t.Cleanup helper: it intentionally ignores
// errors (e.g. the database was never created because an earlier assertion
// in the test already failed) so cleanup never masks the real test failure.
func dropDatabase(t *testing.T, adminURL, dbName string) {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), adminURL)
	if err != nil {
		return
	}
	defer pool.Close()
	_, _ = pool.Exec(context.Background(), `DROP DATABASE IF EXISTS `+quoteIdentifier(dbName))
}

func TestResolveURLFallsBackToRawURLWithoutGitRoot(t *testing.T) {
	// scripts/verify-postgres.ps1 runs the suite from a source copy inside a
	// disposable container: no .git anywhere above the package, one database
	// nobody else can reach. That must use the URL exactly as given.
	rawURL := "postgres://postgres:secret@127.0.0.1:5432/invoice_test?sslmode=disable"
	ensureCalled := false
	got, err := resolveURL(rawURL,
		func() (string, error) { return "", errors.New("no .git found walking up from /src/internal/testdb") },
		func(context.Context, string, string) error { ensureCalled = true; return nil })
	if err != nil {
		t.Fatalf("resolveURL returned error: %v", err)
	}
	if got != rawURL {
		t.Fatalf("resolveURL = %q, want the raw URL %q", got, rawURL)
	}
	if ensureCalled {
		t.Fatal("ensure must not run when no per-worktree rewrite happened")
	}
}

func TestResolveURLRewritesAndEnsuresWhenGitRootIsFound(t *testing.T) {
	rawURL := "postgres://postgres:secret@127.0.0.1:5432/invoice_test?sslmode=disable"
	var ensuredDB string
	got, err := resolveURL(rawURL,
		func() (string, error) { return filepath.Join("K:", "repo", "wt-Feature-A"), nil },
		func(_ context.Context, sourceURL, dbName string) error {
			if sourceURL != rawURL {
				t.Fatalf("ensure received sourceURL %q, want %q", sourceURL, rawURL)
			}
			ensuredDB = dbName
			return nil
		})
	if err != nil {
		t.Fatalf("resolveURL returned error: %v", err)
	}
	if ensuredDB != "invoice_test_wt_feature_a" {
		t.Fatalf("ensured database = %q, want invoice_test_wt_feature_a", ensuredDB)
	}
	if got != "postgres://postgres:secret@127.0.0.1:5432/invoice_test_wt_feature_a?sslmode=disable" {
		t.Fatalf("resolveURL = %q", got)
	}
}

func TestResolveURLReportsEnsureFailure(t *testing.T) {
	_, err := resolveURL("postgres://postgres:secret@127.0.0.1:5432/invoice_test",
		func() (string, error) { return filepath.Join("K:", "repo", "wt-x"), nil },
		func(context.Context, string, string) error { return errors.New("connection refused") })
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("expected the ensure error to be reported, got %v", err)
	}
}
