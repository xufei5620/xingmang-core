package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// config 只承载**非机密**配置。
//
// 数据库连接串刻意不在这里：它可能携带密码，必须走 database.go 的
// CredentialRef 纪律（宪法 7 条）。把它留在 config 里，早晚会有人为了写测试
// 而直接塞一个内联密码进来，纪律就从「代码保证」退化成「约定」。
type config struct {
	Environment    string
	ListenAddr     string
	RequestTimeout time.Duration
}

// configFromEnv 从环境变量读取配置。
// getenv 作为参数注入，便于测试（对齐 cmd/platform-worker 的模式）。
func configFromEnv(getenv func(string) string) (config, error) {
	c := config{
		ListenAddr:     "127.0.0.1:8080", // 规格 §21.2：绑回环，由宿主 Nginx 反代
		RequestTimeout: 30 * time.Second,
	}

	c.Environment = strings.TrimSpace(getenv("ENVIRONMENT"))
	if c.Environment == "" {
		return config{}, fmt.Errorf("ENVIRONMENT is required")
	}
	if _, err := registry.ParseEnvironment(c.Environment); err != nil {
		return config{}, fmt.Errorf("ENVIRONMENT: %w", err)
	}

	if v := strings.TrimSpace(getenv("LISTEN_ADDR")); v != "" {
		c.ListenAddr = v
	}
	if v := strings.TrimSpace(getenv("REQUEST_TIMEOUT")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return config{}, fmt.Errorf("REQUEST_TIMEOUT: %w", err)
		}
		if d <= 0 {
			return config{}, fmt.Errorf("REQUEST_TIMEOUT must be positive, got %s", v)
		}
		c.RequestTimeout = d
	}
	return c, nil
}
