package cpa

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// fixedNow is the clock every test in this file injects: freshness is a
// function of time, testing it against a real clock builds the test on sand.
var fixedNow = time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
var snapshotObserved = time.Date(2026, 8, 31, 11, 45, 0, 0, time.UTC)

const snapshotGeneration = "0123456789abcdef0123456789abcdef"

const fullSchemaDDL = `
CREATE TABLE usage_events (
	id INTEGER PRIMARY KEY,
	request_id TEXT,
	event_hash TEXT,
	timestamp_ms INTEGER,
	timestamp TEXT,
	provider TEXT,
	executor_type TEXT,
	model TEXT,
	endpoint TEXT,
	method TEXT,
	path TEXT,
	auth_type TEXT,
	auth_index INTEGER,
	source TEXT,
	source_hash TEXT,
	api_key_hash TEXT,
	input_tokens INTEGER,
	output_tokens INTEGER,
	cache_read_tokens INTEGER,
	cache_creation_tokens INTEGER
);
CREATE TABLE model_prices (
	model TEXT PRIMARY KEY,
	prompt_per_1m REAL,
	completion_per_1m REAL,
	cache_per_1m REAL,
	cache_read_per_1m REAL,
	cache_creation_per_1m REAL
);
CREATE TABLE api_key_aliases (
	api_key_hash TEXT PRIMARY KEY,
	alias TEXT
);
CREATE TABLE codex_inspection_runs (
	id INTEGER PRIMARY KEY,
	started_at_ms INTEGER NOT NULL
);
CREATE TABLE codex_inspection_results (
	run_id INTEGER,
	account_key TEXT,
	file_name TEXT,
	display_account TEXT,
	auth_index INTEGER,
	account_id TEXT,
	provider TEXT,
	disabled INTEGER,
	status TEXT,
	state TEXT,
	action TEXT,
	action_reason TEXT
);
`

// newSyntheticDB builds a throwaway usage.sqlite under a fresh temp
// directory, runs ddl then seed against a read-write handle, closes it, and
// returns the directory a FileClient should be pointed at. Building the
// database via a real read-write connection (rather than hand-writing bytes)
// is deliberate: it proves the read path works against whatever this SQLite
// build actually persists, not against a guess at the file format.
func newSyntheticDB(t *testing.T, ddl string, seed func(t *testing.T, db *sql.DB)) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.ToSlash(filepath.Join(dir, defaultDatabaseFileName))

	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatalf("open synthetic db: %v", err)
	}
	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		t.Fatalf("create schema: %v", err)
	}
	mustExec(t, db, `CREATE TABLE xingmang_snapshot_metadata_v1 (
		singleton_id INTEGER PRIMARY KEY,
		generation TEXT NOT NULL,
		observed_at_ms INTEGER NOT NULL
	)`)
	mustExec(t, db, `INSERT INTO xingmang_snapshot_metadata_v1(singleton_id,generation,observed_at_ms) VALUES(1,?,?)`,
		snapshotGeneration, snapshotObserved.UnixMilli())
	if seed != nil {
		seed(t, db)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close synthetic db: %v", err)
	}
	return dir
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func newTestClient(t *testing.T, dataDir string) ReadClient {
	t.Helper()
	client, err := NewFileClient(FileConfig{
		DataDir: dataDir,
		Now:     func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("NewFileClient: %v", err)
	}
	return client
}

func TestNewFileClientRejectsDatabaseFileNameEscapes(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	for _, name := range []string{
		"../outside.sqlite",
		`..\outside.sqlite`,
		filepath.Join(dataDir, "outside.sqlite"),
		"nested/usage.sqlite",
		`nested\usage.sqlite`,
		"usage.sqlite?mode=rw",
		"usage.sqlite%3fmode=rw",
	} {
		name := name
		t.Run(name, func(t *testing.T) {
			if _, err := NewFileClient(FileConfig{DataDir: dataDir, FileName: name}); err == nil {
				t.Fatalf("NewFileClient(FileName=%q) succeeded; want confinement error", name)
			}
		})
	}

	if _, err := NewFileClient(FileConfig{DataDir: dataDir, FileName: "usage-current.sqlite"}); err != nil {
		t.Fatalf("NewFileClient rejected a safe basename: %v", err)
	}
}

// req1Ms / req2Ms fall inside 2026-08-31 UTC; priorDayMs falls the day
// before and must never appear in a "2026-08-31" query.
var (
	req1Ms     = time.Date(2026, 8, 31, 1, 0, 0, 0, time.UTC).UnixMilli()
	req2Ms     = time.Date(2026, 8, 31, 2, 0, 0, 0, time.UTC).UnixMilli()
	req3Ms     = time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC).UnixMilli()
	priorDayMs = time.Date(2026, 8, 30, 23, 0, 0, 0, time.UTC).UnixMilli()
)

func seedUsageFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	// claude-x is fully priced; gpt-y has no model_prices row at all.
	mustExec(t, db, `INSERT INTO model_prices (model, prompt_per_1m, completion_per_1m, cache_read_per_1m, cache_creation_per_1m) VALUES (?,?,?,?,?)`,
		"claude-x", 3.0, 15.0, 0.3, 3.75)

	mustExec(t, db, `INSERT INTO api_key_aliases (api_key_hash, alias) VALUES (?,?)`, "hash-key-a", "prod-key-a")
	// key B is deliberately left with no alias row.

	// key A / claude-x, request 1: no cache tokens.
	mustExec(t, db, `INSERT INTO usage_events (timestamp_ms, provider, model, api_key_hash, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens) VALUES (?,?,?,?,?,?,?,?)`,
		req1Ms, "anthropic", "claude-x", "hash-key-a", 1000, 500, 0, 0)
	// key A / claude-x, request 2: with cache tokens.
	mustExec(t, db, `INSERT INTO usage_events (timestamp_ms, provider, model, api_key_hash, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens) VALUES (?,?,?,?,?,?,?,?)`,
		req2Ms, "anthropic", "claude-x", "hash-key-a", 2000, 1000, 100000, 50000)
	// key B / gpt-y: unpriced model.
	mustExec(t, db, `INSERT INTO usage_events (timestamp_ms, provider, model, api_key_hash, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens) VALUES (?,?,?,?,?,?,?,?)`,
		req3Ms, "openai", "gpt-y", "hash-key-b", 300, 200, 0, 0)
	// Prior-day row for a third key/model: must never show up in a
	// "2026-08-31" query. If day filtering breaks, totals below would be off.
	mustExec(t, db, `INSERT INTO usage_events (timestamp_ms, provider, model, api_key_hash, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens) VALUES (?,?,?,?,?,?,?,?)`,
		priorDayMs, "anthropic", "claude-x", "hash-key-c", 9999, 9999, 0, 0)

	// The newest run has the smaller integer ID. Ordering by ID/rowid would
	// therefore choose 900 incorrectly; only started_at_ms chooses 101.
	oldRunAt := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC).UnixMilli()
	newRunAt := time.Date(2026, 8, 31, 10, 30, 0, 0, time.UTC).UnixMilli()
	mustExec(t, db, `INSERT INTO codex_inspection_runs (id,started_at_ms) VALUES (?,?)`, 900, oldRunAt)
	mustExec(t, db, `INSERT INTO codex_inspection_results (run_id, account_key, display_account, provider, disabled, status, state, action, action_reason) VALUES (900,'acct-old','Old Account','anthropic',1,'stale','stale','disable','superseded run')`)

	mustExec(t, db, `INSERT INTO codex_inspection_runs (id,started_at_ms) VALUES (?,?)`, 101, newRunAt)
	mustExec(t, db, `INSERT INTO codex_inspection_results (run_id, account_key, display_account, provider, disabled, status, state, action, action_reason) VALUES (101,'acct-ok','OK Account','anthropic',0,'clean','clean','','')`)
	mustExec(t, db, `INSERT INTO codex_inspection_results (run_id, account_key, display_account, provider, disabled, status, state, action, action_reason) VALUES (101,'acct-bad','Bad Account','anthropic',0,'error','needs_review','reauth_required','token expired')`)
}

func TestFileClient_VersionHealthCapabilities(t *testing.T) {
	dir := newSyntheticDB(t, fullSchemaDDL, seedUsageFixture)
	client := newTestClient(t, dir)
	ctx := context.Background()

	version, err := client.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if !version.Supported || version.Detected == "" {
		t.Fatalf("Version = %+v, want Supported=true and a non-empty Detected", version)
	}

	health, err := client.Health(ctx)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !health.Healthy {
		t.Fatalf("Health = %+v, want Healthy=true (all candidate token columns matched)", health)
	}
	if health.Detail != "" {
		t.Fatalf("Health.Detail = %q, want empty (no unresolved token columns)", health.Detail)
	}

	caps, err := client.Capabilities(ctx)
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if len(caps) != len(ReadCapabilities) {
		t.Fatalf("Capabilities returned %d entries, want %d", len(caps), len(ReadCapabilities))
	}
}

func TestFileClient_UsageSummary(t *testing.T) {
	dir := newSyntheticDB(t, fullSchemaDDL, seedUsageFixture)
	client := newTestClient(t, dir)

	got, err := client.UsageSummary(context.Background(), "2026-08-31")
	if err != nil {
		t.Fatalf("UsageSummary: %v", err)
	}
	if got.Instance != FileInstance {
		t.Fatalf("Instance = %q, want %q", got.Instance, FileInstance)
	}
	if !got.ObservedAt.Equal(snapshotObserved) || got.Watermark != snapshotGeneration {
		t.Fatalf("Snapshot = %+v, want producer time %s and generation %s", got.Snapshot, snapshotObserved, snapshotGeneration)
	}
	if got.TotalRequestCount != 3 {
		t.Fatalf("TotalRequestCount = %d, want 3 (prior-day row must be excluded)", got.TotalRequestCount)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("Rows = %d entries, want 2 (claude-x, gpt-y)", len(got.Rows))
	}

	// Rows are ordered by provider then model: "anthropic" < "openai".
	claude := got.Rows[0]
	if claude.Provider != "anthropic" || claude.Model != "claude-x" {
		t.Fatalf("Rows[0] = %+v, want anthropic/claude-x first", claude)
	}
	if claude.RequestCount != 2 || claude.TokensIn != 3000 || claude.TokensOut != 1500 ||
		claude.TokensCacheRead != 100000 || claude.TokensCacheCreation != 50000 {
		t.Fatalf("claude-x row = %+v, want request_count=2 tokens_in=3000 tokens_out=1500 cache_read=100000 cache_creation=50000", claude)
	}
	if claude.CostMicros == nil || *claude.CostMicros != 249000 {
		t.Fatalf("claude-x CostMicros = %v, want 249000", claude.CostMicros)
	}

	gpt := got.Rows[1]
	if gpt.Provider != "openai" || gpt.Model != "gpt-y" {
		t.Fatalf("Rows[1] = %+v, want openai/gpt-y", gpt)
	}
	if gpt.RequestCount != 1 || gpt.CostMicros != nil {
		t.Fatalf("gpt-y row = %+v, want request_count=1 and CostMicros=nil (no model_prices row)", gpt)
	}

	if got.TotalCostMicros == nil || *got.TotalCostMicros != 249000 {
		t.Fatalf("TotalCostMicros = %v, want 249000 (only claude-x is priced)", got.TotalCostMicros)
	}
	if got.Currency != Currency {
		t.Fatalf("Currency = %q, want %q", got.Currency, Currency)
	}
	if got.UnpricedRequestCount != 1 {
		t.Fatalf("UnpricedRequestCount = %d, want 1", got.UnpricedRequestCount)
	}
	if len(got.UnpricedModels) != 1 || got.UnpricedModels[0] != "openai/gpt-y" {
		t.Fatalf("UnpricedModels = %v, want [openai/gpt-y]", got.UnpricedModels)
	}
	if !got.IsPartial {
		t.Fatal("IsPartial = false, want true (one unpriced model)")
	}
}

func TestFileClient_UsageSummary_EmptyDay(t *testing.T) {
	dir := newSyntheticDB(t, fullSchemaDDL, seedUsageFixture)
	client := newTestClient(t, dir)

	got, err := client.UsageSummary(context.Background(), "2020-01-01")
	if err != nil {
		t.Fatalf("UsageSummary on a day with no rows should not error: %v", err)
	}
	if got.TotalRequestCount != 0 || len(got.Rows) != 0 {
		t.Fatalf("got %+v, want an empty (not nil-erroring) summary for a day with no data", got)
	}
	if got.TotalCostMicros != nil {
		t.Fatalf("TotalCostMicros = %v, want nil (nothing to sum, not a fabricated 0)", got.TotalCostMicros)
	}
}

func TestFileClient_UsageSummary_RejectsMalformedDay(t *testing.T) {
	dir := newSyntheticDB(t, fullSchemaDDL, seedUsageFixture)
	client := newTestClient(t, dir)
	for _, bad := range []string{"", "not-a-day", "2026/08/31", "2026-8-31"} {
		if _, err := client.UsageSummary(context.Background(), bad); err == nil {
			t.Fatalf("UsageSummary(%q) = nil error, want a bad_response error", bad)
		}
	}
}

func TestFileClient_KeyUsage(t *testing.T) {
	dir := newSyntheticDB(t, fullSchemaDDL, seedUsageFixture)
	client := newTestClient(t, dir)

	got, err := client.KeyUsage(context.Background(), "2026-08-31")
	if err != nil {
		t.Fatalf("KeyUsage: %v", err)
	}
	if got.TotalKeyCount != 2 {
		t.Fatalf("TotalKeyCount = %d, want 2 (prior-day key excluded)", got.TotalKeyCount)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("Rows = %d, want 2", len(got.Rows))
	}

	// Sorted by request_count desc: key A (2 requests) before key B (1).
	keyA := got.Rows[0]
	if keyA.APIKeyHash != "hash-key-a" || keyA.Alias != "prod-key-a" {
		t.Fatalf("Rows[0] = %+v, want hash-key-a aliased prod-key-a", keyA)
	}
	if keyA.RequestCount != 2 || keyA.TokensIn != 3000 || keyA.TokensCacheCreation != 50000 {
		t.Fatalf("key A row = %+v, want request_count=2 tokens_in=3000 cache_creation=50000", keyA)
	}
	if keyA.CostMicros == nil || *keyA.CostMicros != 249000 {
		t.Fatalf("key A CostMicros = %v, want 249000", keyA.CostMicros)
	}
	if keyA.UnpricedRequestCount != 0 {
		t.Fatalf("key A UnpricedRequestCount = %d, want 0", keyA.UnpricedRequestCount)
	}
	if keyA.LastUsedAt == nil || !keyA.LastUsedAt.Equal(time.UnixMilli(req2Ms).UTC()) {
		t.Fatalf("key A LastUsedAt = %v, want %v (the later of its two requests)", keyA.LastUsedAt, time.UnixMilli(req2Ms).UTC())
	}

	keyB := got.Rows[1]
	if keyB.APIKeyHash != "hash-key-b" || keyB.Alias != "" {
		t.Fatalf("Rows[1] = %+v, want hash-key-b with no alias", keyB)
	}
	if keyB.RequestCount != 1 || keyB.CostMicros != nil || keyB.UnpricedRequestCount != 1 {
		t.Fatalf("key B row = %+v, want request_count=1 CostMicros=nil unpriced=1", keyB)
	}

	if !got.IsPartial {
		t.Fatal("IsPartial = false, want true (key B has unpriced usage)")
	}
}

func TestFileClient_AccountHealth(t *testing.T) {
	dir := newSyntheticDB(t, fullSchemaDDL, seedUsageFixture)
	client := newTestClient(t, dir)

	got, err := client.AccountHealth(context.Background())
	if err != nil {
		t.Fatalf("AccountHealth: %v", err)
	}
	if got.RunID != "101" {
		t.Fatalf("RunID = %q, want decimal integer ID 101 selected by started_at_ms", got.RunID)
	}
	wantRunAt := time.Date(2026, 8, 31, 10, 30, 0, 0, time.UTC)
	if got.RunAt == nil || !got.RunAt.Equal(wantRunAt) {
		t.Fatalf("RunAt = %v, want %v", got.RunAt, wantRunAt)
	}
	if !got.ObservedAt.Equal(snapshotObserved) || got.Watermark != snapshotGeneration {
		t.Fatalf("Snapshot = %+v, want producer metadata", got.Snapshot)
	}
	if got.AccountCount != 2 {
		t.Fatalf("AccountCount = %d, want 2 (run-old's account must not be counted)", got.AccountCount)
	}
	if got.DisabledCount != 0 {
		t.Fatalf("DisabledCount = %d, want 0 (both run-new accounts have disabled=0)", got.DisabledCount)
	}
	if got.AnomalyCount != 1 || len(got.Anomalies) != 1 {
		t.Fatalf("Anomalies = %+v (count %d), want exactly acct-bad", got.Anomalies, got.AnomalyCount)
	}
	anomaly := got.Anomalies[0]
	if anomaly.AccountKey != "acct-bad" || anomaly.DisplayAccount != "Bad Account" ||
		anomaly.Action != "reauth_required" || anomaly.ActionReason != "token expired" {
		t.Fatalf("Anomalies[0] = %+v, want acct-bad flagged for reauth_required", anomaly)
	}
	if got.Truncated {
		t.Fatal("Truncated = true, want false (only one anomaly, well under MaxAnomalies)")
	}
}

func TestFileClient_AccountHealth_NoRunsYet(t *testing.T) {
	dir := newSyntheticDB(t, fullSchemaDDL, nil) // schema only, no rows at all
	client := newTestClient(t, dir)

	got, err := client.AccountHealth(context.Background())
	if err != nil {
		t.Fatalf("AccountHealth with no runs should not error: %v", err)
	}
	if got.RunID != "" || got.AccountCount != 0 {
		t.Fatalf("got %+v, want an empty, non-error summary when no run has ever completed", got)
	}
}

func TestFileClientRejectsDatabaseWithoutPublishedSnapshotMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.ToSlash(filepath.Join(dir, defaultDatabaseFileName))
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(fullSchemaDDL); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	client := newTestClient(t, dir)
	if _, err = client.UsageSummary(context.Background(), "2026-08-31"); err == nil {
		t.Fatal("UsageSummary accepted an ordinary database without snapshot metadata")
	}
}

// TestFileClient_TokenColumnFallback proves the PRAGMA-driven column
// resolution actually falls back through the candidate list (contract §8)
// instead of only ever matching the first-choice names the main fixture
// happens to use. A completely unresolved role may degrade to 0, but every
// consumer-visible result and health signal must be marked incomplete so the
// value cannot be mistaken for a complete cost/usage total.
func TestFileClient_TokenColumnFallback(t *testing.T) {
	const altDDL = `
CREATE TABLE usage_events (
	id INTEGER PRIMARY KEY,
	timestamp_ms INTEGER,
	provider TEXT,
	model TEXT,
	api_key_hash TEXT,
	prompt_tokens INTEGER,
	completion_tokens INTEGER
);
CREATE TABLE model_prices (model TEXT PRIMARY KEY, prompt_per_1m REAL, completion_per_1m REAL, cache_per_1m REAL, cache_read_per_1m REAL, cache_creation_per_1m REAL);
CREATE TABLE api_key_aliases (api_key_hash TEXT PRIMARY KEY, alias TEXT);
CREATE TABLE codex_inspection_runs (id INTEGER PRIMARY KEY, started_at_ms INTEGER NOT NULL);
CREATE TABLE codex_inspection_results (run_id INTEGER, account_key TEXT, display_account TEXT, provider TEXT, disabled INTEGER, status TEXT, state TEXT, action TEXT, action_reason TEXT);
`
	dir := newSyntheticDB(t, altDDL, func(t *testing.T, db *sql.DB) {
		mustExec(t, db, `INSERT INTO usage_events (timestamp_ms, provider, model, api_key_hash, prompt_tokens, completion_tokens) VALUES (?,?,?,?,?,?)`,
			req1Ms, "anthropic", "claude-x", "hash-key-a", 111, 222)
	})
	client := newTestClient(t, dir)

	health, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if health.Healthy {
		t.Fatalf("Health = %+v, want Healthy=false when required token roles are unresolved", health)
	}
	if health.Detail == "" {
		t.Fatal("Health.Detail = \"\", want a note naming the unresolved cache_read/cache_creation roles")
	}

	got, err := client.UsageSummary(context.Background(), "2026-08-31")
	if err != nil {
		t.Fatalf("UsageSummary: %v", err)
	}
	if len(got.Rows) != 1 {
		t.Fatalf("Rows = %d, want 1", len(got.Rows))
	}
	row := got.Rows[0]
	if row.TokensIn != 111 || row.TokensOut != 222 {
		t.Fatalf("row = %+v, want tokens_in=111 (via prompt_tokens fallback) tokens_out=222 (via completion_tokens fallback)", row)
	}
	if row.TokensCacheRead != 0 || row.TokensCacheCreation != 0 {
		t.Fatalf("row = %+v, want cache tokens 0 (no matching column at all, not an error)", row)
	}
	if !got.IsPartial {
		t.Fatal("UsageSummary.IsPartial = false, want true when token roles are unresolved")
	}

	keys, err := client.KeyUsage(context.Background(), "2026-08-31")
	if err != nil {
		t.Fatalf("KeyUsage: %v", err)
	}
	if !keys.IsPartial {
		t.Fatal("KeyUsage.IsPartial = false, want true when token roles are unresolved")
	}
}
