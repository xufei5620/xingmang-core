package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
)

var ErrInvalidBearer = errors.New("invalid desktop bearer token")

type BearerVerifier interface {
	Verify(ctx context.Context, rawAccessToken string) (Principal, error)
}

type BearerConfig struct {
	Audiences         []string
	AuthorizedParties []string
	MaximumTokenAge   time.Duration
}

func (c BearerConfig) Validate() error {
	if len(uniqueStrings(c.Audiences)) == 0 || len(uniqueStrings(c.AuthorizedParties)) == 0 {
		return errors.New("desktop bearer verifier requires audience and authorized-party allowlists")
	}
	for _, value := range append(append([]string{}, c.Audiences...), c.AuthorizedParties...) {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value || len(value) > 512 || hasControl(value) {
			return errors.New("desktop bearer allowlist contains an invalid value")
		}
	}
	if c.maxAge() < time.Minute || c.maxAge() > time.Hour {
		return errors.New("desktop bearer maximum age must be between 1 minute and 1 hour")
	}
	return nil
}

func (c BearerConfig) maxAge() time.Duration {
	if c.MaximumTokenAge == 0 {
		return 15 * time.Minute
	}
	return c.MaximumTokenAge
}

type oidcBearerVerifier struct {
	client   *OIDCClient
	config   BearerConfig
	verifier *coreoidc.IDTokenVerifier
}

func (c *OIDCClient) NewBearerVerifier(config BearerConfig) (BearerVerifier, error) {
	if c == nil || c.provider == nil {
		return nil, ErrOIDCDisabled
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	// go-oidc supports one ClientID. Desktop/API deployments need a small exact
	// audience allowlist, so the library verifies signature/issuer/expiry and the
	// verifier below performs the mandatory multi-audience check itself.
	verifier := c.provider.VerifierContext(coreoidc.ClientContext(context.Background(), c.httpClient), &coreoidc.Config{
		SkipClientIDCheck:    true,
		SupportedSigningAlgs: c.algorithms,
	})
	return &oidcBearerVerifier{client: c, config: config, verifier: verifier}, nil
}

func (v *oidcBearerVerifier) Verify(ctx context.Context, rawAccessToken string) (Principal, error) {
	if v == nil || v.client == nil || v.verifier == nil || len(rawAccessToken) < 64 || len(rawAccessToken) > 256*1024 || strings.Count(rawAccessToken, ".") != 2 || strings.ContainsAny(rawAccessToken, " \t\r\n\x00") {
		return Principal{}, ErrInvalidBearer
	}
	if err := validateJWTJSONSegments(rawAccessToken); err != nil {
		return Principal{}, ErrInvalidBearer
	}
	token, err := v.verifier.Verify(coreoidc.ClientContext(ctx, v.client.httpClient), rawAccessToken)
	if err != nil || token.IssuedAt.IsZero() || token.Issuer != v.client.config.IssuerURL {
		return Principal{}, ErrInvalidBearer
	}
	now := v.client.now().UTC()
	if token.IssuedAt.After(now.Add(v.client.config.clockSkew())) || token.IssuedAt.Before(now.Add(-v.config.maxAge())) {
		return Principal{}, ErrInvalidBearer
	}
	principal, azp, err := v.client.principalFromToken(token)
	if err != nil {
		return Principal{}, ErrInvalidBearer
	}
	if !intersects(token.Audience, v.config.Audiences) || !slices.Contains(v.config.AuthorizedParties, azp) {
		return Principal{}, ErrInvalidBearer
	}
	var claims map[string]json.RawMessage
	if err = token.Claims(&claims); err != nil {
		return Principal{}, ErrInvalidBearer
	}
	clientID, err := optionalStringClaim(claims["client_id"])
	if err != nil || clientID != "" && clientID != azp {
		return Principal{}, ErrInvalidBearer
	}
	return principal, nil
}

func intersects(left, right []string) bool {
	for _, value := range left {
		if slices.Contains(right, value) {
			return true
		}
	}
	return false
}

// ExtractDesktopBearer accepts credentials only from one Authorization header.
// It explicitly rejects OAuth query/form bearer conventions because URLs are
// routinely logged and the browser application must use its opaque cookie.
func ExtractDesktopBearer(request *http.Request) (string, error) {
	if request == nil || request.URL == nil {
		return "", ErrInvalidBearer
	}
	query := request.URL.Query()
	for _, key := range []string{"access_token", "token", "bearer_token"} {
		if query.Has(key) {
			return "", ErrInvalidBearer
		}
	}
	values := request.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") || strings.Contains(values[0], ",") {
		return "", ErrInvalidBearer
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	if token == "" || strings.TrimSpace(token) != token || strings.ContainsAny(token, " \t\r\n\x00") {
		return "", ErrInvalidBearer
	}
	return token, nil
}
