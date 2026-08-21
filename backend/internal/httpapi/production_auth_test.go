package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
			ID   string `json:"id"`
			Role string `json:"role"`
		} `json:"user"`
	}
	if err := json.Unmarshal(statusRecorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Authenticated || response.CSRFToken != csrfCookie.Value || response.User.Role != "user" {
		t.Fatalf("unexpected session bootstrap: %+v", response)
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
