package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/url"
	"strings"
	"testing"
)

func databaseTestURL(host, password string) string {
	u := url.URL{Scheme: "postgres", Host: host, Path: "/xingmang", User: url.User("xingmang")}
	if password != "" {
		u.User = url.UserPassword("xingmang", password)
	}
	return u.String()
}

func TestDatabaseURLFromEnvAllowsEndpointWithoutInlinePassword(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":  "production",
		"DATABASE_URL": databaseTestURL("db.internal:5432", "") + "?sslmode=disable",
	}
	got, err := databaseURLFromEnv(context.Background(), func(key string) string { return values[key] }, slog.Default())
	if err != nil || got != values["DATABASE_URL"] {
		t.Fatalf("databaseURLFromEnv = %q, %v", got, err)
	}
}

func TestDatabaseURLFromEnvRejectsInlinePasswordOutsideDevelopment(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":  "production",
		"DATABASE_URL": databaseTestURL("db.internal:5432", "inline-"+"value"),
	}
	if _, err := databaseURLFromEnv(context.Background(), func(key string) string { return values[key] }, slog.Default()); err == nil {
		t.Fatal("production inline database password should be rejected")
	}
}

func TestDatabaseURLFromEnvResolvesPasswordRef(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":           "production",
		"DATABASE_URL":          databaseTestURL("db.internal:5432", ""),
		"DATABASE_PASSWORD_REF": "secret://database/postgres-password",
		"DATABASE_PASSWORD":     "test-" + "value",
	}
	got, err := databaseURLFromEnv(context.Background(), func(key string) string { return values[key] }, slog.Default())
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

func TestDatabaseURLFromEnvRejectsInlinePasswordEvenWithRefInProduction(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":           "production",
		"DATABASE_URL":          databaseTestURL("db.internal:5432", "stale-"+"value"),
		"DATABASE_PASSWORD_REF": "secret://database/postgres-password",
		"DATABASE_PASSWORD":     "provider-" + "value",
	}
	if _, err := databaseURLFromEnv(context.Background(), func(key string) string { return values[key] }, slog.Default()); err == nil {
		t.Fatal("production inline password must fail closed even when a ref is present")
	}
}

func TestDatabaseURLFromEnvRejectsInlinePasswordWithRefInDevelopment(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":           "development",
		"DATABASE_URL":          databaseTestURL("localhost:5432", "stale-"+"value"),
		"DATABASE_PASSWORD_REF": "secret://database/postgres-password",
		"DATABASE_PASSWORD":     "provider-" + "value",
	}
	if _, err := databaseURLFromEnv(context.Background(), func(key string) string { return values[key] }, slog.Default()); err == nil {
		t.Fatal("inline password must be removed before using a password ref")
	}
}

func TestDatabaseURLFromEnvDoesNotLogPassword(t *testing.T) {
	values := map[string]string{
		"ENVIRONMENT":           "production",
		"DATABASE_URL":          databaseTestURL("db.internal:5432", ""),
		"DATABASE_PASSWORD_REF": "secret://database/postgres-password",
		"DATABASE_PASSWORD":     "test-" + "value",
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	if _, err := databaseURLFromEnv(context.Background(), func(key string) string { return values[key] }, logger); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), values["DATABASE_PASSWORD"]) {
		t.Fatalf("database password leaked into logs: %s", logs.String())
	}
}

// --- XM-R008：query 参数绕过（红队 issue #33）---

func TestDatabaseURLFromEnvRejectsQueryPasswordInProduction(t *testing.T) {
	// 修复前：?password= 不在 url.User 里，「有没有内联密码」的判断为 false，
	// DSN 被原样返回，pgx 用这个明文密码连库——完全绕开 CredentialRef。
	values := map[string]string{
		"ENVIRONMENT":  "production",
		"DATABASE_URL": "postgres://xingmang@db.internal:5432/xingmang?password=" + "leak" + "ed",
	}
	got, err := databaseURLFromEnv(context.Background(),
		func(key string) string { return values[key] }, slog.Default())
	if err == nil {
		t.Fatalf("query 形态的密码应被拒绝, got %q", got)
	}
}

func TestDatabaseURLFromEnvRejectsQueryPasswordEvenInDevelopment(t *testing.T) {
	// 开发环境允许 userinfo 内联密码（README 的本地流程），但不允许 query
	// 形态——它在生产同样能用，留着就是留一条绕过路径的肌肉记忆
	values := map[string]string{
		"ENVIRONMENT":  "development",
		"DATABASE_URL": "postgres://xingmang@localhost:5432/xingmang?password=" + "dev" + "pw",
	}
	if _, err := databaseURLFromEnv(context.Background(),
		func(key string) string { return values[key] }, slog.Default()); err == nil {
		t.Fatal("query 形态的密码在任何环境都应被拒绝")
	}
}

func TestDatabaseURLFromEnvRejectsHostOverride(t *testing.T) {
	// DSN 声明连 db.internal，pgx 实际会连 elsewhere.example.com。
	// 这种「看起来连 A 实际连 B」的配置在任何环境都不该放行
	values := map[string]string{
		"ENVIRONMENT":  "production",
		"DATABASE_URL": "postgres://xingmang@db.internal:5432/xingmang?host=elsewhere.example.com",
	}
	if _, err := databaseURLFromEnv(context.Background(),
		func(key string) string { return values[key] }, slog.Default()); err == nil {
		t.Fatal("query 覆盖主机应被拒绝")
	}
}

func TestDatabaseURLFromEnvRejectsUnknownQueryParams(t *testing.T) {
	// 白名单语义：不认识的参数一律拒绝。黑名单漏一个就是一个绕过口子
	values := map[string]string{
		"ENVIRONMENT":  "production",
		"DATABASE_URL": "postgres://xingmang@db.internal:5432/xingmang?passfile=/tmp/pw",
	}
	if _, err := databaseURLFromEnv(context.Background(),
		func(key string) string { return values[key] }, slog.Default()); err == nil {
		t.Fatal("未许可的查询参数应被拒绝")
	}
}
