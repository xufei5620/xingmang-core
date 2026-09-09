package main

import "github.com/xufei5620/xingmang-platform/internal/platform/jobs"

// workerStartupAttrs 是 worker_started 那一条日志的**全部**属性。
//
// 它整条抽在这里、main() 只负责 logger.InfoContext(ctx, "worker_started",
// workerStartupAttrs(config)...)，是为了让「哪些字段会被打出去」这件事可测。
// 早先只抽走了接入模式那几个字段（connectorModeStartupAttrs），于是缺席断言
// 钉的是辅助函数的返回值，而不是真正被打出来的那一行：在 main() 的切片里加回
// 一个裸键 sub2api_mode，全套门禁一条都不红，而 2026-09-08 的误读恰恰读的就是
// 那一行。规则存在不等于规则被调用到——闸必须架在产物上，不是架在半成品上。
//
// 启动时就把采集配置摊开的理由不变：运维必须能一眼看出这个进程写进看板的
// 数字是 Fake 产的还是真实上游来的，而不是等发现数字不对再回来翻配置。
func workerStartupAttrs(config jobs.Config) []any {
	startup := []any{
		"event", "worker_started", "module", "platform.worker",
		"environment", config.Environment, "principal_id", "worker:platform",
		// secret_root 只打路径，不打内容。
		"secret_root", config.SecretRoot,
	}
	startup = append(startup, connectorModeStartupAttrs(config)...)
	startup = append(startup,
		// 成本采集同理（XM-0037b）。多一条 secrets_configured：real 模式还需要
		// 一个能解析**登记簿里那些引用**的 SecretProvider，而那些引用在进程
		// 启动时还不知道（它们在库里，由 Action 维护），所以现阶段它必然是
		// false——把这个事实打在启动日志里，比让人配完 real 再去猜为什么
		// 每个账号都报 not_supported 强。
		//
		// finance_collect_mode 不带 _default 后缀是有意的：finance 不在
		// credentials.Platforms，core.connector_config 里没有它的行，
		// XM_FINANCE_COLLECT_MODE 就是它的真相源，这个字段打的已经是生效值。
		// 同理还有 audit_archive_mode 与 reqlog_metrics_mode。
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
		"reqlog_metrics_interval", config.ReqlogMetricsInterval.String(),
		// XM-ASSURE1-core：检测任务的 Worker 永远注册（按需触发，不是周期
		// 任务），这里只打 Kill Switch/预算这两个真正门控探测是否发生的值。
		"assurance_probe_global_enabled", config.AssuranceProbeGlobalEnabled,
		"assurance_probe_daily_budget", config.AssuranceProbeDailyBudget,
		"assurance_probe_secrets_configured", config.AssuranceProbeSecrets != nil)
	return startup
}

// connectorModeStartupAttrs 是 worker_started 里与「接入模式」有关的那几个
// 字段（XM-OPS-TRUTH）。
//
// 字段名带 _default 后缀是这次修复的要点：
//   - *_mode_default 是**环境变量给的缺省**，后台热切换模式不重启容器，
//     它永远不变；本进程启动那一刻还没读过库，这里打不出生效值。
//   - 生效值在第一轮同步（*SyncRunOnStart 默认 true）的
//     connector_config_applied，以及每轮 job_completed 的 *_mode /
//     *_mode_source 里——effective_mode_log_events 把这条指路带进日志本身，
//     不指望有人先读过 runbook。
//
// 2026-09-08 有人照旧字段名读 worker_started 里的 sub2api_mode，得出「生产在
// 跑假数据」的结论，而生产那时跑的是 real（core.connector_config 两行 08-30
// 就设成 real 了）。
//
// CPA 不在这里：worker_started 从来没打过 cpa_mode，而且它那个字段本来就是
// 生效值（cpa 不在 credentials.Platforms，没有 core.connector_config 行）。
func connectorModeStartupAttrs(config jobs.Config) []any {
	return []any{
		// connector_config_source 说的是「这个进程的生效模式由谁决定」，
		// 所以它必须**从入参推导**，不能写死成 "database"。
		//
		// 生产装配无条件接了这张表（cmd/platform-worker/main.go），但
		// ConnectorConfigs 为 nil 的部署（静态工厂那条路，见
		// jobs.Config.sub2apiClientFactory）每一轮都按 env 解析、
		// job_completed 打的是 *_mode_source=env——那种部署里写死一句
		// "database" 就是一份「事实变了它不会跟着变、也不会报错」的副本，
		// 正是本片要消灭的那种东西。
		"connector_config_source", connectorConfigSourceLabel(config),
		"effective_mode_log_events", "connector_config_applied,job_completed",
		"sub2api_sync_enabled", config.Sub2APISyncEnabled,
		"sub2api_mode_default", string(config.Sub2APIMode),
		"sub2api_source", config.Sub2APIInstanceID,
		"sub2api_sync_interval", config.Sub2APISyncInterval.String(),
		"newapi_sync_enabled", config.NewAPISyncEnabled,
		"newapi_mode_default", string(config.NewAPIMode),
		"newapi_source", config.NewAPIInstanceID,
		"newapi_sync_interval", config.NewAPISyncInterval.String(),
	}
}

// connectorConfigSourceLabel 与 jobs.ResolveEffectiveMode 的两个来源标签
// （database / env）取同一套词，好让启动日志与每轮 job_completed 的
// *_mode_source 能直接对读。
func connectorConfigSourceLabel(config jobs.Config) string {
	if config.ConnectorConfigs != nil {
		return jobs.ModeSourceDatabase
	}
	return jobs.ModeSourceEnv
}
