package postgresstore

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
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

// backendRoot is the backend/ directory every tree rule in this file scans.
func backendRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// scanBackendDecls parses every non-test Go file under backend/ -- not just
// this package -- and returns one entry per top-level declaration. Scanning
// the whole tree matters for the resolution-guard rule below: a freeze
// resolution written in another package could not call this package's
// unexported guard at all, and this rule is what would say so.
func scanBackendDecls(t *testing.T) []goDecl {
	t.Helper()
	root := backendRoot(t)
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

// containmentRenderers are the three functions that render containment-aware
// SQL. A dead-status test that arrives through one of them has asked the
// containment question by construction, because that is what they render.
var containmentRenderers = []string{
	"sourceEventContainedByOpenFreezeSQL",
	"sourceDeadEventCountColumnsSQL",
	"sourceDeadEventFlagColumnsSQL",
}

// mentionsDeadIngestStatus reports whether a query contains a dead
// processing_status judgment.
//
// This is the whole test, and it is deliberately not a pattern over how the
// judgment is spelled. Three rounds of review walked through three successive
// spelling-locked matchers: first `='dead'` on its own line, then a FILTER
// with the column on the left, then an aggregate wrapper written as `FILTER (`
// or `CASE WHEN`. Each fix taught the matcher one more spelling and left the
// next one open -- a correlated scalar subquery, `sum((... ='dead')::int)`,
// a `::text` cast, `'dead' = ANY(ARRAY[...])`. The escapes were never running
// out, because "which SQL shapes aggregate a status" is an open set.
//
// So the property is inverted. Any SQL that mentions both the column and the
// value is a dead-status judgment, whatever it does with it, and it has to
// come from a renderer, share a concatenation with one, or be on the
// exemption list below with a written reason. There is no spelling that
// escapes this, because there is no spelling in it.
//
// The argument is a whole concatenation chain, not one literal. Asking it of a
// single literal was the next escape a review found, and it had three
// spellings of its own: pull `'dead'` out into a package constant and
// concatenate it, break the column name across two literals in the middle of
// the token, or keep the table name in some other declaration's constant. All
// three leave every individual literal innocent. joinSQLChain resolves and
// joins first, so none of them changes what this function sees.
//
// 'dead' carries its closing quote, so `'deadline'` is not a match, and the
// column name is required, so a `status='dead'` on some other table is not
// one either.
//
// # Known boundary, not covered and not pretended to be
//
// A query that takes its status set as a bind parameter --
// `processing_status = ANY($2)` with 'dead' passed from Go at call time -- is
// invisible to this and to any other rule that reads source text, because the
// value is not in the source. No amount of tightening here reaches it. The
// same is true of two subtler shapes: a rendered fragment that lands inside a
// SQL `--` comment, and one short-circuited by `(true OR ...)`. Both would
// satisfy every rule in this file while doing nothing at runtime; a static
// rule can prove a fragment is present, never that it has effect. These are
// written down rather than papered over, because a rule that claims coverage
// it does not have is worse than one whose edges are known.
func mentionsDeadIngestStatus(sql string) bool {
	return strings.Contains(sql, "processing_status") && strings.Contains(sql, "'dead'")
}

// nodeParents maps every node under root to its parent, which go/ast does not
// record. Walking upwards is what turns "this literal" into "the expression
// this literal is part of".
func nodeParents(root ast.Node) map[ast.Node]ast.Node {
	parents := make(map[ast.Node]ast.Node, 256)
	var stack []ast.Node
	ast.Inspect(root, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) > 0 {
			parents[node] = stack[len(stack)-1]
		}
		stack = append(stack, node)
		return true
	})
	return parents
}

// concatChainRoot walks up from a node through enclosing `+` expressions and
// parentheses to the outermost one: the expression that builds the whole
// query string this literal is a fragment of.
func concatChainRoot(node ast.Node, parents map[ast.Node]ast.Node) ast.Node {
	for {
		parent, ok := parents[node]
		if !ok {
			return node
		}
		switch typed := parent.(type) {
		case *ast.ParenExpr:
		case *ast.BinaryExpr:
			if typed.Op != token.ADD {
				return node
			}
		default:
			return node
		}
		node = parent
	}
}

// concatChainLeaves flattens a `a + b + c` tree into its operands.
func concatChainLeaves(expr ast.Expr) []ast.Expr {
	switch typed := expr.(type) {
	case *ast.ParenExpr:
		return concatChainLeaves(typed.X)
	case *ast.BinaryExpr:
		if typed.Op == token.ADD {
			return append(concatChainLeaves(typed.X), concatChainLeaves(typed.Y)...)
		}
	}
	return []ast.Expr{expr}
}

// chainCallsAnyOf reports whether the concatenation that node belongs to has
// an operand that is a call to one of names.
//
// This replaces a character window over the declaration's normalised text.
// The window had two ways out that cost one line each: write the renderer's
// name into a SQL comment inside the raw string, or put an unrelated query
// that happens to call the renderer within the window. Neither is a call, and
// neither survives being asked of the syntax tree. It also removes the
// arbitrary constant -- "the same concatenated string" is a real boundary,
// where "240 characters" was a guess that had to be defended.
func chainCallsAnyOf(node ast.Node, parents map[ast.Node]ast.Node, names []string) bool {
	root, ok := concatChainRoot(node, parents).(ast.Expr)
	if !ok {
		return false
	}
	for _, leaf := range concatChainLeaves(root) {
		call, isCall := leaf.(*ast.CallExpr)
		if !isCall {
			continue
		}
		var called string
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			called = fun.Name
		case *ast.SelectorExpr:
			called = fun.Sel.Name
		}
		for _, name := range names {
			if called == name {
				return true
			}
		}
	}
	return false
}

// sqlChain is one query as the compiler will see it: every string fragment on
// one `+` concatenation, joined in source order, together with the expression
// that builds it.
type sqlChain struct {
	root ast.Node
	text string
}

// joinSQLChain returns the text a concatenation produces, as far as it can be
// known statically: string literals contribute their value, identifiers that
// name a string constant in the same package contribute theirs, and anything
// else (a call, a variable, a parameter) contributes nothing.
//
// Fragments are joined with no separator, on purpose. `"processing_st" +
// "atus='dead'"` is one column name to the compiler and has to be one here
// too; inserting anything between fragments would re-open exactly the escape
// this closes.
func joinSQLChain(root ast.Expr, constants map[string]string) string {
	var text strings.Builder
	for _, leaf := range concatChainLeaves(root) {
		switch typed := leaf.(type) {
		case *ast.BasicLit:
			if typed.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(typed.Value)
			if err != nil {
				value = typed.Value
			}
			text.WriteString(value)
		case *ast.Ident:
			if value, known := constants[typed.Name]; known {
				text.WriteString(value)
			}
		}
	}
	return text.String()
}

// declSQLChains returns one entry per concatenated query in a declaration.
//
// Every SQL rule in this file runs over these rather than over individual
// literals or over the declaration's raw text, and that is what makes the
// rules independent of where a maintainer chose to put the `+` signs. A review
// escaped the per-literal version three ways at once -- a constant holding
// 'dead', a column name split mid-token, a table name kept in another
// declaration -- and none of the three survives being asked of the joined
// chain with constants resolved.
func declSQLChains(decl goDecl, constants map[string]string) []sqlChain {
	if decl.node == nil {
		return nil
	}
	parents := nodeParents(decl.node)
	seen := map[ast.Node]bool{}
	var chains []sqlChain
	ast.Inspect(decl.node, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.BasicLit:
			if typed.Kind != token.STRING {
				return true
			}
		case *ast.Ident:
			if _, known := constants[typed.Name]; !known {
				return true
			}
		default:
			return true
		}
		root, ok := concatChainRoot(node, parents).(ast.Expr)
		if !ok || seen[root] {
			return true
		}
		seen[root] = true
		chains = append(chains, sqlChain{root: root, text: joinSQLChain(root, constants)})
		return true
	})
	return chains
}

// declPackageConstants returns the string constants visible to a declaration,
// which is what lets an identifier on a concatenation be resolved to the SQL
// it stands for.
func declPackageConstants(decl goDecl, byPackage map[string]map[string]string) map[string]string {
	if constants := byPackage[path.Dir(decl.file)]; constants != nil {
		return constants
	}
	return map[string]string{}
}

// deadStatusExemption is one declaration that writes a dead processing_status
// test by hand and is allowed to. Every field is load-bearing:
//
//   - file and decl pin it to one place, so an exemption cannot drift onto
//     some other declaration that happens to be renamed into the same slot.
//   - literals is how many such SQL strings that declaration holds. The scan
//     asserts the count exactly, so adding a dead-status query to an already
//     exempt declaration turns this red instead of inheriting its exemption.
//   - reason has to say why the site is row-level, because that is the whole
//     basis of the exemption.
//
// The list may only shrink. A stale entry -- one whose declaration no longer
// writes a dead-status test, or writes a different number of them -- fails
// the test, so an exemption cannot outlive the thing it excuses. That is the
// rule the list would otherwise quietly break: an exemption list that keeps
// entries after the code changed is a second, hand-written scope list, and a
// hand-written scope list is a mirror.
type deadStatusExemption struct {
	file     string
	decl     string
	literals int
	reason   string
}

// deadStatusExemptions is the complete set of hand-written dead-status SQL in
// the tree that does not go through a renderer. Every one of them acts on
// named rows -- a specific (source_instance_id, stream_id, event_id), or the
// row a lease token already claimed -- rather than summarising a source's
// health, so there is no aggregate for containment to change the meaning of.
// A repair tool that requeues one named dead event is not answering "is this
// source healthy"; it is doing what an operator asked, to a row they named.
var deadStatusExemptions = []deadStatusExemption{
	{
		file: "internal/postgresstore/eligibility_repair.go", decl: "RepairPreAnchorUsageEligibility", literals: 2,
		reason: "selects, then requeues, dead/failed usage_event and credit_event rows by " +
			"(source_instance_id, stream_id, event_id) under FOR UPDATE. Row-level repair, no aggregate.",
	},
	{
		file: "internal/postgresstore/ingest_requeue_dead_repair.go", decl: "repairIngestRequeueDeadEvent", literals: 2,
		reason: "reads and requeues exactly one named dead event. The candidate list that decides " +
			"which events are offered does go through the renderer (ingestRequeueDeadCandidates).",
	},
	{
		file: "internal/postgresstore/ingest_unreplayable_acknowledge.go", decl: "AcknowledgeUnreplayableIngestEvent", literals: 2,
		reason: "reads and closes exactly one named dead event as unreplayable. Same shape as the " +
			"requeue repair and the same reasoning.",
	},
	{
		file: "internal/postgresstore/source_sync.go", decl: "MarkSourceEventFailed", literals: 1,
		reason: "this is the write that *produces* dead status, on the row its own lease token holds. " +
			"It is upstream of containment rather than a reader of it.",
	},
}

// sqlColumnRefSource matches a column reference with an optional table alias
// and an optional cast, so that `ef.source_revision_hash::text` reads the same
// as `source_revision_hash`.
const sqlColumnRefSource = `(?:\w+\.)?\b%s(?:::\w+)?`

// sqlEqualityOperatorSource matches the ways two columns are compared for
// equality in this codebase's SQL. `IS NOT DISTINCT FROM` is the NULL-safe
// spelling of `=`; a review used it to write the containment correlation by
// hand without tripping the rule below. `IS DISTINCT FROM` is included because
// the negation of the correlation is still the correlation being retyped.
const sqlEqualityOperatorSource = `(?: ?= ?| IS (?:NOT )?DISTINCT FROM )`

// freezeCorrelationPattern matches the containment correlation itself: an
// eligibility freeze's source_revision_hash compared to an ingest row's
// payload_hash, in either order, under any alias, through either equality
// spelling, with or without casts. This is the pairing that was retyped three
// times in the first cut of this slice.
var freezeCorrelationPattern = regexp.MustCompile(
	`(?i)(?:` +
		fmt.Sprintf(sqlColumnRefSource, "source_revision_hash") + sqlEqualityOperatorSource +
		fmt.Sprintf(sqlColumnRefSource, "payload_hash") +
		`|` +
		fmt.Sprintf(sqlColumnRefSource, "payload_hash") + sqlEqualityOperatorSource +
		fmt.Sprintf(sqlColumnRefSource, "source_revision_hash") +
		`)`)

// freezeStatusWritePattern matches any write that assigns eligibility_freezes'
// status column, and captures what it assigns.
//
// The rule used to look for the assigned value `'resolved'` directly, and each
// review round found another way to assign a resolution without writing that
// literal next to that column:
//
//   - `SET resolved_at=now(), status=$2`. The value is a bind parameter, so
//     no literal appears in the SQL at all. This is the one that matters most:
//     a parameterised resolution is the most likely way a new door gets
//     written, and it was completely invisible.
//   - `SET a=..., b=..., c=..., status='resolved'` with more than 160
//     characters of other assignments first, which overran the old bounded
//     run.
//
// So the match no longer asks what is being assigned. It asks whether the
// status column of this table is being written at all, and the decision about
// safety is made afterwards, on the captured value: only the literal 'open' is
// safe, because re-opening a freeze cannot un-contain a dead event. Everything
// else -- 'resolved', a bind parameter, a CASE expression, a function call --
// is treated as a resolution and has to pass the guard. A door that writes
// something genuinely harmless in a shape this cannot read gets a red it can
// answer by calling the guard, which is the safe direction to be wrong in.
//
// `[^;]*?` is bounded by the statement separator rather than by a character
// count, so the length of the SET list no longer decides whether the rule can
// see the assignment.
//
// \b in front of status is what keeps `prior_status=...` from counting -- an
// underscore is a word character, so there is no boundary there, while
// `ef.status` has one.
// It also no longer requires the words `UPDATE eligibility_freezes` to sit
// next to each other. A review wrote a sixth door as
//
//	INSERT INTO eligibility_freezes (...) VALUES (...)
//	ON CONFLICT (id) DO UPDATE SET status='resolved'
//
// which resolves a freeze without that phrase ever appearing -- and this is
// not a contrived shape, it is how consumption.go already writes to this
// table twice (today only touching updated_at). So the table and the
// assignment are matched separately: freezeTableName has to appear somewhere
// in the query, and this pattern finds the assignment wherever the SET is.
// `UPDATE ... SET` and `DO UPDATE SET` are then the same thing, which is what
// they are.
//
// SET is still required, because it is what separates writing the column from
// reading it -- `WHERE ef.status='resolved'` is a lookup and must not count.
//
// Known boundary: `SET "status"=` with a quoted identifier would not match.
// Nothing in this repository quotes identifiers, and adding the quoted form to
// the pattern would be one more spelling in a list, which is the shape this
// file keeps having to undo.
var freezeStatusWritePattern = regexp.MustCompile(
	`(?i)\bSET (?:[^;]*?, ?)?\bstatus ?= ?([^,;]+)`)

// freezeTableName is the table whose status column matters. It is looked for
// in the joined concatenation rather than in one literal, so splitting it
// across two fragments or keeping it in another declaration's constant does
// not hide it.
const freezeTableName = "eligibility_freezes"

// freezeTablePattern matches that table and not a longer name that starts with
// it. The word boundary is what keeps `eligibility_freezes_archive` out: an
// underscore is a word character, so there is no boundary inside that name.
// A plain substring test put the archive table's own status writes under this
// rule, which the negative control below caught the moment the table name
// stopped being anchored to the word UPDATE.
var freezeTablePattern = regexp.MustCompile(`\b` + freezeTableName + `\b`)

// freezeStatusReopenValue is the only assigned value that does not need the
// dead-event guard. It is matched as a whole captured token so that a CASE
// expression which merely mentions 'open' somewhere is not mistaken for one.
const freezeStatusReopenValue = `'open'`

// sqlWriteTargetPattern finds what a statement writes to. `DO UPDATE` is
// matched as a unit and before the bare `UPDATE` alternative, because in an
// upsert the target is the INSERT's table and there is no name after the verb.
var sqlWriteTargetPattern = regexp.MustCompile(`(?i)\b(DO UPDATE|UPDATE(?: ONLY)? (\w+)|INSERT INTO (\w+))`)

// freezeStatusWriteTarget reads which table a status assignment belongs to,
// given everything in the query before it.
//
// Decoupling the table from the verb -- which is what makes the upsert
// spelling visible -- costs the ability to tell whose status is being written
// when one query names two tables. This gets it back without going back to
// matching a phrase: the target is whatever the nearest preceding write verb
// names, and for `DO UPDATE` that is the INSERT's table.
//
// An unreadable prefix returns "", and the caller treats that as the freeze
// table. Demanding the guard for a write this cannot attribute is the safe
// direction to be wrong in.
func freezeStatusWriteTarget(prefix string) string {
	matches := sqlWriteTargetPattern.FindAllStringSubmatch(prefix, -1)
	if len(matches) == 0 {
		return ""
	}
	last := matches[len(matches)-1]
	if strings.EqualFold(last[1], "DO UPDATE") {
		for index := len(matches) - 1; index >= 0; index-- {
			if matches[index][3] != "" {
				return matches[index][3]
			}
		}
		return ""
	}
	if last[2] != "" {
		return last[2]
	}
	return last[3]
}

// freezeResolutionsIn returns the assigned values, one per status write to the
// freeze table in the query, that are not the safe re-open literal. A query
// that does not name the table at all writes nothing here by definition.
func freezeResolutionsIn(sql string) []string {
	if !freezeTablePattern.MatchString(sql) {
		return nil
	}
	var resolutions []string
	for _, span := range freezeStatusWritePattern.FindAllStringSubmatchIndex(sql, -1) {
		if target := freezeStatusWriteTarget(sql[:span[0]]); target != "" &&
			!strings.EqualFold(target, freezeTableName) {
			continue
		}
		assigned := strings.TrimSpace(sql[span[2]:span[3]])
		if index := strings.IndexAny(assigned, " \t"); index >= 0 {
			assigned = assigned[:index]
		}
		if assigned == freezeStatusReopenValue {
			continue
		}
		resolutions = append(resolutions, assigned)
	}
	return resolutions
}

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
	if len(freezeResolutionsIn(commented.normalized)) != 0 {
		t.Error("a freeze resolution written in a Go comment counts as a door, which inflates " +
			"the door floor and lets the matcher rot behind it")
	}
}

// TestSQLChainJoiningDefeatsFragmentation is the stake under the fix that made
// every SQL rule read a whole concatenation instead of one literal at a time.
//
// Per-literal was the next escape after the shape lists ran out, and it had
// three spellings, all of which leave each individual literal innocent: pull
// the value into a package constant, break the column name across two
// fragments in the middle of the token, or keep the table name in some other
// declaration's constant. This test plants all three and one negative control.
func TestSQLChainJoiningDefeatsFragmentation(t *testing.T) {
	constants := map[string]string{
		"zzDeadLiteral": "'dead'",
		"zzFreezeTable": "eligibility_freezes",
	}
	triggers := func(source string) bool {
		for _, chain := range declSQLChains(parseDeclForSelfTest(t, source), constants) {
			if mentionsDeadIngestStatus(chain.text) {
				return true
			}
		}
		return false
	}
	for name, source := range map[string]string{
		"the value pulled into a package constant": "func q() string {\n\treturn `SELECT count(*) FROM source_ingest_events " +
			"WHERE processing_status=` + zzDeadLiteral\n}",
		"the column name split in the middle of the token": "func q() string {\n\treturn `SELECT count(*) FROM source_ingest_events " +
			"WHERE processing_st` + `atus='dead'`\n}",
		"both at once": "func q() string {\n\treturn `SELECT count(*) WHERE processing_st` + `atus=` + zzDeadLiteral\n}",
		"fragments separated by a call that contributes nothing": "func q(alias string) string {\n\t" +
			"return `WHERE ` + alias + `.processing_status=` + zzDeadLiteral\n}",
	} {
		if !triggers(source) {
			t.Errorf("a dead-status judgment written as %s is invisible to the rule, so the rule can "+
				"be walked through by choosing where to put the `+` signs:\n%s", name, source)
		}
	}
	// The control that keeps the joining honest. Two separate queries in one
	// declaration are two chains, and neither of them judges anything dead; if
	// joining reached across them, every declaration holding a status query and
	// a dead literal anywhere would be reported and the rule would be turned
	// off within a week.
	separate := "func q() (string, string) {\n\treturn `SELECT processing_status FROM source_ingest_events`, " +
		"`SELECT 'dead'::text`\n}"
	if triggers(separate) {
		t.Errorf("joining reached across two separate queries in one declaration, which reports "+
			"sites that judge nothing:\n%s", separate)
	}
	// The freeze table, kept in another declaration's constant. The resolution
	// rule reads the same joined text, so this closes the same hole there.
	viaConstant := parseDeclForSelfTest(t, "func q() string {\n\treturn `UPDATE ` + zzFreezeTable + ` SET status='resolved'`\n}")
	resolves := false
	for _, chain := range declSQLChains(viaConstant, constants) {
		if len(freezeResolutionsIn(normalizeSource(chain.text))) > 0 {
			resolves = true
		}
	}
	if !resolves {
		t.Error("a freeze resolution whose table name comes from a constant is not seen as a " +
			"resolution, so a door can be written by naming the table somewhere else")
	}
}

// TestEveryDeadStatusQueryReachesContainment holds the rule that no SQL in the
// backend may judge an ingest event dead without that judgment either coming
// from a containment renderer, sharing a concatenated query with one, or being
// on the exemption list above with a written reason.
//
// The rule is stated over string literals rather than over shapes because the
// three previous versions of it were stated over shapes, and each one was
// walked through by a review inventing a shape it did not list -- a FILTER
// split over lines, a `::text` cast, `= ANY(ARRAY[...])`, a `CASE WHEN` sum, a
// correlated scalar subquery, `sum((...)::int)`. There is no end to that list.
// There is an end to "does this SQL mention the column and the value", so that
// is the question now.
//
// What it costs: row-level repair paths that legitimately name dead rows now
// need an exemption. That is the trade, and it is the right way round -- the
// exemptions are four declarations, written down, each with a reason and an
// exact literal count, and the list can only shrink.
func TestEveryDeadStatusQueryReachesContainment(t *testing.T) {
	// Self-test on the trigger. Every spelling a review has used to walk
	// through a previous version of this rule has to be recognised, including
	// the two from the latest round that no shape-based matcher saw.
	for _, planted := range []string{
		`count(*) FILTER (WHERE processing_status='dead')`,
		"count(*) FILTER (\n\t\tWHERE sie.processing_status='dead')",
		`count(sie.event_id) FILTER (WHERE sie.processing_status = 'dead')`,
		`count(*) FILTER (WHERE sie.processing_status IN ('dead'))`,
		`count(*) FILTER (WHERE sie.processing_status IN ('failed','dead'))`,
		`count(*) FILTER (WHERE sie.processing_status::text='dead')`,
		`count(*) FILTER (WHERE 'dead'=sie.processing_status)`,
		`count(*) FILTER (WHERE sie.processing_status = ANY(ARRAY['dead']))`,
		`sum(CASE WHEN sie.processing_status='dead' THEN 1 ELSE 0 END)`,
		`count(*) filter (where sie.processing_status='dead')`,
		// The two from the latest review round. Neither is a FILTER and
		// neither is a CASE WHEN, so the shape-based version of this rule was
		// blind to both.
		`(SELECT count(*) FROM source_ingest_events d WHERE d.source_instance_id=s.id AND d.processing_status='dead')`,
		`sum((sie.processing_status='dead')::int)`,
		// And two more that no shape list would have thought of, to make the
		// point that the trigger does not care.
		`count(*) FILTER (WHERE sie.processing_status SIMILAR TO 'dead')`,
		`array_agg(sie.event_id) FILTER (WHERE sie.processing_status='dead')`,
	} {
		if !mentionsDeadIngestStatus(planted) {
			t.Fatalf("the dead-status trigger does not recognise SQL that judges an event dead, "+
				"so this rule proves nothing:\n\t%s", planted)
		}
	}
	// The trigger must still say no to SQL that is not about this judgment,
	// or the exemption list becomes the whole codebase and gets deleted.
	for _, allowed := range []string{
		`count(*) FILTER (WHERE processing_status='queued')`,
		`count(*) FILTER (WHERE sie.prior_processing_status_note='deadline')`,
		`UPDATE eligibility_freezes SET status='dead_letter_pending'`,
		`SELECT status FROM jobs WHERE status='dead'`,
	} {
		if mentionsDeadIngestStatus(allowed) {
			t.Fatalf("the dead-status trigger fires on SQL that does not judge an ingest event "+
				"dead, so it will be weakened to shut it up:\n\t%s", allowed)
		}
	}
	// The renderers must actually produce SQL the trigger recognises. A
	// renderer the trigger cannot see would let every hand-written copy of it
	// through while this whole file still read as coverage.
	for name, rendered := range map[string]string{
		"sourceDeadEventCountColumnsSQL": sourceDeadEventCountColumnsSQL("count(*)", "sie"),
		"sourceDeadEventFlagColumnsSQL":  sourceDeadEventFlagColumnsSQL("sie"),
	} {
		if !mentionsDeadIngestStatus(rendered) {
			t.Fatalf("the dead-status trigger does not recognise what %s produces:\n\t%s", name, rendered)
		}
	}

	// Which exemptions were actually used, so a stale one can be reported.
	used := map[string]int{}
	exemptionKey := func(file, decl string) string { return file + "::" + decl }
	exempt := map[string]deadStatusExemption{}
	for _, exemption := range deadStatusExemptions {
		exempt[exemptionKey(exemption.file, exemption.decl)] = exemption
	}

	constantsByPackage := packageStringConstants(t, backendRoot(t))
	rendered := 0
	for _, decl := range scanBackendDecls(t) {
		for _, chain := range declSQLChains(decl, declPackageConstants(decl, constantsByPackage)) {
			if !mentionsDeadIngestStatus(chain.text) {
				continue
			}
			// chain.root is already the outermost expression, so no parent
			// map is needed to find it again.
			if chainCallsAnyOf(chain.root, nil, containmentRenderers) {
				rendered++
				continue
			}
			key := exemptionKey(decl.file, decl.name)
			if _, ok := exempt[key]; ok {
				used[key]++
				continue
			}
			t.Errorf("%s: %s builds SQL that judges an ingest event dead without reaching "+
				"containment. Render it from sourceDeadEventCountColumnsSQL, concatenate "+
				"sourceEventContainedByOpenFreezeSQL into the same query, or -- if it acts on "+
				"named rows rather than summarising a source -- add it to deadStatusExemptions "+
				"with a reason. A dead judgment that cannot see containment reports a contained, "+
				"single-account outage as a source-wide one:\n\t%s",
				decl.file, decl.name, strings.TrimSpace(normalizeSource(chain.text)))
		}
	}

	// The exemption list may only shrink. An entry whose declaration no longer
	// writes the SQL it excuses, or writes a different number of such queries,
	// is reported here -- otherwise the list turns into a second hand-written
	// scope list that nobody ever prunes, and a hand-written scope list is a
	// mirror.
	for _, exemption := range deadStatusExemptions {
		key := exemptionKey(exemption.file, exemption.decl)
		switch count := used[key]; {
		case count == 0:
			t.Errorf("the exemption for %s no longer excuses anything: that declaration builds no "+
				"un-rendered dead-status SQL any more. Delete the entry -- an exemption that "+
				"outlives its reason is how this list stops being reviewable", key)
		case count != exemption.literals:
			t.Errorf("the exemption for %s covers %d dead-status queries but is written for %d. "+
				"If a query was added, it inherited an exemption written for something else; "+
				"look at it and then update the count. Reason on file: %s",
				key, count, exemption.literals, exemption.reason)
		}
	}

	// Vacuity from the other side. If nothing in the tree reaches containment
	// through a renderer any more, "everything reaches containment" is true of
	// an empty set, and the health surfaces have quietly stopped asking.
	// Six queries today, counted per concatenated query rather than per string
	// literal: the two health-surface renderers build one query each out of two
	// fragments, and the other four are the cycle publisher, the carry-forward
	// wait, the requeue candidate list and the readiness query.
	if rendered < 6 {
		t.Fatalf("only %d dead-status queries reach containment through a renderer; six do today "+
			"(the two column renderers, the cycle publisher, the carry-forward wait, the requeue "+
			"candidate list and the readiness query), so this rule has lost sight of them", rendered)
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
		// The NULL-safe spelling of the same comparison. A review retyped the
		// correlation this way and the rule did not see it -- and this one is
		// not merely an alternative, it is the spelling someone reaches for
		// precisely when they start worrying about the NULL case the renderer
		// already handles with `source_revision_hash IS NOT NULL`.
		`ef.source_revision_hash IS NOT DISTINCT FROM sie.payload_hash`,
		`sie.payload_hash IS NOT DISTINCT FROM ef.source_revision_hash`,
		`ef.source_revision_hash is not distinct from sie.payload_hash`,
		`ef.source_revision_hash IS DISTINCT FROM sie.payload_hash`,
		// Casts on either side or both.
		`ef.source_revision_hash::text=sie.payload_hash`,
		`ef.source_revision_hash=sie.payload_hash::text`,
		`ef.source_revision_hash::text = sie.payload_hash::text`,
		`sie.payload_hash::text IS NOT DISTINCT FROM ef.source_revision_hash::text`,
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
	constantsByPackage := packageStringConstants(t, backendRoot(t))
	for _, decl := range scanBackendDecls(t) {
		// A call, not a mention: this counts declarations whose syntax tree
		// contains the call. The text version of this test counted the
		// definition's name where it appears in a SQL comment inside
		// consumption.go's query, which would have kept the floor below
		// satisfied after the last real caller had gone.
		if declCallsFunction(decl, definition) {
			callers++
		}
		// Per joined query rather than per declaration text: the table name
		// used to be looked for in the declaration's raw source, where
		// `"eligibility_free" + "zes"` does not appear and a name kept in
		// another declaration's constant does not appear either.
		for _, chain := range declSQLChains(decl, declPackageConstants(decl, constantsByPackage)) {
			if !freezeTablePattern.MatchString(chain.text) ||
				!freezeCorrelationPattern.MatchString(normalizeSource(chain.text)) {
				continue
			}
			t.Errorf("%s: %s correlates an eligibility freeze's source_revision_hash to an ingest "+
				"row's payload_hash by hand. Render it from %s(alias, narrowing...) instead -- two "+
				"copies of this predicate disagree silently, and the disagreement is either a "+
				"re-opened source-wide outage or a permanently lost balance fact",
				decl.file, decl.name, definition)
		}
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
		// The two from the latest review round. The first is the important
		// one: nothing about a parameterised resolution says 'resolved'
		// anywhere in the SQL, so every version of this rule that looked for
		// that literal was blind to the most ordinary way to write a new door.
		`UPDATE eligibility_freezes SET resolved_at=now(), status=$2`,
		`UPDATE eligibility_freezes SET status=$1 WHERE id=$2`,
		// A SET list longer than the old 160-character bound, with status at
		// the end of it.
		`UPDATE eligibility_freezes SET resolved_at=now(),resolved_by=$1::uuid,` +
			`resolution_note=$2,resolution_kind=$3,superseded_by=NULL,reopened_at=NULL,` +
			`reopen_reason=NULL,updated_at=now(),revision=revision+1,status='resolved'`,
		// A value this rule cannot read is treated as a resolution, because
		// the safe direction is to demand the guard.
		`UPDATE eligibility_freezes SET status=CASE WHEN $1 THEN 'resolved' ELSE 'open' END`,
		// The upsert. A review wrote a sixth door this way and walked it
		// straight through: the phrase `UPDATE eligibility_freezes` never
		// appears, and this is not a contrived shape -- consumption.go
		// already writes this table with ON CONFLICT twice.
		`INSERT INTO eligibility_freezes (id,status) VALUES ($1,'open') ` +
			`ON CONFLICT (id) DO UPDATE SET status='resolved'`,
		`INSERT INTO eligibility_freezes (id) VALUES ($1) ` +
			`ON CONFLICT (id) DO UPDATE SET resolved_at=now(), status=$2`,
		// The table named before the verb in a CTE, which is the same
		// separation seen from the other side.
		`WITH target AS (SELECT id FROM eligibility_freezes WHERE id=$1) ` +
			`UPDATE eligibility_freezes SET status='resolved' FROM target`,
	} {
		if len(freezeResolutionsIn(normalizeSource(planted))) == 0 {
			t.Fatalf("the freeze-resolution matcher misses a spelling a maintainer would write, "+
				"so this guard proves nothing:\n\t%s", planted)
		}
	}
	for _, allowed := range []string{
		// Re-opening a freeze cannot un-contain a dead event, and it is the
		// only assigned value that is safe. It stays out by being recognised
		// and excluded, not by being unmatched -- which is why it is checked
		// here rather than left to the matcher's blind spots.
		`UPDATE eligibility_freezes SET status='open'`,
		`UPDATE eligibility_freezes SET reopened_at=now(), status = 'open'`,
		`update eligibility_freezes as zz set status='open'`,
		// Recording what the status used to be is not writing the status.
		// \b in the matcher is what draws this line.
		`UPDATE eligibility_freezes SET prior_status='resolved'`,
		// A different table whose name starts with this one's.
		`UPDATE eligibility_freezes_archive SET status='resolved'`,
		// Reading is not writing. SET is what separates the two, and it is
		// the only thing this rule still requires of the verb.
		`SELECT status FROM eligibility_freezes WHERE status='resolved'`,
		`SELECT id FROM eligibility_freezes WHERE status IN ('resolved','open')`,
		// An upsert on this table that does not touch status. Both of
		// consumption.go's existing writes are this shape, and reporting them
		// would be a false alarm on the most ordinary thing in the file.
		`INSERT INTO eligibility_freezes (id) VALUES ($1) ON CONFLICT (id) DO UPDATE SET updated_at=now()`,
		// A status write on a different table, in a query that merely reads
		// this one.
		`UPDATE eligibility_freezes_archive SET status='resolved' WHERE id IN (SELECT id FROM eligibility_freezes)`,
	} {
		if resolutions := freezeResolutionsIn(normalizeSource(allowed)); len(resolutions) != 0 {
			t.Fatalf("the freeze-resolution matcher fires on a write that does not resolve a "+
				"freeze (it read %v), so it will be weakened to shut it up:\n\t%s", resolutions, allowed)
		}
	}

	doors := 0
	constantsByPackage := packageStringConstants(t, backendRoot(t))
	for _, decl := range scanBackendDecls(t) {
		resolves := false
		for _, chain := range declSQLChains(decl, declPackageConstants(decl, constantsByPackage)) {
			if len(freezeResolutionsIn(normalizeSource(chain.text))) > 0 {
				resolves = true
				break
			}
		}
		if !resolves {
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
