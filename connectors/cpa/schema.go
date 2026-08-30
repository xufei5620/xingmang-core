package cpa

import (
	"context"
	"database/sql"
	"fmt"
)

// tokenColumns names the four usage_events token-count columns this package
// reads, resolved once per FileClient against the real table via
// resolveTokenColumns.
//
// ⚠️ Unverified (contract §8): the task brief confirmed usage_events' join,
// identity and pricing-relevant columns (provider, model, timestamp_ms,
// api_key_hash, ...) but explicitly left token-count column names for this
// package to confirm via PRAGMA table_info against a live database, which
// this package cannot reach. Each field below is resolved from a short,
// documented candidate list (candidateInputTokenColumns etc.); a role with no
// matching column resolves to "" and is treated as always-zero for that role
// — cost/token totals stay honest (0, not fabricated) and Health() reports
// which roles could not be resolved so the gap is visible in logs/handoff,
// never silent.
type tokenColumns struct {
	input, output, cacheRead, cacheCreation string
}

// Candidate column names, most-likely-first. Every name here is a Go string
// literal under this package's control — never derived from row data or
// caller input — so building SQL identifiers from a *matched* candidate
// carries no injection risk even though the match itself is checked against
// live PRAGMA output.
var (
	candidateInputTokenColumns         = []string{"input_tokens", "prompt_tokens", "tokens_in"}
	candidateOutputTokenColumns        = []string{"output_tokens", "completion_tokens", "tokens_out"}
	candidateCacheReadTokenColumns     = []string{"cache_read_tokens", "cache_read_input_tokens", "cached_tokens", "cache_tokens"}
	candidateCacheCreationTokenColumns = []string{"cache_creation_tokens", "cache_creation_input_tokens", "cache_write_tokens"}
)

// resolveTokenColumns queries PRAGMA table_info(usage_events) and picks the
// first matching candidate for each token role. missing lists the roles
// (by field name: "input"/"output"/"cache_read"/"cache_creation") that
// resolved to no column at all, for Health()/log visibility.
func resolveTokenColumns(ctx context.Context, db *sql.DB) (cols tokenColumns, missing []string, err error) {
	present, err := tableColumnSet(ctx, db, "usage_events")
	if err != nil {
		return tokenColumns{}, nil, err
	}
	resolve := func(role string, candidates []string) string {
		for _, name := range candidates {
			if present[name] {
				return name
			}
		}
		missing = append(missing, role)
		return ""
	}
	cols.input = resolve("input", candidateInputTokenColumns)
	cols.output = resolve("output", candidateOutputTokenColumns)
	cols.cacheRead = resolve("cache_read", candidateCacheReadTokenColumns)
	cols.cacheCreation = resolve("cache_creation", candidateCacheCreationTokenColumns)
	return cols, missing, nil
}

// tableColumnSet returns the set of column names PRAGMA table_info reports
// for one table. table is always one of this package's own literal constants
// at call sites — never external input — so building the PRAGMA statement by
// concatenation (SQLite does not support binding identifiers/table names as
// query parameters) carries no injection risk.
func tableColumnSet(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, fmt.Errorf("cpa: read schema of %s: %w", table, err)
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var (
			cid       int64
			name      string
			decltype  sql.NullString
			notnull   int64
			dfltValue sql.NullString
			pk        int64
		)
		if err := rows.Scan(&cid, &name, &decltype, &notnull, &dfltValue, &pk); err != nil {
			return nil, fmt.Errorf("cpa: scan schema of %s: %w", table, err)
		}
		out[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cpa: iterate schema of %s: %w", table, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("cpa: table %s reported no columns (missing or unreadable)", table)
	}
	return out, nil
}

// sumExpr returns a SQL fragment summing one token column, or the literal
// "0" when the role could not be resolved (see tokenColumns' doc comment).
// column is always "" or one of this package's own candidate constants.
func sumExpr(column string) string {
	if column == "" {
		return "0"
	}
	return `COALESCE(SUM("` + column + `"),0)`
}
