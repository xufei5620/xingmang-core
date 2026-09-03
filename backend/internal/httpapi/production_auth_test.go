package httpapi

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"invoice-system/backend/internal/auth"
	"invoice-system/backend/internal/ledger"
)

type fakeOIDCFlowClient struct {
	principal auth.Principal
	lastBegin auth.BeginAuthorizationInput
}

func (f *fakeOIDCFlowClient) RPInitiatedLogoutURL() (string, error) {
	return "https://identity.example/logout?client_id=invoice-web&post_logout_redirect_uri=https%3A%2F%2Finvoice.example%2F", nil
}

func (f *fakeOIDCFlowClient) VerifyBackchannelLogout(context.Context, string) (auth.VerifiedBackchannelLogout, error) {
	now := time.Now().UTC()
	return auth.VerifiedBackchannelLogout{Issuer: "https://identity.example", Subject: "subject-1", TokenID: "logout-jti", IssuedAt: now, ExpiresAt: now.Add(time.Minute)}, nil
}

type fakeBackchannelLogoutProcessor struct{ calls int }

func (f *fakeBackchannelLogoutProcessor) Process(_ context.Context, _ auth.VerifiedBackchannelLogout, _ auth.BackchannelLogoutActor) (auth.BackchannelLogoutResult, error) {
	f.calls++
	return auth.BackchannelLogoutResult{EventID: "event-1", RevokedSessions: 1}, nil
}

func (f *fakeOIDCFlowClient) Begin(_ context.Context, input auth.BeginAuthorizationInput) (auth.AuthorizationRequest, error) {
	f.lastBegin = input
	return auth.AuthorizationRequest{
		URL: "https://identity.example/authorize", BrowserBinding: input.BrowserBinding,
		ExpiresAt: time.Now().Add(10 * time.Minute), ProviderLabel: "Test Identity",
	}, nil
}

func (f *fakeOIDCFlowClient) Callback(context.Context, auth.CallbackInput) (auth.CallbackResult, error) {
	return auth.CallbackResult{Principal: f.principal, Purpose: auth.FlowLogin, ReturnPath: "/orders"}, nil
}

func productionAuthServer(t *testing.T) (*Server, *fakeOIDCFlowClient) {
	t.Helper()
	keyFile := filepath.Join(t.TempDir(), "binding.key")
	if err := os.WriteFile(keyFile, []byte(strings.Repeat("b", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	hasher, err := auth.NewClientBindingHasherFromFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := auth.NewSessionManager(auth.NewMemorySessionStore(), auth.SessionConfig{}, &auth.MemorySecurityAuditSink{})
	if err != nil {
		t.Fatal(err)
	}
	csrf, err := auth.NewCSRFPolicy([]string{"https://invoice.example"})
	if err != nil {
		t.Fatal(err)
	}
	oidc := &fakeOIDCFlowClient{principal: auth.Principal{
		Issuer: "https://identity.example", Subject: "subject-1",
		Email: "user@example.com", EmailVerified: true,
		Roles: []string{"invoice-user"}, AuthTime: time.Now().UTC(),
	}}
	runtime := &ProductionAuth{
		OIDC: oidc, Logout: oidc, BackchannelLogout: &fakeBackchannelLogoutProcessor{}, Sessions: sessions, BindingHasher: hasher, CSRF: csrf,
		Admin: auth.AdminPolicy{Role: "invoice-admin", RequiredACR: "urn:test:mfa", RequiredAMR: []string{"otp"}, StepUpMaxAge: 10 * time.Minute},
		ProvisionUser: func(context.Context, auth.Principal, string) (SessionUser, error) {
			return SessionUser{ID: "10000000-0000-4000-8000-000000000001", Email: "user@example.com", EmailVerified: true}, nil
		},
		LoadUser: func(context.Context, string) (SessionUser, error) {
			return SessionUser{ID: "10000000-0000-4000-8000-000000000001", Email: "user@example.com", EmailVerified: true}, nil
		},
	}
	server, err := NewWithConfig(ledger.NewService(), Config{
		AuthMode: "oidc", AdminIPAllowlist: []string{"127.0.0.1/32"}, ProductionAuth: runtime,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return server, oidc
}

func TestProductionOIDCLoginSessionAndCSRF(t *testing.T) {
	server, oidc := productionAuthServer(t)

	status := httptest.NewRequest(http.MethodGet, "https://invoice.example/api/v1/auth/session", nil)
	status.RemoteAddr = "127.0.0.1:443"
	status.Header.Set("User-Agent", "test-browser")
	statusRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(statusRecorder, status)
	if statusRecorder.Code != http.StatusOK || !strings.Contains(statusRecorder.Body.String(), `"authenticated":false`) {
		t.Fatalf("anonymous session response=%d %s", statusRecorder.Code, statusRecorder.Body.String())
	}

	login := httptest.NewRequest(http.MethodGet, "https://invoice.example/api/v1/auth/login?return_to=/orders", nil)
	login.RemoteAddr = "127.0.0.1:443"
	login.Header.Set("User-Agent", "test-browser")
	loginRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(loginRecorder, login)
	if loginRecorder.Code != http.StatusSeeOther || loginRecorder.Header().Get("Location") != "https://identity.example/authorize" || oidc.lastBegin.ReturnPath != "/orders" {
		t.Fatalf("login response=%d location=%q begin=%+v", loginRecorder.Code, loginRecorder.Header().Get("Location"), oidc.lastBegin)
	}
	var flowCookie *http.Cookie
	for _, cookie := range loginRecorder.Result().Cookies() {
		if cookie.Name == oidcFlowCookieName {
			flowCookie = cookie
		}
	}
	if flowCookie == nil || !flowCookie.Secure || !flowCookie.HttpOnly {
		t.Fatalf("missing secure flow cookie: %+v", flowCookie)
	}

	callback := httptest.NewRequest(http.MethodGet, "https://invoice.example/api/v1/auth/callback?code=code&state=state", nil)
	callback.RemoteAddr = "127.0.0.1:443"
	callback.Header.Set("User-Agent", "test-browser")
	callback.AddCookie(flowCookie)
	callbackRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(callbackRecorder, callback)
	if callbackRecorder.Code != http.StatusSeeOther || callbackRecorder.Header().Get("Location") != "/orders" {
		t.Fatalf("callback response=%d body=%s", callbackRecorder.Code, callbackRecorder.Body.String())
	}
	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range callbackRecorder.Result().Cookies() {
		switch cookie.Name {
		case defaultSessionCookieName:
			sessionCookie = cookie
		case csrfCookieName:
			csrfCookie = cookie
		}
	}
	if sessionCookie == nil || csrfCookie == nil || !sessionCookie.HttpOnly || csrfCookie.HttpOnly {
		t.Fatalf("invalid auth cookies session=%+v csrf=%+v", sessionCookie, csrfCookie)
	}

	status = httptest.NewRequest(http.MethodGet, "https://invoice.example/api/v1/auth/session", nil)
	status.RemoteAddr = "127.0.0.1:443"
	status.Header.Set("User-Agent", "test-browser")
	status.AddCookie(sessionCookie)
	status.AddCookie(csrfCookie)
	statusRecorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(statusRecorder, status)
	var response struct {
		Authenticated bool   `json:"authenticated"`
		CSRFToken     string `json:"csrf_token"`
		User          struct {
			ID             string `json:"id"`
			Role           string `json:"role"`
			Platform       string `json:"platform"`
			PlatformUserID string `json:"platform_user_id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(statusRecorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Authenticated || response.CSRFToken != csrfCookie.Value || response.User.Role != "user" {
		t.Fatalf("unexpected session bootstrap: %+v", response)
	}
	if response.User.Platform != "" || response.User.PlatformUserID != "" {
		t.Fatalf("an OIDC session must never carry a platform-login identity: %+v", response.User)
	}

	mutation := httptest.NewRequest(http.MethodPost, "https://invoice.example/api/v1/user/profiles", strings.NewReader(`{}`))
	mutation.RemoteAddr = "127.0.0.1:443"
	mutation.Header.Set("User-Agent", "test-browser")
	mutation.Header.Set("Content-Type", "application/json")
	mutation.AddCookie(sessionCookie)
	mutation.AddCookie(csrfCookie)
	mutationRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(mutationRecorder, mutation)
	if mutationRecorder.Code != http.StatusForbidden || !strings.Contains(mutationRecorder.Body.String(), "CSRF_REJECTED") {
		t.Fatalf("mutation without CSRF status=%d body=%s", mutationRecorder.Code, mutationRecorder.Body.String())
	}

	mutation = httptest.NewRequest(http.MethodPost, "https://invoice.example/api/v1/user/profiles", strings.NewReader(`{}`))
	mutation.RemoteAddr = "127.0.0.1:443"
	mutation.Header.Set("User-Agent", "test-browser")
	mutation.Header.Set("Content-Type", "application/json")
	mutation.Header.Set("Origin", "https://invoice.example")
	mutation.Header.Set("Sec-Fetch-Site", "same-origin")
	mutation.Header.Set("X-CSRF-Token", csrfCookie.Value)
	mutation.AddCookie(sessionCookie)
	mutation.AddCookie(csrfCookie)
	mutationRecorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(mutationRecorder, mutation)
	if mutationRecorder.Code == http.StatusForbidden {
		t.Fatalf("valid CSRF was rejected: %s", mutationRecorder.Body.String())
	}

	logout := httptest.NewRequest(http.MethodPost, "https://invoice.example/api/v1/auth/logout", nil)
	logout.RemoteAddr = "127.0.0.1:443"
	logout.Header.Set("User-Agent", "test-browser")
	logout.Header.Set("Origin", "https://invoice.example")
	logout.Header.Set("Sec-Fetch-Site", "same-origin")
	logout.Header.Set("X-CSRF-Token", csrfCookie.Value)
	logout.AddCookie(sessionCookie)
	logout.AddCookie(csrfCookie)
	logoutRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(logoutRecorder, logout)
	if logoutRecorder.Code != http.StatusOK || !strings.Contains(logoutRecorder.Body.String(), `"logout_url":"https://identity.example/logout?`) {
		t.Fatalf("logout response=%d %s", logoutRecorder.Code, logoutRecorder.Body.String())
	}
	reused := httptest.NewRequest(http.MethodGet, "https://invoice.example/api/v1/user/funding-lots", nil)
	reused.RemoteAddr = "127.0.0.1:443"
	reused.Header.Set("User-Agent", "test-browser")
	reused.AddCookie(sessionCookie)
	reusedRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(reusedRecorder, reused)
	if reusedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status=%d", reusedRecorder.Code)
	}
}

func TestProductionAuthRejectsBearerOnBrowserRoute(t *testing.T) {
	server, _ := productionAuthServer(t)
	request := httptest.NewRequest(http.MethodGet, "https://invoice.example/api/v1/user/funding-lots", nil)
	request.RemoteAddr = "127.0.0.1:443"
	request.Header.Set("Authorization", "Bearer token")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("browser bearer status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestBackchannelLogoutRouteRequiresStrictFormAndNoUserSession(t *testing.T) {
	server, _ := productionAuthServer(t)
	form := url.Values{"logout_token": {"signed.logout.token"}}
	request := httptest.NewRequest(http.MethodPost, "https://invoice.example/api/v1/auth/backchannel-logout", strings.NewReader(form.Encode()))
	request.RemoteAddr = "127.0.0.1:443"
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("backchannel status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	for name, mutate := range map[string]func(*http.Request){
		"wrong_content_type": func(r *http.Request) { r.Header.Set("Content-Type", "application/json") },
		"query_parameter":    func(r *http.Request) { r.URL.RawQuery = "logout_token=query" },
		"cookie":             func(r *http.Request) { r.Header.Set("Cookie", "session=forbidden") },
		"extra_field":        func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(form.Encode() + "&extra=x")) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := httptest.NewRequest(http.MethodPost, "https://invoice.example/api/v1/auth/backchannel-logout", strings.NewReader(form.Encode()))
			candidate.RemoteAddr = "127.0.0.1:443"
			candidate.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			mutate(candidate)
			result := httptest.NewRecorder()
			server.Handler().ServeHTTP(result, candidate)
			if result.Code < 400 {
				t.Fatalf("unsafe backchannel request accepted: %d", result.Code)
			}
		})
	}
}

type failingRevokeStore struct{ *auth.MemorySessionStore }

func (f failingRevokeStore) RevokeToken(context.Context, string, time.Time, string) (auth.Session, error) {
	return auth.Session{}, errors.New("database unavailable")
}

func TestLogoutDoesNotClaimSuccessWhenLocalRevocationFails(t *testing.T) {
	store := failingRevokeStore{MemorySessionStore: auth.NewMemorySessionStore()}
	sessions, err := auth.NewSessionManager(store, auth.SessionConfig{}, &auth.MemorySecurityAuditSink{})
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{Issuer: "https://identity.example", Subject: "subject-1"}
	credentials, err := sessions.Issue(context.Background(), auth.IssueSessionInput{UserID: "10000000-0000-4000-8000-000000000001", Principal: principal, RequestID: "issue"})
	if err != nil {
		t.Fatal(err)
	}
	oidc := &fakeOIDCFlowClient{}
	runtime := &ProductionAuth{Logout: oidc, Sessions: sessions}
	request := httptest.NewRequest(http.MethodPost, "https://invoice.example/api/v1/auth/logout", nil)
	request = request.WithContext(context.WithValue(request.Context(), identityKey, identity{UserID: credentials.Session.UserID, Session: &credentials.Session, SessionToken: credentials.Token}))
	recorder := httptest.NewRecorder()
	runtime.logout(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "SESSION_REVOKE_FAILED") || len(recorder.Result().Cookies()) != 0 {
		t.Fatalf("logout failure response=%d body=%s cookies=%v", recorder.Code, recorder.Body.String(), recorder.Result().Cookies())
	}
}

// --- CR-0006 (XM-INV-CONSOLE-ASSERT): console-assertion exchange endpoint ---

const (
	testConsoleAssertionIssuer   = "https://console.example"
	testConsoleAssertionAudience = "xingmang-console-assertion-v1"
	// testConsoleAssertionACR must match console_assertion.go's own
	// unexported consoleAssertionRequiredACR constant -- duplicated here
	// because it is package-private in internal/auth and this is a
	// different package; VerifyConsoleAssertion rejects any other value.
	testConsoleAssertionACR = "xingmang-console-totp-v1"
)

// memoryConsoleAssertionNonceStore is a minimal, in-memory
// auth.ConsoleAssertionNonceStore for unit tests that do not use a real
// PostgreSQL database (see console_assertion_postgres coverage in
// internal/auth's own integration test for the real atomic-INSERT
// contract this fake only approximates with a mutex).
type memoryConsoleAssertionNonceStore struct {
	mu   sync.Mutex
	seen map[string]bool
}

func newMemoryConsoleAssertionNonceStore() *memoryConsoleAssertionNonceStore {
	return &memoryConsoleAssertionNonceStore{seen: make(map[string]bool)}
}

func (m *memoryConsoleAssertionNonceStore) ConsumeNonce(_ context.Context, nonceHash string, _ time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen[nonceHash] {
		return false, nil
	}
	m.seen[nonceHash] = true
	return true, nil
}

type consoleAssertionFixture struct {
	server     *Server
	oidc       *fakeOIDCFlowClient
	privateKey ed25519.PrivateKey
	keyID      string
}

// consoleAssertionServer builds a ProductionAuth with BOTH the OIDC fake
// (productionAuthServer's own fixture shape) and console-assertion wired,
// on one server instance -- directly supporting a coexistence test (design
// spec test matrix #23) rather than only ever exercising the two paths in
// isolation. disableOIDC mirrors DisableOIDCAdminLogin for the phase-2
// (OIDC_ADMIN_LOGIN_ENABLED=false) route-registration tests.
func consoleAssertionServer(t *testing.T, disableOIDC bool) consoleAssertionFixture {
	t.Helper()
	keyFile := filepath.Join(t.TempDir(), "binding.key")
	if err := os.WriteFile(keyFile, []byte(strings.Repeat("b", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	hasher, err := auth.NewClientBindingHasherFromFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := auth.NewSessionManager(auth.NewMemorySessionStore(), auth.SessionConfig{}, &auth.MemorySecurityAuditSink{})
	if err != nil {
		t.Fatal(err)
	}
	csrf, err := auth.NewCSRFPolicy([]string{"https://invoice.example"})
	if err != nil {
		t.Fatal(err)
	}
	oidc := &fakeOIDCFlowClient{principal: auth.Principal{
		Issuer: "https://identity.example", Subject: "subject-1",
		Email: "user@example.com", EmailVerified: true,
		Roles: []string{"invoice-admin"}, AuthTime: time.Now().UTC(),
	}}
	adminPolicy := auth.AdminPolicy{Role: "invoice-admin", RequiredACR: "urn:test:mfa", RequiredAMR: []string{"otp"}, StepUpMaxAge: 10 * time.Minute}

	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	fingerprintSum := sha256.Sum256(public)
	keyring, err := auth.NewConsoleAssertionKeyring([]auth.ConsoleAssertionTrustedKey{{
		KeyID: "test-key", Algorithm: "Ed25519", PublicKey: base64.StdEncoding.EncodeToString(public),
		Fingerprint: hex.EncodeToString(fingerprintSum[:]), Purpose: "console_admin_assertion_signing", Protocol: "xm-console-assertion-v1",
		ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour),
	}})
	if err != nil {
		t.Fatal(err)
	}

	runtime := &ProductionAuth{
		OIDC: oidc, Logout: oidc, BackchannelLogout: &fakeBackchannelLogoutProcessor{}, Sessions: sessions, BindingHasher: hasher, CSRF: csrf,
		Admin:                 adminPolicy,
		DisableOIDCAdminLogin: disableOIDC,
		ProvisionUser: func(context.Context, auth.Principal, string) (SessionUser, error) {
			return SessionUser{ID: "10000000-0000-4000-8000-000000000001", Email: "user@example.com", EmailVerified: true}, nil
		},
		LoadUser: func(context.Context, string) (SessionUser, error) {
			return SessionUser{ID: "10000000-0000-4000-8000-000000000001", Email: "user@example.com", EmailVerified: true}, nil
		},
		ConsoleAssertionEnabled:     true,
		ConsoleAssertionKeyring:     keyring,
		ConsoleAssertionConfig:      auth.ConsoleAssertionConfig{Issuer: testConsoleAssertionIssuer, Audience: testConsoleAssertionAudience},
		ConsoleAssertionNonces:      newMemoryConsoleAssertionNonceStore(),
		ConsoleAssertionRateLimiter: auth.NewLoginRateLimiter(3, time.Minute),
		SecurityAudit:               &auth.MemorySecurityAuditSink{},
	}
	if disableOIDC {
		runtime.OIDC = nil
		runtime.Logout = nil
	}
	// PublicOrigin mirrors runtime.go's publicOrigin (PUBLIC_ORIGIN) wiring --
	// the same value the CSRF policy above uses -- and is what
	// consoleAssertionOriginAllowed checks a redeeming request's Origin
	// header against alongside the console issuer (XM-INV-ASSERT-ORIGIN).
	server, err := NewWithConfig(ledger.NewService(), Config{
		AuthMode: "oidc", AdminIPAllowlist: []string{"127.0.0.1/32"}, ProductionAuth: runtime, PublicOrigin: "https://invoice.example",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return consoleAssertionFixture{server: server, oidc: oidc, privateKey: private, keyID: "test-key"}
}

func signTestConsoleAssertion(t *testing.T, private ed25519.PrivateKey, kid string, mutate func(map[string]any)) string {
	t.Helper()
	now := time.Now().UTC()
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	header := map[string]any{"alg": "EdDSA", "kid": kid, "typ": "xm-console-assertion+jwt"}
	payload := map[string]any{
		"iss": testConsoleAssertionIssuer, "aud": testConsoleAssertionAudience,
		"sub": "22222222-2222-4222-8222-222222222222", "username": "console-operator",
		"roles": []string{"invoice-admin"}, "acr": testConsoleAssertionACR,
		"amr": []string{"pwd", "otp"}, "scope": "sub2api",
		"nonce": base64.RawURLEncoding.EncodeToString(nonce),
		"iat":   now.Unix(), "exp": now.Unix() + 300, "nbf": now.Unix(),
	}
	if mutate != nil {
		mutate(payload)
	}
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
	signature := ed25519.Sign(private, []byte(headerB64+"."+payloadB64))
	return headerB64 + "." + payloadB64 + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func consoleAssertionExchangeRequest(token string) *http.Request {
	body := `{"assertion":"` + token + `"}`
	request := httptest.NewRequest(http.MethodPost, "https://invoice.example/api/v1/auth/console-assertion", strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:443"
	request.Header.Set("User-Agent", "console-embed")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", testConsoleAssertionIssuer)
	return request
}

func TestConsoleAssertionExchangeIssuesAdminSessionSatisfyingStepUp(t *testing.T) {
	fixture := consoleAssertionServer(t, false)
	token := signTestConsoleAssertion(t, fixture.privateKey, fixture.keyID, nil)

	recorder := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(recorder, consoleAssertionExchangeRequest(token))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"ok":true`) {
		t.Fatalf("exchange response=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range recorder.Result().Cookies() {
		switch cookie.Name {
		case defaultSessionCookieName:
			sessionCookie = cookie
		case csrfCookieName:
			csrfCookie = cookie
		}
	}
	if sessionCookie == nil || csrfCookie == nil {
		t.Fatalf("exchange did not set session cookies: %v", recorder.Result().Cookies())
	}

	// The resulting session must satisfy admin authorization -- including
	// step-up -- immediately, with no separate MFA round trip: an admin-only
	// route must succeed on the very first authenticated request.
	admin := httptest.NewRequest(http.MethodGet, "https://invoice.example/api/v1/admin/source-health", nil)
	admin.RemoteAddr = "127.0.0.1:443"
	admin.Header.Set("User-Agent", "console-embed")
	admin.AddCookie(sessionCookie)
	admin.AddCookie(csrfCookie)
	adminRecorder := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(adminRecorder, admin)
	if adminRecorder.Code == http.StatusForbidden {
		t.Fatalf("admin route rejected a fresh console-assertion session: %d %s", adminRecorder.Code, adminRecorder.Body.String())
	}

	// The session's ACR must be the CONFIGURED OIDC admin ACR ("urn:test:mfa"
	// here), not the assertion's own domain constant -- see
	// ConsolePrincipalFromClaims' doc comment. Verified indirectly: a session
	// GET must report role "admin" (AuthorizeSession's ACR check already
	// passed above), and platform must stay empty (Platform=="" path).
	status := httptest.NewRequest(http.MethodGet, "https://invoice.example/api/v1/auth/session", nil)
	status.RemoteAddr = "127.0.0.1:443"
	status.Header.Set("User-Agent", "console-embed")
	status.AddCookie(sessionCookie)
	status.AddCookie(csrfCookie)
	statusRecorder := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(statusRecorder, status)
	var response struct {
		Authenticated         bool `json:"authenticated"`
		OIDCAdminLoginEnabled bool `json:"oidc_admin_login_enabled"`
		AdminStepUpRequired   bool `json:"admin_step_up_required"`
		User                  struct {
			Role     string `json:"role"`
			Platform string `json:"platform"`
		} `json:"user"`
	}
	if err := json.Unmarshal(statusRecorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Authenticated || response.User.Role != "admin" || response.User.Platform != "" || response.AdminStepUpRequired {
		t.Fatalf("unexpected post-exchange session: %+v", response)
	}
	if !response.OIDCAdminLoginEnabled {
		t.Fatalf("expected oidc_admin_login_enabled=true when OIDC stays on, got %+v", response)
	}
}

func TestConsoleAssertionExchangeRejectsReplay(t *testing.T) {
	fixture := consoleAssertionServer(t, false)
	token := signTestConsoleAssertion(t, fixture.privateKey, fixture.keyID, nil)

	first := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(first, consoleAssertionExchangeRequest(token))
	if first.Code != http.StatusOK {
		t.Fatalf("first exchange response=%d body=%s", first.Code, first.Body.String())
	}
	second := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(second, consoleAssertionExchangeRequest(token))
	if second.Code != http.StatusUnauthorized || !strings.Contains(second.Body.String(), "ASSERTION_INVALID") {
		t.Fatalf("replayed exchange response=%d body=%s", second.Code, second.Body.String())
	}
}

func TestConsoleAssertionExchangeRejectsWrongOrDuplicateOrigin(t *testing.T) {
	for name, mutate := range map[string]func(*http.Request){
		"missing":   func(r *http.Request) { r.Header.Del("Origin") },
		"wrong":     func(r *http.Request) { r.Header.Set("Origin", "https://attacker.example") },
		"duplicate": func(r *http.Request) { r.Header.Add("Origin", "https://other.example") },
		// Even when BOTH headers are individually legitimate (console issuer,
		// plus the invoice app's own origin -- see
		// TestConsoleAssertionExchangeAcceptsInvoiceOriginSameOriginRedeem
		// below), exactly-one is still required: a request carrying two
		// Origin headers is never something a real browser produces, and
		// must still be rejected.
		"duplicate_both_legitimate": func(r *http.Request) { r.Header.Add("Origin", "https://invoice.example") },
		"http_not_https": func(r *http.Request) {
			r.Header.Set("Origin", strings.Replace(testConsoleAssertionIssuer, "https://", "http://", 1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			// A fresh fixture per case: the rate limiter is keyed by client
			// IP and every rejection (including a prior subtest's) counts
			// against it, which would otherwise make a later subtest observe
			// 429 instead of the 403 this test is actually checking for.
			fixture := consoleAssertionServer(t, false)
			token := signTestConsoleAssertion(t, fixture.privateKey, fixture.keyID, nil)
			request := consoleAssertionExchangeRequest(token)
			mutate(request)
			recorder := httptest.NewRecorder()
			fixture.server.Handler().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "ORIGIN_REJECTED") {
				t.Fatalf("response=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

// TestConsoleAssertionExchangeAcceptsInvoiceOriginSameOriginRedeem is the
// XM-INV-ASSERT-ORIGIN regression: the integrated protocol has the invoice
// web app itself -- not the console -- redeem the assertion with a
// same-origin fetch, so the browser sends the invoice app's own public
// origin (server.publicOrigin, wired from PUBLIC_ORIGIN/PublicOrigin, same
// value the CSRF policy above already uses) as Origin. Before this slice,
// only the console issuer was accepted and every real redemption failed
// ORIGIN_REJECTED (2026-09-03 canary finding).
func TestConsoleAssertionExchangeAcceptsInvoiceOriginSameOriginRedeem(t *testing.T) {
	fixture := consoleAssertionServer(t, false)
	token := signTestConsoleAssertion(t, fixture.privateKey, fixture.keyID, nil)
	request := consoleAssertionExchangeRequest(token)
	request.Header.Set("Origin", "https://invoice.example")

	recorder := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"ok":true`) {
		t.Fatalf("same-origin invoice exchange response=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestConsoleAssertionExchangeAcceptsConfiguredConsoleIssuerOrigin makes the
// second accepted origin explicit (consoleAssertionExchangeRequest already
// defaults Origin to the console issuer and every other exchange test above
// relies on that implicitly): a hypothetical direct cross-origin POST from
// the console itself must keep working, not just the invoice app's own
// same-origin redeem this slice adds.
func TestConsoleAssertionExchangeAcceptsConfiguredConsoleIssuerOrigin(t *testing.T) {
	fixture := consoleAssertionServer(t, false)
	token := signTestConsoleAssertion(t, fixture.privateKey, fixture.keyID, nil)
	request := consoleAssertionExchangeRequest(token)
	request.Header.Set("Origin", testConsoleAssertionIssuer)

	recorder := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"ok":true`) {
		t.Fatalf("console-issuer-origin exchange response=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestConsoleAssertionExchangeForeignOriginRejectionCountsAgainstRateLimit
// proves origin_rejected still feeds ConsoleAssertionRateLimiter.RecordFailure
// (team lead's brief: "keep rate-limit failure accounting") -- a genuinely
// foreign origin must still exhaust the limiter exactly like any other
// rejection reason, not get a quieter path now that two origins are legitimate.
func TestConsoleAssertionExchangeForeignOriginRejectionCountsAgainstRateLimit(t *testing.T) {
	fixture := consoleAssertionServer(t, false) // rate limiter allows 3 failures per minute
	for i := 0; i < 3; i++ {
		token := signTestConsoleAssertion(t, fixture.privateKey, fixture.keyID, nil)
		request := consoleAssertionExchangeRequest(token)
		request.Header.Set("Origin", "https://attacker.example")
		recorder := httptest.NewRecorder()
		fixture.server.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "ORIGIN_REJECTED") {
			t.Fatalf("attempt %d response=%d body=%s", i, recorder.Code, recorder.Body.String())
		}
	}
	// A LEGITIMATE origin, still rate limited by the three prior failures.
	token := signTestConsoleAssertion(t, fixture.privateKey, fixture.keyID, nil)
	recorder := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(recorder, consoleAssertionExchangeRequest(token))
	if recorder.Code != http.StatusTooManyRequests || !strings.Contains(recorder.Body.String(), "RATE_LIMITED") {
		t.Fatalf("response=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestConsoleAssertionOriginAllowed is a pure-function table test of the
// origin-matching logic itself, independent of the HTTP-level tests above.
func TestConsoleAssertionOriginAllowed(t *testing.T) {
	cases := []struct {
		name                          string
		origin, invoiceOrigin, issuer string
		want                          bool
	}{
		{"matches invoice origin", "https://invoice.example", "https://invoice.example", "https://console.example", true},
		{"matches console issuer", "https://console.example", "https://invoice.example", "https://console.example", true},
		{"matches neither", "https://attacker.example", "https://invoice.example", "https://console.example", false},
		{"empty origin never matches", "", "https://invoice.example", "https://console.example", false},
		{"unset invoice origin does not widen the check", "https://invoice.example", "", "https://console.example", false},
		{"unset console issuer does not widen the check", "https://console.example", "https://invoice.example", "", false},
		{"both unset", "https://invoice.example", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := consoleAssertionOriginAllowed(tc.origin, tc.invoiceOrigin, tc.issuer); got != tc.want {
				t.Fatalf("consoleAssertionOriginAllowed(%q,%q,%q)=%v want %v", tc.origin, tc.invoiceOrigin, tc.issuer, got, tc.want)
			}
		})
	}
}

func TestConsoleAssertionExchangeDisabledReturnsServiceUnavailable(t *testing.T) {
	fixture := consoleAssertionServer(t, false)
	fixture.server.productionAuth.ConsoleAssertionEnabled = false
	token := signTestConsoleAssertion(t, fixture.privateKey, fixture.keyID, nil)

	recorder := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(recorder, consoleAssertionExchangeRequest(token))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "CONSOLE_ASSERTION_DISABLED") {
		t.Fatalf("response=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestConsoleAssertionExchangeRateLimited(t *testing.T) {
	fixture := consoleAssertionServer(t, false) // rate limiter allows 3 failures per minute
	for i := 0; i < 3; i++ {
		token := signTestConsoleAssertion(t, fixture.privateKey, fixture.keyID, func(p map[string]any) { p["scope"] = "not-a-real-scope" })
		recorder := httptest.NewRecorder()
		fixture.server.Handler().ServeHTTP(recorder, consoleAssertionExchangeRequest(token))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d response=%d body=%s", i, recorder.Code, recorder.Body.String())
		}
	}
	token := signTestConsoleAssertion(t, fixture.privateKey, fixture.keyID, nil) // a valid one, still rate limited
	recorder := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(recorder, consoleAssertionExchangeRequest(token))
	if recorder.Code != http.StatusTooManyRequests || !strings.Contains(recorder.Body.String(), "RATE_LIMITED") {
		t.Fatalf("response=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestOIDCAdminLoginDisabledUnregistersOIDCRoutesButKeepsAssertionWorking(t *testing.T) {
	fixture := consoleAssertionServer(t, true)

	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/callback", "/api/v1/auth/admin/step-up"} {
		request := httptest.NewRequest(http.MethodGet, "https://invoice.example"+path, nil)
		request.RemoteAddr = "127.0.0.1:443"
		recorder := httptest.NewRecorder()
		fixture.server.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("path=%s expected 404 when OIDC admin login is disabled, got %d", path, recorder.Code)
		}
	}
	backchannel := httptest.NewRequest(http.MethodPost, "https://invoice.example/api/v1/auth/backchannel-logout", strings.NewReader("logout_token=x"))
	backchannel.RemoteAddr = "127.0.0.1:443"
	backchannel.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	backchannelRecorder := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(backchannelRecorder, backchannel)
	if backchannelRecorder.Code != http.StatusNotFound {
		t.Fatalf("backchannel-logout expected 404 when OIDC admin login is disabled, got %d", backchannelRecorder.Code)
	}

	// The assertion path must work completely unaffected.
	token := signTestConsoleAssertion(t, fixture.privateKey, fixture.keyID, nil)
	exchangeRecorder := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(exchangeRecorder, consoleAssertionExchangeRequest(token))
	if exchangeRecorder.Code != http.StatusOK {
		t.Fatalf("exchange with OIDC disabled response=%d body=%s", exchangeRecorder.Code, exchangeRecorder.Body.String())
	}
	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range exchangeRecorder.Result().Cookies() {
		switch cookie.Name {
		case defaultSessionCookieName:
			sessionCookie = cookie
		case csrfCookieName:
			csrfCookie = cookie
		}
	}

	// Logout for this assertion-issued session must not touch a.Logout
	// (nil here): if it did, this would panic instead of responding.
	logout := httptest.NewRequest(http.MethodPost, "https://invoice.example/api/v1/auth/logout", nil)
	logout.RemoteAddr = "127.0.0.1:443"
	logout.Header.Set("User-Agent", "console-embed")
	logout.Header.Set("Origin", "https://invoice.example")
	logout.Header.Set("Sec-Fetch-Site", "same-origin")
	logout.Header.Set("X-CSRF-Token", csrfCookie.Value)
	logout.AddCookie(sessionCookie)
	logout.AddCookie(csrfCookie)
	logoutRecorder := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(logoutRecorder, logout)
	if logoutRecorder.Code != http.StatusOK || strings.Contains(logoutRecorder.Body.String(), "logout_url") {
		t.Fatalf("assertion-session logout response=%d body=%s", logoutRecorder.Code, logoutRecorder.Body.String())
	}

	// session_status must still report oidc_admin_login_enabled=false so
	// the frontend can hide the OIDC entry (unauthenticated branch, since
	// we just logged out above).
	status := httptest.NewRequest(http.MethodGet, "https://invoice.example/api/v1/auth/session", nil)
	status.RemoteAddr = "127.0.0.1:443"
	statusRecorder := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(statusRecorder, status)
	if !strings.Contains(statusRecorder.Body.String(), `"oidc_admin_login_enabled":false`) {
		t.Fatalf("expected oidc_admin_login_enabled=false, got %s", statusRecorder.Body.String())
	}
}

func TestConsoleAssertionAndOIDCLoginCoexistOnTheSameDeployment(t *testing.T) {
	fixture := consoleAssertionServer(t, false)

	// A real OIDC login still works unchanged (design spec test #23).
	login := httptest.NewRequest(http.MethodGet, "https://invoice.example/api/v1/auth/login?return_to=/orders", nil)
	login.RemoteAddr = "127.0.0.1:443"
	login.Header.Set("User-Agent", "oidc-browser")
	loginRecorder := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(loginRecorder, login)
	if loginRecorder.Code != http.StatusSeeOther {
		t.Fatalf("OIDC login response=%d", loginRecorder.Code)
	}

	// A console assertion also works, on the very same server/AdminPolicy.
	token := signTestConsoleAssertion(t, fixture.privateKey, fixture.keyID, nil)
	exchangeRecorder := httptest.NewRecorder()
	fixture.server.Handler().ServeHTTP(exchangeRecorder, consoleAssertionExchangeRequest(token))
	if exchangeRecorder.Code != http.StatusOK {
		t.Fatalf("exchange response=%d body=%s", exchangeRecorder.Code, exchangeRecorder.Body.String())
	}
}
