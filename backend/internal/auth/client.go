package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

const flowVerifierAADPrefix = "invoice:oidc-flow:pkce:"

type BeginAuthorizationInput struct {
	Purpose           FlowPurpose
	BrowserBinding    string
	ExpectedIdentity  *Principal
	ExistingSessionID string
	ReturnPath        string
}

type AuthorizationRequest struct {
	URL            string `json:"-"`
	State          string `json:"-"`
	BrowserBinding string `json:"-"`
	ExpiresAt      time.Time
	ProviderLabel  string
}

type CallbackInput struct {
	Code           string
	State          string
	BrowserBinding string
}

type CallbackResult struct {
	Principal         Principal   `json:"-"`
	Purpose           FlowPurpose `json:"-"`
	ExistingSessionID string      `json:"-"`
	ReturnPath        string      `json:"-"`
}

type OIDCClient struct {
	config     OIDCConfig
	flowStore  FlowStore
	protector  FlowSecretProtector
	httpClient *http.Client
	oauth      oauth2.Config
	provider   *coreoidc.Provider
	verifier   *coreoidc.IDTokenVerifier
	algorithms []string
	logoutURL  string
	now        func() time.Time
}

func (c *OIDCClient) String() string {
	if c == nil {
		return "OIDCClient(disabled)"
	}
	return "OIDCClient(provider=" + c.config.providerLabel() + ")"
}

func (c *OIDCClient) GoString() string { return c.String() }

type discoveryDocument struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	UserInfoEndpoint                  string   `json:"userinfo_endpoint"`
	JWKSURI                           string   `json:"jwks_uri"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	ScopesSupported                   []string `json:"scopes_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	IDTokenSigningAlgorithmsSupported []string `json:"id_token_signing_alg_values_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	EndSessionEndpoint                string   `json:"end_session_endpoint"`
	BackchannelLogoutSupported        bool     `json:"backchannel_logout_supported"`
	BackchannelLogoutSessionSupported bool     `json:"backchannel_logout_session_supported"`
}

// NewOIDCClient is the production constructor. It has no plaintext secret
// parameter by design; callers can only name a mounted secret file.
func NewOIDCClient(ctx context.Context, config OIDCConfig, flows FlowStore, protector FlowSecretProtector) (*OIDCClient, error) {
	if err := config.ValidateForProduction(); err != nil {
		return nil, err
	}
	secret, err := LoadClientSecretFile(config.ClientSecretFile)
	if err != nil {
		return nil, err
	}
	return newOIDCClient(ctx, config, secret, nil, flows, protector)
}

// newOIDCClient accepts dependencies only so cryptographic/HTTP tests can run
// against httptest TLS without a real identity provider. It is intentionally
// unexported to keep production code on the _FILE constructor.
func newOIDCClient(ctx context.Context, config OIDCConfig, clientSecret string, baseClient *http.Client, flows FlowStore, protector FlowSecretProtector) (*OIDCClient, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if clientSecret == "" {
		return nil, ErrClientSecretMissing
	}
	if flows == nil || protector == nil {
		return nil, errors.New("OIDC flow store and secret protector are required")
	}
	client, err := strictOIDCHTTPClient(baseClient, config)
	if err != nil {
		return nil, err
	}
	discovery, algorithms, err := discoverProviderWithRetry(ctx, client, config)
	if err != nil {
		return nil, err
	}
	provider := (&coreoidc.ProviderConfig{
		IssuerURL:  config.IssuerURL,
		AuthURL:    discovery.AuthorizationEndpoint,
		TokenURL:   discovery.TokenEndpoint,
		JWKSURL:    discovery.JWKSURI,
		Algorithms: algorithms,
	}).NewProvider(coreoidc.ClientContext(ctx, client))
	authStyle := oauth2.AuthStyleInHeader
	if config.tokenAuthMethod() == "client_secret_post" {
		authStyle = oauth2.AuthStyleInParams
	}
	oauthConfig := oauth2.Config{
		ClientID:     config.ClientID,
		ClientSecret: clientSecret,
		RedirectURL:  config.RedirectURL,
		Endpoint: oauth2.Endpoint{
			AuthURL:   discovery.AuthorizationEndpoint,
			TokenURL:  discovery.TokenEndpoint,
			AuthStyle: authStyle,
		},
		Scopes: config.scopes(),
	}
	return &OIDCClient{
		config:     config,
		flowStore:  flows,
		protector:  protector,
		httpClient: client,
		oauth:      oauthConfig,
		provider:   provider,
		algorithms: append([]string(nil), algorithms...),
		logoutURL:  discovery.EndSessionEndpoint,
		verifier: provider.VerifierContext(coreoidc.ClientContext(ctx, client), &coreoidc.Config{
			ClientID:             config.ClientID,
			SupportedSigningAlgs: algorithms,
		}),
		now: time.Now,
	}, nil
}

func discoverProviderWithRetry(ctx context.Context, client *http.Client, config OIDCConfig) (discoveryDocument, []string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		document, algorithms, err := discoverProvider(ctx, client, config)
		if err == nil {
			return document, algorithms, nil
		}
		lastErr = err
		var networkError net.Error
		if !errors.As(err, &networkError) || attempt == 2 {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return discoveryDocument{}, nil, ctx.Err()
		case <-timer.C:
		}
	}
	return discoveryDocument{}, nil, lastErr
}

func (c *OIDCClient) Begin(ctx context.Context, input BeginAuthorizationInput) (AuthorizationRequest, error) {
	if c == nil || c.flowStore == nil || c.protector == nil {
		return AuthorizationRequest{}, ErrOIDCDisabled
	}
	if !input.Purpose.Valid() {
		return AuthorizationRequest{}, errors.New("invalid authorization flow purpose")
	}
	if err := validateReturnPath(input.ReturnPath); err != nil {
		return AuthorizationRequest{}, err
	}
	browserBinding := input.BrowserBinding
	if browserBinding == "" {
		var err error
		browserBinding, err = randomURLToken(32)
		if err != nil {
			return AuthorizationRequest{}, err
		}
	}
	if !validOpaqueToken(browserBinding) {
		return AuthorizationRequest{}, errors.New("invalid OIDC browser binding")
	}
	state, err := randomURLToken(32)
	if err != nil {
		return AuthorizationRequest{}, err
	}
	nonce, err := randomURLToken(32)
	if err != nil {
		return AuthorizationRequest{}, err
	}
	verifier, err := randomURLToken(32)
	if err != nil {
		return AuthorizationRequest{}, err
	}
	now := c.now().UTC()
	flow := AuthorizationFlow{
		StateHash:          sha256Hex(state),
		NonceHash:          sha256Hex(nonce),
		BrowserBindingHash: sha256Hex(browserBinding),
		Purpose:            input.Purpose,
		ExistingSessionID:  input.ExistingSessionID,
		ReturnPath:         input.ReturnPath,
		CreatedAt:          now,
		ExpiresAt:          now.Add(c.config.flowTTL()),
	}
	if input.ExpectedIdentity != nil {
		flow.ExpectedIdentityHash = input.ExpectedIdentity.IdentityHash()
	}
	if input.Purpose != FlowLogin && input.ExpectedIdentity == nil {
		return AuthorizationRequest{}, errors.New("step-up and account binding require the current identity")
	}
	flow.CodeVerifierCiphertext, flow.CodeVerifierKeyVersion, err = c.protector.Seal([]byte(verifier), flowVerifierAADPrefix+flow.StateHash)
	if err != nil {
		return AuthorizationRequest{}, fmt.Errorf("protect PKCE verifier: %w", err)
	}
	if err = c.flowStore.Create(ctx, flow); err != nil {
		return AuthorizationRequest{}, fmt.Errorf("store authorization flow: %w", err)
	}
	options := []oauth2.AuthCodeOption{coreoidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)}
	if input.Purpose == FlowAdminStepUp {
		options = append(options, oauth2.SetAuthURLParam("prompt", "login"), oauth2.SetAuthURLParam("max_age", "0"))
		if c.config.RequiredAdminACR != "" {
			options = append(options, oauth2.SetAuthURLParam("acr_values", c.config.RequiredAdminACR))
		}
	}
	return AuthorizationRequest{
		URL:            c.oauth.AuthCodeURL(state, options...),
		State:          state,
		BrowserBinding: browserBinding,
		ExpiresAt:      flow.ExpiresAt,
		ProviderLabel:  c.config.providerLabel(),
	}, nil
}

func (c *OIDCClient) Callback(ctx context.Context, input CallbackInput) (CallbackResult, error) {
	if c == nil || c.flowStore == nil || c.protector == nil {
		return CallbackResult{}, ErrOIDCDisabled
	}
	if !validOpaqueToken(input.State) || !validOpaqueToken(input.BrowserBinding) || strings.TrimSpace(input.Code) == "" || len(input.Code) > 4096 || hasControl(input.Code) {
		return CallbackResult{}, ErrInvalidFlow
	}
	now := c.now().UTC()
	flow, err := c.flowStore.Consume(ctx, sha256Hex(input.State), sha256Hex(input.BrowserBinding), now)
	if err != nil {
		return CallbackResult{}, ErrInvalidFlow
	}
	verifierBytes, err := c.protector.Open(flow.CodeVerifierCiphertext, flow.CodeVerifierKeyVersion, flowVerifierAADPrefix+flow.StateHash)
	if err != nil || !validOpaqueToken(string(verifierBytes)) {
		return CallbackResult{}, ErrInvalidFlow
	}
	exchangeCtx := coreoidc.ClientContext(ctx, c.httpClient)
	token, err := c.oauth.Exchange(exchangeCtx, input.Code, oauth2.VerifierOption(string(verifierBytes)))
	for i := range verifierBytes {
		verifierBytes[i] = 0
	}
	if err != nil {
		return CallbackResult{}, ErrTokenExchange
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" || len(rawIDToken) > 256*1024 {
		return CallbackResult{}, ErrInvalidIDToken
	}
	if err := validateJWTJSONSegments(rawIDToken); err != nil {
		return CallbackResult{}, ErrInvalidIDToken
	}
	idToken, err := c.verifier.Verify(exchangeCtx, rawIDToken)
	if err != nil {
		return CallbackResult{}, ErrInvalidIDToken
	}
	if idToken.Issuer != c.config.IssuerURL || !idToken.Expiry.After(now) {
		return CallbackResult{}, ErrInvalidIDToken
	}
	if idToken.AccessTokenHash != "" {
		if token.AccessToken == "" || idToken.VerifyAccessToken(token.AccessToken) != nil {
			return CallbackResult{}, ErrInvalidIDToken
		}
	}
	if idToken.Nonce == "" || !secureEqualHex(sha256Hex(idToken.Nonce), flow.NonceHash) {
		return CallbackResult{}, ErrInvalidIDToken
	}
	if idToken.IssuedAt.IsZero() || idToken.IssuedAt.After(now.Add(c.config.clockSkew())) || idToken.IssuedAt.Before(now.Add(-c.config.idTokenMaxAge())) {
		return CallbackResult{}, ErrInvalidIDToken
	}
	principal, azp, err := c.principalFromToken(idToken)
	if err != nil || !validAuthorizedParty(idToken.Audience, azp, c.config.ClientID) {
		return CallbackResult{}, ErrInvalidIDToken
	}
	if flow.ExpectedIdentityHash != "" && !secureEqualHex(principal.IdentityHash(), flow.ExpectedIdentityHash) {
		return CallbackResult{}, ErrIdentityMismatch
	}
	if flow.Purpose == FlowAdminStepUp {
		policy := AdminPolicy{
			Role:         c.config.AdminRole,
			RequiredACR:  c.config.RequiredAdminACR,
			RequiredAMR:  c.config.RequiredAdminAMR,
			StepUpMaxAge: c.config.stepUpMaxAge(),
			ClockSkew:    c.config.clockSkew(),
		}
		if err = policy.VerifyFreshPrincipal(principal, now, flow.CreatedAt); err != nil {
			return CallbackResult{}, err
		}
	}
	return CallbackResult{Principal: principal, Purpose: flow.Purpose, ExistingSessionID: flow.ExistingSessionID, ReturnPath: flow.ReturnPath}, nil
}

func (c *OIDCClient) principalFromToken(token *coreoidc.IDToken) (Principal, string, error) {
	var claims map[string]json.RawMessage
	if err := token.Claims(&claims); err != nil {
		return Principal{}, "", err
	}
	if token.Subject == "" || len(token.Subject) > 512 || hasControl(token.Subject) {
		return Principal{}, "", errors.New("OIDC subject is invalid")
	}
	roles, err := stringListClaim(claims[c.config.roleClaim()], true)
	if err != nil {
		return Principal{}, "", err
	}
	amr, err := stringListClaim(claims["amr"], true)
	if err != nil {
		return Principal{}, "", err
	}
	email, err := optionalStringClaim(claims["email"])
	if err != nil || len(email) > 320 || hasControl(email) || strings.TrimSpace(email) != email {
		return Principal{}, "", errors.New("OIDC email claim is invalid")
	}
	if email != "" {
		parsed, parseErr := mail.ParseAddress(email)
		if parseErr != nil || parsed.Name != "" || parsed.Address != email {
			return Principal{}, "", errors.New("OIDC email claim is invalid")
		}
		at := strings.LastIndexByte(email, '@')
		if at <= 0 || at == len(email)-1 {
			return Principal{}, "", errors.New("OIDC email claim is invalid")
		}
		email = email[:at+1] + strings.ToLower(email[at+1:])
	}
	emailVerified, err := optionalBoolClaim(claims["email_verified"])
	if err != nil {
		return Principal{}, "", err
	}
	acr, err := optionalStringClaim(claims["acr"])
	if err != nil || len(acr) > 512 || hasControl(acr) || strings.TrimSpace(acr) != acr {
		return Principal{}, "", errors.New("OIDC ACR claim is invalid")
	}
	sid, err := optionalStringClaim(claims["sid"])
	if err != nil || len(sid) > 512 || hasControl(sid) || strings.TrimSpace(sid) != sid {
		return Principal{}, "", errors.New("OIDC sid claim is invalid")
	}
	authTime, err := numericDateClaim(claims["auth_time"])
	if err != nil {
		return Principal{}, "", err
	}
	azp, err := optionalStringClaim(claims["azp"])
	if err != nil || hasControl(azp) || strings.TrimSpace(azp) != azp {
		return Principal{}, "", errors.New("OIDC azp claim is invalid")
	}
	return Principal{
		Issuer: token.Issuer, Subject: token.Subject,
		Email: email, EmailVerified: emailVerified,
		Roles: roles, ACR: acr, AMR: amr, AuthTime: authTime,
		IssuedAt: token.IssuedAt, ExpiresAt: token.Expiry, ProviderSID: sid,
	}, azp, nil
}

func validAuthorizedParty(audience []string, azp, clientID string) bool {
	if !slices.Contains(audience, clientID) {
		return false
	}
	if len(audience) > 1 {
		return azp == clientID
	}
	return azp == "" || azp == clientID
}

func optionalStringClaim(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errors.New("OIDC string claim has the wrong type")
	}
	return value, nil
}

func optionalBoolClaim(raw json.RawMessage) (bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, errors.New("OIDC boolean claim has the wrong type")
	}
	return value, nil
}

func stringListClaim(raw json.RawMessage, optional bool) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		if optional {
			return nil, nil
		}
		return nil, errors.New("OIDC list claim is missing")
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, errors.New("OIDC list claim has the wrong type")
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value || len(value) > 512 || hasControl(value) {
			return nil, errors.New("OIDC list claim contains an invalid value")
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func numericDateClaim(raw json.RawMessage) (time.Time, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}, nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return time.Time{}, errors.New("OIDC auth_time claim has the wrong type")
	}
	seconds, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, errors.New("OIDC auth_time claim is invalid")
	}
	return time.Unix(seconds, 0).UTC(), nil
}

func validOpaqueToken(value string) bool {
	if len(value) < 43 || len(value) > 128 || strings.ContainsAny(value, "=\r\n\x00") {
		return false
	}
	decoded, err := url.QueryUnescape(value)
	return err == nil && decoded == value
}

func discoverProvider(ctx context.Context, client *http.Client, config OIDCConfig) (discoveryDocument, []string, error) {
	discoveryURL := strings.TrimSuffix(config.IssuerURL, "/") + "/.well-known/openid-configuration"
	var document discoveryDocument
	if err := getJSON(ctx, client, discoveryURL, &document); err != nil {
		return document, nil, fmt.Errorf("OIDC discovery failed: %w", err)
	}
	if document.Issuer != config.IssuerURL {
		return document, nil, errors.New("OIDC discovery issuer does not exactly match configured issuer")
	}
	allowedHosts := config.endpointHosts()
	for label, endpoint := range map[string]string{
		"authorization": document.AuthorizationEndpoint,
		"token":         document.TokenEndpoint,
		"userinfo":      document.UserInfoEndpoint,
		"jwks":          document.JWKSURI,
		"end-session":   document.EndSessionEndpoint,
	} {
		if err := validateProviderEndpoint(endpoint, allowedHosts); err != nil {
			return document, nil, fmt.Errorf("OIDC %s endpoint: %w", label, err)
		}
	}
	if config.RequireBackchannelLogout && !document.BackchannelLogoutSupported {
		return document, nil, errors.New("OIDC provider does not advertise back-channel logout")
	}
	if config.RequireBackchannelLogout && !document.BackchannelLogoutSessionSupported {
		return document, nil, errors.New("OIDC provider does not advertise session-aware back-channel logout")
	}
	if !slices.Contains(document.ResponseTypesSupported, "code") {
		return document, nil, errors.New("OIDC provider does not advertise authorization code flow")
	}
	if !slices.Contains(document.CodeChallengeMethodsSupported, "S256") {
		return document, nil, errors.New("OIDC provider does not advertise PKCE S256")
	}
	if len(document.ScopesSupported) > 0 && !slices.Contains(document.ScopesSupported, "openid") {
		return document, nil, errors.New("OIDC provider does not advertise the openid scope")
	}
	methods := document.TokenEndpointAuthMethodsSupported
	if len(methods) == 0 {
		methods = []string{"client_secret_basic"}
	}
	if !slices.Contains(methods, config.tokenAuthMethod()) {
		return document, nil, errors.New("OIDC provider does not advertise the configured token authentication method")
	}
	var algorithms []string
	for _, allowed := range config.signingAlgorithms() {
		if slices.Contains(document.IDTokenSigningAlgorithmsSupported, allowed) {
			algorithms = append(algorithms, allowed)
		}
	}
	if len(algorithms) == 0 {
		return document, nil, errors.New("OIDC provider advertises no allowed ID-token signing algorithm")
	}
	if err := preflightJWKS(ctx, client, document.JWKSURI, algorithms); err != nil {
		return document, nil, err
	}
	return document, algorithms, nil
}

func validateProviderEndpoint(raw string, allowedHosts []string) error {
	if err := validateExactHTTPSURL(raw, true); err != nil {
		return err
	}
	u, _ := url.Parse(raw)
	for _, host := range allowedHosts {
		if strings.EqualFold(u.Host, host) {
			return nil
		}
	}
	return errors.New("endpoint host is not explicitly allowed")
}

func preflightJWKS(ctx context.Context, client *http.Client, uri string, algorithms []string) error {
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := getJSON(ctx, client, uri, &set); err != nil {
		return fmt.Errorf("OIDC JWKS preflight failed: %w", err)
	}
	seenKids := make(map[string]struct{})
	usable := 0
	for _, rawKey := range set.Keys {
		var key struct {
			KeyType string   `json:"kty"`
			KeyID   string   `json:"kid"`
			Use     string   `json:"use"`
			Alg     string   `json:"alg"`
			KeyOps  []string `json:"key_ops"`
			D       string   `json:"d"`
			P       string   `json:"p"`
			Q       string   `json:"q"`
			DP      string   `json:"dp"`
			DQ      string   `json:"dq"`
			QI      string   `json:"qi"`
		}
		if err := json.Unmarshal(rawKey, &key); err != nil {
			return errors.New("OIDC JWKS contains a malformed key")
		}
		if key.D != "" || key.P != "" || key.Q != "" || key.DP != "" || key.DQ != "" || key.QI != "" {
			return errors.New("OIDC JWKS unexpectedly contains private key material")
		}
		if key.KeyID != "" {
			if _, exists := seenKids[key.KeyID]; exists {
				return errors.New("OIDC JWKS contains duplicate key IDs")
			}
			seenKids[key.KeyID] = struct{}{}
		}
		if !slices.Contains([]string{"RSA", "EC", "OKP"}, key.KeyType) || key.Use != "" && key.Use != "sig" {
			continue
		}
		if len(key.KeyOps) != 0 && !slices.Contains(key.KeyOps, "verify") {
			continue
		}
		if key.Alg != "" && !slices.Contains(algorithms, key.Alg) {
			continue
		}
		var parsed jose.JSONWebKey
		if err := json.Unmarshal(rawKey, &parsed); err != nil || !parsed.Valid() || !parsed.IsPublic() {
			return errors.New("OIDC JWKS contains an invalid public signing key")
		}
		if key.Alg != "" {
			if !publicJWKSupportsAlgorithm(parsed, key.Alg) {
				return errors.New("OIDC JWKS signing key does not match its advertised algorithm")
			}
		} else {
			compatible := false
			for _, algorithm := range algorithms {
				if publicJWKSupportsAlgorithm(parsed, algorithm) {
					compatible = true
					break
				}
			}
			if !compatible {
				continue
			}
		}
		usable++
	}
	if usable == 0 {
		return errors.New("OIDC JWKS contains no usable public signing key")
	}
	return nil
}

func publicJWKSupportsAlgorithm(key jose.JSONWebKey, algorithm string) bool {
	switch publicKey := key.Key.(type) {
	case *rsa.PublicKey:
		return slices.Contains([]string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512"}, algorithm) &&
			publicKey.N != nil && publicKey.N.BitLen() >= 2048
	case *ecdsa.PublicKey:
		if publicKey.Curve == nil || publicKey.Curve.Params() == nil {
			return false
		}
		return algorithm == "ES256" && publicKey.Curve.Params().Name == "P-256" ||
			algorithm == "ES384" && publicKey.Curve.Params().Name == "P-384" ||
			algorithm == "ES512" && publicKey.Curve.Params().Name == "P-521"
	case ed25519.PublicKey:
		return algorithm == "EdDSA" && len(publicKey) == ed25519.PublicKeySize
	default:
		return false
	}
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return redactOIDCRequestError(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	contentType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	contentType = strings.ToLower(contentType)
	if parseErr != nil || contentType != "application/json" && contentType != "application/jwk-set+json" {
		return errors.New("response Content-Type is not JSON")
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if err = rejectDuplicateJSONKeys(body); err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err = decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("JSON response contains trailing data")
	}
	return nil
}

type oidcRequestError struct{ cause error }

func (e *oidcRequestError) Error() string { return "OIDC provider request failed" }
func (e *oidcRequestError) Unwrap() error { return e.cause }

func redactOIDCRequestError(err error) error {
	var requestError *url.Error
	if errors.As(err, &requestError) {
		return &oidcRequestError{cause: err}
	}
	return err
}

func validateJWTJSONSegments(raw string) error {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return errors.New("JWT must have three segments")
	}
	for _, encoded := range parts[:2] {
		if len(encoded) == 0 || len(encoded) > 256*1024 {
			return errors.New("JWT JSON segment length is invalid")
		}
		body, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || len(body) == 0 {
			return errors.New("JWT JSON segment encoding is invalid")
		}
		if err = rejectDuplicateJSONKeys(body); err != nil {
			return err
		}
	}
	return nil
}

func rejectDuplicateJSONKeys(body []byte) error {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	if err := consumeUniqueJSONValue(decoder); err != nil {
		return err
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		return errors.New("JSON contains trailing data")
	}
	return nil
}

func consumeUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return errors.New("JSON object contains a duplicate key")
			}
			seen[key] = struct{}{}
			if err = consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("JSON object is not terminated")
		}
	case '[':
		for decoder.More() {
			if err := consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("JSON array is not terminated")
		}
	default:
		return errors.New("JSON begins with an unexpected delimiter")
	}
	return nil
}

func strictOIDCHTTPClient(base *http.Client, config OIDCConfig) (*http.Client, error) {
	allowedHosts := config.endpointHosts()
	if len(allowedHosts) == 0 {
		return nil, errors.New("OIDC endpoint host allowlist is empty")
	}
	allowed := make(map[string]struct{}, len(allowedHosts))
	for _, host := range allowedHosts {
		if err := validateEndpointHost(host); err != nil {
			return nil, err
		}
		allowed[strings.ToLower(host)] = struct{}{}
	}
	var transport http.RoundTripper
	if base != nil && base.Transport != nil {
		transport = base.Transport
	} else {
		dialContext, err := protectedOIDCDialContext(config.AllowedPrivateEndpointIPs)
		if err != nil {
			return nil, err
		}
		transport = &http.Transport{
			Proxy:                 nil,
			DialContext:           dialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          20,
			IdleConnTimeout:       60 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 8 * time.Second,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		}
	}
	return &http.Client{
		Transport: &boundedOIDCTransport{base: transport, allowedHosts: allowed, maxBytes: config.maxResponseBytes()},
		Timeout:   12 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("OIDC HTTP redirects are not allowed")
		},
	}, nil
}

var nonPublicOIDCEndpointPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

var currentPublicIPv6Prefix = netip.MustParsePrefix("2000::/3")

type oidcIPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type oidcConnectionDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

type oidcDialResult struct {
	connection net.Conn
	err        error
}

func protectedOIDCDialContext(privateExceptions []string) (func(context.Context, string, string) (net.Conn, error), error) {
	return protectedOIDCDialContextWithDependencies(
		privateExceptions,
		net.DefaultResolver,
		&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second},
	)
}

func protectedOIDCDialContextWithDependencies(privateExceptions []string, resolver oidcIPResolver, dialer oidcConnectionDialer) (func(context.Context, string, string) (net.Conn, error), error) {
	if resolver == nil || dialer == nil {
		return nil, errors.New("OIDC resolver and dialer are required")
	}
	allowedPrivate := make(map[netip.Addr]struct{}, len(privateExceptions))
	for _, raw := range privateExceptions {
		address, err := netip.ParseAddr(raw)
		if err != nil || address.String() != raw || address != address.Unmap() || !address.IsPrivate() {
			return nil, errors.New("OIDC private endpoint IP exception is invalid")
		}
		allowedPrivate[address] = struct{}{}
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || host == "" || port == "" {
			return nil, errors.New("OIDC provider dial address is invalid")
		}
		resolved, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		unique := make(map[netip.Addr]struct{}, len(resolved))
		addresses := make([]netip.Addr, 0, len(resolved))
		for _, candidate := range resolved {
			candidate = candidate.Unmap()
			if !isPublicOIDCEndpointIP(candidate) {
				if _, explicitlyAllowed := allowedPrivate[candidate]; !explicitlyAllowed {
					return nil, errors.New("OIDC provider resolved to a non-public address")
				}
			}
			if _, exists := unique[candidate]; exists {
				continue
			}
			unique[candidate] = struct{}{}
			addresses = append(addresses, candidate)
			if len(addresses) > 32 {
				return nil, errors.New("OIDC provider resolved to too many addresses")
			}
		}
		if len(addresses) == 0 {
			return nil, errors.New("OIDC provider resolved to no usable address")
		}
		dialContext, cancel := context.WithCancel(ctx)
		results := make(chan oidcDialResult, len(addresses))
		for _, candidate := range addresses {
			target := net.JoinHostPort(candidate.String(), port)
			go func(target string) {
				connection, dialErr := dialer.DialContext(dialContext, network, target)
				results <- oidcDialResult{connection: connection, err: dialErr}
			}(target)
		}
		var lastErr error
		for received := 0; received < len(addresses); received++ {
			result := <-results
			if result.err == nil && result.connection != nil {
				cancel()
				go closeUnusedOIDCConnections(results, len(addresses)-received-1)
				return result.connection, nil
			}
			if result.connection != nil {
				_ = result.connection.Close()
			}
			if result.err == nil {
				result.err = errors.New("OIDC provider dial returned no connection")
			}
			lastErr = result.err
		}
		cancel()
		return nil, lastErr
	}, nil
}

func closeUnusedOIDCConnections(results <-chan oidcDialResult, remaining int) {
	for index := 0; index < remaining; index++ {
		result := <-results
		if result.connection != nil {
			_ = result.connection.Close()
		}
	}
}

func isPublicOIDCEndpointIP(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() ||
		address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() ||
		address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	if address.Is6() && !currentPublicIPv6Prefix.Contains(address) {
		return false
	}
	for _, prefix := range nonPublicOIDCEndpointPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

type boundedOIDCTransport struct {
	base         http.RoundTripper
	allowedHosts map[string]struct{}
	maxBytes     int64
}

func (t *boundedOIDCTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "https" || request.URL.User != nil {
		return nil, errors.New("OIDC outbound request must use HTTPS without userinfo")
	}
	if _, ok := t.allowedHosts[strings.ToLower(request.URL.Host)]; !ok {
		return nil, errors.New("OIDC outbound request host is not allowed")
	}
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	response.Body = &maximumReadCloser{body: response.Body, remaining: t.maxBytes}
	return response, nil
}

type maximumReadCloser struct {
	body      io.ReadCloser
	remaining int64
}

func (r *maximumReadCloser) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		var probe [1]byte
		n, err := r.body.Read(probe[:])
		if n > 0 {
			return 0, errors.New("OIDC HTTP response exceeds configured size limit")
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.body.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func (r *maximumReadCloser) Close() error { return r.body.Close() }
