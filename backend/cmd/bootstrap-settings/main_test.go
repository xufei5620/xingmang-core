package main

import (
	"os"
	"path/filepath"
	"testing"

	"invoice-system/backend/internal/adminsettings"
)

func TestReadBootstrapSettingsStrict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	body := `{"issuer_name":"主体","minimum_request_minor":20000,"eligibility_start_at":"2026-09-01T00:00:00+08:00","smtp_host":"smtp.qq.com","smtp_port":587,"smtp_from":"invoice@qq.com","smtp_from_name":"发票中心","smtp_starttls":true,"admin_cidrs":["203.0.113.10/32"]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := readBootstrapSettings(path)
	if err != nil || settings.IssuerName != "主体" || settings.MinimumRequestMinor != 20_000 ||
		settings.EligibilityStartAt != adminsettings.EligibilityStartAtRFC3339 {
		t.Fatalf("settings=%+v err=%v", settings, err)
	}
	if err = os.WriteFile(path, []byte(body+` {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = readBootstrapSettings(path); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
}

func TestReadOneLineSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(path, []byte("postgres://example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := readOneLineSecret(path)
	if err != nil || value != "postgres://example" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	if err = os.WriteFile(path, []byte("one\ntwo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = readOneLineSecret(path); err == nil {
		t.Fatal("multi-line secret was accepted")
	}
}
