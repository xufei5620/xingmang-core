package eligibilitywire

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// These tests pin the RECOGNITION rule of the code scan, separately from the
// probes that consume it. The second cut of discover.go recognised only bare
// string literals, and the review's own mutations showed what that costs: a
// status written as a named constant was invisible (green), while extracting
// an existing literal into a constant -- a pure refactor -- went red with a
// message telling the reader to delete the value from the contract. Both
// directions are nailed down here against a throwaway package, so a regression
// in the rule shows up in this file rather than as a probe that happens to
// stay green.

func scanProbePackage(t *testing.T, source string) (StatusScan, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "probe.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	scan := StatusScan{Literals: []StatusLiteral{}, Funcs: map[string]bool{}}
	err := scanPackageDir(&scan, dir, "probe")
	return scan, err
}

func literalValues(scan StatusScan, kind string) []string {
	out := []string{}
	for _, literal := range scan.Literals {
		if literal.Kind == kind {
			out = append(out, literal.Value)
		}
	}
	return out
}

func TestScanResolvesNamedConstantAssignments(t *testing.T) {
	// Review mutation V2, reproduced: the third response-time status is spelled
	// as a constant, once by assignment and once as a composite-literal key.
	scan, err := scanProbePackage(t, `package probe

const zzProbeStatus = "zz_probe_status"

const zzOtherStatus = "zz_" + "other_status"

type lot struct{ EligibilityStatus string }

func override(lots []lot) {
	for i := range lots {
		lots[i].EligibilityStatus = zzProbeStatus
	}
}

func build() lot {
	return lot{EligibilityStatus: zzOtherStatus}
}
`)
	if err != nil {
		t.Fatal(err)
	}
	got := literalValues(scan, LiteralAssignment)
	if len(got) != 2 || got[0] != "zz_probe_status" || got[1] != "zz_other_status" {
		t.Fatalf("named constants must be resolved to their values, got %v", got)
	}
	if len(scan.InFunc("override")) != 1 || len(scan.InFunc("build")) != 1 {
		t.Fatalf("each value must be attributed to the function that assigns it, got %v", scan.Literals)
	}
	if scan.Literals[0].File != "probe/probe.go" || scan.Literals[0].Line != 11 {
		t.Fatalf("the location must point at the assignment, got %s", scan.Literals[0])
	}
}

func TestScanStillSeesBareLiteralsAndPassthroughs(t *testing.T) {
	// The historical RC58 shape must keep working, and copying one status
	// field into another (what user_dto.go and ListUserEligibilitySummaries do)
	// must be neither a literal nor an error.
	scan, err := scanProbePackage(t, `package probe

type lot struct{ EligibilityStatus string }

type dto struct{ EligibilityStatus string }

func override(l *lot, base lot) dto {
	l.EligibilityStatus = "source_unavailable"
	l.EligibilityStatus = (base.EligibilityStatus)
	return dto{EligibilityStatus: l.EligibilityStatus}
}
`)
	if err != nil {
		t.Fatal(err)
	}
	got := literalValues(scan, LiteralAssignment)
	if len(got) != 1 || got[0] != "source_unavailable" {
		t.Fatalf("want exactly the bare literal, got %v", got)
	}
}

// TestStatusScanCoversEveryGoFileThatMentionsAStatus is this scan's answer to
// the hand-listed-scope problem, and it is deliberately the same shape as the
// unit-code side's coverage probe.
//
// The scope used to be {application, postgresstore, httpapi}. Review planted
// `EligibilityStatus: "zz_probe_status"` in agents/sourceagent and this gate
// stayed green -- a status the code can introduce, in a package the gate never
// opened. The scope is discovered now, and this probe checks that claim WITHOUT
// reusing the discovery: its own walk, matching text rather than parsing.
//
// It matches BOTH spellings on purpose. `EligibilityStatus` catches the Go
// assignments; `eligibility_status` catches SQL text, which is where the
// COALESCE-default half of this scan gets its values -- a query string can
// introduce a status in a file that never names the Go field.
//
// testdata/ and vendor/ are the discovery's only content exclusions and are
// checked rather than trusted: a status-mentioning .go file under either fails
// here instead of quietly leaving the gate.
func TestStatusScanCoversEveryGoFileThatMentionsAStatus(t *testing.T) {
	scan, err := ScanStatusLiterals()
	if err != nil {
		t.Fatal(err)
	}
	visited := Set(scan.ScannedDirs)
	mentions := regexp.MustCompile(`EligibilityStatus|eligibility_status`)
	uncovered, excluded := sweepForCoverage(t, mentions, visited)
	if len(uncovered) > 0 {
		t.Fatalf("these non-test Go files mention an eligibility_status but live in directories the scan never "+
			"visited:\n  %s\nthe scan visited %d directories; DiscoverGoPackageDirs is missing them",
			strings.Join(uncovered, "\n  "), len(scan.ScannedDirs))
	}
	if len(excluded) > 0 {
		t.Fatalf("these files mention an eligibility_status from inside testdata/ or vendor/, which the scan skips "+
			"by name:\n  %s\neither move them, or decide explicitly that this vocabulary is out of the gate",
			strings.Join(excluded, "\n  "))
	}
	if len(visited) == 0 {
		t.Fatal("the scan reported visiting no directories at all")
	}
}

// TestStatusScanScopeReachesTheAgents is the narrow, named form of the same
// regression: agents/ is where the review's probe status went, and it is in the
// gate. Separate from the coverage probe because that probe would still pass if
// agents/ contained no status-mentioning file at all, and "the agents are
// covered" is the thing review actually asked for.
//
// It asserts against what the SCAN VISITED, not against what
// DiscoverGoPackageDirs returns. The first version asked the discovery helper,
// and a mutation that narrowed the scan's own loop while leaving the helper
// alone sailed straight past it -- the assertion was true, about the wrong
// subject. The helper is not the gate; the scan is.
func TestStatusScanScopeReachesTheAgents(t *testing.T) {
	scan, err := ScanStatusLiterals()
	if err != nil {
		t.Fatal(err)
	}
	visited := Set(scan.ScannedDirs)
	agents := []string{}
	for _, dir := range scan.ScannedDirs {
		if strings.HasPrefix(dir, "agents/") {
			agents = append(agents, dir)
		}
	}
	if len(agents) == 0 {
		t.Fatalf("the status scan visited no agents package; it visited: %v", scan.ScannedDirs)
	}
	// The three packages the old hand-written list named are still there, so
	// this is a widening rather than a swap.
	for _, needed := range []string{
		"backend/internal/application",
		"backend/internal/postgresstore",
		"backend/internal/httpapi",
	} {
		if !visited[needed] {
			t.Fatalf("the status scan no longer visits %s; it visited: %v", needed, scan.ScannedDirs)
		}
	}
}

// TestScanRefusesACrossPackageStatusSelector closes the hole the unit-code
// scan's review found on its side of the package. isStatusPassthrough used to
// look only at the NAME after the dot, so a status read out of ANOTHER package
// -- whose contents this scan never reads, and whose vocabulary is therefore
// unknown -- counted as "a copy of another status field" and was skipped
// silently. Both spellings are refused: the package imported (what real code
// would look like) and the package not imported at all (what the type checker
// cannot resolve, which must not be read as "fine").
func TestScanRefusesACrossPackageStatusSelector(t *testing.T) {
	for name, source := range map[string]string{
		"imported package": `package probe

import "invoice-system/backend/internal/zzelsewhere"

type lot struct{ EligibilityStatus string }

func override(l *lot) {
	l.EligibilityStatus = zzelsewhere.EligibilityStatus
}
`,
		"package not imported at all": `package probe

type lot struct{ EligibilityStatus string }

func override(l *lot) {
	l.EligibilityStatus = zzelsewhere.EligibilityStatus
}
`,
		"cross-package value in a composite literal": `package probe

import "invoice-system/backend/internal/zzelsewhere"

type lot struct{ EligibilityStatus string }

func build() lot {
	return lot{EligibilityStatus: zzelsewhere.EligibilityStatus}
}
`,
		"cross-package value behind a deref": `package probe

import "invoice-system/backend/internal/zzelsewhere"

type lot struct{ EligibilityStatus string }

func override(l *lot) {
	l.EligibilityStatus = *zzelsewhere.EligibilityStatus
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := scanProbePackage(t, source)
			if err == nil {
				t.Fatal("a status read out of another package must be refused, not counted as a passthrough")
			}
			if !strings.Contains(err.Error(), "probe/probe.go:") {
				t.Fatalf("the refusal must name file:line, got: %v", err)
			}
		})
	}
}

// TestScanStillAcceptsInPackageStatusCopies is the control case, and it is the
// half a careless fix would break. The scan type-checks with stubImporter, so
// every type reached through an import is invalid; a rule written against the
// base's TYPE rather than its identity would refuse `item.EligibilityStatus`
// for an `item` whose struct is declared elsewhere -- which is exactly what
// application and httpapi do when they copy a store row's status into a DTO.
func TestScanStillAcceptsInPackageStatusCopies(t *testing.T) {
	scan, err := scanProbePackage(t, `package probe

import "invoice-system/backend/internal/zzelsewhere"

type dto struct{ EligibilityStatus string }

type lot struct{ EligibilityStatus string }

// item's type comes from an imported package, so the checker cannot type it
// here. The VALUE still comes from a variable, not from a package.
func toDTO(item zzelsewhere.Row, l lot, rows []lot) dto {
	out := dto{EligibilityStatus: item.EligibilityStatus}
	out.EligibilityStatus = l.EligibilityStatus
	out.EligibilityStatus = rows[0].EligibilityStatus
	return out
}
`)
	if err != nil {
		t.Fatalf("copying a status from an in-package variable must stay a passthrough: %v", err)
	}
	if len(literalValues(scan, LiteralAssignment)) != 0 {
		t.Fatalf("a copy introduces no vocabulary, got %v", literalValues(scan, LiteralAssignment))
	}
}

func TestScanRefusesAnAssignmentItCannotEvaluate(t *testing.T) {
	// Review mutation V7, reproduced: the status comes out of a helper. There
	// is no constant to fold, so the scan must not answer at all -- an answer
	// with a gap is what let V7 through green.
	for name, source := range map[string]string{
		"helper return": `package probe

type lot struct{ EligibilityStatus string }

func pick() string { return "zz" }

func override(l *lot) {
	l.EligibilityStatus = pick()
}
`,
		"local variable": `package probe

type lot struct{ EligibilityStatus string }

func override(l *lot, s string) {
	status := s
	l.EligibilityStatus = status
}
`,
		"composite literal from helper": `package probe

type lot struct{ EligibilityStatus string }

func pick() string { return "zz" }

func build() lot {
	return lot{EligibilityStatus: pick()}
}
`,
		"tuple assignment": `package probe

type lot struct{ EligibilityStatus string }

func pick() (string, error) { return "zz", nil }

func override(l *lot) (err error) {
	l.EligibilityStatus, err = pick()
	return err
}
`,
		"constant from another package": `package probe

import "invoice-system/backend/internal/somewhere"

type lot struct{ EligibilityStatus string }

func override(l *lot) {
	l.EligibilityStatus = somewhere.Status
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := scanProbePackage(t, source)
			if err == nil {
				t.Fatal("an EligibilityStatus the scan cannot evaluate must be refused, not skipped")
			}
			if !strings.Contains(err.Error(), "probe/probe.go:") {
				t.Fatalf("the refusal must name file:line, got: %v", err)
			}
		})
	}
}

func TestScanResolvesCoalesceDefaultsBuiltFromConstants(t *testing.T) {
	scan, err := scanProbePackage(t, `package probe

const zzDefault = "zz_coalesce_default"

const listQuery = "SELECT COALESCE(eas.eligibility_status,'" + zzDefault + "') FROM t"

func list() string {
	return listQuery + " WHERE 1=1"
}

func other() string {
	return "SELECT COALESCE(eas.eligibility_status,'missing'), COALESCE(eas.unit_code,'') FROM t"
}
`)
	if err != nil {
		t.Fatal(err)
	}
	got := literalValues(scan, LiteralCoalesceDefault)
	// The constant is recorded where it is declared (package scope) and where it
	// is used (inside list); the bare literal once. Sets are what the probes
	// consume, so duplicates are fine -- what matters is that the value built
	// from a concatenation is visible at all, and attributed to list().
	if !Set(got)["zz_coalesce_default"] || !Set(got)["missing"] {
		t.Fatalf("COALESCE defaults built from constants must be resolved, got %v", got)
	}
	inList := literalValues(StatusScan{Literals: scan.InFunc("list")}, LiteralCoalesceDefault)
	if len(inList) != 1 || inList[0] != "zz_coalesce_default" {
		t.Fatalf("the default must be attributed to the function whose query carries it, got %v", inList)
	}
	if len(scan.InFunc("other")) != 1 {
		t.Fatalf("a query with one eligibility_status COALESCE must yield exactly one literal, got %v", scan.InFunc("other"))
	}
}

func TestScanRefusesACoalesceDefaultItCannotRead(t *testing.T) {
	_, err := scanProbePackage(t, `package probe

func list() string {
	return "SELECT COALESCE(eas.eligibility_status,$2) FROM t"
}
`)
	if err == nil {
		t.Fatal("a COALESCE default that is not a literal must be refused, not read as no default")
	}
	if !strings.Contains(err.Error(), "probe/probe.go:4") {
		t.Fatalf("the refusal must name file:line, got: %v", err)
	}
}

func TestScanRefusesAnEmptyPackageDir(t *testing.T) {
	scan := StatusScan{Literals: []StatusLiteral{}, Funcs: map[string]bool{}}
	if err := scanPackageDir(&scan, t.TempDir(), "probe"); err == nil {
		t.Fatal("a scanned directory with no Go files reads exactly like a package that introduces nothing; it must be an error")
	}
}

// TestNewConstantStatusReachesTheGate is the end-to-end negative form the
// review asked for: a status introduced as a named constant is not merely
// "visible to the scan" but lands in the synthetic set the contract gate
// compares against, so the gate would go red on it.
func TestNewConstantStatusReachesTheGate(t *testing.T) {
	contract, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := DiscoverPersistedStatuses()
	if err != nil {
		t.Fatal(err)
	}
	scan, err := scanProbePackage(t, `package probe

const zzProbeStatus = "zz_probe_status"

type lot struct{ EligibilityStatus string }

func override(l *lot) {
	l.EligibilityStatus = zzProbeStatus
}
`)
	if err != nil {
		t.Fatal(err)
	}
	synthetic := map[string]bool{}
	for _, literal := range scan.Literals {
		if !Set(persisted.Values)[literal.Value] {
			synthetic[literal.Value] = true
		}
	}
	extra, _ := Diff(synthetic, Set(contract.LotSyntheticStatuses))
	if len(extra) != 1 || extra[0] != "zz_probe_status" {
		t.Fatalf("a constant-spelled status must reach the contract gate as undeclared, got extra=%v", extra)
	}
}
