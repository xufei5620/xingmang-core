// Command eligibility-repair implements design XM-INV-PREANCHOR-USAGE part 3:
// a versioned, human-approved lifecycle operation that cleans up the
// 2026-09-02 production incident's SOURCE_GAP/EVENT_DEAD freezes and stuck
// source_ingest_events for POLICY_ANCHOR accounts, now that the projection
// rule itself (internal/postgresstore's observeEligibilityFact) no longer
// produces them. Defaults to --dry-run; --apply requires an operator id and
// actually mutates the database. See docs/handoffs/XM-INV-PREANCHOR-USAGE.md
// for the production runbook (expected counts, exact invocation order).
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/migrate"
	"invoice-system/backend/internal/postgresstore"
	"invoice-system/backend/internal/securefields"
)

const fixedResolutionNote = "pre-anchor usage fact skipped by XM-INV-PREANCHOR-USAGE repair"

func main() {
	databaseURLFile := flag.String("database-url-file", "", "absolute path to the database URL secret")
	keyringFile := flag.String("field-keyring-file", "", "absolute path to the field encryption keyring")
	migrationsDir := flag.String("migrations-dir", "/app/migrations", "bundled migration directory")
	apply := flag.Bool("apply", false, "actually resolve freezes and requeue events (default is a dry run that changes nothing)")
	operatorID := flag.String("operator-id", "", "the approving operator's admin UUID (required with --apply)")
	flag.Parse()
	if flag.NArg() != 0 {
		slog.Error("eligibility-repair does not accept positional arguments")
		os.Exit(2)
	}
	for name, value := range map[string]string{"database URL file": *databaseURLFile,
		"field keyring file": *keyringFile, "migrations directory": *migrationsDir} {
		if !filepath.IsAbs(value) {
			slog.Error("eligibility-repair path must be absolute", "field", name)
			os.Exit(2)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := run(ctx, *databaseURLFile, *keyringFile, *migrationsDir, *apply, *operatorID, os.Stdout); err != nil {
		slog.Error("eligibility-repair failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, databaseURLFile, keyringFile, migrationsDir string, apply bool, operatorID string, out io.Writer) error {
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
	store := postgresstore.New(pool)

	in := postgresstore.PreAnchorUsageRepairInput{Apply: apply, OperatorID: operatorID}
	if apply {
		if operatorID == "" {
			return errors.New("--operator-id is required with --apply")
		}
		// Evidence and note are the same fixed text here -- an automated
		// repair has no separate human-authored justification distinct from
		// its own reason -- but are still encrypted and hashed
		// independently (two ciphertexts under two AAD labels), matching the
		// manual resolution path's column shape exactly.
		fixedSum := sha256.Sum256([]byte(fixedResolutionNote))
		fixedHash := hex.EncodeToString(fixedSum[:])
		evidenceCiphertext, encErr := keyring.Encrypt([]byte(fixedResolutionNote),
			"eligibility-repair/XM-INV-PREANCHOR-USAGE/evidence")
		if encErr != nil {
			return fmt.Errorf("encrypt resolution evidence: %w", encErr)
		}
		noteCiphertext, encErr := keyring.Encrypt([]byte(fixedResolutionNote),
			"eligibility-repair/XM-INV-PREANCHOR-USAGE/note")
		if encErr != nil {
			return fmt.Errorf("encrypt resolution note: %w", encErr)
		}
		in.EvidenceHash, in.EvidenceCiphertext = fixedHash, evidenceCiphertext
		in.NoteHash, in.NoteCiphertext = fixedHash, noteCiphertext
	}

	result, err := store.RepairPreAnchorUsageEligibility(ctx, in, postgresstore.AuditActor{
		Type: "admin", ID: operatorID, Reason: "XM-INV-PREANCHOR-USAGE repair tool " + modeLabel(apply)})
	if err != nil {
		return fmt.Errorf("repair pre-anchor usage eligibility: %w", err)
	}
	printSummary(out, result)
	return nil
}

func modeLabel(apply bool) string {
	if apply {
		return "apply"
	}
	return "dry-run"
}

func printSummary(out io.Writer, result postgresstore.PreAnchorUsageRepairResult) {
	mode := "DRY RUN (nothing was changed)"
	if result.Applied {
		mode = "APPLIED"
	}
	fmt.Fprintf(out, "eligibility-repair XM-INV-PREANCHOR-USAGE: %s\n\n", mode)
	fmt.Fprintf(out, "%-38s %10s %10s %10s %11s\n", "ACCOUNT", "SRC_GAP", "EVT_DEAD", "REQUEUED", "REACTIVATED")
	for _, account := range result.Accounts {
		fmt.Fprintf(out, "%-38s %10d %10d %10d %11t\n", account.ExternalAccountID,
			account.SourceGapFreezesResolved, account.EventDeadFreezesResolved,
			account.EventsRequeued, account.Reactivated)
	}
	fmt.Fprintf(out, "\n%-38s %10d %10d %10d\n", "TOTAL", result.TotalSourceGapFreezesResolved,
		result.TotalEventDeadFreezesResolved, result.TotalEventsRequeued)
	fmt.Fprintf(out, "\naccounts affected: %d\n", len(result.Accounts))
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
