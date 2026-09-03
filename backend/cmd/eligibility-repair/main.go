// Command eligibility-repair implements three versioned, human-approved
// lifecycle operations that clean up production incidents left behind by
// design XM-INV-POLICY-ANCHOR and its balance-evidence evaluator, now that
// the underlying bugs are fixed:
//   - --kind=pre-anchor-usage (the default, unchanged): design
//     XM-INV-PREANCHOR-USAGE's SOURCE_GAP/EVENT_DEAD freezes and stuck
//     source_ingest_events for POLICY_ANCHOR accounts. See
//     docs/handoffs/XM-INV-PREANCHOR-USAGE.md.
//   - --kind=balance-anchor: design XM-INV-ANCHOR-BALANCE's SOURCE_GAP
//     freezes on a POLICY_ANCHOR account's own balance_checkpoint/
//     balance_carry_forward_proof evidence. See
//     docs/handoffs/XM-INV-ANCHOR-BALANCE.md.
//   - --kind=balance-blip: design XM-INV-BALANCE-BLIP's synthesized
//     UNKNOWN_POSITIVE credits from a single transient positive checkpoint,
//     and the UNKNOWN_NEGATIVE_BALANCE freezes that credit's permanent
//     excess produced on every checkpoint after it. See
//     docs/handoffs/XM-INV-BALANCE-BLIP.md.
//   - --kind=queue-narrow: design XM-INV-ELIG-SIMPLIFY section 3(C)'s
//     manual-queue narrowing -- every still-open UNKNOWN_NEGATIVE_BALANCE,
//     USAGE_EXCEEDS_LEDGER or funding_lot-less LATE_FINALIZED_EVENT freeze
//     left over from before XM-INV-ELIG-AUTO-RECONCILE and
//     XM-INV-ELIG-QUEUE-NARROW shipped. See
//     docs/handoffs/XM-INV-ELIG-QUEUE-NARROW.md.
//   - --kind=policy-start-reanchor: design XM-INV-ELIG-SIMPLIFY section
//     3(D) -- re-anchors the POLICY_ANCHOR accounts bootstrapped before
//     that slice shipped (cutover_at still the triggering checkpoint's own
//     as_of, not the global policy start) so a real in-window cash payment
//     is no longer permanently excluded. An account with no in-window
//     funding lot is left untouched in both modes ("expected no-op"). See
//     docs/handoffs/XM-INV-ELIG-POLICY-START-ANCHOR.md.
//   - --kind=projection-requeue-dead: XM-INV-PROJECTION-FAILURE-GRADING --
//     resets every eligibility_projection_jobs row the failure grading
//     escalated to the terminal status='dead' (after 8 consecutive
//     per-account processing errors) back to status='queued'/attempts=0 so
//     the worker retries it. Optional --account narrows to one external
//     account id. See docs/handoffs/XM-INV-PROJECTION-FAILURE-GRADING.md.
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
	kindPreAnchorUsage        = "pre-anchor-usage"
	kindBalanceAnchor         = "balance-anchor"
	kindBalanceBlip           = "balance-blip"
	kindQueueNarrow           = "queue-narrow"
	kindPolicyStartReanchor   = "policy-start-reanchor"
	kindProjectionRequeueDead = "projection-requeue-dead"

	preAnchorUsageFixedResolutionNote = "pre-anchor usage fact skipped by XM-INV-PREANCHOR-USAGE repair"
	balanceAnchorFixedResolutionNote  = "POLICY_ANCHOR balance evidence self-healed by XM-INV-ANCHOR-BALANCE repair"
	balanceBlipFixedResolutionNote    = "balance blip credit reversed by XM-INV-BALANCE-BLIP repair"
	// queueNarrowFixedResolutionNote is design section 3(C) item 3's own
	// specified fixed resolution note text, verbatim.
	queueNarrowFixedResolutionNote = "由 XM-INV-ELIG-SIMPLIFY 迁移自动解除"
)

func main() {
	databaseURLFile := flag.String("database-url-file", "", "absolute path to the database URL secret")
	keyringFile := flag.String("field-keyring-file", "", "absolute path to the field encryption keyring")
	migrationsDir := flag.String("migrations-dir", "/app/migrations", "bundled migration directory")
	apply := flag.Bool("apply", false, "actually resolve freezes (and, for pre-anchor-usage, requeue events; default is a dry run that changes nothing)")
	operatorID := flag.String("operator-id", "", "the approving operator's admin UUID (required with --apply)")
	kind := flag.String("kind", kindPreAnchorUsage, "which repair to run: pre-anchor-usage (default, design XM-INV-PREANCHOR-USAGE), balance-anchor (design XM-INV-ANCHOR-BALANCE), balance-blip (design XM-INV-BALANCE-BLIP), queue-narrow (design XM-INV-ELIG-SIMPLIFY section 3(C)), policy-start-reanchor (design XM-INV-ELIG-SIMPLIFY section 3(D)), or projection-requeue-dead (XM-INV-PROJECTION-FAILURE-GRADING)")
	accountID := flag.String("account", "", "optional external account id filter (projection-requeue-dead only; empty means every dead job)")
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
	if err := run(ctx, *databaseURLFile, *keyringFile, *migrationsDir, *apply, *operatorID, *kind, *accountID, os.Stdout); err != nil {
		slog.Error("eligibility-repair failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, databaseURLFile, keyringFile, migrationsDir string, apply bool, operatorID, kind, accountID string, out io.Writer) error {
	if kind != kindPreAnchorUsage && kind != kindBalanceAnchor && kind != kindBalanceBlip && kind != kindQueueNarrow &&
		kind != kindPolicyStartReanchor && kind != kindProjectionRequeueDead {
		return fmt.Errorf("unknown --kind %q, want %q, %q, %q, %q, %q or %q", kind, kindPreAnchorUsage, kindBalanceAnchor,
			kindBalanceBlip, kindQueueNarrow, kindPolicyStartReanchor, kindProjectionRequeueDead)
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
	if kind == kindBalanceBlip {
		return runBalanceBlip(ctx, store, apply, operatorID, keyring, out)
	}
	if kind == kindQueueNarrow {
		return runQueueNarrow(ctx, store, apply, operatorID, keyring, out)
	}
	if kind == kindPolicyStartReanchor {
		return runPolicyStartReanchor(ctx, store, apply, operatorID, out)
	}
	if kind == kindProjectionRequeueDead {
		return runProjectionRequeueDead(ctx, store, apply, operatorID, accountID, out)
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

func runBalanceBlip(ctx context.Context, store *postgresstore.Store, apply bool, operatorID string, keyring securefields.Keyring, out io.Writer) error {
	in := postgresstore.BalanceBlipRepairInput{Apply: apply, OperatorID: operatorID}
	if apply {
		hash, evidenceCiphertext, noteCiphertext, encErr := encryptFixedResolution(keyring,
			balanceBlipFixedResolutionNote, "XM-INV-BALANCE-BLIP")
		if encErr != nil {
			return encErr
		}
		in.EvidenceHash, in.EvidenceCiphertext = hash, evidenceCiphertext
		in.NoteHash, in.NoteCiphertext = hash, noteCiphertext
	}
	result, err := store.RepairBalanceBlipEligibility(ctx, in, postgresstore.AuditActor{
		Type: "admin", ID: operatorID, Reason: "XM-INV-BALANCE-BLIP repair tool " + modeLabel(apply)})
	if err != nil {
		return fmt.Errorf("repair balance blip eligibility: %w", err)
	}
	printBalanceBlipSummary(out, result)
	return nil
}

func runQueueNarrow(ctx context.Context, store *postgresstore.Store, apply bool, operatorID string, keyring securefields.Keyring, out io.Writer) error {
	in := postgresstore.QueueNarrowRepairInput{Apply: apply, OperatorID: operatorID}
	if apply {
		hash, evidenceCiphertext, noteCiphertext, encErr := encryptFixedResolution(keyring,
			queueNarrowFixedResolutionNote, "XM-INV-ELIG-QUEUE-NARROW")
		if encErr != nil {
			return encErr
		}
		in.EvidenceHash, in.EvidenceCiphertext = hash, evidenceCiphertext
		in.NoteHash, in.NoteCiphertext = hash, noteCiphertext
	}
	result, err := store.RepairQueueNarrowEligibility(ctx, in, postgresstore.AuditActor{
		Type: "admin", ID: operatorID, Reason: "XM-INV-ELIG-QUEUE-NARROW repair tool " + modeLabel(apply)})
	if err != nil {
		return fmt.Errorf("repair queue narrow eligibility: %w", err)
	}
	printQueueNarrowSummary(out, result)
	return nil
}

func runProjectionRequeueDead(ctx context.Context, store *postgresstore.Store, apply bool, operatorID, accountID string, out io.Writer) error {
	// Unlike the freeze-resolution repairs, this one never resolves an
	// eligibility_freezes row, so there is no encrypted resolution note or
	// evidence to prepare here -- same reasoning as policy-start-reanchor.
	in := postgresstore.ProjectionRequeueDeadRepairInput{Apply: apply, OperatorID: operatorID, AccountID: accountID}
	result, err := store.RepairProjectionRequeueDead(ctx, in, postgresstore.AuditActor{
		Type: "admin", ID: operatorID, Reason: "XM-INV-PROJECTION-FAILURE-GRADING repair tool " + modeLabel(apply)})
	if err != nil {
		return fmt.Errorf("repair projection requeue dead: %w", err)
	}
	printProjectionRequeueDeadSummary(out, result)
	return nil
}

func runPolicyStartReanchor(ctx context.Context, store *postgresstore.Store, apply bool, operatorID string, out io.Writer) error {
	// Unlike the other three kinds, this repair never resolves an
	// eligibility_freezes row (a POLICY_ANCHOR account's cutover boundary is
	// not gated behind one), so there is no encrypted resolution note or
	// evidence to prepare here.
	in := postgresstore.PolicyStartReanchorRepairInput{Apply: apply, OperatorID: operatorID}
	result, err := store.RepairPolicyStartReanchorEligibility(ctx, in, postgresstore.AuditActor{
		Type: "admin", ID: operatorID, Reason: "XM-INV-ELIG-POLICY-START-ANCHOR repair tool " + modeLabel(apply)})
	if err != nil {
		return fmt.Errorf("repair policy start reanchor eligibility: %w", err)
	}
	printPolicyStartReanchorSummary(out, result)
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

func printBalanceBlipSummary(out io.Writer, result postgresstore.BalanceBlipRepairResult) {
	mode := "DRY RUN (nothing was changed)"
	if result.Applied {
		mode = "APPLIED"
	}
	fmt.Fprintf(out, "eligibility-repair XM-INV-BALANCE-BLIP: %s\n\n", mode)
	fmt.Fprintf(out, "%-38s %10s %11s %11s %11s\n", "ACCOUNT", "CREDITS", "FREEZES", "CKPT_RESET", "REACTIVATED")
	for _, account := range result.Accounts {
		fmt.Fprintf(out, "%-38s %10d %11d %11d %11t\n", account.ExternalAccountID,
			account.BlipCreditsRemoved, account.NegativeFreezesResolved,
			account.CheckpointEvaluationsReset, account.Reactivated)
	}
	fmt.Fprintf(out, "\n%-38s %10d %11d %11d\n", "TOTAL", result.TotalBlipCreditsRemoved,
		result.TotalNegativeFreezesResolved, result.TotalCheckpointEvaluationsReset)
	fmt.Fprintf(out, "\naccounts affected: %d\n", len(result.Accounts))
}

func printQueueNarrowSummary(out io.Writer, result postgresstore.QueueNarrowRepairResult) {
	mode := "DRY RUN (nothing was changed)"
	if result.Applied {
		mode = "APPLIED"
	}
	fmt.Fprintf(out, "eligibility-repair XM-INV-ELIG-QUEUE-NARROW: %s\n\n", mode)
	fmt.Fprintf(out, "%-38s %8s %9s %9s %11s %9s %11s\n", "ACCOUNT", "NEG_BAL", "USAGE_EXC", "LATE_FACT", "PEND_RECON", "OVERAGE", "REACTIVATED")
	for _, account := range result.Accounts {
		fmt.Fprintf(out, "%-38s %8d %9d %9d %11t %9t %11t\n", account.ExternalAccountID,
			account.NegativeBalanceFreezesResolved, account.UsageExceedsLedgerFreezesResolved,
			account.LateFactFreezesResolved, account.RebuiltPendingReconciliation,
			account.UsageOverageReprojected, account.Reactivated)
	}
	fmt.Fprintf(out, "\n%-38s %8d %9d %9d\n", "TOTAL", result.TotalNegativeBalanceFreezesResolved,
		result.TotalUsageExceedsLedgerFreezesResolved, result.TotalLateFactFreezesResolved)
	fmt.Fprintf(out, "\naccounts affected: %d\n", len(result.Accounts))
	if len(result.Errors) > 0 {
		fmt.Fprintf(out, "\nACCOUNT ERRORS (not applied, other accounts still processed):\n")
		for _, accountErr := range result.Errors {
			fmt.Fprintf(out, "%-38s %s\n", accountErr.ExternalAccountID, accountErr.Message)
		}
	}
}

func printPolicyStartReanchorSummary(out io.Writer, result postgresstore.PolicyStartReanchorRepairResult) {
	mode := "DRY RUN (nothing was changed)"
	if result.Applied {
		mode = "APPLIED"
	}
	fmt.Fprintf(out, "eligibility-repair XM-INV-ELIG-POLICY-START-ANCHOR: %s\n\n", mode)
	fmt.Fprintf(out, "%-38s %20s %20s %9s %6s %8s %11s\n", "ACCOUNT", "OLD_CUTOVER_AT", "NEW_CUTOVER_AT", "WIN_LOTS", "NOOP", "BLOCKED", "REPROJECTED")
	for _, account := range result.Accounts {
		fmt.Fprintf(out, "%-38s %20s %20s %9d %6t %8t %11t\n", account.ExternalAccountID,
			account.OldCutoverAt.UTC().Format(time.RFC3339), account.NewCutoverAt.UTC().Format(time.RFC3339),
			account.WindowFundingLots, account.NoOp, account.Blocked, account.Reprojected)
		fmt.Fprintf(out, "  old_balance=%s new_balance=%s\n", account.OldCutoverBalanceUnits, account.NewCutoverBalanceUnits)
	}
	fmt.Fprintf(out, "\naccounts examined: %d\n", len(result.Accounts))
	if len(result.Errors) > 0 {
		fmt.Fprintf(out, "\nACCOUNT ERRORS (not applied, other accounts still processed):\n")
		for _, accountErr := range result.Errors {
			fmt.Fprintf(out, "%-38s %s\n", accountErr.ExternalAccountID, accountErr.Message)
		}
	}
}

func printProjectionRequeueDeadSummary(out io.Writer, result postgresstore.ProjectionRequeueDeadRepairResult) {
	mode := "DRY RUN (nothing was changed)"
	if result.Applied {
		mode = "APPLIED"
	}
	fmt.Fprintf(out, "eligibility-repair XM-INV-PROJECTION-FAILURE-GRADING: %s\n\n", mode)
	fmt.Fprintf(out, "%-38s %8s %10s %16s %10s\n", "ACCOUNT", "ATTEMPTS", "ERROR_CODE", "DEAD_SINCE", "REQUEUED")
	for _, account := range result.Accounts {
		deadSince := ""
		if !account.DeadSince.IsZero() {
			deadSince = account.DeadSince.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(out, "%-38s %8d %10s %16s %10t\n", account.ExternalAccountID,
			account.PreviousAttempts, account.LastErrorCode, deadSince, account.Requeued)
		if account.LastError != "" {
			fmt.Fprintf(out, "  last_error: %s\n", account.LastError)
		}
	}
	fmt.Fprintf(out, "\ntotal requeued: %d\n", result.TotalRequeued)
	fmt.Fprintf(out, "accounts affected: %d\n", len(result.Accounts))
	if len(result.Errors) > 0 {
		fmt.Fprintf(out, "\nACCOUNT ERRORS (not applied, other accounts still processed):\n")
		for _, accountErr := range result.Errors {
			fmt.Fprintf(out, "%-38s %s\n", accountErr.ExternalAccountID, accountErr.Message)
		}
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
