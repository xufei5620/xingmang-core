package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// databaseURLFromEnv keeps database credentials behind the existing
// SecretProvider boundary when a non-development worker needs a password.
// DATABASE_URL may contain an inline password only for an explicitly named
// development environment, and the value is never logged.
func databaseURLFromEnv(ctx context.Context, getenv func(string) string, logger *slog.Logger) (string, error) {
	if getenv == nil {
		return "", fmt.Errorf("environment reader is required")
	}
	raw := strings.TrimSpace(getenv("DATABASE_URL"))
	if raw == "" {
		return "", fmt.Errorf("DATABASE_URL is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" {
		return "", fmt.Errorf("DATABASE_URL is not a valid postgres URL")
	}

	environment := strings.TrimSpace(getenv("ENVIRONMENT"))
	refText := strings.TrimSpace(getenv("DATABASE_PASSWORD_REF"))
	hasInlinePassword := false
	if parsed.User != nil {
		_, hasInlinePassword = parsed.User.Password()
	}
	if hasInlinePassword && (environment != "development" || refText != "") {
		return "", fmt.Errorf("remove inline database password before using DATABASE_PASSWORD_REF; inline passwords are development-only")
	}
	if refText == "" {
		return raw, nil
	}
	if parsed.User == nil {
		return "", fmt.Errorf("DATABASE_URL must include a user when DATABASE_PASSWORD_REF is set")
	}

	ref, err := secrets.ParseCredentialRef(refText)
	if err != nil {
		return "", fmt.Errorf("DATABASE_PASSWORD_REF: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	provider, err := secrets.NewEnvProvider(
		map[string]string{ref.String(): "DATABASE_PASSWORD"},
		secrets.WithLookup(func(name string) (string, bool) {
			value := getenv(name)
			return value, value != ""
		}),
	)
	if err != nil {
		return "", err
	}
	audited := secrets.NewAudited(provider, secrets.NewSlogRecorder(logger), environment)
	value, err := audited.Resolve(secrets.WithCaller(ctx, "worker:platform"), ref, "platform-worker database connection")
	if err != nil {
		return "", fmt.Errorf("resolve database password: %w", err)
	}
	parsed.User = url.UserPassword(parsed.User.Username(), value.Reveal())
	return parsed.String(), nil
}
