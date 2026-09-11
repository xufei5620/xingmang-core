package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"invoice-system/backend/internal/auth"
	"invoice-system/backend/internal/ledger"
	"invoice-system/backend/staffauth"
)

// fakePlatformAuthenticator is a scriptable test double for
// auth.PlatformAuthenticator. It never talks to a real Sub2API/New API
// endpoint -- see sub2api_login_test.go/newapi_login_test.go in the auth
// package for httptest-server-level coverage of the actual wire forwarding.
// This double instead lets the httpapi-level tests below exercise the HTTP
// handler itself: routing, same-site/CSRF-adjacent checks, rate limiting,
// error-code mapping, and -- most importantly -- session issuance and
// cross-platform isolation.
type fakePlatformAuthenticator struct {
	mu         sync.Mutex
	loginCalls int
	twoFACalls int
	onLogin    func(identifier, password string) (auth.PlatformLoginResult, error)
	onTwoFA    func(tempToken, code string) (auth.PlatformLoginResult, error)
}

func (f *fakePlatformAuthenticator) Login(_ context.Context, identifier, password string) (auth.PlatformLoginResult, error) {
	f.mu.Lock()
	f.loginCalls++
	f.mu.Unlock()
	return f.onLogin(identifier, password)
}

func (f *fakePlatformAuthenticator) VerifyTwoFA(_ context.Context, tempToken, code string) (auth.PlatformLoginResult, error) {
	f.mu.Lock()
	f.twoFACalls++
	f.mu.Unlock()
	return f.onTwoFA(tempToken, code)
}

func (f *fakePlatformAuthenticator) counts() (login int, twoFA int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loginCalls, f.twoFACalls
}

func rejectingAuthenticator() *fakePlatformAuthenticator {
	return &fakePlatformAuthenticator{
		onLogin: func(string, string) (auth.PlatformLoginResult, error) {
			return auth.PlatformLoginResult{}, auth.ErrPlatformCredentialsInvalid
		},
		onTwoFA: func(string, string) (auth.PlatformLoginResult, error) {
			return auth.PlatformLoginResult{}, auth.ErrPlatformTwoFAInvalid
		},
	}
}

// platformLoginServer builds a production session Server with isolated
// platform-password login wired up. ProvisionUser is
// deliberately keyed by (platform, platform_user_id) -- exactly like the real
// PostgresIdentityStore.ResolveOrCreate -- so tests can prove two different
// platform accounts never collapse onto the same local session identity.
func platformLoginServer(t *testing.T, sub2api, newapi auth.PlatformAuthenticator, maxAttempts int, window time.Duration) *Server {
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
	runtime := &ProductionAuth{
		Sessions: sessions, BindingHasher: hasher, CSRF: csrf,
		Admin:         auth.AdminPolicy{Role: "invoice-admin", RequiredACR: "urn:test:mfa", RequiredAMR: []string{"otp"}, StepUpMaxAge: 10 * time.Minute},
		StaffResolver: func(*http.Request) (staffauth.Identity, error) { return staffauth.Identity{}, auth.ErrSessionInvalid },
		ProvisionUser: func(_ context.Context, principal auth.Principal, _ string) (SessionUser, error) {
			return SessionUser{ID: string(principal.Platform) + ":" + principal.PlatformUserID, Email: principal.Email, EmailVerified: principal.EmailVerified}, nil
		},
		LoadUser: func(_ context.Context, userID string) (SessionUser, error) {
			return SessionUser{ID: userID}, nil
		},
	}
	platformLogin := &PlatformLogin{
		Authenticators: map[auth.Platform]auth.PlatformAuthenticator{auth.PlatformSub2API: sub2api, auth.PlatformNewAPI: newapi},
		Origins:        map[auth.Platform]string{auth.PlatformSub2API: "https://sub2api.example", auth.PlatformNewAPI: "https://newapi.example"},
		RateLimiter:    auth.NewLoginRateLimiter(maxAttempts, window),
		PendingTwoFA:   auth.NewPendingTwoFAPlatforms(10 * time.Minute),
		Auth:           runtime,
	}
	server, err := NewWithConfig(ledger.NewService(), Config{
		AuthMode: "session", AdminIPAllowlist: []string{"127.0.0.1/32"}, ProductionAuth: runtime, PlatformLogin: platformLogin,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func newPlatformLoginHTTPRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "https://invoice.example/api/v1/auth/platform-login", strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:443"
	request.Header.Set("User-Agent", "test-browser")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	return request
}

func sessionStatusOf(t *testing.T, server *Server, cookies []*http.Cookie) map[string]any {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "https://invoice.example/api/v1/auth/session", nil)
	request.RemoteAddr = "127.0.0.1:443"
	request.Header.Set("User-Agent", "test-browser")
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("session status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPlatformLoginSuccessIssuesSessionWithPlatformIdentity(t *testing.T) {
	sub2api := &fakePlatformAuthenticator{onLogin: func(identifier, password string) (auth.PlatformLoginResult, error) {
		if identifier != "user@example.com" || password != "correct horse battery staple" {
			t.Fatalf("unexpected forwarded credentials: %q/%q", identifier, password)
		}
		return auth.PlatformLoginResult{PlatformUserID: "555", Username: "exampleuser", Email: "user@example.com", EmailVerified: true}, nil
	}}
	server := platformLoginServer(t, sub2api, rejectingAuthenticator(), 8, time.Minute)

	request := newPlatformLoginHTTPRequest(t, `{"platform":"sub2api","identifier":"user@example.com","password":"correct horse battery staple"}`)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"ok":true`) {
		t.Fatalf("login response=%d body=%s", recorder.Code, recorder.Body.String())
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
	if sessionCookie == nil || !sessionCookie.HttpOnly || !sessionCookie.Secure {
		t.Fatalf("missing secure session cookie: %+v", sessionCookie)
	}
	if csrfCookie == nil {
		t.Fatalf("missing CSRF cookie")
	}

	status := sessionStatusOf(t, server, []*http.Cookie{sessionCookie, csrfCookie})
	if status["authenticated"] != true {
		t.Fatalf("unexpected session status: %+v", status)
	}
	user, _ := status["user"].(map[string]any)
	if user["platform"] != "sub2api" || user["id"] != "sub2api:555" {
		t.Fatalf("unexpected session user: %+v", user)
	}
	if user["platform_user_id"] != "555" {
		t.Fatalf("expected the session's platform_user_id to surface the authenticator's PlatformUserID: %+v", user)
	}
	// XM-INV-OBS-BUNDLE follow-up: the captured username is stored on the
	// session row itself (Session.DisplayName, encrypted in production via
	// PostgresSessionStore -- see postgres_integration_test.go for the
	// encrypt/decrypt round trip), so it survives this GET /session call
	// even though that request only carries a userID, not a principal.
	if user["username"] != "exampleuser" || user["display_name"] != "exampleuser" {
		t.Fatalf("expected the session's captured username to surface on this session reload too: %+v", user)
	}

	if login, twoFA := sub2api.counts(); login != 1 || twoFA != 0 {
		t.Fatalf("unexpected authenticator calls: login=%d twoFA=%d", login, twoFA)
	}
}

func TestPlatformLoginSuccessLogsStructuredInfo(t *testing.T) {
	// XM-INV-OBS-BUNDLE: a successful platform login previously logged
	// nothing at all (only failures were logged), leaving operators with no
	// positive signal that logins were working. The log must carry enough to
	// correlate a login with its session (request_id, platform, a claimed-vs-
	// created marker, and a short user id prefix) and must never carry the
	// submitted password.
	const password = "correct horse battery staple sentinel"
	sub2api := &fakePlatformAuthenticator{onLogin: func(identifier, gotPassword string) (auth.PlatformLoginResult, error) {
		if gotPassword != password {
			t.Fatalf("unexpected forwarded password: %q", gotPassword)
		}
		return auth.PlatformLoginResult{PlatformUserID: "777", Username: "loguser", Email: "log@example.com", EmailVerified: true}, nil
	}}
	server := platformLoginServer(t, sub2api, rejectingAuthenticator(), 8, time.Minute)
	// Override ProvisionUser with a fixed ID and Claimed:true so the log
	// assertions below are deterministic and exercise the claim-path branch
	// of the new "claimed" field (the create path is already covered by
	// TestPlatformLoginSuccessIssuesSessionWithPlatformIdentity's default).
	const provisionedUserID = "10000000-0000-4000-8000-000000000001"
	server.productionAuth.ProvisionUser = func(_ context.Context, principal auth.Principal, _ string) (SessionUser, error) {
		return SessionUser{ID: provisionedUserID, Claimed: true, Email: principal.Email, EmailVerified: principal.EmailVerified}, nil
	}
	var logs bytes.Buffer
	server.logger = slog.New(slog.NewTextHandler(&logs, nil))

	request := newPlatformLoginHTTPRequest(t, `{"platform":"sub2api","identifier":"log@example.com","password":"`+password+`"}`)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	logText := logs.String()
	for _, want := range []string{
		"platform login succeeded", "platform=sub2api", "claimed=true", "user_id=10000000", "request_id=",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("log missing %q: %s", want, logText)
		}
	}
	if strings.Contains(logText, password) {
		t.Fatalf("log must never contain the submitted password: %s", logText)
	}
	if strings.Contains(logText, provisionedUserID) {
		t.Fatalf("log must only carry the 8-char user id prefix, not the full id: %s", logText)
	}
}

func TestPlatformLoginWrongCredentialsRejectedGenerically(t *testing.T) {
	server := platformLoginServer(t, rejectingAuthenticator(), rejectingAuthenticator(), 8, time.Minute)
	request := newPlatformLoginHTTPRequest(t, `{"platform":"sub2api","identifier":"nobody@example.com","password":"wrong"}`)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "PLATFORM_CREDENTIALS_INVALID") {
		t.Fatalf("body should carry the generic credentials-invalid code: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "nobody@example.com") {
		t.Fatalf("error body must not echo the submitted identifier: %s", recorder.Body.String())
	}
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == defaultSessionCookieName && cookie.Value != "" {
			t.Fatalf("a failed login must not issue a session cookie")
		}
	}
}

func TestPlatformLoginUnknownAccountAndWrongPasswordGetIdenticalResponse(t *testing.T) {
	// The authenticator layer already folds these (see
	// TestSub2APILoginWrongPasswordAndUnknownEmailAreIndistinguishable in the
	// auth package); this asserts the HTTP layer preserves that -- CR-0004's
	// "failure must not leak whether the account exists".
	server := platformLoginServer(t, rejectingAuthenticator(), rejectingAuthenticator(), 8, time.Minute)
	unknown := httptest.NewRecorder()
	server.Handler().ServeHTTP(unknown, newPlatformLoginHTTPRequest(t, `{"platform":"sub2api","identifier":"unknown@example.com","password":"x"}`))
	wrong := httptest.NewRecorder()
	server.Handler().ServeHTTP(wrong, newPlatformLoginHTTPRequest(t, `{"platform":"sub2api","identifier":"known@example.com","password":"wrong"}`))
	if unknown.Code != wrong.Code || unknown.Body.String() != wrong.Body.String() {
		t.Fatalf("responses differ: unknown=%d %q wrong=%d %q", unknown.Code, unknown.Body.String(), wrong.Code, wrong.Body.String())
	}
}

func TestPlatformLoginRateLimitedAfterRepeatedFailures(t *testing.T) {
	sub2api := rejectingAuthenticator()
	server := platformLoginServer(t, sub2api, rejectingAuthenticator(), 2, time.Minute)
	body := `{"platform":"sub2api","identifier":"repeat@example.com","password":"wrong"}`

	for i := 0; i < 2; i++ {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, newPlatformLoginHTTPRequest(t, body))
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Fatalf("attempt %d status=%d body=%s", i, recorder.Code, recorder.Body.String())
		}
	}
	// Third attempt: the rate limiter must reject before the authenticator is
	// ever called again.
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, newPlatformLoginHTTPRequest(t, body))
	if recorder.Code != http.StatusTooManyRequests || !strings.Contains(recorder.Body.String(), "PLATFORM_LOGIN_RATE_LIMITED") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if login, _ := sub2api.counts(); login != 2 {
		t.Fatalf("rate-limited attempt must not reach the authenticator: login calls=%d", login)
	}

	// A different account from the same IP is unaffected: the key is scoped
	// to IP+account, not IP alone.
	other := httptest.NewRecorder()
	server.Handler().ServeHTTP(other, newPlatformLoginHTTPRequest(t, `{"platform":"sub2api","identifier":"someone-else@example.com","password":"wrong"}`))
	if other.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a different account must not be blocked by another account's lockout: status=%d body=%s", other.Code, other.Body.String())
	}
}

func TestPlatformLoginSuccessResetsPriorFailureCount(t *testing.T) {
	var calls int
	sub2api := &fakePlatformAuthenticator{onLogin: func(identifier, password string) (auth.PlatformLoginResult, error) {
		calls++
		if password == "correct" {
			return auth.PlatformLoginResult{PlatformUserID: "9", Email: identifier}, nil
		}
		return auth.PlatformLoginResult{}, auth.ErrPlatformCredentialsInvalid
	}}
	server := platformLoginServer(t, sub2api, rejectingAuthenticator(), 2, time.Minute)
	body := func(password string) string {
		return `{"platform":"sub2api","identifier":"reset@example.com","password":"` + password + `"}`
	}
	fail := httptest.NewRecorder()
	server.Handler().ServeHTTP(fail, newPlatformLoginHTTPRequest(t, body("wrong")))
	if fail.Code != http.StatusUnprocessableEntity {
		t.Fatalf("first attempt status=%d", fail.Code)
	}
	success := httptest.NewRecorder()
	server.Handler().ServeHTTP(success, newPlatformLoginHTTPRequest(t, body("correct")))
	if success.Code != http.StatusOK {
		t.Fatalf("second attempt (success) status=%d body=%s", success.Code, success.Body.String())
	}
	// Two more failures after the successful login must still be allowed
	// before lockout (maxAttempts=2): this proves Reset actually cleared the
	// earlier failure rather than failures accumulating across the login.
	for i := 0; i < 2; i++ {
		attempt := httptest.NewRecorder()
		server.Handler().ServeHTTP(attempt, newPlatformLoginHTTPRequest(t, body("wrong-again")))
		if attempt.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d unexpectedly rate-limited right after a successful login reset the counter", i)
		}
	}
	if calls != 4 {
		t.Fatalf("expected 4 authenticator calls, got %d", calls)
	}
}

func TestPlatformLoginTwoFAFlowOnlyIssuesSessionAfterVerification(t *testing.T) {
	sub2api := &fakePlatformAuthenticator{
		onLogin: func(identifier, password string) (auth.PlatformLoginResult, error) {
			return auth.PlatformLoginResult{RequiresTwoFA: true, TempToken: "temp-token-0123456789"}, nil
		},
		onTwoFA: func(tempToken, code string) (auth.PlatformLoginResult, error) {
			if tempToken != "temp-token-0123456789" || code != "123456" {
				return auth.PlatformLoginResult{}, auth.ErrPlatformTwoFAInvalid
			}
			return auth.PlatformLoginResult{PlatformUserID: "7", Email: "twofa@example.com"}, nil
		},
	}
	server := platformLoginServer(t, sub2api, rejectingAuthenticator(), 8, time.Minute)

	first := httptest.NewRecorder()
	server.Handler().ServeHTTP(first, newPlatformLoginHTTPRequest(t, `{"platform":"sub2api","identifier":"twofa@example.com","password":"correct"}`))
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"requires_two_fa":true`) || !strings.Contains(first.Body.String(), "temp-token-0123456789") {
		t.Fatalf("first step status=%d body=%s", first.Code, first.Body.String())
	}
	for _, cookie := range first.Result().Cookies() {
		if cookie.Name == defaultSessionCookieName && cookie.Value != "" {
			t.Fatalf("a login requiring 2FA must not issue a session cookie before verification")
		}
	}

	wrongCode := httptest.NewRequest(http.MethodPost, "https://invoice.example/api/v1/auth/platform-login/2fa", strings.NewReader(`{"platform":"sub2api","temp_token":"temp-token-0123456789","code":"000000"}`))
	wrongCode.RemoteAddr = "127.0.0.1:443"
	wrongCode.Header.Set("Content-Type", "application/json")
	wrongCode.Header.Set("Sec-Fetch-Site", "same-origin")
	wrongRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(wrongRecorder, wrongCode)
	if wrongRecorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong 2FA code status=%d body=%s", wrongRecorder.Code, wrongRecorder.Body.String())
	}

	rightCode := httptest.NewRequest(http.MethodPost, "https://invoice.example/api/v1/auth/platform-login/2fa", strings.NewReader(`{"platform":"sub2api","temp_token":"temp-token-0123456789","code":"123456"}`))
	rightCode.RemoteAddr = "127.0.0.1:443"
	rightCode.Header.Set("Content-Type", "application/json")
	rightCode.Header.Set("Sec-Fetch-Site", "same-origin")
	rightRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(rightRecorder, rightCode)
	if rightRecorder.Code != http.StatusOK {
		t.Fatalf("correct 2FA code status=%d body=%s", rightRecorder.Code, rightRecorder.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, cookie := range rightRecorder.Result().Cookies() {
		if cookie.Name == defaultSessionCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil {
		t.Fatalf("verified 2FA login must issue a session cookie")
	}

	if login, twoFA := sub2api.counts(); login != 1 || twoFA != 2 {
		t.Fatalf("unexpected authenticator calls: login=%d twoFA=%d", login, twoFA)
	}
}

func TestPlatformLoginCrossSiteRequestRejected(t *testing.T) {
	sub2api := &fakePlatformAuthenticator{onLogin: func(string, string) (auth.PlatformLoginResult, error) {
		t.Fatal("authenticator must not be called for a cross-site request")
		return auth.PlatformLoginResult{}, nil
	}}
	server := platformLoginServer(t, sub2api, rejectingAuthenticator(), 8, time.Minute)
	request := newPlatformLoginHTTPRequest(t, `{"platform":"sub2api","identifier":"user@example.com","password":"x"}`)
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "CROSS_SITE_REQUEST_REJECTED") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPlatformLoginInvalidPlatformRejected(t *testing.T) {
	server := platformLoginServer(t, rejectingAuthenticator(), rejectingAuthenticator(), 8, time.Minute)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, newPlatformLoginHTTPRequest(t, `{"platform":"keycloak","identifier":"user@example.com","password":"x"}`))
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "INVALID_PLATFORM") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPlatformLoginMissingCredentialsRejected(t *testing.T) {
	server := platformLoginServer(t, rejectingAuthenticator(), rejectingAuthenticator(), 8, time.Minute)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, newPlatformLoginHTTPRequest(t, `{"platform":"sub2api","identifier":"user@example.com","password":""}`))
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "PLATFORM_CREDENTIALS_REQUIRED") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPlatformLoginSessionsAreIsolatedAcrossPlatformsAndAccounts(t *testing.T) {
	sub2api := &fakePlatformAuthenticator{onLogin: func(identifier, password string) (auth.PlatformLoginResult, error) {
		return auth.PlatformLoginResult{PlatformUserID: "111", Email: identifier}, nil
	}}
	newapi := &fakePlatformAuthenticator{onLogin: func(identifier, password string) (auth.PlatformLoginResult, error) {
		return auth.PlatformLoginResult{PlatformUserID: "222", Username: identifier}, nil
	}}
	server := platformLoginServer(t, sub2api, newapi, 8, time.Minute)

	loginAs := func(body string) (sessionCookie, csrfCookie *http.Cookie) {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, newPlatformLoginHTTPRequest(t, body))
		if recorder.Code != http.StatusOK {
			t.Fatalf("login status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		for _, cookie := range recorder.Result().Cookies() {
			switch cookie.Name {
			case defaultSessionCookieName:
				sessionCookie = cookie
			case csrfCookieName:
				csrfCookie = cookie
			}
		}
		return
	}

	sub2Session, sub2CSRF := loginAs(`{"platform":"sub2api","identifier":"person@example.com","password":"x"}`)
	newSession, newCSRF := loginAs(`{"platform":"newapi","identifier":"person","password":"x"}`)
	if sub2Session == nil || newSession == nil || sub2Session.Value == newSession.Value {
		t.Fatalf("expected two distinct session tokens, got sub2api=%v newapi=%v", sub2Session, newSession)
	}

	sub2Status := sessionStatusOf(t, server, []*http.Cookie{sub2Session, sub2CSRF})
	newStatus := sessionStatusOf(t, server, []*http.Cookie{newSession, newCSRF})

	sub2User, _ := sub2Status["user"].(map[string]any)
	newUser, _ := newStatus["user"].(map[string]any)
	if sub2User["platform"] != "sub2api" || sub2User["id"] != "sub2api:111" {
		t.Fatalf("unexpected sub2api session user: %+v", sub2User)
	}
	if newUser["platform"] != "newapi" || newUser["id"] != "newapi:222" {
		t.Fatalf("unexpected newapi session user: %+v", newUser)
	}
	if sub2User["platform_user_id"] != "111" || newUser["platform_user_id"] != "222" {
		t.Fatalf("expected each session's platform_user_id to stay isolated to its own platform account: sub2=%+v new=%+v", sub2User, newUser)
	}
	if sub2User["id"] == newUser["id"] {
		t.Fatalf("sub2api and newapi identities must never collapse onto the same local user")
	}
}

func TestPlatformLoginRuntimeRequiresBothAuthenticators(t *testing.T) {
	incomplete := &PlatformLogin{
		Authenticators: map[auth.Platform]auth.PlatformAuthenticator{auth.PlatformSub2API: rejectingAuthenticator()},
		Origins:        map[auth.Platform]string{auth.PlatformSub2API: "https://sub2api.example", auth.PlatformNewAPI: "https://newapi.example"},
		RateLimiter:    auth.NewLoginRateLimiter(8, time.Minute),
		Auth:           &ProductionAuth{},
	}
	if err := incomplete.Validate(); err == nil {
		t.Fatal("expected an error when the New API authenticator is missing")
	}
}

func TestPlatformLoginRuntimeRequiresPendingTwoFAStore(t *testing.T) {
	incomplete := &PlatformLogin{
		Authenticators: map[auth.Platform]auth.PlatformAuthenticator{auth.PlatformSub2API: rejectingAuthenticator(), auth.PlatformNewAPI: rejectingAuthenticator()},
		Origins:        map[auth.Platform]string{auth.PlatformSub2API: "https://sub2api.example", auth.PlatformNewAPI: "https://newapi.example"},
		RateLimiter:    auth.NewLoginRateLimiter(8, time.Minute),
		Auth:           &ProductionAuth{},
		// PendingTwoFA deliberately left nil.
	}
	if err := incomplete.Validate(); err == nil {
		t.Fatal("expected an error when the pending-2FA-platform store is missing")
	}
}

// --- XM-INV-AUTOLOGIN: auto-detect (no `platform` field) coverage below.
// The explicit-`platform` tests above are unmodified and continue to pass
// unchanged, proving the pre-auto-detect behavior is preserved as an
// ops/test-only override path.

func TestPlatformLoginAutoDetectEmailIdentifierTriesSub2APIFirst(t *testing.T) {
	sub2api := &fakePlatformAuthenticator{onLogin: func(identifier, password string) (auth.PlatformLoginResult, error) {
		if identifier != "person@example.com" || password != "x" {
			t.Fatalf("unexpected forwarded credentials: %q/%q", identifier, password)
		}
		return auth.PlatformLoginResult{PlatformUserID: "1", Email: identifier}, nil
	}}
	newapi := &fakePlatformAuthenticator{onLogin: func(string, string) (auth.PlatformLoginResult, error) {
		t.Fatal("New API must not be consulted when Sub2API already accepted the credentials")
		return auth.PlatformLoginResult{}, nil
	}}
	server := platformLoginServer(t, sub2api, newapi, 8, time.Minute)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, newPlatformLoginHTTPRequest(t, `{"identifier":"person@example.com","password":"x"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if login, _ := sub2api.counts(); login != 1 {
		t.Fatalf("expected exactly one Sub2API attempt, got %d", login)
	}

	status := sessionStatusOf(t, server, recorder.Result().Cookies())
	user, _ := status["user"].(map[string]any)
	if user["platform"] != "sub2api" {
		t.Fatalf("expected the session to be attributed to sub2api, got: %+v", user)
	}
}

func TestPlatformLoginAutoDetectFallsBackToNewAPIWhenSub2APIRejectsEmailShapedIdentifier(t *testing.T) {
	// A New API account's own username can itself happen to look like an
	// email address; auto-detect must still find it once Sub2API has
	// definitively said the credentials are invalid there.
	sub2api := rejectingAuthenticator()
	newapi := &fakePlatformAuthenticator{onLogin: func(identifier, password string) (auth.PlatformLoginResult, error) {
		if identifier != "person@example.com" {
			t.Fatalf("unexpected identifier forwarded to New API: %q", identifier)
		}
		return auth.PlatformLoginResult{PlatformUserID: "88", Username: identifier}, nil
	}}
	server := platformLoginServer(t, sub2api, newapi, 8, time.Minute)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, newPlatformLoginHTTPRequest(t, `{"identifier":"person@example.com","password":"x"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if login, _ := sub2api.counts(); login != 1 {
		t.Fatalf("expected exactly one Sub2API attempt before falling back, got %d", login)
	}
	if login, _ := newapi.counts(); login != 1 {
		t.Fatalf("expected exactly one New API attempt, got %d", login)
	}

	status := sessionStatusOf(t, server, recorder.Result().Cookies())
	user, _ := status["user"].(map[string]any)
	if user["platform"] != "newapi" {
		t.Fatalf("expected the session to be attributed to newapi, got: %+v", user)
	}
}

func TestPlatformLoginAutoDetectUsernameIdentifierOnlyTriesNewAPI(t *testing.T) {
	sub2api := &fakePlatformAuthenticator{onLogin: func(string, string) (auth.PlatformLoginResult, error) {
		t.Fatal("Sub2API only accepts email-shaped identifiers; a bare username must never be forwarded to it")
		return auth.PlatformLoginResult{}, nil
	}}
	newapi := &fakePlatformAuthenticator{onLogin: func(identifier, password string) (auth.PlatformLoginResult, error) {
		return auth.PlatformLoginResult{PlatformUserID: "2", Username: identifier}, nil
	}}
	server := platformLoginServer(t, sub2api, newapi, 8, time.Minute)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, newPlatformLoginHTTPRequest(t, `{"identifier":"plainusername","password":"x"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if login, _ := newapi.counts(); login != 1 {
		t.Fatalf("expected exactly one New API attempt, got %d", login)
	}
}

func TestPlatformLoginAutoDetectBothPlatformsInvalidIsGeneric(t *testing.T) {
	server := platformLoginServer(t, rejectingAuthenticator(), rejectingAuthenticator(), 8, time.Minute)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, newPlatformLoginHTTPRequest(t, `{"identifier":"nobody@example.com","password":"wrong"}`))
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "PLATFORM_CREDENTIALS_INVALID") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "账号或密码不正确") {
		t.Fatalf("expected the generic Chinese message: %s", recorder.Body.String())
	}
}

func TestPlatformLoginAutoDetectUpstreamUnavailableDoesNotFallBack(t *testing.T) {
	sub2api := &fakePlatformAuthenticator{onLogin: func(string, string) (auth.PlatformLoginResult, error) {
		return auth.PlatformLoginResult{}, auth.ErrPlatformUnavailable
	}}
	newapi := &fakePlatformAuthenticator{onLogin: func(string, string) (auth.PlatformLoginResult, error) {
		t.Fatal("an upstream outage must not trigger platform-switching fallback")
		return auth.PlatformLoginResult{}, nil
	}}
	server := platformLoginServer(t, sub2api, newapi, 8, time.Minute)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, newPlatformLoginHTTPRequest(t, `{"identifier":"person@example.com","password":"x"}`))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "PLATFORM_LOGIN_UNAVAILABLE") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if login, _ := sub2api.counts(); login != 1 {
		t.Fatalf("expected exactly one Sub2API attempt, got %d", login)
	}
}

func TestPlatformLoginAutoDetectMissingCredentialsStillRejected(t *testing.T) {
	server := platformLoginServer(t, rejectingAuthenticator(), rejectingAuthenticator(), 8, time.Minute)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, newPlatformLoginHTTPRequest(t, `{"identifier":"","password":"x"}`))
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "PLATFORM_CREDENTIALS_REQUIRED") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPlatformLoginExplicitPlatformBypassesAutoDetectOrder(t *testing.T) {
	// A bare-username identifier would normally auto-route to New API only;
	// explicitly requesting sub2api must still go straight to Sub2API with no
	// auto-detection at all.
	sub2api := &fakePlatformAuthenticator{onLogin: func(identifier, password string) (auth.PlatformLoginResult, error) {
		return auth.PlatformLoginResult{PlatformUserID: "3", Email: identifier}, nil
	}}
	newapi := &fakePlatformAuthenticator{onLogin: func(string, string) (auth.PlatformLoginResult, error) {
		t.Fatal("an explicit platform selection must never consult the other platform")
		return auth.PlatformLoginResult{}, nil
	}}
	server := platformLoginServer(t, sub2api, newapi, 8, time.Minute)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, newPlatformLoginHTTPRequest(t, `{"platform":"sub2api","identifier":"plainusername","password":"x"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if login, _ := sub2api.counts(); login != 1 {
		t.Fatalf("expected exactly one Sub2API attempt, got %d", login)
	}
}

func TestPlatformLoginAutoDetectRateLimitSharesOneBucketPerRequest(t *testing.T) {
	sub2api := rejectingAuthenticator()
	newapi := rejectingAuthenticator()
	server := platformLoginServer(t, sub2api, newapi, 2, time.Minute)
	body := `{"identifier":"shared@example.com","password":"wrong"}`

	for i := 0; i < 2; i++ {
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, newPlatformLoginHTTPRequest(t, body))
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Fatalf("attempt %d status=%d body=%s", i, recorder.Code, recorder.Body.String())
		}
	}
	// If one auto-detect request incorrectly charged the rate limiter once per
	// upstream platform tried (instead of once per request), maxAttempts=2
	// would already have been exhausted after the FIRST request above, and
	// the second would already be 429.
	if login, _ := sub2api.counts(); login != 2 {
		t.Fatalf("expected 2 Sub2API attempts, got %d", login)
	}
	if login, _ := newapi.counts(); login != 2 {
		t.Fatalf("expected 2 New API attempts, got %d", login)
	}

	third := httptest.NewRecorder()
	server.Handler().ServeHTTP(third, newPlatformLoginHTTPRequest(t, body))
	if third.Code != http.StatusTooManyRequests {
		t.Fatalf("third attempt status=%d body=%s", third.Code, third.Body.String())
	}
	if login, _ := sub2api.counts(); login != 2 {
		t.Fatalf("rate-limited attempt must not reach the authenticator: sub2api login calls=%d", login)
	}
}

func TestPlatformLoginAutoDetectTwoFARemembersPlatformWithoutClientResend(t *testing.T) {
	sub2api := &fakePlatformAuthenticator{
		onLogin: func(identifier, password string) (auth.PlatformLoginResult, error) {
			return auth.PlatformLoginResult{RequiresTwoFA: true, TempToken: "auto-temp-token-0123456789"}, nil
		},
		onTwoFA: func(tempToken, code string) (auth.PlatformLoginResult, error) {
			if tempToken != "auto-temp-token-0123456789" || code != "123456" {
				return auth.PlatformLoginResult{}, auth.ErrPlatformTwoFAInvalid
			}
			return auth.PlatformLoginResult{PlatformUserID: "42", Email: "auto2fa@example.com"}, nil
		},
	}
	newapi := &fakePlatformAuthenticator{onLogin: func(string, string) (auth.PlatformLoginResult, error) {
		t.Fatal("New API must not be consulted once Sub2API accepts the credentials and requests 2FA")
		return auth.PlatformLoginResult{}, nil
	}}
	server := platformLoginServer(t, sub2api, newapi, 8, time.Minute)

	first := httptest.NewRecorder()
	server.Handler().ServeHTTP(first, newPlatformLoginHTTPRequest(t, `{"identifier":"auto2fa@example.com","password":"correct"}`))
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"requires_two_fa":true`) {
		t.Fatalf("first step status=%d body=%s", first.Code, first.Body.String())
	}
	for _, cookie := range first.Result().Cookies() {
		if cookie.Name == defaultSessionCookieName && cookie.Value != "" {
			t.Fatalf("a login requiring 2FA must not issue a session cookie before verification")
		}
	}

	// The follow-up 2FA request carries no `platform` field at all: the
	// browser never displayed a platform choice in auto-detect mode.
	verify := httptest.NewRequest(http.MethodPost, "https://invoice.example/api/v1/auth/platform-login/2fa", strings.NewReader(`{"temp_token":"auto-temp-token-0123456789","code":"123456"}`))
	verify.RemoteAddr = "127.0.0.1:443"
	verify.Header.Set("Content-Type", "application/json")
	verify.Header.Set("Sec-Fetch-Site", "same-origin")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, verify)
	if recorder.Code != http.StatusOK {
		t.Fatalf("2fa verify status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == defaultSessionCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil {
		t.Fatalf("verified auto-detected 2FA login must issue a session cookie")
	}
	if login, twoFA := sub2api.counts(); login != 1 || twoFA != 1 {
		t.Fatalf("unexpected sub2api calls: login=%d twoFA=%d", login, twoFA)
	}
	if login, _ := newapi.counts(); login != 0 {
		t.Fatalf("New API must never be called: login calls=%d", login)
	}
}

func TestPlatformLoginTwoFAWithoutPlatformAndUnknownTempTokenIsRejectedGenerically(t *testing.T) {
	server := platformLoginServer(t, rejectingAuthenticator(), rejectingAuthenticator(), 8, time.Minute)
	request := httptest.NewRequest(http.MethodPost, "https://invoice.example/api/v1/auth/platform-login/2fa", strings.NewReader(`{"temp_token":"totally-unknown-token-000000","code":"123456"}`))
	request.RemoteAddr = "127.0.0.1:443"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity || !strings.Contains(recorder.Body.String(), "PLATFORM_CREDENTIALS_INVALID") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
