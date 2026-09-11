package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"invoice-system/backend/internal/auth"
	"invoice-system/backend/internal/ledger"
	"invoice-system/backend/staffauth"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func productionAuthServer(t *testing.T) (*Server, *auth.SessionManager) {
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
		Admin:         auth.AdminPolicy{Role: "invoice-admin", RequiredACR: "mfa", RequiredAMR: []string{"otp"}, StepUpMaxAge: 10 * time.Minute},
		StaffResolver: func(*http.Request) (staffauth.Identity, error) { return staffauth.Identity{}, auth.ErrSessionInvalid },
		ProvisionUser: func(context.Context, auth.Principal, string) (SessionUser, error) {
			return SessionUser{ID: "10000000-0000-4000-8000-000000000001", Email: "user@example.com", EmailVerified: true}, nil
		},
		LoadUser: func(context.Context, string) (SessionUser, error) {
			return SessionUser{ID: "10000000-0000-4000-8000-000000000001", Email: "user@example.com", EmailVerified: true}, nil
		},
	}
	server, err := NewWithConfig(ledger.NewService(), Config{
		AuthMode: "session", AdminIPAllowlist: []string{"127.0.0.1/32"}, ProductionAuth: runtime,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return server, sessions
}

func issueHTTPTestSession(t *testing.T, server *Server, p auth.Principal) auth.SessionCredentials {
	t.Helper()
	r := httptest.NewRequest("GET", "https://invoice.example/", nil)
	r.RemoteAddr = "127.0.0.1:443"
	r.Header.Set("User-Agent", "test-browser")
	binding, err := server.productionAuth.clientBinding(server, r)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := server.productionAuth.Sessions.Issue(context.Background(), auth.IssueSessionInput{UserID: "10000000-0000-4000-8000-000000000001", Principal: p, Binding: binding, RequestID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return credentials
}
func TestProductionSessionAndCSRF(t *testing.T) {
	server, _ := productionAuthServer(t)

	status := httptest.NewRequest(http.MethodGet, "https://invoice.example/api/v1/auth/session", nil)
	status.RemoteAddr = "127.0.0.1:443"
	status.Header.Set("User-Agent", "test-browser")
	statusRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(statusRecorder, status)
	if statusRecorder.Code != http.StatusOK || !strings.Contains(statusRecorder.Body.String(), `"authenticated":false`) {
		t.Fatalf("anonymous session response=%d %s", statusRecorder.Code, statusRecorder.Body.String())
	}

	credentials := issueHTTPTestSession(t, server, auth.Principal{Issuer: "https://sub2api.example", Subject: "subject-1", Platform: auth.PlatformSub2API, PlatformUserID: "subject-1", Roles: []string{"invoice-user"}, AuthTime: time.Now().UTC()})
	callbackRecorder := httptest.NewRecorder()
	server.productionAuth.setSessionCookies(callbackRecorder, credentials)
	var sessionCookie, csrfCookie *http.Cookie
	for _, c := range callbackRecorder.Result().Cookies() {
		if c.Name == defaultSessionCookieName {
			sessionCookie = c
		}
		if c.Name == csrfCookieName {
			csrfCookie = c
		}
	}
	if sessionCookie == nil || csrfCookie == nil || !sessionCookie.HttpOnly || csrfCookie.HttpOnly {
		t.Fatal("invalid session cookie attributes")
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
	if response.User.Platform != string(auth.PlatformSub2API) || response.User.PlatformUserID != "subject-1" {
		t.Fatalf("user session lost its source platform identity: %+v", response.User)
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
	if logoutRecorder.Code != http.StatusOK || strings.Contains(logoutRecorder.Body.String(), `"logout_url"`) {
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
	runtime := &ProductionAuth{Sessions: sessions}
	request := httptest.NewRequest(http.MethodPost, "https://invoice.example/api/v1/auth/logout", nil)
	request = request.WithContext(context.WithValue(request.Context(), identityKey, identity{UserID: credentials.Session.UserID, Session: &credentials.Session, SessionToken: credentials.Token}))
	recorder := httptest.NewRecorder()
	runtime.logout(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "SESSION_REVOKE_FAILED") || len(recorder.Result().Cookies()) != 0 {
		t.Fatalf("logout failure response=%d body=%s cookies=%v", recorder.Code, recorder.Body.String(), recorder.Result().Cookies())
	}
}

func TestBrowserBearerCannotRideValidUserSession(t *testing.T) {
	server, _ := productionAuthServer(t)
	credentials := issueHTTPTestSession(t, server, auth.Principal{Issuer: "https://sub2api.example", Subject: "7", Platform: auth.PlatformSub2API, PlatformUserID: "7"})
	r := staffHTTPRequest("GET", "/test-user")
	r.AddCookie(server.productionAuth.Sessions.SessionCookie(credentials.Token, credentials.Session.AbsoluteExpiresAt))
	r.Header.Set("Authorization", "Bearer forged")
	w := httptest.NewRecorder()
	server.productionAuth.Require(server, "user", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("Bearer accepted with real cookie: %d", w.Code)
	}
}
