package postgresstore

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"invoice-system/backend/internal/domain"
)

// SaveProfileCAS is the production profile writer.  expectedRevision must be
// zero for a create and exactly match the stored revision for an update.
func (s *Store) SaveProfileCAS(ctx context.Context, p ProfileRecord, emailVerified bool, expectedRevision int64, actor AuditActor) (ProfileRecord, error) {
	if p.ID == "" || p.PrincipalID == "" || len(p.TitleCiphertext) == 0 || len(p.EmailCiphertext) == 0 {
		return ProfileRecord{}, errors.New("profile id, owner, encrypted title and encrypted email are required")
	}
	if p.Type != domain.ProfilePersonal && p.Type != domain.ProfileEnterprise {
		return ProfileRecord{}, errors.New("unsupported profile type")
	}
	if p.Type == domain.ProfileEnterprise && len(p.TaxIDCiphertext) == 0 {
		return ProfileRecord{}, errors.New("encrypted enterprise tax ID is required")
	}
	if !emailVerified {
		return ProfileRecord{}, errors.New("profile delivery email is not verified")
	}
	p.EmailVerified = true
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProfileRecord{}, fmt.Errorf("begin save profile: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,1))`, p.PrincipalID); err != nil {
		return ProfileRecord{}, fmt.Errorf("lock profile owner: %w", err)
	}
	var storedOwner string
	var storedRevision int64
	var storedCreatedAt = p.CreatedAt
	err = tx.QueryRow(ctx, `SELECT invoice_user_id,revision,created_at FROM invoice_profiles WHERE id=$1 FOR UPDATE`, p.ID).Scan(&storedOwner, &storedRevision, &storedCreatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows) && expectedRevision != 0:
		return ProfileRecord{}, domain.ErrNotFound
	case errors.Is(err, pgx.ErrNoRows):
		err = nil
	case err != nil:
		return ProfileRecord{}, fmt.Errorf("lock invoice profile: %w", err)
	case storedOwner != p.PrincipalID:
		return ProfileRecord{}, domain.ErrForbidden
	case storedRevision != expectedRevision:
		return ProfileRecord{}, domain.ErrVersionConflict
	}
	if p.IsDefault {
		if _, err = tx.Exec(ctx, `UPDATE invoice_profiles SET is_default=FALSE,updated_at=now() WHERE invoice_user_id=$1 AND id<>$2 AND is_default`, p.PrincipalID, p.ID); err != nil {
			return ProfileRecord{}, fmt.Errorf("clear previous default profile: %w", err)
		}
	}
	if storedRevision == 0 {
		err = tx.QueryRow(ctx, `
			INSERT INTO invoice_profiles(
				id,invoice_user_id,profile_type,title_ciphertext,tax_id_ciphertext,tax_id_hmac,
				email_ciphertext,email_verified,address_ciphertext,phone_ciphertext,
				bank_name_ciphertext,bank_account_ciphertext,is_default,revision)
			VALUES($1,$2,$3,$4,$5,NULLIF($6,''),$7,TRUE,$8,$9,$10,$11,$12,1)
			RETURNING revision,created_at,updated_at`,
			p.ID, p.PrincipalID, p.Type, p.TitleCiphertext, nullableBytes(p.TaxIDCiphertext), p.TaxIDHMAC,
			p.EmailCiphertext, nullableBytes(p.AddressCiphertext), nullableBytes(p.PhoneCiphertext),
			nullableBytes(p.BankNameCiphertext), nullableBytes(p.BankAccountCiphertext), p.IsDefault).Scan(
			&p.Revision, &p.CreatedAt, &p.UpdatedAt)
	} else {
		err = tx.QueryRow(ctx, `
			UPDATE invoice_profiles SET
				profile_type=$1,title_ciphertext=$2,tax_id_ciphertext=$3,tax_id_hmac=NULLIF($4,''),
				email_ciphertext=$5,email_verified=TRUE,address_ciphertext=$6,phone_ciphertext=$7,
				bank_name_ciphertext=$8,bank_account_ciphertext=$9,is_default=$10,
				revision=revision+1,updated_at=now()
			WHERE id=$11 AND invoice_user_id=$12 AND revision=$13
			RETURNING revision,created_at,updated_at`,
			p.Type, p.TitleCiphertext, nullableBytes(p.TaxIDCiphertext), p.TaxIDHMAC,
			p.EmailCiphertext, nullableBytes(p.AddressCiphertext), nullableBytes(p.PhoneCiphertext),
			nullableBytes(p.BankNameCiphertext), nullableBytes(p.BankAccountCiphertext), p.IsDefault,
			p.ID, p.PrincipalID, expectedRevision).Scan(&p.Revision, &p.CreatedAt, &p.UpdatedAt)
	}
	if err != nil {
		return ProfileRecord{}, fmt.Errorf("persist invoice profile: %w", err)
	}
	action := "profile.created"
	if storedRevision > 0 {
		action = "profile.updated"
	}
	if err = writeAudit(ctx, tx, actor, action, "invoice_profile", p.ID,
		map[string]any{"revision": storedRevision}, map[string]any{"revision": p.Revision, "default": p.IsDefault}); err != nil {
		return ProfileRecord{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ProfileRecord{}, fmt.Errorf("commit invoice profile: %w", err)
	}
	return p, nil
}

func (s *Store) ListProfileRecords(ctx context.Context, principalID string) ([]ProfileRecord, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id,invoice_user_id,profile_type,title_ciphertext,COALESCE(tax_id_ciphertext,''::bytea),
			COALESCE(tax_id_hmac,''),email_ciphertext,COALESCE(address_ciphertext,''::bytea),
			COALESCE(phone_ciphertext,''::bytea),COALESCE(bank_name_ciphertext,''::bytea),
			COALESCE(bank_account_ciphertext,''::bytea),email_verified,is_default,revision,created_at,updated_at
		FROM invoice_profiles WHERE invoice_user_id=$1
		ORDER BY is_default DESC,updated_at DESC,id
		LIMIT 500`, principalID)
	if err != nil {
		return nil, fmt.Errorf("list invoice profiles: %w", err)
	}
	defer rows.Close()
	out := make([]ProfileRecord, 0)
	for rows.Next() {
		var p ProfileRecord
		if err = rows.Scan(&p.ID, &p.PrincipalID, &p.Type, &p.TitleCiphertext, &p.TaxIDCiphertext,
			&p.TaxIDHMAC, &p.EmailCiphertext, &p.AddressCiphertext, &p.PhoneCiphertext,
			&p.BankNameCiphertext, &p.BankAccountCiphertext, &p.EmailVerified, &p.IsDefault, &p.Revision,
			&p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	// Preserve a deterministic order even if a future query plan changes.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDefault != out[j].IsDefault {
			return out[i].IsDefault
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}
