package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadBreakGlassCIDRsPrefersDeploymentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "break-glass-cidrs")
	if err := os.WriteFile(path, []byte("203.0.113.8/32\n2001:db8::1/128\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADMIN_BREAK_GLASS_CIDRS_FILE", path)
	t.Setenv("ADMIN_BOOTSTRAP_IP_ALLOWLIST", "127.0.0.1/32")
	got, err := loadBreakGlassCIDRs("mock")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"203.0.113.8/32", "2001:db8::1/128"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CIDRs=%v want %v", got, want)
	}
}

func TestLoadBreakGlassCIDRsEnvFallbackIsMockOnly(t *testing.T) {
	t.Setenv("ADMIN_BREAK_GLASS_CIDRS_FILE", "")
	t.Setenv("ADMIN_BOOTSTRAP_IP_ALLOWLIST", "127.0.0.1/32")
	if _, err := loadBreakGlassCIDRs("oidc"); err == nil {
		t.Fatal("production accepted environment fallback")
	}
	got, err := loadBreakGlassCIDRs("mock")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "127.0.0.1/32" {
		t.Fatalf("CIDRs=%v", got)
	}
}

func TestExactHTTPSOriginAndSecretFile(t *testing.T) {
	if got, err := exactHTTPSOrigin("https://invoice.solov.cc"); err != nil || got != "https://invoice.solov.cc" {
		t.Fatalf("origin=%q err=%v", got, err)
	}
	for _, value := range []string{"http://invoice.solov.cc", "https://invoice.solov.cc/path", "https://user@invoice.solov.cc", "https://invoice.solov.cc?token=x"} {
		if _, err := exactHTTPSOrigin(value); err == nil {
			t.Errorf("unsafe origin accepted: %s", value)
		}
	}
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if value, err := readSecretLine(path, 64); err != nil || value != "value" {
		t.Fatalf("secret=%q err=%v", value, err)
	}
	if err := os.WriteFile(path, []byte("one\ntwo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecretLine(path, 64); err == nil {
		t.Fatal("multi-line secret accepted")
	}
}

func TestBoundedOIDCResponseSizeEnvironment(t *testing.T) {
	t.Setenv("OIDC_MAX_HTTP_RESPONSE_BYTES", "1048576")
	if value, err := boundedInt64Env("OIDC_MAX_HTTP_RESPONSE_BYTES", 1<<20, 64<<10, 4<<20); err != nil || value != 1<<20 {
		t.Fatalf("response limit=%d err=%v", value, err)
	}
	for _, value := range []string{"65535", "4194305", "not-an-integer"} {
		t.Setenv("OIDC_MAX_HTTP_RESPONSE_BYTES", value)
		if _, err := boundedInt64Env("OIDC_MAX_HTTP_RESPONSE_BYTES", 1<<20, 64<<10, 4<<20); err == nil {
			t.Fatalf("unsafe response limit accepted: %q", value)
		}
	}
}
