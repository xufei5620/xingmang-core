package auth

import (
	"crypto/subtle"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"
)

const (
	defaultClockSkew    = 2 * time.Minute
	defaultStepUpMaxAge = 10 * time.Minute
)

var (
	ErrInvalidFlow        = errors.New("invalid or expired binding challenge")
	ErrIdentityMismatch   = errors.New("identity does not match the current session")
	ErrAdminRoleRequired  = errors.New("administrator role is required")
	ErrMFAStepUpRequired  = errors.New("fresh MFA step-up is required")
	ErrProductionMockAuth = errors.New("mock authentication is forbidden in production")
)

// Principal contains a verified source identity or trusted platform staff identity.
// Issuer/Subject are historical stable keys; email never merges accounts.
type Principal struct {
	Issuer         string    `json:"-"`
	Subject        string    `json:"-"`
	Email          string    `json:"-"`
	EmailVerified  bool      `json:"-"`
	DisplayName    string    `json:"-"`
	Roles          []string  `json:"-"`
	ACR            string    `json:"-"`
	AMR            []string  `json:"-"`
	AuthTime       time.Time `json:"-"`
	IssuedAt       time.Time `json:"-"`
	ExpiresAt      time.Time `json:"-"`
	ProviderSID    string    `json:"-"`
	Platform       Platform  `json:"-"`
	PlatformUserID string    `json:"-"`
}

func (p Principal) IdentityHash() string {
	return sha256Hex(p.Issuer + "\n" + p.Subject)
}

func validateExactHTTPSURL(raw string, requireNoQuery bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || strings.Contains(raw, "#") {
		return errors.New("must be an absolute HTTPS URL without userinfo or fragment")
	}
	if requireNoQuery && (u.RawQuery != "" || u.ForceQuery) {
		return errors.New("must not contain a query")
	}
	if strings.Contains(u.Host, "*") || strings.ContainsAny(raw, "\r\n\x00") {
		return errors.New("must not contain wildcard or control characters")
	}
	return nil
}

func validateEndpointHost(host string) error {
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, "/?#@*\r\n\x00") {
		return errors.New("invalid endpoint host")
	}
	parsedURL, err := url.Parse("https://" + host)
	if err != nil || parsedURL.Host != host || parsedURL.Path != "" {
		return errors.New("invalid endpoint host")
	}
	parsedHost := host
	if h, _, splitErr := net.SplitHostPort(host); splitErr == nil {
		parsedHost = h
	}
	if parsedHost == "" {
		return errors.New("invalid endpoint host")
	}
	return nil
}

func hasControl(value string) bool {
	return strings.ContainsAny(value, "\r\n\x00")
}

func secureEqualHex(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
