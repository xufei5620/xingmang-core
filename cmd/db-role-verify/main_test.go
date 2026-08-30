package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/dbroles"
)

func TestRunRequiresManagedDatabaseURL(t *testing.T) {
	var out, err bytes.Buffer
	code := run(nil, &out, &err, func(string) (string, bool) { return "", false })
	if code != exitConfig {
		t.Fatalf("exit=%d, want %d", code, exitConfig)
	}
	if !strings.Contains(err.String(), "DATABASE_URL") || strings.Contains(err.String(), "postgres://") {
		t.Fatalf("unexpected error output: %q", err.String())
	}
}

func TestRunRejectsUnsafeDSNWithoutLeakingIt(t *testing.T) {
	var out, err bytes.Buffer
	secret := "hunter2"
	code := run([]string{"--policy", filepath.FromSlash("contracts/database/role-policy.v1.json")}, &out, &err, func(string) (string, bool) { return "postgres://reader:" + secret + "@db.example/xingmang", true })
	if code != exitConfig {
		t.Fatalf("exit=%d, want %d", code, exitConfig)
	}
	if strings.Contains(out.String()+err.String(), secret) {
		t.Fatal("DSN password leaked")
	}
}

func TestParseUTCIsCanonicalAndUTCOnly(t *testing.T) {
	if _, ok := parseUTC("2026-08-30T10:00:00Z"); !ok {
		t.Fatal("canonical UTC rejected")
	}
	for _, value := range []string{"2026-08-30T10:00:00+00:00", "2026-08-30T10:00:00.000Z", "2026-08-30T18:00:00+08:00", "not-a-time"} {
		if _, ok := parseUTC(value); ok {
			t.Fatalf("non-canonical timestamp accepted: %s", value)
		}
	}
}

func TestFormatViolationDoesNotIncludeSensitiveValues(t *testing.T) {
	text := formatViolation(ViolationForTest("CATALOG_ROLE_ATTRIBUTES_DRIFT", "xm_api_runtime", "xm_api_a", "pg_roles"))
	if strings.Contains(text, "postgres://") || strings.Contains(text, "password") {
		t.Fatalf("unsafe output: %q", text)
	}
}

func ViolationForTest(code, capability, identity, object string) dbroles.Violation {
	return dbroles.Violation{Code: code, Capability: capability, Identity: identity, Object: object, Message: "role attributes differ"}
}
