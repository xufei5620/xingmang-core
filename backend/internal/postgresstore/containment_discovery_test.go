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
// source text, whitespace-normalised so that formatting cannot hide a match,
// and its own syntax tree, so that a rule asking "does this declaration call
// X" can ask the tree instead of the text.
type goDecl struct {
	file       string
	name       string
	normalized string
	node       ast.Decl
}

// normalizeSource collapses every run of whitespace to a single space, so a
// statement broken across lines and indented reads the same as a one-liner.
func normalizeSource(source string) string {
	return strings.Join(strings.Fields(source), " ")
}

// blankComments returns a copy of a file's bytes with every Go comment
// replaced by spaces. Comments are the cheapest place to hide from a text
// matcher: a door that resolves a freeze can carry the guard's name in a
// `// this one does not need assertNoBlockingDeadEventForFreezeTx(...)` line
// and read as guarded, which is how a review broke the rule below without
// turning it red. No rule in this file is allowed to see comments.
//
// SQL inside string literals is untouched -- the parser does not treat `--`
// inside a raw string as a Go comment -- so a query's own commentary is still
// part of the query text, which is what the SQL rules want.
func blankComments(body []byte, parsed *ast.File, fset *token.FileSet) []byte {
	out := make([]byte, len(body))
	copy(out, body)
	for _, group := range parsed.Comments {
		for _, comment := range group.List {
			start := fset.Position(comment.Pos()).Offset
			end := fset.Position(comment.End()).Offset
			if start < 0 || end > len(out) || start >= end {
				continue
			}
			for i := start; i < end; i++ {
				if out[i] != '\n' {
					out[i] = ' '
				}
			}
		}
	}
	return out
}

// declCallsFunction reports whether a declaration actually calls name, by
// walking its syntax tree rather than searching its source text. The text
// search this replaces (`strings.Contains(decl.normalized, name+"(")`) counted
// a mention inside a comment or inside a SQL string as a call, so one line of
// prose was enough to make an unguarded door read as guarded, and a mention in
// a query comment was enough to keep a caller-count vacuity check satisfied
// after the last real caller had gone.
func declCallsFunction(decl goDecl, name string) bool {
	if decl.node == nil {
		return false
	}
	called := false
	ast.Inspect(decl.node, func(node ast.Node) bool {
		if called {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if fun.Name == name {
				called = true
			}
		case *ast.SelectorExpr:
			// A caller in another package spells it
			// postgresstore.name; the selector is what matters.
			if fun.Sel.Name == name {
				called = true
			}
		}
		return !called
	})
	return called
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
		// Every rule below matches against code only: comments are blanked
		// here, once, so that no rule has to remember to ignore them.
		code := blankComments(body, parsed, fset)
		for _, decl := range parsed.Decls {
			start := fset.Position(decl.Pos()).Offset
			end := fset.Position(decl.End()).Offset
			if start < 0 || end > len(code) || start >= end {
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
				normalized: normalizeSource(string(code[start:end])),
				node:       decl,
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

// deadStatusTestSource matches "this row's processing_status is dead" without
// depending on how it is spelled: either side of the comparison may come
// first, the column may carry a cast, and the value may arrive through `=`,
// `IN (...)` or `= ANY (ARRAY[...])`. The previous version of the rule below
// was locked to `processing_status =/IN 'dead'` with the column on the left,
// and a review walked through it with a `::text` cast, a reversed comparison
// and an `ANY(ARRAY['dead'])`.
const deadStatusTestSource = `(?:` +
	`(?:\w+\.)?\bprocessing_status(?:::\w+)? ?= ?(?:ANY ?)?\(? ?(?:ARRAY ?\[ ?)?'dead'` +
	`|(?:\w+\.)?\bprocessing_status(?:::\w+)? ?IN ?\([^)]{0,160}?'dead'` +
	`|'dead' ?= ?(?:\w+\.)?\bprocessing_status(?:::\w+)?` +
	`)`

// handWrittenDeadFilterPattern matches a hand-written aggregate over dead
// events. "Aggregate" is the part that makes this rule narrower than "mentions
// dead anywhere": a plain `WHERE processing_status='dead'` is an ordinary
// row-level predicate that many repair paths legitimately write, while a dead
// count folded into a health surface is the thing that must come from
// sourceDeadEventCountColumnsSQL so that containment is asked about at the
// same time. Both aggregate shapes count: a FILTER clause, and the
// `sum(CASE WHEN ... THEN 1 ELSE 0 END)` spelling of the same total, which the
// FILTER-only version of this rule did not see at all.
var handWrittenDeadFilterPattern = regexp.MustCompile(
	`(?i)(?:FILTER ?\( ?WHERE|CASE WHEN)[^;]{0,240}?` + deadStatusTestSource)

// containmentRenderCall is how a query asks the containment question. The
// correlation rule below is what makes this the only spelling there is: a
// hand-written correlation is an error there, so requiring this literal here
// is not a spelling lock, it is the one spelling that rule permits.
const containmentRenderCall = "sourceEventContainedByOpenFreezeSQL("

// deadAggregateContainmentWindow is how far past a dead aggregate's own text
// the containment call is still counted as belonging to it. The normalised
// declaration text interleaves SQL with Go concatenation, so "in the same
// FILTER" cannot be read off it exactly; this is a deliberately short leash,
// and the direction of the error is the safe one -- a dead aggregate that asks
// containment further away than this is reported, not ignored.
const deadAggregateContainmentWindow = 240

// deadAggregatesWithoutContainment returns each dead-status aggregate in a
// normalised declaration whose own text does not consult containment.
//
// This, not "may only appear in the renderer", is the property the rule is
// actually about. sourceDeadEventCountColumnsSQL is the standard way to get it
// right and covers every health surface, but it renders one specific shape
// (two count columns), and a query that needs a different shape -- the cycle
// publisher counts unfinished events, and excludes contained dead ones from
// that count -- is not made safer by being forced through it. What must never
// happen is an aggregate that judges an event dead without asking whether the
// dead event is contained.
func deadAggregatesWithoutContainment(normalized string) []string {
	var bare []string
	for _, span := range handWrittenDeadFilterPattern.FindAllStringIndex(normalized, -1) {
		end := span[1] + deadAggregateContainmentWindow
		if end > len(normalized) {
			end = len(normalized)
		}
		if strings.Contains(normalized[span[0]:end], containmentRenderCall) {
			continue
		}
		bare = append(bare, normalized[span[0]:span[1]])
	}
	return bare
}

// freezeCorrelationPattern matches the containment correlation itself: an
// eligibility freeze's source_revision_hash equated to an ingest row's
// payload_hash, in either order and under any alias. This is the pairing that
// was retyped three times in the first cut of this slice.
var freezeCorrelationPattern = regexp.MustCompile(
	`(?:\w+\.)?source_revision_hash ?= ?(?:\w+\.)?payload_hash|(?:\w+\.)?payload_hash ?= ?(?:\w+\.)?source_revision_hash`)

// freezeResolutionPattern matches a write that resolves an eligibility freeze.
// Every part of it below the table name is a spelling that a review actually
// used to walk a sixth, unguarded door past the previous version of this rule:
//
//   - (?i): `update eligibility_freezes set status='resolved'`. SQL keywords
//     are case-insensitive and this package is not consistent about them.
//   - `(?:AS )?`: `UPDATE eligibility_freezes AS ef SET ...`. The old
//     `(?: \w+)?` ate exactly one word, so an explicit AS slipped through.
//   - `(?:[^;]{0,160}?, ?)?`: `SET resolved_at=now(), status='resolved'`.
//     status is not required to be the first assignment after SET; [^;] keeps
//     the run inside one statement.
//   - ` ?= ?`: spaces around the equals sign.
//
// \b in front of status is what keeps `prior_status='resolved'` from counting
// as a resolution -- an underscore is a word character, so there is no
// boundary there, while `ef.status` has one.
var freezeResolutionPattern = regexp.MustCompile(
	`(?i)UPDATE eligibility_freezes(?: (?:AS )?\w+)? SET (?:[^;]{0,160}?, ?)?\bstatus ?= ?'resolved'`)

// parseDeclForSelfTest parses a single top-level declaration out of a source
// snippet and builds the same goDecl the tree scan would build for it, so the
// self-tests below exercise the real comment blanking and the real AST walk
// rather than a simplified stand-in.
func parseDeclForSelfTest(t *testing.T, source string) goDecl {
	t.Helper()
	fset := token.NewFileSet()
	body := []byte("package p\n" + source)
	parsed, err := parser.ParseFile(fset, "selftest.go", body, parser.ParseComments)
	if err != nil {
		t.Fatalf("self-test snippet does not parse: %v\n%s", err, source)
	}
	if len(parsed.Decls) != 1 {
		t.Fatalf("self-test snippet must hold exactly one declaration, found %d", len(parsed.Decls))
	}
	decl := parsed.Decls[0]
	code := blankComments(body, parsed, fset)
	start := fset.Position(decl.Pos()).Offset
	end := fset.Position(decl.End()).Offset
	return goDecl{
		file:       "selftest.go",
		name:       "selftest",
		normalized: normalizeSource(string(code[start:end])),
		node:       decl,
	}
}

// TestDeclCallsFunctionIgnoresCommentsAndStrings is the stake under the third
// fix in this file. The two rules below decide "is this declaration guarded"
// and "does anything still call the definition"; both used to decide it by
// searching the declaration's source text for `name(`, and a review walked an
// unguarded door past that check with a single line of prose. Text search
// cannot tell a call from a mention, so the rules ask the syntax tree -- and
// this test is what says the tree is actually being asked.
func TestDeclCallsFunctionIgnoresCommentsAndStrings(t *testing.T) {
	const target = "assertNoBlockingDeadEventForFreezeTx"
	for name, source := range map[string]string{
		"line comment": "func door() {\n" +
			"\t// This door does not need " + target + "(ctx, tx, id, src)\n" +
			"\t// because the caller already checked.\n" +
			"\t_ = resolve()\n}",
		"block comment":                             "func door() {\n\t/* calls " + target + "(ctx, tx, id, src) elsewhere */\n\t_ = resolve()\n}",
		"doc comment on a nested func":              "func door() {\n\tinner := func() {\n\t\t// " + target + "(ctx, tx, id, src)\n\t}\n\t_ = inner\n}",
		"raw string (a SQL comment inside a query)": "func door() {\n\t_ = `SELECT 1 -- guarded by " + target + "(...)`\n}",
		"interpreted string":                        "func door() {\n\t_ = \"" + target + "(ctx, tx, id, src)\"\n}",
		"an identifier that merely mentions it":     "func door() {\n\t_ = " + target + "Note\n}",
	} {
		decl := parseDeclForSelfTest(t, source)
		if declCallsFunction(decl, target) {
			t.Errorf("declCallsFunction counts a %s as a call to %s, so a door can read as "+
				"guarded by writing one line about the guard:\n%s", name, target, source)
		}
	}
	// Positive controls: the shapes a real call takes. Without these the
	// function above could return false unconditionally and every rule that
	// uses it would report a clean tree.
	for name, source := range map[string]string{
		"plain call":                    "func door() {\n\t_ = " + target + "(ctx, tx, id, src)\n}",
		"call in an if":                 "func door() {\n\tif err := " + target + "(ctx, tx, id, src); err != nil {\n\t\treturn\n\t}\n}",
		"qualified call":                "func door() {\n\t_ = postgresstore." + target + "(ctx, tx, id, src)\n}",
		"call in a nested closure":      "func door() {\n\tinner := func() {\n\t\t_ = " + target + "(ctx, tx, id, src)\n\t}\n\t_ = inner\n}",
		"call inside a var declaration": "var door = " + target + "(ctx, tx, id, src)",
	} {
		decl := parseDeclForSelfTest(t, source)
		if !declCallsFunction(decl, target) {
			t.Errorf("declCallsFunction misses a %s of %s, so every declaration would look "+
				"unguarded and the rule would be turned off:\n%s", name, target, source)
		}
	}
	// The blanking must not eat code: a declaration that both mentions the
	// guard in a comment and calls it is guarded.
	both := parseDeclForSelfTest(t, "func door() {\n\t// see "+target+"\n\t_ = "+target+"(ctx, tx, id, src)\n}")
	if !declCallsFunction(both, target) {
		t.Error("declCallsFunction misses a real call that sits next to a comment about it")
	}
	// And the blanking must reach the text the SQL rules match on.
	commented := parseDeclForSelfTest(t, "func door() {\n\t// UPDATE eligibility_freezes SET status='resolved'\n\t_ = 1\n}")
	if freezeResolutionPattern.MatchString(commented.normalized) {
		t.Error("a freeze resolution written in a Go comment counts as a door, which inflates " +
			"the door floor and lets the matcher rot behind it")
	}
}

// TestEveryDeadEventAggregateConsultsContainment holds the rule that an
// aggregate over dead events may not be written without asking, in the same
// expression, whether those dead events are contained. Rendering it from
// sourceDeadEventCountColumnsSQL is how every health surface satisfies this
// and is what a fifth surface should do; the cycle publisher, which needs a
// different column shape, satisfies it by calling the containment renderer
// inline instead.
//
// This is a wider net than the first cut of the rule, which asked only whether
// a dead FILTER appeared outside sourceDeadEventCountColumnsSQL. That question
// was answered "no" partly by accident: the publisher's aggregate spells its
// dead test as IN ('failed','dead'), which the old matcher did not see at all.
// Widening the matcher to see it and then narrowing the rule to what actually
// matters leaves the publisher passing for a reason instead of by oversight.
func TestEveryDeadEventAggregateConsultsContainment(t *testing.T) {
	// sourceDeadEventCountColumnsSQL is the renderer the health surfaces use.
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
		// The four a review walked through the previous version of this rule.
		`count(*) FILTER (WHERE sie.processing_status IN ('failed','dead'))`,
		`count(*) FILTER (WHERE sie.processing_status::text='dead')`,
		`count(*) FILTER (WHERE 'dead'=sie.processing_status)`,
		`count(*) FILTER (WHERE sie.processing_status = ANY(ARRAY['dead']))`,
		`sum(CASE WHEN sie.processing_status='dead' THEN 1 ELSE 0 END)`,
		// ... and the lower-case spelling of the same, since SQL keywords are
		// case-insensitive and this package writes them both ways.
		`count(*) filter (where sie.processing_status='dead')`,
	} {
		if !handWrittenDeadFilterPattern.MatchString(normalizeSource(planted)) {
			t.Fatalf("the dead-FILTER matcher does not catch a spelling a maintainer would write, "+
				"so this guard proves nothing:\n\t%s", planted)
		}
		if len(deadAggregatesWithoutContainment(normalizeSource(planted))) != 1 {
			t.Fatalf("a dead aggregate that never asks containment is not reported, so the rule "+
				"below would pass over it:\n\t%s", planted)
		}
	}
	// The containment allowance and its leash. Both directions have to be
	// staked: an allowance nobody can fail is an exemption list with extra
	// steps, and one nobody can satisfy gets deleted the first time it fires.
	withContainment := normalizeSource(
		`count(*) FILTER (WHERE sie.processing_status='dead' AND ` + containmentRenderCall + `"sie") )`)
	if len(deadAggregatesWithoutContainment(withContainment)) != 0 {
		t.Fatal("a dead aggregate that asks containment in the same FILTER is reported anyway, " +
			"which leaves no way to write one that is not the two-column renderer")
	}
	farAway := normalizeSource(`count(*) FILTER (WHERE sie.processing_status='dead')` +
		strings.Repeat(" AND sie.stream_id<>'x'", 20) + containmentRenderCall + `"sie")`)
	if len(deadAggregatesWithoutContainment(farAway)) != 1 {
		t.Fatal("a containment call far away from the dead aggregate still excuses it, so any " +
			"declaration that asks containment once is excused everywhere in its body")
	}
	for _, allowed := range []string{
		`count(*) FILTER (WHERE processing_status='queued')`,
		// A row-level predicate is not a health aggregate. Several repair
		// paths legitimately select the dead rows they are about to act on,
		// and a rule that fired on those would be turned off within a week.
		`WHERE sie.processing_status='dead' AND sie.source_instance_id=$1`,
		// Counting what is *not* dead says nothing about containment.
		`count(*) FILTER (WHERE sie.processing_status <> 'dead')`,
		// A different column that merely ends in the same word.
		`count(*) FILTER (WHERE sie.prior_processing_status_note='deadline')`,
	} {
		if handWrittenDeadFilterPattern.MatchString(normalizeSource(allowed)) {
			t.Fatalf("the dead-FILTER matcher fires on something that is not an aggregate "+
				"dead FILTER, so it will be weakened to shut it up:\n\t%s", allowed)
		}
	}

	found := false
	for _, decl := range scanBackendDecls(t) {
		if decl.name == renderer && handWrittenDeadFilterPattern.MatchString(decl.normalized) {
			found = true
		}
		for _, bare := range deadAggregatesWithoutContainment(decl.normalized) {
			t.Errorf("%s: %s aggregates dead events without asking containment in the same "+
				"expression. Render it from sourceDeadEventCountColumnsSQL, or ask "+
				"%s inline the way the cycle publisher does -- a dead count that cannot see "+
				"containment reports a contained, single-account outage as a source-wide one:\n\t%s",
				decl.file, decl.name, containmentRenderCall, bare)
		}
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
		// A call, not a mention: this counts declarations whose syntax tree
		// contains the call. The text version of this test counted the
		// definition's name where it appears in a SQL comment inside
		// consumption.go's query, which would have kept the floor below
		// satisfied after the last real caller had gone.
		if declCallsFunction(decl, definition) {
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
		// The four spellings a review used to walk an unguarded sixth door
		// straight through the previous version of this matcher. Each one is
		// something a maintainer would write without a second thought.
		`UPDATE eligibility_freezes SET status = 'resolved'`,
		`UPDATE eligibility_freezes AS zz SET status='resolved'`,
		`UPDATE eligibility_freezes SET resolved_at=now(), status='resolved'`,
		`update eligibility_freezes set status='resolved'`,
		// Both at once, plus an alias, which is what the combination looks
		// like in practice.
		`update eligibility_freezes as zz set resolved_at = now(), status = 'resolved'`,
	} {
		if !freezeResolutionPattern.MatchString(normalizeSource(planted)) {
			t.Fatalf("the freeze-resolution matcher misses a spelling a maintainer would write, "+
				"so this guard proves nothing:\n\t%s", planted)
		}
	}
	for _, allowed := range []string{
		`UPDATE eligibility_freezes SET status='open'`,
		// Recording what the status used to be is not resolving anything.
		// \b in the matcher is what draws this line.
		`UPDATE eligibility_freezes SET prior_status='resolved'`,
		// A different table whose name starts with this one's.
		`UPDATE eligibility_freezes_archive SET status='resolved'`,
	} {
		if freezeResolutionPattern.MatchString(normalizeSource(allowed)) {
			t.Fatalf("the freeze-resolution matcher fires on a write that does not resolve a "+
				"freeze, so it will be weakened to shut it up:\n\t%s", allowed)
		}
	}

	doors := 0
	for _, decl := range scanBackendDecls(t) {
		if !freezeResolutionPattern.MatchString(decl.normalized) {
			continue
		}
		doors++
		// Asking the syntax tree, not the text. A door whose only mention of
		// the guard is `// this one does not need
		// assertNoBlockingDeadEventForFreezeTx(...)` used to pass here.
		if !declCallsFunction(decl, guard) {
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
