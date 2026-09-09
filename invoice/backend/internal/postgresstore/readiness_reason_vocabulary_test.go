package postgresstore

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The readiness reasons a stream can carry are produced in Go and rendered in
// Chinese by the admin console. Nothing connected the two, and they had drifted
// three ways at once by the time anyone looked:
//
//   - ECONOMIC_RESCAN_ACTIVE was produced and had no label, so an operator
//     watching a bounded, expected rescan saw the words for "we do not know
//     what this is" on the source health page;
//   - SCAN_CYCLE_INCOMPLETE and CONFIGURATION_DRIFT had labels and no producer,
//     two sentences that could never appear.
//
// The rule below is the same shape as the containment rules in
// containment_discovery.go's tests, and for the same reason: a checker whose
// idea of "the reasons" is a list somebody typed agrees with itself forever.
// Both sides are discovered -- the Go side from the type that carries the
// field, the TypeScript side from the object literal that labels it -- and the
// comparison is exact in both directions.

// sourceStreamHealthTypeName is what makes a function a readiness-reason
// producer. Scope comes from the type rather than from a file name, so a
// second producer written in another package is found rather than missed.
const sourceStreamHealthTypeName = "SourceStreamHealth"

// sourceReasonLabelsRelPath is the admin console's label table.
const sourceReasonLabelsRelPath = "../../../web/src/App.tsx"

// sourceReasonLabelsDecl opens the table. Anchoring on the declaration rather
// than on a line number means a moved table is still found; a renamed or
// deleted one makes the scan refuse to answer rather than compare against an
// empty map.
const sourceReasonLabelsDecl = "const sourceReasonLabels: Record<string, string> = {"

// tsLabelEntryPattern reads one `KEY: "text",` line of that table.
var tsLabelEntryPattern = regexp.MustCompile(`^\s*([A-Z][A-Z0-9_]*)\s*:\s*"(.*)",?\s*$`)

// packageStringConstants collects package-level string constants per package
// directory, so that `append(item.Reasons, economicRescanActiveReason)` can be
// resolved to the value it will actually put on the wire. The containment
// rules use the same map to resolve a constant spliced into a query.
//
// Keys are directories relative to root, in slash form, so a caller holding a
// declaration's relative file path can look up its package directly.
func packageStringConstants(t *testing.T, root string) map[string]map[string]string {
	t.Helper()
	constants := map[string]map[string]string{}
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "vendor", "node_modules", "testdata", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		for _, decl := range parsed.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for index, name := range value.Names {
					if index >= len(value.Values) {
						continue
					}
					lit, ok := value.Values[index].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					text, unquoteErr := strconv.Unquote(lit.Value)
					if unquoteErr != nil {
						continue
					}
					if constants[dir] == nil {
						constants[dir] = map[string]string{}
					}
					constants[dir][name.Name] = text
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("scanning backend for string constants: %v", walkErr)
	}
	return constants
}

// signatureMentions reports whether a function's receiver, parameters or
// results name the given type.
func signatureMentions(decl *ast.FuncDecl, typeName string) bool {
	mentions := false
	inspect := func(node ast.Node) {
		if node == nil {
			return
		}
		ast.Inspect(node, func(inner ast.Node) bool {
			if ident, ok := inner.(*ast.Ident); ok && ident.Name == typeName {
				mentions = true
			}
			return !mentions
		})
	}
	if decl.Recv != nil {
		inspect(decl.Recv)
	}
	if decl.Type != nil {
		if decl.Type.Params != nil {
			inspect(decl.Type.Params)
		}
		if decl.Type.Results != nil {
			inspect(decl.Type.Results)
		}
	}
	return mentions
}

// discoverReadinessReasons returns every reason string the backend can put on
// a source stream, mapped to where it was found.
//
// It refuses to answer rather than answering with a gap. An appended value it
// cannot fold to a string constant -- a bind of some kind, a function call, a
// variable -- is an error, because the alternative is silently reporting a
// smaller vocabulary than the one that reaches the screen, and every consumer
// of this function would then compare against a set that is missing exactly
// the entry nobody thought about.
func discoverReadinessReasons(t *testing.T) map[string]string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	constants := packageStringConstants(t, root)
	reasons := map[string]string{}
	producers := 0
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "vendor", "node_modules", "testdata", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		parsed, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !signatureMentions(fn, sourceStreamHealthTypeName) {
				continue
			}
			appended := false
			var walkFail error
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				name, ok := call.Fun.(*ast.Ident)
				if !ok || name.Name != "append" || len(call.Args) < 2 {
					return true
				}
				target, ok := call.Args[0].(*ast.SelectorExpr)
				if !ok || target.Sel.Name != "Reasons" {
					return true
				}
				appended = true
				for _, arg := range call.Args[1:] {
					where := fmt.Sprintf("%s:%d (%s)",
						filepath.ToSlash(rel), fset.Position(arg.Pos()).Line, fn.Name.Name)
					switch typed := arg.(type) {
					case *ast.BasicLit:
						if typed.Kind != token.STRING {
							walkFail = fmt.Errorf("%s appends a non-string literal to Reasons", where)
							return false
						}
						text, unquoteErr := strconv.Unquote(typed.Value)
						if unquoteErr != nil {
							walkFail = fmt.Errorf("%s appends a string literal that will not unquote: %v", where, unquoteErr)
							return false
						}
						reasons[text] = where
					case *ast.Ident:
						text, known := constants[dir][typed.Name]
						if !known {
							walkFail = fmt.Errorf(
								"%s appends `%s` to Reasons, which this scan cannot resolve to a package-level "+
									"string constant. It refuses to answer rather than report a vocabulary that is "+
									"missing exactly the reason nobody thought about; either make it a constant in "+
									"this package or teach this scan how to read it", where, typed.Name)
							return false
						}
						reasons[text] = where
					default:
						walkFail = fmt.Errorf(
							"%s appends an expression this scan cannot evaluate as a constant (`%T`); see the note "+
								"on discoverReadinessReasons about refusing to answer", where, arg)
						return false
					}
				}
				return true
			})
			if walkFail != nil {
				t.Fatal(walkFail)
			}
			if appended {
				producers++
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("scanning backend for readiness reasons: %v", walkErr)
	}
	// Vacuity from both sides. Zero producers means the scan's idea of what a
	// producer looks like has gone stale; a short vocabulary means it found
	// one and read almost nothing out of it.
	if producers == 0 {
		t.Fatalf("no function in the backend both mentions %s in its signature and appends to a Reasons "+
			"field; either the evaluator moved to a shape this scan cannot see, or the type was renamed",
			sourceStreamHealthTypeName)
	}
	if len(reasons) < 8 {
		t.Fatalf("only %d readiness reasons discovered from %d producer(s); the evaluator states more than "+
			"that, so this scan is reading almost none of it", len(reasons), producers)
	}
	return reasons
}

// discoverSourceReasonLabels reads the admin console's label table.
//
// Like the Go side, it refuses to answer rather than answering with a gap: a
// line inside the table it cannot read as one `KEY: "text"` entry is an error,
// because skipping it would quietly shrink the set being compared.
func discoverSourceReasonLabels(t *testing.T) map[string]string {
	t.Helper()
	body, err := os.ReadFile(sourceReasonLabelsRelPath)
	if err != nil {
		t.Fatalf("reading the admin console's label table: %v", err)
	}
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	start := -1
	for index, line := range lines {
		if strings.TrimSpace(line) == sourceReasonLabelsDecl {
			if start >= 0 {
				t.Fatalf("%s declares sourceReasonLabels twice; this scan cannot tell which one the page "+
					"renders from", sourceReasonLabelsRelPath)
			}
			start = index
		}
	}
	if start < 0 {
		t.Fatalf("%s no longer contains `%s`. If the table was renamed or moved, this rule stopped "+
			"comparing anything and has to be pointed at the new one",
			sourceReasonLabelsRelPath, sourceReasonLabelsDecl)
	}
	labels := map[string]string{}
	closed := false
	for _, line := range lines[start+1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "};" {
			closed = true
			break
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		match := tsLabelEntryPattern.FindStringSubmatch(line)
		if match == nil {
			t.Fatalf("%s: this scan cannot read `%s` as one readiness-reason label. Skipping it would "+
				"compare against a table smaller than the one the page renders, so it refuses instead",
				sourceReasonLabelsRelPath, trimmed)
		}
		if previous, duplicate := labels[match[1]]; duplicate {
			t.Fatalf("%s labels %s twice (%q then %q); the later one wins at runtime and the earlier one "+
				"is dead text", sourceReasonLabelsRelPath, match[1], previous, match[2])
		}
		labels[match[1]] = match[2]
	}
	if !closed {
		t.Fatalf("%s: the sourceReasonLabels table has no closing `};`", sourceReasonLabelsRelPath)
	}
	if len(labels) < 8 {
		t.Fatalf("only %d labels read out of %s; the table holds more than that, so this scan is reading "+
			"almost none of it", len(labels), sourceReasonLabelsRelPath)
	}
	return labels
}

func sortedKeys(set map[string]string) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// TestEveryReadinessReasonHasExactlyOneLabel is the cross-check: the reasons
// the backend can produce and the reasons the admin console can name are the
// same set, no more and no less.
//
// Both failure directions are real and both were live when this was written.
// A produced reason with no label renders as 未识别的安全阻断原因 -- the page
// telling an operator, during an incident, that it does not know what its own
// backend just said. A label with no producer is a sentence that can never
// appear, which is harmless until someone reads the list to find out what the
// system can report and believes it.
func TestEveryReadinessReasonHasExactlyOneLabel(t *testing.T) {
	produced := discoverReadinessReasons(t)
	labelled := discoverSourceReasonLabels(t)

	for _, reason := range sortedKeys(produced) {
		if _, ok := labelled[reason]; !ok {
			t.Errorf("%s is produced at %s and has no label in %s, so the source health page shows an "+
				"operator the words for \"unrecognised\" while the backend knows exactly what it means. "+
				"Add the Chinese copy for it",
				reason, produced[reason], sourceReasonLabelsRelPath)
		}
	}
	for _, reason := range sortedKeys(labelled) {
		if _, ok := produced[reason]; !ok {
			t.Errorf("%s is labelled in %s (%q) but nothing in the backend produces it. Delete the label: "+
				"a reason list that includes text the system can never emit is read as documentation and "+
				"is wrong", reason, sourceReasonLabelsRelPath, labelled[reason])
		}
	}

	// The non-fatal set is part of the same vocabulary and drifts the same
	// way. An entry here that nothing produces silently marks nothing as
	// non-fatal, which is the failure that would take a stream out of
	// rotation for a condition someone had already decided was tolerable.
	for reason := range nonFatalStreamHealthReasons {
		if _, ok := produced[reason]; !ok {
			t.Errorf("nonFatalStreamHealthReasons excuses %s, which nothing produces any more; it is "+
				"excusing nothing, and the condition it was written for is now fatal again", reason)
		}
	}
}
