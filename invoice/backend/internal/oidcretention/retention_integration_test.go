package oidcretention

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/migrate"
	"invoice-system/backend/internal/testdb"
)

func retentionIntegrationPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	// testdb.URL rewrites the shared default "invoice_test" database to a
	// per-git-worktree database (created on first use), so concurrent
	// worktrees never race the DROP SCHEMA CASCADE below.
	databaseURL := testdb.URL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	deadline := time.Now().Add(10 * time.Second)
	for {
		pingCtx, cancel := context.WithTimeout(ctx, time.Second)
		err = pool.Ping(pingCtx)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("PostgreSQL integration pool unavailable: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err = migrate.Up(ctx, pool, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	return pool, ctx
}

func insertLogoutRetentionEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string, receivedAt, expiresAt time.Time) {
	t.Helper()
	issuedAt := receivedAt.Add(-time.Minute)
	if !expiresAt.After(issuedAt) {
		issuedAt = expiresAt.Add(-time.Minute)
	}
	issuer := sha256.Sum256([]byte("issuer-" + id))
	jti := sha256.Sum256([]byte("jti-" + id))
	sid := sha256.Sum256([]byte("sid-" + id))
	_, err := pool.Exec(ctx, `
		INSERT INTO oidc_backchannel_logout_events(
			id,issuer_hash,jti_hash,sid_hash,token_issued_at,token_expires_at,
			received_at,revoked_session_count,request_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,0,$8)`,
		id, hex.EncodeToString(issuer[:]), hex.EncodeToString(jti[:]), hex.EncodeToString(sid[:]),
		issuedAt, expiresAt, receivedAt, "retention-test-"+id)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetentionDryRunBoundariesBatchingAndReplayGuard(t *testing.T) {
	pool, ctx := retentionIntegrationPool(t)
	var now time.Time
	if err := pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	for index, age := range []time.Duration{181 * 24 * time.Hour, 182 * 24 * time.Hour, 183 * 24 * time.Hour} {
		received := now.Add(-age)
		insertLogoutRetentionEvent(t, ctx, pool, fmt.Sprintf("10000000-0000-4000-8000-%012d", index+1), received, received.Add(5*time.Minute))
	}
	// Just inside the 180-day boundary must remain.
	recent := now.Add(-(179*24*time.Hour + 23*time.Hour))
	insertLogoutRetentionEvent(t, ctx, pool, "20000000-0000-4000-8000-000000000001", recent, recent.Add(5*time.Minute))
	// received_at alone can never make a still-live/unusually long-lived token
	// eligible: token_expires_at independently has to precede the cutoff.
	veryOld := now.Add(-220 * 24 * time.Hour)
	insertLogoutRetentionEvent(t, ctx, pool, "20000000-0000-4000-8000-000000000002", veryOld, now.Add(time.Hour))

	options := Options{Retention: MinimumRetention, BatchSize: 2}
	dryRun, err := Run(ctx, pool, options)
	if err != nil || dryRun.Eligible != 3 || dryRun.Deleted != 0 || dryRun.RequestID != "" {
		t.Fatalf("dry run=%+v error=%v", dryRun, err)
	}
	var before int64
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM oidc_backchannel_logout_events`).Scan(&before); err != nil || before != 5 {
		t.Fatalf("dry run mutated events: count=%d error=%v", before, err)
	}

	options.Execute = true
	options.MaintenanceConfirmed = true
	options.Reason = "approved integration retention"
	executed, err := Run(ctx, pool, options)
	if err != nil || executed.Eligible != 3 || executed.Deleted != 3 || executed.Batches != 2 || executed.RequestID == "" {
		t.Fatalf("execution=%+v error=%v", executed, err)
	}
	var remaining, batchAudits, completionAudits int64
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM oidc_backchannel_logout_events`).Scan(&remaining); err != nil || remaining != 2 {
		t.Fatalf("remaining=%d error=%v", remaining, err)
	}
	if err = pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE action='auth.backchannel_logout.retention.batch'),
		       count(*) FILTER (WHERE action='auth.backchannel_logout.retention.complete')
		FROM audit_events WHERE request_id=$1`, executed.RequestID).Scan(&batchAudits, &completionAudits); err != nil || batchAudits != 2 || completionAudits != 1 {
		t.Fatalf("batch audits=%d completion audits=%d error=%v", batchAudits, completionAudits, err)
	}
	var futureExpiryStillPresent bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM oidc_backchannel_logout_events WHERE id='20000000-0000-4000-8000-000000000002')`).Scan(&futureExpiryStillPresent); err != nil || !futureExpiryStillPresent {
		t.Fatalf("replay-window guard record present=%v error=%v", futureExpiryStillPresent, err)
	}
}

func TestRetentionConcurrentLockFailsClosed(t *testing.T) {
	pool, ctx := retentionIntegrationPool(t)
	lockConn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lockConn.Release()
	if _, err = lockConn.Exec(ctx, `SELECT pg_advisory_lock($1)`, maintenanceLock); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = lockConn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, maintenanceLock) }()
	_, err = Run(ctx, pool, Options{Retention: MinimumRetention})
	if !errors.Is(err, ErrMaintenanceBusy) {
		t.Fatalf("concurrent maintenance error=%v", err)
	}
}

func TestRetentionAuditFailureRollsBackDeletion(t *testing.T) {
	pool, ctx := retentionIntegrationPool(t)
	var now time.Time
	if err := pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	received := now.Add(-181 * 24 * time.Hour)
	insertLogoutRetentionEvent(t, ctx, pool, "30000000-0000-4000-8000-000000000001", received, received.Add(5*time.Minute))
	if _, err := pool.Exec(ctx, `
		CREATE FUNCTION fail_retention_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.action='auth.backchannel_logout.retention.batch' THEN
				RAISE EXCEPTION 'forced retention audit failure';
			END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER fail_retention_audit_trigger BEFORE INSERT ON audit_events
		FOR EACH ROW EXECUTE FUNCTION fail_retention_audit()`); err != nil {
		t.Fatal(err)
	}
	_, err := Run(ctx, pool, Options{
		Retention: MinimumRetention, BatchSize: 1, Execute: true,
		MaintenanceConfirmed: true, Reason: "forced audit rollback test",
	})
	if err == nil {
		t.Fatal("audit failure did not fail retention execution")
	}
	var events, audits int64
	if queryErr := pool.QueryRow(ctx, `SELECT count(*) FROM oidc_backchannel_logout_events`).Scan(&events); queryErr != nil || events != 1 {
		t.Fatalf("audit failure did not roll back deletion: events=%d error=%v", events, queryErr)
	}
	if queryErr := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action LIKE 'auth.backchannel_logout.retention.%'`).Scan(&audits); queryErr != nil || audits != 0 {
		t.Fatalf("failed batch left audit residue: audits=%d error=%v", audits, queryErr)
	}
}

func TestRetentionRejectsNonOwnerEvenWithDeleteGrant(t *testing.T) {
	pool, ctx := retentionIntegrationPool(t)
	const role = "retention_nonowner_test"
	const password = "retention-nonowner-test-password"
	_, _ = pool.Exec(ctx, `DROP OWNED BY retention_nonowner_test`)
	_, _ = pool.Exec(ctx, `DROP ROLE IF EXISTS retention_nonowner_test`)
	if _, err := pool.Exec(ctx, `CREATE ROLE retention_nonowner_test LOGIN PASSWORD 'retention-nonowner-test-password'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP OWNED BY retention_nonowner_test`)
		_, _ = pool.Exec(context.Background(), `DROP ROLE IF EXISTS retention_nonowner_test`)
	})
	var database string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatal(err)
	}
	grantSQL := fmt.Sprintf(`GRANT CONNECT ON DATABASE %s TO %s`, pgx.Identifier{database}.Sanitize(), pgx.Identifier{role}.Sanitize())
	if _, err := pool.Exec(ctx, grantSQL+`; GRANT USAGE ON SCHEMA public TO retention_nonowner_test; GRANT SELECT,INSERT,DELETE ON oidc_backchannel_logout_events,audit_events TO retention_nonowner_test`); err != nil {
		t.Fatal(err)
	}
	config := pool.Config().Copy()
	config.ConnConfig.User = role
	config.ConnConfig.Password = password
	config.MaxConns = 1
	config.MinConns = 0
	nonOwnerPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if err = nonOwnerPool.Ping(ctx); err != nil {
		nonOwnerPool.Close()
		t.Fatal(err)
	}
	_, err = Run(ctx, nonOwnerPool, Options{Retention: MinimumRetention})
	nonOwnerPool.Close()
	if !errors.Is(err, ErrNotOwner) {
		t.Fatalf("non-owner retention error=%v", err)
	}
}
