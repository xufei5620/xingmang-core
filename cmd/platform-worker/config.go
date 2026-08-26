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
	return config, nil
}
