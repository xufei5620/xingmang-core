package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	defaultRoleClaim       = "roles"
	defaultFlowTTL         = 10 * time.Minute
	defaultIDTokenMaxAge   = 15 * time.Minute
	defaultClockSkew       = 2 * time.Minute
	defaultStepUpMaxAge    = 10 * time.Minute
	defaultLogoutTokenAge  = 10 * time.Minute
	defaultProviderLabel   = "SoloV 统一登录"
	defaultMaxResponseSize = int64(1 << 20)
)

var (
	ErrOIDCDisabled        = errors.New("OIDC verifier is disabled")
	ErrInvalidFlow         = errors.New("invalid or expired authorization flow")
	ErrTokenExchange       = errors.New("OIDC token exchange failed")
	ErrInvalidIDToken      = errors.New("invalid OIDC ID token")
	ErrIdentityMismatch    = errors.New("OIDC identity does not match the current session")
	ErrAdminRoleRequired   = errors.New("administrator role is required")
	ErrMFAStepUpRequired   = errors.New("fresh MFA step-up is required")
	ErrProductionMockAuth  = errors.New("mock authentication is forbidden in production")
	ErrPlainClientSecret   = errors.New("OIDC client secret must be supplied only through a _FILE setting")
	ErrClientSecretMissing = errors.New("OIDC client secret file is required")
	ErrInvalidLogoutToken  = errors.New("invalid OIDC back-channel logout token")
	ErrLogoutUnsupported   = errors.New("OIDC provider logout is not safely configured")
)

// Principal is the verified projection of an ID token. Issuer and Subject are
// the stable identity key. Email is display/delivery metadata, never an account
// binding key.
// Platform and PlatformUserID are set only for a platform-password login (see
// platform_login.go): Issuer then holds the platform's exact configured login
// origin and Subject holds PlatformUserID, so the two fields are an explicit,
// redundant-by-design projection of the same (issuer, subject) pair used
// everywhere else -- never an independent identity key.
type Principal struct {
	Issuer         string    `json:"-"`
	Subject        string    `json:"-"`
	Email          string    `json:"-"`
	EmailVerified  bool      `json:"-"`
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

// OIDCConfig is provider-agnostic but deliberately strict. Endpoint hosts are
// exact hostnames (or hostname:port in local TLS tests), never suffix matches.
// ClientSecretFile is the only supported client-secret input.
type OIDCConfig struct {
	IssuerURL                 string
	ClientID                  string
	ClientSecretFile          string
	RedirectURL               string
	PostLogoutRedirectURL     string
	ProviderLabel             string
	AdminRole                 string
	RoleClaim                 string
	RequiredAdminACR          string
	RequiredAdminAMR          []string
	AllowedSigningAlgs        []string
	AllowedEndpointHosts      []string
	AllowedPrivateEndpointIPs []string
	Scopes                    []string
	TokenEndpointAuthMethod   string
	FlowTTL                   time.Duration
	IDTokenMaxAge             time.Duration
	ClockSkew                 time.Duration
	AdminStepUpMaxAge         time.Duration
	LogoutTokenMaxAge         time.Duration
	RequireBackchannelLogout  bool
	MaximumHTTPResponseBytes  int64
}

func (c OIDCConfig) Validate() error {
	if err := c.validateProviderConfiguration(false); err != nil {
		return err
	}
	if err := validateExactHTTPSURL(c.RedirectURL, true); err != nil {
		return fmt.Errorf("OIDC redirect: %w", err)
	}
	if c.PostLogoutRedirectURL != "" {
		if err := validateExactHTTPSURL(c.PostLogoutRedirectURL, true); err != nil {
			return fmt.Errorf("OIDC post-logout redirect: %w", err)
		}
		if exactOrigin(c.PostLogoutRedirectURL) != exactOrigin(c.RedirectURL) {
			return errors.New("OIDC callback and post-logout redirect must use the same exact origin")
		}
	}
	if strings.TrimSpace(c.ClientID) == "" || strings.TrimSpace(c.ClientID) != c.ClientID || hasControl(c.ClientID) {
		return errors.New("OIDC client ID is required")
	}
	if strings.TrimSpace(c.AdminRole) == "" || strings.TrimSpace(c.AdminRole) != c.AdminRole || hasControl(c.AdminRole) {
		return errors.New("OIDC administrator role is required")
	}
	if len(c.RoleClaim) > 512 || strings.ContainsAny(c.RoleClaim, "\r\n\x00") {
		return errors.New("OIDC role claim is invalid")
	}
	if len(c.providerLabel()) > 128 || hasControl(c.providerLabel()) {
		return errors.New("OIDC provider label is invalid")
	}
	for _, raw := range c.Scopes {
		if strings.TrimSpace(raw) != raw {
			return errors.New("OIDC scopes must not contain surrounding whitespace")
		}
	}
	for _, scope := range c.scopes() {
		if strings.TrimSpace(scope) == "" || strings.ContainsAny(scope, " \t\r\n\x00") {
			return fmt.Errorf("invalid OIDC scope %q", scope)
		}
		if scope == "offline_access" {
			return errors.New("offline_access is not allowed because invoice sessions do not persist refresh tokens")
		}
	}
	if ttl := c.flowTTL(); ttl < time.Minute || ttl > 20*time.Minute {
		return errors.New("OIDC authorization flow TTL must be between 1 and 20 minutes")
	}
	if age := c.idTokenMaxAge(); age < time.Minute || age > time.Hour {
		return errors.New("OIDC ID-token maximum age must be between 1 minute and 1 hour")
	}
	if skew := c.clockSkew(); skew < 0 || skew > 5*time.Minute {
		return errors.New("OIDC clock skew must be between 0 and 5 minutes")
	}
	if age := c.stepUpMaxAge(); age < time.Minute || age > time.Hour {
		return errors.New("OIDC administrator step-up maximum age must be between 1 minute and 1 hour")
	}
	if age := c.logoutTokenMaxAge(); age < time.Minute || age > 30*time.Minute {
		return errors.New("OIDC logout-token maximum age must be between 1 and 30 minutes")
	}
	return nil
}

// ValidateProviderPreflight validates the discovery-only subset of the
// production OIDC contract. It deliberately does not require or read a client
// secret, but it still requires an explicit endpoint allowlist and the same
// back-channel logout policy as the application runtime.
func (c OIDCConfig) ValidateProviderPreflight() error {
	if err := c.validateProviderConfiguration(true); err != nil {
		return err
	}
	if !c.RequireBackchannelLogout {
		return errors.New("production OIDC provider preflight requires back-channel logout")
	}
	return nil
}

func (c OIDCConfig) validateProviderConfiguration(requireExplicitHosts bool) error {
	if err := validateExactHTTPSURL(c.IssuerURL, true); err != nil {
		return fmt.Errorf("OIDC issuer: %w", err)
	}
	for _, raw := range c.AllowedSigningAlgs {
		if strings.TrimSpace(raw) != raw {
			return errors.New("OIDC signing algorithms must not contain surrounding whitespace")
		}
	}
	for _, alg := range c.signingAlgorithms() {
		if !supportedSigningAlgorithm(alg) {
			return fmt.Errorf("OIDC signing algorithm %q is not allowed", alg)
		}
	}
	for _, raw := range c.AllowedEndpointHosts {
		if strings.TrimSpace(raw) != raw {
			return errors.New("OIDC endpoint hosts must not contain surrounding whitespace")
		}
	}
	for _, host := range c.endpointHosts() {
		if err := validateEndpointHost(host); err != nil {
			return err
		}
	}
	if requireExplicitHosts && len(c.AllowedEndpointHosts) == 0 {
		return errors.New("production OIDC provider preflight requires an explicit endpoint host allowlist")
	}
	for _, raw := range c.AllowedPrivateEndpointIPs {
		if strings.TrimSpace(raw) != raw {
			return errors.New("OIDC private endpoint IPs must not contain surrounding whitespace")
		}
		address, err := netip.ParseAddr(raw)
		if err != nil || address.String() != raw || address != address.Unmap() || !address.IsPrivate() {
			return errors.New("OIDC private endpoint IP exceptions must be canonical private IP addresses")
		}
	}
	if method := c.tokenAuthMethod(); method != "client_secret_basic" && method != "client_secret_post" {
		return errors.New("OIDC token endpoint auth method must be client_secret_basic or client_secret_post")
	}
	if c.maxResponseBytes() < 64*1024 || c.maxResponseBytes() > 4*1024*1024 {
		return errors.New("OIDC maximum HTTP response size must be between 64 KiB and 4 MiB")
	}
	return nil
}

func exactOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// ValidateForProduction adds requirements that intentionally make an
// incomplete IdP/MFA configuration fail closed at startup.
func (c OIDCConfig) ValidateForProduction() error {
	if err := c.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(c.ClientSecretFile) == "" || hasControl(c.ClientSecretFile) {
		return ErrClientSecretMissing
	}
	if strings.TrimSpace(c.RequiredAdminACR) == "" || strings.TrimSpace(c.RequiredAdminACR) != c.RequiredAdminACR || len(c.RequiredAdminAMR) == 0 {
		return errors.New("production administrator policy requires both ACR and AMR constraints")
	}
	for _, method := range c.RequiredAdminAMR {
		if strings.TrimSpace(method) == "" || strings.TrimSpace(method) != method || hasControl(method) {
			return errors.New("OIDC required administrator AMR value is invalid")
		}
	}
	if len(c.AllowedEndpointHosts) == 0 {
		return errors.New("production OIDC requires an explicit endpoint host allowlist")
	}
	if c.PostLogoutRedirectURL == "" {
		return errors.New("production OIDC requires an exact post-logout redirect URL")
	}
	if !c.RequireBackchannelLogout {
		return errors.New("production OIDC requires back-channel logout support")
	}
	return nil
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
		return errors.New("invalid OIDC endpoint host")
	}
	parsedURL, err := url.Parse("https://" + host)
	if err != nil || parsedURL.Host != host || parsedURL.Path != "" {
		return errors.New("invalid OIDC endpoint host")
	}
	parsedHost := host
	if h, _, splitErr := net.SplitHostPort(host); splitErr == nil {
		parsedHost = h
	}
	if parsedHost == "" {
		return errors.New("invalid OIDC endpoint host")
	}
	return nil
}

func (c OIDCConfig) roleClaim() string {
	if strings.TrimSpace(c.RoleClaim) == "" {
		return defaultRoleClaim
	}
	return c.RoleClaim
}

func (c OIDCConfig) providerLabel() string {
	if strings.TrimSpace(c.ProviderLabel) == "" {
		return defaultProviderLabel
	}
	return strings.TrimSpace(c.ProviderLabel)
}

func (c OIDCConfig) signingAlgorithms() []string {
	if len(c.AllowedSigningAlgs) == 0 {
		return []string{"RS256"}
	}
	return uniqueStrings(c.AllowedSigningAlgs)
}

func supportedSigningAlgorithm(alg string) bool {
	return slices.Contains([]string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA"}, alg)
}

func (c OIDCConfig) endpointHosts() []string {
	if len(c.AllowedEndpointHosts) != 0 {
		return uniqueStrings(c.AllowedEndpointHosts)
	}
	u, _ := url.Parse(c.IssuerURL)
	if u == nil {
		return nil
	}
	return []string{strings.ToLower(u.Host)}
}

func (c OIDCConfig) scopes() []string {
	values := append([]string{"openid", "email", "profile"}, c.Scopes...)
	return uniqueStrings(values)
}

func (c OIDCConfig) tokenAuthMethod() string {
	if c.TokenEndpointAuthMethod == "" {
		return "client_secret_basic"
	}
	return c.TokenEndpointAuthMethod
}

func (c OIDCConfig) flowTTL() time.Duration {
	if c.FlowTTL == 0 {
		return defaultFlowTTL
	}
	return c.FlowTTL
}

func (c OIDCConfig) idTokenMaxAge() time.Duration {
	if c.IDTokenMaxAge == 0 {
		return defaultIDTokenMaxAge
	}
	return c.IDTokenMaxAge
}

func (c OIDCConfig) clockSkew() time.Duration {
	if c.ClockSkew == 0 {
		return defaultClockSkew
	}
	return c.ClockSkew
}

func (c OIDCConfig) stepUpMaxAge() time.Duration {
	if c.AdminStepUpMaxAge == 0 {
		return defaultStepUpMaxAge
	}
	return c.AdminStepUpMaxAge
}

func (c OIDCConfig) logoutTokenMaxAge() time.Duration {
	if c.LogoutTokenMaxAge == 0 {
		return defaultLogoutTokenAge
	}
	return c.LogoutTokenMaxAge
}

func (c OIDCConfig) maxResponseBytes() int64 {
	if c.MaximumHTTPResponseBytes == 0 {
		return defaultMaxResponseSize
	}
	return c.MaximumHTTPResponseBytes
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
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

// Verifier is retained for compatibility with the first mock milestone.
// OIDCClient deliberately does not implement it because production flows must
// use the server-side Begin/Callback transaction store rather than caller-
// supplied state, nonce and PKCE values.
type Verifier interface {
	AuthorizationURL(state, nonce, codeChallenge string) (string, error)
	ExchangeAndVerify(ctx context.Context, code, state, nonce, codeVerifier string) (Principal, error)
}

type DisabledVerifier struct{}

func (DisabledVerifier) AuthorizationURL(string, string, string) (string, error) {
	return "", ErrOIDCDisabled
}

func (DisabledVerifier) ExchangeAndVerify(context.Context, string, string, string, string) (Principal, error) {
	return Principal{}, ErrOIDCDisabled
}
