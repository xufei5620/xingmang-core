package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/url"
	"strings"
	"testing"
)

// 与 cmd/platform-worker/database_test.go 同构：两个进程连同一个库，
// 只在一边验证等于没验证。

func databaseTestURL(host, password string) string {
	u := url.URL{Scheme: "postgres", Host: host, Path: "/xingmang", User: url.User("xingmang")}
	if password != "" {
		u.User = url.UserPassword("xingmang", password)
	}
	return u.String()
}

func TestDatabaseURLFromEnvRequiresValue(t *testing.T) {
	if _, err := databaseURLFromEnv(context.Background(),
		func(string) string { return "" }, slog.Default()); err == nil {
		t.Fatal("缺 DATABASE_URL 应报错")
	}
}

func TestDatabaseURLFromEnvAcceptsLegacyName(t *testing.T) {
	// 旧变量名仍可用（docs/modules/httpapi/RUNBOOK.md 与 README 的本地流程），
	// 但走的是同一条校验路径
	values := map[string]string{
		"ENVIRONMENT":     "development",
		"XM_DATABASE_URL": databaseTestURL("localhost:5432", "dev-"+"value") + "?sslmode=disable",
	}
	got, err := databaseURLFromEnv(context.Background(),
		func(key string) string { return values[key] }, slog.Default())
	if err != nil || got != values["XM_DATABASE_URL"] {
		t.Fatalf("databaseURLFromEnv = %q, %v", got, err)
	}
}

func TestDatabaseURLFromEnvPrefersCanonicalName(t *testing.T) {
	// 两个都配时以 DATABASE_URL 为准：compose 与 worker 用的是这个名字，
	// 让「实际连哪里」只有一个可信来源
	values := map[string]string{
		"ENVIRONMENT":     "development",
		"DATABASE_URL":    databaseTestURL("localhost:5432", ""),
		"XM_DATABASE_URL": databaseTestURL("other.example.com:5432", ""),
	}
	got, err := databaseURLFromEnv(context.Background(),
		func(key string) string { return values[key] }, slog.Default())
	if err != nil || got != values["DATABASE_URL"] {
		t.Fatalf("databaseURLFromEnv = %q, %v", got, err)
	}
}

func TestDatabaseURLFromEnvAllowsEndpointWithoutInlinePassword(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":  "staging",
		"DATABASE_URL": databaseTestURL("postgres:5432", "") + "?sslmode=disable",
	}
	got, err := databaseURLFromEnv(context.Background(),
		func(key string) string { return values[key] }, slog.Default())
	if err != nil || got != values["DATABASE_URL"] {
		t.Fatalf("databaseURLFromEnv = %q, %v", got, err)
	}
}

func TestDatabaseURLFromEnvRejectsInlinePasswordOutsideDevelopment(t *testing.T) {
	// staging 也算「非开发」：验收环境的库同样不许把密码写进 DSN
	for _, environment := range []string{"staging", "production"} {
		values := map[string]string{
			"ENVIRONMENT":  environment,
			"DATABASE_URL": databaseTestURL("db.internal:5432", "inline-"+"value"),
		}
		if _, err := databaseURLFromEnv(context.Background(),
			func(key string) string { return values[key] }, slog.Default()); err == nil {
			t.Fatalf("%s 的内联密码应被拒绝", environment)
		}
	}
}

func TestDatabaseURLFromEnvResolvesPasswordRef(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":           "staging",
		"DATABASE_URL":          databaseTestURL("postgres:5432", ""),
		"DATABASE_PASSWORD_REF": "secret://database/postgres-password",
		"DATABASE_PASSWORD":     "test-" + "value",
	}
	got, err := databaseURLFromEnv(context.Background(),
		func(key string) string { return values[key] }, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(got)
	if err != nil || parsed.User == nil {
		t.Fatalf("resolved URL = %q, parse error = %v", got, err)
	}
	password, ok := parsed.User.Password()
	if !ok || password != values["DATABASE_PASSWORD"] {
		t.Fatalf("resolved password = %q, want provider value", password)
	}
}

func TestDatabaseURLFromEnvRejectsInlinePasswordEvenWithRef(t *testing.T) {
	// 配了 ref 却还留着内联密码：说明有人只改了一半，必须失败而不是「二选一」
	for _, environment := range []string{"development", "staging"} {
		values := map[string]string{
			"ENVIRONMENT":           environment,
			"DATABASE_URL":          databaseTestURL("localhost:5432", "stale-"+"value"),
			"DATABASE_PASSWORD_REF": "secret://database/postgres-password",
			"DATABASE_PASSWORD":     "provider-" + "value",
		}
		if _, err := databaseURLFromEnv(context.Background(),
			func(key string) string { return values[key] }, slog.Default()); err == nil {
			t.Fatalf("%s 下 ref 与内联密码并存应被拒绝", environment)
		}
	}
}

func TestDatabaseURLFromEnvRejectsUnknownPasswordRef(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":           "staging",
		"DATABASE_URL":          databaseTestURL("postgres:5432", ""),
		"DATABASE_PASSWORD_REF": "not-a-credential-ref",
	}
	if _, err := databaseURLFromEnv(context.Background(),
		func(key string) string { return values[key] }, slog.Default()); err == nil {
		t.Fatal("非法 CredentialRef 应报错")
	}
}

func TestDatabaseURLFromEnvDoesNotLogPassword(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":           "staging",
		"DATABASE_URL":          databaseTestURL("postgres:5432", ""),
		"DATABASE_PASSWORD_REF": "secret://database/postgres-password",
		"DATABASE_PASSWORD":     "test-" + "value",
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	if _, err := databaseURLFromEnv(context.Background(),
		func(key string) string { return values[key] }, logger); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), values["DATABASE_PASSWORD"]) {
		t.Fatalf("database password leaked into logs: %s", logs.String())
	}
}

// --- query 参数绕过（红队 issue #33，与 worker 同一组用例）---

func TestDatabaseURLFromEnvRejectsQueryPassword(t *testing.T) {
	// ?password= 不在 url.User 里，自己解析 URL 判断「有没有内联密码」会返回
	// false，而 pgx 照用不误——所以判定必须问 pgx（pgdsn 的存在理由）
	for _, environment := range []string{"development", "staging", "production"} {
		values := map[string]string{
			"ENVIRONMENT":  environment,
			"DATABASE_URL": "postgres://xingmang@postgres:5432/xingmang?password=" + "leak" + "ed",
		}
		got, err := databaseURLFromEnv(context.Background(),
			func(key string) string { return values[key] }, slog.Default())
		if err == nil {
			t.Fatalf("%s 下 query 形态的密码应被拒绝, got %q", environment, got)
		}
	}
}

func TestDatabaseURLFromEnvRejectsHostOverride(t *testing.T) {
	// DSN 声明连 postgres，pgx 实际会连 elsewhere.example.com
	values := map[string]string{
		"ENVIRONMENT":  "staging",
		"DATABASE_URL": "postgres://xingmang@postgres:5432/xingmang?host=elsewhere.example.com",
	}
	if _, err := databaseURLFromEnv(context.Background(),
		func(key string) string { return values[key] }, slog.Default()); err == nil {
		t.Fatal("query 覆盖主机应被拒绝")
	}
}

func TestDatabaseURLFromEnvRejectsUnknownQueryParams(t *testing.T) {
	// 白名单语义：不认识的参数一律拒绝。黑名单漏一个就是一个绕过口子
	for _, raw := range []string{
		"postgres://xingmang@postgres:5432/xingmang?passfile=/tmp/pw",
		"postgres://xingmang@postgres:5432/xingmang?service=prod",
	} {
		values := map[string]string{"ENVIRONMENT": "staging", "DATABASE_URL": raw}
		if _, err := databaseURLFromEnv(context.Background(),
			func(key string) string { return values[key] }, slog.Default()); err == nil {
			t.Fatalf("未许可的查询参数应被拒绝: %s", raw)
		}
	}
}

func TestDatabaseURLFromEnvRejectsNonPostgresScheme(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":  "staging",
		"DATABASE_URL": "mysql://xingmang@db:3306/xingmang",
	}
	if _, err := databaseURLFromEnv(context.Background(),
		func(key string) string { return values[key] }, slog.Default()); err == nil {
		t.Fatal("非 postgres scheme 应被拒绝")
	}
}
