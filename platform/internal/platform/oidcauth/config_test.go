package oidcauth

import (
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/httpapi"
)

// 编译期断言：Resolver 必须满足 httpapi.PrincipalResolver。
//
// 本包**不导入** httpapi（否则就成了反向依赖），靠的是 Go 接口的结构化满足；
// 这行断言只存在于测试里，作用是让「有人改了 Resolve 的签名」在本包就暴露，
// 而不是等到 cmd/platform-api 装配时才报错。
var _ httpapi.PrincipalResolver = (*Resolver)(nil)

const goodIssuer = "https://auth.solov.cc/realms/solov-staff"

func baseConfig() Config {
	return Config{
		IssuerURL:   goodIssuer,
		Audience:    "xingmang-admin-web",
		Environment: "staging",
	}
}

// 构造期就该炸掉的配置错误：它们不需要网络就能发现，留到运行时只会表现成
// 「所有人都登不进来」，排查成本差一个量级。
func TestNewOIDCResolverRejectsBadConfig(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"缺 IssuerURL":        func(c *Config) { c.IssuerURL = "" },
		"IssuerURL 带尾斜杠":     func(c *Config) { c.IssuerURL = goodIssuer + "/" },
		"IssuerURL 不是绝对 URL": func(c *Config) { c.IssuerURL = "auth.solov.cc/realms/solov-staff" },
		"缺 Audience":         func(c *Config) { c.Audience = "" },
		"缺 Environment":      func(c *Config) { c.Environment = "" },
		"生产用 http": func(c *Config) {
			c.Environment = "production"
			c.IssuerURL = "http://auth.solov.cc/realms/solov-staff"
		},
		"JWKSURL 跨源":        func(c *Config) { c.JWKSURL = "https://evil.example.com/certs" },
		"RoleScopeMap 显式为空": func(c *Config) { c.RoleScopeMap = map[string][]string{} },
		"JWKSMaxAge 小于 TTL": func(c *Config) {
			c.JWKSTTL = time.Hour
			c.JWKSMaxAge = time.Minute
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := baseConfig()
			mutate(&cfg)
			if _, err := NewOIDCResolver(cfg); err == nil {
				t.Fatal("应拒绝构造")
			}
		})
	}
}

func TestNewOIDCResolverAcceptsProductionHTTPS(t *testing.T) {
	cfg := baseConfig()
	cfg.Environment = "production"
	if _, err := NewOIDCResolver(cfg); err != nil {
		t.Fatalf("生产 + https 应可构造: %v", err)
	}
}

// 构造**不得**发起任何网络请求：把启动挂在 Keycloak 的可用性上，意味着身份
// 服务抖一下平台连 /healthz 都起不来。这里用一个绝对连不通的地址来证明。
func TestNewOIDCResolverDoesNotTouchNetwork(t *testing.T) {
	cfg := baseConfig()
	// 保留 TEST-NET-1（RFC 5737），任何环境都不该有东西在上面应答
	cfg.IssuerURL = "https://192.0.2.1/realms/solov-staff"
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := NewOIDCResolver(cfg); err != nil {
			t.Errorf("构造不该依赖网络: %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("构造疑似发起了网络请求（超过 3 秒未返回）")
	}
}

func TestNewOIDCResolverFillsDefaults(t *testing.T) {
	r, err := NewOIDCResolver(baseConfig())
	if err != nil {
		t.Fatal(err)
	}
	if r.cfg.ClockSkew != defaultClockSkew {
		t.Errorf("ClockSkew = %v, want %v", r.cfg.ClockSkew, defaultClockSkew)
	}
	if r.cfg.JWKSTTL != defaultJWKSTTL {
		t.Errorf("JWKSTTL = %v", r.cfg.JWKSTTL)
	}
	if r.cfg.JWKSMaxAge != defaultJWKSMaxAge {
		t.Errorf("JWKSMaxAge = %v", r.cfg.JWKSMaxAge)
	}
	// 零值必须落成**默认冷却**而不是「无冷却」：cmd/platform-api 就是不填这个
	// 字段的调用方，落成 0 等于把 JWKS 刷新的放大攻击面默认打开
	if r.cfg.JWKSRefreshCooldown != defaultJWKSRefreshCooldown {
		t.Errorf("JWKSRefreshCooldown = %v, want %v（零值不得表示无冷却）",
			r.cfg.JWKSRefreshCooldown, defaultJWKSRefreshCooldown)
	}
	if len(r.cfg.RoleScopeMap) == 0 {
		t.Error("RoleScopeMap 应回落到默认表")
	}
	if r.keys.client == nil || r.keys.client.Timeout <= 0 {
		t.Error("默认 HTTP 客户端必须带超时（规格 §18.1-4）")
	}
}

// 启动日志要能回答「当时到底配的什么」——授权策略出问题时第一个要看的就是它。
func TestNewOIDCResolverLogsRoleScopeMap(t *testing.T) {
	capture := newLogCapture()
	cfg := baseConfig()
	cfg.Logger = capture.logger()
	if _, err := NewOIDCResolver(cfg); err != nil {
		t.Fatal(err)
	}
	rec, ok := capture.find("oidc_resolver_ready")
	if !ok {
		t.Fatalf("构造应记一条启动日志:\n%s", capture.text())
	}
	if rec["issuer"] != goodIssuer || rec["audience"] != "xingmang-admin-web" {
		t.Errorf("启动日志缺关键字段: %v", rec)
	}
	if !strings.Contains(capture.text(), "role_scope_map") {
		t.Error("启动日志应包含角色映射表")
	}
}
