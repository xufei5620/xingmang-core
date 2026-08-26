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
	logger.InfoContext(ctx, "worker_started", "event", "worker_started", "module", "platform.worker", "environment", config.Environment, "principal_id", "worker:platform")

	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Stop(stopCtx); err != nil {
		logger.ErrorContext(stopCtx, "worker_stop_failed", "event", "worker_stop_failed", "module", "platform.worker", "error_code", "shutdown_error")
		os.Exit(1)
	}
	logger.InfoContext(context.Background(), "worker_stopped", "event", "worker_stopped", "module", "platform.worker", "environment", config.Environment, "principal_id", "worker:platform")
}
