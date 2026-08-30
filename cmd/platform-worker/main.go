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
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
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
	// XM-CRED0：文件优先（XM_SECRET_ROOT，后台写入）、env 兜底，每轮同步现解析。
	config.Sub2APISecrets, err = sub2apiSecretsFromEnv(os.Getenv, logger, config.Environment, config.Sub2APICredentialRef, config.SecretRoot)
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "sub2api_credential_ref_invalid")
		os.Exit(2)
	}
	// NewAPI 只读凭据的 Provider（XM-0038）。与 Sub2API 同一套装配、
	// 两份实例：两条采集链路各用各的凭据与登记表。
	config.NewAPISecrets, err = newapiSecretsFromEnv(os.Getenv, logger, config.Environment, config.NewAPICredentialRef, config.SecretRoot)
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "newapi_credential_ref_invalid")
		os.Exit(2)
	}
	// NewAPI 收入侧的只读 DSN 通道（XM-0044，设计稿 §3.2）。装配在进程入口：
	// 连接池连的是别人家的生产库，必须按进程持有并在退出时关闭。
	//
	// 配错**不让 worker 起不来**（一个采集通道不该拖垮心跳与别的任务），
	// 但也绝不静默降级成「没配」：那时挂上去的是一个如实报 unavailable 的
	// 降级通道，采集日志里看得见（见 degradedRevenueSource）。
	revenueSource, closeRevenue, err := newapiRevenueFromEnv(ctx, os.Getenv, logger, config.Environment, config.SecretRoot)
	if err != nil {
		logger.ErrorContext(ctx, "newapi_revenue_dsn_unavailable",
			"event", "newapi_revenue_dsn_unavailable", "module", "platform.worker",
			"environment", config.Environment, "principal_id", "worker:platform",
			"error_code", "newapi_revenue_dsn_unavailable",
			// err 已过 scrubError，口令不在里面。
			"detail", err.Error())
	}
	if closeRevenue != nil {
		defer closeRevenue()
	}
	config.FinanceNewAPIRevenue = revenueSource
	// 成本采集的账号/令牌引用来自登记簿，不能在 configFromEnv 阶段预枚举。
	// Provider 只按 Resolve 即时查 env/file；缺少 provider 保留为 nil，让
	// real 采集器按 not_supported 记录可见降级，而不是阻断心跳。
	config.FinanceCollectSecrets, err = financeSecretsFromLookup(
		os.LookupEnv,
		logger,
		config.Environment,
		config.FinanceCollectMode,
		config.FinanceCollectSecretProvider,
		config.FinanceCollectSecretRoot,
		config.FinanceCollectSecretScopes,
	)
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "finance_secret_provider_invalid")
		os.Exit(2)
	}

	// 告警投递凭据的 Provider（XM-0033）。同样装配在进程入口，
	// 告警模块只拿接口。引用没配时返回 nil，不是错误——见 alertSecretsFromEnv。
	config.AlertSecrets, err = alertSecretsFromEnv(os.Getenv, logger, config.Environment, config.AlertTelegramBotRef)
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "alert_credential_ref_invalid")
		os.Exit(2)
	}
	// 企业微信告警渠道的 Provider（XM-ALERT-WECOM）。与上面 Telegram 那条
	// 不同：走文件优先链（XM-CRED0），使 Webhook 地址能从「设置→凭据」页
	// 粘贴——见 alertWeComSecretsFromEnv 的注释。
	config.AlertWeComSecrets, err = alertWeComSecretsFromEnv(os.Getenv, logger, config.Environment, config.AlertWeComWebhookRef, config.SecretRoot)
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "alert_wecom_credential_ref_invalid")
		os.Exit(2)
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "database_pool_invalid")
		os.Exit(1)
	}
	defer pool.Close()
	// 运行时阈值由 finance current 表提供；环境变量仅在独立 lifecycle
	// bootstrap 命令中读取。若迁移/bootstrap 尚未完成，告警轮次会 fail closed，
	// 不会拿空 Findings 把既有告警恢复掉。
	config.RunwayThresholdProvider = finance.NewRunwayThresholdCurrentStore(pool, nil)
	pingCtx, cancelPing := context.WithTimeout(ctx, 10*time.Second)
	err = pool.Ping(pingCtx)
	cancelPing()
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "database_unreachable")
		os.Exit(1)
	}
	// XM-CRED0：接入模式 / 端点 / allowlist / 凭据引用由 core.connector_config
	// 每轮决定，上面从环境变量读到的 XM_SUB2API_* / XM_NEWAPI_* 只作缺省。
	// 装在这里而不是 configFromEnv：它需要连接池。
	config.ConnectorConfigs = jobs.NewPgConnectorConfigSource(pool)

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
		// XM-CRED0：下面的 *_mode / endpoint 等只是环境变量给的**缺省**；
		// 生效配置每轮从 core.connector_config 读，变化时另有
		// connector_config_applied 日志。secret_root 只打路径，不打内容。
		"connector_config_source", "database",
		"secret_root", config.SecretRoot,
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
		"finance_collect_secret_provider", config.FinanceCollectSecretProvider,
		"finance_collect_secret_scopes", len(config.FinanceCollectSecretScopes),
		// newapi 收入侧走的是只读 DSN（§3.2），与上面那条 HTTP 采集是两条通道。
		// 打出来才看得出台账里 newapi 那几行的收入是「真读了」还是「写 NULL」。
		"newapi_revenue_dsn_configured", config.FinanceNewAPIRevenue != nil,
		// 告警同理：运维必须能一眼看出这个进程会不会评估告警、会不会投递、
		// 往哪儿投。**只打渠道是否配置，不打 chat_id、不打 webhook 地址**——
		// 后者常常本身就是凭据（宪法 7 条）。
		"alert_evaluate_enabled", config.AlertEvaluateEnabled,
		"alert_evaluate_interval", config.AlertEvaluateInterval.String(),
		"alert_telegram_configured", config.AlertTelegramBotRef != "" && config.AlertTelegramChatID != "",
		"alert_webhook_configured", config.AlertWebhookURL != "",
		"alert_wecom_configured", config.AlertWeComWebhookRef != "",
		"alert_balance_threshold_minor_units", config.AlertBalanceThresholdMinorUnits,
		// AUD2 is intentionally manual-only until R2-10 and DB-role gates are
		// merged. Log policy state without emitting endpoint or credential refs.
		"audit_archive_enabled", config.AuditArchive.Enabled,
		"audit_archive_mode", string(config.AuditArchive.Mode),
		"audit_archive_scheduler_enabled", config.AuditArchive.SchedulerEnabled,
		"audit_archive_periodic_registered", jobs.AuditArchivePeriodicRegistrationAllowed(),
		// 请求量/成功率聚合（XM-REQLOG-METRICS）。mode=off 时这条任务根本不
		// 注册（见 jobs.NewClient），运维要能从这一行看出是不是这个原因。
		"reqlog_metrics_mode", string(config.ReqlogMetricsMode),
		"reqlog_metrics_mode_recognized", config.ReqlogMetricsModeRecognized,
		"reqlog_metrics_data_dir", config.ReqlogMetricsDataDir,
		"reqlog_metrics_interval", config.ReqlogMetricsInterval.String())

	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Stop(stopCtx); err != nil {
		logger.ErrorContext(stopCtx, "worker_stop_failed", "event", "worker_stop_failed", "module", "platform.worker", "error_code", "shutdown_error")
		os.Exit(1)
	}
	logger.InfoContext(context.Background(), "worker_stopped", "event", "worker_stopped", "module", "platform.worker", "environment", config.Environment, "principal_id", "worker:platform")
}
