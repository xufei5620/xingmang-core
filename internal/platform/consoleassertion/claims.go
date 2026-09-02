package consoleassertion

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// jwsHeader is the JWS protected header (spec §3.1). No jku/jwk/x5c fields:
// this domain distributes trust out-of-band (the reviewed keyring manifest),
// never via a header-carried key.
type jwsHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

// wireClaims is the assertion payload, field-for-field the table in spec
// §3.2. Deliberately a concrete struct, not map[string]any: the set of
// claims this package will ever emit is a closed, reviewed list, and a typo
// in a field name should be a compile error, not a silently-missing claim.
type wireClaims struct {
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

// randomNonce returns a fresh 32-byte random value, base64url-encoded (43
// characters, no padding) -- spec §3.2's nonce, doubling as the JWT jti.
func randomNonce() (string, error) {
	buf := make([]byte, nonceBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("consoleassertion: 生成 nonce 失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// signCompact builds and signs the JWS compact serialization
// ({header}.{payload}.{signature}, all base64url-nopad -- spec §3.1). No
// external JOSE dependency: this mirrors how the invoice side's own verifier
// was built by hand against the same frozen spec (its handoff document notes
// it did the same, citing the same "no shared JOSE library across these two
// systems" reasoning), and how internal/platform/oidcauth already parses
// OIDC tokens in this repository without one.
func signCompact(priv ed25519.PrivateKey, kid string, claims wireClaims) (string, error) {
	headerJSON, err := json.Marshal(jwsHeader{Alg: Algorithm, Kid: kid, Typ: TokenType})
	if err != nil {
		return "", fmt.Errorf("consoleassertion: 编码 JWS header 失败: %w", err)
	}
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("consoleassertion: 编码断言 claims 失败: %w", err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(payloadJSON)
	signature := ed25519.Sign(priv, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
