package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
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

func TestValidateEligibilityPolicyStartFailsClosedAtExactBoundary(t *testing.T) {
	want := time.Date(2026, time.August, 31, 16, 0, 0, 0, time.UTC)
	for _, configured := range []string{
		"2026-09-01T00:00:00+08:00",
		"2026-08-31T16:00:00Z",
	} {
		if err := validateEligibilityPolicyStart(configured, want); err != nil {
			t.Fatalf("equivalent boundary %q rejected: %v", configured, err)
		}
	}
	for name, fixture := range map[string]struct {
		configured string
		database   time.Time
	}{
		"missing":    {database: want},
		"invalid":    {configured: "2026-09-01", database: want},
		"env before": {configured: "2026-08-31T15:59:59.999999Z", database: want},
		"env after":  {configured: "2026-08-31T16:00:00.000001Z", database: want},
		"db before":  {configured: "2026-09-01T00:00:00+08:00", database: want.Add(-time.Microsecond)},
		"db after":   {configured: "2026-09-01T00:00:00+08:00", database: want.Add(time.Microsecond)},
		"db missing": {configured: "2026-09-01T00:00:00+08:00"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateEligibilityPolicyStart(fixture.configured, fixture.database); err == nil {
				t.Fatal("invalid eligibility policy boundary was accepted")
			}
		})
	}
}
