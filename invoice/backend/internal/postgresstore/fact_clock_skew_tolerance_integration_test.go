package postgresstore

import (
	"context"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5"
)

// factClockSkewQuerier is satisfied by both *pgxpool.Pool and pgx.Tx, so the
// classification below can be run once against the committed schema and once
// against an uncommitted, rolled-back ALTER TABLE that proves it can fail.
type factClockSkewQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// factClockSkewExemptFactTables lists tables that carry both
// stream_watermark_at and observed_at and deliberately do NOT carry the
// clock-skew CHECK, with the reason. Shrink-only: adding the constraint to one
// of these turns this test red, and the fix is to delete the entry, never to
// keep it. Adding a NEW entry means writing down why a fact table is exempt
// from a rule every other fact table obeys.
var factClockSkewExemptFactTables = map[string]string{
	"balance_carry_forward_proofs": "receiver-derived carry-forward evidence rather than a source-observed fact: " +
		"its observed_at is validated against the cycle and batch by the 0014/0026 trigger, and it never passes " +
		"through validateFactMetadata at all",
}

// TestFactClockSkewToleranceMatchesEveryFactTableCheckConstraint is
// XM-INV-BINDING-SKEW T3b, and the only test that pins the *value* of
// factClockSkewTolerance against anything outside Go. Everything else in the
// slice builds its inputs from the constant and so stays green if the constant
// moves; the three fact tables' DDL CHECKs cannot follow it (a migration is a
// frozen file), so a drift between them is exactly the failure this catches.
//
// Two things it deliberately does not do:
//
//   - It does not compare constraint text against the Go fragment.
//     pg_get_constraintdef normalises `interval '5 minutes'` and
//     `interval '300 seconds'` alike to `'00:05:00'::interval`, so the
//     comparison has to be value-against-value. It gets there by letting the
//     same server deparse a probe constraint built from the rendered fragment,
//     which also makes the test immune to deparse differences between
//     PostgreSQL versions.
//   - It does not hand-list which tables must carry the constraint. The scope
//     is discovered from the catalog (every table with both columns), because
//     a verifier whose scope comes from the same hand-maintained list as the
//     thing it verifies is green by construction.
func TestFactClockSkewToleranceMatchesEveryFactTableCheckConstraint(t *testing.T) {
	store, ctx := integrationStore(t)

	// The reference: PostgreSQL's own rendering of the Go fragment, produced
	// by the server under test.
	if _, err := store.pool.Exec(ctx, `CREATE TABLE xm_fact_clock_skew_probe(
		stream_watermark_at TIMESTAMPTZ NOT NULL,
		observed_at TIMESTAMPTZ NOT NULL,
		CHECK (stream_watermark_at <= observed_at + `+factClockSkewToleranceSQL+`))`); err != nil {
		t.Fatalf("build the reference constraint from %s: %v", factClockSkewToleranceSQL, err)
	}
	t.Cleanup(func() { _, _ = store.pool.Exec(context.Background(), `DROP TABLE IF EXISTS xm_fact_clock_skew_probe`) })
	var reference string
	if err := store.pool.QueryRow(ctx, `SELECT pg_get_constraintdef(con.oid)
		FROM pg_constraint con JOIN pg_class rel ON rel.oid=con.conrelid
		WHERE con.contype='c' AND rel.relname='xm_fact_clock_skew_probe'`).Scan(&reference); err != nil {
		t.Fatal(err)
	}

	// The tolerance the database parses out of the fragment must be the
	// duration Go means by it, independently of how either spells it.
	var renderedSeconds float64
	if err := store.pool.QueryRow(ctx,
		`SELECT EXTRACT(EPOCH FROM `+factClockSkewToleranceSQL+`)::float8`).Scan(&renderedSeconds); err != nil {
		t.Fatal(err)
	}
	if renderedSeconds != factClockSkewTolerance.Seconds() {
		t.Fatalf("PostgreSQL reads %s as %gs but factClockSkewTolerance is %gs",
			factClockSkewToleranceSQL, renderedSeconds, factClockSkewTolerance.Seconds())
	}

	discovered := discoverFactTablesWithObservationWindow(t, ctx, store.pool)
	// The scope guard. A zero-row or shrunken discovery would make every
	// per-table assertion below pass without asserting anything, so the set is
	// pinned: a table renamed or a fact table added forces a deliberate look,
	// which is the point of discovering the scope rather than listing it.
	wantDiscovered := []string{
		"balance_carry_forward_proofs",
		"balance_reconciliation_checkpoints",
		"source_credit_events",
		"source_usage_events",
	}
	if len(discovered) != len(wantDiscovered) {
		t.Fatalf("tables carrying both stream_watermark_at and observed_at: %v, want %v", discovered, wantDiscovered)
	}
	for i, name := range wantDiscovered {
		if discovered[i] != name {
			t.Fatalf("tables carrying both stream_watermark_at and observed_at: %v, want %v", discovered, wantDiscovered)
		}
	}

	matches := countMatchingFactClockSkewChecks(t, ctx, store.pool, discovered, reference)
	visited := 0
	for _, table := range discovered {
		count, present := matches[table]
		if !present {
			t.Fatalf("no CHECK-constraint count came back for %s; the classification query missed a discovered table", table)
		}
		if reason, exempt := factClockSkewExemptFactTables[table]; exempt {
			if count != 0 {
				t.Fatalf("%s now carries the clock-skew CHECK but is still listed as exempt (%q); delete the "+
					"exemption rather than keeping it -- the list only ever shrinks", table, reason)
			}
			continue
		}
		if count != 1 {
			t.Fatalf("%s carries %d CHECK constraints equal to %q, want exactly 1; the fact tables' DDL and "+
				"factClockSkewTolerance (%s) have drifted", table, count, reference, factClockSkewTolerance)
		}
		visited++
	}
	if want := len(discovered) - len(factClockSkewExemptFactTables); visited != want {
		t.Fatalf("checked %d non-exempt fact tables, want %d", visited, want)
	}

	// The exemption clause above is an absence assertion, so it is worth
	// nothing until it has been shown to fire. Add the constraint the
	// exemption says is absent, re-run the same classification against the
	// uncommitted transaction, and roll back.
	assertFactClockSkewExemptionCanFail(t, ctx, store, discovered, reference)
}

// discoverFactTablesWithObservationWindow returns every ordinary table in the
// current schema carrying both stream_watermark_at and observed_at -- the two
// columns the rule relates. Deliberately excludes source_ingest_batches, which
// has its own, different five-minute CHECK (scan_ceiling_at against
// source_captured_at, agent capture time rather than any record's
// observation): it has no observed_at column, so the two-column predicate
// leaves it out by construction rather than by a name in a list.
func discoverFactTablesWithObservationWindow(t *testing.T, ctx context.Context, q factClockSkewQuerier) []string {
	t.Helper()
	rows, err := q.Query(ctx, `
		SELECT rel.relname FROM pg_class rel
		JOIN pg_namespace ns ON ns.oid=rel.relnamespace
		WHERE ns.nspname=current_schema() AND rel.relkind='r'
			AND rel.relname<>'xm_fact_clock_skew_probe'
			AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid=rel.oid
				AND a.attname='stream_watermark_at' AND a.attnum>0 AND NOT a.attisdropped)
			AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid=rel.oid
				AND a.attname='observed_at' AND a.attnum>0 AND NOT a.attisdropped)
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	names := make([]string, 0, 8)
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	return names
}

// countMatchingFactClockSkewChecks counts, per table, the CHECK constraints
// whose deparsed definition equals the reference. Every requested table gets
// an entry (including zero), so a caller cannot mistake "not looked at" for
// "no match".
func countMatchingFactClockSkewChecks(t *testing.T, ctx context.Context, q factClockSkewQuerier,
	tables []string, reference string) map[string]int {
	t.Helper()
	rows, err := q.Query(ctx, `
		SELECT rel.relname,
			count(con.oid) FILTER (WHERE pg_get_constraintdef(con.oid)=$2)
		FROM pg_class rel
		JOIN pg_namespace ns ON ns.oid=rel.relnamespace
		LEFT JOIN pg_constraint con ON con.conrelid=rel.oid AND con.contype='c'
		WHERE ns.nspname=current_schema() AND rel.relname=ANY($1)
		GROUP BY rel.relname`, tables, reference)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	counts := make(map[string]int, len(tables))
	for rows.Next() {
		var name string
		var count int
		if err = rows.Scan(&name, &count); err != nil {
			t.Fatal(err)
		}
		counts[name] = count
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return counts
}

// assertFactClockSkewExemptionCanFail proves the exemption clause is a live
// assertion rather than a permanently true one, by giving each exempt table
// the very constraint the exemption says it does not have and checking that
// the classification notices -- then rolling the whole thing back.
func assertFactClockSkewExemptionCanFail(t *testing.T, ctx context.Context, store *Store,
	discovered []string, reference string) {
	t.Helper()
	if len(factClockSkewExemptFactTables) == 0 {
		return
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	for table := range factClockSkewExemptFactTables {
		if _, err = tx.Exec(ctx, `ALTER TABLE `+pgx.Identifier{table}.Sanitize()+
			` ADD CHECK (stream_watermark_at <= observed_at + `+factClockSkewToleranceSQL+`)`); err != nil {
			t.Fatalf("add the probe constraint to exempt table %s: %v", table, err)
		}
	}
	matches := countMatchingFactClockSkewChecks(t, ctx, tx, discovered, reference)
	for table := range factClockSkewExemptFactTables {
		if matches[table] != 1 {
			t.Fatalf("exempt table %s was given the clock-skew CHECK and the classification still reported %d "+
				"matches: the exemption assertion above cannot fail and proves nothing", table, matches[table])
		}
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatalf("roll the probe constraints back: %v", err)
	}
}
