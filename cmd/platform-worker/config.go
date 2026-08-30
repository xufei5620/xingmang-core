package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
)

func configFromEnv(getenv func(string) string) (jobs.Config, error) {
	config := jobs.DefaultConfig()
	config.Environment = getenv("ENVIRONMENT")
	config.Environment = strings.TrimSpace(config.Environment)
	if config.Environment == "" {
		return jobs.Config{}, fmt.Errorf("environment is required")
	}
	archiveConfig, err := auditArchiveConfigFromEnv(getenv, config.Environment)
	if err != nil {
		return jobs.Config{}, err
	}
	config.AuditArchive = archiveConfig
	if value := getenv("HEARTBEAT_INTERVAL"); value != "" {
		interval, err := time.ParseDuration(value)
		if err != nil {
			return jobs.Config{}, fmt.Errorf("heartbeat interval: %w", err)
		}
		config.HeartbeatInterval = interval
	}
	if value := getenv("HEARTBEAT_RUN_ON_START"); value != "" {
		runOnStart, err := strconv.ParseBool(value)
		if err != nil {
			return jobs.Config{}, fmt.Errorf("heartbeat run on start: %w", err)
		}
		config.HeartbeatRunOnStart = runOnStart
	}
	if value := getenv("HEARTBEAT_FAILURES"); value != "" {
		failures, err := strconv.Atoi(value)
		if err != nil {
			return jobs.Config{}, fmt.Errorf("heartbeat failures: %w", err)
		}
		config.HeartbeatFailures = failures
	}

	// XM-CRED0：后台写入的凭据文件根目录。只读路径形状，不读任何文件。
	config.SecretRoot, err = parseSecretRoot(getenv(secretRootEnvVar))
	if err != nil {
		return jobs.Config{}, err
	}

	// XM-0022：Sub2API 周期同步。默认 fake——真实只读账号还没就绪（XM-0017），
	// 把默认设成 real 只会让每个新环境一上来就满屏同步失败。
	mode, err := jobs.ParseSub2APIMode(getenv("XM_SUB2API_MODE"))
	if err != nil {
		return jobs.Config{}, err
	}
	config.Sub2APIMode = mode
	if value := strings.TrimSpace(getenv("XM_SUB2API_INSTANCE_ID")); value != "" {
		config.Sub2APIInstanceID = value
	}
	// 只读进配置、不解析：凭据只经 CredentialRef（ADR-014、宪法 7 条）。
	// 明文由 SecretProvider 在客户端构造 Authorization 头的那一瞬才出现，
	// 这里与 jobs.Config 都只看见引用本身。
	config.Sub2APICredentialRef = strings.TrimSpace(getenv("XM_SUB2API_CREDENTIAL_REF"))
	// real 模式的连接配置（XM-0017）。三项缺任意一项，real 模式都立不起来，
	// 但**不在启动时报错**：缺配置会在每轮同步写成一条说得清缺哪个的
	// SyncFailed 观测，看板看得见（规格 §9.1）。启动即崩的话，一个配错的
	// 采集通道会把整个 worker（心跳、其他任务）一起拖下水。
	config.Sub2APIEndpoint = strings.TrimSpace(getenv("XM_SUB2API_ENDPOINT"))
	config.Sub2APITargetAllowlist = parseHostAllowlist(getenv("XM_SUB2API_TARGET_ALLOWLIST"))
	if value := getenv("XM_SUB2API_SYNC_ENABLED"); value != "" {
		// 采集链路的停用开关（宪法 26 条）：上游出事时能立刻停掉读取，
		// 而不必改代码重发版。关掉之后看板不会假装新鲜——observed_at 不再
		// 前进，新鲜度自然降级（规格 §9.1）。
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return jobs.Config{}, fmt.Errorf("sub2api sync enabled: %w", err)
		}
		config.Sub2APISyncEnabled = enabled
	}
	if value := getenv("XM_SUB2API_SYNC_INTERVAL"); value != "" {
		interval, err := time.ParseDuration(value)
		if err != nil {
			return jobs.Config{}, fmt.Errorf("sub2api sync interval: %w", err)
		}
		config.Sub2APISyncInterval = interval
	}

	// XM-0035/XM-0038：NewAPI 周期同步。默认 fake——真实只读凭据由用户自配，
	// 把默认设成 real 只会让每个新环境一上来就满屏 not_supported。
	newapiMode, err := jobs.ParseNewAPIMode(getenv("XM_NEWAPI_MODE"))
	if err != nil {
		return jobs.Config{}, err
	}
	config.NewAPIMode = newapiMode
	if value := strings.TrimSpace(getenv("XM_NEWAPI_INSTANCE_ID")); value != "" {
		config.NewAPIInstanceID = value
	}
	// 只读进配置、不解析：凭据只经 CredentialRef（ADR-014、宪法 7 条）。
	// 明文由 SecretProvider 在客户端构造 Authorization 头的那一瞬才出现，
	// 这里与 jobs.Config 都只看见引用本身。
	config.NewAPICredentialRef = strings.TrimSpace(getenv("XM_NEWAPI_CREDENTIAL_REF"))
	// real 模式的连接配置（XM-0038）。三项缺任意一项，real 模式都立不起来，
	// 但**不在启动时报错**：缺配置会在每轮同步写成一条说得清缺哪个的
	// SyncFailed 观测，看板看得见（规格 §9.1）。启动即崩的话，一个配错的
	// 采集通道会把整个 worker（心跳、其他任务）一起拖下水。
	config.NewAPIEndpoint = strings.TrimSpace(getenv("XM_NEWAPI_ENDPOINT"))
	config.NewAPITargetAllowlist = parseHostAllowlist(getenv("XM_NEWAPI_TARGET_ALLOWLIST"))
	// 可选：旧版本 NewAPI 需要的 New-Api-User 头（管理员用户 id）。
	// 它**不是**凭据，新版本上游会忽略它——缺它不会让 real 模式立不起来。
	config.NewAPIUserID = strings.TrimSpace(getenv("XM_NEWAPI_USER_ID"))
	if value := getenv("XM_NEWAPI_SYNC_ENABLED"); value != "" {
		// 采集链路的停用开关（宪法 26 条）：上游出事时能立刻停掉读取，
		// 而不必改代码重发版。关掉之后看板不会假装新鲜——observed_at 不再
		// 前进，新鲜度自然降级（规格 §9.1）。
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return jobs.Config{}, fmt.Errorf("newapi sync enabled: %w", err)
		}
		config.NewAPISyncEnabled = enabled
	}
	if value := getenv("XM_NEWAPI_SYNC_INTERVAL"); value != "" {
		interval, err := time.ParseDuration(value)
		if err != nil {
			return jobs.Config{}, fmt.Errorf("newapi sync interval: %w", err)
		}
		config.NewAPISyncInterval = interval
	}

	// XM-0037b：成本采集与利润台账入账（设计稿 §8.1）。默认 fake——
	// 真实只读凭据还没就绪，把默认设成 real 只会让每个新环境一上来就
	// 满屏采集失败。
	//
	// 这里刻意**不**登记 endpoint 与 credential ref：那两样逐账号不同，
	// 来自成本登记簿（§2.1 的 base_url / credential_ref），由 Action 维护。
	// 在进程配置里再放一份，两处迟早会漂，而漂了之后采集会用着 A 的地址、
	// B 的凭据，报出来的错还是「认证失败」。
	financeMode, err := jobs.ParseFinanceCollectMode(getenv("XM_FINANCE_COLLECT_MODE"))
	if err != nil {
		return jobs.Config{}, err
	}
	config.FinanceCollectMode = financeMode
	if value := strings.TrimSpace(getenv("XM_FINANCE_COLLECT_INSTANCE_ID")); value != "" {
		config.FinanceCollectInstanceID = value
	}
	// XM-REAL0-a：成本侧登记簿的账号/令牌 CredentialRef 在运行时才知道，
	// 由显式 env 或 file convention provider 按引用即时解析；这里仅读取
	// provider 选择与 scope 白名单，不读取任何秘密值。
	config.FinanceCollectSecretProvider = strings.TrimSpace(getenv(financeSecretProviderEnvVar))
	config.FinanceCollectSecretRoot = strings.TrimSpace(getenv(financeSecretRootEnvVar))
	config.FinanceCollectSecretScopes, err = parseFinanceSecretScopes(
		getenv(financeSecretScopesEnvVar))
	if err != nil {
		return jobs.Config{}, err
	}
	config.FinanceCollectTargetAllowlist = parseHostAllowlist(
		getenv("XM_FINANCE_COLLECT_TARGET_ALLOWLIST"))
	if value := getenv("XM_FINANCE_COLLECT_ENABLED"); value != "" {
		// 采集链路的停用开关（宪法 26 条）：上游出事时能立刻停掉读取，
		// 而不必改代码重发版。关掉之后台账不会假装有数——今日行停止刷新，
		// finance.profit.daily 的 observed_at 不再前进，新鲜度自然降级。
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return jobs.Config{}, fmt.Errorf("finance collect enabled: %w", err)
		}
		config.FinanceCollectEnabled = enabled
	}
	if value := getenv("XM_FINANCE_COLLECT_INTERVAL"); value != "" {
		// §12 拍板：采集频率可配，默认 5min。
		interval, err := time.ParseDuration(value)
		if err != nil {
			return jobs.Config{}, fmt.Errorf("finance collect interval: %w", err)
		}
		config.FinanceCollectInterval = interval
	}
	if value := getenv("XM_FINANCE_COLLECT_REQUEST_TIMEOUT"); value != "" {
		timeout, err := time.ParseDuration(value)
		if err != nil {
			return jobs.Config{}, fmt.Errorf("finance collect request timeout: %w", err)
		}
		config.FinanceCollectRequestTimeout = timeout
	}

	// XM-R012：保留期清理。天数可配，**清理本身没有关闭开关以外的旁路**——
	// 一张没有清理路径的追加表迟早会成为运维事故。
	//
	// ⚠️ 审计事件不在清理范围内（宪法 11 条 append-only），所以这里没有
	// 「审计保留天数」这个变量：给一个删不掉东西的旋钮，比不给更误导。
	if value := getenv("XM_RETENTION_ENABLED"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return jobs.Config{}, fmt.Errorf("retention enabled: %w", err)
		}
		config.RetentionEnabled = enabled
	}
	if value := getenv("XM_RETENTION_INTERVAL"); value != "" {
		interval, err := time.ParseDuration(value)
		if err != nil {
			return jobs.Config{}, fmt.Errorf("retention interval: %w", err)
		}
		config.RetentionInterval = interval
	}
	if value := strings.TrimSpace(getenv("XM_METRIC_SAMPLE_RETENTION_DAYS")); value != "" {
		days, err := strconv.Atoi(value)
		if err != nil {
			return jobs.Config{}, fmt.Errorf("metric sample retention days: %w", err)
		}
		if days <= 0 {
			// 0 最自然的读法是「不保留」，也就是把整张表删空——而想表达
			// 「不清理」的人该去关 XM_RETENTION_ENABLED。两种意图差得太远，
			// 不能让一个手滑的 0 去猜。
			return jobs.Config{}, fmt.Errorf(
				"XM_METRIC_SAMPLE_RETENTION_DAYS 必须为正（想停清理请置 XM_RETENTION_ENABLED=false），got %s", value)
		}
		config.MetricSampleRetentionDays = days
	}
	if value := strings.TrimSpace(getenv("XM_ALERT_RETENTION_DAYS")); value != "" {
		days, err := strconv.Atoi(value)
		if err != nil {
			return jobs.Config{}, fmt.Errorf("alert retention days: %w", err)
		}
		if days <= 0 {
			return jobs.Config{}, fmt.Errorf(
				"XM_ALERT_RETENTION_DAYS 必须为正（想停清理请置 XM_RETENTION_ENABLED=false），got %s", value)
		}
		config.AlertRetentionDays = days
	}

	// XM-0033：告警评估与投递（规格 §9.3 / §9.4）。
	//
	// 投递渠道的三个变量只**读进配置、不解析**：Bot Token 只经 CredentialRef
	// （ADR-014、宪法 7 条），明文由 SecretProvider 在构造 Bot API URL 的
	// 那一瞬才出现。这里与 jobs.Config 都只看见引用本身。
	config.AlertTelegramBotRef = strings.TrimSpace(getenv("XM_ALERT_TELEGRAM_BOT_REF"))
	config.AlertTelegramChatID = strings.TrimSpace(getenv("XM_ALERT_TELEGRAM_CHAT_ID"))
	config.AlertWebhookURL = strings.TrimSpace(getenv("XM_ALERT_WEBHOOK_URL"))
	if value := getenv("XM_ALERT_EVALUATE_ENABLED"); value != "" {
		// 告警链路的停用开关（宪法 26 条）。关掉之后告警页不会假装正常：
		// 已有告警的 last_seen_at 停止前进，新问题不会被发现——这是一个
		// 显式的、看得出来的降级，不是静默失效。
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return jobs.Config{}, fmt.Errorf("alert evaluate enabled: %w", err)
		}
		config.AlertEvaluateEnabled = enabled
	}
	if value := getenv("XM_ALERT_EVALUATE_INTERVAL"); value != "" {
		interval, err := time.ParseDuration(value)
		if err != nil {
			return jobs.Config{}, fmt.Errorf("alert evaluate interval: %w", err)
		}
		config.AlertEvaluateInterval = interval
	}
	if value := strings.TrimSpace(getenv("XM_ALERT_BALANCE_THRESHOLD_MINOR_UNITS")); value != "" {
		// 单位是**最小货币单位**的整数（宪法 13 条：金额禁止 float）。
		// ParseInt 而不是 ParseFloat：一个写成 "5000.5" 的阈值说明写的人
		// 搞错了口径，此时报错比四舍五入成一个谁都没想要的值好。
		threshold, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return jobs.Config{}, fmt.Errorf("alert balance threshold（须为最小货币单位的整数）: %w", err)
		}
		if threshold <= 0 {
			return jobs.Config{}, fmt.Errorf("alert balance threshold 必须为正，got %d", threshold)
		}
		config.AlertBalanceThresholdMinorUnits = threshold
	}
	return config, nil
}

// parseHostAllowlist 把逗号分隔的主机清单拆成精确匹配用的切片。
//
// 只做拆分、去空白、转小写——**不做**任何补全或推断（比如"从 endpoint 猜
// 一个主机塞进去"）。allowlist 的全部价值就在于它是人显式写下的那一份，
// 系统替人填进去的那一项等于没有。
func parseHostAllowlist(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		host := strings.ToLower(strings.TrimSpace(part))
		if host != "" {
			out = append(out, host)
		}
	}
	return out
}
