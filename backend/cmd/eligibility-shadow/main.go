// Command eligibility-shadow is XM-INV-SHADOW-EVAL's release-rehearsal
// projection driver. It is invoked, always inside the tools image, by
// deploy/rehearsal/shadow-eval.sh against a throwaway PostgreSQL container
// that script has already restored a signed production backup into on an
// isolated Docker network -- never against a live production database.
//
// It takes a point-in-time snapshot of every account's eligibility status,
// open freezes by reason, and balance-evidence evaluation outcomes right
// after the restore (the "before" baseline), drives the exact production
// eligibility-projection worker entry points
// (postgresstore.ProcessEligibilityProjectionJobs /
// postgresstore.EligibilityProjectionHealth) until the queue has nothing
// left immediately claimable or --max-rounds is reached, takes the same
// snapshot again (the "after" state), and prints one JSON report to stdout
// describing the difference. It never crashes the run on a per-account
// projection error -- ProcessEligibilityProjectionJobs already guarantees
// that for the batch it claims -- and instead records every such error in
// the report.
//
// Exit codes: 0 when the report's verdict is "ready" (no new freeze-reason
// category and no projection error versus the pre-run baseline); 3 when the
// candidate regressed (see Report.Verdict / EvaluateReadiness); 2 for a
// flag-usage error; 1 for any other execution failure (cannot reach the
// database, migration set mismatch, a snapshot query itself failing).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/migrate"
	"invoice-system/backend/internal/postgresstore"
)

// shadowEvalActor identifies every audit_events row this rehearsal writes
// (freezes/reactivations the candidate's projection logic produces while
// draining the restored copy's queue) as coming from the rehearsal tool
// itself, never a real operator -- the restored database is a throwaway
// container torn down at the end of the rehearsal, but any audit trail it
// leaves behind while it exists should be unambiguous about its source.
var shadowEvalActor = postgresstore.AuditActor{
	Type: "system", ID: "xm-inv-shadow-eval", Reason: "XM-INV-SHADOW-EVAL release rehearsal",
}

func main() {
	databaseURLFile := flag.String("database-url-file", "", "absolute path to the restored database's owner database URL secret")
	migrationsDir := flag.String("migrations-dir", "/app/migrations", "bundled migration directory")
	maxRounds := flag.Int("max-rounds", 200, "maximum ProcessEligibilityProjectionJobs rounds before giving up on draining the queue (1-10000)")
	batchLimit := flag.Int("batch-limit", 25, "accounts claimed per round (1-100, same cap ProcessEligibilityProjectionJobs itself enforces)")
	timeout := flag.Duration("timeout", 25*time.Minute, "overall context budget for the whole drain loop")
	backupLabel := flag.String("backup-label", "", "identifying label of the restored backup, echoed into the report")
	candidateTag := flag.String("candidate-image-tag", "", "the candidate release's image tag, echoed into the report")
	migrationsApplied := flag.String("migrations-applied", "", "comma-separated migration file names deploy/rehearsal/shadow-eval.sh's own invoice-migrate step newly applied before this run, empty when the restored backup was already current")
	evidenceBatchLimit := flag.Int("evidence-batch-limit", 0, "bound on pending balance-evidence items one projection job evaluates (0 = unbounded, the production default until the differential rehearsal proves the bound equivalent); the same image runs both modes so two rehearsals of one backup can be diffed")
	reprojectAll := flag.Bool("reproject-all", false, "queue one projection job per account at its own finalized_through before draining, so the candidate evaluator actually runs against the restored data; required for any release that changes the evaluator, the projection or a migration feeding either")
	flag.Parse()

	if flag.NArg() != 0 {
		slog.Error("eligibility-shadow does not accept positional arguments")
		os.Exit(2)
	}
	if !filepath.IsAbs(*databaseURLFile) || !filepath.IsAbs(*migrationsDir) {
		slog.Error("database-url-file and migrations-dir must be absolute paths")
		os.Exit(2)
	}
	if *maxRounds < 1 || *maxRounds > 10000 {
		slog.Error("max-rounds must be 1-10000")
		os.Exit(2)
	}
	if *batchLimit < 1 || *batchLimit > 100 {
		slog.Error("batch-limit must be 1-100")
		os.Exit(2)
	}
	if *evidenceBatchLimit < 0 || *evidenceBatchLimit > 10000 {
		slog.Error("evidence-batch-limit must be 0-10000")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	databaseURL, err := readOneLineSecret(*databaseURLFile)
	if err != nil {
		slog.Error("read database credential", "error", err)
		os.Exit(1)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err = pool.Ping(ctx); err != nil {
		slog.Error("ping database", "error", err)
		os.Exit(1)
	}
	if err = migrate.Verify(ctx, pool, *migrationsDir); err != nil {
		slog.Error("database migration set mismatch", "error", err)
		os.Exit(1)
	}
	store := postgresstore.New(pool)
	store.SetEvidenceBatchLimit(*evidenceBatchLimit)

	report, err := run(ctx, store, runOptions{
		MaxRounds: *maxRounds, BatchLimit: *batchLimit,
		BackupLabel: *backupLabel, CandidateImageTag: *candidateTag,
		MigrationsApplied:  parseMigrationsApplied(*migrationsApplied),
		ReprojectAll:       *reprojectAll,
		EvidenceBatchLimit: *evidenceBatchLimit,
	})
	if err != nil {
		slog.Error("eligibility-shadow rehearsal failed", "error", err)
		os.Exit(1)
	}

	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		slog.Error("encode shadow-eval report", "error", err)
		os.Exit(1)
	}
	// The JSON report is the only thing this command prints to stdout --
	// deploy/rehearsal/shadow-eval.sh redirects stdout straight to
	// shadow-eval.json. Every other message (including this human summary)
	// goes to stderr via slog's default handler.
	fmt.Println(string(encoded))
	slog.Info("shadow-eval rehearsal complete", "verdict", report.Verdict, "rounds_run", report.RoundsRun,
		"queue_drained", report.QueueDrained, "new_freeze_reasons", report.NewFreezeReasons,
		"failed_accounts", len(report.FailedAccounts), "round_errors", len(report.RoundErrors))
	os.Exit(ExitCode(report))
}

type runOptions struct {
	MaxRounds, BatchLimit          int
	BackupLabel, CandidateImageTag string
	MigrationsApplied              []string
	ReprojectAll                   bool
	EvidenceBatchLimit             int
}

// parseMigrationsApplied splits --migrations-applied's comma-separated
// value into a slice, trimming whitespace and dropping empty entries so an
// empty flag (the restored backup was already at the candidate's migration
// set -- nothing for deploy/rehearsal/shadow-eval.sh's invoice-migrate step
// to apply) yields a nil slice, matching Report.MigrationsApplied's
// "nil means none" convention shared with RoundErrors/FailedAccounts.
func parseMigrationsApplied(value string) []string {
	var names []string
	for _, name := range strings.Split(value, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func run(ctx context.Context, store *postgresstore.Store, opts runOptions) (Report, error) {
	report := Report{
		GeneratedAt: time.Now().UTC(), BackupLabel: opts.BackupLabel,
		CandidateImageTag: opts.CandidateImageTag, MaxRounds: opts.MaxRounds,
		MigrationsApplied:     opts.MigrationsApplied,
		ReprojectAllRequested: opts.ReprojectAll,
		EvidenceBatchLimit:    opts.EvidenceBatchLimit,
	}

	// Enqueue before the baseline snapshot, so BeforeHealth records the work
	// this run set itself rather than an empty queue that would look exactly
	// like the vacuous runs this flag exists to end. The enqueue changes no
	// account state -- only eligibility_projection_jobs -- so the baseline
	// it precedes is still the restored data as the backup captured it.
	if opts.ReprojectAll {
		enqueued, enqueueErr := store.EnqueueEligibilityShadowReprojection(ctx)
		if enqueueErr != nil {
			return report, fmt.Errorf("queue full reprojection: %w", enqueueErr)
		}
		report.AccountsEnqueued = enqueued
	}

	before, err := store.EligibilityShadowSnapshot(ctx)
	if err != nil {
		return report, fmt.Errorf("take pre-projection snapshot: %w", err)
	}
	report.Before = toReportSnapshot(before)
	beforeHealth, err := store.EligibilityProjectionHealth(ctx)
	if err != nil {
		return report, fmt.Errorf("read pre-projection health: %w", err)
	}
	report.BeforeHealth = toReportHealth(beforeHealth)

	for report.RoundsRun < opts.MaxRounds {
		claimable, claimErr := store.EligibilityProjectionClaimableCount(ctx, time.Time{})
		if claimErr != nil {
			return report, fmt.Errorf("count claimable projection jobs: %w", claimErr)
		}
		if claimable == 0 {
			report.QueueDrained = true
			break
		}
		// ProcessEligibilityProjectionJobs never aborts partway through the
		// batch it claims on a per-account error -- see its own doc comment
		// -- so a non-nil error here means at least one account in this
		// round failed or hit BALANCE_PROOF_PENDING backoff, not that the
		// whole round was lost. Record it and keep draining; the durable
		// per-account trace is read back from eligibility_projection_jobs
		// after the loop via EligibilityShadowFailedJobs.
		projected, procErr := store.ProcessEligibilityProjectionJobs(ctx, opts.BatchLimit, time.Time{}, shadowEvalActor)
		report.RoundsRun++
		report.AccountsProjected += projected
		if procErr != nil {
			report.RoundErrors = append(report.RoundErrors, procErr.Error())
		}
	}

	after, err := store.EligibilityShadowSnapshot(ctx)
	if err != nil {
		return report, fmt.Errorf("take post-projection snapshot: %w", err)
	}
	report.After = toReportSnapshot(after)
	afterHealth, err := store.EligibilityProjectionHealth(ctx)
	if err != nil {
		return report, fmt.Errorf("read post-projection health: %w", err)
	}
	report.AfterHealth = toReportHealth(afterHealth)

	failed, err := store.EligibilityShadowFailedJobs(ctx)
	if err != nil {
		return report, fmt.Errorf("list failed projection jobs: %w", err)
	}
	report.FailedAccounts = toReportFailedAccounts(failed)
	pending, err := store.EligibilityShadowPendingJobs(ctx)
	if err != nil {
		return report, fmt.Errorf("list pending projection jobs: %w", err)
	}
	report.PendingAccounts = toReportPendingAccounts(pending)

	EvaluateReadiness(&report)
	return report, nil
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
