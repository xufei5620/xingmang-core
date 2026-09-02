// Package testdb resolves the PostgreSQL connection URL that integration
// tests should use, so that concurrent git worktrees (this repo's normal way
// of running independent agents/branches side by side) never race each
// other's `DROP SCHEMA public CASCADE`/migration setup against one shared
// database.
//
// Background: every integration test that resets the public schema to a
// known state (postgresstore, oidcretention, auth, adminsettings, and one
// migrate test -- search each package for "DROP SCHEMA public CASCADE")
// read INVOICE_TEST_DATABASE_URL directly and ran that reset against
// whatever database the URL named. When two worktrees both set
// INVOICE_TEST_DATABASE_URL to the same shared "invoice_test" database (the
// documented default, e.g.
// postgres://postgres:test@127.0.0.1:55432/invoice_test?sslmode=disable) and
// run these tests at overlapping wall-clock time, one worktree's schema drop
// can land mid-migration for the other. This was reported independently
// twice: docs/handoffs/XM-INV-CYCLE-BACKOFF.md hit "database contains
// unknown migration 0016_policy_anchor.sql" in the adminsettings and auth
// packages, and docs/handoffs/XM-INV-OBS-BUNDLE.md hit "relation
// schema_migrations does not exist" while another agent's run raced the
// same literal database -- each worked around by hand with a one-off
// dedicated database name for that task only.
//
// URL, below, makes that workaround automatic and permanent: when
// INVOICE_TEST_DATABASE_URL names exactly the shared default database
// "invoice_test", it is rewritten to a name derived from the current git
// worktree ("invoice_test_<sanitized worktree directory name>"), which is
// created on first use if it does not already exist. A URL that already
// names any other, explicit database (e.g. a hand-picked
// "invoice_test_backoff", as both handoffs above used) is returned
// unchanged -- every existing workaround, and anyone deliberately pointing
// at a specific database, keeps working exactly as before.
//
// Integration tests that already isolate themselves with a uniquely-named
// schema per run (backupverify, application, and most of migrate's tests --
// look for a random or timestamp-suffixed schema name rather than a fixed
// "public") are not exposed to this race and do not need this package; nothing
// stops them from using it too, but it is not required for correctness there.
package testdb

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// sharedDefaultDatabaseName is this repo's documented shared dev
	// database name (see docs/PRODUCTION-RUNBOOK.md and every handoff's
	// "Tests run" section). Only a URL naming exactly this database is
	// rewritten; anything else is an explicit, deliberate choice.
	sharedDefaultDatabaseName = "invoice_test"
	perWorktreeDatabasePrefix = "invoice_test_"
	// PostgreSQL identifiers are limited to NAMEDATALEN-1 = 63 bytes.
	postgresIdentifierMaxBytes = 63
)

// errNotSet is returned by resolve when INVOICE_TEST_DATABASE_URL is unset,
// so URL can turn it into the same t.Skip every call site already had.
var errNotSet = errors.New("INVOICE_TEST_DATABASE_URL is not set")

// resolve is computed once per test binary process (the worktree and the
// env var are both fixed for the life of that process) and reused by every
// URL(t) call, so the pg_database existence check and any CREATE DATABASE
// only happen once even though a package's tests call URL(t) many times.
// Each Go test package compiles to its own separate binary/process, so this
// runs once per package under test, not once for the whole `go test ./...`
// sweep; ensureDatabaseExists tolerates that (see its own comment).
var resolve = sync.OnceValues(func() (string, error) {
	rawURL := os.Getenv("INVOICE_TEST_DATABASE_URL")
	if rawURL == "" {
		return "", errNotSet
	}
	root, err := worktreeRoot()
	if err != nil {
		return "", fmt.Errorf("locate git worktree root for per-worktree test database: %w", err)
	}
	resolvedURL, dbName, rewritten, err := rewriteURLForWorktree(rawURL, root)
	if err != nil {
		return "", fmt.Errorf("derive per-worktree test database url: %w", err)
	}
	if rewritten {
		if err := ensureDatabaseExists(context.Background(), rawURL, dbName); err != nil {
			return "", fmt.Errorf("ensure per-worktree test database %q exists: %w", dbName, err)
		}
	}
	return resolvedURL, nil
})

// URL returns the connection URL this test process should use, rewriting
// the shared default database to a per-worktree one and creating it on
// first use as described in the package doc. It calls t.Skip if
// INVOICE_TEST_DATABASE_URL is not set at all, matching every call site's
// previous behavior exactly for that case.
func URL(t *testing.T) string {
	t.Helper()
	resolvedURL, err := resolve()
	if err != nil {
		if errors.Is(err, errNotSet) {
			t.Skip("INVOICE_TEST_DATABASE_URL is not set")
		}
		t.Fatal(err)
	}
	return resolvedURL
}

// rewriteURLForWorktree returns rawURL unchanged (rewritten=false) when its
// database name is not exactly the shared default "invoice_test" -- an
// explicit non-default name is always respected as-is. Otherwise it returns
// a URL with the database name replaced by a name derived from worktreeDir,
// along with that derived name.
func rewriteURLForWorktree(rawURL, worktreeDir string) (resolvedURL, dbName string, rewritten bool, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", false, fmt.Errorf("parse INVOICE_TEST_DATABASE_URL: %w", err)
	}
	currentDB := strings.TrimPrefix(u.Path, "/")
	if currentDB != sharedDefaultDatabaseName {
		return rawURL, currentDB, false, nil
	}
	derived := perWorktreeDatabaseName(worktreeDir)
	u.Path = "/" + derived
	return u.String(), derived, true, nil
}

// perWorktreeDatabaseName derives a valid, worktree-distinct PostgreSQL
// database name from a worktree directory path. Two different worktree
// directories always derive different names.
func perWorktreeDatabaseName(worktreeDir string) string {
	base := filepath.Base(filepath.Clean(worktreeDir))
	suffix := sanitizeIdentifierPart(base)
	name := perWorktreeDatabasePrefix + suffix
	if len(name) <= postgresIdentifierMaxBytes {
		return name
	}
	// A worktree directory name long enough to overflow PostgreSQL's
	// 63-byte identifier limit is truncated, with a short hash of the full
	// (untruncated) name appended so two long names that only differ near
	// the end don't collide after truncation.
	sum := sha256.Sum256([]byte(base))
	shortHash := hex.EncodeToString(sum[:])[:8]
	keep := postgresIdentifierMaxBytes - len(perWorktreeDatabasePrefix) - len(shortHash) - 1
	if keep < 0 {
		keep = 0
	}
	if keep > len(suffix) {
		keep = len(suffix)
	}
	return perWorktreeDatabasePrefix + suffix[:keep] + "_" + shortHash
}

// sanitizeIdentifierPart lowercases name and replaces every rune that is
// not [a-z0-9] with '_', collapsing repeats and trimming leading and
// trailing underscores, so the result is always safe to embed unquoted in a
// PostgreSQL identifier regardless of what characters the worktree
// directory name contains.
func sanitizeIdentifierPart(name string) string {
	name = strings.ToLower(name)
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	s := b.String()
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	s = strings.Trim(s, "_")
	if s == "" {
		s = "worktree"
	}
	return s
}

// worktreeRoot locates the root of the git worktree (or ordinary
// repository checkout) containing this source file, by walking up from
// this file's own compiled-in path -- the same directory `git rev-parse
// --show-toplevel` would report, but without spawning git, so it works even
// if git is not on PATH. A worktree's root has a ".git" file (a "gitdir:
// ..." pointer); the main checkout's root has a ".git" directory -- either
// satisfies os.Stat and is treated the same way here, since only the
// directory name is used (to name the per-worktree database), never the
// pointer contents.
func worktreeRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("runtime.Caller could not resolve this file's own path")
	}
	return worktreeRootFrom(filepath.Dir(file))
}

func worktreeRootFrom(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path for %q: %w", start, err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, ".git")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no .git found walking up from %s", start)
		}
		dir = parent
	}
}

// ensureDatabaseExists creates dbName on the same PostgreSQL server and
// with the same credentials as sourceURL if it does not already exist,
// using a pg_database existence check (PostgreSQL's CREATE DATABASE has no
// IF NOT EXISTS clause). It connects to the "postgres" maintenance
// database to do this, since CREATE DATABASE cannot run against the
// database being created or inside a transaction.
func ensureDatabaseExists(ctx context.Context, sourceURL, dbName string) error {
	adminURL, err := withDatabaseName(sourceURL, "postgres")
	if err != nil {
		return err
	}
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		return fmt.Errorf("connect to maintenance database: %w", err)
	}
	defer adminPool.Close()
	if err := waitPoolReady(ctx, adminPool, 10*time.Second); err != nil {
		return fmt.Errorf("maintenance database did not become reachable: %w", err)
	}

	var exists bool
	if err := adminPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, dbName).Scan(&exists); err != nil {
		return fmt.Errorf("check pg_database for %q: %w", dbName, err)
	}
	if exists {
		return nil
	}
	// dbName is only ever produced by sanitizeIdentifierPart above
	// (lowercase ASCII letters, digits and underscores only), so it is safe
	// to interpolate into DDL that PostgreSQL does not allow to be
	// parameterized.
	if _, err := adminPool.Exec(ctx, `CREATE DATABASE `+quoteIdentifier(dbName)); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P04" {
			// duplicate_database: another process (e.g. a second package's
			// test binary, or a second integration test run started from
			// the same worktree at almost the same time) created it between
			// our existence check and this CREATE DATABASE. That is exactly
			// the kind of race this function exists to avoid *between
			// different worktrees*; two processes in the *same* worktree
			// racing to create their own shared per-worktree database is
			// harmless by definition, so this is success, not an error.
			return nil
		}
		return fmt.Errorf("create database %q: %w", dbName, err)
	}
	return nil
}

func withDatabaseName(rawURL, dbName string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse database url: %w", err)
	}
	u.Path = "/" + dbName
	return u.String(), nil
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func waitPoolReady(ctx context.Context, pool *pgxpool.Pool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var err error
	for time.Now().Before(deadline) {
		pingCtx, cancel := context.WithTimeout(ctx, time.Second)
		err = pool.Ping(pingCtx)
		cancel()
		if err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}
