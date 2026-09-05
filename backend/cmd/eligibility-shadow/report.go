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

	// The quantities an allocation change moves. Classifications alone
	// cannot show that an account's expected balance reached a predicted
	// value, which is the claim an evaluator change actually makes; see
	// postgresstore.EligibilityShadowAccountStatus for why overage_units is
	// a string.
	ProjectionVersion int64     `json:"projection_version"`
	FinalizedThrough  time.Time `json:"finalized_through"`
	OverageUnits      string    `json:"non_invoiceable_overage_units"`
	CapMinor          int64     `json:"cap_minor"`
	ConsumedCashMinor int64     `json:"consumed_cash_minor"`
	ReservedMinor     int64     `json:"reserved_minor"`
	IssuedMinor       int64     `json:"issued_minor"`
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

// PendingAccount is one account whose projection job was still present, and
// not dead, when the drain stopped -- typically requeued with backoff
// (BALANCE_PROOF_PENDING) past the drain's "nothing claimable" exit. It is
// informational: a pending job is not a projection error, but "7 of 8
// projected" must be able to say which account and why without a database.
type PendingAccount struct {
	ExternalAccountID string    `json:"external_account_id"`
	Status            string    `json:"status"`
	LastErrorCode     string    `json:"last_error_code"`
	AttemptCount      int64     `json:"attempt_count"`
	RequestedThrough  time.Time `json:"requested_through"`
	NextAttemptAt     time.Time `json:"next_attempt_at"`
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

	// XM-INV-SHADOW-EVAL-VACUOUS. RoundsRun and QueueDrained cannot tell a
	// real rehearsal from an empty one: a drain against an already-empty
	// queue reports exactly one round and "drained", and so does a run that
	// claims every account in a single batch. Only AccountsProjected
	// separates them, and it was being discarded (ProcessEligibilityProjectionJobs
	// returns it; run() ignored the value). The three fields below are what
	// make a vacuous run visible in the artifact instead of only in the
	// reader's assumptions -- and, when ReprojectAllRequested is set,
	// blocking rather than merely visible.
	ReprojectAllRequested bool  `json:"reproject_all_requested"`
	AccountsEnqueued      int64 `json:"accounts_enqueued"`
	AccountsProjected     int   `json:"accounts_projected"`
	// EvidenceBatchLimit labels which mode this rehearsal ran in (fix 3): 0
	// is the unbounded single pass, N is the bounded evidence pass. The
	// differential rehearsal is two reports of one backup that differ only
	// here, whose per-account quantities must be identical.
	EvidenceBatchLimit int `json:"evidence_batch_limit"`
	// ReevaluateEvidence / EvaluationsCleared record that the rehearsal
	// cleared the copy's evaluations first (and how many), so the evidence
	// pass had work; a differential pair is only meaningful with this set.
	ReevaluateEvidence bool  `json:"reevaluate_evidence_requested"`
	EvaluationsCleared int64 `json:"evaluations_cleared"`
	// ReleaseCatchupRequested / AccountsReleased: the RC87 post-deploy repair
	// replayed on the copy (catchup_key_hmac cleared) before anything is
	// queued, so a backup taken while an account was still excluded from
	// finalization reproduces the 2026-09-04 catch-up burst forward-only.
	ReleaseCatchupRequested []string `json:"release_catchup_requested"`
	AccountsReleased        int64    `json:"accounts_released"`
	// FinalizationWindowRequested / AccountsWindowed: every finalization
	// target was queued through the window a finalization pass would have
	// requested at the copy's last watermarks, never lowering a captured one.
	FinalizationWindowRequested bool  `json:"finalization_window_requested"`
	AccountsWindowed            int64 `json:"accounts_windowed"`
	// FinalizationWindowLagSeconds: how much earlier than the copy's last
	// watermarks the window was derived (0 = exactly what finalization would
	// have requested at them).
	FinalizationWindowLagSeconds int64 `json:"finalization_window_lag_seconds"`

	Before       Snapshot         `json:"before"`
	BeforeHealth ProjectionHealth `json:"before_projection_health"`
	After        Snapshot         `json:"after"`
	AfterHealth  ProjectionHealth `json:"after_projection_health"`

	FailedAccounts []FailedAccount `json:"failed_accounts"`
	RoundErrors    []string        `json:"round_errors"`
	// PendingAccounts follows the same nil-means-none contract as the two
	// fields above; deploy/rehearsal/shadow-eval-lib.sh never reads it for a
	// verdict, only to print a count in the human summary.
	PendingAccounts []PendingAccount `json:"pending_accounts"`

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

	// A rehearsal that was asked to reproject every account and projected
	// none proved nothing about the candidate, so it must not read as
	// "ready" -- that is precisely the failure this field exists to catch,
	// and the one three releases shipped past. It is checked before the
	// freeze/error comparison because those comparisons are themselves
	// vacuous when nothing ran: "no new freeze reasons" is trivially true
	// of a projection that never executed.
	if report.ReprojectAllRequested && report.AccountsProjected == 0 {
		report.Verdict = VerdictNotReady
		report.VerdictReason = "a full reprojection was requested but no account was projected, so this run proves nothing about the candidate"
		return
	}

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
			ProjectionVersion: account.ProjectionVersion,
			FinalizedThrough:  account.FinalizedThrough,
			OverageUnits:      account.OverageUnits,
			CapMinor:          account.CapMinor,
			ConsumedCashMinor: account.ConsumedCashMinor,
			ReservedMinor:     account.ReservedMinor,
			IssuedMinor:       account.IssuedMinor,
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

// toReportPendingAccounts follows toReportFailedAccounts' nil-means-none
// contract for the same reason (see its comment).
func toReportPendingAccounts(pending []postgresstore.EligibilityShadowPendingJob) []PendingAccount {
	if len(pending) == 0 {
		return nil
	}
	result := make([]PendingAccount, 0, len(pending))
	for _, job := range pending {
		result = append(result, PendingAccount{
			ExternalAccountID: job.ExternalAccountID, Status: job.Status, LastErrorCode: job.LastErrorCode,
			AttemptCount: job.AttemptCount, RequestedThrough: job.RequestedThrough, NextAttemptAt: job.NextAttemptAt,
		})
	}
	return result
}
