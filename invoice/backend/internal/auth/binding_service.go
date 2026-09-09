package auth

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrInvalidBindingProof = errors.New("external-account binding proof is invalid or expired")

type BindingService struct {
	store    BindingProofStore
	verifier BindingProofVerifier
	now      func() time.Time
}

func NewBindingService(store BindingProofStore, verifier BindingProofVerifier) (*BindingService, error) {
	if store == nil || verifier == nil {
		return nil, errors.New("binding proof store and verifier are required")
	}
	return &BindingService{store: store, verifier: verifier, now: time.Now}, nil
}

type BeginBindingInput struct {
	InvoiceUserID    string
	SourceInstanceID string
	ExternalUserID   string
	Method           BindingProofMethod
	RequestID        string
}

type IssuedBindingChallenge struct {
	Challenge BindingChallenge
	Secret    string `json:"-"`
}

func (s *BindingService) Begin(ctx context.Context, input BeginBindingInput) (IssuedBindingChallenge, error) {
	if !validUUIDString(input.InvoiceUserID) || !validUUIDString(input.SourceInstanceID) || strings.TrimSpace(input.ExternalUserID) == "" || strings.TrimSpace(input.ExternalUserID) != input.ExternalUserID || len(input.ExternalUserID) > 512 || !input.Method.Valid() || !validRequestID(input.RequestID) {
		return IssuedBindingChallenge{}, errors.New("binding challenge coordinates are required")
	}
	if input.Method == BindingAdminAttested {
		return IssuedBindingChallenge{}, errors.New("administrator-attested binding requires a separate MFA-authorized command")
	}
	secret, err := randomURLToken(32)
	if err != nil {
		return IssuedBindingChallenge{}, err
	}
	now := s.now().UTC()
	challenge := BindingChallenge{
		ID: randomUUIDv4(), InvoiceUserID: input.InvoiceUserID,
		SourceInstanceID: input.SourceInstanceID, ExternalUserID: input.ExternalUserID,
		Method: input.Method, ChallengeHash: sha256Hex(secret), RequestID: input.RequestID,
		CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	}
	if err = s.store.CreateBindingChallenge(ctx, challenge); err != nil {
		return IssuedBindingChallenge{}, err
	}
	return IssuedBindingChallenge{Challenge: challenge, Secret: secret}, nil
}

func (s *BindingService) Verify(ctx context.Context, proof BindingProof) (VerifiedBindingProof, error) {
	if !validUUIDString(proof.ChallengeID) || !validOpaqueToken(proof.Challenge) || len(proof.Evidence) == 0 || len(proof.Evidence) > 64*1024 || !validRequestID(proof.RequestID) {
		return VerifiedBindingProof{}, ErrInvalidBindingProof
	}
	now := s.now().UTC()
	challenge, err := s.store.GetActiveBindingChallenge(ctx, proof.ChallengeID, now)
	if err != nil || !secureEqualHex(challenge.ChallengeHash, sha256Hex(proof.Challenge)) {
		return VerifiedBindingProof{}, ErrInvalidBindingProof
	}
	if proof.InvoiceUserID != challenge.InvoiceUserID || proof.SourceInstanceID != challenge.SourceInstanceID || proof.ExternalUserID != challenge.ExternalUserID || proof.Method != challenge.Method {
		return VerifiedBindingProof{}, ErrInvalidBindingProof
	}
	verified, err := s.verifier.VerifyBindingProof(ctx, challenge, proof)
	if err != nil {
		_ = s.store.RejectBindingProof(ctx, challenge.ID, proof.RequestID, "cryptographic or source proof verification failed", now)
		return VerifiedBindingProof{}, ErrInvalidBindingProof
	}
	if verified.ChallengeID != challenge.ID || verified.InvoiceUserID != challenge.InvoiceUserID || verified.SourceInstanceID != challenge.SourceInstanceID || verified.ExternalUserID != challenge.ExternalUserID || verified.Method != challenge.Method || len(verified.EvidenceHash) != 64 {
		return VerifiedBindingProof{}, ErrInvalidBindingProof
	}
	if !secureEqualHex(verified.EvidenceHash, sha256Hex(string(proof.Evidence))) {
		return VerifiedBindingProof{}, ErrInvalidBindingProof
	}
	verified.VerifiedAt = now
	if err = s.store.ConsumeVerifiedBindingProof(ctx, verified, proof.RequestID); err != nil {
		return VerifiedBindingProof{}, ErrInvalidBindingProof
	}
	return verified, nil
}
