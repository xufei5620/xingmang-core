package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresFlowStore struct{ pool *pgxpool.Pool }

func NewPostgresFlowStore(pool *pgxpool.Pool) *PostgresFlowStore {
	return &PostgresFlowStore{pool: pool}
}

func (s *PostgresFlowStore) Create(ctx context.Context, flow AuthorizationFlow) error {
	if s == nil || s.pool == nil {
		return errors.New("nil PostgreSQL flow store")
	}
	if err := validateFlow(flow); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO oidc_authorization_flows(
			state_hash,nonce_hash,browser_binding_hash,code_verifier_ciphertext,
			code_verifier_key_version,purpose,expected_identity_hash,
			existing_session_id,return_path,created_at,expires_at
		) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,''),NULLIF($8,'')::uuid,$9,$10,$11)`,
		flow.StateHash, flow.NonceHash, flow.BrowserBindingHash, flow.CodeVerifierCiphertext,
		flow.CodeVerifierKeyVersion, string(flow.Purpose), flow.ExpectedIdentityHash,
		flow.ExistingSessionID, flow.ReturnPath, flow.CreatedAt, flow.ExpiresAt)
	return err
}

func (s *PostgresFlowStore) Consume(ctx context.Context, stateHash, browserBindingHash string, now time.Time) (AuthorizationFlow, error) {
	if s == nil || s.pool == nil || len(stateHash) != 64 || len(browserBindingHash) != 64 {
		return AuthorizationFlow{}, ErrInvalidFlow
	}
	flow, err := scanAuthorizationFlow(s.pool.QueryRow(ctx, `
		UPDATE oidc_authorization_flows
		SET consumed_at=$3
		WHERE state_hash=$1 AND browser_binding_hash=$2
		  AND consumed_at IS NULL AND expires_at>$3
		RETURNING state_hash,nonce_hash,browser_binding_hash,code_verifier_ciphertext,
		          code_verifier_key_version,purpose,COALESCE(expected_identity_hash,''),
		          COALESCE(existing_session_id::text,''),return_path,created_at,expires_at,consumed_at`,
		stateHash, browserBindingHash, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthorizationFlow{}, ErrInvalidFlow
	}
	return flow, err
}

func (s *PostgresFlowStore) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("nil PostgreSQL flow store")
	}
	result, err := s.pool.Exec(ctx, `DELETE FROM oidc_authorization_flows WHERE expires_at<=$1 OR consumed_at<$1-interval '1 hour'`, now)
	return result.RowsAffected(), err
}

func scanAuthorizationFlow(row pgx.Row) (AuthorizationFlow, error) {
	var flow AuthorizationFlow
	var purpose string
	err := row.Scan(&flow.StateHash, &flow.NonceHash, &flow.BrowserBindingHash,
		&flow.CodeVerifierCiphertext, &flow.CodeVerifierKeyVersion, &purpose,
		&flow.ExpectedIdentityHash, &flow.ExistingSessionID, &flow.ReturnPath,
		&flow.CreatedAt, &flow.ExpiresAt, &flow.ConsumedAt)
	flow.Purpose = FlowPurpose(purpose)
	return flow, err
}

type PostgresSessionStore struct{ pool *pgxpool.Pool }

func NewPostgresSessionStore(pool *pgxpool.Pool) *PostgresSessionStore {
	return &PostgresSessionStore{pool: pool}
}

func (s *PostgresSessionStore) Create(ctx context.Context, session Session) error {
	if s == nil || s.pool == nil {
		return errors.New("nil PostgreSQL session store")
	}
	if err := validateSessionRecord(session); err != nil {
		return err
	}
	result, err := s.pool.Exec(ctx, insertSessionSQL,
		session.ID, session.FamilyID, session.UserID, session.Issuer, session.Subject,
		session.TokenHash, session.CSRFHash, nullableString(session.ProviderSIDHash), session.Roles,
		session.ACR, session.AMR, nullableTime(session.AuthTime), session.MFAAt,
		nullableString(session.ClientIPHash), nullableString(session.UserAgentHash), session.CreatedAt,
		session.LastSeenAt, session.IdleExpiresAt, session.AbsoluteExpiresAt, nullableString(session.RotatedFrom))
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrIdentityMismatch
	}
	return nil
}

const insertSessionSQL = `
	INSERT INTO auth_sessions(
		id,family_id,invoice_user_id,token_hash,csrf_hash,provider_sid_hash,
		roles,acr,amr,auth_time,mfa_at,client_ip_hmac,user_agent_hmac,
		created_at,last_seen_at,idle_expires_at,absolute_expires_at,rotated_from
	)
	SELECT $1,$2,u.id,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,NULLIF($20,'')::uuid
	FROM invoice_users u
	WHERE u.id=$3 AND u.oidc_issuer=$4 AND u.oidc_subject=$5 AND u.status='active'`

func (s *PostgresSessionStore) AuthenticateAndTouch(ctx context.Context, tokenHash string, binding ClientBinding, now time.Time, idleTTL time.Duration) (Session, error) {
	if s == nil || s.pool == nil || len(tokenHash) != 64 {
		return Session{}, ErrSessionInvalid
	}
	desiredIdleExpiry := now.Add(idleTTL)
	session, err := scanSession(s.pool.QueryRow(ctx, `
		UPDATE auth_sessions s
		SET last_seen_at=$4,idle_expires_at=LEAST(s.absolute_expires_at,$5)
		FROM invoice_users u
		WHERE s.token_hash=$1 AND s.invoice_user_id=u.id AND u.status='active'
		  AND s.revoked_at IS NULL AND s.idle_expires_at>$4 AND s.absolute_expires_at>$4
		  AND (s.client_ip_hmac IS NULL OR s.client_ip_hmac=$2)
		  AND (s.user_agent_hmac IS NULL OR s.user_agent_hmac=$3)
		RETURNING s.id,s.family_id,s.invoice_user_id,u.oidc_issuer,u.oidc_subject,
		          COALESCE(u.platform,''),COALESCE(u.platform_user_id,''),
		          s.token_hash,s.csrf_hash,COALESCE(s.provider_sid_hash,''),s.roles,s.acr,s.amr,
		          s.auth_time,s.mfa_at,COALESCE(s.client_ip_hmac,''),COALESCE(s.user_agent_hmac,''),
		          s.created_at,s.last_seen_at,s.idle_expires_at,s.absolute_expires_at,
		          COALESCE(s.rotated_from::text,''),s.revoked_at,s.revoked_reason`,
		tokenHash, nullableString(binding.IPHash), nullableString(binding.UserAgentHash), now, desiredIdleExpiry))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrSessionInvalid
	}
	return session, err
}

func (s *PostgresSessionStore) Rotate(ctx context.Context, oldTokenHash, expectedSessionID string, next Session, now time.Time) (Session, error) {
	if s == nil || s.pool == nil {
		return Session{}, errors.New("nil PostgreSQL session store")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Session{}, err
	}
	defer tx.Rollback(context.Background())
	old, err := scanSession(tx.QueryRow(ctx, `
		SELECT s.id,s.family_id,s.invoice_user_id,u.oidc_issuer,u.oidc_subject,
		       COALESCE(u.platform,''),COALESCE(u.platform_user_id,''),
		       s.token_hash,s.csrf_hash,COALESCE(s.provider_sid_hash,''),s.roles,s.acr,s.amr,
		       s.auth_time,s.mfa_at,COALESCE(s.client_ip_hmac,''),COALESCE(s.user_agent_hmac,''),
		       s.created_at,s.last_seen_at,s.idle_expires_at,s.absolute_expires_at,
		       COALESCE(s.rotated_from::text,''),s.revoked_at,s.revoked_reason
		FROM auth_sessions s JOIN invoice_users u ON u.id=s.invoice_user_id
		WHERE s.token_hash=$1 AND s.id=$2 AND s.revoked_at IS NULL
		  AND s.idle_expires_at>$3 AND s.absolute_expires_at>$3 AND u.status='active'
		FOR UPDATE OF s`, oldTokenHash, expectedSessionID, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrSessionInvalid
	}
	if err != nil {
		return Session{}, err
	}
	if old.Issuer != next.Issuer || old.Subject != next.Subject || old.Platform != next.Platform || old.PlatformUserID != next.PlatformUserID {
		return Session{}, ErrIdentityMismatch
	}
	if _, err = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=$2,revoked_reason='rotated' WHERE id=$1 AND revoked_at IS NULL`, old.ID, now); err != nil {
		return Session{}, err
	}
	next.FamilyID = old.FamilyID
	next.UserID = old.UserID
	next.AbsoluteExpiresAt = old.AbsoluteExpiresAt
	if next.IdleExpiresAt.After(next.AbsoluteExpiresAt) {
		next.IdleExpiresAt = next.AbsoluteExpiresAt
	}
	next.RotatedFrom = old.ID
	if err = validateSessionRecord(next); err != nil {
		return Session{}, err
	}
	result, err := tx.Exec(ctx, insertSessionSQL,
		next.ID, next.FamilyID, next.UserID, next.Issuer, next.Subject,
		next.TokenHash, next.CSRFHash, nullableString(next.ProviderSIDHash), next.Roles,
		next.ACR, next.AMR, nullableTime(next.AuthTime), next.MFAAt,
		nullableString(next.ClientIPHash), nullableString(next.UserAgentHash), next.CreatedAt,
		next.LastSeenAt, next.IdleExpiresAt, next.AbsoluteExpiresAt, next.RotatedFrom)
	if err != nil {
		return Session{}, err
	}
	if result.RowsAffected() != 1 {
		return Session{}, ErrIdentityMismatch
	}
	if err = tx.Commit(ctx); err != nil {
		return Session{}, err
	}
	return next, nil
}

func (s *PostgresSessionStore) RevokeToken(ctx context.Context, tokenHash string, now time.Time, reason string) (Session, error) {
	if s == nil || s.pool == nil {
		return Session{}, errors.New("nil PostgreSQL session store")
	}
	session, err := scanSession(s.pool.QueryRow(ctx, `
		UPDATE auth_sessions s SET revoked_at=$2,revoked_reason=$3
		FROM invoice_users u
		WHERE s.token_hash=$1 AND s.invoice_user_id=u.id AND s.revoked_at IS NULL
		RETURNING s.id,s.family_id,s.invoice_user_id,u.oidc_issuer,u.oidc_subject,
		          COALESCE(u.platform,''),COALESCE(u.platform_user_id,''),
		          s.token_hash,s.csrf_hash,COALESCE(s.provider_sid_hash,''),s.roles,s.acr,s.amr,
		          s.auth_time,s.mfa_at,COALESCE(s.client_ip_hmac,''),COALESCE(s.user_agent_hmac,''),
		          s.created_at,s.last_seen_at,s.idle_expires_at,s.absolute_expires_at,
		          COALESCE(s.rotated_from::text,''),s.revoked_at,s.revoked_reason`, tokenHash, now, reason))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrSessionInvalid
	}
	return session, err
}

func (s *PostgresSessionStore) RevokeFamily(ctx context.Context, familyID string, now time.Time, reason string) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("nil PostgreSQL session store")
	}
	result, err := s.pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at=$2,revoked_reason=$3 WHERE family_id=$1 AND revoked_at IS NULL`, familyID, now, reason)
	return result.RowsAffected(), err
}

func (s *PostgresSessionStore) RevokeUser(ctx context.Context, userID string, now time.Time, reason string) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("nil PostgreSQL session store")
	}
	result, err := s.pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at=$2,revoked_reason=$3 WHERE invoice_user_id=$1 AND revoked_at IS NULL`, userID, now, reason)
	return result.RowsAffected(), err
}

func (s *PostgresSessionStore) DeleteExpired(ctx context.Context, before time.Time) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("nil PostgreSQL session store")
	}
	// A rotated child references its predecessor. Preserve every ancestor of a
	// session that is not yet eligible, then prune only fully stale chains.
	result, err := s.pool.Exec(ctx, `
		WITH RECURSIVE retained(id,rotated_from) AS (
			SELECT id,rotated_from
			FROM auth_sessions
			WHERE absolute_expires_at >= $1
			  AND (revoked_at IS NULL OR revoked_at >= $1)
			UNION
			SELECT parent.id,parent.rotated_from
			FROM auth_sessions parent
			JOIN retained child ON child.rotated_from=parent.id
		)
		DELETE FROM auth_sessions session
		WHERE (session.absolute_expires_at<$1 OR session.revoked_at<$1)
		  AND NOT EXISTS (SELECT 1 FROM retained WHERE retained.id=session.id)`, before)
	return result.RowsAffected(), err
}

func scanSession(row pgx.Row) (Session, error) {
	var session Session
	var authTime *time.Time
	var platform string
	err := row.Scan(
		&session.ID, &session.FamilyID, &session.UserID, &session.Issuer, &session.Subject,
		&platform, &session.PlatformUserID,
		&session.TokenHash, &session.CSRFHash, &session.ProviderSIDHash, &session.Roles,
		&session.ACR, &session.AMR, &authTime, &session.MFAAt,
		&session.ClientIPHash, &session.UserAgentHash, &session.CreatedAt,
		&session.LastSeenAt, &session.IdleExpiresAt, &session.AbsoluteExpiresAt,
		&session.RotatedFrom, &session.RevokedAt, &session.RevokedReason,
	)
	session.Platform = Platform(platform)
	if authTime != nil {
		session.AuthTime = authTime.UTC()
	}
	return session, err
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

type PostgresSecurityAuditSink struct{ pool *pgxpool.Pool }

func NewPostgresSecurityAuditSink(pool *pgxpool.Pool) *PostgresSecurityAuditSink {
	return &PostgresSecurityAuditSink{pool: pool}
}

func (s *PostgresSecurityAuditSink) RecordSecurityEvent(ctx context.Context, event SecurityAuditEvent) error {
	if s == nil || s.pool == nil {
		return errors.New("nil PostgreSQL security audit sink")
	}
	if event.ID == "" {
		event.ID = randomUUIDv4()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	if err := event.Validate(); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audit_events(
			id,actor_type,actor_id,action,object_type,object_id,request_id,
			source_ip_hmac,before_hash,after_hash,reason,severity,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),NULLIF($9,''),NULLIF($10,''),$11,$12,$13)`,
		event.ID, event.ActorType, event.ActorID, event.Action, event.ObjectType,
		event.ObjectID, event.RequestID, event.SourceIPHash, event.BeforeHash,
		event.AfterHash, event.Reason, string(event.Severity), event.CreatedAt)
	return err
}

type PostgresBindingProofStore struct{ pool *pgxpool.Pool }

func NewPostgresBindingProofStore(pool *pgxpool.Pool) *PostgresBindingProofStore {
	return &PostgresBindingProofStore{pool: pool}
}

func (s *PostgresBindingProofStore) CreateBindingChallenge(ctx context.Context, challenge BindingChallenge) error {
	if s == nil || s.pool == nil {
		return errors.New("nil PostgreSQL binding-proof store")
	}
	if err := challenge.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(challenge.RequestID) == "" {
		return errors.New("binding challenge request ID is required")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO external_account_binding_proofs(
			id,invoice_user_id,source_instance_id,external_user_id,proof_method,
			challenge_hash,status,request_id,created_at,expires_at
		) VALUES($1,$2,$3,$4,$5,$6,'pending',$7,$8,$9)`,
		challenge.ID, challenge.InvoiceUserID, challenge.SourceInstanceID,
		challenge.ExternalUserID, string(challenge.Method), challenge.ChallengeHash,
		challenge.RequestID, challenge.CreatedAt, challenge.ExpiresAt)
	return err
}

func (s *PostgresBindingProofStore) GetActiveBindingChallenge(ctx context.Context, challengeID string, now time.Time) (BindingChallenge, error) {
	if s == nil || s.pool == nil {
		return BindingChallenge{}, errors.New("nil PostgreSQL binding-proof store")
	}
	var challenge BindingChallenge
	var method string
	err := s.pool.QueryRow(ctx, `
		SELECT id,invoice_user_id,source_instance_id,external_user_id,proof_method,
		       challenge_hash,request_id,created_at,expires_at,consumed_at
		FROM external_account_binding_proofs
		WHERE id=$1 AND status='pending' AND expires_at>$2`, challengeID, now).Scan(
		&challenge.ID, &challenge.InvoiceUserID, &challenge.SourceInstanceID,
		&challenge.ExternalUserID, &method, &challenge.ChallengeHash, &challenge.RequestID,
		&challenge.CreatedAt, &challenge.ExpiresAt, &challenge.ConsumedAt)
	challenge.Method = BindingProofMethod(method)
	if errors.Is(err, pgx.ErrNoRows) {
		return BindingChallenge{}, ErrInvalidFlow
	}
	return challenge, err
}

func (s *PostgresBindingProofStore) ConsumeVerifiedBindingProof(ctx context.Context, proof VerifiedBindingProof, requestID string) error {
	if s == nil || s.pool == nil {
		return errors.New("nil PostgreSQL binding-proof store")
	}
	if err := validateVerifiedBindingProof(proof); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	result, err := tx.Exec(ctx, `
		UPDATE external_account_binding_proofs
		SET status='verified',evidence_hash=$2,source_revision_hash=$3,
		    request_id=$4,verifier_actor_id=$5,verified_at=$6,consumed_at=$6
		WHERE id=$1 AND invoice_user_id=$7 AND source_instance_id=$8
		  AND external_user_id=$9 AND proof_method=$10
		  AND status='pending' AND expires_at>$6`,
		proof.ChallengeID, proof.EvidenceHash, proof.SourceRevisionHash, requestID,
		proof.SourceInstanceID, proof.VerifiedAt, proof.InvoiceUserID,
		proof.SourceInstanceID, proof.ExternalUserID, string(proof.Method))
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrInvalidFlow
	}
	if err = insertSecurityAudit(ctx, tx, SecurityAuditEvent{
		ID: randomUUIDv4(), ActorType: "source_connector", ActorID: proof.SourceInstanceID,
		Action: "external_account.binding_proof.verify", ObjectType: "binding_proof",
		ObjectID: proof.ChallengeID, RequestID: requestID, AfterHash: proof.EvidenceHash,
		Severity: SeverityNotice, CreatedAt: proof.VerifiedAt,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresBindingProofStore) RejectBindingProof(ctx context.Context, challengeID, requestID, reason string, now time.Time) error {
	if s == nil || s.pool == nil || strings.TrimSpace(reason) == "" || len(reason) > 500 {
		return errors.New("binding proof rejection requires a bounded reason")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	result, err := tx.Exec(ctx, `
		UPDATE external_account_binding_proofs
		SET status='rejected',request_id=$2,verifier_actor_id='binding-verifier',
		    rejection_reason=$3,consumed_at=$4
		WHERE id=$1 AND status='pending'`, challengeID, requestID, reason, now)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrInvalidFlow
	}
	if err = insertSecurityAudit(ctx, tx, SecurityAuditEvent{
		ID: randomUUIDv4(), ActorType: "system", ActorID: "binding-verifier",
		Action: "external_account.binding_proof.reject", ObjectType: "binding_proof",
		ObjectID: challengeID, RequestID: requestID, Reason: reason,
		Severity: SeverityWarning, CreatedAt: now,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func validateVerifiedBindingProof(proof VerifiedBindingProof) error {
	if !validUUIDString(proof.ChallengeID) || !validUUIDString(proof.InvoiceUserID) || !validUUIDString(proof.SourceInstanceID) || strings.TrimSpace(proof.ExternalUserID) == "" || !proof.Method.Valid() || len(proof.EvidenceHash) != 64 || proof.VerifiedAt.IsZero() {
		return errors.New("verified external-account binding proof is invalid")
	}
	return nil
}

func insertSecurityAudit(ctx context.Context, tx pgx.Tx, event SecurityAuditEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO audit_events(
			id,actor_type,actor_id,action,object_type,object_id,request_id,
			source_ip_hmac,before_hash,after_hash,reason,severity,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),NULLIF($9,''),NULLIF($10,''),$11,$12,$13)`,
		event.ID, event.ActorType, event.ActorID, event.Action, event.ObjectType,
		event.ObjectID, event.RequestID, event.SourceIPHash, event.BeforeHash,
		event.AfterHash, event.Reason, string(event.Severity), event.CreatedAt)
	return err
}
