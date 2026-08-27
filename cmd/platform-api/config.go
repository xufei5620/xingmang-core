package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/oidcauth"
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
	Auth           authConfig
}

// authMode 是身份解析器的选择开关（XM_AUTH_MODE）。
type authMode string

const (
	// authModeDevHeader：身份来自 X-Dev-* 请求头，仅非生产（Foundation-A 现状）。
	authModeDevHeader authMode = "dev-header"
	// authModeOIDC：身份来自 Keycloak 的 Access Token（XM-0008）。
	authModeOIDC authMode = "oidc"
)

// authConfig 是身份解析的配置。**不含任何机密**：OIDC 校验只用公钥（JWKS），
// 平台侧不需要 Client Secret——CR-0001 的 xingmang-admin-web 是 public client。
type authConfig struct {
	Mode           authMode
	OIDCIssuer     string
	OIDCAudience   string
	OIDCJWKSURL    string
	OIDCRoleScopes map[string][]string
	OIDCClockSkew  time.Duration
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

	auth, err := authConfigFromEnv(getenv, c.Environment)
	if err != nil {
		return config{}, err
	}
	c.Auth = auth
	return c, nil
}

// authConfigFromEnv 解析身份相关配置，全程 Fail Closed。
//
// 三条不可协商的规则：
//
//  1. **生产只允许 oidc**。dev-header 让调用方用请求头自称身份，在生产等于没有
//     鉴权。httpapi.NewDevHeaderResolver 里已经有一道硬拒绝，这里再挡一次——
//     那道闸在「构造解析器」时才触发，这道在「读配置」时就触发，报错信息也能
//     直接说清楚该怎么改；
//  2. **oidc 缺 issuer 或 audience 拒绝启动**。少了 issuer 就没有信任根，少了
//     audience 就等于接受任何 Client 拿到的令牌。两者都不能有默认值；
//  3. **非生产默认 dev-header**。XM-0008 不改现状：development / staging 的栈
//     照常跑，切换是显式动作（把 XM_AUTH_MODE 设成 oidc）。
func authConfigFromEnv(getenv func(string) string, environment string) (authConfig, error) {
	a := authConfig{}

	mode := strings.TrimSpace(getenv("XM_AUTH_MODE"))
	if mode == "" {
		// 默认值跟着环境走：生产没有「先跑起来再说」这个选项
		if environment == "production" {
			mode = string(authModeOIDC)
		} else {
			mode = string(authModeDevHeader)
		}
	}
	a.Mode = authMode(mode)

	switch a.Mode {
	case authModeDevHeader:
		if environment == "production" {
			return authConfig{}, fmt.Errorf(
				"XM_AUTH_MODE=dev-header 不允许在生产环境使用：" +
					"请求头自称身份等于没有鉴权。生产请设 XM_AUTH_MODE=oidc " +
					"并配置 XM_OIDC_ISSUER / XM_OIDC_AUDIENCE（前置条件：CR-0001 已执行）")
		}
	case authModeOIDC:
	default:
		return authConfig{}, fmt.Errorf("XM_AUTH_MODE 必须是 dev-header 或 oidc，got %q", mode)
	}

	a.OIDCIssuer = strings.TrimSpace(getenv("XM_OIDC_ISSUER"))
	a.OIDCAudience = strings.TrimSpace(getenv("XM_OIDC_AUDIENCE"))
	a.OIDCJWKSURL = strings.TrimSpace(getenv("XM_OIDC_JWKS_URL"))

	roleScopes, err := oidcauth.ParseRoleScopeMap(getenv("XM_OIDC_ROLE_SCOPES"))
	if err != nil {
		return authConfig{}, fmt.Errorf("XM_OIDC_ROLE_SCOPES: %w", err)
	}
	a.OIDCRoleScopes = roleScopes

	if v := strings.TrimSpace(getenv("XM_OIDC_CLOCK_SKEW")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return authConfig{}, fmt.Errorf("XM_OIDC_CLOCK_SKEW: %w", err)
		}
		// 上限刻意压到 5 分钟：偏移容忍本质是在延长令牌寿命，
		// 一个「顺手写成 24h」的值会让 CR-0001 定的 5 分钟令牌形同虚设
		if d <= 0 || d > 5*time.Minute {
			return authConfig{}, fmt.Errorf("XM_OIDC_CLOCK_SKEW 必须在 (0, 5m] 之间，got %s", v)
		}
		a.OIDCClockSkew = d
	}

	if a.Mode == authModeOIDC {
		if a.OIDCIssuer == "" {
			return authConfig{}, fmt.Errorf(
				"XM_AUTH_MODE=oidc 时 XM_OIDC_ISSUER 必填，" +
					"例如 https://auth.solov.cc/realms/solov-staff")
		}
		if a.OIDCAudience == "" {
			return authConfig{}, fmt.Errorf(
				"XM_AUTH_MODE=oidc 时 XM_OIDC_AUDIENCE 必填，" +
					"例如 xingmang-admin-web（CR-0001 的 Client ID）")
		}
	}
	return a, nil
}
