package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AUD2 contract tests are opt-in and require a disposable loopback PostgreSQL
// database. They deliberately do not use XM_TEST_DATABASE_URL, which may point
// at the shared launch stack.
func aud2ContractPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("XM_AUD2_CONTRACT_DATABASE_URL"))
	if dsn == "" {
		t.Skip("set XM_AUD2_CONTRACT_DATABASE_URL to a disposable loopback database")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse AUD2 contract DSN: %v", err)
	}
	if !isLoopbackHost(config.ConnConfig.Host) {
		t.Fatalf("AUD2 contract database must be loopback, got %q", config.ConnConfig.Host)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatalf("open AUD2 contract pool: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Fatalf("ping AUD2 contract database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func isLoopbackHost(host string) bool {
	switch strings.TrimSpace(strings.ToLower(host)) {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

func insertAuditChainRoot(t *testing.T, tx pgx.Tx, id uuid.UUID) {
	t.Helper()
	_, err := tx.Exec(context.Background(), `
		INSERT INTO audit.chain_root
		  (id, computed_at, from_sequence, to_sequence, root_hash, signature, key_id)
		VALUES ($1, $2, 1, 1, $3, $4, $5)`,
		id, time.Now().UTC(), strings.Repeat("a", 64), "contract-signature", "contract-key")
	if err != nil {
		t.Fatalf("insert contract chain root: %v", err)
	}
}

func archiveSegmentArgs(id, rootID uuid.UUID, from, to int64) []any {
	return []any{
		id, 1, from, to, to - from + 1,
		strings.Repeat("b", 64), strings.Repeat("c", 64),
		json.RawMessage(`[{"version":1,"row_count":1},{"version":2,"row_count":0}]`),
		json.RawMessage(`{"development":1}`),
		"audit/v1/payload/seq-0000000000000000001-0000000000000000001-" + strings.Repeat("d", 64) + ".ndjson",
		"provider-version-1", strings.Repeat("d", 64), int64(24),
		json.RawMessage(`[]`),
		"audit/v1/manifest/seq-0000000000000000001-0000000000000000001-" + strings.Repeat("e", 64) + ".json",
		"provider-version-2", strings.Repeat("e", 64), "contract-signature", "contract-key", rootID,
		strings.Repeat("a", 64), strings.Repeat("f", 64), int64(1), time.Now().UTC(), time.Now().UTC(),
	}
}

func insertArchiveSegment(t *testing.T, tx pgx.Tx, id, rootID uuid.UUID, from, to int64) error {
	t.Helper()
	_, err := tx.Exec(context.Background(), `
		INSERT INTO audit.archive_segment (
		  id, format_version, from_sequence, to_sequence, row_count,
		  first_prev_hash, last_event_hash, canonical_version_counts, environment_counts,
		  payload_object_key, payload_version_id, payload_sha256, payload_size_bytes,
		  projections, manifest_object_key, manifest_version_id, manifest_sha256,
		  manifest_signature, manifest_key_id, chain_root_id, chain_root_hash,
		  checkpoint_sha256, recovery_generation, committed_at, verified_at
		) VALUES (
		  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25
		)`, archiveSegmentArgs(id, rootID, from, to)...)
	return err
}

func TestAUD2ArchiveSegmentAcceptsCommittedRow(t *testing.T) {
	pool := aud2ContractPool(t)
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	rootID := uuid.New()
	insertAuditChainRoot(t, tx, rootID)
	if err := insertArchiveSegment(t, tx, uuid.New(), rootID, 1, 1); err != nil {
		t.Fatalf("valid archive segment rejected: %v", err)
	}
}

func TestAUD2ArchiveSegmentRejectsInvalidRangeAndHash(t *testing.T) {
	pool := aud2ContractPool(t)
	for _, tc := range []struct {
		name   string
		mutate func([]any)
	}{
		{name: "range", mutate: func(args []any) { args[3] = int64(0); args[4] = int64(0) }},
		{name: "hash", mutate: func(args []any) { args[11] = "not-a-sha" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := pool.Begin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			rootID := uuid.New()
			insertAuditChainRoot(t, tx, rootID)
			args := archiveSegmentArgs(uuid.New(), rootID, 1, 1)
			tc.mutate(args)
			_, err = tx.Exec(context.Background(), `
				INSERT INTO audit.archive_segment (
				  id, format_version, from_sequence, to_sequence, row_count,
				  first_prev_hash, last_event_hash, canonical_version_counts, environment_counts,
				  payload_object_key, payload_version_id, payload_sha256, payload_size_bytes,
				  projections, manifest_object_key, manifest_version_id, manifest_sha256,
				  manifest_signature, manifest_key_id, chain_root_id, chain_root_hash,
				  checkpoint_sha256, recovery_generation, committed_at, verified_at
				) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25
				)`, args...)
			if err == nil {
				t.Fatal("invalid archive segment unexpectedly accepted")
			}
		})
	}
}

func TestAUD2ArchiveSegmentDuplicateRangeIsRejected(t *testing.T) {
	pool := aud2ContractPool(t)
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	rootID := uuid.New()
	insertAuditChainRoot(t, tx, rootID)
	if err := insertArchiveSegment(t, tx, uuid.New(), rootID, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := insertArchiveSegment(t, tx, uuid.New(), rootID, 1, 1); err == nil {
		t.Fatal("duplicate archive range unexpectedly accepted")
	}
}

func TestAUD2ArchiveSegmentIsAppendOnly(t *testing.T) {
	pool := aud2ContractPool(t)
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	rootID := uuid.New()
	segmentID := uuid.New()
	insertAuditChainRoot(t, tx, rootID)
	if err := insertArchiveSegment(t, tx, segmentID, rootID, 1, 1); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE audit.archive_segment SET row_count = 2 WHERE id = $1`,
		`DELETE FROM audit.archive_segment WHERE id = $1`,
		`TRUNCATE audit.archive_segment`,
	} {
		var err error
		if strings.HasPrefix(statement, "TRUNCATE") {
			_, err = tx.Exec(context.Background(), statement)
		} else {
			_, err = tx.Exec(context.Background(), statement, segmentID)
		}
		if err == nil {
			// A no-op rule is also acceptable, but the row must remain unchanged.
			var count int
			if scanErr := tx.QueryRow(context.Background(), `SELECT count(*) FROM audit.archive_segment WHERE id = $1`, segmentID).Scan(&count); scanErr != nil {
				t.Fatal(scanErr)
			}
			if count != 1 {
				t.Fatalf("append-only statement removed/changed row: %s", statement)
			}
		}
	}
}

func TestAUD2ArchiveSchemaMissingIsAVisibleRedFailure(t *testing.T) {
	if strings.TrimSpace(os.Getenv("XM_AUD2_CONTRACT_DATABASE_URL")) == "" || os.Getenv("XM_AUD2_EXPECT_SCHEMA") != "1" {
		t.Skip("set XM_AUD2_CONTRACT_DATABASE_URL and XM_AUD2_EXPECT_SCHEMA=1 for the migration RED probe")
	}
	pool := aud2ContractPool(t)
	var relation *string
	if err := pool.QueryRow(context.Background(), `SELECT to_regclass('audit.archive_segment')`).Scan(&relation); err != nil {
		t.Fatal(err)
	}
	if relation == nil || *relation != "audit.archive_segment" {
		t.Fatalf("AUD2 migration RED: audit.archive_segment is absent")
	}
}

func TestAUD2ContractDSNRejectsRemoteHost(t *testing.T) {
	if os.Getenv("XM_AUD2_CONTRACT_DATABASE_URL") == "" {
		t.Skip("opt-in contract test")
	}
	config, err := pgxpool.ParseConfig(os.Getenv("XM_AUD2_CONTRACT_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	if isLoopbackHost(config.ConnConfig.Host) {
		return
	}
	// Keep this assertion explicit so a future helper cannot silently relax the
	// loopback-only contract.
	t.Fatalf("remote contract host %q must be rejected", config.ConnConfig.Host)
}

func TestAUD2ContractErrorDoesNotExposeDSN(t *testing.T) {
	err := fmt.Errorf("archive contract failed: %w", &pgconn.PgError{Code: "42501", Message: "permission denied"})
	if strings.Contains(err.Error(), "password") || strings.Contains(err.Error(), "postgres://") {
		t.Fatalf("contract error contains credential material: %v", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("unexpected sentinel")
	}
}
