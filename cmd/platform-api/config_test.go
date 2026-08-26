package main

import (
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestConfigFromEnvRequiresEnvironmentAndDatabase(t *testing.T) {
	if _, err := configFromEnv(env(map[string]string{})); err == nil {
		t.Fatal("缺 ENVIRONMENT 应报错")
	}
	if _, err := configFromEnv(env(map[string]string{"ENVIRONMENT": "development"})); err == nil {
		t.Fatal("缺 XM_DATABASE_URL 应报错")
	}
}

func TestConfigFromEnvDefaults(t *testing.T) {
	c, err := configFromEnv(env(map[string]string{
		"ENVIRONMENT":     "development",
		"XM_DATABASE_URL": "postgres://u:p@localhost:5432/db",
	}))
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
	if _, err := configFromEnv(env(map[string]string{
		"ENVIRONMENT":     "prod",
		"XM_DATABASE_URL": "postgres://u:p@localhost:5432/db",
	})); err == nil {
		t.Fatal("非法 ENVIRONMENT 应报错")
	}
}

func TestConfigFromEnvParsesTimeout(t *testing.T) {
	c, err := configFromEnv(env(map[string]string{
		"ENVIRONMENT":     "staging",
		"XM_DATABASE_URL": "postgres://u:p@localhost:5432/db",
		"REQUEST_TIMEOUT": "5s",
	}))
	if err != nil || c.RequestTimeout != 5*time.Second {
		t.Fatalf("c = %+v, err = %v", c, err)
	}
	for name, v := range map[string]string{"非法格式": "abc", "零值": "0s", "负值": "-1s"} {
		if _, err := configFromEnv(env(map[string]string{
			"ENVIRONMENT":     "staging",
			"XM_DATABASE_URL": "postgres://u:p@localhost:5432/db",
			"REQUEST_TIMEOUT": v,
		})); err == nil {
			t.Fatalf("%s 的超时应报错", name)
		}
	}
}
