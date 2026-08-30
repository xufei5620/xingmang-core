package main

import (
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// 数据库连接串的用例已搬到 database_test.go：它现在走 CredentialRef 纪律，
// 和「监听地址、超时」这类明文配置不是一类东西。

func TestConfigFromEnvRequiresEnvironment(t *testing.T) {
	if _, err := configFromEnv(env(map[string]string{})); err == nil {
		t.Fatal("缺 ENVIRONMENT 应报错")
	}
}

func TestConfigFromEnvDefaults(t *testing.T) {
	c, err := configFromEnv(env(map[string]string{"ENVIRONMENT": "development"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != "127.0.0.1:8080" {
		t.Fatalf("默认监听地址应绑回环（规格 §21.2 由宿主 Nginx 反代）, got %q", c.ListenAddr)
	}
	if c.RequestTimeout != 30*time.Second {
		t.Fatalf("默认超时 = %v", c.RequestTimeout)
	}
}

func TestConfigFromEnvRejectsUnknownEnvironment(t *testing.T) {
	if _, err := configFromEnv(env(map[string]string{"ENVIRONMENT": "prod"})); err == nil {
		t.Fatal("非法 ENVIRONMENT 应报错")
	}
}

// 容器里必须能绑 0.0.0.0：默认值是给「宿主直跑 + Nginx 反代」的形态用的，
// compose 形态由 LISTEN_ADDR 覆盖（deploy/compose/launch.yaml）。
func TestConfigFromEnvOverridesListenAddr(t *testing.T) {
	c, err := configFromEnv(env(map[string]string{
		"ENVIRONMENT": "staging",
		"LISTEN_ADDR": "0.0.0.0:8080",
	}))
	if err != nil || c.ListenAddr != "0.0.0.0:8080" {
		t.Fatalf("c = %+v, err = %v", c, err)
	}
}

func TestConfigFromEnvParsesTimeout(t *testing.T) {
	c, err := configFromEnv(env(map[string]string{
		"ENVIRONMENT":     "staging",
		"REQUEST_TIMEOUT": "5s",
	}))
	if err != nil || c.RequestTimeout != 5*time.Second {
		t.Fatalf("c = %+v, err = %v", c, err)
	}
	for name, v := range map[string]string{"非法格式": "abc", "零值": "0s", "负值": "-1s"} {
		if _, err := configFromEnv(env(map[string]string{
			"ENVIRONMENT":     "staging",
			"REQUEST_TIMEOUT": v,
		})); err == nil {
			t.Fatalf("%s 的超时应报错", name)
		}
	}
}

// 凭据文件根目录（XM-CRED0）：默认与 compose 的 xm-secrets 挂载点一致，可覆盖。
func TestConfigFromEnvSecretRoot(t *testing.T) {
	c, err := configFromEnv(env(map[string]string{"ENVIRONMENT": "development"}))
	if err != nil || c.SecretRoot != "/run/xm/secrets" {
		t.Fatalf("默认 SecretRoot = %q, err = %v", c.SecretRoot, err)
	}
	c, err = configFromEnv(env(map[string]string{
		"ENVIRONMENT":    "development",
		"XM_SECRET_ROOT": " /var/lib/xm/secrets ",
	}))
	if err != nil || c.SecretRoot != "/var/lib/xm/secrets" {
		t.Fatalf("覆盖后 SecretRoot = %q, err = %v", c.SecretRoot, err)
	}
}
