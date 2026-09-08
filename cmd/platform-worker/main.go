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
	"github.com/xufei5620/xingmang-platform/internal/platform/approval"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
)

func main() {
	migrate := flag.Bool("migrate", false, "apply River migrations and exit")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	// 同时设成全局默认（XM-LOG-INJECTED）：注入 logger 只覆盖得到拿得到它的
	// 调用点，而 `WriteJSON` 的编码失败那一处没有 ctx 也没有 logger 参数
	// （它有近百个调用点，为一条极少发生的日志改签名不划算）。不设默认的话
	// 那一条会以文本格式写 stderr，与其余 JSON/stdout 的日志分家。
	//
	// 安全性已核对：全仓没有任何地方用标准 `log` 包，所以这一行不会改变
	// 除 slog 之外的任何输出。
	slog.SetDefault(logger)
	ctx, stopSignal := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSignal()

	config, err := configFromEnv(os.Getenv)
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "worker_config_invalid", "err", err.Error())
		os.Exit(2)
	}
	databaseURL, err := databaseURLFromEnv(ctx, os.Getenv, logger)
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "database_url_invalid", "err", err.Error())
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
	// XM-ASSURE1-core：探测专用凭据（core.connector_config.probe_credential_ref）
	// 的通用 Provider——与上面几条不同，引用是"因平台而异、存在数据库里"的，
	// 见 assuranceProbeSecretsFromEnv 的注释。
	config.AssuranceProbeSecrets, err = assuranceProbeSecretsFromEnv(os.Getenv, logger, config.Environment, config.SecretRoot)
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "assurance_probe_secret_provider_invalid")
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

	// Infini 卡片同步（XM-CARD2）：承担异步开卡的轮询与不确定态的对账收敛。
	// 默认关闭（XM_CARDS_MODE 未设或 off 时 syncer 为 nil），装配失败即拒绝
	// 启动——一个「以为在收敛不确定态、其实每轮都失败」的 worker，
	// 会让那些可能已经花掉的钱永远停在不确定态而没人知道。
	cardSyncer, err := buildCardSyncer(pool,
		cardsSecretProvider(config.SecretRoot, config.Environment, logger),
		config.Environment, os.Getenv)
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed",
			"module", "platform.worker", "error_code", "cards_config_invalid", "err", err.Error())
		os.Exit(2)
	}
	if cardSyncer != nil {
		config.CardSyncer = cardSyncer
		config.CardSyncEnabled = true
	}

	// 接码巡检（XM-SMS2 #7）：只读的连接测试 + 余额快照，一分钱都不花。
	// 默认关闭（XM_SMS_MODE 未设或 off 时 prober 为 nil）；装配失败即拒绝
	// 启动——一个「以为在盯着余额和凭据、其实每轮都失败」的 worker，会让
	// 「密钥过期」与「余额见底」这两件事在买号失败那一刻才被发现。
	smsProber, err := buildSMSProber(pool,
		smsSecretProvider(config.SecretRoot, config.Environment, logger),
		config.Environment, os.Getenv)
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed",
			"module", "platform.worker", "error_code", "sms_config_invalid", "err", err.Error())
		os.Exit(2)
	}
	if smsProber != nil {
		config.SMSProber = smsProber
		config.SMSProbeEnabled = true
	}

	if *migrate {
		if err := jobs.Migrate(ctx, pool, logger); err != nil {
			logger.ErrorContext(ctx, "worker_migration_failed", "event", "worker_migration_failed", "module", "platform.worker", "error_code", "migration_failed")
			os.Exit(1)
		}
		logger.InfoContext(ctx, "worker_migration_completed", "event", "worker_migration_completed", "module", "platform.worker", "environment", config.Environment, "principal_id", "worker:platform")
		return
	}

	// 审批中心（XM-0030c）：过期清理任务要靠它把过期的 PENDING 推到 EXPIRED，
	// 并写下队列观测——那条观测是 alerts 的 approval.pending.too_long 规则的
	// **唯一输入**。为 nil 时任务不注册（jobs.NewClient 里两个条件都要满足）。
	config.Approvals = approval.NewService(
		approval.NewPgStore(pool, config.Environment, nil),
		approval.DefaultPolicy(),
		nil,
	)

	client, err := jobs.NewClient(pool, config)
	if err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "worker_config_invalid", "err", err.Error())
		os.Exit(2)
	}
	if err := client.Start(ctx); err != nil {
		logger.ErrorContext(ctx, "worker_start_failed", "event", "worker_start_failed", "module", "platform.worker", "error_code", "worker_start_error", "err", err.Error())
		os.Exit(1)
	}
	// 启动时就把采集配置摊开：运维必须能一眼看出这个进程写进看板的数字
	// 是 Fake 产的还是真实上游来的，而不是等发现数字不对再回来翻配置。
	//
	// 整条属性由 workerStartupAttrs 组装（XM-OPS-TRUTH），main() 只负责打出去。
	// **不要在这里就地补字段**：缺席断言（不许再出现裸键 sub2api_mode /
	// newapi_mode，它装的是 env 缺省，2026-09-08 就是这么读错的）钉的是那个
	// 函数的返回值；字段留在 main() 里就等于绕过了闸——闸必须架在打出去的
	// 那一行上，不是架在半成品上。要加字段请改
	// cmd/platform-worker/startup_log.go。
	logger.InfoContext(ctx, "worker_started", workerStartupAttrs(config)...)

	<-ctx.Done()
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Stop(stopCtx); err != nil {
		logger.ErrorContext(stopCtx, "worker_stop_failed", "event", "worker_stop_failed", "module", "platform.worker", "error_code", "shutdown_error")
		os.Exit(1)
	}
	logger.InfoContext(context.Background(), "worker_stopped", "event", "worker_stopped", "module", "platform.worker", "environment", config.Environment, "principal_id", "worker:platform")
}
