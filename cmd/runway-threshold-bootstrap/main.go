// Command runway-threshold-bootstrap imports the deployment-time runway
// environment values into finance.runway_threshold_config exactly once.
//
// It is a lifecycle command, not an HTTP/API write path. Re-running with the
// same values is idempotent; a different existing row is rejected by the
// finance store.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/buildinfo"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/pgdsn"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

var getenv = os.Getenv

func main() {
	flag.Parse()
	command := flag.Arg(0)
	if command == "" {
		command = "up"
	}
	if command == "version" {
		fmt.Println(buildinfo.String())
		return
	}
	if command != "up" {
		fmt.Fprintln(os.Stderr, "用法: runway-threshold-bootstrap [up|version]")
		os.Exit(2)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil)).With(
		slog.String("service", "runway-threshold-bootstrap"),
		slog.String("build", buildinfo.String()),
	)
	ctx := context.Background()
	environment := strings.TrimSpace(getenv("ENVIRONMENT"))
	if environment == "" {
		logger.Error("缺少 ENVIRONMENT")
		os.Exit(2)
	}
	thresholds, err := finance.ParseRunwayThresholds(
		getenv("XM_FINANCE_RUNWAY_WARN_DAYS"), getenv("XM_FINANCE_RUNWAY_CRIT_DAYS"),
	)
	if err != nil {
		logger.Error("阈值环境变量无效", slog.Any("err", err))
		os.Exit(2)
	}
	dsn, err := databaseURLFromEnv(ctx, getenv, logger)
	if err != nil {
		logger.Error("数据库连接配置无效", slog.Any("err", err))
		os.Exit(2)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		logger.Error("数据库连接池创建失败", slog.Any("err", err))
		os.Exit(1)
	}
	defer pool.Close()
	store := finance.NewRunwayThresholdStore(pool, nil)
	result, err := store.Bootstrap(ctx, finance.BootstrapRunwayThresholdInput{
		Environment: environment,
		Thresholds:  thresholds,
		Actor:       "platform-lifecycle:runway-threshold-bootstrap",
		Reason:      "import existing runway environment thresholds",
		RequestID:   uuid.NewString(),
	})
	if err != nil {
		logger.Error("阈值 bootstrap 失败", slog.Any("err", err))
		os.Exit(1)
	}
	// 只输出可核对的非敏感事实；不输出 DSN、CredentialRef 或理由全文。
	fmt.Printf("environment=%s revision=%d critical_days=%d warning_days=%d serious_days=%d\n",
		result.Environment, result.Revision, result.Thresholds.CriticalDays,
		result.Thresholds.WarningDays, result.Thresholds.SeriousDays)
}

func databaseURLFromEnv(ctx context.Context, read func(string) string, logger *slog.Logger) (string, error) {
	raw := strings.TrimSpace(read("DATABASE_URL"))
	if raw == "" {
		raw = strings.TrimSpace(read("XM_DATABASE_URL"))
	}
	if raw == "" {
		return "", fmt.Errorf("DATABASE_URL is required")
	}
	environment := strings.TrimSpace(read("ENVIRONMENT"))
	refText := strings.TrimSpace(read("DATABASE_PASSWORD_REF"))
	if err := pgdsn.Validate(raw, environment != "development" || refText != ""); err != nil {
		return "", err
	}
	if refText == "" {
		return raw, nil
	}
	ref, err := secrets.ParseCredentialRef(refText)
	if err != nil {
		return "", err
	}
	provider, err := secrets.NewEnvProvider(map[string]string{ref.String(): "DATABASE_PASSWORD"}, secrets.WithLookup(func(name string) (string, bool) {
		value := read(name)
		return value, value != ""
	}))
	if err != nil {
		return "", err
	}
	if logger == nil {
		logger = slog.Default()
	}
	audited := secrets.NewAudited(provider, secrets.NewSlogRecorder(logger), environment)
	value, err := audited.Resolve(secrets.WithCaller(ctx, "lifecycle:runway-threshold-bootstrap"), ref, "runway bootstrap database connection")
	if err != nil {
		return "", err
	}
	return pgdsn.WithPassword(raw, value.Reveal())
}
