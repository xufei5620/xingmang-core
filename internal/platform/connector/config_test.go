package connector

import (
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		ServiceInstanceID: "sub2api-prod",
		Environment:       "production",
		Endpoint:          "https://api.solov.cc",
		CredentialRef:     "secret://sub2api-prod/read-only-admin",
		TargetAllowlist:   []string{"api.solov.cc"},
		Timeout:           30 * time.Second,
	}
}

func TestConfigValidateAcceptsValid(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("合法配置被拒绝: %v", err)
	}
}

func TestConfigRejectsPlaintextCredential(t *testing.T) {
	// ADR-014 / 宪法 7 条：只接受 CredentialRef，内联明文一律拒绝
	for name, cred := range map[string]string{
		"数据库 DSN": "postgres://user:pass@host:5432/db",
		// 刻意不写成真实密钥的形态：测试假值只要不是合法 CredentialRef 即可，
		// 长得像 live key 会被 secret-scan 判为泄漏（实测触发过一次）
		"裸 token": "inline-token-placeholder-not-a-ref",
		"空":       "",
		"形态不对":    "secret:/missing-slash/name",
	} {
		c := validConfig()
		c.CredentialRef = cred
		if err := c.Validate(); err == nil {
			t.Fatalf("%s 应被拒绝", name)
		}
	}
}

func TestConfigRejectsNonHTTPS(t *testing.T) {
	for _, ep := range []string{
		"http://api.solov.cc",
		"ftp://api.solov.cc",
		"api.solov.cc",
	} {
		c := validConfig()
		c.Endpoint = ep
		if err := c.Validate(); err == nil {
			t.Fatalf("非 https endpoint %q 应被拒绝", ep)
		}
	}
}

func TestConfigRejectsWriteIntentParams(t *testing.T) {
	// ADR-018 闸 1：拒绝可写连接配置
	for _, ep := range []string{
		"https://api.solov.cc?readonly=false",
		"https://api.solov.cc?mode=rw",
		"https://api.solov.cc?read_only=false&x=1",
	} {
		c := validConfig()
		c.Endpoint = ep
		if err := c.Validate(); err == nil {
			t.Fatalf("含可写意图参数的 endpoint %q 应被拒绝", ep)
		}
	}
}

func TestConfigRequiresAllowlistContainingEndpoint(t *testing.T) {
	c := validConfig()
	c.TargetAllowlist = nil
	if err := c.Validate(); err == nil {
		t.Fatal("空 allowlist 应被拒绝（ADR-004）")
	}

	// 配置自相矛盾：endpoint 主机不在自己的 allowlist 内。
	// 这种配置能连上才怪，但不校验的话故障现象是「一个请求都发不出去」，极难排查
	c = validConfig()
	c.TargetAllowlist = []string{"other.example.com"}
	if err := c.Validate(); err == nil {
		t.Fatal("endpoint 主机不在 allowlist 内应被拒绝")
	}

	// 端口不影响匹配
	c = validConfig()
	c.Endpoint = "https://api.solov.cc:8443"
	if err := c.Validate(); err != nil {
		t.Fatalf("带端口的 endpoint 应能匹配 allowlist: %v", err)
	}
}

func TestConfigRequiresTimeout(t *testing.T) {
	// 规格 §18.1-4：所有外部 I/O 必须有超时
	for _, d := range []time.Duration{0, -time.Second} {
		c := validConfig()
		c.Timeout = d
		if err := c.Validate(); err == nil {
			t.Fatalf("超时 %v 应被拒绝", d)
		}
	}
}

func TestConfigRequiresIdentityFields(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"空 service_instance_id": func(c *Config) { c.ServiceInstanceID = "" },
		"空 environment":         func(c *Config) { c.Environment = "" },
		"空 endpoint":            func(c *Config) { c.Endpoint = "" },
	} {
		c := validConfig()
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Fatalf("%s 应被拒绝", name)
		}
	}
}
