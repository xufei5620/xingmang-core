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
