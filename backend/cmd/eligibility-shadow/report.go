package main

import (
	"sort"
	"time"

	"invoice-system/backend/internal/postgresstore"
)

// Verdict values for Report.Verdict. VerdictNotReady also selects
// ExitCode's release-blocking exit status (3).
const (
	VerdictReady    = "ready"
	VerdictNotReady = "not_ready"
)

// AccountStatus is the JSON shape of one account's eligibility status and
// open-freeze count, either before or after the candidate's projection ran.
type AccountStatus struct {
	ExternalAccountID string `json:"external_account_id"`
	EligibilityStatus string `json:"eligibility_status"`
	OpenFreezes       int64  `json:"open_freezes"`
}

// FreezeCount is the open freeze count for one freeze_reason category.
type FreezeCount struct {
	FreezeReason string `json:"freeze_reason"`
	Open         int64  `json:"open"`
}

// EvaluationCount is the count of balance evidence items currently at one
// evaluation_status.
type EvaluationCount struct {
	EvaluationStatus string `json:"evaluation_status"`
	Count            int64  `json:"count"`
}

// FailedAccount is one account left in eligibility_projection_jobs
// status='failed' after the drain.
type FailedAccount struct {
	ExternalAccountID string    `json:"external_account_id"`
	LastErrorCode     string    `json:"last_error_code"`
	AttemptCount      int64     `json:"attempt_count"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// Snapshot is one point-in-time read of account/freeze/evaluation state.
type Snapshot struct {
	Accounts    []AccountStatus   `json:"accounts"`
	OpenFreezes []FreezeCount     `json:"open_freezes_by_reason"`
	Evaluations []EvaluationCount `json:"evaluations_by_status"`
}

// ProjectionHealth is the JSON shape of postgresstore.EligibilityProjectionHealth.
// Failed is populated from that struct's Dead field (XM-INV-PROJECTION-FAILURE-GRADING
// renamed postgresstore.EligibilityProjectionHealth.Failed to Dead: it now
// counts only the new terminal status='dead' grade, not a job merely
// retrying with backoff) -- kept under its historical JSON key name here
// deliberately, to avoid churning deploy/rehearsal/test-shadow-eval.sh's
// fixture JSON, which never reads this field (only round_errors and
// failed_accounts drive shadow_eval_has_errors -- see
// deploy/rehearsal/shadow-eval-lib.sh). See
// docs/handoffs/XM-INV-PROJECTION-FAILURE-GRADING.md.
type ProjectionHealth struct {
	Queued             int64     `json:"queued"`
	Failed             int64     `json:"failed"`
	Processing         int64     `json:"processing"`
	OldestPending      time.Time `json:"oldest_pending"`
	ProofPending       int64     `json:"proof_pending"`
	OldestProofPending time.Time `json:"oldest_proof_pending"`
}

// Report is XM-INV-SHADOW-EVAL's full rehearsal report: exactly what
// deploy/rehearsal/shadow-eval.sh writes to
// rehearsals/<stamp>/shadow-eval.json, and what its own independent
// comparison in bash re-derives an exit code from (see
// deploy/rehearsal/test-shadow-eval.sh) rather than trusting this process's
// exit code alone.
type Report struct {
	GeneratedAt       time.Time `json:"generated_at"`
	BackupLabel       string    `json:"backup_label,omitempty"`
	CandidateImageTag string    `json:"candidate_image_tag,omitempty"`
	// MigrationsApplied lists, in the order deploy/rehearsal/shadow-eval.sh
	// applied them, every migration file its candidate-tools-image
	// invoice-migrate step newly added to the restored backup's own
	// schema_migrations table (computed by that script diffing the table
	// before and after running invoice-migrate, then passed in via
	// --migrations-applied) -- nil/omitted when the restored backup was
	// already at the candidate's migration set (nothing to apply). This is
	// what makes the verdict below explicitly "candidate schema + candidate
	// evaluator against production data", not merely "candidate evaluator
	// against whatever schema the backup happened to be taken at".
	MigrationsApplied []string `json:"migrations_applied"`
	MaxRounds         int      `json:"max_rounds"`
	RoundsRun         int      `json:"rounds_run"`
	QueueDrained      bool     `json:"queue_drained"`

	Before       Snapshot         `json:"before"`
	BeforeHealth ProjectionHealth `json:"before_projection_health"`
	After        Snapshot         `json:"after"`
	AfterHealth  ProjectionHealth `json:"after_projection_health"`

	FailedAccounts []FailedAccount `json:"failed_accounts"`
	RoundErrors    []string        `json:"round_errors"`

	NewFreezeReasons    []string `json:"new_freeze_reasons"`
	HasProjectionErrors bool     `json:"has_projection_errors"`
	Verdict             string   `json:"verdict"`
	VerdictReason       string   `json:"verdict_reason"`
}

// EvaluateReadiness compares the After snapshot's open freeze reasons and
// this run's captured errors against the Before snapshot -- taken
// immediately after restore, before the candidate's projection ran anything
// -- and fills in NewFreezeReasons/HasProjectionErrors/Verdict/VerdictReason.
//
// It is a pure function over already-gathered data (no I/O) precisely so it
// can be unit tested without a database (see report_test.go), and so
// deploy/rehearsal/shadow-eval.sh's bash test can exercise the identical
// logic against fixture JSON -- the release-blocking exit code is meant to
// survive a bug in either implementation alone, not depend on trusting this
// process's own exit code unverified.
func EvaluateReadiness(report *Report) {
	before := make(map[string]bool, len(report.Before.OpenFreezes))
	for _, count := range report.Before.OpenFreezes {
		if count.Open > 0 {
			before[count.FreezeReason] = true
		}
	}
	var newReasons []string
	for _, count := range report.After.OpenFreezes {
		if count.Open > 0 && !before[count.FreezeReason] {
			newReasons = append(newReasons, count.FreezeReason)
		}
	}
	sort.Strings(newReasons)
	report.NewFreezeReasons = newReasons
	report.HasProjectionErrors = len(report.RoundErrors) > 0 || len(report.FailedAccounts) > 0

	switch {
	case len(newReasons) > 0 && report.HasProjectionErrors:
		report.Verdict = VerdictNotReady
		report.VerdictReason = "new freeze reason categories and projection errors appeared after the candidate ran"
	case len(newReasons) > 0:
		report.Verdict = VerdictNotReady
		report.VerdictReason = "new freeze reason categories appeared after the candidate ran"
	case report.HasProjectionErrors:
		report.Verdict = VerdictNotReady
		report.VerdictReason = "the candidate produced projection errors"
	default:
		report.Verdict = VerdictReady
		report.VerdictReason = "no new freeze reason categories and no projection errors versus the post-restore baseline"
	}
}

// ExitCode maps a Report's Verdict to the release-blocking exit status the
// task brief specifies: 0 when ready, 3 when the candidate regressed. Any
// other exit code (2 for a usage error, 1 for an execution failure such as a
// lost database connection) is set directly in main and never reaches this
// function.
func ExitCode(report Report) int {
	if report.Verdict == VerdictReady {
		return 0
	}
	return 3
}

func toReportSnapshot(snapshot postgresstore.EligibilityShadowSnapshot) Snapshot {
	result := Snapshot{
		Accounts:    make([]AccountStatus, 0, len(snapshot.Accounts)),
		OpenFreezes: make([]FreezeCount, 0, len(snapshot.OpenFreezes)),
		Evaluations: make([]EvaluationCount, 0, len(snapshot.Evaluations)),
	}
	for _, account := range snapshot.Accounts {
		result.Accounts = append(result.Accounts, AccountStatus{
			ExternalAccountID: account.ExternalAccountID,
			EligibilityStatus: account.EligibilityStatus,
			OpenFreezes:       account.OpenFreezes,
		})
	}
	for _, count := range snapshot.OpenFreezes {
		result.OpenFreezes = append(result.OpenFreezes, FreezeCount{
			FreezeReason: count.FreezeReason, Open: count.Open,
		})
	}
	for _, count := range snapshot.Evaluations {
		result.Evaluations = append(result.Evaluations, EvaluationCount{
			EvaluationStatus: count.EvaluationStatus, Count: count.Count,
		})
	}
	return result
}

func toReportHealth(health postgresstore.EligibilityProjectionHealth) ProjectionHealth {
	return ProjectionHealth{
		Queued: health.Queued, Failed: health.Dead, Processing: health.Processing,
		OldestPending: health.OldestPending, ProofPending: health.ProofPending,
		OldestProofPending: health.OldestProofPending,
	}
}

// toReportFailedAccounts returns nil, not an initialized-but-empty slice,
// when failed has no elements. A real production run (RC78) caught the bug
// in this function's previous unconditional
// make([]FailedAccount, 0, len(failed)): json.MarshalIndent renders a
// non-nil empty slice as the inline "[]", but
// deploy/rehearsal/shadow-eval-lib.sh's shadow_eval_has_errors (at the
// time) only recognized "no failures" via the literal `null`, not "[]" --
// so a genuinely clean rehearsal ("ready" per this process's own ExitCode)
// was independently recomputed as "not_ready" by shadow-eval.sh's bash
// verdict, and the two disagreeing was itself treated as a
// rehearsal-tooling failure. RoundErrors never had this bug (it is only
// ever nil-until-appended, see run() in main.go); this brings
// FailedAccounts in line with that same "nil means none" contract instead
// of just patching the bash side to tolerate both shapes (shadow-eval-lib.sh
// is hardened to do that too, as defense in depth -- see its own comments
// -- but the two independent implementations must first agree on one
// definition, and nil-means-none is the one every other []T field here
// already follows).
func toReportFailedAccounts(failed []postgresstore.EligibilityShadowFailedJob) []FailedAccount {
	if len(failed) == 0 {
		return nil
	}
	result := make([]FailedAccount, 0, len(failed))
	for _, job := range failed {
		result = append(result, FailedAccount{
			ExternalAccountID: job.ExternalAccountID, LastErrorCode: job.LastErrorCode,
			AttemptCount: job.AttemptCount, UpdatedAt: job.UpdatedAt,
		})
	}
	return result
}
