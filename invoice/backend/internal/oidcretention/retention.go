package oidcretention

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// MinimumRetention deliberately exceeds every accepted logout-token
	// lifetime by several orders of magnitude. An operator cannot lower it.
	MinimumRetention = 180 * 24 * time.Hour
	DefaultRetention = 365 * 24 * time.Hour
	maintenanceLock  = int64(0x4f494443524554) // "OIDCRET"
)

var (
	ErrInvalidOptions  = errors.New("invalid OIDC logout retention options")
	ErrNotOwner        = errors.New("OIDC logout retention requires the direct database and table owner")
	ErrMaintenanceBusy = errors.New("another OIDC logout retention operation holds the maintenance lock")
)

type Options struct {
	Retention            time.Duration
	BatchSize            int
	Execute              bool
	MaintenanceConfirmed bool
	Reason               string
	LockTimeout          time.Duration
	StatementTimeout     time.Duration
}

type Result struct {
	DatabaseNow time.Time
	Cutoff      time.Time
	Eligible    int64
	Deleted     int64
	Batches     int
	RequestID   string
}

// Run previews or deletes expired replay records through one owner connection.
// Every committed deletion batch contains its immutable audit row in the same
// transaction. Runtime application roles are rejected even if accidentally
// granted DELETE later.
func Run(ctx context.Context, pool *pgxpool.Pool, options Options) (Result, error) {
	options, err := normalizeOptions(options)
	if err != nil {
		return Result{}, err
	}
	if pool == nil {
		return Result{}, fmt.Errorf("%w: nil PostgreSQL pool", ErrInvalidOptions)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("acquire owner connection: %w", err)
	}
	defer conn.Release()
	if err = verifyDirectOwner(ctx, conn); err != nil {
		return Result{}, err
	}
	var locked bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, maintenanceLock).Scan(&locked); err != nil {
		return Result{}, fmt.Errorf("acquire retention maintenance lock: %w", err)
	}
	if !locked {
		return Result{}, ErrMaintenanceBusy
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, maintenanceLock)
	}()

	result := Result{}
	if err = conn.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&result.DatabaseNow); err != nil {
		return Result{}, fmt.Errorf("read database clock: %w", err)
	}
	result.DatabaseNow = result.DatabaseNow.UTC()
	result.Cutoff = result.DatabaseNow.Add(-options.Retention)
	result.Eligible, err = countEligible(ctx, conn, options, result.Cutoff)
	if err != nil {
		return Result{}, err
	}
	if !options.Execute {
		return result, nil
	}
	result.RequestID, err = randomUUID()
	if err != nil {
		return Result{}, fmt.Errorf("generate retention request ID: %w", err)
	}
	for batchNumber := 1; ; batchNumber++ {
		deleted, runErr := deleteBatch(ctx, conn, options, result, batchNumber)
		if runErr != nil {
			return result, runErr
		}
		if deleted == 0 {
			break
		}
		result.Deleted += deleted
		result.Batches++
	}
	return result, nil
}

func normalizeOptions(options Options) (Options, error) {
	if options.Retention == 0 {
		options.Retention = DefaultRetention
	}
	if options.BatchSize == 0 {
		options.BatchSize = 500
	}
	if options.LockTimeout == 0 {
		options.LockTimeout = 5 * time.Second
	}
	if options.StatementTimeout == 0 {
		options.StatementTimeout = 30 * time.Second
	}
	options.Reason = strings.TrimSpace(options.Reason)
	if options.Retention < MinimumRetention || options.Retention > 10*365*24*time.Hour ||
		options.BatchSize < 1 || options.BatchSize > 5000 ||
		options.LockTimeout < time.Second || options.LockTimeout > 30*time.Second ||
		options.StatementTimeout < 5*time.Second || options.StatementTimeout > 2*time.Minute {
		return Options{}, ErrInvalidOptions
	}
	if options.Execute && (!options.MaintenanceConfirmed || options.Reason == "" || len(options.Reason) > 256 || strings.ContainsAny(options.Reason, "\r\n\x00")) {
		return Options{}, fmt.Errorf("%w: execution requires --maintenance-confirmed and a single-line reason of at most 256 bytes", ErrInvalidOptions)
	}
	return options, nil
}

func verifyDirectOwner(ctx context.Context, conn *pgxpool.Conn) error {
	var directSession, databaseOwner, eventOwner, auditOwner bool
	err := conn.QueryRow(ctx, `
		SELECT current_user=session_user,
		       current_user=pg_get_userbyid(d.datdba),
		       current_user=pg_get_userbyid(events.relowner),
		       current_user=pg_get_userbyid(audits.relowner)
		FROM pg_database d
		JOIN pg_class events ON events.oid=to_regclass('public.oidc_backchannel_logout_events')
		JOIN pg_class audits ON audits.oid=to_regclass('public.audit_events')
		WHERE d.datname=current_database()`).Scan(&directSession, &databaseOwner, &eventOwner, &auditOwner)
	if err != nil {
		return fmt.Errorf("verify retention owner: %w", err)
	}
	if !directSession || !databaseOwner || !eventOwner || !auditOwner {
		return ErrNotOwner
	}
	return nil
}

func countEligible(ctx context.Context, conn *pgxpool.Conn, options Options, cutoff time.Time) (int64, error) {
	tx, err := beginConfiguredTx(ctx, conn, options)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var eligible int64
	// token_expires_at must itself precede the retention cutoff. This keeps a
	// replay record even if a malformed legacy row has an unexpectedly long
	// token lifetime, independent of received_at.
	if err = tx.QueryRow(ctx, `
		SELECT count(*) FROM oidc_backchannel_logout_events
		WHERE received_at < $1 AND token_expires_at < $1`, cutoff).Scan(&eligible); err != nil {
		return 0, fmt.Errorf("count eligible logout replay records: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit retention preview: %w", err)
	}
	return eligible, nil
}

func deleteBatch(ctx context.Context, conn *pgxpool.Conn, options Options, result Result, batchNumber int) (int64, error) {
	tx, err := beginConfiguredTx(ctx, conn, options)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var deleted int64
	if err = tx.QueryRow(ctx, `
		WITH candidates AS (
			SELECT id
			FROM oidc_backchannel_logout_events
			WHERE received_at < $1 AND token_expires_at < $1
			ORDER BY received_at,id
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		), removed AS (
			DELETE FROM oidc_backchannel_logout_events events
			USING candidates
			WHERE events.id=candidates.id
			RETURNING events.id
		)
		SELECT count(*) FROM removed`, result.Cutoff, options.BatchSize).Scan(&deleted); err != nil {
		return 0, fmt.Errorf("delete OIDC logout retention batch: %w", err)
	}
	action := "auth.backchannel_logout.retention.batch"
	metadata := fmt.Sprintf("%s | retention_hours=%d cutoff=%s batch=%d deleted=%d",
		options.Reason, int64(options.Retention/time.Hour), result.Cutoff.Format(time.RFC3339Nano), batchNumber, deleted)
	if deleted == 0 {
		action = "auth.backchannel_logout.retention.complete"
		metadata = fmt.Sprintf("%s | retention_hours=%d cutoff=%s total_deleted=%d batches=%d",
			options.Reason, int64(options.Retention/time.Hour), result.Cutoff.Format(time.RFC3339Nano), result.Deleted, result.Batches)
	}
	if len(metadata) > 500 {
		return 0, errors.New("bounded retention audit metadata exceeds 500 bytes")
	}
	auditID, err := randomUUID()
	if err != nil {
		return 0, fmt.Errorf("generate retention audit ID: %w", err)
	}
	objectID := fmt.Sprintf("%s/%06d", result.RequestID, batchNumber)
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\n%s\n%d\n%d", result.RequestID, result.Cutoff.Format(time.RFC3339Nano), batchNumber, deleted)))
	if _, err = tx.Exec(ctx, `
		INSERT INTO audit_events(
			id,actor_type,actor_id,action,object_type,object_id,request_id,
			after_hash,reason,created_at)
		VALUES($1,'system','oidc-logout-retention',$2,'maintenance_job',$3,$4,$5,$6,clock_timestamp())`,
		auditID, action, objectID, result.RequestID, hex.EncodeToString(digest[:]), metadata); err != nil {
		return 0, fmt.Errorf("audit OIDC logout retention batch: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit OIDC logout retention batch: %w", err)
	}
	return deleted, nil
}

func beginConfiguredTx(ctx context.Context, conn *pgxpool.Conn, options Options) (pgx.Tx, error) {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite})
	if err != nil {
		return nil, fmt.Errorf("begin retention transaction: %w", err)
	}
	if _, err = tx.Exec(ctx, `
		SELECT set_config('lock_timeout',$1,true),
		       set_config('statement_timeout',$2,true),
		       set_config('idle_in_transaction_session_timeout',$2,true)`,
		postgresDuration(options.LockTimeout), postgresDuration(options.StatementTimeout)); err != nil {
		_ = tx.Rollback(context.Background())
		return nil, fmt.Errorf("configure retention transaction timeouts: %w", err)
	}
	return tx, nil
}

func postgresDuration(value time.Duration) string {
	return fmt.Sprintf("%dms", value.Milliseconds())
}

func randomUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(raw[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
