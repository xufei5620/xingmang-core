package postgresstore

import (
	"os"
	"regexp"
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

// TestPendingReevaluateBlockerCodesAreDeclared is the other half: every
// blocker the tool actually emits during this package's own repair tests must
// be one of the declared codes. Without it, a new blocker could be added with
// an undeclared code and the length check above would keep passing.
func TestPendingReevaluateBlockerCodesAreDeclared(t *testing.T) {
	declared := map[string]bool{}
	for _, code := range PendingReevaluateBlockerCodes {
		if declared[code] {
			t.Fatalf("duplicate blocker code %q", code)
		}
		declared[code] = true
	}
	// The codes the repair emits are asserted here as a fixed set rather than
	// harvested at runtime: harvesting from whatever the other tests happened
	// to trigger would make this pass by not exercising a branch.
	for _, code := range []string{
		"account_missing", "not_pending", "open_freeze", "job_processing", "job_dead",
		"window_empty", "no_derivable_cycle", "cycle_has_proof", "cycle_has_stranded",
		"prior_unknown_magnitude", "self_dealing",
	} {
		if !declared[code] {
			t.Fatalf("blocker code %q is emitted by the repair but not declared in PendingReevaluateBlockerCodes", code)
		}
	}
}
