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

func TestProductionMigrationRequiresExactEligibilityBoundary(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	for _, valid := range []string{"2026-09-01T00:00:00+08:00", "2026-08-31T16:00:00Z"} {
		t.Setenv("ELIGIBILITY_START_AT", valid)
		if err := validateMigrationEligibilityPolicy(); err != nil {
			t.Fatalf("valid boundary %q rejected: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "2026-09-01", "2026-08-31T15:59:59.999999Z", "2026-08-31T16:00:00.000001Z"} {
		t.Setenv("ELIGIBILITY_START_AT", invalid)
		if err := validateMigrationEligibilityPolicy(); err == nil {
			t.Fatalf("invalid production migration boundary %q accepted", invalid)
		}
	}
}
