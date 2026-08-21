package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrationDatabaseURLProductionRequiresFile(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://plaintext")
	t.Setenv("DATABASE_URL_FILE", "")
	if _, err := migrationDatabaseURL(); err == nil {
		t.Fatal("production accepted plaintext DATABASE_URL")
	}
	path := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(path, []byte("postgres://file-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DATABASE_URL_FILE", path)
	value, err := migrationDatabaseURL()
	if err != nil || value != "postgres://file-value" {
		t.Fatalf("value=%q err=%v", value, err)
	}
}

func TestMigrationDatabaseURLDevelopmentAllowsEnvironment(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATABASE_URL_FILE", "")
	t.Setenv("DATABASE_URL", "postgres://development")
	value, err := migrationDatabaseURL()
	if err != nil || value != "postgres://development" {
		t.Fatalf("value=%q err=%v", value, err)
	}
}
