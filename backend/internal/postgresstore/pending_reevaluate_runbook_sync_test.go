package postgresstore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// pendingReevaluateRunbookPath is the operator-facing table this test holds
// the code to: docs/PRODUCTION-RUNBOOK.md's "STOP | What to do instead" table
// under "Re-evaluating one parked account".
const pendingReevaluateRunbookPath = "../../../docs/PRODUCTION-RUNBOOK.md"

// stopTableRowPattern matches a markdown table body row: two cells, neither
// empty, and not the header or the `| --- |` separator.
var stopTableRowPattern = regexp.MustCompile(`^\|[^|]+\|[^|]+\|$`)

// TestPendingReevaluateBlockersMatchTheRunbookTable is minor finding 7 from
// the first review: the STOP table listed eight rows while the code could
// refuse for ten reasons, so two refusals an operator could actually hit had
// no documented answer -- and nothing would have noticed the next time one
// was added.
//
// A table of "what to do instead" is only useful if it is complete, and the
// only way it stays complete is if incompleteness fails the build. This does
// not try to match wording (the rows are prose, deliberately); it holds the
// two to the same length, which is what catches a blocker added on one side
// and not the other.
func TestPendingReevaluateBlockersMatchTheRunbookTable(t *testing.T) {
	body, err := os.ReadFile(pendingReevaluateRunbookPath)
	if err != nil {
		t.Fatalf("read the runbook: %v", err)
	}
	rows := countStopTableRows(t, string(body))
	if rows != len(PendingReevaluateBlockerCodes) {
		t.Fatalf("the runbook's STOP table has %d rows but the tool can refuse for %d reasons (%v); "+
			"every refusal needs a row saying what to do instead",
			rows, len(PendingReevaluateBlockerCodes), PendingReevaluateBlockerCodes)
	}
}

// countStopTableRows finds the one STOP table and counts its body rows,
// failing rather than returning zero when it cannot find it -- a renamed
// heading must not silently turn this test into "0 == 0".
func countStopTableRows(t *testing.T, body string) int {
	t.Helper()
	const header = "| `STOP` | What to do instead |"
	start := strings.Index(body, header)
	if start < 0 {
		t.Fatalf("could not find the STOP table header %q in the runbook", header)
	}
	lines := strings.Split(body[start:], "\n")
	if len(lines) < 3 || !strings.Contains(lines[1], "---") {
		t.Fatalf("the STOP table does not have a separator row after its header: %q", lines[:min(3, len(lines))])
	}
	rows := 0
	for _, line := range lines[2:] {
		line = strings.TrimRight(strings.TrimSpace(line), "\r")
		if line == "" || !strings.HasPrefix(line, "|") {
			break
		}
		if !stopTableRowPattern.MatchString(line) {
			t.Fatalf("unexpected row shape in the STOP table: %q", line)
		}
		rows++
	}
	if rows == 0 {
		t.Fatal("the STOP table has no body rows; this test would otherwise pass vacuously")
	}
	return rows
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// discoverBlockerCodesFromSource parses pending_reevaluate_repair.go and
// returns the code of every check the repair can raise as a blocker: each
// add(code, name, passed, blocker, ...) call whose blocker argument is the
// literal true, plus the PendingReevaluateCheck literals built directly with
// Blocker: true.
//
// Discovered rather than hand-listed on purpose. A completeness check whose
// own input is a list someone has to remember to update is not a completeness
// check: forget the list entry and the runbook row together and it stays
// green, which is exactly the failure it exists to prevent.
func discoverBlockerCodesFromSource(t *testing.T) []string {
	t.Helper()
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "pending_reevaluate_repair.go", nil, 0)
	if err != nil {
		t.Fatalf("parse the repair source: %v", err)
	}
	seen := map[string]bool{}
	codes := []string{}
	record := func(code string) {
		if code != "" && !seen[code] {
			seen[code] = true
			codes = append(codes, code)
		}
	}
	stringLit := func(expr ast.Expr) string {
		lit, ok := expr.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return ""
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return ""
		}
		return value
	}
	isLiteral := func(expr ast.Expr, name string) bool {
		ident, ok := expr.(*ast.Ident)
		return ok && ident.Name == name
	}
	// add(code, name, passed, blocker, ...) stores Blocker: blocker && !passed.
	// So a call can raise a blocker unless the blocker argument is literally
	// false, or the passed argument is literally true -- the latter is how the
	// report's "this condition holds" variants are written, and they can never
	// block however the blocker flag reads.
	canBlock := func(passed, blocker ast.Expr) bool {
		return !isLiteral(blocker, "false") && !isLiteral(passed, "true")
	}
	ast.Inspect(parsed, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.CallExpr:
			ident, ok := typed.Fun.(*ast.Ident)
			if !ok || ident.Name != "add" || len(typed.Args) < 4 {
				return true
			}
			if canBlock(typed.Args[2], typed.Args[3]) {
				record(stringLit(typed.Args[0]))
			}
		case *ast.CompositeLit:
			name, ok := typed.Type.(*ast.Ident)
			if !ok || name.Name != "PendingReevaluateCheck" {
				return true
			}
			code := ""
			blocker, passed := ast.Expr(nil), ast.Expr(nil)
			for _, element := range typed.Elts {
				pair, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := pair.Key.(*ast.Ident)
				if !ok {
					continue
				}
				switch key.Name {
				case "Code":
					code = stringLit(pair.Value)
				case "Blocker":
					blocker = pair.Value
				case "Passed":
					passed = pair.Value
				}
			}
			// An absent Blocker field is the zero value: never a blocker.
			if blocker != nil && !isLiteral(blocker, "false") &&
				(passed == nil || !isLiteral(passed, "true")) {
				record(code)
			}
		}
		return true
	})
	if len(codes) == 0 {
		t.Fatal("found no blocker codes in the repair source; this test would otherwise pass vacuously")
	}
	sort.Strings(codes)
	return codes
}

// TestPendingReevaluateBlockerCodesAreDiscoveredNotListed closes minor finding
// c of the second review: PendingReevaluateBlockerCodes is a hand-written
// list, so a new blocker missing from both it and the runbook left the length
// comparison happily green. The declared list must now equal what the source
// actually raises.
func TestPendingReevaluateBlockerCodesAreDiscoveredNotListed(t *testing.T) {
	discovered := discoverBlockerCodesFromSource(t)
	declared := append([]string(nil), PendingReevaluateBlockerCodes...)
	sort.Strings(declared)
	if len(discovered) != len(declared) {
		t.Fatalf("the repair raises %d blocker codes %v but declares %d %v",
			len(discovered), discovered, len(declared), declared)
	}
	for i := range discovered {
		if discovered[i] != declared[i] {
			t.Fatalf("blocker codes differ: raised %v, declared %v", discovered, declared)
		}
	}
}
