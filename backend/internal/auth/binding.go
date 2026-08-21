package auth

import (
	"context"
	"errors"
	"strings"
	"time"
)

type BindingProofMethod string

const (
	BindingSourceSignedChallenge BindingProofMethod = "source_signed_challenge"
	BindingOIDCSubjectProjection BindingProofMethod = "oidc_subject_projection"
	BindingAdminAttested         BindingProofMethod = "admin_attested"
)

func (m BindingProofMethod) Valid() bool {
	return m == BindingSourceSignedChallenge || m == BindingOIDCSubjectProjection || m == BindingAdminAttested
}

type BindingChallenge struct {
	ID               string
	InvoiceUserID    string
	SourceInstanceID string
	ExternalUserID   string
	Method           BindingProofMethod
	ChallengeHash    string
	RequestID        string
	CreatedAt        time.Time
	ExpiresAt        time.Time
	ConsumedAt       *time.Time
}

func (c BindingChallenge) Validate() error {
	if !validUUIDString(c.ID) || !validUUIDString(c.InvoiceUserID) || !validUUIDString(c.SourceInstanceID) || strings.TrimSpace(c.ExternalUserID) == "" || strings.TrimSpace(c.ExternalUserID) != c.ExternalUserID || len(c.ChallengeHash) != 64 || !c.Method.Valid() {
		return errors.New("external-account binding challenge is invalid")
	}
	if c.CreatedAt.IsZero() || !c.ExpiresAt.After(c.CreatedAt) || c.ExpiresAt.Sub(c.CreatedAt) > 15*time.Minute {
		return errors.New("external-account binding challenge lifetime is invalid")
	}
	if !validRequestID(c.RequestID) {
		return errors.New("external-account binding challenge request ID is invalid")
	}
	return nil
}

type BindingProof struct {
	ChallengeID        string
	InvoiceUserID      string
	SourceInstanceID   string
	ExternalUserID     string
	Method             BindingProofMethod
	Challenge          string
	Evidence           []byte
	SourceRevisionHash string
	RequestID          string
}

type VerifiedBindingProof struct {
	ChallengeID        string
	InvoiceUserID      string
	SourceInstanceID   string
	ExternalUserID     string
	Method             BindingProofMethod
	EvidenceHash       string
	SourceRevisionHash string
	VerifiedAt         time.Time
}

// BindingProofVerifier is implemented by an isolated source connector. It must
// verify a short-lived challenge bound to all four identity coordinates. An
// email match alone is deliberately not a proof method.
type BindingProofVerifier interface {
	VerifyBindingProof(ctx context.Context, challenge BindingChallenge, proof BindingProof) (VerifiedBindingProof, error)
}

type BindingProofStore interface {
	CreateBindingChallenge(ctx context.Context, challenge BindingChallenge) error
	GetActiveBindingChallenge(ctx context.Context, challengeID string, now time.Time) (BindingChallenge, error)
	ConsumeVerifiedBindingProof(ctx context.Context, proof VerifiedBindingProof, requestID string) error
	RejectBindingProof(ctx context.Context, challengeID, requestID, reason string, now time.Time) error
}
