package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"invoice-system/backend/internal/auth"
	"invoice-system/backend/internal/domain"
)

// TestSessionPlatform covers the one piece of new XM-INV-PLATFORM-SCOPE logic
// that lives entirely inside the httpapi package (every other behavior is
// exercised by the PostgreSQL integration test in the application package):
// sessionPlatform is the single place every /api/v1/user/* handler reads the
// server-side scope from, so a bug here would silently unscope or
// misscope every one of them at once.
func TestSessionPlatform(t *testing.T) {
	withSession := func(session *auth.Session) *http.Request {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/user/funding-lots", nil)
		return request.WithContext(context.WithValue(request.Context(), identityKey, identity{
			UserID: "u1", Session: session,
		}))
	}

	t.Run("platform-password session reports its platform", func(t *testing.T) {
		for _, platform := range []auth.Platform{auth.PlatformSub2API, auth.PlatformNewAPI} {
			request := withSession(&auth.Session{Platform: platform})
			if got := sessionPlatform(request); got != domain.SourceType(platform) {
				t.Fatalf("platform=%s: sessionPlatform=%q", platform, got)
			}
		}
	})

	t.Run("OIDC/admin session has no platform", func(t *testing.T) {
		request := withSession(&auth.Session{Platform: ""})
		if got := sessionPlatform(request); got != "" {
			t.Fatalf("OIDC session should be unscoped, got %q", got)
		}
	})

	t.Run("mock-mode identity has no Session at all", func(t *testing.T) {
		// server.go's mock-mode require() branch never sets identity.Session
		// (see NewWithConfig: mock mode rejects ProductionAuth/PlatformLogin
		// entirely). sessionPlatform must not panic on that nil and must
		// report unscoped, matching AUTH_MODE=mock's existing behavior.
		request := withSession(nil)
		if got := sessionPlatform(request); got != "" {
			t.Fatalf("session-less identity should be unscoped, got %q", got)
		}
	})

	t.Run("unauthenticated request has no identity in context at all", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/user/funding-lots", nil)
		if got := sessionPlatform(request); got != "" {
			t.Fatalf("missing identity should be unscoped, got %q", got)
		}
	})
}
