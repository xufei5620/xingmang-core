package archive

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
)

type KeyPurpose string

const (
	PurposeChainRoot     KeyPurpose = "chain_root_signing"
	PurposeManifest      KeyPurpose = "archive_manifest_signing"
	PurposeCheckpoint    KeyPurpose = "archive_checkpoint_signing"
	PurposeRecoveryIndex KeyPurpose = "recovery_index_signing"
	PurposePLOApproval   KeyPurpose = "plo_approval_signing"
	PurposePLOResult     KeyPurpose = "plo_result_receipt_signing"
	PurposeKillSwitch    KeyPurpose = "kill_switch_signing"
	PurposeScrubReceipt  KeyPurpose = "archive_scrub_receipt_signing"
	PurposeScrubHead     KeyPurpose = "archive_scrub_head_signing"
)

const (
	ProtocolChainRootV1     = "audit-chain-root/v1"
	ProtocolManifestV1      = "xm-audit-archive-manifest-v1"
	ProtocolCheckpointV1    = "xm-audit-archive-checkpoint-v1"
	ProtocolRecoveryIndexV1 = "xm-audit-recovery-index-v1"
	ProtocolPLOApprovalV1   = "xm-audit-archive-plo-approval-v1"
	ProtocolPLOResultV1     = "xm-audit-archive-plo-result-v1"
	ProtocolKillSwitchV1    = "xm-audit-archive-kill-switch-v1"
	ProtocolScrubReceiptV1  = "xm-audit-archive-scrub-receipt-v1"
	ProtocolScrubHeadV1     = "xm-audit-archive-scrub-head-v1"

	DomainManifestV1      = ProtocolManifestV1
	DomainCheckpointV1    = ProtocolCheckpointV1
	DomainRecoveryIndexV1 = ProtocolRecoveryIndexV1
	DomainPLOApprovalV1   = ProtocolPLOApprovalV1
	DomainPLOResultV1     = ProtocolPLOResultV1
	DomainKillSwitchV1    = ProtocolKillSwitchV1
	DomainScrubReceiptV1  = ProtocolScrubReceiptV1
	DomainScrubHeadV1     = ProtocolScrubHeadV1
)

type TrustedKey struct {
	KeyID        string
	Algorithm    string
	PublicKey    string
	Fingerprint  string
	Purpose      KeyPurpose
	Protocol     string
	ValidFrom    time.Time
	ValidUntil   time.Time
	RevokedAt    *time.Time
	RevokeReason string
}

type Keyring interface {
	Lookup(keyID string, purpose KeyPurpose, protocol string, signedAt time.Time) (TrustedKey, error)
}

type PurposeSigner interface {
	audit.Signer
	Purpose() KeyPurpose
	Protocol() string
}

type purposeSigner struct {
	audit.Signer
	purpose  KeyPurpose
	protocol string
}

func WrapPurposeSigner(signer audit.Signer, purpose KeyPurpose, protocol string) (PurposeSigner, error) {
	if signer == nil || strings.TrimSpace(string(purpose)) == "" || strings.TrimSpace(protocol) == "" {
		return nil, fmt.Errorf("signer purpose/protocol required")
	}
	return &purposeSigner{Signer: signer, purpose: purpose, protocol: protocol}, nil
}

func (signer *purposeSigner) Purpose() KeyPurpose { return signer.purpose }
func (signer *purposeSigner) Protocol() string    { return signer.protocol }

func NewEd25519PurposeSigner(
	keyID string, seed []byte, purpose KeyPurpose, protocol string,
) (PurposeSigner, error) {
	if strings.TrimSpace(string(purpose)) == "" || strings.TrimSpace(protocol) == "" {
		return nil, fmt.Errorf("signer purpose/protocol required")
	}
	signer, err := audit.NewEd25519Signer(keyID, seed)
	if err != nil {
		return nil, err
	}
	return &purposeSigner{Signer: signer, purpose: purpose, protocol: protocol}, nil
}

func ValidateUniqueSignerMaterial(signers ...PurposeSigner) error {
	seen := make(map[string]KeyPurpose, len(signers))
	for _, signer := range signers {
		if signer == nil {
			return fmt.Errorf("nil signer")
		}
		fingerprint := sha256.Sum256(signer.Public())
		encoded := hex.EncodeToString(fingerprint[:])
		if prior, found := seen[encoded]; found && prior != signer.Purpose() {
			return fmt.Errorf("signing material reused across purposes")
		}
		seen[encoded] = signer.Purpose()
	}
	return nil
}

func ValidateTrustMaterialSeparation(signers []PurposeSigner, keyrings ...Keyring) error {
	seen := map[string]KeyPurpose{}
	register := func(fingerprint string, purpose KeyPurpose) error {
		if prior, found := seen[fingerprint]; found && prior != purpose {
			return fmt.Errorf("trusted key material reused across purposes")
		}
		seen[fingerprint] = purpose
		return nil
	}
	for _, keyring := range keyrings {
		if keyring == nil {
			continue
		}
		static, ok := keyring.(*StaticKeyring)
		if !ok {
			return fmt.Errorf("keyring does not expose auditable trust records")
		}
		for _, record := range static.keys {
			if err := register(record.Fingerprint, record.Purpose); err != nil {
				return err
			}
		}
	}
	for _, signer := range signers {
		if signer == nil {
			return fmt.Errorf("nil signer")
		}
		fingerprint := sha256.Sum256(signer.Public())
		if err := register(hex.EncodeToString(fingerprint[:]), signer.Purpose()); err != nil {
			return err
		}
	}
	return nil
}

type StaticKeyring struct {
	keys map[string]TrustedKey
}

func NewStaticKeyring(records []TrustedKey) (*StaticKeyring, error) {
	keyring := &StaticKeyring{keys: make(map[string]TrustedKey, len(records))}
	materialPurpose := make(map[string]KeyPurpose, len(records))
	for _, record := range records {
		if strings.TrimSpace(record.KeyID) == "" || record.Algorithm != "Ed25519" ||
			strings.TrimSpace(string(record.Purpose)) == "" || strings.TrimSpace(record.Protocol) == "" ||
			!record.ValidFrom.Before(record.ValidUntil) {
			return nil, fmt.Errorf("invalid trusted key metadata")
		}
		public, err := base64.StdEncoding.DecodeString(record.PublicKey)
		if err != nil || len(public) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("invalid trusted public key")
		}
		fingerprint := sha256.Sum256(public)
		encodedFingerprint := hex.EncodeToString(fingerprint[:])
		if record.Fingerprint != encodedFingerprint {
			return nil, fmt.Errorf("trusted key fingerprint mismatch")
		}
		if _, found := keyring.keys[record.KeyID]; found {
			return nil, fmt.Errorf("duplicate trusted key id")
		}
		if prior, found := materialPurpose[encodedFingerprint]; found && prior != record.Purpose {
			return nil, fmt.Errorf("trusted key material reused across purposes")
		}
		materialPurpose[encodedFingerprint] = record.Purpose
		keyring.keys[record.KeyID] = record
	}
	return keyring, nil
}

func (keyring *StaticKeyring) Len() int {
	if keyring == nil {
		return 0
	}
	return len(keyring.keys)
}

func (keyring *StaticKeyring) Lookup(
	keyID string, purpose KeyPurpose, protocol string, signedAt time.Time,
) (TrustedKey, error) {
	if keyring == nil {
		return TrustedKey{}, fmt.Errorf("trusted keyring unavailable")
	}
	record, found := keyring.keys[keyID]
	if !found || record.Purpose != purpose || record.Protocol != protocol {
		return TrustedKey{}, fmt.Errorf("trusted key not found for purpose/protocol")
	}
	signedAt = signedAt.UTC()
	if signedAt.Before(record.ValidFrom) || !signedAt.Before(record.ValidUntil) {
		return TrustedKey{}, fmt.Errorf("signature time outside key validity")
	}
	if record.RevokedAt != nil && !signedAt.Before(record.RevokedAt.UTC()) {
		return TrustedKey{}, fmt.Errorf("key revoked for signature time")
	}
	return record, nil
}

type trustedKeyJSON struct {
	KeyID        string     `json:"key_id"`
	Algorithm    string     `json:"algorithm"`
	PublicKey    string     `json:"public_key"`
	Fingerprint  string     `json:"fingerprint"`
	Purpose      KeyPurpose `json:"purpose"`
	Protocol     string     `json:"protocol"`
	ValidFrom    WireTime   `json:"valid_from"`
	ValidUntil   WireTime   `json:"valid_until"`
	RevokedAt    *WireTime  `json:"revoked_at"`
	RevokeReason string     `json:"revoke_reason"`
}

func LoadStaticKeyringJSON(raw []byte) (*StaticKeyring, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, fmt.Errorf("decode trusted keyring: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire []trustedKeyJSON
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("decode trusted keyring: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode trusted keyring trailing value")
	}
	records := make([]TrustedKey, 0, len(wire))
	for _, item := range wire {
		validFrom, err := ParseWireTime(item.ValidFrom)
		if err != nil {
			return nil, err
		}
		validUntil, err := ParseWireTime(item.ValidUntil)
		if err != nil {
			return nil, err
		}
		var revokedAt *time.Time
		if item.RevokedAt != nil {
			parsed, err := ParseWireTime(*item.RevokedAt)
			if err != nil {
				return nil, err
			}
			revokedAt = &parsed
		}
		records = append(records, TrustedKey{
			KeyID: item.KeyID, Algorithm: item.Algorithm, PublicKey: item.PublicKey,
			Fingerprint: item.Fingerprint, Purpose: item.Purpose, Protocol: item.Protocol,
			ValidFrom: validFrom, ValidUntil: validUntil, RevokedAt: revokedAt,
			RevokeReason: item.RevokeReason,
		})
	}
	return NewStaticKeyring(records)
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, isDelimiter := token.(json.Delim)
		if !isDelimiter {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if _, found := seen[key]; found {
					return fmt.Errorf("duplicate object key")
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return fmt.Errorf("unterminated object")
			}
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return fmt.Errorf("unterminated array")
			}
		default:
			return fmt.Errorf("unexpected delimiter")
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON value")
	}
	return nil
}

func SignaturePayload(domain, digest string) []byte {
	return []byte(domain + "\nsha256=" + digest + "\n")
}

func VerifyDetachedDomainSignature(
	domain, digest string, signature []byte, public ed25519.PublicKey,
) bool {
	return ed25519.Verify(public, SignaturePayload(domain, digest), signature)
}

func signEnvelope(unsigned any, signer PurposeSigner, purpose KeyPurpose, protocol, domain string) (
	string, string, error,
) {
	if signer == nil || signer.Purpose() != purpose || signer.Protocol() != protocol {
		return "", "", fmt.Errorf("signer purpose/protocol mismatch")
	}
	digest, err := unsignedHash(unsigned)
	if err != nil {
		return "", "", err
	}
	signature := base64.StdEncoding.EncodeToString(signer.Sign(SignaturePayload(domain, digest)))
	return digest, signature, nil
}

func SignManifest(value ManifestV1, signer PurposeSigner) (SignedManifestV1, error) {
	if err := ValidateManifestStructure(value); err != nil {
		return SignedManifestV1{}, err
	}
	digest, signature, err := signEnvelope(value, signer, PurposeManifest, ProtocolManifestV1, DomainManifestV1)
	if err != nil {
		return SignedManifestV1{}, err
	}
	return SignedManifestV1{
		Unsigned: value, UnsignedSHA256: digest, SignatureAlgorithm: "Ed25519",
		SignatureKeyID: signer.KeyID(), Signature: signature,
	}, nil
}

func SignCheckpoint(value CheckpointV1, signer PurposeSigner) (SignedCheckpointV1, error) {
	if err := validateCheckpoint(value); err != nil {
		return SignedCheckpointV1{}, err
	}
	digest, signature, err := signEnvelope(value, signer, PurposeCheckpoint, ProtocolCheckpointV1, DomainCheckpointV1)
	if err != nil {
		return SignedCheckpointV1{}, err
	}
	return SignedCheckpointV1{Unsigned: value, UnsignedSHA256: digest, SignatureAlgorithm: "Ed25519",
		SignatureKeyID: signer.KeyID(), Signature: signature}, nil
}

func SignRecoveryIndex(value RecoveryIndexV1, signer PurposeSigner) (SignedRecoveryIndexV1, error) {
	if err := validateRecoveryIndex(value); err != nil {
		return SignedRecoveryIndexV1{}, err
	}
	digest, signature, err := signEnvelope(value, signer, PurposeRecoveryIndex,
		ProtocolRecoveryIndexV1, DomainRecoveryIndexV1)
	if err != nil {
		return SignedRecoveryIndexV1{}, err
	}
	return SignedRecoveryIndexV1{Unsigned: value, UnsignedSHA256: digest, SignatureAlgorithm: "Ed25519",
		SignatureKeyID: signer.KeyID(), Signature: signature}, nil
}
