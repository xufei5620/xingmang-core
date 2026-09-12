package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"invoice-system/backend/internal/securefields"
)

// identityMigratedAction is the audit_events.action written on a successful
// apply. Its (before_hash, after_hash) pair -- sha256Hex(issuer+"\n"+subject)
// for the old and new identity, the same hashing convention Principal.
// IdentityHash already uses -- doubles as this tool's own idempotency
// witness: a later run that no longer finds --from-issuer/--from-subject but
// finds an invoice_users row already sitting at --to-issuer/--to-subject with
// exactly this audit trail is recognized as "already migrated" rather than a
// generic "0 rows matched" refusal.
const identityMigratedAction = "identity.oidc_binding.migrated"

// IdentityMigrationInput drives MigrateOIDCBinding: design CR-0006 change
// item f / XM-INV-IDENTITY-MIGRATE, a one-time, human-approved rewrite of the
// single existing invoice_users admin row's (oidc_issuer, oidc_subject) from
// Keycloak's values to the trusted staff resolver's current tuple. Identity
// lookup remains keyed exclusively on that pair and preserves the historical
// row. This maintenance function is not called by a login or HTTP endpoint.
// Mirrors cmd/eligibility-repair's dry-run/apply lifecycle-tool shape.
type IdentityMigrationInput struct {
	FromIssuer  string
	FromSubject string
	ToIssuer    string
	ToSubject   string
	Apply       bool
	OperatorID  string
}

// IdentityMigrationRow is the matched invoice_users row's printable summary,
// captured before any mutation in both dry-run and apply.
type IdentityMigrationRow struct {
	UserID            string
	Status            string
	MaskedEmail       string
	CreatedAt         time.Time
	AuthSessionsTotal int64
	AuthSessionsLive  int64
	AuditRows         int64
}

// IdentityMigrationResult is MigrateOIDCBinding's full outcome, printable as
// the CLI's summary.
type IdentityMigrationResult struct {
	Applied             bool
	AlreadyMigrated     bool
	Row                 IdentityMigrationRow
	SessionsInvalidated int64
	FromIssuer          string
	FromSubject         string
	ToIssuer            string
	ToSubject           string
}

type identityMigrationCandidate struct {
	ID              string
	Status          string
	EmailCiphertext []byte
	CreatedAt       time.Time
}

// userEmailAADForMigration must byte-for-byte match application/crypto.go's
// unexported userEmailAAD -- duplicated here because this package (the
// invoice service's independent identity boundary, see doc.go) deliberately
// never imports the much larger application package. Same "mirror by hand,
// different domain, not literally shared code" precedent console_assertion.go
// already follows against xingmang-platform's fleet_keyring.go.
func userEmailAADForMigration(issuer, subject string) string {
	return "invoice-user-email\n" + issuer + "\n" + subject
}

// maskIdentityEmail mirrors httpapi's maskedEmailName (production_auth.go)
// byte-for-byte; duplicated for the same cross-package reason as the AAD
// helper above.
func maskIdentityEmail(email string) string {
	local, _, ok := strings.Cut(strings.TrimSpace(email), "@")
	if !ok || local == "" {
		return "用户"
	}
	runes := []rune(local)
	if len(runes) == 1 {
		return string(runes[0]) + "***"
	}
	return string(runes[0]) + "***" + string(runes[len(runes)-1])
}

func identityMigrationHash(issuer, subject string) string {
	return sha256Hex(issuer + "\n" + subject)
}

// validateIdentityMigrationInput checks in independently of any database
// connection -- every "refuses when subject formats are invalid" /
// "--operator-id is required with --apply" rule MigrateOIDCBinding documents,
// factored out so it can be unit tested without PostgreSQL. It trims
// whitespace but performs no other normalization (issuer case/trailing-slash,
// subject case) -- both flags must match whatever is already byte-exact in
// invoice_users, the same discipline auth.ResolveOrCreate's own
// validateExactHTTPSURL call already applies to a login's principal.
func validateIdentityMigrationInput(in IdentityMigrationInput) (fromIssuer, fromSubject, toIssuer, toSubject, operatorID string, err error) {
	fromIssuer = strings.TrimSpace(in.FromIssuer)
	toIssuer = strings.TrimSpace(in.ToIssuer)
	fromSubject = strings.TrimSpace(in.FromSubject)
	toSubject = strings.TrimSpace(in.ToSubject)
	if err = validateExactHTTPSURL(fromIssuer, true); err != nil {
		return "", "", "", "", "", fmt.Errorf("--from-issuer: %w", err)
	}
	if err = validateExactHTTPSURL(toIssuer, true); err != nil {
		return "", "", "", "", "", fmt.Errorf("--to-issuer: %w", err)
	}
	if !validUUIDString(fromSubject) {
		return "", "", "", "", "", errors.New("--from-subject must be a UUID")
	}
	if !validUUIDString(toSubject) {
		return "", "", "", "", "", errors.New("--to-subject must be a UUID")
	}
	if fromIssuer == toIssuer && fromSubject == toSubject {
		return "", "", "", "", "", errors.New("--from-issuer/--from-subject and --to-issuer/--to-subject are identical, nothing to migrate")
	}
	operatorID = strings.TrimSpace(in.OperatorID)
	if in.Apply && !validUUIDString(operatorID) {
		return "", "", "", "", "", errors.New("a valid operator UUID is required to apply")
	}
	return fromIssuer, fromSubject, toIssuer, toSubject, operatorID, nil
}

// MigrateOIDCBinding validates and, only when in.Apply, executes the CR-0006
// item f identity rebind in one all-or-nothing transaction: it locks the
// matched row, rewrites oidc_issuer/oidc_subject (and re-encrypts
// email_ciphertext under the new pair's AAD, if an email is on file --
// otherwise it would remain permanently undecryptable under the old AAD once
// the issuer/subject that authenticated it change), invalidates every live
// auth_sessions row for that user, and writes the identityMigratedAction
// audit row. A dry run (Apply==false) runs every one of the same validation
// and lookup queries, inside the same transaction, then always rolls back.
func MigrateOIDCBinding(ctx context.Context, pool *pgxpool.Pool, keyring securefields.Keyring, in IdentityMigrationInput) (IdentityMigrationResult, error) {
	if pool == nil {
		return IdentityMigrationResult{}, errors.New("nil PostgreSQL connection pool")
	}
	fromIssuer, fromSubject, toIssuer, toSubject, operatorID, err := validateIdentityMigrationInput(in)
	if err != nil {
		return IdentityMigrationResult{}, err
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return IdentityMigrationResult{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	beforeHash := identityMigrationHash(fromIssuer, fromSubject)
	afterHash := identityMigrationHash(toIssuer, toSubject)

	sourceRows, err := queryIdentityMigrationCandidates(ctx, tx, fromIssuer, fromSubject)
	if err != nil {
		return IdentityMigrationResult{}, err
	}

	if len(sourceRows) == 0 {
		targetRows, targetErr := queryIdentityMigrationCandidates(ctx, tx, toIssuer, toSubject)
		if targetErr != nil {
			return IdentityMigrationResult{}, targetErr
		}
		if len(targetRows) == 1 {
			migrated, auditErr := priorIdentityMigrationAuditExists(ctx, tx, targetRows[0].ID, beforeHash, afterHash)
			if auditErr != nil {
				return IdentityMigrationResult{}, auditErr
			}
			if migrated {
				row, rowErr := loadIdentityMigrationRow(ctx, tx, keyring, targetRows[0], toIssuer, toSubject)
				if rowErr != nil {
					return IdentityMigrationResult{}, rowErr
				}
				return IdentityMigrationResult{
					AlreadyMigrated: true, Row: row,
					FromIssuer: fromIssuer, FromSubject: fromSubject, ToIssuer: toIssuer, ToSubject: toSubject,
				}, nil
			}
			return IdentityMigrationResult{}, errors.New("no invoice_users row matches --from-issuer/--from-subject" +
				" (a row already exists at --to-issuer/--to-subject, but not as a result of migrating this --from identity" +
				" -- refusing rather than guessing)")
		}
		return IdentityMigrationResult{}, errors.New("no invoice_users row matches --from-issuer/--from-subject")
	}
	if len(sourceRows) > 1 {
		return IdentityMigrationResult{}, fmt.Errorf("%d invoice_users rows matched --from-issuer/--from-subject, expected exactly one", len(sourceRows))
	}
	source := sourceRows[0]
	if source.Status != "active" {
		return IdentityMigrationResult{}, fmt.Errorf("matched invoice_users row status is %q, not active; refusing to migrate", source.Status)
	}

	targetRows, err := queryIdentityMigrationCandidates(ctx, tx, toIssuer, toSubject)
	if err != nil {
		return IdentityMigrationResult{}, err
	}
	if len(targetRows) > 0 {
		return IdentityMigrationResult{}, errors.New("--to-issuer/--to-subject already exists on another invoice_users row")
	}

	row, err := loadIdentityMigrationRow(ctx, tx, keyring, source, fromIssuer, fromSubject)
	if err != nil {
		return IdentityMigrationResult{}, err
	}
	result := IdentityMigrationResult{
		Row: row, FromIssuer: fromIssuer, FromSubject: fromSubject, ToIssuer: toIssuer, ToSubject: toSubject,
	}
	if !in.Apply {
		return result, nil
	}

	var newEmailCiphertext []byte
	if len(source.EmailCiphertext) > 0 {
		plaintext, decErr := keyring.Decrypt(source.EmailCiphertext, userEmailAADForMigration(fromIssuer, fromSubject))
		if decErr != nil {
			return IdentityMigrationResult{}, fmt.Errorf("decrypt existing email for re-encryption: %w", decErr)
		}
		newEmailCiphertext, decErr = keyring.Encrypt(plaintext, userEmailAADForMigration(toIssuer, toSubject))
		if decErr != nil {
			return IdentityMigrationResult{}, fmt.Errorf("re-encrypt email under new identity: %w", decErr)
		}
	}

	var rowsAffected int64
	if newEmailCiphertext != nil {
		tag, execErr := tx.Exec(ctx, `
			UPDATE invoice_users SET oidc_issuer=$1,oidc_subject=$2,email_ciphertext=$3,updated_at=now()
			WHERE id=$4 AND oidc_issuer=$5 AND oidc_subject=$6`,
			toIssuer, toSubject, newEmailCiphertext, source.ID, fromIssuer, fromSubject)
		if execErr != nil {
			return IdentityMigrationResult{}, execErr
		}
		rowsAffected = tag.RowsAffected()
	} else {
		tag, execErr := tx.Exec(ctx, `
			UPDATE invoice_users SET oidc_issuer=$1,oidc_subject=$2,updated_at=now()
			WHERE id=$3 AND oidc_issuer=$4 AND oidc_subject=$5`,
			toIssuer, toSubject, source.ID, fromIssuer, fromSubject)
		if execErr != nil {
			return IdentityMigrationResult{}, execErr
		}
		rowsAffected = tag.RowsAffected()
	}
	if rowsAffected != 1 {
		return IdentityMigrationResult{}, errors.New("invoice_users row changed concurrently; refusing to apply")
	}

	now := time.Now().UTC()
	sessionTag, err := tx.Exec(ctx, `
		UPDATE auth_sessions SET revoked_at=$2,revoked_reason=$3
		WHERE invoice_user_id=$1 AND revoked_at IS NULL`,
		source.ID, now, "oidc identity migrated (XM-INV-IDENTITY-MIGRATE)")
	if err != nil {
		return IdentityMigrationResult{}, err
	}
	result.SessionsInvalidated = sessionTag.RowsAffected()

	if err = insertSecurityAudit(ctx, tx, SecurityAuditEvent{
		ID: randomUUIDv4(), ActorType: "admin", ActorID: operatorID,
		Action: identityMigratedAction, ObjectType: "invoice_user", ObjectID: source.ID,
		RequestID: "identity-migrate-cli", BeforeHash: beforeHash, AfterHash: afterHash,
		Reason:   "XM-INV-IDENTITY-MIGRATE / CR-0006 item f: rebind admin identity to console assertion",
		Severity: SeverityNotice, CreatedAt: now,
	}); err != nil {
		return IdentityMigrationResult{}, err
	}

	if err = tx.Commit(ctx); err != nil {
		return IdentityMigrationResult{}, err
	}
	result.Applied = true
	return result, nil
}

func queryIdentityMigrationCandidates(ctx context.Context, tx pgx.Tx, issuer, subject string) ([]identityMigrationCandidate, error) {
	rows, err := tx.Query(ctx, `
		SELECT id,status,COALESCE(email_ciphertext,''::bytea),created_at
		FROM invoice_users WHERE oidc_issuer=$1 AND oidc_subject=$2
		FOR UPDATE`, issuer, subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []identityMigrationCandidate
	for rows.Next() {
		var candidate identityMigrationCandidate
		if err = rows.Scan(&candidate.ID, &candidate.Status, &candidate.EmailCiphertext, &candidate.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, candidate)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func priorIdentityMigrationAuditExists(ctx context.Context, tx pgx.Tx, objectID, beforeHash, afterHash string) (bool, error) {
	var count int64
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM audit_events
		WHERE object_type='invoice_user' AND object_id=$1 AND action=$2
		  AND before_hash=$3 AND after_hash=$4`,
		objectID, identityMigratedAction, beforeHash, afterHash).Scan(&count)
	return count > 0, err
}

func loadIdentityMigrationRow(ctx context.Context, tx pgx.Tx, keyring securefields.Keyring, candidate identityMigrationCandidate, issuer, subject string) (IdentityMigrationRow, error) {
	maskedEmail := "(no email on file)"
	if len(candidate.EmailCiphertext) > 0 {
		plaintext, err := keyring.Decrypt(candidate.EmailCiphertext, userEmailAADForMigration(issuer, subject))
		if err != nil {
			return IdentityMigrationRow{}, fmt.Errorf("decrypt existing email for %s: %w", candidate.ID, err)
		}
		maskedEmail = maskIdentityEmail(string(plaintext))
	}
	var total, live int64
	if err := tx.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE revoked_at IS NULL)
		FROM auth_sessions WHERE invoice_user_id=$1`, candidate.ID).Scan(&total, &live); err != nil {
		return IdentityMigrationRow{}, err
	}
	var auditRows int64
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM audit_events WHERE object_type='invoice_user' AND object_id=$1`,
		candidate.ID).Scan(&auditRows); err != nil {
		return IdentityMigrationRow{}, err
	}
	return IdentityMigrationRow{
		UserID: candidate.ID, Status: candidate.Status, MaskedEmail: maskedEmail, CreatedAt: candidate.CreatedAt,
		AuthSessionsTotal: total, AuthSessionsLive: live, AuditRows: auditRows,
	}, nil
}
