package httpapi

import (
	"context"
	"invoice-system/backend/internal/auth"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRetiredOIDCRoutesNeverRegister(t *testing.T) {
	server, _ := productionAuthServer(t)
	for _, c := range []struct{ method, path string }{{"GET", "/api/v1/auth/login"}, {"GET", "/api/v1/auth/callback"}, {"GET", "/api/v1/auth/admin/step-up"}, {"POST", "/api/v1/auth/backchannel-logout"}, {"POST", "/api/v1/auth/console-assertion"}} {
		t.Run(c.path, func(t *testing.T) {
			r := httptest.NewRequest(c.method, "https://invoice.example"+c.path, nil)
			r.RemoteAddr = "127.0.0.1:443"
			r.Header.Set("User-Agent", "test-browser")
			w := httptest.NewRecorder()
			server.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusNotFound || w.Header().Get("Location") != "" {
				t.Fatalf("retired route still active: %s %d location=%q", c.path, w.Code, w.Header().Get("Location"))
			}
		})
	}
}

func TestLegacyCookieCannotAuthorizeAdministrator(t *testing.T) {
	server, _ := productionAuthServer(t)
	r := httptest.NewRequest("GET", "https://invoice.example/test-admin", nil)
	r.RemoteAddr = "127.0.0.1:443"
	r.Header.Set("User-Agent", "test-browser")
	binding, err := server.productionAuth.clientBinding(server, r)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	creds, err := server.productionAuth.Sessions.Issue(context.Background(), auth.IssueSessionInput{UserID: "10000000-0000-4000-8000-000000000001", Principal: auth.Principal{Issuer: "https://identity.example", Subject: "admin-old", Roles: []string{"invoice-admin"}, ACR: "mfa", AMR: []string{"pwd", "otp"}, AuthTime: now}, MFAAt: &now, Binding: binding, RequestID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	r.AddCookie(server.productionAuth.Sessions.SessionCookie(creds.Token, creds.Session.AbsoluteExpiresAt))
	w := httptest.NewRecorder()
	server.productionAuth.Require(server, "admin", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("legacy administrator cookie authorized: status=%d body=%s", w.Code, w.Body.String())
	}
	user := httptest.NewRequest("GET", "https://invoice.example/api/v1/auth/session", nil)
	user.RemoteAddr = r.RemoteAddr
	user.Header.Set("User-Agent", "test-browser")
	user.AddCookie(server.productionAuth.Sessions.SessionCookie(creds.Token, creds.Session.AbsoluteExpiresAt))
	result := httptest.NewRecorder()
	server.Handler().ServeHTTP(result, user)
	if !strings.Contains(result.Body.String(), `"authenticated":false`) {
		t.Fatalf("legacy non-platform user session still accepted: %s", result.Body.String())
	}
}
