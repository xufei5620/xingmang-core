package consoleassertion

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// fakeKeyProvider is a minimal SigningKeyProvider backed by an in-memory map,
// so Signer tests never touch a real file/CredentialRef store.
type fakeKeyProvider struct {
	values map[string]string // ref.String() -> secret_value
	err    error
}

func (f *fakeKeyProvider) Resolve(_ context.Context, ref secrets.CredentialRef, _ string) (secrets.SecretValue, error) {
	if f.err != nil {
		return secrets.SecretValue{}, f.err
	}
	v, ok := f.values[ref.String()]
	if !ok {
		return secrets.SecretValue{}, errors.New("not found")
	}
	return secrets.NewSecretValue([]byte(v)), nil
}

func newFakeSigner(t *testing.T, keyID string) (*Signer, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	ref := secrets.MustCredentialRef("secret://console-assertion/signing-key-" + keyID)
	provider := &fakeKeyProvider{values: map[string]string{
		ref.String(): base64.StdEncoding.EncodeToString(priv.Seed()),
	}}
	signer, err := NewSigner(context.Background(), provider, ref)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return signer, pub
}

func TestNewSignerDerivesKeyIDFromRefName(t *testing.T) {
	signer, _ := newFakeSigner(t, "2026-09")
	if signer.KeyID() != "2026-09" {
		t.Fatalf("KeyID() = %q, want 2026-09", signer.KeyID())
	}
}

func TestNewSignerRejectsRefWithoutConventionPrefix(t *testing.T) {
	ref := secrets.MustCredentialRef("secret://console-assertion/2026-09")
	provider := &fakeKeyProvider{values: map[string]string{}}
	if _, err := NewSigner(context.Background(), provider, ref); err == nil {
		t.Fatal("expected rejection of a ref name without the signing-key- prefix")
	}
}

func TestNewSignerRejectsMalformedSeed(t *testing.T) {
	ref := secrets.MustCredentialRef("secret://console-assertion/signing-key-2026-09")
	provider := &fakeKeyProvider{values: map[string]string{ref.String(): "not-base64!!"}}
	if _, err := NewSigner(context.Background(), provider, ref); err == nil {
		t.Fatal("expected rejection of a non-base64 secret value")
	}
	provider = &fakeKeyProvider{values: map[string]string{ref.String(): base64.StdEncoding.EncodeToString([]byte("too-short"))}}
	if _, err := NewSigner(context.Background(), provider, ref); err == nil {
		t.Fatal("expected rejection of a seed that is not exactly ed25519.SeedSize bytes")
	}
}

func TestNewSignerPropagatesResolveError(t *testing.T) {
	ref := secrets.MustCredentialRef("secret://console-assertion/signing-key-2026-09")
	provider := &fakeKeyProvider{err: errors.New("boom")}
	if _, err := NewSigner(context.Background(), provider, ref); err == nil {
		t.Fatal("expected the provider's error to propagate")
	}
}

// assertionClaimsForTest freezes the consumer's JSON names and types. Do not
// alias wireClaims: a producer-only JSON tag change must fail this contract.
type assertionClaimsForTest struct {
	Issuer    string   `json:"iss"`
	Audience  string   `json:"aud"`
	Subject   string   `json:"sub"`
	Username  string   `json:"username"`
	Roles     []string `json:"roles"`
	ACR       string   `json:"acr"`
	AMR       []string `json:"amr"`
	Scope     string   `json:"scope"`
	Nonce     string   `json:"nonce"`
	IssuedAt  int64    `json:"iat"`
	NotBefore int64    `json:"nbf"`
	ExpiresAt int64    `json:"exp"`
}

// verifyForTest independently reimplements the invoice side's verification
// rules (per the frozen technical specification's §3.2/§3.3, not by
// importing anything from this package's own signing path) -- this is the
// "cross-check test" the slice's own scope calls for: proof that a JWS this
// package produces is acceptable to a verifier built from the spec alone,
// not merely self-consistent with this package's own signCompact.
func verifyForTest(t *testing.T, compact string, pub ed25519.PublicKey, now time.Time) assertionClaimsForTest {
	t.Helper()
	parts := strings.Split(compact, ".")
	if len(parts) != 3 {
		t.Fatalf("compact JWS must have exactly 3 segments, got %d", len(parts))
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}
	decoder := json.NewDecoder(bytes.NewReader(headerJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if header.Alg != "EdDSA" {
		t.Fatalf("alg = %q, want EdDSA (no algorithm negotiation permitted)", header.Alg)
	}
	if header.Typ != "xm-console-assertion+jwt" {
		t.Fatalf("typ = %q, want the dedicated xm-console-assertion+jwt value", header.Typ)
	}
	if header.Kid != "2026-09" {
		t.Fatalf("kid = %q, want the selected signing key", header.Kid)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	signingInput := parts[0] + "." + parts[1]
	if !ed25519.Verify(pub, []byte(signingInput), sig) {
		t.Fatal("Ed25519 signature does not verify against the declared public key")
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims assertionClaimsForTest
	decoder = json.NewDecoder(bytes.NewReader(payloadJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&claims); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if claims.ExpiresAt-claims.IssuedAt > 300 {
		t.Fatalf("exp-iat = %ds, exceeds the 5-minute structural cap", claims.ExpiresAt-claims.IssuedAt)
	}
	if claims.NotBefore != claims.IssuedAt {
		t.Fatalf("nbf (%d) != iat (%d)", claims.NotBefore, claims.IssuedAt)
	}
	if claims.IssuedAt != now.Unix() || claims.ExpiresAt != now.Add(5*time.Minute).Unix() {
		t.Fatal("iat/exp must encode the signing time and the frozen five-minute lifetime")
	}
	return claims
}

func TestSignProducesAssertionThatVerifiesAgainstAnIndependentVerifier(t *testing.T) {
	signer, pub := newFakeSigner(t, "2026-09")
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	compact, expiresAt, nonce, err := signer.Sign(now, ClaimsInput{
		Issuer: "https://console.solov.cc", Audience: DefaultAudience, Subject: "staff-uuid",
		Username: "alice", Roles: []string{"admin"}, Scope: "sub2api",
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if expiresAt.Sub(now) != 5*time.Minute {
		t.Fatalf("expiresAt-now = %s, want exactly five minutes", expiresAt.Sub(now))
	}
	claims := verifyForTest(t, compact, pub, now)
	if claims.Issuer != "https://console.solov.cc" || claims.Audience != "xingmang-console-assertion-v1" {
		t.Fatalf("iss/aud mismatch: %+v", claims)
	}
	if claims.Subject != "staff-uuid" || claims.Username != "alice" {
		t.Fatalf("sub/username mismatch: %+v", claims)
	}
	if len(claims.Roles) != 1 || claims.Roles[0] != "admin" {
		t.Fatalf("roles mismatch: %+v", claims.Roles)
	}
	if claims.ACR != "xingmang-console-totp-v1" {
		t.Fatalf("acr = %q, want the frozen console TOTP domain", claims.ACR)
	}
	if len(claims.AMR) != 2 || claims.AMR[0] != "pwd" || claims.AMR[1] != "otp" {
		t.Fatalf("amr mismatch: %+v", claims.AMR)
	}
	if claims.Scope != "sub2api" {
		t.Fatalf("scope mismatch: %+v", claims.Scope)
	}
	if claims.Nonce != nonce {
		t.Fatalf("Sign's returned nonce (%q) does not match the claim actually embedded (%q)", nonce, claims.Nonce)
	}
	decodedNonce, err := base64.RawURLEncoding.DecodeString(claims.Nonce)
	if err != nil || len(decodedNonce) != 32 {
		t.Fatal("nonce is not a 32-byte base64url value")
	}
}

func TestSignTamperedPayloadFailsVerification(t *testing.T) {
	signer, pub := newFakeSigner(t, "2026-09")
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	compact, _, _, err := signer.Sign(now, ClaimsInput{
		Issuer: "https://console.solov.cc", Audience: DefaultAudience, Subject: "staff-uuid", Scope: "sub2api",
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	parts := strings.Split(compact, ".")
	payloadJSON, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	_ = json.Unmarshal(payloadJSON, &claims)
	claims["scope"] = "global" // tamper: escalate scope without re-signing
	tamperedPayload, _ := json.Marshal(claims)
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString(tamperedPayload) + "." + parts[2]

	sig, err := base64.RawURLEncoding.DecodeString(strings.Split(tampered, ".")[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	signingInput := strings.Split(tampered, ".")[0] + "." + strings.Split(tampered, ".")[1]
	if ed25519.Verify(pub, []byte(signingInput), sig) {
		t.Fatal("tampered payload must not verify")
	}
}

func TestSignEachCallUsesAFreshNonce(t *testing.T) {
	signer, _ := newFakeSigner(t, "2026-09")
	now := time.Now().UTC()
	_, _, nonce1, err := signer.Sign(now, ClaimsInput{Issuer: "https://console.solov.cc", Scope: "sub2api"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	_, _, nonce2, err := signer.Sign(now, ClaimsInput{Issuer: "https://console.solov.cc", Scope: "sub2api"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if nonce1 == nonce2 {
		t.Fatal("two calls to Sign produced the same nonce")
	}
}

func TestSignClonesRolesDefensively(t *testing.T) {
	signer, _ := newFakeSigner(t, "2026-09")
	roles := []string{"admin"}
	compact, _, _, err := signer.Sign(time.Now().UTC(), ClaimsInput{
		Issuer: "https://console.solov.cc", Scope: "sub2api", Roles: roles,
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	roles[0] = "mutated" // caller mutates its own slice after Sign has returned

	parts := strings.Split(compact, ".")
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims wireClaims
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if len(claims.Roles) != 1 || claims.Roles[0] != "admin" {
		t.Fatalf("already-issued token's roles claim changed after the caller mutated its own slice: %+v", claims.Roles)
	}
}
