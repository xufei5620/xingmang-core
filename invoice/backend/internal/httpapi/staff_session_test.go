package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"invoice-system/backend/internal/auth"
	"invoice-system/backend/staffauth"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func staffHTTPRequest(method, path string) *http.Request {
	r := httptest.NewRequest(method, "https://invoice.example"+path, nil)
	r.RemoteAddr = "127.0.0.1:443"
	r.Header.Set("User-Agent", "test-browser")
	return r
}
func validStaffIdentity() staffauth.Identity {
	now := time.Now().UTC()
	return staffauth.Identity{Issuer: "https://console.example", Subject: "20000000-0000-4000-8000-000000000001", DisplayName: "Finance operator", Roles: []string{"invoice-admin"}, MFAAt: &now, CSRFToken: strings.Repeat("s", 43)}
}
func TestStaffSessionRevalidatesCurrentPlatformAuthorityAndPreservesUserCookies(t *testing.T) {
	server, _ := productionAuthServer(t)
	staff := validStaffIdentity()
	live := true
	calls := 0
	server.productionAuth.StaffResolver = func(*http.Request) (staffauth.Identity, error) {
		calls++
		if !live {
			return staffauth.Identity{}, errors.New("revoked")
		}
		return staff, nil
	}
	var projected auth.Principal
	server.productionAuth.ProvisionUser = func(_ context.Context, p auth.Principal, _ string) (SessionUser, error) {
		projected = p
		return SessionUser{ID: "30000000-0000-4000-8000-000000000001", CanonicalIssuer: p.Issuer, CanonicalSubject: p.Subject}, nil
	}
	credentials := issueHTTPTestSession(t, server, auth.Principal{Issuer: "https://sub2api.example", Subject: "7", Platform: auth.PlatformSub2API, PlatformUserID: "7"})
	r := staffHTTPRequest("GET", "/api/v1/auth/staff-session")
	r.AddCookie(server.productionAuth.Sessions.SessionCookie(credentials.Token, credentials.Session.AbsoluteExpiresAt))
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	var body struct {
		Authenticated bool                      `json:"authenticated"`
		CSRF          string                    `json:"csrf_token"`
		StepUp        bool                      `json:"admin_step_up_required"`
		User          struct{ ID, Role string } `json:"user"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || !body.Authenticated || body.CSRF != staff.CSRFToken || body.StepUp || body.User.Role != "admin" || len(w.Result().Cookies()) != 0 {
		t.Fatalf("staff bootstrap=%d %s cookies=%v", w.Code, w.Body.String(), w.Result().Cookies())
	}
	if projected.Issuer != staff.Issuer || projected.Subject != staff.Subject || projected.Platform != "" || projected.ACR != "mfa" || len(projected.AMR) != 2 {
		t.Fatalf("trusted identity changed: %+v", projected)
	}
	next := server.productionAuth.Require(server, "admin", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if principal(r).UserID != body.User.ID || principal(r).Role != "admin" {
			t.Error("wrong audit identity")
		}
		w.WriteHeader(204)
	}))
	good := httptest.NewRecorder()
	next.ServeHTTP(good, staffHTTPRequest("GET", "/test-admin"))
	if good.Code != 204 {
		t.Fatal(good.Body.String())
	}
	live = false
	revoked := httptest.NewRecorder()
	next.ServeHTTP(revoked, staffHTTPRequest("GET", "/test-admin"))
	if revoked.Code != 401 || calls != 3 {
		t.Fatalf("revocation ignored status=%d calls=%d", revoked.Code, calls)
	}
	// The staff bootstrap neither overwrites nor revokes the independent user token.
	binding, err := server.productionAuth.clientBinding(server, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = server.productionAuth.Sessions.Authenticate(context.Background(), credentials.Token, binding); err != nil {
		t.Fatalf("staff bootstrap changed user session: %v", err)
	}
}
func TestStaffSessionDoesNotAuthorizeUserSurface(t *testing.T) {
	server, _ := productionAuthServer(t)
	calls := 0
	server.productionAuth.StaffResolver = func(*http.Request) (staffauth.Identity, error) { calls++; return validStaffIdentity(), nil }
	r := staffHTTPRequest("GET", "/api/v1/user/funding-lots")
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != 401 || calls != 0 {
		t.Fatalf("staff crossed into user surface status=%d resolvercalls=%d", w.Code, calls)
	}
}
func TestStaffAdministratorPreservesRoleMFAIPAndCSRFGates(t *testing.T) {
	cases := []struct {
		name     string
		identity func(*staffauth.Identity)
		request  func(*http.Request)
		status   int
		code     string
	}{
		{name: "valid", status: 204},
		{name: "missing role", identity: func(s *staffauth.Identity) { s.Roles = []string{"user"} }, status: 403, code: "ADMIN_FORBIDDEN"},
		{name: "missing MFA", identity: func(s *staffauth.Identity) { s.MFAAt = nil }, status: 403, code: "ADMIN_STEP_UP_REQUIRED"},
		{name: "stale MFA", identity: func(s *staffauth.Identity) { at := time.Now().Add(-11 * time.Minute); s.MFAAt = &at }, status: 403, code: "ADMIN_STEP_UP_REQUIRED"},
		{name: "future MFA", identity: func(s *staffauth.Identity) { at := time.Now().Add(3 * time.Minute); s.MFAAt = &at }, status: 403, code: "ADMIN_STEP_UP_REQUIRED"},
		{name: "denied IP", request: func(r *http.Request) { r.RemoteAddr = "192.0.2.42:443" }, status: 403, code: "ADMIN_NETWORK_DENIED"},
		{name: "missing CSRF", request: func(r *http.Request) { r.Header.Del("X-CSRF-Token") }, status: 403, code: "CSRF_REJECTED"},
		{name: "wrong CSRF", request: func(r *http.Request) { r.Header.Set("X-CSRF-Token", strings.Repeat("z", 43)) }, status: 403, code: "CSRF_REJECTED"},
		{name: "duplicate CSRF", request: func(r *http.Request) { r.Header.Add("X-CSRF-Token", strings.Repeat("s", 43)) }, status: 403, code: "CSRF_REJECTED"},
		{name: "foreign origin", request: func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, status: 403, code: "CSRF_REJECTED"},
		{name: "duplicate origin", request: func(r *http.Request) { r.Header.Add("Origin", "https://invoice.example") }, status: 403, code: "CSRF_REJECTED"},
		{name: "bearer", request: func(r *http.Request) { r.Header.Set("Authorization", "Bearer forged") }, status: 401, code: "AUTH_REQUIRED"},
		{name: "unsafe issuer", identity: func(s *staffauth.Identity) { s.Issuer = "https://console.example/path" }, status: 401, code: "AUTH_REQUIRED"},
		{name: "empty CSRF proof", identity: func(s *staffauth.Identity) { s.CSRFToken = "" }, status: 401, code: "AUTH_REQUIRED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := productionAuthServer(t)
			staff := validStaffIdentity()
			if tc.identity != nil {
				tc.identity(&staff)
			}
			server.productionAuth.StaffResolver = func(*http.Request) (staffauth.Identity, error) { return staff, nil }
			r := staffHTTPRequest("POST", "/test-admin")
			r.Header.Set("Origin", "https://invoice.example")
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("X-CSRF-Token", strings.Repeat("s", 43))
			if tc.request != nil {
				tc.request(r)
			}
			w := httptest.NewRecorder()
			server.productionAuth.Require(server, "admin", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
				t.Fatalf("got %d %s want %d %s", w.Code, w.Body.String(), tc.status, tc.code)
			}
		})
	}
}
func TestStaffBootstrapRequiresStepUpAndCannotWriteCookies(t *testing.T) {
	server, _ := productionAuthServer(t)
	staff := validStaffIdentity()
	staff.MFAAt = nil
	server.productionAuth.StaffResolver = func(*http.Request) (staffauth.Identity, error) { return staff, nil }
	r := staffHTTPRequest("GET", "/api/v1/auth/staff-session")
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"admin_step_up_required":true`) || len(w.Result().Cookies()) != 0 {
		t.Fatalf("bootstrap stepup=%d %s", w.Code, w.Body.String())
	}
}
func TestProductionRuntimeRequiresStaffResolver(t *testing.T) {
	server, _ := productionAuthServer(t)
	server.productionAuth.StaffResolver = nil
	if err := server.productionAuth.Validate(); err == nil {
		t.Fatal("missing trusted staff resolver accepted")
	}
}

func TestStaffCallbackCannotBeReplacedByPlatformUserCookie(t *testing.T) {
	server, _ := productionAuthServer(t)
	now := time.Now().UTC()
	binding, err := server.productionAuth.clientBinding(server, staffHTTPRequest("GET", "/test-admin"))
	if err != nil {
		t.Fatal(err)
	}
	creds, err := server.productionAuth.Sessions.Issue(context.Background(), auth.IssueSessionInput{UserID: "10000000-0000-4000-8000-000000000001", Principal: auth.Principal{Issuer: "https://sub2api.example", Subject: "7", Platform: auth.PlatformSub2API, PlatformUserID: "7", Roles: []string{"invoice-admin"}, ACR: "mfa", AMR: []string{"pwd", "otp"}, AuthTime: now}, MFAAt: &now, Binding: binding, RequestID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	r := staffHTTPRequest("GET", "/test-admin")
	r.AddCookie(server.productionAuth.Sessions.SessionCookie(creds.Token, creds.Session.AbsoluteExpiresAt))
	w := httptest.NewRecorder()
	server.productionAuth.Require(server, "admin", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("user cookie substituted for trusted staff callback: %d %s", w.Code, w.Body.String())
	}
}

func TestStaffIdentityCannotClaimDifferentHistoricalTuple(t *testing.T) {
	for _, field := range []string{"issuer", "subject"} {
		t.Run(field, func(t *testing.T) {
			server, _ := productionAuthServer(t)
			server.productionAuth.StaffResolver = func(*http.Request) (staffauth.Identity, error) { return validStaffIdentity(), nil }
			server.productionAuth.ProvisionUser = func(_ context.Context, p auth.Principal, _ string) (SessionUser, error) {
				user := SessionUser{ID: "30000000-0000-4000-8000-000000000001", CanonicalIssuer: p.Issuer, CanonicalSubject: p.Subject}
				if field == "issuer" {
					user.CanonicalIssuer = "https://other.example"
				} else {
					user.CanonicalSubject = "other-staff"
				}
				return user, nil
			}
			w := httptest.NewRecorder()
			server.productionAuth.Require(server, "admin", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, staffHTTPRequest("GET", "/test-admin"))
			if w.Code != 401 {
				t.Fatalf("different historical identity authorized: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
