package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
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
	ID            string
	DisplayName   string
	Email         string
	EmailVerified bool
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
}

func (a *ProductionAuth) Validate() error {
	if a == nil || a.OIDC == nil || a.Logout == nil || a.BackchannelLogout == nil || a.Sessions == nil || a.BindingHasher == nil || a.ProvisionUser == nil || a.LoadUser == nil {
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

func (a *ProductionAuth) cookieName() string {
	if strings.TrimSpace(a.SessionCookieName) == "" {
		return defaultSessionCookieName
	}
	return a.SessionCookieName
}

func (a *ProductionAuth) Register(server *Server) {
	server.mux.Handle("GET /api/v1/auth/session", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.sessionStatus(server, w, r) }))
	server.mux.Handle("GET /api/v1/auth/login", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.beginLogin(w, r) }))
	server.mux.Handle("GET /api/v1/auth/callback", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.callback(server, w, r) }))
	server.mux.Handle("GET /api/v1/auth/admin/step-up", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.beginAdminStepUp(server, w, r) }))
	server.mux.Handle("POST /api/v1/auth/backchannel-logout", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.backchannelLogout(server, w, r) }))
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
	role := "user"
	stepUpRequired := false
	if slices.Contains(current.Session.Roles, a.Admin.Role) {
		if a.Admin.AuthorizeSession(*current.Session, time.Now().UTC()) == nil && server.adminIPAllowed(r) {
			role = "admin"
		} else {
			stepUpRequired = true
		}
	}
	displayName := strings.TrimSpace(user.DisplayName)
	if displayName == "" {
		displayName = maskedEmailName(user.Email)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"csrf_token":    csrf,
		"user": map[string]any{
			"id": user.ID, "display_name": displayName, "email": user.Email,
			"email_verified": user.EmailVerified, "role": role,
		},
		"admin_step_up_required": stepUpRequired,
	})
}

func (a *ProductionAuth) logout(w http.ResponseWriter, r *http.Request) {
	current := principal(r)
	logoutURL, err := a.Logout.RPInitiatedLogoutURL()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "OIDC_LOGOUT_UNAVAILABLE", "identity-provider logout is unavailable")
		return
	}
	if current.SessionToken == "" {
		writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication is required")
		return
	}
	if err = a.Sessions.RevokeToken(r.Context(), current.SessionToken, "user logout", requestID(r)); err != nil {
		writeError(w, http.StatusServiceUnavailable, "SESSION_REVOKE_FAILED", "logout could not revoke the local session")
		return
	}
	a.clearAuthCookies(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "logout_url": logoutURL})
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
