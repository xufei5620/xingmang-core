package auth

// console_assertion.go implements CR-0006's console-assertion admin login
// path: verification of a signed, console-issued login credential (a
// JWT-shaped, Ed25519-signed compact token) against a static, reviewed
// public-key manifest, and its translation into the same Principal shape
// the OIDC callback already produces. The wire contract implemented here is
// frozen in the platform repo's
// docs/superpowers/specs/2026-09-02-cr0006-console-assertion-design.md
// (section 3) -- field names, bounds and error taxonomy below track that
// document exactly; do not diverge from it without updating both sides.
//
// This file deliberately mirrors xingmang-platform's
// internal/platform/jobs/fleet_keyring.go keyring shape and validation
// rules (same reviewed-manifest pattern, a different signing domain --
// purpose/protocol below instead of the job-fleet ones, so a signature from
// one domain can never be replayed as the other). It does not import that
// package (different module, different repo); the shapes are intentionally
// kept parallel by hand.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

const (
	consoleAssertionAlgorithm = "EdDSA"
	consoleAssertionType      = "xm-console-assertion+jwt"
	consoleAssertionPurpose   = "console_admin_assertion_signing"
	consoleAssertionProtocol  = "xm-console-assertion-v1"

	// consoleAssertionRequiredACR is the assertion's own "acr" claim: a fixed
	// protocol constant proving "the console verified TOTP recently", not a
	// deployment-configured value. It is intentionally distinct from
	// AdminPolicy.RequiredACR (today OIDC_REQUIRED_ADMIN_ACR, e.g. Keycloak's
	// urn:solov:loa:2 in production): the two must be able to coexist during
	// CR-0006 phase 1's parallel run, so a verified assertion's Principal.ACR
	// is set to the CALLER's currently configured AdminPolicy.RequiredACR
	// (see ConsolePrincipalFromClaims), never to this constant directly --
	// that keeps AuthorizeSession's single-value equality check unchanged
	// for both OIDC and assertion sessions at once, matching CR-0006's "查判
	// 定代码不需要改一行" design goal for the two paths to genuinely coexist
	// rather than only working when reconfigured one way or the other.
	consoleAssertionRequiredACR = "xingmang-console-totp-v1"

	consoleAssertionMaxLifetime = 5 * time.Minute
	consoleAssertionClockSkew   = 60 * time.Second
	consoleAssertionMaxRoles    = 100
	consoleAssertionMaxAMR      = 32
	consoleAssertionMaxKeyIDLen = 128
	consoleAssertionMaxUsername = 128
	consoleAssertionNonceBytes  = 32
	// consoleAssertionMaxSize bounds the raw compact-serialization string
	// before any parsing, independent of validateJWTJSONSegments' own
	// per-segment bound -- test matrix #20 ("断言体积异常").
	consoleAssertionMaxSize = 16 * 1024
)

// ErrConsoleAssertionInvalid is the single externally-observable outcome for
// every rejection: bad signature, unknown/revoked kid, expired, wrong
// iss/aud/acr, missing amr, replayed nonce, malformed structure. This
// matches the codebase's existing "one failure code, log the real reason
// server-side" rule (OIDC token verification, session lookup, login
// password checks all do the same) -- see the design spec's "设计取
// 舍" note: letting a caller distinguish reasons turns this endpoint into a
// free oracle (e.g. "is this nonce already used"). Every sentinel below
// wraps this one with errors.Is, so callers that only care about "was it
// valid" need check nothing else, while the specific error is still
// available for server-side audit logging (never sent to the client).
var ErrConsoleAssertionInvalid = errors.New("console assertion is invalid, expired, or already used")

var (
	errConsoleAssertionMalformed     = fmt.Errorf("%w: malformed compact serialization", ErrConsoleAssertionInvalid)
	errConsoleAssertionBadAlgorithm  = fmt.Errorf("%w: unsupported or missing algorithm", ErrConsoleAssertionInvalid)
	errConsoleAssertionBadType       = fmt.Errorf("%w: unexpected typ", ErrConsoleAssertionInvalid)
	errConsoleAssertionUnknownKey    = fmt.Errorf("%w: signing key not found for kid", ErrConsoleAssertionInvalid)
	errConsoleAssertionBadSignature  = fmt.Errorf("%w: signature verification failed", ErrConsoleAssertionInvalid)
	errConsoleAssertionBadLifetime   = fmt.Errorf("%w: exp-iat exceeds the structural maximum", ErrConsoleAssertionInvalid)
	errConsoleAssertionExpired       = fmt.Errorf("%w: outside the iat/nbf/exp validity window", ErrConsoleAssertionInvalid)
	errConsoleAssertionBadIssuer     = fmt.Errorf("%w: iss does not match the configured console issuer", ErrConsoleAssertionInvalid)
	errConsoleAssertionBadAudience   = fmt.Errorf("%w: aud does not match the configured audience", ErrConsoleAssertionInvalid)
	errConsoleAssertionBadACR        = fmt.Errorf("%w: acr does not match the console-assertion domain constant", ErrConsoleAssertionInvalid)
	errConsoleAssertionMissingAMR    = fmt.Errorf("%w: amr does not include pwd and otp", ErrConsoleAssertionInvalid)
	errConsoleAssertionMissingRole   = fmt.Errorf("%w: roles does not include the configured administrator role", ErrConsoleAssertionInvalid)
	errConsoleAssertionBadScope      = fmt.Errorf("%w: scope is not one of sub2api/newapi/global", ErrConsoleAssertionInvalid)
	errConsoleAssertionBadNonce      = fmt.Errorf("%w: nonce is not a 32-byte base64url value", ErrConsoleAssertionInvalid)
	errConsoleAssertionBadClaimShape = fmt.Errorf("%w: a claim field is missing, oversized, or contains control characters", ErrConsoleAssertionInvalid)
	errConsoleAssertionReplayed      = fmt.Errorf("%w: nonce already consumed", ErrConsoleAssertionInvalid)
)

// ConsoleAssertionTrustedKey is a public-only trust record, one entry of the
// static keyring manifest (contracts/auth/console-assertion-keyring.v1.json
// in both repos). Field shapes and validation rules mirror xingmang-
// platform's JobFleetTrustedKey exactly.
type ConsoleAssertionTrustedKey struct {
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

// ConsoleAssertionKeyring is an immutable in-memory keyring loaded from a
// reviewed repository artifact at process startup.
type ConsoleAssertionKeyring struct {
	keys map[string]ConsoleAssertionTrustedKey
}

// NewConsoleAssertionKeyring validates and copies public trust records. At
// least one key is required: an operator turning CONSOLE_ASSERTION_ENABLED
// on with an empty manifest almost certainly forgot to fill it in, and
// should see a loud startup failure rather than a verifier that silently
// rejects every assertion forever.
func NewConsoleAssertionKeyring(records []ConsoleAssertionTrustedKey) (*ConsoleAssertionKeyring, error) {
	if len(records) == 0 {
		return nil, errors.New("console assertion keyring: no trusted keys")
	}
	keyring := &ConsoleAssertionKeyring{keys: make(map[string]ConsoleAssertionTrustedKey, len(records))}
	seenMaterial := make(map[string]string, len(records))
	for index, record := range records {
		if err := validateConsoleAssertionKeyMetadata(index, record); err != nil {
			return nil, err
		}
		if index > 0 && records[index-1].KeyID >= record.KeyID {
			return nil, errors.New("console assertion keyring: records must be sorted by key id")
		}
		public, err := decodeConsoleAssertionPublicKey(record.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("console assertion keyring: key %q: %w", record.KeyID, err)
		}
		fingerprint := sha256Hex(string(public))
		if record.Fingerprint != fingerprint {
			return nil, fmt.Errorf("console assertion keyring: key %q fingerprint mismatch", record.KeyID)
		}
		if _, exists := keyring.keys[record.KeyID]; exists {
			return nil, fmt.Errorf("console assertion keyring: duplicate key id %q", record.KeyID)
		}
		if previous, exists := seenMaterial[fingerprint]; exists {
			return nil, fmt.Errorf("console assertion keyring: public material reused by %q and %q", previous, record.KeyID)
		}
		seenMaterial[fingerprint] = record.KeyID
		keyring.keys[record.KeyID] = cloneConsoleAssertionTrustedKey(record)
	}
	return keyring, nil
}

func validateConsoleAssertionKeyMetadata(index int, record ConsoleAssertionTrustedKey) error {
	if record.KeyID == "" || len(record.KeyID) > consoleAssertionMaxKeyIDLen || strings.TrimSpace(record.KeyID) != record.KeyID || hasControl(record.KeyID) {
		return fmt.Errorf("console assertion keyring: key[%d]: invalid key id", index)
	}
	if record.Algorithm != "Ed25519" {
		return fmt.Errorf("console assertion keyring: key %q algorithm must be Ed25519", record.KeyID)
	}
	if record.Purpose != consoleAssertionPurpose || record.Protocol != consoleAssertionProtocol {
		return fmt.Errorf("console assertion keyring: key %q purpose/protocol is not the console-assertion domain", record.KeyID)
	}
	if record.ValidFrom.IsZero() || record.ValidUntil.IsZero() || !record.ValidFrom.Before(record.ValidUntil) {
		return fmt.Errorf("console assertion keyring: key %q invalid validity", record.KeyID)
	}
	if record.ValidFrom.Location() != time.UTC || record.ValidUntil.Location() != time.UTC {
		return fmt.Errorf("console assertion keyring: key %q validity must be UTC", record.KeyID)
	}
	if record.RevokedAt != nil {
		if record.RevokedAt.Location() != time.UTC || record.RevokedAt.Before(record.ValidFrom) {
			return fmt.Errorf("console assertion keyring: key %q invalid revocation", record.KeyID)
		}
		if !record.RevokedAt.Before(record.ValidUntil) {
			return fmt.Errorf("console assertion keyring: key %q revocation must precede expiry", record.KeyID)
		}
		if strings.TrimSpace(record.RevokeReason) == "" {
			return fmt.Errorf("console assertion keyring: key %q revocation reason required", record.KeyID)
		}
	}
	return nil
}

func cloneConsoleAssertionTrustedKey(record ConsoleAssertionTrustedKey) ConsoleAssertionTrustedKey {
	clone := record
	if record.RevokedAt != nil {
		at := *record.RevokedAt
		clone.RevokedAt = &at
	}
	return clone
}

// Len returns the number of trusted public keys.
func (keyring *ConsoleAssertionKeyring) Len() int {
	if keyring == nil {
		return 0
	}
	return len(keyring.keys)
}

// lookup requires an exact key ID and a half-open validity interval at
// signedAt (the assertion's own iat, not wall-clock time -- see this file's
// package doc comment). purpose/protocol are fixed to this domain's
// constants, not caller-supplied, unlike the platform's job-fleet keyring
// which serves two domains from one file.
func (keyring *ConsoleAssertionKeyring) lookup(keyID string, signedAt time.Time) (ConsoleAssertionTrustedKey, error) {
	if keyring == nil {
		return ConsoleAssertionTrustedKey{}, errConsoleAssertionUnknownKey
	}
	record, ok := keyring.keys[keyID]
	if !ok || record.Purpose != consoleAssertionPurpose || record.Protocol != consoleAssertionProtocol {
		return ConsoleAssertionTrustedKey{}, errConsoleAssertionUnknownKey
	}
	if signedAt.IsZero() {
		return ConsoleAssertionTrustedKey{}, errConsoleAssertionExpired
	}
	signedAt = signedAt.UTC()
	if signedAt.Before(record.ValidFrom) || !signedAt.Before(record.ValidUntil) {
		return ConsoleAssertionTrustedKey{}, errConsoleAssertionUnknownKey
	}
	if record.RevokedAt != nil && !signedAt.Before(record.RevokedAt.UTC()) {
		return ConsoleAssertionTrustedKey{}, errConsoleAssertionUnknownKey
	}
	return cloneConsoleAssertionTrustedKey(record), nil
}

type consoleAssertionTrustedKeyJSON struct {
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

// LoadConsoleAssertionKeyringJSON strictly decodes a public keyring manifest
// (contracts/auth/console-assertion-keyring.v1.json's shape). Unknown
// fields, duplicate object keys, and trailing JSON values are rejected --
// same discipline as xingmang-platform's LoadJobFleetKeyringJSON.
func LoadConsoleAssertionKeyringJSON(raw []byte) (*ConsoleAssertionKeyring, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, fmt.Errorf("console assertion keyring: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var wire []consoleAssertionTrustedKeyJSON
	if err := decoder.Decode(&wire); err != nil {
		return nil, fmt.Errorf("console assertion keyring: decode: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("console assertion keyring: trailing JSON value")
		}
		return nil, fmt.Errorf("console assertion keyring: trailing data: %w", err)
	}
	records := make([]ConsoleAssertionTrustedKey, 0, len(wire))
	for index, item := range wire {
		validFrom, err := parseConsoleAssertionTime(item.ValidFrom)
		if err != nil {
			return nil, fmt.Errorf("console assertion keyring: key[%d] valid_from: %w", index, err)
		}
		validUntil, err := parseConsoleAssertionTime(item.ValidUntil)
		if err != nil {
			return nil, fmt.Errorf("console assertion keyring: key[%d] valid_until: %w", index, err)
		}
		var revokedAt *time.Time
		if item.RevokedAt != nil {
			parsed, err := parseConsoleAssertionTime(*item.RevokedAt)
			if err != nil {
				return nil, fmt.Errorf("console assertion keyring: key[%d] revoked_at: %w", index, err)
			}
			revokedAt = &parsed
		}
		records = append(records, ConsoleAssertionTrustedKey{
			KeyID: item.KeyID, Algorithm: item.Algorithm, PublicKey: item.PublicKey,
			Fingerprint: item.Fingerprint, Purpose: item.Purpose, Protocol: item.Protocol,
			ValidFrom: validFrom, ValidUntil: validUntil, RevokedAt: revokedAt,
			RevokeReason: item.RevokeReason,
		})
	}
	return NewConsoleAssertionKeyring(records)
}

func parseConsoleAssertionTime(value string) (time.Time, error) {
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

func decodeConsoleAssertionPublicKey(encoded string) (ed25519.PublicKey, error) {
	if strings.TrimSpace(encoded) != encoded {
		return nil, errors.New("public key has surrounding whitespace")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(decoded) != encoded {
		return nil, errors.New("invalid Ed25519 public key")
	}
	return ed25519.PublicKey(decoded), nil
}

// ConsoleAssertionConfig is the deployment-configured half of the contract:
// CONSOLE_ASSERTION_ISSUER / CONSOLE_ASSERTION_AUDIENCE /
// CONSOLE_ASSERTION_ADMIN_ROLE.
//
// AdminRole is the role name an assertion's own `roles` claim must contain
// before it can be exchanged for an administrator session. It is configured
// separately from OIDCConfig.AdminRole because the two issuers keep separate
// role vocabularies: the console signs a staff account's roles verbatim
// (admin, credential-admin, staff ...), while the transitional Keycloak login
// carries this deployment's realm role (invoice-admin). Both paths are live
// at once during CR-0006 phase 2, so one configured name cannot serve both --
// XM-INV-CONSOLE-ASSERT-ADMIN-ROLE. Where it is loaded (cmd/api/runtime.go)
// it defaults to OIDCConfig.AdminRole, so a deployment that never sets the
// new variable behaves exactly as it did before.
type ConsoleAssertionConfig struct {
	Issuer    string
	Audience  string
	AdminRole string
}

func (c ConsoleAssertionConfig) Validate() error {
	if err := validateExactHTTPSURL(c.Issuer, true); err != nil {
		return fmt.Errorf("console assertion issuer: %w", err)
	}
	if strings.TrimSpace(c.Audience) == "" || strings.TrimSpace(c.Audience) != c.Audience || len(c.Audience) > 256 || hasControl(c.Audience) {
		return errors.New("console assertion audience is required")
	}
	if strings.TrimSpace(c.AdminRole) == "" || strings.TrimSpace(c.AdminRole) != c.AdminRole || len(c.AdminRole) > 512 || hasControl(c.AdminRole) {
		return errors.New("console assertion administrator role is required")
	}
	return nil
}

// ConsoleAssertionClaims is the fully verified, decoded payload -- returned
// only once signature and every structural/temporal/claim check in this
// file has passed. It deliberately excludes nonce replay state: that is a
// stateful, storage-backed check the caller performs separately via
// ConsoleAssertionNonceStore (see ConsumeNonce), keeping this verifier pure
// and unit-testable without a database.
type ConsoleAssertionClaims struct {
	Subject   string
	Username  string
	Roles     []string
	ACR       string
	AMR       []string
	Scope     string
	Nonce     string
	IssuedAt  time.Time
	ExpiresAt time.Time
	// KeyID is the header's kid, kept on the verified result purely for
	// server-side audit logging (see the design spec section 7's "kid"
	// audit field) -- it plays no further role in verification once the
	// keyring lookup above has already succeeded.
	KeyID string
}

type consoleAssertionHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

type consoleAssertionPayload struct {
	Iss      string   `json:"iss"`
	Aud      string   `json:"aud"`
	Sub      string   `json:"sub"`
	Username string   `json:"username"`
	Roles    []string `json:"roles"`
	ACR      string   `json:"acr"`
	AMR      []string `json:"amr"`
	Scope    string   `json:"scope"`
	Nonce    string   `json:"nonce"`
	Iat      int64    `json:"iat"`
	Exp      int64    `json:"exp"`
	Nbf      int64    `json:"nbf"`
}

// VerifyConsoleAssertion checks the JWS compact serialization's structure,
// EdDSA signature against the keyring, and every claim constraint from the
// design spec section 3.2 except nonce replay (see ConsoleAssertionClaims'
// doc comment). The role the `roles` claim must contain is cfg.AdminRole,
// the console administrator role this deployment configures -- read from the
// config rather than accepted as a separate argument, so a caller cannot pass
// a role name belonging to a different issuer's vocabulary, which is exactly
// how XM-INV-CONSOLE-ASSERT-ADMIN-ROLE reached production. Rejecting a
// non-admin assertion here, not merely at a later Require("admin", ...)
// check, matches this endpoint's sole purpose (issuing an administrator
// session) and the design spec's own error table folding "roles 不含所需角色"
// into the same unified ASSERTION_INVALID outcome.
func VerifyConsoleAssertion(raw string, keyring *ConsoleAssertionKeyring, cfg ConsoleAssertionConfig, now time.Time) (ConsoleAssertionClaims, error) {
	if len(raw) == 0 || len(raw) > consoleAssertionMaxSize || strings.ContainsAny(raw, " \t\r\n\x00") {
		return ConsoleAssertionClaims{}, errConsoleAssertionMalformed
	}
	if err := validateJWTJSONSegments(raw); err != nil {
		return ConsoleAssertionClaims{}, errConsoleAssertionMalformed
	}
	parts := strings.Split(raw, ".")
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ConsoleAssertionClaims{}, errConsoleAssertionMalformed
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ConsoleAssertionClaims{}, errConsoleAssertionMalformed
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != ed25519.SignatureSize {
		return ConsoleAssertionClaims{}, errConsoleAssertionMalformed
	}

	var header consoleAssertionHeader
	if err = strictUnmarshal(headerJSON, &header); err != nil {
		return ConsoleAssertionClaims{}, errConsoleAssertionMalformed
	}
	if header.Alg != consoleAssertionAlgorithm {
		return ConsoleAssertionClaims{}, errConsoleAssertionBadAlgorithm
	}
	if header.Typ != consoleAssertionType {
		return ConsoleAssertionClaims{}, errConsoleAssertionBadType
	}
	if header.Kid == "" || len(header.Kid) > consoleAssertionMaxKeyIDLen || hasControl(header.Kid) {
		return ConsoleAssertionClaims{}, errConsoleAssertionUnknownKey
	}

	var payload consoleAssertionPayload
	if err = strictUnmarshal(payloadJSON, &payload); err != nil {
		return ConsoleAssertionClaims{}, errConsoleAssertionMalformed
	}

	if payload.Iat <= 0 || payload.Exp <= 0 || payload.Nbf <= 0 {
		return ConsoleAssertionClaims{}, errConsoleAssertionBadClaimShape
	}
	issuedAt := time.Unix(payload.Iat, 0).UTC()
	expiresAt := time.Unix(payload.Exp, 0).UTC()
	notBefore := time.Unix(payload.Nbf, 0).UTC()
	if !notBefore.Equal(issuedAt) {
		return ConsoleAssertionClaims{}, errConsoleAssertionBadClaimShape
	}
	// Structural lifetime cap applies regardless of clock skew: a signature
	// that is otherwise perfectly valid must still be rejected if it claims
	// a longer-than-permitted lifetime (test matrix #5).
	if !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > consoleAssertionMaxLifetime {
		return ConsoleAssertionClaims{}, errConsoleAssertionBadLifetime
	}

	key, err := keyring.lookup(header.Kid, issuedAt)
	if err != nil {
		return ConsoleAssertionClaims{}, err
	}
	publicKey, err := decodeConsoleAssertionPublicKey(key.PublicKey)
	if err != nil {
		return ConsoleAssertionClaims{}, errConsoleAssertionUnknownKey
	}
	signedData := []byte(parts[0] + "." + parts[1])
	if !ed25519.Verify(publicKey, signedData, signature) {
		return ConsoleAssertionClaims{}, errConsoleAssertionBadSignature
	}

	now = now.UTC()
	if issuedAt.After(now.Add(consoleAssertionClockSkew)) || !expiresAt.After(now.Add(-consoleAssertionClockSkew)) || notBefore.After(now.Add(consoleAssertionClockSkew)) {
		return ConsoleAssertionClaims{}, errConsoleAssertionExpired
	}
	if payload.Iss != cfg.Issuer {
		return ConsoleAssertionClaims{}, errConsoleAssertionBadIssuer
	}
	if payload.Aud != cfg.Audience {
		return ConsoleAssertionClaims{}, errConsoleAssertionBadAudience
	}
	if payload.ACR != consoleAssertionRequiredACR {
		return ConsoleAssertionClaims{}, errConsoleAssertionBadACR
	}
	if err = validateConsoleAssertionClaimSet(payload.Roles, consoleAssertionMaxRoles); err != nil {
		return ConsoleAssertionClaims{}, errConsoleAssertionBadClaimShape
	}
	if len(payload.Roles) == 0 || !slices.Contains(payload.Roles, cfg.AdminRole) {
		return ConsoleAssertionClaims{}, errConsoleAssertionMissingRole
	}
	if err = validateConsoleAssertionClaimSet(payload.AMR, consoleAssertionMaxAMR); err != nil {
		return ConsoleAssertionClaims{}, errConsoleAssertionBadClaimShape
	}
	if !slices.Contains(payload.AMR, "pwd") || !slices.Contains(payload.AMR, "otp") {
		return ConsoleAssertionClaims{}, errConsoleAssertionMissingAMR
	}
	if payload.Scope != "sub2api" && payload.Scope != "newapi" && payload.Scope != "global" {
		return ConsoleAssertionClaims{}, errConsoleAssertionBadScope
	}
	nonceBytes, err := base64.RawURLEncoding.DecodeString(payload.Nonce)
	if err != nil || len(nonceBytes) != consoleAssertionNonceBytes || base64.RawURLEncoding.EncodeToString(nonceBytes) != payload.Nonce {
		return ConsoleAssertionClaims{}, errConsoleAssertionBadNonce
	}
	if strings.TrimSpace(payload.Sub) == "" || strings.TrimSpace(payload.Sub) != payload.Sub || len(payload.Sub) > 512 || hasControl(payload.Sub) {
		return ConsoleAssertionClaims{}, errConsoleAssertionBadClaimShape
	}
	if len(payload.Username) > consoleAssertionMaxUsername || hasControl(payload.Username) {
		return ConsoleAssertionClaims{}, errConsoleAssertionBadClaimShape
	}

	return ConsoleAssertionClaims{
		Subject: payload.Sub, Username: payload.Username,
		Roles: append([]string{}, payload.Roles...), ACR: payload.ACR,
		AMR: append([]string{}, payload.AMR...), Scope: payload.Scope,
		Nonce: payload.Nonce, IssuedAt: issuedAt, ExpiresAt: expiresAt,
		KeyID: header.Kid,
	}, nil
}

func validateConsoleAssertionClaimSet(values []string, limit int) error {
	if len(values) > limit {
		return errors.New("claim set is too large")
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value || len(value) > 512 || hasControl(value) {
			return errors.New("claim set contains an invalid value")
		}
	}
	return nil
}

func strictUnmarshal(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

// ConsolePrincipalFromClaims maps verified claims onto the same Principal
// shape the OIDC callback produces, so it enters session issuance through
// the identical Platform=="" path (identity.go's ResolveOrCreate,
// cmd/api/runtime.go's provisionPlatformOrOIDCUser) -- CR-0006 change list
// item (e) requires zero changes to that path.
//
// Two claims are deliberately TRANSLATED rather than copied, because the
// console and this system keep separate vocabularies for them and the
// principal must speak this system's:
//
//   - requiredACR is the caller's CURRENTLY CONFIGURED
//     AdminPolicy.RequiredACR (not the fixed consoleAssertionRequiredACR
//     constant already checked inside VerifyConsoleAssertion) -- see that
//     constant's doc comment for why.
//   - adminRole is the caller's CURRENTLY CONFIGURED AdminPolicy.Role, and
//     becomes the principal's only role. VerifyConsoleAssertion has already
//     required the assertion to carry the configured console administrator
//     role, so that fact is established; carrying the console's own role
//     names through instead would leave a foreign vocabulary in invoice
//     sessions that still fails every AdminPolicy check, which is precisely
//     what XM-INV-CONSOLE-ASSERT-ADMIN-ROLE observed in production. The
//     console's raw roles stay in the verified claims for auditing.
func ConsolePrincipalFromClaims(claims ConsoleAssertionClaims, issuer, requiredACR, adminRole string) Principal {
	return Principal{
		Issuer: issuer, Subject: claims.Subject, DisplayName: claims.Username,
		Roles: []string{adminRole}, ACR: requiredACR,
		AMR: append([]string{}, claims.AMR...), AuthTime: claims.IssuedAt,
	}
}

// ConsoleAssertionNonceStore provides single-use consumption of a console
// assertion's nonce. ConsumeNonce must be atomic (INSERT ... ON CONFLICT DO
// NOTHING or equivalent): of two concurrent callers racing to consume the
// same nonce, exactly one may report ok=true.
type ConsoleAssertionNonceStore interface {
	ConsumeNonce(ctx context.Context, nonceHash string, expiresAt time.Time) (bool, error)
}
