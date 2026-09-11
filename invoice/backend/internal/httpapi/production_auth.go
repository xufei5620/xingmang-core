package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"invoice-system/backend/internal/application"
	"invoice-system/backend/internal/auth"
	"invoice-system/backend/internal/postgresstore"
	"invoice-system/backend/staffauth"
)

const (
	defaultSessionCookieName = "__Host-invoice_session"
	csrfCookieName           = "__Host-invoice_csrf"
)

// SessionUser preserves existing invoice identities and their historical keys.
// Canonical identity fields never authorize a login by themselves.
type SessionUser struct {
	ID               string
	Claimed          bool
	Email            string
	EmailVerified    bool
	CanonicalIssuer  string
	CanonicalSubject string
}

type ProductionAuth struct {
	Sessions          *auth.SessionManager
	BindingHasher     *auth.ClientBindingHasher
	CSRF              auth.CSRFPolicy
	Admin             auth.AdminPolicy
	SessionCookieName string
	ProvisionUser     func(context.Context, auth.Principal, string) (SessionUser, error)
	LoadUser          func(context.Context, string) (SessionUser, error)
	// StaffResolver is an in-process callback. It must verify the current
	// platform session and permissions on every request, never browser claims.
	StaffResolver staffauth.Resolver
	SecurityAudit auth.SecurityAuditSink
}

func (a *ProductionAuth) Validate() error {
	if a == nil || a.Sessions == nil || a.BindingHasher == nil || a.ProvisionUser == nil || a.LoadUser == nil || a.StaffResolver == nil {
		return errors.New("production authentication runtime is incomplete")
	}
	if err := a.Admin.Validate(); err != nil {
		return err
	}
	if err := a.CSRF.Validate(); err != nil {
		return err
	}
	name := a.cookieName()
	if !strings.HasPrefix(name, "__Host-") || strings.ContainsAny(name, ";=, \t\r\n\x00") {
		return errors.New("production session cookie name is invalid")
	}
	return nil
}

func (a *ProductionAuth) Register(server *Server) {
	server.mux.Handle("GET /api/v1/auth/session", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.sessionStatus(server, w, r) }))
	server.mux.Handle("GET /api/v1/auth/staff-session", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.staffSessionStatus(server, w, r) }))
	server.mux.Handle("POST /api/v1/auth/logout", a.Require(server, "user", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.logout(w, r) })))
}

func (a *ProductionAuth) Require(server *Server, role string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var current *identity
		var err error
		if role == "admin" {
			current, err = a.authenticateStaff(server, r)
		} else {
			current, err = a.authenticate(server, r)
		}
		if err != nil {
			if errors.Is(err, auth.ErrAdminRoleRequired) {
				writeError(w, http.StatusForbidden, "ADMIN_FORBIDDEN", "administrator authorization is required")
			} else {
				writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
			}
			return
		}
		if role == "admin" {
			if err = a.Admin.AuthorizeSession(*current.Session, time.Now().UTC()); err != nil {
				code := "ADMIN_FORBIDDEN"
				if errors.Is(err, auth.ErrMFAStepUpRequired) {
					code = "ADMIN_STEP_UP_REQUIRED"
				}
				writeError(w, http.StatusForbidden, code, "administrator authorization is required")
				return
			}
			if !server.adminIPAllowed(r) {
				writeError(w, http.StatusForbidden, "ADMIN_NETWORK_DENIED", "administrator network is not allowed")
				return
			}
		}
		if err = a.CSRF.ValidateMutation(r, *current.Session); err != nil {
			writeError(w, http.StatusForbidden, "CSRF_REJECTED", "request origin or CSRF token is invalid")
			return
		}
		ctx := context.WithValue(r.Context(), identityKey, *current)
		ctx = application.WithAuditActor(ctx, postgresstore.AuditActor{Type: current.Role, ID: current.UserID, RequestID: requestID(r), SourceIPHMAC: current.Session.ClientIPHash, Reason: "authenticated HTTP operation"})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *ProductionAuth) authenticate(server *Server, r *http.Request) (*identity, error) {
	if len(r.Header.Values("Authorization")) != 0 {
		return nil, errors.New("browser routes do not accept bearer credentials")
	}
	cookie, err := r.Cookie(a.cookieName())
	if err != nil || cookie.Value == "" {
		return nil, auth.ErrSessionInvalid
	}
	binding, err := a.clientBinding(server, r)
	if err != nil {
		return nil, err
	}
	session, err := a.Sessions.Authenticate(r.Context(), cookie.Value, binding)
	if err != nil {
		return nil, err
	}
	// Provider and old administrator cookies cannot cross into the isolated
	// SUB/NEW user surface after retirement. Historical ledger keys stay intact.
	if !session.Platform.Valid() || strings.TrimSpace(session.PlatformUserID) == "" {
		return nil, auth.ErrSessionInvalid
	}
	return &identity{UserID: session.UserID, Role: "user", Session: &session, SessionToken: cookie.Value}, nil
}

func (a *ProductionAuth) authenticateStaff(server *Server, r *http.Request) (*identity, error) {
	if len(r.Header.Values("Authorization")) != 0 || a.StaffResolver == nil {
		return nil, auth.ErrSessionInvalid
	}
	staff, err := a.StaffResolver(r)
	if err != nil {
		return nil, auth.ErrSessionInvalid
	}
	origin, err := url.Parse(staff.Issuer)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || strings.ContainsAny(staff.Issuer, "*\r\n\t") || strings.TrimSpace(staff.Subject) == "" || len(staff.Subject) > 512 || strings.IndexFunc(staff.Subject, unicode.IsControl) >= 0 || len(staff.CSRFToken) < 43 || len(staff.CSRFToken) > 128 || strings.IndexFunc(staff.CSRFToken, unicode.IsControl) >= 0 {
		return nil, auth.ErrSessionInvalid
	}
	if !slices.Contains(staff.Roles, a.Admin.Role) {
		return nil, auth.ErrAdminRoleRequired
	}
	binding, err := a.clientBinding(server, r)
	if err != nil {
		return nil, err
	}
	p := auth.Principal{Issuer: staff.Issuer, Subject: staff.Subject, DisplayName: staff.DisplayName, Roles: append([]string(nil), staff.Roles...)}
	if staff.MFAAt != nil && !staff.MFAAt.IsZero() {
		p.ACR = "mfa"
		p.AMR = []string{"pwd", "otp"}
		p.AuthTime = staff.MFAAt.UTC()
	}
	user, err := a.ProvisionUser(r.Context(), p, requestID(r))
	if err != nil || user.ID == "" {
		return nil, auth.ErrSessionInvalid
	}
	if (user.CanonicalIssuer != "" && user.CanonicalIssuer != p.Issuer) || (user.CanonicalSubject != "" && user.CanonicalSubject != p.Subject) {
		return nil, auth.ErrIdentityMismatch
	}
	sum := sha256.Sum256([]byte(staff.CSRFToken))
	// This projection is request-local. No invoice administrator cookie or
	// session row is issued, so platform revocation takes effect immediately.
	session := auth.Session{UserID: user.ID, Issuer: p.Issuer, Subject: p.Subject, Roles: p.Roles, ACR: p.ACR, AMR: p.AMR, AuthTime: p.AuthTime, MFAAt: staff.MFAAt, CSRFHash: hex.EncodeToString(sum[:]), ClientIPHash: binding.IPHash, UserAgentHash: binding.UserAgentHash, DisplayName: staff.DisplayName}
	return &identity{UserID: user.ID, Role: "admin", Session: &session, CSRFToken: staff.CSRFToken}, nil
}

func (a *ProductionAuth) staffSessionStatus(server *Server, w http.ResponseWriter, r *http.Request) {
	current, err := a.authenticateStaff(server, r)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	if !server.adminIPAllowed(r) {
		writeError(w, http.StatusForbidden, "ADMIN_NETWORK_DENIED", "administrator network is not allowed")
		return
	}
	stepUp := a.Admin.AuthorizeSession(*current.Session, time.Now().UTC()) != nil
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrf_token": current.CSRFToken, "admin_step_up_required": stepUp, "user": map[string]any{"id": current.UserID, "display_name": current.Session.DisplayName, "email": "", "email_verified": false, "role": "admin", "platform": "", "platform_user_id": "", "username": current.Session.DisplayName}})
}

func (a *ProductionAuth) logout(w http.ResponseWriter, r *http.Request) {
	current := principal(r)
	if current.SessionToken == "" {
		writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if err := a.Sessions.RevokeToken(r.Context(), current.SessionToken, "user logout", requestID(r)); err != nil {
		writeError(w, http.StatusServiceUnavailable, "SESSION_REVOKE_FAILED", "logout could not revoke the local session")
		return
	}
	a.clearAuthCookies(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *ProductionAuth) cookieName() string {
	if strings.TrimSpace(a.SessionCookieName) == "" {
		return defaultSessionCookieName
	}
	return a.SessionCookieName
}

func (a *ProductionAuth) clientBinding(server *Server, r *http.Request) (auth.ClientBinding, error) {
	clientIP := server.requestClientIP(r)
	if clientIP == nil {
		return auth.ClientBinding{}, errors.New("trusted client IP is unavailable")
	}
	return a.BindingHasher.Hash(clientIP.String(), r.UserAgent())
}

func (a *ProductionAuth) sessionStatus(server *Server, w http.ResponseWriter, r *http.Request) {
	current, err := a.authenticate(server, r)
	if err != nil {
		a.clearAuthCookies(w)
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	csrf, ok := validCSRFCookie(r, *current.Session)
	if !ok {
		binding, bindingErr := a.clientBinding(server, r)
		principal := auth.Principal{
			Issuer: current.Session.Issuer, Subject: current.Session.Subject,
			Roles: current.Session.Roles, ACR: current.Session.ACR, AMR: current.Session.AMR,
			AuthTime: current.Session.AuthTime,
			Platform: current.Session.Platform, PlatformUserID: current.Session.PlatformUserID,
			// Carries the already-decrypted captured platform username forward
			// across this CSRF-token rotation -- Rotate() re-encrypts it onto the
			// new session row (migration 0017); this is a silent renewal of the
			// same login, not a fresh one, so it must not be lost here.
			DisplayName: current.Session.DisplayName,
		}
		credentials, rotateErr := a.Sessions.Rotate(r.Context(), auth.RotateSessionInput{
			Token: current.SessionToken, ExpectedSessionID: current.Session.ID,
			Principal: principal, Binding: binding, MFAAt: current.Session.MFAAt,
			RequestID: requestID(r),
		})
		if bindingErr != nil || rotateErr != nil {
			a.clearAuthCookies(w)
			writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
			return
		}
		current.Session = &credentials.Session
		current.SessionToken = credentials.Token
		csrf = credentials.CSRFToken
		a.setSessionCookies(w, credentials)
	}
	user, err := a.LoadUser(r.Context(), current.UserID)
	if err != nil || user.ID != current.UserID {
		_ = a.Sessions.RevokeToken(r.Context(), current.SessionToken, "user record unavailable", requestID(r))
		a.clearAuthCookies(w)
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
		return
	}
	// The captured platform username lives on the session row (encrypted,
	// migration 0017), not on the invoice_user record -- CR-0003 makes
	// identity strictly per-platform-login, and current.Session was already
	// decrypted by PostgresSessionStore when a.authenticate loaded it above.
	rawDisplayName := strings.TrimSpace(current.Session.DisplayName)
	displayName := rawDisplayName
	if displayName == "" {
		displayName = maskedEmailName(user.Email)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"csrf_token":    csrf,
		"user": map[string]any{
			"id": user.ID, "display_name": displayName, "email": user.Email,
			"email_verified": user.EmailVerified, "role": "user", "platform": current.Session.Platform,
			// Additive: exposes the already-computed Session.PlatformUserID (empty
			// for historical sessions) so an embedded, platform-scoped frontend view can
			// label a platform-password account that has neither a real display
			// name nor an email (e.g. a username-only New API account) instead of
			// showing every such account as the same generic fallback name.
			"platform_user_id": current.Session.PlatformUserID,
			// username is the raw captured platform account name -- unlike
			// display_name above it is never backfilled with maskedEmailName's
			// generic "用户" placeholder, so a caller can tell "we truly have a
			// name" apart from "there was nothing better to show" without
			// pattern-matching the fallback string. Sourced from the session
			// row (see current.Session.DisplayName above), not invoice_users --
			// empty only for a historical session, or a platform session issued
			// before migration 0017.
			"username": rawDisplayName,
		},
		"admin_step_up_required": false,
	})
}

func (a *ProductionAuth) setSessionCookies(w http.ResponseWriter, credentials auth.SessionCredentials) {
	http.SetCookie(w, a.Sessions.SessionCookie(credentials.Token, credentials.Session.AbsoluteExpiresAt))
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookieName, Value: credentials.CSRFToken, Path: "/",
		Expires: credentials.Session.AbsoluteExpiresAt, MaxAge: int(time.Until(credentials.Session.AbsoluteExpiresAt).Seconds()),
		Secure: true, HttpOnly: false, SameSite: http.SameSiteStrictMode,
	})
}

func (a *ProductionAuth) clearAuthCookies(w http.ResponseWriter) {
	http.SetCookie(w, a.Sessions.ClearSessionCookie())
	http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), Secure: true, SameSite: http.SameSiteStrictMode})
}

func validCSRFCookie(r *http.Request, session auth.Session) (string, bool) {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || len(cookie.Value) < 43 || len(cookie.Value) > 128 {
		return "", false
	}
	sum := sha256.Sum256([]byte(cookie.Value))
	want, err := hex.DecodeString(session.CSRFHash)
	if err != nil || len(want) != len(sum) || subtle.ConstantTimeCompare(sum[:], want) != 1 {
		return "", false
	}
	return cookie.Value, true
}

func maskedEmailName(email string) string {
	local, _, ok := strings.Cut(strings.TrimSpace(email), "@")
	if !ok || local == "" {
		return "用户"
	}
	runes := []rune(local)
	if len(runes) == 1 {
		return string(runes[0]) + "***"
	}
	return string(runes[0]) + "***" + string(runes[len(runes)-1])
}
