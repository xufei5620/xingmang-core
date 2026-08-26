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
