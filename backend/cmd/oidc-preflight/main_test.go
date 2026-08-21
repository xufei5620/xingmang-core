package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"invoice-system/backend/internal/auth"
)

func TestRunEmitsOnlySafeProviderEvidence(t *testing.T) {
	const (
		issuer = "https://identity.example/realms/invoice"
		secret = "/run/secrets/do-not-print-oidc-secret"
	)
	values := map[string]string{
		"OIDC_ISSUER_URL":             issuer,
		"OIDC_ALLOWED_ENDPOINT_HOSTS": "identity.example",
		"OIDC_ALLOWED_SIGNING_ALGS":   "RS256",
		"OIDC_CLIENT_SECRET_FILE":     secret,
	}
	var output bytes.Buffer
	err := run(context.Background(), &output, func(name string) string { return values[name] }, func(_ context.Context, config auth.OIDCConfig) (auth.ProviderPreflightReport, error) {
		if config.ClientSecretFile != secret || config.IssuerURL != issuer || !config.RequireBackchannelLogout {
			t.Fatalf("unexpected preflight config: %+v", config)
		}
		return auth.ProviderPreflightReport{
			Status: "ok", ConfigurationFingerprint: "sha256:fixture", DiscoveryOnly: true,
			ClientSecretFileConfigured: true, AllowedSigningAlgorithms: []string{"RS256"},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), issuer) || strings.Contains(output.String(), secret) || strings.Contains(output.String(), "do-not-print") {
		t.Fatalf("safe report leaked provider configuration: %s", output.String())
	}
	if !strings.Contains(output.String(), `"status": "ok"`) || !strings.Contains(output.String(), `"discovery_only": true`) {
		t.Fatalf("missing safe report fields: %s", output.String())
	}
}

func TestRunDoesNotWriteProviderErrorsOrSecrets(t *testing.T) {
	const marker = "do-not-print-provider-token"
	values := map[string]string{
		"OIDC_ISSUER_URL":             "https://identity.example/realms/invoice",
		"OIDC_ALLOWED_ENDPOINT_HOSTS": "identity.example",
	}
	var output bytes.Buffer
	err := run(context.Background(), &output, func(name string) string { return values[name] }, func(context.Context, auth.OIDCConfig) (auth.ProviderPreflightReport, error) {
		return auth.ProviderPreflightReport{}, errors.New("provider response contained " + marker)
	})
	if err == nil || output.Len() != 0 {
		t.Fatalf("failed preflight wrote output: err=%v output=%q", err, output.String())
	}
	if message := publicFailureMessage(err); strings.Contains(message, marker) || message != "oidc-preflight: provider contract rejected" {
		t.Fatalf("unsafe public failure message: %q", message)
	}
}

func TestConfigRejectsIssuerQueryAndDoesNotRequireSecretFile(t *testing.T) {
	values := map[string]string{
		"OIDC_ISSUER_URL":             "https://identity.example/realms/invoice?access_token=do-not-print",
		"OIDC_ALLOWED_ENDPOINT_HOSTS": "identity.example",
	}
	_, _, err := configFromEnvironment(func(name string) string { return values[name] })
	if err == nil || strings.Contains(err.Error(), "access_token") || strings.Contains(err.Error(), "do-not-print") {
		t.Fatalf("unsafe issuer accepted or echoed: %v", err)
	}
	values["OIDC_ISSUER_URL"] = "https://identity.example/realms/invoice"
	config, _, err := configFromEnvironment(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if config.ClientSecretFile != "" {
		t.Fatal("discovery-only preflight unexpectedly required a client secret file")
	}
}
