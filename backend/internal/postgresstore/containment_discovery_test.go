package postgresstore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The discovery guards in this file all work the same way, and the shape is
// deliberate. A checker whose coverage is a hand-written list of the things it
// checks is green by construction for everything not on the list, which is how
// a gate stops covering what it was written to cover. So each rule below is
// stated as a property of the whole tree ("nowhere may X appear outside Y")
// rather than as a list of known call sites, and each one carries:
//
//   - a self-test: spellings a maintainer might plausibly write, asserted to
//     be caught. A matcher that has quietly stopped matching anything is worse
//     than no matcher, because it reads as coverage.
//   - a positive control: the definition the rule points at must actually be
//     found in the tree, so renaming or deleting it fails here instead of
//     silently emptying the rule.
//   - a vacuity check on the number of declarations scanned.
//
// The previous version of the first rule was a per-line strings.Contains pair.
// A code review broke it three ways without turning it red -- a FILTER split
// over two lines, spaces around the `=`, and `IN ('dead')` -- so the matching
// now happens on whitespace-normalised declaration text with regexps that
// cover the operator and quoting variants.

// goDecl is one top-level declaration (func, var, const, type) with its own
// source text, whitespace-normalised so that formatting cannot hide a match.
type goDecl struct {
	file       string
	name       string
	normalized string
}

// normalizeSource collapses every run of whitespace to a single space, so a
// statement broken across lines and indented reads the same as a one-liner.
func normalizeSource(source string) string {
	return strings.Join(strings.Fields(source), " ")
}

// scanBackendDecls parses every non-test Go file under backend/ -- not just
// this package -- and returns one entry per top-level declaration. Scanning
// the whole tree matters for the resolution-guard rule below: a freeze
// resolution written in another package could not call this package's
// unexported guard at all, and this rule is what would say so.
func scanBackendDecls(t *testing.T) []goDecl {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	decls := make([]goDecl, 0, 512)
	fset := token.NewFileSet()
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
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		parsed, parseErr := parser.ParseFile(fset, path, body, parser.ParseComments)
		if parseErr != nil {
			return parseErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		for _, decl := range parsed.Decls {
			start := fset.Position(decl.Pos()).Offset
			end := fset.Position(decl.End()).Offset
			if start < 0 || end > len(body) || start >= end {
				continue
			}
			name := "<anonymous>"
			switch typed := decl.(type) {
			case *ast.FuncDecl:
				name = typed.Name.Name
			case *ast.GenDecl:
				for _, spec := range typed.Specs {
					switch s := spec.(type) {
					case *ast.ValueSpec:
						if len(s.Names) > 0 {
							name = s.Names[0].Name
						}
					case *ast.TypeSpec:
						name = s.Name.Name
					}
					if name != "<anonymous>" {
						break
					}
				}
			}
			decls = append(decls, goDecl{
				file:       filepath.ToSlash(rel),
				name:       name,
				normalized: normalizeSource(string(body[start:end])),
			})
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	// Without this the whole file is vacuous the moment the walk stops
	// finding sources.
	if len(decls) < 200 {
		t.Fatalf("only %d top-level declarations scanned under backend/; the discovery guards are not looking at the tree", len(decls))
	}
	return decls
}

// handWrittenDeadFilterPattern matches a dead-event aggregate FILTER in any
// spelling a maintainer would plausibly reach for: `='dead'`, `= 'dead'`, and
// `IN ('dead'...)`, with the FILTER's own parenthesis and WHERE separated by
// any amount of whitespace (already collapsed to single spaces by then).
var handWrittenDeadFilterPattern = regexp.MustCompile(
	`FILTER ?\( ?WHERE.{0,240}?processing_status ?(?:=|IN) ?\(? ?'dead'`)

// freezeCorrelationPattern matches the containment correlation itself: an
// eligibility freeze's source_revision_hash equated to an ingest row's
// payload_hash, in either order and under any alias. This is the pairing that
// was retyped three times in the first cut of this slice.
var freezeCorrelationPattern = regexp.MustCompile(
	`(?:\w+\.)?source_revision_hash ?= ?(?:\w+\.)?payload_hash|(?:\w+\.)?payload_hash ?= ?(?:\w+\.)?source_revision_hash`)

// freezeResolutionPattern matches a write that resolves an eligibility freeze,
// with or without a table alias between the table name and SET.
var freezeResolutionPattern = regexp.MustCompile(
	`UPDATE eligibility_freezes(?: \w+)? SET status='resolved'`)

// TestEveryDeadEventCountIsRenderedFromOneDefinition holds the rule that a
// dead-event count may not be spelled out by hand anywhere: every one must
// come from sourceDeadEventCountColumnsSQL or sourceDeadEventFlagColumnsSQL,
// so a fifth health surface cannot be added without consulting containment.
func TestEveryDeadEventCountIsRenderedFromOneDefinition(t *testing.T) {
	// sourceDeadEventCountColumnsSQL is the only declaration allowed to
	// contain the literal pairing, because it is the one that writes it.
	// sourceDeadEventFlagColumnsSQL renders the same judgment as boolean
	// columns rather than a FILTER, so it is covered by the correlation rule
	// below instead.
	const renderer = "sourceDeadEventCountColumnsSQL"
	// The strongest control available: the matcher must recognise what the
	// definition actually produces. A matcher that no longer matches the real
	// thing would let every hand-written copy through while still reading as
	// coverage.
	if !handWrittenDeadFilterPattern.MatchString(normalizeSource(sourceDeadEventCountColumnsSQL("count(*)", "sie"))) {
		t.Fatal("the dead-FILTER matcher does not recognise the output of " + renderer +
			", so it cannot recognise a hand-written copy of it either")
	}
	// Self-test. If these do not trip the matcher, everything below is a green
	// light for nothing at all.
	for _, planted := range []string{
		`count(*) FILTER (WHERE processing_status='dead')`,
		"count(*) FILTER (\n\t\tWHERE sie.processing_status='dead')",
		`count(sie.event_id) FILTER (WHERE sie.processing_status = 'dead')`,
		`count(*) FILTER (WHERE sie.processing_status IN ('dead'))`,
		`count(*) FILTER (WHERE sie.processing_status IN ('dead','failed'))`,
		`count(*) FILTER (WHERE sie.stream_id='usage' AND sie.processing_status='dead')`,
	} {
		if !handWrittenDeadFilterPattern.MatchString(normalizeSource(planted)) {
			t.Fatalf("the dead-FILTER matcher does not catch a spelling a maintainer would write, "+
				"so this guard proves nothing:\n\t%s", planted)
		}
	}
	for _, allowed := range []string{
		`count(*) FILTER (WHERE processing_status='queued')`,
		`WHERE sie.processing_status='dead' AND sie.source_instance_id=$1`,
	} {
		if handWrittenDeadFilterPattern.MatchString(normalizeSource(allowed)) {
			t.Fatalf("the dead-FILTER matcher fires on something that is not an aggregate "+
				"dead FILTER, so it will be weakened to shut it up:\n\t%s", allowed)
		}
	}

	found := false
	for _, decl := range scanBackendDecls(t) {
		if !handWrittenDeadFilterPattern.MatchString(decl.normalized) {
			continue
		}
		if decl.name == renderer {
			found = true
			continue
		}
		t.Errorf("%s: %s spells a dead-event FILTER by hand instead of rendering it from "+
			"sourceDeadEventCountColumnsSQL/sourceDeadEventFlagColumnsSQL, so it cannot see containment",
			decl.file, decl.name)
	}
	// Positive control: a rule pointing at a definition the scan can no longer
	// find is a rule about nothing.
	if !found {
		t.Errorf("%s was not found in the scanned tree; if it was renamed or inlined, this rule "+
			"stopped covering anything", renderer)
	}
}

// TestContainmentCorrelationIsWrittenInOnePlace holds the harder half of the
// same rule. The FILTER guard above cannot see a correlation written as a JOIN
// or a bare EXISTS, and that is exactly how the first cut of this slice ended
// up with three copies of the containment predicate: one rendered definition
// plus two hand-written ones (the unfreeze guard and the carry-forward wait),
// each with its own narrowing bolted on.
//
// The narrowings are legitimate and different -- one freeze, one account -- so
// the rule is not "nobody else may ask": it is "nobody else may retype the
// core". sourceEventContainedByOpenFreezeSQL takes the narrowing as an
// argument precisely so that asking a narrower question costs one string
// rather than a copy.
func TestContainmentCorrelationIsWrittenInOnePlace(t *testing.T) {
	const definition = "sourceEventContainedByOpenFreezeSQL"
	// The definition assembles the correlation from fragments, so its own
	// source text does not contain the pattern -- which is why this rule needs
	// no allowed declaration at all, and why the control has to be run against
	// the rendered output instead. All three renderers are checked: each one
	// is a way the correlation reaches a query, and a matcher blind to any of
	// them is blind to a hand-written copy shaped like it.
	for name, rendered := range map[string]string{
		definition:                       sourceEventContainedByOpenFreezeSQL("sie"),
		"sourceDeadEventFlagColumnsSQL":  sourceDeadEventFlagColumnsSQL("sie"),
		"sourceDeadEventCountColumnsSQL": sourceDeadEventCountColumnsSQL("count(*)", "sie"),
	} {
		if !freezeCorrelationPattern.MatchString(normalizeSource(rendered)) ||
			!strings.Contains(rendered, "eligibility_freezes") {
			t.Fatalf("the containment-correlation matcher does not recognise what %s produces, "+
				"so it cannot recognise a hand-written copy of it either:\n\t%s", name, rendered)
		}
	}
	for _, planted := range []string{
		`ef.source_revision_hash=sie.payload_hash`,
		`ef.source_revision_hash = sie.payload_hash`,
		`sie.payload_hash=ef.source_revision_hash`,
		"JOIN eligibility_freezes f\n\t\t\t  ON f.source_revision_hash = e.payload_hash",
		`WHERE source_revision_hash=payload_hash`,
	} {
		if !freezeCorrelationPattern.MatchString(normalizeSource(planted)) {
			t.Fatalf("the containment-correlation matcher misses a spelling a maintainer would "+
				"write, so this guard proves nothing:\n\t%s", planted)
		}
	}
	if freezeCorrelationPattern.MatchString(normalizeSource(`WHERE status='open' AND source_revision_hash=$1`)) {
		t.Fatal("the correlation matcher fires on a bind-parameter lookup, which is a different " +
			"shape (a value against a column) and would force a contrived renderer")
	}

	callers := 0
	for _, decl := range scanBackendDecls(t) {
		if strings.Contains(decl.normalized, definition+"(") {
			callers++
		}
		if !strings.Contains(decl.normalized, "eligibility_freezes") ||
			!freezeCorrelationPattern.MatchString(decl.normalized) {
			continue
		}
		t.Errorf("%s: %s correlates an eligibility freeze's source_revision_hash to an ingest "+
			"row's payload_hash by hand. Render it from %s(alias, narrowing...) instead -- two "+
			"copies of this predicate disagree silently, and the disagreement is either a "+
			"re-opened source-wide outage or a permanently lost balance fact",
			decl.file, decl.name, definition)
	}
	// Vacuity from the other side: if nothing calls the definition any more,
	// "nobody writes it by hand" is trivially true and means nothing.
	if callers < 5 {
		t.Fatalf("only %d declarations call %s; the containment question is asked by the four "+
			"health surfaces, the cycle publisher, the carry-forward wait, the unfreeze guard and "+
			"the requeue repair, so the rule has lost sight of its callers", callers, definition)
	}
}

// TestEveryFreezeResolutionPassesTheDeadEventGuard is the rule the first cut
// of this slice most needed and did not have. An open freeze is the only thing
// standing between a dead event and a source-wide outage, so resolving one
// while its event is still dead un-contains the event; for a balance_checkpoint
// it also releases ensureBalanceCarryForwardProofTx's wait, and the projection
// worker then writes an immutable "balance unchanged" proof that migration
// 0014's trigger makes permanent.
//
// The first cut guarded the admin endpoint and documented one exemption. There
// were four other doors, three of them unguarded, and the sentence claiming
// otherwise was a survey by reading. This rule replaces the survey: no
// exemption list, no "this one is safe because of what it does elsewhere in
// the function" -- if a declaration resolves a freeze, it asks the guard.
func TestEveryFreezeResolutionPassesTheDeadEventGuard(t *testing.T) {
	const guard = "assertNoBlockingDeadEventForFreezeTx"
	for _, planted := range []string{
		`UPDATE eligibility_freezes SET status='resolved',resolved_at=now()`,
		`UPDATE eligibility_freezes ef SET status='resolved',resolved_at=now()`,
		"UPDATE eligibility_freezes\n\t\t\tSET status='resolved'",
	} {
		if !freezeResolutionPattern.MatchString(normalizeSource(planted)) {
			t.Fatalf("the freeze-resolution matcher misses a spelling a maintainer would write, "+
				"so this guard proves nothing:\n\t%s", planted)
		}
	}
	if freezeResolutionPattern.MatchString(normalizeSource(`UPDATE eligibility_freezes SET status='open'`)) {
		t.Fatal("the freeze-resolution matcher fires on a write that does not resolve anything")
	}

	doors := 0
	for _, decl := range scanBackendDecls(t) {
		if !freezeResolutionPattern.MatchString(decl.normalized) {
			continue
		}
		doors++
		if !strings.Contains(decl.normalized, guard+"(") {
			t.Errorf("%s: %s resolves an eligibility freeze without calling %s. Every door that "+
				"resolves a freeze has to establish the ordering for itself -- an open freeze is "+
				"the only thing containing a dead event, and a resolution that un-contains one "+
				"takes the whole source instance away from every account",
				decl.file, decl.name, guard)
		}
	}
	// Five doors today: the admin resolution endpoint plus the four repair
	// tools. The count is asserted low-side only -- a sixth door is allowed to
	// exist, it just has to call the guard -- but zero or one would mean the
	// matcher stopped finding the doors it was written for.
	if doors < 5 {
		t.Fatalf("only %d freeze-resolution sites found; this rule was written against 5 "+
			"(ResolveEligibilityFreeze plus the pre-anchor, balance-anchor, balance-blip and "+
			"queue-narrow repairs), so the matcher has stopped seeing them", doors)
	}
}
