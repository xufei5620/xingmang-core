package auth

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Platform identifies which upstream product (Sub2API or New API) a login,
// identity or session belongs to. See CR-0003/CR-0004 in the platform repo's
// docs/change-requests for the product decision this implements.
type Platform string

const (
	PlatformSub2API Platform = "sub2api"
	PlatformNewAPI  Platform = "newapi"
)

func (p Platform) Valid() bool {
	return p == PlatformSub2API || p == PlatformNewAPI
}

var (
	// ErrPlatformCredentialsInvalid is the single, deliberately generic
	// outcome for a wrong password, an unknown account, or a disabled/locked
	// account: the platform login handler must never let a caller distinguish
	// these from the response.
	ErrPlatformCredentialsInvalid = errors.New("platform account or password is incorrect")
	ErrPlatformTwoFAInvalid       = errors.New("platform verification code is incorrect or expired")
	ErrPlatformUnavailable        = errors.New("platform login service is temporarily unavailable")
	ErrPlatformTwoFAUnsupported   = errors.New("platform does not support two-factor login verification")
	ErrPlatformLoginRateLimited   = errors.New("too many login attempts; try again later")
)

// PlatformLoginResult is the outcome of a platform password verification.
// Username/Email are display/delivery metadata only -- PlatformUserID (paired
// with the platform) is the sole identity key, exactly like Principal.Subject
// for OIDC. When RequiresTwoFA is true every other field except TempToken is
// zero: the caller must not provision a session yet.
type PlatformLoginResult struct {
	RequiresTwoFA  bool
	TempToken      string
	PlatformUserID string
	Username       string
	Email          string
	EmailVerified  bool
}

// PlatformAuthenticator verifies a user-supplied platform account password
// directly against that platform's own login endpoint over HTTPS. The
// password exists only in the forwarded request body: implementations must
// never persist, cache or log it. See sub2api_login.go and newapi_login.go.
type PlatformAuthenticator interface {
	// Login forwards identifier+password to the platform's login endpoint.
	Login(ctx context.Context, identifier, password string) (PlatformLoginResult, error)
	// VerifyTwoFA completes a login that returned RequiresTwoFA. A platform
	// with no 2FA login step returns ErrPlatformTwoFAUnsupported.
	VerifyTwoFA(ctx context.Context, tempToken, code string) (PlatformLoginResult, error)
}

// PlatformEndpointConfig is the per-platform, operator-configured login
// target. BaseURL doubles as the Principal.Issuer used for local identity
// resolution (see ResolveOrCreate), so it must stay stable once deployed.
type PlatformEndpointConfig struct {
	BaseURL string
	Timeout time.Duration
}

func (c PlatformEndpointConfig) Validate() error {
	if err := validateExactHTTPSURL(c.BaseURL, true); err != nil {
		return fmt.Errorf("platform login base URL: %w", err)
	}
	if c.Timeout < time.Second || c.Timeout > time.Minute {
		return errors.New("platform login timeout must be between 1 second and 1 minute")
	}
	return nil
}

func (c PlatformEndpointConfig) origin() string {
	return strings.TrimRight(c.BaseURL, "/")
}

func (c PlatformEndpointConfig) timeout() time.Duration {
	if c.Timeout <= 0 {
		return 10 * time.Second
	}
	return c.Timeout
}

// platformHTTPClient pins outbound calls to one exact HTTPS host and uses
// the shared SSRF-safe dialer and bounded response transport. Tests may supply
// their own TLS-trusted transport; production always uses the protected dialer.
func platformHTTPClient(baseURL string, timeout time.Duration, base *http.Client) (*http.Client, error) {
	parsedHost, err := hostOf(baseURL)
	if err != nil {
		return nil, err
	}
	if err = validateEndpointHost(parsedHost); err != nil {
		return nil, err
	}
	var transport http.RoundTripper
	if base != nil && base.Transport != nil {
		transport = base.Transport
	} else {
		dialContext, dialErr := protectedEndpointDialContext(nil)
		if dialErr != nil {
			return nil, dialErr
		}
		transport = &http.Transport{
			Proxy:                 nil,
			DialContext:           dialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          8,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: timeout,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		}
	}
	return &http.Client{
		Transport: &boundedEndpointTransport{base: transport, allowedHosts: map[string]struct{}{strings.ToLower(parsedHost): {}}, maxBytes: 256 * 1024},
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("platform login HTTP redirects are not allowed")
		},
	}, nil
}

// looksLikeOpaqueToken is a light sanity check on a platform-issued 2FA flow
// token (Sub2API's temp_token, New API's flow_token) before it round-trips
// through our own session/JSON layers.
func looksLikeOpaqueToken(value string) bool {
	return len(value) >= 8 && len(value) <= 2048 && !hasControl(value)
}

func hostOf(rawURL string) (string, error) {
	if err := validateExactHTTPSURL(rawURL, true); err != nil {
		return "", err
	}
	trimmed := strings.TrimPrefix(rawURL, "https://")
	host, _, found := strings.Cut(trimmed, "/")
	if !found {
		host = trimmed
	}
	if host == "" {
		return "", errors.New("platform login base URL host is empty")
	}
	return host, nil
}

// LoginRateLimiter is a bounded, in-memory consecutive-failure lockout keyed
// by an opaque caller-supplied key (typically a hash of client IP + account
// identifier). It matches this service's single-replica V1 topology; see
// RELEASE-READINESS.md.
type LoginRateLimiter struct {
	mu          sync.Mutex
	failures    map[string][]time.Time
	maxAttempts int
	window      time.Duration
	now         func() time.Time
}

func NewLoginRateLimiter(maxAttempts int, window time.Duration) *LoginRateLimiter {
	if maxAttempts <= 0 {
		maxAttempts = 8
	}
	if window <= 0 {
		window = 15 * time.Minute
	}
	return &LoginRateLimiter{failures: make(map[string][]time.Time), maxAttempts: maxAttempts, window: window, now: time.Now}
}

// Allow reports whether a new attempt for key may proceed right now. It does
// not itself count as an attempt: call RecordFailure after a failed
// verification, or Reset after a successful one.
func (l *LoginRateLimiter) Allow(key string) bool {
	if l == nil || key == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(key)
	return len(l.failures[key]) < l.maxAttempts
}

func (l *LoginRateLimiter) RecordFailure(key string) {
	if l == nil || key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(key)
	l.failures[key] = append(l.failures[key], l.now().UTC())
}

func (l *LoginRateLimiter) Reset(key string) {
	if l == nil || key == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}

// prune must be called with mu held.
func (l *LoginRateLimiter) prune(key string) {
	cutoff := l.now().UTC().Add(-l.window)
	kept := l.failures[key][:0]
	for _, at := range l.failures[key] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, key)
		return
	}
	l.failures[key] = kept
}

// PendingTwoFAPlatforms remembers which platform issued a given login's
// upstream temp_token/flow_token (CR-0004 auto-detected login): once the
// login page stops asking the user to pick a platform up front, the browser
// can no longer be trusted to say which platform a follow-up
// POST /platform-login/2fa belongs to, so the backend records it itself at
// the moment the platform accepted the password and asked for a second
// factor. Bounded and TTL-pruned, same shape as LoginRateLimiter; matches
// this service's single-replica V1 topology.
type PendingTwoFAPlatforms struct {
	mu      sync.Mutex
	entries map[string]pendingTwoFAEntry
	ttl     time.Duration
	now     func() time.Time
}

type pendingTwoFAEntry struct {
	platform  Platform
	expiresAt time.Time
}

func NewPendingTwoFAPlatforms(ttl time.Duration) *PendingTwoFAPlatforms {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &PendingTwoFAPlatforms{entries: make(map[string]pendingTwoFAEntry), ttl: ttl, now: time.Now}
}

// Remember records which platform issued tempToken. Safe to call for every
// RequiresTwoFA login result, including one produced by an explicit,
// non-auto-detected platform choice (harmless: Lookup is only consulted when
// the caller omits platform).
func (s *PendingTwoFAPlatforms) Remember(tempToken string, platform Platform) {
	if s == nil || tempToken == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	s.entries[tempToken] = pendingTwoFAEntry{platform: platform, expiresAt: s.now().UTC().Add(s.ttl)}
}

// Lookup reports the platform remembered for tempToken, if any and not yet
// expired. It does not consume the entry: a wrong verification code must
// leave it in place so the browser can retry with the same temp_token.
func (s *PendingTwoFAPlatforms) Lookup(tempToken string) (Platform, bool) {
	if s == nil || tempToken == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	entry, ok := s.entries[tempToken]
	if !ok {
		return "", false
	}
	return entry.platform, true
}

// Forget discards a completed (successfully verified) temp_token's entry.
func (s *PendingTwoFAPlatforms) Forget(tempToken string) {
	if s == nil || tempToken == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, tempToken)
}

// pruneLocked must be called with mu held.
func (s *PendingTwoFAPlatforms) pruneLocked() {
	now := s.now().UTC()
	for token, entry := range s.entries {
		if !entry.expiresAt.After(now) {
			delete(s.entries, token)
		}
	}
}
