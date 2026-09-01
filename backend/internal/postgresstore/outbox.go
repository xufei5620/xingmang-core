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

const (
	outboxLeaseDuration = 10 * time.Minute
	outboxMaxAttempts   = 8
)

// ClaimOutbox uses row locks plus an opaque lease token.  ClaimID, not the raw
// UUID, must be passed to MarkOutboxSent/Failed so a stale worker cannot
// acknowledge a job reclaimed after its lease expired.
func (s *Store) ClaimOutbox(ctx context.Context, limit int, now time.Time) ([]OutboxClaim, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	rows, err := tx.Query(ctx, `
		SELECT id FROM email_outbox
		WHERE attempt_count < $1 AND (
			(status IN ('queued','failed') AND next_attempt_at <= $2)
			OR (status='sending' AND lease_expires_at <= $2)
		)
		ORDER BY next_attempt_at,created_at,id
		FOR UPDATE SKIP LOCKED LIMIT $3`, outboxMaxAttempts, now, limit)
	if err != nil {
		return nil, fmt.Errorf("select due outbox: %w", err)
	}
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	claims := make([]OutboxClaim, 0, len(ids))
	for _, id := range ids {
		token := randomUUID()
		leaseUntil := now.Add(outboxLeaseDuration)
		var claim OutboxClaim
		err = tx.QueryRow(ctx, `
			UPDATE email_outbox SET status='sending',attempt_count=attempt_count+1,
				lease_token=$1,lease_expires_at=$2,updated_at=$3
			WHERE id=$4
			RETURNING id,invoice_request_id,created_at`, token, leaseUntil, now, id).Scan(
			&claim.OutboxID, &claim.RequestID, &claim.CreatedAt)
		if err != nil {
			return nil, err
		}
		err = tx.QueryRow(ctx, `
			SELECT request_no,invoice_user_id,idempotency_key,profile_snapshot_ciphertext
			FROM invoice_requests WHERE id=$1`, claim.RequestID).Scan(
			&claim.RequestNo, &claim.PrincipalID, &claim.IdempotencyKey, &claim.ProfileSnapshotCiphertext)
		if err != nil {
			return nil, err
		}
		claim.ClaimID = claim.OutboxID + "." + token
		claims = append(claims, claim)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return claims, nil
}

func splitClaimID(claimID string) (string, string, error) {
	index := strings.LastIndex(claimID, ".")
	if index <= 0 || index == len(claimID)-1 {
		return "", "", domain.ErrConflict
	}
	return claimID[:index], claimID[index+1:], nil
}

func (s *Store) MarkOutboxSent(ctx context.Context, claimID, providerMessageID string, now time.Time) error {
	id, token, err := splitClaimID(claimID)
	if err != nil {
		return err
	}
	providerMessageID = strings.TrimSpace(providerMessageID)
	if len(providerMessageID) > 512 {
		return errors.New("provider message ID is too long")
	}
	command, err := s.pool.Exec(ctx, `
		UPDATE email_outbox SET status='sent',provider_message_id=NULLIF($1,''),
			last_error_code=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=$2
		WHERE id=$3 AND status='sending' AND lease_token=$4`, providerMessageID, now, id, token)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	return nil
}

func (s *Store) MarkOutboxFailed(ctx context.Context, claimID, errorCode string, nextAttemptAt time.Time) error {
	id, token, err := splitClaimID(claimID)
	if err != nil {
		return err
	}
	errorCode = strings.TrimSpace(errorCode)
	if errorCode == "" || len(errorCode) > 128 || strings.ContainsAny(errorCode, "\r\n\x00") {
		return errors.New("invalid outbox error code")
	}
	if nextAttemptAt.IsZero() {
		nextAttemptAt = time.Now().UTC().Add(5 * time.Minute)
	}
	command, err := s.pool.Exec(ctx, `
		UPDATE email_outbox SET
			status=CASE WHEN attempt_count >= $1 THEN 'dead' ELSE 'failed' END,
			last_error_code=$2,next_attempt_at=$3,lease_token=NULL,
			lease_expires_at=NULL,updated_at=now()
		WHERE id=$4 AND status='sending' AND lease_token=$5`,
		outboxMaxAttempts, errorCode, nextAttemptAt, id, token)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	return nil
}

func (s *Store) GetOutbox(ctx context.Context, outboxID string) (domain.EmailOutbox, error) {
	var out domain.EmailOutbox
	err := s.pool.QueryRow(ctx, `
		SELECT id,invoice_request_id,document_id,recipient_hash,template_version,
			status,attempt_count,next_attempt_at,created_at
		FROM email_outbox WHERE id=$1`, outboxID).Scan(&out.ID, &out.RequestID,
		&out.DocumentID, &out.RecipientHash, &out.TemplateVersion, &out.Status,
		&out.Attempts, &out.NextAttemptAt, &out.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EmailOutbox{}, domain.ErrNotFound
	}
	return out, err
}

// GetInvoiceDeliveryState: see postgresstore's GetRequestRecord doc comment
// for why a platform mismatch (XM-INV-PLATFORM-SCOPE) reports ErrNotFound
// rather than ErrForbidden.
func (s *Store) GetInvoiceDeliveryState(ctx context.Context, principalID, requestID string, admin bool, platform domain.SourceType) (InvoiceDeliveryState, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return InvoiceDeliveryState{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	record, err := getRequestRecordTx(ctx, tx, requestID, false)
	if err != nil {
		return InvoiceDeliveryState{}, err
	}
	if !admin && record.Request.PrincipalID != principalID {
		return InvoiceDeliveryState{}, domain.ErrForbidden
	}
	if !admin && platform != "" && record.Request.SourceType != platform {
		return InvoiceDeliveryState{}, domain.ErrNotFound
	}
	if !admin && record.Request.Status != domain.StatusIssued {
		return InvoiceDeliveryState{}, domain.ErrInvalidState
	}
	document, outbox, found, err := getAttachedDocumentTx(ctx, tx, requestID)
	if err != nil {
		return InvoiceDeliveryState{}, err
	}
	if !found {
		return InvoiceDeliveryState{}, domain.ErrNotFound
	}
	if err = tx.Commit(ctx); err != nil {
		return InvoiceDeliveryState{}, err
	}
	return InvoiceDeliveryState{Document: document, Outbox: outbox}, nil
}

func (s *Store) RequeueEmail(ctx context.Context, requestID, adminID, reason string, actor AuditActor) (domain.EmailOutbox, error) {
	reason = strings.TrimSpace(reason)
	if requestID == "" || adminID == "" || reason == "" || len(reason) > 500 || strings.ContainsAny(reason, "\r\n\x00") {
		return domain.EmailOutbox{}, errors.New("request, administrator and bounded requeue reason are required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.EmailOutbox{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	record, err := getRequestRecordTx(ctx, tx, requestID, true)
	if err != nil {
		return domain.EmailOutbox{}, err
	}
	if record.Request.Status != domain.StatusIssued {
		return domain.EmailOutbox{}, domain.ErrInvalidState
	}
	_, outbox, found, err := getAttachedDocumentTx(ctx, tx, requestID)
	if err != nil {
		return domain.EmailOutbox{}, err
	}
	if !found {
		return domain.EmailOutbox{}, domain.ErrNotFound
	}
	if outbox.Status == "queued" || outbox.Status == "sending" {
		return domain.EmailOutbox{}, domain.ErrConflict
	}
	before := outbox
	now := time.Now().UTC()
	command, err := tx.Exec(ctx, `
		UPDATE email_outbox SET status='queued',attempt_count=0,next_attempt_at=$1,
			provider_message_id=NULL,last_error_code=NULL,lease_token=NULL,
			lease_expires_at=NULL,updated_at=$1
		WHERE id=$2 AND status IN ('sent','failed','dead')`, now, outbox.ID)
	if err != nil {
		return domain.EmailOutbox{}, err
	}
	if command.RowsAffected() != 1 {
		return domain.EmailOutbox{}, domain.ErrConflict
	}
	outbox.Status = "queued"
	outbox.Attempts = 0
	outbox.NextAttemptAt = now
	actor = actor.normalized()
	actor.ID = adminID
	actor.Type = "admin"
	actor.Reason = reason
	if err = writeAudit(ctx, tx, actor, "email_outbox.requeued", "email_outbox", outbox.ID, before, outbox); err != nil {
		return domain.EmailOutbox{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.EmailOutbox{}, err
	}
	return outbox, nil
}
