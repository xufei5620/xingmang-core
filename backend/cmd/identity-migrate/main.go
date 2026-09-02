// Command identity-migrate implements design CR-0006 change item f /
// XM-INV-IDENTITY-MIGRATE: a versioned, human-approved lifecycle operation
// that rewrites the invoice system's one existing admin identity's
// invoice_users.oidc_issuer/oidc_subject from Keycloak's values to the
// console assertion's, so the console's first assertion login lands on the
// same historical row instead of auth.ResolveOrCreate minting an orphaned
// second identity (see docs/handoffs/XM-INV-CONSOLE-ASSERT.md, "Not
// run"/Risk 3, and docs/handoffs/XM-INV-IDENTITY-MIGRATE.md for the
// production runbook). Defaults to --dry-run; --apply requires an approving
// operator id and actually mutates the database. Mirrors
// cmd/eligibility-repair's CLI shape exactly (absolute-path flags, one-line
// secret file reader, migrate.Verify before touching data, an all-or-nothing
// transaction, a printed summary).
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/auth"
	"invoice-system/backend/internal/migrate"
	"invoice-system/backend/internal/securefields"
)

func main() {
	databaseURLFile := flag.String("database-url-file", "", "absolute path to the database URL secret")
	keyringFile := flag.String("field-keyring-file", "", "absolute path to the field encryption keyring")
	migrationsDir := flag.String("migrations-dir", "/app/migrations", "bundled migration directory")
	fromIssuer := flag.String("from-issuer", "", "the identity's current oidc_issuer (exact HTTPS URL)")
	fromSubject := flag.String("from-subject", "", "the identity's current oidc_subject (UUID)")
	toIssuer := flag.String("to-issuer", "", "the identity's new oidc_issuer (exact HTTPS URL)")
	toSubject := flag.String("to-subject", "", "the identity's new oidc_subject (UUID)")
	apply := flag.Bool("apply", false, "actually rewrite the identity and invalidate its sessions (default is a dry run that changes nothing)")
	operatorID := flag.String("operator-id", "", "the approving operator's admin UUID (required with --apply)")
	flag.Parse()
	if flag.NArg() != 0 {
		slog.Error("identity-migrate does not accept positional arguments")
		os.Exit(2)
	}
	for name, value := range map[string]string{"database URL file": *databaseURLFile,
		"field keyring file": *keyringFile, "migrations directory": *migrationsDir} {
		if !filepath.IsAbs(value) {
			slog.Error("identity-migrate path must be absolute", "field", name)
			os.Exit(2)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	in := auth.IdentityMigrationInput{
		FromIssuer: *fromIssuer, FromSubject: *fromSubject,
		ToIssuer: *toIssuer, ToSubject: *toSubject,
		Apply: *apply, OperatorID: *operatorID,
	}
	if err := run(ctx, *databaseURLFile, *keyringFile, *migrationsDir, in, os.Stdout); err != nil {
		slog.Error("identity-migrate failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, databaseURLFile, keyringFile, migrationsDir string, in auth.IdentityMigrationInput, out io.Writer) error {
	databaseURL, err := readOneLineSecret(databaseURLFile)
	if err != nil {
		return fmt.Errorf("read database credential: %w", err)
	}
	keyring, err := securefields.LoadKeyringFile(keyringFile)
	if err != nil {
		return fmt.Errorf("load field keyring: %w", err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	if err = migrate.Verify(ctx, pool, migrationsDir); err != nil {
		return fmt.Errorf("database migration set mismatch: %w", err)
	}

	result, err := auth.MigrateOIDCBinding(ctx, pool, keyring, in)
	if err != nil {
		return fmt.Errorf("migrate oidc binding: %w", err)
	}
	printSummary(out, result)
	return nil
}

func printSummary(out io.Writer, result auth.IdentityMigrationResult) {
	mode := "DRY RUN (nothing was changed)"
	switch {
	case result.AlreadyMigrated:
		mode = "ALREADY MIGRATED (nothing was changed)"
	case result.Applied:
		mode = "APPLIED"
	}
	fmt.Fprintf(out, "identity-migrate XM-INV-IDENTITY-MIGRATE: %s\n\n", mode)
	fmt.Fprintf(out, "from: %s / %s\n", result.FromIssuer, result.FromSubject)
	fmt.Fprintf(out, "to:   %s / %s\n\n", result.ToIssuer, result.ToSubject)
	row := result.Row
	fmt.Fprintf(out, "user_id:             %s\n", row.UserID)
	fmt.Fprintf(out, "status:              %s\n", row.Status)
	fmt.Fprintf(out, "email:               %s\n", row.MaskedEmail)
	fmt.Fprintf(out, "created_at:          %s\n", row.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(out, "auth_sessions_total: %d\n", row.AuthSessionsTotal)
	fmt.Fprintf(out, "auth_sessions_live:  %d\n", row.AuthSessionsLive)
	fmt.Fprintf(out, "audit_rows:          %d\n\n", row.AuditRows)
	switch {
	case result.AlreadyMigrated:
		fmt.Fprintf(out, "this identity was already migrated to --to-issuer/--to-subject; no changes made.\n")
	case result.Applied:
		fmt.Fprintf(out, "invalidated %d live auth_sessions row(s); wrote identity.oidc_binding.migrated audit row.\n", result.SessionsInvalidated)
	default:
		fmt.Fprintf(out, "planned: rewrite oidc_issuer/oidc_subject, invalidate %d live auth_sessions row(s), write identity.oidc_binding.migrated audit row.\n", row.AuthSessionsLive)
	}
}

func readOneLineSecret(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil {
		return "", err
	}
	if len(body) == 0 || len(body) > 64<<10 {
		return "", errors.New("secret file has invalid size")
	}
	body = bytes.TrimSuffix(body, []byte("\r\n"))
	body = bytes.TrimSuffix(body, []byte("\n"))
	if len(body) == 0 || bytes.ContainsAny(body, "\r\n\x00") {
		return "", errors.New("secret file must contain exactly one non-empty line")
	}
	return string(body), nil
}
