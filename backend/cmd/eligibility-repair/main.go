// Command eligibility-repair implements two versioned, human-approved
// lifecycle operations that clean up production incidents left behind by
// design XM-INV-POLICY-ANCHOR, now that the underlying projection bugs are
// fixed:
//   - --kind=pre-anchor-usage (the default, unchanged): design
//     XM-INV-PREANCHOR-USAGE's SOURCE_GAP/EVENT_DEAD freezes and stuck
//     source_ingest_events for POLICY_ANCHOR accounts. See
//     docs/handoffs/XM-INV-PREANCHOR-USAGE.md.
//   - --kind=balance-anchor: design XM-INV-ANCHOR-BALANCE's SOURCE_GAP
//     freezes on a POLICY_ANCHOR account's own balance_checkpoint/
//     balance_carry_forward_proof evidence. See
//     docs/handoffs/XM-INV-ANCHOR-BALANCE.md.
//
// Defaults to --dry-run; --apply requires an operator id and actually
// mutates the database. Omitting --kind reproduces this tool's original
// (pre-anchor-usage) behavior exactly, so any existing invocation (e.g.
// rc70-repair.sh) keeps working unchanged.
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

const (
	kindPreAnchorUsage = "pre-anchor-usage"
	kindBalanceAnchor  = "balance-anchor"

	preAnchorUsageFixedResolutionNote = "pre-anchor usage fact skipped by XM-INV-PREANCHOR-USAGE repair"
	balanceAnchorFixedResolutionNote  = "POLICY_ANCHOR balance evidence self-healed by XM-INV-ANCHOR-BALANCE repair"
)

func main() {
	databaseURLFile := flag.String("database-url-file", "", "absolute path to the database URL secret")
	keyringFile := flag.String("field-keyring-file", "", "absolute path to the field encryption keyring")
	migrationsDir := flag.String("migrations-dir", "/app/migrations", "bundled migration directory")
	apply := flag.Bool("apply", false, "actually resolve freezes (and, for pre-anchor-usage, requeue events; default is a dry run that changes nothing)")
	operatorID := flag.String("operator-id", "", "the approving operator's admin UUID (required with --apply)")
	kind := flag.String("kind", kindPreAnchorUsage, "which repair to run: pre-anchor-usage (default, design XM-INV-PREANCHOR-USAGE) or balance-anchor (design XM-INV-ANCHOR-BALANCE)")
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
	if err := run(ctx, *databaseURLFile, *keyringFile, *migrationsDir, *apply, *operatorID, *kind, os.Stdout); err != nil {
		slog.Error("eligibility-repair failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, databaseURLFile, keyringFile, migrationsDir string, apply bool, operatorID, kind string, out io.Writer) error {
	if kind != kindPreAnchorUsage && kind != kindBalanceAnchor {
		return fmt.Errorf("unknown --kind %q, want %q or %q", kind, kindPreAnchorUsage, kindBalanceAnchor)
	}
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

	if apply && operatorID == "" {
		return errors.New("--operator-id is required with --apply")
	}
	if kind == kindBalanceAnchor {
		return runBalanceAnchor(ctx, store, apply, operatorID, keyring, out)
	}
	return runPreAnchorUsage(ctx, store, apply, operatorID, keyring, out)
}

func runPreAnchorUsage(ctx context.Context, store *postgresstore.Store, apply bool, operatorID string, keyring securefields.Keyring, out io.Writer) error {
	in := postgresstore.PreAnchorUsageRepairInput{Apply: apply, OperatorID: operatorID}
	if apply {
		// Evidence and note are the same fixed text here -- an automated
		// repair has no separate human-authored justification distinct from
		// its own reason -- but are still encrypted and hashed
		// independently (two ciphertexts under two AAD labels), matching the
		// manual resolution path's column shape exactly.
		hash, evidenceCiphertext, noteCiphertext, encErr := encryptFixedResolution(keyring,
			preAnchorUsageFixedResolutionNote, "XM-INV-PREANCHOR-USAGE")
		if encErr != nil {
			return encErr
		}
		in.EvidenceHash, in.EvidenceCiphertext = hash, evidenceCiphertext
		in.NoteHash, in.NoteCiphertext = hash, noteCiphertext
	}
	result, err := store.RepairPreAnchorUsageEligibility(ctx, in, postgresstore.AuditActor{
		Type: "admin", ID: operatorID, Reason: "XM-INV-PREANCHOR-USAGE repair tool " + modeLabel(apply)})
	if err != nil {
		return fmt.Errorf("repair pre-anchor usage eligibility: %w", err)
	}
	printPreAnchorUsageSummary(out, result)
	return nil
}

func runBalanceAnchor(ctx context.Context, store *postgresstore.Store, apply bool, operatorID string, keyring securefields.Keyring, out io.Writer) error {
	in := postgresstore.BalanceAnchorRepairInput{Apply: apply, OperatorID: operatorID}
	if apply {
		hash, evidenceCiphertext, noteCiphertext, encErr := encryptFixedResolution(keyring,
			balanceAnchorFixedResolutionNote, "XM-INV-ANCHOR-BALANCE")
		if encErr != nil {
			return encErr
		}
		in.EvidenceHash, in.EvidenceCiphertext = hash, evidenceCiphertext
		in.NoteHash, in.NoteCiphertext = hash, noteCiphertext
	}
	result, err := store.RepairBalanceAnchorEligibility(ctx, in, postgresstore.AuditActor{
		Type: "admin", ID: operatorID, Reason: "XM-INV-ANCHOR-BALANCE repair tool " + modeLabel(apply)})
	if err != nil {
		return fmt.Errorf("repair balance anchor eligibility: %w", err)
	}
	printBalanceAnchorSummary(out, result)
	return nil
}

// encryptFixedResolution encrypts the same fixed note text under two AAD
// labels (evidence and note), matching every repair kind's identical
// column-shape requirement, and returns the shared hex hash plus both
// ciphertexts.
func encryptFixedResolution(keyring securefields.Keyring, note, aadPrefix string) (hash string, evidenceCiphertext, noteCiphertext []byte, err error) {
	sum := sha256.Sum256([]byte(note))
	hash = hex.EncodeToString(sum[:])
	evidenceCiphertext, err = keyring.Encrypt([]byte(note), "eligibility-repair/"+aadPrefix+"/evidence")
	if err != nil {
		return "", nil, nil, fmt.Errorf("encrypt resolution evidence: %w", err)
	}
	noteCiphertext, err = keyring.Encrypt([]byte(note), "eligibility-repair/"+aadPrefix+"/note")
	if err != nil {
		return "", nil, nil, fmt.Errorf("encrypt resolution note: %w", err)
	}
	return hash, evidenceCiphertext, noteCiphertext, nil
}

func modeLabel(apply bool) string {
	if apply {
		return "apply"
	}
	return "dry-run"
}

func printPreAnchorUsageSummary(out io.Writer, result postgresstore.PreAnchorUsageRepairResult) {
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

func printBalanceAnchorSummary(out io.Writer, result postgresstore.BalanceAnchorRepairResult) {
	mode := "DRY RUN (nothing was changed)"
	if result.Applied {
		mode = "APPLIED"
	}
	fmt.Fprintf(out, "eligibility-repair XM-INV-ANCHOR-BALANCE: %s\n\n", mode)
	fmt.Fprintf(out, "%-38s %10s %11s %11s\n", "ACCOUNT", "SRC_GAP", "CKPT_RESET", "REACTIVATED")
	for _, account := range result.Accounts {
		fmt.Fprintf(out, "%-38s %10d %11d %11t\n", account.ExternalAccountID,
			account.SourceGapFreezesResolved, account.CheckpointEvaluationsReset, account.Reactivated)
	}
	fmt.Fprintf(out, "\n%-38s %10d %11d\n", "TOTAL", result.TotalSourceGapFreezesResolved,
		result.TotalCheckpointEvaluationsReset)
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
