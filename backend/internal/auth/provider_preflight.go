package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"strings"
)

// ProviderPreflightReport contains only non-secret pass/fail evidence. Raw
// issuer and endpoint URLs are deliberately excluded from this type so it is
// safe to emit from a deployment tool.
type ProviderPreflightReport struct {
	Status                      string   `json:"status"`
	ConfigurationFingerprint    string   `json:"configuration_fingerprint"`
	DiscoveryOnly               bool     `json:"discovery_only"`
	ClientSecretFileConfigured  bool     `json:"client_secret_file_configured"`
	AllowedSigningAlgorithms    []string `json:"allowed_signing_algorithms"`
	IssuerHTTPS                 bool     `json:"issuer_https"`
	EndpointHostAllowlist       bool     `json:"endpoint_host_allowlist"`
	EndpointAddressPolicy       bool     `json:"endpoint_address_policy"`
	AuthorizationEndpoint       bool     `json:"authorization_endpoint"`
	TokenEndpoint               bool     `json:"token_endpoint"`
	UserInfoEndpoint            bool     `json:"userinfo_endpoint"`
	JWKSEndpoint                bool     `json:"jwks_endpoint"`
	EndSessionEndpoint          bool     `json:"end_session_endpoint"`
	AuthorizationCodeFlow       bool     `json:"authorization_code_flow"`
	PKCES256                    bool     `json:"pkce_s256"`
	BackchannelLogoutSupported  bool     `json:"backchannel_logout_supported"`
	BackchannelSessionSupported bool     `json:"backchannel_logout_session_supported"`
	BoundedResponses            bool     `json:"bounded_responses"`
	HTTPRedirectsRejected       bool     `json:"http_redirects_rejected"`
}

// PreflightOIDCProvider checks a provider's discovery and JWKS contract using
// the exact strict HTTP and metadata validation path used by NewOIDCClient.
// It never reads ClientSecretFile and performs no token exchange.
func PreflightOIDCProvider(ctx context.Context, config OIDCConfig) (ProviderPreflightReport, error) {
	return preflightOIDCProvider(ctx, config, nil)
}

func preflightOIDCProvider(ctx context.Context, config OIDCConfig, baseClient *http.Client) (ProviderPreflightReport, error) {
	if err := config.ValidateProviderPreflight(); err != nil {
		return ProviderPreflightReport{}, err
	}
	client, err := strictOIDCHTTPClient(baseClient, config)
	if err != nil {
		return ProviderPreflightReport{}, err
	}
	document, algorithms, err := discoverProviderWithRetry(ctx, client, config)
	if err != nil {
		return ProviderPreflightReport{}, err
	}
	return ProviderPreflightReport{
		Status:                      "ok",
		ConfigurationFingerprint:    providerConfigurationFingerprint(config),
		DiscoveryOnly:               true,
		ClientSecretFileConfigured:  strings.TrimSpace(config.ClientSecretFile) != "",
		AllowedSigningAlgorithms:    append([]string(nil), algorithms...),
		IssuerHTTPS:                 true,
		EndpointHostAllowlist:       true,
		EndpointAddressPolicy:       true,
		AuthorizationEndpoint:       document.AuthorizationEndpoint != "",
		TokenEndpoint:               document.TokenEndpoint != "",
		UserInfoEndpoint:            document.UserInfoEndpoint != "",
		JWKSEndpoint:                document.JWKSURI != "",
		EndSessionEndpoint:          document.EndSessionEndpoint != "",
		AuthorizationCodeFlow:       slices.Contains(document.ResponseTypesSupported, "code"),
		PKCES256:                    slices.Contains(document.CodeChallengeMethodsSupported, "S256"),
		BackchannelLogoutSupported:  document.BackchannelLogoutSupported,
		BackchannelSessionSupported: document.BackchannelLogoutSessionSupported,
		BoundedResponses:            true,
		HTTPRedirectsRejected:       true,
	}, nil
}

func providerConfigurationFingerprint(config OIDCConfig) string {
	hosts := append([]string(nil), config.endpointHosts()...)
	for index := range hosts {
		hosts[index] = strings.ToLower(hosts[index])
	}
	slices.Sort(hosts)
	privateIPs := append([]string(nil), config.AllowedPrivateEndpointIPs...)
	slices.Sort(privateIPs)
	algorithms := append([]string(nil), config.signingAlgorithms()...)
	slices.Sort(algorithms)
	canonical := config.IssuerURL + "\n" + strings.Join(hosts, ",") + "\n" +
		strings.Join(privateIPs, ",") + "\n" + strings.Join(algorithms, ",") + "\n" +
		config.tokenAuthMethod() + "\nbackchannel-required"
	digest := sha256.Sum256([]byte(canonical))
	return "sha256:" + hex.EncodeToString(digest[:])
}
