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
//	1. Go assigns a literal to a FundingLot/EligibilitySummary EligibilityStatus
//	   field (application/service.go's freshness override is this), or
//	2. a query supplies a literal default for a NULL column
//	   (COALESCE(eas.eligibility_status,'missing') is this).
//
// Both are found by walking the source, so adding a third one makes the probes
// go red on their own rather than waiting for someone to remember the contract.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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

var coalesceDefaultPattern = regexp.MustCompile(
	`(?i)COALESCE\s*\(\s*[A-Za-z_][A-Za-z0-9_]*\.eligibility_status\s*,\s*'([^']*)'`,
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
		entries, err := os.ReadDir(dir)
		if err != nil {
			return StatusScan{}, fmt.Errorf("eligibilitywire: scan %s: %w", dir, err)
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
		for _, name := range names {
			rel := strings.Join(append(append([]string{}, parts...), name), "/")
			if err := scanFile(&scan, filepath.Join(dir, name), rel); err != nil {
				return StatusScan{}, err
			}
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

func scanFile(scan *StatusScan, path, rel string) error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("eligibilitywire: parse %s: %w", rel, err)
	}
	record := func(value, kind string, pos token.Pos, fn string) {
		scan.Literals = append(scan.Literals, StatusLiteral{
			Value: value, Kind: kind, File: rel, Line: fset.Position(pos).Line, Func: fn,
		})
	}
	walk := func(node ast.Node, fn string) {
		ast.Inspect(node, func(n ast.Node) bool {
			switch typed := n.(type) {
			case *ast.AssignStmt:
				for i, lhs := range typed.Lhs {
					selector, ok := lhs.(*ast.SelectorExpr)
					if !ok || selector.Sel == nil || selector.Sel.Name != "EligibilityStatus" {
						continue
					}
					if i >= len(typed.Rhs) {
						continue
					}
					if value, ok := stringLiteral(typed.Rhs[i]); ok {
						record(value, LiteralAssignment, typed.Rhs[i].Pos(), fn)
					}
				}
			case *ast.KeyValueExpr:
				key, ok := typed.Key.(*ast.Ident)
				if !ok || key.Name != "EligibilityStatus" {
					break
				}
				if value, ok := stringLiteral(typed.Value); ok {
					record(value, LiteralAssignment, typed.Value.Pos(), fn)
				}
			case *ast.BasicLit:
				if typed.Kind != token.STRING {
					break
				}
				text, err := strconv.Unquote(typed.Value)
				if err != nil {
					break
				}
				for _, match := range coalesceDefaultPattern.FindAllStringSubmatch(text, -1) {
					record(match[1], LiteralCoalesceDefault, typed.Pos(), fn)
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
				scan.Funcs[name] = true
			}
			walk(fn, name)
			continue
		}
		walk(decl, "")
	}
	return nil
}

func stringLiteral(expr ast.Expr) (string, bool) {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return "", false
	}
	return value, true
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
