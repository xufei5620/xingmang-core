package auth

import (
	"errors"
	"testing"
)

func TestProductionAuthRejectsMockAndPlaintextSecret(t *testing.T) {
	if err := EnforceProductionAuthMode("production", "mock", func(string) string { return "" }); !errors.Is(err, ErrProductionMockAuth) {
		t.Fatalf("mock production error=%v", err)
	}
	if err := EnforceProductionAuthMode("production", "oidc", func(name string) string {
		if name == "OIDC_CLIENT_SECRET" {
			return "leaked"
		}
		return ""
	}); !errors.Is(err, ErrPlainClientSecret) {
		t.Fatalf("plaintext secret error=%v", err)
	}
	if err := EnforceProductionAuthMode("development", "mock", nil); err != nil {
		t.Fatal(err)
	}
}

func TestClientSecretFileReaderStripsOneLineEndingOnly(t *testing.T) {
	secret, err := loadClientSecret("/run/secrets/oidc", func(string) ([]byte, error) { return []byte("  exact secret  \r\n"), nil })
	if err != nil {
		t.Fatal(err)
	}
	if secret != "  exact secret  " {
		t.Fatalf("secret was unexpectedly trimmed: %q", secret)
	}
	if _, err = loadClientSecret("", func(string) ([]byte, error) { return nil, nil }); !errors.Is(err, ErrClientSecretMissing) {
		t.Fatalf("missing file error=%v", err)
	}
	if _, err = loadClientSecret("secret", func(string) ([]byte, error) { return []byte{'x', 0, 'y'}, nil }); err == nil {
		t.Fatal("NUL secret accepted")
	}
}

func TestProductionOIDCConfigRequiresFileMFAAndEndpointAllowlist(t *testing.T) {
	base := OIDCConfig{IssuerURL: "https://identity.example", ClientID: "invoice-web", RedirectURL: "https://invoice.example/auth/callback", AdminRole: "invoice-admin"}
	if err := base.ValidateForProduction(); !errors.Is(err, ErrClientSecretMissing) {
		t.Fatalf("missing secret file error=%v", err)
	}
	base.ClientSecretFile = "/run/secrets/oidc"
	if err := base.ValidateForProduction(); err == nil {
		t.Fatal("missing MFA constraints accepted")
	}
	base.RequiredAdminACR = "urn:invoice:mfa"
	base.RequiredAdminAMR = []string{"otp"}
	if err := base.ValidateForProduction(); err == nil {
		t.Fatal("missing endpoint allowlist accepted")
	}
	base.AllowedEndpointHosts = []string{"identity.example"}
	if err := base.ValidateForProduction(); err == nil {
		t.Fatal("missing post-logout redirect accepted")
	}
	base.PostLogoutRedirectURL = "https://invoice.example/"
	if err := base.ValidateForProduction(); err == nil {
		t.Fatal("missing back-channel logout requirement accepted")
	}
	base.RequireBackchannelLogout = true
	if err := base.ValidateForProduction(); err != nil {
		t.Fatal(err)
	}
}
