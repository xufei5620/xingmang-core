package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
)

func main() {
	migrate := flag.Bool("migrate", false, "apply River migrations and exit")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stopSignal := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignal()

	config, err := configFromEnv(os.Getenv)
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "worker_config_invalid")
		os.Exit(2)
	}
	databaseURL, err := databaseURLFromEnv(ctx, os.Getenv, logger)
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "database_url_invalid")
		os.Exit(2)
	}
	// Sub2API 只读凭据的 Provider（XM-0017）。装配在进程入口，任务层只拿接口。
	// 引用没配时返回 nil，不是错误——见 sub2apiSecretsFromEnv 的注释。
	config.Sub2APISecrets, err = sub2apiSecretsFromEnv(os.Getenv, logger, config.Environment, config.Sub2APICredentialRef)
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "sub2api_credential_ref_invalid")
		os.Exit(2)
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "database_pool_invalid")
		os.Exit(1)
	}
	defer pool.Close()
	pingCtx, cancelPing := context.WithTimeout(ctx, 10*time.Second)
	err = pool.Ping(pingCtx)
	cancelPing()
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "database_unreachable")
		os.Exit(1)
	}

	if *migrate {
		if err := jobs.Migrate(ctx, pool, logger); err != nil {
			logger.ErrorContext(ctx, "worker_migration_failed", "event", "worker_migration_failed", "module", "platform.worker", "error_code", "migration_failed")
			os.Exit(1)
		}
		logger.InfoContext(ctx, "worker_migration_completed", "event", "worker_migration_completed", "module", "platform.worker", "environment", config.Environment, "principal_id", "worker:platform")
		return
	}

	client, err := jobs.NewClient(pool, config)
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "worker_config_invalid")
		os.Exit(2)
	}
	if err := client.Start(ctx); err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "worker_start_error")
		os.Exit(1)
	}
	// 启动时就把采集配置摊开：运维必须能一眼看出这个进程写进看板的数字
	// 是 Fake 产的还是真实上游来的，而不是等发现数字不对再回来翻配置。
	logger.InfoContext(ctx, "worker_started", "event", "worker_started", "module", "platform.worker",
		"environment", config.Environment, "principal_id", "worker:platform",
		"sub2api_sync_enabled", config.Sub2APISyncEnabled,
		"sub2api_mode", string(config.Sub2APIMode),
		"sub2api_source", config.Sub2APIInstanceID,
		"sub2api_sync_interval", config.Sub2APISyncInterval.String())

	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Stop(stopCtx); err != nil {
		logger.ErrorContext(stopCtx, "worker_stop_failed", "event", "worker_stop_failed", "module", "platform.worker", "error_code", "shutdown_error")
		os.Exit(1)
	}
	logger.InfoContext(context.Background(), "worker_stopped", "event", "worker_stopped", "module", "platform.worker", "environment", config.Environment, "principal_id", "worker:platform")
}
