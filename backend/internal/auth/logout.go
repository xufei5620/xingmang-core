package auth

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"time"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
)

const backchannelLogoutEvent = "http://schemas.openid.net/event/backchannel-logout"

type VerifiedBackchannelLogout struct {
	Issuer    string    `json:"-"`
	Subject   string    `json:"-"`
	SessionID string    `json:"-"`
	TokenID   string    `json:"-"`
	IssuedAt  time.Time `json:"-"`
	ExpiresAt time.Time `json:"-"`
}

func (c *OIDCClient) RPInitiatedLogoutURL() (string, error) {
	if c == nil || c.logoutURL == "" || c.config.PostLogoutRedirectURL == "" {
		return "", ErrLogoutUnsupported
	}
	if exactOrigin(c.config.PostLogoutRedirectURL) != exactOrigin(c.config.RedirectURL) {
		return "", ErrLogoutUnsupported
	}
	endpoint, err := url.Parse(c.logoutURL)
	if err != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return "", ErrLogoutUnsupported
	}
	query := endpoint.Query()
	query.Set("client_id", c.config.ClientID)
	query.Set("post_logout_redirect_uri", c.config.PostLogoutRedirectURL)
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}

func (c *OIDCClient) VerifyBackchannelLogout(ctx context.Context, raw string) (VerifiedBackchannelLogout, error) {
	if c == nil || c.verifier == nil || len(raw) < 64 || len(raw) > 256*1024 || strings.Count(raw, ".") != 2 || strings.ContainsAny(raw, " \t\r\n\x00") {
		return VerifiedBackchannelLogout{}, ErrInvalidLogoutToken
	}
	if err := validateJWTJSONSegments(raw); err != nil {
		return VerifiedBackchannelLogout{}, ErrInvalidLogoutToken
	}
	verified, err := c.verifier.VerifyLogout(coreoidc.ClientContext(ctx, c.httpClient), raw)
	if err != nil || verified.Issuer != c.config.IssuerURL {
		return VerifiedBackchannelLogout{}, ErrInvalidLogoutToken
	}
	now := c.now().UTC()
	maxAge := c.config.logoutTokenMaxAge()
	if verified.IssuedAt.IsZero() || verified.IssuedAt.After(now.Add(c.config.clockSkew())) || verified.IssuedAt.Before(now.Add(-maxAge)) ||
		!verified.Expiry.After(now) || verified.Expiry.Before(verified.IssuedAt) || verified.Expiry.After(verified.IssuedAt.Add(maxAge+c.config.clockSkew())) {
		return VerifiedBackchannelLogout{}, ErrInvalidLogoutToken
	}
	if strings.TrimSpace(verified.TokenID) == "" || strings.TrimSpace(verified.TokenID) != verified.TokenID || len(verified.TokenID) > 512 || hasControl(verified.TokenID) {
		return VerifiedBackchannelLogout{}, ErrInvalidLogoutToken
	}
	if verified.Subject != "" && (strings.TrimSpace(verified.Subject) != verified.Subject || len(verified.Subject) > 512 || hasControl(verified.Subject)) {
		return VerifiedBackchannelLogout{}, ErrInvalidLogoutToken
	}
	if verified.SessionID != "" && (strings.TrimSpace(verified.SessionID) != verified.SessionID || len(verified.SessionID) > 512 || hasControl(verified.SessionID)) {
		return VerifiedBackchannelLogout{}, ErrInvalidLogoutToken
	}
	if !slices.Contains(verified.Audience, c.config.ClientID) {
		return VerifiedBackchannelLogout{}, ErrInvalidLogoutToken
	}
	var claims struct {
		Events map[string]json.RawMessage `json:"events"`
	}
	if err = verified.Claims(&claims); err != nil || len(claims.Events) != 1 {
		return VerifiedBackchannelLogout{}, ErrInvalidLogoutToken
	}
	rawEvent, ok := claims.Events[backchannelLogoutEvent]
	if !ok {
		return VerifiedBackchannelLogout{}, ErrInvalidLogoutToken
	}
	var eventMembers map[string]json.RawMessage
	if err = json.Unmarshal(rawEvent, &eventMembers); err != nil || eventMembers == nil || len(eventMembers) != 0 {
		return VerifiedBackchannelLogout{}, ErrInvalidLogoutToken
	}
	return VerifiedBackchannelLogout{
		Issuer: verified.Issuer, Subject: verified.Subject, SessionID: verified.SessionID,
		TokenID: verified.TokenID, IssuedAt: verified.IssuedAt.UTC(), ExpiresAt: verified.Expiry.UTC(),
	}, nil
}
