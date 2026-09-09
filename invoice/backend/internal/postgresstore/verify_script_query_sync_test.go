package postgresstore

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// verifyScriptRelPath is the production planner check
// (deploy/postgres/verify-source-readiness-index.sh). It EXPLAINs the
// readiness query against the real database with real statistics, which no
// test can do -- and to do that it carries its own copy of the query.
const verifyScriptRelPath = "../../../deploy/postgres/verify-source-readiness-index.sh"

// sqlLineCommentPattern strips `--` comments, which the Go query carries and
// the shell copy does not. Only whole-line comments appear in either, so the
// pattern is anchored to avoid eating a `--` inside a string literal (there
// are none today, and this keeps it that way if one ever appears).
var sqlLineCommentPattern = regexp.MustCompile(`(?m)^[ \t]*--.*$`)

// normalizeReadinessQuery reduces both copies to the same canonical text. The
// two intentional differences are removed explicitly rather than tolerated by
// a fuzzy comparison, so that any other difference is a failure:
//
//   - the shell copy schema-qualifies tables as public.<table>, because it
//     runs with a locked-down search_path;
//   - the Go copy carries SQL comments, which the shell copy drops.
//
// Layout is then made irrelevant: whitespace runs collapse to one space, and a
// space is dropped entirely when either neighbour is not an identifier
// character. That last step cannot merge two identifiers into one -- a space
// between two word characters is always kept -- so it removes formatting
// differences without ever making two different queries compare equal.
func normalizeReadinessQuery(query string) string {
	query = sqlLineCommentPattern.ReplaceAllString(query, " ")
	query = strings.ReplaceAll(query, "public.", "")
	collapsed := strings.Join(strings.Fields(query), " ")
	var out strings.Builder
	out.Grow(len(collapsed))
	isWord := func(b byte) bool {
		return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
	}
	for i := 0; i < len(collapsed); i++ {
		if collapsed[i] == ' ' && i > 0 && i+1 < len(collapsed) &&
			(!isWord(collapsed[i-1]) || !isWord(collapsed[i+1])) {
			continue
		}
		out.WriteByte(collapsed[i])
	}
	return out.String()
}

// TestVerifyScriptPlansTheQueryTheServiceActuallyRuns makes the shell copy
// unable to drift.
//
// The copy had already fallen three changes behind the Go query before this
// slice: every green run of that script was proving an index path for a query
// the service no longer ran. Re-synchronising it by hand fixed the symptom and
// left the mechanism -- and a freshly-synced copy is more dangerous than a
// visibly stale one, because it is trusted. The maintenance rules written in
// the script's own comment are correct and completely unenforceable by
// reading, which is what a comment is.
//
// So the copy is now pinned here. Editing sourceReadinessHealthQuery without
// editing the script fails this test in the same commit that causes the drift,
// with the two normalised texts side by side to fix it from.
func TestVerifyScriptPlansTheQueryTheServiceActuallyRuns(t *testing.T) {
	script, err := os.ReadFile(filepath.FromSlash(verifyScriptRelPath))
	if err != nil {
		t.Fatalf("cannot read the deployment verifier; if it moved, this pin moves with it: %v", err)
	}
	text := string(script)

	// The copy is the heredoc inside emit_readiness_query. Delimiting on the
	// function and its quoted heredoc marker rather than on line numbers: the
	// script is edited by hand and line numbers are the first thing to rot.
	const opener = "emit_readiness_query() {\n  cat <<'SQL'\n"
	start := strings.Index(text, opener)
	if start < 0 {
		t.Fatal("emit_readiness_query no longer opens a quoted SQL heredoc in " + verifyScriptRelPath +
			"; the pin cannot find the copy it is supposed to be pinning")
	}
	rest := text[start+len(opener):]
	end := strings.Index(rest, "\nSQL\n")
	if end < 0 {
		t.Fatal("emit_readiness_query's SQL heredoc is not terminated in " + verifyScriptRelPath)
	}
	scriptQuery := normalizeReadinessQuery(rest[:end])
	goQuery := normalizeReadinessQuery(sourceReadinessHealthQuery)

	// Vacuity: a normaliser that collapsed either side to nothing would make
	// the comparison below pass for the wrong reason.
	if len(goQuery) < 1000 || len(scriptQuery) < 1000 {
		t.Fatalf("normalised query lengths look wrong (go=%d script=%d); the extraction or the "+
			"normaliser is broken, not the copies", len(goQuery), len(scriptQuery))
	}
	// The one thing this slice added has to be present on both sides, stated
	// separately so a future normaliser bug cannot hide it inside a passing
	// equality check.
	for name, query := range map[string]string{"go": goQuery, "script": scriptQuery} {
		if !strings.Contains(query, "dead_contained") || !strings.Contains(query, "dead_events_contained") {
			t.Fatalf("the %s copy of the readiness query does not compute containment at all", name)
		}
	}

	if goQuery == scriptQuery {
		return
	}
	// Point at the first divergence rather than dumping two 3KB strings.
	limit := len(goQuery)
	if len(scriptQuery) < limit {
		limit = len(scriptQuery)
	}
	diverged := limit
	for i := 0; i < limit; i++ {
		if goQuery[i] != scriptQuery[i] {
			diverged = i
			break
		}
	}
	window := func(text string) string {
		from := diverged - 60
		if from < 0 {
			from = 0
		}
		to := diverged + 90
		if to > len(text) {
			to = len(text)
		}
		return text[from:to]
	}
	t.Fatalf("deploy/postgres/verify-source-readiness-index.sh no longer carries the query the "+
		"service runs, so its EXPLAIN proves an index path for something else.\n"+
		"first divergence at character %d:\n  go:     ...%s...\n  script: ...%s...\n"+
		"update emit_readiness_query in the same commit that changes sourceReadinessHealthQuery.",
		diverged, window(goQuery), window(scriptQuery))
}
