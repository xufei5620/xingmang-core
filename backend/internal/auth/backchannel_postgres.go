package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresBackchannelLogoutRepository struct{ pool *pgxpool.Pool }

func NewPostgresBackchannelLogoutRepository(pool *pgxpool.Pool) *PostgresBackchannelLogoutRepository {
	return &PostgresBackchannelLogoutRepository{pool: pool}
}

func (r *PostgresBackchannelLogoutRepository) ApplyBackchannelLogout(ctx context.Context, event VerifiedBackchannelLogout, actor BackchannelLogoutActor) (BackchannelLogoutResult, error) {
	if r == nil || r.pool == nil {
		return BackchannelLogoutResult{}, errors.New("nil PostgreSQL back-channel logout repository")
	}
	issuerHash := sha256Hex(event.Issuer)
	jtiHash := sha256Hex(event.Issuer + "\n" + event.TokenID)
	sidHash := optionalHash(event.SessionID)
	subjectHash := ""
	if event.Subject != "" {
		subjectHash = optionalHash(event.Issuer + "\n" + event.Subject)
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return BackchannelLogoutResult{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,11))`, issuerHash+"\n"+jtiHash); err != nil {
		return BackchannelLogoutResult{}, err
	}
	var existing BackchannelLogoutResult
	err = tx.QueryRow(ctx, `
		SELECT id,revoked_session_count FROM oidc_backchannel_logout_events
		WHERE issuer_hash=$1 AND jti_hash=$2`, issuerHash, jtiHash).Scan(&existing.EventID, &existing.RevokedSessions)
	if err == nil {
		existing.Replay = true
		if err = tx.Commit(ctx); err != nil {
			return BackchannelLogoutResult{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return BackchannelLogoutResult{}, err
	}
	now := time.Now().UTC()
	var command pgconn.CommandTag
	if event.SessionID != "" {
		command, err = tx.Exec(ctx, `
			UPDATE auth_sessions SET revoked_at=$2,revoked_reason='oidc back-channel logout'
			WHERE provider_sid_hash=$1 AND revoked_at IS NULL`, sidHash, now)
	} else {
		command, err = tx.Exec(ctx, `
			UPDATE auth_sessions s SET revoked_at=$3,revoked_reason='oidc back-channel logout'
			FROM invoice_users u
			WHERE s.invoice_user_id=u.id AND u.oidc_issuer=$1 AND u.oidc_subject=$2
			  AND s.revoked_at IS NULL`, event.Issuer, event.Subject, now)
	}
	if err != nil {
		return BackchannelLogoutResult{}, err
	}
	result := BackchannelLogoutResult{EventID: randomUUIDv4(), RevokedSessions: command.RowsAffected()}
	_, err = tx.Exec(ctx, `
		INSERT INTO oidc_backchannel_logout_events(
			id,issuer_hash,jti_hash,sid_hash,subject_hash,token_issued_at,
			token_expires_at,received_at,revoked_session_count,request_id,source_ip_hmac
		) VALUES($1,$2,$3,NULLIF($4,''),NULLIF($5,''),$6,$7,$8,$9,$10,NULLIF($11,''))`,
		result.EventID, issuerHash, jtiHash, sidHash, subjectHash, event.IssuedAt,
		event.ExpiresAt, now, result.RevokedSessions, actor.RequestID, actor.SourceIPHash)
	if err != nil {
		return BackchannelLogoutResult{}, err
	}
	if err = insertSecurityAudit(ctx, tx, SecurityAuditEvent{
		ID: randomUUIDv4(), ActorType: "oidc", ActorID: issuerHash,
		Action: "auth.backchannel_logout.process", ObjectType: "oidc_backchannel_logout",
		ObjectID: result.EventID, RequestID: actor.RequestID, SourceIPHash: actor.SourceIPHash,
		AfterHash: sha256Hex(result.EventID + "\n" + jtiHash), Severity: SeverityNotice, CreatedAt: now,
	}); err != nil {
		return BackchannelLogoutResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return BackchannelLogoutResult{}, err
	}
	return result, nil
}
