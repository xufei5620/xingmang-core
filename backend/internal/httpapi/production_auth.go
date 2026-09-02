package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"

	"invoice-system/backend/internal/application"
	"invoice-system/backend/internal/auth"
	"invoice-system/backend/internal/postgresstore"
)

const (
	defaultSessionCookieName = "__Host-invoice_session"
	csrfCookieName           = "__Host-invoice_csrf"
	oidcFlowCookieName       = "__Host-invoice_oidc_flow"
)

type OIDCFlowClient interface {
	Begin(context.Context, auth.BeginAuthorizationInput) (auth.AuthorizationRequest, error)
	Callback(context.Context, auth.CallbackInput) (auth.CallbackResult, error)
}

type OIDCLogoutClient interface {
	RPInitiatedLogoutURL() (string, error)
	VerifyBackchannelLogout(context.Context, string) (auth.VerifiedBackchannelLogout, error)
}

type BackchannelLogoutProcessor interface {
	Process(context.Context, auth.VerifiedBackchannelLogout, auth.BackchannelLogoutActor) (auth.BackchannelLogoutResult, error)
}

type SessionUser struct {
	ID string
	// Claimed is true when provisioning landed on a pre-existing invoice_user
	// via the external-account claim path (see provisionPlatformOrOIDCUser in
	// cmd/api/runtime.go) instead of creating a fresh one. It is
	// request-scoped observability only -- never serialized to the session
	// response.
	Claimed       bool
	Email         string
	EmailVerified bool
	// CanonicalIssuer/CanonicalSubject carry the invoice_user's stored
	// oidc_issuer/oidc_subject when provisioning landed on a pre-existing
	// identity whose canonical pair differs from the login principal's
	// (platform-password login claiming a source-projected SSO identity).
	// The session INSERT guards on the stored pair
	// (WHERE u.oidc_issuer AND u.oidc_subject), so issuing with the
	// synthetic platform pair fails closed with ErrIdentityMismatch -- the
	// RC56 production canary hit exactly that. Empty means the login
	// principal's pair IS the canonical pair.
	CanonicalIssuer  string
	CanonicalSubject string
}

type ProductionAuth struct {
	OIDC              OIDCFlowClient
	Logout            OIDCLogoutClient
	BackchannelLogout BackchannelLogoutProcessor
	Sessions          *auth.SessionManager
	BindingHasher     *auth.ClientBindingHasher
	CSRF              auth.CSRFPolicy
	Admin             auth.AdminPolicy
	SessionCookieName string
	ProvisionUser     func(context.Context, auth.Principal, string) (SessionUser, error)
	LoadUser          func(context.Context, string) (SessionUser, error)
	// DisableOIDCAdminLogin gates whether the OIDC login/callback/admin-step-
	// up/backchannel-logout routes are registered at all (CR-0006 phase 1's
	// OIDC_ADMIN_LOGIN_ENABLED, inverted so the zero value -- false --
	// preserves today's behavior for every existing caller and test that
	// constructs a ProductionAuth without knowing this field exists). When
	// true, OIDC/Logout may be nil: Register skips wiring routes that would
	// otherwise call through them, and Validate does not require them.
	DisableOIDCAdminLogin bool
	// ConsoleAssertionEnabled gates the exchange handler itself (the route is
	// always registered so a flip from false->true never needs a fresh
	// binary; see consoleAssertionExchange's own disabled-check for the
	// CONSOLE_ASSERTION_DISABLED response), CR-0006's CONSOLE_ASSERTION_ENABLED.
	ConsoleAssertionEnabled bool
	ConsoleAssertionKeyring *auth.ConsoleAssertionKeyring
	ConsoleAssertionConfig  auth.ConsoleAssertionConfig
	ConsoleAssertionNonces  auth.ConsoleAssertionNonceStore
	// ConsoleAssertionRateLimiter bounds console-assertion exchange attempts
	// by client IP with the same failure-lockout primitive PlatformLogin
	// already uses (auth.LoginRateLimiter), so a malformed/forged-signature
	// flood gets a real ASSERTION_INVALID-shaped rejection with a distinct
	// RATE_LIMITED code once the caller floods too fast -- the edge's
	// nginx limit_req (zone=invoice_auth, 20r/m, matching the design spec's
	// cited rate exactly) already covers this route by prefix, but returns a
	// bare non-JSON response, not this endpoint's own typed error envelope.
	ConsoleAssertionRateLimiter *auth.LoginRateLimiter
	// SecurityAudit is a direct reference to the same sink Sessions already
	// writes through internally (see cmd/api/runtime.go's wiring) -- needed
	// here because the console-assertion exchange audits both a distinct
	// success event and every rejection reason (team lead's brief), neither
	// of which SessionManager's own private helper covers.
	SecurityAudit auth.SecurityAuditSink
}

func (a *ProductionAuth) Validate() error {
	if a == nil || a.BackchannelLogout == nil || a.Sessions == nil || a.BindingHasher == nil || a.ProvisionUser == nil || a.LoadUser == nil {
		return errors.New("production authentication runtime is incomplete")
	}
	if !a.DisableOIDCAdminLogin && (a.OIDC == nil || a.Logout == nil) {
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
	if a.ConsoleAssertionEnabled {
		if a.ConsoleAssertionKeyring == nil || a.ConsoleAssertionNonces == nil || a.ConsoleAssertionRateLimiter == nil || a.SecurityAudit == nil {
			return errors.New("console assertion runtime is incomplete")
		}
		if err := a.ConsoleAssertionConfig.Validate(); err != nil {
			return fmt.Errorf("console assertion config: %w", err)
		}
	}
	return nil
}

func (a *ProductionAuth) cookieName() string {
	if strings.TrimSpace(a.SessionCookieName) == "" {
		return defaultSessionCookieName
	}
	return a.SessionCookieName
}

func (a *ProductionAuth) Register(server *Server) {
	server.mux.Handle("GET /api/v1/auth/session", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.sessionStatus(server, w, r) }))
	// CR-0006 (XM-INV-CONSOLE-ASSERT): OIDC login/callback/admin-step-up/
	// backchannel-logout are only registered while OIDC_ADMIN_LOGIN_ENABLED
	// is true (the default, unchanged, in this phase). This is the "OIDC
	// path 是否注册" switch the change request describes for the eventual
	// Keycloak-retirement slice; flipping it off here today is exercised by
	// tests but not yet used in production, where it stays on.
	if !a.DisableOIDCAdminLogin {
		server.mux.Handle("GET /api/v1/auth/login", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.beginLogin(w, r) }))
		server.mux.Handle("GET /api/v1/auth/callback", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.callback(server, w, r) }))
		server.mux.Handle("GET /api/v1/auth/admin/step-up", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.beginAdminStepUp(server, w, r) }))
		server.mux.Handle("POST /api/v1/auth/backchannel-logout", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.backchannelLogout(server, w, r) }))
	}
	// The exchange route is always registered (see ConsoleAssertionEnabled's
	// doc comment): the handler itself answers CONSOLE_ASSERTION_DISABLED
	// when the feature is off, so flipping the flag on later never needs a
	// fresh binary/route table.
	server.mux.Handle("POST /api/v1/auth/console-assertion", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.consoleAssertionExchange(server, w, r) }))
	server.mux.Handle("POST /api/v1/auth/logout", a.Require(server, "user", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.logout(w, r) })))
}

func (a *ProductionAuth) Require(server *Server, role string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := a.authenticate(server, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
			return
		}
		if role == "admin" {
			if err = a.Admin.AuthorizeSession(*principal.Session, time.Now().UTC()); err != nil {
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
			principal.Role = "admin"
		}
		if err = a.CSRF.ValidateMutation(r, *principal.Session); err != nil {
			writeError(w, http.StatusForbidden, "CSRF_REJECTED", "request origin or CSRF token is invalid")
			return
		}
		ctx := context.WithValue(r.Context(), identityKey, *principal)
		actorType := "user"
		if principal.Role == "admin" {
			actorType = "admin"
		}
		ctx = application.WithAuditActor(ctx, postgresstore.AuditActor{
			Type: actorType, ID: principal.UserID, RequestID: requestID(r),
			SourceIPHMAC: principal.Session.ClientIPHash, Reason: "authenticated HTTP operation",
		})
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
	return &identity{UserID: session.UserID, Role: "user", Session: &session, SessionToken: cookie.Value}, nil
}

func (a *ProductionAuth) clientBinding(server *Server, r *http.Request) (auth.ClientBinding, error) {
	clientIP := server.requestClientIP(r)
	if clientIP == nil {
		return auth.ClientBinding{}, errors.New("trusted client IP is unavailable")
	}
	return a.BindingHasher.Hash(clientIP.String(), r.UserAgent())
}

func (a *ProductionAuth) beginLogin(w http.ResponseWriter, r *http.Request) {
	browserBinding := existingFlowBinding(r)
	request, err := a.OIDC.Begin(r.Context(), auth.BeginAuthorizationInput{
		Purpose: auth.FlowLogin, BrowserBinding: browserBinding,
		ReturnPath: returnPath(r),
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "OIDC_UNAVAILABLE", "login provider is unavailable")
		return
	}
	cookie, err := auth.OIDCFlowCookie(request.BrowserBinding, request.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OIDC_FLOW_FAILED", "login flow could not be created")
		return
	}
	http.SetCookie(w, cookie)
	http.Redirect(w, r, request.URL, http.StatusSeeOther)
}

func (a *ProductionAuth) beginAdminStepUp(server *Server, w http.ResponseWriter, r *http.Request) {
	current, err := a.authenticate(server, r)
	if err != nil || current.Session == nil || !slices.Contains(current.Session.Roles, a.Admin.Role) || !server.adminIPAllowed(r) {
		writeError(w, http.StatusForbidden, "ADMIN_FORBIDDEN", "administrator role and network are required")
		return
	}
	expected := auth.Principal{Issuer: current.Session.Issuer, Subject: current.Session.Subject}
	request, err := a.OIDC.Begin(r.Context(), auth.BeginAuthorizationInput{
		Purpose: auth.FlowAdminStepUp, BrowserBinding: existingFlowBinding(r),
		ExpectedIdentity: &expected, ExistingSessionID: current.Session.ID,
		ReturnPath: returnPath(r),
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "OIDC_UNAVAILABLE", "administrator verification is unavailable")
		return
	}
	cookie, err := auth.OIDCFlowCookie(request.BrowserBinding, request.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OIDC_FLOW_FAILED", "administrator verification could not be created")
		return
	}
	http.SetCookie(w, cookie)
	http.Redirect(w, r, request.URL, http.StatusSeeOther)
}

func (a *ProductionAuth) callback(server *Server, w http.ResponseWriter, r *http.Request) {
	flowCookie, err := r.Cookie(oidcFlowCookieName)
	if err != nil {
		http.SetCookie(w, auth.ClearOIDCFlowCookie())
		writeError(w, http.StatusBadRequest, "OIDC_CALLBACK_REJECTED", "login callback is invalid")
		return
	}
	result, err := a.OIDC.Callback(r.Context(), auth.CallbackInput{
		Code: r.URL.Query().Get("code"), State: r.URL.Query().Get("state"), BrowserBinding: flowCookie.Value,
	})
	if err != nil {
		http.SetCookie(w, auth.ClearOIDCFlowCookie())
		writeError(w, http.StatusBadRequest, "OIDC_CALLBACK_REJECTED", "login callback is invalid")
		return
	}
	binding, err := a.clientBinding(server, r)
	if err != nil {
		http.SetCookie(w, auth.ClearOIDCFlowCookie())
		writeError(w, http.StatusBadRequest, "CLIENT_BINDING_FAILED", "client binding is unavailable")
		return
	}
	var credentials auth.SessionCredentials
	callbackRequestID := requestID(r)
	switch result.Purpose {
	case auth.FlowLogin:
		user, provisionErr := a.ProvisionUser(r.Context(), result.Principal, callbackRequestID)
		if provisionErr != nil || user.ID == "" {
			http.SetCookie(w, auth.ClearOIDCFlowCookie())
			writeError(w, http.StatusForbidden, "USER_PROVISION_FAILED", "user account is unavailable")
			return
		}
		var mfaAt *time.Time
		if a.Admin.VerifyFreshPrincipal(result.Principal, time.Now().UTC(), time.Time{}) == nil {
			value := result.Principal.AuthTime.UTC()
			mfaAt = &value
		}
		credentials, err = a.Sessions.Issue(r.Context(), auth.IssueSessionInput{
			UserID: user.ID, Principal: result.Principal, Binding: binding,
			MFAAt: mfaAt, RequestID: callbackRequestID,
		})
	case auth.FlowAdminStepUp:
		currentCookie, cookieErr := r.Cookie(a.cookieName())
		if cookieErr != nil {
			err = auth.ErrSessionInvalid
			break
		}
		current, authErr := a.Sessions.Authenticate(r.Context(), currentCookie.Value, binding)
		if authErr != nil || current.ID != result.ExistingSessionID {
			err = auth.ErrSessionInvalid
			break
		}
		mfaAt := result.Principal.AuthTime.UTC()
		credentials, err = a.Sessions.Rotate(r.Context(), auth.RotateSessionInput{
			Token: currentCookie.Value, ExpectedSessionID: current.ID,
			Principal: result.Principal, Binding: binding, MFAAt: &mfaAt,
			RequestID: callbackRequestID,
		})
	default:
		err = errors.New("unsupported callback purpose")
	}
	if err != nil {
		http.SetCookie(w, auth.ClearOIDCFlowCookie())
		writeError(w, http.StatusForbidden, "SESSION_ISSUE_FAILED", "login session could not be established")
		return
	}
	http.SetCookie(w, auth.ClearOIDCFlowCookie())
	a.setSessionCookies(w, credentials)
	http.Redirect(w, r, result.ReturnPath, http.StatusSeeOther)
}

// consoleAssertionExchange is CR-0006's console-assertion login path: an
// unauthenticated (no pre-existing session/CSRF token possible), same-shape
// sibling of callback() above, reached from the embedded admin iframe's
// postMessage handshake or a future top-level entry instead of the OIDC
// redirect dance. See docs/superpowers/specs/2026-09-02-cr0006-console-
// assertion-design.md section 5.2 for the frozen error-code table this
// implements exactly, and console_assertion.go's package doc for the claims
// contract. Every branch below records a security-audit event before
// responding -- both the one success event and every rejection reason
// (team lead's brief) -- because, unlike the OIDC callback, this endpoint
// has no upstream identity provider of its own keeping a parallel log.
func (a *ProductionAuth) consoleAssertionExchange(server *Server, w http.ResponseWriter, r *http.Request) {
	requestIDValue := requestID(r)
	if !a.ConsoleAssertionEnabled {
		writeError(w, http.StatusServiceUnavailable, "CONSOLE_ASSERTION_DISABLED", "console-assertion login is not enabled")
		return
	}
	binding, err := a.clientBinding(server, r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "INTERNAL", "client identity is unavailable")
		return
	}
	rateLimitKey := binding.IPHash
	if rateLimitKey == "" || !a.ConsoleAssertionRateLimiter.Allow(rateLimitKey) {
		a.auditConsoleAssertionRejection(r.Context(), "", requestIDValue, "rate_limited")
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "too many console-assertion attempts; try again later")
		return
	}

	origins := r.Header.Values("Origin")
	if len(origins) != 1 || origins[0] != a.ConsoleAssertionConfig.Issuer {
		a.ConsoleAssertionRateLimiter.RecordFailure(rateLimitKey)
		a.auditConsoleAssertionRejection(r.Context(), "", requestIDValue, "origin_rejected")
		writeError(w, http.StatusForbidden, "ORIGIN_REJECTED", "request origin is not the configured console origin")
		return
	}

	var body struct {
		Assertion string `json:"assertion"`
	}
	if !decodeJSON(w, r, &body) {
		a.ConsoleAssertionRateLimiter.RecordFailure(rateLimitKey)
		a.auditConsoleAssertionRejection(r.Context(), "", requestIDValue, "malformed")
		return
	}
	if strings.TrimSpace(body.Assertion) == "" {
		a.ConsoleAssertionRateLimiter.RecordFailure(rateLimitKey)
		a.auditConsoleAssertionRejection(r.Context(), "", requestIDValue, "malformed")
		writeError(w, http.StatusBadRequest, "ASSERTION_MALFORMED", "assertion is required")
		return
	}

	claims, err := auth.VerifyConsoleAssertion(body.Assertion, a.ConsoleAssertionKeyring, a.ConsoleAssertionConfig, a.Admin.Role, time.Now().UTC())
	if err != nil {
		a.ConsoleAssertionRateLimiter.RecordFailure(rateLimitKey)
		a.auditConsoleAssertionRejection(r.Context(), consoleAssertionAttemptHash(body.Assertion), requestIDValue, "assertion_invalid: "+err.Error())
		writeError(w, http.StatusUnauthorized, "ASSERTION_INVALID", "console assertion is invalid")
		return
	}

	consumed, err := a.ConsoleAssertionNonces.ConsumeNonce(r.Context(), consoleAssertionNonceHash(claims.Nonce), claims.ExpiresAt)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "INTERNAL", "console assertion could not be verified")
		return
	}
	if !consumed {
		a.ConsoleAssertionRateLimiter.RecordFailure(rateLimitKey)
		a.auditConsoleAssertionRejection(r.Context(), claims.Subject, requestIDValue, "replayed")
		writeError(w, http.StatusUnauthorized, "ASSERTION_INVALID", "console assertion is invalid")
		return
	}

	principal := auth.ConsolePrincipalFromClaims(claims, a.ConsoleAssertionConfig.Issuer, a.Admin.RequiredACR)
	user, err := a.ProvisionUser(r.Context(), principal, requestIDValue)
	if err != nil || user.ID == "" {
		a.auditConsoleAssertionRejection(r.Context(), claims.Subject, requestIDValue, "user_provision_failed")
		writeError(w, http.StatusForbidden, "USER_PROVISION_FAILED", "user account is unavailable")
		return
	}
	// Mirrors callback()'s FlowLogin branch exactly: VerifyFreshPrincipal
	// re-derives mfa_at from the SAME roles/acr/amr/AuthTime fields
	// AuthorizeSession later re-checks, so a console assertion satisfies
	// admin step-up precisely because its amr already proves "otp" and its
	// iat (copied onto AuthTime) is recent -- no bespoke step-up logic
	// needed here at all (CR-0006 change list item 4).
	var mfaAt *time.Time
	if a.Admin.VerifyFreshPrincipal(principal, time.Now().UTC(), time.Time{}) == nil {
		value := principal.AuthTime.UTC()
		mfaAt = &value
	}
	credentials, err := a.Sessions.Issue(r.Context(), auth.IssueSessionInput{
		UserID: user.ID, Principal: principal, Binding: binding, MFAAt: mfaAt, RequestID: requestIDValue,
	})
	if err != nil {
		a.auditConsoleAssertionRejection(r.Context(), claims.Subject, requestIDValue, "session_issue_failed")
		writeError(w, http.StatusForbidden, "SESSION_ISSUE_FAILED", "login session could not be established")
		return
	}
	a.ConsoleAssertionRateLimiter.Reset(rateLimitKey)
	a.setSessionCookies(w, credentials)
	_ = a.SecurityAudit.RecordSecurityEvent(r.Context(), auth.SecurityAuditEvent{
		ActorType: "console_assertion", ActorID: claims.Subject,
		Action: "auth.console_assertion.exchanged", ObjectType: "auth_session", ObjectID: credentials.Session.ID,
		RequestID: requestIDValue, Severity: auth.SeverityNotice,
		Reason: fmt.Sprintf("kid=%s scope=%s nonce_hash=%s", safeAuditToken(claims.KeyID), safeAuditToken(claims.Scope), consoleAssertionNonceHash(claims.Nonce)[:16]),
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// auditConsoleAssertionRejection records every rejection reason server-side
// (team lead's brief), independent of the single generic ASSERTION_INVALID
// response every case above sends the caller -- see console_assertion.go's
// ErrConsoleAssertionInvalid doc comment for why the two must not carry the
// same detail. actorID is best-effort: the raw assertion's own sha256 (via
// consoleAssertionAttemptHash) before signature verification succeeds (claims
// are not yet trustworthy), or the verified claims.Subject after.
func (a *ProductionAuth) auditConsoleAssertionRejection(ctx context.Context, actorID, requestIDValue, reason string) {
	if a.SecurityAudit == nil {
		return
	}
	if actorID == "" {
		actorID = "unknown"
	}
	_ = a.SecurityAudit.RecordSecurityEvent(ctx, auth.SecurityAuditEvent{
		ActorType: "console_assertion", ActorID: actorID,
		Action: "auth.console_assertion.rejected", ObjectType: "console_assertion_attempt", ObjectID: requestIDValue,
		RequestID: requestIDValue, Reason: safeAuditToken(reason), Severity: auth.SeverityWarning,
	})
}

func consoleAssertionNonceHash(nonce string) string {
	sum := sha256.Sum256([]byte(nonce))
	return hex.EncodeToString(sum[:])
}

func consoleAssertionAttemptHash(assertion string) string {
	sum := sha256.Sum256([]byte(assertion))
	return hex.EncodeToString(sum[:])
}

// safeAuditToken bounds a value that will be embedded in an audit Reason
// field (SecurityAuditEvent.Validate caps it at 500 bytes and rejects
// control characters) -- verifier error strings are all package-internal
// constants today, but this keeps the call sites safe even if that changes.
func safeAuditToken(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return ' '
		}
		return r
	}, value)
	if len(value) > 200 {
		value = value[:200]
	}
	return value
}

func (a *ProductionAuth) sessionStatus(server *Server, w http.ResponseWriter, r *http.Request) {
	current, err := a.authenticate(server, r)
	if err != nil {
		a.clearAuthCookies(w)
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false, "oidc_admin_login_enabled": !a.DisableOIDCAdminLogin})
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
			writeJSON(w, http.StatusOK, map[string]any{"authenticated": false, "oidc_admin_login_enabled": !a.DisableOIDCAdminLogin})
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
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false, "oidc_admin_login_enabled": !a.DisableOIDCAdminLogin})
		return
	}
	role := "user"
	stepUpRequired := false
	if slices.Contains(current.Session.Roles, a.Admin.Role) {
		if a.Admin.AuthorizeSession(*current.Session, time.Now().UTC()) == nil && server.adminIPAllowed(r) {
			role = "admin"
		} else {
			stepUpRequired = true
		}
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
		"authenticated":            true,
		"csrf_token":               csrf,
		"oidc_admin_login_enabled": !a.DisableOIDCAdminLogin,
		"user": map[string]any{
			"id": user.ID, "display_name": displayName, "email": user.Email,
			"email_verified": user.EmailVerified, "role": role, "platform": current.Session.Platform,
			// Additive: exposes the already-computed Session.PlatformUserID (empty
			// for OIDC sessions) so an embedded, platform-scoped frontend view can
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
			// empty only for an OIDC session, or a platform session issued
			// before migration 0017.
			"username": rawDisplayName,
		},
		"admin_step_up_required": stepUpRequired,
	})
}

func (a *ProductionAuth) logout(w http.ResponseWriter, r *http.Request) {
	current := principal(r)
	if current.SessionToken == "" {
		writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	// A platform-password session (current.Session.Platform != "") never went
	// through the OIDC provider, so there is no RP-initiated logout URL to
	// send the browser to -- only the local session is revoked. A
	// console-assertion session (CR-0006: Issuer == the configured console
	// origin) is the same story -- it never touched Keycloak either, despite
	// also having Platform=="" like an OIDC session (both use the identical
	// ResolveOrCreate path, see identity.go). And once OIDC admin login is
	// disabled outright (DisableOIDCAdminLogin), a.Logout was never built at
	// all, so there is no RP-initiated URL to compute for any session
	// regardless of its origin.
	consoleAssertionSession := current.Session != nil && a.ConsoleAssertionConfig.Issuer != "" && current.Session.Issuer == a.ConsoleAssertionConfig.Issuer
	logoutURL := ""
	if !a.DisableOIDCAdminLogin && !consoleAssertionSession && (current.Session == nil || current.Session.Platform == "") {
		var err error
		logoutURL, err = a.Logout.RPInitiatedLogoutURL()
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "OIDC_LOGOUT_UNAVAILABLE", "identity-provider logout is unavailable")
			return
		}
	}
	if err := a.Sessions.RevokeToken(r.Context(), current.SessionToken, "user logout", requestID(r)); err != nil {
		writeError(w, http.StatusServiceUnavailable, "SESSION_REVOKE_FAILED", "logout could not revoke the local session")
		return
	}
	a.clearAuthCookies(w)
	response := map[string]any{"ok": true}
	if logoutURL != "" {
		response["logout_url"] = logoutURL
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *ProductionAuth) backchannelLogout(server *Server, w http.ResponseWriter, r *http.Request) {
	const maximumBody = int64(256 << 10)
	if r.URL.RawQuery != "" || len(r.Header.Values("Authorization")) != 0 || len(r.Header.Values("Cookie")) != 0 || r.Header.Get("Content-Encoding") != "" {
		writeError(w, http.StatusBadRequest, "BACKCHANNEL_LOGOUT_REJECTED", "back-channel logout request is invalid")
		return
	}
	contentTypes := r.Header.Values("Content-Type")
	if len(contentTypes) != 1 {
		writeError(w, http.StatusUnsupportedMediaType, "BACKCHANNEL_CONTENT_TYPE", "application/x-www-form-urlencoded is required")
		return
	}
	mediaType, parameters, err := mime.ParseMediaType(contentTypes[0])
	if err != nil || mediaType != "application/x-www-form-urlencoded" || len(parameters) != 0 {
		writeError(w, http.StatusUnsupportedMediaType, "BACKCHANNEL_CONTENT_TYPE", "application/x-www-form-urlencoded is required")
		return
	}
	if r.ContentLength > maximumBody {
		writeError(w, http.StatusRequestEntityTooLarge, "BACKCHANNEL_BODY_TOO_LARGE", "back-channel logout request is too large")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maximumBody)
	defer r.Body.Close()
	if err = r.ParseForm(); err != nil || len(r.PostForm) != 1 || len(r.PostForm["logout_token"]) != 1 {
		writeError(w, http.StatusBadRequest, "BACKCHANNEL_LOGOUT_REJECTED", "back-channel logout request is invalid")
		return
	}
	verified, err := a.Logout.VerifyBackchannelLogout(r.Context(), r.PostForm["logout_token"][0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "BACKCHANNEL_LOGOUT_REJECTED", "back-channel logout token is invalid")
		return
	}
	clientIP := server.requestClientIP(r)
	if clientIP == nil {
		writeError(w, http.StatusServiceUnavailable, "BACKCHANNEL_CLIENT_IP", "back-channel client identity is unavailable")
		return
	}
	binding, err := a.BindingHasher.Hash(clientIP.String(), "oidc-backchannel-logout")
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "BACKCHANNEL_CLIENT_IP", "back-channel client identity is unavailable")
		return
	}
	if _, err = a.BackchannelLogout.Process(r.Context(), verified, auth.BackchannelLogoutActor{RequestID: requestID(r), SourceIPHash: binding.IPHash}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "BACKCHANNEL_LOGOUT_FAILED", "back-channel logout could not be persisted")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
	http.SetCookie(w, auth.ClearOIDCFlowCookie())
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

func existingFlowBinding(r *http.Request) string {
	cookie, err := r.Cookie(oidcFlowCookieName)
	if err == nil && len(cookie.Value) >= 43 && len(cookie.Value) <= 128 {
		return cookie.Value
	}
	value := make([]byte, 32)
	if _, err = rand.Read(value); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

func returnPath(r *http.Request) string {
	value := strings.TrimSpace(r.URL.Query().Get("return_to"))
	if value == "" {
		return "/"
	}
	return value
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
