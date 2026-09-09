package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"

	"invoice-system/backend/internal/securefields"
)

type oidcFixture struct {
	t                 *testing.T
	server            *httptest.Server
	key               *rsa.PrivateKey
	wrongKey          *rsa.PrivateKey
	now               time.Time
	mu                sync.Mutex
	nonce             string
	challenge         string
	claims            map[string]any
	discoveryEdit     func(map[string]any)
	useWrongKey       bool
	includePrivateJWK bool
}

func newOIDCFixture(t *testing.T) *oidcFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &oidcFixture{t: t, key: key, wrongKey: wrong, now: time.Now().UTC().Truncate(time.Second)}
	fixture.server = httptest.NewTLSServer(http.HandlerFunc(fixture.handle))
	t.Cleanup(fixture.server.Close)
	fixture.claims = fixture.defaultClaims()
	return fixture
}

func (f *oidcFixture) defaultClaims() map[string]any {
	return map[string]any{
		"iss": f.server.URL, "sub": "user-123", "aud": "invoice-web",
		"exp": f.now.Add(5 * time.Minute).Unix(), "iat": f.now.Unix(),
		"email": "user@EXAMPLE.COM", "email_verified": true,
		"roles": []string{"invoice-user", "invoice-admin"},
		"acr":   "urn:invoice:mfa", "amr": []string{"pwd", "otp"},
		"auth_time": f.now.Unix(), "sid": "provider-session-1",
	}
}

func (f *oidcFixture) handle(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/.well-known/openid-configuration":
		document := map[string]any{
			"issuer":                                f.server.URL,
			"authorization_endpoint":                f.server.URL + "/authorize",
			"token_endpoint":                        f.server.URL + "/token",
			"userinfo_endpoint":                     f.server.URL + "/userinfo",
			"jwks_uri":                              f.server.URL + "/jwks",
			"response_types_supported":              []string{"code"},
			"scopes_supported":                      []string{"openid", "email", "profile"},
			"code_challenge_methods_supported":      []string{"S256"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic"},
			"end_session_endpoint":                  f.server.URL + "/logout",
			"backchannel_logout_supported":          true,
			"backchannel_logout_session_supported":  true,
		}
		if f.discoveryEdit != nil {
			f.discoveryEdit(document)
		}
		writeFixtureJSON(writer, document)
	case "/jwks":
		jwk := rsaPublicJWK(&f.key.PublicKey)
		if f.includePrivateJWK {
			jwk["d"] = base64.RawURLEncoding.EncodeToString(f.key.D.Bytes())
		}
		writeFixtureJSON(writer, map[string]any{"keys": []any{jwk}})
	case "/token":
		clientID, secret, ok := request.BasicAuth()
		if !ok || clientID != "invoice-web" || secret != "test-secret" {
			http.Error(writer, "bad client", http.StatusUnauthorized)
			return
		}
		if err := request.ParseForm(); err != nil || request.Form.Get("grant_type") != "authorization_code" || request.Form.Get("code") != "good-code" {
			http.Error(writer, "bad exchange", http.StatusBadRequest)
			return
		}
		verifier := request.Form.Get("code_verifier")
		challenge := sha256.Sum256([]byte(verifier))
		f.mu.Lock()
		expected := f.challenge
		nonce := f.nonce
		claims := cloneClaims(f.claims)
		wrongKey := f.useWrongKey
		f.mu.Unlock()
		if verifier == "" || base64.RawURLEncoding.EncodeToString(challenge[:]) != expected {
			http.Error(writer, "bad PKCE", http.StatusBadRequest)
			return
		}
		if _, exists := claims["nonce"]; !exists {
			claims["nonce"] = nonce
		}
		key := f.key
		if wrongKey {
			key = f.wrongKey
		}
		raw, err := signFixtureJWT(key, claims)
		if err != nil {
			f.t.Errorf("sign token: %v", err)
			http.Error(writer, "sign", http.StatusInternalServerError)
			return
		}
		writeFixtureJSON(writer, map[string]any{"access_token": "fixture-access-token", "token_type": "Bearer", "expires_in": 300, "id_token": raw})
	default:
		http.NotFound(writer, request)
	}
}

func (f *oidcFixture) client(t *testing.T) *OIDCClient {
	t.Helper()
	host := strings.TrimPrefix(f.server.URL, "https://")
	config := OIDCConfig{
		IssuerURL: f.server.URL, ClientID: "invoice-web", RedirectURL: f.server.URL + "/callback", PostLogoutRedirectURL: f.server.URL + "/",
		AdminRole: "invoice-admin", RequiredAdminACR: "urn:invoice:mfa", RequiredAdminAMR: []string{"pwd", "otp"},
		AllowedSigningAlgs: []string{"RS256"}, AllowedEndpointHosts: []string{host},
		RequireBackchannelLogout: true,
	}
	keyring := securefields.Keyring{CurrentKeyID: "k1", EncryptionKeys: map[string][]byte{"k1": []byte("0123456789abcdef0123456789abcdef")}, IndexKey: []byte("abcdef0123456789abcdef0123456789")}
	client, err := newOIDCClient(context.Background(), config, "test-secret", f.server.Client(), NewMemoryFlowStore(), SecureFieldsFlowProtector{Keyring: keyring})
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return f.now }
	return client
}

func (f *oidcFixture) begin(t *testing.T, client *OIDCClient, input BeginAuthorizationInput) AuthorizationRequest {
	t.Helper()
	start, err := client.Begin(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(start.URL)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.nonce = parsed.Query().Get("nonce")
	f.challenge = parsed.Query().Get("code_challenge")
	f.mu.Unlock()
	if parsed.Query().Get("code_challenge_method") != "S256" || f.nonce == "" || f.challenge == "" {
		t.Fatalf("authorization URL missing nonce/PKCE: %s", start.URL)
	}
	return start
}

func TestOIDCAuthorizationCodePKCEStateNonceAndReplay(t *testing.T) {
	fixture := newOIDCFixture(t)
	client := fixture.client(t)
	start := fixture.begin(t, client, BeginAuthorizationInput{Purpose: FlowLogin, ReturnPath: "/invoices"})
	wrongBinding, _ := randomURLToken(32)
	if _, err := client.Callback(context.Background(), CallbackInput{Code: "good-code", State: start.State, BrowserBinding: wrongBinding}); !errors.Is(err, ErrInvalidFlow) {
		t.Fatalf("wrong browser binding error=%v", err)
	}
	result, err := client.Callback(context.Background(), CallbackInput{Code: "good-code", State: start.State, BrowserBinding: start.BrowserBinding})
	if err != nil {
		t.Fatal(err)
	}
	if result.Principal.Issuer != fixture.server.URL || result.Principal.Subject != "user-123" || result.Principal.Email != "user@example.com" || !result.Principal.EmailVerified || result.ReturnPath != "/invoices" {
		t.Fatalf("unexpected verified result: %+v", result)
	}
	if _, err = client.Callback(context.Background(), CallbackInput{Code: "good-code", State: start.State, BrowserBinding: start.BrowserBinding}); !errors.Is(err, ErrInvalidFlow) {
		t.Fatalf("replayed callback error=%v", err)
	}
}

func TestOIDCRejectsCryptographicAndClaimFailures(t *testing.T) {
	fixture := newOIDCFixture(t)
	tests := []struct {
		name   string
		mutate func(*oidcFixture)
	}{
		{"wrong_nonce", func(f *oidcFixture) { f.claims["nonce"] = "attacker-nonce" }},
		{"wrong_audience", func(f *oidcFixture) { f.claims["aud"] = "other-client" }},
		{"expired", func(f *oidcFixture) { f.claims["exp"] = f.now.Add(-time.Minute).Unix() }},
		{"wrong_signature", func(f *oidcFixture) { f.useWrongKey = true }},
		{"multi_audience_without_azp", func(f *oidcFixture) { f.claims["aud"] = []string{"invoice-web", "other"} }},
		{"email_verified_wrong_type", func(f *oidcFixture) { f.claims["email_verified"] = "true" }},
		{"role_with_surrounding_whitespace", func(f *oidcFixture) { f.claims["roles"] = []string{" invoice-admin"} }},
		{"future_issued_at", func(f *oidcFixture) { f.claims["iat"] = f.now.Add(5 * time.Minute).Unix() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture.mu.Lock()
			fixture.claims = fixture.defaultClaims()
			fixture.useWrongKey = false
			fixture.mu.Unlock()
			test.mutate(fixture)
			client := fixture.client(t)
			start := fixture.begin(t, client, BeginAuthorizationInput{Purpose: FlowLogin})
			if _, err := client.Callback(context.Background(), CallbackInput{Code: "good-code", State: start.State, BrowserBinding: start.BrowserBinding}); !errors.Is(err, ErrInvalidIDToken) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestOIDCAdminStepUpRequiresSameIdentityRoleACRAMRAndFreshAuth(t *testing.T) {
	fixture := newOIDCFixture(t)
	tests := []struct {
		name   string
		mutate func(*oidcFixture)
		want   error
	}{
		{"same_identity_fresh_mfa", func(*oidcFixture) {}, nil},
		{"identity_swap", func(f *oidcFixture) { f.claims["sub"] = "other-user" }, ErrIdentityMismatch},
		{"missing_role", func(f *oidcFixture) { f.claims["roles"] = []string{"invoice-user"} }, ErrAdminRoleRequired},
		{"wrong_acr", func(f *oidcFixture) { f.claims["acr"] = "urn:password" }, ErrMFAStepUpRequired},
		{"missing_amr_factor", func(f *oidcFixture) { f.claims["amr"] = []string{"pwd"} }, ErrMFAStepUpRequired},
		{"stale_auth_time", func(f *oidcFixture) { f.claims["auth_time"] = f.now.Add(-time.Hour).Unix() }, ErrMFAStepUpRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture.mu.Lock()
			fixture.claims = fixture.defaultClaims()
			fixture.useWrongKey = false
			fixture.mu.Unlock()
			test.mutate(fixture)
			client := fixture.client(t)
			existing := &Principal{Issuer: fixture.server.URL, Subject: "user-123"}
			start := fixture.begin(t, client, BeginAuthorizationInput{Purpose: FlowAdminStepUp, ExpectedIdentity: existing, ExistingSessionID: "20000000-0000-4000-8000-000000000001"})
			parsed, _ := url.Parse(start.URL)
			if parsed.Query().Get("prompt") != "login" || parsed.Query().Get("max_age") != "0" || parsed.Query().Get("acr_values") == "" {
				t.Fatal("step-up authorization URL does not force fresh authentication")
			}
			_, err := client.Callback(context.Background(), CallbackInput{Code: "good-code", State: start.State, BrowserBinding: start.BrowserBinding})
			if test.want == nil && err != nil {
				t.Fatal(err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
}

func TestOIDCStrictDiscoveryAndJWKS(t *testing.T) {
	fixture := newOIDCFixture(t)
	tests := []struct {
		name   string
		mutate func(*oidcFixture)
	}{
		{"issuer_mismatch", func(f *oidcFixture) {
			f.discoveryEdit = func(d map[string]any) { d["issuer"] = "https://attacker.invalid" }
		}},
		{"insecure_token_endpoint", func(f *oidcFixture) {
			f.discoveryEdit = func(d map[string]any) { d["token_endpoint"] = "http://example.invalid/token" }
		}},
		{"pkce_s256_not_advertised", func(f *oidcFixture) {
			f.discoveryEdit = func(d map[string]any) { d["code_challenge_methods_supported"] = []string{"plain"} }
		}},
		{"end_session_missing", func(f *oidcFixture) {
			f.discoveryEdit = func(d map[string]any) { d["end_session_endpoint"] = "" }
		}},
		{"backchannel_not_supported", func(f *oidcFixture) {
			f.discoveryEdit = func(d map[string]any) { d["backchannel_logout_supported"] = false }
		}},
		{"backchannel_session_not_supported", func(f *oidcFixture) {
			f.discoveryEdit = func(d map[string]any) { d["backchannel_logout_session_supported"] = false }
		}},
		{"userinfo_missing", func(f *oidcFixture) {
			f.discoveryEdit = func(d map[string]any) { d["userinfo_endpoint"] = "" }
		}},
		{"private_jwk", func(f *oidcFixture) { f.includePrivateJWK = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture.discoveryEdit = nil
			fixture.includePrivateJWK = false
			test.mutate(fixture)
			host := strings.TrimPrefix(fixture.server.URL, "https://")
			config := OIDCConfig{IssuerURL: fixture.server.URL, ClientID: "invoice-web", RedirectURL: fixture.server.URL + "/callback", PostLogoutRedirectURL: fixture.server.URL + "/", AdminRole: "invoice-admin", AllowedEndpointHosts: []string{host}, RequireBackchannelLogout: true}
			keyring := securefields.Keyring{CurrentKeyID: "k1", EncryptionKeys: map[string][]byte{"k1": []byte("0123456789abcdef0123456789abcdef")}, IndexKey: []byte("abcdef0123456789abcdef0123456789")}
			if _, err := newOIDCClient(context.Background(), config, "secret", fixture.server.Client(), NewMemoryFlowStore(), SecureFieldsFlowProtector{Keyring: keyring}); err == nil {
				t.Fatal("unsafe discovery/JWKS accepted")
			}
		})
	}
}

func TestDesktopBearerVerifierAndHeaderOnlyExtraction(t *testing.T) {
	fixture := newOIDCFixture(t)
	client := fixture.client(t)
	verifier, err := client.NewBearerVerifier(BearerConfig{Audiences: []string{"invoice-api"}, AuthorizedParties: []string{"invoice-desktop"}})
	if err != nil {
		t.Fatal(err)
	}
	claims := fixture.defaultClaims()
	claims["aud"] = []string{"invoice-api"}
	claims["azp"] = "invoice-desktop"
	delete(claims, "nonce")
	raw, err := signFixtureJWT(fixture.key, claims)
	if err != nil {
		t.Fatal(err)
	}
	if principal, err := verifier.Verify(context.Background(), raw); err != nil || principal.Subject != "user-123" {
		t.Fatalf("desktop bearer verify: principal=%+v err=%v", principal, err)
	}
	claims["azp"] = "attacker-client"
	wrong, _ := signFixtureJWT(fixture.key, claims)
	if _, err = verifier.Verify(context.Background(), wrong); !errors.Is(err, ErrInvalidBearer) {
		t.Fatalf("wrong azp error=%v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://invoice.example/api", nil)
	request.Header.Set("Authorization", "Bearer "+raw)
	if extracted, err := ExtractDesktopBearer(request); err != nil || extracted != raw {
		t.Fatalf("extract=%q err=%v", extracted, err)
	}
	queryRequest := httptest.NewRequest(http.MethodGet, "https://invoice.example/api?access_token="+url.QueryEscape(raw), nil)
	queryRequest.Header.Set("Authorization", "Bearer "+raw)
	if _, err = ExtractDesktopBearer(queryRequest); !errors.Is(err, ErrInvalidBearer) {
		t.Fatal("query bearer was accepted")
	}
}

func TestRejectsDuplicateSecurityJSONKeys(t *testing.T) {
	if err := rejectDuplicateJSONKeys([]byte(`{"issuer":"good","issuer":"evil"}`)); err == nil {
		t.Fatal("duplicate discovery key accepted")
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","alg":"none"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user"}`))
	if err := validateJWTJSONSegments(header + "." + payload + ".signature"); err == nil {
		t.Fatal("duplicate JWT header key accepted")
	}
}

func TestOIDCClientAndTransportErrorsDoNotExposeIssuerOrSecret(t *testing.T) {
	fixture := newOIDCFixture(t)
	goodClient := fixture.client(t)
	if strings.Contains(fmt.Sprintf("%#v", goodClient), "test-secret") || strings.Contains(fmt.Sprintf("%+v", goodClient), fixture.server.URL) {
		t.Fatal("OIDC client formatter exposed provider configuration or client secret")
	}
	host := strings.TrimPrefix(fixture.server.URL, "https://")
	config := OIDCConfig{IssuerURL: fixture.server.URL, ClientID: "invoice-web", RedirectURL: fixture.server.URL + "/callback", PostLogoutRedirectURL: fixture.server.URL + "/", AdminRole: "invoice-admin", AllowedEndpointHosts: []string{host}, RequireBackchannelLogout: true}
	keyring := securefields.Keyring{CurrentKeyID: "k1", EncryptionKeys: map[string][]byte{"k1": []byte("0123456789abcdef0123456789abcdef")}, IndexKey: []byte("abcdef0123456789abcdef0123456789")}
	fixture.server.Close()
	client, err := newOIDCClient(context.Background(), config, "super-secret-value", fixture.server.Client(), NewMemoryFlowStore(), SecureFieldsFlowProtector{Keyring: keyring})
	if err == nil || strings.Contains(err.Error(), fixture.server.URL) || strings.Contains(err.Error(), "super-secret-value") {
		t.Fatalf("unredacted construction error=%v", err)
	}
	if client != nil && strings.Contains(fmt.Sprintf("%#v", client), "super-secret-value") {
		t.Fatal("OIDC client formatter exposed client secret")
	}
}

func rsaPublicJWK(key *rsa.PublicKey) map[string]any {
	return map[string]any{
		"kty": "RSA", "kid": "fixture-key", "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}
}

func signFixtureJWT(key *rsa.PrivateKey, claims map[string]any) (string, error) {
	options := (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "fixture-key")
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, options)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signed, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}
	return signed.CompactSerialize()
}

func writeFixtureJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

func cloneClaims(source map[string]any) map[string]any {
	copy := make(map[string]any, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}
