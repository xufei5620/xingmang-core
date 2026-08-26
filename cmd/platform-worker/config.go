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
	// XM-0017 接上真实客户端时才会有人用它，jobs.Config 现在只校验它的形状。
	config.Sub2APICredentialRef = strings.TrimSpace(getenv("XM_SUB2API_CREDENTIAL_REF"))
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
