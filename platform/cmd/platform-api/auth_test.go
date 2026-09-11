package main

import (
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---------------------------------------------------------------------------
// XM_AUTH_MODE 的默认值与合法值
// ---------------------------------------------------------------------------

func TestAuthModeDefaults(t *testing.T) {
	for _, name := range []string{"development", "staging"} {
		c, err := configFromEnv(env(map[string]string{"ENVIRONMENT": name}))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if c.Auth.Mode != authModeDevHeader {
			t.Fatalf("%s 默认应是 dev-header（XM-0008 不改现状）, got %q", name, c.Auth.Mode)
		}
	}
}

func TestAuthModeRejectsUnknownValue(t *testing.T) {
	_, err := configFromEnv(env(map[string]string{
		"ENVIRONMENT":  "staging",
		"XM_AUTH_MODE": "none",
	}))
	if err == nil {
		t.Fatal("未知的 XM_AUTH_MODE 应拒绝启动，而不是悄悄回落到某个模式")
	}
}

// ---------------------------------------------------------------------------
// 生产：只允许本地会话
// ---------------------------------------------------------------------------

func TestProductionRefusesDevHeader(t *testing.T) {
	t.Run("显式配 dev-header 直接拒绝", func(t *testing.T) {
		_, err := configFromEnv(env(map[string]string{
			"ENVIRONMENT":  "production",
			"XM_AUTH_MODE": "dev-header",
		}))
		if err == nil {
			t.Fatal("生产 + dev-header 必须拒绝启动：请求头自称身份等于没有鉴权")
		}
		if !strings.Contains(err.Error(), "dev-header") {
			t.Errorf("报错应说清是哪个配置的问题: %v", err)
		}
	})

	// 纵深防御：即便有人绕过 configFromEnv 直接拼一个 config，
	// httpapi.NewDevHeaderResolver 自己那道硬拒绝仍然在
	t.Run("直接构造 config 也拦得住", func(t *testing.T) {
		cfg := config{Environment: "production", Auth: authConfig{Mode: authModeDevHeader}}
		if _, err := newPrincipalResolver(cfg, quietLogger()); err == nil {
			t.Fatal("第二道闸（NewDevHeaderResolver）应拦住生产的 dev-header")
		}
	})
}

func TestProductionAllowsLocalAuthMode(t *testing.T) {
	c, err := configFromEnv(env(map[string]string{
		"ENVIRONMENT":  "production",
		"XM_AUTH_MODE": "local",
	}))
	if err != nil {
		t.Fatalf("生产 + local 应允许启动（账号与口令哈希都在平台自己的库里）: %v", err)
	}
	if c.Auth.Mode != authModeLocal {
		t.Fatalf("Mode = %q", c.Auth.Mode)
	}
}

func TestLocalAuthModeWorksInNonProduction(t *testing.T) {
	for _, envName := range []string{"development", "staging"} {
		c, err := configFromEnv(env(map[string]string{
			"ENVIRONMENT":  envName,
			"XM_AUTH_MODE": "local",
		}))
		if err != nil || c.Auth.Mode != authModeLocal {
			t.Fatalf("%s: c = %+v, err = %v", envName, c, err)
		}
	}
}

// newPrincipalResolver（auth.go）刻意不处理 local——它只装配不依赖数据库连接
// 的开发头模式；local 需要 pool，main.go 单独装配（见 cmd/platform-api/localauth.go
// 与 main.go 里 `if cfg.Auth.Mode == authModeLocal` 分支）。这条测试提醒读到
// 这个函数的人：新增一种模式不代表它自动被这里接管。
func TestNewPrincipalResolverDoesNotHandleLocalMode(t *testing.T) {
	cfg := config{Environment: "staging", Auth: authConfig{Mode: authModeLocal}}
	if _, err := newPrincipalResolver(cfg, quietLogger()); err == nil {
		t.Fatal("newPrincipalResolver 不处理 local 模式，main.go 需要单独装配")
	}
}

// dev-header 模式在非生产照常可用，且必须留下「现在没有真鉴权」的痕迹。
func TestDevHeaderModeStillWorksInNonProduction(t *testing.T) {
	c, err := configFromEnv(env(map[string]string{"ENVIRONMENT": "development"}))
	if err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	if _, err := newPrincipalResolver(c, logger); err != nil {
		t.Fatalf("非生产的 dev-header 应可用: %v", err)
	}
	if !strings.Contains(buf.String(), "auth_mode_dev_header") {
		t.Fatalf("dev-header 模式应留一条 warn:\n%s", buf.String())
	}
}

func TestLocalRoleScopesFromEnv(t *testing.T) {
	c, err := configFromEnv(env(map[string]string{"ENVIRONMENT": "production", "XM_AUTH_ROLE_SCOPES": `{"ops":["ops.read"],"auditor":["audit.read"]}`}))
	if err != nil {
		t.Fatal(err)
	}
	roles := localAuthRoleMap(c)
	if !slices.Equal(roles["ops"], []string{"ops.read"}) {
		t.Fatalf("custom roles lost: %v", roles)
	}
	if _, ok := roles["admin"]; ok {
		t.Fatalf("custom roles were merged with defaults: %v", roles)
	}
}

func TestLocalRoleScopesRejectMalformedOrReversedMap(t *testing.T) {
	for _, raw := range []string{"staff=registry.read", `{}`, `{"registry.read":["registry.read"]}`} {
		if _, err := authConfigFromEnv(env(map[string]string{"XM_AUTH_ROLE_SCOPES": raw}), "production"); err == nil {
			t.Fatalf("unsafe role map accepted: %s", raw)
		}
	}
}
