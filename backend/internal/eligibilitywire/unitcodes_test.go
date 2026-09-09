package eligibilitywire

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// XM-INV-UNIT-DISPLAY. Two layers are pinned here, in this order:
//
//  1. the RECOGNITION rule -- what the scan counts as a unit_code position --
//     against throwaway packages, so a regression in the rule shows up in this
//     file rather than as a real gate that happens to stay green; and
//  2. the CONTRACT gate itself, two-directionally, against the real trees.
//
// The recognition tests come first because they are what makes the contract
// gate meaningful: a scan that recognises nothing agrees with any contract that
// declares nothing, and the incident this whole package exists for is exactly
// "a gate that could not fail".

func scanUnitCodeProbePackage(t *testing.T, source string) (UnitCodeScan, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "probe.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	scan := UnitCodeScan{Literals: []UnitCodeLiteral{}, UnitFuncs: map[string]bool{}, Delegations: []UnitCodeLiteral{}}
	if _, err := scanUnitCodePackageDir(&scan, dir, "probe"); err != nil {
		return UnitCodeScan{}, err
	}
	// The real scan verifies delegations after the whole walk; the probe
	// helper does the same, so both halves of that rule are exercised here.
	if err := scan.VerifyDelegations(); err != nil {
		return UnitCodeScan{}, err
	}
	return scan, nil
}

func unitCodeValues(scan UnitCodeScan, kind string) []string {
	out := []string{}
	for _, literal := range scan.Literals {
		if literal.Kind == kind {
			out = append(out, literal.Value)
		}
	}
	return out
}

// TestUnitCodeScanFindsEveryPositionalShape walks the shapes that actually
// occur in this repository today, plus the constant-extraction refactor that
// broke the eligibility_status scan once already.
func TestUnitCodeScanFindsEveryPositionalShape(t *testing.T) {
	scan, err := scanUnitCodeProbePackage(t, `package probe

const zzWalletUnitCode = "ZZ_CONST_CODE"

type manifest struct{ UnitCode string }

type wallet struct{ WalletUnitCode string }

// assignment to a local named unitCode, the source_processor.go shape
func pick(newAPI bool) string {
	unitCode := "ZZ_ASSIGNED_CODE"
	if newAPI {
		unitCode = "ZZ_REASSIGNED_CODE"
	}
	return unitCode
}

// composite-literal key, both the struct field and the DTO map spelling
func build() (manifest, map[string]any) {
	return manifest{UnitCode: "ZZ_STRUCT_KEY_CODE"},
		map[string]any{"unit_code": "ZZ_MAP_KEY_CODE"}
}

// comparison against a unit-code field, the validator.go shape
func check(m manifest) bool {
	return m.UnitCode != "ZZ_COMPARED_CODE"
}

// constant return from a function whose name mentions a unit
func unitCodeForSource(newAPI bool) string {
	if newAPI {
		return "ZZ_RETURNED_CODE"
	}
	return ""
}

// the named-constant refactor
func useConst(w *wallet) {
	w.WalletUnitCode = zzWalletUnitCode
}
`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ZZ_ASSIGNED_CODE",
		"ZZ_REASSIGNED_CODE",
		"ZZ_STRUCT_KEY_CODE",
		"ZZ_MAP_KEY_CODE",
		"ZZ_COMPARED_CODE",
		"ZZ_RETURNED_CODE",
		"ZZ_CONST_CODE",
	}
	found := Set(unitCodeValues(scan, UnitCodePositional))
	for _, value := range want {
		if !found[value] {
			t.Errorf("the scan missed %q; found %v", value, scan.Values())
		}
	}
	// The empty return of unitCodeForSource must NOT become a vocabulary
	// member: "" is this codebase's spelling of "no unit contract yet".
	if found[""] {
		t.Error(`the empty string must not be recorded as a unit code`)
	}
	// The value must be attributed to where a human can go and look.
	for _, literal := range scan.Literals {
		if literal.Value == "ZZ_COMPARED_CODE" && literal.Func != "check" {
			t.Errorf("wrong attribution for the compared code: %s", literal)
		}
	}
}

// TestUnitCodeShapeNetIsIndependentOfPosition is net 2 on its own: a code that
// appears only inside SQL text, in no unit-code position at all.
func TestUnitCodeShapeNetIsIndependentOfPosition(t *testing.T) {
	scan, err := scanUnitCodeProbePackage(t, `package probe

const listQuery = "SELECT id FROM t WHERE code='ZZ_BALANCE_1E9' AND x=1"

func list() string { return listQuery }
`)
	if err != nil {
		t.Fatal(err)
	}
	if !Set(unitCodeValues(scan, UnitCodeShape))["ZZ_BALANCE_1E9"] {
		t.Fatalf("a unit-code-shaped token inside SQL must be found by the shape net, got %v", scan.Values())
	}
	// And it must NOT have been credited to the positional net, or the two
	// nets are not independent and the "shapeless code in a known position"
	// case is not really covered by anything.
	if Set(unitCodeValues(scan, UnitCodePositional))["ZZ_BALANCE_1E9"] {
		t.Fatal("the shape net's find must not be reported as a positional find")
	}
}

func TestUnitCodeScanTreatsCopiesAsIntroducingNothing(t *testing.T) {
	scan, err := scanUnitCodeProbePackage(t, `package probe

type manifest struct{ UnitCode string }

type payload struct{ WalletUnitCode *string }

func copyThrough(m manifest, p payload) manifest {
	out := manifest{UnitCode: m.UnitCode}
	unitCode := ""
	if p.WalletUnitCode != nil {
		unitCode = *p.WalletUnitCode
	}
	out.UnitCode = unitCode
	return out
}
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Values()) != 0 {
		t.Fatalf("copying a unit code from one field to another introduces no vocabulary, got %v", scan.Values())
	}
}

func TestUnitCodeScanRefusesAPositionItCannotEvaluate(t *testing.T) {
	for name, source := range map[string]string{
		"helper return": `package probe

type manifest struct{ UnitCode string }

func pick() string { return "zz" }

func build() manifest {
	return manifest{UnitCode: pick()}
}
`,
		"local variable": `package probe

type manifest struct{ UnitCode string }

func override(m *manifest, s string) {
	code := s
	m.UnitCode = code
}
`,
		"constant from another package": `package probe

import "invoice-system/backend/internal/somewhere"

type manifest struct{ UnitCode string }

func override(m *manifest) {
	m.UnitCode = somewhere.Code
}
`,
		"tuple assignment": `package probe

type manifest struct{ UnitCode string }

func pick() (string, error) { return "zz", nil }

func override(m *manifest) (err error) {
	m.UnitCode, err = pick()
	return err
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := scanUnitCodeProbePackage(t, source)
			if err == nil {
				t.Fatal("a unit_code position the scan cannot evaluate must be refused, not skipped")
			}
			if !strings.Contains(err.Error(), "probe/probe.go:") {
				t.Fatalf("the refusal must name file:line, got: %v", err)
			}
		})
	}
}

// TestUnitCodeScanRefusesACrossPackageUnitCodeSelector is the second hole
// review found. `isUnitCodePassthrough` used to look only at the NAME on the
// right of the dot, so any `<something>.UnitCode` counted as "a copy" -- and a
// copy of another package's variable is not a copy of anything this scan has
// read. Both spellings must be refused: the package imported (the shape real
// code would have) and the package not imported at all (the shape the type
// checker cannot resolve, which must not be mistaken for "fine").
func TestUnitCodeScanRefusesACrossPackageUnitCodeSelector(t *testing.T) {
	for name, source := range map[string]string{
		"imported package": `package probe

import "invoice-system/backend/internal/zzelsewhere"

type manifest struct{ UnitCode string }

func override(m *manifest) {
	m.UnitCode = zzelsewhere.WalletUnitCode
}
`,
		"package not imported at all": `package probe

type manifest struct{ UnitCode string }

func override(m *manifest) {
	m.UnitCode = zzelsewhere.WalletUnitCode
}
`,
		"cross-package value in a composite literal": `package probe

import "invoice-system/backend/internal/zzelsewhere"

type manifest struct{ UnitCode string }

func build() manifest {
	return manifest{UnitCode: zzelsewhere.WalletUnitCode}
}
`,
		"cross-package value behind a deref": `package probe

import "invoice-system/backend/internal/zzelsewhere"

type manifest struct{ UnitCode string }

func override(m *manifest) {
	m.UnitCode = *zzelsewhere.WalletUnitCode
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := scanUnitCodeProbePackage(t, source)
			if err == nil {
				t.Fatal("a unit code copied out of another package must be refused, not counted as a passthrough")
			}
			if !strings.Contains(err.Error(), "probe/probe.go:") {
				t.Fatalf("the refusal must name file:line, got: %v", err)
			}
		})
	}
}

// TestUnitCodeScanStillAcceptsInPackageCopies is the other side of that rule,
// and it is the half that would have made a careless fix expensive: the stub
// importer leaves every IMPORTED TYPE invalid, so a rule written against the
// base's type instead of its identity would refuse `item.UnitCode` for an
// `item` whose struct comes from another package -- which is ordinary, correct
// code throughout postgresstore and application.
func TestUnitCodeScanStillAcceptsInPackageCopies(t *testing.T) {
	scan, err := scanUnitCodeProbePackage(t, `package probe

import "invoice-system/backend/internal/zzelsewhere"

type manifest struct{ UnitCode string }

type payload struct{ WalletUnitCode *string }

// item's type is declared in an imported package, so the checker cannot type
// it here. The VALUE still came from a variable, not from a package.
func copyThrough(item zzelsewhere.Row, p payload, m manifest) manifest {
	out := manifest{UnitCode: item.UnitCode}
	out.UnitCode = m.UnitCode
	if p.WalletUnitCode != nil {
		out.UnitCode = *p.WalletUnitCode
	}
	return out
}
`)
	if err != nil {
		t.Fatalf("copying from an in-package variable must stay a passthrough: %v", err)
	}
	if len(scan.Values()) != 0 {
		t.Fatalf("a copy introduces no vocabulary, got %v", scan.Values())
	}
}

// TestUnitCodeScanDoesNotRefuseAComparisonAgainstAHelper pins the one place
// the scan deliberately does NOT refuse. `manifest.UnitCode !=
// expectedUnitForSource(sourceType)` is how this codebase validates the field;
// treating it as an unreadable position would make the gate error on correct,
// existing code, and the pressure would then be to delete the gate.
func TestUnitCodeScanDoesNotRefuseAComparisonAgainstAHelper(t *testing.T) {
	scan, err := scanUnitCodeProbePackage(t, `package probe

type manifest struct{ UnitCode string }

func expected() string { return "zz" }

func check(m manifest) bool { return m.UnitCode != expected() }
`)
	if err != nil {
		t.Fatalf("a comparison against a helper introduces no vocabulary and must not be refused: %v", err)
	}
	if len(scan.Values()) != 0 {
		t.Fatalf("it must introduce nothing, got %v", scan.Values())
	}
}

// TestUnitCodeScanChecksWhatItDelegatesTo covers both halves of the delegation
// rule. `unitCode := expectedUnitForSource(sourceType)` (postgresstore/store.go)
// must not be refused -- the callee's own constant returns are already in the
// scan -- but that reasoning is only sound while the callee really was read,
// so delegating to a function this scan never saw must be an error rather than
// a silent nothing.
func TestUnitCodeScanChecksWhatItDelegatesTo(t *testing.T) {
	t.Run("callee is scanned", func(t *testing.T) {
		scan, err := scanUnitCodeProbePackage(t, `package probe

type manifest struct{ UnitCode string }

func expectedUnitForSource(newAPI bool) string {
	if newAPI {
		return "ZZ_DELEGATED_CODE"
	}
	return ""
}

func build(newAPI bool) manifest {
	unitCode := expectedUnitForSource(newAPI)
	return manifest{UnitCode: unitCode}
}
`)
		if err != nil {
			t.Fatalf("delegating to a function this scan reads must not be refused: %v", err)
		}
		// The vocabulary still arrives -- through the callee's returns.
		if !Set(scan.Values())["ZZ_DELEGATED_CODE"] {
			t.Fatalf("the delegated-to function's own returns must still be discovered, got %v", scan.Values())
		}
	})
	t.Run("callee is not scanned", func(t *testing.T) {
		_, err := scanUnitCodeProbePackage(t, `package probe

type manifest struct{ UnitCode string }

func build() manifest {
	return manifest{UnitCode: zzUnitCodeFromElsewhere()}
}
`)
		if err == nil {
			t.Fatal("delegating to a function this scan never read must be refused, not trusted")
		}
		if !strings.Contains(err.Error(), "zzUnitCodeFromElsewhere") {
			t.Fatalf("the refusal must name the callee, got: %v", err)
		}
	})
}

// TestUnitCodeScanCoversEveryGoFileThatMentionsAUnitCode is the answer to the
// hole review found: the scan's roots used to be the hand-written list
// {backend/internal, agents}, so backend/cmd -- every operational command in
// this repository -- sat outside the gate, and a unit code planted in
// backend/cmd/bootstrap-sources/main.go left every probe green.
//
// The scope is discovered now, and this probe checks that claim WITHOUT reusing
// the discovery: it sweeps the tree textually (no parsing, no package rules,
// its own walk) for non-test .go files that so much as mention a unit code, and
// requires each one's directory to be one the scan actually visited. If the two
// ever disagree, the scan is the one that is wrong.
//
// testdata/ and vendor/ are the discovery's only content exclusions, and they
// are checked rather than trusted: a unit-code-mentioning .go file appearing
// under either fails this test instead of quietly leaving the gate. There are
// none today.
func TestUnitCodeScanCoversEveryGoFileThatMentionsAUnitCode(t *testing.T) {
	scan, err := ScanUnitCodes()
	if err != nil {
		t.Fatal(err)
	}
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	visited := Set(scan.ScannedDirs)
	mentions := regexp.MustCompile(`UnitCode|\b[A-Z][A-Z0-9_]*_1E[0-9]+\b`)
	uncovered := []string{}
	excluded := []string{}
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// .git and node_modules are not source trees; node_modules is also
			// a junction to the main checkout in this worktree.
			if path != root && (entry.Name() == ".git" || entry.Name() == "node_modules") {
				return fs.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !mentions.Match(body) {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		dir := filepath.ToSlash(rel)
		fileRel := dir + "/" + name
		if strings.Contains("/"+dir+"/", "/testdata/") || strings.Contains("/"+dir+"/", "/vendor/") {
			excluded = append(excluded, fileRel)
			return nil
		}
		if !visited[dir] {
			uncovered = append(uncovered, fileRel)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	if len(uncovered) > 0 {
		t.Fatalf("these non-test Go files mention a unit code but live in directories the scan never visited:\n  %s\n"+
			"the scan visited %d directories; DiscoverGoPackageDirs is missing them",
			strings.Join(uncovered, "\n  "), len(scan.ScannedDirs))
	}
	if len(excluded) > 0 {
		t.Fatalf("these files mention a unit code from inside testdata/ or vendor/, which the scan skips by name:\n  %s\n"+
			"either move them, or decide explicitly that this vocabulary is out of the gate",
			strings.Join(excluded, "\n  "))
	}
	// A sweep that proved nothing would also satisfy both checks above.
	if len(visited) == 0 {
		t.Fatal("the scan reported visiting no directories at all")
	}
}

// TestUnitCodeScanScopeReachesTheCommands is the narrow, named form of the same
// regression: backend/cmd is a directory tree like any other and it is in the
// gate. Separate from the coverage probe because that probe would still pass if
// backend/cmd stopped containing Go code entirely, and "the commands are
// covered" is what review actually asked for.
//
// It asserts against what the SCAN VISITED, not against what
// DiscoverGoPackageDirs returns -- a correction the status scan's twin forced
// in round four. Asking the discovery helper only proves the helper works: a
// scan that narrows its own loop afterwards still passes, and a mutation that
// did exactly that sailed past this assertion. The helper is not the gate.
func TestUnitCodeScanScopeReachesTheCommands(t *testing.T) {
	scan, err := ScanUnitCodes()
	if err != nil {
		t.Fatal(err)
	}
	visited := Set(scan.ScannedDirs)
	commands := []string{}
	for _, dir := range scan.ScannedDirs {
		if strings.HasPrefix(dir, "backend/cmd/") {
			commands = append(commands, dir)
		}
	}
	if len(commands) == 0 {
		t.Fatalf("the unit-code scan visited no backend/cmd package; it visited: %v", scan.ScannedDirs)
	}
	// The trees the first cut named by hand are still there, so this is a
	// widening rather than a swap.
	for _, needed := range []string{"backend/internal/postgresstore", "agents/sourceagent"} {
		if !visited[needed] {
			t.Fatalf("the unit-code scan no longer visits %s; it visited: %v", needed, scan.ScannedDirs)
		}
	}
}

// TestServiceUnitsMatchTheSource is the contract gate: every unit code the
// scanned trees introduce is declared with a divisor, and every declared code
// is one the source can actually produce. Both halves matter -- an undeclared
// code renders on the user's page as a raw unconverted integer, and a declared
// code nothing emits is a divisor nobody has ever checked against reality.
func TestServiceUnitsMatchTheSource(t *testing.T) {
	contract, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	discovered, scan, err := DiscoverUnitCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(discovered) == 0 {
		t.Fatal("the unit-code scan found nothing at all; it is looking at the wrong trees")
	}
	extra, missing := Diff(Set(discovered), Set(contract.ServiceUnitCodes()))
	if len(extra) > 0 || len(missing) > 0 {
		lines := make([]string, 0, len(scan.Literals))
		for _, literal := range scan.FirstLocations() {
			lines = append(lines, "  "+literal.String())
		}
		t.Fatalf("service_units drifted from the source:\n"+
			"  emitted by code but not declared (renders unconverted on the user's page): %v\n"+
			"  declared but no longer emitted anywhere (an unchecked divisor): %v\n"+
			"discovered unit codes:\n%s\n"+
			"update contracts/invoice-eligibility-wire.v1.json and regenerate the frontend module",
			extra, missing, strings.Join(lines, "\n"))
	}
}

// TestBothUnitCodeNetsContributeToTheGate keeps the two nets from collapsing
// into one. If every code in the contract happens to be found by the shape net
// alone, the positional net could rot away unnoticed -- and the positional net
// is the only thing that can see a code with no distinguishing shape, which is
// what NEWAPI_QUOTA is.
func TestBothUnitCodeNetsContributeToTheGate(t *testing.T) {
	scan, err := ScanUnitCodes()
	if err != nil {
		t.Fatal(err)
	}
	positional := Set(unitCodeValues(scan, UnitCodePositional))
	shape := Set(unitCodeValues(scan, UnitCodeShape))
	if len(positional) == 0 {
		t.Fatal("the positional net found nothing; a shapeless new unit code would now be invisible")
	}
	if len(shape) == 0 {
		t.Fatal("the shape net found nothing; a shaped unit code outside a known position would now be invisible")
	}
	onlyPositional := []string{}
	for value := range positional {
		if !shape[value] {
			onlyPositional = append(onlyPositional, value)
		}
	}
	if len(onlyPositional) == 0 {
		t.Fatal("every discovered code is shape-matched, so the positional net is currently carrying no weight; " +
			"if that is genuinely true of the vocabulary, say so here rather than leaving the claim untested")
	}
}

// TestNewUnitCodeReachesTheGate is the end-to-end negative form: a code
// introduced the way a new source would introduce one lands in the set the
// contract is compared against, so the gate goes red on it. This is the
// automated twin of the manual mutation recorded in the handoff (adding a code
// to consumption.go and watching the gate fail).
func TestNewUnitCodeReachesTheGate(t *testing.T) {
	contract, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{
		"shapeless, in a unit-code position": `package probe

type manifest struct{ UnitCode string }

func build() manifest { return manifest{UnitCode: "ZZ_PROBE_QUOTA"} }
`,
		"shaped, nowhere near a unit-code position": `package probe

const auditNote = "SELECT * FROM t WHERE code='ZZ_PROBE_1E4'"

func note() string { return auditNote }
`,
	} {
		want := "ZZ_PROBE_QUOTA"
		if strings.Contains(name, "shaped,") {
			want = "ZZ_PROBE_1E4"
		}
		t.Run(name, func(t *testing.T) {
			scan, err := scanUnitCodeProbePackage(t, source)
			if err != nil {
				t.Fatal(err)
			}
			extra, _ := Diff(Set(scan.Values()), Set(contract.ServiceUnitCodes()))
			if len(extra) != 1 || extra[0] != want {
				t.Fatalf("a new unit code must reach the contract gate as undeclared, got extra=%v", extra)
			}
		})
	}
}

func TestContractRejectsMalformedServiceUnits(t *testing.T) {
	base := Contract{
		LotPersistedStatuses:  []string{"active"},
		LotSyntheticStatuses:  []string{"missing"},
		LotEligibilityStatus:  []string{"active", "missing"},
		LotReasonCode:         []string{"LEDGER_FROZEN"},
		LotStatusReasonPairs:  map[string][]string{"active": {""}},
		SummaryStatus:         []string{"active"},
		SummaryReason:         []string{"READY"},
		SummaryReasonMaxCount: 5,
		ServiceUnits: []ServiceUnit{
			{Code: "ZZ_UNIT", Divisor: "100", Decimals: 2, DisplayLabel: "测试单位"},
		},
	}
	if err := base.validate(); err != nil {
		t.Fatalf("the well-formed fixture must validate: %v", err)
	}
	for name, mutate := range map[string]func(*Contract){
		"no units at all":  func(c *Contract) { c.ServiceUnits = nil },
		"empty code":       func(c *Contract) { c.ServiceUnits[0].Code = "" },
		"repeated code":    func(c *Contract) { c.ServiceUnits = append(c.ServiceUnits, c.ServiceUnits[0]) },
		"divisor zero":     func(c *Contract) { c.ServiceUnits[0].Divisor = "0" },
		"divisor negative": func(c *Contract) { c.ServiceUnits[0].Divisor = "-100" },
		"divisor not a whole number": func(c *Contract) {
			c.ServiceUnits[0].Divisor = "1.5"
		},
		"divisor empty":      func(c *Contract) { c.ServiceUnits[0].Divisor = "" },
		"decimals negative":  func(c *Contract) { c.ServiceUnits[0].Decimals = -1 },
		"decimals too large": func(c *Contract) { c.ServiceUnits[0].Decimals = 9 },
		"label empty":        func(c *Contract) { c.ServiceUnits[0].DisplayLabel = "" },
		"label only spaces":  func(c *Contract) { c.ServiceUnits[0].DisplayLabel = "   " },
		"label has no Chinese": func(c *Contract) {
			c.ServiceUnits[0].DisplayLabel = "Sub2API balance"
		},
	} {
		t.Run(name, func(t *testing.T) {
			mutated := base
			mutated.ServiceUnits = append([]ServiceUnit(nil), base.ServiceUnits...)
			mutate(&mutated)
			if err := mutated.validate(); err == nil {
				t.Fatal("a malformed service_units entry must be rejected")
			}
		})
	}
	// The boundaries themselves must be accepted, or the range check is
	// really a narrower rule than it claims.
	for _, decimals := range []int{0, 8} {
		accepted := base
		accepted.ServiceUnits = []ServiceUnit{{Code: "ZZ_UNIT", Divisor: "1", Decimals: decimals, DisplayLabel: "测试单位"}}
		if err := accepted.validate(); err != nil {
			t.Fatalf("decimals=%d is inside the stated range and must be accepted: %v", decimals, err)
		}
	}
}

// TestGeneratedModuleCarriesTheUnitTable is not covered by the golden
// comparison alone: that test would stay green if the renderer and the
// checked-in file both dropped the unit table.
func TestGeneratedModuleCarriesTheUnitTable(t *testing.T) {
	contract, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	rendered := RenderTypeScript(contract)
	if !strings.Contains(rendered, "export const serviceUnitDefinitions") {
		t.Fatal("the generated module must export the unit table the frontend converts with")
	}
	if !strings.Contains(rendered, "export type ServiceUnitCodeWire") {
		t.Fatal("the generated module must export the unit-code union the frontend types key on")
	}
	for _, unit := range contract.ServiceUnits {
		for _, needed := range []string{unit.Code, unit.Divisor, unit.DisplayLabel} {
			if !strings.Contains(rendered, needed) {
				t.Fatalf("the generated module is missing %q from unit %s", needed, unit.Code)
			}
		}
		// A divisor rendered as a bare number is the float64 bug this
		// contract's string type exists to prevent.
		if strings.Contains(rendered, "divisor: "+unit.Divisor+",") {
			t.Fatalf("unit %s's divisor must be rendered as a quoted string, not a number literal", unit.Code)
		}
	}
}
