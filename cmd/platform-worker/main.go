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
	// 告警投递凭据的 Provider（XM-0033）。同样装配在进程入口，
	// 告警模块只拿接口。引用没配时返回 nil，不是错误——见 alertSecretsFromEnv。
	config.AlertSecrets, err = alertSecretsFromEnv(os.Getenv, logger, config.Environment, config.AlertTelegramBotRef)
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "alert_credential_ref_invalid")
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
		"sub2api_sync_interval", config.Sub2APISyncInterval.String(),
		// NewAPI 同理（XM-0035）：运维必须能一眼看出这批数字是 Fake 产的
		// 还是真实上游来的，而不是等发现数字不对再回来翻配置。
		"newapi_sync_enabled", config.NewAPISyncEnabled,
		"newapi_mode", string(config.NewAPIMode),
		"newapi_source", config.NewAPIInstanceID,
		"newapi_sync_interval", config.NewAPISyncInterval.String(),
		// 成本采集同理（XM-0037b）。多一条 secrets_configured：real 模式还需要
		// 一个能解析**登记簿里那些引用**的 SecretProvider，而那些引用在进程
		// 启动时还不知道（它们在库里，由 Action 维护），所以现阶段它必然是
		// false——把这个事实打在启动日志里，比让人配完 real 再去猜为什么
		// 每个账号都报 not_supported 强。
		"finance_collect_enabled", config.FinanceCollectEnabled,
		"finance_collect_mode", string(config.FinanceCollectMode),
		"finance_collect_source", config.FinanceCollectInstanceID,
		"finance_collect_interval", config.FinanceCollectInterval.String(),
		"finance_collect_allowlist_size", len(config.FinanceCollectTargetAllowlist),
		"finance_collect_secrets_configured", config.FinanceCollectSecrets != nil,
		// 告警同理：运维必须能一眼看出这个进程会不会评估告警、会不会投递、
		// 往哪儿投。**只打渠道是否配置，不打 chat_id、不打 webhook 地址**——
		// 后者常常本身就是凭据（宪法 7 条）。
		"alert_evaluate_enabled", config.AlertEvaluateEnabled,
		"alert_evaluate_interval", config.AlertEvaluateInterval.String(),
		"alert_telegram_configured", config.AlertTelegramBotRef != "" && config.AlertTelegramChatID != "",
		"alert_webhook_configured", config.AlertWebhookURL != "",
		"alert_balance_threshold_minor_units", config.AlertBalanceThresholdMinorUnits)

	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Stop(stopCtx); err != nil {
		logger.ErrorContext(stopCtx, "worker_stop_failed", "event", "worker_stop_failed", "module", "platform.worker", "error_code", "shutdown_error")
		os.Exit(1)
	}
	logger.InfoContext(context.Background(), "worker_stopped", "event", "worker_stopped", "module", "platform.worker", "environment", config.Environment, "principal_id", "worker:platform")
}
