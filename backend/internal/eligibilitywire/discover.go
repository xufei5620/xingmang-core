package eligibilitywire

// Discovery: where the eligibility_status vocabulary actually comes from.
//
// The first cut of this package discovered only the PERSISTED statuses (from
// the migration CHECK) and hand-listed the two response-time-only ones in the
// contract. That left the gate blind in exactly the shape of the incident it
// exists to prevent: RC58 introduced source_unavailable as a response-time
// override in application/service.go, no migration was involved, and nothing
// would have gone red if the contract had simply not mentioned it. Worse, the
// funding-lot probe took its INPUT SPACE from contract.LotEligibilityStatus --
// the very field it was meant to validate -- so a third synthetic status could
// be added, never fed to the emitter, and every probe would stay green.
//
// So the code is scanned too. A status value can enter a response in exactly
// two ways that the column's CHECK constraint does not cover:
//
//	1. Go assigns a value to a FundingLot/EligibilitySummary EligibilityStatus
//	   field (application/service.go's freshness override is this), or
//	2. a query supplies a default for a NULL column
//	   (COALESCE(eas.eligibility_status,'missing') is this).
//
// Both are found by walking the source, so adding a third one makes the probes
// go red on their own rather than waiting for someone to remember the contract.
//
// The RECOGNITION rule is where the second cut of this file went wrong. It
// matched *ast.BasicLit only -- a bare "..." on the right-hand side -- so
// `const probe = "x"; lot.EligibilityStatus = probe` was invisible, and worse,
// invisible silently: no literal found reads exactly like no status introduced,
// and every probe stays green. In the other direction, extracting the EXISTING
// "source_unavailable" into a named constant (a pure refactor) went red with
// "declared but no longer introduced anywhere", i.e. the gate told the reader
// to delete the value from the contract -- the incident this package exists to
// prevent, re-enacted by the gate itself.
//
// So values are now resolved with go/types constant evaluation
// (types.Info.Types[expr].Value): a literal, a named constant, a concatenation
// of constants, a constant from anywhere in the same package all evaluate the
// same. And the one hand-written part that remains -- "this is the set of
// shapes I understand" -- refuses to answer rather than answering with a gap:
// an EligibilityStatus assigned from an expression the checker cannot fold to
// a constant (a variable, a helper's return value, a conversion) makes the scan
// return an error naming file:line, unless it is a passthrough of another
// EligibilityStatus field, which introduces no vocabulary. Likewise a COALESCE
// over eligibility_status whose default is not a single-quoted literal in the
// (constant) query text is an error, not "no default found".
//
// The type check is deliberately lenient about IMPORTS: every imported package
// is stubbed empty, so anything reached through an import is an unresolved
// expression rather than a build-time dependency on the whole module graph.
// Constant folding of in-package values does not need imports, and a status
// spelled as a constant from ANOTHER package therefore lands on the
// refuse-to-answer path -- which is the correct answer for a scan that only
// walks these three directories.

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// scannedPackageDirs are the packages that can put an eligibility_status on the
// wire. Scanning more than strictly necessary is the safe direction: an extra
// literal found somewhere unexpected makes a probe red and forces a human to
// say what it is, which is the outcome we want.
var scannedPackageDirs = [][]string{
	{"backend", "internal", "application"},
	{"backend", "internal", "postgresstore"},
	{"backend", "internal", "httpapi"},
}

const (
	// LiteralAssignment is a Go assignment of a string literal to an
	// EligibilityStatus field -- a response-time override.
	LiteralAssignment = "assignment"
	// LiteralCoalesceDefault is a SQL COALESCE default for the
	// eligibility_status column -- the value a row with no eligibility state
	// row at all reports.
	LiteralCoalesceDefault = "coalesce-default"
)

var (
	coalesceDefaultPattern = regexp.MustCompile(
		`(?i)COALESCE\s*\(\s*[A-Za-z_][A-Za-z0-9_]*\.eligibility_status\s*,\s*'([^']*)'`,
	)
	// coalesceAnyDefaultPattern is the shape-only half: every COALESCE over the
	// column, whatever its default is. The scan refuses to answer for any match
	// here that coalesceDefaultPattern cannot read (a bind parameter, another
	// column, a CASE), because "no literal default" and "a default I could not
	// read" must not be the same green.
	coalesceAnyDefaultPattern = regexp.MustCompile(
		`(?i)COALESCE\s*\(\s*[A-Za-z_][A-Za-z0-9_]*\.eligibility_status\s*,`,
	)
)

// StatusLiteral is one eligibility_status value that CODE introduces, as
// opposed to one the database column is allowed to hold. It carries its
// location because a probe that only reports "an unexpected value appeared"
// leaves the next person grepping; the point of discovery is to hand them the
// line.
type StatusLiteral struct {
	Value string
	Kind  string
	File  string // repository-relative, slash-separated
	Line  int
	Func  string // enclosing function, "" at package scope
}

func (l StatusLiteral) String() string {
	where := l.Func
	if where == "" {
		where = "(package scope)"
	}
	return fmt.Sprintf("%q [%s] %s:%d in %s", l.Value, l.Kind, l.File, l.Line, where)
}

// StatusScan is the result of walking those packages.
type StatusScan struct {
	Literals []StatusLiteral
	// Funcs is every non-test function declared in the scanned packages. A
	// probe that scopes itself to a named function uses this to assert the
	// function still exists: scoping by name and finding nothing would
	// otherwise read exactly like "this function introduces no statuses".
	Funcs map[string]bool
}

// InFunc returns the literals declared inside the named function.
func (s StatusScan) InFunc(name string) []StatusLiteral {
	out := []StatusLiteral{}
	for _, literal := range s.Literals {
		if literal.Func == name {
			out = append(out, literal)
		}
	}
	return out
}

// ScanStatusLiterals walks the scanned packages' non-test sources.
func ScanStatusLiterals() (StatusScan, error) {
	root, err := repoRoot()
	if err != nil {
		return StatusScan{}, err
	}
	scan := StatusScan{Literals: []StatusLiteral{}, Funcs: map[string]bool{}}
	for _, parts := range scannedPackageDirs {
		dir := filepath.Join(append([]string{root}, parts...)...)
		if err := scanPackageDir(&scan, dir, strings.Join(parts, "/")); err != nil {
			return StatusScan{}, err
		}
	}
	sort.Slice(scan.Literals, func(i, j int) bool {
		if scan.Literals[i].File != scan.Literals[j].File {
			return scan.Literals[i].File < scan.Literals[j].File
		}
		return scan.Literals[i].Line < scan.Literals[j].Line
	})
	return scan, nil
}

// stubImporter hands go/types an empty, complete package for every import so
// the checker can fold this package's own constants without the module graph.
// Anything reached through an import is simply unresolved; see the file
// comment for why that is the wanted answer.
type stubImporter struct{}

func (stubImporter) Import(path string) (*types.Package, error) {
	name := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		name = path[i+1:]
	}
	pkg := types.NewPackage(path, name)
	pkg.MarkComplete()
	return pkg, nil
}

// scanPackageDir parses every non-test .go file in dir as one package,
// type-checks it leniently, and records the eligibility_status values the
// package introduces. rel is the repository-relative, slash-separated
// directory used in locations.
func scanPackageDir(scan *StatusScan, dir, rel string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("eligibilitywire: scan %s: %w", dir, err)
	}
	names := []string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return fmt.Errorf("eligibilitywire: scan %s: no Go source files; the scanned package list has gone stale", rel)
	}
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(names))
	relOf := map[*ast.File]string{}
	for _, name := range names {
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("eligibilitywire: parse %s/%s: %w", rel, name, err)
		}
		files = append(files, file)
		relOf[file] = rel + "/" + name
	}
	// Uses is recorded alongside Types because the passthrough rule has to ask
	// what the BASE of a selector resolves to -- a package, a variable, or
	// nothing at all. Identifier resolution survives the errors that the stub
	// importer causes for cross-package TYPES; see passthrough.go.
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Uses:  map[*ast.Ident]types.Object{},
	}
	config := types.Config{
		Importer:    stubImporter{},
		FakeImportC: true,
		// Errors are expected in bulk (every import is a stub). They are
		// swallowed on purpose: the checker keeps going and still records
		// constant values for everything it could fold.
		Error: func(error) {},
	}
	// The package name only matters for error text; the files agree on it.
	pkgName := files[0].Name.Name
	_, _ = config.Check(pkgName, fset, files, info)

	for _, file := range files {
		if err := scanTypedFile(scan, fset, info, file, relOf[file]); err != nil {
			return err
		}
	}
	return nil
}

func scanTypedFile(scan *StatusScan, fset *token.FileSet, info *types.Info, file *ast.File, rel string) error {
	where := func(pos token.Pos) string {
		return fmt.Sprintf("%s:%d", rel, fset.Position(pos).Line)
	}
	record := func(value, kind string, pos token.Pos, fn string) {
		scan.Literals = append(scan.Literals, StatusLiteral{
			Value: value, Kind: kind, File: rel, Line: fset.Position(pos).Line, Func: fn,
		})
	}
	// statusValue classifies an expression that lands in an EligibilityStatus
	// field: a constant is recorded, a passthrough of another EligibilityStatus
	// field introduces nothing, and everything else is refused.
	statusValue := func(expr ast.Expr, fn string) error {
		if value, ok := constantString(info, expr); ok {
			record(value, LiteralAssignment, expr.Pos(), fn)
			return nil
		}
		if isStatusPassthrough(info, expr) {
			return nil
		}
		return fmt.Errorf(
			"eligibilitywire: %s assigns EligibilityStatus from `%s`, which the scan cannot evaluate as a constant; "+
				"the status vocabulary this introduces is unknowable here. Assign a constant (or pass another "+
				"EligibilityStatus field through), or teach discover.go the shape -- do not let it answer with a gap",
			where(expr.Pos()), types.ExprString(expr))
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
					if !isStatusField(lhs) {
						continue
					}
					if len(typed.Lhs) != len(typed.Rhs) {
						// a.EligibilityStatus, err = f(): the value is a
						// tuple element no constant folding can see.
						walkErr = fmt.Errorf(
							"eligibilitywire: %s assigns EligibilityStatus from a multi-value expression `%s`; "+
								"the scan cannot evaluate it", where(typed.Pos()), types.ExprString(typed.Rhs[0]))
						return false
					}
					if err := statusValue(typed.Rhs[i], fn); err != nil {
						walkErr = err
						return false
					}
				}
			case *ast.KeyValueExpr:
				key, ok := typed.Key.(*ast.Ident)
				if !ok || key.Name != "EligibilityStatus" {
					break
				}
				if err := statusValue(typed.Value, fn); err != nil {
					walkErr = err
					return false
				}
			case ast.Expr:
				// Any constant string expression (a literal, a named constant,
				// a concatenation) is a candidate query text. A match stops the
				// descent so the same text is not recorded again from its own
				// sub-expressions.
				text, ok := constantString(info, typed)
				if !ok {
					break
				}
				readable := coalesceDefaultPattern.FindAllStringSubmatch(text, -1)
				if len(coalesceAnyDefaultPattern.FindAllStringIndex(text, -1)) > len(readable) {
					walkErr = fmt.Errorf(
						"eligibilitywire: %s contains a COALESCE over eligibility_status whose default is not a "+
							"single-quoted literal; the scan cannot read what value a NULL row reports there",
						where(typed.Pos()))
					return false
				}
				if len(readable) == 0 {
					break
				}
				for _, match := range readable {
					record(match[1], LiteralCoalesceDefault, typed.Pos(), fn)
				}
				return false
			}
			return true
		})
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			name := ""
			if fn.Name != nil {
				name = fn.Name.Name
				scan.Funcs[name] = true
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

// constantString returns the folded value of expr when the checker resolved it
// to a string constant. A bare literal that the checker did not record (its
// enclosing expression was invalid past recovery) is still a constant by
// definition, so it is read directly rather than dropped.
func constantString(info *types.Info, expr ast.Expr) (string, bool) {
	if tv, ok := info.Types[expr]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
		return constant.StringVal(tv.Value), true
	}
	if literal, ok := ast.Unparen(expr).(*ast.BasicLit); ok && literal.Kind == token.STRING {
		value := constant.MakeFromLiteral(literal.Value, token.STRING, 0)
		if value.Kind() == constant.String {
			return constant.StringVal(value), true
		}
	}
	return "", false
}

// isStatusField reports whether expr is `<anything>.EligibilityStatus`. This is
// the POSITION test: on the left of an assignment it selects the field being
// written, and a write to another package's status field is still a write this
// scan has to account for, so the name alone is the right question here.
//
// Pointer derefs are unwrapped, matching what isUnitCodeExpr does on the other
// scan. Without that, `*row.EligibilityStatus` was not a status position at all,
// which happened to make it refused for the WRONG reason -- and a probe written
// against that shape passes whether the passthrough rule is right or not. A
// deref of a `*string` status field is an ordinary read; whether it counts as a
// copy is isStatusPassthrough's decision to make, not this function's.
func isStatusField(expr ast.Expr) bool {
	for {
		switch typed := ast.Unparen(expr).(type) {
		case *ast.StarExpr:
			expr = typed.X
		case *ast.SelectorExpr:
			return typed.Sel != nil && typed.Sel.Name == "EligibilityStatus"
		default:
			return false
		}
	}
}

// isStatusPassthrough reports whether expr READS a status from a value this
// scan can account for, which introduces no new vocabulary.
//
// This is a different question from isStatusField, and conflating the two is
// the bug this function had until the unit-code scan's review turned it up:
// `l.EligibilityStatus = zzelsewhere.EligibilityStatus` ends in the right name,
// so it counted as "a copy of another status field" -- but the thing being
// copied lives in a package this scan never reads, and its vocabulary is
// therefore unknown. That is what the refusal path exists for.
//
// The base-of-the-selector rule is shared with the unit-code scan; see
// passthrough.go.
func isStatusPassthrough(info *types.Info, expr ast.Expr) bool {
	return isStatusField(expr) && isCopyOfAReadableValue(info, expr)
}

// --- persisted statuses, discovered from the migrations -------------------

var (
	// statusCheckPattern requires the CHECK context rather than matching a bare
	// `eligibility_status IN (...)`, which also appears in ordinary WHERE
	// clauses -- reading one of those as "the constraint" would give the probe
	// a confident wrong answer.
	statusCheckPattern = regexp.MustCompile(`(?i)CHECK\s*\(\s*eligibility_status\s+IN\s*\(([^)]*)\)`)
	sqlSingleQuoted    = regexp.MustCompile(`'([^']*)'`)
	// constraintVocabulary is what makes the "I could not parse this" gate
	// shape-based instead of name-based. The first version of this check keyed
	// on the substring "eligibility_status_check" -- the CURRENT constraint's
	// name -- so a migration that replaced the CHECK with, say, a lookup-table
	// foreign key would never mention it, and the probe would go on answering
	// with migration 0020's values forever without ever going red. A gate whose
	// judgement is a copy of one particular implementation's spelling is the
	// stale-guard failure mode, not a gate.
	constraintVocabulary = regexp.MustCompile(`(?i)\b(CHECK|CONSTRAINT|REFERENCES|ENUM|CREATE\s+TYPE|CREATE\s+DOMAIN)\b`)
)

// PersistedStatuses is the column's own vocabulary plus the migration it was
// read from, so a failure can name the file.
type PersistedStatuses struct {
	Values    []string
	Migration string
}

// DiscoverPersistedStatuses reads the newest migration that states
// source_account_eligibility_state's eligibility_status CHECK and returns that
// constraint's literals.
//
// It refuses to answer at all when a NEWER migration reshapes what the column
// may hold in a form this parser does not understand. Returning 0020's values
// in that situation would be the worst possible behaviour: silently correct
// today, silently wrong the day someone widens the column, and green either way.
func DiscoverPersistedStatuses() (PersistedStatuses, error) {
	dir, err := MigrationsDir()
	if err != nil {
		return PersistedStatuses{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return PersistedStatuses{}, fmt.Errorf("eligibilitywire: read migrations: %w", err)
	}
	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	found := PersistedStatuses{}
	unparsed := []string{}
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return PersistedStatuses{}, err
		}
		text := stripSQLComments(string(body))
		if !strings.Contains(text, "eligibility_status") {
			continue
		}
		matches := statusCheckPattern.FindAllStringSubmatch(text, -1)
		if len(matches) > 0 {
			values := sqlLiterals(matches[0][1])
			for _, other := range matches[1:] {
				if !sameSet(values, sqlLiterals(other[1])) {
					return PersistedStatuses{}, fmt.Errorf(
						"eligibilitywire: migration %s states two different eligibility_status CHECK constraints (%v vs %v); "+
							"the probe cannot tell which one is in force", name, values, sqlLiterals(other[1]))
				}
			}
			found = PersistedStatuses{Values: values, Migration: name}
			unparsed = nil
			continue
		}
		for _, statement := range splitSQLStatements(text) {
			if !strings.Contains(statement, "eligibility_status") {
				continue
			}
			if !constraintVocabulary.MatchString(statement) {
				continue
			}
			unparsed = append(unparsed, fmt.Sprintf("%s: %s", name, condense(statement)))
			break
		}
	}
	if found.Migration == "" {
		return PersistedStatuses{}, fmt.Errorf(
			"eligibilitywire: no migration states an eligibility_status CHECK constraint; statusCheckPattern has gone stale")
	}
	if len(unparsed) > 0 {
		return PersistedStatuses{}, fmt.Errorf(
			"eligibilitywire: these migrations are newer than %s and constrain eligibility_status in a shape this probe "+
				"cannot parse:\n  %s\nteach DiscoverPersistedStatuses that shape rather than letting it keep answering "+
				"with %s's constraint", found.Migration, strings.Join(unparsed, "\n  "), found.Migration)
	}
	return found, nil
}

func sqlLiterals(fragment string) []string {
	out := []string{}
	for _, match := range sqlSingleQuoted.FindAllStringSubmatch(fragment, -1) {
		out = append(out, match[1])
	}
	sort.Strings(out)
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	extra, missing := Diff(Set(a), Set(b))
	return len(extra) == 0 && len(missing) == 0
}

// stripSQLComments removes `--` comments while respecting single-quoted
// literals, so a comment that merely mentions the column cannot trip the
// unparsed-migration gate and a literal containing `--` cannot be truncated.
func stripSQLComments(text string) string {
	var out strings.Builder
	inQuote := false
	for i := 0; i < len(text); i++ {
		char := text[i]
		if inQuote {
			out.WriteByte(char)
			if char == '\'' {
				inQuote = false
			}
			continue
		}
		if char == '\'' {
			inQuote = true
			out.WriteByte(char)
			continue
		}
		if char == '-' && i+1 < len(text) && text[i+1] == '-' {
			for i < len(text) && text[i] != '\n' {
				i++
			}
			out.WriteByte('\n')
			continue
		}
		out.WriteByte(char)
	}
	return out.String()
}

// splitSQLStatements splits on `;` outside single quotes. It is deliberately
// statement-level: a migration may legitimately add CHECK constraints to other
// tables in the same file (0026 does) while merely reading eligibility_status
// somewhere else, and a file-level check would call that a drift.
func splitSQLStatements(text string) []string {
	out := []string{}
	inQuote := false
	start := 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\'':
			inQuote = !inQuote
		case ';':
			if !inQuote {
				out = append(out, text[start:i])
				start = i + 1
			}
		}
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	return out
}

func condense(statement string) string {
	collapsed := strings.Join(strings.Fields(statement), " ")
	if len(collapsed) > 160 {
		return collapsed[:160] + "..."
	}
	return collapsed
}

// --- the input spaces the emitter probes must use -------------------------

// summaryStoreFunc and summaryServiceFunc are the two functions the eligibility
// summary's status passes through. Naming WHERE to look is unavoidable; naming
// WHAT it may contain is what this package refuses to do. ScanStatusLiterals's
// Funcs set is asserted to still contain both, so a rename cannot quietly turn
// this into a scan of nothing.
const (
	summaryStoreFunc   = "ListEligibilitySummaries"
	summaryServiceFunc = "ListUserEligibilitySummaries"
)

// LotStatusInputSpace is the set of eligibility_status values a probe must feed
// the funding-lot emitter: everything the column may hold, plus every literal
// the code can introduce.
//
// It deliberately does NOT read contract.LotEligibilityStatus. An input space
// taken from the field it is meant to validate can never discover a value
// missing from that field -- it just agrees with itself.
func LotStatusInputSpace() ([]string, error) {
	persisted, err := DiscoverPersistedStatuses()
	if err != nil {
		return nil, err
	}
	scan, err := ScanStatusLiterals()
	if err != nil {
		return nil, err
	}
	space := Set(persisted.Values)
	for _, literal := range scan.Literals {
		space[literal.Value] = true
	}
	return sortedKeys(space), nil
}

// SummaryStatusInputSpace is the same thing for the eligibility summary, which
// is a NARROWER set than the funding lot's: the summary rides
// COALESCE(eas.eligibility_status,'syncing') and its service method applies no
// response-time override, so neither `missing` nor `source_unavailable` can
// reach it. The first version of the contract asserted the two sets were equal
// "because they are the same column", which is not what the code does.
func SummaryStatusInputSpace() ([]string, error) {
	persisted, err := DiscoverPersistedStatuses()
	if err != nil {
		return nil, err
	}
	scan, err := ScanStatusLiterals()
	if err != nil {
		return nil, err
	}
	for _, name := range []string{summaryStoreFunc, summaryServiceFunc} {
		if !scan.Funcs[name] {
			return nil, fmt.Errorf(
				"eligibilitywire: %s no longer exists in the scanned packages; the summary status scan is looking at "+
					"nothing. Point summaryStoreFunc/summaryServiceFunc at the functions that carry the summary's "+
					"eligibility_status today", name)
		}
	}
	space := Set(persisted.Values)
	for _, name := range []string{summaryStoreFunc, summaryServiceFunc} {
		for _, literal := range scan.InFunc(name) {
			space[literal.Value] = true
		}
	}
	return sortedKeys(space), nil
}

// SyntheticStatuses is every discovered literal the column cannot itself hold.
// The second return value keeps the locations for the failure message.
func SyntheticStatuses() ([]string, []StatusLiteral, error) {
	persisted, err := DiscoverPersistedStatuses()
	if err != nil {
		return nil, nil, err
	}
	scan, err := ScanStatusLiterals()
	if err != nil {
		return nil, nil, err
	}
	known := Set(persisted.Values)
	space := map[string]bool{}
	sources := []StatusLiteral{}
	for _, literal := range scan.Literals {
		if known[literal.Value] {
			continue
		}
		space[literal.Value] = true
		sources = append(sources, literal)
	}
	return sortedKeys(space), sources, nil
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
