package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

type config struct {
	Environment    string
	ListenAddr     string
	DatabaseURL    string
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

	c.DatabaseURL = strings.TrimSpace(getenv("XM_DATABASE_URL"))
	if c.DatabaseURL == "" {
		return config{}, fmt.Errorf("XM_DATABASE_URL is required")
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
