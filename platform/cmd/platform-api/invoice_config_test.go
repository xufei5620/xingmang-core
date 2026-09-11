package main

import "testing"

func TestUnifiedProductionCannotStartWithMockInvoice(t *testing.T) {
	for _, tc := range []struct {
		name, environment, staffMode, invoiceEnv, invoiceMode string
		wantErr                                               bool
	}{
		{"production", "production", "local", "production", "session", false},
		{"missing-env", "production", "local", "", "session", true},
		{"mock-env", "production", "local", "development", "mock", true},
		{"mock-auth", "production", "local", "production", "mock", true},
		{"old-auth", "production", "local", "production", "oidc", true},
		{"header-staff", "production", "dev-header", "production", "session", true},
		{"local-development", "development", "local", "development", "mock", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"APP_ENV": tc.invoiceEnv, "AUTH_MODE": tc.invoiceMode}
			err := validateUnifiedModes(tc.environment, tc.staffMode, func(k string) string { return env[k] })
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantError=%v", err, tc.wantErr)
			}
		})
	}
}
