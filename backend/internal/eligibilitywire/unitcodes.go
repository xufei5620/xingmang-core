package eligibilitywire

// Discovery: where the unit_code vocabulary actually comes from.
//
// XM-INV-UNIT-DISPLAY. The contract's service_units group is not just a list of
// names -- each entry carries the divisor a number on the user's page is
// computed with. So "the contract knows about every unit code the system can
// emit" stopped being a documentation nicety and became the condition under
// which the displayed balance is right. A code the backend emits and the
// contract does not declare renders as an unconverted raw integer; a code the
// contract declares that nothing emits is a divisor nobody has checked against
// reality.
//
// Both directions are therefore gated, and the scan that feeds the gate uses
// TWO INDEPENDENT NETS on purpose:
//
//  1. POSITIONAL. A constant string that lands in a unit-code position --
//     assigned to a `unitCode`/`*UnitCode` name, used as a `UnitCode:` /
//     `"unit_code":` composite-literal value, compared against a `.UnitCode`
//     field, or returned by a function whose name mentions a unit. This net
//     needs no idea what unit codes look like, which is what lets it find
//     NEWAPI_QUOTA -- a name with no distinguishing shape at all.
//
//  2. SHAPE. Any constant string in the scanned trees containing a
//     `<UPPER>_1E<digits>` token, wherever it appears -- including inside SQL
//     text, where no positional rule would ever reach it.
//
// Neither net is derived from the contract, and neither is derived from the
// other. That is the point: a verifier whose coverage list is same-sourced as
// the thing it verifies is green forever. Net 1 catches a shapeless new code in
// a known position; net 2 catches a shaped new code in an unknown position. A
// code that is both shapeless AND in a position neither rule knows would still
// slip through -- so when net 1 meets a unit-code position holding something it
// cannot evaluate, it REFUSES to answer (naming file:line) rather than
// returning a set with a hole in it, exactly as the eligibility_status scan
// does.
//
// There is deliberately no "these functions must still exist" assertion here,
// unlike SummaryStatusInputSpace. It would add nothing: the contract declares a
// non-empty set, the comparison is two-directional, so a scan that has quietly
// stopped finding anything reports every declared code as missing and goes red
// on its own.
//
// Test files are not scanned, matching the eligibility_status scan. One
// consequence is worth writing down rather than discovering later:
// backend/internal/postgresstore/source_readiness_integration_test.go seeds a
// fixture row with unit code NEWAPI_CREDIT_1E6, which no production path emits
// and the contract does not declare. Scanning tests would make this gate red on
// that fixture. It is left out of scope here, and flagged in the handoff.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// skippedScanDirs are the directory NAMES the package discovery never descends
// into. This is the one hand-written part of the scope, so it is kept to things
// that are not this repository's own Go source by definition, and each one is
// visible here rather than being an implicit consequence of where the walk
// happens to start.
//
// The first cut of this file listed the scanned roots instead --
// {backend/internal, agents} -- and review showed exactly what that costs:
// backend/cmd was outside the gate in its entirety, and a unit code planted in
// backend/cmd/bootstrap-sources/main.go left every probe green. A gate whose
// SCOPE is hand-maintained fails the same way a gate whose expected set is
// hand-maintained fails; the scope has to be discovered too.
var skippedScanDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"testdata":     true,
}

const (
	// UnitCodePositional is a constant string found in a unit-code position.
	UnitCodePositional = "positional"
	// UnitCodeShape is a constant string containing a unit-code-shaped token.
	UnitCodeShape = "shape"
)

var (
	// unitCodeShapePattern is net 2. It matches inside a longer string so a
	// code embedded in SQL text is found too.
	unitCodeShapePattern = regexp.MustCompile(`\b[A-Z][A-Z0-9_]*_1E[0-9]+\b`)
	// unitCodeFuncPattern scopes the "constant return value" half of net 1.
	// It is deliberately broad: a function named for units that returns a
	// string constant this scan then has to account for is the safe direction
	// -- a false positive makes the gate red and forces a human to say what
	// the value is, which is a far better failure than a miss.
	unitCodeFuncPattern = regexp.MustCompile(`(?i)unit`)
)

// UnitCodeLiteral is one unit_code value the source introduces, with where it
// was found, because a gate that only says "an unexpected value appeared"
// leaves the next person grepping.
type UnitCodeLiteral struct {
	Value string
	Kind  string
	File  string // repository-relative, slash-separated
	Line  int
	Func  string // enclosing function, "" at package scope
}

func (l UnitCodeLiteral) String() string {
	where := l.Func
	if where == "" {
		where = "(package scope)"
	}
	return fmt.Sprintf("%q [%s] %s:%d in %s", l.Value, l.Kind, l.File, l.Line, where)
}

// UnitCodeScan is the result of walking the scanned trees.
type UnitCodeScan struct {
	Literals []UnitCodeLiteral
	// UnitFuncs is every scanned function whose name matched
	// unitCodeFuncPattern.
	UnitFuncs map[string]bool
	// Delegations are unit-code positions filled by calling a same-package
	// unit-named function -- `unitCode := expectedUnitForSource(sourceType)`
	// in postgresstore/store.go is one. Value holds the callee's name.
	//
	// These are not refused, because the callee's own constant returns are
	// already recorded by this same scan, so the value introduces nothing new.
	// That reasoning only holds while the callee really is one of the scanned
	// functions, so it is checked rather than assumed: VerifyDelegations runs
	// after the whole walk (the callee may be declared in a file the walk has
	// not reached yet) and turns "I trusted a function I never read" into an
	// error naming file:line.
	Delegations []UnitCodeLiteral
	// ScannedDirs is every directory this scan actually type-checked,
	// repository-relative and slash-separated. The coverage probe compares it
	// against its own textual sweep of the tree: "the scan visited everything
	// that mentions a unit code" is a claim, and a claim with nothing checking
	// it is how backend/cmd sat outside this gate in the first place.
	ScannedDirs []string
}

// VerifyDelegations reports a unit-code position that delegates to a function
// this scan never actually read.
func (s UnitCodeScan) VerifyDelegations() error {
	for _, delegation := range s.Delegations {
		if s.UnitFuncs[delegation.Value] {
			continue
		}
		return fmt.Errorf(
			"eligibilitywire: %s:%d fills a unit_code position by calling %s(), which this scan never read, so the "+
				"values it can return are unknown here. Move it into the scanned trees or teach unitcodes.go the shape",
			delegation.File, delegation.Line, delegation.Value)
	}
	return nil
}

// Values is the discovered vocabulary as a sorted, deduplicated set.
func (s UnitCodeScan) Values() []string {
	values := map[string]bool{}
	for _, literal := range s.Literals {
		values[literal.Value] = true
	}
	return sortedKeys(values)
}

// FirstLocations returns one literal per discovered value, earliest first, for
// failure messages that would otherwise repeat the same file dozens of times.
func (s UnitCodeScan) FirstLocations() []UnitCodeLiteral {
	seen := map[string]bool{}
	out := []UnitCodeLiteral{}
	for _, literal := range s.Literals {
		if seen[literal.Value] {
			continue
		}
		seen[literal.Value] = true
		out = append(out, literal)
	}
	return out
}

// DiscoverGoPackageDirs returns every directory in the repository that holds
// non-test Go source, repository-relative and slash-separated.
//
// This is the scan's SCOPE, and it is discovered rather than declared. A new
// top-level directory of Go code, a new command under backend/cmd, a package
// moved out from under internal/ -- all of them are in the gate the moment they
// exist, with nobody having to remember to add them.
//
// Exported because the coverage probe compares its own, textual sweep of the
// tree against what this actually visited.
func DiscoverGoPackageDirs() ([]string, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}
	dirs := []string{}
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if path != root && skippedScanDirs[entry.Name()] {
			return fs.SkipDir
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, file := range entries {
			name := file.Name()
			if file.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			dirs = append(dirs, filepath.ToSlash(rel))
			return nil
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Strings(dirs)
	return dirs, nil
}

// ScanUnitCodes walks every Go package in the repository's non-test sources.
func ScanUnitCodes() (UnitCodeScan, error) {
	root, err := repoRoot()
	if err != nil {
		return UnitCodeScan{}, err
	}
	dirs, err := DiscoverGoPackageDirs()
	if err != nil {
		return UnitCodeScan{}, err
	}
	scan := UnitCodeScan{
		Literals: []UnitCodeLiteral{}, UnitFuncs: map[string]bool{},
		Delegations: []UnitCodeLiteral{}, ScannedDirs: []string{},
	}
	for _, rel := range dirs {
		scanned, err := scanUnitCodePackageDir(&scan, filepath.Join(root, filepath.FromSlash(rel)), rel)
		if err != nil {
			return UnitCodeScan{}, err
		}
		if scanned {
			scan.ScannedDirs = append(scan.ScannedDirs, rel)
		}
	}
	if len(scan.ScannedDirs) == 0 {
		return UnitCodeScan{}, fmt.Errorf(
			"eligibilitywire: the unit-code scan found no Go packages at all; the package discovery has gone stale")
	}
	sort.SliceStable(scan.Literals, func(i, j int) bool {
		if scan.Literals[i].File != scan.Literals[j].File {
			return scan.Literals[i].File < scan.Literals[j].File
		}
		return scan.Literals[i].Line < scan.Literals[j].Line
	})
	if err := scan.VerifyDelegations(); err != nil {
		return UnitCodeScan{}, err
	}
	return scan, nil
}

// scanUnitCodePackageDir type-checks one directory's non-test files as a
// package. It reports whether the directory held any Go source at all; a
// directory with none is not an error here (the trees contain plenty of
// intermediate directories), which is safe because ScanUnitCodes requires the
// whole walk to have found at least one package and the contract gate requires
// the result to be non-empty.
func scanUnitCodePackageDir(scan *UnitCodeScan, dir, rel string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("eligibilitywire: scan %s: %w", dir, err)
	}
	names := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return false, nil
	}
	sort.Strings(names)
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(names))
	relOf := map[*ast.File]string{}
	for _, name := range names {
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return false, fmt.Errorf("eligibilitywire: parse %s/%s: %w", rel, name, err)
		}
		files = append(files, file)
		relOf[file] = rel + "/" + name
	}
	// Uses is recorded as well as Types: the passthrough rule has to be able to
	// ask what the BASE of a selector resolves to, and "a package" and "an
	// undefined name" both have to be answerable even though the stub importer
	// leaves the resulting TYPE invalid. Identifier resolution survives the
	// errors that constant folding across imports does not.
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Uses:  map[*ast.Ident]types.Object{},
	}
	config := types.Config{
		Importer:    stubImporter{},
		FakeImportC: true,
		Error:       func(error) {},
	}
	_, _ = config.Check(files[0].Name.Name, fset, files, info)
	for _, file := range files {
		if err := scanUnitCodeFile(scan, fset, info, file, relOf[file]); err != nil {
			return false, err
		}
	}
	return true, nil
}

func scanUnitCodeFile(scan *UnitCodeScan, fset *token.FileSet, info *types.Info, file *ast.File, rel string) error {
	record := func(value, kind string, pos token.Pos, fn string) {
		// The empty string is not a unit code: it is how this codebase spells
		// "this row has no unit contract yet" (expectedUnitForSource returns it
		// for an unrecognised source, source_processor zero-initialises with
		// it, and the frontend renders it as 单位合同待建立). Recording it would
		// put an unnamed member in a vocabulary the contract requires to be
		// non-empty everywhere.
		if value == "" {
			return
		}
		scan.Literals = append(scan.Literals, UnitCodeLiteral{
			Value: value, Kind: kind, File: rel, Line: fset.Position(pos).Line, Func: fn,
		})
	}
	where := func(pos token.Pos) string {
		return fmt.Sprintf("%s:%d", rel, fset.Position(pos).Line)
	}
	// positional classifies an expression that lands in a unit-code position:
	// a constant is recorded, a value copied from another unit-code name
	// introduces no vocabulary, and anything else is refused rather than
	// silently skipped.
	var positional func(expr ast.Expr, fn string, scope ast.Node, hops int, seen map[string]bool) error
	positional = func(expr ast.Expr, fn string, scope ast.Node, hops int, seen map[string]bool) error {
		if value, ok := constantString(info, expr); ok {
			record(value, UnitCodePositional, expr.Pos(), fn)
			return nil
		}
		if isUnitCodePassthrough(info, expr) {
			return nil
		}
		// Delegation to a same-package unit-named function. Only a bare
		// identifier callee qualifies: `pkg.Something()` may resolve outside
		// the scanned trees, and a function this scan never read is exactly
		// the gap the refusal below exists to prevent.
		if call, ok := ast.Unparen(expr).(*ast.CallExpr); ok {
			if callee, ok := ast.Unparen(call.Fun).(*ast.Ident); ok && unitCodeFuncPattern.MatchString(callee.Name) {
				scan.Delegations = append(scan.Delegations, UnitCodeLiteral{
					Value: callee.Name, Kind: UnitCodePositional,
					File: rel, Line: fset.Position(expr.Pos()).Line, Func: fn,
				})
				return nil
			}
		}
		// A local hop. payment_v3.go builds the value two steps away from the
		// field -- `unitValue := unitCodeForSource(c.Source); unit = &unitValue;
		// ...{WalletUnitCode: unit}` -- and neither intermediate name says
		// "unit code" in the spelling isUnitCodeName looks for. Following the
		// local definition is what keeps the refusal below aimed at values that
		// genuinely come from outside this function, instead of at ordinary
		// code that merely names a temporary badly.
		if hops > 0 && scope != nil {
			if unwrapped, ok := ast.Unparen(expr).(*ast.UnaryExpr); ok && unwrapped.Op == token.AND {
				return positional(unwrapped.X, fn, scope, hops, seen)
			}
			if ident, ok := ast.Unparen(expr).(*ast.Ident); ok && !seen[ident.Name] {
				sources, resolvable := localAssignments(scope, ident.Name)
				if resolvable && len(sources) > 0 {
					// seen is a PATH, not a visited-set: it is popped on the
					// way out so a name reached twice down two different
					// branches resolves both times. payment_v3.go writes
					// `unitValue := unitCodeForSource(...)` twice in one
					// function, and a visited-set would resolve the first and
					// then refuse the second for having been "seen".
					seen[ident.Name] = true
					for _, source := range sources {
						if err := positional(source, fn, scope, hops-1, seen); err != nil {
							delete(seen, ident.Name)
							return err
						}
					}
					delete(seen, ident.Name)
					return nil
				}
			}
		}
		return fmt.Errorf(
			"eligibilitywire: %s puts `%s` in a unit_code position and the scan cannot evaluate it as a constant; "+
				"the unit vocabulary it introduces is unknowable here. Assign a constant (or pass another unit-code "+
				"value through), or teach unitcodes.go the shape -- do not let it answer with a gap",
			where(expr.Pos()), types.ExprString(expr))
	}
	// localHopBudget bounds the chase. Four is what payment_v3.go's two steps
	// needs with room to spare; an unbounded walk would trade a refusal (loud)
	// for a stack overflow (loud in the wrong place).
	const localHopBudget = 4
	classify := func(expr ast.Expr, fn string, scope ast.Node) error {
		return positional(expr, fn, scope, localHopBudget, map[string]bool{})
	}
	var walkErr error
	walk := func(node ast.Node, fn string) {
		ast.Inspect(node, func(n ast.Node) bool {
			if walkErr != nil {
				return false
			}
			switch typed := n.(type) {
			case *ast.AssignStmt:
				for i, lhs := range typed.Lhs {
					if !isUnitCodeExpr(lhs) {
						continue
					}
					if len(typed.Lhs) != len(typed.Rhs) {
						walkErr = fmt.Errorf(
							"eligibilitywire: %s assigns a unit_code from a multi-value expression `%s`; "+
								"the scan cannot evaluate it", where(typed.Pos()), types.ExprString(typed.Rhs[0]))
						return false
					}
					if err := classify(typed.Rhs[i], fn, node); err != nil {
						walkErr = err
						return false
					}
				}
			case *ast.ValueSpec:
				// `const walletUnitCode = "..."`, i.e. the refactor that
				// extracts a literal into a named constant. The
				// eligibility_status scan went red on exactly this shape once,
				// telling the reader to delete a value that was still in use.
				for i, name := range typed.Names {
					if !isUnitCodeName(name.Name) || i >= len(typed.Values) {
						continue
					}
					if err := classify(typed.Values[i], fn, node); err != nil {
						walkErr = err
						return false
					}
				}
			case *ast.KeyValueExpr:
				if !isUnitCodeKey(typed.Key) {
					break
				}
				if err := classify(typed.Value, fn, node); err != nil {
					walkErr = err
					return false
				}
			case *ast.BinaryExpr:
				// A comparison against a unit-code field is a check, not a
				// source of values, so a non-constant operand here is ignored
				// rather than refused: `manifest.UnitCode !=
				// expectedUnitForSource(sourceType)` introduces nothing and is
				// the ordinary way this codebase validates the field.
				if typed.Op != token.EQL && typed.Op != token.NEQ {
					break
				}
				for _, pair := range [][2]ast.Expr{{typed.X, typed.Y}, {typed.Y, typed.X}} {
					if !isUnitCodeExpr(pair[0]) {
						continue
					}
					if value, ok := constantString(info, pair[1]); ok {
						record(value, UnitCodePositional, pair[1].Pos(), fn)
					}
				}
			case *ast.ReturnStmt:
				if !unitCodeFuncPattern.MatchString(fn) {
					break
				}
				for _, result := range typed.Results {
					if value, ok := constantString(info, result); ok {
						record(value, UnitCodePositional, result.Pos(), fn)
					}
				}
			}
			// Net 2 runs over every node, independently of the positional
			// rules above and without stopping their descent.
			if expr, ok := n.(ast.Expr); ok {
				if text, ok := constantString(info, expr); ok {
					for _, match := range unitCodeShapePattern.FindAllString(text, -1) {
						record(match, UnitCodeShape, expr.Pos(), fn)
					}
				}
			}
			return true
		})
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			name := ""
			if fn.Name != nil {
				name = fn.Name.Name
				if unitCodeFuncPattern.MatchString(name) {
					scan.UnitFuncs[name] = true
				}
			}
			walk(fn, name)
		} else {
			walk(decl, "")
		}
		if walkErr != nil {
			return walkErr
		}
	}
	return nil
}

// localAssignments returns every expression assigned to name inside scope.
//
// It is deliberately name-based and scope-wide rather than flow-sensitive: an
// over-approximation here means MORE expressions get classified, and each of
// them must then be a constant, a copy, or a delegation. The failure direction
// is a refusal, never a miss. `resolvable` is false when name is written by
// something this helper cannot line up one-to-one (a tuple assignment, a range
// clause) -- the caller must then refuse rather than act on a partial answer.
func localAssignments(scope ast.Node, name string) (sources []ast.Expr, resolvable bool) {
	resolvable = true
	ast.Inspect(scope, func(n ast.Node) bool {
		switch typed := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range typed.Lhs {
				ident, ok := ast.Unparen(lhs).(*ast.Ident)
				if !ok || ident.Name != name {
					continue
				}
				if len(typed.Lhs) != len(typed.Rhs) {
					resolvable = false
					return false
				}
				sources = append(sources, typed.Rhs[i])
			}
		case *ast.ValueSpec:
			for i, ident := range typed.Names {
				if ident.Name != name || len(typed.Values) == 0 {
					continue
				}
				if len(typed.Names) != len(typed.Values) {
					resolvable = false
					return false
				}
				sources = append(sources, typed.Values[i])
			}
		case *ast.RangeStmt:
			for _, expr := range []ast.Expr{typed.Key, typed.Value} {
				if ident, ok := expr.(*ast.Ident); ok && ident.Name == name {
					resolvable = false
					return false
				}
			}
		}
		return true
	})
	return sources, resolvable
}

// isUnitCodeName reports whether an identifier names a unit code. `UnitCode`,
// `unitCode`, and any camel-case name ending in `UnitCode` (WalletUnitCode,
// OpeningBalanceUnitCode) count; `unitCodePattern` deliberately does not.
func isUnitCodeName(name string) bool {
	return name == "UnitCode" || name == "unitCode" || strings.HasSuffix(name, "UnitCode")
}

// isUnitCodeExpr reports whether expr names a unit code, through any number of
// parentheses and pointer dereferences.
func isUnitCodeExpr(expr ast.Expr) bool {
	for {
		switch typed := ast.Unparen(expr).(type) {
		case *ast.StarExpr:
			expr = typed.X
		case *ast.Ident:
			return isUnitCodeName(typed.Name)
		case *ast.SelectorExpr:
			return typed.Sel != nil && isUnitCodeName(typed.Sel.Name)
		default:
			return false
		}
	}
}

// isUnitCodeKey covers both `UnitCode: v` in a struct literal and
// `"unit_code": v` in a map literal (the DTO layer builds responses that way).
func isUnitCodeKey(key ast.Expr) bool {
	if ident, ok := ast.Unparen(key).(*ast.Ident); ok {
		return isUnitCodeName(ident.Name)
	}
	if literal, ok := ast.Unparen(key).(*ast.BasicLit); ok && literal.Kind == token.STRING {
		return strings.Trim(literal.Value, "\"`") == "unit_code"
	}
	return false
}

// isUnitCodePassthrough reports whether expr is a unit code copied from a value
// this scan can see -- which introduces no new vocabulary.
//
// The name alone is NOT enough, and the first cut of this function getting that
// wrong is what review caught: it accepted any `<something>.UnitCode`, so
// `m.UnitCode = zzelsewhere.WalletUnitCode` -- another package's variable, whose
// contents this scan never reads -- was waved through as "a copy". A value that
// comes from outside the scanned source is precisely what the refusal path
// exists for, and calling it a copy is the silent-gap answer wearing the name
// of a safe one.
//
// So the BASE of the selector has to resolve, here, to something that is not a
// package:
//
//   - `payload.UnitCode`, `*payload.WalletUnitCode`, `unitCode` -- the base is a
//     variable or parameter in this package, so the value came from somewhere
//     this scan walked (or from a caller, which the contract gate covers by
//     going red when nothing emits a declared code). Passthrough.
//   - `somepkg.WalletUnitCode` -- the base resolves to a *types.PkgName.
//     Refused.
//   - `zzelsewhere.WalletUnitCode` where zzelsewhere is not imported at all --
//     the base resolves to nothing. Also refused: "I could not tell what this
//     is" must never share an answer with "this is fine".
//
// Identifier resolution is used rather than the base's TYPE on purpose. The
// stub importer leaves every imported type invalid, so a type-based rule would
// refuse `item.UnitCode` for an `item` whose struct type lives in another
// package -- ordinary, correct code all over postgresstore and application.
func isUnitCodePassthrough(info *types.Info, expr ast.Expr) bool {
	if !isUnitCodeExpr(expr) {
		return false
	}
	base, ok := leftmostIdent(expr)
	if !ok {
		// `f().UnitCode`, `<-ch.UnitCode`: no identifier to ask about.
		return false
	}
	object, resolved := info.Uses[base]
	if !resolved || object == nil {
		return false
	}
	_, isPackage := object.(*types.PkgName)
	return !isPackage
}

// leftmostIdent walks down the left spine of a selector/deref/index chain to
// the identifier everything else hangs off. `*a.b[0].UnitCode` yields `a`.
func leftmostIdent(expr ast.Expr) (*ast.Ident, bool) {
	for {
		switch typed := ast.Unparen(expr).(type) {
		case *ast.Ident:
			return typed, true
		case *ast.SelectorExpr:
			expr = typed.X
		case *ast.StarExpr:
			expr = typed.X
		case *ast.IndexExpr:
			expr = typed.X
		default:
			return nil, false
		}
	}
}

// DiscoverUnitCodes is the vocabulary the code introduces, for the contract
// gate to compare against service_units in both directions.
func DiscoverUnitCodes() ([]string, UnitCodeScan, error) {
	scan, err := ScanUnitCodes()
	if err != nil {
		return nil, UnitCodeScan{}, err
	}
	return scan.Values(), scan, nil
}
