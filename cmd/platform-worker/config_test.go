package main

import (
	"testing"
	"time"
)

func TestConfigFromEnv(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":            "staging",
		"HEARTBEAT_INTERVAL":     "2s",
		"HEARTBEAT_RUN_ON_START": "false",
		"HEARTBEAT_FAILURES":     "1",
	}
	cfg, err := configFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environment != "staging" || cfg.HeartbeatInterval != 2*time.Second || cfg.HeartbeatRunOnStart || cfg.HeartbeatFailures != 1 {
		t.Fatalf("configFromEnv = %+v", cfg)
	}
}

func TestConfigFromEnvRejectsInvalidValues(t *testing.T) {
	for key, value := range map[string]string{
		"HEARTBEAT_INTERVAL":     "not-a-duration",
		"HEARTBEAT_RUN_ON_START": "maybe",
		"HEARTBEAT_FAILURES":     "not-an-int",
	} {
		values := map[string]string{"ENVIRONMENT": "test", key: value}
		if _, err := configFromEnv(func(name string) string { return values[name] }); err == nil {
			t.Fatalf("%s=%q should fail", key, value)
		}
	}
}

func TestConfigFromEnvRequiresExplicitEnvironment(t *testing.T) {
	if _, err := configFromEnv(func(string) string { return "" }); err == nil {
		t.Fatal("missing ENVIRONMENT should fail closed")
	}
}
