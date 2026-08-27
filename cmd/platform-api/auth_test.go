package main

import (
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"
)

const stagingIssuer = "https://auth.solov.cc/realms/solov-staff"

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
// 生产：只允许 oidc
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

	t.Run("生产默认走 oidc 而不是 dev-header", func(t *testing.T) {
		// 缺 issuer/audience，所以这里期望的是「因为缺 OIDC 配置」而失败，
		// 而不是「默认到了 dev-header 然后跑起来」
		_, err := configFromEnv(env(map[string]string{"ENVIRONMENT": "production"}))
		if err == nil {
			t.Fatal("生产缺 OIDC 配置应拒绝启动")
		}
		if !strings.Contains(err.Error(), "XM_OIDC_ISSUER") {
			t.Fatalf("生产的默认模式应是 oidc（报错应指向 XM_OIDC_ISSUER）, got %v", err)
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

// ---------------------------------------------------------------------------
// oidc：缺配置 Fail Closed
// ---------------------------------------------------------------------------

func TestOIDCModeRequiresIssuerAndAudience(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"缺 issuer 与 audience": {},
		"只有 issuer":           {"XM_OIDC_ISSUER": stagingIssuer},
		"只有 audience":         {"XM_OIDC_AUDIENCE": "xingmang-admin-web"},
		"issuer 是空白": {
			"XM_OIDC_ISSUER":   "   ",
			"XM_OIDC_AUDIENCE": "xingmang-admin-web",
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := map[string]string{"ENVIRONMENT": "staging", "XM_AUTH_MODE": "oidc"}
			for k, v := range vars {
				m[k] = v
			}
			if _, err := configFromEnv(env(m)); err == nil {
				t.Fatal("oidc 模式缺必填配置应拒绝启动（Fail Closed）")
			}
		})
	}
}

func TestOIDCModeAcceptsCompleteConfig(t *testing.T) {
	c, err := configFromEnv(env(map[string]string{
		"ENVIRONMENT":      "staging",
		"XM_AUTH_MODE":     "oidc",
		"XM_OIDC_ISSUER":   stagingIssuer,
		"XM_OIDC_AUDIENCE": "xingmang-admin-web",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Auth.Mode != authModeOIDC || c.Auth.OIDCIssuer != stagingIssuer {
		t.Fatalf("Auth = %+v", c.Auth)
	}
	// 没配映射表时留 nil，由 oidcauth 回落到默认表
	if c.Auth.OIDCRoleScopes != nil {
		t.Fatalf("未配 XM_OIDC_ROLE_SCOPES 时应留 nil, got %v", c.Auth.OIDCRoleScopes)
	}
	if _, err := newPrincipalResolver(c, quietLogger()); err != nil {
		t.Fatalf("完整配置应能装配出 OIDC Resolver: %v", err)
	}
}

// 生产 + oidc + 完整配置 = 唯一被允许的生产形态。
func TestProductionOIDCAssembles(t *testing.T) {
	c, err := configFromEnv(env(map[string]string{
		"ENVIRONMENT":      "production",
		"XM_OIDC_ISSUER":   stagingIssuer,
		"XM_OIDC_AUDIENCE": "xingmang-admin-web",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Auth.Mode != authModeOIDC {
		t.Fatalf("Mode = %q", c.Auth.Mode)
	}
	if _, err := newPrincipalResolver(c, quietLogger()); err != nil {
		t.Fatalf("生产 + oidc + 完整配置应能启动: %v", err)
	}
}

// 生产的 issuer 必须是 https：明文传的令牌等于没有令牌。
func TestProductionOIDCRejectsPlainHTTPIssuer(t *testing.T) {
	c, err := configFromEnv(env(map[string]string{
		"ENVIRONMENT":      "production",
		"XM_OIDC_ISSUER":   "http://auth.solov.cc/realms/solov-staff",
		"XM_OIDC_AUDIENCE": "xingmang-admin-web",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newPrincipalResolver(c, quietLogger()); err == nil {
		t.Fatal("生产的 issuer 必须是 https")
	}
}

// ---------------------------------------------------------------------------
// 可选配置
// ---------------------------------------------------------------------------

func TestOIDCRoleScopesFromEnv(t *testing.T) {
	c, err := configFromEnv(env(map[string]string{
		"ENVIRONMENT":         "staging",
		"XM_AUTH_MODE":        "oidc",
		"XM_OIDC_ISSUER":      stagingIssuer,
		"XM_OIDC_AUDIENCE":    "xingmang-admin-web",
		"XM_OIDC_ROLE_SCOPES": `{"staff":["registry.read"],"auditor":["audit.read"]}`,
		"XM_OIDC_JWKS_URL":    stagingIssuer + "/protocol/openid-connect/certs",
		"XM_OIDC_CLOCK_SKEW":  "30s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Auth.OIDCRoleScopes["staff"], []string{"registry.read"}) {
		t.Fatalf("staff = %v", c.Auth.OIDCRoleScopes["staff"])
	}
	if c.Auth.OIDCClockSkew != 30*time.Second {
		t.Fatalf("ClockSkew = %v", c.Auth.OIDCClockSkew)
	}
	if _, err := newPrincipalResolver(c, quietLogger()); err != nil {
		t.Fatalf("装配失败: %v", err)
	}
}

func TestOIDCOptionalConfigRejectsBadValues(t *testing.T) {
	base := map[string]string{
		"ENVIRONMENT":      "staging",
		"XM_AUTH_MODE":     "oidc",
		"XM_OIDC_ISSUER":   stagingIssuer,
		"XM_OIDC_AUDIENCE": "xingmang-admin-web",
	}
	for name, vars := range map[string]map[string]string{
		"映射表不是 JSON":   {"XM_OIDC_ROLE_SCOPES": "staff=registry.read"},
		"映射表方向写反":      {"XM_OIDC_ROLE_SCOPES": `{"registry.read":["registry.read"]}`},
		"偏移容忍非法":       {"XM_OIDC_CLOCK_SKEW": "abc"},
		"偏移容忍为零":       {"XM_OIDC_CLOCK_SKEW": "0s"},
		"偏移容忍为负":       {"XM_OIDC_CLOCK_SKEW": "-1s"},
		"偏移容忍大到架空令牌寿命": {"XM_OIDC_CLOCK_SKEW": "24h"},
	} {
		t.Run(name, func(t *testing.T) {
			m := map[string]string{}
			for k, v := range base {
				m[k] = v
			}
			for k, v := range vars {
				m[k] = v
			}
			if _, err := configFromEnv(env(m)); err == nil {
				t.Fatal("应拒绝启动")
			}
		})
	}

	t.Run("JWKS 地址跨源", func(t *testing.T) {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		m["XM_OIDC_JWKS_URL"] = "https://evil.example.com/certs"
		c, err := configFromEnv(env(m))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := newPrincipalResolver(c, quietLogger()); err == nil {
			t.Fatal("JWKS 地址与 issuer 不同源应拒绝启动")
		}
	})
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
