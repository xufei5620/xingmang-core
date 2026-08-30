package cpaboundary

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func validEvidence() EvidenceBundle {
	return EvidenceBundle{
		Version: 1, ObservedAt: time.Now().UTC(), TargetVersion: "1.0.0", CPAImageDigest: strings.Repeat("b", 64), RouteInventorySHA256: digest('a'),
		Config:  ConfigProjection{Complete: true, AllowRemoteKnown: true, AllowRemote: false, ManagementPasswordPresent: false, RawPort: 8317, RawBind: "127.0.0.1"},
		Process: ProcessProjection{Complete: true, Args: []string{"cpa", "--listen=127.0.0.1:8317"}, Environment: map[string]string{"MODE": "production"}},
		Routes: RouteProjection{Complete: true, InferenceHostname: "infer.example", CallbackHostname: "callback.example", AllowRedirect: false, IPv6Covered: true, Routes: []ObservedRoute{
			{Plane: "inference", Hostname: "infer.example", Method: "POST", Path: "/v1/chat/completions", Public: true},
			{Plane: "callback", Hostname: "callback.example", Method: "GET", Path: "/v0/management/oauth-callback", Public: true},
			{Plane: "callback", Hostname: "callback.example", Method: "POST", Path: "/v0/management/oauth-callback", Public: true},
		}},
		Firewall: FirewallProjection{Complete: true, RawPortBlockedIPv4: true, RawPortBlockedIPv6: true},
		Adapter:  AdapterProjection{Complete: true, Public: false, RequiresMTLS: true, HasMTLS: true, RedirectAllowed: false, SecretInConfig: false, SecretInArgs: false},
		Digests:  DigestProjection{Complete: true, ImageSHA256: strings.Repeat("b", 64), ConfigSHA256: strings.Repeat("c", 64), RouteInventorySHA256: digest('a')},
	}
}

func TestAuditOfflineReturnsCompleteForSafeStructuredEvidence(t *testing.T) {
	report, err := AuditOffline(validBoundary(), validEvidence())
	if err != nil {
		t.Fatal(err)
	}
	if !report.Complete {
		t.Fatalf("safe evidence should be complete: %+v", report)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("safe evidence produced findings: %+v", report.Findings)
	}
	if report.ContractHash == "" || report.CPAImageDigest != strings.Repeat("b", 64) {
		t.Fatalf("report digests missing: %+v", report)
	}
}

func TestAuditOfflineFailsClosedForUnsafeFacts(t *testing.T) {
	mutate := map[string]func(*EvidenceBundle){
		"raw-port":            func(e *EvidenceBundle) { e.Config.RawBind = "0.0.0.0" },
		"allow-remote":        func(e *EvidenceBundle) { e.Config.AllowRemote = true },
		"management-password": func(e *EvidenceBundle) { e.Config.ManagementPasswordPresent = true },
		"wildcard-management": func(e *EvidenceBundle) {
			e.Routes.Routes = append(e.Routes.Routes, ObservedRoute{Plane: "callback", Hostname: "callback.example", Method: "GET", Path: "/v0/management/*", Public: true})
		},
		"callback-sibling": func(e *EvidenceBundle) {
			e.Routes.Routes = append(e.Routes.Routes, ObservedRoute{Plane: "callback", Hostname: "callback.example", Method: "GET", Path: "/v0/management/oauth-callback/status", Public: true})
		},
		"ipv6":           func(e *EvidenceBundle) { e.Firewall.RawPortBlockedIPv6 = false; e.Routes.IPv6Covered = false },
		"adapter-public": func(e *EvidenceBundle) { e.Adapter.Public = true },
		"adapter-mtls":   func(e *EvidenceBundle) { e.Adapter.HasMTLS = false },
		"secret-config":  func(e *EvidenceBundle) { e.Adapter.SecretInConfig = true },
		"redirect":       func(e *EvidenceBundle) { e.Routes.AllowRedirect = true },
		"route-drift":    func(e *EvidenceBundle) { e.RouteInventorySHA256 = strings.Repeat("f", 64) },
	}
	for name, change := range mutate {
		t.Run(name, func(t *testing.T) {
			e := validEvidence()
			change(&e)
			report, err := AuditOffline(validBoundary(), e)
			if err != nil {
				t.Fatal(err)
			}
			if report.Complete && len(report.Findings) == 0 {
				t.Fatalf("unsafe evidence unexpectedly passed: %+v", report)
			}
			for _, finding := range report.Findings {
				if strings.Contains(finding.Resource, "secret") || strings.Contains(finding.Resource, "password") {
					t.Fatalf("finding leaked sensitive resource: %+v", finding)
				}
			}
		})
	}
}

func TestAuditOfflineIncompleteEvidenceNeverPasses(t *testing.T) {
	e := validEvidence()
	e.Routes.Complete = false
	report, err := AuditOffline(validBoundary(), e)
	if err != nil {
		t.Fatal(err)
	}
	if report.Complete || len(report.Findings) == 0 {
		t.Fatalf("incomplete evidence must be explicit: %+v", report)
	}
	if report.Decision == "pass" {
		t.Fatal("incomplete evidence received pass decision")
	}
}

func TestAuditOfflineNeverEchoesRawSecretFields(t *testing.T) {
	e := validEvidence()
	e.Process.Environment["Authorization"] = "redacted-value"
	e.Process.Args = append(e.Process.Args, "--secret=redacted-value")
	report, err := AuditOffline(validBoundary(), e)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "redacted-value") || strings.Contains(string(raw), "Authorization") {
		t.Fatalf("report echoed sensitive process data: %s", raw)
	}
}

func TestLoadEvidenceBundleRejectsUnknownAndDuplicateFields(t *testing.T) {
	raw, err := json.Marshal(validEvidence())
	if err != nil {
		t.Fatal(err)
	}
	unknown := strings.TrimSuffix(string(raw), "}") + `,"unexpected":true}`
	if _, err := LoadEvidenceBundle([]byte(unknown)); err == nil {
		t.Fatal("unknown evidence field accepted")
	}
	duplicate := strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1)
	if _, err := LoadEvidenceBundle([]byte(duplicate)); err == nil {
		t.Fatal("duplicate evidence field accepted")
	}
}

func TestEvidenceRequiresFreshnessAndExactCallbackPlane(t *testing.T) {
	e := validEvidence()
	e.ObservedAt = time.Time{}
	if err := e.Validate(); err == nil {
		t.Fatal("zero observed_at accepted")
	}
	e = validEvidence()
	e.ObservedAt = time.Now().UTC().Add(10 * time.Minute)
	if err := e.Validate(); err == nil {
		t.Fatal("future observed_at accepted")
	}
	e = validEvidence()
	for i := range e.Routes.Routes {
		if e.Routes.Routes[i].Path == CallbackPath && e.Routes.Routes[i].Method == "GET" {
			e.Routes.Routes[i].Plane = "inference"
		}
	}
	report, err := AuditOffline(validBoundary(), e)
	if err != nil {
		t.Fatal(err)
	}
	if report.Decision == "pass" {
		t.Fatal("inference route masquerading as callback passed")
	}
}

func TestAuditOfflineChecksRawPortAndHostnameShape(t *testing.T) {
	e := validEvidence()
	e.Config.RawPort = 9000
	report, err := AuditOffline(validBoundary(), e)
	if err != nil {
		t.Fatal(err)
	}
	if report.Decision == "pass" {
		t.Fatal("unexpected raw port passed")
	}
	e = validEvidence()
	e.Routes.CallbackHostname = "Callback.EXAMPLE"
	if err := e.Validate(); err == nil {
		t.Fatal("non-canonical callback hostname accepted")
	}
}

func TestAuditOfflineRejectsQueryAbsoluteAndRedirectRouteVariants(t *testing.T) {
	e := validEvidence()
	e.Routes.Routes = append(e.Routes.Routes,
		ObservedRoute{Plane: "callback", Hostname: e.Routes.CallbackHostname, Method: "GET", Path: "/v0/management/oauth-callback?redirect_url=https://bad.invalid", Public: true},
		ObservedRoute{Plane: "inference", Hostname: e.Routes.InferenceHostname, Method: "GET", Path: "/v1/http://bad.invalid", Public: true},
	)
	report, err := AuditOffline(validBoundary(), e)
	if err != nil {
		t.Fatal(err)
	}
	if report.Decision == "pass" {
		t.Fatal("query/absolute route variants passed")
	}
}

func TestLoadEvidenceMissingTargetIsReportedPartialByAudit(t *testing.T) {
	e := validEvidence()
	e.TargetVersion = ""
	e.CPAImageDigest = ""
	e.RouteInventorySHA256 = ""
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadEvidenceBundle(data)
	if err != nil {
		t.Fatalf("missing target facts should parse for partial audit: %v", err)
	}
	report, err := AuditOffline(validBoundary(), loaded)
	if err != nil {
		t.Fatal(err)
	}
	if report.Decision != "partial" || report.Complete {
		t.Fatalf("missing target facts must be partial: %+v", report)
	}
}

func TestAuditOfflineRequiresExplicitMTLS(t *testing.T) {
	e := validEvidence()
	e.Adapter.RequiresMTLS = false
	e.Adapter.HasMTLS = false
	report, err := AuditOffline(validBoundary(), e)
	if err != nil {
		t.Fatal(err)
	}
	if report.Decision == "pass" {
		t.Fatal("adapter without explicit mTLS passed")
	}
}
