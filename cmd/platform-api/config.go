package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
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
	// Reqlog 是「请求详情」这条链路的配置（XM-0039）。
	// 同样不含机密：凭据只有 CredentialRef 形态的引用（宪法 7 条）。
	Reqlog reqlogConfig

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
	// 唯一的读路径是 XM-USERS-REAL 的 real 模式（platformUsersSecretProvider，
	// 见 cmd/platform-api/platformusers.go），用来把 core.connector_config 里
	// 的 CredentialRef 解析成请求头。
	// 目录本身不是机密，路径可以进日志；目录里的文件永远不能。
	SecretRoot string
}

// defaultSecretRoot 与 deploy/compose/launch.yaml 里 xm-secrets 卷的挂载点一致。
const defaultSecretRoot = "/run/xm/secrets"

// authMode 是身份解析器的选择开关（XM_AUTH_MODE）。
type authMode string

const (
	// authModeDevHeader：身份来自 X-Dev-* 请求头，仅非生产（Foundation-A 现状）。
	authModeDevHeader authMode = "dev-header"
	// authModeOIDC：身份来自 Keycloak 的 Access Token（XM-0008）。
	authModeOIDC authMode = "oidc"
	// authModeLocal：身份来自平台自带的账号库（XM-LOGIN），账号与口令哈希
	// 落在 core.staff_account，会话是服务端持有状态的 Cookie。与 dev-header
	// 不同，它不是"请求头自称身份"——账号需要 cmd/staff-bootstrap 或
	// staff.manage Action 显式创建，口令走 argon2id 校验，因此**允许在生产
	// 使用**（不像 dev-header 那样被硬性禁止）。
	authModeLocal authMode = "local"
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
	return c, nil
}

// authConfigFromEnv 解析身份相关配置，全程 Fail Closed。
//
// 四条不可协商的规则：
//
//  1. **生产只允许 oidc 或 local**。dev-header 让调用方用请求头自称身份，在
//     生产等于没有鉴权。httpapi.NewDevHeaderResolver 里已经有一道硬拒绝，
//     这里再挡一次——那道闸在「构造解析器」时才触发，这道在「读配置」时就
//     触发，报错信息也能直接说清楚该怎么改；local（XM-LOGIN）不在此列——
//     账号与口令哈希都在平台自己的库里，有真实的鉴权语义；
//  2. **oidc 缺 issuer 或 audience 拒绝启动**。少了 issuer 就没有信任根，少了
//     audience 就等于接受任何 Client 拿到的令牌。两者都不能有默认值；
//  3. **非生产默认 dev-header**。XM-0008 不改现状：development / staging 的栈
//     照常跑，切换是显式动作（把 XM_AUTH_MODE 设成 oidc 或 local）；
//  4. **XM_AUTH_MODE 是 dev-header / oidc / local 之外的任何值一律拒绝启动**，
//     不静默回落——回落等于让一次拼写错误变成一次静默的鉴权降级。
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
					"并配置 XM_OIDC_ISSUER / XM_OIDC_AUDIENCE（前置条件：CR-0001 已执行），" +
					"或设 XM_AUTH_MODE=local 使用平台自带登录（XM-LOGIN）")
		}
	case authModeOIDC:
	case authModeLocal:
		// 生产允许：账号与口令哈希落在平台自己的库里，不是「请求头自称身份」。
		// 需要先用 cmd/staff-bootstrap 建出第一个管理员账号，见
		// docs/modules/httpapi/AUTH-SWITCH.md「local 模式」一节。
	default:
		return authConfig{}, fmt.Errorf("XM_AUTH_MODE 必须是 dev-header / oidc / local 之一，got %q", mode)
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
