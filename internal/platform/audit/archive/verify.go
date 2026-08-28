package archive

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"reflect"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
)

type VerificationCode string

const (
	VerificationOK                 VerificationCode = "ok"
	VerificationObjectHashMismatch VerificationCode = "object_hash_mismatch"
	VerificationManifestSignature  VerificationCode = "manifest_signature_invalid"
	VerificationManifestGap        VerificationCode = "manifest_gap"
	VerificationSequenceGap        VerificationCode = "sequence_gap"
	VerificationBrokenLink         VerificationCode = "broken_link"
	VerificationEventHashMismatch  VerificationCode = "event_hash_mismatch"
	VerificationRootSignature      VerificationCode = "root_signature_invalid"
	VerificationFormatInvalid      VerificationCode = "archive_format_invalid"
)

type VerificationReport struct {
	Code                   VerificationCode    `json:"code"`
	FirstBadSequence       int64               `json:"first_bad_sequence"`
	Detail                 string              `json:"detail"`
	VerifiedRows           int64               `json:"verified_rows"`
	VerifiedObjects        int64               `json:"verified_objects"`
	VerifiedRootHash       string              `json:"verified_root_hash"`
	ChainIntegrity         string              `json:"chain_integrity"`
	CaptureCompleteness    string              `json:"capture_completeness"`
	CanonicalVersionCounts [2]CanonicalCountV1 `json:"canonical_version_counts"`
	Caveats                []string            `json:"caveats"`
}

type OperationalError struct {
	Code  string
	Cause error
}

func (err *OperationalError) Error() string {
	if err == nil {
		return "archive operational error"
	}
	return err.Code
}

func (err *OperationalError) Unwrap() error { return err.Cause }

type IntegrityError struct{ Report VerificationReport }

func (err *IntegrityError) Error() string { return string(err.Report.Code) }

func report(code VerificationCode, sequence int64, detail string) VerificationReport {
	return VerificationReport{Code: code, FirstBadSequence: sequence, Detail: detail}
}

func VerifyManifestContinuity(
	current SignedManifestV1, previous *SignedManifestV1, previousRef *ArtifactRefV1,
) VerificationReport {
	value := current.Unsigned
	if value.FromSequence == 1 {
		if previous != nil || previousRef != nil || ValidateManifestStructure(value) != nil {
			return report(VerificationManifestGap, value.FromSequence, "first manifest predecessor invalid")
		}
		return report(VerificationOK, 0, "")
	}
	if previous == nil || previousRef == nil || previous.Unsigned.ToSequence+1 != value.FromSequence {
		return report(VerificationManifestGap, value.FromSequence, "previous manifest missing or non-contiguous")
	}
	previousBytes, err := EncodeSignedManifestV1(*previous)
	if err != nil {
		return report(VerificationManifestGap, value.FromSequence, "previous manifest wire invalid")
	}
	sum := sha256.Sum256(previousBytes)
	wantSHA := hex.EncodeToString(sum[:])
	ref := value.PreviousManifest
	if ref.Kind != "object" || ref.BucketID != previousRef.BucketID || ref.Key != previousRef.Key ||
		ref.VersionID != previousRef.VersionID || ref.SHA256 != previousRef.SHA256 ||
		previousRef.SHA256 != wantSHA {
		return report(VerificationManifestGap, value.FromSequence, "previous exact locator mismatch")
	}
	return report(VerificationOK, 0, "")
}

func trustedPublicKey(record TrustedKey) (ed25519.PublicKey, error) {
	public, err := base64.StdEncoding.DecodeString(record.PublicKey)
	if err != nil || len(public) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid trusted public key")
	}
	return ed25519.PublicKey(public), nil
}

func verifyEnvelopeSignature(
	keyID, digest, encodedSignature string,
	keyring Keyring,
	purpose KeyPurpose,
	protocol, domain string,
	signedAt time.Time,
) error {
	if keyring == nil {
		return fmt.Errorf("trusted keyring unavailable")
	}
	record, err := keyring.Lookup(keyID, purpose, protocol, signedAt)
	if err != nil {
		return err
	}
	public, err := trustedPublicKey(record)
	if err != nil {
		return err
	}
	signature, err := base64.StdEncoding.DecodeString(encodedSignature)
	if err != nil || !VerifyDetachedDomainSignature(domain, digest, signature, public) {
		return fmt.Errorf("signature invalid")
	}
	return nil
}

func VerifyManifestSignature(value SignedManifestV1, keyring Keyring) error {
	if err := validateSignedManifest(value); err != nil {
		return err
	}
	signedAt, err := ParseWireTime(value.Unsigned.CreatedAt)
	if err != nil {
		return err
	}
	return verifyEnvelopeSignature(value.SignatureKeyID, value.UnsignedSHA256, value.Signature,
		keyring, PurposeManifest, ProtocolManifestV1, DomainManifestV1, signedAt)
}

func VerifyCheckpointSignature(value SignedCheckpointV1, keyring Keyring) error {
	if err := validateSignedCheckpoint(value); err != nil {
		return err
	}
	signedAt, err := ParseWireTime(value.Unsigned.CreatedAt)
	if err != nil {
		return err
	}
	return verifyEnvelopeSignature(value.SignatureKeyID, value.UnsignedSHA256, value.Signature,
		keyring, PurposeCheckpoint, ProtocolCheckpointV1, DomainCheckpointV1, signedAt)
}

func VerifyRecoveryIndexSignature(value SignedRecoveryIndexV1, keyring Keyring) error {
	if err := validateSignedRecoveryIndex(value); err != nil {
		return err
	}
	signedAt, err := ParseWireTime(value.Unsigned.UpdatedAt)
	if err != nil {
		return err
	}
	return verifyEnvelopeSignature(value.SignatureKeyID, value.UnsignedSHA256, value.Signature,
		keyring, PurposeRecoveryIndex, ProtocolRecoveryIndexV1, DomainRecoveryIndexV1, signedAt)
}

func verifyRootSignature(value ChainRootRefV1, keyring Keyring) bool {
	if keyring == nil {
		return false
	}
	signedAt, err := ParseWireTime(value.ComputedAt)
	if err != nil {
		return false
	}
	record, err := keyring.Lookup(value.KeyID, PurposeChainRoot, ProtocolChainRootV1, signedAt)
	if err != nil {
		return false
	}
	public, err := trustedPublicKey(record)
	if err != nil {
		return false
	}
	root := audit.ChainRoot{
		ID: uuid.MustParse(value.ID), ComputedAt: signedAt,
		FromSequence: value.FromSequence, ToSequence: value.ToSequence,
		RootHash: value.RootHash, Signature: value.Signature, KeyID: value.KeyID,
	}
	return audit.VerifyRoot(root, public) == nil
}

func VerifyChainRootSignature(value ChainRootRefV1, keyring Keyring) error {
	if err := validateChainRootRef(value); err != nil {
		return err
	}
	if !verifyRootSignature(value, keyring) {
		return fmt.Errorf("chain root signature invalid")
	}
	return nil
}

func readObject(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, &OperationalError{Code: "object_read_failed", Cause: fmt.Errorf("reader unavailable")}
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil, &OperationalError{Code: "object_read_failed", Cause: err}
	}
	return raw, nil
}

func verifyObjectBytes(raw []byte, expected ObjectVersionV1) bool {
	sum := sha256.Sum256(raw)
	return int64(len(raw)) == expected.SizeBytes && hex.EncodeToString(sum[:]) == expected.SHA256
}

func VerifySegment(
	ctx context.Context,
	manifest SignedManifestV1,
	payload io.Reader,
	projections map[string]io.Reader,
	rootKeyring Keyring,
	manifestKeyring Keyring,
	previous *SignedManifestV1,
) (VerificationReport, error) {
	if err := ctx.Err(); err != nil {
		return VerificationReport{}, &OperationalError{Code: "context_cancelled", Cause: err}
	}
	if err := validateSignedManifest(manifest); err != nil {
		var compatibility *CompatibilityError
		if errors.As(err, &compatibility) {
			return VerificationReport{}, compatibility
		}
		return report(VerificationFormatInvalid, manifest.Unsigned.FromSequence, "manifest wire invalid"), nil
	}
	if VerifyManifestSignature(manifest, manifestKeyring) != nil {
		return report(VerificationManifestSignature, manifest.Unsigned.FromSequence, "manifest signature invalid"), nil
	}
	var previousRef *ArtifactRefV1
	if manifest.Unsigned.FromSequence > 1 {
		ref := manifest.Unsigned.PreviousManifest
		previousRef = &ArtifactRefV1{BucketID: ref.BucketID, Key: ref.Key, VersionID: ref.VersionID, SHA256: ref.SHA256}
	}
	continuity := VerifyManifestContinuity(manifest, previous, previousRef)
	if continuity.Code != VerificationOK {
		return continuity, nil
	}
	rawPayload, err := readObject(payload)
	if err != nil {
		return VerificationReport{}, err
	}
	if !verifyObjectBytes(rawPayload, manifest.Unsigned.Payload) {
		return report(VerificationObjectHashMismatch, manifest.Unsigned.FromSequence, "payload object bytes mismatch"), nil
	}
	events, err := DecodePayload(bytes.NewReader(rawPayload))
	if err != nil {
		var compatibility *CompatibilityError
		if errors.As(err, &compatibility) {
			return VerificationReport{}, compatibility
		}
		var format *FormatError
		if errors.As(err, &format) && format.Code == string(VerificationEventHashMismatch) {
			return report(VerificationEventHashMismatch, format.Line, "event hash mismatch"), nil
		}
		return report(VerificationFormatInvalid, manifest.Unsigned.FromSequence, "payload wire invalid"), nil
	}
	if int64(len(events)) != manifest.Unsigned.RowCount || len(events) == 0 ||
		events[0].Sequence != manifest.Unsigned.FromSequence ||
		events[len(events)-1].Sequence != manifest.Unsigned.ToSequence {
		return report(VerificationSequenceGap, manifest.Unsigned.FromSequence, "payload range/count mismatch"), nil
	}
	canonicalCounts := [2]int64{}
	for index, event := range events {
		wantSequence := manifest.Unsigned.FromSequence + int64(index)
		if event.Sequence != wantSequence {
			return report(VerificationSequenceGap, event.Sequence, "sequence is not contiguous"), nil
		}
		if index == 0 {
			if event.PrevHash != manifest.Unsigned.FirstPrevHash {
				return report(VerificationBrokenLink, event.Sequence, "first prev_hash differs from manifest"), nil
			}
		} else if event.PrevHash != events[index-1].EventHash {
			return report(VerificationBrokenLink, event.Sequence, "event prev_hash differs from predecessor"), nil
		}
		canonicalCounts[event.CanonicalVersion-1]++
	}
	if events[0].EventHash != manifest.Unsigned.FirstEventHash ||
		events[len(events)-1].EventHash != manifest.Unsigned.LastEventHash ||
		canonicalCounts[0] != manifest.Unsigned.CanonicalVersionCounts[0].RowCount ||
		canonicalCounts[1] != manifest.Unsigned.CanonicalVersionCounts[1].RowCount {
		return report(VerificationEventHashMismatch, manifest.Unsigned.FromSequence, "manifest event evidence mismatch"), nil
	}
	verifiedObjects := int64(1)
	for _, projectionRef := range manifest.Unsigned.Projections {
		raw, err := readObject(projections[projectionRef.Environment])
		if err != nil {
			return VerificationReport{}, err
		}
		if !verifyObjectBytes(raw, projectionRef.Object) {
			return report(VerificationObjectHashMismatch, manifest.Unsigned.FromSequence,
				"projection object bytes mismatch"), nil
		}
		rows, err := DecodeProjection(bytes.NewReader(raw), projectionRef.Environment)
		if err != nil {
			return report(VerificationFormatInvalid, manifest.Unsigned.FromSequence, "projection wire invalid"), nil
		}
		expected := make([]ProjectionEventV1, 0)
		for _, event := range events {
			if event.Environment == projectionRef.Environment {
				expected = append(expected, projectionFromAudit(event))
			}
		}
		if int64(len(rows)) != projectionRef.Object.RowCount || !reflect.DeepEqual(rows, expected) {
			return report(VerificationFormatInvalid, manifest.Unsigned.FromSequence,
				"projection does not match payload allowlist"), nil
		}
		verifiedObjects++
	}
	root := manifest.Unsigned.ChainRoot
	if root.ToSequence < manifest.Unsigned.ToSequence ||
		(root.ToSequence == manifest.Unsigned.ToSequence && root.RootHash != manifest.Unsigned.LastEventHash) ||
		!verifyRootSignature(root, rootKeyring) {
		return report(VerificationRootSignature, manifest.Unsigned.ToSequence, "chain root signature/evidence invalid"), nil
	}
	return VerificationReport{
		Code: VerificationOK, VerifiedRows: int64(len(events)), VerifiedObjects: verifiedObjects,
		VerifiedRootHash: root.RootHash, ChainIntegrity: "verified",
		CaptureCompleteness: func() string {
			if manifest.Unsigned.EligibleActionRunCount == 0 {
				return "not_checked"
			}
			if manifest.Unsigned.MissingAuditEventCount > 0 || manifest.Unsigned.DuplicateAuditEventCount > 0 {
				return "gaps_found"
			}
			return "verified"
		}(),
		CanonicalVersionCounts: manifest.Unsigned.CanonicalVersionCounts,
		Caveats: func() []string {
			if manifest.Unsigned.CanonicalVersionCounts[0].RowCount > 0 {
				return []string{"legacy_canonical_v1_non_injective"}
			}
			return []string{}
		}(),
	}, nil
}
