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
//
// The login page no longer asks the user which platform they belong to
// (XM-INV-AUTOLOGIN, on top of CR-0004): platformLoginRequest.Platform is
// optional, and when empty, login() auto-detects the platform from the
// identifier's shape (see autoDetectPlatformOrder) and tries candidates in
// order via attemptLogin, stopping at the first definitive
// success/requires-2FA/invalid-credentials result -- an upstream outage never
// falls through to the next candidate. An explicit Platform still selects
// exactly that platform with no auto-detection, unchanged from before this
// follow-up (kept for ops/test callers).
type PlatformLogin struct {
	Authenticators map[auth.Platform]auth.PlatformAuthenticator
	Origins        map[auth.Platform]string // exact configured login origin per platform; doubles as Principal.Issuer
	RateLimiter    *auth.LoginRateLimiter
	// PendingTwoFA remembers which platform issued a login's temp_token when
	// the platform was auto-detected rather than named explicitly by the
	// caller, so POST .../2fa does not need Platform resent. See
	// auth.PendingTwoFAPlatforms.
	PendingTwoFA *auth.PendingTwoFAPlatforms
	Auth         *ProductionAuth
}

func (p *PlatformLogin) Validate() error {
	if p == nil || p.Auth == nil || p.RateLimiter == nil || p.PendingTwoFA == nil {
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
	// Platform is optional: when empty the backend auto-detects it from
	// Identifier (see autoDetectPlatformOrder). An explicit value still
	// selects exactly that platform with no fallback, matching the
	// pre-auto-detect behavior (ops/test callers).
	Platform   string `json:"platform"`
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
}

type platformLoginTwoFARequest struct {
	// Platform is optional here too: the backend already remembers which
	// platform issued TempToken (see PlatformLogin.PendingTwoFA). An explicit
	// value is still honored, matching login()'s ops/test override path.
	Platform  string `json:"platform"`
	TempToken string `json:"temp_token"`
	Code      string `json:"code"`
}

func (p *PlatformLogin) login(server *Server, w http.ResponseWriter, r *http.Request) {
	if !sameOriginBrowserRequest(r) {
		server.logger.Error("platform login rejected: cross-site request", "request_id", requestID(r))
		writeError(w, http.StatusForbidden, "CROSS_SITE_REQUEST_REJECTED", "cross-site login requests are not allowed")
		return
	}
	var body platformLoginRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	explicit := strings.TrimSpace(body.Platform) != ""
	var candidates []auth.Platform
	rateLimitTag := platformAutoDetectTag
	if explicit {
		platform := auth.Platform(strings.ToLower(strings.TrimSpace(body.Platform)))
		if !platform.Valid() || p.Authenticators[platform] == nil {
			writeError(w, http.StatusUnprocessableEntity, "INVALID_PLATFORM", "platform must be sub2api or newapi")
			return
		}
		candidates = []auth.Platform{platform}
		rateLimitTag = platform
	} else {
		// No platform named: auto-detect from the identifier's shape. This is
		// the only path the current login page's form actually exercises.
		candidates = autoDetectPlatformOrder(body.Identifier)
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
	// Auto-detect mode may try up to two upstream platforms for this one
	// submission (see attemptLogin), but rateLimitTag is a single fixed value
	// regardless of which candidates end up being tried, so both attempts
	// share one IP+identifier bucket rather than doubling the caller's
	// effective attempt budget.
	key := platformRateLimitKey("login", rateLimitTag, clientIP.String(), body.Identifier)
	if !p.RateLimiter.Allow(key) {
		writeError(w, http.StatusTooManyRequests, "PLATFORM_LOGIN_RATE_LIMITED", "登录尝试过于频繁，请稍后重试")
		return
	}
	result, matchedPlatform, err := p.attemptLogin(r, candidates, body.Identifier, body.Password)
	if err != nil {
		p.handleAuthenticatorError(w, key, err)
		return
	}
	if result.RequiresTwoFA {
		if result.TempToken == "" {
			writeError(w, http.StatusServiceUnavailable, "PLATFORM_LOGIN_UNAVAILABLE", "登录服务暂时不可用，请稍后重试")
			return
		}
		p.PendingTwoFA.Remember(result.TempToken, matchedPlatform)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "requires_two_fa": true, "temp_token": result.TempToken})
		return
	}
	p.RateLimiter.Reset(key)
	p.completeLogin(server, w, r, matchedPlatform, result)
}

func (p *PlatformLogin) verifyTwoFA(server *Server, w http.ResponseWriter, r *http.Request) {
	if !sameOriginBrowserRequest(r) {
		server.logger.Error("platform login 2fa rejected: cross-site request", "request_id", requestID(r))
		writeError(w, http.StatusForbidden, "CROSS_SITE_REQUEST_REJECTED", "cross-site login requests are not allowed")
		return
	}
	var body platformLoginTwoFARequest
	if !decodeJSON(w, r, &body) {
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

	var platform auth.Platform
	if explicit := strings.TrimSpace(body.Platform); explicit != "" {
		platform = auth.Platform(strings.ToLower(explicit))
		if !platform.Valid() || p.Authenticators[platform] == nil {
			writeError(w, http.StatusUnprocessableEntity, "INVALID_PLATFORM", "platform must be sub2api or newapi")
			return
		}
	} else if remembered, ok := p.PendingTwoFA.Lookup(body.TempToken); ok {
		platform = remembered
	} else {
		// No platform was supplied and none is remembered for this temp_token
		// (unknown, or its bookkeeping entry expired): treated exactly like a
		// wrong verification code so the response and rate-limit accounting
		// stay indistinguishable from a real wrong-code attempt, and never
		// leak which case occurred.
		key := platformRateLimitKey("2fa", platformAutoDetectTag, clientIP.String(), body.TempToken)
		p.handleAuthenticatorError(w, key, auth.ErrPlatformTwoFAInvalid)
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
	result, err := p.Authenticators[platform].VerifyTwoFA(r.Context(), body.TempToken, body.Code)
	if err != nil {
		p.handleAuthenticatorError(w, key, err)
		return
	}
	p.RateLimiter.Reset(key)
	p.PendingTwoFA.Forget(body.TempToken)
	p.completeLogin(server, w, r, platform, result)
}

// platformAutoDetectTag is the fixed rate-limit key discriminator used when
// the caller omits `platform` (auto-detect mode): every candidate platform
// tried for one auto-detected login attempt shares this single tag, so
// trying up to two upstream platforms for one submitted form never consumes
// more than one slot of the caller's IP+identifier attempt budget. An
// explicit-platform request keeps using its real platform as the tag
// (unchanged from before this follow-up), so it is never rate-limited
// together with auto-detect traffic for the same identifier.
const platformAutoDetectTag auth.Platform = "auto"

// autoDetectPlatformOrder picks which platform(s) to try, and in what
// order, when the login request does not name one explicitly. An
// email-shaped identifier is plausible on both platforms (a New API
// username may itself happen to look like an email address), so Sub2API is
// tried first and New API is the fallback; a bare (non-email) identifier
// can only ever be a New API username, since Sub2API accounts are always
// keyed by email -- trying Sub2API for it would just waste an upstream call
// that is guaranteed to fail.
func autoDetectPlatformOrder(identifier string) []auth.Platform {
	if strings.Contains(identifier, "@") {
		return []auth.Platform{auth.PlatformSub2API, auth.PlatformNewAPI}
	}
	return []auth.Platform{auth.PlatformNewAPI}
}

// attemptLogin tries each candidate platform's authenticator in order and
// returns the first one that accepts the credentials (including a
// requires-2FA result). It only advances to the next candidate on a
// definitive ErrPlatformCredentialsInvalid: any other error (upstream
// outage, timeout, malformed response) is returned immediately without
// trying the remaining candidates, so one platform's downtime is never
// misreported as a wrong password for a different, available platform.
func (p *PlatformLogin) attemptLogin(r *http.Request, candidates []auth.Platform, identifier, password string) (auth.PlatformLoginResult, auth.Platform, error) {
	var lastErr error
	for _, platform := range candidates {
		result, err := p.Authenticators[platform].Login(r.Context(), identifier, password)
		if err == nil {
			return result, platform, nil
		}
		lastErr = err
		if !errors.Is(err, auth.ErrPlatformCredentialsInvalid) {
			break
		}
	}
	return auth.PlatformLoginResult{}, "", lastErr
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
	loginRequestID := requestID(r)
	if strings.TrimSpace(result.PlatformUserID) == "" {
		server.logger.Error("platform login failed: authenticator returned no platform user ID", "request_id", loginRequestID, "platform", string(platform))
		writeError(w, http.StatusServiceUnavailable, "PLATFORM_LOGIN_UNAVAILABLE", "登录服务暂时不可用，请稍后重试")
		return
	}
	binding, err := p.Auth.clientBinding(server, r)
	if err != nil {
		server.logger.Error("platform login failed: client binding unavailable", "request_id", loginRequestID, "platform", string(platform), "error", err)
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
		DisplayName: strings.TrimSpace(result.Username),
		Platform:    platform, PlatformUserID: result.PlatformUserID,
		AuthTime: time.Now().UTC(),
	}
	user, err := p.Auth.ProvisionUser(r.Context(), principal, loginRequestID)
	if err != nil || user.ID == "" {
		if err == nil {
			err = errors.New("provisioned user has no ID")
		}
		server.logger.Error("platform login failed: user provisioning failed", "request_id", loginRequestID, "platform", string(platform), "error", err)
		writeError(w, http.StatusForbidden, "USER_PROVISION_FAILED", "user account is unavailable")
		return
	}
	if user.CanonicalIssuer != "" && user.CanonicalSubject != "" {
		// The session INSERT only succeeds when the principal pair equals
		// the invoice_user's stored oidc_issuer/oidc_subject. A claim login
		// lands on a pre-existing identity whose canonical pair is its SSO
		// identity, not this login's synthetic platform pair -- issue the
		// session with the stored pair (the session row itself keeps
		// Platform/PlatformUserID for audit). For freshly created platform
		// users the two pairs are identical and this is a no-op.
		principal.Issuer = user.CanonicalIssuer
		principal.Subject = user.CanonicalSubject
	}
	credentials, err := p.Auth.Sessions.Issue(r.Context(), auth.IssueSessionInput{
		UserID: user.ID, Principal: principal, Binding: binding, RequestID: loginRequestID,
	})
	if err != nil {
		server.logger.Error("platform login failed: session issuance failed", "request_id", loginRequestID, "platform", string(platform), "error", err)
		writeError(w, http.StatusForbidden, "SESSION_ISSUE_FAILED", "login session could not be established")
		return
	}
	p.Auth.setSessionCookies(w, credentials)
	server.logger.Info("platform login succeeded", "request_id", loginRequestID,
		"platform", string(platform), "claimed", user.Claimed, "user_id", idPrefix(user.ID))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// idPrefix returns the first 8 characters of an ID for logging -- enough to
// correlate log lines with a database row without writing a full user ID
// (personal-ish identifier) to the log stream. Safe on any length input.
func idPrefix(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
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
