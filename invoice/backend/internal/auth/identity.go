package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type InvoiceIdentity struct {
	UserID         string
	Issuer         string
	Subject        string
	Platform       Platform
	PlatformUserID string
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type IdentityStore interface {
	ResolveOrCreate(ctx context.Context, principal Principal, requestID string) (InvoiceIdentity, error)
}

type PostgresIdentityStore struct{ pool *pgxpool.Pool }

func NewPostgresIdentityStore(pool *pgxpool.Pool) *PostgresIdentityStore {
	return &PostgresIdentityStore{pool: pool}
}

// ResolveOrCreate keys exclusively on (issuer, subject). Verified email is
// intentionally left for the encrypted persistence.RegisterVerifiedEmail path;
// it is never used to merge or bind accounts here.
//
// For a platform-password login, principal.Issuer/Subject already equal
// principal.Platform's configured login origin and PlatformUserID (see
// platform_login.go) -- Platform/PlatformUserID are persisted alongside as an
// explicit, queryable projection of that same pair, never as a second key.
func (s *PostgresIdentityStore) ResolveOrCreate(ctx context.Context, principal Principal, requestID string) (InvoiceIdentity, error) {
	if s == nil || s.pool == nil {
		return InvoiceIdentity{}, errors.New("nil PostgreSQL identity store")
	}
	if validateExactHTTPSURL(principal.Issuer, true) != nil || principal.Subject == "" || len(principal.Subject) > 512 || hasControl(principal.Subject) || principal.IdentityHash() == "" || !validRequestID(requestID) {
		return InvoiceIdentity{}, errors.New("verified identity and request ID are required")
	}
	if principal.Platform != "" && (!principal.Platform.Valid() || principal.PlatformUserID != principal.Subject) {
		return InvoiceIdentity{}, errors.New("platform identity is inconsistent with issuer/subject")
	}
	if principal.Platform == "" && principal.PlatformUserID != "" {
		return InvoiceIdentity{}, errors.New("platform user ID requires a platform")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return InvoiceIdentity{}, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, principal.Issuer+"\n"+principal.Subject); err != nil {
		return InvoiceIdentity{}, err
	}
	identity, err := scanInvoiceIdentity(tx.QueryRow(ctx, `
		SELECT id,oidc_issuer,oidc_subject,COALESCE(platform,''),COALESCE(platform_user_id,''),status,created_at,updated_at
		FROM invoice_users WHERE oidc_issuer=$1 AND oidc_subject=$2`, principal.Issuer, principal.Subject))
	if errors.Is(err, pgx.ErrNoRows) {
		identity, err = scanInvoiceIdentity(tx.QueryRow(ctx, `
			INSERT INTO invoice_users(id,oidc_issuer,oidc_subject,platform,platform_user_id,status,email_verified)
			VALUES($1,$2,$3,NULLIF($4,''),NULLIF($5,''),'active',FALSE)
			RETURNING id,oidc_issuer,oidc_subject,COALESCE(platform,''),COALESCE(platform_user_id,''),status,created_at,updated_at`,
			randomUUIDv4(), principal.Issuer, principal.Subject, string(principal.Platform), principal.PlatformUserID))
		if err != nil {
			return InvoiceIdentity{}, err
		}
		actorType := "staff"
		if principal.Platform != "" {
			actorType = "platform"
		}
		if err = insertSecurityAudit(ctx, tx, SecurityAuditEvent{
			ID: randomUUIDv4(), ActorType: actorType, ActorID: principal.IdentityHash(),
			Action: "auth.identity.create", ObjectType: "invoice_user", ObjectID: identity.UserID,
			RequestID: requestID, Severity: SeverityNotice, CreatedAt: time.Now().UTC(),
		}); err != nil {
			return InvoiceIdentity{}, err
		}
	} else if err != nil {
		return InvoiceIdentity{}, err
	}
	if identity.Platform != principal.Platform || identity.PlatformUserID != principal.PlatformUserID {
		return InvoiceIdentity{}, errors.New("stored platform identity does not match the verified login")
	}
	if identity.Status != "active" {
		return InvoiceIdentity{}, ErrSessionInvalid
	}
	if err = tx.Commit(ctx); err != nil {
		return InvoiceIdentity{}, err
	}
	return identity, nil
}

func scanInvoiceIdentity(row pgx.Row) (InvoiceIdentity, error) {
	var identity InvoiceIdentity
	var platform string
	err := row.Scan(&identity.UserID, &identity.Issuer, &identity.Subject, &platform, &identity.PlatformUserID, &identity.Status, &identity.CreatedAt, &identity.UpdatedAt)
	identity.Platform = Platform(platform)
	return identity, err
}
