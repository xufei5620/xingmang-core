package main

import (
	"reflect"
	"testing"
)

func TestEvaluateReadinessNoChangesIsReady(t *testing.T) {
	report := Report{
		Before: Snapshot{OpenFreezes: []FreezeCount{{FreezeReason: "SOURCE_GAP", Open: 3}}},
		After:  Snapshot{OpenFreezes: []FreezeCount{{FreezeReason: "SOURCE_GAP", Open: 3}}},
	}
	EvaluateReadiness(&report)
	if report.Verdict != VerdictReady || ExitCode(report) != 0 {
		t.Fatalf("expected ready/0, got verdict=%q exit=%d", report.Verdict, ExitCode(report))
	}
	if len(report.NewFreezeReasons) != 0 || report.HasProjectionErrors {
		t.Fatalf("expected no new reasons and no errors, got %+v", report)
	}
}

func TestEvaluateReadinessGrowingExistingReasonStaysReady(t *testing.T) {
	// Only NEW categories block readiness -- a pre-existing reason's open
	// count growing (more accounts hitting the same already-known freeze)
	// is not itself a regression signal for this rehearsal.
	report := Report{
		Before: Snapshot{OpenFreezes: []FreezeCount{{FreezeReason: "SOURCE_GAP", Open: 1}}},
		After:  Snapshot{OpenFreezes: []FreezeCount{{FreezeReason: "SOURCE_GAP", Open: 40}}},
	}
	EvaluateReadiness(&report)
	if report.Verdict != VerdictReady || ExitCode(report) != 0 {
		t.Fatalf("expected ready/0 for a growing pre-existing reason, got verdict=%q exit=%d", report.Verdict, ExitCode(report))
	}
}

func TestEvaluateReadinessNewFreezeReasonBlocks(t *testing.T) {
	report := Report{
		Before: Snapshot{OpenFreezes: []FreezeCount{{FreezeReason: "SOURCE_GAP", Open: 3}}},
		After: Snapshot{OpenFreezes: []FreezeCount{
			{FreezeReason: "SOURCE_GAP", Open: 3},
			{FreezeReason: "UNKNOWN_NEGATIVE_BALANCE", Open: 1},
		}},
	}
	EvaluateReadiness(&report)
	if report.Verdict != VerdictNotReady || ExitCode(report) != 3 {
		t.Fatalf("expected not_ready/3, got verdict=%q exit=%d", report.Verdict, ExitCode(report))
	}
	if !reflect.DeepEqual(report.NewFreezeReasons, []string{"UNKNOWN_NEGATIVE_BALANCE"}) {
		t.Fatalf("expected new_freeze_reasons=[UNKNOWN_NEGATIVE_BALANCE], got %+v", report.NewFreezeReasons)
	}
}

func TestEvaluateReadinessReappearingReasonWithZeroOpenIsNotNew(t *testing.T) {
	// A reason present in Before with Open=0 (defensive: the real snapshot
	// query never emits a zero-count row, but the comparison must not
	// depend on that) must not count as pre-existing for the purpose of
	// blocking, and must not itself be flagged new when After also has 0.
	report := Report{
		Before: Snapshot{OpenFreezes: []FreezeCount{{FreezeReason: "SOURCE_GAP", Open: 0}}},
		After:  Snapshot{OpenFreezes: []FreezeCount{{FreezeReason: "SOURCE_GAP", Open: 0}}},
	}
	EvaluateReadiness(&report)
	if report.Verdict != VerdictReady {
		t.Fatalf("expected ready, got verdict=%q", report.Verdict)
	}
}

func TestEvaluateReadinessMultipleNewReasonsAreSortedAndDeduplicatedByInput(t *testing.T) {
	report := Report{
		Before: Snapshot{},
		After: Snapshot{OpenFreezes: []FreezeCount{
			{FreezeReason: "UNIT_MISMATCH", Open: 1},
			{FreezeReason: "AMBIGUOUS_EVENT_ORDER", Open: 2},
		}},
	}
	EvaluateReadiness(&report)
	if !reflect.DeepEqual(report.NewFreezeReasons, []string{"AMBIGUOUS_EVENT_ORDER", "UNIT_MISMATCH"}) {
		t.Fatalf("expected sorted new reasons, got %+v", report.NewFreezeReasons)
	}
}

func TestEvaluateReadinessRoundErrorsBlockEvenWithoutNewFreezeReasons(t *testing.T) {
	report := Report{
		Before:      Snapshot{},
		After:       Snapshot{},
		RoundErrors: []string{"eligibility projection job failed: some transient error"},
	}
	EvaluateReadiness(&report)
	if report.Verdict != VerdictNotReady || ExitCode(report) != 3 {
		t.Fatalf("expected not_ready/3 from round errors alone, got verdict=%q exit=%d", report.Verdict, ExitCode(report))
	}
	if !report.HasProjectionErrors {
		t.Fatal("expected HasProjectionErrors=true")
	}
}

func TestEvaluateReadinessFailedAccountsBlockEvenWithoutRoundErrors(t *testing.T) {
	// RoundErrors only captures the first error per round
	// (ProcessEligibilityProjectionJobs' own contract); FailedAccounts is the
	// durable per-account trace and must independently block readiness.
	report := Report{
		Before:         Snapshot{},
		After:          Snapshot{},
		FailedAccounts: []FailedAccount{{ExternalAccountID: "acct-1", LastErrorCode: "PROJECTION_FAILED"}},
	}
	EvaluateReadiness(&report)
	if report.Verdict != VerdictNotReady || ExitCode(report) != 3 {
		t.Fatalf("expected not_ready/3 from a failed account alone, got verdict=%q exit=%d", report.Verdict, ExitCode(report))
	}
}

func TestEvaluateReadinessBothNewReasonsAndErrorsStillExitThree(t *testing.T) {
	report := Report{
		Before:      Snapshot{},
		After:       Snapshot{OpenFreezes: []FreezeCount{{FreezeReason: "SOURCE_REFUND", Open: 1}}},
		RoundErrors: []string{"boom"},
	}
	EvaluateReadiness(&report)
	if report.Verdict != VerdictNotReady || ExitCode(report) != 3 {
		t.Fatalf("expected not_ready/3, got verdict=%q exit=%d", report.Verdict, ExitCode(report))
	}
}
