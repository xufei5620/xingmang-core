package main

import (
	"context"
	"errors"
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

// TestSourceRuntimeReadinessAllowsActiveRescanGrace is the /readyz-layer
// counterpart of postgresstore's evaluateSourceStreamHealth tests
// (XM-INV-AGENT-RESTART-GRACE part A): validateSourceRuntimeReadiness must
// accept Ready=true streams carrying only the non-fatal ECONOMIC_RESCAN_ACTIVE
// reason, and must accept a not-Ready stream whose only reasons are
// EVENTS_PENDING and/or ECONOMIC_RESCAN_ACTIVE together -- mirroring the
// pre-existing EVENTS_PENDING-alone tolerance -- while still rejecting any
// other reason, whether alone or alongside these two.
func TestSourceRuntimeReadinessAllowsActiveRescanGrace(t *testing.T) {
	buildReport := func(mutate func(*postgresstore.SourceStreamHealth)) postgresstore.SourceHealthReport {
		report := postgresstore.SourceHealthReport{Ready: true}
		for _, source := range []struct {
			id         string
			sourceType domain.SourceType
		}{{"sub2", domain.SourceSub2API}, {"new", domain.SourceNewAPI}} {
			for _, stream := range []string{"payments", "identities", "usage", "credits", "balances"} {
				item := postgresstore.SourceStreamHealth{
					SourceInstanceID: source.id, SourceType: source.sourceType, SourceEnabled: true,
					StreamID: stream, Ready: true,
				}
				report.Items = append(report.Items, item)
			}
		}
		mutate(&report.Items[0])
		report.Ready = report.Items[0].Ready
		for _, item := range report.Items[1:] {
			report.Ready = report.Ready && item.Ready
		}
		return report
	}

	for name, fixture := range map[string]struct {
		mutate func(*postgresstore.SourceStreamHealth)
		wantOK bool
	}{
		"ready with only the active-rescan reason is accepted": {
			mutate: func(item *postgresstore.SourceStreamHealth) {
				item.Reasons = []string{"ECONOMIC_RESCAN_ACTIVE"}
			},
			wantOK: true,
		},
		"not ready with only pending plus active-rescan is accepted, same as pending alone": {
			mutate: func(item *postgresstore.SourceStreamHealth) {
				item.Ready, item.PendingEvents, item.Reasons = false, 1, []string{"ECONOMIC_RESCAN_ACTIVE", "EVENTS_PENDING"}
			},
			wantOK: true,
		},
		"ready with the active-rescan reason plus pending events is inconsistent": {
			mutate: func(item *postgresstore.SourceStreamHealth) {
				item.PendingEvents, item.Reasons = 1, []string{"ECONOMIC_RESCAN_ACTIVE"}
			},
			wantOK: false,
		},
		"ready with the active-rescan reason plus a genuine fatal reason is inconsistent": {
			mutate: func(item *postgresstore.SourceStreamHealth) {
				item.Ready, item.Reasons = false, []string{"ECONOMIC_RESCAN_ACTIVE", "STREAM_STALE"}
			},
			wantOK: false,
		},
		"not ready with only the active-rescan reason and no pending events is inconsistent": {
			mutate: func(item *postgresstore.SourceStreamHealth) {
				item.Ready, item.Reasons = false, []string{"ECONOMIC_RESCAN_ACTIVE"}
			},
			wantOK: false,
		},
		"dead events alongside the active-rescan reason still fails closed": {
			mutate: func(item *postgresstore.SourceStreamHealth) {
				item.Ready, item.PendingEvents, item.DeadEvents, item.Reasons = false, 1, 1, []string{"ECONOMIC_RESCAN_ACTIVE", "EVENTS_PENDING"}
			},
			wantOK: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			report := buildReport(fixture.mutate)
			err := validateSourceRuntimeReadiness(report)
			if (err == nil) != fixture.wantOK {
				t.Fatalf("validateSourceRuntimeReadiness err=%v want ok=%t report_item=%#v", err, fixture.wantOK, report.Items[0])
			}
		})
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

func TestEligibilityProjectionReadyDistinguishesProofPendingFromStuck(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for name, fixture := range map[string]struct {
		health postgresstore.EligibilityProjectionHealth
		ready  bool
	}{
		"empty":                   {health: postgresstore.EligibilityProjectionHealth{}, ready: true},
		"dead alone":              {health: postgresstore.EligibilityProjectionHealth{Dead: 1}, ready: false},
		"fresh oldest pending":    {health: postgresstore.EligibilityProjectionHealth{Queued: 1, OldestPending: now.Add(-14 * time.Minute)}, ready: true},
		"oldest pending at limit": {health: postgresstore.EligibilityProjectionHealth{Queued: 1, OldestPending: now.Add(-15 * time.Minute)}, ready: true},
		"oldest pending too old":  {health: postgresstore.EligibilityProjectionHealth{Queued: 1, OldestPending: now.Add(-15*time.Minute - time.Nanosecond)}, ready: false},
		"proof pending never fails alone": {
			health: postgresstore.EligibilityProjectionHealth{Queued: 1, ProofPending: 1, OldestProofPending: now.Add(-6 * time.Hour)},
			ready:  true,
		},
		"proof pending alongside a stuck job still fails": {
			health: postgresstore.EligibilityProjectionHealth{
				Queued: 2, OldestPending: now.Add(-20 * time.Minute),
				ProofPending: 1, OldestProofPending: now.Add(-6 * time.Hour),
			},
			ready: false,
		},
		"dead overrides a fresh oldest pending": {health: postgresstore.EligibilityProjectionHealth{Dead: 1, OldestPending: now}, ready: false},
		// XM-INV-PROJECTION-FAILURE-GRADING: a job merely retrying with
		// backoff (Retrying>0, EligibilityProjectionHealth.Queued and
		// attempts>0) is never terminal and must never make /readyz
		// unhealthy by itself -- EligibilityProjectionHealth's own query
		// already keeps such a job out of OldestPending while its backoff
		// has not elapsed, so this is ready purely because OldestPending
		// stays zero here, exactly like the "proof pending never fails
		// alone" case above.
		"retrying account alone never fails": {
			health: postgresstore.EligibilityProjectionHealth{Queued: 1, Retrying: 1}, ready: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := eligibilityProjectionReady(fixture.health, now)
			if (err == nil) != fixture.ready {
				t.Fatalf("ready=%t err=%v", fixture.ready, err)
			}
		})
	}
}

func TestShouldWarnProofPendingRateLimitsToOncePerInterval(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	stale := postgresstore.EligibilityProjectionHealth{ProofPending: 1, OldestProofPending: now.Add(-61 * time.Minute)}
	for name, fixture := range map[string]struct {
		health       postgresstore.EligibilityProjectionHealth
		lastWarnedAt time.Time
		want         bool
	}{
		"no proof pending":                                              {health: postgresstore.EligibilityProjectionHealth{}, want: false},
		"proof pending but fresh":                                       {health: postgresstore.EligibilityProjectionHealth{ProofPending: 1, OldestProofPending: now.Add(-59 * time.Minute)}, want: false},
		"exactly at the age limit":                                      {health: postgresstore.EligibilityProjectionHealth{ProofPending: 1, OldestProofPending: now.Add(-60 * time.Minute)}, want: false},
		"stale, never warned before":                                    {health: stale, want: true},
		"stale, warned a minute ago":                                    {health: stale, lastWarnedAt: now.Add(-time.Minute), want: false},
		"stale, warned exactly one interval ago":                        {health: stale, lastWarnedAt: now.Add(-5 * time.Minute), want: true},
		"stale, warned just under one interval ago":                     {health: stale, lastWarnedAt: now.Add(-5*time.Minute + time.Second), want: false},
		"zero OldestProofPending is ignored even with a positive count": {health: postgresstore.EligibilityProjectionHealth{ProofPending: 1}, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			got := shouldWarnProofPending(fixture.health, now, fixture.lastWarnedAt)
			if got != fixture.want {
				t.Fatalf("shouldWarnProofPending=%t want %t", got, fixture.want)
			}
		})
	}
}

func TestEligibilityProofPendingWarnerRateLimitsAcrossCalls(t *testing.T) {
	warner := &eligibilityProofPendingWarner{}
	now := time.Now().UTC().Truncate(time.Second)
	stale := postgresstore.EligibilityProjectionHealth{ProofPending: 1, OldestProofPending: now.Add(-2 * time.Hour)}

	warner.warnIfStale(stale, now)
	if !warner.lastAt.Equal(now) {
		t.Fatalf("first stale evaluation did not record a warning: lastAt=%v", warner.lastAt)
	}

	soonAfter := now.Add(time.Minute)
	warner.warnIfStale(stale, soonAfter)
	if !warner.lastAt.Equal(now) {
		t.Fatalf("warner fired again inside the rate-limit interval: lastAt=%v", warner.lastAt)
	}

	afterInterval := now.Add(eligibilityProofPendingWarnInterval)
	warner.warnIfStale(stale, afterInterval)
	if !warner.lastAt.Equal(afterInterval) {
		t.Fatalf("warner did not fire again once the interval elapsed: lastAt=%v", warner.lastAt)
	}
}

// TestReadyzAcceptsContainedDeadAcrossRuleAndConsumer runs the rule and its
// consumer as one chain (XM-INV-DEAD-CONTAINMENT). The rule lives in
// postgresstore (evaluateSourceStreamHealth); the judgment lives here
// (validateSourceRuntimeReadiness); nothing in the compiler makes them agree.
// A test that hand-wrote the Reasons it expected would prove only that this
// file agrees with itself, so the Reasons come from
// postgresstore.EvaluateSourceStreamHealth and go into the validator
// untouched.
func TestReadyzAcceptsContainedDeadAcrossRuleAndConsumer(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	policy := postgresstore.SourceFreshnessPolicy{
		EconomicHeartbeatMaxAge: 5 * time.Minute, EconomicWatermarkMaxAge: 15 * time.Minute,
		IdentitiesMaxAge: 15 * time.Minute, Now: now,
	}
	buildEvaluatedReport := func(mutate func(*postgresstore.SourceStreamHealth)) postgresstore.SourceHealthReport {
		report := postgresstore.SourceHealthReport{Ready: true}
		for _, source := range []struct {
			id         string
			sourceType domain.SourceType
		}{{"sub2", domain.SourceSub2API}, {"new", domain.SourceNewAPI}} {
			for _, stream := range []string{"payments", "identities", "usage", "credits", "balances"} {
				item := postgresstore.SourceStreamHealth{
					SourceInstanceID: source.id, SourceType: source.sourceType, SourceEnabled: true,
					StreamID: stream, ApprovedRuntimeVersion: "0.1.179", ObservedRuntimeVersion: "0.1.179",
					ProjectionStatus: "healthy", LastAcceptedAt: now.Add(-30 * time.Second),
					EconomicWatermarkAt: now.Add(-time.Minute),
					DeadEvents:          1, ContainedDeadEvents: 1,
				}
				mutate(&item)
				postgresstore.EvaluateSourceStreamHealth(&item, policy)
				report.Items = append(report.Items, item)
			}
		}
		report.Ready = true
		for _, item := range report.Items {
			report.Ready = report.Ready && item.Ready
		}
		return report
	}

	t.Run("every stream contained is ready and accepted", func(t *testing.T) {
		report := buildEvaluatedReport(func(*postgresstore.SourceStreamHealth) {})
		if !report.Ready || len(report.Items[0].Reasons) != 1 || report.Items[0].Reasons[0] != "EVENTS_DEAD_CONTAINED" {
			t.Fatalf("the rule did not produce the shape under test: ready=%t reasons=%v", report.Ready, report.Items[0].Reasons)
		}
		if err := validateSourceRuntimeReadiness(report); err != nil {
			t.Fatalf("readyz rejected a fully contained report: %v", err)
		}
	})

	t.Run("contained dead beside pending events is still accepted", func(t *testing.T) {
		report := buildEvaluatedReport(func(item *postgresstore.SourceStreamHealth) {
			item.PendingEvents = 1
		})
		if report.Ready {
			t.Fatal("pending events must still make a stream not ready")
		}
		if err := validateSourceRuntimeReadiness(report); err != nil {
			t.Fatalf("readyz rejected the pending-plus-contained combination: %v", err)
		}
	})

	// The honesty gate (B4). This branch exists solely to catch "ready despite
	// dead events", so a report that claims containment without saying so must
	// not slip through it. The Reasons are hand-cleared here on purpose: this
	// is the one case where the rule and the report are deliberately made to
	// disagree, which is exactly what the gate is for.
	t.Run("ready with contained dead but no reason naming it is rejected", func(t *testing.T) {
		report := buildEvaluatedReport(func(*postgresstore.SourceStreamHealth) {})
		report.Items[0].Reasons = nil
		err := validateSourceRuntimeReadiness(report)
		if err == nil || err.Error() != "ready source stream has inconsistent health evidence" {
			t.Fatalf("err=%v want the inconsistent-evidence rejection", err)
		}
	})

	t.Run("an uncontained dead event beside a contained one is still fatal", func(t *testing.T) {
		report := buildEvaluatedReport(func(item *postgresstore.SourceStreamHealth) {
			if item.SourceInstanceID == "sub2" && item.StreamID == "payments" {
				item.DeadEvents = 2
			}
		})
		if report.Ready {
			t.Fatal("an uncontained dead event must still make the report not ready")
		}
		if err := validateSourceRuntimeReadiness(report); !errors.Is(err, errSourceStreamDeadEvents) {
			t.Fatalf("err=%v want errSourceStreamDeadEvents", err)
		}
	})

	// XM-INV-DEAD-CONTAINMENT B5. This one assertion is the only thing that
	// catches inverting validateSourceIngestRuntimeReadiness's subtraction:
	// with {Dead:2,DeadContained:1} the inverted predicate is still true and
	// still returns the same sentinel, so readiness_test.go's table cannot
	// see it. The reverse is also true -- a predicate too generous
	// ("something is contained, so everything is") passes here and fails
	// there. Two tests, one half each.
	t.Run("a single contained ingest dead event does not fail readiness", func(t *testing.T) {
		health := postgresstore.SourceIngestHealth{Dead: 1, DeadContained: 1}
		if err := validateSourceIngestRuntimeReadiness(health, now); err != nil {
			t.Fatalf("a contained ingest dead event kept /readyz at 503: %v", err)
		}
	})

	// The allowed-reason sets are derived, not restated. Equality in both
	// directions: one-way containment would pass for a set that quietly
	// gained an extra tolerance.
	t.Run("the allowed reason sets are derived from the rule", func(t *testing.T) {
		nonFatal := postgresstore.NonFatalStreamHealthReasons()
		if len(readySourceStreamAllowedReasons) != len(nonFatal) {
			t.Fatalf("ready allowed=%v want exactly %v", readySourceStreamAllowedReasons, nonFatal)
		}
		for reason := range nonFatal {
			if !readySourceStreamAllowedReasons[reason] {
				t.Fatalf("ready allowed=%v want exactly %v", readySourceStreamAllowedReasons, nonFatal)
			}
		}
		if len(notReadySourceStreamAllowedReasons) != len(nonFatal)+1 || !notReadySourceStreamAllowedReasons["EVENTS_PENDING"] {
			t.Fatalf("not-ready allowed=%v want %v plus EVENTS_PENDING", notReadySourceStreamAllowedReasons, nonFatal)
		}
		for reason := range nonFatal {
			if !notReadySourceStreamAllowedReasons[reason] {
				t.Fatalf("not-ready allowed=%v want %v plus EVENTS_PENDING", notReadySourceStreamAllowedReasons, nonFatal)
			}
		}
	})

	// And the whole probe: ready, degraded, named.
	t.Run("the probe reports ready with the contained condition named", func(t *testing.T) {
		probe := healthyReadinessProbe(now)
		probe.sourceHealth = func(context.Context) (postgresstore.SourceReadinessHealth, error) {
			return postgresstore.SourceReadinessHealth{
				Ingest: postgresstore.SourceIngestHealth{Dead: 1, DeadContained: 1},
				Report: buildEvaluatedReport(func(*postgresstore.SourceStreamHealth) {}),
			}, nil
		}
		outcome, err := probe.evaluate(context.Background())
		if err != nil {
			t.Fatalf("a fully contained deployment was taken out of rotation: %v", err)
		}
		if len(outcome.Degraded) != 1 || outcome.Degraded[0] != readinessDegradedSourceIngestDeadContained {
			t.Fatalf("degraded=%v want [%s]", outcome.Degraded, readinessDegradedSourceIngestDeadContained)
		}
		if probe.containedDead.lastAt.IsZero() {
			t.Fatal("the contained-dead warning was never reached, so an unrepaired event leaves no trace at all")
		}
	})
}
