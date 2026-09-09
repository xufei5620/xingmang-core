package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/pgdsn"
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
	environment := strings.TrimSpace(getenv("ENVIRONMENT"))
	refText := strings.TrimSpace(getenv("DATABASE_PASSWORD_REF"))

	// 密码何时必须走 CredentialRef：非开发环境，或者已经显式配了 ref。
	// 开发环境且没配 ref 时允许内联密码（见 README 的本地调试流程）。
	requireManagedPassword := environment != "development" || refText != ""

	// 校验交给 pgdsn：它不自己解析 URL 判断「有没有内联密码」，而是问 pgx
	// 实际会用什么配置。?password= / ?host= 这类 query 参数会覆盖 DSN 的
	// 表面声明，自己解析一定漏（见 internal/platform/pgdsn 的包注释）。
	if err := pgdsn.Validate(raw, requireManagedPassword); err != nil {
		return "", err
	}
	if refText == "" {
		return raw, nil
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
	return pgdsn.WithPassword(raw, value.Reveal())
}
