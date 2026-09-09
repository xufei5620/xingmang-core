package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"invoice-system/backend/internal/auth"
)

type environment func(string) string
type providerChecker func(context.Context, auth.OIDCConfig) (auth.ProviderPreflightReport, error)

func main() {
	if err := run(context.Background(), os.Stdout, os.Getenv, auth.PreflightOIDCProvider); err != nil {
		fmt.Fprintln(os.Stderr, publicFailureMessage(err))
		os.Exit(1)
	}
}

func run(ctx context.Context, output io.Writer, getenv environment, check providerChecker) error {
	config, timeout, err := configFromEnvironment(getenv)
	if err != nil {
		return err
	}
	if check == nil {
		return errors.New("OIDC provider checker is unavailable")
	}
	checkContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	report, err := check(checkContext, config)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func configFromEnvironment(getenv environment) (auth.OIDCConfig, time.Duration, error) {
	if getenv == nil {
		return auth.OIDCConfig{}, 0, errors.New("environment is unavailable")
	}
	maximumResponseBytes, err := boundedInt64(getenv("OIDC_MAX_HTTP_RESPONSE_BYTES"), 1<<20, 64<<10, 4<<20)
	if err != nil {
		return auth.OIDCConfig{}, 0, errors.New("OIDC_MAX_HTTP_RESPONSE_BYTES is invalid")
	}
	timeout, err := boundedDuration(getenv("OIDC_PREFLIGHT_TIMEOUT"), 30*time.Second, time.Second, 2*time.Minute)
	if err != nil {
		return auth.OIDCConfig{}, 0, errors.New("OIDC_PREFLIGHT_TIMEOUT is invalid")
	}
	clientSecretFile := getenv("OIDC_CLIENT_SECRET_FILE")
	if clientSecretFile != "" && (!absoluteSecretPath(clientSecretFile) || strings.ContainsAny(clientSecretFile, "\r\n\x00")) {
		return auth.OIDCConfig{}, 0, errors.New("OIDC_CLIENT_SECRET_FILE must be an absolute control-free path when configured")
	}
	config := auth.OIDCConfig{
		IssuerURL:                 getenv("OIDC_ISSUER_URL"),
		ClientSecretFile:          clientSecretFile,
		AllowedEndpointHosts:      csv(getenv("OIDC_ALLOWED_ENDPOINT_HOSTS")),
		AllowedPrivateEndpointIPs: csv(getenv("OIDC_ALLOWED_PRIVATE_ENDPOINT_IPS")),
		AllowedSigningAlgs:        csvWithDefault(getenv("OIDC_ALLOWED_SIGNING_ALGS"), "RS256"),
		TokenEndpointAuthMethod:   valueWithDefault(getenv("OIDC_TOKEN_AUTH_METHOD"), "client_secret_basic"),
		RequireBackchannelLogout:  true,
		MaximumHTTPResponseBytes:  maximumResponseBytes,
	}
	if err = config.ValidateProviderPreflight(); err != nil {
		return auth.OIDCConfig{}, 0, err
	}
	return config, timeout, nil
}

func absoluteSecretPath(path string) bool {
	// Production runs in Linux containers, while the same source is tested on
	// Windows. Accept the native absolute form plus the unambiguous Linux root
	// form so tests exercise the exact mounted-secret path.
	return filepath.IsAbs(path) || strings.HasPrefix(path, "/")
}

func csv(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func csvWithDefault(raw, fallback string) []string {
	if strings.TrimSpace(raw) == "" {
		raw = fallback
	}
	return csv(raw)
}

func valueWithDefault(raw, fallback string) string {
	if raw == "" {
		return fallback
	}
	return raw
}

func boundedInt64(raw string, fallback, minimum, maximum int64) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < minimum || value > maximum {
		return 0, errors.New("value is outside the allowed range")
	}
	return value, nil
}

func boundedDuration(raw string, fallback, minimum, maximum time.Duration) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || value < minimum || value > maximum {
		return 0, errors.New("duration is outside the allowed range")
	}
	return value, nil
}

func publicFailureMessage(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "oidc-preflight: provider check timed out"
	}
	return "oidc-preflight: provider contract rejected"
}
