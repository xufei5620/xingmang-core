package postgresstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"invoice-system/backend/internal/domain"
)

// GetEnabledSourceInstanceID resolves the one enabled source instance for a
// source type (sub2api/newapi). Platform-password login (see
// backend/internal/auth/platform_login.go) uses this to bind a fresh identity
// to the deployment's actual bootstrapped source instance instead of a
// second, independently-configured ID that could drift out of sync with it.
func (s *Store) GetEnabledSourceInstanceID(ctx context.Context, sourceType string) (string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM source_instances WHERE source_type=$1 AND enabled LIMIT 2`, sourceType)
	if err != nil {
		return "", fmt.Errorf("resolve enabled source instance for %s: %w", sourceType, err)
	}
	defer rows.Close()
	var id string
	count := 0
	for rows.Next() {
		if err = rows.Scan(&id); err != nil {
			return "", err
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if count != 1 {
		return "", fmt.Errorf("expected exactly one enabled %s source instance, found %d", sourceType, count)
	}
	return id, nil
}

func (s *Store) UpsertSourceInstance(ctx context.Context, in SourceInstanceRecord) (SourceInstanceRecord, error) {
	if in.ID == "" {
		in.ID = randomUUID()
	}
	if !in.SourceType.Valid() || strings.TrimSpace(in.Name) == "" {
		return SourceInstanceRecord{}, errors.New("valid source type and name are required")
	}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO source_instances(id,source_type,name,runtime_version,enabled)
		VALUES($1,$2,$3,$4,$5)
		ON CONFLICT(id) DO UPDATE SET
			name=EXCLUDED.name,runtime_version=EXCLUDED.runtime_version,
			runtime_version_revision=CASE
				WHEN source_instances.runtime_version IS DISTINCT FROM EXCLUDED.runtime_version
				THEN source_instances.runtime_version_revision+1
				ELSE source_instances.runtime_version_revision END,
			enabled=EXCLUDED.enabled,updated_at=now()
		WHERE source_instances.source_type=EXCLUDED.source_type
		RETURNING id,source_type,name,runtime_version,runtime_version_revision,enabled,created_at,updated_at`,
		in.ID, in.SourceType, strings.TrimSpace(in.Name), strings.TrimSpace(in.RuntimeVersion), in.Enabled).Scan(
		&in.ID, &in.SourceType, &in.Name, &in.RuntimeVersion, &in.RuntimeVersionRevision,
		&in.Enabled, &in.CreatedAt, &in.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SourceInstanceRecord{}, domain.ErrConflict
	}
	if err != nil {
		return SourceInstanceRecord{}, fmt.Errorf("upsert source instance: %w", err)
	}
	return in, nil
}

// ApproveSourceInstance is the deployment-only, audited CAS boundary for a
// source runtime upgrade.  A changed runtime version is accepted only when the
// operator supplied the exact previously approved version.
func (s *Store) ApproveSourceInstance(ctx context.Context, in SourceInstanceRecord, expectedPrevious *string, actor AuditActor) (SourceInstanceRecord, error) {
	if in.ID == "" || !in.SourceType.Valid() || strings.TrimSpace(in.Name) == "" ||
		strings.TrimSpace(in.RuntimeVersion) == "" || len(in.RuntimeVersion) > 128 {
		return SourceInstanceRecord{}, errors.New("complete source identity and approved runtime version are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return SourceInstanceRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var currentVersion, currentType string
	lookupErr := tx.QueryRow(ctx, `SELECT source_type,runtime_version FROM source_instances WHERE id=$1 FOR UPDATE`, in.ID).Scan(&currentType, &currentVersion)
	if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
		return SourceInstanceRecord{}, lookupErr
	}
	if lookupErr == nil {
		if currentType != string(in.SourceType) {
			return SourceInstanceRecord{}, domain.ErrConflict
		}
		if currentVersion != in.RuntimeVersion {
			if expectedPrevious == nil || *expectedPrevious != currentVersion {
				return SourceInstanceRecord{}, domain.ErrVersionConflict
			}
		} else if expectedPrevious != nil && *expectedPrevious != currentVersion {
			return SourceInstanceRecord{}, domain.ErrVersionConflict
		}
	} else if expectedPrevious != nil {
		return SourceInstanceRecord{}, domain.ErrVersionConflict
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO source_instances(id,source_type,name,runtime_version,enabled)
		VALUES($1,$2,$3,$4,$5)
		ON CONFLICT(id) DO UPDATE SET
			name=EXCLUDED.name,
			runtime_version_revision=CASE
				WHEN source_instances.runtime_version IS DISTINCT FROM EXCLUDED.runtime_version
				THEN source_instances.runtime_version_revision+1
				ELSE source_instances.runtime_version_revision END,
			runtime_version=EXCLUDED.runtime_version,enabled=EXCLUDED.enabled,updated_at=now()
		WHERE source_instances.source_type=EXCLUDED.source_type
		RETURNING id,source_type,name,runtime_version,runtime_version_revision,enabled,created_at,updated_at`,
		in.ID, in.SourceType, strings.TrimSpace(in.Name), strings.TrimSpace(in.RuntimeVersion), in.Enabled).Scan(
		&in.ID, &in.SourceType, &in.Name, &in.RuntimeVersion, &in.RuntimeVersionRevision,
		&in.Enabled, &in.CreatedAt, &in.UpdatedAt)
	if err != nil {
		return SourceInstanceRecord{}, err
	}
	action := "source_instance.created"
	if lookupErr == nil {
		action = "source_instance.configuration_approved"
		if currentVersion != in.RuntimeVersion {
			action = "source_instance.runtime_version_approved"
		}
	}
	if err = writeAudit(ctx, tx, actor, action, "source_instance", in.ID,
		map[string]any{"runtime_version": currentVersion},
		map[string]any{"runtime_version": in.RuntimeVersion, "revision": in.RuntimeVersionRevision, "enabled": in.Enabled}); err != nil {
		return SourceInstanceRecord{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return SourceInstanceRecord{}, err
	}
	return in, nil
}

// EnsureUser maps a verified OIDC issuer+subject pair to the internal UUID used
// by every foreign key.  An unverified login can never erase a previously
// verified delivery address.
func (s *Store) EnsureUser(ctx context.Context, in UserRecord, actor AuditActor) (UserRecord, error) {
	if in.ID == "" {
		in.ID = randomUUID()
	}
	if strings.TrimSpace(in.OIDCIssuer) == "" || strings.TrimSpace(in.OIDCSubject) == "" {
		return UserRecord{}, errors.New("OIDC issuer and subject are required")
	}
	if in.Status == "" {
		in.Status = "active"
	}
	if in.EmailVerified && len(in.EmailCiphertext) == 0 {
		return UserRecord{}, errors.New("verified OIDC email ciphertext is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return UserRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var existingID string
	lookupErr := tx.QueryRow(ctx, `SELECT id FROM invoice_users WHERE oidc_issuer=$1 AND oidc_subject=$2 FOR UPDATE`, in.OIDCIssuer, in.OIDCSubject).Scan(&existingID)
	if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
		return UserRecord{}, fmt.Errorf("lock invoice user identity: %w", lookupErr)
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO invoice_users(id,oidc_issuer,oidc_subject,status,email_ciphertext,email_verified)
		VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT(oidc_issuer,oidc_subject) DO UPDATE SET
			status=invoice_users.status,
			email_ciphertext=CASE WHEN EXCLUDED.email_verified THEN EXCLUDED.email_ciphertext ELSE invoice_users.email_ciphertext END,
			email_verified=invoice_users.email_verified OR EXCLUDED.email_verified,
			updated_at=now()
		RETURNING id,oidc_issuer,oidc_subject,status,COALESCE(email_ciphertext,''::bytea),email_verified,created_at,updated_at`,
		in.ID, strings.TrimSpace(in.OIDCIssuer), strings.TrimSpace(in.OIDCSubject), in.Status,
		nullableBytes(in.EmailCiphertext), in.EmailVerified).Scan(
		&in.ID, &in.OIDCIssuer, &in.OIDCSubject, &in.Status, &in.EmailCiphertext,
		&in.EmailVerified, &in.CreatedAt, &in.UpdatedAt)
	if err != nil {
		return UserRecord{}, fmt.Errorf("ensure invoice user: %w", err)
	}
	action := "user.created"
	if existingID != "" {
		action = "user.refreshed"
	}
	if err = writeAudit(ctx, tx, actor, action, "invoice_user", in.ID, nil, map[string]any{
		"status": in.Status, "email_verified": in.EmailVerified,
	}); err != nil {
		return UserRecord{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return UserRecord{}, err
	}
	return in, nil
}

type OIDCVerifiedEmailBuilder func(principalID string) (*VerifiedEmailRecord, error)

// EnsureUserAndSyncOIDCEmail commits the user refresh, current OIDC email
// upsert, and revocation of superseded OIDC emails in one transaction.
func (s *Store) EnsureUserAndSyncOIDCEmail(ctx context.Context, in UserRecord, build OIDCVerifiedEmailBuilder, actor AuditActor) (UserRecord, error) {
	if strings.TrimSpace(in.OIDCIssuer) == "" || strings.TrimSpace(in.OIDCSubject) == "" || build == nil {
		return UserRecord{}, errors.New("OIDC issuer, subject and email builder are required")
	}
	if in.ID == "" {
		in.ID = randomUUID()
	}
	if in.Status == "" {
		in.Status = "active"
	}
	if in.EmailVerified && len(in.EmailCiphertext) == 0 {
		return UserRecord{}, errors.New("verified OIDC email ciphertext is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return UserRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	identityKey := strings.TrimSpace(in.OIDCIssuer) + "\n" + strings.TrimSpace(in.OIDCSubject)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,7))`, identityKey); err != nil {
		return UserRecord{}, err
	}
	var existingID string
	lookupErr := tx.QueryRow(ctx, `SELECT id FROM invoice_users WHERE oidc_issuer=$1 AND oidc_subject=$2 FOR UPDATE`, in.OIDCIssuer, in.OIDCSubject).Scan(&existingID)
	if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
		return UserRecord{}, lookupErr
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO invoice_users(id,oidc_issuer,oidc_subject,status,email_ciphertext,email_verified)
		VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT(oidc_issuer,oidc_subject) DO UPDATE SET
			status=invoice_users.status,
			email_ciphertext=CASE WHEN EXCLUDED.email_verified THEN EXCLUDED.email_ciphertext ELSE invoice_users.email_ciphertext END,
			email_verified=invoice_users.email_verified OR EXCLUDED.email_verified,
			updated_at=now()
		RETURNING id,oidc_issuer,oidc_subject,status,COALESCE(email_ciphertext,''::bytea),email_verified,created_at,updated_at`,
		in.ID, strings.TrimSpace(in.OIDCIssuer), strings.TrimSpace(in.OIDCSubject), in.Status,
		nullableBytes(in.EmailCiphertext), in.EmailVerified).Scan(
		&in.ID, &in.OIDCIssuer, &in.OIDCSubject, &in.Status, &in.EmailCiphertext,
		&in.EmailVerified, &in.CreatedAt, &in.UpdatedAt)
	if err != nil {
		return UserRecord{}, err
	}
	current, err := build(in.ID)
	if err != nil {
		return UserRecord{}, err
	}
	revoked, err := syncOIDCVerifiedEmailTx(ctx, tx, in.ID, current)
	if err != nil {
		return UserRecord{}, err
	}
	action := "user.created"
	if existingID != "" {
		action = "user.refreshed"
	}
	if err = writeAudit(ctx, tx, actor, action, "invoice_user", in.ID, nil,
		map[string]any{"status": in.Status, "email_verified": current != nil}); err != nil {
		return UserRecord{}, err
	}
	if err = writeAudit(ctx, tx, actor, "verified_email.oidc_synchronized", "invoice_user", in.ID, nil,
		map[string]any{"oidc_email_active": current != nil, "revoked_count": revoked}); err != nil {
		return UserRecord{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return UserRecord{}, err
	}
	in.EmailVerified = current != nil
	if current == nil {
		in.EmailCiphertext = nil
	}
	return in, nil
}

func (s *Store) GetUser(ctx context.Context, principalID string) (UserRecord, error) {
	var out UserRecord
	err := s.pool.QueryRow(ctx, `
		SELECT id,oidc_issuer,oidc_subject,status,COALESCE(email_ciphertext,''::bytea),
			email_verified,created_at,updated_at
		FROM invoice_users WHERE id=$1`, principalID).Scan(
		&out.ID, &out.OIDCIssuer, &out.OIDCSubject, &out.Status, &out.EmailCiphertext,
		&out.EmailVerified, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserRecord{}, domain.ErrNotFound
	}
	if err != nil {
		return UserRecord{}, fmt.Errorf("get invoice user: %w", err)
	}
	return out, nil
}

func (s *Store) FindUserByOIDC(ctx context.Context, issuer, subject string) (UserRecord, error) {
	var out UserRecord
	err := s.pool.QueryRow(ctx, `
		SELECT id,oidc_issuer,oidc_subject,status,COALESCE(email_ciphertext,''::bytea),
			email_verified,created_at,updated_at
		FROM invoice_users WHERE oidc_issuer=$1 AND oidc_subject=$2`,
		strings.TrimRight(strings.TrimSpace(issuer), "/"), strings.TrimSpace(subject)).Scan(
		&out.ID, &out.OIDCIssuer, &out.OIDCSubject, &out.Status, &out.EmailCiphertext,
		&out.EmailVerified, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserRecord{}, domain.ErrNotFound
	}
	return out, err
}

func (s *Store) GetExternalAccountBySourceUser(ctx context.Context, sourceInstanceID, externalUserID string) (ExternalAccountRecord, error) {
	var out ExternalAccountRecord
	err := s.pool.QueryRow(ctx, `
		SELECT id,invoice_user_id,source_instance_id,external_user_id,
			COALESCE(external_subject_hmac,''),binding_method,binding_status,
			COALESCE(verified_at,'epoch'::timestamptz),created_at,updated_at
		FROM external_accounts WHERE source_instance_id=$1 AND external_user_id=$2`,
		sourceInstanceID, externalUserID).Scan(&out.ID, &out.PrincipalID,
		&out.SourceInstanceID, &out.ExternalUserID, &out.ExternalSubjectHMAC,
		&out.BindingMethod, &out.BindingStatus, &out.VerifiedAt, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ExternalAccountRecord{}, domain.ErrNotFound
	}
	if out.VerifiedAt.Equal(time.Unix(0, 0).UTC()) {
		out.VerifiedAt = time.Time{}
	}
	return out, err
}

// RevokeExternalAccountFromSource applies a signed, repeated-miss identity
// tombstone.  It revokes the binding, freezes all associated funding lots and
// releases/rejects every not-yet-issued request that depended on the binding.
func (s *Store) RevokeExternalAccountFromSource(ctx context.Context, sourceInstanceID, externalUserID, sourceRevision string, observedAt time.Time, sourceSequence int64, actor AuditActor) error {
	if strings.TrimSpace(sourceInstanceID) == "" || strings.TrimSpace(externalUserID) == "" ||
		strings.TrimSpace(sourceRevision) == "" || observedAt.IsZero() || sourceSequence <= 0 {
		return errors.New("complete identity tombstone evidence is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,4))`, sourceInstanceID+"\n"+externalUserID); err != nil {
		return err
	}
	var accountID, principalID, beforeStatus string
	var currentSourceTime time.Time
	var currentSourceSequence int64
	err = tx.QueryRow(ctx, `
		SELECT id,invoice_user_id,binding_status,source_last_observed_at,source_last_sequence FROM external_accounts
		WHERE source_instance_id=$1 AND external_user_id=$2 FOR UPDATE`,
		sourceInstanceID, externalUserID).Scan(&accountID, &principalID, &beforeStatus, &currentSourceTime, &currentSourceSequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return err
	}
	if observedAt.Before(currentSourceTime) || observedAt.Equal(currentSourceTime) && sourceSequence <= currentSourceSequence {
		if err = writeAudit(ctx, tx, actor, "external_account.stale_tombstone_ignored", "external_account", accountID,
			map[string]any{"source_time": currentSourceTime, "source_sequence": currentSourceSequence},
			map[string]any{"ignored_source_time": observedAt, "ignored_source_sequence": sourceSequence}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	rows, err := tx.Query(ctx, `
		SELECT ir.id,ir.status,ir.version
		FROM invoice_requests ir
		WHERE ir.id IN (
			SELECT ia.invoice_request_id FROM funding_lots fl
			JOIN invoice_allocations ia ON ia.funding_lot_id=fl.id AND ia.allocation_state='reserved'
			WHERE fl.external_account_id=$1
		) AND ir.status IN ('pending_review','needs_changes','approved','manual_issuing')
		ORDER BY ir.id FOR UPDATE OF ir`, accountID)
	if err != nil {
		return err
	}
	type pendingRequest struct {
		id      string
		status  domain.RequestStatus
		version int64
	}
	pending := make([]pendingRequest, 0)
	for rows.Next() {
		var item pendingRequest
		if err = rows.Scan(&item.id, &item.status, &item.version); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range pending {
		if err = releaseReservations(ctx, tx, item.id); err != nil {
			return err
		}
		command, updateErr := tx.Exec(ctx, `
			UPDATE invoice_requests SET status='rejected',review_note='source identity tombstone invalidated this unissued request',
				version=version+1,updated_at=now() WHERE id=$1 AND version=$2`, item.id, item.version)
		if updateErr != nil {
			return updateErr
		}
		if command.RowsAffected() != 1 {
			return domain.ErrVersionConflict
		}
		if err = writeAudit(ctx, tx, actor, "invoice_request.invalidated_by_identity_tombstone", "invoice_request", item.id,
			map[string]any{"status": item.status}, map[string]any{"status": domain.StatusRejected}); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `
		UPDATE funding_lots SET verification_state='frozen',current_cap_minor=0,
			source_status='tombstone:identity_missing',source_revision_hash=$1,
			observed_at=$2,
			source_last_sequence=CASE WHEN $2>source_last_observed_at THEN 0 ELSE source_last_sequence END,
			source_last_observed_at=greatest(source_last_observed_at,$2),updated_at=now()
		WHERE external_account_id=$3`, sourceRevision, observedAt.UTC(), accountID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE external_accounts SET binding_status='revoked',source_last_observed_at=$2,
			source_last_sequence=$3,updated_at=now()
		WHERE id=$1`, accountID, observedAt.UTC(), sourceSequence); err != nil {
		return err
	}
	if err = writeAudit(ctx, tx, actor, "external_account.revoked_by_source_tombstone", "external_account", accountID,
		map[string]any{"status": beforeStatus}, map[string]any{"status": "revoked", "principal_id": principalID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RegisterVerifiedEmail(ctx context.Context, in VerifiedEmailRecord, actor AuditActor) (VerifiedEmailRecord, error) {
	if in.ID == "" {
		in.ID = randomUUID()
	}
	if in.PrincipalID == "" || len(in.EmailCiphertext) == 0 || strings.TrimSpace(in.NormalizedEmailHMAC) == "" ||
		(in.VerificationSource != "oidc" && in.VerificationSource != "email_challenge") {
		return VerifiedEmailRecord{}, errors.New("complete verified email evidence is required")
	}
	if in.VerifiedAt.IsZero() {
		in.VerifiedAt = time.Now().UTC()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return VerifiedEmailRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	err = tx.QueryRow(ctx, `
		INSERT INTO verified_emails(
			id,invoice_user_id,email_ciphertext,normalized_email_hmac,
			verification_source,verified_at,revoked_at)
		VALUES($1,$2,$3,$4,$5,$6,NULL)
		ON CONFLICT(invoice_user_id,normalized_email_hmac) DO UPDATE SET
			email_ciphertext=EXCLUDED.email_ciphertext,
			verification_source=EXCLUDED.verification_source,
			verified_at=EXCLUDED.verified_at,revoked_at=NULL,updated_at=now()
		RETURNING id,invoice_user_id,email_ciphertext,normalized_email_hmac,
			verification_source,verified_at,COALESCE(revoked_at,'epoch'::timestamptz),
			created_at,updated_at`, in.ID, in.PrincipalID, in.EmailCiphertext,
		in.NormalizedEmailHMAC, in.VerificationSource, in.VerifiedAt).Scan(
		&in.ID, &in.PrincipalID, &in.EmailCiphertext, &in.NormalizedEmailHMAC,
		&in.VerificationSource, &in.VerifiedAt, &in.RevokedAt, &in.CreatedAt, &in.UpdatedAt)
	if err != nil {
		return VerifiedEmailRecord{}, fmt.Errorf("register verified email: %w", err)
	}
	if err = writeAudit(ctx, tx, actor, "verified_email.registered", "verified_email", in.ID, nil,
		map[string]any{"invoice_user_id": in.PrincipalID, "source": in.VerificationSource}); err != nil {
		return VerifiedEmailRecord{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return VerifiedEmailRecord{}, err
	}
	return in, nil
}

// SyncOIDCVerifiedEmail atomically replaces the OIDC-derived verified address
// set for one invoice user. Independently challenged addresses are preserved.
// A nil current value revokes every OIDC-derived address.
func (s *Store) SyncOIDCVerifiedEmail(ctx context.Context, principalID string, current *VerifiedEmailRecord, actor AuditActor) error {
	if strings.TrimSpace(principalID) == "" {
		return errors.New("invoice user is required")
	}
	if current != nil {
		if len(current.EmailCiphertext) == 0 || strings.TrimSpace(current.NormalizedEmailHMAC) == "" {
			return errors.New("current OIDC verified email is incomplete")
		}
		current.PrincipalID = principalID
		current.VerificationSource = "oidc"
		if current.ID == "" {
			current.ID = randomUUID()
		}
		if current.VerifiedAt.IsZero() {
			current.VerifiedAt = time.Now().UTC()
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var lockedUserID string
	if err = tx.QueryRow(ctx, `SELECT id FROM invoice_users WHERE id=$1 FOR UPDATE`, principalID).Scan(&lockedUserID); errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	} else if err != nil {
		return err
	}
	revoked, err := syncOIDCVerifiedEmailTx(ctx, tx, principalID, current)
	if err != nil {
		return err
	}
	if err = writeAudit(ctx, tx, actor, "verified_email.oidc_synchronized", "invoice_user", principalID, nil,
		map[string]any{"oidc_email_active": current != nil, "revoked_count": revoked}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func syncOIDCVerifiedEmailTx(ctx context.Context, tx pgx.Tx, principalID string, current *VerifiedEmailRecord) (int64, error) {
	keepHash := ""
	if current != nil {
		if len(current.EmailCiphertext) == 0 || strings.TrimSpace(current.NormalizedEmailHMAC) == "" {
			return 0, errors.New("current OIDC verified email is incomplete")
		}
		current.PrincipalID = principalID
		current.VerificationSource = "oidc"
		if current.ID == "" {
			current.ID = randomUUID()
		}
		if current.VerifiedAt.IsZero() {
			current.VerifiedAt = time.Now().UTC()
		}
		keepHash = current.NormalizedEmailHMAC
		if _, err := tx.Exec(ctx, `
			INSERT INTO verified_emails(
				id,invoice_user_id,email_ciphertext,normalized_email_hmac,
				verification_source,verified_at,revoked_at)
			VALUES($1,$2,$3,$4,'oidc',$5,NULL)
			ON CONFLICT(invoice_user_id,normalized_email_hmac) DO UPDATE SET
				email_ciphertext=EXCLUDED.email_ciphertext,verification_source='oidc',
				verified_at=EXCLUDED.verified_at,revoked_at=NULL,updated_at=now()`,
			current.ID, principalID, current.EmailCiphertext, current.NormalizedEmailHMAC, current.VerifiedAt); err != nil {
			return 0, err
		}
	}
	command, err := tx.Exec(ctx, `
		UPDATE verified_emails SET revoked_at=now(),updated_at=now()
		WHERE invoice_user_id=$1 AND verification_source='oidc' AND revoked_at IS NULL
			AND ($2='' OR normalized_email_hmac<>$2)`, principalID, keepHash)
	if err != nil {
		return 0, err
	}
	if current == nil {
		_, err = tx.Exec(ctx, `UPDATE invoice_users SET email_ciphertext=NULL,email_verified=FALSE,updated_at=now() WHERE id=$1`, principalID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE invoice_users SET email_verified=TRUE,updated_at=now() WHERE id=$1`, principalID)
	}
	if err != nil {
		return 0, err
	}
	return command.RowsAffected(), nil
}

func (s *Store) IsEmailVerified(ctx context.Context, principalID, normalizedEmailHMAC string) (bool, error) {
	var found bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM verified_emails
			WHERE invoice_user_id=$1 AND normalized_email_hmac=$2 AND revoked_at IS NULL
		)`, principalID, normalizedEmailHMAC).Scan(&found)
	return found, err
}

func (s *Store) GetLatestVerifiedEmail(ctx context.Context, principalID string) (VerifiedEmailRecord, error) {
	var out VerifiedEmailRecord
	err := s.pool.QueryRow(ctx, `
		SELECT id,invoice_user_id,email_ciphertext,normalized_email_hmac,
			verification_source,verified_at,COALESCE(revoked_at,'epoch'::timestamptz),
			created_at,updated_at
		FROM verified_emails
		WHERE invoice_user_id=$1 AND revoked_at IS NULL
		ORDER BY verified_at DESC,id DESC LIMIT 1`, principalID).Scan(&out.ID, &out.PrincipalID,
		&out.EmailCiphertext, &out.NormalizedEmailHMAC, &out.VerificationSource,
		&out.VerifiedAt, &out.RevokedAt, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return VerifiedEmailRecord{}, domain.ErrNotFound
	}
	if out.RevokedAt.Equal(time.Unix(0, 0).UTC()) {
		out.RevokedAt = time.Time{}
	}
	return out, err
}

func (s *Store) RevokeVerifiedEmail(ctx context.Context, principalID, normalizedEmailHMAC string, actor AuditActor) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var id string
	err = tx.QueryRow(ctx, `
		UPDATE verified_emails SET revoked_at=now(),updated_at=now()
		WHERE invoice_user_id=$1 AND normalized_email_hmac=$2 AND revoked_at IS NULL
		RETURNING id`, principalID, normalizedEmailHMAC).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return err
	}
	if err = writeAudit(ctx, tx, actor, "verified_email.revoked", "verified_email", id, nil,
		map[string]any{"revoked": true}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) BindExternalAccount(ctx context.Context, in ExternalAccountRecord, actor AuditActor) (ExternalAccountRecord, error) {
	if in.ID == "" {
		in.ID = randomUUID()
	}
	if in.PrincipalID == "" || in.SourceInstanceID == "" || strings.TrimSpace(in.ExternalUserID) == "" || strings.TrimSpace(in.BindingMethod) == "" {
		return ExternalAccountRecord{}, errors.New("external account identity and binding method are required")
	}
	if in.BindingStatus == "" {
		in.BindingStatus = "verified"
	}
	if in.BindingStatus == "verified" && in.VerifiedAt.IsZero() {
		in.VerifiedAt = time.Now().UTC()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ExternalAccountRecord{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	err = tx.QueryRow(ctx, `
		INSERT INTO external_accounts(
			id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,
			binding_method,binding_status,verified_at)
		VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8)
		ON CONFLICT(source_instance_id,external_user_id) DO UPDATE SET
			external_subject_hmac=COALESCE(EXCLUDED.external_subject_hmac,external_accounts.external_subject_hmac),
			binding_method=EXCLUDED.binding_method,binding_status=EXCLUDED.binding_status,
			verified_at=EXCLUDED.verified_at,updated_at=now()
		WHERE external_accounts.invoice_user_id=EXCLUDED.invoice_user_id
		RETURNING id,invoice_user_id,source_instance_id,external_user_id,
			COALESCE(external_subject_hmac,''),binding_method,binding_status,
			COALESCE(verified_at,'epoch'::timestamptz),created_at,updated_at`,
		in.ID, in.PrincipalID, in.SourceInstanceID, strings.TrimSpace(in.ExternalUserID),
		in.ExternalSubjectHMAC, in.BindingMethod, in.BindingStatus, optionalTime(in.VerifiedAt)).Scan(
		&in.ID, &in.PrincipalID, &in.SourceInstanceID, &in.ExternalUserID,
		&in.ExternalSubjectHMAC, &in.BindingMethod, &in.BindingStatus, &in.VerifiedAt,
		&in.CreatedAt, &in.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ExternalAccountRecord{}, domain.ErrForbidden
	}
	if err != nil {
		return ExternalAccountRecord{}, fmt.Errorf("bind external account: %w", err)
	}
	if err = writeAudit(ctx, tx, actor, "external_account.bound", "external_account", in.ID, nil, map[string]any{
		"invoice_user_id": in.PrincipalID, "source_instance_id": in.SourceInstanceID,
		"external_user_id": in.ExternalUserID, "status": in.BindingStatus,
	}); err != nil {
		return ExternalAccountRecord{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ExternalAccountRecord{}, err
	}
	return in, nil
}

// ClaimPlatformIdentity backfills invoice_users.platform/platform_user_id for
// userID with (platform, platformUserID), but only when those two columns are
// still NULL. It never overwrites an already-set value.
//
// This exists for platform-password login "claiming" a pre-existing
// invoice_user that a source-projection pipeline (BindExternalAccountFromSource)
// already created and bound to an external account before the account owner
// ever tried a password login (see runtime.go's ProvisionUser closure and
// docs/handoffs/XM-INV-AUTOLOGIN.md for the 403 this fixes): that user row was
// never created through ResolveOrCreate/EnsureUser, so its platform/
// platform_user_id columns start out NULL even though it already owns real
// funding lots.
//
// The caller MUST compare the returned storedPlatform/storedPlatformUserID
// against what it asked to claim: if they differ, this invoice_user already
// carries a *different* platform identity (backfilled by an earlier claim, or
// -- if that should ever become possible -- some other path) and the login
// must be rejected rather than silently letting two different platform
// accounts collapse onto the same invoice_user.
func (s *Store) ClaimPlatformIdentity(ctx context.Context, userID, platform, platformUserID string, actor AuditActor) (storedPlatform, storedPlatformUserID string, err error) {
	userID = strings.TrimSpace(userID)
	platformUserID = strings.TrimSpace(platformUserID)
	if userID == "" || platform == "" || platformUserID == "" {
		return "", "", errors.New("user id, platform and platform user id are required to claim an identity")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var beforePlatform, beforePlatformUserID string
	lookupErr := tx.QueryRow(ctx, `
		SELECT COALESCE(platform,''),COALESCE(platform_user_id,'')
		FROM invoice_users WHERE id=$1 FOR UPDATE`, userID).Scan(&beforePlatform, &beforePlatformUserID)
	if errors.Is(lookupErr, pgx.ErrNoRows) {
		return "", "", domain.ErrNotFound
	}
	if lookupErr != nil {
		return "", "", fmt.Errorf("lock invoice user for platform claim: %w", lookupErr)
	}
	if beforePlatform != "" || beforePlatformUserID != "" {
		// Already claimed (by this login's own platform identity on a prior
		// login, or -- the case the caller must reject -- a different one):
		// never overwrite. Nothing to commit; report what is actually stored.
		return beforePlatform, beforePlatformUserID, nil
	}
	if err = tx.QueryRow(ctx, `
		UPDATE invoice_users SET platform=$2,platform_user_id=$3,updated_at=now()
		WHERE id=$1
		RETURNING platform,platform_user_id`,
		userID, platform, platformUserID).Scan(&storedPlatform, &storedPlatformUserID); err != nil {
		return "", "", fmt.Errorf("claim platform identity: %w", err)
	}
	if err = writeAudit(ctx, tx, actor, "invoice_user.platform_identity_claimed", "invoice_user", userID, nil,
		map[string]any{"platform": platform, "platform_user_id": platformUserID}); err != nil {
		return "", "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", "", err
	}
	return storedPlatform, storedPlatformUserID, nil
}

func (s *Store) BindExternalAccountFromSource(ctx context.Context, in ExternalAccountRecord, actor AuditActor) (ExternalAccountRecord, bool, error) {
	if in.ID == "" {
		in.ID = randomUUID()
	}
	if in.PrincipalID == "" || in.SourceInstanceID == "" || strings.TrimSpace(in.ExternalUserID) == "" ||
		strings.TrimSpace(in.BindingMethod) == "" || in.SourceObservedAt.IsZero() || in.SourceSequence <= 0 {
		return ExternalAccountRecord{}, false, errors.New("complete monotonic source binding is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ExternalAccountRecord{}, false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,4))`, in.SourceInstanceID+"\n"+in.ExternalUserID); err != nil {
		return ExternalAccountRecord{}, false, err
	}
	var current ExternalAccountRecord
	lookupErr := tx.QueryRow(ctx, `
		SELECT id,invoice_user_id,source_instance_id,external_user_id,
			COALESCE(external_subject_hmac,''),binding_method,binding_status,
			COALESCE(verified_at,'epoch'::timestamptz),source_last_observed_at,
			source_last_sequence,created_at,updated_at
		FROM external_accounts WHERE source_instance_id=$1 AND external_user_id=$2 FOR UPDATE`,
		in.SourceInstanceID, in.ExternalUserID).Scan(&current.ID, &current.PrincipalID,
		&current.SourceInstanceID, &current.ExternalUserID, &current.ExternalSubjectHMAC,
		&current.BindingMethod, &current.BindingStatus, &current.VerifiedAt,
		&current.SourceObservedAt, &current.SourceSequence, &current.CreatedAt, &current.UpdatedAt)
	if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
		return ExternalAccountRecord{}, false, lookupErr
	}
	if lookupErr == nil {
		if current.PrincipalID != in.PrincipalID {
			return ExternalAccountRecord{}, false, domain.ErrForbidden
		}
		if in.SourceObservedAt.Before(current.SourceObservedAt) ||
			in.SourceObservedAt.Equal(current.SourceObservedAt) && in.SourceSequence <= current.SourceSequence {
			if err = writeAudit(ctx, tx, actor, "external_account.stale_source_event_ignored", "external_account", current.ID,
				map[string]any{"source_time": current.SourceObservedAt, "source_sequence": current.SourceSequence},
				map[string]any{"ignored_source_time": in.SourceObservedAt, "ignored_source_sequence": in.SourceSequence}); err != nil {
				return ExternalAccountRecord{}, false, err
			}
			if err = tx.Commit(ctx); err != nil {
				return ExternalAccountRecord{}, false, err
			}
			return current, true, nil
		}
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO external_accounts(
			id,invoice_user_id,source_instance_id,external_user_id,external_subject_hmac,
			binding_method,binding_status,verified_at,source_last_observed_at,source_last_sequence)
		VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9,$10)
		ON CONFLICT(source_instance_id,external_user_id) DO UPDATE SET
			external_subject_hmac=COALESCE(EXCLUDED.external_subject_hmac,external_accounts.external_subject_hmac),
			binding_method=EXCLUDED.binding_method,binding_status=EXCLUDED.binding_status,
			verified_at=EXCLUDED.verified_at,source_last_observed_at=EXCLUDED.source_last_observed_at,
			source_last_sequence=EXCLUDED.source_last_sequence,updated_at=now()
		WHERE external_accounts.invoice_user_id=EXCLUDED.invoice_user_id
		RETURNING id,invoice_user_id,source_instance_id,external_user_id,
			COALESCE(external_subject_hmac,''),binding_method,binding_status,
			COALESCE(verified_at,'epoch'::timestamptz),source_last_observed_at,
			source_last_sequence,created_at,updated_at`,
		in.ID, in.PrincipalID, in.SourceInstanceID, strings.TrimSpace(in.ExternalUserID),
		in.ExternalSubjectHMAC, in.BindingMethod, in.BindingStatus, optionalTime(in.VerifiedAt),
		in.SourceObservedAt.UTC(), in.SourceSequence).Scan(&in.ID, &in.PrincipalID,
		&in.SourceInstanceID, &in.ExternalUserID, &in.ExternalSubjectHMAC,
		&in.BindingMethod, &in.BindingStatus, &in.VerifiedAt, &in.SourceObservedAt,
		&in.SourceSequence, &in.CreatedAt, &in.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ExternalAccountRecord{}, false, domain.ErrForbidden
	}
	if err != nil {
		return ExternalAccountRecord{}, false, err
	}
	if err = writeAudit(ctx, tx, actor, "external_account.source_binding_applied", "external_account", in.ID,
		map[string]any{"status": current.BindingStatus}, map[string]any{"status": in.BindingStatus,
			"source_time": in.SourceObservedAt, "source_sequence": in.SourceSequence}); err != nil {
		return ExternalAccountRecord{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ExternalAccountRecord{}, false, err
	}
	return in, false, nil
}

func (s *Store) ListExternalAccounts(ctx context.Context, principalID string) ([]ConnectedSourceAccount, error) {
	if strings.TrimSpace(principalID) == "" {
		return nil, errors.New("principal ID is required")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT ea.id,si.id,si.source_type,si.name,
			CASE
				WHEN char_length(ea.external_user_id)<=4 THEN repeat('*',char_length(ea.external_user_id))
				ELSE left(ea.external_user_id,2)
					||repeat('*',least(char_length(ea.external_user_id)-4,8))
					||right(ea.external_user_id,2)
			END AS masked_external_user_id,
			ea.binding_status,COALESCE(ea.verified_at,'epoch'::timestamptz),
			COALESCE(max(fl.observed_at),'epoch'::timestamptz)
		FROM external_accounts ea
		JOIN source_instances si ON si.id=ea.source_instance_id
		LEFT JOIN funding_lots fl ON fl.external_account_id=ea.id
		WHERE ea.invoice_user_id=$1
		GROUP BY ea.id,si.id,si.source_type,si.name,ea.external_user_id,
			ea.binding_status,ea.verified_at
		ORDER BY si.name,si.id,ea.id
		LIMIT 50`, principalID)
	if err != nil {
		return nil, fmt.Errorf("list connected source accounts: %w", err)
	}
	defer rows.Close()
	out := make([]ConnectedSourceAccount, 0)
	for rows.Next() {
		var item ConnectedSourceAccount
		if err = rows.Scan(&item.ID, &item.SourceInstanceID, &item.SourceType,
			&item.SourceName, &item.MaskedExternalUserID, &item.BindingStatus,
			&item.VerifiedAt, &item.LastObservedAt); err != nil {
			return nil, err
		}
		if item.VerifiedAt.Equal(time.Unix(0, 0).UTC()) {
			item.VerifiedAt = time.Time{}
		}
		if item.LastObservedAt.Equal(time.Unix(0, 0).UTC()) {
			item.LastObservedAt = time.Time{}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
