package eligibilitywire

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

var (
	checkPattern    = regexp.MustCompile(`eligibility_status\s+IN\s*\(([^)]*)\)`)
	sqlLiteralValue = regexp.MustCompile(`'([^']*)'`)
)

// TestLotPersistedStatusesMatchLatestMigration discovers the persisted status
// set instead of trusting a hand-kept list: it reads the newest migration that
// (re)states source_account_eligibility_state's eligibility_status CHECK and
// compares that constraint's literals against the contract, both ways.
//
// Migration 0020 is what made this necessary -- it widened 0009's three-value
// CHECK to four, and nothing anywhere failed when the frontend's copies of the
// list stayed at the old values.
func TestLotPersistedStatusesMatchLatestMigration(t *testing.T) {
	contract, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := MigrationsDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	newestName := ""
	var newestValues []string
	// unparsedAfter records migrations newer than the one we parsed that still
	// touch this constraint in a shape the pattern above does not recognise.
	// Without this check a future migration written differently would leave the
	// probe silently comparing against a stale constraint -- a gate that gives
	// an old answer without ever going red is worse than no gate at all.
	var unparsedAfter []string
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if !strings.Contains(text, "source_account_eligibility_state") &&
			!strings.Contains(text, "eligibility_status") {
			continue
		}
		match := checkPattern.FindStringSubmatch(text)
		if match == nil {
			if newestName != "" && strings.Contains(text, "eligibility_status_check") {
				unparsedAfter = append(unparsedAfter, name)
			}
			continue
		}
		values := []string{}
		for _, literal := range sqlLiteralValue.FindAllStringSubmatch(match[1], -1) {
			values = append(values, literal[1])
		}
		newestName, newestValues = name, values
		unparsedAfter = nil
	}
	if newestName == "" {
		t.Fatal("no migration states an eligibility_status CHECK constraint; the discovery pattern has gone stale")
	}
	if len(unparsedAfter) > 0 {
		t.Fatalf("migrations %v are newer than %s and touch the eligibility_status CHECK in a shape this probe cannot parse; "+
			"update checkPattern rather than letting the probe compare against a stale constraint",
			unparsedAfter, newestName)
	}
	extra, missing := Diff(Set(newestValues), Set(contract.LotPersistedStatuses))
	if len(extra) > 0 || len(missing) > 0 {
		t.Fatalf("lot_persisted_statuses drifted from %s's CHECK constraint:\n"+
			"  in the migration but not the contract: %v\n"+
			"  in the contract but not the migration: %v\n"+
			"update contracts/invoice-eligibility-wire.v1.json and regenerate the frontend module",
			newestName, extra, missing)
	}
}

// TestLotEligibilityStatusIsPersistedPlusSynthetic pins the composition of the
// wire status set: everything the CHECK constraint allows, plus exactly the two
// response-time-only values, and nothing else.
func TestLotEligibilityStatusIsPersistedPlusSynthetic(t *testing.T) {
	contract, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	union := Set(contract.LotPersistedStatuses)
	for _, value := range contract.LotSyntheticStatuses {
		if union[value] {
			t.Fatalf("%q is listed as synthetic but the CHECK constraint persists it", value)
		}
		union[value] = true
	}
	extra, missing := Diff(Set(contract.LotEligibilityStatus), union)
	if len(extra) > 0 || len(missing) > 0 {
		t.Fatalf("lot_eligibility_status must be exactly persisted+synthetic:\n  extra: %v\n  missing: %v", extra, missing)
	}
	// The summary rides the same status column, so the two lists must agree.
	extra, missing = Diff(Set(contract.SummaryStatus), Set(contract.LotEligibilityStatus))
	if len(extra) > 0 || len(missing) > 0 {
		t.Fatalf("summary_status and lot_eligibility_status describe the same column but differ:\n  extra: %v\n  missing: %v", extra, missing)
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
