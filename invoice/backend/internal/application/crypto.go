package application

import (
	"fmt"
	"strings"

	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/postgresstore"
)

// userEmailAAD is kept as this package's spelling, but the definition itself
// now lives in binding_keys.go's exported UserEmailAAD so cmd/account-bind
// binds to the same physical string instead of hand-keeping a copy of it.
func userEmailAAD(issuer, subject string) string {
	return UserEmailAAD(issuer, subject)
}

func verifiedEmailAAD(principalID, normalizedEmailHMAC string) string {
	return "invoice-verified-email\n" + principalID + "\n" + normalizedEmailHMAC
}

func profileAAD(principalID, profileID, field string) string {
	return "invoice-profile\n" + principalID + "\n" + profileID + "\n" + field
}

func requestSnapshotAAD(principalID, idempotencyKey string) string {
	return "invoice-request-profile-snapshot\n" + principalID + "\n" + idempotencyKey
}

func issueSnapshotAAD(requestID string) string {
	return "invoice-request-issue-snapshot\n" + requestID
}

func sourceEventAAD(sourceInstanceID, eventKind, externalEventID, revision string) string {
	return "invoice-source-event\n" + sourceInstanceID + "\n" + eventKind + "\n" + externalEventID + "\n" + revision
}

func ingestEventAAD(sourceInstanceID, streamID, eventID, payloadHash string) string {
	return "invoice-ingest-event\n" + sourceInstanceID + "\n" + streamID + "\n" + eventID + "\n" + payloadHash
}

func paymentEvidenceAAD(lotID, action, evidenceHash string) string {
	return "invoice-payment-evidence\n" + lotID + "\n" + action + "\n" + evidenceHash
}

func paymentReviewReasonAAD(lotID, action, evidenceHash string) string {
	return "invoice-payment-review-reason\n" + lotID + "\n" + action + "\n" + evidenceHash
}

func refundEvidenceAAD(caseID, resolutionStatus, evidenceHash string) string {
	return "invoice-refund-evidence\n" + caseID + "\n" + resolutionStatus + "\n" + evidenceHash
}

func refundNoteAAD(caseID, resolutionStatus, evidenceHash string) string {
	return "invoice-refund-note\n" + caseID + "\n" + resolutionStatus + "\n" + evidenceHash
}

func eligibilityFreezeEvidenceAAD(freezeID, evidenceHash string) string {
	return "invoice-eligibility-freeze-evidence\n" + freezeID + "\n" + evidenceHash
}

func eligibilityFreezeNoteAAD(freezeID, evidenceHash string) string {
	return "invoice-eligibility-freeze-note\n" + freezeID + "\n" + evidenceHash
}

func (s *Service) encryptProfile(profile domain.InvoiceProfile) (postgresstore.ProfileRecord, error) {
	encrypt := func(field, value string) ([]byte, error) {
		if strings.TrimSpace(value) == "" {
			return nil, nil
		}
		return s.keys.Encrypt([]byte(value), profileAAD(profile.PrincipalID, profile.ID, field))
	}
	title, err := encrypt("title", profile.Title)
	if err != nil {
		return postgresstore.ProfileRecord{}, err
	}
	taxID, err := encrypt("tax_id", profile.TaxID)
	if err != nil {
		return postgresstore.ProfileRecord{}, err
	}
	email, err := encrypt("email", profile.Email)
	if err != nil {
		return postgresstore.ProfileRecord{}, err
	}
	address, err := encrypt("address", profile.Address)
	if err != nil {
		return postgresstore.ProfileRecord{}, err
	}
	phone, err := encrypt("phone", profile.Phone)
	if err != nil {
		return postgresstore.ProfileRecord{}, err
	}
	bankName, err := encrypt("bank_name", profile.BankName)
	if err != nil {
		return postgresstore.ProfileRecord{}, err
	}
	bankAccount, err := encrypt("bank_account", profile.BankAccount)
	if err != nil {
		return postgresstore.ProfileRecord{}, err
	}
	taxHMAC := ""
	if strings.TrimSpace(profile.TaxID) != "" {
		taxHMAC, err = s.keys.BlindIndex("invoice-tax-id/"+profile.PrincipalID, profile.TaxID)
		if err != nil {
			return postgresstore.ProfileRecord{}, err
		}
	}
	return postgresstore.ProfileRecord{
		ID: profile.ID, PrincipalID: profile.PrincipalID, Type: profile.Type,
		TitleCiphertext: title, TaxIDCiphertext: taxID, TaxIDHMAC: taxHMAC,
		EmailCiphertext: email, AddressCiphertext: address, PhoneCiphertext: phone,
		BankNameCiphertext: bankName, BankAccountCiphertext: bankAccount,
		EmailVerified: profile.EmailVerified, IsDefault: profile.IsDefault, Revision: profile.Revision,
	}, nil
}

func (s *Service) decryptProfile(record postgresstore.ProfileRecord) (domain.InvoiceProfile, error) {
	decrypt := func(field string, value []byte) (string, error) {
		if len(value) == 0 {
			return "", nil
		}
		plaintext, err := s.keys.Decrypt(value, profileAAD(record.PrincipalID, record.ID, field))
		if err != nil {
			return "", fmt.Errorf("decrypt profile field %s: %w", field, err)
		}
		return string(plaintext), nil
	}
	title, err := decrypt("title", record.TitleCiphertext)
	if err != nil {
		return domain.InvoiceProfile{}, err
	}
	taxID, err := decrypt("tax_id", record.TaxIDCiphertext)
	if err != nil {
		return domain.InvoiceProfile{}, err
	}
	email, err := decrypt("email", record.EmailCiphertext)
	if err != nil {
		return domain.InvoiceProfile{}, err
	}
	address, err := decrypt("address", record.AddressCiphertext)
	if err != nil {
		return domain.InvoiceProfile{}, err
	}
	phone, err := decrypt("phone", record.PhoneCiphertext)
	if err != nil {
		return domain.InvoiceProfile{}, err
	}
	bankName, err := decrypt("bank_name", record.BankNameCiphertext)
	if err != nil {
		return domain.InvoiceProfile{}, err
	}
	bankAccount, err := decrypt("bank_account", record.BankAccountCiphertext)
	if err != nil {
		return domain.InvoiceProfile{}, err
	}
	return domain.InvoiceProfile{
		ID: record.ID, PrincipalID: record.PrincipalID, Type: record.Type,
		Title: title, TaxID: taxID, Email: email, EmailVerified: record.EmailVerified,
		Address: address, Phone: phone, BankName: bankName, BankAccount: bankAccount,
		IsDefault: record.IsDefault, Revision: record.Revision,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}, nil
}
