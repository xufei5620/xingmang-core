package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"invoice-system/backend/internal/auth"
)

// PlatformLogin is the parallel, JSON-based login surface for ordinary users
// authenticating with their existing Sub2API or New API account password
// (CR-0004). It shares session issuance, CSRF cookies and identity resolution
// with ProductionAuth (the OIDC/administrator login surface, which this does
// not replace or remove) by calling straight through to it.
type PlatformLogin struct {
	Authenticators map[auth.Platform]auth.PlatformAuthenticator
	Origins        map[auth.Platform]string // exact configured login origin per platform; doubles as Principal.Issuer
	RateLimiter    *auth.LoginRateLimiter
	Auth           *ProductionAuth
}

func (p *PlatformLogin) Validate() error {
	if p == nil || p.Auth == nil || p.RateLimiter == nil {
		return errors.New("platform login runtime is incomplete")
	}
	for _, platform := range []auth.Platform{auth.PlatformSub2API, auth.PlatformNewAPI} {
		if p.Authenticators[platform] == nil {
			return fmt.Errorf("platform login authenticator for %s is required", platform)
		}
		if err := (auth.PlatformEndpointConfig{BaseURL: p.Origins[platform], Timeout: time.Second}).Validate(); err != nil {
			return fmt.Errorf("platform login origin for %s: %w", platform, err)
		}
	}
	return nil
}

func (p *PlatformLogin) Register(server *Server) {
	server.mux.Handle("POST /api/v1/auth/platform-login", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p.login(server, w, r) }))
	server.mux.Handle("POST /api/v1/auth/platform-login/2fa", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p.verifyTwoFA(server, w, r) }))
}

type platformLoginRequest struct {
	Platform   string `json:"platform"`
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
}

type platformLoginTwoFARequest struct {
	Platform  string `json:"platform"`
	TempToken string `json:"temp_token"`
	Code      string `json:"code"`
}

func (p *PlatformLogin) login(server *Server, w http.ResponseWriter, r *http.Request) {
	if !sameOriginBrowserRequest(r) {
		writeError(w, http.StatusForbidden, "CROSS_SITE_REQUEST_REJECTED", "cross-site login requests are not allowed")
		return
	}
	var body platformLoginRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	platform := auth.Platform(strings.ToLower(strings.TrimSpace(body.Platform)))
	authenticator, ok := p.Authenticators[platform]
	if !platform.Valid() || !ok {
		writeError(w, http.StatusUnprocessableEntity, "INVALID_PLATFORM", "platform must be sub2api or newapi")
		return
	}
	if !validPlatformIdentifier(body.Identifier) || !validPlatformPassword(body.Password) {
		writeError(w, http.StatusUnprocessableEntity, "PLATFORM_CREDENTIALS_REQUIRED", "account and password are required")
		return
	}
	clientIP := server.requestClientIP(r)
	if clientIP == nil {
		writeError(w, http.StatusServiceUnavailable, "CLIENT_IP_UNAVAILABLE", "client network identity is unavailable")
		return
	}
	key := platformRateLimitKey("login", platform, clientIP.String(), body.Identifier)
	if !p.RateLimiter.Allow(key) {
		writeError(w, http.StatusTooManyRequests, "PLATFORM_LOGIN_RATE_LIMITED", "登录尝试过于频繁，请稍后重试")
		return
	}
	result, err := authenticator.Login(r.Context(), body.Identifier, body.Password)
	if err != nil {
		p.handleAuthenticatorError(w, key, err)
		return
	}
	if result.RequiresTwoFA {
		if result.TempToken == "" {
			writeError(w, http.StatusServiceUnavailable, "PLATFORM_LOGIN_UNAVAILABLE", "登录服务暂时不可用，请稍后重试")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "requires_two_fa": true, "temp_token": result.TempToken})
		return
	}
	p.RateLimiter.Reset(key)
	p.completeLogin(server, w, r, platform, result)
}

func (p *PlatformLogin) verifyTwoFA(server *Server, w http.ResponseWriter, r *http.Request) {
	if !sameOriginBrowserRequest(r) {
		writeError(w, http.StatusForbidden, "CROSS_SITE_REQUEST_REJECTED", "cross-site login requests are not allowed")
		return
	}
	var body platformLoginTwoFARequest
	if !decodeJSON(w, r, &body) {
		return
	}
	platform := auth.Platform(strings.ToLower(strings.TrimSpace(body.Platform)))
	authenticator, ok := p.Authenticators[platform]
	if !platform.Valid() || !ok {
		writeError(w, http.StatusUnprocessableEntity, "INVALID_PLATFORM", "platform must be sub2api or newapi")
		return
	}
	if !validOpaqueToken(body.TempToken) || strings.TrimSpace(body.Code) == "" || len(body.Code) > 32 || hasControlByte(body.Code) {
		writeError(w, http.StatusUnprocessableEntity, "PLATFORM_TWO_FA_CODE_REQUIRED", "verification code is required")
		return
	}
	clientIP := server.requestClientIP(r)
	if clientIP == nil {
		writeError(w, http.StatusServiceUnavailable, "CLIENT_IP_UNAVAILABLE", "client network identity is unavailable")
		return
	}
	// Keyed by IP + temp_token (not the account identifier, which the client
	// no longer sends): this bounds brute-forcing the verification code for
	// one in-flight login attempt without a second identifier lookup.
	key := platformRateLimitKey("2fa", platform, clientIP.String(), body.TempToken)
	if !p.RateLimiter.Allow(key) {
		writeError(w, http.StatusTooManyRequests, "PLATFORM_LOGIN_RATE_LIMITED", "登录尝试过于频繁，请稍后重试")
		return
	}
	result, err := authenticator.VerifyTwoFA(r.Context(), body.TempToken, body.Code)
	if err != nil {
		p.handleAuthenticatorError(w, key, err)
		return
	}
	p.RateLimiter.Reset(key)
	p.completeLogin(server, w, r, platform, result)
}

func (p *PlatformLogin) handleAuthenticatorError(w http.ResponseWriter, rateLimitKey string, err error) {
	switch {
	case errors.Is(err, auth.ErrPlatformCredentialsInvalid), errors.Is(err, auth.ErrPlatformTwoFAInvalid):
		p.RateLimiter.RecordFailure(rateLimitKey)
		// 422, not 401: the frontend's generic HTTP-status error mapper
		// treats 401 as "your session expired, please log in again", which is
		// nonsensical copy for a first login attempt with a wrong password.
		writeError(w, http.StatusUnprocessableEntity, "PLATFORM_CREDENTIALS_INVALID", "账号或密码不正确")
	case errors.Is(err, auth.ErrPlatformTwoFAUnsupported):
		writeError(w, http.StatusUnprocessableEntity, "PLATFORM_TWO_FA_UNSUPPORTED", "该平台不支持此验证方式")
	default:
		// Upstream unavailable, malformed response, timeout, or any other
		// unexpected failure: fail closed without charging the caller's
		// attempt budget, since it is this service's dependency at fault.
		writeError(w, http.StatusServiceUnavailable, "PLATFORM_LOGIN_UNAVAILABLE", "登录服务暂时不可用，请稍后重试")
	}
}

func (p *PlatformLogin) completeLogin(server *Server, w http.ResponseWriter, r *http.Request, platform auth.Platform, result auth.PlatformLoginResult) {
	if strings.TrimSpace(result.PlatformUserID) == "" {
		writeError(w, http.StatusServiceUnavailable, "PLATFORM_LOGIN_UNAVAILABLE", "登录服务暂时不可用，请稍后重试")
		return
	}
	binding, err := p.Auth.clientBinding(server, r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "CLIENT_BINDING_FAILED", "client binding is unavailable")
		return
	}
	// The platform is the source of truth for its own account's email: a
	// platform-supplied address is treated as verified even if the platform's
	// login response has no separate verified flag. An empty address (e.g. a
	// username-only account) stays unverified rather than fabricated.
	emailVerified := result.EmailVerified || strings.TrimSpace(result.Email) != ""
	principal := auth.Principal{
		Issuer: p.Origins[platform], Subject: result.PlatformUserID,
		Email: result.Email, EmailVerified: emailVerified,
		Platform: platform, PlatformUserID: result.PlatformUserID,
		AuthTime: time.Now().UTC(),
	}
	loginRequestID := requestID(r)
	user, err := p.Auth.ProvisionUser(r.Context(), principal, loginRequestID)
	if err != nil || user.ID == "" {
		writeError(w, http.StatusForbidden, "USER_PROVISION_FAILED", "user account is unavailable")
		return
	}
	credentials, err := p.Auth.Sessions.Issue(r.Context(), auth.IssueSessionInput{
		UserID: user.ID, Principal: principal, Binding: binding, RequestID: loginRequestID,
	})
	if err != nil {
		writeError(w, http.StatusForbidden, "SESSION_ISSUE_FAILED", "login session could not be established")
		return
	}
	p.Auth.setSessionCookies(w, credentials)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// sameOriginBrowserRequest rejects a request only when the browser positively
// asserts it is cross-site. Sec-Fetch-Site is sent by every modern browser;
// its absence (older browsers, some HTTP clients) is not itself treated as
// suspicious, mirroring CSRFPolicy.ValidateMutation's stricter check, which
// this pre-session endpoint cannot fully apply (there is no session yet to
// hold a synchronizer token).
func sameOriginBrowserRequest(r *http.Request) bool {
	site := r.Header.Get("Sec-Fetch-Site")
	return site == "" || site == "same-origin" || site == "none"
}

func validPlatformIdentifier(value string) bool {
	return value != "" && len(value) <= 320 && !hasControlByte(value)
}

func validPlatformPassword(value string) bool {
	return value != "" && len(value) <= 1024
}

func hasControlByte(value string) bool {
	return strings.ContainsAny(value, "\r\n\x00")
}

func validOpaqueToken(value string) bool {
	return len(value) >= 16 && len(value) <= 512 && !hasControlByte(value)
}

func platformRateLimitKey(scope string, platform auth.Platform, clientIP, secret string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + string(platform) + "\x00" + clientIP + "\x00" + strings.ToLower(strings.TrimSpace(secret))))
	return hex.EncodeToString(sum[:])
}
