package consoleassertion

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
)

// PublicKeyRecord is one entry of the public keyring manifest distributed to
// both repositories as contracts/auth/console-assertion-keyring.v1.json
// (spec §3.3). Field shape, JSON tags and validation rules mirror
// internal/platform/jobs/fleet_keyring.go's JobFleetTrustedKey by hand --
// this package does not import jobs: fleet_keyring.go's own validation
// hard-codes its purpose/protocol pair and would reject this domain's values
// outright (by design: a signature from one domain must never be replayable
// as another), so a shared type would either weaken that check or need a
// parallel "which domain am I" branch. Keeping a small, independently
// validated sibling type is the same trade-off the invoice-side verifier
// made for its own copy of this shape (see that repo's
// backend/internal/auth/console_assertion.go).
//
// This package is a signer, not a verifier -- it never loads this manifest
// at runtime to decide what to trust (see doc comment on Signer). The type
// exists so cmd/console-assertion-keygen produces exactly this shape, and so
// that shape can be unit-tested against the same rules the invoice side's
// verifier enforces, without needing a live counterpart to check against.
type PublicKeyRecord struct {
	KeyID        string     `json:"key_id"`
	Algorithm    string     `json:"algorithm"`
	PublicKey    string     `json:"public_key"`
	Fingerprint  string     `json:"fingerprint"`
	Purpose      string     `json:"purpose"`
	Protocol     string     `json:"protocol"`
	ValidFrom    time.Time  `json:"valid_from"`
	ValidUntil   time.Time  `json:"valid_until"`
	RevokedAt    *time.Time `json:"revoked_at"`
	RevokeReason string     `json:"revoke_reason"`
}

// keyIDPattern mirrors fleet_keyring.go's validateFleetID rule (lowercase
// letters/digits/./_/-, start and end alphanumeric, 1..63 bytes) with one
// addition: the technical spec's own example key_id ("2026-09") is exactly a
// date-like string, so digits at the start/end are explicitly allowed (they
// already are in fleet_keyring's rule; called out here so it is obvious this
// was checked, not assumed).
func validKeyIDChar(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '.' || ch == '_' || ch == '-'
}

func validateKeyID(id string) error {
	if id == "" || len(id) > 63 {
		return errors.New("key_id must be 1..63 bytes")
	}
	if strings.TrimSpace(id) != id {
		return errors.New("key_id must not contain surrounding whitespace")
	}
	for i := 0; i < len(id); i++ {
		if !validKeyIDChar(id[i]) {
			return errors.New("key_id contains a non-canonical character")
		}
	}
	first, last := id[0], id[len(id)-1]
	isAlnum := func(ch byte) bool { return (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') }
	if !isAlnum(first) || !isAlnum(last) {
		return errors.New("key_id must start/end with a lowercase letter or digit")
	}
	return nil
}

func decodePublicKey(encoded string) (ed25519.PublicKey, error) {
	if strings.TrimSpace(encoded) != encoded {
		return nil, errors.New("public key has surrounding whitespace")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(decoded) != encoded {
		return nil, errors.New("invalid Ed25519 public key")
	}
	return ed25519.PublicKey(decoded), nil
}

func fingerprintOf(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// Validate checks one record in isolation: algorithm, this package's own
// signing domain (purpose/protocol -- deliberately not configurable, a
// record for any other domain is always rejected), fingerprint-matches-key,
// UTC timestamps, valid_from < valid_until, and (if revoked) revocation
// strictly before expiry with a non-blank reason. It does not check
// cross-record rules (duplicate ids, reused key material, sort order) --
// see ValidateRecords for the full-manifest checks.
func (r PublicKeyRecord) Validate() error {
	if err := validateKeyID(r.KeyID); err != nil {
		return fmt.Errorf("key %q: %w", r.KeyID, err)
	}
	if r.Algorithm != Algorithm {
		// NB: fleet_keyring.go's FleetSignatureAlgorithm and this package's
		// Algorithm are both today the literal string "Ed25519"/"EdDSA"
		// respectively for different purposes (one names a key algorithm,
		// the other a JWS alg) -- see the constant doc comments.
		return fmt.Errorf("key %q: algorithm must be Ed25519", r.KeyID)
	}
	if r.Purpose != KeyringPurpose || r.Protocol != KeyringProtocol {
		return fmt.Errorf("key %q: purpose/protocol is not the console-assertion signing domain", r.KeyID)
	}
	pub, err := decodePublicKey(r.PublicKey)
	if err != nil {
		return fmt.Errorf("key %q: %w", r.KeyID, err)
	}
	if r.Fingerprint != fingerprintOf(pub) {
		return fmt.Errorf("key %q: fingerprint does not match public_key", r.KeyID)
	}
	if r.ValidFrom.IsZero() || r.ValidUntil.IsZero() || !r.ValidFrom.Before(r.ValidUntil) {
		return fmt.Errorf("key %q: invalid validity window", r.KeyID)
	}
	if r.ValidFrom.Location() != time.UTC || r.ValidUntil.Location() != time.UTC {
		return fmt.Errorf("key %q: validity must be UTC", r.KeyID)
	}
	if r.RevokedAt != nil {
		if r.RevokedAt.Location() != time.UTC || r.RevokedAt.Before(r.ValidFrom) {
			return fmt.Errorf("key %q: invalid revocation time", r.KeyID)
		}
		if !r.RevokedAt.Before(r.ValidUntil) {
			return fmt.Errorf("key %q: revocation must precede expiry", r.KeyID)
		}
		if strings.TrimSpace(r.RevokeReason) == "" {
			return fmt.Errorf("key %q: revocation reason required", r.KeyID)
		}
	}
	return nil
}

// ValidateRecords checks a full manifest: each record individually (see
// Validate), sorted ascending by key_id, no duplicate key_id, and no public
// key material reused across two ids. An empty list is valid -- it is the
// placeholder shipped before the first real key exists (mirroring the
// invoice side's own empty-array placeholder in its copy of this file).
func ValidateRecords(records []PublicKeyRecord) error {
	seenMaterial := make(map[string]string, len(records))
	for i, record := range records {
		if err := record.Validate(); err != nil {
			return err
		}
		if i > 0 && records[i-1].KeyID >= record.KeyID {
			return errors.New("records must be sorted ascending by key_id with no duplicates")
		}
		if previous, exists := seenMaterial[record.Fingerprint]; exists {
			return fmt.Errorf("public key material reused by %q and %q", previous, record.KeyID)
		}
		seenMaterial[record.Fingerprint] = record.KeyID
	}
	return nil
}

// NewPublicKeyRecord builds and self-validates one manifest entry for a
// freshly generated keypair (cmd/console-assertion-keygen's own use). Kept
// here rather than duplicated in the cmd package so the tool's output shape
// is provably the same shape this package's tests check against.
func NewPublicKeyRecord(keyID string, pub ed25519.PublicKey, validFrom, validUntil time.Time) (PublicKeyRecord, error) {
	if len(pub) != ed25519.PublicKeySize {
		return PublicKeyRecord{}, fmt.Errorf("public key must be %d bytes", ed25519.PublicKeySize)
	}
	record := PublicKeyRecord{
		KeyID: keyID, Algorithm: Algorithm,
		PublicKey:   base64.StdEncoding.EncodeToString(pub),
		Fingerprint: fingerprintOf(pub),
		Purpose:     KeyringPurpose, Protocol: KeyringProtocol,
		ValidFrom: validFrom.UTC(), ValidUntil: validUntil.UTC(),
	}
	if err := record.Validate(); err != nil {
		return PublicKeyRecord{}, err
	}
	return record, nil
}

type publicKeyRecordJSON struct {
	KeyID        string  `json:"key_id"`
	Algorithm    string  `json:"algorithm"`
	PublicKey    string  `json:"public_key"`
	Fingerprint  string  `json:"fingerprint"`
	Purpose      string  `json:"purpose"`
	Protocol     string  `json:"protocol"`
	ValidFrom    string  `json:"valid_from"`
	ValidUntil   string  `json:"valid_until"`
	RevokedAt    *string `json:"revoked_at"`
	RevokeReason string  `json:"revoke_reason"`
}

func parseManifestTime(value string) (time.Time, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return time.Time{}, errors.New("time must be non-empty and trimmed")
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid RFC3339 time: %w", err)
	}
	if !strings.HasSuffix(value, "Z") || parsed.Location() != time.UTC {
		return time.Time{}, errors.New("time must use UTC Z suffix")
	}
	return parsed, nil
}

// MarshalManifestJSON renders records as the canonical, reviewed-file shape
// (spec §3.3's example): a JSON array, RFC3339 UTC timestamps, trailing
// newline. Used by cmd/console-assertion-keygen so the file it writes is
// byte-identical in style to what LoadKeyringJSON accepts back.
func MarshalManifestJSON(records []PublicKeyRecord) ([]byte, error) {
	if err := ValidateRecords(records); err != nil {
		return nil, err
	}
	wire := make([]publicKeyRecordJSON, 0, len(records))
	for _, r := range records {
		item := publicKeyRecordJSON{
			KeyID: r.KeyID, Algorithm: r.Algorithm, PublicKey: r.PublicKey,
			Fingerprint: r.Fingerprint, Purpose: r.Purpose, Protocol: r.Protocol,
			ValidFrom: r.ValidFrom.UTC().Format(time.RFC3339Nano), ValidUntil: r.ValidUntil.UTC().Format(time.RFC3339Nano),
			RevokeReason: r.RevokeReason,
		}
		if r.RevokedAt != nil {
			s := r.RevokedAt.UTC().Format(time.RFC3339Nano)
			item.RevokedAt = &s
		}
		wire = append(wire, item)
	}
	out, err := json.MarshalIndent(wire, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// LoadKeyringJSON strictly decodes a manifest (unknown fields, duplicate
// object keys and trailing JSON values are all rejected, mirroring
// fleet_keyring.go's LoadJobFleetKeyringJSON), then validates it with
// ValidateRecords. Not on this package's runtime signing path (see Signer's
// doc comment) -- provided for cmd/console-assertion-keygen and tests that
// need to round-trip a manifest.
func LoadKeyringJSON(raw []byte) ([]PublicKeyRecord, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire []publicKeyRecordJSON
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("console assertion keyring: decode: %w", err)
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return nil, err
	}
	records := make([]PublicKeyRecord, 0, len(wire))
	for i, item := range wire {
		validFrom, err := parseManifestTime(item.ValidFrom)
		if err != nil {
			return nil, fmt.Errorf("console assertion keyring: record[%d] valid_from: %w", i, err)
		}
		validUntil, err := parseManifestTime(item.ValidUntil)
		if err != nil {
			return nil, fmt.Errorf("console assertion keyring: record[%d] valid_until: %w", i, err)
		}
		var revokedAt *time.Time
		if item.RevokedAt != nil {
			parsed, err := parseManifestTime(*item.RevokedAt)
			if err != nil {
				return nil, fmt.Errorf("console assertion keyring: record[%d] revoked_at: %w", i, err)
			}
			revokedAt = &parsed
		}
		records = append(records, PublicKeyRecord{
			KeyID: item.KeyID, Algorithm: item.Algorithm, PublicKey: item.PublicKey,
			Fingerprint: item.Fingerprint, Purpose: item.Purpose, Protocol: item.Protocol,
			ValidFrom: validFrom, ValidUntil: validUntil, RevokedAt: revokedAt,
			RevokeReason: item.RevokeReason,
		})
	}
	if err := ValidateRecords(records); err != nil {
		return nil, fmt.Errorf("console assertion keyring: %w", err)
	}
	return records, nil
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("console assertion keyring: trailing JSON value")
		}
		return fmt.Errorf("console assertion keyring: trailing data: %w", err)
	}
	return nil
}

// rejectDuplicateJSONKeys walks every object, catching duplicate keys before
// encoding/json would silently keep the last value (identical approach to
// fleet_keyring.go's rejectDuplicateFleetJSONKeys).
func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return errors.New("duplicate object key")
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return errors.New("unterminated object")
			}
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return errors.New("unterminated array")
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		return nil
	}
	if err := walk(); err != nil {
		return fmt.Errorf("console assertion keyring: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("console assertion keyring: trailing JSON value")
	}
	return nil
}
