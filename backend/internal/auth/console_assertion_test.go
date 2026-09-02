package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

const (
	testConsoleAssertionIssuer   = "https://console.solov.cc"
	testConsoleAssertionAudience = "xingmang-console-assertion-v1"
	testConsoleAssertionRole     = "invoice-admin"
)

func testConsoleAssertionCfg() ConsoleAssertionConfig {
	return ConsoleAssertionConfig{Issuer: testConsoleAssertionIssuer, Audience: testConsoleAssertionAudience}
}

// consoleAssertionKeyFixture bundles a generated Ed25519 keypair with a
// one-key keyring trusting it, so each test can tailor the key's own
// validity/revocation window independently.
type consoleAssertionKeyFixture struct {
	private ed25519.PrivateKey
	keyring *ConsoleAssertionKeyring
}

func newConsoleAssertionKeyFixture(t *testing.T, keyID string, validFrom, validUntil time.Time, revokedAt *time.Time) consoleAssertionKeyFixture {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	reason := ""
	if revokedAt != nil {
		reason = "test revocation"
	}
	keyring, err := NewConsoleAssertionKeyring([]ConsoleAssertionTrustedKey{{
		KeyID: keyID, Algorithm: "Ed25519", PublicKey: base64.StdEncoding.EncodeToString(public),
		Fingerprint: sha256Hex(string(public)), Purpose: consoleAssertionPurpose, Protocol: consoleAssertionProtocol,
		ValidFrom: validFrom, ValidUntil: validUntil, RevokedAt: revokedAt, RevokeReason: reason,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return consoleAssertionKeyFixture{private: private, keyring: keyring}
}

func randomNonce(t *testing.T) string {
	t.Helper()
	raw := make([]byte, consoleAssertionNonceBytes)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func defaultAssertionHeader(kid string) map[string]any {
	return map[string]any{"alg": "EdDSA", "kid": kid, "typ": "xm-console-assertion+jwt"}
}

func defaultAssertionPayload(t *testing.T, now time.Time) map[string]any {
	iat := now.Unix()
	return map[string]any{
		"iss": testConsoleAssertionIssuer, "aud": testConsoleAssertionAudience,
		"sub": "11111111-1111-4111-8111-111111111111", "username": "test-operator",
		"roles": []string{testConsoleAssertionRole}, "acr": consoleAssertionRequiredACR,
		"amr": []string{"pwd", "otp"}, "scope": "sub2api",
		"nonce": randomNonce(t), "iat": iat, "exp": iat + 300, "nbf": iat,
	}
}

// signAssertion JSON-encodes header/payload maps (letting individual tests
// inject wrong types, missing fields, or extra fields), signs, and returns
// the compact serialization. Using map[string]any rather than a typed
// struct is deliberate: it lets malformed-shape test cases (wrong field
// type, extra unknown field) be expressed as a one-line map mutation
// instead of a second parallel type per case.
func signAssertion(t *testing.T, private ed25519.PrivateKey, header, payload map[string]any) string {
	t.Helper()
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)
	signed := []byte(headerB64 + "." + payloadB64)
	signature := ed25519.Sign(private, signed)
	return headerB64 + "." + payloadB64 + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func TestVerifyConsoleAssertionAcceptsWellFormedAssertion(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-time.Hour), now.Add(time.Hour), nil)
	token := signAssertion(t, fixture.private, defaultAssertionHeader("2026-09"), defaultAssertionPayload(t, now))

	claims, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if claims.Subject != "11111111-1111-4111-8111-111111111111" || claims.Username != "test-operator" || claims.Scope != "sub2api" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if len(claims.Roles) != 1 || claims.Roles[0] != testConsoleAssertionRole {
		t.Fatalf("unexpected roles: %+v", claims.Roles)
	}
	if !claims.IssuedAt.Equal(now) || !claims.ExpiresAt.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("unexpected iat/exp: %+v", claims)
	}
}

func TestVerifyConsoleAssertionClockSkewBoundary(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-2*time.Hour), now.Add(2*time.Hour), nil)

	for _, tc := range []struct {
		name    string
		signAt  time.Time
		wantErr bool
	}{
		{"59s in the future, within skew", now.Add(59 * time.Second), false},
		{"61s in the future, outside skew", now.Add(61 * time.Second), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := defaultAssertionPayload(t, tc.signAt)
			token := signAssertion(t, fixture.private, defaultAssertionHeader("2026-09"), payload)
			_, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now)
			if tc.wantErr && err == nil {
				t.Fatal("expected rejection, got success")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected success, got %v", err)
			}
		})
	}

	// Expiry-side skew: signed 5m01s ago (so exp = iat+300 = now-1s,
	// nominally expired) but still within the 60s tolerance on the exp side.
	nearExpiry := now.Add(-5*time.Minute - time.Second)
	token := signAssertion(t, fixture.private, defaultAssertionHeader("2026-09"), defaultAssertionPayload(t, nearExpiry))
	if _, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now); err != nil {
		t.Fatalf("expected exp-side skew tolerance to accept, got %v", err)
	}
	tooOld := now.Add(-5*time.Minute - 61*time.Second)
	token = signAssertion(t, fixture.private, defaultAssertionHeader("2026-09"), defaultAssertionPayload(t, tooOld))
	if _, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now); err == nil {
		t.Fatal("expected expiry beyond skew tolerance to be rejected")
	}
}

func TestVerifyConsoleAssertionRejectsOverlongLifetimeDespiteValidSignature(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-time.Hour), now.Add(time.Hour), nil)
	payload := defaultAssertionPayload(t, now)
	payload["exp"] = now.Unix() + 360 // 6 minutes, exceeds the 5-minute structural cap
	token := signAssertion(t, fixture.private, defaultAssertionHeader("2026-09"), payload)

	if _, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now); !errors.Is(err, ErrConsoleAssertionInvalid) {
		t.Fatalf("expected rejection for overlong lifetime, got %v", err)
	}
}

func TestVerifyConsoleAssertionRejectsTamperedPayload(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-time.Hour), now.Add(time.Hour), nil)
	token := signAssertion(t, fixture.private, defaultAssertionHeader("2026-09"), defaultAssertionPayload(t, now))

	parts := splitCompact(t, token)
	tamperedPayload, err := json.Marshal(map[string]any{
		"iss": testConsoleAssertionIssuer, "aud": testConsoleAssertionAudience,
		"sub": "22222222-2222-4222-8222-222222222222", "username": "attacker",
		"roles": []string{testConsoleAssertionRole}, "acr": consoleAssertionRequiredACR,
		"amr": []string{"pwd", "otp"}, "scope": "sub2api",
		"nonce": randomNonce(t), "iat": now.Unix(), "exp": now.Unix() + 300, "nbf": now.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString(tamperedPayload) + "." + parts[2]

	if _, err := VerifyConsoleAssertion(tampered, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now); !errors.Is(err, errConsoleAssertionBadSignature) {
		t.Fatalf("expected signature failure, got %v", err)
	}
}

func TestVerifyConsoleAssertionRejectsBadSignatureBytes(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-time.Hour), now.Add(time.Hour), nil)
	token := signAssertion(t, fixture.private, defaultAssertionHeader("2026-09"), defaultAssertionPayload(t, now))
	parts := splitCompact(t, token)
	badSig := make([]byte, ed25519.SignatureSize)
	tampered := parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(badSig)

	if _, err := VerifyConsoleAssertion(tampered, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now); !errors.Is(err, errConsoleAssertionBadSignature) {
		t.Fatalf("expected signature failure, got %v", err)
	}
}

func TestVerifyConsoleAssertionRejectsWrongIssuerOrAudience(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-time.Hour), now.Add(time.Hour), nil)

	for _, tc := range []struct {
		name    string
		mutate  func(map[string]any)
		wantErr error
	}{
		{"wrong iss", func(p map[string]any) { p["iss"] = "https://console.solov.cc/" }, errConsoleAssertionBadIssuer},
		{"wrong iss case", func(p map[string]any) { p["iss"] = "https://Console.solov.cc" }, errConsoleAssertionBadIssuer},
		{"wrong aud", func(p map[string]any) { p["aud"] = "some-other-audience" }, errConsoleAssertionBadAudience},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := defaultAssertionPayload(t, now)
			tc.mutate(payload)
			token := signAssertion(t, fixture.private, defaultAssertionHeader("2026-09"), payload)
			_, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestVerifyConsoleAssertionRejectsUnknownRevokedOrNotYetValidKey(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)

	t.Run("unknown kid", func(t *testing.T) {
		fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-time.Hour), now.Add(time.Hour), nil)
		token := signAssertion(t, fixture.private, defaultAssertionHeader("some-other-kid"), defaultAssertionPayload(t, now))
		if _, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now); !errors.Is(err, errConsoleAssertionUnknownKey) {
			t.Fatalf("expected unknown key rejection, got %v", err)
		}
	})

	t.Run("revoked kid, iat after revocation", func(t *testing.T) {
		revokedAt := now.Add(-time.Minute)
		fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-time.Hour), now.Add(time.Hour), &revokedAt)
		token := signAssertion(t, fixture.private, defaultAssertionHeader("2026-09"), defaultAssertionPayload(t, now))
		if _, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now); !errors.Is(err, errConsoleAssertionUnknownKey) {
			t.Fatalf("expected revoked key rejection, got %v", err)
		}
	})

	t.Run("revoked kid, iat before revocation is still trusted", func(t *testing.T) {
		revokedAt := now.Add(time.Minute)
		fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-time.Hour), now.Add(time.Hour), &revokedAt)
		token := signAssertion(t, fixture.private, defaultAssertionHeader("2026-09"), defaultAssertionPayload(t, now))
		if _, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now); err != nil {
			t.Fatalf("expected pre-revocation signature to still verify, got %v", err)
		}
	})

	t.Run("key not yet valid", func(t *testing.T) {
		fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(time.Hour), now.Add(2*time.Hour), nil)
		token := signAssertion(t, fixture.private, defaultAssertionHeader("2026-09"), defaultAssertionPayload(t, now))
		if _, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now); !errors.Is(err, errConsoleAssertionUnknownKey) {
			t.Fatalf("expected not-yet-valid key rejection, got %v", err)
		}
	})
}

func TestVerifyConsoleAssertionRejectsAlgorithmDowngrade(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-time.Hour), now.Add(time.Hour), nil)

	for _, alg := range []string{"none", "HS256", "RS256", ""} {
		t.Run("alg="+alg, func(t *testing.T) {
			header := defaultAssertionHeader("2026-09")
			header["alg"] = alg
			token := signAssertion(t, fixture.private, header, defaultAssertionPayload(t, now))
			if _, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now); !errors.Is(err, errConsoleAssertionBadAlgorithm) {
				t.Fatalf("expected algorithm rejection, got %v", err)
			}
		})
	}
}

func TestVerifyConsoleAssertionRejectsWrongType(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-time.Hour), now.Add(time.Hour), nil)
	header := defaultAssertionHeader("2026-09")
	header["typ"] = "JWT"
	token := signAssertion(t, fixture.private, header, defaultAssertionPayload(t, now))
	if _, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now); !errors.Is(err, errConsoleAssertionBadType) {
		t.Fatalf("expected typ rejection, got %v", err)
	}
}

func TestVerifyConsoleAssertionRejectsMissingOTP(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-time.Hour), now.Add(time.Hour), nil)

	for _, tc := range []struct {
		name string
		amr  []string
	}{
		{"otp missing", []string{"pwd"}},
		{"pwd missing", []string{"otp"}},
		{"both missing", []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := defaultAssertionPayload(t, now)
			payload["amr"] = tc.amr
			token := signAssertion(t, fixture.private, defaultAssertionHeader("2026-09"), payload)
			_, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now)
			if !errors.Is(err, errConsoleAssertionMissingAMR) {
				t.Fatalf("expected AMR rejection, got %v", err)
			}
		})
	}
}

func TestVerifyConsoleAssertionRejectsMissingAdminRole(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-time.Hour), now.Add(time.Hour), nil)
	payload := defaultAssertionPayload(t, now)
	payload["roles"] = []string{"finance.read"}
	token := signAssertion(t, fixture.private, defaultAssertionHeader("2026-09"), payload)
	if _, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now); !errors.Is(err, errConsoleAssertionMissingRole) {
		t.Fatalf("expected role rejection, got %v", err)
	}
}

func TestVerifyConsoleAssertionRejectsWrongACR(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-time.Hour), now.Add(time.Hour), nil)
	payload := defaultAssertionPayload(t, now)
	payload["acr"] = "urn:solov:loa:2" // a real OIDC ACR value, not the assertion domain constant
	token := signAssertion(t, fixture.private, defaultAssertionHeader("2026-09"), payload)
	if _, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now); !errors.Is(err, errConsoleAssertionBadACR) {
		t.Fatalf("expected ACR rejection, got %v", err)
	}
}

func TestVerifyConsoleAssertionRejectsBadScope(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-time.Hour), now.Add(time.Hour), nil)
	payload := defaultAssertionPayload(t, now)
	payload["scope"] = "everything"
	token := signAssertion(t, fixture.private, defaultAssertionHeader("2026-09"), payload)
	if _, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now); !errors.Is(err, errConsoleAssertionBadScope) {
		t.Fatalf("expected scope rejection, got %v", err)
	}
}

func TestVerifyConsoleAssertionRejectsMalformedNonce(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-time.Hour), now.Add(time.Hour), nil)

	for _, nonce := range []string{"", "too-short", string(make([]byte, 64))} {
		payload := defaultAssertionPayload(t, now)
		payload["nonce"] = nonce
		token := signAssertion(t, fixture.private, defaultAssertionHeader("2026-09"), payload)
		if _, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now); !errors.Is(err, errConsoleAssertionBadNonce) {
			t.Fatalf("nonce=%q: expected nonce rejection, got %v", nonce, err)
		}
	}
}

func TestVerifyConsoleAssertionRejectsMismatchedNbf(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-time.Hour), now.Add(time.Hour), nil)
	payload := defaultAssertionPayload(t, now)
	payload["nbf"] = now.Unix() - 30
	token := signAssertion(t, fixture.private, defaultAssertionHeader("2026-09"), payload)
	if _, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now); !errors.Is(err, errConsoleAssertionBadClaimShape) {
		t.Fatalf("expected nbf-mismatch rejection, got %v", err)
	}
}

func TestVerifyConsoleAssertionRejectsMalformedCompactSerialization(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-time.Hour), now.Add(time.Hour), nil)
	cfg := testConsoleAssertionCfg()

	for name, raw := range map[string]string{
		"empty":               "",
		"two segments":        "aaaa.bbbb",
		"four segments":       "aaaa.bbbb.cccc.dddd",
		"not base64":          "!!!.bbbb.cccc",
		"whitespace injected": "aaaa.\tbbbb.cccc",
		"oversized": func() string {
			padding := make([]byte, consoleAssertionMaxSize+1)
			return base64.RawURLEncoding.EncodeToString(padding) + ".bbbb.cccc"
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyConsoleAssertion(raw, fixture.keyring, cfg, testConsoleAssertionRole, now); !errors.Is(err, ErrConsoleAssertionInvalid) {
				t.Fatalf("expected malformed rejection, got %v", err)
			}
		})
	}
}

func TestVerifyConsoleAssertionRejectsDuplicateJSONKeys(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-time.Hour), now.Add(time.Hour), nil)
	headerJSON := []byte(`{"alg":"EdDSA","kid":"2026-09","kid":"2026-09","typ":"xm-console-assertion+jwt"}`)
	payloadJSON, err := json.Marshal(defaultAssertionPayload(t, now))
	if err != nil {
		t.Fatal(err)
	}
	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)
	signature := ed25519.Sign(fixture.private, []byte(headerB64+"."+payloadB64))
	token := headerB64 + "." + payloadB64 + "." + base64.RawURLEncoding.EncodeToString(signature)

	if _, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now); !errors.Is(err, ErrConsoleAssertionInvalid) {
		t.Fatalf("expected duplicate-key rejection, got %v", err)
	}
}

func TestVerifyConsoleAssertionRejectsUnknownFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	fixture := newConsoleAssertionKeyFixture(t, "2026-09", now.Add(-time.Hour), now.Add(time.Hour), nil)
	payload := defaultAssertionPayload(t, now)
	payload["extra_unexpected_field"] = "surprise"
	token := signAssertion(t, fixture.private, defaultAssertionHeader("2026-09"), payload)
	if _, err := VerifyConsoleAssertion(token, fixture.keyring, testConsoleAssertionCfg(), testConsoleAssertionRole, now); !errors.Is(err, ErrConsoleAssertionInvalid) {
		t.Fatalf("expected unknown-field rejection, got %v", err)
	}
}

func TestConsolePrincipalFromClaimsUsesConfiguredACRNotAssertionConstant(t *testing.T) {
	claims := ConsoleAssertionClaims{
		Subject: "sub-1", Username: "operator", Roles: []string{"invoice-admin"},
		ACR: consoleAssertionRequiredACR, AMR: []string{"pwd", "otp"}, Scope: "global",
		Nonce: "nonce", IssuedAt: time.Now().UTC(),
	}
	// urn:solov:loa:2 is production's real OIDC_REQUIRED_ADMIN_ACR value
	// (deploy/.env.production.example) -- the resulting Principal must carry
	// THIS value, not the assertion's own "xingmang-console-totp-v1" claim,
	// so AuthorizeSession's single exact-match ACR check keeps working
	// unchanged for OIDC and assertion sessions issued side by side.
	principal := ConsolePrincipalFromClaims(claims, testConsoleAssertionIssuer, "urn:solov:loa:2")
	if principal.ACR != "urn:solov:loa:2" {
		t.Fatalf("expected policy-configured ACR to be copied onto the principal, got %q", principal.ACR)
	}
	if principal.Issuer != testConsoleAssertionIssuer || principal.Subject != "sub-1" || principal.DisplayName != "operator" {
		t.Fatalf("unexpected principal: %+v", principal)
	}
	if principal.Platform != "" || principal.PlatformUserID != "" {
		t.Fatalf("assertion principal must use the Platform==\"\" OIDC-style path, got %+v", principal)
	}
}

func TestConsoleAssertionConfigValidate(t *testing.T) {
	if err := testConsoleAssertionCfg().Validate(); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}
	for name, cfg := range map[string]ConsoleAssertionConfig{
		"non-https issuer":  {Issuer: "http://console.solov.cc", Audience: "a"},
		"issuer with query": {Issuer: "https://console.solov.cc?x=1", Audience: "a"},
		"empty audience":    {Issuer: testConsoleAssertionIssuer, Audience: ""},
		"padded audience":   {Issuer: testConsoleAssertionIssuer, Audience: " a "},
	} {
		t.Run(name, func(t *testing.T) {
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestNewConsoleAssertionKeyringRejectsInvalidManifests(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	public, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	validKey := ConsoleAssertionTrustedKey{
		KeyID: "2026-09", Algorithm: "Ed25519", PublicKey: base64.StdEncoding.EncodeToString(public),
		Fingerprint: sha256Hex(string(public)), Purpose: consoleAssertionPurpose, Protocol: consoleAssertionProtocol,
		ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour),
	}

	if _, err := NewConsoleAssertionKeyring(nil); err == nil {
		t.Fatal("expected empty keyring to be rejected")
	}

	wrongAlg := validKey
	wrongAlg.Algorithm = "RSA"
	if _, err := NewConsoleAssertionKeyring([]ConsoleAssertionTrustedKey{wrongAlg}); err == nil {
		t.Fatal("expected non-Ed25519 algorithm to be rejected")
	}

	wrongPurpose := validKey
	wrongPurpose.Purpose = "job_fleet_manifest_signing"
	if _, err := NewConsoleAssertionKeyring([]ConsoleAssertionTrustedKey{wrongPurpose}); err == nil {
		t.Fatal("expected foreign-domain purpose to be rejected")
	}

	badFingerprint := validKey
	badFingerprint.Fingerprint = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := NewConsoleAssertionKeyring([]ConsoleAssertionTrustedKey{badFingerprint}); err == nil {
		t.Fatal("expected fingerprint mismatch to be rejected")
	}

	duplicateID := []ConsoleAssertionTrustedKey{validKey, validKey}
	if _, err := NewConsoleAssertionKeyring(duplicateID); err == nil {
		t.Fatal("expected duplicate key id to be rejected")
	}

	public2, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	reusedMaterial := validKey
	reusedMaterial.PublicKey = base64.StdEncoding.EncodeToString(public)
	other := validKey
	other.KeyID = "2026-10"
	other.PublicKey = base64.StdEncoding.EncodeToString(public)
	_ = public2
	if _, err := NewConsoleAssertionKeyring([]ConsoleAssertionTrustedKey{validKey, other}); err == nil {
		t.Fatal("expected reused public-key material across two key ids to be rejected")
	}

	invalidWindow := validKey
	invalidWindow.ValidFrom = now.Add(time.Hour)
	invalidWindow.ValidUntil = now.Add(-time.Hour)
	if _, err := NewConsoleAssertionKeyring([]ConsoleAssertionTrustedKey{invalidWindow}); err == nil {
		t.Fatal("expected valid_from >= valid_until to be rejected")
	}

	revokedAfterExpiry := now.Add(time.Hour)
	badRevocation := validKey
	badRevocation.RevokedAt = &revokedAfterExpiry
	badRevocation.RevokeReason = "test"
	if _, err := NewConsoleAssertionKeyring([]ConsoleAssertionTrustedKey{badRevocation}); err == nil {
		t.Fatal("expected revocation at/after expiry to be rejected")
	}

	revokedNoReason := validKey
	revokedTime := now
	revokedNoReason.RevokedAt = &revokedTime
	if _, err := NewConsoleAssertionKeyring([]ConsoleAssertionTrustedKey{revokedNoReason}); err == nil {
		t.Fatal("expected revocation without a reason to be rejected")
	}
}

func TestLoadConsoleAssertionKeyringJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	public, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest := `[{"key_id":"2026-09","algorithm":"Ed25519","public_key":"` + base64.StdEncoding.EncodeToString(public) + `","fingerprint":"` + sha256Hex(string(public)) + `","purpose":"console_admin_assertion_signing","protocol":"xm-console-assertion-v1","valid_from":"` + now.Add(-time.Hour).Format(rfc3339NanoZ) + `","valid_until":"` + now.Add(time.Hour).Format(rfc3339NanoZ) + `","revoked_at":null,"revoke_reason":""}]`

	keyring, err := LoadConsoleAssertionKeyringJSON([]byte(manifest))
	if err != nil {
		t.Fatalf("expected manifest to load, got %v", err)
	}
	if keyring.Len() != 1 {
		t.Fatalf("expected one key, got %d", keyring.Len())
	}

	if _, err := LoadConsoleAssertionKeyringJSON([]byte(`[{"key_id":"a","key_id":"a"}]`)); err == nil {
		t.Fatal("expected duplicate top-level keys to be rejected")
	}
	if _, err := LoadConsoleAssertionKeyringJSON([]byte(`[]`)); err == nil {
		t.Fatal("expected an empty manifest to be rejected (no trusted keys)")
	}
	if _, err := LoadConsoleAssertionKeyringJSON([]byte(`[{"key_id":"2026-09","unknown_field":true}]`)); err == nil {
		t.Fatal("expected an unknown field to be rejected")
	}
	if _, err := LoadConsoleAssertionKeyringJSON([]byte(manifest + `{"trailing":true}`)); err == nil {
		t.Fatal("expected trailing JSON to be rejected")
	}
}

const rfc3339NanoZ = "2006-01-02T15:04:05Z"

func splitCompact(t *testing.T, token string) [3]string {
	t.Helper()
	var out [3]string
	n := 0
	start := 0
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			if n >= 2 {
				t.Fatalf("token has more than three segments: %q", token)
			}
			out[n] = token[start:i]
			n++
			start = i + 1
		}
	}
	out[2] = token[start:]
	return out
}
