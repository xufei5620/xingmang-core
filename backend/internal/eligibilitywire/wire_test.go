package eligibilitywire

import (
	"flag"
	"os"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite web/src/lib/eligibility-wire.generated.ts from the contract")

// TestGeneratedTypeScriptMatchesContract keeps the frontend's enum module and
// the wire contract from drifting apart. Run with -update to regenerate.
func TestGeneratedTypeScriptMatchesContract(t *testing.T) {
	contract, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	path, err := GeneratedTypeScriptPath()
	if err != nil {
		t.Fatal(err)
	}
	want := RenderTypeScript(contract)
	if *update {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("regenerated %s", path)
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated module (run: go test ./internal/eligibilitywire/... -update): %v", err)
	}
	if string(got) != want {
		t.Fatalf("web/src/lib/eligibility-wire.generated.ts is stale.\n"+
			"regenerate with: cd backend && go test ./internal/eligibilitywire/... -update\n"+
			"--- on disk ---\n%s\n--- from contract ---\n%s",
			visibleEndings(string(got)), visibleEndings(want))
	}
}

// TestGeneratedTypeScriptUsesCRLF is a separate assertion from the golden
// comparison on purpose. The golden test alone would stay green if BOTH the
// renderer and the checked-in file used LF -- and that pair is exactly what a
// clean checkout (core.autocrlf=true rewrites *.ts to CRLF) turns red for the
// next person, on a line that has nothing to do with their change.
func TestGeneratedTypeScriptUsesCRLF(t *testing.T) {
	contract, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	rendered := RenderTypeScript(contract)
	if !strings.Contains(rendered, "\r\n") {
		t.Fatal("rendered module has no CRLF endings")
	}
	if strings.Contains(strings.ReplaceAll(rendered, "\r\n", ""), "\n") {
		t.Fatal("rendered module mixes bare LF with CRLF endings")
	}
}

func visibleEndings(value string) string {
	return strings.ReplaceAll(value, "\r", "<CR>")
}

// TestLotPersistedStatusesMatchLatestMigration discovers the persisted status
// set instead of trusting a hand-kept list: DiscoverPersistedStatuses reads the
// newest migration that (re)states source_account_eligibility_state's
// eligibility_status CHECK, and its literals are compared against the contract
// both ways.
//
// Migration 0020 is what made this necessary -- it widened 0009's three-value
// CHECK to four, and nothing anywhere failed when the frontend's copies of the
// list stayed at the old values.
func TestLotPersistedStatusesMatchLatestMigration(t *testing.T) {
	contract, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := DiscoverPersistedStatuses()
	if err != nil {
		t.Fatal(err)
	}
	extra, missing := Diff(Set(persisted.Values), Set(contract.LotPersistedStatuses))
	if len(extra) > 0 || len(missing) > 0 {
		t.Fatalf("lot_persisted_statuses drifted from %s's CHECK constraint:\n"+
			"  in the migration but not the contract: %v\n"+
			"  in the contract but not the migration: %v\n"+
			"update contracts/invoice-eligibility-wire.v1.json and regenerate the frontend module",
			persisted.Migration, extra, missing)
	}
}

// TestLotSyntheticStatusesAreDiscovered is the half that was missing when this
// package first shipped. The two response-time-only statuses were written into
// the contract by hand, and the funding-lot probe then took its input space
// from the contract -- so a THIRD synthetic status, added the same way
// source_unavailable itself was added (one assignment in application/service.go,
// no migration), would have been fed to no probe and declared by no gate. Every
// probe would have stayed green on the exact shape of the incident.
//
// Now the synthetic set is whatever the source actually contains that the CHECK
// constraint does not, in both directions: an undeclared literal is red, and a
// declared value that no longer appears anywhere is red too.
func TestLotSyntheticStatusesAreDiscovered(t *testing.T) {
	contract, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	discovered, sources, err := SyntheticStatuses()
	if err != nil {
		t.Fatal(err)
	}
	extra, missing := Diff(Set(discovered), Set(contract.LotSyntheticStatuses))
	if len(extra) > 0 || len(missing) > 0 {
		lines := make([]string, 0, len(sources))
		for _, literal := range sources {
			lines = append(lines, "  "+literal.String())
		}
		t.Fatalf("lot_synthetic_statuses drifted from the source:\n"+
			"  introduced by code but not declared: %v\n"+
			"  declared but no longer introduced anywhere: %v\n"+
			"discovered literals:\n%s\n"+
			"update contracts/invoice-eligibility-wire.v1.json and regenerate the frontend module",
			extra, missing, strings.Join(lines, "\n"))
	}
	persisted, err := DiscoverPersistedStatuses()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range contract.LotSyntheticStatuses {
		if Set(persisted.Values)[value] {
			t.Fatalf("%q is declared synthetic but %s's CHECK constraint persists it", value, persisted.Migration)
		}
	}
}

// TestLotEligibilityStatusIsPersistedPlusSynthetic pins the composition of the
// wire status set: everything the CHECK constraint allows, plus exactly the
// response-time-only values, and nothing else.
func TestLotEligibilityStatusIsPersistedPlusSynthetic(t *testing.T) {
	contract, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	space, err := LotStatusInputSpace()
	if err != nil {
		t.Fatal(err)
	}
	extra, missing := Diff(Set(contract.LotEligibilityStatus), Set(space))
	if len(extra) > 0 || len(missing) > 0 {
		t.Fatalf("lot_eligibility_status must be exactly persisted+synthetic:\n  extra: %v\n  missing: %v", extra, missing)
	}
}

// TestSummaryStatusesAreDiscovered replaces an assertion that was both
// unfalsifiable and wrong. It used to say summary_status must EQUAL
// lot_eligibility_status "because they describe the same column" -- so the
// summary's own vocabulary was never discovered at all, and the stated reason
// did not match the code: the summary rides
// COALESCE(eas.eligibility_status,'syncing') (not 'missing') and
// ListUserEligibilitySummaries passes the value straight through with no
// freshness override, so `missing` and `source_unavailable` are both
// unreachable there. Narrowing the contract to the truth left the old assertion
// green in the wrong direction and red in the right one -- a gate arguing
// against its own correction.
func TestSummaryStatusesAreDiscovered(t *testing.T) {
	contract, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	space, err := SummaryStatusInputSpace()
	if err != nil {
		t.Fatal(err)
	}
	extra, missing := Diff(Set(contract.SummaryStatus), Set(space))
	if len(extra) > 0 || len(missing) > 0 {
		t.Fatalf("summary_status drifted from what the summary path can actually produce "+
			"(%s's COALESCE default plus any override in %s):\n"+
			"  declared but unreachable: %v\n  reachable but undeclared: %v",
			summaryStoreFunc, summaryServiceFunc, extra, missing)
	}
	// The summary is a subset of the lot vocabulary, never a superset: both
	// read the same column, the lot path merely adds response-time values on
	// top. If this ever fails, the two really have diverged and the frontend's
	// shared status label table needs splitting.
	extra, _ = Diff(Set(contract.SummaryStatus), Set(contract.LotEligibilityStatus))
	if len(extra) > 0 {
		t.Fatalf("summary_status has values the lot status set does not: %v", extra)
	}
}

// TestSummaryStatusScanIsNotLookingAtNothing guards the one hand-written part
// of the summary discovery: the two function NAMES it scopes itself to. Scoping
// by name and finding a renamed function reads identically to "this function
// introduces no status literals", so the scan asserts they still exist.
func TestSummaryStatusScanIsNotLookingAtNothing(t *testing.T) {
	scan, err := ScanStatusLiterals()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{summaryStoreFunc, summaryServiceFunc} {
		if !scan.Funcs[name] {
			t.Fatalf("%s is not among the scanned packages' functions", name)
		}
	}
	if len(scan.InFunc(summaryStoreFunc)) == 0 {
		t.Fatalf("%s introduced no eligibility_status literal at all; it used to supply the "+
			"COALESCE default the summary's vocabulary depends on", summaryStoreFunc)
	}
}

func TestContractRejectsMalformedGroups(t *testing.T) {
	base := Contract{
		LotPersistedStatuses:  []string{"active"},
		LotSyntheticStatuses:  []string{"missing"},
		LotEligibilityStatus:  []string{"active", "missing"},
		LotReasonCode:         []string{"LEDGER_FROZEN"},
		LotStatusReasonPairs:  map[string][]string{"active": {""}},
		SummaryStatus:         []string{"active"},
		SummaryReason:         []string{"READY"},
		SummaryReasonMaxCount: 5,
	}
	if err := base.validate(); err != nil {
		t.Fatalf("the well-formed fixture must validate: %v", err)
	}
	duplicated := base
	duplicated.LotReasonCode = []string{"LEDGER_FROZEN", "LEDGER_FROZEN"}
	if err := duplicated.validate(); err == nil {
		t.Fatal("a repeated value must be rejected")
	}
	empty := base
	empty.SummaryReason = []string{"READY", ""}
	if err := empty.validate(); err == nil {
		t.Fatal("an empty value must be rejected")
	}
	zeroCap := base
	zeroCap.SummaryReasonMaxCount = 0
	if err := zeroCap.validate(); err == nil {
		t.Fatal("a non-positive summary_reason_max_count must be rejected")
	}
}
