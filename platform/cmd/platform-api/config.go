package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/localauth"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
	"github.com/xufei5620/xingmang-platform/internal/platform/rolepermissions"
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
	// Reqlog 是「请求详情」这条链路的配置（XM-0039）。
	// 同样不含机密：凭据只有 CredentialRef 形态的引用（宪法 7 条）。
	Reqlog reqlogConfig
	// CPA 是「CPA 用户管理」逐 key 用量端点的配置（XM-CPA0）。
	// 不含任何机密：只是一条只读挂载路径。
	CPA cpaConfig

	// FinanceDemoSeed 决定启动时是否种一批**演示**登记簿记录（XM-0037d）。
	//
	// 默认 false，**生产环境即便置 true 也会拒绝启动**（见 finance.SeedDemoData）：
	// 演示数据一旦落进生产登记簿，采集就会照着它去打一批 .invalid 域名，
	// 而台账里会多出几条永远算不出成本的渠道。
	FinanceDemoSeed bool

	// RateLimit 是 /api/v1 的限流配额（XM-R011）。
	// 零值走 httpapi 的默认值；两项都可用环境变量调，但**关不掉**。
	RateLimit httpapi.RateLimitConfig

	// SecretRoot 是凭据文件的根目录（XM-CRED0，XM_SECRET_ROOT）。
	//
	// 运营粘贴的凭据以 <root>/<scope>/<name> 落盘；worker 与本进程都用同一个
	// 目录构造 secrets.NewFileProvider 读值。本进程绝大部分路径仍是只写：
	// 读路径包括 XM-USERS-REAL 的 real 模式（platformUsersSecretProvider，
	// 见 cmd/platform-api/platformusers.go）与 XM-AUTH-TOTP0 登录时读回 TOTP
	// 密钥（同一个 Provider 构造，见 main.go 里 registerLocalAuthActions /
	// localauth.NewHandlers 的装配点），两者复用同一份 XM_SECRET_ROOT 目录。
	// 目录本身不是机密，路径可以进日志；目录里的文件永远不能。
	SecretRoot string

	// ConsoleAdminIPAllowlist 是 XM-AUTH-TOTP0/CR-0006 c 条的管理员来源 IP
	// 名单（XM_CONSOLE_ADMIN_IP_ALLOWLIST，逗号分隔 CIDR；空＝不启用）。
	// 只对已经/将要被要求启用 TOTP 的账号（管理员）生效，见
	// localauth.accountNeedsAdminIPCheck。这是纵深防御，不是唯一防线——与
	// 同进程开票管理员适配器复用相同名单独立校验。
	ConsoleAdminIPAllowlist localauth.AdminIPAllowlist
}

// defaultSecretRoot 与 deploy/compose/launch.yaml 里 xm-secrets 卷的挂载点一致。
const defaultSecretRoot = "/run/xm/secrets"

// authMode 是身份解析器的选择开关（XM_AUTH_MODE）。
type authMode string

const (
	// authModeDevHeader：身份来自 X-Dev-* 请求头，仅非生产（Foundation-A 现状）。
	authModeDevHeader authMode = "dev-header"
	// authModeLocal：身份来自平台自带的账号库（XM-LOGIN），账号与口令哈希
	// 落在 core.staff_account，会话是服务端持有状态的 Cookie。与 dev-header
	// 不同，它不是"请求头自称身份"——账号需要 cmd/staff-bootstrap 或
	// staff.manage Action 显式创建，口令走 argon2id 校验，因此**允许在生产
	// 使用**（不像 dev-header 那样被硬性禁止）。
	authModeLocal authMode = "local"
)

// authConfig 只承载本地会话模式和员工角色权限映射，不含凭据。
type authConfig struct {
	Mode       authMode
	RoleScopes map[string][]string
}

// configFromEnv 从环境变量读取配置。
// getenv 作为参数注入，便于测试（对齐 cmd/platform-worker 的模式）。
func configFromEnv(getenv func(string) string) (config, error) {
	c := config{
		ListenAddr:     "127.0.0.1:8080", // 规格 §21.2：绑回环，由宿主 Nginx 反代
		RequestTimeout: 30 * time.Second,
		SecretRoot:     defaultSecretRoot,
	}
	if v := strings.TrimSpace(getenv("XM_SECRET_ROOT")); v != "" {
		c.SecretRoot = v
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

	// 限流配额（XM-R011）。非法值一律拒绝启动，不回落到默认值：
	// 「以为调宽了其实没生效」会让人在一次真实的流量高峰里查错方向。
	if v := strings.TrimSpace(getenv("XM_RATE_LIMIT_PER_MINUTE")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return config{}, fmt.Errorf("XM_RATE_LIMIT_PER_MINUTE: %w", err)
		}
		if n <= 0 {
			// 0 或负数最自然的读法是「不限流」，但那正是本任务要消灭的状态。
			// 要放宽就填一个大数字——那是一个看得见的决定。
			return config{}, fmt.Errorf(
				"XM_RATE_LIMIT_PER_MINUTE 必须为正（限流不可关闭；要放宽请填一个更大的值），got %s", v)
		}
		c.RateLimit.PerMinute = n
	}
	if v := strings.TrimSpace(getenv("XM_RATE_LIMIT_BURST")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return config{}, fmt.Errorf("XM_RATE_LIMIT_BURST: %w", err)
		}
		if n <= 0 {
			return config{}, fmt.Errorf("XM_RATE_LIMIT_BURST 必须为正，got %s", v)
		}
		c.RateLimit.Burst = n
	}

	if value := strings.TrimSpace(getenv("XM_FINANCE_FAKE_SEED")); value != "" {
		seed, err := strconv.ParseBool(value)
		if err != nil {
			// 非法值一律拒绝启动，不回落成 false：「以为开了但没开」会让人
			// 对着一片空看板查半天采集链路。
			return config{}, fmt.Errorf("XM_FINANCE_FAKE_SEED=%q 必须是布尔值: %w", value, err)
		}
		c.FinanceDemoSeed = seed
	}

	auth, err := authConfigFromEnv(getenv, c.Environment)
	if err != nil {
		return config{}, err
	}
	c.Auth = auth

	reqlogCfg, err := reqlogConfigFromEnv(getenv)
	if err != nil {
		return config{}, err
	}
	c.Reqlog = reqlogCfg

	cpaCfg, err := cpaConfigFromEnv(getenv)
	if err != nil {
		return config{}, err
	}
	c.CPA = cpaCfg

	// XM_CONSOLE_ADMIN_IP_ALLOWLIST（XM-AUTH-TOTP0）：非法 CIDR 一律拒绝
	// 启动，不回落成"未启用"——一个写错的 CIDR 段本该收紧访问却悄悄放开，
	// 比进程直接起不来更危险（宪法同一条 Fail Closed 精神）。
	allowlist, err := localauth.ParseAdminIPAllowlist(getenv("XM_CONSOLE_ADMIN_IP_ALLOWLIST"))
	if err != nil {
		return config{}, err
	}
	c.ConsoleAdminIPAllowlist = allowlist

	return c, nil
}

// authConfigFromEnv 只允许本地会话及非生产开发头，错误配置拒绝启动。
func authConfigFromEnv(getenv func(string) string, environment string) (authConfig, error) {
	// 旧配置必须显式移除；尤其权限覆盖不能在升级时静默丢失。
	for _, key := range []string{
		"XM_OIDC_ISSUER", "XM_OIDC_AUDIENCE", "XM_OIDC_JWKS_URL",
		"XM_OIDC_ROLE_SCOPES", "XM_OIDC_CLOCK_SKEW",
		"XM_INVOICE_CONSOLE_ASSERTION_ENABLED", "XM_INVOICE_CONSOLE_ASSERTION_ISSUER",
		"XM_INVOICE_CONSOLE_ASSERTION_AUDIENCE", "XM_INVOICE_CONSOLE_ASSERTION_KEY_REF",
		"XM_INVOICE_CONSOLE_ASSERTION_STEP_UP_MAX_AGE",
	} {
		if strings.TrimSpace(getenv(key)) != "" {
			return authConfig{}, fmt.Errorf("%s 已退役，请移除；员工权限覆盖使用 XM_AUTH_ROLE_SCOPES", key)
		}
	}
	mode := strings.TrimSpace(getenv("XM_AUTH_MODE"))
	if mode == "" {
		if environment == "production" {
			mode = string(authModeLocal)
		} else {
			mode = string(authModeDevHeader)
		}
	}
	a := authConfig{Mode: authMode(mode)}
	switch a.Mode {
	case authModeLocal:
	case authModeDevHeader:
		if environment == "production" {
			return authConfig{}, fmt.Errorf("XM_AUTH_MODE=dev-header 不允许在生产环境使用；生产请使用 local")
		}
	default:
		return authConfig{}, fmt.Errorf("XM_AUTH_MODE 必须是 local 或非生产 dev-header，got %q", mode)
	}
	roleScopes, err := rolepermissions.ParseRoleScopeMap(getenv("XM_AUTH_ROLE_SCOPES"))
	if err != nil {
		return authConfig{}, fmt.Errorf("XM_AUTH_ROLE_SCOPES: %w", err)
	}
	a.RoleScopes = roleScopes
	return a, nil
}
