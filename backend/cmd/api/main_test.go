package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"invoice-system/backend/internal/adminsettings"
	"invoice-system/backend/internal/domain"
	"invoice-system/backend/internal/postgresstore"
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

func TestSourceFreshnessBudgetMustCoverSafetyDelayAndPoll(t *testing.T) {
	if err := validateSourceFreshnessBudget(15*time.Minute, 5*time.Minute, time.Minute); err != nil {
		t.Fatalf("reviewed production freshness budget was rejected: %v", err)
	}
	for _, maximumAge := range []time.Duration{5 * time.Minute, 11 * time.Minute, 12*time.Minute - time.Nanosecond} {
		if err := validateSourceFreshnessBudget(maximumAge, 5*time.Minute, time.Minute); err == nil {
			t.Fatalf("impossible freshness budget %s was accepted", maximumAge)
		}
	}
	if err := validateSourceFreshnessBudget(12*time.Minute, 5*time.Minute, time.Minute); err != nil {
		t.Fatalf("minimum safety/skew/two-poll freshness headroom was rejected: %v", err)
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

func TestValidateIssuerReadinessRejectsBootstrapAndLegacyPlaceholders(t *testing.T) {
	for name, fixture := range map[string]struct {
		issuer string
		ready  bool
	}{
		"empty":                  {issuer: "", ready: false},
		"canonical placeholder":  {issuer: "待配置开票主体", ready: false},
		"production placeholder": {issuer: " 待配置实际开票主体（上线前必须修改） ", ready: false},
		"legacy placeholder":     {issuer: "请替换为实际开票主体全称", ready: false},
		"real issuer":            {issuer: "示例科技有限公司", ready: true},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateIssuerReadiness(adminsettings.Settings{IssuerName: fixture.issuer})
			if (err == nil) != fixture.ready {
				t.Fatalf("issuer=%q ready=%t err=%v", fixture.issuer, fixture.ready, err)
			}
		})
	}
}

func TestSourceRuntimeReadinessAllowsOnlyBoundedPendingWork(t *testing.T) {
	report := postgresstore.SourceHealthReport{Ready: false}
	for _, source := range []struct {
		id         string
		sourceType domain.SourceType
	}{{"sub2", domain.SourceSub2API}, {"new", domain.SourceNewAPI}} {
		for _, stream := range []string{"payments", "identities", "usage", "credits", "balances"} {
			report.Items = append(report.Items, postgresstore.SourceStreamHealth{
				SourceInstanceID: source.id, SourceType: source.sourceType, SourceEnabled: true,
				StreamID: stream, PendingEvents: 1, Ready: false, Reasons: []string{"EVENTS_PENDING"},
			})
		}
	}
	if err := validateSourceRuntimeReadiness(report); err != nil {
		t.Fatalf("bounded processing window removed API readiness: %v", err)
	}

	stale := report
	stale.Items = append([]postgresstore.SourceStreamHealth(nil), report.Items...)
	stale.Items[0].Reasons = []string{"EVENTS_PENDING", "STREAM_STALE"}
	if err := validateSourceRuntimeReadiness(stale); err == nil {
		t.Fatal("stale source stream was accepted as runtime-ready")
	}

	incomplete := report
	incomplete.Items = append([]postgresstore.SourceStreamHealth(nil), report.Items[:len(report.Items)-1]...)
	if err := validateSourceRuntimeReadiness(incomplete); err == nil {
		t.Fatal("incomplete source stream set was accepted as runtime-ready")
	}
}

func TestSourceRuntimeReadinessRejectsInconsistentHealthEvidence(t *testing.T) {
	healthy := postgresstore.SourceHealthReport{Ready: true}
	for _, source := range []struct {
		id         string
		sourceType domain.SourceType
	}{{"sub2", domain.SourceSub2API}, {"new", domain.SourceNewAPI}} {
		for _, stream := range []string{"payments", "identities", "usage", "credits", "balances"} {
			healthy.Items = append(healthy.Items, postgresstore.SourceStreamHealth{
				SourceInstanceID: source.id, SourceType: source.sourceType, SourceEnabled: true,
				StreamID: stream, Ready: true,
			})
		}
	}
	if err := validateSourceRuntimeReadiness(healthy); err != nil {
		t.Fatalf("healthy source report rejected: %v", err)
	}

	for name, mutate := range map[string]func(*postgresstore.SourceHealthReport){
		"false without reason": func(report *postgresstore.SourceHealthReport) {
			report.Items[0].Ready = false
			report.Ready = false
		},
		"pending without reason": func(report *postgresstore.SourceHealthReport) {
			report.Items[0].Ready = false
			report.Items[0].PendingEvents = 1
			report.Ready = false
		},
		"ready with pending reason": func(report *postgresstore.SourceHealthReport) {
			report.Items[0].PendingEvents = 1
			report.Items[0].Reasons = []string{"EVENTS_PENDING"}
		},
		"dead without reason": func(report *postgresstore.SourceHealthReport) {
			report.Items[0].DeadEvents = 1
		},
		"report ready mismatch": func(report *postgresstore.SourceHealthReport) {
			report.Items[0].Ready = false
			report.Items[0].PendingEvents = 1
			report.Items[0].Reasons = []string{"EVENTS_PENDING"}
		},
		"duplicate stream": func(report *postgresstore.SourceHealthReport) {
			report.Items = append(report.Items, report.Items[0])
		},
		"blank source id": func(report *postgresstore.SourceHealthReport) {
			report.Items[0].SourceInstanceID = ""
		},
		"empty report": func(report *postgresstore.SourceHealthReport) {
			report.Ready = false
			report.Items = nil
		},
		"missing source type": func(report *postgresstore.SourceHealthReport) {
			report.Items = report.Items[:5]
		},
		"unsupported stream": func(report *postgresstore.SourceHealthReport) {
			report.Items[0].StreamID = "unknown"
		},
		"unsupported source type": func(report *postgresstore.SourceHealthReport) {
			report.Items[0].SourceType = domain.SourceType("unknown")
		},
	} {
		t.Run(name, func(t *testing.T) {
			report := healthy
			report.Items = append([]postgresstore.SourceStreamHealth(nil), healthy.Items...)
			mutate(&report)
			if err := validateSourceRuntimeReadiness(report); err == nil {
				t.Fatal("inconsistent source health was accepted")
			}
		})
	}
}

func TestSourceIngestRuntimeReadinessBoundsPendingAge(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for name, fixture := range map[string]struct {
		health postgresstore.SourceIngestHealth
		ready  bool
	}{
		"empty":         {health: postgresstore.SourceIngestHealth{}, ready: true},
		"fresh pending": {health: postgresstore.SourceIngestHealth{Pending: 3, OldestPending: now.Add(-14*time.Minute - 59*time.Second)}, ready: true},
		"exact limit":   {health: postgresstore.SourceIngestHealth{Pending: 1, OldestPending: now.Add(-15 * time.Minute)}, ready: true},
		"too old":       {health: postgresstore.SourceIngestHealth{Pending: 1, OldestPending: now.Add(-15*time.Minute - time.Nanosecond)}},
		"dead":          {health: postgresstore.SourceIngestHealth{Dead: 1}},
		"missing age":   {health: postgresstore.SourceIngestHealth{Pending: 1}},
		"future age":    {health: postgresstore.SourceIngestHealth{Pending: 1, OldestPending: now.Add(6 * time.Minute)}},
		"phantom age":   {health: postgresstore.SourceIngestHealth{OldestPending: now.Add(-time.Minute)}},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateSourceIngestRuntimeReadiness(fixture.health, now)
			if (err == nil) != fixture.ready {
				t.Fatalf("ready=%t err=%v", fixture.ready, err)
			}
		})
	}
}
