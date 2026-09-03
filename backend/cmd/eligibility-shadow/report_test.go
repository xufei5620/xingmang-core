package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestReportJSONShapeMatchesShadowEvalLibAssumptions pins the exact
// json.MarshalIndent(report, "", "  ") shape that
// deploy/rehearsal/shadow-eval-lib.sh's hand-written bash/awk parser depends
// on, since that parser cannot use a JSON library (see its own header
// comment for why) and instead exploits three specific properties of this
// output:
//  1. Before's fields always serialize before After's (Go preserves struct
//     declaration order), so the Nth occurrence of a repeated key
//     unambiguously identifies which snapshot it belongs to.
//  2. A non-empty []T slice is always expanded one element per line, never
//     inlined, regardless of how many elements it has.
//  3. An empty (zero-length) []T slice is always inlined as "[]" on the key's
//     own line, and a nil []T slice always marshals as the literal `null` --
//     these are what shadow_eval_has_errors and the empty-array branch of
//     _shadow_eval_freeze_block each depend on.
//
// If a future change to Report/Snapshot/FreezeCount's field order, an added
// custom MarshalJSON, or a switch away from MarshalIndent ever breaks one of
// these, this test fails loudly here -- long before a real rehearsal run
// would surface it as a silently wrong verdict.
func TestReportJSONShapeMatchesShadowEvalLibAssumptions(t *testing.T) {
	report := Report{
		Before: Snapshot{
			OpenFreezes: []FreezeCount{{FreezeReason: "SOURCE_GAP", Open: 3}},
		},
		After: Snapshot{
			OpenFreezes: []FreezeCount{
				{FreezeReason: "SOURCE_GAP", Open: 3},
				{FreezeReason: "UNKNOWN_NEGATIVE_BALANCE", Open: 1},
			},
		},
		RoundErrors:    nil,
		FailedAccounts: nil,
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)

	beforeIdx := strings.Index(text, `"open_freezes_by_reason": [`)
	afterIdx := strings.LastIndex(text, `"open_freezes_by_reason": [`)
	if beforeIdx < 0 || afterIdx <= beforeIdx {
		t.Fatalf("expected two distinct expanded open_freezes_by_reason arrays with Before first, got:\n%s", text)
	}
	if strings.Count(text, `"open_freezes_by_reason": [`) != 2 {
		t.Fatalf("expected exactly two occurrences of the expanded-array marker, got:\n%s", text)
	}
	if !strings.Contains(text, "\n      {\n        \"freeze_reason\": \"SOURCE_GAP\",\n        \"open\": 3\n      }") {
		t.Fatalf("expected each freeze-count element on its own indented lines, got:\n%s", text)
	}

	if !strings.Contains(text, `"round_errors": null`) {
		t.Fatalf("expected a nil RoundErrors to marshal as the literal null, got:\n%s", text)
	}
	if !strings.Contains(text, `"failed_accounts": null`) {
		t.Fatalf("expected a nil FailedAccounts to marshal as the literal null, got:\n%s", text)
	}

	emptyArrayReport := Report{
		Before: Snapshot{OpenFreezes: []FreezeCount{}},
		After:  Snapshot{OpenFreezes: []FreezeCount{}},
	}
	emptyEncoded, err := json.MarshalIndent(emptyArrayReport, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(emptyEncoded), `"open_freezes_by_reason": [],`) {
		t.Fatalf("expected an empty (non-nil) OpenFreezes slice to inline as [], got:\n%s", string(emptyEncoded))
	}

	populatedErrors := Report{RoundErrors: []string{"boom"}}
	populatedEncoded, err := json.MarshalIndent(populatedErrors, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(populatedEncoded), "\"round_errors\": [\n    \"boom\"\n  ]") {
		t.Fatalf("expected a populated RoundErrors to expand one string per line, got:\n%s", string(populatedEncoded))
	}
}

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
