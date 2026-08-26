package main

import (
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/jobs"
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

// TestConfigFromEnvDefaultsSub2APIToFake：真实只读账号还没就绪（XM-0017），
// 默认必须是 fake；来源标识也必须一眼可辨，不能伪装成真实来源。
func TestConfigFromEnvDefaultsSub2APIToFake(t *testing.T) {
	values := map[string]string{"ENVIRONMENT": "staging"}
	cfg, err := configFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Sub2APISyncEnabled {
		t.Fatal("默认应开启 Sub2API 周期同步——看板要的是持续更新的数据")
	}
	if cfg.Sub2APIMode != jobs.Sub2APIModeFake {
		t.Fatalf("默认模式 = %q, want fake", cfg.Sub2APIMode)
	}
	if cfg.Sub2APIInstanceID != jobs.DefaultSub2APIInstanceID {
		t.Fatalf("默认来源 = %q, want %q", cfg.Sub2APIInstanceID, jobs.DefaultSub2APIInstanceID)
	}
	if cfg.Sub2APISyncInterval != jobs.DefaultSub2APISyncInterval {
		t.Fatalf("默认周期 = %s, want %s", cfg.Sub2APISyncInterval, jobs.DefaultSub2APISyncInterval)
	}
	if cfg.Sub2APICredentialRef != "" {
		t.Fatalf("未配置时不该凭空造出凭据引用: %q", cfg.Sub2APICredentialRef)
	}
}

func TestConfigFromEnvReadsSub2APISettings(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":               "staging",
		"XM_SUB2API_MODE":           "real",
		"XM_SUB2API_INSTANCE_ID":    "sub2api-acceptance",
		"XM_SUB2API_SYNC_INTERVAL":  "60s",
		"XM_SUB2API_SYNC_ENABLED":   "false",
		"XM_SUB2API_CREDENTIAL_REF": "secret://sub2api/readonly-token",
	}
	cfg, err := configFromEnv(func(key string) string { return values[key] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sub2APIMode != jobs.Sub2APIModeReal {
		t.Fatalf("mode = %q, want real", cfg.Sub2APIMode)
	}
	if cfg.Sub2APIInstanceID != "sub2api-acceptance" {
		t.Fatalf("source = %q, want sub2api-acceptance", cfg.Sub2APIInstanceID)
	}
	if cfg.Sub2APISyncEnabled || cfg.Sub2APISyncInterval != time.Minute {
		t.Fatalf("停用开关与周期未生效: %+v", cfg)
	}
	// 引用只被接住、不被解析出明文（ADR-014）。
	if cfg.Sub2APICredentialRef != "secret://sub2api/readonly-token" {
		t.Fatalf("credential ref = %q", cfg.Sub2APICredentialRef)
	}
}

func TestConfigFromEnvRejectsInvalidSub2APIValues(t *testing.T) {
	for key, value := range map[string]string{
		"XM_SUB2API_MODE":          "production",
		"XM_SUB2API_SYNC_INTERVAL": "not-a-duration",
		"XM_SUB2API_SYNC_ENABLED":  "maybe",
	} {
		values := map[string]string{"ENVIRONMENT": "staging", key: value}
		if _, err := configFromEnv(func(name string) string { return values[name] }); err == nil {
			t.Fatalf("%s=%q should fail", key, value)
		}
	}
}
