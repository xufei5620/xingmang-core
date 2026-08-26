package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/xufei5620/xingmang-platform/internal/platform/pgdsn"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// legacyDatabaseURLEnv 是本命令早期使用的变量名。
//
// 保留它是为了不打断已有的本地流程与 docs/modules/httpapi/RUNBOOK.md，
// 但它和 DATABASE_URL 走**同一条**校验路径——旧名字不等于旧规矩。
const legacyDatabaseURLEnv = "XM_DATABASE_URL"

// databaseURLFromEnv 组装 platform-api 的数据库连接串。
//
// 与 cmd/platform-worker/database.go 是同一套纪律，刻意保持同构：两个进程连
// 同一个库，如果只有一边挡内联密码，另一边就是现成的绕过口子（宪法 7 条）。
//
// 规矩：
//   - 非开发环境，或已显式配置 DATABASE_PASSWORD_REF 时，密码只能来自
//     CredentialRef；DSN 内联密码、?password= 查询参数、PGPASSWORD、~/.pgpass
//     一律拒绝（判定由 pgdsn 询问 pgx 实际行为得出，不自己解析 URL）。
//   - 明文密码只在返回值里出现一次，绝不进日志（由 secrets.Audited 保证）。
func databaseURLFromEnv(ctx context.Context, getenv func(string) string, logger *slog.Logger) (string, error) {
	if getenv == nil {
		return "", fmt.Errorf("environment reader is required")
	}
	raw := strings.TrimSpace(getenv("DATABASE_URL"))
	usedEnv := "DATABASE_URL"
	if raw == "" {
		raw = strings.TrimSpace(getenv(legacyDatabaseURLEnv))
		usedEnv = legacyDatabaseURLEnv
	}
	if raw == "" {
		return "", fmt.Errorf("DATABASE_URL is required")
	}
	environment := strings.TrimSpace(getenv("ENVIRONMENT"))
	refText := strings.TrimSpace(getenv("DATABASE_PASSWORD_REF"))

	// 密码何时必须走 CredentialRef：非开发环境，或者已经显式配了 ref。
	// 开发环境且没配 ref 时允许内联密码（见 README 的本地调试流程）。
	requireManagedPassword := environment != "development" || refText != ""

	if err := pgdsn.Validate(raw, requireManagedPassword); err != nil {
		return "", fmt.Errorf("%s: %w", usedEnv, err)
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
	// 登记表显式且只有一条：ref → DATABASE_PASSWORD。ADR-014 禁止静默回退到
	// 其他数据源，所以这里不做「找不到就读别的变量」的兜底。
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
	value, err := audited.Resolve(secrets.WithCaller(ctx, "api:platform"), ref, "platform-api database connection")
	if err != nil {
		return "", fmt.Errorf("resolve database password: %w", err)
	}
	return pgdsn.WithPassword(raw, value.Reveal())
}
