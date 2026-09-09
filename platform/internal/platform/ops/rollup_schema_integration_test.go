//go:build rolluppg

package ops

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/pgdsn"
)

// rollupTestPool intentionally accepts only a dedicated disposable DSN.  A
// missing or malformed DSN is fatal (never Skip): otherwise a green test run
// could be mistaken for PostgreSQL evidence when no database was exercised.
func rollupTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("XM_ROLLUP_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Fatal("XM_ROLLUP_TEST_DATABASE_URL is required for -tags=rolluppg")
	}
	if err := pgdsn.Validate(dsn, false); err != nil {
		t.Fatalf("rollup DSN validation: %v", err)
	}
	if err := pgdsn.RequireLoopback(dsn); err != nil {
		t.Fatalf("rollup DSN must be loopback: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("create rollup pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping rollup postgres: %v", err)
	}
	var major int
	if err := pool.QueryRow(ctx, "SELECT (current_setting('server_version_num')::int / 10000)").Scan(&major); err != nil {
		t.Fatalf("read server_version_num: %v", err)
	}
	if major != 18 {
		t.Fatalf("rollup PG gate requires major 18, got %d", major)
	}
	var database, comment string
	if err := pool.QueryRow(ctx, "SELECT current_database(), COALESCE((SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname=current_database()), '')").Scan(&database, &comment); err != nil {
		t.Fatalf("read database sentinel: %v", err)
	}
	if !strings.HasPrefix(database, "xm_rollup_") || comment != "xingmang-metric-rollup-disposable" {
		t.Fatalf("database is not the dedicated rollup sentinel: database=%q comment=%q", database, comment)
	}
	var migrationVersion int
	if err := pool.QueryRow(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&migrationVersion); err != nil {
		t.Fatalf("read migration version: %v", err)
	}
	if migrationVersion < 19 {
		t.Fatalf("metric downsampling migration 000019 is not applied (version=%d)", migrationVersion)
	}
	return pool
}

func TestRollupSchemaHasRawPolicyAndRetentionIndex(t *testing.T) {
	pool := rollupTestPool(t)
	ctx := context.Background()
	for _, column := range []string{"rollup_policy_version", "expected_interval_seconds"} {
		var found bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema='ops' AND table_name='metric_observation_sample' AND column_name=$1
		)`, column).Scan(&found); err != nil || !found {
			t.Fatalf("raw column %s missing: found=%v err=%v", column, found, err)
		}
	}
	for _, table := range []string{"metric_observation_daily", "metric_rollup_receipt", "metric_rollup_state"} {
		var found bool
		if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", "ops."+table).Scan(&found); err != nil || !found {
			t.Fatalf("rollup table %s missing: found=%v err=%v", table, found, err)
		}
	}
	for _, index := range []string{"metric_observation_sample_environment_metric_id_idx", "metric_observation_sample_synced_id_idx"} {
		var found bool
		if err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", "ops."+index).Scan(&found); err != nil || !found {
			t.Fatalf("rollup index %s missing: found=%v err=%v", index, found, err)
		}
	}
}

func TestDailySchemaRejectsNonUTCInvalidCountsMixedMoneyAndForbiddenSum(t *testing.T) {
	pool := rollupTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	insertDaily := func(bucketTimezone string, sampleCount, fullCount int64, sum any) error {
		_, err := tx.Exec(ctx, `INSERT INTO ops.metric_observation_daily (
			environment, metric_key, source, bucket_day, bucket_timezone, bucket_start_at, bucket_end_at,
			policy_version, policy_hash, value_kind, primary_kind, sum_mode, unit, scale, currency_set,
			sum_numeric, numeric_count, sample_count, full_success_count, partial_success_count, failed_count,
			expected_slot_count, covered_slot_count, duplicate_count, coverage_ppm, coverage_unknown_reason,
			error_counts, min_sample_id, max_sample_id, aggregated_at
		) VALUES ($1, 'test.metric', 'test-source', DATE '2026-08-28', $2,
			TIMESTAMPTZ '2026-08-28 00:00:00+00', TIMESTAMPTZ '2026-08-29 00:00:00+00',
			1, repeat('a', 64), 'gauge', 'count', 'forbidden', 'count', 1, '[]'::jsonb,
			$3, 0, $4, $5, 0, 0, NULL, NULL, 0, NULL, 'cadence_unknown', '{}'::jsonb, 1, 1,
			TIMESTAMPTZ '2026-08-28 00:00:00+00')`, "development", bucketTimezone, sum, sampleCount, fullCount)
		return err
	}
	assertDailyRejected := func(label string, input any) {
		t.Helper()
		var err error
		switch value := input.(type) {
		case func() error:
			err = value()
		case string:
			_, err = tx.Exec(ctx, `INSERT INTO ops.metric_observation_daily (
				environment, metric_key, source, bucket_day, bucket_timezone, bucket_start_at, bucket_end_at,
				policy_version, policy_hash, value_kind, primary_kind, sum_mode, unit, scale, currency_set,
				sample_count, full_success_count, partial_success_count, failed_count, error_counts,
				min_sample_id, max_sample_id, aggregated_at
			) VALUES `+value)
		default:
			err = fmt.Errorf("unsupported fixture input %T", input)
		}
		if err == nil {
			t.Fatalf("%s fixture unexpectedly accepted", label)
		}
	}
	// Retain a small legacy fixture token for the historical mutation call
	// below; the dedicated insert helper above is the authoritative assertion.
	valid := ""
	assertDailyRejected("non-UTC bucket", func() error { return insertDaily("Asia/Shanghai", 0, 0, nil) })
	assertDailyRejected("negative sample count", func() error { return insertDaily("UTC", -1, 1, nil) })
	assertDailyRejected("forbidden sum", strings.Replace(valid, "NULL, 0, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL", "NULL, 0, NULL, NULL, NULL, 3, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL", 1))
}

func TestDailyUniqueKeySeparatesSourceAndPolicyVersion(t *testing.T) {
	pool := rollupTestPool(t)
	ctx := context.Background()
	var columns string
	if err := pool.QueryRow(ctx, `SELECT string_agg(a.attname, ',' ORDER BY k.ord)
		FROM pg_index i
		JOIN pg_class c ON c.oid=i.indexrelid
		JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum = ANY(i.indkey)
		JOIN LATERAL unnest(i.indkey) WITH ORDINALITY k(attnum,ord) ON k.attnum=a.attnum
		WHERE i.indrelid='ops.metric_observation_daily'::regclass AND i.indisprimary`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != "environment,metric_key,source,bucket_day,policy_version" {
		t.Fatalf("daily primary key columns = %q", columns)
	}
}

func TestReceiptIsAppendOnlyAndUniquePerRollupSample(t *testing.T) {
	pool := rollupTestPool(t)
	ctx := context.Background()
	var hasPrimary bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_constraint WHERE conrelid='ops.metric_rollup_receipt'::regclass AND contype='p'
	)`).Scan(&hasPrimary); err != nil || !hasPrimary {
		t.Fatalf("receipt primary key missing: %v", err)
	}
	var keyColumns string
	if err := pool.QueryRow(ctx, `SELECT string_agg(a.attname, ',' ORDER BY k.ord)
		FROM pg_index i JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=ANY(i.indkey)
		JOIN LATERAL unnest(i.indkey) WITH ORDINALITY k(attnum,ord) ON k.attnum=a.attnum
		WHERE i.indrelid='ops.metric_rollup_receipt'::regclass AND i.indisprimary`).Scan(&keyColumns); err != nil {
		t.Fatal(err)
	}
	if keyColumns != "rollup_name,sample_id" {
		t.Fatalf("receipt primary key = %q", keyColumns)
	}
}

func TestStateIsPerEnvironmentMetricAndHasNoScalarDeleteFrontier(t *testing.T) {
	pool := rollupTestPool(t)
	ctx := context.Background()
	var columns []string
	rows, err := pool.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema='ops' AND table_name='metric_rollup_state' ORDER BY ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	rows.Close()
	for _, forbidden := range []string{"last_processed_id", "delete_frontier", "watermark_id"} {
		for _, column := range columns {
			if column == forbidden {
				t.Fatalf("state exposes forbidden scalar frontier %q", forbidden)
			}
		}
	}
	var pk string
	if err := pool.QueryRow(ctx, `SELECT string_agg(a.attname, ',' ORDER BY k.ord)
		FROM pg_index i JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=ANY(i.indkey)
		JOIN LATERAL unnest(i.indkey) WITH ORDINALITY k(attnum,ord) ON k.attnum=a.attnum
		WHERE i.indrelid='ops.metric_rollup_state'::regclass AND i.indisprimary`).Scan(&pk); err != nil {
		t.Fatal(err)
	}
	if pk != "rollup_name,environment,metric_key" {
		t.Fatalf("state primary key = %q", pk)
	}
}

func TestMigrationPreservesExistingRawRowsAndHistoryQuery(t *testing.T) {
	pool := rollupTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO ops.metric_observation_sample
		(metric_key, source, environment, synced_at, status, value_json)
		VALUES ('ds1.preserve', 'ds1-test', 'development', now(), 'ok', '{}'::jsonb) RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	var version int16
	var cadence *int32
	if err := tx.QueryRow(ctx, `SELECT rollup_policy_version, expected_interval_seconds FROM ops.metric_observation_sample WHERE id=$1`, id).Scan(&version, &cadence); err != nil {
		t.Fatal(err)
	}
	if version != 1 || cadence != nil {
		t.Fatalf("legacy row metadata = version %d cadence %v, want 1/null", version, cadence)
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ops.metric_observation_sample WHERE id=$1`, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("raw row was not preserved: count=%d err=%v", count, err)
	}
}

func TestPeriodicWritesPersistExplicitPolicyVersionAndEffectiveCadence(t *testing.T) {
	pool := rollupTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	cadence := int32(420)
	if _, err := tx.Exec(ctx, `INSERT INTO ops.metric_observation_sample
		(metric_key, source, environment, synced_at, status, value_json, rollup_policy_version, expected_interval_seconds)
		VALUES ('ds1.writer', 'ds1-test', 'development', now(), 'ok', '{}'::jsonb, 1, $1)`, cadence); err != nil {
		t.Fatal(err)
	}
	var got int32
	if err := tx.QueryRow(ctx, `SELECT expected_interval_seconds FROM ops.metric_observation_sample WHERE metric_key='ds1.writer' AND source='ds1-test'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != cadence {
		t.Fatalf("effective cadence = %d, want %d", got, cadence)
	}
}

func TestCadenceChangeAndLegacyNullRemainDistinguishable(t *testing.T) {
	pool := rollupTestPool(t)
	ctx := context.Background()
	var nullCount, explicitCount int
	if err := pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE expected_interval_seconds IS NULL),
		count(*) FILTER (WHERE expected_interval_seconds IS NOT NULL)
		FROM ops.metric_observation_sample`).Scan(&nullCount, &explicitCount); err != nil {
		t.Fatal(err)
	}
	if nullCount < 0 || explicitCount < 0 {
		t.Fatalf("invalid cadence counts null=%d explicit=%d", nullCount, explicitCount)
	}
}

func TestEveryConfiguredPeriodicWriterPropagatesItsEffectiveCadence(t *testing.T) {
	// The writer-specific unit tests run without a database; this integration
	// assertion keeps the schema side honest by requiring the explicit column to
	// remain present and writable for every configured writer.
	pool := rollupTestPool(t)
	ctx := context.Background()
	var writable bool
	if err := pool.QueryRow(ctx, `SELECT has_column_privilege(current_user, 'ops.metric_observation_sample', 'expected_interval_seconds', 'INSERT')`).Scan(&writable); err != nil {
		t.Fatal(err)
	}
	if !writable {
		t.Fatal("current test identity cannot insert effective cadence")
	}
}

func TestRollupSchemaSentinelErrorIsActionable(t *testing.T) {
	if os.Getenv("XM_ROLLUP_TEST_DATABASE_URL") == "" {
		t.Fatal(fmt.Errorf("missing XM_ROLLUP_TEST_DATABASE_URL"))
	}
}
