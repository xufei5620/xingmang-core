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
//   - --kind=ingest-requeue-dead: XM-INV-DEAD-REQUEUE -- the same operation
//     one layer down, on source_ingest_events: resets every row
//     MarkSourceEventFailed escalated to processing_status='dead' (after 8
//     consecutive attempts) back to 'queued'/attempt_count=0 so the source
//     processor claims it again. Deliberately never touches
//     eligibility_freezes or source_economic_scan_cycles -- it reports both
//     instead, and skips (never silently requeues) an event whose replay the
//     economic fact-context check would refuse, since that can only burn
//     eight attempts and die again. Optional --event narrows to exactly one
//     ingest event id (the exact, recommended form for a reviewed set of
//     rows); optional --account narrows via the event's own open freezes;
//     --include-blocked-cycles overrides the skip. See
//     docs/handoffs/XM-INV-DEAD-REQUEUE.md.
//   - --kind=pending-reevaluate: XM-INV-PENDING-RECON -- asks the projection
//     worker to look at one not_invoiceable_pending_reconciliation account
//     again, now, by enqueueing that account's own projection job. It writes
//     no evidence, no evaluation and no eligibility_status; the worker
//     re-derives and re-evaluates through the ordinary path, so the
//     acceptance line's two-consecutive-matches exit rule is untouched. The
//     dry run is also the diagnosis: six checks (state, open freezes, job
//     row, evidence already waiting, which cycle the evidence would come
//     from, and what the evaluator would say about it) plus the self-dealing
//     guard, each of which refuses the apply when the requeue could not
//     help. --account is required and names exactly one account. See
//     docs/handoffs/XM-INV-PENDING-RECON.md.
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
	kindIngestRequeueDead     = "ingest-requeue-dead"

	// kindIngestAcknowledgeUnreplayable is deliberately its own kind rather
	// than a flag on ingest-requeue-dead: it writes off customer data that
	// can never be recovered, which is a different decision from retrying.
	kindIngestAcknowledgeUnreplayable = "ingest-acknowledge-unreplayable"

	// kindPendingReevaluate (XM-INV-PENDING-RECON) asks the projection worker
	// to look at one not_invoiceable_pending_reconciliation account again,
	// now. It writes no evidence, no evaluation and no eligibility_status of
	// its own -- the worker re-derives and re-evaluates through the ordinary
	// path -- so the acceptance line's two-consecutive-matches exit rule is
	// untouched by it. Its dry run is also the diagnosis: six checks that
	// each refuse the apply when the requeue could not help.
	kindPendingReevaluate = "pending-reevaluate"

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
	kind := flag.String("kind", kindPreAnchorUsage, "which repair to run: pre-anchor-usage (default, design XM-INV-PREANCHOR-USAGE), balance-anchor (design XM-INV-ANCHOR-BALANCE), balance-blip (design XM-INV-BALANCE-BLIP), queue-narrow (design XM-INV-ELIG-SIMPLIFY section 3(C)), policy-start-reanchor (design XM-INV-ELIG-SIMPLIFY section 3(D)), projection-requeue-dead (XM-INV-PROJECTION-FAILURE-GRADING), ingest-requeue-dead (XM-INV-DEAD-REQUEUE), ingest-acknowledge-unreplayable (XM-INV-DEAD-REQUEUE), or pending-reevaluate (XM-INV-PENDING-RECON)")
	accountID := flag.String("account", "", "external account id: an optional filter for projection-requeue-dead and ingest-requeue-dead (empty means every dead row); required, and exactly one account, for pending-reevaluate")
	eventID := flag.String("event", "", "optional source_ingest_events event id filter (ingest-requeue-dead only; empty means every dead ingest event)")
	includeBlockedCycles := flag.Bool("include-blocked-cycles", false, "ingest-requeue-dead only: also requeue events whose replay the runtime would refuse -- an unusable scan-cycle binding, or a binding whose scan_ceiling_at runs past the event's observed_at (skipped by default -- such a requeue can only burn eight attempts and die again)")
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
	filters := repairFilters{accountID: *accountID, eventID: *eventID, includeBlockedCycles: *includeBlockedCycles}
	if err := run(ctx, *databaseURLFile, *keyringFile, *migrationsDir, *apply, *operatorID, *kind, filters, os.Stdout); err != nil {
		slog.Error("eligibility-repair failed", "error", err)
		os.Exit(1)
	}
}

// repairFilters groups the narrowing/override flags that only some kinds
// implement, so adding one does not lengthen run's positional argument list
// again -- and so a caller cannot silently transpose two same-typed
// arguments.
type repairFilters struct {
	accountID            string
	eventID              string
	includeBlockedCycles bool
}

func run(ctx context.Context, databaseURLFile, keyringFile, migrationsDir string, apply bool, operatorID, kind string, filters repairFilters, out io.Writer) error {
	if kind != kindPreAnchorUsage && kind != kindBalanceAnchor && kind != kindBalanceBlip && kind != kindQueueNarrow &&
		kind != kindPolicyStartReanchor && kind != kindProjectionRequeueDead && kind != kindIngestRequeueDead &&
		kind != kindIngestAcknowledgeUnreplayable && kind != kindPendingReevaluate {
		return fmt.Errorf("unknown --kind %q, want one of %q, %q, %q, %q, %q, %q, %q, %q, %q", kind, kindPreAnchorUsage,
			kindBalanceAnchor, kindBalanceBlip, kindQueueNarrow, kindPolicyStartReanchor, kindProjectionRequeueDead,
			kindIngestRequeueDead, kindIngestAcknowledgeUnreplayable, kindPendingReevaluate)
	}
	// A narrowing or override flag that the chosen --kind ignores is rejected
	// rather than silently dropped: an operator who meant to touch three
	// named rows and mistyped --kind must not instead run unnarrowed across
	// every dead row.
	if filters.eventID != "" && kind != kindIngestRequeueDead && kind != kindIngestAcknowledgeUnreplayable {
		return fmt.Errorf("--event is only valid with --kind=%s or --kind=%s", kindIngestRequeueDead, kindIngestAcknowledgeUnreplayable)
	}
	// Acknowledging a fact as permanently lost is never done in bulk.
	if kind == kindIngestAcknowledgeUnreplayable && filters.eventID == "" {
		return fmt.Errorf("--event is required with --kind=%s: this disposition is applied one reviewed event at a time", kindIngestAcknowledgeUnreplayable)
	}
	if filters.accountID != "" && kind == kindIngestAcknowledgeUnreplayable {
		return fmt.Errorf("--account is not valid with --kind=%s; name the event with --event", kindIngestAcknowledgeUnreplayable)
	}
	if filters.includeBlockedCycles && kind != kindIngestRequeueDead {
		return fmt.Errorf("--include-blocked-cycles is only valid with --kind=%s", kindIngestRequeueDead)
	}
	if filters.accountID != "" && kind != kindProjectionRequeueDead && kind != kindIngestRequeueDead &&
		kind != kindPendingReevaluate {
		return fmt.Errorf("--account is only valid with --kind=%s, --kind=%s or --kind=%s",
			kindProjectionRequeueDead, kindIngestRequeueDead, kindPendingReevaluate)
	}
	// Re-evaluating is never done in bulk: it is one reviewed account at a
	// time, the same rule ingest-acknowledge-unreplayable follows for events.
	if kind == kindPendingReevaluate && filters.accountID == "" {
		return fmt.Errorf("--account is required with --kind=%s: this repair is run one reviewed account at a time", kindPendingReevaluate)
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
		return runProjectionRequeueDead(ctx, store, apply, operatorID, filters.accountID, out)
	}
	if kind == kindIngestRequeueDead {
		return runIngestRequeueDead(ctx, store, apply, operatorID, filters, out)
	}
	if kind == kindIngestAcknowledgeUnreplayable {
		return runIngestAcknowledgeUnreplayable(ctx, store, apply, operatorID, filters.eventID, out)
	}
	if kind == kindPendingReevaluate {
		return runPendingReevaluate(ctx, store, apply, operatorID, filters.accountID, out)
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

func runIngestRequeueDead(ctx context.Context, store *postgresstore.Store, apply bool, operatorID string, filters repairFilters, out io.Writer) error {
	// Like projection-requeue-dead, this repair resolves no freeze, so there
	// is no encrypted resolution note or evidence to prepare here. Unlike it,
	// that is not merely "nothing to resolve": see
	// postgresstore.IngestRequeueDeadRepairInput's doc comment for why
	// leaving eligibility_freezes standing is what keeps the failure mode of
	// a second death no worse than today's.
	in := postgresstore.IngestRequeueDeadRepairInput{Apply: apply, OperatorID: operatorID,
		AccountID: filters.accountID, EventID: filters.eventID,
		IncludeBlockedCycles: filters.includeBlockedCycles}
	result, err := store.RepairIngestRequeueDead(ctx, in, postgresstore.AuditActor{
		Type: "admin", ID: operatorID, Reason: "XM-INV-DEAD-REQUEUE repair tool " + modeLabel(apply)})
	if err != nil {
		return fmt.Errorf("repair ingest requeue dead: %w", err)
	}
	printIngestRequeueDeadSummary(out, result)
	return nil
}

func runIngestAcknowledgeUnreplayable(ctx context.Context, store *postgresstore.Store, apply bool,
	operatorID, eventID string, out io.Writer) error {
	result, err := store.AcknowledgeUnreplayableIngestEvent(ctx,
		postgresstore.IngestAcknowledgeUnreplayableInput{Apply: apply, OperatorID: operatorID, EventID: eventID},
		postgresstore.AuditActor{Type: "admin", ID: operatorID,
			Reason: "XM-INV-DEAD-REQUEUE unreplayable acknowledgement " + modeLabel(apply)})
	if err != nil {
		// The event detail is still worth printing on a refusal: the most
		// likely refusal is "this one is replayable after all", and the
		// operator needs to see which binding makes it so.
		if result.Event.EventID != "" {
			printIngestEventDetail(out, result.Event)
		}
		return fmt.Errorf("acknowledge unreplayable ingest event: %w", err)
	}
	printIngestAcknowledgeUnreplayableSummary(out, result)
	return nil
}

func runPendingReevaluate(ctx context.Context, store *postgresstore.Store, apply bool,
	operatorID, accountID string, out io.Writer) error {
	// Like projection-requeue-dead, this repair resolves no eligibility_freezes
	// row -- it only enqueues a projection job -- so there is no encrypted
	// resolution note or evidence to prepare, and the field keyring is not
	// needed at all.
	in := postgresstore.PendingReevaluateRepairInput{Apply: apply, OperatorID: operatorID, AccountID: accountID}
	result, err := store.RepairPendingReevaluate(ctx, in, postgresstore.AuditActor{
		Type: "admin", ID: operatorID, Reason: "XM-INV-PENDING-RECON repair tool " + modeLabel(apply)})
	if err != nil {
		return fmt.Errorf("repair pending reevaluate: %w", err)
	}
	printPendingReevaluateSummary(out, result)
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

// printPendingReevaluateSummary prints the XM-INV-PENDING-RECON report:
// the sibling repairs' banner and fixed-width table, plus the six checks,
// whose explanations are Chinese because the operator on call reads them.
// "accounts affected" counts what actually changed -- zero for a dry run, and
// zero for an apply any check refused.
func printPendingReevaluateSummary(out io.Writer, result postgresstore.PendingReevaluateRepairResult) {
	mode := "DRY RUN (nothing was changed)"
	switch {
	case result.Applied:
		mode = "APPLIED"
	case result.ApplyRequested:
		// Never the dry-run banner here: an operator who typed --apply and
		// read "DRY RUN" would conclude they mistyped the flag, not that the
		// tool refused them.
		mode = "REFUSED (--apply was requested; a check below said STOP, nothing was changed)"
	}
	fmt.Fprintf(out, "eligibility-repair XM-INV-PENDING-RECON: %s\n\n", mode)
	fmt.Fprintf(out, "%-38s %34s %8s %10s\n", "ACCOUNT", "STATUS", "MATCHES", "JOB")
	fmt.Fprintf(out, "%-38s %34s %8s %10s\n", result.AccountID, orNone(result.Status),
		fmt.Sprintf("%d/%d", result.ConsecutiveMatches, result.ExitMatches), orNone(result.JobStatus))
	fmt.Fprintf(out, "\nfinalized_through: %s\n", orNone(formatRepairTime(result.FinalizedThrough)))
	fmt.Fprintf(out, "target cycle:      %s (ceiling %s, in requeue window: %t)\n",
		orNone(result.TargetCycleID), orNone(formatRepairTime(result.TargetCycleAt)), result.TargetInRequeueWindow)
	fmt.Fprintf(out, "prior checkpoint:  %s (as_of %s)\n",
		orNone(result.PriorCheckpointID), orNone(formatRepairTime(result.PriorAsOf)))
	fmt.Fprintf(out, "recomputed:        expected=%s difference=%s -> %s\n",
		orNone(result.RecomputedExpected), orNone(result.RecomputedDifference), orNone(result.RecomputedStatus))

	fmt.Fprintf(out, "\nCHECKS\n")
	for _, check := range result.Checks {
		verdict := "OK  "
		if check.Blocker {
			verdict = "STOP"
		} else if !check.Passed {
			verdict = "NOTE"
		}
		fmt.Fprintf(out, "  [%s] %-12s %s\n", verdict, check.Name, check.Detail)
	}
	if result.Blocked() {
		fmt.Fprintf(out, "\n以上 STOP 项未通过，apply 已被拒绝。\n")
	}
	affected := 0
	if result.Queued {
		affected = 1
	}
	fmt.Fprintf(out, "\naccounts affected: %d\n", affected)
}

// orNone renders an empty string as a dash, so a missing value in the report
// above reads as "there is none" rather than as a gap in the line.
func orNone(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func printIngestRequeueDeadSummary(out io.Writer, result postgresstore.IngestRequeueDeadRepairResult) {
	mode := "DRY RUN (nothing was changed)"
	if result.Applied {
		mode = "APPLIED"
	}
	fmt.Fprintf(out, "eligibility-repair XM-INV-DEAD-REQUEUE: %s\n\n", mode)
	fmt.Fprintf(out, "%-38s %-10s %-18s %8s %21s %s\n", "EVENT", "STREAM", "ENTITY", "ATTEMPTS", "DEAD_SINCE", "REQUEUED")
	for _, event := range result.Events {
		// The REQUEUED column carries the tool's own verdict, never a value
		// the reader has to combine with a runbook rule to interpret.
		verdict := fmt.Sprintf("%t", event.Requeued)
		if event.ReplayBlocked {
			verdict = fmt.Sprintf("%t (replay blocked)", event.Requeued)
		}
		fmt.Fprintf(out, "%-38s %-10s %-18s %8d %21s %s\n", event.EventID, event.StreamID,
			event.EntityType, event.PreviousAttempts, formatRepairTime(event.DeadSince), verdict)
		printIngestEventDetail(out, event)
		if event.ReplayBlocked {
			fmt.Fprintf(out, "  NOT REQUEUED: %s\n", event.ReplayBlockedReason)
			fmt.Fprintf(out, "  (requeuing anyway would spend 8 attempts and re-die; --include-blocked-cycles overrides)\n")
		}
	}
	fmt.Fprintf(out, "\ntotal requeued: %d\n", result.TotalRequeued)
	fmt.Fprintf(out, "not requeued (replay blocked): %d\n", result.TotalBlockedSkipped)
	fmt.Fprintf(out, "events affected: %d\n", len(result.Events))
	if len(result.Errors) > 0 {
		fmt.Fprintf(out, "\nEVENT ERRORS (not applied, other events still processed):\n")
		for _, eventErr := range result.Errors {
			fmt.Fprintf(out, "%-60s %s\n", eventErr.EventKey, eventErr.Message)
		}
	}
}

// printIngestEventDetail prints the evidence block for one dead ingest
// event: what it is, which binding governs a replay, every other binding it
// holds, and the freezes still standing over it. Shared by both ingest kinds
// on purpose -- deciding to requeue an event and deciding to write its fact
// off need exactly the same facts in front of the operator.
func printIngestEventDetail(out io.Writer, event postgresstore.IngestRequeueDeadRepairEvent) {
	fmt.Fprintf(out, "  source=%s stream=%s entity=%s operation=%s\n",
		event.SourceInstanceID, event.StreamID, event.EntityType, event.Operation)
	fmt.Fprintf(out, "  created_at=%s dead_since=%s attempts=%d catchup_key=%t\n",
		formatRepairTime(event.CreatedAt), formatRepairTime(event.DeadSince),
		event.PreviousAttempts, event.HasCatchupKey)
	fmt.Fprintf(out, "  payload_hash=%s\n", event.PayloadHash)
	if event.ProcessingError != "" {
		fmt.Fprintf(out, "  processing_error: %s\n", event.ProcessingError)
	}
	// The replay binding is the only cycle that governs: it is the
	// (event, batch, cycle) triple verifyFactBatchContextTx is handed. Other
	// mappings exist when a later rescan re-delivered the same deterministic
	// event id, and are printed as evidence -- but a healthy cycle among them
	// does not make the event replayable.
	if event.ReplayScanCycleID == "" {
		fmt.Fprintf(out, "  replay_binding: (none -- not a v3 economic fact; no cycle gate applies)\n")
	} else {
		fmt.Fprintf(out, "  replay_binding: cycle=%s cycle_status=%s batch=%s\n",
			event.ReplayScanCycleID, event.ReplayCycleStatus, event.ReplayBatchID)
	}
	for _, cycle := range event.ScanCycles {
		role := "other binding, NOT used by replay"
		if cycle.ReplayBinding {
			role = "replay binding"
		}
		fmt.Fprintf(out, "  scan_cycle: %s cycle_status=%-10s batch=%s (%s)\n",
			cycle.ScanCycleID, cycle.CycleStatus, cycle.BatchID, role)
	}
	if len(event.ScanCycles) == 0 {
		fmt.Fprintf(out, "  scan_cycle: (none -- not mapped to a schema v3 economic cycle)\n")
	}
	for _, freeze := range event.OpenFreezes {
		fmt.Fprintf(out, "  open_freeze: %s account=%s reason=%s (left open on purpose)\n",
			freeze.FreezeID, freeze.ExternalAccountID, freeze.FreezeReason)
	}
	if len(event.OpenFreezes) == 0 {
		fmt.Fprintf(out, "  open_freeze: (none correlated to this payload hash)\n")
	}
}

func printIngestAcknowledgeUnreplayableSummary(out io.Writer, result postgresstore.IngestAcknowledgeUnreplayableResult) {
	mode := "DRY RUN (nothing was changed)"
	if result.Applied {
		mode = "APPLIED"
	}
	fmt.Fprintf(out, "eligibility-repair XM-INV-DEAD-REQUEUE acknowledge-unreplayable: %s\n\n", mode)
	fmt.Fprintf(out, "EVENT %s\n", result.Event.EventID)
	printIngestEventDetail(out, result.Event)
	fmt.Fprintf(out, "  unreplayable because: %s\n", result.Event.ReplayBlockedReason)
	fmt.Fprintf(out, "\nacknowledged: %t\n", result.Acknowledged)
	// Said plainly, because this is the point of the operation and the point
	// an operator must be able to defend afterwards.
	fmt.Fprintf(out, "the event is closed as processing_status='processed' / processing_error='%s'.\n",
		"UNREPLAYABLE_BINDING")
	fmt.Fprintf(out, "NO fact is written: this records that the fact will never be applied, not that it was.\n")
	fmt.Fprintf(out, "open freezes are left exactly as they are; resolve them through the admin path.\n")
}

// formatRepairTime renders a timestamp for the summary table above, leaving
// a zero time blank rather than printing a year-1 placeholder.
func formatRepairTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
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
