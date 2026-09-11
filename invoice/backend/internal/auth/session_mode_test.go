package auth

import "testing"

func TestProductionOnlyAcceptsSessionAuthentication(t *testing.T) {
	if err := EnforceProductionAuthMode("production", "session", nil); err != nil {
		t.Errorf("session mode rejected: %v", err)
	}
	for _, mode := range []string{"oidc", "mock", "", "off", "disabled", "none", "unknown"} {
		if err := EnforceProductionAuthMode("production", mode, nil); err == nil {
			t.Errorf("retired or unsafe mode accepted: %q", mode)
		}
	}
	if err := EnforceProductionAuthMode("development", "mock", nil); err != nil {
		t.Fatal(err)
	}
}
