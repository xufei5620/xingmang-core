package jobs

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

// The fleet artifacts have a dedicated signing domain.  These values are
// intentionally not shared with the audit/archive keyrings: a signature from
// another subsystem must never be replayable as a fleet decision.
const (
	FleetSignatureAlgorithm   = "Ed25519"
	JobFleetManifestPurpose   = "job_fleet_manifest_signing"
	JobFleetInventoryPurpose  = "job_fleet_inventory_signing"
	JobFleetManifestProtocol  = "xm-job-fleet-manifest-v1"
	JobFleetInventoryProtocol = "xm-job-fleet-inventory-v1"
	JobFleetManifestKind      = "xingmang-job-fleet-manifest"
	JobFleetInventoryKind     = "xingmang-job-fleet-inventory"
	JobFleetBindingActive     = "active"
	JobFleetBindingClosed     = "closed"

	// A deployment policy may choose a shorter window, but the contract rejects
	// windows longer than one year so a forgotten artifact cannot authorize an
	// old fleet indefinitely.
	maxFleetValidity = 366 * 24 * time.Hour
)

// JobFleetTrustedKey is a public-only trust record. PublicKey is standard
// base64-encoded Ed25519 public material; no signing key is accepted here.
type JobFleetTrustedKey struct {
	KeyID        string
	Algorithm    string
	PublicKey    string
	Fingerprint  string
	Purpose      string
	Protocol     string
	ValidFrom    time.Time
	ValidUntil   time.Time
	RevokedAt    *time.Time
	RevokeReason string
}

// JobFleetKeyringLookup is the narrow verifier dependency. Implementations
// expose public trust records only; signing remains an external authority.
type JobFleetKeyringLookup interface {
	Lookup(keyID, purpose, protocol string, signedAt time.Time) (JobFleetTrustedKey, error)
}

// JobFleetKeyring is an immutable in-memory keyring loaded from a reviewed
// repository artifact.
type JobFleetKeyring struct {
	keys map[string]JobFleetTrustedKey
}

// NewJobFleetKeyring validates and copies public trust records.
func NewJobFleetKeyring(records []JobFleetTrustedKey) (*JobFleetKeyring, error) {
	if len(records) == 0 {
		return nil, errors.New("job fleet keyring: no trusted keys")
	}
	keyring := &JobFleetKeyring{keys: make(map[string]JobFleetTrustedKey, len(records))}
	seenMaterial := make(map[string]string, len(records))
	for index, record := range records {
		if err := validateFleetKeyMetadata(index, record); err != nil {
			return nil, err
		}
		if index > 0 && records[index-1].KeyID >= record.KeyID {
			return nil, errors.New("job fleet keyring: records must be sorted by key id")
		}
		public, err := decodeFleetPublicKey(record.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("job fleet keyring: key %q: %w", record.KeyID, err)
		}
		fingerprint := fleetSHA256Hex(public)
		if record.Fingerprint != fingerprint {
			return nil, fmt.Errorf("job fleet keyring: key %q fingerprint mismatch", record.KeyID)
		}
		if _, exists := keyring.keys[record.KeyID]; exists {
			return nil, fmt.Errorf("job fleet keyring: duplicate key id %q", record.KeyID)
		}
		if previous, exists := seenMaterial[fingerprint]; exists {
			return nil, fmt.Errorf("job fleet keyring: public material reused by %q and %q", previous, record.KeyID)
		}
		seenMaterial[fingerprint] = record.KeyID
		keyring.keys[record.KeyID] = cloneFleetTrustedKey(record)
	}
	return keyring, nil
}

func validateFleetKeyMetadata(index int, record JobFleetTrustedKey) error {
	if err := validateFleetID("key id", record.KeyID, 63); err != nil {
		return fmt.Errorf("job fleet keyring: key[%d]: %w", index, err)
	}
	if record.Algorithm != FleetSignatureAlgorithm {
		return fmt.Errorf("job fleet keyring: key %q algorithm must be %s", record.KeyID, FleetSignatureAlgorithm)
	}
	if (record.Purpose != JobFleetManifestPurpose || record.Protocol != JobFleetManifestProtocol) &&
		(record.Purpose != JobFleetInventoryPurpose || record.Protocol != JobFleetInventoryProtocol) {
		return fmt.Errorf("job fleet keyring: key %q purpose/protocol is not a fleet domain", record.KeyID)
	}
	if record.ValidFrom.IsZero() || record.ValidUntil.IsZero() || !record.ValidFrom.Before(record.ValidUntil) {
		return fmt.Errorf("job fleet keyring: key %q invalid validity", record.KeyID)
	}
	if record.ValidFrom.Location() != time.UTC || record.ValidUntil.Location() != time.UTC {
		return fmt.Errorf("job fleet keyring: key %q validity must be UTC", record.KeyID)
	}
	if record.RevokedAt != nil {
		if record.RevokedAt.Location() != time.UTC || record.RevokedAt.Before(record.ValidFrom) {
			return fmt.Errorf("job fleet keyring: key %q invalid revocation", record.KeyID)
		}
		if !record.RevokedAt.Before(record.ValidUntil) {
			return fmt.Errorf("job fleet keyring: key %q revocation must precede expiry", record.KeyID)
		}
		if strings.TrimSpace(record.RevokeReason) == "" {
			return fmt.Errorf("job fleet keyring: key %q revocation reason required", record.KeyID)
		}
	}
	return nil
}

func cloneFleetTrustedKey(record JobFleetTrustedKey) JobFleetTrustedKey {
	clone := record
	if record.RevokedAt != nil {
		at := *record.RevokedAt
		clone.RevokedAt = &at
	}
	return clone
}

// Len returns the number of trusted public keys.
func (keyring *JobFleetKeyring) Len() int {
	if keyring == nil {
		return 0
	}
	return len(keyring.keys)
}

// Lookup requires an exact key ID, purpose, protocol, and half-open validity
// interval. A zero timestamp is rejected rather than treated as "now".
func (keyring *JobFleetKeyring) Lookup(keyID, purpose, protocol string, signedAt time.Time) (JobFleetTrustedKey, error) {
	if keyring == nil {
		return JobFleetTrustedKey{}, errors.New("job fleet keyring: unavailable")
	}
	record, ok := keyring.keys[keyID]
	if !ok || record.Purpose != purpose || record.Protocol != protocol {
		return JobFleetTrustedKey{}, errors.New("job fleet keyring: key not found for purpose/protocol")
	}
	if signedAt.IsZero() {
		return JobFleetTrustedKey{}, errors.New("job fleet keyring: signed time is zero")
	}
	signedAt = signedAt.UTC()
	if signedAt.Before(record.ValidFrom) || !signedAt.Before(record.ValidUntil) {
		return JobFleetTrustedKey{}, errors.New("job fleet keyring: signed time outside key validity")
	}
	if record.RevokedAt != nil && !signedAt.Before(record.RevokedAt.UTC()) {
		return JobFleetTrustedKey{}, errors.New("job fleet keyring: key revoked at signed time")
	}
	return cloneFleetTrustedKey(record), nil
}

type fleetTrustedKeyJSON struct {
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

// LoadJobFleetKeyringJSON strictly decodes a public keyring. Unknown fields,
// duplicate object keys, and trailing JSON values are rejected.
func LoadJobFleetKeyringJSON(raw []byte) (*JobFleetKeyring, error) {
	if err := rejectDuplicateFleetJSONKeys(raw); err != nil {
		return nil, fmt.Errorf("job fleet keyring: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire []fleetTrustedKeyJSON
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("job fleet keyring: decode: %w", err)
	}
	if err := rejectFleetTrailingJSON(decoder); err != nil {
		return nil, fmt.Errorf("job fleet keyring: %w", err)
	}
	records := make([]JobFleetTrustedKey, 0, len(wire))
	for index, item := range wire {
		validFrom, err := parseFleetTime(item.ValidFrom)
		if err != nil {
			return nil, fmt.Errorf("job fleet keyring: key[%d] valid_from: %w", index, err)
		}
		validUntil, err := parseFleetTime(item.ValidUntil)
		if err != nil {
			return nil, fmt.Errorf("job fleet keyring: key[%d] valid_until: %w", index, err)
		}
		var revokedAt *time.Time
		if item.RevokedAt != nil {
			parsed, err := parseFleetTime(*item.RevokedAt)
			if err != nil {
				return nil, fmt.Errorf("job fleet keyring: key[%d] revoked_at: %w", index, err)
			}
			revokedAt = &parsed
		}
		records = append(records, JobFleetTrustedKey{
			KeyID: item.KeyID, Algorithm: item.Algorithm, PublicKey: item.PublicKey,
			Fingerprint: item.Fingerprint, Purpose: item.Purpose, Protocol: item.Protocol,
			ValidFrom: validFrom, ValidUntil: validUntil, RevokedAt: revokedAt,
			RevokeReason: item.RevokeReason,
		})
	}
	canonical, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("job fleet keyring: canonical encode: %w", err)
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(raw, canonical) {
		return nil, errors.New("job fleet keyring: non-canonical JSON bytes")
	}
	return NewJobFleetKeyring(records)
}

// This is a build-pinned, public-only keyring. The corresponding reviewed
// bytes live at contracts/jobs/job-fleet-keyring.v1.json. Rotation requires a
// reviewed contract/build change; no deployment-selected path is consulted.
const pinnedJobFleetKeyringJSON = `[{"key_id":"fleet-inventory-key","algorithm":"Ed25519","public_key":"5/FioQvsVZr+oZXk3OhLaVaNXSywlj60RsBoXisX8vA=","fingerprint":"c945cbf2a5602002141e2fb9d17054d6ca069951df73f49d1f8aca87f9878f60","purpose":"job_fleet_inventory_signing","protocol":"xm-job-fleet-inventory-v1","valid_from":"2026-01-01T00:00:00Z","valid_until":"2030-01-01T00:00:00Z","revoked_at":null,"revoke_reason":""},{"key_id":"fleet-manifest-key","algorithm":"Ed25519","public_key":"ebVWLo/mVPlAeLES6KmLp5AfhTrmlb7X4OORC60ElmQ=","fingerprint":"65b60673d6ed884bf01c2c222d82ada0740f29ac3355d6a925c81f17f47a27b8","purpose":"job_fleet_manifest_signing","protocol":"xm-job-fleet-manifest-v1","valid_from":"2026-01-01T00:00:00Z","valid_until":"2030-01-01T00:00:00Z","revoked_at":null,"revoke_reason":""}]` + "\n"

// PinnedJobFleetKeyringJSON returns a defensive copy of the reviewed bytes.
func PinnedJobFleetKeyringJSON() []byte { return []byte(pinnedJobFleetKeyringJSON) }

// PinnedJobFleetKeyringSHA256 is filled from the exact pinned bytes.
const PinnedJobFleetKeyringSHA256 = "1d3938b7120de032342564cc54a13b045e33f28674c2d7578b04b5494dfcc9c8"

// LoadPinnedJobFleetKeyring loads only the build-pinned public keyring.
func LoadPinnedJobFleetKeyring() (*JobFleetKeyring, error) {
	return LoadJobFleetKeyringJSON(PinnedJobFleetKeyringJSON())
}

func decodeFleetPublicKey(encoded string) (ed25519.PublicKey, error) {
	if strings.TrimSpace(encoded) != encoded {
		return nil, errors.New("public key has surrounding whitespace")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(decoded) != encoded {
		return nil, errors.New("invalid Ed25519 public key")
	}
	return ed25519.PublicKey(decoded), nil
}

func fleetSignaturePayload(protocol, digest string) []byte {
	return []byte(protocol + "\nsha256=" + digest + "\n")
}

func fleetSHA256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func validateFleetID(name, value string, maxBytes int) error {
	if value == "" || len(value) > maxBytes {
		return fmt.Errorf("%s must be 1..%d bytes", name, maxBytes)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not contain surrounding whitespace", name)
	}
	for index := 0; index < len(value); index++ {
		ch := value[index]
		if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '.' || ch == '_' || ch == '-') {
			return fmt.Errorf("%s contains non-canonical character", name)
		}
	}
	first, last := value[0], value[len(value)-1]
	if !((first >= 'a' && first <= 'z') || (first >= '0' && first <= '9')) ||
		!((last >= 'a' && last <= 'z') || (last >= '0' && last <= '9')) {
		return fmt.Errorf("%s must start/end with lowercase ASCII letter or digit", name)
	}
	return nil
}

func validateFleetDigest(name, value string) error {
	if len(value) != 64 {
		return fmt.Errorf("%s must be 64 lowercase hex characters", name)
	}
	for index := range value {
		ch := value[index]
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return fmt.Errorf("%s must be 64 lowercase hex characters", name)
		}
	}
	return nil
}

func validateFleetBuildDigest(name, value string) error {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return fmt.Errorf("%s must be sha256:<64 lowercase hex>", name)
	}
	return validateFleetDigest(name, value[len("sha256:"):])
}

func parseFleetTime(value string) (time.Time, error) {
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
	if parsed.UTC().Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("time is not canonical RFC3339 representation")
	}
	return parsed, nil
}

func validateFleetValidity(kind, issued, validFrom, validUntil string) (time.Time, time.Time, time.Time, error) {
	issuedAt, err := parseFleetTime(issued)
	if err != nil {
		return time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("%s issued_at: %w", kind, err)
	}
	from, err := parseFleetTime(validFrom)
	if err != nil {
		return time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("%s valid_from: %w", kind, err)
	}
	until, err := parseFleetTime(validUntil)
	if err != nil {
		return time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("%s valid_until: %w", kind, err)
	}
	if !from.Before(until) || until.Sub(from) > maxFleetValidity {
		return time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("%s validity window invalid or overlong", kind)
	}
	// An artifact may be issued before its activation window (for example,
	// during a staged rollout), but it cannot be issued after expiry.
	if !issuedAt.Before(until) {
		return time.Time{}, time.Time{}, time.Time{}, fmt.Errorf("%s issued_at is at/after expiry", kind)
	}
	return issuedAt, from, until, nil
}

func rejectFleetTrailingJSON(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

// rejectDuplicateFleetJSONKeys walks every object, catching duplicate keys
// before encoding/json would silently keep the last value.
func rejectDuplicateFleetJSONKeys(raw []byte) error {
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
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}
