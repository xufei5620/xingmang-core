package postgresstore

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

var auditActionPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`)

func writeAudit(
	ctx context.Context,
	tx pgx.Tx,
	actor AuditActor,
	action, objectType, objectID string,
	before, after any,
) error {
	actor = actor.normalized()
	_, err := tx.Exec(ctx, `
		INSERT INTO audit_events(
			id,actor_type,actor_id,action,object_type,object_id,request_id,
			source_ip_hmac,before_hash,after_hash,reason)
		VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),NULLIF($9,''),NULLIF($10,''),$11)`,
		randomUUID(), actor.Type, actor.ID, action, objectType, objectID,
		actor.RequestID, actor.SourceIPHMAC, stateHash(before), stateHash(after), actor.Reason)
	if err != nil {
		return fmt.Errorf("write audit event: %w", err)
	}
	return nil
}

func (s *Store) RecordAdminOperationalAudit(ctx context.Context, adminID, action, objectID, requestID, outcome string) error {
	if strings.TrimSpace(adminID) == "" || !auditActionPattern.MatchString(action) ||
		strings.TrimSpace(objectID) == "" || len(objectID) > 256 ||
		strings.TrimSpace(requestID) == "" || len(requestID) > 512 ||
		strings.ContainsAny(objectID+requestID, "\r\n\x00") ||
		(outcome != "success" && outcome != "failure" && outcome != "rate_limited") {
		return errors.New("invalid bounded administrator audit event")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	actor := AuditActor{Type: "admin", ID: adminID, RequestID: requestID, Reason: "outcome=" + outcome}
	if err = writeAudit(ctx, tx, actor, action, "admin_operation", objectID, nil, map[string]string{"outcome": outcome}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RecordMaintenanceAudit(ctx context.Context, actorID, action, objectID, requestID, outcome, reason string) error {
	reason = strings.TrimSpace(reason)
	if strings.TrimSpace(actorID) == "" || !auditActionPattern.MatchString(action) ||
		strings.TrimSpace(objectID) == "" || len(objectID) > 256 ||
		strings.TrimSpace(requestID) == "" || len(requestID) > 512 ||
		strings.ContainsAny(objectID+requestID, "\r\n\x00") ||
		(outcome != "success" && outcome != "failure") || reason == "" || len(reason) > 500 || strings.ContainsAny(reason, "\r\n\x00") {
		return errors.New("invalid bounded maintenance audit event")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	actor := AuditActor{Type: "system", ID: actorID, RequestID: requestID, Reason: reason}
	if err = writeAudit(ctx, tx, actor, action, "maintenance_object", objectID, nil, map[string]string{"outcome": outcome}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
