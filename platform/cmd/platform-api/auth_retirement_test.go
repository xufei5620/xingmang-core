package main

import "testing"

func TestProductionDefaultsToLocalAfterOIDCRetirement(t *testing.T) {
	cfg, err := authConfigFromEnv(func(string) string { return "" }, "production")
	if err != nil || cfg.Mode != authModeLocal {
		t.Fatalf("production must default to local account sessions: mode=%q err=%v", cfg.Mode, err)
	}
}

func TestRetiredAuthenticationConfigFailsClosed(t *testing.T) {
	for _, settings := range []map[string]string{
		{"XM_AUTH_MODE": "oidc"},
		{"XM_AUTH_MODE": "oidc", "XM_OIDC_ISSUER": "https://auth.example.test/realm", "XM_OIDC_AUDIENCE": "staff"},
		{"XM_AUTH_MODE": "local", "XM_OIDC_ROLE_SCOPES": `{"ops":["ops.read"]}`},
		{"XM_AUTH_MODE": "local", "XM_INVOICE_CONSOLE_ASSERTION_ENABLED": "true"},
	} {
		t.Run(settings["XM_AUTH_MODE"]+settings["XM_OIDC_ROLE_SCOPES"]+settings["XM_INVOICE_CONSOLE_ASSERTION_ENABLED"], func(t *testing.T) {
			if _, err := authConfigFromEnv(func(k string) string { return settings[k] }, "production"); err == nil {
				t.Fatal("retired authentication configuration was silently accepted")
			}
		})
	}
}
